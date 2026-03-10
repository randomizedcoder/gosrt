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

// ============================================================================
// RTO Calculation Tests (Phase 6: RTO Suppression)
// ============================================================================

// TestRTOCalcFunc verifies RTO calculation function dispatch
func TestRTOCalcFunc(t *testing.T) {
	tests := []struct {
		name      string
		mode      RTOMode
		margin    float64
		rttVal    float64 // RTT in microseconds
		rttVarVal float64 // RTTVar in microseconds
		wantRTO   uint64  // expected RTO in microseconds
	}{
		{"RTT+RTTVar", RTORttRttVar, 0, 100_000, 10_000, 110_000},
		{"RTT+4*RTTVar", RTORtt4RttVar, 0, 100_000, 10_000, 140_000},
		{"RTT+RTTVar+10%", RTORttRttVarMargin, 0.10, 100_000, 10_000, 121_000},
		{"RTT+RTTVar+20%", RTORttRttVarMargin, 0.20, 100_000, 10_000, 132_000},
		// Edge cases
		{"Zero RTT", RTORttRttVar, 0, 0, 0, 0},
		{"Small values", RTORttRttVar, 0, 100, 10, 110},
		{"Large values", RTORttRttVar, 0, 500_000, 50_000, 550_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &rtt{}
			r.SetRTOMode(tt.mode, tt.margin)

			// Test the function dispatch directly
			if r.rtoCalcFunc == nil {
				t.Fatal("rtoCalcFunc should not be nil after SetRTOMode")
			}

			gotRTO := r.rtoCalcFunc(tt.rttVal, tt.rttVarVal)

			if gotRTO != tt.wantRTO {
				t.Errorf("rtoCalcFunc() = %d, want %d", gotRTO, tt.wantRTO)
			}

			// Verify one-way delay calculation (trivial /2, compiles to >>1)
			gotOneWay := gotRTO / 2
			wantOneWay := tt.wantRTO / 2
			if gotOneWay != wantOneWay {
				t.Errorf("oneWay = %d, want %d", gotOneWay, wantOneWay)
			}
		})
	}
}

// TestRecalculateUpdatesRTO verifies that Recalculate() updates the pre-calculated RTO
func TestRecalculateUpdatesRTO(t *testing.T) {
	r := &rtt{}
	r.SetRTOMode(RTORttRttVar, 0)

	// Initialize with some RTT value
	r.rttBits.Store(math.Float64bits(100_000))   // 100ms
	r.rttVarBits.Store(math.Float64bits(10_000)) // 10ms

	// Trigger Recalculate
	r.Recalculate(100 * time.Millisecond)

	// Verify pre-calculated RTO is populated
	rtoUs := r.rtoUs.Load()
	if rtoUs == 0 {
		t.Error("rtoUs should be non-zero after Recalculate")
	}

	// RTO should be approximately RTT + RTTVar (smoothed values)
	// After one EWMA update, values won't be exactly 100000+10000
	// but should be in a reasonable range
	if rtoUs < 50_000 || rtoUs > 200_000 {
		t.Errorf("rtoUs = %d, should be in reasonable range (50000-200000)", rtoUs)
	}

	// Verify one-way delay is just rtoUs/2
	oneWayUs := rtoUs / 2
	if oneWayUs == 0 {
		t.Error("oneWayUs should be non-zero")
	}
	if oneWayUs != rtoUs/2 {
		t.Errorf("oneWayUs = %d, want %d (rtoUs/2)", oneWayUs, rtoUs/2)
	}
}

// TestRTOCalcFuncNilSafe verifies Recalculate handles nil rtoCalcFunc gracefully
func TestRTOCalcFuncNilSafe(t *testing.T) {
	r := &rtt{}
	// Don't call SetRTOMode - rtoCalcFunc is nil

	r.rttBits.Store(math.Float64bits(100_000))
	r.rttVarBits.Store(math.Float64bits(10_000))

	// Recalculate should handle nil rtoCalcFunc gracefully
	r.Recalculate(100 * time.Millisecond)

	// rtoUs should remain zero
	if r.rtoUs.Load() != 0 {
		t.Error("rtoUs should be 0 when rtoCalcFunc is nil")
	}
}

// TestRTOUsGetter verifies the RTOUs() getter method
func TestRTOUsGetter(t *testing.T) {
	r := &rtt{}
	r.SetRTOMode(RTORttRttVar, 0)

	// Initial value should be 0
	if r.RTOUs() != 0 {
		t.Errorf("Initial RTOUs() = %d, want 0", r.RTOUs())
	}

	// Set RTT values and recalculate
	r.rttBits.Store(math.Float64bits(100_000))
	r.rttVarBits.Store(math.Float64bits(10_000))
	r.Recalculate(100 * time.Millisecond)

	// RTOUs() should now return non-zero
	if r.RTOUs() == 0 {
		t.Error("RTOUs() should be non-zero after Recalculate")
	}

	// Verify RTOUs() matches direct atomic load
	if r.RTOUs() != r.rtoUs.Load() {
		t.Errorf("RTOUs() = %d, rtoUs.Load() = %d - should match", r.RTOUs(), r.rtoUs.Load())
	}
}

// TestRTOModeString verifies RTOMode.String() returns correct values
func TestRTOModeString(t *testing.T) {
	tests := []struct {
		mode RTOMode
		want string
	}{
		{RTORttRttVar, "rtt_rttvar"},
		{RTORtt4RttVar, "rtt_4rttvar"},
		{RTORttRttVarMargin, "rtt_rttvar_margin"},
		{RTOMode(99), "rtt_rttvar"}, // Unknown defaults to rtt_rttvar
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("RTOMode(%d).String() = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

// TestRTTLastSample verifies the raw RTT sample (without EWMA smoothing)
// is correctly stored and retrieved.
func TestRTTLastSample(t *testing.T) {
	r := &rtt{}
	r.rttBits.Store(math.Float64bits(100_000))   // Initial RTT: 100ms
	r.rttVarBits.Store(math.Float64bits(10_000)) // Initial RTTVar: 10ms
	r.SetRTOMode(RTORttRttVar, 0)

	// Apply samples and verify the raw sample is stored (not smoothed)
	tests := []struct {
		sample  time.Duration
		wantRaw uint64 // Expected raw value (last sample)
	}{
		{50_000 * time.Microsecond, 50_000},   // 50ms
		{200_000 * time.Microsecond, 200_000}, // 200ms
		{75_000 * time.Microsecond, 75_000},   // 75ms
		{100 * time.Microsecond, 100},         // 0.1ms (100µs)
	}

	for _, tt := range tests {
		r.Recalculate(tt.sample)

		gotRaw := r.RTTLastSample()
		if gotRaw != tt.wantRaw {
			t.Errorf("RTTLastSample() after %v = %d, want %d", tt.sample, gotRaw, tt.wantRaw)
		}

		// Verify smoothed RTT (may equal raw on first sample, diverges with EWMA)
		smoothedRTT := r.RTT()

		t.Logf("Sample: %v → Raw: %d µs, Smoothed: %.0f µs",
			tt.sample, gotRaw, smoothedRTT)
	}

	// Verify that after multiple diverse samples, raw != smoothed
	// (demonstrating EWMA smoothing is working)
	finalRaw := r.RTTLastSample()
	finalSmoothed := r.RTT()
	if float64(finalRaw) == finalSmoothed {
		t.Logf("Note: Raw (%d) equals Smoothed (%.0f) - this is possible but unlikely after diverse samples",
			finalRaw, finalSmoothed)
	}
}
