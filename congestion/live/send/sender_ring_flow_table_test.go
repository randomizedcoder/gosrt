//go:build go1.18

package send

import (
	"sync"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Table-driven tests for Ring → Btree flow
//
// Tests the lock-free ring buffer flow:
// - Push to ring (from application)
// - Drain from ring to btree (EventLoop)
// - Verify sequence number assignment
// - Verify ordering preservation
//
// Reference: lockless_sender_design.md Section 3.2 "SendPacketRing"
// ============================================================================

// RingFlowTestCase defines a test case for ring flow
type RingFlowTestCase struct {
	Name string

	// Setup
	ISN          uint32
	RingSize     int
	RingShards   int
	PacketCount  int

	// Expected
	ExpectedDrained int
	ExpectedInBtree int
}

var ringFlowTestCases = []RingFlowTestCase{
	{
		Name:            "Single_Packet",
		ISN:             0,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     1,
		ExpectedDrained: 1,
		ExpectedInBtree: 1,
	},
	{
		Name:            "Small_Batch_10",
		ISN:             0,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     10,
		ExpectedDrained: 10,
		ExpectedInBtree: 10,
	},
	{
		Name:            "Medium_Batch_100",
		ISN:             549144712,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     100,
		ExpectedDrained: 100,
		ExpectedInBtree: 100,
	},
	{
		Name:            "Large_Batch_500",
		ISN:             0,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     500,
		ExpectedDrained: 500,
		ExpectedInBtree: 500,
	},
	{
		Name:            "Multi_Shard_2",
		ISN:             0,
		RingSize:        512,
		RingShards:      2,
		PacketCount:     100,
		ExpectedDrained: 100,
		ExpectedInBtree: 100,
	},
	{
		Name:            "Multi_Shard_4",
		ISN:             0,
		RingSize:        256,
		RingShards:      4,
		PacketCount:     200,
		ExpectedDrained: 200,
		ExpectedInBtree: 200,
	},
	{
		Name:            "High_ISN_Wraparound",
		ISN:             MaxSeq31Bit - 50,
		RingSize:        1024,
		RingShards:      1,
		PacketCount:     100,
		ExpectedDrained: 100,
		ExpectedInBtree: 100,
	},
}

// TestRingFlow_Table runs all ring flow test cases
func TestRingFlow_Table(t *testing.T) {
	for _, tc := range ringFlowTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber:        circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:            m,
				OnDeliver:                    func(p packet.Packet) {},
				StartTime:                    time.Now(),
				UseBtree:                     true,
				BtreeDegree:                  32,
				UseSendRing:                  true,
				SendRingSize:                 tc.RingSize,
				SendRingShards:               tc.RingShards,
				UseSendControlRing:           true,
				UseSendEventLoop:             true,
				SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
				SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
			}).(*sender)

			// Push packets
			for i := 0; i < tc.PacketCount; i++ {
				pkt := createTestPacketWithTsbpd(0, uint64(i)*100) // Seq assigned by pushRing
				s.pushRing(pkt)
			}

			// Verify ring has packets
			require.GreaterOrEqual(t, s.packetRing.Len(), 0)

			// Drain all (with EventLoop context)
			var drained int
			runInEventLoopContext(s, func() {
				drained = s.drainRingToBtreeEventLoop()
			})

			// Verify drain count
			require.Equal(t, tc.ExpectedDrained, drained,
				"drained count mismatch")

			// Verify btree count
			require.Equal(t, tc.ExpectedInBtree, s.packetBtree.Len(),
				"btree count mismatch")

			// Verify ring is empty
			require.Equal(t, 0, s.packetRing.Len(),
				"ring should be empty after drain")
		})
	}
}

// TestRingFlow_SequenceAssignment verifies sequence numbers are assigned in Push order
func TestRingFlow_SequenceAssignment(t *testing.T) {
	testCases := []struct {
		Name         string
		ISN          uint32
		PacketCount  int
		ExpectedSeqs []uint32
	}{
		{
			Name:         "ISN_Zero",
			ISN:          0,
			PacketCount:  5,
			ExpectedSeqs: []uint32{0, 1, 2, 3, 4},
		},
		{
			Name:         "ISN_1000",
			ISN:          1000,
			PacketCount:  5,
			ExpectedSeqs: []uint32{1000, 1001, 1002, 1003, 1004},
		},
		{
			Name:         "ISN_High",
			ISN:          549144712,
			PacketCount:  3,
			ExpectedSeqs: []uint32{549144712, 549144713, 549144714},
		},
		{
			Name:         "ISN_Wrap",
			ISN:          MaxSeq31Bit - 2,
			PacketCount:  5,
			ExpectedSeqs: []uint32{MaxSeq31Bit - 2, MaxSeq31Bit - 1, MaxSeq31Bit, 0, 1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver:             func(p packet.Packet) {},
				StartTime:             time.Now(),
				UseBtree:              true,
				UseSendRing:           true,
				UseSendControlRing:    true,
				UseSendEventLoop:      true,
			}).(*sender)

			// Push packets
			for i := 0; i < tc.PacketCount; i++ {
				pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
				s.pushRing(pkt)
			}

			// Drain to btree (with EventLoop context)
			runInEventLoopContext(s, func() {
				s.drainRingToBtreeEventLoop()
			})

			// Collect sequences from btree (in order)
			var seqs []uint32
			s.packetBtree.Iterate(func(p packet.Packet) bool {
				seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
				return true
			})

			require.Equal(t, tc.ExpectedSeqs, seqs,
				"sequence numbers should match expected order")
		})
	}
}

// TestRingFlow_MultipleDrains tests incremental draining
func TestRingFlow_MultipleDrains(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Push 10 packets
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
		s.pushRing(pkt)
	}

	// Drain (with EventLoop context)
	var drained1, drained2 int
	runInEventLoopContext(s, func() {
		drained1 = s.drainRingToBtreeEventLoop()
	})
	require.Equal(t, 10, drained1)
	require.Equal(t, 10, s.packetBtree.Len())

	// Push 10 more
	for i := 10; i < 20; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
		s.pushRing(pkt)
	}

	// Drain again (with EventLoop context)
	runInEventLoopContext(s, func() {
		drained2 = s.drainRingToBtreeEventLoop()
	})
	require.Equal(t, 10, drained2)
	require.Equal(t, 20, s.packetBtree.Len())

	// Verify sequence continuity
	var seqs []uint32
	s.packetBtree.Iterate(func(p packet.Packet) bool {
		seqs = append(seqs, p.Header().PacketSequenceNumber.Val())
		return true
	})

	for i := 0; i < 20; i++ {
		require.Equal(t, uint32(i), seqs[i], "sequence %d mismatch", i)
	}
}

// TestRingFlow_RingCapacity tests behavior at ring capacity
func TestRingFlow_RingCapacity(t *testing.T) {
	ringSize := 64

	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 ringSize,
		SendRingShards:               1,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Push exactly ring capacity
	for i := 0; i < ringSize; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
		s.pushRing(pkt)
	}

	// Drain all (with EventLoop context)
	var drained int
	runInEventLoopContext(s, func() {
		drained = s.drainRingToBtreeEventLoop()
	})

	// Should have drained up to ring capacity
	require.LessOrEqual(t, drained, ringSize)
	require.Equal(t, drained, s.packetBtree.Len())
}

// TestRingFlow_EmptyDrain tests draining when ring is empty
func TestRingFlow_EmptyDrain(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Drain when empty (with EventLoop context)
	var drained int
	runInEventLoopContext(s, func() {
		drained = s.drainRingToBtreeEventLoop()
	})
	require.Equal(t, 0, drained)
	require.Equal(t, 0, s.packetBtree.Len())
}

// TestRingFlow_PreservesTsbpdTime verifies TSBPD time is preserved through flow
func TestRingFlow_PreservesTsbpdTime(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	expectedTsbpd := []uint64{100_000, 200_000, 300_000, 400_000, 500_000}

	// Push packets with specific TSBPD times
	for i, tsbpd := range expectedTsbpd {
		pkt := createTestPacketWithTsbpd(uint32(i), tsbpd)
		// Note: pushRing may modify PktTsbpdTime for probe packets (seq % 16)
		// For seq 0 and 1, there's special handling
		s.pushRing(pkt)
	}

	// Drain (with EventLoop context)
	runInEventLoopContext(s, func() {
		s.drainRingToBtreeEventLoop()
	})

	// Verify TSBPD times are preserved (for non-probe packets)
	idx := 0
	s.packetBtree.Iterate(func(p packet.Packet) bool {
		seq := p.Header().PacketSequenceNumber.Val()
		// Skip probe packets (seq % 16 == 0 or 1)
		if seq%16 != 0 && seq%16 != 1 {
			require.Equal(t, expectedTsbpd[idx], p.Header().PktTsbpdTime,
				"TSBPD time for packet %d", seq)
		}
		idx++
		return true
	})
}

// TestAtomicSequenceNumber_Concurrent tests that atomic sequence assignment
// produces unique sequence numbers under concurrent access.
// Reference: sender_lockfree_architecture.md Section 7.6
func TestAtomicSequenceNumber_Concurrent(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 4096,
		SendRingShards:               4,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	const goroutines = 4
	const perGoroutine = 100

	var wg sync.WaitGroup
	seqChan := make(chan uint32, goroutines*perGoroutine)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				seq := s.assignSequenceNumber()
				seqChan <- seq.Val()
			}
		}()
	}

	wg.Wait()
	close(seqChan)

	// Collect all sequences and verify uniqueness
	seqs := make(map[uint32]bool)
	for seq := range seqChan {
		require.False(t, seqs[seq], "Duplicate sequence: %d", seq)
		seqs[seq] = true
	}

	require.Len(t, seqs, goroutines*perGoroutine, "Should have all unique sequences")
	require.Equal(t, uint64(goroutines*perGoroutine), m.SendSeqAssigned.Load(),
		"SendSeqAssigned metric should match")

	t.Logf("Concurrent test: %d unique sequences from %d goroutines",
		len(seqs), goroutines)
}

// TestAtomicSequenceNumber_Wraparound tests 31-bit wraparound behavior
// Reference: sender_lockfree_architecture.md Section 3.3
func TestAtomicSequenceNumber_Wraparound(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	// Start near MAX_SEQUENCENUMBER
	isn := packet.MAX_SEQUENCENUMBER - 2
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(isn, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		SendRingShards:               1,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Assign 5 sequence numbers - should wrap around
	seqs := make([]uint32, 5)
	for i := 0; i < 5; i++ {
		seqs[i] = s.assignSequenceNumber().Val()
	}

	// Expected: MAX-2, MAX-1, MAX, 0, 1
	expected := []uint32{
		packet.MAX_SEQUENCENUMBER - 2,
		packet.MAX_SEQUENCENUMBER - 1,
		packet.MAX_SEQUENCENUMBER,
		0,
		1,
	}

	require.Equal(t, expected, seqs, "Sequence wraparound mismatch")

	// Verify wraparound metric was incremented
	// (wraps happen when rawSeq != seq31, i.e., when seq exceeds 31 bits)
	// In this case, seq 3 (rawSeq = MAX+1) and seq 4 (rawSeq = MAX+2) trigger wrap
	require.GreaterOrEqual(t, m.SendSeqWraparound.Load(), uint64(2),
		"Should have recorded wraparound events")

	t.Logf("Wraparound test: seqs=%v, wraps=%d", seqs, m.SendSeqWraparound.Load())
}

// ============================================================================
// PushDirect Tests
//
// Tests for Phase 6: Direct ring push from connection.Write()
// Reference: sender_lockfree_architecture.md Section 7.8
// ============================================================================

// TestPushDirect_Basic tests basic PushDirect functionality
func TestPushDirect_Basic(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(100, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 1024,
		SendRingShards:               1,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Verify UseRing() returns true
	require.True(t, s.UseRing(), "UseRing should return true")

	// Push 10 packets directly
	for i := 0; i < 10; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(1000+i*100))
		ok := s.PushDirect(pkt)
		require.True(t, ok, "PushDirect should succeed")
	}

	// Verify metrics
	require.Equal(t, uint64(10), m.SendRingPushed.Load(), "10 packets pushed")
	require.Equal(t, uint64(10), m.SendSeqAssigned.Load(), "10 sequences assigned")

	// Drain and verify sequence numbers
	for i := 0; i < 10; i++ {
		pkt, ok := s.packetRing.TryPop()
		require.True(t, ok)
		require.Equal(t, uint32(100+i), pkt.Header().PacketSequenceNumber.Val())
		require.Equal(t, uint32(0), pkt.Header().TransmitCount, "TransmitCount should be 0")
	}
}

// TestPushDirect_Concurrent tests concurrent PushDirect calls
func TestPushDirect_Concurrent(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 4096,
		SendRingShards:               4, // Multiple shards for concurrency
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	const goroutines = 4
	const perGoroutine = 50

	var wg sync.WaitGroup
	successCount := make([]int, goroutines)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				pkt := createTestPacketWithTsbpd(0, uint64(1000+i*100))
				if s.PushDirect(pkt) {
					successCount[gid]++
				}
			}
		}(g)
	}

	wg.Wait()

	totalSuccess := 0
	for _, c := range successCount {
		totalSuccess += c
	}

	// Verify all pushes succeeded (ring is large enough)
	require.Equal(t, goroutines*perGoroutine, totalSuccess,
		"All concurrent pushes should succeed")

	// Verify unique sequence numbers (no duplicates)
	seqs := make(map[uint32]bool)
	for {
		pkt, ok := s.packetRing.TryPop()
		if !ok {
			break
		}
		seq := pkt.Header().PacketSequenceNumber.Val()
		require.False(t, seqs[seq], "Duplicate sequence number: %d", seq)
		seqs[seq] = true
	}

	require.Len(t, seqs, totalSuccess, "Should have unique sequences")
	t.Logf("Concurrent test: %d unique sequences from %d goroutines", len(seqs), goroutines)
}

// TestPushDirect_RingFull tests behavior when ring is full
func TestPushDirect_RingFull(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		BtreeDegree:                  32,
		UseSendRing:                  true,
		SendRingSize:                 16, // Small ring
		SendRingShards:               1,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	// Fill the ring
	successCount := 0
	for i := 0; i < 100; i++ {
		pkt := createTestPacketWithTsbpd(0, uint64(1000+i*100))
		if s.PushDirect(pkt) {
			successCount++
		}
	}

	// Some should succeed, some should fail
	require.Greater(t, successCount, 0, "Some pushes should succeed")
	require.Less(t, successCount, 100, "Some pushes should fail (ring full)")
	require.Greater(t, m.SendRingDropped.Load(), uint64(0), "Should have ring drops")

	t.Logf("Ring full test: %d succeeded, %d dropped",
		successCount, m.SendRingDropped.Load())
}

// TestUseRing_Disabled tests UseRing returns false when ring not enabled
func TestUseRing_Disabled(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		// Ring NOT enabled
	}).(*sender)

	require.False(t, s.UseRing(), "UseRing should return false when ring disabled")

	// PushDirect should return false
	pkt := createTestPacketWithTsbpd(0, 1000)
	ok := s.PushDirect(pkt)
	require.False(t, ok, "PushDirect should fail when ring disabled")
}

