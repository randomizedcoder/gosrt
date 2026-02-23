package receive

// ack.go - ACK generation and handling functions
// Extracted from receiver.go for better organization

import (
	"math"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (r *receiver) periodicACKLocked(now uint64) (ok bool, seq circular.Number, lite bool) {
	r.AssertTickContext() // Verify we're in Tick (locked) mode

	r.lock.RLock()

	// Early return check: Should we send ACK at all?
	// Full ACK: every periodicACKInterval (10ms)
	// Light ACK: when sequence advances by lightACKDifference
	needLiteACK := false
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		// Not time for Full ACK - check if we need Light ACK
		currentSeq := r.maxSeenSequenceNumber.Val()
		diff := circular.SeqSub(currentSeq, r.lastLightACKSeq)
		if diff >= r.lightACKDifference {
			needLiteACK = true
		} else {
			r.lock.RUnlock()
			return false, circular.Number{}, false // No ACK needed
		}
	}

	// Get buffer time info (for Full ACK metrics)
	minPktTsbpdTime, maxPktTsbpdTime := uint64(0), uint64(0)
	minPkt := r.packetStore.Min()
	if minPkt != nil {
		minH := minPkt.Header()
		minPktTsbpdTime = minH.PktTsbpdTime
		maxPktTsbpdTime = minH.PktTsbpdTime
	}

	// Use contiguousScanWithTime to find the ACK sequence (with TSBPD skip logic)
	// NOTE: contiguousScanWithTime() returns the "next expected" sequence (lastContiguous + 1)
	// per SRT spec. We need to convert this to "last received" for internal tracking.
	scanOk, nextExpectedSeq, skippedPkts := r.contiguousScanWithTime(now)

	// Get max TSBPD time for buffer calculation (iterate to find max)
	if scanOk {
		// Update maxPktTsbpdTime from contiguous packets
		// contiguousPoint is the last received, so iterate from there
		r.packetStore.IterateFrom(circular.New(r.contiguousPoint.Load(), packet.MAX_SEQUENCENUMBER), func(p packet.Packet) bool {
			h := p.Header()
			// contiguousPoint is already updated by contiguousScan, use it as boundary
			if circular.SeqLessOrEqual(h.PacketSequenceNumber.Val(), r.contiguousPoint.Load()) {
				if h.PktTsbpdTime > maxPktTsbpdTime {
					maxPktTsbpdTime = h.PktTsbpdTime
				}
				return true
			}
			return false
		})
	}

	r.lock.RUnlock()

	// Calculate ackSequenceNumber based on scan results or contiguousPoint
	// ackSequenceNumber is the LAST RECEIVED sequence, not the next expected
	ackSequenceNumber := r.lastACKSequenceNumber
	if scanOk {
		// contiguousScan advanced - use the returned nextExpectedSeq - 1
		lastReceivedSeq := circular.SeqSub(nextExpectedSeq, 1)
		ackSequenceNumber = circular.New(lastReceivedSeq, packet.MAX_SEQUENCENUMBER)
	} else {
		// contiguousScan found no progress, but gapScan (NAK path) might have
		// advanced contiguousPoint. We should update lastACKSequenceNumber to
		// reflect the current contiguousPoint, enabling packet delivery.
		//
		// Without this fix, packets won't be delivered because delivery requires
		// seq <= lastACKSequenceNumber, but lastACKSequenceNumber stays stale.
		currentCP := r.contiguousPoint.Load()
		if circular.SeqGreater(currentCP, r.lastACKSequenceNumber.Val()) {
			ackSequenceNumber = circular.New(currentCP, packet.MAX_SEQUENCENUMBER)
		}
	}

	// Write phase: update fields
	r.lock.Lock()
	defer r.lock.Unlock()

	m := r.metrics

	// Re-check interval (may have changed between read and write lock)
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		if !needLiteACK {
			return false, circular.Number{}, false
		}
		lite = true
	}

	// Update tracking fields
	// lastLightACKSeq tracks the last ACKed sequence (last received, not next expected)
	r.lastLightACKSeq = ackSequenceNumber.Val()
	r.lastACKSequenceNumber = ackSequenceNumber
	r.lastPeriodicACK = now

	// Track metrics
	if m != nil {
		m.CongestionRecvPeriodicACKRuns.Add(1)
		msBuf := (maxPktTsbpdTime - minPktTsbpdTime) / 1_000
		m.CongestionRecvMsBuf.Store(msBuf)

		// Track packets skipped due to TSBPD expiry
		if skippedPkts > 0 {
			m.CongestionRecvPktSkippedTSBPD.Add(skippedPkts)
			// Estimate byte count using average payload size
			avgPayloadSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
			m.CongestionRecvByteSkippedTSBPD.Add(skippedPkts * avgPayloadSize)
		}
	}

	// Return: seq is the "next expected" sequence per SRT ACK spec
	// which is ackSequenceNumber.Inc() (last received + 1)
	return true, ackSequenceNumber.Inc(), lite
}

// periodicNakBtreeLocked implements full NAK logic for Tick()-based mode.
// Uses gapScan() for gap detection but adds interval check and metrics.
// Returns: list of NAK ranges as circular.Number pairs (start, end)
//
// Phase 10: Full implementation with timing and range consolidation.
func (r *receiver) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
	r.AssertEventLoopContext() // Verify we're in EventLoop (lock-free) mode

	// Phase 1: Read-only work with read lock (allows concurrent Push() operations)
	r.lock.RLock()

	// Early return check (read-only)
	// Phase 5: ACK Optimization - Use difference-based Light ACK triggering
	needLiteACK := false
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		// Check if we've advanced enough for a Light ACK
		// Use maxSeenSequenceNumber (updated on Push) for early check
		// Use circular arithmetic for wraparound safety
		currentSeq := r.maxSeenSequenceNumber.Val()
		diff := circular.SeqSub(currentSeq, r.lastLightACKSeq)
		if diff >= r.lightACKDifference {
			needLiteACK = true // Will send light ACK
		} else {
			r.lock.RUnlock()
			return // Early return - no ACK needed
		}
	}

	// Read-only iteration (read lock allows concurrent Push() operations)
	minPktTsbpdTime, maxPktTsbpdTime := uint64(0), uint64(0)
	ackSequenceNumber := r.lastACKSequenceNumber

	// Get first packet - needed for buffer time calculation AND scan start validation
	minPkt := r.packetStore.Min()
	if minPkt == nil {
		// No packets in btree - but we should still send periodic ACK for keepalive.
		// This confirms to the sender what we've already received.
		// Skip to write phase with current lastACKSequenceNumber.
		r.lock.RUnlock()

		// Send ACK with last known sequence number (no new packets to acknowledge)
		if r.lockTiming != nil {
			var okResult bool
			var seqResult circular.Number
			var liteResult bool
			metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
				okResult, seqResult, liteResult = r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime, circular.Number{})
			})
			return okResult, seqResult, liteResult
		}
		r.lock.Lock()
		defer r.lock.Unlock()
		return r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime, circular.Number{})
	}
	minH := minPkt.Header()
	minPktTsbpdTime = minH.PktTsbpdTime
	maxPktTsbpdTime = minH.PktTsbpdTime
	minPktSeq := minH.PacketSequenceNumber

	// ACK Scan High Water Mark optimization (Section 26):
	// Instead of scanning from lastACKSequenceNumber every time, remember where we
	// verified contiguous packets up to. Only scan NEW packets since last check.
	// This reduces iterations by ~96.7% at steady state.
	//
	// Phase 10/14: Use contiguousPoint (unified scan variable) instead of ackScanHighWaterMark.
	// This shares the scan tracking with EventLoop mode and simplifies cleanup.
	scanStartPointVal := r.contiguousPoint.Load()
	scanStartPoint := circular.New(scanStartPointVal, packet.MAX_SEQUENCENUMBER)

	// Determine valid scan start point (must handle four cases):
	// 1. Not initialized (Val() == 0): start from lastACKSequenceNumber
	// 2. Behind lastACKSequenceNumber: start from lastACKSequenceNumber
	// 3. Behind minPkt (packets expired from btree): start from minPkt
	// 4. Valid (ahead of both): use high water mark
	if scanStartPointVal == 0 || scanStartPoint.Lt(ackSequenceNumber) {
		// Case 1 & 2: Not initialized or behind ACK point
		scanStartPoint = ackSequenceNumber
	}

	// Case 3: High water mark points to expired packet
	// Tick() released packets, minPkt advanced past our remembered position
	// NOTE: This only updates scanStartPoint (where to START iterating), NOT ackSequenceNumber.
	// We must still verify there's no gap between lastACKSequenceNumber and minPkt.
	if minPktSeq.Gt(scanStartPoint) {
		scanStartPoint = minPktSeq.Dec() // Start just before minPkt to include it
	}

	// NOTE: Removed buggy "Case 4" logic that advanced ackSequenceNumber based on scanStartPoint.
	// The scanStartPoint only controls WHERE we start iterating (for efficiency).
	// The ackSequenceNumber must only advance when we verify actual packet contiguity.
	// See: lockless_phase2_implementation.md "ACK Scan High Water Mark Bug" section.

	// Stack counter for skipped packets - batched update after loop (avoids atomic ops under lock)
	var totalSkippedPkts uint64
	var lastContiguousSeq circular.Number // Track highest verified contiguous sequence
	firstPacketChecked := false           // Track if we've checked the first packet

	// Find the sequence number up until we have all in a row.
	// Where the first gap is (or at the end of the list) is where we can ACK to.
	// Uses IterateFrom for O(log n) seek to scanStartPoint (not lastACKSequenceNumber!)
	r.packetStore.IterateFrom(scanStartPoint, func(p packet.Packet) bool {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()

		// Skip packets at or before scan start point (handles btree edge case)
		if h.PacketSequenceNumber.Lte(scanStartPoint) {
			return true // Continue
		}

		// CRITICAL FIX: Check for gap between lastACKSequenceNumber and first packet.
		// If scanStartPoint was advanced due to packet delivery (Case 3), we must verify
		// that there's no gap between the last ACKed packet and the first remaining packet.
		// Without this check, we'd incorrectly ACK past lost packets.
		if !firstPacketChecked {
			firstPacketChecked = true
			// If the first packet isn't contiguous with ackSequenceNumber, there's a gap
			// (unless this packet's TSBPD time has passed, in which case we skip it)
			if h.PktTsbpdTime > now && !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
				// Gap detected: can't advance ACK past lastACKSequenceNumber
				return false // Stop iteration
			}
		}

		// If there are packets that should have been delivered by now, move forward.
		// This is where we "skip" packets that NEVER arrived - count them!
		if h.PktTsbpdTime <= now {
			// Count packets skipped: gap between current ACK and this packet
			// e.g., if ackSequenceNumber=10 and h.PacketSequenceNumber=15,
			// then packets 11,12,13,14 are being skipped (4 packets)
			skippedCount := uint64(h.PacketSequenceNumber.Distance(ackSequenceNumber))
			if skippedCount > 1 {
				// skippedCount-1 because Distance(10,15)=5, but we skip 11,12,13,14 (4 packets)
				totalSkippedPkts += skippedCount - 1 // Stack counter, no atomic
			}
			ackSequenceNumber = h.PacketSequenceNumber
			lastContiguousSeq = ackSequenceNumber
			return true // Continue
		}

		// Check if the packet is the next in the row.
		if h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
			ackSequenceNumber = h.PacketSequenceNumber
			lastContiguousSeq = ackSequenceNumber
			maxPktTsbpdTime = h.PktTsbpdTime
			return true // Continue
		}

		return false // Stop iteration (gap found)
	})

	// Capture high water mark update for write phase
	newHighWaterMark := lastContiguousSeq

	// Release read lock before acquiring write lock (optimization: minimize lock contention)
	r.lock.RUnlock()

	// Update metrics ONCE after lock released (batched from stack counters)
	// avgPayloadSize is now atomic - no race condition, can read anytime
	m := r.metrics
	if m != nil && totalSkippedPkts > 0 {
		avgPayloadSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
		m.CongestionRecvPktSkippedTSBPD.Add(totalSkippedPkts)
		m.CongestionRecvByteSkippedTSBPD.Add(totalSkippedPkts * avgPayloadSize)
	}

	// Phase 2: Write updates with write lock (brief - only for field updates)
	// Measure lock timing for the write lock (critical section)
	if r.lockTiming != nil {
		var okResult bool
		var seqResult circular.Number
		var liteResult bool
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			okResult, seqResult, liteResult = r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime, newHighWaterMark)
		})
		return okResult, seqResult, liteResult
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime, newHighWaterMark)
}

func (r *receiver) periodicACKWriteLocked(now uint64, needLiteACK bool, ackSequenceNumber circular.Number, minPktTsbpdTime, maxPktTsbpdTime uint64, newHighWaterMark circular.Number) (ok bool, sequenceNumber circular.Number, lite bool) {
	// Check metrics once at the beginning of the function
	m := r.metrics

	// Re-check conditions (may have changed between read and write lock)
	// If interval check still applies and we don't need lite ACK, return early
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		if !needLiteACK {
			return // Early return - no update needed
		}
		// Lite ACK needed, continue to update fields
		lite = true
	}
	// Phase 5: ACK Optimization - Track last Light ACK sequence for difference check
	// Update on BOTH full ACK and lite ACK (ackSequenceNumber is the actual ACKed seq)
	r.lastLightACKSeq = ackSequenceNumber.Val()

	// Track that periodicACK actually ran (not just returned early)
	// Used for health monitoring: expected ~100/sec (10ms interval)
	if m != nil {
		m.CongestionRecvPeriodicACKRuns.Add(1)
	}

	// Update fields (write lock held - brief operation)
	ok = true
	sequenceNumber = ackSequenceNumber.Inc()

	// Keep track of the last ACK's sequence number. With this we can faster ignore
	// packets that come in late that have a lower sequence number.
	r.lastACKSequenceNumber = ackSequenceNumber

	// Phase 14: ackScanHighWaterMark removed - contiguousPoint is now the unified scan variable
	// contiguousPoint is updated by contiguousScan() in periodicACKLocked()

	r.lastPeriodicACK = now
	// RecvLightACKCounter.Store(0) removed - using difference-based Light ACK tracking (Phase 5)

	msBuf := (maxPktTsbpdTime - minPktTsbpdTime) / 1_000
	m.CongestionRecvMsBuf.Store(msBuf)

	return
}
