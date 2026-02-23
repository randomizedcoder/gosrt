# Metrics Lock Analysis Design

## Overview

This document describes the design for a static analysis tool that will identify metrics incremented within lock-protected critical sections and propose transformations to move atomic operations outside of locks using stack-local variables.

**Problem Statement**: Despite migrating to atomic counters (`atomic.Uint64`), metrics are still being incremented **inside** locked sections. This negates the lock-free benefit of atomics and contributes to lock contention.

**Goal**: Create a tool that:
1. Traces all lock acquisition/release call flows
2. Identifies atomic metric operations within critical sections
3. Proposes code transformations using stack-local variables
4. Generates actionable refactoring guidance

**Related Documents**:
- [`metrics_and_statistics_design.md`](./metrics_and_statistics_design.md) - Original atomic migration design
- [`receive_lock_contention_analysis.md`](./receive_lock_contention_analysis.md) - Lock contention evidence
- [`tools/metrics-audit/main.go`](../tools/metrics-audit/main.go) - Existing metrics audit tool

---

## Table of Contents

1. [Problem Analysis](#1-problem-analysis)
2. [Design Principles](#2-design-principles)
3. [Tool Architecture](#3-tool-architecture)
4. [AST Analysis Implementation](#4-ast-analysis-implementation)
5. [Lock Flow Tracking](#5-lock-flow-tracking)
6. [Metrics Detection](#6-metrics-detection)
7. [Transformation Proposals](#7-transformation-proposals)
8. [Output Format](#8-output-format)
9. [Implementation Plan](#9-implementation-plan)

---

## 1. Problem Analysis

### 1.1 The Contradiction

The `metrics_and_statistics_design.md` document specifies atomic counters to **eliminate lock contention**:

```go
// Design intent: Lock-free increments
metrics.PktRecvACKSuccess.Add(1)  // atomic, no lock needed
```

However, the current implementation has these atomic operations **inside** locked sections:

```go
// Actual implementation (receive.go:260-270)
func (r *receiver) Push(pkt packet.Packet) {
    if r.lockTiming != nil {
        metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
            r.pushLocked(pkt)  // ← All metrics inside here!
        })
        return
    }
    r.lock.Lock()
    defer r.lock.Unlock()
    r.pushLocked(pkt)
}
```

Inside `pushLocked()` (lines 285-362):
```go
func (r *receiver) pushLockedNakBtree(pkt packet.Packet) {
    m := r.metrics
    // ❌ ALL of these are INSIDE the lock:
    m.CongestionRecvPkt.Add(1)
    m.CongestionRecvByte.Add(uint64(pktLen))
    m.CongestionRecvPktRetrans.Add(1)
    m.CongestionRecvByteRetrans.Add(uint64(pktLen))
    m.CongestionRecvPktBelated.Add(1)
    m.CongestionRecvByteBelated.Add(uint64(pktLen))
    // ... 15+ more atomic operations under lock
}
```

### 1.2 Why This Matters

Each atomic operation inside a lock:
1. **Extends lock hold time**: ~20-50ns per atomic add
2. **Blocks other goroutines**: While holding the lock
3. **Negates atomic benefits**: Atomics are lock-free, but we're holding a lock anyway

At 50 Mb/s (~4300 packets/sec):
- 15 atomic ops per packet = 64,500 unnecessary atomic ops/sec under lock
- Each adds ~20-50ns = 1.3-3.2ms total lock time added per second

### 1.3 The Solution Pattern

Move atomic operations OUTSIDE the lock using stack-local variables:

```go
// BEFORE (metrics under lock)
r.lock.Lock()
m.CongestionRecvPkt.Add(1)
m.CongestionRecvByte.Add(pktLen)
if isRetrans {
    m.CongestionRecvPktRetrans.Add(1)
}
r.packetStore.Insert(pkt)  // ← Only this NEEDS the lock
r.lock.Unlock()

// AFTER (metrics outside lock)
var (
    incPkt      uint64 = 1
    incByte     uint64 = pktLen
    incRetrans  uint64 = 0
)
if isRetrans {
    incRetrans = 1
}

r.lock.Lock()
r.packetStore.Insert(pkt)  // ← Only this under lock
r.lock.Unlock()

// Update metrics AFTER lock released
m.CongestionRecvPkt.Add(incPkt)
m.CongestionRecvByte.Add(incByte)
if incRetrans > 0 {
    m.CongestionRecvPktRetrans.Add(incRetrans)
}
```

---

## 2. Design Principles

### 2.1 Analysis Requirements

The tool must:

1. **Track lock acquisition/release pairs**
   - `lock.Lock()` / `lock.Unlock()`
   - `lock.RLock()` / `lock.RUnlock()`
   - `WithWLockTiming()` / `WithRLockTiming()` helper calls
   - `defer` patterns

2. **Identify metrics operations**
   - `.Add()` calls on `atomic.Uint64`
   - `.Store()` calls on `atomic.Uint64`
   - `.Swap()` calls (if used)
   - Helper functions like `metrics.IncrementRecvDataDrop()`

3. **Determine operation necessity**
   - Does the operation READ state protected by the lock?
   - Does the operation WRITE state protected by the lock?
   - Could the operation be deferred to after unlock?

4. **Propose transformations**
   - Stack variable declarations
   - Value computation inside lock
   - Atomic updates outside lock

### 2.2 Safety Constraints

Transformations must preserve correctness:

1. **Data dependencies**: If metric value depends on locked state, compute value inside lock
2. **Control flow**: If metric is conditionally updated based on locked state, capture condition
3. **Early returns**: Handle cases where lock is released via early return
4. **Defer patterns**: Properly handle `defer unlock()` patterns

---

## 3. Tool Architecture

### 3.1 High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    metrics-lock-audit                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐       │
│  │  AST Parser  │───▶│ Lock Tracker │───▶│ Metrics      │       │
│  │              │    │              │    │ Detector     │       │
│  └──────────────┘    └──────────────┘    └──────────────┘       │
│         │                   │                   │                │
│         │                   │                   │                │
│         ▼                   ▼                   ▼                │
│  ┌──────────────────────────────────────────────────────┐       │
│  │              Call Flow Analyzer                       │       │
│  │                                                       │       │
│  │   • Tracks lock state through function calls          │       │
│  │   • Builds call graph for "under lock" analysis       │       │
│  │   • Handles closures (WithWLockTiming callbacks)      │       │
│  └──────────────────────────────────────────────────────┘       │
│                            │                                     │
│                            ▼                                     │
│  ┌──────────────────────────────────────────────────────┐       │
│  │           Transformation Generator                    │       │
│  │                                                       │       │
│  │   • Proposes stack variable patterns                  │       │
│  │   • Identifies safe extraction points                 │       │
│  │   • Generates refactoring code                        │       │
│  └──────────────────────────────────────────────────────┘       │
│                            │                                     │
│                            ▼                                     │
│  ┌──────────────────────────────────────────────────────┐       │
│  │              Report Generator                         │       │
│  │                                                       │       │
│  │   • Markdown report with findings                     │       │
│  │   • Per-function analysis                             │       │
│  │   • Suggested code changes                            │       │
│  └──────────────────────────────────────────────────────┘       │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 Data Structures

```go
// LockAcquisition represents a lock acquire operation
type LockAcquisition struct {
    File        string
    Line        int
    Column      int
    LockExpr    string         // e.g., "r.lock", "c.handlePacketMutex"
    LockType    string         // "Lock", "RLock", "WithWLockTiming", "WithRLockTiming"
    Function    string         // Enclosing function name
    IsDeferred  bool           // Whether unlock is via defer
    ClosureFunc *ast.FuncLit   // For WithXLockTiming, the callback closure
}

// LockRelease represents a lock release operation
type LockRelease struct {
    File       string
    Line       int
    Column     int
    LockExpr   string
    ReleaseType string         // "Unlock", "RUnlock"
    Function   string
    IsDeferred bool
}

// LockScope represents the span between lock acquire and release
type LockScope struct {
    Acquisition *LockAcquisition
    Release     *LockRelease
    StartLine   int
    EndLine     int
    Function    string
    Metrics     []MetricOperation  // Metrics ops within this scope
    CalledFuncs []string           // Functions called within scope (may contain metrics)
}

// MetricOperation represents an atomic metric operation
type MetricOperation struct {
    File         string
    Line         int
    Column       int
    MetricExpr   string    // e.g., "m.CongestionRecvPkt"
    MetricField  string    // e.g., "CongestionRecvPkt"
    Operation    string    // "Add", "Store", "Swap"
    Argument     string    // The argument expression (e.g., "1", "uint64(pktLen)")
    InLockScope  bool      // Whether inside a lock scope
    LockScopes   []string  // Names of locks held when this op executes
    CanDefer     bool      // Whether this op can be moved outside lock
    DeferReason  string    // If CanDefer=false, why not
}

// CallFlow represents a function call that may contain metrics or locks
type CallFlow struct {
    CallerFunc    string
    CallerFile    string
    CallerLine    int
    CalleeFunc    string
    InLockScope   bool
    LockScopes    []string
}

// TransformationProposal suggests how to extract metrics from lock
type TransformationProposal struct {
    File              string
    Function          string
    LockScope         *LockScope
    StackVariables    []StackVariable
    PreLockCode       string     // Code to add before lock
    InsideLockCode    string     // Modified code inside lock
    PostLockCode      string     // Code to add after lock (metric updates)
}

// StackVariable represents a local variable to capture metric delta
type StackVariable struct {
    Name         string     // e.g., "incPkt"
    Type         string     // e.g., "uint64"
    InitValue    string     // e.g., "0" or "1"
    MetricField  string     // Which metric this captures
    Operation    string     // "Add" or "Store"
}
```

---

## 4. AST Analysis Implementation

### 4.1 File Scanner

Extend the existing `metrics-audit` pattern:

```go
// ScanResult holds analysis results for the entire codebase
type ScanResult struct {
    LockAcquisitions []LockAcquisition
    LockReleases     []LockRelease
    LockScopes       []LockScope
    MetricOperations []MetricOperation
    CallFlows        []CallFlow
    Transformations  []TransformationProposal
}

// ScanFile analyzes a single Go file for locks and metrics
func ScanFile(path string, fset *token.FileSet) (*FileScanResult, error) {
    f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
    if err != nil {
        return nil, err
    }

    result := &FileScanResult{
        File: path,
    }

    // Phase 1: Find all lock acquisitions and releases
    ast.Inspect(f, func(n ast.Node) bool {
        result.findLockOperations(n, fset)
        return true
    })

    // Phase 2: Find all metric operations
    ast.Inspect(f, func(n ast.Node) bool {
        result.findMetricOperations(n, fset)
        return true
    })

    // Phase 3: Find all function calls (for call flow analysis)
    ast.Inspect(f, func(n ast.Node) bool {
        result.findCallFlows(n, fset)
        return true
    })

    // Phase 4: Build lock scopes and correlate with metrics
    result.buildLockScopes()

    return result, nil
}
```

### 4.2 Lock Operation Detection

```go
// findLockOperations identifies lock acquire/release calls
func (r *FileScanResult) findLockOperations(n ast.Node, fset *token.FileSet) {
    call, ok := n.(*ast.CallExpr)
    if !ok {
        return
    }

    // Check for method calls: x.Lock(), x.Unlock(), x.RLock(), x.RUnlock()
    if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
        method := sel.Sel.Name
        switch method {
        case "Lock":
            r.recordLockAcquisition(call, sel.X, "Lock", fset)
        case "Unlock":
            r.recordLockRelease(call, sel.X, "Unlock", fset)
        case "RLock":
            r.recordLockAcquisition(call, sel.X, "RLock", fset)
        case "RUnlock":
            r.recordLockRelease(call, sel.X, "RUnlock", fset)
        }
    }

    // Check for helper calls: WithWLockTiming(), WithRLockTiming()
    if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
        method := sel.Sel.Name
        if method == "WithWLockTiming" || method == "WithRLockTiming" {
            r.recordHelperLock(call, method, fset)
        }
    }

    // Also check for package-level calls: metrics.WithWLockTiming()
    if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
        if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "metrics" {
            method := sel.Sel.Name
            if method == "WithWLockTiming" || method == "WithRLockTiming" {
                r.recordHelperLock(call, method, fset)
            }
        }
    }
}

// recordHelperLock handles WithWLockTiming/WithRLockTiming patterns
func (r *FileScanResult) recordHelperLock(call *ast.CallExpr, method string, fset *token.FileSet) {
    // WithWLockTiming(metrics, mutex, func() { ... })
    // Arguments: [0]=metrics, [1]=mutex, [2]=callback function
    if len(call.Args) < 3 {
        return
    }

    // Extract the mutex expression (arg 1)
    mutexExpr := exprToString(call.Args[1])

    // Extract the callback closure (arg 2)
    var closureFunc *ast.FuncLit
    if fn, ok := call.Args[2].(*ast.FuncLit); ok {
        closureFunc = fn
    }

    pos := fset.Position(call.Pos())

    r.LockAcquisitions = append(r.LockAcquisitions, LockAcquisition{
        File:        r.File,
        Line:        pos.Line,
        Column:      pos.Column,
        LockExpr:    mutexExpr,
        LockType:    method,
        ClosureFunc: closureFunc,
        Function:    r.currentFunction,
    })
}
```

### 4.3 Metric Operation Detection

```go
// findMetricOperations identifies atomic metric operations
func (r *FileScanResult) findMetricOperations(n ast.Node, fset *token.FileSet) {
    call, ok := n.(*ast.CallExpr)
    if !ok {
        return
    }

    // Check for: x.FieldName.Add() or x.FieldName.Store()
    sel, ok := call.Fun.(*ast.SelectorExpr)
    if !ok {
        return
    }

    method := sel.Sel.Name
    if method != "Add" && method != "Store" && method != "Swap" {
        return
    }

    // Get the field selector: x.FieldName
    fieldSel, ok := sel.X.(*ast.SelectorExpr)
    if !ok {
        return
    }

    fieldName := fieldSel.Sel.Name

    // Check if it looks like a metric field (capital letter)
    if len(fieldName) == 0 || fieldName[0] < 'A' || fieldName[0] > 'Z' {
        return
    }

    // Extract the argument
    argStr := ""
    if len(call.Args) > 0 {
        argStr = exprToString(call.Args[0])
    }

    pos := fset.Position(call.Pos())

    r.MetricOperations = append(r.MetricOperations, MetricOperation{
        File:        r.File,
        Line:        pos.Line,
        Column:      pos.Column,
        MetricExpr:  exprToString(fieldSel),
        MetricField: fieldName,
        Operation:   method,
        Argument:    argStr,
        Function:    r.currentFunction,
    })
}

// Also detect helper function calls that update metrics
func (r *FileScanResult) findMetricHelperCalls(n ast.Node, fset *token.FileSet) {
    call, ok := n.(*ast.CallExpr)
    if !ok {
        return
    }

    // Check for: metrics.IncrementRecvDataDrop(), metrics.IncrementSendDataDrop(), etc.
    sel, ok := call.Fun.(*ast.SelectorExpr)
    if !ok {
        return
    }

    pkg, ok := sel.X.(*ast.Ident)
    if !ok || pkg.Name != "metrics" {
        return
    }

    // Known helper functions that update metrics internally
    helperFuncs := map[string]bool{
        "IncrementRecvDataDrop":  true,
        "IncrementSendDataDrop":  true,
        "IncrementSendErrorDrop": true,
        "IncrementRecvErrorDrop": true,
        "CountNAKEntries":        true,
    }

    if !helperFuncs[sel.Sel.Name] {
        return
    }

    pos := fset.Position(call.Pos())

    r.MetricHelperCalls = append(r.MetricHelperCalls, MetricHelperCall{
        File:     r.File,
        Line:     pos.Line,
        Function: r.currentFunction,
        Helper:   sel.Sel.Name,
        Args:     call.Args,
    })
}
```

---

## 5. Lock Flow Tracking

### 5.1 Call Graph for Lock Context

To determine if a metric operation is "under lock", we need to track:
1. Direct lock/unlock calls
2. Function calls made while lock is held
3. Closures passed to WithWLockTiming that execute under lock

```go
// CallGraphNode represents a function in the call graph
type CallGraphNode struct {
    Name           string
    File           string
    ContainsLocks  bool               // Does this function acquire locks?
    ContainsMetrics bool              // Does this function update metrics?
    Callees        []string           // Functions this calls
    Callers        []string           // Functions that call this
    LockContext    []string           // Locks held when entering this function
}

// BuildCallGraph creates a call graph for lock/metric analysis
func BuildCallGraph(files []*FileScanResult) *CallGraph {
    graph := &CallGraph{
        Nodes: make(map[string]*CallGraphNode),
    }

    // Phase 1: Create nodes for all functions
    for _, f := range files {
        for _, fn := range f.Functions {
            graph.Nodes[fn.Name] = &CallGraphNode{
                Name: fn.Name,
                File: f.File,
            }
        }
    }

    // Phase 2: Add call edges
    for _, f := range files {
        for _, call := range f.CallFlows {
            if caller, ok := graph.Nodes[call.CallerFunc]; ok {
                caller.Callees = append(caller.Callees, call.CalleeFunc)
            }
            if callee, ok := graph.Nodes[call.CalleeFunc]; ok {
                callee.Callers = append(callee.Callers, call.CallerFunc)
            }
        }
    }

    // Phase 3: Mark functions that contain locks/metrics
    for _, f := range files {
        for _, lock := range f.LockAcquisitions {
            if node, ok := graph.Nodes[lock.Function]; ok {
                node.ContainsLocks = true
            }
        }
        for _, metric := range f.MetricOperations {
            if node, ok := graph.Nodes[metric.Function]; ok {
                node.ContainsMetrics = true
            }
        }
    }

    return graph
}
```

### 5.2 Lock Scope Correlation

```go
// BuildLockScopes correlates locks with metrics within them
func (r *FileScanResult) BuildLockScopes() {
    // For traditional lock/unlock pairs
    r.buildTraditionalLockScopes()

    // For WithWLockTiming/WithRLockTiming patterns
    r.buildHelperLockScopes()
}

// buildTraditionalLockScopes handles lock.Lock() / lock.Unlock() patterns
func (r *FileScanResult) buildTraditionalLockScopes() {
    // Group by function
    locksByFunc := make(map[string][]LockAcquisition)
    unlocksByFunc := make(map[string][]LockRelease)

    for _, acq := range r.LockAcquisitions {
        if acq.LockType == "Lock" || acq.LockType == "RLock" {
            locksByFunc[acq.Function] = append(locksByFunc[acq.Function], acq)
        }
    }
    for _, rel := range r.LockReleases {
        unlocksByFunc[rel.Function] = append(unlocksByFunc[rel.Function], rel)
    }

    // Match lock/unlock pairs (simplified - real impl needs better matching)
    for funcName, locks := range locksByFunc {
        unlocks := unlocksByFunc[funcName]
        for i, lock := range locks {
            if i < len(unlocks) {
                scope := LockScope{
                    Acquisition: &lock,
                    Release:     &unlocks[i],
                    StartLine:   lock.Line,
                    EndLine:     unlocks[i].Line,
                    Function:    funcName,
                }
                r.findMetricsInScope(&scope)
                r.LockScopes = append(r.LockScopes, scope)
            }
        }
    }
}

// buildHelperLockScopes handles WithWLockTiming patterns
func (r *FileScanResult) buildHelperLockScopes() {
    for _, acq := range r.LockAcquisitions {
        if acq.LockType != "WithWLockTiming" && acq.LockType != "WithRLockTiming" {
            continue
        }

        if acq.ClosureFunc == nil {
            continue
        }

        // The entire closure body is "under lock"
        scope := LockScope{
            Acquisition: &acq,
            Release:     nil, // Implicit unlock after closure
            StartLine:   acq.Line,
            EndLine:     endLineOfNode(acq.ClosureFunc),
            Function:    acq.Function,
            IsClosure:   true,
        }

        // Find metrics inside the closure
        r.findMetricsInClosure(&scope, acq.ClosureFunc)
        r.LockScopes = append(r.LockScopes, scope)
    }
}

// findMetricsInScope identifies metric operations within a lock scope
func (r *FileScanResult) findMetricsInScope(scope *LockScope) {
    for i := range r.MetricOperations {
        metric := &r.MetricOperations[i]
        if metric.Function == scope.Function &&
           metric.Line >= scope.StartLine &&
           metric.Line <= scope.EndLine {
            metric.InLockScope = true
            metric.LockScopes = append(metric.LockScopes, scope.Acquisition.LockExpr)
            scope.Metrics = append(scope.Metrics, *metric)
        }
    }
}
```

### 5.3 Transitive Lock Context

Handle functions called from within locked sections:

```go
// PropagageLockContext marks metrics in functions called under lock
func (graph *CallGraph) PropagateLockContext(scopes []LockScope) {
    // Build set of functions called under lock
    underLock := make(map[string][]string) // function -> locks held

    for _, scope := range scopes {
        for _, callee := range scope.CalledFuncs {
            underLock[callee] = append(underLock[callee], scope.Acquisition.LockExpr)
        }
    }

    // Propagate transitively
    changed := true
    for changed {
        changed = false
        for funcName, locks := range underLock {
            if node, ok := graph.Nodes[funcName]; ok {
                for _, callee := range node.Callees {
                    existingLocks := underLock[callee]
                    for _, lock := range locks {
                        if !contains(existingLocks, lock) {
                            underLock[callee] = append(underLock[callee], lock)
                            changed = true
                        }
                    }
                }
            }
        }
    }

    // Update nodes with lock context
    for funcName, locks := range underLock {
        if node, ok := graph.Nodes[funcName]; ok {
            node.LockContext = locks
        }
    }
}
```

---

## 6. Metrics Detection

### 6.1 Known Metric Patterns

The tool should recognize these patterns:

```go
// Pattern 1: Direct field access
m.CongestionRecvPkt.Add(1)
m.CongestionRecvByte.Add(uint64(pktLen))

// Pattern 2: Conditional metrics
if pkt.Header().RetransmittedPacketFlag {
    m.CongestionRecvPktRetrans.Add(1)
}

// Pattern 3: Helper functions
metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))

// Pattern 4: Store operations
m.CongestionRecvMsBuf.Store(msBuf)

// Pattern 5: Decrement via Add with complement
m.CongestionRecvPktBuf.Add(^uint64(0))  // Decrement by 1

// Pattern 6: Computed values
m.CongestionRecvByteBuf.Add(^uint64(uint64(p.Len()) - 1))
```

### 6.2 Dependency Analysis

For each metric operation, determine if it can be moved outside the lock:

```go
// CanDeferMetric determines if a metric op can move outside lock
func CanDeferMetric(op *MetricOperation, scope *LockScope, fset *token.FileSet) (bool, string) {
    // Check if the argument expression reads protected state
    argDeps := analyzeExprDependencies(op.Argument)

    // These variables are typically protected by r.lock:
    protectedState := map[string]bool{
        "r.lastACKSequenceNumber":       true,
        "r.lastDeliveredSequenceNumber": true,
        "r.maxSeenSequenceNumber":       true,
        "r.nPackets":                    true,
        "r.avgPayloadSize":              true,
        // etc.
    }

    for _, dep := range argDeps {
        if protectedState[dep] {
            // The metric value depends on locked state
            // BUT: we can still capture the VALUE inside, then Add outside
            return true, "capture_value"
        }
    }

    // Check if the metric itself is conditional on locked state
    if op.IsConditional && conditionReadsLockedState(op.Condition, protectedState) {
        return true, "capture_condition"
    }

    // Pure increments (Add(1)) can always be deferred
    if op.Operation == "Add" && op.Argument == "1" {
        return true, "pure_increment"
    }

    return true, "no_dependencies"
}
```

---

## 7. Transformation Proposals

### 7.1 Transformation Patterns

#### Pattern A: Simple Increment Extraction

```go
// BEFORE
r.lock.Lock()
m.CongestionRecvPkt.Add(1)
m.CongestionRecvByte.Add(uint64(pktLen))
// ... other locked operations ...
r.lock.Unlock()

// AFTER
pktLen := pkt.Len()  // Capture before lock if needed

r.lock.Lock()
// ... only operations that need lock ...
r.lock.Unlock()

m.CongestionRecvPkt.Add(1)
m.CongestionRecvByte.Add(uint64(pktLen))
```

#### Pattern B: Conditional Metric Extraction

```go
// BEFORE
r.lock.Lock()
if pkt.Header().RetransmittedPacketFlag {
    m.CongestionRecvPktRetrans.Add(1)
    m.CongestionRecvByteRetrans.Add(uint64(pktLen))
}
// ... other locked operations ...
r.lock.Unlock()

// AFTER
isRetrans := pkt.Header().RetransmittedPacketFlag  // Capture condition
pktLen := pkt.Len()

r.lock.Lock()
// ... only operations that need lock ...
r.lock.Unlock()

if isRetrans {
    m.CongestionRecvPktRetrans.Add(1)
    m.CongestionRecvByteRetrans.Add(uint64(pktLen))
}
```

#### Pattern C: Value-Dependent Metric Extraction

```go
// BEFORE
r.lock.Lock()
// Value computed from locked state
skippedCount := uint64(h.PacketSequenceNumber.Distance(ackSequenceNumber))
if skippedCount > 1 {
    m.CongestionRecvPktSkippedTSBPD.Add(skippedCount - 1)
}
// ... update ackSequenceNumber (needs lock) ...
r.lock.Unlock()

// AFTER
var totalSkippedPkts uint64  // Stack accumulator

r.lock.Lock()
skippedCount := uint64(h.PacketSequenceNumber.Distance(ackSequenceNumber))
if skippedCount > 1 {
    totalSkippedPkts = skippedCount - 1  // Capture value, don't call atomic
}
// ... update ackSequenceNumber (needs lock) ...
r.lock.Unlock()

if totalSkippedPkts > 0 {
    m.CongestionRecvPktSkippedTSBPD.Add(totalSkippedPkts)
}
```

#### Pattern D: Multiple Metric Batching

```go
// BEFORE
r.lock.Lock()
m.CongestionRecvPktBuf.Add(1)
m.CongestionRecvPktUnique.Add(1)
m.CongestionRecvByteBuf.Add(uint64(pktLen))
m.CongestionRecvByteUnique.Add(uint64(pktLen))
// ... packet store insert ...
r.lock.Unlock()

// AFTER (batch all metric updates)
pktLen := uint64(pkt.Len())

r.lock.Lock()
inserted := r.packetStore.Insert(pkt)  // May fail
r.lock.Unlock()

if inserted {
    m.CongestionRecvPktBuf.Add(1)
    m.CongestionRecvPktUnique.Add(1)
    m.CongestionRecvByteBuf.Add(pktLen)
    m.CongestionRecvByteUnique.Add(pktLen)
}
```

### 7.2 Transformation Generator

```go
// GenerateTransformation creates a transformation proposal for a lock scope
func GenerateTransformation(scope *LockScope, callGraph *CallGraph) *TransformationProposal {
    proposal := &TransformationProposal{
        File:      scope.Acquisition.File,
        Function:  scope.Function,
        LockScope: scope,
    }

    // Group metrics by their dependency type
    var simpleIncrements []MetricOperation
    var conditionalMetrics []MetricOperation
    var valueDependentMetrics []MetricOperation

    for _, metric := range scope.Metrics {
        canDefer, reason := CanDeferMetric(&metric, scope, nil)
        if !canDefer {
            continue // Can't extract this one
        }

        switch reason {
        case "pure_increment":
            simpleIncrements = append(simpleIncrements, metric)
        case "capture_condition":
            conditionalMetrics = append(conditionalMetrics, metric)
        case "capture_value":
            valueDependentMetrics = append(valueDependentMetrics, metric)
        }
    }

    // Generate stack variables for value captures
    for i, metric := range valueDependentMetrics {
        varName := fmt.Sprintf("inc%s", metric.MetricField)
        proposal.StackVariables = append(proposal.StackVariables, StackVariable{
            Name:        varName,
            Type:        "uint64",
            InitValue:   "0",
            MetricField: metric.MetricField,
            Operation:   metric.Operation,
        })
    }

    // Generate pre-lock, inside-lock, and post-lock code
    proposal.PreLockCode = generatePreLockCode(proposal.StackVariables)
    proposal.InsideLockCode = generateInsideLockCode(scope, valueDependentMetrics)
    proposal.PostLockCode = generatePostLockCode(simpleIncrements, conditionalMetrics, proposal.StackVariables)

    return proposal
}
```

---

## 8. Output Format

### 8.1 Report Structure

The tool generates a Markdown report.

**Latest Tool Output** (2025-12-17):
```
=== GoSRT Metrics Lock Analyzer ===
  Scanned 78 .go files
  Found 331 lock operations
  Found 255 metric operations (.Add/.Store)
  Found 184 lock scopes
  Found 25 functions called under lock
  Found 44 metrics under lock (17%)

=== Summary ===
  Total metric operations: 255
  Metrics under lock: 44
  Metrics outside lock: 211
  Lock scopes analyzed: 184
```

**Sample Report Format**:

```markdown
# Metrics Lock Analysis Report

Generated: 2025-12-17 10:30:00

## Summary

- Files analyzed: 78
- Lock scopes found: 184
- Metrics under lock: 44
- Metrics outside lock: 211 (83%)
- Estimated lock time savings: ~TBD ms/sec at 50 Mb/s

## Critical Findings

### 1. congestion/live/receive.go:Push()

**Lock**: `r.lock` (Write)
**Metrics Under Lock**: 15
**Extractable**: 15 (100%)

| Metric | Operation | Argument | Can Extract | Reason |
|--------|-----------|----------|-------------|--------|
| `CongestionRecvPkt` | Add | 1 | ✅ | pure_increment |
| `CongestionRecvByte` | Add | uint64(pktLen) | ✅ | capture_value |
| `CongestionRecvPktRetrans` | Add | 1 | ✅ | capture_condition |
| ... | ... | ... | ... | ... |

**Proposed Transformation**:

```go
// BEFORE (current code)
func (r *receiver) Push(pkt packet.Packet) {
    metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
        r.pushLocked(pkt)
    })
}

// AFTER (extracted metrics)
func (r *receiver) Push(pkt packet.Packet) {
    // Capture values before lock
    pktLen := uint64(pkt.Len())
    isRetrans := pkt.Header().RetransmittedPacketFlag

    var (
        incPkt        uint64 = 1
        incByte       uint64 = pktLen
        incRetrans    uint64 = 0
        incByteBuf    uint64 = 0
        // ... more stack vars ...
    )

    if isRetrans {
        incRetrans = 1
    }

    metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
        r.pushLockedMinimal(pkt, &incByteBuf)  // Only lock-required ops
    })

    // Update metrics AFTER lock released
    m := r.metrics
    m.CongestionRecvPkt.Add(incPkt)
    m.CongestionRecvByte.Add(incByte)
    if incRetrans > 0 {
        m.CongestionRecvPktRetrans.Add(incRetrans)
    }
    // ...
}
```

### 2. congestion/live/receive.go:Tick()

**Lock**: `r.lock` (Write)
**Metrics Under Lock**: 8
...

## Call Flow Analysis

### Functions Called Under Lock

| Caller | Lock | Callee | Contains Metrics |
|--------|------|--------|------------------|
| `Push` | r.lock | `pushLockedNakBtree` | ✅ 15 metrics |
| `Push` | r.lock | `pushLockedOriginal` | ✅ 12 metrics |
| `Tick` | r.lock | `packetStore.RemoveAll` | ✅ 2 metrics |
| ... | ... | ... | ... |

## Recommendations

1. **High Priority**: Extract 15 metrics from `Push()` - saves ~1.5ms/sec
2. **Medium Priority**: Extract 8 metrics from `Tick()` delivery - saves ~0.8ms/sec
3. **Low Priority**: Extract 4 metrics from `periodicACK()` - saves ~0.2ms/sec
```

### 8.2 Machine-Readable Output

Also generate JSON for tooling integration:

```json
{
  "summary": {
    "filesAnalyzed": 45,
    "lockScopesFound": 23,
    "metricsUnderLock": 87,
    "extractableMetrics": 74
  },
  "lockScopes": [
    {
      "file": "congestion/live/receive.go",
      "function": "Push",
      "lockExpr": "r.lock",
      "lockType": "WithWLockTiming",
      "startLine": 260,
      "endLine": 270,
      "metrics": [
        {
          "field": "CongestionRecvPkt",
          "operation": "Add",
          "argument": "1",
          "line": 306,
          "canExtract": true,
          "extractReason": "pure_increment"
        }
      ],
      "transformation": {
        "stackVariables": [...],
        "preLockCode": "...",
        "postLockCode": "..."
      }
    }
  ]
}
```

---

## 9. Implementation Plan

### Phase 1: Core Scanner ✅ COMPLETE

1. ✅ Created `tools/metrics-lock-analyzer/main.go`
2. ✅ Implemented AST parsing for lock operations (331 found)
3. ✅ Implemented AST parsing for metric operations (255 found)
4. ✅ Basic report generation
5. ✅ Fixed `*Locked*` function detection (changed `HasSuffix` to `Contains`)

### Phase 2: Call Flow Analysis 🔄 IN PROGRESS

1. ✅ Build call graph
2. ✅ Track functions called under lock (25 found)
3. 🔄 Propagate lock context transitively
4. ⚠️ **ISSUE**: Closure patterns not fully traced

**Known Limitation**: The tool detects `pushLocked` is called under lock from `WithWLockTiming`, but doesn't propagate to `pushLockedNakBtree` and `pushLockedOriginal` which are called from `pushLocked`.

**Current**: 44 metrics detected under lock
**Expected**: ~70+ metrics under lock (missing ~25 from pushLocked* functions)

### Phase 3: Transformation Generator ⏳ PENDING

1. Implement dependency analysis
2. Generate stack variable proposals
3. Generate code transformation suggestions
4. Handle edge cases (early returns, defer, nested locks)

### Phase 4: Report Generator ⏳ PENDING

1. Markdown report generation (basic version done)
2. JSON output generation
3. Summary statistics

### Phase 5: Integration ⏳ PENDING

1. Add to Makefile (`make analyze-locks`)
2. Add CI check (optional)
3. Documentation

### Progress Summary

| Phase | Status | Effort Est. | Actual |
|-------|--------|------------|--------|
| Phase 1: Core Scanner | ✅ Complete | 4 hours | Done |
| Phase 2: Call Flow | 🔄 In Progress | 4 hours | Partially done |
| Phase 3: Transformation | ⏳ Pending | 6 hours | - |
| Phase 4: Report | ⏳ Pending | 2 hours | Basic done |
| Phase 5: Integration | ⏳ Pending | 2 hours | - |

### Next Steps

1. **Fix closure propagation**: Make tool trace `pushLocked` → `pushLockedNakBtree`
2. **Verify findings**: Manually confirm the 44 detected metrics are correct
3. **Estimate impact**: Calculate time savings from extracting metrics

---

## Appendix A: Metrics Under Lock Inventory (Tool Output)

**Latest Tool Run** (2025-12-17):

### Extraction Potential by File

| File | Metrics Under Lock | Extractable (est.) |
|------|-------------------|-------------------|
| congestion/live/send.go | 20 | 20 |
| connection.go | 10 | 10 |
| congestion/live/receive.go | 8 | 8 |
| congestion/live/nak_consolidate.go | 5 | 5 |
| congestion/live/fast_nak.go | 1 | 1 |
| **TOTAL** | **44** | **44** |

### Critical Findings by Function

#### congestion/live/send.go (20 metrics under lock)

| Function | Lock | Metrics |
|----------|------|---------|
| `pushLocked` | &s.lock | 3 metrics |
| `ackLocked` | &s.lock | 2 metrics |
| `nakLockedOriginal` | &s.lock (via nakLocked) | 7 metrics |
| `nakLockedHonorOrder` | &s.lock (via nakLocked) | 8 metrics |

**Detailed metrics in `nakLockedHonorOrder`**:
- CongestionSendPktLoss.Add(totalLossCount)
- CongestionSendByteLoss.Add(totalLossBytes)
- CongestionSendPktRetrans.Add(1)
- CongestionSendPkt.Add(1)
- CongestionSendByteRetrans.Add(uint64(...))
- CongestionSendByte.Add(uint64(...))
- CongestionSendNAKNotFound.Add(totalLossCount - retransCount)
- CongestionSendNAKHonoredOrder.Add(1)

#### connection.go (10 metrics under lock)

| Function | Lock | Metrics |
|----------|------|---------|
| `handlePacket` | c.cryptoLock | 4 metrics (decrypt errors) |
| `pop` | c.cryptoLock | 3 metrics (encrypt errors) |
| `handleKMRequest` | c.cryptoLock | 2 metrics |
| `handleACKACK` | c.ackLock | 1 metric |

#### congestion/live/receive.go (8 metrics under lock)

| Function | Lock | Metrics |
|----------|------|---------|
| `Tick` | &r.lock | 4 metrics (buffer accounting) |
| `periodicACKWriteLocked` | &r.lock | 2 metrics |
| `periodicNakOriginalLocked` | &r.lock | 2 metrics |

**Note**: The metrics in `pushLockedNakBtree()` and `pushLockedOriginal()` (15+ metrics each) are NOT currently detected by the tool. This is a known limitation - see Phase 2 in implementation plan.

#### congestion/live/nak_consolidate.go (5 metrics under lock)

| Function | Lock | Metrics |
|----------|------|---------|
| `consolidateNakBtree` | (Locked suffix) via buildNakListLocked | 5 metrics |

**Metrics**:
- NakBtreeNilWhenEnabled.Add(1)
- NakConsolidationTimeout.Add(1)
- NakConsolidationMerged.Add(1)
- NakConsolidationRuns.Add(1)
- NakConsolidationEntries.Add(uint64(...))

#### congestion/live/fast_nak.go (1 metric under lock)

| Function | Lock | Metrics |
|----------|------|---------|
| `buildNakListLocked` | (Locked suffix) | 1 metric |

**Metric**: NakBtreeNilWhenEnabled.Add(1)

---

### Expected but Not Detected (Tool Limitation)

The following functions contain metrics under lock but are not currently detected:

| File | Function | Expected Metrics | Issue |
|------|----------|-----------------|-------|
| receive.go | `pushLockedNakBtree` | ~13 | Not propagating through closure call |
| receive.go | `pushLockedOriginal` | ~12 | Not propagating through closure call |

**Why**: These are called via `metrics.WithWLockTiming()` closure → `pushLocked()` → `pushLockedNakBtree()`. The tool detects `pushLocked` is under lock but doesn't propagate to functions it calls.

**Reference metrics in `pushLockedNakBtree()` (lines 285-362)**:

| Line | Metric | Operation | Argument |
|------|--------|-----------|----------|
| 289 | CongestionRecvPktNil | Add | 1 |
| 306 | CongestionRecvPkt | Add | 1 |
| 307 | CongestionRecvByte | Add | uint64(pktLen) |
| 310 | CongestionRecvPktRetrans | Add | 1 |
| 311 | CongestionRecvByteRetrans | Add | uint64(pktLen) |
| 320 | CongestionRecvPktBelated | Add | 1 |
| 321 | CongestionRecvByteBelated | Add | uint64(pktLen) |
| 341 | NakBtreeDeletes | Add | 1 |
| 347 | CongestionRecvPktBuf | Add | 1 |
| 348 | CongestionRecvPktUnique | Add | 1 |
| 349 | CongestionRecvByteBuf | Add | uint64(pktLen) |
| 350 | CongestionRecvByteUnique | Add | uint64(pktLen) |
| 352 | CongestionRecvPktStoreInsertFailed | Add | 1 |

**All 13 atomic operations are under lock - 100% extractable once tool detects them.**

---

## Appendix B: Tool Usage

```bash
# Run the metrics-lock-analyzer
go run tools/metrics-lock-analyzer/main.go

# Run the lock-requirements-analyzer (complementary tool)
go run tools/lock-requirements-analyzer/main.go

# Generate report to file
go run tools/metrics-lock-analyzer/main.go > reports/lock-metrics-audit.md

# Analyze specific files (not yet implemented)
# go run tools/metrics-lock-analyzer/main.go congestion/live/receive.go

# Makefile integration (planned)
# make analyze-locks
```

### Tool Comparison

| Tool | Purpose | Key Output |
|------|---------|------------|
| `metrics-lock-analyzer` | Find metrics INSIDE locks that could be OUTSIDE | 44 metrics under lock |
| `lock-requirements-analyzer` | Find operations that MUST be protected | 43 protected, 50 unprotected |

### Related Tools

- `tools/metrics-audit/main.go` - Verifies metrics definitions alignment
- `tools/metrics-lock-analyzer/main.go` - This tool
- `tools/lock-requirements-analyzer/main.go` - Complementary lock analysis

