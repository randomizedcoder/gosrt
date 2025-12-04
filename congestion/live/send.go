package live

import (
	"container/list"
	"sync"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/congestion"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
)

// SendConfig is the configuration for the liveSend congestion control
type SendConfig struct {
	InitialSequenceNumber circular.Number
	DropThreshold         uint64
	MaxBW                 int64
	InputBW               int64
	MinInputBW            int64
	OverheadBW            int64
	OnDeliver             func(p packet.Packet)
	LockTimingMetrics     *metrics.LockTimingMetrics // Optional lock timing metrics for performance monitoring
	ConnectionMetrics     *metrics.ConnectionMetrics // For atomic statistics updates
}

// sender implements the Sender interface
type sender struct {
	nextSequenceNumber circular.Number
	dropThreshold      uint64

	packetList *list.List
	lossList   *list.List
	lock       sync.RWMutex
	lockTiming *metrics.LockTimingMetrics // Optional lock timing metrics
	metrics    *metrics.ConnectionMetrics // For atomic statistics updates

	avgPayloadSize float64 // bytes
	pktSndPeriod   float64 // microseconds
	maxBW          float64 // bytes/s
	inputBW        float64 // bytes/s
	overheadBW     float64 // percent

	probeTime uint64

	rate struct {
		period uint64 // microseconds
		last   uint64

		bytes        uint64
		bytesSent    uint64
		bytesRetrans uint64

		estimatedInputBW float64 // bytes/s
		estimatedSentBW  float64 // bytes/s

		pktLossRate float64
	}

	deliver func(p packet.Packet)
}

// NewSender takes a SendConfig and returns a new Sender
func NewSender(config SendConfig) congestion.Sender {
	s := &sender{
		nextSequenceNumber: config.InitialSequenceNumber,
		dropThreshold:      config.DropThreshold,
		packetList:         list.New(),
		lossList:           list.New(),
		lockTiming:         config.LockTimingMetrics,
		metrics:            config.ConnectionMetrics,

		avgPayloadSize: packet.MAX_PAYLOAD_SIZE, //  5.1.2. SRT's Default LiveCC Algorithm
		maxBW:          float64(config.MaxBW),
		inputBW:        float64(config.InputBW),
		overheadBW:     float64(config.OverheadBW),

		deliver: config.OnDeliver,
	}

	if s.deliver == nil {
		s.deliver = func(p packet.Packet) {}
	}

	s.maxBW = 128 * 1024 * 1024 // 1 Gbit/s
	s.pktSndPeriod = (s.avgPayloadSize + 16) * 1_000_000 / s.maxBW

	s.rate.period = uint64(time.Second.Microseconds())
	s.rate.last = 0

	return s
}

func (s *sender) Stats() congestion.SendStats {
	// Read lock only for rate calculations
	s.lock.RLock()
	usPktSndPeriod := s.pktSndPeriod
	bytePayload := uint64(s.avgPayloadSize)
	msBuf := uint64(0)
	max := s.lossList.Back()
	min := s.lossList.Front()
	if max != nil && min != nil {
		msBuf = (max.Value.(packet.Packet).Header().PktTsbpdTime - min.Value.(packet.Packet).Header().PktTsbpdTime) / 1_000
	}
	mbpsInputBW := s.rate.estimatedInputBW * 8 / 1024 / 1024
	mbpsSentBW := s.rate.estimatedSentBW * 8 / 1024 / 1024
	pktLossRate := s.rate.pktLossRate
	s.lock.RUnlock()

	// Metrics are always available (initialized in connection.go before NewSender)
	m := s.metrics

	// Update atomic counters for instantaneous/calculated values
	m.CongestionSendUsPktSndPeriod.Store(uint64(usPktSndPeriod))
	m.CongestionSendBytePayload.Store(bytePayload)
	m.CongestionSendMsBuf.Store(msBuf)
	m.CongestionSendMbpsInputBandwidth.Store(uint64(mbpsInputBW * 1000))
	m.CongestionSendMbpsSentBandwidth.Store(uint64(mbpsSentBW * 1000))
	m.CongestionSendPktLossRate.Store(uint64(pktLossRate * 100))

	// Build return struct from atomic counters (lock-free reads)
	// PktLoss = packets reported as lost via NAK (incremented in nakLocked when NAK received)
	// PktDrop = packets dropped locally (too old, errors, etc.) - separate from loss
	return congestion.SendStats{
		Pkt:                         m.CongestionSendPkt.Load(),
		Byte:                        m.CongestionSendByte.Load(),
		PktUnique:                   m.CongestionSendPktUnique.Load(),
		ByteUnique:                  m.CongestionSendByteUnique.Load(),
		PktLoss:                     m.CongestionSendPktLoss.Load(),
		ByteLoss:                    m.CongestionSendByteLoss.Load(),
		PktRetrans:                  m.CongestionSendPktRetrans.Load(),
		ByteRetrans:                 m.CongestionSendByteRetrans.Load(),
		UsSndDuration:               m.CongestionSendUsSndDuration.Load(),
		PktDrop:                     m.CongestionSendDataDropTooOld.Load(), // Only congestion control drops
		ByteDrop:                    m.CongestionSendByteDrop.Load(),       // ByteDrop is maintained by helper functions
		PktBuf:                      m.CongestionSendPktBuf.Load(),
		ByteBuf:                     m.CongestionSendByteBuf.Load(),
		MsBuf:                       msBuf,
		PktFlightSize:               m.CongestionSendPktFlightSize.Load(),
		UsPktSndPeriod:              usPktSndPeriod,
		BytePayload:                 bytePayload,
		MbpsEstimatedInputBandwidth: mbpsInputBW,
		MbpsEstimatedSentBandwidth:  mbpsSentBW,
		PktLossRate:                 pktLossRate,
	}
}

func (s *sender) Flush() {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.packetList = s.packetList.Init()
	s.lossList = s.lossList.Init()
}

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

	// Input bandwidth calculation
	s.rate.bytes += pktLen

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

			s.rate.bytesSent += pktLen

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
			metrics.IncrementSendDataDrop(m, "too_old", uint64(pktLen))

			removeList = append(removeList, e)
		}
	}

	// These packets are not needed anymore (too late)
	for _, e := range removeList {
		p := e.Value.(packet.Packet)

		metrics.DecrementUint64(&m.CongestionSendPktBuf)
		metrics.SubtractUint64(&m.CongestionSendByteBuf, uint64(p.Len()))
		// PktBuf and ByteBuf are decremented in atomic counters above

		s.lossList.Remove(e)

		// This packet has been ACK'd and we don't need it anymore
		p.Decommission()
	}
}

func (s *sender) tickUpdateRateStats(now uint64) {
	tdiff := now - s.rate.last

	if tdiff > s.rate.period {
		s.rate.estimatedInputBW = float64(s.rate.bytes) / (float64(tdiff) / 1000 / 1000)
		s.rate.estimatedSentBW = float64(s.rate.bytesSent) / (float64(tdiff) / 1000 / 1000)
		if s.rate.bytesSent != 0 {
			s.rate.pktLossRate = float64(s.rate.bytesRetrans) / float64(s.rate.bytesSent) * 100
		} else {
			s.rate.pktLossRate = 0
		}

		s.rate.bytes = 0
		s.rate.bytesSent = 0
		s.rate.bytesRetrans = 0

		s.rate.last = now
	}
}

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

		metrics.DecrementUint64(&m.CongestionSendPktBuf)
		metrics.SubtractUint64(&m.CongestionSendByteBuf, uint64(p.Len()))
		// PktBuf and ByteBuf are decremented in atomic counters above

		s.lossList.Remove(e)

		// This packet has been ACK'd and we don't need it anymore
		p.Decommission()
	}

	s.pktSndPeriod = (s.avgPayloadSize + 16) * 1000000 / s.maxBW
}

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

func (s *sender) nakLocked(sequenceNumbers []circular.Number) uint64 {
	// Check metrics once at the beginning of the function
	m := s.metrics

	// First, count all packets reported as lost in the NAK (all reported losses)
	// This represents packets that the receiver detected as missing
	totalLossCount := uint64(0)
	totalLossBytes := uint64(0)
	for i := 0; i < len(sequenceNumbers); i += 2 {
		start := sequenceNumbers[i]
		end := sequenceNumbers[i+1]
		lossCount := uint64(end.Distance(start)) + 1
		totalLossCount += lossCount
		// Estimate bytes based on average payload size
		totalLossBytes += lossCount * uint64(s.avgPayloadSize)
	}

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

				s.rate.bytesSent += pktLen
				s.rate.bytesRetrans += pktLen

				p.Header().RetransmittedPacketFlag = true
				s.deliver(p)

				retransCount++
			}
		}
	}

	return retransCount
}

func (s *sender) SetDropThreshold(threshold uint64) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.dropThreshold = threshold
}
