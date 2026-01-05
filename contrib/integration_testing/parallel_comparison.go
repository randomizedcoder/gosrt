package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ANSI color codes for terminal output (matching contrib/common/colors.go)
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

// Threshold constants for validation
const (
	ThresholdOK      = 0.01 // <1% difference = OK
	ThresholdWarning = 0.03 // 1-3% difference = WARNING
	// >3% = ERROR

	ProcessUptimeTolerance = 10 * time.Second // Process uptime should be within 10s of test duration
	ConnectionAgeTolerance = 10 * time.Second // Connection age should be within 10s of expected

	// EffectivelyZeroThreshold treats values below this as zero.
	// 1e-9 seconds = 1 nanosecond - anything smaller is measurement noise.
	// This prevents misleading percentages like "0 vs 0 = +8000%".
	EffectivelyZeroThreshold = 1e-9
)

// calculateTolerance returns dynamic tolerance based on configured loss rate.
// For clean tests (0% loss), returns a tight 1% tolerance.
// For loss tests, returns loss rate + buffer for measurement variance.
func calculateTolerance(configuredLoss float64) float64 {
	if configuredLoss == 0 {
		return 0.01 // 1% for clean tests - very tight
	}
	// Base: configured loss + buffer for timing/measurement variance
	buffer := 0.02 // 2% base buffer
	if configuredLoss > 0.10 {
		buffer = 0.03 // 3% for high loss (>10%)
	}
	if configuredLoss > 0.15 {
		buffer = 0.05 // 5% for stress tests (>15%)
	}
	return configuredLoss + buffer
}

// StabilityResult holds the result of stability checks
type StabilityResult struct {
	ProcessName    string
	ExpectedUptime time.Duration
	ActualUptime   time.Duration
	Difference     time.Duration
	Status         string // "OK", "WARNING", "ERROR"
	ConnectionAge  time.Duration
	ConnectionOK   bool
}

// CPUMetrics holds CPU usage metrics for a process
type CPUMetrics struct {
	ProcessName   string
	UserJiffies   float64
	SystemJiffies float64
	TotalJiffies  float64
}

// CPUComparison holds baseline vs highperf CPU comparison
type CPUComparison struct {
	Baseline  CPUMetrics
	HighPerf  CPUMetrics
	UserDiff  float64 // percentage difference
	SysDiff   float64 // percentage difference
	TotalDiff float64 // percentage difference
}

// ConnectionMetrics holds metrics for a specific connection identified by peer_type
type ConnectionMetrics struct {
	SocketID   string
	PeerType   string // "publisher", "subscriber", "unknown" (derived from StreamId)
	RemoteAddr string
	StreamID   string
	Metrics    map[string]float64
}

// EnhancedComparison holds a comparison with additional metadata
type EnhancedComparison struct {
	Name        string
	BaselineVal float64
	HighPerfVal float64
	Diff        float64
	DiffPercent float64
	AbsDiff     float64 // For sorting
	Status      string  // "OK", "WARNING", "ERROR", "NEW", "GONE"
}

// ComparisonSection holds a group of comparisons
type ComparisonSection struct {
	Title       string
	Comparisons []EnhancedComparison
	OKCount     int
	WarnCount   int
	ErrorCount  int
}

// ParseProcessStartTime extracts gosrt_process_start_time_seconds from metrics
func ParseProcessStartTime(metrics map[string]float64) (time.Time, bool) {
	for key, val := range metrics {
		if strings.HasPrefix(key, "gosrt_process_start_time_seconds") {
			return time.Unix(int64(val), 0), true
		}
	}
	return time.Time{}, false
}

// ParseConnectionStartTime extracts gosrt_connection_start_time_seconds for a socket
func ParseConnectionStartTime(metrics map[string]float64, socketID string) (time.Time, bool) {
	for key, val := range metrics {
		if strings.HasPrefix(key, "gosrt_connection_start_time_seconds") && strings.Contains(key, socketID) {
			return time.Unix(int64(val), 0), true
		}
	}
	return time.Time{}, false
}

// ParseCPUJiffies extracts CPU jiffies from metrics
func ParseCPUJiffies(metrics map[string]float64) (user, system float64) {
	for key, val := range metrics {
		if strings.HasPrefix(key, "gosrt_process_cpu_user_jiffies_total") {
			user = val
		} else if strings.HasPrefix(key, "gosrt_process_cpu_system_jiffies_total") {
			system = val
		}
	}
	return user, system
}

// ExtractLabel extracts a label value from a metric key
// e.g., extractLabel("foo{bar=\"baz\",qux=\"quux\"}", "bar") returns "baz"
func ExtractLabel(metricKey, labelName string) string {
	pattern := regexp.MustCompile(labelName + `="([^"]*)"`)
	matches := pattern.FindStringSubmatch(metricKey)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// BuildSocketPeerTypeMap builds a socket_id → peer_type mapping from the identity metric
// Uses gosrt_connection_start_time_seconds which has full connection details
func BuildSocketPeerTypeMap(metrics map[string]float64) map[string]string {
	socketToPeerType := make(map[string]string)

	for key := range metrics {
		// Only look at the identity metric
		if !strings.HasPrefix(key, "gosrt_connection_start_time_seconds") {
			continue
		}

		socketID := ExtractLabel(key, "socket_id")
		peerType := ExtractLabel(key, "peer_type")

		if socketID != "" && peerType != "" {
			socketToPeerType[socketID] = peerType
		}
	}

	return socketToPeerType
}

// GroupMetricsByPeerType separates metrics by peer_type using socket_id correlation
// Step 1: Build socket_id → peer_type map from gosrt_connection_start_time_seconds
// Step 2: Use that map to group all other metrics
func GroupMetricsByPeerType(metrics map[string]float64) map[string]map[string]float64 {
	// Build the socket_id → peer_type mapping from identity metric
	socketToPeerType := BuildSocketPeerTypeMap(metrics)

	grouped := make(map[string]map[string]float64)

	for key, val := range metrics {
		// Extract socket_id from this metric
		socketID := ExtractLabel(key, "socket_id")

		// Look up peer_type from our mapping
		peerType := "unknown"
		if socketID != "" {
			if pt, ok := socketToPeerType[socketID]; ok {
				peerType = pt
			}
		}

		if grouped[peerType] == nil {
			grouped[peerType] = make(map[string]float64)
		}
		grouped[peerType][key] = val
	}

	return grouped
}

// NormalizeMetricKeyForComparison removes socket_id, instance, and other varying labels
// but keeps meaningful labels like type, status, reason, direction
func NormalizeMetricKeyForComparison(key string) string {
	idx := strings.Index(key, "{")
	if idx < 0 {
		return key
	}

	baseName := key[:idx]
	labelPart := key[idx:]

	// Extract meaningful labels to keep
	var keepLabels []string

	// Labels to keep
	for _, label := range []string{"type", "status", "reason", "direction"} {
		if val := ExtractLabel(labelPart, label); val != "" {
			keepLabels = append(keepLabels, fmt.Sprintf(`%s="%s"`, label, val))
		}
	}

	if len(keepLabels) == 0 {
		return baseName
	}

	return baseName + "{" + strings.Join(keepLabels, ",") + "}"
}

// CompareMetricMaps compares two metric maps and returns sorted comparisons
func CompareMetricMaps(baseline, highperf map[string]float64, sortByDiff bool) []EnhancedComparison {
	// Normalize and aggregate metrics
	baseNorm := make(map[string]float64)
	highNorm := make(map[string]float64)

	for key, val := range baseline {
		normKey := NormalizeMetricKeyForComparison(key)
		baseNorm[normKey] += val
	}
	for key, val := range highperf {
		normKey := NormalizeMetricKeyForComparison(key)
		highNorm[normKey] += val
	}

	// Build comparison list
	seen := make(map[string]bool)
	var comparisons []EnhancedComparison

	// Process baseline metrics
	for key, baseVal := range baseNorm {
		seen[key] = true
		highVal := highNorm[key]

		comp := createEnhancedComparison(key, baseVal, highVal)
		if comp.BaselineVal != 0 || comp.HighPerfVal != 0 {
			comparisons = append(comparisons, comp)
		}
	}

	// Process highperf-only metrics
	for key, highVal := range highNorm {
		if seen[key] {
			continue
		}
		comp := createEnhancedComparison(key, 0, highVal)
		if comp.HighPerfVal != 0 {
			comparisons = append(comparisons, comp)
		}
	}

	// Sort
	if sortByDiff {
		// Sort by absolute difference (biggest first)
		sort.Slice(comparisons, func(i, j int) bool {
			return comparisons[i].AbsDiff > comparisons[j].AbsDiff
		})
	} else {
		// Sort alphabetically
		sort.Slice(comparisons, func(i, j int) bool {
			return comparisons[i].Name < comparisons[j].Name
		})
	}

	return comparisons
}

// createEnhancedComparison builds a comparison with status
func createEnhancedComparison(name string, baseVal, highVal float64) EnhancedComparison {
	diff := highVal - baseVal
	var diffPct float64
	var status string

	absBase := math.Abs(baseVal)
	absHigh := math.Abs(highVal)

	// Handle "effectively zero" cases to avoid misleading percentages like "0 vs 0 = +8000%"
	// This happens with timing metrics that have sub-nanosecond values
	if absBase < EffectivelyZeroThreshold && absHigh < EffectivelyZeroThreshold {
		status = "OK"
		diffPct = 0
	} else if absBase < EffectivelyZeroThreshold {
		// Baseline is effectively zero, highperf has a real value
		status = "NEW"
		diffPct = 100
	} else if absHigh < EffectivelyZeroThreshold {
		// HighPerf is effectively zero, baseline has a real value
		status = "GONE"
		diffPct = -100
	} else {
		diffPct = (diff / baseVal) * 100
		absPct := math.Abs(diffPct) / 100

		if absPct <= ThresholdOK {
			status = "OK"
		} else if absPct <= ThresholdWarning {
			status = "WARNING"
		} else {
			status = "ERROR"
		}
	}

	return EnhancedComparison{
		Name:        simplifyMetricNameEnhanced(name),
		BaselineVal: baseVal,
		HighPerfVal: highVal,
		Diff:        diff,
		DiffPercent: diffPct,
		AbsDiff:     math.Abs(diffPct),
		Status:      status,
	}
}

// simplifyMetricNameEnhanced removes common prefixes for cleaner display
func simplifyMetricNameEnhanced(name string) string {
	name = strings.TrimPrefix(name, "gosrt_connection_")
	name = strings.TrimPrefix(name, "gosrt_")
	name = strings.TrimPrefix(name, "congestion_")

	// Parse remaining labels into readable format
	if idx := strings.Index(name, "{"); idx > 0 {
		baseName := name[:idx]
		labelPart := name[idx:]

		var suffixes []string

		if val := ExtractLabel(labelPart, "direction"); val != "" {
			suffixes = append(suffixes, val)
		}
		if val := ExtractLabel(labelPart, "type"); val != "" {
			suffixes = append(suffixes, val)
		}
		if val := ExtractLabel(labelPart, "status"); val != "" {
			suffixes = append(suffixes, val)
		}
		if val := ExtractLabel(labelPart, "reason"); val != "" {
			suffixes = append(suffixes, val)
		}

		if len(suffixes) > 0 {
			return baseName + " [" + strings.Join(suffixes, ",") + "]"
		}
		return baseName
	}

	return name
}

// CheckStability verifies process uptimes and connection ages match expected test duration
func CheckStability(baseline, highperf *TestMetrics, testDuration time.Duration) []StabilityResult {
	var results []StabilityResult

	components := []struct {
		name      string
		component string
		pipeline  string
		metrics   *TestMetrics
	}{
		{"baseline-cg", "client-generator", "baseline", baseline},
		{"baseline-server", "server", "baseline", baseline},
		{"baseline-client", "client", "baseline", baseline},
		{"highperf-cg", "client-generator", "highperf", highperf},
		{"highperf-server", "server", "highperf", highperf},
		{"highperf-client", "client", "highperf", highperf},
	}

	for _, comp := range components {
		snapshot := comp.metrics.GetSnapshotByLabel(comp.component, "pre-shutdown")
		if snapshot == nil {
			continue
		}

		result := StabilityResult{
			ProcessName:    comp.name,
			ExpectedUptime: testDuration,
		}

		if startTime, ok := ParseProcessStartTime(snapshot.Metrics); ok {
			// Use snapshot timestamp (when metrics were collected) not current time
			// This avoids false positives from test cleanup/analysis overhead
			result.ActualUptime = snapshot.Timestamp.Sub(startTime)
			result.Difference = result.ActualUptime - testDuration

			if result.Difference < 0 {
				result.Difference = -result.Difference
			}

			if result.Difference <= ProcessUptimeTolerance {
				result.Status = "OK"
			} else if result.Difference <= ProcessUptimeTolerance*2 {
				result.Status = "WARNING"
			} else {
				result.Status = "ERROR"
			}
		} else {
			result.Status = "UNKNOWN"
		}

		results = append(results, result)
	}

	return results
}

// CompareCPU compares CPU usage between baseline and highperf processes
func CompareCPU(baseline, highperf *TestMetrics) []CPUComparison {
	var comparisons []CPUComparison

	components := []struct {
		name      string
		component string
	}{
		{"cg", "client-generator"},
		{"server", "server"},
		{"client", "client"},
	}

	for _, comp := range components {
		baseSnapshot := baseline.GetSnapshotByLabel(comp.component, "pre-shutdown")
		highSnapshot := highperf.GetSnapshotByLabel(comp.component, "pre-shutdown")

		if baseSnapshot == nil || highSnapshot == nil {
			continue
		}

		baseUser, baseSys := ParseCPUJiffies(baseSnapshot.Metrics)
		highUser, highSys := ParseCPUJiffies(highSnapshot.Metrics)

		comparison := CPUComparison{
			Baseline: CPUMetrics{
				ProcessName:   "baseline-" + comp.name,
				UserJiffies:   baseUser,
				SystemJiffies: baseSys,
				TotalJiffies:  baseUser + baseSys,
			},
			HighPerf: CPUMetrics{
				ProcessName:   "highperf-" + comp.name,
				UserJiffies:   highUser,
				SystemJiffies: highSys,
				TotalJiffies:  highUser + highSys,
			},
		}

		// Calculate percentage differences
		if comparison.Baseline.UserJiffies > 0 {
			comparison.UserDiff = ((highUser - baseUser) / baseUser) * 100
		}
		if comparison.Baseline.SystemJiffies > 0 {
			comparison.SysDiff = ((highSys - baseSys) / baseSys) * 100
		}
		if comparison.Baseline.TotalJiffies > 0 {
			comparison.TotalDiff = ((comparison.HighPerf.TotalJiffies - comparison.Baseline.TotalJiffies) / comparison.Baseline.TotalJiffies) * 100
		}

		comparisons = append(comparisons, comparison)
	}

	return comparisons
}

// PrintEnhancedComparison prints the full enhanced comparison report
// lossRate is the configured packet loss rate (0.0-1.0) used for dynamic tolerance
func PrintEnhancedComparison(baseline, highperf *TestMetrics, testDuration time.Duration, lossRate float64) {
	// === STABILITY CHECKS ===
	printStabilityChecks(baseline, highperf, testDuration)

	// === CPU EFFICIENCY ===
	printCPUComparison(baseline, highperf)

	// === TYPE A: CROSS-PIPELINE COMPARISON ===
	printCrossPipelineComparison(baseline, highperf)

	// === TYPE B: SAME-CONNECTION VALIDATION ===
	printSameConnectionValidation(baseline, highperf, lossRate)

	// === SUMMARY ===
	printComparisonSummary(baseline, highperf, testDuration)
}

func printStabilityChecks(baseline, highperf *TestMetrics, testDuration time.Duration) {
	fmt.Println()
	fmt.Printf("%s╔══════════════════════════════════════════════════════════════════════════════╗%s\n", colorCyan, colorReset)
	fmt.Printf("%s║                         STABILITY CHECKS                                      ║%s\n", colorCyan, colorReset)
	fmt.Printf("%s╚══════════════════════════════════════════════════════════════════════════════╝%s\n", colorCyan, colorReset)

	results := CheckStability(baseline, highperf, testDuration)

	fmt.Printf("\n┌─────────────────────────────────────────────────────────────────────────────┐\n")
	fmt.Printf("│ %-25s %15s %15s %10s │\n", "Process", "Expected", "Actual", "Status")
	fmt.Printf("├─────────────────────────────────────────────────────────────────────────────┤\n")

	allOK := true
	for _, r := range results {
		statusColor := colorGreen
		statusIcon := "✓"
		if r.Status == "WARNING" {
			statusColor = colorYellow
			statusIcon = "⚠"
			allOK = false
		} else if r.Status == "ERROR" || r.Status == "UNKNOWN" {
			statusColor = colorRed
			statusIcon = "✗"
			allOK = false
		}

		// Color the process name based on pipeline
		processColor := colorBlue
		if strings.HasPrefix(r.ProcessName, "highperf") {
			processColor = colorGreen
		}

		fmt.Printf("│ %s%-25s%s %15s %15s %s%s %s%s │\n",
			processColor, r.ProcessName, colorReset,
			r.ExpectedUptime.Truncate(time.Second),
			r.ActualUptime.Truncate(time.Second),
			statusColor, statusIcon, r.Status, colorReset)
	}
	fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")

	if allOK {
		fmt.Printf("%s✓ All processes running for expected duration%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("%s⚠ Some processes may have restarted - check logs%s\n", colorYellow, colorReset)
	}
}

func printCPUComparison(baseline, highperf *TestMetrics) {
	fmt.Println()
	fmt.Printf("%s╔══════════════════════════════════════════════════════════════════════════════╗%s\n", colorCyan, colorReset)
	fmt.Printf("%s║                         CPU EFFICIENCY COMPARISON                             ║%s\n", colorCyan, colorReset)
	fmt.Printf("%s╚══════════════════════════════════════════════════════════════════════════════╝%s\n", colorCyan, colorReset)

	comparisons := CompareCPU(baseline, highperf)

	if len(comparisons) == 0 {
		fmt.Printf("\n%s(No CPU metrics available - Linux only)%s\n", colorYellow, colorReset)
		return
	}

	// Header with all columns
	fmt.Printf("\n┌───────────────────────────────────────────────────────────────────────────────────────────┐\n")
	fmt.Printf("│ %-14s %10s %10s %10s %10s %10s %10s │\n",
		"Component", "User", "Δ User", "System", "Δ System", "Total", "Δ Total")
	fmt.Printf("├───────────────────────────────────────────────────────────────────────────────────────────┤\n")

	for _, c := range comparisons {
		// Calculate diff colors
		userDiffColor := colorReset
		if c.UserDiff < -5 {
			userDiffColor = colorGreen // Less userland CPU = better
		} else if c.UserDiff > 5 {
			userDiffColor = colorYellow
		}

		sysDiffColor := colorReset
		if c.SysDiff > 50 {
			sysDiffColor = colorYellow // More system CPU expected with io_uring
		}

		totalDiffColor := colorReset
		if c.TotalDiff < -5 {
			totalDiffColor = colorGreen
		} else if c.TotalDiff > 20 {
			totalDiffColor = colorYellow
		}

		// Component name (e.g., "cg", "server", "client")
		compName := strings.TrimPrefix(c.Baseline.ProcessName, "baseline-")

		// Baseline row
		fmt.Printf("│ %s%-14s%s %10.0f %10s %10.0f %10s %10.0f %10s │\n",
			colorBlue, "base-"+compName, colorReset,
			c.Baseline.UserJiffies, "",
			c.Baseline.SystemJiffies, "",
			c.Baseline.TotalJiffies, "")

		// HighPerf row with all deltas
		userDiffStr := fmt.Sprintf("%+.1f%%", c.UserDiff)
		sysDiffStr := fmt.Sprintf("%+.1f%%", c.SysDiff)
		totalDiffStr := fmt.Sprintf("%+.1f%%", c.TotalDiff)

		fmt.Printf("│ %s%-14s%s %10.0f %s%10s%s %10.0f %s%10s%s %10.0f %s%10s%s │\n",
			colorGreen, "high-"+compName, colorReset,
			c.HighPerf.UserJiffies, userDiffColor, userDiffStr, colorReset,
			c.HighPerf.SystemJiffies, sysDiffColor, sysDiffStr, colorReset,
			c.HighPerf.TotalJiffies, totalDiffColor, totalDiffStr, colorReset)
		fmt.Printf("│ %-87s │\n", "")
	}
	fmt.Printf("└───────────────────────────────────────────────────────────────────────────────────────────┘\n")
	fmt.Printf("(Jiffies = 1/100th second of CPU time)\n")
	fmt.Printf("%sNote: io_uring shifts work from userland to kernel - higher system CPU is expected%s\n", colorYellow, colorReset)
}

func printCrossPipelineComparison(baseline, highperf *TestMetrics) {
	fmt.Println()
	fmt.Printf("%s╔══════════════════════════════════════════════════════════════════════════════╗%s\n", colorCyan, colorReset)
	fmt.Printf("%s║     TYPE A: CROSS-PIPELINE COMPARISON (%sBaseline%s vs %sHighPerf%s)                  ║%s\n",
		colorCyan, colorBlue, colorCyan, colorGreen, colorCyan, colorReset)
	fmt.Printf("%s╚══════════════════════════════════════════════════════════════════════════════╝%s\n", colorCyan, colorReset)

	// A1: Client-Generator comparison
	printComponentComparison("A1: Client-Generator",
		baseline.GetSnapshotByLabel("client-generator", "pre-shutdown"),
		highperf.GetSnapshotByLabel("client-generator", "pre-shutdown"),
		"")

	// A2 & A3: Server comparison (split by peer_type)
	baseServer := baseline.GetSnapshotByLabel("server", "pre-shutdown")
	highServer := highperf.GetSnapshotByLabel("server", "pre-shutdown")

	if baseServer != nil && highServer != nil {
		// Group by peer_type
		// Note: peer_type is derived from StreamId in connection.go:
		//   - "publish:..." → "publisher" (CG connecting to server)
		//   - "subscribe:..." → "subscriber" (Client connecting to server)
		baseGrouped := GroupMetricsByPeerType(baseServer.Metrics)
		highGrouped := GroupMetricsByPeerType(highServer.Metrics)

		// A2: Server CG-side (publisher connections)
		printComponentComparison("A2: Server (CG-side)",
			&MetricsSnapshot{Metrics: baseGrouped["publisher"]},
			&MetricsSnapshot{Metrics: highGrouped["publisher"]},
			"publisher")

		// A3: Server Client-side (subscriber connections)
		printComponentComparison("A3: Server (Client-side)",
			&MetricsSnapshot{Metrics: baseGrouped["subscriber"]},
			&MetricsSnapshot{Metrics: highGrouped["subscriber"]},
			"subscriber")
	}

	// A4: Client comparison
	printComponentComparison("A4: Client",
		baseline.GetSnapshotByLabel("client", "pre-shutdown"),
		highperf.GetSnapshotByLabel("client", "pre-shutdown"),
		"")
}

func printComponentComparison(title string, baseSnapshot, highSnapshot *MetricsSnapshot, peerTypeFilter string) {
	fmt.Println()
	fmt.Printf("┌─────────────────────────────────────────────────────────────────────────────┐\n")
	fmt.Printf("│ %-75s │\n", title)
	fmt.Printf("├─────────────────────────────────────────────────────────────────────────────┤\n")

	if baseSnapshot == nil || highSnapshot == nil || baseSnapshot.Metrics == nil || highSnapshot.Metrics == nil {
		fmt.Printf("│ %-75s │\n", "(No metrics available)")
		fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
		return
	}

	comparisons := CompareMetricMaps(baseSnapshot.Metrics, highSnapshot.Metrics, true)

	// Filter to important metrics and limit display
	var filtered []EnhancedComparison
	for _, c := range comparisons {
		// Skip metrics with zero values on both sides
		if c.BaselineVal == 0 && c.HighPerfVal == 0 {
			continue
		}
		// Skip internal/timing metrics for cleaner output
		if strings.Contains(c.Name, "rtt_ms") || strings.Contains(c.Name, "send_period") {
			continue
		}
		filtered = append(filtered, c)
	}

	if len(filtered) == 0 {
		fmt.Printf("│ %-75s │\n", "(No significant metrics)")
		fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
		return
	}

	fmt.Printf("│ %-38s %s%12s%s %s%12s%s %10s │\n",
		"Metric", colorBlue, "Baseline", colorReset, colorGreen, "HighPerf", colorReset, "Diff")
	fmt.Printf("│ %-38s %12s %12s %10s │\n", strings.Repeat("─", 38), "────────────", "────────────", "──────────")

	// Show top 15 by difference
	maxShow := 15
	if len(filtered) < maxShow {
		maxShow = len(filtered)
	}

	for i := 0; i < maxShow; i++ {
		c := filtered[i]
		printComparisonRow(c)
	}

	if len(filtered) > maxShow {
		fmt.Printf("│ %-75s │\n", fmt.Sprintf("... and %d more metrics", len(filtered)-maxShow))
	}

	fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
}

// formatMetricValue formats a value adaptively based on its magnitude.
// This prevents tiny values like 0.000000001 from displaying as "0".
func formatMetricValue(val float64) string {
	if val == 0 {
		return "0"
	}
	absVal := math.Abs(val)
	switch {
	case absVal >= 1000000:
		return fmt.Sprintf("%.2e", val) // 1.23e+06
	case absVal >= 1:
		return fmt.Sprintf("%.0f", val) // 1234
	case absVal >= 0.001:
		return fmt.Sprintf("%.4f", val) // 0.0012
	case absVal >= 1e-6:
		return fmt.Sprintf("%.2e", val) // 1.23e-04
	default:
		return fmt.Sprintf("%.1e", val) // 1.2e-09
	}
}

func printComparisonRow(c EnhancedComparison) {
	// Format difference
	var diffStr string
	var diffColor string

	switch c.Status {
	case "NEW":
		diffStr = "NEW"
		diffColor = colorYellow
	case "GONE":
		diffStr = "GONE"
		diffColor = colorYellow
	case "OK":
		if c.Diff == 0 || c.DiffPercent == 0 {
			diffStr = "~0"
		} else {
			diffStr = fmt.Sprintf("%+.1f%%", c.DiffPercent)
		}
		diffColor = colorReset
	case "WARNING":
		diffStr = fmt.Sprintf("%+.1f%%", c.DiffPercent)
		diffColor = colorYellow
	case "ERROR":
		diffStr = fmt.Sprintf("%+.1f%%", c.DiffPercent)
		diffColor = colorRed
	default:
		diffStr = fmt.Sprintf("%+.1f%%", c.DiffPercent)
		diffColor = colorReset
	}

	// Status icon
	var statusIcon string
	switch c.Status {
	case "OK":
		statusIcon = "  "
	case "WARNING":
		statusIcon = "⚠ "
	case "ERROR":
		statusIcon = "✗ "
	case "NEW", "GONE":
		statusIcon = "? "
	default:
		statusIcon = "  "
	}

	name := c.Name
	if len(name) > 36 {
		name = name[:33] + "..."
	}

	// Use adaptive formatting for values to handle both large counts and tiny durations
	baseStr := formatMetricValue(c.BaselineVal)
	highStr := formatMetricValue(c.HighPerfVal)

	fmt.Printf("│ %s%-36s %12s %12s %s%10s%s │\n",
		statusIcon, name, baseStr, highStr, diffColor, diffStr, colorReset)
}

func printSameConnectionValidation(baseline, highperf *TestMetrics, lossRate float64) {
	fmt.Println()
	fmt.Printf("%s╔══════════════════════════════════════════════════════════════════════════════╗%s\n", colorCyan, colorReset)
	fmt.Printf("%s║     TYPE B: SAME-CONNECTION VALIDATION (Sender ↔ Receiver)                   ║%s\n", colorCyan, colorReset)
	fmt.Printf("%s╚══════════════════════════════════════════════════════════════════════════════╝%s\n", colorCyan, colorReset)

	// Print tolerance info
	tolerance := calculateTolerance(lossRate)
	fmt.Printf("\nConfigured Loss: %.1f%% → Tolerance: %.1f%%\n", lossRate*100, tolerance*100)

	// B1: Baseline CG → Server (CG sends data, Server receives)
	// CG is SENDER, Server's publisher connection is RECEIVER
	printConnectionValidation("B1: Baseline CG → Server (data flow)",
		baseline.GetSnapshotByLabel("client-generator", "pre-shutdown"),
		baseline.GetSnapshotByLabel("server", "pre-shutdown"),
		"publisher", colorBlue, lossRate, true)

	// B2: HighPerf CG → Server (CG sends data, Server receives)
	printConnectionValidation("B2: HighPerf CG → Server (data flow)",
		highperf.GetSnapshotByLabel("client-generator", "pre-shutdown"),
		highperf.GetSnapshotByLabel("server", "pre-shutdown"),
		"publisher", colorGreen, lossRate, true)

	// B3: Baseline Server → Client (Server sends data, Client receives)
	// Server's subscriber connection is SENDER, Client is RECEIVER
	printConnectionValidation("B3: Baseline Server → Client (data flow)",
		baseline.GetSnapshotByLabel("server", "pre-shutdown"),
		baseline.GetSnapshotByLabel("client", "pre-shutdown"),
		"subscriber", colorBlue, lossRate, false)

	// B4: HighPerf Server → Client (Server sends data, Client receives)
	printConnectionValidation("B4: HighPerf Server → Client (data flow)",
		highperf.GetSnapshotByLabel("server", "pre-shutdown"),
		highperf.GetSnapshotByLabel("client", "pre-shutdown"),
		"subscriber", colorGreen, lossRate, false)
}

// validationPair defines a direction-aware metric pair for same-connection validation
type validationPair struct {
	name      string  // Display name
	direction string  // "S→R" (sender to receiver) or "R→S" (receiver to sender)
	senderKey string  // Metric name on sender side
	recvKey   string  // Metric name on receiver side
	typeLabel string  // type label to filter (e.g., "data", "ack", empty for all)
	extraTol  float64 // Additional tolerance for this metric type (e.g., 0.05 for retrans)
}

func printConnectionValidation(title string, senderSnap, receiverSnap *MetricsSnapshot,
	serverPeerType string, pipelineColor string, lossRate float64, senderIsFirst bool) {

	fmt.Println()
	fmt.Printf("┌─────────────────────────────────────────────────────────────────────────────┐\n")
	fmt.Printf("│ %s%-75s%s │\n", pipelineColor, title, colorReset)
	fmt.Printf("├─────────────────────────────────────────────────────────────────────────────┤\n")

	// Get sender and receiver metrics
	var senderMetrics, receiverMetrics map[string]float64

	if senderIsFirst {
		// snapshot1 is sender (CG/Server), snapshot2 is receiver (Server/Client)
		if senderSnap == nil || senderSnap.Metrics == nil {
			fmt.Printf("│ %-75s │\n", "(No sender metrics available)")
			fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
			return
		}
		senderMetrics = senderSnap.Metrics

		if receiverSnap == nil || receiverSnap.Metrics == nil {
			fmt.Printf("│ %-75s │\n", "(No receiver metrics available)")
			fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
			return
		}

		// For server, filter by peer_type to get the specific connection
		if serverPeerType != "" {
			grouped := GroupMetricsByPeerType(receiverSnap.Metrics)
			receiverMetrics = grouped[serverPeerType]
		} else {
			receiverMetrics = receiverSnap.Metrics
		}
	} else {
		// snapshot1 is server (sender), snapshot2 is client (receiver)
		// Need to filter server metrics by peer_type first
		if senderSnap == nil || senderSnap.Metrics == nil {
			fmt.Printf("│ %-75s │\n", "(No sender metrics available)")
			fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
			return
		}

		if serverPeerType != "" {
			grouped := GroupMetricsByPeerType(senderSnap.Metrics)
			senderMetrics = grouped[serverPeerType]
		} else {
			senderMetrics = senderSnap.Metrics
		}

		if receiverSnap == nil || receiverSnap.Metrics == nil {
			fmt.Printf("│ %-75s │\n", "(No receiver metrics available)")
			fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
			return
		}
		receiverMetrics = receiverSnap.Metrics
	}

	if senderMetrics == nil || len(senderMetrics) == 0 {
		fmt.Printf("│ %-75s │\n", "(No matching sender connection found)")
		fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
		return
	}
	if receiverMetrics == nil || len(receiverMetrics) == 0 {
		fmt.Printf("│ %-75s │\n", "(No matching receiver connection found)")
		fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
		return
	}

	// Direction-aware metric pairs
	// S→R: Sender sends, receiver receives (data, retrans, ACKACK)
	// R→S: Receiver sends, sender receives (ACK, NAK)
	validationPairs := []validationPair{
		// DATA flow: Sender → Receiver
		{"Data Packets [data]", "S→R", "packets_sent_total", "packets_received_total", "data", 0.0},
		{"Retransmits [data]", "S→R", "retransmissions_total", "retransmissions_total", "", 0.05},

		// ACK flow: Receiver → Sender
		{"ACKs", "R→S", "packets_received_total", "packets_sent_total", "ack", 0.0},

		// ACKACK flow: Sender → Receiver
		{"ACKACKs", "S→R", "packets_sent_total", "packets_received_total", "ackack", 0.0},

		// NAK flow: Receiver → Sender
		{"NAKs", "R→S", "packets_received_total", "packets_sent_total", "nak", 0.0},
	}

	baseTolerance := calculateTolerance(lossRate)

	fmt.Printf("│ %-20s %-4s %12s %12s %8s %8s │\n", "Metric", "Dir", "Sender", "Receiver", "Diff", "Status")
	fmt.Printf("│ %-20s %-4s %12s %12s %8s %8s │\n", strings.Repeat("─", 20), "────", "────────────", "────────────", "────────", "────────")

	allOK := true
	for _, vp := range validationPairs {
		// Get metric values based on direction
		var senderVal, receiverVal float64

		if vp.direction == "S→R" {
			// Sender sends, receiver receives
			senderVal = getMetricWithTypeLabel(senderMetrics, vp.senderKey, vp.typeLabel)
			receiverVal = getMetricWithTypeLabel(receiverMetrics, vp.recvKey, vp.typeLabel)
		} else {
			// R→S: Receiver sends (senderKey on receiver), Sender receives (recvKey on sender)
			senderVal = getMetricWithTypeLabel(receiverMetrics, vp.senderKey, vp.typeLabel)
			receiverVal = getMetricWithTypeLabel(senderMetrics, vp.recvKey, vp.typeLabel)
		}

		// Calculate difference
		// For S→R: sender should be >= receiver (due to loss)
		// For R→S: receiver (which sends) should be >= sender (which receives)
		var diffPct float64
		var expectedHigher float64
		if vp.direction == "S→R" {
			expectedHigher = senderVal
		} else {
			expectedHigher = senderVal // In R→S context, senderVal is what receiver sent
		}

		if expectedHigher > 0 {
			diffPct = math.Abs((receiverVal-senderVal)/expectedHigher) * 100
		}

		// Dynamic tolerance
		tolerance := (baseTolerance + vp.extraTol) * 100

		var status string
		var statusColor string
		if diffPct <= tolerance {
			status = "✓ OK"
			statusColor = colorGreen
		} else if diffPct <= tolerance*1.5 {
			status = "⚠ WARN"
			statusColor = colorYellow
			allOK = false
		} else {
			status = "✗ ERR"
			statusColor = colorRed
			allOK = false
		}

		fmt.Printf("│ %-20s %-4s %12.0f %12.0f %7.1f%% %s%8s%s │\n",
			vp.name, vp.direction, senderVal, receiverVal, diffPct, statusColor, status, colorReset)
	}

	fmt.Printf("├─────────────────────────────────────────────────────────────────────────────┤\n")
	if allOK {
		fmt.Printf("│ %s✓ Connection validated - metrics match within tolerance (%.1f%%)%s             │\n",
			colorGreen, baseTolerance*100, colorReset)
	} else {
		fmt.Printf("│ %s⚠ Discrepancies detected - check direction-aware metrics above%s             │\n",
			colorYellow, colorReset)
	}
	fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
}

// getMetricWithTypeLabel gets a metric value, optionally filtering by type label
func getMetricWithTypeLabel(metrics map[string]float64, prefix string, typeLabel string) float64 {
	var sum float64
	for key, val := range metrics {
		// Normalize the key
		normKey := NormalizeMetricKeyForComparison(key)
		baseName := strings.TrimPrefix(normKey, "gosrt_connection_")
		baseName = strings.TrimPrefix(baseName, "congestion_")

		// Check if this metric matches the prefix
		if !strings.Contains(baseName, prefix) && !strings.HasPrefix(baseName, prefix) {
			continue
		}

		// Check type label if specified
		if typeLabel != "" {
			// Look for type label in the original key
			if !strings.Contains(key, "["+typeLabel+"]") && !strings.Contains(key, "type=\""+typeLabel+"\"") {
				continue
			}
		}

		sum += val
	}
	return sum
}

// sumMetricsByPrefix sums all metrics matching a prefix
func sumMetricsByPrefix(metrics map[string]float64, prefix string) float64 {
	var sum float64
	for key, val := range metrics {
		// Check if the base metric name matches
		normKey := NormalizeMetricKeyForComparison(key)
		baseName := strings.TrimPrefix(normKey, "gosrt_connection_")
		baseName = strings.TrimPrefix(baseName, "congestion_")

		// Extract just the metric name without labels
		if idx := strings.Index(baseName, "{"); idx > 0 {
			baseName = baseName[:idx]
		}
		if idx := strings.Index(baseName, " "); idx > 0 {
			baseName = baseName[:idx]
		}

		if strings.Contains(baseName, prefix) || strings.HasPrefix(baseName, prefix) {
			sum += val
		}
	}
	return sum
}

func printComparisonSummary(baseline, highperf *TestMetrics, testDuration time.Duration) {
	fmt.Println()
	fmt.Printf("%s╔══════════════════════════════════════════════════════════════════════════════╗%s\n", colorCyan, colorReset)
	fmt.Printf("%s║                              SUMMARY                                          ║%s\n", colorCyan, colorReset)
	fmt.Printf("%s╚══════════════════════════════════════════════════════════════════════════════╝%s\n", colorCyan, colorReset)

	// Count stability issues
	stabilityResults := CheckStability(baseline, highperf, testDuration)
	stabilityOK := 0
	stabilityWarn := 0
	stabilityErr := 0
	for _, r := range stabilityResults {
		switch r.Status {
		case "OK":
			stabilityOK++
		case "WARNING":
			stabilityWarn++
		default:
			stabilityErr++
		}
	}

	// CPU comparison summary
	cpuComparisons := CompareCPU(baseline, highperf)
	var totalCPUDiff float64
	for _, c := range cpuComparisons {
		totalCPUDiff += c.TotalDiff
	}
	avgCPUDiff := totalCPUDiff / float64(len(cpuComparisons))

	fmt.Println()
	fmt.Printf("┌─────────────────────────────────────────────────────────────────────────────┐\n")
	fmt.Printf("│ Stability:      %s%d OK%s, %s%d warnings%s, %s%d errors%s                                   │\n",
		colorGreen, stabilityOK, colorReset,
		colorYellow, stabilityWarn, colorReset,
		colorRed, stabilityErr, colorReset)

	cpuColor := colorGreen
	cpuStatus := "better"
	if avgCPUDiff > 0 {
		cpuColor = colorRed
		cpuStatus = "worse"
	}
	fmt.Printf("│ CPU Efficiency: %s%+.1f%% avg%s (%s is %s)                                        │\n",
		cpuColor, avgCPUDiff, colorReset, colorGreen+"HighPerf"+colorReset, cpuStatus)

	fmt.Printf("│ Test Duration:  %s                                                          │\n",
		testDuration.Truncate(time.Second))
	fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")

	// Overall status
	overallStatus := "PASSED"
	overallColor := colorGreen
	if stabilityErr > 0 {
		overallStatus = "FAILED (stability issues)"
		overallColor = colorRed
	} else if stabilityWarn > 0 {
		overallStatus = "PASSED with warnings"
		overallColor = colorYellow
	}

	fmt.Printf("\n%s%s=== Overall: %s ===%s\n", colorBold, overallColor, overallStatus, colorReset)
}

// formatDuration formats a duration for display
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return strconv.Itoa(hours) + "h" + strconv.Itoa(minutes) + "m" + strconv.Itoa(seconds) + "s"
	}
	if minutes > 0 {
		return strconv.Itoa(minutes) + "m" + strconv.Itoa(seconds) + "s"
	}
	return strconv.Itoa(seconds) + "s"
}
