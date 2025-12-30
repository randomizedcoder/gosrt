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
	rttBits          atomic.Uint64 // float64 stored as bits
	rttVarBits       atomic.Uint64 // float64 stored as bits
	minNakIntervalUs atomic.Uint64 // minimum NAK interval in microseconds (from config)
}

// Recalculate updates RTT using EWMA smoothing (RFC 4.10).
// Uses atomic CAS to avoid locks.
func (r *rtt) Recalculate(rtt time.Duration) {
	lastRTT := float64(rtt.Microseconds())

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

func (r *rtt) RTT() float64 {
	return math.Float64frombits(r.rttBits.Load())
}

func (r *rtt) RTTVar() float64 {
	return math.Float64frombits(r.rttVarBits.Load())
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

