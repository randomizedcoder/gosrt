package srt

// ACK/ACKACK Redesign - Phase ACK-4: ackEntry struct and btree
// Reference: documentation/ack_optimization_implementation.md
//
// This file implements a btree-based storage for ACK timestamps, replacing
// the map[uint32]time.Time in connection.go. Benefits:
// - Bounded growth (can efficiently delete old entries)
// - O(log n) lookup instead of O(1), but with better memory behavior
// - DeleteMin for efficient cleanup of old entries

import (
	"sync"
	"time"

	"github.com/google/btree"
)

// ackEntry stores an ACK number and the time it was sent.
// Used to calculate RTT when ACKACK is received.
type ackEntry struct {
	ackNum    uint32    // ACK number (TypeSpecific field from ACK packet)
	timestamp time.Time // When the ACK was sent
}

// ackEntryBtree wraps a btree for storing ackEntry items.
// NOT thread-safe - caller must hold appropriate lock.
type ackEntryBtree struct {
	tree *btree.BTreeG[*ackEntry]
}

// newAckEntryBtree creates a new btree for ACK entries.
// degree controls the branching factor (higher = more memory, fewer levels).
// Recommended: 4-8 for small trees (we typically have ~10 entries).
func newAckEntryBtree(degree int) *ackEntryBtree {
	return &ackEntryBtree{
		tree: btree.NewG(degree, func(a, b *ackEntry) bool {
			return a.ackNum < b.ackNum
		}),
	}
}

// Insert adds an ACK entry to the btree.
// Returns the entry for potential pool return on replacement.
func (t *ackEntryBtree) Insert(entry *ackEntry) *ackEntry {
	old, _ := t.tree.ReplaceOrInsert(entry)
	return old
}

// Get retrieves an ACK entry by ACK number.
// Returns nil if not found.
func (t *ackEntryBtree) Get(ackNum uint32) *ackEntry {
	key := &ackEntry{ackNum: ackNum}
	if item, ok := t.tree.Get(key); ok {
		return item
	}
	return nil
}

// Delete removes an ACK entry by ACK number.
// Returns the deleted entry for pool return.
func (t *ackEntryBtree) Delete(ackNum uint32) *ackEntry {
	key := &ackEntry{ackNum: ackNum}
	if deleted, ok := t.tree.Delete(key); ok {
		return deleted
	}
	return nil
}

// DeleteMin removes and returns the entry with the smallest ACK number.
// Returns nil if the tree is empty.
func (t *ackEntryBtree) DeleteMin() *ackEntry {
	if minEntry, ok := t.tree.DeleteMin(); ok {
		return minEntry
	}
	return nil
}

// Min returns the entry with the smallest ACK number without removing it.
// Returns nil if the tree is empty.
func (t *ackEntryBtree) Min() *ackEntry {
	minEntry, ok := t.tree.Min()
	if !ok {
		return nil
	}
	return minEntry
}

// Len returns the number of entries in the btree.
func (t *ackEntryBtree) Len() int {
	return t.tree.Len()
}

// ExpireOlderThan removes all entries with ackNum < threshold.
// Returns the number of entries removed and a slice of removed entries for pool return.
// Uses DeleteMin for efficient sequential removal.
func (t *ackEntryBtree) ExpireOlderThan(threshold uint32) (int, []*ackEntry) {
	var removed []*ackEntry
	for {
		minEntry, ok := t.tree.Min()
		if !ok || minEntry.ackNum >= threshold {
			break
		}
		if deleted, deletedOk := t.tree.DeleteMin(); deletedOk {
			removed = append(removed, deleted)
		}
	}
	return len(removed), removed
}

// ============================================================================
// sync.Pool for ackEntry (ACK-4b)
// ============================================================================

var globalAckEntryPool = &sync.Pool{
	New: func() interface{} {
		return &ackEntry{}
	},
}

// GetAckEntry retrieves an ackEntry from the pool.
// The entry is reset to zero values.
func GetAckEntry() *ackEntry {
	entry, ok := globalAckEntryPool.Get().(*ackEntry)
	if !ok {
		// Pool should only contain *ackEntry, this is a programming error
		panic("globalAckEntryPool contained non-*ackEntry value")
	}
	entry.ackNum = 0
	entry.timestamp = time.Time{}
	return entry
}

// PutAckEntry returns an ackEntry to the pool.
func PutAckEntry(e *ackEntry) {
	if e != nil {
		globalAckEntryPool.Put(e)
	}
}

// PutAckEntries returns multiple ackEntry objects to the pool.
func PutAckEntries(entries []*ackEntry) {
	for _, e := range entries {
		PutAckEntry(e)
	}
}
