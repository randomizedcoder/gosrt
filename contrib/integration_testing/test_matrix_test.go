package main

import (
	"testing"
	"time"
)

func TestGenerateTestName(t *testing.T) {
	tests := []struct {
		name     string
		params   TestMatrixParams
		expected string
	}{
		{
			name: "basic parallel test",
			params: TestMatrixParams{
				Mode:     TestModeParallel,
				Pattern:  "Starlink",
				Bitrate:  20_000_000,
				Buffer:   5 * time.Second,
				RTT:      RTT60,
				Loss:     0,
				Baseline: ConfigBase,
				HighPerf: ConfigFull,
			},
			expected: "Parallel-Starlink-20M-5s-R60-Base-vs-Full",
		},
		{
			name: "parallel with loss",
			params: TestMatrixParams{
				Mode:     TestModeParallel,
				Pattern:  "Starlink",
				Bitrate:  20_000_000,
				Buffer:   5 * time.Second,
				RTT:      RTT60,
				Loss:     0.05,
				Baseline: ConfigBase,
				HighPerf: ConfigFull,
			},
			expected: "Parallel-Starlink-L5-20M-5s-R60-Base-vs-Full",
		},
		{
			name: "parallel with NAK btree only",
			params: TestMatrixParams{
				Mode:     TestModeParallel,
				Pattern:  "Starlink",
				Bitrate:  50_000_000,
				Buffer:   10 * time.Second,
				RTT:      RTT300,
				Loss:     0,
				Baseline: ConfigBase,
				HighPerf: ConfigNakBtree,
			},
			expected: "Parallel-Starlink-50M-10s-R300-Base-vs-NakBtree",
		},
		{
			name: "parallel with timer profile",
			params: TestMatrixParams{
				Mode:     TestModeParallel,
				Pattern:  "Starlink",
				Bitrate:  20_000_000,
				Buffer:   5 * time.Second,
				RTT:      RTT60,
				Loss:     0,
				Baseline: ConfigBase,
				HighPerf: ConfigFull,
				Timer:    TimerFastNak,
			},
			expected: "Parallel-Starlink-20M-5s-R60-T-FastNak-Base-vs-Full",
		},
		{
			name: "network test",
			params: TestMatrixParams{
				Mode:     TestModeNetwork,
				Pattern:  "Starlink",
				Bitrate:  20_000_000,
				Buffer:   5 * time.Second,
				RTT:      RTT60,
				Loss:     0.10,
				HighPerf: ConfigFull,
			},
			expected: "Net-Starlink-L10-20M-5s-R60-Full",
		},
		{
			name: "isolation test",
			params: TestMatrixParams{
				Mode:     TestModeIsolation,
				Pattern:  "Clean",
				Bitrate:  20_000_000,
				Buffer:   5 * time.Second,
				RTT:      RTT0,
				Loss:     0,
				Baseline: ConfigBase,
				HighPerf: ConfigNakBtreeF,
			},
			expected: "Isolation-Clean-20M-5s-R0-Base-vs-NakBtreeF",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateTestName(tt.params)
			if result != tt.expected {
				t.Errorf("GenerateTestName() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGenerateStrategicParallelMatrix(t *testing.T) {
	cfg := DefaultParallelMatrixConfig()
	tests := GenerateParallelTests(cfg)

	// Verify we got tests
	if len(tests) == 0 {
		t.Fatal("GenerateParallelTests() returned no tests")
	}

	// Count by tier
	counts := CountByTier(tests)

	t.Logf("Generated %d tests total", len(tests))
	t.Logf("  Tier 1 (Core): %d", counts[TierCore])
	t.Logf("  Tier 2 (Daily): %d", counts[TierDaily])
	t.Logf("  Tier 3 (Nightly): %d", counts[TierNightly])

	// Verify we have tests in each tier
	if counts[TierCore] == 0 {
		t.Error("No Tier 1 (Core) tests generated")
	}
	if counts[TierDaily] == 0 {
		t.Error("No Tier 2 (Daily) tests generated")
	}

	// Verify expected Tier 1 count (approximately 27)
	if counts[TierCore] < 20 || counts[TierCore] > 35 {
		t.Errorf("Tier 1 count %d outside expected range [20, 35]", counts[TierCore])
	}
}

func TestFilterTestsByTier(t *testing.T) {
	cfg := DefaultParallelMatrixConfig()
	allTests := GenerateParallelTests(cfg)

	tier1Only := FilterTestsByTier(allTests, TierCore)
	tier1and2 := FilterTestsByTier(allTests, TierDaily)
	allTiers := FilterTestsByTier(allTests, TierNightly)

	if len(tier1Only) >= len(tier1and2) {
		t.Error("Tier 1+2 should have more tests than Tier 1 only")
	}
	if len(tier1and2) > len(allTiers) {
		t.Error("All tiers should have at least as many tests as Tier 1+2")
	}

	t.Logf("Tier 1 only: %d tests", len(tier1Only))
	t.Logf("Tier 1+2: %d tests", len(tier1and2))
	t.Logf("All tiers: %d tests", len(allTiers))
}

func TestFilterTestsByConfig(t *testing.T) {
	cfg := DefaultParallelMatrixConfig()
	allTests := GenerateParallelTests(cfg)

	nakBtreeTests := FilterTestsByConfig(allTests, ConfigNakBtree)
	fullTests := FilterTestsByConfig(allTests, ConfigFull)

	if len(nakBtreeTests) == 0 {
		t.Error("No NakBtree tests found")
	}
	if len(fullTests) == 0 {
		t.Error("No Full tests found")
	}

	t.Logf("NakBtree tests: %d", len(nakBtreeTests))
	t.Logf("Full tests: %d", len(fullTests))
}

func TestBuildParallelConfig(t *testing.T) {
	params := TestMatrixParams{
		Mode:     TestModeParallel,
		Pattern:  "Starlink",
		Bitrate:  20_000_000,
		Buffer:   5 * time.Second,
		RTT:      RTT60,
		Loss:     0.05,
		Baseline: ConfigBase,
		HighPerf: ConfigFull,
	}

	generated := buildParallelConfig(params, 90*time.Second, TierCore)
	config := generated.Config

	// Verify basic config
	if config.Name == "" {
		t.Error("Config name is empty")
	}
	if config.Bitrate != params.Bitrate {
		t.Errorf("Bitrate = %d, want %d", config.Bitrate, params.Bitrate)
	}
	if config.TestDuration != 90*time.Second {
		t.Errorf("Duration = %v, want 90s", config.TestDuration)
	}

	// Verify impairment
	if config.Impairment.LossRate != 0.05 {
		t.Errorf("LossRate = %f, want 0.05", config.Impairment.LossRate)
	}
	if config.Impairment.LatencyMs != 60 {
		t.Errorf("LatencyMs = %d, want 60", config.Impairment.LatencyMs)
	}

	// Verify pipeline configs
	if config.Baseline.ServerPort != 6000 {
		t.Errorf("Baseline ServerPort = %d, want 6000", config.Baseline.ServerPort)
	}
	if config.HighPerf.ServerPort != 6001 {
		t.Errorf("HighPerf ServerPort = %d, want 6001", config.HighPerf.ServerPort)
	}

	// Verify SRT latency was applied
	if config.Baseline.SRT.Latency != 5*time.Second {
		t.Errorf("Baseline Latency = %v, want 5s", config.Baseline.SRT.Latency)
	}
}

func TestDeduplicateTests(t *testing.T) {
	tests := []GeneratedParallelTest{
		{Name: "test-a"},
		{Name: "test-b"},
		{Name: "test-a"}, // Duplicate
		{Name: "test-c"},
		{Name: "test-b"}, // Duplicate
	}

	result := deduplicateTests(tests)

	if len(result) != 3 {
		t.Errorf("deduplicateTests() returned %d tests, want 3", len(result))
	}

	// Verify order is preserved
	expected := []string{"test-a", "test-b", "test-c"}
	for i, name := range expected {
		if result[i].Name != name {
			t.Errorf("result[%d].Name = %q, want %q", i, result[i].Name, name)
		}
	}
}
