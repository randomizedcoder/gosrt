package live

import (
	"sync"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/packet"
)

// NAKEntry represents either a single sequence or a range.
type NAKEntry struct {
	Start uint32
	End   uint32 // Same as Start for singles
}

// IsRange returns true if this entry represents a range (more than one sequence).
func (e NAKEntry) IsRange() bool {
	return e.End > e.Start
}

// Count returns the number of sequences covered by this entry.
func (e NAKEntry) Count() uint32 {
	return e.End - e.Start + 1
}

// Pool for intermediate []NAKEntry slices.
// These grow to "right size" for the workload over time.
// Note: Only pool intermediate results, not return values.
var nakEntryPool = sync.Pool{
	New: func() interface{} {
		s := make([]NAKEntry, 0, 64) // Initial capacity
		return &s
	},
}

// consolidateNakBtree converts NAK btree singles into ranges.
// Traverses in ascending order (oldest first = most urgent).
// Respects time budget to avoid blocking.
// Uses sync.Pool for intermediate []NAKEntry slice.
//
// Must be called with r.lock held (at least RLock).
func (r *receiver) consolidateNakBtree() []circular.Number {
	if r.nakBtree == nil {
		// This should never happen when called (ISSUE-001)
		if r.metrics != nil {
			r.metrics.NakBtreeNilWhenEnabled.Add(1)
		}
		return nil
	}
	if r.nakBtree.Len() == 0 {
		return nil
	}

	deadline := time.Now().Add(r.nakConsolidationBudget)

	// Get pooled slice for intermediate entries
	entriesPtr := nakEntryPool.Get().(*[]NAKEntry)
	entries := *entriesPtr

	// Defer: return entries to pool AFTER entriesToNakList() consumes it
	// Reset to len=0 before returning so next Get() is ready to use
	defer func() {
		*entriesPtr = entries[:0] // Reset length for next use
		nakEntryPool.Put(entriesPtr)
	}()

	var currentEntry *NAKEntry
	iterCount := 0

	r.nakBtree.Iterate(func(seq uint32) bool {
		// Check time budget every 100 iterations
		// (time.Now() per iteration is expensive - ~30-50ns each)
		iterCount++
		if iterCount%100 == 0 {
			if time.Now().After(deadline) {
				// Track timeout for monitoring
				if r.metrics != nil {
					r.metrics.NakConsolidationTimeout.Add(1)
				}
				return false // Stop - time's up
			}
		}

		if currentEntry == nil {
			// Start new entry
			currentEntry = &NAKEntry{Start: seq, End: seq}
			return true
		}

		// Check if contiguous or within mergeGap
		// gap = actual distance between sequences minus 1 (for adjacent sequences, gap=0)
		gap := circular.SeqDiff(seq, currentEntry.End) - 1
		if gap >= 0 && uint32(gap) <= r.nakMergeGap {
			// Extend current entry (contiguous or within merge gap)
			currentEntry.End = seq
			if r.metrics != nil {
				r.metrics.NakConsolidationMerged.Add(1)
			}
		} else {
			// Gap too large - emit current, start new
			entries = append(entries, *currentEntry)
			currentEntry = &NAKEntry{Start: seq, End: seq}
		}

		return true
	})

	// Emit final entry
	if currentEntry != nil {
		entries = append(entries, *currentEntry)
	}

	// Track metrics
	if r.metrics != nil {
		r.metrics.NakConsolidationRuns.Add(1)
		r.metrics.NakConsolidationEntries.Add(uint64(len(entries)))
	}

	// Convert to NAK list format
	// entries is consumed here, then returned to pool by defer
	return r.entriesToNakList(entries)
}

// entriesToNakList converts NAKEntry slice to circular.Number pairs.
// Returns a fresh slice (not pooled) because caller needs ownership.
// Format: [start1, end1, start2, end2, ...] where start==end for singles.
func (r *receiver) entriesToNakList(entries []NAKEntry) []circular.Number {
	if len(entries) == 0 {
		return nil
	}

	list := make([]circular.Number, 0, len(entries)*2)

	for _, entry := range entries {
		list = append(list,
			circular.New(entry.Start, packet.MAX_SEQUENCENUMBER),
			circular.New(entry.End, packet.MAX_SEQUENCENUMBER),
		)
	}

	return list
}
