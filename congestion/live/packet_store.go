package live

import (
	"container/list"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/packet"
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
	seqNum := pkt.Header().PacketSequenceNumber

	// Check for duplicate
	for e := s.list.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		if p.Header().PacketSequenceNumber == seqNum {
			return false // Duplicate
		}
		if p.Header().PacketSequenceNumber.Gt(seqNum) {
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

func (s *listPacketStore) Remove(seqNum circular.Number) packet.Packet {
	for e := s.list.Front(); e != nil; e = e.Next() {
		p := e.Value.(packet.Packet)
		if p.Header().PacketSequenceNumber == seqNum {
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
		if p.Header().PacketSequenceNumber == seqNum {
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
