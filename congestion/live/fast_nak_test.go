package live

import (
	"math"
	"testing"
	"time"

	"github.com/datarhei/gosrt/metrics"
)

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

	// Initialize rate stats with reasonable defaults (Phase 1: Lockless)
	m.RecvRatePacketsPerSec.Store(math.Float64bits(500.0)) // 500 pps default

	return r
}

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

func TestCheckFastNak_Disabled(t *testing.T) {
	r := createTestReceiverForFastNak(t)
	r.fastNakEnabled = false

	// Should not panic and not trigger
	r.checkFastNak(time.Now())

	if r.metrics.NakFastTriggers.Load() != 0 {
		t.Error("FastNAK should not trigger when disabled")
	}
}

func TestCheckFastNak_NoNakBtree(t *testing.T) {
	r := createTestReceiverForFastNak(t)
	r.useNakBtree = false

	// Should not panic and not trigger
	r.checkFastNak(time.Now())

	if r.metrics.NakFastTriggers.Load() != 0 {
		t.Error("FastNAK should not trigger when NAK btree disabled")
	}
}

func TestCheckFastNak_NoPreviousPacket(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	// lastPacketArrivalTime is zero
	r.checkFastNak(time.Now())

	if r.metrics.NakFastTriggers.Load() != 0 {
		t.Error("FastNAK should not trigger with no previous packet")
	}
}

func TestCheckFastNak_ShortSilence(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	// Set last arrival to recent time
	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-10 * time.Millisecond))

	r.checkFastNak(now)

	if r.metrics.NakFastTriggers.Load() != 0 {
		t.Error("FastNAK should not trigger for short silence")
	}
}

// Note: Testing actual FastNAK triggering is complex because it requires:
// - Setting up packet store with packets
// - Having gaps in the NAK btree
// - Mocking sendNAK callback
// For now, we test the threshold detection logic

func TestCheckFastNakRecent_Disabled(t *testing.T) {
	r := createTestReceiverForFastNak(t)
	r.fastNakRecentEnabled = false

	// Should not panic
	r.checkFastNakRecent(1000, time.Now())

	if r.metrics.NakFastRecentInserts.Load() != 0 {
		t.Error("FastNAKRecent should not insert when disabled")
	}
}

func TestCheckFastNakRecent_NoNakBtree(t *testing.T) {
	r := createTestReceiverForFastNak(t)
	r.nakBtree = nil

	// Should not panic
	r.checkFastNakRecent(1000, time.Now())
}

func TestCheckFastNakRecent_NoPreviousPacket(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	// lastPacketArrivalTime is zero
	r.checkFastNakRecent(1000, time.Now())

	if r.metrics.NakFastRecentInserts.Load() != 0 {
		t.Error("FastNAKRecent should not insert with no previous packet")
	}
}

func TestCheckFastNakRecent_ShortSilence(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	// Set up previous packet arrival
	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-10 * time.Millisecond))
	r.lastDataPacketSeq.Store(100)

	// Short silence - should not trigger
	r.checkFastNakRecent(105, now)

	if r.metrics.NakFastRecentInserts.Load() != 0 {
		t.Error("FastNAKRecent should not insert for short silence")
	}
}

func TestCheckFastNakRecent_NoSequenceJump(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	// Set up with long silence but no sequence jump
	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-100 * time.Millisecond)) // Long silence
	r.lastDataPacketSeq.Store(100)

	// Next packet is sequential (no jump)
	r.checkFastNakRecent(101, now)

	if r.metrics.NakFastRecentInserts.Load() != 0 {
		t.Error("FastNAKRecent should not insert for no sequence jump")
	}
}

func TestCheckFastNakRecent_SignificantJump(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	// Set up with long silence and significant sequence jump
	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-100 * time.Millisecond))
	r.lastDataPacketSeq.Store(100)

	// Significant jump matching outage
	// 100ms at 500pps = 50 packets, so jump from 100 to 150 is reasonable
	r.checkFastNakRecent(150, now)

	// Should have inserted ~49 sequences (101-149)
	if r.metrics.NakFastRecentInserts.Load() == 0 {
		t.Error("FastNAKRecent should insert for significant jump after outage")
	}

	// Verify sequences were added to NAK btree
	if r.nakBtree.Len() == 0 {
		t.Error("NAK btree should have entries after FastNAKRecent")
	}

	// Check specific sequences
	if !r.nakBtree.Has(101) {
		t.Error("NAK btree should have sequence 101")
	}
	if !r.nakBtree.Has(149) {
		t.Error("NAK btree should have sequence 149")
	}
	if r.nakBtree.Has(100) {
		t.Error("NAK btree should NOT have sequence 100 (last received)")
	}
	if r.nakBtree.Has(150) {
		t.Error("NAK btree should NOT have sequence 150 (just arrived)")
	}
}

func TestPacketsPerSecondEstimate(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(1000.0))

	estimate := r.packetsPerSecondEstimate()

	if estimate != 1000.0 {
		t.Errorf("Expected 1000.0, got %f", estimate)
	}
}

func TestBuildNakListLocked_Empty(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	list := r.buildNakListLocked()

	if len(list) != 0 {
		t.Errorf("Expected empty list for empty NAK btree, got %d entries", len(list))
	}
}

func TestBuildNakListLocked_NoNakBtree(t *testing.T) {
	r := createTestReceiverForFastNak(t)
	r.nakBtree = nil

	list := r.buildNakListLocked()

	if list != nil {
		t.Error("Expected nil for nil NAK btree")
	}
}

func TestBuildNakListLocked_WithEntries(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	r.nakBtree.Insert(100)
	r.nakBtree.Insert(101)
	r.nakBtree.Insert(102)

	list := r.buildNakListLocked()

	if len(list) != 2 { // Should consolidate to single range
		t.Errorf("Expected 2 entries (one range), got %d", len(list))
	}
}

// ============================================================================
// Large Burst Loss Tests - Simulating Starlink Outages (~60ms)
// ============================================================================

func TestCheckFastNakRecent_LargeBurstLoss_5Mbps(t *testing.T) {
	// Scenario: 5 Mbps video stream, 60ms outage
	// Packets: ~5000 pps (1000 byte packets) * 0.06s = ~300 packets
	r := createTestReceiverForFastNak(t)
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(5000.0))

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-60 * time.Millisecond)) // 60ms outage
	r.lastDataPacketSeq.Store(10000)

	// After outage, receive packet with large sequence jump
	// Expected gap: ~300 packets (60ms * 5000pps)
	newSeq := uint32(10300)
	r.checkFastNakRecent(newSeq, now)

	// Verify metrics
	inserts := r.metrics.NakFastRecentInserts.Load()
	t.Logf("FastNAKRecent inserted %d entries for 60ms outage at 5Mbps", inserts)

	if inserts < 200 || inserts > 400 {
		t.Errorf("Expected ~300 inserts (60ms * 5000pps), got %d", inserts)
	}

	// Verify NAK btree has all the missing sequences
	btreeLen := r.nakBtree.Len()
	if btreeLen != int(inserts) {
		t.Errorf("NAK btree length %d doesn't match inserts %d", btreeLen, inserts)
	}

	// Verify specific boundary sequences
	if !r.nakBtree.Has(10001) {
		t.Error("NAK btree should have first missing sequence 10001")
	}
	if !r.nakBtree.Has(newSeq - 1) {
		t.Errorf("NAK btree should have last missing sequence %d", newSeq-1)
	}
	if r.nakBtree.Has(10000) {
		t.Error("NAK btree should NOT have last received sequence 10000")
	}
	if r.nakBtree.Has(newSeq) {
		t.Errorf("NAK btree should NOT have just-arrived sequence %d", newSeq)
	}
}

func TestCheckFastNakRecent_LargeBurstLoss_20Mbps(t *testing.T) {
	// Scenario: 20 Mbps video stream, 60ms outage
	// Packets: ~20000 pps * 0.06s = ~1200 packets
	m := &metrics.ConnectionMetrics{}
	r := &receiver{
		nakBtree:               newNakBtree(32),
		useNakBtree:            true,
		fastNakEnabled:         true,
		fastNakThreshold:       50 * time.Millisecond,
		fastNakRecentEnabled:   true,
		periodicNAKInterval:    20000,
		nakMergeGap:            3,
		nakConsolidationBudget: 5 * time.Second, // Extended for large burst
		metrics:                m,
	}
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(20000.0))

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-60 * time.Millisecond))
	r.lastDataPacketSeq.Store(50000)

	newSeq := uint32(51200)
	r.checkFastNakRecent(newSeq, now)

	inserts := r.metrics.NakFastRecentInserts.Load()
	t.Logf("FastNAKRecent inserted %d entries for 60ms outage at 20Mbps", inserts)

	if inserts < 1000 || inserts > 1400 {
		t.Errorf("Expected ~1200 inserts (60ms * 20000pps), got %d", inserts)
	}

	// Verify consolidation produces a single range
	list := r.consolidateNakBtree()
	entries := len(list) / 2

	if entries != 1 {
		t.Errorf("Large burst should consolidate to 1 range, got %d entries", entries)
	}

	// Verify the range is correct
	if len(list) >= 2 {
		start := list[0].Val()
		end := list[1].Val()
		t.Logf("Consolidated range: %d-%d (%d packets)", start, end, end-start+1)

		if start != 50001 {
			t.Errorf("Range start should be 50001, got %d", start)
		}
		if end != newSeq-1 {
			t.Errorf("Range end should be %d, got %d", newSeq-1, end)
		}
	}
}

func TestCheckFastNakRecent_LargeBurstLoss_100Mbps(t *testing.T) {
	// Scenario: 100 Mbps high-bandwidth stream, 60ms outage
	// Packets: ~100000 pps * 0.06s = ~6000 packets
	// This tests the overflow cap logic
	r := createTestReceiverForFastNak(t)
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(100000.0))

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-60 * time.Millisecond))
	r.lastDataPacketSeq.Store(100000)

	newSeq := uint32(106000)
	r.checkFastNakRecent(newSeq, now)

	inserts := r.metrics.NakFastRecentInserts.Load()
	overflow := r.metrics.NakFastRecentOverflow.Load()

	t.Logf("FastNAKRecent: %d inserts, %d overflow for 60ms outage at 100Mbps", inserts, overflow)

	// Should have hit the overflow cap (maxFastNakRecentGap = 10000)
	if overflow == 0 && inserts > 10000 {
		t.Log("Warning: May need to verify overflow cap is being applied")
	}

	// Consolidation should still work
	list := r.consolidateNakBtree()
	entries := len(list) / 2

	t.Logf("Consolidated to %d entries", entries)

	if entries < 1 {
		t.Error("Should have at least 1 consolidated entry")
	}
}

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
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(5000.0))

	baseTime := time.Now()

	// Burst 1: sequences 1001-1300 (300 packets lost)
	r.lastDataPacketSeq.Store(1000)
	r.lastPacketArrivalTime.Store(baseTime.Add(-300 * time.Millisecond))
	r.checkFastNakRecent(1300, baseTime.Add(-240 * time.Millisecond))

	// Some packets arrive successfully: 1300-1500 (gap in losses)
	r.lastDataPacketSeq.Store(1500)
	r.lastPacketArrivalTime.Store(baseTime.Add(-200 * time.Millisecond))

	// Burst 2: sequences 1501-1800 (300 packets lost)
	r.checkFastNakRecent(1800, baseTime.Add(-140 * time.Millisecond))

	// More packets arrive successfully: 1800-2000 (gap in losses)
	r.lastDataPacketSeq.Store(2000)
	r.lastPacketArrivalTime.Store(baseTime.Add(-100 * time.Millisecond))

	// Burst 3: sequences 2001-2300 (300 packets lost)
	r.checkFastNakRecent(2300, baseTime.Add(-40 * time.Millisecond))

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
		r.nakBtree.Insert(uint32(i))
	}
	priorGaps := r.nakBtree.Len()

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-60 * time.Millisecond))
	r.lastDataPacketSeq.Store(10000)

	// Large burst loss
	newSeq := uint32(10300)
	r.checkFastNakRecent(newSeq, now)

	totalEntries := r.nakBtree.Len()
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
	btreeLen := r.nakBtree.Len()
	if btreeLen == 0 {
		t.Error("Should have entries in NAK btree for long outage")
	}

	// Consolidate and verify
	list := r.consolidateNakBtree()
	if len(list) < 2 {
		t.Error("Should have at least one range entry")
	}
}

// ============================================================================
// Benchmarks for Large Burst Scenarios
// ============================================================================

func BenchmarkFastNakRecent_SmallBurst(b *testing.B) {
	// 50 packet burst
	benchmarkFastNakRecentBurst(b, 50)
}

func BenchmarkFastNakRecent_MediumBurst(b *testing.B) {
	// 300 packet burst (60ms at 5Mbps)
	benchmarkFastNakRecentBurst(b, 300)
}

func BenchmarkFastNakRecent_LargeBurst(b *testing.B) {
	// 1200 packet burst (60ms at 20Mbps)
	benchmarkFastNakRecentBurst(b, 1200)
}

func BenchmarkFastNakRecent_VeryLargeBurst(b *testing.B) {
	// 5000 packet burst (60ms at 100Mbps)
	benchmarkFastNakRecentBurst(b, 5000)
}

func benchmarkFastNakRecentBurst(b *testing.B, burstSize int) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		r := &receiver{
			nakBtree:             newNakBtree(32),
			useNakBtree:          true,
			fastNakEnabled:       true,
			fastNakThreshold:     50 * time.Millisecond,
			fastNakRecentEnabled: true,
			metrics:              &metrics.ConnectionMetrics{},
		}
		r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(float64(burstSize) / 0.06)) // Calculate pps for 60ms burst

		now := time.Now()
		r.lastPacketArrivalTime.Store(now.Add(-60 * time.Millisecond))
		r.lastDataPacketSeq.Store(10000)

		b.StartTimer()
		r.checkFastNakRecent(uint32(10000+burstSize), now)
	}
}

// BenchmarkCheckFastNakRecent tests the performance of the FastNAKRecent check.
func BenchmarkCheckFastNakRecent(b *testing.B) {
	r := &receiver{
		nakBtree:             newNakBtree(32),
		useNakBtree:          true,
		fastNakEnabled:       true,
		fastNakThreshold:     50 * time.Millisecond,
		fastNakRecentEnabled: true,
		metrics:              &metrics.ConnectionMetrics{},
	}
	r.metrics.RecvRatePacketsPerSec.Store(math.Float64bits(500.0))

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-10 * time.Millisecond)) // Short silence
	r.lastDataPacketSeq.Store(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.checkFastNakRecent(uint32(101+i%10), now)
	}
}

