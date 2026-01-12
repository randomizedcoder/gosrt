package receive

// ring.go - Lock-free ring buffer functions
// Extracted from receiver.go for better organization

import (
	"fmt"
	"strings"
	"sync"
	"time"

	ring "github.com/randomizedcoder/go-lock-free-ring"
	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func parseRetryStrategy(s string) ring.RetryStrategy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "next", "nextshard":
		return ring.NextShard
	case "random", "randomshard":
		return ring.RandomShard
	case "adaptive", "adaptivebackoff":
		return ring.AdaptiveBackoff
	case "spin", "yield", "spinthenyield":
		return ring.SpinThenYield
	case "hybrid":
		return ring.Hybrid
	default:
		// "", "sleep", "sleepbackoff", or unknown -> default
		return ring.SleepBackoff
	}
}

// Config is the configuration for the liveRecv congestion control
type Config struct {
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

	// NAK btree expiry configuration (nak_btree_expiry_optimization.md)
	NakExpiryMargin     float64 // Margin for expiry: threshold = now + RTO*(1+margin), default 0.10
	EWMAWarmupThreshold uint32  // Min samples before EWMA trusted (0=disabled), default 32

	// Light ACK configuration (Phase 5: ACK Optimization)
	LightACKDifference uint32 // Send Light ACK after N packets progress (default: 64)

	// Lock-free ring buffer configuration (Phase 3: Lockless Design)
	// When enabled, Push() writes to ring (lock-free), Tick() drains ring before processing
	UsePacketRing             bool          // Enable lock-free ring for packet handoff
	PacketRingSize            int           // Ring capacity per shard (must be power of 2)
	PacketRingShards          int           // Number of shards (must be power of 2)
	PacketRingMaxRetries      int           // Max immediate retries before backoff
	PacketRingBackoffDuration time.Duration // Delay between backoff retries
	PacketRingMaxBackoffs     int           // Max backoff iterations (0 = unlimited)
	PacketRingRetryStrategy   string        // Retry strategy: "", "sleep", "next", "random", "adaptive", "spin", "hybrid"

	// Event loop configuration (Phase 4: Lockless Design)
	// When enabled, replaces timer-driven Tick() with continuous event loop
	// REQUIRES: UsePacketRing=true (event loop consumes from ring)
	UseEventLoop          bool          // Enable continuous event loop
	EventLoopRateInterval time.Duration // Rate metric calculation interval (default: 1s)
	BackoffColdStartPkts  int           // Packets before adaptive backoff engages
	BackoffMinSleep       time.Duration // Minimum sleep during idle periods
	BackoffMaxSleep       time.Duration // Maximum sleep during idle periods

	// Time base configuration (Phase 10: EventLoop Time Fix)
	// When set, nowFn returns time relative to connection start, matching PktTsbpdTime.
	// This is REQUIRED for EventLoop mode to correctly handle TSBPD delivery.
	//
	// Without these fields: nowFn returns absolute Unix time (~1.7 trillion µs)
	// With these fields: nowFn returns TsbpdTimeBase + elapsed_since_StartTime
	//
	// PktTsbpdTime is calculated as: tsbpdTimeBase + timestamp + tsbpdDelay
	// For correct TSBPD comparison, nowFn must use the same time base.
	TsbpdTimeBase uint64    // Time base from connection (same as c.tsbpdTimeBase)
	StartTime     time.Time // Connection start time (same as c.start)

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
		// Too old: already past contiguousPoint - Phase 4 change
		if circular.SeqLessOrEqual(seq.Val(), r.contiguousPoint.Load()) {
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
		// Use lock-free Delete() because this runs in single-threaded event loop context
		if r.nakBtree != nil {
			if r.nakDelete(seq.Val()) {
				m.NakBtreeDeletes.Add(1)
			}
		}

		// Insert into btree using consolidated helper (NO LOCK - exclusive access after drain)
		// Note: insertAndUpdateMetrics already updates RingDrainedPackets when updateDrainMetric=true
		pktLen := p.Len() // already uint64
		if r.insertAndUpdateMetrics(p, pktLen, h.RetransmittedPacketFlag, true /* updateDrainMetric */) {
			drainedCount++ // Track locally for debug/return value
		}
	}

	// Single atomic increments at the end (performance optimization)
	// Note: RingDrainedPackets is already updated by insertAndUpdateMetrics
	if m != nil && processedCount > 0 {
		m.RingPacketsProcessed.Add(processedCount)
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

		// Duplicate/old packet check - Phase 4 change
		if circular.SeqLessOrEqual(seq.Val(), r.contiguousPoint.Load()) {
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
		// Use lock-free Delete() because this runs in single-threaded event loop context
		if r.nakBtree != nil {
			if r.nakDelete(seq.Val()) {
				m.NakBtreeDeletes.Add(1)
			}
		}

		// Insert into btree using consolidated helper
		// Note: insertAndUpdateMetrics already updates RingDrainedPackets when updateDrainMetric=true
		if r.insertAndUpdateMetrics(p, pktLen, h.RetransmittedPacketFlag, true /* updateDrainMetric */) {
			drainedCount++
		}
	}

	// Single atomic increments at the end (performance optimization)
	// Note: RingDrainedPackets is already updated by insertAndUpdateMetrics
	if processedCount > 0 {
		m.RingPacketsProcessed.Add(processedCount)
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
