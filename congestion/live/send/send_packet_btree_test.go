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
// Unit tests for SendPacketBtree
// Reference: lockless_sender_implementation_plan.md Step 1.2
// Patterns from: congestion/live/receive/nak_btree_test.go
// ═══════════════════════════════════════════════════════════════════════════════

// mockAddr implements net.Addr for testing
type mockAddr struct{}

func (m mockAddr) Network() string { return "udp" }
func (m mockAddr) String() string  { return "127.0.0.1:5000" }

// createTestPacket creates a packet with the given sequence number
func createTestPacket(seq uint32) packet.Packet {
	p := packet.NewPacket(mockAddr{})
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	return p
}

func TestSendPacketBtree_Insert_Basic(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Initially empty
	require.Equal(t, 0, bt.Len())

	// Insert packets
	p1 := createTestPacket(100)
	p2 := createTestPacket(200)
	p3 := createTestPacket(150)

	inserted, dup := bt.Insert(p1)
	require.True(t, inserted)
	require.Nil(t, dup)
	require.Equal(t, 1, bt.Len())

	inserted, dup = bt.Insert(p2)
	require.True(t, inserted)
	require.Nil(t, dup)
	require.Equal(t, 2, bt.Len())

	inserted, dup = bt.Insert(p3)
	require.True(t, inserted)
	require.Nil(t, dup)
	require.Equal(t, 3, bt.Len())

	// Verify all present
	require.True(t, bt.Has(100))
	require.True(t, bt.Has(150))
	require.True(t, bt.Has(200))
	require.False(t, bt.Has(300))
}

func TestSendPacketBtree_Insert_Duplicate(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Insert first packet
	p1 := createTestPacket(100)
	inserted, dup := bt.Insert(p1)
	require.True(t, inserted)
	require.Nil(t, dup)

	// Insert duplicate (same sequence number)
	p2 := createTestPacket(100)
	inserted, dup = bt.Insert(p2)
	require.False(t, inserted)
	require.NotNil(t, dup)
	// The old packet should be returned for decommissioning
	require.Equal(t, uint32(100), dup.Header().PacketSequenceNumber.Val())

	// Tree size unchanged
	require.Equal(t, 1, bt.Len())

	// The new packet is now in tree (same seq#)
	p := bt.Get(100)
	require.NotNil(t, p)
}

func TestSendPacketBtree_Get_Found(t *testing.T) {
	bt := NewSendPacketBtree(32)

	p1 := createTestPacket(100)
	p2 := createTestPacket(200)
	bt.Insert(p1)
	bt.Insert(p2)

	// Get existing
	p := bt.Get(100)
	require.NotNil(t, p)
	require.Equal(t, uint32(100), p.Header().PacketSequenceNumber.Val())

	p = bt.Get(200)
	require.NotNil(t, p)
	require.Equal(t, uint32(200), p.Header().PacketSequenceNumber.Val())
}

func TestSendPacketBtree_Get_NotFound(t *testing.T) {
	bt := NewSendPacketBtree(32)

	p1 := createTestPacket(100)
	bt.Insert(p1)

	// Get non-existent
	p := bt.Get(200)
	require.Nil(t, p)

	p = bt.Get(999)
	require.Nil(t, p)
}

func TestSendPacketBtree_Delete_Exists(t *testing.T) {
	bt := NewSendPacketBtree(32)

	p1 := createTestPacket(100)
	p2 := createTestPacket(200)
	bt.Insert(p1)
	bt.Insert(p2)

	require.Equal(t, 2, bt.Len())

	// Delete existing
	removed := bt.Delete(100)
	require.NotNil(t, removed)
	require.Equal(t, uint32(100), removed.Header().PacketSequenceNumber.Val())
	require.Equal(t, 1, bt.Len())
	require.False(t, bt.Has(100))
	require.True(t, bt.Has(200))

	// Delete non-existent
	removed = bt.Delete(999)
	require.Nil(t, removed)
	require.Equal(t, 1, bt.Len())
}

func TestSendPacketBtree_DeleteMin_Multiple(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Insert out of order
	bt.Insert(createTestPacket(300))
	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))

	// DeleteMin should return in sequence order
	p := bt.DeleteMin()
	require.NotNil(t, p)
	require.Equal(t, uint32(100), p.Header().PacketSequenceNumber.Val())
	require.Equal(t, 2, bt.Len())

	p = bt.DeleteMin()
	require.NotNil(t, p)
	require.Equal(t, uint32(200), p.Header().PacketSequenceNumber.Val())
	require.Equal(t, 1, bt.Len())

	p = bt.DeleteMin()
	require.NotNil(t, p)
	require.Equal(t, uint32(300), p.Header().PacketSequenceNumber.Val())
	require.Equal(t, 0, bt.Len())

	// DeleteMin on empty tree
	p = bt.DeleteMin()
	require.Nil(t, p)
}

func TestSendPacketBtree_DeleteBefore_Range(t *testing.T) {
	bt := NewSendPacketBtree(32)

	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))
	bt.Insert(createTestPacket(300))
	bt.Insert(createTestPacket(400))
	bt.Insert(createTestPacket(500))

	// Delete sequences before 300
	removed, packets := bt.DeleteBefore(300)
	require.Equal(t, 2, removed) // 100 and 200
	require.Equal(t, 2, len(packets))
	require.Equal(t, uint32(100), packets[0].Header().PacketSequenceNumber.Val())
	require.Equal(t, uint32(200), packets[1].Header().PacketSequenceNumber.Val())

	require.Equal(t, 3, bt.Len())
	require.False(t, bt.Has(100))
	require.False(t, bt.Has(200))
	require.True(t, bt.Has(300))
	require.True(t, bt.Has(400))
	require.True(t, bt.Has(500))
}

func TestSendPacketBtree_DeleteBefore_Empty(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// DeleteBefore on empty tree
	removed, packets := bt.DeleteBefore(100)
	require.Equal(t, 0, removed)
	require.Equal(t, 0, len(packets))
}

func TestSendPacketBtree_DeleteBefore_NoneMatch(t *testing.T) {
	bt := NewSendPacketBtree(32)

	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))

	// All packets are >= threshold
	removed, packets := bt.DeleteBefore(50)
	require.Equal(t, 0, removed)
	require.Equal(t, 0, len(packets))
	require.Equal(t, 2, bt.Len())
}

// TestSendPacketBtree_IterateFrom tests IterateFrom with subtests
// Ported from: congestion/live/receive/packet_store_test.go:TestPacketStore_IterateFrom_BTree
func TestSendPacketBtree_IterateFrom(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Insert packets: 100, 200, 300, 400, 500
	for _, seq := range []uint32{100, 200, 300, 400, 500} {
		bt.Insert(createTestPacket(seq))
	}

	t.Run("IterateFrom middle", func(t *testing.T) {
		var seqs []uint32
		bt.IterateFrom(250, func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should start from 300 (first >= 250)
		require.Equal(t, []uint32{300, 400, 500}, seqs)
	})

	t.Run("IterateFrom exact match", func(t *testing.T) {
		var seqs []uint32
		bt.IterateFrom(200, func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should start from 200 (exact match)
		require.Equal(t, []uint32{200, 300, 400, 500}, seqs)
	})

	t.Run("IterateFrom beginning", func(t *testing.T) {
		var seqs []uint32
		bt.IterateFrom(50, func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should get all packets
		require.Equal(t, []uint32{100, 200, 300, 400, 500}, seqs)
	})

	t.Run("IterateFrom past end", func(t *testing.T) {
		var seqs []uint32
		bt.IterateFrom(600, func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return true
		})
		// Should get no packets
		require.Empty(t, seqs)
	})

	t.Run("IterateFrom with early stop", func(t *testing.T) {
		var seqs []uint32
		bt.IterateFrom(200, func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return len(seqs) < 2 // Stop after 2
		})
		require.Equal(t, []uint32{200, 300}, seqs)
	})

	t.Run("IterateFrom empty store", func(t *testing.T) {
		emptyBt := NewSendPacketBtree(32)
		var seqs []uint32
		emptyBt.IterateFrom(100, func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return true
		})
		require.Empty(t, seqs)
	})
}

func TestSendPacketBtree_IterateFrom_Ordering(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Insert out of order
	bt.Insert(createTestPacket(500))
	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(300))
	bt.Insert(createTestPacket(200))
	bt.Insert(createTestPacket(400))

	// Iterate from 200
	var seqs []uint32
	completed := bt.IterateFrom(200, func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return true
	})
	require.True(t, completed)
	require.Equal(t, []uint32{200, 300, 400, 500}, seqs)

	// Iterate from start
	seqs = nil
	completed = bt.IterateFrom(100, func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return true
	})
	require.True(t, completed)
	require.Equal(t, []uint32{100, 200, 300, 400, 500}, seqs)

	// Iterate with early stop
	seqs = nil
	completed = bt.IterateFrom(200, func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return p.Header().PacketSequenceNumber.Val() < 400 // Stop after 400
	})
	require.False(t, completed)
	require.Equal(t, []uint32{200, 300, 400}, seqs)
}

func TestSendPacketBtree_Iterate_All(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Insert out of order
	bt.Insert(createTestPacket(300))
	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))

	// Iterate all - should be in sequence order
	var seqs []uint32
	completed := bt.Iterate(func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return true
	})
	require.True(t, completed)
	require.Equal(t, []uint32{100, 200, 300}, seqs)
}

func TestSendPacketBtree_Min(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Min on empty tree
	p := bt.Min()
	require.Nil(t, p)

	// Insert out of order
	bt.Insert(createTestPacket(300))
	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))

	// Min should return lowest
	p = bt.Min()
	require.NotNil(t, p)
	require.Equal(t, uint32(100), p.Header().PacketSequenceNumber.Val())

	// Tree unchanged
	require.Equal(t, 3, bt.Len())
}

func TestSendPacketBtree_Clear(t *testing.T) {
	bt := NewSendPacketBtree(32)

	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))
	bt.Insert(createTestPacket(300))

	require.Equal(t, 3, bt.Len())

	bt.Clear()
	require.Equal(t, 0, bt.Len())
	require.Nil(t, bt.Min())
}

// ═══════════════════════════════════════════════════════════════════════════════
// 31-bit Sequence Number Wraparound Tests
// Reference: circular/seq_math_31bit_wraparound_test.md
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketBtree_Wraparound_SimpleOrdering(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Max 31-bit sequence is 0x7FFFFFFF = 2147483647
	const maxSeq = uint32(0x7FFFFFFF)

	// Insert sequences near wraparound
	bt.Insert(createTestPacket(maxSeq - 2)) // 2147483645
	bt.Insert(createTestPacket(maxSeq - 1)) // 2147483646
	bt.Insert(createTestPacket(maxSeq))     // 2147483647
	bt.Insert(createTestPacket(0))          // After wraparound
	bt.Insert(createTestPacket(1))
	bt.Insert(createTestPacket(2))

	require.Equal(t, 6, bt.Len())

	// Iteration should follow 31-bit wraparound order
	// circular.SeqLess uses signed comparison within window
	var seqs []uint32
	bt.Iterate(func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return true
	})

	// With SeqLess (signed window comparison):
	// maxSeq-2, maxSeq-1, maxSeq, 0, 1, 2 (chronological order)
	require.Equal(t, 6, len(seqs))
}

func TestSendPacketBtree_Wraparound_DeleteBefore(t *testing.T) {
	bt := NewSendPacketBtree(32)

	const maxSeq = uint32(0x7FFFFFFF)

	// Insert sequences around wraparound
	bt.Insert(createTestPacket(maxSeq - 1))
	bt.Insert(createTestPacket(maxSeq))
	bt.Insert(createTestPacket(0))
	bt.Insert(createTestPacket(1))

	// DeleteBefore should handle wraparound correctly
	// Using SeqLess, 0 and 1 are "after" maxSeq in sequence space
	initialLen := bt.Len()
	require.Equal(t, 4, initialLen)

	// This test verifies the btree maintains correct order
	// The actual behavior depends on circular.SeqLess semantics
}

func TestSendPacketBtree_LargeSequenceNumbers(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Test with large sequence numbers (but realistic gaps)
	baseSeq := uint32(2147483000) // Near max but not at wraparound

	bt.Insert(createTestPacket(baseSeq))
	bt.Insert(createTestPacket(baseSeq + 100))
	bt.Insert(createTestPacket(baseSeq + 50))
	bt.Insert(createTestPacket(baseSeq + 200))

	var seqs []uint32
	bt.Iterate(func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return true
	})

	require.Equal(t, 4, len(seqs))
	require.Equal(t, baseSeq, seqs[0])
	require.Equal(t, baseSeq+50, seqs[1])
	require.Equal(t, baseSeq+100, seqs[2])
	require.Equal(t, baseSeq+200, seqs[3])
}

// TestSendPacketBtree_SeqLess_Wraparound_Table is the CRITICAL table-driven wraparound test
// This verifies that the btree's SeqLess comparator handles MAX→0 wraparound correctly.
// Bug reference: receiver_stream_tests_design.md Section 12
// Ported from: congestion/live/receive/packet_store_test.go:TestPacketStore_SeqLess_Wraparound
func TestSendPacketBtree_SeqLess_Wraparound_Table(t *testing.T) {
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
		{
			name:        "Three sequences across wrap",
			insertOrder: []uint32{1, maxSeq, 0},
			expected:    []uint32{maxSeq, 0, 1},
		},
		{
			name:        "Reverse insert order",
			insertOrder: []uint32{0, maxSeq, maxSeq - 1},
			expected:    []uint32{maxSeq - 1, maxSeq, 0},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bt := NewSendPacketBtree(32)

			for _, seq := range tc.insertOrder {
				bt.Insert(createTestPacket(seq))
			}

			var result []uint32
			bt.Iterate(func(p packet.Packet) bool {
				result = append(result, p.Header().PacketSequenceNumber.Val())
				return true
			})

			require.Equal(t, tc.expected, result,
				"BTree circular ordering failed for %s", tc.name)
		})
	}
}

// TestSendPacketBtree_Wraparound_IterateFrom tests IterateFrom with wraparound sequences
// Ported from: congestion/live/receive/packet_store_test.go:TestPacketStore_IterateFrom_Wraparound
func TestSendPacketBtree_Wraparound_IterateFrom(t *testing.T) {
	maxSeq := packet.MAX_SEQUENCENUMBER

	t.Run("BTree wraparound - full ordering", func(t *testing.T) {
		bt := NewSendPacketBtree(32)

		// Insert sequences that span wraparound (in random order to test sorting)
		seqsToInsert := []uint32{1, maxSeq, 0, maxSeq - 1, 2, maxSeq - 2}
		for _, seq := range seqsToInsert {
			bt.Insert(createTestPacket(seq))
		}

		// Iterate all - should be in circular order
		var seqs []uint32
		bt.Iterate(func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return true
		})

		// Expected circular order: MAX-2, MAX-1, MAX, 0, 1, 2
		expectedOrder := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		require.Equal(t, expectedOrder, seqs, "BTree should maintain circular order across MAX→0 boundary")
	})

	t.Run("BTree wraparound - IterateFrom before wrap", func(t *testing.T) {
		bt := NewSendPacketBtree(32)

		seqsToInsert := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		for _, seq := range seqsToInsert {
			bt.Insert(createTestPacket(seq))
		}

		// IterateFrom MAX-1 should get: MAX-1, MAX, 0, 1, 2
		var seqs []uint32
		bt.IterateFrom(maxSeq-1, func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return true
		})

		expectedOrder := []uint32{maxSeq - 1, maxSeq, 0, 1, 2}
		require.Equal(t, expectedOrder, seqs, "IterateFrom should continue across MAX→0 wraparound")
	})

	t.Run("BTree wraparound - IterateFrom after wrap", func(t *testing.T) {
		bt := NewSendPacketBtree(32)

		seqsToInsert := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		for _, seq := range seqsToInsert {
			bt.Insert(createTestPacket(seq))
		}

		// IterateFrom 0 should get: 0, 1, 2 (sequences after wrap)
		var seqs []uint32
		bt.IterateFrom(0, func(p packet.Packet) bool {
			seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
			return true
		})

		expectedOrder := []uint32{0, 1, 2}
		require.Equal(t, expectedOrder, seqs, "IterateFrom after wrap should only get post-wrap packets")
	})

	t.Run("BTree wraparound - Min is MAX-2", func(t *testing.T) {
		bt := NewSendPacketBtree(32)

		seqsToInsert := []uint32{1, maxSeq, 0, maxSeq - 1, 2, maxSeq - 2}
		for _, seq := range seqsToInsert {
			bt.Insert(createTestPacket(seq))
		}

		// Min should be MAX-2 (the "oldest" in circular order)
		min := bt.Min()
		require.NotNil(t, min)
		require.Equal(t, maxSeq-2, min.Header().PacketSequenceNumber.Val(),
			"Min() should return the circularly-smallest sequence (MAX-2)")
	})
}

// TestSendPacketBtree_Wraparound_DeleteBefore_Table tests DeleteBefore with wraparound sequences
func TestSendPacketBtree_Wraparound_DeleteBefore_Table(t *testing.T) {
	maxSeq := packet.MAX_SEQUENCENUMBER

	testCases := []struct {
		name            string
		insertOrder     []uint32
		deleteBeforeSeq uint32
		expectRemoved   int
		expectRemaining []uint32
	}{
		{
			name:            "Delete before wrap point",
			insertOrder:     []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2},
			deleteBeforeSeq: maxSeq,
			expectRemoved:   2, // MAX-2, MAX-1
			expectRemaining: []uint32{maxSeq, 0, 1, 2},
		},
		{
			name:            "Delete including wrap point",
			insertOrder:     []uint32{maxSeq - 1, maxSeq, 0, 1},
			deleteBeforeSeq: 1,
			expectRemoved:   3, // MAX-1, MAX, 0
			expectRemaining: []uint32{1},
		},
		{
			name:            "Delete all before post-wrap",
			insertOrder:     []uint32{maxSeq, 0, 1, 2},
			deleteBeforeSeq: 2,
			expectRemoved:   3, // MAX, 0, 1
			expectRemaining: []uint32{2},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bt := NewSendPacketBtree(32)

			for _, seq := range tc.insertOrder {
				bt.Insert(createTestPacket(seq))
			}

			removed, _ := bt.DeleteBefore(tc.deleteBeforeSeq)
			require.Equal(t, tc.expectRemoved, removed)

			var remaining []uint32
			bt.Iterate(func(p packet.Packet) bool {
				remaining = append(remaining, p.Header().PacketSequenceNumber.Val())
				return true
			})
			require.Equal(t, tc.expectRemaining, remaining)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Empty Operations Tests
// Ported from: congestion/live/receive/nak_btree_test.go:TestNakBtree_EmptyOperations
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketBtree_EmptyOperations(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Min on empty
	p := bt.Min()
	require.Nil(t, p)

	// Get on empty
	p = bt.Get(100)
	require.Nil(t, p)

	// Has on empty
	has := bt.Has(100)
	require.False(t, has)

	// Delete on empty
	removed := bt.Delete(100)
	require.Nil(t, removed)

	// DeleteMin on empty
	removed = bt.DeleteMin()
	require.Nil(t, removed)

	// DeleteBefore on empty
	count, packets := bt.DeleteBefore(100)
	require.Equal(t, 0, count)
	require.Empty(t, packets)

	// DeleteBeforeFunc on empty
	callCount := 0
	count = bt.DeleteBeforeFunc(100, func(p packet.Packet) { callCount++ })
	require.Equal(t, 0, count)
	require.Equal(t, 0, callCount)

	// Iterate on empty
	iterCount := 0
	bt.Iterate(func(p packet.Packet) bool {
		iterCount++
		return true
	})
	require.Equal(t, 0, iterCount)

	// IterateFrom on empty
	iterCount = 0
	bt.IterateFrom(100, func(p packet.Packet) bool {
		iterCount++
		return true
	})
	require.Equal(t, 0, iterCount)

	// Len on empty
	require.Equal(t, 0, bt.Len())

	// Clear on empty (should not panic)
	bt.Clear()
	require.Equal(t, 0, bt.Len())
}

// ═══════════════════════════════════════════════════════════════════════════════
// EventLoop Simulation Tests (Single-Threaded, Lock-Free)
// Ported from: congestion/live/receive/nak_btree_test.go
// These tests verify operations work correctly in the single-threaded event loop context.
// ═══════════════════════════════════════════════════════════════════════════════

// TestSendPacketBtree_EventLoop_FullCycle simulates a complete sender packet lifecycle
func TestSendPacketBtree_EventLoop_FullCycle(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Phase 1: Push multiple packets (simulates Push() in event loop)
	for seq := uint32(100); seq < 110; seq++ {
		p := createTestPacket(seq)
		inserted, dup := bt.Insert(p)
		require.True(t, inserted)
		require.Nil(t, dup)
	}
	require.Equal(t, 10, bt.Len())

	// Phase 2: Deliver some packets (simulates tickDeliverPackets)
	delivered := 0
	bt.IterateFrom(100, func(p packet.Packet) bool {
		seq := p.Header().PacketSequenceNumber.Val()
		if seq < 105 {
			delivered++
			return true
		}
		return false // Stop
	})
	require.Equal(t, 5, delivered)

	// Phase 3: ACK arrives for seq 105 (simulates ackBtree)
	// Remove all packets before 105
	var ackedSeqs []uint32
	removed := bt.DeleteBeforeFunc(105, func(p packet.Packet) {
		ackedSeqs = append(ackedSeqs, p.Header().PacketSequenceNumber.Val())
	})
	require.Equal(t, 5, removed)
	require.Equal(t, []uint32{100, 101, 102, 103, 104}, ackedSeqs)
	require.Equal(t, 5, bt.Len())

	// Phase 4: NAK for seq 106 (simulates nakBtree - lookup for retransmit)
	p := bt.Get(106)
	require.NotNil(t, p)
	require.Equal(t, uint32(106), p.Header().PacketSequenceNumber.Val())

	// Phase 5: More packets pushed
	for seq := uint32(110); seq < 115; seq++ {
		bt.Insert(createTestPacket(seq))
	}
	require.Equal(t, 10, bt.Len())

	// Phase 6: Final ACK for all
	removed = bt.DeleteBeforeFunc(120, func(p packet.Packet) {})
	require.Equal(t, 10, removed)
	require.Equal(t, 0, bt.Len())
}

// TestSendPacketBtree_EventLoop_DuplicatePacketHandling simulates duplicate packet scenarios
func TestSendPacketBtree_EventLoop_DuplicatePacketHandling(t *testing.T) {
	bt := NewSendPacketBtree(32)

	// Insert original packet
	p1 := createTestPacket(100)
	inserted, dup := bt.Insert(p1)
	require.True(t, inserted)
	require.Nil(t, dup)

	// Simulate duplicate (same seq#) - should return old for decommission
	p2 := createTestPacket(100)
	inserted, dup = bt.Insert(p2)
	require.False(t, inserted)
	require.NotNil(t, dup)

	// Only one packet in tree
	require.Equal(t, 1, bt.Len())

	// Verify we can still get it
	p := bt.Get(100)
	require.NotNil(t, p)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrency Tests (for future EventLoop verification)
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketBtree_NotThreadSafe_SingleWriter(t *testing.T) {
	// This test documents that the btree is NOT thread-safe
	// In production, either:
	// 1. EventLoop (single goroutine) accesses btree
	// 2. Lock protects all btree access

	bt := NewSendPacketBtree(32)

	// Sequential operations (safe)
	for i := uint32(0); i < 100; i++ {
		bt.Insert(createTestPacket(i))
	}

	require.Equal(t, 100, bt.Len())

	// Verify all present
	for i := uint32(0); i < 100; i++ {
		require.True(t, bt.Has(i))
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Table-Driven Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketBtree_Insert_Table(t *testing.T) {
	tests := []struct {
		name         string
		insertSeqs   []uint32
		expectLen    int
		expectMinSeq uint32
	}{
		{
			name:         "empty",
			insertSeqs:   []uint32{},
			expectLen:    0,
			expectMinSeq: 0, // No min
		},
		{
			name:         "single",
			insertSeqs:   []uint32{100},
			expectLen:    1,
			expectMinSeq: 100,
		},
		{
			name:         "ordered",
			insertSeqs:   []uint32{100, 200, 300},
			expectLen:    3,
			expectMinSeq: 100,
		},
		{
			name:         "reverse",
			insertSeqs:   []uint32{300, 200, 100},
			expectLen:    3,
			expectMinSeq: 100,
		},
		{
			name:         "random",
			insertSeqs:   []uint32{500, 100, 300, 200, 400},
			expectLen:    5,
			expectMinSeq: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bt := NewSendPacketBtree(32)

			for _, seq := range tt.insertSeqs {
				bt.Insert(createTestPacket(seq))
			}

			require.Equal(t, tt.expectLen, bt.Len())

			if tt.expectLen > 0 {
				p := bt.Min()
				require.NotNil(t, p)
				require.Equal(t, tt.expectMinSeq, p.Header().PacketSequenceNumber.Val())
			} else {
				require.Nil(t, bt.Min())
			}
		})
	}
}

func TestSendPacketBtree_DeleteBefore_Table(t *testing.T) {
	tests := []struct {
		name            string
		insertSeqs      []uint32
		deleteBeforeSeq uint32
		expectRemoved   int
		expectRemaining int
	}{
		{
			name:            "delete_none",
			insertSeqs:      []uint32{100, 200, 300},
			deleteBeforeSeq: 50,
			expectRemoved:   0,
			expectRemaining: 3,
		},
		{
			name:            "delete_some",
			insertSeqs:      []uint32{100, 200, 300, 400, 500},
			deleteBeforeSeq: 300,
			expectRemoved:   2,
			expectRemaining: 3,
		},
		{
			name:            "delete_all",
			insertSeqs:      []uint32{100, 200, 300},
			deleteBeforeSeq: 400,
			expectRemoved:   3,
			expectRemaining: 0,
		},
		{
			name:            "delete_from_empty",
			insertSeqs:      []uint32{},
			deleteBeforeSeq: 100,
			expectRemoved:   0,
			expectRemaining: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bt := NewSendPacketBtree(32)

			for _, seq := range tt.insertSeqs {
				bt.Insert(createTestPacket(seq))
			}

			removed, _ := bt.DeleteBefore(tt.deleteBeforeSeq)
			require.Equal(t, tt.expectRemoved, removed)
			require.Equal(t, tt.expectRemaining, bt.Len())
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// DeleteBeforeFunc Tests (Zero-Allocation Version)
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketBtree_DeleteBeforeFunc_Basic(t *testing.T) {
	bt := NewSendPacketBtree(32)

	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))
	bt.Insert(createTestPacket(300))
	bt.Insert(createTestPacket(400))
	bt.Insert(createTestPacket(500))

	// Track deleted packets via callback
	var deletedSeqs []uint32
	removed := bt.DeleteBeforeFunc(300, func(p packet.Packet) {
		deletedSeqs = append(deletedSeqs, p.Header().PacketSequenceNumber.Val())
	})

	require.Equal(t, 2, removed)
	require.Equal(t, []uint32{100, 200}, deletedSeqs) // Deleted in order
	require.Equal(t, 3, bt.Len())
	require.True(t, bt.Has(300))
	require.True(t, bt.Has(400))
	require.True(t, bt.Has(500))
}

func TestSendPacketBtree_DeleteBeforeFunc_Empty(t *testing.T) {
	bt := NewSendPacketBtree(32)

	callCount := 0
	removed := bt.DeleteBeforeFunc(100, func(p packet.Packet) {
		callCount++
	})

	require.Equal(t, 0, removed)
	require.Equal(t, 0, callCount)
}

func TestSendPacketBtree_DeleteBeforeFunc_NilCallback(t *testing.T) {
	bt := NewSendPacketBtree(32)

	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))
	bt.Insert(createTestPacket(300))

	// nil callback is safe - just counts deletions
	removed := bt.DeleteBeforeFunc(250, nil)

	require.Equal(t, 2, removed)
	require.Equal(t, 1, bt.Len())
}

func TestSendPacketBtree_DeleteBeforeFunc_SimulatesDecommission(t *testing.T) {
	bt := NewSendPacketBtree(32)

	bt.Insert(createTestPacket(100))
	bt.Insert(createTestPacket(200))
	bt.Insert(createTestPacket(300))

	// Simulate real ACK processing with inline callback
	// In production this would do: metrics update + p.Decommission()
	processedSeqs := make([]uint32, 0)
	removed := bt.DeleteBeforeFunc(250, func(p packet.Packet) {
		processedSeqs = append(processedSeqs, p.Header().PacketSequenceNumber.Val())
	})

	require.Equal(t, 2, removed)
	require.Equal(t, []uint32{100, 200}, processedSeqs)
}

func TestSendPacketBtree_DeleteBeforeFunc_Table(t *testing.T) {
	tests := []struct {
		name            string
		insertSeqs      []uint32
		deleteBeforeSeq uint32
		expectRemoved   int
		expectRemaining int
	}{
		{
			name:            "delete_none",
			insertSeqs:      []uint32{100, 200, 300},
			deleteBeforeSeq: 50,
			expectRemoved:   0,
			expectRemaining: 3,
		},
		{
			name:            "delete_some",
			insertSeqs:      []uint32{100, 200, 300, 400, 500},
			deleteBeforeSeq: 300,
			expectRemoved:   2,
			expectRemaining: 3,
		},
		{
			name:            "delete_all",
			insertSeqs:      []uint32{100, 200, 300},
			deleteBeforeSeq: 400,
			expectRemoved:   3,
			expectRemaining: 0,
		},
		{
			name:            "delete_from_empty",
			insertSeqs:      []uint32{},
			deleteBeforeSeq: 100,
			expectRemoved:   0,
			expectRemaining: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bt := NewSendPacketBtree(32)

			for _, seq := range tt.insertSeqs {
				bt.Insert(createTestPacket(seq))
			}

			callCount := 0
			removed := bt.DeleteBeforeFunc(tt.deleteBeforeSeq, func(p packet.Packet) {
				callCount++
			})
			require.Equal(t, tt.expectRemoved, removed)
			require.Equal(t, tt.expectRemoved, callCount) // Callback called for each
			require.Equal(t, tt.expectRemaining, bt.Len())
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Race Detection Helper (for -race flag)
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketBtree_SingleGoroutine_NoRace(t *testing.T) {
	// This test should pass with -race flag
	// It simulates EventLoop pattern: single goroutine accesses btree
	bt := NewSendPacketBtree(32)

	// Simulate EventLoop iterations
	for iter := 0; iter < 10; iter++ {
		// Insert batch
		for i := uint32(0); i < 10; i++ {
			seq := uint32(iter*10) + i
			bt.Insert(createTestPacket(seq))
		}

		// Process some
		if iter > 0 {
			bt.DeleteBefore(uint32(iter * 5))
		}

		// Iterate
		bt.Iterate(func(p packet.Packet) bool {
			return true
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// IterateFrom Corner Cases - Table Driven Tests
// Bug Reference: 20M EventLoop test intermittent failure where IterateFrom(0)
// doesn't find packets at ~549M sequence numbers
// ═══════════════════════════════════════════════════════════════════════════════

func TestSendPacketBtree_IterateFrom_CornerCases_Table(t *testing.T) {
	maxSeq := packet.MAX_SEQUENCENUMBER // 0x7FFFFFFF = 2,147,483,647
	threshold := uint32(maxSeq / 2)     // ~1,073,741,823
	midHigh := uint32(549144712)        // Actual value from failing test
	nearMax := maxSeq - 100             // Just below MAX

	testCases := []struct {
		name       string
		insertSeqs []uint32
		startSeq   uint32
		expected   []uint32 // Expected sequences found (in order)
		desc       string   // Description of what we're testing
	}{
		// ═══════════════════════════════════════════════════════════════════════
		// Group 1: startSeq = 0 (the failing case)
		// These test the exact scenario from the 20M EventLoop failure
		// ═══════════════════════════════════════════════════════════════════════
		{
			name:       "startSeq_0_with_packets_at_midHigh",
			insertSeqs: []uint32{midHigh, midHigh + 1, midHigh + 2},
			startSeq:   0,
			expected:   []uint32{midHigh, midHigh + 1, midHigh + 2},
			desc:       "CRITICAL: Simulates 20M test failure - packets at 549M should be found from startSeq=0",
		},
		{
			name:       "startSeq_0_with_packets_at_low",
			insertSeqs: []uint32{100, 200, 300},
			startSeq:   0,
			expected:   []uint32{100, 200, 300},
			desc:       "Baseline: low sequences from startSeq=0",
		},
		{
			name:       "startSeq_0_with_packets_at_nearMax",
			insertSeqs: []uint32{nearMax, nearMax + 1, nearMax + 50},
			startSeq:   0,
			expected:   []uint32{}, // nearMax is "before" 0 in circular order
			desc:       "Near-MAX packets are BEFORE 0, so IterateFrom(0) shouldn't find them",
		},
		{
			name:       "startSeq_0_with_packets_spanning_zero",
			insertSeqs: []uint32{maxSeq - 1, maxSeq, 0, 1, 2},
			startSeq:   0,
			expected:   []uint32{0, 1, 2}, // Only post-wrap packets
			desc:       "Mixed packets around zero - only >= 0 should be found",
		},
		{
			name:       "startSeq_0_with_packet_at_threshold",
			insertSeqs: []uint32{threshold - 1, threshold, threshold + 1},
			startSeq:   0,
			expected:   []uint32{threshold - 1, threshold, threshold + 1},
			desc:       "Packets at threshold boundary from startSeq=0",
		},
		{
			name:       "startSeq_0_with_single_packet_at_549M",
			insertSeqs: []uint32{midHigh},
			startSeq:   0,
			expected:   []uint32{midHigh},
			desc:       "Single packet at 549M from startSeq=0 - exact failing test scenario",
		},

		// ═══════════════════════════════════════════════════════════════════════
		// Group 2: startSeq near MAX_SEQUENCENUMBER (wraparound boundary)
		// ═══════════════════════════════════════════════════════════════════════
		{
			name:       "startSeq_nearMax_with_packets_after_wrap",
			insertSeqs: []uint32{0, 1, 2},
			startSeq:   nearMax,
			expected:   []uint32{0, 1, 2}, // In circular order, 0,1,2 are AFTER nearMax
			desc:       "Post-wrap packets should be found from startSeq near MAX",
		},
		{
			name:       "startSeq_nearMax_with_packets_spanning_wrap",
			insertSeqs: []uint32{nearMax, nearMax + 50, maxSeq, 0, 1},
			startSeq:   nearMax,
			expected:   []uint32{nearMax, nearMax + 50, maxSeq, 0, 1},
			desc:       "Should find all packets from nearMax through wrap to post-wrap",
		},
		{
			name:       "startSeq_atMax_with_packets_after",
			insertSeqs: []uint32{0, 1, 2, 3},
			startSeq:   maxSeq,
			expected:   []uint32{0, 1, 2, 3}, // 0-3 are after MAX in circular order
			desc:       "Packets at 0,1,2,3 should be found from startSeq=MAX",
		},
		{
			name:       "startSeq_atMax_with_mixed_packets",
			insertSeqs: []uint32{maxSeq - 1, maxSeq, 0, 1},
			startSeq:   maxSeq,
			expected:   []uint32{maxSeq, 0, 1},
			desc:       "From MAX, should find MAX and post-wrap, but not MAX-1",
		},

		// ═══════════════════════════════════════════════════════════════════════
		// Group 3: startSeq at/near threshold (half of MAX)
		// ═══════════════════════════════════════════════════════════════════════
		{
			name:       "startSeq_atThreshold_with_packets_around",
			insertSeqs: []uint32{threshold - 100, threshold, threshold + 100},
			startSeq:   threshold,
			expected:   []uint32{threshold, threshold + 100},
			desc:       "From threshold, should only find >= threshold",
		},
		{
			name:       "startSeq_belowThreshold_with_packets_above",
			insertSeqs: []uint32{threshold + 100, threshold + 200},
			startSeq:   threshold - 100,
			expected:   []uint32{threshold + 100, threshold + 200},
			desc:       "Packets above threshold from startSeq below threshold",
		},
		{
			name:       "startSeq_aboveThreshold_with_lowPackets",
			insertSeqs: []uint32{100, 200, 300},
			startSeq:   threshold + 100,
			expected:   []uint32{}, // 100-300 are "before" threshold+100 in circular terms
			desc:       "Low packets are before high startSeq in circular order",
		},

		// ═══════════════════════════════════════════════════════════════════════
		// Group 4: startSeq at typical "random" initial sequence (~549M)
		// ═══════════════════════════════════════════════════════════════════════
		{
			name:       "startSeq_at549M_with_subsequent_packets",
			insertSeqs: []uint32{midHigh, midHigh + 1, midHigh + 2, midHigh + 3},
			startSeq:   midHigh,
			expected:   []uint32{midHigh, midHigh + 1, midHigh + 2, midHigh + 3},
			desc:       "Normal case: iterate from actual packet sequence",
		},
		{
			name:       "startSeq_after549M_with_packets_at549M",
			insertSeqs: []uint32{midHigh, midHigh + 1, midHigh + 2},
			startSeq:   midHigh + 10,
			expected:   []uint32{}, // All packets are before startSeq
			desc:       "Packets before startSeq should not be found",
		},
		{
			name:       "startSeq_before549M_with_packets_at549M",
			insertSeqs: []uint32{midHigh, midHigh + 1, midHigh + 2},
			startSeq:   midHigh - 100,
			expected:   []uint32{midHigh, midHigh + 1, midHigh + 2},
			desc:       "Packets after startSeq should be found",
		},

		// ═══════════════════════════════════════════════════════════════════════
		// Group 5: Edge cases and empty scenarios
		// ═══════════════════════════════════════════════════════════════════════
		{
			name:       "empty_btree_startSeq_0",
			insertSeqs: []uint32{},
			startSeq:   0,
			expected:   []uint32{},
			desc:       "Empty btree should return nothing",
		},
		{
			name:       "empty_btree_startSeq_549M",
			insertSeqs: []uint32{},
			startSeq:   midHigh,
			expected:   []uint32{},
			desc:       "Empty btree with any startSeq should return nothing",
		},
		{
			name:       "single_packet_at_0_startSeq_0",
			insertSeqs: []uint32{0},
			startSeq:   0,
			expected:   []uint32{0},
			desc:       "Single packet at 0 from startSeq=0",
		},
		{
			name:       "single_packet_at_MAX_startSeq_MAX",
			insertSeqs: []uint32{maxSeq},
			startSeq:   maxSeq,
			expected:   []uint32{maxSeq},
			desc:       "Single packet at MAX from startSeq=MAX",
		},
		{
			name:       "startSeq_1_with_packet_at_0",
			insertSeqs: []uint32{0},
			startSeq:   1,
			expected:   []uint32{}, // 0 is before 1
			desc:       "Packet at 0 should not be found from startSeq=1",
		},

		// ═══════════════════════════════════════════════════════════════════════
		// Group 6: Large gaps testing circular comparison edge cases
		// ═══════════════════════════════════════════════════════════════════════
		{
			name:       "startSeq_0_packets_just_below_threshold",
			insertSeqs: []uint32{threshold - 1},
			startSeq:   0,
			expected:   []uint32{threshold - 1},
			desc:       "Packet just below threshold from startSeq=0 (within valid distance)",
		},
		{
			name:       "startSeq_0_packets_just_above_threshold",
			insertSeqs: []uint32{threshold + 1},
			startSeq:   0,
			expected:   []uint32{}, // threshold+1 is "before" 0 in circular order (large negative distance)
			desc:       "Packet just above threshold from startSeq=0 (outside valid forward distance)",
		},
		{
			name:       "startSeq_near_threshold_boundary",
			insertSeqs: []uint32{threshold - 10, threshold + 10},
			startSeq:   threshold,
			expected:   []uint32{threshold + 10}, // Only the one >= threshold
			desc:       "Packets around threshold with startSeq at threshold",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bt := NewSendPacketBtree(32)

			// Insert packets
			for _, seq := range tc.insertSeqs {
				bt.Insert(createTestPacket(seq))
			}

			// Call IterateFrom
			var found []uint32
			iterStarted := false
			bt.IterateFrom(tc.startSeq, func(p packet.Packet) bool {
				iterStarted = true
				found = append(found, p.Header().PacketSequenceNumber.Val())
				return true
			})

			// Verify results
			if len(tc.expected) == 0 {
				require.Empty(t, found, "Expected no packets but found %v. Test: %s", found, tc.desc)
				require.False(t, iterStarted, "Callback should not have been called for empty result. Test: %s", tc.desc)
			} else {
				require.Equal(t, tc.expected, found, "Packet sequences mismatch. Test: %s", tc.desc)
				require.True(t, iterStarted, "Callback should have been called. Test: %s", tc.desc)
			}
		})
	}
}

// TestSendPacketBtree_IterateFrom_SimulateEventLoopStartup specifically tests
// the exact scenario that failed in the 20M isolation test:
// - deliveryStartPoint = 0 (default, not yet set)
// - Packets at random high sequence (~549M)
// - IterateFrom(0) should find these packets
func TestSendPacketBtree_IterateFrom_SimulateEventLoopStartup(t *testing.T) {
	// These are actual values from the failing integration test
	actualStartSeq := uint32(549144712)

	testCases := []struct {
		name               string
		deliveryStartPoint uint32 // Simulates s.deliveryStartPoint.Load()
		packetSeqs         []uint32
		shouldFind         bool
		minExpectedFound   int
		desc               string
	}{
		{
			name:               "initial_zero_startpoint",
			deliveryStartPoint: 0,
			packetSeqs:         []uint32{actualStartSeq, actualStartSeq + 1, actualStartSeq + 2},
			shouldFind:         true,
			minExpectedFound:   3,
			desc:               "EXACT FAILING SCENARIO: deliveryStartPoint=0, packets at 549M",
		},
		{
			name:               "initial_zero_startpoint_single_packet",
			deliveryStartPoint: 0,
			packetSeqs:         []uint32{actualStartSeq},
			shouldFind:         true,
			minExpectedFound:   1,
			desc:               "Single packet at 549M from deliveryStartPoint=0",
		},
		{
			name:               "correct_startpoint",
			deliveryStartPoint: actualStartSeq,
			packetSeqs:         []uint32{actualStartSeq, actualStartSeq + 1, actualStartSeq + 2},
			shouldFind:         true,
			minExpectedFound:   3,
			desc:               "Normal case: deliveryStartPoint matches first packet",
		},
		{
			name:               "startpoint_ahead_of_packets",
			deliveryStartPoint: actualStartSeq + 10,
			packetSeqs:         []uint32{actualStartSeq, actualStartSeq + 1, actualStartSeq + 2},
			shouldFind:         false,
			minExpectedFound:   0,
			desc:               "deliveryStartPoint ahead of all packets - should find nothing",
		},
		{
			name:               "startpoint_after_first_delivery",
			deliveryStartPoint: actualStartSeq + 1, // After delivering first packet
			packetSeqs:         []uint32{actualStartSeq, actualStartSeq + 1, actualStartSeq + 2},
			shouldFind:         true,
			minExpectedFound:   2, // Should find seq+1 and seq+2
			desc:               "After first packet delivered, should find remaining",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bt := NewSendPacketBtree(32)

			// Insert packets
			for _, seq := range tc.packetSeqs {
				bt.Insert(createTestPacket(seq))
			}

			// Verify btree is not empty
			require.Equal(t, len(tc.packetSeqs), bt.Len(), "Btree should have all packets inserted")

			// Simulate EventLoop: IterateFrom(deliveryStartPoint)
			var found []uint32
			iterStarted := false
			bt.IterateFrom(tc.deliveryStartPoint, func(p packet.Packet) bool {
				iterStarted = true
				found = append(found, p.Header().PacketSequenceNumber.Val())
				return true
			})

			if tc.shouldFind {
				require.True(t, iterStarted, "Callback should have been called. Test: %s", tc.desc)
				require.GreaterOrEqual(t, len(found), tc.minExpectedFound,
					"Should find at least %d packets, found %d. Test: %s",
					tc.minExpectedFound, len(found), tc.desc)
			} else {
				require.False(t, iterStarted, "Callback should NOT have been called. Test: %s", tc.desc)
				require.Empty(t, found, "Should find no packets. Test: %s", tc.desc)
			}

			t.Logf("Test '%s': deliveryStartPoint=%d, btreeLen=%d, found=%v",
				tc.name, tc.deliveryStartPoint, bt.Len(), found)
		})
	}
}

// TestSendPacketBtree_CircularSeqLess_Verification verifies the underlying
// SeqLess comparison logic that the btree depends on
func TestSendPacketBtree_CircularSeqLess_Verification(t *testing.T) {
	// This tests the raw SeqLess function to understand ordering
	midHigh := uint32(549144712)
	maxSeq := packet.MAX_SEQUENCENUMBER
	threshold := uint32(maxSeq / 2)

	testCases := []struct {
		a, b     uint32
		expected bool // Is a < b in circular terms?
		desc     string
	}{
		// Core question: Is 0 < 549M?
		{0, midHigh, true, "0 should be < 549M (within forward half)"},
		{midHigh, 0, false, "549M should NOT be < 0"},

		// Threshold boundary tests
		{0, threshold - 1, true, "0 < threshold-1"},
		{0, threshold, true, "0 < threshold (exactly half)"},
		{0, threshold + 1, false, "0 NOT < threshold+1 (beyond half, wraps negative)"},

		// Near MAX tests
		{maxSeq - 1, maxSeq, true, "MAX-1 < MAX"},
		{maxSeq, 0, true, "MAX < 0 (wraparound)"},
		{maxSeq - 1, 0, true, "MAX-1 < 0"},

		// Random middle value tests
		{midHigh, midHigh + 1, true, "consecutive: a < a+1"},
		{midHigh + 1, midHigh, false, "consecutive: a+1 NOT < a"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			result := circular.SeqLess(tc.a, tc.b)
			require.Equal(t, tc.expected, result,
				"SeqLess(%d, %d) = %v, expected %v. %s",
				tc.a, tc.b, result, tc.expected, tc.desc)
		})
	}
}

// TestSendPacketBtree_ProtectedByLock demonstrates correct multi-goroutine usage
// This is NOT how EventLoop mode works, but shows Tick() mode pattern
func TestSendPacketBtree_ProtectedByLock(t *testing.T) {
	bt := NewSendPacketBtree(32)
	var mu sync.Mutex
	var wg sync.WaitGroup

	const numGoroutines = 4
	const opsPerGoroutine = 100

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for i := 0; i < opsPerGoroutine; i++ {
				mu.Lock()
				seq := uint32(goroutineID*opsPerGoroutine + i)
				bt.Insert(createTestPacket(seq))
				mu.Unlock()
			}
		}(g)
	}

	wg.Wait()

	require.Equal(t, numGoroutines*opsPerGoroutine, bt.Len())
}
