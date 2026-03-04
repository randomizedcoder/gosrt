// Package live provides table-driven sender NAK tests.
//
// This file consolidates TestSendOriginal_* and TestSendHonorOrder_* tests
// into a unified table-driven approach that tests both NAK strategies.
//
// Key difference between strategies:
// - Original: Iterates lossList backwards, retransmits highest seq first
// - HonorOrder: Respects NAK list order, retransmits in receiver priority order
//
// See documentation/table_driven_test_design_implementation.md for progress.
package send

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// NAK STRATEGY ENUM
// ============================================================================

type NakStrategy int

const (
	StrategyOriginal   NakStrategy = iota // Backwards iteration (highest seq first)
	StrategyHonorOrder                    // Respects NAK list order
)

func (s NakStrategy) String() string {
	switch s {
	case StrategyOriginal:
		return "Original"
	case StrategyHonorOrder:
		return "HonorOrder"
	default:
		return "Unknown"
	}
}

// ============================================================================
// TEST CASE DEFINITION
// ============================================================================

// SendNakTestCase defines a single sender NAK test scenario.
type SendNakTestCase struct {
	Name string

	// CODE_PARAM: Maps to SendConfig.InitialSequenceNumber (critical for wraparound)
	StartSeq uint32 // Starting sequence number (default 0)

	TotalPackets int         // Packets to push to sender
	NakRanges    [][2]uint32 // NAK ranges as (start, end) pairs

	// Expected outcomes - differ by strategy
	ExpectedRetrans  int      // Expected retransmit count (same for both)
	ExpectedSeqOrig  []uint32 // Expected sequence order for Original
	ExpectedSeqHonor []uint32 // Expected sequence order for HonorOrder

	// Special test flags
	NotFoundTest   bool // If true, NAK contains seqs not in loss list
	SkipOriginal   bool // Skip Original strategy (e.g., metrics-only test)
	SkipHonorOrder bool // Skip HonorOrder strategy
}

// ============================================================================
// SENDER FACTORY
// ============================================================================

func createSenderForStrategy(strategy NakStrategy, startSeq uint32, onDeliver func(p packet.Packet)) *sender {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	config := SendConfig{
		InitialSequenceNumber: circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		DropThreshold:         100000, // High to prevent drops
		OnDeliver:             onDeliver,
		ConnectionMetrics:     testMetrics,
	}

	if strategy == StrategyHonorOrder {
		config.HonorNakOrder = true
	}

	return NewSender(config).(*sender)
}

// ============================================================================
// TEST RUNNER
// ============================================================================

func runSendNakTableTest(t *testing.T, tc SendNakTestCase, strategy NakStrategy) {
	t.Helper()

	// Skip if not applicable
	if strategy == StrategyOriginal && tc.SkipOriginal {
		t.Skip("Test not applicable to Original strategy")
	}
	if strategy == StrategyHonorOrder && tc.SkipHonorOrder {
		t.Skip("Test not applicable to HonorOrder strategy")
	}

	// Track retransmitted sequences
	var retransmittedSeqs []uint32
	send := createSenderForStrategy(strategy, tc.StartSeq, func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			retransmittedSeqs = append(retransmittedSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Push packets
	for i := 0; i < tc.TotalPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		send.Push(p)
	}

	// Move all to loss list
	send.Tick(uint64(tc.TotalPackets))

	// Build NAK list
	nakList := makeNakListFromPairsTable(tc.NakRanges)

	// Send NAK
	nRetrans := send.NAK(nakList)

	t.Logf("Test: %s [%s]", tc.Name, strategy)
	t.Logf("  Packets: %d, NAK ranges: %d, Retrans: %d", tc.TotalPackets, len(tc.NakRanges), nRetrans)

	// Verify retransmit count
	if tc.ExpectedRetrans > 0 {
		require.Equal(t, uint64(tc.ExpectedRetrans), nRetrans, "Unexpected retransmit count")
	}

	// Verify sequence order based on strategy
	var expectedSeqs []uint32
	if strategy == StrategyOriginal {
		expectedSeqs = tc.ExpectedSeqOrig
	} else {
		expectedSeqs = tc.ExpectedSeqHonor
	}

	if len(expectedSeqs) > 0 {
		require.Equal(t, expectedSeqs, retransmittedSeqs, "Unexpected retransmit sequence order")
	}

	t.Logf("  ✓ %s [%s] completed", tc.Name, strategy)
}

// Helper to build NAK list from pairs
func makeNakListFromPairsTable(pairs [][2]uint32) []circular.Number {
	list := make([]circular.Number, 0, len(pairs)*2)
	for _, p := range pairs {
		list = append(list, circular.New(p[0], packet.MAX_SEQUENCENUMBER))
		list = append(list, circular.New(p[1], packet.MAX_SEQUENCENUMBER))
	}
	return list
}

// ============================================================================
// TEST CASES
// ============================================================================

// SendNakTableTests defines all sender NAK test scenarios.
var SendNakTableTests = []SendNakTestCase{
	// === Basic Tests ===
	{
		Name:             "BasicSingle",
		TotalPackets:     10,
		NakRanges:        [][2]uint32{{5, 5}},
		ExpectedRetrans:  1,
		ExpectedSeqOrig:  []uint32{5},
		ExpectedSeqHonor: []uint32{5},
	},
	{
		Name:            "BasicRange",
		TotalPackets:    10,
		NakRanges:       [][2]uint32{{3, 6}},
		ExpectedRetrans: 4,
		// Original: backwards iteration returns 6,5,4,3
		ExpectedSeqOrig: []uint32{6, 5, 4, 3},
		// HonorOrder: forward iteration returns 3,4,5,6
		ExpectedSeqHonor: []uint32{3, 4, 5, 6},
	},

	// === Multiple Singles ===
	{
		Name:            "MultipleSingles",
		TotalPackets:    20,
		NakRanges:       [][2]uint32{{15, 15}, {5, 5}, {10, 10}},
		ExpectedRetrans: 3,
		// Original: processes ALL packets against ALL ranges, highest first
		// Loss list order is 0-19, backwards gives 19,18,...
		// First match: 15, then 10, then 5
		ExpectedSeqOrig: []uint32{15, 10, 5},
		// HonorOrder: respects NAK list order
		ExpectedSeqHonor: []uint32{15, 5, 10},
	},

	// === Multiple Ranges ===
	{
		Name:            "MultipleRanges",
		TotalPackets:    30,
		NakRanges:       [][2]uint32{{20, 22}, {5, 7}, {12, 14}},
		ExpectedRetrans: 9, // 3 + 3 + 3
		// Original: highest seq first within each range match
		ExpectedSeqOrig: []uint32{22, 21, 20, 14, 13, 12, 7, 6, 5},
		// HonorOrder: respects NAK list order
		ExpectedSeqHonor: []uint32{20, 21, 22, 5, 6, 7, 12, 13, 14},
	},

	// === Mixed Singles and Ranges ===
	{
		Name:            "MixedSinglesAndRanges",
		TotalPackets:    50,
		NakRanges:       [][2]uint32{{40, 40}, {10, 12}, {25, 25}, {30, 33}},
		ExpectedRetrans: 9, // 1 + 3 + 1 + 4 = 9
		// Original: backwards, grouped by first match
		ExpectedSeqOrig: []uint32{40, 33, 32, 31, 30, 25, 12, 11, 10},
		// HonorOrder: NAK list order
		ExpectedSeqHonor: []uint32{40, 10, 11, 12, 25, 30, 31, 32, 33},
	},

	// === Not Found Packets ===
	{
		Name:             "NotFoundPackets",
		TotalPackets:     10,
		NakRanges:        [][2]uint32{{5, 5}, {100, 100}, {7, 7}}, // 100 doesn't exist
		ExpectedRetrans:  2,                                       // Only 5 and 7 exist
		ExpectedSeqOrig:  []uint32{7, 5},
		ExpectedSeqHonor: []uint32{5, 7},
		NotFoundTest:     true,
	},

	// === Modulus Drops (Every Nth) ===
	{
		Name:         "ModulusDrops",
		TotalPackets: 100,
		// NAK every 10th: 10, 20, 30, 40, 50, 60, 70, 80, 90
		NakRanges: [][2]uint32{
			{10, 10}, {20, 20}, {30, 30}, {40, 40}, {50, 50},
			{60, 60}, {70, 70}, {80, 80}, {90, 90},
		},
		ExpectedRetrans:  9,
		ExpectedSeqOrig:  []uint32{90, 80, 70, 60, 50, 40, 30, 20, 10}, // Backwards
		ExpectedSeqHonor: []uint32{10, 20, 30, 40, 50, 60, 70, 80, 90}, // Forward
	},

	// === Burst Drops ===
	{
		Name:         "BurstDrops",
		TotalPackets: 100,
		// Bursts: 20-24, 50-54, 80-84
		NakRanges: [][2]uint32{
			{20, 24}, {50, 54}, {80, 84},
		},
		ExpectedRetrans: 15, // 5 + 5 + 5
		ExpectedSeqOrig: []uint32{
			84, 83, 82, 81, 80, // Third burst (highest first)
			54, 53, 52, 51, 50, // Second burst
			24, 23, 22, 21, 20, // First burst
		},
		ExpectedSeqHonor: []uint32{
			20, 21, 22, 23, 24, // First burst
			50, 51, 52, 53, 54, // Second burst
			80, 81, 82, 83, 84, // Third burst
		},
	},

	// === Realistic Consolidated NAK ===
	{
		Name:         "RealisticConsolidatedNAK",
		TotalPackets: 200,
		// Realistic pattern: one burst + scattered singles
		NakRanges: [][2]uint32{
			{45, 55},   // Burst of 11
			{100, 100}, // Single
			{150, 150}, // Single
			{180, 182}, // Small burst of 3
		},
		ExpectedRetrans: 16, // 11 + 1 + 1 + 3
		ExpectedSeqOrig: []uint32{
			182, 181, 180, 150, 100,
			55, 54, 53, 52, 51, 50, 49, 48, 47, 46, 45,
		},
		ExpectedSeqHonor: []uint32{
			45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55,
			100, 150, 180, 181, 182,
		},
	},

	// === Large Scale ===
	{
		Name:         "LargeScale",
		TotalPackets: 10000,
		// 1% loss = 100 singles
		NakRanges: func() [][2]uint32 {
			var ranges [][2]uint32
			for i := 100; i < 10000; i += 100 {
				ranges = append(ranges, [2]uint32{uint32(i), uint32(i)})
			}
			return ranges
		}(),
		ExpectedRetrans: 99, // 99 singles (100, 200, ..., 9900)
		// Don't verify exact order for large tests
	},

	// === CODE_PARAM Corner Cases: StartSeq and TotalPackets ===

	// Corner: StartSeq=0 (explicit baseline)
	{
		Name:             "Corner_StartSeq_Zero",
		StartSeq:         0, // Explicit zero
		TotalPackets:     10,
		NakRanges:        [][2]uint32{{3, 5}},
		ExpectedRetrans:  3,
		ExpectedSeqOrig:  []uint32{5, 4, 3},
		ExpectedSeqHonor: []uint32{3, 4, 5},
	},

	// Corner: TotalPackets=1 (minimum edge case)
	{
		Name:             "Corner_TotalPackets_Single",
		TotalPackets:     1,
		NakRanges:        [][2]uint32{{0, 0}},
		ExpectedRetrans:  1,
		ExpectedSeqOrig:  []uint32{0},
		ExpectedSeqHonor: []uint32{0},
	},

	// Corner: StartSeq wraparound tests
	{
		Name:             "Wraparound_NearMax",
		StartSeq:         packet.MAX_SEQUENCENUMBER - 50, // Start near MAX
		TotalPackets:     100,                            // Will wrap around
		NakRanges:        [][2]uint32{{packet.MAX_SEQUENCENUMBER - 45, packet.MAX_SEQUENCENUMBER - 45}},
		ExpectedRetrans:  1,
		ExpectedSeqOrig:  []uint32{packet.MAX_SEQUENCENUMBER - 45},
		ExpectedSeqHonor: []uint32{packet.MAX_SEQUENCENUMBER - 45},
	},
	{
		Name:             "Wraparound_AtMax",
		StartSeq:         packet.MAX_SEQUENCENUMBER - 5, // Very close to MAX
		TotalPackets:     20,                            // Will wrap around
		NakRanges:        [][2]uint32{{packet.MAX_SEQUENCENUMBER - 3, packet.MAX_SEQUENCENUMBER - 3}},
		ExpectedRetrans:  1,
		ExpectedSeqOrig:  []uint32{packet.MAX_SEQUENCENUMBER - 3},
		ExpectedSeqHonor: []uint32{packet.MAX_SEQUENCENUMBER - 3},
	},
	{
		Name:            "Wraparound_CrossingMax",
		StartSeq:        packet.MAX_SEQUENCENUMBER - 10,
		TotalPackets:    30, // Seqs: MAX-10 to MAX, then 0 to 18
		NakRanges:       [][2]uint32{{0, 2}, {packet.MAX_SEQUENCENUMBER - 5, packet.MAX_SEQUENCENUMBER - 3}},
		ExpectedRetrans: 6, // 3 + 3
		// Original: processes backwards within each range, ranges in reverse packet order
		ExpectedSeqOrig: []uint32{2, 1, 0, packet.MAX_SEQUENCENUMBER - 3, packet.MAX_SEQUENCENUMBER - 4, packet.MAX_SEQUENCENUMBER - 5},
		// HonorOrder: respects NAK list order
		ExpectedSeqHonor: []uint32{0, 1, 2, packet.MAX_SEQUENCENUMBER - 5, packet.MAX_SEQUENCENUMBER - 4, packet.MAX_SEQUENCENUMBER - 3},
	},
}

// TestSendNak_Table runs all sender NAK tests using table-driven approach.
// Tests run in parallel for faster execution.
func TestSendNak_Table(t *testing.T) {
	for _, tc := range SendNakTableTests {
		tc := tc // Capture range variable for parallel execution
		// Test both strategies
		t.Run(tc.Name+"/Original", func(t *testing.T) {
			t.Parallel() // Run test cases in parallel
			runSendNakTableTest(t, tc, StrategyOriginal)
		})
		t.Run(tc.Name+"/HonorOrder", func(t *testing.T) {
			t.Parallel() // Run test cases in parallel
			runSendNakTableTest(t, tc, StrategyHonorOrder)
		})
	}
}

// ============================================================================
// STRATEGY COMPARISON TEST
// ============================================================================

// TestSendNak_StrategyDifference explicitly shows the difference between strategies.
func TestSendNak_StrategyDifference(t *testing.T) {
	// This test verifies that Original and HonorOrder produce DIFFERENT orders
	// for the same NAK input when ranges are not in sequence order.

	tc := SendNakTestCase{
		TotalPackets: 30,
		NakRanges:    [][2]uint32{{20, 22}, {5, 7}, {12, 14}}, // Out of sequence order
	}

	var origSeqs, honorSeqs []uint32

	// Run Original
	sendOrig := createSenderForStrategy(StrategyOriginal, tc.StartSeq, func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			origSeqs = append(origSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	for i := 0; i < tc.TotalPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		sendOrig.Push(p)
	}
	sendOrig.Tick(uint64(tc.TotalPackets))
	sendOrig.NAK(makeNakListFromPairsTable(tc.NakRanges))

	// Run HonorOrder
	sendHonor := createSenderForStrategy(StrategyHonorOrder, tc.StartSeq, func(p packet.Packet) {
		if p.Header().RetransmittedPacketFlag {
			honorSeqs = append(honorSeqs, p.Header().PacketSequenceNumber.Val())
		}
	})
	for i := 0; i < tc.TotalPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PktTsbpdTime = uint64(i + 1)
		sendHonor.Push(p)
	}
	sendHonor.Tick(uint64(tc.TotalPackets))
	sendHonor.NAK(makeNakListFromPairsTable(tc.NakRanges))

	t.Logf("Original order:   %v", origSeqs)
	t.Logf("HonorOrder order: %v", honorSeqs)

	// Verify they're different
	require.NotEqual(t, origSeqs, honorSeqs, "Strategies should produce different orders")

	// Verify Original is backwards within ranges (highest first)
	require.Equal(t, []uint32{22, 21, 20, 14, 13, 12, 7, 6, 5}, origSeqs)

	// Verify HonorOrder respects NAK list order (ranges processed in order given)
	require.Equal(t, []uint32{20, 21, 22, 5, 6, 7, 12, 13, 14}, honorSeqs)
}

// ============================================================================
// TransmitCount Tests
//
// Verify NAK handler correctly increments TransmitCount on retransmit.
// Reference: sender_lockfree_architecture.md Section 7.9.4
// ============================================================================

// TestNAK_TransmitCount_Increment verifies NAK increments TransmitCount
func TestNAK_TransmitCount_Increment(t *testing.T) {
	testCases := []struct {
		name                    string
		initialTransmitCount    uint32
		expectedAfterRetransmit uint32
	}{
		{
			name:                    "TC_0_becomes_1",
			initialTransmitCount:    0, // Edge case: never first-sent
			expectedAfterRetransmit: 1,
		},
		{
			name:                    "TC_1_becomes_2",
			initialTransmitCount:    1, // Normal: first-sent, now retransmit
			expectedAfterRetransmit: 2,
		},
		{
			name:                    "TC_2_becomes_3",
			initialTransmitCount:    2, // Already retransmitted once
			expectedAfterRetransmit: 3,
		},
		{
			name:                    "TC_10_becomes_11",
			initialTransmitCount:    10, // Many retransmits
			expectedAfterRetransmit: 11,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			var retransmittedPkt packet.Packet

			// Create sender with list mode (simpler for this test)
			sendCfg := SendConfig{
				ConnectionMetrics:     m,
				InitialSequenceNumber: circular.New(100, packet.MAX_SEQUENCENUMBER),
				OnDeliver: func(p packet.Packet) {
					// Capture retransmitted packet
					if p.Header().RetransmittedPacketFlag {
						retransmittedPkt = p
					}
				},
			}
			s := NewSender(sendCfg).(*sender)

			// Create packet and add directly to lossList
			// (In production, packets move to lossList after delivery)
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			pkt := packet.NewPacket(addr)
			pkt.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
			pkt.Header().TransmitCount = tc.initialTransmitCount
			pkt.Header().PktTsbpdTime = 1000
			s.lossList.PushBack(pkt)

			// Send NAK for this packet (start/end pair format)
			nakList := []circular.Number{
				circular.New(100, packet.MAX_SEQUENCENUMBER), // start
				circular.New(100, packet.MAX_SEQUENCENUMBER), // end (same = single packet)
			}
			retransCount := s.NAK(nakList)

			// Verify retransmission occurred
			require.Equal(t, uint64(1), retransCount, "should retransmit 1 packet")
			require.NotNil(t, retransmittedPkt, "should have captured retransmitted packet")

			// Verify TransmitCount was incremented
			require.Equal(t, tc.expectedAfterRetransmit, retransmittedPkt.Header().TransmitCount,
				"TransmitCount should be incremented")

			// Verify RetransmittedPacketFlag is set
			require.True(t, retransmittedPkt.Header().RetransmittedPacketFlag,
				"RetransmittedPacketFlag should be true")
		})
	}
}

// TestNAK_TransmitCount_MultipleRetransmits verifies TransmitCount increments on each NAK
func TestNAK_TransmitCount_MultipleRetransmits(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	var lastTransmitCount uint32

	sendCfg := SendConfig{
		ConnectionMetrics:     m,
		InitialSequenceNumber: circular.New(100, packet.MAX_SEQUENCENUMBER),
		OnDeliver: func(p packet.Packet) {
			if p.Header().RetransmittedPacketFlag {
				lastTransmitCount = p.Header().TransmitCount
			}
		},
	}
	s := NewSender(sendCfg).(*sender)

	// Create packet with initial TransmitCount = 1 (already first-sent)
	// and add directly to lossList
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	pkt := packet.NewPacket(addr)
	pkt.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
	pkt.Header().TransmitCount = 1 // Simulates first-send already happened
	pkt.Header().PktTsbpdTime = 1000
	s.lossList.PushBack(pkt)

	nakList := []circular.Number{
		circular.New(100, packet.MAX_SEQUENCENUMBER),
		circular.New(100, packet.MAX_SEQUENCENUMBER),
	}

	// Multiple NAK retransmits - each should increment TransmitCount
	for i := 0; i < 5; i++ {
		s.NAK(nakList)
		require.Equal(t, uint32(i+2), lastTransmitCount,
			"TransmitCount after NAK %d should be %d", i+1, i+2)
	}

	t.Logf("Final TransmitCount after 5 NAKs: %d", lastTransmitCount)
}

// TestNAK_RetransFirstTime_Metric verifies RetransFirstTime fires only when TC was 0
func TestNAK_RetransFirstTime_Metric(t *testing.T) {
	testCases := []struct {
		name                   string
		initialTransmitCount   uint32
		expectRetransFirstTime bool
	}{
		{
			name:                   "TC_0_fires_RetransFirstTime",
			initialTransmitCount:   0,
			expectRetransFirstTime: true, // TC becomes 1 after increment
		},
		{
			name:                   "TC_1_no_RetransFirstTime",
			initialTransmitCount:   1,
			expectRetransFirstTime: false, // TC becomes 2
		},
		{
			name:                   "TC_5_no_RetransFirstTime",
			initialTransmitCount:   5,
			expectRetransFirstTime: false, // TC becomes 6
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}

			sendCfg := SendConfig{
				ConnectionMetrics:     m,
				InitialSequenceNumber: circular.New(100, packet.MAX_SEQUENCENUMBER),
				OnDeliver:             func(p packet.Packet) {},
			}
			s := NewSender(sendCfg).(*sender)

			// Create packet and add directly to lossList
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			pkt := packet.NewPacket(addr)
			pkt.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
			pkt.Header().TransmitCount = tc.initialTransmitCount
			pkt.Header().PktTsbpdTime = 1000
			s.lossList.PushBack(pkt)

			nakList := []circular.Number{
				circular.New(100, packet.MAX_SEQUENCENUMBER),
				circular.New(100, packet.MAX_SEQUENCENUMBER),
			}
			s.NAK(nakList)

			if tc.expectRetransFirstTime {
				require.Equal(t, uint64(1), m.RetransFirstTime.Load(),
					"RetransFirstTime should fire for TC=0")
			} else {
				require.Equal(t, uint64(0), m.RetransFirstTime.Load(),
					"RetransFirstTime should NOT fire for TC>0")
			}
		})
	}
}
