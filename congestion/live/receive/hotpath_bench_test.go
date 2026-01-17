package receive

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	ring "github.com/randomizedcoder/go-lock-free-ring"
	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ============================================================================
// HOT PATH BENCHMARKS - Performance-critical receive operations
//
// Run with: go test -bench "BenchmarkHot" ./congestion/live/receive/... -benchmem -benchtime=3s
//
// Focus areas:
// - EventLoop iteration overhead
// - Ring operations (push to ring, drain from ring)
// - Contiguous scan performance
// - Packet delivery
// - Individual micro-operations
// ============================================================================

// ============================================================================
// BENCHMARK HELPERS
// ============================================================================

// createHotPathReceiver creates a receiver optimized for hot path testing
// Uses realistic 4096 ring size - benchmarks should use drain goroutine
func createHotPathReceiver(b *testing.B, withRing bool, withEventLoop bool) *receiver {
	return createHotPathReceiverWithSize(b, withRing, withEventLoop, 4096)
}

// createHotPathReceiverWithSize creates a receiver with configurable ring size
func createHotPathReceiverWithSize(b *testing.B, withRing bool, withEventLoop bool, ringSize int) *receiver {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := Config{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000, // 10ms
		PeriodicNAKInterval:    20_000, // 20ms
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             120_000, // 120ms
		NakConsolidationBudget: 20_000,  // 20ms
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		FastNakEnabled:         true,
		FastNakRecentEnabled:   true,
		UsePacketRing:          withRing,
		UseEventLoop:           withEventLoop,
	}

	if withRing {
		recvConfig.PacketRingSize = ringSize
		recvConfig.PacketRingShards = 4
		// Minimal backoff - consumer should keep ring drained
		recvConfig.PacketRingMaxRetries = 3
		recvConfig.PacketRingBackoffDuration = 10 * time.Microsecond
	}

	recv := New(recvConfig)
	return recv.(*receiver)
}

// startDrainGoroutine starts a background goroutine that continuously drains the ring
// Returns a stop function to call when benchmark is done
func startDrainGoroutine(recv *receiver, ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				recv.drainPacketRing(0)
				// Small sleep to prevent busy-loop when ring is empty
				time.Sleep(10 * time.Microsecond)
			}
		}
	}()
}

// generateHotPathPackets generates packets with proper TSBPD times
func generateHotPathPackets(count int, startSeq uint32, nowUs uint64, tsbpdDelayUs uint64) []packet.Packet {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	packets := make([]packet.Packet, count)

	for i := 0; i < count; i++ {
		seq := circular.SeqAdd(startSeq, uint32(i))
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = nowUs + uint64(i*100) + tsbpdDelayUs // Staggered delivery
		packets[i] = p
	}
	return packets
}

// ============================================================================
// PUSH PATH BENCHMARKS
// ============================================================================

// BenchmarkHotPath_PushToRing measures ring push throughput with concurrent drain
// This is realistic: io_uring pushes while EventLoop drains
func BenchmarkHotPath_PushToRing(b *testing.B) {
	recv := createHotPathReceiver(b, true, false)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start background drain goroutine (simulates EventLoop)
	ctx, cancel := context.WithCancel(context.Background())
	startDrainGoroutine(recv, ctx)
	defer cancel()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = uint64(i*100) + 120_000
		recv.Push(p)
	}
}

// BenchmarkHotPath_PushWithLock measures locked push throughput
func BenchmarkHotPath_PushWithLock(b *testing.B) {
	recv := createHotPathReceiver(b, false, false)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = uint64(i*100) + 120_000
		recv.Push(p)
	}
}

// BenchmarkHotPath_PushParallel measures parallel push throughput with drain
func BenchmarkHotPath_PushParallel(b *testing.B) {
	recv := createHotPathReceiver(b, true, false)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start background drain goroutine
	ctx, cancel := context.WithCancel(context.Background())
	startDrainGoroutine(recv, ctx)
	defer cancel()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		localSeq := uint32(0)
		for pb.Next() {
			p := packet.NewPacket(addr)
			h := p.Header()
			// Use local counter to avoid contention
			h.PacketSequenceNumber = circular.New(localSeq, packet.MAX_SEQUENCENUMBER)
			h.PktTsbpdTime = uint64(localSeq*100) + 120_000
			recv.Push(p)
			localSeq++
		}
	})
}

// ============================================================================
// RING DRAIN BENCHMARKS
// ============================================================================

// BenchmarkHotPath_DrainPacketRing measures ring drain throughput
func BenchmarkHotPath_DrainPacketRing(b *testing.B) {
	for _, ringSize := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("Size%d", ringSize), func(b *testing.B) {
			recv := createHotPathReceiver(b, true, false)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

			// Pre-fill ring
			writeConfig := ring.WriteConfig{MaxRetries: 10, BackoffDuration: 100 * time.Microsecond}
			for i := 0; i < ringSize; i++ {
				p := packet.NewPacket(addr)
				h := p.Header()
				h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
				h.PktTsbpdTime = uint64(i*100) + 120_000
				recv.packetRing.WriteWithBackoff(uint64(i), p, writeConfig)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recv.drainPacketRing(0)
				b.StopTimer()
				// Refill ring for next iteration
				recv.contiguousPoint.Store(packet.MAX_SEQUENCENUMBER) // Reset
				recv.packetStore.Clear()
				for j := 0; j < ringSize; j++ {
					p := packet.NewPacket(addr)
					h := p.Header()
					h.PacketSequenceNumber = circular.New(uint32(i*ringSize+j), packet.MAX_SEQUENCENUMBER)
					h.PktTsbpdTime = uint64(j*100) + 120_000
					recv.packetRing.WriteWithBackoff(uint64(j), p, writeConfig)
				}
				b.StartTimer()
			}
		})
	}
}

// BenchmarkHotPath_DrainRingByDelta measures delta-based drain
func BenchmarkHotPath_DrainRingByDelta(b *testing.B) {
	recv := createHotPathReceiver(b, true, false)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-fill ring
	writeConfig := ring.WriteConfig{MaxRetries: 10, BackoffDuration: 100 * time.Microsecond}
	for i := 0; i < 1000; i++ {
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = uint64(i*100) + 120_000
		recv.packetRing.WriteWithBackoff(uint64(i), p, writeConfig)
		recv.metrics.RecvRatePackets.Add(1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recv.drainRingByDelta()
	}
}

// ============================================================================
// CONTIGUOUS SCAN BENCHMARKS
// ============================================================================

// BenchmarkHotPath_ContiguousScan measures scan with different packet counts
func BenchmarkHotPath_ContiguousScan(b *testing.B) {
	for _, packetCount := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("Packets%d", packetCount), func(b *testing.B) {
			recv := createHotPathReceiver(b, false, false)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

			// Add contiguous packets
			for i := 0; i < packetCount; i++ {
				p := packet.NewPacket(addr)
				h := p.Header()
				h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
				h.PktTsbpdTime = 1_000_000 // Far future
				recv.packetStore.Insert(p)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recv.contiguousScan()
			}
		})
	}
}

// BenchmarkHotPath_ContiguousScanWithGaps measures scan with gaps
func BenchmarkHotPath_ContiguousScanWithGaps(b *testing.B) {
	for _, gapPercent := range []int{1, 5, 10} {
		b.Run(fmt.Sprintf("Gap%dpct", gapPercent), func(b *testing.B) {
			recv := createHotPathReceiver(b, false, false)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

			packetCount := 1000
			for i := 0; i < packetCount; i++ {
				// Skip some packets to create gaps
				if (i%(100/gapPercent)) == 0 && i > 0 {
					continue
				}
				p := packet.NewPacket(addr)
				h := p.Header()
				h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
				h.PktTsbpdTime = 1_000_000
				recv.packetStore.Insert(p)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recv.contiguousScan()
			}
		})
	}
}

// ============================================================================
// DELIVERY BENCHMARKS
// ============================================================================

// BenchmarkHotPath_DeliverReadyPackets measures packet delivery throughput
func BenchmarkHotPath_DeliverReadyPackets(b *testing.B) {
	for _, packetCount := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("Ready%d", packetCount), func(b *testing.B) {
			recv := createHotPathReceiver(b, false, false)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

			// Set now to allow delivery
			nowUs := uint64(200_000)
			recv.nowFn = func() uint64 { return nowUs }

			// Add packets that are ready for delivery (TSBPD expired)
			for i := 0; i < packetCount; i++ {
				p := packet.NewPacket(addr)
				h := p.Header()
				h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
				h.PktTsbpdTime = 100_000 // Past (nowUs=200_000)
				recv.packetStore.Insert(p)
			}

			// Advance contiguous point and ACK to enable delivery
			recv.contiguousPoint.Store(uint32(packetCount - 1))
			recv.lastACKSequenceNumber = circular.New(uint32(packetCount-1), packet.MAX_SEQUENCENUMBER)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recv.deliverReadyPacketsWithTime(nowUs)
				b.StopTimer()
				// Refill for next iteration
				for j := 0; j < packetCount; j++ {
					p := packet.NewPacket(addr)
					h := p.Header()
					seq := uint32(i*packetCount + j)
					h.PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
					h.PktTsbpdTime = 100_000
					recv.packetStore.Insert(p)
				}
				recv.contiguousPoint.Store(uint32((i+1)*packetCount - 1))
				recv.lastACKSequenceNumber = circular.New(uint32((i+1)*packetCount-1), packet.MAX_SEQUENCENUMBER)
				b.StartTimer()
			}
		})
	}
}

// ============================================================================
// EVENTLOOP SIMULATION BENCHMARKS
// ============================================================================

// BenchmarkHotPath_EventLoopIteration measures single iteration overhead
func BenchmarkHotPath_EventLoopIteration(b *testing.B) {
	recv := createHotPathReceiver(b, true, true)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	nowUs := uint64(200_000)
	recv.nowFn = func() uint64 { return nowUs }

	// Pre-fill with some packets
	writeConfig := ring.WriteConfig{MaxRetries: 10, BackoffDuration: 100 * time.Microsecond}
	for i := 0; i < 100; i++ {
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = 100_000
		recv.packetRing.WriteWithBackoff(uint64(i), p, writeConfig)
		recv.metrics.RecvRatePackets.Add(1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate one EventLoop iteration without tickers
		recv.drainRingByDelta()
		recv.contiguousScan()
		recv.deliverReadyPackets()
	}
}

// BenchmarkHotPath_EventLoopFull measures full EventLoop throughput
func BenchmarkHotPath_EventLoopFull(b *testing.B) {
	b.Skip("Full EventLoop benchmark is slow - enable manually")

	recv := createHotPathReceiver(b, true, true)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start EventLoop in background
	var wg sync.WaitGroup
	wg.Add(1)
	recv.EventLoop(ctx, &wg)

	// Let it warm up
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = uint64(i*100) + 120_000
		recv.Push(p)
	}
	b.StopTimer()

	cancel()
	wg.Wait()
}

// ============================================================================
// MICRO-BENCHMARKS - Individual hot operations
// ============================================================================

// BenchmarkHotPath_PacketStoreInsert measures btree insert performance
func BenchmarkHotPath_PacketStoreInsert(b *testing.B) {
	recv := createHotPathReceiver(b, false, false)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = 1_000_000
		recv.packetStore.Insert(p)
	}
}

// BenchmarkHotPath_PacketStoreInsertComparison compares Insert (optimized) vs InsertDuplicateCheck (legacy)
// InsertDuplicateCheck: Has() + ReplaceOrInsert() = 2 btree traversals
// Insert: ReplaceOrInsert() only = 1 traversal for unique packets (20% faster)
func BenchmarkHotPath_PacketStoreInsertComparison(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Unique packets (common case) - Insert should be ~20% faster
	b.Run("Unique/DuplicateCheck", func(b *testing.B) {
		store := NewBTreePacketStore(32).(*btreePacketStore)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			store.InsertDuplicateCheck(p)
		}
	})

	b.Run("Unique/Insert", func(b *testing.B) {
		store := NewBTreePacketStore(32).(*btreePacketStore)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			store.Insert(p)
		}
	})

	// Duplicate packets (rare case) - both should be similar
	b.Run("Duplicate/DuplicateCheck", func(b *testing.B) {
		store := NewBTreePacketStore(32).(*btreePacketStore)
		// Pre-insert packets 0-999
		for i := 0; i < 1000; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			store.InsertDuplicateCheck(p)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			h.PacketSequenceNumber = circular.New(uint32(i%1000), packet.MAX_SEQUENCENUMBER)
			store.InsertDuplicateCheck(p)
		}
	})

	b.Run("Duplicate/Insert", func(b *testing.B) {
		store := NewBTreePacketStore(32).(*btreePacketStore)
		// Pre-insert packets 0-999
		for i := 0; i < 1000; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			store.InsertDuplicateCheck(p)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			h.PacketSequenceNumber = circular.New(uint32(i%1000), packet.MAX_SEQUENCENUMBER)
			_, dupPkt := store.Insert(p)
			if dupPkt != nil {
				// Would call releasePacketFully(dupPkt) in production
				_ = dupPkt
			}
		}
	})

	// Mixed workload (realistic: 1% duplicates)
	b.Run("Mixed1pct/DuplicateCheck", func(b *testing.B) {
		store := NewBTreePacketStore(32).(*btreePacketStore)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			// 1% duplicates: every 100th packet repeats seq 0
			seq := uint32(i)
			if i%100 == 0 && i > 0 {
				seq = 0
			}
			h.PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
			store.InsertDuplicateCheck(p)
		}
	})

	b.Run("Mixed1pct/Insert", func(b *testing.B) {
		store := NewBTreePacketStore(32).(*btreePacketStore)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			seq := uint32(i)
			if i%100 == 0 && i > 0 {
				seq = 0
			}
			h.PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
			_, dupPkt := store.Insert(p)
			if dupPkt != nil {
				_ = dupPkt
			}
		}
	})
}

// BenchmarkHotPath_PacketStoreHas measures btree lookup performance
func BenchmarkHotPath_PacketStoreHas(b *testing.B) {
	recv := createHotPathReceiver(b, false, false)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-populate
	for i := 0; i < 10000; i++ {
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = 1_000_000
		recv.packetStore.Insert(p)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq := circular.New(uint32(i%10000), packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Has(seq)
	}
}

// BenchmarkHotPath_CircularSeqArithmetic measures sequence number operations
func BenchmarkHotPath_CircularSeqArithmetic(b *testing.B) {
	seq1 := uint32(packet.MAX_SEQUENCENUMBER - 100)
	seq2 := uint32(200)

	b.Run("SeqAdd", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = circular.SeqAdd(seq1, uint32(i))
		}
	})

	b.Run("SeqSub", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = circular.SeqSub(seq2, seq1)
		}
	})

	b.Run("SeqLess", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = circular.SeqLess(seq1, seq2)
		}
	})

	b.Run("SeqDiff", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = circular.SeqDiff(seq2, seq1)
		}
	})
}

// BenchmarkHotPath_NakBtreeOperations measures NAK btree performance
func BenchmarkHotPath_NakBtreeOperations(b *testing.B) {
	recv := createHotPathReceiver(b, false, false)

	// Test both lock-free (event loop) and locking (tick) versions

	b.Run("Insert_LockFree", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			recv.nakBtree.Insert(uint32(i))
		}
	})

	b.Run("Insert_Locking", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			recv.nakBtree.InsertLocking(uint32(i))
		}
	})

	// Pre-populate for lookup/delete tests
	for i := 0; i < 10000; i++ {
		recv.nakBtree.InsertLocking(uint32(i))
	}

	b.Run("Has_LockFree", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			recv.nakBtree.Has(uint32(i % 10000))
		}
	})

	b.Run("Has_Locking", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			recv.nakBtree.HasLocking(uint32(i % 10000))
		}
	})

	b.Run("Delete_LockFree", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			recv.nakBtree.Delete(uint32(i % 10000))
			// Re-insert for next iteration
			recv.nakBtree.Insert(uint32(i % 10000))
		}
	})

	b.Run("Delete_Locking", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			recv.nakBtree.DeleteLocking(uint32(i % 10000))
			// Re-insert for next iteration
			recv.nakBtree.InsertLocking(uint32(i % 10000))
		}
	})
}

// BenchmarkHotPath_GapScan measures gap scanning performance
func BenchmarkHotPath_GapScan(b *testing.B) {
	recv := createHotPathReceiver(b, false, false)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets with gaps
	for i := 0; i < 1000; i++ {
		if i%10 == 0 {
			continue // Create gaps
		}
		p := packet.NewPacket(addr)
		h := p.Header()
		h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		h.PktTsbpdTime = 1_000_000
		recv.packetStore.Insert(p)
	}

	// Set up scan window
	recv.nowFn = func() uint64 { return 500_000 }

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recv.gapScan() // Returns (gaps, tsbpds), ignore return values for benchmark
	}
}

// ============================================================================
// COMPARISON BENCHMARKS - Ring vs Lock-based
// ============================================================================

// BenchmarkHotPath_PushComparison compares ring vs lock-based push
func BenchmarkHotPath_PushComparison(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	b.Run("Ring", func(b *testing.B) {
		recv := createHotPathReceiver(b, true, false)
		ctx, cancel := context.WithCancel(context.Background())
		startDrainGoroutine(recv, ctx)
		defer cancel()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			h.PktTsbpdTime = uint64(i*100) + 120_000
			recv.Push(p)
		}
	})

	b.Run("Lock", func(b *testing.B) {
		recv := createHotPathReceiver(b, false, false)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := packet.NewPacket(addr)
			h := p.Header()
			h.PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			h.PktTsbpdTime = uint64(i*100) + 120_000
			recv.Push(p)
		}
	})
}

// BenchmarkHotPath_PushParallelComparison compares parallel push throughput
func BenchmarkHotPath_PushParallelComparison(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for _, goroutines := range []int{1, 2, 4, 8, 16} {
		b.Run(fmt.Sprintf("Ring-G%d", goroutines), func(b *testing.B) {
			recv := createHotPathReceiver(b, true, false)
			ctx, cancel := context.WithCancel(context.Background())
			startDrainGoroutine(recv, ctx)
			defer cancel()

			b.SetParallelism(goroutines)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				localSeq := uint32(0)
				for pb.Next() {
					p := packet.NewPacket(addr)
					h := p.Header()
					h.PacketSequenceNumber = circular.New(localSeq, packet.MAX_SEQUENCENUMBER)
					h.PktTsbpdTime = uint64(localSeq*100) + 120_000
					recv.Push(p)
					localSeq++
				}
			})
		})

		b.Run(fmt.Sprintf("Lock-G%d", goroutines), func(b *testing.B) {
			recv := createHotPathReceiver(b, false, false)
			b.SetParallelism(goroutines)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				localSeq := uint32(0)
				for pb.Next() {
					p := packet.NewPacket(addr)
					h := p.Header()
					h.PacketSequenceNumber = circular.New(localSeq, packet.MAX_SEQUENCENUMBER)
					h.PktTsbpdTime = uint64(localSeq*100) + 120_000
					recv.Push(p)
					localSeq++
				}
			})
		})
	}
}
