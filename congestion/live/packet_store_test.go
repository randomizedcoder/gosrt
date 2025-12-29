//go:build go1.18

package live

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// mockPacket creates a mock packet with given sequence number for testing
func mockPacket(seq uint32) packet.Packet {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")
	pkt := packet.NewPacket(addr)
	pkt.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	return pkt
}

// TestPacketStore_IterateFrom_BTree tests IterateFrom using AscendGreaterOrEqual
func TestPacketStore_IterateFrom_BTree(t *testing.T) {
	store := NewBTreePacketStore(32)

	// Insert packets: 100, 200, 300, 400, 500
	for _, seq := range []uint32{100, 200, 300, 400, 500} {
		store.Insert(mockPacket(seq))
	}

	t.Run("IterateFrom middle", func(t *testing.T) {
		var seqs []uint32
		store.IterateFrom(circular.New(250, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should start from 300 (first >= 250)
		require.Equal(t, []uint32{300, 400, 500}, seqs)
	})

	t.Run("IterateFrom exact match", func(t *testing.T) {
		var seqs []uint32
		store.IterateFrom(circular.New(200, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should start from 200 (exact match)
		require.Equal(t, []uint32{200, 300, 400, 500}, seqs)
	})

	t.Run("IterateFrom beginning", func(t *testing.T) {
		var seqs []uint32
		store.IterateFrom(circular.New(50, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should get all packets
		require.Equal(t, []uint32{100, 200, 300, 400, 500}, seqs)
	})

	t.Run("IterateFrom past end", func(t *testing.T) {
		var seqs []uint32
		store.IterateFrom(circular.New(600, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should get no packets
		require.Empty(t, seqs)
	})

	t.Run("IterateFrom with early stop", func(t *testing.T) {
		var seqs []uint32
		store.IterateFrom(circular.New(200, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return len(seqs) < 2 // Stop after 2
		})
		require.Equal(t, []uint32{200, 300}, seqs)
	})

	t.Run("IterateFrom empty store", func(t *testing.T) {
		emptyStore := NewBTreePacketStore(32)
		var seqs []uint32
		emptyStore.IterateFrom(circular.New(100, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})
		require.Empty(t, seqs)
	})
}

// TestPacketStore_IterateFrom_List tests IterateFrom for list-based store (fallback)
func TestPacketStore_IterateFrom_List(t *testing.T) {
	store := NewListPacketStore()

	// Insert packets: 100, 200, 300, 400, 500
	for _, seq := range []uint32{100, 200, 300, 400, 500} {
		store.Insert(mockPacket(seq))
	}

	t.Run("IterateFrom middle", func(t *testing.T) {
		var seqs []uint32
		store.IterateFrom(circular.New(250, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should start from 300 (first >= 250)
		require.Equal(t, []uint32{300, 400, 500}, seqs)
	})

	t.Run("IterateFrom exact match", func(t *testing.T) {
		var seqs []uint32
		store.IterateFrom(circular.New(200, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})
		require.Equal(t, []uint32{200, 300, 400, 500}, seqs)
	})
}

// TestPacketStore_IterateFrom_Wraparound tests sequence number wraparound handling
func TestPacketStore_IterateFrom_Wraparound(t *testing.T) {
	// Test with sequences near MAX_SEQUENCENUMBER (31-bit: 0x7FFFFFFF)
	// Wraparound: MAX-2, MAX-1, MAX, 0, 1, 2
	// In circular order: MAX-2 < MAX-1 < MAX < 0 < 1 < 2

	t.Run("BTree wraparound - full ordering", func(t *testing.T) {
		store := NewBTreePacketStore(32)

		// Insert sequences that span wraparound (in random order to test sorting)
		maxSeq := packet.MAX_SEQUENCENUMBER
		seqsToInsert := []uint32{1, maxSeq, 0, maxSeq - 1, 2, maxSeq - 2}
		for _, seq := range seqsToInsert {
			store.Insert(mockPacket(seq))
		}

		// Iterate all - should be in circular order
		var seqs []uint32
		store.Iterate(func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})

		// Expected circular order: MAX-2, MAX-1, MAX, 0, 1, 2
		expectedOrder := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		require.Equal(t, expectedOrder, seqs, "BTree should maintain circular order across MAX→0 boundary")
	})

	t.Run("BTree wraparound - IterateFrom before wrap", func(t *testing.T) {
		store := NewBTreePacketStore(32)

		maxSeq := packet.MAX_SEQUENCENUMBER
		seqsToInsert := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		for _, seq := range seqsToInsert {
			store.Insert(mockPacket(seq))
		}

		// IterateFrom MAX-1 should get: MAX-1, MAX, 0, 1, 2
		var seqs []uint32
		store.IterateFrom(circular.New(maxSeq-1, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})

		expectedOrder := []uint32{maxSeq - 1, maxSeq, 0, 1, 2}
		require.Equal(t, expectedOrder, seqs, "IterateFrom should continue across MAX→0 wraparound")
	})

	t.Run("BTree wraparound - IterateFrom after wrap", func(t *testing.T) {
		store := NewBTreePacketStore(32)

		maxSeq := packet.MAX_SEQUENCENUMBER
		seqsToInsert := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		for _, seq := range seqsToInsert {
			store.Insert(mockPacket(seq))
		}

		// IterateFrom 0 should get: 0, 1, 2 (sequences after wrap)
		var seqs []uint32
		store.IterateFrom(circular.New(0, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})

		expectedOrder := []uint32{0, 1, 2}
		require.Equal(t, expectedOrder, seqs, "IterateFrom after wrap should only get post-wrap packets")
	})

	t.Run("BTree wraparound - Min is MAX-2", func(t *testing.T) {
		store := NewBTreePacketStore(32)

		maxSeq := packet.MAX_SEQUENCENUMBER
		seqsToInsert := []uint32{1, maxSeq, 0, maxSeq - 1, 2, maxSeq - 2}
		for _, seq := range seqsToInsert {
			store.Insert(mockPacket(seq))
		}

		// Min should be MAX-2 (the "oldest" in circular order)
		min := store.Min()
		require.NotNil(t, min)
		require.Equal(t, maxSeq-2, min.Header().PacketSequenceNumber.Val(),
			"Min() should return the circularly-smallest sequence (MAX-2)")
	})

	t.Run("List wraparound - full ordering", func(t *testing.T) {
		store := NewListPacketStore()

		maxSeq := packet.MAX_SEQUENCENUMBER
		seqsToInsert := []uint32{1, maxSeq, 0, maxSeq - 1, 2, maxSeq - 2}
		for _, seq := range seqsToInsert {
			store.Insert(mockPacket(seq))
		}

		var seqs []uint32
		store.Iterate(func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})

		expectedOrder := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		require.Equal(t, expectedOrder, seqs, "List should maintain circular order across MAX→0 boundary")
	})
}

// TestPacketStore_SeqLess_Wraparound specifically tests that the btree uses correct circular comparison
func TestPacketStore_SeqLess_Wraparound(t *testing.T) {
	// This test verifies that the btree's SeqLess comparator handles MAX→0 wraparound
	// Bug reference: receiver_stream_tests_design.md Section 12

	maxSeq := packet.MAX_SEQUENCENUMBER

	testCases := []struct {
		name        string
		insertOrder []uint32
		expected    []uint32
	}{
		{
			name:        "Simple wraparound",
			insertOrder: []uint32{maxSeq, 0},
			expected:    []uint32{maxSeq, 0}, // MAX < 0 in circular terms
		},
		{
			name:        "Wraparound with gap",
			insertOrder: []uint32{50, maxSeq - 50},
			expected:    []uint32{maxSeq - 50, 50}, // MAX-50 < 50 in circular terms
		},
		{
			name:        "Multiple around boundary",
			insertOrder: []uint32{2, maxSeq - 1, 0, maxSeq, 1},
			expected:    []uint32{maxSeq - 1, maxSeq, 0, 1, 2},
		},
		{
			name:        "Large gap across boundary",
			insertOrder: []uint32{100, maxSeq - 100},
			expected:    []uint32{maxSeq - 100, 100}, // MAX-100 is "before" 100
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewBTreePacketStore(32)

			for _, seq := range tc.insertOrder {
				store.Insert(mockPacket(seq))
			}

			var result []uint32
			store.Iterate(func(pkt packet.Packet) bool {
				result = append(result, pkt.Header().PacketSequenceNumber.Val())
				return true
			})

			require.Equal(t, tc.expected, result,
				"BTree circular ordering failed for %s", tc.name)
		})
	}
}

// BenchmarkPacketStore_IterateFrom_vs_Iterate compares performance
func BenchmarkPacketStore_IterateFrom_vs_Iterate(b *testing.B) {
	store := NewBTreePacketStore(32)

	// Insert 1000 packets
	for i := uint32(0); i < 1000; i++ {
		store.Insert(mockPacket(i * 10)) // 0, 10, 20, ..., 9990
	}

	startSeq := circular.New(5000, packet.MAX_SEQUENCENUMBER) // Start from middle

	b.Run("Iterate_with_skip", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			count := 0
			store.Iterate(func(pkt packet.Packet) bool {
				seq := pkt.Header().PacketSequenceNumber.Val()
				if seq < startSeq.Val() {
					return true // Skip
				}
				count++
				return true
			})
		}
	})

	b.Run("IterateFrom_AscendGreaterOrEqual", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			count := 0
			store.IterateFrom(startSeq, func(pkt packet.Packet) bool {
				count++
				return true
			})
		}
	})
}

// ============================================================================
// RemoveAll Tests and Benchmarks (Phase 13: ACK Optimization)
// ============================================================================

// TestRemoveAll_Basic verifies RemoveAll removes matching packets in order
func TestRemoveAll_Basic(t *testing.T) {
	store := NewBTreePacketStore(32)

	// Insert packets 0-9
	for i := uint32(0); i < 10; i++ {
		p := mockPacket(i)
		p.Header().PktTsbpdTime = uint64(100 + i*10) // TSBPD times: 100, 110, 120, ...
		store.Insert(p)
	}

	// Remove packets with TSBPD time <= 150 (should remove 0-5)
	var delivered []uint32
	removed := store.RemoveAll(
		func(pkt packet.Packet) bool {
			return pkt.Header().PktTsbpdTime <= 150
		},
		func(pkt packet.Packet) {
			delivered = append(delivered, pkt.Header().PacketSequenceNumber.Val())
		},
	)

	require.Equal(t, 6, removed, "Should remove 6 packets (0-5)")
	require.Equal(t, []uint32{0, 1, 2, 3, 4, 5}, delivered, "Packets delivered in order")
	require.Equal(t, 4, store.Len(), "4 packets remaining")

	// Verify remaining packets
	var remaining []uint32
	store.Iterate(func(pkt packet.Packet) bool {
		remaining = append(remaining, pkt.Header().PacketSequenceNumber.Val())
		return true
	})
	require.Equal(t, []uint32{6, 7, 8, 9}, remaining, "Remaining packets correct")
}

// TestRemoveAll_StopsAtNonMatching verifies RemoveAll stops at first non-matching
func TestRemoveAll_StopsAtNonMatching(t *testing.T) {
	store := NewBTreePacketStore(32)

	// Insert packets 0, 1, 2, 10, 11, 12 (gap at 3-9)
	for _, seq := range []uint32{0, 1, 2, 10, 11, 12} {
		p := mockPacket(seq)
		p.Header().PktTsbpdTime = uint64(seq * 10) // TSBPD = seq * 10
		store.Insert(p)
	}

	// Remove packets with seq < 5 (should remove 0, 1, 2, stop at 10)
	var delivered []uint32
	removed := store.RemoveAll(
		func(pkt packet.Packet) bool {
			return pkt.Header().PacketSequenceNumber.Val() < 5
		},
		func(pkt packet.Packet) {
			delivered = append(delivered, pkt.Header().PacketSequenceNumber.Val())
		},
	)

	require.Equal(t, 3, removed, "Should remove 3 packets")
	require.Equal(t, []uint32{0, 1, 2}, delivered, "Packets delivered in order")
	require.Equal(t, 3, store.Len(), "3 packets remaining")
}

// TestRemoveAll_Empty verifies RemoveAll handles empty btree
func TestRemoveAll_Empty(t *testing.T) {
	store := NewBTreePacketStore(32)

	removed := store.RemoveAll(
		func(pkt packet.Packet) bool { return true },
		func(pkt packet.Packet) {},
	)

	require.Equal(t, 0, removed, "Should remove 0 packets from empty btree")
}

// TestRemoveAll_NoMatch verifies RemoveAll handles no matching packets
func TestRemoveAll_NoMatch(t *testing.T) {
	store := NewBTreePacketStore(32)

	for i := uint32(0); i < 5; i++ {
		store.Insert(mockPacket(i))
	}

	// Predicate that matches nothing
	removed := store.RemoveAll(
		func(pkt packet.Packet) bool { return false },
		func(pkt packet.Packet) {},
	)

	require.Equal(t, 0, removed, "Should remove 0 packets")
	require.Equal(t, 5, store.Len(), "All packets remain")
}

// TestRemoveAll_All verifies RemoveAll can remove all packets
func TestRemoveAll_All(t *testing.T) {
	store := NewBTreePacketStore(32)

	for i := uint32(0); i < 100; i++ {
		store.Insert(mockPacket(i))
	}

	// Remove all
	removed := store.RemoveAll(
		func(pkt packet.Packet) bool { return true },
		func(pkt packet.Packet) {},
	)

	require.Equal(t, 100, removed, "Should remove all 100 packets")
	require.Equal(t, 0, store.Len(), "Btree should be empty")
}

// TestRemoveAll_MatchesSlow verifies optimized RemoveAll matches slow version
func TestRemoveAll_MatchesSlow(t *testing.T) {
	// Create two identical stores
	store1 := NewBTreePacketStore(32).(*btreePacketStore)
	store2 := NewBTreePacketStore(32).(*btreePacketStore)

	// Insert same packets
	for i := uint32(0); i < 100; i++ {
		p1 := mockPacket(i)
		p1.Header().PktTsbpdTime = uint64(i * 10)
		p2 := mockPacket(i)
		p2.Header().PktTsbpdTime = uint64(i * 10)
		store1.Insert(p1)
		store2.Insert(p2)
	}

	// Use same predicate
	threshold := uint64(500)
	predicate := func(pkt packet.Packet) bool {
		return pkt.Header().PktTsbpdTime <= threshold
	}

	// Track deliveries
	var delivered1, delivered2 []uint32

	// Run both
	removed1 := store1.RemoveAll(predicate, func(pkt packet.Packet) {
		delivered1 = append(delivered1, pkt.Header().PacketSequenceNumber.Val())
	})
	removed2 := store2.RemoveAllSlow(predicate, func(pkt packet.Packet) {
		delivered2 = append(delivered2, pkt.Header().PacketSequenceNumber.Val())
	})

	// Verify same results
	require.Equal(t, removed1, removed2, "Both should remove same count")
	require.Equal(t, delivered1, delivered2, "Both should deliver in same order")
	require.Equal(t, store1.Len(), store2.Len(), "Both should have same length")
}

// BenchmarkRemoveAll_Optimized_vs_Slow compares the two implementations
// Note: Setup cost is included in timing but is identical for both versions,
// so relative comparison is valid. Uses -benchtime=100x for consistent iterations.
func BenchmarkRemoveAll_Optimized_vs_Slow(b *testing.B) {
	scenarios := []struct {
		name        string
		totalPkts   int
		removeCount int
	}{
		{"Remove_10_from_100", 100, 10},
		{"Remove_50_from_100", 100, 50},
		{"Remove_100_from_1000", 1000, 100},
		{"Remove_500_from_1000", 1000, 500},
	}

	for _, sc := range scenarios {
		threshold := uint64(sc.removeCount - 1)
		predicate := func(pkt packet.Packet) bool {
			return pkt.Header().PktTsbpdTime <= threshold
		}
		deliverFunc := func(pkt packet.Packet) {}

		b.Run(sc.name+"_Optimized", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				store := NewBTreePacketStore(32).(*btreePacketStore)
				for j := 0; j < sc.totalPkts; j++ {
					p := mockPacket(uint32(j))
					p.Header().PktTsbpdTime = uint64(j)
					store.Insert(p)
				}
				store.RemoveAll(predicate, deliverFunc)
			}
		})

		b.Run(sc.name+"_Slow", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				store := NewBTreePacketStore(32).(*btreePacketStore)
				for j := 0; j < sc.totalPkts; j++ {
					p := mockPacket(uint32(j))
					p.Header().PktTsbpdTime = uint64(j)
					store.Insert(p)
				}
				store.RemoveAllSlow(predicate, deliverFunc)
			}
		})
	}
}

// BenchmarkRemoveAllOnly benchmarks just the RemoveAll operation using a pool
// of pre-created stores. More accurate but complex.
func BenchmarkRemoveAllOnly(b *testing.B) {
	const totalPkts = 1000
	const removeCount = 500
	const poolSize = 10000 // Pre-create this many stores

	threshold := uint64(removeCount - 1)
	predicate := func(pkt packet.Packet) bool {
		return pkt.Header().PktTsbpdTime <= threshold
	}
	deliverFunc := func(pkt packet.Packet) {}

	// Pre-create a pool of stores
	makeStore := func() *btreePacketStore {
		store := NewBTreePacketStore(32).(*btreePacketStore)
		for j := 0; j < totalPkts; j++ {
			p := mockPacket(uint32(j))
			p.Header().PktTsbpdTime = uint64(j)
			store.Insert(p)
		}
		return store
	}

	b.Run("Optimized", func(b *testing.B) {
		stores := make([]*btreePacketStore, poolSize)
		for i := range stores {
			stores[i] = makeStore()
		}
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			idx := i % poolSize
			if i > 0 && idx == 0 {
				// Refill pool when exhausted
				b.StopTimer()
				for j := range stores {
					stores[j] = makeStore()
				}
				b.StartTimer()
			}
			stores[idx].RemoveAll(predicate, deliverFunc)
		}
	})

	b.Run("Slow", func(b *testing.B) {
		stores := make([]*btreePacketStore, poolSize)
		for i := range stores {
			stores[i] = makeStore()
		}
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			idx := i % poolSize
			if i > 0 && idx == 0 {
				b.StopTimer()
				for j := range stores {
					stores[j] = makeStore()
				}
				b.StartTimer()
			}
			stores[idx].RemoveAllSlow(predicate, deliverFunc)
		}
	})
}
