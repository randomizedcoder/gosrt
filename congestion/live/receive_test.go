package live

import (
	"net"
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
)

func mockLiveRecv(onSendACK func(seq circular.Number, light bool), onSendNAK func(list []circular.Number), onDeliver func(p packet.Packet)) *receiver {
	// Initialize metrics for tests
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44) // IPv4 + UDP + SRT header

	recv := NewReceiver(ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10,
		PeriodicNAKInterval:   20,
		OnSendACK:             onSendACK,
		OnSendNAK:             onSendNAK,
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
	})

	return recv.(*receiver)
}

func TestRecvSequence(t *testing.T) {
	nACK := 0
	nNAK := 0
	numbers := []uint32{}
	recv := mockLiveRecv(
		func(seq circular.Number, light bool) {
			nACK++
		},
		func(list []circular.Number) {
			nNAK++
		},
		func(p packet.Packet) {
			numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	require.Equal(t, 0, nACK)
	require.Equal(t, 0, nNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(0), recv.lastACKSequenceNumber.Inc().Val())

	recv.Tick(1)

	require.Equal(t, 0, nACK)
	require.Equal(t, 0, nNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(0), recv.lastACKSequenceNumber.Inc().Val())

	recv.Tick(10) // ACK period

	require.Equal(t, 1, nACK)
	require.Equal(t, 0, nNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(9), recv.lastACKSequenceNumber.Val())

	require.Exactly(t, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, numbers)
}

func TestRecvTSBPD(t *testing.T) {
	numbers := []uint32{}
	recv := mockLiveRecv(
		nil,
		nil,
		func(p packet.Packet) {
			numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 20; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	require.Equal(t, uint32(19), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(0), recv.lastACKSequenceNumber.Inc().Val())

	recv.Tick(10) // ACK period

	require.Equal(t, uint32(19), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(19), recv.lastACKSequenceNumber.Val())

	require.Exactly(t, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, numbers)
}

func TestRecvNAK(t *testing.T) {
	seqACK := uint32(0)
	seqNAK := []uint32{}
	numbers := []uint32{}
	recv := mockLiveRecv(
		func(seq circular.Number, light bool) {
			seqACK = seq.Val()
		},
		func(list []circular.Number) {
			seqNAK = []uint32{}
			for _, sn := range list {
				seqNAK = append(seqNAK, sn.Val())
			}
		},
		func(p packet.Packet) {
			numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	require.Equal(t, uint32(0), seqACK)
	require.Equal(t, []uint32{}, seqNAK)
	require.Equal(t, uint32(4), recv.maxSeenSequenceNumber.Val())

	for i := 7; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	require.Equal(t, uint32(0), seqACK)
	require.Equal(t, []uint32{5, 6}, seqNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())

	recv.Tick(10) // ACK period

	require.Equal(t, uint32(10), seqACK)
	require.Equal(t, []uint32{5, 6}, seqNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())
}

func TestRecvPeriodicNAK(t *testing.T) {
	seqACK := uint32(0)
	seqNAK := []uint32{}
	numbers := []uint32{}
	recv := mockLiveRecv(
		func(seq circular.Number, light bool) {
			seqACK = seq.Val()
		},
		func(list []circular.Number) {
			seqNAK = []uint32{}
			for _, sn := range list {
				seqNAK = append(seqNAK, sn.Val())
			}
		},
		func(p packet.Packet) {
			numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(50 + i + 1)

		recv.Push(p)
	}

	require.Equal(t, uint32(0), seqACK)
	require.Equal(t, []uint32{}, seqNAK)
	require.Equal(t, uint32(4), recv.maxSeenSequenceNumber.Val())

	for i := 7; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(50 + i + 1)

		recv.Push(p)
	}

	require.Equal(t, uint32(0), seqACK)
	require.Equal(t, []uint32{5, 6}, seqNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())

	recv.Tick(10) // ACK period

	require.Equal(t, uint32(5), seqACK)
	require.Equal(t, []uint32{5, 6}, seqNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())

	recv.Tick(20) // ACK period, NAK period

	require.Equal(t, uint32(5), seqACK)
	require.Equal(t, []uint32{5, 6}, seqNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())
}

func TestRecvACK(t *testing.T) {
	seqACK := uint32(0)
	seqNAK := []uint32{}
	numbers := []uint32{}
	recv := mockLiveRecv(
		func(seq circular.Number, light bool) {
			seqACK = seq.Val()
		},
		func(list []circular.Number) {
			seqNAK = []uint32{}
			for _, sn := range list {
				seqNAK = append(seqNAK, sn.Val())
			}
		},
		func(p packet.Packet) {
			numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(10 + i + 1)

		recv.Push(p)
	}

	require.Equal(t, uint32(0), seqACK)
	require.Equal(t, []uint32{}, seqNAK)
	require.Equal(t, uint32(4), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(0), recv.lastACKSequenceNumber.Inc().Val())
	require.Equal(t, uint32(0), recv.lastDeliveredSequenceNumber.Inc().Val())
	require.Exactly(t, []uint32{}, numbers)

	for i := 7; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(30 + i + 1)

		recv.Push(p)
	}

	require.Equal(t, uint32(0), seqACK)
	require.Equal(t, []uint32{5, 6}, seqNAK)
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(0), recv.lastACKSequenceNumber.Inc().Val())
	require.Equal(t, uint32(0), recv.lastDeliveredSequenceNumber.Inc().Val())
	require.Exactly(t, []uint32{}, numbers)

	for i := 15; i < 20; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(30 + i + 1)

		recv.Push(p)
	}

	require.Equal(t, uint32(0), seqACK)
	require.Equal(t, []uint32{10, 14}, seqNAK)
	require.Equal(t, uint32(19), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(0), recv.lastACKSequenceNumber.Inc().Val())
	require.Equal(t, uint32(0), recv.lastDeliveredSequenceNumber.Inc().Val())
	require.Exactly(t, []uint32{}, numbers)

	recv.Tick(10)

	require.Equal(t, uint32(5), seqACK)
	require.Equal(t, []uint32{10, 14}, seqNAK)
	require.Equal(t, uint32(19), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(5), recv.lastACKSequenceNumber.Inc().Val())
	require.Equal(t, uint32(0), recv.lastDeliveredSequenceNumber.Inc().Val())
	require.Exactly(t, []uint32{}, numbers)

	recv.Tick(20)

	require.Equal(t, uint32(5), seqACK)
	require.Equal(t, []uint32{5, 6, 10, 14}, seqNAK)
	require.Equal(t, uint32(19), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(5), recv.lastACKSequenceNumber.Inc().Val())
	require.Equal(t, uint32(5), recv.lastDeliveredSequenceNumber.Inc().Val())
	require.Exactly(t, []uint32{0, 1, 2, 3, 4}, numbers)

	recv.Tick(30)

	require.Equal(t, uint32(5), seqACK)
	require.Equal(t, []uint32{5, 6, 10, 14}, seqNAK)
	require.Equal(t, uint32(19), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(5), recv.lastACKSequenceNumber.Inc().Val())
	require.Equal(t, uint32(5), recv.lastDeliveredSequenceNumber.Inc().Val())
	require.Exactly(t, []uint32{0, 1, 2, 3, 4}, numbers)

	for i := 5; i < 7; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(30 + i + 1)

		recv.Push(p)
	}

	recv.Tick(40)

	require.Equal(t, uint32(10), seqACK)
	require.Equal(t, []uint32{10, 14}, seqNAK)
	require.Equal(t, uint32(19), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint32(10), recv.lastACKSequenceNumber.Inc().Val())
	require.Equal(t, uint32(10), recv.lastDeliveredSequenceNumber.Inc().Val())
	require.Exactly(t, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, numbers)
}

func TestRecvDropTooLate(t *testing.T) {
	recv := mockLiveRecv(
		nil,
		nil,
		nil,
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	recv.Tick(10) // ACK period

	stats := recv.Stats()

	require.Equal(t, uint32(9), recv.lastACKSequenceNumber.Val())
	require.Equal(t, uint32(9), recv.lastDeliveredSequenceNumber.Val())
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint64(0), stats.PktDrop)

	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(uint32(3), packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = uint64(4)

	recv.Push(p)

	stats = recv.Stats()

	require.Equal(t, uint64(1), stats.PktDrop)
}

func TestRecvDropAlreadyACK(t *testing.T) {
	recv := mockLiveRecv(
		nil,
		nil,
		nil,
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	for i := 5; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(10 + i + 1)

		recv.Push(p)
	}

	recv.Tick(10) // ACK period

	stats := recv.Stats()

	require.Equal(t, uint32(9), recv.lastACKSequenceNumber.Val())
	require.Equal(t, uint32(4), recv.lastDeliveredSequenceNumber.Val())
	require.Equal(t, uint32(9), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint64(0), stats.PktDrop)

	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(uint32(6), packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = uint64(7)

	recv.Push(p)

	stats = recv.Stats()

	require.Equal(t, uint64(1), stats.PktDrop)
}

func TestRecvDropAlreadyRecvNoACK(t *testing.T) {
	recv := mockLiveRecv(
		nil,
		nil,
		nil,
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	for i := 5; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(10 + i + 1)

		recv.Push(p)
	}

	recv.Tick(10) // ACK period

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(10+i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(20 + i + 1)

		recv.Push(p)
	}

	stats := recv.Stats()

	require.Equal(t, uint32(9), recv.lastACKSequenceNumber.Val())
	require.Equal(t, uint32(4), recv.lastDeliveredSequenceNumber.Val())
	require.Equal(t, uint32(19), recv.maxSeenSequenceNumber.Val())
	require.Equal(t, uint64(0), stats.PktDrop)

	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(uint32(15), packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = uint64(20 + 6)

	recv.Push(p)

	stats = recv.Stats()

	require.Equal(t, uint64(1), stats.PktDrop)
}

func TestRecvFlush(t *testing.T) {
	recv := mockLiveRecv(
		nil,
		nil,
		nil,
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	require.Equal(t, 10, recv.packetStore.Len())

	recv.Flush()

	require.Equal(t, 0, recv.packetStore.Len())
}

func TestRecvPeriodicACKLite(t *testing.T) {
	liteACK := false
	recv := mockLiveRecv(
		func(seq circular.Number, light bool) {
			liteACK = light
		},
		nil,
		nil,
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 100; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(10 + i + 1)

		recv.Push(p)
	}

	require.Equal(t, false, liteACK)

	recv.Tick(1)

	require.Equal(t, true, liteACK)
}

func TestSkipTooLate(t *testing.T) {
	seqACK := uint32(0)
	numbers := []uint32{}
	recv := mockLiveRecv(
		func(seq circular.Number, light bool) {
			seqACK = seq.Val()
		},
		nil,
		func(p packet.Packet) {
			numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}

	recv.Tick(10)

	require.Equal(t, uint32(5), seqACK)
	require.Equal(t, []uint32{0, 1, 2, 3, 4}, numbers)

	for i := 5; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(3+i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(13 + i + 1)

		recv.Push(p)
	}

	recv.Tick(20)

	require.Equal(t, uint32(13), seqACK)
	require.Equal(t, []uint32{0, 1, 2, 3, 4, 8, 9}, numbers)
}

func TestIssue67(t *testing.T) {
	ackNumbers := []uint32{}
	nakNumbers := [][2]uint32{}
	numbers := []uint32{}
	recv := mockLiveRecv(
		func(seq circular.Number, light bool) {
			ackNumbers = append(ackNumbers, seq.Val())
		},
		func(list []circular.Number) {
			nakNumbers = append(nakNumbers, [2]uint32{list[0].Val(), list[1].Val()})
		},
		func(p packet.Packet) {
			numbers = append(numbers, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(0, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = 1

	recv.Push(p)

	recv.Tick(10)
	recv.Tick(20)
	recv.Tick(30)
	recv.Tick(40)
	recv.Tick(50)
	recv.Tick(60)
	recv.Tick(70)
	recv.Tick(80)
	recv.Tick(90)

	require.Equal(t, []uint32{1, 1, 1, 1, 1, 1, 1, 1, 1}, ackNumbers)

	p = packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(12, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = 121

	recv.Push(p)

	require.Equal(t, [][2]uint32{
		{1, 11},
	}, nakNumbers)

	p = packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(1, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = 11

	recv.Push(p)

	p = packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(11, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = 111

	recv.Push(p)

	recv.Tick(100)

	require.Equal(t, []uint32{1, 1, 1, 1, 1, 1, 1, 1, 1, 2}, ackNumbers)

	recv.Tick(110)

	require.Equal(t, []uint32{1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2}, ackNumbers)

	recv.Tick(120)

	require.Equal(t, []uint32{1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 13}, ackNumbers)

	recv.Tick(130)

	require.Equal(t, []uint32{1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 13, 13}, ackNumbers)
}

// TestListVsBTreeEquivalence verifies that list and btree implementations produce identical results
func TestListVsBTreeEquivalence(t *testing.T) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Helper to create a receiver with specified algorithm
	createReceiver := func(algorithm string) *receiver {
		// Initialize metrics for tests
		testMetrics := &metrics.ConnectionMetrics{
			HandlePacketLockTiming: &metrics.LockTimingMetrics{},
			ReceiverLockTiming:     &metrics.LockTimingMetrics{},
			SenderLockTiming:       &metrics.LockTimingMetrics{},
		}
		testMetrics.HeaderSize.Store(44) // IPv4 + UDP + SRT header

		recv := NewReceiver(ReceiveConfig{
			InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
			PeriodicACKInterval:    10,
			PeriodicNAKInterval:    20,
			PacketReorderAlgorithm: algorithm,
			BTreeDegree:            32,
			OnSendACK:              func(seq circular.Number, light bool) {},
			OnSendNAK:              func(list []circular.Number) {},
			OnDeliver:              func(p packet.Packet) {},
			ConnectionMetrics:      testMetrics,
		})
		return recv.(*receiver)
	}

	// Test 1: In-order packet insertion
	t.Run("InOrderInsertion", func(t *testing.T) {
		recvList := createReceiver("list")
		recvBTree := createReceiver("btree")

		for i := 0; i < 100; i++ {
			pList := packet.NewPacket(addr)
			pList.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			pList.Header().PktTsbpdTime = uint64(i + 1)

			pBTree := packet.NewPacket(addr)
			pBTree.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			pBTree.Header().PktTsbpdTime = uint64(i + 1)

			recvList.Push(pList)
			recvBTree.Push(pBTree)
		}

		require.Equal(t, recvList.packetStore.Len(), recvBTree.packetStore.Len())
		require.Equal(t, recvList.maxSeenSequenceNumber.Val(), recvBTree.maxSeenSequenceNumber.Val())
		require.Equal(t, recvList.lastACKSequenceNumber.Val(), recvBTree.lastACKSequenceNumber.Val())
	})

	// Test 2: Out-of-order packet insertion
	t.Run("OutOfOrderInsertion", func(t *testing.T) {
		recvList := createReceiver("list")
		recvBTree := createReceiver("btree")

		// Insert packets out of order: 0, 2, 1, 4, 3, 6, 5, ...
		for i := 0; i < 50; i++ {
			seq := uint32(i * 2)
			if i > 0 {
				seq = uint32((i-1)*2 + 1)
			}

			pList := packet.NewPacket(addr)
			pList.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
			pList.Header().PktTsbpdTime = uint64(seq + 1)

			pBTree := packet.NewPacket(addr)
			pBTree.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
			pBTree.Header().PktTsbpdTime = uint64(seq + 1)

			recvList.Push(pList)
			recvBTree.Push(pBTree)
		}

		require.Equal(t, recvList.packetStore.Len(), recvBTree.packetStore.Len())
		require.Equal(t, recvList.maxSeenSequenceNumber.Val(), recvBTree.maxSeenSequenceNumber.Val())
	})

	// Test 3: Duplicate packet handling
	t.Run("DuplicateHandling", func(t *testing.T) {
		recvList := createReceiver("list")
		recvBTree := createReceiver("btree")

		// Insert same packet twice
		p1List := packet.NewPacket(addr)
		p1List.Header().PacketSequenceNumber = circular.New(10, packet.MAX_SEQUENCENUMBER)
		p1List.Header().PktTsbpdTime = 11

		p1BTree := packet.NewPacket(addr)
		p1BTree.Header().PacketSequenceNumber = circular.New(10, packet.MAX_SEQUENCENUMBER)
		p1BTree.Header().PktTsbpdTime = 11

		recvList.Push(p1List)
		recvBTree.Push(p1BTree)

		// Insert duplicate
		p2List := packet.NewPacket(addr)
		p2List.Header().PacketSequenceNumber = circular.New(10, packet.MAX_SEQUENCENUMBER)
		p2List.Header().PktTsbpdTime = 11

		p2BTree := packet.NewPacket(addr)
		p2BTree.Header().PacketSequenceNumber = circular.New(10, packet.MAX_SEQUENCENUMBER)
		p2BTree.Header().PktTsbpdTime = 11

		recvList.Push(p2List)
		recvBTree.Push(p2BTree)

		require.Equal(t, recvList.packetStore.Len(), recvBTree.packetStore.Len())
		statsList := recvList.Stats()
		statsBTree := recvBTree.Stats()
		require.Equal(t, statsList.PktDrop, statsBTree.PktDrop)
	})

	// Test 4: Iteration order
	t.Run("IterationOrder", func(t *testing.T) {
		recvList := createReceiver("list")
		recvBTree := createReceiver("btree")

		// Insert packets out of order
		seqs := []uint32{5, 2, 8, 1, 9, 3, 7, 4, 6, 0}
		for _, seq := range seqs {
			pList := packet.NewPacket(addr)
			pList.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
			pList.Header().PktTsbpdTime = uint64(seq + 100)

			pBTree := packet.NewPacket(addr)
			pBTree.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
			pBTree.Header().PktTsbpdTime = uint64(seq + 100)

			recvList.Push(pList)
			recvBTree.Push(pBTree)
		}

		// Collect packets in order
		var listSeqs []uint32
		recvList.packetStore.Iterate(func(p packet.Packet) bool {
			listSeqs = append(listSeqs, p.Header().PacketSequenceNumber.Val())
			return true
		})

		var btreeSeqs []uint32
		recvBTree.packetStore.Iterate(func(p packet.Packet) bool {
			btreeSeqs = append(btreeSeqs, p.Header().PacketSequenceNumber.Val())
			return true
		})

		require.Equal(t, listSeqs, btreeSeqs, "Iteration order should be identical")
	})

	// Test 5: Min() operation
	t.Run("MinOperation", func(t *testing.T) {
		recvList := createReceiver("list")
		recvBTree := createReceiver("btree")

		// Insert packets out of order
		seqs := []uint32{5, 2, 8, 1, 9, 3, 7, 4, 6, 0}
		for _, seq := range seqs {
			pList := packet.NewPacket(addr)
			pList.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
			pList.Header().PktTsbpdTime = uint64(seq + 100)

			pBTree := packet.NewPacket(addr)
			pBTree.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
			pBTree.Header().PktTsbpdTime = uint64(seq + 100)

			recvList.Push(pList)
			recvBTree.Push(pBTree)
		}

		minList := recvList.packetStore.Min()
		minBTree := recvBTree.packetStore.Min()

		require.NotNil(t, minList)
		require.NotNil(t, minBTree)
		require.Equal(t, minList.Header().PacketSequenceNumber.Val(), minBTree.Header().PacketSequenceNumber.Val())
	})
}
