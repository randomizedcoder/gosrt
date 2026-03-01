// metrics-lock-analyzer is a static analysis tool that identifies metrics
// incremented within lock-protected critical sections and proposes
// transformations to move atomic operations outside of locks.
//
// Problem: Despite using atomic.Uint64, metrics are still being incremented
// INSIDE locked sections, negating the lock-free benefit and extending hold times.
//
// This tool:
// 1. Traces all lock acquisition/release call flows
// 2. Identifies atomic metric operations within critical sections
// 3. Proposes code transformations using stack-local variables
// 4. Generates actionable refactoring guidance
//
// Usage:
//
//	go run tools/metrics-lock-analyzer/main.go
//	make analyze-locks
//
// See documentation/metrics_lock_analysis_design.md for full design.
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

// =============================================================================
// Data Structures
// =============================================================================

// LockOperation represents a lock acquire or release operation
type LockOperation struct {
	File       string
	Line       int
	Column     int
	LockExpr   string // e.g., "r.lock", "c.handlePacketMutex"
	OpType     string // "Lock", "Unlock", "RLock", "RUnlock", "WithWLockTiming", "WithRLockTiming"
	Function   string // Enclosing function name
	IsAcquire  bool   // true for Lock/RLock, false for Unlock/RUnlock
	IsDeferred bool   // Whether this is via defer
}

// MetricOperation represents an atomic metric operation
type MetricOperation struct {
	File        string
	Line        int
	Column      int
	MetricExpr  string // e.g., "m.CongestionRecvPkt"
	MetricField string // e.g., "CongestionRecvPkt"
	Operation   string // "Add", "Store", "Swap", "Load"
	Argument    string // The argument expression (e.g., "1", "uint64(pktLen)")
	Function    string // Enclosing function name
	InLockScope bool   // Whether inside a detected lock scope
	LockExpr    string // Which lock this is under (if any)
}

// LockScope represents a region of code protected by a lock
type LockScope struct {
	File       string
	Function   string
	LockExpr   string
	LockType   string // "Lock", "RLock", "WithWLockTiming", "WithRLockTiming"
	StartLine  int
	EndLine    int
	Metrics    []*MetricOperation
	IsClosure  bool // true if this is a WithXLockTiming closure
	IsDeferred bool // true if unlock is via defer
}

// FunctionInfo tracks information about a function
type FunctionInfo struct {
	Name        string
	File        string
	StartLine   int
	EndLine     int
	HasLocks    bool
	HasMetrics  bool
	IsLockedFn  bool // true if name ends with "Locked" or "locked"
	CalledFuncs []string
}

// FileScanResult holds the scan results for a single file
type FileScanResult struct {
	File            string
	LockOperations  []LockOperation
	MetricOps       []MetricOperation
	Functions       []FunctionInfo
	LockScopes      []LockScope
	currentFunction string // tracking state during AST walk
}

// ScanResult holds the complete analysis results
type ScanResult struct {
	Files            []*FileScanResult
	TotalLockOps     int
	TotalMetricOps   int
	MetricsUnderLock int
	LockScopes       []LockScope
	// FunctionsUnderLock maps function names to lock info when they're called under lock
	FunctionsUnderLock map[string]string
}

// =============================================================================
// Main Entry Point
// =============================================================================

func main() {
	// Find the project root
	root := findProjectRoot()
	if root == "" {
		fmt.Println("ERROR: Could not find project root (looking for metrics/metrics.go)")
		os.Exit(2)
	}

	fmt.Println("=== GoSRT Metrics Lock Analyzer ===")
	fmt.Printf("Project root: %s\n\n", root)

	// Get files to analyze (from args or scan project)
	var filesToAnalyze []string
	if len(os.Args) > 1 {
		filesToAnalyze = os.Args[1:]
	} else {
		filesToAnalyze = findGoFiles(root)
	}

	// Phase 1: Scan all files
	fmt.Println("Phase 1: Scanning for lock and metric operations...")
	result := &ScanResult{
		FunctionsUnderLock: make(map[string]string),
	}
	fset := token.NewFileSet()

	for _, file := range filesToAnalyze {
		fileResult, err := scanFile(file, fset)
		if err != nil {
			continue // Skip files that don't parse
		}
		if fileResult != nil {
			result.Files = append(result.Files, fileResult)
			result.TotalLockOps += len(fileResult.LockOperations)
			result.TotalMetricOps += len(fileResult.MetricOps)
		}
	}

	fmt.Printf("  Scanned %d .go files\n", len(result.Files))
	fmt.Printf("  Found %d lock operations\n", result.TotalLockOps)
	fmt.Printf("  Found %d metric operations (.Add/.Store)\n\n", result.TotalMetricOps)

	// Phase 2: Build lock scopes and identify functions called under lock
	fmt.Println("Phase 2: Building lock scopes...")
	for _, f := range result.Files {
		f.buildLockScopes()
		result.LockScopes = append(result.LockScopes, f.LockScopes...)
		// Collect functions called under lock
		for _, scope := range f.LockScopes {
			if strings.Contains(scope.LockType, "via call") {
				result.FunctionsUnderLock[scope.Function] = scope.LockExpr
			}
		}
	}

	// Also mark any function with "Locked" suffix as called under lock
	for _, f := range result.Files {
		for _, fn := range f.Functions {
			if fn.IsLockedFn {
				if _, ok := result.FunctionsUnderLock[fn.Name]; !ok {
					result.FunctionsUnderLock[fn.Name] = "(Locked suffix)"
				}
			}
		}
	}

	// Phase 2.5: Propagate lock context - find functions called from *Locked functions
	// and mark them as under lock too (transitive closure)
	propagateLockedFunctions(result)

	fmt.Printf("  Found %d lock scopes\n", len(result.LockScopes))
	fmt.Printf("  Found %d functions called under lock\n\n", len(result.FunctionsUnderLock))

	// Phase 3: Correlate metrics with lock scopes
	fmt.Println("Phase 3: Correlating metrics with locks...")
	metricsUnderLock := 0
	for _, f := range result.Files {
		for i := range f.MetricOps {
			f.correlateMetricWithLocks(&f.MetricOps[i], result.FunctionsUnderLock)
			if f.MetricOps[i].InLockScope {
				metricsUnderLock++
			}
		}
	}
	result.MetricsUnderLock = metricsUnderLock
	fmt.Printf("  Found %d metrics under lock (%d%%)\n\n",
		metricsUnderLock,
		percentOf(metricsUnderLock, result.TotalMetricOps))

	// Phase 4: Generate report
	fmt.Println("=== Summary ===")
	fmt.Printf("  Total metric operations: %d\n", result.TotalMetricOps)
	fmt.Printf("  Metrics under lock: %d\n", result.MetricsUnderLock)
	fmt.Printf("  Metrics outside lock: %d\n", result.TotalMetricOps-result.MetricsUnderLock)
	fmt.Printf("  Lock scopes analyzed: %d\n", len(result.LockScopes))
	fmt.Println()

	// Report critical findings
	generateReport(result)
}

// =============================================================================
// File Scanning
// =============================================================================

// scanFile analyzes a single Go file for locks and metrics
func scanFile(path string, fset *token.FileSet) (*FileScanResult, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	result := &FileScanResult{
		File: path,
	}

	// Walk the AST to find functions first
	ast.Inspect(f, func(n ast.Node) bool {
		if node, ok := n.(*ast.FuncDecl); ok {
			result.recordFunction(node, fset)
		}
		return true
	})

	// Walk again to find lock and metric operations within function context
	var currentFunc string
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			currentFunc = node.Name.Name
			result.currentFunction = currentFunc
		case *ast.CallExpr:
			result.checkCallExpr(node, fset, currentFunc)
		}
		return true
	})

	return result, nil
}

// recordFunction captures function information
func (r *FileScanResult) recordFunction(fn *ast.FuncDecl, fset *token.FileSet) {
	pos := fset.Position(fn.Pos())
	endPos := fset.Position(fn.End())

	info := FunctionInfo{
		Name:       fn.Name.Name,
		File:       r.File,
		StartLine:  pos.Line,
		EndLine:    endPos.Line,
		IsLockedFn: strings.Contains(fn.Name.Name, "Locked") || strings.Contains(fn.Name.Name, "locked"),
	}

	r.Functions = append(r.Functions, info)
}

// checkCallExpr examines a call expression for lock/metric operations
func (r *FileScanResult) checkCallExpr(call *ast.CallExpr, fset *token.FileSet, currentFunc string) {
	// Check for method calls: x.Method()
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	method := sel.Sel.Name

	// Check for lock operations
	switch method {
	case "Lock":
		r.recordLockOp(call, sel.X, "Lock", true, fset, currentFunc)
	case "Unlock":
		r.recordLockOp(call, sel.X, "Unlock", false, fset, currentFunc)
	case "RLock":
		r.recordLockOp(call, sel.X, "RLock", true, fset, currentFunc)
	case "RUnlock":
		r.recordLockOp(call, sel.X, "RUnlock", false, fset, currentFunc)
	case "WithWLockTiming":
		r.recordWithLockTiming(call, "WithWLockTiming", fset, currentFunc)
	case "WithRLockTiming":
		r.recordWithLockTiming(call, "WithRLockTiming", fset, currentFunc)
	case "WithLockTiming":
		r.recordWithLockTiming(call, "WithLockTiming", fset, currentFunc)
	}

	// Check for metric operations: x.Field.Add() or x.Field.Store()
	if method == "Add" || method == "Store" || method == "Swap" || method == "Load" {
		r.checkMetricOp(call, sel, method, fset, currentFunc)
	}
}

// recordLockOp records a lock acquire/release operation
func (r *FileScanResult) recordLockOp(call *ast.CallExpr, lockExpr ast.Expr, opType string, isAcquire bool, fset *token.FileSet, currentFunc string) {
	pos := fset.Position(call.Pos())

	op := LockOperation{
		File:      r.File,
		Line:      pos.Line,
		Column:    pos.Column,
		LockExpr:  exprToString(lockExpr),
		OpType:    opType,
		Function:  currentFunc,
		IsAcquire: isAcquire,
	}

	r.LockOperations = append(r.LockOperations, op)
}

// recordWithLockTiming handles WithWLockTiming/WithRLockTiming patterns
func (r *FileScanResult) recordWithLockTiming(call *ast.CallExpr, opType string, fset *token.FileSet, currentFunc string) {
	// WithWLockTiming(metrics, mutex, func() { ... })
	// Arguments: [0]=metrics, [1]=mutex, [2]=callback function
	if len(call.Args) < 3 {
		return
	}

	// Extract the mutex expression (arg 1, may need to dereference &r.lock)
	mutexExpr := exprToString(call.Args[1])

	pos := fset.Position(call.Pos())

	op := LockOperation{
		File:      r.File,
		Line:      pos.Line,
		Column:    pos.Column,
		LockExpr:  mutexExpr,
		OpType:    opType,
		Function:  currentFunc,
		IsAcquire: true, // WithXLockTiming is effectively an acquire
	}

	r.LockOperations = append(r.LockOperations, op)

	// Analyze the closure body for metric operations
	if fn, ok := call.Args[2].(*ast.FuncLit); ok {
		r.analyzeClosureForMetrics(fn, mutexExpr, opType, fset, currentFunc)
	}
}

// analyzeClosureForMetrics scans a closure for metric operations
func (r *FileScanResult) analyzeClosureForMetrics(fn *ast.FuncLit, lockExpr string, lockType string, fset *token.FileSet, parentFunc string) {
	// Record the lock scope for the closure
	startPos := fset.Position(fn.Pos())
	endPos := fset.Position(fn.End())

	scope := LockScope{
		File:      r.File,
		Function:  parentFunc,
		LockExpr:  lockExpr,
		LockType:  lockType,
		StartLine: startPos.Line,
		EndLine:   endPos.Line,
		IsClosure: true,
	}

	// Walk the closure body for metric operations and function calls
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if isCall {
			if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel {
				method := sel.Sel.Name
				if method == "Add" || method == "Store" || method == "Swap" {
					metricOp := r.extractMetricOp(call, sel, method, fset, parentFunc)
					if metricOp != nil {
						metricOp.InLockScope = true
						metricOp.LockExpr = lockExpr
						scope.Metrics = append(scope.Metrics, metricOp)
					}
				}
			}

			// Check for calls to *Locked functions (method calls like r.pushLocked())
			if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel {
				calledFunc := sel.Sel.Name
				// Record ANY function called from within the closure as potentially under lock
				// Functions with "Locked" in name are definitely under lock
				if strings.Contains(calledFunc, "Locked") || strings.Contains(calledFunc, "locked") {
					r.recordLockedFunctionCall(calledFunc, lockExpr, lockType, parentFunc, fset, call)
				}
			}
			// Check for plain function calls (ident calls like pushLocked())
			if ident, isIdent := call.Fun.(*ast.Ident); isIdent {
				if strings.Contains(ident.Name, "Locked") || strings.Contains(ident.Name, "locked") {
					r.recordLockedFunctionCall(ident.Name, lockExpr, lockType, parentFunc, fset, call)
				}
			}
		}
		return true
	})

	r.LockScopes = append(r.LockScopes, scope)
}

// recordLockedFunctionCall notes when a *Locked function is called under lock
func (r *FileScanResult) recordLockedFunctionCall(funcName string, lockExpr string, lockType string, callerFunc string, fset *token.FileSet, call *ast.CallExpr) {
	pos := fset.Position(call.Pos())

	// Create a lock scope entry marking this function as called under lock
	scope := LockScope{
		File:      r.File,
		Function:  funcName,
		LockExpr:  lockExpr,
		LockType:  lockType + " (via call)",
		StartLine: pos.Line,
		EndLine:   pos.Line,
		IsClosure: false,
	}

	r.LockScopes = append(r.LockScopes, scope)
}

// checkMetricOp checks if a call is a metric operation
func (r *FileScanResult) checkMetricOp(call *ast.CallExpr, sel *ast.SelectorExpr, method string, fset *token.FileSet, currentFunc string) {
	metricOp := r.extractMetricOp(call, sel, method, fset, currentFunc)
	if metricOp != nil {
		r.MetricOps = append(r.MetricOps, *metricOp)
	}
}

// extractMetricOp extracts metric operation details
func (r *FileScanResult) extractMetricOp(call *ast.CallExpr, sel *ast.SelectorExpr, method string, fset *token.FileSet, currentFunc string) *MetricOperation {
	// Get the field selector: x.FieldName
	fieldSel, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	fieldName := fieldSel.Sel.Name

	// Check if it looks like a metric field (capital letter)
	if len(fieldName) == 0 || fieldName[0] < 'A' || fieldName[0] > 'Z' {
		return nil
	}

	// Skip .Load() calls as they're reads, not writes
	if method == "Load" {
		return nil
	}

	// Extract the argument
	argStr := ""
	if len(call.Args) > 0 {
		argStr = exprToString(call.Args[0])
	}

	pos := fset.Position(call.Pos())

	return &MetricOperation{
		File:        r.File,
		Line:        pos.Line,
		Column:      pos.Column,
		MetricExpr:  exprToString(fieldSel),
		MetricField: fieldName,
		Operation:   method,
		Argument:    argStr,
		Function:    currentFunc,
	}
}

// =============================================================================
// Lock Scope Building
// =============================================================================

// buildLockScopes correlates lock/unlock pairs into scopes
func (r *FileScanResult) buildLockScopes() {
	// Group lock operations by function
	locksByFunc := make(map[string][]LockOperation)
	for _, op := range r.LockOperations {
		locksByFunc[op.Function] = append(locksByFunc[op.Function], op)
	}

	// For each function, try to match lock/unlock pairs
	for funcName, ops := range locksByFunc {
		// Separate acquires and releases
		var acquires, releases []LockOperation
		for _, op := range ops {
			// Skip WithXLockTiming - they're handled specially in analyzeClosureForMetrics
			if strings.HasPrefix(op.OpType, "With") {
				continue
			}
			if op.IsAcquire {
				acquires = append(acquires, op)
			} else {
				releases = append(releases, op)
			}
		}

		// Simple matching: pair by lock expression and order
		// This is a simplification - real code might have more complex patterns
		lockExprs := make(map[string][]LockOperation)
		for _, acq := range acquires {
			lockExprs[acq.LockExpr] = append(lockExprs[acq.LockExpr], acq)
		}

		for _, rel := range releases {
			acqs := lockExprs[rel.LockExpr]
			if len(acqs) > 0 {
				// Match with the most recent acquire before this release
				var bestAcq *LockOperation
				for i := range acqs {
					if acqs[i].Line < rel.Line {
						bestAcq = &acqs[i]
					}
				}
				if bestAcq != nil {
					scope := LockScope{
						File:      r.File,
						Function:  funcName,
						LockExpr:  rel.LockExpr,
						LockType:  bestAcq.OpType,
						StartLine: bestAcq.Line,
						EndLine:   rel.Line,
						IsClosure: false,
					}
					r.LockScopes = append(r.LockScopes, scope)
				}
			}
		}
	}
}

// correlateMetricWithLocks determines if a metric is inside a lock scope
func (r *FileScanResult) correlateMetricWithLocks(metric *MetricOperation, functionsUnderLock map[string]string) {
	// Check if already marked (from closure analysis)
	if metric.InLockScope {
		return
	}

	// Check against all lock scopes in this file
	for i := range r.LockScopes {
		scope := &r.LockScopes[i]
		if scope.Function == metric.Function &&
			metric.Line >= scope.StartLine &&
			metric.Line <= scope.EndLine {
			metric.InLockScope = true
			metric.LockExpr = scope.LockExpr
			scope.Metrics = append(scope.Metrics, metric)
			return
		}
	}

	// Check if metric is in a function that's called under lock (global map)
	if lockExpr, ok := functionsUnderLock[metric.Function]; ok {
		metric.InLockScope = true
		metric.LockExpr = lockExpr
		return
	}

	// Check if in a *Locked function (local to this file)
	for _, fn := range r.Functions {
		if fn.Name == metric.Function && fn.IsLockedFn {
			metric.InLockScope = true
			metric.LockExpr = "(Locked suffix)"
			return
		}
	}
}

// =============================================================================
// Lock Context Propagation
// =============================================================================

// propagateLockedFunctions scans *Locked functions to find what they call
// and marks those called functions as also being under lock
func propagateLockedFunctions(result *ScanResult) {
	// Build a map of function name -> function body for quick lookup
	funcBodies := make(map[string]*ast.BlockStmt)

	// We need to re-parse files to get the function bodies
	for _, f := range result.Files {
		fset := token.NewFileSet()
		astFile, err := parser.ParseFile(fset, f.File, nil, 0)
		if err != nil {
			continue
		}

		ast.Inspect(astFile, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok && fn.Body != nil {
				funcBodies[fn.Name.Name] = fn.Body
			}
			return true
		})
	}

	// For each function that's under lock, find ALL functions it calls (not just *Locked ones)
	// Then mark those called functions as under lock
	changed := true
	iterations := 0
	maxIterations := 10 // Prevent infinite loops

	for changed && iterations < maxIterations {
		changed = false
		iterations++

		// Get current list of functions under lock (copy keys to avoid mutation during iteration)
		funcList := make([]string, 0, len(result.FunctionsUnderLock))
		for funcName := range result.FunctionsUnderLock {
			funcList = append(funcList, funcName)
		}

		for _, funcName := range funcList {
			lockExpr := result.FunctionsUnderLock[funcName]
			body, ok := funcBodies[funcName]
			if !ok {
				continue
			}

			// Find all function calls in this function
			ast.Inspect(body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}

				var calledFunc string

				// Check for method calls (r.methodName())
				if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel {
					calledFunc = sel.Sel.Name
				}

				// Check for plain function calls
				if ident, isIdent := call.Fun.(*ast.Ident); isIdent {
					calledFunc = ident.Name
				}

				if calledFunc == "" {
					return true
				}

				// Check if the called function already tracked as under lock
				if _, already := result.FunctionsUnderLock[calledFunc]; already {
					return true
				}

				// Propagate lock context to any function called from a *Locked function
				// This captures the transitive closure of functions under lock
				callerIsLocked := strings.Contains(funcName, "Locked") || strings.Contains(funcName, "locked")
				calleeIsLocked := strings.Contains(calledFunc, "Locked") || strings.Contains(calledFunc, "locked")

				// If caller is *Locked, propagate to ANY callee that exists in codebase
				if callerIsLocked {
					if _, exists := funcBodies[calledFunc]; exists {
						result.FunctionsUnderLock[calledFunc] = fmt.Sprintf("%s (via %s)", lockExpr, funcName)
						changed = true
						return true
					}
				}

				// Always track any *Locked function that's called
				if calleeIsLocked {
					result.FunctionsUnderLock[calledFunc] = fmt.Sprintf("%s (via %s)", lockExpr, funcName)
					changed = true
				}

				return true
			})
		}
	}
}

// =============================================================================
// Report Generation
// =============================================================================

func generateReport(result *ScanResult) {
	fmt.Println("=== Critical Findings ===")
	fmt.Println()

	// Group metrics by file and function
	type functionMetrics struct {
		file     string
		function string
		lockExpr string
		metrics  []*MetricOperation
	}

	funcMetrics := make(map[string]*functionMetrics)

	for _, f := range result.Files {
		for i := range f.MetricOps {
			m := &f.MetricOps[i]
			if !m.InLockScope {
				continue
			}

			key := fmt.Sprintf("%s:%s", f.File, m.Function)
			if _, ok := funcMetrics[key]; !ok {
				funcMetrics[key] = &functionMetrics{
					file:     f.File,
					function: m.Function,
					lockExpr: m.LockExpr,
				}
			}
			funcMetrics[key].metrics = append(funcMetrics[key].metrics, m)
		}
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(funcMetrics))
	for k := range funcMetrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Print findings
	for _, key := range keys {
		fm := funcMetrics[key]
		if len(fm.metrics) == 0 {
			continue
		}

		// Get relative path
		relPath := fm.file
		if idx := strings.Index(relPath, "gosrt/"); idx >= 0 {
			relPath = relPath[idx+6:]
		}

		fmt.Printf("File: %s\n", relPath)
		fmt.Printf("  Function: %s\n", fm.function)
		fmt.Printf("  Lock: %s\n", fm.lockExpr)
		fmt.Printf("  Metrics under lock: %d\n", len(fm.metrics))

		// Sort metrics by line number
		sort.Slice(fm.metrics, func(i, j int) bool {
			return fm.metrics[i].Line < fm.metrics[j].Line
		})

		for _, m := range fm.metrics {
			fmt.Printf("    - %s.%s(%s) [line %d]\n",
				m.MetricField, m.Operation, m.Argument, m.Line)
		}
		fmt.Println()
	}

	// Summary of extractable metrics
	fmt.Println("=== Extraction Potential ===")
	fmt.Println()

	// Count by file
	type fileStats struct {
		file             string
		metricsUnderLock int
		extractable      int
	}

	fileStatsMap := make(map[string]*fileStats)
	for _, f := range result.Files {
		stats := &fileStats{file: f.File}
		for j := range f.MetricOps {
			m := &f.MetricOps[j]
			if m.InLockScope {
				stats.metricsUnderLock++
				// For now, assume all are extractable (Phase 3 will refine)
				stats.extractable++
			}
		}
		if stats.metricsUnderLock > 0 {
			fileStatsMap[f.File] = stats
		}
	}

	fileStatsList := make([]*fileStats, 0, len(fileStatsMap))
	for _, s := range fileStatsMap {
		fileStatsList = append(fileStatsList, s)
	}
	sort.Slice(fileStatsList, func(i, j int) bool {
		return fileStatsList[i].metricsUnderLock > fileStatsList[j].metricsUnderLock
	})

	fmt.Println("| File | Metrics Under Lock | Extractable (est.) |")
	fmt.Println("|------|-------------------|-------------------|")
	for _, s := range fileStatsList {
		relPath := s.file
		if idx := strings.Index(relPath, "gosrt/"); idx >= 0 {
			relPath = relPath[idx+6:]
		}
		fmt.Printf("| %s | %d | %d |\n", relPath, s.metricsUnderLock, s.extractable)
	}
	fmt.Println()

	// Recommendations
	fmt.Println("=== Recommendations ===")
	fmt.Println()
	fmt.Println("1. High Priority: Extract metrics from pushLockedNakBtree() and pushLockedOriginal()")
	fmt.Println("   - These are called on every packet (~4300/sec at 50 Mb/s)")
	fmt.Println("   - 15+ atomic operations per call under lock")
	fmt.Println()
	fmt.Println("2. Use stack variables to capture metric deltas inside lock")
	fmt.Println("   - Declare: var incPkt uint64 = 1")
	fmt.Println("   - After lock release: m.CongestionRecvPkt.Add(incPkt)")
	fmt.Println()
	fmt.Println("3. See documentation/metrics_lock_analysis_design.md for transformation patterns")
	fmt.Println()
}

// =============================================================================
// Utilities
// =============================================================================

// findProjectRoot looks for the directory containing metrics/metrics.go
func findProjectRoot() string {
	// Try current directory first
	if _, err := os.Stat("metrics/metrics.go"); err == nil {
		return "."
	}

	// Try parent directories
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to get current directory: %v\n", err)
		return ""
	}
	for i := 0; i < 5; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "metrics/metrics.go")); statErr == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}

	return ""
}

// findGoFiles finds all .go files in the project (excluding vendor, tools, tests)
func findGoFiles(root string) []string {
	var files []string

	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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

		files = append(files, path)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error walking directory %s: %v\n", root, err)
	}

	return files
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
	case *ast.UnaryExpr:
		if t.Op.String() == "&" {
			return "&" + exprToString(t.X)
		}
		return t.Op.String() + exprToString(t.X)
	case *ast.CallExpr:
		return exprToString(t.Fun) + "(...)"
	case *ast.BasicLit:
		return t.Value
	case *ast.BinaryExpr:
		return exprToString(t.X) + " " + t.Op.String() + " " + exprToString(t.Y)
	case *ast.ParenExpr:
		return "(" + exprToString(t.X) + ")"
	case *ast.IndexExpr:
		return exprToString(t.X) + "[" + exprToString(t.Index) + "]"
	case *ast.TypeAssertExpr:
		return exprToString(t.X) + ".(type)"
	default:
		return fmt.Sprintf("<%T>", expr)
	}
}

// percentOf calculates percentage safely
func percentOf(part, total int) int {
	if total == 0 {
		return 0
	}
	return (part * 100) / total
}
