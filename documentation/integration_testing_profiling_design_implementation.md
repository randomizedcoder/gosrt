# Integration Testing Profiling - Implementation Progress

**Document:** `integration_testing_profiling_design_implementation.md`
**Design Document:** [`integration_testing_profiling_design.md`](./integration_testing_profiling_design.md)
**Created:** 2025-12-16
**Completed:** 2025-12-16
**Status:** ✅ Complete

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
| **2** | Add `-profilepath` flag to all components | 3 main.go files | ✅ Complete |
| **3** | Create profiling controller | `profiling.go`, `profiling_test.go` | ✅ Complete |
| **4** | Create profile analyzer | `profile_analyzer.go`, `profile_analyzer_test.go` | ✅ Complete |
| **5** | Create HTML report generator | `profile_report.go`, `profile_report_test.go` | ✅ Complete |
| **6** | Integrate with isolation tests | `test_isolation_mode.go`, `Makefile` | ✅ Complete |
| **7** | Integrate with parallel tests + comparison | `test_parallel_mode.go`, `Makefile` | ✅ Complete |

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

## 5. Phase 2: Add `-profilepath` Flag ✅ Complete

### 5.1 Objective

Add `-profilepath` flag to all three components to allow integration tests to specify the output directory for profile files.

### 5.2 Changes Made

| File | Changes |
|------|---------|
| `contrib/server/main.go` | Added `-profilepath` flag, updated `profile.Start()` |
| `contrib/client/main.go` | Added `-profilepath` flag, updated `profile.Start()` |
| `contrib/client-generator/main.go` | Added `-profilepath` flag, updated `profile.Start()` |

### 5.3 Code Changes

**Flag definition (added to each component):**
```go
profilePath = flag.String("profilepath", ".", "directory to write profile files to")
```

**Profile start (updated in each component):**
```go
// Before:
profile.Start(profile.ProfilePath("."), profile.NoShutdownHook, p)

// After:
profile.Start(profile.ProfilePath(*profilePath), profile.NoShutdownHook, p)
```

### 5.4 Verification

```
$ go build -o /tmp/server-test ./contrib/server
$ go build -o /tmp/client-test ./contrib/client
$ go build -o /tmp/cg-test ./contrib/client-generator

✓ server
✓ client
✓ client-generator

$ /tmp/server-test -h 2>&1 | grep profilepath
  -profilepath string

$ /tmp/client-test -h 2>&1 | grep profilepath
  -profilepath string

$ /tmp/cg-test -h 2>&1 | grep profilepath
  -profilepath string
```

### 5.5 Usage

```bash
# Integration test would invoke:
./server -profile=cpu -profilepath=/tmp/profile_TestName/server
./client-generator -profile=cpu -profilepath=/tmp/profile_TestName/cg
./client -profile=cpu -profilepath=/tmp/profile_TestName/client

# Profile files will be written to:
# /tmp/profile_TestName/server/cpu.pprof
# /tmp/profile_TestName/cg/cpu.pprof
# /tmp/profile_TestName/client/cpu.pprof
```

### 5.6 Progress Log

| Date | Action | Status |
|------|--------|--------|
| 2025-12-16 | Added `-profilepath` flag to client-generator | ✅ |
| 2025-12-16 | Updated profile.Start() in client-generator | ✅ |
| 2025-12-16 | Added `-profilepath` flag to client | ✅ |
| 2025-12-16 | Updated profile.Start() in client | ✅ |
| 2025-12-16 | Added `-profilepath` flag to server | ✅ |
| 2025-12-16 | Updated profile.Start() in server | ✅ |
| 2025-12-16 | Verified all builds | ✅ |
| 2025-12-16 | Updated test_flags.sh with profile tests | ✅ |
| 2025-12-16 | **Phase 2 Complete** | ✅ |

### 5.7 Test Coverage

Updated `contrib/common/test_flags.sh` to test profile-related flags:

```bash
# New tests added (Tests 43-51):
test_help_flag "Server -profile flag exists" "-profile" "$SERVER_BIN"
test_help_flag "Server -profilepath flag exists" "-profilepath" "$SERVER_BIN"
test_help_flag "Client -profile flag exists" "-profile" "$CLIENT_BIN"
test_help_flag "Client -profilepath flag exists" "-profilepath" "$CLIENT_BIN"
test_help_flag "Client-generator -profile flag exists" "-profile" "$CLIENTGEN_BIN"
test_help_flag "Client-generator -profilepath flag exists" "-profilepath" "$CLIENTGEN_BIN"
test_help_flag "Client-generator -bitrate flag exists" "-bitrate" "$CLIENTGEN_BIN"
test_help_flag "Server -addr flag exists" "-addr" "$SERVER_BIN"
test_help_flag "Client -from flag exists" "-from" "$CLIENT_BIN"
```

**Result:** All 9 new tests passed.

---

## 6. Phase 3: Create Profiling Controller ✅ Complete

### 6.1 Objective

Create `contrib/integration_testing/profiling.go` to manage profiling during integration tests.

### 6.2 Files Created

| File | Description |
|------|-------------|
| `contrib/integration_testing/profiling.go` | Profiling controller implementation |
| `contrib/integration_testing/profiling_test.go` | Unit tests |

### 6.3 Key Features Implemented

1. **Parse `PROFILES` environment variable**
   - Supports: `cpu`, `mem`, `mutex`, `block`, `heap`, `allocs`, `trace`
   - Supports `all` to enable all profiles (except trace)
   - Supports comma-separated lists: `cpu,mutex,heap`

2. **Create output directories**
   - Creates timestamped directories: `/tmp/profile_TestName_20250116_143022/`
   - Creates component subdirectories: `.../server/`, `.../cg/`, `.../client/`

3. **Build component command lines**
   - `GetProfileArgs(component, profileType)` returns CLI args
   - Example: `["-profile", "cpu", "-profilepath", "/tmp/profile_Test/server"]`

4. **Utility functions**
   - `ProfilingEnabled()` - checks if PROFILES is set
   - `GetProfileDuration(profileType)` - returns recommended duration
   - `ListProfileFiles()` - finds all .pprof files
   - `GetProfileComponents()` - parses component directories

### 6.4 API Summary

```go
// Core types
type ProfileType string  // "cpu", "mutex", "block", "heap", "allocs", "trace"

type ProfileConfig struct {
    TestName  string
    Profiles  []ProfileType
    OutputDir string
    Duration  time.Duration
}

// Creation
func NewProfileConfig(testName string) (*ProfileConfig, error)
func ParseProfiles(env string) []ProfileType

// CLI args
func (c *ProfileConfig) GetProfileArgs(component string, profileType ProfileType) ([]string, error)
func (c *ProfileConfig) GetFirstProfileArgs(component string) ([]string, error)

// Utilities
func ProfilingEnabled() bool
func GetProfileDuration(p ProfileType) time.Duration
func (c *ProfileConfig) ListProfileFiles() ([]string, error)
func (c *ProfileConfig) PrintProfilingInfo()
```

### 6.5 Usage Example

```go
// In integration test:
config, err := NewProfileConfig("Parallel-Starlink-5Mbps")
if err != nil {
    return err
}

if config != nil {
    config.PrintProfilingInfo()

    // Add profiling args to server command
    serverArgs = append(serverArgs, "-addr", ":6000")
    if profileArgs, _ := config.GetFirstProfileArgs("server"); profileArgs != nil {
        serverArgs = append(serverArgs, profileArgs...)
    }
}
```

### 6.6 Test Results

```
=== RUN   TestParseProfiles
    --- PASS: TestParseProfiles/empty
    --- PASS: TestParseProfiles/all
    --- PASS: TestParseProfiles/single_cpu
    --- PASS: TestParseProfiles/multiple
    --- PASS: TestParseProfiles/with_spaces
    --- PASS: TestParseProfiles/case_insensitive
=== RUN   TestCreateProfileDir
--- PASS: TestCreateProfileDir
=== RUN   TestProfileConfig_GetProfileArgs
--- PASS: TestProfileConfig_GetProfileArgs
=== RUN   TestGetProfileDuration
    --- PASS: TestGetProfileDuration/cpu (120s)
    --- PASS: TestGetProfileDuration/mutex (120s)
    --- PASS: TestGetProfileDuration/heap (60s)
    --- PASS: TestGetProfileDuration/allocs (60s)
    --- PASS: TestGetProfileDuration/trace (30s)
=== RUN   TestProfilingEnabled
--- PASS: TestProfilingEnabled
=== RUN   TestNewProfileConfig
--- PASS: TestNewProfileConfig
=== RUN   TestProfileFilePath
--- PASS: TestProfileFilePath

PASS - All 7 tests passed
```

### 6.7 Progress Log

| Date | Action | Status |
|------|--------|--------|
| 2025-12-16 | Created profiling.go | ✅ |
| 2025-12-16 | Implemented ProfileType and constants | ✅ |
| 2025-12-16 | Implemented ParseProfiles() | ✅ |
| 2025-12-16 | Implemented CreateProfileDir() | ✅ |
| 2025-12-16 | Implemented ProfileConfig methods | ✅ |
| 2025-12-16 | Created profiling_test.go | ✅ |
| 2025-12-16 | Verified all tests pass | ✅ |
| 2025-12-16 | **Phase 3 Complete** | ✅ |

---

## 7. Phase 4: Create Profile Analyzer ✅ Complete

### 7.1 Objective

Create `contrib/integration_testing/profile_analyzer.go` to analyze collected profiles and generate comparison reports.

### 7.2 Files Created

| File | Description |
|------|-------------|
| `contrib/integration_testing/profile_analyzer.go` | Profile analyzer implementation |
| `contrib/integration_testing/profile_analyzer_test.go` | Unit tests (8 tests) |

### 7.3 Key Features Implemented

1. **Run `go tool pprof -top`** on collected profiles with profile-type-specific flags
2. **Generate flame graph SVGs** (optional, requires graphviz)
3. **Parse top functions** and extract metrics (name, flat%, cumulative%)
4. **Compare baseline vs highperf profiles** with delta calculations
5. **Generate recommendations** based on detected patterns:
   - Channel overhead (chanrecv/chansend > 5%)
   - Slice allocations (makeSlice > 3%)
   - Lock contention (Mutex/Lock > 5%)
   - GC pressure (mallocgc > 5%)
   - Syscall overhead (syscall > 10%)

### 7.4 API Summary

```go
// Core types
type ProfileAnalysis struct {
    Component   string      // "server", "cg", "client"
    Pipeline    string      // "baseline", "highperf"
    ProfileType ProfileType
    TopOutput   string      // Raw pprof -top output
    TopFuncs    []FuncStat  // Parsed top 10 functions
    FlameGraph  string      // Path to SVG (if generated)
}

type FuncStat struct {
    Name       string
    Flat       float64     // Percentage
    Cumulative float64
}

type ComparisonResult struct {
    ProfileType     ProfileType
    Component       string
    FuncComparisons []FuncComparison
    Summary         string
    Recommendations []string
}

// Analysis functions
func AnalyzeProfile(profilePath, outputDir string) (*ProfileAnalysis, error)
func AnalyzeAllProfiles(profileDir string) ([]*ProfileAnalysis, error)
func CompareProfiles(baseline, highperf *ProfileAnalysis) *ComparisonResult

// Display functions
func PrintAnalysisSummary(analyses []*ProfileAnalysis)
func (r *ComparisonResult) FormatComparison() string
```

### 7.5 Test Results

```
=== RUN   TestParseTopOutput
--- PASS: TestParseTopOutput
=== RUN   TestParsePercentage (5 subtests)
--- PASS: TestParsePercentage
=== RUN   TestCompareProfiles
--- PASS: TestCompareProfiles
=== RUN   TestFormatComparison
--- PASS: TestFormatComparison
=== RUN   TestGetTopArgs (5 subtests)
--- PASS: TestGetTopArgs
=== RUN   TestTruncateStr (5 subtests)
--- PASS: TestTruncateStr
=== RUN   TestSortByAbsDelta
--- PASS: TestSortByAbsDelta
=== RUN   TestGenerateRecommendations
--- PASS: TestGenerateRecommendations

PASS - All 8 tests passed
```

### 7.6 Example Output

```
╔═══════════════════════════════════════════════════════════════════╗
║ SERVER CPU COMPARISON
╠═══════════════════════════════════════════════════════════════════╣
║ Function                            Baseline   HighPerf      Delta ║
║ ───────────────────────────────────────────────────────────────── ║
║ runtime.chanrecv                       25.0%      5.0%     -80.0% ⬇ ║
║ io_uring.Submit                         0.0%     12.0%      (new) ║
║ runtime.mallocgc                       10.0%      8.0%     -20.0% ⬇ ║
║ syscall.write                          18.0%      0.0%     (gone) ║
╠═══════════════════════════════════════════════════════════════════╣
║ SUMMARY: 2 improvements, 0 regressions
╠═══════════════════════════════════════════════════════════════════╣
║ RECOMMENDATIONS:                                                  ║
║ • Channel overhead (5.0%): Consider buffered channels or io_uring ║
╚═══════════════════════════════════════════════════════════════════╝
```

### 7.7 Progress Log

| Date | Action | Status |
|------|--------|--------|
| 2025-12-16 | Created profile_analyzer.go | ✅ |
| 2025-12-16 | Implemented ProfileAnalysis struct | ✅ |
| 2025-12-16 | Implemented AnalyzeProfile() | ✅ |
| 2025-12-16 | Implemented parseTopOutput() | ✅ |
| 2025-12-16 | Implemented CompareProfiles() | ✅ |
| 2025-12-16 | Implemented FormatComparison() | ✅ |
| 2025-12-16 | Implemented generateRecommendations() | ✅ |
| 2025-12-16 | Created profile_analyzer_test.go | ✅ |
| 2025-12-16 | Verified all tests pass | ✅ |
| 2025-12-16 | **Phase 4 Complete** | ✅ |

---

## 8. Phase 5: Create Report Generator ✅ Complete

### 8.1 Objective

Create `contrib/integration_testing/profile_report.go` to generate comprehensive HTML reports.

### 8.2 Files Created

| File | Description |
|------|-------------|
| `contrib/integration_testing/profile_report.go` | Report generator implementation |
| `contrib/integration_testing/profile_report_test.go` | Unit tests (9 tests) |

### 8.3 Key Features Implemented

1. **Generate HTML report** with dark theme, responsive layout
2. **Dashboard cards** for key metrics (CPU, memory, lock, block improvements)
3. **Comparison tables** for parallel tests with delta calculations
4. **Recommendations section** with optimization suggestions
5. **Zero-copy assessment** section for memory optimization planning
6. **Raw profile data** with expandable pprof output
7. **JSON export** for programmatic access
8. **Text summary** for quick terminal review

### 8.4 API Summary

```go
// Core types
type ProfileReport struct {
    TestName       string
    TestType       string  // "isolation", "parallel", "clean"
    Timestamp      time.Time
    OutputDir      string
    Duration       time.Duration
    Analyses       []*ProfileAnalysis
    Comparisons    []*ComparisonResult
    OverallSummary *PerformanceSummary
    Recommendations []string
    ZeroCopyReadiness *ZeroCopyAssessment
}

type PerformanceSummary struct {
    CPUImprovement   float64
    MemImprovement   float64
    LockImprovement  float64
    BlockImprovement float64
}

// Creation functions
func NewProfileReport(testName, testType, outputDir string, duration time.Duration) *ProfileReport
func GenerateReportFromDirectory(testName, testType, profileDir string, duration time.Duration) (*ProfileReport, error)
func GenerateComparisonReport(testName, baselineDir, highperfDir string, duration time.Duration) (*ProfileReport, error)

// Report methods
func (r *ProfileReport) AddAnalysis(analysis *ProfileAnalysis)
func (r *ProfileReport) AddComparison(comparison *ComparisonResult)
func (r *ProfileReport) CalculateOverallSummary()
func GenerateHTMLReport(report *ProfileReport) error
```

### 8.5 Output Files

| File | Description |
|------|-------------|
| `report.html` | Full interactive HTML report with dark theme |
| `report.json` | Machine-readable JSON for CI/CD integration |
| `summary.txt` | Quick text summary for terminal review |

### 8.6 Test Results

```
=== RUN   TestNewProfileReport
--- PASS: TestNewProfileReport
=== RUN   TestAddAnalysis
--- PASS: TestAddAnalysis
=== RUN   TestAddComparison
--- PASS: TestAddComparison
=== RUN   TestAddComparisonDeduplicatesRecommendations
--- PASS: TestAddComparisonDeduplicatesRecommendations
=== RUN   TestCalculateOverallSummary
--- PASS: TestCalculateOverallSummary
=== RUN   TestGenerateHTMLReport
--- PASS: TestGenerateHTMLReport
=== RUN   TestGenerateHTMLReportWithComparison
--- PASS: TestGenerateHTMLReportWithComparison
=== RUN   TestGenerateTextSummary
--- PASS: TestGenerateTextSummary
=== RUN   TestZeroCopyAssessment
--- PASS: TestZeroCopyAssessment

PASS - All 9 tests passed
```

### 8.7 Progress Log

| Date | Action | Status |
|------|--------|--------|
| 2025-12-16 | Created profile_report.go | ✅ |
| 2025-12-16 | Implemented ProfileReport struct | ✅ |
| 2025-12-16 | Implemented HTML template with dark theme | ✅ |
| 2025-12-16 | Implemented GenerateHTMLReport() | ✅ |
| 2025-12-16 | Implemented JSON and text summary output | ✅ |
| 2025-12-16 | Implemented helper functions | ✅ |
| 2025-12-16 | Fixed template type issues | ✅ |
| 2025-12-16 | Created profile_report_test.go | ✅ |
| 2025-12-16 | Verified all tests pass | ✅ |
| 2025-12-16 | **Phase 5 Complete** | ✅ |

---

## 9. Phase 6: Integrate with Isolation Tests ✅ Complete

### 9.1 Objective

Add profiling support to the isolation test runner.

### 9.2 Files Modified

| File | Changes |
|------|---------|
| `contrib/integration_testing/test_isolation_mode.go` | Added profiling integration |
| `Makefile` | Added PROFILES documentation |

### 9.3 Changes to `runIsolationModeTest()`

1. **Check for PROFILES environment variable** at function start
2. **Create profile config** with output directory
3. **Add profiling flags** to all component commands:
   - Control server: `-profile <type> -profilepath <dir>/control_server`
   - Test server: `-profile <type> -profilepath <dir>/test_server`
   - Control CG: `-profile <type> -profilepath <dir>/control_cg`
   - Test CG: `-profile <type> -profilepath <dir>/test_cg`
4. **After test completion**:
   - Analyze all collected profiles
   - Print analysis summary
   - Generate comparison between control and test pipelines
   - Generate HTML report

### 9.4 Usage

```bash
# Run isolation test with CPU profiling
sudo PROFILES=cpu make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr

# Run with multiple profile types
sudo PROFILES=cpu,mutex make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr

# Run with all profiles (cpu, mutex, block, heap, allocs)
sudo PROFILES=all make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr
```

### 9.5 Expected Output

When profiling is enabled, the test will:
1. Print profiling configuration at start
2. Add profiling flags to CLI commands
3. Run the test as normal
4. After completion, analyze profiles and print:
   - Analysis summary for each component
   - Comparison tables (Control vs Test)
   - Optimization recommendations
5. Generate HTML report in `/tmp/profile_<testname>_<timestamp>/`

Example output:
```
╔═══════════════════════════════════════════════════════════════════════╗
║  PROFILING ENABLED                                                    ║
╠═══════════════════════════════════════════════════════════════════════╣
║ Test Name:     Isolation-5M-Server-NakBtree-IoUr                      ║
║ Output Dir:    /tmp/profile_Isolation-5M-Server-NakBtree-IoUr_...     ║
║ Profiles:      cpu                                                    ║
╚═══════════════════════════════════════════════════════════════════════╝

=== Isolation Test: Isolation-5M-Server-NakBtree-IoUr ===
...

=== Analyzing Profiles ===
...

=== Profile Report Generated ===
HTML Report:  /tmp/profile_.../report.html
JSON Data:    /tmp/profile_.../report.json
Text Summary: /tmp/profile_.../summary.txt
```

### 9.6 Progress Log

| Date | Action | Status |
|------|--------|--------|
| 2025-12-16 | Added profiling check at function start | ✅ |
| 2025-12-16 | Added profiling flags to CLI commands | ✅ |
| 2025-12-16 | Added profile analysis after test | ✅ |
| 2025-12-16 | Added comparison generation | ✅ |
| 2025-12-16 | Added HTML report generation | ✅ |
| 2025-12-16 | Updated Makefile with PROFILES docs | ✅ |
| 2025-12-16 | **Phase 6 Complete** | ✅ |

---

## 10. Phase 7: Integrate with Parallel Tests ✅ Complete

### 10.1 Objective

Add profiling with comparison mode for parallel tests, where we compare baseline and highperf pipelines running simultaneously.

### 10.2 Files Modified

| File | Changes |
|------|---------|
| `contrib/integration_testing/test_parallel_mode.go` | Added profiling integration with comparison |
| `Makefile` | Added PROFILES documentation and pass-through |

### 10.3 Key Features

1. **Profiling Check** - Checks `PROFILES` env var at function start
2. **Profile Config** - Creates unified output directory for all 6 components
3. **CLI Flags** - Adds `-profile` and `-profilepath` to all 6 processes:
   - `baseline_server`, `baseline_cg`, `baseline_client`
   - `highperf_server`, `highperf_cg`, `highperf_client`
4. **Post-Test Analysis**:
   - Separates profiles by pipeline (baseline vs highperf)
   - Generates component-level comparisons (server vs server, cg vs cg, client vs client)
   - Prints formatted comparison tables with delta %
   - Calculates overall improvements/regressions
   - Provides optimization recommendations
   - Generates comprehensive HTML report

### 10.4 New Functions

| Function | Description |
|----------|-------------|
| `generateParallelProfileReport()` | Main comparison logic - analyzes profiles, generates comparisons, creates report |
| `printParallelProfileSummary()` | Prints overall summary with total improvements/regressions and top recommendations |

### 10.5 Usage

```bash
# Run parallel test with CPU profiling
sudo PROFILES=cpu make test-parallel CONFIG=Parallel-Starlink-5Mbps

# Run with multiple profile types
sudo PROFILES=cpu,mutex make test-parallel CONFIG=Parallel-Starlink-5Mbps

# Run with all profiles
sudo PROFILES=all make test-parallel CONFIG=Parallel-Starlink-5Mbps
```

### 10.6 Expected Output

When profiling is enabled, the test will produce detailed comparison output:

```
╔═══════════════════════════════════════════════════════════════════════╗
║  PROFILING ENABLED                                                    ║
╠═══════════════════════════════════════════════════════════════════════╣
║ Test Name:     Parallel-Starlink-5Mbps                                ║
║ Output Dir:    /tmp/profile_Parallel-Starlink-5Mbps_...               ║
║ Profiles:      cpu                                                    ║
╚═══════════════════════════════════════════════════════════════════════╝

... test execution ...

=== Analyzing Parallel Test Profiles ===
Found 6 baseline profiles and 6 highperf profiles

╔═══════════════════════════════════════════════════════════════════╗
║  SERVER CPU: Baseline vs HighPerf                                 ║
╚═══════════════════════════════════════════════════════════════════╝
║ Function                     Baseline  HighPerf  Delta            ║
║ runtime.chanrecv            25.0%      5.0%     -20.0% ⬇         ║
║ syscall.write               15.0%      3.0%     -12.0% ⬇         ║
...

╔═══════════════════════════════════════════════════════════════════╗
║  OVERALL PERFORMANCE COMPARISON: Baseline vs HighPerf             ║
╠═══════════════════════════════════════════════════════════════════╣
║  Total Improvements: 12                                           ║
║  Total Regressions:  2                                            ║
╠═══════════════════════════════════════════════════════════════════╣
║  TOP RECOMMENDATIONS:                                             ║
║  • Channel overhead (X%): Consider buffered channels or io_uring  ║
║  • Syscall overhead (X%): Batch operations, use io_uring          ║
╚═══════════════════════════════════════════════════════════════════╝

=== Profile Report Generated ===
HTML Report:  /tmp/profile_.../report.html
JSON Data:    /tmp/profile_.../report.json
Text Summary: /tmp/profile_.../summary.txt
```

### 10.7 Comparison Logic

The parallel test profiling performs intelligent matching:

1. **Component Matching** - Compares:
   - `baseline_server` ↔ `highperf_server`
   - `baseline_cg` ↔ `highperf_cg`
   - `baseline_client` ↔ `highperf_client`

2. **Profile Type Matching** - Only compares same profile types (CPU vs CPU, mutex vs mutex)

3. **Delta Calculation** - For each function:
   - Positive delta = regression (function uses more CPU in highperf)
   - Negative delta = improvement (function uses less CPU in highperf)

4. **Aggregation** - Totals across all comparisons to show overall impact

### 10.8 Progress Log

| Date | Action | Status |
|------|--------|--------|
| 2025-12-16 | Added profiling check at function start | ✅ |
| 2025-12-16 | Added profiling flags to all 6 CLI commands | ✅ |
| 2025-12-16 | Created generateParallelProfileReport() | ✅ |
| 2025-12-16 | Created printParallelProfileSummary() | ✅ |
| 2025-12-16 | Added component-level comparisons | ✅ |
| 2025-12-16 | Added overall summary with recommendations | ✅ |
| 2025-12-16 | Updated Makefile with PROFILES docs | ✅ |
| 2025-12-16 | **Phase 7 Complete** | ✅ |

---

## 11. Implementation Complete 🎉

All 7 phases of the profiling feature have been implemented:

### Summary of Files Created/Modified

| File | Type | Description |
|------|------|-------------|
| `contrib/client-generator/main.go` | Modified | Added `-profile` and `-profilepath` flags |
| `contrib/client/main.go` | Modified | Added `-profilepath` flag |
| `contrib/server/main.go` | Modified | Added `-profilepath` flag |
| `contrib/integration_testing/profiling.go` | **New** | Profile configuration and utilities |
| `contrib/integration_testing/profiling_test.go` | **New** | Unit tests for profiling |
| `contrib/integration_testing/profile_analyzer.go` | **New** | Profile analysis with pprof |
| `contrib/integration_testing/profile_analyzer_test.go` | **New** | Unit tests for analyzer |
| `contrib/integration_testing/profile_report.go` | **New** | HTML report generation |
| `contrib/integration_testing/profile_report_test.go` | **New** | Unit tests for report |
| `contrib/integration_testing/test_isolation_mode.go` | Modified | Added profiling integration |
| `contrib/integration_testing/test_parallel_mode.go` | Modified | Added profiling with comparison |
| `Makefile` | Modified | Added PROFILES documentation |

### Quick Reference

```bash
# Isolation test with CPU profiling
sudo PROFILES=cpu make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr

# Parallel test with full comparison
sudo PROFILES=cpu make test-parallel CONFIG=Parallel-Starlink-5Mbps

# Multiple profile types
sudo PROFILES=cpu,mutex,block make test-parallel CONFIG=Parallel-Starlink-5Mbps

# All profiles
sudo PROFILES=all make test-parallel CONFIG=Parallel-Starlink-5Mbps
```

### Output Files

| File | Description |
|------|-------------|
| `report.html` | Interactive HTML report with dark theme, flame graphs, comparisons |
| `report.json` | Machine-readable JSON for CI/CD integration |
| `summary.txt` | Quick text summary for terminal review |
| `*/*.pprof` | Raw profile files for manual analysis |

---

## 12. Implementation Complete - Summary ✅

**Status:** 🟢 COMPLETE
**Completed:** 2025-12-16

The profiling feature has been successfully implemented and verified. All 7 phases are complete and the infrastructure is working correctly:

- ✅ Component names correctly parsed from directory structure
- ✅ Pipeline identification (control/test, baseline/highperf)
- ✅ Comparison tables generated with delta analysis
- ✅ Recommendations provided based on profile patterns
- ✅ HTML, JSON, and text reports generated
- ✅ Output formatting widened to ~110 chars to prevent truncation

### Usage

```bash
# Isolation test with CPU profiling
sudo PROFILES=cpu make test-isolation CONFIG=Isolation-50M-Full

# Isolation test with multiple profiles
sudo PROFILES=cpu,mutex,block make test-isolation CONFIG=Isolation-50M-Full

# Parallel test with profiling
sudo PROFILES=cpu make test-parallel CONFIG=Parallel-Starlink-5Mbps

# All profiles
sudo PROFILES=all make test-isolation CONFIG=<test-name>
```

### Output Files

| File | Description |
|------|-------------|
| `report.html` | Interactive HTML report with dark theme |
| `report.json` | Machine-readable JSON for CI/CD |
| `summary.txt` | Quick text summary |
| `*/*.pprof` | Raw profile files for `go tool pprof` |

---

## 13. Potential Future Enhancements

The following enhancements are documented for future consideration but are **not blocking** the current profiling implementation.

### 13.1 Aggregate Summary per Component

**Current:** Each function compared individually with delta percentages.

**Enhancement:** Add overall CPU/memory improvement per component:

```
╔═══════════════════════════════════════════════════════════════════════════╗
║ AGGREGATE SUMMARY                                                         ║
╠═══════════════════════════════════════════════════════════════════════════╣
║ Component    CPU Change    Memory Change    Lock Contention              ║
║ ─────────────────────────────────────────────────────────────────────── ║
║ server       +15.3%        -8.2%            Reduced (futex down)         ║
║ cg           -2.1%         +1.0%            Increased (lock2 up)         ║
╚═══════════════════════════════════════════════════════════════════════════╝
```

**Effort:** Medium
**Value:** Provides quick at-a-glance assessment

### 13.2 Automatic Bottleneck Identification

**Current:** User must interpret function deltas to find key issues.

**Enhancement:** Automatically highlight the biggest performance concern:

```
╔═══════════════════════════════════════════════════════════════════════════╗
║ ⚠️ TOP BOTTLENECK IDENTIFIED                                             ║
╠═══════════════════════════════════════════════════════════════════════════╣
║ Function:  runtime.futex                                                  ║
║ Component: test_server                                                    ║
║ CPU Time:  43.4%                                                          ║
║ Delta:     +1104.7% vs control                                            ║
║                                                                           ║
║ Analysis:  The test server is spending 43% of CPU waiting on kernel       ║
║            synchronization. This indicates either:                        ║
║            • io_uring completion polling overhead                         ║
║            • Lock contention in btree/NAK btree operations                ║
║                                                                           ║
║ Suggested: Run with PROFILES=mutex,block for detailed contention data    ║
╚═══════════════════════════════════════════════════════════════════════════╝
```

**Effort:** Medium
**Value:** Accelerates diagnosis by surfacing the key issue

### 13.3 Cross-Component Comparison View

**Current:** Compares same component across pipelines (CG control vs CG test).

**Enhancement:** Add option to compare across components for connection debugging:

```
# Compare client-generator send path with server receive path
--compare-mode=cross-component

╔═══════════════════════════════════════════════════════════════════════════╗
║ CROSS-COMPONENT: CG Send Path vs Server Receive Path                     ║
╠═══════════════════════════════════════════════════════════════════════════╣
║ CG (Sender):                                                              ║
║   • syscall.Syscall6: 4.4% (io_uring submit)                             ║
║   • runtime.selectgo: 17.5% (channel operations)                          ║
║                                                                           ║
║ Server (Receiver):                                                        ║
║   • runtime.futex: 43.4% (kernel wait)                                   ║
║   • syscall.Syscall6: 32.8% (io_uring complete)                          ║
║                                                                           ║
║ Observation: Server receive path has 7.5x more syscall overhead           ║
╚═══════════════════════════════════════════════════════════════════════════╝
```

**Effort:** High
**Value:** Useful for debugging SRT connection path issues

### 13.4 Historical Comparison

**Enhancement:** Compare current run against a baseline from a previous test:

```bash
PROFILES=cpu BASELINE=/tmp/profile_previous/ make test-isolation CONFIG=...
```

**Effort:** Medium
**Value:** Track performance regression over time

### 13.5 CI/CD Integration Thresholds

**Enhancement:** Define pass/fail thresholds for CI:

```yaml
# .github/workflows/perf.yml
- name: Run performance test
  run: PROFILES=cpu make test-isolation CONFIG=Isolation-50M-Full
  env:
    PERF_MAX_REGRESSION: 10%  # Fail if any function regresses >10%
```

**Effort:** Medium
**Value:** Automated performance regression detection

---

## 14. Analysis Moved to Defect Document

The detailed profiling analysis from the 50 Mb/s test runs has been moved to [`integration_testing_50mbps_defect.md`](./integration_testing_50mbps_defect.md) Section 12, where the investigation continues.

---

*Implementation complete. Future enhancements documented for reference.*

