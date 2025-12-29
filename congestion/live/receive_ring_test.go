package live

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// mockRingRecv creates a receiver with ring buffer enabled for testing
func mockRingRecv(onSendACK func(seq circular.Number, light bool), onSendNAK func(list []circular.Number), onDeliver func(p packet.Packet)) *receiver {
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
		// Ring buffer configuration
		UsePacketRing:             true,
		PacketRingSize:            256, // Small for testing
		PacketRingShards:          4,
		PacketRingMaxRetries:      5,
		PacketRingBackoffDuration: 10 * time.Microsecond,
		PacketRingMaxBackoffs:     3,
	})

	return recv.(*receiver)
}

// TestRingEnabled verifies that ring buffer is properly initialized
func TestRingEnabled(t *testing.T) {
	recv := mockRingRecv(nil, nil, nil)

	require.True(t, recv.usePacketRing, "usePacketRing should be true")
	require.NotNil(t, recv.packetRing, "packetRing should be initialized")
	require.NotNil(t, recv.pushFn, "pushFn should be set")
}

// TestRingDisabled verifies legacy path when ring is disabled
func TestRingDisabled(t *testing.T) {
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
		ConnectionMetrics:     testMetrics,
		UsePacketRing:         false, // Disabled
	})

	r := recv.(*receiver)
	require.False(t, r.usePacketRing, "usePacketRing should be false")
	require.Nil(t, r.packetRing, "packetRing should be nil")
	require.NotNil(t, r.pushFn, "pushFn should still be set (to pushWithLock)")
}

// TestPushToRing verifies packets are written to ring buffer
func TestPushToRing(t *testing.T) {
	recv := mockRingRecv(nil, nil, nil)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 10 packets via ring
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(100 + i)

		recv.Push(p)
	}

	// Verify metrics were updated (rate metrics happen in pushToRing)
	require.Equal(t, uint64(10), recv.metrics.RecvLightACKCounter.Load())
	require.Equal(t, uint64(10), recv.metrics.RecvRatePackets.Load())

	// Packets should be in ring, NOT in btree yet (btree is empty until drain)
	require.Equal(t, 0, recv.packetStore.Len(), "btree should be empty before drain")

	// Verify ring has items (approximate - ring.Len() is snapshot)
	ringLen := recv.packetRing.Len()
	require.Greater(t, ringLen, uint64(0), "ring should have packets")
}

// TestDrainPacketRing verifies packets are transferred from ring to btree
func TestDrainPacketRing(t *testing.T) {
	recv := mockRingRecv(nil, nil, nil)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets to ring
	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(100 + i)

		recv.Push(p)
	}

	require.Equal(t, 0, recv.packetStore.Len(), "btree empty before drain")

	// Drain ring to btree
	recv.drainPacketRing(50) // now=50, all packets have PktTsbpdTime > now

	// Verify packets moved to btree
	require.Equal(t, 10, recv.packetStore.Len(), "btree should have all packets after drain")

	// Verify ring is empty
	require.Equal(t, uint64(0), recv.packetRing.Len(), "ring should be empty after drain")

	// Verify metrics
	require.Equal(t, uint64(10), recv.metrics.RingDrainedPackets.Load())
}

// TestRingFullPath verifies the complete Push -> Tick -> Deliver flow
func TestRingFullPath(t *testing.T) {
	deliveredSeqs := []uint32{}
	ackSeq := uint32(0)

	recv := mockRingRecv(
		func(seq circular.Number, light bool) {
			ackSeq = seq.Val()
		},
		nil,
		func(p packet.Packet) {
			deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push 5 packets via ring
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1) // TSBPD time 1-5

		recv.Push(p)
	}

	// Tick will: drain ring -> process ACK/NAK -> deliver
	recv.Tick(10) // ACK period, all packets delivered (TSBPD time <= 10)

	require.Equal(t, uint32(5), ackSeq, "ACK should be at seq 5")
	require.Equal(t, []uint32{0, 1, 2, 3, 4}, deliveredSeqs, "all packets should be delivered")
}

// TestRingDuplicateHandling verifies duplicates are dropped during drain
func TestRingDuplicateHandling(t *testing.T) {
	recv := mockRingRecv(nil, nil, nil)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push same sequence twice
	for i := 0; i < 2; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(5, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(100)

		recv.Push(p)
	}

	// Drain ring
	recv.drainPacketRing(50)

	// Only one should be in btree
	require.Equal(t, 1, recv.packetStore.Len(), "duplicate should be dropped")

	// Check drop metric
	dropCount := recv.metrics.CongestionRecvDataDropDuplicate.Load()
	require.Equal(t, uint64(1), dropCount, "one packet should be dropped as duplicate")
}

// TestRingOutOfOrderHandling verifies out-of-order packets are handled correctly
func TestRingOutOfOrderHandling(t *testing.T) {
	deliveredSeqs := []uint32{}

	recv := mockRingRecv(
		nil,
		nil,
		func(p packet.Packet) {
			deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets out of order: 4, 2, 0, 3, 1
	outOfOrder := []uint32{4, 2, 0, 3, 1}
	for _, seq := range outOfOrder {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(seq + 1)

		recv.Push(p)
	}

	// Tick to drain and deliver
	recv.Tick(10)

	// Packets should be delivered in order (btree sorts them)
	require.Equal(t, []uint32{0, 1, 2, 3, 4}, deliveredSeqs, "packets should be delivered in order")
}

// TestRingVsLegacyEquivalence verifies ring path produces same results as legacy path
func TestRingVsLegacyEquivalence(t *testing.T) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create both receivers
	testMetrics1 := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics1.HeaderSize.Store(44)

	testMetrics2 := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics2.HeaderSize.Store(44)

	var deliveredRing []uint32
	var deliveredLegacy []uint32

	recvRing := NewReceiver(ReceiveConfig{
		InitialSequenceNumber:     circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:       10,
		PeriodicNAKInterval:       20,
		ConnectionMetrics:         testMetrics1,
		UsePacketRing:             true,
		PacketRingSize:            256,
		PacketRingShards:          4,
		PacketRingMaxRetries:      10,
		PacketRingBackoffDuration: 100 * time.Microsecond,
		OnDeliver: func(p packet.Packet) {
			deliveredRing = append(deliveredRing, p.Header().PacketSequenceNumber.Val())
		},
	}).(*receiver)

	recvLegacy := NewReceiver(ReceiveConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10,
		PeriodicNAKInterval:   20,
		ConnectionMetrics:     testMetrics2,
		UsePacketRing:         false,
		OnDeliver: func(p packet.Packet) {
			deliveredLegacy = append(deliveredLegacy, p.Header().PacketSequenceNumber.Val())
		},
	}).(*receiver)

	// Push same packets to both (out of order)
	outOfOrder := []uint32{7, 3, 9, 1, 5, 0, 8, 2, 6, 4}
	for _, seq := range outOfOrder {
		p1 := packet.NewPacket(addr)
		p1.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p1.Header().PktTsbpdTime = uint64(seq + 1)

		p2 := packet.NewPacket(addr)
		p2.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p2.Header().PktTsbpdTime = uint64(seq + 1)

		recvRing.Push(p1)
		recvLegacy.Push(p2)
	}

	// Tick both
	recvRing.Tick(20)
	recvLegacy.Tick(20)

	// Results should be identical
	require.Equal(t, deliveredLegacy, deliveredRing, "ring and legacy should deliver same packets")
	require.Equal(t, recvLegacy.lastACKSequenceNumber.Val(), recvRing.lastACKSequenceNumber.Val())
	require.Equal(t, recvLegacy.contiguousPoint.Load(), recvRing.contiguousPoint.Load()) // Phase 4
}

// TestRingConcurrentPush verifies concurrent pushes work correctly
func TestRingConcurrentPush(t *testing.T) {
	recv := mockRingRecv(nil, nil, nil)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	const numGoroutines = 10
	const packetsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Concurrently push packets from multiple goroutines
	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			defer wg.Done()
			baseSeq := uint32(goroutineID * packetsPerGoroutine)
			for i := 0; i < packetsPerGoroutine; i++ {
				p := packet.NewPacket(addr)
				p.Header().PacketSequenceNumber = circular.New(baseSeq+uint32(i), packet.MAX_SEQUENCENUMBER)
				p.Header().PktTsbpdTime = uint64(1000 + baseSeq + uint32(i))

				recv.Push(p)
			}
		}(g)
	}

	wg.Wait()

	// Drain ring
	recv.drainPacketRing(500)

	// Verify total packets received (rate metric counts all pushes)
	totalPushed := recv.metrics.RecvRatePackets.Load()
	require.Equal(t, uint64(numGoroutines*packetsPerGoroutine), totalPushed)

	// Btree should have all unique packets (1000 unique sequences)
	require.Equal(t, numGoroutines*packetsPerGoroutine, recv.packetStore.Len())
}

// TestRingDropsMetric verifies ring drop counting when ring is full
func TestRingDropsMetric(t *testing.T) {
	// Create receiver with very small ring that fills quickly
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := NewReceiver(ReceiveConfig{
		InitialSequenceNumber:     circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:       10,
		PeriodicNAKInterval:       20,
		ConnectionMetrics:         testMetrics,
		UsePacketRing:             true,
		PacketRingSize:            8, // Very small
		PacketRingShards:          1, // Single shard = total 8 slots
		PacketRingMaxRetries:      1, // Give up quickly
		PacketRingBackoffDuration: 1 * time.Microsecond,
		PacketRingMaxBackoffs:     1, // Give up after 1 backoff
	}).(*receiver)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push many more packets than ring can hold
	for i := 0; i < 100; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(100 + i)

		recv.Push(p)
	}

	// Some packets should have been dropped
	drops := recv.metrics.RingDropsTotal.Load()
	t.Logf("Ring drops: %d", drops)

	// With ring size 8 and 100 packets, we expect significant drops
	// (exact number depends on timing, but should be > 0)
	require.Greater(t, drops, uint64(0), "should have ring drops when overwhelmed")
}

// TestRingTooOldPacketHandling verifies old packets are dropped during drain
func TestRingTooOldPacketHandling(t *testing.T) {
	deliveredSeqs := []uint32{}

	recv := mockRingRecv(
		nil,
		nil,
		func(p packet.Packet) {
			deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
		},
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push and deliver first batch
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)

		recv.Push(p)
	}
	recv.Tick(10) // Deliver packets 0-4

	require.Equal(t, uint32(4), recv.contiguousPoint.Load()) // Phase 4

	// Now push an old packet (seq 2, already delivered)
	pOld := packet.NewPacket(addr)
	pOld.Header().PacketSequenceNumber = circular.New(2, packet.MAX_SEQUENCENUMBER)
	pOld.Header().PktTsbpdTime = 3

	recv.Push(pOld)
	recv.Tick(20)

	// Old packet should be dropped, not delivered again
	require.Equal(t, []uint32{0, 1, 2, 3, 4}, deliveredSeqs, "old packet should not be re-delivered")

	// Check drop metric
	belated := recv.metrics.CongestionRecvPktBelated.Load()
	require.Equal(t, uint64(1), belated, "belated packet should be counted")
}

// TestRingFunctionDispatch verifies correct function is called based on config
func TestRingFunctionDispatch(t *testing.T) {
	t.Run("RingEnabled", func(t *testing.T) {
		recv := mockRingRecv(nil, nil, nil)
		addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(1, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 100

		recv.Push(p)

		// With ring enabled, packet goes to ring, NOT directly to btree
		require.Equal(t, 0, recv.packetStore.Len(), "packet should be in ring, not btree")
		require.Greater(t, recv.packetRing.Len(), uint64(0), "ring should have packet")
	})

	t.Run("RingDisabled", func(t *testing.T) {
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
			ConnectionMetrics:     testMetrics,
			UsePacketRing:         false,
		}).(*receiver)

		addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(1, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = 100

		recv.Push(p)

		// With ring disabled, packet goes directly to btree
		require.Equal(t, 1, recv.packetStore.Len(), "packet should be in btree")
	})
}

// TestRingEmptyDrain verifies drainPacketRing handles empty ring correctly
func TestRingEmptyDrain(t *testing.T) {
	recv := mockRingRecv(nil, nil, nil)

	// Drain empty ring - should not panic or error
	recv.drainPacketRing(100)

	require.Equal(t, uint64(0), recv.metrics.RingDrainedPackets.Load())
	require.Equal(t, 0, recv.packetStore.Len())
}
