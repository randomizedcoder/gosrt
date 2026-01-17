//go:build go1.18

package common

import (
	"fmt"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Push Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRing_Push(b *testing.B) {
	sizes := []int{128, 1024, 8192}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("Size%d", size), func(b *testing.B) {
			ring, _ := NewControlRing[TestPacket](size, 1)
			pkt := TestPacket{Type: 1, Seq: 100, Data: 12345}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !ring.Push(0, pkt) {
					// Drain if full
					for {
						if _, ok := ring.TryPop(); !ok {
							break
						}
					}
					ring.Push(0, pkt)
				}
			}
		})
	}
}

func BenchmarkControlRing_Push_NoOverflow(b *testing.B) {
	// Large ring to avoid overflow during benchmark
	ring, _ := NewControlRing[TestPacket](1<<20, 1) // 1M entries
	pkt := TestPacket{Type: 1, Seq: 100, Data: 12345}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.Push(0, pkt)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TryPop Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRing_TryPop(b *testing.B) {
	ring, _ := NewControlRing[TestPacket](8192, 1)
	pkt := TestPacket{Type: 1, Seq: 100}

	// Pre-fill
	for i := 0; i < 8000; i++ {
		ring.Push(0, pkt)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := ring.TryPop(); !ok {
			// Refill
			for j := 0; j < 1000; j++ {
				ring.Push(0, pkt)
			}
		}
	}
}

func BenchmarkControlRing_TryPop_Empty(b *testing.B) {
	ring, _ := NewControlRing[TestPacket](128, 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.TryPop() // Always returns false
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Push+Pop Balanced Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRing_PushPop_Balanced(b *testing.B) {
	ring, _ := NewControlRing[TestPacket](128, 1)
	pkt := TestPacket{Type: 1, Seq: 100}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.Push(0, pkt)
		ring.TryPop()
	}
}

func BenchmarkControlRing_PushPop_Batch(b *testing.B) {
	batchSizes := []int{1, 10, 50, 100}
	for _, batch := range batchSizes {
		b.Run(fmt.Sprintf("Batch%d", batch), func(b *testing.B) {
			ring, _ := NewControlRing[TestPacket](8192, 1)
			pkt := TestPacket{Type: 1, Seq: 100}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Push batch
				for j := 0; j < batch; j++ {
					ring.Push(0, pkt)
				}
				// Pop batch
				for j := 0; j < batch; j++ {
					ring.TryPop()
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRing_Push_Concurrent(b *testing.B) {
	ring, _ := NewControlRing[TestPacket](1<<20, 1) // Large ring

	b.RunParallel(func(pb *testing.PB) {
		pkt := TestPacket{Type: 1, Seq: 0}
		for pb.Next() {
			ring.Push(0, pkt)
			pkt.Seq++
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Multi-Shard Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRing_Push_MultiShard(b *testing.B) {
	shardCounts := []int{1, 2, 4}
	for _, shards := range shardCounts {
		b.Run(fmt.Sprintf("Shards%d", shards), func(b *testing.B) {
			ring, _ := NewControlRing[TestPacket](1024, shards)
			pkt := TestPacket{Type: 1, Seq: 100}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				shardID := uint64(i % shards)
				if !ring.Push(shardID, pkt) {
					// Drain
					for {
						if _, ok := ring.TryPop(); !ok {
							break
						}
					}
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Allocation Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRing_Allocs(b *testing.B) {
	ring, _ := NewControlRing[TestPacket](128, 1)
	pkt := TestPacket{Type: 1, Seq: 100}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.Push(0, pkt)
		ring.TryPop()
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Len Benchmark
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRing_Len(b *testing.B) {
	ring, _ := NewControlRing[TestPacket](1024, 1)
	// Add some items
	for i := 0; i < 500; i++ {
		ring.Push(0, TestPacket{Seq: uint32(i)})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ring.Len()
	}
}
