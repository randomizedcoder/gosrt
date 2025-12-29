package live

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Sender Tests - UNIQUE TESTS ONLY
// NAK strategy tests (Original/HonorOrder) moved to send_table_test.go
// ═══════════════════════════════════════════════════════════════════════════

func mockLiveSend(onDeliver func(p packet.Packet)) *sender {
	// Initialize metrics for tests
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44) // IPv4 + UDP + SRT header

	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         10,
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
	})

	return send.(*sender)
}

// mockLiveSendHighThreshold creates a sender with high drop threshold for comprehensive tests
func mockLiveSendHighThreshold(onDeliver func(p packet.Packet)) *sender {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         100000, // Very high to prevent drops
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
	})

	return send.(*sender)
}

// mockLiveSendHonorOrder creates a sender with HonorNakOrder enabled
func mockLiveSendHonorOrder(onDeliver func(p packet.Packet)) *sender {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         1000, // High threshold to avoid drops during test
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
		HonorNakOrder:         true, // Enable honor NAK order
	})

	return send.(*sender)
}

// Helper to create NAK list from pairs (start, end)
func makeNakListFromPairs(pairs [][2]uint32) []circular.Number {
	list := make([]circular.Number, 0, len(pairs)*2)
	for _, p := range pairs {
		list = append(list, circular.New(p[0], packet.MAX_SEQUENCENUMBER))
		list = append(list, circular.New(p[1], packet.MAX_SEQUENCENUMBER))
	}
	return list
}

// ═══════════════════════════════════════════════════════════════════════════
// Unique Tests: Core Sender Functionality
// ═══════════════════════════════════════════════════════════════════════════

func TestSendSequence(t *testing.T) {
	numbers := []uint32{}
	send := mockLiveSend(func(p packet.Packet) {
		numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)

		send.Push(p)
	}

	send.Tick(5)

	require.Exactly(t, []uint32{0, 1, 2, 3, 4}, numbers)

	send.Tick(10)

	require.Exactly(t, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, numbers)
}

func TestSendLossListACK(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)

		send.Push(p)
	}

	send.Tick(10)

	require.Equal(t, 10, send.lossList.Len())

	for i := 0; i < 10; i++ {
		send.ACK(circular.New(uint32(i+1), packet.MAX_SEQUENCENUMBER))
		require.Equal(t, 10-(i+1), send.lossList.Len())
	}
}

func TestSendRetransmit(t *testing.T) {
	numbers := []uint32{}
	var nRetransmitFromFlag uint64
	send := mockLiveSend(func(p packet.Packet) {
		numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
		if p.Header().RetransmittedPacketFlag {
			nRetransmitFromFlag++
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)

		send.Push(p)
	}

	send.Tick(10)

	require.Equal(t, uint64(0), nRetransmitFromFlag)

	nRetransmit := send.NAK([]circular.Number{
		circular.New(2, packet.MAX_SEQUENCENUMBER),
		circular.New(2, packet.MAX_SEQUENCENUMBER),
	})

	require.Equal(t, uint64(1), nRetransmit)
	require.Equal(t, uint64(1), nRetransmitFromFlag)

	nRetransmit = send.NAK([]circular.Number{
		circular.New(5, packet.MAX_SEQUENCENUMBER),
		circular.New(7, packet.MAX_SEQUENCENUMBER),
	})

	require.Equal(t, uint64(3), nRetransmit)         // Packets 5, 6, 7
	require.Equal(t, uint64(4), nRetransmitFromFlag) // Total: 1 + 3 = 4
}

func TestSendDrop(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)

		send.Push(p)
	}

	send.Tick(10)

	require.Equal(t, 10, send.lossList.Len())

	send.Tick(20)

	require.Equal(t, 0, send.lossList.Len())
}

func TestSendFlush(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)

		send.Push(p)
	}

	require.Exactly(t, 10, send.packetList.Len())
	require.Exactly(t, 0, send.lossList.Len())

	send.Tick(5)

	require.Exactly(t, 5, send.packetList.Len())
	require.Exactly(t, 5, send.lossList.Len())

	send.Flush()

	require.Exactly(t, 0, send.packetList.Len())
	require.Exactly(t, 0, send.lossList.Len())
}

// ═══════════════════════════════════════════════════════════════════════════
// Unique Test: HonorNakOrder Metric
// ═══════════════════════════════════════════════════════════════════════════

func TestSendHonorOrder_Metric(t *testing.T) {
	// Test that CongestionSendNAKHonoredOrder metric is incremented
	send := mockLiveSendHonorOrder(func(p packet.Packet) {})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)

	require.Equal(t, uint64(0), send.metrics.CongestionSendNAKHonoredOrder.Load())

	// First NAK
	nakList := makeNakListFromPairs([][2]uint32{{5, 5}})
	send.NAK(nakList)

	require.Equal(t, uint64(1), send.metrics.CongestionSendNAKHonoredOrder.Load())

	// Second NAK
	nakList = makeNakListFromPairs([][2]uint32{{3, 3}})
	send.NAK(nakList)

	require.Equal(t, uint64(2), send.metrics.CongestionSendNAKHonoredOrder.Load())
}
