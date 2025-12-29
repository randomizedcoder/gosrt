package live

import (
	"sync/atomic"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
)

// checkFastNak triggers immediate NAK if silent period exceeded.
// Called from Push() when a packet arrives after potential silence.
// This handles the Starlink reconnection scenario where packets resume
// after a ~60ms outage.
//
// Must NOT hold r.lock when calling this (it acquires lock internally).
func (r *receiver) checkFastNak(now time.Time) {
	if !r.fastNakEnabled || !r.useNakBtree {
		return
	}

	// Load last arrival time atomically
	lastArrival := r.lastPacketArrivalTime.Load()
	if lastArrival.IsZero() {
		return // No previous packet
	}

	silentPeriod := now.Sub(lastArrival)
	if silentPeriod < r.fastNakThreshold {
		return // Not long enough silence
	}

	// Check if we just sent a NAK (avoid duplicate sends)
	lastNakTime := r.lastNakTime.Load()
	if !lastNakTime.IsZero() && now.Sub(lastNakTime) < time.Duration(r.periodicNAKInterval)*time.Microsecond {
		return
	}

	// Trigger FastNAK
	r.triggerFastNak(now)
}

// triggerFastNak runs the NAK generation and sends immediately.
// Used after detecting a silent period (potential network outage recovery).
func (r *receiver) triggerFastNak(now time.Time) {
	// Acquire lock and generate NAK list
	r.lock.Lock()
	list := r.buildNakListLocked()
	r.lock.Unlock()

	if len(list) == 0 {
		return
	}

	// Count and send NAK
	metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
	r.sendNAK(list)

	// Update last NAK time
	r.lastNakTime.Store(now)

	// Track metric
	if r.metrics != nil {
		r.metrics.NakFastTriggers.Add(1)
	}
}

// checkFastNakRecent detects sequence jump after outage and pre-populates NAK btree.
// Called from pushLockedNakBtree when a packet arrives.
// If we detect a "jump" in sequence numbers after a silent period, we immediately
// add the missing range to the NAK btree.
//
// Must be called with r.lock held.
func (r *receiver) checkFastNakRecent(currentSeq uint32, now time.Time) {
	if !r.fastNakRecentEnabled {
		return
	}
	if r.nakBtree == nil {
		// This should never happen when fastNakRecentEnabled=true (ISSUE-001)
		if r.metrics != nil {
			r.metrics.NakBtreeNilWhenEnabled.Add(1)
		}
		return
	}

	// Load last arrival time
	lastArrival := r.lastPacketArrivalTime.Load()
	if lastArrival.IsZero() {
		return
	}

	silentPeriod := now.Sub(lastArrival)
	if silentPeriod < r.fastNakThreshold {
		return // Not an outage
	}

	lastSeq := r.lastDataPacketSeq.Load()
	if lastSeq == 0 {
		return
	}

	// Calculate expected gap based on outage duration
	// Use packets per second estimate from rate stats
	pps := r.packetsPerSecondEstimate()
	if pps <= 0 {
		pps = 100 // Fallback estimate
	}
	expectedGap := uint32(silentPeriod.Seconds() * pps)

	// Actual gap (signed to handle wraparound correctly)
	actualGapSigned := circular.SeqDiff(currentSeq, lastSeq)
	if actualGapSigned <= 0 {
		return // Not a forward jump
	}
	actualGap := uint32(actualGapSigned)

	// If actual gap is significant, add to NAK btree
	// Use threshold to avoid false positives from io_uring reordering
	const minGapThreshold = uint32(10) // At least 10 packets gap

	// Upper bound: max(expectedGap*2, minUpperBound) but capped at maxAbsoluteGap
	// - minUpperBound prevents filtering legit gaps when pps estimate is stale/low
	// - maxAbsoluteGap prevents runaway insertions from sequence corruption
	// DISC-008 FIX: Original was just expectedGap*2 which filtered at wraparound
	const minUpperBound = uint32(1000)    // At least 1000 packets before filtering
	const maxAbsoluteGap = uint32(100000) // Never insert more than 100k NAKs

	upperBound := expectedGap * 2
	if upperBound < minUpperBound {
		upperBound = minUpperBound
	}
	if upperBound > maxAbsoluteGap {
		upperBound = maxAbsoluteGap
	}

	if actualGap > minGapThreshold && actualGap < upperBound {
		// Add missing range to NAK btree (as singles)
		seq := circular.SeqAdd(lastSeq, 1)
		for circular.SeqLess(seq, currentSeq) {
			r.nakBtree.Insert(seq)
			seq = circular.SeqAdd(seq, 1)
		}

		if r.metrics != nil {
			r.metrics.NakFastRecentInserts.Add(uint64(actualGap - 1))
		}
	}
}

// packetsPerSecondEstimate returns current packets per second estimate.
// Used by FastNAKRecent to calculate expected gap.
func (r *receiver) packetsPerSecondEstimate() float64 {
	// Phase 1: Lockless - Use atomic getter from ConnectionMetrics
	return r.metrics.GetRecvRatePacketsPerSec()
}

// AtomicTime provides atomic operations for time.Time values.
// Uses atomic.Value under the hood.
type AtomicTime struct {
	v atomic.Value
}

// Load atomically loads the time.Time value.
// Returns zero time if not yet stored.
func (at *AtomicTime) Load() time.Time {
	val := at.v.Load()
	if val == nil {
		return time.Time{}
	}
	return val.(time.Time)
}

// Store atomically stores the time.Time value.
func (at *AtomicTime) Store(t time.Time) {
	at.v.Store(t)
}

// buildNakListLocked generates the NAK list from the NAK btree.
// This is a helper used by both periodicNakBtree and triggerFastNak.
// Must be called with r.lock held.
func (r *receiver) buildNakListLocked() []circular.Number {
	if r.nakBtree == nil {
		// This should never happen when called (ISSUE-001)
		if r.metrics != nil {
			r.metrics.NakBtreeNilWhenEnabled.Add(1)
		}
		return nil
	}
	if r.nakBtree.Len() == 0 {
		return nil
	}

	// Use the consolidation function which handles merging
	return r.consolidateNakBtree()
}
