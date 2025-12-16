# Integration Testing Profiling Design

**Document:** `integration_testing_profiling_design.md`
**Created:** 2025-12-16
**Status:** 📋 Design Review
**Related:**
- [`integration_testing_50mbps_defect.md`](./integration_testing_50mbps_defect.md) - Motivating defect
- [`integration_testing_matrix_design.md`](./integration_testing_matrix_design.md) - Matrix testing framework
- [`parallel_comparison_test_design.md`](./parallel_comparison_test_design.md) - Parallel test design

---

## 1. Overview

### 1.1 Motivation

During Phase 7 of the integration testing matrix implementation, we discovered that 50 Mb/s clean network tests fail with unexpected NAKs, drops, and throughput caps (see [`integration_testing_50mbps_defect.md`](./integration_testing_50mbps_defect.md)). Diagnosing such performance issues currently requires:

1. Manually adding profiling code
2. Running tests multiple times with different profile types
3. Manually analyzing each profile output
4. Correlating findings across profile types

This is time-consuming and error-prone. This document proposes an **integrated profiling mode** that can be enabled on-demand to automatically collect, analyze, and report performance data.

### 1.2 Goals

1. **On-demand profiling**: Enable profiling via environment variable without code changes
2. **Multi-profile collection**: Gather CPU, mutex, block, heap, allocs, and trace profiles
3. **Automated analysis**: Extract top functions, generate flame graphs, identify hotspots
4. **Comparison mode**: Compare Control vs Test pipelines in parallel tests
5. **HTML report**: Generate a single navigable report with all findings

### 1.3 Non-Goals (v1)

- Real-time profiling dashboards
- Automatic performance regression detection
- Integration with external APM tools

---

## 2. Current State Analysis

### 2.1 Profiling Support by Component

| Component | File | Has `-profile` Flag | Status |
|-----------|------|---------------------|--------|
| `server` | `contrib/server/main.go` | ✅ Yes | Ready |
| `client` | `contrib/client/main.go` | ✅ Yes | Ready |
| `client-generator` | `contrib/client-generator/main.go` | ❌ **No** | **Needs Adding** |

### 2.2 Existing Profiling Implementation

The `server` and `client` use the `github.com/pkg/profile` package:

```go
// From contrib/server/main.go (lines 108-132)
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

if p != nil {
    defer profile.Start(profile.ProfilePath("."), profile.NoShutdownHook, p).Stop()
}
```

### 2.3 Profile Output Behavior

The `profile.ProfilePath(".")` writes to the current working directory with names like:
- `cpu.pprof`
- `mem.pprof`
- `block.pprof`
- `trace.out`

---

## 3. Proposed Design

### 3.1 Usage

```bash
# Run isolation test with all profiles
PROFILES=all sudo make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr

# Run clean network test with specific profiles
PROFILES=cpu,mutex go run ./contrib/integration_testing Int-Clean-50M-5s-NakBtree

# Run parallel test with profiling (enables comparison mode)
PROFILES=all sudo make test-parallel CONFIG=Parallel-Starlink-5M-Base-vs-Full
```

### 3.2 Profile Types

| Type | Flag | What It Measures | Duration | File Size |
|------|------|------------------|----------|-----------|
| CPU | `cpu` | CPU time per function | 120s | 5-50 MB |
| Mutex | `mutex` | Lock contention, wait time | 120s | 1-10 MB |
| Block | `block` | Goroutine blocking (I/O, channels) | 120s | 1-10 MB |
| Heap | `heap` | Heap memory in use | 60s | 1-5 MB |
| Allocs | `allocs` | Allocation count by function | 60s | 1-5 MB |
| Trace | `trace` | Execution trace | 30s | 50-500 MB |

**Note:** `PROFILES=all` means: `cpu,mutex,block,heap,allocs` (excludes `trace` due to file size)

### 3.3 Workflow

```
┌─────────────────────────────────────────────────────────────────────┐
│ PROFILES=cpu,mutex go run ./integration_testing Int-Clean-50M-...  │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 1. Parse PROFILES environment variable                              │
│    profiles := parseProfiles(os.Getenv("PROFILES"))                 │
│    → ["cpu", "mutex"]                                               │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 2. Create profile output directory                                  │
│    dir := createProfileDir("Int-Clean-50M-5s-NakBtree")             │
│    → /tmp/profile_Int-Clean-50M-5s-NakBtree_20251216_143022/        │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 3. For each profile type, run test iteration:                       │
│                                                                     │
│    Iteration 1: Run with -profile=cpu for 120s                      │
│      server:           → {dir}/server_cpu.pprof                     │
│      client-generator: → {dir}/cg_cpu.pprof                         │
│      client:           → {dir}/client_cpu.pprof                     │
│                                                                     │
│    Iteration 2: Run with -profile=mutex for 120s                    │
│      server:           → {dir}/server_mutex.pprof                   │
│      client-generator: → {dir}/cg_mutex.pprof                       │
│      client:           → {dir}/client_mutex.pprof                   │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 4. Analyze each profile:                                            │
│                                                                     │
│    go tool pprof -top server_cpu.pprof > server_cpu_top.txt         │
│    go tool pprof -svg server_cpu.pprof > server_cpu_flame.svg       │
│    ...repeat for each component and profile type                    │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│ 5. Generate summary report:                                         │
│    → {dir}/report.html                                              │
│    → {dir}/summary.txt                                              │
└─────────────────────────────────────────────────────────────────────┘
```

### 3.4 Parallel Test Comparison Mode

For parallel tests (Baseline vs HighPerf), the profiling mode enables **side-by-side comparison**:

```
┌─────────────────────────────────────────────────────────────────────┐
│ PROFILES=cpu sudo make test-parallel CONFIG=Parallel-Starlink-5M-..│
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Run BOTH pipelines with CPU profiling:                              │
│                                                                     │
│ Baseline Pipeline:                 HighPerf Pipeline:               │
│   baseline_server_cpu.pprof          highperf_server_cpu.pprof      │
│   baseline_cg_cpu.pprof              highperf_cg_cpu.pprof          │
│   baseline_client_cpu.pprof          highperf_client_cpu.pprof      │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Generate COMPARISON report:                                         │
│                                                                     │
│ ┌─────────────────────────────────────────────────────────────────┐ │
│ │ Parallel Profile Comparison: Baseline vs HighPerf               │ │
│ ├─────────────────────────────────────────────────────────────────┤ │
│ │ SERVER CPU COMPARISON                                           │ │
│ │                                                                 │ │
│ │ Function              Baseline    HighPerf    Delta             │ │
│ │ ─────────────────────────────────────────────────────────────── │ │
│ │ runtime.chanrecv      23.4%       5.2%        -18.2% ✅         │ │
│ │ crypto/aes.gcmEnc     12.1%       11.8%       -0.3%             │ │
│ │ syscall.write         8.5%        2.1%        -6.4% ✅          │ │
│ │ io_uring.Submit       0.0%        8.3%        +8.3% (new)       │ │
│ ├─────────────────────────────────────────────────────────────────┤ │
│ │ KEY FINDING: HighPerf uses io_uring, reducing channel overhead  │ │
│ │ by 18.2% and syscall overhead by 6.4%                           │ │
│ └─────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```

This comparison mode is **extremely valuable** for:
- Validating that optimizations actually improve performance
- Identifying unexpected regressions
- Understanding the specific impact of each feature (io_uring, btree, NAK btree, etc.)

### 3.5 Comprehensive Comparison Report

The comparison report should analyze **all profile dimensions**, not just CPU. This is especially important for future optimizations like those described in [`zero_copy_opportunities.md`](./zero_copy_opportunities.md).

#### 3.5.1 CPU Time Analysis

```
┌─────────────────────────────────────────────────────────────────────┐
│ CPU TIME COMPARISON                                                 │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│ SERVER - Top 5 CPU Consumers                                        │
│ ───────────────────────────────────────────────────────────────────│
│ Rank  Function                      Baseline   HighPerf   Delta     │
│ ───────────────────────────────────────────────────────────────────│
│  1    runtime.chanrecv              23.4%      5.2%       -18.2% ⬇  │
│  2    syscall.write                 18.2%      2.1%       -16.1% ⬇  │
│  3    crypto/aes.gcmAesEnc          12.1%      11.8%      -0.3%     │
│  4    runtime.mallocgc              8.5%       8.2%       -0.3%     │
│  5    io_uring.(*Ring).Submit       0.0%       8.3%       +8.3% (new)│
│                                                                     │
│ CLIENT-GENERATOR - Top 5 CPU Consumers                              │
│ ───────────────────────────────────────────────────────────────────│
│ Rank  Function                      Baseline   HighPerf   Delta     │
│ ───────────────────────────────────────────────────────────────────│
│  1    runtime.chansend              28.7%      6.1%       -22.6% ⬇  │
│  2    syscall.write                 15.3%      1.8%       -13.5% ⬇  │
│  3    bytes.makeSlice               8.2%       2.1%       -6.1% ⬇   │
│  4    runtime.memmove               6.5%       1.2%       -5.3% ⬇   │
│  5    io_uring.(*Ring).Submit       0.0%       12.4%      +12.4% (new)│
│                                                                     │
│ KEY INSIGHTS:                                                       │
│ • Channel overhead reduced by 40.8% total (chanrecv + chansend)     │
│ • Syscall overhead reduced by 29.6% with io_uring                   │
│ • Memory operations (makeSlice, memmove) reduced by 11.4%           │
└─────────────────────────────────────────────────────────────────────┘
```

#### 3.5.2 Memory Allocation Analysis

Critical for zero-copy optimization work:

```
┌─────────────────────────────────────────────────────────────────────┐
│ MEMORY ALLOCATION COMPARISON                                        │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│ ALLOCATION COUNTS (allocs profile)                                  │
│ ───────────────────────────────────────────────────────────────────│
│ Component          Baseline        HighPerf        Delta            │
│ ───────────────────────────────────────────────────────────────────│
│ Server             1,245,678       892,341         -28.4% ⬇         │
│ Client-Generator   2,891,234       1,456,789       -49.6% ⬇         │
│ Client             987,654         654,321         -33.7% ⬇         │
│                                                                     │
│ TOTAL BYTES ALLOCATED (heap profile)                                │
│ ───────────────────────────────────────────────────────────────────│
│ Component          Baseline        HighPerf        Delta            │
│ ───────────────────────────────────────────────────────────────────│
│ Server             1.2 GB          780 MB          -35.0% ⬇         │
│ Client-Generator   2.8 GB          1.1 GB          -60.7% ⬇         │
│ Client             890 MB          450 MB          -49.4% ⬇         │
│                                                                     │
│ TOP ALLOCATION SOURCES (Client-Generator)                           │
│ ───────────────────────────────────────────────────────────────────│
│ Rank  Function                      Baseline   HighPerf   Delta     │
│ ───────────────────────────────────────────────────────────────────│
│  1    bytes.makeSlice               1.2 GB     0.3 GB     -75% ⬇    │
│  2    packet.NewPacket              0.8 GB     0.8 GB     0%        │
│  3    []byte literal                0.4 GB     0.05 GB    -87.5% ⬇  │
│  4    runtime.makeslice             0.2 GB     0.02 GB    -90% ⬇    │
│  5    bufio.NewReader               0.1 GB     0.01 GB    -90% ⬇    │
│                                                                     │
│ ZERO-COPY OPPORTUNITIES IDENTIFIED:                                 │
│ • bytes.makeSlice: Could use sync.Pool for packet buffers           │
│ • []byte literal: Consider pre-allocated buffer pools               │
│ • bufio.NewReader: Reuse readers instead of allocating new          │
│                                                                     │
│ HEAP IN-USE (snapshot at end of test)                               │
│ ───────────────────────────────────────────────────────────────────│
│ Component          Baseline        HighPerf        Delta            │
│ ───────────────────────────────────────────────────────────────────│
│ Server             45 MB           32 MB           -28.9% ⬇         │
│ Client-Generator   128 MB          64 MB           -50.0% ⬇         │
│ Client             38 MB           24 MB           -36.8% ⬇         │
│                                                                     │
│ GC STATISTICS                                                       │
│ ───────────────────────────────────────────────────────────────────│
│ Metric             Baseline        HighPerf        Delta            │
│ ───────────────────────────────────────────────────────────────────│
│ GC Cycles          234             89              -62.0% ⬇         │
│ GC Pause (total)   1.23s           0.34s           -72.4% ⬇         │
│ GC Pause (max)     45ms            12ms            -73.3% ⬇         │
└─────────────────────────────────────────────────────────────────────┘
```

#### 3.5.3 Lock Contention Analysis

Critical for understanding concurrency bottlenecks:

```
┌─────────────────────────────────────────────────────────────────────┐
│ LOCK CONTENTION COMPARISON (mutex profile)                          │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│ TOTAL CONTENTION TIME                                               │
│ ───────────────────────────────────────────────────────────────────│
│ Component          Baseline        HighPerf        Delta            │
│ ───────────────────────────────────────────────────────────────────│
│ Server             2.34s           0.45s           -80.8% ⬇         │
│ Client-Generator   4.56s           0.89s           -80.5% ⬇         │
│ Client             1.23s           0.34s           -72.4% ⬇         │
│                                                                     │
│ TOP CONTENTION SOURCES (Server)                                     │
│ ───────────────────────────────────────────────────────────────────│
│ Rank  Lock Location                 Baseline   HighPerf   Delta     │
│ ───────────────────────────────────────────────────────────────────│
│  1    sync.(*Mutex).Lock            890ms      120ms      -86.5% ⬇  │
│       └─ receiver.go:234                                            │
│  2    runtime.chansend              780ms      89ms       -88.6% ⬇  │
│       └─ connection.go:567                                          │
│  3    sync.(*RWMutex).RLock         340ms      156ms      -54.1% ⬇  │
│       └─ packet_store.go:123                                        │
│  4    runtime.chanrecv              230ms      45ms       -80.4% ⬇  │
│       └─ sender.go:890                                              │
│  5    sync.(*Pool).Get              120ms      34ms       -71.7% ⬇  │
│       └─ buffer_pool.go:45                                          │
│                                                                     │
│ LOCK CONTENTION HOTSPOTS:                                           │
│ • receiver.go:234 - Packet store mutex held during btree insert     │
│ • connection.go:567 - Channel send blocking on full buffer          │
│ • packet_store.go:123 - Read lock contention during lookups         │
│                                                                     │
│ RECOMMENDATIONS:                                                    │
│ • Consider lock-free data structures for hot paths                  │
│ • Use sharded locks for packet store                                │
│ • Increase channel buffer sizes to reduce send blocking             │
└─────────────────────────────────────────────────────────────────────┘
```

#### 3.5.4 Blocking Profile Analysis

Understanding where goroutines block:

```
┌─────────────────────────────────────────────────────────────────────┐
│ BLOCKING OPERATIONS COMPARISON (block profile)                      │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│ TOTAL BLOCKING TIME                                                 │
│ ───────────────────────────────────────────────────────────────────│
│ Component          Baseline        HighPerf        Delta            │
│ ───────────────────────────────────────────────────────────────────│
│ Server             8.9s            2.1s            -76.4% ⬇         │
│ Client-Generator   12.3s           3.4s            -72.4% ⬇         │
│ Client             5.6s            1.8s            -67.9% ⬇         │
│                                                                     │
│ BLOCKING BY CATEGORY                                                │
│ ───────────────────────────────────────────────────────────────────│
│ Category           Baseline        HighPerf        Delta            │
│ ───────────────────────────────────────────────────────────────────│
│ Channel ops        15.2s           2.8s            -81.6% ⬇         │
│ System calls       8.4s            1.2s            -85.7% ⬇         │
│ Mutex waits        2.8s            0.6s            -78.6% ⬇         │
│ Select statements  1.2s            0.4s            -66.7% ⬇         │
│ Condition vars     0.3s            0.1s            -66.7% ⬇         │
│                                                                     │
│ TOP BLOCKING LOCATIONS (Client-Generator)                           │
│ ───────────────────────────────────────────────────────────────────│
│ Rank  Location                      Baseline   HighPerf   Delta     │
│ ───────────────────────────────────────────────────────────────────│
│  1    runtime.chansend              6.7s       1.2s       -82.1% ⬇  │
│       └─ Sending packets to connection                              │
│  2    syscall.write                 3.4s       0.3s       -91.2% ⬇  │
│       └─ UDP socket writes (replaced by io_uring)                   │
│  3    runtime.chanrecv              1.8s       0.5s       -72.2% ⬇  │
│       └─ Receiving ACKs from connection                             │
│  4    runtime.selectgo              0.8s       0.3s       -62.5% ⬇  │
│       └─ Select on multiple channels                                │
│  5    time.Sleep                    0.4s       0.4s       0%        │
│       └─ Rate limiting (expected)                                   │
└─────────────────────────────────────────────────────────────────────┘
```

#### 3.5.5 Performance Summary Dashboard

At the end of each profiling run, generate a comprehensive summary:

```
┌─────────────────────────────────────────────────────────────────────┐
│ PERFORMANCE SUMMARY DASHBOARD                                       │
│ Test: Parallel-Starlink-5M-Base-vs-Full                             │
│ Date: 2025-12-16 14:30:22                                           │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│ ┌─────────────────────────────────────────────────────────────────┐ │
│ │ OVERALL PERFORMANCE IMPROVEMENT                                 │ │
│ │                                                                 │ │
│ │              Baseline    HighPerf    Improvement                │ │
│ │ ─────────────────────────────────────────────────────────────── │ │
│ │ Throughput   4.8 Mb/s    5.0 Mb/s    +4.2% ✅                   │ │
│ │ Latency p50  12ms        8ms         -33.3% ✅                  │ │
│ │ Latency p99  45ms        18ms        -60.0% ✅                  │ │
│ │ CPU Usage    78%         52%         -33.3% ✅                  │ │
│ │ Memory       128 MB      64 MB       -50.0% ✅                  │ │
│ │ GC Pauses    45ms max    12ms max    -73.3% ✅                  │ │
│ │ Allocations  2.8M        1.4M        -50.0% ✅                  │ │
│ │ Lock Wait    4.56s       0.89s       -80.5% ✅                  │ │
│ └─────────────────────────────────────────────────────────────────┘ │
│                                                                     │
│ ┌─────────────────────────────────────────────────────────────────┐ │
│ │ TOP 3 IMPROVEMENTS                                              │ │
│ │                                                                 │ │
│ │ 1. 🏆 Syscall reduction via io_uring (-91% blocking time)       │ │
│ │    • syscall.write: 3.4s → 0.3s                                 │ │
│ │    • Eliminates context switches to kernel                      │ │
│ │                                                                 │ │
│ │ 2. 🥈 Channel overhead reduction (-82% on chansend)             │ │
│ │    • runtime.chansend: 6.7s → 1.2s blocking                     │ │
│ │    • Fewer synchronization points                               │ │
│ │                                                                 │ │
│ │ 3. 🥉 Memory allocation reduction (-75% on makeSlice)           │ │
│ │    • bytes.makeSlice: 1.2GB → 0.3GB                             │ │
│ │    • Buffer pooling reduces GC pressure                         │ │
│ └─────────────────────────────────────────────────────────────────┘ │
│                                                                     │
│ ┌─────────────────────────────────────────────────────────────────┐ │
│ │ REMAINING OPTIMIZATION OPPORTUNITIES                            │ │
│ │                                                                 │ │
│ │ 1. packet.NewPacket still allocates 0.8GB (no change)           │ │
│ │    → Consider: Packet buffer pool with sync.Pool                │ │
│ │                                                                 │ │
│ │ 2. crypto/aes.gcmAesEnc is 11.8% of CPU                         │ │
│ │    → Consider: Hardware AES-NI optimizations                    │ │
│ │                                                                 │ │
│ │ 3. btree operations showing in hot path                         │ │
│ │    → Consider: Tune btree degree for packet sizes               │ │
│ └─────────────────────────────────────────────────────────────────┘ │
│                                                                     │
│ ┌─────────────────────────────────────────────────────────────────┐ │
│ │ ZERO-COPY READINESS ASSESSMENT                                  │ │
│ │ (For future zero_copy_opportunities.md implementation)          │ │
│ │                                                                 │ │
│ │ Current allocation hotspots that could benefit from zero-copy:  │ │
│ │                                                                 │ │
│ │ Location               Allocs/s    Bytes/s     Zero-Copy Ready? │ │
│ │ ─────────────────────────────────────────────────────────────── │ │
│ │ bytes.makeSlice        45,000      380 MB/s    ✅ Pool-able     │ │
│ │ packet.NewPacket       38,000      320 MB/s    ✅ Pool-able     │ │
│ │ []byte literal         12,000      45 MB/s     ⚠️ Review usage  │ │
│ │ bufio.NewReader        8,000       12 MB/s     ✅ Reusable      │ │
│ │                                                                 │ │
│ │ Estimated savings with full zero-copy: 60-70% memory reduction  │ │
│ └─────────────────────────────────────────────────────────────────┘ │
│                                                                     │
│ ═══════════════════════════════════════════════════════════════════ │
│ Report: /tmp/profile_Parallel-Starlink.../report.html               │
│ Flame graphs: *_flame.svg                                           │
│ Raw profiles: *.pprof (use `go tool pprof` for interactive)         │
└─────────────────────────────────────────────────────────────────────┘
```

### 3.6 Metrics Collected Per Profile Type

| Profile | Metrics Extracted | Comparison Value |
|---------|-------------------|------------------|
| **CPU** | Top N functions by time, flame graph, call graph | Identify hotspots, measure optimization impact |
| **Heap** | In-use memory, allocations by function, GC stats | Track memory efficiency, find leaks |
| **Allocs** | Allocation count by function, allocation rate | Find allocation hotspots for pooling |
| **Mutex** | Lock wait time, contention points, holder stacks | Identify lock bottlenecks |
| **Block** | Blocking time by operation, channel waits, syscalls | Find I/O and synchronization bottlenecks |
| **Trace** | Goroutine scheduling, GC events, syscall latency | Detailed execution timeline |

### 3.7 Report Outputs

| Output | Format | Purpose |
|--------|--------|---------|
| `summary.txt` | Plain text | Quick terminal review |
| `report.html` | HTML | Full interactive report with flame graphs |
| `comparison.json` | JSON | Machine-readable for CI/CD integration |
| `*_top.txt` | Text | Raw pprof top output |
| `*_flame.svg` | SVG | Interactive flame graphs |
| `*.pprof` | Binary | Raw profiles for `go tool pprof` |

---

## 4. Implementation Plan

### 4.1 Phase 1: Add Profiling to client-generator

**File:** `contrib/client-generator/main.go`

**Intent:** Add the same profiling support that exists in `server` and `client`.

**Changes Required:**

1. Add import for `github.com/pkg/profile`
2. Add `-profile` flag
3. Add profiling initialization switch
4. Start/stop profiling around main execution

**Code to Add:**

```go
// Add to imports (after existing imports)
import (
    // ... existing imports ...
    "github.com/pkg/profile"
)

// Add to flag definitions (around line 30)
var (
    // ... existing flags ...
    profileFlag = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
)

// Add to main() after flag.Parse() (around line 50)
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

    // Store profile so we can stop it explicitly on signal
    var prof interface{ Stop() }
    if p != nil {
        prof = profile.Start(profile.ProfilePath("."), profile.NoShutdownHook, p)
        defer prof.Stop()
    }

    // ... rest of main() ...
}
```

---

### 4.2 Phase 2: Add Profile Path Flag

**Files:**
- `contrib/server/main.go`
- `contrib/client/main.go`
- `contrib/client-generator/main.go`

**Intent:** Allow integration tests to specify where profile output should be written.

**Changes Required:**

Add `-profilepath` flag to control output directory:

```go
// Add to flag definitions
var (
    // ... existing flags ...
    profileFlag     = flag.String("profile", "", "enable profiling (cpu, mem, ...)")
    profilePathFlag = flag.String("profilepath", ".", "directory for profile output")
)

// Modify profiling initialization
if p != nil {
    prof = profile.Start(profile.ProfilePath(*profilePathFlag), profile.NoShutdownHook, p)
    defer prof.Stop()
}
```

---

### 4.3 Phase 3: Create Profiling Controller

**File:** `contrib/integration_testing/profiling.go` (new file)

**Intent:** Centralize profiling logic for integration tests.

**Code:**

```go
package main

import (
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"
)

// ProfileType represents a type of Go profile
type ProfileType string

const (
    ProfileCPU    ProfileType = "cpu"
    ProfileMutex  ProfileType = "mutex"
    ProfileBlock  ProfileType = "block"
    ProfileHeap   ProfileType = "heap"
    ProfileAllocs ProfileType = "allocs"
    ProfileTrace  ProfileType = "trace"
)

// AllProfiles returns all profile types (excluding trace due to size)
func AllProfiles() []ProfileType {
    return []ProfileType{ProfileCPU, ProfileMutex, ProfileBlock, ProfileHeap, ProfileAllocs}
}

// ProfileConfig holds configuration for a profiling run
type ProfileConfig struct {
    TestName     string
    Profiles     []ProfileType
    OutputDir    string
    Duration     time.Duration // Duration for each profile iteration
}

// ParseProfiles parses the PROFILES environment variable
func ParseProfiles(env string) []ProfileType {
    if env == "" {
        return nil
    }
    if env == "all" {
        return AllProfiles()
    }

    var profiles []ProfileType
    for _, p := range strings.Split(env, ",") {
        switch strings.TrimSpace(p) {
        case "cpu":
            profiles = append(profiles, ProfileCPU)
        case "mutex":
            profiles = append(profiles, ProfileMutex)
        case "block":
            profiles = append(profiles, ProfileBlock)
        case "heap":
            profiles = append(profiles, ProfileHeap)
        case "allocs":
            profiles = append(profiles, ProfileAllocs)
        case "trace":
            profiles = append(profiles, ProfileTrace)
        }
    }
    return profiles
}

// CreateProfileDir creates a directory for profile output
func CreateProfileDir(testName string) (string, error) {
    timestamp := time.Now().Format("20060102_150405")
    safeName := strings.ReplaceAll(testName, "/", "_")
    dir := filepath.Join(os.TempDir(), fmt.Sprintf("profile_%s_%s", safeName, timestamp))

    if err := os.MkdirAll(dir, 0755); err != nil {
        return "", fmt.Errorf("failed to create profile directory: %w", err)
    }
    return dir, nil
}

// ProfileFilePath returns the path for a profile file
func ProfileFilePath(dir, component string, profileType ProfileType) string {
    return filepath.Join(dir, fmt.Sprintf("%s_%s.pprof", component, profileType))
}

// ProfilingEnabled returns true if PROFILES env var is set
func ProfilingEnabled() bool {
    return os.Getenv("PROFILES") != ""
}

// GetProfileDuration returns the recommended duration for a profile type
func GetProfileDuration(p ProfileType) time.Duration {
    switch p {
    case ProfileCPU, ProfileMutex, ProfileBlock:
        return 120 * time.Second
    case ProfileHeap, ProfileAllocs:
        return 60 * time.Second
    case ProfileTrace:
        return 30 * time.Second
    default:
        return 60 * time.Second
    }
}
```

---

### 4.4 Phase 4: Create Profile Analyzer

**File:** `contrib/integration_testing/profile_analyzer.go` (new file)

**Intent:** Analyze collected profiles and generate comprehensive reports for all profile dimensions.

**Code:**

```go
package main

import (
    "bytes"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "strconv"
    "strings"
)

// ProfileAnalysis holds analysis results for a single profile
type ProfileAnalysis struct {
    Component   string      // "server", "cg", "client"
    Pipeline    string      // "baseline", "highperf" (for parallel tests)
    ProfileType ProfileType
    FilePath    string
    TopOutput   string      // Output from `pprof -top`
    FlameGraph  string      // Path to generated SVG
    TopFuncs    []FuncStat  // Parsed top functions (top 10)

    // Aggregate metrics
    TotalTime      string   // For CPU profiles
    TotalAllocs    int64    // For allocs profiles
    TotalBytes     int64    // For heap profiles
    TotalWaitTime  string   // For mutex/block profiles
}

// FuncStat represents a function's profile statistics
type FuncStat struct {
    Name       string
    Flat       float64  // Percentage or absolute value
    FlatStr    string   // Original string representation
    Cumulative float64
    CumStr     string
    Count      int64    // For allocs: number of allocations
    Size       int64    // For heap: bytes allocated
}

// ComparisonResult holds the comparison between baseline and highperf
type ComparisonResult struct {
    ProfileType ProfileType
    Component   string

    // Per-function comparisons
    FuncComparisons []FuncComparison

    // Aggregate comparisons
    TotalImprovement float64  // Positive = highperf is better
    Summary          string
    Recommendations  []string
}

// FuncComparison compares a single function across pipelines
type FuncComparison struct {
    FuncName       string
    BaselineValue  float64
    HighPerfValue  float64
    Delta          float64  // Negative = improvement
    DeltaPercent   float64
    IsNew          bool     // Only in highperf
    IsRemoved      bool     // Only in baseline
}

// AnalyzeProfile runs go tool pprof to analyze a profile
func AnalyzeProfile(profilePath string, outputDir string) (*ProfileAnalysis, error) {
    if _, err := os.Stat(profilePath); os.IsNotExist(err) {
        return nil, fmt.Errorf("profile file not found: %s", profilePath)
    }

    base := filepath.Base(profilePath)
    parts := strings.Split(strings.TrimSuffix(base, ".pprof"), "_")

    analysis := &ProfileAnalysis{
        FilePath: profilePath,
    }

    // Parse filename: {pipeline}_{component}_{profile}.pprof
    // e.g., baseline_server_cpu.pprof
    if len(parts) >= 3 {
        analysis.Pipeline = parts[0]
        analysis.Component = parts[1]
        analysis.ProfileType = ProfileType(parts[2])
    } else if len(parts) >= 2 {
        analysis.Component = parts[0]
        analysis.ProfileType = ProfileType(parts[1])
    }

    // Generate top output with different flags based on profile type
    topArgs := getTopArgs(analysis.ProfileType)
    topPath := strings.TrimSuffix(profilePath, ".pprof") + "_top.txt"
    topCmd := exec.Command("go", append([]string{"tool", "pprof"}, append(topArgs, profilePath)...)...)
    topOutput, err := topCmd.Output()
    if err != nil {
        return nil, fmt.Errorf("pprof -top failed: %w", err)
    }
    analysis.TopOutput = string(topOutput)
    os.WriteFile(topPath, topOutput, 0644)

    // Generate flame graph SVG
    svgPath := strings.TrimSuffix(profilePath, ".pprof") + "_flame.svg"
    svgCmd := exec.Command("go", "tool", "pprof", "-svg", profilePath)
    svgOutput, err := svgCmd.Output()
    if err == nil {
        os.WriteFile(svgPath, svgOutput, 0644)
        analysis.FlameGraph = svgPath
    }

    // Parse top functions
    analysis.TopFuncs = parseTopOutput(string(topOutput), analysis.ProfileType)

    // Extract aggregate metrics
    analysis.extractAggregates()

    return analysis, nil
}

// getTopArgs returns pprof args appropriate for the profile type
func getTopArgs(pt ProfileType) []string {
    switch pt {
    case ProfileHeap:
        return []string{"-top", "-nodecount=10", "-inuse_space"}
    case ProfileAllocs:
        return []string{"-top", "-nodecount=10", "-alloc_objects"}
    case ProfileMutex, ProfileBlock:
        return []string{"-top", "-nodecount=10", "-contentions"}
    default:
        return []string{"-top", "-nodecount=10"}
    }
}

// parseTopOutput extracts function statistics from pprof -top output
func parseTopOutput(output string, pt ProfileType) []FuncStat {
    var funcs []FuncStat
    lines := strings.Split(output, "\n")

    // Skip header lines and parse data
    inData := false
    for _, line := range lines {
        if strings.Contains(line, "flat") && strings.Contains(line, "cum") {
            inData = true
            continue
        }
        if !inData || strings.TrimSpace(line) == "" {
            continue
        }

        fields := strings.Fields(line)
        if len(fields) < 5 {
            continue
        }

        stat := FuncStat{
            FlatStr: fields[0] + " " + fields[1],
            CumStr:  fields[2] + " " + fields[3],
            Name:    fields[len(fields)-1],
        }

        // Parse percentage from fields[1] (e.g., "23.4%")
        stat.Flat = parsePercentage(fields[1])
        stat.Cumulative = parsePercentage(fields[3])

        funcs = append(funcs, stat)

        if len(funcs) >= 10 {
            break
        }
    }

    return funcs
}

func parsePercentage(s string) float64 {
    s = strings.TrimSuffix(s, "%")
    v, _ := strconv.ParseFloat(s, 64)
    return v
}

func (a *ProfileAnalysis) extractAggregates() {
    // Extract totals from the header of pprof output
    lines := strings.Split(a.TopOutput, "\n")
    for _, line := range lines {
        if strings.Contains(line, "of") && strings.Contains(line, "total") {
            // e.g., "2.34s of 10.5s total (22.3%)"
            re := regexp.MustCompile(`([\d.]+\w+)\s+of\s+([\d.]+\w+)\s+total`)
            if matches := re.FindStringSubmatch(line); len(matches) >= 3 {
                a.TotalTime = matches[2]
            }
        }
    }
}

// CompareProfiles generates a comprehensive comparison between baseline and highperf
func CompareProfiles(baseline, highperf *ProfileAnalysis) *ComparisonResult {
    result := &ComparisonResult{
        ProfileType: baseline.ProfileType,
        Component:   baseline.Component,
    }

    // Create map of baseline functions
    baseMap := make(map[string]FuncStat)
    for _, f := range baseline.TopFuncs {
        baseMap[f.Name] = f
    }

    // Create map of highperf functions
    hpMap := make(map[string]FuncStat)
    for _, f := range highperf.TopFuncs {
        hpMap[f.Name] = f
    }

    // Compare all functions from both
    allFuncs := make(map[string]bool)
    for name := range baseMap {
        allFuncs[name] = true
    }
    for name := range hpMap {
        allFuncs[name] = true
    }

    for name := range allFuncs {
        baseStat, inBase := baseMap[name]
        hpStat, inHP := hpMap[name]

        comp := FuncComparison{
            FuncName: name,
        }

        if inBase && inHP {
            comp.BaselineValue = baseStat.Flat
            comp.HighPerfValue = hpStat.Flat
            comp.Delta = hpStat.Flat - baseStat.Flat
            if baseStat.Flat > 0 {
                comp.DeltaPercent = (comp.Delta / baseStat.Flat) * 100
            }
        } else if inBase {
            comp.BaselineValue = baseStat.Flat
            comp.IsRemoved = true
            comp.Delta = -baseStat.Flat
            comp.DeltaPercent = -100
        } else {
            comp.HighPerfValue = hpStat.Flat
            comp.IsNew = true
            comp.Delta = hpStat.Flat
        }

        result.FuncComparisons = append(result.FuncComparisons, comp)
    }

    // Sort by absolute delta (biggest changes first)
    sortByAbsDelta(result.FuncComparisons)

    // Generate summary and recommendations
    result.generateSummary()

    return result
}

func sortByAbsDelta(comps []FuncComparison) {
    // Sort by absolute delta descending
    for i := 0; i < len(comps)-1; i++ {
        for j := i + 1; j < len(comps); j++ {
            if abs(comps[j].Delta) > abs(comps[i].Delta) {
                comps[i], comps[j] = comps[j], comps[i]
            }
        }
    }
}

func abs(x float64) float64 {
    if x < 0 {
        return -x
    }
    return x
}

func (r *ComparisonResult) generateSummary() {
    var buf bytes.Buffer

    // Count improvements vs regressions
    improvements := 0
    regressions := 0
    for _, c := range r.FuncComparisons {
        if c.Delta < -1 { // More than 1% improvement
            improvements++
        } else if c.Delta > 1 { // More than 1% regression
            regressions++
        }
    }

    fmt.Fprintf(&buf, "%d improvements, %d regressions\n", improvements, regressions)

    // Top 3 improvements
    fmt.Fprintf(&buf, "\nTop improvements:\n")
    count := 0
    for _, c := range r.FuncComparisons {
        if c.Delta < 0 && count < 3 {
            fmt.Fprintf(&buf, "  • %s: %.1f%% → %.1f%% (%.1f%% reduction)\n",
                truncate(c.FuncName, 30), c.BaselineValue, c.HighPerfValue, -c.DeltaPercent)
            count++
        }
    }

    r.Summary = buf.String()

    // Generate recommendations based on profile type
    r.generateRecommendations()
}

func (r *ComparisonResult) generateRecommendations() {
    for _, c := range r.FuncComparisons {
        // Check for common optimization opportunities
        if strings.Contains(c.FuncName, "chanrecv") || strings.Contains(c.FuncName, "chansend") {
            if c.HighPerfValue > 5 {
                r.Recommendations = append(r.Recommendations,
                    fmt.Sprintf("Channel overhead (%.1f%%): Consider buffered channels or io_uring", c.HighPerfValue))
            }
        }
        if strings.Contains(c.FuncName, "makeSlice") || strings.Contains(c.FuncName, "makeslice") {
            if c.HighPerfValue > 3 {
                r.Recommendations = append(r.Recommendations,
                    fmt.Sprintf("Slice allocation (%.1f%%): Consider sync.Pool for buffer reuse", c.HighPerfValue))
            }
        }
        if strings.Contains(c.FuncName, "Mutex") || strings.Contains(c.FuncName, "Lock") {
            if c.HighPerfValue > 5 {
                r.Recommendations = append(r.Recommendations,
                    fmt.Sprintf("Lock contention (%.1f%%): Consider lock-free structures or sharding", c.HighPerfValue))
            }
        }
    }
}

func truncate(s string, maxLen int) string {
    if len(s) <= maxLen {
        return s
    }
    return s[:maxLen-3] + "..."
}

// FormatComparison generates a formatted string for the comparison
func (r *ComparisonResult) FormatComparison() string {
    var buf bytes.Buffer

    fmt.Fprintf(&buf, "\n╔═══════════════════════════════════════════════════════════════════╗\n")
    fmt.Fprintf(&buf, "║ %s %s COMPARISON                                          \n",
        strings.ToUpper(r.Component), strings.ToUpper(string(r.ProfileType)))
    fmt.Fprintf(&buf, "╠═══════════════════════════════════════════════════════════════════╣\n")

    fmt.Fprintf(&buf, "║ %-35s %10s %10s %10s ║\n", "Function", "Baseline", "HighPerf", "Delta")
    fmt.Fprintf(&buf, "║ %s ║\n", strings.Repeat("─", 65))

    // Show top 5 comparisons
    for i, c := range r.FuncComparisons {
        if i >= 5 {
            break
        }

        deltaStr := fmt.Sprintf("%.1f%%", c.DeltaPercent)
        indicator := ""
        if c.Delta < -5 {
            indicator = " ⬇"
        } else if c.Delta > 5 {
            indicator = " ⬆"
        }
        if c.IsNew {
            deltaStr = "(new)"
        } else if c.IsRemoved {
            deltaStr = "(gone)"
        }

        fmt.Fprintf(&buf, "║ %-35s %9.1f%% %9.1f%% %9s%s ║\n",
            truncate(c.FuncName, 35), c.BaselineValue, c.HighPerfValue, deltaStr, indicator)
    }

    fmt.Fprintf(&buf, "╠═══════════════════════════════════════════════════════════════════╣\n")
    fmt.Fprintf(&buf, "║ SUMMARY: %s", r.Summary)

    if len(r.Recommendations) > 0 {
        fmt.Fprintf(&buf, "╠═══════════════════════════════════════════════════════════════════╣\n")
        fmt.Fprintf(&buf, "║ RECOMMENDATIONS:                                                  ║\n")
        for _, rec := range r.Recommendations {
            fmt.Fprintf(&buf, "║ • %-63s ║\n", truncate(rec, 63))
        }
    }

    fmt.Fprintf(&buf, "╚═══════════════════════════════════════════════════════════════════╝\n")

    return buf.String()
}
```

---

### 4.5 Phase 5: Create Report Generator

**File:** `contrib/integration_testing/profile_report.go` (new file)

**Intent:** Generate comprehensive HTML report with all profile analysis, comparisons, and recommendations.

**Code:**

```go
package main

import (
    "encoding/json"
    "fmt"
    "html/template"
    "os"
    "path/filepath"
    "time"
)

// ProfileReport holds all data for the HTML report
type ProfileReport struct {
    TestName       string
    TestType       string  // "isolation", "parallel", "clean"
    Timestamp      time.Time
    OutputDir      string
    Duration       time.Duration

    // For single pipeline tests
    Analyses       []*ProfileAnalysis

    // For parallel comparison tests
    IsComparison   bool
    BaselineAnalyses  []*ProfileAnalysis
    HighPerfAnalyses  []*ProfileAnalysis
    Comparisons    []*ComparisonResult

    // Aggregated insights
    OverallSummary    *PerformanceSummary
    Recommendations   []string
    ZeroCopyReadiness *ZeroCopyAssessment
}

// PerformanceSummary aggregates key metrics across all profiles
type PerformanceSummary struct {
    // CPU metrics
    CPUTopFunc       string
    CPUTopPercent    float64
    CPUImprovement   float64  // For comparisons

    // Memory metrics
    TotalAllocations int64
    TotalBytes       int64
    HeapInUse        int64
    GCCycles         int
    MaxGCPause       time.Duration
    MemImprovement   float64  // For comparisons

    // Contention metrics
    TotalLockWait    time.Duration
    TopContention    string
    LockImprovement  float64  // For comparisons

    // Blocking metrics
    TotalBlockTime   time.Duration
    TopBlocker       string
    BlockImprovement float64  // For comparisons
}

// ZeroCopyAssessment identifies opportunities for zero-copy optimizations
type ZeroCopyAssessment struct {
    Candidates []ZeroCopyCandidate
    EstimatedSavings string
}

type ZeroCopyCandidate struct {
    Location     string
    AllocsPerSec int64
    BytesPerSec  int64
    Poolable     bool
    Reusable     bool
    Notes        string
}

const reportTemplate = `<!DOCTYPE html>
<html>
<head>
    <title>Profile Report: {{.TestName}}</title>
    <style>
        :root {
            --bg-primary: #1a1a2e;
            --bg-secondary: #16213e;
            --bg-card: #0f3460;
            --text-primary: #eee;
            --text-secondary: #aaa;
            --accent: #e94560;
            --success: #00d4aa;
            --warning: #ffa500;
        }
        body {
            font-family: 'JetBrains Mono', 'Fira Code', monospace;
            background: var(--bg-primary);
            color: var(--text-primary);
            margin: 0;
            padding: 20px;
        }
        .container { max-width: 1400px; margin: 0 auto; }
        h1 { color: var(--accent); margin-bottom: 5px; }
        h2 { color: var(--text-primary); border-bottom: 2px solid var(--accent); padding-bottom: 8px; }
        h3 { color: var(--text-secondary); }

        .meta { color: var(--text-secondary); margin-bottom: 20px; }
        .meta code { background: var(--bg-secondary); padding: 2px 6px; border-radius: 3px; }

        .dashboard {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
            gap: 20px;
            margin-bottom: 30px;
        }
        .metric-card {
            background: var(--bg-card);
            padding: 20px;
            border-radius: 8px;
            border-left: 4px solid var(--accent);
        }
        .metric-value { font-size: 2em; color: var(--success); }
        .metric-label { color: var(--text-secondary); font-size: 0.9em; }
        .metric-delta { font-size: 0.8em; margin-top: 5px; }
        .delta-positive { color: var(--success); }
        .delta-negative { color: var(--accent); }

        .comparison-table {
            width: 100%;
            border-collapse: collapse;
            margin: 20px 0;
        }
        .comparison-table th, .comparison-table td {
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid var(--bg-secondary);
        }
        .comparison-table th {
            background: var(--bg-card);
            color: var(--accent);
        }
        .comparison-table tr:hover { background: var(--bg-secondary); }

        .analysis {
            background: var(--bg-secondary);
            margin-bottom: 30px;
            padding: 20px;
            border-radius: 8px;
        }
        .top-output {
            font-family: monospace;
            white-space: pre;
            font-size: 11px;
            overflow-x: auto;
            background: var(--bg-primary);
            padding: 15px;
            border-radius: 5px;
        }
        .flame-link {
            display: inline-block;
            margin: 10px 0;
            padding: 10px 20px;
            background: var(--accent);
            color: white;
            text-decoration: none;
            border-radius: 5px;
            font-weight: bold;
        }
        .flame-link:hover { opacity: 0.9; }

        .recommendations {
            background: linear-gradient(135deg, var(--bg-card), var(--bg-secondary));
            padding: 20px;
            border-radius: 8px;
            margin: 20px 0;
        }
        .recommendations li {
            margin: 10px 0;
            padding: 10px;
            background: var(--bg-primary);
            border-radius: 5px;
            border-left: 3px solid var(--warning);
        }

        .zero-copy {
            background: var(--bg-card);
            padding: 20px;
            border-radius: 8px;
            margin: 20px 0;
        }
        .poolable { color: var(--success); }
        .review { color: var(--warning); }

        .summary-box {
            background: linear-gradient(135deg, #134e5e, #71b280);
            padding: 20px;
            border-radius: 8px;
            margin: 20px 0;
        }

        .tabs { display: flex; gap: 10px; margin-bottom: 20px; }
        .tab {
            padding: 10px 20px;
            background: var(--bg-secondary);
            border: none;
            color: var(--text-primary);
            cursor: pointer;
            border-radius: 5px 5px 0 0;
        }
        .tab.active { background: var(--bg-card); color: var(--accent); }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔬 Profile Report: {{.TestName}}</h1>
        <div class="meta">
            <p>Type: <code>{{.TestType}}</code> | Generated: <code>{{.Timestamp.Format "2006-01-02 15:04:05"}}</code> | Duration: <code>{{.Duration}}</code></p>
            <p>Output: <code>{{.OutputDir}}</code></p>
        </div>

        {{if .IsComparison}}
        <!-- PARALLEL COMPARISON DASHBOARD -->
        <div class="summary-box">
            <h2>📊 Performance Comparison: Baseline vs HighPerf</h2>
        </div>

        <div class="dashboard">
            <div class="metric-card">
                <div class="metric-label">CPU Improvement</div>
                <div class="metric-value">{{printf "%.1f" .OverallSummary.CPUImprovement}}%</div>
                <div class="metric-delta delta-positive">⬇ Channel + syscall overhead reduced</div>
            </div>
            <div class="metric-card">
                <div class="metric-label">Memory Reduction</div>
                <div class="metric-value">{{printf "%.1f" .OverallSummary.MemImprovement}}%</div>
                <div class="metric-delta delta-positive">⬇ Fewer allocations</div>
            </div>
            <div class="metric-card">
                <div class="metric-label">Lock Wait Reduction</div>
                <div class="metric-value">{{printf "%.1f" .OverallSummary.LockImprovement}}%</div>
                <div class="metric-delta delta-positive">⬇ Less contention</div>
            </div>
            <div class="metric-card">
                <div class="metric-label">Block Time Reduction</div>
                <div class="metric-value">{{printf "%.1f" .OverallSummary.BlockImprovement}}%</div>
                <div class="metric-delta delta-positive">⬇ Less I/O waiting</div>
            </div>
        </div>

        <!-- DETAILED COMPARISONS BY PROFILE TYPE -->
        {{range .Comparisons}}
        <div class="analysis">
            <h2>{{.Component | ToUpper}} - {{.ProfileType | ToUpper}} Comparison</h2>
            <table class="comparison-table">
                <tr>
                    <th>Function</th>
                    <th>Baseline</th>
                    <th>HighPerf</th>
                    <th>Delta</th>
                    <th>Status</th>
                </tr>
                {{range .FuncComparisons}}
                <tr>
                    <td>{{.FuncName}}</td>
                    <td>{{printf "%.1f%%" .BaselineValue}}</td>
                    <td>{{printf "%.1f%%" .HighPerfValue}}</td>
                    <td class="{{if lt .Delta 0}}delta-positive{{else}}delta-negative{{end}}">
                        {{printf "%+.1f%%" .Delta}}
                    </td>
                    <td>
                        {{if .IsNew}}🆕 New{{else if .IsRemoved}}✅ Removed{{else if lt .DeltaPercent -20}}🏆 Big Win{{else if gt .DeltaPercent 20}}⚠️ Regression{{end}}
                    </td>
                </tr>
                {{end}}
            </table>
            <p><strong>Summary:</strong> {{.Summary}}</p>
        </div>
        {{end}}

        {{end}}

        <!-- RECOMMENDATIONS -->
        {{if .Recommendations}}
        <div class="recommendations">
            <h2>💡 Optimization Recommendations</h2>
            <ul>
                {{range .Recommendations}}
                <li>{{.}}</li>
                {{end}}
            </ul>
        </div>
        {{end}}

        <!-- ZERO-COPY ASSESSMENT -->
        {{if .ZeroCopyReadiness}}
        <div class="zero-copy">
            <h2>🚀 Zero-Copy Readiness Assessment</h2>
            <p>Estimated savings with full zero-copy: <strong>{{.ZeroCopyReadiness.EstimatedSavings}}</strong></p>
            <table class="comparison-table">
                <tr>
                    <th>Location</th>
                    <th>Allocs/s</th>
                    <th>Bytes/s</th>
                    <th>Status</th>
                    <th>Notes</th>
                </tr>
                {{range .ZeroCopyReadiness.Candidates}}
                <tr>
                    <td>{{.Location}}</td>
                    <td>{{.AllocsPerSec}}</td>
                    <td>{{.BytesPerSec}}</td>
                    <td>
                        {{if .Poolable}}<span class="poolable">✅ Pool-able</span>
                        {{else if .Reusable}}<span class="poolable">✅ Reusable</span>
                        {{else}}<span class="review">⚠️ Review</span>{{end}}
                    </td>
                    <td>{{.Notes}}</td>
                </tr>
                {{end}}
            </table>
        </div>
        {{end}}

        <!-- RAW PROFILE DATA -->
        <h2>📁 Raw Profile Data</h2>
        {{range .Analyses}}
        <div class="analysis">
            <h3>{{.Pipeline}} {{.Component}} - {{.ProfileType}}</h3>
            <p>File: <code>{{.FilePath}}</code></p>
            {{if .FlameGraph}}
            <a class="flame-link" href="{{.FlameGraph}}" target="_blank">🔥 View Flame Graph</a>
            {{end}}
            <details>
                <summary>Top Functions (click to expand)</summary>
                <div class="top-output">{{.TopOutput}}</div>
            </details>
        </div>
        {{end}}
    </div>
</body>
</html>`

// GenerateHTMLReport creates an HTML report from the profile analyses
func GenerateHTMLReport(report *ProfileReport) error {
    funcMap := template.FuncMap{
        "ToUpper": strings.ToUpper,
    }

    tmpl, err := template.New("report").Funcs(funcMap).Parse(reportTemplate)
    if err != nil {
        return fmt.Errorf("failed to parse template: %w", err)
    }

    reportPath := filepath.Join(report.OutputDir, "report.html")
    f, err := os.Create(reportPath)
    if err != nil {
        return fmt.Errorf("failed to create report file: %w", err)
    }
    defer f.Close()

    if err := tmpl.Execute(f, report); err != nil {
        return fmt.Errorf("failed to execute template: %w", err)
    }

    // Also generate JSON for programmatic access
    jsonPath := filepath.Join(report.OutputDir, "report.json")
    jsonData, _ := json.MarshalIndent(report, "", "  ")
    os.WriteFile(jsonPath, jsonData, 0644)

    // Generate text summary
    textPath := filepath.Join(report.OutputDir, "summary.txt")
    generateTextSummary(report, textPath)

    fmt.Printf("\n=== Profile Report Generated ===\n")
    fmt.Printf("HTML Report: %s\n", reportPath)
    fmt.Printf("JSON Data:   %s\n", jsonPath)
    fmt.Printf("Text Summary: %s\n", textPath)

    return nil
}

func generateTextSummary(report *ProfileReport, path string) {
    var buf bytes.Buffer

    fmt.Fprintf(&buf, "PROFILE SUMMARY: %s\n", report.TestName)
    fmt.Fprintf(&buf, "Generated: %s\n", report.Timestamp.Format("2006-01-02 15:04:05"))
    fmt.Fprintf(&buf, "%s\n\n", strings.Repeat("=", 70))

    if report.IsComparison && report.OverallSummary != nil {
        fmt.Fprintf(&buf, "PERFORMANCE IMPROVEMENTS (HighPerf vs Baseline)\n")
        fmt.Fprintf(&buf, "%s\n", strings.Repeat("-", 50))
        fmt.Fprintf(&buf, "  CPU Overhead:     %.1f%% reduction\n", report.OverallSummary.CPUImprovement)
        fmt.Fprintf(&buf, "  Memory Usage:     %.1f%% reduction\n", report.OverallSummary.MemImprovement)
        fmt.Fprintf(&buf, "  Lock Contention:  %.1f%% reduction\n", report.OverallSummary.LockImprovement)
        fmt.Fprintf(&buf, "  Blocking Time:    %.1f%% reduction\n", report.OverallSummary.BlockImprovement)
        fmt.Fprintf(&buf, "\n")
    }

    if len(report.Recommendations) > 0 {
        fmt.Fprintf(&buf, "RECOMMENDATIONS\n")
        fmt.Fprintf(&buf, "%s\n", strings.Repeat("-", 50))
        for i, rec := range report.Recommendations {
            fmt.Fprintf(&buf, "  %d. %s\n", i+1, rec)
        }
        fmt.Fprintf(&buf, "\n")
    }

    os.WriteFile(path, buf.Bytes(), 0644)
}
```

---

### 4.6 Phase 6: Integrate with Isolation Tests

**File:** `contrib/integration_testing/test_isolation_mode.go`

**Intent:** Add profiling support to isolation test runner.

**Changes to Function:** `runIsolationModeTest()`

```go
func runIsolationModeTest(config IsolationTestConfig) (passed bool) {
    // Check if profiling is enabled
    profiles := ParseProfiles(os.Getenv("PROFILES"))
    if len(profiles) > 0 {
        return runIsolationModeTestWithProfiling(config, profiles)
    }

    // ... existing implementation ...
}

func runIsolationModeTestWithProfiling(config IsolationTestConfig, profiles []ProfileType) bool {
    // Create profile output directory
    profileDir, err := CreateProfileDir(config.Name)
    if err != nil {
        fmt.Printf("Failed to create profile directory: %v\n", err)
        return false
    }
    fmt.Printf("\n=== Profiling Mode Enabled ===\n")
    fmt.Printf("Profile directory: %s\n", profileDir)
    fmt.Printf("Profiles: %v\n\n", profiles)

    var allAnalyses []*ProfileAnalysis

    // Run test for each profile type
    for _, profileType := range profiles {
        fmt.Printf("\n--- Collecting %s profile ---\n", profileType)

        // Modify flags to include profiling
        controlServerFlags := config.GetControlServerFlags("control")
        controlServerFlags = append(controlServerFlags,
            "-profile", string(profileType),
            "-profilepath", filepath.Join(profileDir, "control_server"))

        testServerFlags := config.GetTestServerFlags("test")
        testServerFlags = append(testServerFlags,
            "-profile", string(profileType),
            "-profilepath", filepath.Join(profileDir, "test_server"))

        // ... run test with modified flags ...

        // Collect and analyze profiles
        // (profile files will be written by the components)
    }

    // Generate report
    report := &ProfileReport{
        TestName:  config.Name,
        Timestamp: time.Now(),
        OutputDir: profileDir,
        Analyses:  allAnalyses,
    }
    GenerateHTMLReport(report)

    return true
}
```

---

### 4.7 Phase 7: Integrate with Parallel Tests

**File:** `contrib/integration_testing/test_parallel_mode.go`

**Intent:** Add profiling with comparison mode for parallel tests.

**Changes to Function:** `runParallelModeTest()`

```go
func runParallelModeTest(config ParallelTestConfig) ParallelTestResult {
    // Check if profiling is enabled
    profiles := ParseProfiles(os.Getenv("PROFILES"))
    if len(profiles) > 0 {
        return runParallelModeTestWithProfiling(config, profiles)
    }

    // ... existing implementation ...
}

func runParallelModeTestWithProfiling(config ParallelTestConfig, profiles []ProfileType) ParallelTestResult {
    profileDir, _ := CreateProfileDir(config.Name)

    // Create subdirectories for each pipeline
    baselineDir := filepath.Join(profileDir, "baseline")
    highperfDir := filepath.Join(profileDir, "highperf")
    os.MkdirAll(baselineDir, 0755)
    os.MkdirAll(highperfDir, 0755)

    // For each profile type, run BOTH pipelines
    for _, profileType := range profiles {
        // Run baseline pipeline with profiling
        // Run highperf pipeline with profiling
        // (Can run simultaneously since they use different ports)
    }

    // Collect all analyses
    var baselineAnalyses, highperfAnalyses []*ProfileAnalysis

    // Analyze and compare
    var comparisons []string
    for i := range baselineAnalyses {
        comp := CompareProfiles(baselineAnalyses[i], highperfAnalyses[i])
        comparisons = append(comparisons, comp)
    }

    // Generate comparative report
    report := &ProfileReport{
        TestName:    config.Name + " (Parallel Comparison)",
        Timestamp:   time.Now(),
        OutputDir:   profileDir,
        Analyses:    append(baselineAnalyses, highperfAnalyses...),
        Comparisons: comparisons,
    }
    GenerateHTMLReport(report)

    return ParallelTestResult{Passed: true}
}
```

---

## 5. File Summary

| File | Status | Changes |
|------|--------|---------|
| `contrib/client-generator/main.go` | **Modify** | Add `-profile` and `-profilepath` flags |
| `contrib/server/main.go` | **Modify** | Add `-profilepath` flag |
| `contrib/client/main.go` | **Modify** | Add `-profilepath` flag |
| `contrib/integration_testing/profiling.go` | **New** | Profile parsing and utilities |
| `contrib/integration_testing/profile_analyzer.go` | **New** | Profile analysis with pprof |
| `contrib/integration_testing/profile_report.go` | **New** | HTML report generation |
| `contrib/integration_testing/test_isolation_mode.go` | **Modify** | Add profiling integration |
| `contrib/integration_testing/test_parallel_mode.go` | **Modify** | Add profiling with comparison |
| `contrib/integration_testing/test_graceful_shutdown.go` | **Modify** | Add profiling to clean network tests |

---

## 6. Implementation Priority

| Priority | Phase | Description | Effort | Value |
|----------|-------|-------------|--------|-------|
| **P1** | Phase 1 | Add profiling to client-generator | Low | High |
| **P1** | Phase 2 | Add `-profilepath` flag to all components | Low | Medium |
| **P2** | Phase 3 | Create profiling controller | Medium | High |
| **P2** | Phase 4 | Create profile analyzer | Medium | High |
| **P3** | Phase 5 | Create HTML report generator | Medium | Medium |
| **P3** | Phase 6 | Integrate with isolation tests | Medium | High |
| **P4** | Phase 7 | Integrate with parallel tests + comparison | High | **Very High** |

---

## 7. Example Output

```
$ PROFILES=cpu,mutex go run ./contrib/integration_testing Int-Clean-50M-5s-NakBtree

=== Profiling Mode Enabled ===
Profile directory: /tmp/profile_Int-Clean-50M-5s-NakBtree_20251216_143022/
Profiles: [cpu mutex]

--- Collecting cpu profile (120s) ---
Running test iteration...
[test output]

--- Collecting mutex profile (120s) ---
Running test iteration...
[test output]

=== Analyzing Profiles ===
Analyzing server_cpu.pprof...
Analyzing cg_cpu.pprof...
Analyzing client_cpu.pprof...
Analyzing server_mutex.pprof...
...

=== Report Generated ===
Profile directory: /tmp/profile_Int-Clean-50M-5s-NakBtree_20251216_143022/

Files:
  server_cpu.pprof
  server_cpu_top.txt
  server_cpu_flame.svg
  cg_cpu.pprof
  cg_cpu_top.txt
  cg_cpu_flame.svg
  ...
  report.html  ← Open in browser

=== Top CPU Consumers ===
SERVER:
  1. runtime.chanrecv (23.4%)
  2. syscall.write (18.2%)
  3. crypto/aes.gcmAesEnc (12.1%)

CLIENT-GENERATOR:
  1. runtime.chansend (28.7%)  ← BOTTLENECK
  2. syscall.write (15.3%)
  3. bytes.makeSlice (8.2%)

=== Recommendation ===
Client-generator channel overhead (28.7%) suggests buffered channels
or io_uring send path optimization would significantly improve throughput.
```

---

## 8. Decision Required

Please review this design and provide feedback on:

1. **Scope**: Is this the right set of features for v1?
2. **Phases**: Should any phases be combined or split?
3. **Comparison Mode**: Should we support more than Baseline vs HighPerf?
4. **Report Format**: HTML + text, or add other formats (JSON, markdown)?
5. **Priorities**: Adjust the implementation order?

---

*End of document*

