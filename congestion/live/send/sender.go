// Package send implements the sender-side congestion control for SRT live mode.
package send

import (
	"container/list"
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
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

	// NAK order configuration - when true, retransmit in NAK packet order (receiver-controlled priority)
	HonorNakOrder bool
}

// sender implements the Sender interface
type sender struct {
	nextSequenceNumber circular.Number
	lastACKedSequence  circular.Number // Highest sequence number that has been ACK'd
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

	// rate struct removed - now using metrics.ConnectionMetrics atomics (Phase 1: Lockless)

	deliver func(p packet.Packet)

	// NAK order configuration
	honorNakOrder bool // When true, retransmit in NAK packet order (receiver-controlled priority)
}

// NewSender takes a SendConfig and returns a new Sender
func NewSender(sendConfig SendConfig) congestion.Sender {
	s := &sender{
		nextSequenceNumber: sendConfig.InitialSequenceNumber,
		dropThreshold:      sendConfig.DropThreshold,
		packetList:         list.New(),
		lossList:           list.New(),
		lockTiming:         sendConfig.LockTimingMetrics,
		metrics:            sendConfig.ConnectionMetrics,

		avgPayloadSize: packet.MAX_PAYLOAD_SIZE, //  5.1.2. SRT's Default LiveCC Algorithm
		maxBW:          float64(sendConfig.MaxBW),
		inputBW:        float64(sendConfig.InputBW),
		overheadBW:     float64(sendConfig.OverheadBW),

		deliver: sendConfig.OnDeliver,

		honorNakOrder: sendConfig.HonorNakOrder,
	}

	if s.deliver == nil {
		s.deliver = func(p packet.Packet) {}
	}

	s.maxBW = 128 * 1024 * 1024 // 1 Gbit/s
	s.pktSndPeriod = (s.avgPayloadSize + 16) * 1_000_000 / s.maxBW

	// Initialize rate calculation period in ConnectionMetrics (Phase 1: Lockless)
	// Default period is 1 second (1,000,000 microseconds)
	if s.metrics != nil {
		s.metrics.SendRatePeriodUs.Store(uint64(time.Second.Microseconds()))
		s.metrics.SendRateLastUs.Store(0)
	}

	return s
}

func (s *sender) Flush() {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.packetList = s.packetList.Init()
	s.lossList = s.lossList.Init()
}

func (s *sender) SetDropThreshold(threshold uint64) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.dropThreshold = threshold
}
