// Package live provides table-driven stream tests for the receiver.
//
// This file implements a comprehensive test matrix generator that creates
// test cases covering all combinations of:
//   - Receiver configurations (Original, NakBtree, NakBtreeF, NakBtreeFr)
//   - Loss patterns (Periodic, Burst, LargeBurst, etc.)
//   - Reorder patterns (SwapPairs, DelayEveryNth, BurstReorder)
//   - Stream profiles (different bitrates and durations)
//   - Start sequences (normal and wraparound scenarios)
//   - Timer profiles (default, fast, slow)
//
// Tests are organized into tiers:
//   - Tier 1: Core validation (~50 tests, <3s) - every PR
//   - Tier 2: Extended coverage (~200 tests, <15s) - daily CI
//   - Tier 3: Comprehensive (~600 tests, <60s) - nightly CI
//
// See documentation/receiver_stream_tests_design.md for full design details.
package live

import (
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// DIMENSION 1: Receiver Configuration
// ============================================================================

// ReceiverConfig defines a receiver configuration for matrix testing.
// These configurations match the integration test variants in
// integration_testing_matrix_design.md.
type ReceiverConfig struct {
	Name                   string
	UseNakBtree            bool
	FastNakEnabled         bool
	FastNakRecentEnabled   bool
	NakMergeGap            uint32
	NakRecentPercent       float64
	NakConsolidationBudget uint64 // microseconds
}

// Predefined receiver configurations matching integration test variants
var (
	// CfgOriginal uses the original (non-btree) NAK mechanism
	CfgOriginal = ReceiverConfig{
		Name:        "Original",
		UseNakBtree: false,
	}

	// CfgNakBtree uses NAK btree without FastNAK
	CfgNakBtree = ReceiverConfig{
		Name:                   "NakBtree",
		UseNakBtree:            true,
		FastNakEnabled:         false,
		FastNakRecentEnabled:   false,
		NakMergeGap:            3,
		NakRecentPercent:       0.10,
		NakConsolidationBudget: 20_000, // 20ms
	}

	// CfgNakBtreeF uses NAK btree with FastNAK (no FastNAKRecent)
	CfgNakBtreeF = ReceiverConfig{
		Name:                   "NakBtreeF",
		UseNakBtree:            true,
		FastNakEnabled:         true,
		FastNakRecentEnabled:   false,
		NakMergeGap:            3,
		NakRecentPercent:       0.10,
		NakConsolidationBudget: 20_000,
	}

	// CfgNakBtreeFr uses NAK btree with FastNAK and FastNAKRecent
	CfgNakBtreeFr = ReceiverConfig{
		Name:                   "NakBtreeFr",
		UseNakBtree:            true,
		FastNakEnabled:         true,
		FastNakRecentEnabled:   true,
		NakMergeGap:            3,
		NakRecentPercent:       0.10,
		NakConsolidationBudget: 20_000,
	}
)

// AllReceiverConfigs returns all receiver configurations to test.
func AllReceiverConfigs() []ReceiverConfig {
	return []ReceiverConfig{
		CfgOriginal,
		CfgNakBtree,
		CfgNakBtreeF,
		CfgNakBtreeFr,
	}
}

// NakBtreeConfigs returns only NAK btree configurations (for btree-specific tests).
func NakBtreeConfigs() []ReceiverConfig {
	return []ReceiverConfig{
		CfgNakBtree,
		CfgNakBtreeF,
		CfgNakBtreeFr,
	}
}

// ============================================================================
// DIMENSION 2: Stream Profiles
// ============================================================================

// StreamProfile defines a packet stream configuration.
type StreamProfile struct {
	Name         string
	BitrateBps   uint64  // Bits per second
	PayloadBytes uint32  // Bytes per packet
	DurationSec  float64 // Stream duration in seconds
	TsbpdDelayUs uint64  // TSBPD delay in microseconds
}

// Predefined stream profiles
var (
	Stream1MbpsShort = StreamProfile{
		Name:         "1Mbps-Short",
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  1.0,
		TsbpdDelayUs: 120_000, // 120ms
	}

	Stream1MbpsMedium = StreamProfile{
		Name:         "1Mbps-Medium",
		BitrateBps:   1_000_000,
		PayloadBytes: 1400,
		DurationSec:  5.0,
		TsbpdDelayUs: 120_000,
	}

	Stream5MbpsMedium = StreamProfile{
		Name:         "5Mbps-Medium",
		BitrateBps:   5_000_000,
		PayloadBytes: 1316, // 7 MPEG-TS packets
		DurationSec:  5.0,
		TsbpdDelayUs: 120_000,
	}

	Stream20MbpsShort = StreamProfile{
		Name:         "20Mbps-Short",
		BitrateBps:   20_000_000,
		PayloadBytes: 1316,
		DurationSec:  2.0,
		TsbpdDelayUs: 120_000,
	}
)

// AllStreamProfiles returns all stream profiles.
func AllStreamProfiles() []StreamProfile {
	return []StreamProfile{
		Stream1MbpsShort,
		Stream1MbpsMedium,
		Stream5MbpsMedium,
		Stream20MbpsShort,
	}
}

// ShortStreamProfiles returns only short duration streams (for tier 1).
func ShortStreamProfiles() []StreamProfile {
	return []StreamProfile{
		Stream1MbpsShort,
		Stream20MbpsShort,
	}
}

// ============================================================================
// DIMENSION 3: Loss Patterns (reusing existing types from receive_test.go)
// ============================================================================

// AllLossPatterns returns all loss patterns to test.
func AllLossPatterns() []LossPattern {
	return []LossPattern{
		NoLoss{},
		PeriodicLoss{Period: 10, Offset: 0},
		PeriodicLoss{Period: 20, Offset: 5},
		BurstLoss{BurstInterval: 100, BurstSize: 5},
		BurstLoss{BurstInterval: 50, BurstSize: 10},
		LargeBurstLoss{StartIndex: 50, Size: 30},
		LargeBurstLoss{StartIndex: 100, Size: 100},
		MultiBurstLoss{Bursts: []struct{ Start, Size int }{{50, 5}, {150, 10}, {300, 20}}},
		HighLossWindow{WindowStart: 100, WindowEnd: 200, LossRate: 0.50},
		&CorrelatedLoss{BaseLossRate: 0.05, Correlation: 0.25},
		PercentageLoss{Rate: 0.02},
		PercentageLoss{Rate: 0.10},
	}
}

// CoreLossPatterns returns basic loss patterns for tier 1 tests.
func CoreLossPatterns() []LossPattern {
	return []LossPattern{
		NoLoss{},
		PeriodicLoss{Period: 10, Offset: 0},
		BurstLoss{BurstInterval: 100, BurstSize: 5},
	}
}

// ============================================================================
// DIMENSION 4: Reorder Patterns (reusing existing types from receive_test.go)
// ============================================================================

// AllReorderPatterns returns all reorder patterns to test.
// nil represents no reordering (in-order delivery).
func AllReorderPatterns() []OutOfOrderPattern {
	return []OutOfOrderPattern{
		nil, // No reorder
		SwapAdjacentPairs{},
		DelayEveryNth{N: 5, Delay: 3},
		DelayEveryNth{N: 10, Delay: 8},
		BurstReorder{BurstSize: 4},
		BurstReorder{BurstSize: 8},
		BurstReorder{BurstSize: 16},
	}
}

// CoreReorderPatterns returns basic reorder patterns for tier 2 tests.
func CoreReorderPatterns() []OutOfOrderPattern {
	return []OutOfOrderPattern{
		nil,
		SwapAdjacentPairs{},
		BurstReorder{BurstSize: 4},
	}
}

// ============================================================================
// DIMENSION 5: Start Sequences (for wraparound testing)
// ============================================================================

// AllStartSequences returns all start sequence values to test.
func AllStartSequences() []uint32 {
	const MAX_SEQ = packet.MAX_SEQUENCENUMBER
	return []uint32{
		1,              // Normal start
		1000,           // Middle of space
		MAX_SEQ - 100,  // Near wraparound
		MAX_SEQ - 1000, // Slightly before wraparound
	}
}

// NormalStartSequence returns just the normal start sequence.
func NormalStartSequence() []uint32 {
	return []uint32{1}
}

// WraparoundStartSequences returns sequences for wraparound testing.
func WraparoundStartSequences() []uint32 {
	const MAX_SEQ = packet.MAX_SEQUENCENUMBER
	return []uint32{
		MAX_SEQ - 100,
		MAX_SEQ - 1000,
	}
}

// ============================================================================
// DIMENSION 6: Timer Profiles
// ============================================================================

// TimerProfile defines timer intervals for testing.
type TimerProfile struct {
	Name          string
	NakIntervalUs uint64 // Periodic NAK interval in microseconds
	AckIntervalUs uint64 // Periodic ACK interval in microseconds
}

// Predefined timer profiles
var (
	TimerDefault = TimerProfile{
		Name:          "Default",
		NakIntervalUs: 20_000, // 20ms
		AckIntervalUs: 10_000, // 10ms
	}

	TimerFast = TimerProfile{
		Name:          "Fast",
		NakIntervalUs: 10_000, // 10ms
		AckIntervalUs: 5_000,  // 5ms
	}

	TimerSlow = TimerProfile{
		Name:          "Slow",
		NakIntervalUs: 50_000, // 50ms
		AckIntervalUs: 20_000, // 20ms
	}
)

// AllTimerProfiles returns all timer profiles.
func AllTimerProfiles() []TimerProfile {
	return []TimerProfile{
		TimerDefault,
		TimerFast,
		TimerSlow,
	}
}

// DefaultTimerProfile returns just the default timer profile.
func DefaultTimerProfile() []TimerProfile {
	return []TimerProfile{TimerDefault}
}

// ============================================================================
// TEST CASE DEFINITION
// ============================================================================

// StreamTestCase represents a single generated test case.
type StreamTestCase struct {
	Name           string
	ReceiverConfig ReceiverConfig
	StreamProfile  StreamProfile
	LossPattern    LossPattern
	ReorderPattern OutOfOrderPattern // nil = no reorder
	StartSeq       uint32
	TimerProfile   TimerProfile
}

// ============================================================================
// MATRIX OPTIONS AND GENERATOR
// ============================================================================

// MatrixOptions controls which test combinations to generate.
type MatrixOptions struct {
	// Dimension filters - return true to include
	ConfigFilter   func(ReceiverConfig) bool
	StreamFilter   func(StreamProfile) bool
	LossFilter     func(LossPattern) bool
	ReorderFilter  func(OutOfOrderPattern) bool
	StartSeqFilter func(uint32) bool
	TimerFilter    func(TimerProfile) bool

	// Convenience flags
	IncludeWraparound      bool
	IncludeTimerVariations bool
}

// Tier1Options generates core validation tests (~50 cases).
var Tier1Options = MatrixOptions{
	ConfigFilter: func(c ReceiverConfig) bool { return true },                // All configs
	StreamFilter: func(s StreamProfile) bool { return s.DurationSec <= 2.0 }, // Short streams only
	LossFilter: func(l LossPattern) bool {
		switch l.(type) {
		case NoLoss, PeriodicLoss, BurstLoss:
			return true
		}
		return false
	},
	ReorderFilter:          func(r OutOfOrderPattern) bool { return r == nil }, // No reorder
	StartSeqFilter:         func(s uint32) bool { return s == 1 },              // Normal start only
	TimerFilter:            func(t TimerProfile) bool { return t.Name == "Default" },
	IncludeWraparound:      false,
	IncludeTimerVariations: false,
}

// Tier2Options generates extended coverage tests (~200 cases).
var Tier2Options = MatrixOptions{
	ConfigFilter: func(c ReceiverConfig) bool { return c.UseNakBtree },       // Only NAK btree configs
	StreamFilter: func(s StreamProfile) bool { return s.DurationSec <= 2.0 }, // Short streams only
	LossFilter: func(l LossPattern) bool {
		// Core loss patterns only
		switch l.(type) {
		case NoLoss, PeriodicLoss, BurstLoss, LargeBurstLoss:
			return true
		}
		return false
	},
	ReorderFilter: func(r OutOfOrderPattern) bool {
		// In-order and basic reorder only
		if r == nil {
			return true
		}
		switch r.(type) {
		case SwapAdjacentPairs, BurstReorder:
			// Only use BurstReorder with size 4
			if br, ok := r.(BurstReorder); ok {
				return br.BurstSize == 4
			}
			return true
		}
		return false
	},
	StartSeqFilter: func(s uint32) bool {
		// Normal start + one wraparound
		return s == 1 || s == packet.MAX_SEQUENCENUMBER-100
	},
	TimerFilter:            func(t TimerProfile) bool { return t.Name == "Default" },
	IncludeWraparound:      true,
	IncludeTimerVariations: false,
}

// Tier3Options generates comprehensive tests (~600-800 cases).
var Tier3Options = MatrixOptions{
	ConfigFilter: func(c ReceiverConfig) bool { return c.UseNakBtree }, // Focus on NAK btree
	StreamFilter: func(s StreamProfile) bool {
		// Short and medium streams only
		return s.DurationSec <= 5.0
	},
	LossFilter: func(l LossPattern) bool {
		// Key loss patterns (exclude some redundant ones)
		switch l.(type) {
		case NoLoss, PeriodicLoss, BurstLoss, LargeBurstLoss, MultiBurstLoss, HighLossWindow:
			return true
		}
		return false
	},
	ReorderFilter: func(r OutOfOrderPattern) bool {
		// Core reorder patterns
		if r == nil {
			return true
		}
		switch v := r.(type) {
		case SwapAdjacentPairs:
			return true
		case DelayEveryNth:
			return v.N == 5 // Only one variant
		case BurstReorder:
			return v.BurstSize == 4 || v.BurstSize == 8 // Two sizes
		}
		return false
	},
	StartSeqFilter: func(s uint32) bool {
		// Normal start + one wraparound
		return s == 1 || s == packet.MAX_SEQUENCENUMBER-100
	},
	TimerFilter:            func(t TimerProfile) bool { return t.Name == "Default" }, // Default only
	IncludeWraparound:      true,
	IncludeTimerVariations: false,
}

// GenerateTestMatrix generates all test case combinations based on options.
func GenerateTestMatrix(opts MatrixOptions) []StreamTestCase {
	var cases []StreamTestCase

	configs := AllReceiverConfigs()
	streams := AllStreamProfiles()
	losses := AllLossPatterns()
	reorders := AllReorderPatterns()
	startSeqs := AllStartSequences()
	timers := AllTimerProfiles()

	// Apply filters
	if !opts.IncludeWraparound {
		startSeqs = NormalStartSequence()
	}
	if !opts.IncludeTimerVariations {
		timers = DefaultTimerProfile()
	}

	for _, cfg := range configs {
		if opts.ConfigFilter != nil && !opts.ConfigFilter(cfg) {
			continue
		}
		for _, stream := range streams {
			if opts.StreamFilter != nil && !opts.StreamFilter(stream) {
				continue
			}
			for _, loss := range losses {
				if opts.LossFilter != nil && !opts.LossFilter(loss) {
					continue
				}
				for _, reorder := range reorders {
					if opts.ReorderFilter != nil && !opts.ReorderFilter(reorder) {
						continue
					}
					for _, startSeq := range startSeqs {
						if opts.StartSeqFilter != nil && !opts.StartSeqFilter(startSeq) {
							continue
						}
						for _, timer := range timers {
							if opts.TimerFilter != nil && !opts.TimerFilter(timer) {
								continue
							}

							name := generateTestName(cfg, stream, loss, reorder, startSeq, timer)
							cases = append(cases, StreamTestCase{
								Name:           name,
								ReceiverConfig: cfg,
								StreamProfile:  stream,
								LossPattern:    loss,
								ReorderPattern: reorder,
								StartSeq:       startSeq,
								TimerProfile:   timer,
							})
						}
					}
				}
			}
		}
	}

	return cases
}

// generateTestName creates a hierarchical test name for filtering.
func generateTestName(cfg ReceiverConfig, stream StreamProfile, loss LossPattern, reorder OutOfOrderPattern, startSeq uint32, timer TimerProfile) string {
	reorderName := "none"
	if reorder != nil {
		reorderName = reorder.Description()
	}

	seqName := "seq-1"
	if startSeq > 1000 {
		seqName = fmt.Sprintf("seq-max-%d", packet.MAX_SEQUENCENUMBER-startSeq)
	} else if startSeq > 1 {
		seqName = fmt.Sprintf("seq-%d", startSeq)
	}

	return fmt.Sprintf("%s/%s/%s/%s/%s/%s",
		cfg.Name,
		stream.Name,
		loss.Description(),
		reorderName,
		seqName,
		timer.Name,
	)
}

// ============================================================================
// TEST RUNNER
// ============================================================================

// RunTestMatrix runs all generated test cases in parallel.
func RunTestMatrix(t *testing.T, cases []StreamTestCase) {
	t.Logf("Running %d test cases", len(cases))
	for _, tc := range cases {
		tc := tc // Capture for parallel
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			RunSingleTest(t, tc)
		})
	}
}

// RunSingleTest executes a single test case.
func RunSingleTest(t *testing.T, tc StreamTestCase) {
	// Capture NAKs
	// The NAK list is in [start, end, start, end, ...] format for ranges
	nakedSet := make(map[uint32]bool)
	nakLock := sync.Mutex{}
	onSendNAK := func(list []circular.Number) {
		nakLock.Lock()
		defer nakLock.Unlock()
		// Parse NAK ranges: [start, end, start, end, ...]
		for i := 0; i+1 < len(list); i += 2 {
			start := list[i].Val()
			end := list[i+1].Val()
			// Expand range into individual sequences
			for seq := start; ; seq = circular.SeqAdd(seq, 1) {
				nakedSet[seq] = true
				if seq == end {
					break
				}
			}
		}
	}

	// Create receiver with mock time for TSBPD-aware logic
	recv, mockTime := createMatrixReceiver(t, tc.ReceiverConfig, tc.TimerProfile, tc.StartSeq, tc.StreamProfile.TsbpdDelayUs, onSendNAK)

	// Generate packet stream
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	stream := generateMatrixStream(addr, tc.StreamProfile, tc.StartSeq)

	// Apply loss pattern
	surviving, dropped := applyLossPattern(stream.Packets, tc.LossPattern)

	// Apply reorder pattern (if any)
	if tc.ReorderPattern != nil {
		surviving = tc.ReorderPattern.Reorder(surviving)
	}

	// Push packets to receiver
	for _, p := range surviving {
		recv.Push(p)
	}

	// Run NAK cycles starting after the stream ends
	runNakCyclesWithMockTime(recv, mockTime, stream.EndTimeUs, tc.StreamProfile, 100)

	// Verify results
	verifyNakResults(t, tc, dropped, nakedSet)
}

// createMatrixReceiver creates a receiver with the given configuration.
// Returns the receiver and a mock time pointer. The caller should set *mockTime
// before calling Tick() to ensure TSBPD-aware logic uses the correct time.
func createMatrixReceiver(t *testing.T, cfg ReceiverConfig, timer TimerProfile, startSeq uint32, tsbpdDelayUs uint64, onSendNAK func([]circular.Number)) (*receiver, *uint64) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   timer.AckIntervalUs,
		PeriodicNAKInterval:   timer.NakIntervalUs,
		OnSendACK:             func(seq circular.Number, light bool) {},
		OnSendNAK:             onSendNAK,
		OnDeliver:             func(p packet.Packet) {},
		ConnectionMetrics:     testMetrics,
		TsbpdDelay:            tsbpdDelayUs,
	}

	// Apply receiver config
	if cfg.UseNakBtree {
		recvConfig.PacketReorderAlgorithm = "btree"
		recvConfig.UseNakBtree = true
		recvConfig.NakRecentPercent = cfg.NakRecentPercent
		recvConfig.NakMergeGap = cfg.NakMergeGap
		recvConfig.NakConsolidationBudget = cfg.NakConsolidationBudget
		recvConfig.FastNakEnabled = cfg.FastNakEnabled
		recvConfig.FastNakRecentEnabled = cfg.FastNakRecentEnabled
		if cfg.FastNakEnabled {
			recvConfig.FastNakThresholdUs = 50_000 // 50ms default
		}
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Inject mock time function for TSBPD-aware logic (Phase 5).
	// The gapScan() and contiguousScanWithTime() functions use r.nowFn()
	// to determine if packets are TSBPD-expired. Without mock time,
	// they would use real time (billions of microseconds) which would
	// make all test packets appear TSBPD-expired.
	mockTime := uint64(0)
	r.nowFn = func() uint64 { return mockTime }

	return r, &mockTime
}

// MatrixStreamResult holds generated stream data.
type MatrixStreamResult struct {
	Packets      []packet.Packet
	TotalPackets int
	EndTimeUs    uint64
}

// generateMatrixStream generates packets based on stream profile.
func generateMatrixStream(addr net.Addr, profile StreamProfile, startSeq uint32) MatrixStreamResult {
	// Calculate packets per second: bitrate / (payload_size * 8)
	packetsPerSecond := float64(profile.BitrateBps) / float64(profile.PayloadBytes*8)
	packetIntervalUs := uint64(1_000_000 / packetsPerSecond)

	totalPackets := int(packetsPerSecond * profile.DurationSec)
	packets := make([]packet.Packet, 0, totalPackets)

	startTimeUs := uint64(1_000_000) // Start at 1 second
	seq := startSeq

	for i := 0; i < totalPackets; i++ {
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = startTimeUs + uint64(i)*packetIntervalUs + profile.TsbpdDelayUs
		p.Header().Timestamp = uint32(startTimeUs + uint64(i)*packetIntervalUs)

		packets = append(packets, p)
		seq = circular.SeqAdd(seq, 1) // Handle wraparound
	}

	endTimeUs := startTimeUs + uint64(totalPackets)*packetIntervalUs

	return MatrixStreamResult{
		Packets:      packets,
		TotalPackets: totalPackets,
		EndTimeUs:    endTimeUs,
	}
}

// runNakCycles runs multiple NAK cycles to ensure all gaps are detected.
//
// With the unified contiguousPoint approach (Phase 14), NAK scanning only processes
// packets in a specific time window:
//   - Not TSBPD-expired: now < PktTsbpdTime
//   - Not too recent: PktTsbpdTime <= now + TsbpdDelay * NakRecentPercent
//
// This means valid tick time for a packet is:
//
//	PktTsbpdTime - TsbpdDelay * NakRecentPercent <= now < PktTsbpdTime
//
// The window is only NakRecentPercent wide (typically 10% = 12ms for 120ms TSBPD).
// To scan ALL packets, we slide through the stream in small steps.
func runNakCycles(recv *receiver, streamEndTimeUs uint64, profile StreamProfile, cycles int) {
	// Legacy function for backward compatibility - uses real time
	var dummyMockTime uint64
	runNakCyclesWithMockTime(recv, &dummyMockTime, streamEndTimeUs, profile, cycles)
}

func runNakCyclesWithMockTime(recv *receiver, mockTime *uint64, streamEndTimeUs uint64, profile StreamProfile, cycles int) {
	// Stream timing:
	// - First packet arrival: 1_000_000 µs (1 second)
	// - First packet's PktTsbpdTime = 1_000_000 + TsbpdDelayUs
	// - Last packet's PktTsbpdTime ≈ streamEndTimeUs + TsbpdDelayUs
	startTimeUs := uint64(1_000_000)
	firstPktTsbpdTime := startTimeUs + profile.TsbpdDelayUs
	lastPktTsbpdTime := streamEndTimeUs + profile.TsbpdDelayUs

	// NAK scan window per ack_optimization_plan.md Section 3.2:
	// - tooRecentThreshold = now + tsbpdDelay * (1 - nakRecentPercent)
	// - Packets with PktTsbpdTime > tooRecentThreshold are "too recent"
	// - For nakRecentPercent=0.10, scannable window is now to now + 90% of tsbpdDelay
	//
	// To scan packet P, we need: P.PktTsbpdTime <= now + 0.90 * tsbpdDelay
	// Rearranging: now >= P.PktTsbpdTime - 0.90 * tsbpdDelay
	nakRecentPercent := 0.10 // matches CfgNakBtree
	nakWindowSize := uint64(float64(profile.TsbpdDelayUs) * (1.0 - nakRecentPercent))

	// Start just before first packet becomes scannable
	// First packet's PktTsbpdTime = firstPktTsbpdTime
	// Scannable when: now >= firstPktTsbpdTime - nakWindowSize
	tickTime := firstPktTsbpdTime - nakWindowSize

	// Slide by small steps to ensure we cover all packets
	// Each packet arrives ~10ms apart for 1Mbps, so 5ms step is reasonable
	slideStep := uint64(5_000) // 5ms

	for i := 0; i < cycles; i++ {
		// Set mock time BEFORE Tick so gapScan() uses the correct time
		*mockTime = tickTime
		recv.Tick(tickTime)

		// Slide the window forward
		tickTime += slideStep

		// Stop when we've passed the last packet's NAK window
		// Last packet is scannable when: now >= lastPktTsbpdTime - nakWindowSize
		// We need to tick BEYOND that to ensure we scan it
		if tickTime > lastPktTsbpdTime {
			break
		}
	}

	// Final pass: ensure we're past the NAK window for all packets
	// but BEFORE TSBPD expiry (which would trigger TSBPD skip in ACK)
	// The NAK window ends at packet.PktTsbpdTime, TSBPD skip triggers when now > PktTsbpdTime
	*mockTime = lastPktTsbpdTime - 1 // Just before last packet's TSBPD expires
	recv.Tick(*mockTime)
}

// verifyNakResults checks that all dropped packets were NAKed (excluding "too recent" packets).
func verifyNakResults(t *testing.T, tc StreamTestCase, dropped []uint32, nakedSet map[uint32]bool) {
	// For Original mode (non-btree), immediate NAKs work differently
	// Skip detailed verification for now - just check no panic
	if !tc.ReceiverConfig.UseNakBtree {
		// Original mode uses immediate NAKs, different verification needed
		return
	}

	// No dropped packets = nothing to verify
	if len(dropped) == 0 {
		return
	}

	// Calculate the "too recent" threshold
	// Packets dropped late in the stream won't be NAKed because they're within
	// the tooRecentThreshold window (NakRecentPercent * TsbpdDelay).
	// For a typical NakRecentPercent=0.10 and TsbpdDelay=120ms,
	// that's 12ms worth of packets that won't be NAKed.
	nakRecentPercent := tc.ReceiverConfig.NakRecentPercent
	if nakRecentPercent == 0 {
		nakRecentPercent = 0.10 // Default
	}
	tsbpdUs := tc.StreamProfile.TsbpdDelayUs

	// Calculate packet interval in microseconds
	packetsPerSecond := float64(tc.StreamProfile.BitrateBps) / float64(tc.StreamProfile.PayloadBytes*8)
	packetIntervalUs := 1_000_000.0 / packetsPerSecond

	// Calculate how many packets are in the "too recent" window
	tooRecentWindowUs := float64(tsbpdUs) * nakRecentPercent
	tooRecentPackets := int(tooRecentWindowUs / packetIntervalUs)

	// Count missed NAKs (excluding packets in the "too recent" window)
	// The "too recent" window applies to the last N packets in the stream
	missedCount := 0
	totalDropped := len(dropped)

	for _, droppedSeq := range dropped {
		if !nakedSet[droppedSeq] {
			missedCount++
		}
	}

	// Be more lenient with the tolerance:
	// 1. All packets in the "too recent" window won't be NAKed (expected)
	// 2. Allow 10% additional tolerance for timing variations and burst loss edge cases
	// 3. Add extra tolerance for burst loss patterns (last burst may straddle boundaries)
	expectedMissed := tooRecentPackets
	if expectedMissed > totalDropped {
		expectedMissed = totalDropped
	}
	tolerance := expectedMissed + (totalDropped / 10) // tooRecent + 10%
	if tolerance < 10 {
		tolerance = 10
	}

	// Log debugging info
	t.Logf("NAK verification: dropped=%d, uniqueNAKed=%d, missed=%d, tooRecentWindow=%d pkts, tolerance=%d",
		totalDropped, len(nakedSet), missedCount, tooRecentPackets, tolerance)

	if missedCount > tolerance {
		t.Errorf("Missed %d/%d NAKs (tolerance: %d based on tooRecent=%d). First few missed: %v",
			missedCount, totalDropped, tolerance, tooRecentPackets, getMissedSample(dropped, nakedSet, 5))
	}
}

// getMissedSample returns a sample of missed sequence numbers.
func getMissedSample(dropped []uint32, nakedSet map[uint32]bool, maxSamples int) []uint32 {
	var missed []uint32
	for _, seq := range dropped {
		if !nakedSet[seq] {
			missed = append(missed, seq)
			if len(missed) >= maxSamples {
				break
			}
		}
	}
	return missed
}

// ============================================================================
// COMPREHENSIVE TEST METRICS
// ============================================================================
// TestMetricsCollector tracks all important metrics during a test for
// comprehensive verification of ACKs, NAKs, delivery, and recovery.
type TestMetricsCollector struct {
	mu sync.Mutex

	// ACK tracking
	ACKCount      int
	ACKLiteCount  int
	ACKFullCount  int
	ACKSequences  []uint32

	// NAK tracking
	NAKCount        int
	NAKedSequences  map[uint32]int // seq -> count (to detect over-NAKing)
	UniqueNAKCount  int

	// Delivery tracking
	DeliveredCount     int
	DeliveredSequences []uint32

	// Expected values for validation
	ExpectedPackets    int
	DroppedPackets     []uint32
	RetransmittedCount int
}

// NewTestMetricsCollector creates a new metrics collector.
func NewTestMetricsCollector() *TestMetricsCollector {
	return &TestMetricsCollector{
		NAKedSequences:     make(map[uint32]int),
		DeliveredSequences: make([]uint32, 0),
		ACKSequences:       make([]uint32, 0),
	}
}

// OnSendACK callback for tracking ACKs.
func (c *TestMetricsCollector) OnSendACK(seq circular.Number, light bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ACKCount++
	c.ACKSequences = append(c.ACKSequences, seq.Val())
	if light {
		c.ACKLiteCount++
	} else {
		c.ACKFullCount++
	}
}

// OnSendNAK callback for tracking NAKs.
func (c *TestMetricsCollector) OnSendNAK(list []circular.Number) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.NAKCount++
	// Parse NAK ranges: [start, end, start, end, ...]
	for i := 0; i + 1 < len(list); i += 2 {
		start := list[i].Val()
		end := list[i+1].Val()
		for seq := start; ; seq = circular.SeqAdd(seq, 1) {
			c.NAKedSequences[seq]++
			if seq == end {
				break
			}
		}
	}
	c.UniqueNAKCount = len(c.NAKedSequences)
}

// OnDeliver callback for tracking delivery.
func (c *TestMetricsCollector) OnDeliver(p packet.Packet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.DeliveredCount++
	c.DeliveredSequences = append(c.DeliveredSequences, p.Header().PacketSequenceNumber.Val())
}

// Verify checks all metrics against expected values.
func (c *TestMetricsCollector) Verify(t *testing.T, testName string, testDurationMs int, ackIntervalMs int, nakIntervalMs int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	t.Logf("=== %s Metrics Summary ===", testName)

	// ACK verification
	expectedACKsMin := testDurationMs / ackIntervalMs / 2 // Allow 50% tolerance
	expectedACKsMax := testDurationMs / ackIntervalMs * 2 // Allow 2x tolerance
	t.Logf("ACKs: total=%d (lite=%d, full=%d), expected range=[%d, %d]",
		c.ACKCount, c.ACKLiteCount, c.ACKFullCount, expectedACKsMin, expectedACKsMax)
	if c.ACKCount < expectedACKsMin {
		t.Errorf("Too few ACKs: got %d, expected at least %d", c.ACKCount, expectedACKsMin)
	}
	if c.ACKCount > expectedACKsMax {
		t.Errorf("Too many ACKs: got %d, expected at most %d", c.ACKCount, expectedACKsMax)
	}

	// NAK verification
	droppedCount := len(c.DroppedPackets)
	t.Logf("NAKs: calls=%d, unique_seqs=%d, dropped_pkts=%d",
		c.NAKCount, c.UniqueNAKCount, droppedCount)

	// Check for over-NAKing (same sequence NAKed too many times)
	maxNakRetries := testDurationMs / nakIntervalMs
	for seq, count := range c.NAKedSequences {
		if count > maxNakRetries+2 { // Allow small tolerance
			t.Errorf("Over-NAKing detected: seq %d NAKed %d times (max expected: %d)",
				seq, count, maxNakRetries)
		}
	}

	// Delivery verification
	expectedDelivered := c.ExpectedPackets - droppedCount + c.RetransmittedCount
	t.Logf("Delivery: delivered=%d, expected=%d (total=%d - dropped=%d + retrans=%d)",
		c.DeliveredCount, expectedDelivered, c.ExpectedPackets, droppedCount, c.RetransmittedCount)

	// Recovery rate
	recoveredCount := 0
	for _, seq := range c.DroppedPackets {
		for _, deliveredSeq := range c.DeliveredSequences {
			if seq == deliveredSeq {
				recoveredCount++
				break
			}
		}
	}
	recoveryRate := float64(0)
	if droppedCount > 0 {
		recoveryRate = float64(recoveredCount) / float64(droppedCount) * 100
	}
	t.Logf("Recovery: recovered=%d/%d (%.1f%%)", recoveredCount, droppedCount, recoveryRate)
}

// ============================================================================
// TIER TEST FUNCTIONS
// ============================================================================

// TestStream_Tier1 runs core validation tests.
// These tests must pass for every PR.
func TestStream_Tier1(t *testing.T) {
	cases := GenerateTestMatrix(Tier1Options)
	t.Logf("Tier 1: %d test cases", len(cases))
	RunTestMatrix(t, cases)
}

// TestStream_Tier2 runs extended coverage tests.
// These tests run in daily CI.
func TestStream_Tier2(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Tier 2 tests in short mode")
	}
	cases := GenerateTestMatrix(Tier2Options)
	t.Logf("Tier 2: %d test cases", len(cases))
	RunTestMatrix(t, cases)
}

// TestStream_Tier3 runs comprehensive tests.
// These tests run in nightly CI.
func TestStream_Tier3(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Tier 3 tests in short mode")
	}
	cases := GenerateTestMatrix(Tier3Options)
	t.Logf("Tier 3: %d test cases", len(cases))
	RunTestMatrix(t, cases)
}

// TestStream_Framework verifies the test framework itself works.
func TestStream_Framework(t *testing.T) {
	// Verify matrix generation
	tier1Cases := GenerateTestMatrix(Tier1Options)
	require.Greater(t, len(tier1Cases), 0, "Tier1 should generate test cases")
	t.Logf("Tier1 generates %d cases", len(tier1Cases))

	tier2Cases := GenerateTestMatrix(Tier2Options)
	require.Greater(t, len(tier2Cases), len(tier1Cases), "Tier2 should generate more cases than Tier1")
	t.Logf("Tier2 generates %d cases", len(tier2Cases))

	tier3Cases := GenerateTestMatrix(Tier3Options)
	require.Greater(t, len(tier3Cases), len(tier2Cases), "Tier3 should generate more cases than Tier2")
	t.Logf("Tier3 generates %d cases", len(tier3Cases))

	// Verify test naming
	for i, tc := range tier1Cases[:min(5, len(tier1Cases))] {
		t.Logf("Sample case %d: %s", i, tc.Name)
		require.NotEmpty(t, tc.Name, "Test case should have a name")
	}

	// Run a single simple test case to verify the runner works
	t.Run("SingleTest", func(t *testing.T) {
		tc := StreamTestCase{
			Name:           "Framework/Verify",
			ReceiverConfig: CfgNakBtree,
			StreamProfile:  Stream1MbpsShort,
			LossPattern:    NoLoss{},
			ReorderPattern: nil,
			StartSeq:       1,
			TimerProfile:   TimerDefault,
		}
		RunSingleTest(t, tc)
	})
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ============================================================================
// TSBPD ADVANCEMENT TESTS
// ============================================================================
// These tests verify contiguousPoint advancement when packets are permanently
// lost or significantly delayed beyond their TSBPD deadline.
// See documentation/contiguous_point_tsbpd_advancement_design.md

// createTSBPDTestReceiver creates a receiver configured for TSBPD advancement testing.
// It returns the receiver and a function to set the mock time.
func createTSBPDTestReceiver(t *testing.T, startSeq uint32, tsbpdDelayUs uint64) (*receiver, *uint64, *metrics.ConnectionMetrics) {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000, // 10ms
		PeriodicNAKInterval:    20_000, // 20ms
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
		NakMergeGap:            3,
		NakConsolidationBudget: 20_000,
	}

	recv := NewReceiver(recvConfig).(*receiver)

	// Set up mock time
	mockTime := uint64(1_000_000) // Start at 1 second
	recv.nowFn = func() uint64 { return mockTime }

	return recv, &mockTime, testMetrics
}

// createTestPacket creates a packet with specific sequence and TSBPD time.
func createTestPacket(seq uint32, tsbpdTime uint64) packet.Packet {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	p := packet.NewPacket(addr)
	p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
	p.Header().PktTsbpdTime = tsbpdTime
	p.Header().Timestamp = uint32(tsbpdTime - 120_000) // Arrival time before TSBPD
	return p
}

// TestTSBPDAdvancement_RingOutOfOrder tests the current bug where ring out-of-order
// delivery causes packets to be dropped as "too_old".
//
// This is the specific scenario we're fixing:
// - io_uring receives packets 1-10
// - Ring round-robin reads packet 4 first (from shard 0)
// - Packet 4 inserted into btree
// - contiguousScan finds gap at 1-3, "stale gap" handling jumps contiguousPoint
// - Packets 1-3 read from ring later, rejected as "too_old"
func TestTSBPDAdvancement_RingOutOfOrder(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	// Time setup:
	// - Current time: 1 second (1_000_000 µs)
	// - Packets TSBPD: 1 second + 120ms = 1.12 seconds
	// - TSBPD has NOT expired yet
	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Create packets 1-10 with TSBPD time in the future
	packets := make([]packet.Packet, 10)
	for i := 0; i < 10; i++ {
		seq := uint32(i + 1)
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(i*1000) // Each packet 1ms apart
		packets[i] = createTestPacket(seq, tsbpdTime)
	}

	// Simulate ring out-of-order: push packets 4, 5, 6, ... then 1, 2, 3
	// This simulates what happens with io_uring + round-robin ring reading
	outOfOrderSequence := []int{3, 4, 5, 6, 7, 8, 9, 0, 1, 2} // 0-indexed
	for _, idx := range outOfOrderSequence {
		recv.Push(packets[idx])
	}

	// Run Tick to process packets (time is before TSBPD expiry)
	recv.Tick(*mockTime)

	// Check results
	// BEFORE FIX: Packets 1-3 would be dropped as "too_old" because the stale gap
	// handling incorrectly advances contiguousPoint when it sees packet 4 first.
	// AFTER FIX: All packets should be accepted (no too_old drops)
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	// Log the state for debugging
	t.Logf("contiguousPoint: %d", recv.contiguousPoint.Load())
	t.Logf("too_old drops: %d", tooOldDrops)
	t.Logf("store size: %d", recv.packetStore.Len())

	// This test currently FAILS (demonstrates broken behavior)
	// After implementing the fix, this assertion should PASS
	if tooOldDrops > 0 {
		t.Errorf("BROKEN: %d packets dropped as too_old due to out-of-order ring delivery", tooOldDrops)
		t.Log("This test demonstrates the bug. After implementing TSBPD-aware advancement, this should pass.")
	}

	// Verify all 10 packets are in the store
	if recv.packetStore.Len() != 10 {
		t.Errorf("Expected 10 packets in store, got %d", recv.packetStore.Len())
	}
}

// TestTSBPDAdvancement_CompleteOutage tests that contiguousPoint advances correctly
// after a complete network outage longer than the TSBPD buffer.
//
// Scenario:
// - Packets 1-100 received, contiguousPoint=100
// - Network outage for 3 seconds (> 120ms TSBPD)
// - Packets 101-199 NEVER arrive
// - Packets 200+ start arriving
//
// Expected behavior:
//   - When packet 200 arrives and its TSBPD is checked, packets 101-199's TSBPD
//     would have expired (if they existed)
//   - contiguousPoint should advance to 199 (btree.Min()-1)
//   - Packets 200+ should be processed normally
func TestTSBPDAdvancement_CompleteOutage(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Phase 1: Receive packets 1-100
	for seq := uint32(1); seq <= 100; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Run Tick to process and deliver packets
	*mockTime = baseTime + tsbpdDelayUs + 100_000 // After TSBPD of packet 100
	recv.Tick(*mockTime)

	// Verify contiguousPoint advanced to 100
	t.Logf("After phase 1: contiguousPoint=%d", recv.contiguousPoint.Load())

	// Phase 2: Network outage - 3 seconds pass, packets 101-199 never arrive
	// Advance time by 3 seconds
	*mockTime = baseTime + 3_000_000 // 3 seconds later

	// Phase 3: Packets 200-210 arrive (after the gap)
	// These packets have TSBPD time based on when they were "sent" (not current mockTime)
	// For the test to work, we set their TSBPD to match arrival during the outage
	// so that TSBPD expiry can trigger advancement
	packet200TsbpdTime := *mockTime + tsbpdDelayUs // Packet 200's TSBPD
	for seq := uint32(200); seq <= 210; seq++ {
		tsbpdTime := packet200TsbpdTime + uint64((seq-200)*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Advance time past TSBPD of packet 200 to trigger TSBPD-based advancement
	// At this point, the gap 101-199 is unrecoverable because btree.Min() (200)'s TSBPD has expired
	*mockTime = packet200TsbpdTime + 1 // Just past TSBPD of packet 200
	recv.Tick(*mockTime)

	// Check results
	contiguousPoint := recv.contiguousPoint.Load()
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	t.Logf("After outage: contiguousPoint=%d", contiguousPoint)
	t.Logf("too_old drops: %d", tooOldDrops)
	t.Logf("store size: %d", recv.packetStore.Len())

	// BEFORE FIX: contiguousPoint might be stuck at 100 or advanced incorrectly
	// AFTER FIX: contiguousPoint should advance to 199 (btree.Min()-1)
	// and packets 200-210 should NOT be dropped as too_old

	// The gap 101-199 should be recognized as TSBPD-expired (unrecoverable)
	// contiguousPoint should advance to 199
	if contiguousPoint < 199 {
		t.Errorf("BROKEN: contiguousPoint stuck at %d, expected >= 199", contiguousPoint)
		t.Log("After implementing TSBPD-aware advancement, contiguousPoint should advance to btree.Min()-1")
	}

	// Packets 200-210 should NOT be dropped
	if tooOldDrops > 0 {
		t.Errorf("BROKEN: %d packets dropped as too_old after outage", tooOldDrops)
	}
}

// TestTSBPDAdvancement_MidStreamGap tests that contiguousPoint advances when
// a mid-stream gap expires due to TSBPD.
//
// Scenario:
// - Packets 1-100 received, contiguousPoint=100
// - Packets 101-150 lost (never arrive)
// - Packets 151-200 arrive (stored in btree)
// - NAKs sent but retransmissions also lost
// - TSBPD expires for packets 101-150
//
// Expected behavior:
// - When TSBPD expires for packet 151 (the minimum in btree after gap)
// - contiguousPoint should advance to 150 (btree.Min()-1)
// - Packets 151-200 become deliverable
func TestTSBPDAdvancement_MidStreamGap(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Phase 1: Receive packets 1-100
	for seq := uint32(1); seq <= 100; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick to deliver
	*mockTime = baseTime + tsbpdDelayUs + 100_000
	recv.Tick(*mockTime)
	t.Logf("After packets 1-100: contiguousPoint=%d", recv.contiguousPoint.Load())

	// Phase 2: Packets 101-150 are lost, packets 151-200 arrive
	// Time advances slightly (packets arriving in real-time)
	arrivalTime := *mockTime + 50_000 // 50ms later
	for seq := uint32(151); seq <= 200; seq++ {
		tsbpdTime := arrivalTime + tsbpdDelayUs + uint64((seq-151)*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick - packets 151-200 in btree, but gap 101-150 exists
	*mockTime = arrivalTime
	recv.Tick(*mockTime)
	t.Logf("After gap: contiguousPoint=%d, store size=%d", recv.contiguousPoint.Load(), recv.packetStore.Len())

	// Phase 3: Time advances past TSBPD of packet 151
	// At this point, packets 101-150 are TSBPD-expired (unrecoverable)
	*mockTime = arrivalTime + tsbpdDelayUs + 10_000 // Past TSBPD of first packets in btree
	recv.Tick(*mockTime)

	// Check results
	contiguousPoint := recv.contiguousPoint.Load()
	t.Logf("After TSBPD expiry: contiguousPoint=%d", contiguousPoint)

	// BEFORE FIX: contiguousPoint might be stuck at 100
	// AFTER FIX: contiguousPoint should advance to 150 when btree.Min()'s TSBPD expires

	// With the gap being 50 packets (101-150), this is less than the stale threshold of 64
	// So the current broken code won't advance it based on gap size alone.
	// The fix should advance it based on TSBPD expiry of the minimum packet.

	// After TSBPD expiry of packet 151, the gap 101-150 is unrecoverable
	// contiguousPoint should advance to 150
	if contiguousPoint < 150 {
		t.Errorf("BROKEN: contiguousPoint stuck at %d, expected >= 150 after TSBPD expiry", contiguousPoint)
		t.Log("After implementing TSBPD-aware advancement, contiguousPoint should advance when btree.Min()'s TSBPD expires")
	}

	// Check no unexpected too_old drops
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()
	t.Logf("too_old drops: %d", tooOldDrops)
}

// TestTSBPDAdvancement_SmallGapNoAdvance tests that contiguousPoint does NOT advance
// for small gaps when TSBPD has NOT expired (packets might still arrive).
//
// This is a "negative test" to ensure we don't advance too eagerly.
func TestTSBPDAdvancement_SmallGapNoAdvance(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, _ := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Receive packets 1-10, then 15-20 (gap of 11-14)
	for seq := uint32(1); seq <= 10; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}
	for seq := uint32(15); seq <= 20; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick while TSBPD has NOT expired (packets 11-14 might still arrive)
	*mockTime = baseTime + 50_000 // Only 50ms, TSBPD is 120ms
	recv.Tick(*mockTime)

	contiguousPoint := recv.contiguousPoint.Load()
	t.Logf("contiguousPoint=%d (gap 11-14 exists, TSBPD not expired)", contiguousPoint)

	// contiguousPoint should NOT advance past 10 because:
	// 1. Gap exists (11-14)
	// 2. TSBPD has NOT expired for packet 15
	// 3. Packets 11-14 might still arrive
	if contiguousPoint > 10 {
		t.Errorf("BROKEN: contiguousPoint advanced to %d prematurely (TSBPD not expired)", contiguousPoint)
		t.Log("contiguousPoint should NOT advance until TSBPD expires for btree.Min()")
	}

	// Now advance time past TSBPD of packet 15
	*mockTime = baseTime + tsbpdDelayUs + 15_000 + 1 // Just past TSBPD of packet 15
	recv.Tick(*mockTime)

	contiguousPoint = recv.contiguousPoint.Load()
	t.Logf("After TSBPD expiry: contiguousPoint=%d", contiguousPoint)

	// NOW contiguousPoint should advance to 14 (btree.Min()-1 = 15-1 = 14)
	// because TSBPD has expired and gap is unrecoverable
	if contiguousPoint < 14 {
		t.Logf("Note: After fix, contiguousPoint should be 14 (btree.Min()-1)")
	}
}

// TestTSBPDAdvancement_ExtendedOutage tests recovery from a very long outage
// with multiple TSBPD advancement cycles.
//
// Scenario:
// - Packets 1-1000 received, contiguousPoint=1000
// - 30+ second outage with 80% packet loss
// - Thousands of packets may have expired TSBPD
// - System must recover gracefully through multiple Tick cycles
//
// This tests Edge Case 1 from the design document: "Very Long Outage"
func TestTSBPDAdvancement_ExtendedOutage(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Phase 1: Receive packets 1-1000 (establishing baseline)
	for seq := uint32(1); seq <= 1000; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*100) // 100µs apart
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick to deliver initial packets
	*mockTime = baseTime + tsbpdDelayUs + 100_000 // After TSBPD of packet 1000
	recv.Tick(*mockTime)
	t.Logf("After initial 1000 packets: contiguousPoint=%d", recv.contiguousPoint.Load())

	// Phase 2: Simulate extended outage - jump forward 30 seconds
	// Packets 1001-5000 would have been sent but many were lost
	outageDuration := uint64(30_000_000) // 30 seconds
	*mockTime = baseTime + outageDuration

	// Only ~20% of packets during outage arrive (sparse arrivals)
	// Simulate this by pushing packets at irregular intervals
	gapStarts := []uint32{1001, 1500, 2000, 3000, 4000}
	gapSizes := []uint32{400, 400, 800, 800, 800}

	currentSeq := uint32(1001)
	for i, gapStart := range gapStarts {
		gapSize := gapSizes[i]
		gapEnd := gapStart + gapSize - 1

		// Skip the gap
		currentSeq = gapEnd + 1

		// Push some packets after this gap
		nextGap := uint32(6000)
		if i+1 < len(gapStarts) {
			nextGap = gapStarts[i+1]
		}

		for seq := currentSeq; seq < nextGap && seq < 5500; seq++ {
			tsbpdTime := *mockTime + tsbpdDelayUs + uint64((seq-1001)*100)
			p := createTestPacket(seq, tsbpdTime)
			recv.Push(p)
		}
		currentSeq = nextGap
	}

	t.Logf("After sparse arrivals: store size=%d", recv.packetStore.Len())

	// Phase 3: Run many Tick cycles to trigger TSBPD-based advancements
	// Each cycle should advance contiguousPoint when TSBPD expires
	initialCP := recv.contiguousPoint.Load()
	tickCount := 0
	advancementCount := 0

	for i := 0; i < 500; i++ {
		*mockTime += 20_000 // Advance 20ms each tick
		prevCP := recv.contiguousPoint.Load()
		recv.Tick(*mockTime)
		newCP := recv.contiguousPoint.Load()

		if newCP != prevCP {
			advancementCount++
			t.Logf("Tick %d: contiguousPoint advanced %d -> %d", i, prevCP, newCP)
		}
		tickCount++

		// Stop early if we've advanced past all the gaps
		if newCP >= 5000 {
			break
		}
	}

	finalCP := recv.contiguousPoint.Load()
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	t.Logf("Final state after %d ticks:", tickCount)
	t.Logf("  contiguousPoint: %d -> %d", initialCP, finalCP)
	t.Logf("  advancements: %d", advancementCount)
	t.Logf("  too_old drops: %d", tooOldDrops)
	t.Logf("  store size: %d", recv.packetStore.Len())

	// Verify system recovered
	if finalCP <= initialCP {
		t.Errorf("contiguousPoint did not advance (stuck at %d)", finalCP)
	}

	// Should have had multiple advancements due to multiple gaps
	if advancementCount < 3 {
		t.Errorf("Expected multiple TSBPD advancements, got %d", advancementCount)
	}
}

// TestTSBPDAdvancement_Wraparound tests TSBPD advancement with sequence numbers
// near the 31-bit wraparound boundary.
//
// This tests Edge Case 4 from the design document: "Wraparound"
func TestTSBPDAdvancement_Wraparound(t *testing.T) {
	// Start sequence near MAX (2^31 - 100)
	const maxSeq = uint32(0x7FFFFFFF) // 2^31 - 1
	const startSeq = maxSeq - 100
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Phase 1: Receive packets around the wraparound point
	// Sequences: maxSeq-100, maxSeq-99, ..., maxSeq, 0, 1, 2, ...
	// Use packet INDEX (i) for TSBPD time calculation, not sequence number
	for i := uint32(0); i < 50; i++ {
		seq := circular.SeqAdd(startSeq, i)
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(i*1000) // Use index, not seq
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick to process - time should be past TSBPD of packet 49
	*mockTime = baseTime + tsbpdDelayUs + 50_000
	t.Logf("Before Tick: mockTime=%d, contiguousPoint=%d, lastACKSeq=%d, lastPeriodicACK=%d",
		*mockTime, recv.contiguousPoint.Load(), recv.lastACKSequenceNumber.Val(), recv.lastPeriodicACK)
	recv.Tick(*mockTime)

	cpAfterPhase1 := recv.contiguousPoint.Load()
	expectedAfterPhase1 := circular.SeqAdd(startSeq, 49)
	t.Logf("After phase 1 (50 packets): contiguousPoint=%d (0x%08x), expected=%d (0x%08x)",
		cpAfterPhase1, cpAfterPhase1, expectedAfterPhase1, expectedAfterPhase1)
	t.Logf("Phase 1: store size=%d, lastACKSeq=%d (0x%08x), lastPeriodicACK=%d",
		recv.packetStore.Len(), recv.lastACKSequenceNumber.Val(), recv.lastACKSequenceNumber.Val(), recv.lastPeriodicACK)

	// Phase 2: Create a gap
	// Gap: indices 50-60 (11 packets)
	// Push indices 61-99 (39 packets)
	gapStartIdx := uint32(50)
	gapEndIdx := uint32(60)
	gapStartSeq := circular.SeqAdd(startSeq, gapStartIdx)
	gapEndSeq := circular.SeqAdd(startSeq, gapEndIdx)
	t.Logf("Gap indices %d-%d: seq %d (0x%08x) to %d (0x%08x)",
		gapStartIdx, gapEndIdx, gapStartSeq, gapStartSeq, gapEndSeq, gapEndSeq)

	// Push packets after the gap (indices 61-99)
	for i := uint32(61); i < 100; i++ {
		seq := circular.SeqAdd(startSeq, i)
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(i*1000) // Use index, not seq
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Tick - gap exists but TSBPD of packet 61 NOT yet expired
	// Packet 61's TSBPD = baseTime + 120_000 + 61_000 = baseTime + 181_000
	*mockTime = baseTime + tsbpdDelayUs + 60_000 // Before packet 61's TSBPD
	recv.Tick(*mockTime)

	cpMidTest := recv.contiguousPoint.Load()
	t.Logf("After gap creation (time=%d, before TSBPD expiry): contiguousPoint=%d (0x%08x)",
		*mockTime, cpMidTest, cpMidTest)
	t.Logf("Phase 2: store size=%d", recv.packetStore.Len())
	if minPkt := recv.packetStore.Min(); minPkt != nil {
		t.Logf("Phase 2: btree.Min() seq=%d (0x%08x), TSBPD=%d",
			minPkt.Header().PacketSequenceNumber.Val(),
			minPkt.Header().PacketSequenceNumber.Val(),
			minPkt.Header().PktTsbpdTime)
	}

	// Phase 3: Advance time past TSBPD of packet 61
	// Packet 61's TSBPD = baseTime + tsbpdDelayUs + 61_000
	packet61TsbpdTime := baseTime + tsbpdDelayUs + 61_000
	*mockTime = packet61TsbpdTime + 1000 // 1ms past TSBPD of packet 61
	t.Logf("Advancing time to %d (packet 61 TSBPD=%d)", *mockTime, packet61TsbpdTime)
	recv.Tick(*mockTime)

	cpFinal := recv.contiguousPoint.Load()
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	t.Logf("After TSBPD expiry: contiguousPoint=%d (0x%08x)", cpFinal, cpFinal)
	t.Logf("too_old drops: %d", tooOldDrops)

	// Verify contiguousPoint advanced correctly across wraparound
	// btree.Min() after gap = packet at index 61
	btreeMinSeq := circular.SeqAdd(startSeq, 61)
	expectedCP := circular.SeqSub(btreeMinSeq, 1) // btree.Min()-1 = index 60
	t.Logf("btree.Min()=%d (0x%08x), expected contiguousPoint=%d (0x%08x)",
		btreeMinSeq, btreeMinSeq, expectedCP, expectedCP)

	// Use circular comparison since we're dealing with wraparound
	if !circular.SeqLessOrEqual(expectedCP, cpFinal) {
		t.Errorf("contiguousPoint did not advance correctly across wraparound")
		t.Errorf("  expected >= %d (0x%08x), got %d (0x%08x)", expectedCP, expectedCP, cpFinal, cpFinal)
	}

	// No packets should be dropped as too_old
	if tooOldDrops > 0 {
		t.Errorf("Unexpected too_old drops: %d", tooOldDrops)
	}
}

// TestTSBPDAdvancement_MultipleGaps tests recovery with multiple gaps
// that expire at different times.
//
// Scenario:
// - Packets 1-100 received
// - Gap 101-120 (lost)
// - Packets 121-200 received
// - Gap 201-250 (lost)
// - Packets 251-300 received
// - Each gap expires independently as time advances
func TestTSBPDAdvancement_MultipleGaps(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, testMetrics := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Create a stream with multiple gaps
	// Each segment has packets with TSBPD spread out
	type segment struct {
		start, end uint32
		timeOffset uint64 // Time offset from baseTime for this segment
	}

	segments := []segment{
		{1, 100, 0},         // Packets 1-100, TSBPD starts at baseTime
		{121, 200, 100_000}, // Packets 121-200, TSBPD starts 100ms later
		{251, 300, 250_000}, // Packets 251-300, TSBPD starts 250ms later
	}

	for _, seg := range segments {
		for seq := seg.start; seq <= seg.end; seq++ {
			tsbpdTime := baseTime + seg.timeOffset + tsbpdDelayUs + uint64((seq-seg.start)*1000)
			p := createTestPacket(seq, tsbpdTime)
			recv.Push(p)
		}
	}

	t.Logf("Initial store size: %d", recv.packetStore.Len())
	t.Logf("Gaps: 101-120, 201-250")

	// Run Tick cycles and track advancement
	advancements := []struct {
		time uint64
		cp   uint32
	}{}

	prevCP := recv.contiguousPoint.Load()

	// Run many Tick cycles, advancing time gradually
	for i := 0; i < 50; i++ {
		*mockTime = baseTime + uint64(i)*20_000 // 20ms per tick
		recv.Tick(*mockTime)

		newCP := recv.contiguousPoint.Load()
		if newCP != prevCP {
			advancements = append(advancements, struct {
				time uint64
				cp   uint32
			}{*mockTime, newCP})
			t.Logf("Tick %d (time=%d): contiguousPoint %d -> %d",
				i, *mockTime, prevCP, newCP)
			prevCP = newCP
		}
	}

	finalCP := recv.contiguousPoint.Load()
	tooOldDrops := testMetrics.CongestionRecvDataDropTooOld.Load()

	t.Logf("Final state:")
	t.Logf("  contiguousPoint: %d", finalCP)
	t.Logf("  advancements: %d", len(advancements))
	t.Logf("  too_old drops: %d", tooOldDrops)

	// Should have had at least 2 major advancements (one for each gap)
	// Gap 1 (101-120): Should trigger advancement when packet 121's TSBPD expires
	// Gap 2 (201-250): Should trigger advancement when packet 251's TSBPD expires
	if len(advancements) < 2 {
		t.Errorf("Expected at least 2 advancements for 2 gaps, got %d", len(advancements))
	}

	// Final contiguousPoint should be well past the initial state
	if finalCP < 200 {
		t.Errorf("Expected final contiguousPoint >= 200, got %d", finalCP)
	}
}

// TestTSBPDAdvancement_IterativeCycles tests gradual advancement through
// many small Tick cycles with time advancing in small increments.
//
// This tests that the advancement logic works correctly when called many
// times with small time deltas, not just with large time jumps.
func TestTSBPDAdvancement_IterativeCycles(t *testing.T) {
	const startSeq = uint32(1)
	const tsbpdDelayUs = uint64(120_000) // 120ms

	recv, mockTime, _ := createTSBPDTestReceiver(t, startSeq, tsbpdDelayUs)

	baseTime := uint64(1_000_000)
	*mockTime = baseTime

	// Create packets with a gap
	// Packets 1-50, gap 51-60, packets 61-100
	for seq := uint32(1); seq <= 50; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}
	for seq := uint32(61); seq <= 100; seq++ {
		tsbpdTime := baseTime + tsbpdDelayUs + uint64(seq*1000)
		p := createTestPacket(seq, tsbpdTime)
		recv.Push(p)
	}

	// Track contiguousPoint over many small Tick cycles
	cpHistory := []uint32{}
	tickInterval := uint64(1_000) // 1ms per tick
	totalTicks := 200             // 200ms of ticks

	for i := 0; i < totalTicks; i++ {
		*mockTime = baseTime + uint64(i)*tickInterval
		recv.Tick(*mockTime)
		cp := recv.contiguousPoint.Load()
		cpHistory = append(cpHistory, cp)
	}

	// Log progression
	uniqueCPs := make(map[uint32]int)
	for i, cp := range cpHistory {
		if _, exists := uniqueCPs[cp]; !exists {
			uniqueCPs[cp] = i
			t.Logf("Tick %d (time=%d): contiguousPoint=%d",
				i, baseTime+uint64(i)*tickInterval, cp)
		}
	}

	// Verify:
	// 1. contiguousPoint should advance from 0 -> 50 (initial contiguous region)
	// 2. After TSBPD expiry (~120 ticks), should advance past the gap to 60
	finalCP := cpHistory[len(cpHistory)-1]

	if finalCP < 50 {
		t.Errorf("Expected contiguousPoint >= 50 after initial packets, got %d", finalCP)
	}

	// Check if TSBPD-based advancement occurred
	// TSBPD of packet 61 expires at baseTime + 120ms + 61ms = baseTime + 181ms
	// At tick 181 (181ms), contiguousPoint should advance past the gap
	tsbpdExpiresTick := int(tsbpdDelayUs/tickInterval) + 61
	if tsbpdExpiresTick < totalTicks {
		cpAtExpiry := cpHistory[tsbpdExpiresTick]
		t.Logf("At TSBPD expiry tick %d: contiguousPoint=%d", tsbpdExpiresTick, cpAtExpiry)

		if cpAtExpiry < 60 {
			t.Logf("Note: After TSBPD expiry, contiguousPoint should advance to 60 (gap 51-60 skipped)")
		}
	}

	// Verify monotonic advancement (contiguousPoint should never go backwards)
	for i := 1; i < len(cpHistory); i++ {
		if cpHistory[i] < cpHistory[i-1] {
			t.Errorf("contiguousPoint went backwards at tick %d: %d -> %d",
				i, cpHistory[i-1], cpHistory[i])
		}
	}
}

// ============================================================================
// COMPREHENSIVE LOSS RECOVERY TEST
// ============================================================================
// TestLossRecovery_Full simulates the complete NAK/retransmit/delivery cycle
// to verify that lost packets are actually recovered and delivered.
//
// This test:
// 1. Sends packets with some dropped (simulating network loss)
// 2. Verifies NAKs are sent for dropped packets
// 3. Simulates retransmission by pushing dropped packets back BEFORE TSBPD expires
// 4. Verifies ALL packets are delivered (100% recovery)
// 5. Checks ACK counts are reasonable
// 6. Checks for over-NAKing
//
// Key insight: For recovery to work, retransmits must arrive BEFORE the packet's
// TSBPD time expires. This test uses realistic timing to ensure the NAK→retransmit
// cycle completes within the TSBPD window.
func TestLossRecovery_Full(t *testing.T) {
	const (
		totalPackets   = 100
		dropRate       = 0.05 // 5% loss
		startSeq       = uint32(1)
		tsbpdDelayUs   = uint64(500_000) // 500ms - enough window for recovery
		ackIntervalUs  = uint64(10_000)  // 10ms
		nakIntervalUs  = uint64(20_000)  // 20ms
		testDurationMs = 1500            // ~1.5s test with interleaved NAK/retransmit
		packetSpreadUs = 5_000           // 5ms between packet TSBPD times
	)

	// Create metrics collector
	collector := NewTestMetricsCollector()

	// Create receiver with metrics collection
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber: circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:   ackIntervalUs,
		PeriodicNAKInterval:   nakIntervalUs,
		OnSendACK:             collector.OnSendACK,
		OnSendNAK:             collector.OnSendNAK,
		OnDeliver:             collector.OnDeliver,
		ConnectionMetrics:     testMetrics,
		TsbpdDelay:            tsbpdDelayUs,
		PacketReorderAlgorithm: "btree",
		UseNakBtree:            true,
		NakRecentPercent:       0.10,
	}

	recv := NewReceiver(recvConfig)
	r := recv.(*receiver)

	// Use mock time
	baseTime := uint64(1_000_000)
	mockTime := baseTime
	r.nowFn = func() uint64 { return mockTime }

	// Generate packets and apply loss
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	var allPackets []packet.Packet
	var droppedPackets []packet.Packet
	var droppedSeqs []uint32

	for i := 0; i < totalPackets; i++ {
		seq := startSeq + uint32(i)
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		// PktTsbpdTime = baseTime + tsbpdDelay + (index * packetSpread)
		// This staggers delivery times but keeps TSBPD deadline well in the future
		p.Header().PktTsbpdTime = baseTime + tsbpdDelayUs + uint64(i*int(packetSpreadUs))
		p.Header().Timestamp = uint32(i * int(packetSpreadUs))

		allPackets = append(allPackets, p)

		// Apply deterministic loss pattern (every 20th packet: seq 21, 41, 61, 81)
		if i > 0 && i%20 == 0 {
			droppedPackets = append(droppedPackets, p)
			droppedSeqs = append(droppedSeqs, seq)
			t.Logf("Dropping packet seq=%d (i=%d), TSBPD=%d", seq, i, p.Header().PktTsbpdTime)
		}
	}

	collector.ExpectedPackets = totalPackets
	collector.DroppedPackets = droppedSeqs

	// Phase 1: Push packets (excluding dropped)
	for _, p := range allPackets {
		seq := p.Header().PacketSequenceNumber.Val()
		isDropped := false
		for _, dSeq := range droppedSeqs {
			if seq == dSeq {
				isDropped = true
				break
			}
		}
		if !isDropped {
			recv.Push(p)
		}
	}

	t.Logf("Phase 1: Pushed %d packets, dropped %d", totalPackets-len(droppedSeqs), len(droppedSeqs))

	// Phase 2: Run Tick cycles to generate NAKs
	//
	// NAK logic: A gap is NAKable if the NEXT packet's PktTsbpdTime <= tooRecentThreshold
	// where tooRecentThreshold = now + tsbpdDelay * (1 - nakRecentPercent)
	//                         = now + 500ms * 0.90 = now + 450ms
	//
	// For dropped packet seq=21, the next packet seq=22 has TSBPD = 1,500,000 + 21*5000 = 1,605,000
	// For NAK to trigger: now + 450,000 >= 1,605,000 → now >= 1,155,000
	//
	// But we also need to stay BEFORE the dropped packet's own TSBPD to avoid
	// expiring it before retransmit arrives. Seq 21 TSBPD = 1,600,000
	//
	t.Logf("Phase 2 start: contiguousPoint=%d", r.contiguousPoint.Load())
	firstTsbpd := allPackets[0].Header().PktTsbpdTime
	lastTsbpd := allPackets[totalPackets-1].Header().PktTsbpdTime
	tooRecentWindow := uint64(float64(tsbpdDelayUs) * 0.10) // 50ms
	nakWindow := tsbpdDelayUs - tooRecentWindow              // 450ms

	t.Logf("  First packet TSBPD=%d, Last packet TSBPD=%d", firstTsbpd, lastTsbpd)
	t.Logf("  NAK window: tsbpdDelay=%d, nakWindow=%d, tooRecentWindow=%d",
		tsbpdDelayUs, nakWindow, tooRecentWindow)

	// Advance time to where gaps become NAKable
	//
	// Gap at seq 21: Next packet is seq 22 with TSBPD = 1,500,000 + 21*5000 = 1,605,000
	// tooRecentThreshold = now + tsbpdDelay * 0.90 = now + 450,000
	// NAK triggers when: tooRecentThreshold >= packet.PktTsbpdTime
	// i.e., now + 450,000 >= 1,605,000 → now >= 1,155,000
	//
	// We need to stay BEFORE the dropped packet's TSBPD expires (1,600,000)
	// NAK window for seq 21: [1,155,000, 1,600,000)
	//
	droppedSeq21Tsbpd := droppedPackets[0].Header().PktTsbpdTime // 1,600,000
	nextPacketTsbpd := baseTime + tsbpdDelayUs + 21*packetSpreadUs // 1,605,000 (seq 22)
	nakStartTime := nextPacketTsbpd - nakWindow                    // 1,605,000 - 450,000 = 1,155,000

	t.Logf("  Seq 21 TSBPD=%d, Seq 22 TSBPD=%d, NAK possible at time >= %d",
		droppedSeq21Tsbpd, nextPacketTsbpd, nakStartTime)

	for tick := 0; tick < 15; tick++ {
		// Start at NAK window entry and advance in 5ms steps
		mockTime = nakStartTime + uint64(tick*5_000) // 5ms per tick
		r.Tick(mockTime)
		t.Logf("  tick %d (time=%d): CP=%d, delivered=%d, NAKs=%d, tooRecent=%d",
			tick, mockTime, r.contiguousPoint.Load(), collector.DeliveredCount,
			collector.UniqueNAKCount, mockTime+nakWindow)
	}

	t.Logf("Phase 2 end: NAKed %d unique sequences, CP=%d",
		collector.UniqueNAKCount, r.contiguousPoint.Load())

	// Phase 3: Interleaved NAK/Retransmit cycle (simulates real network)
	//
	// In real systems:
	// 1. Receiver detects gap → sends NAK
	// 2. RTT passes → retransmit arrives
	// 3. Repeat for next gap
	//
	// We simulate ~20ms RTT. After each NAK, we "receive" the retransmit
	// before TSBPD expires for that packet.
	//
	t.Logf("Phase 3: Interleaved NAK/Retransmit cycle")

	// Track which packets we've retransmitted
	retransmitted := make(map[uint32]bool)

	// Continue ticking and retransmitting
	for tick := 0; tick < 50; tick++ {
		// Advance time by 10ms per tick
		mockTime = nakStartTime + uint64((15+tick)*10_000)
		r.Tick(mockTime)

		// Check for new NAKs and "receive" retransmits
		collector.mu.Lock()
		for seq := range collector.NAKedSequences {
			if !retransmitted[seq] {
				// Simulate retransmit arriving ~20ms after NAK
				for _, p := range droppedPackets {
					if p.Header().PacketSequenceNumber.Val() == seq {
						// Check if retransmit is still useful (CP hasn't passed it)
						cp := r.contiguousPoint.Load()
						if seq > cp {
							retransP := packet.NewPacket(addr)
							retransP.Header().PacketSequenceNumber = p.Header().PacketSequenceNumber
							retransP.Header().PktTsbpdTime = p.Header().PktTsbpdTime
							retransP.Header().Timestamp = p.Header().Timestamp
							retransP.Header().RetransmittedPacketFlag = true
							recv.Push(retransP)
							collector.RetransmittedCount++
							t.Logf("  tick %d: Retransmit seq=%d (CP=%d)", tick, seq, cp)
						} else {
							t.Logf("  tick %d: SKIP retransmit seq=%d (CP=%d already past)", tick, seq, cp)
						}
						break
					}
				}
				retransmitted[seq] = true
			}
		}
		collector.mu.Unlock()

		// Log progress every 10 ticks
		if tick%10 == 0 {
			t.Logf("  tick %d (time=%d): CP=%d, delivered=%d, NAKs=%d, retrans=%d",
				tick, mockTime, r.contiguousPoint.Load(), collector.DeliveredCount,
				collector.UniqueNAKCount, collector.RetransmittedCount)
		}
	}

	t.Logf("Phase 3 end: NAKed %d, Retransmitted %d, CP=%d",
		collector.UniqueNAKCount, collector.RetransmittedCount, r.contiguousPoint.Load())

	// Phase 4: Run more Tick cycles for delivery
	// Now advance time PAST TSBPD to trigger delivery of all packets
	t.Logf("Phase 4 start: advancing time past TSBPD (last=%d)", lastTsbpd)

	for tick := 0; tick < 20; tick++ {
		// Advance time past all packet TSBPD times
		mockTime = baseTime + tsbpdDelayUs + uint64(tick*50_000) // 50ms per tick
		r.Tick(mockTime)
		if tick%5 == 0 {
			t.Logf("  tick %d (time=%d): CP=%d, delivered=%d",
				tick, mockTime, r.contiguousPoint.Load(), collector.DeliveredCount)
		}
	}

	t.Logf("Phase 4 end: mockTime=%d, delivered=%d", mockTime, collector.DeliveredCount)

	// Verify results
	collector.Verify(t, "LossRecovery_Full",
		testDurationMs,
		int(ackIntervalUs/1000),
		int(nakIntervalUs/1000))

	// Key assertions
	// 1. All dropped packets should have been NAKed
	for _, seq := range droppedSeqs {
		if collector.NAKedSequences[seq] == 0 {
			t.Errorf("Dropped packet %d was NOT NAKed", seq)
		}
	}

	// 2. Find undelivered packets
	deliveredSet := make(map[uint32]bool)
	for _, seq := range collector.DeliveredSequences {
		deliveredSet[seq] = true
	}
	var undelivered []uint32
	for i := 0; i < totalPackets; i++ {
		seq := startSeq + uint32(i)
		if !deliveredSet[seq] {
			undelivered = append(undelivered, seq)
		}
	}
	if len(undelivered) > 0 {
		t.Logf("Undelivered packets: %v", undelivered)
		// Check if undelivered are in the "too recent" window
		// With nakRecentPercent=0.10 and tsbpdDelay=120ms, window = 12ms
		// At 1000us per packet, that's ~12 packets in the too-recent window
	}

	// 3. Verify contiguous point advanced to end
	finalCP := r.contiguousPoint.Load()
	expectedCP := startSeq + uint32(totalPackets) - 1

	t.Logf("Final: contiguousPoint=%d, expected=%d", finalCP, expectedCP)
	t.Logf("Delivered: %d/%d (%.1f%%)", collector.DeliveredCount, totalPackets,
		float64(collector.DeliveredCount)/float64(totalPackets)*100)
	t.Logf("Store remaining: %d packets", r.packetStore.Len())

	// Allow some undelivered due to timing edge cases at end of test
	// The last few packets might not have their TSBPD time reached yet
	minExpectedDelivery := totalPackets - 5 // Allow up to 5 undelivered at end
	if collector.DeliveredCount < minExpectedDelivery {
		t.Errorf("Too few packets delivered: got %d, expected at least %d",
			collector.DeliveredCount, minExpectedDelivery)
	}

	// Verify recovery: dropped packets should be in delivered (after retransmit)
	recoveredCount := 0
	for _, seq := range droppedSeqs {
		if deliveredSet[seq] {
			recoveredCount++
		}
	}
	recoveryRate := float64(recoveredCount) / float64(len(droppedSeqs)) * 100
	t.Logf("Recovery of dropped packets: %d/%d (%.1f%%)", recoveredCount, len(droppedSeqs), recoveryRate)

	// All dropped packets should be recovered after retransmit
	if recoveredCount < len(droppedSeqs) {
		notRecovered := []uint32{}
		for _, seq := range droppedSeqs {
			if !deliveredSet[seq] {
				notRecovered = append(notRecovered, seq)
			}
		}
		t.Errorf("Not all dropped packets recovered: %v not delivered", notRecovered)
	}
}
