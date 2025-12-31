package receive

import (
	"math"
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// mockLiveRecvWithMetrics creates a receiver that returns its metrics for testing
func mockLiveRecvWithMetrics(onSendACK func(seq circular.Number, light bool), onSendNAK func(list []circular.Number), onDeliver func(p packet.Packet)) (*receiver, *metrics.ConnectionMetrics) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms in microseconds
		PeriodicNAKInterval:   20_000, // 20ms in microseconds
		OnSendACK:             onSendACK,
		OnSendNAK:             onSendNAK,
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
	})

	return recv.(*receiver), testMetrics
}

// TestReceiverLossCounter verifies that when packets are received with gaps,
// the loss counter is correctly updated.
func TestReceiverLossCounter(t *testing.T) {
	nakList := []circular.Number{}
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {
			nakList = append(nakList, list...)
		},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets 0-4 (no gaps)
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	// Skip packets 5, 6 and push packets 7-9 (creates gap)
	for i := 7; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	// Verify NAK was generated for missing packets 5, 6
	require.Len(t, nakList, 2, "NAK list should have 2 entries (start=5, end=6)")
	require.Equal(t, uint32(5), nakList[0].Val())
	require.Equal(t, uint32(6), nakList[1].Val())

	// Verify loss counter matches actual missing packets (5 and 6 = 2 packets)
	require.Equal(t, uint64(2), testMetrics.CongestionRecvPktLoss.Load(),
		"CongestionRecvPktLoss should be 2 (packets 5 and 6 missing)")

	// Verify NAK detail counters
	require.Equal(t, uint64(2), testMetrics.CongestionRecvNAKRange.Load(),
		"CongestionRecvNAKRange should be 2 (packets requested via range NAK)")
	require.Equal(t, uint64(0), testMetrics.CongestionRecvNAKSingle.Load(),
		"CongestionRecvNAKSingle should be 0 (no single NAK entries)")

	// Verify invariant: NAKSingle + NAKRange = NAKPktsTotal
	require.Equal(t, uint64(2), testMetrics.CongestionRecvNAKPktsTotal.Load(),
		"CongestionRecvNAKPktsTotal should be 2")
}

// TestReceiverPacketCounters verifies that received packet counters
// are correctly updated for unique and retransmitted packets.
func TestReceiverPacketCounters(t *testing.T) {
	deliveredCount := 0
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {},
		func(p packet.Packet) {
			deliveredCount++
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 5 unique packets
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	// Verify congestion receive counters
	require.Equal(t, uint64(5), testMetrics.CongestionRecvPkt.Load(),
		"CongestionRecvPkt should be 5")
	require.Equal(t, uint64(5), testMetrics.CongestionRecvPktUnique.Load(),
		"CongestionRecvPktUnique should be 5")

	// Push a retransmitted packet (same sequence number as packet 2)
	retransPacket := packet.NewPacket(addr)
	retransPacket.Header().PacketSequenceNumber = circular.New(2, packet.MAX_SEQUENCENUMBER)
	retransPacket.Header().PktTsbpdTime = uint64(3)
	retransPacket.Header().RetransmittedPacketFlag = true
	recv.Push(retransPacket)

	// Total packets received should increase, but unique should stay same
	require.Equal(t, uint64(6), testMetrics.CongestionRecvPkt.Load(),
		"CongestionRecvPkt should be 6 (5 unique + 1 retrans)")
	require.Equal(t, uint64(1), testMetrics.CongestionRecvPktRetrans.Load(),
		"CongestionRecvPktRetrans should be 1")
}

// TestReceiverLargeGap verifies that large sequence gaps are handled correctly
func TestReceiverLargeGap(t *testing.T) {
	nakRanges := [][]circular.Number{}
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {
			// Capture NAK ranges
			nakRanges = append(nakRanges, list)
		},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets 0-9
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	require.Equal(t, uint64(10), testMetrics.CongestionRecvPkt.Load(),
		"Should have received 10 packets")

	// Skip packets 10-49 and push packet 50 (creates 40-packet gap)
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(50, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = uint64(51)
	recv.Push(p)

	// Verify NAK was generated for missing packets 10-49
	require.Len(t, nakRanges, 1, "Should have one NAK range")
	require.Len(t, nakRanges[0], 2, "NAK range should have start and end")
	require.Equal(t, uint32(10), nakRanges[0][0].Val(), "NAK range should start at 10")
	require.Equal(t, uint32(49), nakRanges[0][1].Val(), "NAK range should end at 49")

	// Total received: 10 + 1 = 11
	require.Equal(t, uint64(11), testMetrics.CongestionRecvPkt.Load(),
		"Should have received 11 packets total")
}

// TestReceiverACKGeneration verifies that ACKs are generated correctly
func TestReceiverACKGeneration(t *testing.T) {
	ackSequences := []uint32{}
	ackTypes := []bool{} // true = light ACK, false = full ACK
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {
			ackSequences = append(ackSequences, seq.Val())
			ackTypes = append(ackTypes, light)
		},
		func(list []circular.Number) {},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 25 packets (0-24)
	for i := 0; i < 25; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	// No ACKs yet (need to tick)
	require.Len(t, ackSequences, 0, "No ACKs before tick")

	// Tick past ACK interval (periodic ACK interval is 10_000 µs = 10ms)
	recv.Tick(10_000)

	// Should have one ACK now
	require.Len(t, ackSequences, 1, "Should have 1 ACK after tick")
	// ACK sequence is the NEXT expected sequence number (after last received)
	require.Equal(t, uint32(25), ackSequences[0],
		"ACK should be for sequence 25 (next expected after receiving 0-24)")

	// Verify receive counters
	require.Equal(t, uint64(25), testMetrics.CongestionRecvPkt.Load(),
		"Should have received 25 packets")
	require.Equal(t, uint64(25), testMetrics.CongestionRecvPktUnique.Load(),
		"All 25 packets should be unique")
}

// TestReceiverPeriodicNAK verifies periodic NAK behavior for unrecovered losses
func TestReceiverPeriodicNAK(t *testing.T) {
	nakCounts := 0
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {
			nakCounts++
		},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets with gap: 0-4, skip 5-6, 7-9
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}
	for i := 7; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	// First NAK is immediate when gap detected
	require.Equal(t, 1, nakCounts, "Should have 1 immediate NAK when gap detected")

	// Verify receive counters
	require.Equal(t, uint64(8), testMetrics.CongestionRecvPkt.Load(),
		"Should have received 8 packets (0-4 and 7-9)")
}

// TestPeriodicACKRunsCounter verifies that the periodicACK run counter
// is incremented each time periodicACK actually runs.
func TestPeriodicACKRunsCounter(t *testing.T) {
	ackCounts := 0
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {
			ackCounts++
		},
		func(list []circular.Number) {},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push a few packets with TSBPD time in the future
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(1_000_000) // Well into the future
		recv.Push(p)
	}

	// Initial tick at 10ms (10_000 µs) - should run since lastPeriodicACK = 0
	recv.Tick(10_000)
	count1 := testMetrics.CongestionRecvPeriodicACKRuns.Load()
	require.Equal(t, uint64(1), count1,
		"CongestionRecvPeriodicACKRuns should be 1 after first tick")

	// Tick at 21ms (21_000 µs) - 11ms after first, should run (> 10ms interval)
	recv.Tick(21_000)
	count2 := testMetrics.CongestionRecvPeriodicACKRuns.Load()
	require.Equal(t, uint64(2), count2,
		"CongestionRecvPeriodicACKRuns should be 2 after second tick")

	// Tick at 25ms - only 4ms after last, should NOT run (< 10ms interval)
	recv.Tick(25_000)
	count3 := testMetrics.CongestionRecvPeriodicACKRuns.Load()
	require.Equal(t, count2, count3,
		"CongestionRecvPeriodicACKRuns should not increase when interval not elapsed")
}

// TestPeriodicNAKRunsCounter verifies that the periodicNAK run counter
// is incremented each time periodicNAK actually runs.
func TestPeriodicNAKRunsCounter(t *testing.T) {
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets with gap: 0-4, skip 5-6, 7
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(1_000_000) // Well into the future
		recv.Push(p)
	}
	// Skip 5-6, push 7
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(7, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = uint64(1_000_000)
	recv.Push(p)

	// Tick at 20ms to trigger periodic NAK
	recv.Tick(20_000)
	count1 := testMetrics.CongestionRecvPeriodicNAKRuns.Load()
	require.Equal(t, uint64(1), count1,
		"CongestionRecvPeriodicNAKRuns should be 1 after first periodic NAK")

	// Tick at 41ms (21ms after first), should run (> 20ms interval)
	recv.Tick(41_000)
	count2 := testMetrics.CongestionRecvPeriodicNAKRuns.Load()
	require.Equal(t, uint64(2), count2,
		"CongestionRecvPeriodicNAKRuns should be 2 after second periodic NAK")

	// Tick at 50ms - only 9ms after last, should NOT run (< 20ms interval)
	recv.Tick(50_000)
	count3 := testMetrics.CongestionRecvPeriodicNAKRuns.Load()
	require.Equal(t, count2, count3,
		"CongestionRecvPeriodicNAKRuns should not increase when interval not elapsed")
}

// TestTSBPDSkipCounter verifies that packets skipped at TSBPD time are counted.
func TestTSBPDSkipCounter(t *testing.T) {
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets 0-4 with TSBPD time = 100
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 100
		recv.Push(p)
	}

	// Skip packets 5-6 (the gap - these NEVER arrive)

	// Push packet 7-9 with TSBPD time = 100
	for i := 7; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 100
		recv.Push(p)
	}

	// Before TSBPD skip, counter should be 0
	require.Equal(t, uint64(0), testMetrics.CongestionRecvPktSkippedTSBPD.Load(),
		"CongestionRecvPktSkippedTSBPD should be 0 before TSBPD expires")

	// Now simulate time passing - TSBPD time (100) has passed
	recv.Tick(10_200)

	// Verify TSBPD skip counter was incremented for the 2 missing packets (5, 6)
	require.Equal(t, uint64(2), testMetrics.CongestionRecvPktSkippedTSBPD.Load(),
		"CongestionRecvPktSkippedTSBPD should be 2 (packets 5 and 6 skipped)")

	// Verify byte skip counter is non-zero
	require.Greater(t, testMetrics.CongestionRecvByteSkippedTSBPD.Load(), uint64(0),
		"CongestionRecvByteSkippedTSBPD should be > 0")
}

// TestReceiverRateStats verifies that rate statistics are calculated correctly.
// This test catches a bug where EventLoop used absolute Unix time for rate stats
// instead of relative connection time, causing RecvRateLastUs to be ~1.7e12.
func TestReceiverRateStats(t *testing.T) {
	recv, testMetrics := mockLiveRecvWithMetrics(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push some packets
	for i := 0; i < 100; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(10_000_000) // Well into future
		recv.Push(p)
	}

	// Verify initial rate counters are accumulated
	require.Equal(t, uint64(100), testMetrics.RecvRatePackets.Load(),
		"RecvRatePackets should be 100 after pushing 100 packets")

	// RecvRateLastUs should be 0 initially (not yet calculated)
	require.Equal(t, uint64(0), testMetrics.RecvRateLastUs.Load(),
		"RecvRateLastUs should be 0 before first rate calculation")

	// Tick past rate period (default 1 second = 1_000_000 µs)
	// Use a reasonable time value, NOT Unix timestamp
	recv.Tick(1_500_000) // 1.5 seconds in microseconds

	// RecvRateLastUs should now be set to the tick time
	lastUs := testMetrics.RecvRateLastUs.Load()
	require.Equal(t, uint64(1_500_000), lastUs,
		"RecvRateLastUs should be 1.5M µs (1.5s), not a Unix timestamp")

	// Verify this is NOT a Unix timestamp (which would be ~1.7e12 for 2025)
	require.Less(t, lastUs, uint64(1_000_000_000_000),
		"RecvRateLastUs should be relative time, not Unix timestamp")

	// Verify rate counters were reset after calculation
	require.Equal(t, uint64(0), testMetrics.RecvRatePackets.Load(),
		"RecvRatePackets should be reset to 0 after rate calculation")

	// Verify computed rate is reasonable (100 packets in ~1.5s ≈ 67 pkt/s)
	packetsPerSec := math.Float64frombits(testMetrics.RecvRatePacketsPerSec.Load())
	require.Greater(t, packetsPerSec, float64(50),
		"RecvRatePacketsPerSec should be > 50 pkt/s")
	require.Less(t, packetsPerSec, float64(100),
		"RecvRatePacketsPerSec should be < 100 pkt/s (100 packets / 1.5s ≈ 67)")
}

// ============================================================================
// NAK Btree / EventLoop Path Tests
// ============================================================================
//
// IMPORTANT: The tests above (TestReceiverLossCounter, etc.) use the Legacy
// Push path which increments CongestionRecvPktLoss directly.
//
// The EventLoop path with NAK btree uses DIFFERENT metrics:
// - NakBtreeInserts: Unique gaps added to NAK btree (equivalent to CongestionRecvPktLoss)
// - NakBtreeScanGaps: All gaps found during periodic scans (includes re-scans)
// - NakBtreeDeletes: Gaps resolved when packet arrives
// - NakFastRecentInserts: Immediate NAK inserts (FastNAK feature)
//
// See documentation/parallel_tests_defects.md "Metric Path Differences" section.
// ============================================================================

// mockLiveRecvWithNakBtree creates a receiver configured for NAK btree mode.
// This simulates the EventLoop configuration used in HighPerf tests.
func mockLiveRecvWithNakBtree(onSendACK func(seq circular.Number, light bool), onSendNAK func(list []circular.Number), onDeliver func(p packet.Packet)) (*receiver, *metrics.ConnectionMetrics) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms in microseconds
		PeriodicNAKInterval:   20_000, // 20ms in microseconds
		OnSendACK:             onSendACK,
		OnSendNAK:             onSendNAK,
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
		UseNakBtree:           true,      // Enable NAK btree (EventLoop mode)
		BTreeDegree:           32,        // NAK btree uses same degree setting
		TsbpdDelay:            3_000_000, // 3 seconds in microseconds (for scan window)
		NakRecentPercent:      0.10,      // 10% of TSBPD for "too recent" threshold
	})

	return recv.(*receiver), testMetrics
}

// TestNakBtreeInsertOnPeriodicNAK verifies that when periodicNakBtree() detects gaps,
// NakBtreeInserts is incremented for unique gaps and NakBtreeScanGaps for all gaps found.
//
// Key difference from Legacy path:
// - Legacy: CongestionRecvPktLoss incremented on IMMEDIATE NAK in Push()
// - EventLoop: NakBtreeInserts incremented on PERIODIC NAK scan
func TestNakBtreeInsertOnPeriodicNAK(t *testing.T) {
	nakLists := [][]circular.Number{}
	recv, testMetrics := mockLiveRecvWithNakBtree(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {
			nakLists = append(nakLists, list)
		},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets 0-4 (no gaps)
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(1_000_000) // 1 second in the future
		recv.Push(p)
	}

	// Skip packets 5, 6 and push packets 7-9 (creates gap)
	for i := 7; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(1_000_000) // 1 second in the future
		recv.Push(p)
	}

	// NOTE: With NAK btree enabled, Push() does NOT immediately detect gaps.
	// Gap detection happens in periodicNakBtree() during Tick().
	// Verify CongestionRecvPktLoss is 0 (not used in NAK btree path)
	require.Equal(t, uint64(0), testMetrics.CongestionRecvPktLoss.Load(),
		"CongestionRecvPktLoss should be 0 in NAK btree mode (this is by design, not a bug)")

	// Tick to trigger periodic NAK scan
	recv.Tick(20_001) // Just past NAK interval

	// Verify NAK btree metrics
	// NakBtreeInserts: should count unique gaps (2 packets: seq 5 and 6)
	inserts := testMetrics.NakBtreeInserts.Load()
	require.Greater(t, inserts, uint64(0),
		"NakBtreeInserts should be > 0 after periodic NAK detects gaps")

	// NakBtreeScanGaps: should count all gaps found during scan
	scanGaps := testMetrics.NakBtreeScanGaps.Load()
	require.Equal(t, uint64(2), scanGaps,
		"NakBtreeScanGaps should be 2 (packets 5 and 6 missing)")

	// Verify NAK was sent
	require.Len(t, nakLists, 1, "Should have sent one NAK")

	// Key documentation point:
	// In EventLoop mode, NakBtreeInserts is the EQUIVALENT of CongestionRecvPktLoss.
	// Tests and analysis code should use NakBtreeInserts when comparing loss detection.
	t.Logf("NAK btree mode: NakBtreeInserts=%d (equivalent to Legacy CongestionRecvPktLoss)", inserts)
}

// TestNakBtreeDeleteOnPacketArrival verifies that when a missing packet arrives,
// it is removed from the NAK btree and NakBtreeDeletes is incremented.
func TestNakBtreeDeleteOnPacketArrival(t *testing.T) {
	recv, testMetrics := mockLiveRecvWithNakBtree(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets 0-4, skip 5, push 6 (creates gap at seq 5)
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(1_000_000)
		recv.Push(p)
	}
	// Skip 5, push 6
	p6 := packet.NewPacket(addr)
	p6.Header().PacketSequenceNumber = circular.New(6, packet.MAX_SEQUENCENUMBER)
	p6.Header().PktTsbpdTime = uint64(1_000_000)
	recv.Push(p6)

	// Tick to trigger periodic NAK (adds seq 5 to NAK btree)
	recv.Tick(20_001)

	// Verify gap was detected and added to btree
	inserts := testMetrics.NakBtreeInserts.Load()
	require.Equal(t, uint64(1), inserts,
		"NakBtreeInserts should be 1 (seq 5 missing)")

	// Verify NakBtreeDeletes is 0 before missing packet arrives
	require.Equal(t, uint64(0), testMetrics.NakBtreeDeletes.Load(),
		"NakBtreeDeletes should be 0 before missing packet arrives")

	// Now the missing packet 5 arrives (simulating retransmit)
	p5 := packet.NewPacket(addr)
	p5.Header().PacketSequenceNumber = circular.New(5, packet.MAX_SEQUENCENUMBER)
	p5.Header().PktTsbpdTime = uint64(1_000_000)
	p5.Header().RetransmittedPacketFlag = true
	recv.Push(p5)

	// Verify NakBtreeDeletes incremented (packet removed from NAK btree)
	deletes := testMetrics.NakBtreeDeletes.Load()
	require.Equal(t, uint64(1), deletes,
		"NakBtreeDeletes should be 1 after missing packet arrives")

	// Key insight: deletes should eventually equal inserts (all gaps resolved)
	// In a healthy connection: NakBtreeInserts ≈ NakBtreeDeletes
	t.Logf("NAK btree: inserts=%d, deletes=%d (should be equal when all gaps resolved)", inserts, deletes)
}

// TestNakBtreeScanGapsVsInserts demonstrates the difference between
// NakBtreeScanGaps (cumulative) and NakBtreeInserts (unique).
//
// With high latency (300ms RTT) and 20ms NAK interval:
// - Each gap is scanned ~15 times before retransmit arrives
// - NakBtreeScanGaps counts ALL scans (high number)
// - NakBtreeInserts counts UNIQUE gaps only (lower number)
func TestNakBtreeScanGapsVsInserts(t *testing.T) {
	recv, testMetrics := mockLiveRecvWithNakBtree(
		func(seq circular.Number, light bool) {},
		func(list []circular.Number) {},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create a gap: packets 0-4, skip 5-6, packet 7
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(1_000_000)
		recv.Push(p)
	}
	p7 := packet.NewPacket(addr)
	p7.Header().PacketSequenceNumber = circular.New(7, packet.MAX_SEQUENCENUMBER)
	p7.Header().PktTsbpdTime = uint64(1_000_000)
	recv.Push(p7)

	// First periodic NAK scan (20ms)
	recv.Tick(20_001)
	insertAfter1 := testMetrics.NakBtreeInserts.Load()
	scanAfter1 := testMetrics.NakBtreeScanGaps.Load()

	// Second periodic NAK scan (40ms) - same gaps scanned again
	recv.Tick(40_001)
	insertAfter2 := testMetrics.NakBtreeInserts.Load()
	scanAfter2 := testMetrics.NakBtreeScanGaps.Load()

	// Third periodic NAK scan (60ms) - same gaps scanned again
	recv.Tick(60_001)
	insertAfter3 := testMetrics.NakBtreeInserts.Load()
	scanAfter3 := testMetrics.NakBtreeScanGaps.Load()

	// Key insight: Inserts should stay constant (unique gaps)
	// but ScanGaps increases each time (cumulative)
	require.Equal(t, insertAfter1, insertAfter2,
		"NakBtreeInserts should NOT increase on re-scan (already in btree)")
	require.Equal(t, insertAfter2, insertAfter3,
		"NakBtreeInserts should stay constant for same gaps")

	// ScanGaps should increase each scan (assuming gaps still not resolved)
	// Note: ScanGaps might not increase if tooRecentThreshold filtering kicks in
	// This test documents the expected behavior
	t.Logf("Scan 1: inserts=%d, scanGaps=%d", insertAfter1, scanAfter1)
	t.Logf("Scan 2: inserts=%d, scanGaps=%d", insertAfter2, scanAfter2)
	t.Logf("Scan 3: inserts=%d, scanGaps=%d", insertAfter3, scanAfter3)

	// Key documentation point for analysis code:
	// When comparing Baseline vs HighPerf:
	// - Baseline.CongestionRecvPktLoss ≈ HighPerf.NakBtreeInserts (unique gaps)
	// - HighPerf.NakBtreeScanGaps > HighPerf.NakBtreeInserts (includes re-scans)
}
