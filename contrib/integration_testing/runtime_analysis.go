package main

import (
	"fmt"
	"time"

	"github.com/montanaflynn/stats"
)

// RuntimeStabilityThresholds defines acceptable growth rates for runtime metrics
type RuntimeStabilityThresholds struct {
	// Memory: max bytes/hour growth (after warmup)
	MaxHeapGrowthBytesPerHour int64 // Default: 1 MB/hour

	// Goroutines: max growth rate
	MaxGoroutineGrowthPerHour float64 // Default: 0 (should not grow)

	// GC: max increase in GC pause time per hour
	MaxGCPauseGrowthMsPerHour float64 // Default: 100ms/hour

	// CPU: max variance in CPU usage (coefficient of variation)
	MaxCPUVariancePercent float64 // Default: 20%
}

// DefaultRuntimeThresholds provides sensible defaults for stability analysis
var DefaultRuntimeThresholds = RuntimeStabilityThresholds{
	MaxHeapGrowthBytesPerHour: 1 * 1024 * 1024, // 1 MB/hour
	MaxGoroutineGrowthPerHour: 1.0,             // Allow 1/hour (some variance is normal)
	MaxGCPauseGrowthMsPerHour: 100.0,           // 100ms/hour
	MaxCPUVariancePercent:     30.0,            // 30% variance allowed
}

// RuntimeViolation represents a stability threshold violation
type RuntimeViolation struct {
	Metric     string
	GrowthRate float64
	Threshold  float64
	Message    string
}

// RuntimeWarning represents a non-critical stability concern
type RuntimeWarning struct {
	Metric  string
	Message string
}

// RuntimeSummary provides a snapshot of runtime health
type RuntimeSummary struct {
	// Memory
	InitialHeapMB       float64
	FinalHeapMB         float64
	PeakHeapMB          float64
	HeapGrowthMBPerHour float64
	HeapStable          bool

	// Goroutines
	InitialGoroutines   int
	FinalGoroutines     int
	PeakGoroutines      int
	GoroutineGrowthRate float64
	GoroutinesStable    bool

	// GC
	TotalGCPauseMs    float64
	GCPauseGrowthRate float64
	GCStable          bool

	// CPU
	AvgCPUPercent      float64
	CPUVariancePercent float64
	CPUStable          bool
}

// RuntimeStabilityResult holds the result of runtime stability analysis
type RuntimeStabilityResult struct {
	Passed     bool
	Component  string
	Violations []RuntimeViolation
	Warnings   []RuntimeWarning
	Summary    RuntimeSummary
}

// TrendAnalysis results from linear regression
type TrendAnalysis struct {
	Slope     float64 // Growth rate per second (positive = growing)
	Intercept float64 // Initial value
	RSquared  float64 // Goodness of fit (0-1, higher = better fit)
}

// WarmupDuration determines how much initial data to skip based on test duration
func WarmupDuration(testDuration time.Duration) time.Duration {
	switch {
	case testDuration >= 12*time.Hour:
		return 15 * time.Minute // 12h+ test: skip first 15 min
	case testDuration >= 1*time.Hour:
		return 10 * time.Minute // 1h+ test: skip first 10 min
	case testDuration >= 30*time.Minute:
		return 5 * time.Minute // 30m+ test: skip first 5 min
	default:
		return 0 // Shorter tests: no warmup skip
	}
}

// AnalyzeTrend performs linear regression on time series data
func AnalyzeTrend(timestamps []time.Time, values []float64) (TrendAnalysis, error) {
	if len(timestamps) < 2 {
		return TrendAnalysis{}, fmt.Errorf("need at least 2 data points")
	}

	if len(timestamps) != len(values) {
		return TrendAnalysis{}, fmt.Errorf("timestamps and values must have same length")
	}

	// Convert to stats.Series (X = seconds from start, Y = metric value)
	startTime := timestamps[0]
	series := make(stats.Series, len(timestamps))
	for i, t := range timestamps {
		series[i] = stats.Coordinate{
			X: t.Sub(startTime).Seconds(),
			Y: values[i],
		}
	}

	// Perform linear regression
	regressionLine, err := stats.LinearRegression(series)
	if err != nil {
		return TrendAnalysis{}, fmt.Errorf("linear regression failed: %w", err)
	}

	// Extract slope from regression line
	if len(regressionLine) < 2 {
		return TrendAnalysis{}, fmt.Errorf("regression produced insufficient points")
	}

	// Slope = (y2-y1)/(x2-x1)
	dx := regressionLine[len(regressionLine)-1].X - regressionLine[0].X
	dy := regressionLine[len(regressionLine)-1].Y - regressionLine[0].Y
	if dx == 0 {
		return TrendAnalysis{Slope: 0, Intercept: regressionLine[0].Y, RSquared: 1}, nil
	}
	slope := dy / dx
	intercept := regressionLine[0].Y

	// Calculate R-squared (coefficient of determination)
	yData := stats.Float64Data(values)
	mean, meanErr := stats.Mean(yData)
	if meanErr != nil {
		return TrendAnalysis{}, fmt.Errorf("failed to calculate mean: %w", meanErr)
	}

	ssTotal := 0.0
	ssResidual := 0.0
	for i, v := range values {
		ssTotal += (v - mean) * (v - mean)
		predicted := slope*series[i].X + intercept
		ssResidual += (v - predicted) * (v - predicted)
	}

	rSquared := 0.0
	if ssTotal > 0 {
		rSquared = 1 - (ssResidual / ssTotal)
	}

	return TrendAnalysis{
		Slope:     slope,
		Intercept: intercept,
		RSquared:  rSquared,
	}, nil
}

// ComputeVarianceStats calculates mean and coefficient of variation
func ComputeVarianceStats(values []float64) (mean, cv float64, err error) {
	if len(values) == 0 {
		return 0, 0, fmt.Errorf("no values provided")
	}

	data := stats.Float64Data(values)

	mean, err = stats.Mean(data)
	if err != nil {
		return 0, 0, err
	}

	stdDev, err := stats.StandardDeviation(data)
	if err != nil {
		return 0, 0, err
	}

	// Coefficient of variation = stddev / mean * 100 (as percentage)
	if mean != 0 {
		cv = (stdDev / mean) * 100
	}

	return mean, cv, nil
}

// GetMinMaxStats returns min and max values
func GetMinMaxStats(values []float64) (minVal, maxVal float64, err error) {
	if len(values) == 0 {
		return 0, 0, fmt.Errorf("no values provided")
	}

	data := stats.Float64Data(values)

	minVal, err = stats.Min(data)
	if err != nil {
		return 0, 0, err
	}

	maxVal, err = stats.Max(data)
	if err != nil {
		return 0, 0, err
	}

	return minVal, maxVal, nil
}

// filterAfterWarmup returns snapshots after the warmup period
func filterAfterWarmup(snapshots []*MetricsSnapshot, warmup time.Duration) []*MetricsSnapshot {
	if len(snapshots) == 0 || warmup == 0 {
		return snapshots
	}

	startTime := snapshots[0].Timestamp
	cutoff := startTime.Add(warmup)

	var filtered []*MetricsSnapshot
	for _, s := range snapshots {
		if s.Timestamp.After(cutoff) && s.Error == nil {
			filtered = append(filtered, s)
		}
	}

	return filtered
}

// extractMetricValues extracts a specific metric from snapshots
func extractRuntimeMetricValues(snapshots []*MetricsSnapshot, metric string) ([]time.Time, []float64) {
	var timestamps []time.Time
	var values []float64

	for _, s := range snapshots {
		if s.Error == nil {
			if v, ok := s.Metrics[metric]; ok {
				timestamps = append(timestamps, s.Timestamp)
				values = append(values, v)
			}
		}
	}

	return timestamps, values
}

// ValidateRuntimeStability performs runtime stability analysis on a time series
// FAIL-SAFE: For applicable tests (>= 30 min), defaults to failed until all metrics confirm stability
func ValidateRuntimeStability(ts MetricsTimeSeries, testDuration time.Duration,
	thresholds RuntimeStabilityThresholds) RuntimeStabilityResult {

	// FAIL-SAFE: Start with failed for long tests
	result := RuntimeStabilityResult{Passed: false, Component: ts.Component}

	// Skip if test is too short for stability analysis
	// For short tests, we pass because analysis is not applicable (not because we confirmed stability)
	if testDuration < 30*time.Minute {
		result.Passed = true // Not applicable = passes
		result.Summary.HeapStable = true
		result.Summary.GoroutinesStable = true
		result.Summary.GCStable = true
		result.Summary.CPUStable = true
		result.Warnings = append(result.Warnings, RuntimeWarning{
			Metric:  "duration",
			Message: fmt.Sprintf("Test duration %.0f min is too short for stability analysis (need >= 30 min)", testDuration.Minutes()),
		})
		return result
	}

	// Extract data points after warmup
	warmup := WarmupDuration(testDuration)
	stableSnapshots := filterAfterWarmup(ts.Snapshots, warmup)

	if len(stableSnapshots) < 3 {
		// Can't analyze = fail (we don't know if it's stable)
		result.Warnings = append(result.Warnings, RuntimeWarning{
			Metric:  "samples",
			Message: "Insufficient samples after warmup for stability analysis - cannot confirm stability",
		})
		return result
	}

	// Track stability confirmations - all must be true to pass
	heapAnalyzed := false
	goroutinesAnalyzed := false

	// Analyze heap memory trend
	timestamps, heapValues := extractRuntimeMetricValues(stableSnapshots, "go_memstats_heap_alloc_bytes")
	if len(timestamps) >= 2 {
		heapTrend, err := AnalyzeTrend(timestamps, heapValues)
		if err == nil {
			heapAnalyzed = true
			result.Summary.HeapGrowthMBPerHour = heapTrend.Slope * 3600 / (1024 * 1024)
			result.Summary.HeapStable = heapTrend.Slope*3600 <= float64(thresholds.MaxHeapGrowthBytesPerHour)

			if !result.Summary.HeapStable {
				result.Violations = append(result.Violations, RuntimeViolation{
					Metric:     "HeapMemory",
					GrowthRate: result.Summary.HeapGrowthMBPerHour,
					Threshold:  float64(thresholds.MaxHeapGrowthBytesPerHour) / (1024 * 1024),
					Message: fmt.Sprintf("Heap growing at %.2f MB/hour (max: %.2f MB/hour) - possible memory leak",
						result.Summary.HeapGrowthMBPerHour,
						float64(thresholds.MaxHeapGrowthBytesPerHour)/(1024*1024)),
				})
			}
		} else {
			result.Warnings = append(result.Warnings, RuntimeWarning{
				Metric:  "HeapMemory",
				Message: fmt.Sprintf("Could not analyze heap trend: %v", err),
			})
		}
	}

	// Analyze goroutine trend
	timestamps, goroutineValues := extractRuntimeMetricValues(stableSnapshots, "go_goroutines")
	if len(timestamps) >= 2 {
		goroutineTrend, err := AnalyzeTrend(timestamps, goroutineValues)
		if err == nil {
			goroutinesAnalyzed = true
			result.Summary.GoroutineGrowthRate = goroutineTrend.Slope * 3600
			result.Summary.GoroutinesStable = goroutineTrend.Slope*3600 <= thresholds.MaxGoroutineGrowthPerHour

			if !result.Summary.GoroutinesStable {
				result.Violations = append(result.Violations, RuntimeViolation{
					Metric:     "Goroutines",
					GrowthRate: result.Summary.GoroutineGrowthRate,
					Threshold:  thresholds.MaxGoroutineGrowthPerHour,
					Message: fmt.Sprintf("Goroutines growing at %.1f/hour - possible goroutine leak",
						result.Summary.GoroutineGrowthRate),
				})
			}
		}
	}

	// Analyze GC pause time trend (optional - adds to summary but doesn't fail the test)
	timestamps, gcValues := extractRuntimeMetricValues(stableSnapshots, "go_gc_duration_seconds_sum")
	if len(timestamps) >= 2 {
		gcTrend, err := AnalyzeTrend(timestamps, gcValues)
		if err == nil {
			result.Summary.GCPauseGrowthRate = gcTrend.Slope * 3600 * 1000 // Convert to ms/hour
			result.Summary.GCStable = result.Summary.GCPauseGrowthRate <= thresholds.MaxGCPauseGrowthMsPerHour

			// GC pause growth is a warning, not a failure
			if !result.Summary.GCStable {
				result.Warnings = append(result.Warnings, RuntimeWarning{
					Metric: "GCPause",
					Message: fmt.Sprintf("GC pause time growing at %.1f ms/hour (threshold: %.1f ms/hour)",
						result.Summary.GCPauseGrowthRate, thresholds.MaxGCPauseGrowthMsPerHour),
				})
			}
		}
	}

	// Analyze CPU variance (optional - adds to summary but doesn't fail the test)
	_, cpuValues := extractRuntimeMetricValues(stableSnapshots, "process_cpu_seconds_total")
	if len(cpuValues) >= 2 {
		// For CPU, we look at the rate of change (derivative) stability, not the raw values
		// Convert cumulative CPU seconds to rate differences
		var cpuRates []float64
		for i := 1; i < len(cpuValues); i++ {
			rate := cpuValues[i] - cpuValues[i-1]
			if rate >= 0 {
				cpuRates = append(cpuRates, rate)
			}
		}

		if len(cpuRates) >= 2 {
			avgCPU, cpuCV, err := ComputeVarianceStats(cpuRates)
			if err == nil {
				result.Summary.AvgCPUPercent = avgCPU * 100 // Convert to percentage-like representation
				result.Summary.CPUVariancePercent = cpuCV
				result.Summary.CPUStable = cpuCV <= thresholds.MaxCPUVariancePercent

				// CPU variance is a warning, not a failure
				if !result.Summary.CPUStable {
					result.Warnings = append(result.Warnings, RuntimeWarning{
						Metric: "CPUVariance",
						Message: fmt.Sprintf("CPU usage variance %.1f%% (threshold: %.1f%%)",
							cpuCV, thresholds.MaxCPUVariancePercent),
					})
				}
			}
		}
	}

	// Populate summary with initial/final/peak values
	populateRuntimeSummary(&result.Summary, ts.Snapshots)

	// EXPLICIT PASS: Only pass when critical metrics (heap, goroutines) are stable
	// We must have analyzed both AND found no violations
	if heapAnalyzed && goroutinesAnalyzed &&
		result.Summary.HeapStable && result.Summary.GoroutinesStable &&
		len(result.Violations) == 0 {
		result.Passed = true
	}

	return result
}

// populateRuntimeSummary fills in initial/final/peak values
func populateRuntimeSummary(summary *RuntimeSummary, snapshots []*MetricsSnapshot) {
	if len(snapshots) == 0 {
		return
	}

	// Find successful snapshots
	var heapValues, goroutineValues []float64
	for _, s := range snapshots {
		if s.Error == nil {
			if v, ok := s.Metrics["go_memstats_heap_alloc_bytes"]; ok {
				heapValues = append(heapValues, v)
			}
			if v, ok := s.Metrics["go_goroutines"]; ok {
				goroutineValues = append(goroutineValues, v)
			}
		}
	}

	if len(heapValues) > 0 {
		summary.InitialHeapMB = heapValues[0] / (1024 * 1024)
		summary.FinalHeapMB = heapValues[len(heapValues)-1] / (1024 * 1024)
		_, peakHeap, err := GetMinMaxStats(heapValues)
		if err == nil {
			summary.PeakHeapMB = peakHeap / (1024 * 1024)
		}
	}

	if len(goroutineValues) > 0 {
		summary.InitialGoroutines = int(goroutineValues[0])
		summary.FinalGoroutines = int(goroutineValues[len(goroutineValues)-1])
		_, peakGoroutines, err := GetMinMaxStats(goroutineValues)
		if err == nil {
			summary.PeakGoroutines = int(peakGoroutines)
		}
	}
}

// PrintRuntimeStabilityResult outputs the runtime stability result to console
func PrintRuntimeStabilityResult(result RuntimeStabilityResult) {
	fmt.Printf("\n=== Runtime Stability Analysis: %s ===\n", result.Component)

	// Memory Analysis
	fmt.Println("\nMemory Analysis:")
	fmt.Printf("  Initial Heap:     %.2f MB\n", result.Summary.InitialHeapMB)
	fmt.Printf("  Final Heap:       %.2f MB\n", result.Summary.FinalHeapMB)
	fmt.Printf("  Peak Heap:        %.2f MB\n", result.Summary.PeakHeapMB)
	fmt.Printf("  Growth Rate:      %.2f MB/hour\n", result.Summary.HeapGrowthMBPerHour)
	if result.Summary.HeapStable {
		fmt.Println("  Status:           ✓ STABLE")
	} else {
		fmt.Println("  Status:           ✗ UNSTABLE")
	}

	// Goroutine Analysis
	fmt.Println("\nGoroutine Analysis:")
	fmt.Printf("  Initial:          %d\n", result.Summary.InitialGoroutines)
	fmt.Printf("  Final:            %d\n", result.Summary.FinalGoroutines)
	fmt.Printf("  Peak:             %d\n", result.Summary.PeakGoroutines)
	fmt.Printf("  Growth Rate:      %.1f/hour\n", result.Summary.GoroutineGrowthRate)
	if result.Summary.GoroutinesStable {
		fmt.Println("  Status:           ✓ STABLE")
	} else {
		fmt.Println("  Status:           ✗ UNSTABLE")
	}

	// GC Analysis
	fmt.Println("\nGC Analysis:")
	fmt.Printf("  Pause Growth:     %.1f ms/hour\n", result.Summary.GCPauseGrowthRate)
	if result.Summary.GCStable {
		fmt.Println("  Status:           ✓ STABLE")
	} else {
		fmt.Println("  Status:           ⚠ WARNING")
	}

	// CPU Analysis
	fmt.Println("\nCPU Analysis:")
	fmt.Printf("  Variance:         %.1f%%\n", result.Summary.CPUVariancePercent)
	if result.Summary.CPUStable {
		fmt.Println("  Status:           ✓ STABLE")
	} else {
		fmt.Println("  Status:           ⚠ WARNING")
	}

	// Violations
	if len(result.Violations) > 0 {
		fmt.Println("\nViolations:")
		for _, v := range result.Violations {
			fmt.Printf("  ✗ %s\n", v.Message)
		}
	}

	// Warnings
	if len(result.Warnings) > 0 {
		fmt.Println("\nWarnings:")
		for _, w := range result.Warnings {
			fmt.Printf("  ⚠ %s: %s\n", w.Metric, w.Message)
		}
	}

	// Final Result
	if result.Passed {
		fmt.Printf("\nRUNTIME STABILITY: ✓ PASSED\n")
	} else {
		fmt.Printf("\nRUNTIME STABILITY: ✗ FAILED (%d violations)\n", len(result.Violations))
	}
}

// AnalyzeRuntimeStabilityForAllComponents analyzes all components and returns combined result
func AnalyzeRuntimeStabilityForAllComponents(ts *TestMetricsTimeSeries, testDuration time.Duration) []RuntimeStabilityResult {
	results := make([]RuntimeStabilityResult, 0, 3)

	// Analyze each component
	results = append(results, ValidateRuntimeStability(ts.Server, testDuration, DefaultRuntimeThresholds))
	results = append(results, ValidateRuntimeStability(ts.ClientGenerator, testDuration, DefaultRuntimeThresholds))
	results = append(results, ValidateRuntimeStability(ts.Client, testDuration, DefaultRuntimeThresholds))

	return results
}
