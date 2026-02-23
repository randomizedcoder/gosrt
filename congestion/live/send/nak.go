package send

import (
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// NAK is the public API for receiving NAKs.
// Routes through control ring (if enabled) for EventLoop processing,
// otherwise processes directly with locking.
// Returns the number of packets retransmitted (0 if routed to ring).
//
// IMPORTANT (Bug fix 2026-01-17): In EventLoop mode, we must NOT fall back
// to the locked path when the ring is full. EventLoop is designed to be
// lock-free - it doesn't check the lock. See: sender_control_ring_overflow_test.go
func (s *sender) NAK(sequenceNumbers []circular.Number) uint64 {
	if len(sequenceNumbers) == 0 {
		return 0
	}

	// Phase 3: Route through control ring for EventLoop processing
	if s.controlRing != nil {
		if s.controlRing.PushNAK(sequenceNumbers) {
			s.metrics.SendControlRingPushedNAK.Add(1)
			return 0 // Actual count tracked when EventLoop processes
		}
		// Ring full
		s.metrics.SendControlRingDroppedNAK.Add(1)

		// CRITICAL: In EventLoop mode, do NOT fall back to locked path!
		// EventLoop doesn't hold the lock, so this would race with btree iteration.
		// Dropping a NAK is safe - receiver will send NAK again if packet still missing.
		if s.useEventLoop {
			return 0
		}
		// Tick mode - safe to use locked fallback (Tick holds the lock)

		// DEBUG ASSERT: Should never reach here in EventLoop mode (fix above should return)
		s.AssertNotEventLoopOnFallback("NAK")
	}

	// Legacy/Tick path with locking
	if s.lockTiming != nil {
		var result uint64
		metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
			result = s.nakLocked(sequenceNumbers)
		})
		return result
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.nakLocked(sequenceNumbers)
}

// nakLocked dispatches to btree or list implementation based on config.
func (s *sender) nakLocked(sequenceNumbers []circular.Number) uint64 {
	if s.useBtree {
		return s.nakBtree(sequenceNumbers)
	}
	// Legacy list mode
	if s.honorNakOrder {
		return s.nakLockedHonorOrder(sequenceNumbers)
	}
	return s.nakLockedOriginal(sequenceNumbers)
}

// nakBtree processes NAK using O(log n) btree lookup (Phase 1: Btree mode)
// Reference: lockless_sender_implementation_plan.md Step 1.10
func (s *sender) nakBtree(sequenceNumbers []circular.Number) uint64 {
	m := s.metrics

	// Defensive check: NAK should not request sequences we already ACK'd
	s.checkNakBeforeACK(sequenceNumbers)

	// Count packets requested by this NAK
	totalLossCount := metrics.CountNAKEntries(m, sequenceNumbers, metrics.NAKCounterRecv)
	totalLossBytes := totalLossCount * uint64(s.avgPayloadSize)

	m.CongestionSendPktLoss.Add(totalLossCount)
	m.CongestionSendByteLoss.Add(totalLossBytes)

	// Pre-fetch RTO values once (avoid repeated syscalls/atomics in loop)
	nowUs := uint64(time.Now().UnixMicro())
	var oneWayThreshold uint64
	if s.rtoUs != nil {
		oneWayThreshold = s.rtoUs.Load() / 2
	}

	retransCount := uint64(0)

	// Process each range in NAK
	for i := 0; i < len(sequenceNumbers); i += 2 {
		startSeq := sequenceNumbers[i].Val()
		endSeq := sequenceNumbers[i+1].Val()

		// O(log n) lookup for each sequence in range
		for seq := startSeq; circular.SeqLessOrEqual(seq, endSeq); seq = circular.SeqAdd(seq, 1) {
			p := s.packetBtree.Get(seq)
			if p == nil {
				// Packet not found (already ACK'd and removed, or dropped)
				continue
			}

			h := p.Header()

			// RTO suppression check
			if h.LastRetransmitTimeUs > 0 && oneWayThreshold > 0 {
				if nowUs-h.LastRetransmitTimeUs < oneWayThreshold {
					m.RetransSuppressed.Add(1)
					continue
				}
			}

			// Retransmit
			h.LastRetransmitTimeUs = nowUs
			h.TransmitCount++
			if h.TransmitCount == 1 {
				m.RetransFirstTime.Add(1)
			}
			m.RetransAllowed.Add(1)

			pktLen := p.Len()
			m.CongestionSendPktRetrans.Add(1)
			m.CongestionSendPkt.Add(1)
			m.CongestionSendByteRetrans.Add(uint64(pktLen))
			m.CongestionSendByte.Add(uint64(pktLen))

			s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
			m.SendRateBytesSent.Add(pktLen)
			m.SendRateBytesRetrans.Add(pktLen)

			h.RetransmittedPacketFlag = true
			s.deliver(p)
			retransCount++
		}
	}

	if retransCount < totalLossCount {
		m.CongestionSendNAKNotFound.Add(totalLossCount - retransCount)
	}

	return retransCount
}

// isNakBeforeACK checks if a NAK sequence number is before the last ACK'd sequence.
// This is a defensive check - NAK should never request sequences we already ACK'd.
// If this happens, it indicates timing issues (NAK sent before ACK arrived at sender)
// or potentially a receiver bug.
// Returns true if the sequence is before lastACKedSequence (invalid NAK).
func (s *sender) isNakBeforeACK(seqNum circular.Number) bool {
	return seqNum.Lt(s.lastACKedSequence)
}

// checkNakBeforeACK scans NAK entries for any sequence before lastACKedSequence.
// Increments NakBeforeACKCount metric once if any invalid entry is found.
// sequenceNumbers is pairs of [start, end] ranges per SRT NAK format.
func (s *sender) checkNakBeforeACK(sequenceNumbers []circular.Number) {
	for i := 0; i < len(sequenceNumbers); i += 2 {
		if s.isNakBeforeACK(sequenceNumbers[i]) {
			s.metrics.NakBeforeACKCount.Add(1)
			return // Count once per NAK packet, not per entry
		}
	}
}

// nakLockedOriginal processes a NAK (Negative Acknowledgement) from the receiver.
// This is the original implementation that iterates backward through the loss list.
// RFC SRT Appendix A defines two NAK encoding formats in the loss list:
// - Figure 21: Single sequence number (start == end) - 4 bytes on wire
// - Figure 22: Range of sequence numbers (start != end) - 8 bytes on wire
func (s *sender) nakLockedOriginal(sequenceNumbers []circular.Number) uint64 {
	// Check metrics once at the beginning of the function
	m := s.metrics

	// Defensive check: NAK should not request sequences we already ACK'd
	s.checkNakBeforeACK(sequenceNumbers)

	// Count packets requested by this NAK using shared helper.
	// This ensures 100% consistency with how the receiver counts sent NAKs.
	totalLossCount := metrics.CountNAKEntries(m, sequenceNumbers, metrics.NAKCounterRecv)
	totalLossBytes := totalLossCount * uint64(s.avgPayloadSize)

	// Increment loss counters for all reported losses (packets in NAK list)
	m.CongestionSendPktLoss.Add(totalLossCount)
	m.CongestionSendByteLoss.Add(totalLossBytes)

	// ──────────────────────────────────────────────────────────────────
	// PRE-FETCH VALUES ONCE (avoid repeated syscalls/atomics in loop)
	// Performance: 1 syscall + 1 atomic load instead of N each
	// ──────────────────────────────────────────────────────────────────
	nowUs := uint64(time.Now().UnixMicro())
	var oneWayThreshold uint64
	if s.rtoUs != nil {
		// One-way delay = RTO/2 (sender→receiver only, not round-trip)
		// Note: /2 on uint64 compiles to bit shift (>>1)
		oneWayThreshold = s.rtoUs.Load() / 2
	}

	// Now, retransmit packets that we can find in our buffer
	retransCount := uint64(0)
	for e := s.lossList.Back(); e != nil; e = e.Prev() {
		p := e.Value.(packet.Packet)

		for i := 0; i < len(sequenceNumbers); i += 2 {
			if p.Header().PacketSequenceNumber.Gte(sequenceNumbers[i]) && p.Header().PacketSequenceNumber.Lte(sequenceNumbers[i+1]) {
				h := p.Header()

				// ──────────────────────────────────────────────────────────────
				// RETRANSMIT SUPPRESSION CHECK (RTO-based)
				// Skip if previous retransmit hasn't had time to arrive at receiver.
				// Uses one-way delay (RTO/2) since we only care about Sender→Receiver.
				// ──────────────────────────────────────────────────────────────
				if h.LastRetransmitTimeUs > 0 && oneWayThreshold > 0 {
					if nowUs-h.LastRetransmitTimeUs < oneWayThreshold {
						// Too soon - previous retransmit still in flight
						m.RetransSuppressed.Add(1)
						continue // Skip this packet, check next
					}
				}

				// ──────────────────────────────────────────────────────────────
				// PROCEED WITH RETRANSMIT - update tracking
				// ──────────────────────────────────────────────────────────────
				h.LastRetransmitTimeUs = nowUs
				h.TransmitCount++

				// Track first-time vs repeated retransmits
				if h.TransmitCount == 1 {
					m.RetransFirstTime.Add(1)
				}
				m.RetransAllowed.Add(1)

				pktLen := p.Len()
				m.CongestionSendPktRetrans.Add(1)
				m.CongestionSendPkt.Add(1)
				m.CongestionSendByteRetrans.Add(uint64(pktLen))
				m.CongestionSendByte.Add(uint64(pktLen))

				//  5.1.2. SRT's Default LiveCC Algorithm
				s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)

				// Phase 1: Lockless - use atomic counters
				m.SendRateBytesSent.Add(pktLen)
				m.SendRateBytesRetrans.Add(pktLen)

				h.RetransmittedPacketFlag = true
				s.deliver(p)

				retransCount++
			}
		}
	}

	// Track NAK requests we couldn't fulfill (packets already dropped from buffer)
	// This happens when the receiver requests retransmission of packets that
	// exceeded our drop threshold and were discarded
	if retransCount < totalLossCount {
		m.CongestionSendNAKNotFound.Add(totalLossCount - retransCount)
	}

	return retransCount
}

// nakLockedHonorOrder processes a NAK by retransmitting packets in the order
// they appear in the NAK packet (receiver-controlled priority).
// This allows the receiver to prioritize which packets get retransmitted first,
// which is useful when the NAK btree consolidation orders entries by urgency.
func (s *sender) nakLockedHonorOrder(sequenceNumbers []circular.Number) uint64 {
	m := s.metrics

	// Defensive check: NAK should not request sequences we already ACK'd
	s.checkNakBeforeACK(sequenceNumbers)

	// Count packets requested by this NAK using shared helper.
	totalLossCount := metrics.CountNAKEntries(m, sequenceNumbers, metrics.NAKCounterRecv)
	totalLossBytes := totalLossCount * uint64(s.avgPayloadSize)

	// Increment loss counters for all reported losses
	m.CongestionSendPktLoss.Add(totalLossCount)
	m.CongestionSendByteLoss.Add(totalLossBytes)

	// ──────────────────────────────────────────────────────────────────
	// PRE-FETCH VALUES ONCE (avoid repeated syscalls/atomics in loop)
	// Performance: 1 syscall + 1 atomic load instead of N each
	// ──────────────────────────────────────────────────────────────────
	nowUs := uint64(time.Now().UnixMicro())
	var oneWayThreshold uint64
	if s.rtoUs != nil {
		// One-way delay = RTO/2 (sender→receiver only, not round-trip)
		oneWayThreshold = s.rtoUs.Load() / 2
	}

	// Retransmit packets in NAK order (honoring receiver priority)
	retransCount := uint64(0)

	// Process each range/single in the NAK list in order
	for i := 0; i < len(sequenceNumbers); i += 2 {
		startSeq := sequenceNumbers[i]
		endSeq := sequenceNumbers[i+1]

		// Find and retransmit packets in this range, in sequence order
		for e := s.lossList.Front(); e != nil; e = e.Next() {
			p := e.Value.(packet.Packet)
			h := p.Header()
			pktSeq := h.PacketSequenceNumber

			// Check if this packet is in the requested range
			if pktSeq.Gte(startSeq) && pktSeq.Lte(endSeq) {
				// ──────────────────────────────────────────────────────────────
				// RETRANSMIT SUPPRESSION CHECK (RTO-based)
				// Skip if previous retransmit hasn't had time to arrive at receiver.
				// Uses one-way delay (RTO/2) since we only care about Sender→Receiver.
				// ──────────────────────────────────────────────────────────────
				if h.LastRetransmitTimeUs > 0 && oneWayThreshold > 0 {
					if nowUs-h.LastRetransmitTimeUs < oneWayThreshold {
						// Too soon - previous retransmit still in flight
						m.RetransSuppressed.Add(1)
						continue // Skip this packet, check next
					}
				}

				// ──────────────────────────────────────────────────────────────
				// PROCEED WITH RETRANSMIT - update tracking
				// ──────────────────────────────────────────────────────────────
				h.LastRetransmitTimeUs = nowUs
				h.TransmitCount++

				// Track first-time vs repeated retransmits
				if h.TransmitCount == 1 {
					m.RetransFirstTime.Add(1)
				}
				m.RetransAllowed.Add(1)

				pktLen := p.Len()
				m.CongestionSendPktRetrans.Add(1)
				m.CongestionSendPkt.Add(1)
				m.CongestionSendByteRetrans.Add(uint64(pktLen))
				m.CongestionSendByte.Add(uint64(pktLen))

				// Update running average payload size
				s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)

				// Phase 1: Lockless - use atomic counters
				m.SendRateBytesSent.Add(pktLen)
				m.SendRateBytesRetrans.Add(pktLen)

				h.RetransmittedPacketFlag = true
				s.deliver(p)

				retransCount++
			}
		}
	}

	// Track NAK requests we couldn't fulfill
	if retransCount < totalLossCount {
		m.CongestionSendNAKNotFound.Add(totalLossCount - retransCount)
	}

	// Track that we honored NAK order (for metrics)
	if m != nil {
		m.CongestionSendNAKHonoredOrder.Add(1)
	}

	return retransCount
}
