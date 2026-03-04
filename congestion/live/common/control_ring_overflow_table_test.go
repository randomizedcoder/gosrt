//go:build go1.18

package common

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// =============================================================================
// Control Ring Overflow Table-Driven Tests
//
// Phase 3.2: Comprehensive tests for generic control ring overflow scenarios.
// Tests the common ControlRing[T] implementation used by both sender and receiver.
//
// Reference: completely_lockfree_receiver.md Section 5.1.1
// =============================================================================

// TestControlRing_Creation_Comprehensive tests all creation scenarios.
func TestControlRing_Creation_Comprehensive(t *testing.T) {
	testCases := []struct {
		name         string
		size         int
		shards       int
		expectShards int
		expectError  bool
	}{
		// Normal configurations
		{"normal_128_1", 128, 1, 1, false},
		{"normal_256_2", 256, 2, 2, false},
		{"normal_64_4", 64, 4, 4, false},
		{"large_4096_1", 4096, 1, 1, false},

		// Default handling
		{"zero_size", 0, 1, 1, false},
		{"zero_shards", 128, 0, 1, false},
		{"both_zero", 0, 0, 1, false},
		{"negative_size", -1, 1, 1, false},
		{"negative_shards", 128, -1, 1, false},
		{"both_negative", -1, -1, 1, false},

		// Edge cases
		{"minimal_1_1", 1, 1, 1, false},
		{"power_of_two", 256, 1, 1, false},
		{"non_power_of_two", 100, 1, 1, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](tc.size, tc.shards)

			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, ring)
				require.Equal(t, tc.expectShards, ring.Shards())
				require.Equal(t, 0, ring.Len())
				require.Greater(t, ring.Cap(), 0)
			}
		})
	}
}

// TestControlRing_Overflow_TableDriven tests overflow behavior.
func TestControlRing_Overflow_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		ringSize      int
		shards        int
		pushCount     int
		expectMinDrop int
	}{
		{"tiny_ring_overflow", 4, 1, 10, 1},
		{"small_ring_overflow", 16, 1, 32, 1},
		{"medium_overflow", 64, 2, 200, 1},
		{"no_overflow", 100, 1, 50, 0},
		{"exact_capacity", 32, 1, 32, 0},
		{"one_over", 32, 1, 33, 0}, // May or may not overflow
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](tc.ringSize, tc.shards)
			require.NoError(t, err)

			pushed := 0
			dropped := 0

			for i := 0; i < tc.pushCount; i++ {
				pkt := TestPacket{Type: uint8(i % 256), Seq: uint32(i)}
				if ring.Push(0, pkt) {
					pushed++
				} else {
					dropped++
				}
			}

			t.Logf("%s: pushed=%d, dropped=%d, len=%d, cap=%d",
				tc.name, pushed, dropped, ring.Len(), ring.Cap())

			require.GreaterOrEqual(t, dropped, tc.expectMinDrop)
			require.Equal(t, tc.pushCount, pushed+dropped)
		})
	}
}

// TestControlRing_TryPop_States_TableDriven tests TryPop in various states.
func TestControlRing_TryPop_States_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		setupRing func(*ControlRing[TestPacket])
		expectOK  bool
		expectSeq uint32
	}{
		{
			name:      "empty_ring",
			setupRing: func(r *ControlRing[TestPacket]) {},
			expectOK:  false,
		},
		{
			name: "single_item",
			setupRing: func(r *ControlRing[TestPacket]) {
				r.Push(0, TestPacket{Seq: 42})
			},
			expectOK:  true,
			expectSeq: 42,
		},
		{
			name: "after_drain",
			setupRing: func(r *ControlRing[TestPacket]) {
				r.Push(0, TestPacket{Seq: 1})
				r.TryPop()
			},
			expectOK: false,
		},
		{
			name: "fifo_order",
			setupRing: func(r *ControlRing[TestPacket]) {
				r.Push(0, TestPacket{Seq: 100})
				r.Push(0, TestPacket{Seq: 200})
			},
			expectOK:  true,
			expectSeq: 100, // First in, first out
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](32, 1)
			require.NoError(t, err)

			tc.setupRing(ring)

			pkt, ok := ring.TryPop()

			require.Equal(t, tc.expectOK, ok)
			if tc.expectOK {
				require.Equal(t, tc.expectSeq, pkt.Seq)
			}
		})
	}
}

// TestControlRing_MultiShard_Distribution_TableDriven tests multi-shard behavior.
func TestControlRing_MultiShard_Distribution_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		shards      int
		pushPattern []uint64 // Shard IDs to push to
		pushCount   int
	}{
		{
			name:        "single_shard_all",
			shards:      1,
			pushPattern: []uint64{0, 0, 0, 0},
			pushCount:   4,
		},
		{
			name:        "two_shards_alternating",
			shards:      2,
			pushPattern: []uint64{0, 1, 0, 1},
			pushCount:   4,
		},
		{
			name:        "four_shards_round_robin",
			shards:      4,
			pushPattern: []uint64{0, 1, 2, 3, 0, 1, 2, 3},
			pushCount:   8,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](64, tc.shards)
			require.NoError(t, err)

			require.Equal(t, tc.shards, ring.Shards())

			for i := 0; i < tc.pushCount; i++ {
				shardID := tc.pushPattern[i%len(tc.pushPattern)]
				ok := ring.Push(shardID, TestPacket{Seq: uint32(i)})
				require.True(t, ok)
			}

			require.Equal(t, tc.pushCount, ring.Len())

			// Drain and count
			count := 0
			for {
				_, ok := ring.TryPop()
				if !ok {
					break
				}
				count++
			}
			require.Equal(t, tc.pushCount, count)
		})
	}
}

// TestControlRing_Concurrent_Overflow_TableDriven tests concurrent overflow scenarios.
func TestControlRing_Concurrent_Overflow_TableDriven(t *testing.T) {
	testCases := []struct {
		name           string
		ringSize       int
		shards         int
		numProducers   int
		pushesPerProd  int
		consumerActive bool
	}{
		{
			name:           "overflow_no_consumer",
			ringSize:       32,
			shards:         1,
			numProducers:   4,
			pushesPerProd:  100,
			consumerActive: false,
		},
		{
			name:           "overflow_with_consumer",
			ringSize:       32,
			shards:         2,
			numProducers:   4,
			pushesPerProd:  100,
			consumerActive: true,
		},
		{
			name:           "high_shards",
			ringSize:       64,
			shards:         4,
			numProducers:   8,
			pushesPerProd:  50,
			consumerActive: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](tc.ringSize, tc.shards)
			require.NoError(t, err)

			var pushed, dropped, consumed atomic.Int64
			var wg sync.WaitGroup
			stop := make(chan struct{})

			// Consumer
			if tc.consumerActive {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							for {
								if _, ok := ring.TryPop(); ok {
									consumed.Add(1)
								} else {
									return
								}
							}
						default:
							if _, ok := ring.TryPop(); ok {
								consumed.Add(1)
							}
						}
					}
				}()
			}

			// Producers
			for p := 0; p < tc.numProducers; p++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					for i := 0; i < tc.pushesPerProd; i++ {
						pkt := TestPacket{
							Type: uint8(id),
							Seq:  uint32(id*tc.pushesPerProd + i),
						}
						if ring.Push(uint64(id%tc.shards), pkt) {
							pushed.Add(1)
						} else {
							dropped.Add(1)
						}
					}
				}(p)
			}

			time.Sleep(50 * time.Millisecond)
			close(stop)
			wg.Wait()

			total := int64(tc.numProducers * tc.pushesPerProd)
			t.Logf("%s: pushed=%d, dropped=%d, consumed=%d",
				tc.name, pushed.Load(), dropped.Load(), consumed.Load())

			require.Equal(t, total, pushed.Load()+dropped.Load())
			if tc.consumerActive {
				require.Equal(t, pushed.Load(), consumed.Load())
			}
		})
	}
}

// TestControlRing_Len_Cap_TableDriven tests Len and Cap accuracy.
func TestControlRing_Len_Cap_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		ringSize  int
		shards    int
		pushCount int
		popCount  int
		expectLen int
	}{
		{"empty", 32, 1, 0, 0, 0},
		{"half_full", 32, 1, 16, 0, 16},
		{"full", 32, 1, 32, 0, 32},
		{"push_pop_equal", 32, 1, 20, 20, 0},
		{"partial_drain", 32, 1, 20, 10, 10},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](tc.ringSize, tc.shards)
			require.NoError(t, err)

			// Push
			for i := 0; i < tc.pushCount; i++ {
				ring.Push(0, TestPacket{Seq: uint32(i)})
			}

			// Pop
			for i := 0; i < tc.popCount; i++ {
				ring.TryPop()
			}

			require.Equal(t, tc.expectLen, ring.Len())
			require.Greater(t, ring.Cap(), 0)
		})
	}
}

// TestControlRing_ZeroValue_TableDriven tests zero value handling.
func TestControlRing_ZeroValue_TableDriven(t *testing.T) {
	testCases := []struct {
		name   string
		packet TestPacket
	}{
		{"zero_value", TestPacket{}},
		{"zero_seq", TestPacket{Type: 1, Seq: 0}},
		{"zero_type", TestPacket{Type: 0, Seq: 100}},
		{"max_values", TestPacket{Type: 255, Seq: 0xFFFFFFFF}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](32, 1)
			require.NoError(t, err)

			ok := ring.Push(0, tc.packet)
			require.True(t, ok)

			got, ok := ring.TryPop()
			require.True(t, ok)
			require.Equal(t, tc.packet, got)
		})
	}
}

// TestControlRing_RecoveryAfterFull tests recovery after ring becomes full.
func TestControlRing_RecoveryAfterFull(t *testing.T) {
	ring, err := NewControlRing[TestPacket](4, 1)
	require.NoError(t, err)

	// Fill ring
	for i := 0; i < 4; i++ {
		ok := ring.Push(0, TestPacket{Seq: uint32(i)})
		require.True(t, ok)
	}

	// Verify full
	ok := ring.Push(0, TestPacket{Seq: 99})
	require.False(t, ok, "ring should be full")

	// Pop one
	pkt, ok := ring.TryPop()
	require.True(t, ok)
	require.Equal(t, uint32(0), pkt.Seq)

	// Now push should succeed
	ok = ring.Push(0, TestPacket{Seq: 99})
	require.True(t, ok)

	// Verify all items can be drained
	count := 0
	for {
		_, popOk := ring.TryPop()
		if !popOk {
			break
		}
		count++
	}
	require.Equal(t, 4, count) // 3 original + 1 new
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkControlRing_OverflowRecovery(b *testing.B) {
	ring, _ := NewControlRing[TestPacket](64, 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Fill
		for j := 0; j < 100; j++ {
			ring.Push(0, TestPacket{Seq: uint32(j)})
		}
		// Drain
		for {
			if _, ok := ring.TryPop(); !ok {
				break
			}
		}
	}
}
