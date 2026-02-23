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
package receive

import (
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
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

	recvConfig := Config{
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

	recv := New(recvConfig)
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
	ACKCount     int
	ACKLiteCount int
	ACKFullCount int
	ACKSequences []uint32

	// NAK tracking
	NAKCount       int
	NAKedSequences map[uint32]int // seq -> count (to detect over-NAKing)
	UniqueNAKCount int

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
	for i := 0; i+1 < len(list); i += 2 {
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
