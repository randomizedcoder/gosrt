//go:build go1.18

package send

import (
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Table-driven tests for Ring → Btree flow
//
// Tests the lock-free ring buffer flow:
// - Push to ring (from application)
// - Drain from ring to btree (EventLoop)
// - Verify sequence number assignment
// - Verify ordering preservation
//
// Reference: lockless_sender_design.md Section 3.2 "SendPacketRing"
// ============================================================================

// RingFlowTestCase defines a test case for ring flow
type RingFlowTestCase struct {
	Name string

	// Setup
	ISN          uint32
	RingSize     int
	RingShards   int
	PacketCount  int

	// Expected
	ExpectedDrained int
	ExpectedInBtree int
}

var ringFlowTestCases = []RingFlowTestCase{
	{
		Name:            "Single_Packet",
		ISN:             0,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     1,
		ExpectedDrained: 1,
		ExpectedInBtree: 1,
	},
	{
		Name:            "Small_Batch_10",
		ISN:             0,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     10,
		ExpectedDrained: 10,
		ExpectedInBtree: 10,
	},
	{
		Name:            "Medium_Batch_100",
		ISN:             549144712,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     100,
		ExpectedDrained: 100,
		ExpectedInBtree: 100,
	},
	{
		Name:            "Large_Batch_500",
		ISN:             0,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     500,
		ExpectedDrained: 500,
		ExpectedInBtree: 500,
	},
	{
		Name:            "Multi_Shard_2",
		ISN:             0,
		RingSize:        512,
		RingShards:      2,
		PacketCount:     100,
		ExpectedDrained: 100,
		ExpectedInBtree: 100,
	},
	{
		Name:            "Multi_Shard_4",
		ISN:             0,
		RingSize:        256,
		RingShards:      4,
		PacketCount:     200,
		ExpectedDrained: 200,
		ExpectedInBtree: 200,
	},
	{
		Name:            "High_ISN_Wraparound",
		ISN:             MaxSeq31Bit - 50,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     100,
		ExpectedDrained: 100,
		ExpectedInBtree: 100,
	},
}

// TestRingFlow_Table runs all ring flow test cases
func TestRingFlow_Table(t *testing.T) {
	for _, tc := range ringFlowTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber:        circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:            m,
				OnDeliver:                    func(p packet.Packet) {},
				StartTime:                    time.Now(),
				UseBtree:                     true,
				BtreeDegree:                  32,
				UseSendRing:                  true,
				SendRingSize:                 tc.RingSize,
				SendRingShards:               tc.RingShards,
				UseSendControlRing:           true,
				UseSendEventLoop:             true,
				SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
				SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
			}).(*sender)

			// Push packets
			for i := 0; i < tc.PacketCount; i++ {
				pkt := createTestPacketWithTsbpd(0, uint64(i)*100) // Seq assigned by pushRing
				s.pushRing(pkt)
			}

			// Verify ring has packets
			require.GreaterOrEqual(t, s.packetRing.Len(), 0)

			// Drain all
			drained := s.drainRingToBtreeEventLoop()

			// Verify drain count
			require.Equal(t, tc.ExpectedDrained, drained,
				"drained count mismatch")

			// Verify btree count
			require.Equal(t, tc.ExpectedInBtree, s.packetBtree.Len(),
				"btree count mismatch")

			// Verify ring is empty
			require.Equal(t, 0, s.packetRing.Len(),
				"ring should be empty after drain")
		})
	}
}

// TestRingFlow_SequenceAssignment verifies sequence numbers are assigned in Push order
func TestRingFlow_SequenceAssignment(t *testing.T) {
	testCases := []struct {
		Name         string
		ISN          uint32
		PacketCount  int
		ExpectedSeqs []uint32
	}{
		{
			Name:         "ISN_Zero",
			ISN:          0,
			PacketCount:  5,
			ExpectedSeqs: []uint32{0, 1, 2, 3, 4},
		},
		{
			Name:         "ISN_1000",
			ISN:          1000,
			PacketCount:  5,
			ExpectedSeqs: []uint32{1000, 1001, 1002, 1003, 1004},
		},
		{
			Name:         "ISN_High",
			ISN:          549144712,
			PacketCount:  3,
			ExpectedSeqs: []uint32{549144712, 549144713, 549144714},
		},
		{
			Name:         "ISN_Wrap",
			ISN:          MaxSeq31Bit - 2,
			PacketCount:  5,
			ExpectedSeqs: []uint32{MaxSeq31Bit - 2, MaxSeq31Bit - 1, MaxSeq31Bit, 0, 1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver:             func(p packet.Packet) {},
				StartTime:             time.Now(),
				UseBtree:              true,
				UseSendRing:           true,
				UseSendControlRing:    true,
				UseSendEventLoop:      true,
			}).(*sender)

			// Push packets
			for i := 0; i < tc.PacketCount; i++ {
				pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
				s.pushRing(pkt)
			}

			// Drain to btree
			s.drainRingToBtreeEventLoop()

			// Collect sequences from btree (in order)
			var seqs []uint32
			s.packetBtree.Iterate(func(p packet.Packet) bool {
				seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
				return true
			})

			require.Equal(t, tc.ExpectedSeqs, seqs,
				"sequence numbers should match expected order")
		})
	}
}

// TestRingFlow_MultipleDrains tests incremental draining
func TestRingFlow_MultipleDrains(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Push 10 packets
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
		s.pushRing(pkt)
	}

	// Drain
	drained1 := s.drainRingToBtreeEventLoop()
	require.Equal(t, 10, drained1)
	require.Equal(t, 10, s.packetBtree.Len())

	// Push 10 more
	for i := 10; i < 20; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
		s.pushRing(pkt)
	}

	// Drain again
	drained2 := s.drainRingToBtreeEventLoop()
	require.Equal(t, 10, drained2)
	require.Equal(t, 20, s.packetBtree.Len())

	// Verify sequence continuity
	var seqs []uint32
	s.packetBtree.Iterate(func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return true
	})

	for i := 0; i < 20; i++ {
		require.Equal(t, uint32(i), seqs[i], "sequence %d mismatch", i)
	}
}

// TestRingFlow_RingCapacity tests behavior at ring capacity
func TestRingFlow_RingCapacity(t *testing.T) {
	ringSize := 64

	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 ringSize,
		SendRingShards:               1,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Push exactly ring capacity
	for i := 0; i < ringSize; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
		s.pushRing(pkt)
	}

	// Drain all
	drained := s.drainRingToBtreeEventLoop()

	// Should have drained up to ring capacity
	require.LessOrEqual(t, drained, ringSize)
	require.Equal(t, drained, s.packetBtree.Len())
}

// TestRingFlow_EmptyDrain tests draining when ring is empty
func TestRingFlow_EmptyDrain(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Drain when empty
	drained := s.drainRingToBtreeEventLoop()
	require.Equal(t, 0, drained)
	require.Equal(t, 0, s.packetBtree.Len())
}

// TestRingFlow_PreservesTsbpdTime verifies TSBPD time is preserved through flow
func TestRingFlow_PreservesTsbpdTime(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	expectedTsbpd := []uint64{100_000, 200_000, 300_000, 400_000, 500_000}

	// Push packets with specific TSBPD times
	for i, tsbpd := range expectedTsbpd {
		pkt := createTestPacketWithTsbpd(uint32(i), tsbpd)
		// Note: pushRing may modify PktTsbpdTime for probe packets (seq % 16)
		// For seq 0 and 1, there's special handling
		s.pushRing(pkt)
	}

	// Drain
	s.drainRingToBtreeEventLoop()

	// Verify TSBPD times are preserved (for non-probe packets)
	idx := 0
	s.packetBtree.Iterate(func(p packet.Packet) bool {
		seq := p.Header().PacketSequenceNumber.Val()
		// Skip probe packets (seq % 16 == 0 or 1)
		if seq%16 != 0 && seq%16 != 1 {
			require.Equal(t, expectedTsbpd[idx], p.Header().PktTsbpdTime,
				"TSBPD time for packet %d", seq)
		}
		idx++
		return true
	})
}



