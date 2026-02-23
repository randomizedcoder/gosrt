# Performance Tools Flag Unification Design

**Created**: 2026-01-17
**Status**: Draft
**Parent**: `performance_testing_implementation_log.md`

## Executive Summary

Refactor `contrib/performance/` and `contrib/client-seeker/` to use the shared flag system in `contrib/common/flags.go` instead of the current `KEY=value` configuration system. This enables:

1. **Direct config reuse** from isolation/parallel tests
2. **Single source of truth** for all SRT configuration options
3. **Consistency** across all tools in the codebase
4. **Full access** to 100+ tuning parameters

## Motivation

### Problem Statement

Currently, the performance testing tools have their own configuration system:

```
┌─────────────────────────────────────────────────────────────────┐
│                    CURRENT STATE                                 │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  server                    ──┬──>  contrib/common/flags.go      │
│  client-generator          ──┤     (100+ flags)                 │
│  client                    ──┘                                   │
│                                                                  │
│  performance               ──┬──>  KEY=value parser             │
│  client-seeker             ──┘     (15 fields, duplicated)      │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

This creates several issues:

1. **Duplication**: `SRTConfig` in `performance/config.go` duplicates fields from `srt.Config`
2. **Incomplete coverage**: Only ~15 of 100+ flags are exposed
3. **Config translation**: Can't directly copy flags from isolation tests
4. **Maintenance burden**: Changes to `srt.Config` require updates in multiple places

### Example: Current vs Desired

**Current workflow** (copying config from isolation test):
```bash
# From isolation test Makefile:
# -iouringrecvringcount 2 -fc 102400 -rcvbuf 67108864 -latency 5000 ...

# Must manually translate to KEY=value:
./performance RECV_RINGS=2 FC=102400 RECV_BUF=64M LATENCY=5s ...
```

**Desired workflow** (direct copy-paste):
```bash
# Copy flags directly from isolation test:
./performance -initial 350M -iouringrecvringcount 2 -fc 102400 \
  -rcvbuf 67108864 -latency 5000 -useeventloop -usepacketring ...
```

## Design

### Architecture After Refactor

```
┌─────────────────────────────────────────────────────────────────┐
│                    TARGET STATE                                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  server                    ──┐                                   │
│  client-generator          ──┤                                   │
│  client                    ──┼──>  contrib/common/flags.go      │
│  performance               ──┤     (100+ flags + test flags)    │
│  client-seeker             ──┘                                   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### New Flag Categories

Add test-specific flags to `contrib/common/flags.go`:

```go
// Performance test orchestrator flags (new section)
var (
    // Search parameters
    InitialBitrate = flag.Int64("initial", 200_000_000,
        "Starting bitrate for search (default: 200M)")
    MinBitrate = flag.Int64("min-bitrate", 50_000_000,
        "Minimum bitrate floor (default: 50M)")
    MaxBitrate = flag.Int64("max-bitrate", 600_000_000,
        "Maximum bitrate ceiling (default: 600M)")
    StepSize = flag.Int64("step", 10_000_000,
        "Additive increase step (default: 10M)")
    Precision = flag.Int64("precision", 5_000_000,
        "Search stops when high-low < precision (default: 5M)")
    SearchTimeout = flag.Duration("search-timeout", 10*time.Minute,
        "Maximum search time (default: 10m)")

    // Stability evaluation
    WarmUpDuration = flag.Duration("warmup", 2*time.Second,
        "Warm-up duration after bitrate change (default: 2s)")
    StabilityWindow = flag.Duration("stability-window", 5*time.Second,
        "Stability evaluation window (default: 5s)")

    // Output
    TestVerbose = flag.Bool("test-verbose", false,
        "Enable verbose test output")
    TestJSONOutput = flag.Bool("test-json", false,
        "Output results as JSON")
    TestOutputFile = flag.String("test-output", "",
        "Path for test result output")
)
```

### Component Changes

#### 1. `contrib/common/flags.go`

**Add**: ~30 lines for test-specific flags (search, stability, output)

```go
// ════════════════════════════════════════════════════════════════
// Performance Test Flags
// ════════════════════════════════════════════════════════════════

var (
    // Search parameters (used by performance orchestrator)
    InitialBitrate = flag.Int64("initial", 200_000_000,
        "Starting bitrate for performance search (supports K/M/G suffix)")
    // ... etc
)

// BuildFlagArgs returns CLI arguments for all explicitly-set flags.
// Used by performance orchestrator to spawn subprocesses with the same config.
func BuildFlagArgs() []string {
    var args []string
    flag.Visit(func(f *flag.Flag) {
        // Skip test-specific flags (not for subprocesses)
        if isTestOnlyFlag(f.Name) {
            return
        }
        args = append(args, fmt.Sprintf("-%s=%s", f.Name, f.Value.String()))
    })
    return args
}

func isTestOnlyFlag(name string) bool {
    testFlags := map[string]bool{
        "initial": true, "min-bitrate": true, "max-bitrate": true,
        "step": true, "precision": true, "search-timeout": true,
        "warmup": true, "stability-window": true,
        "test-verbose": true, "test-json": true, "test-output": true,
    }
    return testFlags[name]
}
```

#### 2. `contrib/performance/config.go`

**Remove**: `SRTConfig` struct entirely
**Simplify**: Keep only test-specific structs

```go
package main

import (
    "time"
    "github.com/randomizedcoder/gosrt/contrib/common"
)

// SearchConfig - populated from common flags
type SearchConfig struct {
    InitialBitrate  int64
    MinBitrate      int64
    MaxBitrate      int64
    StepSize        int64
    Precision       int64
    Timeout         time.Duration
}

// ConfigFromFlags populates config from parsed flags.
func ConfigFromFlags() *Config {
    return &Config{
        Search: SearchConfig{
            InitialBitrate: *common.InitialBitrate,
            MinBitrate:     *common.MinBitrate,
            MaxBitrate:     *common.MaxBitrate,
            StepSize:       *common.StepSize,
            Precision:      *common.Precision,
            Timeout:        *common.SearchTimeout,
        },
        Stability: StabilityConfig{
            WarmUpDuration:  *common.WarmUpDuration,
            StabilityWindow: *common.StabilityWindow,
            // ... thresholds
        },
        Verbose:    *common.TestVerbose,
        JSONOutput: *common.TestJSONOutput,
        OutputFile: *common.TestOutputFile,
    }
}
```

#### 3. `contrib/performance/process.go`

**Simplify**: Use `common.BuildFlagArgs()` instead of manual arg construction

```go
// Before (manual construction):
func (pm *ProcessManager) buildServerArgs() []string {
    return []string{
        "-addr", pm.cfg.ServerAddr,
        fmt.Sprintf("-fc=%d", pm.cfg.SRT.FC),
        fmt.Sprintf("-rcvbuf=%d", pm.cfg.SRT.RecvBuf),
        // ... 30+ more lines
    }
}

// After (automatic from flags):
func (pm *ProcessManager) buildServerArgs() []string {
    args := []string{"-addr", pm.cfg.ServerAddr}
    args = append(args, common.BuildFlagArgs()...)
    args = append(args, "-promuds", pm.cfg.ServerPromUDS)
    return args
}
```

#### 4. `contrib/client-seeker/main.go`

**Simplify**: Use shared flags instead of custom parsing

```go
package main

import (
    "github.com/randomizedcoder/gosrt/contrib/common"
    srt "github.com/randomizedcoder/gosrt"
)

func main() {
    // Parse all shared flags
    common.ParseFlags()

    // Build SRT config from flags
    config := srt.DefaultConfig()
    common.ApplyFlagsToConfig(&config)

    // Seeker-specific config
    seekerConfig := &SeekerConfig{
        TargetAddr:    *common.SeekerTarget,    // new flag
        ControlSocket: *common.SeekerControlUDS, // new flag
        // ...
    }

    // ... rest of main
}
```

### File Changes Summary

| File | Action | Lines Changed |
|------|--------|---------------|
| `contrib/common/flags.go` | Add test flags + `BuildFlagArgs()` | +80 |
| `contrib/performance/config.go` | Remove `SRTConfig`, simplify | -150, +50 |
| `contrib/performance/process.go` | Use `BuildFlagArgs()` | -100, +20 |
| `contrib/performance/main.go` | Use `common.ParseFlags()` | ~10 |
| `contrib/client-seeker/main.go` | Use `common.ParseFlags()` | ~30 |
| `contrib/client-seeker/config.go` | Remove, merge into main | -100 |

**Net change**: ~-200 lines (simplification)

## Usage Examples

### Example 1: Copy Config from Isolation Test

```bash
# Isolation test uses these flags:
# -iouringrecvringcount 2 -fc 102400 -rcvbuf 67108864 -latency 5000
# -useeventloop -usepacketring -packetringsize 16384 -packetringshards 8

# Direct copy-paste to performance tool:
./contrib/performance/performance \
  -initial 350M -max 600M \
  -iouringrecvringcount 2 -fc 102400 -rcvbuf 67108864 -latency 5000 \
  -useeventloop -usepacketring -packetringsize 16384 -packetringshards 8
```

### Example 2: High-Throughput Test with Full Config

```bash
./contrib/performance/performance \
  -initial 400M -step 20M -stability-window 10s \
  -iouringenabled -iouringrecvenabled \
  -iouringrecvringcount 4 -iouringrecvringsize 16384 \
  -useeventloop -usepacketring -packetringsize 16384 \
  -usesendbtree -usesendring -usesendcontrolring \
  -sendcontrolringsize 1024 \
  -fc 204800 -rcvbuf 134217728 -sndbuf 134217728 \
  -latency 5000 -tlpktdrop
```

### Example 3: Run client-seeker Standalone

```bash
# With full flag support:
./contrib/client-seeker/client-seeker \
  -target srt://127.0.0.1:6000/test \
  -control-socket /tmp/seeker.sock \
  -iouringenabled -usesendbtree -usesendring \
  -fc 102400 -latency 3000
```

## Implementation Plan

### Phase 1: Add Test Flags to Common (30 min)

1. Add performance test flags to `contrib/common/flags.go`
2. Add `BuildFlagArgs()` helper function
3. Test: `go build ./contrib/common/...`

### Phase 2: Update Performance Tool (1 hour)

1. Remove `SRTConfig` struct from `config.go`
2. Add `ConfigFromFlags()` function
3. Update `main.go` to use `common.ParseFlags()`
4. Update `process.go` to use `BuildFlagArgs()`
5. Test: `go build ./contrib/performance/...`

### Phase 3: Update Client-Seeker (30 min)

1. Update `main.go` to use `common.ParseFlags()`
2. Remove duplicate config parsing
3. Test: `go build ./contrib/client-seeker/...`

### Phase 4: Integration Test (30 min)

1. Run performance test with isolation test flags
2. Verify server/seeker spawn with correct flags
3. Run throughput search

### Phase 5: Update Scripts/Documentation (15 min)

1. Update Makefile targets
2. Update help text
3. Document in this file

## Verification Criteria

- [ ] `go build ./contrib/...` succeeds
- [ ] `./performance -help` shows all SRT flags + test flags
- [ ] `./client-seeker -help` shows all SRT flags + seeker flags
- [ ] Copy-paste from isolation test works
- [ ] Performance search completes successfully
- [ ] Prometheus metrics collected correctly

## Rollback Plan

If issues arise:
1. Revert to `KEY=value` parser (code still exists in git history)
2. Keep both systems temporarily with deprecation warning

## Related Documents

- `contrib/common/flags.go` - Shared flag definitions
- `performance_testing_implementation_plan.md` - Original design
- `performance_testing_implementation_log.md` - Progress tracking
- `contrib/integration_testing/config.go` - Isolation test configs

---

## Implementation Log

### 2026-01-17: Document Created

- Initial design drafted
- Estimated effort: 2-3 hours
- Priority: High (blocking further performance testing)

### 2026-01-17: Implementation Complete ✅

**Phase 1: Add Test Flags to Common (30 min)**
- ✅ Added 20+ test-specific flags to `contrib/common/flags.go`
- ✅ Added `BuildFlagArgs()` and `BuildFlagArgsFiltered()` helpers
- ✅ Added `testOnlyFlags` map to filter orchestrator-only flags
- ✅ Added `PrintFlagSummary()` for debugging

**Phase 2: Update Performance Tool (45 min)**
- ✅ Removed `SRTConfig` struct from `config.go`
- ✅ Added `ConfigFromFlags()` function
- ✅ Updated `main.go` to use `common.ParseFlags()`
- ✅ Updated `process.go` to use `common.BuildFlagArgsFiltered()`
- ✅ Added `defaultHighThroughputArgs()` for when no SRT flags are set

**Phase 3: Update Client-Seeker (20 min)**
- ✅ Updated `main.go` to use common flags for control/metrics paths
- ✅ Kept seeker-specific flags (`-min-bitrate-seeker`, `-packet-size`, etc.)

**Phase 4: Integration Test (10 min)**
- ✅ Verified dry-run with isolation test flags
- ✅ Verified SRT/TEST flag categorization works correctly

**Verification:**
```bash
./contrib/performance/performance -dry-run \
  -initial 350000000 -fc 102400 -rcvbuf 67108864 \
  -iouringrecvringcount 2 -useeventloop -usepacketring \
  -usesendcontrolring -sendcontrolringsize 1024 -test-verbose

# Output shows:
#   SRT Flags: -fc=102400, -iouringrecvringcount=2, ...
#   TEST Flags (filtered): -initial=350000000, -test-verbose=true
```

**Total Time: ~1.5 hours**
