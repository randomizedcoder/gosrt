//go:build go1.18

package receive

import (
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Push Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkRecvControlRing_PushACKACK(b *testing.B) {
	ring, _ := NewRecvControlRing(8192, 1)
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !ring.PushACKACK(uint32(i), now) {
			// Drain if full
			for {
				if _, ok := ring.TryPop(); !ok {
					break
				}
			}
			ring.PushACKACK(uint32(i), now)
		}
	}
}

func BenchmarkRecvControlRing_PushACKACK_NoOverflow(b *testing.B) {
	ring, _ := NewRecvControlRing(1<<20, 1) // Large ring
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.PushACKACK(uint32(i), now)
	}
}

func BenchmarkRecvControlRing_PushKEEPALIVE(b *testing.B) {
	ring, _ := NewRecvControlRing(8192, 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !ring.PushKEEPALIVE() {
			// Drain if full
			for {
				if _, ok := ring.TryPop(); !ok {
					break
				}
			}
			ring.PushKEEPALIVE()
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Pop Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkRecvControlRing_TryPop(b *testing.B) {
	ring, _ := NewRecvControlRing(8192, 1)
	now := time.Now()

	// Pre-fill
	for i := 0; i < 8000; i++ {
		ring.PushACKACK(uint32(i), now)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := ring.TryPop(); !ok {
			// Refill
			for j := 0; j < 1000; j++ {
				ring.PushACKACK(uint32(j), now)
			}
		}
	}
}

func BenchmarkRecvControlRing_TryPop_SingleConsumer(b *testing.B) {
	ring, _ := NewRecvControlRing(65536, 1)
	now := time.Now()

	// Pre-fill
	for i := 0; i < 60000; i++ {
		ring.PushACKACK(uint32(i), now)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := ring.TryPop(); !ok {
			// Refill
			for j := 0; j < 10000; j++ {
				ring.PushACKACK(uint32(j), now)
			}
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Push+Pop Balanced Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkRecvControlRing_PushPop_ACKACK(b *testing.B) {
	ring, _ := NewRecvControlRing(128, 1)
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.PushACKACK(uint32(i), now)
		ring.TryPop()
	}
}

func BenchmarkRecvControlRing_PushPop_KEEPALIVE(b *testing.B) {
	ring, _ := NewRecvControlRing(128, 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.PushKEEPALIVE()
		ring.TryPop()
	}
}

func BenchmarkRecvControlRing_PushPop_Mixed(b *testing.B) {
	ring, _ := NewRecvControlRing(128, 1)
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			ring.PushACKACK(uint32(i), now)
		} else {
			ring.PushKEEPALIVE()
		}
		ring.TryPop()
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkRecvControlRing_PushACKACK_Concurrent(b *testing.B) {
	ring, _ := NewRecvControlRing(1<<20, 1) // Large ring
	now := time.Now()

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			ring.PushACKACK(seq, now)
			seq++
		}
	})
}

func BenchmarkRecvControlRing_PushMixed_Concurrent(b *testing.B) {
	ring, _ := NewRecvControlRing(1<<20, 1)
	now := time.Now()

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			if seq%2 == 0 {
				ring.PushACKACK(seq, now)
			} else {
				ring.PushKEEPALIVE()
			}
			seq++
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Allocation Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkRecvControlRing_Allocs_ACKACK(b *testing.B) {
	ring, _ := NewRecvControlRing(128, 1)
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.PushACKACK(uint32(i), now)
		ring.TryPop()
	}
}

func BenchmarkRecvControlRing_Allocs_KEEPALIVE(b *testing.B) {
	ring, _ := NewRecvControlRing(128, 1)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.PushKEEPALIVE()
		ring.TryPop()
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Realistic Workload Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkRecvControlRing_RealisticACKACK(b *testing.B) {
	// Simulate: ~100 ACKACK/sec (Full ACK every 10ms)
	// Ring size 128 is plenty
	ring, _ := NewRecvControlRing(128, 1)
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Push 1 ACKACK
		ring.PushACKACK(uint32(i), now)

		// Drain all (EventLoop batch)
		for {
			if _, ok := ring.TryPop(); !ok {
				break
			}
		}
	}
}

func BenchmarkRecvControlRing_RealisticBurst(b *testing.B) {
	// Simulate: burst of 10 ACKACKs, then drain
	ring, _ := NewRecvControlRing(128, 1)
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Push burst
		for j := 0; j < 10; j++ {
			ring.PushACKACK(uint32(i*10+j), now)
		}

		// Drain all
		for {
			if _, ok := ring.TryPop(); !ok {
				break
			}
		}
	}
}
