package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/randomizedcoder/gosrt/contrib/common"
)

// SearchConfig defines parameters for the binary search algorithm.
type SearchConfig struct {
	InitialBitrate  int64         // Starting bitrate (default: 200 Mb/s)
	MinBitrate      int64         // Floor bitrate (default: 50 Mb/s)
	MaxBitrate      int64         // Ceiling bitrate (default: 600 Mb/s)
	StepSize        int64         // Additive increase step (default: 10 Mb/s)
	DecreasePercent float64       // Multiplicative decrease (default: 0.25 = 25%)
	Precision       int64         // Stop when high-low < precision (default: 5 Mb/s)
	Timeout         time.Duration // Maximum search time (default: 10 min)
}

// StabilityConfig defines thresholds for stability evaluation.
type StabilityConfig struct {
	// Timing (from TimingModel)
	WarmUpDuration  time.Duration // Ignore metrics after bitrate change
	StabilityWindow time.Duration // Window for evaluation
	SampleInterval  time.Duration // Prometheus scrape interval

	// Stability thresholds (stable if ALL below)
	MaxGapRate    float64 // Max gaps per second (default: 0.01)
	MaxNAKRate    float64 // Max NAKs per second (default: 0.02)
	MaxRTTMs      float64 // Max RTT in milliseconds (default: 100)
	MinThroughput float64 // Min throughput ratio vs target (default: 0.95)

	// Critical thresholds (abort immediately if ANY exceeded)
	CriticalGapRate float64 // Abort threshold (default: 0.05)
	CriticalNAKRate float64 // Abort threshold (default: 0.10)
}

// SRTConfig holds SRT-specific configuration for tests.
type SRTConfig struct {
	FC        uint32 // Flow control window
	RecvRings int    // Number of receive rings
}

// Config combines all configuration for a performance test.
// SRT configuration is obtained from common.FlagSet and passed to subprocesses
// via common.BuildFlagArgs().
type Config struct {
	Search    SearchConfig
	Stability StabilityConfig
	Timing    TimingModel
	SRT       SRTConfig

	// Process paths
	ServerBinary string
	SeekerBinary string
	ServerAddr   string

	// Unix domain socket paths
	ServerPromUDS    string // Server Prometheus metrics UDS
	SeekerPromUDS    string // Seeker Prometheus metrics UDS
	SeekerControlUDS string // Seeker control socket UDS

	// Profiling
	ProfileDir string // Directory for profile output

	// Output options
	Verbose        bool
	JSONOutput     bool
	OutputFile     string
	OutputPath     string        // Path for probe history JSON
	StatusInterval time.Duration // Interval for progress status updates (0=disabled)
}

// ConfigFromFlags creates configuration from parsed common flags.
// Call common.ParseFlags() before this function.
//
// SRT configuration (fc, rcvbuf, iouringrecvringcount, etc.) is NOT stored
// in Config. Instead, use common.BuildFlagArgs() to pass these to subprocesses.
func ConfigFromFlags() *Config {
	cfg := &Config{
		Search: SearchConfig{
			InitialBitrate:  *common.TestInitialBitrate,
			MinBitrate:      *common.TestMinBitrate,
			MaxBitrate:      *common.TestMaxBitrate,
			StepSize:        *common.TestStepSize,
			DecreasePercent: *common.TestDecreasePercent,
			Precision:       *common.TestPrecision,
			Timeout:         *common.TestSearchTimeout,
		},
		Stability: StabilityConfig{
			WarmUpDuration:  *common.TestWarmUpDuration,
			StabilityWindow: *common.TestStabilityWindow,
			SampleInterval:  *common.TestSampleInterval,
			MaxGapRate:      *common.TestMaxGapRate,
			MaxNAKRate:      *common.TestMaxNAKRate,
			MaxRTTMs:        *common.TestMaxRTTMs,
			MinThroughput:   *common.TestMinThroughput,
			CriticalGapRate: 0.05, // 5% gaps = critical (not configurable)
			CriticalNAKRate: 0.10, // 10% NAKs = critical (not configurable)
		},
		Timing: DefaultTimingModel(),

		ServerBinary: "./contrib/server/server",
		SeekerBinary: "./contrib/client-seeker/client-seeker",
		ServerAddr:   "127.0.0.1:6000",

		ServerPromUDS:    "/tmp/srt_server_perf.sock",
		SeekerPromUDS:    *common.SeekerMetricsUDS,
		SeekerControlUDS: *common.SeekerControlUDS,

		ProfileDir: *common.TestProfileDir,

		Verbose:        *common.TestVerbose,
		JSONOutput:     *common.TestJSONOutput,
		OutputFile:     *common.TestOutputFile,
		StatusInterval: *common.TestStatusInterval,
	}

	// Sync timing model with stability config
	cfg.Timing.WarmUpDuration = cfg.Stability.WarmUpDuration
	cfg.Timing.StabilityWindow = cfg.Stability.StabilityWindow
	cfg.Timing.SampleInterval = cfg.Stability.SampleInterval
	cfg.Timing.Precision = cfg.Search.Precision
	cfg.Timing.SearchTimeout = cfg.Search.Timeout
	cfg.Timing.WatchdogTimeout = *common.SeekerWatchdogTimeout
	cfg.Timing.HeartbeatInterval = *common.SeekerHeartbeatInterval
	cfg.Timing.computeDerived()

	return cfg
}

// FormatBitrate formats a bitrate as a human-readable string.
func FormatBitrate(bps int64) string {
	switch {
	case bps >= 1_000_000_000:
		return fmt.Sprintf("%.2f Gb/s", float64(bps)/1_000_000_000)
	case bps >= 1_000_000:
		return fmt.Sprintf("%.2f Mb/s", float64(bps)/1_000_000)
	case bps >= 1_000:
		return fmt.Sprintf("%.2f Kb/s", float64(bps)/1_000)
	default:
		return fmt.Sprintf("%d b/s", bps)
	}
}

// ParseBitrate parses a human-readable bitrate string.
// Supports suffixes: K, M, G (case-insensitive, decimal: 1000 multiplier)
//
// Examples:
//   - "100M" -> 100_000_000
//   - "1.5G" -> 1_500_000_000
//   - "500K" -> 500_000
//   - "1000000" -> 1_000_000
func ParseBitrate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty bitrate string")
	}

	// Check for suffix
	multiplier := int64(1)
	suffix := strings.ToUpper(s[len(s)-1:])

	switch suffix {
	case "K":
		multiplier = 1_000
		s = s[:len(s)-1]
	case "M":
		multiplier = 1_000_000
		s = s[:len(s)-1]
	case "G":
		multiplier = 1_000_000_000
		s = s[:len(s)-1]
	}

	// Parse the numeric part
	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid bitrate number: %w", err)
	}

	if value <= 0 {
		return 0, fmt.Errorf("bitrate must be positive, got %f", value)
	}

	return int64(value * float64(multiplier)), nil
}

// ParseBytes parses a human-readable byte size string.
// Supports suffixes: K, M, G (case-insensitive, binary: 1024 multiplier)
//
// Examples:
//   - "64K" -> 65536
//   - "1M" -> 1048576
//   - "1G" -> 1073741824
func ParseBytes(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty byte size string")
	}

	// Check for suffix
	multiplier := uint64(1)
	suffix := strings.ToUpper(s[len(s)-1:])

	switch suffix {
	case "K":
		multiplier = 1024
		s = s[:len(s)-1]
	case "M":
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case "G":
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}

	// Parse the numeric part
	value, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size number: %w", err)
	}

	if value == 0 {
		return 0, fmt.Errorf("byte size must be positive")
	}

	result := value * multiplier
	if result > uint64(^uint32(0)) {
		return 0, fmt.Errorf("byte size too large for uint32: %d", result)
	}

	return uint32(result), nil
}

// ParseDuration parses a duration string using Go's time.ParseDuration.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}
	return time.ParseDuration(s)
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	timing := DefaultTimingModel()
	return &Config{
		Search: SearchConfig{
			InitialBitrate:  200_000_000,  // 200 Mb/s
			MinBitrate:      50_000_000,   // 50 Mb/s
			MaxBitrate:      600_000_000,  // 600 Mb/s
			StepSize:        10_000_000,   // 10 Mb/s
			DecreasePercent: 0.25,         // 25%
			Precision:       5_000_000,    // 5 Mb/s
			Timeout:         10 * time.Minute,
		},
		Stability: StabilityConfig{
			WarmUpDuration:  timing.WarmUpDuration,
			StabilityWindow: timing.StabilityWindow,
			SampleInterval:  timing.SampleInterval,
			MaxGapRate:      0.01,
			MaxNAKRate:      0.02,
			MaxRTTMs:        100,
			MinThroughput:   0.95,
			CriticalGapRate: 0.05,
			CriticalNAKRate: 0.10,
		},
		Timing: timing,
		SRT: SRTConfig{
			FC:        102400,
			RecvRings: 1,
		},
		Verbose:    false,
		JSONOutput: false,
	}
}

// DefaultStabilityConfig returns a StabilityConfig with default values.
func DefaultStabilityConfig() StabilityConfig {
	timing := DefaultTimingModel()
	return StabilityConfig{
		WarmUpDuration:  timing.WarmUpDuration,
		StabilityWindow: timing.StabilityWindow,
		SampleInterval:  timing.SampleInterval,
		MaxGapRate:      0.01,
		MaxNAKRate:      0.02,
		MaxRTTMs:        100,
		MinThroughput:   0.95,
		CriticalGapRate: 0.05,
		CriticalNAKRate: 0.10,
	}
}

// DefaultSearchConfig returns a SearchConfig with default values.
func DefaultSearchConfig() SearchConfig {
	return SearchConfig{
		InitialBitrate:  200_000_000, // 200 Mb/s
		MinBitrate:      50_000_000,  // 50 Mb/s
		MaxBitrate:      600_000_000, // 600 Mb/s
		StepSize:        10_000_000,  // 10 Mb/s
		DecreasePercent: 0.25,        // 25%
		Precision:       5_000_000,   // 5 Mb/s
		Timeout:         10 * time.Minute,
	}
}

// ParseArgs parses configuration from a map of string key-value pairs.
// This is useful for environment variable or command-line parsing.
func ParseArgs(args map[string]string) (*Config, error) {
	cfg := DefaultConfig()

	// Parse search parameters
	if v, ok := args["INITIAL"]; ok {
		bitrate, err := ParseBitrate(v)
		if err != nil {
			return nil, fmt.Errorf("invalid INITIAL: %w", err)
		}
		cfg.Search.InitialBitrate = bitrate
	}

	if v, ok := args["MAX"]; ok {
		bitrate, err := ParseBitrate(v)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX: %w", err)
		}
		cfg.Search.MaxBitrate = bitrate
	}

	if v, ok := args["STEP"]; ok {
		bitrate, err := ParseBitrate(v)
		if err != nil {
			return nil, fmt.Errorf("invalid STEP: %w", err)
		}
		cfg.Search.StepSize = bitrate
	}

	if v, ok := args["PRECISION"]; ok {
		bitrate, err := ParseBitrate(v)
		if err != nil {
			return nil, fmt.Errorf("invalid PRECISION: %w", err)
		}
		cfg.Search.Precision = bitrate
		cfg.Timing.Precision = bitrate
	}

	// Parse SRT parameters
	if v, ok := args["FC"]; ok {
		fc, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid FC: %w", err)
		}
		cfg.SRT.FC = uint32(fc)
	}

	if v, ok := args["RECV_RINGS"]; ok {
		rings, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid RECV_RINGS: %w", err)
		}
		cfg.SRT.RecvRings = rings
	}

	// Parse timing parameters
	if v, ok := args["WARMUP"]; ok {
		d, err := ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid WARMUP: %w", err)
		}
		cfg.Stability.WarmUpDuration = d
		cfg.Timing.WarmUpDuration = d
	}

	if v, ok := args["STABILITY"]; ok {
		d, err := ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid STABILITY: %w", err)
		}
		cfg.Stability.StabilityWindow = d
		cfg.Timing.StabilityWindow = d
	}

	// Parse flags
	if v, ok := args["VERBOSE"]; ok {
		cfg.Verbose = strings.ToLower(v) == "true" || v == "1"
	}

	if v, ok := args["JSON"]; ok {
		cfg.JSONOutput = strings.ToLower(v) == "true" || v == "1"
	}

	// Recompute derived timing values
	cfg.Timing.MinProbeDuration = cfg.Timing.WarmUpDuration + cfg.Timing.StabilityWindow
	cfg.Timing.computeDerived()

	return cfg, nil
}
