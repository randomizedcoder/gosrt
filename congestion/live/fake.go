package live

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/congestion/live/receive"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
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

	// Light ACK tracking (Phase 5: ACK Optimization)
	lightACKDifference uint32 // Threshold for sending Light ACK (default: 64)
	lastLightACKSeq    uint32 // Sequence when last Light ACK was sent

	sendACK func(seq circular.Number, light bool)
	sendNAK func(list []circular.Number)
	deliver func(p packet.Packet)

	lock sync.RWMutex
}

func NewFakeLiveReceive(recvConfig receive.Config, wg *sync.WaitGroup) congestion.Receiver {
	defer wg.Done()
	// Phase 1: Lockless - Create metrics for rate tracking (even for fake receiver)
	m := metrics.NewConnectionMetrics()

	// Initialize Light ACK tracking (Phase 5: ACK Optimization)
	lightACKDiff := recvConfig.LightACKDifference
	if lightACKDiff == 0 {
		lightACKDiff = 64 // RFC default
	}

	// lastACKSequenceNumber is ISN.Dec(), so use that for lastLightACKSeq initialization
	lastACKSeq := recvConfig.InitialSequenceNumber.Dec()

	r := &fakeLiveReceive{
		maxSeenSequenceNumber:       recvConfig.InitialSequenceNumber.Dec(),
		lastACKSequenceNumber:       lastACKSeq,
		lastDeliveredSequenceNumber: recvConfig.InitialSequenceNumber.Dec(),

		periodicACKInterval: recvConfig.PeriodicACKInterval,
		periodicNAKInterval: recvConfig.PeriodicNAKInterval,

		avgPayloadSize: 1456, //  5.1.2. SRT's Default LiveCC Algorithm

		metrics: m, // Phase 1: Lockless

		// Light ACK tracking (Phase 5: ACK Optimization)
		// Initialize lastLightACKSeq from lastACKSequenceNumber (NOT ISN)
		lightACKDifference: lightACKDiff,
		lastLightACKSeq:    lastACKSeq.Val(),

		sendACK: recvConfig.OnSendACK,
		sendNAK: recvConfig.OnSendNAK,
		deliver: recvConfig.OnDeliver,
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
	m.RecvLightACKCounter.Add(1) // Used for Light ACK triggering until Phase 5 complete

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

	// Phase 5: ACK Optimization - Use difference-based Light ACK triggering
	// 4.8.1. Packet Acknowledgement (ACKs, ACKACKs)
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		// Check if we've advanced enough for a Light ACK
		// Use maxSeenSequenceNumber (updated on Push) for early check
		currentSeq := r.maxSeenSequenceNumber.Val()
		diff := circular.SeqSub(currentSeq, r.lastLightACKSeq)
		if diff >= r.lightACKDifference {
			lite = true // Send light ACK
		} else {
			return
		}
	}

	ok = true
	sequenceNumber = r.maxSeenSequenceNumber.Inc()

	r.lastACKSequenceNumber = r.maxSeenSequenceNumber

	r.lastPeriodicACK = now
	// Phase 5: ACK Optimization - Track last Light ACK sequence for difference check
	// Update on BOTH full ACK and lite ACK
	r.lastLightACKSeq = r.maxSeenSequenceNumber.Val()

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

// SetRTTProvider is a no-op for the fake receiver (Phase 6: RTO Suppression).
// The fake receiver doesn't implement NAK suppression.
func (r *fakeLiveReceive) SetRTTProvider(rtt congestion.RTTProvider) {
	// No-op: fake receiver doesn't use RTO-based suppression
}

// EventLoop is a no-op for the fake receiver (Phase 4: Lockless Design).
// The fake receiver doesn't use the event loop - it uses timer-driven Tick().
func (r *fakeLiveReceive) EventLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	// No-op: fake receiver uses Tick() only
}

// UseEventLoop returns false for the fake receiver.
// The fake receiver always uses timer-driven Tick().
func (r *fakeLiveReceive) UseEventLoop() bool {
	return false
}

// SetProcessConnectionControlPackets is a no-op for the fake receiver.
// The fake receiver doesn't process connection-level control packets.
func (r *fakeLiveReceive) SetProcessConnectionControlPackets(fn func() int) {
	// No-op: fake receiver doesn't use control packet callback
}
