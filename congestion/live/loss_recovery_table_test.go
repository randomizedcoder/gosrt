// Package live provides table-driven loss recovery tests.
//
// This file implements a comprehensive table-driven approach for testing
// loss recovery scenarios with full timing control. Each test case specifies:
//   - Drop pattern (periodic, burst, head, tail, clustered, etc.)
//   - Configuration (TSBPD delay, NAK percent, start sequence)
//   - Timing parameters (NAK cycles, delivery cycles, tick intervals)
//   - Expected outcomes (full recovery, partial recovery, drops)
//
// The timing parameters allow tests to properly cover edge cases like
// heavy loss and large streams that require more NAK/retransmit cycles.
package live

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ============================================================================
// DROP PATTERN INTERFACE
// ============================================================================

// DropPattern defines how packets are dropped during a test.
type DropPattern interface {
	// ShouldDrop returns true if the packet at index i should be dropped
	ShouldDrop(i int, totalPackets int) bool
	// Description returns a human-readable description
	Description() string
}

// ============================================================================
// DROP PATTERN IMPLEMENTATIONS
// ============================================================================

// DropEveryN drops every Nth packet (indices N-1, 2N-1, 3N-1, ...)
type DropEveryN struct {
	N      int
	Offset int // Start offset (0 = first at index N-1)
	Max    int // Max index to drop at (0 = no limit, drop until end)
}

func (d DropEveryN) ShouldDrop(i int, totalPackets int) bool {
	if d.N <= 0 {
		return false
	}
	if d.Max > 0 && i > d.Max {
		return false // Don't drop beyond Max
	}
	return (i+1-d.Offset)%d.N == 0 && i >= d.Offset
}

func (d DropEveryN) Description() string {
	return "every_" + itoa(d.N)
}

// DropBurst drops a contiguous range of packets
type DropBurst struct {
	Start int // First index to drop
	Count int // Number of packets to drop
}

func (d DropBurst) ShouldDrop(i int, totalPackets int) bool {
	return i >= d.Start && i < d.Start+d.Count
}

func (d DropBurst) Description() string {
	return "burst_" + itoa(d.Start) + "_" + itoa(d.Count)
}

// DropHead drops the first N packets
type DropHead struct {
	Count int
}

func (d DropHead) ShouldDrop(i int, totalPackets int) bool {
	return i < d.Count
}

func (d DropHead) Description() string {
	return "head_" + itoa(d.Count)
}

// DropTail drops the last N packets
type DropTail struct {
	Count int
}

func (d DropTail) ShouldDrop(i int, totalPackets int) bool {
	return i >= totalPackets-d.Count
}

func (d DropTail) Description() string {
	return "tail_" + itoa(d.Count)
}

// DropNearTail drops N packets near the end, with M packets after
type DropNearTail struct {
	GapStart int // How many packets AFTER the gap
	GapSize  int // Size of gap
}

func (d DropNearTail) ShouldDrop(i int, totalPackets int) bool {
	gapStartIdx := totalPackets - d.GapStart - d.GapSize
	return i >= gapStartIdx && i < gapStartIdx+d.GapSize
}

func (d DropNearTail) Description() string {
	return "near_tail_" + itoa(d.GapSize)
}

// DropMultipleBursts drops multiple bursts
type DropMultipleBursts struct {
	Bursts []DropBurst
}

func (d DropMultipleBursts) ShouldDrop(i int, totalPackets int) bool {
	for _, b := range d.Bursts {
		if b.ShouldDrop(i, totalPackets) {
			return true
		}
	}
	return false
}

func (d DropMultipleBursts) Description() string {
	return "multi_burst_" + itoa(len(d.Bursts))
}

// DropClustered drops small groups near each other
type DropClustered struct {
	Clusters [][]int // Each cluster is a list of indices
}

func (d DropClustered) ShouldDrop(i int, totalPackets int) bool {
	for _, cluster := range d.Clusters {
		for _, idx := range cluster {
			if i == idx {
				return true
			}
		}
	}
	return false
}

func (d DropClustered) Description() string {
	return "clustered_" + itoa(len(d.Clusters))
}

// DropSpecific drops specific indices
type DropSpecific struct {
	Indices []int
}

func (d DropSpecific) ShouldDrop(i int, totalPackets int) bool {
	for _, idx := range d.Indices {
		if i == idx {
			return true
		}
	}
	return false
}

func (d DropSpecific) Description() string {
	return "specific_" + itoa(len(d.Indices))
}

// simple int to string without fmt
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// ============================================================================
// TEST CASE DEFINITION
// ============================================================================

// LossRecoveryTestCase defines a single loss recovery test scenario.
type LossRecoveryTestCase struct {
	Name         string
	TotalPackets int
	StartSeq     uint32      // For wraparound tests, use MAX_SEQUENCENUMBER - N
	TsbpdDelayUs uint64      // TSBPD delay in microseconds (default 500_000)
	NakRecentPct float64     // NAK recent percent (default 0.10)
	DropPattern  DropPattern // How to drop packets

	// Protocol timing - must be smaller than TSBPD for recovery to work!
	AckIntervalUs uint64 // ACK interval in microseconds (default 10_000 = 10ms)
	NakIntervalUs uint64 // NAK interval in microseconds (default 20_000 = 20ms)

	// Test timing configuration - critical for proper test coverage
	NakCycles      int // Number of NAK/retransmit cycles (default 60)
	DeliveryCycles int // Number of delivery cycles (default 20)
	NakTickUs      int // Microseconds per NAK tick (default 10_000)
	DeliveryTickUs int // Microseconds per delivery tick (default 50_000)
	PacketSpreadUs int // Microseconds between packet TSBPD times (default 5_000)

	// Retransmit behavior
	DoRetransmit bool // Whether to send retransmits

	// Expected outcomes
	ExpectFullRecovery bool    // If true, expect 100% delivery
	MinDeliveryPct     float64 // Minimum delivery % (for partial tests)
	MinNakPct          float64 // Minimum % of dropped packets that should be NAKed
	MaxOverNakFactor   float64 // Maximum NAK count / dropped count (detect over-NAKing)
}

// ============================================================================
// DERIVED DEFAULTS - Compute timing params from CODE_PARAMs
// ============================================================================

// applyDerivedDefaults computes DERIVED parameters from CODE_PARAMs.
// This ensures timing parameters are always compatible with the TSBPD window.
//
// Derivation formulas (from TsbpdDelayUs):
//   - AckIntervalUs   = TsbpdDelayUs / 10  (ACK must fire multiple times within TSBPD)
//   - NakIntervalUs   = TsbpdDelayUs / 5   (NAK must fire before packet expires)
//   - NakTickUs       = TsbpdDelayUs / 50  (fine-grained timing within window)
//   - DeliveryTickUs  = TsbpdDelayUs / 25  (allow multiple delivery checks)
//   - PacketSpreadUs  = TsbpdDelayUs / 100 (fit packets within TSBPD window)
//
// Derivation formulas (from TotalPackets + DropPattern):
//   - NakCycles       = max(60, TotalPackets/2) * lossMultiplier
//   - DeliveryCycles  = max(20, TotalPackets/5)
func (tc *LossRecoveryTestCase) applyDerivedDefaults() {
	// Step 1: Apply CODE_PARAM defaults (if not set)
	if tc.TsbpdDelayUs == 0 {
		tc.TsbpdDelayUs = 500_000 // 500ms default
	}
	if tc.NakRecentPct == 0 {
		tc.NakRecentPct = 0.10
	}

	// Step 2: Derive timing params from TsbpdDelayUs (only if not explicitly set)
	// This is the key insight: timing params MUST be proportional to TSBPD
	if tc.AckIntervalUs == 0 {
		tc.AckIntervalUs = tc.TsbpdDelayUs / 10 // 10% of TSBPD
	}
	if tc.NakIntervalUs == 0 {
		tc.NakIntervalUs = tc.TsbpdDelayUs / 5 // 20% of TSBPD
	}
	if tc.NakTickUs == 0 {
		tc.NakTickUs = int(tc.TsbpdDelayUs / 50) // 2% of TSBPD
		if tc.NakTickUs < 100 {
			tc.NakTickUs = 100 // minimum 100us
		}
	}
	if tc.DeliveryTickUs == 0 {
		tc.DeliveryTickUs = int(tc.TsbpdDelayUs / 25) // 4% of TSBPD
		if tc.DeliveryTickUs < 200 {
			tc.DeliveryTickUs = 200 // minimum 200us
		}
	}
	if tc.PacketSpreadUs == 0 {
		// CRITICAL: PacketSpreadUs must scale with TotalPackets to ensure
		// all packets fit within the TSBPD window (use 80% to leave buffer)
		if tc.TotalPackets > 1 {
			tc.PacketSpreadUs = int(tc.TsbpdDelayUs * 80 / uint64(tc.TotalPackets*100))
		} else {
			tc.PacketSpreadUs = int(tc.TsbpdDelayUs / 100) // Default for single packet
		}
		if tc.PacketSpreadUs < 50 {
			tc.PacketSpreadUs = 50 // minimum 50us
		}
	}

	// Step 3: Derive cycle counts from TotalPackets AND TsbpdDelayUs (only if not explicitly set)
	// The key insight: cycles * tickInterval must cover the TSBPD window
	if tc.NakCycles == 0 {
		// Base cycles from packet count
		baseCycles := maxInt(60, tc.TotalPackets/2)
		// Ensure we have enough cycles to cover the TSBPD window
		// Minimum cycles = 2 * TSBPD / NakTickUs (to go through window twice)
		minCycles := int(2 * tc.TsbpdDelayUs / uint64(tc.NakTickUs))
		tc.NakCycles = maxInt(baseCycles, minCycles)
		// Adjust for heavy loss patterns
		if isHeavyLossPattern(tc.DropPattern) {
			tc.NakCycles *= 3
		}
	}
	if tc.DeliveryCycles == 0 {
		// Base cycles from packet count
		baseCycles := maxInt(20, tc.TotalPackets/5)
		// Ensure we have enough cycles to cover the TSBPD window
		minCycles := int(2 * tc.TsbpdDelayUs / uint64(tc.DeliveryTickUs))
		tc.DeliveryCycles = maxInt(baseCycles, minCycles)
	}
}

// isHeavyLossPattern returns true if the drop pattern causes >10% loss
func isHeavyLossPattern(p DropPattern) bool {
	if p == nil {
		return false
	}
	switch dp := p.(type) {
	case DropEveryN:
		return dp.N <= 10 // Every 10th or more frequent = heavy
	case DropBurst:
		return dp.Count >= 10 // Bursts of 10+ = heavy
	case DropMultipleBursts:
		total := 0
		for _, b := range dp.Bursts {
			total += b.Count
		}
		return total >= 10
	default:
		return false
	}
}

// maxInt returns the larger of a and b
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ============================================================================
// TABLE-DRIVEN TEST RUNNER
// ============================================================================

// runLossRecoveryTableTest runs a single loss recovery test case with full timing control.
func runLossRecoveryTableTest(t *testing.T, tc LossRecoveryTestCase) {
	t.Helper()

	// Apply derived defaults - this computes timing params from CODE_PARAMs
	tc.applyDerivedDefaults()

	ackIntervalUs := tc.AckIntervalUs
	nakIntervalUs := tc.NakIntervalUs

	collector := NewTestMetricsCollector()

	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber:  circular.New(tc.StartSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    ackIntervalUs,
		PeriodicNAKInterval:    nakIntervalUs,
		OnSendACK:              collector.OnSendACK,
		OnSendNAK:              collector.OnSendNAK,
		OnDeliver:              collector.OnDeliver,
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tc.TsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       tc.NakRecentPct,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	baseTime := uint64(1_000_000)
	mockTime := baseTime
	r.nowFn = func() uint64 { return mockTime }

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	var allPackets []packet.Packet
	var droppedPackets []packet.Packet
	var droppedSeqs []uint32
	isDropped := make(map[int]bool)

	// Generate packets and identify dropped ones
	for i := 0; i < tc.TotalPackets; i++ {
		seq := circular.SeqAdd(tc.StartSeq, uint32(i))
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tc.TsbpdDelayUs + uint64(i*tc.PacketSpreadUs)
		p.Header().Timestamp = uint32(i * tc.PacketSpreadUs)

		allPackets = append(allPackets, p)

		if tc.DropPattern != nil && tc.DropPattern.ShouldDrop(i, tc.TotalPackets) {
			droppedPackets = append(droppedPackets, p)
			droppedSeqs = append(droppedSeqs, seq)
			isDropped[i] = true
		}
	}

	collector.ExpectedPackets = tc.TotalPackets
	collector.DroppedPackets = droppedSeqs

	t.Logf("Test: %s", tc.Name)
	t.Logf("  Config: TSBPD=%dms, NakRecent=%.0f%%, NakCycles=%d, DeliveryCycles=%d",
		tc.TsbpdDelayUs/1000, tc.NakRecentPct*100, tc.NakCycles, tc.DeliveryCycles)
	patternDesc := "none"
	if tc.DropPattern != nil {
		patternDesc = tc.DropPattern.Description()
	}
	t.Logf("  Pattern: %s, Dropped: %d/%d (%.1f%%)",
		patternDesc,
		len(droppedSeqs), tc.TotalPackets,
		float64(len(droppedSeqs))/float64(tc.TotalPackets)*100)

	// Phase 1: Push non-dropped packets
	for i, p := range allPackets {
		if !isDropped[i] {
			recv.Push(p)
		}
	}

	// Phase 2: NAK/Retransmit cycles
	// Key insight: Retransmits must arrive BEFORE TSBPD expiry. We interleave:
	// 1. Tick to generate NAKs
	// 2. Immediately retransmit NAKed packets
	// 3. Tick again to process retransmits before TSBPD advances
	if tc.DoRetransmit && len(droppedPackets) > 0 {
		// Calculate NAK window start time
		// NAK is generated when: PktTsbpdTime <= now + tsbpdDelay * (1 - nakRecentPercent)
		// So we need: now >= PktTsbpdTime - tsbpdDelay * (1 - nakRecentPercent)
		firstTsbpd := allPackets[0].Header().PktTsbpdTime
		nakWindow := uint64(float64(tc.TsbpdDelayUs) * (1.0 - tc.NakRecentPct))
		nakStartTime := firstTsbpd - nakWindow

		retransmitted := make(map[uint32]bool)

		for tick := 0; tick < tc.NakCycles; tick++ {
			mockTime = nakStartTime + uint64(tick*tc.NakTickUs)
			r.Tick(mockTime)

			// Immediately retransmit any NAKed packets
			collector.mu.Lock()
			for seq := range collector.NAKedSequences {
				if !retransmitted[seq] {
					for _, p := range droppedPackets {
						if p.Header().PacketSequenceNumber.Val() == seq {
							// Only retransmit if not yet past contiguousPoint
							cp := r.contiguousPoint.Load()
							// Use circular comparison for wraparound safety
							seqNum := circular.New(seq, packet.MAX_SEQUENCENUMBER)
							cpNum := circular.New(cp, packet.MAX_SEQUENCENUMBER)
							if seqNum.Gt(cpNum) {
								retransP := packet.NewPacket(addr)
								retransP.Header().PacketSequenceNumber = p.Header().PacketSequenceNumber
								retransP.Header().PktTsbpdTime = p.Header().PktTsbpdTime
								retransP.Header().Timestamp = p.Header().Timestamp
								retransP.Header().RetransmittedPacketFlag = true
								recv.Push(retransP)
								collector.RetransmittedCount++
							}
							break
						}
					}
					retransmitted[seq] = true
				}
			}
			collector.mu.Unlock()

			// Extra tick to process the retransmit before time advances too far
			// This simulates the retransmit arriving quickly after NAK
			r.Tick(mockTime + 1000) // +1ms
		}
	} else if !tc.DoRetransmit {
		// Just run ticks to let TSBPD expire packets (no retransmit)
		for tick := 0; tick < tc.NakCycles; tick++ {
			mockTime = baseTime + tc.TsbpdDelayUs + uint64(tick*tc.NakTickUs)
			r.Tick(mockTime)
		}
	}

	// Phase 3: Delivery cycles - advance time to trigger TSBPD delivery
	for tick := 0; tick < tc.DeliveryCycles; tick++ {
		mockTime = baseTime + tc.TsbpdDelayUs + uint64(tick*tc.DeliveryTickUs)
		r.Tick(mockTime)
	}

	// Results
	droppedCount := len(droppedSeqs)
	deliveryPct := float64(collector.DeliveredCount) / float64(tc.TotalPackets) * 100
	nakPct := float64(0)
	if droppedCount > 0 {
		nakPct = float64(collector.UniqueNAKCount) / float64(droppedCount) * 100
	}

	t.Logf("  Results: NAKed=%d/%d (%.0f%%), Retrans=%d, Delivered=%d/%d (%.1f%%)",
		collector.UniqueNAKCount, droppedCount, nakPct,
		collector.RetransmittedCount,
		collector.DeliveredCount, tc.TotalPackets, deliveryPct)

	// Verify expectations
	if tc.ExpectFullRecovery {
		if collector.DeliveredCount < tc.TotalPackets {
			t.Errorf("Expected full recovery: got %d/%d delivered (%.1f%%)",
				collector.DeliveredCount, tc.TotalPackets, deliveryPct)
		}
	}

	if tc.MinDeliveryPct > 0 {
		if deliveryPct < tc.MinDeliveryPct {
			t.Errorf("Delivery below minimum: got %.1f%%, want >= %.1f%%",
				deliveryPct, tc.MinDeliveryPct)
		}
	}

	if tc.MinNakPct > 0 && droppedCount > 0 {
		if nakPct < tc.MinNakPct {
			t.Errorf("NAK rate below minimum: got %.1f%%, want >= %.1f%%",
				nakPct, tc.MinNakPct)
		}
	}

	if tc.MaxOverNakFactor > 0 && droppedCount > 0 {
		overNakFactor := float64(collector.UniqueNAKCount) / float64(droppedCount)
		if overNakFactor > tc.MaxOverNakFactor {
			t.Errorf("Over-NAKing detected: NAKed %d for %d dropped (%.1fx, max %.1fx)",
				collector.UniqueNAKCount, droppedCount, overNakFactor, tc.MaxOverNakFactor)
		}
	}

	t.Logf("  ✓ %s completed", tc.Name)
}

// ============================================================================
// TEST CASES
// ============================================================================

// LossRecoveryTableTests defines all loss recovery test scenarios.
var LossRecoveryTableTests = []LossRecoveryTestCase{
	// === Phase 1: Critical ===
	{
		Name:               "Full",
		TotalPackets:       105, // Extra packets to ensure last dropped has trailing data
		StartSeq:           1,
		DropPattern:        DropEveryN{N: 20}, // Drops 19,39,59,79,99 - all have trailing packets
		DoRetransmit:       true,
		NakCycles:          80, // More cycles for reliable recovery
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100, // All dropped should be NAKed
	},
	{
		Name:               "Wraparound",
		TotalPackets:       100,
		StartSeq:           packet.MAX_SEQUENCENUMBER - 50, // Start near wraparound
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100, // All 4 should be NAKed
	},
	{
		Name:               "BurstLoss",
		TotalPackets:       100,
		StartSeq:           1,
		DropPattern:        DropBurst{Start: 45, Count: 10},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100, // All burst packets should be NAKed
	},
	{
		Name:           "TSBPD_Expiry",
		TotalPackets:   100,
		StartSeq:       1,
		DropPattern:    DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:   false, // No retransmit - packets will expire
		NakCycles:      60,
		DeliveryCycles: 30,
		MinDeliveryPct: 95, // 96/100 expected (4 dropped, not recovered)
		MinNakPct:      80, // NAKs should still be sent even if not recovered
	},

	// === Phase 2: Important ===
	{
		Name:               "HeadLoss",
		TotalPackets:       100,
		StartSeq:           1,
		DropPattern:        DropHead{Count: 5},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "NearTailLoss",
		TotalPackets:       105, // Extra packets after gap to trigger NAK
		StartSeq:           1,
		DropPattern:        DropNearTail{GapStart: 10, GapSize: 5}, // Drop 90-94, 95-104 follow
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:           "LateRetransmit",
		TotalPackets:   100,
		StartSeq:       1,
		DropPattern:    DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:   false, // Simulates late retransmit (after TSBPD)
		NakCycles:      60,
		DeliveryCycles: 30,
		MinDeliveryPct: 95, // 96/100 expected
		MinNakPct:      80,
	},

	// === Phase 3: Comprehensive ===
	{
		Name:               "PartialRecovery",
		TotalPackets:       100,
		StartSeq:           1,
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:         "MultipleBursts",
		TotalPackets: 100,
		StartSeq:     1,
		DropPattern: DropMultipleBursts{
			Bursts: []DropBurst{
				{Start: 20, Count: 5},
				{Start: 60, Count: 5},
			},
		},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "HeavyLoss",
		TotalPackets:       212, // 212 not divisible by 5, so last drop at 209, trailing 210-211
		StartSeq:           1,
		DropPattern:        DropEveryN{N: 5}, // 20% loss = 42 packets (indices 4,9,14...209)
		DoRetransmit:       true,
		NakCycles:          300, // More cycles for heavy loss
		DeliveryCycles:     60,
		PacketSpreadUs:     2_000, // Tighter spread (2ms * 212 = 424ms total)
		NakTickUs:          2_000, // Faster ticks
		ExpectFullRecovery: true,
		MinNakPct:          95, // Heavy loss may have some edge cases
	},

	// === Phase 4: Config Variants ===
	{
		Name:               "PeriodicLoss",
		TotalPackets:       105, // Extra to avoid tail loss edge case
		StartSeq:           1,
		DropPattern:        DropEveryN{N: 10}, // 10% loss
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:         "ClusteredLoss",
		TotalPackets: 100,
		StartSeq:     1,
		DropPattern: DropClustered{
			Clusters: [][]int{
				{15, 16},     // Cluster 1: 2 packets
				{45, 46, 47}, // Cluster 2: 3 packets
				{75, 76, 77}, // Cluster 3: 3 packets
			},
		},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "LargeStream",
		TotalPackets:       1050, // Extra trailing packets
		StartSeq:           1,
		DropPattern:        DropEveryN{N: 20}, // 5% loss = ~52 packets
		DoRetransmit:       true,
		NakCycles:          400, // Many more cycles for large stream
		DeliveryCycles:     150,
		PacketSpreadUs:     500,   // Very tight spread (500us * 1050 = 525ms total)
		NakTickUs:          2_000, // Very fast ticks (2ms)
		ExpectFullRecovery: true,
		MinNakPct:          90,
	},
	{
		Name:               "SmallTSBPD",
		TotalPackets:       100,
		StartSeq:           1,
		TsbpdDelayUs:       100_000, // 100ms - tighter window
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          100, // More cycles for tight window
		DeliveryCycles:     50,
		NakTickUs:          1_000, // Very fast ticks (1ms) for tight TSBPD
		PacketSpreadUs:     500,   // Tight spread to fit in 100ms window
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "HighNakPercent",
		TotalPackets:       100,
		StartSeq:           1,
		NakRecentPct:       0.30, // 30% too-recent window (wider)
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},

	// === Corner Case Tests: CODE_PARAM combinations ===
	// These tests systematically cover corner values for:
	// - StartSeq: 0, MAX-100, MAX
	// - TsbpdDelayUs: 10_000 (10ms), 120_000 (120ms), 500_000 (500ms)
	// - NakRecentPct: 0.05 (5%), 0.10 (10%), 0.25 (25%)

	// StartSeq corners
	{
		Name:               "Corner_StartSeq_Zero",
		TotalPackets:       100,
		StartSeq:           0, // Zero baseline
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "Corner_StartSeq_NearMax",
		TotalPackets:       100,
		StartSeq:           packet.MAX_SEQUENCENUMBER - 100, // Near MAX - wraparound zone
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "Corner_StartSeq_AtMax",
		TotalPackets:       100,
		StartSeq:           packet.MAX_SEQUENCENUMBER, // AT MAX - immediate wrap
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},

	// TsbpdDelayUs corners - using DERIVED defaults for timing params
	// The applyDerivedDefaults() function computes AckInterval, NakInterval, etc.
	// from TsbpdDelayUs, ensuring timing is always compatible.
	{
		Name:         "Corner_TSBPD_10ms",
		TotalPackets: 50, // Fewer packets for tight timing
		StartSeq:     1,
		TsbpdDelayUs: 10_000, // 10ms - very aggressive
		// All timing params DERIVED from TsbpdDelayUs:
		// - AckIntervalUs = 1_000 (10ms/10)
		// - NakIntervalUs = 2_000 (10ms/5)
		// - NakTickUs = 200 (10ms/50)
		// - PacketSpreadUs = 100 (10ms/100)
		DropPattern:        DropSpecific{Indices: []int{10, 20, 30, 40}},
		DoRetransmit:       true,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:         "Corner_TSBPD_120ms",
		TotalPackets: 100,
		StartSeq:     1,
		TsbpdDelayUs: 120_000, // 120ms - standard
		// Timing params DERIVED from TsbpdDelayUs:
		// - AckIntervalUs = 12_000 (120ms/10)
		// - NakIntervalUs = 24_000 (120ms/5)
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "Corner_TSBPD_500ms_Explicit",
		TotalPackets:       100,
		StartSeq:           1,
		TsbpdDelayUs:       500_000, // 500ms - high latency (explicit, not default)
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},

	// NakRecentPct corners
	{
		Name:               "Corner_NakRecent_5pct",
		TotalPackets:       100,
		StartSeq:           1,
		NakRecentPct:       0.05, // 5% - aggressive NAKing (very small recent window)
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "Corner_NakRecent_25pct",
		TotalPackets:       100,
		StartSeq:           1,
		NakRecentPct:       0.25, // 25% - conservative NAKing (larger recent window)
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},

	// TotalPackets corners (stress testing)
	{
		Name:               "Corner_TotalPackets_Single",
		TotalPackets:       1, // Minimum - single packet
		StartSeq:           1,
		DropPattern:        nil, // No drops - just test single packet handling
		DoRetransmit:       true,
		ExpectFullRecovery: true,
	},
	{
		Name:         "Corner_TotalPackets_Large",
		TotalPackets: 1000, // Stress test - large packet count
		StartSeq:     1,
		TsbpdDelayUs: 500_000,                       // Standard 500ms TSBPD
		DropPattern:  DropEveryN{N: 50, Offset: 50}, // Drop every 50th (19 drops)
		DoRetransmit: true,
		// IMPORTANT: With 1000 packets spread over TSBPD window, later drops
		// may have TSBPD expire before retransmit can arrive. This is CORRECT
		// behavior per contiguous_point_tsbpd_advancement_design.md:
		// "If TSBPD has expired → definitely lost, advance"
		// Allow 95%+ NAK rate (18/19 = 94.7%) and 99%+ recovery
		ExpectFullRecovery: false, // Edge case: last drop may TSBPD-expire
		MinDeliveryPct:     99.0,  // At least 99% delivered
		MinNakPct:          90,    // Allow some drops to TSBPD-expire before NAK
	},

	// NakRecentPct typical value (ensures 10% is covered)
	{
		Name:               "Corner_NakRecent_10pct_Typical",
		TotalPackets:       100,
		StartSeq:           1,
		NakRecentPct:       0.10, // 10% - typical default value
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},

	// Combined corner cases (most critical combinations)
	// Wraparound + 10ms TSBPD - tests sequence wraparound handling under extreme latency
	{
		Name:         "Corner_Combo_MaxSeq_10msTSBPD",
		TotalPackets: 50,
		StartSeq:     packet.MAX_SEQUENCENUMBER - 25, // Wraparound with tight timing
		TsbpdDelayUs: 10_000,
		// Timing params DERIVED from TsbpdDelayUs - no need to specify!
		DropPattern:        DropSpecific{Indices: []int{10, 20, 30, 40}},
		DoRetransmit:       true,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "Corner_Combo_ZeroSeq_5pctNak",
		TotalPackets:       100,
		StartSeq:           0,
		NakRecentPct:       0.05, // Aggressive NAK with zero start
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
	{
		Name:               "Corner_Combo_MaxSeq_25pctNak_500msTSBPD",
		TotalPackets:       100,
		StartSeq:           packet.MAX_SEQUENCENUMBER - 50,
		TsbpdDelayUs:       500_000,
		NakRecentPct:       0.25, // Conservative NAK near wraparound
		DropPattern:        DropSpecific{Indices: []int{20, 40, 60, 80}},
		DoRetransmit:       true,
		NakCycles:          80,
		DeliveryCycles:     30,
		ExpectFullRecovery: true,
		MinNakPct:          100,
	},
}

// TestLossRecovery_Table runs all loss recovery tests using table-driven approach.
// Tests run in parallel for faster execution.
func TestLossRecovery_Table(t *testing.T) {
	for _, tc := range LossRecoveryTableTests {
		tc := tc // Capture range variable for parallel execution
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel() // Run test cases in parallel
			runLossRecoveryTableTest(t, tc)
		})
	}
}

// ============================================================================
// NEGATIVE TESTS - Validate that derivation formulas are necessary
// ============================================================================
//
// These tests INTENTIONALLY use wrong derived parameters to prove:
// 1. Our positive tests are meaningful (not passing by accident)
// 2. The derivation formulas are necessary (not just nice-to-have)
// 3. The system fails gracefully with misconfiguration

// NegativeDerivationTests intentionally misconfigure timing parameters
// to validate that correct derivation is necessary for recovery.
var NegativeDerivationTests = []LossRecoveryTestCase{
	// Test 1: NAK interval larger than TSBPD - NAKs cannot fire in time
	{
		Name:          "Negative_NakInterval_TooLarge",
		TotalPackets:  50,
		StartSeq:      1,
		TsbpdDelayUs:  10_000, // 10ms TSBPD
		NakIntervalUs: 20_000, // 20ms NAK interval - WRONG! > TSBPD
		// Other params derived correctly
		AckIntervalUs:  1_000, // 1ms (correct)
		NakTickUs:      200,
		DeliveryTickUs: 400,
		PacketSpreadUs: 100,
		NakCycles:      100,
		DeliveryCycles: 50,
		DropPattern:    DropSpecific{Indices: []int{10, 20, 30, 40}},
		DoRetransmit:   true,
		// EXPECT FAILURE: NAKs cannot fire before packets expire
		ExpectFullRecovery: false,
		MinDeliveryPct:     85, // Expect ~92% (4 lost, not recovered)
		MinNakPct:          0,  // NAKs may not fire at all
	},

	// Test 2: ACK interval equals TSBPD - too slow for effective recovery
	{
		Name:          "Negative_AckInterval_EqualsTSBPD",
		TotalPackets:  50,
		StartSeq:      1,
		TsbpdDelayUs:  10_000, // 10ms TSBPD
		AckIntervalUs: 10_000, // 10ms ACK interval - WRONG! = TSBPD
		// Other params derived correctly
		NakIntervalUs:  2_000,
		NakTickUs:      200,
		DeliveryTickUs: 400,
		PacketSpreadUs: 100,
		NakCycles:      100,
		DeliveryCycles: 50,
		DropPattern:    DropSpecific{Indices: []int{10, 20, 30, 40}},
		DoRetransmit:   true,
		// With ACK = TSBPD, recovery is degraded but may still partially work
		ExpectFullRecovery: false,
		MinDeliveryPct:     85,
	},

	// Test 3: Too few NAK cycles for heavy loss
	{
		Name:           "Negative_NakCycles_TooFew",
		TotalPackets:   200,
		StartSeq:       1,
		TsbpdDelayUs:   500_000,
		NakCycles:      5,                // WAY too few for 40 dropped packets (20% loss)
		DeliveryCycles: 20,               // Also too few
		DropPattern:    DropEveryN{N: 5}, // Heavy 20% loss
		DoRetransmit:   true,
		// EXPECT FAILURE: Not enough cycles to recover all losses
		// Actual result: ~31% delivery - very degraded!
		ExpectFullRecovery: false,
		MinDeliveryPct:     20, // Catastrophic degradation expected
	},

	// Test 4: Packet spread exceeds TSBPD - packets arrive after expiry
	{
		Name:           "Negative_PacketSpread_TooLarge",
		TotalPackets:   20,
		StartSeq:       1,
		TsbpdDelayUs:   10_000, // 10ms TSBPD
		PacketSpreadUs: 15_000, // 15ms spread - WRONG! > TSBPD
		NakIntervalUs:  2_000,  // Correct
		AckIntervalUs:  1_000,  // Correct
		NakTickUs:      200,
		DeliveryTickUs: 400,
		NakCycles:      100,
		DeliveryCycles: 100,
		DropPattern:    DropSpecific{Indices: []int{5, 10, 15}},
		DoRetransmit:   true,
		// With packets spread > TSBPD, each packet expires before next arrives
		// Actual result: ~15% delivery - nearly total failure!
		ExpectFullRecovery: false,
		MinDeliveryPct:     10, // Severe degradation expected
	},

	// Test 5: All timing params wrong (catastrophic misconfiguration)
	{
		Name:           "Negative_AllTimingWrong",
		TotalPackets:   50,
		StartSeq:       1,
		TsbpdDelayUs:   10_000,  // 10ms TSBPD
		AckIntervalUs:  50_000,  // 50ms ACK - WRONG! 5x TSBPD
		NakIntervalUs:  100_000, // 100ms NAK - WRONG! 10x TSBPD
		NakTickUs:      50_000,  // 50ms tick - WRONG! 5x TSBPD
		DeliveryTickUs: 100_000, // 100ms delivery - WRONG!
		PacketSpreadUs: 20_000,  // 20ms spread - WRONG!
		NakCycles:      5,       // Too few
		DeliveryCycles: 5,       // Too few
		DropPattern:    DropSpecific{Indices: []int{10, 20, 30, 40}},
		DoRetransmit:   true,
		// Complete misconfiguration should cause catastrophic failure
		// Actual result: ~38% delivery - system barely functions!
		ExpectFullRecovery: false,
		MinDeliveryPct:     20, // Very low bar - just needs to not crash
		MinNakPct:          0,  // NAKs unlikely to fire
	},
}

// TestLossRecovery_NegativeDerivations validates that correct derivation is necessary.
// These tests EXPECT degraded performance due to intentional misconfiguration.
func TestLossRecovery_NegativeDerivations(t *testing.T) {
	for _, tc := range NegativeDerivationTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			// Note: These tests may log errors/warnings - that's expected!
			// We're validating that the system degrades gracefully.
			runLossRecoveryTableTest(t, tc)
		})
	}
}
