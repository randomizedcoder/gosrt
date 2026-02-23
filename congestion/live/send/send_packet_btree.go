//go:build go1.18

// Package send implements the sender-side congestion control for SRT live mode.
package send

import (
	"github.com/google/btree"
	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════════
// SendPacketBtree - Generic btree for sender packet storage
//
// IMPLEMENTATION FOLLOWS: congestion/live/receive/packet_store_btree.go
// - Uses btree.BTreeG[*sendPacketItem] (generic) - NO interface btree!
// - Typed comparator function - NO reflection!
// - Single-traversal ReplaceOrInsert pattern for duplicates
//
// Reference: lockless_sender_implementation_plan.md Step 1.1
// ═══════════════════════════════════════════════════════════════════════════════

// sendPacketItem wraps a packet for btree storage.
// Stores seqNum separately for fast comparison (avoids Header() call during sort).
// Reference: congestion/live/receive/packet_store_btree.go:11-15
type sendPacketItem struct {
	seqNum circular.Number
	packet packet.Packet
}

// SendPacketBtree provides O(log n) packet storage using generic btree.
// NOT thread-safe - caller must hold appropriate lock (or use in EventLoop).
// Reference: congestion/live/receive/packet_store_btree.go:17-21
type SendPacketBtree struct {
	tree *btree.BTreeG[*sendPacketItem] // Generic btree - NO interface btree!

	// Reusable pivot for lookups - avoids allocation per lookup
	// NOT thread-safe - only use from single goroutine (EventLoop or with lock)
	lookupPivot sendPacketItem
}

// NewSendPacketBtree creates a new btree with the specified degree.
// Uses typed comparator function for maximum performance.
// Reference: congestion/live/receive/packet_store_btree.go:24-32
func NewSendPacketBtree(degree int) *SendPacketBtree {
	if degree < 2 {
		degree = 32 // Default (same as receiver)
	}
	return &SendPacketBtree{
		tree: btree.NewG(degree, func(a, b *sendPacketItem) bool {
			// Direct typed comparison - no interfaces, no reflection!
			// Uses optimized SeqLess function on raw uint32 values
			// ~10% faster than circular.Number.Lt() method
			// Reference: circular/seq_math_31bit_wraparound_test.md
			return circular.SeqLess(a.seqNum.Val(), b.seqNum.Val())
		}),
	}
}

// Insert adds a packet using single-traversal ReplaceOrInsert pattern.
// Returns (inserted bool, duplicatePacket) following receiver pattern.
//
// Key insight: ReplaceOrInsert atomically swaps, so on duplicate we keep the new
// packet and return the old one for release - no second traversal needed.
//
// Performance (benchmarked vs Has() + ReplaceOrInsert()):
//   - Unique packet (common): 636 ns vs 790 ns = 20% faster
//   - Duplicate packet (rare): single traversal (~300 ns)
//   - Mixed 1% duplicates: 633 ns vs 796 ns = 20% faster
//   - Memory: identical (3 allocs/op in both cases)
//
// Reference: congestion/live/receive/packet_store_btree.go:54-72
// NOTE: Does NOT update SendBtreeLen metric (done once per EventLoop iteration)
func (bt *SendPacketBtree) Insert(pkt packet.Packet) (bool, packet.Packet) {
	h := pkt.Header()
	item := &sendPacketItem{
		seqNum: h.PacketSequenceNumber,
		packet: pkt,
	}

	// Single traversal - ReplaceOrInsert returns (oldItem, replaced bool)
	// If duplicate exists, new packet replaces old - we keep the new one (same seq#/data)
	old, replaced := bt.tree.ReplaceOrInsert(item)

	if replaced {
		// Duplicate! New packet is now in tree, old packet was kicked out.
		// Return old packet for caller to release (saves 2nd traversal).
		return false, old.packet
	}

	return true, nil
}

// Get retrieves a packet by sequence number. O(log n).
// Uses reusable pivot to avoid allocation.
func (bt *SendPacketBtree) Get(seq uint32) packet.Packet {
	bt.lookupPivot.seqNum = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	item, found := bt.tree.Get(&bt.lookupPivot)
	if !found {
		return nil
	}
	return item.packet
}

// Delete removes a packet by sequence number. O(log n).
// Uses reusable pivot to avoid allocation.
// Reference: congestion/live/receive/packet_store_btree.go:121-132 (Remove)
func (bt *SendPacketBtree) Delete(seq uint32) packet.Packet {
	// Safety: check if tree is empty first to avoid nil root panic
	// This can happen if Delete is called on an empty tree or during shutdown
	if bt.tree == nil || bt.tree.Len() == 0 {
		return nil
	}
	bt.lookupPivot.seqNum = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	removed, found := bt.tree.Delete(&bt.lookupPivot)
	if !found {
		return nil
	}
	return removed.packet
}

// DeleteMin removes and returns the packet with the lowest sequence number.
// O(log n) - no lookup needed. Used by ACK processing and shutdown cleanup.
func (bt *SendPacketBtree) DeleteMin() packet.Packet {
	// Safety: check if tree is empty first to avoid nil root panic
	if bt.tree.Len() == 0 {
		return nil
	}
	item, found := bt.tree.DeleteMin()
	if !found {
		return nil
	}
	return item.packet
}

// Min returns the packet with the lowest sequence number without removing.
// Reference: congestion/live/receive/packet_store_btree.go:210-216
func (bt *SendPacketBtree) Min() packet.Packet {
	// Safety: check if tree is empty first to avoid nil root panic
	if bt.tree.Len() == 0 {
		return nil
	}
	item, found := bt.tree.Min()
	if !found {
		return nil
	}
	return item.packet
}

// Max returns the packet with the highest sequence number without removing.
// Used by Stats() to calculate buffer time range.
func (bt *SendPacketBtree) Max() packet.Packet {
	item, found := bt.tree.Max()
	if !found {
		return nil
	}
	return item.packet
}

// Has checks if a packet with the given sequence number exists.
// Uses reusable pivot to avoid allocation.
// Reference: congestion/live/receive/packet_store_btree.go:194-200
func (bt *SendPacketBtree) Has(seq uint32) bool {
	bt.lookupPivot.seqNum = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	return bt.tree.Has(&bt.lookupPivot)
}

// DeleteBefore removes all packets with sequence numbers < seq.
// Returns slice of deleted packets. Use DeleteBeforeFunc for zero-allocation.
//
// Performance: O(k * log N) where k = removed packets, N = tree size
// Reference: congestion/live/receive/packet_store_btree.go:144-168 (RemoveAll)
//
// NOTE: Does NOT update SendBtreeLen metric (done once per EventLoop iteration)
func (bt *SendPacketBtree) DeleteBefore(seq uint32) (removed int, packets []packet.Packet) {
	packets = make([]packet.Packet, 0, 16) // Pre-allocate for common case

	for {
		// Get the minimum element
		minItem, found := bt.tree.Min()
		if !found {
			break // Tree is empty
		}

		// Check if it's before the threshold using 31-bit wraparound comparison
		if !circular.SeqLess(minItem.seqNum.Val(), seq) {
			break // Stop at first non-matching (sorted order)
		}

		// Delete the minimum (O(log n), no lookup needed)
		bt.tree.DeleteMin()
		packets = append(packets, minItem.packet)
		removed++
	}

	return removed, packets
}

// DeleteBeforeFunc removes all packets with sequence < seq, calling fn for each.
// ZERO ALLOCATION - preferred for ACK processing hot path.
//
// The callback fn receives each deleted packet for inline processing (e.g., decommission).
// Return value from fn is ignored - all matching packets are always deleted.
//
// Performance: O(k * log N) where k = removed packets, N = tree size
// Allocations: 0 B/op, 0 allocs/op (vs DeleteBefore: ~2500 B/op, 31 allocs/op for 10 packets)
//
// Example usage in ackBtree:
//
//	bt.DeleteBeforeFunc(ackSeq, func(p packet.Packet) {
//	    m.CongestionSendPktBuf.Add(^uint64(0))
//	    m.CongestionSendByteBuf.Add(^uint64(uint64(p.Len()) - 1))
//	    p.Decommission()
//	})
func (bt *SendPacketBtree) DeleteBeforeFunc(seq uint32, fn func(p packet.Packet)) int {
	removed := 0

	for {
		// Get the minimum element
		minItem, found := bt.tree.Min()
		if !found {
			break // Tree is empty
		}

		// Check if it's before the threshold using 31-bit wraparound comparison
		if !circular.SeqLess(minItem.seqNum.Val(), seq) {
			break // Stop at first non-matching (sorted order)
		}

		// Delete the minimum (O(log n), no lookup needed)
		bt.tree.DeleteMin()
		removed++

		// Process inline - no slice allocation needed
		if fn != nil {
			fn(minItem.packet)
		}
	}

	return removed
}

// IterateFrom iterates packets starting from seq (inclusive).
// Uses AscendGreaterOrEqual for O(log n) seek to start position.
// Uses reusable pivot to avoid allocation.
// Reference: congestion/live/receive/packet_store_btree.go:106-119
func (bt *SendPacketBtree) IterateFrom(seq uint32, fn func(p packet.Packet) bool) bool {
	bt.lookupPivot.seqNum = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	stopped := false
	bt.tree.AscendGreaterOrEqual(&bt.lookupPivot, func(item *sendPacketItem) bool {
		if !fn(item.packet) {
			stopped = true
			return false // Stop iteration
		}
		return true // Continue
	})
	return !stopped // Return true if completed
}

// Iterate iterates all packets in sequence order.
// Reference: congestion/live/receive/packet_store_btree.go:94-104
func (bt *SendPacketBtree) Iterate(fn func(p packet.Packet) bool) bool {
	stopped := false
	bt.tree.Ascend(func(item *sendPacketItem) bool {
		if !fn(item.packet) {
			stopped = true
			return false
		}
		return true
	})
	return !stopped
}

// Len returns the number of packets in the btree.
// Reference: congestion/live/receive/packet_store_btree.go:202-204
func (bt *SendPacketBtree) Len() int {
	return bt.tree.Len()
}

// Clear removes all packets from the btree.
// Reference: congestion/live/receive/packet_store_btree.go:206-208
func (bt *SendPacketBtree) Clear() {
	bt.tree.Clear(false) // Don't add nodes to freelist (simpler)
}
