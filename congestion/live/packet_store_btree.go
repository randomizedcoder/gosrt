//go:build go1.18

package live

import (
	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/packet"
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
		tree: btree.NewG[*packetItem](degree, func(a, b *packetItem) bool {
			return a.seqNum.Lt(b.seqNum)
		}),
	}
}

func (s *btreePacketStore) Insert(pkt packet.Packet) bool {
	item := &packetItem{
		seqNum: pkt.Header().PacketSequenceNumber,
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

func (s *btreePacketStore) RemoveAll(predicate func(pkt packet.Packet) bool, deliverFunc func(pkt packet.Packet)) int {
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

