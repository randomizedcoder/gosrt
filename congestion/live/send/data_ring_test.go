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
// Unit tests for SendPacketRing
// Reference: lockless_sender_implementation_plan.md Step 2.7
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketRing_NewSendPacketRing(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		shards      int
		expectError bool
	}{
		{
			name:        "default_single_shard",
			size:        1024,
			shards:      1,
			expectError: false,
		},
		{
			name:        "multiple_shards",
			size:        1024,
			shards:      4,
			expectError: false,
		},
		{
			name:        "zero_shards_defaults_to_1",
			size:        1024,
			shards:      0,
			expectError: false,
		},
		{
			name:        "negative_shards_defaults_to_1",
			size:        1024,
			shards:      -1,
			expectError: false,
		},
		{
			name:        "zero_size_defaults",
			size:        0,
			shards:      1,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring, err := NewSendPacketRing(tt.size, tt.shards)
			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, ring)
			} else {
				require.NoError(t, err)
				require.NotNil(t, ring)
				// Verify shards is at least 1
				require.GreaterOrEqual(t, ring.Shards(), 1)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Single-Shard Tests (Default Configuration)
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketRing_SingleShard_Basic(t *testing.T) {
	ring, err := NewSendPacketRing(16, 1)
	require.NoError(t, err)
	require.Equal(t, 1, ring.Shards())

	// Push a packet
	p := createTestPacket(100)
	ok := ring.Push(p)
	require.True(t, ok)
	require.Equal(t, 1, ring.Len())

	// Pop the packet
	popped, ok := ring.TryPop()
	require.True(t, ok)
	require.NotNil(t, popped)
	require.Equal(t, uint32(100), popped.Header().PacketSequenceNumber.Val())
	require.Equal(t, 0, ring.Len())
}

func TestSendPacketRing_SingleShard_Ordering(t *testing.T) {
	ring, err := NewSendPacketRing(16, 1)
	require.NoError(t, err)

	// Push packets in order
	for seq := uint32(100); seq < 105; seq++ {
		p := createTestPacket(seq)
		ok := ring.Push(p)
		require.True(t, ok)
	}
	require.Equal(t, 5, ring.Len())

	// Pop should return in FIFO order (single shard)
	for seq := uint32(100); seq < 105; seq++ {
		popped, ok := ring.TryPop()
		require.True(t, ok)
		require.Equal(t, seq, popped.Header().PacketSequenceNumber.Val())
	}
	require.Equal(t, 0, ring.Len())
}

func TestSendPacketRing_SingleShard_DrainBatch(t *testing.T) {
	ring, err := NewSendPacketRing(16, 1)
	require.NoError(t, err)

	// Push 10 packets
	for seq := uint32(0); seq < 10; seq++ {
		p := createTestPacket(seq)
		ring.Push(p)
	}
	require.Equal(t, 10, ring.Len())

	// Drain batch of 5
	batch := ring.DrainBatch(5)
	require.Equal(t, 5, len(batch))
	require.Equal(t, 5, ring.Len())

	// Verify batch order
	for i, p := range batch {
		require.Equal(t, uint32(i), p.Header().PacketSequenceNumber.Val())
	}

	// Drain remaining
	batch = ring.DrainAll()
	require.Equal(t, 5, len(batch))
	require.Equal(t, 0, ring.Len())
}

func TestSendPacketRing_SingleShard_Empty(t *testing.T) {
	ring, err := NewSendPacketRing(16, 1)
	require.NoError(t, err)

	// Pop from empty ring
	p, ok := ring.TryPop()
	require.False(t, ok)
	require.Nil(t, p)

	// DrainBatch from empty ring
	batch := ring.DrainBatch(10)
	require.Empty(t, batch)

	// DrainAll from empty ring
	all := ring.DrainAll()
	require.Empty(t, all)
}

func TestSendPacketRing_SingleShard_Full(t *testing.T) {
	// Small ring to test full condition
	ring, err := NewSendPacketRing(4, 1)
	require.NoError(t, err)

	// Fill the ring
	for seq := uint32(0); seq < 4; seq++ {
		p := createTestPacket(seq)
		ok := ring.Push(p)
		require.True(t, ok, "should be able to push packet %d", seq)
	}

	// Next push should fail (ring full)
	p := createTestPacket(999)
	ok := ring.Push(p)
	require.False(t, ok, "push should fail when ring is full")

	// Drain one and try again
	_, ok = ring.TryPop()
	require.True(t, ok)

	ok = ring.Push(p)
	require.True(t, ok, "push should succeed after draining")
}

// ═══════════════════════════════════════════════════════════════════════════════
// Multi-Shard Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketRing_MultiShard_Basic(t *testing.T) {
	ring, err := NewSendPacketRing(1024, 4)
	require.NoError(t, err)
	require.Equal(t, 4, ring.Shards())

	// Push packets
	for seq := uint32(0); seq < 100; seq++ {
		p := createTestPacket(seq)
		ok := ring.Push(p)
		require.True(t, ok)
	}
	require.Equal(t, 100, ring.Len())

	// Drain all
	all := ring.DrainAll()
	require.Equal(t, 100, len(all))
	require.Equal(t, 0, ring.Len())
}

func TestSendPacketRing_MultiShard_ConcurrentPush(t *testing.T) {
	ring, err := NewSendPacketRing(4096, 4)
	require.NoError(t, err)

	const numGoroutines = 4
	const packetsPerGoroutine = 100

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < packetsPerGoroutine; i++ {
				seq := uint32(goroutineID*packetsPerGoroutine + i)
				p := createTestPacket(seq)
				// Retry on failure (ring might be temporarily full)
				for !ring.Push(p) {
					// Busy wait - in production would use backoff
				}
			}
		}(g)
	}
	wg.Wait()

	require.Equal(t, numGoroutines*packetsPerGoroutine, ring.Len())

	// Drain and verify all packets present
	all := ring.DrainAll()
	require.Equal(t, numGoroutines*packetsPerGoroutine, len(all))

	// Verify all sequence numbers are present (order may vary with multi-shard)
	seqSet := make(map[uint32]bool)
	for _, p := range all {
		seqSet[p.Header().PacketSequenceNumber.Val()] = true
	}
	require.Equal(t, numGoroutines*packetsPerGoroutine, len(seqSet))
}

// ═══════════════════════════════════════════════════════════════════════════════
// Table-Driven Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketRing_ShardConfigurations(t *testing.T) {
	tests := []struct {
		name          string
		shards        int
		pushCount     int
		expectOrdered bool // Only true for single shard
	}{
		{
			name:          "1_shard_strict_order",
			shards:        1,
			pushCount:     50,
			expectOrdered: true,
		},
		{
			name:          "2_shards",
			shards:        2,
			pushCount:     50,
			expectOrdered: false, // Multi-shard doesn't guarantee order
		},
		{
			name:          "4_shards",
			shards:        4,
			pushCount:     50,
			expectOrdered: false,
		},
		{
			name:          "8_shards",
			shards:        8,
			pushCount:     50,
			expectOrdered: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring, err := NewSendPacketRing(1024, tt.shards)
			require.NoError(t, err)

			// Push packets
			for seq := uint32(0); seq < uint32(tt.pushCount); seq++ {
				p := createTestPacket(seq)
				ok := ring.Push(p)
				require.True(t, ok)
			}

			// Drain all
			all := ring.DrainAll()
			require.Equal(t, tt.pushCount, len(all))

			if tt.expectOrdered {
				// Verify strict FIFO order
				for i, p := range all {
					require.Equal(t, uint32(i), p.Header().PacketSequenceNumber.Val(),
						"packet %d should have seq %d", i, i)
				}
			} else {
				// Just verify all packets are present (order may vary)
				seqSet := make(map[uint32]bool)
				for _, p := range all {
					seqSet[p.Header().PacketSequenceNumber.Val()] = true
				}
				require.Equal(t, tt.pushCount, len(seqSet))
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// EventLoop Simulation Tests (Single-Threaded Consumer)
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketRing_EventLoop_DrainCycle(t *testing.T) {
	ring, err := NewSendPacketRing(1024, 1)
	require.NoError(t, err)

	// Simulate multiple Push/Drain cycles (like EventLoop iterations)
	for cycle := 0; cycle < 10; cycle++ {
		// Push batch
		for seq := uint32(cycle * 10); seq < uint32((cycle+1)*10); seq++ {
			p := createTestPacket(seq)
			ring.Push(p)
		}

		// Drain all (like drainRingToBtree)
		batch := ring.DrainAll()
		require.Equal(t, 10, len(batch))
		require.Equal(t, 0, ring.Len())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkSendPacketRing_Push_1Shard(b *testing.B) {
	ring, _ := NewSendPacketRing(8192, 1)
	p := createTestPacket(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		if !ring.Push(p) {
			// Ring full, drain
			ring.DrainAll()
			ring.Push(p)
		}
	}
}

func BenchmarkSendPacketRing_Push_4Shards(b *testing.B) {
	ring, _ := NewSendPacketRing(8192, 4)
	p := createTestPacket(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		if !ring.Push(p) {
			ring.DrainAll()
			ring.Push(p)
		}
	}
}

func BenchmarkSendPacketRing_Push_8Shards(b *testing.B) {
	ring, _ := NewSendPacketRing(8192, 8)
	p := createTestPacket(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		if !ring.Push(p) {
			ring.DrainAll()
			ring.Push(p)
		}
	}
}

func BenchmarkSendPacketRing_DrainBatch(b *testing.B) {
	ring, _ := NewSendPacketRing(8192, 1)

	// Pre-fill
	for i := 0; i < 1000; i++ {
		p := createTestPacket(uint32(i))
		ring.Push(p)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := ring.DrainBatch(100)
		// Re-push for next iteration
		for _, p := range batch {
			ring.Push(p)
		}
	}
}

func BenchmarkSendPacketRing_PushPop_Concurrent(b *testing.B) {
	ring, _ := NewSendPacketRing(8192, 4)

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			p := createTestPacket(seq)
			seq++
			if ring.Push(p) {
				ring.TryPop()
			}
		}
	})
}
