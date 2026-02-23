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
// Table-driven tests for TSBPD (Timestamp-Based Packet Delivery)
//
// TSBPD is the core timing mechanism for SRT packet delivery:
// - PktTsbpdTime is set when packet is created (relative to connection start)
// - EventLoop's nowFn returns relative time (also since connection start)
// - Packets are delivered when nowUs >= PktTsbpdTime
// - Packets are dropped when PktTsbpdTime < (nowUs - dropThreshold)
//
// Reference: lockless_sender_design.md Section 5.2 "TSBPD Timeline"
// ============================================================================

// TsbpdDeliveryTestCase tests TSBPD delivery timing
type TsbpdDeliveryTestCase struct {
	Name string

	// Setup
	ISN           uint32
	PacketTsbpd   []uint64 // TSBPD times for packets
	NowUs         uint64   // Current time

	// Expected
	ExpectedDelivered int
	ExpectedNextUs    int64 // -1 means no next, 0+ means µs until next
}

var tsbpdDeliveryTestCases = []TsbpdDeliveryTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Exact Boundary Tests
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "Exact_Boundary_Ready",
		ISN:               0,
		PacketTsbpd:       []uint64{1_000_000},
		NowUs:             1_000_000, // Exactly at TSBPD time
		ExpectedDelivered: 1,         // Should deliver (<=)
		ExpectedNextUs:    -1,
	},
	{
		Name:              "Exact_Boundary_Just_Before",
		ISN:               0,
		PacketTsbpd:       []uint64{1_000_000},
		NowUs:             999_999, // 1µs before TSBPD time
		ExpectedDelivered: 0,       // Not ready
		ExpectedNextUs:    1,       // 1µs until ready
	},
	{
		Name:              "Exact_Boundary_Just_After",
		ISN:               0,
		PacketTsbpd:       []uint64{1_000_000},
		NowUs:             1_000_001, // 1µs after TSBPD time
		ExpectedDelivered: 1,         // Should deliver
		ExpectedNextUs:    -1,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Multiple Packets - Partial Delivery
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "Multiple_First_Ready",
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200, 300, 400, 500},
		NowUs:             150,
		ExpectedDelivered: 1,  // Only first (100 <= 150)
		ExpectedNextUs:    50, // 200 - 150 = 50
	},
	{
		Name:              "Multiple_Half_Ready",
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200, 300, 400, 500},
		NowUs:             300,
		ExpectedDelivered: 3,   // 100, 200, 300 all <= 300
		ExpectedNextUs:    100, // 400 - 300 = 100
	},
	{
		Name:              "Multiple_All_Ready",
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200, 300, 400, 500},
		NowUs:             1000,
		ExpectedDelivered: 5, // All ready
		ExpectedNextUs:    -1,
	},
	{
		Name:              "Multiple_None_Ready",
		ISN:               0,
		PacketTsbpd:       []uint64{1000, 2000, 3000},
		NowUs:             500,
		ExpectedDelivered: 0,
		ExpectedNextUs:    500, // 1000 - 500 = 500
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Large Time Values
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "Large_Time_32Seconds",
		ISN:               0,
		PacketTsbpd:       []uint64{32_000_000}, // 32 seconds in µs
		NowUs:             32_000_001,
		ExpectedDelivered: 1,
		ExpectedNextUs:    -1,
	},
	{
		Name:              "Large_Time_Near_Ready",
		ISN:               0,
		PacketTsbpd:       []uint64{32_000_000},
		NowUs:             31_999_000, // 1ms before
		ExpectedDelivered: 0,
		ExpectedNextUs:    1000, // 1ms
	},

	// ═══════════════════════════════════════════════════════════════════════
	// High ISN with TSBPD (Regression test for the bug)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "High_ISN_549M_TSBPD",
		ISN:               549144712,
		PacketTsbpd:       []uint64{100, 200, 300},
		NowUs:             1_000_000,
		ExpectedDelivered: 3,
		ExpectedNextUs:    -1,
	},
	{
		Name:              "High_ISN_Partial_Ready",
		ISN:               879502527,
		PacketTsbpd:       []uint64{100_000, 200_000, 300_000},
		NowUs:             150_000,
		ExpectedDelivered: 1,
		ExpectedNextUs:    50_000, // 200K - 150K
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Zero Time Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "Zero_TSBPD_Ready",
		ISN:               0,
		PacketTsbpd:       []uint64{0, 0, 0}, // All packets at time 0
		NowUs:             0,
		ExpectedDelivered: 3, // All should be ready (0 <= 0)
		ExpectedNextUs:    -1,
	},
	{
		Name:              "Zero_NowUs_Not_Ready",
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200},
		NowUs:             0,
		ExpectedDelivered: 0,
		ExpectedNextUs:    100,
	},
}

// TestSender_TSBPD_Delivery_Table tests TSBPD delivery timing
func TestSender_TSBPD_Delivery_Table(t *testing.T) {
	for _, tc := range tsbpdDeliveryTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			var deliveredCount atomic.Int32

			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver:             func(p packet.Packet) { deliveredCount.Add(1) },
				StartTime:             time.Now(),
				UseBtree:              true,
				UseSendRing:           true,
				UseSendControlRing:    true,
				UseSendEventLoop:      true,
				SendDropThresholdUs:   10_000_000, // 10 seconds (won't drop)
			}).(*sender)

			// Override nowFn
			s.nowFn = func() uint64 { return tc.NowUs }

			// Insert packets with specified TSBPD times
			for i, tsbpd := range tc.PacketTsbpd {
				seq := circular.SeqAdd(tc.ISN, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, tsbpd)
				s.packetBtree.Insert(pkt)
			}

			// Run delivery (with EventLoop context)
			var delivered int
			var nextDeliveryIn time.Duration
			runInEventLoopContext(s, func() {
				delivered, nextDeliveryIn = s.deliverReadyPacketsEventLoop(tc.NowUs)
			})

			// Verify delivered count
			require.Equal(t, tc.ExpectedDelivered, delivered,
				"delivered count mismatch")

			// Verify next delivery time
			if tc.ExpectedNextUs == -1 {
				// Should be 0 or max (all delivered or empty)
				require.True(t, nextDeliveryIn == 0 || nextDeliveryIn >= time.Millisecond,
					"expected 0 or max sleep, got %v", nextDeliveryIn)
			} else {
				// Allow some tolerance due to sleep factor
				expected := time.Duration(tc.ExpectedNextUs) * time.Microsecond
				require.InDelta(t, expected.Microseconds(), nextDeliveryIn.Microseconds(),
					float64(expected.Microseconds())*0.2+1000,
					"nextDeliveryIn mismatch")
			}
		})
	}
}

// TsbpdDropTestCase tests TSBPD drop threshold
type TsbpdDropTestCase struct {
	Name string

	// Setup
	ISN             uint32
	PacketTsbpd     []uint64
	NowUs           uint64
	DropThresholdUs uint64

	// Expected
	ExpectedDropped int
}

var tsbpdDropTestCases = []TsbpdDropTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// No Drop Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:            "NoDrops_All_Fresh",
		ISN:             0,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           500,
		DropThresholdUs: 1_000_000, // 1 second
		ExpectedDropped: 0,         // All within threshold
	},
	{
		Name:            "NoDrops_Early_Connection",
		ISN:             0,
		PacketTsbpd:     []uint64{0, 100, 200},
		NowUs:           500_000, // 500ms into connection
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 0, // Underflow protection
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Drop Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:            "Drop_All_Old",
		ISN:             0,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           2_000_000, // 2 seconds
		DropThresholdUs: 1_000_000, // 1 second threshold
		ExpectedDropped: 3,         // All packets are > 1 second old
	},
	{
		Name:            "Drop_Some_Old",
		ISN:             0,
		PacketTsbpd:     []uint64{100, 500_000, 1_500_000, 1_900_000},
		NowUs:           2_000_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 2, // First two are > 1 second old (100, 500K < threshold=1M)
	},
	{
		Name:            "Drop_Exact_Threshold",
		ISN:             0,
		PacketTsbpd:     []uint64{1_000_000}, // Exactly at threshold
		NowUs:           2_000_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 1, // Exactly at threshold DOES drop (uses <= comparison)
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Underflow Protection (the bug we fixed!)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:            "Underflow_Protection_NowUs_Small",
		ISN:             0,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           500_000,   // 500ms (less than dropThreshold)
		DropThresholdUs: 1_000_000, // 1 second
		ExpectedDropped: 0,         // Should NOT drop - underflow protection
	},
	{
		Name:            "Underflow_Protection_NowUs_Zero",
		ISN:             0,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           0,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 0, // Should NOT drop - underflow protection
	},

	// ═══════════════════════════════════════════════════════════════════════
	// High ISN Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:            "High_ISN_No_Drop",
		ISN:             549144712,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           500_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 0,
	},
	{
		Name:            "High_ISN_Drop_All",
		ISN:             879502527,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           2_000_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 3,
	},
}

// TestSender_TSBPD_Drop_Table tests TSBPD drop threshold
func TestSender_TSBPD_Drop_Table(t *testing.T) {
	for _, tc := range tsbpdDropTestCases {
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
				SendDropThresholdUs:   tc.DropThresholdUs,
				DropThreshold:         tc.DropThresholdUs,
			}).(*sender)

			// Override nowFn
			s.nowFn = func() uint64 { return tc.NowUs }

			// Insert packets
			for i, tsbpd := range tc.PacketTsbpd {
				seq := circular.SeqAdd(tc.ISN, uint32(i))
				pkt := createTestPacketWithTsbpd(seq, tsbpd)
				s.packetBtree.Insert(pkt)
			}

			initialLen := s.packetBtree.Len()

			// Run drop logic (with EventLoop context)
			runInEventLoopContext(s, func() {
				s.dropOldPacketsEventLoop(tc.NowUs)
			})

			// Calculate dropped
			dropped := initialLen - s.packetBtree.Len()

			require.Equal(t, tc.ExpectedDropped, dropped,
				"dropped count mismatch")
		})
	}
}

// TestSender_TSBPD_TimeBase_Consistency verifies nowFn and PktTsbpdTime use same time base
func TestSender_TSBPD_TimeBase_Consistency(t *testing.T) {
	// This test verifies the critical requirement:
	// Both nowFn and PktTsbpdTime must use the same relative time base
	// (time since connection start in microseconds)

	startTime := time.Now()
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             startTime,
		UseBtree:              true,
		UseSendRing:           true,
		UseSendControlRing:    true,
		UseSendEventLoop:      true,
	}).(*sender)

	// Wait a bit
	time.Sleep(50 * time.Millisecond)

	// Get nowFn value
	nowUs := s.nowFn()

	// Should be approximately 50ms = 50,000 µs
	require.Greater(t, nowUs, uint64(30_000), "nowFn should return > 30ms")
	require.Less(t, nowUs, uint64(100_000), "nowFn should return < 100ms")

	// Critical: nowFn should NOT return absolute time (which would be ~1.7e15 in 2026)
	require.Less(t, nowUs, uint64(1_000_000_000_000),
		"nowFn must return RELATIVE time, not absolute time")
}

// TestSender_TSBPD_Calculate_DropThreshold tests calculateDropThreshold helper
func TestSender_TSBPD_Calculate_DropThreshold(t *testing.T) {
	// These tests are also in drop_threshold_test.go, but included here for completeness

	testCases := []struct {
		Name          string
		NowUs         uint64
		DropThreshold uint64
		ShouldDrop    bool
		Expected      uint64
	}{
		{"Normal", 2_000_000, 1_000_000, true, 1_000_000},
		{"Underflow_Protection", 500_000, 1_000_000, false, 0},
		{"Zero_NowUs", 0, 1_000_000, false, 0},
		{"Zero_Threshold", 1_000_000, 0, true, 1_000_000}, // 0 threshold means nowUs - 0 = nowUs
		{"Exact_Boundary", 1_000_000, 1_000_000, true, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			threshold, shouldDrop := calculateDropThreshold(tc.NowUs, tc.DropThreshold)

			require.Equal(t, tc.ShouldDrop, shouldDrop, "shouldDrop mismatch")
			if shouldDrop {
				require.Equal(t, tc.Expected, threshold, "threshold mismatch")
			}
		})
	}
}

