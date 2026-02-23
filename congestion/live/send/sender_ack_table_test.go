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
// Table-driven tests for ACK processing
//
// When an ACK is received, the sender:
// 1. Removes all packets with sequence < ACK from btree (they've been received)
// 2. Updates lastACKedSequence tracking
// 3. Updates contiguousPoint if applicable
//
// Reference: lockless_sender_design.md Section 5.3 "ACK Processing"
// ============================================================================

// ACKTestCase defines a test case for ACK processing
type ACKTestCase struct {
	Name string

	// Setup
	ISN           uint32
	PacketSeqs    []uint32 // Sequence numbers in btree (as offsets from ISN)
	ACKSeq        uint32   // ACK sequence number (absolute)
	PrevLastACKed uint32   // Previous lastACKedSequence

	// Expected
	ExpectedRemaining  int    // Packets remaining after ACK
	ExpectedLastACKed  uint32 // New lastACKedSequence
	ExpectedMinSeq     uint32 // Minimum sequence in btree after ACK (0 if empty)
}

var ackTestCases = []ACKTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Basic ACK Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "ACK_All_Contiguous",
		ISN:               0,
		PacketSeqs:        []uint32{0, 1, 2, 3, 4},
		ACKSeq:            5,
		PrevLastACKed:     0,
		ExpectedRemaining: 0, // All removed
		ExpectedLastACKed: 5,
		ExpectedMinSeq:    0,
	},
	{
		Name:              "ACK_Partial_Half",
		ISN:               0,
		PacketSeqs:        []uint32{0, 1, 2, 3, 4},
		ACKSeq:            3,
		PrevLastACKed:     0,
		ExpectedRemaining: 2, // 3, 4 remain
		ExpectedLastACKed: 3,
		ExpectedMinSeq:    3,
	},
	{
		Name:              "ACK_First_Only",
		ISN:               0,
		PacketSeqs:        []uint32{0, 1, 2, 3, 4},
		ACKSeq:            1,
		PrevLastACKed:     0,
		ExpectedRemaining: 4, // 1, 2, 3, 4 remain
		ExpectedLastACKed: 1,
		ExpectedMinSeq:    1,
	},
	{
		Name:              "ACK_None_Below",
		ISN:               100,
		PacketSeqs:        []uint32{0, 1, 2}, // 100, 101, 102
		ACKSeq:            50,                // Before all packets
		PrevLastACKed:     0,
		ExpectedRemaining: 3, // All remain
		ExpectedLastACKed: 50,
		ExpectedMinSeq:    100,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// High ISN Cases (Regression test for initialization bug)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "ACK_High_ISN_549M",
		ISN:               549144712,
		PacketSeqs:        []uint32{0, 1, 2, 3, 4}, // 549144712-549144716
		ACKSeq:            549144715,
		PrevLastACKed:     549144712,
		ExpectedRemaining: 2, // 549144715, 549144716 remain
		ExpectedLastACKed: 549144715,
		ExpectedMinSeq:    549144715,
	},
	{
		Name:              "ACK_High_ISN_All",
		ISN:               879502527,
		PacketSeqs:        []uint32{0, 1, 2},
		ACKSeq:            879502530,
		PrevLastACKed:     879502527,
		ExpectedRemaining: 0,
		ExpectedLastACKed: 879502530,
		ExpectedMinSeq:    0,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Wraparound Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "ACK_Wrap_Before_Max",
		ISN:               MaxSeq31Bit - 5,
		PacketSeqs:        []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, // MAX-5 to MAX+4 (wraps)
		ACKSeq:            MaxSeq31Bit,                            // ACK up to MAX
		PrevLastACKed:     MaxSeq31Bit - 5,
		ExpectedRemaining: 5, // MAX, 0, 1, 2, 3 remain
		ExpectedLastACKed: MaxSeq31Bit,
		ExpectedMinSeq:    MaxSeq31Bit,
	},
	{
		Name:              "ACK_Wrap_After_Max",
		ISN:               MaxSeq31Bit - 5,
		PacketSeqs:        []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		ACKSeq:            2, // ACK including wrapped 0, 1
		PrevLastACKed:     MaxSeq31Bit,
		ExpectedRemaining: 2, // 2, 3 remain
		ExpectedLastACKed: 2,
		ExpectedMinSeq:    2,
	},
	{
		Name:              "ACK_Wrap_All",
		ISN:               MaxSeq31Bit - 2,
		PacketSeqs:        []uint32{0, 1, 2, 3, 4}, // MAX-2 to MAX+2
		ACKSeq:            3,                       // ACK all (wrapped)
		PrevLastACKed:     MaxSeq31Bit - 2,
		ExpectedRemaining: 0,
		ExpectedLastACKed: 3,
		ExpectedMinSeq:    0,
	},
	{
		Name:              "ACK_At_Max",
		ISN:               MaxSeq31Bit,
		PacketSeqs:        []uint32{0, 1, 2}, // MAX, 0, 1
		ACKSeq:            1,                 // ACK up to 1
		PrevLastACKed:     MaxSeq31Bit,
		ExpectedRemaining: 1, // 1 remains
		ExpectedLastACKed: 1,
		ExpectedMinSeq:    1,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Edge Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "ACK_Empty_Btree",
		ISN:               0,
		PacketSeqs:        []uint32{},
		ACKSeq:            10,
		PrevLastACKed:     0,
		ExpectedRemaining: 0,
		ExpectedLastACKed: 10,
		ExpectedMinSeq:    0,
	},
	{
		Name:              "ACK_Single_Packet_Remove",
		ISN:               1000,
		PacketSeqs:        []uint32{0},
		ACKSeq:            1001,
		PrevLastACKed:     1000,
		ExpectedRemaining: 0,
		ExpectedLastACKed: 1001,
		ExpectedMinSeq:    0,
	},
	{
		Name:              "ACK_Single_Packet_Keep",
		ISN:               1000,
		PacketSeqs:        []uint32{0},
		ACKSeq:            1000, // Exactly at packet, but < removes
		PrevLastACKed:     999,
		ExpectedRemaining: 1, // Packet at 1000 stays (ACK is exclusive)
		ExpectedLastACKed: 1000,
		ExpectedMinSeq:    1000,
	},
	{
		Name:              "ACK_Large_Gap",
		ISN:               0,
		PacketSeqs:        []uint32{0, 1, 2, 100, 101, 102}, // Gap: 3-99 missing
		ACKSeq:            50,
		PrevLastACKed:     0,
		ExpectedRemaining: 3, // 100, 101, 102 remain (0,1,2 all < 50 removed)
		ExpectedLastACKed: 50,
		ExpectedMinSeq:    100, // First remaining is 100
	},
}

// TestSender_ACK_Table runs all ACK test cases
func TestSender_ACK_Table(t *testing.T) {
	for _, tc := range ackTestCases {
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

			// Set previous lastACKedSequence
			s.lastACKedSequence = circular.New(tc.PrevLastACKed, packet.MAX_SEQUENCENUMBER)

			// Insert packets
			for _, seqOffset := range tc.PacketSeqs {
				seq := circular.SeqAdd(tc.ISN, seqOffset)
				pkt := createTestPacketWithTsbpd(seq, uint64(seqOffset)*100)
				s.packetBtree.Insert(pkt)
			}

			// Process ACK
			s.ackBtree(circular.New(tc.ACKSeq, packet.MAX_SEQUENCENUMBER))

			// Verify remaining count
			require.Equal(t, tc.ExpectedRemaining, s.packetBtree.Len(),
				"remaining packet count mismatch")

			// Verify lastACKedSequence updated
			require.Equal(t, tc.ExpectedLastACKed, s.lastACKedSequence.Val(),
				"lastACKedSequence mismatch")

			// Verify minimum sequence (if any remain)
			if tc.ExpectedRemaining > 0 {
				minPkt := s.packetBtree.Min()
				require.NotNil(t, minPkt)
				require.Equal(t, tc.ExpectedMinSeq, minPkt.Header().PacketSequenceNumber.Val(),
					"minimum sequence after ACK mismatch")
			}
		})
	}
}

// TestSender_ACK_Idempotent tests that duplicate ACKs are handled correctly
func TestSender_ACK_Idempotent(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           true,
		UseSendControlRing:    true,
		UseSendEventLoop:      true,
	}).(*sender)

	// Insert packets
	for i := uint32(0); i < 10; i++ {
		pkt := createTestPacketWithTsbpd(i, uint64(i)*100)
		s.packetBtree.Insert(pkt)
	}

	// First ACK
	s.ackBtree(circular.New(5, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 5, s.packetBtree.Len(), "after first ACK")

	// Duplicate ACK (same sequence)
	s.ackBtree(circular.New(5, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 5, s.packetBtree.Len(), "after duplicate ACK - should be idempotent")

	// Older ACK (should be ignored or have no effect)
	s.ackBtree(circular.New(3, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 5, s.packetBtree.Len(), "after older ACK - no additional removal")
}

// TestSender_ACK_Progressive tests progressive ACK advancement
func TestSender_ACK_Progressive(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           true,
		UseSendControlRing:    true,
		UseSendEventLoop:      true,
	}).(*sender)

	// Insert 20 packets
	for i := uint32(0); i < 20; i++ {
		pkt := createTestPacketWithTsbpd(i, uint64(i)*100)
		s.packetBtree.Insert(pkt)
	}

	// Progressive ACKs
	testSteps := []struct {
		ACK              uint32
		ExpectedRemaining int
	}{
		{5, 15},   // Remove 0-4
		{10, 10},  // Remove 5-9
		{15, 5},   // Remove 10-14
		{20, 0},   // Remove 15-19
	}

	for _, step := range testSteps {
		s.ackBtree(circular.New(step.ACK, packet.MAX_SEQUENCENUMBER))
		require.Equal(t, step.ExpectedRemaining, s.packetBtree.Len(),
			"after ACK(%d)", step.ACK)
	}
}

// TestSender_ACK_WithGaps tests ACK with non-contiguous packets
func TestSender_ACK_WithGaps(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           true,
		UseSendControlRing:    true,
		UseSendEventLoop:      true,
	}).(*sender)

	// Insert packets with gaps: 0, 1, 2, 10, 11, 12, 20, 21, 22
	seqs := []uint32{0, 1, 2, 10, 11, 12, 20, 21, 22}
	for _, seq := range seqs {
		pkt := createTestPacketWithTsbpd(seq, uint64(seq)*100)
		s.packetBtree.Insert(pkt)
	}

	// ACK at 5 (between gaps)
	s.ackBtree(circular.New(5, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 6, s.packetBtree.Len(), "after ACK(5): 10,11,12,20,21,22 remain")

	// ACK at 15 (in next gap)
	s.ackBtree(circular.New(15, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 3, s.packetBtree.Len(), "after ACK(15): 20,21,22 remain")
}

// TestSender_ACK_Wraparound_Progressive tests ACK across wraparound boundary
func TestSender_ACK_Wraparound_Progressive(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(MaxSeq31Bit-5, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           true,
		UseSendControlRing:    true,
		UseSendEventLoop:      true,
	}).(*sender)

	// Insert packets: MAX-5 to MAX+4 (wraps around)
	for i := uint32(0); i < 10; i++ {
		seq := circular.SeqAdd(MaxSeq31Bit-5, i)
		pkt := createTestPacketWithTsbpd(seq, uint64(i)*100)
		s.packetBtree.Insert(pkt)
	}

	require.Equal(t, 10, s.packetBtree.Len(), "initial")

	// ACK before wrap
	s.ackBtree(circular.New(MaxSeq31Bit-2, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 7, s.packetBtree.Len(), "after ACK before wrap")

	// ACK at MAX
	s.ackBtree(circular.New(MaxSeq31Bit, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 5, s.packetBtree.Len(), "after ACK at MAX")

	// ACK after wrap
	s.ackBtree(circular.New(2, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 2, s.packetBtree.Len(), "after ACK past wrap")

	// ACK all
	s.ackBtree(circular.New(4, packet.MAX_SEQUENCENUMBER))
	require.Equal(t, 0, s.packetBtree.Len(), "after final ACK")
}

