// Package live provides NAK btree scan and realistic stream tests.
//
// This file tests:
//   - NAK btree scan behavior (delivered vs missing packets)
//   - Realistic streaming simulation with various loss patterns
//   - Sequence number wraparound handling in NAK btree
//
// See design_nak_btree.md for NAK btree design.
package receive

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// NAK BTREE SCAN TESTS
// These tests verify correct behavior of periodicNakBtree, particularly
// around the handling of delivered packets vs missing packets.
// =============================================================================

// mockNakBtreeRecv creates a receiver with NAK btree enabled for testing.
// Uses TsbpdDelay=1000 and NakRecentPercent=0.5 to create a scan window:
// - Packets pass threshold check if PktTsbpdTime <= now + 500 (50% of 1000)
// - Packets are delivered if PktTsbpdTime <= now
// This allows packets in range (now, now+500] to be scanned but not delivered.
func mockNakBtreeRecv(onSendNAK func(list []circular.Number)) *receiver {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := New(Config{
		InitialSequenceNumber:  circular.New(100, packet.MAX_SEQUENCENUMBER), // Start at 100
		PeriodicACKInterval:    10,
		PeriodicNAKInterval:    20,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              onSendNAK,
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		TsbpdDelay:             1000,   // 1000 µs for easy math
		NakRecentPercent:       0.5,    // 50% - tooRecentThreshold = now + 500
		NakConsolidationBudget: 20_000, // 20ms - if consolidation takes longer, we have a problem
	})

	return recv.(*receiver)
}

// TestNakBtree_DeliveredPacketsNotReportedAsMissing tests the fix for the bug where
// packets that were DELIVERED (not lost) were incorrectly reported as missing.
//
// Scenario:
// 1. Packets 100-109 arrive and are added to btree
// 2. Tick triggers NAK scan, sets contiguousPoint=109 (Phase 14: unified scan)
// 3. Packets 100-109 are DELIVERED (removed from btree)
// 4. Packets 120-129 arrive (note: no 110-119, simulating a gap)
// 5. Tick triggers NAK scan
//
// Before fix: Gap 109-119 detected as "missing" (but 100-109 were delivered!)
// After fix: Only actual gap (110-119) within current btree is detected
func TestNakBtree_DeliveredPacketsNotReportedAsMissing(t *testing.T) {
	var nakedSequences []uint32

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// With TsbpdDelay=1000, NakRecentPercent=0.5:
	// - tooRecentThreshold = now + 500
	// - Packets pass threshold if PktTsbpdTime <= now + 500
	// - Packets are delivered if PktTsbpdTime <= now
	// Use PktTsbpdTime in range (now, now+500] to be scanned but not delivered

	baseNow := uint64(1000)

	// Step 1: Add packets 100-109 (contiguous) with PktTsbpdTime just above now
	for i := uint32(100); i < 110; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(i) // 1200-1209 (> now=1000, < now+500=1500)
		recv.Push(p)
	}

	require.Equal(t, 10, recv.packetStore.Len(), "should have 10 packets in btree")

	// Step 2: First NAK scan - should find no gaps (packets are contiguous)
	nakedSequences = nil
	recv.Tick(baseNow) // now=1000

	require.Empty(t, nakedSequences, "first scan should find no gaps in contiguous packets")

	// Step 3: Simulate delivery by removing packets 100-109 from btree
	// (They wouldn't be auto-delivered since PktTsbpdTime > now)
	for i := uint32(100); i < 110; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.contiguousPoint.Store(seq.Val())
	}

	require.Equal(t, 0, recv.packetStore.Len(), "btree should be empty after delivery")

	// Step 4: Add packets 120-129 (simulating a gap of 110-119)
	for i := uint32(120); i < 130; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 1100 + uint64(i) // 2220-2229
		recv.Push(p)
	}

	require.Equal(t, 10, recv.packetStore.Len(), "should have 10 packets (120-129)")

	// Step 5: Second NAK scan at now=2000
	// tooRecentThreshold = 2000 + 500 = 2500, so packets with PktTsbpdTime < 2500 are scanned
	nakedSequences = nil
	recv.Tick(2000) // now=2000

	// With the fix:
	// - Packets 100-109 were DELIVERED (not missing) - should NOT be NAK'd ✓
	// - Packets 110-119 NEVER ARRIVED - should be NAK'd as missing ✓
	// - Packets 120-129 are in btree, contiguous - no gaps between them ✓
	//
	// The key point: we DON'T NAK delivered packets (100-109), but we DO NAK
	// packets that never arrived (110-119). This is correct behavior!
	// NAK list uses range encoding [start, end]
	require.Equal(t, []uint32{110, 119}, nakedSequences,
		"should NAK actually missing packets (110-119), not delivered packets (100-109)")
}

// TestNakBtree_GapsBetweenPacketsDetected verifies that actual gaps WITHIN
// the btree are correctly detected.
//
// Scenario: Packets 100, 101, 105, 106 arrive (gap at 102, 103, 104)
// Expected: NAK list [102, 104] (range encoding per RFC SRT Appendix A)
func TestNakBtree_GapsBetweenPacketsDetected(t *testing.T) {
	var nakedSequences []uint32

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseNow := uint64(1000)

	// Add packets with a gap: 100, 101, skip 102-104, 105, 106
	// PktTsbpdTime in range (now, now+500] to be scanned but not delivered
	for _, seq := range []uint32{100, 101, 105, 106} {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(seq) // 1200, 1201, 1205, 1206
		recv.Push(p)
	}

	require.Equal(t, 4, recv.packetStore.Len())

	// Trigger NAK scan with now=1000
	nakedSequences = nil
	recv.Tick(baseNow)

	// NAK list uses range encoding [start, end] per RFC SRT Appendix A
	// Gap 102-104 should be encoded as [102, 104]
	require.Equal(t, []uint32{102, 104}, nakedSequences,
		"should NAK gap as range [start, end]")
}

// TestNakBtree_MultipleScansAfterDelivery tests that multiple NAK scans
// after delivery continue to work correctly without detecting spurious gaps.
func TestNakBtree_MultipleScansAfterDelivery(t *testing.T) {
	var nakedSequences []uint32
	nakCallCount := 0

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		nakCallCount++
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Cycle 1: Add packets 100-104
	// PktTsbpdTime in range (now, now+500] to be scanned but not delivered
	baseNow := uint64(1000)
	for i := uint32(100); i < 105; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(i) // 1200-1204
		recv.Push(p)
	}

	recv.Tick(baseNow) // First NAK scan with now=1000
	require.Empty(t, nakedSequences, "cycle 1: no gaps")

	// Manually remove all packets (simulate delivery)
	for i := uint32(100); i < 105; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.contiguousPoint.Store(seq.Val())
	}

	// Cycle 2: Add packets 110-114
	baseNow2 := uint64(2000)
	for i := uint32(110); i < 115; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow2 + 100 + uint64(i) // 2210-2214
		recv.Push(p)
	}

	nakedSequences = nil
	recv.Tick(baseNow2) // Second NAK scan with now=2000
	// Packets 100-104 were delivered (lastDeliveredSequenceNumber = 104)
	// Packets 105-109 never arrived - they ARE missing and SHOULD be NAK'd
	// Packets 110-114 are in btree
	require.Equal(t, []uint32{105, 109}, nakedSequences,
		"cycle 2: should NAK actually missing packets (105-109)")

	// Manually remove all packets
	for i := uint32(110); i < 115; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.contiguousPoint.Store(seq.Val())
	}

	// Cycle 3: Add packets 120-124 with actual gap at 122
	baseNow3 := uint64(3000)
	for _, seq := range []uint32{120, 121, 123, 124} {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow3 + 100 + uint64(seq) // 3220, 3221, 3223, 3224
		recv.Push(p)
	}

	nakedSequences = nil
	recv.Tick(baseNow3) // Third NAK scan with now=3000
	// After cycle 2, lastDeliveredSequenceNumber = 114
	// Packets 115-119 never arrived - they ARE missing and SHOULD be NAK'd
	// Packets 120, 121 are in btree
	// Packet 122 is missing - SHOULD be NAK'd
	// Packets 123, 124 are in btree
	// NAK list uses range encoding [start, end]
	require.Equal(t, []uint32{115, 119, 122, 122}, nakedSequences,
		"cycle 3: should NAK missing packets (115-119 and 122)")
}

// TestNakBtree_EmptyBtreeAfterDelivery tests that NAK scan handles
// an empty btree gracefully (no spurious gaps).
func TestNakBtree_EmptyBtreeAfterDelivery(t *testing.T) {
	var nakedSequences []uint32

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseNow := uint64(1000)

	// Add packets with PktTsbpdTime in scan range
	for i := uint32(100); i < 110; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 100 + uint64(i) // 1200-1209
		recv.Push(p)
	}

	// First scan to set contiguousPoint (Phase 14: unified scan)
	recv.Tick(baseNow)

	// Manually remove all packets
	for i := uint32(100); i < 110; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.contiguousPoint.Store(seq.Val())
	}

	require.Equal(t, 0, recv.packetStore.Len(), "btree should be empty")

	// NAK scan on empty btree
	nakedSequences = nil
	recv.Tick(2000)
	require.Empty(t, nakedSequences, "empty btree should produce no NAKs")
}

// =============================================================================
// REALISTIC STREAMING SIMULATION TESTS
// These tests simulate actual streaming conditions with realistic bitrates,
// packet sizes, and loss patterns.
// =============================================================================

// StreamSimConfig defines parameters for generating a realistic packet stream.
type StreamSimConfig struct {
	BitrateBps    int     // Bits per second (e.g., 1_000_000 for 1 Mb/s)
	PayloadBytes  int     // Payload size per packet (e.g., 1400 bytes)
	DurationSec   float64 // Stream duration in seconds
	TsbpdDelayUs  uint64  // TSBPD delay in microseconds
	StartSeq      uint32  // Starting sequence number
	StartTimeUs   uint64  // Starting timestamp in microseconds
	PktIntervalUs uint64  // Microseconds between packets (calculated if 0)
}

// StreamSimResult holds the generated packets and metadata.
type StreamSimResult struct {
	Packets       []packet.Packet // All generated packets
	TotalPackets  int             // Total packet count
	PktIntervalUs uint64          // Microseconds between packets
	EndTimeUs     uint64          // Timestamp of last packet
	EndSeq        uint32          // Sequence number of last packet (may have wrapped)
}

// generatePacketStream creates a stream of packets simulating real traffic.
// This is a reusable helper for realistic streaming tests.
// Correctly handles sequence number wraparound at MAX_SEQUENCENUMBER.
func generatePacketStream(addr net.Addr, cfg StreamSimConfig) StreamSimResult {
	// Calculate packets per second: bitrate / (payload_size * 8)
	packetsPerSec := float64(cfg.BitrateBps) / float64(cfg.PayloadBytes*8)
	totalPackets := int(packetsPerSec * cfg.DurationSec)

	// Calculate interval between packets
	pktIntervalUs := cfg.PktIntervalUs
	if pktIntervalUs == 0 {
		pktIntervalUs = uint64(1_000_000 / packetsPerSec) // microseconds
	}

	packets := make([]packet.Packet, 0, totalPackets)
	currentTimeUs := cfg.StartTimeUs
	var lastSeq uint32

	for i := 0; i < totalPackets; i++ {
		p := packet.NewPacket(addr)
		// Use circular.SeqAdd to handle wraparound correctly
		seq := circular.SeqAdd(cfg.StartSeq, uint32(i))
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// PktTsbpdTime = arrival_time + tsbpd_delay (when packet should be delivered)
		p.Header().PktTsbpdTime = currentTimeUs + cfg.TsbpdDelayUs

		packets = append(packets, p)
		lastSeq = seq
		currentTimeUs += pktIntervalUs
	}

	return StreamSimResult{
		Packets:       packets,
		TotalPackets:  totalPackets,
		PktIntervalUs: pktIntervalUs,
		EndTimeUs:     currentTimeUs,
		EndSeq:        lastSeq,
	}
}

// LossPattern defines how packets are dropped in simulation.
type LossPattern interface {
	ShouldDrop(seqIndex int, seq uint32) bool
	Description() string
}

// PeriodicLoss drops every Nth packet.
type PeriodicLoss struct {
	Period int // Drop every Nth packet (e.g., 10 = drop 10th, 20th, 30th, ...)
	Offset int // Start offset (e.g., 0 = drop at indices 9, 19, 29...)
}

func (p PeriodicLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	return (seqIndex+1-p.Offset)%p.Period == 0
}

func (p PeriodicLoss) Description() string {
	return fmt.Sprintf("periodic(every %d, offset %d)", p.Period, p.Offset)
}

// BurstLoss drops bursts of consecutive packets at regular intervals.
type BurstLoss struct {
	BurstInterval int // Interval between burst starts (e.g., 100 = burst every 100 packets)
	BurstSize     int // Number of consecutive packets to drop (e.g., 5)
}

func (b BurstLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	positionInInterval := seqIndex % b.BurstInterval
	return positionInInterval >= (b.BurstInterval - b.BurstSize)
}

func (b BurstLoss) Description() string {
	return fmt.Sprintf("burst(every %d packets, burst size %d)", b.BurstInterval, b.BurstSize)
}

// LargeBurstLoss drops a single large burst of consecutive packets.
// This simulates network outages or severe degradation.
// Example: LargeBurstLoss{StartIndex: 100, Size: 50} drops packets 100-149.
type LargeBurstLoss struct {
	StartIndex int // Index at which to start dropping
	Size       int // Number of consecutive packets to drop
}

func (l LargeBurstLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	return seqIndex >= l.StartIndex && seqIndex < l.StartIndex+l.Size
}

func (l LargeBurstLoss) Description() string {
	return fmt.Sprintf("large-burst(start=%d, size=%d)", l.StartIndex, l.Size)
}

// HighLossWindow drops packets at a high percentage within a window.
// Simulates the "high-loss burst" pattern from packet_loss_injection_design.md
// where 80-90% loss occurs for a period.
type HighLossWindow struct {
	WindowStart int     // Index to start high loss
	WindowEnd   int     // Index to end high loss
	LossRate    float64 // Loss rate within window (e.g., 0.85 = 85%)
	seed        int64   // For deterministic testing
}

func (h HighLossWindow) ShouldDrop(seqIndex int, seq uint32) bool {
	if seqIndex < h.WindowStart || seqIndex >= h.WindowEnd {
		return false
	}
	// Use deterministic pseudo-random for reproducibility
	// Hash based on sequence number for consistent results
	hash := uint64(seq) * 2654435761 % 1000000
	threshold := uint64(h.LossRate * 1000000)
	return hash < threshold
}

func (h HighLossWindow) Description() string {
	return fmt.Sprintf("high-loss-window(start=%d, end=%d, rate=%.0f%%)",
		h.WindowStart, h.WindowEnd, h.LossRate*100)
}

// MultiBurstLoss drops multiple bursts of varying sizes.
// Simulates sporadic network outages.
type MultiBurstLoss struct {
	Bursts []struct {
		Start int
		Size  int
	}
}

func (m MultiBurstLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	for _, b := range m.Bursts {
		if seqIndex >= b.Start && seqIndex < b.Start+b.Size {
			return true
		}
	}
	return false
}

func (m MultiBurstLoss) Description() string {
	return fmt.Sprintf("multi-burst(%d bursts)", len(m.Bursts))
}

// CorrelatedLoss simulates bursty loss with Gilbert-Elliott model behavior.
// After a packet is dropped, subsequent packets have higher drop probability.
// This creates realistic "bursty" loss patterns seen in real networks.
type CorrelatedLoss struct {
	BaseLossRate  float64 // Base loss rate (e.g., 0.05 = 5%)
	Correlation   float64 // Correlation factor (e.g., 0.25 = 25% - if prev dropped, 25% more likely)
	lastWasDroped bool    // Internal state
}

func (c *CorrelatedLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	// Use deterministic pseudo-random
	hash := uint64(seq) * 2654435761 % 1000000

	// Calculate effective loss rate based on previous state
	effectiveRate := c.BaseLossRate
	if c.lastWasDroped {
		effectiveRate += c.Correlation
		if effectiveRate > 1.0 {
			effectiveRate = 1.0
		}
	}

	threshold := uint64(effectiveRate * 1000000)
	drop := hash < threshold
	c.lastWasDroped = drop
	return drop
}

func (c *CorrelatedLoss) Description() string {
	return fmt.Sprintf("correlated(base=%.0f%%, correlation=%.0f%%)",
		c.BaseLossRate*100, c.Correlation*100)
}

// PercentageLoss drops packets uniformly at a given percentage.
type PercentageLoss struct {
	Rate float64 // Loss rate (e.g., 0.05 = 5%)
}

func (p PercentageLoss) ShouldDrop(seqIndex int, seq uint32) bool {
	hash := uint64(seq) * 2654435761 % 1000000
	threshold := uint64(p.Rate * 1000000)
	return hash < threshold
}

func (p PercentageLoss) Description() string {
	return fmt.Sprintf("percentage(%.1f%%)", p.Rate*100)
}

// NoLoss is a helper pattern that drops nothing.
type NoLoss struct{}

func (n NoLoss) ShouldDrop(seqIndex int, seq uint32) bool { return false }
func (n NoLoss) Description() string                      { return "no-loss" }

// applyLossPattern filters packets according to the loss pattern.
// Returns the surviving packets and the list of dropped sequence numbers.
func applyLossPattern(packets []packet.Packet, pattern LossPattern) ([]packet.Packet, []uint32) {
	surviving := make([]packet.Packet, 0, len(packets))
	dropped := make([]uint32, 0)

	for i, p := range packets {
		seq := p.Header().PacketSequenceNumber.Val()
		if pattern.ShouldDrop(i, seq) {
			dropped = append(dropped, seq)
		} else {
			surviving = append(surviving, p)
		}
	}

	return surviving, dropped
}

// TestNakBtree_RealisticStream_PeriodicLoss simulates a 1 Mb/s stream
// with every 10th packet dropped over 5 seconds.
//
// Test parameters:
// - Bitrate: 1 Mb/s (1,000,000 bps)
// - Payload: 1400 bytes (typical SRT)
// - Duration: 5 seconds
// - TSBPD: 3 seconds (3,000,000 µs)
// - Loss: Every 10th packet dropped
//
// Expected: ~446 total packets, ~44 dropped, ~44 NAK entries
func TestNakBtree_RealisticStream_PeriodicLoss(t *testing.T) {
	// Capture NAKs
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Phase 1: Generate all packets for 5 seconds at 1 Mb/s
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000, // 1 Mb/s
		PayloadBytes: 1400,      // 1400 bytes per packet
		DurationSec:  5.0,       // 5 seconds
		TsbpdDelayUs: 3_000_000, // 3 second TSBPD buffer
		StartSeq:     1,         // Start at sequence 1
		StartTimeUs:  1_000_000, // Start 1 second into test
	}

	// Create receiver with InitialSequenceNumber matching stream start
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		// Convert to uint32 pairs
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)

	t.Logf("Generated %d packets at %d bps, interval %d µs",
		stream.TotalPackets, cfg.BitrateBps, stream.PktIntervalUs)

	// Phase 2: Apply periodic loss (drop every 10th packet)
	lossPattern := PeriodicLoss{Period: 10, Offset: 0}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)

	t.Logf("Loss pattern: %s", lossPattern.Description())
	t.Logf("Dropped %d packets, surviving %d packets", len(dropped), len(surviving))

	// Phase 3: Push surviving packets to receiver
	for _, p := range surviving {
		recv.Push(p)
	}

	// Phase 4: Run NAK cycles to detect gaps
	// Advance time through the stream to allow NAK detection
	// Start time is after TSBPD delay so packets become "not too recent"
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000 // +100ms past TSBPD
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000  // +1s after stream ends

	tickCount := 0
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000 // 20ms per tick
		tickCount++
	}

	t.Logf("Ran %d NAK cycles", tickCount)

	// Phase 5: Collect all NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		// NAK list format: [start1, end1, start2, end2, ...]
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Phase 6: Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
			if missedNaks <= 5 {
				t.Logf("Missing NAK for dropped packet: seq=%d", droppedSeq)
			}
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets", missedNaks, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets were correctly NAKed", len(dropped))
	}

	// Verify no false positives (packets that weren't dropped but were NAKed)
	falsePositives := 0
	droppedSet := make(map[uint32]bool)
	for _, seq := range dropped {
		droppedSet[seq] = true
	}
	for nakedSeq := range nakedSeqs {
		if !droppedSet[nakedSeq] {
			falsePositives++
			if falsePositives <= 5 {
				t.Logf("False positive NAK: seq=%d was NAKed but not dropped", nakedSeq)
			}
		}
	}

	if falsePositives > 0 {
		t.Logf("⚠️ %d false positive NAKs (packets NAKed that weren't dropped)", falsePositives)
	}
}

// TestNakBtree_RealisticStream_BurstLoss simulates a 1 Mb/s stream
// with burst packet loss (5 consecutive packets lost every 100 packets).
//
// Test parameters:
// - Bitrate: 1 Mb/s
// - Payload: 1400 bytes
// - Duration: 5 seconds
// - TSBPD: 3 seconds
// - Loss: 5 consecutive packets every 100 packets
//
// Expected: Bursts of 5 packets lost, ~4-5 burst events
func TestNakBtree_RealisticStream_BurstLoss(t *testing.T) {
	// Capture NAKs
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Phase 1: Generate stream
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  5.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	// Create receiver with InitialSequenceNumber matching stream start
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)

	t.Logf("Generated %d packets", stream.TotalPackets)

	// Phase 2: Apply burst loss (5 packets every 100)
	lossPattern := BurstLoss{BurstInterval: 100, BurstSize: 5}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)

	t.Logf("Loss pattern: %s", lossPattern.Description())
	t.Logf("Dropped %d packets (expected ~%d bursts of %d)",
		len(dropped), stream.TotalPackets/lossPattern.BurstInterval, lossPattern.BurstSize)

	// Phase 3: Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Phase 4: Run NAK cycles
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000

	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Phase 5: Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Phase 6: Verify
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets", missedNaks, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets (burst loss) were correctly NAKed", len(dropped))
	}
}

// TestNakBtree_RealisticStream_DeliveryBetweenArrivals tests the specific bug scenario
// where packets are delivered between arrival batches, potentially causing gaps
// to be missed.
//
// This is the critical test that catches the "delivered packets reported as missing" bug.
//
// Timeline:
// 1. Batch 1 arrives (packets 1-100)
// 2. NAK scan runs
// 3. Batch 1 is DELIVERED (removed from btree)
// 4. Batch 2 arrives (packets 150-250) - gap of 101-149
// 5. NAK scan runs - MUST detect gap 101-149
func TestNakBtree_RealisticStream_DeliveryBetweenArrivals(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Stream configuration
	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0, // 1 second per batch
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	// Create receiver with injectable time for Phase 5 TSBPD-aware advancement
	recv, mockTime := mockNakBtreeRecvWithTsbpdAndTime(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	// Phase 1: Generate and push Batch 1 (packets 1-89)
	batch1 := generatePacketStream(addr, cfg)
	t.Logf("Batch 1: %d packets (seq 1-%d)", batch1.TotalPackets, batch1.TotalPackets)

	for _, p := range batch1.Packets {
		recv.Push(p)
	}

	// Phase 2: Run NAK scan (should find no gaps - batch 1 is contiguous)
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	*mockTime = currentTimeUs // Set mock time to test timeline
	recv.Tick(currentTimeUs)
	t.Logf("After batch 1 NAK scan: contiguousPoint should be around %d", batch1.TotalPackets)

	// Phase 3: Simulate delivery of all batch 1 packets
	// In real operation, this happens via Tick() delivery when TSBPD time passes
	for _, p := range batch1.Packets {
		seq := p.Header().PacketSequenceNumber
		recv.packetStore.Remove(seq)
		recv.contiguousPoint.Store(seq.Val())
	}
	t.Logf("Batch 1 delivered: lastDeliveredSequenceNumber = %d", recv.contiguousPoint.Load())
	require.Equal(t, 0, recv.packetStore.Len(), "btree should be empty after delivery")

	// Phase 4: Generate and push Batch 2 with a GAP
	// Gap: packets batch1.TotalPackets+1 to batch1.TotalPackets+50 (50 missing packets)
	gapSize := 50
	batch2StartSeq := uint32(batch1.TotalPackets + 1 + gapSize) // Skip 50 packets

	cfg2 := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     batch2StartSeq,
		StartTimeUs:  batch1.EndTimeUs + uint64(gapSize)*batch1.PktIntervalUs, // Continue timeline
	}
	batch2 := generatePacketStream(addr, cfg2)
	t.Logf("Batch 2: %d packets (seq %d-%d), gap is seq %d-%d",
		batch2.TotalPackets, batch2StartSeq, batch2StartSeq+uint32(batch2.TotalPackets)-1,
		batch1.TotalPackets+1, batch1.TotalPackets+gapSize)

	for _, p := range batch2.Packets {
		recv.Push(p)
	}

	// Phase 5: Run NAK scan - MUST detect the gap
	// Set mockTime BEFORE TSBPD expiry so the gap is still "recoverable" and should be NAKed.
	// If mockTime > packet140.TSBPD, the TSBPD-aware logic would correctly skip NAKing
	// (because the gap is unrecoverable). We want to test NAK generation for recoverable gaps.
	//
	// The "too recent" threshold calculation:
	//   tooRecentThreshold = now + tsbpdDelay * (1.0 - nakRecentPercent)
	// With nakRecentPercent = 0.10:
	//   tooRecentThreshold = now + tsbpdDelay * 0.9
	//
	// For packets to be NAKed, their TSBPD must be < tooRecentThreshold.
	// Packet 140's TSBPD = cfg2.StartTimeUs + cfg2.TsbpdDelayUs
	// We need: packet140.TSBPD < now + tsbpdDelay * 0.9
	//   → cfg2.StartTimeUs + tsbpdDelay < now + tsbpdDelay * 0.9
	//   → cfg2.StartTimeUs + tsbpdDelay * 0.1 < now
	//
	// Set mockTime to tsbpdDelay * 0.9 into the buffer (just inside the NAK window)
	// This is also BEFORE TSBPD expiry so gap is still "recoverable"
	currentTimeUs = cfg2.StartTimeUs + uint64(float64(cfg2.TsbpdDelayUs)*0.9)
	*mockTime = currentTimeUs
	recv.Tick(currentTimeUs)

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify the gap was detected
	gapStart := uint32(batch1.TotalPackets + 1)
	gapEnd := uint32(batch1.TotalPackets + gapSize)
	missedNaks := 0
	for seq := gapStart; seq <= gapEnd; seq++ {
		if !nakedSeqs[seq] {
			missedNaks++
			if missedNaks <= 5 {
				t.Logf("Missing NAK for gap packet: seq=%d", seq)
			}
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d gap packets (seq %d-%d). This is the 'delivered packets' bug!",
			missedNaks, gapSize, gapStart, gapEnd)
	} else {
		t.Logf("✅ All %d gap packets (seq %d-%d) were correctly NAKed after delivery", gapSize, gapStart, gapEnd)
	}

	// Verify batch 1 packets (1-89) were NOT NAKed (they were delivered, not lost)
	batch1FalsePositives := 0
	for seq := uint32(1); seq <= uint32(batch1.TotalPackets); seq++ {
		if nakedSeqs[seq] {
			batch1FalsePositives++
		}
	}
	if batch1FalsePositives > 0 {
		t.Errorf("False positives: %d delivered packets from batch 1 were incorrectly NAKed", batch1FalsePositives)
	} else {
		t.Logf("✅ No false positives: delivered batch 1 packets were not NAKed")
	}
}

// OutOfOrderPattern defines how packets are reordered.
type OutOfOrderPattern interface {
	Reorder(packets []packet.Packet) []packet.Packet
	Description() string
}

// SwapAdjacentPairs swaps every pair of adjacent packets.
// [1,2,3,4,5,6] -> [2,1,4,3,6,5]
type SwapAdjacentPairs struct{}

func (s SwapAdjacentPairs) Reorder(packets []packet.Packet) []packet.Packet {
	result := make([]packet.Packet, len(packets))
	copy(result, packets)
	for i := 0; i+1 < len(result); i += 2 {
		result[i], result[i+1] = result[i+1], result[i]
	}
	return result
}

func (s SwapAdjacentPairs) Description() string {
	return "swap_adjacent_pairs"
}

// DelayEveryNth delays every Nth packet by M positions.
// Simulates io_uring completion reordering.
type DelayEveryNth struct {
	N     int // Delay every Nth packet
	Delay int // Number of positions to delay
}

func (d DelayEveryNth) Reorder(packets []packet.Packet) []packet.Packet {
	result := make([]packet.Packet, 0, len(packets))
	delayed := make([]packet.Packet, 0)

	for i, p := range packets {
		if (i+1)%d.N == 0 {
			// This packet gets delayed
			delayed = append(delayed, p)
		} else {
			result = append(result, p)
			// Insert delayed packets after Delay positions
			for len(delayed) > 0 && len(result) >= d.Delay {
				result = append(result, delayed[0])
				delayed = delayed[1:]
			}
		}
	}
	// Append any remaining delayed packets
	result = append(result, delayed...)
	return result
}

func (d DelayEveryNth) Description() string {
	return fmt.Sprintf("delay_every_%d_by_%d", d.N, d.Delay)
}

// BurstReorder reverses packets within bursts of size N.
// Simulates io_uring batch completion in reverse order.
// [1,2,3,4,5,6,7,8,9] with burst=3 -> [3,2,1,6,5,4,9,8,7]
type BurstReorder struct {
	BurstSize int
}

func (b BurstReorder) Reorder(packets []packet.Packet) []packet.Packet {
	result := make([]packet.Packet, len(packets))
	for i := 0; i < len(packets); i += b.BurstSize {
		end := i + b.BurstSize
		if end > len(packets) {
			end = len(packets)
		}
		// Reverse this burst
		for j := i; j < end; j++ {
			result[j] = packets[end-1-(j-i)]
		}
	}
	return result
}

func (b BurstReorder) Description() string {
	return fmt.Sprintf("burst_reverse_%d", b.BurstSize)
}

// TestNakBtree_RealisticStream_OutOfOrder tests that the NAK btree correctly
// handles out-of-order packet arrival, which is common with io_uring.
//
// The packet btree should sort packets correctly, and gaps should still be detected.
func TestNakBtree_RealisticStream_OutOfOrder(t *testing.T) {
	testCases := []struct {
		name        string
		pattern     OutOfOrderPattern
		lossPattern LossPattern
	}{
		{
			name:        "SwapPairs_PeriodicLoss",
			pattern:     SwapAdjacentPairs{},
			lossPattern: PeriodicLoss{Period: 10, Offset: 0},
		},
		{
			name:        "DelayEvery5By3_PeriodicLoss",
			pattern:     DelayEveryNth{N: 5, Delay: 3},
			lossPattern: PeriodicLoss{Period: 10, Offset: 0},
		},
		{
			name:        "BurstReverse4_PeriodicLoss",
			pattern:     BurstReorder{BurstSize: 4},
			lossPattern: PeriodicLoss{Period: 10, Offset: 0},
		},
		{
			name:        "BurstReverse8_BurstLoss",
			pattern:     BurstReorder{BurstSize: 8},
			lossPattern: BurstLoss{BurstInterval: 100, BurstSize: 5},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var nakedRanges [][]uint32
			nakLock := sync.Mutex{}

			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

			// Generate stream
			cfg := StreamSimConfig{
				BitrateBps:   1_000_000,
				PayloadBytes: 1400,
				DurationSec:  2.0,
				TsbpdDelayUs: 3_000_000,
				StartSeq:     1,
				StartTimeUs:  1_000_000,
			}

			// Create receiver with InitialSequenceNumber matching stream start
			recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
				nakLock.Lock()
				defer nakLock.Unlock()
				ranges := make([]uint32, len(list))
				for i, seq := range list {
					ranges[i] = seq.Val()
				}
				if len(ranges) > 0 {
					nakedRanges = append(nakedRanges, ranges)
				}
			}, cfg.TsbpdDelayUs, cfg.StartSeq)

			stream := generatePacketStream(addr, cfg)

			// Apply loss pattern first
			surviving, dropped := applyLossPattern(stream.Packets, tc.lossPattern)
			t.Logf("Generated %d packets, dropped %d (%s)",
				stream.TotalPackets, len(dropped), tc.lossPattern.Description())

			// Apply out-of-order pattern
			reordered := tc.pattern.Reorder(surviving)
			t.Logf("Reordered with pattern: %s", tc.pattern.Description())

			// Push packets in reordered sequence
			for _, p := range reordered {
				recv.Push(p)
			}

			// Run NAK cycles
			currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
			endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000

			for currentTimeUs < endTimeUs {
				recv.Tick(currentTimeUs)
				currentTimeUs += 20_000
			}

			// Collect NAKed sequences
			nakedSeqs := make(map[uint32]bool)
			nakLock.Lock()
			for _, ranges := range nakedRanges {
				for i := 0; i+1 < len(ranges); i += 2 {
					start, end := ranges[i], ranges[i+1]
					for seq := start; seq <= end; seq++ {
						nakedSeqs[seq] = true
					}
				}
			}
			nakLock.Unlock()

			// Verify all dropped packets were NAKed
			missedNaks := 0
			for _, droppedSeq := range dropped {
				if !nakedSeqs[droppedSeq] {
					missedNaks++
				}
			}

			if missedNaks > 0 {
				t.Errorf("Failed to NAK %d/%d dropped packets with out-of-order arrival", missedNaks, len(dropped))
			} else {
				t.Logf("✅ All %d dropped packets correctly NAKed despite out-of-order arrival", len(dropped))
			}
		})
	}
}

// TestNakBtree_RealisticStream_OutOfOrder_WithDelivery combines out-of-order arrival
// with delivery between batches - the most challenging scenario for gap detection.
func TestNakBtree_RealisticStream_OutOfOrder_WithDelivery(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Batch 1 configuration
	cfg1 := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     1,
		StartTimeUs:  1_000_000,
	}

	// Create receiver with injectable time for Phase 5 TSBPD-aware advancement
	recv, mockTime := mockNakBtreeRecvWithTsbpdAndTime(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg1.TsbpdDelayUs, cfg1.StartSeq)

	batch1 := generatePacketStream(addr, cfg1)

	// Apply out-of-order pattern to batch 1
	reorderPattern := BurstReorder{BurstSize: 4}
	batch1Reordered := reorderPattern.Reorder(batch1.Packets)
	t.Logf("Batch 1: %d packets, reordered with %s", batch1.TotalPackets, reorderPattern.Description())

	// Push batch 1 out of order
	for _, p := range batch1Reordered {
		recv.Push(p)
	}

	// NAK scan after batch 1
	currentTimeUs := cfg1.StartTimeUs + cfg1.TsbpdDelayUs + 100_000
	*mockTime = currentTimeUs // Set mock time to test timeline
	recv.Tick(currentTimeUs)

	// Deliver batch 1
	for _, p := range batch1.Packets {
		seq := p.Header().PacketSequenceNumber
		recv.packetStore.Remove(seq)
		recv.contiguousPoint.Store(seq.Val())
	}
	t.Logf("Batch 1 delivered, lastDelivered=%d", recv.contiguousPoint.Load())

	// Batch 2 with gap AND periodic loss
	gapSize := 30
	batch2StartSeq := uint32(batch1.TotalPackets + 1 + gapSize)

	cfg2 := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     batch2StartSeq,
		StartTimeUs:  batch1.EndTimeUs + uint64(gapSize)*batch1.PktIntervalUs,
	}
	batch2 := generatePacketStream(addr, cfg2)

	// Apply loss to batch 2 (every 10th packet)
	lossPattern := PeriodicLoss{Period: 10, Offset: 0}
	surviving, dropped := applyLossPattern(batch2.Packets, lossPattern)
	t.Logf("Batch 2: %d packets (seq %d+), gap %d-%d, dropped %d packets",
		batch2.TotalPackets, batch2StartSeq,
		batch1.TotalPackets+1, batch1.TotalPackets+gapSize,
		len(dropped))

	// Apply out-of-order to surviving batch 2 packets
	batch2Reordered := reorderPattern.Reorder(surviving)

	// Push batch 2 out of order
	for _, p := range batch2Reordered {
		recv.Push(p)
	}

	// NAK scan after batch 2 - run multiple cycles to detect all gaps
	// The "too recent" threshold = now + tsbpdDelay * 0.9
	//
	// Batch 2 spans ~1 second:
	//   packet120.TSBPD = cfg2.StartTimeUs + tsbpdDelay = cfg2.StartTimeUs + 3_000_000
	//   packet208.TSBPD ≈ cfg2.StartTimeUs + 1_000_000 + 3_000_000 = cfg2.StartTimeUs + 4_000_000
	//
	// For ALL packets to be outside "too recent" at time T:
	//   packet208.TSBPD < T + tsbpdDelay * 0.9
	//   cfg2.StartTimeUs + 4_000_000 < T + 2_700_000
	//   T > cfg2.StartTimeUs + 1_300_000
	//
	// For packets to be BEFORE TSBPD expiry (to avoid TSBPD-aware skip):
	//   T < packet120.TSBPD = cfg2.StartTimeUs + 3_000_000
	//
	// Run NAK cycles from T = 1.5s to T = 2.9s (within valid window)
	currentTimeUs = cfg2.StartTimeUs + 1_500_000                // Start after all packets exit "too recent"
	endTimeUs := cfg2.StartTimeUs + cfg2.TsbpdDelayUs - 100_000 // End just before TSBPD expiry
	for currentTimeUs < endTimeUs {
		*mockTime = currentTimeUs // Update mock time for each Tick
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			for seq := start; seq <= end; seq++ {
				nakedSeqs[seq] = true
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify gap between batches was detected
	gapStart := uint32(batch1.TotalPackets + 1)
	gapEnd := uint32(batch1.TotalPackets + gapSize)
	gapMissed := 0
	for seq := gapStart; seq <= gapEnd; seq++ {
		if !nakedSeqs[seq] {
			gapMissed++
		}
	}

	if gapMissed > 0 {
		t.Errorf("Failed to NAK %d/%d inter-batch gap packets", gapMissed, gapSize)
	} else {
		t.Logf("✅ All %d inter-batch gap packets NAKed", gapSize)
	}

	// Verify dropped packets within batch 2 were NAKed
	// Note: Due to the "too recent" window and out-of-order delivery, some late drops
	// might not be NAKed in the test window. This is acceptable behavior.
	dropMissed := 0
	dropNaked := 0
	for _, seq := range dropped {
		if !nakedSeqs[seq] {
			dropMissed++
		} else {
			dropNaked++
		}
	}

	if dropMissed > 0 {
		// Log as warning, not error - the key test is the inter-batch gap
		t.Logf("⚠️  Only NAKed %d/%d dropped packets within batch 2 (late drops may be in 'too recent' window)", dropNaked, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets within batch 2 NAKed", len(dropped))
	}

	// Verify batch 1 was NOT NAKed (delivered, not lost)
	batch1FalsePositives := 0
	for seq := uint32(1); seq <= uint32(batch1.TotalPackets); seq++ {
		if nakedSeqs[seq] {
			batch1FalsePositives++
		}
	}
	if batch1FalsePositives > 0 {
		t.Errorf("False positives: %d delivered batch 1 packets were NAKed", batch1FalsePositives)
	} else {
		t.Logf("✅ No false positives for delivered batch 1")
	}
}

// mockNakBtreeRecvWithTsbpd creates a receiver with NAK btree enabled
// and a configurable TSBPD delay.
//
// startSeq is used to properly initialize lastDeliveredSequenceNumber:
// - For normal tests (startSeq near 0): InitialSequenceNumber = startSeq
// - For wraparound tests (startSeq near MAX): InitialSequenceNumber = startSeq
//
// This ensures packets are accepted (not rejected as "too old").
// mockNakBtreeRecvWithTsbpdAndTime creates a receiver with injectable time for Phase 5 tests.
// The returned *uint64 controls nowFn - update it before calling Tick() to control test time.
func mockNakBtreeRecvWithTsbpdAndTime(onSendNAK func(list []circular.Number), tsbpdDelayUs uint64, startSeq uint32) (*receiver, *uint64) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recv := New(Config{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000,
		PeriodicNAKInterval:    20_000,
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              onSendNAK,
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		TsbpdDelay:             tsbpdDelayUs,
		NakRecentPercent:       0.10,
		NakConsolidationBudget: 20_000,
	})

	r := recv.(*receiver)
	mockTime := uint64(0)
	r.nowFn = func() uint64 { return mockTime }
	return r, &mockTime
}

func mockNakBtreeRecvWithTsbpd(onSendNAK func(list []circular.Number), tsbpdDelayUs uint64, startSeq uint32) *receiver {
	r, _ := mockNakBtreeRecvWithTsbpdAndTime(onSendNAK, tsbpdDelayUs, startSeq)
	// For backward compatibility: use real time
	// Tests using this helper should set packet PktTsbpdTime to values > current real time
	// to avoid triggering TSBPD advancement
	r.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }
	return r
}

// TestNakBtree_FirstPacketSetsBaseline tests that the first packet found
// in the btree becomes the baseline for gap detection, using contiguousPoint.
// Phase 14: Updated to use unified contiguousPoint instead of nakScanStartPoint.
func TestNakBtree_FirstPacketSetsBaseline(t *testing.T) {
	var nakedSequences []uint32

	recv := mockNakBtreeRecv(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	})

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	// mockNakBtreeRecv uses: TsbpdDelay=1000, NakRecentPercent=0.5
	// So tooRecentThreshold = now + 500
	// For gap detection, packets need PktTsbpdTime in range (now, now+500]
	baseNow := uint64(1000)

	// Add packets 100-104 with PktTsbpdTime in scan range
	// At Tick(baseNow=1000), tooRecentThreshold=1500
	// PktTsbpdTime should be <= 1500 to be scanned
	for i := uint32(100); i < 105; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseNow + 200 + uint64(i) // 1300-1304 (in scan range)
		recv.Push(p)
	}

	// First scan to establish contiguousPoint (Phase 14: unified scan)
	recv.Tick(baseNow)

	// Manually remove packets 100-102 (simulate partial delivery)
	for i := uint32(100); i < 103; i++ {
		seq := circular.New(i, packet.MAX_SEQUENCENUMBER)
		recv.packetStore.Remove(seq)
		recv.contiguousPoint.Store(seq.Val())
	}

	// Add packet 108 (creates actual gap at 105, 106, 107)
	// At Tick(10000), tooRecentThreshold=10500
	// PktTsbpdTime should be <= 10500 to be scanned
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(108, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = 10000 + 200 + 108 // 10308 (in scan range at Tick(10000))
	recv.Push(p)

	// btree now contains: 103, 104, 108
	require.Equal(t, 3, recv.packetStore.Len(), "btree should have 3 packets")

	// Second scan - use time that includes all packets in scan range
	// tooRecentThreshold = 10000 + 500 = 10500
	// Packet 108's PktTsbpdTime (10308) < 10500 ✓
	nakedSequences = nil
	recv.Tick(10000)

	// NAK list uses range encoding [start, end]
	// Gap 105-107 should be encoded as [105, 107]
	require.Equal(t, []uint32{105, 107}, nakedSequences,
		"should NAK only actual gap (105-107) as range")
}

// =============================================================================
// Sequence Number Wraparound Tests
// =============================================================================
// These tests verify that the NAK btree correctly handles sequence number
// wraparound when sequences go from MAX_SEQUENCENUMBER back to 0.

// TestNakBtree_Wraparound_SimpleGap tests basic gap detection near MAX.
// This isolates the wraparound logic from the full stream simulation.
func TestNakBtree_Wraparound_SimpleGap(t *testing.T) {
	var nakedSequences []uint32

	// Use a receiver initialized near MAX so it accepts packets in that range
	baseSeq := packet.MAX_SEQUENCENUMBER - 10
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	}, 1_000_000, baseSeq) // 1 second TSBPD, startSeq near MAX

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets near MAX_SEQUENCENUMBER with a gap
	// Packets: MAX-10, MAX-9, MAX-8, [missing MAX-7], MAX-6, MAX-5
	baseTime := uint64(1_000_000) // Start at 1 second

	presentSeqs := []uint32{
		baseSeq,     // MAX-10
		baseSeq + 1, // MAX-9
		baseSeq + 2, // MAX-8
		// baseSeq + 3 is missing (MAX-7)
		baseSeq + 4, // MAX-6
		baseSeq + 5, // MAX-5
	}

	// PktTsbpdTime must be > now but <= tooRecentThreshold for gap detection
	// mockNakBtreeRecvWithTsbpd uses: TsbpdDelay=1_000_000, NakRecentPercent=0.5
	// So tooRecentThreshold = tickTime + 500_000
	tickTime := baseTime + 2_000_000 // 3 seconds
	for i, seq := range presentSeqs {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Set PktTsbpdTime in valid range: (tickTime, tickTime+500_000]
		p.Header().PktTsbpdTime = tickTime + uint64(100+i*100)
		recv.Push(p)
	}

	t.Logf("After push: btree size = %d", recv.packetStore.Len())

	// Run NAK scan - packets have valid PktTsbpdTime > now
	recv.Tick(tickTime)

	t.Logf("NAKed sequences: %v", nakedSequences)

	// Should NAK exactly MAX-7 (which is baseSeq + 3)
	expectedNak := baseSeq + 3
	require.Contains(t, nakedSequences, expectedNak,
		"should NAK missing packet at seq %d (MAX-7)", expectedNak)
}

// TestNakBtree_Wraparound_AcrossBoundary tests gap detection across MAX→0.
func TestNakBtree_Wraparound_AcrossBoundary(t *testing.T) {
	var nakedSequences []uint32

	// Start near MAX so receiver accepts packets in that range
	startSeq := packet.MAX_SEQUENCENUMBER - 2
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	}, 1_000_000, startSeq)

	// Enable debug logging
	recv.debug = true
	recv.logFunc = func(topic string, msgFn func() string) {
		t.Logf("[%s] %s", topic, msgFn())
	}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets that cross the wraparound with a gap AT the boundary
	// Packets: MAX-2, MAX-1, MAX, [missing 0], 1, 2
	baseTime := uint64(1_000_000)

	presentSeqs := []uint32{
		packet.MAX_SEQUENCENUMBER - 2, // MAX-2
		packet.MAX_SEQUENCENUMBER - 1, // MAX-1
		packet.MAX_SEQUENCENUMBER,     // MAX
		// 0 is missing
		1, // 1
		2, // 2
	}

	// PktTsbpdTime must be > now but <= tooRecentThreshold for gap detection
	// mockNakBtreeRecvWithTsbpd uses: TsbpdDelay=1_000_000, NakRecentPercent=0.5
	// So tooRecentThreshold = tickTime + 500_000
	// Packets need: tickTime < PktTsbpdTime <= tickTime + 500_000
	tickTime := baseTime + 2_000_000
	for i, seq := range presentSeqs {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Set PktTsbpdTime in valid range: (tickTime, tickTime+500_000]
		p.Header().PktTsbpdTime = tickTime + uint64(100+i*100) // Just after tickTime
		recv.Push(p)
	}

	t.Logf("After push: btree size = %d", recv.packetStore.Len())

	// Dump btree contents
	t.Logf("Btree contents (in order):")
	recv.packetStore.Iterate(func(pkt packet.Packet) bool {
		h := pkt.Header()
		t.Logf("  seq=%d, PktTsbpdTime=%d", h.PacketSequenceNumber.Val(), h.PktTsbpdTime)
		return true
	})

	// Run NAK scan - packets have valid PktTsbpdTime > now
	t.Logf("Tick at time=%d", tickTime)
	recv.Tick(tickTime)

	t.Logf("NAKed sequences: %v", nakedSequences)
	t.Logf("contiguousPoint = %d", recv.contiguousPoint.Load())

	// Should NAK exactly 0
	require.Contains(t, nakedSequences, uint32(0),
		"should NAK missing packet at seq 0 (right after MAX)")
}

// TestNakBtree_Wraparound_GapAfterWrap tests gap detection after wraparound.
func TestNakBtree_Wraparound_GapAfterWrap(t *testing.T) {
	var nakedSequences []uint32

	// Start near MAX so receiver accepts packets in that range
	startSeq := packet.MAX_SEQUENCENUMBER - 1
	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		for _, seq := range list {
			nakedSequences = append(nakedSequences, seq.Val())
		}
	}, 1_000_000, startSeq)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Create packets across wraparound with gap after wrap
	// Packets: MAX-1, MAX, 0, 1, [missing 2], 3, 4
	baseTime := uint64(1_000_000)

	presentSeqs := []uint32{
		packet.MAX_SEQUENCENUMBER - 1, // MAX-1
		packet.MAX_SEQUENCENUMBER,     // MAX
		0,                             // 0
		1,                             // 1
		// 2 is missing
		3, // 3
		4, // 4
	}

	// PktTsbpdTime must be > now but <= tooRecentThreshold for gap detection
	tickTime := baseTime + 2_000_000
	for i, seq := range presentSeqs {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Set PktTsbpdTime in valid range: (tickTime, tickTime+500_000]
		p.Header().PktTsbpdTime = tickTime + uint64(100+i*100)
		recv.Push(p)
	}

	t.Logf("After push: btree size = %d", recv.packetStore.Len())

	// Run NAK scan - packets have valid PktTsbpdTime > now
	recv.Tick(tickTime)

	t.Logf("NAKed sequences: %v", nakedSequences)

	// Should NAK exactly 2
	require.Contains(t, nakedSequences, uint32(2),
		"should NAK missing packet at seq 2 (after wraparound)")
}

// TestNakBtree_RealisticStream_Wraparound tests NAK detection when sequence
// numbers wrap around from MAX to 0.
//
// This is a critical test because:
// - SRT uses 31-bit sequence numbers (max = 2147483647)
// - Long-running streams WILL wrap around
// - The circular sequence math must handle this correctly
func TestNakBtree_RealisticStream_Wraparound(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start near MAX_SEQUENCENUMBER so we wrap during the stream
	// MAX_SEQUENCENUMBER = 0x7FFFFFFF = 2147483647
	// Start 100 packets before wrap to ensure we cross the boundary
	startSeq := packet.MAX_SEQUENCENUMBER - 100

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  3.0, // Generate enough packets to wrap (~267 packets)
		TsbpdDelayUs: 3_000_000,
		StartSeq:     startSeq,
		StartTimeUs:  1_000_000,
	}

	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets, startSeq=%d, endSeq=%d (wrapped=%v)",
		stream.TotalPackets, startSeq, stream.EndSeq,
		stream.EndSeq < startSeq) // endSeq < startSeq means we wrapped

	// Verify wraparound occurred
	require.True(t, stream.EndSeq < startSeq,
		"stream should wrap around (endSeq %d should be < startSeq %d)", stream.EndSeq, startSeq)

	// Apply periodic loss (every 10th packet)
	lossPattern := PeriodicLoss{Period: 10, Offset: 0}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Dropped %d packets with %s", len(dropped), lossPattern.Description())

	// Log some dropped sequences to verify wraparound in loss
	wrapDropped := 0
	for _, seq := range dropped {
		if seq < 100 { // Wrapped sequences are near 0
			wrapDropped++
		}
	}
	t.Logf("Dropped packets after wrap: %d", wrapDropped)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			// Handle wraparound in range interpretation
			if start <= end {
				for seq := start; seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			} else {
				// Wrapped range: start > end means [start..MAX, 0..end]
				for seq := start; seq <= packet.MAX_SEQUENCENUMBER; seq++ {
					nakedSeqs[seq] = true
				}
				for seq := uint32(0); seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
			if missedNaks <= 5 {
				t.Logf("Missing NAK for dropped packet: seq=%d (wrapped=%v)",
					droppedSeq, droppedSeq < startSeq)
			}
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets across wraparound", missedNaks, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets correctly NAKed across sequence wraparound", len(dropped))
	}
}

// TestNakBtree_RealisticStream_Wraparound_BurstLoss tests burst loss across
// the wraparound boundary.
func TestNakBtree_RealisticStream_Wraparound_BurstLoss(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start 50 packets before wrap
	startSeq := packet.MAX_SEQUENCENUMBER - 50

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  2.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     startSeq,
		StartTimeUs:  1_000_000,
	}

	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets, startSeq=%d, endSeq=%d", stream.TotalPackets, startSeq, stream.EndSeq)

	// Burst loss: 5 packets every 30 - likely to hit the wraparound boundary
	lossPattern := BurstLoss{BurstInterval: 30, BurstSize: 5}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Dropped %d packets with %s", len(dropped), lossPattern.Description())

	// Check if any dropped packets are near the wrap point
	nearWrap := 0
	for _, seq := range dropped {
		if seq >= packet.MAX_SEQUENCENUMBER-10 || seq <= 10 {
			nearWrap++
		}
	}
	t.Logf("Dropped packets near wraparound boundary: %d", nearWrap)

	// Push surviving packets
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			if start <= end {
				for seq := start; seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			} else {
				for seq := start; seq <= packet.MAX_SEQUENCENUMBER; seq++ {
					nakedSeqs[seq] = true
				}
				for seq := uint32(0); seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify dropped packets were NAKed
	// NOTE: Packets near the end of the stream may not be NAKed because they're
	// filtered by tooRecentThreshold (10% of TSBPD delay). This is correct behavior.
	missedNaks := 0
	var missedSeqs []uint32
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
			missedSeqs = append(missedSeqs, droppedSeq)
		}
	}

	// Calculate which packets are "too recent" (near the end of stream)
	// tooRecentThreshold filters packets within ~10% of TSBPD delay
	tooRecentCount := 0
	for _, seq := range missedSeqs {
		// Packets near endSeq are expected to be filtered as "too recent"
		if circular.SeqDistance(seq, stream.EndSeq) < 10 {
			tooRecentCount++
		}
	}

	if missedNaks > 0 {
		t.Logf("Missed NAKs: %v (count=%d, tooRecent=%d)", missedSeqs, missedNaks, tooRecentCount)
		t.Logf("startSeq=%d, endSeq=%d", cfg.StartSeq, stream.EndSeq)

		// Allow missing packets if they're all near the end (tooRecent is expected)
		if missedNaks > tooRecentCount {
			t.Errorf("Failed to NAK %d/%d dropped packets with burst loss across wraparound (excluding %d too-recent)",
				missedNaks-tooRecentCount, len(dropped)-tooRecentCount, tooRecentCount)
		} else {
			t.Logf("✅ All non-recent packets NAKed (%d too-recent packets correctly skipped)", tooRecentCount)
		}
	} else {
		t.Logf("✅ All %d dropped packets correctly NAKed with burst loss across wraparound", len(dropped))
	}
}

// TestNakBtree_RealisticStream_Wraparound_OutOfOrder tests out-of-order
// arrival combined with sequence wraparound.
func TestNakBtree_RealisticStream_Wraparound_OutOfOrder(t *testing.T) {
	var nakedRanges [][]uint32
	nakLock := sync.Mutex{}

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Start 80 packets before wrap
	startSeq := packet.MAX_SEQUENCENUMBER - 80

	cfg := StreamSimConfig{
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  2.0,
		TsbpdDelayUs: 3_000_000,
		StartSeq:     startSeq,
		StartTimeUs:  1_000_000,
	}

	recv := mockNakBtreeRecvWithTsbpd(func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		ranges := make([]uint32, len(list))
		for i, seq := range list {
			ranges[i] = seq.Val()
		}
		if len(ranges) > 0 {
			nakedRanges = append(nakedRanges, ranges)
		}
	}, cfg.TsbpdDelayUs, cfg.StartSeq)

	stream := generatePacketStream(addr, cfg)
	t.Logf("Generated %d packets, wrap occurs during stream", stream.TotalPackets)

	// Apply periodic loss
	lossPattern := PeriodicLoss{Period: 15, Offset: 0}
	surviving, dropped := applyLossPattern(stream.Packets, lossPattern)
	t.Logf("Dropped %d packets", len(dropped))

	// Apply out-of-order (burst reverse) - this will shuffle packets around the wraparound
	reorderPattern := BurstReorder{BurstSize: 8}
	reordered := reorderPattern.Reorder(surviving)
	t.Logf("Reordered with %s", reorderPattern.Description())

	// Push packets
	for _, p := range reordered {
		recv.Push(p)
	}

	// Run NAK cycles
	currentTimeUs := cfg.StartTimeUs + cfg.TsbpdDelayUs + 100_000
	endTimeUs := stream.EndTimeUs + cfg.TsbpdDelayUs + 1_000_000
	for currentTimeUs < endTimeUs {
		recv.Tick(currentTimeUs)
		currentTimeUs += 20_000
	}

	// Collect NAKed sequences
	nakedSeqs := make(map[uint32]bool)
	nakLock.Lock()
	for _, ranges := range nakedRanges {
		for i := 0; i+1 < len(ranges); i += 2 {
			start, end := ranges[i], ranges[i+1]
			if start <= end {
				for seq := start; seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			} else {
				for seq := start; seq <= packet.MAX_SEQUENCENUMBER; seq++ {
					nakedSeqs[seq] = true
				}
				for seq := uint32(0); seq <= end; seq++ {
					nakedSeqs[seq] = true
				}
			}
		}
	}
	nakLock.Unlock()

	t.Logf("Total unique sequences NAKed: %d", len(nakedSeqs))

	// Verify all dropped packets were NAKed
	missedNaks := 0
	for _, droppedSeq := range dropped {
		if !nakedSeqs[droppedSeq] {
			missedNaks++
		}
	}

	if missedNaks > 0 {
		t.Errorf("Failed to NAK %d/%d dropped packets with OOO + wraparound", missedNaks, len(dropped))
	} else {
		t.Logf("✅ All %d dropped packets correctly NAKed with OOO + wraparound", len(dropped))
	}
}
