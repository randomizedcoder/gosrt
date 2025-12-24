package live

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/congestion"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	ring "github.com/randomizedcoder/go-lock-free-ring"
)

// gapSlicePool reuses []uint32 slices for collecting gaps in periodicNakBtree.
// This avoids allocation on every 20ms cycle.
// Slices are returned with len=0 (reset before Put), capacity preserved.
var gapSlicePool = sync.Pool{
	New: func() interface{} {
		s := make([]uint32, 0, 128) // Pre-allocate typical capacity
		return &s
	},
}

// DefaultNakConsolidationBudgetUs is the default time budget for NAK consolidation (2ms).
// This should be sufficient for consolidating thousands of NAK entries under normal conditions.
// If consolidation routinely exceeds this budget, it indicates a performance problem.
const DefaultNakConsolidationBudgetUs = 2_000 // 2ms in microseconds

// defaultNakConsolidationBudget returns the NAK consolidation budget as a time.Duration.
// If configValue is 0, uses DefaultNakConsolidationBudgetUs (5ms).
func defaultNakConsolidationBudget(configValue uint64) time.Duration {
	if configValue == 0 {
		return DefaultNakConsolidationBudgetUs * time.Microsecond
	}
	return time.Duration(configValue) * time.Microsecond
}

// ReceiveConfig is the configuration for the liveRecv congestion control
type ReceiveConfig struct {
	InitialSequenceNumber  circular.Number
	PeriodicACKInterval    uint64 // microseconds
	PeriodicNAKInterval    uint64 // microseconds
	OnSendACK              func(seq circular.Number, light bool)
	OnSendNAK              func(list []circular.Number)
	OnDeliver              func(p packet.Packet)
	PacketReorderAlgorithm string                     // "list" (default) or "btree"
	BTreeDegree            int                        // B-tree degree (default: 32, only used if PacketReorderAlgorithm == "btree")
	LockTimingMetrics      *metrics.LockTimingMetrics // Optional lock timing metrics for performance monitoring
	ConnectionMetrics      *metrics.ConnectionMetrics // For atomic statistics updates

	// Buffer pool for zero-copy support (Phase 2: Lockless Design)
	// When provided, receiver.releasePacketFully() returns buffers to this pool
	BufferPool *sync.Pool

	// NAK btree configuration (Phase 4)
	UseNakBtree            bool    // Enable NAK btree for improved out-of-order handling
	SuppressImmediateNak   bool    // Suppress immediate NAK, let periodic NAK handle gaps
	TsbpdDelay             uint64  // Microseconds, for scan window calculation
	NakRecentPercent       float64 // Percentage of TSBPD delay for "recent" window (e.g., 0.10)
	NakMergeGap            uint32  // Maximum gap to merge into a single range
	NakConsolidationBudget uint64  // Microseconds, time budget for consolidation

	// FastNAK configuration
	FastNakEnabled       bool   // Enable FastNAK after silence
	FastNakThresholdUs   uint64 // Microseconds, silence threshold to trigger FastNAK
	FastNakRecentEnabled bool   // Enable FastNAKRecent to detect sequence jumps

	// Lock-free ring buffer configuration (Phase 3: Lockless Design)
	// When enabled, Push() writes to ring (lock-free), Tick() drains ring before processing
	UsePacketRing             bool          // Enable lock-free ring for packet handoff
	PacketRingSize            int           // Ring capacity per shard (must be power of 2)
	PacketRingShards          int           // Number of shards (must be power of 2)
	PacketRingMaxRetries      int           // Max immediate retries before backoff
	PacketRingBackoffDuration time.Duration // Delay between backoff retries
	PacketRingMaxBackoffs     int           // Max backoff iterations (0 = unlimited)

	// Event loop configuration (Phase 4: Lockless Design)
	// When enabled, replaces timer-driven Tick() with continuous event loop
	// REQUIRES: UsePacketRing=true (event loop consumes from ring)
	UseEventLoop          bool          // Enable continuous event loop
	EventLoopRateInterval time.Duration // Rate metric calculation interval (default: 1s)
	BackoffColdStartPkts  int           // Packets before adaptive backoff engages
	BackoffMinSleep       time.Duration // Minimum sleep during idle periods
	BackoffMaxSleep       time.Duration // Maximum sleep during idle periods

	// Debug logging (for investigation)
	// LogFunc is called for debug logging following the gosrt pattern:
	//   LogFunc("receiver:nak:debug", func() string { return "message" })
	Debug   bool                        // Enable debug logging
	LogFunc func(string, func() string) // Logging callback (lazy evaluation)
}

// adaptiveBackoff manages sleep duration during idle periods in the event loop.
// Uses actual receive rate (from Phase 1 metrics) to determine appropriate backoff.
// Higher traffic = shorter sleeps, lower traffic = longer sleeps.
type adaptiveBackoff struct {
	metrics          *metrics.ConnectionMetrics
	minSleep         time.Duration // Floor for sleep (e.g., 10µs)
	maxSleep         time.Duration // Ceiling for sleep (e.g., 1ms)
	coldStart        int           // Packets to see before engaging backoff
	currentSleep     time.Duration // Current sleep duration
	idleIterations   int64         // Consecutive idle iterations
	packetsSeenTotal uint64        // Total packets seen (for cold start)
}

// newAdaptiveBackoff creates a new adaptive backoff with the given configuration.
func newAdaptiveBackoff(m *metrics.ConnectionMetrics, minSleep, maxSleep time.Duration, coldStart int) *adaptiveBackoff {
	return &adaptiveBackoff{
		metrics:      m,
		minSleep:     minSleep,
		maxSleep:     maxSleep,
		coldStart:    coldStart,
		currentSleep: minSleep,
	}
}

// recordActivity resets backoff state when a packet is processed or delivered.
// This ensures the loop stays responsive when traffic is flowing.
func (b *adaptiveBackoff) recordActivity() {
	b.idleIterations = 0
	b.currentSleep = b.minSleep
	b.packetsSeenTotal++
}

// getSleepDuration returns the appropriate sleep duration for the current state.
// Uses the receive rate from Phase 1 metrics to determine backoff.
func (b *adaptiveBackoff) getSleepDuration() time.Duration {
	b.idleIterations++

	// Cold start: don't sleep much until we've seen enough traffic
	// This ensures responsiveness during connection establishment
	if b.packetsSeenTotal < uint64(b.coldStart) {
		return b.minSleep
	}

	// Use receive rate to determine backoff
	// Higher rate = shorter sleep, lower rate = longer sleep
	rate := b.metrics.GetRecvRatePacketsPerSec()

	if rate < 100 {
		// Low rate (< 100 pkt/s): use maximum sleep
		return b.maxSleep
	} else if rate > 10000 {
		// High rate (> 10000 pkt/s): use minimum sleep
		return b.minSleep
	}

	// Linear interpolation: 100 pkt/s -> maxSleep, 10000 pkt/s -> minSleep
	// ratio goes from 0.0 (at 100 pkt/s) to 1.0 (at 10000 pkt/s)
	ratio := (rate - 100) / (10000 - 100)
	sleepRange := b.maxSleep - b.minSleep
	return b.maxSleep - time.Duration(float64(sleepRange)*ratio)
}

// receiver implements the Receiver interface
type receiver struct {
	maxSeenSequenceNumber       circular.Number
	lastACKSequenceNumber       circular.Number
	lastDeliveredSequenceNumber circular.Number
	packetStore                 packetStore
	lock                        sync.RWMutex
	lockTiming                  *metrics.LockTimingMetrics // Optional lock timing metrics
	metrics                     *metrics.ConnectionMetrics // For atomic statistics updates

	// Buffer pool for zero-copy support (Phase 2: Lockless Design)
	// Used by releasePacketFully() to return receive buffers to the pool
	bufferPool *sync.Pool

	// nPackets removed - now using metrics.RecvLightACKCounter (Phase 1: Lockless)
	// rate struct removed - now using metrics.ConnectionMetrics atomics (Phase 1: Lockless)

	periodicACKInterval uint64 // config
	periodicNAKInterval uint64 // config

	lastPeriodicACK uint64
	lastPeriodicNAK uint64

	// Running averages (atomic uint64 with Float64bits/Float64frombits)
	// Using atomic operations for lock-free access per gosrt_lockless_design.md Section 8.3
	avgPayloadSizeBits  atomic.Uint64 // float64 via math.Float64bits/Float64frombits
	avgLinkCapacityBits atomic.Uint64 // float64 via math.Float64bits/Float64frombits

	probeTime    time.Time
	probeNextSeq circular.Number

	sendACK func(seq circular.Number, light bool)
	sendNAK func(list []circular.Number)
	deliver func(p packet.Packet)

	// NAK btree fields (Phase 4)
	useNakBtree            bool
	suppressImmediateNak   bool
	nakBtree               *nakBtree
	tsbpdDelay             uint64 // Microseconds
	nakRecentPercent       float64
	nakMergeGap            uint32
	nakConsolidationBudget time.Duration

	// FastNAK fields
	fastNakEnabled       bool
	fastNakThreshold     time.Duration
	fastNakRecentEnabled bool

	// FastNAK tracking (atomic for lock-free access)
	lastPacketArrivalTime AtomicTime    // Time of last packet arrival
	lastNakTime           AtomicTime    // Time of last NAK sent
	lastDataPacketSeq     atomic.Uint32 // Last data packet sequence (for FastNAKRecent)

	// NAK scan tracking - independent of ACK to avoid TSBPD skip issues
	// This ensures we never skip scanning a region even if ACK jumps forward
	nakScanStartPoint atomic.Uint32 // Starting sequence for next NAK btree scan

	// ACK scan optimization: remembers the highest verified contiguous sequence.
	// This allows periodicACK to only scan NEW packets, not re-verify entire buffer.
	// Similar pattern to nakScanStartPoint. Protected by r.lock.
	ackScanHighWaterMark circular.Number

	// Lock-free ring buffer (Phase 3: Lockless Design)
	// When enabled, Push() writes to packetRing (lock-free), Tick() drains to btree
	usePacketRing bool
	packetRing    *ring.ShardedRing   // Lock-free ring for packet handoff
	writeConfig   ring.WriteConfig    // Backoff configuration for ring writes
	pushFn        func(packet.Packet) // Function dispatch: pushToRing or pushWithLock

	// Event loop (Phase 4: Lockless Design)
	// When enabled, replaces timer-driven Tick() with continuous event loop
	useEventLoop          bool
	eventLoopRateInterval time.Duration
	backoffColdStartPkts  int
	backoffMinSleep       time.Duration
	backoffMaxSleep       time.Duration

	// Debug logging
	debug   bool
	logFunc func(string, func() string)
}

// NewReceiver takes a ReceiveConfig and returns a new Receiver
func NewReceiver(config ReceiveConfig) congestion.Receiver {
	// Choose packet store implementation based on config
	var store packetStore
	if config.PacketReorderAlgorithm == "btree" {
		degree := config.BTreeDegree
		if degree <= 0 {
			degree = 32 // Default btree degree
		}
		store = NewBTreePacketStore(degree)
	} else {
		// Default to list implementation
		store = NewListPacketStore()
	}

	r := &receiver{
		maxSeenSequenceNumber:       config.InitialSequenceNumber.Dec(),
		lastACKSequenceNumber:       config.InitialSequenceNumber.Dec(),
		lastDeliveredSequenceNumber: config.InitialSequenceNumber.Dec(),
		packetStore:                 store,
		lockTiming:                  config.LockTimingMetrics,
		metrics:                     config.ConnectionMetrics,
		bufferPool:                  config.BufferPool, // Phase 2: zero-copy support

		periodicACKInterval: config.PeriodicACKInterval,
		periodicNAKInterval: config.PeriodicNAKInterval,

		// avgPayloadSize initialized via atomic below

		sendACK: config.OnSendACK,
		sendNAK: config.OnSendNAK,
		deliver: config.OnDeliver,

		// NAK btree configuration
		useNakBtree:            config.UseNakBtree,
		suppressImmediateNak:   config.SuppressImmediateNak,
		tsbpdDelay:             config.TsbpdDelay,
		nakRecentPercent:       config.NakRecentPercent,
		nakMergeGap:            config.NakMergeGap,
		nakConsolidationBudget: defaultNakConsolidationBudget(config.NakConsolidationBudget),

		// FastNAK configuration
		fastNakEnabled:       config.FastNakEnabled,
		fastNakThreshold:     time.Duration(config.FastNakThresholdUs) * time.Microsecond,
		fastNakRecentEnabled: config.FastNakRecentEnabled,
	}

	// Create NAK btree if enabled
	if r.useNakBtree {
		degree := config.BTreeDegree
		if degree <= 0 {
			degree = 32 // Default btree degree
		}
		r.nakBtree = newNakBtree(degree)
	}

	// Initialize lock-free ring buffer if enabled (Phase 3: Lockless Design)
	if config.UsePacketRing {
		r.usePacketRing = true

		// Calculate total capacity (per-shard size * number of shards)
		ringSize := config.PacketRingSize
		if ringSize <= 0 {
			ringSize = 1024 // Default per-shard capacity
		}
		numShards := config.PacketRingShards
		if numShards <= 0 {
			numShards = 4 // Default number of shards
		}
		totalCapacity := uint64(ringSize * numShards)

		var err error
		r.packetRing, err = ring.NewShardedRing(totalCapacity, uint64(numShards))
		if err != nil {
			panic(fmt.Sprintf("failed to create packet ring: %v", err))
		}

		// Configure backoff behavior for ring writes
		r.writeConfig = ring.WriteConfig{
			MaxRetries:      config.PacketRingMaxRetries,
			BackoffDuration: config.PacketRingBackoffDuration,
			MaxBackoffs:     config.PacketRingMaxBackoffs,
		}
		// Apply defaults if not configured
		if r.writeConfig.MaxRetries <= 0 {
			r.writeConfig.MaxRetries = 10
		}
		if r.writeConfig.BackoffDuration <= 0 {
			r.writeConfig.BackoffDuration = 100 * time.Microsecond
		}
		// MaxBackoffs=0 is valid (unlimited), so no default needed

		// Function dispatch: use ring path
		r.pushFn = r.pushToRing
	} else {
		// Function dispatch: use legacy locked path
		r.pushFn = r.pushWithLock
	}

	// Event loop configuration (Phase 4: Lockless Design)
	if config.UseEventLoop {
		r.useEventLoop = true
		r.eventLoopRateInterval = config.EventLoopRateInterval
		if r.eventLoopRateInterval <= 0 {
			r.eventLoopRateInterval = 1 * time.Second // Default: 1s
		}
		r.backoffColdStartPkts = config.BackoffColdStartPkts
		if r.backoffColdStartPkts <= 0 {
			r.backoffColdStartPkts = 1000 // Default: 1000 packets
		}
		r.backoffMinSleep = config.BackoffMinSleep
		if r.backoffMinSleep <= 0 {
			r.backoffMinSleep = 10 * time.Microsecond // Default: 10µs
		}
		r.backoffMaxSleep = config.BackoffMaxSleep
		if r.backoffMaxSleep <= 0 {
			r.backoffMaxSleep = 1 * time.Millisecond // Default: 1ms
		}
	}

	// Debug logging configuration
	if config.Debug {
		r.debug = true
		r.logFunc = config.LogFunc
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

	// Initialize rate calculation period in ConnectionMetrics (Phase 1: Lockless)
	// Default period is 1 second (1,000,000 microseconds)
	if r.metrics != nil {
		r.metrics.RecvRatePeriodUs.Store(uint64(time.Second.Microseconds()))
		r.metrics.RecvRateLastUs.Store(0)
	}

	// Initialize running averages with atomic operations (Phase 2: avgPayloadSize atomic)
	// 5.1.2. SRT's Default LiveCC Algorithm - default 1456 bytes
	r.avgPayloadSizeBits.Store(math.Float64bits(1456))
	// avgLinkCapacity starts at 0

	// Initialize nakScanStartPoint from InitialSequenceNumber (known from handshake).
	// This is CRITICAL for wraparound handling:
	// - If we used btree.Min() instead, we'd get the CIRCULAR minimum (e.g., 0 or 1)
	// - But for a stream starting at MAX-2, logical order is: MAX-2, MAX-1, MAX, 0, 1, 2, ...
	// - Btree circular order is: 0, 1, 2, ..., MAX-2, MAX-1, MAX
	// - Starting from btree.Min() would give wrong gap detection across wraparound
	r.nakScanStartPoint.Store(config.InitialSequenceNumber.Val())

	return r
}

// releasePacketFully returns the packet's buffer to the pool, then decommissions the packet.
// This is the Phase 2 (Lockless Design) buffer lifecycle abstraction.
//
// For zero-copy path: zeros the buffer, returns to bufferPool, clears recvBuffer, decommissions packet
// For legacy path: just decommissions (recvBuffer will be nil)
//
// Call this instead of p.Decommission() when the packet came from a zero-copy unmarshal.
func (r *receiver) releasePacketFully(p packet.Packet) {
	// DecommissionWithBuffer safely handles both zero-copy and legacy paths.
	// It checks HasRecvBuffer() internally and only returns buffer if present.
	p.DecommissionWithBuffer(r.bufferPool)
}

func (r *receiver) Stats() congestion.ReceiveStats {
	// Lock-free reads of running averages via atomic operations
	avgPayloadSize := math.Float64frombits(r.avgPayloadSizeBits.Load())
	avgLinkCapacity := math.Float64frombits(r.avgLinkCapacityBits.Load())

	bytePayload := uint64(avgPayloadSize)
	mbpsLinkCapacity := avgLinkCapacity * packet.MAX_PAYLOAD_SIZE * 8 / 1024 / 1024

	// Phase 1: Lockless - Get rates from ConnectionMetrics (lock-free)
	m := r.metrics
	mbpsBandwidth := m.GetRecvRateMbps()            // Uses atomic load + conversion
	pktRetransRate := m.GetRecvRateRetransPercent() // Uses atomic load + conversion

	// Update atomic counters for instantaneous/calculated values
	m.CongestionRecvBytePayload.Store(bytePayload)
	m.CongestionRecvMbpsBandwidth.Store(uint64(mbpsBandwidth * 1000))
	m.CongestionRecvMbpsLinkCapacity.Store(uint64(mbpsLinkCapacity * 1000))
	m.CongestionRecvPktRetransRate.Store(uint64(pktRetransRate * 100))

	// Build return struct from atomic counters (lock-free reads)
	return congestion.ReceiveStats{
		Pkt:         m.CongestionRecvPkt.Load(),
		Byte:        m.CongestionRecvByte.Load(),
		PktUnique:   m.CongestionRecvPktUnique.Load(),
		ByteUnique:  m.CongestionRecvByteUnique.Load(),
		PktLoss:     m.CongestionRecvPktLoss.Load(),
		ByteLoss:    m.CongestionRecvByteLoss.Load(),
		PktRetrans:  m.CongestionRecvPktRetrans.Load(),
		ByteRetrans: m.CongestionRecvByteRetrans.Load(),
		PktBelated:  m.CongestionRecvPktBelated.Load(),
		ByteBelated: m.CongestionRecvByteBelated.Load(),
		PktDrop: m.CongestionRecvDataDropTooOld.Load() +
			m.CongestionRecvDataDropAlreadyAcked.Load() +
			m.CongestionRecvDataDropDuplicate.Load() +
			m.CongestionRecvDataDropStoreInsertFailed.Load(),
		ByteDrop:                   m.CongestionRecvByteDrop.Load(), // ByteDrop is maintained by helper functions
		PktBuf:                     m.CongestionRecvPktBuf.Load(),
		ByteBuf:                    m.CongestionRecvByteBuf.Load(),
		MsBuf:                      m.CongestionRecvMsBuf.Load(),
		BytePayload:                bytePayload,
		MbpsEstimatedRecvBandwidth: mbpsBandwidth,
		MbpsEstimatedLinkCapacity:  mbpsLinkCapacity,
		PktRetransRate:             pktRetransRate,
	}
}

func (r *receiver) PacketRate() (pps, bps, capacity float64) {
	// Phase 1: Lockless - pps and bps from atomic metrics (no lock needed)
	m := r.metrics
	pps = m.GetRecvRatePacketsPerSec()
	bps = m.GetRecvRateBytesPerSec()

	// Lock-free read of avgLinkCapacity via atomic operation
	capacity = math.Float64frombits(r.avgLinkCapacityBits.Load())

	return
}

func (r *receiver) Flush() {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.packetStore.Clear()
}

func (r *receiver) Push(pkt packet.Packet) {
	// Phase 3: Lockless - Use function dispatch for ring vs locked path
	r.pushFn(pkt)
}

// pushWithLock is the legacy locked path (UsePacketRing=false)
// This wraps the existing pushLocked behavior with optional lock timing metrics.
func (r *receiver) pushWithLock(pkt packet.Packet) {
	if r.lockTiming != nil {
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			r.pushLocked(pkt)
		})
		return
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	r.pushLocked(pkt)
}

// pushToRing is the new lock-free path (UsePacketRing=true)
// Writes packet to lock-free ring buffer for later processing by Tick().
// This decouples packet arrival (io_uring completion) from processing (event loop).
func (r *receiver) pushToRing(pkt packet.Packet) {
	// Rate metrics (always atomic - Phase 1)
	m := r.metrics
	m.RecvLightACKCounter.Add(1)
	m.RecvRatePackets.Add(1)
	m.RecvRateBytes.Add(pkt.Len())

	// Use packet sequence number for shard selection (distributes load)
	producerID := uint64(pkt.Header().PacketSequenceNumber.Val())

	if !r.packetRing.WriteWithBackoff(producerID, pkt, r.writeConfig) {
		// Ring write failed after all backoff retries - ring is persistently full
		m.RingDropsTotal.Add(1)
		r.releasePacketFully(pkt)
	}
}

func (r *receiver) pushLocked(pkt packet.Packet) {
	// Dispatch to appropriate implementation based on NAK btree mode
	if r.useNakBtree {
		r.pushLockedNakBtree(pkt)
		return
	}
	r.pushLockedOriginal(pkt)
}

// pushLockedNakBtree handles packet arrival when NAK btree is enabled (io_uring path).
// Key difference: NO gap detection or immediate NAK.
// With io_uring, packets arrive out of order, so gap detection would cause false positives.
// Instead, the btree sorts packets automatically, and periodicNakBtree scans for real gaps.
func (r *receiver) pushLockedNakBtree(pkt packet.Packet) {
	m := r.metrics

	if pkt == nil {
		m.CongestionRecvPktNil.Add(1)
		return
	}

	now := time.Now()
	seq := pkt.Header().PacketSequenceNumber.Val()

	// FastNAK tracking: detect outage recovery
	if r.fastNakEnabled && r.fastNakRecentEnabled {
		r.checkFastNakRecent(seq, now)
	}

	// Phase 1: Lockless - Use atomic counters instead of embedded rate struct
	m.RecvLightACKCounter.Add(1) // Replaces r.nPackets++
	pktLen := pkt.Len()
	m.RecvRatePackets.Add(1)    // Replaces r.rate.packets++
	m.RecvRateBytes.Add(pktLen) // Replaces r.rate.bytes += pktLen

	m.CongestionRecvPkt.Add(1)
	m.CongestionRecvByte.Add(uint64(pktLen))

	if pkt.Header().RetransmittedPacketFlag {
		m.CongestionRecvPktRetrans.Add(1)
		m.CongestionRecvByteRetrans.Add(uint64(pktLen))
		m.RecvRateBytesRetrans.Add(pktLen) // Replaces r.rate.bytesRetrans += pktLen
	}

	// 5.1.2. SRT's Default LiveCC Algorithm - Exponential Moving Average
	// Using atomic load/store (no CAS loop needed - EMA tolerates rare lost updates)
	oldAvg := math.Float64frombits(r.avgPayloadSizeBits.Load())
	newAvg := 0.875*oldAvg + 0.125*float64(pktLen)
	r.avgPayloadSizeBits.Store(math.Float64bits(newAvg))

	// Check if too old (already delivered)
	if pkt.Header().PacketSequenceNumber.Lte(r.lastDeliveredSequenceNumber) {
		m.CongestionRecvPktBelated.Add(1)
		m.CongestionRecvByteBelated.Add(uint64(pktLen))
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
		return
	}

	// Check if already acknowledged
	if pkt.Header().PacketSequenceNumber.Lt(r.lastACKSequenceNumber) {
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonAlreadyAcked, uint64(pktLen))
		return
	}

	// Check for duplicate (already in store)
	if r.packetStore.Has(pkt.Header().PacketSequenceNumber) {
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonDuplicate, uint64(pktLen))
		return
	}

	// Delete from NAK btree - this packet is no longer missing
	if r.nakBtree != nil {
		if r.nakBtree.Delete(seq) {
			m.NakBtreeDeletes.Add(1)
		}
	}

	// Insert into packet btree (btree handles ordering automatically)
	if r.packetStore.Insert(pkt) {
		m.CongestionRecvPktBuf.Add(1)
		m.CongestionRecvPktUnique.Add(1)
		m.CongestionRecvByteBuf.Add(uint64(pktLen))
		m.CongestionRecvByteUnique.Add(uint64(pktLen))
	} else {
		m.CongestionRecvPktStoreInsertFailed.Add(1)
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonStoreInsertFailed, uint64(pktLen))
	}

	// Update FastNAK tracking (after packet is accepted)
	r.lastPacketArrivalTime.Store(now)
	r.lastDataPacketSeq.Store(seq)

	// NOTE: No gap detection, no immediate NAK, no maxSeenSequenceNumber tracking
	// Gaps are detected by periodicNakBtree() which scans the packet btree
}

// pushLockedOriginal is the original implementation with gap detection and immediate NAK.
// Used when NAK btree is disabled (non-io_uring path).
func (r *receiver) pushLockedOriginal(pkt packet.Packet) {
	// Check metrics once at the beginning of the function
	m := r.metrics

	if pkt == nil {
		m.CongestionRecvPktNil.Add(1)
		return
	}

	// This is not really well (not at all) described in the specs. See core.cpp and window.h
	// and search for PUMASK_SEQNO_PROBE (0xF). Every 16th and 17th packet are
	// sent in pairs. This is used as a probe for the theoretical capacity of the link.
	if !pkt.Header().RetransmittedPacketFlag {
		probe := pkt.Header().PacketSequenceNumber.Val() & 0xF
		switch probe {
		case 0:
			r.probeTime = time.Now()
			r.probeNextSeq = pkt.Header().PacketSequenceNumber.Inc()
		case 1:
			if pkt.Header().PacketSequenceNumber.Equals(r.probeNextSeq) && !r.probeTime.IsZero() && pkt.Len() != 0 {
				// The time between packets scaled to a fully loaded packet
				diff := float64(time.Since(r.probeTime).Microseconds()) * (packet.MAX_PAYLOAD_SIZE / float64(pkt.Len()))
				if diff != 0 {
					// Here we're doing an average of the measurements (atomic EMA update)
					oldCap := math.Float64frombits(r.avgLinkCapacityBits.Load())
					newCap := 0.875*oldCap + 0.125*1_000_000/diff
					r.avgLinkCapacityBits.Store(math.Float64bits(newCap))
				}
			} else {
				r.probeTime = time.Time{}
			}
		default:
			r.probeTime = time.Time{}
		}
	} else {
		r.probeTime = time.Time{}
	}

	// Phase 1: Lockless - Use atomic counters instead of embedded rate struct
	m.RecvLightACKCounter.Add(1) // Replaces r.nPackets++

	pktLen := pkt.Len()

	m.RecvRatePackets.Add(1)    // Replaces r.rate.packets++
	m.RecvRateBytes.Add(pktLen) // Replaces r.rate.bytes += pktLen

	m.CongestionRecvPkt.Add(1)
	m.CongestionRecvByte.Add(uint64(pktLen))

	//pkt.PktTsbpdTime = pkt.Timestamp + r.delay
	if pkt.Header().RetransmittedPacketFlag {
		m.CongestionRecvPktRetrans.Add(1)
		m.CongestionRecvByteRetrans.Add(uint64(pktLen))

		m.RecvRateBytesRetrans.Add(pktLen) // Replaces r.rate.bytesRetrans += pktLen
	}

	// 5.1.2. SRT's Default LiveCC Algorithm - Exponential Moving Average
	// Using atomic load/store (no CAS loop needed - EMA tolerates rare lost updates)
	oldAvg := math.Float64frombits(r.avgPayloadSizeBits.Load())
	newAvg := 0.875*oldAvg + 0.125*float64(pktLen)
	r.avgPayloadSizeBits.Store(math.Float64bits(newAvg))

	if pkt.Header().PacketSequenceNumber.Lte(r.lastDeliveredSequenceNumber) {
		// Too old, because up until r.lastDeliveredSequenceNumber, we already delivered
		m.CongestionRecvPktBelated.Add(1)
		m.CongestionRecvByteBelated.Add(uint64(pktLen))
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
		return
	}

	if pkt.Header().PacketSequenceNumber.Lt(r.lastACKSequenceNumber) {
		// Already acknowledged, ignoring
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonAlreadyAcked, uint64(pktLen))
		return
	}

	if pkt.Header().PacketSequenceNumber.Equals(r.maxSeenSequenceNumber.Inc()) {
		// In order, the packet we expected
		r.maxSeenSequenceNumber = pkt.Header().PacketSequenceNumber
	} else if pkt.Header().PacketSequenceNumber.Lte(r.maxSeenSequenceNumber) {
		// Out of order, is it a missing piece? put it in the correct position
		if r.packetStore.Has(pkt.Header().PacketSequenceNumber) {
			// Already received (has been sent more than once), ignoring
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonDuplicate, uint64(pktLen))
			return
		}

		// Insert in correct position (packetStore handles ordering)
		if r.packetStore.Insert(pkt) {
			// Late arrival, this fills a gap
			m.CongestionRecvPktBuf.Add(1)
			m.CongestionRecvPktUnique.Add(1)
			m.CongestionRecvByteBuf.Add(uint64(pktLen))
			m.CongestionRecvByteUnique.Add(uint64(pktLen))
		} else {
			// Duplicate (shouldn't happen after Has check, but be safe)
			m.CongestionRecvPktStoreInsertFailed.Add(1)
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonStoreInsertFailed, uint64(pktLen))
		}

		return
	} else {
		// Too far ahead, there are some missing sequence numbers, immediate NAK report.
		// TODO: Implement SRTO_LOSSMAXTTL to delay NAK for reordered packets.
		nakList := []circular.Number{
			r.maxSeenSequenceNumber.Inc(),
			pkt.Header().PacketSequenceNumber.Dec(),
		}
		r.sendNAK(nakList)

		// Count packets requested by this NAK using shared helper.
		// This ensures 100% consistency with how the sender counts received NAKs.
		// Note: The helper correctly handles both single (start==end) and range entries.
		missingPkts := metrics.CountNAKEntries(m, nakList, metrics.NAKCounterSend)

		// Update loss counters with the correct packet count
		m.CongestionRecvPktLoss.Add(missingPkts)
		avgPayloadSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
		m.CongestionRecvByteLoss.Add(missingPkts * avgPayloadSize)

		r.maxSeenSequenceNumber = pkt.Header().PacketSequenceNumber
	}

	m.CongestionRecvPktBuf.Add(1)
	m.CongestionRecvPktUnique.Add(1)
	m.CongestionRecvByteBuf.Add(uint64(pktLen))
	m.CongestionRecvByteUnique.Add(uint64(pktLen))

	r.packetStore.Insert(pkt)
}

// periodicACK calculates the ACK sequence number by scanning contiguous packets.
//
// Performance optimizations (see integration_testing_50mbps_defect.md Section 24 & 26):
// - Uses IterateFrom with AscendGreaterOrEqual for O(log n) seek
// - ACK Scan High Water Mark: only scans NEW packets, not entire buffer (96.7% reduction)
// - Batches metrics updates with stack counters (single atomic update after loop)
// - Minimizes operations under RLock
func (r *receiver) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
	// Phase 1: Read-only work with read lock (allows concurrent Push() operations)
	r.lock.RLock()

	// Early return check (read-only)
	// Phase 1: Lockless - Use atomic RecvLightACKCounter instead of r.nPackets
	needLiteACK := false
	lightACKCount := r.metrics.RecvLightACKCounter.Load()
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		if lightACKCount >= 64 {
			needLiteACK = true // Will send light ACK, but can't reset counter yet
		} else {
			r.lock.RUnlock()
			return // Early return - no ACK needed
		}
	}

	// Read-only iteration (read lock allows concurrent Push() operations)
	minPktTsbpdTime, maxPktTsbpdTime := uint64(0), uint64(0)
	ackSequenceNumber := r.lastACKSequenceNumber

	// Get first packet - needed for buffer time calculation AND scan start validation
	minPkt := r.packetStore.Min()
	if minPkt == nil {
		// No packets in btree - but we should still send periodic ACK for keepalive.
		// This confirms to the sender what we've already received.
		// Skip to write phase with current lastACKSequenceNumber.
		r.lock.RUnlock()

		// Send ACK with last known sequence number (no new packets to acknowledge)
		if r.lockTiming != nil {
			var okResult bool
			var seqResult circular.Number
			var liteResult bool
			metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
				okResult, seqResult, liteResult = r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime, circular.Number{})
			})
			return okResult, seqResult, liteResult
		}
		r.lock.Lock()
		defer r.lock.Unlock()
		return r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime, circular.Number{})
	}
	minH := minPkt.Header()
	minPktTsbpdTime = minH.PktTsbpdTime
	maxPktTsbpdTime = minH.PktTsbpdTime
	minPktSeq := minH.PacketSequenceNumber

	// ACK Scan High Water Mark optimization (Section 26):
	// Instead of scanning from lastACKSequenceNumber every time, remember where we
	// verified contiguous packets up to. Only scan NEW packets since last check.
	// This reduces iterations by ~96.7% at steady state.
	scanStartPoint := r.ackScanHighWaterMark

	// Determine valid scan start point (must handle four cases):
	// 1. Not initialized (Val() == 0): start from lastACKSequenceNumber
	// 2. Behind lastACKSequenceNumber: start from lastACKSequenceNumber
	// 3. Behind minPkt (packets expired from btree): start from minPkt
	// 4. Valid (ahead of both): use high water mark
	if scanStartPoint.Val() == 0 || scanStartPoint.Lt(ackSequenceNumber) {
		// Case 1 & 2: Not initialized or behind ACK point
		scanStartPoint = ackSequenceNumber
	}

	// Case 3: High water mark points to expired packet
	// Tick() released packets, minPkt advanced past our remembered position
	// NOTE: This only updates scanStartPoint (where to START iterating), NOT ackSequenceNumber.
	// We must still verify there's no gap between lastACKSequenceNumber and minPkt.
	if minPktSeq.Gt(scanStartPoint) {
		scanStartPoint = minPktSeq.Dec() // Start just before minPkt to include it
	}

	// NOTE: Removed buggy "Case 4" logic that advanced ackSequenceNumber based on scanStartPoint.
	// The scanStartPoint only controls WHERE we start iterating (for efficiency).
	// The ackSequenceNumber must only advance when we verify actual packet contiguity.
	// See: lockless_phase2_implementation.md "ACK Scan High Water Mark Bug" section.

	// Stack counter for skipped packets - batched update after loop (avoids atomic ops under lock)
	var totalSkippedPkts uint64
	var lastContiguousSeq circular.Number // Track highest verified contiguous sequence
	firstPacketChecked := false           // Track if we've checked the first packet

	// Find the sequence number up until we have all in a row.
	// Where the first gap is (or at the end of the list) is where we can ACK to.
	// Uses IterateFrom for O(log n) seek to scanStartPoint (not lastACKSequenceNumber!)
	r.packetStore.IterateFrom(scanStartPoint, func(p packet.Packet) bool {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()

		// Skip packets at or before scan start point (handles btree edge case)
		if h.PacketSequenceNumber.Lte(scanStartPoint) {
			return true // Continue
		}

		// CRITICAL FIX: Check for gap between lastACKSequenceNumber and first packet.
		// If scanStartPoint was advanced due to packet delivery (Case 3), we must verify
		// that there's no gap between the last ACKed packet and the first remaining packet.
		// Without this check, we'd incorrectly ACK past lost packets.
		if !firstPacketChecked {
			firstPacketChecked = true
			// If the first packet isn't contiguous with ackSequenceNumber, there's a gap
			// (unless this packet's TSBPD time has passed, in which case we skip it)
			if h.PktTsbpdTime > now && !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
				// Gap detected: can't advance ACK past lastACKSequenceNumber
				return false // Stop iteration
			}
		}

		// If there are packets that should have been delivered by now, move forward.
		// This is where we "skip" packets that NEVER arrived - count them!
		if h.PktTsbpdTime <= now {
			// Count packets skipped: gap between current ACK and this packet
			// e.g., if ackSequenceNumber=10 and h.PacketSequenceNumber=15,
			// then packets 11,12,13,14 are being skipped (4 packets)
			skippedCount := uint64(h.PacketSequenceNumber.Distance(ackSequenceNumber))
			if skippedCount > 1 {
				// skippedCount-1 because Distance(10,15)=5, but we skip 11,12,13,14 (4 packets)
				totalSkippedPkts += skippedCount - 1 // Stack counter, no atomic
			}
			ackSequenceNumber = h.PacketSequenceNumber
			lastContiguousSeq = ackSequenceNumber
			return true // Continue
		}

		// Check if the packet is the next in the row.
		if h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
			ackSequenceNumber = h.PacketSequenceNumber
			lastContiguousSeq = ackSequenceNumber
			maxPktTsbpdTime = h.PktTsbpdTime
			return true // Continue
		}

		return false // Stop iteration (gap found)
	})

	// Capture high water mark update for write phase
	newHighWaterMark := lastContiguousSeq

	// Release read lock before acquiring write lock (optimization: minimize lock contention)
	r.lock.RUnlock()

	// Update metrics ONCE after lock released (batched from stack counters)
	// avgPayloadSize is now atomic - no race condition, can read anytime
	m := r.metrics
	if m != nil && totalSkippedPkts > 0 {
		avgPayloadSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
		m.CongestionRecvPktSkippedTSBPD.Add(totalSkippedPkts)
		m.CongestionRecvByteSkippedTSBPD.Add(totalSkippedPkts * avgPayloadSize)
	}

	// Phase 2: Write updates with write lock (brief - only for field updates)
	// Measure lock timing for the write lock (critical section)
	if r.lockTiming != nil {
		var okResult bool
		var seqResult circular.Number
		var liteResult bool
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			okResult, seqResult, liteResult = r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime, newHighWaterMark)
		})
		return okResult, seqResult, liteResult
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime, newHighWaterMark)
}

func (r *receiver) periodicACKWriteLocked(now uint64, needLiteACK bool, ackSequenceNumber circular.Number, minPktTsbpdTime, maxPktTsbpdTime uint64, newHighWaterMark circular.Number) (ok bool, sequenceNumber circular.Number, lite bool) {
	// Check metrics once at the beginning of the function
	m := r.metrics

	// Re-check conditions (may have changed between read and write lock)
	// If interval check still applies and we don't need lite ACK, return early
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		if !needLiteACK {
			return // Early return - no update needed
		}
		// Lite ACK needed, continue to update fields
		lite = true
	}

	// Track that periodicACK actually ran (not just returned early)
	// Used for health monitoring: expected ~100/sec (10ms interval)
	if m != nil {
		m.CongestionRecvPeriodicACKRuns.Add(1)
	}

	// Update fields (write lock held - brief operation)
	ok = true
	sequenceNumber = ackSequenceNumber.Inc()

	// Keep track of the last ACK's sequence number. With this we can faster ignore
	// packets that come in late that have a lower sequence number.
	r.lastACKSequenceNumber = ackSequenceNumber

	// Update ACK scan high water mark for next periodicACK call
	// This allows us to skip re-verifying contiguous packets we've already checked
	if newHighWaterMark.Val() > 0 && newHighWaterMark.Gt(r.ackScanHighWaterMark) {
		r.ackScanHighWaterMark = newHighWaterMark
	}

	r.lastPeriodicACK = now
	r.metrics.RecvLightACKCounter.Store(0) // Phase 1: Lockless - Reset atomic counter

	msBuf := (maxPktTsbpdTime - minPktTsbpdTime) / 1_000
	m.CongestionRecvMsBuf.Store(msBuf)

	return
}

func (r *receiver) periodicNAK(now uint64) []circular.Number {
	// Debug: log dispatch decision
	if r.debug && r.logFunc != nil {
		if r.useNakBtree {
			btreeSize := 0
			if r.nakBtree != nil {
				btreeSize = r.nakBtree.Len()
			}
			r.logFunc("receiver:nak:debug", func() string {
				return fmt.Sprintf("periodicNAK: using NAK btree (size=%d, nakBtree=%v)",
					btreeSize, r.nakBtree != nil)
			})
		} else {
			r.logFunc("receiver:nak:debug", func() string {
				return "periodicNAK: using original (packet btree scan)"
			})
		}
	}

	// Dispatch to appropriate implementation
	if r.useNakBtree {
		return r.periodicNakBtree(now)
	}
	return r.periodicNakOriginal(now)
}

// periodicNakOriginal is the original implementation that iterates through the packet store.
func (r *receiver) periodicNakOriginal(now uint64) []circular.Number {
	if r.lockTiming != nil {
		var result []circular.Number
		metrics.WithRLockTiming(r.lockTiming, &r.lock, func() {
			result = r.periodicNakOriginalLocked(now)
		})
		return result
	}
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.periodicNakOriginalLocked(now)
}

// periodicNakOriginalLocked builds the NAK loss list by iterating through the packet store.
// RFC SRT Appendix A defines two NAK encoding formats:
// - Figure 21: Single sequence number (start == end) - 4 bytes on wire
// - Figure 22: Range of sequence numbers (start != end) - 8 bytes on wire
// The list contains pairs [start, end] for each gap found.
func (r *receiver) periodicNakOriginalLocked(now uint64) []circular.Number {
	if now-r.lastPeriodicNAK < r.periodicNAKInterval {
		return nil
	}

	// Track that periodicNAK actually ran (not just returned early)
	// Used for health monitoring: expected ~50/sec (20ms interval)
	m := r.metrics
	if m != nil {
		m.CongestionRecvPeriodicNAKRuns.Add(1)
		m.NakPeriodicOriginalRuns.Add(1)
	}

	list := []circular.Number{}

	// Send a periodic NAK

	ackSequenceNumber := r.lastACKSequenceNumber

	// Send a NAK for all gaps.
	// Not all gaps might get announced because the size of the NAK packet is limited.
	r.packetStore.Iterate(func(p packet.Packet) bool {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()

		// Skip packets that we already ACK'd.
		if h.PacketSequenceNumber.Lte(ackSequenceNumber) {
			return true // Continue
		}

		// If this packet is not in sequence, we stop here and report that gap.
		if !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
			nackSequenceNumber := ackSequenceNumber.Inc()

			list = append(list, nackSequenceNumber)
			list = append(list, h.PacketSequenceNumber.Dec())
		}

		ackSequenceNumber = h.PacketSequenceNumber
		return true // Continue
	})

	r.lastPeriodicNAK = now

	return list
}

// periodicNakBtree scans the packet btree to find gaps and builds NAK list.
// This is the new implementation for handling out-of-order packets with io_uring.
//
// Algorithm:
// 1. Scan packet btree from last ACK'd sequence
// 2. For each gap in the sequence, add missing seqs to NAK btree
// 3. Skip packets that are "too recent" (might still be in flight)
// 4. Consolidate NAK btree into ranges and return
//
// Performance optimizations (see integration_testing_50mbps_defect.md Section 23.8):
// - Uses IterateFrom with AscendGreaterOrEqual for O(log n) seek
// - Minimizes lock scope to only packetStore iteration
// - Uses sync.Pool for gap slice reuse (zero allocs per cycle)
// - Batches metrics updates (single atomic op instead of per-packet)
// - expireNakEntries moved to Tick() after sendNAK (not in hot path)
func (r *receiver) periodicNakBtree(now uint64) []circular.Number {
	// === PRE-WORK: No lock needed ===

	if now-r.lastPeriodicNAK < r.periodicNAKInterval {
		if r.metrics != nil {
			r.metrics.NakPeriodicSkipped.Add(1)
		}
		return nil
	}

	// Track that periodicNAK actually ran
	m := r.metrics
	if m != nil {
		m.CongestionRecvPeriodicNAKRuns.Add(1)
		m.NakPeriodicBtreeRuns.Add(1)
	}

	if r.nakBtree == nil {
		// This should never happen when useNakBtree=true (ISSUE-001)
		if m != nil {
			m.NakBtreeNilWhenEnabled.Add(1)
		}
		return nil
	}

	// Note: expireNakEntries() moved to Tick() after sendNAK - not in hot path

	// Step 1: Calculate "too recent" threshold (no lock needed)
	// Packets with TSBPD beyond this are too new to NAK (might be reordered, not lost)
	tooRecentThreshold := now
	if r.nakRecentPercent > 0 && r.tsbpdDelay > 0 {
		tooRecentThreshold = now + uint64(float64(r.tsbpdDelay)*r.nakRecentPercent)
	}

	// DEBUG: Log threshold calculation
	if r.debug && r.logFunc != nil {
		r.logFunc("receiver:nak:scan:debug", func() string {
			return fmt.Sprintf("periodicNakBtree SCAN: now=%d, tsbpdDelay=%d, nakRecentPercent=%.2f, tooRecentThreshold=%d (now+%dms)",
				now, r.tsbpdDelay, r.nakRecentPercent, tooRecentThreshold, (tooRecentThreshold-now)/1000)
		})
	}

	// Get gap slice from pool (zero allocs per cycle)
	gapsPtr := gapSlicePool.Get().(*[]uint32)
	defer func() {
		*gapsPtr = (*gapsPtr)[:0] // Reset length, preserve capacity
		gapSlicePool.Put(gapsPtr)
	}()

	// Track metrics locally, update once after loop (reduces atomic ops ~95x)
	var packetsScanned uint64
	var lastScannedSeq uint32

	// === MINIMAL LOCK SCOPE: Only for packetStore access ===
	r.lock.RLock()

	// Step 2: Get starting point (under lock for packetStore.Min access)
	// Initialize lazily from oldest packet in store
	startSeq := r.nakScanStartPoint.Load()
	firstScanEver := false
	if startSeq == 0 {
		minPkt := r.packetStore.Min()
		if minPkt == nil {
			r.lock.RUnlock()
			return nil // No packets yet
		}
		startSeq = minPkt.Header().PacketSequenceNumber.Val()
		r.nakScanStartPoint.Store(startSeq)
		firstScanEver = true // Flag: this is our first NAK scan, expectedSeq should start from btree.Min()
	}

	// DEBUG: Log packet btree state
	if r.debug && r.logFunc != nil {
		minPkt := r.packetStore.Min()
		var minSeq uint32
		var minTsbpd uint64
		if minPkt != nil {
			minSeq = minPkt.Header().PacketSequenceNumber.Val()
			minTsbpd = minPkt.Header().PktTsbpdTime
		}
		btreeSize := r.packetStore.Len()
		r.logFunc("receiver:nak:scan:debug", func() string {
			return fmt.Sprintf("SCAN WINDOW: startSeq=%d, btree_min=%d, btree_size=%d, minTsbpd=%d, threshold=%d",
				startSeq, minSeq, btreeSize, minTsbpd, tooRecentThreshold)
		})
	}

	// Step 3: Scan packet btree from startSeq to find gaps
	// Uses IterateFrom with AscendGreaterOrEqual for O(log n) seek to start
	//
	// IMPORTANT: The first packet we find might be > startSeq because:
	// a) Packets between nakScanStartPoint and btree_min were DELIVERED (not lost)
	// b) Packets between nakScanStartPoint and btree_min are ACTUALLY MISSING (lost)
	//
	// To distinguish: use lastDeliveredSequenceNumber
	// - If startSeq <= lastDeliveredSequenceNumber: those packets were delivered (skip gap)
	// - If startSeq > lastDeliveredSequenceNumber: those packets are missing (detect gap)
	startSeqNum := circular.New(startSeq, packet.MAX_SEQUENCENUMBER)

	// Determine initial expectedSeq:
	// - On first scan ever: start from btree.Min() (we just learned the starting sequence)
	// - If startSeq is beyond what we've delivered: use startSeq (detect gaps from there)
	// - If startSeq is at or before lastDelivered: use lastDelivered+1 (skip delivered packets)
	var expectedSeq circular.Number
	if firstScanEver {
		// First NAK scan: we just learned the starting sequence from btree.Min()
		// expectedSeq should start from there - nothing before that was ever received
		expectedSeq = startSeqNum
	} else {
		lastDelivered := r.lastDeliveredSequenceNumber.Val()
		if circular.SeqLessOrEqual(startSeq, lastDelivered) {
			// startSeq was already delivered, start expecting from lastDelivered+1
			expectedSeq = circular.New(circular.SeqAdd(lastDelivered, 1), packet.MAX_SEQUENCENUMBER)
		} else {
			// startSeq is beyond lastDelivered, start expecting from startSeq
			expectedSeq = startSeqNum
		}
	}
	firstPacket := true

	// DEBUG: Track first gap for logging
	var firstGapExpected, firstGapActual uint32
	var firstGapFound bool

	// scanPacket is a closure for processing each packet during NAK scan
	// Returns true to continue iteration, false to stop
	scanPacket := func(pkt packet.Packet) bool {
		h := pkt.Header()
		actualSeqNum := h.PacketSequenceNumber

		// Stop if this packet is "too recent" (might still be reordered)
		if h.PktTsbpdTime > tooRecentThreshold {
			// DEBUG: Log why we stopped
			if r.debug && r.logFunc != nil && packetsScanned == 0 {
				r.logFunc("receiver:nak:scan:debug", func() string {
					return fmt.Sprintf("SCAN STOPPED AT FIRST PACKET: seq=%d, PktTsbpdTime=%d > threshold=%d (packet is too recent)",
						actualSeqNum.Val(), h.PktTsbpdTime, tooRecentThreshold)
				})
			}
			return false // Stop iteration
		}

		// For the first packet found, we DON'T reset expectedSeq
		// It was already set correctly above based on lastDeliveredSequenceNumber
		// This allows us to detect gaps from expectedSeq to actualSeqNum (if any)
		if firstPacket {
			firstPacket = false
			// expectedSeq was already set above, don't override
		}

		// Detect gaps: expected vs actual (only BETWEEN packets we find)
		if actualSeqNum.Gt(expectedSeq) {
			// There's a gap - collect missing sequences for batch insert
			gapStart := expectedSeq.Val()
			seq := gapStart
			endSeq := actualSeqNum.Dec().Val()

			// DEBUG: Record first gap for logging
			if !firstGapFound {
				firstGapExpected = gapStart
				firstGapActual = actualSeqNum.Val()
				firstGapFound = true
			}

			for circular.SeqLess(seq, endSeq) || seq == endSeq {
				*gapsPtr = append(*gapsPtr, seq)
				seq = circular.SeqAdd(seq, 1)
			}
		}

		packetsScanned++ // Local counter, not atomic
		lastScannedSeq = actualSeqNum.Val()
		expectedSeq = actualSeqNum.Inc()
		return true // Continue
	}

	// Pass 1: Iterate from startSeq to end of btree (in circular order)
	stoppedEarly := false
	r.packetStore.IterateFrom(startSeqNum, func(pkt packet.Packet) bool {
		if !scanPacket(pkt) {
			stoppedEarly = true
			return false
		}
		return true
	})

	// DEBUG: Log state after Pass 1
	if r.debug && r.logFunc != nil {
		r.logFunc("receiver:nak:scan:debug", func() string {
			return fmt.Sprintf("PASS1 COMPLETE: expectedSeq=%d, startSeq=%d, stoppedEarly=%v, packetsScanned=%d",
				expectedSeq.Val(), startSeq, stoppedEarly, packetsScanned)
		})
	}

	// Pass 2: Handle sequence number wraparound
	// If we started near MAX and expectedSeq has wrapped to near 0, we need to
	// continue scanning from the beginning of the btree (where 0, 1, 2, ... are stored)
	//
	// Detect wraparound: startSeq is "high" (> MAX/2) and expectedSeq is "low" (< MAX/2)
	// This means we've crossed the MAX→0 boundary.
	//
	// NOTE: We use SeqLess directly, NOT circular.Number.Lt() because:
	// - Lt() returns false when distance > threshold (half sequence space)
	// - SeqLess uses signed arithmetic which correctly identifies wraparound
	if !stoppedEarly && packetsScanned > 0 {
		// Check if we need to wrap around using SeqLess (not Lt)
		if circular.SeqLess(expectedSeq.Val(), startSeq) {
			if r.debug && r.logFunc != nil {
				r.logFunc("receiver:nak:scan:debug", func() string {
					return fmt.Sprintf("WRAPAROUND DETECTED: expectedSeq=%d < startSeq=%d, continuing from btree.Min()",
						expectedSeq.Val(), startSeq)
				})
			}

			// Continue from beginning of btree (where wrapped sequences are stored)
			// Stop when we reach startSeq or hit tooRecentThreshold
			//
			// NOTE: Use SeqGreaterOrEqual directly because Gte() uses threshold comparison
			// which doesn't work correctly for wraparound scenarios
			r.packetStore.Iterate(func(pkt packet.Packet) bool {
				h := pkt.Header()
				actualSeqNum := h.PacketSequenceNumber

				// Stop if we've reached or passed startSeq (completed the wrap)
				// Using SeqGreaterOrEqual for correct wraparound handling
				if circular.SeqGreaterOrEqual(actualSeqNum.Val(), startSeq) {
					return false
				}

				return scanPacket(pkt)
			})
		}
	}

	// DEBUG: Log scan results
	if r.debug && r.logFunc != nil {
		if firstGapFound {
			r.logFunc("receiver:nak:scan:debug", func() string {
				return fmt.Sprintf("GAPS DETECTED: first gap at expected=%d, actual=%d, total_gaps=%d, packets_scanned=%d, lastScannedSeq=%d",
					firstGapExpected, firstGapActual, len(*gapsPtr), packetsScanned, lastScannedSeq)
			})
		} else if packetsScanned > 0 {
			r.logFunc("receiver:nak:scan:debug", func() string {
				return fmt.Sprintf("NO GAPS: packets_scanned=%d, lastScannedSeq=%d",
					packetsScanned, lastScannedSeq)
			})
		}
	}

	r.lock.RUnlock()
	// === END LOCK SCOPE ===

	// === POST-WORK: No lock needed (nakBtree has its own lock) ===

	// Update metrics once (single atomic op instead of per-packet)
	if m != nil && packetsScanned > 0 {
		m.NakBtreeScanPackets.Add(packetsScanned)
	}

	// Batch insert all gaps with single lock acquisition
	if len(*gapsPtr) > 0 {
		inserted := r.nakBtree.InsertBatch(*gapsPtr)
		if m != nil {
			m.NakBtreeInserts.Add(uint64(inserted))
			m.NakBtreeScanGaps.Add(uint64(len(*gapsPtr)))
		}
	}

	// Step 4: Update scan start point for next iteration
	if lastScannedSeq > 0 {
		r.nakScanStartPoint.Store(lastScannedSeq)
	}

	// Step 5: Consolidate NAK btree into ranges (has its own lock)
	list := r.consolidateNakBtree()

	// Update NAK btree size gauge
	if m != nil {
		m.NakBtreeSize.Store(uint64(r.nakBtree.Len()))
	}

	r.lastPeriodicNAK = now

	// Debug: log NAK list if not empty
	if r.debug && r.logFunc != nil && len(list) > 0 {
		btreeSize := r.nakBtree.Len()
		r.logFunc("receiver:nak:debug", func() string {
			// Show first few entries to avoid huge logs
			preview := list
			if len(preview) > 10 {
				preview = preview[:10]
			}
			return fmt.Sprintf("periodicNakBtree: generated %d NAK entries, btree_size=%d, first_10=%v",
				len(list), btreeSize, preview)
		})
	}

	return list
}

// expireNakEntries removes entries from the NAK btree that are too old to be useful.
// An entry is expired if its sequence is less than the oldest packet in the packet btree.
// This is called in Tick() AFTER sendNAK to keep it out of the hot path.
// The NAK btree has its own lock, so this only needs brief RLock for packetStore.Min().
func (r *receiver) expireNakEntries() int {
	if r.nakBtree == nil {
		// This should never happen when useNakBtree=true (ISSUE-001)
		if r.metrics != nil {
			r.metrics.NakBtreeNilWhenEnabled.Add(1)
		}
		return 0
	}

	// Find the oldest packet in the packet btree (brief lock)
	r.lock.RLock()
	minPkt := r.packetStore.Min()
	var cutoff uint32
	if minPkt != nil {
		cutoff = minPkt.Header().PacketSequenceNumber.Val()
	}
	r.lock.RUnlock()

	if minPkt == nil {
		return 0 // Empty packet store, nothing to expire
	}

	// Any NAK entry older than the oldest packet's sequence is expired
	// (the packet btree has already released those packets via TSBPD)
	// nakBtree.DeleteBefore has its own lock
	expired := r.nakBtree.DeleteBefore(cutoff)
	if expired > 0 && r.metrics != nil {
		r.metrics.NakBtreeExpired.Add(uint64(expired))
	}

	return expired
}

// drainPacketRing consumes all packets from the lock-free ring into the btree.
// This is called at the start of Tick() when UsePacketRing is enabled.
//
// Key properties:
//   - TryRead() is NON-BLOCKING: returns (nil, false) when ring is empty
//   - Loop terminates immediately when ring is empty
//   - After drain, Tick() has exclusive access to btree (no producers writing)
//   - Packets are validated for duplicates/old sequences before insertion
//
// This is the Phase 3 lockless design: producers write to ring (lock-free),
// single consumer (Tick) drains ring and processes btree exclusively.
func (r *receiver) drainPacketRing(now uint64) {
	m := r.metrics

	// Use local accumulators for performance (single atomic increment at end)
	// processedCount = ALL packets read from ring (for delta calculation)
	// drainedCount = packets SUCCESSFULLY inserted into btree
	var processedCount uint64
	var drainedCount uint64

	for {
		// TryRead is non-blocking - returns immediately if ring is empty
		item, ok := r.packetRing.TryRead()
		if !ok {
			// Ring is empty - exit loop and proceed with ACK/NAK/delivery
			break
		}

		p := item.(packet.Packet)
		h := p.Header()
		seq := h.PacketSequenceNumber

		processedCount++ // ALL packets read from ring

		// Duplicate/old packet check (same logic as pushLockedNakBtree)
		// Too old: already delivered
		if seq.Lte(r.lastDeliveredSequenceNumber) {
			m.CongestionRecvPktBelated.Add(1)
			m.CongestionRecvByteBelated.Add(uint64(p.Len()))
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(p.Len()))
			r.releasePacketFully(p)
			continue
		}

		// Already acknowledged
		if seq.Lt(r.lastACKSequenceNumber) {
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonAlreadyAcked, uint64(p.Len()))
			r.releasePacketFully(p)
			continue
		}

		// Duplicate: already in store
		if r.packetStore.Has(seq) {
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonDuplicate, uint64(p.Len()))
			r.releasePacketFully(p)
			continue
		}

		// Delete from NAK btree - this packet is no longer missing
		if r.nakBtree != nil {
			if r.nakBtree.Delete(seq.Val()) {
				m.NakBtreeDeletes.Add(1)
			}
		}

		// Insert into btree (NO LOCK - exclusive access after drain)
		pktLen := p.Len()
		if r.packetStore.Insert(p) {
			drainedCount++ // Successfully inserted into btree
			m.CongestionRecvPktBuf.Add(1)
			m.CongestionRecvPktUnique.Add(1)
			m.CongestionRecvByteBuf.Add(uint64(pktLen))
			m.CongestionRecvByteUnique.Add(uint64(pktLen))
			m.CongestionRecvPkt.Add(1)
			m.CongestionRecvByte.Add(uint64(pktLen))

			if h.RetransmittedPacketFlag {
				m.CongestionRecvPktRetrans.Add(1)
				m.CongestionRecvByteRetrans.Add(uint64(pktLen))
			}
		} else {
			m.CongestionRecvPktStoreInsertFailed.Add(1)
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonStoreInsertFailed, uint64(pktLen))
			r.releasePacketFully(p)
		}
	}

	// Single atomic increments at the end (performance optimization)
	if m != nil {
		if processedCount > 0 {
			m.RingPacketsProcessed.Add(processedCount)
		}
		if drainedCount > 0 {
			m.RingDrainedPackets.Add(drainedCount)
		}
	}
}

// drainAllFromRing drains all pending packets from the ring into the btree.
// This is a wrapper around drainPacketRing for use in EventLoop where we need
// to ensure the btree is up-to-date before ACK/NAK processing.
// The `now` parameter in drainPacketRing is unused, so we pass 0.
func (r *receiver) drainAllFromRing() {
	if r.packetRing == nil {
		return
	}
	r.drainPacketRing(0)
}

// drainRingByDelta drains packets from ring based on received vs processed delta.
// This ensures all received packets are in the btree before periodic operations.
//
// The delta calculation uses two atomic counters:
//   - RecvRatePackets: incremented when packets are pushed to ring
//   - RingPacketsProcessed: incremented when packets are consumed from ring
//
// The difference tells us exactly how many packets are in the ring.
// This is O(1) - just two atomic loads and a subtraction.
//
// Returns number of packets actually drained.
func (r *receiver) drainRingByDelta() uint64 {
	if r.packetRing == nil || r.metrics == nil {
		return 0
	}

	m := r.metrics
	received := m.RecvRatePackets.Load()
	processed := m.RingPacketsProcessed.Load()

	// Calculate expected ring contents
	if received <= processed {
		return 0 // Ring should be empty (or counter wrapped - unlikely)
	}
	delta := received - processed

	// Debug: log delta calculation
	if r.debug && r.logFunc != nil && delta > 0 {
		r.logFunc("receiver:ring:debug", func() string {
			return fmt.Sprintf("drainRingByDelta: received=%d, processed=%d, delta=%d",
				received, processed, delta)
		})
	}

	// Use local accumulators for performance (single atomic increment at end)
	var processedCount uint64
	var drainedCount uint64

	// Drain up to delta packets
	for i := uint64(0); i < delta; i++ {
		item, ok := r.packetRing.TryRead()
		if !ok {
			break // Ring actually empty (counter race - fine)
		}

		processedCount++

		// Process the packet (same logic as drainPacketRing)
		p := item.(packet.Packet)
		h := p.Header()
		seq := h.PacketSequenceNumber
		pktLen := p.Len()

		// Duplicate/old packet check
		if seq.Lte(r.lastDeliveredSequenceNumber) {
			m.CongestionRecvPktBelated.Add(1)
			m.CongestionRecvByteBelated.Add(uint64(pktLen))
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
			r.releasePacketFully(p)
			continue
		}

		if seq.Lt(r.lastACKSequenceNumber) {
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonAlreadyAcked, uint64(pktLen))
			r.releasePacketFully(p)
			continue
		}

		if r.packetStore.Has(seq) {
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonDuplicate, uint64(pktLen))
			r.releasePacketFully(p)
			continue
		}

		// Delete from NAK btree - this packet is no longer missing
		if r.nakBtree != nil {
			if r.nakBtree.Delete(seq.Val()) {
				m.NakBtreeDeletes.Add(1)
			}
		}

		// Insert into btree
		if r.packetStore.Insert(p) {
			drainedCount++
			m.CongestionRecvPktBuf.Add(1)
			m.CongestionRecvPktUnique.Add(1)
			m.CongestionRecvByteBuf.Add(uint64(pktLen))
			m.CongestionRecvByteUnique.Add(uint64(pktLen))
			m.CongestionRecvPkt.Add(1)
			m.CongestionRecvByte.Add(uint64(pktLen))

			if h.RetransmittedPacketFlag {
				m.CongestionRecvPktRetrans.Add(1)
				m.CongestionRecvByteRetrans.Add(uint64(pktLen))
			}
		} else {
			m.CongestionRecvPktStoreInsertFailed.Add(1)
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonStoreInsertFailed, uint64(pktLen))
			r.releasePacketFully(p)
		}
	}

	// Single atomic increments at the end (performance optimization)
	if processedCount > 0 {
		m.RingPacketsProcessed.Add(processedCount)
	}
	if drainedCount > 0 {
		m.RingDrainedPackets.Add(drainedCount)
	}

	// Debug: log drain result
	if r.debug && r.logFunc != nil && processedCount > 0 {
		r.logFunc("receiver:ring:debug", func() string {
			return fmt.Sprintf("drainRingByDelta: drained %d packets (processed=%d, btree_inserts=%d)",
				processedCount, processedCount, drainedCount)
		})
	}

	return drainedCount
}

func (r *receiver) Tick(now uint64) {
	// Phase 3/4: Drain ring buffer before processing (if enabled)
	// Uses delta-based drain for precise control: received - processed = in ring
	// This transfers packets from lock-free ring into btree for ordered processing.
	// After drain, Tick() has exclusive access to btree - no locks needed.
	if r.usePacketRing {
		r.drainRingByDelta()
	}

	if ok, sequenceNumber, lite := r.periodicACK(now); ok {
		r.sendACK(sequenceNumber, lite)
	}

	if list := r.periodicNAK(now); len(list) != 0 {
		// Count NAK entries using shared helper before sending.
		// This ensures 100% consistency with how the sender counts received NAKs.
		// RFC SRT Appendix A:
		//   - Figure 21: Single (start == end) - 4 bytes on wire
		//   - Figure 22: Range (start != end) - 8 bytes on wire
		metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
		r.sendNAK(list)
	}

	// Expire NAK btree entries AFTER NAK is sent - not time-critical
	// This was moved from periodicNakBtree() to keep it out of the hot path.
	// We have 10-20ms until next Tick/periodicNAK cycle.
	if r.useNakBtree && r.nakBtree != nil {
		r.expireNakEntries()
	}

	// Deliver packets whose PktTsbpdTime is ripe
	// Capture metrics once to avoid repeated checks in closures
	m := r.metrics
	if r.lockTiming != nil {
		var removed int
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			removed = r.packetStore.RemoveAll(
				func(p packet.Packet) bool {
					// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
					h := p.Header()
					return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
				},
				func(p packet.Packet) {
					m.CongestionRecvPktBuf.Add(^uint64(0))                    // Decrement by 1
					m.CongestionRecvByteBuf.Add(^uint64(uint64(p.Len()) - 1)) // Subtract pktLen
					// PktBuf and ByteBuf are decremented in atomic counters above

					// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
					h := p.Header()
					r.lastDeliveredSequenceNumber = h.PacketSequenceNumber

					r.deliver(p)
				},
			)
		})
		_ = removed
	} else {
		r.lock.Lock()
		removed := r.packetStore.RemoveAll(
			func(p packet.Packet) bool {
				// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
				h := p.Header()
				return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
			},
			func(p packet.Packet) {
				m.CongestionRecvPktBuf.Add(^uint64(0))                    // Decrement by 1
				m.CongestionRecvByteBuf.Add(^uint64(uint64(p.Len()) - 1)) // Subtract pktLen

				// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
				h := p.Header()
				r.lastDeliveredSequenceNumber = h.PacketSequenceNumber

				r.deliver(p)
			},
		)
		r.lock.Unlock()
		_ = removed
	}

	// Update rate statistics
	if r.lockTiming != nil {
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			r.updateRateStats(now)
		})
	} else {
		r.lock.Lock()
		r.updateRateStats(now)
		r.lock.Unlock()
	}
}

func (r *receiver) updateRateStats(now uint64) {
	// Phase 1: Lockless - All rate calculations now use atomic ConnectionMetrics
	m := r.metrics

	lastUs := m.RecvRateLastUs.Load()
	periodUs := m.RecvRatePeriodUs.Load()
	tdiff := now - lastUs // microseconds

	if tdiff > periodUs {
		// Load current counters
		packets := m.RecvRatePackets.Load()
		bytes := m.RecvRateBytes.Load()
		bytesRetrans := m.RecvRateBytesRetrans.Load()

		// Calculate rates
		seconds := float64(tdiff) / 1_000_000
		packetsPerSecond := float64(packets) / seconds
		bytesPerSecond := float64(bytes) / seconds

		var pktRetransRate float64
		if bytes != 0 {
			pktRetransRate = float64(bytesRetrans) / float64(bytes) * 100
		}

		// Store computed rates as float64 bits (lock-free)
		m.RecvRatePacketsPerSec.Store(math.Float64bits(packetsPerSecond))
		m.RecvRateBytesPerSec.Store(math.Float64bits(bytesPerSecond))
		m.RecvRatePktRetransRate.Store(math.Float64bits(pktRetransRate))

		// Reset counters for next period
		m.RecvRatePackets.Store(0)
		m.RecvRateBytes.Store(0)
		m.RecvRateBytesRetrans.Store(0)

		// Update last calculation time
		m.RecvRateLastUs.Store(now)
	}
}

// ============================================================================
// Event Loop (Phase 4: Lockless Design)
// ============================================================================

// EventLoop runs the continuous event loop for packet processing.
// This replaces the timer-driven Tick() for lower latency and smoother CPU usage.
// REQUIRES: UsePacketRing=true (event loop consumes from ring)
//
// The event loop:
//   - Processes packets immediately as they arrive from the ring
//   - Delivers packets when TSBPD-ready (not batched)
//   - Uses separate tickers for ACK, NAK, and rate calculation
//   - Adaptive backoff minimizes CPU spin when idle
func (r *receiver) EventLoop(ctx context.Context) {
	if !r.useEventLoop {
		return // Event loop not enabled
	}
	if r.packetRing == nil {
		return // Ring not initialized (should not happen if config validated)
	}

	// Create backoff manager
	backoff := newAdaptiveBackoff(
		r.metrics,
		r.backoffMinSleep,
		r.backoffMaxSleep,
		r.backoffColdStartPkts,
	)

	// ACK interval from config (microseconds -> time.Duration)
	ackInterval := time.Duration(r.periodicACKInterval) * time.Microsecond
	if ackInterval <= 0 {
		ackInterval = 10 * time.Millisecond // Default: 10ms
	}

	// NAK interval from config (microseconds -> time.Duration)
	nakInterval := time.Duration(r.periodicNAKInterval) * time.Microsecond
	if nakInterval <= 0 {
		nakInterval = 20 * time.Millisecond // Default: 20ms
	}

	// Rate calculation interval
	rateInterval := r.eventLoopRateInterval
	if rateInterval <= 0 {
		rateInterval = 1 * time.Second // Default: 1s
	}

	// Create ACK ticker first
	ackTicker := time.NewTicker(ackInterval)
	defer ackTicker.Stop()

	// Offset NAK ticker by half ACK interval to spread work evenly
	// This prevents ACK and NAK from firing simultaneously
	time.Sleep(ackInterval / 2)
	nakTicker := time.NewTicker(nakInterval)
	defer nakTicker.Stop()

	// Offset rate ticker further to spread work
	time.Sleep(ackInterval / 4)
	rateTicker := time.NewTicker(rateInterval)
	defer rateTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ackTicker.C:
			// CRITICAL: Drain ring before ACK to ensure ACK reflects all received packets
			// Uses delta-based drain for precise control: received - processed = in ring
			r.drainRingByDelta()
			now := uint64(time.Now().UnixMicro())
			if ok, seq, lite := r.periodicACK(now); ok {
				r.sendACK(seq, lite)
			}

		case <-nakTicker.C:
			// CRITICAL: Drain ring before NAK scan to avoid false gaps
			// Without this, packets sitting in the ring appear as "gaps" in the btree,
			// causing spurious NAKs even when no packets are actually lost.
			// Uses delta-based drain for precise control: received - processed = in ring
			r.drainRingByDelta()
			now := uint64(time.Now().UnixMicro())
			if list := r.periodicNAK(now); len(list) != 0 {
				metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
				r.sendNAK(list)
			}
			// Expire NAK btree entries after NAK is sent
			if r.useNakBtree && r.nakBtree != nil {
				r.expireNakEntries()
			}

		case <-rateTicker.C:
			now := uint64(time.Now().UnixMicro())
			r.updateRateStats(now)

		default:
			// Primary work: process packets and deliver
			now := uint64(time.Now().UnixMicro())
			processed := r.processOnePacket()
			delivered := r.deliverReadyPacketsNoLock(now)

			if !processed && delivered == 0 {
				// No work done - sleep to avoid CPU spin
				time.Sleep(backoff.getSleepDuration())
			} else {
				// Activity recorded - reset backoff
				backoff.recordActivity()
			}
		}
	}
}

// processOnePacket consumes one packet from the ring and inserts into btree.
// Returns true if a packet was processed (or rejected as duplicate).
// Called by EventLoop's default case for continuous processing.
func (r *receiver) processOnePacket() bool {
	if r.packetRing == nil {
		return false
	}

	// TryRead is non-blocking - returns immediately if ring is empty
	item, ok := r.packetRing.TryRead()
	if !ok {
		return false // Ring empty
	}

	p := item.(packet.Packet)
	h := p.Header()
	seq := h.PacketSequenceNumber
	pktLen := p.Len()
	m := r.metrics

	// RingPacketsProcessed = ALL packets read from ring (for delta calculation)
	// This is incremented unconditionally when a packet is read from the ring
	if m != nil {
		m.RingPacketsProcessed.Add(1)
	}

	// Duplicate/old packet check
	// Too old: already delivered
	if seq.Lte(r.lastDeliveredSequenceNumber) {
		m.CongestionRecvPktBelated.Add(1)
		m.CongestionRecvByteBelated.Add(uint64(pktLen))
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
		r.releasePacketFully(p)
		return true // Still processed (rejected)
	}

	// Already acknowledged
	if seq.Lt(r.lastACKSequenceNumber) {
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonAlreadyAcked, uint64(pktLen))
		r.releasePacketFully(p)
		return true
	}

	// Duplicate: already in store
	if r.packetStore.Has(seq) {
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonDuplicate, uint64(pktLen))
		r.releasePacketFully(p)
		return true
	}

	// Delete from NAK btree - this packet is no longer missing
	if r.nakBtree != nil {
		if r.nakBtree.Delete(seq.Val()) {
			m.NakBtreeDeletes.Add(1)
		}
	}

	// Insert into btree (NO LOCK - exclusive access in event loop)
	if r.packetStore.Insert(p) {
		// RingDrainedPackets = packets SUCCESSFULLY inserted into btree (subset of processed)
		m.RingDrainedPackets.Add(1)
		m.CongestionRecvPktBuf.Add(1)
		m.CongestionRecvPktUnique.Add(1)
		m.CongestionRecvByteBuf.Add(uint64(pktLen))
		m.CongestionRecvByteUnique.Add(uint64(pktLen))
		m.CongestionRecvPkt.Add(1)
		m.CongestionRecvByte.Add(uint64(pktLen))

		if h.RetransmittedPacketFlag {
			m.CongestionRecvPktRetrans.Add(1)
			m.CongestionRecvByteRetrans.Add(uint64(pktLen))
		}
	} else {
		m.CongestionRecvPktStoreInsertFailed.Add(1)
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonStoreInsertFailed, uint64(pktLen))
		r.releasePacketFully(p)
	}

	return true
}

// deliverReadyPacketsNoLock delivers all packets whose TSBPD time has arrived.
// Called every loop iteration for smooth, non-bursty delivery.
// Returns the count of packets delivered.
// NO LOCK needed - event loop has exclusive access to btree.
func (r *receiver) deliverReadyPacketsNoLock(now uint64) int {
	m := r.metrics
	delivered := 0

	// Iterate from btree.Min() forward, delivering packets whose time has come
	// Stop when we hit a packet still in the future
	removed := r.packetStore.RemoveAll(
		func(p packet.Packet) bool {
			// Check if packet is ready for delivery:
			// 1. Must be <= lastACKSequenceNumber (acknowledged)
			// 2. Must have TSBPD time <= now (ready for playback)
			h := p.Header()
			return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
		},
		func(p packet.Packet) {
			// Update metrics
			pktLen := p.Len()
			m.CongestionRecvPktBuf.Add(^uint64(0))                   // Decrement by 1
			m.CongestionRecvByteBuf.Add(^uint64(uint64(pktLen) - 1)) // Subtract pktLen

			// Update last delivered sequence
			h := p.Header()
			r.lastDeliveredSequenceNumber = h.PacketSequenceNumber

			// Deliver to application
			r.deliver(p)
			delivered++
		},
	)
	_ = removed

	return delivered
}

// UseEventLoop returns whether the event loop is enabled.
// Used by connection code to decide between EventLoop and Tick loop.
func (r *receiver) UseEventLoop() bool {
	return r.useEventLoop
}

func (r *receiver) SetNAKInterval(nakInterval uint64) {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.periodicNAKInterval = nakInterval
}

func (r *receiver) String(t uint64) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("maxSeen=%d lastACK=%d lastDelivered=%d\n", r.maxSeenSequenceNumber.Val(), r.lastACKSequenceNumber.Val(), r.lastDeliveredSequenceNumber.Val()))

	r.lock.RLock()
	r.packetStore.Iterate(func(p packet.Packet) bool {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()
		b.WriteString(fmt.Sprintf("   %d @ %d (in %d)\n", h.PacketSequenceNumber.Val(), h.PktTsbpdTime, int64(h.PktTsbpdTime)-int64(t)))
		return true // Continue
	})
	r.lock.RUnlock()

	return b.String()
}
