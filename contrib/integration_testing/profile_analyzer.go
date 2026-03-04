package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ProfileAnalysis holds analysis results for a single profile
type ProfileAnalysis struct {
	Component   string // "server", "cg", "client"
	Pipeline    string // "baseline", "highperf" (for parallel tests)
	ProfileType ProfileType
	FilePath    string
	TopOutput   string     // Output from `pprof -top`
	FlameGraph  string     // Path to generated SVG
	TopFuncs    []FuncStat // Parsed top functions (top 10)

	// Aggregate metrics
	TotalTime     string // For CPU profiles
	TotalAllocs   int64  // For allocs profiles
	TotalBytes    int64  // For heap profiles
	TotalWaitTime string // For mutex/block profiles
}

// FuncStat represents a function's profile statistics
type FuncStat struct {
	Name       string
	Flat       float64 // Percentage or absolute value
	FlatStr    string  // Original string representation
	Cumulative float64
	CumStr     string
	Count      int64 // For allocs: number of allocations
	Size       int64 // For heap: bytes allocated
}

// ComparisonResult holds the comparison between baseline and highperf
type ComparisonResult struct {
	ProfileType ProfileType
	Component   string

	// Per-function comparisons
	FuncComparisons []FuncComparison

	// Aggregate comparisons
	TotalImprovement float64 // Positive = highperf is better
	Summary          string
	Recommendations  []string
}

// FuncComparison compares a single function across pipelines
type FuncComparison struct {
	FuncName      string
	BaselineValue float64
	HighPerfValue float64
	Delta         float64 // Negative = improvement
	DeltaPercent  float64
	IsNew         bool // Only in highperf
	IsRemoved     bool // Only in baseline
}

// AnalyzeProfile runs go tool pprof to analyze a profile
// If binaryPath is provided, it will be used to resolve symbols (important for debug builds)
func AnalyzeProfile(ctx context.Context, profilePath string, outputDir string) (*ProfileAnalysis, error) {
	return AnalyzeProfileWithBinary(ctx, profilePath, outputDir, "")
}

// AnalyzeProfileWithBinary runs go tool pprof with a specified binary for symbol resolution
func AnalyzeProfileWithBinary(ctx context.Context, profilePath string, outputDir string, binaryPath string) (*ProfileAnalysis, error) {
	if _, err := os.Stat(profilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("profile file not found: %s", profilePath)
	}

	analysis := &ProfileAnalysis{
		FilePath: profilePath,
	}

	// Parse profile path structure:
	// Path format: /tmp/profile_TestName_timestamp/component_name/profiletype.pprof
	// Example: /tmp/profile_Isolation-50M-Full_20251216_103448/control_server/cpu.pprof
	//
	// Component is in the parent directory name, profile type is the filename

	// Get the filename (e.g., "cpu.pprof")
	base := filepath.Base(profilePath)
	profileType := strings.TrimSuffix(base, ".pprof")
	analysis.ProfileType = ProfileType(profileType)

	// Get the parent directory name (e.g., "control_server")
	parentDir := filepath.Base(filepath.Dir(profilePath))

	// Parse component from parent directory
	// Formats: "control_server", "test_cg", "baseline_client", "highperf_server"
	switch {
	case strings.HasPrefix(parentDir, "control_"):
		analysis.Pipeline = "control"
		analysis.Component = strings.TrimPrefix(parentDir, "control_")
	case strings.HasPrefix(parentDir, "test_"):
		analysis.Pipeline = "test"
		analysis.Component = strings.TrimPrefix(parentDir, "test_")
	case strings.HasPrefix(parentDir, "baseline_"):
		analysis.Pipeline = "baseline"
		analysis.Component = strings.TrimPrefix(parentDir, "baseline_")
	case strings.HasPrefix(parentDir, "highperf_"):
		analysis.Pipeline = "highperf"
		analysis.Component = strings.TrimPrefix(parentDir, "highperf_")
	default:
		// Fallback: use entire parent dir as component
		analysis.Component = parentDir
	}

	// If no binary path specified, try to derive it from component name
	if binaryPath == "" {
		binaryPath = deriveBinaryPath(analysis.Component, analysis.Pipeline)
	}

	// Generate top output with different flags based on profile type
	topArgs := getTopArgs(analysis.ProfileType)
	topPath := strings.TrimSuffix(profilePath, ".pprof") + "_top.txt"

	// Build pprof command: go tool pprof [flags] [binary] profile
	// If binary is provided, symbols will be resolved correctly
	var cmdArgs []string
	cmdArgs = append(cmdArgs, "tool", "pprof")
	cmdArgs = append(cmdArgs, topArgs...)
	if binaryPath != "" {
		cmdArgs = append(cmdArgs, binaryPath)
	}
	cmdArgs = append(cmdArgs, profilePath)

	topCmd := exec.CommandContext(ctx, "go", cmdArgs...)
	topOutput, err := topCmd.Output()
	if err != nil {
		// Try to get stderr for debugging
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("pprof -top failed: %w\nstderr: %s", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("pprof -top failed: %w", err)
	}
	analysis.TopOutput = string(topOutput)
	if writeErr := os.WriteFile(topPath, topOutput, 0644); writeErr != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to write top output to %s: %v\n", topPath, writeErr)
	}

	// Generate flame graph SVG (optional - may fail if graphviz not installed)
	svgPath := strings.TrimSuffix(profilePath, ".pprof") + "_flame.svg"
	var svgArgs []string
	svgArgs = append(svgArgs, "tool", "pprof", "-svg")
	if binaryPath != "" {
		svgArgs = append(svgArgs, binaryPath)
	}
	svgArgs = append(svgArgs, profilePath)
	svgCmd := exec.CommandContext(ctx, "go", svgArgs...)
	svgOutput, svgErr := svgCmd.Output()
	if svgErr == nil {
		if svgWriteErr := os.WriteFile(svgPath, svgOutput, 0644); svgWriteErr == nil {
			analysis.FlameGraph = svgPath
		}
	}

	// Parse top functions
	analysis.TopFuncs = parseTopOutput(string(topOutput), analysis.ProfileType)

	// Extract aggregate metrics
	analysis.extractAggregates()

	return analysis, nil
}

// deriveBinaryPath attempts to find the debug binary for a given component
// Component names like "control_server", "test_cg", "baseline_client" map to binaries
func deriveBinaryPath(component, pipeline string) string {
	baseDir := getBaseDir()

	// Determine binary name from component
	var binaryName string
	switch {
	case strings.Contains(component, "server"):
		binaryName = "server-debug"
	case strings.Contains(component, "cg") || strings.Contains(component, "client-generator"):
		binaryName = "client-generator-debug"
	case strings.Contains(component, "client"):
		binaryName = "client-debug"
	default:
		return "" // Unknown component
	}

	// Construct path based on binary type
	var binaryPath string
	switch {
	case strings.Contains(binaryName, "server"):
		binaryPath = filepath.Join(baseDir, "contrib", "server", binaryName)
	case strings.Contains(binaryName, "client-generator"):
		binaryPath = filepath.Join(baseDir, "contrib", "client-generator", binaryName)
	case strings.Contains(binaryName, "client"):
		binaryPath = filepath.Join(baseDir, "contrib", "client", binaryName)
	}

	// Check if debug binary exists
	if _, err := os.Stat(binaryPath); err == nil {
		return binaryPath
	}

	// Fall back to non-debug binary
	nonDebugPath := strings.TrimSuffix(binaryPath, "-debug")
	if _, err := os.Stat(nonDebugPath); err == nil {
		return nonDebugPath
	}

	return "" // No binary found
}

// getTopArgs returns pprof args appropriate for the profile type
func getTopArgs(pt ProfileType) []string {
	switch pt {
	case ProfileHeap:
		return []string{"-top", "-nodecount=10", "-inuse_space"}
	case ProfileAllocs:
		return []string{"-top", "-nodecount=10", "-alloc_objects"}
	case ProfileMutex, ProfileBlock:
		return []string{"-top", "-nodecount=10", "-contentions"}
	default:
		return []string{"-top", "-nodecount=10"}
	}
}

// parseTopOutput extracts function statistics from pprof -top output
func parseTopOutput(output string, pt ProfileType) []FuncStat {
	var funcs []FuncStat
	lines := strings.Split(output, "\n")

	// Skip header lines and parse data
	inData := false
	for _, line := range lines {
		if strings.Contains(line, "flat") && strings.Contains(line, "cum") {
			inData = true
			continue
		}
		if !inData || strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		stat := FuncStat{
			FlatStr: fields[0] + " " + fields[1],
			CumStr:  fields[2] + " " + fields[3],
			Name:    fields[len(fields)-1],
		}

		// Parse percentage from fields[1] (e.g., "23.4%")
		stat.Flat = parsePercentage(fields[1])
		stat.Cumulative = parsePercentage(fields[3])

		funcs = append(funcs, stat)

		if len(funcs) >= 10 {
			break
		}
	}

	return funcs
}

func parsePercentage(s string) float64 {
	s = strings.TrimSuffix(s, "%")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func (a *ProfileAnalysis) extractAggregates() {
	// Extract totals from the header of pprof output
	lines := strings.Split(a.TopOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, "of") && strings.Contains(line, "total") {
			// e.g., "2.34s of 10.5s total (22.3%)"
			re := regexp.MustCompile(`([\d.]+\w+)\s+of\s+([\d.]+\w+)\s+total`)
			if matches := re.FindStringSubmatch(line); len(matches) >= 3 {
				a.TotalTime = matches[2]
			}
		}
	}
}

// CompareProfiles generates a comprehensive comparison between baseline and highperf
func CompareProfiles(baseline, highperf *ProfileAnalysis) *ComparisonResult {
	result := &ComparisonResult{
		ProfileType: baseline.ProfileType,
		Component:   baseline.Component,
	}

	// Create map of baseline functions
	baseMap := make(map[string]FuncStat)
	for _, f := range baseline.TopFuncs {
		baseMap[f.Name] = f
	}

	// Create map of highperf functions
	hpMap := make(map[string]FuncStat)
	for _, f := range highperf.TopFuncs {
		hpMap[f.Name] = f
	}

	// Compare all functions from both
	allFuncs := make(map[string]bool)
	for name := range baseMap {
		allFuncs[name] = true
	}
	for name := range hpMap {
		allFuncs[name] = true
	}

	for name := range allFuncs {
		baseStat, inBase := baseMap[name]
		hpStat, inHP := hpMap[name]

		comp := FuncComparison{
			FuncName: name,
		}

		switch {
		case inBase && inHP:
			comp.BaselineValue = baseStat.Flat
			comp.HighPerfValue = hpStat.Flat
			comp.Delta = hpStat.Flat - baseStat.Flat
			if baseStat.Flat > 0 {
				comp.DeltaPercent = (comp.Delta / baseStat.Flat) * 100
			}
		case inBase:
			comp.BaselineValue = baseStat.Flat
			comp.IsRemoved = true
			comp.Delta = -baseStat.Flat
			comp.DeltaPercent = -100
		default:
			comp.HighPerfValue = hpStat.Flat
			comp.IsNew = true
			comp.Delta = hpStat.Flat
		}

		result.FuncComparisons = append(result.FuncComparisons, comp)
	}

	// Sort by absolute delta (biggest changes first)
	sortByAbsDelta(result.FuncComparisons)

	// Generate summary and recommendations
	result.generateSummary()

	return result
}

func sortByAbsDelta(comps []FuncComparison) {
	// Sort by absolute delta descending (bubble sort for simplicity)
	for i := 0; i < len(comps)-1; i++ {
		for j := i + 1; j < len(comps); j++ {
			if absFloat(comps[j].Delta) > absFloat(comps[i].Delta) {
				comps[i], comps[j] = comps[j], comps[i]
			}
		}
	}
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func (r *ComparisonResult) generateSummary() {
	var buf bytes.Buffer

	// Count improvements vs regressions
	improvements := 0
	regressions := 0
	for _, c := range r.FuncComparisons {
		if c.Delta < -1 { // More than 1% improvement
			improvements++
		} else if c.Delta > 1 { // More than 1% regression
			regressions++
		}
	}

	fmt.Fprintf(&buf, "%d improvements, %d regressions\n", improvements, regressions)

	// Top 3 improvements
	fmt.Fprintf(&buf, "\nTop improvements:\n")
	count := 0
	for _, c := range r.FuncComparisons {
		if c.Delta < 0 && count < 3 {
			fmt.Fprintf(&buf, "  • %s: %.1f%% → %.1f%% (%.1f%% reduction)\n",
				truncateStr(c.FuncName, 60), c.BaselineValue, c.HighPerfValue, -c.DeltaPercent)
			count++
		}
	}

	r.Summary = buf.String()

	// Generate recommendations based on profile type
	r.generateRecommendations()
}

func (r *ComparisonResult) generateRecommendations() {
	for _, c := range r.FuncComparisons {
		// Check for common optimization opportunities
		if strings.Contains(c.FuncName, "chanrecv") || strings.Contains(c.FuncName, "chansend") {
			if c.HighPerfValue > 5 {
				r.Recommendations = append(r.Recommendations,
					fmt.Sprintf("Channel overhead (%.1f%%): Consider buffered channels or io_uring", c.HighPerfValue))
			}
		}
		if strings.Contains(c.FuncName, "makeSlice") || strings.Contains(c.FuncName, "makeslice") {
			if c.HighPerfValue > 3 {
				r.Recommendations = append(r.Recommendations,
					fmt.Sprintf("Slice allocation (%.1f%%): Consider sync.Pool for buffer reuse", c.HighPerfValue))
			}
		}
		if strings.Contains(c.FuncName, "Mutex") || strings.Contains(c.FuncName, "Lock") {
			if c.HighPerfValue > 5 {
				r.Recommendations = append(r.Recommendations,
					fmt.Sprintf("Lock contention (%.1f%%): Consider lock-free structures or sharding", c.HighPerfValue))
			}
		}
		if strings.Contains(c.FuncName, "mallocgc") || strings.Contains(c.FuncName, "gcBgMarkWorker") {
			if c.HighPerfValue > 5 {
				r.Recommendations = append(r.Recommendations,
					fmt.Sprintf("GC pressure (%.1f%%): Consider object pooling or reducing allocations", c.HighPerfValue))
			}
		}
		if strings.Contains(c.FuncName, "syscall") || strings.Contains(c.FuncName, "Syscall") {
			if c.HighPerfValue > 10 {
				r.Recommendations = append(r.Recommendations,
					fmt.Sprintf("Syscall overhead (%.1f%%): Consider io_uring or batching", c.HighPerfValue))
			}
		}
	}
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// FormatComparison generates a formatted string for the comparison
func (r *ComparisonResult) FormatComparison() string {
	var buf bytes.Buffer

	// Use 110-char wide box for better function name visibility
	fmt.Fprintf(&buf, "\n╔══════════════════════════════════════════════════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(&buf, "║ %s %s COMPARISON                                                                                  \n",
		strings.ToUpper(r.Component), strings.ToUpper(string(r.ProfileType)))
	fmt.Fprintf(&buf, "╠══════════════════════════════════════════════════════════════════════════════════════════════════════════╣\n")

	fmt.Fprintf(&buf, "║ %-70s %10s %10s %12s ║\n", "Function", "Baseline", "HighPerf", "Delta")
	fmt.Fprintf(&buf, "║ %s ║\n", strings.Repeat("─", 104))

	// Show top 10 comparisons (increased from 5)
	for i, c := range r.FuncComparisons {
		if i >= 10 {
			break
		}

		deltaStr := fmt.Sprintf("%.1f%%", c.DeltaPercent)
		indicator := ""
		if c.Delta < -5 {
			indicator = " ⬇"
		} else if c.Delta > 5 {
			indicator = " ⬆"
		}
		if c.IsNew {
			deltaStr = "(new)"
		} else if c.IsRemoved {
			deltaStr = "(gone)"
		}

		fmt.Fprintf(&buf, "║ %-70s %9.1f%% %9.1f%% %10s%s ║\n",
			truncateStr(c.FuncName, 70), c.BaselineValue, c.HighPerfValue, deltaStr, indicator)
	}

	fmt.Fprintf(&buf, "╠══════════════════════════════════════════════════════════════════════════════════════════════════════════╣\n")
	fmt.Fprintf(&buf, "║ SUMMARY: %s", r.Summary)

	if len(r.Recommendations) > 0 {
		fmt.Fprintf(&buf, "╠══════════════════════════════════════════════════════════════════════════════════════════════════════════╣\n")
		fmt.Fprintf(&buf, "║ RECOMMENDATIONS:                                                                                        ║\n")
		for _, rec := range r.Recommendations {
			fmt.Fprintf(&buf, "║ • %-101s ║\n", truncateStr(rec, 101))
		}
	}

	fmt.Fprintf(&buf, "╚══════════════════════════════════════════════════════════════════════════════════════════════════════════╝\n")

	return buf.String()
}

// AnalyzeAllProfiles analyzes all profiles in a directory
func AnalyzeAllProfiles(ctx context.Context, profileDir string) ([]*ProfileAnalysis, error) {
	var analyses []*ProfileAnalysis

	err := filepath.Walk(profileDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".pprof") {
			return nil
		}

		analysis, err := AnalyzeProfile(ctx, path, profileDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to analyze %s: %v\n", path, err)
			return nil // Continue with other files
		}

		analyses = append(analyses, analysis)
		return nil
	})

	return analyses, err
}

// PrintAnalysisSummary prints a summary of all profile analyses
func PrintAnalysisSummary(analyses []*ProfileAnalysis) {
	if len(analyses) == 0 {
		fmt.Println("No profiles analyzed")
		return
	}

	fmt.Printf("\n╔══════════════════════════════════════════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  PROFILE ANALYSIS SUMMARY                                                                                ║\n")
	fmt.Printf("╠══════════════════════════════════════════════════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║ %-12s %-10s %-8s %-70s ║\n", "Component", "Pipeline", "Type", "Top Function")
	fmt.Printf("║ %s ║\n", strings.Repeat("─", 104))

	for _, a := range analyses {
		topFunc := "(none)"
		if len(a.TopFuncs) > 0 {
			topFunc = truncateStr(a.TopFuncs[0].Name, 70)
		}
		pipeline := a.Pipeline
		if pipeline == "" {
			pipeline = "-"
		}
		fmt.Printf("║ %-12s %-10s %-8s %-70s ║\n",
			truncateStr(a.Component, 12),
			truncateStr(pipeline, 10),
			string(a.ProfileType),
			topFunc)
	}

	fmt.Printf("╚══════════════════════════════════════════════════════════════════════════════════════════════════════════╝\n")
}
