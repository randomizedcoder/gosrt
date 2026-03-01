package receive

// nak_phases.go - Phase-decomposed NAK generation functions
// Extracted from periodicNakBtree for reduced cyclomatic complexity,
// better testability, and improved benchmarkability.
//
// See: gocyclo_prealloc_progress_log.md Phase 3

import (
	"fmt"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// nakScanParams holds parameters calculated for a NAK scan operation.
// This struct enables testing and benchmarking of individual phases.
type nakScanParams struct {
	// startSeq is the sequence number to begin scanning from
	startSeq uint32

	// tooRecentThreshold is the TSBPD time beyond which packets are too recent
	tooRecentThreshold uint64

	// firstScanEver indicates this is the first NAK scan (no prior context)
	firstScanEver bool

	// btreeMinSeq is the minimum sequence in the packet btree
	btreeMinSeq uint32

	// btreeMinTsbpd is the TSBPD time of the minimum packet
	btreeMinTsbpd uint64
}

// nakScanResult holds the results of a gap detection scan.
type nakScanResult struct {
	// gaps contains the detected gap sequence numbers
	gaps []uint32

	// packetsScanned is the number of packets examined
	packetsScanned uint64

	// lastScannedSeq is the last sequence number scanned
	lastScannedSeq uint32
}

// gapSlicePool is defined in receiver.go (shared pool)

// shouldRunPeriodicNak checks if it's time to run periodic NAK.
// Returns false if rate limiting interval hasn't elapsed.
//
// Phase 1: Rate limiting and early exit.
func (r *receiver) shouldRunPeriodicNak(now uint64) bool {
	if now-r.lastPeriodicNAK < r.periodicNAKInterval {
		if r.metrics != nil {
			r.metrics.NakPeriodicSkipped.Add(1)
		}
		return false
	}
	return true
}

// calculateNakScanParams computes the parameters for a NAK scan.
// Returns nil if scan cannot proceed (e.g., empty btree, nil nakBtree).
//
// Phase 2: Scan parameter calculation.
func (r *receiver) calculateNakScanParams(now uint64) *nakScanParams {
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

	// Calculate "too recent" threshold
	tooRecentThreshold := r.tooRecentThreshold(now)

	// DEBUG: Log threshold calculation
	if r.debug && r.logFunc != nil {
		r.logFunc("receiver:nak:scan:debug", func() string {
			return fmt.Sprintf("periodicNakBtree SCAN: now=%d, tsbpdDelay=%d, nakRecentPercent=%.2f, tooRecentThreshold=%d (now+%dms)",
				now, r.tsbpdDelay, r.nakRecentPercent, tooRecentThreshold, (tooRecentThreshold-now)/1000)
		})
	}

	// Get starting point from unified contiguousPoint
	contiguousPt := r.contiguousPoint.Load()
	startSeq := circular.SeqAdd(contiguousPt, 1) // Next expected sequence
	firstScanEver := false

	// Check if btree is empty
	minPkt := r.packetStore.Min()
	if minPkt == nil {
		return nil // No packets yet
	}

	// TSBPD-AWARE NAK SCAN START (Phase 5):
	// If btree.Min() is ahead of startSeq AND min packet's TSBPD has expired,
	// then packets between startSeq and btree.Min() are unrecoverable.
	btreeMin := minPkt.Header().PacketSequenceNumber.Val()
	btreeMinTsbpd := minPkt.Header().PktTsbpdTime

	if circular.SeqLess(startSeq, btreeMin) {
		gapSize := circular.SeqSub(btreeMin, startSeq)
		if gapSize > 0 {
			// Check if min packet's TSBPD has expired
			if btreeMinTsbpd > 0 && now > btreeMinTsbpd {
				// TSBPD expired - packets in the gap are unrecoverable
				startSeq = btreeMin
				firstScanEver = true
			}
		}
	}

	// DEBUG: Log packet btree state
	if r.debug && r.logFunc != nil {
		btreeSize := r.packetStore.Len()
		r.logFunc("receiver:nak:scan:debug", func() string {
			return fmt.Sprintf("SCAN WINDOW: startSeq=%d, btree_min=%d, btree_size=%d, minTsbpd=%d, threshold=%d",
				startSeq, btreeMin, btreeSize, btreeMinTsbpd, tooRecentThreshold)
		})
	}

	return &nakScanParams{
		startSeq:           startSeq,
		tooRecentThreshold: tooRecentThreshold,
		firstScanEver:      firstScanEver,
		btreeMinSeq:        btreeMin,
		btreeMinTsbpd:      btreeMinTsbpd,
	}
}

// detectGaps scans the packet btree to find sequence gaps.
// Uses sync.Pool for gap slice to avoid allocations.
//
// Phase 3: Gap detection (the actual btree scan).
func (r *receiver) detectGaps(params *nakScanParams) *nakScanResult {
	// Get gap slice from pool (zero allocs per cycle)
	gapsPtr, ok := gapSlicePool.Get().(*[]uint32)
	if !ok {
		fallbackSlice := make([]uint32, 0, 128)
		gapsPtr = &fallbackSlice
	}

	result := &nakScanResult{
		gaps: *gapsPtr,
	}

	// Determine initial expectedSeq
	startSeqNum := circular.New(params.startSeq, packet.MAX_SEQUENCENUMBER)
	var expectedSeq circular.Number

	if params.firstScanEver {
		expectedSeq = startSeqNum
	} else {
		currentContiguousPt := r.contiguousPoint.Load()
		if circular.SeqLessOrEqual(params.startSeq, currentContiguousPt) {
			expectedSeq = circular.New(circular.SeqAdd(currentContiguousPt, 1), packet.MAX_SEQUENCENUMBER)
		} else {
			expectedSeq = startSeqNum
		}
	}
	firstPacket := true

	// DEBUG: Track first gap for logging
	var firstGapExpected, firstGapActual uint32
	var firstGapFound bool

	// scanPacket is a closure for processing each packet during NAK scan
	scanPacket := func(pkt packet.Packet) bool {
		h := pkt.Header()
		actualSeqNum := h.PacketSequenceNumber

		if firstPacket {
			firstPacket = false
		}

		// Detect gaps BEFORE checking "too recent" threshold
		if actualSeqNum.Gt(expectedSeq) {
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
				result.gaps = append(result.gaps, seq)
				seq = circular.SeqAdd(seq, 1)
			}
		}

		// Check if this packet is "too recent"
		if h.PktTsbpdTime > params.tooRecentThreshold {
			if r.debug && r.logFunc != nil {
				r.logFunc("receiver:nak:scan:debug", func() string {
					return fmt.Sprintf("SCAN STOPPED AT PACKET: seq=%d, PktTsbpdTime=%d > threshold=%d, gaps_detected=%d",
						actualSeqNum.Val(), h.PktTsbpdTime, params.tooRecentThreshold, len(result.gaps))
				})
			}
			return false // Stop iteration
		}

		result.packetsScanned++
		result.lastScannedSeq = actualSeqNum.Val()
		expectedSeq = actualSeqNum.Inc()
		return true // Continue
	}

	// Pass 1: Iterate from startSeq to end of btree
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
				expectedSeq.Val(), params.startSeq, stoppedEarly, result.packetsScanned)
		})
	}

	// Pass 2: Handle sequence number wraparound
	if !stoppedEarly && result.packetsScanned > 0 {
		if circular.SeqLess(expectedSeq.Val(), params.startSeq) {
			if r.debug && r.logFunc != nil {
				r.logFunc("receiver:nak:scan:debug", func() string {
					return fmt.Sprintf("WRAPAROUND DETECTED: expectedSeq=%d < startSeq=%d",
						expectedSeq.Val(), params.startSeq)
				})
			}

			r.packetStore.Iterate(func(pkt packet.Packet) bool {
				h := pkt.Header()
				actualSeqNum := h.PacketSequenceNumber

				if circular.SeqGreaterOrEqual(actualSeqNum.Val(), params.startSeq) {
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
				return fmt.Sprintf("GAPS DETECTED: first_gap_expected=%d, first_gap_actual=%d, total_gaps=%d, packets_scanned=%d",
					firstGapExpected, firstGapActual, len(result.gaps), result.packetsScanned)
			})
		} else if result.packetsScanned > 0 {
			r.logFunc("receiver:nak:scan:debug", func() string {
				return fmt.Sprintf("NO GAPS: packets_scanned=%d, lastScannedSeq=%d",
					result.packetsScanned, result.lastScannedSeq)
			})
		}
	}

	return result
}

// returnGapsToPool returns the gap slice to the pool for reuse.
func returnGapsToPool(gaps []uint32) {
	// Reset length, preserve capacity
	gaps = gaps[:0]
	gapSlicePool.Put(&gaps)
}

// processNakScanResult handles the results of a gap detection scan.
// Inserts gaps into NAK btree, updates metrics, and returns consolidated NAK list.
//
// Phase 4: NAK btree insert and consolidation.
func (r *receiver) processNakScanResult(scanResult *nakScanResult, now uint64) []circular.Number {
	m := r.metrics

	// Update metrics once (single atomic op instead of per-packet)
	if m != nil && scanResult.packetsScanned > 0 {
		m.NakBtreeScanPackets.Add(scanResult.packetsScanned)
	}

	// Batch insert all gaps with single lock acquisition
	if len(scanResult.gaps) > 0 {
		inserted := r.nakInsertBatch(scanResult.gaps)
		if m != nil {
			m.NakBtreeInserts.Add(uint64(inserted))
			m.NakBtreeScanGaps.Add(uint64(len(scanResult.gaps)))
		}
	}

	// Update contiguousPoint if we found contiguous packets
	if scanResult.lastScannedSeq > 0 && len(scanResult.gaps) == 0 {
		r.contiguousPoint.Store(scanResult.lastScannedSeq)
	}

	// Consolidate NAK btree into ranges
	list := r.consolidateNakBtree()

	// Update NAK btree size gauge
	if m != nil {
		m.NakBtreeSize.Store(uint64(r.nakLen()))
	}

	r.lastPeriodicNAK = now

	// Debug: log NAK list if not empty
	if r.debug && r.logFunc != nil && len(list) > 0 {
		btreeSize := r.nakLen()
		r.logFunc("receiver:nak:debug", func() string {
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

// periodicNakBtreePhased is the phase-decomposed version of periodicNakBtree.
// This function orchestrates the phases for better testability and lower complexity.
//
// Phases:
// 1. shouldRunPeriodicNak - Rate limiting check
// 2. calculateNakScanParams - Compute scan parameters
// 3. detectGaps - Scan btree for gaps
// 4. processNakScanResult - Insert gaps and consolidate
func (r *receiver) periodicNakBtreePhased(now uint64) []circular.Number {
	// Phase 1: Rate limiting
	if !r.shouldRunPeriodicNak(now) {
		return nil
	}

	// Phase 2: Calculate scan parameters
	params := r.calculateNakScanParams(now)
	if params == nil {
		return nil
	}

	// Phase 3: Detect gaps
	scanResult := r.detectGaps(params)
	defer returnGapsToPool(scanResult.gaps)

	// Phase 4: Process results
	return r.processNakScanResult(scanResult, now)
}
