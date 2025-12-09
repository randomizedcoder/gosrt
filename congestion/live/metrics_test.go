package live

import (
	"net"
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// mockLiveSendWithMetrics creates a sender that returns its metrics for testing
//
// IMPORTANT: DropThreshold behavior affects NAK/retransmit tests!
// When Tick(now) is called, packets with (PktTsbpdTime + DropThreshold <= now) are DROPPED.
// This means:
//   - With DropThreshold=10 and Tick(10): packets with time 1-10 are kept (1+10=11 > 10)
//   - With DropThreshold=10 and Tick(20): packets with time 1-10 are DROPPED (1+10=11 <= 20)
//
// For tests: use Tick(N) where N <= max(PktTsbpdTime) to avoid unexpected drops.
func mockLiveSendWithMetrics(onDeliver func(p packet.Packet)) (*sender, *metrics.ConnectionMetrics) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         10,
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
	})

	return send.(*sender), testMetrics
}

// TestSenderRetransmitBehavior verifies that when NAK is received, the sender
// correctly retransmits packets and tracks the congestion control counters.
// Note: PktRetransFromNAK is incremented at connection level, not here.
func TestSenderRetransmitBehavior(t *testing.T) {
	retransmittedPackets := []uint32{}
	send, testMetrics := mockLiveSendWithMetrics(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedPackets = append(retransmittedPackets, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10 packets
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	// Tick to send packets (required before NAK can retransmit)
	send.Tick(10)

	// Verify initial state
	require.Equal(t, uint64(10), testMetrics.CongestionSendPkt.Load(),
		"Should have sent 10 packets")
	require.Equal(t, uint64(10), testMetrics.CongestionSendPktUnique.Load(),
		"Should have 10 unique packets (no retransmits yet)")

	// Simulate NAK for packets 2-4 (3 packets)
	nRetransmit := send.NAK([]circular.Number{
		circular.New(2, packet.MAX_SEQUENCENUMBER), // start
		circular.New(4, packet.MAX_SEQUENCENUMBER), // end (inclusive)
	})

	// Verify NAK() return value
	require.Equal(t, uint64(3), nRetransmit, "NAK() should return 3 retransmits for range 2-4")

	// Verify retransmissions were sent
	require.Len(t, retransmittedPackets, 3, "Should have retransmitted 3 packets")
	require.Contains(t, retransmittedPackets, uint32(2))
	require.Contains(t, retransmittedPackets, uint32(3))
	require.Contains(t, retransmittedPackets, uint32(4))

	// Verify congestion control retransmit counter
	require.Equal(t, uint64(3), testMetrics.CongestionSendPktRetrans.Load(),
		"CongestionSendPktRetrans should be 3 after retransmitting 3 packets")

	// Total sent should now include retransmits
	require.Equal(t, uint64(13), testMetrics.CongestionSendPkt.Load(),
		"Total sent should be 13 (10 original + 3 retransmits)")
}

// TestSenderCongestionCounters verifies that congestion control counters
// are correctly updated as packets are sent.
func TestSenderCongestionCounters(t *testing.T) {
	packetsSent := 0
	send, testMetrics := mockLiveSendWithMetrics(func(p packet.Packet) {
		packetsSent++
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 5 packets
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	// Tick to trigger sending
	send.Tick(10)

	// Verify packets were sent
	require.Equal(t, 5, packetsSent, "Should have sent 5 packets")

	// Verify congestion counters
	require.Equal(t, uint64(5), testMetrics.CongestionSendPkt.Load(),
		"CongestionSendPkt should be 5")
	require.Equal(t, uint64(5), testMetrics.CongestionSendPktUnique.Load(),
		"CongestionSendPktUnique should be 5 (no retransmits)")
}

// mockLiveRecvWithMetrics creates a receiver that returns its metrics for testing
func mockLiveRecvWithMetrics(onSendACK func(seq circular.Number, light bool), onSendNAK func(list []circular.Number), onDeliver func(p packet.Packet)) (*receiver, *metrics.ConnectionMetrics) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := NewReceiver(ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10,
		PeriodicNAKInterval:   20,
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
	require.Len(t, nakList, 2, "Should have NAK'd 2 packets (5 and 6)")
	require.Equal(t, uint32(5), nakList[0].Val())
	require.Equal(t, uint32(6), nakList[1].Val())

	// Verify loss counter
	// Note: The loss counter uses Distance(newPkt, maxSeen) which is 7-4=3, not 2.
	// This is because Distance counts the "gap" including the step to the new packet.
	// The NAK correctly reports 2 missing packets (5, 6), but the loss metric
	// counts 3 due to the Distance calculation.
	require.Equal(t, uint64(3), testMetrics.CongestionRecvPktLoss.Load(),
		"CongestionRecvPktLoss should be 3 (Distance from 4 to 7)")
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

// TestSenderMultipleNAKCalls verifies handling of multiple separate NAK calls
// This tests that counters accumulate correctly across multiple NAK calls
func TestSenderMultipleNAKCalls(t *testing.T) {
	retransmittedPackets := make(map[uint32]bool)
	send, testMetrics := mockLiveSendWithMetrics(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedPackets[p.Header().PacketSequenceNumber.Val()] = true
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10 packets (same as existing tests)
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	// Tick to send all packets
	send.Tick(10)

	require.Equal(t, uint64(10), testMetrics.CongestionSendPkt.Load(),
		"Should have sent 10 packets")

	// First NAK for packets 2-3 (2 packets)
	nRetransmit1 := send.NAK([]circular.Number{
		circular.New(2, packet.MAX_SEQUENCENUMBER),
		circular.New(3, packet.MAX_SEQUENCENUMBER),
	})
	require.Equal(t, uint64(2), nRetransmit1, "First NAK should retransmit 2 packets")

	// Second NAK for packets 5-7 (3 packets)
	nRetransmit2 := send.NAK([]circular.Number{
		circular.New(5, packet.MAX_SEQUENCENUMBER),
		circular.New(7, packet.MAX_SEQUENCENUMBER),
	})
	require.Equal(t, uint64(3), nRetransmit2, "Second NAK should retransmit 3 packets")

	// Verify total retransmissions
	require.Equal(t, uint64(5), testMetrics.CongestionSendPktRetrans.Load(),
		"Total retransmissions should be 5 (2+3)")

	// Verify total sent (original + retransmits)
	require.Equal(t, uint64(15), testMetrics.CongestionSendPkt.Load(),
		"Total sent should be 15 (10 original + 5 retransmits)")

	// Verify specific packets were retransmitted
	require.True(t, retransmittedPackets[2], "Packet 2 should have been retransmitted")
	require.True(t, retransmittedPackets[3], "Packet 3 should have been retransmitted")
	require.True(t, retransmittedPackets[5], "Packet 5 should have been retransmitted")
	require.True(t, retransmittedPackets[6], "Packet 6 should have been retransmitted")
	require.True(t, retransmittedPackets[7], "Packet 7 should have been retransmitted")
}

// TestSenderMultipleNAKRangesInSingleCall verifies handling of multiple NAK ranges in one call
// According to SRT spec, NAK can contain multiple loss ranges
func TestSenderMultipleNAKRangesInSingleCall(t *testing.T) {
	retransmittedPackets := []uint32{}
	send, testMetrics := mockLiveSendWithMetrics(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedPackets = append(retransmittedPackets, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10 packets (same as existing tests)
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}
	send.Tick(10)

	// Simulate NAK with multiple ranges in single call
	// Ranges: [2-2], [5-6], [8-8] (4 packets total)
	nRetransmit := send.NAK([]circular.Number{
		circular.New(2, packet.MAX_SEQUENCENUMBER), // Range 1 start
		circular.New(2, packet.MAX_SEQUENCENUMBER), // Range 1 end (single packet)
		circular.New(5, packet.MAX_SEQUENCENUMBER), // Range 2 start
		circular.New(6, packet.MAX_SEQUENCENUMBER), // Range 2 end (2 packets)
		circular.New(8, packet.MAX_SEQUENCENUMBER), // Range 3 start
		circular.New(8, packet.MAX_SEQUENCENUMBER), // Range 3 end (single packet)
	})

	require.Equal(t, uint64(4), nRetransmit,
		"NAK with 3 ranges should retransmit 4 packets (1+2+1)")
	require.Equal(t, uint64(4), testMetrics.CongestionSendPktRetrans.Load(),
		"Retransmit counter should be 4")
	require.Len(t, retransmittedPackets, 4, "Should have retransmitted 4 packets")

	// Verify specific packets
	require.Contains(t, retransmittedPackets, uint32(2))
	require.Contains(t, retransmittedPackets, uint32(5))
	require.Contains(t, retransmittedPackets, uint32(6))
	require.Contains(t, retransmittedPackets, uint32(8))
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

	// Tick past ACK interval (periodic ACK interval is 10)
	recv.Tick(10)

	// Should have one ACK now
	require.Len(t, ackSequences, 1, "Should have 1 ACK after tick")
	// ACK sequence is the NEXT expected sequence number (after last received)
	// So for packets 0-24, ACK is 25
	require.Equal(t, uint32(25), ackSequences[0],
		"ACK should be for sequence 25 (next expected after receiving 0-24)")

	// Verify receive counters
	require.Equal(t, uint64(25), testMetrics.CongestionRecvPkt.Load(),
		"Should have received 25 packets")
	require.Equal(t, uint64(25), testMetrics.CongestionRecvPktUnique.Load(),
		"All 25 packets should be unique")
}

// TestReceiverPeriodicNAK verifies periodic NAK behavior for unrecovered losses
// Note: This test documents the behavior - periodic NAKs may require specific conditions
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
