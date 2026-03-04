package receive

// tick.go - Tick, and delivery functions
// Extracted from receiver.go for better organization

import (
	"fmt"
	"math"
	"strings"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (r *receiver) Tick(now uint64) {
	// Step 7.5.2: Runtime Verification (Debug Mode)
	// Track that we're in Tick context - panics if EventLoop is active.
	// No-op in release builds.
	r.EnterTick()
	defer r.ExitTick()

	// Phase 3/4: Drain ring buffer before processing (if enabled)
	// Uses delta-based drain for precise control: received - processed = in ring
	// This transfers packets from lock-free ring into btree for ordered processing.
	// After drain, Tick() has exclusive access to btree - no locks needed.
	if r.usePacketRing {
		r.drainRingByDelta()
	}

	// NAK FIRST: Detect gaps BEFORE ACK advances contiguousPoint with TSBPD skip.
	// This is critical for the unified contiguousPoint approach:
	// - ACK uses TSBPD skip to advance contiguousPoint past expired packets
	// - If ACK runs first, NAK would miss gaps in expired regions
	// - Running NAK first ensures gaps are detected before ACK skips them
	//
	// NAK: Call locked wrapper when using NAK btree (Tick mode needs lock).
	// - periodicNakBtreeLocked() acquires r.lock.RLock for btree access
	// - periodicNakOriginal() has internal locking
	// EventLoop mode calls periodicNAK() directly (single-threaded, no lock needed).
	var list []circular.Number
	if r.useNakBtree {
		list = r.periodicNakBtreeLocked(now)
	} else {
		list = r.periodicNAK(now)
	}
	if len(list) != 0 {
		// Count NAK entries using shared helper before sending.
		// This ensures 100% consistency with how the sender counts received NAKs.
		// RFC SRT Appendix A:
		//   - Figure 21: Single (start == end) - 4 bytes on wire
		//   - Figure 22: Range (start != end) - 8 bytes on wire
		metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
		r.sendNAK(list)
	}

	// Phase 10: Use the new locked wrapper for ACK.
	// periodicACKLocked() internally uses contiguousScanWithTime() which has:
	// - TSBPD skip logic (advances contiguousPoint past expired packets)
	// - STALE contiguousPoint handling via threshold-based detection
	// - 31-bit wraparound safety using circular.Seq* functions
	// - Interval checks, Light ACK logic, and metrics
	//
	// ACK runs AFTER NAK so gaps are detected before TSBPD skip advances past them.
	// Note: Delivery depends on lastACKSequenceNumber, so ACK must still run before delivery.
	if ok, sequenceNumber, lite := r.periodicACKLocked(now); ok {
		r.sendACK(sequenceNumber, lite)
	}

	// Expire NAK btree entries AFTER NAK is sent - not time-critical
	// This was moved from periodicNakBtree() to keep it out of the hot path.
	// We have 10-20ms until next Tick/periodicNAK cycle.
	if r.useNakBtree && r.nakBtree != nil {
		r.expireNakEntries()
	}

	// Phase 10 (ACK Optimization): Use deliverReadyPacketsLocked() instead of inline code
	// Note: Delivery MUST happen AFTER ACK - packets require lastACKSequenceNumber to be set.
	// The original plan to reorder delivery before ACK was incorrect due to this dependency.
	r.deliverReadyPacketsLocked(now)

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

// processOnePacket consumes one packet from the ring and inserts into btree.
// Returns true if a packet was processed (or rejected as duplicate).
// Called by EventLoop's default case for continuous processing.
func (r *receiver) processOnePacket() bool {
	// Step 7.5.2: Assert EventLoop context (no-op in release builds)
	r.AssertEventLoopContext()

	if r.packetRing == nil {
		return false
	}

	// TryRead is non-blocking - returns immediately if ring is empty
	item, ok := r.packetRing.TryRead()
	if !ok {
		return false // Ring empty
	}

	p, ok := item.(packet.Packet)
	if !ok {
		return false // Invalid item
	}
	h := p.Header()
	seq := h.PacketSequenceNumber
	pktLen := p.Len()
	m := r.metrics

	// RingPacketsProcessed = ALL packets read from ring (for delta calculation)
	// This is incremented unconditionally when a packet is read from the ring
	if m != nil {
		m.RingPacketsProcessed.Add(1)
	}

	// Duplicate/old packet check - Phase 4 change
	// Too old: already past contiguousPoint
	if circular.SeqLessOrEqual(seq.Val(), r.contiguousPoint.Load()) {
		m.CongestionRecvPktBelated.Add(1)
		m.CongestionRecvByteBelated.Add(uint64(pktLen))
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
		r.releasePacketFully(p)
		return true // Still processed (rejected)
	}

	// Already acknowledged
	if seq.Lt(r.lastACKSequenceNumber) {
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonAlreadyAcked, pktLen)
		r.releasePacketFully(p)
		return true
	}

	// Duplicate: already in store
	if r.packetStore.Has(seq) {
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonDuplicate, pktLen)
		r.releasePacketFully(p)
		return true
	}

	// Delete from NAK btree - this packet is no longer missing
	// Use DeleteLocking() because this is called from tick() path (not event loop)
	if r.nakBtree != nil {
		if r.nakDelete(seq.Val()) {
			m.NakBtreeDeletes.Add(1)
		}
	}

	// Insert into btree using consolidated helper (NO LOCK - exclusive access in event loop)
	r.insertAndUpdateMetrics(p, pktLen, h.RetransmittedPacketFlag, true /* updateDrainMetric */)

	return true
}

// ═══════════════════════════════════════════════════════════════════════════
// Phase 9: Delivery Functions (ACK Optimization)
// ═══════════════════════════════════════════════════════════════════════════

// deliverReadyPacketsWithTime delivers all packets whose TSBPD time <= now.
// This is the core function - no locking inside.
// Called every EventLoop/Tick iteration for smooth, non-bursty delivery.
// Returns the count of packets delivered.
//
// Context: This is a SHARED function called from both:
//   - EventLoop: via deliverReadyPackets() - no lock needed (single-threaded btree access)
//   - Tick: via deliverReadyPacketsLocked() - lock acquired by caller
//
// No context assertion here because this function works correctly in either mode.
func (r *receiver) deliverReadyPacketsWithTime(now uint64) int {
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
			m.CongestionRecvPktBuf.Add(^uint64(0))     // Decrement by 1
			m.CongestionRecvByteBuf.Add(^(pktLen - 1)) // Subtract pktLen

			// Note: lastDeliveredSequenceNumber removed in Phase 4 - using contiguousPoint instead
			// contiguousPoint is already updated by contiguousScan()

			// Deliver to application
			r.deliver(p)
			delivered++
		},
	)
	_ = removed

	return delivered
}

// deliverReadyPackets delivers packets using nowFn for current time.
// This is for EventLoop where we want to use injectable time.
func (r *receiver) deliverReadyPackets() int {
	return r.deliverReadyPacketsWithTime(r.nowFn())
}

// deliverReadyPacketsLocked wraps deliverReadyPacketsWithTime with write lock (for Tick()-based mode)
// Takes `now` parameter to match the time used by other Tick() operations.
func (r *receiver) deliverReadyPacketsLocked(now uint64) int {
	if r.lockTiming != nil {
		var delivered int
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			delivered = r.deliverReadyPacketsWithTime(now)
		})
		return delivered
	}
	r.lock.Lock()
	delivered := r.deliverReadyPacketsWithTime(now)
	r.lock.Unlock()
	return delivered
}

// deliverReadyPacketsNoLock is a backward-compatible wrapper for EventLoop callers.
// Deprecated - prefer deliverReadyPackets().
func (r *receiver) deliverReadyPacketsNoLock(now uint64) int {
	return r.deliverReadyPacketsWithTime(now)
}

// ═══════════════════════════════════════════════════════════════════════════
// End Phase 9: Delivery Functions
// ═══════════════════════════════════════════════════════════════════════════

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

// SetRTTProvider sets the RTT provider for NAK suppression.
// Called during connection setup after the connection's RTT tracker is configured.
// Phase 6: RTO Suppression - enables RTO-based NAK suppression in consolidateNakBtree().
func (r *receiver) SetRTTProvider(rtt congestion.RTTProvider) {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.rtt = rtt
}

// SetProcessConnectionControlPackets sets the callback for processing
// connection-level control packets (ACKACK, KEEPALIVE) in EventLoop mode.
// Called by connection.go after receiver is created and recvControlRing is initialized.
// The callback should be c.drainRecvControlRing which processes control packets
// inline in the EventLoop, eliminating the ~100µs polling latency.
func (r *receiver) SetProcessConnectionControlPackets(fn func() int) {
	r.processConnectionControlPackets = fn
}

func (r *receiver) String(t uint64) string {
	var b strings.Builder

	// Note: lastDelivered replaced with contiguousPoint in Phase 4
	b.WriteString(fmt.Sprintf("maxSeen=%d lastACK=%d contiguousPoint=%d\n", r.maxSeenSequenceNumber.Val(), r.lastACKSequenceNumber.Val(), r.contiguousPoint.Load()))

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
