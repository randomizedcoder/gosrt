# Automated Metrics Audit System Design

## Problem Statement

GoSRT has three separate concerns that must stay synchronized:
1. **Metric Definitions** (`metrics/metrics.go`) - The `ConnectionMetrics` struct fields
2. **Metric Increments** (throughout codebase) - Where counters are actually `.Add()` or `.Store()`
3. **Prometheus Export** (`metrics/handler.go`) - What's exposed to monitoring

Currently, these can drift apart:
- A metric is defined but never incremented (dead code)
- A metric is incremented but not exported (invisible to monitoring)
- A metric is exported but references a non-existent field (compile error, caught)

## Goals

1. **Detect unused metrics** - Fields in `ConnectionMetrics` that are never incremented
2. **Detect unexported metrics** - Fields that ARE incremented but NOT exported to Prometheus
3. **Automated verification** - Run as part of CI/build to catch drift early
4. **Low maintenance** - Should not require manual updating when metrics change

---

## Option 1: Go AST Static Analysis (Recommended)

### Approach
Use Go's `go/ast` and `go/parser` packages to statically analyze the codebase.

### Why It's Simpler Than It Sounds

The pattern we're looking for is very specific:
```go
something.FieldName.Add(...)   // or .Store(...)
```

In AST terms, this is a `CallExpr` where:
- `Fun` is a `SelectorExpr` (the `.Add` part)
- `Fun.X` is another `SelectorExpr` (the `.FieldName` part)

We don't need full type tracking - we just extract `FieldName` and check if it matches a known metric.

### Detailed Implementation

```go
// tools/metrics-audit/main.go
package main

import (
    "fmt"
    "go/ast"
    "go/parser"
    "go/token"
    "os"
    "path/filepath"
    "strings"
)

func main() {
    // Phase 1: Parse metrics.go to find all atomic.Uint64/Int64 fields
    definedMetrics := parseMetricsStruct("metrics/metrics.go")

    // Phase 2: Scan all .go files for .Add() and .Store() calls
    incrementedMetrics := findIncrementCalls(".")

    // Phase 3: Parse handler.go to find all .Load() calls (exported)
    exportedMetrics := findExportedMetrics("metrics/handler.go")

    // Phase 4: Compare and report
    reportDiscrepancies(definedMetrics, incrementedMetrics, exportedMetrics)
}

// parseMetricsStruct extracts all atomic fields from ConnectionMetrics
func parseMetricsStruct(path string) map[string]bool {
    fset := token.NewFileSet()
    f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
    if err != nil {
        panic(err)
    }

    metrics := make(map[string]bool)

    ast.Inspect(f, func(n ast.Node) bool {
        // Find: type ConnectionMetrics struct { ... }
        ts, ok := n.(*ast.TypeSpec)
        if !ok || ts.Name.Name != "ConnectionMetrics" {
            return true
        }

        st, ok := ts.Type.(*ast.StructType)
        if !ok {
            return true
        }

        for _, field := range st.Fields.List {
            // Check if type contains "atomic."
            typeStr := exprToString(field.Type)
            if strings.Contains(typeStr, "atomic.") {
                for _, name := range field.Names {
                    metrics[name.Name] = true
                }
            }
        }
        return false // Found it, stop searching
    })

    return metrics
}

// findIncrementCalls finds all .Add() and .Store() calls on metric fields
func findIncrementCalls(rootDir string) map[string][]string {
    increments := make(map[string][]string)

    filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
            return nil
        }
        // Skip vendor, test files if desired
        if strings.Contains(path, "vendor/") {
            return nil
        }

        fset := token.NewFileSet()
        f, err := parser.ParseFile(fset, path, nil, 0)
        if err != nil {
            return nil // Skip files that don't parse
        }

        ast.Inspect(f, func(n ast.Node) bool {
            // Look for function calls
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
            //                              ^^^^^^^^^^^^^^^^^ sel.X
            fieldSel, ok := sel.X.(*ast.SelectorExpr)
            if !ok {
                return true
            }

            fieldName := fieldSel.Sel.Name
            pos := fset.Position(n.Pos())
            location := fmt.Sprintf("%s:%d", path, pos.Line)
            increments[fieldName] = append(increments[fieldName], location)

            return true
        })
        return nil
    })

    return increments
}

// findExportedMetrics finds all .Load() calls in the Prometheus handler
func findExportedMetrics(path string) map[string]bool {
    fset := token.NewFileSet()
    f, err := parser.ParseFile(fset, path, nil, 0)
    if err != nil {
        panic(err)
    }

    exported := make(map[string]bool)

    ast.Inspect(f, func(n ast.Node) bool {
        call, ok := n.(*ast.CallExpr)
        if !ok {
            return true
        }

        sel, ok := call.Fun.(*ast.SelectorExpr)
        if !ok {
            return true
        }

        // Look for .Load() calls
        if sel.Sel.Name != "Load" {
            return true
        }

        // Extract field name from metrics.FieldName.Load()
        fieldSel, ok := sel.X.(*ast.SelectorExpr)
        if !ok {
            return true
        }

        exported[fieldSel.Sel.Name] = true
        return true
    })

    return exported
}

// Helper: convert AST expression to string for type checking
func exprToString(expr ast.Expr) string {
    switch t := expr.(type) {
    case *ast.Ident:
        return t.Name
    case *ast.SelectorExpr:
        return exprToString(t.X) + "." + t.Sel.Name
    default:
        return ""
    }
}

func reportDiscrepancies(defined, incremented map[string][]string, exported map[string]bool) {
    fmt.Println("=== GoSRT Metrics Audit ===")
    fmt.Printf("Defined: %d, Incremented: %d unique, Exported: %d\n\n",
        len(defined), len(incremented), len(exported))

    // Check for metrics defined but never incremented
    fmt.Println("=== Defined but NEVER incremented ===")
    for name := range defined {
        if _, ok := incremented[name]; !ok {
            fmt.Printf("  - %s\n", name)
        }
    }

    // Check for metrics incremented but not exported
    fmt.Println("\n=== Incremented but NOT exported ===")
    for name, locations := range incremented {
        if !exported[name] {
            fmt.Printf("  ❌ %s\n", name)
            for _, loc := range locations {
                fmt.Printf("       - %s\n", loc)
            }
        }
    }
}
```

### AST Structure Explained

For the code `c.metrics.PktRecvDataSuccess.Add(1)`:

```
CallExpr {
  Fun: SelectorExpr {           // .Add
    X: SelectorExpr {           // .PktRecvDataSuccess
      X: SelectorExpr {         // .metrics
        X: Ident { Name: "c" }
        Sel: Ident { Name: "metrics" }
      }
      Sel: Ident { Name: "PktRecvDataSuccess" }  // ← This is what we want
    }
    Sel: Ident { Name: "Add" }
  }
  Args: [BasicLit { Value: "1" }]
}
```

We just need to go 2 levels deep into `SelectorExpr` to get `PktRecvDataSuccess`.

### What About Aliases?

For code like:
```go
m := c.metrics
m.PktRecvDataSuccess.Add(1)
```

The AST for `m.PktRecvDataSuccess.Add(1)` is:
```
CallExpr {
  Fun: SelectorExpr {
    X: SelectorExpr {
      X: Ident { Name: "m" }              // Different variable name
      Sel: Ident { Name: "PktRecvDataSuccess" }  // ← Still get this!
    }
    Sel: Ident { Name: "Add" }
  }
}
```

**We still extract `PktRecvDataSuccess`!** The variable name doesn't matter - we only care about the field name.

### Edge Cases Handled Automatically

1. **Different variable names** (`c.metrics`, `m`, `conn.metrics`) - all work
2. **Method receivers** (`func (c *srtConn) foo() { c.metrics.X.Add() }`) - works
3. **Nested functions** - works (AST walks everything)

### Edge Cases NOT Handled (rare in practice)

1. **Reflection-based access** - Can't detect `reflect.ValueOf(m).Field("X")`
2. **Interface indirection** - `var x interface{} = m; x.(ConnectionMetrics).X.Add()`
3. **Code generation** - Metrics generated at runtime

These are extremely rare patterns that don't appear in GoSRT.

### Effort Estimate: 1 Day

| Task | Time |
|------|------|
| Core AST parsing | 2 hours |
| File walking | 30 min |
| Report generation | 1 hour |
| Testing & edge cases | 2 hours |
| Integration with Makefile | 30 min |
| **Total** | **~1 day** |

### Pros
- **Accurate** - Understands Go syntax, not text patterns
- **Fast** - Static analysis, no runtime needed
- **Simple** - Just 2 levels of SelectorExpr, no type tracking needed
- **Robust** - Handles aliases, different variable names automatically

### Cons
- **Go-only** - Can't run without Go toolchain (but we have that)
- **False positives** - May catch non-metric `.Add()` calls (filter by checking if field name is in defined set)

---

## Option 2: Regex/Grep-Based Analysis

### Approach
Use grep with carefully crafted regex patterns to find metric usage.

### Implementation

```bash
#!/bin/bash
# tools/metrics-audit.sh

# Phase 1: Extract defined metrics from metrics.go
DEFINED=$(grep -oP '^\s+\w+\s+atomic\.(Uint64|Int64)' metrics/metrics.go |
          awk '{print $1}')

# Phase 2: Find all .Add() and .Store() calls
INCREMENTED=$(grep -rhoP '\.\K\w+(?=\.Add\(|\\.Store\()' --include="*.go" . |
              sort -u)

# Phase 3: Find metrics referenced in handler.go
EXPORTED=$(grep -oP 'metrics\.\K\w+(?=\.Load\(\))' metrics/handler.go |
           sort -u)

# Phase 4: Compare
echo "=== Defined but never incremented ==="
comm -23 <(echo "$DEFINED" | sort) <(echo "$INCREMENTED" | sort)

echo "=== Incremented but not exported ==="
comm -23 <(echo "$INCREMENTED" | sort) <(echo "$EXPORTED" | sort)
```

### Pros
- **Simple** - No Go code needed, just shell/regex
- **Fast to implement** - Hour or two
- **Cross-platform** - Works with standard Unix tools

### Cons
- **Fragile** - Regex can miss edge cases or false-positive
- **No alias tracking** - Won't catch `m := metrics; m.Field.Add()`
- **Hard to maintain** - Complex regex patterns

### Effort: Low (1-2 hours)

---

## Option 3: Hybrid Go Test with Reflection + grep

### Approach
Combine runtime reflection (for struct fields) with grep (for increment locations).

### Implementation

```go
// metrics/audit_test.go
package metrics

import (
    "os/exec"
    "reflect"
    "strings"
    "testing"
)

func TestMetricsAudit(t *testing.T) {
    // Phase 1: Use reflection to get all atomic fields
    definedMetrics := getDefinedMetrics()

    // Phase 2: Use grep to find increment locations
    incrementedMetrics := findIncrementedMetrics()

    // Phase 3: Parse handler.go for exported metrics
    exportedMetrics := findExportedMetrics()

    // Report
    for name := range definedMetrics {
        if _, ok := incrementedMetrics[name]; !ok {
            t.Logf("WARNING: %s defined but never incremented", name)
        }
        if _, ok := exportedMetrics[name]; !ok {
            t.Errorf("ERROR: %s incremented but not exported to Prometheus", name)
        }
    }
}

func getDefinedMetrics() map[string]bool {
    metrics := make(map[string]bool)
    t := reflect.TypeOf(ConnectionMetrics{})

    for i := 0; i < t.NumField(); i++ {
        field := t.Field(i)
        if strings.Contains(field.Type.String(), "atomic.") {
            metrics[field.Name] = true
        }
    }
    return metrics
}

func findIncrementedMetrics() map[string]bool {
    // Run grep and parse output
    cmd := exec.Command("grep", "-rhoP",
        `\.\K\w+(?=\.Add\(|\.Store\()`,
        "--include=*.go", ".")
    out, _ := cmd.Output()

    metrics := make(map[string]bool)
    for _, name := range strings.Split(string(out), "\n") {
        name = strings.TrimSpace(name)
        if name != "" {
            metrics[name] = true
        }
    }
    return metrics
}
```

### Pros
- **Best of both worlds** - Reflection for struct, grep for usage
- **Runs as test** - Integrates with `go test`
- **Immediate feedback** - Fails CI if metrics drift

### Cons
- **External dependency** - Requires grep (not on Windows)
- **Grep limitations** - Same as Option 2

### Effort: Low-Medium (half day)

---

## Option 4: Go Analysis Framework

### Approach
Use `golang.org/x/tools/go/analysis` framework for a proper linter.

### Implementation
Create a custom analyzer that runs as part of `go vet` or `golangci-lint`.

```go
// Skeleton - full implementation is complex
package metricsaudit

import (
    "golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
    Name: "metricsaudit",
    Doc:  "checks that all defined metrics are incremented and exported",
    Run:  run,
}

func run(pass *analysis.Pass) (interface{}, error) {
    // Full type-checked AST with cross-file analysis
    // Can track types through interfaces, etc.
}
```

### Pros
- **Most accurate** - Full type information available
- **Professional** - Can be published as a golangci-lint plugin
- **IDE integration** - Works with gopls

### Cons
- **Complex** - Significant learning curve
- **Overkill** - For a single project, this is heavy

### Effort: High (1 week)

---

## Recommendation

**Go with Option 1 (Go AST)** - it's simpler than it sounds.

### Rationale
1. **Not actually complex** - Just 2 levels of SelectorExpr traversal
2. **More robust than grep** - Handles aliases, different variable names
3. **No external dependencies** - Pure Go, works on all platforms
4. **1 day effort** - Not much more than the grep approach
5. **Professional** - Could be open-sourced as a tool

### Implementation Plan

1. **Phase 1** (Today): Create `tools/metrics-audit/main.go` with AST parsing
2. **Phase 2**: Add Makefile target `make audit-metrics`
3. **Phase 3**: Integrate into CI as a required check

---

## Data Structures for Audit

```go
type MetricAuditResult struct {
    // All fields defined in ConnectionMetrics
    Defined map[string]MetricDefinition

    // Fields that have .Add() or .Store() calls
    Incremented map[string][]IncrementLocation

    // Fields exported in Prometheus handler
    Exported map[string]PrometheusExport

    // Analysis results
    NeverIncremented []string  // Defined but never used
    NotExported      []string  // Incremented but not visible
    Orphaned         []string  // Exported but not defined (compile error)
}

type MetricDefinition struct {
    Name       string
    Type       string  // "atomic.Uint64" or "atomic.Int64"
    Comment    string  // Any associated comment
    IsCommented bool   // True if the field is commented out
}

type IncrementLocation struct {
    File     string
    Line     int
    Method   string  // "Add" or "Store"
    CallExpr string  // Full expression for context
}

type PrometheusExport struct {
    MetricName  string   // e.g., "gosrt_connection_packets_received_total"
    Labels      []string // e.g., ["socket_id", "type", "status"]
    FieldName   string   // ConnectionMetrics field being read
}
```

---

## Expected Output

```
=== GoSRT Metrics Audit Report ===

Defined Metrics: 145
Incremented Metrics: 112
Exported Metrics: 61

=== Defined but NEVER incremented (dead code) ===
  - PktRecvACKDropped (commented out in metrics.go)
  - PktRecvACKError (commented out in metrics.go)
  ... (33 fields, all commented out - OK)

=== Incremented but NOT exported (invisible to monitoring) ===
  ❌ PktRecvIoUring - incremented at:
       - metrics/packet_classifier.go:18
       - metrics/packet_classifier.go:129
  ❌ PktSentRingFull - incremented at:
       - metrics/packet_classifier.go:246
  ... (53 fields - ACTION REQUIRED)

=== Summary ===
✅ 33 metrics correctly commented out (not implemented)
✅ 61 metrics correctly exported to Prometheus
❌ 53 metrics need to be added to Prometheus handler

AUDIT FAILED - 53 metrics not exported
```

---

## Next Steps

1. Approve this design
2. Implement Option 3 (Hybrid test)
3. Fix the 53 missing Prometheus exports
4. Add to CI pipeline

