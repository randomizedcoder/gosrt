package srt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ACK/ACKACK Redesign - Phase ACK-4: ackEntry btree tests
// Reference: documentation/ack_optimization_implementation.md

func TestAckEntryBtree_InsertGet(t *testing.T) {
	tree := newAckEntryBtree(4)

	now := time.Now()
	entry := &ackEntry{ackNum: 100, timestamp: now}

	tree.Insert(entry)

	// Get existing
	got := tree.Get(100)
	require.NotNil(t, got)
	require.Equal(t, uint32(100), got.ackNum)
	require.Equal(t, now, got.timestamp)

	// Get non-existing
	got = tree.Get(999)
	require.Nil(t, got)
}

func TestAckEntryBtree_Delete(t *testing.T) {
	tree := newAckEntryBtree(4)

	now := time.Now()
	tree.Insert(&ackEntry{ackNum: 100, timestamp: now})
	tree.Insert(&ackEntry{ackNum: 200, timestamp: now})

	require.Equal(t, 2, tree.Len())

	// Delete existing
	deleted := tree.Delete(100)
	require.NotNil(t, deleted)
	require.Equal(t, uint32(100), deleted.ackNum)
	require.Equal(t, 1, tree.Len())

	// Get deleted should return nil
	got := tree.Get(100)
	require.Nil(t, got)

	// Delete non-existing
	deleted = tree.Delete(999)
	require.Nil(t, deleted)
	require.Equal(t, 1, tree.Len())
}

func TestAckEntryBtree_DeleteMin(t *testing.T) {
	tree := newAckEntryBtree(4)

	now := time.Now()
	tree.Insert(&ackEntry{ackNum: 300, timestamp: now})
	tree.Insert(&ackEntry{ackNum: 100, timestamp: now})
	tree.Insert(&ackEntry{ackNum: 200, timestamp: now})

	// DeleteMin should return entries in order
	min := tree.DeleteMin()
	require.NotNil(t, min)
	require.Equal(t, uint32(100), min.ackNum)

	min = tree.DeleteMin()
	require.NotNil(t, min)
	require.Equal(t, uint32(200), min.ackNum)

	min = tree.DeleteMin()
	require.NotNil(t, min)
	require.Equal(t, uint32(300), min.ackNum)

	// Empty tree
	min = tree.DeleteMin()
	require.Nil(t, min)
}

func TestAckEntryBtree_Min(t *testing.T) {
	tree := newAckEntryBtree(4)

	// Empty tree
	min := tree.Min()
	require.Nil(t, min)

	now := time.Now()
	tree.Insert(&ackEntry{ackNum: 300, timestamp: now})
	tree.Insert(&ackEntry{ackNum: 100, timestamp: now})

	// Min should return smallest without removing
	min = tree.Min()
	require.NotNil(t, min)
	require.Equal(t, uint32(100), min.ackNum)
	require.Equal(t, 2, tree.Len()) // Length unchanged
}

func TestAckEntryBtree_ExpireOlderThan(t *testing.T) {
	tree := newAckEntryBtree(4)

	now := time.Now()
	tree.Insert(&ackEntry{ackNum: 100, timestamp: now})
	tree.Insert(&ackEntry{ackNum: 200, timestamp: now})
	tree.Insert(&ackEntry{ackNum: 300, timestamp: now})
	tree.Insert(&ackEntry{ackNum: 400, timestamp: now})

	// Expire entries < 250
	count, removed := tree.ExpireOlderThan(250)
	require.Equal(t, 2, count)
	require.Len(t, removed, 2)
	require.Equal(t, uint32(100), removed[0].ackNum)
	require.Equal(t, uint32(200), removed[1].ackNum)

	require.Equal(t, 2, tree.Len())

	// Verify remaining entries
	require.Nil(t, tree.Get(100))
	require.Nil(t, tree.Get(200))
	require.NotNil(t, tree.Get(300))
	require.NotNil(t, tree.Get(400))
}

func TestAckEntryBtree_ExpireOlderThan_Empty(t *testing.T) {
	tree := newAckEntryBtree(4)

	count, removed := tree.ExpireOlderThan(100)
	require.Equal(t, 0, count)
	require.Len(t, removed, 0)
}

func TestAckEntryPool(t *testing.T) {
	// Get from pool
	entry := GetAckEntry()
	require.NotNil(t, entry)
	require.Equal(t, uint32(0), entry.ackNum)
	require.True(t, entry.timestamp.IsZero())

	// Set values
	entry.ackNum = 123
	entry.timestamp = time.Now()

	// Return to pool
	PutAckEntry(entry)

	// Get again - should be reset
	entry2 := GetAckEntry()
	require.NotNil(t, entry2)
	require.Equal(t, uint32(0), entry2.ackNum)
	require.True(t, entry2.timestamp.IsZero())
}

func TestAckEntryBtree_InsertReplace(t *testing.T) {
	tree := newAckEntryBtree(4)

	now1 := time.Now()
	now2 := now1.Add(time.Second)

	entry1 := &ackEntry{ackNum: 100, timestamp: now1}
	tree.Insert(entry1)

	entry2 := &ackEntry{ackNum: 100, timestamp: now2} // Same ackNum, different time
	old := tree.Insert(entry2)

	// Should return the old entry
	require.NotNil(t, old)
	require.Equal(t, now1, old.timestamp)

	// Get should return new entry
	got := tree.Get(100)
	require.NotNil(t, got)
	require.Equal(t, now2, got.timestamp)

	// Still only one entry
	require.Equal(t, 1, tree.Len())
}

// ============================================================================
// Wraparound Tests (ACK numbers are 32-bit)
// ============================================================================

func TestGetNextACKNumber_Wraparound(t *testing.T) {
	// Create a minimal connection to test getNextACKNumber
	c := &srtConn{}

	// Set nextACKNumber to near max
	c.nextACKNumber.Store(0xFFFFFFFE)

	// Get next - should be 0xFFFFFFFE
	ack1 := c.getNextACKNumber()
	require.Equal(t, uint32(0xFFFFFFFE), ack1)

	// Get next - should be 0xFFFFFFFF
	ack2 := c.getNextACKNumber()
	require.Equal(t, uint32(0xFFFFFFFF), ack2)

	// Get next - should wrap to 1 (skipping 0)
	ack3 := c.getNextACKNumber()
	require.Equal(t, uint32(1), ack3, "Should wrap to 1, skipping 0")

	// Get next - should be 2
	ack4 := c.getNextACKNumber()
	require.Equal(t, uint32(2), ack4)
}

func TestGetNextACKNumber_SkipsZero(t *testing.T) {
	c := &srtConn{}

	// Set nextACKNumber to max (0xFFFFFFFF)
	c.nextACKNumber.Store(0xFFFFFFFF)

	// Get 0xFFFFFFFF
	ack1 := c.getNextACKNumber()
	require.Equal(t, uint32(0xFFFFFFFF), ack1)

	// Next should be 1, not 0
	ack2 := c.getNextACKNumber()
	require.Equal(t, uint32(1), ack2, "ACK number 0 is reserved for Light ACK")
}

func TestAckEntryBtree_ExpireOlderThan_NoWraparound(t *testing.T) {
	// NOTE: ExpireOlderThan uses simple < comparison, which does NOT handle
	// uint32 wraparound. This is acceptable because:
	// 1. ACK numbers increment at ~100/sec (10ms Full ACK interval)
	// 2. At that rate, wraparound takes 2^32/100 = 42M seconds ≈ 1.3 years
	// 3. By the time we wrap, old entries are cleaned up via ACKACK or timeout
	//
	// This test documents the limitation.

	tree := newAckEntryBtree(4)
	now := time.Now()

	// Simulate entries after wraparound
	tree.Insert(&ackEntry{ackNum: 1, timestamp: now})          // After wrap
	tree.Insert(&ackEntry{ackNum: 2, timestamp: now})          // After wrap
	tree.Insert(&ackEntry{ackNum: 0xFFFFFF00, timestamp: now}) // Before wrap

	// If we expire < 3, it should remove 1 and 2
	// But 0xFFFFFF00 will NOT be removed because 0xFFFFFF00 >= 3
	count, removed := tree.ExpireOlderThan(3)
	require.Equal(t, 2, count)
	require.Len(t, removed, 2)

	// 0xFFFFFF00 is still there (this is the limitation)
	require.NotNil(t, tree.Get(0xFFFFFF00),
		"KNOWN LIMITATION: ExpireOlderThan doesn't handle wraparound")
}

// Benchmark to compare against map
func BenchmarkAckEntryBtree_Insert(b *testing.B) {
	tree := newAckEntryBtree(4)
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry := &ackEntry{ackNum: uint32(i), timestamp: now}
		tree.Insert(entry)
	}
}

func BenchmarkAckEntryBtree_Get(b *testing.B) {
	tree := newAckEntryBtree(4)
	now := time.Now()
	// Insert some entries
	for i := 0; i < 100; i++ {
		tree.Insert(&ackEntry{ackNum: uint32(i), timestamp: now})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree.Get(uint32(i % 100))
	}
}

func BenchmarkAckEntryBtree_DeleteMin(b *testing.B) {
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tree := newAckEntryBtree(4)
		for j := 0; j < 10; j++ {
			tree.Insert(&ackEntry{ackNum: uint32(j), timestamp: now})
		}
		b.StartTimer()
		for tree.Len() > 0 {
			tree.DeleteMin()
		}
	}
}

func BenchmarkAckEntryPool(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry := GetAckEntry()
		entry.ackNum = uint32(i)
		entry.timestamp = time.Now()
		PutAckEntry(entry)
	}
}

func BenchmarkAckEntryMap_Insert(b *testing.B) {
	m := make(map[uint32]time.Time)
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m[uint32(i)] = now
	}
}

func BenchmarkAckEntryMap_Get(b *testing.B) {
	m := make(map[uint32]time.Time)
	now := time.Now()
	// Insert some entries
	for i := 0; i < 100; i++ {
		m[uint32(i)] = now
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m[uint32(i%100)]
	}
}
