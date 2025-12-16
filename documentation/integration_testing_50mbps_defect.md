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

## 9. Appendix: Raw Test Output

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

*End of document*

