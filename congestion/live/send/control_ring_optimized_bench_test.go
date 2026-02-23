//go:build go1.18

package send

import (
	"testing"

	ring "github.com/randomizedcoder/go-lock-free-ring"
	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmarks comparing different control ring optimization strategies
//
// Analysis: Current ControlPacket is 144 bytes because of [32]uint32 NAK array.
// ACKs only need 4 bytes (sequence number). Since ACKs >> NAKs in frequency,
// optimizing ACK size could significantly reduce allocations.
// ═══════════════════════════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════════════════════════
// Option A: Separate ACK ring storing just uint32
// ═══════════════════════════════════════════════════════════════════════════════

type ACKOnlyRing struct {
	ring *ring.ShardedRing
}

func NewACKOnlyRing(size, shards int) (*ACKOnlyRing, error) {
	if shards < 1 {
		shards = 1
	}
	r, err := ring.NewShardedRing(uint64(size*shards), uint64(shards))
	if err != nil {
		return nil, err
	}
	return &ACKOnlyRing{ring: r}, nil
}

func (r *ACKOnlyRing) PushACK(seq uint32) bool {
	return r.ring.Write(uint64(seq)%4, seq) // Use seq % 4 for shard selection
}

func (r *ACKOnlyRing) TryPopACK() (uint32, bool) {
	item, ok := r.ring.TryRead()
	if !ok {
		return 0, false
	}
	return item.(uint32), true
}

// ═══════════════════════════════════════════════════════════════════════════════
// Option B: Smaller ControlPacket without inline NAK array
// ═══════════════════════════════════════════════════════════════════════════════

type SmallControlPacket struct {
	Type        uint8
	ACKSequence uint32
	NAKCount    uint8       // max 255 NAKs per packet
	NAKSeqs     *[32]uint32 // pointer to pooled array (nil for ACK)
}

// Size: 1 + 3 padding + 4 + 1 + 7 padding + 8 = 24 bytes (vs 144 bytes)

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRing_CurrentDesign_PushACK(b *testing.B) {
	r, err := NewSendControlRing(8192, 2)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	seq := circular.New(100, packet.MAX_SEQUENCENUMBER)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if !r.PushACK(seq) {
			b.StopTimer()
			for { // drain
				if _, ok := r.TryPop(); !ok {
					break
				}
			}
			b.StartTimer()
			r.PushACK(seq)
		}
	}
}

func BenchmarkControlRing_ACKOnly_PushACK(b *testing.B) {
	r, err := NewACKOnlyRing(8192, 2)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	seq := uint32(100)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if !r.PushACK(seq) {
			b.StopTimer()
			for { // drain
				if _, ok := r.TryPopACK(); !ok {
					break
				}
			}
			b.StartTimer()
			r.PushACK(seq)
		}
	}
}

// Compare memory allocation of direct uint32 vs ControlPacket
func BenchmarkControlRing_Interface_Uint32(b *testing.B) {
	r, err := ring.NewShardedRing(8192, 2)
	if err != nil {
		b.Fatalf("failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !r.Write(0, uint32(i)) {
			b.StopTimer()
			for {
				if _, ok := r.TryRead(); !ok {
					break
				}
			}
			b.StartTimer()
		}
	}
}

func BenchmarkControlRing_Interface_ControlPacket(b *testing.B) {
	r, err := ring.NewShardedRing(8192, 2)
	if err != nil {
		b.Fatalf("failed: %v", err)
	}

	cp := ControlPacket{Type: ControlTypeACK, ACKSequence: 100}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if !r.Write(0, cp) {
			b.StopTimer()
			for {
				if _, ok := r.TryRead(); !ok {
					break
				}
			}
			b.StartTimer()
		}
	}
}

// Benchmark raw allocation cost
func BenchmarkAllocation_ControlPacket(b *testing.B) {
	var sink ControlPacket
	for i := 0; i < b.N; i++ {
		cp := ControlPacket{
			Type:        ControlTypeACK,
			ACKSequence: uint32(i),
		}
		sink = cp
	}
	_ = sink
}

func BenchmarkAllocation_Uint32(b *testing.B) {
	var sink uint32
	for i := 0; i < b.N; i++ {
		sink = uint32(i)
	}
	_ = sink
}

// ═══════════════════════════════════════════════════════════════════════════════
// V1 vs V2 Comparison Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkControlRingV1_PushACK(b *testing.B) {
	r, err := NewSendControlRing(8192, 2)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	seq := circular.New(100, packet.MAX_SEQUENCENUMBER)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if !r.PushACK(seq) {
			b.StopTimer()
			for {
				if _, ok := r.TryPop(); !ok {
					break
				}
			}
			b.StartTimer()
			r.PushACK(seq)
		}
	}
}

func BenchmarkControlRingV2_PushACK(b *testing.B) {
	r, err := NewSendControlRingV2(8192, 256, 2)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if !r.PushACK(uint32(100)) {
			b.StopTimer()
			for {
				if _, ok := r.TryPopACK(); !ok {
					break
				}
			}
			b.StartTimer()
			r.PushACK(uint32(100))
		}
	}
}

func BenchmarkControlRingV2_PushACKCircular(b *testing.B) {
	r, err := NewSendControlRingV2(8192, 256, 2)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	seq := circular.New(100, packet.MAX_SEQUENCENUMBER)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if !r.PushACKCircular(seq) {
			b.StopTimer()
			for {
				if _, ok := r.TryPopACK(); !ok {
					break
				}
			}
			b.StartTimer()
			r.PushACKCircular(seq)
		}
	}
}

func BenchmarkControlRingV1_PushNAK_Small(b *testing.B) {
	r, err := NewSendControlRing(8192, 2)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	seqs := []circular.Number{
		circular.New(100, packet.MAX_SEQUENCENUMBER),
		circular.New(101, packet.MAX_SEQUENCENUMBER),
	}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if !r.PushNAK(seqs) {
			b.StopTimer()
			for {
				if _, ok := r.TryPop(); !ok {
					break
				}
			}
			b.StartTimer()
			r.PushNAK(seqs)
		}
	}
}

func BenchmarkControlRingV2_PushNAK_Small(b *testing.B) {
	r, err := NewSendControlRingV2(256, 8192, 2)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	seqs := []circular.Number{
		circular.New(100, packet.MAX_SEQUENCENUMBER),
		circular.New(101, packet.MAX_SEQUENCENUMBER),
	}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if !r.PushNAK(seqs) {
			b.StopTimer()
			for {
				if _, ok := r.TryPopNAK(); !ok {
					break
				}
			}
			b.StartTimer()
			r.PushNAK(seqs)
		}
	}
}

func BenchmarkControlRingV1_TryPop(b *testing.B) {
	r, err := NewSendControlRing(65536, 4)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	// Pre-fill
	for i := 0; i < 60000; i++ {
		r.PushACK(circular.New(uint32(i), packet.MAX_SEQUENCENUMBER))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, ok := r.TryPop()
		if !ok {
			b.StopTimer()
			for j := 0; j < 60000; j++ {
				r.PushACK(circular.New(uint32(j), packet.MAX_SEQUENCENUMBER))
			}
			b.StartTimer()
		}
	}
}

func BenchmarkControlRingV2_TryPopACK(b *testing.B) {
	r, err := NewSendControlRingV2(65536, 256, 4)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	// Pre-fill
	for i := 0; i < 60000; i++ {
		r.PushACK(uint32(i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, ok := r.TryPopACK()
		if !ok {
			b.StopTimer()
			for j := 0; j < 60000; j++ {
				r.PushACK(uint32(j))
			}
			b.StartTimer()
		}
	}
}

// Concurrent ACK push comparison
func BenchmarkControlRingV1_PushACK_Concurrent(b *testing.B) {
	r, err := NewSendControlRing(65536, 4)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			r.PushACK(circular.New(seq, packet.MAX_SEQUENCENUMBER))
			seq++
		}
	})
}

func BenchmarkControlRingV2_PushACK_Concurrent(b *testing.B) {
	r, err := NewSendControlRingV2(65536, 256, 4)
	if err != nil {
		b.Fatalf("failed to create ring: %v", err)
	}

	b.RunParallel(func(pb *testing.PB) {
		seq := uint32(0)
		for pb.Next() {
			r.PushACK(seq)
			seq++
		}
	})
}
