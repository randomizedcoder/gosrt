//go:build go1.18

package receive

import (
	"github.com/google/btree"
	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// packetItem wraps a packet for storage in btree
type packetItem struct {
	seqNum circular.Number
	packet packet.Packet
}

// btreePacketStore implements packetStore using github.com/google/btree
// NOT thread-safe - caller must hold appropriate lock from receiver
type btreePacketStore struct {
	tree *btree.BTreeG[*packetItem]
}

// NewBTreePacketStore creates a new btree-based packet store
func NewBTreePacketStore(degree int) packetStore {
	return &btreePacketStore{
		tree: btree.NewG(degree, func(a, b *packetItem) bool {
			// Use optimized SeqLess function on raw uint32 values
			// ~10% faster than circular.Number.Lt() method
			return circular.SeqLess(a.seqNum.Val(), b.seqNum.Val())
		}),
	}
}

// Insert uses single btree traversal for the common case (unique packets).
// Instead of Has() + ReplaceOrInsert() (2 traversals), we do:
// 1. ReplaceOrInsert() - single traversal
// 2. If duplicate detected (old returned), restore old and return new packet for release
//
// Performance (benchmarked):
//   - Unique packet (common): 636 ns vs 790 ns = 20% faster
//   - Duplicate packet (rare): 582 ns vs 472 ns = 23% slower (acceptable tradeoff)
//   - Mixed 1% duplicates: 633 ns vs 796 ns = 20% faster
//   - Memory: identical (3 allocs/op in both cases)
//
// Returns: (inserted bool, duplicatePacket packet.Packet)
//   - inserted=true, duplicatePacket=nil: new packet inserted successfully
//   - inserted=false, duplicatePacket=pkt: duplicate detected, caller should release pkt
func (s *btreePacketStore) Insert(pkt packet.Packet) (bool, packet.Packet) {
	h := pkt.Header()
	item := &packetItem{
		seqNum: h.PacketSequenceNumber,
		packet: pkt,
	}

	// Single traversal - try to insert
	// ReplaceOrInsert returns (oldItem, replaced bool)
	old, replaced := s.tree.ReplaceOrInsert(item)

	if replaced {
		// Duplicate! New packet replaced old one in the tree.
		// Restore old packet to tree, return new packet for caller to release.
		s.tree.ReplaceOrInsert(old)
		return false, pkt
	}

	return true, nil
}

// InsertDuplicateCheck is the legacy method using Has() + ReplaceOrInsert() (2 traversals).
// Kept for benchmarking comparison. Use Insert() for production - it's 20% faster.
func (s *btreePacketStore) InsertDuplicateCheck(pkt packet.Packet) bool {
	// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
	h := pkt.Header()
	item := &packetItem{
		seqNum: h.PacketSequenceNumber,
		packet: pkt,
	}

	// Check for duplicate - first traversal
	if s.tree.Has(item) {
		return false
	}

	// Insert - second traversal
	s.tree.ReplaceOrInsert(item)
	return true
}

func (s *btreePacketStore) Iterate(fn func(pkt packet.Packet) bool) bool {
	stopped := false
	s.tree.Ascend(func(item *packetItem) bool {
		if !fn(item.packet) {
			stopped = true
			return false // Stop iteration
		}
		return true // Continue
	})
	return !stopped // Return true if completed
}

// IterateFrom iterates packets starting from startSeq (inclusive) using AscendGreaterOrEqual.
// This provides O(log n) seek to start position vs O(n) for Iterate with manual skip.
func (s *btreePacketStore) IterateFrom(startSeq circular.Number, fn func(pkt packet.Packet) bool) bool {
	pivot := &packetItem{seqNum: startSeq}
	stopped := false
	s.tree.AscendGreaterOrEqual(pivot, func(item *packetItem) bool {
		if !fn(item.packet) {
			stopped = true
			return false // Stop iteration
		}
		return true // Continue
	})
	return !stopped // Return true if completed
}

func (s *btreePacketStore) Remove(seqNum circular.Number) packet.Packet {
	item := &packetItem{
		seqNum: seqNum,
		packet: nil, // Not needed for lookup
	}

	removed, found := s.tree.Delete(item)
	if !found {
		return nil
	}
	return removed.packet
}

// RemoveAll removes packets starting from Min() that match predicate.
// Uses DeleteMin() for O(log n) per delete (no lookup needed).
// This is the optimized implementation - see RemoveAllSlow for the original.
//
// Performance: For n packets to remove from btree of size N:
//   - RemoveAll (optimized):  O(n * log N) - DeleteMin is O(log N)
//   - RemoveAllSlow:          O(n * log N) for collection + O(n * log N) for deletes
//     BUT requires allocation of toRemove slice
//
// The optimization eliminates the temporary slice allocation and second pass.
func (s *btreePacketStore) RemoveAll(predicate func(pkt packet.Packet) bool, deliverFunc func(pkt packet.Packet)) int {
	removed := 0

	for {
		// Get the minimum element
		minItem, found := s.tree.Min()
		if !found {
			break // Tree is empty
		}

		// Check if it matches predicate
		if !predicate(minItem.packet) {
			break // Stop at first non-matching (sorted order)
		}

		// Deliver the packet
		deliverFunc(minItem.packet)

		// Delete the minimum (O(log n), no lookup needed)
		s.tree.DeleteMin()
		removed++
	}

	return removed
}

// RemoveAllSlow is the original implementation for benchmarking comparison.
// It collects items to remove in a slice, then deletes them in a second pass.
// This requires allocation of the toRemove slice and two traversals.
func (s *btreePacketStore) RemoveAllSlow(predicate func(pkt packet.Packet) bool, deliverFunc func(pkt packet.Packet)) int {
	removed := 0
	var toRemove []*packetItem

	s.tree.Ascend(func(item *packetItem) bool {
		if predicate(item.packet) {
			deliverFunc(item.packet)
			toRemove = append(toRemove, item)
			removed++
			return true // Continue
		}
		return false // Stop at first non-matching
	})

	for _, item := range toRemove {
		s.tree.Delete(item)
	}

	return removed
}

func (s *btreePacketStore) Has(seqNum circular.Number) bool {
	item := &packetItem{
		seqNum: seqNum,
		packet: nil, // Not needed for lookup
	}
	return s.tree.Has(item)
}

func (s *btreePacketStore) Len() int {
	return s.tree.Len()
}

func (s *btreePacketStore) Clear() {
	s.tree.Clear(false) // Don't add nodes to freelist (simpler)
}

func (s *btreePacketStore) Min() packet.Packet {
	item, found := s.tree.Min()
	if !found {
		return nil
	}
	return item.packet
}
