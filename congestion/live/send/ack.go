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
//
// IMPORTANT (Bug fix 2026-01-17): In EventLoop mode, we must NOT fall back
// to the locked path when the ring is full. EventLoop is designed to be
// lock-free - it doesn't check the lock. See: sender_control_ring_overflow_test.go
func (s *sender) ACK(sequenceNumber circular.Number) {
	// Phase 3: Route through control ring for EventLoop processing
	if s.controlRing != nil {
		if s.controlRing.PushACK(sequenceNumber) {
			s.metrics.SendControlRingPushedACK.Add(1)
			return
		}
		// Ring full
		s.metrics.SendControlRingDroppedACK.Add(1)

		// CRITICAL: In EventLoop mode, do NOT fall back to locked path!
		// EventLoop doesn't hold the lock, so this would race with btree iteration.
		// Dropping an ACK is safe - sender will receive the next periodic ACK shortly.
		if s.useEventLoop {
			return
		}
		// Tick mode - safe to use locked fallback (Tick holds the lock)

		// DEBUG ASSERT: Should never reach here in EventLoop mode (fix above should return)
		s.AssertNotEventLoopOnFallback("ACK")
	}

	// Legacy/Tick path with locking
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
		m.CongestionSendPktBuf.Add(^uint64(0))     // Decrement by 1
		m.CongestionSendByteBuf.Add(^(pktLen - 1)) // Subtract pktLen
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
		p, ok := e.Value.(packet.Packet)
		if !ok {
			continue // Skip invalid element
		}
		if p.Header().PacketSequenceNumber.Lt(sequenceNumber) {
			// Remove packet from buffer because it has been successfully transmitted
			removeList = append(removeList, e)
		} else {
			break
		}
	}

	// These packets are not needed anymore (ACK'd)
	for _, e := range removeList {
		p, ok := e.Value.(packet.Packet)
		if !ok {
			s.lossList.Remove(e)
			continue // Skip invalid element
		}

		m.CongestionSendPktBuf.Add(^uint64(0))      // Decrement by 1
		m.CongestionSendByteBuf.Add(^(p.Len() - 1)) // Subtract pktLen
		// PktBuf and ByteBuf are decremented in atomic counters above

		s.lossList.Remove(e)

		// This packet has been ACK'd and we don't need it anymore
		p.Decommission()
	}

	s.pktSndPeriod = (s.avgPayloadSize + 16) * 1000000 / s.maxBW
}
