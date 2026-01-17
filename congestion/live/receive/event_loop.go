package receive

// event_loop.go - EventLoop
// Extracted from receiver.go for better organization

import (
	"context"
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

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
func (r *receiver) EventLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	// DEBUG: Track EventLoop entry
	if r.metrics != nil {
		r.metrics.EventLoopEntered.Add(1)
	}

	if !r.useEventLoop {
		// DEBUG: Track early return reason
		if r.metrics != nil {
			r.metrics.EventLoopExitedEarly.Add(1)
		}
		return // Event loop not enabled
	}
	if r.packetRing == nil {
		// DEBUG: Track early return reason
		if r.metrics != nil {
			r.metrics.EventLoopExitedNoRing.Add(1)
		}
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

		// ═══════════════════════════════════════════════════════════════════════
		// CONTROL PACKET PRIORITY PATTERN
		// Service control packets BEFORE and AFTER each major action to minimize
		// ACKACK/KEEPALIVE latency. This ensures RTT measurements are accurate.
		// Reference: sender_lockfree_implementation_plan.md Phase 5
		// ═══════════════════════════════════════════════════════════════════════

		// Track total control packets for diagnostics
		totalControlProcessed := 0

		// ──────────────────────────────────────────────────────────────────────
		// 1. SERVICE CONTROL FIRST (minimize ACKACK latency)
		// ──────────────────────────────────────────────────────────────────────
		totalControlProcessed += r.processControlPacketsWithMetrics()

		// ──────────────────────────────────────────────────────────────────────
		// 2. Handle tickers - time-critical periodic operations
		// ──────────────────────────────────────────────────────────────────────
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

		// ──────────────────────────────────────────────────────────────────────
		// 3. SERVICE CONTROL AFTER TICKERS (may have arrived during ticker handling)
		// ──────────────────────────────────────────────────────────────────────
		totalControlProcessed += r.processControlPacketsWithMetrics()

		// ──────────────────────────────────────────────────────────────────────
		// 4. Drain data ring → btree
		// ──────────────────────────────────────────────────────────────────────
		r.drainRingByDelta()

		// ──────────────────────────────────────────────────────────────────────
		// 5. SERVICE CONTROL AFTER DRAIN (may have arrived during drain)
		// ──────────────────────────────────────────────────────────────────────
		totalControlProcessed += r.processControlPacketsWithMetrics()

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

		// ──────────────────────────────────────────────────────────────────────
		// 6. Deliver ready packets (TSBPD)
		// Note: Delivery depends on lastACKSequenceNumber being set (by contiguousScan)
		// ──────────────────────────────────────────────────────────────────────
		delivered := r.deliverReadyPackets()

		// ──────────────────────────────────────────────────────────────────────
		// 7. SERVICE CONTROL AFTER DELIVERY (may have arrived during delivery)
		// ──────────────────────────────────────────────────────────────────────
		totalControlProcessed += r.processControlPacketsWithMetrics()

		// ──────────────────────────────────────────────────────────────────────
		// 8. Process one packet from ring into btree
		// ──────────────────────────────────────────────────────────────────────
		processed := r.processOnePacket()

		// ──────────────────────────────────────────────────────────────────────
		// 9. Continuous ACK scan with Light ACK difference
		// ──────────────────────────────────────────────────────────────────────
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

		// ──────────────────────────────────────────────────────────────────────
		// 10. Adaptive backoff when idle
		// ──────────────────────────────────────────────────────────────────────
		// Include control packet processing in activity check
		if !processed && delivered == 0 && !ok && totalControlProcessed == 0 {
			r.metrics.EventLoopIdleBackoffs.Add(1)
			// No work done - sleep to avoid CPU spin
			time.Sleep(backoff.getSleepDuration())
		} else {
			// Activity recorded - reset backoff
			backoff.recordActivity()
		}
	}
}

// processControlPacketsWithMetrics processes control packets and updates metrics.
// Returns count of packets processed. Helper to reduce code duplication.
func (r *receiver) processControlPacketsWithMetrics() int {
	if r.processConnectionControlPackets == nil {
		return 0
	}
	n := r.processConnectionControlPackets()
	if n > 0 && r.metrics != nil {
		r.metrics.EventLoopControlProcessed.Add(uint64(n))
	}
	return n
}
