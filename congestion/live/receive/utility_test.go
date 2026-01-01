package receive

import (
	"net"
	"runtime"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Tests for utility functions with 0% coverage
// These are simple getters/setters/debug methods, but testing ensures coverage
// ============================================================================

func TestPacketRate(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	recv := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Call PacketRate - should return values from metrics
	pps, bps, capacity := recv.PacketRate()

	// Initial values should be 0
	require.GreaterOrEqual(t, pps, float64(0), "pps should be >= 0")
	require.GreaterOrEqual(t, bps, float64(0), "bps should be >= 0")
	require.GreaterOrEqual(t, capacity, float64(0), "capacity should be >= 0")

	t.Logf("PacketRate: pps=%.2f, bps=%.2f, capacity=%.2f", pps, bps, capacity)
}

func TestUseEventLoop(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	// Test with EventLoop disabled
	recv1 := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		UseEventLoop:          false,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	require.False(t, recv1.UseEventLoop(), "UseEventLoop should return false when disabled")

	// Test with EventLoop enabled
	recv2 := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		UseEventLoop:          true,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	require.True(t, recv2.UseEventLoop(), "UseEventLoop should return true when enabled")
}

func TestSetNAKInterval(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	recv := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000, // Initial value
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Verify initial value
	require.Equal(t, uint64(20_000), recv.periodicNAKInterval)

	// Set new value
	recv.SetNAKInterval(50_000)

	require.Equal(t, uint64(50_000), recv.periodicNAKInterval,
		"SetNAKInterval should update periodicNAKInterval")

	// Set to 0 (edge case)
	recv.SetNAKInterval(0)
	require.Equal(t, uint64(0), recv.periodicNAKInterval,
		"SetNAKInterval should allow 0")
}

func TestString(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	recv := New(Config{
		InitialSequenceNumber: circular.New(100, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Test with empty packet store
	str := recv.String(0)
	require.Contains(t, str, "maxSeen=", "String should contain maxSeen")
	require.Contains(t, str, "lastACK=", "String should contain lastACK")
	require.Contains(t, str, "contiguousPoint=", "String should contain contiguousPoint")
	t.Logf("String (empty): %s", str)
}

func TestDrainAllFromRing(t *testing.T) {
	// Test with nil ring (no crash)
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	recv := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		UsePacketRing:         false, // No ring
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Should not crash when ring is nil
	recv.drainAllFromRing()
	t.Log("drainAllFromRing with nil ring completed without crash")
}

func TestDeliverReadyPacketsNoLock(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	recv := New(Config{
		InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   10_000,
		PeriodicNAKInterval:   20_000,
		TsbpdDelay:            500_000,
		UseNakBtree:           true,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             func(list []circular.Number) {},
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
	}).(*receiver)

	// Test empty store
	delivered := recv.deliverReadyPacketsNoLock(1_000_000)
	require.Equal(t, 0, delivered, "Should deliver 0 packets from empty store")
}

func TestParseRetryStrategy(t *testing.T) {
	// Test all retry strategy strings to ensure coverage of switch cases
	// The actual enum values come from the go-lock-free-ring package
	testCases := []string{
		"spin",
		"yield",
		"spinthenyield",
		"backoff",
		"sleepbackoff",
		"sleep",
		"adaptive",
		"adaptivebackoff",
		"hybrid",
		"next",
		"nextshard",
		"random",
		"randomshard",
		"unknown",   // Default case
		"",          // Empty - default case
		"ADAPTIVE",  // Case-insensitive
		"  hybrid ", // With whitespace
	}

	for _, input := range testCases {
		input := input
		t.Run(input, func(t *testing.T) {
			result := parseRetryStrategy(input)
			t.Logf("parseRetryStrategy(%q) = %d", input, result)
			// Just verify it doesn't panic and returns a valid enum
			require.GreaterOrEqual(t, int(result), 0, "Should return valid enum value >= 0")
		})
	}
}

// ============================================================================
// Test for insertAndUpdateMetrics helper (consolidates Insert + metrics pattern)
// ============================================================================

func TestInsertAndUpdateMetrics(t *testing.T) {
	t.Run("UniquePacket_Success", func(t *testing.T) {
		testMetrics := &metrics.ConnectionMetrics{
			HandlePacketLockTiming: &metrics.LockTimingMetrics{},
			ReceiverLockTiming:     &metrics.LockTimingMetrics{},
			SenderLockTiming:       &metrics.LockTimingMetrics{},
		}

		recv := New(Config{
			InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
			PeriodicACKInterval:   10_000,
			PeriodicNAKInterval:   20_000,
			TsbpdDelay:            500_000,
			UseNakBtree:           true,
			OnSendACK:             func(seq circular.Number, light bool) {},
			OnSendNAK:             func(list []circular.Number) {},
			OnDeliver:             func(p packet.Packet) {},
			ConnectionMetrics:     testMetrics,
		}).(*receiver)

		addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		pktLen := p.Len()

		// Initial metrics should be 0
		require.Equal(t, uint64(0), testMetrics.CongestionRecvPktUnique.Load())

		// Insert unique packet
		inserted := recv.insertAndUpdateMetrics(p, pktLen, false, false)

		require.True(t, inserted, "Unique packet should be inserted")
		require.Equal(t, uint64(1), testMetrics.CongestionRecvPktUnique.Load())
		require.Equal(t, pktLen, testMetrics.CongestionRecvByteUnique.Load())
		require.Equal(t, uint64(1), testMetrics.CongestionRecvPktBuf.Load())
	})

	t.Run("DuplicatePacket_Rejected", func(t *testing.T) {
		testMetrics := &metrics.ConnectionMetrics{
			HandlePacketLockTiming: &metrics.LockTimingMetrics{},
			ReceiverLockTiming:     &metrics.LockTimingMetrics{},
			SenderLockTiming:       &metrics.LockTimingMetrics{},
		}

		recv := New(Config{
			InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
			PeriodicACKInterval:   10_000,
			PeriodicNAKInterval:   20_000,
			TsbpdDelay:            500_000,
			UseNakBtree:           true,
			OnSendACK:             func(seq circular.Number, light bool) {},
			OnSendNAK:             func(list []circular.Number) {},
			OnDeliver:             func(p packet.Packet) {},
			ConnectionMetrics:     testMetrics,
		}).(*receiver)

		addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

		// Insert first packet
		p1 := packet.NewPacket(addr)
		p1.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		inserted1 := recv.insertAndUpdateMetrics(p1, p1.Len(), false, false)
		require.True(t, inserted1, "First packet should be inserted")

		// Insert duplicate with same sequence number
		p2 := packet.NewPacket(addr)
		p2.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		inserted2 := recv.insertAndUpdateMetrics(p2, p2.Len(), false, false)

		require.False(t, inserted2, "Duplicate packet should be rejected")
		require.Equal(t, uint64(1), testMetrics.CongestionRecvPktStoreInsertFailed.Load())
	})

	t.Run("RetransmittedPacket_UpdatesRetransMetrics", func(t *testing.T) {
		testMetrics := &metrics.ConnectionMetrics{
			HandlePacketLockTiming: &metrics.LockTimingMetrics{},
			ReceiverLockTiming:     &metrics.LockTimingMetrics{},
			SenderLockTiming:       &metrics.LockTimingMetrics{},
		}

		recv := New(Config{
			InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
			PeriodicACKInterval:   10_000,
			PeriodicNAKInterval:   20_000,
			TsbpdDelay:            500_000,
			UseNakBtree:           true,
			OnSendACK:             func(seq circular.Number, light bool) {},
			OnSendNAK:             func(list []circular.Number) {},
			OnDeliver:             func(p packet.Packet) {},
			ConnectionMetrics:     testMetrics,
		}).(*receiver)

		addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		p.Header().RetransmittedPacketFlag = true
		pktLen := p.Len()

		inserted := recv.insertAndUpdateMetrics(p, pktLen, true /* isRetransmit */, false)

		require.True(t, inserted)
		require.Equal(t, uint64(1), testMetrics.CongestionRecvPktRetrans.Load())
		require.Equal(t, pktLen, testMetrics.CongestionRecvByteRetrans.Load())
	})

	t.Run("DrainMetricFlag_UpdatesRingDrainedPackets", func(t *testing.T) {
		testMetrics := &metrics.ConnectionMetrics{
			HandlePacketLockTiming: &metrics.LockTimingMetrics{},
			ReceiverLockTiming:     &metrics.LockTimingMetrics{},
			SenderLockTiming:       &metrics.LockTimingMetrics{},
		}

		recv := New(Config{
			InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
			PeriodicACKInterval:   10_000,
			PeriodicNAKInterval:   20_000,
			TsbpdDelay:            500_000,
			UseNakBtree:           true,
			OnSendACK:             func(seq circular.Number, light bool) {},
			OnSendNAK:             func(list []circular.Number) {},
			OnDeliver:             func(p packet.Packet) {},
			ConnectionMetrics:     testMetrics,
		}).(*receiver)

		addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)

		// With drain metric flag
		inserted := recv.insertAndUpdateMetrics(p, p.Len(), false, true /* updateDrainMetric */)

		require.True(t, inserted)
		require.Equal(t, uint64(1), testMetrics.RingDrainedPackets.Load())
	})
}

// TestBtreeInsertDuplicateReturnsOldPacketForPoolRelease verifies that when a duplicate
// packet is inserted into the btree, the OLD packet (that was in the tree) is returned
// for proper sync.Pool release, NOT the new packet.
//
// This test validates the fix in packet_store_btree.go where we now keep the new packet
// in the tree and return the old packet for decommissioning (single traversal optimization).
func TestBtreeInsertDuplicateReturnsOldPacketForPoolRelease(t *testing.T) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create btree store
	store := NewBTreePacketStore(32)

	// Create first packet (will stay in tree on duplicate insert)
	p1 := packet.NewPacket(addr)
	p1.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
	// Mark p1 with a distinctive timestamp so we can identify it
	p1.Header().Timestamp = 111111

	// Insert first packet
	inserted1, dupPkt1 := store.Insert(p1)
	require.True(t, inserted1, "First insert should succeed")
	require.Nil(t, dupPkt1, "No duplicate on first insert")

	// Create second packet with same sequence number (duplicate)
	p2 := packet.NewPacket(addr)
	p2.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
	// Mark p2 with different timestamp to distinguish from p1
	p2.Header().Timestamp = 222222

	// Insert duplicate - should return the OLD packet (p1) for release
	inserted2, dupPkt2 := store.Insert(p2)
	require.False(t, inserted2, "Duplicate insert should return false")
	require.NotNil(t, dupPkt2, "Should return duplicate packet for release")

	// CRITICAL: Verify we got the OLD packet back (p1), not the new one (p2)
	// After the fix, p2 stays in tree and p1 is returned for decommissioning
	require.Equal(t, uint32(111111), dupPkt2.Header().Timestamp,
		"Should return OLD packet (timestamp 111111), not new packet (timestamp 222222)")

	// Verify the NEW packet (p2) is now in the tree
	storedPkt := store.Min()
	require.NotNil(t, storedPkt, "Tree should have a packet")
	require.Equal(t, uint32(222222), storedPkt.Header().Timestamp,
		"NEW packet (timestamp 222222) should be in tree")

	// Verify we can safely decommission the returned old packet
	// This should NOT panic and should return the packet to the sync.Pool
	dupPkt2.Decommission()

	// After decommission, the packet's payload should be nil (returned to pool)
	// Note: We can't directly check p1's state since it's now returned to pool,
	// but the test passing without panic confirms proper pool return
	t.Log("Successfully decommissioned old packet - returned to sync.Pool")
}

// TestBtreeInsertDuplicateMetricsAndPoolReturn tests the full flow through
// insertAndUpdateMetrics, verifying both metrics are updated AND the packet
// is properly returned to the sync.Pool.
func TestBtreeInsertDuplicateMetricsAndPoolReturn(t *testing.T) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	recv := New(Config{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000,
		PeriodicNAKInterval:    20_000,
		TsbpdDelay:             500_000,
		PacketReorderAlgorithm: "btree", // Use btree, not list!
		BTreeDegree:            32,
		UseNakBtree:            true,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
	}).(*receiver)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Insert first packet
	p1 := packet.NewPacket(addr)
	p1.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
	p1.Header().Timestamp = 111111
	inserted1 := recv.insertAndUpdateMetrics(p1, p1.Len(), false, false)
	require.True(t, inserted1, "First packet should be inserted")

	// Insert duplicate
	p2 := packet.NewPacket(addr)
	p2.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
	p2.Header().Timestamp = 222222
	inserted2 := recv.insertAndUpdateMetrics(p2, p2.Len(), false, false)

	require.False(t, inserted2, "Duplicate should be rejected")

	// Verify metrics were updated
	require.Equal(t, uint64(1), testMetrics.CongestionRecvPktStoreInsertFailed.Load(),
		"Store insert failed metric should be incremented")
	require.Equal(t, uint64(1), testMetrics.CongestionRecvPktDuplicate.Load(),
		"Duplicate packet metric should be incremented")

	// Verify the NEW packet is in the tree (not the old one)
	storedPkt := recv.packetStore.Min()
	require.NotNil(t, storedPkt)
	require.Equal(t, uint32(222222), storedPkt.Header().Timestamp,
		"NEW packet should be in tree after duplicate insert")

	t.Log("Duplicate packet correctly handled: metrics updated, OLD packet released to pool, NEW packet in tree")
}

// TestListVsBtreeDuplicateBehavior documents the behavioral difference between
// list and btree packet stores on duplicate insertion:
// - List:  Keeps OLD packet in store, returns NEW for release (reject duplicate)
// - Btree: Keeps NEW packet in store, returns OLD for release (single traversal optimization)
//
// Both are correct since duplicate packets have identical data - only the
// packet struct identity differs.
func TestListVsBtreeDuplicateBehavior(t *testing.T) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	t.Run("List_KeepsOldPacket", func(t *testing.T) {
		store := NewListPacketStore()

		p1 := packet.NewPacket(addr)
		p1.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		p1.Header().Timestamp = 111111
		store.Insert(p1)

		p2 := packet.NewPacket(addr)
		p2.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		p2.Header().Timestamp = 222222
		_, dupPkt := store.Insert(p2)

		// List returns NEW packet for release, keeps OLD in store
		require.Equal(t, uint32(222222), dupPkt.Header().Timestamp,
			"List should return NEW packet for release")
		require.Equal(t, uint32(111111), store.Min().Header().Timestamp,
			"List should keep OLD packet in store")

		dupPkt.Decommission()
	})

	t.Run("Btree_KeepsNewPacket", func(t *testing.T) {
		store := NewBTreePacketStore(32)

		p1 := packet.NewPacket(addr)
		p1.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		p1.Header().Timestamp = 111111
		store.Insert(p1)

		p2 := packet.NewPacket(addr)
		p2.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		p2.Header().Timestamp = 222222
		_, dupPkt := store.Insert(p2)

		// Btree returns OLD packet for release, keeps NEW in store (single traversal)
		require.Equal(t, uint32(111111), dupPkt.Header().Timestamp,
			"Btree should return OLD packet for release")
		require.Equal(t, uint32(222222), store.Min().Header().Timestamp,
			"Btree should keep NEW packet in store")

		dupPkt.Decommission()
	})

	t.Log("Both implementations correctly handle duplicates - just different packet kept")
}

// BenchmarkDuplicatePacketPoolReturn benchmarks duplicate packet handling
// and verifies memory allocations are stable (sync.Pool reuse working).
func BenchmarkDuplicatePacketPoolReturn(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	store := NewBTreePacketStore(32)

	// Pre-populate with a packet at seq 100
	initial := packet.NewPacket(addr)
	initial.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
	store.Insert(initial)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Create duplicate packet
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)

		// Insert duplicate - returns OLD packet for release
		_, dupPkt := store.Insert(p)

		// Release to pool (simulates what insertAndUpdateMetrics does)
		if dupPkt != nil {
			dupPkt.Decommission()
		}
	}
}

// BenchmarkMixedPacketPoolReturn benchmarks realistic packet flow with
// mostly unique packets and occasional duplicates (1% duplicate rate).
func BenchmarkMixedPacketPoolReturn(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	store := NewBTreePacketStore(32)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// 1% duplicate rate - every 100th packet is a duplicate of seq 50
		var seqNum uint32
		if i%100 == 0 && i > 0 {
			seqNum = 50 // Duplicate
		} else {
			seqNum = uint32(i)
		}

		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seqNum, packet.MAX_SEQUENCENUMBER)

		inserted, dupPkt := store.Insert(p)

		if !inserted && dupPkt != nil {
			// Duplicate - release to pool
			dupPkt.Decommission()
		}
		// Note: unique packets stay in btree (not released here)
	}
}

// TestMemoryStabilityWithDuplicates verifies that memory doesn't grow
// when processing many duplicate packets (sync.Pool is working).
func TestMemoryStabilityWithDuplicates(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory stability test in short mode")
	}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	recv := New(Config{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000,
		PeriodicNAKInterval:    20_000,
		TsbpdDelay:             500_000,
		PacketReorderAlgorithm: "btree",
		BTreeDegree:            32,
		UseNakBtree:            true,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
	}).(*receiver)

	// Insert initial packet at seq 100
	initial := packet.NewPacket(addr)
	initial.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
	recv.insertAndUpdateMetrics(initial, initial.Len(), false, false)

	// Force GC to establish baseline
	runtime.GC()
	var baselineStats runtime.MemStats
	runtime.ReadMemStats(&baselineStats)
	baselineAlloc := baselineStats.HeapAlloc

	// Insert many duplicates
	const iterations = 100_000
	for i := 0; i < iterations; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
		recv.insertAndUpdateMetrics(p, p.Len(), false, false)
	}

	// Force GC and check memory
	runtime.GC()
	var finalStats runtime.MemStats
	runtime.ReadMemStats(&finalStats)
	finalAlloc := finalStats.HeapAlloc

	// Calculate growth
	var growth int64
	if finalAlloc > baselineAlloc {
		growth = int64(finalAlloc - baselineAlloc)
	} else {
		growth = -int64(baselineAlloc - finalAlloc)
	}

	t.Logf("Processed %d duplicate packets", iterations)
	t.Logf("Baseline heap: %d bytes", baselineAlloc)
	t.Logf("Final heap:    %d bytes", finalAlloc)
	t.Logf("Growth:        %d bytes (%.2f bytes/packet)", growth, float64(growth)/float64(iterations))
	t.Logf("Duplicates detected: %d", testMetrics.CongestionRecvPktDuplicate.Load())

	// Memory should not grow significantly (allow some slack for runtime overhead)
	// If sync.Pool is working, growth should be near-zero or negative after GC
	maxAllowedGrowthPerPacket := float64(10) // bytes per packet - very generous
	maxAllowedGrowth := int64(maxAllowedGrowthPerPacket * float64(iterations))

	if growth > maxAllowedGrowth {
		t.Errorf("Memory grew too much: %d bytes (max allowed: %d). Possible memory leak!", growth, maxAllowedGrowth)
	} else {
		t.Logf("✅ Memory stable - sync.Pool is working correctly")
	}
}

// TestMemoryStabilityMixedPackets verifies memory stability with a realistic
// mix of unique packets (99%) and duplicates (1%).
// This simulates real-world conditions where duplicates are rare but do occur.
func TestMemoryStabilityMixedPackets(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory stability test in short mode")
	}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}

	recv := New(Config{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000,
		PeriodicNAKInterval:    20_000,
		TsbpdDelay:             500_000,
		PacketReorderAlgorithm: "btree",
		BTreeDegree:            32,
		UseNakBtree:            true,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
	}).(*receiver)

	const totalPackets = 100_000
	const duplicateEvery = 100 // 1% duplicate rate
	const duplicateSeq = 50    // Fixed sequence for duplicates

	// Force GC to establish baseline
	runtime.GC()
	var baselineStats runtime.MemStats
	runtime.ReadMemStats(&baselineStats)
	baselineAlloc := baselineStats.HeapAlloc

	uniqueCount := 0
	dupCount := 0

	for i := 0; i < totalPackets; i++ {
		p := packet.NewPacket(addr)

		// Every Nth packet is a duplicate of seq 50
		if i%duplicateEvery == 0 && i > 0 {
			p.Header().PacketSequenceNumber = circular.New(duplicateSeq, packet.MAX_SEQUENCENUMBER)
			dupCount++
		} else {
			p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
			uniqueCount++
		}

		recv.insertAndUpdateMetrics(p, p.Len(), false, false)
	}

	// Force GC and check memory
	runtime.GC()
	var finalStats runtime.MemStats
	runtime.ReadMemStats(&finalStats)
	finalAlloc := finalStats.HeapAlloc

	// Calculate growth
	var growth int64
	if finalAlloc > baselineAlloc {
		growth = int64(finalAlloc - baselineAlloc)
	} else {
		growth = -int64(baselineAlloc - finalAlloc)
	}

	detectedDups := testMetrics.CongestionRecvPktDuplicate.Load()

	t.Logf("Processed %d total packets:", totalPackets)
	t.Logf("  - Unique:     %d", uniqueCount)
	t.Logf("  - Duplicates: %d (expected %d)", dupCount, totalPackets/duplicateEvery-1)
	t.Logf("Baseline heap: %d bytes", baselineAlloc)
	t.Logf("Final heap:    %d bytes", finalAlloc)
	t.Logf("Growth:        %d bytes", growth)
	t.Logf("Duplicates detected by metrics: %d", detectedDups)

	// Verify duplicate detection
	expectedDups := uint64(totalPackets/duplicateEvery - 1) // First one at seq 50 is unique
	if detectedDups != expectedDups {
		t.Errorf("Expected %d duplicates detected, got %d", expectedDups, detectedDups)
	}

	// Memory WILL grow for unique packets (they stay in btree), but should be bounded
	// Expected: ~uniqueCount * (packetItem size) = ~99000 * ~64 bytes = ~6MB for btree items
	// Packets themselves should be pooled

	// Check that growth is reasonable (not runaway)
	// Allow ~100 bytes per unique packet for btree overhead
	maxExpectedGrowth := int64(uniqueCount) * 100
	if growth > maxExpectedGrowth {
		t.Errorf("Memory grew too much: %d bytes (max expected: %d). Possible leak!", growth, maxExpectedGrowth)
	} else {
		t.Logf("✅ Memory growth reasonable for %d unique packets in btree", uniqueCount)
	}

	t.Logf("Unique packets stay in btree (expected growth)")
	t.Logf("Duplicate packets returned to pool (no extra growth)")
}
