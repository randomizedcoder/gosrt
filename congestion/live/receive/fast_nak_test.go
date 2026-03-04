package receive

import (
	"math"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
)

// ═══════════════════════════════════════════════════════════════════════════
// Fast NAK Tests - UNIQUE TESTS ONLY
// Duplicated tests moved to fast_nak_table_test.go
// ═══════════════════════════════════════════════════════════════════════════

// createTestReceiverForFastNak creates a receiver with FastNAK enabled for testing.
func createTestReceiverForFastNak(t *testing.T) *receiver {
	t.Helper()

	m := &metrics.ConnectionMetrics{}

	r := &receiver{
		nakBtree:             newNakBtree(32),
		useNakBtree:          true,
		fastNakEnabled:       true,
		fastNakThreshold:     50 * time.Millisecond,
		fastNakRecentEnabled: true,
		periodicNAKInterval:  20000, // 20ms in microseconds
		nakMergeGap:          3,
		metrics:              m,
	}
	r.setupNakDispatch(false) // Use locking versions for tests

	// Initialize rate stats with reasonable defaults (Phase 1: Lockless)
	m.RecvRatePacketsPerSec.Store(math.Float64bits(500.0)) // 500 pps default

	return r
}

// ═══════════════════════════════════════════════════════════════════════════
// AtomicTime Tests (unique - tests AtomicTime type)
// ═══════════════════════════════════════════════════════════════════════════

func TestAtomicTime_LoadStore(t *testing.T) {
	var at AtomicTime

	// Load from uninitialized should return zero time
	if !at.Load().IsZero() {
		t.Error("Expected zero time from uninitialized AtomicTime")
	}

	// Store and load
	now := time.Now()
	at.Store(now)

	loaded := at.Load()
	if !loaded.Equal(now) {
		t.Errorf("Expected %v, got %v", now, loaded)
	}
}

func TestAtomicTime_ConcurrentAccess(t *testing.T) {
	var at AtomicTime

	done := make(chan struct{})

	// Writer goroutine
	go func() {
		for i := 0; i < 1000; i++ {
			at.Store(time.Now())
		}
		done <- struct{}{}
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 1000; i++ {
			_ = at.Load()
		}
		done <- struct{}{}
	}()

	<-done
	<-done
	// If we get here without race, test passes
}

// ═══════════════════════════════════════════════════════════════════════════
// PacketsPerSecondEstimate Test (unique - tests pps estimate function)
// ═══════════════════════════════════════════════════════════════════════════

func TestPacketsPerSecondEstimate(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(1000.0))

	estimate := r.packetsPerSecondEstimate()

	if estimate != 1000.0 {
		t.Errorf("Expected 1000.0, got %f", estimate)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Complex Scenario Tests (unique - multi-burst, consolidation integration)
// ═══════════════════════════════════════════════════════════════════════════

func TestCheckFastNakRecent_MultipleBurstLosses(t *testing.T) {
	// Scenario: Multiple short outages with gaps between them (Starlink pattern)
	// Each outage is ~60ms at 5000pps = ~300 packets
	// Between outages, some packets arrive successfully (creating gaps in loss)
	m := &metrics.ConnectionMetrics{}
	r := &receiver{
		nakBtree:               newNakBtree(32),
		useNakBtree:            true,
		fastNakEnabled:         true,
		fastNakThreshold:       50 * time.Millisecond,
		fastNakRecentEnabled:   true,
		periodicNAKInterval:    20000,
		nakMergeGap:            3,
		nakConsolidationBudget: 5 * time.Second,
		metrics:                m,
	}
	r.setupNakDispatch(false) // Use locking versions for tests
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(5000.0))

	baseTime := time.Now()

	// Burst 1: sequences 1001-1300 (300 packets lost)
	r.lastDataPacketSeq.Store(1000)
	r.lastPacketArrivalTime.Store(baseTime.Add(-300 * time.Millisecond))
	r.checkFastNakRecent(1300, baseTime.Add(-240*time.Millisecond))

	// Some packets arrive successfully: 1300-1500 (gap in losses)
	r.lastDataPacketSeq.Store(1500)
	r.lastPacketArrivalTime.Store(baseTime.Add(-200 * time.Millisecond))

	// Burst 2: sequences 1501-1800 (300 packets lost)
	r.checkFastNakRecent(1800, baseTime.Add(-140*time.Millisecond))

	// More packets arrive successfully: 1800-2000 (gap in losses)
	r.lastDataPacketSeq.Store(2000)
	r.lastPacketArrivalTime.Store(baseTime.Add(-100 * time.Millisecond))

	// Burst 3: sequences 2001-2300 (300 packets lost)
	r.checkFastNakRecent(2300, baseTime.Add(-40*time.Millisecond))

	inserts := r.metrics.NakFastRecentInserts.Load()
	t.Logf("Total FastNAKRecent inserts after 3 bursts: %d", inserts)

	// Should have ~900 total (3 * 300)
	if inserts < 700 || inserts > 1100 {
		t.Errorf("Expected ~900 inserts for 3 bursts, got %d", inserts)
	}

	// Consolidate - should get 3 separate ranges due to gaps between bursts
	list := r.consolidateNakBtree()
	entries := len(list) / 2

	t.Logf("Consolidated to %d entries from 3 burst losses", entries)

	// Due to large gaps between bursts (200 packets > nakMergeGap=3), should have 3 separate ranges
	if entries != 3 {
		t.Errorf("Expected 3 ranges from 3 separate bursts (gaps > nakMergeGap), got %d", entries)
	}

	// Verify ranges are non-overlapping
	if entries >= 3 && len(list) >= 6 {
		t.Logf("Range 1: %d-%d", list[0].Val(), list[1].Val())
		t.Logf("Range 2: %d-%d", list[2].Val(), list[3].Val())
		t.Logf("Range 3: %d-%d", list[4].Val(), list[5].Val())
	}
}

func TestCheckFastNakRecent_LargeBurstThenConsolidate(t *testing.T) {
	// Full integration: Large burst → consolidate → verify NAK format
	r := createTestReceiverForFastNak(t)
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(5000.0))
	r.nakMergeGap = 3
	r.nakConsolidationBudget = 1 * time.Second

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-60 * time.Millisecond))
	r.lastDataPacketSeq.Store(10000)

	// Create burst loss
	newSeq := uint32(10300)
	r.checkFastNakRecent(newSeq, now)

	// Consolidate
	list := r.consolidateNakBtree()

	// Should be exactly 1 range entry (2 elements: start, end)
	if len(list) != 2 {
		t.Fatalf("Expected 2 list elements (1 range), got %d", len(list))
	}

	start := list[0].Val()
	end := list[1].Val()

	t.Logf("Burst loss range: %d to %d (%d packets)", start, end, end-start+1)

	// Verify the range
	expectedStart := uint32(10001)
	expectedEnd := newSeq - 1

	if start != expectedStart {
		t.Errorf("Expected range start %d, got %d", expectedStart, start)
	}
	if end != expectedEnd {
		t.Errorf("Expected range end %d, got %d", expectedEnd, end)
	}

	// This single range represents ~300 packets but only 8 bytes on wire
	t.Logf("✅ 300-packet burst loss encoded as single 8-byte range entry")
}

func TestCheckFastNakRecent_LargeBurstWithPriorGaps(t *testing.T) {
	// Scenario: Some gaps already in NAK btree, then large burst
	r := createTestReceiverForFastNak(t)
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(5000.0))
	r.nakMergeGap = 3
	r.nakConsolidationBudget = 1 * time.Second

	// Pre-existing gaps (singles from earlier modulus-like drops)
	for i := 100; i <= 500; i += 50 {
		r.nakBtree.InsertLocking(uint32(i))
	}
	priorGaps := r.nakBtree.LenLocking()

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-60 * time.Millisecond))
	r.lastDataPacketSeq.Store(10000)

	// Large burst loss
	newSeq := uint32(10300)
	r.checkFastNakRecent(newSeq, now)

	totalEntries := r.nakBtree.LenLocking()
	t.Logf("Prior gaps: %d, After burst: %d total", priorGaps, totalEntries)

	// Consolidate
	list := r.consolidateNakBtree()
	entries := len(list) / 2

	t.Logf("Consolidated to %d entries (prior singles + burst range)", entries)

	// Should have: 9 singles (100, 150, ..., 500) + 1 burst range
	expectedEntries := 9 + 1 // 9 singles + 1 range
	if entries != expectedEntries {
		t.Errorf("Expected %d entries, got %d", expectedEntries, entries)
	}
}

func TestCheckFastNakRecent_VeryLongOutage(t *testing.T) {
	// Scenario: Very long outage (500ms) - tests cap logic
	r := createTestReceiverForFastNak(t)
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(5000.0))

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-500 * time.Millisecond)) // 500ms outage!
	r.lastDataPacketSeq.Store(10000)

	// 500ms at 5000pps = 2500 packets
	newSeq := uint32(12500)
	r.checkFastNakRecent(newSeq, now)

	inserts := r.metrics.NakFastRecentInserts.Load()
	overflow := r.metrics.NakFastRecentOverflow.Load()

	t.Logf("Very long outage (500ms): %d inserts, %d overflow", inserts, overflow)

	// Verify entries were capped or inserted appropriately
	btreeLen := r.nakBtree.LenLocking()
	if btreeLen == 0 {
		t.Error("Should have entries in NAK btree for long outage")
	}

	// Consolidate and verify
	list := r.consolidateNakBtree()
	if len(list) < 2 {
		t.Error("Should have at least one range entry")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Benchmarks
// ═══════════════════════════════════════════════════════════════════════════

func BenchmarkFastNakRecent_SmallBurst(b *testing.B) {
	benchmarkFastNakRecentBurst(b, 50)
}

func BenchmarkFastNakRecent_MediumBurst(b *testing.B) {
	benchmarkFastNakRecentBurst(b, 300)
}

func BenchmarkFastNakRecent_LargeBurst(b *testing.B) {
	benchmarkFastNakRecentBurst(b, 1200)
}

func BenchmarkFastNakRecent_VeryLargeBurst(b *testing.B) {
	benchmarkFastNakRecentBurst(b, 5000)
}

func benchmarkFastNakRecentBurst(b *testing.B, burstSize int) {
	for i := 0; i < b.N; i++ {
		m := &metrics.ConnectionMetrics{}
		r := &receiver{
			nakBtree:             newNakBtree(32),
			useNakBtree:          true,
			fastNakEnabled:       true,
			fastNakThreshold:     50 * time.Millisecond,
			fastNakRecentEnabled: true,
			periodicNAKInterval:  20000,
			metrics:              m,
		}
		r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(5000.0))

		now := time.Now()
		r.lastPacketArrivalTime.Store(now.Add(-60 * time.Millisecond))
		r.lastDataPacketSeq.Store(10000)

		r.checkFastNakRecent(uint32(10000+burstSize), now)
	}
}
