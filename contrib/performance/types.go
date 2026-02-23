package main

import (
	"time"
)

// TerminationReason indicates why a search ended.
type TerminationReason string

const (
	TerminationSuccess   TerminationReason = "success"    // Found and proved ceiling
	TerminationTimeout   TerminationReason = "timeout"    // Search timeout exceeded
	TerminationEOF       TerminationReason = "eof"        // Connection died
	TerminationWatchdog  TerminationReason = "watchdog"   // Watchdog triggered
	TerminationCritical  TerminationReason = "critical"   // Critical threshold exceeded
	TerminationCancelled TerminationReason = "cancelled"  // User cancelled
	TerminationInvariant TerminationReason = "invariant"  // Invariant violation
	TerminationError     TerminationReason = "error"      // Unexpected error
)

// SearchStatus indicates the outcome of a search.
type SearchStatus string

const (
	StatusSuccess SearchStatus = "success" // Found ceiling
	StatusFailed  SearchStatus = "failed"  // Could not find ceiling
	StatusAborted SearchStatus = "aborted" // Aborted early
)

// ProbeResult is the verdict from the StabilityGate.
type ProbeResult struct {
	Verdict   ProbeVerdict
	Metrics   StabilityMetrics
	Duration  time.Duration
	Samples   int
	Reason    string // Human-readable explanation
	Artifacts *DiagnosticCapture
}

// ProbeVerdict is the stability verdict.
type ProbeVerdict string

const (
	VerdictStable   ProbeVerdict = "stable"   // All metrics within bounds
	VerdictUnstable ProbeVerdict = "unstable" // Some metrics exceeded thresholds
	VerdictCritical ProbeVerdict = "critical" // Critical thresholds exceeded
	VerdictEOF      ProbeVerdict = "eof"      // Connection died during probe
	VerdictTimeout  ProbeVerdict = "timeout"  // Probe timed out
)

// StabilityMetrics contains metrics for stability evaluation.
type StabilityMetrics struct {
	// Core metrics
	GapRate       float64 // Gaps per second
	NAKRate       float64 // NAKs per second
	RTTMs         float64 // Round-trip time in milliseconds
	RTTVarianceMs float64 // RTT variance in milliseconds

	// Throughput
	TargetBitrate int64   // What we asked for
	ActualBitrate int64   // What we measured
	ThroughputTE  float64 // Throughput Efficiency = Actual/Target

	// Connection state
	ConnectionAlive bool
	PacketsSent     uint64
	BytesSent       uint64

	// Timestamp
	Timestamp time.Time

	// ═══════════════════════════════════════════════════════════════════
	// Bottleneck Detection Metrics (from client-seeker instrumentation)
	// See: client_seeker_instrumentation_design.md
	// ═══════════════════════════════════════════════════════════════════

	// Generator efficiency (key metric for bottleneck detection)
	GeneratorEfficiency float64 // ActualBps / TargetBps from generator

	// TokenBucket metrics (tool overhead)
	TokenBucketWaitSec  float64 // Total time waiting for tokens
	TokenBucketSpinSec  float64 // Time spent in spin-wait loops
	TokenBucketBlocked  int64   // Times consume had to wait
	TokenBucketMode     int     // 0=sleep, 1=hybrid, 2=spin

	// Publisher write metrics (library overhead)
	SRTWriteSec     float64 // Total time in Write() calls
	SRTWriteBlocked int64   // Times Write() blocked (> 1ms)
	SRTWriteErrors  int64   // Write errors

	// Bottleneck analysis result
	BottleneckType   string  // "NONE", "TOOL-LIMITED", "LIBRARY-LIMITED", "UNKNOWN"
	BottleneckReason string  // Human-readable explanation
}

// SeekerStatus is the status from the client-seeker.
type SeekerStatus struct {
	CurrentBitrate  int64
	TargetBitrate   int64
	PacketsSent     uint64
	BytesSent       uint64
	ConnectionAlive bool
	UptimeSeconds   float64
	WatchdogState   string
}

// ProbeRecord records a single probe for history.
type ProbeRecord struct {
	Number        int           `json:"number"`
	TargetBitrate int64         `json:"target_bitrate"`
	Stable        bool          `json:"stable"`
	Critical      bool          `json:"critical,omitempty"`
	Duration      time.Duration `json:"duration"`
	Metrics       *StabilityMetrics `json:"metrics,omitempty"`
}

// InvariantViolation records a violated invariant.
type InvariantViolation struct {
	Invariant   string `json:"invariant"`
	Description string `json:"description"`
	Low         int64  `json:"low,omitempty"`
	High        int64  `json:"high,omitempty"`
	ProbeCount  int    `json:"probe_count,omitempty"`
}

// DiagnosticCapture contains diagnostic data captured on failure.
type DiagnosticCapture struct {
	CPUProfilePath       string    `json:"cpu_profile_path,omitempty"`
	HeapProfilePath      string    `json:"heap_profile_path,omitempty"`
	GoroutineProfilePath string    `json:"goroutine_profile_path,omitempty"`
	CapturedAt           time.Time `json:"captured_at"`
	TriggerBitrate       int64     `json:"trigger_bitrate"`
	TriggerReason        string    `json:"trigger_reason"`
}

// CeilingProofData contains data proving the ceiling.
type CeilingProofData struct {
	StableRuns     int           `json:"stable_runs"`
	JitterTestPass bool          `json:"jitter_test_pass"`
	ProofDuration  time.Duration `json:"proof_duration"`
	FinalMetrics   StabilityMetrics `json:"final_metrics"`
}

// HypothesisModel contains thresholds for bottleneck hypothesis validation.
type HypothesisModel struct {
	// H1: FC Window Exhaustion
	H1NAKRateThreshold float64 `json:"h1_nak_rate_threshold"` // Default: 0.02

	// H2: EventLoop Starvation
	H2TEThreshold float64 `json:"h2_te_threshold"` // Default: 0.95

	// H3: Btree Iteration Lag
	H3GapRateThreshold float64 `json:"h3_gap_rate_threshold"` // Default: 0.01

	// H4: Ring Buffer Contention
	H4RetryThreshold float64 `json:"h4_retry_threshold"` // Default: 0.001

	// H5: GC/Memory Pressure
	H5RTTVarianceThreshold float64 `json:"h5_rtt_variance_threshold"` // Default: 20.0 (ms)
}

// DefaultHypothesisModel returns sensible defaults.
func DefaultHypothesisModel() HypothesisModel {
	return HypothesisModel{
		H1NAKRateThreshold:     0.02,
		H2TEThreshold:          0.95,
		H3GapRateThreshold:     0.01,
		H4RetryThreshold:       0.001,
		H5RTTVarianceThreshold: 20.0,
	}
}

// FailureArtifacts contains comprehensive diagnostic data on any failure.
// This struct is ALWAYS populated, even on success.
type FailureArtifacts struct {
	ConfigSnapshot     Config            `json:"config_snapshot"`
	TimingSnapshot     TimingModel       `json:"timing_snapshot"`
	HypothesisSnapshot HypothesisModel   `json:"hypothesis_thresholds"`
	TerminationReason  TerminationReason `json:"termination_reason"`

	// Probe history
	Probes      []ProbeRecord `json:"probes"`
	LastNProbes int           `json:"last_n_probes"`

	// Metrics (present if any samples collected)
	LastSampleWindow []StabilityMetrics `json:"last_sample_window,omitempty"`
	FinalMetrics     *StabilityMetrics  `json:"final_metrics,omitempty"`

	// Diagnostics (present if profiling enabled)
	ProfilePaths []string `json:"profile_paths,omitempty"`

	// Invariant violation (present if that's why we failed)
	Violation *InvariantViolation `json:"violation,omitempty"`
}

// SearchResult is the final result of a search.
type SearchResult struct {
	Status     SearchStatus     `json:"status"`
	Ceiling    int64            `json:"ceiling"`
	Proven     bool             `json:"proven"`
	ProofData  *CeilingProofData `json:"proof_data,omitempty"`
	Metrics    *StabilityMetrics `json:"metrics,omitempty"`

	// ALWAYS present, even on failure
	Artifacts FailureArtifacts `json:"artifacts"`

	// Human-readable failure reason (if failed)
	FailReason string `json:"fail_reason,omitempty"`
}

// HypothesisValidation contains validated hypotheses from the final report.
type HypothesisValidation struct {
	FCWindowExhaustion   bool   `json:"fc_window_exhaustion"`
	EventLoopStarvation  bool   `json:"event_loop_starvation"`
	BtreeIterationLag    bool   `json:"btree_iteration_lag"`
	RingBufferContention bool   `json:"ring_buffer_contention"`
	GCMemoryPressure     bool   `json:"gc_memory_pressure"`
	Summary              string `json:"summary"`
}
