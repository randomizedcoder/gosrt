package receive

// tick.go - Tick, EventLoop, and delivery functions
// Extracted from receiver.go for better organization

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

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
	// NAK: Use periodicNAK() which dispatches to the correct implementation:
	// - periodicNakBtree() when useNakBtree=true (has NAK btree, merge gap, etc.)
	// - periodicNakOriginal() otherwise
	if list := r.periodicNAK(now); len(list) != 0 {
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

	// Step 7.5.2: Runtime Verification (Debug Mode)
	// Track that we're in EventLoop context - panics if Tick is active.
	// No-op in release builds.
	r.EnterEventLoop()
	defer r.ExitEventLoop()

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

	// Phase 11 (ACK Optimization): Periodic FULL ACK ticker
	// Light ACKs are sent continuously based on LightACKDifference (every 64 packets).
	// But Full ACKs are still needed periodically for RTT calculation because:
	// - Light ACKs don't trigger ACKACK (no RTT info)
	// - Without RTT, sender pacing is wrong → packets arrive late → drops
	// The Full ACK ticker ensures RTT is calculated every 10ms.
	fullACKTicker := time.NewTicker(ackInterval)
	defer fullACKTicker.Stop()

	// Offset NAK ticker by half of ACK interval to spread work evenly
	// This prevents ACK and NAK from firing at the same time, reducing CPU spikes.
	// With 10ms ACK and 20ms NAK: Full ACK fires at 0, 10, 20, ...
	//                            NAK fires at 5, 25, 45, ...
	// See gosrt_lockless_design.md Section 9.3.1 "Solution: Offset Tickers"
	time.Sleep(ackInterval / 2)

	// NAK ticker remains - gap detection is still timer-based
	nakTicker := time.NewTicker(nakInterval)
	defer nakTicker.Stop()

	// Offset rate ticker to further spread work
	// Full ACK fires at 0, 10, 20, ...
	// NAK fires at 5, 25, 45, ...
	// Rate fires at 7.5, 1007.5, ... (ackInterval/4 after NAK)
	time.Sleep(ackInterval / 4)

	// Rate ticker for statistics
	rateTicker := time.NewTicker(rateInterval)
	defer rateTicker.Stop()

	for {
		// Phase 4 (ACK/ACKACK Redesign): Track EventLoop iterations for diagnostics
		r.metrics.EventLoopIterations.Add(1)

		// Handle tickers first - these are time-critical periodic operations
		select {
		case <-ctx.Done():
			return

		case <-fullACKTicker.C:
			r.metrics.EventLoopFullACKFires.Add(1)
			// Periodic Full ACK for RTT calculation
			// Light ACKs (sent continuously) don't trigger ACKACK, so without periodic
			// Full ACKs, RTT would never be calculated and sender pacing would be wrong.
			//
			// This runs contiguousScan to get the latest ACK sequence, then sends a
			// Full ACK (lite=false) which triggers ACKACK from the sender.
			r.drainRingByDelta()
			if ok, newContiguous := r.contiguousScan(); ok {
				r.lastACKSequenceNumber = circular.New(newContiguous, packet.MAX_SEQUENCENUMBER)
				r.sendACK(circular.New(circular.SeqAdd(newContiguous, 1), packet.MAX_SEQUENCENUMBER), false) // Full ACK
				r.lastLightACKSeq = newContiguous
			} else {
				// No progress from scan, but MUST still update lastACKSequenceNumber
				// to enable packet delivery. Without this, packets accumulate in btree
				// but can't be delivered because deliverReadyPackets() checks:
				//   seq <= lastACKSequenceNumber
				//
				// BUG FIX (2025-12-26): Previously this else branch only sent ACK
				// but didn't update lastACKSequenceNumber, causing packets to expire
				// via TSBPD before delivery could happen → drops!
				currentSeq := r.contiguousPoint.Load()
				if currentSeq > 0 {
					r.lastACKSequenceNumber = circular.New(currentSeq, packet.MAX_SEQUENCENUMBER)
					r.sendACK(circular.New(circular.SeqAdd(currentSeq, 1), packet.MAX_SEQUENCENUMBER), false) // Full ACK
				}
			}

		case <-nakTicker.C:
			r.metrics.EventLoopNAKFires.Add(1)
			// CRITICAL: Drain ring before NAK scan to avoid false gaps
			// Without this, packets sitting in the ring appear as "gaps" in the btree,
			// causing spurious NAKs even when no packets are actually lost.
			// Uses delta-based drain for precise control: received - processed = in ring
			r.drainRingByDelta()
			// Use r.nowFn() for consistent time base with PktTsbpdTime (relative to connection start)
			// BUG FIX: time.Now().UnixMicro() was absolute time, causing tooRecentThreshold
			// to be ~1.7e12. All packets appeared "not too recent" → excessive NAKing.
			now := r.nowFn()
			if list := r.periodicNAK(now); len(list) != 0 {
				metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
				r.sendNAK(list)
			}
			// Expire NAK btree entries after NAK is sent
			if r.useNakBtree && r.nakBtree != nil {
				r.expireNakEntries()
			}

		case <-rateTicker.C:
			r.metrics.EventLoopRateFires.Add(1)
			// Use r.nowFn() for consistent time base with rest of EventLoop
			// BUG FIX: time.Now().UnixMicro() was absolute time (~1.7e12),
			// causing RecvRateLastUs to be a massive number instead of
			// relative connection time. This made rate calculations incorrect
			// on first period (dividing by ~56 years instead of ~1 second).
			now := r.nowFn()
			r.updateRateStats(now)

		default:
			// No ticker fired - fall through to packet processing below
		}

		// =====================================================================
		// Packet Processing (runs every iteration, not just when no ticker fires)
		// =====================================================================
		// Refactored: Moved from default: case to run after select.
		// Benefits:
		//   1. Less nesting - code moves left (Go idiom)
		//   2. Processing always runs, even when a ticker fires
		//   3. Clearer separation: tickers handle time-critical ops, this handles packets
		// =====================================================================

		r.metrics.EventLoopDefaultRuns.Add(1)

		// Deliver ready packets first to shrink btree
		// Note: Delivery depends on lastACKSequenceNumber being set (by contiguousScan)
		delivered := r.deliverReadyPackets()

		// Process one packet from ring into btree
		processed := r.processOnePacket()

		// Continuous ACK scan with Light ACK difference
		// This replaces the ticker-based periodicACK - called every iteration
		ok, newContiguous := r.contiguousScan()
		if ok {
			// Check if we've advanced enough to send an ACK
			diff := circular.SeqSub(newContiguous, r.lastLightACKSeq)
			if diff >= r.lightACKDifference {
				// Determine ACK type: Light vs Full (Force Full on massive jump)
				//
				// Rationale: If contiguousPoint jumps by a large amount (e.g., 500 packets
				// when a large gap is filled), sending just a Light ACK loses valuable info.
				// A Full ACK is more valuable here because it:
				//   1. Updates the sender's congestion window immediately
				//   2. Provides fresh RTT information after recovery
				//   3. Triggers ACKACK for accurate RTT measurement
				//
				// Threshold: 4x the LightACKDifference (e.g., 256 packets if diff=64)
				forceFullACK := diff >= (r.lightACKDifference * 4)
				lite := !forceFullACK

				// Update lastACKSequenceNumber for delivery check
				r.lastACKSequenceNumber = circular.New(newContiguous, packet.MAX_SEQUENCENUMBER)

				// Send ACK (newContiguous + 1 per SRT spec: "next expected sequence")
				r.sendACK(circular.New(circular.SeqAdd(newContiguous, 1), packet.MAX_SEQUENCENUMBER), lite)
				r.lastLightACKSeq = newContiguous
			}
		}

		// Adaptive backoff when idle
		if !processed && delivered == 0 && !ok {
			r.metrics.EventLoopIdleBackoffs.Add(1)
			// No work done - sleep to avoid CPU spin
			time.Sleep(backoff.getSleepDuration())
		} else {
			// Activity recorded - reset backoff
			backoff.recordActivity()
		}
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
			m.CongestionRecvPktBuf.Add(^uint64(0))                   // Decrement by 1
			m.CongestionRecvByteBuf.Add(^uint64(uint64(pktLen) - 1)) // Subtract pktLen

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
