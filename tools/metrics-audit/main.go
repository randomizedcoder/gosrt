// metrics-audit is a static analysis tool that verifies alignment between:
// 1. Metrics defined in ConnectionMetrics struct (metrics/metrics.go)
// 2. Metrics defined in ListenerMetrics struct (metrics/listener_metrics.go)
// 3. Metrics actually incremented via .Add()/.Store() calls
// 4. Metrics exported to Prometheus via .Load() calls (metrics/handler.go)
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
	File   string
	Line   int
	Method string // "Add" or "Store"
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
	metricsInUse, fileCount := findIncrementCalls(root)
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

	// Find metrics with multiple increments (potential double-counting)
	var multipleIncrements []string
	for name, locs := range metricsInUse {
		if len(locs) > 1 {
			multipleIncrements = append(multipleIncrements, name)
		}
	}
	sort.Strings(multipleIncrements)

	fmt.Printf("⚠️  Multiple increment locations (review for double-counting): %d fields\n", len(multipleIncrements))
	if len(multipleIncrements) > 0 {
		for _, name := range multipleIncrements {
			locs := metricsInUse[name]
			fmt.Printf("   - %s (%d locations):\n", name, len(locs))
			for _, loc := range locs {
				fmt.Printf("       %s:%d (.%s)\n", loc.File, loc.Line, loc.Method)
			}
		}
	}
	fmt.Println()

	// Summary
	fmt.Println("=== Summary ===")
	if len(missingExports) == 0 && len(neverUsed) == 0 {
		fmt.Println("✅ AUDIT PASSED: All used metrics are exported to Prometheus")
		if len(multipleIncrements) > 0 {
			fmt.Printf("⚠️  WARNING: %d metrics have multiple increment locations - review for potential double-counting\n", len(multipleIncrements))
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
func findIncrementCalls(rootDir string) (map[string][]IncrementLocation, int) {
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

			increments[fieldName] = append(increments[fieldName], IncrementLocation{
				File:   relPath,
				Line:   pos.Line,
				Method: method,
			})

			return true
		})
		return nil
	})

	return increments, fileCount
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
