//go:build go1.18

package common

import (
	"sync"
	"testing"
)

// TestPacket is a simple test packet type for unit tests
type TestPacket struct {
	Type uint8
	Seq  uint32
	Data int64
}

// ═══════════════════════════════════════════════════════════════════════════════
// Basic Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestControlRing_NewControlRing(t *testing.T) {
	tests := []struct {
		name           string
		size           int
		shards         int
		expectedShards int
	}{
		{"default_values", 0, 0, 1},
		{"explicit_128_1", 128, 1, 1},
		{"explicit_256_2", 256, 2, 2},
		{"small_ring", 16, 1, 1},
		{"negative_size", -1, 1, 1},
		{"negative_shards", 128, -1, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](tc.size, tc.shards)
			if err != nil {
				t.Fatalf("NewControlRing failed: %v", err)
			}
			if ring.Shards() != tc.expectedShards {
				t.Errorf("Shards() = %d, want %d", ring.Shards(), tc.expectedShards)
			}
			if ring.Len() != 0 {
				t.Errorf("Len() = %d, want 0 for new ring", ring.Len())
			}
		})
	}
}

func TestControlRing_PushPop_Single(t *testing.T) {
	ring, err := NewControlRing[TestPacket](128, 1)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	// Push single packet
	pkt := TestPacket{Type: 1, Seq: 100, Data: 12345}
	if !ring.Push(0, pkt) {
		t.Fatal("Push failed")
	}

	if ring.Len() != 1 {
		t.Errorf("Len() = %d, want 1", ring.Len())
	}

	// Pop single packet
	got, ok := ring.TryPop()
	if !ok {
		t.Fatal("TryPop failed")
	}
	if got.Type != pkt.Type || got.Seq != pkt.Seq || got.Data != pkt.Data {
		t.Errorf("got %+v, want %+v", got, pkt)
	}

	// Should be empty now
	_, ok = ring.TryPop()
	if ok {
		t.Error("TryPop should return false on empty ring")
	}
}

func TestControlRing_PushPop_Multiple(t *testing.T) {
	ring, err := NewControlRing[TestPacket](128, 1)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	// Push multiple packets
	count := 50
	for i := 0; i < count; i++ {
		pkt := TestPacket{Type: uint8(i % 2), Seq: uint32(i), Data: int64(i * 100)}
		if !ring.Push(0, pkt) {
			t.Fatalf("Push %d failed", i)
		}
	}

	if ring.Len() != count {
		t.Errorf("Len() = %d, want %d", ring.Len(), count)
	}

	// Pop all and verify order (FIFO)
	for i := 0; i < count; i++ {
		got, ok := ring.TryPop()
		if !ok {
			t.Fatalf("TryPop %d failed", i)
		}
		if got.Seq != uint32(i) {
			t.Errorf("packet %d: Seq = %d, want %d", i, got.Seq, i)
		}
	}

	// Should be empty
	if ring.Len() != 0 {
		t.Errorf("Len() = %d, want 0 after draining", ring.Len())
	}
}

func TestControlRing_Full(t *testing.T) {
	// Small ring to test full behavior
	ring, err := NewControlRing[TestPacket](4, 1)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	// Fill ring
	for i := 0; i < 4; i++ {
		if !ring.Push(0, TestPacket{Seq: uint32(i)}) {
			t.Fatalf("Push %d failed before ring full", i)
		}
	}

	// Next push should fail (ring full)
	if ring.Push(0, TestPacket{Seq: 999}) {
		t.Error("Push should fail when ring is full")
	}

	// Pop one to make room
	_, ok := ring.TryPop()
	if !ok {
		t.Fatal("TryPop should succeed")
	}

	// Now push should succeed
	if !ring.Push(0, TestPacket{Seq: 999}) {
		t.Error("Push should succeed after making room")
	}
}

func TestControlRing_Empty(t *testing.T) {
	ring, err := NewControlRing[TestPacket](128, 1)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	// Empty ring should return false
	_, ok := ring.TryPop()
	if ok {
		t.Error("TryPop should return false on empty ring")
	}

	// Multiple pops on empty should all return false
	for i := 0; i < 10; i++ {
		_, popOk := ring.TryPop()
		if popOk {
			t.Errorf("TryPop %d should return false on empty ring", i)
		}
	}
}

func TestControlRing_Len(t *testing.T) {
	ring, err := NewControlRing[TestPacket](128, 1)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	if ring.Len() != 0 {
		t.Errorf("Len() = %d, want 0", ring.Len())
	}

	// Add items and check len
	for i := 1; i <= 10; i++ {
		ring.Push(0, TestPacket{Seq: uint32(i)})
		if ring.Len() != i {
			t.Errorf("after %d pushes: Len() = %d, want %d", i, ring.Len(), i)
		}
	}

	// Remove items and check len
	for i := 9; i >= 0; i-- {
		ring.TryPop()
		if ring.Len() != i {
			t.Errorf("after pop: Len() = %d, want %d", ring.Len(), i)
		}
	}
}

func TestControlRing_Cap(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		shards      int
		expectedCap int
	}{
		{"128x1", 128, 1, 128},
		{"64x2", 64, 2, 128},
		{"256x1", 256, 1, 256},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewControlRing[TestPacket](tc.size, tc.shards)
			if err != nil {
				t.Fatalf("NewControlRing failed: %v", err)
			}
			// Note: actual capacity may be rounded up to power of 2 by underlying ring
			if ring.Cap() < tc.expectedCap {
				t.Errorf("Cap() = %d, want >= %d", ring.Cap(), tc.expectedCap)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Multi-Shard Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestControlRing_MultiShard(t *testing.T) {
	ring, err := NewControlRing[TestPacket](64, 2)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	if ring.Shards() != 2 {
		t.Errorf("Shards() = %d, want 2", ring.Shards())
	}

	// Push to different shards
	ring.Push(0, TestPacket{Type: 0, Seq: 100})
	ring.Push(1, TestPacket{Type: 1, Seq: 200})
	ring.Push(0, TestPacket{Type: 0, Seq: 101})
	ring.Push(1, TestPacket{Type: 1, Seq: 201})

	if ring.Len() != 4 {
		t.Errorf("Len() = %d, want 4", ring.Len())
	}

	// Pop all - order depends on ring implementation
	count := 0
	for {
		_, ok := ring.TryPop()
		if !ok {
			break
		}
		count++
	}
	if count != 4 {
		t.Errorf("popped %d packets, want 4", count)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestControlRing_ConcurrentPush(t *testing.T) {
	ring, err := NewControlRing[TestPacket](65536, 1)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	const numGoroutines = 4
	const pushesPerGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < pushesPerGoroutine; i++ {
				pkt := TestPacket{
					Type: uint8(goroutineID),
					Seq:  uint32(i),
					Data: int64(goroutineID*pushesPerGoroutine + i),
				}
				ring.Push(uint64(goroutineID), pkt)
			}
		}(g)
	}

	wg.Wait()

	// Verify all packets were pushed (or ring was full)
	totalPushed := ring.Len()
	t.Logf("Total pushed: %d (expected up to %d)", totalPushed, numGoroutines*pushesPerGoroutine)

	// Drain and count
	count := 0
	for {
		_, ok := ring.TryPop()
		if !ok {
			break
		}
		count++
	}
	if count != totalPushed {
		t.Errorf("drained %d, but Len() was %d", count, totalPushed)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Edge Cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestControlRing_ZeroValuePacket(t *testing.T) {
	ring, err := NewControlRing[TestPacket](128, 1)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	// Push zero-value packet
	var zeroPkt TestPacket
	if !ring.Push(0, zeroPkt) {
		t.Fatal("Push zero-value packet failed")
	}

	got, ok := ring.TryPop()
	if !ok {
		t.Fatal("TryPop failed")
	}
	if got != zeroPkt {
		t.Errorf("got %+v, want zero value", got)
	}
}

func TestControlRing_LargePacket(t *testing.T) {
	// Test with a larger packet type
	type LargePacket struct {
		Data [256]byte
		Seq  uint64
	}

	ring, err := NewControlRing[LargePacket](64, 1)
	if err != nil {
		t.Fatalf("NewControlRing failed: %v", err)
	}

	pkt := LargePacket{Seq: 12345}
	for i := range pkt.Data {
		pkt.Data[i] = byte(i)
	}

	if !ring.Push(0, pkt) {
		t.Fatal("Push large packet failed")
	}

	got, ok := ring.TryPop()
	if !ok {
		t.Fatal("TryPop failed")
	}
	if got.Seq != pkt.Seq {
		t.Errorf("Seq = %d, want %d", got.Seq, pkt.Seq)
	}
	if got.Data != pkt.Data {
		t.Error("Data mismatch")
	}
}
