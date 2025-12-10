package metrics

import (
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create circular numbers for testing
func seq(n uint32) circular.Number {
	return circular.New(n, 0x7FFFFFFF)
}

func TestCountNAKEntries_NilMetrics(t *testing.T) {
	list := []circular.Number{seq(5), seq(5)}
	total := CountNAKEntries(nil, list, NAKCounterSend)
	assert.Equal(t, uint64(0), total, "Should return 0 for nil metrics")
}

func TestCountNAKEntries_EmptyList(t *testing.T) {
	m := NewConnectionMetrics()
	total := CountNAKEntries(m, nil, NAKCounterSend)
	assert.Equal(t, uint64(0), total, "Should return 0 for empty list")

	total = CountNAKEntries(m, []circular.Number{}, NAKCounterSend)
	assert.Equal(t, uint64(0), total, "Should return 0 for empty list")
}

func TestCountNAKEntries_SinglePacket(t *testing.T) {
	// Single NAK entry: start == end means exactly 1 packet
	m := NewConnectionMetrics()
	list := []circular.Number{seq(42), seq(42)} // NAK for packet 42

	total := CountNAKEntries(m, list, NAKCounterSend)

	assert.Equal(t, uint64(1), total, "Single entry should request 1 packet")
	assert.Equal(t, uint64(1), m.CongestionRecvNAKSingle.Load(), "Single counter should be 1")
	assert.Equal(t, uint64(0), m.CongestionRecvNAKRange.Load(), "Range counter should be 0")
	assert.Equal(t, uint64(1), m.CongestionRecvNAKPktsTotal.Load(), "Total counter should be 1")
}

func TestCountNAKEntries_RangePackets(t *testing.T) {
	// Range NAK entry: start != end means multiple packets
	m := NewConnectionMetrics()
	// NAK for packets 10, 11, 12 (3 packets)
	list := []circular.Number{seq(10), seq(12)}

	total := CountNAKEntries(m, list, NAKCounterSend)

	assert.Equal(t, uint64(3), total, "Range should request 3 packets")
	assert.Equal(t, uint64(0), m.CongestionRecvNAKSingle.Load(), "Single counter should be 0")
	assert.Equal(t, uint64(3), m.CongestionRecvNAKRange.Load(), "Range counter should be 3")
	assert.Equal(t, uint64(3), m.CongestionRecvNAKPktsTotal.Load(), "Total counter should be 3")
}

func TestCountNAKEntries_MixedList(t *testing.T) {
	// Mix of single and range entries
	m := NewConnectionMetrics()
	list := []circular.Number{
		seq(5), seq(5), // Single: packet 5 (1 packet)
		seq(10), seq(12), // Range: packets 10, 11, 12 (3 packets)
		seq(20), seq(20), // Single: packet 20 (1 packet)
		seq(30), seq(31), // Range: packets 30, 31 (2 packets)
	}

	total := CountNAKEntries(m, list, NAKCounterSend)

	assert.Equal(t, uint64(7), total, "Total should be 1+3+1+2=7 packets")
	assert.Equal(t, uint64(2), m.CongestionRecvNAKSingle.Load(), "Should have 2 single entries")
	assert.Equal(t, uint64(5), m.CongestionRecvNAKRange.Load(), "Should have 3+2=5 range packets")
	assert.Equal(t, uint64(7), m.CongestionRecvNAKPktsTotal.Load(), "Total should match")
}

func TestCountNAKEntries_Invariant(t *testing.T) {
	// Verify invariant: Single + Range == Total
	m := NewConnectionMetrics()
	list := []circular.Number{
		seq(1), seq(1),
		seq(5), seq(8),
		seq(10), seq(10),
		seq(15), seq(20),
	}

	CountNAKEntries(m, list, NAKCounterSend)

	singles := m.CongestionRecvNAKSingle.Load()
	ranges := m.CongestionRecvNAKRange.Load()
	total := m.CongestionRecvNAKPktsTotal.Load()

	assert.Equal(t, singles+ranges, total,
		"Invariant violated: Single (%d) + Range (%d) != Total (%d)",
		singles, ranges, total)
}

func TestCountNAKEntries_RecvCounterType(t *testing.T) {
	// Verify NAKCounterRecv uses the correct counter set
	m := NewConnectionMetrics()
	list := []circular.Number{
		seq(5), seq(5), // Single (1)
		seq(10), seq(12), // Range (3)
	}

	total := CountNAKEntries(m, list, NAKCounterRecv)

	// Send counters should be unchanged
	assert.Equal(t, uint64(0), m.CongestionRecvNAKSingle.Load(), "Send single should be 0")
	assert.Equal(t, uint64(0), m.CongestionRecvNAKRange.Load(), "Send range should be 0")
	assert.Equal(t, uint64(0), m.CongestionRecvNAKPktsTotal.Load(), "Send total should be 0")

	// Recv counters should be set
	assert.Equal(t, uint64(4), total, "Total should be 4")
	assert.Equal(t, uint64(1), m.CongestionSendNAKSingleRecv.Load(), "Recv single should be 1")
	assert.Equal(t, uint64(3), m.CongestionSendNAKRangeRecv.Load(), "Recv range should be 3")
	assert.Equal(t, uint64(4), m.CongestionSendNAKPktsRecv.Load(), "Recv total should be 4")
}

func TestCountNAKEntries_Accumulates(t *testing.T) {
	// Verify multiple calls accumulate correctly
	m := NewConnectionMetrics()

	CountNAKEntries(m, []circular.Number{seq(1), seq(1)}, NAKCounterSend)   // 1 single
	CountNAKEntries(m, []circular.Number{seq(5), seq(7)}, NAKCounterSend)   // 3 range
	CountNAKEntries(m, []circular.Number{seq(10), seq(10)}, NAKCounterSend) // 1 single

	assert.Equal(t, uint64(2), m.CongestionRecvNAKSingle.Load(), "Should accumulate 2 singles")
	assert.Equal(t, uint64(3), m.CongestionRecvNAKRange.Load(), "Should accumulate 3 range packets")
	assert.Equal(t, uint64(5), m.CongestionRecvNAKPktsTotal.Load(), "Should accumulate 5 total")
}

func TestCountNAKEntries_ConsistencyBetweenSendAndRecv(t *testing.T) {
	// This is the KEY test: same NAK list should produce same packet counts
	// regardless of whether it's being sent or received
	list := []circular.Number{
		seq(1), seq(1), // Single (1)
		seq(5), seq(8), // Range (4)
		seq(10), seq(10), // Single (1)
		seq(15), seq(17), // Range (3)
	}

	// Simulate sender generating NAK
	sender := NewConnectionMetrics()
	sendTotal := CountNAKEntries(sender, list, NAKCounterSend)

	// Simulate receiver processing same NAK
	receiver := NewConnectionMetrics()
	recvTotal := CountNAKEntries(receiver, list, NAKCounterRecv)

	// Both should count the same number of packets
	require.Equal(t, sendTotal, recvTotal, "Send and recv should count same total")

	// And the individual counters should match (just in different fields)
	assert.Equal(t, sender.CongestionRecvNAKSingle.Load(),
		receiver.CongestionSendNAKSingleRecv.Load(),
		"Single counts should match")
	assert.Equal(t, sender.CongestionRecvNAKRange.Load(),
		receiver.CongestionSendNAKRangeRecv.Load(),
		"Range counts should match")
	assert.Equal(t, sender.CongestionRecvNAKPktsTotal.Load(),
		receiver.CongestionSendNAKPktsRecv.Load(),
		"Total counts should match")
}

// TestCountNAKEntries_EdgeCases tests boundary conditions
func TestCountNAKEntries_EdgeCases(t *testing.T) {
	t.Run("Adjacent packets in range", func(t *testing.T) {
		// Range of just 2 adjacent packets
		m := NewConnectionMetrics()
		list := []circular.Number{seq(10), seq(11)}
		total := CountNAKEntries(m, list, NAKCounterSend)

		assert.Equal(t, uint64(2), total, "Adjacent range should be 2 packets")
		assert.Equal(t, uint64(0), m.CongestionRecvNAKSingle.Load())
		assert.Equal(t, uint64(2), m.CongestionRecvNAKRange.Load())
	})

	t.Run("Large range", func(t *testing.T) {
		// Large range of 100 packets
		m := NewConnectionMetrics()
		list := []circular.Number{seq(0), seq(99)}
		total := CountNAKEntries(m, list, NAKCounterSend)

		assert.Equal(t, uint64(100), total, "Large range should count all packets")
		assert.Equal(t, uint64(100), m.CongestionRecvNAKRange.Load())
	})

	t.Run("Many single entries", func(t *testing.T) {
		// 10 separate single packet NAKs
		m := NewConnectionMetrics()
		list := make([]circular.Number, 20)
		for i := 0; i < 10; i++ {
			list[i*2] = seq(uint32(i * 10))
			list[i*2+1] = seq(uint32(i * 10))
		}
		total := CountNAKEntries(m, list, NAKCounterSend)

		assert.Equal(t, uint64(10), total, "10 singles should be 10 packets")
		assert.Equal(t, uint64(10), m.CongestionRecvNAKSingle.Load())
		assert.Equal(t, uint64(0), m.CongestionRecvNAKRange.Load())
	})
}
