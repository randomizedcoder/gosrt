package receive

// nak.go - NAK generation and handling functions
// Extracted from receiver.go for better organization

import (
	"fmt"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (r *receiver) periodicNakBtreeLocked(now uint64) []circular.Number {
	// Interval check: only run NAK every periodicNAKInterval
	if now-r.lastPeriodicNAK < r.periodicNAKInterval {
		if r.metrics != nil {
			r.metrics.NakPeriodicSkipped.Add(1)
		}
		return nil
	}

	// Track that periodicNAK ran
	m := r.metrics
	if m != nil {
		m.CongestionRecvPeriodicNAKRuns.Add(1)
	}

	r.lock.RLock()
	gaps := r.gapScan()
	r.lock.RUnlock()

	if len(gaps) == 0 {
		r.lastPeriodicNAK = now
		return nil
	}

	// Convert individual gaps to NAK ranges (start, end pairs)
	// Consolidate consecutive sequences into ranges for efficiency
	var result []circular.Number
	rangeStart := gaps[0]
	rangeEnd := gaps[0]

	for i := 1; i < len(gaps); i++ {
		// Check if this gap is consecutive with the current range
		// Use circular arithmetic for wraparound safety
		if circular.SeqAdd(rangeEnd, 1) == gaps[i] {
			rangeEnd = gaps[i]
		} else {
			// End current range and start new one
			result = append(result,
				circular.New(rangeStart, packet.MAX_SEQUENCENUMBER),
				circular.New(rangeEnd, packet.MAX_SEQUENCENUMBER))
			rangeStart = gaps[i]
			rangeEnd = gaps[i]
		}
	}

	// Add final range
	result = append(result,
		circular.New(rangeStart, packet.MAX_SEQUENCENUMBER),
		circular.New(rangeEnd, packet.MAX_SEQUENCENUMBER))

	r.lastPeriodicNAK = now
	return result
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
				btreeSize = r.nakBtree.Len()
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

// periodicNakBtree scans the packet btree to find gaps and builds NAK list.
// This is the new implementation for handling out-of-order packets with io_uring.
//
// Algorithm:
// 1. Scan packet btree from last ACK'd sequence
// 2. For each gap in the sequence, add missing seqs to NAK btree
// 3. Skip packets that are "too recent" (might still be in flight)
// 4. Consolidate NAK btree into ranges and return
//
// Performance optimizations (see integration_testing_50mbps_defect.md Section 23.8):
// - Uses IterateFrom with AscendGreaterOrEqual for O(log n) seek
// - Minimizes lock scope to only packetStore iteration
// - Uses sync.Pool for gap slice reuse (zero allocs per cycle)
// - Batches metrics updates (single atomic op instead of per-packet)
// - expireNakEntries moved to Tick() after sendNAK (not in hot path)
func (r *receiver) periodicNakBtree(now uint64) []circular.Number {
	// === PRE-WORK: No lock needed ===

	if now-r.lastPeriodicNAK < r.periodicNAKInterval {
		if r.metrics != nil {
			r.metrics.NakPeriodicSkipped.Add(1)
		}
		return nil
	}

	// Track that periodicNAK actually ran
	m := r.metrics
	if m != nil {
		m.CongestionRecvPeriodicNAKRuns.Add(1)
		m.NakPeriodicBtreeRuns.Add(1)
	}

	if r.nakBtree == nil {
		// This should never happen when useNakBtree=true (ISSUE-001)
		if m != nil {
			m.NakBtreeNilWhenEnabled.Add(1)
		}
		return nil
	}

	// Note: expireNakEntries() moved to Tick() after sendNAK - not in hot path

	// Step 1: Calculate "too recent" threshold (no lock needed)
	// See CalcTooRecentThreshold() for formula documentation
	tooRecentThreshold := r.tooRecentThreshold(now)

	// DEBUG: Log threshold calculation
	if r.debug && r.logFunc != nil {
		r.logFunc("receiver:nak:scan:debug", func() string {
			return fmt.Sprintf("periodicNakBtree SCAN: now=%d, tsbpdDelay=%d, nakRecentPercent=%.2f, tooRecentThreshold=%d (now+%dms)",
				now, r.tsbpdDelay, r.nakRecentPercent, tooRecentThreshold, (tooRecentThreshold-now)/1000)
		})
	}

	// Get gap slice from pool (zero allocs per cycle)
	gapsPtr := gapSlicePool.Get().(*[]uint32)
	defer func() {
		*gapsPtr = (*gapsPtr)[:0] // Reset length, preserve capacity
		gapSlicePool.Put(gapsPtr)
	}()

	// Track metrics locally, update once after loop (reduces atomic ops ~95x)
	var packetsScanned uint64
	var lastScannedSeq uint32

	// === MINIMAL LOCK SCOPE: Only for packetStore access ===
	r.lock.RLock()

	// Step 2: Get starting point from unified contiguousPoint (Phase 14)
	// Per ack_optimization_plan.md Section 3.1: Both ACK and NAK use contiguousPoint
	// We scan from contiguousPoint+1 since contiguousPoint is the last VERIFIED sequence
	contiguousPt := r.contiguousPoint.Load()
	startSeq := circular.SeqAdd(contiguousPt, 1) // Next expected sequence
	firstScanEver := false

	// Check if btree is empty
	minPkt := r.packetStore.Min()
	if minPkt == nil {
		r.lock.RUnlock()
		return nil // No packets yet
	}

	// TSBPD-AWARE NAK SCAN START (Phase 5):
	// If btree.Min() is ahead of startSeq AND min packet's TSBPD has expired,
	// then packets between startSeq and btree.Min() are unrecoverable.
	// Don't NAK those sequences - they're TSBPD-expired.
	//
	// See documentation/contiguous_point_tsbpd_advancement_design.md
	btreeMin := minPkt.Header().PacketSequenceNumber.Val()
	if circular.SeqLess(startSeq, btreeMin) {
		gapSize := circular.SeqSub(btreeMin, startSeq)
		if gapSize > 0 {
			// Check if min packet's TSBPD has expired
			minTsbpdTime := minPkt.Header().PktTsbpdTime
			if minTsbpdTime > 0 && now > minTsbpdTime {
				// TSBPD expired - packets in the gap are unrecoverable
				// Advance scan start to btreeMin, treat like first scan
				startSeq = btreeMin
				firstScanEver = true
			}
		}
	}

	// DEBUG: Log packet btree state
	if r.debug && r.logFunc != nil {
		minPkt := r.packetStore.Min()
		var minSeq uint32
		var minTsbpd uint64
		if minPkt != nil {
			minSeq = minPkt.Header().PacketSequenceNumber.Val()
			minTsbpd = minPkt.Header().PktTsbpdTime
		}
		btreeSize := r.packetStore.Len()
		r.logFunc("receiver:nak:scan:debug", func() string {
			return fmt.Sprintf("SCAN WINDOW: startSeq=%d, btree_min=%d, btree_size=%d, minTsbpd=%d, threshold=%d",
				startSeq, minSeq, btreeSize, minTsbpd, tooRecentThreshold)
		})
	}

	// Step 3: Scan packet btree from startSeq to find gaps
	// Uses IterateFrom with AscendGreaterOrEqual for O(log n) seek to start
	//
	// IMPORTANT: The first packet we find might be > startSeq because:
	// a) Packets between contiguousPoint and btree_min were DELIVERED (TSBPD expiry)
	// b) Packets between contiguousPoint and btree_min are ACTUALLY MISSING (lost)
	//
	// Phase 4: Use contiguousPoint instead of lastDeliveredSequenceNumber
	// - If startSeq <= contiguousPoint: those packets are past the contiguous boundary (skip)
	// - If startSeq > contiguousPoint: those packets are missing (detect gap)
	startSeqNum := circular.New(startSeq, packet.MAX_SEQUENCENUMBER)

	// Determine initial expectedSeq:
	// - On first scan ever: start from btree.Min() (we just learned the starting sequence)
	// - If startSeq is beyond contiguousPoint: use startSeq (detect gaps from there)
	// - If startSeq is at or before contiguousPoint: use contiguousPoint+1 (skip past boundary)
	var expectedSeq circular.Number
	if firstScanEver {
		// First NAK scan: we just learned the starting sequence from btree.Min()
		// expectedSeq should start from there - nothing before that was ever received
		expectedSeq = startSeqNum
	} else {
		contiguousPt := r.contiguousPoint.Load()
		if circular.SeqLessOrEqual(startSeq, contiguousPt) {
			// startSeq is at or before contiguousPoint, start expecting from contiguousPoint+1
			expectedSeq = circular.New(circular.SeqAdd(contiguousPt, 1), packet.MAX_SEQUENCENUMBER)
		} else {
			// startSeq is beyond contiguousPoint, start expecting from startSeq
			expectedSeq = startSeqNum
		}
	}
	firstPacket := true

	// DEBUG: Track first gap for logging
	var firstGapExpected, firstGapActual uint32
	var firstGapFound bool

	// scanPacket is a closure for processing each packet during NAK scan
	// Returns true to continue iteration, false to stop
	scanPacket := func(pkt packet.Packet) bool {
		h := pkt.Header()
		actualSeqNum := h.PacketSequenceNumber

		// Stop if this packet is "too recent" (might still be reordered)
		// Per ack_optimization_plan.md Section 3.2: packets beyond tooRecentThreshold
		// are in the "TOO RECENT" zone. Gaps leading to them might be reordering.
		if h.PktTsbpdTime > tooRecentThreshold {
			// DEBUG: Log why we stopped
			if r.debug && r.logFunc != nil && packetsScanned == 0 {
				r.logFunc("receiver:nak:scan:debug", func() string {
					return fmt.Sprintf("SCAN STOPPED AT FIRST PACKET: seq=%d, PktTsbpdTime=%d > threshold=%d (packet is too recent)",
						actualSeqNum.Val(), h.PktTsbpdTime, tooRecentThreshold)
				})
			}
			return false // Stop iteration
		}

		// For the first packet found, we DON'T reset expectedSeq
		// It was already set correctly above based on contiguousPoint (Phase 4)
		// This allows us to detect gaps from expectedSeq to actualSeqNum (if any)
		if firstPacket {
			firstPacket = false
			// expectedSeq was already set above, don't override
		}

		// Detect gaps: expected vs actual (only BETWEEN packets we find)
		if actualSeqNum.Gt(expectedSeq) {
			// There's a gap - collect missing sequences for batch insert
			gapStart := expectedSeq.Val()
			seq := gapStart
			endSeq := actualSeqNum.Dec().Val()

			// DEBUG: Record first gap for logging
			if !firstGapFound {
				firstGapExpected = gapStart
				firstGapActual = actualSeqNum.Val()
				firstGapFound = true
			}

			for circular.SeqLess(seq, endSeq) || seq == endSeq {
				*gapsPtr = append(*gapsPtr, seq)
				seq = circular.SeqAdd(seq, 1)
			}
		}

		packetsScanned++ // Local counter, not atomic
		lastScannedSeq = actualSeqNum.Val()
		expectedSeq = actualSeqNum.Inc()
		return true // Continue
	}

	// Pass 1: Iterate from startSeq to end of btree (in circular order)
	stoppedEarly := false
	r.packetStore.IterateFrom(startSeqNum, func(pkt packet.Packet) bool {
		if !scanPacket(pkt) {
			stoppedEarly = true
			return false
		}
		return true
	})

	// DEBUG: Log state after Pass 1
	if r.debug && r.logFunc != nil {
		r.logFunc("receiver:nak:scan:debug", func() string {
			return fmt.Sprintf("PASS1 COMPLETE: expectedSeq=%d, startSeq=%d, stoppedEarly=%v, packetsScanned=%d",
				expectedSeq.Val(), startSeq, stoppedEarly, packetsScanned)
		})
	}

	// Pass 2: Handle sequence number wraparound
	// If we started near MAX and expectedSeq has wrapped to near 0, we need to
	// continue scanning from the beginning of the btree (where 0, 1, 2, ... are stored)
	//
	// Detect wraparound: startSeq is "high" (> MAX/2) and expectedSeq is "low" (< MAX/2)
	// This means we've crossed the MAX→0 boundary.
	//
	// NOTE: We use SeqLess directly, NOT circular.Number.Lt() because:
	// - Lt() returns false when distance > threshold (half sequence space)
	// - SeqLess uses signed arithmetic which correctly identifies wraparound
	if !stoppedEarly && packetsScanned > 0 {
		// Check if we need to wrap around using SeqLess (not Lt)
		if circular.SeqLess(expectedSeq.Val(), startSeq) {
			if r.debug && r.logFunc != nil {
				r.logFunc("receiver:nak:scan:debug", func() string {
					return fmt.Sprintf("WRAPAROUND DETECTED: expectedSeq=%d < startSeq=%d, continuing from btree.Min()",
						expectedSeq.Val(), startSeq)
				})
			}

			// Continue from beginning of btree (where wrapped sequences are stored)
			// Stop when we reach startSeq or hit tooRecentThreshold
			//
			// NOTE: Use SeqGreaterOrEqual directly because Gte() uses threshold comparison
			// which doesn't work correctly for wraparound scenarios
			r.packetStore.Iterate(func(pkt packet.Packet) bool {
				h := pkt.Header()
				actualSeqNum := h.PacketSequenceNumber

				// Stop if we've reached or passed startSeq (completed the wrap)
				// Using SeqGreaterOrEqual for correct wraparound handling
				if circular.SeqGreaterOrEqual(actualSeqNum.Val(), startSeq) {
					return false
				}

				return scanPacket(pkt)
			})
		}
	}

	// DEBUG: Log scan results
	if r.debug && r.logFunc != nil {
		if firstGapFound {
			r.logFunc("receiver:nak:scan:debug", func() string {
				return fmt.Sprintf("GAPS DETECTED: first gap at expected=%d, actual=%d, total_gaps=%d, packets_scanned=%d, lastScannedSeq=%d",
					firstGapExpected, firstGapActual, len(*gapsPtr), packetsScanned, lastScannedSeq)
			})
		} else if packetsScanned > 0 {
			r.logFunc("receiver:nak:scan:debug", func() string {
				return fmt.Sprintf("NO GAPS: packets_scanned=%d, lastScannedSeq=%d",
					packetsScanned, lastScannedSeq)
			})
		}
	}

	r.lock.RUnlock()
	// === END LOCK SCOPE ===

	// === POST-WORK: No lock needed (nakBtree has its own lock) ===

	// Update metrics once (single atomic op instead of per-packet)
	if m != nil && packetsScanned > 0 {
		m.NakBtreeScanPackets.Add(packetsScanned)
	}

	// Batch insert all gaps with single lock acquisition
	if len(*gapsPtr) > 0 {
		inserted := r.nakBtree.InsertBatch(*gapsPtr)
		if m != nil {
			m.NakBtreeInserts.Add(uint64(inserted))
			m.NakBtreeScanGaps.Add(uint64(len(*gapsPtr)))
		}
	}

	// Step 4: Update contiguousPoint if we found contiguous packets (Phase 14)
	// Per ack_optimization_plan.md Section 3.2: gapScan advances contiguousPoint
	// if it finds contiguous packets BEFORE the first gap
	// Example: contiguousPoint=5, btree=[5,6,7,8,11,12] → advance to 8, gaps=[9,10]
	if lastScannedSeq > 0 && len(*gapsPtr) == 0 {
		// No gaps found - all scanned packets were contiguous, advance contiguousPoint
		r.contiguousPoint.Store(lastScannedSeq)
	}

	// Step 5: Consolidate NAK btree into ranges (has its own lock)
	list := r.consolidateNakBtree()

	// Update NAK btree size gauge
	if m != nil {
		m.NakBtreeSize.Store(uint64(r.nakBtree.Len()))
	}

	r.lastPeriodicNAK = now

	// Debug: log NAK list if not empty
	if r.debug && r.logFunc != nil && len(list) > 0 {
		btreeSize := r.nakBtree.Len()
		r.logFunc("receiver:nak:debug", func() string {
			// Show first few entries to avoid huge logs
			preview := list
			if len(preview) > 10 {
				preview = preview[:10]
			}
			return fmt.Sprintf("periodicNakBtree: generated %d NAK entries, btree_size=%d, first_10=%v",
				len(list), btreeSize, preview)
		})
	}

	return list
}

// expireNakEntries removes entries from the NAK btree that are too old to be useful.
// An entry is expired if its sequence is less than the oldest packet in the packet btree.
// This is called in Tick() AFTER sendNAK to keep it out of the hot path.
// The NAK btree has its own lock, so this only needs brief RLock for packetStore.Min().
func (r *receiver) expireNakEntries() int {
	if r.nakBtree == nil {
		// This should never happen when useNakBtree=true (ISSUE-001)
		if r.metrics != nil {
			r.metrics.NakBtreeNilWhenEnabled.Add(1)
		}
		return 0
	}

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
	// Use DeleteBeforeLocking() because this is called from tick() path (not event loop)
	expired := r.nakBtree.DeleteBeforeLocking(cutoff)
	if expired > 0 && r.metrics != nil {
		r.metrics.NakBtreeExpired.Add(uint64(expired))
	}

	return expired
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
