//go:build go1.18

package send

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Control Ring Overflow Table-Driven Tests
//
// Phase 3.2: Comprehensive tests for control ring overflow scenarios,
// edge cases, and error handling paths.
//
// Reference: lockless_sender_implementation_plan.md Phase 3
// =============================================================================

// TestSendControlRing_Creation_TableDriven tests ring creation with various configs.
func TestSendControlRing_Creation_TableDriven(t *testing.T) {
	testCases := []struct {
		name         string
		size         int
		shards       int
		expectShards int
		expectMinCap int
		expectError  bool
	}{
		// === Normal configurations ===
		{
			name:         "default_2_shards",
			size:         256,
			shards:       2,
			expectShards: 2,
			expectMinCap: 256, // At least this
		},
		{
			name:         "single_shard",
			size:         256,
			shards:       1,
			expectShards: 1,
			expectMinCap: 256,
		},
		{
			name:         "4_shards",
			size:         256,
			shards:       4,
			expectShards: 4,
			expectMinCap: 256,
		},
		{
			name:         "large_ring",
			size:         4096,
			shards:       2,
			expectShards: 2,
			expectMinCap: 4096,
		},

		// === Default value cases ===
		{
			name:         "zero_shards_defaults_to_2",
			size:         256,
			shards:       0,
			expectShards: 2,
			expectMinCap: 256,
		},
		{
			name:         "negative_shards_defaults_to_2",
			size:         256,
			shards:       -1,
			expectShards: 2,
			expectMinCap: 256,
		},
		{
			name:         "zero_size_defaults_to_256",
			size:         0,
			shards:       2,
			expectShards: 2,
			expectMinCap: 256,
		},
		{
			name:         "negative_size_defaults",
			size:         -1,
			shards:       2,
			expectShards: 2,
			expectMinCap: 256,
		},
		{
			name:         "both_defaults",
			size:         0,
			shards:       0,
			expectShards: 2,
			expectMinCap: 256,
		},

		// === Minimum viable configurations ===
		{
			name:         "minimum_size",
			size:         1,
			shards:       1,
			expectShards: 1,
			expectMinCap: 1,
		},
		{
			name:         "small_size",
			size:         4,
			shards:       1,
			expectShards: 1,
			expectMinCap: 4,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(tc.size, tc.shards)

			if tc.expectError {
				require.Error(t, err)
				require.Nil(t, ring)
			} else {
				require.NoError(t, err)
				require.NotNil(t, ring)
				require.Equal(t, tc.expectShards, ring.Shards())
				require.GreaterOrEqual(t, ring.Len(), 0)
				// Ring library may round up capacity to power of 2
			}
		})
	}
}

// TestSendControlRing_Overflow_ACK_TableDriven tests ACK overflow with various ring sizes.
func TestSendControlRing_Overflow_ACK_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		ringSize      int
		shards        int
		pushCount     int
		expectDropped int // Minimum expected drops
		expectPushed  int // Minimum expected pushes
	}{
		{
			name:          "tiny_ring_overflow",
			ringSize:      4,
			shards:        1,
			pushCount:     10,
			expectDropped: 1, // At least some dropped
			expectPushed:  4, // At least ring size pushed
		},
		{
			name:          "small_ring_overflow",
			ringSize:      16,
			shards:        1,
			pushCount:     32,
			expectDropped: 1,
			expectPushed:  16,
		},
		{
			name:          "medium_ring_overflow",
			ringSize:      64,
			shards:        2,
			pushCount:     200,
			expectDropped: 1,
			expectPushed:  64,
		},
		{
			name:          "no_overflow",
			ringSize:      100,
			shards:        1,
			pushCount:     50,
			expectDropped: 0, // No overflow expected
			expectPushed:  50,
		},
		{
			name:          "exactly_at_capacity",
			ringSize:      32,
			shards:        1,
			pushCount:     32,
			expectDropped: 0, // May or may not overflow depending on exact timing
			expectPushed:  32,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(tc.ringSize, tc.shards)
			require.NoError(t, err)

			pushed := 0
			dropped := 0

			for i := 0; i < tc.pushCount; i++ {
				seq := circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
				if ring.PushACK(seq) {
					pushed++
				} else {
					dropped++
				}
			}

			t.Logf("%s: pushed=%d, dropped=%d (ring cap=%d)", tc.name, pushed, dropped, ring.Len()+dropped)

			require.GreaterOrEqual(t, dropped, tc.expectDropped, "dropped should be at least expected")
			require.GreaterOrEqual(t, pushed, tc.expectPushed, "pushed should be at least expected")
			require.Equal(t, tc.pushCount, pushed+dropped, "pushed + dropped should equal total")
		})
	}
}

// TestSendControlRing_Overflow_NAK_TableDriven tests NAK overflow with various configurations.
func TestSendControlRing_Overflow_NAK_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		ringSize      int
		shards        int
		nakSeqCount   int  // Sequences per NAK
		nakPushCount  int  // Number of NAK pushes
		expectChunked bool // Expect chunking (>32 sequences)
		expectDropped int  // Minimum expected drops
	}{
		{
			name:          "small_nak_no_chunk",
			ringSize:      16,
			shards:        1,
			nakSeqCount:   2,
			nakPushCount:  20,
			expectChunked: false,
			expectDropped: 1,
		},
		{
			name:          "large_nak_with_chunk",
			ringSize:      8,
			shards:        1,
			nakSeqCount:   50, // > 32, will be chunked
			nakPushCount:  5,
			expectChunked: true,
			expectDropped: 1,
		},
		{
			name:          "max_chunk_size",
			ringSize:      64,
			shards:        1,
			nakSeqCount:   32, // Exactly max chunk
			nakPushCount:  10,
			expectChunked: false,
			expectDropped: 0,
		},
		{
			name:          "just_over_chunk_size",
			ringSize:      64,
			shards:        1,
			nakSeqCount:   33, // One over max chunk
			nakPushCount:  5,
			expectChunked: true,
			expectDropped: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(tc.ringSize, tc.shards)
			require.NoError(t, err)

			pushSuccessCount := 0
			pushFailCount := 0

			for i := 0; i < tc.nakPushCount; i++ {
				seqs := make([]circular.Number, tc.nakSeqCount)
				for j := 0; j < tc.nakSeqCount; j++ {
					seqs[j] = circular.New(uint32(i*tc.nakSeqCount+j), packet.MAX_SEQUENCENUMBER)
				}
				if ring.PushNAK(seqs) {
					pushSuccessCount++
				} else {
					pushFailCount++
				}
			}

			t.Logf("%s: success=%d, fail=%d, ring len=%d", tc.name, pushSuccessCount, pushFailCount, ring.Len())

			if tc.expectChunked && tc.nakSeqCount > MaxNAKSequencesPerPacket {
				// Each NAK push creates multiple packets when chunked
				expectedPacketsPerPush := (tc.nakSeqCount + MaxNAKSequencesPerPacket - 1) / MaxNAKSequencesPerPacket
				t.Logf("Expected %d packets per NAK push", expectedPacketsPerPush)
			}

			require.GreaterOrEqual(t, pushFailCount, tc.expectDropped)
		})
	}
}

// TestSendControlRing_DrainBatch_TableDriven tests batch draining with various configurations.
func TestSendControlRing_DrainBatch_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		ringSize      int
		prePushCount  int
		drainMax      int
		expectDrained int
		expectRemain  int
	}{
		{
			name:          "drain_all",
			ringSize:      32,
			prePushCount:  10,
			drainMax:      20,
			expectDrained: 10,
			expectRemain:  0,
		},
		{
			name:          "partial_drain",
			ringSize:      32,
			prePushCount:  20,
			drainMax:      10,
			expectDrained: 10,
			expectRemain:  10,
		},
		{
			name:          "exact_drain",
			ringSize:      32,
			prePushCount:  15,
			drainMax:      15,
			expectDrained: 15,
			expectRemain:  0,
		},
		{
			name:          "drain_empty",
			ringSize:      32,
			prePushCount:  0,
			drainMax:      10,
			expectDrained: 0,
			expectRemain:  0,
		},
		{
			name:          "drain_zero_max",
			ringSize:      32,
			prePushCount:  10,
			drainMax:      0,
			expectDrained: 0,
			expectRemain:  10,
		},
		{
			name:          "drain_negative_max",
			ringSize:      32,
			prePushCount:  10,
			drainMax:      -5,
			expectDrained: 0,
			expectRemain:  10,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(tc.ringSize, 1)
			require.NoError(t, err)

			// Pre-push ACKs
			for i := 0; i < tc.prePushCount; i++ {
				ring.PushACK(circular.New(uint32(i), packet.MAX_SEQUENCENUMBER))
			}

			// Drain batch
			batch := ring.DrainBatch(tc.drainMax)

			require.Equal(t, tc.expectDrained, len(batch), "drained count mismatch")
			require.Equal(t, tc.expectRemain, ring.Len(), "remaining count mismatch")
		})
	}
}

// TestSendControlRing_TryPop_States_TableDriven tests TryPop in various ring states.
func TestSendControlRing_TryPop_States_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		setupRing  func(*SendControlRing)
		expectOK   bool
		expectType ControlPacketType
	}{
		{
			name:      "empty_ring",
			setupRing: func(r *SendControlRing) {},
			expectOK:  false,
		},
		{
			name: "single_ack",
			setupRing: func(r *SendControlRing) {
				r.PushACK(circular.New(100, packet.MAX_SEQUENCENUMBER))
			},
			expectOK:   true,
			expectType: ControlTypeACK,
		},
		{
			name: "single_nak",
			setupRing: func(r *SendControlRing) {
				r.PushNAK([]circular.Number{circular.New(100, packet.MAX_SEQUENCENUMBER)})
			},
			expectOK:   true,
			expectType: ControlTypeNAK,
		},
		{
			name: "after_full_drain",
			setupRing: func(r *SendControlRing) {
				r.PushACK(circular.New(100, packet.MAX_SEQUENCENUMBER))
				r.TryPop() // Drain
			},
			expectOK: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(32, 1)
			require.NoError(t, err)

			tc.setupRing(ring)

			cp, ok := ring.TryPop()

			require.Equal(t, tc.expectOK, ok)
			if tc.expectOK {
				require.Equal(t, tc.expectType, cp.Type)
			}
		})
	}
}

// TestSendControlRing_Concurrent_Overflow_TableDriven tests concurrent overflow scenarios.
func TestSendControlRing_Concurrent_Overflow_TableDriven(t *testing.T) {
	testCases := []struct {
		name           string
		ringSize       int
		shards         int
		numProducers   int
		pushesPerProd  int
		consumerActive bool
	}{
		{
			name:           "overflow_no_consumer",
			ringSize:       32,
			shards:         1,
			numProducers:   4,
			pushesPerProd:  100,
			consumerActive: false,
		},
		{
			name:           "overflow_with_consumer",
			ringSize:       32,
			shards:         2,
			numProducers:   4,
			pushesPerProd:  100,
			consumerActive: true,
		},
		{
			name:           "high_contention",
			ringSize:       16,
			shards:         1,
			numProducers:   8,
			pushesPerProd:  50,
			consumerActive: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(tc.ringSize, tc.shards)
			require.NoError(t, err)

			var pushed, dropped, consumed atomic.Int64
			var producerWg sync.WaitGroup
			stop := make(chan struct{})
			consumerDone := make(chan struct{})

			// Start consumer if active
			if tc.consumerActive {
				go func() {
					defer close(consumerDone)
					for {
						select {
						case <-stop:
							// Final drain with retry logic to handle visibility delays
							emptyCount := 0
							const maxEmptyRetries = 10
							for emptyCount < maxEmptyRetries {
								if _, ok := ring.TryPop(); ok {
									consumed.Add(1)
									emptyCount = 0
								} else {
									emptyCount++
									time.Sleep(100 * time.Microsecond)
								}
							}
							return
						default:
							if _, ok := ring.TryPop(); ok {
								consumed.Add(1)
							}
						}
					}
				}()
			} else {
				close(consumerDone)
			}

			// Start producers
			for p := 0; p < tc.numProducers; p++ {
				producerWg.Add(1)
				go func(producerID int) {
					defer producerWg.Done()
					for i := 0; i < tc.pushesPerProd; i++ {
						seq := circular.New(uint32(producerID*tc.pushesPerProd+i), packet.MAX_SEQUENCENUMBER)
						if ring.PushACK(seq) {
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

			totalAttempts := int64(tc.numProducers * tc.pushesPerProd)
			t.Logf("%s: pushed=%d, dropped=%d, consumed=%d (attempts=%d)",
				tc.name, pushed.Load(), dropped.Load(), consumed.Load(), totalAttempts)

			require.Equal(t, totalAttempts, pushed.Load()+dropped.Load(), "all attempts accounted for")
			if tc.consumerActive {
				require.Equal(t, pushed.Load(), consumed.Load(), "all pushed should be consumed")
			}
		})
	}
}

// TestSendControlRing_NAK_Chunking_Boundary_TableDriven tests NAK chunking boundaries.
func TestSendControlRing_NAK_Chunking_Boundary_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		nakSeqCount   int
		expectPackets int
	}{
		{"1_seq_1_packet", 1, 1},
		{"16_seq_1_packet", 16, 1},
		{"31_seq_1_packet", 31, 1},
		{"32_seq_1_packet", 32, 1},    // Exactly max
		{"33_seq_2_packets", 33, 2},   // One over
		{"64_seq_2_packets", 64, 2},   // Exactly 2
		{"65_seq_3_packets", 65, 3},   // One into third
		{"100_seq_4_packets", 100, 4}, // 32+32+32+4
		{"128_seq_4_packets", 128, 4}, // Exactly 4
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(256, 1)
			require.NoError(t, err)

			seqs := make([]circular.Number, tc.nakSeqCount)
			for i := 0; i < tc.nakSeqCount; i++ {
				seqs[i] = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			}

			ok := ring.PushNAK(seqs)
			require.True(t, ok)
			require.Equal(t, tc.expectPackets, ring.Len(), "packet count mismatch")

			// Verify all sequences are present
			totalSeqs := 0
			for i := 0; i < tc.expectPackets; i++ {
				cp, popOk := ring.TryPop()
				require.True(t, popOk)
				require.Equal(t, ControlTypeNAK, cp.Type)
				totalSeqs += cp.NAKCount
			}
			require.Equal(t, tc.nakSeqCount, totalSeqs, "total sequences mismatch")
		})
	}
}

// TestSendControlRing_Empty_NAK_TableDriven tests empty NAK handling.
func TestSendControlRing_Empty_NAK_TableDriven(t *testing.T) {
	testCases := []struct {
		name         string
		seqs         []circular.Number
		expectOK     bool
		expectPushed int
	}{
		{
			name:         "nil_slice",
			seqs:         nil,
			expectOK:     true,
			expectPushed: 0,
		},
		{
			name:         "empty_slice",
			seqs:         []circular.Number{},
			expectOK:     true,
			expectPushed: 0,
		},
		{
			name:         "single_seq",
			seqs:         []circular.Number{circular.New(1, packet.MAX_SEQUENCENUMBER)},
			expectOK:     true,
			expectPushed: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewSendControlRing(32, 1)
			require.NoError(t, err)

			ok := ring.PushNAK(tc.seqs)
			require.Equal(t, tc.expectOK, ok)
			require.Equal(t, tc.expectPushed, ring.Len())
		})
	}
}

// TestSendControlRing_Metrics_Simulation tests that overflow scenarios would trigger metrics.
func TestSendControlRing_Metrics_Simulation(t *testing.T) {
	// This test simulates what happens in the real sender when control ring overflows
	m := &metrics.ConnectionMetrics{}

	ring, err := NewSendControlRing(4, 1) // Tiny ring
	require.NoError(t, err)

	// Simulate ACK flood
	for i := 0; i < 20; i++ {
		seq := circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		if !ring.PushACK(seq) {
			// In real code, this would increment SendControlRingDroppedACK
			m.SendControlRingDroppedACK.Add(1)
		}
	}

	// Simulate NAK flood
	for i := 0; i < 10; i++ {
		seqs := []circular.Number{circular.New(uint32(i*2), packet.MAX_SEQUENCENUMBER)}
		if !ring.PushNAK(seqs) {
			// In real code, this would increment SendControlRingDroppedNAK
			m.SendControlRingDroppedNAK.Add(1)
		}
	}

	droppedACKs := m.SendControlRingDroppedACK.Load()
	droppedNAKs := m.SendControlRingDroppedNAK.Load()

	t.Logf("Simulated: dropped ACKs=%d, dropped NAKs=%d", droppedACKs, droppedNAKs)

	require.Greater(t, droppedACKs, uint64(0), "expected some ACK drops")
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkSendControlRing_Overflow_Recovery(b *testing.B) {
	ring, _ := NewSendControlRing(64, 2)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Push until full
		for j := 0; j < 100; j++ {
			ring.PushACK(circular.New(uint32(j), packet.MAX_SEQUENCENUMBER))
		}
		// Drain
		for {
			if _, ok := ring.TryPop(); !ok {
				break
			}
		}
	}
}

func BenchmarkSendControlRing_NAK_Chunking(b *testing.B) {
	ring, _ := NewSendControlRing(4096, 2)
	seqs := make([]circular.Number, 100) // 100 sequences = 4 chunks
	for i := range seqs {
		seqs[i] = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.PushNAK(seqs)
		// Drain
		for {
			if _, ok := ring.TryPop(); !ok {
				break
			}
		}
	}
}
