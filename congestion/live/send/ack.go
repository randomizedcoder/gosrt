package send

import (
	"container/list"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (s *sender) ACK(sequenceNumber circular.Number) {
	if s.lockTiming != nil {
		metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
			s.ackLocked(sequenceNumber)
		})
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	s.ackLocked(sequenceNumber)
}

func (s *sender) ackLocked(sequenceNumber circular.Number) {
	// Check metrics once at the beginning of the function
	m := s.metrics

	// Track highest ACK'd sequence for NAK-before-ACK validation
	// This is a cumulative ACK - all sequences below this are confirmed
	if sequenceNumber.Gt(s.lastACKedSequence) {
		s.lastACKedSequence = sequenceNumber
	}

	removeList := make([]*list.Element, 0, s.lossList.Len())
	for e := s.lossList.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		if p.Header().PacketSequenceNumber.Lt(sequenceNumber) {
			// Remove packet from buffer because it has been successfully transmitted
			removeList = append(removeList, e)
		} else {
			break
		}
	}

	// These packets are not needed anymore (ACK'd)
	for _, e := range removeList {
		p := e.Value.(packet.Packet)

		m.CongestionSendPktBuf.Add(^uint64(0))                    // Decrement by 1
		m.CongestionSendByteBuf.Add(^uint64(uint64(p.Len()) - 1)) // Subtract pktLen
		// PktBuf and ByteBuf are decremented in atomic counters above

		s.lossList.Remove(e)

		// This packet has been ACK'd and we don't need it anymore
		p.Decommission()
	}

	s.pktSndPeriod = (s.avgPayloadSize + 16) * 1000000 / s.maxBW
}
