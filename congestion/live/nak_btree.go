//go:build go1.18

package live

import (
	"sync"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/google/btree"
)

// nakBtree stores missing sequence numbers for NAK generation.
// Stores singles only (not ranges) for simplicity of delete operations.
// Uses a separate lock from the packet btree.
type nakBtree struct {
	tree *btree.BTreeG[uint32]
	mu   sync.RWMutex
}

// newNakBtree creates a new NAK btree.
// Degree of 32 is a good default (same as packet btree).
func newNakBtree(degree int) *nakBtree {
	return &nakBtree{
		tree: btree.NewG(degree, func(a, b uint32) bool {
			return circular.SeqLess(a, b)
		}),
	}
}

// Insert adds a missing sequence number.
func (nb *nakBtree) Insert(seq uint32) {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	nb.tree.ReplaceOrInsert(seq)
}

// InsertBatch adds multiple missing sequence numbers in a single lock acquisition.
// Returns the count of newly inserted sequences (excludes duplicates).
// This is more efficient than calling Insert() multiple times when adding
// multiple gaps discovered during a periodic NAK scan.
func (nb *nakBtree) InsertBatch(seqs []uint32) int {
	if len(seqs) == 0 {
		return 0
	}
	nb.mu.Lock()
	defer nb.mu.Unlock()

	count := 0
	for _, seq := range seqs {
		// ReplaceOrInsert returns (oldItem, replaced)
		// If replaced is false, this was a new insert
		if _, replaced := nb.tree.ReplaceOrInsert(seq); !replaced {
			count++
		}
	}
	return count
}

// Delete removes a sequence number (packet arrived or expired).
func (nb *nakBtree) Delete(seq uint32) bool {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	_, found := nb.tree.Delete(seq)
	return found
}

// DeleteBefore removes all sequences before cutoff (expired).
// Returns count of deleted entries.
func (nb *nakBtree) DeleteBefore(cutoff uint32) int {
	nb.mu.Lock()
	defer nb.mu.Unlock()

	var toDelete []uint32
	nb.tree.Ascend(func(seq uint32) bool {
		if circular.SeqLess(seq, cutoff) {
			toDelete = append(toDelete, seq)
			return true
		}
		return false // Stop when we reach cutoff
	})

	for _, seq := range toDelete {
		nb.tree.Delete(seq)
	}
	return len(toDelete)
}

// Len returns the number of entries.
func (nb *nakBtree) Len() int {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	return nb.tree.Len()
}

// Iterate traverses in ascending order (oldest first = most urgent).
// The callback should not modify the btree.
func (nb *nakBtree) Iterate(fn func(seq uint32) bool) {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	nb.tree.Ascend(fn)
}

// IterateDescending traverses in descending order (newest first).
// The callback should not modify the btree.
func (nb *nakBtree) IterateDescending(fn func(seq uint32) bool) {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	nb.tree.Descend(fn)
}

// Min returns the minimum sequence number, or 0 if empty.
func (nb *nakBtree) Min() (uint32, bool) {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	if nb.tree.Len() == 0 {
		return 0, false
	}
	min, _ := nb.tree.Min()
	return min, true
}

// Max returns the maximum sequence number, or 0 if empty.
func (nb *nakBtree) Max() (uint32, bool) {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	if nb.tree.Len() == 0 {
		return 0, false
	}
	max, _ := nb.tree.Max()
	return max, true
}

// Has returns true if the sequence number is in the btree.
func (nb *nakBtree) Has(seq uint32) bool {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	return nb.tree.Has(seq)
}

// Clear removes all entries.
func (nb *nakBtree) Clear() {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	nb.tree.Clear(false)
}
