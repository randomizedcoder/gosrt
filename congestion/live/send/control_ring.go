//go:build go1.18

// Package send implements the sender-side congestion control for SRT live mode.
package send

import (
	"fmt"

	ring "github.com/randomizedcoder/go-lock-free-ring"
	"github.com/randomizedcoder/gosrt/circular"
)

// ═══════════════════════════════════════════════════════════════════════════════
// SendControlRing - Lock-free ring buffer for sender control packets (ACK/NAK)
//
// CRITICAL: This ring routes ACK/NAK control packets to the EventLoop so that
// the EventLoop is the ONLY goroutine accessing the SendPacketBtree.
//
// Without this, ACK/NAK handlers (called from io_uring completion handlers)
// would access the btree concurrently with the EventLoop, breaking the
// single-threaded access model.
//
// Reference: lockless_sender_implementation_plan.md Phase 3
// Reference: lockless_sender_design.md Section 7.4
// ═══════════════════════════════════════════════════════════════════════════════

// ControlPacketType identifies the type of control packet
type ControlPacketType uint8

const (
	ControlTypeACK ControlPacketType = iota
	ControlTypeNAK
)

// ControlPacket wraps an ACK or NAK for ring transport.
// This is a value type (not pointer) to avoid allocations in the hot path.
type ControlPacket struct {
	Type        ControlPacketType
	ACKSequence uint32   // For ACK: the acknowledged sequence number
	NAKCount    int      // For NAK: number of sequences in NAKSequences
	NAKSequences [32]uint32 // For NAK: up to 32 sequence numbers (inline to avoid allocation)
	// Note: For NAKs with >32 sequences, multiple ControlPackets are pushed
}

// MaxNAKSequencesPerPacket is the maximum NAK sequences per ControlPacket
const MaxNAKSequencesPerPacket = 32

// SendControlRing wraps the lock-free ring for control packets.
// Push() is called from io_uring completion handlers (multiple goroutines).
// TryPop() is called from EventLoop (single consumer).
type SendControlRing struct {
	ring   *ring.ShardedRing
	shards int
}

// NewSendControlRing creates a control ring with configurable size and shards.
//
// Parameters:
//   - size: per-shard capacity (will be rounded to power of 2 by ring library)
//   - shards: number of shards (default 2 for ACK/NAK separation)
//
// Reference: congestion/live/receive/receiver.go:228 (similar pattern)
func NewSendControlRing(size, shards int) (*SendControlRing, error) {
	if shards < 1 {
		shards = 2 // Default: 2 shards (one for ACK, one for NAK)
	}
	if size < 1 {
		size = 256 // Default size
	}

	totalCapacity := uint64(size * shards)

	r, err := ring.NewShardedRing(totalCapacity, uint64(shards))
	if err != nil {
		return nil, fmt.Errorf("failed to create control ring: %w", err)
	}

	return &SendControlRing{
		ring:   r,
		shards: shards,
	}, nil
}

// PushACK pushes an ACK to the control ring.
// Thread-safe: can be called from multiple goroutines (io_uring handlers).
// Returns true if successful, false if ring is full.
func (r *SendControlRing) PushACK(seq circular.Number) bool {
	cp := ControlPacket{
		Type:        ControlTypeACK,
		ACKSequence: seq.Val(),
	}
	// Use ACK type as producer ID for shard selection
	return r.ring.Write(uint64(ControlTypeACK), cp)
}

// PushNAK pushes NAK sequence numbers to the control ring.
// Thread-safe: can be called from multiple goroutines (io_uring handlers).
// Returns true if all sequences were pushed, false if any failed.
//
// For large NAK lists (>32 sequences), multiple ControlPackets are pushed.
func (r *SendControlRing) PushNAK(seqs []circular.Number) bool {
	if len(seqs) == 0 {
		return true
	}

	// Process in chunks of MaxNAKSequencesPerPacket
	for i := 0; i < len(seqs); i += MaxNAKSequencesPerPacket {
		end := i + MaxNAKSequencesPerPacket
		if end > len(seqs) {
			end = len(seqs)
		}
		chunk := seqs[i:end]

		cp := ControlPacket{
			Type:     ControlTypeNAK,
			NAKCount: len(chunk),
		}
		for j, seq := range chunk {
			cp.NAKSequences[j] = seq.Val()
		}

		// Use NAK type as producer ID for shard selection
		if !r.ring.Write(uint64(ControlTypeNAK), cp) {
			return false
		}
	}
	return true
}

// TryPop attempts to pop a control packet from the ring.
// Returns (packet, true) if successful, (zero, false) if ring is empty.
//
// NOT thread-safe for multiple consumers - designed for single EventLoop consumer.
func (r *SendControlRing) TryPop() (ControlPacket, bool) {
	item, ok := r.ring.TryRead()
	if !ok {
		return ControlPacket{}, false
	}
	cp, ok := item.(ControlPacket)
	if !ok {
		return ControlPacket{}, false
	}
	return cp, true
}

// DrainBatch drains up to max control packets from the ring.
// Returns slice of drained packets (may be empty if ring is empty).
//
// NOT thread-safe for multiple consumers - designed for single EventLoop consumer.
func (r *SendControlRing) DrainBatch(max int) []ControlPacket {
	if max <= 0 {
		return nil
	}

	result := make([]ControlPacket, 0, max)
	for i := 0; i < max; i++ {
		cp, ok := r.TryPop()
		if !ok {
			break
		}
		result = append(result, cp)
	}
	return result
}

// Len returns an approximate count of items in the ring.
// Note: This is approximate due to concurrent access.
func (r *SendControlRing) Len() int {
	return int(r.ring.Len())
}

// Shards returns the number of shards configured for this ring.
func (r *SendControlRing) Shards() int {
	return r.shards
}

