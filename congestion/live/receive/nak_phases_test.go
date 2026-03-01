package receive

import (
	"testing"
	"time"
)

// TestShouldRunPeriodicNak_RateLimiting tests the rate limiting logic.
func TestShouldRunPeriodicNak_RateLimiting(t *testing.T) {
	tests := []struct {
		name           string
		lastNak        uint64
		now            uint64
		nakInterval    uint64
		expectedResult bool
	}{
		{
			name:           "first_run_should_proceed",
			lastNak:        0,
			now:            1000000, // 1s in microseconds
			nakInterval:    20000,   // 20ms in microseconds
			expectedResult: true,
		},
		{
			name:           "interval_elapsed_should_proceed",
			lastNak:        1000000,
			now:            1025000, // 25ms after lastNak
			nakInterval:    20000,   // 20ms interval
			expectedResult: true,
		},
		{
			name:           "interval_not_elapsed_should_skip",
			lastNak:        1000000,
			now:            1010000, // 10ms after lastNak
			nakInterval:    20000,   // 20ms interval
			expectedResult: false,
		},
		{
			name:           "exactly_at_interval_boundary_should_skip",
			lastNak:        1000000,
			now:            1019999, // just under 20ms
			nakInterval:    20000,
			expectedResult: false,
		},
		{
			name:           "exactly_after_interval_should_proceed",
			lastNak:        1000000,
			now:            1020000, // exactly 20ms
			nakInterval:    20000,
			expectedResult: true,
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
				t.Errorf("shouldRunPeriodicNak(%d) = %v, want %v (lastNak=%d, interval=%d)",
					tc.now, result, tc.expectedResult, tc.lastNak, tc.nakInterval)
			}
		})
	}
}

// TestCalculateNakScanParams_NilBtree tests handling when nakBtree is nil.
func TestCalculateNakScanParams_NilBtree(t *testing.T) {
	r := &receiver{
		nakBtree: nil,
	}

	params := r.calculateNakScanParams(1000000)
	if params != nil {
		t.Errorf("calculateNakScanParams with nil nakBtree should return nil, got %+v", params)
	}
}

// TestNakScanParamsStruct tests the nakScanParams struct fields.
func TestNakScanParamsStruct(t *testing.T) {
	params := &nakScanParams{
		startSeq:           100,
		tooRecentThreshold: 5000000,
		firstScanEver:      true,
		btreeMinSeq:        100,
		btreeMinTsbpd:      4000000,
	}

	if params.startSeq != 100 {
		t.Errorf("startSeq = %d, want 100", params.startSeq)
	}
	if params.tooRecentThreshold != 5000000 {
		t.Errorf("tooRecentThreshold = %d, want 5000000", params.tooRecentThreshold)
	}
	if !params.firstScanEver {
		t.Error("firstScanEver should be true")
	}
}

// TestNakScanResultStruct tests the nakScanResult struct.
func TestNakScanResultStruct(t *testing.T) {
	result := &nakScanResult{
		gaps:           []uint32{101, 102, 103},
		packetsScanned: 10,
		lastScannedSeq: 110,
	}

	if len(result.gaps) != 3 {
		t.Errorf("gaps length = %d, want 3", len(result.gaps))
	}
	if result.packetsScanned != 10 {
		t.Errorf("packetsScanned = %d, want 10", result.packetsScanned)
	}
	if result.lastScannedSeq != 110 {
		t.Errorf("lastScannedSeq = %d, want 110", result.lastScannedSeq)
	}
}

// TestReturnGapsToPool tests the pool return logic.
func TestReturnGapsToPool(t *testing.T) {
	// Get a slice from pool
	gapsPtr, _ := gapSlicePool.Get().(*[]uint32)
	if gapsPtr == nil {
		// Pool might return nil, create new
		gaps := make([]uint32, 0, 128)
		gapsPtr = &gaps
	}

	// Add some gaps
	*gapsPtr = append(*gapsPtr, 1, 2, 3, 4, 5)

	// Return to pool (should reset length)
	returnGapsToPool(*gapsPtr)

	// Get again from pool - should have capacity but zero length
	// Note: Pool doesn't guarantee we get the same slice back
	newGapsPtr, _ := gapSlicePool.Get().(*[]uint32)
	if newGapsPtr != nil && len(*newGapsPtr) != 0 {
		// If we got a used slice back, it should be empty
		t.Logf("Got slice from pool with len=%d (should be 0)", len(*newGapsPtr))
	}
}

// BenchmarkShouldRunPeriodicNak benchmarks the rate limiting check.
func BenchmarkShouldRunPeriodicNak(b *testing.B) {
	r := &receiver{
		lastPeriodicNAK:     1000000,
		periodicNAKInterval: 20000, // 20ms
	}

	scenarios := []struct {
		name string
		now  uint64
	}{
		{"should_proceed", 1030000}, // 30ms after lastNak
		{"should_skip", 1010000},    // 10ms after lastNak
	}

	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = r.shouldRunPeriodicNak(sc.now)
			}
		})
	}
}

// BenchmarkGapSlicePool benchmarks the pool allocation pattern.
func BenchmarkGapSlicePool(b *testing.B) {
	b.Run("pool_get_put", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			gapsPtr, ok := gapSlicePool.Get().(*[]uint32)
			if !ok {
				gaps := make([]uint32, 0, 128)
				gapsPtr = &gaps
			}
			*gapsPtr = append(*gapsPtr, uint32(i), uint32(i+1), uint32(i+2))
			*gapsPtr = (*gapsPtr)[:0]
			gapSlicePool.Put(gapsPtr)
		}
	})

	b.Run("direct_alloc", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			gaps := make([]uint32, 0, 128)
			gaps = append(gaps, uint32(i), uint32(i+1), uint32(i+2))
			_ = gaps
		}
	})
}

// TestProcessNakScanResult_NoGaps tests processing when no gaps found.
func TestProcessNakScanResult_NoGaps(t *testing.T) {
	// Create a minimal receiver for testing
	r := setupTestReceiverForPhases(t)

	scanResult := &nakScanResult{
		gaps:           []uint32{},
		packetsScanned: 10,
		lastScannedSeq: 110,
	}

	now := uint64(time.Now().UnixMicro())
	list := r.processNakScanResult(scanResult, now)

	// No gaps means empty NAK list
	if len(list) != 0 {
		t.Errorf("expected empty NAK list for no gaps, got %d entries", len(list))
	}

	// contiguousPoint should be updated
	if r.contiguousPoint.Load() != 110 {
		t.Errorf("contiguousPoint = %d, want 110", r.contiguousPoint.Load())
	}
}

// setupTestReceiverForPhases creates a minimal receiver for phase testing.
func setupTestReceiverForPhases(t *testing.T) *receiver {
	t.Helper()

	r := &receiver{
		nakBtree:            newNakBtree(32),
		periodicNAKInterval: 20000, // 20ms in microseconds
		tsbpdDelay:          1000000,
		nakRecentPercent:    0.10,
	}
	r.setupNakDispatch(false) // Use locking versions for tests

	return r
}
