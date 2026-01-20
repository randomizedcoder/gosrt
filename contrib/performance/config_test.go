package main

import (
	"testing"
	"time"
)

func TestParseBitrate(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		// Basic numbers
		{"1000000", 1_000_000, false},
		{"100", 100, false},

		// K suffix
		{"100K", 100_000, false},
		{"100k", 100_000, false},
		{"1.5K", 1_500, false},

		// M suffix
		{"100M", 100_000_000, false},
		{"100m", 100_000_000, false},
		{"1.5M", 1_500_000, false},
		{"500M", 500_000_000, false},

		// G suffix
		{"1G", 1_000_000_000, false},
		{"1g", 1_000_000_000, false},
		{"1.5G", 1_500_000_000, false},

		// Whitespace
		{"  100M  ", 100_000_000, false},

		// Errors
		{"", 0, true},
		{"abc", 0, true},
		{"-100M", 0, true},
		{"0M", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseBitrate(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseBitrate(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("ParseBitrate(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseBytes(t *testing.T) {
	tests := []struct {
		input    string
		expected uint32
		wantErr  bool
	}{
		// Basic numbers
		{"1024", 1024, false},
		{"100", 100, false},

		// K suffix (1024 multiplier)
		{"1K", 1024, false},
		{"64K", 65536, false},

		// M suffix
		{"1M", 1024 * 1024, false},
		{"64M", 64 * 1024 * 1024, false},

		// G suffix
		{"1G", 1024 * 1024 * 1024, false},

		// Errors
		{"", 0, true},
		{"abc", 0, true},
		{"-1M", 0, true},
		{"0M", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseBytes(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseBytes(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("ParseBytes(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"1s", 1 * time.Second, false},
		{"500ms", 500 * time.Millisecond, false},
		{"5m", 5 * time.Minute, false},
		{"1h", 1 * time.Hour, false},
		{"2s500ms", 2500 * time.Millisecond, false},

		// Errors
		{"", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDuration(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDuration(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseArgs_Defaults(t *testing.T) {
	config, err := ParseArgs(map[string]string{})
	if err != nil {
		t.Fatalf("ParseArgs with empty args failed: %v", err)
	}

	// Check defaults
	if config.Search.InitialBitrate != 200_000_000 {
		t.Errorf("InitialBitrate = %d, want 200000000", config.Search.InitialBitrate)
	}
	if config.Search.MaxBitrate != 600_000_000 {
		t.Errorf("MaxBitrate = %d, want 600000000", config.Search.MaxBitrate)
	}
	if config.SRT.FC != 102400 {
		t.Errorf("FC = %d, want 102400", config.SRT.FC)
	}
}

func TestParseArgs_Custom(t *testing.T) {
	args := map[string]string{
		"INITIAL":    "100M",
		"MAX":        "500M",
		"STEP":       "20M",
		"FC":         "204800",
		"RECV_RINGS": "4",
		"VERBOSE":    "true",
	}

	config, err := ParseArgs(args)
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	if config.Search.InitialBitrate != 100_000_000 {
		t.Errorf("InitialBitrate = %d, want 100000000", config.Search.InitialBitrate)
	}
	if config.Search.MaxBitrate != 500_000_000 {
		t.Errorf("MaxBitrate = %d, want 500000000", config.Search.MaxBitrate)
	}
	if config.Search.StepSize != 20_000_000 {
		t.Errorf("StepSize = %d, want 20000000", config.Search.StepSize)
	}
	if config.SRT.FC != 204800 {
		t.Errorf("FC = %d, want 204800", config.SRT.FC)
	}
	if config.SRT.RecvRings != 4 {
		t.Errorf("RecvRings = %d, want 4", config.SRT.RecvRings)
	}
	if !config.Verbose {
		t.Error("Verbose should be true")
	}
}

func TestParseArgs_InvalidValue(t *testing.T) {
	args := map[string]string{
		"INITIAL": "not-a-number",
	}

	_, err := ParseArgs(args)
	if err == nil {
		t.Error("Expected error for invalid bitrate")
	}
}

func TestParseArgs_TimingSync(t *testing.T) {
	args := map[string]string{
		"WARMUP":    "3s",
		"STABILITY": "8s",
		"PRECISION": "10M",
	}

	config, err := ParseArgs(args)
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	// Timing model should be synced with stability config
	if config.Timing.WarmUpDuration != 3*time.Second {
		t.Errorf("Timing.WarmUpDuration = %v, want 3s", config.Timing.WarmUpDuration)
	}
	if config.Timing.StabilityWindow != 8*time.Second {
		t.Errorf("Timing.StabilityWindow = %v, want 8s", config.Timing.StabilityWindow)
	}
	if config.Timing.Precision != 10_000_000 {
		t.Errorf("Timing.Precision = %d, want 10000000", config.Timing.Precision)
	}

	// Derived values should be computed
	expectedMinProbe := 3*time.Second + 8*time.Second
	if config.Timing.MinProbeDuration != expectedMinProbe {
		t.Errorf("Timing.MinProbeDuration = %v, want %v", config.Timing.MinProbeDuration, expectedMinProbe)
	}
}

func TestDefaultConfig_Valid(t *testing.T) {
	config := DefaultConfig()

	// Timing should be valid
	if err := config.Timing.ValidateContracts(); err != nil {
		t.Errorf("Default config timing should be valid: %v", err)
	}

	// Search config should be reasonable
	if config.Search.InitialBitrate < config.Search.MinBitrate {
		t.Error("InitialBitrate should be >= MinBitrate")
	}
	if config.Search.InitialBitrate > config.Search.MaxBitrate {
		t.Error("InitialBitrate should be <= MaxBitrate")
	}
}

func TestFormatBitrate(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1_500_000_000, "1.50 Gb/s"},
		{1_000_000_000, "1.00 Gb/s"},
		{500_000_000, "500.00 Mb/s"},
		{100_000_000, "100.00 Mb/s"},
		{1_500_000, "1.50 Mb/s"},
		{1_000_000, "1.00 Mb/s"},
		{500_000, "500.00 Kb/s"},
		{1_000, "1.00 Kb/s"},
		{500, "500 b/s"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := FormatBitrate(tt.input)
			if got != tt.expected {
				t.Errorf("FormatBitrate(%d) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFormatBitrateShort(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1_000_000_000, "1G"},
		{500_000_000, "500M"},
		{100_000_000, "100M"},
		{1_000_000, "1M"},
		{500_000, "500K"},
		{1_000, "1K"},
		{500, "500"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := FormatBitrateShort(tt.input)
			if got != tt.expected {
				t.Errorf("FormatBitrateShort(%d) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
