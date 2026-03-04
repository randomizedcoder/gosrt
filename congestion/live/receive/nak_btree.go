//go:build go1.18

package receive

import (
	"sync"

	"github.com/google/btree"
	"github.com/randomizedcoder/gosrt/circular"
)

// NakEntryWithTime stores a missing sequence number with timing information.
// Used in NAK btree to track:
// - When the packet should be delivered (TsbpdTimeUs) - for RTT-aware expiry
// - When we last sent NAK (LastNakedAtUs) - for NAK suppression
// - How many times NAK'd (NakCount) - for metrics
//
// Phase 6: RTO Suppression - prevents redundant NAKs within RTO window.
// Phase 7: NAK Btree Expiry Optimization - RTT-aware early expiry (nak_btree_expiry_optimization.md)
type NakEntryWithTime struct {
	Seq           uint32 // Missing sequence number
	TsbpdTimeUs   uint64 // TSBPD release time for this sequence (microseconds) - for expiry
	LastNakedAtUs uint64 // When we last sent NAK for this seq (microseconds) - for suppression
	NakCount      uint32 // Number of times NAK'd
}

// nakBtree stores missing sequence numbers for NAK generation.
// Stores NakEntryWithTime for suppression tracking.
// Uses a separate lock from the packet btree.
type nakBtree struct {
	tree *btree.BTreeG[NakEntryWithTime]
	mu   sync.RWMutex
}

// newNakBtree creates a new NAK btree.
// Degree of 32 is a good default (same as packet btree).
func newNakBtree(degree int) *nakBtree {
	return &nakBtree{
		tree: btree.NewG(degree, func(a, b NakEntryWithTime) bool {
			return circular.SeqLess(a.Seq, b.Seq)
		}),
	}
}

// Insert adds a missing sequence number.
// Initializes LastNakedAtUs=0 and NakCount=0 for new entries.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use InsertLocking().
func (nb *nakBtree) Insert(seq uint32) {
	entry := NakEntryWithTime{Seq: seq, LastNakedAtUs: 0, NakCount: 0}
	nb.tree.ReplaceOrInsert(entry)
}

// InsertLocking adds a missing sequence number with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) InsertLocking(seq uint32) {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	nb.Insert(seq)
}

// InsertBatch adds multiple missing sequence numbers.
// Returns the count of newly inserted sequences (excludes duplicates).
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use InsertBatchLocking().
func (nb *nakBtree) InsertBatch(seqs []uint32) int {
	if len(seqs) == 0 {
		return 0
	}

	count := 0
	for _, seq := range seqs {
		entry := NakEntryWithTime{Seq: seq, LastNakedAtUs: 0, NakCount: 0}
		// ReplaceOrInsert returns (oldItem, replaced)
		// If replaced is false, this was a new insert
		if _, replaced := nb.tree.ReplaceOrInsert(entry); !replaced {
			count++
		}
	}
	return count
}

// InsertBatchLocking adds multiple missing sequence numbers with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) InsertBatchLocking(seqs []uint32) int {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return nb.InsertBatch(seqs)
}

// --- TSBPD-aware insertion methods (nak_btree_expiry_optimization.md) ---

// InsertWithTsbpd adds a missing sequence number with its estimated TSBPD time.
// Used for RTT-aware expiry: entries expire based on TSBPD time, not sequence.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use InsertWithTsbpdLocking().
func (nb *nakBtree) InsertWithTsbpd(seq uint32, tsbpdTimeUs uint64) {
	entry := NakEntryWithTime{Seq: seq, TsbpdTimeUs: tsbpdTimeUs, LastNakedAtUs: 0, NakCount: 0}
	nb.tree.ReplaceOrInsert(entry)
}

// InsertWithTsbpdLocking adds a missing sequence with TSBPD time, with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) InsertWithTsbpdLocking(seq uint32, tsbpdTimeUs uint64) {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	nb.InsertWithTsbpd(seq, tsbpdTimeUs)
}

// InsertBatchWithTsbpd adds multiple missing sequences with their TSBPD times.
// seqs and tsbpdTimes must have the same length.
// Returns the count of newly inserted sequences (excludes duplicates).
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use InsertBatchWithTsbpdLocking().
func (nb *nakBtree) InsertBatchWithTsbpd(seqs []uint32, tsbpdTimes []uint64) int {
	if len(seqs) == 0 || len(seqs) != len(tsbpdTimes) {
		return 0
	}

	count := 0
	for i, seq := range seqs {
		entry := NakEntryWithTime{Seq: seq, TsbpdTimeUs: tsbpdTimes[i], LastNakedAtUs: 0, NakCount: 0}
		if _, replaced := nb.tree.ReplaceOrInsert(entry); !replaced {
			count++
		}
	}
	return count
}

// InsertBatchWithTsbpdLocking adds multiple sequences with TSBPD times, with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) InsertBatchWithTsbpdLocking(seqs []uint32, tsbpdTimes []uint64) int {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return nb.InsertBatchWithTsbpd(seqs, tsbpdTimes)
}

// Delete removes a sequence number (packet arrived or expired).
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use DeleteLocking().
func (nb *nakBtree) Delete(seq uint32) bool {
	searchEntry := NakEntryWithTime{Seq: seq}
	_, found := nb.tree.Delete(searchEntry)
	return found
}

// DeleteLocking removes a sequence number with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) DeleteLocking(seq uint32) bool {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return nb.Delete(seq)
}

// DeleteBefore removes all sequences before cutoff (expired).
// Returns count of deleted entries.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use DeleteBeforeLocking().
func (nb *nakBtree) DeleteBefore(cutoff uint32) int {
	var toDelete []NakEntryWithTime
	nb.tree.Ascend(func(entry NakEntryWithTime) bool {
		if circular.SeqLess(entry.Seq, cutoff) {
			toDelete = append(toDelete, entry)
			return true
		}
		return false // Stop when we reach cutoff
	})

	for _, entry := range toDelete {
		nb.tree.Delete(entry)
	}
	return len(toDelete)
}

// DeleteBeforeLocking removes all sequences before cutoff with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) DeleteBeforeLocking(cutoff uint32) int {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return nb.DeleteBefore(cutoff)
}

// --- TSBPD-aware deletion methods (nak_btree_expiry_optimization.md) ---

// DeleteBeforeTsbpd removes entries whose TSBPD time is before the threshold.
// This enables RTT-aware early expiry: expire entries that cannot be recovered
// in time (TSBPD - RTO margin has passed).
//
// Note: This method iterates by sequence order (btree ordering), not TSBPD order.
// For most cases where TSBPD increases monotonically with sequence, this works well.
// For adversarial/edge cases (clock skew), some entries may not be expired optimally.
//
// Returns count of deleted entries.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use DeleteBeforeTsbpdLocking().
func (nb *nakBtree) DeleteBeforeTsbpd(expiryThresholdUs uint64) int {
	var toDelete []NakEntryWithTime
	nb.tree.Ascend(func(entry NakEntryWithTime) bool {
		if entry.TsbpdTimeUs < expiryThresholdUs {
			toDelete = append(toDelete, entry)
		}
		// Continue iteration - TSBPD may not be monotonic with sequence
		return true
	})

	for _, entry := range toDelete {
		nb.tree.Delete(entry)
	}
	return len(toDelete)
}

// DeleteBeforeTsbpdLocking removes entries by TSBPD time with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) DeleteBeforeTsbpdLocking(expiryThresholdUs uint64) int {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return nb.DeleteBeforeTsbpd(expiryThresholdUs)
}

// DeleteBeforeTsbpdSlow is the unoptimized implementation that scans all entries.
// This is provided for benchmark comparison against DeleteBeforeTsbpd.
// It collects all entries with TSBPD < threshold, then deletes them.
// Time complexity: O(n) scan + O(k log n) deletes = O(n + k log n)
func (nb *nakBtree) DeleteBeforeTsbpdSlow(expiryThresholdUs uint64) int {
	var toDelete []NakEntryWithTime
	nb.tree.Ascend(func(entry NakEntryWithTime) bool {
		if entry.TsbpdTimeUs < expiryThresholdUs {
			toDelete = append(toDelete, entry)
		}
		return true // Must scan all - TSBPD may not be monotonic
	})

	for _, entry := range toDelete {
		nb.tree.Delete(entry)
	}
	return len(toDelete)
}

// Len returns the number of entries.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use LenLocking().
func (nb *nakBtree) Len() int {
	return nb.tree.Len()
}

// LenLocking returns the number of entries with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) LenLocking() int {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	return nb.Len()
}

// Iterate traverses in ascending order (oldest first = most urgent).
// Returns entries by value - caller cannot modify entries in the btree directly.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use IterateLocking().
// For suppression updates, use IterateAndUpdate.
func (nb *nakBtree) Iterate(fn func(entry NakEntryWithTime) bool) {
	nb.tree.Ascend(fn)
}

// IterateLocking traverses in ascending order with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) IterateLocking(fn func(entry NakEntryWithTime) bool) {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	nb.Iterate(fn)
}

// IterateAndUpdate traverses in ascending order, allowing updates to LastNakedAtUs/NakCount.
// The callback receives a pointer-like access: returns (updatedEntry, shouldUpdate, continueIteration).
// If shouldUpdate is true, the entry is replaced in the btree with the returned updatedEntry.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use IterateAndUpdateLocking().
func (nb *nakBtree) IterateAndUpdate(fn func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool)) {
	var updates []NakEntryWithTime
	nb.tree.Ascend(func(entry NakEntryWithTime) bool {
		updated, shouldUpdate, cont := fn(entry)
		if shouldUpdate {
			updates = append(updates, updated)
		}
		return cont
	})

	// Apply updates after iteration (can't modify during Ascend)
	for _, entry := range updates {
		nb.tree.ReplaceOrInsert(entry)
	}
}

// IterateAndUpdateLocking traverses with update support and lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) IterateAndUpdateLocking(fn func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool)) {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	nb.IterateAndUpdate(fn)
}

// IterateDescending traverses in descending order (newest first).
// The callback should not modify the btree.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use IterateDescendingLocking().
func (nb *nakBtree) IterateDescending(fn func(entry NakEntryWithTime) bool) {
	nb.tree.Descend(fn)
}

// IterateDescendingLocking traverses in descending order with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) IterateDescendingLocking(fn func(entry NakEntryWithTime) bool) {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	nb.IterateDescending(fn)
}

// Min returns the minimum sequence number, or 0 if empty.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use MinLocking().
func (nb *nakBtree) Min() (uint32, bool) {
	if nb.tree.Len() == 0 {
		return 0, false
	}
	minEntry, _ := nb.tree.Min()
	return minEntry.Seq, true
}

// MinLocking returns the minimum sequence number with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) MinLocking() (uint32, bool) {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	return nb.Min()
}

// Max returns the maximum sequence number, or 0 if empty.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use MaxLocking().
func (nb *nakBtree) Max() (uint32, bool) {
	if nb.tree.Len() == 0 {
		return 0, false
	}
	maxEntry, _ := nb.tree.Max()
	return maxEntry.Seq, true
}

// MaxLocking returns the maximum sequence number with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) MaxLocking() (uint32, bool) {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	return nb.Max()
}

// Has returns true if the sequence number is in the btree.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use HasLocking().
func (nb *nakBtree) Has(seq uint32) bool {
	searchEntry := NakEntryWithTime{Seq: seq}
	return nb.tree.Has(searchEntry)
}

// HasLocking returns true if the sequence number is in the btree with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) HasLocking(seq uint32) bool {
	nb.mu.RLock()
	defer nb.mu.RUnlock()
	return nb.Has(seq)
}

// Clear removes all entries.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use ClearLocking().
func (nb *nakBtree) Clear() {
	nb.tree.Clear(false)
}

// ClearLocking removes all entries with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) ClearLocking() {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	nb.Clear()
}
