package receive

import (
	"net"
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
		"unknown",  // Default case
		"",         // Empty - default case
		"ADAPTIVE", // Case-insensitive
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

