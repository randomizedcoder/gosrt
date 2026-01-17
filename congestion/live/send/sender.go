// Package send implements the sender-side congestion control for SRT live mode.
package send

import (
	"container/list"
	"sync"
	"sync/atomic"
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
	OnLog                 func(topic string, message func() string) // Optional logging callback
	LockTimingMetrics     *metrics.LockTimingMetrics                // Optional lock timing metrics for performance monitoring
	ConnectionMetrics     *metrics.ConnectionMetrics                // For atomic statistics updates

	// StartTime is the connection start time for time base calculation.
	// PktTsbpdTime uses relative time (since connection start), so EventLoop
	// must also use relative time for TSBPD comparisons.
	// CRITICAL: Without this, EventLoop uses absolute time (~1.7e12 µs) while
	// PktTsbpdTime uses relative time (~millions µs), causing all packets
	// to appear "too old" and get dropped immediately.
	// Set to time.Now() when creating the connection.
	StartTime time.Time

	// NAK order configuration - when true, retransmit in NAK packet order (receiver-controlled priority)
	HonorNakOrder bool

	// RTO-based retransmit suppression (Phase 6: RTO Suppression)
	// Pointer to connection's pre-calculated RTO in microseconds.
	// When set, sender suppresses retransmits within one-way delay (RTOUs/2).
	RTOUs *atomic.Uint64

	// --- Phase 1: Sender Lockless Btree ---

	// UseBtree enables btree for sender packet storage.
	// When enabled, replaces linked lists with O(log n) btree operations.
	// Default: false (use linked lists)
	UseBtree bool

	// BtreeDegree is the B-tree degree for sender packet storage.
	// Default: 32 (same as receiver)
	BtreeDegree int

	// --- Phase 2: Sender Lock-Free Ring ---

	// UseSendRing enables lock-free ring for Push() operations.
	// When enabled, Push() writes to ring (lock-free), Tick() drains to btree.
	// REQUIRES: UseBtree=true
	// Default: false
	UseSendRing bool

	// SendRingSize is the ring capacity per shard.
	// Default: 1024
	SendRingSize int

	// SendRingShards is the number of ring shards.
	// Default: 1 (preserves strict ordering)
	SendRingShards int

	// --- Phase 3: Sender Control Packet Ring ---

	// UseSendControlRing enables lock-free ring for ACK/NAK routing.
	// When enabled, control packets are queued to EventLoop via ring.
	// CRITICAL: Required for lock-free sender EventLoop.
	// REQUIRES: UseSendRing=true
	// Default: false
	UseSendControlRing bool

	// SendControlRingSize is the control ring capacity per shard.
	// Default: 256
	SendControlRingSize int

	// SendControlRingShards is the number of control ring shards.
	// Default: 2 (one for ACK, one for NAK)
	SendControlRingShards int

	// --- Phase 4: Sender EventLoop ---

	// UseSendEventLoop enables continuous event loop for sender.
	// When enabled, replaces Tick() with continuous EventLoop.
	// REQUIRES: UseSendControlRing=true
	// Default: false
	UseSendEventLoop bool

	// SendEventLoopBackoffMinSleep is minimum sleep during idle periods.
	// Default: 100µs
	SendEventLoopBackoffMinSleep time.Duration

	// SendEventLoopBackoffMaxSleep is maximum sleep during idle periods.
	// Default: 1ms
	SendEventLoopBackoffMaxSleep time.Duration

	// SendTsbpdSleepFactor is the multiplier for TSBPD-aware sleep.
	// Default: 0.9
	SendTsbpdSleepFactor float64

	// SendDropThresholdUs is the threshold for dropping old packets (microseconds).
	// Default: 1000000 (1 second)
	SendDropThresholdUs uint64

	// --- Phase 5: Zero-Copy Payload Pool ---

	// ValidatePayloadSize enables payload size validation in Push().
	// When enabled, payloads exceeding srt.MaxPayloadSize are rejected.
	// Default: false (no validation for backward compatibility)
	ValidatePayloadSize bool
}

// sender implements the Sender interface
type sender struct {
	nextSequenceNumber circular.Number
	lastACKedSequence  circular.Number // Highest sequence number that has been ACK'd
	dropThreshold      uint64

	// Atomic 31-bit sequence number (Phase 2: Lock-free sender)
	// Used when useRing=true for thread-safe sequence assignment in Write() path.
	// Formula: seq = (initialSeq + nextSeqOffset) & packet.MAX_SEQUENCENUMBER
	// Reference: sender_lockfree_architecture.md Section 7.6
	nextSeqOffset atomic.Uint32 // Offset from initialSeq (incremented atomically)
	initialSeq    uint32        // Starting sequence number (set once at init)

	// Legacy storage (list mode)
	packetList *list.List
	lossList   *list.List

	// Phase 1: Btree storage (replaces packetList/lossList when enabled)
	useBtree    bool
	packetBtree *SendPacketBtree // Packets waiting to be sent / pending ACK

	// Tracking points for btree mode (replaces list iteration)
	contiguousPoint    atomic.Uint64 // Highest contiguous seq delivered (like receiver)
	deliveryStartPoint atomic.Uint64 // Start of TSBPD delivery window

	// Sequence gap detection for diagnosing phantom NAK issue (Jan 2026)
	lastInsertedSeq    atomic.Uint64 // Last sequence number inserted into btree
	lastInsertedSeqSet atomic.Bool   // True after first packet inserted

	lock       sync.RWMutex
	lockTiming *metrics.LockTimingMetrics // Optional lock timing metrics
	metrics    *metrics.ConnectionMetrics // For atomic statistics updates

	avgPayloadSize float64 // bytes
	pktSndPeriod   float64 // microseconds
	maxBW          float64 // bytes/s
	inputBW        float64 // bytes/s
	overheadBW     float64 // percent

	// Probe time for link capacity probing (atomic for concurrent PushDirect)
	probeTime atomic.Uint64

	// rate struct removed - now using metrics.ConnectionMetrics atomics (Phase 1: Lockless)

	deliver func(p packet.Packet)
	log     func(topic string, message func() string) // Optional logging callback

	// NAK order configuration
	honorNakOrder bool // When true, retransmit in NAK packet order (receiver-controlled priority)

	// RTO-based retransmit suppression (Phase 6: RTO Suppression)
	// Pointer to connection's pre-calculated RTO in microseconds.
	// Nil = suppression disabled (legacy behavior)
	rtoUs *atomic.Uint64

	// Phase 2: Lock-free ring
	useRing    bool
	packetRing *SendPacketRing

	// Phase 3: Control packet ring
	// controlRing is the lock-free ring for ACK/NAK routing.
	// nil means disabled, non-nil means enabled (no separate bool needed).
	// Reference: completely_lockfree_receiver.md Section 6.1.2
	controlRing *SendControlRing

	// Phase 4: EventLoop
	useEventLoop     bool
	backoffMinSleep  time.Duration
	backoffMaxSleep  time.Duration
	tsbpdSleepFactor float64
	// dropThreshold is already defined at line 109

	// Phase 4: Time base for EventLoop
	// CRITICAL: PktTsbpdTime uses relative time (since connection start), so
	// EventLoop must also use relative time for TSBPD comparisons.
	// Without this, EventLoop uses absolute time (~1.7e12 µs) while packets
	// have relative time (~millions µs), causing all packets to be dropped.
	nowFn func() uint64

	// Phase 5: Zero-copy payload validation
	validatePayloadSize bool // When true, validate payload size in Push()

	// Debug context tracking (Step 7.5.2: Runtime Verification)
	// Only active in debug builds (-tags debug), zero-size struct in release builds.
	debug debugContext
}

// NewSender takes a SendConfig and returns a new Sender
func NewSender(sendConfig SendConfig) congestion.Sender {
	s := &sender{
		nextSequenceNumber: sendConfig.InitialSequenceNumber,
		initialSeq:         sendConfig.InitialSequenceNumber.Val(), // For atomic mode
		dropThreshold:      sendConfig.DropThreshold,
		lockTiming:         sendConfig.LockTimingMetrics,
		metrics:            sendConfig.ConnectionMetrics,

		avgPayloadSize: packet.MAX_PAYLOAD_SIZE, //  5.1.2. SRT's Default LiveCC Algorithm
		maxBW:          float64(sendConfig.MaxBW),
		inputBW:        float64(sendConfig.InputBW),
		overheadBW:     float64(sendConfig.OverheadBW),

		deliver: sendConfig.OnDeliver,
		log:     sendConfig.OnLog,

		honorNakOrder: sendConfig.HonorNakOrder,

		rtoUs: sendConfig.RTOUs, // RTO suppression (nil = disabled)

		// Phase 1: Btree mode
		useBtree: sendConfig.UseBtree,
	}

	// Initialize storage based on config
	if s.useBtree {
		// Phase 1: Btree storage
		degree := sendConfig.BtreeDegree
		if degree == 0 {
			degree = 32 // Default (same as receiver)
		}
		s.packetBtree = NewSendPacketBtree(degree)
		// Note: lossList concept merged into packetBtree - all packets tracked in one structure
		// After delivery, packets stay in btree until ACK'd or dropped
	} else {
		// Legacy: linked lists
		s.packetList = list.New()
		s.lossList = list.New()
	}

	// Phase 2: Initialize ring if enabled
	if sendConfig.UseSendRing {
		if !sendConfig.UseBtree {
			panic("UseSendRing requires UseBtree=true")
		}

		ringSize := sendConfig.SendRingSize
		if ringSize == 0 {
			ringSize = 1024 // Default
		}

		ringShards := sendConfig.SendRingShards
		if ringShards < 1 {
			ringShards = 1 // Default: single shard for ordering
		}

		var err error
		s.packetRing, err = NewSendPacketRing(ringSize, ringShards)
		if err != nil {
			panic("failed to create send packet ring: " + err.Error())
		}
		s.useRing = true
	}

	// Phase 3: Initialize control ring if enabled
	if sendConfig.UseSendControlRing {
		if !sendConfig.UseSendRing {
			panic("UseSendControlRing requires UseSendRing=true")
		}

		controlRingSize := sendConfig.SendControlRingSize
		if controlRingSize == 0 {
			controlRingSize = 256 // Default
		}

		controlRingShards := sendConfig.SendControlRingShards
		if controlRingShards < 1 {
			controlRingShards = 2 // Default: 2 shards (ACK/NAK separation)
		}

		var err error
		s.controlRing, err = NewSendControlRing(controlRingSize, controlRingShards)
		if err != nil {
			panic("failed to create send control ring: " + err.Error())
		}
		// Note: No separate useControlRing bool - controlRing != nil means enabled
	}

	// Phase 4: Initialize EventLoop if enabled
	if sendConfig.UseSendEventLoop {
		if !sendConfig.UseSendControlRing {
			panic("UseSendEventLoop requires UseSendControlRing=true")
		}

		s.useEventLoop = true

		// Backoff configuration
		s.backoffMinSleep = sendConfig.SendEventLoopBackoffMinSleep
		if s.backoffMinSleep == 0 {
			s.backoffMinSleep = 100 * time.Microsecond // Default: 100µs
		}

		s.backoffMaxSleep = sendConfig.SendEventLoopBackoffMaxSleep
		if s.backoffMaxSleep == 0 {
			s.backoffMaxSleep = 1 * time.Millisecond // Default: 1ms
		}

		// TSBPD sleep factor
		s.tsbpdSleepFactor = sendConfig.SendTsbpdSleepFactor
		if s.tsbpdSleepFactor <= 0 || s.tsbpdSleepFactor > 1.0 {
			s.tsbpdSleepFactor = 0.9 // Default: 90%
		}

		// Override drop threshold if specified in EventLoop config
		if sendConfig.SendDropThresholdUs > 0 {
			s.dropThreshold = sendConfig.SendDropThresholdUs
		}
		// Otherwise keep the existing dropThreshold from sendConfig.DropThreshold

		// CRITICAL: Initialize time function for EventLoop
		// PktTsbpdTime uses relative time (since connection start), so EventLoop
		// must also use relative time for TSBPD comparisons.
		// Without this, EventLoop uses absolute time (~1.7e12 µs) while packets
		// have relative time (~millions µs), causing all packets to be dropped.
		// BUG FIX: This was the cause of "send_data_drop_total [too_old]" for ALL packets.
		if !sendConfig.StartTime.IsZero() {
			start := sendConfig.StartTime
			s.nowFn = func() uint64 {
				return uint64(time.Since(start).Microseconds())
			}
		} else {
			// Fallback: use time since a fixed point (less accurate but avoids crash)
			// In production, StartTime should ALWAYS be set by connection.go
			fallbackStart := time.Now()
			s.nowFn = func() uint64 {
				return uint64(time.Since(fallbackStart).Microseconds())
			}
		}

	}

	// CRITICAL FIX: Initialize deliveryStartPoint to ISN
	// BUG: deliveryStartPoint defaulted to 0 (atomic.Uint64 zero value)
	// but nextSequenceNumber was set to ISN (~549M random value from handshake).
	// This caused IterateFrom(0) to fail finding packets at ~549M in the btree.
	// Result: 60% failure rate in integration tests (packets dropped as "too_old").
	//
	// Compare with receiver (receiver.go:345) which correctly initializes:
	//   r.contiguousPoint.Store(recvConfig.InitialSequenceNumber.Dec().Val())
	//
	// Sender uses IterateFrom(startSeq) which finds packets >= startSeq,
	// so we initialize to ISN (not ISN-1) to find the first packet at ISN.
	//
	// Reference: send_eventloop_intermittent_failure_bug.md
	//
	// Note: This is initialized for ALL modes (including legacy) because
	// the Tick path also uses this value in tickDeliverReadyPacketsBtree.
	s.deliveryStartPoint.Store(uint64(sendConfig.InitialSequenceNumber.Val()))

	// Phase 5: Zero-copy payload validation
	s.validatePayloadSize = sendConfig.ValidatePayloadSize

	// Initialize debug context (Step 7.5.2: Runtime Verification)
	// No-op in release builds, enables assertions in debug builds.
	s.initDebugContext()

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

	if s.useBtree {
		// Phase 1: Btree mode - clear the btree
		s.packetBtree.Clear()
	} else {
		// Legacy: linked lists
		s.packetList = s.packetList.Init()
		s.lossList = s.lossList.Init()
	}
}

func (s *sender) SetDropThreshold(threshold uint64) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.dropThreshold = threshold
}
