package live

import (
	"net"
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
)

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

// ============================================================================
// Original NAK Comprehensive Tests (matching HonorNakOrder test coverage)
// ============================================================================
//
// IMPORTANT: Original NAK iterates lossList BACKWARDS (newest-first), checking
// each packet against ALL NAK ranges. This means:
// - Packets are retransmitted in REVERSE sequence order (highest seq first)
// - Order is determined by lossList, NOT by NAK list order
// - This differs from HonorOrder which respects NAK list order (receiver priority)

func TestSendOriginal_BasicSingle(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSend(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10 packets
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10) // Move all to loss list

	// NAK for single packet (seq 5)
	nakList := makeNakListFromPairs([][2]uint32{{5, 5}})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(1), nRetrans)
	require.Equal(t, []uint32{5}, retransmittedSeqs)
}

func TestSendOriginal_BasicRange(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSend(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10 packets
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)

	// NAK for range (seq 3-6)
	nakList := makeNakListFromPairs([][2]uint32{{3, 6}})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(4), nRetrans)
	// Original iterates lossList BACKWARDS, so packets come in reverse order
	require.Equal(t, []uint32{6, 5, 4, 3}, retransmittedSeqs)
}

func TestSendOriginal_MultipleSingles(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHighThreshold(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 20 packets
	for i := 0; i < 20; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(20)

	// NAK for multiple singles: 15, 5, 10 (NAK order)
	// Original iterates lossList backwards (seq 19, 18, 17, ..., 0)
	// For each packet, checks ALL NAK ranges
	// So order is: 15, 10, 5 (highest to lowest from lossList)
	nakList := makeNakListFromPairs([][2]uint32{
		{15, 15},
		{5, 5},
		{10, 10},
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(3), nRetrans)
	// Packets delivered in lossList order (newest-first = highest seq first)
	require.Equal(t, []uint32{15, 10, 5}, retransmittedSeqs)
}

func TestSendOriginal_MultipleRanges(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHighThreshold(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 30 packets
	for i := 0; i < 30; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(30)

	// NAK for multiple ranges: 20-22, 5-7, 12-14 (NAK order)
	// Original iterates lossList backwards, so packets come in lossList order:
	// 22, 21, 20, 14, 13, 12, 7, 6, 5
	nakList := makeNakListFromPairs([][2]uint32{
		{20, 22},
		{5, 7},
		{12, 14},
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(9), nRetrans) // 3 + 3 + 3
	// Packets in lossList order (highest seq first across all ranges)
	require.Equal(t, []uint32{22, 21, 20, 14, 13, 12, 7, 6, 5}, retransmittedSeqs)
}

func TestSendOriginal_MixedSinglesAndRanges(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHighThreshold(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 50 packets
	for i := 0; i < 50; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(50)

	// Mixed NAK: range 40-42, single 10, range 25-27, single 5, single 35
	// Original delivers in lossList order (highest seq first):
	// 42, 41, 40, 35, 27, 26, 25, 10, 5
	nakList := makeNakListFromPairs([][2]uint32{
		{40, 42}, // Range
		{10, 10}, // Single
		{25, 27}, // Range
		{5, 5},   // Single
		{35, 35}, // Single
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(9), nRetrans) // 3 + 1 + 3 + 1 + 1
	// Packets in lossList order (highest to lowest matching NAK entries)
	require.Equal(t, []uint32{42, 41, 40, 35, 27, 26, 25, 10, 5}, retransmittedSeqs)
}

func TestSendOriginal_ModulusDrops(t *testing.T) {
	// Simulate receiver NAK for modulus-based drops (every 10th packet)
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHighThreshold(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 100 packets
	for i := 0; i < 100; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(100)

	// NAK for every 10th packet (10, 20, 30, ..., 90)
	// Original delivers in reverse order: 90, 80, 70, ..., 10
	var pairs [][2]uint32
	for i := 10; i <= 90; i += 10 {
		pairs = append(pairs, [2]uint32{uint32(i), uint32(i)})
	}
	nakList := makeNakListFromPairs(pairs)
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(9), nRetrans)
	// Highest to lowest (lossList backward iteration)
	expected := []uint32{90, 80, 70, 60, 50, 40, 30, 20, 10}
	require.Equal(t, expected, retransmittedSeqs)
}

func TestSendOriginal_BurstDrops(t *testing.T) {
	// Simulate receiver NAK for burst drops
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHighThreshold(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 200 packets
	for i := 0; i < 200; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(200)

	// NAK for burst drops: 30-39, 80-89, 150-159
	// Original delivers in lossList order (highest first across all ranges):
	// 159, 158, ..., 150, 89, 88, ..., 80, 39, 38, ..., 30
	nakList := makeNakListFromPairs([][2]uint32{
		{30, 39},
		{80, 89},
		{150, 159},
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(30), nRetrans) // 10 + 10 + 10

	expected := []uint32{}
	// Highest range first (150-159 in reverse)
	for i := uint32(159); i >= 150; i-- {
		expected = append(expected, i)
	}
	// Then 80-89 in reverse
	for i := uint32(89); i >= 80; i-- {
		expected = append(expected, i)
	}
	// Then 30-39 in reverse
	for i := uint32(39); i >= 30; i-- {
		expected = append(expected, i)
	}
	require.Equal(t, expected, retransmittedSeqs)
}

func TestSendOriginal_RealisticConsolidatedNAK(t *testing.T) {
	// Simulate a realistic NAK packet from receiver
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHighThreshold(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 500 packets
	for i := 0; i < 500; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(500)

	// Realistic NAK from receiver (ordered by urgency - oldest first):
	// 50-54, 75, 100-102, 150, 175, 200, 250-260, 300
	nakList := makeNakListFromPairs([][2]uint32{
		{50, 54},   // 5 packets
		{75, 75},   // 1 packet
		{100, 102}, // 3 packets
		{150, 150}, // 1 packet
		{175, 175}, // 1 packet
		{200, 200}, // 1 packet
		{250, 260}, // 11 packets
		{300, 300}, // 1 packet
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(24), nRetrans) // 5+1+3+1+1+1+11+1

	// Original delivers in lossList order (highest to lowest matching NAK entries)
	// 300, 260, 259, ..., 250, 200, 175, 150, 102, 101, 100, 75, 54, 53, 52, 51, 50
	expected := []uint32{300}
	for i := uint32(260); i >= 250; i-- {
		expected = append(expected, i)
	}
	expected = append(expected, 200, 175, 150)
	for i := uint32(102); i >= 100; i-- {
		expected = append(expected, i)
	}
	expected = append(expected, 75)
	for i := uint32(54); i >= 50; i-- {
		expected = append(expected, i)
	}

	require.Equal(t, expected, retransmittedSeqs)
	t.Logf("✅ Original NAK realistic: %d packets retransmitted (newest-first order)", nRetrans)
}

func TestSendOriginal_NotFoundPackets(t *testing.T) {
	// Test that packets not in lossList are counted as not found
	send := mockLiveSend(func(p packet.Packet) {})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push only 10 packets (0-9)
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)

	// NAK for packets that don't exist
	nakList := makeNakListFromPairs([][2]uint32{
		{5, 5},     // Exists
		{100, 100}, // Doesn't exist
		{200, 200}, // Doesn't exist
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(1), nRetrans) // Only packet 5 was retransmitted

	// Check NakNotFound metric
	require.Equal(t, uint64(2), send.metrics.CongestionSendNAKNotFound.Load())
}

func TestSendOriginal_LargeScale(t *testing.T) {
	// Test with a large number of NAK entries
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	retransmitCount := 0
	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         100000, // Very high to prevent drops
		OnDeliver: func(p packet.Packet) {
			if p.Header().RetransmittedPacketFlag {
				retransmitCount++
			}
		},
		ConnectionMetrics: testMetrics,
		HonorNakOrder:     false, // Original behavior
	}).(*sender)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10000 packets
	for i := 0; i < 10000; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10000)

	// Create NAK for singles (every 20th packet)
	var pairs [][2]uint32
	for i := 20; i < 10000; i += 20 {
		pairs = append(pairs, [2]uint32{uint32(i), uint32(i)})
	}
	nakList := makeNakListFromPairs(pairs)
	nRetrans := send.NAK(nakList)

	expectedCount := uint64(len(pairs))
	require.Equal(t, expectedCount, nRetrans)
	require.Equal(t, int(expectedCount), retransmitCount)
	t.Logf("✅ Original NAK large scale: %d packets retransmitted (newest-first order)", nRetrans)
}

// TestSendOriginal_VsHonorOrder_Difference demonstrates the key behavioral difference
func TestSendOriginal_VsHonorOrder_Difference(t *testing.T) {
	// This test shows why HonorNakOrder exists:
	// - Original: Retransmits newest-first (might miss urgent old packets)
	// - HonorOrder: Retransmits in NAK order (receiver controls priority)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Test Original behavior
	originalSeqs := []uint32{}
	sendOriginal := mockLiveSendHighThreshold(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			originalSeqs = append(originalSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})
	for i := 0; i < 100; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		sendOriginal.Push(p)
	}
	sendOriginal.Tick(100)

	// Test HonorOrder behavior
	honorSeqs := []uint32{}
	sendHonor := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			honorSeqs = append(honorSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})
	for i := 0; i < 100; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		sendHonor.Push(p)
	}
	sendHonor.Tick(100)

	// Same NAK list for both: 10, 50, 30 (receiver wants 10 first - it's most urgent)
	nakList := makeNakListFromPairs([][2]uint32{{10, 10}, {50, 50}, {30, 30}})

	sendOriginal.NAK(nakList)
	sendHonor.NAK(nakList)

	// Original: newest-first from lossList → 50, 30, 10
	require.Equal(t, []uint32{50, 30, 10}, originalSeqs)

	// HonorOrder: NAK list order (receiver priority) → 10, 50, 30
	require.Equal(t, []uint32{10, 50, 30}, honorSeqs)

	t.Logf("Original order: %v (newest-first)", originalSeqs)
	t.Logf("HonorOrder:     %v (receiver priority)", honorSeqs)
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

// ============================================================================
// HonorNakOrder Tests
// ============================================================================

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

func TestSendHonorOrder_BasicSingle(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10 packets
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10) // Move all to loss list

	// NAK for single packet (seq 5)
	nakList := makeNakListFromPairs([][2]uint32{{5, 5}})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(1), nRetrans)
	require.Equal(t, []uint32{5}, retransmittedSeqs)
}

func TestSendHonorOrder_BasicRange(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10 packets
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)

	// NAK for range (seq 3-6)
	nakList := makeNakListFromPairs([][2]uint32{{3, 6}})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(4), nRetrans)
	require.Equal(t, []uint32{3, 4, 5, 6}, retransmittedSeqs)
}

func TestSendHonorOrder_MultipleSingles(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 20 packets
	for i := 0; i < 20; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(20)

	// NAK for multiple singles in specific order: 15, 5, 10
	// HonorOrder should retransmit in this order
	nakList := makeNakListFromPairs([][2]uint32{
		{15, 15},
		{5, 5},
		{10, 10},
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(3), nRetrans)
	// With HonorOrder, should be in NAK list order
	require.Equal(t, []uint32{15, 5, 10}, retransmittedSeqs)
}

func TestSendHonorOrder_MultipleRanges(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 30 packets
	for i := 0; i < 30; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(30)

	// NAK for multiple ranges in specific order: 20-22, 5-7, 12-14
	nakList := makeNakListFromPairs([][2]uint32{
		{20, 22},
		{5, 7},
		{12, 14},
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(9), nRetrans) // 3 + 3 + 3
	// With HonorOrder, should process ranges in NAK list order
	require.Equal(t, []uint32{20, 21, 22, 5, 6, 7, 12, 13, 14}, retransmittedSeqs)
}

func TestSendHonorOrder_MixedSinglesAndRanges(t *testing.T) {
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 50 packets
	for i := 0; i < 50; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(50)

	// Mixed NAK: range 40-42, single 10, range 25-27, single 5, single 35
	nakList := makeNakListFromPairs([][2]uint32{
		{40, 42}, // Range first (most urgent per receiver)
		{10, 10}, // Single
		{25, 27}, // Range
		{5, 5},   // Single
		{35, 35}, // Single
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(9), nRetrans) // 3 + 1 + 3 + 1 + 1
	// Verify order matches NAK list order
	require.Equal(t, []uint32{40, 41, 42, 10, 25, 26, 27, 5, 35}, retransmittedSeqs)
}

func TestSendHonorOrder_ModulusDrops(t *testing.T) {
	// Simulate receiver NAK for modulus-based drops (every 10th packet)
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 100 packets
	for i := 0; i < 100; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(100)

	// NAK for every 10th packet (as receiver would generate from modulus drops)
	// Receiver NAK btree orders by urgency (oldest first typically)
	var pairs [][2]uint32
	for i := 10; i <= 90; i += 10 {
		pairs = append(pairs, [2]uint32{uint32(i), uint32(i)})
	}
	nakList := makeNakListFromPairs(pairs)
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(9), nRetrans) // 9 packets
	// Verify order matches NAK list order (10, 20, 30, ...)
	expected := []uint32{10, 20, 30, 40, 50, 60, 70, 80, 90}
	require.Equal(t, expected, retransmittedSeqs)
}

func TestSendHonorOrder_BurstDrops(t *testing.T) {
	// Simulate receiver NAK for burst drops (consecutive packets)
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 200 packets
	for i := 0; i < 200; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(200)

	// NAK for burst drops: 30-39, 80-89, 150-159 (as receiver would consolidate)
	nakList := makeNakListFromPairs([][2]uint32{
		{30, 39},
		{80, 89},
		{150, 159},
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(30), nRetrans) // 10 + 10 + 10

	// Verify packets are in NAK list order (range by range)
	expected := []uint32{}
	for i := uint32(30); i <= 39; i++ {
		expected = append(expected, i)
	}
	for i := uint32(80); i <= 89; i++ {
		expected = append(expected, i)
	}
	for i := uint32(150); i <= 159; i++ {
		expected = append(expected, i)
	}
	require.Equal(t, expected, retransmittedSeqs)
}

func TestSendHonorOrder_RealisticConsolidatedNAK(t *testing.T) {
	// Simulate a realistic NAK packet from receiver with mixed singles and ranges
	// This is what the receiver's consolidateNakBtree() would produce
	retransmittedSeqs := []uint32{}
	send := mockLiveSendHonorOrder(func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 500 packets
	for i := 0; i < 500; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(500)

	// Realistic NAK from receiver (ordered by urgency - oldest first):
	// - Range 50-54 (oldest burst)
	// - Single 75
	// - Range 100-102
	// - Singles 150, 175, 200 (scattered drops)
	// - Range 250-260 (recent burst)
	// - Single 300
	nakList := makeNakListFromPairs([][2]uint32{
		{50, 54},   // 5 packets
		{75, 75},   // 1 packet
		{100, 102}, // 3 packets
		{150, 150}, // 1 packet
		{175, 175}, // 1 packet
		{200, 200}, // 1 packet
		{250, 260}, // 11 packets
		{300, 300}, // 1 packet
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(24), nRetrans) // 5+1+3+1+1+1+11+1

	// Verify retransmission order matches NAK list order
	expected := []uint32{}
	for i := uint32(50); i <= 54; i++ {
		expected = append(expected, i)
	}
	expected = append(expected, 75)
	for i := uint32(100); i <= 102; i++ {
		expected = append(expected, i)
	}
	expected = append(expected, 150, 175, 200)
	for i := uint32(250); i <= 260; i++ {
		expected = append(expected, i)
	}
	expected = append(expected, 300)

	require.Equal(t, expected, retransmittedSeqs)
	t.Logf("✅ Realistic consolidated NAK: %d packets retransmitted in correct order", nRetrans)
}

func TestSendHonorOrder_NotFoundPackets(t *testing.T) {
	// Test that packets not in lossList are counted as not found
	send := mockLiveSendHonorOrder(func(p packet.Packet) {})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push only 10 packets (0-9)
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)

	// NAK for packets that don't exist (100, 200)
	nakList := makeNakListFromPairs([][2]uint32{
		{5, 5},     // Exists
		{100, 100}, // Doesn't exist
		{200, 200}, // Doesn't exist
	})
	nRetrans := send.NAK(nakList)

	require.Equal(t, uint64(1), nRetrans) // Only packet 5 was retransmitted

	// Check NakNotFound metric
	require.Equal(t, uint64(2), send.metrics.CongestionSendNAKNotFound.Load())
}

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

func TestSendHonorOrder_LargeScale(t *testing.T) {
	// Test with a large number of NAK entries
	// Use high drop threshold to prevent packet expiry
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	retransmitCount := 0
	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         100000, // Very high to prevent drops
		OnDeliver: func(p packet.Packet) {
			if p.Header().RetransmittedPacketFlag {
				retransmitCount++
			}
		},
		ConnectionMetrics: testMetrics,
		HonorNakOrder:     true,
	}).(*sender)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10000 packets
	for i := 0; i < 10000; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10000)

	// Create NAK for singles (every 20th packet from 0-9999)
	var pairs [][2]uint32
	for i := 20; i < 10000; i += 20 {
		pairs = append(pairs, [2]uint32{uint32(i), uint32(i)})
	}
	nakList := makeNakListFromPairs(pairs)
	nRetrans := send.NAK(nakList)

	expectedCount := uint64(len(pairs))
	require.Equal(t, expectedCount, nRetrans)
	require.Equal(t, int(expectedCount), retransmitCount)
	t.Logf("✅ Large scale: %d packets retransmitted", nRetrans)
}

// Benchmark HonorNakOrder vs Original
func BenchmarkNAK_Original(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		send := mockLiveSend(func(p packet.Packet) {})

		for j := 0; j < 1000; j++ {
			p := packet.NewPacket(addr)
			p.Header().PktTsbpdTime = uint64(j + 1)
			send.Push(p)
		}
		send.Tick(1000)

		// NAK for 100 singles
		var pairs [][2]uint32
		for j := 10; j <= 1000; j += 10 {
			pairs = append(pairs, [2]uint32{uint32(j), uint32(j)})
		}
		nakList := makeNakListFromPairs(pairs)

		b.StartTimer()
		send.NAK(nakList)
	}
}

func BenchmarkNAK_HonorOrder(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		send := mockLiveSendHonorOrder(func(p packet.Packet) {})

		for j := 0; j < 1000; j++ {
			p := packet.NewPacket(addr)
			p.Header().PktTsbpdTime = uint64(j + 1)
			send.Push(p)
		}
		send.Tick(1000)

		// NAK for 100 singles
		var pairs [][2]uint32
		for j := 10; j <= 1000; j += 10 {
			pairs = append(pairs, [2]uint32{uint32(j), uint32(j)})
		}
		nakList := makeNakListFromPairs(pairs)

		b.StartTimer()
		send.NAK(nakList)
	}
}
