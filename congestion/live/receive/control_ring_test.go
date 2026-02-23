//go:build go1.18

package receive

import (
	"sync"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Basic Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecvControlRing_NewRecvControlRing(t *testing.T) {
	tests := []struct {
		name           string
		size           int
		shards         int
		expectedShards int
	}{
		{"default", 128, 1, 1},
		{"small", 16, 1, 1},
		{"multi_shard", 64, 2, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewRecvControlRing(tc.size, tc.shards)
			if err != nil {
				t.Fatalf("NewRecvControlRing failed: %v", err)
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

func TestRecvControlRing_PushACKACK(t *testing.T) {
	ring, err := NewRecvControlRing(128, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	now := time.Now()
	ackNum := uint32(42)

	// Push ACKACK
	if !ring.PushACKACK(ackNum, now) {
		t.Fatal("PushACKACK failed")
	}

	if ring.Len() != 1 {
		t.Errorf("Len() = %d, want 1", ring.Len())
	}

	// Pop and verify
	pkt, ok := ring.TryPop()
	if !ok {
		t.Fatal("TryPop failed")
	}

	if pkt.Type != RecvControlTypeACKACK {
		t.Errorf("Type = %d (%s), want ACKACK", pkt.Type, pkt.Type.String())
	}
	if pkt.ACKNumber != ackNum {
		t.Errorf("ACKNumber = %d, want %d", pkt.ACKNumber, ackNum)
	}
	if pkt.Timestamp != now.UnixNano() {
		t.Errorf("Timestamp = %d, want %d", pkt.Timestamp, now.UnixNano())
	}

	// Verify ArrivalTime() helper
	arrivalTime := pkt.ArrivalTime()
	if arrivalTime.UnixNano() != now.UnixNano() {
		t.Errorf("ArrivalTime() = %v, want %v", arrivalTime, now)
	}
}

func TestRecvControlRing_PushKEEPALIVE(t *testing.T) {
	ring, err := NewRecvControlRing(128, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	// Push KEEPALIVE
	if !ring.PushKEEPALIVE() {
		t.Fatal("PushKEEPALIVE failed")
	}

	if ring.Len() != 1 {
		t.Errorf("Len() = %d, want 1", ring.Len())
	}

	// Pop and verify
	pkt, ok := ring.TryPop()
	if !ok {
		t.Fatal("TryPop failed")
	}

	if pkt.Type != RecvControlTypeKEEPALIVE {
		t.Errorf("Type = %d (%s), want KEEPALIVE", pkt.Type, pkt.Type.String())
	}
}

func TestRecvControlRing_Mixed(t *testing.T) {
	ring, err := NewRecvControlRing(128, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	now := time.Now()

	// Push mixed packet types
	ring.PushACKACK(1, now)
	ring.PushKEEPALIVE()
	ring.PushACKACK(2, now.Add(time.Millisecond))
	ring.PushKEEPALIVE()
	ring.PushACKACK(3, now.Add(2*time.Millisecond))

	if ring.Len() != 5 {
		t.Errorf("Len() = %d, want 5", ring.Len())
	}

	// Pop and verify order (FIFO)
	expected := []struct {
		typ    RecvControlPacketType
		ackNum uint32
	}{
		{RecvControlTypeACKACK, 1},
		{RecvControlTypeKEEPALIVE, 0},
		{RecvControlTypeACKACK, 2},
		{RecvControlTypeKEEPALIVE, 0},
		{RecvControlTypeACKACK, 3},
	}

	for i, exp := range expected {
		pkt, ok := ring.TryPop()
		if !ok {
			t.Fatalf("TryPop %d failed", i)
		}
		if pkt.Type != exp.typ {
			t.Errorf("packet %d: Type = %s, want %s", i, pkt.Type.String(), exp.typ.String())
		}
		if exp.typ == RecvControlTypeACKACK && pkt.ACKNumber != exp.ackNum {
			t.Errorf("packet %d: ACKNumber = %d, want %d", i, pkt.ACKNumber, exp.ackNum)
		}
	}

	// Should be empty
	_, ok := ring.TryPop()
	if ok {
		t.Error("TryPop should return false on empty ring")
	}
}

func TestRecvControlRing_Full(t *testing.T) {
	// Small ring to test full behavior
	ring, err := NewRecvControlRing(4, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	now := time.Now()

	// Fill ring
	for i := 0; i < 4; i++ {
		if !ring.PushACKACK(uint32(i), now) {
			t.Fatalf("Push %d failed before ring full", i)
		}
	}

	// Next push should fail (ring full)
	if ring.PushACKACK(999, now) {
		t.Error("PushACKACK should fail when ring is full")
	}
	if ring.PushKEEPALIVE() {
		t.Error("PushKEEPALIVE should fail when ring is full")
	}
}

func TestRecvControlRing_Empty(t *testing.T) {
	ring, err := NewRecvControlRing(128, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	// Empty ring should return false
	_, ok := ring.TryPop()
	if ok {
		t.Error("TryPop should return false on empty ring")
	}
}

func TestRecvControlPacketType_String(t *testing.T) {
	tests := []struct {
		typ      RecvControlPacketType
		expected string
	}{
		{RecvControlTypeACKACK, "ACKACK"},
		{RecvControlTypeKEEPALIVE, "KEEPALIVE"},
		{RecvControlPacketType(99), "UNKNOWN"},
	}

	for _, tc := range tests {
		got := tc.typ.String()
		if got != tc.expected {
			t.Errorf("(%d).String() = %s, want %s", tc.typ, got, tc.expected)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RTT Calculation Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecvControlRing_RTT_Timestamp(t *testing.T) {
	ring, err := NewRecvControlRing(128, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	// Simulate: ACKACK arrives at specific time
	arrivalTime := time.Now()
	ackNum := uint32(100)

	ring.PushACKACK(ackNum, arrivalTime)

	// Simulate: EventLoop processes later
	time.Sleep(10 * time.Millisecond)
	processTime := time.Now()

	pkt, ok := ring.TryPop()
	if !ok {
		t.Fatal("TryPop failed")
	}

	// The timestamp should be the arrival time, not process time
	packetArrival := pkt.ArrivalTime()
	if packetArrival.After(processTime) {
		t.Error("packet arrival time should be before process time")
	}

	// Should be close to arrivalTime (within a few microseconds)
	diff := packetArrival.Sub(arrivalTime)
	if diff < 0 || diff > time.Microsecond {
		t.Errorf("timestamp drift: %v", diff)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecvControlRing_ConcurrentPushACKACK(t *testing.T) {
	ring, err := NewRecvControlRing(65536, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	const numGoroutines = 4
	const pushesPerGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	now := time.Now()

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < pushesPerGoroutine; i++ {
				ackNum := uint32(goroutineID*pushesPerGoroutine + i)
				ring.PushACKACK(ackNum, now)
			}
		}(g)
	}

	wg.Wait()

	totalPushed := ring.Len()
	t.Logf("Total pushed: %d (expected %d)", totalPushed, numGoroutines*pushesPerGoroutine)

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

func TestRecvControlRing_ConcurrentMixed(t *testing.T) {
	// Use a very large ring to minimize drops in concurrent test
	ring, err := NewRecvControlRing(1<<20, 1) // 1M entries
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	const numGoroutines = 4
	const opsPerGoroutine = 500

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	now := time.Now()

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				if i%2 == 0 {
					ring.PushACKACK(uint32(goroutineID*opsPerGoroutine+i), now)
				} else {
					ring.PushKEEPALIVE()
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify we can drain all
	ackackCount := 0
	keepaliveCount := 0
	for {
		pkt, ok := ring.TryPop()
		if !ok {
			break
		}
		switch pkt.Type {
		case RecvControlTypeACKACK:
			ackackCount++
		case RecvControlTypeKEEPALIVE:
			keepaliveCount++
		}
	}

	t.Logf("ACKACK: %d, KEEPALIVE: %d", ackackCount, keepaliveCount)

	// Under concurrent writes, some contention-related losses may occur
	// This is acceptable for control packets (fallback to locked path).
	// Expect at least 90% success rate.
	expectedPerType := numGoroutines * opsPerGoroutine / 2
	minExpected := expectedPerType * 90 / 100

	if ackackCount < minExpected {
		t.Errorf("ACKACK count = %d, want >= %d (90%% of %d)", ackackCount, minExpected, expectedPerType)
	}
	if keepaliveCount < minExpected {
		t.Errorf("KEEPALIVE count = %d, want >= %d (90%% of %d)", keepaliveCount, minExpected, expectedPerType)
	}

	// Both types should be received
	if ackackCount == 0 {
		t.Error("expected some ACKACK packets")
	}
	if keepaliveCount == 0 {
		t.Error("expected some KEEPALIVE packets")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Producer-Consumer Tests (Realistic Scenario)
// ═══════════════════════════════════════════════════════════════════════════════

// TestRecvControlRing_ProducerConsumer_Realistic simulates actual ACKACK rates.
// In production: ~100 ACKACK/sec (Full ACK every 10ms), single producer.
// This test verifies 100% success with realistic traffic patterns.
func TestRecvControlRing_ProducerConsumer_Realistic(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping time-sensitive producer-consumer test in short mode")
	}
	ring, err := NewRecvControlRing(128, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	const (
		testDuration = 100 * time.Millisecond
		ackInterval  = 1 * time.Millisecond // 1000/sec (10x faster than production for quick test)
	)

	var totalPushed, totalPopped int64
	stopConsumer := make(chan struct{})
	consumerReady := make(chan struct{})

	// Consumer (EventLoop) - polls continuously
	go func() {
		close(consumerReady)
		for {
			select {
			case <-stopConsumer:
				// Final drain
				for {
					if _, ok := ring.TryPop(); ok {
						totalPopped++
					} else {
						return
					}
				}
			default:
				if _, ok := ring.TryPop(); ok {
					totalPopped++
				}
			}
		}
	}()

	<-consumerReady

	// Producer - sends at realistic rate
	start := time.Now()
	ticker := time.NewTicker(ackInterval)
	defer ticker.Stop()

	ackNum := uint32(0)
	for time.Since(start) < testDuration {
		<-ticker.C
		if ring.PushACKACK(ackNum, time.Now()) {
			totalPushed++
		} else {
			t.Errorf("Push failed for ACK %d - ring should never be full at realistic rates", ackNum)
		}
		ackNum++
	}

	close(stopConsumer)
	time.Sleep(10 * time.Millisecond) // Let consumer drain

	t.Logf("Realistic test: pushed=%d, popped=%d in %v", totalPushed, totalPopped, testDuration)

	// At realistic rates, expect 100% success
	if totalPopped != totalPushed {
		t.Errorf("Lost packets: pushed=%d, popped=%d", totalPushed, totalPopped)
	}
}

// TestRecvControlRing_ProducerConsumer_StressWithConsumer shows behavior under
// extreme stress (much faster than production) with active consumer.
// This demonstrates that even under heavy load, the fallback mechanism works.
func TestRecvControlRing_ProducerConsumer_StressWithConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	// Large ring to give consumer more buffer
	ring, err := NewRecvControlRing(4096, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	const numProducers = 4
	const pushesPerProducer = 10000
	totalExpected := int64(numProducers * pushesPerProducer)

	var wg sync.WaitGroup
	var totalPushed, totalPopped, pushFailed int64
	var pushLock sync.Mutex

	stopConsumer := make(chan struct{})
	consumerReady := make(chan struct{})

	// Consumer goroutine
	go func() {
		close(consumerReady)
		for {
			select {
			case <-stopConsumer:
				// Final drain
				for {
					if _, ok := ring.TryPop(); ok {
						totalPopped++
					} else {
						return
					}
				}
			default:
				if _, ok := ring.TryPop(); ok {
					totalPopped++
				}
			}
		}
	}()

	<-consumerReady

	// Producers push as fast as possible
	wg.Add(numProducers)
	now := time.Now()

	for p := 0; p < numProducers; p++ {
		go func(producerID int) {
			defer wg.Done()
			localPushed := int64(0)
			localFailed := int64(0)
			for i := 0; i < pushesPerProducer; i++ {
				ackNum := uint32(producerID*pushesPerProducer + i)
				if ring.PushACKACK(ackNum, now) {
					localPushed++
				} else {
					localFailed++
				}
			}
			pushLock.Lock()
			totalPushed += localPushed
			pushFailed += localFailed
			pushLock.Unlock()
		}(p)
	}

	wg.Wait()
	close(stopConsumer)
	time.Sleep(10 * time.Millisecond) // Let consumer finish draining

	successRate := float64(totalPushed) / float64(totalExpected) * 100
	t.Logf("Stress test: pushed=%d, failed=%d, popped=%d (%.2f%% success)",
		totalPushed, pushFailed, totalPopped, successRate)
	t.Logf("Ring capacity: %d, Producers: %d pushing at max speed", ring.Cap(), numProducers)

	// Under extreme stress, some ring-full failures are expected
	// This is why we have the fallback path
	// The key metric: all successfully pushed packets must be popped
	if totalPopped != totalPushed {
		t.Errorf("Lost packets: pushed=%d but only popped=%d", totalPushed, totalPopped)
	}

	// Log what would happen in production with fallback
	t.Logf("In production: %d packets would use fallback (locked) path", pushFailed)
}

// TestRecvControlRing_ProducerConsumer_SingleProducerMaxSpeed shows throughput
// with a single producer (most realistic) pushing at max speed.
func TestRecvControlRing_ProducerConsumer_SingleProducerMaxSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance-sensitive throughput test in short mode")
	}
	ring, err := NewRecvControlRing(128, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	const totalPackets = 100000
	var totalPushed, totalPopped, pushFailed int64

	stopConsumer := make(chan struct{})
	consumerReady := make(chan struct{})

	// Consumer
	go func() {
		close(consumerReady)
		for {
			select {
			case <-stopConsumer:
				for {
					if _, ok := ring.TryPop(); ok {
						totalPopped++
					} else {
						return
					}
				}
			default:
				if _, ok := ring.TryPop(); ok {
					totalPopped++
				}
			}
		}
	}()

	<-consumerReady
	start := time.Now()
	now := time.Now()

	// Single producer at max speed
	for i := 0; i < totalPackets; i++ {
		if ring.PushACKACK(uint32(i), now) {
			totalPushed++
		} else {
			pushFailed++
		}
	}

	elapsed := time.Since(start)
	close(stopConsumer)
	time.Sleep(10 * time.Millisecond)

	successRate := float64(totalPushed) / float64(totalPackets) * 100
	throughput := float64(totalPushed) / elapsed.Seconds()

	t.Logf("Single producer: pushed=%d, failed=%d, popped=%d (%.2f%% success)",
		totalPushed, pushFailed, totalPopped, successRate)
	t.Logf("Throughput: %.2f million packets/sec", throughput/1e6)

	// Single producer should have very high success - only fails if ring fills
	// before consumer can drain. With matching speeds, should be >95%
	if successRate < 90 {
		t.Errorf("Success rate %.2f%% is lower than expected for single producer", successRate)
	}

	// All pushed should be popped
	if totalPopped != totalPushed {
		t.Errorf("Lost packets: pushed=%d, popped=%d", totalPushed, totalPopped)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Fallback Behavior Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecvControlRing_FullFallback(t *testing.T) {
	// Test the pattern used when ring is full: return false, caller uses locked path
	ring, err := NewRecvControlRing(4, 1)
	if err != nil {
		t.Fatalf("NewRecvControlRing failed: %v", err)
	}

	now := time.Now()
	pushedCount := 0
	droppedCount := 0

	// Try to push more than capacity
	for i := 0; i < 10; i++ {
		if ring.PushACKACK(uint32(i), now) {
			pushedCount++
		} else {
			droppedCount++
		}
	}

	t.Logf("pushed: %d, dropped: %d (cap: %d)", pushedCount, droppedCount, ring.Cap())

	// Should have dropped some
	if droppedCount == 0 {
		t.Error("expected some packets to be dropped")
	}

	// pushed + dropped should equal total attempts
	if pushedCount+droppedCount != 10 {
		t.Errorf("pushed(%d) + dropped(%d) != 10", pushedCount, droppedCount)
	}
}

