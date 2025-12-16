# Integration Testing Profiling - Implementation Progress

**Document:** `integration_testing_profiling_design_implementation.md`  
**Design Document:** [`integration_testing_profiling_design.md`](./integration_testing_profiling_design.md)  
**Created:** 2025-12-16  
**Status:** 🔄 In Progress

---

## 1. Overview

This document tracks the implementation progress of the automated profiling feature for integration tests, as designed in [`integration_testing_profiling_design.md`](./integration_testing_profiling_design.md).

### 1.1 Goal

Enable on-demand profiling of integration tests via `PROFILES=<types>` environment variable, with automated analysis and HTML report generation.

### 1.2 Motivation

The 50 Mb/s performance defect ([`integration_testing_50mbps_defect.md`](./integration_testing_50mbps_defect.md)) highlighted the need for systematic profiling capabilities to diagnose performance issues.

---

## 2. Implementation Phases Summary

| Phase | Description | Files | Status |
|-------|-------------|-------|--------|
| **1** | Add profiling to client-generator | `contrib/client-generator/main.go` | ✅ Complete |
| **2** | Add `-profilepath` flag to all components | 3 main.go files | ⏳ Pending |
| **3** | Create profiling controller | `profiling.go` (new) | ⏳ Pending |
| **4** | Create profile analyzer | `profile_analyzer.go` (new) | ⏳ Pending |
| **5** | Create HTML report generator | `profile_report.go` (new) | ⏳ Pending |
| **6** | Integrate with isolation tests | `test_isolation_mode.go` | ⏳ Pending |
| **7** | Integrate with parallel tests + comparison | `test_parallel_mode.go` | ⏳ Pending |

---

## 3. Phase 1: Add Profiling to client-generator

### 3.1 Objective

Add the same `-profile` flag support to `client-generator` that already exists in `server` and `client`.

### 3.2 Current State

| Component | File | Has `-profile` Flag |
|-----------|------|---------------------|
| `server` | `contrib/server/main.go` | ✅ Yes |
| `client` | `contrib/client/main.go` | ✅ Yes |
| `client-generator` | `contrib/client-generator/main.go` | ❌ **No** |

### 3.3 Changes Required

1. Add import for `github.com/pkg/profile`
2. Add `-profile` flag definition
3. Add profiling initialization switch statement
4. Start profiling before main logic, stop on exit

### 3.4 Implementation

**File:** `contrib/client-generator/main.go`

#### 3.4.1 Add Import

```go
import (
    // ... existing imports ...
    "github.com/pkg/profile"
)
```

#### 3.4.2 Add Flag

```go
var (
    // ... existing flags ...
    profileFlag = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
)
```

#### 3.4.3 Add Profiling Initialization

```go
func main() {
    flag.Parse()
    
    // Setup profiling if requested
    var p func(*profile.Profile)
    switch *profileFlag {
    case "cpu":
        p = profile.CPUProfile
    case "mem":
        p = profile.MemProfile
    case "allocs":
        p = profile.MemProfileAllocs
    case "heap":
        p = profile.MemProfileHeap
    case "rate":
        p = profile.MemProfileRate(2048)
    case "mutex":
        p = profile.MutexProfile
    case "block":
        p = profile.BlockProfile
    case "thread":
        p = profile.ThreadcreationProfile
    case "trace":
        p = profile.TraceProfile
    default:
    }
    
    var prof interface{ Stop() }
    if p != nil {
        prof = profile.Start(profile.ProfilePath("."), profile.NoShutdownHook, p)
        defer prof.Stop()
    }
    
    // ... rest of main() ...
}
```

### 3.5 Progress Log

| Date | Action | Status |
|------|--------|--------|
| 2025-12-16 | Phase 1 started | ✅ |
| 2025-12-16 | Added import for github.com/pkg/profile | ✅ |
| 2025-12-16 | Added -profile flag | ✅ |
| 2025-12-16 | Added profiling initialization | ✅ |
| 2025-12-16 | Verified build | ✅ |
| 2025-12-16 | **Phase 1 Complete** | ✅ |

### 3.6 Changes Made

**File:** `contrib/client-generator/main.go`

1. **Added import:**
```go
import (
    // ... existing imports ...
    "github.com/pkg/profile"
)
```

2. **Added flag:**
```go
var (
    // ... existing flags ...
    profileFlag = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
)
```

3. **Added profiling initialization in main():**
```go
// Setup profiling if requested
var p func(*profile.Profile)
switch *profileFlag {
case "cpu":
    p = profile.CPUProfile
case "mem":
    p = profile.MemProfile
case "allocs":
    p = profile.MemProfileAllocs
case "heap":
    p = profile.MemProfileHeap
case "rate":
    p = profile.MemProfileRate(2048)
case "mutex":
    p = profile.MutexProfile
case "block":
    p = profile.BlockProfile
case "thread":
    p = profile.ThreadcreationProfile
case "trace":
    p = profile.TraceProfile
default:
}

var prof interface{ Stop() }
if p != nil {
    prof = profile.Start(profile.ProfilePath("."), profile.NoShutdownHook, p)
    defer prof.Stop()
}
_ = prof // Silence unused variable warning when profiling is not enabled
```

### 3.7 Verification

```
$ go build -o /tmp/client-generator-test ./contrib/client-generator
✓ Build successful

$ /tmp/client-generator-test -h 2>&1 | grep -A1 "profile"
  -profile string
        enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)
```

### 3.8 All Three Components Now Have Profiling

| Component | File | Has `-profile` Flag |
|-----------|------|---------------------|
| `server` | `contrib/server/main.go` | ✅ Yes |
| `client` | `contrib/client/main.go` | ✅ Yes |
| `client-generator` | `contrib/client-generator/main.go` | ✅ Yes (just added) |

---

## 4. Testing

### 4.1 Phase 1 Verification

```bash
# Build client-generator
cd /home/das/Downloads/srt/gosrt
go build -o contrib/client-generator/client-generator ./contrib/client-generator

# Test help shows -profile flag
./contrib/client-generator/client-generator -h | grep profile

# Test CPU profiling (run for a few seconds, then Ctrl+C)
./contrib/client-generator/client-generator -to null -bitrate 1000000 -profile=cpu &
sleep 5
kill %1

# Verify cpu.pprof was created
ls -la cpu.pprof

# Analyze the profile
go tool pprof -top cpu.pprof
```

---

## 5. Phase 2 Preview: Add `-profilepath` Flag

### 5.1 Objective

Add `-profilepath` flag to all three components to allow integration tests to specify the output directory for profile files.

### 5.2 Changes Required

Currently, `profile.Start(profile.ProfilePath("."), ...)` writes to the current directory. We need:

1. Add `-profilepath` flag to all three components
2. Use the flag value in `profile.ProfilePath()`
3. This enables tests to organize profiles by test name

### 5.3 Files to Modify

| File | Change |
|------|--------|
| `contrib/server/main.go` | Add `-profilepath` flag, update profile.Start() |
| `contrib/client/main.go` | Add `-profilepath` flag, update profile.Start() |
| `contrib/client-generator/main.go` | Add `-profilepath` flag, update profile.Start() |

### 5.4 Expected Usage

```bash
# Integration test would invoke:
./server -profile=cpu -profilepath=/tmp/profile_TestName/server
./client-generator -profile=cpu -profilepath=/tmp/profile_TestName/cg
./client -profile=cpu -profilepath=/tmp/profile_TestName/client
```

---

*Document will be updated as implementation progresses.*

