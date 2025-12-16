package main

import (
	"fmt"
	"strings"
	"time"
)

// ============================================================================
// TEST MATRIX GENERATOR
// ============================================================================
//
// This file implements programmatic generation of test configurations from a
// parameter matrix. Instead of manually defining hundreds of test configs,
// we define parameter ranges and selection strategies.
//
// See documentation/integration_testing_matrix_design.md for full design.
// ============================================================================

// TestTier indicates when a test should run
type TestTier int

const (
	// TierCore runs on every PR/commit (~27 tests)
	TierCore TestTier = 1

	// TierDaily runs daily CI (~40 additional tests)
	TierDaily TestTier = 2

	// TierNightly runs nightly/release (~80 additional tests)
	TierNightly TestTier = 3
)

// TestMatrixParams represents a single test's parameters for name generation
// and configuration building.
type TestMatrixParams struct {
	Mode     TestMode      // Net, Parallel, Isolation
	Pattern  string        // "Starlink", "Clean", "Loss"
	Bitrate  int64         // Bits per second (e.g., 20_000_000)
	Buffer   time.Duration // SRT latency buffer
	RTT      RTTProfile    // Network round-trip time
	Loss     float64       // Background loss percentage (0.0 to 1.0)
	Timer    TimerProfile  // Timer interval profile (optional)
	Baseline ConfigVariant // For parallel tests: what to compare against
	HighPerf ConfigVariant // For parallel tests: the test configuration
}

// ParallelMatrixConfig defines the parameters for parallel test generation.
type ParallelMatrixConfig struct {
	// Parameter ranges
	Bitrates []int64
	Buffers  []time.Duration
	RTTs     []RTTProfile
	Losses   []float64
	Configs  []ConfigVariant
	Timers   []TimerProfile

	// Baseline configuration
	Baseline ConfigVariant

	// Test duration
	DefaultDuration time.Duration
}

// DefaultParallelMatrixConfig returns the default configuration for parallel test generation.
func DefaultParallelMatrixConfig() ParallelMatrixConfig {
	return ParallelMatrixConfig{
		Bitrates:        []int64{20_000_000, 50_000_000},
		Buffers:         []time.Duration{1 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second},
		RTTs:            []RTTProfile{RTT0, RTT10, RTT60, RTT130, RTT300},
		Losses:          []float64{0, 0.02, 0.05, 0.10, 0.15},
		Configs:         []ConfigVariant{ConfigNakBtree, ConfigNakBtreeF, ConfigNakBtreeFr, ConfigFull},
		Timers:          []TimerProfile{TimerDefault, TimerFast, TimerSlow, TimerFastNak, TimerSlowNak},
		Baseline:        ConfigBase,
		DefaultDuration: 90 * time.Second,
	}
}

// GeneratedParallelTest is the output from the matrix generator.
type GeneratedParallelTest struct {
	Name        string           // Generated name following convention
	Description string           // Human-readable description
	Tier        TestTier         // Which tier this test belongs to
	Params      TestMatrixParams // Original parameters
	Duration    time.Duration    // Test duration

	// Parallel test configuration (ready to use)
	Config ParallelTestConfig
}

// GenerateTestName creates a standardized test name from parameters.
// Format: {Mode}-{Pattern}[-{Loss}]-{Bitrate}-{Buffer}-{RTT}[-{Timer}]-{Config}
//
// Examples:
//   - Parallel-Starlink-20M-5s-R60-Base-vs-Full
//   - Parallel-Starlink-L5-50M-10s-R130-Base-vs-NakBtreeF
//   - Isolation-Clean-20M-5s-R0-Base-vs-NakBtree
//   - Net-Starlink-L10-20M-5s-R60-Full
func GenerateTestName(p TestMatrixParams) string {
	var parts []string

	// Mode prefix
	switch p.Mode {
	case TestModeNetwork:
		parts = append(parts, "Net")
	case TestModeParallel:
		parts = append(parts, "Parallel")
	case TestModeIsolation:
		parts = append(parts, "Isolation")
	case TestModeClean:
		parts = append(parts, "Int")
	default:
		parts = append(parts, "Test")
	}

	// Pattern with optional loss
	if p.Loss > 0 {
		parts = append(parts, fmt.Sprintf("%s-L%d", p.Pattern, int(p.Loss*100)))
	} else {
		parts = append(parts, p.Pattern)
	}

	// Bitrate in Megabits
	parts = append(parts, fmt.Sprintf("%dM", p.Bitrate/1_000_000))

	// Buffer duration
	parts = append(parts, fmt.Sprintf("%ds", int(p.Buffer.Seconds())))

	// RTT profile
	parts = append(parts, string(p.RTT))

	// Timer profile (only if non-default)
	if p.Timer != "" && p.Timer != TimerDefault {
		parts = append(parts, string(p.Timer))
	}

	// Config (parallel: baseline-vs-highperf, others: just config)
	if p.Mode == TestModeParallel || p.Mode == TestModeIsolation {
		parts = append(parts, fmt.Sprintf("%s-vs-%s", p.Baseline, p.HighPerf))
	} else {
		parts = append(parts, string(p.HighPerf))
	}

	return strings.Join(parts, "-")
}

// GenerateParallelTests generates all parallel tests based on the matrix config.
func GenerateParallelTests(cfg ParallelMatrixConfig) []GeneratedParallelTest {
	var tests []GeneratedParallelTest

	defaults := TestMatrixParams{
		Mode:     TestModeParallel,
		Pattern:  "Starlink",
		Bitrate:  20_000_000,
		Buffer:   5 * time.Second,
		RTT:      RTT60,
		Loss:     0.0,
		Timer:    TimerDefault,
		Baseline: cfg.Baseline,
		HighPerf: ConfigFull,
	}

	// ========================================================================
	// TIER 1: Core Tests (~27 tests)
	// ========================================================================

	// 1. FastNAK Permutations (6 tests) - Tier 1
	for _, bitrate := range []int64{20_000_000, 50_000_000} {
		for _, config := range []ConfigVariant{ConfigNakBtree, ConfigNakBtreeF, ConfigNakBtreeFr} {
			p := defaults
			p.Bitrate = bitrate
			p.HighPerf = config
			tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierCore))
		}
	}

	// 2. RTT Sweep (5 tests) - Tier 1
	for _, rtt := range cfg.RTTs {
		p := defaults
		p.RTT = rtt
		p.HighPerf = ConfigFull
		tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierCore))
	}

	// 3. Buffer Sweep (4 tests) - Tier 1
	for _, buffer := range cfg.Buffers {
		p := defaults
		p.Buffer = buffer
		p.HighPerf = ConfigFull
		tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierCore))
	}

	// 4. Background Loss Sweep (4 tests) - Tier 1
	for _, loss := range []float64{0.02, 0.05, 0.10, 0.15} {
		p := defaults
		p.Loss = loss
		p.HighPerf = ConfigFull
		tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierCore))
	}

	// 5. Stress Tests (4 tests) - Tier 1
	stressTests := []TestMatrixParams{
		{Mode: TestModeParallel, Pattern: "Starlink", Bitrate: 50_000_000, Buffer: 1 * time.Second, RTT: RTT60, HighPerf: ConfigFull, Baseline: cfg.Baseline},
		{Mode: TestModeParallel, Pattern: "Starlink", Bitrate: 50_000_000, Buffer: 30 * time.Second, RTT: RTT60, HighPerf: ConfigFull, Baseline: cfg.Baseline},
		{Mode: TestModeParallel, Pattern: "Starlink", Bitrate: 20_000_000, Buffer: 5 * time.Second, RTT: RTT300, HighPerf: ConfigFull, Baseline: cfg.Baseline},
		{Mode: TestModeParallel, Pattern: "Starlink", Bitrate: 50_000_000, Buffer: 5 * time.Second, RTT: RTT300, HighPerf: ConfigFull, Baseline: cfg.Baseline},
	}
	for _, p := range stressTests {
		duration := cfg.DefaultDuration
		if p.Bitrate >= 50_000_000 || p.RTT == RTT300 {
			duration = 120 * time.Second
		}
		tests = append(tests, buildParallelConfig(p, duration, TierCore))
	}

	// 6. Timer Interval Tests (4 tests) - Tier 1
	for _, timer := range []TimerProfile{TimerFast, TimerSlow, TimerFastNak, TimerSlowNak} {
		p := defaults
		p.Timer = timer
		p.HighPerf = ConfigFull
		tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierCore))
	}

	// ========================================================================
	// TIER 2: Extended Coverage (~26 tests)
	// ========================================================================

	// Bitrate × Buffer Cross-Product (8 tests) - Tier 2
	for _, bitrate := range cfg.Bitrates {
		for _, buffer := range cfg.Buffers {
			p := defaults
			p.Bitrate = bitrate
			p.Buffer = buffer
			p.HighPerf = ConfigFull
			tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierDaily))
		}
	}

	// RTT × Loss Cross-Product (9 tests) - Tier 2
	for _, rtt := range []RTTProfile{RTT10, RTT60, RTT130} {
		for _, loss := range []float64{0.02, 0.05, 0.10} {
			p := defaults
			p.RTT = rtt
			p.Loss = loss
			p.HighPerf = ConfigFull
			tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierDaily))
		}
	}

	// FastNAK × RTT Cross-Product (9 tests) - Tier 2
	for _, rtt := range []RTTProfile{RTT10, RTT130, RTT300} {
		for _, config := range []ConfigVariant{ConfigNakBtree, ConfigNakBtreeF, ConfigNakBtreeFr} {
			p := defaults
			p.RTT = rtt
			p.HighPerf = config
			tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierDaily))
		}
	}

	// ========================================================================
	// TIER 3: Comprehensive Tests (~48 tests)
	// ========================================================================

	// Full RTT × Buffer Matrix (20 tests) - Tier 3
	for _, rtt := range cfg.RTTs {
		for _, buffer := range cfg.Buffers {
			p := defaults
			p.RTT = rtt
			p.Buffer = buffer
			p.HighPerf = ConfigFull
			tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierNightly))
		}
	}

	// Full Loss × Bitrate Matrix (10 tests) - Tier 3
	for _, bitrate := range cfg.Bitrates {
		for _, loss := range cfg.Losses {
			p := defaults
			p.Bitrate = bitrate
			p.Loss = loss
			p.HighPerf = ConfigFull
			tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierNightly))
		}
	}

	// FastNAK × Bitrate × RTT (18 tests) - Tier 3
	for _, config := range []ConfigVariant{ConfigNakBtree, ConfigNakBtreeF, ConfigNakBtreeFr} {
		for _, bitrate := range cfg.Bitrates {
			for _, rtt := range []RTTProfile{RTT10, RTT60, RTT130} {
				p := defaults
				p.HighPerf = config
				p.Bitrate = bitrate
				p.RTT = rtt
				tests = append(tests, buildParallelConfig(p, cfg.DefaultDuration, TierNightly))
			}
		}
	}

	return deduplicateTests(tests)
}

// buildParallelConfig creates a GeneratedParallelTest from TestMatrixParams.
func buildParallelConfig(p TestMatrixParams, duration time.Duration, tier TestTier) GeneratedParallelTest {
	p.Mode = TestModeParallel
	if p.Baseline == "" {
		p.Baseline = ConfigBase
	}

	// Build SRT configs
	baseConfig := GetSRTConfig(p.Baseline).WithLatency(p.Buffer)
	highConfig := GetSRTConfig(p.HighPerf).WithLatency(p.Buffer)

	// Apply timer profile if non-default
	if p.Timer != "" && p.Timer != TimerDefault {
		baseConfig = baseConfig.WithTimerProfile(p.Timer)
		highConfig = highConfig.WithTimerProfile(p.Timer)
	}

	// Build network impairment
	impairment := NetworkImpairment{
		Pattern:        p.Pattern,
		LossRate:       p.Loss,
		LatencyMs:      GetRTTMs(p.RTT),
		LatencyProfile: GetLatencyProfile(p.RTT),
	}

	// Build ParallelTestConfig
	config := ParallelTestConfig{
		Name:         GenerateTestName(p),
		Description:  generateDescription(p),
		Impairment:   impairment,
		Bitrate:      p.Bitrate,
		TestDuration: duration,
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          baseConfig,
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          highConfig,
		},
	}

	return GeneratedParallelTest{
		Name:        config.Name,
		Description: config.Description,
		Tier:        tier,
		Params:      p,
		Duration:    duration,
		Config:      config,
	}
}

// generateDescription creates a human-readable test description.
func generateDescription(p TestMatrixParams) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("%s pattern", p.Pattern))
	parts = append(parts, fmt.Sprintf("%d Mb/s", p.Bitrate/1_000_000))
	parts = append(parts, fmt.Sprintf("%s buffer", p.Buffer))
	parts = append(parts, fmt.Sprintf("%dms RTT", GetRTTMs(p.RTT)))

	if p.Loss > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%% loss", p.Loss*100))
	}

	if p.Timer != "" && p.Timer != TimerDefault {
		parts = append(parts, fmt.Sprintf("%s timers", p.Timer))
	}

	if p.Mode == TestModeParallel || p.Mode == TestModeIsolation {
		parts = append(parts, fmt.Sprintf("%s vs %s", p.Baseline, p.HighPerf))
	}

	return strings.Join(parts, ", ")
}

// deduplicateTests removes duplicate tests based on name.
func deduplicateTests(tests []GeneratedParallelTest) []GeneratedParallelTest {
	seen := make(map[string]bool)
	var result []GeneratedParallelTest

	for _, t := range tests {
		if !seen[t.Name] {
			seen[t.Name] = true
			result = append(result, t)
		}
	}

	return result
}

// ============================================================================
// FILTERING AND UTILITIES
// ============================================================================

// CountByTier counts tests by tier.
func CountByTier(tests []GeneratedParallelTest) map[TestTier]int {
	counts := make(map[TestTier]int)
	for _, t := range tests {
		counts[t.Tier]++
	}
	return counts
}

// FilterTestsByTier returns tests up to and including the specified tier.
func FilterTestsByTier(tests []GeneratedParallelTest, maxTier TestTier) []GeneratedParallelTest {
	var result []GeneratedParallelTest
	for _, t := range tests {
		if t.Tier <= maxTier {
			result = append(result, t)
		}
	}
	return result
}

// FilterTestsByConfig returns tests that use the specified high-perf config.
func FilterTestsByConfig(tests []GeneratedParallelTest, config ConfigVariant) []GeneratedParallelTest {
	var result []GeneratedParallelTest
	for _, t := range tests {
		if t.Params.HighPerf == config {
			result = append(result, t)
		}
	}
	return result
}

// FilterTestsByRTT returns tests that use the specified RTT profile.
func FilterTestsByRTT(tests []GeneratedParallelTest, rtt RTTProfile) []GeneratedParallelTest {
	var result []GeneratedParallelTest
	for _, t := range tests {
		if t.Params.RTT == rtt {
			result = append(result, t)
		}
	}
	return result
}

// FilterTestsByBitrate returns tests that use the specified bitrate.
func FilterTestsByBitrate(tests []GeneratedParallelTest, bitrate int64) []GeneratedParallelTest {
	var result []GeneratedParallelTest
	for _, t := range tests {
		if t.Params.Bitrate == bitrate {
			result = append(result, t)
		}
	}
	return result
}

// PrintTestMatrix prints the generated test matrix for debugging/review.
func PrintTestMatrix(tests []GeneratedParallelTest) {
	fmt.Printf("Generated %d tests:\n\n", len(tests))

	tierNames := map[TestTier]string{
		TierCore:    "Core",
		TierDaily:   "Daily",
		TierNightly: "Nightly",
	}

	for i, t := range tests {
		fmt.Printf("%3d. [%s] %s\n", i+1, tierNames[t.Tier], t.Name)
		fmt.Printf("     %s\n", t.Description)
		fmt.Printf("     Duration: %s\n\n", t.Duration)
	}
}

// PrintTestSummary prints a summary of test counts by tier and category.
func PrintTestSummary(tests []GeneratedParallelTest) {
	counts := CountByTier(tests)

	fmt.Println("Test Matrix Summary")
	fmt.Println("===================")
	fmt.Printf("Total tests: %d\n\n", len(tests))
	fmt.Printf("By Tier:\n")
	fmt.Printf("  Tier 1 (Core):    %d tests\n", counts[TierCore])
	fmt.Printf("  Tier 2 (Daily):   %d tests\n", counts[TierDaily])
	fmt.Printf("  Tier 3 (Nightly): %d tests\n", counts[TierNightly])

	// Count by config
	fmt.Printf("\nBy Config:\n")
	for _, config := range []ConfigVariant{ConfigNakBtree, ConfigNakBtreeF, ConfigNakBtreeFr, ConfigFull} {
		filtered := FilterTestsByConfig(tests, config)
		fmt.Printf("  %s: %d tests\n", config, len(filtered))
	}

	// Count by RTT
	fmt.Printf("\nBy RTT:\n")
	for _, rtt := range []RTTProfile{RTT0, RTT10, RTT60, RTT130, RTT300} {
		filtered := FilterTestsByRTT(tests, rtt)
		fmt.Printf("  %s: %d tests\n", rtt, len(filtered))
	}
}

// ============================================================================
// CLEAN NETWORK TEST GENERATION
// ============================================================================

// GeneratedCleanTest represents a matrix-generated clean network test.
type GeneratedCleanTest struct {
	Name        string        // Generated name following convention
	Description string        // Human-readable description
	Tier        TestTier      // Which tier this test belongs to
	Duration    time.Duration // Test duration
	Config      TestConfig    // The actual test configuration
}

// CleanTestParams represents parameters for a clean network test.
type CleanTestParams struct {
	Bitrate int64         // Bits per second
	Buffer  time.Duration // SRT latency buffer
	Config  ConfigVariant // Configuration variant
	Timer   TimerProfile  // Timer profile (optional)
}

// GenerateCleanTestName creates a standardized name for a clean network test.
// Format: Int-Clean-{Bitrate}-{Buffer}-{Config}[-{Timer}]
//
// Examples:
//   - Int-Clean-20M-5s-Base
//   - Int-Clean-50M-10s-NakBtree
//   - Int-Clean-20M-5s-Full-T-FastNak
func GenerateCleanTestName(p CleanTestParams) string {
	var parts []string

	parts = append(parts, "Int-Clean")
	parts = append(parts, fmt.Sprintf("%dM", p.Bitrate/1_000_000))
	parts = append(parts, fmt.Sprintf("%ds", int(p.Buffer.Seconds())))
	parts = append(parts, string(p.Config))

	if p.Timer != "" && p.Timer != TimerDefault {
		parts = append(parts, string(p.Timer))
	}

	return strings.Join(parts, "-")
}

// GenerateCleanNetworkTests generates clean network tests following the matrix approach.
// These tests run without network impairment on loopback interface.
func GenerateCleanNetworkTests() []GeneratedCleanTest {
	var tests []GeneratedCleanTest

	// Default parameters
	defaultBitrate := int64(20_000_000)
	defaultBuffer := 5 * time.Second
	defaultDuration := 15 * time.Second

	// ========================================================================
	// TIER 1: Core Clean Network Tests
	// ========================================================================

	// 1. Config variant sweep at default bitrate/buffer (7 tests)
	for _, config := range []ConfigVariant{ConfigBase, ConfigBtree, ConfigIoUr, ConfigNakBtree, ConfigNakBtreeF, ConfigNakBtreeFr, ConfigFull} {
		p := CleanTestParams{
			Bitrate: defaultBitrate,
			Buffer:  defaultBuffer,
			Config:  config,
		}
		tests = append(tests, buildCleanTest(p, defaultDuration, TierCore))
	}

	// 2. Bitrate sweep with Full config (3 tests: 5M, 20M, 50M)
	for _, bitrate := range []int64{5_000_000, 20_000_000, 50_000_000} {
		p := CleanTestParams{
			Bitrate: bitrate,
			Buffer:  defaultBuffer,
			Config:  ConfigFull,
		}
		tests = append(tests, buildCleanTest(p, defaultDuration, TierCore))
	}

	// 3. Buffer sweep with Full config (4 tests: 1s, 5s, 10s, 30s)
	for _, buffer := range []time.Duration{1 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second} {
		p := CleanTestParams{
			Bitrate: defaultBitrate,
			Buffer:  buffer,
			Config:  ConfigFull,
		}
		tests = append(tests, buildCleanTest(p, defaultDuration, TierCore))
	}

	// ========================================================================
	// TIER 2: Extended Clean Network Tests
	// ========================================================================

	// NAK btree variants at different bitrates (6 tests)
	for _, bitrate := range []int64{5_000_000, 50_000_000} {
		for _, config := range []ConfigVariant{ConfigNakBtree, ConfigNakBtreeF, ConfigNakBtreeFr} {
			p := CleanTestParams{
				Bitrate: bitrate,
				Buffer:  defaultBuffer,
				Config:  config,
			}
			tests = append(tests, buildCleanTest(p, defaultDuration, TierDaily))
		}
	}

	// Timer profile tests (4 tests)
	for _, timer := range []TimerProfile{TimerFast, TimerSlow, TimerFastNak, TimerSlowNak} {
		p := CleanTestParams{
			Bitrate: defaultBitrate,
			Buffer:  defaultBuffer,
			Config:  ConfigFull,
			Timer:   timer,
		}
		tests = append(tests, buildCleanTest(p, defaultDuration, TierDaily))
	}

	// ========================================================================
	// TIER 3: Comprehensive Clean Network Tests
	// ========================================================================

	// Bitrate × Buffer cross-product with Full config (12 tests)
	for _, bitrate := range []int64{5_000_000, 20_000_000, 50_000_000} {
		for _, buffer := range []time.Duration{1 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second} {
			p := CleanTestParams{
				Bitrate: bitrate,
				Buffer:  buffer,
				Config:  ConfigFull,
			}
			tests = append(tests, buildCleanTest(p, defaultDuration, TierNightly))
		}
	}

	// NAK btree × Buffer cross-product (12 tests)
	for _, config := range []ConfigVariant{ConfigNakBtree, ConfigNakBtreeF, ConfigNakBtreeFr} {
		for _, buffer := range []time.Duration{1 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second} {
			p := CleanTestParams{
				Bitrate: defaultBitrate,
				Buffer:  buffer,
				Config:  config,
			}
			tests = append(tests, buildCleanTest(p, defaultDuration, TierNightly))
		}
	}

	return deduplicateCleanTests(tests)
}

// buildCleanTest creates a GeneratedCleanTest from CleanTestParams.
func buildCleanTest(p CleanTestParams, duration time.Duration, tier TestTier) GeneratedCleanTest {
	// Build SRT config
	srtConfig := GetSRTConfig(p.Config).WithLatency(p.Buffer)

	// Apply timer profile if non-default
	if p.Timer != "" && p.Timer != TimerDefault {
		srtConfig = srtConfig.WithTimerProfile(p.Timer)
	}

	name := GenerateCleanTestName(p)

	// Build description
	var descParts []string
	descParts = append(descParts, fmt.Sprintf("%d Mb/s", p.Bitrate/1_000_000))
	descParts = append(descParts, fmt.Sprintf("%s buffer", p.Buffer))
	descParts = append(descParts, fmt.Sprintf("%s config", p.Config))
	if p.Timer != "" && p.Timer != TimerDefault {
		descParts = append(descParts, fmt.Sprintf("%s timers", p.Timer))
	}
	description := "Clean network: " + strings.Join(descParts, ", ")

	// Build TestConfig
	config := TestConfig{
		Name:            name,
		Description:     description,
		Mode:            TestModeClean,
		Bitrate:         p.Bitrate,
		TestDuration:    duration,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &srtConfig, // Apply to all components
	}

	return GeneratedCleanTest{
		Name:        name,
		Description: description,
		Tier:        tier,
		Duration:    duration,
		Config:      config,
	}
}

// deduplicateCleanTests removes duplicate tests based on name.
func deduplicateCleanTests(tests []GeneratedCleanTest) []GeneratedCleanTest {
	seen := make(map[string]bool)
	var result []GeneratedCleanTest

	for _, t := range tests {
		if !seen[t.Name] {
			seen[t.Name] = true
			result = append(result, t)
		}
	}

	return result
}

// FilterCleanTestsByTier returns tests up to and including the specified tier.
func FilterCleanTestsByTier(tests []GeneratedCleanTest, maxTier TestTier) []GeneratedCleanTest {
	var result []GeneratedCleanTest
	for _, t := range tests {
		if t.Tier <= maxTier {
			result = append(result, t)
		}
	}
	return result
}

// CountCleanByTier counts clean tests by tier.
func CountCleanByTier(tests []GeneratedCleanTest) map[TestTier]int {
	counts := make(map[TestTier]int)
	for _, t := range tests {
		counts[t.Tier]++
	}
	return counts
}

// PrintCleanTestMatrix prints the generated clean test matrix.
func PrintCleanTestMatrix(tests []GeneratedCleanTest) {
	tierNames := map[TestTier]string{
		TierCore:    "Core",
		TierDaily:   "Daily",
		TierNightly: "Nightly",
	}

	fmt.Printf("Matrix-Generated Clean Network Tests (%d total):\n\n", len(tests))
	for i, t := range tests {
		fmt.Printf("  %3d. [%-8s] %-45s %s\n", i+1, tierNames[t.Tier], t.Name, t.Duration)
	}
}

// PrintCleanTestSummary prints a summary of clean test counts.
func PrintCleanTestSummary(tests []GeneratedCleanTest) {
	counts := CountCleanByTier(tests)

	fmt.Println("Clean Network Test Matrix Summary")
	fmt.Println("==================================")
	fmt.Printf("Total tests: %d\n\n", len(tests))
	fmt.Printf("By Tier:\n")
	fmt.Printf("  Tier 1 (Core):    %d tests\n", counts[TierCore])
	fmt.Printf("  Tier 2 (Daily):   %d tests\n", counts[TierDaily])
	fmt.Printf("  Tier 3 (Nightly): %d tests\n", counts[TierNightly])

	// Calculate total duration
	var totalDuration time.Duration
	for _, t := range tests {
		totalDuration += t.Duration
	}
	fmt.Printf("\nEstimated total runtime: %s\n", totalDuration.Round(time.Minute))
}
