package receive

import (
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
