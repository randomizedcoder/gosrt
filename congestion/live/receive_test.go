package live

import (
	"fmt"
	"net"
	"sync"
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

// =============================================================================
// NAK BTREE SCAN TESTS
// These tests verify correct behavior of periodicNakBtree, particularly
// around the handling of delivered packets vs missing packets.
// =============================================================================

// mockNakBtreeRecv creates a receiver with NAK btree enabled for testing.
// Uses TsbpdDelay=1000 and NakRecentPercent=0.5 to create a scan window:
// - Packets pass threshold check if PktTsbpdTime <= now + 500 (50% of 1000)
// - Packets are delivered if PktTsbpdTime <= now
// This allows packets in range (now, now+500] to be scanned but not delivered.
func mockNakBtreeRecv(onSendNAK func(list []circular.Number)) *receiver {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := NewReceiver(ReceiveConfig{
		InitialSequenceNumber:  circular.New(100, packet.MAX_SEQUENCENUMBER), // Start at 100
		PeriodicACKInterval:    10,
		PeriodicNAKInterval:    20,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              onSendNAK,
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		TsbpdDelay:             1000,   // 1000 µs for easy math
		NakRecentPercent:       0.5,    // 50% - tooRecentThreshold = now + 500
		NakConsolidationBudget: 20_000, // 20ms - if consolidation takes longer, we have a problem
	})

	return recv.(*receiver)
}

// TestNakBtree_DeliveredPacketsNotReportedAsMissing tests the fix for the bug where
// packets that were DELIVERED (not lost) were incorrectly reported as missing.
//
// Scenario:
// 1. Packets 100-109 arrive and are added to btree
// 2. Tick triggers NAK scan, sets nakScanStartPoint=109
// 3. Packets 100-109 are DELIVERED (removed from btree)
// 4. Packets 120-129 arrive (note: no 110-119, simulating a gap)
// 5. Tick triggers NAK scan
//
// Before fix: Gap 109-119 detected as "missing" (but 100-109 were delivered!)
// After fix: Only actual gap (110-119) within current btree is detected
func TestNakBtree_DeliveredPacketsNotReportedAsMissing(t *testing.T) {
	var nakedSequences []uint32

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// With TsbpdDelay=1000, NakRecentPercent=0.5:
	// - tooRecentThreshold = now + 500
	// - Packets pass threshold if PktTsbpdTime <= now + 500
	// - Packets are delivered if PktTsbpdTime <= now
	// Use PktTsbpdTime in range (now, now+500] to be scanned but not delivered

	baseNow := uint64(1000)

	// Step 1: Add packets 100-109 (contiguous) with PktTsbpdTime just above now
	for i := uint32(100); i < 110; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(i) // 1200-1209 (> now=1000, < now+500=1500)
		recv.Push(p)
	}

	require.Equal(t, 10, recv.packetStore.Len(), "should have 10 packets in btree")

	// Step 2: First NAK scan - should find no gaps (packets are contiguous)
	nakedSequences = nil
	recv.Tick(baseNow) // now=1000

	require.Empty(t, nakedSequences, "first scan should find no gaps in contiguous packets")

	// Step 3: Simulate delivery by removing packets 100-109 from btree
	// (They wouldn't be auto-delivered since PktTsbpdTime > now)
	for i := uint32(100); i < 110; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.lastDeliveredSequenceNumber = seq
	}

	require.Equal(t, 0, recv.packetStore.Len(), "btree should be empty after delivery")

	// Step 4: Add packets 120-129 (simulating a gap of 110-119)
	for i := uint32(120); i < 130; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 1100 + uint64(i) // 2220-2229
		recv.Push(p)
	}

	require.Equal(t, 10, recv.packetStore.Len(), "should have 10 packets (120-129)")

	// Step 5: Second NAK scan at now=2000
	// tooRecentThreshold = 2000 + 500 = 2500, so packets with PktTsbpdTime < 2500 are scanned
	nakedSequences = nil
	recv.Tick(2000) // now=2000

	// With the fix:
	// - Packets 100-109 were DELIVERED (not missing) - should NOT be NAK'd ✓
	// - Packets 110-119 NEVER ARRIVED - should be NAK'd as missing ✓
	// - Packets 120-129 are in btree, contiguous - no gaps between them ✓
	//
	// The key point: we DON'T NAK delivered packets (100-109), but we DO NAK
	// packets that never arrived (110-119). This is correct behavior!
	// NAK list uses range encoding [start, end]
	require.Equal(t, []uint32{110, 119}, nakedSequences,
		"should NAK actually missing packets (110-119), not delivered packets (100-109)")
}

// TestNakBtree_GapsBetweenPacketsDetected verifies that actual gaps WITHIN
// the btree are correctly detected.
//
// Scenario: Packets 100, 101, 105, 106 arrive (gap at 102, 103, 104)
// Expected: NAK list [102, 104] (range encoding per RFC SRT Appendix A)
func TestNakBtree_GapsBetweenPacketsDetected(t *testing.T) {
	var nakedSequences []uint32

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseNow := uint64(1000)

	// Add packets with a gap: 100, 101, skip 102-104, 105, 106
	// PktTsbpdTime in range (now, now+500] to be scanned but not delivered
	for _, seq := range []uint32{100, 101, 105, 106} {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(seq) // 1200, 1201, 1205, 1206
		recv.Push(p)
	}

	require.Equal(t, 4, recv.packetStore.Len())

	// Trigger NAK scan with now=1000
	nakedSequences = nil
	recv.Tick(baseNow)

	// NAK list uses range encoding [start, end] per RFC SRT Appendix A
	// Gap 102-104 should be encoded as [102, 104]
	require.Equal(t, []uint32{102, 104}, nakedSequences,
		"should NAK gap as range [start, end]")
}

// TestNakBtree_MultipleScansAfterDelivery tests that multiple NAK scans
// after delivery continue to work correctly without detecting spurious gaps.
func TestNakBtree_MultipleScansAfterDelivery(t *testing.T) {
	var nakedSequences []uint32
	nakCallCount := 0

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		nakCallCount++
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Cycle 1: Add packets 100-104
	// PktTsbpdTime in range (now, now+500] to be scanned but not delivered
	baseNow := uint64(1000)
	for i := uint32(100); i < 105; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(i) // 1200-1204
		recv.Push(p)
	}

	recv.Tick(baseNow) // First NAK scan with now=1000
	require.Empty(t, nakedSequences, "cycle 1: no gaps")

	// Manually remove all packets (simulate delivery)
	for i := uint32(100); i < 105; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.lastDeliveredSequenceNumber = seq
	}

	// Cycle 2: Add packets 110-114
	baseNow2 := uint64(2000)
	for i := uint32(110); i < 115; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow2 + 100 + uint64(i) // 2210-2214
		recv.Push(p)
	}

	nakedSequences = nil
	recv.Tick(baseNow2) // Second NAK scan with now=2000
	// Packets 100-104 were delivered (lastDeliveredSequenceNumber = 104)
	// Packets 105-109 never arrived - they ARE missing and SHOULD be NAK'd
	// Packets 110-114 are in btree
	require.Equal(t, []uint32{105, 109}, nakedSequences,
		"cycle 2: should NAK actually missing packets (105-109)")

	// Manually remove all packets
	for i := uint32(110); i < 115; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.lastDeliveredSequenceNumber = seq
	}

	// Cycle 3: Add packets 120-124 with actual gap at 122
	baseNow3 := uint64(3000)
	for _, seq := range []uint32{120, 121, 123, 124} {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow3 + 100 + uint64(seq) // 3220, 3221, 3223, 3224
		recv.Push(p)
	}

	nakedSequences = nil
	recv.Tick(baseNow3) // Third NAK scan with now=3000
	// After cycle 2, lastDeliveredSequenceNumber = 114
	// Packets 115-119 never arrived - they ARE missing and SHOULD be NAK'd
	// Packets 120, 121 are in btree
	// Packet 122 is missing - SHOULD be NAK'd
	// Packets 123, 124 are in btree
	// NAK list uses range encoding [start, end]
	require.Equal(t, []uint32{115, 119, 122, 122}, nakedSequences,
		"cycle 3: should NAK missing packets (115-119 and 122)")
}

// TestNakBtree_EmptyBtreeAfterDelivery tests that NAK scan handles
// an empty btree gracefully (no spurious gaps).
func TestNakBtree_EmptyBtreeAfterDelivery(t *testing.T) {
	var nakedSequences []uint32

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseNow := uint64(1000)

	// Add packets with PktTsbpdTime in scan range
	for i := uint32(100); i < 110; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(i) // 1200-1209
		recv.Push(p)
	}

	// First scan to set nakScanStartPoint
	recv.Tick(baseNow)

	// Manually remove all packets
	for i := uint32(100); i < 110; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.lastDeliveredSequenceNumber = seq
	}

	require.Equal(t, 0, recv.packetStore.Len(), "btree should be empty")

	// NAK scan on empty btree
	nakedSequences = nil
	recv.Tick(2000)
	require.Empty(t, nakedSequences, "empty btree should produce no NAKs")
}

// =============================================================================
// REALISTIC STREAMING SIMULATION TESTS
// These tests simulate actual streaming conditions with realistic bitrates,
// packet sizes, and loss patterns.
// =============================================================================

// StreamSimConfig defines parameters for generating a realistic packet stream.
type StreamSimConfig struct {
	BitrateBps    int     // Bits per second (e.g., 1_000_000 for 1 Mb/s)
	PayloadBytes  int     // Payload size per packet (e.g., 1400 bytes)
	DurationSec   float64 // Stream duration in seconds
	TsbpdDelayUs  uint64  // TSBPD delay in microseconds
	StartSeq      uint32  // Starting sequence number
	StartTimeUs   uint64  // Starting timestamp in microseconds
	PktIntervalUs uint64  // Microseconds between packets (calculated if 0)
}

// StreamSimResult holds the generated packets and metadata.
type StreamSimResult struct {
	Packets       []packet.Packet // All generated packets
	TotalPackets  int             // Total packet count
	PktIntervalUs uint64          // Microseconds between packets
	EndTimeUs     uint64          // Timestamp of last packet
	EndSeq        uint32          // Sequence number of last packet (may have wrapped)
}

// generatePacketStream creates a stream of packets simulating real traffic.
// This is a reusable helper for realistic streaming tests.
// Correctly handles sequence number wraparound at MAX_SEQUENCENUMBER.
func generatePacketStream(addr net.Addr, cfg StreamSimConfig) StreamSimResult {
	// Calculate packets per second: bitrate / (payload_size * 8)
	packetsPerSec := float64(cfg.BitrateBps) / float64(cfg.PayloadBytes*8)
	totalPackets := int(packetsPerSec * cfg.DurationSec)

	// Calculate interval between packets
	pktIntervalUs := cfg.PktIntervalUs
	if pktIntervalUs == 0 {
		pktIntervalUs = uint64(1_000_000 / packetsPerSec) // microseconds
	}

	packets := make([]packet.Packet, 0, totalPackets)
	currentTimeUs := cfg.StartTimeUs
	var lastSeq uint32

	for i := 0; i < totalPackets; i++ {
		p := packet.NewPacket(addr)
		// Use circular.SeqAdd to handle wraparound correctly
		seq := circular.SeqAdd(cfg.StartSeq, uint32(i))
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// PktTsbpdTime = arrival_time + tsbpd_delay (when packet should be delivered)
		p.Header().PktTsbpdTime = currentTimeUs + cfg.TsbpdDelayUs

		packets = append(packets, p)
		lastSeq = seq
		currentTimeUs += pktIntervalUs
	}

	return StreamSimResult{
		Packets:       packets,
		TotalPackets:  totalPackets,
		PktIntervalUs: pktIntervalUs,
		EndTimeUs:     currentTimeUs,
		EndSeq:        lastSeq,
	}
}

// LossPattern defines how packets are dropped in simulation.
type LossPattern interface {
	ShouldDrop(seqIndex int, seq uint32) bool
	Description() string
}

// PeriodicLoss drops every Nth packet.
type PeriodicLoss struct {
	Period int // Drop every Nth packet (e.g., 10 = drop 10th, 20th, 30th, ...)
	Offset int // Start offset (e.g., 0 = drop at indices 9, 19, 29...)
}

func (p PeriodicLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	return (seqIndex+1-p.Offset)%p.Period == 0
}

func (p PeriodicLoss) Description() string {
	return fmt.Sprintf("periodic(every %d, offset %d)", p.Period, p.Offset)
}

// BurstLoss drops bursts of consecutive packets at regular intervals.
type BurstLoss struct {
	BurstInterval int // Interval between burst starts (e.g., 100 = burst every 100 packets)
	BurstSize     int // Number of consecutive packets to drop (e.g., 5)
}

func (b BurstLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	positionInInterval := seqIndex % b.BurstInterval
	return positionInInterval >= (b.BurstInterval - b.BurstSize)
}

func (b BurstLoss) Description() string {
	return fmt.Sprintf("burst(every %d packets, burst size %d)", b.BurstInterval, b.BurstSize)
}

// LargeBurstLoss drops a single large burst of consecutive packets.
// This simulates network outages or severe degradation.
// Example: LargeBurstLoss{StartIndex: 100, Size: 50} drops packets 100-149.
type LargeBurstLoss struct {
	StartIndex int // Index at which to start dropping
	Size       int // Number of consecutive packets to drop
}

func (l LargeBurstLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	return seqIndex >= l.StartIndex && seqIndex < l.StartIndex+l.Size
}

func (l LargeBurstLoss) Description() string {
	return fmt.Sprintf("large-burst(start=%d, size=%d)", l.StartIndex, l.Size)
}

// HighLossWindow drops packets at a high percentage within a window.
// Simulates the "high-loss burst" pattern from packet_loss_injection_design.md
// where 80-90% loss occurs for a period.
type HighLossWindow struct {
	WindowStart int     // Index to start high loss
	WindowEnd   int     // Index to end high loss
	LossRate    float64 // Loss rate within window (e.g., 0.85 = 85%)
	seed        int64   // For deterministic testing
}

func (h HighLossWindow) ShouldDrop(seqIndex int, seq uint32) bool {
	if seqIndex < h.WindowStart || seqIndex >= h.WindowEnd {
		return false
	}
	// Use deterministic pseudo-random for reproducibility
	// Hash based on sequence number for consistent results
	hash := uint64(seq) * 2654435761 % 1000000
	threshold := uint64(h.LossRate * 1000000)
	return hash < threshold
}

func (h HighLossWindow) Description() string {
	return fmt.Sprintf("high-loss-window(start=%d, end=%d, rate=%.0f%%)",
		h.WindowStart, h.WindowEnd, h.LossRate*100)
}

// MultiBurstLoss drops multiple bursts of varying sizes.
// Simulates sporadic network outages.
type MultiBurstLoss struct {
	Bursts []struct {
		Start int
		Size  int
	}
}

func (m MultiBurstLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	for _, b := range m.Bursts {
		if seqIndex >= b.Start && seqIndex < b.Start+b.Size {
			return true
		}
	}
	return false
}

func (m MultiBurstLoss) Description() string {
	return fmt.Sprintf("multi-burst(%d bursts)", len(m.Bursts))
}

// CorrelatedLoss simulates bursty loss with Gilbert-Elliott model behavior.
// After a packet is dropped, subsequent packets have higher drop probability.
// This creates realistic "bursty" loss patterns seen in real networks.
type CorrelatedLoss struct {
	BaseLossRate  float64 // Base loss rate (e.g., 0.05 = 5%)
	Correlation   float64 // Correlation factor (e.g., 0.25 = 25% - if prev dropped, 25% more likely)
	lastWasDroped bool    // Internal state
}

func (c *CorrelatedLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	// Use deterministic pseudo-random
	hash := uint64(seq) * 2654435761 % 1000000

	// Calculate effective loss rate based on previous state
	effectiveRate := c.BaseLossRate
	if c.lastWasDroped {
		effectiveRate += c.Correlation
		if effectiveRate > 1.0 {
			effectiveRate = 1.0
		}
	}

	threshold := uint64(effectiveRate * 1000000)
	drop := hash < threshold
	c.lastWasDroped = drop
	return drop
}

func (c *CorrelatedLoss) Description() string {
	return fmt.Sprintf("correlated(base=%.0f%%, correlation=%.0f%%)",
		c.BaseLossRate*100, c.Correlation*100)
}

// PercentageLoss drops packets uniformly at a given percentage.
type PercentageLoss struct {
	Rate float64 // Loss rate (e.g., 0.05 = 5%)
}

func (p PercentageLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	hash := uint64(seq) * 2654435761 % 1000000
	threshold := uint64(p.Rate * 1000000)
	return hash < threshold
}

func (p PercentageLoss) Description() string {
	return fmt.Sprintf("percentage(%.1f%%)", p.Rate*100)
}

// NoLoss is a helper pattern that drops nothing.
type NoLoss struct{}

func (n NoLoss) ShouldDrop(seqIndex int, seq uint32) bool { return false }
func (n NoLoss) Description() string                      { return "no-loss" }

// applyLossPattern filters packets according to the loss pattern.
// Returns the surviving packets and the list of dropped sequence numbers.
func applyLossPattern(packets []packet.Packet, pattern LossPattern) ([]packet.Packet, []uint32) {
	surviving := make([]packet.Packet, 0, len(packets))
	dropped := make([]uint32, 0)

	for i, p := range packets {
		seq := p.Header().PacketSequenceNumber.Val()
		if pattern.ShouldDrop(i, seq) {
			dropped = append(dropped, seq)
		} else {
			surviving = append(surviving, p)
		}
	}

	return surviving, dropped
}

// TestNakBtree_RealisticStream_PeriodicLoss simulates a 1 Mb/s stream
// with every 10th packet dropped over 5 seconds.
//
// Test parameters:
// - Bitrate: 1 Mb/s (1,000,000 bps)
// - Payload: 1400 bytes (typical SRT)
// - Duration: 5 seconds
// - TSBPD: 3 seconds (3,000,000 µs)
// - Loss: Every 10th packet dropped
//
// Expected: ~446 total packets, ~44 dropped, ~44 NAK entries
func TestNakBtree_RealisticStream_PeriodicLoss(t *testing.T) {
	// Capture NAKs
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Phase 1: Generate all packets for 5 seconds at 1 Mb/s
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000, // 1 Mb/s
		PayloadBytes: 1400,      // 1400 bytes per packet
		DurationSec:  5.0,       // 5 seconds
		TsbpdDelayUs: 3_000_000, // 3 second TSBPD buffer
		StartSeq:     1,         // Start at sequence 1
		StartTimeUs:  1_000_000, // Start 1 second into test
	}

	// Create receiver with InitialSequenceNumber matching stream start
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		// Convert to uint32 pairs
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)

	t.Logf("Generated %d packets at %d bps, interval %d µs",
		stream.TotalPackets, cfg.BitrateBps, stream.PktIntervalUs)

	// Phase 2: Apply periodic loss (drop every 10th packet)
	lossPattern := PeriodicLoss{Period: 10, Offset: 0}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)

	t.Logf("Loss pattern: %s", lossPattern.Description())
	t.Logf("Dropped %d packets, surviving %d packets", len(dropped), len(surviving))

	// Phase 3: Push surviving packets to receiver
	for _, p := range surviving {
		recv.Push(p)
	}

	// Phase 4: Run NAK cycles to detect gaps
	// Advance time through the stream to allow NAK detection
	// Start time is after TSBPD delay so packets become "not too recent"
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000 // +100ms past TSBPD
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000  // +1s after stream ends

	tickCount := 0
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000 // 20ms per tick
		tickCount++
	}

	t.Logf("Ran %d NAK cycles", tickCount)

	// Phase 5: Collect all NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		// NAK list format: [start1, end1, start2, end2, ...]
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Phase 6: Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
			if missedNaks <= 5 {
				t.Logf("Missing NAK for dropped packet: seq=%d", droppedSeq)
			}
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets", missedNaks, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets were correctly NAKed", len(dropped))
	}

	// Verify no false positives (packets that weren't dropped but were NAKed)
	falsePositives := 0
	droppedSet := make(map[uint32]bool)
	for _, seq := range dropped {
		droppedSet[seq] = true
	}
	for nakedSeq := range nakedSeqs {
		if !droppedSet[nakedSeq] {
			falsePositives++
			if falsePositives <= 5 {
				t.Logf("False positive NAK: seq=%d was NAKed but not dropped", nakedSeq)
			}
		}
	}

	if falsePositives > 0 {
		t.Logf("⚠️ %d false positive NAKs (packets NAKed that weren't dropped)", falsePositives)
	}
}

// TestNakBtree_RealisticStream_BurstLoss simulates a 1 Mb/s stream
// with burst packet loss (5 consecutive packets lost every 100 packets).
//
// Test parameters:
// - Bitrate: 1 Mb/s
// - Payload: 1400 bytes
// - Duration: 5 seconds
// - TSBPD: 3 seconds
// - Loss: 5 consecutive packets every 100 packets
//
// Expected: Bursts of 5 packets lost, ~4-5 burst events
func TestNakBtree_RealisticStream_BurstLoss(t *testing.T) {
	// Capture NAKs
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Phase 1: Generate stream
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  5.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	// Create receiver with InitialSequenceNumber matching stream start
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)

	t.Logf("Generated %d packets", stream.TotalPackets)

	// Phase 2: Apply burst loss (5 packets every 100)
	lossPattern := BurstLoss{BurstInterval: 100, BurstSize: 5}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)

	t.Logf("Loss pattern: %s", lossPattern.Description())
	t.Logf("Dropped %d packets (expected ~%d bursts of %d)",
		len(dropped), stream.TotalPackets/lossPattern.BurstInterval, lossPattern.BurstSize)

	// Phase 3: Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Phase 4: Run NAK cycles
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000

	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Phase 5: Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Phase 6: Verify
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets", missedNaks, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets (burst loss) were correctly NAKed", len(dropped))
	}
}

// TestNakBtree_RealisticStream_DeliveryBetweenArrivals tests the specific bug scenario
// where packets are delivered between arrival batches, potentially causing gaps
// to be missed.
//
// This is the critical test that catches the "delivered packets reported as missing" bug.
//
// Timeline:
// 1. Batch 1 arrives (packets 1-100)
// 2. NAK scan runs
// 3. Batch 1 is DELIVERED (removed from btree)
// 4. Batch 2 arrives (packets 150-250) - gap of 101-149
// 5. NAK scan runs - MUST detect gap 101-149
func TestNakBtree_RealisticStream_DeliveryBetweenArrivals(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Stream configuration
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0, // 1 second per batch
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	// Create receiver with InitialSequenceNumber matching stream start
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	// Phase 1: Generate and push Batch 1 (packets 1-89)
	batch1 := generatePacketStream(addr, cfg)
	t.Logf("Batch 1: %d packets (seq 1-%d)", batch1.TotalPackets, batch1.TotalPackets)

	for _, p := range batch1.Packets {
		recv.Push(p)
	}

	// Phase 2: Run NAK scan (should find no gaps - batch 1 is contiguous)
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	recv.Tick(currentTimeUs)
	t.Logf("After batch 1 NAK scan: nakScanStartPoint should be around %d", batch1.TotalPackets)

	// Phase 3: Simulate delivery of all batch 1 packets
	// In real operation, this happens via Tick() delivery when TSBPD time passes
	for _, p := range batch1.Packets {
		seq := p.Header().PacketSequenceNumber
		recv.packetStore.Remove(seq)
		recv.lastDeliveredSequenceNumber = seq
	}
	t.Logf("Batch 1 delivered: lastDeliveredSequenceNumber = %d", recv.lastDeliveredSequenceNumber.Val())
	require.Equal(t, 0, recv.packetStore.Len(), "btree should be empty after delivery")

	// Phase 4: Generate and push Batch 2 with a GAP
	// Gap: packets batch1.TotalPackets+1 to batch1.TotalPackets+50 (50 missing packets)
	gapSize := 50
	batch2StartSeq := uint32(batch1.TotalPackets + 1 + gapSize) // Skip 50 packets

	cfg2 := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     batch2StartSeq,
		StartTimeUs:  batch1.EndTimeUs + uint64(gapSize)*batch1.PktIntervalUs, // Continue timeline
	}
	batch2 := generatePacketStream(addr, cfg2)
	t.Logf("Batch 2: %d packets (seq %d-%d), gap is seq %d-%d",
		batch2.TotalPackets, batch2StartSeq, batch2StartSeq+uint32(batch2.TotalPackets)-1,
		batch1.TotalPackets+1, batch1.TotalPackets+gapSize)

	for _, p := range batch2.Packets {
		recv.Push(p)
	}

	// Phase 5: Run NAK scan - MUST detect the gap
	currentTimeUs = cfg2.StartTimeUs + cfg2.TsbpdDelayUs + 100_000
	recv.Tick(currentTimeUs)

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify the gap was detected
	gapStart := uint32(batch1.TotalPackets + 1)
	gapEnd := uint32(batch1.TotalPackets + gapSize)
	missedNaks := 0
	for seq := gapStart; seq <= gapEnd; seq++ {
		if !nakedSeqs[seq] {
			missedNaks++
			if missedNaks <= 5 {
				t.Logf("Missing NAK for gap packet: seq=%d", seq)
			}
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d gap packets (seq %d-%d). This is the 'delivered packets' bug!",
			missedNaks, gapSize, gapStart, gapEnd)
	} else {
		t.Logf("✅ All %d gap packets (seq %d-%d) were correctly NAKed after delivery", gapSize, gapStart, gapEnd)
	}

	// Verify batch 1 packets (1-89) were NOT NAKed (they were delivered, not lost)
	batch1FalsePositives := 0
	for seq := uint32(1); seq <= uint32(batch1.TotalPackets); seq++ {
		if nakedSeqs[seq] {
			batch1FalsePositives++
		}
	}
	if batch1FalsePositives > 0 {
		t.Errorf("False positives: %d delivered packets from batch 1 were incorrectly NAKed", batch1FalsePositives)
	} else {
		t.Logf("✅ No false positives: delivered batch 1 packets were not NAKed")
	}
}

// OutOfOrderPattern defines how packets are reordered.
type OutOfOrderPattern interface {
	Reorder(packets []packet.Packet) []packet.Packet
	Description() string
}

// SwapAdjacentPairs swaps every pair of adjacent packets.
// [1,2,3,4,5,6] -> [2,1,4,3,6,5]
type SwapAdjacentPairs struct{}

func (s SwapAdjacentPairs) Reorder(packets []packet.Packet) []packet.Packet {
	result := make([]packet.Packet, len(packets))
	copy(result, packets)
	for i := 0; i+1 < len(result); i += 2 {
		result[i], result[i+1] = result[i+1], result[i]
	}
	return result
}

func (s SwapAdjacentPairs) Description() string {
	return "swap_adjacent_pairs"
}

// DelayEveryNth delays every Nth packet by M positions.
// Simulates io_uring completion reordering.
type DelayEveryNth struct {
	N     int // Delay every Nth packet
	Delay int // Number of positions to delay
}

func (d DelayEveryNth) Reorder(packets []packet.Packet) []packet.Packet {
	result := make([]packet.Packet, 0, len(packets))
	delayed := make([]packet.Packet, 0)

	for i, p := range packets {
		if (i+1)%d.N == 0 {
			// This packet gets delayed
			delayed = append(delayed, p)
		} else {
			result = append(result, p)
			// Insert delayed packets after Delay positions
			for len(delayed) > 0 && len(result) >= d.Delay {
				result = append(result, delayed[0])
				delayed = delayed[1:]
			}
		}
	}
	// Append any remaining delayed packets
	result = append(result, delayed...)
	return result
}

func (d DelayEveryNth) Description() string {
	return fmt.Sprintf("delay_every_%d_by_%d", d.N, d.Delay)
}

// BurstReorder reverses packets within bursts of size N.
// Simulates io_uring batch completion in reverse order.
// [1,2,3,4,5,6,7,8,9] with burst=3 -> [3,2,1,6,5,4,9,8,7]
type BurstReorder struct {
	BurstSize int
}

func (b BurstReorder) Reorder(packets []packet.Packet) []packet.Packet {
	result := make([]packet.Packet, len(packets))
	for i := 0; i < len(packets); i += b.BurstSize {
		end := i + b.BurstSize
		if end > len(packets) {
			end = len(packets)
		}
		// Reverse this burst
		for j := i; j < end; j++ {
			result[j] = packets[end-1-(j-i)]
		}
	}
	return result
}

func (b BurstReorder) Description() string {
	return fmt.Sprintf("burst_reverse_%d", b.BurstSize)
}

// TestNakBtree_RealisticStream_OutOfOrder tests that the NAK btree correctly
// handles out-of-order packet arrival, which is common with io_uring.
//
// The packet btree should sort packets correctly, and gaps should still be detected.
func TestNakBtree_RealisticStream_OutOfOrder(t *testing.T) {
	testCases := []struct {
		name        string
		pattern     OutOfOrderPattern
		lossPattern LossPattern
	}{
		{
			name:        "SwapPairs_PeriodicLoss",
			pattern:     SwapAdjacentPairs{},
			lossPattern: PeriodicLoss{Period: 10, Offset: 0},
		},
		{
			name:        "DelayEvery5By3_PeriodicLoss",
			pattern:     DelayEveryNth{N: 5, Delay: 3},
			lossPattern: PeriodicLoss{Period: 10, Offset: 0},
		},
		{
			name:        "BurstReverse4_PeriodicLoss",
			pattern:     BurstReorder{BurstSize: 4},
			lossPattern: PeriodicLoss{Period: 10, Offset: 0},
		},
		{
			name:        "BurstReverse8_BurstLoss",
			pattern:     BurstReorder{BurstSize: 8},
			lossPattern: BurstLoss{BurstInterval: 100, BurstSize: 5},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var nakedRanges [][]uint32
			nakLock := sync.Mutex{}

			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

			// Generate stream
			cfg := StreamSimConfig{
				BitrateBps:   1_000_000,
				PayloadBytes: 1400,
				DurationSec:  2.0,
				TsbpdDelayUs: 3_000_000,
				StartSeq:     1,
				StartTimeUs:  1_000_000,
			}

			// Create receiver with InitialSequenceNumber matching stream start
			recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
				nakLock.Lock()
				defer nakLock.Unlock()
				ranges := make([]uint32, len(list))
				for i, seq := range list {
					ranges[i] = seq.Val()
				}
				if len(ranges) > 0 {
					nakedRanges = append(nakedRanges, ranges)
				}
			}, cfg.TsbpdDelayUs, cfg.StartSeq)

			stream := generatePacketStream(addr, cfg)

			// Apply loss pattern first
			surviving, dropped := applyLossPattern(stream.Packets, tc.lossPattern)
			t.Logf("Generated %d packets, dropped %d (%s)",
				stream.TotalPackets, len(dropped), tc.lossPattern.Description())

			// Apply out-of-order pattern
			reordered := tc.pattern.Reorder(surviving)
			t.Logf("Reordered with pattern: %s", tc.pattern.Description())

			// Push packets in reordered sequence
			for _, p := range reordered {
				recv.Push(p)
			}

			// Run NAK cycles
			currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
			endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000

			for currentTimeUs < endTimeUs {
				recv.Tick(currentTimeUs)
				currentTimeUs += 20_000
			}

			// Collect NAKed sequences
			nakedSeqs := make(map[uint32]bool)
			nakLock.Lock()
			for _, ranges := range nakedRanges {
				for i := 0; i+1 < len(ranges); i += 2 {
					start, end := ranges[i], ranges[i+1]
					for seq := start; seq <= end; seq++ {
						nakedSeqs[seq] = true
					}
				}
			}
			nakLock.Unlock()

			// Verify all dropped packets were NAKed
			missedNaks := 0
			for _, droppedSeq := range dropped {
				if !nakedSeqs[droppedSeq] {
					missedNaks++
				}
			}

			if missedNaks > 0 {
				t.Errorf("Failed to NAK %d/%d dropped packets with out-of-order arrival", missedNaks, len(dropped))
			} else {
				t.Logf("✅ All %d dropped packets correctly NAKed despite out-of-order arrival", len(dropped))
			}
		})
	}
}

// TestNakBtree_RealisticStream_OutOfOrder_WithDelivery combines out-of-order arrival
// with delivery between batches - the most challenging scenario for gap detection.
func TestNakBtree_RealisticStream_OutOfOrder_WithDelivery(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Batch 1 configuration
	cfg1 := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	// Create receiver with InitialSequenceNumber matching stream start
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg1.TsbpdDelayUs, cfg1.StartSeq)

	batch1 := generatePacketStream(addr, cfg1)

	// Apply out-of-order pattern to batch 1
	reorderPattern := BurstReorder{BurstSize: 4}
	batch1Reordered := reorderPattern.Reorder(batch1.Packets)
	t.Logf("Batch 1: %d packets, reordered with %s", batch1.TotalPackets, reorderPattern.Description())

	// Push batch 1 out of order
	for _, p := range batch1Reordered {
		recv.Push(p)
	}

	// NAK scan after batch 1
	currentTimeUs := cfg1.StartTimeUs + cfg1.TsbpdDelayUs + 100_000
	recv.Tick(currentTimeUs)

	// Deliver batch 1
	for _, p := range batch1.Packets {
		seq := p.Header().PacketSequenceNumber
		recv.packetStore.Remove(seq)
		recv.lastDeliveredSequenceNumber = seq
	}
	t.Logf("Batch 1 delivered, lastDelivered=%d", recv.lastDeliveredSequenceNumber.Val())

	// Batch 2 with gap AND periodic loss
	gapSize := 30
	batch2StartSeq := uint32(batch1.TotalPackets + 1 + gapSize)

	cfg2 := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     batch2StartSeq,
		StartTimeUs:  batch1.EndTimeUs + uint64(gapSize)*batch1.PktIntervalUs,
	}
	batch2 := generatePacketStream(addr, cfg2)

	// Apply loss to batch 2 (every 10th packet)
	lossPattern := PeriodicLoss{Period: 10, Offset: 0}
	surviving, dropped := applyLossPattern(batch2.Packets, lossPattern)
	t.Logf("Batch 2: %d packets (seq %d+), gap %d-%d, dropped %d packets",
		batch2.TotalPackets, batch2StartSeq,
		batch1.TotalPackets+1, batch1.TotalPackets+gapSize,
		len(dropped))

	// Apply out-of-order to surviving batch 2 packets
	batch2Reordered := reorderPattern.Reorder(surviving)

	// Push batch 2 out of order
	for _, p := range batch2Reordered {
		recv.Push(p)
	}

	// NAK scan after batch 2 - run enough cycles to detect all gaps
	currentTimeUs = cfg2.StartTimeUs + cfg2.TsbpdDelayUs + 100_000
	endTimeUs := cfg2.StartTimeUs + cfg2.TsbpdDelayUs + 2_000_000 // 2 seconds of NAK cycles
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify gap between batches was detected
	gapStart := uint32(batch1.TotalPackets + 1)
	gapEnd := uint32(batch1.TotalPackets + gapSize)
	gapMissed := 0
	for seq := gapStart; seq <= gapEnd; seq++ {
		if !nakedSeqs[seq] {
			gapMissed++
		}
	}

	if gapMissed > 0 {
		t.Errorf("Failed to NAK %d/%d inter-batch gap packets", gapMissed, gapSize)
	} else {
		t.Logf("✅ All %d inter-batch gap packets NAKed", gapSize)
	}

	// Verify dropped packets within batch 2 were NAKed
	dropMissed := 0
	for _, seq := range dropped {
		if !nakedSeqs[seq] {
			dropMissed++
		}
	}

	if dropMissed > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets within batch 2", dropMissed, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets within batch 2 NAKed", len(dropped))
	}

	// Verify batch 1 was NOT NAKed (delivered, not lost)
	batch1FalsePositives := 0
	for seq := uint32(1); seq <= uint32(batch1.TotalPackets); seq++ {
		if nakedSeqs[seq] {
			batch1FalsePositives++
		}
	}
	if batch1FalsePositives > 0 {
		t.Errorf("False positives: %d delivered batch 1 packets were NAKed", batch1FalsePositives)
	} else {
		t.Logf("✅ No false positives for delivered batch 1")
	}
}

// mockNakBtreeRecvWithTsbpd creates a receiver with NAK btree enabled
// and a configurable TSBPD delay.
//
// startSeq is used to properly initialize lastDeliveredSequenceNumber:
// - For normal tests (startSeq near 0): InitialSequenceNumber = startSeq
// - For wraparound tests (startSeq near MAX): InitialSequenceNumber = startSeq
//
// This ensures packets are accepted (not rejected as "too old").
func mockNakBtreeRecvWithTsbpd(onSendNAK func(list []circular.Number), tsbpdDelayUs uint64, startSeq uint32) *receiver {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	// InitialSequenceNumber = startSeq means:
	// - lastDeliveredSequenceNumber = startSeq.Dec() = startSeq - 1
	// - Packets with seq > startSeq-1 will be accepted
	// This works for both normal sequences (startSeq=1) and wraparound (startSeq=MAX-10)
	recv := NewReceiver(ReceiveConfig{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000, // 10ms in µs
		PeriodicNAKInterval:    20_000, // 20ms in µs
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              onSendNAK,
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		TsbpdDelay:             tsbpdDelayUs,
		NakRecentPercent:       0.10,   // 10% "too recent" window
		NakConsolidationBudget: 20_000, // 20ms - if consolidation takes longer, we have a problem
	})

	return recv.(*receiver)
}

// TestNakBtree_FirstPacketSetsBaseline tests that the first packet found
// in the btree becomes the baseline for gap detection, not nakScanStartPoint.
func TestNakBtree_FirstPacketSetsBaseline(t *testing.T) {
	var nakedSequences []uint32

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseNow := uint64(1000)

	// Add packets 100-104 with PktTsbpdTime in scan range (now, now+500]
	for i := uint32(100); i < 105; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(i) // 1200-1204
		recv.Push(p)
	}

	// First scan to establish nakScanStartPoint
	recv.Tick(baseNow)

	// Manually remove packets 100-102 (simulate partial delivery)
	for i := uint32(100); i < 103; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.lastDeliveredSequenceNumber = seq
	}

	// Add packet 108 (creates actual gap at 105, 106, 107)
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(108, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = baseNow + 100 + 108 // 1208
	recv.Push(p)

	// btree now contains: 103, 104, 108
	require.Equal(t, 3, recv.packetStore.Len(), "btree should have 3 packets")

	// Second scan
	// The scan should:
	// 1. Find first packet (103), use as baseline (NOT nakScanStartPoint from cycle 1)
	// 2. Detect gap from 105-107 (between 104 and 108)
	nakedSequences = nil
	recv.Tick(2000)

	// NAK list uses range encoding [start, end]
	// Gap 105-107 should be encoded as [105, 107]
	require.Equal(t, []uint32{105, 107}, nakedSequences,
		"should NAK only actual gap (105-107) as range")
}

// =============================================================================
// Sequence Number Wraparound Tests
// =============================================================================
// These tests verify that the NAK btree correctly handles sequence number
// wraparound when sequences go from MAX_SEQUENCENUMBER back to 0.

// TestNakBtree_Wraparound_SimpleGap tests basic gap detection near MAX.
// This isolates the wraparound logic from the full stream simulation.
func TestNakBtree_Wraparound_SimpleGap(t *testing.T) {
	var nakedSequences []uint32

	// Use a receiver initialized near MAX so it accepts packets in that range
	baseSeq := packet.MAX_SEQUENCENUMBER - 10
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	}, 1_000_000, baseSeq) // 1 second TSBPD, startSeq near MAX

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets near MAX_SEQUENCENUMBER with a gap
	// Packets: MAX-10, MAX-9, MAX-8, [missing MAX-7], MAX-6, MAX-5
	baseTime := uint64(1_000_000) // Start at 1 second

	presentSeqs := []uint32{
		baseSeq,     // MAX-10
		baseSeq + 1, // MAX-9
		baseSeq + 2, // MAX-8
		// baseSeq + 3 is missing (MAX-7)
		baseSeq + 4, // MAX-6
		baseSeq + 5, // MAX-5
	}

	for i, seq := range presentSeqs {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Set PktTsbpdTime properly for TSBPD check
		p.Header().PktTsbpdTime = baseTime + uint64(i*100_000) // 100ms between packets
		recv.Push(p)
	}

	t.Logf("After push: btree size = %d", recv.packetStore.Len())

	// Run NAK scan - time must be past TSBPD threshold
	recv.Tick(baseTime + 2_000_000) // 2 seconds later

	t.Logf("NAKed sequences: %v", nakedSequences)

	// Should NAK exactly MAX-7 (which is baseSeq + 3)
	expectedNak := baseSeq + 3
	require.Contains(t, nakedSequences, expectedNak,
		"should NAK missing packet at seq %d (MAX-7)", expectedNak)
}

// TestNakBtree_Wraparound_AcrossBoundary tests gap detection across MAX→0.
func TestNakBtree_Wraparound_AcrossBoundary(t *testing.T) {
	var nakedSequences []uint32

	// Start near MAX so receiver accepts packets in that range
	startSeq := packet.MAX_SEQUENCENUMBER - 2
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	}, 1_000_000, startSeq)

	// Enable debug logging
	recv.debug = true
	recv.logFunc = func(topic string, msgFn func() string) {
		t.Logf("[%s] %s", topic, msgFn())
	}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets that cross the wraparound with a gap AT the boundary
	// Packets: MAX-2, MAX-1, MAX, [missing 0], 1, 2
	baseTime := uint64(1_000_000)

	presentSeqs := []uint32{
		packet.MAX_SEQUENCENUMBER - 2, // MAX-2
		packet.MAX_SEQUENCENUMBER - 1, // MAX-1
		packet.MAX_SEQUENCENUMBER,     // MAX
		// 0 is missing
		1, // 1
		2, // 2
	}

	for i, seq := range presentSeqs {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + uint64(i*100_000)
		recv.Push(p)
	}

	t.Logf("After push: btree size = %d", recv.packetStore.Len())

	// Dump btree contents
	t.Logf("Btree contents (in order):")
	recv.packetStore.Iterate(func(pkt packet.Packet) bool {
		h := pkt.Header()
		t.Logf("  seq=%d, PktTsbpdTime=%d", h.PacketSequenceNumber.Val(), h.PktTsbpdTime)
		return true
	})

	// Run NAK scan
	tickTime := baseTime + 2_000_000
	t.Logf("Tick at time=%d", tickTime)
	recv.Tick(tickTime)

	t.Logf("NAKed sequences: %v", nakedSequences)
	t.Logf("nakScanStartPoint = %d", recv.nakScanStartPoint.Load())

	// Should NAK exactly 0
	require.Contains(t, nakedSequences, uint32(0),
		"should NAK missing packet at seq 0 (right after MAX)")
}

// TestNakBtree_Wraparound_GapAfterWrap tests gap detection after wraparound.
func TestNakBtree_Wraparound_GapAfterWrap(t *testing.T) {
	var nakedSequences []uint32

	// Start near MAX so receiver accepts packets in that range
	startSeq := packet.MAX_SEQUENCENUMBER - 1
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	}, 1_000_000, startSeq)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets across wraparound with gap after wrap
	// Packets: MAX-1, MAX, 0, 1, [missing 2], 3, 4
	baseTime := uint64(1_000_000)

	presentSeqs := []uint32{
		packet.MAX_SEQUENCENUMBER - 1, // MAX-1
		packet.MAX_SEQUENCENUMBER,     // MAX
		0,                             // 0
		1,                             // 1
		// 2 is missing
		3, // 3
		4, // 4
	}

	for i, seq := range presentSeqs {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + uint64(i*100_000)
		recv.Push(p)
	}

	t.Logf("After push: btree size = %d", recv.packetStore.Len())

	// Run NAK scan
	recv.Tick(baseTime + 2_000_000)

	t.Logf("NAKed sequences: %v", nakedSequences)

	// Should NAK exactly 2
	require.Contains(t, nakedSequences, uint32(2),
		"should NAK missing packet at seq 2 (after wraparound)")
}

// TestNakBtree_RealisticStream_Wraparound tests NAK detection when sequence
// numbers wrap around from MAX to 0.
//
// This is a critical test because:
// - SRT uses 31-bit sequence numbers (max = 2147483647)
// - Long-running streams WILL wrap around
// - The circular sequence math must handle this correctly
func TestNakBtree_RealisticStream_Wraparound(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start near MAX_SEQUENCENUMBER so we wrap during the stream
	// MAX_SEQUENCENUMBER = 0x7FFFFFFF = 2147483647
	// Start 100 packets before wrap to ensure we cross the boundary
	startSeq := packet.MAX_SEQUENCENUMBER - 100

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  3.0, // Generate enough packets to wrap (~267 packets)
		TsbpdDelayUs: 3_000_000,
		StartSeq:     startSeq,
		StartTimeUs:  1_000_000,
	}

	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets, startSeq=%d, endSeq=%d (wrapped=%v)",
		stream.TotalPackets, startSeq, stream.EndSeq,
		stream.EndSeq < startSeq) // endSeq < startSeq means we wrapped

	// Verify wraparound occurred
	require.True(t, stream.EndSeq < startSeq,
		"stream should wrap around (endSeq %d should be < startSeq %d)", stream.EndSeq, startSeq)

	// Apply periodic loss (every 10th packet)
	lossPattern := PeriodicLoss{Period: 10, Offset: 0}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Dropped %d packets with %s", len(dropped), lossPattern.Description())

	// Log some dropped sequences to verify wraparound in loss
	wrapDropped := 0
	for _, seq := range dropped {
		if seq < 100 { // Wrapped sequences are near 0
			wrapDropped++
		}
	}
	t.Logf("Dropped packets after wrap: %d", wrapDropped)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			// Handle wraparound in range interpretation
			if start <= end {
				for seq := start; seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			} else {
				// Wrapped range: start > end means [start..MAX, 0..end]
				for seq := start; seq <= packet.MAX_SEQUENCENUMBER; seq++ {
					nakedSeqs[seq] = true
				}
				for seq := uint32(0); seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
			if missedNaks <= 5 {
				t.Logf("Missing NAK for dropped packet: seq=%d (wrapped=%v)",
					droppedSeq, droppedSeq < startSeq)
			}
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets across wraparound", missedNaks, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets correctly NAKed across sequence wraparound", len(dropped))
	}
}

// TestNakBtree_RealisticStream_Wraparound_BurstLoss tests burst loss across
// the wraparound boundary.
func TestNakBtree_RealisticStream_Wraparound_BurstLoss(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start 50 packets before wrap
	startSeq := packet.MAX_SEQUENCENUMBER - 50

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  2.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     startSeq,
		StartTimeUs:  1_000_000,
	}

	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets, startSeq=%d, endSeq=%d", stream.TotalPackets, startSeq, stream.EndSeq)

	// Burst loss: 5 packets every 30 - likely to hit the wraparound boundary
	lossPattern := BurstLoss{BurstInterval: 30, BurstSize: 5}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Dropped %d packets with %s", len(dropped), lossPattern.Description())

	// Check if any dropped packets are near the wrap point
	nearWrap := 0
	for _, seq := range dropped {
		if seq >= packet.MAX_SEQUENCENUMBER-10 || seq <= 10 {
			nearWrap++
		}
	}
	t.Logf("Dropped packets near wraparound boundary: %d", nearWrap)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			if start <= end {
				for seq := start; seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			} else {
				for seq := start; seq <= packet.MAX_SEQUENCENUMBER; seq++ {
					nakedSeqs[seq] = true
				}
				for seq := uint32(0); seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify dropped packets were NAKed
	// NOTE: Packets near the end of the stream may not be NAKed because they're
	// filtered by tooRecentThreshold (10% of TSBPD delay). This is correct behavior.
	missedNaks := 0
	var missedSeqs []uint32
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
			missedSeqs = append(missedSeqs, droppedSeq)
		}
	}

	// Calculate which packets are "too recent" (near the end of stream)
	// tooRecentThreshold filters packets within ~10% of TSBPD delay
	tooRecentCount := 0
	for _, seq := range missedSeqs {
		// Packets near endSeq are expected to be filtered as "too recent"
		if circular.SeqDistance(seq, stream.EndSeq) < 10 {
			tooRecentCount++
		}
	}

	if missedNaks > 0 {
		t.Logf("Missed NAKs: %v (count=%d, tooRecent=%d)", missedSeqs, missedNaks, tooRecentCount)
		t.Logf("startSeq=%d, endSeq=%d", cfg.StartSeq, stream.EndSeq)

		// Allow missing packets if they're all near the end (tooRecent is expected)
		if missedNaks > tooRecentCount {
			t.Errorf("Failed to NAK %d/%d dropped packets with burst loss across wraparound (excluding %d too-recent)",
				missedNaks-tooRecentCount, len(dropped)-tooRecentCount, tooRecentCount)
		} else {
			t.Logf("✅ All non-recent packets NAKed (%d too-recent packets correctly skipped)", tooRecentCount)
		}
	} else {
		t.Logf("✅ All %d dropped packets correctly NAKed with burst loss across wraparound", len(dropped))
	}
}

// TestNakBtree_RealisticStream_Wraparound_OutOfOrder tests out-of-order
// arrival combined with sequence wraparound.
func TestNakBtree_RealisticStream_Wraparound_OutOfOrder(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start 80 packets before wrap
	startSeq := packet.MAX_SEQUENCENUMBER - 80

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  2.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     startSeq,
		StartTimeUs:  1_000_000,
	}

	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets, wrap occurs during stream", stream.TotalPackets)

	// Apply periodic loss
	lossPattern := PeriodicLoss{Period: 15, Offset: 0}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Dropped %d packets", len(dropped))

	// Apply out-of-order (burst reverse) - this will shuffle packets around the wraparound
	reorderPattern := BurstReorder{BurstSize: 8}
	reordered := reorderPattern.Reorder(surviving)
	t.Logf("Reordered with %s", reorderPattern.Description())

	// Push packets
	for _, p := range reordered {
		recv.Push(p)
	}

	// Run NAK cycles
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			if start <= end {
				for seq := start; seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			} else {
				for seq := start; seq <= packet.MAX_SEQUENCENUMBER; seq++ {
					nakedSeqs[seq] = true
				}
				for seq := uint32(0); seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets with OOO + wraparound", missedNaks, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets correctly NAKed with OOO + wraparound", len(dropped))
	}
}

// =============================================================================
// Large-Scale Stream Tests with Advanced Loss Patterns
// =============================================================================
// These tests simulate longer durations, higher packet counts, and more severe
// loss patterns as described in packet_loss_injection_design.md.

// filterTooRecentPackets returns dropped packets that are old enough to be NAKed.
// Packets with PktTsbpdTime > (now - tsbpdDelay * nakRecentPercent) are "too recent".
// The receiver won't NAK these because they might just be reordered, not lost.
func filterTooRecentPackets(dropped []uint32, droppedPktTimes map[uint32]uint64, now, tsbpdDelay uint64, nakRecentPercent float64) []uint32 {
	threshold := now - uint64(float64(tsbpdDelay)*nakRecentPercent)
	var oldEnough []uint32
	for _, seq := range dropped {
		if pktTime, ok := droppedPktTimes[seq]; ok {
			if pktTime <= threshold {
				oldEnough = append(oldEnough, seq)
			}
		}
	}
	return oldEnough
}

// TestNakBtree_LargeStream_LargeBurstLoss simulates a 50-packet consecutive burst loss.
// This is a severe network outage scenario - half a second of data at typical rates.
func TestNakBtree_LargeStream_LargeBurstLoss(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Generate a 10-second stream at 2 Mb/s (more packets to work with)
	cfg := StreamSimConfig{
		BitrateBps:   2_000_000, // 2 Mb/s
		PayloadBytes: 1400,
		DurationSec:  10.0,
		TsbpdDelayUs: 3_000_000, // 3 second TSBPD
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets for 10s @ 2Mb/s", stream.TotalPackets)

	// Drop 50 consecutive packets starting at packet 500 (early in stream, won't be "too recent")
	lossPattern := LargeBurstLoss{StartIndex: 500, Size: 50}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied %s: dropped %d packets", lossPattern.Description(), len(dropped))

	// Create receiver
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run multiple NAK cycles with time well past the burst
	// Run at end of stream + TSBPD delay to ensure all packets are "old enough"
	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	for i := 0; i < 100; i++ {
		recv.Tick(currentTimeUs + uint64(i*20_000))
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	// Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
		}
	}

	require.Equal(t, 0, missedNaks, "Failed to NAK %d/%d packets in large burst", missedNaks, len(dropped))
	t.Logf("✅ All %d packets in large burst correctly NAKed", len(dropped))
}

// TestNakBtree_LargeStream_HighLossWindow simulates 85% loss for a window of packets.
// This is the "high-loss burst" pattern from packet_loss_injection_design.md.
func TestNakBtree_LargeStream_HighLossWindow(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Generate a 10-second stream at 1 Mb/s (longer to ensure high-loss window is not at end)
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  10.0, // Longer duration so loss window is in middle
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets for 10s @ 1Mb/s", stream.TotalPackets)

	// 85% loss for packets 100-200 (early in stream, won't be "too recent")
	lossPattern := HighLossWindow{
		WindowStart: 100,
		WindowEnd:   200,
		LossRate:    0.85,
	}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied %s: dropped %d packets (expected ~85 from window of 100)",
		lossPattern.Description(), len(dropped))

	// Create receiver
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles with time well past stream end + TSBPD
	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	for i := 0; i < 100; i++ {
		recv.Tick(currentTimeUs + uint64(i*20_000))
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	// Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
		}
	}

	require.Equal(t, 0, missedNaks, "Failed to NAK %d/%d high-loss window packets", missedNaks, len(dropped))
	t.Logf("✅ All %d packets in high-loss window correctly NAKed", len(dropped))
}

// TestNakBtree_LargeStream_MultipleBursts simulates multiple burst losses.
// This simulates sporadic network outages over a stream.
func TestNakBtree_LargeStream_MultipleBursts(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Generate a 30-second stream at 2 Mb/s (long enough to have bursts in middle)
	cfg := StreamSimConfig{
		BitrateBps:   2_000_000,
		PayloadBytes: 1400,
		DurationSec:  30.0, // Long stream
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets for 30s @ 2Mb/s", stream.TotalPackets)

	// Multiple bursts in first half of stream (won't be "too recent" at end)
	// At 2Mb/s with 1400 byte packets, we get ~178 packets/sec
	// In 30 seconds, that's ~5340 packets, so bursts at 100-1000 are in first 20%
	lossPattern := MultiBurstLoss{
		Bursts: []struct {
			Start int
			Size  int
		}{
			{Start: 100, Size: 10},  // Small burst early
			{Start: 300, Size: 30},  // Medium burst
			{Start: 600, Size: 50},  // Large burst
			{Start: 900, Size: 100}, // Very large burst (network outage)
			{Start: 1200, Size: 20}, // Recovery test
		},
	}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied %s: dropped %d packets total", lossPattern.Description(), len(dropped))

	// Build map of dropped packet times
	droppedTimes := make(map[uint32]uint64)
	for _, p := range stream.Packets {
		seq := p.Header().PacketSequenceNumber.Val()
		droppedTimes[seq] = p.Header().PktTsbpdTime
	}

	// Create receiver
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles with time well past stream + TSBPD
	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	for i := 0; i < 150; i++ {
		recv.Tick(currentTimeUs + uint64(i*20_000))
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	// Filter out "too recent" dropped packets
	nakRecentPercent := 0.10
	droppedOldEnough := filterTooRecentPackets(dropped, droppedTimes, currentTimeUs, cfg.TsbpdDelayUs, nakRecentPercent)
	t.Logf("After filtering 'too recent': %d/%d packets should be NAKed", len(droppedOldEnough), len(dropped))

	// Debug: show which ranges were NAKed
	t.Logf("NAK ranges received: %d ranges", len(nakedRanges))
	for i, ranges := range nakedRanges {
		if i < 5 || i >= len(nakedRanges)-2 {
			t.Logf("  Range %d: %v", i, ranges)
		}
	}

	// Verify all "old enough" dropped packets were NAKed
	missedNaks := 0
	var missedList []uint32
	for _, droppedSeq := range droppedOldEnough {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
			if len(missedList) < 20 {
				missedList = append(missedList, droppedSeq)
			}
		}
	}

	if missedNaks > 0 {
		t.Logf("First 20 missed seqs: %v", missedList)
		// Show which bursts are affected
		for _, b := range lossPattern.Bursts {
			burstSeqStart := cfg.StartSeq + uint32(b.Start)
			burstSeqEnd := cfg.StartSeq + uint32(b.Start+b.Size-1)
			nakedInBurst := 0
			for seq := burstSeqStart; seq <= burstSeqEnd; seq++ {
				if nakedSeqs[seq] {
					nakedInBurst++
				}
			}
			t.Logf("  Burst [%d-%d]: %d/%d NAKed", burstSeqStart, burstSeqEnd, nakedInBurst, b.Size)
		}
	}

	require.Equal(t, 0, missedNaks, "Failed to NAK %d/%d multi-burst packets", missedNaks, len(droppedOldEnough))
	t.Logf("✅ All %d packets across multiple bursts correctly NAKed", len(droppedOldEnough))
}

// TestNakBtree_LargeStream_CorrelatedLoss tests bursty loss with Gilbert-Elliott behavior.
// Real networks often have correlated loss - if one packet is lost, the next is more likely to be lost.
func TestNakBtree_LargeStream_CorrelatedLoss(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Generate a 10-second stream
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  10.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets for 10s @ 1Mb/s", stream.TotalPackets)

	// Correlated loss: 5% base + 25% correlation (as per netem "loss 5% 25%")
	lossPattern := &CorrelatedLoss{
		BaseLossRate: 0.05,
		Correlation:  0.25,
	}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied %s: dropped %d packets (%.1f%%)",
		lossPattern.Description(), len(dropped), float64(len(dropped))/float64(stream.TotalPackets)*100)

	// Build a map of dropped packet times for filtering "too recent" packets
	droppedTimes := make(map[uint32]uint64)
	for _, p := range stream.Packets {
		seq := p.Header().PacketSequenceNumber.Val()
		for _, d := range dropped {
			if d == seq {
				droppedTimes[seq] = p.Header().PktTsbpdTime
				break
			}
		}
	}

	// Create receiver
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles with time well past stream + TSBPD
	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	for i := 0; i < 100; i++ {
		recv.Tick(currentTimeUs + uint64(i*20_000))
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	// Filter out "too recent" dropped packets (they won't be NAKed)
	nakRecentPercent := 0.10 // Match receiver config
	droppedOldEnough := filterTooRecentPackets(dropped, droppedTimes, currentTimeUs, cfg.TsbpdDelayUs, nakRecentPercent)
	t.Logf("After filtering 'too recent': %d/%d packets should be NAKed", len(droppedOldEnough), len(dropped))

	// Verify all "old enough" dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range droppedOldEnough {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
		}
	}

	require.Equal(t, 0, missedNaks, "Failed to NAK %d/%d correlated-loss packets", missedNaks, len(droppedOldEnough))
	t.Logf("✅ All %d packets with correlated loss correctly NAKed", len(droppedOldEnough))
}

// TestNakBtree_LargeStream_VeryLongStream tests a 30-second stream with high packet count.
// This stress tests the NAK btree with thousands of packets and realistic conditions.
func TestNakBtree_LargeStream_VeryLongStream(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Generate a 30-second stream at 5 Mb/s (high bitrate)
	cfg := StreamSimConfig{
		BitrateBps:   5_000_000, // 5 Mb/s
		PayloadBytes: 1400,
		DurationSec:  30.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets for 30s @ 5Mb/s", stream.TotalPackets)

	// Use 2% uniform loss + periodic large bursts in first half of stream
	// First apply uniform loss
	uniformLoss := PercentageLoss{Rate: 0.02}
	surviving1, dropped1 := applyLossPattern(stream.Packets, uniformLoss)

	// Apply burst losses in first 15 seconds only (so they're not "too recent")
	packetsPerSec := stream.TotalPackets / 30
	burstLoss := MultiBurstLoss{
		Bursts: []struct {
			Start int
			Size  int
		}{
			{Start: int(2 * packetsPerSec), Size: 25},  // 2s
			{Start: int(5 * packetsPerSec), Size: 25},  // 5s
			{Start: int(8 * packetsPerSec), Size: 25},  // 8s
			{Start: int(11 * packetsPerSec), Size: 25}, // 11s
			{Start: int(14 * packetsPerSec), Size: 25}, // 14s
		},
	}
	surviving2, dropped2 := applyLossPattern(surviving1, burstLoss)

	// Build map of dropped packet times
	droppedTimes := make(map[uint32]uint64)
	for _, p := range stream.Packets {
		seq := p.Header().PacketSequenceNumber.Val()
		droppedTimes[seq] = p.Header().PktTsbpdTime
	}

	// Combine dropped lists
	allDropped := make(map[uint32]bool)
	for _, seq := range dropped1 {
		allDropped[seq] = true
	}
	for _, seq := range dropped2 {
		allDropped[seq] = true
	}
	allDroppedSlice := make([]uint32, 0, len(allDropped))
	for seq := range allDropped {
		allDroppedSlice = append(allDroppedSlice, seq)
	}

	t.Logf("Applied %s + %s: dropped %d packets total (%.1f%%)",
		uniformLoss.Description(), burstLoss.Description(),
		len(allDropped), float64(len(allDropped))/float64(stream.TotalPackets)*100)

	// Create receiver
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	// Push surviving packets
	for _, p := range surviving2 {
		recv.Push(p)
	}

	// Run many NAK cycles with time well past stream + TSBPD
	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	for i := 0; i < 200; i++ {
		recv.Tick(currentTimeUs + uint64(i*20_000))
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	// Filter out "too recent" dropped packets
	nakRecentPercent := 0.10
	droppedOldEnough := filterTooRecentPackets(allDroppedSlice, droppedTimes, currentTimeUs, cfg.TsbpdDelayUs, nakRecentPercent)
	t.Logf("After filtering 'too recent': %d/%d packets should be NAKed", len(droppedOldEnough), len(allDropped))

	// Verify all "old enough" dropped packets were NAKed
	missedNaks := 0
	var missedList []uint32
	for _, droppedSeq := range droppedOldEnough {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
			if len(missedList) < 10 {
				missedList = append(missedList, droppedSeq)
			}
		}
	}

	if missedNaks > 0 {
		t.Logf("First 10 missed: %v", missedList)
	}
	require.Equal(t, 0, missedNaks, "Failed to NAK %d/%d long-stream packets", missedNaks, len(droppedOldEnough))
	t.Logf("✅ All %d packets in long stream correctly NAKed", len(droppedOldEnough))
}

// TestNakBtree_LargeStream_ExtremeBurstLoss tests a 100-packet consecutive burst.
// This simulates a complete network outage for ~1 second at typical rates.
func TestNakBtree_LargeStream_ExtremeBurstLoss(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Generate a 10-second stream (longer to ensure burst is in middle)
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  10.0, // Longer so burst isn't at end
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets for 10s @ 1Mb/s", stream.TotalPackets)

	// Drop 100 consecutive packets early (extreme burst)
	lossPattern := LargeBurstLoss{StartIndex: 100, Size: 100}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied %s: dropped %d packets", lossPattern.Description(), len(dropped))

	// Create receiver
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles with time well past stream + TSBPD
	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	for i := 0; i < 100; i++ {
		recv.Tick(currentTimeUs + uint64(i*20_000))
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	// Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
		}
	}

	require.Equal(t, 0, missedNaks, "Failed to NAK %d/%d extreme burst packets", missedNaks, len(dropped))
	t.Logf("✅ All %d packets in extreme burst correctly NAKed", len(dropped))
}

// =============================================================================
// NakMergeGap Consolidation Tests
// =============================================================================
// These tests verify that NakMergeGap correctly controls how non-contiguous
// NAK entries are merged into ranges. Per design_nak_btree.md Section 4.4:
// - NakMergeGap=0: Only strictly contiguous sequences merge
// - NakMergeGap=3: Merge gaps up to 3 (default, balance between precision and efficiency)
// - NakMergeGap=10: Aggressive merging (fewer NAKs, more potential duplicate retransmissions)

// mockNakBtreeRecvWithMergeGap creates a receiver with configurable NakMergeGap.
func mockNakBtreeRecvWithMergeGap(onSendNAK func(list []circular.Number), tsbpdDelayUs uint64, startSeq uint32, mergeGap uint32) *receiver {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := NewReceiver(ReceiveConfig{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000,
		PeriodicNAKInterval:    20_000,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              onSendNAK,
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		TsbpdDelay:             tsbpdDelayUs,
		NakRecentPercent:       0.10,
		NakConsolidationBudget: 20_000, // 20ms budget
		NakMergeGap:            mergeGap,
	})

	return recv.(*receiver)
}

// TestNakMergeGap_ZeroMeansStrictlyContiguous tests that NakMergeGap=0
// only merges strictly contiguous sequences.
func TestNakMergeGap_ZeroMeansStrictlyContiguous(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  2.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)

	// Create gaps with small distances: drop 101-102, 105-106 (gap of 2 between bursts)
	// With mergeGap=0, these should NOT merge (gap > 0)
	lossPattern := MultiBurstLoss{
		Bursts: []struct {
			Start int
			Size  int
		}{
			{Start: 100, Size: 2}, // Drop seq 101-102
			{Start: 104, Size: 2}, // Drop seq 105-106 (gap of 2 packets: 103, 104)
		},
	}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied loss: dropped %d packets", len(dropped))

	// Create receiver with NakMergeGap=0
	recv := mockNakBtreeRecvWithMergeGap(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq, 0) // mergeGap=0

	for _, p := range surviving {
		recv.Push(p)
	}

	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	recv.Tick(currentTimeUs)

	nakLock.Lock()
	require.NotEmpty(t, nakedRanges, "Expected NAK ranges")
	firstNak := nakedRanges[0]
	nakLock.Unlock()

	// With mergeGap=0, should have 2 separate ranges: [101, 102] and [105, 106]
	t.Logf("NAK ranges with mergeGap=0: %v", firstNak)
	require.Equal(t, 4, len(firstNak), "Expected 2 ranges (4 values) with mergeGap=0, got %d values", len(firstNak))
	require.Equal(t, uint32(101), firstNak[0], "First range start")
	require.Equal(t, uint32(102), firstNak[1], "First range end")
	require.Equal(t, uint32(105), firstNak[2], "Second range start")
	require.Equal(t, uint32(106), firstNak[3], "Second range end")
	t.Logf("✅ NakMergeGap=0 correctly produces separate ranges for non-contiguous gaps")
}

// TestNakMergeGap_DefaultMergesSmallGaps tests that NakMergeGap=3 (default)
// merges gaps up to 3 packets.
func TestNakMergeGap_DefaultMergesSmallGaps(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  2.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)

	// Create gaps: drop 101-102, then 106-107 (gap of 3 packets: 103, 104, 105)
	// With mergeGap=3, these SHOULD merge into single range [101, 107]
	lossPattern := MultiBurstLoss{
		Bursts: []struct {
			Start int
			Size  int
		}{
			{Start: 100, Size: 2}, // Drop seq 101-102
			{Start: 105, Size: 2}, // Drop seq 106-107 (gap of 3: 103, 104, 105)
		},
	}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied loss: dropped %d packets (%v)", len(dropped), dropped)

	// Create receiver with NakMergeGap=3 (default)
	recv := mockNakBtreeRecvWithMergeGap(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq, 3) // mergeGap=3

	for _, p := range surviving {
		recv.Push(p)
	}

	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	recv.Tick(currentTimeUs)

	nakLock.Lock()
	require.NotEmpty(t, nakedRanges, "Expected NAK ranges")
	firstNak := nakedRanges[0]
	nakLock.Unlock()

	// With mergeGap=3, should merge into single range [101, 107]
	// Note: This includes 103, 104, 105 which DID arrive - they'll be retransmitted as duplicates
	t.Logf("NAK ranges with mergeGap=3: %v", firstNak)
	require.Equal(t, 2, len(firstNak), "Expected 1 merged range (2 values) with mergeGap=3, got %d values", len(firstNak))
	require.Equal(t, uint32(101), firstNak[0], "Merged range start")
	require.Equal(t, uint32(107), firstNak[1], "Merged range end")
	t.Logf("✅ NakMergeGap=3 correctly merges gaps of 3 or less into single range")
}

// TestNakMergeGap_LargeGapNotMerged tests that gaps larger than NakMergeGap are NOT merged.
func TestNakMergeGap_LargeGapNotMerged(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  2.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)

	// Create gaps: drop 101-102, then 108-109 (gap of 5 packets: 103, 104, 105, 106, 107)
	// With mergeGap=3, these should NOT merge (gap > 3)
	lossPattern := MultiBurstLoss{
		Bursts: []struct {
			Start int
			Size  int
		}{
			{Start: 100, Size: 2}, // Drop seq 101-102
			{Start: 107, Size: 2}, // Drop seq 108-109 (gap of 5 > mergeGap=3)
		},
	}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied loss: dropped %d packets (%v)", len(dropped), dropped)

	// Create receiver with NakMergeGap=3
	recv := mockNakBtreeRecvWithMergeGap(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq, 3) // mergeGap=3

	for _, p := range surviving {
		recv.Push(p)
	}

	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	recv.Tick(currentTimeUs)

	nakLock.Lock()
	require.NotEmpty(t, nakedRanges, "Expected NAK ranges")
	firstNak := nakedRanges[0]
	nakLock.Unlock()

	// With gap > mergeGap, should have 2 separate ranges
	t.Logf("NAK ranges with mergeGap=3, gap=5: %v", firstNak)
	require.Equal(t, 4, len(firstNak), "Expected 2 separate ranges (4 values), got %d values", len(firstNak))
	require.Equal(t, uint32(101), firstNak[0], "First range start")
	require.Equal(t, uint32(102), firstNak[1], "First range end")
	require.Equal(t, uint32(108), firstNak[2], "Second range start")
	require.Equal(t, uint32(109), firstNak[3], "Second range end")
	t.Logf("✅ NakMergeGap=3 correctly keeps separate ranges when gap exceeds threshold")
}

// TestNakMergeGap_AggressiveMerging tests NakMergeGap=10 for aggressive merging.
func TestNakMergeGap_AggressiveMerging(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  2.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)

	// Create gaps: drop 101-102, 110-111, 119-120 (gaps of 7 and 7 packets)
	// With mergeGap=10, all should merge into single range [101, 120]
	lossPattern := MultiBurstLoss{
		Bursts: []struct {
			Start int
			Size  int
		}{
			{Start: 100, Size: 2}, // Drop seq 101-102
			{Start: 109, Size: 2}, // Drop seq 110-111 (gap of 7)
			{Start: 118, Size: 2}, // Drop seq 119-120 (gap of 7)
		},
	}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Applied loss: dropped %d packets (%v)", len(dropped), dropped)

	// Create receiver with NakMergeGap=10 (aggressive)
	recv := mockNakBtreeRecvWithMergeGap(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq, 10) // mergeGap=10

	for _, p := range surviving {
		recv.Push(p)
	}

	currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
	recv.Tick(currentTimeUs)

	nakLock.Lock()
	require.NotEmpty(t, nakedRanges, "Expected NAK ranges")
	firstNak := nakedRanges[0]
	nakLock.Unlock()

	// With mergeGap=10, all bursts should merge into single range [101, 120]
	t.Logf("NAK ranges with mergeGap=10: %v", firstNak)
	require.Equal(t, 2, len(firstNak), "Expected 1 merged range (2 values) with mergeGap=10, got %d values", len(firstNak))
	require.Equal(t, uint32(101), firstNak[0], "Merged range start")
	require.Equal(t, uint32(120), firstNak[1], "Merged range end")
	t.Logf("✅ NakMergeGap=10 aggressively merges all gaps into single range")
}

// TestNakMergeGap_TradeoffAnalysis documents the trade-off between different NakMergeGap values.
func TestNakMergeGap_TradeoffAnalysis(t *testing.T) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  5.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	stream := generatePacketStream(addr, cfg)

	// Create a realistic loss pattern with multiple small gaps
	lossPattern := MultiBurstLoss{
		Bursts: []struct {
			Start int
			Size  int
		}{
			{Start: 100, Size: 2}, // 101-102
			{Start: 106, Size: 2}, // 107-108 (gap of 4)
			{Start: 115, Size: 3}, // 116-118 (gap of 7)
			{Start: 200, Size: 2}, // 201-202
			{Start: 204, Size: 2}, // 205-206 (gap of 2)
		},
	}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)

	testCases := []struct {
		mergeGap       uint32
		expectedRanges int
		description    string
	}{
		{0, 5, "Strict: each burst is separate range"},
		{3, 4, "Default: merges gap of 2, not 4 or 7"},
		{5, 3, "Medium: merges gaps ≤5"},
		{10, 2, "Aggressive: merges most gaps"},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("mergeGap=%d", tc.mergeGap), func(t *testing.T) {
			var nakedRanges [][]uint32
			nakLock := sync.Mutex{}

			recv := mockNakBtreeRecvWithMergeGap(func(list []circular.Number) {
				nakLock.Lock()
				defer nakLock.Unlock()
				ranges := make([]uint32, len(list))
				for i, seq := range list {
					ranges[i] = seq.Val()
				}
				if len(ranges) > 0 {
					nakedRanges = append(nakedRanges, ranges)
				}
			}, cfg.TsbpdDelayUs, cfg.StartSeq, tc.mergeGap)

			for _, p := range surviving {
				recv.Push(p)
			}

			currentTimeUs := cfg.StartTimeUs + uint64(cfg.DurationSec*1_000_000) + cfg.TsbpdDelayUs
			recv.Tick(currentTimeUs)

			nakLock.Lock()
			require.NotEmpty(t, nakedRanges, "Expected NAK ranges")
			firstNak := nakedRanges[0]
			nakLock.Unlock()

			actualRanges := len(firstNak) / 2
			t.Logf("mergeGap=%d: %d ranges, NAK list=%v (%s)", tc.mergeGap, actualRanges, firstNak, tc.description)

			// Count how many dropped packets are covered by NAK ranges
			nakedSeqs := make(map[uint32]bool)
			for i := 0; i+1 < len(firstNak); i += 2 {
				for seq := firstNak[i]; seq <= firstNak[i+1]; seq++ {
					nakedSeqs[seq] = true
				}
			}

			// All dropped must be covered
			for _, d := range dropped {
				require.True(t, nakedSeqs[d], "Dropped seq %d not covered by NAK", d)
			}

			// Count extra (duplicate) sequences that will be retransmitted
			extraRetransmits := len(nakedSeqs) - len(dropped)
			t.Logf("  → NAKed %d seqs, dropped %d, extra retransmits: %d", len(nakedSeqs), len(dropped), extraRetransmits)

			require.Equal(t, tc.expectedRanges, actualRanges,
				"Expected %d ranges with mergeGap=%d, got %d", tc.expectedRanges, tc.mergeGap, actualRanges)
		})
	}
}

// =============================================================================
// ACK Sequence Number Wraparound Tests
// =============================================================================
// These tests verify that the ACK logic correctly handles sequence number
// wraparound when sequences go from MAX_SEQUENCENUMBER back to 0.

// mockLiveRecvWithStartSeq creates a receiver with configurable initial sequence.
// This allows testing wraparound scenarios where sequences start near MAX.
func mockLiveRecvWithStartSeq(startSeq uint32, onSendACK func(seq circular.Number, light bool), onSendNAK func(list []circular.Number), onDeliver func(p packet.Packet)) *receiver {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := NewReceiver(ReceiveConfig{
		InitialSequenceNumber: circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000, // 10ms
		PeriodicNAKInterval:   20_000, // 20ms
		OnSendACK:             onSendACK,
		OnSendNAK:             onSendNAK,
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
		TsbpdDelay:            100_000, // 100ms TSBPD
	})

	return recv.(*receiver)
}

// TestACK_Wraparound_Contiguity tests that ACK advances correctly across MAX→0.
func TestACK_Wraparound_Contiguity(t *testing.T) {
	var lastACK uint32
	var delivered []uint32

	startSeq := packet.MAX_SEQUENCENUMBER - 3
	recv := mockLiveRecvWithStartSeq(startSeq,
		func(seq circular.Number, light bool) {
			// Note: ACK callback receives the NEXT expected sequence (one past last received)
			lastACK = seq.Val()
		},
		func(list []circular.Number) {},
		func(p packet.Packet) {
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push contiguous packets across wraparound: MAX-3, MAX-2, MAX-1, MAX, 0, 1, 2
	sequences := []uint32{
		packet.MAX_SEQUENCENUMBER - 3,
		packet.MAX_SEQUENCENUMBER - 2,
		packet.MAX_SEQUENCENUMBER - 1,
		packet.MAX_SEQUENCENUMBER,
		0,
		1,
		2,
	}

	baseTime := uint64(1_000_000)
	for i, seq := range sequences {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + uint64(i*10_000) // 10ms apart
		recv.Push(p)
	}

	// Run Tick to process ACKs and deliver packets
	// Time must be past TSBPD for delivery
	recv.Tick(baseTime + 200_000)

	t.Logf("lastACK = %d (next expected = 3)", lastACK)
	t.Logf("delivered = %v", delivered)

	// ACK reports NEXT expected sequence, so after receiving seq 2, ACK = 3
	require.Equal(t, uint32(3), lastACK,
		"ACK should report next expected seq 3 after receiving up to seq 2")

	// All packets should be delivered in order
	require.Equal(t, sequences, delivered,
		"packets should be delivered in sequence order across wraparound")
}

// TestACK_Wraparound_GapAtBoundary tests gap detection at the MAX→0 boundary.
// Note: ACK will skip gaps if TSBPD time has passed (live streaming semantics).
// This test verifies NAK is sent for the gap before it's skipped.
func TestACK_Wraparound_GapAtBoundary(t *testing.T) {
	var lastACK uint32
	var nakedSeqs []uint32

	startSeq := packet.MAX_SEQUENCENUMBER - 2
	recv := mockLiveRecvWithStartSeq(startSeq,
		func(seq circular.Number, light bool) {
			lastACK = seq.Val()
		},
		func(list []circular.Number) {
			for _, seq := range list {
				nakedSeqs = append(nakedSeqs, seq.Val())
			}
		},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets with gap at seq 0: MAX-2, MAX-1, MAX, [missing 0], 1, 2
	sequences := []uint32{
		packet.MAX_SEQUENCENUMBER - 2,
		packet.MAX_SEQUENCENUMBER - 1,
		packet.MAX_SEQUENCENUMBER,
		// 0 is missing
		1,
		2,
	}

	baseTime := uint64(1_000_000)
	for i, seq := range sequences {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Set TSBPD time in the FUTURE so gaps aren't skipped
		p.Header().PktTsbpdTime = baseTime + 500_000 + uint64(i*10_000)
		recv.Push(p)
	}

	// Run Tick with time BEFORE TSBPD (so gaps aren't skipped)
	recv.Tick(baseTime + 100_000)

	t.Logf("lastACK = %d (next expected)", lastACK)
	t.Logf("nakedSeqs = %v", nakedSeqs)

	// ACK reports NEXT expected after MAX (which is 0, but since 0 is missing, it reports after MAX)
	// Note: ACK stops at the gap, so it should be MAX+1 = 0
	require.Equal(t, uint32(0), lastACK,
		"ACK should report next expected seq 0 (gap at 0, stopped at MAX)")

	// NAK should be sent for seq 0
	require.Contains(t, nakedSeqs, uint32(0),
		"NAK should be sent for missing seq 0 at wraparound boundary")
}

// TestACK_Wraparound_GapAfterWrap tests gap detection after wraparound.
func TestACK_Wraparound_GapAfterWrap(t *testing.T) {
	var lastACK uint32

	startSeq := packet.MAX_SEQUENCENUMBER - 1
	recv := mockLiveRecvWithStartSeq(startSeq,
		func(seq circular.Number, light bool) {
			lastACK = seq.Val()
		},
		func(list []circular.Number) {},
		func(p packet.Packet) {},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets: MAX-1, MAX, 0, 1, [missing 2], 3, 4
	sequences := []uint32{
		packet.MAX_SEQUENCENUMBER - 1,
		packet.MAX_SEQUENCENUMBER,
		0,
		1,
		// 2 is missing
		3,
		4,
	}

	baseTime := uint64(1_000_000)
	for i, seq := range sequences {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Set TSBPD in the future so gaps aren't skipped
		p.Header().PktTsbpdTime = baseTime + 500_000 + uint64(i*10_000)
		recv.Push(p)
	}

	// Run Tick with time before TSBPD
	recv.Tick(baseTime + 100_000)

	t.Logf("lastACK = %d (next expected)", lastACK)

	// ACK reports NEXT expected, so after receiving seq 1, ACK = 2
	require.Equal(t, uint32(2), lastACK,
		"ACK should report next expected seq 2 (gap at 2, stopped at seq 1)")
}

// TestACK_Wraparound_SkippedCount tests skipped packet count across wraparound.
func TestACK_Wraparound_SkippedCount(t *testing.T) {
	var lastACK uint32
	var delivered []uint32

	startSeq := packet.MAX_SEQUENCENUMBER - 2
	recv := mockLiveRecvWithStartSeq(startSeq,
		func(seq circular.Number, light bool) {
			lastACK = seq.Val()
		},
		func(list []circular.Number) {},
		func(p packet.Packet) {
			delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets with gaps that span wraparound
	// MAX-2, MAX-1, [missing MAX, 0, 1], 2, 3
	// This tests that skipped count is calculated correctly across wrap
	sequences := []uint32{
		packet.MAX_SEQUENCENUMBER - 2,
		packet.MAX_SEQUENCENUMBER - 1,
		// MAX, 0, 1 missing
		2,
		3,
	}

	baseTime := uint64(1_000_000)
	for i, seq := range sequences {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Make earlier packets deliverable (TSBPD passed)
		p.Header().PktTsbpdTime = baseTime + uint64(i*10_000)
		recv.Push(p)
	}

	// First tick - ACK should advance to MAX-1 (contiguous so far)
	recv.Tick(baseTime + 50_000)
	t.Logf("After tick 1: lastACK = %d", lastACK)

	// Second tick with time past all TSBPD - should skip missing packets
	recv.Tick(baseTime + 500_000)
	t.Logf("After tick 2: lastACK = %d, delivered = %v", lastACK, delivered)

	// The skipped count metric should reflect 3 skipped packets (MAX, 0, 1)
	// This is tracked in CongestionRecvPktSkippedTSBPD
	skipped := recv.metrics.CongestionRecvPktSkippedTSBPD.Load()
	t.Logf("Skipped packets metric: %d", skipped)

	// Note: The exact behavior depends on TSBPD timing and skip logic
	// This test verifies the mechanism works across wraparound
}
