# Multi-Stage Profiling Plan for SRT Library Optimization

## Goal
Identify the bottlenecks in the SRT library that limit throughput to ~375 Mb/s, with the goal of reaching 500 Mb/s.

## Context
- **Current ceiling**: ~375 Mb/s (confirmed LIBRARY-LIMITED, not tool-limited)
- **Target**: 500 Mb/s (4K video production)
- **Bottleneck hypothesis**: EventLoop Starvation (from instrumentation)

## Profiling Strategy

We'll capture profiles at 3 bitrate levels to understand the behavior:

| Stage | Bitrate | Purpose | Duration |
|-------|---------|---------|----------|
| 1 | 300 Mb/s | **Baseline** - stable, healthy operation | 30s |
| 2 | 370 Mb/s | **Near-ceiling** - where things start to struggle | 30s |
| 3 | 400 Mb/s | **Overload** - where system fails | 30s |

## Profile Types to Capture

| Profile | Purpose | Key Metrics |
|---------|---------|-------------|
| `cpu` | Where CPU time is spent | syscall %, EventLoop time, header access |
| `mutex` | Lock contention | Which mutexes, wait time |
| `block` | Blocking operations | I/O waits, channel ops |
| `heap` | Memory allocation pressure | Alloc rate, GC pressure |
| `goroutine` | Concurrency issues | Goroutine counts, stuck goroutines |

## Stage 1: Baseline (300 Mb/s)

**Objective**: Understand healthy operation patterns

```bash
# Run for 30 seconds at 300 Mb/s with CPU profiling
./contrib/performance/scripts/run_profile.sh 300 cpu 30

# Run with all profiles
./contrib/performance/scripts/run_profile.sh 300 all 30
```

**Expected observations**:
- Server CPU usage < 50%
- Low mutex contention
- Stable memory usage
- No blocking issues

## Stage 2: Near-Ceiling (370 Mb/s)

**Objective**: Identify what starts to struggle

```bash
./contrib/performance/scripts/run_profile.sh 370 cpu 30
./contrib/performance/scripts/run_profile.sh 370 all 30
```

**Expected observations**:
- Server CPU usage approaching 100% on one core
- Possible mutex contention appearing
- Memory allocation rate increasing
- EventLoop may show strain

## Stage 3: Overload (400 Mb/s)

**Objective**: Capture failure mode

```bash
./contrib/performance/scripts/run_profile.sh 400 cpu 30
./contrib/performance/scripts/run_profile.sh 400 all 30
```

**Expected observations**:
- Connection likely dies within 5-10 seconds
- Capture what's consuming CPU at failure point
- Identify if it's locks, GC, or EventLoop

## Analysis Workflow

### Step 1: Generate Top Functions Report

```bash
go tool pprof -top server_cpu.pprof | head -30
go tool pprof -top seeker_cpu.pprof | head -30
```

### Step 2: Generate Flame Graphs

```bash
go tool pprof -svg server_cpu.pprof > server_cpu_flame.svg
go tool pprof -svg server_heap.pprof > server_heap_flame.svg
```

### Step 3: Compare Across Stages

```bash
# Compare 300 Mb/s vs 370 Mb/s
go tool pprof -diff_base=stage1/server_cpu.pprof stage2/server_cpu.pprof
```

### Step 4: Focus on Hot Functions

Based on existing analysis (cpu_profile_analysis.md), key areas to examine:

1. **`deliverReadyPacketsEventLoop()`** - The main send path
2. **`processRecvCompletion()`** - Receive completion handling
3. **`periodicACK/NAK`** - Timer callbacks
4. **`packet.Header()`** - Called millions of times
5. **`btree.iterate`** - Packet store iteration

## Known Optimization Opportunities (from cpu_profile_analysis.md)

### HIGH IMPACT
1. **Cache `Header()` in hot loops** - 60-80% reduction in Header() calls
2. **Batch ACK/NAK processing** - Reduce timer callback overhead

### MEDIUM IMPACT
3. **Inline critical paths** - Avoid function call overhead
4. **Pool allocations** - Reduce GC pressure

## Scripts

### run_profile.sh
Captures a single profile run at specified bitrate.

### analyze_profiles.sh
Generates comparison reports and flame graphs.

### summary_report.sh
Creates markdown summary of all findings.

## Success Criteria

After optimization, we should see:
1. **CPU**: syscall% should be > 60% (more time in kernel I/O)
2. **EventLoop**: deliverReadyPacketsEventLoop < 10% of CPU
3. **Throughput**: 450+ Mb/s sustainable
4. **No new bottlenecks**: Mutex/block profiles clean
