package send

import (
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// maxPayloadSize is the maximum SRT payload size (7 MPEG-TS packets).
// This is defined locally to avoid import cycle with root srt package.
// Must match srt.MaxPayloadSize (1316 bytes).
const maxPayloadSize = 1316

// Push adds a packet to the sender's buffer.
// Dispatches to ring (lock-free) or locked path based on config.
//
// For zero-copy operation (Phase 5):
//   - Application acquires buffer via srt.GetBuffer()
//   - Fills buffer with payload data (up to srt.MaxPayloadSize)
//   - Creates packet with buffer
//   - Calls Push() - buffer is now owned by sender
//   - Buffer is returned to pool when ACK'd or dropped
//
// Reference: lockless_sender_design.md Section 6.2
func (s *sender) Push(p packet.Packet) {
	// Validate payload size if validation is enabled
	// Uses local maxPayloadSize constant to avoid import cycle
	if s.validatePayloadSize && p != nil {
		if p.Len() < 0 || p.Len() > maxPayloadSize {
			s.metrics.SendPayloadSizeErrors.Add(1)
			p.Decommission() // Return buffer to pool
			return
		}
	}

	// Phase 2: Lock-free ring path
	if s.useRing {
		s.pushRing(p)
		return
	}

	// Legacy path with locking
	if s.lockTiming != nil {
		metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
			s.pushLocked(p)
		})
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	s.pushLocked(p)
}

// pushLocked is called with lock held. Dispatches to btree or list.
func (s *sender) pushLocked(p packet.Packet) {
	if s.useBtree {
		s.pushBtree(p)
	} else {
		s.pushList(p)
	}
}

// pushBtree inserts packet into btree (Phase 1: Btree mode)
// Reference: lockless_sender_implementation_plan.md Step 1.9
func (s *sender) pushBtree(p packet.Packet) {
	if p == nil {
		return
	}

	m := s.metrics

	// Assign sequence number
	p.Header().PacketSequenceNumber = s.nextSequenceNumber
	p.Header().PacketPositionFlag = packet.SinglePacket
	p.Header().OrderFlag = false
	p.Header().MessageNumber = 1
	s.nextSequenceNumber = s.nextSequenceNumber.Inc()

	pktLen := p.Len()
	m.CongestionSendPktBuf.Add(1)
	m.CongestionSendByteBuf.Add(uint64(pktLen))
	m.SendRateBytes.Add(pktLen)

	p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

	// Link capacity probing (same as list mode)
	probe := p.Header().PacketSequenceNumber.Val() & 0xF
	switch probe {
	case 0:
		s.probeTime = p.Header().PktTsbpdTime
	case 1:
		p.Header().PktTsbpdTime = s.probeTime
	}

	// Insert into btree (O(log n))
	// ReplaceOrInsert handles duplicates with single traversal
	inserted, old := s.packetBtree.Insert(p)
	if !inserted && old != nil {
		// Duplicate detected (should not happen in normal operation)
		// TODO: Add SendBtreeDuplicates metric in Phase 6
		old.Decommission() // Return old packet to pool
	}
	_ = inserted // Silence unused variable warning

	flightSize := uint64(s.packetBtree.Len())
	m.CongestionSendPktFlightSize.Store(flightSize)
}

// pushRing pushes to lock-free ring (Phase 2: Ring mode)
// Sequence number assignment happens here for deterministic ordering.
// Reference: lockless_sender_implementation_plan.md Step 2.5
//
// BUG FIX (January 10, 2026): Sequence number was being incremented BEFORE
// the ring push, causing sequence gaps when Push() failed due to ring full.
// Now we only increment AFTER successful push to prevent gaps.
func (s *sender) pushRing(p packet.Packet) {
	if p == nil {
		return
	}
	m := s.metrics

	// Assign sequence number BEFORE pushing to ring
	// This ensures sequence numbers are assigned in Push() order
	// Note: nextSequenceNumber access is NOT thread-safe, but Push() is
	// typically called from a single goroutine (application writer)
	currentSeq := s.nextSequenceNumber
	p.Header().PacketSequenceNumber = currentSeq
	p.Header().PacketPositionFlag = packet.SinglePacket
	p.Header().OrderFlag = false
	p.Header().MessageNumber = 1
	// NOTE: Do NOT increment nextSequenceNumber here - wait for successful push

	// Set timestamp
	p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

	// Link capacity probing
	probe := currentSeq.Val() & 0xF
	switch probe {
	case 0:
		s.probeTime = p.Header().PktTsbpdTime
	case 1:
		p.Header().PktTsbpdTime = s.probeTime
	}

	// Push to ring (lock-free)
	if !s.packetRing.Push(p) {
		m.SendRingDropped.Add(1)
		p.Decommission() // Return to pool
		// CRITICAL: Do NOT increment sequence - next packet will reuse this sequence
		// This prevents sequence gaps when ring is full
		return
	}

	// Only increment sequence AFTER successful push
	// This ensures no sequence gaps even when packets are dropped due to ring full
	s.nextSequenceNumber = s.nextSequenceNumber.Inc()
	m.SendRingPushed.Add(1)
}

// pushList is the legacy linked list implementation
func (s *sender) pushList(p packet.Packet) {
	if p == nil {
		return
	}

	// Check metrics once at the beginning of the function
	m := s.metrics

	// Give to the packet a sequence number
	p.Header().PacketSequenceNumber = s.nextSequenceNumber
	p.Header().PacketPositionFlag = packet.SinglePacket
	p.Header().OrderFlag = false
	p.Header().MessageNumber = 1

	s.nextSequenceNumber = s.nextSequenceNumber.Inc()

	pktLen := p.Len()

	m.CongestionSendPktBuf.Add(1)
	m.CongestionSendByteBuf.Add(uint64(pktLen))

	// Input bandwidth calculation (Phase 1: Lockless - use atomic)
	s.metrics.SendRateBytes.Add(pktLen)

	p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

	// Every 16th and 17th packet should be sent at the same time in order
	// for the receiver to determine the link capacity. Not really well
	// documented in the specs.
	// PktTsbpdTime is used for the timing of sending the packets. Here we
	// can modify it because it has already been used to set the packet's
	// timestamp.
	probe := p.Header().PacketSequenceNumber.Val() & 0xF
	switch probe {
	case 0:
		s.probeTime = p.Header().PktTsbpdTime
	case 1:
		p.Header().PktTsbpdTime = s.probeTime
	}

	s.packetList.PushBack(p)

	flightSize := uint64(s.packetList.Len())
	m.CongestionSendPktFlightSize.Store(flightSize)
}
