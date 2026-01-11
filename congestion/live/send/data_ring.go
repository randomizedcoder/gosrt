//go:build go1.18

// Package send implements the sender-side congestion control for SRT live mode.
package send

import (
	"fmt"

	ring "github.com/randomizedcoder/go-lock-free-ring"
	"github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════════
// SendPacketRing - Lock-free ring buffer for sender data packets
//
// IMPLEMENTATION FOLLOWS: congestion/live/receive/receiver.go:228
// - Uses github.com/randomizedcoder/go-lock-free-ring ShardedRing
// - Push() writes to ring (lock-free from multiple goroutines)
// - EventLoop/Tick drains to btree (single consumer)
//
// Shard Configuration:
// - Default: 1 shard (preserves strict packet ordering)
// - For high write throughput: increase shards (btree sorts packets anyway)
//
// Reference: lockless_sender_implementation_plan.md Step 2.1
// ═══════════════════════════════════════════════════════════════════════════════

// SendPacketRing wraps the lock-free ring for sender data packets.
// Push() writes to ring (lock-free), EventLoop/Tick drains to btree.
type SendPacketRing struct {
	ring   *ring.ShardedRing
	shards int
}

// NewSendPacketRing creates a send ring with configurable size and shards.
//
// Parameters:
//   - size: per-shard capacity (will be rounded to power of 2 by ring library)
//   - shards: number of shards (default 1 for strict ordering)
//
// Shard behavior:
//   - 1 shard: strict FIFO ordering, suitable for single producer
//   - N shards: higher throughput with multiple producers, btree sorts by seq#
//
// Reference: congestion/live/receive/receiver.go:228
func NewSendPacketRing(size, shards int) (*SendPacketRing, error) {
	if shards < 1 {
		shards = 1 // Default: single shard for ordering
	}
	if size < 1 {
		size = 1024 // Default size
	}

	totalCapacity := uint64(size * shards)

	r, err := ring.NewShardedRing(totalCapacity, uint64(shards))
	if err != nil {
		return nil, fmt.Errorf("failed to create send ring: %w", err)
	}

	return &SendPacketRing{
		ring:   r,
		shards: shards,
	}, nil
}

// TryPush attempts to push a packet to the ring without blocking.
// Returns true if successful, false if ring is full.
//
// Thread-safe: can be called from multiple goroutines concurrently.
// Uses packet sequence number for shard selection (distributes load).
func (r *SendPacketRing) TryPush(p packet.Packet) bool {
	// Use packet sequence number for shard selection
	producerID := uint64(p.Header().PacketSequenceNumber.Val())
	return r.ring.Write(producerID, p)
}

// Push pushes a packet to the ring.
// This is equivalent to TryPush - for sender, we don't block on ring-full.
// Caller should handle ring-full by applying backpressure to application.
//
// Thread-safe: can be called from multiple goroutines concurrently.
// Uses packet sequence number for shard selection (distributes load).
func (r *SendPacketRing) Push(p packet.Packet) bool {
	// Use packet sequence number for shard selection
	producerID := uint64(p.Header().PacketSequenceNumber.Val())
	return r.ring.Write(producerID, p)
}

// TryPop attempts to pop a packet from the ring without blocking.
// Returns (packet, true) if successful, (nil, false) if ring is empty.
//
// NOT thread-safe for multiple consumers - designed for single EventLoop consumer.
func (r *SendPacketRing) TryPop() (packet.Packet, bool) {
	item, ok := r.ring.TryRead()
	if !ok {
		return nil, false
	}
	p, ok := item.(packet.Packet)
	if !ok {
		return nil, false
	}
	return p, true
}

// DrainBatch drains up to max packets from the ring.
// Returns slice of drained packets (may be empty if ring is empty).
//
// NOT thread-safe for multiple consumers - designed for single EventLoop consumer.
func (r *SendPacketRing) DrainBatch(max int) []packet.Packet {
	if max <= 0 {
		return nil
	}

	result := make([]packet.Packet, 0, max)
	for i := 0; i < max; i++ {
		p, ok := r.TryPop()
		if !ok {
			break
		}
		result = append(result, p)
	}
	return result
}

// DrainAll drains all available packets from the ring.
// Uses a reasonable batch size to avoid excessive allocation.
//
// NOT thread-safe for multiple consumers - designed for single EventLoop consumer.
func (r *SendPacketRing) DrainAll() []packet.Packet {
	// Start with reasonable capacity, grow as needed
	result := make([]packet.Packet, 0, 64)
	for {
		p, ok := r.TryPop()
		if !ok {
			break
		}
		result = append(result, p)
	}
	return result
}

// Len returns an approximate count of items in the ring.
// Note: This is approximate due to concurrent access.
func (r *SendPacketRing) Len() int {
	return int(r.ring.Len())
}

// Shards returns the number of shards configured for this ring.
func (r *SendPacketRing) Shards() int {
	return r.shards
}

