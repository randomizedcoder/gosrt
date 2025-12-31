package live

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
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
func mockLiveSendWithMetrics(onDeliver func(p packet.Packet)) (congestion.Sender, *metrics.ConnectionMetrics) {
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

	return send, testMetrics
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
