package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"
)

// DiagnosticProfiler captures profiles on failure.
type DiagnosticProfiler struct {
	outputDir     string
	captureOnCrit bool
	profiles      []string // "cpu", "heap", "goroutine", "allocs", "block", "mutex"
	enabled       bool
}

// NewDiagnosticProfiler creates a new profiler.
func NewDiagnosticProfiler(outputDir string, profiles []string) *DiagnosticProfiler {
	if profiles == nil {
		profiles = []string{"heap", "goroutine"} // Default profiles (fast to capture)
	}

	return &DiagnosticProfiler{
		outputDir:     outputDir,
		profiles:      profiles,
		captureOnCrit: true,
		enabled:       true,
	}
}

// SetEnabled enables or disables profiling.
func (dp *DiagnosticProfiler) SetEnabled(enabled bool) {
	dp.enabled = enabled
}

// IsEnabled returns true if profiling is enabled.
func (dp *DiagnosticProfiler) IsEnabled() bool {
	return dp.enabled
}

// CaptureAtFailure implements Profiler interface.
func (dp *DiagnosticProfiler) CaptureAtFailure(bitrate int64, metrics StabilityMetrics) *DiagnosticCapture {
	if !dp.enabled || !dp.captureOnCrit {
		return nil
	}

	// Ensure output directory exists
	if err := os.MkdirAll(dp.outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "profiler: failed to create output dir: %v\n", err)
		return nil
	}

	capture := &DiagnosticCapture{
		CapturedAt:     time.Now(),
		TriggerBitrate: bitrate,
		TriggerReason:  "critical_threshold",
	}

	// Generate timestamp for filenames
	ts := time.Now().Format("20060102_150405")

	// Capture each requested profile
	for _, profileType := range dp.profiles {
		path := filepath.Join(dp.outputDir, fmt.Sprintf("%s_%s_%dMbps.pprof", profileType, ts, bitrate/1_000_000))

		if err := dp.captureProfile(profileType, path); err != nil {
			fmt.Fprintf(os.Stderr, "profiler: failed to capture %s: %v\n", profileType, err)
			continue
		}

		// Store path in capture
		switch profileType {
		case "cpu":
			capture.CPUProfilePath = path
		case "heap":
			capture.HeapProfilePath = path
		case "goroutine":
			capture.GoroutineProfilePath = path
		}
	}

	// Calculate TE metric
	if metrics.TargetBitrate > 0 {
		// TE is already calculated in metrics, but recalculate for safety
		capture.TriggerBitrate = bitrate
	}

	return capture
}

// captureProfile captures a single profile type.
func (dp *DiagnosticProfiler) captureProfile(profileType, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	switch profileType {
	case "cpu":
		// CPU profile needs to run for a duration - skip for immediate capture
		// Instead, we'll capture a heap snapshot which is instant
		return fmt.Errorf("cpu profile requires duration, use heap instead for immediate capture")

	case "heap":
		runtime.GC() // Get up-to-date statistics
		return pprof.WriteHeapProfile(f)

	case "goroutine":
		p := pprof.Lookup("goroutine")
		if p == nil {
			return fmt.Errorf("goroutine profile not found")
		}
		return p.WriteTo(f, 0)

	case "allocs":
		p := pprof.Lookup("allocs")
		if p == nil {
			return fmt.Errorf("allocs profile not found")
		}
		return p.WriteTo(f, 0)

	case "block":
		p := pprof.Lookup("block")
		if p == nil {
			return fmt.Errorf("block profile not found")
		}
		return p.WriteTo(f, 0)

	case "mutex":
		p := pprof.Lookup("mutex")
		if p == nil {
			return fmt.Errorf("mutex profile not found")
		}
		return p.WriteTo(f, 0)

	case "threadcreate":
		p := pprof.Lookup("threadcreate")
		if p == nil {
			return fmt.Errorf("threadcreate profile not found")
		}
		return p.WriteTo(f, 0)

	default:
		return fmt.Errorf("unknown profile type: %s", profileType)
	}
}

// CaptureWithDuration captures a CPU profile for the specified duration.
func (dp *DiagnosticProfiler) CaptureWithDuration(duration time.Duration, bitrate int64) (*DiagnosticCapture, error) {
	if !dp.enabled {
		return nil, nil
	}

	// Ensure output directory exists
	if err := os.MkdirAll(dp.outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	capture := &DiagnosticCapture{
		CapturedAt:     time.Now(),
		TriggerBitrate: bitrate,
		TriggerReason:  "manual_capture",
	}

	ts := time.Now().Format("20060102_150405")

	// CPU profile
	cpuPath := filepath.Join(dp.outputDir, fmt.Sprintf("cpu_%s_%dMbps.pprof", ts, bitrate/1_000_000))
	cpuFile, err := os.Create(cpuPath)
	if err != nil {
		return nil, fmt.Errorf("create cpu profile: %w", err)
	}

	if err := pprof.StartCPUProfile(cpuFile); err != nil {
		cpuFile.Close()
		return nil, fmt.Errorf("start cpu profile: %w", err)
	}

	time.Sleep(duration)

	pprof.StopCPUProfile()
	cpuFile.Close()
	capture.CPUProfilePath = cpuPath

	// Also capture heap at the end
	heapPath := filepath.Join(dp.outputDir, fmt.Sprintf("heap_%s_%dMbps.pprof", ts, bitrate/1_000_000))
	if err := dp.captureProfile("heap", heapPath); err == nil {
		capture.HeapProfilePath = heapPath
	}

	// And goroutines
	goroutinePath := filepath.Join(dp.outputDir, fmt.Sprintf("goroutine_%s_%dMbps.pprof", ts, bitrate/1_000_000))
	if err := dp.captureProfile("goroutine", goroutinePath); err == nil {
		capture.GoroutineProfilePath = goroutinePath
	}

	return capture, nil
}

// GetProfilePaths returns all profile paths from a capture.
func (dc *DiagnosticCapture) GetProfilePaths() []string {
	var paths []string
	if dc.CPUProfilePath != "" {
		paths = append(paths, dc.CPUProfilePath)
	}
	if dc.HeapProfilePath != "" {
		paths = append(paths, dc.HeapProfilePath)
	}
	if dc.GoroutineProfilePath != "" {
		paths = append(paths, dc.GoroutineProfilePath)
	}
	return paths
}
