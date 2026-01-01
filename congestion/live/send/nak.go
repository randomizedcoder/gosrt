package send

import (
	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// NAK processes a NAK request and returns the number of packets retransmitted
func (s *sender) NAK(sequenceNumbers []circular.Number) uint64 {
	if len(sequenceNumbers) == 0 {
		return 0
	}

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

// nakLocked dispatches to the original or honor-order implementation.
func (s *sender) nakLocked(sequenceNumbers []circular.Number) uint64 {
	if s.honorNakOrder {
		return s.nakLockedHonorOrder(sequenceNumbers)
	}
	return s.nakLockedOriginal(sequenceNumbers)
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

	// Now, retransmit packets that we can find in our buffer
	retransCount := uint64(0)
	for e := s.lossList.Back(); e != nil; e = e.Prev() {
		p := e.Value.(packet.Packet)

		for i := 0; i < len(sequenceNumbers); i += 2 {
			if p.Header().PacketSequenceNumber.Gte(sequenceNumbers[i]) && p.Header().PacketSequenceNumber.Lte(sequenceNumbers[i+1]) {
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

				p.Header().RetransmittedPacketFlag = true
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

	// Retransmit packets in NAK order (honoring receiver priority)
	retransCount := uint64(0)

	// Process each range/single in the NAK list in order
	for i := 0; i < len(sequenceNumbers); i += 2 {
		startSeq := sequenceNumbers[i]
		endSeq := sequenceNumbers[i+1]

		// Find and retransmit packets in this range, in sequence order
		for e := s.lossList.Front(); e != nil; e = e.Next() {
			p := e.Value.(packet.Packet)
			pktSeq := p.Header().PacketSequenceNumber

			// Check if this packet is in the requested range
			if pktSeq.Gte(startSeq) && pktSeq.Lte(endSeq) {
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

				p.Header().RetransmittedPacketFlag = true
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
