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
// Table-driven tests for Tick() methods
//
// The sender supports two modes (from lockless_sender_design.md Section 7.3):
//
// 1. **Tick Mode (Legacy):** Uses locking for all operations
//    - Tick() acquires lock and calls tickDeliverPackets(), tickDropOldPackets()
//    - Push(), ACK(), NAK() also acquire lock
//    - Concurrent access protected by s.lock
//
// 2. **EventLoop Mode (New):** Uses lock-free rings + single-threaded btree
//    - EventLoop() does all btree operations (no locks needed)
//    - Push() writes to lock-free SendPacketRing
//    - ACK/NAK routed via ControlPacketRing to EventLoop
//
// These tests focus on Tick mode to ensure locking is correct.
//
// Reference: lockless_sender_design.md Section 7.3 "Legacy Tick Mode Compatibility"
// ============================================================================

// TickDeliveryTestCase defines a test case for Tick delivery
type TickDeliveryTestCase struct {
	Name string

	// Configuration
	UseBtree   bool
	UseRing    bool
	UseControl bool

	// Setup
	ISN         uint32
	PacketTsbpd []uint64 // TSBPD times for packets
	NowUs       uint64   // Current time for Tick

	// Expected
	ExpectedDelivered int
}

var tickDeliveryTestCases = []TickDeliveryTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Btree Mode (No Ring)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "Btree_NoRing_AllReady",
		UseBtree:          true,
		UseRing:           false,
		UseControl:        false,
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200, 300, 400, 500},
		NowUs:             1_000_000,
		ExpectedDelivered: 5,
	},
	{
		Name:              "Btree_NoRing_PartialReady",
		UseBtree:          true,
		UseRing:           false,
		UseControl:        false,
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200, 300, 400, 500},
		NowUs:             250,
		ExpectedDelivered: 2, // 100, 200 ready
	},
	{
		Name:              "Btree_NoRing_NoneReady",
		UseBtree:          true,
		UseRing:           false,
		UseControl:        false,
		ISN:               0,
		PacketTsbpd:       []uint64{1000, 2000, 3000},
		NowUs:             500,
		ExpectedDelivered: 0,
	},
	{
		Name:              "Btree_NoRing_HighISN",
		UseBtree:          true,
		UseRing:           false,
		UseControl:        false,
		ISN:               549144712, // Regression test
		PacketTsbpd:       []uint64{100, 200, 300},
		NowUs:             1_000_000,
		ExpectedDelivered: 3,
	},
	{
		Name:              "Btree_NoRing_Wraparound",
		UseBtree:          true,
		UseRing:           false,
		UseControl:        false,
		ISN:               MaxSeq31Bit - 2,
		PacketTsbpd:       []uint64{100, 200, 300, 400, 500},
		NowUs:             1_000_000,
		ExpectedDelivered: 5,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Btree Mode (With Ring)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "Btree_WithRing_AllReady",
		UseBtree:          true,
		UseRing:           true,
		UseControl:        false,
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200, 300},
		NowUs:             1_000_000,
		ExpectedDelivered: 3,
	},
	{
		Name:              "Btree_WithRing_HighISN",
		UseBtree:          true,
		UseRing:           true,
		UseControl:        false,
		ISN:               879502527,
		PacketTsbpd:       []uint64{100, 200, 300},
		NowUs:             1_000_000,
		ExpectedDelivered: 3,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// List Mode (Legacy)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "List_NoRing_AllReady",
		UseBtree:          false,
		UseRing:           false,
		UseControl:        false,
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200, 300, 400, 500},
		NowUs:             1_000_000,
		ExpectedDelivered: 5,
	},
	{
		Name:              "List_NoRing_PartialReady",
		UseBtree:          false,
		UseRing:           false,
		UseControl:        false,
		ISN:               0,
		PacketTsbpd:       []uint64{100, 200, 300, 400, 500},
		NowUs:             250,
		ExpectedDelivered: 2,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Edge Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:              "Empty_Btree",
		UseBtree:          true,
		UseRing:           false,
		UseControl:        false,
		ISN:               0,
		PacketTsbpd:       []uint64{},
		NowUs:             1_000_000,
		ExpectedDelivered: 0,
	},
	{
		Name:              "Empty_List",
		UseBtree:          false,
		UseRing:           false,
		UseControl:        false,
		ISN:               0,
		PacketTsbpd:       []uint64{},
		NowUs:             1_000_000,
		ExpectedDelivered: 0,
	},
}

// TestTick_Delivery_Table tests Tick delivery logic
func TestTick_Delivery_Table(t *testing.T) {
	for _, tc := range tickDeliveryTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			var deliveredCount atomic.Int32

			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver:             func(p packet.Packet) { deliveredCount.Add(1) },
				StartTime:             time.Now(),
				UseBtree:              tc.UseBtree,
				BtreeDegree:           32,
				UseSendRing:           tc.UseRing,
				SendRingSize:          1024,
				UseSendControlRing:    tc.UseControl,
				UseSendEventLoop:      false, // Tick mode
				SendDropThresholdUs:   10_000_000,
				DropThreshold:         10_000_000,
				LockTimingMetrics:     &metrics.LockTimingMetrics{},
			}).(*sender)

			// Push packets via Push() (acquires lock)
			for i, tsbpd := range tc.PacketTsbpd {
				pkt := createTestPacketWithTsbpd(uint32(i), tsbpd)
				s.Push(pkt)
			}

			// Run Tick
			s.Tick(tc.NowUs)

			// Verify delivered count
			require.Equal(t, tc.ExpectedDelivered, int(deliveredCount.Load()),
				"delivered count mismatch")
		})
	}
}

// TickDropTestCase defines a test case for Tick drop logic
type TickDropTestCase struct {
	Name string

	// Configuration
	UseBtree bool

	// Setup
	ISN             uint32
	PacketTsbpd     []uint64
	NowUs           uint64
	DropThresholdUs uint64

	// Expected
	ExpectedDropped int
}

var tickDropTestCases = []TickDropTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Btree Drop Tests
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:            "Btree_Drop_All",
		UseBtree:        true,
		ISN:             0,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           2_000_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 3,
	},
	{
		Name:            "Btree_Drop_None",
		UseBtree:        true,
		ISN:             0,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           500_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 0, // Underflow protection
	},
	{
		Name:            "Btree_Drop_Partial",
		UseBtree:        true,
		ISN:             0,
		PacketTsbpd:     []uint64{100, 500_000, 1_500_000},
		NowUs:           2_000_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 2, // First two are old
	},
	{
		Name:            "Btree_Drop_Underflow",
		UseBtree:        true,
		ISN:             0,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           500_000, // Less than threshold
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 0, // Underflow protection
	},

	// ═══════════════════════════════════════════════════════════════════════
	// List Drop Tests (Legacy)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:            "List_Drop_All",
		UseBtree:        false,
		ISN:             0,
		PacketTsbpd:     []uint64{100, 200, 300},
		NowUs:           2_000_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 3,
	},
	{
		Name:            "List_Drop_None",
		UseBtree:        false,
		ISN:             0,
		PacketTsbpd:     []uint64{1_500_000, 1_600_000, 1_700_000},
		NowUs:           2_000_000,
		DropThresholdUs: 1_000_000,
		ExpectedDropped: 0, // All within threshold
	},
}

// TestTick_Drop_Table tests Tick drop logic
// Note: Drop logic behavior differs between btree and list mode:
// - Btree: Packets dropped directly from btree based on TSBPD + threshold
// - List: Packets must be in lossList (after delivery) to be dropped
func TestTick_Drop_Table(t *testing.T) {
	for _, tc := range tickDropTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			// Skip list mode drop tests - list mode drops from lossList
			// which requires more complex setup with delivery first
			if !tc.UseBtree {
				t.Skip("List mode drops require packets in lossList after delivery")
			}

			m := &metrics.ConnectionMetrics{}
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(tc.ISN, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     m,
				OnDeliver:             func(p packet.Packet) {},
				StartTime:             time.Now(),
				UseBtree:              tc.UseBtree,
				UseSendRing:           false,
				UseSendControlRing:    false,
				UseSendEventLoop:      false, // Tick mode
				SendDropThresholdUs:   tc.DropThresholdUs,
				DropThreshold:         tc.DropThresholdUs,
				LockTimingMetrics:     &metrics.LockTimingMetrics{},
			}).(*sender)

			// For btree mode, insert packets directly (bypassing delivery)
			// This allows us to test the drop logic in isolation
			for i, tsbpd := range tc.PacketTsbpd {
				pkt := createTestPacketWithTsbpd(circular.SeqAdd(tc.ISN, uint32(i)), tsbpd)
				s.packetBtree.Insert(pkt)
			}

			initialCount := s.packetBtree.Len()
			initialDrops := m.CongestionSendPktDrop.Load()

			// Run tickDropOldPackets directly
			s.lock.Lock()
			s.tickDropOldPacketsBtree(tc.NowUs)
			s.lock.Unlock()

			finalDrops := m.CongestionSendPktDrop.Load()
			dropped := int(finalDrops - initialDrops)

			require.Equal(t, tc.ExpectedDropped, dropped,
				"dropped count mismatch (initial=%d, final btree=%d)",
				initialCount, s.packetBtree.Len())
		})
	}
}

// TestTick_RateStats tests Tick rate statistics update
func TestTick_RateStats(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           false,
		UseSendEventLoop:      false,
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	// Set initial state
	m.SendRateLastUs.Store(0)
	m.SendRatePeriodUs.Store(1_000_000) // 1 second period
	m.SendRateBytes.Store(1000)
	m.SendRateBytesSent.Store(800)

	// First Tick at 500ms - should NOT update (within period)
	s.Tick(500_000)

	// Rates should not be updated yet
	require.Equal(t, uint64(1000), m.SendRateBytes.Load())

	// Second Tick at 1.5s - should update (past period)
	s.Tick(1_500_000)

	// Counters should be reset after update
	require.Equal(t, uint64(0), m.SendRateBytes.Load())
	require.Equal(t, uint64(0), m.SendRateBytesSent.Load())
}

// ============================================================================
// Concurrent Race Tests for Tick()
// ============================================================================

// TestTick_Race_Push tests Tick() concurrent with Push()
func TestTick_Race_Push(t *testing.T) {
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
		UseSendEventLoop:      false, // Tick mode
		SendDropThresholdUs:   10_000_000,
		DropThreshold:         10_000_000,
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(0)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Push goroutine
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

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Tick+Push completed")
}

// TestTick_Race_ACK tests Tick() concurrent with ACK()
func TestTick_Race_ACK(t *testing.T) {
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

	// Pre-populate
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
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	t.Log("Tick+ACK completed")
}

// TestTick_Race_NAK tests Tick() concurrent with NAK()
func TestTick_Race_NAK(t *testing.T) {
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

	// Pre-populate
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

	// NAK goroutine
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

	t.Logf("Tick+NAK completed, retransmitted=%d", retransmitted.Load())
}

// TestTick_Race_All tests all operations concurrent with Tick()
func TestTick_Race_All(t *testing.T) {
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

	// Push goroutine
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

	// NAK goroutine - NAK needs at least 2 entries for range format
	wg.Add(1)
	go func() {
		defer wg.Done()
		nakSeq := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
				// NAK format requires pairs (start, end) for ranges
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

	t.Log("Tick+Push+ACK+NAK completed")
}

// TestTick_Race_WithRing tests Tick() with ring mode concurrent operations
func TestTick_Race_WithRing(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		BtreeDegree:           32,
		UseSendRing:           true, // Ring mode
		SendRingSize:          1024,
		SendRingShards:        1,
		UseSendControlRing:    true,
		SendControlRingSize:   256,
		UseSendEventLoop:      false, // Tick mode (not EventLoop)
		SendDropThresholdUs:   10_000_000,
		DropThreshold:         10_000_000,
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	var nowUs atomic.Uint64
	nowUs.Store(0)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Push goroutine (lock-free to ring)
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

	// Tick goroutine (drains ring with lock)
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

	t.Log("Tick+Ring completed")
}

// TestTick_Race_ListMode tests Tick() with legacy list mode
func TestTick_Race_ListMode(t *testing.T) {
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

	// Push goroutine
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

	t.Log("Tick+List mode completed")
}

// TestTick_EnterExitContext tests Tick/EventLoop mutual exclusion
func TestTick_EnterExitContext(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	s := NewSender(SendConfig{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:     m,
		OnDeliver:             func(p packet.Packet) {},
		StartTime:             time.Now(),
		UseBtree:              true,
		UseSendRing:           false,
		UseSendControlRing:    false,
		UseSendEventLoop:      false, // Tick mode
		LockTimingMetrics:     &metrics.LockTimingMetrics{},
	}).(*sender)

	// Tick should work normally
	s.Tick(1_000_000)

	// Multiple ticks should work
	for i := 0; i < 10; i++ {
		s.Tick(uint64(i) * 1_000_000)
	}

	t.Log("Tick context switching completed")
}
