// test-audit is a unified tool for analyzing and improving table-driven tests.
//
// It combines features from test-table-audit and test-combinatorial-gen:
// - Test function discovery and pattern detection
// - AST-based struct field extraction
// - Production code parameter extraction
// - Test field classification (CODE_PARAM vs TEST_INFRA vs EXPECTATION)
// - Corner case coverage verification
//
// Usage:
//
//	test-audit [mode] [options] [file/dir]
//
// Modes:
//
//	audit     Full audit of test files (default)
//	classify  Classify test fields vs production code
//	coverage  Check corner case coverage
//	suggest   Suggest table structures for conversion
//
// Examples:
//
//	test-audit audit congestion/live
//	test-audit classify -file congestion/live/loss_recovery_table_test.go
//	test-audit coverage -file congestion/live/loss_recovery_table_test.go
//	test-audit suggest -file congestion/live/nak_consolidate_test.go
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// =============================================================================
// Data Structures
// =============================================================================

// TestFunction represents a parsed test function
type TestFunction struct {
	Name         string
	File         string
	Line         int
	Lines        int
	Patterns     []string
	Assertions   []string
	MockTimeUsed bool
}

// TestFile represents analysis of a test file
type TestFile struct {
	Path          string
	Package       string
	TestFunctions []TestFunction
	TotalLines    int
	Patterns      map[string]int
	Structs       []StructInfo
}

// StructInfo holds metadata about a test case struct
type StructInfo struct {
	Name   string
	Fields []FieldInfo
}

// FieldInfo holds metadata about a struct field
type FieldInfo struct {
	Name string
	Type string
}

// CodeParameter represents a real parameter in production code
type CodeParameter struct {
	Name       string
	Type       string
	Source     string // e.g., "receiver struct field"
	File       string
	IsCritical bool
}

// CodeParamMap is a map for O(1) parameter lookups (keyed by lowercase name)
type CodeParamMap map[string]*CodeParameter

// FieldClassification categorizes a test field
type FieldClassification struct {
	FieldName   string
	Category    string // "code_param", "test_infra", "expectation"
	MatchedCode *CodeParameter
	Reason      string
}

// CornerValue represents a corner case value
type CornerValue struct {
	Field       string
	Value       string
	IsCritical  bool
	Description string
}

// =============================================================================
// Pattern Matchers
// =============================================================================

var patternMatchers = []struct {
	Name    string
	Pattern *regexp.Regexp
}{
	{Name: "mock_time", Pattern: regexp.MustCompile(`nowFn\s*=|mockTime`)},
	{Name: "packet_create", Pattern: regexp.MustCompile(`packet\.NewPacket`)},
	{Name: "recv_push", Pattern: regexp.MustCompile(`recv\.Push|r\.Push`)},
	{Name: "recv_tick", Pattern: regexp.MustCompile(`recv\.Tick|r\.Tick`)},
	{Name: "nak_btree", Pattern: regexp.MustCompile(`nakBtree`)},
	{Name: "contiguous_point", Pattern: regexp.MustCompile(`contiguousPoint`)},
	{Name: "tsbpd", Pattern: regexp.MustCompile(`[Tt]sbpd|TSBPD`)},
	{Name: "sequence_wrap", Pattern: regexp.MustCompile(`MAX_SEQUENCENUMBER|wraparound|Wraparound`)},
	{Name: "loss_drop", Pattern: regexp.MustCompile(`drop|Drop|loss|Loss`)},
	{Name: "nak_send", Pattern: regexp.MustCompile(`OnSendNAK|sendNAK`)},
	{Name: "ack_send", Pattern: regexp.MustCompile(`OnSendACK|sendACK`)},
	{Name: "metrics_check", Pattern: regexp.MustCompile(`metrics\.|\.Load\(\)`)},
	{Name: "require_assert", Pattern: regexp.MustCompile(`require\.|assert\.`)},
	{Name: "for_loop", Pattern: regexp.MustCompile(`for\s+\w+\s*:?=`)},
	{Name: "table_driven", Pattern: regexp.MustCompile(`t\.Run\(|tc\.|testCase`)},
}

// =============================================================================
// Main Entry Point
// =============================================================================

func main() {
	// Define flags
	var (
		fileFlag    string
		dirFlag     string
		prodDirFlag string
		verboseFlag bool
	)

	flag.StringVar(&fileFlag, "file", "", "Specific test file to analyze")
	flag.StringVar(&dirFlag, "dir", "congestion/live", "Directory to analyze")
	flag.StringVar(&prodDirFlag, "prod-dir", "", "Production code directory (default: same as test)")
	flag.BoolVar(&verboseFlag, "verbose", false, "Verbose output")
	flag.Parse()

	// Get mode from positional args
	mode := "audit"
	args := flag.Args()
	if len(args) > 0 {
		mode = args[0]
	}

	// Find project root
	root := findProjectRoot()
	if root == "" {
		fmt.Fprintln(os.Stderr, "ERROR: Could not find project root (looking for congestion/live)")
		os.Exit(1)
	}

	// Default prod-dir to dir
	if prodDirFlag == "" {
		prodDirFlag = dirFlag
	}

	// Execute mode
	switch mode {
	case "audit":
		runAudit(root, dirFlag, fileFlag, verboseFlag)
	case "classify":
		if fileFlag == "" {
			fmt.Fprintln(os.Stderr, "ERROR: -file required for classify mode")
			os.Exit(1)
		}
		runClassify(root, fileFlag, prodDirFlag)
	case "coverage":
		if fileFlag == "" {
			fmt.Fprintln(os.Stderr, "ERROR: -file required for coverage mode")
			os.Exit(1)
		}
		runCoverage(root, fileFlag)
	case "suggest":
		if fileFlag == "" {
			fmt.Fprintln(os.Stderr, "ERROR: -file required for suggest mode")
			os.Exit(1)
		}
		runSuggest(root, fileFlag)
	default:
		fmt.Fprintf(os.Stderr, "ERROR: Unknown mode '%s'\n", mode)
		fmt.Fprintln(os.Stderr, "Valid modes: audit, classify, coverage, suggest")
		os.Exit(1)
	}
}

// =============================================================================
// Mode: Audit
// =============================================================================

func runAudit(root, dir, file string, verbose bool) {
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Println("TEST-AUDIT: Full Analysis")
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf("Project root: %s\n\n", root)

	var files []string
	if file != "" {
		files = []string{filepath.Join(root, file)}
	} else {
		files = findTestFiles(filepath.Join(root, dir))
	}

	var results []TestFile
	totalTests := 0
	totalLines := 0

	for _, f := range files {
		result := analyzeTestFile(f)
		if result != nil && len(result.TestFunctions) > 0 {
			results = append(results, *result)
			totalTests += len(result.TestFunctions)
			totalLines += result.TotalLines
		}
	}

	// Sort by line count
	sort.Slice(results, func(i, j int) bool {
		return results[i].TotalLines > results[j].TotalLines
	})

	fmt.Printf("Analyzed %d test files\n", len(results))
	fmt.Printf("Total test functions: %d\n", totalTests)
	fmt.Printf("Total test lines: %d\n\n", totalLines)

	// Print summary
	fmt.Println("=== File Summary (by size) ===")
	fmt.Printf("%-45s %6s %6s %s\n", "FILE", "TESTS", "LINES", "TABLE-DRIVEN?")
	fmt.Println(strings.Repeat("-", 100))

	for _, tf := range results {
		baseName := filepath.Base(tf.Path)
		tableDriven := "No"
		if isTableDriven(tf) {
			tableDriven = "✅ Yes"
		}
		fmt.Printf("%-45s %6d %6d %s\n", baseName, len(tf.TestFunctions), tf.TotalLines, tableDriven)
	}

	// Identify candidates for conversion
	fmt.Println("\n=== Table-Driven Conversion Candidates ===")
	for _, tf := range results {
		if len(tf.TestFunctions) >= 5 && !isTableDriven(tf) {
			score := calculateScore(tf)
			baseName := filepath.Base(tf.Path)
			fmt.Printf("\n📁 %s\n", baseName)
			fmt.Printf("   Tests: %d, Lines: %d, Score: %d/100\n", len(tf.TestFunctions), tf.TotalLines, score)
			fmt.Printf("   Top patterns: %s\n", getTopPatterns(tf.Patterns, 3))
			if score >= 60 {
				est := estimateSavings(tf)
				fmt.Printf("   → HIGH potential (~%d lines saved)\n", est)
			}
		}
	}

	// Table-driven files that need review
	fmt.Println("\n=== Table-Driven Tests Needing Review ===")
	fmt.Println("(Run 'test-audit classify -file <path>' to analyze field classification)")
	for _, tf := range results {
		if isTableDriven(tf) {
			baseName := filepath.Base(tf.Path)
			fmt.Printf("  🔍 %s\n", baseName)
		}
	}
}

// =============================================================================
// Mode: Classify
// =============================================================================

func runClassify(root, file, prodDir string) {
	fullPath := filepath.Join(root, file)
	prodPath := filepath.Join(root, prodDir)

	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf("FIELD CLASSIFICATION: %s\n", filepath.Base(file))
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	// Extract production parameters
	codeParams, err := extractCodeParameters(prodPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not extract production params: %v\n", err)
	}

	// Filter to only critical/useful parameters for display
	var criticalParams []*CodeParameter
	for _, cp := range codeParams {
		if cp.IsCritical && cp.Type != "unknown" {
			criticalParams = append(criticalParams, cp)
		}
	}

	fmt.Printf("\n🔍 Critical Production Code Parameters (%d found):\n", len(criticalParams))
	fmt.Println("───────────────────────────────────────────────────────────────────")
	for _, cp := range criticalParams {
		fmt.Printf("  %-25s %-12s from %s 🎯\n", cp.Name, cp.Type, cp.Source)
	}

	// Parse test file for structs
	structs := extractStructs(fullPath)
	if len(structs) == 0 {
		fmt.Println("\nNo test case structs found.")
		return
	}

	for _, s := range structs {
		fmt.Printf("\n📋 Test Struct: %s (%d fields)\n", s.Name, len(s.Fields))
		fmt.Println("───────────────────────────────────────────────────────────────────")

		classifications := classifyFields(s.Fields, codeParams)

		// Group by category
		var codeParamFields, infraFields, expectFields []FieldClassification
		for _, c := range classifications {
			switch c.Category {
			case "code_param":
				codeParamFields = append(codeParamFields, c)
			case "test_infra":
				infraFields = append(infraFields, c)
			case "expectation":
				expectFields = append(expectFields, c)
			}
		}

		fmt.Printf("\n🎯 CODE PARAMETERS (need corner coverage): %d\n", len(codeParamFields))
		for _, f := range codeParamFields {
			fmt.Printf("   ✅ %-20s → %s\n", f.FieldName, f.Reason)
		}

		fmt.Printf("\n⚙️  TEST INFRASTRUCTURE (skip combinatorial): %d\n", len(infraFields))
		for _, f := range infraFields {
			fmt.Printf("   ⚙️  %-20s → %s\n", f.FieldName, f.Reason)
		}

		fmt.Printf("\n📊 EXPECTATIONS (derived): %d\n", len(expectFields))
		for _, f := range expectFields {
			fmt.Printf("   📈 %-20s → %s\n", f.FieldName, f.Reason)
		}

		// Calculate combinations
		fmt.Println("\n═══════════════════════════════════════════════════════════════════")
		fmt.Println("COMBINATORIAL ANALYSIS")
		fmt.Println("═══════════════════════════════════════════════════════════════════")

		if len(codeParamFields) == 0 {
			fmt.Println("  No CODE_PARAM fields identified.")
			fmt.Println("  Consider whether any TEST_INFRA fields should be reclassified.")
		} else {
			totalCombos := 1
			fmt.Println("\n  CODE_PARAM corner values needed:")
			for _, f := range codeParamFields {
				corners := generateCorners(f.FieldName, getFieldType(s.Fields, f.FieldName))
				fmt.Printf("    %-20s: %d values %v\n", f.FieldName, len(corners), cornerValues(corners))
				if len(corners) > 0 {
					totalCombos *= len(corners)
				}
			}
			fmt.Printf("\n  Total combinations: %d\n", totalCombos)
			fmt.Printf("  (vs ~78,000 if all %d fields were varied!)\n", len(s.Fields))
		}
	}
}

// =============================================================================
// Mode: Coverage
// =============================================================================

func runCoverage(root, file string) {
	fullPath := filepath.Join(root, file)

	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf("CORNER CASE COVERAGE: %s\n", filepath.Base(file))
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	// Parse structs
	structs := extractStructs(fullPath)
	if len(structs) == 0 {
		fmt.Println("No test case structs found.")
		return
	}

	// Extract test values from file
	testValues := extractTestValues(fullPath)

	for _, s := range structs {
		fmt.Printf("\n📋 Struct: %s (%d fields)\n", s.Name, len(s.Fields))
		fmt.Println("───────────────────────────────────────────────────────────────────")

		totalCorners := 0
		coveredCorners := 0
		criticalMissing := 0

		for _, field := range s.Fields {
			// Skip derived/config fields
			if strings.HasPrefix(field.Name, "Expected") ||
				strings.HasPrefix(field.Name, "Min") ||
				strings.HasPrefix(field.Name, "Max") ||
				field.Name == "Name" {
				continue
			}

			corners := generateCorners(field.Name, field.Type)
			if len(corners) == 0 {
				continue
			}

			actualValues := testValues[field.Name]

			for _, corner := range corners {
				totalCorners++
				covered := false

				for actualVal, testNames := range actualValues {
					if valuesMatch(corner.Value, actualVal) {
						covered = true
						status := "✅"
						if corner.IsCritical {
							status = "✅🎯"
						}
						fmt.Printf("  %s %-20s: %s [%v]\n", status, field.Name+"/"+corner.Value, corner.Description, testNames)
						coveredCorners++
						break
					}
				}

				if !covered {
					status := "❌"
					if corner.IsCritical {
						status = "❌🚨"
						criticalMissing++
					}
					fmt.Printf("  %s %-20s: %s\n", status, field.Name+"/"+corner.Value, corner.Description)
				}
			}
		}

		fmt.Println()
		fmt.Printf("  Summary: %d/%d corners covered (%.1f%%)\n", coveredCorners, totalCorners,
			float64(coveredCorners)/float64(totalCorners)*100)
		if criticalMissing > 0 {
			fmt.Printf("  ⚠️  %d CRITICAL corners missing\n", criticalMissing)
		} else if coveredCorners == totalCorners {
			fmt.Printf("  ✅ All corners covered!\n")
		}
	}
}

// =============================================================================
// Mode: Suggest
// =============================================================================

func runSuggest(root, file string) {
	fullPath := filepath.Join(root, file)

	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf("TABLE STRUCTURE SUGGESTION: %s\n", filepath.Base(file))
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	result := analyzeTestFile(fullPath)
	if result == nil {
		fmt.Println("Could not analyze file.")
		return
	}

	// Group tests by prefix
	groups := make(map[string][]TestFunction)
	for _, fn := range result.TestFunctions {
		parts := strings.SplitN(fn.Name, "_", 2)
		if len(parts) == 2 {
			groups[parts[0]] = append(groups[parts[0]], fn)
		}
	}

	// Find largest group
	var largestPrefix string
	var largestCount int
	for prefix, tests := range groups {
		if len(tests) > largestCount {
			largestCount = len(tests)
			largestPrefix = prefix
		}
	}

	if largestCount < 3 {
		fmt.Println("No clear test groups found (need 3+ tests with common prefix).")
		return
	}

	fmt.Printf("\n📊 Largest test group: %s_* (%d tests)\n", largestPrefix, largestCount)
	fmt.Println("───────────────────────────────────────────────────────────────────")

	for _, t := range groups[largestPrefix] {
		shortName := strings.TrimPrefix(t.Name, largestPrefix+"_")
		fmt.Printf("  • %s (line %d, %d lines)\n", shortName, t.Line, t.Lines)
	}

	// Suggest struct based on patterns
	fmt.Printf("\n📝 Suggested Table Structure:\n")
	fmt.Println("───────────────────────────────────────────────────────────────────")
	fmt.Println("```go")
	fmt.Printf("type %sTestCase struct {\n", strings.TrimPrefix(largestPrefix, "Test"))
	fmt.Println("    Name string")

	if result.Patterns["packet_create"] > 0 {
		fmt.Println("    TotalPackets int")
	}
	if result.Patterns["sequence_wrap"] > 0 {
		fmt.Println("    StartSeq     uint32  // For wraparound tests")
	}
	if result.Patterns["tsbpd"] > 0 {
		fmt.Println("    TsbpdDelayUs uint64")
	}
	if result.Patterns["loss_drop"] > 0 {
		fmt.Println("    DropPattern  DropPattern")
	}
	if result.Patterns["nak_btree"] > 0 {
		fmt.Println("    NakMergeGap  uint32")
	}
	if result.Patterns["mock_time"] > 0 {
		fmt.Println("    // Timing")
		fmt.Println("    NakCycles    int")
	}

	fmt.Println("    // Expectations")
	fmt.Println("    Expected...  interface{}")
	fmt.Println("}")
	fmt.Println("```")

	// Estimate savings
	estSavings := estimateSavings(*result)
	fmt.Printf("\n💰 Estimated savings: ~%d lines (%.0f%%)\n", estSavings, float64(estSavings)/float64(result.TotalLines)*100)
}

// =============================================================================
// Helper Functions
// =============================================================================

func findProjectRoot() string {
	if _, err := os.Stat("congestion/live"); err == nil {
		return "."
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to get current directory: %v\n", err)
		return ""
	}
	for i := 0; i < 5; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "congestion/live")); statErr == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func findTestFiles(dir string) []string {
	var files []string
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error walking directory %s: %v\n", dir, err)
	}
	return files
}

func analyzeTestFile(path string) *TestFile {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	result := &TestFile{
		Path:       path,
		Package:    node.Name.Name,
		Patterns:   make(map[string]int),
		TotalLines: len(lines),
	}

	// Count patterns
	for _, pm := range patternMatchers {
		matches := pm.Pattern.FindAllString(contentStr, -1)
		if len(matches) > 0 {
			result.Patterns[pm.Name] = len(matches)
		}
	}

	// Analyze functions
	ast.Inspect(node, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			return true
		}

		name := fn.Name.Name
		if !strings.HasPrefix(name, "Test") {
			return true
		}

		startLine := fset.Position(fn.Pos()).Line
		endLine := fset.Position(fn.End()).Line

		tf := TestFunction{
			Name:  name,
			File:  path,
			Line:  startLine,
			Lines: endLine - startLine + 1,
		}

		// Extract function content for pattern matching
		if startLine > 0 && endLine <= len(lines) {
			funcContent := strings.Join(lines[startLine-1:endLine], "\n")
			tf.MockTimeUsed = strings.Contains(funcContent, "mockTime") || strings.Contains(funcContent, "nowFn")
		}

		result.TestFunctions = append(result.TestFunctions, tf)
		return true
	})

	return result
}

func isTableDriven(tf TestFile) bool {
	return tf.Patterns["table_driven"] > 5 || strings.Contains(tf.Path, "_table_")
}

func calculateScore(tf TestFile) int {
	score := 0
	if len(tf.TestFunctions) >= 10 {
		score += 30
	} else if len(tf.TestFunctions) >= 5 {
		score += 20
	}

	commonPatterns := 0
	for _, count := range tf.Patterns {
		if count >= len(tf.TestFunctions)/2 {
			commonPatterns++
		}
	}
	score += commonPatterns * 5

	if tf.Patterns["mock_time"] > 0 {
		score += 10
	}
	if tf.Patterns["packet_create"] > 3 {
		score += 10
	}
	if tf.Patterns["loss_drop"] > 0 {
		score += 15
	}
	if tf.Patterns["for_loop"] > 3 {
		score += 10
	}

	return score
}

func estimateSavings(tf TestFile) int {
	score := calculateScore(tf)
	reductionPct := float64(score) / 100.0 * 0.7
	if reductionPct > 0.7 {
		reductionPct = 0.7
	}
	if reductionPct < 0.3 {
		reductionPct = 0.3
	}
	return int(float64(tf.TotalLines) * reductionPct)
}

func getTopPatterns(patterns map[string]int, n int) string {
	type kv struct {
		k string
		v int
	}
	sorted := make([]kv, 0, len(patterns))
	for k, v := range patterns {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].v > sorted[j].v
	})

	var top []string
	for i := 0; i < n && i < len(sorted); i++ {
		top = append(top, fmt.Sprintf("%s(%d)", sorted[i].k, sorted[i].v))
	}
	return strings.Join(top, ", ")
}

// =============================================================================
// Production Code Parameter Extraction
// =============================================================================

func extractCodeParameters(prodDir string) (CodeParamMap, error) {
	paramMap := make(CodeParamMap)

	// Files in the production directory (congestion/live/)
	prodFiles := []string{"receive.go", "send.go", "connection.go"}
	for _, filename := range prodFiles {
		fullPath := filepath.Join(prodDir, filename)
		addFileParams(fullPath, paramMap)
	}

	// Also check root config.go (important SRT-level config)
	rootDir := filepath.Dir(prodDir) // Go up from congestion/live to root
	if filepath.Base(rootDir) == "congestion" {
		rootDir = filepath.Dir(rootDir)
	}
	rootConfig := filepath.Join(rootDir, "config.go")
	addFileParams(rootConfig, paramMap)

	return paramMap, nil
}

func addFileParams(fullPath string, paramMap CodeParamMap) {
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return
	}

	fileParams, err := analyzeProductionFile(fullPath)
	if err != nil {
		return
	}

	// Add to map (deduplicates automatically, first one wins)
	for i := range fileParams {
		p := &fileParams[i]
		key := strings.ToLower(p.Name)
		if _, exists := paramMap[key]; !exists {
			paramMap[key] = p
		}
	}
}

func analyzeProductionFile(filename string) ([]CodeParameter, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var params []CodeParameter
	baseName := filepath.Base(filename)

	relevantStructs := map[string]bool{
		"receiver": true, "sender": true,
		"receiveconfig": true, "sendconfig": true,
		"config": true, "streamconfig": true,
	}

	ast.Inspect(node, func(n ast.Node) bool {
		x, isTypeSpec := n.(*ast.TypeSpec)
		if isTypeSpec {
			if st, isStruct := x.Type.(*ast.StructType); isStruct {
				structName := strings.ToLower(x.Name.Name)
				if relevantStructs[structName] {
					for _, field := range st.Fields.List {
						for _, name := range field.Names {
							params = append(params, CodeParameter{
								Name:       name.Name,
								Type:       typeToString(field.Type),
								Source:     x.Name.Name + " struct",
								File:       baseName,
								IsCritical: isCriticalParam(name.Name),
							})
						}
					}
				}
			}
		}
		return true
	})

	return params, nil
}

func isCriticalParam(name string) bool {
	// Map for O(1) lookup of critical parameter patterns
	criticalPatterns := map[string]bool{
		"tsbpddelay": true, "tsbpd": true,
		"nakrecent": true, "nakmergegap": true,
		"initialsequencenumber": true, "contiguouspoint": true,
		"lastacksequencenumber": true, "ringsize": true, "buffersize": true,
		// Root config.go parameters
		"latency": true, "peerlatency": true, "recvlatency": true,
		"fc": true, "inputbw": true, "maxbw": true,
		"mss": true, "payloadsize": true,
		"rcvbuf": true, "sndbuf": true,
	}

	nameLower := strings.ToLower(name)
	for pattern := range criticalPatterns {
		if strings.Contains(nameLower, pattern) {
			return true
		}
	}
	return false
}

// =============================================================================
// Struct Extraction
// =============================================================================

func extractStructs(filename string) []StructInfo {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
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
				Type: typeToString(field.Type),
			})
		}

		if len(info.Fields) > 0 {
			structs = append(structs, info)
		}
		return true
	})

	return structs
}

func typeToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeToString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeToString(t.X)
	case *ast.ArrayType:
		return "[]" + typeToString(t.Elt)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "unknown"
	}
}

// =============================================================================
// Field Classification
// =============================================================================

func classifyFields(fields []FieldInfo, codeParams CodeParamMap) []FieldClassification {
	result := make([]FieldClassification, 0, len(fields))

	for _, f := range fields {
		result = append(result, classifyField(f, codeParams))
	}

	return result
}

func classifyField(field FieldInfo, codeParams CodeParamMap) FieldClassification {
	fieldLower := strings.ToLower(field.Name)

	// Check for expectation patterns first
	if strings.HasPrefix(field.Name, "Expected") ||
		strings.HasPrefix(field.Name, "Min") ||
		strings.HasPrefix(field.Name, "Max") {
		return FieldClassification{
			FieldName: field.Name,
			Category:  "expectation",
			Reason:    "Name pattern indicates expected result",
		}
	}

	// Test infrastructure patterns (check before code param matching)
	infraPatterns := map[string]bool{
		"cycle": true, "iteration": true, "spread": true, "pattern": true,
		"packets": true, "name": true, "description": true, "skip": true,
		"timeout": true, "tick": true, "interval": true, "retransmit": true,
		"recovery": true, "expectfull": true, "total": true,
	}
	for pattern := range infraPatterns {
		if strings.Contains(fieldLower, pattern) {
			return FieldClassification{
				FieldName: field.Name,
				Category:  "test_infra",
				Reason:    fmt.Sprintf("Contains '%s' - test infrastructure", pattern),
			}
		}
	}

	// O(1) map lookup: Try exact match first
	if cp, exists := codeParams[fieldLower]; exists && isValidCodeParam(cp) {
		return FieldClassification{
			FieldName:   field.Name,
			Category:    "code_param",
			MatchedCode: cp,
			Reason:      fmt.Sprintf("Maps to %s.%s in %s", cp.Source, cp.Name, cp.File),
		}
	}

	// Semantic aliases map for O(1) lookup
	semanticAliases := map[string]string{
		"startseq":     "initialsequencenumber",
		"tsbpddelayus": "tsbpddelay",
		"nakrecentpct": "nakrecentpercent",
		"nakmergegap":  "nakmergegap",
	}

	if alias, hasAlias := semanticAliases[fieldLower]; hasAlias {
		if cp, exists := codeParams[alias]; exists && isValidCodeParam(cp) {
			return FieldClassification{
				FieldName:   field.Name,
				Category:    "code_param",
				MatchedCode: cp,
				Reason:      fmt.Sprintf("Maps to %s.%s in %s", cp.Source, cp.Name, cp.File),
			}
		}
	}

	// Default
	return FieldClassification{
		FieldName: field.Name,
		Category:  "test_infra",
		Reason:    "Not found in production code",
	}
}

// isValidCodeParam filters out callback/function fields
func isValidCodeParam(cp *CodeParameter) bool {
	if cp == nil || cp.Type == "unknown" {
		return false
	}
	cpLower := strings.ToLower(cp.Name)
	// Skip callbacks and pointers to complex types
	if strings.Contains(cpLower, "func") || strings.Contains(cpLower, "deliver") ||
		strings.Contains(cpLower, "sendack") || strings.Contains(cpLower, "sendnak") {
		return false
	}
	return true
}

func getFieldType(fields []FieldInfo, name string) string {
	for _, f := range fields {
		if f.Name == name {
			return f.Type
		}
	}
	return "unknown"
}

// =============================================================================
// Corner Case Generation
// =============================================================================

func generateCorners(fieldName, fieldType string) []CornerValue {
	var corners []CornerValue
	maxSeq := uint32(0x7FFFFFFF)

	fieldLower := strings.ToLower(fieldName)

	// Sequence number fields
	if (strings.Contains(fieldLower, "seq") || strings.Contains(fieldLower, "point")) && fieldType == "uint32" {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "0", IsCritical: true, Description: "Zero - baseline"},
			CornerValue{Field: fieldName, Value: fmt.Sprintf("%d", maxSeq-100), IsCritical: true, Description: "Near MAX - wraparound zone"},
			CornerValue{Field: fieldName, Value: fmt.Sprintf("%d", maxSeq), IsCritical: true, Description: "AT MAX - immediate wrap"},
		)
		return corners
	}

	// Packet count fields
	if strings.Contains(fieldLower, "total") || strings.Contains(fieldLower, "count") || strings.Contains(fieldLower, "packets") {
		if fieldType == "int" || fieldType == "uint32" {
			corners = append(corners,
				CornerValue{Field: fieldName, Value: "1", IsCritical: true, Description: "Minimum - single"},
				CornerValue{Field: fieldName, Value: "100", IsCritical: false, Description: "Typical"},
				CornerValue{Field: fieldName, Value: "1000", IsCritical: true, Description: "Large - stress"},
			)
			return corners
		}
	}

	// Time/delay fields
	if strings.Contains(fieldLower, "tsbpd") || strings.Contains(fieldLower, "delay") {
		if fieldType == "uint64" {
			corners = append(corners,
				CornerValue{Field: fieldName, Value: "10000", IsCritical: true, Description: "10ms - aggressive"},
				CornerValue{Field: fieldName, Value: "120000", IsCritical: false, Description: "120ms - standard"},
				CornerValue{Field: fieldName, Value: "500000", IsCritical: true, Description: "500ms - high latency"},
			)
			return corners
		}
	}

	// Percentage fields
	if strings.Contains(fieldLower, "percent") || strings.Contains(fieldLower, "pct") {
		if fieldType == "float64" {
			corners = append(corners,
				CornerValue{Field: fieldName, Value: "0.05", IsCritical: true, Description: "5% - aggressive"},
				CornerValue{Field: fieldName, Value: "0.10", IsCritical: false, Description: "10% - typical"},
				CornerValue{Field: fieldName, Value: "0.25", IsCritical: true, Description: "25% - conservative"},
			)
			return corners
		}
	}

	// Gap/merge fields
	if strings.Contains(fieldLower, "gap") || strings.Contains(fieldLower, "merge") {
		if fieldType == "uint32" || fieldType == "int" {
			corners = append(corners,
				CornerValue{Field: fieldName, Value: "0", IsCritical: true, Description: "Zero gap"},
				CornerValue{Field: fieldName, Value: "3", IsCritical: false, Description: "Typical gap"},
				CornerValue{Field: fieldName, Value: "100", IsCritical: true, Description: "Large gap"},
			)
			return corners
		}
	}

	// Boolean fields
	if fieldType == "bool" {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "true", IsCritical: true, Description: "Enabled"},
			CornerValue{Field: fieldName, Value: "false", IsCritical: true, Description: "Disabled"},
		)
		return corners
	}

	return corners
}

func cornerValues(corners []CornerValue) []string {
	vals := make([]string, 0, len(corners))
	for _, c := range corners {
		vals = append(vals, c.Value)
	}
	return vals
}

// =============================================================================
// Test Value Extraction
// =============================================================================

func extractTestValues(filename string) map[string]map[string][]string {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
	if err != nil {
		return nil
	}

	values := make(map[string]map[string][]string)

	ast.Inspect(node, func(n ast.Node) bool {
		compLit, isCompLit := n.(*ast.CompositeLit)
		if !isCompLit {
			return true
		}

		currentTestName := ""

		for _, elt := range compLit.Elts {
			kvExpr, isKV := elt.(*ast.KeyValueExpr)
			if !isKV {
				continue
			}

			key, isIdent := kvExpr.Key.(*ast.Ident)
			if !isIdent {
				continue
			}

			fieldName := key.Name
			valueStr := extractValue(kvExpr.Value)

			if fieldName == "Name" {
				currentTestName = strings.Trim(valueStr, "\"")
				continue
			}

			if values[fieldName] == nil {
				values[fieldName] = make(map[string][]string)
			}

			if currentTestName != "" {
				values[fieldName][valueStr] = append(values[fieldName][valueStr], currentTestName)
			}
		}

		return true
	})

	return values
}

func extractValue(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return extractValue(v.X) + "." + v.Sel.Name
	case *ast.BinaryExpr:
		return extractValue(v.X) + " " + v.Op.String() + " " + extractValue(v.Y)
	case *ast.UnaryExpr:
		return v.Op.String() + extractValue(v.X)
	case *ast.CallExpr:
		return extractValue(v.Fun)
	case *ast.CompositeLit:
		if v.Type != nil {
			return extractValue(v.Type)
		}
		return "composite"
	case *ast.StarExpr:
		return "*" + extractValue(v.X)
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func valuesMatch(cornerVal, actualVal string) bool {
	// Direct match
	if cornerVal == actualVal {
		return true
	}

	// Handle underscore notation (120_000 vs 120000)
	cleanCorner := strings.ReplaceAll(cornerVal, "_", "")
	cleanActual := strings.ReplaceAll(actualVal, "_", "")
	if cleanCorner == cleanActual {
		return true
	}

	// Handle MAX expressions
	if strings.Contains(actualVal, "MAX_SEQUENCENUMBER") {
		if strings.Contains(actualVal, "- 100") && cornerVal == "2147483547" {
			return true
		}
		if strings.Contains(actualVal, "- 50") && cornerVal == "2147483597" {
			return true
		}
		if !strings.Contains(actualVal, "-") && cornerVal == "2147483647" {
			return true
		}
	}

	return false
}
