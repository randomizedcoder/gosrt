package live

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Core Scan Function Tests - UNIQUE TESTS ONLY
// Duplicated tests moved to core_scan_table_test.go
// ═══════════════════════════════════════════════════════════════════════════

// createScanTestReceiver creates a minimal receiver for scan testing
func createScanTestReceiver(t *testing.T, startSeq uint32) *receiver {
	testMetrics := &metrics.ConnectionMetrics{}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000, // 10ms
		PeriodicNAKInterval:    20_000, // 20ms
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",
		TsbpdDelay:             120_000, // 120ms
		NakRecentPercent:       0.10,
	}

	recv := NewReceiver(recvConfig)
	return recv.(*receiver)
}

// createScanTestPacket creates a packet with given sequence number
func createScanTestPacket(t *testing.T, seq uint32, tsbpdTime uint64) packet.Packet {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = tsbpdTime
	return p
}

// ═══════════════════════════════════════════════════════════════════════════
// Unique Test: Empty Btree with Stale ContiguousPoint
// This test is NOT covered by table-driven tests because it requires
// testing the empty btree → new packets scenario specifically
// ═══════════════════════════════════════════════════════════════════════════

func TestContiguousScan_StaleContiguousPoint_EmptyBtree(t *testing.T) {
	// Scenario: contiguousPoint=100, all packets delivered, btree empty
	// Then new packets 300+ arrive (gap of 200 > threshold 64)
	//
	// This simulates: All packets up to 299 delivered via TSBPD
	// New burst of packets starting at 300

	recv := createScanTestReceiver(t, 100)
	recv.contiguousPoint.Store(100)

	// Btree is empty initially
	ok, ackSeq := recv.contiguousScan()
	require.False(t, ok, "Empty btree should return ok=false")
	require.Equal(t, uint32(0), ackSeq)

	// Now new packets arrive (gap of 200 >= threshold 64)
	for _, seq := range []uint32{300, 301, 302} {
		p := createScanTestPacket(t, seq, 1_000_000)
		recv.packetStore.Insert(p)
	}

	ok, ackSeq = recv.contiguousScan()

	t.Logf("After adding packets 300-302: ok=%v, ackSeq=%d, contiguousPoint=%d",
		ok, ackSeq, recv.contiguousPoint.Load())

	require.True(t, ok, "Should handle large gap and make progress")
	require.Equal(t, uint32(303), ackSeq, "Should ACK to 303")
	require.Equal(t, uint32(302), recv.contiguousPoint.Load())
}
