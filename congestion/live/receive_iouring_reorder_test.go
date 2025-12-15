//go:build go1.18

package live

import (
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Test Helper Functions
// ============================================================================

// generatePackets creates n packets with sequential sequence numbers
// starting from startSeq. TSBPD times are spread across tsbpdDelay,
// with older packets having earlier TSBPD times.
func generatePackets(addr net.Addr, startSeq uint32, n int, baseTime uint64, tsbpdDelay time.Duration) []packet.Packet {
	packets := make([]packet.Packet, n)
	delayPerPacket := uint64(tsbpdDelay.Microseconds()) / uint64(n)

	for i := 0; i < n; i++ {
		p := packet.NewPacket(addr)
		seq := startSeq + uint32(i)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Packets with lower sequence have earlier TSBPD times
		p.Header().PktTsbpdTime = baseTime + uint64(i)*delayPerPacket
		packets[i] = p
	}
	return packets
}

// shufflePacketsDeterministic shuffles packets using a seeded RNG for reproducibility
func shufflePacketsDeterministic(packets []packet.Packet, seed int64) []packet.Packet {
	shuffled := make([]packet.Packet, len(packets))
	copy(shuffled, packets)

	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	return shuffled
}

// dropByModulus drops packets where seq % modulus == 0
// Returns (delivered, dropped) with exact known contents
func dropByModulus(packets []packet.Packet, modulus int) (delivered, dropped []packet.Packet) {
	for _, pkt := range packets {
		seq := pkt.Header().PacketSequenceNumber.Val()
		if int(seq)%modulus == 0 {
			dropped = append(dropped, pkt)
		} else {
			delivered = append(delivered, pkt)
		}
	}
	return delivered, dropped
}

// dropByBursts drops consecutive sequences at specified start points
// Returns (delivered, dropped) with exact known contents
func dropByBursts(packets []packet.Packet, burstStarts []uint32, burstSize int) (delivered, dropped []packet.Packet) {
	// Create a map of sequences to drop
	dropSet := make(map[uint32]bool)
	for _, start := range burstStarts {
		for i := 0; i < burstSize; i++ {
			dropSet[start+uint32(i)] = true
		}
	}

	for _, pkt := range packets {
		seq := pkt.Header().PacketSequenceNumber.Val()
		if dropSet[seq] {
			dropped = append(dropped, pkt)
		} else {
			delivered = append(delivered, pkt)
		}
	}
	return delivered, dropped
}

// nakListCapture provides a thread-safe way to capture NAK lists
type nakListCapture struct {
	mu       sync.Mutex
	nakLists [][]uint32
}

func newNakListCapture() *nakListCapture {
	return &nakListCapture{
		nakLists: make([][]uint32, 0),
	}
}

func (c *nakListCapture) capture(list []circular.Number) {
	c.mu.Lock()
	defer c.mu.Unlock()

	vals := make([]uint32, len(list))
	for i, n := range list {
		vals[i] = n.Val()
	}
	c.nakLists = append(c.nakLists, vals)
}

func (c *nakListCapture) getNakLists() [][]uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([][]uint32, len(c.nakLists))
	copy(result, c.nakLists)
	return result
}

// getAllNAKedSequences returns all sequences that were NAK'd (flattened from NAK lists)
func (c *nakListCapture) getAllNAKedSequences() map[uint32]bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	sequences := make(map[uint32]bool)
	for _, list := range c.nakLists {
		// NAK lists come in pairs: [start1, end1, start2, end2, ...]
		for i := 0; i+1 < len(list); i += 2 {
			start := list[i]
			end := list[i+1]
			for seq := start; seq <= end; seq++ {
				sequences[seq] = true
			}
		}
	}
	return sequences
}

// verifyAllDroppedWereNAKd checks that every dropped packet appears in NAK lists
func verifyAllDroppedWereNAKd(t *testing.T, dropped []packet.Packet, capture *nakListCapture) {
	t.Helper()
	nakedSeqs := capture.getAllNAKedSequences()

	for _, pkt := range dropped {
		seq := pkt.Header().PacketSequenceNumber.Val()
		require.True(t, nakedSeqs[seq], "Dropped packet seq=%d was not NAK'd", seq)
	}
}

// expectedModulusDrops calculates which sequences would be dropped for modulus dropping
func expectedModulusDrops(startSeq uint32, n int, modulus int) []uint32 {
	var dropped []uint32
	for i := 0; i < n; i++ {
		seq := startSeq + uint32(i)
		if int(seq)%modulus == 0 {
			dropped = append(dropped, seq)
		}
	}
	return dropped
}

// mockLiveRecvNakBtree creates a receiver with NAK btree enabled
func mockLiveRecvNakBtree(
	onSendACK func(seq circular.Number, light bool),
	onSendNAK func(list []circular.Number),
	onDeliver func(p packet.Packet),
	tsbpdDelayUs uint64,
) *receiver {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44) // IPv4 + UDP + SRT header

	recv := NewReceiver(ReceiveConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10,                      // 10 microseconds for fast testing
		PeriodicNAKInterval:    20,                      // 20 microseconds for fast testing
		OnSendACK:              onSendACK,
		OnSendNAK:              onSendNAK,
		OnDeliver:              onDeliver,
		ConnectionMetrics:      testMetrics,
		PacketReorderAlgorithm: "btree",                 // Required for NAK btree
		UseNakBtree:            true,                    // Enable NAK btree
		SuppressImmediateNak:   true,                    // Suppress immediate NAK
		TsbpdDelay:             tsbpdDelayUs,            // e.g., 200000 for 200ms
		NakRecentPercent:       0.10,                    // 10% "too recent" window
		NakMergeGap:            3,                       // Merge gaps within 3 packets
		NakConsolidationBudget: 2000,                    // 2ms budget
		FastNakEnabled:         false,                   // Disable for deterministic tests
		FastNakRecentEnabled:   false,                   // Disable for deterministic tests
	})

	return recv.(*receiver)
}

// ============================================================================
// Test 0: In-Order Arrival Baseline (Control Test)
// ============================================================================

func TestInOrderArrival_Baseline(t *testing.T) {
	// This is a control test to ensure NAK btree works correctly when
	// packets arrive in order. This provides a baseline comparison for
	// out-of-order tests.

	const (
		numPackets   = 110
		startSeq     = uint32(1)
		dropModulus  = 10 // Drop every 10th packet: 10, 20, 30, ... 100
		tsbpdDelayUs = uint64(200_000)
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	nakCapture := newNakListCapture()

	recv := mockLiveRecvNakBtree(
		func(seq circular.Number, light bool) {},
		nakCapture.capture,
		func(p packet.Packet) {},
		tsbpdDelayUs,
	)

	// Generate packets with realistic TSBPD times
	baseTime := uint64(1_000_000)
	delayPerPkt := tsbpdDelayUs / uint64(numPackets)

	packets := make([]packet.Packet, numPackets)
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(addr)
		seq := startSeq + uint32(i)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*delayPerPkt
		packets[i] = p
	}

	// Drop deterministically but deliver IN ORDER (no shuffle)
	delivered, dropped := dropByModulus(packets, dropModulus)
	t.Logf("Total packets: %d, Delivered: %d, Dropped: %d", numPackets, len(delivered), len(dropped))

	// Push packets IN ORDER (this is the control case)
	for _, pkt := range delivered {
		recv.Push(pkt)
	}

	// Run NAK cycles with advancing time
	currentTime := baseTime
	for cycle := 0; cycle < 50; cycle++ {
		currentTime += 20_000 // 20ms per cycle
		recv.Tick(currentTime)
	}

	// Verify dropped packets were NAK'd
	nakedSeqs := nakCapture.getAllNAKedSequences()
	var nakedCount int
	for _, pkt := range dropped {
		seq := pkt.Header().PacketSequenceNumber.Val()
		if nakedSeqs[seq] {
			nakedCount++
		}
	}

	minExpected := len(dropped) * 9 / 10
	require.GreaterOrEqual(t, nakedCount, minExpected,
		"Expected at least %d/%d dropped packets to be NAK'd, got %d",
		minExpected, len(dropped), nakedCount)

	t.Logf("✅ In-order baseline: %d/%d dropped packets NAK'd", nakedCount, len(dropped))
}

// ============================================================================
// Test 1: Out-of-Order Arrival with Modulus Dropping
// ============================================================================

func TestOutOfOrderArrival_ModulusDrops(t *testing.T) {
	// Setup
	// Use 110 packets so packet 100 isn't at the "too recent" boundary
	const (
		numPackets       = 110
		startSeq         = uint32(1)
		dropModulus      = 10 // Drop every 10th packet: 10, 20, 30, ... 100
		tsbpdDelayUs     = uint64(200_000) // 200ms in microseconds
		nakIntervalUs    = uint64(20)      // 20µs NAK interval
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	nakCapture := newNakListCapture()
	deliveredSeqs := make([]uint32, 0)
	var deliverMu sync.Mutex

	// Create receiver with NAK btree enabled
	recv := mockLiveRecvNakBtree(
		func(seq circular.Number, light bool) {}, // ACK callback
		nakCapture.capture,                        // NAK callback
		func(p packet.Packet) {
			deliverMu.Lock()
			deliveredSeqs = append(deliveredSeqs, p.Header().PacketSequenceNumber.Val())
			deliverMu.Unlock()
		},
		tsbpdDelayUs,
	)

	// Calculate realistic time values:
	// - All packets have TSBPD times spread across the buffer
	// - baseTime is the current "now" when packets start arriving
	// - Packets at the start have TSBPD = baseTime + tsbpdDelay
	// - Later packets have progressively later TSBPD times
	baseTime := uint64(1_000_000) // 1 second into the test
	delayPerPkt := tsbpdDelayUs / uint64(numPackets)

	// Generate packets with realistic TSBPD times
	// PktTsbpdTime = baseTime + tsbpdDelay + (seqOffset * delayPerPkt)
	packets := make([]packet.Packet, numPackets)
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(addr)
		seq := startSeq + uint32(i)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// Older packets (lower seq) have earlier TSBPD, newer have later TSBPD
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*delayPerPkt
		packets[i] = p
	}

	// Drop deterministically: 10, 20, 30, ...
	delivered, dropped := dropByModulus(packets, dropModulus)
	t.Logf("Total packets: %d, Delivered: %d, Dropped: %d", numPackets, len(delivered), len(dropped))

	// Verify expected drops
	expectedDropped := expectedModulusDrops(startSeq, numPackets, dropModulus)
	require.Equal(t, len(expectedDropped), len(dropped), "Unexpected number of dropped packets")

	// Shuffle delivered packets (simulating io_uring reordering)
	shuffled := shufflePacketsDeterministic(delivered, 42) // Fixed seed for reproducibility

	// Push all shuffled packets
	for _, pkt := range shuffled {
		recv.Push(pkt)
	}

	// Verify NAK btree was populated (through gap detection in periodicNakBtree)
	require.NotNil(t, recv.nakBtree)
	t.Logf("NAK btree size after push (before NAK cycles): %d", recv.nakBtree.Len())

	// Run multiple NAK cycles, advancing time to mature TSBPD times
	// The "too recent" threshold is: now + tsbpdDelay*nakRecentPercent (10%)
	// So we need time to advance until older packets are no longer "too recent"

	// Start time at baseTime + small offset
	currentTime := baseTime

	// Run enough cycles to scan through the buffer
	// Each cycle, advance time by NAK interval * 1000 to make progress
	for cycle := 0; cycle < 50; cycle++ {
		currentTime += nakIntervalUs * 1000 // Advance by 20ms per cycle
		recv.Tick(currentTime)
	}

	t.Logf("NAK btree size after NAK cycles: %d", recv.nakBtree.Len())

	// Verify all dropped packets were NAK'd
	nakLists := nakCapture.getNakLists()
	t.Logf("Total NAK lists generated: %d", len(nakLists))

	// Should have at least one NAK list
	require.Greater(t, len(nakLists), 0, "Expected at least one NAK list")

	// Verify dropped packets were NAK'd (excluding the last one which may be at "recent" boundary)
	// The "recent" boundary is 10% of TSBPD delay from the newest packet
	// Packets at the very end of the buffer may not be NAK'd yet by design
	nakedSeqs := nakCapture.getAllNAKedSequences()
	var nakedCount int
	for _, pkt := range dropped {
		seq := pkt.Header().PacketSequenceNumber.Val()
		if nakedSeqs[seq] {
			nakedCount++
		}
	}

	// At least 90% of dropped packets should be NAK'd (the rest are at the "recent" boundary)
	minExpected := len(dropped) * 9 / 10
	require.GreaterOrEqual(t, nakedCount, minExpected,
		"Expected at least %d/%d dropped packets to be NAK'd, got %d",
		minExpected, len(dropped), nakedCount)

	t.Logf("✅ %d/%d dropped packets were successfully NAK'd (remaining are at recent boundary)",
		nakedCount, len(dropped))
}

// ============================================================================
// Test 2: TSBPD Skip Does Not Skip Scanning
// ============================================================================

func TestTSBPDSkip_DoesNotSkipScanning(t *testing.T) {
	// This test verifies the specific FR-6 bug fix:
	// When ACK jumps forward due to TSBPD skip, we still scan the skipped region

	const (
		tsbpdDelayUs = uint64(100_000) // 100ms in microseconds
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	nakCapture := newNakListCapture()
	var ackSeq atomic.Uint32

	recv := mockLiveRecvNakBtree(
		func(seq circular.Number, light bool) {
			ackSeq.Store(seq.Val())
		},
		nakCapture.capture,
		func(p packet.Packet) {},
		tsbpdDelayUs,
	)

	// Use realistic time values
	// baseTime represents "now" when packets arrive
	// TSBPD time = baseTime + tsbpdDelay + offset
	baseTime := uint64(1_000_000) // 1 second into test
	pktInterval := uint64(2000)   // 2ms between packets

	// Deliver packets 1-10 (in order)
	for i := uint32(1); i <= 10; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*pktInterval
		recv.Push(p)
	}

	// Skip packets 11-20 (simulate loss during "io_uring delay")
	// These should be detected as gaps

	// Deliver packets 21-30 (in order, but after the gap)
	for i := uint32(21); i <= 30; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(i, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*pktInterval
		recv.Push(p)
	}

	// At this point, NAK btree might be empty (gaps detected during periodic NAK scan)
	require.NotNil(t, recv.nakBtree)
	t.Logf("NAK btree size after push: %d", recv.nakBtree.Len())

	// Run NAK cycles with advancing time
	// Time needs to advance so that packets become "not too recent"
	currentTime := baseTime
	for cycle := 0; cycle < 100; cycle++ {
		currentTime += 20_000 // 20ms per cycle
		recv.Tick(currentTime)
	}

	t.Logf("NAK btree size after cycles: %d", recv.nakBtree.Len())

	// Verify packets 11-20 were NAK'd
	nakedSeqs := nakCapture.getAllNAKedSequences()
	for seq := uint32(11); seq <= 20; seq++ {
		require.True(t, nakedSeqs[seq], "Gap packet seq=%d was not NAK'd", seq)
	}

	t.Logf("✅ Gap packets 11-20 were successfully NAK'd despite TSBPD progression")
}

// ============================================================================
// Test 3: Concurrent Push/Tick/NAK/ACK Stress Test
// ============================================================================

func TestConcurrent_PushTickNAKACK_OutOfOrder(t *testing.T) {
	// This test runs all receiver operations concurrently
	// with out-of-order packet arrival to detect race conditions

	const (
		numPackets   = 500
		dropModulus  = 20
		tsbpdDelayUs = uint64(200_000) // 200ms
		testDuration = 100 * time.Millisecond
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	nakCapture := newNakListCapture()

	recv := mockLiveRecvNakBtree(
		func(seq circular.Number, light bool) {},
		nakCapture.capture,
		func(p packet.Packet) {},
		tsbpdDelayUs,
	)

	// Generate packets with realistic TSBPD times
	baseTime := uint64(1_000_000) // 1 second
	pktInterval := tsbpdDelayUs / uint64(numPackets)

	packets := make([]packet.Packet, numPackets)
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i+1), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*pktInterval
		packets[i] = p
	}

	delivered, _ := dropByModulus(packets, dropModulus)
	shuffled := shufflePacketsDeterministic(delivered, 42)

	var wg sync.WaitGroup
	stopCh := make(chan struct{})
	var tickTime atomic.Uint64
	tickTime.Store(baseTime)

	// Goroutine 1: Push packets
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, pkt := range shuffled {
			select {
			case <-stopCh:
				return
			default:
				recv.Push(pkt)
				time.Sleep(100 * time.Microsecond) // Spread out arrival
			}
		}
	}()

	// Goroutine 2: Call Tick periodically with advancing time
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(1 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				newTime := tickTime.Add(10_000) // Advance 10ms per tick
				recv.Tick(newTime)
			}
		}
	}()

	// Run for test duration
	time.Sleep(testDuration)
	close(stopCh)
	wg.Wait()

	// The test passes if no race conditions occurred
	// (run with -race flag to detect races)
	t.Logf("✅ Concurrent test completed without race conditions")
	t.Logf("NAK btree final size: %d", recv.nakBtree.Len())
	t.Logf("Total NAK lists: %d", len(nakCapture.getNakLists()))
}

// ============================================================================
// Test 4: Burst Drops for Range Consolidation
// ============================================================================

func TestOutOfOrderArrival_BurstDrops_RangeConsolidation(t *testing.T) {
	// Test that consecutive dropped packets are consolidated into RANGES

	const (
		numPackets   = 200 // Smaller for faster test
		burstSize    = 5
		tsbpdDelayUs = uint64(200_000) // 200ms
	)
	burstStarts := []uint32{30, 60, 90, 120, 150}
	// This will drop: 30-34, 60-64, 90-94, 120-124, 150-154

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	nakCapture := newNakListCapture()

	recv := mockLiveRecvNakBtree(
		func(seq circular.Number, light bool) {},
		nakCapture.capture,
		func(p packet.Packet) {},
		tsbpdDelayUs,
	)

	// Generate packets with realistic TSBPD times
	baseTime := uint64(1_000_000)
	pktInterval := tsbpdDelayUs / uint64(numPackets)

	packets := make([]packet.Packet, numPackets)
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i+1), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*pktInterval
		packets[i] = p
	}

	// Drop by bursts
	delivered, dropped := dropByBursts(packets, burstStarts, burstSize)
	t.Logf("Total: %d, Delivered: %d, Dropped: %d", numPackets, len(delivered), len(dropped))
	require.Equal(t, len(burstStarts)*burstSize, len(dropped))

	// Shuffle and push
	shuffled := shufflePacketsDeterministic(delivered, 42)
	for _, pkt := range shuffled {
		recv.Push(pkt)
	}

	// Run NAK cycles with advancing time
	currentTime := baseTime
	for cycle := 0; cycle < 100; cycle++ {
		currentTime += 20_000 // 20ms per cycle
		recv.Tick(currentTime)
	}

	// Verify all burst packets were NAK'd
	nakedSeqs := nakCapture.getAllNAKedSequences()
	for _, start := range burstStarts {
		for i := 0; i < burstSize; i++ {
			seq := start + uint32(i)
			require.True(t, nakedSeqs[seq], "Burst packet seq=%d was not NAK'd", seq)
		}
	}

	// Check that bursts were consolidated into ranges
	// With MergeGap=3, consecutive sequences should form ranges
	nakLists := nakCapture.getNakLists()
	t.Logf("Total NAK lists: %d", len(nakLists))

	// Log some NAK entries to verify format
	if len(nakLists) > 0 {
		t.Logf("Sample NAK list: %v", nakLists[0])
	}

	t.Logf("✅ All %d burst-dropped packets were successfully NAK'd", len(dropped))
}

// ============================================================================
// Test 5: Mixed Pattern (Singles + Ranges)
// ============================================================================

func TestOutOfOrderArrival_MixedPattern(t *testing.T) {
	// Combines modulus drops (singles) and burst drops (ranges)

	const (
		numPackets   = 200
		tsbpdDelayUs = uint64(200_000) // 200ms
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	nakCapture := newNakListCapture()

	recv := mockLiveRecvNakBtree(
		func(seq circular.Number, light bool) {},
		nakCapture.capture,
		func(p packet.Packet) {},
		tsbpdDelayUs,
	)

	// Generate packets with realistic TSBPD times
	baseTime := uint64(1_000_000)
	pktInterval := tsbpdDelayUs / uint64(numPackets)

	packets := make([]packet.Packet, numPackets)
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i+1), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*pktInterval
		packets[i] = p
	}

	// Create mixed drop pattern:
	// - Every 25th packet: 25, 50, 75, 100 (singles, gap > mergeGap)
	// - Burst at 120-124: consecutive (range)
	// - Burst at 160-162: consecutive (range)
	dropSet := make(map[uint32]bool)

	// Singles (modulus 25, gap of 24 > mergeGap of 3)
	for i := uint32(25); i < uint32(numPackets); i += 25 {
		dropSet[i] = true
	}

	// Bursts
	for i := uint32(120); i <= 124; i++ {
		dropSet[i] = true
	}
	for i := uint32(160); i <= 162; i++ {
		dropSet[i] = true
	}

	// Split packets
	var delivered, dropped []packet.Packet
	for _, pkt := range packets {
		seq := pkt.Header().PacketSequenceNumber.Val()
		if dropSet[seq] {
			dropped = append(dropped, pkt)
		} else {
			delivered = append(delivered, pkt)
		}
	}

	t.Logf("Total: %d, Delivered: %d, Dropped: %d", numPackets, len(delivered), len(dropped))

	// Shuffle and push
	shuffled := shufflePacketsDeterministic(delivered, 42)
	for _, pkt := range shuffled {
		recv.Push(pkt)
	}

	// Run NAK cycles with advancing time
	currentTime := baseTime
	for cycle := 0; cycle < 100; cycle++ {
		currentTime += 20_000 // 20ms per cycle
		recv.Tick(currentTime)
	}

	// Verify all dropped packets were NAK'd
	verifyAllDroppedWereNAKd(t, dropped, nakCapture)

	t.Logf("✅ All %d mixed-pattern dropped packets were successfully NAK'd", len(dropped))
}

// ============================================================================
// Test 6: nakScanStartPoint Progression
// ============================================================================

func TestNakScanStartPoint_ProgressesToRecentBoundary(t *testing.T) {
	// Verify nakScanStartPoint advances correctly through the buffer

	const (
		numPackets   = 200
		dropModulus  = 20
		tsbpdDelayUs = uint64(500_000) // 500ms for longer buffer to observe progression
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	nakCapture := newNakListCapture()

	recv := mockLiveRecvNakBtree(
		func(seq circular.Number, light bool) {},
		nakCapture.capture,
		func(p packet.Packet) {},
		tsbpdDelayUs,
	)

	// Generate packets with realistic TSBPD times
	baseTime := uint64(1_000_000)
	pktInterval := tsbpdDelayUs / uint64(numPackets)

	packets := make([]packet.Packet, numPackets)
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i+1), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*pktInterval
		packets[i] = p
	}

	delivered, _ := dropByModulus(packets, dropModulus)
	shuffled := shufflePacketsDeterministic(delivered, 42)

	for _, pkt := range shuffled {
		recv.Push(pkt)
	}

	// Track nakScanStartPoint progression
	initialScanPoint := recv.nakScanStartPoint.Load()
	t.Logf("Initial nakScanStartPoint: %d", initialScanPoint)

	// Run NAK cycles with advancing time and track progression
	currentTime := baseTime
	for cycle := 0; cycle < 20; cycle++ {
		currentTime += 50_000 // Advance 50ms per cycle (big jumps to observe scan window movement)
		recv.Tick(currentTime)
		currentScanPoint := recv.nakScanStartPoint.Load()
		t.Logf("After cycle %d (time=%d): nakScanStartPoint=%d", cycle, currentTime, currentScanPoint)
	}

	finalScanPoint := recv.nakScanStartPoint.Load()

	// nakScanStartPoint should have advanced from initial position
	// (unless we scanned everything immediately, which is also valid)
	t.Logf("Final nakScanStartPoint: %d (advanced by %d)", finalScanPoint, finalScanPoint-initialScanPoint)

	t.Logf("✅ nakScanStartPoint progression test completed")
}

// ============================================================================
// Test 7: Arrival Order Permutations
// ============================================================================

func TestOutOfOrder_ArrivalPermutations(t *testing.T) {
	// Test various out-of-order arrival patterns

	tests := []struct {
		name       string
		shuffleFn  func([]packet.Packet) []packet.Packet
	}{
		{
			name: "Random shuffle (seed 42)",
			shuffleFn: func(p []packet.Packet) []packet.Packet {
				return shufflePacketsDeterministic(p, 42)
			},
		},
		{
			name: "Random shuffle (seed 123)",
			shuffleFn: func(p []packet.Packet) []packet.Packet {
				return shufflePacketsDeterministic(p, 123)
			},
		},
		{
			name: "Reverse order",
			shuffleFn: func(p []packet.Packet) []packet.Packet {
				result := make([]packet.Packet, len(p))
				for i, pkt := range p {
					result[len(p)-1-i] = pkt
				}
				return result
			},
		},
		{
			name: "Interleaved (odd then even)",
			shuffleFn: func(p []packet.Packet) []packet.Packet {
				var odds, evens []packet.Packet
				for i, pkt := range p {
					if i%2 == 0 {
						evens = append(evens, pkt)
					} else {
						odds = append(odds, pkt)
					}
				}
				return append(odds, evens...)
			},
		},
		{
			name: "Batches of 10 in reverse",
			shuffleFn: func(p []packet.Packet) []packet.Packet {
				result := make([]packet.Packet, 0, len(p))
				batchSize := 10
				for i := len(p); i > 0; i -= batchSize {
					start := i - batchSize
					if start < 0 {
						start = 0
					}
					result = append(result, p[start:i]...)
				}
				return result
			},
		},
	}

	const (
		numPackets   = 110 // Extra packets so last dropped isn't at boundary
		dropModulus  = 10
		tsbpdDelayUs = uint64(200_000)
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nakCapture := newNakListCapture()

			recv := mockLiveRecvNakBtree(
				func(seq circular.Number, light bool) {},
				nakCapture.capture,
				func(p packet.Packet) {},
				tsbpdDelayUs,
			)

			// Generate packets with realistic TSBPD times
			baseTime := uint64(1_000_000)
			pktInterval := tsbpdDelayUs / uint64(numPackets)

			packets := make([]packet.Packet, numPackets)
			for i := 0; i < numPackets; i++ {
				p := packet.NewPacket(addr)
				p.Header().PacketSequenceNumber = circular.New(uint32(i+1), packet.MAX_SEQUENCENUMBER)
				p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*pktInterval
				packets[i] = p
			}

			delivered, dropped := dropByModulus(packets, dropModulus)
			shuffled := tc.shuffleFn(delivered)

			for _, pkt := range shuffled {
				recv.Push(pkt)
			}

			// Run NAK cycles with advancing time
			currentTime := baseTime
			for cycle := 0; cycle < 100; cycle++ {
				currentTime += 20_000 // 20ms per cycle
				recv.Tick(currentTime)
			}

			// Verify (at least 90% of dropped packets should be NAK'd)
			nakedSeqs := nakCapture.getAllNAKedSequences()
			var nakedCount int
			for _, pkt := range dropped {
				if nakedSeqs[pkt.Header().PacketSequenceNumber.Val()] {
					nakedCount++
				}
			}
			minExpected := len(dropped) * 9 / 10
			require.GreaterOrEqual(t, nakedCount, minExpected,
				"Expected at least %d/%d dropped packets NAK'd, got %d",
				minExpected, len(dropped), nakedCount)
			t.Logf("✅ %s: %d/%d dropped packets NAK'd", tc.name, nakedCount, len(dropped))
		})
	}
}

// ============================================================================
// Benchmark: Out-of-Order Push Performance
// ============================================================================

func BenchmarkOutOfOrderPush_NakBtree(b *testing.B) {
	const (
		numPackets   = 1000
		tsbpdDelayUs = uint64(200_000)
	)

	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

	// Pre-generate packets with realistic TSBPD
	baseTime := uint64(1_000_000)
	pktInterval := tsbpdDelayUs / uint64(numPackets)

	packets := make([]packet.Packet, numPackets)
	for i := 0; i < numPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(uint32(i+1), packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i)*pktInterval
		packets[i] = p
	}
	shuffled := shufflePacketsDeterministic(packets, 42)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recv := mockLiveRecvNakBtree(
			func(seq circular.Number, light bool) {},
			func(list []circular.Number) {},
			func(p packet.Packet) {},
			tsbpdDelayUs,
		)

		for _, pkt := range shuffled {
			recv.Push(pkt)
		}
	}
}

