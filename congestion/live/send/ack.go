package send

import (
	"container/list"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ACK is the public API for receiving ACKs.
// Routes through control ring (if enabled) for EventLoop processing,
// otherwise processes directly with locking.
func (s *sender) ACK(sequenceNumber circular.Number) {
	// Phase 3: Route through control ring for EventLoop processing
	if s.useControlRing {
		if s.controlRing.PushACK(sequenceNumber) {
			s.metrics.SendControlRingPushedACK.Add(1)
			return
		}
		// Ring full - fallback to direct processing with lock
		s.metrics.SendControlRingDroppedACK.Add(1)
		// Fall through to locked path
	}

	// Legacy path with locking
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
	if s.useBtree {
		s.ackBtree(sequenceNumber)
	} else {
		s.ackList(sequenceNumber)
	}
}

// ackBtree processes ACK using btree DeleteBefore (Phase 1: Btree mode)
// Reference: lockless_sender_implementation_plan.md Step 1.11
func (s *sender) ackBtree(sequenceNumber circular.Number) {
	m := s.metrics

	// Track highest ACK'd sequence for NAK-before-ACK validation
	if sequenceNumber.Gt(s.lastACKedSequence) {
		s.lastACKedSequence = sequenceNumber
	}

	// Remove all packets before ACK sequence (O(k log n) where k = removed count)
	removed, packets := s.packetBtree.DeleteBefore(sequenceNumber.Val())

	// Decommission removed packets
	for _, p := range packets {
		pktLen := p.Len()
		m.CongestionSendPktBuf.Add(^uint64(0))                    // Decrement by 1
		m.CongestionSendByteBuf.Add(^uint64(uint64(pktLen) - 1)) // Subtract pktLen
		p.Decommission()
	}

	s.pktSndPeriod = (s.avgPayloadSize + 16) * 1000000 / s.maxBW

	_ = removed // Will be used for metrics in Phase 6
}

// ackList is the legacy linked list implementation
func (s *sender) ackList(sequenceNumber circular.Number) {
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
