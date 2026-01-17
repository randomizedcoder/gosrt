//go:build go1.18

package send

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Unit tests for Sender EventLoop
// Reference: lockless_sender_implementation_plan.md Phase 4
// ═══════════════════════════════════════════════════════════════════════════════

func createEventLoopSender(t *testing.T) *sender {
	m := &metrics.ConnectionMetrics{}
	delivered := make([]packet.Packet, 0)

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver: func(p packet.Packet) {
			delivered = append(delivered, p)
		},
		// Phase 1: Enable btree
		UseBtree:    true,
		BtreeDegree: 32,
		// Phase 2: Enable ring
		UseSendRing:    true,
		SendRingSize:   1024,
		SendRingShards: 1,
		// Phase 3: Enable control ring
		UseSendControlRing:    true,
		SendControlRingSize:   256,
		SendControlRingShards: 2,
		// Phase 4: Enable EventLoop
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendTsbpdSleepFactor:         0.9,
		SendDropThresholdUs:          1000000,
	}).(*sender)

	return s
}

// createEventLoopSenderWithContext creates a sender and marks it as being in EventLoop context.
// This is for tests that call EventLoop-internal functions directly.
// Call the returned cleanup function at the end of the test (or use defer).
func createEventLoopSenderWithContext(t *testing.T) (*sender, func()) {
	s := createEventLoopSender(t)
	s.EnterEventLoop()
	return s, func() { s.ExitEventLoop() }
}

// ═══════════════════════════════════════════════════════════════════════════════
// UseEventLoop Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_UseEventLoop_Enabled(t *testing.T) {
	s := createEventLoopSender(t)
	require.True(t, s.UseEventLoop())
}

func TestEventLoop_UseEventLoop_Disabled(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
	}).(*sender)

	require.False(t, s.UseEventLoop())
}

// ═══════════════════════════════════════════════════════════════════════════════
// EventLoop Startup/Shutdown Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_StartStop(t *testing.T) {
	s := createEventLoopSender(t)

	ctx, cancel := context.WithCancel(context.Background())

	// Start EventLoop in goroutine
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		s.EventLoop(ctx, &wg) // wg.Done() called internally
		close(done)
	}()

	// Let it run briefly
	time.Sleep(10 * time.Millisecond)

	// Stop EventLoop
	cancel()

	// Wait for shutdown
	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("EventLoop did not shutdown in time")
	}

	// Verify metrics were updated
	require.Greater(t, s.metrics.SendEventLoopIterations.Load(), uint64(0))
}

func TestEventLoop_DisabledNoOp(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		// EventLoop NOT enabled
	}).(*sender)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should return immediately
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		s.EventLoop(ctx, &wg) // wg.Done() called internally
		close(done)
	}()

	select {
	case <-done:
		// Success - returned immediately
	case <-time.After(100 * time.Millisecond):
		t.Fatal("EventLoop should have returned immediately when disabled")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TSBPD Sleep Calculation Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_CalculateTsbpdSleepDuration_Activity(t *testing.T) {
	s := createEventLoopSender(t)

	// With activity, should return 0 sleep
	result := s.calculateTsbpdSleepDuration(
		100*time.Millisecond, // nextDeliveryIn
		5,                    // deliveredCount > 0
		0,                    // controlDrained
		100*time.Microsecond, // minSleep
		1*time.Millisecond,   // maxSleep
	)

	require.Equal(t, time.Duration(0), result.Duration)
	require.False(t, result.WasEmpty)
	require.False(t, result.WasTsbpd)
}

func TestEventLoop_CalculateTsbpdSleepDuration_ControlActivity(t *testing.T) {
	s := createEventLoopSender(t)

	// With control activity, should return 0 sleep
	result := s.calculateTsbpdSleepDuration(
		100*time.Millisecond, // nextDeliveryIn
		0,                    // deliveredCount
		3,                    // controlDrained > 0
		100*time.Microsecond, // minSleep
		1*time.Millisecond,   // maxSleep
	)

	require.Equal(t, time.Duration(0), result.Duration)
	require.False(t, result.WasEmpty)
}

func TestEventLoop_CalculateTsbpdSleepDuration_TsbpdAware(t *testing.T) {
	s := createEventLoopSender(t)

	// With no activity and next delivery in 10ms, should sleep ~9ms (90%)
	result := s.calculateTsbpdSleepDuration(
		10*time.Millisecond,  // nextDeliveryIn
		0,                    // deliveredCount
		0,                    // controlDrained
		100*time.Microsecond, // minSleep
		100*time.Millisecond, // maxSleep (high to not clamp)
	)

	// Should be 90% of 10ms = 9ms
	require.True(t, result.WasTsbpd)
	require.False(t, result.WasEmpty)
	require.InDelta(t, 9*time.Millisecond, result.Duration, float64(100*time.Microsecond))
}

func TestEventLoop_CalculateTsbpdSleepDuration_ClampedMin(t *testing.T) {
	s := createEventLoopSender(t)

	// With very short next delivery, should clamp to min
	result := s.calculateTsbpdSleepDuration(
		10*time.Microsecond,  // nextDeliveryIn (very short)
		0,                    // deliveredCount
		0,                    // controlDrained
		100*time.Microsecond, // minSleep
		1*time.Millisecond,   // maxSleep
	)

	require.True(t, result.ClampedMin)
	require.Equal(t, 100*time.Microsecond, result.Duration)
}

func TestEventLoop_CalculateTsbpdSleepDuration_ClampedMax(t *testing.T) {
	s := createEventLoopSender(t)

	// With very long next delivery, should clamp to max
	result := s.calculateTsbpdSleepDuration(
		10*time.Second,       // nextDeliveryIn (very long)
		0,                    // deliveredCount
		0,                    // controlDrained
		100*time.Microsecond, // minSleep
		1*time.Millisecond,   // maxSleep
	)

	require.True(t, result.ClampedMax)
	require.Equal(t, 1*time.Millisecond, result.Duration)
}

func TestEventLoop_CalculateTsbpdSleepDuration_EmptyBtree(t *testing.T) {
	s := createEventLoopSender(t)

	// With no next delivery (empty btree), should use max sleep
	result := s.calculateTsbpdSleepDuration(
		0,                    // nextDeliveryIn = 0 (no packets)
		0,                    // deliveredCount
		0,                    // controlDrained
		100*time.Microsecond, // minSleep
		1*time.Millisecond,   // maxSleep
	)

	require.True(t, result.WasEmpty)
	require.Equal(t, 1*time.Millisecond, result.Duration)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Cleanup Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_CleanupOnShutdown(t *testing.T) {
	s := createEventLoopSender(t)

	// Push some packets to ring
	for i := uint32(0); i < 10; i++ {
		p := createTestPacket(i)
		s.packetRing.Push(p)
	}
	require.Equal(t, 10, int(s.packetRing.ring.Len()))

	// Push some control packets
	s.controlRing.PushACK(circular.New(100, packet.MAX_SEQUENCENUMBER))
	s.controlRing.PushACK(circular.New(101, packet.MAX_SEQUENCENUMBER))

	// Call cleanup
	s.cleanupOnShutdown()

	// Verify rings are empty
	require.Equal(t, 0, int(s.packetRing.ring.Len()))
	require.Equal(t, 0, s.controlRing.Len())
	require.Equal(t, 0, s.packetBtree.Len())
	require.Equal(t, uint64(0), s.metrics.SendBtreeLen.Load())
}

// ═══════════════════════════════════════════════════════════════════════════════
// Process Control Packets Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_ProcessControlPacketsDelta_ACK(t *testing.T) {
	s, cleanup := createEventLoopSenderWithContext(t)
	defer cleanup()

	// Insert some packets into btree
	for i := uint32(0); i < 10; i++ {
		p := createTestPacket(i)
		s.packetBtree.Insert(p)
	}
	require.Equal(t, 10, s.packetBtree.Len())

	// Push ACK for seq 5 (should remove 0-4)
	s.controlRing.PushACK(circular.New(5, packet.MAX_SEQUENCENUMBER))

	// Process control packets
	processed := s.processControlPacketsDelta()

	require.Equal(t, 1, processed)
	require.Equal(t, uint64(1), s.metrics.SendEventLoopACKsProcessed.Load())
	// Packets 0-4 should be removed (5 packets)
	require.Equal(t, 5, s.packetBtree.Len())
}

func TestEventLoop_ProcessControlPacketsDelta_Empty(t *testing.T) {
	s, cleanup := createEventLoopSenderWithContext(t)
	defer cleanup()

	// No control packets
	processed := s.processControlPacketsDelta()

	require.Equal(t, 0, processed)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Drain Ring Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_DrainRingToBtreeEventLoop(t *testing.T) {
	s, cleanup := createEventLoopSenderWithContext(t)
	defer cleanup()

	// Push packets to ring
	for i := uint32(0); i < 10; i++ {
		p := createTestPacket(i)
		s.packetRing.Push(p)
	}
	require.Equal(t, 0, s.packetBtree.Len())

	// Drain
	drained := s.drainRingToBtreeEventLoop()

	require.Equal(t, 10, drained)
	require.Equal(t, 10, s.packetBtree.Len())
	require.Equal(t, uint64(10), s.metrics.SendRingDrained.Load())
}

// ═══════════════════════════════════════════════════════════════════════════════
// Table-Driven Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_CalculateTsbpdSleepDuration_Table(t *testing.T) {
	s := createEventLoopSender(t)

	tests := []struct {
		name           string
		nextDeliveryIn time.Duration
		deliveredCount int
		controlDrained int
		minSleep       time.Duration
		maxSleep       time.Duration
		expectDuration time.Duration
		expectTsbpd    bool
		expectEmpty    bool
		expectClampMin bool
		expectClampMax bool
	}{
		{
			name:           "activity_no_sleep",
			nextDeliveryIn: 10 * time.Millisecond,
			deliveredCount: 1,
			controlDrained: 0,
			minSleep:       100 * time.Microsecond,
			maxSleep:       1 * time.Millisecond,
			expectDuration: 0,
			expectTsbpd:    false,
			expectEmpty:    false,
		},
		{
			name:           "control_activity_no_sleep",
			nextDeliveryIn: 10 * time.Millisecond,
			deliveredCount: 0,
			controlDrained: 1,
			minSleep:       100 * time.Microsecond,
			maxSleep:       1 * time.Millisecond,
			expectDuration: 0,
			expectTsbpd:    false,
			expectEmpty:    false,
		},
		{
			name:           "empty_btree_max_sleep",
			nextDeliveryIn: 0,
			deliveredCount: 0,
			controlDrained: 0,
			minSleep:       100 * time.Microsecond,
			maxSleep:       1 * time.Millisecond,
			expectDuration: 1 * time.Millisecond,
			expectTsbpd:    false,
			expectEmpty:    true,
		},
		{
			name:           "tsbpd_clamp_min",
			nextDeliveryIn: 50 * time.Microsecond, // 90% = 45µs < 100µs min
			deliveredCount: 0,
			controlDrained: 0,
			minSleep:       100 * time.Microsecond,
			maxSleep:       1 * time.Millisecond,
			expectDuration: 100 * time.Microsecond,
			expectTsbpd:    true,
			expectEmpty:    false,
			expectClampMin: true,
		},
		{
			name:           "tsbpd_clamp_max",
			nextDeliveryIn: 100 * time.Millisecond, // 90% = 90ms > 1ms max
			deliveredCount: 0,
			controlDrained: 0,
			minSleep:       100 * time.Microsecond,
			maxSleep:       1 * time.Millisecond,
			expectDuration: 1 * time.Millisecond,
			expectTsbpd:    true,
			expectEmpty:    false,
			expectClampMax: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.calculateTsbpdSleepDuration(
				tt.nextDeliveryIn,
				tt.deliveredCount,
				tt.controlDrained,
				tt.minSleep,
				tt.maxSleep,
			)

			require.Equal(t, tt.expectDuration, result.Duration, "Duration mismatch")
			require.Equal(t, tt.expectTsbpd, result.WasTsbpd, "WasTsbpd mismatch")
			require.Equal(t, tt.expectEmpty, result.WasEmpty, "WasEmpty mismatch")
			require.Equal(t, tt.expectClampMin, result.ClampedMin, "ClampedMin mismatch")
			require.Equal(t, tt.expectClampMax, result.ClampedMax, "ClampedMax mismatch")
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Idle Backoff Tests (matching receiver's TestEventLoop_IdleBackoff)
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_IdleBackoff(t *testing.T) {
	s := createEventLoopSender(t)

	// Run EventLoop with NO packets (idle)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	s.EventLoop(ctx, &wg) // wg.Done() called internally

	// Check idle backoff metrics
	idleBackoffs := s.metrics.SendEventLoopIdleBackoffs.Load()
	iterations := s.metrics.SendEventLoopIterations.Load()

	// With no packets, most iterations should trigger backoff
	require.Greater(t, idleBackoffs, uint64(0), "Idle backoffs should occur with no traffic")
	t.Logf("Idle backoffs: %d, total iterations: %d", idleBackoffs, iterations)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Ring Integration Tests (matching receiver's TestEventLoop_Ring_*)
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_Ring_BasicFlow(t *testing.T) {
	s := createEventLoopSender(t)

	// Start EventLoop in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		s.EventLoop(ctx, &wg) // wg.Done() called internally
		close(done)
	}()

	// Push packets while EventLoop is running
	for i := uint32(0); i < 20; i++ {
		p := createTestPacket(i)
		p.Header().PktTsbpdTime = uint64(time.Now().UnixMicro()) // Ready immediately
		s.packetRing.Push(p)
		time.Sleep(1 * time.Millisecond) // Simulate packet arrival rate
	}

	// Wait for processing
	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	// Verify packets were drained
	drained := s.metrics.SendRingDrained.Load()
	require.Equal(t, uint64(20), drained, "All packets should be drained from ring")
	t.Logf("Ring flow: drained=%d", drained)
}

func TestEventLoop_Ring_HighThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high throughput test in short mode")
	}

	s := createEventLoopSender(t)

	// Start EventLoop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		s.EventLoop(ctx, &wg) // wg.Done() called internally
		close(done)
	}()

	// Push 1000 packets as fast as possible
	numPackets := 1000
	for i := 0; i < numPackets; i++ {
		p := createTestPacket(uint32(i))
		p.Header().PktTsbpdTime = uint64(time.Now().UnixMicro()) // Ready immediately
		s.packetRing.Push(p)
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	drained := s.metrics.SendRingDrained.Load()
	require.Equal(t, uint64(numPackets), drained, "All packets should be drained")

	t.Logf("High throughput: %d packets - drained=%d", numPackets, drained)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Metrics Increment Tests (matching receiver's TestEventLoop_MetricsIncrement)
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_MetricsIncrement(t *testing.T) {
	s := createEventLoopSender(t)

	// Run EventLoop for 100ms
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	s.EventLoop(ctx, &wg) // wg.Done() called internally

	// Check metrics were incremented
	iterations := s.metrics.SendEventLoopIterations.Load()
	defaultRuns := s.metrics.SendEventLoopDefaultRuns.Load()

	require.Greater(t, iterations, uint64(0), "SendEventLoopIterations should be > 0")
	require.Greater(t, defaultRuns, uint64(0), "SendEventLoopDefaultRuns should be > 0")

	t.Logf("EventLoop ran for 100ms: iterations=%d, defaultRuns=%d", iterations, defaultRuns)
}

// ═══════════════════════════════════════════════════════════════════════════════
// ACK/NAK Processing Tests (sender-specific - receiver generates, sender receives)
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_ProcessControlPacketsDelta_NAK(t *testing.T) {
	s, cleanup := createEventLoopSenderWithContext(t)
	defer cleanup()

	// Insert packets 0-9 into btree
	for i := uint32(0); i < 10; i++ {
		p := createTestPacket(i)
		s.packetBtree.Insert(p)
	}
	require.Equal(t, 10, s.packetBtree.Len())

	// Verify initial state
	require.Equal(t, uint64(0), s.metrics.PktRetransFromNAK.Load())

	// Push NAK for seq 3-5 (ranges: start, end pairs)
	// SRT NAK format is [start1, end1, start2, end2, ...]
	nakSeqs := []circular.Number{
		circular.New(3, packet.MAX_SEQUENCENUMBER), // start
		circular.New(5, packet.MAX_SEQUENCENUMBER), // end (inclusive)
	}
	s.controlRing.PushNAK(nakSeqs)

	// Process control packets
	processed := s.processControlPacketsDelta()

	require.Equal(t, 1, processed)
	require.Equal(t, uint64(1), s.metrics.SendEventLoopNAKsProcessed.Load())
	// Btree should still have all 10 packets (NAK doesn't remove them)
	require.Equal(t, 10, s.packetBtree.Len())
	// BUG FIX: PktRetransFromNAK should now be updated when EventLoop processes NAKs
	// NAK for seq 3-5 = 3 packets retransmitted
	require.Equal(t, uint64(3), s.metrics.PktRetransFromNAK.Load(),
		"PktRetransFromNAK should be updated when EventLoop processes NAKs")
}

func TestEventLoop_ProcessControlPacketsDelta_Mixed(t *testing.T) {
	s, cleanup := createEventLoopSenderWithContext(t)
	defer cleanup()

	// Insert packets 0-9 into btree
	for i := uint32(0); i < 10; i++ {
		p := createTestPacket(i)
		s.packetBtree.Insert(p)
	}

	// Push ACK for seq 3, then NAK for seq 7-7 (single packet range)
	s.controlRing.PushACK(circular.New(3, packet.MAX_SEQUENCENUMBER))
	s.controlRing.PushNAK([]circular.Number{
		circular.New(7, packet.MAX_SEQUENCENUMBER), // start
		circular.New(7, packet.MAX_SEQUENCENUMBER), // end (same = single packet)
	})

	// Process control packets
	processed := s.processControlPacketsDelta()

	require.Equal(t, 2, processed)
	require.Equal(t, uint64(1), s.metrics.SendEventLoopACKsProcessed.Load())
	require.Equal(t, uint64(1), s.metrics.SendEventLoopNAKsProcessed.Load())
	// ACK for seq 3 should remove packets 0-2 (3 packets)
	require.Equal(t, 7, s.packetBtree.Len())
}

// ═══════════════════════════════════════════════════════════════════════════════
// Wraparound Tests (matching receiver's TestEventLoop_LossRecovery_Wraparound)
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_Wraparound(t *testing.T) {
	s := createEventLoopSender(t)

	// Start near the 31-bit wraparound point
	startSeq := packet.MAX_SEQUENCENUMBER - 50
	numPackets := 100

	// Push packets that wrap around
	for i := 0; i < numPackets; i++ {
		seq := circular.SeqAdd(startSeq, uint32(i))
		p := createTestPacket(seq)
		p.Header().PktTsbpdTime = uint64(time.Now().UnixMicro()) + uint64(i*1000) // Staggered TSBPD
		s.packetRing.Push(p)
	}

	// Start EventLoop
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	s.EventLoop(ctx, &wg) // wg.Done() called internally

	// Verify all packets were processed
	drained := s.metrics.SendRingDrained.Load()
	inserted := s.metrics.SendBtreeInserted.Load()

	require.Equal(t, uint64(numPackets), drained, "All packets should be drained")
	require.Equal(t, uint64(numPackets), inserted, "All packets should be inserted to btree")

	t.Logf("Wraparound test: start=0x%08X, drained=%d, inserted=%d",
		startSeq, drained, inserted)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Drop Ticker Tests (sender-specific - receiver doesn't drop)
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_DropTicker_Fires(t *testing.T) {
	s := createEventLoopSender(t)

	// Run EventLoop for 250ms (drop ticker fires every 100ms)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	s.EventLoop(ctx, &wg) // wg.Done() called internally

	// Check drop ticker fired
	dropFires := s.metrics.SendEventLoopDropFires.Load()
	require.GreaterOrEqual(t, dropFires, uint64(2), "Drop ticker should fire at least twice in 250ms")

	t.Logf("Drop ticker fires: %d", dropFires)
}

func TestEventLoop_DropOldPackets(t *testing.T) {
	s, cleanup := createEventLoopSenderWithContext(t)
	defer cleanup()

	// Insert packets with old TSBPD time (should be dropped)
	oldTime := uint64(time.Now().UnixMicro()) - 2*s.dropThreshold // 2 seconds ago
	for i := uint32(0); i < 5; i++ {
		p := createTestPacket(i)
		p.Header().PktTsbpdTime = oldTime
		s.packetBtree.Insert(p)
	}

	// Insert packets with recent TSBPD time (should NOT be dropped)
	recentTime := uint64(time.Now().UnixMicro())
	for i := uint32(5); i < 10; i++ {
		p := createTestPacket(i)
		p.Header().PktTsbpdTime = recentTime
		s.packetBtree.Insert(p)
	}

	require.Equal(t, 10, s.packetBtree.Len())

	// Run drop check
	s.dropOldPacketsEventLoop(uint64(time.Now().UnixMicro()))

	// Old packets should be dropped, recent packets should remain
	require.Equal(t, 5, s.packetBtree.Len())
	require.Equal(t, uint64(5), s.metrics.CongestionSendPktDrop.Load())
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent Push Test (matching receiver's high-concurrency scenarios)
// ═══════════════════════════════════════════════════════════════════════════════

func TestEventLoop_ConcurrentPush(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	s := createEventLoopSender(t)

	// Start EventLoop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var eventLoopWg sync.WaitGroup
	done := make(chan struct{})
	eventLoopWg.Add(1)
	go func() {
		s.EventLoop(ctx, &eventLoopWg) // wg.Done() called internally
		close(done)
	}()

	// Multiple goroutines pushing packets concurrently
	const numGoroutines = 4
	const packetsPerGoroutine = 250
	totalPackets := numGoroutines * packetsPerGoroutine

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			baseSeq := uint32(goroutineID * packetsPerGoroutine)
			for i := 0; i < packetsPerGoroutine; i++ {
				p := createTestPacket(baseSeq + uint32(i))
				p.Header().PktTsbpdTime = uint64(time.Now().UnixMicro())
				for !s.packetRing.Push(p) {
					// Ring full, wait briefly
					time.Sleep(10 * time.Microsecond)
				}
			}
		}(g)
	}

	wg.Wait()

	// Wait for EventLoop to process
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	drained := s.metrics.SendRingDrained.Load()
	require.Equal(t, uint64(totalPackets), drained,
		"All concurrent packets should be drained")

	t.Logf("Concurrent push: %d goroutines × %d packets = %d total, drained=%d",
		numGoroutines, packetsPerGoroutine, totalPackets, drained)
}
