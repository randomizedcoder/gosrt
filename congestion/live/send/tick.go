package send

import (
	"container/list"
	"math"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (s *sender) Tick(now uint64) {
	// Step 7.5.2: Runtime Verification (Debug Mode)
	// Track that we're in Tick context - panics if EventLoop is active.
	// No-op in release builds.
	s.EnterTick()
	defer s.ExitTick()

	// Track Tick invocations for burst detection comparison (Packets/Iteration ratio)
	s.metrics.SendTickRuns.Add(1)

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
	// Phase 2: Drain data ring to btree first (if ring mode enabled)
	if s.useRing {
		s.drainRingToBtree()
	}

	// Phase 3: Process control ring (if control ring mode enabled)
	if s.useControlRing {
		s.processControlRing()
	}

	if s.useBtree {
		s.tickDeliverPacketsBtree(now)
	} else {
		s.tickDeliverPacketsList(now)
	}
}

// drainRingToBtree drains packets from ring to btree.
// Called by Tick() before processing (Phase 2: Ring mode).
// Reference: lockless_sender_implementation_plan.md Step 2.6
func (s *sender) drainRingToBtree() {
	if s.packetRing == nil {
		return
	}

	m := s.metrics
	drained := 0

	// Drain all available packets
	for {
		p, ok := s.packetRing.TryPop()
		if !ok {
			break
		}

		pktLen := p.Len()
		m.CongestionSendPktBuf.Add(1)
		m.CongestionSendByteBuf.Add(uint64(pktLen))
		m.SendRateBytes.Add(pktLen)

		// Insert into btree
		inserted, old := s.packetBtree.Insert(p)
		if !inserted && old != nil {
			m.SendBtreeDuplicates.Add(1)
			old.Decommission()
		}

		m.SendBtreeInserted.Add(1)
		m.SendRingDrained.Add(1)
		drained++
	}

	if drained > 0 {
		m.CongestionSendPktFlightSize.Store(uint64(s.packetBtree.Len()))
	}
}

// processControlRing drains and processes control packets from the control ring.
// Called by Tick() when control ring mode is enabled (Phase 3: Control Ring mode).
// Reference: lockless_sender_implementation_plan.md Step 3.4
func (s *sender) processControlRing() {
	if s.controlRing == nil {
		return
	}

	m := s.metrics

	// Process all available control packets
	for {
		cp, ok := s.controlRing.TryPop()
		if !ok {
			break
		}

		m.SendControlRingDrained.Add(1)

		switch cp.Type {
		case ControlTypeACK:
			// Process ACK - remove packets before this sequence
			s.ackBtree(circular.New(cp.ACKSequence, packet.MAX_SEQUENCENUMBER))
			m.SendControlRingProcessed.Add(1)

		case ControlTypeNAK:
			// Convert inline array to slice for processing
			seqs := make([]circular.Number, cp.NAKCount)
			for i := 0; i < cp.NAKCount; i++ {
				seqs[i] = circular.New(cp.NAKSequences[i], packet.MAX_SEQUENCENUMBER)
			}
			// Process NAK - retransmit requested packets
			retransCount := s.nakBtree(seqs)
			m.SendControlRingProcessed.Add(1)
			// Track actual retransmissions from NAK processing
			// (NAK() returns 0 when routed to ring, so we update here)
			if retransCount > 0 {
				m.PktRetransFromNAK.Add(retransCount)
			}
		}
	}
}

// tickDeliverPacketsBtree delivers packets using btree iteration (Phase 1: Btree mode)
// In btree mode, packets are NOT moved to lossList - they stay in packetBtree until ACK'd.
// This simplifies the design: packetBtree = all packets pending ACK.
// Reference: lockless_sender_implementation_plan.md Step 1.12
func (s *sender) tickDeliverPacketsBtree(now uint64) {
	m := s.metrics
	delivered := 0

	// Get current delivery start point
	startSeq := s.deliveryStartPoint.Load()

	// Iterate from delivery point and deliver ready packets
	s.packetBtree.IterateFrom(uint32(startSeq), func(p packet.Packet) bool {
		if p.Header().PktTsbpdTime > now {
			return false // Stop - this and following packets not ready
		}

		pktLen := p.Len()
		m.CongestionSendPkt.Add(1)
		m.CongestionSendPktUnique.Add(1)
		m.CongestionSendByte.Add(uint64(pktLen))
		m.CongestionSendByteUnique.Add(uint64(pktLen))
		m.CongestionSendUsSndDuration.Add(uint64(s.pktSndPeriod))

		s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
		m.SendRateBytesSent.Add(pktLen)

		s.deliver(p)
		delivered++

		// Update delivery point to next sequence
		nextSeq := p.Header().PacketSequenceNumber.Val()
		s.deliveryStartPoint.Store(uint64(circular.SeqAdd(nextSeq, 1)))

		return true // Continue iteration
	})

	// Track delivered packets for burst detection comparison (Packets/Iteration ratio)
	m.SendTickDeliveredPackets.Add(uint64(delivered))
}

// tickDeliverPacketsList is the legacy linked list implementation
func (s *sender) tickDeliverPacketsList(now uint64) {
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

	// Track delivered packets for burst detection comparison (Packets/Iteration ratio)
	m.SendTickDeliveredPackets.Add(uint64(len(removeList)))
}

func (s *sender) tickDropOldPackets(now uint64) {
	if s.useBtree {
		s.tickDropOldPacketsBtree(now)
	} else {
		s.tickDropOldPacketsList(now)
	}
}

// tickDropOldPacketsBtree removes packets that exceeded drop threshold (Phase 1: Btree mode)
func (s *sender) tickDropOldPacketsBtree(now uint64) {
	m := s.metrics

	// Calculate drop threshold with underflow protection
	// See drop_threshold.go and drop_threshold_test.go for details on the bug this fixes
	threshold, shouldDrop := calculateDropThreshold(now, s.dropThreshold)
	if !shouldDrop {
		return // Too early - no packets can be old enough to drop yet
	}

	// Iterate from beginning and drop old packets
	for {
		p := s.packetBtree.Min()
		if p == nil {
			break
		}

		if !shouldDropPacket(p.Header().PktTsbpdTime, threshold) {
			break // Remaining packets are not old enough
		}

		// Remove and drop
		s.packetBtree.Delete(p.Header().PacketSequenceNumber.Val())

		pktLen := p.Len()
		metrics.IncrementSendDataDrop(m, metrics.DropReasonTooOldSend, uint64(pktLen))
		m.CongestionSendPktBuf.Add(^uint64(0))
		m.CongestionSendByteBuf.Add(^uint64(uint64(pktLen) - 1))

		p.Decommission()
	}
}

// tickDropOldPacketsList is the legacy linked list implementation
func (s *sender) tickDropOldPacketsList(now uint64) {
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
