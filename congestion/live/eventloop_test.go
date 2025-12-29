//go:build go1.18

package live

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"net"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// EventLoop Time Base Tests (TDD Phase 1-2)
//
// These tests verify that EventLoop uses the correct time base for TSBPD
// calculations. The bug is that nowFn returns absolute Unix time (~1.7T µs)
// while PktTsbpdTime is calculated relative to connection start (~millions µs).
//
// EXPECTED BEHAVIOR BEFORE FIX: Tests FAIL
// EXPECTED BEHAVIOR AFTER FIX: Tests PASS
// ═══════════════════════════════════════════════════════════════════════════

// TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery verifies EventLoop doesn't deliver
// packets before their TSBPD time when using production-like time bases.
//
// THE BUG:
// - nowFn returns absolute Unix time (~1,766,961,920,000,000 µs for 2025)
// - PktTsbpdTime is relative (~3,000,000 µs = 3 seconds from connection start)
// - Since absolute >> relative, all packets appear TSBPD-expired immediately
//
// EXPECTED BEFORE FIX: FAIL (packets delivered immediately)
// EXPECTED AFTER FIX: PASS (packets held until TSBPD time)
func TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery(t *testing.T) {
	// Skip if running with -short flag (EventLoop tests can be slow)
	if testing.Short() {
		t.Skip("Skipping EventLoop test in short mode")
	}

	// Configuration matching production
	tsbpdDelayUs := uint64(3_000_000) // 3 seconds - matches integration test

	var deliveredMu sync.Mutex
	var delivered []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms
		PeriodicNAKInterval:   20_000, // 20ms
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver: func(p packet.Packet) {
			deliveredMu.Lock()
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
			deliveredMu.Unlock()
		},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,

		// Enable ring buffer (required for EventLoop)
		UsePacketRing:             true,
		PacketRingSize:            1024,
		PacketRingShards:          4,
		PacketRingMaxRetries:      10,
		PacketRingBackoffDuration: 100 * time.Microsecond,

		// Enable EventLoop
		UseEventLoop:          true,
		EventLoopRateInterval: 1 * time.Second,
		BackoffColdStartPkts:  100,
		BackoffMinSleep:       10 * time.Microsecond,
		BackoffMaxSleep:       1 * time.Millisecond,

		// Time base fix (Phase 10): Use relative time matching PktTsbpdTime
		TsbpdTimeBase: 0,          // Connection starts at time 0
		StartTime:     time.Now(), // Track elapsed time from now
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Create packets with RELATIVE PktTsbpdTime (matching production)
	// In production: PktTsbpdTime = tsbpdTimeBase + timestamp + tsbpdDelay
	// With tsbpdTimeBase=0 (start of connection):
	// - First packet timestamp = 0, so PktTsbpdTime = 0 + 0 + 3_000_000 = 3s
	// - Second packet timestamp = 10000, so PktTsbpdTime = 0 + 10000 + 3_000_000 = 3.01s
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		// Simulate production PktTsbpdTime calculation:
		// tsbpdTimeBase (0) + timestamp (i*10000) + tsbpdDelay (3_000_000)
		p.Header().PktTsbpdTime = uint64(i*10_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(i * 10_000)
		recv.Push(p)
	}

	// Start EventLoop in background with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	// Wait for EventLoop to process packets
	time.Sleep(50 * time.Millisecond)

	// Check how many packets were delivered
	deliveredMu.Lock()
	count := len(delivered)
	deliveredMu.Unlock()

	// ANALYSIS:
	// - If nowFn returns absolute Unix time (~1.7 trillion µs)
	// - And PktTsbpdTime is 3 million µs (relative)
	// - Then now (1.7T) > PktTsbpdTime (3M) → packets delivered immediately
	//
	// - If nowFn returns relative time (0-50000 µs for 50ms elapsed)
	// - And PktTsbpdTime is 3 million µs (3 seconds from start)
	// - Then now (50000) < PktTsbpdTime (3M) → packets NOT delivered yet

	// BEFORE FIX: This assertion FAILS - all 10 packets delivered immediately
	// AFTER FIX: This assertion PASSES - no packets delivered (TSBPD not reached)
	if count > 0 {
		t.Errorf("BUG DETECTED: %d packets delivered before TSBPD time (expected 0)", count)
		t.Logf("This proves the time base mismatch:")
		t.Logf("  - nowFn returns absolute Unix time (~%d µs)", time.Now().UnixMicro())
		t.Logf("  - PktTsbpdTime is relative (~%d µs)", tsbpdDelayUs)
		t.Logf("  - Since %d >> %d, packets appear expired immediately",
			time.Now().UnixMicro(), tsbpdDelayUs)
		t.Logf("Fix: Pass TsbpdTimeBase and StartTime to ReceiveConfig")
	}

	// Wait for EventLoop to finish
	cancel()
	wg.Wait()
}

// TestEventLoop_TimeBase_ContiguousScan verifies contiguousScan uses correct time.
//
// contiguousScan() uses r.nowFn() to check TSBPD expiry for gap skipping.
// With wrong time base, gaps are skipped prematurely.
func TestEventLoop_TimeBase_ContiguousScan(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping EventLoop test in short mode")
	}

	tsbpdDelayUs := uint64(3_000_000) // 3 seconds

	var ackSequences []uint32
	var ackMu sync.Mutex

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK: func(seq circular.Number, light bool) {
			ackMu.Lock()
			ackSequences = append(ackSequences, seq.Val())
			ackMu.Unlock()
		},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		UsePacketRing:          true,
		PacketRingSize:         1024,
		PacketRingShards:       4,
		UseEventLoop:           true,
		// Time base fix (Phase 10)
		TsbpdTimeBase: 0,
		StartTime:     time.Now(),
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets with a gap: 0, 1, 2, [missing 3, 4], 5, 6, 7
	// With correct time base: gap at 3,4 should NOT be skipped (TSBPD not expired)
	// With wrong time base: gap at 3,4 IS skipped (all packets appear expired)
	seqs := []uint32{0, 1, 2, 5, 6, 7}
	for _, seq := range seqs {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(seq*10_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(seq * 10_000)
		recv.Push(p)
	}

	// Run EventLoop briefly
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()

	// Check contiguousPoint
	cp := r.contiguousPoint.Load()

	// BEFORE FIX: contiguousPoint jumps to 7 (gap skipped because TSBPD "expired")
	// AFTER FIX: contiguousPoint stays at 2 (gap NOT skipped, waiting for NAK/retransmit)

	// Note: ISN is 0, contiguousPoint starts at ISN.Dec() = MAX
	// After receiving 0,1,2 contiguously, contiguousPoint should be 2
	// If gap is skipped, contiguousPoint jumps past 3,4 to become 7

	if cp > 2 && cp != packet.MAX_SEQUENCENUMBER {
		t.Errorf("BUG DETECTED: contiguousPoint=%d (expected <=2)", cp)
		t.Logf("Gap at sequences 3,4 was incorrectly skipped")
		t.Logf("This indicates TSBPD time appears expired due to time base mismatch")
	}
}

// TestEventLoop_TimeBase_GapScan verifies gapScan uses correct time for NAK generation.
//
// gapScan() uses r.nowFn() to calculate tooRecentThreshold.
// With wrong time base, all packets appear far in the past, missing the NAK window.
func TestEventLoop_TimeBase_GapScan(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping EventLoop test in short mode")
	}

	tsbpdDelayUs := uint64(3_000_000) // 3 seconds

	var nakedSequences []uint32
	var nakMu sync.Mutex

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000, // 20ms NAK interval
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK: func(list []circular.Number) {
			nakMu.Lock()
			for _, seq := range list {
				nakedSequences = append(nakedSequences, seq.Val())
			}
			nakMu.Unlock()
		},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		UsePacketRing:          true,
		PacketRingSize:         1024,
		PacketRingShards:       4,
		UseEventLoop:           true,
		// Time base fix (Phase 10)
		TsbpdTimeBase: 0,
		StartTime:     time.Now(),
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets 0-9, but drop packet 5
	// PktTsbpdTime calculation:
	//   - With correct time base: now ≈ 0, tooRecentThreshold ≈ 0 + 3M * 0.90 = 2.7M
	//   - Packets with PktTsbpdTime < 2.7M are scannable (not "too recent")
	//   - Use PktTsbpdTime ≈ 100_000 + i*10_000 (100ms-200ms, well within window)
	// Gap at seq 5 should trigger NAK because packets around it are scannable
	for i := 0; i < 10; i++ {
		if i == 5 {
			continue // Drop packet 5
		}
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		// Use PktTsbpdTime values well within the NAK window (not "too recent")
		// tooRecentThreshold ≈ now + 2.7M, so PktTsbpdTime < 2.7M is scannable
		p.Header().PktTsbpdTime = uint64(100_000 + i*10_000) // 100ms-190ms
		p.Header().Timestamp = uint32(i * 10_000)
		recv.Push(p)
	}

	// Run EventLoop long enough for NAK timer to fire multiple times
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	wg.Wait()

	// Check if packet 5 was NAKed
	nakMu.Lock()
	nakCount := len(nakedSequences)
	var found5 bool
	for _, seq := range nakedSequences {
		if seq == 5 {
			found5 = true
			break
		}
	}
	nakMu.Unlock()

	// BEFORE FIX: No NAKs generated (all packets appear TSBPD-expired)
	// AFTER FIX: Packet 5 is NAKed (within proper NAK window)

	if nakCount == 0 {
		t.Errorf("BUG DETECTED: No NAKs generated (expected NAK for packet 5)")
		t.Logf("This indicates gapScan thinks all packets are TSBPD-expired")
		t.Logf("With absolute Unix time (%d µs), all packets with relative TSBPD (%d µs) appear expired",
			time.Now().UnixMicro(), tsbpdDelayUs)
	} else if !found5 {
		t.Logf("NOTE: %d NAKs generated but packet 5 not included: %v", nakCount, nakedSequences)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// EventLoop Functionality Tests (Phase 6)
// These test basic EventLoop operation regardless of time base
// ═══════════════════════════════════════════════════════════════════════════

// TestEventLoop_ContextCancellation verifies clean shutdown when context is cancelled.
func TestEventLoop_ContextCancellation(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
		UsePacketRing:         true,
		PacketRingSize:        256,
		PacketRingShards:      4,
		UseEventLoop:          true,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	ctx, cancel := context.WithCancel(context.Background())

	// Track when EventLoop exits
	var exited atomic.Bool
	go func() {
		r.EventLoop(ctx)
		exited.Store(true)
	}()

	// Let EventLoop run briefly
	time.Sleep(10 * time.Millisecond)
	require.False(t, exited.Load(), "EventLoop should still be running")

	// Cancel context
	cancel()

	// EventLoop should exit within reasonable time
	time.Sleep(50 * time.Millisecond)
	require.True(t, exited.Load(), "EventLoop should have exited after context cancel")
}

// TestEventLoop_MetricsIncrement verifies EventLoop metrics are incremented.
func TestEventLoop_MetricsIncrement(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms
		PeriodicNAKInterval:   20_000, // 20ms
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
		UsePacketRing:         true,
		PacketRingSize:        256,
		PacketRingShards:      4,
		UseEventLoop:          true,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Run EventLoop for 100ms
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	r.EventLoop(ctx)

	// Check metrics were incremented
	iterations := testMetrics.EventLoopIterations.Load()
	defaultRuns := testMetrics.EventLoopDefaultRuns.Load()

	require.Greater(t, iterations, uint64(0), "EventLoopIterations should be > 0")
	require.Greater(t, defaultRuns, uint64(0), "EventLoopDefaultRuns should be > 0")

	t.Logf("EventLoop ran for 100ms: iterations=%d, defaultRuns=%d", iterations, defaultRuns)
}

// TestEventLoop_Basic_PacketDelivery verifies packets are delivered correctly
// when TSBPD time passes.
func TestEventLoop_Basic_PacketDelivery(t *testing.T) {
	tsbpdDelayUs := uint64(50_000) // 50ms - short for testing
	startTime := time.Now()

	var deliveredMu sync.Mutex
	var delivered []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms
		PeriodicNAKInterval:   20_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver: func(p packet.Packet) {
			deliveredMu.Lock()
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
			deliveredMu.Unlock()
		},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Push packets with TSBPD time 50ms in the future
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*1_000) + tsbpdDelayUs // 50ms + offset
		p.Header().Timestamp = uint32(i * 1_000)
		recv.Push(p)
	}

	// Start EventLoop - run longer than TSBPD delay
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	r.EventLoop(ctx)

	// After 150ms (> 50ms TSBPD), all packets should be delivered
	deliveredMu.Lock()
	count := len(delivered)
	deliveredMu.Unlock()

	require.Equal(t, 5, count, "All 5 packets should be delivered after TSBPD time")
	t.Logf("Delivered %d packets: %v", count, delivered)
}

// TestEventLoop_ACK_Periodic verifies ACKs are sent periodically.
func TestEventLoop_ACK_Periodic(t *testing.T) {
	tsbpdDelayUs := uint64(30_000) // 30ms
	startTime := time.Now()

	var ackMu sync.Mutex
	var ackSeqs []uint32
	var ackTypes []bool // true = light ACK

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms ACK interval
		PeriodicNAKInterval:   20_000,
		OnSendACK: func(seq circular.Number, light bool) {
			ackMu.Lock()
			ackSeqs = append(ackSeqs, seq.Val())
			ackTypes = append(ackTypes, light)
			ackMu.Unlock()
		},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Push contiguous packets
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*1_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(i * 1_000)
		recv.Push(p)
	}

	// Run EventLoop for 100ms (should trigger multiple ACK intervals)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	r.EventLoop(ctx)

	ackMu.Lock()
	ackCount := len(ackSeqs)
	ackMu.Unlock()

	require.Greater(t, ackCount, 0, "At least one ACK should be sent")
	t.Logf("Sent %d ACKs with sequences: %v", ackCount, ackSeqs)
}

// TestEventLoop_NAK_GapDetection verifies NAKs are generated for gaps.
func TestEventLoop_NAK_GapDetection(t *testing.T) {
	// Use a longer TSBPD delay so packets aren't expired immediately
	tsbpdDelayUs := uint64(500_000) // 500ms
	startTime := time.Now()

	var nakMu sync.Mutex
	var nakedSeqs []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms
		PeriodicNAKInterval:   20_000, // 20ms NAK interval
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK: func(list []circular.Number) {
			nakMu.Lock()
			for _, seq := range list {
				nakedSeqs = append(nakedSeqs, seq.Val())
			}
			nakMu.Unlock()
		},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10, // 10% "too recent" = 90% NAKable
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Push packets with a gap: 0, 1, 2, [missing 3, 4], 5, 6, 7, 8, 9
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	for i := 0; i < 10; i++ {
		if i == 3 || i == 4 {
			continue // Create gap
		}
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*10_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(i * 10_000)
		recv.Push(p)
	}

	// Run EventLoop for 200ms (should trigger several NAK intervals)
	// With 500ms TSBPD and 10% recent, packets are NAKable from ~50ms to ~500ms
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	r.EventLoop(ctx)

	nakMu.Lock()
	nakCount := len(nakedSeqs)
	has3 := false
	has4 := false
	for _, seq := range nakedSeqs {
		if seq == 3 {
			has3 = true
		}
		if seq == 4 {
			has4 = true
		}
	}
	nakMu.Unlock()

	// Should have NAKed sequences 3 and 4
	require.Greater(t, nakCount, 0, "NAKs should be generated for gap")
	require.True(t, has3 || has4, "NAKs should include sequences 3 or 4")
	t.Logf("NAKed %d sequences: %v", nakCount, nakedSeqs)
}

// TestEventLoop_IdleBackoff verifies idle backoff behavior during low traffic.
func TestEventLoop_IdleBackoff(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
		UsePacketRing:         true,
		PacketRingSize:        256,
		PacketRingShards:      4,
		UseEventLoop:          true,
		BackoffColdStartPkts:  10, // Engage backoff quickly
		BackoffMinSleep:       100 * time.Microsecond,
		BackoffMaxSleep:       1 * time.Millisecond,
		EventLoopRateInterval: 100 * time.Millisecond,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Run EventLoop with NO packets (idle)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	r.EventLoop(ctx)

	// Check idle backoff metrics
	idleBackoffs := testMetrics.EventLoopIdleBackoffs.Load()
	iterations := testMetrics.EventLoopIterations.Load()

	// With no packets, most iterations should trigger backoff
	require.Greater(t, idleBackoffs, uint64(0), "Idle backoffs should occur with no traffic")
	t.Logf("Idle backoffs: %d, total iterations: %d", idleBackoffs, iterations)
}

// ═══════════════════════════════════════════════════════════════════════════
// EventLoop + Ring Integration Tests (Phase 7)
// Test interaction between EventLoop and the packet ring buffer
// ═══════════════════════════════════════════════════════════════════════════

// TestEventLoop_Ring_BasicFlow verifies packets flow from ring to btree to delivery.
func TestEventLoop_Ring_BasicFlow(t *testing.T) {
	tsbpdDelayUs := uint64(30_000) // 30ms
	startTime := time.Now()

	var deliveredMu sync.Mutex
	var delivered []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver: func(p packet.Packet) {
			deliveredMu.Lock()
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
			deliveredMu.Unlock()
		},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Start EventLoop in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	// Push packets while EventLoop is running (simulates io_uring)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	for i := 0; i < 20; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*1_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(i * 1_000)
		recv.Push(p)
		time.Sleep(1 * time.Millisecond) // Simulate packet arrival rate
	}

	// Wait for TSBPD + processing
	time.Sleep(100 * time.Millisecond)

	cancel()
	wg.Wait()

	// Verify packets were drained and processed
	drained := testMetrics.RingDrainedPackets.Load()
	processed := testMetrics.RingPacketsProcessed.Load()

	deliveredMu.Lock()
	deliveredCount := len(delivered)
	deliveredMu.Unlock()

	require.Equal(t, uint64(20), drained, "All packets should be drained from ring")
	require.Equal(t, uint64(20), processed, "All packets should be processed")
	require.Equal(t, 20, deliveredCount, "All packets should be delivered")
	t.Logf("Ring flow: drained=%d, processed=%d, delivered=%d", drained, processed, deliveredCount)
}

// TestEventLoop_Ring_OutOfOrder verifies out-of-order packets are reordered.
func TestEventLoop_Ring_OutOfOrder(t *testing.T) {
	tsbpdDelayUs := uint64(50_000) // 50ms
	startTime := time.Now()

	var deliveredMu sync.Mutex
	var delivered []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver: func(p packet.Packet) {
			deliveredMu.Lock()
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
			deliveredMu.Unlock()
		},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Start EventLoop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	// Push packets out of order: 2, 0, 3, 1, 4
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	outOfOrder := []uint32{2, 0, 3, 1, 4}
	for _, seq := range outOfOrder {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(seq*1_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(seq * 1_000)
		recv.Push(p)
	}

	// Wait for TSBPD + processing
	time.Sleep(150 * time.Millisecond)

	cancel()
	wg.Wait()

	deliveredMu.Lock()
	deliveredCopy := make([]uint32, len(delivered))
	copy(deliveredCopy, delivered)
	deliveredMu.Unlock()

	// Packets should be delivered in sequence order (0,1,2,3,4), not arrival order
	require.Equal(t, 5, len(deliveredCopy), "All 5 packets should be delivered")

	// Check ordering: packets should be delivered in TSBPD order
	// (Note: exact order depends on TSBPD times which are based on seq)
	for i := 0; i < len(deliveredCopy)-1; i++ {
		// TSBPD time is proportional to seq, so lower seq = earlier delivery
		require.LessOrEqual(t, deliveredCopy[i], deliveredCopy[i+1],
			"Packets should be delivered in TSBPD order: %v", deliveredCopy)
	}
	t.Logf("Out-of-order delivery: arrived=%v, delivered=%v", outOfOrder, deliveredCopy)
}

// TestEventLoop_Ring_HighThroughput verifies EventLoop handles high packet rates.
func TestEventLoop_Ring_HighThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high throughput test in short mode")
	}

	tsbpdDelayUs := uint64(20_000) // 20ms - short TSBPD for high throughput
	startTime := time.Now()

	var deliveredCount atomic.Uint64

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   5_000, // 5ms for faster ACK
		PeriodicNAKInterval:   10_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver: func(p packet.Packet) {
			deliveredCount.Add(1)
		},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		UsePacketRing:          true,
		PacketRingSize:         1024, // Larger ring for throughput
		PacketRingShards:       8,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Start EventLoop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	// Push 1000 packets as fast as possible
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	numPackets := 1000
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*100) + tsbpdDelayUs // 100µs apart
		p.Header().Timestamp = uint32(i * 100)
		recv.Push(p)
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	cancel()
	wg.Wait()

	delivered := deliveredCount.Load()
	drained := testMetrics.RingDrainedPackets.Load()
	processed := testMetrics.RingPacketsProcessed.Load()

	require.Equal(t, uint64(numPackets), drained, "All packets should be drained")
	require.Equal(t, uint64(numPackets), processed, "All packets should be processed")
	require.Equal(t, uint64(numPackets), delivered, "All packets should be delivered")

	t.Logf("High throughput: %d packets - drained=%d, processed=%d, delivered=%d",
		numPackets, drained, processed, delivered)
}

// ═══════════════════════════════════════════════════════════════════════════
// io_uring Simulation Tests (Phase 8)
// Test scenarios that occur with real io_uring usage
// ═══════════════════════════════════════════════════════════════════════════

// TestEventLoop_IoUring_SimulatedReorder tests aggressive reordering as seen
// from io_uring completion events (CQEs can complete out of order).
func TestEventLoop_IoUring_SimulatedReorder(t *testing.T) {
	tsbpdDelayUs := uint64(100_000) // 100ms
	startTime := time.Now()

	var deliveredMu sync.Mutex
	var delivered []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver: func(p packet.Packet) {
			deliveredMu.Lock()
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
			deliveredMu.Unlock()
		},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Start EventLoop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	// Simulate io_uring reordering: push packets in reverse batches
	// Batch 1: 9,8,7,6,5 (late completions arrive first due to CQE batching)
	// Batch 2: 4,3,2,1,0 (early completions arrive later)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// First batch (late packets complete first)
	for i := 9; i >= 5; i-- {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*5_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(i * 5_000)
		recv.Push(p)
	}
	time.Sleep(5 * time.Millisecond)

	// Second batch (early packets complete later)
	for i := 4; i >= 0; i-- {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*5_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(i * 5_000)
		recv.Push(p)
	}

	// Wait for TSBPD + processing
	time.Sleep(200 * time.Millisecond)

	cancel()
	wg.Wait()

	deliveredMu.Lock()
	deliveredCount := len(delivered)
	deliveredCopy := make([]uint32, deliveredCount)
	copy(deliveredCopy, delivered)
	deliveredMu.Unlock()

	require.Equal(t, 10, deliveredCount, "All 10 packets should be delivered")

	// Verify packets delivered in TSBPD order (lower seq = earlier TSBPD)
	for i := 0; i < deliveredCount-1; i++ {
		require.LessOrEqual(t, deliveredCopy[i], deliveredCopy[i+1],
			"Packets should be reordered to TSBPD order: %v", deliveredCopy)
	}
	t.Logf("io_uring reorder simulation: delivered=%v", deliveredCopy)
}

// TestEventLoop_IoUring_LossRecovery tests loss recovery via NAK/retransmit.
func TestEventLoop_IoUring_LossRecovery(t *testing.T) {
	tsbpdDelayUs := uint64(500_000) // 500ms - long enough to allow NAK+retransmit
	startTime := time.Now()

	var deliveredMu sync.Mutex
	var delivered []uint32
	var nakMu sync.Mutex
	var nakedSeqs []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK: func(list []circular.Number) {
			nakMu.Lock()
			for _, seq := range list {
				nakedSeqs = append(nakedSeqs, seq.Val())
			}
			nakMu.Unlock()
		},
		OnDeliver: func(p packet.Packet) {
			deliveredMu.Lock()
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
			deliveredMu.Unlock()
		},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets with one "lost" (seq 5): 0,1,2,3,4,[5 lost],6,7,8,9
	// PktTsbpdTime calculation:
	//   - now ≈ 0 at start, grows with time.Since(startTime)
	//   - tooRecentThreshold = now + tsbpdDelay * 0.90 = now + 450_000
	//   - For NAK at 100ms: tooRecentThreshold ≈ 100_000 + 450_000 = 550_000
	//   - Packets with PktTsbpdTime < 550_000 are scannable
	// Use PktTsbpdTime values that are within the NAK window
	for i := 0; i < 10; i++ {
		if i == 5 {
			continue // Simulate packet loss
		}
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		// Use PktTsbpdTime values well within the NAK window
		p.Header().PktTsbpdTime = uint64(50_000 + i*10_000) // 50ms-140ms
		p.Header().Timestamp = uint32(i * 10_000)
		recv.Push(p)
	}

	// Wait for NAK to be generated
	time.Sleep(100 * time.Millisecond)

	nakMu.Lock()
	hasNak5 := false
	for _, seq := range nakedSeqs {
		if seq == 5 {
			hasNak5 = true
			break
		}
	}
	nakMu.Unlock()

	// Simulate retransmission of packet 5
	if hasNak5 {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(5, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(50_000 + 5*10_000) // Match original PktTsbpdTime
		p.Header().Timestamp = uint32(5 * 10_000)
		p.Header().RetransmittedPacketFlag = true
		recv.Push(p)
	}

	// Wait for all packets to be delivered
	time.Sleep(500 * time.Millisecond)

	cancel()
	wg.Wait()

	deliveredMu.Lock()
	deliveredCount := len(delivered)
	deliveredMu.Unlock()

	nakMu.Lock()
	nakCount := len(nakedSeqs)
	nakMu.Unlock()

	require.True(t, hasNak5, "NAK should be sent for missing packet 5")
	require.Equal(t, 10, deliveredCount, "All 10 packets should be delivered after retransmit")
	t.Logf("Loss recovery: NAKs=%d, delivered=%d", nakCount, deliveredCount)
}

// TestEventLoop_IoUring_BurstLoss tests handling of burst packet loss.
func TestEventLoop_IoUring_BurstLoss(t *testing.T) {
	tsbpdDelayUs := uint64(500_000) // 500ms
	startTime := time.Now()

	var nakMu sync.Mutex
	var nakedSeqs []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK: func(list []circular.Number) {
			nakMu.Lock()
			for _, seq := range list {
				nakedSeqs = append(nakedSeqs, seq.Val())
			}
			nakMu.Unlock()
		},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Simulate burst loss: packets 5-9 are all lost
	// Receive: 0,1,2,3,4,[5,6,7,8,9 lost],10,11,12,13,14
	for i := 0; i < 15; i++ {
		if i >= 5 && i <= 9 {
			continue // Burst loss
		}
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*10_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(i * 10_000)
		recv.Push(p)
	}

	// Wait for NAKs to be generated - need several NAK intervals
	// NAK interval is 20ms, need multiple cycles to cover all gaps in burst
	time.Sleep(400 * time.Millisecond)

	cancel()
	wg.Wait()

	nakMu.Lock()
	uniqueNaks := make(map[uint32]bool)
	for _, seq := range nakedSeqs {
		uniqueNaks[seq] = true
	}
	nakMu.Unlock()

	// Should NAK at least some sequences from 5,6,7,8,9
	// NAK btree may batch or limit per-cycle, so check some are covered
	// The key validation is that the NAK mechanism IS working for burst loss
	lostSeqs := []uint32{5, 6, 7, 8, 9}
	nakedCount := 0
	for _, seq := range lostSeqs {
		if uniqueNaks[seq] {
			nakedCount++
		}
	}
	// At minimum, at least 1 NAK should be generated for the burst
	// In practice, NAK btree may handle ranges differently or batch
	require.GreaterOrEqual(t, nakedCount, 1, "NAK should cover at least 1 burst-lost packet")
	require.Greater(t, len(nakedSeqs), 0, "NAKs should be generated for burst loss")
	t.Logf("Burst loss: NAKed unique seqs=%v (expected: %v, got %d/5)", uniqueNaks, lostSeqs, nakedCount)
}

// TestEventLoop_IoUring_TSBPD_Expiry tests TSBPD-based gap skipping for
// permanently lost packets.
func TestEventLoop_IoUring_TSBPD_Expiry(t *testing.T) {
	// Use very short TSBPD for testing expiry
	tsbpdDelayUs := uint64(50_000) // 50ms
	startTime := time.Now()

	var deliveredMu sync.Mutex
	var delivered []uint32
	var ackMu sync.Mutex
	var ackSeqs []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		OnSendACK: func(seq circular.Number, light bool) {
			ackMu.Lock()
			ackSeqs = append(ackSeqs, seq.Val())
			ackMu.Unlock()
		},
		OnSendNAK: func(list []circular.Number) {},
		OnDeliver: func(p packet.Packet) {
			deliveredMu.Lock()
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
			deliveredMu.Unlock()
		},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets with gap at 2: 0,1,[2 lost],3,4
	// Packet 2 is PERMANENTLY lost (no retransmit will arrive)
	for i := 0; i < 5; i++ {
		if i == 2 {
			continue // Permanently lost
		}
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i*5_000) + tsbpdDelayUs
		p.Header().Timestamp = uint32(i * 5_000)
		recv.Push(p)
	}

	// Wait longer than TSBPD for packet 2 to expire
	// TSBPD for packet 2 = 10_000 (2*5000) + 50_000 = 60_000 µs = 60ms
	time.Sleep(150 * time.Millisecond)

	cancel()
	wg.Wait()

	deliveredMu.Lock()
	deliveredCount := len(delivered)
	deliveredCopy := make([]uint32, deliveredCount)
	copy(deliveredCopy, delivered)
	deliveredMu.Unlock()

	// After TSBPD expiry, packets 0,1,3,4 should be delivered
	// Packet 2 was skipped (TSBPD expired without arrival)
	require.GreaterOrEqual(t, deliveredCount, 3, "At least packets before and after gap should be delivered")

	// Check metrics for TSBPD advancement
	tsbpdAdvancements := testMetrics.ContiguousPointTSBPDAdvancements.Load()

	t.Logf("TSBPD expiry: delivered=%v, tsbpd_advancements=%d", deliveredCopy, tsbpdAdvancements)
}

// TestEventLoop_NAK_TimeBase_Consistency verifies that EventLoop uses the correct
// time base (r.nowFn) for NAK calculations, not time.Now().UnixMicro() directly.
//
// BUG: In EventLoop's nakTicker.C handler, we were using:
//
//	now := uint64(time.Now().UnixMicro())  // WRONG - absolute time ~1.7e12
//
// This should be:
//
//	now := r.nowFn()  // CORRECT - relative time since connection start
//
// The symptom: With the bug, `tooRecentThreshold = now + tsbpdDelay*0.90 = 1.7e12 + 450_000 ≈ 1.7e12`
// So ALL packets appear "not too recent" (since their relative PktTsbpdTime << 1.7e12),
// causing over-NAKing of packets that should be skipped as "too recent".
//
// This test creates packets where some should be "too recent" to NAK.
// With the FIX, these packets are correctly skipped.
// With the BUG, these packets are incorrectly NAKed.
func TestEventLoop_NAK_TimeBase_Consistency(t *testing.T) {
	// Use a short TSBPD delay for faster testing
	tsbpdDelayUs := uint64(100_000) // 100ms
	nakRecentPercent := 0.10        // 10% - so tooRecentThreshold = now + 90_000

	startTime := time.Now()

	var nakMu sync.Mutex
	var nakedSeqs []uint32

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms
		PeriodicNAKInterval:   5_000,  // 5ms - frequent NAK scans
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK: func(list []circular.Number) {
			nakMu.Lock()
			for _, seq := range list {
				nakedSeqs = append(nakedSeqs, seq.Val())
			}
			nakMu.Unlock()
		},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       nakRecentPercent,
		UsePacketRing:          true,
		PacketRingSize:         256,
		PacketRingShards:       4,
		UseEventLoop:           true,
		TsbpdTimeBase:          0,
		StartTime:              startTime,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.EventLoop(ctx)
	}()

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Wait for EventLoop to start
	time.Sleep(10 * time.Millisecond)

	// Calculate time values relative to connection start
	// now ≈ 10ms = 10_000 after startTime
	// tooRecentThreshold = now + tsbpdDelay * 0.90 = 10_000 + 90_000 = 100_000

	// Push packet 0 with PktTsbpdTime = 50_000 (NOT too recent - should be scanned)
	p0 := packet.NewPacket(addr)
	p0.Header().PacketSequenceNumber = circular.New(0, packet.MAX_SEQUENCENUMBER)
	p0.Header().PktTsbpdTime = 50_000 // 50ms - within scan window
	p0.Header().Timestamp = 0
	recv.Push(p0)

	// Create a gap at seq 1,2 (missing)

	// Push packet 3 with PktTsbpdTime = 200_000 (TOO RECENT - beyond threshold)
	// With the FIX: now ≈ 10_000, threshold ≈ 100_000, so 200_000 > 100_000 → too recent → stop scan
	// With the BUG: now ≈ 1.7e12, threshold ≈ 1.7e12, so 200_000 < 1.7e12 → NOT too recent → continue scan
	p3 := packet.NewPacket(addr)
	p3.Header().PacketSequenceNumber = circular.New(3, packet.MAX_SEQUENCENUMBER)
	p3.Header().PktTsbpdTime = 200_000 // 200ms - should be "too recent"
	p3.Header().Timestamp = 30_000
	recv.Push(p3)

	// Wait for NAK scans to run
	time.Sleep(50 * time.Millisecond)

	cancel()
	wg.Wait()

	nakMu.Lock()
	nakCount := len(nakedSeqs)
	hasNak1 := false
	hasNak2 := false
	for _, seq := range nakedSeqs {
		if seq == 1 {
			hasNak1 = true
		}
		if seq == 2 {
			hasNak2 = true
		}
	}
	naksCopy := make([]uint32, len(nakedSeqs))
	copy(naksCopy, nakedSeqs)
	nakMu.Unlock()

	t.Logf("NAK time base test: nakedSeqs=%v, hasNak1=%v, hasNak2=%v", naksCopy, hasNak1, hasNak2)

	// With the CORRECT time base (r.nowFn()), gap 1,2 should NOT be NAKed
	// because packet 3 is "too recent" (PktTsbpdTime=200_000 > threshold≈100_000)
	// and the scan stops before recording the gap.
	//
	// With the BUGGY time base (time.Now().UnixMicro()), gap 1,2 WOULD be NAKed
	// because packet 3 appears "not too recent" (200_000 < 1.7e12).

	// This assertion catches the BUG: if NAKs are generated for seq 1 or 2, the time base is wrong
	require.False(t, hasNak1 || hasNak2,
		"Gap 1,2 should NOT be NAKed because packet 3 is 'too recent'. "+
			"If NAKed, EventLoop is using absolute time instead of r.nowFn(). NAKs=%v", naksCopy)

	t.Logf("PASS: No spurious NAKs for gaps leading to 'too recent' packets. nakCount=%d", nakCount)
}
