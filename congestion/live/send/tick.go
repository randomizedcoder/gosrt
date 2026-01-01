package send

import (
	"container/list"
	"math"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (s *sender) Tick(now uint64) {
	// Deliver packets whose PktTsbpdTime is ripe
	if s.lockTiming != nil {
		metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
			s.tickDeliverPackets(now)
		})

		metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
			s.tickDropOldPackets(now)
		})

		metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
			s.tickUpdateRateStats(now)
		})
		return
	}

	// Fallback without metrics
	s.lock.Lock()
	s.tickDeliverPackets(now)
	s.lock.Unlock()

	s.lock.Lock()
	s.tickDropOldPackets(now)
	s.lock.Unlock()

	s.lock.Lock()
	s.tickUpdateRateStats(now)
	s.lock.Unlock()
}

func (s *sender) tickDeliverPackets(now uint64) {
	// Check metrics once at the beginning of the function
	m := s.metrics

	removeList := make([]*list.Element, 0, s.packetList.Len())
	for e := s.packetList.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		if p.Header().PktTsbpdTime <= now {
			pktLen := p.Len()

			m.CongestionSendPkt.Add(1)
			m.CongestionSendPktUnique.Add(1)
			m.CongestionSendByte.Add(uint64(pktLen))
			m.CongestionSendByteUnique.Add(uint64(pktLen))
			m.CongestionSendUsSndDuration.Add(uint64(s.pktSndPeriod))

			//  5.1.2. SRT's Default LiveCC Algorithm
			s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)

			s.metrics.SendRateBytesSent.Add(pktLen) // Phase 1: Lockless

			s.deliver(p)
			removeList = append(removeList, e)
		} else {
			break
		}
	}

	for _, e := range removeList {
		s.lossList.PushBack(e.Value)
		s.packetList.Remove(e)
	}
}

func (s *sender) tickDropOldPackets(now uint64) {
	// Check metrics once at the beginning of the function
	m := s.metrics

	removeList := make([]*list.Element, 0, s.lossList.Len())
	for e := s.lossList.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)

		if p.Header().PktTsbpdTime+s.dropThreshold <= now {
			// Dropped packet because too old (local drop, not a loss)
			// Note: PktDrop = local drops (too old, errors, etc.)
			// Note: PktLoss = packets reported as lost via NAK (incremented in nakLocked when NAK received)
			pktLen := p.Len()
			metrics.IncrementSendDataDrop(m, metrics.DropReasonTooOldSend, uint64(pktLen))

			removeList = append(removeList, e)
		}
	}

	// These packets are not needed anymore (too late)
	for _, e := range removeList {
		p := e.Value.(packet.Packet)

		m.CongestionSendPktBuf.Add(^uint64(0))                    // Decrement by 1
		m.CongestionSendByteBuf.Add(^uint64(uint64(p.Len()) - 1)) // Subtract pktLen
		// PktBuf and ByteBuf are decremented in atomic counters above

		s.lossList.Remove(e)

		// This packet has been ACK'd and we don't need it anymore
		p.Decommission()
	}
}

func (s *sender) tickUpdateRateStats(now uint64) {
	// Phase 1: Lockless - All rate calculations now use atomic ConnectionMetrics
	m := s.metrics

	lastUs := m.SendRateLastUs.Load()
	periodUs := m.SendRatePeriodUs.Load()
	tdiff := now - lastUs

	if tdiff > periodUs {
		// Load current counters
		bytes := m.SendRateBytes.Load()
		bytesSent := m.SendRateBytesSent.Load()
		bytesRetrans := m.SendRateBytesRetrans.Load()

		// Calculate rates
		seconds := float64(tdiff) / 1_000_000
		estimatedInputBW := float64(bytes) / seconds
		estimatedSentBW := float64(bytesSent) / seconds

		var pktRetransRate float64
		if bytesSent != 0 {
			pktRetransRate = float64(bytesRetrans) / float64(bytesSent) * 100
		}

		// Store computed rates as float64 bits (lock-free)
		m.SendRateEstInputBW.Store(math.Float64bits(estimatedInputBW))
		m.SendRateEstSentBW.Store(math.Float64bits(estimatedSentBW))
		m.SendRatePktRetransRate.Store(math.Float64bits(pktRetransRate))

		// Reset counters for next period
		m.SendRateBytes.Store(0)
		m.SendRateBytesSent.Store(0)
		m.SendRateBytesRetrans.Store(0)

		// Update last calculation time
		m.SendRateLastUs.Store(now)
	}
}
