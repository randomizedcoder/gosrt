package send

import (
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (s *sender) Push(p packet.Packet) {
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

func (s *sender) pushLocked(p packet.Packet) {
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
