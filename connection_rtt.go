package srt

import (
	"math"
	"sync/atomic"
	"time"
)

// rtt implements lock-free RTT tracking using atomic operations.
// ACK-10: Replaced lock-based implementation with atomics for 8x better performance.
// See rtt_benchmark_test.go for benchmarks showing:
//   - Reads: 50-800x faster
//   - Mixed workload: 8x faster
type rtt struct {
	rttBits          atomic.Uint64 // float64 stored as bits (EWMA smoothed)
	rttVarBits       atomic.Uint64 // float64 stored as bits (EWMA smoothed)
	minNakIntervalUs atomic.Uint64 // minimum NAK interval in microseconds (from config)

	// Raw RTT: last sample WITHOUT EWMA smoothing (for diagnostics)
	// This shows the actual measured RTT from the most recent ACKACK.
	// Useful for verifying arrival time capture is working correctly.
	rttLastSampleUs atomic.Uint64 // Last RTT sample in microseconds (no smoothing)

	// Pre-calculated RTO for suppression (updated when RTT changes, read on every NAK/retransmit)
	// Callers: r.rtoUs.Load() for full RTO, r.rtoUs.Load()/2 for one-way delay
	rtoUs atomic.Uint64 // Pre-calculated RTO in microseconds

	// RTO calculation function (set once at connection setup via SetRTOMode)
	rtoCalcFunc func(rttVal, rttVarVal float64) uint64
}

// SetRTOMode configures the RTO calculation function at connection setup.
// Uses function dispatch to eliminate switch overhead on every RTT update.
// Called once during connection initialization.
func (r *rtt) SetRTOMode(mode RTOMode, extraMargin float64) {
	switch mode {
	case RTORtt4RttVar:
		// RFC 6298 conservative: RTT + 4*RTTVar
		r.rtoCalcFunc = func(rttVal, rttVarVal float64) uint64 {
			return uint64(rttVal + 4.0*rttVarVal)
		}
	case RTORttRttVarMargin:
		// With configurable margin: (RTT + RTTVar) * (1 + margin)
		// Capture margin in closure (computed once, used many times)
		marginMultiplier := 1.0 + extraMargin
		r.rtoCalcFunc = func(rttVal, rttVarVal float64) uint64 {
			return uint64((rttVal + rttVarVal) * marginMultiplier)
		}
	default:
		// RTORttRttVar (default): RTT + RTTVar
		r.rtoCalcFunc = func(rttVal, rttVarVal float64) uint64 {
			return uint64(rttVal + rttVarVal)
		}
	}
}

// Recalculate updates RTT using EWMA smoothing (RFC 4.10).
// Also pre-calculates RTO for suppression logic.
// Uses atomic CAS to avoid locks.
func (r *rtt) Recalculate(rtt time.Duration) {
	lastRTT := float64(rtt.Microseconds())

	// Store raw sample (no smoothing) for diagnostics
	// This allows verification that arrival time capture is correct
	r.rttLastSampleUs.Store(uint64(lastRTT))

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

			// Pre-calculate RTO using function dispatch (no switch overhead)
			if r.rtoCalcFunc != nil {
				r.rtoUs.Store(r.rtoCalcFunc(newRTTVal, newRTTVarVal))
			}
			break
		}
		// CAS failed - another goroutine updated RTT, retry with new value
	}
}

func (r *rtt) RTT() float64 {
	return math.Float64frombits(r.rttBits.Load())
}

func (r *rtt) RTTVar() float64 {
	return math.Float64frombits(r.rttVarBits.Load())
}

// RTTLastSample returns the most recent RTT sample in microseconds WITHOUT EWMA smoothing.
// This is useful for diagnostics to verify arrival time capture is working correctly
// and to distinguish network variance from EWMA smoothing effects.
func (r *rtt) RTTLastSample() uint64 {
	return r.rttLastSampleUs.Load()
}

func (r *rtt) NAKInterval() float64 {
	// 4.8.2.  Packet Retransmission (NAKs)
	rttVal := math.Float64frombits(r.rttBits.Load())
	rttVarVal := math.Float64frombits(r.rttVarBits.Load())
	// Use multiplication instead of division (faster: ~3-4 cycles vs ~15-20 cycles)
	nakInterval := (rttVal + 4*rttVarVal) * 0.5

	// Use configured minimum NAK interval (from Config.PeriodicNakIntervalMs)
	minNakInterval := float64(r.minNakIntervalUs.Load())
	if nakInterval < minNakInterval {
		nakInterval = minNakInterval
	}
	return nakInterval
}

// RTOUs returns the pre-calculated RTO in microseconds.
// This is the preferred way to access RTO for suppression checks.
// Use rtoUs.Load()/2 for one-way delay (sender-side suppression).
func (r *rtt) RTOUs() uint64 {
	return r.rtoUs.Load()
}
