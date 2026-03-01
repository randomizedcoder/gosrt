package receive

// nak.go - NAK generation and handling functions
// Extracted from receiver.go for better organization

import (
	"fmt"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// periodicNakBtreeLocked is the locking wrapper for Tick mode.
// Acquires r.lock.RLock for packetStore access, then calls the primary function.
// Called from Tick() and legacy code paths.
func (r *receiver) periodicNakBtreeLocked(now uint64) []circular.Number {
	r.AssertTickContext()

	r.lock.RLock()
	defer r.lock.RUnlock()

	return r.periodicNakBtree(now)
}

// ═══════════════════════════════════════════════════════════════════════════
// End Phase 8: Locked Wrappers
// ═══════════════════════════════════════════════════════════════════════════

// periodicACK calculates the ACK sequence number by scanning contiguous packets.
//
// Performance optimizations (see integration_testing_50mbps_defect.md Section 24 & 26):
// - Uses IterateFrom with AscendGreaterOrEqual for O(log n) seek
// - ACK Scan High Water Mark: only scans NEW packets, not entire buffer (96.7% reduction)
// - Batches metrics updates with stack counters (single atomic update after loop)
// - Minimizes operations under RLock
func (r *receiver) periodicNAK(now uint64) []circular.Number {
	// Debug: log dispatch decision
	if r.debug && r.logFunc != nil {
		if r.useNakBtree {
			btreeSize := 0
			if r.nakBtree != nil {
				btreeSize = r.nakLen()
			}
			r.logFunc("receiver:nak:debug", func() string {
				return fmt.Sprintf("periodicNAK: using NAK btree (size=%d, nakBtree=%v)",
					btreeSize, r.nakBtree != nil)
			})
		} else {
			r.logFunc("receiver:nak:debug", func() string {
				return "periodicNAK: using original (packet btree scan)"
			})
		}
	}

	// Dispatch to appropriate implementation
	if r.useNakBtree {
		return r.periodicNakBtree(now)
	}
	return r.periodicNakOriginal(now)
}

// periodicNakOriginal is the original implementation that iterates through the packet store.
func (r *receiver) periodicNakOriginal(now uint64) []circular.Number {
	if r.lockTiming != nil {
		var result []circular.Number
		metrics.WithRLockTiming(r.lockTiming, &r.lock, func() {
			result = r.periodicNakOriginalLocked(now)
		})
		return result
	}
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.periodicNakOriginalLocked(now)
}

// periodicNakOriginalLocked builds the NAK loss list by iterating through the packet store.
// RFC SRT Appendix A defines two NAK encoding formats:
// - Figure 21: Single sequence number (start == end) - 4 bytes on wire
// - Figure 22: Range of sequence numbers (start != end) - 8 bytes on wire
// The list contains pairs [start, end] for each gap found.
func (r *receiver) periodicNakOriginalLocked(now uint64) []circular.Number {
	if now-r.lastPeriodicNAK < r.periodicNAKInterval {
		return nil
	}

	// Track that periodicNAK actually ran (not just returned early)
	// Used for health monitoring: expected ~50/sec (20ms interval)
	m := r.metrics
	if m != nil {
		m.CongestionRecvPeriodicNAKRuns.Add(1)
		m.NakPeriodicOriginalRuns.Add(1)
	}

	list := []circular.Number{}

	// Send a periodic NAK

	ackSequenceNumber := r.lastACKSequenceNumber

	// Send a NAK for all gaps.
	// Not all gaps might get announced because the size of the NAK packet is limited.
	r.packetStore.Iterate(func(p packet.Packet) bool {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()

		// Skip packets that we already ACK'd.
		if h.PacketSequenceNumber.Lte(ackSequenceNumber) {
			return true // Continue
		}

		// If this packet is not in sequence, we stop here and report that gap.
		if !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
			nackSequenceNumber := ackSequenceNumber.Inc()

			list = append(list, nackSequenceNumber)
			list = append(list, h.PacketSequenceNumber.Dec())
		}

		ackSequenceNumber = h.PacketSequenceNumber
		return true // Continue
	})

	r.lastPeriodicNAK = now

	return list
}

// periodicNakBtree is the primary NAK function for EventLoop mode.
// Scans the packet btree to find gaps and builds NAK list.
// NO LOCK - EventLoop is single-threaded, so no lock needed.
// Called from EventLoop directly, or from periodicNakBtreeLocked() in Tick mode.
//
// Algorithm:
// 1. Scan packet btree from last ACK'd sequence
// 2. For each gap in the sequence, add missing seqs to NAK btree
// 3. Skip packets that are "too recent" (might still be in flight)
// 4. Consolidate NAK btree into ranges and return
//
// Performance optimizations (see integration_testing_50mbps_defect.md Section 23.8):
// - Uses IterateFrom with AscendGreaterOrEqual for O(log n) seek
// - Uses sync.Pool for gap slice reuse (zero allocs per cycle)
// - Batches metrics updates (single atomic op instead of per-packet)
// - expireNakEntries moved to Tick() after sendNAK (not in hot path)
//
// This function delegates to periodicNakBtreePhased for phase-decomposed
// implementation with reduced cyclomatic complexity (see nak_phases.go).
func (r *receiver) periodicNakBtree(now uint64) []circular.Number {
	return r.periodicNakBtreePhased(now)
}

// expireNakEntries removes entries from the NAK btree that are too old to be useful.
// Uses RTT-aware time-based expiry (nak_btree_expiry_optimization.md) when RTT is available,
// falls back to sequence-based expiry otherwise.
//
// Time-based expiry: Entry expires if TSBPD < now + RTO*(1+nakExpiryMargin)
// Sequence-based fallback: Entry expires if seq < oldest packet's sequence
//
// This is called in Tick() AFTER sendNAK to keep it out of the hot path.
func (r *receiver) expireNakEntries() int {
	if r.nakBtree == nil {
		// This should never happen when useNakBtree=true (ISSUE-001)
		if r.metrics != nil {
			r.metrics.NakBtreeNilWhenEnabled.Add(1)
		}
		return 0
	}

	// Try time-based expiry first (preferred - nak_btree_expiry_optimization.md)
	nowUs := r.nowFn()
	expiryThreshold := r.calculateExpiryThreshold(nowUs)

	if expiryThreshold > 0 {
		// RTT available - use time-based expiry
		expired := r.nakDeleteBeforeTsbpd(expiryThreshold)
		if expired > 0 && r.metrics != nil {
			r.metrics.NakBtreeExpiredEarly.Add(uint64(expired))
		}
		return expired
	}

	// Fallback: sequence-based expiry (RTT not yet available)
	// Find the oldest packet in the packet btree (brief lock)
	r.lock.RLock()
	minPkt := r.packetStore.Min()
	var cutoff uint32
	if minPkt != nil {
		cutoff = minPkt.Header().PacketSequenceNumber.Val()
	}
	r.lock.RUnlock()

	if minPkt == nil {
		return 0 // Empty packet store, nothing to expire
	}

	// Any NAK entry older than the oldest packet's sequence is expired
	// (the packet btree has already released those packets via TSBPD)
	expired := r.nakDeleteBefore(cutoff)
	if expired > 0 && r.metrics != nil {
		r.metrics.NakBtreeExpired.Add(uint64(expired))
	}

	return expired
}

// --- TSBPD Estimation Functions (nak_btree_expiry_optimization.md Section 4) ---

// updateInterPacketInterval tracks the inter-packet arrival interval using EWMA.
// This is used as a fallback for TSBPD estimation when linear interpolation
// is not possible (e.g., gap at start of buffer, single packet).
//
// The function is extracted for testability - allows unit testing of the EWMA
// logic without needing full receiver setup.
//
// Parameters:
//   - nowUs: Current time in microseconds
//   - lastArrivalUs: Previous packet arrival time (0 if first packet)
//   - oldInterval: Current EWMA value (0 if uninitialized)
//
// Returns:
//   - newInterval: Updated EWMA value (0 if measurement invalid)
//   - valid: Whether the measurement was valid and interval was updated
func updateInterPacketInterval(nowUs, lastArrivalUs, oldInterval uint64) (newInterval uint64, valid bool) {
	// Need a previous arrival time to calculate interval
	if lastArrivalUs == 0 || nowUs <= lastArrivalUs {
		return 0, false
	}

	intervalUs := nowUs - lastArrivalUs

	// Clamp to valid range to filter outliers
	if intervalUs < InterPacketIntervalMinUs || intervalUs > InterPacketIntervalMaxUs {
		return 0, false
	}

	// First measurement: use directly
	if oldInterval == 0 {
		return intervalUs, true
	}

	// EWMA update: 87.5% old + 12.5% new
	newInterval = uint64(float64(oldInterval)*(1.0-InterPacketEWMAAlpha) + float64(intervalUs)*InterPacketEWMAAlpha)
	return newInterval, true
}

// isEWMAWarm returns true if enough inter-packet samples have been collected
// for the EWMA to be considered reliable.
//
// See nak_btree_expiry_optimization.md Section 4.6 for warm-up strategy.
func (r *receiver) isEWMAWarm() bool {
	// Threshold of 0 means warm-up check is disabled (always warm)
	if r.ewmaWarmupThreshold == 0 {
		return true
	}
	return r.interPacketEst.sampleCount.Load() >= r.ewmaWarmupThreshold
}

// estimateTsbpdForSeq uses linear interpolation to estimate TSBPD for a missing sequence.
// This provides accurate estimates when we have boundary packets on both sides of the gap.
//
// Formula: TSBPD_missing = lowerTsbpd + (missingSeq - lowerSeq) * (upperTsbpd - lowerTsbpd) / (upperSeq - lowerSeq)
//
// Guards against adversarial inputs:
// - Inverted TSBPD (upper < lower): returns lowerTsbpd
// - Equal TSBPD: returns lowerTsbpd
// - Equal sequence: returns lowerTsbpd
// - Result monotonicity: result >= lowerTsbpd for forward gaps
//
// See nak_btree_expiry_optimization.md Section 4.5.6 for design rationale.
func estimateTsbpdForSeq(missingSeq, lowerSeq uint32, lowerTsbpd uint64, upperSeq uint32, upperTsbpd uint64) uint64 {
	// Guard #1: Inverted or equal TSBPD - return safe fallback
	if upperTsbpd <= lowerTsbpd {
		return lowerTsbpd
	}

	// Guard #2: Equal sequence (shouldn't happen but be safe)
	if upperSeq == lowerSeq {
		return lowerTsbpd
	}

	// Safe to interpolate
	seqRange := uint64(circular.SeqSub(upperSeq, lowerSeq))
	if seqRange == 0 {
		return lowerTsbpd
	}

	tsbpdRange := upperTsbpd - lowerTsbpd
	seqOffset := uint64(circular.SeqSub(missingSeq, lowerSeq))

	estimated := lowerTsbpd + (seqOffset * tsbpdRange / seqRange)

	// Guard #3: Monotonicity - result should be >= lowerTsbpd
	if estimated < lowerTsbpd {
		return lowerTsbpd
	}

	return estimated
}

// estimateTsbpdFallback uses inter-packet interval when linear interpolation not possible.
// This handles edge cases where we don't have both boundary packets:
//   - Gap at start of packet buffer (no lower boundary)
//   - Single packet in buffer
//
// During warm-up (EWMA not yet reliable), uses conservative tsbpdDelay estimate.
// See nak_btree_expiry_optimization.md Section 4.6 for warm-up strategy.
//
// Returns estimated TSBPD for missingSeq based on reference packet.
func (r *receiver) estimateTsbpdFallback(missingSeq uint32, refSeq uint32, refTsbpd uint64) uint64 {
	// During warm-up, use conservative estimate (tsbpdDelay as worst-case)
	// This may slightly over-NAK but won't miss recovery opportunities
	if !r.isEWMAWarm() {
		if r.metrics != nil {
			r.metrics.NakTsbpdEstColdFallback.Add(1)
		}
		// Conservative: assume refTsbpd + full tsbpdDelay per packet
		// This over-estimates TSBPD, meaning we'll expire NAKs later (safer)
		return refTsbpd + r.tsbpdDelay
	}

	// EWMA is warm - use it
	intervalUs := r.interPacketEst.avgIntervalUs.Load()
	if intervalUs == 0 {
		// Edge case: warm but no interval (shouldn't happen, but handle it)
		intervalUs = InterPacketIntervalDefaultUs
	}

	// Calculate signed sequence difference for forward/backward estimation
	seqDiff := int64(circular.SeqSub(missingSeq, refSeq))

	// Estimate TSBPD: ref + (seqDiff * interval)
	estimated := uint64(int64(refTsbpd) + seqDiff*int64(intervalUs))

	// TSBPD Monotonicity Guard for forward gaps
	if seqDiff > 0 && estimated < refTsbpd {
		return refTsbpd
	}

	return estimated
}

// calculateExpiryThreshold computes the TSBPD threshold for NAK entry expiry.
// Entries with TSBPD < threshold are expired (no time for retransmit to arrive).
//
// Formula: threshold = now + (RTO * (1 + nakExpiryMargin))
//
// Returns 0 if RTT not yet available (use sequence-based fallback).
//
// See nak_btree_expiry_optimization.md Section 5.2.4 for design.
func (r *receiver) calculateExpiryThreshold(nowUs uint64) uint64 {
	if r.rtt == nil {
		return 0 // No RTT provider - use fallback
	}

	rtoUs := r.rtt.RTOUs()
	if rtoUs == 0 {
		return 0 // RTT not yet measured - use fallback
	}

	// Apply percentage-based nakExpiryMargin: RTO * (1 + nakExpiryMargin)
	adjustedRtoUs := uint64(float64(rtoUs) * (1.0 + r.nakExpiryMargin))

	return nowUs + adjustedRtoUs
}

// drainPacketRing consumes all packets from the lock-free ring into the btree.
// This is called at the start of Tick() when UsePacketRing is enabled.
//
// Key properties:
//   - TryRead() is NON-BLOCKING: returns (nil, false) when ring is empty
//   - Loop terminates immediately when ring is empty
//   - After drain, Tick() has exclusive access to btree (no producers writing)
//   - Packets are validated for duplicates/old sequences before insertion
//
// This is the Phase 3 lockless design: producers write to ring (lock-free),
// single consumer (Tick) drains ring and processes btree exclusively.
