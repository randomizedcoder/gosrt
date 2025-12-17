//go:build go1.18

package live

import (
	"net"
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/packet"
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
	// Wraparound: MAX-1, MAX, 0, 1, 2

	t.Run("BTree wraparound", func(t *testing.T) {
		store := NewBTreePacketStore(32)

		// Insert sequences that span wraparound
		maxSeq := packet.MAX_SEQUENCENUMBER
		seqsToInsert := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		for _, seq := range seqsToInsert {
			store.Insert(mockPacket(seq))
		}

		// IterateFrom just before wrap should get packets in circular order
		var seqs []uint32
		store.IterateFrom(circular.New(maxSeq-1, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})

		// Due to circular comparison in btree, this should work correctly
		// Expected: maxSeq-1, maxSeq, then wraps to 0, 1, 2
		require.GreaterOrEqual(t, len(seqs), 2, "Should get at least maxSeq-1 and maxSeq")
		require.Equal(t, maxSeq-1, seqs[0])
	})

	t.Run("List wraparound", func(t *testing.T) {
		store := NewListPacketStore()

		maxSeq := packet.MAX_SEQUENCENUMBER
		seqsToInsert := []uint32{maxSeq - 2, maxSeq - 1, maxSeq, 0, 1, 2}
		for _, seq := range seqsToInsert {
			store.Insert(mockPacket(seq))
		}

		var seqs []uint32
		store.IterateFrom(circular.New(maxSeq-1, packet.MAX_SEQUENCENUMBER), func(pkt packet.Packet) bool {
			seqs = append(seqs, pkt.Header().PacketSequenceNumber.Val())
			return true
		})

		require.GreaterOrEqual(t, len(seqs), 2, "Should get at least maxSeq-1 and maxSeq")
	})
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

