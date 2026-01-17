//go:build go1.18

package send

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Integration tests for EventLoop
//
// These tests verify the full EventLoop cycle:
// - Push → Ring → Btree (via drain)
// - Control packet processing (ACK/NAK)
// - Delivery (via TSBPD)
// - Drop (old packets)
//
// Reference: lockless_sender_design.md Section 3.1 "EventLoop Architecture"
// ============================================================================

// TestEventLoop_PushToDrain tests Push → Ring → Btree flow
func TestEventLoop_PushToDrain(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		SendRingShards:               1,
		UseSendControlRing:           true,
		SendControlRingSize:          256,
		SendControlRingShards:        2,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          1_000_000,
	}).(*sender)

	// Initially empty
	require.Equal(t, 0, s.packetBtree.Len())
	require.Equal(t, 0, s.packetRing.Len())

	// Push packets to ring (not directly to btree)
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100)
		s.pushRing(pkt)
	}

	// Ring should have packets
	require.Equal(t, 10, s.packetRing.Len())
	// Btree still empty (not drained yet)
	require.Equal(t, 0, s.packetBtree.Len())

	// Drain ring to btree (with EventLoop context)
	var drained int
	runInEventLoopContext(s, func() {
		drained = s.drainRingToBtreeEventLoop()
	})
	require.Equal(t, 10, drained)

	// Now btree has packets, ring is empty
	require.Equal(t, 10, s.packetBtree.Len())
	require.Equal(t, 0, s.packetRing.Len())
}

// TestEventLoop_DrainThenDeliver tests drain followed by delivery
func TestEventLoop_DrainThenDeliver(t *testing.T) {
	var deliveredCount atomic.Int32

	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) { deliveredCount.Add(1) },
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          10_000_000,
	}).(*sender)

	// Set nowFn for predictable TSBPD
	s.nowFn = func() uint64 { return 1_000_000 }

	// Push packets with TSBPD times <= nowUs
	for i := 0; i < 5; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100) // 0, 100, 200, 300, 400
		s.pushRing(pkt)
	}

	// Drain to btree and deliver (with EventLoop context)
	var delivered int
	runInEventLoopContext(s, func() {
		s.drainRingToBtreeEventLoop()
		require.Equal(t, 5, s.packetBtree.Len())

		// Deliver ready packets
		delivered, _ = s.deliverReadyPacketsEventLoop(1_000_000)
	})

	require.Equal(t, 5, delivered)
	require.Equal(t, int32(5), deliveredCount.Load())
}

// TestEventLoop_HighISN tests EventLoop with high ISN (regression test)
func TestEventLoop_HighISN(t *testing.T) {
	isnValues := []uint32{
		549144712,  // THE FAILING CASE
		879502527,  // From actual test
		2147483640, // Near max
	}

	for _, isn := range isnValues {
		t.Run(formatISN(isn), func(t *testing.T) {
			var deliveredCount atomic.Int32

			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber:        circular.New(isn, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:            m,
				OnDeliver:                    func(p packet.Packet) { deliveredCount.Add(1) },
				StartTime:                    time.Now(),
				UseBtree:                     true,
				UseSendRing:                  true,
				UseSendControlRing:           true,
				UseSendEventLoop:             true,
				SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
				SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
				SendDropThresholdUs:          10_000_000,
			}).(*sender)

			// CRITICAL: Verify deliveryStartPoint is initialized to ISN
			require.Equal(t, uint64(isn), s.deliveryStartPoint.Load(),
				"deliveryStartPoint must be initialized to ISN")

			// Set time
			s.nowFn = func() uint64 { return 1_000_000 }

			// Push packets at ISN, ISN+1, etc.
			for i := 0; i < 5; i++ {
				seq := circular.SeqAdd(isn, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
				s.pushRing(pkt)
			}

			// Drain and deliver (with EventLoop context)
			var drained, delivered int
			runInEventLoopContext(s, func() {
				drained = s.drainRingToBtreeEventLoop()
				require.Equal(t, 5, drained)

				// Deliver - THIS IS THE CRITICAL TEST
				// Before fix: IterateFrom(0) would fail to find packets at ~549M
				// After fix: IterateFrom(ISN) correctly finds packets
				delivered, _ = s.deliverReadyPacketsEventLoop(1_000_000)
			})

			require.Equal(t, 5, delivered,
				"BUG: Failed to deliver packets at high ISN %d. "+
					"Check deliveryStartPoint initialization.", isn)
			require.Equal(t, int32(5), deliveredCount.Load())
		})
	}
}

// TestEventLoop_ACKProcessing tests ACK via control ring
func TestEventLoop_ACKProcessing(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	s.nowFn = func() uint64 { return 1_000_000 }

	// Insert packets directly to btree (bypass ring for this test)
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100)
		s.packetBtree.Insert(pkt)
	}

	require.Equal(t, 10, s.packetBtree.Len())

	// Simulate ACK processing directly (control ring test is separate)
	ackSeq := uint32(5)
	s.ackBtree(circular.New(ackSeq, packet.MAX_SEQUENCENUMBER))

	// Should have removed 5 packets (0-4)
	require.Equal(t, 5, s.packetBtree.Len())
}

// TestEventLoop_DropOld tests drop of old packets
func TestEventLoop_DropOld(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendDropThresholdUs:          1_000_000, // 1 second
		DropThreshold:                1_000_000,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Insert packets with old TSBPD times
	for i := 0; i < 5; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100) // 0, 100, 200, 300, 400 µs
		s.packetBtree.Insert(pkt)
	}

	require.Equal(t, 5, s.packetBtree.Len())

	// Set time to 2 seconds (all packets are > 1 second old)
	s.nowFn = func() uint64 { return 2_000_000 }

	// Drop old packets (with EventLoop context)
	runInEventLoopContext(s, func() {
		s.dropOldPacketsEventLoop(2_000_000)
	})

	// All should be dropped
	require.Equal(t, 0, s.packetBtree.Len())
}

// TestEventLoop_UnderflowProtection tests drop threshold underflow protection
func TestEventLoop_UnderflowProtection(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendDropThresholdUs:          1_000_000, // 1 second
		DropThreshold:                1_000_000,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Insert packets
	for i := 0; i < 5; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100)
		s.packetBtree.Insert(pkt)
	}

	require.Equal(t, 5, s.packetBtree.Len())

	// Set time to 500ms (LESS than dropThreshold of 1s)
	// This triggers underflow protection
	s.nowFn = func() uint64 { return 500_000 }

	// Try to drop - should NOT drop due to underflow protection (with EventLoop context)
	runInEventLoopContext(s, func() {
		s.dropOldPacketsEventLoop(500_000)
	})

	// All should remain
	require.Equal(t, 5, s.packetBtree.Len(),
		"underflow protection should prevent drops when nowUs < dropThreshold")
}

// TestEventLoop_RingFull tests behavior when ring is full
func TestEventLoop_RingFull(t *testing.T) {
	ringSize := 32 // Small ring for testing

	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 ringSize,
		SendRingShards:               1,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Fill the ring - pushRing doesn't return error, check ring Len instead
	initialLen := s.packetRing.Len()
	for i := 0; i < ringSize*2; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100)
		s.pushRing(pkt)
	}
	pushed := s.packetRing.Len() - initialLen

	// Ring should be full at capacity
	require.LessOrEqual(t, pushed, ringSize,
		"should not push more than ring capacity")
}

// TestEventLoop_FullCycle tests complete cycle: push → drain → deliver
func TestEventLoop_FullCycle(t *testing.T) {
	var deliveredSeqs []uint32
	var mu = make(chan struct{}, 1) // Simple mutex via channel

	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(549144712, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver: func(p packet.Packet) {
			select {
			case mu <- struct{}{}:
				deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
				<-mu
			default:
			}
		},
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
		SendDropThresholdUs:          10_000_000,
	}).(*sender)

	// Set predictable time
	s.nowFn = func() uint64 { return 1_000_000 }

	// Push 100 packets
	isn := uint32(549144712)
	for i := 0; i < 100; i++ {
		seq := circular.SeqAdd(isn, uint32(i))
		pkt := createTestPacketWithTsbpd(seq, uint64(i)*1000) // All < 1M
		s.pushRing(pkt)
	}

	// Drain and deliver (with EventLoop context)
	var drained, delivered int
	runInEventLoopContext(s, func() {
		drained = s.drainRingToBtreeEventLoop()
		require.Equal(t, 100, drained)

		// Deliver
		delivered, _ = s.deliverReadyPacketsEventLoop(1_000_000)
	})

	require.Equal(t, 100, delivered)
	require.Len(t, deliveredSeqs, 100)

	// Verify ordering (should be in sequence order)
	for i := 0; i < len(deliveredSeqs)-1; i++ {
		require.True(t, circular.SeqLess(deliveredSeqs[i], deliveredSeqs[i+1]) ||
			deliveredSeqs[i] == deliveredSeqs[i+1]-1,
			"packets should be delivered in order")
	}
}

// TestEventLoop_ConcurrentPushDrain tests concurrent push while draining
func TestEventLoop_ConcurrentPushDrain(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	s.nowFn = func() uint64 { return 1_000_000 }

	// Push from goroutine
	done := make(chan bool)
	go func() {
		for i := 0; i < 100; i++ {
			pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100)
			s.pushRing(pkt)
		}
		done <- true
	}()

	// Drain multiple times while push is happening (with EventLoop context)
	totalDrained := 0
	runInEventLoopContext(s, func() {
		for i := 0; i < 50; i++ {
			drained := s.drainRingToBtreeEventLoop()
			totalDrained += drained
			time.Sleep(100 * time.Microsecond)
		}
	})

	<-done

	// Final drain (with EventLoop context)
	var drained int
	runInEventLoopContext(s, func() {
		drained = s.drainRingToBtreeEventLoop()
	})
	totalDrained += drained

	// Should have drained all 100 packets (eventually)
	require.Equal(t, 100, s.packetBtree.Len()+s.packetRing.Len(),
		"all packets should be either in btree or ring")
}

