// metrics-audit is a static analysis tool that verifies alignment between:
// 1. Metrics defined in ConnectionMetrics struct (metrics/metrics.go)
// 2. Metrics defined in ListenerMetrics struct (metrics/listener_metrics.go)
// 3. Metrics actually incremented via .Add()/.Store() calls
// 4. Metrics exported to Prometheus via .Load() calls (metrics/handler.go)
//
// Additionally, it performs mutual exclusion analysis to distinguish between:
// - True double-counting bugs (same metric incremented from code paths that run together)
// - Expected behavior (same metric incremented from mutually exclusive code paths)
//
// Usage:
//
//	go run tools/metrics-audit/main.go
//	make audit-metrics
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MetricField represents a field in the ConnectionMetrics struct
type MetricField struct {
	Name        string
	Type        string
	IsCommented bool
	Line        int
}

// IncrementLocation tracks where a metric is incremented
type IncrementLocation struct {
	File     string
	Line     int
	Method   string // "Add" or "Store"
	Function string // Enclosing function name
	Group    string // Assigned mutual exclusion group (if any)
}

// MutualExclusionConfig represents the YAML configuration
type MutualExclusionConfig struct {
	Groups           map[string]GroupDef   `yaml:"groups"`
	MutualExclusion  [][]string            `yaml:"mutual_exclusion"`
	SeparatePrograms []string              `yaml:"separate_programs"`
	KnownPatterns    []KnownPattern        `yaml:"known_patterns"`
}

// GroupDef defines a mutual exclusion group
type GroupDef struct {
	Description string   `yaml:"description"`
	Files       []string `yaml:"files"`
	Functions   []string `yaml:"functions"`
}

// KnownPattern documents an acceptable multi-increment pattern
type KnownPattern struct {
	Metric string `yaml:"metric"`
	Reason string `yaml:"reason"`
}

// MultiIncrementAnalysis holds the analysis result for a metric with multiple increments
type MultiIncrementAnalysis struct {
	Metric           string
	Locations        []IncrementLocation
	InSeparateProgs  []IncrementLocation // In contrib/test programs
	InLibrary        []IncrementLocation // In main library
	GroupedLocations map[string][]IncrementLocation // By group name
	UngroupedLocs    []IncrementLocation
	IsMutuallyExclusive bool
	KnownPattern     *KnownPattern
	Violations       []string
}

func main() {
	// Find the project root (where metrics/ directory is)
	root := findProjectRoot()
	if root == "" {
		fmt.Println("ERROR: Could not find project root (looking for metrics/metrics.go)")
		os.Exit(2)
	}

	fmt.Println("=== GoSRT Metrics Audit ===")
	fmt.Printf("Project root: %s\n\n", root)

	// Load mutual exclusion config
	config := loadMutualExclusionConfig(root)

	// Phase 1a: Parse metrics.go for ConnectionMetrics struct fields
	fmt.Println("Phase 1a: Parsing metrics/metrics.go for ConnectionMetrics fields...")
	metricsInStruct, commentedOut := parseMetricsStruct(filepath.Join(root, "metrics/metrics.go"), "ConnectionMetrics")
	fmt.Printf("  Found %d atomic fields in ConnectionMetrics\n", len(metricsInStruct))
	fmt.Printf("  Found %d commented-out fields\n\n", len(commentedOut))

	// Phase 1b: Parse listener_metrics.go for ListenerMetrics struct fields
	fmt.Println("Phase 1b: Parsing metrics/listener_metrics.go for ListenerMetrics fields...")
	listenerMetricsFile := filepath.Join(root, "metrics/listener_metrics.go")
	listenerMetricsInStruct := make(map[string]bool)
	listenerCommentedOut := make(map[string]bool)
	if _, err := os.Stat(listenerMetricsFile); err == nil {
		listenerMetricsInStruct, listenerCommentedOut = parseMetricsStruct(listenerMetricsFile, "ListenerMetrics")
		fmt.Printf("  Found %d atomic fields in ListenerMetrics\n", len(listenerMetricsInStruct))
		fmt.Printf("  Found %d commented-out fields\n\n", len(listenerCommentedOut))
	} else {
		fmt.Println("  (metrics/listener_metrics.go not found - skipping)")
	}

	// Merge listener metrics into main tracking maps
	for name := range listenerMetricsInStruct {
		metricsInStruct[name] = true
	}
	for name := range listenerCommentedOut {
		commentedOut[name] = true
	}

	// Phase 2: Scan codebase for .Add()/.Store() calls
	fmt.Println("Phase 2: Scanning codebase for .Add()/.Store() calls...")
	metricsInUse, fileCount := findIncrementCalls(root, config)
	fmt.Printf("  Found %d unique fields being incremented\n", len(metricsInUse))
	fmt.Printf("  Scanned %d .go files\n\n", fileCount)

	// Phase 3: Parse handler.go for .Load() calls
	fmt.Println("Phase 3: Parsing metrics/handler.go for .Load() calls...")
	metricsInPrometheus := findExportedMetrics(filepath.Join(root, "metrics/handler.go"))
	fmt.Printf("  Found %d fields being exported to Prometheus\n\n", len(metricsInPrometheus))

	// Phase 4: Compare and report
	fmt.Println("=== Results ===")
	fmt.Println()

	// Find metrics that are used but not exported (the critical issue)
	var missingExports []string
	for name := range metricsInUse {
		if !metricsInPrometheus[name] {
			// Check if it's a known metric (in struct)
			if metricsInStruct[name] {
				missingExports = append(missingExports, name)
			}
		}
	}
	sort.Strings(missingExports)

	// Find metrics defined but never used
	var neverUsed []string
	for name := range metricsInStruct {
		if _, used := metricsInUse[name]; !used {
			neverUsed = append(neverUsed, name)
		}
	}
	sort.Strings(neverUsed)

	// Find fully aligned metrics
	var fullyAligned []string
	for name := range metricsInStruct {
		if _, used := metricsInUse[name]; used {
			if metricsInPrometheus[name] {
				fullyAligned = append(fullyAligned, name)
			}
		}
	}

	// Report fully aligned
	fmt.Printf("✅ Fully Aligned (defined, used, exported): %d fields\n\n", len(fullyAligned))

	// Report never used (usually commented out - OK)
	fmt.Printf("⚠️  Defined but never used: %d fields\n", len(neverUsed))
	if len(neverUsed) > 0 {
		for _, name := range neverUsed {
			status := ""
			if commentedOut[name] {
				status = " (commented out - OK)"
			}
			fmt.Printf("   - %s%s\n", name, status)
		}
	}
	fmt.Println()

	// Report missing exports (the critical issue)
	fmt.Printf("❌ Used but NOT exported to Prometheus: %d fields\n", len(missingExports))
	if len(missingExports) > 0 {
		for _, name := range missingExports {
			fmt.Printf("   - %s\n", name)
			if locs, ok := metricsInUse[name]; ok {
				for _, loc := range locs {
					fmt.Printf("       %s:%d (.%s)\n", loc.File, loc.Line, loc.Method)
				}
			}
		}
	}
	fmt.Println()

	// Phase 5: Analyze multiple increment locations with mutual exclusion
	fmt.Println("=== Multiple Increment Analysis ===")
	fmt.Println()

	analyses := analyzeMultipleIncrements(metricsInUse, config)

	// Categorize results
	var mutuallyExclusive []MultiIncrementAnalysis
	var separatePrograms []MultiIncrementAnalysis
	var knownPatterns []MultiIncrementAnalysis
	var potentialIssues []MultiIncrementAnalysis

	for _, a := range analyses {
		if a.KnownPattern != nil {
			knownPatterns = append(knownPatterns, a)
		} else if len(a.InLibrary) == 0 && len(a.InSeparateProgs) > 0 {
			// Only in contrib programs
			separatePrograms = append(separatePrograms, a)
		} else if len(a.InSeparateProgs) > 0 && len(a.InLibrary) > 0 {
			// Split between programs - OK
			separatePrograms = append(separatePrograms, a)
		} else if a.IsMutuallyExclusive {
			mutuallyExclusive = append(mutuallyExclusive, a)
		} else {
			potentialIssues = append(potentialIssues, a)
		}
	}

	// Report mutually exclusive (OK)
	fmt.Printf("✅ Mutually Exclusive Code Paths (OK): %d fields\n", len(mutuallyExclusive))
	if len(mutuallyExclusive) > 0 {
		for _, a := range mutuallyExclusive {
			groups := getGroupNames(a.GroupedLocations)
			fmt.Printf("   - %s (%d locations in %s)\n", a.Metric, len(a.Locations), strings.Join(groups, "/"))
		}
	}
	fmt.Println()

	// Report separate programs (OK)
	fmt.Printf("✅ Separate Programs (OK): %d fields\n", len(separatePrograms))
	if len(separatePrograms) > 0 {
		for _, a := range separatePrograms {
			fmt.Printf("   - %s (library: %d, contrib: %d)\n",
				a.Metric, len(a.InLibrary), len(a.InSeparateProgs))
		}
	}
	fmt.Println()

	// Report known patterns (OK)
	fmt.Printf("✅ Known Patterns (documented): %d fields\n", len(knownPatterns))
	if len(knownPatterns) > 0 {
		for _, a := range knownPatterns {
			fmt.Printf("   - %s: %s\n", a.Metric, a.KnownPattern.Reason)
		}
	}
	fmt.Println()

	// Report potential issues (needs review)
	fmt.Printf("⚠️  Potential Double-Counting (review): %d fields\n", len(potentialIssues))
	if len(potentialIssues) > 0 {
		for _, a := range potentialIssues {
			fmt.Printf("   - %s (%d locations):\n", a.Metric, len(a.Locations))
			for _, v := range a.Violations {
				fmt.Printf("       ⚠️  %s\n", v)
			}
			for _, loc := range a.Locations {
				groupInfo := ""
				if loc.Group != "" {
					groupInfo = fmt.Sprintf(" [%s]", loc.Group)
				}
				funcInfo := ""
				if loc.Function != "" {
					funcInfo = fmt.Sprintf(" in %s()", loc.Function)
				}
				fmt.Printf("       %s:%d (.%s)%s%s\n", loc.File, loc.Line, loc.Method, funcInfo, groupInfo)
			}
		}
	}
	fmt.Println()

	// Summary
	fmt.Println("=== Summary ===")
	if len(missingExports) == 0 && len(neverUsed) == 0 {
		fmt.Println("✅ AUDIT PASSED: All used metrics are exported to Prometheus")
		if len(potentialIssues) > 0 {
			fmt.Printf("⚠️  WARNING: %d metrics may have potential double-counting - review above\n", len(potentialIssues))
		} else if len(analyses) > 0 {
			fmt.Printf("✅ All %d multi-location metrics verified as mutually exclusive or documented\n", len(analyses))
		}
		os.Exit(0)
	} else if len(missingExports) > 0 {
		fmt.Printf("❌ AUDIT FAILED: %d metrics need to be added to Prometheus handler\n", len(missingExports))
		os.Exit(1)
	} else {
		fmt.Printf("⚠️  AUDIT WARNING: %d metrics defined but never used\n", len(neverUsed))
		os.Exit(0)
	}
}

// loadMutualExclusionConfig loads the YAML configuration file
func loadMutualExclusionConfig(root string) *MutualExclusionConfig {
	configPath := filepath.Join(root, "tools/metrics-audit/mutual_exclusion.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Printf("  (No mutual_exclusion.yaml found - skipping exclusion analysis)\n\n")
		return &MutualExclusionConfig{}
	}

	var config MutualExclusionConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		fmt.Printf("  WARNING: Could not parse mutual_exclusion.yaml: %v\n\n", err)
		return &MutualExclusionConfig{}
	}

	groupCount := len(config.Groups)
	ruleCount := len(config.MutualExclusion)
	fmt.Printf("Phase 0: Loaded mutual exclusion config (%d groups, %d rules)\n\n", groupCount, ruleCount)

	return &config
}

// findProjectRoot looks for the directory containing metrics/metrics.go
func findProjectRoot() string {
	// Try current directory first
	if _, err := os.Stat("metrics/metrics.go"); err == nil {
		return "."
	}

	// Try parent directories
	dir, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "metrics/metrics.go")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}

	return ""
}

// parseMetricsStruct extracts all atomic fields from a metrics struct
// structName: name of the struct to parse (e.g., "ConnectionMetrics", "ListenerMetrics")
// Returns: (active fields, commented-out fields)
func parseMetricsStruct(path string, structName string) (map[string]bool, map[string]bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		fmt.Printf("ERROR: Could not parse %s: %v\n", path, err)
		os.Exit(2)
	}

	activeMetrics := make(map[string]bool)
	commentedMetrics := make(map[string]bool)

	// First, find commented-out field definitions in comments
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			text := c.Text
			// Look for patterns like: // FieldName atomic.Uint64
			if strings.Contains(text, "atomic.Uint64") || strings.Contains(text, "atomic.Int64") {
				// Extract field name - it's the first word after //
				text = strings.TrimPrefix(text, "//")
				text = strings.TrimSpace(text)
				parts := strings.Fields(text)
				if len(parts) >= 2 {
					fieldName := parts[0]
					// Skip if it looks like a comment, not a field
					if !strings.HasPrefix(fieldName, "Not") &&
						!strings.HasPrefix(fieldName, "TODO") &&
						!strings.HasPrefix(fieldName, "Note") &&
						len(fieldName) > 3 {
						commentedMetrics[fieldName] = true
					}
				}
			}
		}
	}

	// Now find active fields in the struct
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != structName {
			return true
		}

		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}

		for _, field := range st.Fields.List {
			typeStr := exprToString(field.Type)
			if strings.Contains(typeStr, "atomic.Uint64") || strings.Contains(typeStr, "atomic.Int64") {
				for _, name := range field.Names {
					activeMetrics[name.Name] = true
				}
			}
		}
		return false
	})

	return activeMetrics, commentedMetrics
}

// findIncrementCalls finds all .Add() and .Store() calls on metric fields
// Returns: (map of field name -> locations, file count)
func findIncrementCalls(rootDir string, config *MutualExclusionConfig) (map[string][]IncrementLocation, int) {
	increments := make(map[string][]IncrementLocation)
	fileCount := 0

	filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		// Skip non-Go files
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Skip vendor, tools, and test files
		if strings.Contains(path, "vendor/") ||
			strings.Contains(path, "tools/") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fileCount++

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil // Skip files that don't parse
		}

		// Build a map of positions to enclosing function names
		funcMap := buildFunctionMap(f, fset)

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			// Check if it's a method call: X.Method()
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			// Is method Add or Store?
			method := sel.Sel.Name
			if method != "Add" && method != "Store" {
				return true
			}

			// Extract the field name from: something.FieldName.Add()
			fieldSel, ok := sel.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			fieldName := fieldSel.Sel.Name

			// Filter: only include fields that look like metrics (start with capital)
			if len(fieldName) == 0 || fieldName[0] < 'A' || fieldName[0] > 'Z' {
				return true
			}

			pos := fset.Position(n.Pos())
			relPath, _ := filepath.Rel(rootDir, path)
			if relPath == "" {
				relPath = path
			}

			// Find enclosing function
			funcName := findEnclosingFunction(funcMap, pos.Line)

			// Determine group
			group := findGroup(relPath, funcName, config)

			increments[fieldName] = append(increments[fieldName], IncrementLocation{
				File:     relPath,
				Line:     pos.Line,
				Method:   method,
				Function: funcName,
				Group:    group,
			})

			return true
		})
		return nil
	})

	return increments, fileCount
}

// buildFunctionMap creates a map of line ranges to function names
func buildFunctionMap(f *ast.File, fset *token.FileSet) map[int]string {
	funcMap := make(map[int]string)

	ast.Inspect(f, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				startLine := fset.Position(fn.Body.Pos()).Line
				endLine := fset.Position(fn.Body.End()).Line
				funcName := fn.Name.Name
				// For methods, include receiver type
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					if t := fn.Recv.List[0].Type; t != nil {
						// funcName is already the method name
					}
				}
				for line := startLine; line <= endLine; line++ {
					funcMap[line] = funcName
				}
			}
		}
		return true
	})

	return funcMap
}

// findEnclosingFunction finds the function name for a given line
func findEnclosingFunction(funcMap map[int]string, line int) string {
	if name, ok := funcMap[line]; ok {
		return name
	}
	return ""
}

// findGroup determines which mutual exclusion group a location belongs to
func findGroup(file, funcName string, config *MutualExclusionConfig) string {
	if config == nil {
		return ""
	}

	for groupName, groupDef := range config.Groups {
		// Check file match
		for _, f := range groupDef.Files {
			if strings.Contains(file, f) {
				return groupName
			}
		}

		// Check function match
		for _, fn := range groupDef.Functions {
			if funcName == fn {
				return groupName
			}
		}
	}

	return ""
}

// isSeparateProgram checks if a file path is in a separate program
func isSeparateProgram(file string, config *MutualExclusionConfig) bool {
	if config == nil {
		return false
	}

	for _, prefix := range config.SeparatePrograms {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return false
}

// areMutuallyExclusive checks if two groups are in the same mutual exclusion rule
func areMutuallyExclusive(group1, group2 string, config *MutualExclusionConfig) bool {
	if config == nil || group1 == "" || group2 == "" {
		return false
	}

	for _, rule := range config.MutualExclusion {
		hasGroup1 := false
		hasGroup2 := false
		for _, g := range rule {
			if g == group1 {
				hasGroup1 = true
			}
			if g == group2 {
				hasGroup2 = true
			}
		}
		if hasGroup1 && hasGroup2 {
			return true
		}
	}

	return false
}

// findKnownPattern checks if a metric has a documented known pattern
func findKnownPattern(metric string, config *MutualExclusionConfig) *KnownPattern {
	if config == nil {
		return nil
	}

	for i := range config.KnownPatterns {
		if config.KnownPatterns[i].Metric == metric {
			return &config.KnownPatterns[i]
		}
	}
	return nil
}

// analyzeMultipleIncrements analyzes all metrics with multiple increment locations
func analyzeMultipleIncrements(metricsInUse map[string][]IncrementLocation, config *MutualExclusionConfig) []MultiIncrementAnalysis {
	var analyses []MultiIncrementAnalysis

	// Get sorted metric names for consistent output
	var metricNames []string
	for name, locs := range metricsInUse {
		if len(locs) > 1 {
			metricNames = append(metricNames, name)
		}
	}
	sort.Strings(metricNames)

	for _, name := range metricNames {
		locs := metricsInUse[name]

		analysis := MultiIncrementAnalysis{
			Metric:           name,
			Locations:        locs,
			GroupedLocations: make(map[string][]IncrementLocation),
			KnownPattern:     findKnownPattern(name, config),
		}

		// Separate into library vs contrib programs
		for _, loc := range locs {
			if isSeparateProgram(loc.File, config) {
				analysis.InSeparateProgs = append(analysis.InSeparateProgs, loc)
			} else {
				analysis.InLibrary = append(analysis.InLibrary, loc)
			}

			// Group by exclusion group
			if loc.Group != "" {
				analysis.GroupedLocations[loc.Group] = append(analysis.GroupedLocations[loc.Group], loc)
			} else {
				analysis.UngroupedLocs = append(analysis.UngroupedLocs, loc)
			}
		}

		// Analyze mutual exclusion for library locations only
		analysis.IsMutuallyExclusive = analyzeExclusivity(analysis.InLibrary, config)

		// Generate violations if not mutually exclusive and not a known pattern
		if !analysis.IsMutuallyExclusive && analysis.KnownPattern == nil && len(analysis.InLibrary) > 1 {
			analysis.Violations = generateViolations(analysis, config)
		}

		analyses = append(analyses, analysis)
	}

	return analyses
}

// analyzeExclusivity determines if all locations are in mutually exclusive groups
func analyzeExclusivity(locs []IncrementLocation, config *MutualExclusionConfig) bool {
	if len(locs) <= 1 {
		return true
	}

	// Get unique groups
	groups := make(map[string]bool)
	for _, loc := range locs {
		if loc.Group != "" {
			groups[loc.Group] = true
		}
	}

	// If all locations are in the same group, they're OK (different functions in same mode)
	if len(groups) == 1 {
		return true
	}

	// Check if all groups are mutually exclusive with each other
	groupList := make([]string, 0, len(groups))
	for g := range groups {
		groupList = append(groupList, g)
	}

	for i := 0; i < len(groupList); i++ {
		for j := i + 1; j < len(groupList); j++ {
			if !areMutuallyExclusive(groupList[i], groupList[j], config) {
				return false
			}
		}
	}

	return true
}

// generateViolations creates violation messages for potential issues
func generateViolations(analysis MultiIncrementAnalysis, config *MutualExclusionConfig) []string {
	var violations []string

	// Check for ungrouped locations
	if len(analysis.UngroupedLocs) > 0 && len(analysis.GroupedLocations) > 0 {
		violations = append(violations, fmt.Sprintf("%d ungrouped locations mixed with grouped", len(analysis.UngroupedLocs)))
	}

	// Check for multiple ungrouped locations
	if len(analysis.UngroupedLocs) > 1 {
		violations = append(violations, fmt.Sprintf("%d ungrouped locations (no mutual exclusion defined)", len(analysis.UngroupedLocs)))
	}

	// Check for groups that aren't mutually exclusive
	groups := getGroupNames(analysis.GroupedLocations)
	for i := 0; i < len(groups); i++ {
		for j := i + 1; j < len(groups); j++ {
			if !areMutuallyExclusive(groups[i], groups[j], config) {
				violations = append(violations, fmt.Sprintf("groups '%s' and '%s' are not mutually exclusive", groups[i], groups[j]))
			}
		}
	}

	if len(violations) == 0 {
		violations = append(violations, "multiple locations without clear exclusion pattern")
	}

	return violations
}

// getGroupNames extracts sorted group names from grouped locations
func getGroupNames(grouped map[string][]IncrementLocation) []string {
	var names []string
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// findExportedMetrics finds all .Load() calls and getter method calls in the Prometheus handler
func findExportedMetrics(path string) map[string]bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		fmt.Printf("ERROR: Could not parse %s: %v\n", path, err)
		os.Exit(2)
	}

	exported := make(map[string]bool)

	// Map of getter method names to the underlying field names they access
	// These are methods on ConnectionMetrics that call .Load() internally
	getterToField := map[string]string{
		"GetRecvRatePacketsPerSec":  "RecvRatePacketsPerSec",
		"GetRecvRateBytesPerSec":    "RecvRateBytesPerSec",
		"GetRecvRateMbps":           "RecvRateBytesPerSec", // Derived from bytes/sec
		"GetRecvRateRetransPercent": "RecvRatePktRetransRate",
		"GetSendRateEstInputBW":     "SendRateEstInputBW",
		"GetSendRateEstSentBW":      "SendRateEstSentBW",
		"GetSendRateMbps":           "SendRateEstSentBW", // Derived from sent BW
		"GetSendRateRetransPercent": "SendRatePktRetransRate",
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		methodName := sel.Sel.Name

		// Check for getter method calls: metrics.GetXxx()
		if fieldName, isGetter := getterToField[methodName]; isGetter {
			exported[fieldName] = true
			return true
		}

		// Look for .Load() calls
		if methodName != "Load" {
			return true
		}

		// Extract field name from metrics.FieldName.Load()
		fieldSel, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		fieldName := fieldSel.Sel.Name

		// Filter: only include fields that look like metrics
		if len(fieldName) > 0 && fieldName[0] >= 'A' && fieldName[0] <= 'Z' {
			exported[fieldName] = true
		}

		return true
	})

	return exported
}

// exprToString converts an AST expression to a string representation
func exprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(t.X)
	default:
		return ""
	}
}
