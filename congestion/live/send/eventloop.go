//go:build go1.18

// Package send implements the sender-side congestion control for SRT live mode.
package send

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Sender EventLoop - Lock-Free Continuous Processing
//
// The EventLoop is the ONLY goroutine that accesses SendPacketBtree:
// 1. Drains SendPacketRing → inserts to btree
// 2. Drains SendControlRing → processes ACKs (DeleteBefore) and NAKs (Get + retransmit)
// 3. Delivers ready packets (TSBPD time reached)
// 4. Drops old packets (threshold reached)
//
// This ensures single-threaded btree access with zero locks.
//
// Reference: lockless_sender_design.md Section 7.1
// Reference: lockless_sender_implementation_plan.md Phase 4
// ═══════════════════════════════════════════════════════════════════════════════

// EventLoop runs the continuous sender processing loop.
// REQUIRES: UseSendBtree, UseSendRing, UseSendControlRing all enabled.
//
// The EventLoop is the ONLY goroutine that accesses SendPacketBtree,
// ensuring single-threaded btree access with zero locks.
//
// wg is decremented on exit for graceful shutdown coordination.
func (s *sender) EventLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	m := s.metrics

	// Track EventLoop entry attempts (diagnostic for intermittent failures)
	m.SendEventLoopStartAttempts.Add(1)

	if !s.useEventLoop {
		m.SendEventLoopSkippedDisabled.Add(1)
		return
	}

	// Track successful EventLoop starts
	m.SendEventLoopStarted.Add(1)

	// Step 7.5.2: Runtime Verification (Debug Mode)
	// Track that we're in EventLoop context - panics if Tick is active.
	// No-op in release builds.
	s.EnterEventLoop()
	defer s.ExitEventLoop()

	// ═══════════════════════════════════════════════════════════════════════════
	// CRITICAL: Cleanup on shutdown (see Implementation Note 2)
	// Must drain rings and decommission all packets to prevent leaks.
	// ═══════════════════════════════════════════════════════════════════════════
	defer s.cleanupOnShutdown()

	// Drop ticker (periodic old packet cleanup)
	// Use configured interval (microseconds -> time.Duration)
	dropInterval := time.Duration(s.sendDropIntervalUs) * time.Microsecond
	if dropInterval <= 0 {
		dropInterval = 100 * time.Millisecond // Default: 100ms
	}
	dropTicker := time.NewTicker(dropInterval)
	defer dropTicker.Stop()

	for {
		m.SendEventLoopIterations.Add(1)

		select {
		case <-ctx.Done():
			return // cleanupOnShutdown() called via defer

		case <-dropTicker.C:
			m.SendEventLoopDropFires.Add(1)
			// Use s.nowFn() for consistent time base with PktTsbpdTime
			s.dropOldPacketsEventLoop(s.nowFn())

		default:
			m.SendEventLoopDefaultRuns.Add(1)
		}

		// ═══════════════════════════════════════════════════════════════════════
		// CONTROL PACKET PRIORITY PATTERN
		// Service control packets BEFORE each major action to minimize ACK/NAK latency.
		// This ensures RTT measurements are accurate by processing ACKACKs immediately.
		// Reference: sender_lockfree_architecture.md Section 7.9.1
		// ═══════════════════════════════════════════════════════════════════════

		// Track total control packets for backoff calculation
		totalControlDrained := 0
		var dataDrained int

		// ──────────────────────────────────────────────────────────────────────
		// Drain data ring → btree (with control interleave)
		// ──────────────────────────────────────────────────────────────────────
		if s.maxDataPerIteration > 0 {
			// ═══════════════════════════════════════════════════════════════════
			// TIGHT LOOP MODE (eventloop_batch_sizing_design.md)
			// Control ring is checked after EVERY data packet for minimum latency.
			// At 350 Mb/s: 30,000 checks/sec × 2ns = 0.006% overhead
			// But control latency drops from 1-2ms to ~500ns!
			// ═══════════════════════════════════════════════════════════════════
			dataDrained, totalControlDrained = s.drainRingToBtreeEventLoopTight()
			if dataDrained > 0 {
				m.SendEventLoopDataDrained.Add(uint64(dataDrained))
			}
		} else {
			// ═══════════════════════════════════════════════════════════════════
			// LEGACY UNBOUNDED MODE (for backward compatibility)
			// Control checked 3× per iteration, but data drain is unbounded.
			// ═══════════════════════════════════════════════════════════════════

			// 1. SERVICE CONTROL RING FIRST
			if drained := s.processControlPacketsDelta(); drained > 0 {
				m.SendEventLoopControlDrained.Add(uint64(drained))
				totalControlDrained += drained
			}

			// 2. Drain data ring → btree (unbounded)
			dataDrained = s.drainRingToBtreeEventLoop()
			if dataDrained > 0 {
				m.SendEventLoopDataDrained.Add(uint64(dataDrained))
			}

			// 3. SERVICE CONTROL RING AGAIN
			if drained := s.processControlPacketsDelta(); drained > 0 {
				m.SendEventLoopControlDrained.Add(uint64(drained))
				totalControlDrained += drained
			}
		}

		// ──────────────────────────────────────────────────────────────────────
		// 4. Deliver ready packets (TSBPD) + first transmissions (TransmitCount=0)
		// CRITICAL: Use s.nowFn() for consistent time base with PktTsbpdTime
		// PktTsbpdTime uses relative time (since connection start), so we must too.
		// ──────────────────────────────────────────────────────────────────────
		nowUs := s.nowFn()
		delivered, _ := s.deliverReadyPacketsEventLoop(nowUs)

		// ──────────────────────────────────────────────────────────────────────
		// 5. SERVICE CONTROL RING AFTER DELIVERY (may have arrived during delivery)
		// ──────────────────────────────────────────────────────────────────────
		if drained := s.processControlPacketsDelta(); drained > 0 {
			m.SendEventLoopControlDrained.Add(uint64(drained))
			totalControlDrained += drained
		}

		// ═══════════════════════════════════════════════════════════════════════
		// OPTIMIZATION: Update SendBtreeLen ONCE per iteration (see Note 4)
		// This minimizes atomic overhead - O(1) per iteration instead of O(packets)
		// ═══════════════════════════════════════════════════════════════════════
		if s.packetBtree != nil {
			m.SendBtreeLen.Store(uint64(s.packetBtree.Len()))
		}

		// 6. Adaptive backoff when idle
		// Determine if we did work this iteration
		hadActivity := delivered > 0 || totalControlDrained > 0 || dataDrained > 0

		// ═══════════════════════════════════════════════════════════════════════
		// ADAPTIVE BACKOFF (adaptive_eventloop_mode_design.md)
		//
		// CRITICAL: time.Sleep() has ~1ms minimum granularity on Linux!
		// Even requesting 1µs results in ~1ms actual sleep.
		// This caps EventLoop at ~1000 iter/sec, limiting throughput to ~350 Mb/s.
		//
		// Solution: Adaptive backoff that switches between modes:
		// - YIELD mode: runtime.Gosched() - ~46M iter/sec (high throughput)
		// - SLEEP mode: time.Sleep() - ~1K iter/sec (CPU friendly when idle)
		//
		// Strategy: Start in Yield, switch to Sleep after 1s idle, any activity
		// immediately wakes back to Yield.
		//
		// This handles both:
		// - High throughput (>300 Mb/s): stays in Yield mode
		// - Low throughput (<20 Mb/s): relaxes to Sleep mode, saves CPU
		// ═══════════════════════════════════════════════════════════════════════
		if s.adaptiveBackoff != nil {
			s.adaptiveBackoff.Wait(hadActivity)
			if !hadActivity {
				m.SendEventLoopIdleBackoffs.Add(1)
			}
		} else if !hadActivity {
			// Fallback: simple Gosched when no adaptive backoff configured
			m.SendEventLoopIdleBackoffs.Add(1)
			runtime.Gosched()
		}
	}
}

// cleanupOnShutdown drains rings and decommissions all packets on EventLoop exit.
// CRITICAL: Prevents packet/buffer leaks when connection closes.
// See Implementation Note 2 for design rationale.
func (s *sender) cleanupOnShutdown() {
	m := s.metrics

	// 1. Drain any remaining packets from data ring (return to pool without inserting)
	if s.packetRing != nil {
		for {
			p, ok := s.packetRing.TryPop()
			if !ok {
				break
			}
			p.Decommission() // Return payload buffer to pool
			m.SendRingDrained.Add(1)
		}
	}

	// 2. Decommission all packets remaining in btree
	if s.packetBtree != nil {
		for {
			p := s.packetBtree.DeleteMin()
			if p == nil {
				break
			}
			p.Decommission()
		}
	}

	// 3. Drain control ring (just discard - no buffers to return)
	if s.controlRing != nil {
		for {
			_, ok := s.controlRing.TryPop()
			if !ok {
				break
			}
			m.SendControlRingProcessed.Add(1)
		}
	}

	// Final btree length update
	m.SendBtreeLen.Store(0)
}

// processControlPacketsDelta drains and processes control packets from ring.
// Returns the number of control packets processed.
// Called from EventLoop - NO LOCKING (single-threaded btree access).
func (s *sender) processControlPacketsDelta() int {
	// Step 7.5.2: Assert EventLoop context (no-op in release builds)
	s.AssertEventLoopContext()

	if s.controlRing == nil {
		return 0
	}

	m := s.metrics
	processed := 0

	for {
		cp, ok := s.controlRing.TryPop()
		if !ok {
			break
		}

		m.SendControlRingDrained.Add(1)

		switch cp.Type {
		case ControlTypeACK:
			// Process ACK - remove packets before this sequence
			// NO LOCKING: EventLoop is single consumer of btree
			s.ackBtree(circular.New(cp.ACKSequence, packet.MAX_SEQUENCENUMBER))
			m.SendControlRingProcessed.Add(1)
			m.SendEventLoopACKsProcessed.Add(1)

		case ControlTypeNAK:
			// Convert inline array to slice for processing
			seqs := make([]circular.Number, cp.NAKCount)
			for i := 0; i < cp.NAKCount; i++ {
				seqs[i] = circular.New(cp.NAKSequences[i], packet.MAX_SEQUENCENUMBER)
			}
			// Process NAK - retransmit requested packets
			// NO LOCKING: EventLoop is single consumer of btree
			retransCount := s.nakBtree(seqs)
			m.SendControlRingProcessed.Add(1)
			m.SendEventLoopNAKsProcessed.Add(1)
			// Track actual retransmissions from NAK processing
			// (NAK() returns 0 when routed to ring, so we update here)
			if retransCount > 0 {
				m.PktRetransFromNAK.Add(retransCount)
			}
		}

		processed++
	}

	return processed
}

// drainRingToBtreeEventLoop drains packets from ring to btree.
// Called from EventLoop - NO LOCKING (single-threaded btree access).
func (s *sender) drainRingToBtreeEventLoop() int {
	// Step 7.5.2: Assert EventLoop context (no-op in release builds)
	s.AssertEventLoopContext()

	m := s.metrics

	// Diagnostic: Track drain attempts
	m.SendEventLoopDrainAttempts.Add(1)

	if s.packetRing == nil {
		m.SendEventLoopDrainRingNil.Add(1)
		return 0
	}

	// Diagnostic: Log ring length before drain attempt
	ringLen := s.packetRing.Len()
	if ringLen > 0 {
		m.SendEventLoopDrainRingHadData.Add(1)
	}

	drained := 0

	// Drain all available packets
	for {
		p, ok := s.packetRing.TryPop()
		if !ok {
			if drained == 0 {
				m.SendEventLoopDrainRingEmpty.Add(1)
			}
			break
		}

		pktLen := p.Len()
		m.CongestionSendPktBuf.Add(1)
		m.CongestionSendByteBuf.Add(pktLen)
		m.SendRateBytes.Add(pktLen)

		// Sequence gap detection: detect if we skipped any sequence numbers
		// This helps diagnose the phantom NAK issue where sender skips sequences
		// during Push() → Ring → Btree flow. Non-zero value indicates sender bug.
		currentSeq := uint64(p.Header().PacketSequenceNumber.Val())
		tsbpdTime := p.Header().PktTsbpdTime
		if s.lastInsertedSeqSet.Load() {
			lastSeq := s.lastInsertedSeq.Load()
			// Expected: currentSeq == lastSeq + 1 (modulo 31-bit space)
			// Gap detected if difference > 1 (accounting for wraparound)
			expectedSeq := (lastSeq + 1) & 0x7FFFFFFF // 31-bit wrap
			if currentSeq != expectedSeq {
				// Gap detected! Sender skipped a sequence number.
				// This explains phantom NAKs: receiver correctly NAKs for
				// sequences that were never sent.
				m.SendRingDrainSeqGap.Add(1)
				// Log gap detection for debugging
				if s.log != nil {
					s.log("sender:eventloop:drain:gap", func() string {
						return fmt.Sprintf("SEQ GAP: expected=%d got=%d lastSeq=%d",
							expectedSeq, currentSeq, lastSeq)
					})
				}
			}
		}
		s.lastInsertedSeq.Store(currentSeq)
		s.lastInsertedSeqSet.Store(true)

		// Log packet drain for debugging
		if s.log != nil {
			s.log("sender:eventloop:drain", func() string {
				return fmt.Sprintf("drained seq=%d tsbpdTime=%d", currentSeq, tsbpdTime)
			})
		}

		// Insert into btree - NO LOCKING
		inserted, old := s.packetBtree.Insert(p)
		if !inserted && old != nil {
			m.SendBtreeDuplicates.Add(1)
			old.Decommission()
		}

		m.SendBtreeInserted.Add(1)
		m.SendRingDrained.Add(1)
		drained++
	}

	return drained
}

// drainRingToBtreeEventLoopTight drains packets with control-priority tight loop.
// Checks control ring after EVERY data packet for minimum latency (~500ns).
// Called from EventLoop - NO LOCKING (single-threaded btree access).
//
// Why tight loop? At 350+ Mb/s:
// - Empty control ring check costs ~2ns (one atomic load)
// - 30,000 checks/sec × 2ns = 60µs/sec = 0.006% overhead
// - But control latency drops from ~1-2ms (unbounded) to ~500ns (tight)
//
// Reference: eventloop_batch_sizing_design.md "Tight Loop" section
func (s *sender) drainRingToBtreeEventLoopTight() (int, int) {
	// Step 7.5.2: Assert EventLoop context (no-op in release builds)
	s.AssertEventLoopContext()

	m := s.metrics
	m.SendEventLoopDrainAttempts.Add(1)

	if s.packetRing == nil {
		m.SendEventLoopDrainRingNil.Add(1)
		return 0, 0
	}

	// Diagnostic: Log ring length before drain attempt
	ringLen := s.packetRing.Len()
	if ringLen > 0 {
		m.SendEventLoopDrainRingHadData.Add(1)
	}

	drained := 0
	totalControlDrained := 0

	// Tight loop: check control after EVERY data packet
	for drained < s.maxDataPerIteration {
		// 1. ALWAYS check control first (high priority, ~2ns when empty)
		if controlDrained := s.processControlPacketsDelta(); controlDrained > 0 {
			m.SendEventLoopControlDrained.Add(uint64(controlDrained))
			totalControlDrained += controlDrained
		}

		// 2. Process ONE data packet
		p, ok := s.packetRing.TryPop()
		if !ok {
			if drained == 0 {
				m.SendEventLoopDrainRingEmpty.Add(1)
			}
			break // Data ring empty
		}

		// 3. Update metrics and insert to btree (same as unbounded version)
		pktLen := p.Len()
		m.CongestionSendPktBuf.Add(1)
		m.CongestionSendByteBuf.Add(pktLen)
		m.SendRateBytes.Add(pktLen)

		// Sequence gap detection
		currentSeq := uint64(p.Header().PacketSequenceNumber.Val())
		tsbpdTime := p.Header().PktTsbpdTime
		if s.lastInsertedSeqSet.Load() {
			lastSeq := s.lastInsertedSeq.Load()
			expectedSeq := (lastSeq + 1) & 0x7FFFFFFF
			if currentSeq != expectedSeq {
				m.SendRingDrainSeqGap.Add(1)
				if s.log != nil {
					s.log("sender:eventloop:drain:gap", func() string {
						return fmt.Sprintf("SEQ GAP: expected=%d got=%d lastSeq=%d",
							expectedSeq, currentSeq, lastSeq)
					})
				}
			}
		}
		s.lastInsertedSeq.Store(currentSeq)
		s.lastInsertedSeqSet.Store(true)

		// Log packet drain for debugging
		if s.log != nil {
			s.log("sender:eventloop:drain:tight", func() string {
				return fmt.Sprintf("drained seq=%d tsbpdTime=%d", currentSeq, tsbpdTime)
			})
		}

		// Insert into btree - NO LOCKING
		inserted, old := s.packetBtree.Insert(p)
		if !inserted && old != nil {
			m.SendBtreeDuplicates.Add(1)
			old.Decommission()
		}

		m.SendBtreeInserted.Add(1)
		m.SendRingDrained.Add(1)
		drained++
	}

	// Track if we hit the cap (indicates high load)
	if drained >= s.maxDataPerIteration {
		m.SendEventLoopTightCapReached.Add(1)
	}

	return drained, totalControlDrained
}

// deliverReadyPacketsEventLoop delivers packets whose TSBPD time has passed.
// Returns (delivered count, duration until next packet).
// Called from EventLoop - NO LOCKING (single-threaded btree access).
func (s *sender) deliverReadyPacketsEventLoop(nowUs uint64) (int, time.Duration) {
	// Step 7.5.2: Assert EventLoop context (no-op in release builds)
	s.AssertEventLoopContext()

	m := s.metrics
	m.SendDeliveryAttempts.Add(1)
	m.SendDeliveryLastNowUs.Store(nowUs)

	if s.packetBtree == nil {
		return 0, s.backoffMaxSleep
	}

	// Track btree emptiness
	btreeLen := s.packetBtree.Len()
	if btreeLen == 0 {
		m.SendDeliveryBtreeEmpty.Add(1)
		return 0, s.backoffMaxSleep
	}

	delivered := 0
	var nextDeliveryIn time.Duration
	iterStarted := false

	// Get current delivery start point (avoids re-examining already-delivered packets)
	startSeq := s.deliveryStartPoint.Load()
	m.SendDeliveryStartSeq.Store(startSeq)

	// Log delivery attempt start
	if s.log != nil {
		s.log("sender:eventloop:delivery:start", func() string {
			return fmt.Sprintf("nowUs=%d btreeLen=%d startSeq=%d", nowUs, btreeLen, startSeq)
		})
	}

	// DEBUG: Track btree min sequence to understand IterateFrom behavior
	if minPkt := s.packetBtree.Min(); minPkt != nil {
		minSeq := minPkt.Header().PacketSequenceNumber.Val()
		m.SendDeliveryBtreeMinSeq.Store(uint64(minSeq))
	}

	// Iterate from delivery point and deliver ready packets
	// Matches tickDeliverPacketsBtree() behavior for consistency
	// Phase 3: Added TransmitCount check - only deliver packets with TransmitCount==0
	// Reference: sender_lockfree_architecture.md Section 7.9.3
	s.packetBtree.IterateFrom(uint32(startSeq), func(p packet.Packet) bool {
		if !iterStarted {
			iterStarted = true
			m.SendDeliveryIterStarted.Add(1)
		}

		tsbpdTime := p.Header().PktTsbpdTime
		m.SendDeliveryLastTsbpd.Store(tsbpdTime)

		if tsbpdTime > nowUs {
			// This packet not ready yet - calculate time until ready
			if delivered == 0 {
				m.SendDeliveryTsbpdNotReady.Add(1)
			}
			nextDeliveryIn = time.Duration(tsbpdTime-nowUs) * time.Microsecond
			// Log packet not ready
			if s.log != nil {
				seq := p.Header().PacketSequenceNumber.Val()
				s.log("sender:eventloop:delivery:notready", func() string {
					return fmt.Sprintf("seq=%d tsbpdTime=%d nowUs=%d waitUs=%d",
						seq, tsbpdTime, nowUs, tsbpdTime-nowUs)
				})
			}
			return false // Stop iteration
		}

		// ═══════════════════════════════════════════════════════════════════
		// TransmitCount check - only send if not already transmitted
		// Packets with TransmitCount >= 1 stay in btree for NAK retransmit
		// Reference: sender_lockfree_architecture.md Section 7.9.3
		// ═══════════════════════════════════════════════════════════════════
		seq := p.Header().PacketSequenceNumber.Val()

		if p.Header().TransmitCount == 0 {
			// First transmission - send and mark as sent
			pktLen := p.Len()

			// Log packet delivery
			if s.log != nil {
				s.log("sender:eventloop:delivery:firstsend", func() string {
					return fmt.Sprintf("seq=%d tsbpdTime=%d nowUs=%d transmitCount=0->1", seq, tsbpdTime, nowUs)
				})
			}

			m.CongestionSendPkt.Add(1)
			m.CongestionSendPktUnique.Add(1)
			m.CongestionSendByte.Add(pktLen)
			m.CongestionSendByteUnique.Add(pktLen)
			m.CongestionSendUsSndDuration.Add(uint64(s.pktSndPeriod))
			m.SendRateBytesSent.Add(pktLen)
			m.SendFirstTransmit.Add(1) // Track first transmissions

			s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
			s.deliver(p)

			// Mark as transmitted - packet stays in btree for potential NAK retransmit
			p.Header().TransmitCount = 1

			delivered++
		} else {
			// Already sent - skip (stays in btree for NAK retransmit)
			m.SendAlreadySent.Add(1)

			if s.log != nil {
				s.log("sender:eventloop:delivery:alreadysent", func() string {
					return fmt.Sprintf("seq=%d transmitCount=%d (skipped)", seq, p.Header().TransmitCount)
				})
			}
		}

		// Update delivery point to next sequence (same as tickDeliverPacketsBtree)
		s.deliveryStartPoint.Store(uint64(circular.SeqAdd(seq, 1)))

		return true
	})

	m.SendDeliveryPackets.Add(uint64(delivered))
	return delivered, nextDeliveryIn
}

// dropOldPacketsEventLoop removes packets that have exceeded the drop threshold.
// Called from EventLoop - NO LOCKING (single-threaded btree access).
//
// DEFENSIVE MEASURE: If we drop a packet at or ahead of deliveryStartPoint,
// we advance deliveryStartPoint to prevent stale iteration. This handles the
// head-of-line blocking scenario where a packet with future TSBPD blocks
// delivery of subsequent packets until they all become "too old".
func (s *sender) dropOldPacketsEventLoop(nowUs uint64) {
	// Step 7.5.2: Assert EventLoop context (no-op in release builds)
	s.AssertEventLoopContext()

	if s.packetBtree == nil {
		return
	}

	m := s.metrics

	// Calculate drop threshold with underflow protection
	// See drop_threshold.go and drop_threshold_test.go for details on the bug this fixes
	threshold, shouldDrop := calculateDropThreshold(nowUs, s.dropThreshold)
	if !shouldDrop {
		return // Too early - no packets can be old enough to drop yet
	}

	// Get current delivery start point for defensive check
	currentStartPoint := uint32(s.deliveryStartPoint.Load())

	// Iterate from beginning and drop old packets
	for {
		p := s.packetBtree.Min()
		if p == nil {
			break
		}

		if !shouldDropPacket(p.Header().PktTsbpdTime, threshold) {
			break // Remaining packets are not old enough
		}

		seq := p.Header().PacketSequenceNumber.Val()

		// DEFENSIVE: Check if we're dropping a packet at or ahead of deliveryStartPoint
		// This indicates head-of-line blocking caused packets to be dropped without delivery.
		// circular.SeqLessOrEqual handles 31-bit wraparound correctly.
		if circular.SeqLessOrEqual(currentStartPoint, seq) {
			// Track this anomaly - it means we're dropping undelivered packets
			m.SendDropAheadOfDelivery.Add(1)

			// Log the anomaly
			if s.log != nil {
				tsbpdTime := p.Header().PktTsbpdTime
				s.log("sender:eventloop:drop:undelivered", func() string {
					return fmt.Sprintf("UNDELIVERED DROP: seq=%d tsbpdTime=%d threshold=%d startPoint=%d",
						seq, tsbpdTime, threshold, currentStartPoint)
				})
			}

			// Advance deliveryStartPoint past this packet to prevent stale iteration
			// Next delivery will start from the packet after this dropped one
			nextSeq := circular.SeqAdd(seq, 1)
			s.deliveryStartPoint.Store(uint64(nextSeq))
			currentStartPoint = nextSeq // Update for next iteration
		}

		// Log drop
		if s.log != nil {
			tsbpdTime := p.Header().PktTsbpdTime
			s.log("sender:eventloop:drop", func() string {
				return fmt.Sprintf("DROP: seq=%d tsbpdTime=%d threshold=%d nowUs=%d",
					seq, tsbpdTime, threshold, nowUs)
			})
		}

		// Remove and drop
		s.packetBtree.Delete(seq)

		pktLen := p.Len()
		// Use helper to increment both granular and aggregate counters
		// This ensures CongestionSendDataDropTooOld is incremented (shown in Prometheus)
		metrics.IncrementSendDataDrop(m, metrics.DropReasonTooOldSend, pktLen)
		m.CongestionSendPktBuf.Add(^uint64(0))
		m.CongestionSendByteBuf.Add(^(pktLen - 1))

		p.Decommission()
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TSBPD-Aware Sleep
// ═══════════════════════════════════════════════════════════════════════════════

// tsbpdSleepResult contains the result of sleep duration calculation.
type tsbpdSleepResult struct {
	Duration   time.Duration
	WasTsbpd   bool // True if sleep based on next packet TSBPD
	WasEmpty   bool // True if btree was empty (used max sleep)
	ClampedMin bool // True if duration was clamped to minimum
	ClampedMax bool // True if duration was clamped to maximum
}

// calculateTsbpdSleepDuration determines optimal sleep based on TSBPD.
// Reference: lockless_sender_design.md Section 7.1 "TSBPD-Aware Sleep"
func (s *sender) calculateTsbpdSleepDuration(
	nextDeliveryIn time.Duration,
	deliveredCount int,
	controlDrained int,
	minSleep time.Duration,
	maxSleep time.Duration,
) tsbpdSleepResult {
	res := tsbpdSleepResult{
		Duration: maxSleep,
		WasEmpty: true,
	}

	m := s.metrics

	// If there was activity, don't sleep
	if deliveredCount > 0 || controlDrained > 0 {
		res.Duration = 0
		res.WasEmpty = false
		return res
	}

	// Use TSBPD time for sleep if available
	if nextDeliveryIn > 0 {
		// Sleep until configured factor of next packet's TSBPD time
		calculatedSleep := time.Duration(float64(nextDeliveryIn) * s.tsbpdSleepFactor)

		res.Duration = calculatedSleep
		res.WasTsbpd = true
		res.WasEmpty = false

		// Clamp to configured bounds
		if res.Duration < minSleep {
			res.Duration = minSleep
			res.ClampedMin = true
		} else if res.Duration > maxSleep {
			res.Duration = maxSleep
			res.ClampedMax = true
		}
	}

	// Update metrics
	if res.WasTsbpd {
		m.SendEventLoopTsbpdSleeps.Add(1)
		m.SendEventLoopNextDeliveryTotalUs.Add(uint64(nextDeliveryIn.Microseconds()))
	} else if res.WasEmpty {
		m.SendEventLoopEmptyBtreeSleeps.Add(1)
	}
	if res.ClampedMin {
		m.SendEventLoopSleepClampedMin.Add(1)
	}
	if res.ClampedMax {
		m.SendEventLoopSleepClampedMax.Add(1)
	}
	m.SendEventLoopSleepTotalUs.Add(uint64(res.Duration.Microseconds()))

	return res
}

// UseEventLoop returns whether EventLoop mode is enabled
func (s *sender) UseEventLoop() bool {
	return s.useEventLoop
}
