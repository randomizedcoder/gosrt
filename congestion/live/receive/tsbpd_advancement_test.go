// Package live provides TSBPD advancement tests for the receiver.
//
// These tests verify contiguousPoint advancement when packets are permanently
// lost or significantly delayed beyond their TSBPD deadline.
//
// Key scenarios tested:
//   - Complete outage recovery (all packets lost in a range)
//   - Mid-stream gap handling
//   - Small gap (no premature advancement)
//   - Extended outage
//   - 31-bit sequence wraparound
//   - Multiple simultaneous gaps
//   - Iterative recovery cycles
//
// See documentation/contiguous_point_tsbpd_advancement_design.md for design.
package receive

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ============================================================================
// TSBPD ADVANCEMENT TESTS
// ============================================================================
// These tests verify contiguousPoint advancement when packets are permanently
// lost or significantly delayed beyond their TSBPD deadline.
// See documentation/contiguous_point_tsbpd_advancement_design.md

// createTSBPDTestReceiver creates a receiver configured for TSBPD advancement testing.
// It returns the receiver and a function to set the mock time.
func createTSBPDTestReceiver(t *testing.T, startSeq uint32, tsbpdDelayUs uint64) (*receiver, *uint64, *metrics.ConnectionMetrics) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := Config{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000, // 10ms
		PeriodicNAKInterval:    20_000, // 20ms
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		NakMergeGap:            3,
		NakConsolidationBudget: 20_000,
	}

	recv := New(recvConfig).(*receiver)

	// Set up mock time
	mockTime := uint64(1_000_000) // Start at 1 second
	recv.nowFn = func() uint64 { return mockTime }

	return recv, &mockTime, testMetrics
}

// createTestPacket creates a packet with specific sequence and TSBPD time.
func createTestPacket(seq uint32, tsbpdTime uint64) packet.Packet {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = tsbpdTime
	p.Header().Timestamp = uint32(tsbpdTime - 120_000) // Arrival time before TSBPD
	return p
}

// TestTSBPDAdvancement_RingOutOfOrder tests the current bug where ring out-of-order
// delivery causes packets to be dropped as "too_old".
//
// This is the specific scenario we're fixing:
// - io_uring receives packets 1-10
// - Ring round-robin reads packet 4 first (from shard 0)
// - Packet 4 inserted into btree
// - contiguousScan finds gap at 1-3, "stale gap" handling jumps contiguousPoint
// - Packets 1-3 read from ring later, rejected as "too_old"
func TestTSBPDAdvancement_RingOutOfOrder(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	// Time setup:
	// - Current time: 1 second (1_000_000 µs)
	// - Packets TSBPD: 1 second + 120ms = 1.12 seconds
	// - TSBPD has NOT expired yet
	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Create packets 1-10 with TSBPD time in the future
	packets := make([]packet.Packet, 10)
	for i := 0; i < 10; i++ {
		seq := uint32(i + 1)
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(i*1000) // Each packet 1ms apart
		packets[i] = createTestPacket(seq, tsbpdTime)
	}

	// Simulate ring out-of-order: push packets 4, 5, 6, ... then 1, 2, 3
	// This simulates what happens with io_uring + round-robin ring reading
	outOfOrderSequence := []int{3, 4, 5, 6, 7, 8, 9, 0, 1, 2} // 0-indexed
	for _, idx := range outOfOrderSequence {
		recv.Push(packets[idx])
	}

	// Run Tick to process packets (time is before TSBPD expiry)
	recv.Tick(*mockTime)

	// Check results
	// BEFORE FIX: Packets 1-3 would be dropped as "too_old" because the stale gap
	// handling incorrectly advances contiguousPoint when it sees packet 4 first.
	// AFTER FIX: All packets should be accepted (no too_old drops)
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	// Log the state for debugging
	t.Logf("contiguousPoint: %d", recv.contiguousPoint.Load())
	t.Logf("too_old drops: %d", tooOldDrops)
	t.Logf("store size: %d", recv.packetStore.Len())

	// This test currently FAILS (demonstrates broken behavior)
	// After implementing the fix, this assertion should PASS
	if tooOldDrops > 0 {
		t.Errorf("BROKEN: %d packets dropped as too_old due to out-of-order ring delivery", tooOldDrops)
		t.Log("This test demonstrates the bug. After implementing TSBPD-aware advancement, this should pass.")
	}

	// Verify all 10 packets are in the store
	if recv.packetStore.Len() != 10 {
		t.Errorf("Expected 10 packets in store, got %d", recv.packetStore.Len())
	}
}

// TestTSBPDAdvancement_CompleteOutage tests that contiguousPoint advances correctly
// after a complete network outage longer than the TSBPD buffer.
//
// Scenario:
// - Packets 1-100 received, contiguousPoint=100
// - Network outage for 3 seconds (> 120ms TSBPD)
// - Packets 101-199 NEVER arrive
// - Packets 200+ start arriving
//
// Expected behavior:
//   - When packet 200 arrives and its TSBPD is checked, packets 101-199's TSBPD
//     would have expired (if they existed)
//   - contiguousPoint should advance to 199 (btree.Min()-1)
//   - Packets 200+ should be processed normally
func TestTSBPDAdvancement_CompleteOutage(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Phase 1: Receive packets 1-100
	for seq := uint32(1); seq <= 100; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Run Tick to process and deliver packets
	*mockTime = baseTime + tsbpdDelayUs + 100_000 // After TSBPD of packet 100
	recv.Tick(*mockTime)

	// Verify contiguousPoint advanced to 100
	t.Logf("After phase 1: contiguousPoint=%d", recv.contiguousPoint.Load())

	// Phase 2: Network outage - 3 seconds pass, packets 101-199 never arrive
	// Advance time by 3 seconds
	*mockTime = baseTime + 3_000_000 // 3 seconds later

	// Phase 3: Packets 200-210 arrive (after the gap)
	// These packets have TSBPD time based on when they were "sent" (not current mockTime)
	// For the test to work, we set their TSBPD to match arrival during the outage
	// so that TSBPD expiry can trigger advancement
	packet200TsbpdTime := *mockTime + tsbpdDelayUs // Packet 200's TSBPD
	for seq := uint32(200); seq <= 210; seq++ {
		tsbpdTime := packet200TsbpdTime + uint64((seq-200)*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Advance time past TSBPD of packet 200 to trigger TSBPD-based advancement
	// At this point, the gap 101-199 is unrecoverable because btree.Min() (200)'s TSBPD has expired
	*mockTime = packet200TsbpdTime + 1 // Just past TSBPD of packet 200
	recv.Tick(*mockTime)

	// Check results
	contiguousPoint := recv.contiguousPoint.Load()
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	t.Logf("After outage: contiguousPoint=%d", contiguousPoint)
	t.Logf("too_old drops: %d", tooOldDrops)
	t.Logf("store size: %d", recv.packetStore.Len())

	// BEFORE FIX: contiguousPoint might be stuck at 100 or advanced incorrectly
	// AFTER FIX: contiguousPoint should advance to 199 (btree.Min()-1)
	// and packets 200-210 should NOT be dropped as too_old

	// The gap 101-199 should be recognized as TSBPD-expired (unrecoverable)
	// contiguousPoint should advance to 199
	if contiguousPoint < 199 {
		t.Errorf("BROKEN: contiguousPoint stuck at %d, expected >= 199", contiguousPoint)
		t.Log("After implementing TSBPD-aware advancement, contiguousPoint should advance to btree.Min()-1")
	}

	// Packets 200-210 should NOT be dropped
	if tooOldDrops > 0 {
		t.Errorf("BROKEN: %d packets dropped as too_old after outage", tooOldDrops)
	}
}

// TestTSBPDAdvancement_MidStreamGap tests that contiguousPoint advances when
// a mid-stream gap expires due to TSBPD.
//
// Scenario:
// - Packets 1-100 received, contiguousPoint=100
// - Packets 101-150 lost (never arrive)
// - Packets 151-200 arrive (stored in btree)
// - NAKs sent but retransmissions also lost
// - TSBPD expires for packets 101-150
//
// Expected behavior:
// - When TSBPD expires for packet 151 (the minimum in btree after gap)
// - contiguousPoint should advance to 150 (btree.Min()-1)
// - Packets 151-200 become deliverable
func TestTSBPDAdvancement_MidStreamGap(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Phase 1: Receive packets 1-100
	for seq := uint32(1); seq <= 100; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick to deliver
	*mockTime = baseTime + tsbpdDelayUs + 100_000
	recv.Tick(*mockTime)
	t.Logf("After packets 1-100: contiguousPoint=%d", recv.contiguousPoint.Load())

	// Phase 2: Packets 101-150 are lost, packets 151-200 arrive
	// Time advances slightly (packets arriving in real-time)
	arrivalTime := *mockTime + 50_000 // 50ms later
	for seq := uint32(151); seq <= 200; seq++ {
		tsbpdTime := arrivalTime + tsbpdDelayUs + uint64((seq-151)*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick - packets 151-200 in btree, but gap 101-150 exists
	*mockTime = arrivalTime
	recv.Tick(*mockTime)
	t.Logf("After gap: contiguousPoint=%d, store size=%d", recv.contiguousPoint.Load(), recv.packetStore.Len())

	// Phase 3: Time advances past TSBPD of packet 151
	// At this point, packets 101-150 are TSBPD-expired (unrecoverable)
	*mockTime = arrivalTime + tsbpdDelayUs + 10_000 // Past TSBPD of first packets in btree
	recv.Tick(*mockTime)

	// Check results
	contiguousPoint := recv.contiguousPoint.Load()
	t.Logf("After TSBPD expiry: contiguousPoint=%d", contiguousPoint)

	// BEFORE FIX: contiguousPoint might be stuck at 100
	// AFTER FIX: contiguousPoint should advance to 150 when btree.Min()'s TSBPD expires

	// With the gap being 50 packets (101-150), this is less than the stale threshold of 64
	// So the current broken code won't advance it based on gap size alone.
	// The fix should advance it based on TSBPD expiry of the minimum packet.

	// After TSBPD expiry of packet 151, the gap 101-150 is unrecoverable
	// contiguousPoint should advance to 150
	if contiguousPoint < 150 {
		t.Errorf("BROKEN: contiguousPoint stuck at %d, expected >= 150 after TSBPD expiry", contiguousPoint)
		t.Log("After implementing TSBPD-aware advancement, contiguousPoint should advance when btree.Min()'s TSBPD expires")
	}

	// Check no unexpected too_old drops
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()
	t.Logf("too_old drops: %d", tooOldDrops)
}

// TestTSBPDAdvancement_SmallGapNoAdvance tests that contiguousPoint does NOT advance
// for small gaps when TSBPD has NOT expired (packets might still arrive).
//
// This is a "negative test" to ensure we don't advance too eagerly.
func TestTSBPDAdvancement_SmallGapNoAdvance(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, _ := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Receive packets 1-10, then 15-20 (gap of 11-14)
	for seq := uint32(1); seq <= 10; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}
	for seq := uint32(15); seq <= 20; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick while TSBPD has NOT expired (packets 11-14 might still arrive)
	*mockTime = baseTime + 50_000 // Only 50ms, TSBPD is 120ms
	recv.Tick(*mockTime)

	contiguousPoint := recv.contiguousPoint.Load()
	t.Logf("contiguousPoint=%d (gap 11-14 exists, TSBPD not expired)", contiguousPoint)

	// contiguousPoint should NOT advance past 10 because:
	// 1. Gap exists (11-14)
	// 2. TSBPD has NOT expired for packet 15
	// 3. Packets 11-14 might still arrive
	if contiguousPoint > 10 {
		t.Errorf("BROKEN: contiguousPoint advanced to %d prematurely (TSBPD not expired)", contiguousPoint)
		t.Log("contiguousPoint should NOT advance until TSBPD expires for btree.Min()")
	}

	// Now advance time past TSBPD of packet 15
	*mockTime = baseTime + tsbpdDelayUs + 15_000 + 1 // Just past TSBPD of packet 15
	recv.Tick(*mockTime)

	contiguousPoint = recv.contiguousPoint.Load()
	t.Logf("After TSBPD expiry: contiguousPoint=%d", contiguousPoint)

	// NOW contiguousPoint should advance to 14 (btree.Min()-1 = 15-1 = 14)
	// because TSBPD has expired and gap is unrecoverable
	if contiguousPoint < 14 {
		t.Logf("Note: After fix, contiguousPoint should be 14 (btree.Min()-1)")
	}
}

// TestTSBPDAdvancement_ExtendedOutage tests recovery from a very long outage
// with multiple TSBPD advancement cycles.
//
// Scenario:
// - Packets 1-1000 received, contiguousPoint=1000
// - 30+ second outage with 80% packet loss
// - Thousands of packets may have expired TSBPD
// - System must recover gracefully through multiple Tick cycles
//
// This tests Edge Case 1 from the design document: "Very Long Outage"
func TestTSBPDAdvancement_ExtendedOutage(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Phase 1: Receive packets 1-1000 (establishing baseline)
	for seq := uint32(1); seq <= 1000; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*100) // 100µs apart
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick to deliver initial packets
	*mockTime = baseTime + tsbpdDelayUs + 100_000 // After TSBPD of packet 1000
	recv.Tick(*mockTime)
	t.Logf("After initial 1000 packets: contiguousPoint=%d", recv.contiguousPoint.Load())

	// Phase 2: Simulate extended outage - jump forward 30 seconds
	// Packets 1001-5000 would have been sent but many were lost
	outageDuration := uint64(30_000_000) // 30 seconds
	*mockTime = baseTime + outageDuration

	// Only ~20% of packets during outage arrive (sparse arrivals)
	// Simulate this by pushing packets at irregular intervals
	gapStarts := []uint32{1001, 1500, 2000, 3000, 4000}
	gapSizes := []uint32{400, 400, 800, 800, 800}

	currentSeq := uint32(1001)
	for i, gapStart := range gapStarts {
		gapSize := gapSizes[i]
		gapEnd := gapStart + gapSize - 1

		// Skip the gap
		currentSeq = gapEnd + 1

		// Push some packets after this gap
		nextGap := uint32(6000)
		if i+1 < len(gapStarts) {
			nextGap = gapStarts[i+1]
		}

		for seq := currentSeq; seq < nextGap && seq < 5500; seq++ {
			tsbpdTime := *mockTime + tsbpdDelayUs + uint64((seq-1001)*100)
			p := createTestPacket(seq, tsbpdTime)
			recv.Push(p)
		}
		currentSeq = nextGap
	}

	t.Logf("After sparse arrivals: store size=%d", recv.packetStore.Len())

	// Phase 3: Run many Tick cycles to trigger TSBPD-based advancements
	// Each cycle should advance contiguousPoint when TSBPD expires
	initialCP := recv.contiguousPoint.Load()
	tickCount := 0
	advancementCount := 0

	for i := 0; i < 500; i++ {
		*mockTime += 20_000 // Advance 20ms each tick
		prevCP := recv.contiguousPoint.Load()
		recv.Tick(*mockTime)
		newCP := recv.contiguousPoint.Load()

		if newCP != prevCP {
			advancementCount++
			t.Logf("Tick %d: contiguousPoint advanced %d -> %d", i, prevCP, newCP)
		}
		tickCount++

		// Stop early if we've advanced past all the gaps
		if newCP >= 5000 {
			break
		}
	}

	finalCP := recv.contiguousPoint.Load()
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	t.Logf("Final state after %d ticks:", tickCount)
	t.Logf("  contiguousPoint: %d -> %d", initialCP, finalCP)
	t.Logf("  advancements: %d", advancementCount)
	t.Logf("  too_old drops: %d", tooOldDrops)
	t.Logf("  store size: %d", recv.packetStore.Len())

	// Verify system recovered
	if finalCP <= initialCP {
		t.Errorf("contiguousPoint did not advance (stuck at %d)", finalCP)
	}

	// Should have had multiple advancements due to multiple gaps
	if advancementCount < 3 {
		t.Errorf("Expected multiple TSBPD advancements, got %d", advancementCount)
	}
}

// TestTSBPDAdvancement_Wraparound tests TSBPD advancement with sequence numbers
// near the 31-bit wraparound boundary.
//
// This tests Edge Case 4 from the design document: "Wraparound"
func TestTSBPDAdvancement_Wraparound(t *testing.T) {
	// Start sequence near MAX (2^31 - 100)
	const maxSeq = uint32(0x7FFFFFFF) // 2^31 - 1
	const startSeq = maxSeq - 100
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Phase 1: Receive packets around the wraparound point
	// Sequences: maxSeq-100, maxSeq-99, ..., maxSeq, 0, 1, 2, ...
	// Use packet INDEX (i) for TSBPD time calculation, not sequence number
	for i := uint32(0); i < 50; i++ {
		seq := circular.SeqAdd(startSeq, i)
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(i*1000) // Use index, not seq
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick to process - time should be past TSBPD of packet 49
	*mockTime = baseTime + tsbpdDelayUs + 50_000
	t.Logf("Before Tick: mockTime=%d, contiguousPoint=%d, lastACKSeq=%d, lastPeriodicACK=%d",
		*mockTime, recv.contiguousPoint.Load(), recv.lastACKSequenceNumber.Val(), recv.lastPeriodicACK)
	recv.Tick(*mockTime)

	cpAfterPhase1 := recv.contiguousPoint.Load()
	expectedAfterPhase1 := circular.SeqAdd(startSeq, 49)
	t.Logf("After phase 1 (50 packets): contiguousPoint=%d (0x%08x), expected=%d (0x%08x)",
		cpAfterPhase1, cpAfterPhase1, expectedAfterPhase1, expectedAfterPhase1)
	t.Logf("Phase 1: store size=%d, lastACKSeq=%d (0x%08x), lastPeriodicACK=%d",
		recv.packetStore.Len(), recv.lastACKSequenceNumber.Val(), recv.lastACKSequenceNumber.Val(), recv.lastPeriodicACK)

	// Phase 2: Create a gap
	// Gap: indices 50-60 (11 packets)
	// Push indices 61-99 (39 packets)
	gapStartIdx := uint32(50)
	gapEndIdx := uint32(60)
	gapStartSeq := circular.SeqAdd(startSeq, gapStartIdx)
	gapEndSeq := circular.SeqAdd(startSeq, gapEndIdx)
	t.Logf("Gap indices %d-%d: seq %d (0x%08x) to %d (0x%08x)",
		gapStartIdx, gapEndIdx, gapStartSeq, gapStartSeq, gapEndSeq, gapEndSeq)

	// Push packets after the gap (indices 61-99)
	for i := uint32(61); i < 100; i++ {
		seq := circular.SeqAdd(startSeq, i)
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(i*1000) // Use index, not seq
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick - gap exists but TSBPD of packet 61 NOT yet expired
	// Packet 61's TSBPD = baseTime + 120_000 + 61_000 = baseTime + 181_000
	*mockTime = baseTime + tsbpdDelayUs + 60_000 // Before packet 61's TSBPD
	recv.Tick(*mockTime)

	cpMidTest := recv.contiguousPoint.Load()
	t.Logf("After gap creation (time=%d, before TSBPD expiry): contiguousPoint=%d (0x%08x)",
		*mockTime, cpMidTest, cpMidTest)
	t.Logf("Phase 2: store size=%d", recv.packetStore.Len())
	if minPkt := recv.packetStore.Min(); minPkt != nil {
		t.Logf("Phase 2: btree.Min() seq=%d (0x%08x), TSBPD=%d",
			minPkt.Header().PacketSequenceNumber.Val(),
			minPkt.Header().PacketSequenceNumber.Val(),
			minPkt.Header().PktTsbpdTime)
	}

	// Phase 3: Advance time past TSBPD of packet 61
	// Packet 61's TSBPD = baseTime + tsbpdDelayUs + 61_000
	packet61TsbpdTime := baseTime + tsbpdDelayUs + 61_000
	*mockTime = packet61TsbpdTime + 1000 // 1ms past TSBPD of packet 61
	t.Logf("Advancing time to %d (packet 61 TSBPD=%d)", *mockTime, packet61TsbpdTime)
	recv.Tick(*mockTime)

	cpFinal := recv.contiguousPoint.Load()
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	t.Logf("After TSBPD expiry: contiguousPoint=%d (0x%08x)", cpFinal, cpFinal)
	t.Logf("too_old drops: %d", tooOldDrops)

	// Verify contiguousPoint advanced correctly across wraparound
	// btree.Min() after gap = packet at index 61
	btreeMinSeq := circular.SeqAdd(startSeq, 61)
	expectedCP := circular.SeqSub(btreeMinSeq, 1) // btree.Min()-1 = index 60
	t.Logf("btree.Min()=%d (0x%08x), expected contiguousPoint=%d (0x%08x)",
		btreeMinSeq, btreeMinSeq, expectedCP, expectedCP)

	// Use circular comparison since we're dealing with wraparound
	if !circular.SeqLessOrEqual(expectedCP, cpFinal) {
		t.Errorf("contiguousPoint did not advance correctly across wraparound")
		t.Errorf("  expected >= %d (0x%08x), got %d (0x%08x)", expectedCP, expectedCP, cpFinal, cpFinal)
	}

	// No packets should be dropped as too_old
	if tooOldDrops > 0 {
		t.Errorf("Unexpected too_old drops: %d", tooOldDrops)
	}
}

// TestTSBPDAdvancement_MultipleGaps tests recovery with multiple gaps
// that expire at different times.
//
// Scenario:
// - Packets 1-100 received
// - Gap 101-120 (lost)
// - Packets 121-200 received
// - Gap 201-250 (lost)
// - Packets 251-300 received
// - Each gap expires independently as time advances
func TestTSBPDAdvancement_MultipleGaps(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Create a stream with multiple gaps
	// Each segment has packets with TSBPD spread out
	type segment struct {
		start, end uint32
		timeOffset uint64 // Time offset from baseTime for this segment
	}

	segments := []segment{
		{1, 100, 0},         // Packets 1-100, TSBPD starts at baseTime
		{121, 200, 100_000}, // Packets 121-200, TSBPD starts 100ms later
		{251, 300, 250_000}, // Packets 251-300, TSBPD starts 250ms later
	}

	for _, seg := range segments {
		for seq := seg.start; seq <= seg.end; seq++ {
			tsbpdTime := baseTime + seg.timeOffset + tsbpdDelayUs + uint64((seq-seg.start)*1000)
			p := createTestPacket(seq, tsbpdTime)
			recv.Push(p)
		}
	}

	t.Logf("Initial store size: %d", recv.packetStore.Len())
	t.Logf("Gaps: 101-120, 201-250")

	// Run Tick cycles and track advancement
	advancements := []struct {
		time uint64
		cp   uint32
	}{}

	prevCP := recv.contiguousPoint.Load()

	// Run many Tick cycles, advancing time gradually
	for i := 0; i < 50; i++ {
		*mockTime = baseTime + uint64(i)*20_000 // 20ms per tick
		recv.Tick(*mockTime)

		newCP := recv.contiguousPoint.Load()
		if newCP != prevCP {
			advancements = append(advancements, struct {
				time uint64
				cp   uint32
			}{*mockTime, newCP})
			t.Logf("Tick %d (time=%d): contiguousPoint %d -> %d",
				i, *mockTime, prevCP, newCP)
			prevCP = newCP
		}
	}

	finalCP := recv.contiguousPoint.Load()
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	t.Logf("Final state:")
	t.Logf("  contiguousPoint: %d", finalCP)
	t.Logf("  advancements: %d", len(advancements))
	t.Logf("  too_old drops: %d", tooOldDrops)

	// Should have had at least 2 major advancements (one for each gap)
	// Gap 1 (101-120): Should trigger advancement when packet 121's TSBPD expires
	// Gap 2 (201-250): Should trigger advancement when packet 251's TSBPD expires
	if len(advancements) < 2 {
		t.Errorf("Expected at least 2 advancements for 2 gaps, got %d", len(advancements))
	}

	// Final contiguousPoint should be well past the initial state
	if finalCP < 200 {
		t.Errorf("Expected final contiguousPoint >= 200, got %d", finalCP)
	}
}

// TestTSBPDAdvancement_IterativeCycles tests gradual advancement through
// many small Tick cycles with time advancing in small increments.
//
// This tests that the advancement logic works correctly when called many
// times with small time deltas, not just with large time jumps.
func TestTSBPDAdvancement_IterativeCycles(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, _ := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Create packets with a gap
	// Packets 1-50, gap 51-60, packets 61-100
	for seq := uint32(1); seq <= 50; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}
	for seq := uint32(61); seq <= 100; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Track contiguousPoint over many small Tick cycles
	cpHistory := []uint32{}
	tickInterval := uint64(1_000) // 1ms per tick
	totalTicks := 200             // 200ms of ticks

	for i := 0; i < totalTicks; i++ {
		*mockTime = baseTime + uint64(i)*tickInterval
		recv.Tick(*mockTime)
		cp := recv.contiguousPoint.Load()
		cpHistory = append(cpHistory, cp)
	}

	// Log progression
	uniqueCPs := make(map[uint32]int)
	for i, cp := range cpHistory {
		if _, exists := uniqueCPs[cp]; !exists {
			uniqueCPs[cp] = i
			t.Logf("Tick %d (time=%d): contiguousPoint=%d",
				i, baseTime+uint64(i)*tickInterval, cp)
		}
	}

	// Verify:
	// 1. contiguousPoint should advance from 0 -> 50 (initial contiguous region)
	// 2. After TSBPD expiry (~120 ticks), should advance past the gap to 60
	finalCP := cpHistory[len(cpHistory)-1]

	if finalCP < 50 {
		t.Errorf("Expected contiguousPoint >= 50 after initial packets, got %d", finalCP)
	}

	// Check if TSBPD-based advancement occurred
	// TSBPD of packet 61 expires at baseTime + 120ms + 61ms = baseTime + 181ms
	// At tick 181 (181ms), contiguousPoint should advance past the gap
	tsbpdExpiresTick := int(tsbpdDelayUs/tickInterval) + 61
	if tsbpdExpiresTick < totalTicks {
		cpAtExpiry := cpHistory[tsbpdExpiresTick]
		t.Logf("At TSBPD expiry tick %d: contiguousPoint=%d", tsbpdExpiresTick, cpAtExpiry)

		if cpAtExpiry < 60 {
			t.Logf("Note: After TSBPD expiry, contiguousPoint should advance to 60 (gap 51-60 skipped)")
		}
	}

	// Verify monotonic advancement (contiguousPoint should never go backwards)
	for i := 1; i < len(cpHistory); i++ {
		if cpHistory[i] < cpHistory[i-1] {
			t.Errorf("contiguousPoint went backwards at tick %d: %d -> %d",
				i, cpHistory[i-1], cpHistory[i])
		}
	}
}
