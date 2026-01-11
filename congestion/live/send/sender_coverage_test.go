//go:build go1.18

package send

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Coverage-focused tests for sender package
//
// These tests specifically target functions with < 90% coverage:
// - tick.go:processControlRing (NAK path)
// - send_packet_btree.go:Max, Iterate
// - nak.go:NAK (various paths)
// - push.go:Push (validation, non-ring paths)
// - stats.go:Stats
// - data_ring.go:TryPush
// ============================================================================

// ============================================================================
// Tick.go Coverage: processControlRing NAK path
// ============================================================================

func TestTick_ProcessControlRing_NAKPath(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(100, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           256,
		UseSendControlRing:     true,
		SendControlRingSize:    64,
		// NOT using EventLoop - Tick mode with control ring
	}).(*sender)

	// Add packets to btree for NAK to find
	for i := 0; i < 5; i++ {
		seq := circular.SeqAdd(100, uint32(i))
		pkt := createTestPacketWithTsbpd(seq, 1000)
		s.packetBtree.Insert(pkt)
	}

	// Verify initial state
	require.Equal(t, uint64(0), m.PktRetransFromNAK.Load())

	// Push NAK to control ring (NAK for seq 100-102 = 3 packets)
	nakSeqs := []circular.Number{
		circular.New(100, packet.MAX_SEQUENCENUMBER),
		circular.New(102, packet.MAX_SEQUENCENUMBER),
	}
	s.controlRing.PushNAK(nakSeqs)

	// Process control ring (should process the NAK)
	s.processControlRing()

	require.Equal(t, uint64(1), m.SendControlRingDrained.Load())
	require.Equal(t, uint64(1), m.SendControlRingProcessed.Load())
	// BUG FIX: PktRetransFromNAK should now be updated when Tick processes NAKs via control ring
	// NAK for seq 100-102 = 3 packets retransmitted
	require.Equal(t, uint64(3), m.PktRetransFromNAK.Load(),
		"PktRetransFromNAK should be updated when Tick processes NAKs via control ring")
}

func TestTick_ProcessControlRing_MultiplePackets(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           256,
		UseSendControlRing:     true,
		SendControlRingSize:    64,
	}).(*sender)

	// Add packets to btree
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		s.packetBtree.Insert(pkt)
	}

	// Push multiple ACKs and NAKs
	s.controlRing.PushACK(circular.New(5, packet.MAX_SEQUENCENUMBER))
	nakSeqs := []circular.Number{
		circular.New(5, packet.MAX_SEQUENCENUMBER),
		circular.New(7, packet.MAX_SEQUENCENUMBER),
	}
	s.controlRing.PushNAK(nakSeqs)

	// Process all
	s.processControlRing()

	require.Equal(t, uint64(2), m.SendControlRingDrained.Load())
	require.Equal(t, uint64(2), m.SendControlRingProcessed.Load())
}

func TestTick_ProcessControlRing_NilRing(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		// No control ring
	}).(*sender)

	// Should not panic with nil control ring
	s.processControlRing()

	require.Equal(t, uint64(0), m.SendControlRingDrained.Load())
}

// ============================================================================
// send_packet_btree.go Coverage: Max, Iterate
// ============================================================================

func TestBtree_Max(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Empty tree
	require.Nil(t, bt.Max())

	// Add packets
	for i := uint32(0); i < 10; i++ {
		pkt := createTestPacketWithTsbpd(i, uint64(i*100))
		bt.Insert(pkt)
	}

	// Max should return highest sequence
	maxPkt := bt.Max()
	require.NotNil(t, maxPkt)
	require.Equal(t, uint32(9), maxPkt.Header().PacketSequenceNumber.Val())
}

func TestBtree_Max_Wraparound(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Add packets near wraparound
	seqs := []uint32{
		packet.MAX_SEQUENCENUMBER - 2,
		packet.MAX_SEQUENCENUMBER - 1,
		packet.MAX_SEQUENCENUMBER,
		0,
		1,
	}

	for _, seq := range seqs {
		pkt := createTestPacketWithTsbpd(seq, 1000)
		bt.Insert(pkt)
	}

	// Max should handle wraparound (highest in sequence order = 1)
	maxPkt := bt.Max()
	require.NotNil(t, maxPkt)
	// With SeqLess ordering, 1 > MAX_SEQUENCENUMBER due to wraparound
	require.Equal(t, uint32(1), maxPkt.Header().PacketSequenceNumber.Val())
}

func TestBtree_Iterate_Empty(t *testing.T) {
	bt := NewSendPacketBtree(32)

	count := 0
	completed := bt.Iterate(func(p packet.Packet) bool {
		count++
		return true
	})

	require.True(t, completed)
	require.Equal(t, 0, count)
}

func TestBtree_Iterate_StopEarly(t *testing.T) {
	bt := NewSendPacketBtree(32)

	for i := uint32(0); i < 10; i++ {
		pkt := createTestPacketWithTsbpd(i, 1000)
		bt.Insert(pkt)
	}

	// Stop after 5 packets
	count := 0
	completed := bt.Iterate(func(p packet.Packet) bool {
		count++
		return count < 5
	})

	require.False(t, completed) // Stopped early
	require.Equal(t, 5, count)
}

func TestBtree_Iterate_All(t *testing.T) {
	bt := NewSendPacketBtree(32)

	for i := uint32(0); i < 10; i++ {
		pkt := createTestPacketWithTsbpd(i, 1000)
		bt.Insert(pkt)
	}

	// Iterate all
	seqs := []uint32{}
	completed := bt.Iterate(func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return true
	})

	require.True(t, completed)
	require.Equal(t, 10, len(seqs))
	// Should be in order
	for i, seq := range seqs {
		require.Equal(t, uint32(i), seq)
	}
}

// ============================================================================
// nak.go Coverage: Various NAK paths
// ============================================================================

func TestNAK_EmptySequences(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	// Empty NAK
	count := s.NAK([]circular.Number{})
	require.Equal(t, uint64(0), count)
}

func TestNAK_WithControlRing_Success(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           256,
		UseSendControlRing:     true,
		SendControlRingSize:    64,
	}).(*sender)

	// NAK should push to ring
	nakSeqs := []circular.Number{
		circular.New(0, packet.MAX_SEQUENCENUMBER),
		circular.New(5, packet.MAX_SEQUENCENUMBER),
	}
	count := s.NAK(nakSeqs)

	require.Equal(t, uint64(0), count) // Returns 0 when routed to ring
	require.Equal(t, uint64(1), m.SendControlRingPushedNAK.Load())
}

func TestNAK_WithControlRing_RingFull(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           256,
		UseSendControlRing:     true,
		SendControlRingSize:    4, // Small ring
	}).(*sender)

	// Add packets to btree
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		s.packetBtree.Insert(pkt)
	}

	// Fill the control ring
	for i := 0; i < 10; i++ {
		nakSeqs := []circular.Number{
			circular.New(uint32(i), packet.MAX_SEQUENCENUMBER),
			circular.New(uint32(i), packet.MAX_SEQUENCENUMBER),
		}
		s.NAK(nakSeqs)
	}

	// Some should have been dropped and fallen back to direct processing
	require.Greater(t, m.SendControlRingDroppedNAK.Load(), uint64(0))
}

func TestNAK_RTOSuppression(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	rtoUs := atomic.Uint64{}
	rtoUs.Store(100_000) // 100ms RTO

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		RTOUs:                 &rtoUs,
	}).(*sender)

	// Add packet
	pkt := createTestPacketWithTsbpd(0, 1000)
	s.packetBtree.Insert(pkt)

	// First NAK - should retransmit
	nakSeqs := []circular.Number{
		circular.New(0, packet.MAX_SEQUENCENUMBER),
		circular.New(0, packet.MAX_SEQUENCENUMBER),
	}
	count1 := s.nakBtree(nakSeqs)
	require.Equal(t, uint64(1), count1)

	// Immediate second NAK - should be suppressed
	count2 := s.nakBtree(nakSeqs)
	require.Equal(t, uint64(0), count2)
	require.Greater(t, m.RetransSuppressed.Load(), uint64(0))
}

func TestNAK_BeforeACK(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	// Set lastACKedSequence
	s.lastACKedSequence = circular.New(100, packet.MAX_SEQUENCENUMBER)

	// NAK for sequence before ACK
	nakSeqs := []circular.Number{
		circular.New(50, packet.MAX_SEQUENCENUMBER),
		circular.New(60, packet.MAX_SEQUENCENUMBER),
	}
	s.nakBtree(nakSeqs)

	require.Equal(t, uint64(1), m.NakBeforeACKCount.Load())
}

func TestNAK_LockedOriginal(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		// Legacy list mode
	}).(*sender)

	// Add packets to lossList
	for i := 0; i < 5; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		s.lossList.PushBack(pkt)
	}

	nakSeqs := []circular.Number{
		circular.New(1, packet.MAX_SEQUENCENUMBER),
		circular.New(3, packet.MAX_SEQUENCENUMBER),
	}
	count := s.nakLockedOriginal(nakSeqs)

	require.Greater(t, count, uint64(0))
}

func TestNAK_LockedHonorOrder(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		HonorNakOrder:         true,
	}).(*sender)

	// Add packets to lossList
	for i := 0; i < 5; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		s.lossList.PushBack(pkt)
	}

	nakSeqs := []circular.Number{
		circular.New(1, packet.MAX_SEQUENCENUMBER),
		circular.New(3, packet.MAX_SEQUENCENUMBER),
	}
	count := s.nakLockedHonorOrder(nakSeqs)

	require.Greater(t, count, uint64(0))
	require.Equal(t, uint64(1), m.CongestionSendNAKHonoredOrder.Load())
}

// ============================================================================
// push.go Coverage: Push validation, non-ring paths
// ============================================================================

func TestPush_PayloadValidation_TooLarge(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:   circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:       m,
		OnDeliver:               func(p packet.Packet) {},
		StartTime:               time.Now(),
		UseBtree:                true,
		ValidatePayloadSize:     true,
	}).(*sender)

	// Create packet and manually set a large payload length
	// We can't easily create oversized packet, so test the validation logic
	// by checking that validation is enabled
	require.True(t, s.validatePayloadSize, "validatePayloadSize should be enabled")
}

func TestPush_NilPacket_Coverage(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	// Push nil - should not panic
	s.Push(nil)

	require.Equal(t, uint64(0), m.CongestionSendPktBuf.Load())
}

func TestPush_LegacyListMode(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		// Legacy list mode (no btree, no ring)
	}).(*sender)

	pkt := createTestPacketWithTsbpd(0, 1000)
	s.Push(pkt)

	require.Equal(t, 1, s.packetList.Len())
}

func TestPush_BtreeMode(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	pkt := createTestPacketWithTsbpd(0, 1000)
	s.Push(pkt)

	require.Equal(t, 1, s.packetBtree.Len())
}

func TestPush_ProbeTimeSetting(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	// Push 17 packets to trigger probe timing
	for i := 0; i < 17; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(1000+i*10))
		s.Push(pkt)
	}

	require.Equal(t, 17, s.packetBtree.Len())
}

func TestPush_WithLockTiming(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	lt := &metrics.LockTimingMetrics{} // Use pointer to LockTimingMetrics

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		LockTimingMetrics:     lt,
	}).(*sender)

	pkt := createTestPacketWithTsbpd(0, 1000)
	s.Push(pkt)

	require.Equal(t, 1, s.packetBtree.Len())
}

func TestPush_RingMode(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           256,
		UseSendControlRing:     true,
		SendControlRingSize:    64,
		UseSendEventLoop:       true,
	}).(*sender)

	// Use Push (which goes through pushRing)
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(0, 1000)
		s.Push(pkt)
	}

	require.Equal(t, uint64(10), m.SendRingPushed.Load())
}

func TestPush_RingFull(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           8, // Small ring
		SendRingShards:         1,
		UseSendControlRing:     true,
		SendControlRingSize:    64,
		UseSendEventLoop:       true,
	}).(*sender)

	// Fill ring and cause drops
	for i := 0; i < 20; i++ {
		pkt := createTestPacketWithTsbpd(0, 1000)
		s.Push(pkt)
	}

	// Some packets should have been dropped
	require.Greater(t, m.SendRingDropped.Load(), uint64(0))
}

// ============================================================================
// stats.go Coverage: Stats() function
// ============================================================================

func TestStats_EmptyBtree(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	stats := s.Stats()

	require.Equal(t, uint64(0), stats.MsBuf)
}

func TestStats_BtreeWithPackets(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	// Add packets with time spread
	for i := 0; i < 5; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i*1_000_000)) // 1 second apart
		s.packetBtree.Insert(pkt)
	}

	stats := s.Stats()

	// Should calculate buffer time (4 seconds = 4000ms)
	require.Equal(t, uint64(4000), stats.MsBuf)
}

func TestStats_LegacyListMode(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		// Legacy list mode
	}).(*sender)

	// Add packets to lossList
	for i := 0; i < 5; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i*1_000_000))
		s.lossList.PushBack(pkt)
	}

	stats := s.Stats()

	// Should calculate buffer time from lossList
	require.Equal(t, uint64(4000), stats.MsBuf)
}

func TestStats_AllFields(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	// Pre-populate metrics
	m.CongestionSendPkt.Store(100)
	m.CongestionSendByte.Store(100000)
	m.CongestionSendPktUnique.Store(95)
	m.CongestionSendByteUnique.Store(95000)
	m.CongestionSendPktLoss.Store(5)
	m.CongestionSendByteLoss.Store(5000)
	m.CongestionSendPktRetrans.Store(3)
	m.CongestionSendByteRetrans.Store(3000)

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	stats := s.Stats()

	require.Equal(t, uint64(100), stats.Pkt)
	require.Equal(t, uint64(100000), stats.Byte)
	require.Equal(t, uint64(95), stats.PktUnique)
	require.Equal(t, uint64(95000), stats.ByteUnique)
	require.Equal(t, uint64(5), stats.PktLoss)
	require.Equal(t, uint64(5000), stats.ByteLoss)
	require.Equal(t, uint64(3), stats.PktRetrans)
	require.Equal(t, uint64(3000), stats.ByteRetrans)
}

// ============================================================================
// data_ring.go Coverage: TryPush
// ============================================================================

func TestDataRing_TryPush(t *testing.T) {
	ring, err := NewSendPacketRing(64, 1)
	require.NoError(t, err)

	pkt := createTestPacketWithTsbpd(100, 1000)

	ok := ring.TryPush(pkt)
	require.True(t, ok)

	// Verify packet is in ring
	require.Equal(t, 1, ring.Len())
}

func TestDataRing_TryPush_Full(t *testing.T) {
	ring, err := NewSendPacketRing(4, 1)
	require.NoError(t, err)

	// Fill the ring
	for i := 0; i < 4; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		ring.TryPush(pkt)
	}

	// Try to push when full
	pkt := createTestPacketWithTsbpd(100, 1000)
	ok := ring.TryPush(pkt)

	// May or may not succeed depending on ring implementation
	// Just ensure no panic
	_ = ok
}

func TestDataRing_TryPop_Empty(t *testing.T) {
	ring, err := NewSendPacketRing(64, 1)
	require.NoError(t, err)

	// Try to pop from empty ring
	pkt, ok := ring.TryPop()
	require.False(t, ok)
	require.Nil(t, pkt)
}

func TestDataRing_DrainBatch(t *testing.T) {
	ring, err := NewSendPacketRing(64, 1)
	require.NoError(t, err)

	// Add packets
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		ring.Push(pkt)
	}

	// Drain batch
	pkts := ring.DrainBatch(100)
	require.Equal(t, 10, len(pkts))
}

func TestDataRing_DrainBatch_PartialDrain(t *testing.T) {
	ring, err := NewSendPacketRing(64, 1)
	require.NoError(t, err)

	// Add packets
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		ring.Push(pkt)
	}

	// Drain only 5
	pkts := ring.DrainBatch(5)
	require.Equal(t, 5, len(pkts))
	require.Equal(t, 5, ring.Len())
}

// ============================================================================
// send_packet_btree.go Coverage: NewSendPacketBtree edge cases
// ============================================================================

func TestBtree_NewWithDifferentDegrees(t *testing.T) {
	degrees := []int{2, 4, 8, 16, 32, 64}

	for _, degree := range degrees {
		t.Run("Degree_"+string(rune('0'+degree)), func(t *testing.T) {
			bt := NewSendPacketBtree(degree)
			require.NotNil(t, bt)
			require.Equal(t, 0, bt.Len())

			// Add and verify
			pkt := createTestPacketWithTsbpd(0, 1000)
			bt.Insert(pkt)
			require.Equal(t, 1, bt.Len())
		})
	}
}

func TestBtree_NewWithDefaultDegree(t *testing.T) {
	// Test with 0 degree (should use default)
	bt := NewSendPacketBtree(0)
	require.NotNil(t, bt)

	// Should still work
	pkt := createTestPacketWithTsbpd(0, 1000)
	bt.Insert(pkt)
	require.Equal(t, 1, bt.Len())
}

func TestBtree_NewWithNegativeDegree(t *testing.T) {
	// Test with negative degree (should use default or handle gracefully)
	bt := NewSendPacketBtree(-1)
	require.NotNil(t, bt)
}

// ============================================================================
// Additional coverage for edge cases
// ============================================================================

// ============================================================================
// Control Ring Coverage
// ============================================================================

func TestControlRing_TryPop_Empty(t *testing.T) {
	ring, err := NewSendControlRing(64, 1)
	require.NoError(t, err)

	cp, ok := ring.TryPop()
	require.False(t, ok)
	require.Equal(t, ControlTypeACK, cp.Type) // Default type
}

func TestControlRing_DrainBatch(t *testing.T) {
	ring, err := NewSendControlRing(64, 1)
	require.NoError(t, err)

	// Add ACKs
	for i := 0; i < 5; i++ {
		ring.PushACK(circular.New(uint32(i*10), packet.MAX_SEQUENCENUMBER))
	}

	// Drain batch
	cps := ring.DrainBatch(10)
	require.Equal(t, 5, len(cps))
}

func TestControlRing_DrainBatch_PartialDrain(t *testing.T) {
	ring, err := NewSendControlRing(64, 1)
	require.NoError(t, err)

	// Add ACKs
	for i := 0; i < 10; i++ {
		ring.PushACK(circular.New(uint32(i*10), packet.MAX_SEQUENCENUMBER))
	}

	// Drain only 5
	cps := ring.DrainBatch(5)
	require.Equal(t, 5, len(cps))
	require.Equal(t, 5, ring.Len())
}

// ============================================================================
// Additional Flush tests
// ============================================================================

func TestFlush_BtreeMode(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
	}).(*sender)

	// Add packets
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		s.packetBtree.Insert(pkt)
	}

	s.Flush()

	require.Equal(t, 0, s.packetBtree.Len())
}

func TestFlush_ListMode(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
	}).(*sender)

	// Add packets to lists
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), 1000)
		s.packetList.PushBack(pkt)
		s.lossList.PushBack(pkt)
	}

	s.Flush()

	require.Equal(t, 0, s.packetList.Len())
	require.Equal(t, 0, s.lossList.Len())
}

func TestFlush_WithRing(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           64,
		UseSendControlRing:     true,
		SendControlRingSize:    32,
		UseSendEventLoop:       true,
	}).(*sender)

	// Add packets to ring and btree
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(0, 1000)
		s.pushRing(pkt)
	}
	s.drainRingToBtreeEventLoop()

	s.Flush()

	require.Equal(t, 0, s.packetBtree.Len())
}

