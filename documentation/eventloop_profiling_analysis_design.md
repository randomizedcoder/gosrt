# EventLoop Profiling Analysis Design

**Status**: Active
**Created**: 2026-01-17
**Parent**: [performance_testing_implementation_log.md](performance_testing_implementation_log.md)
**Goal**: Identify the bottleneck preventing throughput from exceeding ~360 Mb/s

---

## Executive Summary

Timer interval optimization proved that **timer overhead is NOT the bottleneck**:

| Timer Config | Timer Fires/sec | Max Throughput |
|--------------|-----------------|----------------|
| Default (10ms/20ms) | 260 | 353.75 Mb/s |
| High-Throughput (100ms/200ms) | 27 | ~360 Mb/s |
| Ultra-Long (500ms/1000ms) | ~7 | ~360 Mb/s |

A **97% reduction** in timer fires yielded only **2% throughput improvement**.

The bottleneck is somewhere in the EventLoop hot path. This document designs a comprehensive profiling approach to identify it.

---

## 1. Current Performance Ceiling

### 1.1 Observed Behavior

- **Maximum sustainable**: ~360 Mb/s
- **Target**: 500 Mb/s (4K ProRes 4444)
- **Gap**: ~140 Mb/s (28% below target)

### 1.2 Failure Pattern

```
╔══════════════════════════════════════════════════════════════╗
║  FAILURE ANALYSIS at 365 Mb/s
╠══════════════════════════════════════════════════════════════╣
║  Throughput Efficiency: 74.5%
║  Gap Rate: 0.000%   NAK Rate: 0.000%   RTT: 0.00ms
╠══════════════════════════════════════════════════════════════╣
║  🔴 HYPOTHESIS 2: EventLoop Starvation
║     Low throughput without packet loss
╚══════════════════════════════════════════════════════════════╝
```

**Key observation**: No packet loss, no NAKs, yet throughput drops significantly.
This indicates the **sender cannot generate packets fast enough**.

---

## 2. Hypothesized Bottlenecks

### 2.1 Sender-Side Bottlenecks

| # | Hypothesis | Description | Profile Type |
|---|------------|-------------|--------------|
| H1 | **Btree Iteration** | `deliverReadyPacketsEventLoop()` iterates btree on every loop | CPU |
| H2 | **Memory Allocation** | Per-packet allocations causing GC pressure | Heap, Allocs |
| H3 | **Ring Contention** | Lock-free ring operations causing cache invalidation | CPU, Mutex |
| H4 | **System Calls** | `sendmsg`/`io_uring` submission overhead | CPU |
| H5 | **TSBPD Calculation** | Time calculations on hot path | CPU |

### 2.2 Client-Seeker Bottlenecks

| # | Hypothesis | Description | Profile Type |
|---|------------|-------------|--------------|
| H6 | **TokenBucket** | Rate limiter spin-wait consuming CPU | CPU |
| H7 | **SRT Write** | Connection write blocking on socket | Block |
| H8 | **Packet Creation** | Allocating packets for transmission | Heap, Allocs |

### 2.3 Server-Side Bottlenecks

| # | Hypothesis | Description | Profile Type |
|---|------------|-------------|--------------|
| H9 | **io_uring CQE** | Completion queue processing overhead | CPU |
| H10 | **Packet Delivery** | Receiver EventLoop delivery path | CPU |
| H11 | **ACK Generation** | ACK packet creation overhead | CPU |

---

## 3. Profiling Strategy

### 3.1 Profile Types

| Profile | What It Shows | When to Use |
|---------|---------------|-------------|
| **CPU** | Where CPU time is spent | Always - primary tool |
| **Heap** | Current memory usage | Memory leaks, large allocations |
| **Allocs** | All allocations (even freed) | GC pressure analysis |
| **Mutex** | Lock contention | If CPU shows Lock/Unlock |
| **Block** | Goroutine blocking | Channel/sync.Cond issues |
| **Goroutine** | Current goroutine state | Deadlock diagnosis |

### 3.2 Multi-Component Profiling

Profile ALL components simultaneously to understand system-wide behavior:

```
┌─────────────────────────────────────────────────────────────────┐
│                    System Under Test                            │
├───────────────────┬───────────────────┬─────────────────────────┤
│  Client-Seeker    │     Server        │   (Client - optional)   │
│  ───────────────  │  ─────────────    │                         │
│  • TokenBucket    │  • io_uring recv  │                         │
│  • SRT Write      │  • EventLoop      │                         │
│  • Packet Gen     │  • Receiver       │                         │
├───────────────────┼───────────────────┼─────────────────────────┤
│  Profile via:     │  Profile via:     │                         │
│  pprof endpoint   │  pprof endpoint   │                         │
│  :6060            │  :6061            │                         │
└───────────────────┴───────────────────┴─────────────────────────┘
```

### 3.3 Expected Hot Functions

Based on architecture analysis, these are the **expected hot functions**:

**Sender EventLoop** (`congestion/live/send/eventloop.go`):
```go
func (s *sender) EventLoop(ctx context.Context)          // Main loop
func (s *sender) deliverReadyPacketsEventLoop(nowUs)     // TSBPD delivery
func (s *sender) drainRingToBtreeEventLoop()             // Ring → Btree
func (s *sender) processControlPacketsDelta()            // ACK/NAK handling
func (s *sender) dropOldPacketsEventLoop(nowUs)          // Drop stale packets
```

**Receiver EventLoop** (`congestion/live/receive/event_loop.go`):
```go
func (r *receiver) eventLoop(ctx context.Context)        // Main loop
func (r *receiver) deliverReadyPacketsEventLoop()        // Packet delivery
func (r *receiver) processControlPacketsDelta()          // Control handling
```

---

## 4. Implementation Plan

### Phase 1: File-Based Profiling (COMPLETED)

All components (server, client-generator, client, client-seeker) now support file-based profiling
using the `github.com/pkg/profile` package.

**Usage:**
```bash
# CPU profile (writes to profilepath when program exits)
./contrib/server/server -profile cpu -profilepath /tmp/profiles ...
./contrib/client-seeker/client-seeker -profile cpu -profilepath /tmp/profiles ...

# Other profile types:
#   cpu, mem, allocs, heap, rate, mutex, block, thread, trace
```

**Advantage**: No HTTP coordination needed - profile is written when program terminates.

### Phase 2: Automated Profile Capture

Extend the `performance` tool to capture profiles during test runs.

**New flags:**
```
-profile-duration 30s    # Duration for CPU profile
-profile-bitrate 350     # Capture at this bitrate (Mb/s)
-profile-types cpu,heap  # Profile types to capture
```

**Workflow:**
1. Run performance test until target bitrate reached
2. Pause search loop
3. Capture CPU profile for specified duration
4. Capture instant profiles (heap, goroutine, etc.)
5. Resume search or analyze

### Phase 3: Profile Analysis Automation

Leverage existing `contrib/integration_testing/profile_analyzer.go`:

```go
// Analyze profiles and generate comparison report
analyses, _ := AnalyzeAllProfiles("/tmp/srt_profiles")
PrintAnalysisSummary(analyses)

// For each profile, generate flame graph
for _, a := range analyses {
    fmt.Printf("Flame graph: %s\n", a.FlameGraph)
}
```

### Phase 4: Hot Path Instrumentation

Add fine-grained metrics to suspected hot functions:

```go
// In deliverReadyPacketsEventLoop:
iterStart := time.Now()
s.packetBtree.IterateFrom(uint32(startSeq), func(p packet.Packet) bool {
    // ... existing code ...
})
m.SendDeliveryIterDuration.Add(uint64(time.Since(iterStart).Microseconds()))
```

---

## 5. Profiling Scripts

### 5.1 Profile Capture Script (RECOMMENDED)

```bash
# Location: contrib/performance/scripts/profile_capture.sh

# Capture CPU profile at 350 Mb/s for 60 seconds
./contrib/performance/scripts/profile_capture.sh -b 350 -d 60 -p cpu

# Options:
#   -b, --bitrate MBPS   Target bitrate in Mb/s (default: 350)
#   -d, --duration SECS  Profile duration in seconds (default: 60)
#   -o, --output DIR     Output directory (default: auto-generated)
#   -p, --profile TYPE   Profile type: cpu, mem, heap, allocs, mutex, block
```

### 5.2 Profile Comparison Script

```bash
# Compare stable (300 Mb/s) vs failure (400 Mb/s)
./contrib/performance/scripts/profile_compare.sh /tmp/profiles_300M /tmp/profiles_400M
```

---

## 6. Manual Profiling Commands (Alternative)

### 6.1 Remote Profile Capture

```bash
# CPU profile (30 second sample)
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/profile?seconds=30

# Heap profile (instant)
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/heap

# Allocs profile (instant)
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/allocs

# Block profile (requires runtime.SetBlockProfileRate)
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/block

# Mutex profile (requires runtime.SetMutexProfileFraction)
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/mutex

# Goroutine dump
curl http://localhost:6060/debug/pprof/goroutine?debug=2
```

### 5.2 Analyze Saved Profiles

```bash
# Interactive analysis
go tool pprof /tmp/srt_profiles/cpu.pprof

# Top functions
go tool pprof -top /tmp/srt_profiles/cpu.pprof

# Flame graph (requires graphviz)
go tool pprof -svg /tmp/srt_profiles/cpu.pprof > flame.svg

# Web UI
go tool pprof -http=:8080 /tmp/srt_profiles/cpu.pprof
```

### 5.3 Specific Function Analysis

```bash
# Show time in specific function
go tool pprof -focus=deliverReadyPacketsEventLoop /tmp/srt_profiles/cpu.pprof

# Show callers of function
go tool pprof -focus=Iterate /tmp/srt_profiles/cpu.pprof
(pprof) web
```

---

## 6. Existing Profile Analysis Reference

The codebase already has profile analysis documents:

| Document | Focus |
|----------|-------|
| `cpu_profile_analysis.md` | System call overhead, Header() caching |
| `block_profile_analysis.md` | Channel blocking, sync primitives |
| `memory_profile_analysis.md` | Memory allocation patterns |
| `mutex_profile_analysis.md` | Lock contention points |
| `client_cpu_profile_analysis.md` | Client-specific CPU usage |

**Key findings from previous analysis** (`cpu_profile_analysis.md`):

1. **System calls**: 50.36% - Expected (io_uring)
2. **Runtime futex**: 24.24% - Expected (WaitCQE blocking)
3. **Header()**: 2.66% - **Optimization opportunity**
4. **Btree iterate**: 1.33% - Acceptable

---

## 7. Test Plan

### 7.1 Baseline Profile at 350 Mb/s

Run test at stable 350 Mb/s and capture comprehensive profiles:

```bash
# Start server with pprof
PPROF_ADDR=:6061 ./contrib/server/server -addr 127.0.0.1:6000 ...

# Start seeker with pprof
PPROF_ADDR=:6060 ./contrib/client-seeker/client-seeker ...

# Capture 30s CPU profile from both
go tool pprof -seconds=30 -output=seeker_cpu.pprof http://localhost:6060/debug/pprof/profile
go tool pprof -seconds=30 -output=server_cpu.pprof http://localhost:6061/debug/pprof/profile
```

### 7.2 Failure Point Profile at 365-370 Mb/s

Capture profiles when system starts failing:

```bash
# Run performance tool with profiling
./contrib/performance/performance \
  -initial 365000000 \
  -profile-duration 30s \
  -profile-types cpu,heap,allocs \
  ... other flags ...
```

### 7.3 Comparative Analysis

Compare baseline (350 Mb/s) vs failure point (365+ Mb/s):

```bash
# Use existing profile comparison
go run ./contrib/integration_testing/... analyze \
  --baseline=/tmp/profiles_350M \
  --compare=/tmp/profiles_365M
```

---

## 8. Optimization Targets

Based on previous analysis, potential optimizations (from `cpu_profile_analysis.md`):

### 8.1 Cache Header() in Hot Loops (HIGH IMPACT)

```go
// Before (periodicACK):
r.packetStore.Iterate(func(p packet.Packet) bool {
    if p.Header().PacketSequenceNumber.Lte(ackSequenceNumber) {
        return true
    }
    if p.Header().PktTsbpdTime <= now {  // Multiple Header() calls
        ackSequenceNumber = p.Header().PacketSequenceNumber
        return true
    }
    // ...
})

// After:
r.packetStore.Iterate(func(p packet.Packet) bool {
    h := p.Header()  // Cache once
    if h.PacketSequenceNumber.Lte(ackSequenceNumber) {
        return true
    }
    if h.PktTsbpdTime <= now {
        ackSequenceNumber = h.PacketSequenceNumber
        return true
    }
    // ...
})
```

**Expected impact**: 1-2% CPU reduction

### 8.2 Batch Delivery (MEDIUM IMPACT)

Instead of delivering one packet at a time, batch them:

```go
// Collect packets to deliver in a slice
var toDeliver []packet.Packet
s.packetBtree.IterateFrom(startSeq, func(p packet.Packet) bool {
    if p.Header().PktTsbpdTime <= nowUs && p.Header().TransmitCount == 0 {
        toDeliver = append(toDeliver, p)
    }
    return true
})

// Send batch
for _, p := range toDeliver {
    s.onDeliver(p)
}
```

### 8.3 Reduce Iteration Scope (HIGH IMPACT)

If the btree has many packets, iteration is O(n). Consider:

1. **Smaller btree** - More aggressive drops
2. **Sharded btrees** - By sequence range
3. **Skip list** - Different data structure with O(1) min access

---

## 9. Profiling Results (2026-01-17)

### 9.1 What We Did

1. **Created profiling scripts** in `contrib/performance/scripts/`:
   - `profile_capture.sh` - Automated CPU/heap profile capture at specified bitrate
   - `profile_compare.sh` - Compare profiles between stable and failure bitrates

2. **Added file-based profiling to client-seeker** using `github.com/pkg/profile`:
   - Same interface as server, client-generator, and client
   - Flags: `-profile cpu -profilepath /tmp/profiles`

3. **Ran profile capture at 350 Mb/s** for 30 seconds:
   ```bash
   ./contrib/performance/scripts/profile_capture.sh -b 350 -d 30 -p cpu
   ```

4. **Analyzed profiles** using `go tool pprof -top`

### 9.2 Observations

#### Seeker (Client-Seeker) Profile - **THE BOTTLENECK**

| Function | CPU % | Category |
|----------|-------|----------|
| `syscall.Syscall6` | 34.94% | ✅ Expected (network I/O) |
| `runtime.nanotime` | 22.58% | ⚠️ **TokenBucket time checks** |
| `time.Since` | 25.52% | ⚠️ **TokenBucket spin-wait** |
| `time.runtimeNano` | 23.79% | ⚠️ **TokenBucket timing** |
| `TokenBucket.spinWait` | 26.59% | ⚠️ **Rate limiter spinning** |
| `runtime.schedule` | 26.10% | ⚠️ **Goroutine thrashing** |

**Key Finding**: **~70% of seeker CPU is spent on time-related operations!**

The `spinWait` function in `tokenbucket.go` calls `time.Since(start)` in a tight loop:

```go
// tokenbucket.go:258
for time.Since(start) < duration {
    spins++
    // ...
}
```

At 350 Mb/s with 1456-byte packets:
- ~30,000 packets/second
- Each packet calls `ConsumeOrWait()` → `spinWait()` → tight `time.Since()` loop
- This consumes massive CPU just doing time checks

#### Server Profile - **HEALTHY**

| Function | CPU % | Category |
|----------|-------|----------|
| `syscall.Syscall6` | 40.02% | ✅ Expected (io_uring) |
| `runtime.futex` | 5.78% | ✅ Expected (blocking wait) |
| `btreePacketStore.IterateFrom` | 5.29% | ✅ Acceptable |
| `processRecvCompletion` | 21.84% | ✅ Expected |
| `runtime.mallocgc` | 9.18% | ⚠️ Some allocation overhead |

**Key Finding**: The server is spending CPU where expected (syscalls/io_uring).
The SRT library is NOT the bottleneck.

### 9.3 Root Cause Analysis

**The bottleneck is the client-seeker's TokenBucket, NOT the SRT library.**

The `RefillHybrid` mode (default) was designed for microsecond precision, but:
1. Creates excessive CPU overhead from `time.Since()` calls
2. Causes goroutine thrashing from frequent `runtime.Gosched()` calls
3. Leaves insufficient CPU for actual data transmission

The performance test infrastructure itself is limiting throughput!

### 9.4 Recommended Next Steps

#### Option 1: Change TokenBucket default to `RefillSleep` (RECOMMENDED)

**File**: `contrib/client-seeker/bitrate.go` line 43

```go
// Current (high CPU overhead):
bucket: NewTokenBucket(initialBitrate, RefillHybrid),

// Proposed (lower CPU, still accurate):
bucket: NewTokenBucket(initialBitrate, RefillSleep),
```

**Pros**:
- Simple one-line change
- Uses OS `time.Sleep()` instead of spin-wait
- Should dramatically reduce seeker CPU usage
- Allows more CPU for actual data transmission

**Cons**:
- Slightly less precise timing (1-15ms OS scheduler granularity)
- May cause micro-bursts at very high bitrates

**Expected Impact**:
- Reduce seeker CPU from ~70% time-related to ~10%
- Should allow testing the actual SRT library performance ceiling

#### Option 2: Add CLI flag for RefillMode

Add a flag to allow selecting the mode:
```
-tokenbucket-mode sleep|hybrid|spin
```

This allows experimentation without code changes.

#### Option 3: Batch token consumption

Instead of consuming 1 packet at a time, batch multiple packets:
```go
// Current: ConsumeOrWait(1456) per packet
// Proposed: ConsumeOrWait(1456 * 10) for 10 packets
```

This reduces the rate limiter call frequency by 10x.

### 9.5 Important Implications

1. **Previous ~360 Mb/s ceiling was NOT an SRT limit** - it was the testing tool!

2. **The actual SRT library performance is unknown** - we haven't been able to test it properly because the client-seeker was the bottleneck.

3. **Need to re-run all performance tests** after fixing the seeker.

---

## 10. Design: Tool vs Library Bottleneck Detection

### 10.1 The Problem

We discovered the client-seeker was the bottleneck, NOT the SRT library. This wasted significant debugging time because we had no metrics to distinguish **tool-limited** vs **library-limited** scenarios.

**Question**: How do we prevent this in the future?

### 10.2 Proposed Metrics for Client-Seeker

Add Prometheus metrics to expose internal tool performance:

#### TokenBucket Metrics (Rate Limiter)

| Metric | Type | Description | Bottleneck Indicator |
|--------|------|-------------|---------------------|
| `seeker_tokenbucket_wait_seconds_total` | Counter | Total time waiting for tokens | High = tool-limited |
| `seeker_tokenbucket_spin_seconds_total` | Counter | Time in spin-wait loop | High = CPU wasted |
| `seeker_tokenbucket_consume_count` | Counter | Number of consume calls | |
| `seeker_tokenbucket_consume_blocked_count` | Counter | Times consume had to wait | High = tool-limited |
| `seeker_tokenbucket_tokens_available` | Gauge | Current tokens available | Always full = library-limited |

#### Generator Metrics (Packet Creation)

| Metric | Type | Description | Bottleneck Indicator |
|--------|------|-------------|---------------------|
| `seeker_generator_packets_total` | Counter | Packets generated | |
| `seeker_generator_target_bps` | Gauge | Target bitrate | |
| `seeker_generator_actual_bps` | Gauge | Achieved bitrate | |
| `seeker_generator_efficiency` | Gauge | actual/target ratio | <0.95 = something limited |

#### SRT Write Metrics (Library Interface)

| Metric | Type | Description | Bottleneck Indicator |
|--------|------|-------------|---------------------|
| `seeker_srt_write_seconds_total` | Counter | Time in SRT Write() | High = library-limited |
| `seeker_srt_write_blocked_count` | Counter | Times Write() blocked | High = library-limited |
| `seeker_srt_write_errors_total` | Counter | Write errors | |

### 10.3 Bottleneck Detection Logic

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    AUTOMATED BOTTLENECK DETECTION                           │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  IF generator_efficiency < 0.95:                                            │
│      IF tokenbucket_wait_seconds / elapsed > 0.10:                          │
│          → 🔴 TOOL-LIMITED: TokenBucket rate limiter overhead               │
│      ELIF srt_write_seconds / elapsed > 0.50:                               │
│          → 🟢 LIBRARY-LIMITED: SRT Write() is slow                          │
│      ELIF tokenbucket_tokens_available consistently high:                   │
│          → 🟢 LIBRARY-LIMITED: Tokens available but can't send              │
│      ELSE:                                                                  │
│          → 🟡 UNKNOWN: Need CPU profile                                     │
│                                                                             │
│  IF generator_efficiency >= 0.95:                                           │
│      → ✅ HEALTHY: Tool keeping up with target rate                         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 10.4 Implementation in StabilityGate

The performance tool's `StabilityGate` already analyzes metrics. We can add:

```go
// In gate.go - add to evaluateMetrics()

func (g *StabilityGate) detectBottleneck(metrics StabilityMetrics) string {
    // Check if generator is keeping up
    if metrics.SeekerEfficiency < 0.95 {
        // Check where time is being spent
        tokenWaitRatio := metrics.TokenBucketWaitSeconds / metrics.ElapsedSeconds
        srtWriteRatio := metrics.SRTWriteSeconds / metrics.ElapsedSeconds

        if tokenWaitRatio > 0.10 {
            return "TOOL-LIMITED: TokenBucket overhead (%.1f%% of time in rate limiter)"
        }
        if srtWriteRatio > 0.50 {
            return "LIBRARY-LIMITED: SRT Write blocking (%.1f%% of time in Write())"
        }
        if metrics.TokensAvailable > metrics.TokensCapacity * 0.8 {
            return "LIBRARY-LIMITED: Tokens accumulating (library can't consume)"
        }
        return "UNKNOWN: Need CPU profile for diagnosis"
    }
    return "HEALTHY: Tool keeping up with target"
}
```

### 10.5 Enhanced Failure Report

Update the hypothesis report to include tool health:

```
╔═══════════════════════════════════════════════════════════════════════════╗
║                           FAILURE ANALYSIS at 400 Mb/s                    ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  Throughput Efficiency: 87.5%                                             ║
║  Gap Rate: 0.000%   NAK Rate: 0.000%   RTT: 0.35ms                        ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  TOOL HEALTH CHECK:                                                       ║
║    TokenBucket Wait:     2.1% of time  ✅ OK                              ║
║    SRT Write Time:      45.3% of time  ✅ OK                              ║
║    Tokens Available:    12% of capacity ✅ OK                             ║
║    Generator Efficiency: 87.5%         ⚠️ LOW                             ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  DIAGNOSIS: 🟢 LIBRARY-LIMITED                                            ║
║    Tool is healthy, bottleneck is in SRT library                          ║
║    → Check server CPU/EventLoop profiles                                  ║
╚═══════════════════════════════════════════════════════════════════════════╝
```

### 10.6 Quick Health Check Command

Add a CLI command to check tool health:

```bash
# Check if tool is healthy at current bitrate
./contrib/client-seeker/client-seeker -health-check -target srt://... -initial 400000000

# Output:
# Tool Health Check at 400 Mb/s:
#   TokenBucket CPU:    3.2%  ✅ (< 10%)
#   Generator Latency:  0.8ms ✅ (< 5ms)
#   SRT Write Latency:  2.1ms ✅
#   Overall:           HEALTHY - tool is not the bottleneck
```

### 10.7 Design Principles for Future Tools

1. **Always instrument internal operations** - every wait, every call to external code
2. **Expose "overhead" metrics** - how much time spent in tool code vs useful work
3. **Add health checks** - automated detection of self-imposed limits
4. **Design for observability first** - metrics before implementation
5. **Include tool metrics in failure reports** - always answer "is the tool limiting?"

### 10.8 Immediate Action Items

1. **Add TokenBucket Prometheus metrics** to `client-seeker`
2. **Add efficiency metric** (actual_bps / target_bps) to generator
3. **Add bottleneck detection** to StabilityGate
4. **Update failure report** to include tool health

---

## 11. Definition of Done

This profiling analysis is complete when:

- [ ] HTTP pprof endpoints added to server and seeker
- [ ] Automated profile capture integrated into performance tool
- [ ] Baseline profiles captured at 350 Mb/s
- [ ] Failure point profiles captured at 365+ Mb/s
- [ ] Top 3 CPU consumers identified
- [ ] Root cause of 360 Mb/s ceiling documented
- [ ] At least one optimization implemented and tested
- [ ] Throughput increased by at least 10% (to ~400 Mb/s)

---

## 10. Related Documents

- [performance_testing_implementation_log.md](performance_testing_implementation_log.md) - Parent log
- [configurable_timer_intervals_design.md](configurable_timer_intervals_design.md) - Timer optimization
- [cpu_profile_analysis.md](cpu_profile_analysis.md) - Previous CPU analysis
- [sender_lockfree_architecture.md](sender_lockfree_architecture.md) - EventLoop design
- [completely_lockfree_receiver.md](completely_lockfree_receiver.md) - Receiver design
