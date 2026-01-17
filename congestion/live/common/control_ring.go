//go:build go1.18

package common

import (
	"fmt"

	ring "github.com/randomizedcoder/go-lock-free-ring"
)

// ═══════════════════════════════════════════════════════════════════════════════
// ControlRing[T] - Generic lock-free ring for control packets
//
// This generic ring is used by both sender and receiver for routing control
// packets to their respective EventLoops for lock-free processing.
//
// Thread-safety:
//   - Push(): Safe to call from multiple goroutines (io_uring handlers)
//   - TryPop(): Single consumer only (EventLoop)
//
// Reference: completely_lockfree_receiver.md Section 5.1.1
// Reference: lockless_sender_design.md Section 7.4
// ═══════════════════════════════════════════════════════════════════════════════

// ControlRing is a generic lock-free ring for control packets.
// T is the control packet type (e.g., send.ControlPacket or receive.RecvControlPacket).
type ControlRing[T any] struct {
	ring   *ring.ShardedRing
	shards int
}

// NewControlRing creates a generic control ring with configurable size and shards.
//
// Parameters:
//   - size: per-shard capacity (default: 128)
//   - shards: number of shards (default: 1)
//
// The total capacity is size * shards.
func NewControlRing[T any](size, shards int) (*ControlRing[T], error) {
	if shards < 1 {
		shards = 1 // Default: 1 shard
	}
	if size < 1 {
		size = 128 // Default size
	}

	totalCapacity := uint64(size * shards)
	r, err := ring.NewShardedRing(totalCapacity, uint64(shards))
	if err != nil {
		return nil, fmt.Errorf("failed to create control ring: %w", err)
	}
	return &ControlRing[T]{ring: r, shards: shards}, nil
}

// Push writes a control packet to the ring using the given shard ID.
// Thread-safe: can be called from multiple goroutines (io_uring handlers).
// Returns true if successful, false if ring is full.
//
// The shardID is typically the control packet type (e.g., 0 for ACK, 1 for NAK)
// to distribute writes across shards. With single-shard configuration (shards=1),
// shardID is ignored.
func (r *ControlRing[T]) Push(shardID uint64, packet T) bool {
	return r.ring.Write(shardID, packet)
}

// TryPop attempts to pop a control packet from the ring.
// NOT thread-safe: must be called from single consumer (EventLoop).
// Returns the packet and true if successful, zero value and false if ring is empty.
func (r *ControlRing[T]) TryPop() (T, bool) {
	item, ok := r.ring.TryRead()
	if !ok {
		var zero T
		return zero, false
	}
	cp, ok := item.(T)
	if !ok {
		// Type assertion failed - should never happen if Push() is used correctly
		var zero T
		return zero, false
	}
	return cp, true
}

// Len returns an approximate count of items in the ring.
// This is approximate because the ring is lock-free and items may be
// added/removed concurrently.
func (r *ControlRing[T]) Len() int {
	return int(r.ring.Len())
}

// Shards returns the number of shards configured for this ring.
func (r *ControlRing[T]) Shards() int {
	return r.shards
}

// Cap returns the total capacity of the ring (size * shards).
func (r *ControlRing[T]) Cap() int {
	return int(r.ring.Cap())
}
