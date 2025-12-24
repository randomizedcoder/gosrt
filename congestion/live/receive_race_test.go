// Package live provides race detection tests for the receiver.
//
// These tests are designed to be run with `go test -race` to detect
// data races in concurrent access patterns. They exercise:
//
//   - Multiple concurrent Push() calls (simulating io_uring CQEs)
//   - Push() concurrent with Tick() (producer + consumer)
//   - Push() concurrent with delivery callbacks
//   - NAK btree concurrent operations
//
// Run with: go test -race -run "TestRace_" ./congestion/live/... -v
//
// See documentation/receiver_stream_tests_design.md Section 11 for details.
package live

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
)

// ============================================================================
// RACE TEST INFRASTRUCTURE
// ============================================================================

// raceTestConfig defines configuration for race tests.
type raceTestConfig struct {
	ReceiverConfig ReceiverConfig
	Producers      int           // Number of concurrent producer goroutines
	PacketsPerProd int           // Packets per producer
	Duration       time.Duration // Test duration (for time-based tests)
	WithTicker     bool          // Run Tick() concurrently
	TickInterval   time.Duration // Interval between Tick() calls
}

// defaultRaceConfig returns a default race test configuration.
func defaultRaceConfig(cfg ReceiverConfig) raceTestConfig {
	return raceTestConfig{
		ReceiverConfig: cfg,
		Producers:      4,
		PacketsPerProd: 1000,
		Duration:       2 * time.Second,
		WithTicker:     false,
		TickInterval:   10 * time.Millisecond,
	}
}

// createRaceReceiver creates a receiver configured for race testing.
func createRaceReceiver(t *testing.T, cfg ReceiverConfig, startSeq uint32) (*receiver, *raceMetrics) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	rm := &raceMetrics{}

	recvConfig := ReceiveConfig{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000, // 10ms
		PeriodicNAKInterval:    20_000, // 20ms
		OnSendACK:              func(seq circular.Number, light bool) { rm.ackCount.Add(1) },
		OnSendNAK:              func(list []circular.Number) { rm.nakCount.Add(1) },
		OnDeliver:              func(p packet.Packet) { rm.deliverCount.Add(1) },
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             120_000, // 120ms
		NakConsolidationBudget: 20_000,  // 20ms
	}

	// Apply receiver config
	if cfg.UseNakBtree {
		recvConfig.PacketReorderAlgorithm = "btree"
		recvConfig.UseNakBtree = true
		recvConfig.NakRecentPercent = cfg.NakRecentPercent
		recvConfig.NakMergeGap = cfg.NakMergeGap
		recvConfig.FastNakEnabled = cfg.FastNakEnabled
		recvConfig.FastNakRecentEnabled = cfg.FastNakRecentEnabled
		if cfg.FastNakEnabled {
			recvConfig.FastNakThresholdUs = 50_000 // 50ms
		}
	}

	recv := NewReceiver(recvConfig)
	return recv.(*receiver), rm
}

// raceMetrics tracks metrics during race tests.
type raceMetrics struct {
	pushCount    atomic.Uint64
	ackCount     atomic.Uint64
	nakCount     atomic.Uint64
	deliverCount atomic.Uint64
	tickCount    atomic.Uint64
}

// createRacePacket creates a packet for race testing.
func createRacePacket(seq uint32, addr net.Addr, baseTimeUs uint64) packet.Packet {
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = baseTimeUs + uint64(seq)*1000 // 1ms per packet
	p.Header().Timestamp = uint32(baseTimeUs + uint64(seq)*1000)
	return p
}

// ============================================================================
// RACE TESTS: CONCURRENT PUSH
// ============================================================================

// TestRace_PushConcurrent tests multiple goroutines calling Push() simultaneously.
// This simulates io_uring completion handlers from multiple CQEs.
func TestRace_PushConcurrent(t *testing.T) {
	configs := AllReceiverConfigs()

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			recv, rm := createRaceReceiver(t, cfg, 1)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			const numProducers = 4
			const packetsPerProducer = 1000

			var wg sync.WaitGroup
			for i := 0; i < numProducers; i++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					startSeq := uint32(producerID * packetsPerProducer)
					for j := 0; j < packetsPerProducer; j++ {
						seq := startSeq + uint32(j)
						p := createRacePacket(seq, addr, baseTimeUs)
						recv.Push(p)
						rm.pushCount.Add(1)
					}
				}(i)
			}
			wg.Wait()

			t.Logf("Pushed %d packets from %d producers", rm.pushCount.Load(), numProducers)
		})
	}
}

// TestRace_PushConcurrent_HighContention tests high contention Push() scenario.
func TestRace_PushConcurrent_HighContention(t *testing.T) {
	configs := NakBtreeConfigs() // Focus on NAK btree configs

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			recv, rm := createRaceReceiver(t, cfg, 1)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			const numProducers = 8
			const packetsPerProducer = 500

			// All producers push overlapping sequence numbers to maximize contention
			var wg sync.WaitGroup
			for i := 0; i < numProducers; i++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					for j := 0; j < packetsPerProducer; j++ {
						// Interleaved sequences: producer 0 gets 0,8,16... producer 1 gets 1,9,17...
						seq := uint32(j*numProducers + producerID)
						p := createRacePacket(seq, addr, baseTimeUs)
						recv.Push(p)
						rm.pushCount.Add(1)
					}
				}(i)
			}
			wg.Wait()

			t.Logf("High contention: %d packets from %d producers", rm.pushCount.Load(), numProducers)
		})
	}
}

// ============================================================================
// RACE TESTS: PUSH + TICK CONCURRENT
// ============================================================================

// TestRace_PushWithTick tests Push() concurrent with Tick().
// This is the primary producer/consumer race scenario.
func TestRace_PushWithTick(t *testing.T) {
	configs := AllReceiverConfigs()

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			recv, rm := createRaceReceiver(t, cfg, 1)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var wg sync.WaitGroup

			// Producer goroutine
			wg.Add(1)
			go func() {
				defer wg.Done()
				seq := uint32(1)
				for {
					select {
					case <-ctx.Done():
						return
					default:
						p := createRacePacket(seq, addr, baseTimeUs)
						recv.Push(p)
						rm.pushCount.Add(1)
						seq++
						if seq > 10000 {
							seq = 1 // Wrap to keep test bounded
						}
					}
				}
			}()

			// Consumer goroutine (Tick)
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				now := baseTimeUs + 200_000 // Start after TSBPD delay
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						recv.Tick(now)
						rm.tickCount.Add(1)
						now += 10_000 // Advance 10ms
					}
				}
			}()

			wg.Wait()
			t.Logf("Push/Tick race: %d pushes, %d ticks, %d deliveries",
				rm.pushCount.Load(), rm.tickCount.Load(), rm.deliverCount.Load())
		})
	}
}

// TestRace_PushWithTick_FastTick tests with very fast Tick() interval.
func TestRace_PushWithTick_FastTick(t *testing.T) {
	configs := NakBtreeConfigs()

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			recv, rm := createRaceReceiver(t, cfg, 1)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			var wg sync.WaitGroup

			// Producer
			wg.Add(1)
			go func() {
				defer wg.Done()
				seq := uint32(1)
				for {
					select {
					case <-ctx.Done():
						return
					default:
						p := createRacePacket(seq, addr, baseTimeUs)
						recv.Push(p)
						rm.pushCount.Add(1)
						seq++
					}
				}
			}()

			// Fast consumer (1ms ticks)
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(1 * time.Millisecond)
				defer ticker.Stop()
				now := baseTimeUs + 200_000
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						recv.Tick(now)
						rm.tickCount.Add(1)
						now += 1_000
					}
				}
			}()

			wg.Wait()
			t.Logf("Fast tick race: %d pushes, %d ticks", rm.pushCount.Load(), rm.tickCount.Load())
		})
	}
}

// ============================================================================
// RACE TESTS: FULL PIPELINE
// ============================================================================

// TestRace_FullPipeline tests all concurrent paths simultaneously.
func TestRace_FullPipeline(t *testing.T) {
	configs := AllReceiverConfigs()

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			recv, rm := createRaceReceiver(t, cfg, 1)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			var wg sync.WaitGroup

			// Multiple producers (simulating io_uring)
			const numProducers = 4
			for i := 0; i < numProducers; i++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					seq := uint32(producerID * 10000)
					for {
						select {
						case <-ctx.Done():
							return
						default:
							p := createRacePacket(seq, addr, baseTimeUs)
							recv.Push(p)
							rm.pushCount.Add(1)
							seq++
						}
					}
				}(i)
			}

			// Tick goroutine
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				now := baseTimeUs + 200_000
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						recv.Tick(now)
						rm.tickCount.Add(1)
						now += 10_000
					}
				}
			}()

			wg.Wait()

			t.Logf("Full pipeline: %d pushes from %d producers, %d ticks, %d ACKs, %d NAKs, %d deliveries",
				rm.pushCount.Load(), numProducers, rm.tickCount.Load(),
				rm.ackCount.Load(), rm.nakCount.Load(), rm.deliverCount.Load())
		})
	}
}

// TestRace_FullPipeline_WithLoss tests full pipeline with simulated packet loss.
func TestRace_FullPipeline_WithLoss(t *testing.T) {
	configs := NakBtreeConfigs()

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			recv, rm := createRaceReceiver(t, cfg, 1)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var wg sync.WaitGroup
			var droppedCount atomic.Uint64

			// Producer with 10% simulated loss
			wg.Add(1)
			go func() {
				defer wg.Done()
				seq := uint32(1)
				for {
					select {
					case <-ctx.Done():
						return
					default:
						// Drop every 10th packet to trigger NAK logic
						if seq%10 != 0 {
							p := createRacePacket(seq, addr, baseTimeUs)
							recv.Push(p)
							rm.pushCount.Add(1)
						} else {
							droppedCount.Add(1)
						}
						seq++
					}
				}
			}()

			// Tick goroutine
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				now := baseTimeUs + 200_000
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						recv.Tick(now)
						rm.tickCount.Add(1)
						now += 10_000
					}
				}
			}()

			wg.Wait()

			t.Logf("Pipeline with loss: %d pushes, %d dropped, %d ticks, %d NAKs",
				rm.pushCount.Load(), droppedCount.Load(), rm.tickCount.Load(), rm.nakCount.Load())
		})
	}
}

// ============================================================================
// RACE TESTS: SPECIFIC OPERATIONS
// ============================================================================

// TestRace_NakBtreeOperations tests NAK btree concurrent insert/delete/scan.
func TestRace_NakBtreeOperations(t *testing.T) {
	configs := NakBtreeConfigs()

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			recv, rm := createRaceReceiver(t, cfg, 1)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var wg sync.WaitGroup

			// Push packets with gaps to trigger NAK btree inserts
			wg.Add(1)
			go func() {
				defer wg.Done()
				seq := uint32(1)
				for {
					select {
					case <-ctx.Done():
						return
					default:
						// Create gaps by skipping sequences
						if seq%5 != 0 {
							p := createRacePacket(seq, addr, baseTimeUs)
							recv.Push(p)
							rm.pushCount.Add(1)
						}
						seq++
					}
				}
			}()

			// Push "retransmitted" packets to trigger NAK btree deletes
			wg.Add(1)
			go func() {
				defer wg.Done()
				seq := uint32(5) // Start with a gap sequence
				for {
					select {
					case <-ctx.Done():
						return
					default:
						p := createRacePacket(seq, addr, baseTimeUs)
						recv.Push(p)
						rm.pushCount.Add(1)
						seq += 5 // Next gap
						time.Sleep(100 * time.Microsecond) // Slower rate
					}
				}
			}()

			// Tick to trigger NAK btree scans
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				now := baseTimeUs + 200_000
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						recv.Tick(now)
						rm.tickCount.Add(1)
						now += 5_000
					}
				}
			}()

			wg.Wait()

			t.Logf("NAK btree operations: %d pushes, %d ticks, %d NAKs",
				rm.pushCount.Load(), rm.tickCount.Load(), rm.nakCount.Load())
		})
	}
}

// TestRace_MetricsUpdates tests concurrent metrics updates.
func TestRace_MetricsUpdates(t *testing.T) {
	configs := AllReceiverConfigs()

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			recv, rm := createRaceReceiver(t, cfg, 1)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			var wg sync.WaitGroup

			// Multiple rapid pushes
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					seq := uint32(id * 1000)
					for {
						select {
						case <-ctx.Done():
							return
						default:
							p := createRacePacket(seq, addr, baseTimeUs)
							recv.Push(p)
							rm.pushCount.Add(1)
							seq++
						}
					}
				}(i)
			}

			// Rapid ticks
			wg.Add(1)
			go func() {
				defer wg.Done()
				now := baseTimeUs + 200_000
				for {
					select {
					case <-ctx.Done():
						return
					default:
						recv.Tick(now)
						rm.tickCount.Add(1)
						now += 100 // Very fast advancement
					}
				}
			}()

			wg.Wait()

			t.Logf("Metrics race: %d pushes, %d ticks", rm.pushCount.Load(), rm.tickCount.Load())
		})
	}
}

// ============================================================================
// RACE TESTS: WRAPAROUND
// ============================================================================

// TestRace_SequenceWraparound tests concurrent operations near sequence wraparound.
func TestRace_SequenceWraparound(t *testing.T) {
	configs := NakBtreeConfigs()

	for _, cfg := range configs {
		t.Run(cfg.Name, func(t *testing.T) {
			// Start near MAX_SEQUENCENUMBER
			startSeq := packet.MAX_SEQUENCENUMBER - 500
			recv, rm := createRaceReceiver(t, cfg, startSeq)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var wg sync.WaitGroup

			// Producer that wraps around
			wg.Add(1)
			go func() {
				defer wg.Done()
				seq := startSeq
				for {
					select {
					case <-ctx.Done():
						return
					default:
						p := createRacePacket(seq, addr, baseTimeUs)
						recv.Push(p)
						rm.pushCount.Add(1)
						seq = circular.SeqAdd(seq, 1)
					}
				}
			}()

			// Tick during wraparound
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				now := baseTimeUs + 200_000
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						recv.Tick(now)
						rm.tickCount.Add(1)
						now += 10_000
					}
				}
			}()

			wg.Wait()

			t.Logf("Wraparound race: %d pushes, %d ticks (started at seq %d)",
				rm.pushCount.Load(), rm.tickCount.Load(), startSeq)
		})
	}
}

