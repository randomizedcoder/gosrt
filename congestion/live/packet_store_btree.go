//go:build go1.18

package live

import (
	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/google/btree"
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

func (s *btreePacketStore) Insert(pkt packet.Packet) bool {
	// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
	h := pkt.Header()
	item := &packetItem{
		seqNum: h.PacketSequenceNumber,
		packet: pkt,
	}

	// Check for duplicate
	if s.tree.Has(item) {
		return false
	}

	// Insert (ReplaceOrInsert handles duplicates, but we check Has() first)
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
