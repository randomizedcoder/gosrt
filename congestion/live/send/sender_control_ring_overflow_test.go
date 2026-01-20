//go:build go1.18

package send

import (
	"sync"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ============================================================================
// Control Ring Overflow Tests
//
// These tests verify the race condition that occurs when the control ring
// overflows and ACK/NAK processing falls back to direct btree access.
//
// Bug discovered: 2026-01-17
// When control ring is full:
// 1. EventLoop is iterating btree via IterateFrom() (no lock)
// 2. ACK() falls back to ackBtree() with lock acquisition
// 3. But EventLoop doesn't hold the lock → RACE
//
// Trigger conditions:
// - 4+ receive rings (higher concurrency in receive path)
// - High throughput (~350+ Mb/s)
// - Many ACKs arriving simultaneously
// ============================================================================

// TestRace_ControlRingOverflow_ACK tests race when ACK control ring overflows.
// This is the bug that caused panic at 4 receive rings + 350 Mb/s.
//
// EXPECTED TO FAIL with -race until the bug is fixed.
// The fix options are:
// 1. Never fallback: drop ACK if ring is full (may cause retransmissions)
// 2. Block until ring has space (may stall caller)
// 3. Use mutex in EventLoop when control ring has fallback enabled
func TestRace_ControlRingOverflow_ACK(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	// Use TINY control ring to force overflow
	const controlRingSize = 4
	const controlRingShards = 1

	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 4096,
		SendRingShards:               1,
		UseSendControlRing:           true,
		SendControlRingSize:          controlRingSize,   // TINY - will overflow!
		SendControlRingShards:        controlRingShards, // Single shard
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          10_000_000,
		DropThreshold:                10_000_000,
	}).(*sender)

	// Fixed time for deterministic TSBPD
	s.nowFn = func() uint64 { return 1_000_000 }

	// Pre-populate btree with packets (so EventLoop has something to iterate)
	for i := uint32(0); i < 1000; i++ {
		pkt := createTestPacketWithTsbpd(i, 0) // All ready for delivery
		s.packetBtree.Insert(pkt)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Simulated EventLoop goroutine - continuously iterates btree
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop()
		defer s.ExitEventLoop()

		for {
			select {
			case <-stop:
				return
			default:
				// This is what EventLoop does - iterate the btree
				// WITHOUT holding any lock (lock-free design)
				s.packetBtree.IterateFrom(0, func(p packet.Packet) bool {
					// Simulate some work
					_ = p.Header().PacketSequenceNumber
					return true
				})

				// Also do delivery (another btree access)
				s.deliverReadyPacketsEventLoop(s.nowFn())

				// Process control ring (safe path)
				s.processControlPacketsDelta()
			}
		}
	}()

	// ACK flood goroutine - sends ACKs faster than EventLoop can process
	// This will overflow the control ring and trigger fallback to direct btree access
	wg.Add(1)
	go func() {
		defer wg.Done()
		ackSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				// Flood ACKs - this will overflow the tiny control ring
				// causing fallback to direct ackBtree() call
				s.ACK(circular.New(ackSeq, packet.MAX_SEQUENCENUMBER))
				ackSeq++
				// NO sleep - maximum pressure on control ring
			}
		}
	}()

	// Let it run for a bit - race detector should catch the issue
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Check that we actually triggered overflow
	droppedACKs := m.SendControlRingDroppedACK.Load()
	t.Logf("Control ring dropped ACKs: %d", droppedACKs)

	// If we didn't drop any ACKs, the test didn't trigger the bug
	if droppedACKs == 0 {
		t.Log("WARNING: No control ring overflow occurred - increase ACK rate or decrease ring size")
	} else {
		t.Logf("SUCCESS: Triggered %d ACK fallbacks - race detector should have caught this", droppedACKs)
	}
}

// TestRace_ControlRingOverflow_NAK tests race when NAK control ring overflows.
// Same bug as ACK, but with NAK processing.
func TestRace_ControlRingOverflow_NAK(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	// Use TINY control ring to force overflow
	const controlRingSize = 4
	const controlRingShards = 1

	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 4096,
		SendRingShards:               1,
		UseSendControlRing:           true,
		SendControlRingSize:          controlRingSize,   // TINY - will overflow!
		SendControlRingShards:        controlRingShards, // Single shard
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          10_000_000,
		DropThreshold:                10_000_000,
	}).(*sender)

	// Fixed time for deterministic TSBPD
	s.nowFn = func() uint64 { return 1_000_000 }

	// Pre-populate btree with packets (so NAK has something to find)
	for i := uint32(0); i < 1000; i++ {
		pkt := createTestPacketWithTsbpd(i, 0)
		s.packetBtree.Insert(pkt)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Simulated EventLoop goroutine - continuously iterates btree
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop()
		defer s.ExitEventLoop()

		for {
			select {
			case <-stop:
				return
			default:
				// Iterate btree WITHOUT lock
				s.packetBtree.IterateFrom(0, func(p packet.Packet) bool {
					_ = p.Header().PacketSequenceNumber
					return true
				})

				// Delivery (another btree access)
				s.deliverReadyPacketsEventLoop(s.nowFn())

				// Process control ring (safe path)
				s.processControlPacketsDelta()
			}
		}
	}()

	// NAK flood goroutine - sends NAKs faster than EventLoop can process
	wg.Add(1)
	go func() {
		defer wg.Done()
		nakSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				// Flood NAKs - this will overflow the tiny control ring
				// causing fallback to direct nakBtree() call
				seqs := []circular.Number{
					circular.New(nakSeq, packet.MAX_SEQUENCENUMBER),
					circular.New(nakSeq, packet.MAX_SEQUENCENUMBER), // Single packet range
				}
				s.NAK(seqs)
				nakSeq++
				// NO sleep - maximum pressure on control ring
			}
		}
	}()

	// Let it run for a bit
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Check that we actually triggered overflow
	droppedNAKs := m.SendControlRingDroppedNAK.Load()
	t.Logf("Control ring dropped NAKs: %d", droppedNAKs)

	if droppedNAKs == 0 {
		t.Log("WARNING: No control ring overflow occurred")
	} else {
		t.Logf("SUCCESS: Triggered %d NAK fallbacks - race detector should have caught this", droppedNAKs)
	}
}

// TestRace_ControlRingOverflow_Combined tests both ACK and NAK overflow simultaneously.
// This is closest to the real-world scenario that triggered the panic.
func TestRace_ControlRingOverflow_Combined(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	// Use TINY control ring to force overflow
	const controlRingSize = 4
	const controlRingShards = 1

	s := NewSender(SendConfig{
		InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:            m,
		OnDeliver:                    func(p packet.Packet) {},
		StartTime:                    time.Now(),
		UseBtree:                     true,
		UseSendRing:                  true,
		SendRingSize:                 4096,
		SendRingShards:               1,
		UseSendControlRing:           true,
		SendControlRingSize:          controlRingSize,
		SendControlRingShards:        controlRingShards,
		UseSendEventLoop:             true,
		SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
		SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
		SendDropThresholdUs:          10_000_000,
		DropThreshold:                10_000_000,
	}).(*sender)

	s.nowFn = func() uint64 { return 1_000_000 }

	// Pre-populate btree
	for i := uint32(0); i < 1000; i++ {
		pkt := createTestPacketWithTsbpd(i, 0)
		s.packetBtree.Insert(pkt)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// EventLoop goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnterEventLoop()
		defer s.ExitEventLoop()

		for {
			select {
			case <-stop:
				return
			default:
				s.packetBtree.IterateFrom(0, func(p packet.Packet) bool {
					_ = p.Header()
					return true
				})
				s.deliverReadyPacketsEventLoop(s.nowFn())
				s.processControlPacketsDelta()
			}
		}
	}()

	// ACK flood goroutine
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
				ackSeq++
			}
		}
	}()

	// NAK flood goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		nakSeq := uint32(500) // Start in middle of btree
		for {
			select {
			case <-stop:
				return
			default:
				seqs := []circular.Number{
					circular.New(nakSeq, packet.MAX_SEQUENCENUMBER),
					circular.New(nakSeq, packet.MAX_SEQUENCENUMBER),
				}
				s.NAK(seqs)
				nakSeq = (nakSeq + 1) % 1000 // Cycle through btree
			}
		}
	}()

	// Let it run
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	droppedACKs := m.SendControlRingDroppedACK.Load()
	droppedNAKs := m.SendControlRingDroppedNAK.Load()
	t.Logf("Control ring dropped: ACKs=%d, NAKs=%d", droppedACKs, droppedNAKs)

	if droppedACKs == 0 && droppedNAKs == 0 {
		t.Log("WARNING: No control ring overflow occurred")
	} else {
		t.Logf("SUCCESS: Triggered %d ACK + %d NAK fallbacks", droppedACKs, droppedNAKs)
	}
}
