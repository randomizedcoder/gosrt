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
// Table-driven tests for sender initialization
//
// CRITICAL BUG DISCOVERED: deliveryStartPoint was never initialized!
// - It defaulted to 0 (atomic.Uint64 zero value)
// - But nextSequenceNumber was set to ISN (~549M random value)
// - This caused IterateFrom(0) to fail finding packets at ~549M
// - Result: 60% failure rate in integration tests
//
// Reference: send_eventloop_intermittent_failure_bug.md
// Comparison: receiver.go:345 correctly initializes contiguousPoint to ISN-1
// ============================================================================

// SenderInitTestCase defines a test case for sender initialization
type SenderInitTestCase struct {
	Name string

	// Configuration
	InitialSequenceNumber uint32
	UseBtree              bool
	UseRing               bool
	UseControlRing        bool
	UseEventLoop          bool
	DropThresholdUs       uint64

	// Expected initialization state
	ExpectedNextSeq         uint32
	ExpectedDeliveryStart   uint32 // CRITICAL: Must match ISN
	ExpectedContiguousPoint uint32
	ExpectedUseBtree        bool
	ExpectedUseEventLoop    bool
}

var senderInitTestCases = []SenderInitTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Basic ISN Values - Must initialize deliveryStartPoint correctly
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                    "ISN_Zero_AllEnabled",
		InitialSequenceNumber:   0,
		UseBtree:                true,
		UseRing:                 true,
		UseControlRing:          true,
		UseEventLoop:            true,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         0,
		ExpectedDeliveryStart:   0, // Must be ISN, not default
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    true,
	},
	{
		Name:                    "ISN_Small_1000",
		InitialSequenceNumber:   1000,
		UseBtree:                true,
		UseRing:                 true,
		UseControlRing:          true,
		UseEventLoop:            true,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         1000,
		ExpectedDeliveryStart:   1000, // CRITICAL: Must be 1000, not 0!
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    true,
	},
	{
		Name:                    "ISN_Random_549M", // THE FAILING CASE!
		InitialSequenceNumber:   549144712,
		UseBtree:                true,
		UseRing:                 true,
		UseControlRing:          true,
		UseEventLoop:            true,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         549144712,
		ExpectedDeliveryStart:   549144712, // MUST NOT BE 0!
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    true,
	},
	{
		Name:                    "ISN_Random_879M", // From actual passing test
		InitialSequenceNumber:   879502527,
		UseBtree:                true,
		UseRing:                 true,
		UseControlRing:          true,
		UseEventLoop:            true,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         879502527,
		ExpectedDeliveryStart:   879502527,
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    true,
	},
	{
		Name:                    "ISN_NearMax",
		InitialSequenceNumber:   2147483640, // MAX - 7
		UseBtree:                true,
		UseRing:                 true,
		UseControlRing:          true,
		UseEventLoop:            true,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         2147483640,
		ExpectedDeliveryStart:   2147483640,
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    true,
	},
	{
		Name:                    "ISN_AtMax",
		InitialSequenceNumber:   2147483647, // MAX (31-bit)
		UseBtree:                true,
		UseRing:                 true,
		UseControlRing:          true,
		UseEventLoop:            true,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         2147483647,
		ExpectedDeliveryStart:   2147483647,
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    true,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Legacy Mode Tests - deliveryStartPoint should still be initialized
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                    "Legacy_NoEventLoop",
		InitialSequenceNumber:   5000,
		UseBtree:                false,
		UseRing:                 false,
		UseControlRing:          false,
		UseEventLoop:            false,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         5000,
		ExpectedDeliveryStart:   5000, // Still should be initialized
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        false,
		ExpectedUseEventLoop:    false,
	},
	{
		Name:                    "BtreeOnly_NoRing",
		InitialSequenceNumber:   10000,
		UseBtree:                true,
		UseRing:                 false,
		UseControlRing:          false,
		UseEventLoop:            false,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         10000,
		ExpectedDeliveryStart:   10000,
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    false,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Wraparound Edge Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                    "ISN_One_Before_Max",
		InitialSequenceNumber:   2147483646, // MAX - 1
		UseBtree:                true,
		UseRing:                 true,
		UseControlRing:          true,
		UseEventLoop:            true,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         2147483646,
		ExpectedDeliveryStart:   2147483646,
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    true,
	},
	{
		Name:                    "ISN_HalfMax",
		InitialSequenceNumber:   1073741823, // MAX / 2
		UseBtree:                true,
		UseRing:                 true,
		UseControlRing:          true,
		UseEventLoop:            true,
		DropThresholdUs:         1_000_000,
		ExpectedNextSeq:         1073741823,
		ExpectedDeliveryStart:   1073741823,
		ExpectedContiguousPoint: 0,
		ExpectedUseBtree:        true,
		ExpectedUseEventLoop:    true,
	},
}

// TestSender_Init_Table runs all initialization test cases
func TestSender_Init_Table(t *testing.T) {
	for _, tc := range senderInitTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			// Create sender with test configuration
			m := &metrics.ConnectionMetrics{}
			start := time.Now()

			config := SendConfig{
				InitialSequenceNumber: circular.New(tc.InitialSequenceNumber, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver:             func(p packet.Packet) {},
				StartTime:             start,
				DropThreshold:         tc.DropThresholdUs,
				// Phase 1
				UseBtree:    tc.UseBtree,
				BtreeDegree: 32,
				// Phase 2
				UseSendRing:    tc.UseRing,
				SendRingSize:   1024,
				SendRingShards: 1,
				// Phase 3
				UseSendControlRing:    tc.UseControlRing,
				SendControlRingSize:   256,
				SendControlRingShards: 2,
				// Phase 4
				UseSendEventLoop:             tc.UseEventLoop,
				SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
				SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
				SendTsbpdSleepFactor:         0.9,
				SendDropThresholdUs:          tc.DropThresholdUs,
			}

			s := NewSender(config).(*sender)

			// ═══════════════════════════════════════════════════════════════
			// CRITICAL ASSERTIONS - These catch the bug!
			// ═══════════════════════════════════════════════════════════════

			// Test 1: nextSequenceNumber must be ISN
			require.Equal(t, tc.ExpectedNextSeq, s.nextSequenceNumber.Val(),
				"nextSequenceNumber must be initialized to ISN")

			// Test 2: deliveryStartPoint must be ISN (THE BUG!)
			gotDeliveryStart := s.deliveryStartPoint.Load()
			require.Equal(t, uint64(tc.ExpectedDeliveryStart), gotDeliveryStart,
				"CRITICAL: deliveryStartPoint must be initialized to ISN, not 0! "+
					"This bug caused 60% failure rate in integration tests. "+
					"See send_eventloop_intermittent_failure_bug.md")

			// Test 3: contiguousPoint should be 0 initially
			gotContiguous := s.contiguousPoint.Load()
			require.Equal(t, uint64(tc.ExpectedContiguousPoint), gotContiguous,
				"contiguousPoint should be 0 initially")

			// ═══════════════════════════════════════════════════════════════
			// Configuration State Assertions
			// ═══════════════════════════════════════════════════════════════

			// Test 4: useBtree flag
			require.Equal(t, tc.ExpectedUseBtree, s.useBtree,
				"useBtree flag mismatch")

			// Test 5: useEventLoop flag
			require.Equal(t, tc.ExpectedUseEventLoop, s.useEventLoop,
				"useEventLoop flag mismatch")

			// Test 6: btree created when enabled
			if tc.UseBtree {
				require.NotNil(t, s.packetBtree, "packetBtree should be created when useBtree=true")
			}

			// Test 7: ring created when enabled
			if tc.UseRing {
				require.NotNil(t, s.packetRing, "packetRing should be created when useRing=true")
			}

			// Test 8: control ring created when enabled
			// Note: controlRing != nil means enabled (no separate bool)
			if tc.UseControlRing {
				require.NotNil(t, s.controlRing, "controlRing should be non-nil when UseSendControlRing=true")
			}

			// Test 9: metrics attached
			require.NotNil(t, s.metrics, "metrics should be attached")
		})
	}
}

// TestSender_Init_DeliveryStartPoint_MustMatchISN is a focused test for the specific bug
func TestSender_Init_DeliveryStartPoint_MustMatchISN(t *testing.T) {
	// This test specifically catches the bug where deliveryStartPoint was 0
	// when ISN was ~549M, causing IterateFrom(0) to fail
	isnValues := []uint32{
		0,           // Trivial case
		1000,        // Small value
		549144712,   // THE FAILING CASE from integration tests
		879502527,   // From actual test metrics
		1073741823,  // Half max
		2147483640,  // Near max
		2147483647,  // Max
	}

	for _, isn := range isnValues {
		t.Run("ISN_"+formatISN(isn), func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(isn, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver:             func(p packet.Packet) {},
				StartTime:             time.Now(),
				UseBtree:              true,
				UseSendRing:           true,
				UseSendControlRing:    true,
				UseSendEventLoop:      true,
			}).(*sender)

			gotDeliveryStart := s.deliveryStartPoint.Load()

			require.Equal(t, uint64(isn), gotDeliveryStart,
				"BUG: deliveryStartPoint=%d but ISN=%d. "+
					"This mismatch caused 60% failure rate! "+
					"IterateFrom(0) cannot find packets at %d",
				gotDeliveryStart, isn, isn)
		})
	}
}

// TestSender_Init_NowFn_UsesRelativeTime verifies time base consistency
func TestSender_Init_NowFn_UsesRelativeTime(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	start := time.Now()

	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             start,
		UseBtree:              true,
		UseSendRing:           true,
		UseSendControlRing:    true, // Required for EventLoop
		UseSendEventLoop:      true,
	}).(*sender)

	// nowFn should return relative time (microseconds since start)
	time.Sleep(10 * time.Millisecond)
	nowUs := s.nowFn()

	// Should be roughly 10ms = 10,000 µs (allow some tolerance)
	require.Greater(t, nowUs, uint64(5_000), "nowFn should return > 5ms after 10ms sleep")
	require.Less(t, nowUs, uint64(100_000), "nowFn should return < 100ms (relative time)")

	// Critically, it should NOT be absolute time (~1.7e15 µs in 2026)
	require.Less(t, nowUs, uint64(1_000_000_000_000),
		"CRITICAL: nowFn must return relative time, not absolute time! "+
			"Absolute time (~1.7e15 µs) would cause all packets to be dropped as 'too old'")
}

// TestSender_Init_Btree_Empty verifies btree starts empty
func TestSender_Init_Btree_Empty(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(549144712, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           true,
		UseSendControlRing:    true, // Required for EventLoop
		UseSendEventLoop:      true,
	}).(*sender)

	require.Equal(t, 0, s.packetBtree.Len(), "btree should be empty at initialization")
	require.Nil(t, s.packetBtree.Min(), "btree.Min() should be nil when empty")
}

// TestSender_Init_Ring_Empty verifies ring starts empty
func TestSender_Init_Ring_Empty(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           true,
		SendRingSize:          1024,
		UseSendControlRing:    true, // Required for EventLoop
		UseSendEventLoop:      true,
	}).(*sender)

	require.Equal(t, 0, s.packetRing.Len(), "ring should be empty at initialization")
}

// TestSender_Init_CompareWithReceiver documents the expected pattern
// (receiver correctly initializes contiguousPoint)
func TestSender_Init_CompareWithReceiver(t *testing.T) {
	// This test documents the pattern from receiver.go:345:
	// r.contiguousPoint.Store(recvConfig.InitialSequenceNumber.Dec().Val())
	//
	// The sender should similarly initialize deliveryStartPoint to ISN
	// (not ISN-1 because delivery uses >= comparison, not >)

	isn := uint32(549144712)
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(isn, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           true,
		UseSendControlRing:    true, // Required for EventLoop
		UseSendEventLoop:      true,
	}).(*sender)

	// Sender uses: IterateFrom(deliveryStartPoint) which finds packets >= startPoint
	// So deliveryStartPoint should be ISN to find the first packet at ISN
	require.Equal(t, uint64(isn), s.deliveryStartPoint.Load(),
		"deliveryStartPoint should be ISN (not ISN-1) because IterateFrom uses >= comparison")

	// nextSequenceNumber should also be ISN
	require.Equal(t, isn, s.nextSequenceNumber.Val(),
		"nextSequenceNumber should be ISN")
}

// Helper function to format ISN for test names
func formatISN(isn uint32) string {
	switch {
	case isn == 0:
		return "0"
	case isn == 2147483647:
		return "Max"
	case isn > 1_000_000_000:
		return "Large"
	case isn > 1_000_000:
		return "Medium"
	default:
		return "Small"
	}
}

