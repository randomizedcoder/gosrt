package live

import (
	"net"
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Phase 7: Core Scan Function Tests (ACK Optimization)
// Tests for contiguousScan() and gapScan() with 31-bit wraparound safety
// ═══════════════════════════════════════════════════════════════════════════

// createScanTestReceiver creates a minimal receiver for scan testing
func createScanTestReceiver(t *testing.T, startSeq uint32) *receiver {
	testMetrics := &metrics.ConnectionMetrics{}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000, // 10ms
		PeriodicNAKInterval:    20_000, // 20ms
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",
		TsbpdDelay:             120_000, // 120ms
		NakRecentPercent:       0.10,
	}

	recv := NewReceiver(recvConfig)
	return recv.(*receiver)
}

// createScanTestPacket creates a packet with given sequence number
func createScanTestPacket(t *testing.T, seq uint32, tsbpdTime uint64) packet.Packet {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = tsbpdTime
	return p
}

// ═══════════════════════════════════════════════════════════════════════════
// contiguousScan Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestContiguousScan_Empty(t *testing.T) {
	recv := createScanTestReceiver(t, 0)

	ok, ackSeq := recv.contiguousScan()

	require.False(t, ok, "Empty btree should return ok=false")
	require.Equal(t, uint32(0), ackSeq)
}

func TestContiguousScan_Contiguous(t *testing.T) {
	recv := createScanTestReceiver(t, 0)

	// Insert contiguous packets: 0, 1, 2, 3, 4
	for i := uint32(0); i <= 4; i++ {
		p := createScanTestPacket(t, i, 100+uint64(i))
		recv.packetStore.Insert(p)
	}

	ok, ackSeq := recv.contiguousScan()

	require.True(t, ok, "Contiguous packets should return ok=true")
	// ACK seq should be lastContiguous + 1 = 4 + 1 = 5
	require.Equal(t, uint32(5), ackSeq)
	// contiguousPoint should be updated to 4
	require.Equal(t, uint32(4), recv.contiguousPoint.Load())
}

func TestContiguousScan_Gap(t *testing.T) {
	recv := createScanTestReceiver(t, 0)

	// Set nowFn to return a fixed time
	baseTime := uint64(1_000_000_000)
	recv.nowFn = func() uint64 { return baseTime }

	// Insert packets with gap: 0, 1, 3, 4 (missing 2)
	// TSBPD time in FUTURE so gap doesn't get skipped
	futureTime := baseTime + 1_000_000
	for _, seq := range []uint32{0, 1, 3, 4} {
		p := createScanTestPacket(t, seq, futureTime)
		recv.packetStore.Insert(p)
	}

	ok, ackSeq := recv.contiguousScan()

	require.True(t, ok, "Partial contiguous should return ok=true")
	// ACK seq should be lastContiguous + 1 = 1 + 1 = 2
	require.Equal(t, uint32(2), ackSeq)
	// contiguousPoint should be updated to 1 (stops at gap)
	require.Equal(t, uint32(1), recv.contiguousPoint.Load())
}

func TestContiguousScan_NoProgress(t *testing.T) {
	recv := createScanTestReceiver(t, 0)

	// Set nowFn to return a fixed time
	baseTime := uint64(1_000_000_000)
	recv.nowFn = func() uint64 { return baseTime }

	// Insert packet 2 (gap at 0, 1)
	// TSBPD time in FUTURE so gap doesn't get skipped
	futureTime := baseTime + 1_000_000
	p := createScanTestPacket(t, 2, futureTime)
	recv.packetStore.Insert(p)

	ok, ackSeq := recv.contiguousScan()

	// ISN is 0, but first packet is 2, so no contiguous progress
	// contiguousPoint stays at ISN.Dec() = MAX_SEQUENCENUMBER
	require.False(t, ok, "Gap at start should return ok=false")
	require.Equal(t, uint32(0), ackSeq)
	// NOTE: contiguousPoint is initialized to ISN.Dec() = MAX when ISN=0
	require.Equal(t, uint32(packet.MAX_SEQUENCENUMBER), recv.contiguousPoint.Load(),
		"contiguousPoint should stay at ISN.Dec() = MAX")
}

// ═══════════════════════════════════════════════════════════════════════════
// contiguousScan 31-bit Wraparound Tests (CRITICAL)
// ═══════════════════════════════════════════════════════════════════════════

func TestContiguousScan_Wraparound(t *testing.T) {
	// Test sequence: MAX-2, MAX-1, MAX, 0, 1, 2 (crosses 31-bit boundary)
	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)

	// Start at MAX-2
	recv := createScanTestReceiver(t, maxSeq-2)

	// Insert contiguous packets that cross the boundary
	seqs := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
	for i, seq := range seqs {
		p := createScanTestPacket(t, seq, 100+uint64(i))
		recv.packetStore.Insert(p)
	}

	ok, ackSeq := recv.contiguousScan()

	require.True(t, ok, "Wraparound contiguous should return ok=true")
	// ACK seq should be 2 + 1 = 3
	require.Equal(t, uint32(3), ackSeq)
	// contiguousPoint should be at 2
	require.Equal(t, uint32(2), recv.contiguousPoint.Load())
}

func TestContiguousScan_WraparoundWithGap(t *testing.T) {
	// Test sequence: MAX-2, MAX-1, MAX, 2, 3 (gap at 0, 1)
	// IMPORTANT: Set TSBPD time in FUTURE so scan stops at gap instead of skipping
	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)

	// Start at MAX-2
	recv := createScanTestReceiver(t, maxSeq-2)

	// Set nowFn to return a fixed time
	baseTime := uint64(1_000_000_000)
	recv.nowFn = func() uint64 { return baseTime }

	// Insert packets with gap at 0, 1
	// TSBPD time in FUTURE so gap doesn't get skipped
	futureTime := baseTime + 1_000_000
	seqs := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 2, 3}
	for _, seq := range seqs {
		p := createScanTestPacket(t, seq, futureTime)
		recv.packetStore.Insert(p)
	}

	ok, ackSeq := recv.contiguousScan()

	require.True(t, ok, "Partial wraparound should return ok=true")
	// ACK seq should be MAX + 1 = 0 (wrapped)
	require.Equal(t, uint32(0), ackSeq)
	// contiguousPoint should be at MAX
	require.Equal(t, maxSeq, recv.contiguousPoint.Load())
}

// ═══════════════════════════════════════════════════════════════════════════
// gapScan Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestGapScan_NoGaps(t *testing.T) {
	recv := createScanTestReceiver(t, 0)

	// Insert contiguous packets: 0, 1, 2, 3, 4
	for i := uint32(0); i <= 4; i++ {
		p := createScanTestPacket(t, i, 100+uint64(i))
		recv.packetStore.Insert(p)
	}

	gaps := recv.gapScan()

	require.Empty(t, gaps, "Contiguous packets should have no gaps")
	// contiguousPoint should advance to 4
	require.Equal(t, uint32(4), recv.contiguousPoint.Load())
}

func TestGapScan_SingleGap(t *testing.T) {
	recv := createScanTestReceiver(t, 0)

	// Insert packets with single gap: 0, 1, 3, 4 (missing 2)
	// Use far future TSBPD times so they're not "too recent"
	for _, seq := range []uint32{0, 1, 3, 4} {
		p := createScanTestPacket(t, seq, 1) // Far past (not too recent)
		recv.packetStore.Insert(p)
	}

	gaps := recv.gapScan()

	require.Equal(t, []uint32{2}, gaps, "Should detect gap at 2")
	// contiguousPoint should advance to 1 (before gap)
	require.Equal(t, uint32(1), recv.contiguousPoint.Load())
}

func TestGapScan_MultipleGaps(t *testing.T) {
	recv := createScanTestReceiver(t, 0)

	// Insert packets with gaps: 0, 3, 6 (missing 1,2 and 4,5)
	for _, seq := range []uint32{0, 3, 6} {
		p := createScanTestPacket(t, seq, 1) // Far past
		recv.packetStore.Insert(p)
	}

	gaps := recv.gapScan()

	require.Equal(t, []uint32{1, 2, 4, 5}, gaps, "Should detect gaps at 1,2,4,5")
}

func TestGapScan_AdvancesContiguousPoint(t *testing.T) {
	recv := createScanTestReceiver(t, 0)

	// Insert: 0, 1, 2, 5, 6 (gap at 3, 4)
	for _, seq := range []uint32{0, 1, 2, 5, 6} {
		p := createScanTestPacket(t, seq, 1) // Far past
		recv.packetStore.Insert(p)
	}

	gaps := recv.gapScan()

	require.Equal(t, []uint32{3, 4}, gaps)
	// contiguousPoint should advance to 2 (contiguous before gap)
	require.Equal(t, uint32(2), recv.contiguousPoint.Load())
}

// ═══════════════════════════════════════════════════════════════════════════
// gapScan 31-bit Wraparound Tests (CRITICAL)
// ═══════════════════════════════════════════════════════════════════════════

func TestGapScan_Wraparound(t *testing.T) {
	// Test sequence: MAX-2, MAX-1, MAX, 2, 3 (gap at 0, 1)
	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)

	// Start at MAX-2
	recv := createScanTestReceiver(t, maxSeq-2)

	// Insert packets with gap at 0, 1
	seqs := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 2, 3}
	for _, seq := range seqs {
		p := createScanTestPacket(t, seq, 1) // Far past
		recv.packetStore.Insert(p)
	}

	gaps := recv.gapScan()

	require.Equal(t, []uint32{0, 1}, gaps, "Should detect gap at 0, 1 across wraparound")
	// contiguousPoint should be at MAX
	require.Equal(t, maxSeq, recv.contiguousPoint.Load())
}

func TestGapScan_WraparoundNoGaps(t *testing.T) {
	// Test sequence: MAX-2, MAX-1, MAX, 0, 1, 2 (all contiguous)
	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)

	// Start at MAX-2
	recv := createScanTestReceiver(t, maxSeq-2)

	// Insert contiguous packets across wraparound
	seqs := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
	for _, seq := range seqs {
		p := createScanTestPacket(t, seq, 1) // Far past
		recv.packetStore.Insert(p)
	}

	gaps := recv.gapScan()

	require.Empty(t, gaps, "No gaps in contiguous wraparound")
	// contiguousPoint should advance to 2
	require.Equal(t, uint32(2), recv.contiguousPoint.Load())
}

// ═══════════════════════════════════════════════════════════════════════════
// Stale contiguousPoint Tests (TSBPD Expiry Scenario)
//
// These tests verify behavior when contiguousPoint points to a sequence that
// has been delivered/removed from the btree due to TSBPD expiry.
// ═══════════════════════════════════════════════════════════════════════════

func TestContiguousScan_StaleContiguousPoint_BtreeMinAhead(t *testing.T) {
	// Scenario: contiguousPoint=100, but packets 100-299 were delivered (removed).
	// Btree now contains only [300,301,302].
	// Gap of 200 packets (>= threshold of 64) triggers stale detection.
	//
	// This simulates: TSBPD passed for 100-299, they were delivered.
	// New packets 300+ arrive.
	//
	// Expected: contiguousScan should detect stale contiguousPoint and advance it.
	// The scan should skip the delivered gap and ACK the new packets.

	recv := createScanTestReceiver(t, 100)

	// Set contiguousPoint to 100 (simulating previous state)
	recv.contiguousPoint.Store(100)

	// Btree is empty of 100-299, only has 300,301,302
	// (simulating delivery removed them - gap of 200 > threshold 64)
	for _, seq := range []uint32{300, 301, 302} {
		p := createScanTestPacket(t, seq, 1_000_000) // Future TSBPD
		recv.packetStore.Insert(p)
	}

	t.Logf("Before scan: contiguousPoint=%d, btree.Min=%d, gap=%d",
		recv.contiguousPoint.Load(),
		recv.packetStore.Min().Header().PacketSequenceNumber.Val(),
		300-101) // gap size

	// Run contiguousScan
	ok, ackSeq := recv.contiguousScan()

	t.Logf("After scan: ok=%v, ackSeq=%d, contiguousPoint=%d",
		ok, ackSeq, recv.contiguousPoint.Load())

	// With stale contiguousPoint handling (gap=199 >= threshold=64):
	// - Detects btree.Min(300) > contiguousPoint+1 (101)
	// - Gap size (199) >= threshold (64), triggers stale handling
	// - Advances contiguousPoint to 299 (btree.Min-1)
	// - Scans and finds 300,301,302 contiguous
	// - Returns ackSeq=303 (next expected after 302)
	require.True(t, ok, "Should handle stale contiguousPoint and make progress")
	require.Equal(t, uint32(303), ackSeq, "Should ACK to 303 (next expected after 302)")
	require.Equal(t, uint32(302), recv.contiguousPoint.Load(), "contiguousPoint should be 302")
}

func TestContiguousScan_StaleContiguousPoint_EmptyBtree(t *testing.T) {
	// Scenario: contiguousPoint=100, all packets delivered, btree empty
	// Then new packets 300+ arrive (gap of 200 > threshold 64)
	//
	// This simulates: All packets up to 299 delivered via TSBPD
	// New burst of packets starting at 300

	recv := createScanTestReceiver(t, 100)
	recv.contiguousPoint.Store(100)

	// Btree is empty initially
	ok, ackSeq := recv.contiguousScan()
	require.False(t, ok, "Empty btree should return ok=false")
	require.Equal(t, uint32(0), ackSeq)

	// Now new packets arrive (gap of 200 >= threshold 64)
	for _, seq := range []uint32{300, 301, 302} {
		p := createScanTestPacket(t, seq, 1_000_000)
		recv.packetStore.Insert(p)
	}

	ok, ackSeq = recv.contiguousScan()

	t.Logf("After adding packets 300-302: ok=%v, ackSeq=%d, contiguousPoint=%d",
		ok, ackSeq, recv.contiguousPoint.Load())

	require.True(t, ok, "Should handle large gap and make progress")
	require.Equal(t, uint32(303), ackSeq, "Should ACK to 303")
	require.Equal(t, uint32(302), recv.contiguousPoint.Load())
}

func TestGapScan_StaleContiguousPoint_BtreeMinAhead(t *testing.T) {
	// Scenario: contiguousPoint=100, btree has [300,301,303,304] (gap at 302)
	// Gap of 200 packets (>= threshold of 64) triggers stale detection.
	//
	// This simulates: Packets 100-299 were delivered via TSBPD.
	// New packets arrive with gap at 302.
	//
	// Expected: gapScan should NOT NAK 101-299 (already delivered).
	// Should only report the actual gap at 302.

	recv := createScanTestReceiver(t, 100)
	recv.contiguousPoint.Store(100)

	// Insert packets with gap at 302 (gap of 200 >= threshold 64)
	for _, seq := range []uint32{300, 301, 303, 304} {
		p := createScanTestPacket(t, seq, 1_000_000)
		recv.packetStore.Insert(p)
	}

	t.Logf("Before gapScan: contiguousPoint=%d, gap to btree.Min=%d",
		recv.contiguousPoint.Load(), 300-101)

	gaps := recv.gapScan()

	t.Logf("After gapScan: gaps=%v, contiguousPoint=%d",
		gaps, recv.contiguousPoint.Load())

	// With stale contiguousPoint handling (gap=199 >= threshold=64):
	// - Detects btree.Min(300) > contiguousPoint+1 (101)
	// - Advances scan start to 299 (btree.Min-1)
	// - Finds 300,301 contiguous, then gap at 302, then 303,304
	// - Only reports gap at 302
	require.Equal(t, []uint32{302}, gaps, "Should only NAK the actual gap at 302, not delivered packets")
	require.Equal(t, uint32(301), recv.contiguousPoint.Load(),
		"contiguousPoint should be 301 (last contiguous before gap)")
}

func TestContiguousScan_NormalProgression(t *testing.T) {
	// Control test: Normal case where contiguousPoint is valid
	// and packets are contiguous

	recv := createScanTestReceiver(t, 100)
	recv.contiguousPoint.Store(100)

	// Insert contiguous packets
	for _, seq := range []uint32{100, 101, 102, 103, 104} {
		p := createScanTestPacket(t, seq, 1_000_000)
		recv.packetStore.Insert(p)
	}

	ok, ackSeq := recv.contiguousScan()

	require.True(t, ok, "Should make progress with contiguous packets")
	// ackSeq is "next expected" so should be 105
	require.Equal(t, uint32(105), ackSeq, "Should ACK to 105 (next expected)")
	require.Equal(t, uint32(104), recv.contiguousPoint.Load(), "contiguousPoint should be 104")
}

// ═══════════════════════════════════════════════════════════════════════════
// Stale contiguousPoint + 31-bit Wraparound Tests (CRITICAL)
// These test the combination of stale detection and sequence wraparound.
// ═══════════════════════════════════════════════════════════════════════════

func TestContiguousScan_StaleContiguousPoint_Wraparound(t *testing.T) {
	// Scenario: contiguousPoint near MAX, btree.Min crossed to small numbers
	// contiguousPoint = MAX-50, btree has [100,101,102]
	// Gap of ~150 packets crosses the 31-bit boundary
	//
	// This simulates: TSBPD delivered packets MAX-50 through MAX, 0-99
	// New packets 100+ arrive

	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)
	startSeq := maxSeq - 50 // Start at MAX-50

	recv := createScanTestReceiver(t, startSeq)
	recv.contiguousPoint.Store(startSeq)

	// Btree has packets after wraparound (100,101,102)
	// Gap: (MAX-50)+1 to 99 = ~150 packets (>= threshold 64)
	for _, seq := range []uint32{100, 101, 102} {
		p := createScanTestPacket(t, seq, 1_000_000)
		recv.packetStore.Insert(p)
	}

	// Calculate expected gap size for logging
	expectedNext := circular.SeqAdd(startSeq, 1)
	gapSize := circular.SeqSub(100, expectedNext)
	t.Logf("Before scan: contiguousPoint=%d (MAX-%d), btree.Min=100, gap=%d",
		startSeq, maxSeq-startSeq, gapSize)

	ok, ackSeq := recv.contiguousScan()

	t.Logf("After scan: ok=%v, ackSeq=%d, contiguousPoint=%d",
		ok, ackSeq, recv.contiguousPoint.Load())

	// With wraparound-safe stale handling:
	// - Detects gap crosses 31-bit boundary (MAX-50 → 100)
	// - Gap size ~150 >= threshold 64
	// - Advances contiguousPoint to 99 (btree.Min-1)
	// - Scans and finds 100,101,102 contiguous
	// - Returns ackSeq=103
	require.True(t, ok, "Should handle stale contiguousPoint across wraparound")
	require.Equal(t, uint32(103), ackSeq, "Should ACK to 103")
	require.Equal(t, uint32(102), recv.contiguousPoint.Load(), "contiguousPoint should be 102")
}

func TestGapScan_StaleContiguousPoint_Wraparound(t *testing.T) {
	// Similar scenario for gapScan: contiguousPoint near MAX, btree.Min after wraparound
	// contiguousPoint = MAX-50, btree has [100,101,103,104] (gap at 102)

	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)
	startSeq := maxSeq - 50

	recv := createScanTestReceiver(t, startSeq)
	recv.contiguousPoint.Store(startSeq)

	// Insert packets with gap at 102
	for _, seq := range []uint32{100, 101, 103, 104} {
		p := createScanTestPacket(t, seq, 1_000_000)
		recv.packetStore.Insert(p)
	}

	expectedNext := circular.SeqAdd(startSeq, 1)
	gapSize := circular.SeqSub(100, expectedNext)
	t.Logf("Before gapScan: contiguousPoint=%d (MAX-%d), btree.Min=100, gap=%d",
		startSeq, maxSeq-startSeq, gapSize)

	gaps := recv.gapScan()

	t.Logf("After gapScan: gaps=%v, contiguousPoint=%d",
		gaps, recv.contiguousPoint.Load())

	// Should NOT NAK the wraparound gap (MAX-49 through 99)
	// Should only NAK the actual gap at 102
	require.Equal(t, []uint32{102}, gaps, "Should only NAK gap at 102, not wraparound gap")
	require.Equal(t, uint32(101), recv.contiguousPoint.Load(),
		"contiguousPoint should be 101 (last contiguous before gap)")
}

func TestContiguousScan_SmallGap_NoStaleHandling(t *testing.T) {
	// Scenario: Small gap (< threshold) should NOT trigger stale handling
	// contiguousPoint = 100, btree has [110,111,112] (gap of 10, < threshold 64)
	//
	// This is real packet loss, not TSBPD delivery
	// IMPORTANT: Set TSBPD time far in FUTURE so TSBPD skip logic doesn't trigger

	recv := createScanTestReceiver(t, 100)
	recv.contiguousPoint.Store(100)

	// Set nowFn to return a fixed time
	baseTime := uint64(1_000_000_000)
	recv.nowFn = func() uint64 { return baseTime }

	// Small gap (10 < threshold 64), TSBPD time in FUTURE
	futureTime := baseTime + 1_000_000 // 1 second in future
	for _, seq := range []uint32{110, 111, 112} {
		p := createScanTestPacket(t, seq, futureTime)
		recv.packetStore.Insert(p)
	}

	ok, ackSeq := recv.contiguousScan()

	// Should NOT advance past the gap (TSBPD not expired, so it's real packet loss)
	require.False(t, ok, "Small gap should not trigger stale handling")
	require.Equal(t, uint32(0), ackSeq, "Should not ACK past small gap")
	require.Equal(t, uint32(100), recv.contiguousPoint.Load(),
		"contiguousPoint should stay at 100")
}

func TestContiguousScan_SmallGap_Wraparound_NoStaleHandling(t *testing.T) {
	// Scenario: Small gap across wraparound should NOT trigger stale handling
	// contiguousPoint = MAX-5, btree has [5,6,7] (gap of ~11, < threshold 64)
	// IMPORTANT: Set TSBPD time far in FUTURE so TSBPD skip logic doesn't trigger

	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)
	startSeq := maxSeq - 5

	recv := createScanTestReceiver(t, startSeq)
	recv.contiguousPoint.Store(startSeq)

	// Set nowFn to return a fixed time
	baseTime := uint64(1_000_000_000)
	recv.nowFn = func() uint64 { return baseTime }

	// Small gap across wraparound (MAX-5 → 5 is ~11 packets, < threshold 64)
	// TSBPD time in FUTURE
	futureTime := baseTime + 1_000_000
	for _, seq := range []uint32{5, 6, 7} {
		p := createScanTestPacket(t, seq, futureTime)
		recv.packetStore.Insert(p)
	}

	expectedNext := circular.SeqAdd(startSeq, 1)
	gapSize := circular.SeqSub(5, expectedNext)
	t.Logf("Gap size across wraparound: %d (should be < 64)", gapSize)

	ok, ackSeq := recv.contiguousScan()

	// Should NOT advance past the gap (TSBPD not expired, so it's real packet loss)
	require.False(t, ok, "Small gap across wraparound should not trigger stale handling")
	require.Equal(t, uint32(0), ackSeq)
	require.Equal(t, startSeq, recv.contiguousPoint.Load(),
		"contiguousPoint should stay at MAX-5")
}

func TestContiguousScan_ExactThreshold_Wraparound(t *testing.T) {
	// Edge case: gap exactly at threshold (64) across wraparound
	// contiguousPoint = MAX-32, btree has [32,33,34]
	// Gap = (MAX-32)+1 to 31 = 64 packets (exactly at threshold)

	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)
	startSeq := maxSeq - 32

	recv := createScanTestReceiver(t, startSeq)
	recv.contiguousPoint.Store(startSeq)

	// Gap of exactly 64 across wraparound
	for _, seq := range []uint32{32, 33, 34} {
		p := createScanTestPacket(t, seq, 1_000_000)
		recv.packetStore.Insert(p)
	}

	expectedNext := circular.SeqAdd(startSeq, 1)
	gapSize := circular.SeqSub(32, expectedNext)
	t.Logf("Gap size at boundary: %d (should be exactly 64)", gapSize)
	require.Equal(t, uint32(64), gapSize, "Test setup: gap should be exactly 64")

	ok, ackSeq := recv.contiguousScan()

	// Gap >= threshold (64), should trigger stale handling
	require.True(t, ok, "Gap at threshold should trigger stale handling")
	require.Equal(t, uint32(35), ackSeq, "Should ACK to 35")
	require.Equal(t, uint32(34), recv.contiguousPoint.Load(), "contiguousPoint should be 34")
}
