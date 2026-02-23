package main

import (
	"context"
	"time"
)

// Seeker controls the data generator (client-seeker process).
// This interface abstracts the control socket communication.
type Seeker interface {
	// SetBitrate changes the target bitrate immediately.
	SetBitrate(ctx context.Context, bps int64) error

	// Status returns the current seeker status.
	Status(ctx context.Context) (SeekerStatus, error)

	// Heartbeat sends a keep-alive to reset the watchdog.
	Heartbeat(ctx context.Context) error

	// Stop gracefully stops the seeker.
	Stop(ctx context.Context) error

	// IsAlive returns true if the connection is alive.
	IsAlive() bool
}

// MetricsSource provides stability metrics from Prometheus scraping.
type MetricsSource interface {
	// Sample collects current metrics from all sources.
	Sample(ctx context.Context) (StabilityMetrics, error)

	// SampleSeeker collects metrics from the seeker only.
	SampleSeeker(ctx context.Context) (StabilityMetrics, error)

	// SampleServer collects metrics from the server only.
	SampleServer(ctx context.Context) (StabilityMetrics, error)
}

// Gate provides stability verdicts (the inner loop oracle).
// The Gate is stateless - it evaluates a single probe and returns a verdict.
type Gate interface {
	// Probe evaluates stability at the given bitrate.
	// probeStart is when the probe officially started (after ramp completed).
	// The Gate handles warm-up and stability window internally.
	Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult

	// WithConfig returns a Gate with modified configuration.
	// Useful for extended proof phase with longer windows.
	WithConfig(config StabilityConfig) Gate
}

// Profiler captures diagnostic data on failure.
type Profiler interface {
	// CaptureAtFailure captures profiles when a critical threshold is hit.
	CaptureAtFailure(bitrate int64, metrics StabilityMetrics) *DiagnosticCapture

	// SetEnabled enables or disables profiling.
	SetEnabled(enabled bool)

	// IsEnabled returns true if profiling is enabled.
	IsEnabled() bool
}

// ProcessController manages external processes (server, seeker).
type ProcessController interface {
	// StartServer starts the SRT server process.
	StartServer(ctx context.Context) error

	// StartSeeker starts the client-seeker process.
	StartSeeker(ctx context.Context, initialBitrate int64) error

	// WaitReady waits for all processes to be ready.
	WaitReady(ctx context.Context) error

	// Stop stops all processes.
	Stop()

	// ServerMetricsPath returns the path to the server's Prometheus socket.
	ServerMetricsPath() string

	// SeekerMetricsPath returns the path to the seeker's Prometheus socket.
	SeekerMetricsPath() string

	// SeekerControlPath returns the path to the seeker's control socket.
	SeekerControlPath() string
}

// Reporter outputs progress and results.
type Reporter interface {
	// ProbeStarted is called when a probe begins.
	ProbeStarted(number int, bitrate int64)

	// ProbeCompleted is called when a probe finishes.
	ProbeCompleted(number int, result ProbeResult)

	// BoundsUpdated is called when search bounds change.
	BoundsUpdated(low, high int64)

	// SearchCompleted is called when the search finishes.
	SearchCompleted(result SearchResult)

	// Error reports an error.
	Error(err error)

	// Verbose outputs verbose information (if enabled).
	Verbose(format string, args ...interface{})
}

// ReadinessCriteria defines what "ready" means for the system.
type ReadinessCriteria struct {
	ServerRunning       bool
	SeekerRunning       bool
	ServerMetricsReady  bool
	SeekerMetricsReady  bool
	SeekerControlReady  bool
	ConnectionEstablished bool
}

// String returns a human-readable summary of readiness.
func (rc ReadinessCriteria) String() string {
	return "ReadinessCriteria{" +
		"ServerRunning:" + boolStr(rc.ServerRunning) +
		", SeekerRunning:" + boolStr(rc.SeekerRunning) +
		", ServerMetrics:" + boolStr(rc.ServerMetricsReady) +
		", SeekerMetrics:" + boolStr(rc.SeekerMetricsReady) +
		", SeekerControl:" + boolStr(rc.SeekerControlReady) +
		", Connection:" + boolStr(rc.ConnectionEstablished) +
		"}"
}

// AllReady returns true if all criteria are met.
func (rc ReadinessCriteria) AllReady() bool {
	return rc.ServerRunning &&
		rc.SeekerRunning &&
		rc.ServerMetricsReady &&
		rc.SeekerMetricsReady &&
		rc.SeekerControlReady &&
		rc.ConnectionEstablished
}

// FirstFailure returns the first criterion that is not met.
func (rc ReadinessCriteria) FirstFailure() string {
	if !rc.ServerRunning {
		return "server not running"
	}
	if !rc.SeekerRunning {
		return "seeker not running"
	}
	if !rc.ServerMetricsReady {
		return "server metrics socket not responding"
	}
	if !rc.SeekerMetricsReady {
		return "seeker metrics socket not responding"
	}
	if !rc.SeekerControlReady {
		return "seeker control socket not responding"
	}
	if !rc.ConnectionEstablished {
		return "SRT connection not established"
	}
	return ""
}

func boolStr(b bool) string {
	if b {
		return "✓"
	}
	return "✗"
}
