package srt

// ACK/ACKACK Redesign - Phase ACK-1: RTT Lock vs Atomic Benchmark
// Reference: documentation/ack_optimization_implementation.md
//            Section: "### Improvement #7: Atomic RTT Calculation (Benchmark First!)"
//
// This file benchmarks two RTT calculation implementations:
// 1. rttLock - uses sync.RWMutex (current implementation)
// 2. rttAtomic - uses atomic.Uint64 with CAS
//
// Run benchmarks:
//   go test -bench=BenchmarkRTT -benchmem -count=5
//   go test -bench=BenchmarkRTT -benchmem -cpu=1,2,4,8

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// Option A: Lock-based RTT (current implementation style)
// ============================================================================

type rttLock struct {
	rtt    float64
	rttVar float64
	lock   sync.RWMutex
}

func (r *rttLock) RecalculateRTTLock(newRTT time.Duration) {
	lastRTT := float64(newRTT.Microseconds())

	r.lock.Lock()
	defer r.lock.Unlock()

	// RFC 4.10: EWMA smoothing
	r.rtt = r.rtt*0.875 + lastRTT*0.125
	r.rttVar = r.rttVar*0.75 + math.Abs(r.rtt-lastRTT)*0.25
}

func (r *rttLock) RTT() float64 {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.rtt
}

func (r *rttLock) RTTVar() float64 {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.rttVar
}

func (r *rttLock) NAKInterval() float64 {
	r.lock.RLock()
	defer r.lock.RUnlock()
	nakInterval := (r.rtt + 4*r.rttVar) * 0.5 // multiply instead of divide (faster)
	if nakInterval < 20000 {
		nakInterval = 20000 // Minimum 20ms
	}
	return nakInterval
}

// ============================================================================
// Option B: Atomic-based RTT (proposed lock-free implementation)
// ============================================================================

type rttAtomic struct {
	rttBits    atomic.Uint64 // float64 stored as bits
	rttVarBits atomic.Uint64 // float64 stored as bits
}

func (r *rttAtomic) RecalculateRTTAtomic(newRTT time.Duration) {
	lastRTT := float64(newRTT.Microseconds())

	for {
		oldRTTBits := r.rttBits.Load()
		oldRTT := math.Float64frombits(oldRTTBits)
		oldRTTVar := math.Float64frombits(r.rttVarBits.Load())

		// RFC 4.10: EWMA smoothing
		newRTTVal := oldRTT*0.875 + lastRTT*0.125
		newRTTVarVal := oldRTTVar*0.75 + math.Abs(newRTTVal-lastRTT)*0.25

		// CAS the RTT value
		if r.rttBits.CompareAndSwap(oldRTTBits, math.Float64bits(newRTTVal)) {
			// RTT updated, now update RTTVar (slight race window acceptable for EWMA)
			r.rttVarBits.Store(math.Float64bits(newRTTVarVal))
			break
		}
		// CAS failed - another goroutine updated RTT, retry with new value
	}
}

func (r *rttAtomic) RTT() float64 {
	return math.Float64frombits(r.rttBits.Load())
}

func (r *rttAtomic) RTTVar() float64 {
	return math.Float64frombits(r.rttVarBits.Load())
}

func (r *rttAtomic) NAKInterval() float64 {
	rtt := math.Float64frombits(r.rttBits.Load())
	rttVar := math.Float64frombits(r.rttVarBits.Load())
	nakInterval := (rtt + 4*rttVar) * 0.5 // multiply instead of divide (faster)
	if nakInterval < 20000 {
		nakInterval = 20000 // Minimum 20ms
	}
	return nakInterval
}

// ============================================================================
// Benchmarks - Single-threaded (no contention)
// ============================================================================

func BenchmarkRTT_Lock_Recalculate(b *testing.B) {
	r := &rttLock{rtt: 1000, rttVar: 100}
	duration := 950 * time.Microsecond
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.RecalculateRTTLock(duration)
	}
}

func BenchmarkRTT_Atomic_Recalculate(b *testing.B) {
	r := &rttAtomic{}
	r.rttBits.Store(math.Float64bits(1000))
	r.rttVarBits.Store(math.Float64bits(100))
	duration := 950 * time.Microsecond
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.RecalculateRTTAtomic(duration)
	}
}

func BenchmarkRTT_Lock_Read(b *testing.B) {
	r := &rttLock{rtt: 1000, rttVar: 100}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.RTT()
		_ = r.RTTVar()
	}
}

func BenchmarkRTT_Atomic_Read(b *testing.B) {
	r := &rttAtomic{}
	r.rttBits.Store(math.Float64bits(1000))
	r.rttVarBits.Store(math.Float64bits(100))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.RTT()
		_ = r.RTTVar()
	}
}

func BenchmarkRTT_Lock_NAKInterval(b *testing.B) {
	r := &rttLock{rtt: 1000, rttVar: 100}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.NAKInterval()
	}
}

func BenchmarkRTT_Atomic_NAKInterval(b *testing.B) {
	r := &rttAtomic{}
	r.rttBits.Store(math.Float64bits(1000))
	r.rttVarBits.Store(math.Float64bits(100))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.NAKInterval()
	}
}

// ============================================================================
// Benchmarks - Contention (multiple goroutines)
// ============================================================================

func BenchmarkRTT_Lock_Recalculate_Contention(b *testing.B) {
	r := &rttLock{rtt: 1000, rttVar: 100}
	duration := 950 * time.Microsecond
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.RecalculateRTTLock(duration)
		}
	})
}

func BenchmarkRTT_Atomic_Recalculate_Contention(b *testing.B) {
	r := &rttAtomic{}
	r.rttBits.Store(math.Float64bits(1000))
	r.rttVarBits.Store(math.Float64bits(100))
	duration := 950 * time.Microsecond
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.RecalculateRTTAtomic(duration)
		}
	})
}

func BenchmarkRTT_Lock_Read_Contention(b *testing.B) {
	r := &rttLock{rtt: 1000, rttVar: 100}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = r.RTT()
			_ = r.RTTVar()
		}
	})
}

func BenchmarkRTT_Atomic_Read_Contention(b *testing.B) {
	r := &rttAtomic{}
	r.rttBits.Store(math.Float64bits(1000))
	r.rttVarBits.Store(math.Float64bits(100))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = r.RTT()
			_ = r.RTTVar()
		}
	})
}

// ============================================================================
// Benchmarks - Mixed read/write (realistic scenario)
// ============================================================================

// BenchmarkRTT_Lock_Mixed simulates realistic usage:
// - 1 writer (handleACKACK updating RTT every ~10ms = 100/sec)
// - Multiple readers (sendACK reading RTT for Full ACK)
func BenchmarkRTT_Lock_Mixed(b *testing.B) {
	r := &rttLock{rtt: 1000, rttVar: 100}
	duration := 950 * time.Microsecond

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each goroutine does 10 reads per 1 write (simulating real ratio)
		i := 0
		for pb.Next() {
			if i%10 == 0 {
				r.RecalculateRTTLock(duration)
			} else {
				_ = r.RTT()
				_ = r.RTTVar()
			}
			i++
		}
	})
}

func BenchmarkRTT_Atomic_Mixed(b *testing.B) {
	r := &rttAtomic{}
	r.rttBits.Store(math.Float64bits(1000))
	r.rttVarBits.Store(math.Float64bits(100))
	duration := 950 * time.Microsecond

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each goroutine does 10 reads per 1 write (simulating real ratio)
		i := 0
		for pb.Next() {
			if i%10 == 0 {
				r.RecalculateRTTAtomic(duration)
			} else {
				_ = r.RTT()
				_ = r.RTTVar()
			}
			i++
		}
	})
}

// ============================================================================
// Correctness tests
// ============================================================================

func TestRTT_Lock_Correctness(t *testing.T) {
	r := &rttLock{rtt: 1000, rttVar: 100}

	// Apply a few RTT samples
	r.RecalculateRTTLock(900 * time.Microsecond)
	r.RecalculateRTTLock(1100 * time.Microsecond)
	r.RecalculateRTTLock(950 * time.Microsecond)

	// RTT should be smoothed around 1000
	rtt := r.RTT()
	if rtt < 900 || rtt > 1100 {
		t.Errorf("RTT out of expected range: %f", rtt)
	}

	// NAKInterval should be > 20000 (minimum)
	nak := r.NAKInterval()
	if nak < 20000 {
		t.Errorf("NAKInterval below minimum: %f", nak)
	}
}

func TestRTT_Atomic_Correctness(t *testing.T) {
	r := &rttAtomic{}
	r.rttBits.Store(math.Float64bits(1000))
	r.rttVarBits.Store(math.Float64bits(100))

	// Apply a few RTT samples
	r.RecalculateRTTAtomic(900 * time.Microsecond)
	r.RecalculateRTTAtomic(1100 * time.Microsecond)
	r.RecalculateRTTAtomic(950 * time.Microsecond)

	// RTT should be smoothed around 1000
	rtt := r.RTT()
	if rtt < 900 || rtt > 1100 {
		t.Errorf("RTT out of expected range: %f", rtt)
	}

	// NAKInterval should be > 20000 (minimum)
	nak := r.NAKInterval()
	if nak < 20000 {
		t.Errorf("NAKInterval below minimum: %f", nak)
	}
}

// TestRTT_Both_Equivalent verifies both implementations produce same results
func TestRTT_Both_Equivalent(t *testing.T) {
	rLock := &rttLock{rtt: 1000, rttVar: 100}
	rAtomic := &rttAtomic{}
	rAtomic.rttBits.Store(math.Float64bits(1000))
	rAtomic.rttVarBits.Store(math.Float64bits(100))

	samples := []time.Duration{
		900 * time.Microsecond,
		1100 * time.Microsecond,
		950 * time.Microsecond,
		1050 * time.Microsecond,
		980 * time.Microsecond,
	}

	for _, sample := range samples {
		rLock.RecalculateRTTLock(sample)
		rAtomic.RecalculateRTTAtomic(sample)
	}

	// Both should produce same results (within floating point tolerance)
	tolerance := 0.001
	if math.Abs(rLock.RTT()-rAtomic.RTT()) > tolerance {
		t.Errorf("RTT mismatch: lock=%f atomic=%f", rLock.RTT(), rAtomic.RTT())
	}
	if math.Abs(rLock.RTTVar()-rAtomic.RTTVar()) > tolerance {
		t.Errorf("RTTVar mismatch: lock=%f atomic=%f", rLock.RTTVar(), rAtomic.RTTVar())
	}
	if math.Abs(rLock.NAKInterval()-rAtomic.NAKInterval()) > tolerance {
		t.Errorf("NAKInterval mismatch: lock=%f atomic=%f", rLock.NAKInterval(), rAtomic.NAKInterval())
	}
}
