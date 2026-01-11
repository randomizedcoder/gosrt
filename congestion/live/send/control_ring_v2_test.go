//go:build go1.18

package send

import (
	"sync"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Unit tests for SendControlRingV2 (optimized separate ACK/NAK rings)
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRingV2_NewSendControlRingV2(t *testing.T) {
	tests := []struct {
		name        string
		ackSize     int
		nakSize     int
		shards      int
		expectError bool
	}{
		{"default", 256, 64, 2, false},
		{"single_shard", 256, 64, 1, false},
		{"4_shards", 256, 64, 4, false},
		{"zero_defaults", 0, 0, 0, false},
		{"large_ack_ring", 8192, 64, 2, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring, err := NewSendControlRingV2(tt.ackSize, tt.nakSize, tt.shards)
			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, ring)
			} else {
				require.NoError(t, err)
				require.NotNil(t, ring)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ACK Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRingV2_PushACK_Basic(t *testing.T) {
	ring, err := NewSendControlRingV2(16, 8, 2)
	require.NoError(t, err)

	// Push ACK
	ok := ring.PushACK(100)
	require.True(t, ok)
	require.Equal(t, 1, ring.ACKLen())

	// Pop and verify
	seq, ok := ring.TryPopACK()
	require.True(t, ok)
	require.Equal(t, uint32(100), seq)
	require.Equal(t, 0, ring.ACKLen())
}

func TestSendControlRingV2_PushACKCircular_Basic(t *testing.T) {
	ring, err := NewSendControlRingV2(16, 8, 2)
	require.NoError(t, err)

	// Push ACK using circular.Number
	seq := circular.New(100, packet.MAX_SEQUENCENUMBER)
	ok := ring.PushACKCircular(seq)
	require.True(t, ok)

	// Pop and verify
	result, ok := ring.TryPopACK()
	require.True(t, ok)
	require.Equal(t, uint32(100), result)
}

func TestSendControlRingV2_PushACK_Multiple(t *testing.T) {
	ring, err := NewSendControlRingV2(32, 8, 2)
	require.NoError(t, err)

	// Push multiple ACKs
	for seq := uint32(100); seq < 120; seq++ {
		ok := ring.PushACK(seq)
		require.True(t, ok)
	}
	require.Equal(t, 20, ring.ACKLen())

	// Pop all and verify
	seqs := make([]uint32, 0, 20)
	for i := 0; i < 20; i++ {
		seq, ok := ring.TryPopACK()
		require.True(t, ok)
		seqs = append(seqs, seq)
	}
	require.Equal(t, 20, len(seqs))
	require.Equal(t, 0, ring.ACKLen())
}

// ═══════════════════════════════════════════════════════════════════════════════
// NAK Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRingV2_PushNAK_Basic(t *testing.T) {
	ring, err := NewSendControlRingV2(16, 16, 2)
	require.NoError(t, err)

	// Push NAK with a few sequences
	seqs := []circular.Number{
		circular.New(100, packet.MAX_SEQUENCENUMBER),
		circular.New(101, packet.MAX_SEQUENCENUMBER),
		circular.New(102, packet.MAX_SEQUENCENUMBER),
	}
	ok := ring.PushNAK(seqs)
	require.True(t, ok)
	require.Equal(t, 1, ring.NAKLen())

	// Pop and verify
	pkt, ok := ring.TryPopNAK()
	require.True(t, ok)
	require.Equal(t, uint8(3), pkt.Count)
	require.Equal(t, uint32(100), pkt.Sequences[0])
	require.Equal(t, uint32(101), pkt.Sequences[1])
	require.Equal(t, uint32(102), pkt.Sequences[2])
}

func TestSendControlRingV2_PushNAK_Empty(t *testing.T) {
	ring, err := NewSendControlRingV2(16, 16, 2)
	require.NoError(t, err)

	// Push empty NAK should succeed
	ok := ring.PushNAK([]circular.Number{})
	require.True(t, ok)
	require.Equal(t, 0, ring.NAKLen())
}

func TestSendControlRingV2_PushNAK_LargeChunked(t *testing.T) {
	ring, err := NewSendControlRingV2(16, 64, 2)
	require.NoError(t, err)

	// Push NAK with more than MaxNAKSequencesV2 sequences
	seqs := make([]circular.Number, 50) // > 32
	for i := 0; i < 50; i++ {
		seqs[i] = circular.New(uint32(100+i), packet.MAX_SEQUENCENUMBER)
	}
	ok := ring.PushNAK(seqs)
	require.True(t, ok)
	require.Equal(t, 2, ring.NAKLen()) // Split into 2 packets (32 + 18)

	// Pop first chunk
	pkt1, ok := ring.TryPopNAK()
	require.True(t, ok)
	require.Equal(t, uint8(32), pkt1.Count)

	// Pop second chunk
	pkt2, ok := ring.TryPopNAK()
	require.True(t, ok)
	require.Equal(t, uint8(18), pkt2.Count)

	require.Equal(t, 0, ring.NAKLen())
}

// ═══════════════════════════════════════════════════════════════════════════════
// Empty Ring Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRingV2_Empty(t *testing.T) {
	ring, err := NewSendControlRingV2(16, 8, 2)
	require.NoError(t, err)

	// Pop from empty rings
	seq, ok := ring.TryPopACK()
	require.False(t, ok)
	require.Equal(t, uint32(0), seq)

	pkt, ok := ring.TryPopNAK()
	require.False(t, ok)
	require.Equal(t, NAKPacketV2{}, pkt)
}

// ═══════════════════════════════════════════════════════════════════════════════
// TotalLen Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRingV2_TotalLen(t *testing.T) {
	ring, err := NewSendControlRingV2(32, 32, 2)
	require.NoError(t, err)

	// Push ACKs and NAKs
	for i := 0; i < 10; i++ {
		ring.PushACK(uint32(i))
	}
	ring.PushNAK([]circular.Number{
		circular.New(100, packet.MAX_SEQUENCENUMBER),
		circular.New(101, packet.MAX_SEQUENCENUMBER),
	})

	require.Equal(t, 10, ring.ACKLen())
	require.Equal(t, 1, ring.NAKLen())
	require.Equal(t, 11, ring.TotalLen())
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent Access Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRingV2_ConcurrentPushACK(t *testing.T) {
	ring, err := NewSendControlRingV2(1024, 64, 4)
	require.NoError(t, err)

	const numGoroutines = 4
	const acksPerGoroutine = 100

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < acksPerGoroutine; i++ {
				seq := uint32(goroutineID*acksPerGoroutine + i)
				for !ring.PushACK(seq) {
					// Retry if ring full
				}
			}
		}(g)
	}
	wg.Wait()

	require.Equal(t, numGoroutines*acksPerGoroutine, ring.ACKLen())

	// Drain and verify all ACKs present
	seqSet := make(map[uint32]bool)
	for i := 0; i < numGoroutines*acksPerGoroutine; i++ {
		seq, ok := ring.TryPopACK()
		require.True(t, ok)
		seqSet[seq] = true
	}
	require.Equal(t, numGoroutines*acksPerGoroutine, len(seqSet))
}

// ═══════════════════════════════════════════════════════════════════════════════
// EventLoop Simulation Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRingV2_EventLoop_ProcessCycle(t *testing.T) {
	ring, err := NewSendControlRingV2(64, 16, 2)
	require.NoError(t, err)

	// Simulate multiple cycles of push/process
	for cycle := 0; cycle < 10; cycle++ {
		// Push some ACKs and NAKs (simulates io_uring completion handlers)
		for i := 0; i < 5; i++ {
			ring.PushACK(uint32(cycle*100 + i))
		}
		ring.PushNAK([]circular.Number{
			circular.New(uint32(cycle*100+50), packet.MAX_SEQUENCENUMBER),
			circular.New(uint32(cycle*100+51), packet.MAX_SEQUENCENUMBER),
		})

		// Process all ACKs (simulates EventLoop drain)
		ackCount := 0
		for {
			_, ok := ring.TryPopACK()
			if !ok {
				break
			}
			ackCount++
		}
		require.Equal(t, 5, ackCount)

		// Process all NAKs
		nakCount := 0
		for {
			_, ok := ring.TryPopNAK()
			if !ok {
				break
			}
			nakCount++
		}
		require.Equal(t, 1, nakCount)

		require.Equal(t, 0, ring.TotalLen())
	}
}
