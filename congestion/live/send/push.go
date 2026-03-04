package send

import (
	"github.com/randomizedcoder/gosrt/circular"
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
		if p.Len() > maxPayloadSize {
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

// PushDirect pushes a packet directly to the lock-free ring.
// Called from connection.Write() to bypass writeQueue channel for lower latency.
// Returns false if ring is full (caller should handle - typically drop).
//
// Thread-safe: Uses atomic sequence number assignment.
// Only valid when UseRing() returns true.
//
// Reference: sender_lockfree_architecture.md Section 7.8
func (s *sender) PushDirect(p packet.Packet) bool {
	if p == nil {
		return false
	}

	// Validate payload size if enabled
	if s.validatePayloadSize {
		if p.Len() > maxPayloadSize {
			s.metrics.SendPayloadSizeErrors.Add(1)
			return false // Don't decommission - caller handles
		}
	}

	// Must have ring enabled
	if !s.useRing || s.packetRing == nil {
		return false
	}

	m := s.metrics

	// Assign sequence number atomically (thread-safe, 31-bit wraparound)
	seqNum := s.assignSequenceNumber()
	p.Header().PacketSequenceNumber = seqNum
	p.Header().PacketPositionFlag = packet.SinglePacket
	p.Header().OrderFlag = false
	p.Header().MessageNumber = 1

	// Initialize TransmitCount (never sent yet)
	p.Header().TransmitCount = 0

	// Set timestamp
	p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

	// Link capacity probing (atomic for concurrent access)
	probe := seqNum.Val() & 0xF
	switch probe {
	case 0:
		s.probeTime.Store(p.Header().PktTsbpdTime)
	case 1:
		p.Header().PktTsbpdTime = s.probeTime.Load()
	}

	// Push to ring (lock-free)
	if !s.packetRing.Push(p) {
		m.SendRingDropped.Add(1)
		return false // Caller handles decommission
	}

	m.SendRingPushed.Add(1)
	return true
}

// UseRing returns whether the lock-free ring is enabled.
// When true, PushDirect() can be used for lower latency writes.
func (s *sender) UseRing() bool {
	return s.useRing && s.packetRing != nil
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
	m.CongestionSendByteBuf.Add(pktLen)
	m.SendRateBytes.Add(pktLen)

	p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

	// Link capacity probing (atomic for concurrent access)
	probe := p.Header().PacketSequenceNumber.Val() & 0xF
	switch probe {
	case 0:
		s.probeTime.Store(p.Header().PktTsbpdTime)
	case 1:
		p.Header().PktTsbpdTime = s.probeTime.Load()
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

// assignSequenceNumber atomically assigns a 31-bit sequence number.
// Uses offset from initialSeq to handle wraparound correctly.
// Formula: (initialSeq + offset) & packet.MAX_SEQUENCENUMBER
//
// This is thread-safe and can be called from multiple goroutines.
// Reference: sender_lockfree_architecture.md Section 7.6
func (s *sender) assignSequenceNumber() circular.Number {
	// Step 1: Atomically increment counter and get previous value
	rawOffset := s.nextSeqOffset.Add(1) - 1

	// Step 2: Add to initial sequence number
	rawSeq := s.initialSeq + rawOffset

	// Step 3: Mask to 31 bits using existing constant
	// This ensures: 0x7FFFFFFF + 1 → 0x00000000 (not 0x80000000)
	seq31 := rawSeq & packet.MAX_SEQUENCENUMBER

	// Track wraparound for metrics
	if rawSeq != seq31 {
		s.metrics.SendSeqWraparound.Add(1)
	}
	s.metrics.SendSeqAssigned.Add(1)

	return circular.New(seq31, packet.MAX_SEQUENCENUMBER)
}

// pushRing pushes to lock-free ring (Phase 2: Ring mode)
// Sequence number assignment happens here using atomic 31-bit assignment.
// Reference: sender_lockfree_architecture.md Section 7.6
//
// IMPORTANT: Sequence number is assigned BEFORE ring push attempt.
// If ring push fails, the sequence number is "lost" (creates a gap).
// This is acceptable because:
// 1. Receiver handles gaps via NAK
// 2. Prevents duplicate sequence numbers (more problematic than gaps)
// 3. Atomic operation cannot be "undone"
func (s *sender) pushRing(p packet.Packet) {
	if p == nil {
		return
	}
	m := s.metrics

	// Assign sequence number atomically (thread-safe, 31-bit wraparound)
	seqNum := s.assignSequenceNumber()
	p.Header().PacketSequenceNumber = seqNum
	p.Header().PacketPositionFlag = packet.SinglePacket
	p.Header().OrderFlag = false
	p.Header().MessageNumber = 1

	// Initialize TransmitCount (never sent yet)
	// This enables first-send detection in EventLoop
	p.Header().TransmitCount = 0

	// Set timestamp
	p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

	// Link capacity probing (atomic for concurrent access)
	probe := seqNum.Val() & 0xF
	switch probe {
	case 0:
		s.probeTime.Store(p.Header().PktTsbpdTime)
	case 1:
		p.Header().PktTsbpdTime = s.probeTime.Load()
	}

	// Push to ring (lock-free)
	if !s.packetRing.Push(p) {
		m.SendRingDropped.Add(1)
		p.Decommission() // Return to pool
		// Note: Sequence number already assigned (atomic). Gap is acceptable.
		// Receiver will NAK, but we can't retransmit (packet not buffered).
		return
	}

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
	m.CongestionSendByteBuf.Add(pktLen)

	// Input bandwidth calculation (Phase 1: Lockless - use atomic)
	s.metrics.SendRateBytes.Add(pktLen)

	p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

	// Every 16th and 17th packet should be sent at the same time in order
	// for the receiver to determine the link capacity. Not really well
	// documented in the specs.
	// PktTsbpdTime is used for the timing of sending the packets. Here we
	// can modify it because it has already been used to set the packet's
	// timestamp.
	// (Atomic for consistency with other paths, though list mode is single-threaded)
	probe := p.Header().PacketSequenceNumber.Val() & 0xF
	switch probe {
	case 0:
		s.probeTime.Store(p.Header().PktTsbpdTime)
	case 1:
		p.Header().PktTsbpdTime = s.probeTime.Load()
	}

	s.packetList.PushBack(p)

	flightSize := uint64(s.packetList.Len())
	m.CongestionSendPktFlightSize.Store(flightSize)
}
