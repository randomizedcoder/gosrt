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
// Unit tests for SendControlRing
// Reference: lockless_sender_implementation_plan.md Phase 3
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRing_NewSendControlRing(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		shards      int
		expectError bool
	}{
		{
			name:        "default_2_shards",
			size:        256,
			shards:      2,
			expectError: false,
		},
		{
			name:        "single_shard",
			size:        256,
			shards:      1,
			expectError: false,
		},
		{
			name:        "4_shards",
			size:        256,
			shards:      4,
			expectError: false,
		},
		{
			name:        "zero_shards_defaults_to_2",
			size:        256,
			shards:      0,
			expectError: false,
		},
		{
			name:        "zero_size_defaults",
			size:        0,
			shards:      2,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring, err := NewSendControlRing(tt.size, tt.shards)
			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, ring)
			} else {
				require.NoError(t, err)
				require.NotNil(t, ring)
				require.GreaterOrEqual(t, ring.Shards(), 1)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ACK Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRing_PushACK_Basic(t *testing.T) {
	ring, err := NewSendControlRing(16, 2)
	require.NoError(t, err)

	// Push an ACK
	seq := circular.New(100, packet.MAX_SEQUENCENUMBER)
	ok := ring.PushACK(seq)
	require.True(t, ok)
	require.Equal(t, 1, ring.Len())

	// Pop and verify
	cp, ok := ring.TryPop()
	require.True(t, ok)
	require.Equal(t, ControlTypeACK, cp.Type)
	require.Equal(t, uint32(100), cp.ACKSequence)
	require.Equal(t, 0, ring.Len())
}

func TestSendControlRing_PushACK_Multiple(t *testing.T) {
	ring, err := NewSendControlRing(16, 2)
	require.NoError(t, err)

	// Push multiple ACKs
	for seq := uint32(100); seq < 110; seq++ {
		ok := ring.PushACK(circular.New(seq, packet.MAX_SEQUENCENUMBER))
		require.True(t, ok)
	}
	require.Equal(t, 10, ring.Len())

	// Pop all and verify
	seqs := make([]uint32, 0, 10)
	for i := 0; i < 10; i++ {
		cp, ok := ring.TryPop()
		require.True(t, ok)
		require.Equal(t, ControlTypeACK, cp.Type)
		seqs = append(seqs, cp.ACKSequence)
	}
	require.Equal(t, 10, len(seqs))
	require.Equal(t, 0, ring.Len())
}

// ═══════════════════════════════════════════════════════════════════════════════
// NAK Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRing_PushNAK_Basic(t *testing.T) {
	ring, err := NewSendControlRing(16, 2)
	require.NoError(t, err)

	// Push a NAK with a few sequences
	seqs := []circular.Number{
		circular.New(100, packet.MAX_SEQUENCENUMBER),
		circular.New(101, packet.MAX_SEQUENCENUMBER),
		circular.New(102, packet.MAX_SEQUENCENUMBER),
	}
	ok := ring.PushNAK(seqs)
	require.True(t, ok)
	require.Equal(t, 1, ring.Len())

	// Pop and verify
	cp, ok := ring.TryPop()
	require.True(t, ok)
	require.Equal(t, ControlTypeNAK, cp.Type)
	require.Equal(t, 3, cp.NAKCount)
	require.Equal(t, uint32(100), cp.NAKSequences[0])
	require.Equal(t, uint32(101), cp.NAKSequences[1])
	require.Equal(t, uint32(102), cp.NAKSequences[2])
}

func TestSendControlRing_PushNAK_Empty(t *testing.T) {
	ring, err := NewSendControlRing(16, 2)
	require.NoError(t, err)

	// Push empty NAK should succeed
	ok := ring.PushNAK([]circular.Number{})
	require.True(t, ok)
	require.Equal(t, 0, ring.Len()) // No packet pushed for empty NAK
}

func TestSendControlRing_PushNAK_LargeChunked(t *testing.T) {
	ring, err := NewSendControlRing(64, 2)
	require.NoError(t, err)

	// Push NAK with more than MaxNAKSequencesPerPacket sequences
	seqs := make([]circular.Number, 50) // > 32
	for i := 0; i < 50; i++ {
		seqs[i] = circular.New(uint32(100+i), packet.MAX_SEQUENCENUMBER)
	}
	ok := ring.PushNAK(seqs)
	require.True(t, ok)
	require.Equal(t, 2, ring.Len()) // Should be split into 2 packets (32 + 18)

	// Pop first chunk
	cp1, ok := ring.TryPop()
	require.True(t, ok)
	require.Equal(t, ControlTypeNAK, cp1.Type)
	require.Equal(t, 32, cp1.NAKCount) // First chunk: 32 sequences

	// Pop second chunk
	cp2, ok := ring.TryPop()
	require.True(t, ok)
	require.Equal(t, ControlTypeNAK, cp2.Type)
	require.Equal(t, 18, cp2.NAKCount) // Second chunk: 18 sequences

	require.Equal(t, 0, ring.Len())
}

// ═══════════════════════════════════════════════════════════════════════════════
// Mixed ACK/NAK Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRing_MixedACKNAK(t *testing.T) {
	ring, err := NewSendControlRing(32, 2)
	require.NoError(t, err)

	// Push ACK
	ring.PushACK(circular.New(100, packet.MAX_SEQUENCENUMBER))

	// Push NAK
	ring.PushNAK([]circular.Number{
		circular.New(200, packet.MAX_SEQUENCENUMBER),
		circular.New(201, packet.MAX_SEQUENCENUMBER),
	})

	// Push another ACK
	ring.PushACK(circular.New(150, packet.MAX_SEQUENCENUMBER))

	require.Equal(t, 3, ring.Len())

	// Pop all - order depends on shard selection
	ackCount := 0
	nakCount := 0
	for i := 0; i < 3; i++ {
		cp, ok := ring.TryPop()
		require.True(t, ok)
		switch cp.Type {
		case ControlTypeACK:
			ackCount++
		case ControlTypeNAK:
			nakCount++
		}
	}
	require.Equal(t, 2, ackCount)
	require.Equal(t, 1, nakCount)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Empty Ring Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRing_Empty(t *testing.T) {
	ring, err := NewSendControlRing(16, 2)
	require.NoError(t, err)

	// Pop from empty ring
	cp, ok := ring.TryPop()
	require.False(t, ok)
	require.Equal(t, ControlPacket{}, cp)

	// DrainBatch from empty ring
	batch := ring.DrainBatch(10)
	require.Empty(t, batch)
}

// ═══════════════════════════════════════════════════════════════════════════════
// DrainBatch Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRing_DrainBatch(t *testing.T) {
	ring, err := NewSendControlRing(32, 2)
	require.NoError(t, err)

	// Push 10 ACKs
	for seq := uint32(100); seq < 110; seq++ {
		ring.PushACK(circular.New(seq, packet.MAX_SEQUENCENUMBER))
	}
	require.Equal(t, 10, ring.Len())

	// Drain batch of 5
	batch := ring.DrainBatch(5)
	require.Equal(t, 5, len(batch))
	require.Equal(t, 5, ring.Len())

	// Drain remaining
	batch = ring.DrainBatch(10)
	require.Equal(t, 5, len(batch))
	require.Equal(t, 0, ring.Len())
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent Access Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRing_ConcurrentPushACK(t *testing.T) {
	ring, err := NewSendControlRing(1024, 4)
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
				for !ring.PushACK(circular.New(seq, packet.MAX_SEQUENCENUMBER)) {
					// Retry if ring full
				}
			}
		}(g)
	}
	wg.Wait()

	require.Equal(t, numGoroutines*acksPerGoroutine, ring.Len())

	// Drain and verify all ACKs present
	seqSet := make(map[uint32]bool)
	for i := 0; i < numGoroutines*acksPerGoroutine; i++ {
		cp, ok := ring.TryPop()
		require.True(t, ok)
		require.Equal(t, ControlTypeACK, cp.Type)
		seqSet[cp.ACKSequence] = true
	}
	require.Equal(t, numGoroutines*acksPerGoroutine, len(seqSet))
}

func TestSendControlRing_ConcurrentPushNAK(t *testing.T) {
	ring, err := NewSendControlRing(1024, 4)
	require.NoError(t, err)

	const numGoroutines = 4
	const naksPerGoroutine = 50

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < naksPerGoroutine; i++ {
				seqs := []circular.Number{
					circular.New(uint32(goroutineID*1000+i*2), packet.MAX_SEQUENCENUMBER),
					circular.New(uint32(goroutineID*1000+i*2+1), packet.MAX_SEQUENCENUMBER),
				}
				for !ring.PushNAK(seqs) {
					// Retry if ring full
				}
			}
		}(g)
	}
	wg.Wait()

	require.Equal(t, numGoroutines*naksPerGoroutine, ring.Len())

	// Drain and verify all NAKs present
	nakCount := 0
	for {
		cp, ok := ring.TryPop()
		if !ok {
			break
		}
		require.Equal(t, ControlTypeNAK, cp.Type)
		nakCount++
	}
	require.Equal(t, numGoroutines*naksPerGoroutine, nakCount)
}

// ═══════════════════════════════════════════════════════════════════════════════
// EventLoop Simulation Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendControlRing_EventLoop_ProcessCycle(t *testing.T) {
	ring, err := NewSendControlRing(64, 2)
	require.NoError(t, err)

	// Simulate multiple cycles of push/process
	for cycle := 0; cycle < 10; cycle++ {
		// Push some ACKs and NAKs (simulates io_uring completion handlers)
		for i := 0; i < 5; i++ {
			ring.PushACK(circular.New(uint32(cycle*100+i), packet.MAX_SEQUENCENUMBER))
		}
		ring.PushNAK([]circular.Number{
			circular.New(uint32(cycle*100+50), packet.MAX_SEQUENCENUMBER),
			circular.New(uint32(cycle*100+51), packet.MAX_SEQUENCENUMBER),
		})

		// Process all (simulates EventLoop drain)
		ackCount := 0
		nakCount := 0
		for {
			cp, ok := ring.TryPop()
			if !ok {
				break
			}
			switch cp.Type {
			case ControlTypeACK:
				ackCount++
			case ControlTypeNAK:
				nakCount++
			}
		}
		require.Equal(t, 5, ackCount)
		require.Equal(t, 1, nakCount)
		require.Equal(t, 0, ring.Len())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkSendControlRing_PushACK(b *testing.B) {
	ring, _ := NewSendControlRing(8192, 2)
	seq := circular.New(100, packet.MAX_SEQUENCENUMBER)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !ring.PushACK(seq) {
			// Ring full, drain
			for {
				_, ok := ring.TryPop()
				if !ok {
					break
				}
			}
			ring.PushACK(seq)
		}
	}
}

func BenchmarkSendControlRing_PushNAK_Small(b *testing.B) {
	ring, _ := NewSendControlRing(8192, 2)
	seqs := []circular.Number{
		circular.New(100, packet.MAX_SEQUENCENUMBER),
		circular.New(101, packet.MAX_SEQUENCENUMBER),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !ring.PushNAK(seqs) {
			// Ring full, drain
			for {
				_, ok := ring.TryPop()
				if !ok {
					break
				}
			}
			ring.PushNAK(seqs)
		}
	}
}

// BenchmarkSendControlRing_PushACK_Concurrent tests concurrent ACK pushes
// (simulates multiple io_uring completion handlers pushing ACKs)
// NOTE: TryPop() is NOT thread-safe - EventLoop is single consumer
func BenchmarkSendControlRing_PushACK_Concurrent(b *testing.B) {
	ring, err := NewSendControlRing(65536, 4) // Large ring to avoid full
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			ring.PushACK(circular.New(seq, packet.MAX_SEQUENCENUMBER))
			seq++
		}
	})
}

// BenchmarkSendControlRing_TryPop_SingleConsumer tests single consumer pop rate
// (simulates EventLoop draining the control ring)
func BenchmarkSendControlRing_TryPop_SingleConsumer(b *testing.B) {
	ring, err := NewSendControlRing(65536, 4)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	// Pre-fill ring
	for i := 0; i < 60000; i++ {
		ring.PushACK(circular.New(uint32(i), packet.MAX_SEQUENCENUMBER))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, ok := ring.TryPop()
		if !ok {
			// Refill if empty
			b.StopTimer()
			for j := 0; j < 60000; j++ {
				ring.PushACK(circular.New(uint32(j), packet.MAX_SEQUENCENUMBER))
			}
			b.StartTimer()
		}
	}
}
