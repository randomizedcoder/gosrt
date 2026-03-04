//go:build go1.18

package send

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Sender Ring Race Table-Driven Tests
//
// Phase 3.3: Comprehensive race condition tests for sender rings.
// These tests are designed to be run with -race to detect data races.
//
// Reference: unit_test_coverage_improvement_plan.md Phase 3.3
// =============================================================================

// =============================================================================
// SendPacketRing (Data Ring) Race Tests
// =============================================================================

// TestRace_DataRing_MultiProducerSingleConsumer tests the MPSC pattern.
// This is the expected usage pattern: multiple writers, single EventLoop consumer.
func TestRace_DataRing_MultiProducerSingleConsumer(t *testing.T) {
	testCases := []struct {
		name         string
		ringSize     int
		shards       int
		numProducers int
		itemsPerProd int
		batchSize    int // 0 = TryPop, >0 = DrainBatch
	}{
		{"1shard_4prod_TryPop", 4096, 1, 4, 500, 0},
		{"1shard_4prod_DrainBatch_10", 4096, 1, 4, 500, 10},
		{"1shard_4prod_DrainBatch_100", 4096, 1, 4, 500, 100},
		{"4shard_8prod_TryPop", 4096, 4, 8, 500, 0},
		{"4shard_8prod_DrainBatch_50", 4096, 4, 8, 500, 50},
		{"small_ring_overflow", 64, 1, 4, 200, 10},
		{"high_contention", 256, 2, 16, 100, 5},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendPacketRing(tc.ringSize, tc.shards)
			require.NoError(t, err)

			var pushed, dropped, consumed atomic.Int64
			var producerWg sync.WaitGroup
			stop := make(chan struct{})
			consumerDone := make(chan struct{})

			// Single consumer (EventLoop pattern)
			go func() {
				defer close(consumerDone)
				for {
					select {
					case <-stop:
						// Final drain with retry logic to handle visibility delays
						emptyCount := 0
						const maxEmptyRetries = 10
						for emptyCount < maxEmptyRetries {
							if tc.batchSize > 0 {
								batch := ring.DrainBatch(tc.batchSize)
								if len(batch) > 0 {
									consumed.Add(int64(len(batch)))
									emptyCount = 0
								} else {
									emptyCount++
									time.Sleep(100 * time.Microsecond)
								}
							} else {
								if _, ok := ring.TryPop(); ok {
									consumed.Add(1)
									emptyCount = 0
								} else {
									emptyCount++
									time.Sleep(100 * time.Microsecond)
								}
							}
						}
						return
					default:
						if tc.batchSize > 0 {
							batch := ring.DrainBatch(tc.batchSize)
							consumed.Add(int64(len(batch)))
						} else {
							if _, ok := ring.TryPop(); ok {
								consumed.Add(1)
							}
						}
					}
				}
			}()

			// Multiple producers
			for p := 0; p < tc.numProducers; p++ {
				producerWg.Add(1)
				go func(producerID int) {
					defer producerWg.Done()
					baseSeq := uint32(producerID * tc.itemsPerProd)
					for i := 0; i < tc.itemsPerProd; i++ {
						pkt := createTestPacket(baseSeq + uint32(i))
						if ring.Push(pkt) {
							pushed.Add(1)
						} else {
							dropped.Add(1)
						}
					}
				}(p)
			}

			// Wait for all producers to finish, then stop consumer
			producerWg.Wait()
			close(stop)
			<-consumerDone

			total := int64(tc.numProducers * tc.itemsPerProd)
			t.Logf("%s: pushed=%d, dropped=%d, consumed=%d",
				tc.name, pushed.Load(), dropped.Load(), consumed.Load())

			require.Equal(t, total, pushed.Load()+dropped.Load())
			require.Equal(t, pushed.Load(), consumed.Load())
		})
	}
}

// TestRace_DataRing_DrainAll_Concurrent tests DrainAll under concurrent Push.
func TestRace_DataRing_DrainAll_Concurrent(t *testing.T) {
	testCases := []struct {
		name         string
		ringSize     int
		shards       int
		numProducers int
		drainPeriod  time.Duration
	}{
		{"frequent_drain", 1024, 1, 4, time.Millisecond},
		{"infrequent_drain", 1024, 1, 4, 10 * time.Millisecond},
		{"multi_shard", 1024, 4, 8, 5 * time.Millisecond},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendPacketRing(tc.ringSize, tc.shards)
			require.NoError(t, err)

			var pushed, consumed atomic.Int64
			var wg sync.WaitGroup
			stop := make(chan struct{})
			testDuration := 50 * time.Millisecond

			// Consumer with periodic DrainAll
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(tc.drainPeriod)
				defer ticker.Stop()

				for {
					select {
					case <-stop:
						// Final drain
						all := ring.DrainAll()
						consumed.Add(int64(len(all)))
						return
					case <-ticker.C:
						all := ring.DrainAll()
						consumed.Add(int64(len(all)))
					}
				}
			}()

			// Producers pushing continuously
			for p := 0; p < tc.numProducers; p++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					seq := uint32(producerID * 10000)
					for {
						select {
						case <-stop:
							return
						default:
							pkt := createTestPacket(seq)
							if ring.Push(pkt) {
								pushed.Add(1)
							}
							seq++
						}
					}
				}(p)
			}

			time.Sleep(testDuration)
			close(stop)
			wg.Wait()

			t.Logf("%s: pushed=%d, consumed=%d, remaining=%d",
				tc.name, pushed.Load(), consumed.Load(), ring.Len())

			// All pushed packets should eventually be consumed
			remaining := ring.DrainAll()
			finalConsumed := consumed.Load() + int64(len(remaining))
			require.Equal(t, pushed.Load(), finalConsumed)
		})
	}
}

// TestRace_DataRing_Len_Concurrent tests Len() reads during concurrent access.
func TestRace_DataRing_Len_Concurrent(t *testing.T) {
	ring, err := NewSendPacketRing(4096, 4)
	require.NoError(t, err)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Multiple producers
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			seq := uint32(producerID * 10000)
			for {
				select {
				case <-stop:
					return
				default:
					pkt := createTestPacket(seq)
					ring.Push(pkt)
					seq++
				}
			}
		}(p)
	}

	// Consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ring.TryPop()
			}
		}
	}()

	// Len() reader (the race detector will catch any issues)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = ring.Len()
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Just verify no race detected (test passes if no race)
	t.Logf("Len concurrent test completed, final len=%d", ring.Len())
}

// =============================================================================
// SendControlRing Race Tests
// =============================================================================

// TestRace_ControlRing_ACK_MPSC tests ACK push from multiple io_uring handlers.
func TestRace_ControlRing_ACK_MPSC(t *testing.T) {
	testCases := []struct {
		name         string
		ringSize     int
		shards       int
		numProducers int
		acksPerProd  int
	}{
		{"single_shard", 256, 1, 4, 500},
		{"dual_shard", 256, 2, 4, 500},
		{"quad_shard", 256, 4, 8, 500},
		{"high_contention", 64, 1, 8, 200},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(tc.ringSize, tc.shards)
			require.NoError(t, err)

			var pushed, dropped, consumed atomic.Int64
			var wg sync.WaitGroup
			stop := make(chan struct{})

			// Single consumer (EventLoop)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						// Final drain
						for {
							batch := ring.DrainBatch(100)
							if len(batch) == 0 {
								return
							}
							consumed.Add(int64(len(batch)))
						}
					default:
						batch := ring.DrainBatch(10)
						consumed.Add(int64(len(batch)))
					}
				}
			}()

			// Multiple ACK producers (simulating io_uring handlers)
			for p := 0; p < tc.numProducers; p++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					baseSeq := uint32(producerID * tc.acksPerProd)
					for i := 0; i < tc.acksPerProd; i++ {
						seq := circular.New(baseSeq+uint32(i), packet.MAX_SEQUENCENUMBER)
						if ring.PushACK(seq) {
							pushed.Add(1)
						} else {
							dropped.Add(1)
						}
					}
				}(p)
			}

			time.Sleep(50 * time.Millisecond)
			close(stop)
			wg.Wait()

			total := int64(tc.numProducers * tc.acksPerProd)
			t.Logf("%s: pushed=%d, dropped=%d, consumed=%d",
				tc.name, pushed.Load(), dropped.Load(), consumed.Load())

			require.Equal(t, total, pushed.Load()+dropped.Load())
			require.Equal(t, pushed.Load(), consumed.Load())
		})
	}
}

// TestRace_ControlRing_NAK_MPSC tests NAK push with chunking under race.
func TestRace_ControlRing_NAK_MPSC(t *testing.T) {
	testCases := []struct {
		name         string
		ringSize     int
		shards       int
		numProducers int
		nakSeqsCount int // Number of sequences per NAK call
		naksPerProd  int
	}{
		{"small_nak", 256, 2, 4, 5, 100},
		{"boundary_nak_32", 256, 2, 4, 32, 50},
		{"large_nak_chunked", 512, 2, 4, 64, 30},
		{"huge_nak_chunked", 1024, 4, 2, 128, 10},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(tc.ringSize, tc.shards)
			require.NoError(t, err)

			var pushed, dropped, consumed atomic.Int64
			var wg sync.WaitGroup
			stop := make(chan struct{})

			// Single consumer
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						for {
							batch := ring.DrainBatch(100)
							if len(batch) == 0 {
								return
							}
							consumed.Add(int64(len(batch)))
						}
					default:
						batch := ring.DrainBatch(10)
						consumed.Add(int64(len(batch)))
					}
				}
			}()

			// Multiple NAK producers
			for p := 0; p < tc.numProducers; p++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					baseSeq := uint32(producerID * tc.naksPerProd * tc.nakSeqsCount)
					for n := 0; n < tc.naksPerProd; n++ {
						seqs := make([]circular.Number, tc.nakSeqsCount)
						for i := 0; i < tc.nakSeqsCount; i++ {
							seqs[i] = circular.New(baseSeq+uint32(n*tc.nakSeqsCount+i), packet.MAX_SEQUENCENUMBER)
						}
						if ring.PushNAK(seqs) {
							pushed.Add(1)
						} else {
							dropped.Add(1)
						}
					}
				}(p)
			}

			time.Sleep(100 * time.Millisecond)
			close(stop)
			wg.Wait()

			total := int64(tc.numProducers * tc.naksPerProd)
			t.Logf("%s: pushed=%d, dropped=%d, consumed=%d (ring len=%d)",
				tc.name, pushed.Load(), dropped.Load(), consumed.Load(), ring.Len())

			require.Equal(t, total, pushed.Load()+dropped.Load())
		})
	}
}

// TestRace_ControlRing_Mixed_ACK_NAK tests mixed ACK/NAK concurrent access.
func TestRace_ControlRing_Mixed_ACK_NAK(t *testing.T) {
	ring, err := NewSendControlRing(512, 2)
	require.NoError(t, err)

	var ackPushed, ackDropped, nakPushed, nakDropped, consumed atomic.Int64
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				for {
					batch := ring.DrainBatch(100)
					if len(batch) == 0 {
						return
					}
					consumed.Add(int64(len(batch)))
				}
			default:
				batch := ring.DrainBatch(10)
				consumed.Add(int64(len(batch)))
			}
		}
	}()

	// ACK producers
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				seq := circular.New(uint32(producerID*1000+i), packet.MAX_SEQUENCENUMBER)
				if ring.PushACK(seq) {
					ackPushed.Add(1)
				} else {
					ackDropped.Add(1)
				}
			}
		}(p)
	}

	// NAK producers
	for p := 0; p < 2; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				seqs := make([]circular.Number, 10)
				for j := 0; j < 10; j++ {
					seqs[j] = circular.New(uint32(producerID*10000+i*10+j), packet.MAX_SEQUENCENUMBER)
				}
				if ring.PushNAK(seqs) {
					nakPushed.Add(1)
				} else {
					nakDropped.Add(1)
				}
			}
		}(p)
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Logf("ACK: pushed=%d, dropped=%d; NAK: pushed=%d, dropped=%d; consumed=%d",
		ackPushed.Load(), ackDropped.Load(), nakPushed.Load(), nakDropped.Load(), consumed.Load())

	totalAttempts := int64(4*500 + 2*100)
	require.Equal(t, totalAttempts,
		ackPushed.Load()+ackDropped.Load()+nakPushed.Load()+nakDropped.Load())
}

// =============================================================================
// SendControlRingV2 Race Tests
// =============================================================================

// TestRace_ControlRingV2_ACK_MPSC tests V2 ACK ring under race.
func TestRace_ControlRingV2_ACK_MPSC(t *testing.T) {
	testCases := []struct {
		name         string
		ackSize      int
		nakSize      int
		shards       int
		numProducers int
		acksPerProd  int
	}{
		{"default_config", 256, 64, 2, 4, 1000},
		{"high_throughput", 1024, 128, 4, 8, 1000},
		{"small_ring", 64, 32, 1, 4, 500},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRingV2(tc.ackSize, tc.nakSize, tc.shards)
			require.NoError(t, err)

			var pushed, dropped, consumed atomic.Int64
			var wg sync.WaitGroup
			stop := make(chan struct{})

			// Consumer
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						for {
							if _, ok := ring.TryPopACK(); ok {
								consumed.Add(1)
							} else {
								return
							}
						}
					default:
						if _, ok := ring.TryPopACK(); ok {
							consumed.Add(1)
						}
					}
				}
			}()

			// Producers
			for p := 0; p < tc.numProducers; p++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					baseSeq := uint32(producerID * tc.acksPerProd)
					for i := 0; i < tc.acksPerProd; i++ {
						if ring.PushACK(baseSeq + uint32(i)) {
							pushed.Add(1)
						} else {
							dropped.Add(1)
						}
					}
				}(p)
			}

			time.Sleep(50 * time.Millisecond)
			close(stop)
			wg.Wait()

			total := int64(tc.numProducers * tc.acksPerProd)
			t.Logf("%s: pushed=%d, dropped=%d, consumed=%d",
				tc.name, pushed.Load(), dropped.Load(), consumed.Load())

			require.Equal(t, total, pushed.Load()+dropped.Load())
			require.Equal(t, pushed.Load(), consumed.Load())
		})
	}
}

// TestRace_ControlRingV2_NAK_Chunking tests V2 NAK chunking under race.
func TestRace_ControlRingV2_NAK_Chunking(t *testing.T) {
	ring, err := NewSendControlRingV2(256, 128, 2)
	require.NoError(t, err)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var nakPacketsConsumed atomic.Int64

	// Consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				for {
					if _, ok := ring.TryPopNAK(); ok {
						nakPacketsConsumed.Add(1)
					} else {
						return
					}
				}
			default:
				if _, ok := ring.TryPopNAK(); ok {
					nakPacketsConsumed.Add(1)
				}
			}
		}
	}()

	// Producers pushing NAKs that require chunking (>32 seqs)
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// Create NAK with 64 sequences (will be split into 2 chunks)
				seqs := make([]circular.Number, 64)
				for j := 0; j < 64; j++ {
					seqs[j] = circular.New(uint32(producerID*100000+i*64+j), packet.MAX_SEQUENCENUMBER)
				}
				ring.PushNAK(seqs)
			}
		}(p)
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Logf("NAK packets consumed: %d (each 64-seq NAK becomes 2 packets)",
		nakPacketsConsumed.Load())

	// Each producer sends 50 NAKs of 64 seqs = 50 * 2 chunks = 100 packets per producer
	// 4 producers = 400 total packets (assuming no drops)
	// We just verify the test completes without race
}

// =============================================================================
// Integration Race Tests
// =============================================================================

// TestRace_Combined_DataAndControl tests simultaneous data and control ring access.
func TestRace_Combined_DataAndControl(t *testing.T) {
	dataRing, err := NewSendPacketRing(2048, 1)
	require.NoError(t, err)

	controlRing, err := NewSendControlRing(256, 2)
	require.NoError(t, err)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	var dataPushed, dataConsumed atomic.Int64
	var ackPushed, nakPushed, controlConsumed atomic.Int64

	// EventLoop consumer (single goroutine draining both rings)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				// Final drain
				for {
					_, ok := dataRing.TryPop()
					if ok {
						dataConsumed.Add(1)
					} else {
						break
					}
				}
				for {
					batch := controlRing.DrainBatch(100)
					if len(batch) == 0 {
						break
					}
					controlConsumed.Add(int64(len(batch)))
				}
				return
			default:
				// Drain data ring
				if _, ok := dataRing.TryPop(); ok {
					dataConsumed.Add(1)
				}
				// Drain control ring
				batch := controlRing.DrainBatch(5)
				controlConsumed.Add(int64(len(batch)))
			}
		}
	}()

	// Data producers
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				pkt := createTestPacket(uint32(producerID*1000 + i))
				if dataRing.Push(pkt) {
					dataPushed.Add(1)
				}
			}
		}(p)
	}

	// ACK producer (simulating receiver ACKs)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			seq := circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			if controlRing.PushACK(seq) {
				ackPushed.Add(1)
			}
		}
	}()

	// NAK producer (simulating receiver NAKs)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			seqs := []circular.Number{
				circular.New(uint32(i*2), packet.MAX_SEQUENCENUMBER),
				circular.New(uint32(i*2+1), packet.MAX_SEQUENCENUMBER),
			}
			if controlRing.PushNAK(seqs) {
				nakPushed.Add(1)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Logf("Data: pushed=%d, consumed=%d", dataPushed.Load(), dataConsumed.Load())
	t.Logf("Control: ACK pushed=%d, NAK pushed=%d, consumed=%d",
		ackPushed.Load(), nakPushed.Load(), controlConsumed.Load())

	require.Equal(t, dataPushed.Load(), dataConsumed.Load())
}

// TestRace_SequenceWraparound tests sequence numbers near MAX during race.
func TestRace_SequenceWraparound(t *testing.T) {
	ring, err := NewSendControlRing(512, 2)
	require.NoError(t, err)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	const nearMax = packet.MAX_SEQUENCENUMBER - 1000

	// Consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				ring.DrainBatch(10000)
				return
			default:
				ring.DrainBatch(10)
			}
		}
	}()

	// Producers pushing sequences near MAX (wraparound territory)
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				// Sequences that will wrap around
				seq := circular.New((nearMax+uint32(producerID*500+i))%packet.MAX_SEQUENCENUMBER, packet.MAX_SEQUENCENUMBER)
				ring.PushACK(seq)
			}
		}(p)
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Sequence wraparound race test completed successfully")
}

// TestRace_TryPush_vs_Push tests both methods under race.
func TestRace_TryPush_vs_Push(t *testing.T) {
	ring, err := NewSendPacketRing(256, 2)
	require.NoError(t, err)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				ring.DrainAll()
				return
			default:
				ring.TryPop()
			}
		}
	}()

	// TryPush users
	for p := 0; p < 2; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				pkt := createTestPacket(uint32(producerID*1000 + i))
				ring.TryPush(pkt)
			}
		}(p)
	}

	// Push users
	for p := 0; p < 2; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				pkt := createTestPacket(uint32(producerID*1000 + 500 + i))
				ring.Push(pkt)
			}
		}(p)
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("TryPush vs Push race test completed successfully")
}

// =============================================================================
// Benchmarks with Race Verification
// =============================================================================

func BenchmarkRace_DataRing_MPSC(b *testing.B) {
	ring, _ := NewSendPacketRing(8192, 4)

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			pkt := createTestPacket(seq)
			seq++
			if ring.Push(pkt) {
				ring.TryPop() // Simulate consumer
			}
		}
	})
}

func BenchmarkRace_ControlRing_ACK_MPSC(b *testing.B) {
	ring, _ := NewSendControlRing(4096, 4)

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			n := circular.New(seq, packet.MAX_SEQUENCENUMBER)
			seq++
			if ring.PushACK(n) {
				ring.TryPop() // Simulate consumer
			}
		}
	})
}

func BenchmarkRace_ControlRingV2_ACK(b *testing.B) {
	ring, _ := NewSendControlRingV2(4096, 256, 4)

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			seq++
			if ring.PushACK(seq) {
				ring.TryPopACK() // Simulate consumer
			}
		}
	})
}
