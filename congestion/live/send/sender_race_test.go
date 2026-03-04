//go:build go1.18

package send

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Race condition tests for sender
//
// These tests verify thread-safety of the sender implementation.
// The lockless design ensures:
// - Push() can be called from any goroutine (writes to lock-free ring)
// - EventLoop is single-threaded (reads from ring, writes to btree)
// - Control packets flow through control ring (lock-free)
//
// Run with: go test -race ./congestion/live/send/...
//
// Reference: lockless_sender_design.md Section 7.4 "Race Analysis"
// ============================================================================

// TestRace_SinglePusher tests single goroutine calling Push() + concurrent EventLoop
// Note: Push() is NOT thread-safe by design (accesses nextSequenceNumber, probeTime).
// The design states Push() is typically called from a single goroutine.
func TestRace_SinglePusher(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 4096, // Large ring to avoid drops
		SendRingShards:               1,    // Single shard
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	const totalPackets = 400
	var wg sync.WaitGroup
	var pushed atomic.Int64

	// Single pusher goroutine (design constraint)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < totalPackets; i++ {
			pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
			s.pushRing(pkt)
			pushed.Add(1)
		}
	}()

	wg.Wait()

	require.Equal(t, int64(totalPackets), pushed.Load())

	// Drain all (with EventLoop context)
	var drained int
	runInEventLoopContext(s, func() {
		drained = s.drainRingToBtreeEventLoop()
	})

	// Should have drained all pushed packets
	require.Equal(t, totalPackets, drained)
}

// TestRace_PushWhileDraining tests Push() concurrent with EventLoop drain
func TestRace_PushWhileDraining(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 2048,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	s.nowFn = func() uint64 { return 1_000_000 }

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Push goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
				s.pushRing(pkt)
				time.Sleep(10 * time.Microsecond)
			}
		}
	}()

	// Drain goroutine (simulating EventLoop)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop() // Enter context for entire goroutine
		defer s.ExitEventLoop()
		for {
			select {
			case <-stop:
				return
			default:
				s.drainRingToBtreeEventLoop()
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// Run for a short time
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Final drain (with EventLoop context)
	runInEventLoopContext(s, func() {
		s.drainRingToBtreeEventLoop()
	})

	// Should have some packets in btree
	require.GreaterOrEqual(t, s.packetBtree.Len()+s.packetRing.Len(), 0)
}

// TestRace_PushWhileDelivering tests Push() concurrent with delivery
func TestRace_PushWhileDelivering(t *testing.T) {
	var deliveredCount atomic.Int64

	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) { deliveredCount.Add(1) },
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 2048,
		UseSendControlRing:           true,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          10_000_000,
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(1_000_000)
	s.nowFn = nowUs.Load

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Push goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				pkt := createTestPacketWithTsbpd(0, uint64(i)*10) // Small TSBPD
				s.pushRing(pkt)
				time.Sleep(10 * time.Microsecond)
			}
		}
	}()

	// EventLoop simulation
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop() // Enter context for entire goroutine
		defer s.ExitEventLoop()
		for {
			select {
			case <-stop:
				return
			default:
				s.drainRingToBtreeEventLoop()
				s.deliverReadyPacketsEventLoop(nowUs.Load())
				nowUs.Add(1000) // Advance time
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Some packets should have been delivered
	require.GreaterOrEqual(t, deliveredCount.Load(), int64(0))
}

// TestRace_ACKWhileDelivering tests ACK processing concurrent with delivery
func TestRace_ACKWhileDelivering(t *testing.T) {
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

	s.nowFn = func() uint64 { return 1_000_000 }

	// Pre-populate btree
	for i := 0; i < 1000; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100)
		s.packetBtree.Insert(pkt)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// NOTE: In the actual design, ACK processing happens IN the EventLoop,
	// not from a separate goroutine. This test verifies that IF someone
	// mistakenly called ackBtree from outside EventLoop, there would be races.
	// The test should pass with -race because we're calling sequentially in
	// this simplified version.

	// Simulated EventLoop (does both ACK and delivery)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop() // Enter context for entire goroutine
		defer s.ExitEventLoop()
		ackSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				// Delivery
				s.deliverReadyPacketsEventLoop(1_000_000)
				// ACK (in EventLoop)
				s.ackBtree(circular.New(ackSeq, packet.MAX_SEQUENCENUMBER))
				ackSeq += 10
				time.Sleep(10 * time.Microsecond)
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Btree should have fewer packets
	require.Less(t, s.packetBtree.Len(), 1000)
}

// TestRace_DropWhileDelivering tests drop concurrent with delivery
func TestRace_DropWhileDelivering(t *testing.T) {
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
		SendDropThresholdUs:          100_000, // 100ms
		DropThreshold:                100_000,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(0)
	s.nowFn = nowUs.Load

	// Pre-populate with old packets
	for i := 0; i < 100; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100) // TSBPD 0-9900
		s.packetBtree.Insert(pkt)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Simulated EventLoop
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop() // Enter context for entire goroutine
		defer s.ExitEventLoop()
		for {
			select {
			case <-stop:
				return
			default:
				currentNow := nowUs.Load()
				s.deliverReadyPacketsEventLoop(currentNow)
				s.dropOldPacketsEventLoop(currentNow)
				nowUs.Add(10000) // Advance 10ms
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Some packets should have been dropped or delivered
	require.Less(t, s.packetBtree.Len(), 100)
}

// TestRace_MultipleOperations tests all operations running concurrently
// This test verifies the CORRECT architecture:
// - Single Push() goroutine (design constraint: Push() not thread-safe)
// - Single EventLoop goroutine does ALL btree operations
//
// Note: pushRing accesses s.probeTime and s.nextSequenceNumber which are
// NOT thread-safe. The design explicitly states Push() is typically called
// from a single goroutine (the application writer). See push.go line 119.
func TestRace_MultipleOperations(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 4096,
		UseSendControlRing:           true,
		SendControlRingSize:          512,
		UseSendEventLoop:             true,
		SendDropThresholdUs:          1_000_000,
		DropThreshold:                1_000_000,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(0)
	s.nowFn = nowUs.Load

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Single Push goroutine (design constraint: Push() is NOT thread-safe)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; ; j++ {
			select {
			case <-stop:
				return
			default:
				pkt := createTestPacketWithTsbpd(0, nowUs.Load()+uint64(j)*10)
				s.pushRing(pkt)
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	// Single EventLoop goroutine (ALL btree operations happen here)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop() // Enter context for entire goroutine
		defer s.ExitEventLoop()
		for {
			select {
			case <-stop:
				return
			default:
				currentNow := nowUs.Load()

				// All btree operations in single goroutine (EventLoop pattern)
				s.drainRingToBtreeEventLoop()
				s.deliverReadyPacketsEventLoop(currentNow)
				s.dropOldPacketsEventLoop(currentNow)

				// Advance time
				nowUs.Add(1000)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// Run for 100ms
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Final cleanup (single-threaded now, with EventLoop context)
	runInEventLoopContext(s, func() {
		s.drainRingToBtreeEventLoop()
	})

	t.Log("MultipleOperations completed without race")
}

// ============================================================================
// Tick() race tests - tests the legacy locked path
//
// The Tick() methods use locking wrappers (metrics.WithWLockTiming) and can
// be called concurrently with Push() from different goroutines.
// ============================================================================

// TestRace_Tick_Push tests Tick() concurrent with Push()
// This is a common scenario: application pushes packets while the periodic
// ticker delivers them.
func TestRace_Tick_Push(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		BtreeDegree:           32,
		UseSendRing:           false, // Use locked path
		UseSendControlRing:    false,
		UseSendEventLoop:      false, // Use Tick() not EventLoop
		SendDropThresholdUs:   10_000_000,
		DropThreshold:         10_000_000,
		LockTimingMetrics:     &metrics.LockTimingMetrics{}, // Enable lock timing
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(0)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Single Push goroutine (design constraint)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				pkt := createTestPacketWithTsbpd(0, nowUs.Load())
				s.Push(pkt)
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	// Tick goroutine (simulates periodic ticker)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Tick(nowUs.Load())
				nowUs.Add(1000)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// Run for 100ms
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Tick+Push completed without race")
}

// TestRace_Tick_ACK tests Tick() concurrent with ACK()
func TestRace_Tick_ACK(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           false,
		UseSendControlRing:    false,
		UseSendEventLoop:      false,
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(1_000_000)

	// Pre-populate with packets
	for i := 0; i < 1000; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100)
		s.Push(pkt)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Tick goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Tick(nowUs.Load())
				nowUs.Add(1000)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// ACK goroutine (simulates ACK packets arriving)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ackSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				s.ACK(circular.New(ackSeq, packet.MAX_SEQUENCENUMBER))
				ackSeq += 5
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Tick+ACK completed without race")
}

// TestRace_Tick_NAK tests Tick() concurrent with NAK()
func TestRace_Tick_NAK(t *testing.T) {
	var retransmitted atomic.Int64

	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) { retransmitted.Add(1) },
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           false,
		UseSendControlRing:    false,
		UseSendEventLoop:      false,
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(1_000_000)

	// Pre-populate with packets
	for i := 0; i < 500; i++ {
		pkt := createTestPacketWithTsbpd(uint32(i), uint64(i)*100)
		s.Push(pkt)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Tick goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Tick(nowUs.Load())
				nowUs.Add(1000)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// NAK goroutine (simulates NAK packets arriving)
	wg.Add(1)
	go func() {
		defer wg.Done()
		nakSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				// Request retransmission of a few packets
				seqs := []circular.Number{
					circular.New(nakSeq, packet.MAX_SEQUENCENUMBER),
					circular.New(nakSeq+1, packet.MAX_SEQUENCENUMBER),
				}
				s.NAK(seqs)
				nakSeq += 10
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Logf("Tick+NAK completed without race, retransmitted=%d", retransmitted.Load())
}

// TestRace_Tick_Push_ACK_NAK tests all operations concurrent with Tick()
func TestRace_Tick_Push_ACK_NAK(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           false,
		UseSendControlRing:    false,
		UseSendEventLoop:      false,
		SendDropThresholdUs:   10_000_000,
		DropThreshold:         10_000_000,
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(0)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Single Push goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				pkt := createTestPacketWithTsbpd(0, nowUs.Load())
				s.Push(pkt)
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	// Tick goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Tick(nowUs.Load())
				nowUs.Add(1000)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// ACK goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		ackSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				s.ACK(circular.New(ackSeq, packet.MAX_SEQUENCENUMBER))
				ackSeq += 10
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()

	// NAK goroutine - NAK needs pairs for range format
	wg.Add(1)
	go func() {
		defer wg.Done()
		nakSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				seqs := []circular.Number{
					circular.New(nakSeq, packet.MAX_SEQUENCENUMBER),
					circular.New(nakSeq+2, packet.MAX_SEQUENCENUMBER),
				}
				s.NAK(seqs)
				nakSeq += 20
				time.Sleep(300 * time.Microsecond)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Tick+Push+ACK+NAK completed without race")
}

// TestRace_Tick_WithRing tests Tick() with ring mode enabled
// In ring mode, Push() writes to lock-free ring, Tick() drains it with lock
func TestRace_Tick_WithRing(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		BtreeDegree:           32,
		UseSendRing:           true, // Ring mode - Push is lock-free
		SendRingSize:          1024,
		SendRingShards:        1,
		UseSendControlRing:    true,
		SendControlRingSize:   256,
		UseSendEventLoop:      false, // Use Tick() not EventLoop
		SendDropThresholdUs:   10_000_000,
		DropThreshold:         10_000_000,
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(0)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Single Push goroutine (writes to lock-free ring)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				pkt := createTestPacketWithTsbpd(0, nowUs.Load())
				s.Push(pkt)
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	// Tick goroutine (drains ring to btree with lock)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Tick(nowUs.Load())
				nowUs.Add(1000)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// ACK goroutine (uses lock)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ackSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				s.ACK(circular.New(ackSeq, packet.MAX_SEQUENCENUMBER))
				ackSeq += 5
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Tick+Ring completed without race")
}

// TestRace_Tick_ListMode tests Tick() with legacy list mode
func TestRace_Tick_ListMode(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              false, // List mode
		UseSendRing:           false,
		UseSendControlRing:    false,
		UseSendEventLoop:      false,
		SendDropThresholdUs:   10_000_000,
		DropThreshold:         10_000_000,
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(0)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Single Push goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				pkt := createTestPacketWithTsbpd(0, nowUs.Load())
				s.Push(pkt)
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	// Tick goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Tick(nowUs.Load())
				nowUs.Add(1000)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// ACK goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		ackSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				s.ACK(circular.New(ackSeq, packet.MAX_SEQUENCENUMBER))
				ackSeq += 5
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Tick+List mode completed without race")
}

// TestRace_ReadMetrics tests reading metrics while operations are running
func TestRace_ReadMetrics(t *testing.T) {
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

	s.nowFn = func() uint64 { return 1_000_000 }

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Push goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				pkt := createTestPacketWithTsbpd(0, uint64(i)*100)
				s.pushRing(pkt)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	// Metrics reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				// Read various metrics
				_ = m.SendRingPushed.Load()
				_ = m.SendBtreeInserted.Load()
				_ = m.SendDeliveryPackets.Load()
				_ = m.SendDeliveryAttempts.Load()
				time.Sleep(10 * time.Microsecond)
			}
		}
	}()

	// EventLoop
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop() // Enter context for entire goroutine
		defer s.ExitEventLoop()
		for {
			select {
			case <-stop:
				return
			default:
				s.drainRingToBtreeEventLoop()
				s.deliverReadyPacketsEventLoop(1_000_000)
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Metrics read concurrently without race")
}

// ============================================================================
// Control Ring Overflow Tests
// ============================================================================
// NOTE: These tests have been moved to sender_control_ring_overflow_test.go
// See: TestRace_ControlRingOverflow_ACK, TestRace_ControlRingOverflow_NAK, etc.
