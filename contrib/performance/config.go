package main

import (
	"fmt"
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

// Config combines all configuration for a performance test.
// SRT configuration is obtained from common.FlagSet and passed to subprocesses
// via common.BuildFlagArgs().
type Config struct {
	Search    SearchConfig
	Stability StabilityConfig
	Timing    TimingModel

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
