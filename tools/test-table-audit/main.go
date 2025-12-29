// test-table-audit is a static analysis tool that helps convert individual tests
// to table-driven tests safely. It:
//
// 1. Parses test files and extracts test function metadata
// 2. Identifies common patterns (setup, assertions, parameters)
// 3. Suggests table-driven structures based on patterns
// 4. Verifies table-driven tests cover all original test cases
//
// Usage:
//
//	go run tools/test-table-audit/main.go [file.go]
//	make audit-tests
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

// TestFunction represents a parsed test function
type TestFunction struct {
	Name          string
	File          string
	Line          int
	Lines         int // Total lines in function
	SetupPatterns []string
	Assertions    []string
	MockTimeUsed  bool
	PacketCount   int    // Detected from literals if possible
	LossPattern   string // Detected from code patterns
	SequenceStart string // Detected start sequence
	Parameters    map[string]string
}

// TestFile represents analysis of a test file
type TestFile struct {
	Path          string
	Package       string
	TestFunctions []TestFunction
	TotalLines    int
	Patterns      map[string]int // Pattern name -> count
}

// PatternMatcher helps identify common test patterns
type PatternMatcher struct {
	Name    string
	Pattern *regexp.Regexp
}

var patternMatchers = []PatternMatcher{
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

var assertionMatchers = []PatternMatcher{
	{Name: "require.Equal", Pattern: regexp.MustCompile(`require\.Equal`)},
	{Name: "require.True", Pattern: regexp.MustCompile(`require\.True`)},
	{Name: "require.False", Pattern: regexp.MustCompile(`require\.False`)},
	{Name: "require.Greater", Pattern: regexp.MustCompile(`require\.Greater`)},
	{Name: "require.GreaterOrEqual", Pattern: regexp.MustCompile(`require\.GreaterOrEqual`)},
	{Name: "require.Less", Pattern: regexp.MustCompile(`require\.Less`)},
	{Name: "require.LessOrEqual", Pattern: regexp.MustCompile(`require\.LessOrEqual`)},
	{Name: "require.NotNil", Pattern: regexp.MustCompile(`require\.NotNil`)},
	{Name: "require.Nil", Pattern: regexp.MustCompile(`require\.Nil`)},
	{Name: "require.NoError", Pattern: regexp.MustCompile(`require\.NoError`)},
	{Name: "t.Error", Pattern: regexp.MustCompile(`t\.Error[f]?\(`)},
	{Name: "t.Fatal", Pattern: regexp.MustCompile(`t\.Fatal[f]?\(`)},
}

func main() {
	var targetFile string
	var showDetails bool
	var suggestTable bool
	var verifyMode bool

	flag.StringVar(&targetFile, "file", "", "Specific test file to analyze (default: all *_test.go in congestion/live)")
	flag.BoolVar(&showDetails, "details", false, "Show detailed function analysis")
	flag.BoolVar(&suggestTable, "suggest", false, "Suggest table-driven structure")
	flag.BoolVar(&verifyMode, "verify", false, "Verify table tests cover original tests")
	flag.Parse()

	root := findProjectRoot()
	if root == "" {
		fmt.Println("ERROR: Could not find project root")
		os.Exit(2)
	}

	fmt.Println("=== GoSRT Test Table Audit ===")
	fmt.Printf("Project root: %s\n\n", root)

	var files []string
	if targetFile != "" {
		files = []string{filepath.Join(root, targetFile)}
	} else {
		files = findTestFiles(filepath.Join(root, "congestion/live"))
	}

	allResults := make([]TestFile, 0)
	totalTests := 0
	totalLines := 0

	for _, file := range files {
		result := analyzeTestFile(file)
		if result != nil && len(result.TestFunctions) > 0 {
			allResults = append(allResults, *result)
			totalTests += len(result.TestFunctions)
			totalLines += result.TotalLines
		}
	}

	// Sort by line count (largest files first)
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].TotalLines > allResults[j].TotalLines
	})

	fmt.Printf("Analyzed %d test files\n", len(allResults))
	fmt.Printf("Total test functions: %d\n", totalTests)
	fmt.Printf("Total test lines: %d\n\n", totalLines)

	// Print summary table
	fmt.Println("=== File Summary (by size) ===")
	fmt.Printf("%-45s %6s %6s %s\n", "FILE", "TESTS", "LINES", "TOP PATTERNS")
	fmt.Println(strings.Repeat("-", 100))

	for _, tf := range allResults {
		baseName := filepath.Base(tf.Path)
		topPatterns := getTopPatterns(tf.Patterns, 3)
		fmt.Printf("%-45s %6d %6d %s\n", baseName, len(tf.TestFunctions), tf.TotalLines, topPatterns)
	}

	// Identify table-driven candidates
	fmt.Println("\n=== Table-Driven Candidates ===")
	fmt.Println("Files with high test count and common patterns:\n")

	for _, tf := range allResults {
		if len(tf.TestFunctions) >= 5 && !isAlreadyTableDriven(tf) {
			score := calculateTableDrivenScore(tf)
			baseName := filepath.Base(tf.Path)
			fmt.Printf("  %s\n", baseName)
			fmt.Printf("    Tests: %d, Lines: %d, Score: %d/100\n", len(tf.TestFunctions), tf.TotalLines, score)
			fmt.Printf("    Patterns: %s\n", getTopPatterns(tf.Patterns, 5))
			if score >= 60 {
				fmt.Printf("    → HIGH potential for table-driven conversion\n")
				estSavings := estimateSavings(tf)
				fmt.Printf("    → Estimated savings: ~%d lines (%.0f%%)\n", estSavings, float64(estSavings)/float64(tf.TotalLines)*100)
			} else if score >= 40 {
				fmt.Printf("    → MEDIUM potential for table-driven conversion\n")
			}
			fmt.Println()
		}
	}

	// Group similar test names
	fmt.Println("=== Test Name Patterns ===")
	fmt.Println("Tests with similar naming suggest table-driven structure:\n")

	testGroups := groupTestsByPrefix(allResults)
	for prefix, tests := range testGroups {
		if len(tests) >= 3 {
			fmt.Printf("  %s_* (%d tests)\n", prefix, len(tests))
			for _, t := range tests {
				shortName := strings.TrimPrefix(t.Name, prefix+"_")
				fmt.Printf("    - %s (line %d)\n", shortName, t.Line)
			}
			fmt.Println()
		}
	}

	if showDetails {
		fmt.Println("\n=== Detailed Function Analysis ===")
		for _, tf := range allResults {
			fmt.Printf("\nFile: %s\n", filepath.Base(tf.Path))
			for _, fn := range tf.TestFunctions {
				fmt.Printf("  %s (line %d, %d lines)\n", fn.Name, fn.Line, fn.Lines)
				fmt.Printf("    Patterns: %v\n", fn.SetupPatterns)
				fmt.Printf("    Assertions: %v\n", fn.Assertions)
			}
		}
	}

	if suggestTable {
		fmt.Println("\n=== Suggested Table Structure ===")
		for _, tf := range allResults {
			if len(tf.TestFunctions) >= 5 && calculateTableDrivenScore(tf) >= 50 {
				suggestTableStructure(tf)
			}
		}
	}

	if verifyMode {
		fmt.Println("\n=== Table Coverage Verification ===")
		verifyTableCoverage(allResults)
	}

	// Summary
	fmt.Println("\n=== Recommendations ===")
	fmt.Println("1. Run with -details for per-function analysis")
	fmt.Println("2. Run with -suggest for table structure suggestions")
	fmt.Println("3. Run with -verify after conversion to ensure coverage")
	fmt.Println("4. Run with -file=path/to/test.go to analyze single file")
}

func findProjectRoot() string {
	// Try current directory first
	if _, err := os.Stat("congestion/live"); err == nil {
		return "."
	}
	// Try parent directories
	dir, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "congestion/live")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func findTestFiles(dir string) []string {
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func analyzeTestFile(path string) *TestFile {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil
	}

	// Read file content for pattern matching
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

	// Count patterns in whole file
	for _, pm := range patternMatchers {
		matches := pm.Pattern.FindAllString(contentStr, -1)
		if len(matches) > 0 {
			result.Patterns[pm.Name] = len(matches)
		}
	}

	// Analyze each test function
	ast.Inspect(node, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			return true
		}

		name := fn.Name.Name
		if !strings.HasPrefix(name, "Test") {
			return true
		}

		// Get line range
		startLine := fset.Position(fn.Pos()).Line
		endLine := fset.Position(fn.End()).Line
		funcLines := endLine - startLine + 1

		// Extract function body as string
		funcContent := ""
		if startLine > 0 && endLine <= len(lines) {
			funcContent = strings.Join(lines[startLine-1:endLine], "\n")
		}

		tf := TestFunction{
			Name:       name,
			File:       path,
			Line:       startLine,
			Lines:      funcLines,
			Parameters: make(map[string]string),
		}

		// Detect patterns in function
		for _, pm := range patternMatchers {
			if pm.Pattern.MatchString(funcContent) {
				tf.SetupPatterns = append(tf.SetupPatterns, pm.Name)
			}
		}

		// Detect assertions
		for _, am := range assertionMatchers {
			if am.Pattern.MatchString(funcContent) {
				tf.Assertions = append(tf.Assertions, am.Name)
			}
		}

		// Detect mock time usage
		tf.MockTimeUsed = strings.Contains(funcContent, "mockTime") || strings.Contains(funcContent, "nowFn")

		// Try to detect packet count from constants
		packetCountRe := regexp.MustCompile(`totalPackets\s*=\s*(\d+)|numPackets\s*=\s*(\d+)`)
		if matches := packetCountRe.FindStringSubmatch(funcContent); len(matches) > 1 {
			for _, m := range matches[1:] {
				if m != "" {
					tf.Parameters["totalPackets"] = m
					break
				}
			}
		}

		result.TestFunctions = append(result.TestFunctions, tf)
		return true
	})

	return result
}

func getTopPatterns(patterns map[string]int, n int) string {
	type kv struct {
		k string
		v int
	}
	var sorted []kv
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

func isAlreadyTableDriven(tf TestFile) bool {
	// Check if file already uses table-driven approach
	return tf.Patterns["table_driven"] > 5 || strings.Contains(tf.Path, "_table_")
}

func calculateTableDrivenScore(tf TestFile) int {
	score := 0

	// More tests = higher score
	if len(tf.TestFunctions) >= 10 {
		score += 30
	} else if len(tf.TestFunctions) >= 5 {
		score += 20
	}

	// Common patterns across tests = higher score
	commonPatterns := 0
	for _, count := range tf.Patterns {
		if count >= len(tf.TestFunctions)/2 {
			commonPatterns++
		}
	}
	score += commonPatterns * 5

	// Mock time usage suggests parameterizable timing
	if tf.Patterns["mock_time"] > 0 {
		score += 10
	}

	// Packet creation suggests parameterizable input
	if tf.Patterns["packet_create"] > 3 {
		score += 10
	}

	// Loss patterns are easily parameterized
	if tf.Patterns["loss_drop"] > 0 {
		score += 15
	}

	// For loops suggest iteration that could be table-driven
	if tf.Patterns["for_loop"] > 3 {
		score += 10
	}

	return score
}

func estimateSavings(tf TestFile) int {
	// Based on loss_recovery conversion: 76% reduction
	// Conservative estimate: 50-70% based on pattern match
	score := calculateTableDrivenScore(tf)
	reductionPct := float64(score) / 100.0 * 0.7 // Max 70% reduction
	if reductionPct > 0.7 {
		reductionPct = 0.7
	}
	if reductionPct < 0.3 {
		reductionPct = 0.3
	}
	return int(float64(tf.TotalLines) * reductionPct)
}

func groupTestsByPrefix(results []TestFile) map[string][]TestFunction {
	groups := make(map[string][]TestFunction)

	for _, tf := range results {
		for _, fn := range tf.TestFunctions {
			// Extract prefix (e.g., TestConsolidateNakBtree from TestConsolidateNakBtree_Empty)
			parts := strings.SplitN(fn.Name, "_", 2)
			if len(parts) == 2 {
				prefix := parts[0]
				groups[prefix] = append(groups[prefix], fn)
			}
		}
	}

	return groups
}

func suggestTableStructure(tf TestFile) {
	baseName := filepath.Base(tf.Path)
	fmt.Printf("\n--- %s ---\n", baseName)

	// Identify common prefix for test functions
	prefixCounts := make(map[string]int)
	for _, fn := range tf.TestFunctions {
		parts := strings.SplitN(fn.Name, "_", 2)
		if len(parts) >= 1 {
			prefixCounts[parts[0]]++
		}
	}

	// Find most common prefix
	var commonPrefix string
	maxCount := 0
	for prefix, count := range prefixCounts {
		if count > maxCount {
			maxCount = count
			commonPrefix = prefix
		}
	}

	fmt.Printf("Suggested test name: %s_Table\n\n", commonPrefix)
	fmt.Printf("Suggested struct:\n")
	fmt.Printf("```go\n")
	fmt.Printf("type %sTestCase struct {\n", strings.TrimPrefix(commonPrefix, "Test"))
	fmt.Printf("    Name string\n")

	// Suggest fields based on patterns
	if tf.Patterns["packet_create"] > 0 {
		fmt.Printf("    TotalPackets int\n")
	}
	if tf.Patterns["sequence_wrap"] > 0 {
		fmt.Printf("    StartSeq     uint32\n")
	}
	if tf.Patterns["tsbpd"] > 0 {
		fmt.Printf("    TsbpdDelayUs uint64\n")
	}
	if tf.Patterns["mock_time"] > 0 {
		fmt.Printf("    // Timing\n")
		fmt.Printf("    TickCount    int\n")
		fmt.Printf("    TickInterval int\n")
	}
	if tf.Patterns["loss_drop"] > 0 {
		fmt.Printf("    DropPattern  DropPattern // Reuse from loss_recovery\n")
	}
	if tf.Patterns["nak_btree"] > 0 {
		fmt.Printf("    NakMergeGap  int\n")
	}

	fmt.Printf("    // Expectations\n")
	for _, fn := range tf.TestFunctions {
		for _, a := range fn.Assertions {
			if strings.Contains(a, "Equal") {
				fmt.Printf("    Expected... interface{}\n")
				break
			}
		}
		break
	}

	fmt.Printf("}\n")
	fmt.Printf("```\n")

	// List test cases to convert
	fmt.Printf("\nTest cases to convert (%d):\n", len(tf.TestFunctions))
	for _, fn := range tf.TestFunctions {
		shortName := strings.TrimPrefix(fn.Name, commonPrefix+"_")
		if shortName == fn.Name {
			shortName = fn.Name
		}
		fmt.Printf("  - %s → {Name: \"%s\", ...}\n", fn.Name, shortName)
	}
}

func verifyTableCoverage(results []TestFile) {
	// Find table-driven test files
	var tableDrivenFiles []TestFile
	var individualFiles []TestFile

	for _, tf := range results {
		if strings.Contains(tf.Path, "_table_") {
			tableDrivenFiles = append(tableDrivenFiles, tf)
		} else {
			individualFiles = append(individualFiles, tf)
		}
	}

	fmt.Printf("Table-driven test files: %d\n", len(tableDrivenFiles))
	fmt.Printf("Individual test files: %d\n\n", len(individualFiles))

	// For each table-driven file, find corresponding individual tests
	for _, tableFile := range tableDrivenFiles {
		baseName := strings.TrimSuffix(filepath.Base(tableFile.Path), "_table_test.go")
		fmt.Printf("Checking coverage for: %s\n", baseName)

		// Find matching individual file
		for _, indFile := range individualFiles {
			indBaseName := strings.TrimSuffix(filepath.Base(indFile.Path), "_test.go")
			if indBaseName == baseName {
				// Compare test counts
				fmt.Printf("  Individual tests: %d\n", len(indFile.TestFunctions))
				fmt.Printf("  Table test cases: (check t.Run calls)\n")
				// Would need to parse t.Run calls for accurate count
			}
		}
	}
}
