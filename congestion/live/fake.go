package live

import (
	"math"
	"sync"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/congestion"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
)

type fakeLiveReceive struct {
	maxSeenSequenceNumber       circular.Number
	lastACKSequenceNumber       circular.Number
	lastDeliveredSequenceNumber circular.Number

	// nPackets removed - now using metrics.RecvLightACKCounter (Phase 1: Lockless)
	// rate struct removed - now using metrics.ConnectionMetrics atomics (Phase 1: Lockless)

	periodicACKInterval uint64 // config
	periodicNAKInterval uint64 // config

	lastPeriodicACK uint64

	avgPayloadSize float64 // bytes

	metrics *metrics.ConnectionMetrics // Phase 1: Lockless

	sendACK func(seq circular.Number, light bool)
	sendNAK func(list []circular.Number)
	deliver func(p packet.Packet)

	lock sync.RWMutex
}

func NewFakeLiveReceive(config ReceiveConfig) congestion.Receiver {
	// Phase 1: Lockless - Create metrics for rate tracking (even for fake receiver)
	m := metrics.NewConnectionMetrics()

	r := &fakeLiveReceive{
		maxSeenSequenceNumber:       config.InitialSequenceNumber.Dec(),
		lastACKSequenceNumber:       config.InitialSequenceNumber.Dec(),
		lastDeliveredSequenceNumber: config.InitialSequenceNumber.Dec(),

		periodicACKInterval: config.PeriodicACKInterval,
		periodicNAKInterval: config.PeriodicNAKInterval,

		avgPayloadSize: 1456, //  5.1.2. SRT's Default LiveCC Algorithm

		metrics: m, // Phase 1: Lockless

		sendACK: config.OnSendACK,
		sendNAK: config.OnSendNAK,
		deliver: config.OnDeliver,
	}

	if r.sendACK == nil {
		r.sendACK = func(seq circular.Number, light bool) {}
	}

	if r.sendNAK == nil {
		r.sendNAK = func(list []circular.Number) {}
	}

	if r.deliver == nil {
		r.deliver = func(p packet.Packet) {}
	}

	// Phase 1: Lockless - Initialize rate calculation period in ConnectionMetrics
	m.RecvRatePeriodUs.Store(uint64(time.Second.Microseconds()))
	m.RecvRateLastUs.Store(uint64(time.Now().UnixMicro()))

	return r
}

func (r *fakeLiveReceive) Stats() congestion.ReceiveStats { return congestion.ReceiveStats{} }
func (r *fakeLiveReceive) PacketRate() (pps, bps, capacity float64) {
	// Phase 1: Lockless - Calculate rate and update atomics
	m := r.metrics
	now := uint64(time.Now().UnixMicro())
	lastUs := m.RecvRateLastUs.Load()
	periodUs := m.RecvRatePeriodUs.Load()
	tdiff := now - lastUs

	if tdiff < periodUs {
		// Return cached rates
		pps = m.GetRecvRatePacketsPerSec()
		bps = m.GetRecvRateBytesPerSec()
		return
	}

	// Calculate new rates
	packets := m.RecvRatePackets.Load()
	bytes := m.RecvRateBytes.Load()
	seconds := float64(tdiff) / 1_000_000

	pps = float64(packets) / seconds
	bps = float64(bytes) / seconds

	// Store computed rates
	m.RecvRatePacketsPerSec.Store(math.Float64bits(pps))
	m.RecvRateBytesPerSec.Store(math.Float64bits(bps))

	// Reset counters
	m.RecvRatePackets.Store(0)
	m.RecvRateBytes.Store(0)
	m.RecvRateLastUs.Store(now)

	return
}

func (r *fakeLiveReceive) Flush() {}

func (r *fakeLiveReceive) Push(pkt packet.Packet) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if pkt == nil {
		return
	}

	// Phase 1: Lockless - Use atomic counters
	m := r.metrics
	m.RecvLightACKCounter.Add(1) // Replaces r.nPackets++

	pktLen := pkt.Len()

	m.RecvRatePackets.Add(1)    // Replaces r.rate.packets++
	m.RecvRateBytes.Add(pktLen) // Replaces r.rate.bytes += pktLen

	//  5.1.2. SRT's Default LiveCC Algorithm
	r.avgPayloadSize = 0.875*r.avgPayloadSize + 0.125*float64(pktLen)

	if pkt.Header().PacketSequenceNumber.Lte(r.lastDeliveredSequenceNumber) {
		// Too old, because up until r.lastDeliveredSequenceNumber, we already delivered
		return
	}

	if pkt.Header().PacketSequenceNumber.Lt(r.lastACKSequenceNumber) {
		// Already acknowledged, ignoring
		return
	}

	if pkt.Header().PacketSequenceNumber.Lte(r.maxSeenSequenceNumber) {
		return
	}

	r.maxSeenSequenceNumber = pkt.Header().PacketSequenceNumber
}

func (r *fakeLiveReceive) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
	r.lock.RLock()
	defer r.lock.RUnlock()

	// Phase 1: Lockless - Use atomic RecvLightACKCounter instead of r.nPackets
	lightACKCount := r.metrics.RecvLightACKCounter.Load()

	// 4.8.1. Packet Acknowledgement (ACKs, ACKACKs)
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		if lightACKCount >= 64 {
			lite = true // Send light ACK
		} else {
			return
		}
	}

	ok = true
	sequenceNumber = r.maxSeenSequenceNumber.Inc()

	r.lastACKSequenceNumber = r.maxSeenSequenceNumber

	r.lastPeriodicACK = now
	r.metrics.RecvLightACKCounter.Store(0) // Phase 1: Lockless - Reset atomic counter

	return
}

func (r *fakeLiveReceive) Tick(now uint64) {
	if ok, sequenceNumber, lite := r.periodicACK(now); ok {
		r.sendACK(sequenceNumber, lite)
	}

	// Deliver packets whose PktTsbpdTime is ripe
	r.lock.Lock()
	defer r.lock.Unlock()

	r.lastDeliveredSequenceNumber = r.lastACKSequenceNumber
}

func (r *fakeLiveReceive) SetNAKInterval(nakInterval uint64) {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.periodicNAKInterval = nakInterval
}
