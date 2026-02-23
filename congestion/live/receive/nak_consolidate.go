package receive

import (
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
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

// consolidateNakBtree converts NAK btree singles into ranges with RTO-based suppression.
// Traverses in ascending order (oldest first = most urgent).
// Respects time budget to avoid blocking.
// Uses sync.Pool for intermediate []NAKEntry slice.
//
// Phase 6: RTO Suppression - skips entries where full round-trip hasn't completed:
//   - NAK → Sender → Retransmit → back to us = full RTO
//   - Uses pre-calculated rtoUs from connection's RTT tracker
//
// Must be called with r.lock held (at least RLock).
func (r *receiver) consolidateNakBtree() []circular.Number {
	if r.nakBtree == nil || r.nakLen == nil {
		// This should never happen when called (ISSUE-001)
		if r.metrics != nil && r.nakBtree == nil {
			r.metrics.NakBtreeNilWhenEnabled.Add(1)
		}
		return nil
	}
	if r.nakLen() == 0 {
		return nil
	}

	// ──────────────────────────────────────────────────────────────────
	// PRE-FETCH ALL VALUES ONCE (minimize syscalls/atomic loads in loop)
	// ──────────────────────────────────────────────────────────────────
	nowUs := uint64(time.Now().UnixMicro())
	deadline := time.Now().Add(r.nakConsolidationBudget)

	// RTO threshold for suppression (single atomic load for ALL entries)
	var rtoThreshold uint64
	if r.rtt != nil {
		rtoThreshold = r.rtt.RTOUs() // Direct atomic access
	}

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
	var suppressedCount uint64
	var allowedCount uint64

	if r.nakIterateAndUpdate == nil {
		return nil
	}
	r.nakIterateAndUpdate(func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool) {
		// Check time budget every 100 iterations
		iterCount++
		if iterCount%100 == 0 {
			if time.Now().After(deadline) {
				if r.metrics != nil {
					r.metrics.NakConsolidationTimeout.Add(1)
				}
				return entry, false, false // Stop - time's up
			}
		}

		// ──────────────────────────────────────────────────────────────
		// NAK SUPPRESSION CHECK (RTO-based)
		// Skip entries where full round-trip hasn't had time to complete.
		// Full RTO: NAK → Sender → Retransmit → back to us
		// ──────────────────────────────────────────────────────────────
		if entry.LastNakedAtUs > 0 && rtoThreshold > 0 {
			timeSinceNAK := nowUs - entry.LastNakedAtUs
			if timeSinceNAK < rtoThreshold {
				// Too soon - round-trip hasn't completed
				suppressedCount++
				return entry, false, true // Continue to next entry, don't update
			}
		}

		// ──────────────────────────────────────────────────────────────
		// INCLUDE IN NAK - update tracking
		// ──────────────────────────────────────────────────────────────
		entry.LastNakedAtUs = nowUs
		entry.NakCount++
		allowedCount++

		seq := entry.Seq

		if currentEntry == nil {
			// Start new entry
			currentEntry = &NAKEntry{Start: seq, End: seq}
			return entry, true, true // Update entry, continue
		}

		// Check if contiguous or within mergeGap
		gap := circular.SeqDiff(seq, currentEntry.End) - 1
		if gap >= 0 && uint32(gap) <= r.nakMergeGap {
			// Extend current entry
			currentEntry.End = seq
			if r.metrics != nil {
				r.metrics.NakConsolidationMerged.Add(1)
			}
		} else {
			// Gap too large - emit current, start new
			entries = append(entries, *currentEntry)
			currentEntry = &NAKEntry{Start: seq, End: seq}
		}

		return entry, true, true // Update entry, continue
	})

	// Emit final entry
	if currentEntry != nil {
		entries = append(entries, *currentEntry)
	}

	// Track metrics
	if r.metrics != nil {
		r.metrics.NakConsolidationRuns.Add(1)
		r.metrics.NakConsolidationEntries.Add(uint64(len(entries)))
		r.metrics.NakSuppressedSeqs.Add(suppressedCount)
		r.metrics.NakAllowedSeqs.Add(allowedCount)
	}

	// Convert to NAK list format
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
