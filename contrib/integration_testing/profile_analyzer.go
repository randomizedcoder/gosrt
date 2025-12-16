package main

import (
	"bytes"
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
func AnalyzeProfile(profilePath string, outputDir string) (*ProfileAnalysis, error) {
	if _, err := os.Stat(profilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("profile file not found: %s", profilePath)
	}

	base := filepath.Base(profilePath)
	parts := strings.Split(strings.TrimSuffix(base, ".pprof"), "_")

	analysis := &ProfileAnalysis{
		FilePath: profilePath,
	}

	// Parse filename: {pipeline}_{component}_{profile}.pprof
	// e.g., baseline_server_cpu.pprof or server_cpu.pprof
	if len(parts) >= 3 {
		analysis.Pipeline = parts[0]
		analysis.Component = parts[1]
		analysis.ProfileType = ProfileType(parts[2])
	} else if len(parts) >= 2 {
		analysis.Component = parts[0]
		analysis.ProfileType = ProfileType(parts[1])
	} else if len(parts) == 1 {
		// Just the profile type (e.g., cpu.pprof)
		analysis.ProfileType = ProfileType(parts[0])
	}

	// Generate top output with different flags based on profile type
	topArgs := getTopArgs(analysis.ProfileType)
	topPath := strings.TrimSuffix(profilePath, ".pprof") + "_top.txt"
	topCmd := exec.Command("go", append([]string{"tool", "pprof"}, append(topArgs, profilePath)...)...)
	topOutput, err := topCmd.Output()
	if err != nil {
		// Try to get stderr for debugging
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("pprof -top failed: %w\nstderr: %s", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("pprof -top failed: %w", err)
	}
	analysis.TopOutput = string(topOutput)
	if err := os.WriteFile(topPath, topOutput, 0644); err != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to write top output to %s: %v\n", topPath, err)
	}

	// Generate flame graph SVG (optional - may fail if graphviz not installed)
	svgPath := strings.TrimSuffix(profilePath, ".pprof") + "_flame.svg"
	svgCmd := exec.Command("go", "tool", "pprof", "-svg", profilePath)
	svgOutput, err := svgCmd.Output()
	if err == nil {
		if err := os.WriteFile(svgPath, svgOutput, 0644); err == nil {
			analysis.FlameGraph = svgPath
		}
	}

	// Parse top functions
	analysis.TopFuncs = parseTopOutput(string(topOutput), analysis.ProfileType)

	// Extract aggregate metrics
	analysis.extractAggregates()

	return analysis, nil
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
	v, _ := strconv.ParseFloat(s, 64)
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

		if inBase && inHP {
			comp.BaselineValue = baseStat.Flat
			comp.HighPerfValue = hpStat.Flat
			comp.Delta = hpStat.Flat - baseStat.Flat
			if baseStat.Flat > 0 {
				comp.DeltaPercent = (comp.Delta / baseStat.Flat) * 100
			}
		} else if inBase {
			comp.BaselineValue = baseStat.Flat
			comp.IsRemoved = true
			comp.Delta = -baseStat.Flat
			comp.DeltaPercent = -100
		} else {
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
				truncateStr(c.FuncName, 30), c.BaselineValue, c.HighPerfValue, -c.DeltaPercent)
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

	fmt.Fprintf(&buf, "\n╔═══════════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(&buf, "║ %s %s COMPARISON                                          \n",
		strings.ToUpper(r.Component), strings.ToUpper(string(r.ProfileType)))
	fmt.Fprintf(&buf, "╠═══════════════════════════════════════════════════════════════════╣\n")

	fmt.Fprintf(&buf, "║ %-35s %10s %10s %10s ║\n", "Function", "Baseline", "HighPerf", "Delta")
	fmt.Fprintf(&buf, "║ %s ║\n", strings.Repeat("─", 65))

	// Show top 5 comparisons
	for i, c := range r.FuncComparisons {
		if i >= 5 {
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

		fmt.Fprintf(&buf, "║ %-35s %9.1f%% %9.1f%% %9s%s ║\n",
			truncateStr(c.FuncName, 35), c.BaselineValue, c.HighPerfValue, deltaStr, indicator)
	}

	fmt.Fprintf(&buf, "╠═══════════════════════════════════════════════════════════════════╣\n")
	fmt.Fprintf(&buf, "║ SUMMARY: %s", r.Summary)

	if len(r.Recommendations) > 0 {
		fmt.Fprintf(&buf, "╠═══════════════════════════════════════════════════════════════════╣\n")
		fmt.Fprintf(&buf, "║ RECOMMENDATIONS:                                                  ║\n")
		for _, rec := range r.Recommendations {
			fmt.Fprintf(&buf, "║ • %-63s ║\n", truncateStr(rec, 63))
		}
	}

	fmt.Fprintf(&buf, "╚═══════════════════════════════════════════════════════════════════╝\n")

	return buf.String()
}

// AnalyzeAllProfiles analyzes all profiles in a directory
func AnalyzeAllProfiles(profileDir string) ([]*ProfileAnalysis, error) {
	var analyses []*ProfileAnalysis

	err := filepath.Walk(profileDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".pprof") {
			return nil
		}

		analysis, err := AnalyzeProfile(path, profileDir)
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

	fmt.Printf("\n╔═══════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  PROFILE ANALYSIS SUMMARY                                             ║\n")
	fmt.Printf("╠═══════════════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║ %-15s %-10s %-10s %-35s ║\n", "Component", "Pipeline", "Type", "Top Function")
	fmt.Printf("║ %s ║\n", strings.Repeat("─", 69))

	for _, a := range analyses {
		topFunc := "(none)"
		if len(a.TopFuncs) > 0 {
			topFunc = truncateStr(a.TopFuncs[0].Name, 35)
		}
		pipeline := a.Pipeline
		if pipeline == "" {
			pipeline = "-"
		}
		fmt.Printf("║ %-15s %-10s %-10s %-35s ║\n",
			truncateStr(a.Component, 15),
			truncateStr(pipeline, 10),
			string(a.ProfileType),
			topFunc)
	}

	fmt.Printf("╚═══════════════════════════════════════════════════════════════════════╝\n")
}
