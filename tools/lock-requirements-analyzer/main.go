// lock-requirements-analyzer is a static analysis tool that identifies operations
// that MUST be protected by locks in the GoSRT codebase.
//
// Protected Resources:
// 1. Packet btree (packetStore) - Insert, Remove, Iterate, Has, Min
// 2. NAK btree (nakBtree) - Insert, Delete, Iterate, Len
// 3. Rate calculations - rate.*, avgPayloadSize, avgLinkCapacity
//
// This tool:
// 1. Finds all operations on protected data structures
// 2. Checks if they're within a lock scope
// 3. Reports any unprotected operations (potential race conditions)
//
// Usage:
//
//	go run tools/lock-requirements-analyzer/main.go
//	make analyze-lock-requirements
//
// This is the complement to metrics-lock-analyzer:
// - metrics-lock-analyzer: finds metrics that DON'T need locks
// - lock-requirements-analyzer: finds operations that DO need locks
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
// Protected Operations Configuration
// =============================================================================

// ProtectedResource defines a resource that requires lock protection
type ProtectedResource struct {
	Name        string   // Human-readable name
	FieldNames  []string // Field names that access this resource (e.g., "packetStore", "nakBtree")
	Methods     []string // Methods that require protection (e.g., "Insert", "Delete")
	Description string   // Why this needs protection
}

// Define all protected resources
var protectedResources = []ProtectedResource{
	{
		Name:       "Packet Store (btree)",
		FieldNames: []string{"packetStore"},
		Methods: []string{
			"Insert", "Has", "Min", "Clear",
			"Iterate", "IterateFrom",
			"RemoveAll", "Remove",
		},
		Description: "Packet reordering buffer - concurrent access causes data races",
	},
	{
		Name:       "NAK Btree",
		FieldNames: []string{"nakBtree"},
		Methods: []string{
			"Insert", "InsertBatch",
			"Delete", "DeleteBefore",
			"Iterate", "Len",
		},
		Description: "Missing sequence tracking - concurrent modification causes corruption",
	},
	{
		Name:       "Rate Counters",
		FieldNames: []string{"rate"},
		Methods:    []string{
			// Direct field access counts - we track assignments
		},
		Description: "Rate statistics - non-atomic updates cause data races (TODO: migrate to atomics)",
	},
	{
		Name:        "Running Averages",
		FieldNames:  []string{"avgPayloadSize", "avgLinkCapacity"},
		Methods:     []string{}, // Direct field access
		Description: "EMA calculations - non-atomic read-modify-write causes races (TODO: migrate to atomics)",
	},
	{
		Name:        "Sequence Numbers",
		FieldNames:  []string{"lastACKSequenceNumber", "lastDeliveredSequenceNumber", "maxSeenSequenceNumber", "ackScanHighWaterMark"},
		Methods:     []string{},
		Description: "Protocol state - must be consistent with packet store operations",
	},
	{
		Name:       "Loss List (sender)",
		FieldNames: []string{"lossList"},
		Methods: []string{
			"Push", "Pop", "Front", "Init", "Len",
		},
		Description: "Sender loss tracking - concurrent access causes corruption",
	},
}

// =============================================================================
// Data Structures
// =============================================================================

// ProtectedOperation represents an operation on a protected resource
type ProtectedOperation struct {
	File         string
	Line         int
	Column       int
	Function     string
	Resource     string // Which protected resource
	Operation    string // Method or field access
	Expression   string // Full expression
	IsProtected  bool   // Is this within a lock scope?
	LockExpr     string // Which lock protects it (if any)
	NeedsReview  bool   // Flagged for manual review
	ReviewReason string
}

// LockScope represents a region protected by a lock
type LockScope struct {
	File      string
	Function  string
	LockExpr  string
	LockType  string
	StartLine int
	EndLine   int
}

// FileScanResult holds scan results for a file
type FileScanResult struct {
	File            string
	ProtectedOps    []ProtectedOperation
	LockScopes      []LockScope
	Functions       []FunctionInfo
	currentFunction string
}

// FunctionInfo tracks function information
type FunctionInfo struct {
	Name       string
	StartLine  int
	EndLine    int
	IsLockedFn bool
}

// ScanResult holds complete analysis results
type ScanResult struct {
	Files              []*FileScanResult
	TotalProtectedOps  int
	ProtectedOpsInLock int
	UnprotectedOps     int
	FunctionsUnderLock map[string]string
}

// =============================================================================
// Main Entry Point
// =============================================================================

func main() {
	root := findProjectRoot()
	if root == "" {
		fmt.Println("ERROR: Could not find project root")
		os.Exit(2)
	}

	fmt.Println("=== GoSRT Lock Requirements Analyzer ===")
	fmt.Printf("Project root: %s\n\n", root)

	// Get files to analyze
	var filesToAnalyze []string
	if len(os.Args) > 1 {
		filesToAnalyze = os.Args[1:]
	} else {
		filesToAnalyze = findGoFiles(root)
	}

	// Phase 1: Scan for protected operations and lock scopes
	fmt.Println("Phase 1: Scanning for protected operations and lock scopes...")
	result := &ScanResult{
		FunctionsUnderLock: make(map[string]string),
	}
	fset := token.NewFileSet()

	for _, file := range filesToAnalyze {
		fileResult, err := scanFile(file, fset)
		if err != nil {
			continue
		}
		if fileResult != nil {
			result.Files = append(result.Files, fileResult)
			result.TotalProtectedOps += len(fileResult.ProtectedOps)
		}
	}

	fmt.Printf("  Scanned %d files\n", len(result.Files))
	fmt.Printf("  Found %d protected operations\n\n", result.TotalProtectedOps)

	// Phase 2: Build lock scopes and identify functions under lock
	fmt.Println("Phase 2: Building lock scopes...")
	for _, f := range result.Files {
		f.buildLockScopes()
		// Collect functions called under lock
		for _, scope := range f.LockScopes {
			if strings.Contains(scope.LockType, "via call") {
				result.FunctionsUnderLock[scope.Function] = scope.LockExpr
			}
		}
	}

	// Mark *Locked functions as under lock
	for _, f := range result.Files {
		for _, fn := range f.Functions {
			if fn.IsLockedFn {
				if _, ok := result.FunctionsUnderLock[fn.Name]; !ok {
					result.FunctionsUnderLock[fn.Name] = "(Locked suffix)"
				}
			}
		}
	}

	// Propagate lock context
	propagateLockedFunctions(result)
	fmt.Printf("  Found %d functions under lock\n\n", len(result.FunctionsUnderLock))

	// Phase 3: Check if protected operations are within lock scopes
	fmt.Println("Phase 3: Verifying lock protection...")
	for _, f := range result.Files {
		for i := range f.ProtectedOps {
			f.checkProtection(&f.ProtectedOps[i], result.FunctionsUnderLock)
			if f.ProtectedOps[i].IsProtected {
				result.ProtectedOpsInLock++
			} else {
				result.UnprotectedOps++
			}
		}
	}

	fmt.Printf("  Protected operations in lock: %d\n", result.ProtectedOpsInLock)
	fmt.Printf("  Potentially unprotected: %d\n\n", result.UnprotectedOps)

	// Generate report
	generateReport(result)
}

// =============================================================================
// File Scanning
// =============================================================================

func scanFile(path string, fset *token.FileSet) (*FileScanResult, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	result := &FileScanResult{
		File: path,
	}

	// Find functions
	ast.Inspect(f, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok {
			result.recordFunction(fn, fset)
		}
		return true
	})

	// Find lock operations and protected operations
	var currentFunc string
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			currentFunc = node.Name.Name
			result.currentFunction = currentFunc
		case *ast.CallExpr:
			result.checkForLockOps(node, fset, currentFunc)
			result.checkForProtectedOps(node, fset, currentFunc)
		case *ast.AssignStmt:
			result.checkForProtectedAssign(node, fset, currentFunc)
		}
		return true
	})

	return result, nil
}

func (r *FileScanResult) recordFunction(fn *ast.FuncDecl, fset *token.FileSet) {
	pos := fset.Position(fn.Pos())
	endPos := fset.Position(fn.End())

	r.Functions = append(r.Functions, FunctionInfo{
		Name:       fn.Name.Name,
		StartLine:  pos.Line,
		EndLine:    endPos.Line,
		IsLockedFn: strings.Contains(fn.Name.Name, "Locked") || strings.Contains(fn.Name.Name, "locked"),
	})
}

func (r *FileScanResult) checkForLockOps(call *ast.CallExpr, fset *token.FileSet, currentFunc string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	method := sel.Sel.Name
	switch method {
	case "Lock", "RLock":
		r.recordLockAcquire(call, sel.X, method, fset, currentFunc)
	case "Unlock", "RUnlock":
		r.recordLockRelease(call, sel.X, method, fset, currentFunc)
	case "WithWLockTiming", "WithRLockTiming", "WithLockTiming":
		r.recordWithLockTiming(call, method, fset, currentFunc)
	}
}

func (r *FileScanResult) recordLockAcquire(call *ast.CallExpr, lockExpr ast.Expr, lockType string, fset *token.FileSet, currentFunc string) {
	pos := fset.Position(call.Pos())
	scope := LockScope{
		File:      r.File,
		Function:  currentFunc,
		LockExpr:  exprToString(lockExpr),
		LockType:  lockType,
		StartLine: pos.Line,
		EndLine:   0, // Will be filled by matching release
	}
	r.LockScopes = append(r.LockScopes, scope)
}

func (r *FileScanResult) recordLockRelease(call *ast.CallExpr, lockExpr ast.Expr, lockType string, fset *token.FileSet, currentFunc string) {
	pos := fset.Position(call.Pos())
	lockName := exprToString(lockExpr)

	// Find matching acquire and set end line
	for i := len(r.LockScopes) - 1; i >= 0; i-- {
		if r.LockScopes[i].Function == currentFunc && r.LockScopes[i].LockExpr == lockName && r.LockScopes[i].EndLine == 0 {
			r.LockScopes[i].EndLine = pos.Line
			break
		}
	}
}

func (r *FileScanResult) recordWithLockTiming(call *ast.CallExpr, lockType string, fset *token.FileSet, currentFunc string) {
	if len(call.Args) < 3 {
		return
	}

	mutexExpr := exprToString(call.Args[1])
	pos := fset.Position(call.Pos())

	// Get closure end position
	endLine := pos.Line
	if fn, ok := call.Args[2].(*ast.FuncLit); ok {
		endPos := fset.Position(fn.End())
		endLine = endPos.Line

		// Scan closure for calls to *Locked functions
		r.scanClosureForLockedCalls(fn, mutexExpr, lockType, fset, currentFunc)
	}

	scope := LockScope{
		File:      r.File,
		Function:  currentFunc,
		LockExpr:  mutexExpr,
		LockType:  lockType,
		StartLine: pos.Line,
		EndLine:   endLine,
	}
	r.LockScopes = append(r.LockScopes, scope)
}

func (r *FileScanResult) scanClosureForLockedCalls(fn *ast.FuncLit, lockExpr, lockType string, fset *token.FileSet, parentFunc string) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		var calledFunc string
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			calledFunc = sel.Sel.Name
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			calledFunc = ident.Name
		}

		if calledFunc != "" && (strings.Contains(calledFunc, "Locked") || strings.Contains(calledFunc, "locked")) {
			pos := fset.Position(call.Pos())
			scope := LockScope{
				File:      r.File,
				Function:  calledFunc,
				LockExpr:  lockExpr,
				LockType:  lockType + " (via call)",
				StartLine: pos.Line,
				EndLine:   pos.Line,
			}
			r.LockScopes = append(r.LockScopes, scope)
		}
		return true
	})
}

// checkForProtectedOps checks if a call expression accesses protected resources
func (r *FileScanResult) checkForProtectedOps(call *ast.CallExpr, fset *token.FileSet, currentFunc string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	method := sel.Sel.Name

	// Check if this is a method call on a protected resource
	for _, res := range protectedResources {
		// Check if method matches
		isProtectedMethod := false
		for _, m := range res.Methods {
			if m == method {
				isProtectedMethod = true
				break
			}
		}
		if !isProtectedMethod {
			continue
		}

		// Check if receiver is a protected field
		receiverName := extractReceiverFieldName(sel.X)
		for _, field := range res.FieldNames {
			if strings.Contains(receiverName, field) {
				pos := fset.Position(call.Pos())
				r.ProtectedOps = append(r.ProtectedOps, ProtectedOperation{
					File:       r.File,
					Line:       pos.Line,
					Column:     pos.Column,
					Function:   currentFunc,
					Resource:   res.Name,
					Operation:  method,
					Expression: exprToString(call.Fun) + "(...)",
				})
				return
			}
		}
	}
}

// checkForProtectedAssign checks for assignments to protected fields
func (r *FileScanResult) checkForProtectedAssign(assign *ast.AssignStmt, fset *token.FileSet, currentFunc string) {
	for _, lhs := range assign.Lhs {
		fieldName := extractFieldPath(lhs)
		if fieldName == "" {
			continue
		}

		// Check against protected resources
		for _, res := range protectedResources {
			for _, protectedField := range res.FieldNames {
				if strings.Contains(fieldName, protectedField) {
					pos := fset.Position(assign.Pos())

					// Determine if it's a read-modify-write (e.g., r.rate.packets++)
					opType := "assign"
					if assign.Tok.String() == "+=" || assign.Tok.String() == "-=" {
						opType = "read-modify-write"
					}

					r.ProtectedOps = append(r.ProtectedOps, ProtectedOperation{
						File:       r.File,
						Line:       pos.Line,
						Column:     pos.Column,
						Function:   currentFunc,
						Resource:   res.Name,
						Operation:  opType,
						Expression: fieldName,
					})
					return
				}
			}
		}
	}
}

func extractReceiverFieldName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return extractReceiverFieldName(e.X) + "." + e.Sel.Name
	case *ast.Ident:
		return e.Name
	default:
		return ""
	}
}

func extractFieldPath(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		base := extractFieldPath(e.X)
		if base != "" {
			return base + "." + e.Sel.Name
		}
		return e.Sel.Name
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return extractFieldPath(e.X) + "[...]"
	default:
		return ""
	}
}

// =============================================================================
// Lock Scope Building
// =============================================================================

func (r *FileScanResult) buildLockScopes() {
	// Fill in any missing end lines (defer patterns)
	for i := range r.LockScopes {
		if r.LockScopes[i].EndLine == 0 {
			// Find function end as fallback
			for _, fn := range r.Functions {
				if fn.Name == r.LockScopes[i].Function {
					r.LockScopes[i].EndLine = fn.EndLine
					break
				}
			}
		}
	}
}

func (r *FileScanResult) checkProtection(op *ProtectedOperation, functionsUnderLock map[string]string) {
	// Check if operation is within a lock scope
	for _, scope := range r.LockScopes {
		if scope.Function == op.Function &&
			op.Line >= scope.StartLine &&
			op.Line <= scope.EndLine {
			op.IsProtected = true
			op.LockExpr = scope.LockExpr
			return
		}
	}

	// Check if function is under lock (via call or *Locked suffix)
	if lockExpr, ok := functionsUnderLock[op.Function]; ok {
		op.IsProtected = true
		op.LockExpr = lockExpr
		return
	}

	// Check for *Locked* function pattern (Locked can be in middle, e.g., pushLockedNakBtree)
	if strings.Contains(op.Function, "Locked") || strings.Contains(op.Function, "locked") {
		op.IsProtected = true
		op.LockExpr = "(Locked suffix)"
		return
	}

	// Special case: nakBtree has its own internal lock, so some operations may be safe
	if op.Resource == "NAK Btree" && (op.Operation == "InsertBatch" || op.Operation == "DeleteBefore" || op.Operation == "Len") {
		op.IsProtected = true
		op.LockExpr = "(nakBtree internal lock)"
		op.NeedsReview = true
		op.ReviewReason = "NAK btree has internal lock - verify this is sufficient"
		return
	}

	// Not protected
	op.IsProtected = false
}

// =============================================================================
// Lock Context Propagation
// =============================================================================

func propagateLockedFunctions(result *ScanResult) {
	funcBodies := make(map[string]*ast.BlockStmt)

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

	changed := true
	iterations := 0
	for changed && iterations < 10 {
		changed = false
		iterations++

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

			ast.Inspect(body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				var calledFunc string
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					calledFunc = sel.Sel.Name
				}
				if ident, ok := call.Fun.(*ast.Ident); ok {
					calledFunc = ident.Name
				}

				if calledFunc == "" {
					return true
				}

				if _, already := result.FunctionsUnderLock[calledFunc]; already {
					return true
				}

				callerIsLocked := strings.Contains(funcName, "Locked") || strings.Contains(funcName, "locked")
				calleeIsLocked := strings.Contains(calledFunc, "Locked") || strings.Contains(calledFunc, "locked")

				if callerIsLocked {
					if _, exists := funcBodies[calledFunc]; exists {
						result.FunctionsUnderLock[calledFunc] = fmt.Sprintf("%s (via %s)", lockExpr, funcName)
						changed = true
						return true
					}
				}

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
	fmt.Println("=== Protected Resources Summary ===")
	fmt.Println()

	for _, res := range protectedResources {
		fmt.Printf("Resource: %s\n", res.Name)
		fmt.Printf("  Fields: %v\n", res.FieldNames)
		fmt.Printf("  Protected Methods: %v\n", res.Methods)
		fmt.Printf("  Why: %s\n\n", res.Description)
	}

	// Group operations by protection status
	var unprotected []ProtectedOperation
	var needsReview []ProtectedOperation
	var protected []ProtectedOperation

	for _, f := range result.Files {
		for _, op := range f.ProtectedOps {
			if !op.IsProtected {
				unprotected = append(unprotected, op)
			} else if op.NeedsReview {
				needsReview = append(needsReview, op)
			} else {
				protected = append(protected, op)
			}
		}
	}

	// Report unprotected operations (CRITICAL)
	if len(unprotected) > 0 {
		fmt.Println("=== ❌ UNPROTECTED OPERATIONS (Potential Race Conditions) ===")
		fmt.Println()

		// Group by file and function
		byFunc := make(map[string][]ProtectedOperation)
		for _, op := range unprotected {
			key := fmt.Sprintf("%s:%s", op.File, op.Function)
			byFunc[key] = append(byFunc[key], op)
		}

		var keys []string
		for k := range byFunc {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			ops := byFunc[key]
			if len(ops) == 0 {
				continue
			}

			relPath := ops[0].File
			if idx := strings.Index(relPath, "gosrt/"); idx >= 0 {
				relPath = relPath[idx+6:]
			}

			fmt.Printf("File: %s\n", relPath)
			fmt.Printf("  Function: %s\n", ops[0].Function)
			fmt.Printf("  Unprotected operations:\n")

			for _, op := range ops {
				fmt.Printf("    ⚠️  %s.%s [line %d] - %s\n",
					op.Resource, op.Operation, op.Line, op.Expression)
			}
			fmt.Println()
		}
	}

	// Report operations needing review
	if len(needsReview) > 0 {
		fmt.Println("=== ⚠️  NEEDS REVIEW (May Be Safe) ===")
		fmt.Println()

		for _, op := range needsReview {
			relPath := op.File
			if idx := strings.Index(relPath, "gosrt/"); idx >= 0 {
				relPath = relPath[idx+6:]
			}
			fmt.Printf("  %s:%d %s.%s - %s\n",
				relPath, op.Line, op.Resource, op.Operation, op.ReviewReason)
		}
		fmt.Println()
	}

	// Summary table
	fmt.Println("=== Summary by Resource ===")
	fmt.Println()

	type resourceStats struct {
		name        string
		protected   int
		unprotected int
		needsReview int
	}

	stats := make(map[string]*resourceStats)
	for _, res := range protectedResources {
		stats[res.Name] = &resourceStats{name: res.Name}
	}

	for _, f := range result.Files {
		for _, op := range f.ProtectedOps {
			s := stats[op.Resource]
			if s == nil {
				continue
			}
			if !op.IsProtected {
				s.unprotected++
			} else if op.NeedsReview {
				s.needsReview++
			} else {
				s.protected++
			}
		}
	}

	fmt.Println("| Resource | Protected | Unprotected | Needs Review |")
	fmt.Println("|----------|-----------|-------------|--------------|")
	for _, res := range protectedResources {
		s := stats[res.Name]
		if s.protected+s.unprotected+s.needsReview > 0 {
			fmt.Printf("| %s | %d | %d | %d |\n",
				s.name, s.protected, s.unprotected, s.needsReview)
		}
	}
	fmt.Println()

	// Final summary
	fmt.Println("=== Final Summary ===")
	fmt.Printf("  Total protected operations: %d\n", result.TotalProtectedOps)
	fmt.Printf("  ✅ Operations in lock scope: %d\n", result.ProtectedOpsInLock)
	fmt.Printf("  ❌ Potentially unprotected: %d\n", result.UnprotectedOps)
	fmt.Println()

	if result.UnprotectedOps > 0 {
		fmt.Println("⚠️  WARNING: Found potentially unprotected operations!")
		fmt.Println("   Review the operations above to determine if they need lock protection.")
		fmt.Println()
	} else {
		fmt.Println("✅ All protected operations appear to be within lock scopes.")
		fmt.Println()
	}
}

// =============================================================================
// Utilities
// =============================================================================

func findProjectRoot() string {
	if _, err := os.Stat("metrics/metrics.go"); err == nil {
		return "."
	}

	dir, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "metrics/metrics.go")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}

	return ""
}

func findGoFiles(root string) []string {
	var files []string

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		if strings.Contains(path, "vendor/") ||
			strings.Contains(path, "tools/") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}

		files = append(files, path)
		return nil
	})

	return files
}

func exprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(t.X)
	case *ast.UnaryExpr:
		return t.Op.String() + exprToString(t.X)
	case *ast.CallExpr:
		return exprToString(t.Fun) + "(...)"
	case *ast.BasicLit:
		return t.Value
	case *ast.BinaryExpr:
		return exprToString(t.X) + " " + t.Op.String() + " " + exprToString(t.Y)
	case *ast.IndexExpr:
		return exprToString(t.X) + "[...]"
	default:
		return fmt.Sprintf("<%T>", expr)
	}
}
