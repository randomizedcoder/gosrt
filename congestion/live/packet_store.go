package live

import (
	"container/list"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// packetStore defines the interface for storing and retrieving packets in order
// NOT thread-safe - caller must hold appropriate lock from receiver
type packetStore interface {
	// Insert inserts a packet into the store in the correct position
	// Returns true if packet was inserted, false if duplicate
	Insert(pkt packet.Packet) bool

	// Iterate calls fn for each packet in order until fn returns false
	// fn receives (packet) and returns whether to continue
	Iterate(fn func(pkt packet.Packet) bool) bool

	// IterateFrom calls fn for each packet starting from startSeq (inclusive) in order
	// until fn returns false. Uses AscendGreaterOrEqual for O(log n) start in btree.
	// fn receives (packet) and returns whether to continue
	IterateFrom(startSeq circular.Number, fn func(pkt packet.Packet) bool) bool

	// Remove removes a specific packet (by sequence number)
	// Returns the removed packet, or nil if not found
	Remove(seqNum circular.Number) packet.Packet

	// RemoveAll removes all packets matching the predicate, calling deliverFunc for each
	// Stops at first packet that doesn't match
	RemoveAll(predicate func(pkt packet.Packet) bool, deliverFunc func(pkt packet.Packet)) int

	// Has returns true if a packet with the given sequence number exists
	Has(seqNum circular.Number) bool

	// Len returns the number of packets in the store
	Len() int

	// Clear removes all packets from the store
	Clear()

	// Min returns the packet with the smallest sequence number, or nil if empty
	Min() packet.Packet
}

// listPacketStore implements packetStore using container/list.List
// NOT thread-safe - caller must hold appropriate lock from receiver
type listPacketStore struct {
	list *list.List
}

// NewListPacketStore creates a new list-based packet store
func NewListPacketStore() packetStore {
	return &listPacketStore{
		list: list.New(),
	}
}

func (s *listPacketStore) Insert(pkt packet.Packet) bool {
	// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
	h := pkt.Header()
	seqNum := h.PacketSequenceNumber

	// Check for duplicate
	for e := s.list.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		// Note: Still need to call Header() for each list element in comparison loop
		ph := p.Header()
		if ph.PacketSequenceNumber == seqNum {
			return false // Duplicate
		}
		if ph.PacketSequenceNumber.Gt(seqNum) {
			s.list.InsertBefore(pkt, e)
			return true
		}
	}

	// Insert at end
	s.list.PushBack(pkt)
	return true
}

func (s *listPacketStore) Iterate(fn func(pkt packet.Packet) bool) bool {
	for e := s.list.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		if !fn(p) {
			return false // Stop iteration
		}
	}
	return true // Completed
}

func (s *listPacketStore) IterateFrom(startSeq circular.Number, fn func(pkt packet.Packet) bool) bool {
	// For list-based store, we must scan from beginning (O(n))
	// This is a fallback - btree implementation uses AscendGreaterOrEqual for O(log n)
	for e := s.list.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		h := p.Header()
		// Skip packets before startSeq (handles wraparound via circular comparison)
		if h.PacketSequenceNumber.Lt(startSeq) {
			continue
		}
		if !fn(p) {
			return false // Stop iteration
		}
	}
	return true // Completed
}

func (s *listPacketStore) Remove(seqNum circular.Number) packet.Packet {
	for e := s.list.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()
		if h.PacketSequenceNumber == seqNum {
			s.list.Remove(e)
			return p
		}
	}
	return nil
}

func (s *listPacketStore) RemoveAll(predicate func(pkt packet.Packet) bool, deliverFunc func(pkt packet.Packet)) int {
	removed := 0
	var toRemove []*list.Element

	for e := s.list.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		if predicate(p) {
			deliverFunc(p)
			toRemove = append(toRemove, e)
			removed++
		} else {
			break // Stop at first non-matching
		}
	}

	for _, e := range toRemove {
		s.list.Remove(e)
	}

	return removed
}

func (s *listPacketStore) Has(seqNum circular.Number) bool {
	for e := s.list.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()
		if h.PacketSequenceNumber == seqNum {
			return true
		}
	}
	return false
}

func (s *listPacketStore) Len() int {
	return s.list.Len()
}

func (s *listPacketStore) Clear() {
	s.list = s.list.Init()
}

func (s *listPacketStore) Min() packet.Packet {
	if s.list.Len() == 0 {
		return nil
	}
	return s.list.Front().Value.(packet.Packet)
}
