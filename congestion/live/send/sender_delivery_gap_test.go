//go:build go1.18

package send

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Tests for Delivery Gap Scenarios
//
// These tests address gaps identified in send_eventloop_intermittent_failure_bug.md:
// 1. Packets stuck in btree that become "too old" before delivery
// 2. Full Push → Ring → Btree → Delivery flow with realistic timing
// 3. deliveryStartPoint pointing to dropped/missing packets
// 4. Link capacity probing (probeTime) edge cases
//
// Reference: send_eventloop_intermittent_failure_bug.md Section 24.2
// ============================================================================

// TestDelivery_PacketBecameTooOld tests the scenario where a packet enters
// the btree but is never delivered because its TSBPD time doesn't become
// ready before the drop threshold is exceeded.
//
// CRITICAL BUG FOUND: SRT delivery is IN-ORDER. If packet N has TSBPD in the future,
// packets N+1, N+2, etc. CANNOT be delivered even if their TSBPD has passed.
// This causes a "head-of-line blocking" effect where one delayed packet blocks
// all subsequent packets, eventually causing them ALL to be dropped as "too old".
//
// NOTE: Packets remain in btree after delivery - they are only removed when ACKed.
func TestDelivery_PacketBecameTooOld(t *testing.T) {
	var deliveredSeqs []uint32
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) { deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val()) },
		StartTime:                    time.Now(),
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		UseSendControlRing:           true,
		SendControlRingSize:          256,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          100_000, // 100ms drop threshold (short for testing)
	}).(*sender)

	// Simulate time starting at 0
	currentTimeUs := uint64(0)
	s.nowFn = func() uint64 { return currentTimeUs }

	// Insert packet 0: ready immediately
	p0 := createTestPacketWithTsbpd(0, 0) // TSBPD = 0
	s.packetBtree.Insert(p0)

	// Insert packet 1: ready at time 50ms
	p1 := createTestPacketWithTsbpd(1, 50_000)
	s.packetBtree.Insert(p1)

	// Insert packet 2: "stuck" - TSBPD time is 200ms (way in the future)
	// This simulates a packet that was pushed with incorrect timing
	p2 := createTestPacketWithTsbpd(2, 200_000)
	s.packetBtree.Insert(p2)

	// Insert packet 3: ready at time 60ms - BUT BLOCKED BY PACKET 2!
	p3 := createTestPacketWithTsbpd(3, 60_000)
	s.packetBtree.Insert(p3)

	require.Equal(t, 4, s.packetBtree.Len())

	// Must enter EventLoop context before calling EventLoop functions
	// This is enforced by AssertEventLoopContext() in debug builds
	s.EnterEventLoop()
	defer s.ExitEventLoop()

	// Time = 0: Only packet 0 is ready
	currentTimeUs = 0
	delivered, _ := s.deliverReadyPacketsEventLoop(currentTimeUs)
	require.Equal(t, 1, delivered, "Should deliver packet 0")
	require.Equal(t, []uint32{0}, deliveredSeqs)

	// NOTE: Packets remain in btree after delivery - removed only when ACKed
	// So btree still has 4 packets, but deliveryStartPoint has advanced
	require.Equal(t, 4, s.packetBtree.Len(), "Btree keeps packets until ACKed")

	// Time = 100ms: Packet 1 is ready (tsbpd=50ms), BUT...
	// Packet 2 (tsbpd=200ms) is NOT ready, so iteration STOPS at packet 2.
	// Packet 3 (tsbpd=60ms) is "ready" but BLOCKED by packet 2!
	// This is SRT's IN-ORDER delivery behavior.
	currentTimeUs = 100_000
	delivered, _ = s.deliverReadyPacketsEventLoop(currentTimeUs)
	require.Equal(t, 1, delivered, "Should only deliver packet 1 - packet 2 blocks packet 3!")
	require.Equal(t, []uint32{0, 1}, deliveredSeqs)

	// Btree still has 4 packets (delivery doesn't remove, ACK removes)
	require.Equal(t, 4, s.packetBtree.Len(), "Btree keeps packets until ACKed")

	// Simulate ACK for packets 0 and 1
	s.ackBtree(circular.New(2, packet.MAX_SEQUENCENUMBER)) // ACK up to seq 2
	require.Equal(t, 2, s.packetBtree.Len(), "ACK should remove packets 0 and 1")

	// Time = 350ms: Drop check runs
	// Drop threshold = 350ms - 100ms = 250ms
	// Packet 2 (tsbpd=200ms): 200ms < 250ms → DROPPED
	// Packet 3 (tsbpd=60ms): 60ms < 250ms → DROPPED (even though it was "ready"!)
	currentTimeUs = 350_000
	s.dropOldPacketsEventLoop(currentTimeUs)
	require.Equal(t, 0, s.packetBtree.Len(), "Both packets 2 and 3 should be dropped")
	require.Equal(t, uint64(2), m.CongestionSendDataDropTooOld.Load(),
		"CRITICAL: Packet 3 was 'ready' but blocked by packet 2, then dropped!")

	// Verify packets 2 AND 3 were NEVER delivered
	require.Equal(t, []uint32{0, 1}, deliveredSeqs,
		"Only packets 0 and 1 delivered - packet 2 blocked packet 3 causing both to be dropped")
}

// TestDelivery_FullFlowWithRing tests the complete flow:
// Push() → Ring → drainRingToBtree() → deliverReady()
// This ensures no gaps are introduced through the full path.
func TestDelivery_FullFlowWithRing(t *testing.T) {
	var deliveredSeqs []uint32
	var mu atomic.Int32
	m := &metrics.ConnectionMetrics{}

	start := time.Now()
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(1000, packet.MAX_SEQUENCENUMBER), // Non-zero ISN
		ConnectionMetrics:     m,
		OnDeliver: func(p packet.Packet) {
			mu.Add(1)
			deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
		},
		StartTime:                    start,
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		SendRingShards:               1,
		UseSendControlRing:           true,
		SendControlRingSize:          256,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          1_000_000, // 1 second
	}).(*sender)

	// Use real relative time
	s.nowFn = func() uint64 { return uint64(time.Since(start).Microseconds()) }

	// Mark EventLoop context for drain operations
	s.EnterEventLoop()
	defer s.ExitEventLoop()

	// Push 100 packets through the full flow
	numPackets := 100
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(mockAddr{})
		// Set TSBPD time to now + small offset (ready almost immediately)
		p.Header().PktTsbpdTime = uint64(time.Since(start).Microseconds()) + uint64(i*100)
		s.Push(p)
	}

	// Verify all pushed to ring
	require.Equal(t, uint64(numPackets), m.SendRingPushed.Load(), "All packets should be pushed to ring")

	// Drain ring to btree
	drained := s.drainRingToBtreeEventLoop()
	require.Equal(t, numPackets, drained, "All packets should drain to btree")

	// Verify NO sequence gaps during drain
	require.Equal(t, uint64(0), m.SendRingDrainSeqGap.Load(),
		"CRITICAL: Sequence gap detected during drain! This causes phantom NAKs.")

	// Allow time for TSBPD to become ready
	time.Sleep(20 * time.Millisecond)

	// Deliver ready packets
	nowUs := uint64(time.Since(start).Microseconds())
	delivered, _ := s.deliverReadyPacketsEventLoop(nowUs)
	require.Equal(t, numPackets, delivered, "All packets should be delivered")

	// Verify delivered sequences are contiguous
	for i := 0; i < len(deliveredSeqs)-1; i++ {
		expected := circular.SeqAdd(deliveredSeqs[i], 1)
		actual := deliveredSeqs[i+1]
		require.Equal(t, expected, actual,
			"Delivered sequences should be contiguous: seq[%d]=%d, seq[%d]=%d, expected %d",
			i, deliveredSeqs[i], i+1, deliveredSeqs[i+1], expected)
	}
}

// TestDelivery_DeliveryStartPointAfterDrop tests what happens when
// deliveryStartPoint points to a sequence that was dropped.
func TestDelivery_DeliveryStartPointAfterDrop(t *testing.T) {
	var deliveredSeqs []uint32
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(100, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) { deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val()) },
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          100_000,
	}).(*sender)

	currentTimeUs := uint64(0)
	s.nowFn = func() uint64 { return currentTimeUs }

	// Insert packets 100, 101, 103, 104 (gap at 102)
	s.packetBtree.Insert(createTestPacketWithTsbpd(100, 0))
	s.packetBtree.Insert(createTestPacketWithTsbpd(101, 0))
	// Skip 102 - simulates packet that was never pushed or dropped
	s.packetBtree.Insert(createTestPacketWithTsbpd(103, 0))
	s.packetBtree.Insert(createTestPacketWithTsbpd(104, 0))

	// Must enter EventLoop context before calling EventLoop functions
	s.EnterEventLoop()
	defer s.ExitEventLoop()

	// Deliver all
	delivered, _ := s.deliverReadyPacketsEventLoop(currentTimeUs)
	require.Equal(t, 4, delivered)

	// Verify deliveryStartPoint is at 105 (past all delivered)
	require.Equal(t, uint64(105), s.deliveryStartPoint.Load())

	// Verify delivered sequences jump over the gap
	require.Equal(t, []uint32{100, 101, 103, 104}, deliveredSeqs,
		"Should deliver available packets, skipping missing seq 102")
}

// TestDelivery_ProbeTimeInitialization tests link capacity probing
// doesn't cause incorrect TSBPD times on startup.
func TestDelivery_ProbeTimeInitialization(t *testing.T) {
	var deliveredSeqs []uint32
	m := &metrics.ConnectionMetrics{}

	// Use ISN where first packet has seqNum & 0xF == 0
	// This triggers probeTime SET on first packet
	isn := uint32(16) // 16 & 0xF == 0

	start := time.Now()
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(isn, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) { deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val()) },
		StartTime:                    start,
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		SendRingShards:               1,
		UseSendControlRing:           true,
		SendControlRingSize:          256,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          1_000_000,
	}).(*sender)

	s.nowFn = func() uint64 { return uint64(time.Since(start).Microseconds()) }
	s.EnterEventLoop()
	defer s.ExitEventLoop()

	// Push 20 packets (covers probe sequence 0 and 1)
	for i := 0; i < 20; i++ {
		p := packet.NewPacket(mockAddr{})
		p.Header().PktTsbpdTime = uint64(time.Since(start).Microseconds()) + 1000 // Ready in 1ms
		s.Push(p)
	}

	// Drain and wait
	s.drainRingToBtreeEventLoop()
	time.Sleep(5 * time.Millisecond)

	// Deliver
	nowUs := uint64(time.Since(start).Microseconds())
	delivered, _ := s.deliverReadyPacketsEventLoop(nowUs)

	// CRITICAL: All 20 packets should be delivered
	// If probeTime is incorrectly 0, packet 17 (seq & 0xF == 1) would have TSBPD=0
	// and either be delivered immediately or be "too old"
	require.Equal(t, 20, delivered,
		"All packets should be delivered. If <20, check probeTime initialization.")

	// Verify no drops
	require.Equal(t, uint64(0), m.CongestionSendDataDropTooOld.Load(),
		"No packets should be dropped due to probeTime issues")
}

// TestDelivery_ConcurrentPushAndDrop tests the flow: Push → Ring → Btree → Deliver.
// Verifies that all pushed packets are drained and delivered with no sequence gaps.
func TestDelivery_ConcurrentPushAndDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	m := &metrics.ConnectionMetrics{}
	var deliveredCount atomic.Int64

	start := time.Now()
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) { deliveredCount.Add(1) },
		StartTime:                    start,
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          2_000_000, // 2 second (longer to avoid drops during test)
	}).(*sender)

	// Push packets FIRST, then run EventLoop
	numPackets := 200
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(mockAddr{})
		// All packets ready immediately (current time)
		p.Header().PktTsbpdTime = uint64(time.Since(start).Microseconds())
		s.Push(p)
	}

	// Verify all pushed to ring
	pushed := m.SendRingPushed.Load()
	require.Equal(t, uint64(numPackets), pushed, "All packets should be pushed")

	// Run EventLoop for short time - enough to drain and deliver
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		s.EventLoop(ctx, &wg) // wg.Done() called internally
		close(done)
	}()

	// Wait for EventLoop to finish
	<-done

	// Verify metrics
	drained := m.SendRingDrained.Load()
	dropped := m.CongestionSendDataDropTooOld.Load()
	delivered := deliveredCount.Load()

	t.Logf("Pushed=%d, Drained=%d, Delivered=%d, Dropped=%d",
		pushed, drained, delivered, dropped)

	// All pushed should be drained
	require.Equal(t, pushed, drained, "All pushed packets should be drained")

	// All drained should be delivered (since drop threshold is long and packets were ready)
	require.Equal(t, drained, uint64(delivered),
		"All drained packets should be delivered (no drops with 2s threshold)")

	// With 2s drop threshold and <200ms test, no drops should occur
	require.Equal(t, uint64(0), dropped, "No drops expected with long threshold")

	// NOTE: Btree is cleared by cleanupOnShutdown() when EventLoop exits,
	// so we can't check btree state after EventLoop completes.

	// No sequence gaps should be detected - THIS IS THE KEY ASSERTION
	require.Equal(t, uint64(0), m.SendRingDrainSeqGap.Load(),
		"CRITICAL: No sequence gaps should occur during drain")
}
