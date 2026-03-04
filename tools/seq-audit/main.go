// seq-audit is a type-aware AST analyzer for finding unsafe sequence arithmetic.
//
// Unlike simple pattern matching, this tool uses Go's type checker to:
//   - Know actual types of variables (uint32, int32, etc.)
//   - Detect int32(uint32 - uint32) patterns that fail at 31-bit wraparound
//   - Track type conversions through expressions
//
// The key bug pattern we're looking for:
//
//	func SeqDiff(a, b uint32) int32 {
//	    return int32(a - b)  // BROKEN! Fails at wraparound
//	}
//
// When a=10 and b=0x7FFFFF00:
//   - a - b = 10 - 2147483392 = wraps to 0x80000110 (large uint32)
//   - int32(0x80000110) = -2147483376 (negative!)
//   - Should be ~265 (positive, because 10 is "after" MAX in circular space)
//
// Usage:
//
//	seq-audit [options] <packages...>
//
// Examples:
//
//	seq-audit ./congestion/live ./circular
//	seq-audit -verbose ./...
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// =============================================================================
// Data Structures
// =============================================================================

// Finding represents a potential issue found in the code
type Finding struct {
	File       string
	Line       int
	Column     int
	Severity   string // "HIGH", "MEDIUM", "LOW", "INFO"
	Category   string
	Pattern    string
	TypeInfo   string
	Context    string
	Suggestion string
}

func main() {
	verbose := flag.Bool("verbose", false, "Show all findings including INFO level")
	includeTests := flag.Bool("tests", false, "Also scan test files (finds documented bugs)")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		args = []string{"./congestion/live", "./circular"}
	}

	findings, err := analyzePackages(args, *includeTests)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Sort by severity then file
	sort.Slice(findings, func(i, j int) bool {
		sevOrder := map[string]int{"HIGH": 0, "MEDIUM": 1, "LOW": 2, "INFO": 3}
		if sevOrder[findings[i].Severity] != sevOrder[findings[j].Severity] {
			return sevOrder[findings[i].Severity] < sevOrder[findings[j].Severity]
		}
		return findings[i].File < findings[j].File
	})

	// Print findings and exit with error if HIGH severity found
	hasHigh := printFindings(findings, *verbose)
	if hasHigh {
		os.Exit(1)
	}
}

func analyzePackages(patterns []string, includeTests bool) ([]Finding, error) {
	// Load packages with full type information
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
		return nil, fmt.Errorf("loading packages: %w", err)
	}

	var allFindings []Finding

	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			for _, e := range pkg.Errors {
				fmt.Fprintf(os.Stderr, "Warning: %v\n", e)
			}
			continue
		}

		findings := analyzePackage(pkg)
		allFindings = append(allFindings, findings...)
	}

	return allFindings, nil
}

func analyzePackage(pkg *packages.Package) []Finding {
	var findings []Finding

	for i, file := range pkg.Syntax {
		if i >= len(pkg.CompiledGoFiles) {
			continue
		}
		filename := pkg.CompiledGoFiles[i]

		analyzer := &TypeAwareAnalyzer{
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

// =============================================================================
// Type-Aware AST Analyzer
// =============================================================================

type TypeAwareAnalyzer struct {
	pkg         *packages.Package
	fset        *token.FileSet
	info        *types.Info
	filename    string
	findings    *[]Finding
	currentFunc string
}

func (a *TypeAwareAnalyzer) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}

	switch node := n.(type) {
	case *ast.FuncDecl:
		a.currentFunc = node.Name.Name
		return a

	case *ast.CallExpr:
		a.checkTypeConversion(node)
	}

	return a
}

// checkTypeConversion looks for int32(uint32_expr) where uint32_expr is a subtraction
func (a *TypeAwareAnalyzer) checkTypeConversion(call *ast.CallExpr) {
	// Get the type of the conversion target
	convType := a.info.TypeOf(call.Fun)
	if convType == nil {
		return
	}

	// Check if it's a conversion to int32
	basic, ok := convType.(*types.Basic)
	if !ok || basic.Kind() != types.Int32 {
		return
	}

	if len(call.Args) != 1 {
		return
	}

	arg := call.Args[0]

	// Check if argument is a subtraction
	binExpr, ok := arg.(*ast.BinaryExpr)
	if !ok || binExpr.Op != token.SUB {
		return
	}

	// Get the type of the subtraction operands
	leftType := a.info.TypeOf(binExpr.X)
	rightType := a.info.TypeOf(binExpr.Y)

	if leftType == nil || rightType == nil {
		return
	}

	// Check if both operands are uint32
	leftBasic, leftOk := leftType.Underlying().(*types.Basic)
	rightBasic, rightOk := rightType.Underlying().(*types.Basic)

	if !leftOk || !rightOk {
		return
	}

	// Flag if we're converting (uint32 - uint32) to int32
	// This is the exact pattern that fails at 31-bit wraparound
	if leftBasic.Kind() == types.Uint32 && rightBasic.Kind() == types.Uint32 {
		pos := a.fset.Position(call.Pos())
		*a.findings = append(*a.findings, Finding{
			File:     a.filename,
			Line:     pos.Line,
			Column:   pos.Column,
			Severity: "HIGH",
			Category: "int32-uint32-subtraction",
			Pattern:  fmt.Sprintf("int32(%s - %s)", exprString(binExpr.X), exprString(binExpr.Y)),
			TypeInfo: "int32(uint32 - uint32)",
			Context:  fmt.Sprintf("function %s", a.currentFunc),
			Suggestion: "This pattern fails at 31-bit wraparound. " +
				"When a < b (in uint32 terms but a is 'after' b circularly), " +
				"a-b wraps to a large uint32 which becomes negative in int32. " +
				"Use threshold-based comparison instead.",
		})
	}

	// Also flag uint64 - uint64 → int64 if in sequence context
	if leftBasic.Kind() == types.Uint64 && rightBasic.Kind() == types.Uint64 {
		if isSequenceRelatedFunc(a.currentFunc) {
			pos := a.fset.Position(call.Pos())
			*a.findings = append(*a.findings, Finding{
				File:       a.filename,
				Line:       pos.Line,
				Column:     pos.Column,
				Severity:   "MEDIUM",
				Category:   "int64-uint64-subtraction",
				Pattern:    fmt.Sprintf("int64(%s - %s)", exprString(binExpr.X), exprString(binExpr.Y)),
				TypeInfo:   "int64(uint64 - uint64)",
				Context:    fmt.Sprintf("function %s", a.currentFunc),
				Suggestion: "Similar wraparound issue may exist for 64-bit sequences.",
			})
		}
	}
}

// isSequenceRelatedFunc checks if function name suggests sequence operations
func isSequenceRelatedFunc(name string) bool {
	hints := []string{"Seq", "seq", "Diff", "diff", "Distance", "Compare", "Less", "Greater"}
	for _, hint := range hints {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

// exprString returns a string representation of an expression
func exprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.CallExpr:
		return exprString(e.Fun) + "(...)"
	case *ast.IndexExpr:
		return exprString(e.X) + "[...]"
	case *ast.BasicLit:
		return e.Value
	case *ast.UnaryExpr:
		return fmt.Sprintf("%s%s", e.Op.String(), exprString(e.X))
	case *ast.ParenExpr:
		return "(" + exprString(e.X) + ")"
	case *ast.BinaryExpr:
		return fmt.Sprintf("%s %s %s", exprString(e.X), e.Op.String(), exprString(e.Y))
	default:
		return fmt.Sprintf("<%T>", expr)
	}
}

// =============================================================================
// Output
// =============================================================================

func printFindings(findings []Finding, verbose bool) bool {
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Println("TYPE-AWARE SEQUENCE ARITHMETIC AUDIT")
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Println("This tool uses Go's type checker to find int32(uint32 - uint32)")
	fmt.Println("patterns that fail at 31-bit sequence number wraparound.")
	fmt.Println()

	// Count by severity
	counts := map[string]int{}
	for i := range findings {
		counts[findings[i].Severity]++
	}

	fmt.Printf("Summary: %d HIGH, %d MEDIUM, %d LOW, %d INFO\n\n",
		counts["HIGH"], counts["MEDIUM"], counts["LOW"], counts["INFO"])

	if counts["HIGH"] == 0 && counts["MEDIUM"] == 0 && !verbose {
		fmt.Println("✅ No high/medium severity issues found!")
		fmt.Println()
		if counts["LOW"]+counts["INFO"] > 0 {
			fmt.Println("Run with -verbose to see all findings.")
		}
		return false // No HIGH severity issues
	}

	// Group by file
	byFile := make(map[string][]Finding)
	for i := range findings {
		f := &findings[i]
		if !verbose && (f.Severity == "LOW" || f.Severity == "INFO") {
			continue
		}
		byFile[f.File] = append(byFile[f.File], *f)
	}

	// Sort files
	var files []string
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	for _, file := range files {
		// Show relative path for readability
		displayFile := file
		if idx := strings.Index(file, "gosrt/"); idx >= 0 {
			displayFile = file[idx+6:]
		}

		fmt.Printf("📁 %s\n", displayFile)
		fmt.Println("───────────────────────────────────────────────────────────────────")

		fileFindings := byFile[file]
		for i := range fileFindings {
			f := &fileFindings[i]
			severityIcon := map[string]string{
				"HIGH":   "🔴",
				"MEDIUM": "🟠",
				"LOW":    "🟡",
				"INFO":   "ℹ️",
			}
			fmt.Printf("  %s [Line %d] %s\n", severityIcon[f.Severity], f.Line, f.Pattern)
			fmt.Printf("     Type: %s\n", f.TypeInfo)
			fmt.Printf("     Context: %s\n", f.Context)
			fmt.Printf("     💡 %s\n", f.Suggestion)
			fmt.Println()
		}
	}

	// Print explanation for HIGH severity
	if counts["HIGH"] > 0 {
		fmt.Println("═══════════════════════════════════════════════════════════════════")
		fmt.Println("🔴 WHY int32(uint32 - uint32) IS DANGEROUS")
		fmt.Println("═══════════════════════════════════════════════════════════════════")
		fmt.Println()
		fmt.Println("For 31-bit SRT sequence numbers (max = 0x7FFFFFFF = 2147483647):")
		fmt.Println()
		fmt.Println("  Example: a=10, b=0x7FFFFF00 (2147483392)")
		fmt.Println("  Circular meaning: 10 is ~265 packets AFTER 0x7FFFFF00 (wraparound)")
		fmt.Println()
		fmt.Println("  What happens:")
		fmt.Println("    a - b = 10 - 2147483392")
		fmt.Println("          = -2147483382 in signed math")
		fmt.Println("          = 0x80000110 in uint32 (wraps around)")
		fmt.Println("    int32(0x80000110) = -2147483376")
		fmt.Println()
		fmt.Println("  Expected: +265 (a is AFTER b in circular space)")
		fmt.Println("  Actual:   -2147483376 (WRONG!)")
		fmt.Println()
		fmt.Println("Fix: Use threshold-based comparison:")
		fmt.Println("  if d > seqThreshold31 { /* wraparound case */ }")
		fmt.Println()
	}

	return counts["HIGH"] > 0
}
