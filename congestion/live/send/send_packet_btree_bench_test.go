//go:build go1.18

package send

import (
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmarks for SendPacketBtree
// Reference: lockless_sender_implementation_plan.md Step 1.14
//
// Performance targets (from receiver btree benchmarks):
// - Insert: ≤ 700 ns/op
// - Get: ≤ 400 ns/op
// - Delete: ≤ 400 ns/op
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkSendPacketBtree_Insert(b *testing.B) {
	bt := NewSendPacketBtree(32)
	pkts := make([]packet.Packet, b.N)
	for i := range pkts {
		pkts[i] = createBenchPacket(uint32(i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.Insert(pkts[i])
	}
}

func BenchmarkSendPacketBtree_Insert_Duplicate(b *testing.B) {
	bt := NewSendPacketBtree(32)

	// Pre-populate with packets
	for i := 0; i < 1000; i++ {
		bt.Insert(createBenchPacket(uint32(i)))
	}

	// Benchmark duplicate insertion
	p := createBenchPacket(500) // Duplicate
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.Insert(p)
	}
}

func BenchmarkSendPacketBtree_Get_Found(b *testing.B) {
	bt := NewSendPacketBtree(32)

	// Pre-populate
	const size = 1000
	for i := 0; i < size; i++ {
		bt.Insert(createBenchPacket(uint32(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.Get(uint32(i % size))
	}
}

func BenchmarkSendPacketBtree_Get_NotFound(b *testing.B) {
	bt := NewSendPacketBtree(32)

	// Pre-populate with 0-999
	for i := 0; i < 1000; i++ {
		bt.Insert(createBenchPacket(uint32(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.Get(uint32(10000 + i)) // Not found
	}
}

func BenchmarkSendPacketBtree_Delete(b *testing.B) {
	bt := NewSendPacketBtree(32)

	// Pre-populate
	for i := 0; i < b.N+1000; i++ {
		bt.Insert(createBenchPacket(uint32(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.Delete(uint32(i))
	}
}

func BenchmarkSendPacketBtree_DeleteMin(b *testing.B) {
	bt := NewSendPacketBtree(32)

	// Pre-populate
	for i := 0; i < b.N; i++ {
		bt.Insert(createBenchPacket(uint32(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.DeleteMin()
	}
}

func BenchmarkSendPacketBtree_DeleteBefore_10(b *testing.B) {
	// Create one btree, continuously add and remove
	bt := NewSendPacketBtree(32)
	baseSeq := uint32(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Add 10 packets
		for j := 0; j < 10; j++ {
			bt.Insert(createBenchPacket(baseSeq + uint32(j)))
		}
		// Delete them (allocates slice)
		bt.DeleteBefore(baseSeq + 10)
		baseSeq += 10
	}
}

// BenchmarkSendPacketBtree_DeleteBeforeFunc_10 benchmarks zero-allocation deletion.
// This is the preferred method for ACK processing hot path.
func BenchmarkSendPacketBtree_DeleteBeforeFunc_10(b *testing.B) {
	bt := NewSendPacketBtree(32)
	baseSeq := uint32(0)

	// Simulate real-world callback (inline decommission)
	processCount := 0
	processFn := func(p packet.Packet) {
		processCount++ // Minimal work to prevent optimization
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Add 10 packets
		for j := 0; j < 10; j++ {
			bt.Insert(createBenchPacket(baseSeq + uint32(j)))
		}
		// Delete with zero-allocation callback
		bt.DeleteBeforeFunc(baseSeq+10, processFn)
		baseSeq += 10
	}
}

// BenchmarkSendPacketBtree_DeleteBeforeFunc_100 tests larger batch deletion.
func BenchmarkSendPacketBtree_DeleteBeforeFunc_100(b *testing.B) {
	bt := NewSendPacketBtree(32)
	baseSeq := uint32(0)

	processCount := 0
	processFn := func(p packet.Packet) {
		processCount++
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Add 100 packets
		for j := 0; j < 100; j++ {
			bt.Insert(createBenchPacket(baseSeq + uint32(j)))
		}
		// Delete with zero-allocation callback
		bt.DeleteBeforeFunc(baseSeq+100, processFn)
		baseSeq += 100
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Isolated Deletion Benchmarks (pre-populated btree, no insert in hot path)
// These show the true difference between slice vs callback approach
// ═══════════════════════════════════════════════════════════════════════════════

// BenchmarkSendPacketBtree_DeleteBefore_Isolated measures ONLY delete time
func BenchmarkSendPacketBtree_DeleteBefore_Isolated(b *testing.B) {
	// Pre-create all packets once
	const batchSize = 10
	packets := make([]packet.Packet, b.N*batchSize)
	for i := range packets {
		packets[i] = createBenchPacket(uint32(i))
	}

	bt := NewSendPacketBtree(32)
	pktIdx := 0

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Pre-insert (stop timer during setup)
		b.StopTimer()
		for j := 0; j < batchSize; j++ {
			bt.Insert(packets[pktIdx+j])
		}
		b.StartTimer()

		// ONLY measure deletion
		bt.DeleteBefore(uint32(pktIdx + batchSize))
		pktIdx += batchSize
	}
}

// BenchmarkSendPacketBtree_DeleteBeforeFunc_Isolated measures ONLY delete time with callback
func BenchmarkSendPacketBtree_DeleteBeforeFunc_Isolated(b *testing.B) {
	// Pre-create all packets once
	const batchSize = 10
	packets := make([]packet.Packet, b.N*batchSize)
	for i := range packets {
		packets[i] = createBenchPacket(uint32(i))
	}

	bt := NewSendPacketBtree(32)
	pktIdx := 0
	count := 0
	fn := func(p packet.Packet) { count++ }

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Pre-insert (stop timer during setup)
		b.StopTimer()
		for j := 0; j < batchSize; j++ {
			bt.Insert(packets[pktIdx+j])
		}
		b.StartTimer()

		// ONLY measure deletion with zero-alloc callback
		bt.DeleteBeforeFunc(uint32(pktIdx+batchSize), fn)
		pktIdx += batchSize
	}
}

func BenchmarkSendPacketBtree_IterateFrom(b *testing.B) {
	bt := NewSendPacketBtree(32)

	// Pre-populate
	for i := 0; i < 1000; i++ {
		bt.Insert(createBenchPacket(uint32(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		count := 0
		bt.IterateFrom(500, func(p packet.Packet) bool {
			count++
			return count < 100 // Iterate 100 packets
		})
	}
}

func BenchmarkSendPacketBtree_Has(b *testing.B) {
	bt := NewSendPacketBtree(32)

	// Pre-populate
	for i := 0; i < 1000; i++ {
		bt.Insert(createBenchPacket(uint32(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bt.Has(uint32(i % 1000))
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Comparison benchmarks: Btree vs List NAK lookup
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkNAKLookup_Btree_Small(b *testing.B) {
	benchmarkNAKLookupBtree(b, 100)
}

func BenchmarkNAKLookup_Btree_Medium(b *testing.B) {
	benchmarkNAKLookupBtree(b, 1000)
}

func BenchmarkNAKLookup_Btree_Large(b *testing.B) {
	benchmarkNAKLookupBtree(b, 10000)
}

func benchmarkNAKLookupBtree(b *testing.B, size int) {
	bt := NewSendPacketBtree(32)
	for i := 0; i < size; i++ {
		bt.Insert(createBenchPacket(uint32(i)))
	}

	// Simulate NAK for 10 random sequences
	nakSeqs := []uint32{uint32(size / 4), uint32(size / 2), uint32(size * 3 / 4)}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, seq := range nakSeqs {
			bt.Get(seq)
		}
	}
}

// Helper function
func createBenchPacket(seq uint32) packet.Packet {
	p := packet.NewPacket(mockAddr{})
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	return p
}
