// code-audit is a unified AST analysis tool for comprehensive code quality.
//
// This tool consolidates three specialized analyzers:
//   - seq:     Detect 31-bit sequence arithmetic bugs (int32(uint32-uint32))
//   - test:    Analyze test struct fields and coverage
//   - metrics: Verify Prometheus metrics alignment
//
// Usage:
//
//	code-audit [mode] [options] [packages/files...]
//
// Modes:
//
//	all       Run all analyses (default)
//	seq       Sequence arithmetic analysis only
//	test      Test structure analysis only
//	metrics   Prometheus metrics analysis only
//	coverage  Code coverage analysis
//
// Examples:
//
//	code-audit                               # Run all analyses
//	code-audit seq ./congestion/live         # Sequence audit only
//	code-audit test -file loss_recovery_table_test.go classify
//	code-audit metrics                       # Metrics audit only
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// =============================================================================
// Common Data Structures
// =============================================================================

// Finding represents a potential issue found in the code
type Finding struct {
	File       string
	Line       int
	Column     int
	Severity   string // "HIGH", "MEDIUM", "LOW", "INFO"
	Category   string // "seq", "test", "metrics"
	Pattern    string
	TypeInfo   string
	Context    string
	Suggestion string
}

// AuditResult holds the results of all analyses
type AuditResult struct {
	SeqFindings     []Finding
	TestIssues      []TestIssue
	MetricIssues    []MetricIssue
	HasHighSeverity bool
	Summary         AuditSummary
}

// AuditSummary provides high-level metrics
type AuditSummary struct {
	SeqHigh      int
	SeqMedium    int
	TestGaps     int
	MetricGaps   int
	TotalTests   int
	TotalCovered int
}

// TestIssue represents a test coverage issue
type TestIssue struct {
	File        string
	StructName  string
	FieldName   string
	IssueType   string // "missing_corner", "unclassified", "no_table"
	Description string
	Severity    string
}

// MetricIssue represents a metrics alignment issue
type MetricIssue struct {
	MetricName  string
	IssueType   string // "not_exported", "never_used", "multiple_increments"
	Description string
	Locations   []string
	Severity    string
}

// =============================================================================
// Main Entry Point
// =============================================================================

func main() {
	// Define flags
	var (
		fileFlag    string
		dirFlag     string
		verboseFlag bool
		testsFlag   bool
	)

	flag.StringVar(&fileFlag, "file", "", "Specific file to analyze")
	flag.StringVar(&dirFlag, "dir", "", "Directory to analyze")
	flag.BoolVar(&verboseFlag, "verbose", false, "Verbose output")
	flag.BoolVar(&testsFlag, "tests", false, "Include test files in sequence analysis")
	flag.Parse()

	// Get mode from positional args
	mode := "all"
	args := flag.Args()
	if len(args) > 0 {
		mode = args[0]
		args = args[1:]
	}

	// Find project root
	root := findProjectRoot()
	if root == "" {
		fmt.Fprintln(os.Stderr, "ERROR: Could not find project root")
		os.Exit(1)
	}

	result := &AuditResult{}

	// Execute based on mode
	switch mode {
	case "all":
		runAllAnalyses(root, result, verboseFlag, testsFlag)
	case "seq":
		targets := []string{"./congestion/live", "./circular"}
		if len(args) > 0 {
			targets = args
		}
		runSeqAnalysis(targets, result, verboseFlag, testsFlag)
	case "test":
		subMode := "audit"
		if len(args) > 0 {
			subMode = args[0]
		}
		runTestAnalysis(root, fileFlag, dirFlag, subMode, result, verboseFlag)
	case "metrics":
		runMetricsAnalysis(root, result, verboseFlag)
	case "coverage":
		runCoverageAnalysis(root, result, verboseFlag)
	default:
		fmt.Fprintf(os.Stderr, "ERROR: Unknown mode '%s'\n", mode)
		fmt.Fprintln(os.Stderr, "Valid modes: all, seq, test, metrics, coverage")
		os.Exit(1)
	}

	// Print summary and exit
	printSummary(result)
	if result.HasHighSeverity {
		os.Exit(1)
	}
}

// =============================================================================
// All Analyses
// =============================================================================

func runAllAnalyses(root string, result *AuditResult, verbose, includeTests bool) {
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Println("           CODE-AUDIT: Comprehensive Code Quality Analysis")
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Printf("Project root: %s\n\n", root)

	// 1. Sequence analysis
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│  PHASE 1: Sequence Arithmetic Analysis                          │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	runSeqAnalysis([]string{"./congestion/live", "./circular"}, result, verbose, includeTests)
	fmt.Println()

	// 2. Metrics analysis
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│  PHASE 2: Prometheus Metrics Analysis                           │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	runMetricsAnalysis(root, result, verbose)
	fmt.Println()

	// 3. Test analysis summary
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│  PHASE 3: Test Coverage Analysis                                │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	runTestAnalysis(root, "", "congestion/live", "summary", result, verbose)
}

// =============================================================================
// Sequence Analysis (from seq-audit)
// =============================================================================

func runSeqAnalysis(patterns []string, result *AuditResult, verbose, includeTests bool) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedCompiledGoFiles,
		Tests: includeTests,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading packages: %v\n", err)
		return
	}

	var findings []Finding
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			continue
		}
		findings = append(findings, analyzePackageSeq(pkg)...)
	}

	// Sort by severity
	sort.Slice(findings, func(i, j int) bool {
		sevOrder := map[string]int{"HIGH": 0, "MEDIUM": 1, "LOW": 2, "INFO": 3}
		return sevOrder[findings[i].Severity] < sevOrder[findings[j].Severity]
	})

	result.SeqFindings = findings

	// Count severities
	for _, f := range findings {
		switch f.Severity {
		case "HIGH":
			result.Summary.SeqHigh++
			result.HasHighSeverity = true
		case "MEDIUM":
			result.Summary.SeqMedium++
		}
	}

	// Print findings
	if len(findings) == 0 {
		fmt.Println("  ✅ No sequence arithmetic issues found")
	} else {
		fmt.Printf("  Found %d issues (%d HIGH, %d MEDIUM)\n",
			len(findings), result.Summary.SeqHigh, result.Summary.SeqMedium)

		for _, f := range findings {
			if f.Severity == "HIGH" || f.Severity == "MEDIUM" || verbose {
				icon := map[string]string{"HIGH": "🔴", "MEDIUM": "🟠", "LOW": "🟡"}[f.Severity]
				fmt.Printf("  %s [%s:%d] %s\n", icon, shortPath(f.File), f.Line, f.Pattern)
				if verbose {
					fmt.Printf("     💡 %s\n", f.Suggestion)
				}
			}
		}
	}
}

func analyzePackageSeq(pkg *packages.Package) []Finding {
	var findings []Finding

	for i, file := range pkg.Syntax {
		if i >= len(pkg.CompiledGoFiles) {
			continue
		}
		filename := pkg.CompiledGoFiles[i]

		analyzer := &SeqAnalyzer{
			pkg:      pkg,
			fset:     pkg.Fset,
			info:     pkg.TypesInfo,
			filename: filename,
			findings: &findings,
		}
		ast.Walk(analyzer, file)
	}

	return findings
}

type SeqAnalyzer struct {
	pkg         *packages.Package
	fset        *token.FileSet
	info        *types.Info
	filename    string
	findings    *[]Finding
	currentFunc string
}

func (a *SeqAnalyzer) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}

	switch node := n.(type) {
	case *ast.FuncDecl:
		a.currentFunc = node.Name.Name
	case *ast.CallExpr:
		a.checkTypeConversion(node)
	}

	return a
}

func (a *SeqAnalyzer) checkTypeConversion(call *ast.CallExpr) {
	convType := a.info.TypeOf(call.Fun)
	if convType == nil {
		return
	}

	basic, ok := convType.(*types.Basic)
	if !ok || basic.Kind() != types.Int32 {
		return
	}

	if len(call.Args) != 1 {
		return
	}

	binExpr, ok := call.Args[0].(*ast.BinaryExpr)
	if !ok || binExpr.Op != token.SUB {
		return
	}

	leftType := a.info.TypeOf(binExpr.X)
	rightType := a.info.TypeOf(binExpr.Y)

	if leftType == nil || rightType == nil {
		return
	}

	leftBasic, leftOk := leftType.Underlying().(*types.Basic)
	rightBasic, rightOk := rightType.Underlying().(*types.Basic)

	if !leftOk || !rightOk {
		return
	}

	if leftBasic.Kind() == types.Uint32 && rightBasic.Kind() == types.Uint32 {
		pos := a.fset.Position(call.Pos())
		*a.findings = append(*a.findings, Finding{
			File:     a.filename,
			Line:     pos.Line,
			Column:   pos.Column,
			Severity: "HIGH",
			Category: "seq",
			Pattern:  fmt.Sprintf("int32(%s - %s)", exprString(binExpr.X), exprString(binExpr.Y)),
			TypeInfo: "int32(uint32 - uint32)",
			Context:  fmt.Sprintf("function %s", a.currentFunc),
			Suggestion: "This pattern fails at 31-bit wraparound. " +
				"Use threshold-based comparison instead.",
		})
	}
}

// =============================================================================
// Metrics Analysis (from metrics-audit)
// =============================================================================

func runMetricsAnalysis(root string, result *AuditResult, verbose bool) {
	// Parse metrics struct
	metricsFile := filepath.Join(root, "metrics/metrics.go")
	metricsInStruct := parseMetricsStruct(metricsFile, "ConnectionMetrics")

	// Also check listener metrics
	listenerFile := filepath.Join(root, "metrics/listener_metrics.go")
	if _, err := os.Stat(listenerFile); err == nil {
		for name := range parseMetricsStruct(listenerFile, "ListenerMetrics") {
			metricsInStruct[name] = true
		}
	}

	// Find increment calls
	metricsInUse := findIncrementCalls(root)

	// Find exported metrics
	handlerFile := filepath.Join(root, "metrics/handler.go")
	metricsExported := findExportedMetrics(handlerFile)

	// Analyze
	var issues []MetricIssue

	// Missing exports
	for name := range metricsInUse {
		if metricsInStruct[name] && !metricsExported[name] {
			issues = append(issues, MetricIssue{
				MetricName:  name,
				IssueType:   "not_exported",
				Description: "Used but not exported to Prometheus",
				Severity:    "HIGH",
			})
			result.HasHighSeverity = true
		}
	}

	// Never used
	for name := range metricsInStruct {
		if _, used := metricsInUse[name]; !used {
			issues = append(issues, MetricIssue{
				MetricName:  name,
				IssueType:   "never_used",
				Description: "Defined but never used",
				Severity:    "LOW",
			})
		}
	}

	result.MetricIssues = issues

	// Count aligned
	aligned := 0
	for name := range metricsInStruct {
		if _, used := metricsInUse[name]; used {
			if metricsExported[name] {
				aligned++
			}
		}
	}

	notExported := 0
	for _, issue := range issues {
		if issue.IssueType == "not_exported" {
			notExported++
		}
	}
	result.Summary.MetricGaps = notExported

	// Print
	if notExported == 0 {
		fmt.Printf("  ✅ All %d used metrics are exported to Prometheus\n", aligned)
	} else {
		fmt.Printf("  ❌ %d metrics NOT exported to Prometheus:\n", notExported)
		for _, issue := range issues {
			if issue.IssueType == "not_exported" {
				fmt.Printf("     - %s\n", issue.MetricName)
			}
		}
	}
}

func parseMetricsStruct(path, structName string) map[string]bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return make(map[string]bool)
	}

	metrics := make(map[string]bool)
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
			typeStr := typeExprString(field.Type)
			if strings.Contains(typeStr, "atomic.Uint64") || strings.Contains(typeStr, "atomic.Int64") {
				for _, name := range field.Names {
					metrics[name.Name] = true
				}
			}
		}
		return false
	})

	return metrics
}

func findIncrementCalls(rootDir string) map[string]bool {
	increments := make(map[string]bool)

	filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") ||
			strings.Contains(path, "vendor/") ||
			strings.Contains(path, "tools/") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
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

			method := sel.Sel.Name
			if method != "Add" && method != "Store" {
				return true
			}

			fieldSel, ok := sel.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			fieldName := fieldSel.Sel.Name
			if len(fieldName) > 0 && fieldName[0] >= 'A' && fieldName[0] <= 'Z' {
				increments[fieldName] = true
			}

			return true
		})
		return nil
	})

	return increments
}

func findExportedMetrics(path string) map[string]bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return make(map[string]bool)
	}

	exported := make(map[string]bool)

	// Getter methods
	getterToField := map[string]string{
		"GetRecvRatePacketsPerSec":  "RecvRatePacketsPerSec",
		"GetRecvRateBytesPerSec":    "RecvRateBytesPerSec",
		"GetRecvRateMbps":           "RecvRateBytesPerSec",
		"GetRecvRateRetransPercent": "RecvRatePktRetransRate",
		"GetSendRateEstInputBW":     "SendRateEstInputBW",
		"GetSendRateEstSentBW":      "SendRateEstSentBW",
		"GetSendRateMbps":           "SendRateEstSentBW",
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

		if fieldName, isGetter := getterToField[methodName]; isGetter {
			exported[fieldName] = true
			return true
		}

		if methodName != "Load" {
			return true
		}

		fieldSel, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		fieldName := fieldSel.Sel.Name
		if len(fieldName) > 0 && fieldName[0] >= 'A' && fieldName[0] <= 'Z' {
			exported[fieldName] = true
		}

		return true
	})

	return exported
}

// =============================================================================
// Test Analysis (from test-audit)
// =============================================================================

type TestFile struct {
	Path          string
	TotalLines    int
	TestFunctions int
	IsTableDriven bool
	Patterns      map[string]int
}

var testPatterns = []struct {
	Name    string
	Pattern *regexp.Regexp
}{
	{Name: "table_driven", Pattern: regexp.MustCompile(`t\.Run\(|tc\.|testCase`)},
	{Name: "mock_time", Pattern: regexp.MustCompile(`nowFn\s*=|mockTime`)},
	{Name: "sequence_wrap", Pattern: regexp.MustCompile(`MAX_SEQUENCENUMBER|wraparound|Wraparound`)},
	{Name: "nak", Pattern: regexp.MustCompile(`[Nn]ak|NAK`)},
	{Name: "tsbpd", Pattern: regexp.MustCompile(`[Tt]sbpd|TSBPD`)},
}

func runTestAnalysis(root, file, dir, subMode string, result *AuditResult, verbose bool) {
	testDir := filepath.Join(root, "congestion/live")
	if dir != "" {
		testDir = filepath.Join(root, dir)
	}

	files := findTestFiles(testDir)

	switch subMode {
	case "summary", "audit":
		var tableDriven, legacy int
		var totalTests, totalLines int

		for _, f := range files {
			tf := analyzeTestFile(f)
			if tf == nil {
				continue
			}
			totalTests += tf.TestFunctions
			totalLines += tf.TotalLines

			if tf.IsTableDriven {
				tableDriven++
			} else if tf.TestFunctions > 5 {
				legacy++
			}
		}

		fmt.Printf("  📊 Test Files: %d total (%d table-driven, %d legacy)\n", len(files), tableDriven, legacy)
		fmt.Printf("  📊 Test Functions: %d across %d lines\n", totalTests, totalLines)

		result.Summary.TotalTests = totalTests
		result.Summary.TestGaps = legacy

		if legacy > 0 && verbose {
			fmt.Println("\n  Legacy files (candidates for table-driven conversion):")
			for _, f := range files {
				tf := analyzeTestFile(f)
				if tf != nil && !tf.IsTableDriven && tf.TestFunctions > 5 {
					fmt.Printf("     - %s (%d tests)\n", filepath.Base(f), tf.TestFunctions)
				}
			}
		}

	case "classify":
		if file == "" {
			fmt.Println("  ERROR: -file required for classify mode")
			return
		}
		runTestClassify(root, file, verbose)

	case "coverage":
		if file == "" {
			fmt.Println("  ERROR: -file required for coverage mode")
			return
		}
		runTestCoverage(root, file, verbose)
	}
}

func findTestFiles(dir string) []string {
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func analyzeTestFile(path string) *TestFile {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	tf := &TestFile{
		Path:       path,
		TotalLines: len(lines),
		Patterns:   make(map[string]int),
	}

	// Count patterns
	for _, pm := range testPatterns {
		matches := pm.Pattern.FindAllString(contentStr, -1)
		tf.Patterns[pm.Name] = len(matches)
	}

	tf.IsTableDriven = tf.Patterns["table_driven"] > 5 || strings.Contains(path, "_table_")

	// Count test functions
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, content, 0)
	if err != nil {
		return tf
	}

	ast.Inspect(node, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name != nil && strings.HasPrefix(fn.Name.Name, "Test") {
			tf.TestFunctions++
		}
		return true
	})

	return tf
}

func runTestClassify(root, file string, verbose bool) {
	fullPath := filepath.Join(root, file)

	fmt.Printf("\n  📋 Classifying fields in: %s\n", filepath.Base(file))
	fmt.Println("  " + strings.Repeat("─", 60))

	// Extract structs
	structs := extractTestStructs(fullPath)
	if len(structs) == 0 {
		fmt.Println("  No test case structs found.")
		return
	}

	for _, s := range structs {
		fmt.Printf("\n  Struct: %s (%d fields)\n", s.Name, len(s.Fields))

		codeParams := 0
		infraFields := 0
		expectFields := 0

		for _, f := range s.Fields {
			category := classifyField(f.Name, f.Type)
			switch category {
			case "code_param":
				codeParams++
				if verbose {
					fmt.Printf("    🎯 %s (%s) - CODE_PARAM\n", f.Name, f.Type)
				}
			case "test_infra":
				infraFields++
			case "expectation":
				expectFields++
			}
		}

		fmt.Printf("    CODE_PARAMs: %d | TEST_INFRA: %d | EXPECTATIONS: %d\n",
			codeParams, infraFields, expectFields)
	}
}

func runTestCoverage(root, file string, verbose bool) {
	fullPath := filepath.Join(root, file)

	fmt.Printf("\n  📋 Corner case coverage: %s\n", filepath.Base(file))
	fmt.Println("  " + strings.Repeat("─", 60))

	structs := extractTestStructs(fullPath)
	if len(structs) == 0 {
		fmt.Println("  No test case structs found.")
		return
	}

	testValues := extractTestValues(fullPath)

	for _, s := range structs {
		covered := 0
		total := 0

		for _, field := range s.Fields {
			corners := generateCorners(field.Name, field.Type)
			if len(corners) == 0 {
				continue
			}

			actualValues := testValues[field.Name]
			for _, corner := range corners {
				total++
				for actualVal := range actualValues {
					if valuesMatch(corner.Value, actualVal) {
						covered++
						break
					}
				}
			}
		}

		if total > 0 {
			pct := float64(covered) / float64(total) * 100
			status := "✅"
			if pct < 80 {
				status = "⚠️"
			}
			if pct < 50 {
				status = "❌"
			}
			fmt.Printf("  %s %s: %d/%d corners (%.0f%%)\n", status, s.Name, covered, total, pct)
		}
	}
}

// =============================================================================
// Coverage Analysis
// =============================================================================

func runCoverageAnalysis(root string, result *AuditResult, verbose bool) {
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│  Code Coverage Analysis                                          │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")

	// Find all production Go files
	var prodFiles []string
	var testFiles []string

	packages := []string{".", "congestion/live", "circular", "crypto", "metrics"}

	for _, pkg := range packages {
		pkgPath := filepath.Join(root, pkg)
		files, _ := filepath.Glob(filepath.Join(pkgPath, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				testFiles = append(testFiles, f)
			} else if !strings.Contains(f, "vendor/") {
				prodFiles = append(prodFiles, f)
			}
		}
	}

	fmt.Printf("  Production files: %d\n", len(prodFiles))
	fmt.Printf("  Test files: %d\n", len(testFiles))

	// Count functions
	prodFunctions := countFunctions(prodFiles)
	testFunctions := countTestFunctions(testFiles)

	fmt.Printf("  Production functions: %d\n", prodFunctions)
	fmt.Printf("  Test functions: %d\n", testFunctions)

	ratio := float64(testFunctions) / float64(prodFunctions)
	fmt.Printf("  Test/Prod ratio: %.2f\n", ratio)

	if ratio >= 1.0 {
		fmt.Println("  ✅ Good test coverage ratio")
	} else if ratio >= 0.5 {
		fmt.Println("  ⚠️  Moderate test coverage ratio")
	} else {
		fmt.Println("  ❌ Low test coverage ratio")
	}
}

func countFunctions(files []string) int {
	count := 0
	for _, f := range files {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			continue
		}
		ast.Inspect(node, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncDecl); ok {
				count++
			}
			return true
		})
	}
	return count
}

func countTestFunctions(files []string) int {
	count := 0
	for _, f := range files {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			continue
		}
		ast.Inspect(node, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok {
				if fn.Name != nil && strings.HasPrefix(fn.Name.Name, "Test") {
					count++
				}
			}
			return true
		})
	}
	return count
}

// =============================================================================
// Test Struct Helpers
// =============================================================================

type StructInfo struct {
	Name   string
	Fields []FieldInfo
}

type FieldInfo struct {
	Name string
	Type string
}

func extractTestStructs(filename string) []StructInfo {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		return nil
	}

	var structs []StructInfo

	ast.Inspect(node, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}

		structType, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			return true
		}

		name := typeSpec.Name.Name
		if !strings.Contains(name, "Test") && !strings.Contains(name, "Case") {
			return true
		}

		info := StructInfo{Name: name}
		for _, field := range structType.Fields.List {
			if len(field.Names) == 0 {
				continue
			}
			info.Fields = append(info.Fields, FieldInfo{
				Name: field.Names[0].Name,
				Type: typeExprString(field.Type),
			})
		}

		if len(info.Fields) > 0 {
			structs = append(structs, info)
		}
		return true
	})

	return structs
}

func classifyField(name, fieldType string) string {
	nameLower := strings.ToLower(name)

	// Expectations
	if strings.HasPrefix(name, "Expected") ||
		strings.HasPrefix(name, "Min") ||
		strings.HasPrefix(name, "Max") {
		return "expectation"
	}

	// Test infrastructure
	infraPatterns := []string{"cycle", "iteration", "spread", "pattern",
		"packets", "name", "description", "skip", "timeout", "tick"}
	for _, p := range infraPatterns {
		if strings.Contains(nameLower, p) {
			return "test_infra"
		}
	}

	// Code parameters
	codeParams := []string{"seq", "tsbpd", "nak", "latency", "mss", "fc", "buffer"}
	for _, p := range codeParams {
		if strings.Contains(nameLower, p) {
			return "code_param"
		}
	}

	return "test_infra"
}

type CornerValue struct {
	Field      string
	Value      string
	IsCritical bool
}

func generateCorners(fieldName, fieldType string) []CornerValue {
	var corners []CornerValue
	maxSeq := uint32(0x7FFFFFFF)
	fieldLower := strings.ToLower(fieldName)

	// Sequence fields
	if (strings.Contains(fieldLower, "seq") || strings.Contains(fieldLower, "point")) && fieldType == "uint32" {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "0", IsCritical: true},
			CornerValue{Field: fieldName, Value: fmt.Sprintf("%d", maxSeq-100), IsCritical: true},
			CornerValue{Field: fieldName, Value: fmt.Sprintf("%d", maxSeq), IsCritical: true},
		)
	}

	// Time fields
	if strings.Contains(fieldLower, "tsbpd") || strings.Contains(fieldLower, "delay") {
		if fieldType == "uint64" {
			corners = append(corners,
				CornerValue{Field: fieldName, Value: "10000", IsCritical: true},
				CornerValue{Field: fieldName, Value: "120000", IsCritical: false},
				CornerValue{Field: fieldName, Value: "500000", IsCritical: true},
			)
		}
	}

	return corners
}

func extractTestValues(filename string) map[string]map[string]bool {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, content, 0)
	if err != nil {
		return nil
	}

	values := make(map[string]map[string]bool)

	ast.Inspect(node, func(n ast.Node) bool {
		compLit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}

		for _, elt := range compLit.Elts {
			kvExpr, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}

			key, ok := kvExpr.Key.(*ast.Ident)
			if !ok {
				continue
			}

			fieldName := key.Name
			valueStr := extractValueStr(kvExpr.Value)

			if values[fieldName] == nil {
				values[fieldName] = make(map[string]bool)
			}
			values[fieldName][valueStr] = true
		}

		return true
	})

	return values
}

func extractValueStr(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return extractValueStr(v.X) + "." + v.Sel.Name
	case *ast.BinaryExpr:
		return extractValueStr(v.X) + " " + v.Op.String() + " " + extractValueStr(v.Y)
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func valuesMatch(cornerVal, actualVal string) bool {
	if cornerVal == actualVal {
		return true
	}
	cleanCorner := strings.ReplaceAll(cornerVal, "_", "")
	cleanActual := strings.ReplaceAll(actualVal, "_", "")
	return cleanCorner == cleanActual
}

// =============================================================================
// Summary
// =============================================================================

func printSummary(result *AuditResult) {
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════════════")
	fmt.Println("                           AUDIT SUMMARY")
	fmt.Println("════════════════════════════════════════════════════════════════════")

	// Sequence issues
	if result.Summary.SeqHigh > 0 {
		fmt.Printf("  🔴 Sequence Bugs:     %d HIGH severity\n", result.Summary.SeqHigh)
	} else {
		fmt.Println("  ✅ Sequence Arithmetic: Clean")
	}

	// Metrics issues
	if result.Summary.MetricGaps > 0 {
		fmt.Printf("  🔴 Metrics:           %d not exported\n", result.Summary.MetricGaps)
	} else {
		fmt.Println("  ✅ Prometheus Metrics: Aligned")
	}

	// Test coverage
	if result.Summary.TotalTests > 0 {
		fmt.Printf("  📊 Test Functions:    %d total\n", result.Summary.TotalTests)
	}

	fmt.Println("════════════════════════════════════════════════════════════════════")

	if result.HasHighSeverity {
		fmt.Println("  ❌ AUDIT FAILED - High severity issues found")
		fmt.Println("════════════════════════════════════════════════════════════════════")
	} else {
		fmt.Println("  ✅ AUDIT PASSED")
		fmt.Println("════════════════════════════════════════════════════════════════════")
	}
}

// =============================================================================
// Utilities
// =============================================================================

func findProjectRoot() string {
	markers := []string{"go.mod", "congestion/live", "metrics/metrics.go"}

	if allExist(".", markers) {
		return "."
	}

	dir, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		if allExist(dir, markers) {
			return dir
		}
		dir = filepath.Dir(dir)
	}

	return ""
}

func allExist(base string, paths []string) bool {
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(base, p)); os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func shortPath(path string) string {
	if idx := strings.Index(path, "gosrt/"); idx >= 0 {
		return path[idx+6:]
	}
	return filepath.Base(path)
}

func exprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.CallExpr:
		return exprString(e.Fun) + "(...)"
	case *ast.BasicLit:
		return e.Value
	case *ast.BinaryExpr:
		return fmt.Sprintf("%s %s %s", exprString(e.X), e.Op.String(), exprString(e.Y))
	default:
		return fmt.Sprintf("<%T>", expr)
	}
}

func typeExprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeExprString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeExprString(t.X)
	case *ast.ArrayType:
		return "[]" + typeExprString(t.Elt)
	default:
		return "unknown"
	}
}

