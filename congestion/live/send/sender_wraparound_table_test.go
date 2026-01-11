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
// Table-driven tests for 31-bit sequence number wraparound
//
// The SRT protocol uses 31-bit sequence numbers (max 2147483647).
// These tests verify that all sender operations correctly handle wraparound.
//
// Reference: circular/seq_math_31bit_wraparound_test.go for comparison patterns
// ============================================================================

const (
	MaxSeq31Bit = uint32(2147483647) // 2^31 - 1
)

// WraparoundTestCase defines a test case for wraparound operations
type WraparoundTestCase struct {
	Name string

	// Setup
	ISN        uint32 // Initial sequence number
	NumPackets int    // Number of packets to insert/process

	// Expected
	ExpectedFirstSeq uint32 // First packet sequence
	ExpectedLastSeq  uint32 // Last packet sequence (after wraparound)
	CrossesWrap      bool   // Whether sequence crosses MAX→0 boundary
}

var wraparoundInsertTestCases = []WraparoundTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// No Wraparound Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:             "NoWrap_Zero_Start",
		ISN:              0,
		NumPackets:       10,
		ExpectedFirstSeq: 0,
		ExpectedLastSeq:  9,
		CrossesWrap:      false,
	},
	{
		Name:             "NoWrap_Middle_Start",
		ISN:              1000000,
		NumPackets:       100,
		ExpectedFirstSeq: 1000000,
		ExpectedLastSeq:  1000099,
		CrossesWrap:      false,
	},
	{
		Name:             "NoWrap_Near_Max",
		ISN:              MaxSeq31Bit - 100,
		NumPackets:       50,
		ExpectedFirstSeq: MaxSeq31Bit - 100,
		ExpectedLastSeq:  MaxSeq31Bit - 51,
		CrossesWrap:      false,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Wraparound Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:             "Wrap_From_Max_Minus_5",
		ISN:              MaxSeq31Bit - 5,
		NumPackets:       10,
		ExpectedFirstSeq: MaxSeq31Bit - 5,
		ExpectedLastSeq:  3, // Wraps: MAX-5, MAX-4, MAX-3, MAX-2, MAX-1, MAX, 0, 1, 2, 3
		CrossesWrap:      true,
	},
	{
		Name:             "Wrap_From_Max_Minus_2",
		ISN:              MaxSeq31Bit - 2,
		NumPackets:       5,
		ExpectedFirstSeq: MaxSeq31Bit - 2,
		ExpectedLastSeq:  1, // MAX-2, MAX-1, MAX, 0, 1
		CrossesWrap:      true,
	},
	{
		Name:             "Wrap_From_Max_Minus_1",
		ISN:              MaxSeq31Bit - 1,
		NumPackets:       5,
		ExpectedFirstSeq: MaxSeq31Bit - 1,
		ExpectedLastSeq:  2, // MAX-1, MAX, 0, 1, 2
		CrossesWrap:      true,
	},
	{
		Name:             "Wrap_From_Max",
		ISN:              MaxSeq31Bit,
		NumPackets:       5,
		ExpectedFirstSeq: MaxSeq31Bit,
		ExpectedLastSeq:  3, // MAX, 0, 1, 2, 3
		CrossesWrap:      true,
	},
	{
		Name:             "Wrap_Large_Batch_100",
		ISN:              MaxSeq31Bit - 50,
		NumPackets:       100,
		ExpectedFirstSeq: MaxSeq31Bit - 50,
		ExpectedLastSeq:  48, // Wraps past MAX
		CrossesWrap:      true,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Post-Wraparound Cases (starting after wrap)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:             "PostWrap_Start_At_Zero",
		ISN:              0,
		NumPackets:       5,
		ExpectedFirstSeq: 0,
		ExpectedLastSeq:  4,
		CrossesWrap:      false,
	},
	{
		Name:             "PostWrap_Start_At_One",
		ISN:              1,
		NumPackets:       5,
		ExpectedFirstSeq: 1,
		ExpectedLastSeq:  5,
		CrossesWrap:      false,
	},
}

// TestSender_Wraparound_Insert tests packet insertion across wraparound
func TestSender_Wraparound_Insert(t *testing.T) {
	for _, tc := range wraparoundInsertTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver:             func(p packet.Packet) {},
				StartTime:             time.Now(),
				UseBtree:              true,
				BtreeDegree:           32,
				UseSendRing:           true,
				UseSendControlRing:    true,
				UseSendEventLoop:      true,
			}).(*sender)

			// Insert packets
			for i := 0; i < tc.NumPackets; i++ {
				seq := circular.SeqAdd(tc.ISN, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
				s.packetBtree.Insert(pkt)
			}

			// Verify btree length
			require.Equal(t, tc.NumPackets, s.packetBtree.Len(),
				"btree should contain all inserted packets")

			// Verify first packet
			minPkt := s.packetBtree.Min()
			require.NotNil(t, minPkt)
			require.Equal(t, tc.ExpectedFirstSeq, minPkt.Header().PacketSequenceNumber.Val(),
				"first packet sequence mismatch")

			// Verify last packet by iterating
			var lastSeq uint32
			s.packetBtree.Iterate(func(p packet.Packet) bool {
				lastSeq = p.Header().PacketSequenceNumber.Val()
				return true
			})
			require.Equal(t, tc.ExpectedLastSeq, lastSeq,
				"last packet sequence mismatch")

			// If wraparound, verify we can find packets on both sides
			if tc.CrossesWrap {
				// Should find packet at MAX
				pktAtMax := s.packetBtree.Get(MaxSeq31Bit)
				if tc.ISN <= MaxSeq31Bit && circular.SeqAdd(tc.ISN, uint32(tc.NumPackets-1)) >= 0 {
					require.NotNil(t, pktAtMax, "should find packet at MAX during wraparound")
				}
			}
		})
	}
}

// TestSender_Wraparound_IterateFrom tests IterateFrom across wraparound boundary
func TestSender_Wraparound_IterateFrom(t *testing.T) {
	testCases := []struct {
		Name          string
		ISN           uint32
		NumPackets    int
		StartFrom     uint32 // Where to start iteration
		ExpectedCount int    // Expected packets found
	}{
		{
			Name:          "IterateFrom_Zero_NoWrap",
			ISN:           0,
			NumPackets:    10,
			StartFrom:     0,
			ExpectedCount: 10,
		},
		{
			Name:          "IterateFrom_Middle_NoWrap",
			ISN:           0,
			NumPackets:    10,
			StartFrom:     5,
			ExpectedCount: 5, // 5,6,7,8,9
		},
		{
			Name:          "IterateFrom_Before_Wrap",
			ISN:           MaxSeq31Bit - 5,
			NumPackets:    10,
			StartFrom:     MaxSeq31Bit - 5,
			ExpectedCount: 10,
		},
		{
			Name:          "IterateFrom_After_Wrap",
			ISN:           MaxSeq31Bit - 5,
			NumPackets:    10,
			StartFrom:     0, // Start from wrapped portion
			ExpectedCount: 4, // 0, 1, 2, 3
		},
		{
			Name:          "IterateFrom_At_Max",
			ISN:           MaxSeq31Bit - 2,
			NumPackets:    5,
			StartFrom:     MaxSeq31Bit,
			ExpectedCount: 3, // MAX, 0, 1
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

			// Insert packets
			for i := 0; i < tc.NumPackets; i++ {
				seq := circular.SeqAdd(tc.ISN, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
				s.packetBtree.Insert(pkt)
			}

			// Count packets found via IterateFrom
			count := 0
			s.packetBtree.IterateFrom(tc.StartFrom, func(p packet.Packet) bool {
				count++
				return true
			})

			require.Equal(t, tc.ExpectedCount, count,
				"IterateFrom(%d) found wrong number of packets", tc.StartFrom)
		})
	}
}

// TestSender_Wraparound_DeleteBefore tests DeleteBefore across wraparound
func TestSender_Wraparound_DeleteBefore(t *testing.T) {
	testCases := []struct {
		Name               string
		ISN                uint32
		NumPackets         int
		DeleteBefore       uint32
		ExpectedRemaining  int
		ExpectedMinSeqAfter uint32
	}{
		{
			Name:               "DeleteBefore_NoWrap",
			ISN:                0,
			NumPackets:         10,
			DeleteBefore:       5,
			ExpectedRemaining:  5, // 5,6,7,8,9
			ExpectedMinSeqAfter: 5,
		},
		{
			Name:               "DeleteBefore_AllPackets",
			ISN:                0,
			NumPackets:         10,
			DeleteBefore:       10,
			ExpectedRemaining:  0,
			ExpectedMinSeqAfter: 0, // N/A (empty)
		},
		{
			Name:               "DeleteBefore_None",
			ISN:                100,
			NumPackets:         10,
			DeleteBefore:       50, // Before all packets
			ExpectedRemaining:  10,
			ExpectedMinSeqAfter: 100,
		},
		{
			Name:               "DeleteBefore_Wrap_DeletePre",
			ISN:                MaxSeq31Bit - 5,
			NumPackets:         10,
			DeleteBefore:       MaxSeq31Bit, // Delete up to MAX (exclusive)
			ExpectedRemaining:  5,           // MAX, 0, 1, 2, 3
			ExpectedMinSeqAfter: MaxSeq31Bit,
		},
		{
			Name:               "DeleteBefore_Wrap_DeletePost",
			ISN:                MaxSeq31Bit - 5,
			NumPackets:         10,
			DeleteBefore:       2, // Delete including wrapped 0,1
			ExpectedRemaining:  2, // 2, 3
			ExpectedMinSeqAfter: 2,
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

			// Insert packets
			for i := 0; i < tc.NumPackets; i++ {
				seq := circular.SeqAdd(tc.ISN, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
				s.packetBtree.Insert(pkt)
			}

			// Delete packets before threshold
			deleted, _ := s.packetBtree.DeleteBefore(tc.DeleteBefore)
			_ = deleted // We could verify this too

			// Verify remaining count
			require.Equal(t, tc.ExpectedRemaining, s.packetBtree.Len(),
				"remaining packet count mismatch after DeleteBefore(%d)", tc.DeleteBefore)

			// Verify minimum sequence (if any remain)
			if tc.ExpectedRemaining > 0 {
				minPkt := s.packetBtree.Min()
				require.NotNil(t, minPkt)
				require.Equal(t, tc.ExpectedMinSeqAfter, minPkt.Header().PacketSequenceNumber.Val(),
					"minimum sequence after delete mismatch")
			}
		})
	}
}

// TestSender_Wraparound_Delivery tests delivery across wraparound boundary
func TestSender_Wraparound_Delivery(t *testing.T) {
	testCases := []struct {
		Name              string
		ISN               uint32
		NumPackets        int
		NowUs             uint64
		ExpectedDelivered int
		ExpectedStartPoint uint32
	}{
		{
			Name:              "Deliver_Wrap_All",
			ISN:               MaxSeq31Bit - 5,
			NumPackets:        10,
			NowUs:             1_000_000,
			ExpectedDelivered: 10,
			ExpectedStartPoint: 4, // ISN + 10 wraps to 4
		},
		{
			Name:              "Deliver_At_Max",
			ISN:               MaxSeq31Bit,
			NumPackets:        3,
			NowUs:             1_000_000,
			ExpectedDelivered: 3,
			ExpectedStartPoint: 2, // MAX, 0, 1 → next is 2
		},
		{
			Name:              "Deliver_Partial_Wrap",
			ISN:               MaxSeq31Bit - 2,
			NumPackets:        5,
			NowUs:             300, // First 4 packets ready (TSBPD 0, 100, 200, 300 are all <= 300)
			ExpectedDelivered: 4,
			ExpectedStartPoint: MaxSeq31Bit + 2, // Should be 1 after wrap
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var deliveredCount atomic.Int32
			var deliveredSeqs []uint32

			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver: func(p packet.Packet) {
					deliveredCount.Add(1)
					deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
				},
				StartTime:          time.Now(),
				UseBtree:           true,
				UseSendRing:        true,
				UseSendControlRing: true,
				UseSendEventLoop:   true,
				SendDropThresholdUs: 10_000_000,
			}).(*sender)

			// Override nowFn
			s.nowFn = func() uint64 { return tc.NowUs }

			// Insert packets
			for i := 0; i < tc.NumPackets; i++ {
				seq := circular.SeqAdd(tc.ISN, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
				s.packetBtree.Insert(pkt)
			}

			// Run delivery
			delivered, _ := s.deliverReadyPacketsEventLoop(tc.NowUs)

			require.Equal(t, tc.ExpectedDelivered, delivered,
				"delivered count mismatch")

			// Verify deliveryStartPoint wraps correctly
			gotStartPoint := uint32(s.deliveryStartPoint.Load())
			expectedWrapped := circular.SeqAdd(tc.ISN, uint32(tc.ExpectedDelivered))
			require.Equal(t, expectedWrapped, gotStartPoint,
				"deliveryStartPoint should wrap correctly")
		})
	}
}

// TestSender_Wraparound_ACK tests ACK processing across wraparound
func TestSender_Wraparound_ACK(t *testing.T) {
	testCases := []struct {
		Name                 string
		ISN                  uint32
		NumPackets           int
		ACKSeq               uint32
		ExpectedRemainingLen int
	}{
		{
			Name:                 "ACK_NoWrap",
			ISN:                  0,
			NumPackets:           10,
			ACKSeq:               5,
			ExpectedRemainingLen: 5, // 5,6,7,8,9
		},
		{
			Name:                 "ACK_Wrap_Before",
			ISN:                  MaxSeq31Bit - 5,
			NumPackets:           10,
			ACKSeq:               MaxSeq31Bit, // ACK up to MAX (exclusive)
			ExpectedRemainingLen: 5,           // MAX, 0, 1, 2, 3
		},
		{
			Name:                 "ACK_Wrap_After",
			ISN:                  MaxSeq31Bit - 5,
			NumPackets:           10,
			ACKSeq:               2, // ACK including wrapped 0, 1
			ExpectedRemainingLen: 2, // 2, 3
		},
		{
			Name:                 "ACK_All_Wrap",
			ISN:                  MaxSeq31Bit - 5,
			NumPackets:           10,
			ACKSeq:               4, // ACK all
			ExpectedRemainingLen: 0,
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

			// Insert packets
			for i := 0; i < tc.NumPackets; i++ {
				seq := circular.SeqAdd(tc.ISN, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
				s.packetBtree.Insert(pkt)
			}

			// Process ACK
			s.ackBtree(circular.New(tc.ACKSeq, packet.MAX_SEQUENCENUMBER))

			// Verify remaining
			require.Equal(t, tc.ExpectedRemainingLen, s.packetBtree.Len(),
				"remaining packets after ACK(%d) mismatch", tc.ACKSeq)
		})
	}
}

// TestSender_Wraparound_SeqAdd verifies circular.SeqAdd behavior
func TestSender_Wraparound_SeqAdd(t *testing.T) {
	testCases := []struct {
		Base     uint32
		Add      uint32
		Expected uint32
	}{
		{0, 1, 1},
		{0, 10, 10},
		{MaxSeq31Bit, 1, 0},                       // Wrap
		{MaxSeq31Bit, 2, 1},                       // Wrap
		{MaxSeq31Bit - 1, 2, 0},                   // Wrap
		{MaxSeq31Bit - 5, 10, 4},                  // Wrap
		{1000000000, 1000000000, 2000000000},      // Large, no wrap
		{2000000000, 500000000, 352516352},        // Wraps
	}

	for _, tc := range testCases {
		result := circular.SeqAdd(tc.Base, tc.Add)
		require.Equal(t, tc.Expected, result,
			"SeqAdd(%d, %d) = %d, expected %d", tc.Base, tc.Add, result, tc.Expected)
	}
}

// TestSender_Wraparound_CircularComparison verifies circular.SeqLess behavior
func TestSender_Wraparound_CircularComparison(t *testing.T) {
	testCases := []struct {
		A        uint32
		B        uint32
		Expected bool
	}{
		{0, 1, true},
		{1, 0, false},
		{MaxSeq31Bit, 0, true},  // MAX < 0 in circular space (MAX is "before" 0)
		{0, MaxSeq31Bit, false}, // 0 > MAX in circular space
		{MaxSeq31Bit - 1, MaxSeq31Bit, true},
		{MaxSeq31Bit, MaxSeq31Bit - 1, false},
		{100, 100, false}, // Equal
	}

	for _, tc := range testCases {
		result := circular.SeqLess(tc.A, tc.B)
		require.Equal(t, tc.Expected, result,
			"SeqLess(%d, %d) = %v, expected %v", tc.A, tc.B, result, tc.Expected)
	}
}

