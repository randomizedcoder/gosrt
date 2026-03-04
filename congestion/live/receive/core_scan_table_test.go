package receive

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Table-Driven Core Scan Tests
// Tests for contiguousScan() and gapScan() with 31-bit wraparound safety
// ═══════════════════════════════════════════════════════════════════════════

// ContiguousScanTestCase defines test parameters for contiguousScan tests
type ContiguousScanTestCase struct {
	Name            string
	StartSeq        uint32   // Initial sequence number
	ContiguousPoint uint32   // Starting contiguousPoint (0 = use default ISN-1)
	SetContiguousPt bool     // Whether to explicitly set contiguousPoint
	PacketSeqs      []uint32 // Sequences to insert
	TsbpdTime       uint64   // TSBPD time for packets (0 = use default)
	SetMockTime     bool     // Whether to set mock time
	MockTime        uint64   // Mock time value
	ExpectedOk      bool     // Expected return value
	ExpectedAckSeq  uint32   // Expected ACK sequence
	ExpectedCP      uint32   // Expected contiguousPoint after scan
}

// GapScanTestCase defines test parameters for gapScan tests
type GapScanTestCase struct {
	Name            string
	StartSeq        uint32   // Initial sequence number
	ContiguousPoint uint32   // Starting contiguousPoint (0 = use default ISN-1)
	SetContiguousPt bool     // Whether to explicitly set contiguousPoint
	PacketSeqs      []uint32 // Sequences to insert
	TsbpdTime       uint64   // TSBPD time for packets
	ExpectedGaps    []uint32 // Expected gap sequences
	ExpectedCP      uint32   // Expected contiguousPoint after scan
}

// createTableScanReceiver creates a receiver for table-driven scan tests
func createTableScanReceiver(t *testing.T, startSeq uint32) *receiver {
	testMetrics := &metrics.ConnectionMetrics{}
	testMetrics.HeaderSize.Store(44)

	recvConfig := Config{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000,
		PeriodicNAKInterval:    20_000,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",
		TsbpdDelay:             120_000,
		NakRecentPercent:       0.10,
	}

	recv := New(recvConfig)
	return recv.(*receiver)
}

// createTableScanPacket creates a packet for table-driven tests
func createTableScanPacket(seq uint32, tsbpdTime uint64) packet.Packet {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = tsbpdTime
	return p
}

// ═══════════════════════════════════════════════════════════════════════════
// ContiguousScan Table-Driven Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestContiguousScan_Table(t *testing.T) {
	t.Parallel()

	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)

	testCases := []ContiguousScanTestCase{
		// Basic scenarios
		{
			Name:           "Empty",
			StartSeq:       0,
			PacketSeqs:     []uint32{},
			ExpectedOk:     false,
			ExpectedAckSeq: 0,
			ExpectedCP:     maxSeq, // ISN.Dec() when ISN=0
		},
		{
			Name:           "Contiguous",
			StartSeq:       0,
			PacketSeqs:     []uint32{0, 1, 2, 3, 4},
			TsbpdTime:      100,
			ExpectedOk:     true,
			ExpectedAckSeq: 5,
			ExpectedCP:     4,
		},
		{
			Name:           "Gap_FutureTSBPD",
			StartSeq:       0,
			PacketSeqs:     []uint32{0, 1, 3, 4}, // Missing 2
			SetMockTime:    true,
			MockTime:       1_000_000_000,
			TsbpdTime:      1_001_000_000, // Future
			ExpectedOk:     true,
			ExpectedAckSeq: 2,
			ExpectedCP:     1, // Stops at gap
		},
		{
			Name:           "NoProgress_GapAtStart",
			StartSeq:       0,
			PacketSeqs:     []uint32{2}, // Gap at 0, 1
			SetMockTime:    true,
			MockTime:       1_000_000_000,
			TsbpdTime:      1_001_000_000,
			ExpectedOk:     false,
			ExpectedAckSeq: 0,
			ExpectedCP:     maxSeq, // ISN.Dec() = MAX when ISN=0
		},

		// Wraparound scenarios
		{
			Name:           "Wraparound",
			StartSeq:       maxSeq - 2,
			PacketSeqs:     []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2},
			TsbpdTime:      100,
			ExpectedOk:     true,
			ExpectedAckSeq: 3,
			ExpectedCP:     2,
		},
		{
			Name:           "WraparoundWithGap",
			StartSeq:       maxSeq - 2,
			PacketSeqs:     []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 2, 3}, // Gap at 0, 1
			SetMockTime:    true,
			MockTime:       1_000_000_000,
			TsbpdTime:      1_001_000_000,
			ExpectedOk:     true,
			ExpectedAckSeq: 0, // MAX + 1 wrapped
			ExpectedCP:     maxSeq,
		},

		// Stale contiguousPoint scenarios
		{
			Name:            "StaleCP_BtreeMinAhead",
			StartSeq:        100,
			ContiguousPoint: 100,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{300, 301, 302}, // Gap of 200 >= threshold 64
			TsbpdTime:       1_000_000,
			ExpectedOk:      true,
			ExpectedAckSeq:  303,
			ExpectedCP:      302,
		},
		{
			Name:            "NormalProgression",
			StartSeq:        100,
			ContiguousPoint: 100,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{100, 101, 102, 103, 104},
			TsbpdTime:       1_000_000,
			ExpectedOk:      true,
			ExpectedAckSeq:  105,
			ExpectedCP:      104,
		},

		// Stale + Wraparound
		{
			Name:            "StaleCP_Wraparound",
			StartSeq:        maxSeq - 50,
			ContiguousPoint: maxSeq - 50,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{100, 101, 102}, // Gap ~150 across boundary
			TsbpdTime:       1_000_000,
			ExpectedOk:      true,
			ExpectedAckSeq:  103,
			ExpectedCP:      102,
		},

		// Small gaps (< threshold) should NOT trigger stale handling
		{
			Name:            "SmallGap_NoStaleHandling",
			StartSeq:        100,
			ContiguousPoint: 100,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{110, 111, 112}, // Gap of 10 < threshold 64
			SetMockTime:     true,
			MockTime:        1_000_000_000,
			TsbpdTime:       1_001_000_000, // Future TSBPD
			ExpectedOk:      false,
			ExpectedAckSeq:  0,
			ExpectedCP:      100, // Stays unchanged
		},
		{
			Name:            "SmallGap_Wraparound_NoStaleHandling",
			StartSeq:        maxSeq - 5,
			ContiguousPoint: maxSeq - 5,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{5, 6, 7}, // Gap ~11 < threshold 64
			SetMockTime:     true,
			MockTime:        1_000_000_000,
			TsbpdTime:       1_001_000_000,
			ExpectedOk:      false,
			ExpectedAckSeq:  0,
			ExpectedCP:      maxSeq - 5,
		},
		{
			Name:            "ExactThreshold_Wraparound",
			StartSeq:        maxSeq - 32,
			ContiguousPoint: maxSeq - 32,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{32, 33, 34}, // Gap exactly 64
			TsbpdTime:       1_000_000,
			ExpectedOk:      true,
			ExpectedAckSeq:  35,
			ExpectedCP:      34,
		},

		// ═══════════════════════════════════════════════════════════════════════
		// CORNER CASE TESTS: StartSeq and ContiguousPoint extremes
		// Added to ensure 31-bit wraparound is handled correctly at boundaries
		//
		// NOTE: These tests use SetMockTime to ensure TSBPD is in the future,
		// so gaps properly block the scan. Without mock time, TsbpdTime=100
		// would be in the past and gaps would be skipped (DISC-001).
		// ═══════════════════════════════════════════════════════════════════════

		// StartSeq near MAX (MAX-100) - contiguous packets
		{
			Name:           "Corner_StartSeq_NearMax",
			StartSeq:       maxSeq - 100,
			PacketSeqs:     []uint32{maxSeq - 100, maxSeq - 99, maxSeq - 98},
			SetMockTime:    true,
			MockTime:       1_000_000_000,
			TsbpdTime:      1_000_000_100, // Slightly in future
			ExpectedOk:     true,
			ExpectedAckSeq: maxSeq - 97,
			ExpectedCP:     maxSeq - 98,
		},

		// StartSeq at MAX - crosses boundary
		{
			Name:           "Corner_StartSeq_AtMax",
			StartSeq:       maxSeq,
			PacketSeqs:     []uint32{maxSeq, 0, 1, 2},
			SetMockTime:    true,
			MockTime:       1_000_000_000,
			TsbpdTime:      1_000_000_100,
			ExpectedOk:     true,
			ExpectedAckSeq: 3,
			ExpectedCP:     2,
		},

		// ContiguousPoint at 0
		{
			Name:            "Corner_CP_Zero",
			StartSeq:        1,
			ContiguousPoint: 0,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{1, 2, 3, 4},
			SetMockTime:     true,
			MockTime:        1_000_000_000,
			TsbpdTime:       1_000_000_100,
			ExpectedOk:      true,
			ExpectedAckSeq:  5,
			ExpectedCP:      4,
		},

		// ContiguousPoint near MAX (MAX-100)
		{
			Name:            "Corner_CP_NearMax",
			StartSeq:        maxSeq - 100,
			ContiguousPoint: maxSeq - 100,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{maxSeq - 100, maxSeq - 99, maxSeq - 98},
			SetMockTime:     true,
			MockTime:        1_000_000_000,
			TsbpdTime:       1_000_000_100,
			ExpectedOk:      true,
			ExpectedAckSeq:  maxSeq - 97,
			ExpectedCP:      maxSeq - 98,
		},

		// ContiguousPoint at MAX - crosses boundary
		{
			Name:            "Corner_CP_AtMax",
			StartSeq:        maxSeq,
			ContiguousPoint: maxSeq,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{maxSeq, 0, 1, 2},
			SetMockTime:     true,
			MockTime:        1_000_000_000,
			TsbpdTime:       1_000_000_100,
			ExpectedOk:      true,
			ExpectedAckSeq:  3,
			ExpectedCP:      2,
		},

		// Both StartSeq and ContiguousPoint near MAX with GAP
		// DISC-001: Tests gap detection at wraparound boundary
		// Gap of 98 packets (maxSeq-98 through maxSeq-1) should stop the scan
		{
			Name:            "Corner_Combo_BothNearMax_WithGap",
			StartSeq:        maxSeq - 100,
			ContiguousPoint: maxSeq - 100,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{maxSeq - 100, maxSeq - 99, maxSeq, 0, 1}, // Gap at maxSeq-98 through maxSeq-1
			SetMockTime:     true,
			MockTime:        1_000_000_000,
			TsbpdTime:       1_001_000_000, // Future - gaps should block
			ExpectedOk:      true,
			ExpectedAckSeq:  maxSeq - 98, // Should stop at first gap
			ExpectedCP:      maxSeq - 99,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			recv := createTableScanReceiver(t, tc.StartSeq)

			// Set contiguousPoint if specified
			if tc.SetContiguousPt {
				recv.contiguousPoint.Store(tc.ContiguousPoint)
			}

			// Set mock time if specified
			if tc.SetMockTime {
				mockTime := tc.MockTime
				recv.nowFn = func() uint64 { return mockTime }
			}

			// Insert packets
			tsbpdTime := tc.TsbpdTime
			if tsbpdTime == 0 {
				tsbpdTime = 100
			}
			for i, seq := range tc.PacketSeqs {
				p := createTableScanPacket(seq, tsbpdTime+uint64(i))
				recv.packetStore.Insert(p)
			}

			// Run scan
			ok, ackSeq := recv.contiguousScan()

			// Verify results
			require.Equal(t, tc.ExpectedOk, ok, "ok mismatch")
			require.Equal(t, tc.ExpectedAckSeq, ackSeq, "ackSeq mismatch")
			require.Equal(t, tc.ExpectedCP, recv.contiguousPoint.Load(), "contiguousPoint mismatch")
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// GapScan Table-Driven Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestGapScan_Table(t *testing.T) {
	t.Parallel()

	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)

	testCases := []GapScanTestCase{
		// Basic scenarios
		{
			Name:         "NoGaps",
			StartSeq:     0,
			PacketSeqs:   []uint32{0, 1, 2, 3, 4},
			TsbpdTime:    1, // Far past
			ExpectedGaps: []uint32{},
			ExpectedCP:   4,
		},
		{
			Name:         "SingleGap",
			StartSeq:     0,
			PacketSeqs:   []uint32{0, 1, 3, 4}, // Missing 2
			TsbpdTime:    1,
			ExpectedGaps: []uint32{2},
			ExpectedCP:   1,
		},
		{
			Name:         "MultipleGaps",
			StartSeq:     0,
			PacketSeqs:   []uint32{0, 3, 6}, // Missing 1,2 and 4,5
			TsbpdTime:    1,
			ExpectedGaps: []uint32{1, 2, 4, 5},
			ExpectedCP:   0, // Only first packet is contiguous
		},
		{
			Name:         "AdvancesContiguousPoint",
			StartSeq:     0,
			PacketSeqs:   []uint32{0, 1, 2, 5, 6}, // Gap at 3, 4
			TsbpdTime:    1,
			ExpectedGaps: []uint32{3, 4},
			ExpectedCP:   2,
		},

		// Wraparound scenarios
		{
			Name:         "Wraparound",
			StartSeq:     maxSeq - 2,
			PacketSeqs:   []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 2, 3}, // Gap at 0, 1
			TsbpdTime:    1,
			ExpectedGaps: []uint32{0, 1},
			ExpectedCP:   maxSeq,
		},
		{
			Name:         "WraparoundNoGaps",
			StartSeq:     maxSeq - 2,
			PacketSeqs:   []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2},
			TsbpdTime:    1,
			ExpectedGaps: []uint32{},
			ExpectedCP:   2,
		},

		// Stale contiguousPoint scenario
		{
			Name:            "StaleCP_BtreeMinAhead",
			StartSeq:        100,
			ContiguousPoint: 100,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{300, 301, 303, 304}, // Gap of 200 to btree, gap at 302
			TsbpdTime:       1_000_000,
			ExpectedGaps:    []uint32{302}, // Only actual gap, not delivered packets
			ExpectedCP:      301,
		},

		// Stale + Wraparound
		{
			Name:            "StaleCP_Wraparound",
			StartSeq:        maxSeq - 50,
			ContiguousPoint: maxSeq - 50,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{100, 101, 103, 104}, // Gap at 102
			TsbpdTime:       1_000_000,
			ExpectedGaps:    []uint32{102},
			ExpectedCP:      101,
		},

		// ═══════════════════════════════════════════════════════════════════════
		// CORNER CASE TESTS: StartSeq and ContiguousPoint extremes for gapScan
		// Added to ensure 31-bit wraparound is handled correctly at boundaries
		//
		// NOTE: gapScan uses TsbpdTime directly for tooRecentThreshold comparison.
		// Using small TsbpdTime values (like 1) means packets are far in the past,
		// which is valid for gap detection tests.
		// ═══════════════════════════════════════════════════════════════════════

		// StartSeq near MAX (MAX-100)
		{
			Name:         "Corner_StartSeq_NearMax",
			StartSeq:     maxSeq - 100,
			PacketSeqs:   []uint32{maxSeq - 100, maxSeq - 99, maxSeq - 97, maxSeq - 96}, // Gap at maxSeq-98
			TsbpdTime:    1,
			ExpectedGaps: []uint32{maxSeq - 98},
			ExpectedCP:   maxSeq - 99,
		},

		// StartSeq at MAX - crosses boundary
		{
			Name:         "Corner_StartSeq_AtMax",
			StartSeq:     maxSeq,
			PacketSeqs:   []uint32{maxSeq, 0, 2, 3}, // Gap at 1
			TsbpdTime:    1,
			ExpectedGaps: []uint32{1},
			ExpectedCP:   0,
		},

		// ContiguousPoint at 0
		{
			Name:            "Corner_CP_Zero",
			StartSeq:        1,
			ContiguousPoint: 0,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{1, 2, 4, 5}, // Gap at 3
			TsbpdTime:       1,
			ExpectedGaps:    []uint32{3},
			ExpectedCP:      2,
		},

		// ContiguousPoint near MAX (MAX-100)
		{
			Name:            "Corner_CP_NearMax",
			StartSeq:        maxSeq - 100,
			ContiguousPoint: maxSeq - 100,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{maxSeq - 100, maxSeq - 99, maxSeq - 97}, // Gap at maxSeq-98
			TsbpdTime:       1,
			ExpectedGaps:    []uint32{maxSeq - 98},
			ExpectedCP:      maxSeq - 99,
		},

		// ContiguousPoint at MAX - crosses boundary
		{
			Name:            "Corner_CP_AtMax",
			StartSeq:        maxSeq,
			ContiguousPoint: maxSeq,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{maxSeq, 0, 2, 3}, // Gap at 1
			TsbpdTime:       1,
			ExpectedGaps:    []uint32{1},
			ExpectedCP:      0,
		},

		// Both StartSeq and ContiguousPoint near MAX - small gap at boundary
		// DISC-002: Tests gap detection at wraparound boundary
		{
			Name:            "Corner_Combo_BothNearMax_WithGap",
			StartSeq:        maxSeq - 3,
			ContiguousPoint: maxSeq - 3,
			SetContiguousPt: true,
			PacketSeqs:      []uint32{maxSeq - 3, maxSeq - 2, maxSeq, 1, 2}, // Gap at maxSeq-1 and 0
			TsbpdTime:       1,
			ExpectedGaps:    []uint32{maxSeq - 1, 0}, // Two gaps at wraparound boundary
			ExpectedCP:      maxSeq - 2,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			recv := createTableScanReceiver(t, tc.StartSeq)

			// Set contiguousPoint if specified
			if tc.SetContiguousPt {
				recv.contiguousPoint.Store(tc.ContiguousPoint)
			}

			// Insert packets
			for _, seq := range tc.PacketSeqs {
				p := createTableScanPacket(seq, tc.TsbpdTime)
				recv.packetStore.Insert(p)
			}

			// Run scan
			gaps, gapTsbpds := recv.gapScan()

			// Verify results
			if len(tc.ExpectedGaps) == 0 {
				require.Empty(t, gaps, "Expected no gaps")
			} else {
				require.Equal(t, tc.ExpectedGaps, gaps, "gaps mismatch")
				// Verify TSBPD times were estimated for all gaps
				require.Equal(t, len(gaps), len(gapTsbpds), "TSBPD count should match gap count")
			}
			require.Equal(t, tc.ExpectedCP, recv.contiguousPoint.Load(), "contiguousPoint mismatch")
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Special Case: Empty Btree Then Packets Arrive
// ═══════════════════════════════════════════════════════════════════════════

func TestContiguousScan_StaleCP_EmptyBtree_Table(t *testing.T) {
	t.Parallel()

	// This test has two phases and can't be fully table-driven
	recv := createTableScanReceiver(t, 100)
	recv.contiguousPoint.Store(100)

	// Phase 1: Empty btree
	ok, ackSeq := recv.contiguousScan()
	require.False(t, ok, "Empty btree should return ok=false")
	require.Equal(t, uint32(0), ackSeq)

	// Phase 2: New packets arrive (gap of 200 >= threshold 64)
	for _, seq := range []uint32{300, 301, 302} {
		p := createTableScanPacket(seq, 1_000_000)
		recv.packetStore.Insert(p)
	}

	ok, ackSeq = recv.contiguousScan()
	require.True(t, ok, "Should handle large gap and make progress")
	require.Equal(t, uint32(303), ackSeq, "Should ACK to 303")
	require.Equal(t, uint32(302), recv.contiguousPoint.Load())
}
