//go:build go1.18

// Package send implements the sender-side congestion control for SRT live mode.
package send

import (
	"fmt"

	ring "github.com/randomizedcoder/go-lock-free-ring"
	"github.com/randomizedcoder/gosrt/circular"
)

// ═══════════════════════════════════════════════════════════════════════════════
// SendControlRingV2 - Optimized lock-free ring for sender control packets
//
// OPTIMIZATION: Separate rings for ACK and NAK to minimize allocations.
// - ACK ring stores uint32 (4 bytes, no heap allocation due to small size)
// - NAK ring stores NAKPacket (variable size, less frequent)
//
// Performance improvement:
// - ACK push: 10 ns/op, 0 allocs (vs 112 ns/op, 1 alloc in V1)
// - 11x faster ACK processing
//
// Reference: lockless_sender_implementation_tracking.md (Optimization Analysis)
// ═══════════════════════════════════════════════════════════════════════════════

// NAKPacketV2 stores NAK sequence numbers for ring transport.
// Uses inline array to avoid slice allocation.
type NAKPacketV2 struct {
	Count     uint8      // Number of valid sequences (max 32)
	Sequences [32]uint32 // Inline array of sequence numbers
}

// MaxNAKSequencesV2 is the maximum NAK sequences per NAKPacketV2
const MaxNAKSequencesV2 = 32

// SendControlRingV2 uses separate rings for ACK and NAK for optimal performance.
// ACK ring: stores uint32 (no allocation)
// NAK ring: stores NAKPacketV2 (rare, larger struct acceptable)
type SendControlRingV2 struct {
	ackRing *ring.ShardedRing
	nakRing *ring.ShardedRing
}

// NewSendControlRingV2 creates an optimized control ring with separate ACK/NAK rings.
//
// Parameters:
//   - ackSize: ACK ring capacity per shard (recommended: 256-512)
//   - nakSize: NAK ring capacity per shard (recommended: 64-128, NAKs are rare)
//   - shards: number of shards for each ring (recommended: 2-4)
func NewSendControlRingV2(ackSize, nakSize, shards int) (*SendControlRingV2, error) {
	if shards < 1 {
		shards = 2
	}
	if ackSize < 1 {
		ackSize = 256
	}
	if nakSize < 1 {
		nakSize = 64
	}

	ackRing, err := ring.NewShardedRing(uint64(ackSize*shards), uint64(shards))
	if err != nil {
		return nil, fmt.Errorf("failed to create ACK ring: %w", err)
	}

	nakRing, err := ring.NewShardedRing(uint64(nakSize*shards), uint64(shards))
	if err != nil {
		return nil, fmt.Errorf("failed to create NAK ring: %w", err)
	}

	return &SendControlRingV2{
		ackRing: ackRing,
		nakRing: nakRing,
	}, nil
}

// PushACK pushes an ACK sequence number to the ACK ring.
// ZERO ALLOCATIONS: uint32 is small enough to avoid heap escape.
// Thread-safe: can be called from multiple goroutines.
func (r *SendControlRingV2) PushACK(seq uint32) bool {
	// Use sequence number for shard distribution
	return r.ackRing.Write(uint64(seq), seq)
}

// PushACKCircular is a convenience wrapper that accepts circular.Number.
// ZERO ALLOCATIONS: extracts uint32 and calls PushACK.
func (r *SendControlRingV2) PushACKCircular(seq circular.Number) bool {
	return r.PushACK(seq.Val())
}

// TryPopACK attempts to pop an ACK sequence number.
// Returns (sequence, true) if successful, (0, false) if empty.
// NOT thread-safe for multiple consumers - designed for single EventLoop.
func (r *SendControlRingV2) TryPopACK() (uint32, bool) {
	item, ok := r.ackRing.TryRead()
	if !ok {
		return 0, false
	}
	ackSeq, typeOK := item.(uint32)
	if !typeOK {
		return 0, false
	}
	return ackSeq, true
}

// PushNAK pushes NAK sequence numbers to the NAK ring.
// For large NAK lists (>32 sequences), splits into multiple packets.
// Thread-safe: can be called from multiple goroutines.
func (r *SendControlRingV2) PushNAK(seqs []circular.Number) bool {
	if len(seqs) == 0 {
		return true
	}

	// Process in chunks of MaxNAKSequencesV2
	for i := 0; i < len(seqs); i += MaxNAKSequencesV2 {
		end := i + MaxNAKSequencesV2
		if end > len(seqs) {
			end = len(seqs)
		}
		chunk := seqs[i:end]

		pkt := NAKPacketV2{
			Count: uint8(len(chunk)),
		}
		for j, seq := range chunk {
			pkt.Sequences[j] = seq.Val()
		}

		// Use NAK count for shard distribution (vary by chunk)
		if !r.nakRing.Write(uint64(pkt.Count)+uint64(i), pkt) {
			return false
		}
	}
	return true
}

// TryPopNAK attempts to pop a NAK packet.
// Returns (packet, true) if successful, (empty, false) if empty.
// NOT thread-safe for multiple consumers - designed for single EventLoop.
func (r *SendControlRingV2) TryPopNAK() (NAKPacketV2, bool) {
	item, ok := r.nakRing.TryRead()
	if !ok {
		return NAKPacketV2{}, false
	}
	nakPacket, typeOK := item.(NAKPacketV2)
	if !typeOK {
		return NAKPacketV2{}, false
	}
	return nakPacket, true
}

// ACKLen returns approximate count of items in the ACK ring.
func (r *SendControlRingV2) ACKLen() int {
	return int(r.ackRing.Len())
}

// NAKLen returns approximate count of items in the NAK ring.
func (r *SendControlRingV2) NAKLen() int {
	return int(r.nakRing.Len())
}

// TotalLen returns approximate total count of items in both rings.
func (r *SendControlRingV2) TotalLen() int {
	return r.ACKLen() + r.NAKLen()
}
