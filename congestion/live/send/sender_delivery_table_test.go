//go:build go1.18

package send

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Table-driven tests for deliverReadyPacketsEventLoop
//
// These tests validate the delivery logic with various ISN values and
// deliveryStartPoint scenarios. The key bug was:
// - deliveryStartPoint defaulted to 0
// - ISN was ~549M (random from handshake)
// - IterateFrom(0) failed to find packets at ~549M
//
// Reference: send_eventloop_intermittent_failure_bug.md
// ============================================================================

// DeliveryTestCase defines a test case for delivery logic
type DeliveryTestCase struct {
	Name string

	// Setup
	InitialSequenceNumber uint32
	PacketData            []DeliveryPacket // Packets to insert
	NowUs                 uint64           // Current time for delivery check
	DropThresholdUs       uint64           // Drop threshold

	// Expected outcomes
	ExpectedDelivered     int   // Number of packets delivered
	ExpectedNextDeliveryUs int64 // Microseconds until next (-1 = max)
	ExpectedStartPoint    uint32 // Updated deliveryStartPoint after delivery
}

// DeliveryPacket represents a packet for delivery testing
type DeliveryPacket struct {
	SeqOffset   uint32 // Offset from ISN
	TsbpdTimeUs uint64 // TSBPD time in microseconds
}

var deliveryTestCases = []DeliveryTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Empty Btree Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                   "Empty_Btree_ISN_Zero",
		InitialSequenceNumber:  0,
		PacketData:             []DeliveryPacket{},
		NowUs:                  1_000_000,
		DropThresholdUs:        1_000_000,
		ExpectedDelivered:      0,
		ExpectedNextDeliveryUs: -1, // Max sleep when empty
		ExpectedStartPoint:     0,
	},
	{
		Name:                   "Empty_Btree_ISN_Random",
		InitialSequenceNumber:  549144712,
		PacketData:             []DeliveryPacket{},
		NowUs:                  1_000_000,
		DropThresholdUs:        1_000_000,
		ExpectedDelivered:      0,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:     549144712,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// All Ready Cases - ISN=0
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                  "All_Ready_ISN_Zero_5Packets",
		InitialSequenceNumber: 0,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
			{SeqOffset: 1, TsbpdTimeUs: 200},
			{SeqOffset: 2, TsbpdTimeUs: 300},
			{SeqOffset: 3, TsbpdTimeUs: 400},
			{SeqOffset: 4, TsbpdTimeUs: 500},
		},
		NowUs:                  1_000_000, // All are ready
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      5,
		ExpectedNextDeliveryUs: -1, // No more packets
		ExpectedStartPoint:     5,  // Moved past all delivered
	},
	{
		Name:                  "All_Ready_ISN_1000",
		InitialSequenceNumber: 1000,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
			{SeqOffset: 1, TsbpdTimeUs: 200},
			{SeqOffset: 2, TsbpdTimeUs: 300},
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      3,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:     1003, // 1000 + 3
	},

	// ═══════════════════════════════════════════════════════════════════════
	// THE BUG CASES: High ISN values (random from handshake)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                  "High_ISN_549M_All_Ready", // THE FAILING CASE
		InitialSequenceNumber: 549144712,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
			{SeqOffset: 1, TsbpdTimeUs: 200},
			{SeqOffset: 2, TsbpdTimeUs: 300},
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      3,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:     549144715, // 549144712 + 3
	},
	{
		Name:                  "High_ISN_879M_All_Ready", // From actual test metrics
		InitialSequenceNumber: 879502527,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
			{SeqOffset: 1, TsbpdTimeUs: 200},
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      2,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:     879502529,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// None Ready Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                  "None_Ready_ISN_Zero",
		InitialSequenceNumber: 0,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 2_000_000},
			{SeqOffset: 1, TsbpdTimeUs: 3_000_000},
		},
		NowUs:                  1_000_000, // None ready yet
		DropThresholdUs:        5_000_000,
		ExpectedDelivered:      0,
		ExpectedNextDeliveryUs: 1_000_000, // 2M - 1M = 1M
		ExpectedStartPoint:     0,         // Unchanged
	},
	{
		Name:                  "None_Ready_ISN_High",
		InitialSequenceNumber: 549144712,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 5_000_000},
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        10_000_000,
		ExpectedDelivered:      0,
		ExpectedNextDeliveryUs: 4_000_000, // 5M - 1M = 4M
		ExpectedStartPoint:     549144712, // Unchanged
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Partial Ready Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                  "Partial_Ready_2of5",
		InitialSequenceNumber: 0,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
			{SeqOffset: 1, TsbpdTimeUs: 200},
			{SeqOffset: 2, TsbpdTimeUs: 500_000},
			{SeqOffset: 3, TsbpdTimeUs: 600_000},
			{SeqOffset: 4, TsbpdTimeUs: 700_000},
		},
		NowUs:                  300_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      2,
		ExpectedNextDeliveryUs: 200_000, // 500K - 300K
		ExpectedStartPoint:     2,
	},
	{
		Name:                  "Partial_Ready_High_ISN",
		InitialSequenceNumber: 549144712,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
			{SeqOffset: 1, TsbpdTimeUs: 500_000},
		},
		NowUs:                  200_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      1,
		ExpectedNextDeliveryUs: 300_000, // 500K - 200K
		ExpectedStartPoint:     549144713,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Exact Boundary Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                  "Exact_TSBPD_Boundary",
		InitialSequenceNumber: 0,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 1_000_000}, // Exactly at nowUs
			{SeqOffset: 1, TsbpdTimeUs: 1_000_001}, // Just after
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      1, // Only first (<=)
		ExpectedNextDeliveryUs: 1, // 1M+1 - 1M = 1
		ExpectedStartPoint:     1,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Wraparound Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                  "Near_Max_No_Wrap",
		InitialSequenceNumber: 2147483640, // MAX - 7
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
			{SeqOffset: 1, TsbpdTimeUs: 200},
			{SeqOffset: 2, TsbpdTimeUs: 300},
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      3,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:     2147483643,
	},
	{
		Name:                  "At_Max",
		InitialSequenceNumber: 2147483647, // MAX
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      1,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:     0, // Wrapped!
	},
	{
		Name:                  "Wraparound_Across_Boundary",
		InitialSequenceNumber: 2147483645, // MAX - 2
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100}, // Seq 2147483645
			{SeqOffset: 1, TsbpdTimeUs: 200}, // Seq 2147483646
			{SeqOffset: 2, TsbpdTimeUs: 300}, // Seq 2147483647 (MAX)
			{SeqOffset: 3, TsbpdTimeUs: 400}, // Seq 0 (wrapped!)
			{SeqOffset: 4, TsbpdTimeUs: 500}, // Seq 1
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      5,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:     2, // Wrapped past MAX
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Single Packet Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                  "Single_Packet_Ready",
		InitialSequenceNumber: 1000,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 100},
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        2_000_000,
		ExpectedDelivered:      1,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:     1001,
	},
	{
		Name:                  "Single_Packet_Not_Ready",
		InitialSequenceNumber: 1000,
		PacketData: []DeliveryPacket{
			{SeqOffset: 0, TsbpdTimeUs: 2_000_000},
		},
		NowUs:                  1_000_000,
		DropThresholdUs:        5_000_000,
		ExpectedDelivered:      0,
		ExpectedNextDeliveryUs: 1_000_000,
		ExpectedStartPoint:     1000,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Large Batch Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                  "Large_Batch_100_Packets",
		InitialSequenceNumber: 549144712,
		PacketData:            generateDeliveryPackets(100, 1000), // 100 packets, 1ms apart
		NowUs:                 1_000_000_000,                      // All ready (1 second)
		DropThresholdUs:       2_000_000_000,
		ExpectedDelivered:     100,
		ExpectedNextDeliveryUs: -1,
		ExpectedStartPoint:    549144812, // 549144712 + 100
	},
}

// generateDeliveryPackets creates n packets with given interval
func generateDeliveryPackets(n int, intervalUs uint64) []DeliveryPacket {
	packets := make([]DeliveryPacket, n)
	for i := 0; i < n; i++ {
		packets[i] = DeliveryPacket{
			SeqOffset:   uint32(i),
			TsbpdTimeUs: uint64(i) * intervalUs,
		}
	}
	return packets
}

// TestSender_Delivery_Table runs all delivery test cases
func TestSender_Delivery_Table(t *testing.T) {
	for _, tc := range deliveryTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			// Track delivered packets
			var deliveredCount atomic.Int32
			var deliveredSeqs []uint32

			m := &metrics.ConnectionMetrics{}
			start := time.Now()

			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(tc.InitialSequenceNumber, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver: func(p packet.Packet) {
					deliveredCount.Add(1)
					deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
				},
				StartTime:                    start,
				UseBtree:                     true,
				BtreeDegree:                  32,
				UseSendRing:                  true,
				SendRingSize:                 1024,
				UseSendControlRing:           true,
				SendControlRingSize:          256,
				UseSendEventLoop:             true,
				SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
				SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
				SendDropThresholdUs:          tc.DropThresholdUs,
			}).(*sender)

			// Override nowFn to return test time
			s.nowFn = func() uint64 {
				return tc.NowUs
			}

			// Insert test packets into btree
			for _, pd := range tc.PacketData {
				seq := circular.SeqAdd(tc.InitialSequenceNumber, pd.SeqOffset)
				pkt := createTestPacketWithTsbpd(seq, pd.TsbpdTimeUs)
				s.packetBtree.Insert(pkt)
			}

			// Verify initial deliveryStartPoint
			require.Equal(t, uint64(tc.InitialSequenceNumber), s.deliveryStartPoint.Load(),
				"deliveryStartPoint should be initialized to ISN")

			// Run delivery (with EventLoop context)
			var delivered int
			var nextDeliveryIn time.Duration
			runInEventLoopContext(s, func() {
				delivered, nextDeliveryIn = s.deliverReadyPacketsEventLoop(tc.NowUs)
			})

			// Verify delivered count
			require.Equal(t, tc.ExpectedDelivered, delivered,
				"delivered count mismatch")

			// Verify nextDeliveryIn
			if tc.ExpectedNextDeliveryUs == -1 {
				// When all packets are delivered, nextDeliveryIn is 0 (iteration completed)
				// The caller (EventLoop) handles this by checking btree.Len() for next sleep
				require.True(t, nextDeliveryIn == 0 || nextDeliveryIn >= time.Millisecond,
					"expected 0 or max sleep when all delivered, got %v", nextDeliveryIn)
			} else {
				expectedDuration := time.Duration(tc.ExpectedNextDeliveryUs) * time.Microsecond
				// Allow some tolerance due to sleep factor
				require.InDelta(t, expectedDuration.Microseconds(), nextDeliveryIn.Microseconds(),
					float64(expectedDuration.Microseconds())*0.2+1000, // 20% + 1ms tolerance
					"nextDeliveryIn mismatch: expected ~%v, got %v", expectedDuration, nextDeliveryIn)
			}

			// Verify deliveryStartPoint updated correctly
			gotStartPoint := uint32(s.deliveryStartPoint.Load())
			require.Equal(t, tc.ExpectedStartPoint, gotStartPoint,
				"deliveryStartPoint should be updated after delivery")

			// Verify delivered packets were actually delivered (via callback)
			require.Equal(t, int32(tc.ExpectedDelivered), deliveredCount.Load(),
				"OnDeliver callback count mismatch")
		})
	}
}

// TestSender_Delivery_ISN_Mismatch_Bug explicitly tests the bug scenario
func TestSender_Delivery_ISN_Mismatch_Bug(t *testing.T) {
	// This test recreates the exact scenario that caused 60% failures:
	// - ISN = 549144712 (random from handshake)
	// - deliveryStartPoint = 0 (BUG: not initialized)
	// - Packets at 549144712, 549144713, etc.
	// - IterateFrom(0) fails to find packets at ~549M

	isnValues := []uint32{
		549144712,  // THE FAILING CASE
		879502527,  // From actual test metrics
		1073741823, // Half max
		2147483640, // Near max
	}

	for _, isn := range isnValues {
		t.Run(formatISN(isn), func(t *testing.T) {
			var deliveredCount atomic.Int32
			m := &metrics.ConnectionMetrics{}

			s := NewSender(SendConfig{
				InitialSequenceNumber:        circular.New(isn, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:            m,
				OnDeliver:                    func(p packet.Packet) { deliveredCount.Add(1) },
				StartTime:                    time.Now(),
				UseBtree:                     true,
				UseSendRing:                  true,
				UseSendControlRing:           true,
				UseSendEventLoop:             true,
				SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
				SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
				SendDropThresholdUs:          2_000_000,
			}).(*sender)

			// CRITICAL: Verify deliveryStartPoint was initialized correctly
			require.Equal(t, uint64(isn), s.deliveryStartPoint.Load(),
				"BUG: deliveryStartPoint should be %d, not 0", isn)

			// Insert packets at ISN, ISN+1, ISN+2
			for i := uint32(0); i < 3; i++ {
				seq := circular.SeqAdd(isn, i)
				pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
				s.packetBtree.Insert(pkt)
			}

			require.Equal(t, 3, s.packetBtree.Len(), "btree should have 3 packets")

			// Set nowUs to make all packets ready
			s.nowFn = func() uint64 { return 1_000_000 }

			// Run delivery (with EventLoop context)
			var delivered int
			runInEventLoopContext(s, func() {
				delivered, _ = s.deliverReadyPacketsEventLoop(1_000_000)
			})

			// With the fix, all 3 packets should be delivered
			require.Equal(t, 3, delivered,
				"BUG: IterateFrom(%d) should find packets at %d. "+
					"Before fix, IterateFrom(0) would fail to find packets at ~549M. "+
					"See send_eventloop_intermittent_failure_bug.md",
				isn, isn)

			// Verify callback was called
			require.Equal(t, int32(3), deliveredCount.Load())
		})
	}
}

// TestSender_Delivery_Wraparound tests delivery across 31-bit wraparound
func TestSender_Delivery_Wraparound(t *testing.T) {
	testCases := []struct {
		Name          string
		ISN           uint32
		NumPackets    int
		ExpectedStart uint32 // Expected deliveryStartPoint after delivery
	}{
		{
			Name:          "Wrap_From_Max_Minus_2",
			ISN:           2147483645, // MAX - 2
			NumPackets:    5,
			ExpectedStart: 2, // 2147483645 + 5 wraps to 2
		},
		{
			Name:          "Wrap_From_Max_Minus_1",
			ISN:           2147483646, // MAX - 1
			NumPackets:    5,
			ExpectedStart: 3, // 2147483646 + 5 wraps to 3
		},
		{
			Name:          "Wrap_From_Max",
			ISN:           2147483647, // MAX
			NumPackets:    3,
			ExpectedStart: 2, // 2147483647 + 3 wraps to 2
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var deliveredCount atomic.Int32
			m := &metrics.ConnectionMetrics{}

			s := NewSender(SendConfig{
				InitialSequenceNumber:        circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:            m,
				OnDeliver:                    func(p packet.Packet) { deliveredCount.Add(1) },
				StartTime:                    time.Now(),
				UseBtree:                     true,
				UseSendRing:                  true,
				UseSendControlRing:           true,
				UseSendEventLoop:             true,
				SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
				SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
				SendDropThresholdUs:          2_000_000,
			}).(*sender)

			// Insert packets
			for i := 0; i < tc.NumPackets; i++ {
				seq := circular.SeqAdd(tc.ISN, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
				s.packetBtree.Insert(pkt)
			}

			// Set nowUs to make all packets ready
			s.nowFn = func() uint64 { return 1_000_000 }

			// Run delivery (with EventLoop context)
			var delivered int
			runInEventLoopContext(s, func() {
				delivered, _ = s.deliverReadyPacketsEventLoop(1_000_000)
			})

			require.Equal(t, tc.NumPackets, delivered, "all packets should be delivered")

			// Check deliveryStartPoint wrapped correctly
			gotStartPoint := uint32(s.deliveryStartPoint.Load())
			require.Equal(t, tc.ExpectedStart, gotStartPoint,
				"deliveryStartPoint should wrap correctly")
		})
	}
}

// createTestPacketWithTsbpd creates a test packet with specific sequence and TSBPD time
func createTestPacketWithTsbpd(seq uint32, tsbpdTimeUs uint64) packet.Packet {
	p := packet.NewPacket(mockAddr{})
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = tsbpdTimeUs
	return p
}

// ============================================================================
// TransmitCount-based delivery tests
//
// Tests for Phase 3: TransmitCount check in deliverReadyPacketsEventLoop
// - TransmitCount == 0: First transmission - deliver and set to 1
// - TransmitCount >= 1: Already sent - skip (stays for NAK retransmit)
//
// Reference: sender_lockfree_architecture.md Section 7.9.3
// ============================================================================// TestDeliveryTransmitCount_Table tests the TransmitCount-based delivery logic
func TestDeliveryTransmitCount_Table(t *testing.T) {
	testCases := []struct {
		Name                   string
		InitialTransmitCount   uint32
		ExpectDelivered        bool
		ExpectFirstTransmit    uint64
		ExpectAlreadySent      uint64
		ExpectFinalTransmitCount uint32
	}{
		{
			Name:                   "TransmitCount_0_delivers",
			InitialTransmitCount:   0,
			ExpectDelivered:        true,
			ExpectFirstTransmit:    1,
			ExpectAlreadySent:      0,
			ExpectFinalTransmitCount: 1,
		},
		{
			Name:                   "TransmitCount_1_skips",
			InitialTransmitCount:   1,
			ExpectDelivered:        false,
			ExpectFirstTransmit:    0,
			ExpectAlreadySent:      1,
			ExpectFinalTransmitCount: 1, // Unchanged
		},
		{
			Name:                   "TransmitCount_2_skips",
			InitialTransmitCount:   2,
			ExpectDelivered:        false,
			ExpectFirstTransmit:    0,
			ExpectAlreadySent:      1,
			ExpectFinalTransmitCount: 2, // Unchanged
		},
		{
			Name:                   "TransmitCount_high_skips",
			InitialTransmitCount:   10,
			ExpectDelivered:        false,
			ExpectFirstTransmit:    0,
			ExpectAlreadySent:      1,
			ExpectFinalTransmitCount: 10, // Unchanged
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			var deliveredCount atomic.Int32

			s := NewSender(SendConfig{
				InitialSequenceNumber:        circular.New(100, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:            m,
				OnDeliver:                    func(p packet.Packet) { deliveredCount.Add(1) },
				StartTime:                    time.Now(),
				UseBtree:                     true,
				BtreeDegree:                  32,
				UseSendRing:                  true,
				SendRingSize:                 1024,
				SendRingShards:               1,
				UseSendControlRing:           true,
				UseSendEventLoop:             true,
				SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
				SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
			}).(*sender)

			// Create packet with specific TransmitCount
			pkt := createTestPacketWithTsbpd(100, 1000) // TSBPD = 1000µs
			pkt.Header().TransmitCount = tc.InitialTransmitCount
			s.packetBtree.Insert(pkt)

			// Set delivery start point
			s.deliveryStartPoint.Store(100)

			// Deliver with nowUs > tsbpdTime (packet is ready) - with EventLoop context
			nowUs := uint64(2000)
			var delivered int
			runInEventLoopContext(s, func() {
				delivered, _ = s.deliverReadyPacketsEventLoop(nowUs)
			})

			// Check delivery
			if tc.ExpectDelivered {
				require.Equal(t, 1, delivered, "should deliver packet")
				require.Equal(t, int32(1), deliveredCount.Load(), "deliver callback should be called")
			} else {
				require.Equal(t, 0, delivered, "should NOT deliver packet")
				require.Equal(t, int32(0), deliveredCount.Load(), "deliver callback should NOT be called")
			}

			// Check metrics
			require.Equal(t, tc.ExpectFirstTransmit, m.SendFirstTransmit.Load(),
				"SendFirstTransmit metric mismatch")
			require.Equal(t, tc.ExpectAlreadySent, m.SendAlreadySent.Load(),
				"SendAlreadySent metric mismatch")

			// Check final TransmitCount on packet
			gotPkt := s.packetBtree.Get(100)
			require.NotNil(t, gotPkt, "packet should still be in btree")
			require.Equal(t, tc.ExpectFinalTransmitCount, gotPkt.Header().TransmitCount,
				"TransmitCount should be updated correctly")
		})
	}
}

// TestDeliveryTransmitCount_MixedPackets tests a mix of sent and unsent packets
func TestDeliveryTransmitCount_MixedPackets(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	var deliveredSeqs []uint32
	var mu sync.Mutex

	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(100, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver: func(p packet.Packet) {
			mu.Lock()
			deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
			mu.Unlock()
		},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		SendRingShards:               1,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Insert 5 packets: alternating TransmitCount 0 and 1
	// Seq 100: TC=0 (deliver)
	// Seq 101: TC=1 (skip)
	// Seq 102: TC=0 (deliver)
	// Seq 103: TC=1 (skip)
	// Seq 104: TC=0 (deliver)
	for i := uint32(0); i < 5; i++ {
		pkt := createTestPacketWithTsbpd(100+i, 1000) // All ready
		if i%2 == 0 {
			pkt.Header().TransmitCount = 0 // Will be delivered
		} else {
			pkt.Header().TransmitCount = 1 // Already sent, skip
		}
		s.packetBtree.Insert(pkt)
	}

	s.deliveryStartPoint.Store(100)

	// Deliver (with EventLoop context)
	var delivered int
	runInEventLoopContext(s, func() {
		delivered, _ = s.deliverReadyPacketsEventLoop(2000)
	})

	// Should deliver 3 packets (100, 102, 104)
	require.Equal(t, 3, delivered)
	require.Equal(t, uint64(3), m.SendFirstTransmit.Load())
	require.Equal(t, uint64(2), m.SendAlreadySent.Load())

	// Verify which packets were delivered
	require.ElementsMatch(t, []uint32{100, 102, 104}, deliveredSeqs)

	// All packets still in btree (for NAK retransmit)
	require.Equal(t, 5, s.packetBtree.Len())
}
