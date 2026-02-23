package send

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Test Helpers - Shared across all send test files
// ═══════════════════════════════════════════════════════════════════════════

func mockLiveSend(onDeliver func(p packet.Packet)) *sender {
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

	return send.(*sender)
}

func mockLiveSendHighThreshold(onDeliver func(p packet.Packet)) *sender {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         100000,
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
	})

	return send.(*sender)
}

func mockLiveSendHonorOrder(onDeliver func(p packet.Packet)) *sender {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         1000,
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
		HonorNakOrder:         true,
	})

	return send.(*sender)
}

func makeNakListFromPairs(pairs [][2]uint32) []circular.Number {
	list := make([]circular.Number, 0, len(pairs)*2)
	for _, p := range pairs {
		list = append(list, circular.New(p[0], packet.MAX_SEQUENCENUMBER))
		list = append(list, circular.New(p[1], packet.MAX_SEQUENCENUMBER))
	}
	return list
}

// ═══════════════════════════════════════════════════════════════════════════
// Sender Core Tests: NewSender, Flush, SetDropThreshold
// ═══════════════════════════════════════════════════════════════════════════

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

func TestSetDropThreshold(t *testing.T) {
	send := mockLiveSend(nil)

	// Initial threshold from mock
	require.Equal(t, uint64(10), send.dropThreshold)

	// Change threshold
	send.SetDropThreshold(500)
	require.Equal(t, uint64(500), send.dropThreshold)

	// Change again
	send.SetDropThreshold(1000)
	require.Equal(t, uint64(1000), send.dropThreshold)
}

// ═══════════════════════════════════════════════════════════════════════════
// Stats Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestStats(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push some packets
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	// Get stats before tick
	stats := send.Stats()
	require.Equal(t, uint64(5), stats.PktBuf)

	// Tick to deliver packets
	send.Tick(5)

	// Get stats after tick
	stats = send.Stats()
	require.Equal(t, uint64(5), stats.Pkt)
	require.Equal(t, uint64(5), stats.PktUnique)
}

// ═══════════════════════════════════════════════════════════════════════════
// Push Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestPush(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets and verify sequence numbers
	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	require.Equal(t, 5, send.packetList.Len())

	// Verify sequence numbers are assigned correctly
	i := 0
	for e := send.packetList.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		require.Equal(t, uint32(i), p.Header().PacketSequenceNumber.Val())
		i++
	}
}

func TestPush_NilPacket(t *testing.T) {
	send := mockLiveSend(nil)

	// Push nil packet should not panic
	send.Push(nil)
	require.Equal(t, 0, send.packetList.Len())
}

// ═══════════════════════════════════════════════════════════════════════════
// Tick Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestTick_DeliverPackets(t *testing.T) {
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

func TestTick_DropOldPackets(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)
	require.Equal(t, 10, send.lossList.Len())

	// Tick with time > PktTsbpdTime + dropThreshold should drop packets
	send.Tick(20)
	require.Equal(t, 0, send.lossList.Len())

	// Verify drop metrics
	require.Greater(t, send.metrics.CongestionSendDataDropTooOld.Load(), uint64(0))
}

func TestTick_UpdateRateStats(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Set a short period for testing and initialize last time
	send.metrics.SendRatePeriodUs.Store(100) // 100 microseconds
	send.metrics.SendRateLastUs.Store(0)     // Start at 0

	for i := 0; i < 5; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	// First tick delivers packets
	send.Tick(5)

	// Verify packets were delivered
	require.Equal(t, uint64(5), send.metrics.CongestionSendPkt.Load())

	// Tick with enough time elapsed to trigger rate calculation (> periodUs)
	send.Tick(200) // 200 > 100 periodUs, should trigger rate calc

	// Verify last time was updated (proves rate calc happened)
	require.Equal(t, uint64(200), send.metrics.SendRateLastUs.Load())
}

// ═══════════════════════════════════════════════════════════════════════════
// ACK Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestACK(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)
	require.Equal(t, 10, send.lossList.Len())

	// ACK removes packets from loss list
	for i := 0; i < 10; i++ {
		send.ACK(circular.New(uint32(i+1), packet.MAX_SEQUENCENUMBER))
		require.Equal(t, 10-(i+1), send.lossList.Len())
	}
}

func TestACK_UpdatesLastACKedSequence(t *testing.T) {
	send := mockLiveSend(nil)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)

	// Initial lastACKedSequence should be 0
	require.Equal(t, uint32(0), send.lastACKedSequence.Val())

	// ACK sequence 5
	send.ACK(circular.New(5, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, uint32(5), send.lastACKedSequence.Val())

	// ACK sequence 8
	send.ACK(circular.New(8, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, uint32(8), send.lastACKedSequence.Val())

	// ACK with lower sequence should not update
	send.ACK(circular.New(3, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, uint32(8), send.lastACKedSequence.Val())
}

// ═══════════════════════════════════════════════════════════════════════════
// NAK Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestNAK_Retransmit(t *testing.T) {
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

	require.Equal(t, uint64(3), nRetransmit)
	require.Equal(t, uint64(4), nRetransmitFromFlag)
}

func TestNAK_EmptyList(t *testing.T) {
	send := mockLiveSend(nil)

	// Empty NAK should return 0
	result := send.NAK([]circular.Number{})
	require.Equal(t, uint64(0), result)
}

func TestNAK_HonorOrderMetric(t *testing.T) {
	send := mockLiveSendHonorOrder(func(p packet.Packet) {})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for i := 0; i < 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	send.Tick(10)

	require.Equal(t, uint64(0), send.metrics.CongestionSendNAKHonoredOrder.Load())

	nakList := makeNakListFromPairs([][2]uint32{{5, 5}})
	send.NAK(nakList)

	require.Equal(t, uint64(1), send.metrics.CongestionSendNAKHonoredOrder.Load())
}

// ═══════════════════════════════════════════════════════════════════════════
// NAK-before-ACK Defensive Check Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestIsNakBeforeACK(t *testing.T) {
	send := mockLiveSend(nil)
	send.lastACKedSequence = circular.New(100, packet.MAX_SEQUENCENUMBER)

	tests := []struct {
		name     string
		seqNum   uint32
		expected bool
	}{
		{"seq 50 < lastACK 100 → true (invalid)", 50, true},
		{"seq 99 < lastACK 100 → true (invalid)", 99, true},
		{"seq 100 == lastACK 100 → false (valid)", 100, false},
		{"seq 101 > lastACK 100 → false (valid)", 101, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seqNum := circular.New(tt.seqNum, packet.MAX_SEQUENCENUMBER)
			result := send.isNakBeforeACK(seqNum)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestCheckNakBeforeACK_NoViolation(t *testing.T) {
	send := mockLiveSend(nil)
	send.lastACKedSequence = circular.New(100, packet.MAX_SEQUENCENUMBER)

	nakList := makeNakListFromPairs([][2]uint32{{105, 105}, {110, 115}})

	initialCount := send.metrics.NakBeforeACKCount.Load()
	send.checkNakBeforeACK(nakList)

	require.Equal(t, initialCount, send.metrics.NakBeforeACKCount.Load())
}

func TestCheckNakBeforeACK_WithViolation(t *testing.T) {
	send := mockLiveSend(nil)
	send.lastACKedSequence = circular.New(100, packet.MAX_SEQUENCENUMBER)

	nakList := makeNakListFromPairs([][2]uint32{{50, 60}, {110, 115}})

	initialCount := send.metrics.NakBeforeACKCount.Load()
	send.checkNakBeforeACK(nakList)

	require.Equal(t, initialCount+1, send.metrics.NakBeforeACKCount.Load())
}

func TestCheckNakBeforeACK_EmptyList(t *testing.T) {
	send := mockLiveSend(nil)
	send.lastACKedSequence = circular.New(100, packet.MAX_SEQUENCENUMBER)

	nakList := []circular.Number{}

	initialCount := send.metrics.NakBeforeACKCount.Load()
	send.checkNakBeforeACK(nakList)

	require.Equal(t, initialCount, send.metrics.NakBeforeACKCount.Load())
}

func TestIsNakBeforeACK_Wraparound(t *testing.T) {
	send := mockLiveSend(nil)
	send.lastACKedSequence = circular.New(packet.MAX_SEQUENCENUMBER-100, packet.MAX_SEQUENCENUMBER)

	tests := []struct {
		name     string
		seqNum   uint32
		expected bool
	}{
		{"seq MAX-200 < lastACK MAX-100 → invalid", packet.MAX_SEQUENCENUMBER - 200, true},
		{"seq MAX-100 == lastACK → valid", packet.MAX_SEQUENCENUMBER - 100, false},
		{"seq 0 (wrapped) > lastACK MAX-100 → valid", 0, false},
		{"seq 100 (wrapped) > lastACK MAX-100 → valid", 100, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seqNum := circular.New(tt.seqNum, packet.MAX_SEQUENCENUMBER)
			result := send.isNakBeforeACK(seqNum)
			require.Equal(t, tt.expected, result)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Retransmit Suppression Tests (Phase 6: RTO Suppression)
// ═══════════════════════════════════════════════════════════════════════════

// mockLiveSendWithRTO creates a sender with RTO suppression enabled
func mockLiveSendWithRTO(onDeliver func(p packet.Packet), rtoUs *atomic.Uint64) *sender {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         100000, // High threshold to keep packets
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
		RTOUs:                 rtoUs, // Enable RTO suppression
	})

	return send.(*sender)
}

// TestRetransmitSuppression_FirstRetransmitAllowed verifies first retransmit goes through
func TestRetransmitSuppression_FirstRetransmitAllowed(t *testing.T) {
	var rtoUs atomic.Uint64
	rtoUs.Store(100_000) // 100ms RTO → 50ms one-way

	deliverCount := 0
	send := mockLiveSendWithRTO(func(p packet.Packet) {
		deliverCount++
	}, &rtoUs)

	// Push a packet
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 6000}
	p := packet.NewPacket(addr)
	p.Header().PktTsbpdTime = 1
	send.Push(p)

	// Tick to deliver packet and move to loss list (timestamp > PktTsbpdTime)
	send.Tick(10)
	initialDeliverCount := deliverCount // Count includes initial delivery

	// First NAK should trigger retransmit - use sequence 0 (first packet)
	nakList := []circular.Number{
		circular.New(0, packet.MAX_SEQUENCENUMBER),
		circular.New(0, packet.MAX_SEQUENCENUMBER),
	}
	count := send.NAK(nakList)

	require.Equal(t, uint64(1), count, "First retransmit should succeed")
	require.Equal(t, initialDeliverCount+1, deliverCount, "Packet should be re-delivered")
	require.Equal(t, uint64(1), send.metrics.RetransFirstTime.Load(), "Should track first-time retransmit")
	require.Equal(t, uint64(1), send.metrics.RetransAllowed.Load(), "Should track allowed retransmit")
	require.Equal(t, uint64(0), send.metrics.RetransSuppressed.Load(), "No suppression on first retransmit")
}

// TestRetransmitSuppression_ImmediateSecondSuppressed verifies immediate second retransmit is suppressed
func TestRetransmitSuppression_ImmediateSecondSuppressed(t *testing.T) {
	var rtoUs atomic.Uint64
	rtoUs.Store(100_000) // 100ms RTO → 50ms one-way

	deliverCount := 0
	send := mockLiveSendWithRTO(func(p packet.Packet) {
		deliverCount++
	}, &rtoUs)

	// Push a packet
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 6000}
	p := packet.NewPacket(addr)
	p.Header().PktTsbpdTime = 1
	send.Push(p)

	// Tick to deliver packet and move to loss list
	send.Tick(10)
	initialDeliverCount := deliverCount

	nakList := []circular.Number{
		circular.New(0, packet.MAX_SEQUENCENUMBER),
		circular.New(0, packet.MAX_SEQUENCENUMBER),
	}

	// First NAK - should retransmit
	count1 := send.NAK(nakList)
	require.Equal(t, uint64(1), count1, "First retransmit should succeed")
	require.Equal(t, initialDeliverCount+1, deliverCount)

	// Immediate second NAK - should be suppressed (within 50ms one-way delay)
	countAfterFirst := deliverCount
	count2 := send.NAK(nakList)
	require.Equal(t, uint64(0), count2, "Immediate second retransmit should be suppressed")
	require.Equal(t, countAfterFirst, deliverCount, "No additional delivery")
	require.Equal(t, uint64(1), send.metrics.RetransSuppressed.Load(), "Should track suppressed retransmit")
}

// TestRetransmitSuppression_AfterDelayAllowed verifies retransmit after delay goes through
func TestRetransmitSuppression_AfterDelayAllowed(t *testing.T) {
	var rtoUs atomic.Uint64
	rtoUs.Store(20_000) // 20ms RTO → 10ms one-way (short for testing)

	deliverCount := 0
	send := mockLiveSendWithRTO(func(p packet.Packet) {
		deliverCount++
	}, &rtoUs)

	// Push a packet
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 6000}
	p := packet.NewPacket(addr)
	p.Header().PktTsbpdTime = 1
	send.Push(p)

	// Tick to deliver packet and move to loss list
	send.Tick(10)
	initialDeliverCount := deliverCount

	nakList := []circular.Number{
		circular.New(0, packet.MAX_SEQUENCENUMBER),
		circular.New(0, packet.MAX_SEQUENCENUMBER),
	}

	// First NAK - should retransmit
	count1 := send.NAK(nakList)
	require.Equal(t, uint64(1), count1)

	// Wait longer than one-way delay (10ms + margin)
	time.Sleep(15 * time.Millisecond)

	// Second NAK - should succeed after delay
	count2 := send.NAK(nakList)
	require.Equal(t, uint64(1), count2, "Retransmit after delay should succeed")
	require.Equal(t, initialDeliverCount+2, deliverCount, "Should have two retransmit deliveries")
	require.Equal(t, uint64(2), send.metrics.RetransAllowed.Load())
}

// TestRetransmitSuppression_DisabledWhenNilRTOUs verifies no suppression when rtoUs is nil
func TestRetransmitSuppression_DisabledWhenNilRTOUs(t *testing.T) {
	// No RTO suppression (nil rtoUs)
	deliverCount := 0
	send := mockLiveSendHighThreshold(func(p packet.Packet) {
		deliverCount++
	})

	// Push a packet
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 6000}
	p := packet.NewPacket(addr)
	p.Header().PktTsbpdTime = 1
	send.Push(p)

	// Tick to deliver packet and move to loss list
	send.Tick(10)
	initialDeliverCount := deliverCount

	nakList := []circular.Number{
		circular.New(0, packet.MAX_SEQUENCENUMBER),
		circular.New(0, packet.MAX_SEQUENCENUMBER),
	}

	// Multiple immediate NAKs - all should succeed without suppression
	count1 := send.NAK(nakList)
	count2 := send.NAK(nakList)
	count3 := send.NAK(nakList)

	require.Equal(t, uint64(1), count1)
	require.Equal(t, uint64(1), count2)
	require.Equal(t, uint64(1), count3)
	require.Equal(t, initialDeliverCount+3, deliverCount, "All retransmits should succeed without RTO suppression")
	require.Equal(t, uint64(0), send.metrics.RetransSuppressed.Load(), "No suppression when rtoUs is nil")
}

// TestRetransmitSuppression_HonorOrderMode verifies suppression works with HonorNakOrder
func TestRetransmitSuppression_HonorOrderMode(t *testing.T) {
	var rtoUs atomic.Uint64
	rtoUs.Store(100_000) // 100ms RTO → 50ms one-way

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	retransmitCount := 0
	send := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         100000,
		OnDeliver: func(p packet.Packet) {
			retransmitCount++
		},
		ConnectionMetrics: testMetrics,
		HonorNakOrder:     true,   // Enable honor-order mode
		RTOUs:             &rtoUs, // Enable RTO suppression
	}).(*sender)

	// Push a packet
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 6000}
	p := packet.NewPacket(addr)
	p.Header().PktTsbpdTime = 1
	send.Push(p)

	// Tick to move packet to loss list
	send.Tick(10)

	nakList := []circular.Number{
		circular.New(0, packet.MAX_SEQUENCENUMBER),
		circular.New(0, packet.MAX_SEQUENCENUMBER),
	}

	// First NAK - should retransmit
	count1 := send.NAK(nakList)
	require.Equal(t, uint64(1), count1, "First retransmit should succeed in honor-order mode")

	// Immediate second NAK - should be suppressed
	count2 := send.NAK(nakList)
	require.Equal(t, uint64(0), count2, "Immediate second should be suppressed in honor-order mode")
	require.Equal(t, uint64(1), send.metrics.RetransSuppressed.Load())
}
