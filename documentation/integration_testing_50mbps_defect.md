# Integration Testing 50 Mb/s Performance Defect

**Document:** `integration_testing_50mbps_defect.md`
**Created:** 2025-12-16
**Status:** 🔴 Open - Investigation Required
**Severity:** Medium (blocks Tier 2/3 test validation at 50 Mb/s)

---

## 1. Overview

### 1.1 Objective

As part of the comprehensive integration testing framework for goSRT, we are implementing a matrix-based test generator to systematically validate the library across multiple dimensions:

- **Bitrates:** 20 Mb/s, 50 Mb/s
- **Buffer sizes:** 1s, 5s, 10s, 30s
- **Configurations:** Base, NakBtree, NakBtreeF, NakBtreeFr, Full
- **RTT profiles:** R0 (0ms), R10 (10ms), R60 (60ms), R130 (130ms), R300 (300ms)

The design is detailed in [`integration_testing_matrix_design.md`](./integration_testing_matrix_design.md), which defines:
- Tiered test execution (Tier 1: Core, Tier 2: Extended, Tier 3: Comprehensive)
- Standardized naming convention for all tests
- Strategic test selection to manage combinatorial explosion

The NAK btree integration testing plan is documented in [`nak_btree_integration_testing.md`](./nak_btree_integration_testing.md), which tracks:
- Phase 1: Isolation Tests (✅ Complete - 17/17 passed)
- Phase 2: Network Loss Tests (✅ Complete)
- Phase 3: Parallel Comparison Tests (✅ Complete)

### 1.2 Current Progress

Per the implementation progress in `integration_testing_matrix_design.md`:

| Phase | Status | Notes |
|-------|--------|-------|
| Phase 1-6 | ✅ Complete | Foundation, generator, CLI, clean network tests |
| Phase 7 | 🔄 Blocked | **50 Mb/s tests failing on clean network** |
| Phase 8 | ✅ Complete | Test name migration |
| Phase 9 | ⏳ Pending | Documentation |

---

## 2. Problem Description

### 2.1 Observed Behavior

When running 50 Mb/s clean network tests (no packet loss, no latency injection), we observe significant degradation that should not occur on a clean loopback network:

**Test:** `Int-Clean-50M-5s-NakBtree` (60 second duration)

| Metric | Expected | Observed | Delta |
|--------|----------|----------|-------|
| Sustained throughput | 50 Mb/s | ~35 Mb/s | -30% |
| Packets sent (CG) | ~285,000 | 295,642 | OK |
| Packets received (Server) | ~285,000 | 259,615 | **-12%** |
| NAKs sent | 0 | 160,565+ | **Unexpected** |
| Retransmissions | 0 | 12,928 | **4.2%** |
| Client drops | 0 | 9,576 | **Unexpected** |

### 2.2 Symptoms

1. **Ingress Imbalance:** Server receives 12% fewer packets than client-generator sends
2. **Spurious NAKs:** 160K+ NAKs on a network with 0% configured loss
3. **Retransmissions:** 4.2% retransmit rate when 0% expected
4. **Throughput Cap:** Cannot sustain 50 Mb/s; settles at ~35 Mb/s
5. **Client Drops:** Subscriber drops packets despite clean network

### 2.3 Contrast with 20 Mb/s Tests

20 Mb/s tests pass cleanly:

| Metric | 20 Mb/s | 50 Mb/s |
|--------|---------|---------|
| Throughput | ✅ Sustained | ❌ Capped at ~35 |
| NAKs | 0 | 160K+ |
| Drops | 0 | 9.5K+ |
| Retrans | 0 | 4.2% |

This suggests a **performance ceiling** between 35-50 Mb/s in the current test infrastructure.

---

## 3. Analysis

### 3.1 What We Know

1. **goSRT does not implement congestion control** - The SRT protocol in goSRT does not have CC algorithms that would artificially throttle throughput.

2. **Clean network = 0% loss** - The test runs on loopback (127.0.0.x) with no `tc netem` impairment configured.

3. **Lower bitrates work fine** - 20 Mb/s tests pass with 0 NAKs, 0 drops, 0 retransmissions.

4. **The problem is in the test infrastructure** - The NAKs and drops indicate the sender/receiver can't keep up, not network loss.

### 3.2 Packet Rate Calculation

At 50 Mb/s with typical SRT packet size (~1316 bytes payload):

```
50,000,000 bits/sec ÷ 8 = 6,250,000 bytes/sec
6,250,000 ÷ 1316 ≈ 4,750 packets/second
```

At 20 Mb/s:
```
20,000,000 ÷ 8 ÷ 1316 ≈ 1,900 packets/second
```

The 50 Mb/s test requires **2.5x the packet rate** of 20 Mb/s.

---

## 4. Hypotheses

### 4.1 Hypothesis A: Go Channel Overhead in Client-Generator

**Description:** The `client-generator` application uses Go channels to pass data between goroutines. At high packet rates (~4,750 pkt/s), the channel synchronization overhead may become a bottleneck.

**Evidence:**
- Go channels use mutex locks internally
- At high packet rates, lock contention increases
- The client-generator does NOT use io_uring for the send path

**Testable:** Profile with `go tool pprof` to check for mutex contention

### 4.2 Hypothesis B: CPU Saturation

**Description:** Single-core saturation from packet processing at high rates.

**Evidence:**
- goSRT runs packet handling on the main goroutine
- At 4,750 pkt/s, there's ~210µs per packet
- Complex operations (encryption, checksums, btree operations) may exceed this budget

**Testable:** Monitor CPU usage during 50 Mb/s test

### 4.3 Hypothesis C: io_uring Not Used on Send Path

**Description:** The NAK btree tests enable `io_uring` for receive but not send. The send path uses traditional `WriteTo()` which has more syscall overhead.

**Evidence:**
- Config shows `IoUringRecvEnabled: true` but `IoUringEnabled: false` (send)
- io_uring batch processing reduces syscall overhead significantly
- Receive path benefits don't help if send path is bottlenecked

**Testable:** Enable io_uring send and re-run 50 Mb/s test

### 4.4 Hypothesis D: Internal Buffer Contention

**Description:** High packet rates may cause contention on internal buffers (send buffer, receive buffer, packet store).

**Evidence:**
- SRT uses flow control windows
- At high rates, buffers may fill faster than they drain
- This would cause backpressure and drops

**Testable:** Increase buffer sizes and observe behavior

### 4.5 Hypothesis E: Timer Resolution

**Description:** The periodic ACK/NAK timers may not fire accurately enough at high packet rates.

**Evidence:**
- Default NAK interval is 20ms
- Default ACK interval is 10ms
- At 4,750 pkt/s, ~95 packets arrive between NAK checks

**Testable:** Reduce timer intervals and observe

---

## 5. Proposed Next Steps

### 5.1 Immediate Actions (Unblock Tier 2/3)

| Option | Description | Effort | Risk |
|--------|-------------|--------|------|
| **A** | Reduce 50M tests to 30-40M | Low | May miss real performance issues |
| **B** | Mark 50M clean tests as "characterization only" | Low | Test exists but doesn't fail |
| **C** | Skip 50M clean tests; keep 50M network tests | Low | Partial coverage |

**Recommendation:** Option B or C - Keep tests but mark as non-blocking for now.

### 5.2 Investigation Actions (Root Cause)

| Step | Action | Goal |
|------|--------|------|
| 1 | **Profile with pprof** | Identify CPU hotspots and lock contention |
| 2 | **Enable io_uring send** | Test if send path is the bottleneck |
| 3 | **Monitor system resources** | CPU, memory, context switches during test |
| 4 | **Compare with C++ srt-live-transmit** | Is this a goSRT limitation or test infra? |

### 5.3 Potential Fixes

| Fix | Description | Effort |
|-----|-------------|--------|
| Add io_uring to client-generator send | Reduce syscall overhead | Medium |
| Optimize channel usage in client-generator | Reduce lock contention | High |
| Use buffered channels | Reduce synchronization | Medium |
| Bypass channels entirely for send path | Direct writes | High |

---

## 6. Test Commands

```bash
# Run the specific failing test
go run ./contrib/integration_testing Int-Clean-50M-5s-NakBtree

# List all 50M tests
make test-clean-matrix-tier2-list | grep 50M

# Run with profiling (if implemented)
PPROF=true go run ./contrib/integration_testing Int-Clean-50M-5s-NakBtree

# Compare with 20M test
go run ./contrib/integration_testing Int-Clean-20M-5s-NakBtree
```

---

## 7. Related Documents

- [`integration_testing_matrix_design.md`](./integration_testing_matrix_design.md) - Matrix testing design and Phase 7 details
- [`nak_btree_integration_testing.md`](./nak_btree_integration_testing.md) - NAK btree test plan
- [`nak_btree_debugging.md`](./nak_btree_debugging.md) - Previous NAK btree debugging (resolved)
- [`integration_testing_design.md`](./integration_testing_design.md) - Overall integration testing framework

---

## 8. Decision Required

Before proceeding, please choose:

- [ ] **Option A:** Reduce 50M tests to 30M (permanent change)
- [ ] **Option B:** Mark 50M clean tests as "characterization" (non-blocking)
- [ ] **Option C:** Skip 50M clean tests entirely for now
- [ ] **Option D:** Investigate root cause before proceeding (may take days)
- [ ] **Option E:** Other (please specify)

---

## 9. Proposed Enhancement: Automated Profiling Mode

### 9.1 Motivation

This defect highlights the need for **automated performance profiling** integrated into the test framework. Currently, diagnosing performance issues requires:

1. Manually adding profiling code
2. Running tests multiple times with different profile types
3. Manually analyzing each profile output
4. Correlating findings across profile types

This is time-consuming and error-prone. A better approach would be a **built-in profiling mode** that can be enabled on demand.

### 9.2 Design Concept

#### Usage

```bash
# Run a specific test with all profiling enabled
PROFILES=all make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr

# Run clean network test with profiling
PROFILES=all go run ./contrib/integration_testing Int-Clean-50M-5s-NakBtree

# Run parallel test with specific profiles only
PROFILES=cpu,mutex make test-parallel CONFIG=Parallel-Starlink-5M-Base-vs-Full
```

#### Profile Types (from `contrib/server/main.go`)

| Profile Type | Flag Value | What It Measures |
|--------------|------------|------------------|
| CPU | `cpu` | CPU time distribution across functions |
| Memory | `mem` | Memory allocation by function |
| Allocations | `allocs` | Number of allocations (not size) |
| Heap | `heap` | Heap memory in use |
| Mutex | `mutex` | Lock contention and wait time |
| Block | `block` | Goroutine blocking (I/O, channels, etc.) |
| Thread | `thread` | Thread creation |
| Trace | `trace` | Execution trace for `go tool trace` |

### 9.3 Proposed Workflow

```
┌─────────────────────────────────────────────────────────────────┐
│  PROFILES=all go run ./integration_testing Int-Clean-50M-...   │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  1. Create temp directory: /tmp/profile_50M_20251216_143022/    │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  2. Run test iterations with each profile type:                 │
│                                                                 │
│     Iteration 1: -profile=cpu    → cpu.pprof    (120s)          │
│     Iteration 2: -profile=mutex  → mutex.pprof  (120s)          │
│     Iteration 3: -profile=block  → block.pprof  (120s)          │
│     Iteration 4: -profile=heap   → heap.pprof   (120s)          │
│     Iteration 5: -profile=allocs → allocs.pprof (120s)          │
│     Iteration 6: -profile=trace  → trace.out    (60s)           │
│                                                                 │
│     Total time: ~10 minutes                                     │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  3. Analyze each profile automatically:                         │
│                                                                 │
│     go tool pprof -top cpu.pprof > cpu_top.txt                  │
│     go tool pprof -svg cpu.pprof > cpu_flamegraph.svg           │
│     go tool pprof -top mutex.pprof > mutex_top.txt              │
│     ... etc                                                     │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  4. Generate summary report: report.html                        │
│                                                                 │
│     ┌─────────────────────────────────────────────────────┐     │
│     │  Performance Profile Report                         │     │
│     │  Test: Int-Clean-50M-5s-NakBtree                    │     │
│     │  Date: 2025-12-16 14:30:22                          │     │
│     ├─────────────────────────────────────────────────────┤     │
│     │  CPU Hot Spots:                                     │     │
│     │    1. runtime.chanrecv (23%)                        │     │
│     │    2. syscall.write (18%)                           │     │
│     │    3. crypto/aes.gcmAesEnc (12%)                    │     │
│     ├─────────────────────────────────────────────────────┤     │
│     │  Mutex Contention:                                  │     │
│     │    1. sync.(*Mutex).Lock - 45ms wait                │     │
│     │    2. runtime.chansend - 23ms wait                  │     │
│     ├─────────────────────────────────────────────────────┤     │
│     │  Memory Allocations:                                │     │
│     │    1. bytes.makeSlice - 1.2GB total                 │     │
│     │    2. packet.NewPacket - 800MB total                │     │
│     ├─────────────────────────────────────────────────────┤     │
│     │  [Embedded Flame Graphs]                            │     │
│     │  [Link to trace viewer]                             │     │
│     └─────────────────────────────────────────────────────┘     │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  5. Output summary:                                             │
│                                                                 │
│     Profile results saved to:                                   │
│       /tmp/profile_50M_20251216_143022/                         │
│                                                                 │
│     Files:                                                      │
│       cpu.pprof, cpu_top.txt, cpu_flamegraph.svg                │
│       mutex.pprof, mutex_top.txt                                │
│       block.pprof, block_top.txt                                │
│       heap.pprof, heap_top.txt                                  │
│       allocs.pprof, allocs_top.txt                              │
│       trace.out                                                 │
│       report.html  ← Open this in browser                       │
│                                                                 │
│     Top Issue: runtime.chanrecv (23% CPU)                       │
│     Recommendation: Consider buffered channels or io_uring      │
└─────────────────────────────────────────────────────────────────┘
```

### 9.4 Implementation Considerations

#### Where to Add Profiling

| Component | Profiling Value | Notes |
|-----------|-----------------|-------|
| `server` | High | Already has `-profile` flag |
| `client-generator` | High | Sender path - likely bottleneck |
| `client` | Medium | Receiver path |

#### Profile Duration

| Profile Type | Duration | Rationale |
|--------------|----------|-----------|
| CPU | 120s | Need sustained load for accurate sampling |
| Mutex | 120s | Need lock contention to accumulate |
| Block | 120s | Need blocking events to occur |
| Heap | 60s | Snapshot at end is sufficient |
| Allocs | 60s | Cumulative, shorter OK |
| Trace | 30s | Files get large quickly |

#### Integration Points

1. **Isolation Tests** (`test_isolation_mode.go`)
   - Add `PROFILES` environment variable check
   - Run multiple iterations if profiling enabled
   - Collect profiles from both Control and Test pipelines

2. **Parallel Tests** (`test_parallel_mode.go`)
   - Profile both Baseline and HighPerf pipelines
   - Compare profiles between pipelines
   - Highlight differences in contention/allocation patterns

3. **Clean Network Tests** (`test_graceful_shutdown.go`)
   - Add profile collection for matrix-generated tests
   - Useful for characterizing performance limits

### 9.5 Report Generation

The report could be generated using:

1. **`go tool pprof`** - Extract top functions, generate SVG flame graphs
2. **`go tool trace`** - Generate trace viewer link
3. **HTML template** - Combine all outputs into single navigable report

Example automated analysis:

```bash
# Extract top 20 CPU consumers
go tool pprof -top -nodecount=20 cpu.pprof

# Generate flame graph SVG
go tool pprof -svg cpu.pprof > cpu_flamegraph.svg

# Check for lock contention
go tool pprof -top mutex.pprof | grep -E "(Mutex|Lock|chan)"

# Memory hotspots
go tool pprof -top -alloc_space heap.pprof
```

### 9.6 Benefits for This Defect

If this profiling mode existed, we could run:

```bash
PROFILES=cpu,mutex,block go run ./contrib/integration_testing Int-Clean-50M-5s-NakBtree
```

And immediately get:
- CPU flame graph showing where time is spent
- Mutex contention report (likely channels)
- Block profile showing goroutine waits

This would confirm or refute our hypotheses within minutes instead of hours of manual investigation.

### 9.7 Related Documentation

- [`integration_testing_design.md`](./integration_testing_design.md) - Section on profiling integration
- [`parallel_comparison_test_design.md`](./parallel_comparison_test_design.md) - Parallel test profiling

### 9.8 Implementation Priority

| Priority | Item | Effort |
|----------|------|--------|
| P1 | Add `-profile` flag passthrough to integration tests | Low |
| P2 | Collect profiles from all components | Medium |
| P3 | Auto-generate text summary (top functions) | Medium |
| P4 | Generate HTML report with flame graphs | High |
| P5 | Add comparison mode (before/after) | High |

---

## 10. Appendix: Raw Test Output

```
Test: Int-Clean-50M-5s-NakBtree
Duration: 60s
Tier: 2

--- Results ---
Actual throughput: ~35 Mb/s (target: 50 Mb/s)
Client-gen packets sent: 295,642
Server packets received: 259,615
Ingress imbalance: 12%
NAKs sent: 160,565+
Retransmissions: 12,928 (4.2%)
Client drops: 9,576
```

---

## 11. Decision Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2025-12-16 | Document created | Capture 50M performance issue |
| | | |

---

*End of document*

