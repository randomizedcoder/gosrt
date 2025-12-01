package live

import (
	"net"
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/congestion"
	"github.com/datarhei/gosrt/packet"
)

// Helper to create a receiver with specified algorithm
func createBenchReceiver(algorithm string, degree int) congestion.Receiver {
	return NewReceiver(ReceiveConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10,
		PeriodicNAKInterval:     20,
		PacketReorderAlgorithm: algorithm,
		BTreeDegree:            degree,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
	})
}

// BenchmarkPushInOrder_List benchmarks in-order packet insertion with list
func BenchmarkPushInOrder_List(b *testing.B) {
	recv := createBenchReceiver("list", 0)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}
}

// BenchmarkPushInOrder_BTree benchmarks in-order packet insertion with btree
func BenchmarkPushInOrder_BTree(b *testing.B) {
	recv := createBenchReceiver("btree", 32)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}
}

// BenchmarkPushOutOfOrder_List benchmarks out-of-order packet insertion with list
func BenchmarkPushOutOfOrder_List(b *testing.B) {
	recv := createBenchReceiver("list", 0)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-populate with some packets
	for i := 0; i < 1000; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Insert packets that need to be sorted in the middle
		seq := uint32(500 + (i % 100))
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(seq + 1)
		recv.Push(p)
	}
}

// BenchmarkPushOutOfOrder_BTree benchmarks out-of-order packet insertion with btree
func BenchmarkPushOutOfOrder_BTree(b *testing.B) {
	recv := createBenchReceiver("btree", 32)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-populate with some packets
	for i := 0; i < 1000; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Insert packets that need to be sorted in the middle
		seq := uint32(500 + (i % 100))
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(seq + 1)
		recv.Push(p)
	}
}

// BenchmarkHas_List benchmarks Has() operation with list
func BenchmarkHas_List(b *testing.B) {
	recv := createBenchReceiver("list", 0)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-populate with packets
	for i := 0; i < 1000; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	r := recv.(*receiver)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq := circular.New(uint32(i%1000), packet.MAX_SEQUENCENUMBER)
		r.packetStore.Has(seq)
	}
}

// BenchmarkHas_BTree benchmarks Has() operation with btree
func BenchmarkHas_BTree(b *testing.B) {
	recv := createBenchReceiver("btree", 32)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-populate with packets
	for i := 0; i < 1000; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	r := recv.(*receiver)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq := circular.New(uint32(i%1000), packet.MAX_SEQUENCENUMBER)
		r.packetStore.Has(seq)
	}
}

// BenchmarkIterate_List benchmarks iteration with list
func BenchmarkIterate_List(b *testing.B) {
	recv := createBenchReceiver("list", 0)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-populate with packets
	for i := 0; i < 1000; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	r := recv.(*receiver)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.packetStore.Iterate(func(p packet.Packet) bool {
			return true // Continue
		})
	}
}

// BenchmarkIterate_BTree benchmarks iteration with btree
func BenchmarkIterate_BTree(b *testing.B) {
	recv := createBenchReceiver("btree", 32)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-populate with packets
	for i := 0; i < 1000; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = uint64(i + 1)
		recv.Push(p)
	}

	r := recv.(*receiver)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.packetStore.Iterate(func(p packet.Packet) bool {
			return true // Continue
		})
	}
}

// BenchmarkRemoveAll_List benchmarks RemoveAll() operation with list
func BenchmarkRemoveAll_List(b *testing.B) {
	recv := createBenchReceiver("list", 0)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	r := recv.(*receiver)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Reset and populate
		r.packetStore.Clear()
		for j := 0; j < 100; j++ {
			p := packet.NewPacket(addr)
			p.Header().PacketSequenceNumber = circular.New(uint32(j), packet.MAX_SEQUENCENUMBER)
			p.Header().PktTsbpdTime = uint64(j + 1)
			recv.Push(p)
		}

		// Remove all
		r.packetStore.RemoveAll(
			func(p packet.Packet) bool {
				return p.Header().PacketSequenceNumber.Val() < 50
			},
			func(p packet.Packet) {
				// Deliver
			},
		)
	}
}

// BenchmarkRemoveAll_BTree benchmarks RemoveAll() operation with btree
func BenchmarkRemoveAll_BTree(b *testing.B) {
	recv := createBenchReceiver("btree", 32)
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	r := recv.(*receiver)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Reset and populate
		r.packetStore.Clear()
		for j := 0; j < 100; j++ {
			p := packet.NewPacket(addr)
			p.Header().PacketSequenceNumber = circular.New(uint32(j), packet.MAX_SEQUENCENUMBER)
			p.Header().PktTsbpdTime = uint64(j + 1)
			recv.Push(p)
		}

		// Remove all
		r.packetStore.RemoveAll(
			func(p packet.Packet) bool {
				return p.Header().PacketSequenceNumber.Val() < 50
			},
			func(p packet.Packet) {
				// Deliver
			},
		)
	}
}

