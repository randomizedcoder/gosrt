package live

import (
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

	// Initialize rate stats with reasonable defaults
	r.rate.packetsPerSecond = 500.0 // 500 pps default

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

	r.rate.packetsPerSecond = 1000.0

	estimate := r.packetsPerSecondEstimate()

	if estimate != 1000.0 {
		t.Errorf("Expected 1000.0, got %f", estimate)
	}
}

func TestBuildNakListLocked_Empty(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	list := r.buildNakListLocked(uint64(time.Now().UnixMicro()))

	if len(list) != 0 {
		t.Errorf("Expected empty list for empty NAK btree, got %d entries", len(list))
	}
}

func TestBuildNakListLocked_NoNakBtree(t *testing.T) {
	r := createTestReceiverForFastNak(t)
	r.nakBtree = nil

	list := r.buildNakListLocked(uint64(time.Now().UnixMicro()))

	if list != nil {
		t.Error("Expected nil for nil NAK btree")
	}
}

func TestBuildNakListLocked_WithEntries(t *testing.T) {
	r := createTestReceiverForFastNak(t)

	r.nakBtree.Insert(100)
	r.nakBtree.Insert(101)
	r.nakBtree.Insert(102)

	list := r.buildNakListLocked(uint64(time.Now().UnixMicro()))

	if len(list) != 2 { // Should consolidate to single range
		t.Errorf("Expected 2 entries (one range), got %d", len(list))
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
	r.rate.packetsPerSecond = 500.0

	now := time.Now()
	r.lastPacketArrivalTime.Store(now.Add(-10 * time.Millisecond)) // Short silence
	r.lastDataPacketSeq.Store(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.checkFastNakRecent(uint32(101+i%10), now)
	}
}

