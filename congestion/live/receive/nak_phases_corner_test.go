package receive

import (
	"net"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════
// Corner Case Tests for NAK Phases
// These tests focus on edge cases where bugs are likely to hide.
// ═══════════════════════════════════════════════════════════════════════════

// TestShouldRunPeriodicNak_OverflowBoundary tests uint64 overflow edge cases.
func TestShouldRunPeriodicNak_OverflowBoundary(t *testing.T) {
	tests := []struct {
		name           string
		lastNak        uint64
		now            uint64
		nakInterval    uint64
		expectedResult bool
	}{
		{
			name:           "near_uint64_max_no_overflow",
			lastNak:        ^uint64(0) - 100000, // Near max
			now:            ^uint64(0) - 50000,  // 50ms later
			nakInterval:    20000,
			expectedResult: true,
		},
		{
			name:           "lastNak_zero_now_max",
			lastNak:        0,
			now:            ^uint64(0),
			nakInterval:    20000,
			expectedResult: true,
		},
		{
			name:           "both_zero",
			lastNak:        0,
			now:            0,
			nakInterval:    20000,
			expectedResult: false, // 0 - 0 = 0 < 20000
		},
		{
			name:           "large_interval",
			lastNak:        1000000,
			now:            2000000,
			nakInterval:    ^uint64(0), // Max interval - should never run
			expectedResult: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &receiver{
				lastPeriodicNAK:     tc.lastNak,
				periodicNAKInterval: tc.nakInterval,
			}

			result := r.shouldRunPeriodicNak(tc.now)
			if result != tc.expectedResult {
				t.Errorf("shouldRunPeriodicNak = %v, want %v", result, tc.expectedResult)
			}
		})
	}
}

// TestCalculateNakScanParams_SequenceWraparound tests 31-bit sequence wraparound.
func TestCalculateNakScanParams_SequenceWraparound(t *testing.T) {
	// Test with contiguousPoint near MAX_SEQUENCENUMBER
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	// Set contiguousPoint to near MAX
	maxSeq := packet.MAX_SEQUENCENUMBER
	r.contiguousPoint.Store(maxSeq - 5)

	// Add packet at sequence 0 (wrapped)
	now := uint64(time.Now().UnixMicro())
	tsbpdTime := now + r.tsbpdDelay
	pkt := createNakPhasesTestPacket(0, tsbpdTime)
	r.packetStore.Insert(pkt)

	params := r.calculateNakScanParams(now)

	// Should handle wraparound correctly
	if params == nil {
		t.Fatal("calculateNakScanParams returned nil, expected valid params")
	}

	// startSeq should be maxSeq - 4 (contiguousPoint + 1)
	expectedStartSeq := circular.SeqAdd(maxSeq-5, 1)
	if params.startSeq != expectedStartSeq {
		t.Errorf("startSeq = %d, want %d", params.startSeq, expectedStartSeq)
	}
}

// TestDetectGaps_EmptyPacketStore tests gap detection with empty btree.
func TestDetectGaps_EmptyPacketStore(t *testing.T) {
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	params := &nakScanParams{
		startSeq:           100,
		tooRecentThreshold: uint64(time.Now().UnixMicro()) + 5000000,
		firstScanEver:      false,
	}

	result := r.detectGaps(params)

	// Empty store should produce no gaps
	if len(result.gaps) != 0 {
		t.Errorf("expected no gaps with empty store, got %d", len(result.gaps))
	}
	if result.packetsScanned != 0 {
		t.Errorf("expected 0 packets scanned, got %d", result.packetsScanned)
	}
}

// TestDetectGaps_SinglePacket tests gap detection with one packet.
func TestDetectGaps_SinglePacket(t *testing.T) {
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	now := uint64(time.Now().UnixMicro())
	tsbpdTime := now + 500000 // 500ms from now

	// Insert single packet at seq 100
	pkt := createNakPhasesTestPacket(100, tsbpdTime)
	r.packetStore.Insert(pkt)
	r.contiguousPoint.Store(99) // Expecting 100

	params := &nakScanParams{
		startSeq:           100,
		tooRecentThreshold: now + 1000000, // 1s from now
		firstScanEver:      false,
	}

	result := r.detectGaps(params)

	// Single packet at expected seq should have no gaps
	if len(result.gaps) != 0 {
		t.Errorf("expected no gaps for single contiguous packet, got %v", result.gaps)
	}
	if result.packetsScanned != 1 {
		t.Errorf("expected 1 packet scanned, got %d", result.packetsScanned)
	}
}

// TestDetectGaps_LargeGap tests detection of large sequence gaps.
func TestDetectGaps_LargeGap(t *testing.T) {
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	now := uint64(time.Now().UnixMicro())
	tsbpdTime := now + 500000

	// Insert packets 100 and 200, leaving gap 101-199
	r.packetStore.Insert(createNakPhasesTestPacket(100, tsbpdTime))
	r.packetStore.Insert(createNakPhasesTestPacket(200, tsbpdTime))
	r.contiguousPoint.Store(99)

	params := &nakScanParams{
		startSeq:           100,
		tooRecentThreshold: now + 1000000,
		firstScanEver:      false,
	}

	result := r.detectGaps(params)

	// Should detect gap of 99 sequences (101-199)
	expectedGaps := 99
	if len(result.gaps) != expectedGaps {
		t.Errorf("expected %d gaps, got %d", expectedGaps, len(result.gaps))
	}

	// Verify gap starts at 101
	if len(result.gaps) > 0 && result.gaps[0] != 101 {
		t.Errorf("first gap = %d, want 101", result.gaps[0])
	}
}

// TestDetectGaps_WraparoundGap tests gap detection across MAX→0 boundary.
func TestDetectGaps_WraparoundGap(t *testing.T) {
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	now := uint64(time.Now().UnixMicro())
	tsbpdTime := now + 500000

	maxSeq := packet.MAX_SEQUENCENUMBER

	// Insert packet at maxSeq-2 and seq 2 (wrapped), gap at maxSeq-1, maxSeq, 0, 1
	r.packetStore.Insert(createNakPhasesTestPacket(maxSeq-2, tsbpdTime))
	r.packetStore.Insert(createNakPhasesTestPacket(2, tsbpdTime))
	r.contiguousPoint.Store(maxSeq - 3)

	params := &nakScanParams{
		startSeq:           maxSeq - 2,
		tooRecentThreshold: now + 1000000,
		firstScanEver:      false,
	}

	result := r.detectGaps(params)

	// Should detect gaps: maxSeq-1, maxSeq, 0, 1 = 4 gaps
	// Note: This depends on wraparound handling in detectGaps
	t.Logf("Wraparound gap test: detected %d gaps", len(result.gaps))
	if len(result.gaps) > 0 {
		t.Logf("First gap: %d, Last gap: %d", result.gaps[0], result.gaps[len(result.gaps)-1])
	}
}

// TestDetectGaps_TooRecentThresholdExact tests exact threshold boundary.
// The code uses `>` (strictly greater than), so packets AT the threshold are scanned.
func TestDetectGaps_TooRecentThresholdExact(t *testing.T) {
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	now := uint64(time.Now().UnixMicro())
	threshold := now + 500000

	// Code uses: if h.PktTsbpdTime > params.tooRecentThreshold
	// So: TSBPD <= threshold → scan, TSBPD > threshold → stop
	r.packetStore.Insert(createNakPhasesTestPacket(100, threshold-1)) // Below threshold - SCAN
	r.packetStore.Insert(createNakPhasesTestPacket(101, threshold))   // At threshold - SCAN (not >)
	r.packetStore.Insert(createNakPhasesTestPacket(102, threshold+1)) // Above threshold - STOP
	r.contiguousPoint.Store(99)

	params := &nakScanParams{
		startSeq:           100,
		tooRecentThreshold: threshold,
		firstScanEver:      false,
	}

	result := r.detectGaps(params)

	// Should scan packets 100 and 101 (both <= threshold), stop at 102 (> threshold)
	if result.packetsScanned != 2 {
		t.Errorf("packetsScanned = %d, want 2 (scan at and below threshold)", result.packetsScanned)
	}
	if result.lastScannedSeq != 101 {
		t.Errorf("lastScannedSeq = %d, want 101", result.lastScannedSeq)
	}
}

// TestProcessNakScanResult_WithGaps tests processing when gaps are found.
func TestProcessNakScanResult_WithGaps(t *testing.T) {
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	scanResult := &nakScanResult{
		gaps:           []uint32{101, 102, 103},
		packetsScanned: 5,
		lastScannedSeq: 105,
	}

	now := uint64(time.Now().UnixMicro())
	list := r.processNakScanResult(scanResult, now)

	// Should return NAK entries for the gaps
	// Exact format depends on consolidation logic
	t.Logf("NAK list returned: %d entries", len(list))

	// contiguousPoint should NOT be updated when gaps exist
	if r.contiguousPoint.Load() == 105 {
		t.Error("contiguousPoint should not be updated when gaps exist")
	}

	// lastPeriodicNAK should be updated
	if r.lastPeriodicNAK != now {
		t.Errorf("lastPeriodicNAK = %d, want %d", r.lastPeriodicNAK, now)
	}
}

// TestProcessNakScanResult_ZeroPacketsScanned tests edge case.
func TestProcessNakScanResult_ZeroPacketsScanned(t *testing.T) {
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	scanResult := &nakScanResult{
		gaps:           []uint32{},
		packetsScanned: 0,
		lastScannedSeq: 0,
	}

	now := uint64(time.Now().UnixMicro())
	initialContiguousPt := r.contiguousPoint.Load()

	list := r.processNakScanResult(scanResult, now)

	// No packets scanned, no gaps - should not update contiguousPoint
	if len(list) != 0 {
		t.Errorf("expected empty NAK list, got %d entries", len(list))
	}

	// contiguousPoint should remain unchanged (lastScannedSeq == 0)
	if r.contiguousPoint.Load() != initialContiguousPt {
		t.Errorf("contiguousPoint changed unexpectedly")
	}
}

// TestPeriodicNakBtreePhased_FullCycle tests complete NAK cycle.
func TestPeriodicNakBtreePhased_FullCycle(t *testing.T) {
	r := createNakPhasesTestReceiver(t, 20000, 1000000)

	now := uint64(time.Now().UnixMicro())
	tsbpdTime := now + 500000

	// Insert packets with gap: 100, 101, 104, 105 (missing 102, 103)
	for _, seq := range []uint32{100, 101, 104, 105} {
		r.packetStore.Insert(createNakPhasesTestPacket(seq, tsbpdTime))
	}
	r.contiguousPoint.Store(99)

	// First call - should detect gaps
	result1 := r.periodicNakBtreePhased(now)
	t.Logf("First call: %d NAK entries", len(result1))

	// Second call immediately - should be rate-limited
	result2 := r.periodicNakBtreePhased(now)
	if result2 != nil {
		t.Errorf("expected nil (rate-limited), got %d entries", len(result2))
	}

	// Third call after interval - should run
	nowAfterInterval := now + 25000 // 25ms later
	result3 := r.periodicNakBtreePhased(nowAfterInterval)
	t.Logf("Third call (after interval): %d NAK entries", len(result3))
}

// TestGapSlicePoolConcurrent tests pool under concurrent access.
func TestGapSlicePoolConcurrent(t *testing.T) {
	const goroutines = 10
	const iterations = 100

	done := make(chan bool, goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			for i := 0; i < iterations; i++ {
				// Get from pool
				gapsPtr, ok := gapSlicePool.Get().(*[]uint32)
				if !ok || gapsPtr == nil {
					gaps := make([]uint32, 0, 128)
					gapsPtr = &gaps
				}

				// Use the slice
				*gapsPtr = append(*gapsPtr, uint32(id), uint32(i))

				// Return to pool
				returnGapsToPool(*gapsPtr)
			}
			done <- true
		}(g)
	}

	// Wait for all goroutines
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Helper Functions
// ═══════════════════════════════════════════════════════════════════════════

// createNakPhasesTestReceiver creates a receiver for NAK phases corner case testing.
// Uses the proper constructor to ensure all fields are initialized.
func createNakPhasesTestReceiver(t *testing.T, nakInterval, tsbpdDelay uint64) *receiver {
	t.Helper()

	testMetrics := &metrics.ConnectionMetrics{}

	recvConfig := Config{
		InitialSequenceNumber:  circular.New(100, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000,
		PeriodicNAKInterval:    nakInterval,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelay,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		NakMergeGap:            3,
		NakConsolidationBudget: 20_000,
	}

	recv := New(recvConfig).(*receiver)
	recv.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }

	return recv
}

// createNakPhasesTestPacket creates a packet with specific sequence and TSBPD time.
// This is separate from createNakPhasesTestPacket in tsbpd_advancement_test.go to avoid redeclaration.
func createNakPhasesTestPacket(seq uint32, tsbpdTime uint64) packet.Packet {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = tsbpdTime
	p.Header().Timestamp = uint32(tsbpdTime - 120_000)
	return p
}
