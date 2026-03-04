package receive

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	ring "github.com/randomizedcoder/go-lock-free-ring"
	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
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

const DefaultPayloadSizeBytes = 1456

// --- Inter-packet timing constants (nak_btree_expiry_optimization.md Section 4.2) ---

const (
	// InterPacketIntervalMinUs filters out measurement errors (sub-10µs intervals).
	InterPacketIntervalMinUs = 10

	// InterPacketIntervalMaxUs filters out pauses (>100ms gaps aren't normal inter-packet).
	InterPacketIntervalMaxUs = 100_000

	// InterPacketIntervalDefaultUs is the fallback when no measurement available (1ms = ~1000pps).
	InterPacketIntervalDefaultUs = 1000

	// InterPacketEWMAAlpha is the weight for new samples (0.125 = 1/8).
	// Old = 0.875, New = 0.125 provides smooth convergence.
	InterPacketEWMAAlpha = 0.125
)

// interPacketEstimator tracks inter-packet arrival timing for TSBPD estimation.
// All fields are atomic for lock-free access in the event loop.
//
// This struct groups related estimator state to:
// 1. Make the logical relationship between fields explicit
// 2. Prevent partial updates during refactoring
// 3. Enable future extension (e.g., jitter tracking)
//
// See nak_btree_expiry_optimization.md Section 4.6 for warm-up strategy.
type interPacketEstimator struct {
	// avgIntervalUs is the EWMA of inter-packet arrival intervals (microseconds).
	// Updated on each valid packet arrival using 0.875/0.125 weighting.
	avgIntervalUs atomic.Uint64

	// lastArrivalUs is the arrival time of the previous packet (microseconds).
	// Used to calculate the interval to the current packet.
	lastArrivalUs atomic.Uint64

	// sampleCount tracks how many valid samples have been collected.
	// Used for warm-up detection (see ewmaWarmupThreshold config).
	// Saturates at MaxUint32 to avoid overflow.
	sampleCount atomic.Uint32
}

// defaultNakConsolidationBudget returns the NAK consolidation budget as a time.Duration.
// If configValue is 0, uses DefaultNakConsolidationBudgetUs (5ms).
func defaultNakConsolidationBudget(configValue uint64) time.Duration {
	if configValue == 0 {
		return DefaultNakConsolidationBudgetUs * time.Microsecond
	}
	return time.Duration(configValue) * time.Microsecond
}

// parseRetryStrategy converts a string strategy name to ring.RetryStrategy.
// Valid values: "", "sleep", "next", "random", "adaptive", "spin", "hybrid"
// Returns ring.SleepBackoff for empty string or unknown values.
// receiver implements the Receiver interface
type receiver struct {
	maxSeenSequenceNumber circular.Number
	lastACKSequenceNumber circular.Number
	// Note: lastDeliveredSequenceNumber removed in Phase 4 - using contiguousPoint instead
	// See contiguous_point_tsbpd_advancement_design.md
	packetStore packetStore
	lock        sync.RWMutex
	lockTiming  *metrics.LockTimingMetrics // Optional lock timing metrics
	metrics     *metrics.ConnectionMetrics // For atomic statistics updates

	// RTT provider for NAK suppression (Phase 6: RTO Suppression)
	// Set via SetRTTProvider() after connection RTT is configured
	rtt congestion.RTTProvider

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

	// Inter-packet timing estimator for TSBPD estimation fallback
	// See nak_btree_expiry_optimization.md Section 4.6
	interPacketEst interPacketEstimator

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

	// NAK btree expiry configuration (nak_btree_expiry_optimization.md Section 5)
	nakExpiryMargin     float64 // Margin for expiry threshold: now + RTO*(1+margin)
	ewmaWarmupThreshold uint32  // Min samples before EWMA is trusted (0 = disabled)

	// FastNAK fields
	fastNakEnabled       bool
	fastNakThreshold     time.Duration
	fastNakRecentEnabled bool

	// FastNAK tracking (atomic for lock-free access)
	lastPacketArrivalTime AtomicTime    // Time of last packet arrival
	lastNakTime           AtomicTime    // Time of last NAK sent
	lastDataPacketSeq     atomic.Uint32 // Last data packet sequence (for FastNAKRecent)

	// Unified scan starting point (Phase 6/14: ACK Optimization)
	// Last known contiguous sequence number - shared by ACK and NAK scans.
	// Both contiguousScan() and gapScan() start scanning from this point.
	// When contiguous packets are found, this point advances.
	// Uses atomic.Uint32 for lock-free access in EventLoop mode.
	contiguousPoint atomic.Uint32

	// Light ACK tracking (Phase 5: ACK Optimization)
	// Uses difference-based approach: send Light ACK when contiguous progress >= threshold
	lightACKDifference uint32 // Threshold for sending Light ACK (default: 64)
	lastLightACKSeq    uint32 // Sequence when last Light ACK was sent

	// Lock-free ring buffer (Phase 3: Lockless Design)
	// When enabled, Push() writes to packetRing (lock-free), Tick() drains to btree
	usePacketRing bool
	packetRing    *ring.ShardedRing   // Lock-free ring for packet handoff
	writeConfig   ring.WriteConfig    // Backoff configuration for ring writes
	pushFn        func(packet.Packet) // Function dispatch: pushToRing or pushWithLock

	// NAK btree function dispatch (configured once based on usePacketRing)
	// Event loop mode (usePacketRing=true): lock-free versions for single-threaded access
	// Tick mode (usePacketRing=false): locking versions for concurrent Push/Tick safety
	nakInsert       func(seq uint32)
	nakInsertBatch  func(seqs []uint32) int
	nakDelete       func(seq uint32) bool
	nakDeleteBefore func(cutoff uint32) int
	nakLen          func() int

	// TSBPD-aware NAK btree function dispatch (nak_btree_expiry_optimization.md)
	nakInsertBatchWithTsbpd func(seqs []uint32, tsbpdTimes []uint64) int
	nakDeleteBeforeTsbpd    func(expiryThresholdUs uint64) int
	nakIterateAndUpdate     func(fn func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool))

	// Event loop (Phase 4: Lockless Design)
	// When enabled, replaces timer-driven Tick() with continuous event loop
	useEventLoop          bool
	eventLoopRateInterval time.Duration
	backoffColdStartPkts  int
	backoffMinSleep       time.Duration
	backoffMaxSleep       time.Duration

	// Time provider for testability (Phase 9: ACK Optimization)
	// Defaults to time.Now().UnixMicro(). In tests, this can be replaced
	// with a mock to enable deterministic TSBPD delivery testing.
	nowFn func() uint64

	// Debug logging
	debug   bool
	logFunc func(string, func() string)

	// Debug context tracking (Step 7.5.2: Runtime Verification)
	// Only active in debug builds (-tags debug), zero-size struct in release builds.
	debugCtx debugContext

	// Callback to process connection-level control packets (ACKACK, KEEPALIVE)
	// Set via SetProcessConnectionControlPackets() after receiver is created.
	// Called by EventLoop to process control packets inline, eliminating polling latency.
	processConnectionControlPackets func() int
}

// New takes a Config and returns a new Receiver.
func New(recvConfig Config) congestion.Receiver {
	// Choose packet store implementation based on recvConfig
	var store packetStore
	if recvConfig.PacketReorderAlgorithm == "btree" {
		degree := recvConfig.BTreeDegree
		if degree <= 0 {
			degree = 32 // Default btree degree
		}
		store = NewBTreePacketStore(degree)
	} else {
		// Default to list implementation
		store = NewListPacketStore()
	}

	r := &receiver{
		maxSeenSequenceNumber: recvConfig.InitialSequenceNumber.Dec(),
		lastACKSequenceNumber: recvConfig.InitialSequenceNumber.Dec(),
		// Note: lastDeliveredSequenceNumber removed - using contiguousPoint instead (Phase 4)
		packetStore: store,
		lockTiming:  recvConfig.LockTimingMetrics,
		metrics:     recvConfig.ConnectionMetrics,
		bufferPool:  recvConfig.BufferPool, // Phase 2: zero-copy support

		periodicACKInterval: recvConfig.PeriodicACKInterval,
		periodicNAKInterval: recvConfig.PeriodicNAKInterval,

		// avgPayloadSize initialized via atomic below

		sendACK: recvConfig.OnSendACK,
		sendNAK: recvConfig.OnSendNAK,
		deliver: recvConfig.OnDeliver,

		// NAK btree configuration
		useNakBtree:            recvConfig.UseNakBtree,
		suppressImmediateNak:   recvConfig.SuppressImmediateNak,
		tsbpdDelay:             recvConfig.TsbpdDelay,
		nakRecentPercent:       recvConfig.NakRecentPercent,
		nakMergeGap:            recvConfig.NakMergeGap,
		nakConsolidationBudget: defaultNakConsolidationBudget(recvConfig.NakConsolidationBudget),

		// FastNAK configuration
		fastNakEnabled:       recvConfig.FastNakEnabled,
		fastNakThreshold:     time.Duration(recvConfig.FastNakThresholdUs) * time.Microsecond,
		fastNakRecentEnabled: recvConfig.FastNakRecentEnabled,

		// NAK btree expiry configuration (nak_btree_expiry_optimization.md)
		nakExpiryMargin:     recvConfig.NakExpiryMargin,
		ewmaWarmupThreshold: recvConfig.EWMAWarmupThreshold,
	}

	// Create NAK btree if enabled
	if r.useNakBtree {
		degree := recvConfig.BTreeDegree
		if degree <= 0 {
			degree = 32 // Default btree degree
		}
		r.nakBtree = newNakBtree(degree)

		// Setup NAK btree function dispatch based on execution mode
		// This is configured once at startup for zero runtime overhead
		r.setupNakDispatch(recvConfig.UsePacketRing)
	}

	// Initialize lock-free ring buffer if enabled (Phase 3: Lockless Design)
	if recvConfig.UsePacketRing {
		r.usePacketRing = true

		// Calculate total capacity (per-shard size * number of shards)
		ringSize := recvConfig.PacketRingSize
		if ringSize <= 0 {
			ringSize = 1024 // Default per-shard capacity
		}
		numShards := recvConfig.PacketRingShards
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
			Strategy:           parseRetryStrategy(recvConfig.PacketRingRetryStrategy),
			MaxRetries:         recvConfig.PacketRingMaxRetries,
			BackoffDuration:    recvConfig.PacketRingBackoffDuration,
			MaxBackoffs:        recvConfig.PacketRingMaxBackoffs,
			MaxBackoffDuration: 10 * time.Millisecond, // Cap for AdaptiveBackoff/Hybrid
			BackoffMultiplier:  2.0,                   // Exponential growth factor
			// AutoAdaptive strategy options (go-lock-free-ring v1.0.4)
			AdaptiveIdleIterations:   100000,                 // Iterations before Yield→Sleep
			AdaptiveWarmupIterations: 1000,                   // Skip idle count after success
			AdaptiveSleepDuration:    100 * time.Microsecond, // Sleep duration in Sleep mode
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
	if recvConfig.UseEventLoop {
		r.useEventLoop = true
		r.eventLoopRateInterval = recvConfig.EventLoopRateInterval
		if r.eventLoopRateInterval <= 0 {
			r.eventLoopRateInterval = 1 * time.Second // Default: 1s
		}
		r.backoffColdStartPkts = recvConfig.BackoffColdStartPkts
		if r.backoffColdStartPkts <= 0 {
			r.backoffColdStartPkts = 1000 // Default: 1000 packets
		}
		r.backoffMinSleep = recvConfig.BackoffMinSleep
		if r.backoffMinSleep <= 0 {
			r.backoffMinSleep = 10 * time.Microsecond // Default: 10µs
		}
		r.backoffMaxSleep = recvConfig.BackoffMaxSleep
		if r.backoffMaxSleep <= 0 {
			r.backoffMaxSleep = 1 * time.Millisecond // Default: 1ms
		}
	}

	// Debug logging configuration
	if recvConfig.Debug {
		r.debug = true
		r.logFunc = recvConfig.LogFunc
	}

	// Initialize control packet processing callback (for EventLoop mode)
	// This allows connection-level control packets to be processed inline
	// in the EventLoop rather than by a separate polling goroutine.
	r.processConnectionControlPackets = recvConfig.ProcessConnectionControlPackets

	// Initialize time provider for TSBPD delivery (Phase 9: ACK Optimization)
	// Phase 10 Fix: When TsbpdTimeBase/StartTime are provided, use relative time
	// matching the PktTsbpdTime calculation in connection.go.
	//
	// Without this fix, EventLoop mode uses absolute Unix time (~1.7 trillion µs)
	// while PktTsbpdTime uses relative time (~millions µs), causing ALL packets
	// to appear TSBPD-expired immediately.
	if !recvConfig.StartTime.IsZero() {
		// Use relative time matching PktTsbpdTime calculation
		start := recvConfig.StartTime
		base := recvConfig.TsbpdTimeBase
		r.nowFn = func() uint64 {
			return base + uint64(time.Since(start).Microseconds())
		}
	} else {
		// Default: absolute time (backward compatible for tests that override nowFn)
		r.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }
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
	r.avgPayloadSizeBits.Store(math.Float64bits(DefaultPayloadSizeBytes))
	// avgLinkCapacity starts at 0

	// Initialize contiguousPoint to ISN.Dec() (Phase 6/14: ACK Optimization)
	// This matches lastACKSequenceNumber initialization (line ~282).
	//
	// CRITICAL: Must be ISN-1, NOT ISN, because:
	// - contiguousScan() skips packets <= contiguousPoint
	// - If contiguousPoint = ISN, the FIRST packet at ISN would be skipped!
	// - With contiguousPoint = ISN-1, packet at ISN is found contiguous
	//
	// Example: ISN=0
	// - contiguousPoint = -1 (MAX_SEQUENCENUMBER due to wraparound)
	// - First packet at seq=0 arrives
	// - contiguousScan: expected = (-1)+1 = 0, seq=0, contiguous!
	// - Returns ACK = 1 (next expected)
	r.contiguousPoint.Store(recvConfig.InitialSequenceNumber.Dec().Val())

	// Initialize Light ACK tracking (Phase 5: ACK Optimization)
	// Use difference-based approach instead of counter-based
	lightACKDiff := recvConfig.LightACKDifference
	if lightACKDiff == 0 {
		lightACKDiff = 64 // RFC default
	}
	r.lightACKDifference = lightACKDiff
	// Initialize lastLightACKSeq from lastACKSequenceNumber (NOT ISN)
	// This ensures the initial difference is 0, not a huge number due to Dec()
	r.lastLightACKSeq = r.lastACKSequenceNumber.Val()

	// Initialize debug context (Step 7.5.2: Runtime Verification)
	// No-op in release builds, enables assertions in debug builds.
	r.initDebugContext()

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

// insertAndUpdateMetrics inserts a packet into the store and updates metrics.
// This consolidates the common Insert + metrics pattern used throughout the codebase.
//
// Parameters:
//   - p: packet to insert
//   - pktLen: packet length (pre-computed by caller for efficiency, uint64 since length is never negative)
//   - isRetransmit: whether this is a retransmitted packet (for retrans metrics)
//   - updateDrainMetric: whether to increment RingDrainedPackets (for ring drain paths)
//
// Returns true if packet was inserted (unique), false if duplicate.
// On duplicate: increments failure metrics and releases the duplicate packet.
func (r *receiver) insertAndUpdateMetrics(p packet.Packet, pktLen uint64, isRetransmit bool, updateDrainMetric bool) bool {
	m := r.metrics

	// Insert returns (inserted, duplicatePacket)
	// On duplicate: new packet stays in tree, old packet is returned for release
	inserted, dupPkt := r.packetStore.Insert(p)
	if inserted {
		if updateDrainMetric {
			m.RingDrainedPackets.Add(1)
		}
		m.CongestionRecvPktBuf.Add(1)
		m.CongestionRecvPktUnique.Add(1)
		m.CongestionRecvByteBuf.Add(pktLen)
		m.CongestionRecvByteUnique.Add(pktLen)
		m.CongestionRecvPkt.Add(1)
		m.CongestionRecvByte.Add(pktLen)

		if isRetransmit {
			m.CongestionRecvPktRetrans.Add(1)
			m.CongestionRecvByteRetrans.Add(pktLen)
		}
		return true
	}

	// Duplicate - update failure metrics and release the old packet that was kicked out
	m.CongestionRecvPktStoreInsertFailed.Add(1)
	m.CongestionRecvPktDuplicate.Add(1)
	m.CongestionRecvByteDuplicate.Add(pktLen)
	metrics.IncrementRecvDataDrop(m, metrics.DropReasonStoreInsertFailed, pktLen)
	r.releasePacketFully(dupPkt) // Release the OLD packet (kicked out of tree)
	return false
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

// setupNakDispatch configures NAK btree function dispatch based on execution mode.
// Called once at receiver initialization for zero runtime overhead.
//
// In event loop mode (usePacketRing=true):
//   - After draining ring, Tick has exclusive access to nakBtree
//   - Use lock-free versions for maximum performance
//
// In tick mode (usePacketRing=false):
//   - Push and Tick can run concurrently
//   - Use locking versions for thread safety
func (r *receiver) setupNakDispatch(usePacketRing bool) {
	if r.nakBtree == nil {
		return
	}

	if usePacketRing {
		// Event loop mode: lock-free (single-threaded after ring drain)
		r.nakInsert = r.nakBtree.Insert
		r.nakInsertBatch = r.nakBtree.InsertBatch
		r.nakDelete = r.nakBtree.Delete
		r.nakDeleteBefore = r.nakBtree.DeleteBefore
		r.nakLen = r.nakBtree.Len
		// TSBPD-aware methods (nak_btree_expiry_optimization.md)
		r.nakInsertBatchWithTsbpd = r.nakBtree.InsertBatchWithTsbpd
		r.nakDeleteBeforeTsbpd = r.nakBtree.DeleteBeforeTsbpd
		r.nakIterateAndUpdate = r.nakBtree.IterateAndUpdate
	} else {
		// Tick mode: locking (concurrent Push/Tick safety)
		r.nakInsert = r.nakBtree.InsertLocking
		r.nakInsertBatch = r.nakBtree.InsertBatchLocking
		r.nakDelete = r.nakBtree.DeleteLocking
		r.nakDeleteBefore = r.nakBtree.DeleteBeforeLocking
		r.nakLen = r.nakBtree.LenLocking
		// TSBPD-aware methods (nak_btree_expiry_optimization.md)
		r.nakInsertBatchWithTsbpd = r.nakBtree.InsertBatchWithTsbpdLocking
		r.nakDeleteBeforeTsbpd = r.nakBtree.DeleteBeforeTsbpdLocking
		r.nakIterateAndUpdate = r.nakBtree.IterateAndUpdateLocking
	}
}
