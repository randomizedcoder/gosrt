package receive

// scan.go - Contiguous and gap scanning functions
// Extracted from receiver.go for better organization

import (
	"fmt"
	"math"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════
// NAK "Too Recent" Threshold Calculation
// ═══════════════════════════════════════════════════════════════════════════

// CalcTooRecentThreshold calculates the TSBPD threshold for "too recent" packets.
// Packets with PktTsbpdTime > threshold are considered "too recent" to NAK
// (they might still arrive out-of-order, not actually lost).
//
// Formula: threshold = now + tsbpdDelay * (1.0 - nakRecentPercent)
//
// With nakRecentPercent = 0.10 (10%):
//
//	threshold = now + tsbpdDelay * 0.90
//
// Derivation:
//   - A packet with PktTsbpdTime = T arrived at (T - tsbpdDelay)
//   - We wait nakRecentPercent (10%) of TSBPD after arrival before NAKing
//   - So we NAK when: now >= (T - tsbpdDelay) + tsbpdDelay * nakRecentPercent
//   - Rearranging: T <= now + tsbpdDelay * (1.0 - nakRecentPercent)
//   - Packets with T > threshold are "too recent"
//
// Example with tsbpdDelay=3s, nakRecentPercent=0.10:
//   - threshold = now + 2.7s
//   - Packets with TSBPD within 2.7s of now are scannable
//   - Packets with TSBPD more than 2.7s in the future are "too recent"
//
// This function is exported for unit testing.
func CalcTooRecentThreshold(now uint64, tsbpdDelay uint64, nakRecentPercent float64) uint64 {
	if nakRecentPercent <= 0 || tsbpdDelay == 0 {
		return now
	}
	return now + uint64(float64(tsbpdDelay)*(1.0-nakRecentPercent))
}

// tooRecentThreshold is a convenience method that uses the receiver's configuration.
func (r *receiver) tooRecentThreshold(now uint64) uint64 {
	return CalcTooRecentThreshold(now, r.tsbpdDelay, r.nakRecentPercent)
}

// ═══════════════════════════════════════════════════════════════════════════
// Phase 7: Core Scan Functions (ACK Optimization)
// These functions use uint32 internally for efficiency.
// Wrappers convert to circular.Number at API boundary.
// ═══════════════════════════════════════════════════════════════════════════

// contiguousScan scans packet btree for contiguous sequences (ACKing process).
// Updates contiguousPoint atomically when progress is made.
// Thread-safe: uses atomic for contiguousPoint, caller handles btree access.
// Returns: ok=true if ACK should be sent, ackSeq=sequence to ACK
//
// IMPORTANT: Uses circular.SeqLessOrEqual for 31-bit wraparound safety.
// See: circular/seq_math_31bit_wraparound_test.go for the wraparound bug that was fixed.
//
// ⚠️ CRITICAL: All sequence comparisons MUST use circular.Seq* functions.
// Never use raw >, <, >=, <= or subtraction on sequence numbers.
//
// STALE contiguousPoint HANDLING:
// When btree.Min() > contiguousPoint, it means packets between contiguousPoint
// and btree.Min() have been delivered via TSBPD expiry and removed from the btree.
// In this case, we advance contiguousPoint to btree.Min()-1 to skip the gap.
// This is equivalent to the TSBPD skip logic in the old periodicACK().
func (r *receiver) contiguousScan() (ok bool, ackSeq uint32) {
	ok, ackSeq, _ = r.contiguousScanWithTime(r.nowFn())
	return ok, ackSeq
}

// contiguousScanWithTime scans for contiguous packets with TSBPD skip logic.
// The `now` parameter enables skipping packets whose TSBPD time has passed.
// Returns: ok=true if progress, ackSeq=sequence to ACK, skippedPkts=packets skipped due to TSBPD
func (r *receiver) contiguousScanWithTime(now uint64) (ok bool, ackSeq uint32, skippedPkts uint64) {
	// Atomic load of contiguous point
	lastContiguous := r.contiguousPoint.Load()

	// Get min packet (need btree access - caller ensures safety)
	minPkt := r.packetStore.Min()
	if minPkt == nil {
		return false, 0, 0 // Empty btree
	}

	minSeq := minPkt.Header().PacketSequenceNumber.Val()

	// TSBPD-AWARE CONTIGUOUS POINT ADVANCEMENT (Phase 5):
	// If btree.Min() is ahead of contiguousPoint AND min packet's TSBPD has expired,
	// then packets between contiguousPoint and btree.Min() are unrecoverable.
	//
	// Key insight: Use TSBPD time as the authority, NOT arbitrary gap thresholds.
	// See documentation/contiguous_point_tsbpd_advancement_design.md
	//
	// Scenarios where this triggers:
	// 1. Network outage longer than TSBPD buffer
	// 2. Large mid-stream gaps that were never recovered via NAK/retransmission
	// 3. Packets that "virtually roll off" the TSBPD buffer
	expectedNextSeq := circular.SeqAdd(lastContiguous, 1)
	gapSize := circular.SeqSub(minSeq, expectedNextSeq)

	if circular.SeqLess(expectedNextSeq, minSeq) && gapSize > 0 {
		// Gap exists between contiguousPoint and btree.Min()
		// Check if min packet's TSBPD has expired - if so, the gap is unrecoverable
		minTsbpdTime := minPkt.Header().PktTsbpdTime
		if minTsbpdTime > 0 && now > minTsbpdTime {
			// TSBPD expired - packets in the gap are unrecoverable
			// Advance contiguousPoint to btree.Min()-1
			//
			// Example: contiguousPoint=100, btree.Min()=200 (gap=99), now > TSBPD(200)
			//   - Packets 101-199 are TSBPD-expired (unrecoverable)
			//   - Advance contiguousPoint to 199 (btree.Min()-1)
			//   - Scan from 199 to find contiguous packets starting at 200
			lastContiguous = circular.SeqSub(minSeq, 1)
			skippedPkts = uint64(gapSize)

			// Track metrics
			if r.metrics != nil {
				r.metrics.ContiguousPointTSBPDAdvancements.Add(1)
				r.metrics.ContiguousPointTSBPDSkippedPktsTotal.Add(skippedPkts)
				r.metrics.CongestionRecvPktSkippedTSBPD.Add(skippedPkts)
				// Estimate skipped bytes using average payload size
				avgPayloadSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
				if avgPayloadSize == 0 {
					avgPayloadSize = 1316 // Default MPEG-TS payload
				}
				r.metrics.CongestionRecvByteSkippedTSBPD.Add(skippedPkts * avgPayloadSize)
			}

			// Log the TSBPD advancement
			if r.debug && r.logFunc != nil {
				r.logFunc("receiver:tsbpd:advance", func() string {
					return fmt.Sprintf("TSBPD advancement: skipped %d packets (seq %d-%d), new contiguousPoint=%d, minTSBPD=%d, now=%d",
						gapSize, expectedNextSeq, circular.SeqSub(minSeq, 1), lastContiguous, minTsbpdTime, now)
				})
			}
		}
	}

	// Scan forward looking for contiguous sequence
	// Track skipped packets for metrics
	var totalSkipped uint64
	startSeq := lastContiguous
	r.packetStore.IterateFrom(circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		func(p packet.Packet) bool {
			h := p.Header()
			seq := h.PacketSequenceNumber.Val()

			// Skip packets at or before current contiguous point
			// MUST use circular.SeqLessOrEqual for 31-bit wraparound!
			// Bug scenario: contiguousPoint=MAX-1, seq=2
			//   Raw: 2 <= MAX-1 → true (WRONG! 2 is circularly AFTER MAX-1)
			//   Circular: SeqLessOrEqual(2, MAX-1) → false (correct)
			if circular.SeqLessOrEqual(seq, startSeq) {
				return true
			}

			// TSBPD SKIP LOGIC:
			// If this packet's TSBPD time has passed, we can skip past any gap
			// to reach it. This handles the case where packets in the gap will
			// never arrive (TSBPD deadline passed).
			//
			// This matches the old periodicACK() behavior (lines 1303-1316).
			//
			// NOTE: For NAK to detect gaps before ACK skips them, NAK MUST run
			// BEFORE ACK in Tick(). See Tick() for the execution order.
			if h.PktTsbpdTime <= now && h.PktTsbpdTime > 0 {
				// Count skipped packets: gap between lastContiguous and this packet
				// e.g., if lastContiguous=4 and seq=7, then packets 5,6 are skipped
				expected := circular.SeqAdd(lastContiguous, 1)
				if circular.SeqLess(expected, seq) {
					innerGapSize := circular.SeqSub(seq, expected)
					totalSkipped += uint64(innerGapSize)
				}
				// TSBPD expired - advance past gap to this packet
				lastContiguous = seq
				return true // Continue scanning
			}

			// Check if next in sequence
			expected := circular.SeqAdd(lastContiguous, 1)
			if seq == expected {
				lastContiguous = seq
				return true // Continue scanning
			}

			return false // Gap found and not expired, stop
		})

	// Check for progress - either from stale adjustment or from scan
	originalContiguous := r.contiguousPoint.Load()
	if lastContiguous == originalContiguous {
		return false, 0, 0 // No progress
	}

	// Atomic store of new contiguous point
	r.contiguousPoint.Store(lastContiguous)

	// Return the sequence number to ACK (lastContiguous + 1 per SRT spec)
	// https://datatracker.ietf.org/doc/html/draft-sharabayko-srt-01#section-3.2.4
	//   Last Acknowledged Packet Sequence Number: 32 bits. This field
	//   contains the sequence number of the last data packet being
	//   acknowledged plus one. In other words, it is the sequence number
	//   of the first unacknowledged packet.
	return true, circular.SeqAdd(lastContiguous, 1), totalSkipped
}

// gapScan scans packet btree for gaps (missing sequences).
// Updates contiguousPoint atomically when contiguous packets are found before gaps.
// Thread-safe: uses atomic for contiguousPoint, caller handles btree access.
// Returns: list of missing sequence numbers to NAK
//
// IMPORTANT: Uses circular.SeqLessOrEqual for 31-bit wraparound safety.
// See: circular/seq_math_31bit_wraparound_test.go for the wraparound bug that was fixed.
//
// ⚠️ CRITICAL: All sequence comparisons MUST use circular.Seq* functions.
// Never use raw >, <, >=, <= or subtraction on sequence numbers.
//
// STALE contiguousPoint HANDLING:
// When btree.Min() > contiguousPoint, it means packets between contiguousPoint
// and btree.Min() have been delivered via TSBPD expiry and removed from the btree.
// We should NOT NAK those sequences - they were already delivered!
// Instead, advance the scan start point to btree.Min()-1.
// gapScan scans the packet btree for gaps and returns their sequences with estimated TSBPD times.
// Returns (gap sequences, estimated TSBPD times for each gap).
// The TSBPD times are used for RTT-aware early expiry (nak_btree_expiry_optimization.md).
func (r *receiver) gapScan() ([]uint32, []uint64) {
	// Atomic load of contiguous point (shared with contiguousScan)
	lastContiguous := r.contiguousPoint.Load()

	// Get min packet to check for stale contiguousPoint
	minPkt := r.packetStore.Min()
	if minPkt == nil {
		return nil, nil // Empty btree, no gaps to report
	}

	minSeq := minPkt.Header().PacketSequenceNumber.Val()

	// Get current time for TSBPD checks and "too recent" threshold
	// Use nowFn for testability (Phase 5: TSBPD-aware advancement)
	now := r.nowFn()

	// TSBPD-AWARE GAP SCAN START (Phase 5):
	// If btree.Min() is ahead of contiguousPoint AND min packet's TSBPD has expired,
	// then packets between contiguousPoint and btree.Min() are unrecoverable.
	// Don't NAK those sequences - they're TSBPD-expired.
	//
	// See documentation/contiguous_point_tsbpd_advancement_design.md
	expectedNextSeq := circular.SeqAdd(lastContiguous, 1)
	gapSize := circular.SeqSub(minSeq, expectedNextSeq)

	if circular.SeqLess(expectedNextSeq, minSeq) && gapSize > 0 {
		// Gap exists between contiguousPoint and btree.Min()
		// Check if min packet's TSBPD has expired - if so, the gap is unrecoverable
		minTsbpdTime := minPkt.Header().PktTsbpdTime
		if minTsbpdTime > 0 && now > minTsbpdTime {
			// TSBPD expired - packets in the gap are unrecoverable, don't NAK them
			// Advance local lastContiguous to btree.Min()-1 for this scan
			//
			// Note: The actual contiguousPoint update is done in contiguousScan()
			// This just prevents false NAKs for TSBPD-expired sequences
			lastContiguous = circular.SeqSub(minSeq, 1)
		}
	}

	// Calculate tooRecentThreshold - don't NAK packets that arrived recently
	// (they might be out-of-order, not lost)
	tooRecentThreshold := r.tooRecentThreshold(now)

	// Scan forward looking for gaps
	// Track boundary packets for TSBPD estimation (nak_btree_expiry_optimization.md)
	var gaps []uint32
	var gapTsbpds []uint64
	startSeq := lastContiguous
	expectedSeq := circular.SeqAdd(lastContiguous, 1)

	// Track lower boundary for TSBPD interpolation
	var lowerSeq uint32
	var lowerTsbpd uint64

	r.packetStore.IterateFrom(circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		func(p packet.Packet) bool {
			h := p.Header()
			seq := h.PacketSequenceNumber.Val()
			pktTsbpd := h.PktTsbpdTime

			// Skip packets at or before contiguous point
			// MUST use circular.SeqLessOrEqual for 31-bit wraparound!
			if circular.SeqLessOrEqual(seq, startSeq) {
				// Update lower boundary for future gap estimation
				lowerSeq = seq
				lowerTsbpd = pktTsbpd
				return true
			}

			// CRITICAL FIX: Detect gaps BEFORE checking "too recent" threshold.
			// This ensures that gaps leading up to "too recent" packets are still
			// detected and NAKed. Previously, the "too recent" check came first,
			// causing gaps just before "too recent" packets to never be detected.
			//
			// Record gaps between expectedSeq and seq
			// Use circular.SeqLess to handle wraparound in gap detection
			if expectedSeq != seq && circular.SeqLess(expectedSeq, seq) {
				// We have a gap! Current packet is the upper boundary
				upperSeq := seq
				upperTsbpd := pktTsbpd

				// Estimate TSBPD for each missing sequence
				for expectedSeq != seq && circular.SeqLess(expectedSeq, seq) {
					gaps = append(gaps, expectedSeq)

					// Estimate TSBPD using linear interpolation or fallback
					var estTsbpd uint64
					switch {
					case lowerTsbpd > 0 && upperTsbpd > 0:
						// Have both boundaries - use linear interpolation
						estTsbpd = estimateTsbpdForSeq(expectedSeq, lowerSeq, lowerTsbpd, upperSeq, upperTsbpd)
						if r.metrics != nil {
							r.metrics.NakTsbpdEstBoundary.Add(1)
						}
					case upperTsbpd > 0:
						// Only upper boundary - use fallback
						estTsbpd = r.estimateTsbpdFallback(expectedSeq, upperSeq, upperTsbpd)
						if r.metrics != nil {
							r.metrics.NakTsbpdEstEWMA.Add(1)
						}
					case lowerTsbpd > 0:
						// Only lower boundary - use fallback
						estTsbpd = r.estimateTsbpdFallback(expectedSeq, lowerSeq, lowerTsbpd)
						if r.metrics != nil {
							r.metrics.NakTsbpdEstEWMA.Add(1)
						}
					default:
						// No boundaries (shouldn't happen) - use conservative estimate
						estTsbpd = now + r.tsbpdDelay
						if r.metrics != nil {
							r.metrics.NakTsbpdEstEWMA.Add(1)
						}
					}

					gapTsbpds = append(gapTsbpds, estTsbpd)
					expectedSeq = circular.SeqAdd(expectedSeq, 1)
				}
			}

			// Check if this packet is "too recent" (might still be reordered)
			// Per ack_optimization_plan.md Section 3.2: packets beyond tooRecentThreshold
			// are in the "TOO RECENT" zone.
			//
			// NOTE: We've already detected any gaps leading TO this packet above.
			// Now we decide whether to STOP scanning. If this packet is "too recent",
			// we stop here - but we've already recorded the gaps up to this point.
			if pktTsbpd > tooRecentThreshold {
				return false // Stop iteration (but gaps up to this packet already recorded)
			}

			// This packet is present - if no gaps before it, advance contiguousPoint
			// Example: contiguousPoint=5, we find 6,7,8 present, gap at 9
			//          → lastContiguous advances to 8, gaps=[9]
			if len(gaps) == 0 {
				lastContiguous = seq
			}

			// Update lower boundary for next iteration
			lowerSeq = seq
			lowerTsbpd = pktTsbpd

			expectedSeq = circular.SeqAdd(seq, 1)
			return true
		})

	// Update contiguousPoint if we made progress
	originalContiguous := r.contiguousPoint.Load()
	if lastContiguous != originalContiguous {
		r.contiguousPoint.Store(lastContiguous)
	}

	return gaps, gapTsbpds
}

// ═══════════════════════════════════════════════════════════════════════════
// End Phase 7: Core Scan Functions
// ═══════════════════════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════════════════════
// Phase 8: Locked Wrappers (ACK Optimization)
// These wrap the core scan functions with locking for Tick()-based mode.
// ═══════════════════════════════════════════════════════════════════════════

// periodicACKLocked implements full ACK logic for Tick()-based mode.
// Uses contiguousScan() for scanning but adds timing, Light ACK logic, and metrics.
// Returns: ok=true if ACK should be sent, seq=sequence to ACK, lite=true for Light ACK
//
// Phase 10: Full implementation with all functionality from periodicACK():
// - Interval check (Full ACK every 10ms)
// - Light ACK triggering based on lightACKDifference
// - lastACKSequenceNumber and lastPeriodicACK updates
// - Buffer time metrics
