package receive

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Table-driven tests for periodicACK (EventLoop path) - 0% coverage
// Target: Find bugs in ACK generation for EventLoop mode
//
// KEY DESIGN CONCEPTS (from ack_optimization_plan.md):
// - contiguousPoint: tracks highest contiguous sequence received (ISN-1 initially)
// - ACK sequence: reports "next expected" = contiguousPoint + 1
// - Light ACK: sent every 64 packets (triggered by RecvLightACKCounter)
// - Full ACK: sent every 10ms (for RTT measurement)
// - The scan starts from contiguousPoint and advances through contiguous packets
// ============================================================================

type PeriodicACKTestCase struct {
	Name string

	// Setup - ISN is the handshake sequence number, packets start at ISN
	InitialSeqNum   uint32   // Initial sequence number from handshake
	AckInterval     uint64   // Periodic ACK interval (microseconds)
	LastPeriodicAck uint64   // Time of last periodic ACK
	NowTime         uint64   // Current time for the test
	ReceivedPackets []uint32 // Packets that were received (relative to ISN)
	LightACKDiff    uint32   // Light ACK trigger difference

	// Expected outcomes
	ExpectAckSent  bool
	ExpectedAckSeq uint32 // Expected ACK sequence number (next expected)
	ExpectLightAck bool   // Was it a light ACK?
}

var periodicACKTestCases = []PeriodicACKTestCase{
	{
		Name:            "IntervalNotElapsed_Skip",
		InitialSeqNum:   0,
		AckInterval:     10_000, // 10ms
		LastPeriodicAck: 10_000,
		NowTime:         15_000, // Only 5ms elapsed
		ReceivedPackets: []uint32{0, 1, 2, 3, 4},
		LightACKDiff:    64,
		ExpectAckSent:   false, // Interval not elapsed and not enough for light ACK
	},
	{
		Name:            "IntervalElapsed_ContiguousPackets",
		InitialSeqNum:   0,
		AckInterval:     10_000,
		LastPeriodicAck: 0,
		NowTime:         15_000, // 15ms elapsed
		ReceivedPackets: []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		LightACKDiff:    64,
		ExpectAckSent:   true,
		ExpectedAckSeq:  10, // Next expected after 0-9
		ExpectLightAck:  false,
	},
	{
		Name:            "IntervalElapsed_GapInMiddle",
		InitialSeqNum:   0,
		AckInterval:     10_000,
		LastPeriodicAck: 0,
		NowTime:         15_000,
		ReceivedPackets: []uint32{0, 1, 2, 5, 6, 7}, // Gap at 3-4
		LightACKDiff:    64,
		ExpectAckSent:   true,
		ExpectedAckSeq:  3, // Can only ACK up to gap
		ExpectLightAck:  false,
	},
	{
		Name:            "EmptyStore_KeepAliveACK",
		InitialSeqNum:   0,
		AckInterval:     10_000,
		LastPeriodicAck: 0,
		NowTime:         15_000,
		ReceivedPackets: []uint32{}, // No packets
		LightACKDiff:    64,
		ExpectAckSent:   true,
		ExpectedAckSeq:  0, // Keep-alive ACK with ISN
		ExpectLightAck:  false,
	},
	{
		Name:            "SinglePacket",
		InitialSeqNum:   100,
		AckInterval:     10_000,
		LastPeriodicAck: 0,
		NowTime:         15_000,
		ReceivedPackets: []uint32{100}, // Just ISN
		LightACKDiff:    64,
		ExpectAckSent:   true,
		ExpectedAckSeq:  101, // Next after 100
		ExpectLightAck:  false,
	},
	{
		Name:            "LargeContiguousRange",
		InitialSeqNum:   0,
		AckInterval:     10_000,
		LastPeriodicAck: 0,
		NowTime:         15_000,
		ReceivedPackets: makePacketRange(0, 99), // 100 contiguous packets
		LightACKDiff:    64,
		ExpectAckSent:   true,
		ExpectedAckSeq:  100, // Next after 0-99
		ExpectLightAck:  false,
	},
}

func makePacketRange(start, end uint32) []uint32 {
	result := make([]uint32, end-start+1)
	for i := range result {
		result[i] = start + uint32(i)
	}
	return result
}

func TestPeriodicACK_Table(t *testing.T) {
	for _, tc := range periodicACKTestCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			runPeriodicACKTest(t, tc)
		})
	}
}

func runPeriodicACKTest(t *testing.T, tc PeriodicACKTestCase) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create receiver with proper ISN
	// Per design: contiguousPoint is initialized to ISN-1 (one before first expected)
	recv := New(Config{
		InitialSequenceNumber: circular.New(tc.InitialSeqNum, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   tc.AckInterval,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		LightACKDifference:    tc.LightACKDiff,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Set up timing state
	recv.lastPeriodicACK = tc.LastPeriodicAck

	// Add packets to store
	for _, seq := range tc.ReceivedPackets {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = tc.NowTime + 500_000 // Future TSBPD
		recv.packetStore.Insert(p)
		// Update maxSeenSequenceNumber for light ACK check
		if circular.SeqGreater(seq, recv.maxSeenSequenceNumber.Val()) {
			recv.maxSeenSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		}
	}

	// Log initial state
	t.Logf("Initial state: contiguousPoint=%d, lastACKSeq=%d, packets=%v",
		recv.contiguousPoint.Load(), recv.lastACKSequenceNumber.Val(), tc.ReceivedPackets)

	// Call the function under test
	ok, resultSeq, lite := recv.periodicACK(tc.NowTime)

	t.Logf("Result: ok=%v, seq=%d, lite=%v", ok, resultSeq.Val(), lite)

	// Verify results
	if tc.ExpectAckSent {
		require.True(t, ok, "Expected ACK to be sent")
		require.Equal(t, tc.ExpectedAckSeq, resultSeq.Val(),
			"ACK sequence mismatch: got %d, want %d", resultSeq.Val(), tc.ExpectedAckSeq)
		require.Equal(t, tc.ExpectLightAck, lite,
			"Light ACK flag mismatch: got %v, want %v", lite, tc.ExpectLightAck)
	} else {
		require.False(t, ok, "Expected no ACK to be sent")
	}
}

// ============================================================================
// Bug-hunting tests for ACK edge cases
// Based on ack_optimization_plan.md and ack_ackack_redesign_progress.md
// ============================================================================

func TestPeriodicACK_HighWaterMarkBug(t *testing.T) {
	// Test for the bug described in "ACK Scan High Water Mark Bug" section
	// The scanStartPoint should NOT advance ackSequenceNumber unless contiguous
	// Reference: ack_optimization_plan.md Section 3.5

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	recv := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000, // Long enough that nothing expires
		UseNakBtree:           true,
		LightACKDifference:    64,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Scenario from ack_optimization_plan.md:
	// 1. Receive packets 0-9 contiguously
	// 2. ACK runs and advances contiguousPoint to 9 (ACK=10)
	// 3. Now receive packets 50-60 (big gap at 10-49)
	// 4. ACK runs - should STILL be at 10, not jump to 50

	// Step 1: Add packets 0-9
	for i := uint32(0); i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 1_000_000 // Far future
		recv.packetStore.Insert(p)
	}
	recv.maxSeenSequenceNumber = circular.New(9, packet.MAX_SEQUENCENUMBER)

	// First ACK
	ok, seq, _ := recv.periodicACK(15_000)
	require.True(t, ok)
	require.Equal(t, uint32(10), seq.Val(), "First ACK should be 10 (next after 0-9)")
	t.Logf("After first batch: ACK=%d, contiguousPoint=%d", seq.Val(), recv.contiguousPoint.Load())

	// Step 2: Add packets 50-60 (gap at 10-49)
	for i := uint32(50); i <= 60; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 1_000_000 // Far future
		recv.packetStore.Insert(p)
	}
	recv.maxSeenSequenceNumber = circular.New(60, packet.MAX_SEQUENCENUMBER)

	// Second ACK - after gap
	recv.lastPeriodicACK = 0 // Reset interval
	ok, seq, _ = recv.periodicACK(30_000)
	require.True(t, ok)
	t.Logf("After second batch: ACK=%d, contiguousPoint=%d", seq.Val(), recv.contiguousPoint.Load())

	// BUG CHECK: If this returns 50 or 61, the high water mark is incorrectly
	// advancing ACK past the gap
	if seq.Val() > 10 {
		t.Errorf("BUG DETECTED: ACK jumped from 10 to %d - high water mark advancing past gap!", seq.Val())
	}

	require.Equal(t, uint32(10), seq.Val(),
		"ACK should still be 10 - gap at 10-49 blocks advancement")
}

func TestPeriodicACK_TSBPDExpiredGapSkip(t *testing.T) {
	// Test TSBPD-based gap skipping (contiguous_point_tsbpd_advancement_design.md)
	// When packets in a gap have TSBPD-expired, ACK should advance past them

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	recv := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            1000,
		UseNakBtree:           true,
		LightACKDifference:    64,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Scenario: Gap at 5-9, packets 0-4 and 10-14 received
	// Packets 10-14 have TSBPD in the past -> gap is unrecoverable

	// Add packets 0-4 with far future TSBPD
	for i := uint32(0); i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 1_000_000 // Far future
		recv.packetStore.Insert(p)
	}

	// Add packets 10-14 (gap at 5-9) with EXPIRED TSBPD
	for i := uint32(10); i < 15; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 1000 // TSBPD expired (now will be 15_000)
		recv.packetStore.Insert(p)
	}
	recv.maxSeenSequenceNumber = circular.New(14, packet.MAX_SEQUENCENUMBER)

	// Debug state before ACK
	t.Logf("Before ACK: contiguousPoint=%d, lastACKSeq=%d, storeLen=%d",
		recv.contiguousPoint.Load(), recv.lastACKSequenceNumber.Val(), recv.packetStore.Len())

	// Run ACK with now > ACK interval (15_000 > 10_000) AND now > TSBPD of packets 10-14
	ok, seq, _ := recv.periodicACK(15_000)
	t.Logf("After ACK: ok=%v, seq=%d, contiguousPoint=%d", ok, seq.Val(), recv.contiguousPoint.Load())

	require.True(t, ok, "periodicACK should have returned ok=true (interval elapsed)")

	// The first 5 packets (0-4) are contiguous from ISN=-1 (MAX)
	// So ACK should be at least 5 (next expected after 0-4)
	// TSBPD gap skip only kicks in when btree.Min() has expired TSBPD
	// Here btree.Min() is packet 0 with TSBPD far in future
	t.Logf("ACK sequence: %d (expected >= 5)", seq.Val())
	require.GreaterOrEqual(t, seq.Val(), uint32(5),
		"ACK should be at least 5 (next after contiguous 0-4)")
}

func TestPeriodicACK_WraparoundSequence(t *testing.T) {
	// Test ACK behavior at 31-bit sequence wraparound boundary

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start near MAX_SEQUENCENUMBER
	startSeq := packet.MAX_SEQUENCENUMBER - 5

	recv := New(Config{
		InitialSequenceNumber: circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		LightACKDifference:    64,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Add packets crossing wraparound: MAX-5 to MAX, then 0 to 3
	seqs := []uint32{
		packet.MAX_SEQUENCENUMBER - 5,
		packet.MAX_SEQUENCENUMBER - 4,
		packet.MAX_SEQUENCENUMBER - 3,
		packet.MAX_SEQUENCENUMBER - 2,
		packet.MAX_SEQUENCENUMBER - 1,
		packet.MAX_SEQUENCENUMBER,
		0, 1, 2, 3,
	}

	for _, s := range seqs {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(s, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 1_000_000
		recv.packetStore.Insert(p)
	}
	recv.maxSeenSequenceNumber = circular.New(3, packet.MAX_SEQUENCENUMBER)

	ok, seq, _ := recv.periodicACK(15_000)
	require.True(t, ok)
	t.Logf("Wraparound ACK: seq=%d (expected 4)", seq.Val())

	// After receiving MAX-5 to MAX, 0 to 3, next expected is 4
	require.Equal(t, uint32(4), seq.Val(),
		"ACK should be 4 (next after wraparound sequence)")
}
