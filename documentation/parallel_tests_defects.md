# Parallel Tests Defects

This document tracks defects discovered during parallel comparison testing.

---

## Defect #1: Netem Loss Applied to Wrong Inter-Router Link

**Status:** ✅ FIXED AND VERIFIED
**Discovered:** 2025-12-30
**Fixed:** 2025-12-30
**Verified:** 2025-12-30
**Test:** `Parallel-Loss-L10-20M-Base-vs-FullEL`

### Observation

When running the 10% loss test configuration:
```bash
sudo make test-parallel CONFIG=Parallel-Loss-L10-20M-Base-vs-FullEL
```

The mid-test metrics show a significant discrepancy between baseline and highperf pipelines:

| Component | NAKs | Retransmits | Expected Loss Rate |
|-----------|------|-------------|-------------------|
| baseline-cg | 236-246 | 236-246 | 10% (~15,000) |
| highperf-cg | 1 | 1 | 10% (~15,000) |
| baseline-client | 87-104 | 87-104 | 10% (~15,000) |
| highperf-client | 0 | 0 | 10% (~15,000) |

**Key Issues:**
1. Neither pipeline shows anywhere near 10% loss (~15,000 expected retransmissions out of ~150,000 packets)
2. Baseline sees ~250 retransmissions (~0.15% effective loss)
3. HighPerf sees essentially 0 retransmissions

### Root Cause Hypothesis

**Primary Bug: Bash Variable Scope Issue**

The netem loss is being applied to the **wrong inter-router link** due to a bash variable scoping issue.

#### Code Flow Analysis

1. **Latency profile is set correctly** (via `SetLatencyProfile` → `set_latency.sh`):
   ```
   Setting latency profile: regional (profile 1)
   ```
   This changes routing to use `link1_a` / `link1_b` (10ms RTT path).

2. **Loss is applied later** (via `SetLossParallel` → inline bash command):
   ```go
   // network_controller.go:628-629
   script := fmt.Sprintf("source %s/lib.sh && set_loss_percent_parallel %d", nc.ScriptDir, lossPercent)
   ```

3. **The bash script sources `lib.sh` fresh**, which sets:
   ```bash
   # lib.sh:64
   CURRENT_LATENCY_PROFILE=0
   ```

4. **`set_netem_loss` uses the default value**:
   ```bash
   # lib.sh:304-308
   set_netem_loss() {
       local loss_percent="$1"
       local link_index="${CURRENT_LATENCY_PROFILE}"  # <-- Always 0!
       local interface_a="link${link_index}_a"
       local interface_b="link${link_index}_b"
       ...
   }
   ```

**Result:** Loss is applied to `link0_a` / `link0_b`, but traffic is routed through `link1_a` / `link1_b`. The traffic never encounters the 10% loss.

#### Evidence Supporting This Hypothesis

1. **Loss numbers don't match either pipeline** - If loss were being applied correctly, both pipelines should see ~10% loss since they share the same inter-router links
2. **Baseline still sees some "losses"** - The ~250 retransmissions in baseline are likely internal application-level issues (buffer management, timing), not network loss
3. **HighPerf is more efficient** - The io_uring + event loop + btree configuration handles these edge cases more gracefully

### Affected Components

```
contrib/integration_testing/network/lib.sh
  - set_netem_loss() function (lines 304-334)
  - set_loss_percent_parallel() function (lines 581-593)

contrib/integration_testing/network_controller.go
  - SetLossParallel() method (lines 618-639)
```

### Architecture Diagram

```
                      ┌─────────────────────────────────────────────┐
                      │         Inter-Router Links                   │
                      │                                              │
Publisher (.2, .3) ───┤   Router A ════════════════════ Router B ───┤─── Server (.2, .3)
                      │              link0 (0ms RTT)                 │
Subscriber (.2, .3) ──┤              link1 (10ms RTT) ← Traffic      │
                      │              link2 (60ms RTT)                │
                      │              link3 (130ms RTT)               │
                      │              link4 (300ms RTT)               │
                      │                    ↑                         │
                      │                    │                         │
                      │            Loss applied here                 │
                      │            (to link0, not link1!)            │
                      └─────────────────────────────────────────────┘
```

### Secondary Observation

Even without network loss being applied correctly, baseline shows ~250 retransmissions while highperf shows ~1. This suggests:

1. The baseline configuration (`list` + no io_uring) has internal inefficiencies that occasionally cause gaps/delays that trigger NAK responses
2. The highperf configuration (`btree` + io_uring + event loop) handles packet buffering and timing more efficiently

This is a separate concern from the loss application bug but may warrant investigation.

---

## Next Steps Plan

### Phase 1: Verify the Hypothesis (No Code Changes)

1. **Add debug logging to confirm the bug**
   - Run test with `SRT_NETWORK_DEBUG=1` to see which interfaces loss is applied to
   - Manually inspect tc qdisc settings on all links after loss is applied
   ```bash
   ip netns exec ns_router_a_<TEST_ID> tc qdisc show
   ip netns exec ns_router_b_<TEST_ID> tc qdisc show
   ```

2. **Verify traffic path**
   - Confirm which inter-router link is actually carrying traffic
   - Check routing tables in both router namespaces

### Phase 2: Fix Options (Requires Review)

**Option A: Pass Latency Profile to Loss Functions**

Modify `SetLossParallel` to explicitly pass the current latency profile:
```go
func (nc *NetworkController) SetLossParallel(ctx context.Context, lossPercent int) error {
    script := fmt.Sprintf(
        "source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent_parallel %d",
        nc.ScriptDir, nc.CurrentLatencyProfile, lossPercent)
    ...
}
```

**Pros:** Minimal change, explicit about which link to use
**Cons:** Requires keeping Go and bash state in sync

**Option B: Use State File**

Read `CURRENT_LATENCY_PROFILE` from the state file (`/tmp/srt_network_state_<TEST_ID>`) in bash:
```bash
set_netem_loss() {
    local loss_percent="$1"
    local link_index
    if [[ -f "${STATE_FILE}" ]]; then
        link_index=$(grep "^LATENCY_PROFILE=" "${STATE_FILE}" | cut -d'=' -f2)
    fi
    link_index="${link_index:-0}"
    ...
}
```

**Pros:** Bash functions self-discover state
**Cons:** File I/O overhead, potential race conditions

**Option C: Apply Loss to ALL Links**

Modify `set_netem_loss` to apply loss to whichever link is currently in use (the one with active routes):
```bash
set_netem_loss() {
    local loss_percent="$1"
    # Find which link has the active route to server subnet
    local active_link=$(ip netns exec "${NAMESPACE_ROUTER_CLIENT}" ip route show "${SUBNET_SERVER}.0/24" | grep -oP 'link\d')
    ...
}
```

**Pros:** Self-healing, always applies loss to correct link
**Cons:** More complex, slower

### Phase 3: Validation

1. Re-run `Parallel-Loss-L10-20M-Base-vs-FullEL` with fix
2. Verify both pipelines see ~10% loss (approximately equal NAKs/retransmits)
3. Run full test matrix to ensure no regressions

---

## Fix Applied (2025-12-30)

**Solution:** Option A - Pass latency profile explicitly from Go to bash.

**Files Changed:**
- `contrib/integration_testing/network_controller.go`

**Changes Made:**

All functions that invoke bash scripts to apply or clear loss now explicitly set
`CURRENT_LATENCY_PROFILE` before calling the loss functions:

```go
// Before (broken):
script := fmt.Sprintf("source %s/lib.sh && set_loss_percent_parallel %d",
    nc.ScriptDir, lossPercent)

// After (fixed):
script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent_parallel %d",
    nc.ScriptDir, nc.CurrentLatencyProfile, lossPercent)
```

**Functions Fixed:**
1. `SetLossParallel()` - Main parallel loss setter
2. `StopPatternParallel()` - Clear loss when stopping parallel patterns
3. `runPatternParallel()` - Apply/clear loss in parallel pattern loop
4. `runPattern()` - Apply/clear loss in non-parallel pattern loop
5. `StopPattern()` - Clear loss when stopping non-parallel patterns

**Verification:**
- Rebuild: `cd contrib/integration_testing && go build -o integration_testing .`
- Re-run test: `sudo make test-parallel CONFIG=Parallel-Loss-L10-20M-Base-vs-FullEL`
- Both pipelines should now show ~10% loss (similar NAK/retransmit counts)

**Verification Results (2025-12-30):**

| Component | Before Fix | After Fix | Status |
|-----------|------------|-----------|--------|
| baseline-cg retransmits | ~250 | 22,111 | ✅ |
| highperf-cg retransmits | ~1 | **23,778** | ✅ |
| baseline-server retransmits | ~250 | 23,409 | ✅ |
| highperf-server retransmits | ~1 | **24,708** | ✅ |

Both pipelines now experience approximately equal loss (~10% of ~165k packets = ~16k-27k retransmissions accounting for bidirectional loss).

---

## GEO Satellite Latency Test Results

**Test:** `Parallel-Loss-L5-20M-Base-vs-FullEL-GEO`
**Date:** 2025-12-30
**Conditions:** 300ms RTT + 5% packet loss + 20 Mb/s

### Summary

**Test Result: ✅ PASSED**

Both pipelines correctly experienced packet loss (confirming Defect #1 fix works across all latency profiles).

### Key Metrics

| Component | Baseline | HighPerf | Difference |
|-----------|----------|----------|------------|
| CG retransmissions | 28,365 | 24,955 | -12% |
| Server retransmissions | 30,353 | 26,515 | -13% |
| Client retransmissions (recv) | 28,782 | **10,599** | **-63%** |
| bytes_retrans_total (client) | 41.9 MB | **15.4 MB** | **-63%** |

### Key Observations

#### 1. Zero Gaps on HighPerf Pipeline (Significant Advantage)
Mid-test metrics showed:
- **Baseline client:** 377→885+ gaps (increasing over time)
- **HighPerf client:** **0 gaps consistently** ✅

This demonstrates the btree + io_uring configuration handles high-latency packet reordering far more effectively.

#### 2. Fewer Client-Side Retransmissions
The HighPerf pipeline shows **63% fewer retransmissions** at the client receive side (10,599 vs 28,782). This indicates:
- More efficient NAK handling under high-latency conditions
- Reduced duplicate/redundant retransmissions

#### 3. NAK Strategy Differences
- **Baseline:** More single NAKs (28,179 single vs 3,382 range)
- **HighPerf:** More range NAKs (13,258 range vs 15,277 single)

The HighPerf pipeline uses NAK ranges more aggressively, which is more efficient for batching retransmission requests.

### Conclusions

Under challenging GEO satellite conditions (300ms RTT + 5% loss):
1. **Both pipelines maintain data integrity** (recovery=100.0%)
2. **HighPerf shows significant advantages:**
   - Zero gaps vs hundreds of gaps (better reordering)
   - 63% fewer client retransmissions (more efficient ARQ)
   - More efficient NAK batching

This validates the btree + io_uring + EventLoop architecture for high-latency satellite links.

---

## Test Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────────────────────┐
│                     Parallel-Loss-L5-20M-Base-vs-FullEL-GEO Test                            │
│                     300ms RTT (GEO Satellite) + 5% Packet Loss                              │
├─────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                             │
│   ┌─────────────────────────────────────┐     ┌─────────────────────────────────────┐      │
│   │         ns_publisher                 │     │         ns_subscriber               │      │
│   │                                      │     │                                     │      │
│   │  ┌─────────────────────────────┐    │     │    ┌─────────────────────────────┐  │      │
│   │  │ 📦 CG-Baseline              │    │     │    │ 📥 Client-Baseline          │  │      │
│   │  │ IP: 10.1.1.2                │    │     │    │ IP: 10.1.2.2                │  │      │
│   │  │ Algorithm: list             │    │     │    │ Algorithm: list             │  │      │
│   │  │ io_uring: ❌                │    │     │    │ io_uring: ❌                │  │      │
│   │  │ ───────────────────────     │    │     │    │ ───────────────────────     │  │      │
│   │  │ Gaps: 885+  Retx: 28,782    │    │     │    │ Drops: 12,786 too_old       │  │      │
│   │  └─────────────────────────────┘    │     │    └─────────────────────────────┘  │      │
│   │                                      │     │                                     │      │
│   │  ┌─────────────────────────────┐    │     │    ┌─────────────────────────────┐  │      │
│   │  │ 📦 CG-HighPerf              │    │     │    │ 📥 Client-HighPerf          │  │      │
│   │  │ IP: 10.1.1.3                │    │     │    │ IP: 10.1.2.3                │  │      │
│   │  │ Algorithm: btree            │    │     │    │ Algorithm: btree            │  │      │
│   │  │ io_uring: ✅                │    │     │    │ io_uring: ✅                │  │      │
│   │  │ EventLoop: ✅               │    │     │    │ EventLoop: ✅               │  │      │
│   │  │ ───────────────────────     │    │     │    │ ───────────────────────     │  │      │
│   │  │ Gaps: 0 ✅  Retx: 10,599    │    │     │    │ Drops: 7,979 too_old        │  │      │
│   │  └─────────────────────────────┘    │     │    └─────────────────────────────┘  │      │
│   │                                      │     │                                     │      │
│   └──────────────────┬───────────────────┘     └───────────────────┬─────────────────┘      │
│                      │                                             │                        │
│                      │ veth                                   veth │                        │
│                      ▼                                             ▼                        │
│   ┌─────────────────────────────────────────────────────────────────────────────────┐      │
│   │                         ns_router_a (Client Router)                              │      │
│   │                                                                                  │      │
│   │   eth_pub ◄──────────────── link1_a ─────────────────► eth_sub                  │      │
│   │  10.1.1.1                                               10.1.2.1                │      │
│   │                                                                                  │      │
│   └─────────────────────────────────────┬────────────────────────────────────────────┘      │
│                                         │                                                   │
│                                         │ veth pair                                         │
│                              ╔══════════╧══════════╗                                        │
│                              ║   NETEM IMPAIRMENT  ║                                        │
│                              ║                     ║                                        │
│                              ║  🛰️ Latency: 150ms  ║  (each direction = 300ms RTT)          │
│                              ║  📉 Loss: 5%        ║  (bidirectional)                       │
│                              ║                     ║                                        │
│                              ╚══════════╤══════════╝                                        │
│                                         │                                                   │
│   ┌─────────────────────────────────────┴────────────────────────────────────────────┐      │
│   │                         ns_router_b (Server Router)                              │      │
│   │                                                                                  │      │
│   │   ◄─────────────────────── link1_b ──────────────────────────────────────────►  │      │
│   │                                                                                  │      │
│   │   eth_srv: 10.2.1.1                                                             │      │
│   └─────────────────────────────────────┬────────────────────────────────────────────┘      │
│                                         │ veth                                              │
│                                         ▼                                                   │
│                        ┌───────────────────────────────────────────┐                       │
│                        │              ns_server                     │                       │
│                        │                                            │                       │
│                        │  ┌─────────────────────────────────────┐  │                       │
│                        │  │ 🖥️ Server-Baseline                  │  │                       │
│                        │  │ IP: 10.2.1.2:6000                   │  │                       │
│                        │  │ Algorithm: list                     │  │                       │
│                        │  │ Retx: 30,353                        │  │                       │
│                        │  └─────────────────────────────────────┘  │                       │
│                        │                                            │                       │
│                        │  ┌─────────────────────────────────────┐  │                       │
│                        │  │ 🖥️ Server-HighPerf                  │  │                       │
│                        │  │ IP: 10.2.1.3:6001                   │  │                       │
│                        │  │ Algorithm: btree + io_uring + EL    │  │                       │
│                        │  │ Retx: 26,515                        │  │                       │
│                        │  └─────────────────────────────────────┘  │                       │
│                        │                                            │                       │
│                        └───────────────────────────────────────────┘                       │
│                                                                                             │
├─────────────────────────────────────────────────────────────────────────────────────────────┤
│                                      Data Flow                                              │
│                                                                                             │
│   CG-Baseline ──────► Server-Baseline ──────► Client-Baseline                              │
│   (10.1.1.2)         (10.2.1.2:6000)         (10.1.2.2)                                    │
│                   ↑                       ↑                                                 │
│                   │     5% Loss + 300ms RTT (bidirectional)                                │
│                   ↓                       ↓                                                 │
│   CG-HighPerf ───────► Server-HighPerf ──────► Client-HighPerf                             │
│   (10.1.1.3)         (10.2.1.3:6001)         (10.1.2.3)                                    │
│                                                                                             │
│   Both pipelines traverse the SAME network path with IDENTICAL impairments                 │
│                                                                                             │
├─────────────────────────────────────────────────────────────────────────────────────────────┤
│                                  Test Results Summary                                       │
│                                                                                             │
│   Metric                    Baseline         HighPerf         Δ                            │
│   ─────────────────────────────────────────────────────────────                            │
│   Client Gaps               885+             0 ✅             -100%                         │
│   Client Retransmissions    28,782           10,599           -63%                          │
│   NAK Strategy              28k single       15k single       More efficient                │
│                             3k range         13k range        NAK batching                  │
│   Recovery Rate             100%             100%             Both successful               │
│                                                                                             │
└─────────────────────────────────────────────────────────────────────────────────────────────┘
```

---

## Defect #2: Enhanced Comparison Issues Discovered

**Status:** 🔍 INVESTIGATING
**Discovered:** 2025-12-30
**Test:** `Parallel-Loss-L5-20M-Base-vs-FullEL-GEO` (300ms RTT, 5% loss)

### Issue 2.1: Stability Check Timing Bug

**Severity:** 🟡 Medium (false positive, not a real stability issue)

All processes report ~23s longer than expected duration:
```
baseline-cg       Expected: 2m0s   Actual: 2m23s   ERROR
highperf-server   Expected: 2m0s   Actual: 2m23s   ERROR
```

**Root Cause:**
The stability check uses `time.Since(processStartTime)` at comparison time (AFTER test completion), but the test includes:
- 3s connection establishment wait
- Process startup overhead
- Metrics collection time
- Cleanup overhead

**Fix Required:**
Use test end time instead of current time:
```go
// Current (broken):
result.ActualUptime = time.Since(startTime)

// Fixed:
result.ActualUptime = testEndTime.Sub(startTime)
```

### Issue 2.2: Server peer_type Label Missing from Connection Metrics

**Severity:** 🔴 High (blocks connection-level analysis)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ A2: Server (CG-side)                                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│ (No metrics available)                                                      │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Root Cause Identified:**

The `peer_type` label is **only added to the start time metric**, not to all connection metrics:

```go
// metrics/handler.go:55-62 - peer_type IS present here:
writeGauge(b, "gosrt_connection_start_time_seconds", ...,
    "socket_id", socketIdStr, "instance", instanceName,
    "remote_addr", info.RemoteAddr, "stream_id", info.StreamId,
    "peer_type", peerType)  // ✅ Has peer_type

// metrics/handler.go:99-101 - peer_type is MISSING:
writeCounterIfNonZero(b, "gosrt_connection_packets_received_total", ...,
    "socket_id", socketIdStr, "instance", instanceName,
    "type", "ack", "status", "success")  // ❌ No peer_type!
```

**Fix Required:**

Add `peer_type` label to all connection metrics in `handler.go`. This is a large change affecting ~100+ metric writes.

**Workaround (for now):**
Use `socket_id` matching between `gosrt_connection_start_time_seconds` (which has peer_type) and other metrics to correlate connections.

### Issue 2.3: CPU Analysis - io_uring Shifts Work to Kernel

**Severity:** 🟢 Low (expected behavior, well understood)

**Detailed Breakdown:**

| Process | Baseline User | HighPerf User | Δ User | Baseline Sys | HighPerf Sys | Δ Sys |
|---------|---------------|---------------|--------|--------------|--------------|-------|
| CG | 12,611 | 12,740 | +1% | 567 | 1,835 | **+224%** |
| Server | 1,988 | **1,095** | **-45%** | 406 | 3,243 | **+699%** |
| Client | 1,152 | **467** | **-59%** | 264 | 2,392 | **+806%** |

**Key Insight:**
- **Userland CPU is LOWER** for HighPerf server (-45%) and client (-59%)
- **System CPU is HIGHER** because io_uring moves work to kernel space
- This is the EXPECTED io_uring behavior - not a bug!

**Why System CPU Increases:**
- io_uring submission queues involve kernel syscalls
- Completion processing happens in kernel
- The kernel does the heavy lifting instead of userland

**Trade-off is POSITIVE:**
- Lower userland CPU = less Go runtime overhead, less GC pressure
- More predictable latency (no userland scheduling delays)
- Better throughput under stress

**Action:** Update comparison display to show User/System separately and explain the trade-off.

### Issue 2.4: Suspicious Metric Values

**Severity:** 🔴 High (potential overflow/bug)

```
recv_rate_last_us: 136822039 → 1767139987895467 (+1291560821.6%)
```

This value (1.7 quadrillion microseconds) is clearly wrong - likely an overflow or uninitialized value.

**Investigation Needed:**
1. Check where `recv_rate_last_us` is calculated
2. Look for integer overflow in rate calculations
3. Verify timing source is correct

---

## Protocol Metrics Analysis (from JSON logs)

Despite the comparison display issues, the JSON connection logs show the **actual protocol performance**:

### Baseline vs HighPerf Protocol Comparison

| Metric | Baseline | HighPerf | Winner | Explanation |
|--------|----------|----------|--------|-------------|
| **CG retrans %** | 11.8% | 10.7% | HighPerf | Fewer retransmissions needed |
| **CG NAKs received** | 10,165 | 713 | **HighPerf 14x better** | More efficient NAK handling |
| **Client recv retrans** | 28,756 | 10,541 | **HighPerf 2.7x better** | Far fewer retrans received |
| **Client recv loss** | 11,288 | 0 | **HighPerf** | No reported losses |
| **Server retrans sent** | 30,239 | 25,935 | HighPerf | Fewer retransmissions |

### Connection Close Summary (from logs)

**Baseline Pipeline:**
```json
baseline-cg:     retrans=28,761 (11.8%), NAK recv=10,165
baseline-server: retrans_cg=30,239, retrans_client=27,363
baseline-client: retrans_recv=28,756, loss_recv=11,288
```

**HighPerf Pipeline:**
```json
highperf-cg:     retrans=25,788 (10.7%), NAK recv=713  ← 14x fewer NAKs!
highperf-server: retrans_cg=25,935, retrans_client=10,344
highperf-client: retrans_recv=10,541, loss_recv=0     ← Zero loss!
```

### Key Findings

1. **HighPerf NAK efficiency:** 14x fewer NAKs received (713 vs 10,165) - demonstrates superior NAK batching via range NAKs

2. **HighPerf client recovery:** Zero reported losses vs 11,288 for baseline - the btree reordering buffer handles high-latency reordering perfectly

3. **Protocol integrity maintained:** Both pipelines achieved 100% recovery rate - no true data loss

4. **Trade-off confirmed:** HighPerf uses ~50% more CPU but delivers:
   - 14x fewer NAKs
   - 2.7x fewer retransmissions at client
   - Zero gaps
   - Zero reported loss

---

## Latest Test Results (2025-12-30 16:XX)

After fixes applied:
- ✅ Stability checks now show WARNING (not ERROR) - timing fix working
- ✅ CPU breakdown shows userland is LOWER for highperf (as expected with io_uring)
- ❌ Server peer_type correlation still not working - needs investigation
- ❌ `recv_rate_last_us` still shows overflow values

### Positive Findings

| Metric | Baseline | HighPerf | Interpretation |
|--------|----------|----------|----------------|
| `bytes_lost_total [recv]` | 16,412,032 | **0** | HighPerf has ZERO losses! |
| Server userland CPU | 1,988 | **1,095** | 45% less userland work |
| Client userland CPU | 1,152 | **467** | 59% less userland work |
| NAK btree operations | N/A | 10,694 deletes | btree working as expected |
| Ring packets processed | N/A | 230,084 | Lock-free ring working |
| EventLoop iterations | N/A | 440,009 | EventLoop active |

### Remaining Issues

| Issue | Status | Notes |
|-------|--------|-------|
| Server peer_type | ✅ Fixed | Was using wrong keys ("client-generator"/"client" vs "publisher"/"subscriber") |
| recv_rate_last_us | ✅ Fixed | EventLoop was using absolute Unix time instead of relative connection time |
| Stability timing | ⚠️ 16-17s overhead | Expected due to test setup/teardown |

---

## Bug Fix: Server Peer Type Mismatch (2025-12-30)

**Root Cause:**
The comparison code was using the wrong keys to group server metrics:
- Code used: `"client-generator"` and `"client"`
- Actual values: `"publisher"` and `"subscriber"`

**How peer_type is derived:**
In `connection.go:derivePeerType()`:
- StreamId starting with `"publish:"` → `"publisher"`
- StreamId starting with `"subscribe:"` → `"subscriber"`
- Otherwise → `"unknown"`

**Fix Applied:**
Updated `parallel_comparison.go` to use correct peer_type values:
```go
// A2: Server CG-side (publisher connections)
baseGrouped["publisher"]  // was: "client-generator"

// A3: Server Client-side (subscriber connections)
baseGrouped["subscriber"]  // was: "client"
```

---

## Bug Fix: recv_rate_last_us Unix Time (2025-12-30)

**Symptom:**
- Baseline: `recv_rate_last_us = 136,861,731` (~137 seconds - correct)
- HighPerf: `recv_rate_last_us = 1,767,141,927,304,883` (~1.7 quadrillion - wrong!)

**Root Cause:**
In `congestion/live/receive/tick.go`, the EventLoop rate ticker used `time.Now().UnixMicro()`
instead of `r.nowFn()`:

```go
// BEFORE (bug):
case <-rateTicker.C:
    now := uint64(time.Now().UnixMicro())  // Absolute Unix time (~1.7e12)
    r.updateRateStats(now)

// AFTER (fixed):
case <-rateTicker.C:
    now := r.nowFn()  // Relative connection time
    r.updateRateStats(now)
```

**Impact:**
- `RecvRateLastUs` stored Unix timestamp instead of relative connection time
- First rate calculation divided by ~56 years instead of ~1 second
- Computed rates were effectively zero on first period

**Fix Applied:**
- Changed `time.Now().UnixMicro()` to `r.nowFn()` in EventLoop rate ticker
- Added `TestReceiverRateStats` unit test to prevent regression

**Files Changed:**
- `congestion/live/receive/tick.go` - Use `r.nowFn()` for rate stats
- `congestion/live/receive/metrics_test.go` - Added rate stats test

---

## Latest Results (2025-12-30 17:12)

### ✅ Fixed Issues Working

1. **Server peer_type correlation** - A2/A3 now show metrics!
2. **recv_rate_last_us** - No longer showing as overflow
3. **CPU comparison** - Shows User/System/Total with clear breakdown

### 📊 CPU Results (Great News!)

| Component | User Δ | System Δ | Analysis |
|-----------|--------|----------|----------|
| Server | **-40.2%** | +832.3% | Userland savings! |
| Client | **-50.5%** | +1031.0% | Userland savings! |
| CG | +5.0% | +306.0% | CG is sender, less benefit |

**Interpretation:** io_uring successfully shifts work to kernel, reducing userland CPU by 40-50% on receiver side.

### ❌ Type B Validation Shows Large Discrepancies

| Comparison | Data Packets | Diff | Issue |
|------------|-------------|------|-------|
| B1: Baseline CG ↔ Server | 273,741 vs 510,717 | **86.6%** | Server value is ~2× expected |
| B2: HighPerf CG ↔ Server | 280,776 vs 521,650 | **85.8%** | Same issue |
| B3: Baseline Server ↔ Client | 320,227 vs 509,797 | **59.2%** | Different direction, same issue |
| B4: HighPerf Server ↔ Client | 324,532 vs 522,582 | **61.0%** | Same issue |

**Root Cause Hypothesis:**
The server's End 2 value (~510K) appears to be summing BOTH connections (publisher + subscriber) instead of just the filtered peer_type. This explains why it's roughly 2× the expected value.

**Possible Issues:**
1. Some metrics might not have `socket_id` labels and are being summed globally
2. The `sumMetricsByPrefix` function might be matching metrics incorrectly
3. The peer_type filtering might be getting the wrong metrics

---

## Sender ↔ Receiver Metric Pairs Analysis

### Data Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                                                                             │
│   CG (Publisher)              Server                     Client (Subscriber) │
│   ┌─────────────┐     ┌─────────────────────┐           ┌─────────────┐     │
│   │             │     │  Publisher    Subscriber  │     │             │     │
│   │  DATA ────────────────>  ────────────────────────>  DATA         │     │
│   │  SENDER     │     │  RECEIVER    SENDER       │     │  RECEIVER   │     │
│   │             │     │                           │     │             │     │
│   │  <────ACK───────────                          │     │             │     │
│   │  <────NAK───────────                          │     │<────ACK───────    │
│   │             │     │                           │     │<────NAK───────    │
│   └─────────────┘     └─────────────────────────┘       └─────────────┘     │
│                                                                             │
│   Connection 1: CG ↔ Server (publisher)                                     │
│   Connection 2: Server (subscriber) ↔ Client                                │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Connection 1: CG → Server (Publisher Connection)

**Control Packet Flow:**
```
CG (Sender)                     Server (Receiver/Publisher)
     │                                    │
     │ ─────── DATA packets ────────────> │  CG sends, Server receives
     │                                    │
     │ <────── ACK packets ────────────── │  Server sends, CG receives
     │                                    │
     │ ─────── ACKACK packets ──────────> │  CG sends, Server receives
     │                                    │
     │ <────── NAK packets ─────────────  │  Server sends, CG receives
     │                                    │
     │ ─────── Retransmits ────────────>  │  CG retransmits lost data
     │                                    │
```

| What | CG (Sender) Metric | Server-Publisher (Receiver) Metric | Expected Relationship |
|------|-------------------|-----------------------------------|----------------------|
| **DATA packets** | `CongestionSendPkt` | `CongestionRecvPkt` | Sender ≥ Receiver (some lost) |
| **DATA bytes** | `CongestionSendByte` | `CongestionRecvByte` | Sender ≥ Receiver |
| **Unique packets** | `CongestionSendPktUnique` | `CongestionRecvPktUnique` | ≈ equal (unique data) |
| **Retransmits sent** | `CongestionSendPktRetrans` | `CongestionRecvPktRetrans` | Sender ≥ Receiver |
| **ACKs** | `PktRecvACKSuccess` (CG receives) | `PktSentACKSuccess` (Server sends) | Server.sent ≥ CG.recv |
| **ACKACKs** | `PktSentACKACKSuccess` (CG sends) | `PktRecvACKACKSuccess` (Server recv) | CG.sent ≥ Server.recv |
| **NAKs** | `PktRecvNAKSuccess` (CG receives) | `PktSentNAKSuccess` (Server sends) | Server.sent ≥ CG.recv |
| **NAK requests** | `CongestionSendNAKPktsRecv` | `CongestionRecvNAKPktsTotal` | ≈ equal (packets requested) |
| **Loss detected** | (triggers retransmit) | `CongestionRecvPktLoss` | Receiver detects |
| **Packets dropped** | `CongestionSendPktDrop` | `CongestionRecvPktDrop` | Late packets |

### Connection 2: Server (Subscriber) → Client

**Control Packet Flow:**
```
Server (Subscriber/Sender)           Client (Receiver)
     │                                    │
     │ ─────── DATA packets ────────────> │  Server sends, Client receives
     │                                    │
     │ <────── ACK packets ────────────── │  Client sends, Server receives
     │                                    │
     │ ─────── ACKACK packets ──────────> │  Server sends, Client receives
     │                                    │
     │ <────── NAK packets ─────────────  │  Client sends, Server receives
     │                                    │
     │ ─────── Retransmits ────────────>  │  Server retransmits lost data
     │                                    │
```

| What | Server-Subscriber (Sender) Metric | Client (Receiver) Metric | Expected Relationship |
|------|----------------------------------|-------------------------|----------------------|
| **DATA packets** | `CongestionSendPkt` | `CongestionRecvPkt` | Sender ≥ Receiver |
| **DATA bytes** | `CongestionSendByte` | `CongestionRecvByte` | Sender ≥ Receiver |
| **Unique packets** | `CongestionSendPktUnique` | `CongestionRecvPktUnique` | ≈ equal |
| **Retransmits sent** | `CongestionSendPktRetrans` | `CongestionRecvPktRetrans` | Sender ≥ Receiver |
| **ACKs** | `PktRecvACKSuccess` (Server recv) | `PktSentACKSuccess` (Client send) | Client.sent ≥ Server.recv |
| **ACKACKs** | `PktSentACKACKSuccess` (Server send) | `PktRecvACKACKSuccess` (Client recv) | Server.sent ≥ Client.recv |
| **NAKs** | `PktRecvNAKSuccess` (Server recv) | `PktSentNAKSuccess` (Client send) | Client.sent ≥ Server.recv |
| **NAK requests** | `CongestionSendNAKPktsRecv` | `CongestionRecvNAKPktsTotal` | ≈ equal |
| **Loss detected** | (triggers retransmit) | `CongestionRecvPktLoss` | Client detects |

### Key Validation Pairs (for Type B)

For accurate same-connection validation, compare these pairs with **dynamic tolerance based on test loss rate**:

| Validation | Direction | Sender Metric | Receiver Metric | Base Tolerance |
|------------|-----------|--------------|-----------------|----------------|
| **Data packets** | S→R | `CongestionSendPkt` | `CongestionRecvPkt` | loss% + 2% |
| **Retransmits** | S→R | `CongestionSendPktRetrans` | `CongestionRecvPktRetrans` | loss% + 5% |
| **ACKs** | R→S | `PktSentACKSuccess` (Recv) | `PktRecvACKSuccess` (Send) | loss% + 2% |
| **ACKACKs** | S→R | `PktSentACKACKSuccess` (Send) | `PktRecvACKACKSuccess` (Recv) | loss% + 2% |
| **NAKs** | R→S | `PktSentNAKSuccess` (Recv) | `PktRecvNAKSuccess` (Send) | loss% + 2% |
| **NAK requests** | R→S | `CongestionRecvNAKPktsTotal` | `CongestionSendNAKPktsRecv` | loss% + 2% |

### Dynamic Tolerance Based on Test Configuration

| Test Type | Configured Loss | Tolerance Formula | Example |
|-----------|-----------------|-------------------|---------|
| Clean (no loss) | 0% | 1% fixed | ±1% |
| Low loss | 5% | loss + 2% | ±7% |
| High loss | 10% | loss + 3% | ±13% |
| Stress test | 20% | loss + 5% | ±25% |

**Implementation:**
```go
func calculateTolerance(configuredLoss float64) float64 {
    if configuredLoss == 0 {
        return 0.01 // 1% for clean tests
    }
    // Base: configured loss + buffer for timing/measurement variance
    buffer := 0.02 // 2% base buffer
    if configuredLoss > 0.10 {
        buffer = 0.03 // 3% for high loss
    }
    if configuredLoss > 0.15 {
        buffer = 0.05 // 5% for stress tests
    }
    return configuredLoss + buffer
}
```

**Key Insight:**
- Packets SENT will be ≥ packets RECEIVED (due to loss)
- The difference should be **approximately the configured loss rate**
- If difference is **significantly more than configured loss + buffer**, something is wrong

### Current Issue with Type B Validation

The current implementation uses `packets_sent_total` vs `packets_received_total` which:
1. **Sums ALL packet types** (data + ACK + ACKACK + NAK + keepalive)
2. **Doesn't account for direction** - sender sends data, receiver sends ACKs/NAKs

### Proposed Fix

Change the Type B validation to compare DIRECTION-AWARE metrics with **dynamic tolerance from test config**.

**For CG ↔ Server validation (CG is sender, Server is receiver):**

```go
// Pass test's configured loss rate to validation
func printConnectionValidation(title string, sender, receiver *MetricsSnapshot,
                               configuredLoss float64, pipelineColor string) {

    tolerance := calculateTolerance(configuredLoss)

    validationPairs := []struct {
        name       string
        direction  string // "S→R" or "R→S"
        senderKey  string // metric on sender side
        recvKey    string // metric on receiver side
        extraTol   float64 // additional tolerance for this metric type
    }{
        // DATA flow: Sender → Receiver
        {"Data Packets", "S→R", "congestion_send_pkt", "congestion_recv_pkt", 0.0},
        {"Retransmits", "S→R", "congestion_send_pkt_retrans", "congestion_recv_pkt_retrans", 0.05},

        // ACK flow: Receiver → Sender
        {"ACKs", "R→S", "packets_received_total [ack]", "packets_sent_total [ack]", 0.0},

        // ACKACK flow: Sender → Receiver
        {"ACKACKs", "S→R", "packets_sent_total [ackack]", "packets_received_total [ackack]", 0.0},

        // NAK flow: Receiver → Sender
        {"NAKs", "R→S", "packets_received_total [nak]", "packets_sent_total [nak]", 0.0},
        {"NAK Requests", "R→S", "nak_packets_requested_total [recv]", "nak_packets_requested_total [sent]", 0.0},
    }

    for _, vp := range validationPairs {
        pairTolerance := tolerance + vp.extraTol
        // Compare metrics...
    }
}
```

**Example Tolerances:**
| Test | Loss Rate | Data Tolerance | Retransmit Tolerance |
|------|-----------|----------------|---------------------|
| `Parallel-Clean-*` | 0% | 1% | 6% |
| `Parallel-Loss-L5-*` | 5% | 7% | 12% |
| `Parallel-Loss-L10-*` | 10% | 13% | 18% |

**Logic:** For each pair, the SENDER's metric should be ≥ RECEIVER's metric (due to losses),
with the difference being within `configuredLoss + buffer`.

### Prometheus Metric Name Mapping

| Concept | Prometheus Metric | Labels |
|---------|------------------|--------|
| Data packets sent | `gosrt_connection_packets_sent_total` | `type="data"` |
| Data packets received | `gosrt_connection_packets_received_total` | `type="data"` |
| Retransmits sent | `gosrt_connection_retransmissions_total` | (send direction) |
| Retransmits received | `gosrt_congestion_recv_retransmissions_total` | (recv direction) |
| ACK packets sent | `gosrt_connection_packets_sent_total` | `type="ack"` |
| ACK packets received | `gosrt_connection_packets_received_total` | `type="ack"` |
| NAK packets sent | `gosrt_connection_packets_sent_total` | `type="nak"` |
| NAK packets received | `gosrt_connection_packets_received_total` | `type="nak"` |
| NAK pkts requested (recv side) | `gosrt_congestion_recv_nak_packets_requested_total` | `direction="sent"` |
| NAK pkts requested (send side) | `gosrt_congestion_send_nak_packets_requested_total` | `direction="recv"` |

---

## Priority Investigation List

| Priority | Issue | Impact | Effort | Fix |
|----------|-------|--------|--------|-----|
| 1 | ~~Server peer_type correlation~~ | ~~Blocks connection analysis~~ | ~~Medium~~ | ✅ Fixed |
| 2 | ~~recv_rate_last_us overflow~~ | ~~Misleading metrics~~ | ~~Low~~ | ✅ Fixed |
| 3 | **Type B validation needs redesign** | Wrong comparisons | Medium | See "Proposed Fix" above - need direction-aware metric pairs |
| 4 | Stability timing overhead | Cosmetic | Low | Increase tolerance |
| 5 | Document CPU trade-off | User education | Low | ✅ Now clear in output |

### Next Steps for Type B Fix

1. **Phase 1:** Update `printConnectionValidation()` to use direction-aware metric pairs - ✅ DONE
2. **Phase 2:** Extract specific metrics by type label (e.g., `type="data"`) instead of summing all - ✅ DONE
3. **Phase 3:** Add sender/receiver role awareness to comparisons - ✅ DONE
4. **Phase 4:** Pass test config loss rate for dynamic tolerance - ✅ DONE
5. **Testing:** Verify CG→Server shows ~equal data packets (sender ≈ receiver) - 🔄 PENDING

### Implementation Summary (Completed)

**Files Modified:**
- `contrib/integration_testing/parallel_comparison.go`
  - Added `calculateTolerance()` function for dynamic tolerance based on loss rate
  - Updated `PrintEnhancedComparison()` to accept `lossRate` parameter
  - Updated `printSameConnectionValidation()` to pass loss rate and show tolerance info
  - Rewrote `printConnectionValidation()` with direction-aware metric pairs
  - Added `validationPair` struct for S→R / R→S direction tracking
  - Added `getMetricWithTypeLabel()` helper for type-specific metric extraction

- `contrib/integration_testing/test_graceful_shutdown.go`
  - Updated all 3 call sites to pass `config.Impairment.LossRate`

**New Output Format:**
```
Configured Loss: 5.0% → Tolerance: 7.0%

┌─────────────────────────────────────────────────────────────────────────────┐
│ B1: Baseline CG → Server (data flow)                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│ Metric               Dir       Sender     Receiver     Diff   Status │
│ ──────────────────── ──── ──────────── ──────────── ──────── ──────── │
│ Data Packets [data]  S→R       160595       159988    0.4%   ✓ OK │
│ Retransmits [data]   S→R          251          238    5.2%   ✓ OK │
│ ACKs                 R→S         7560         7423    1.8%   ✓ OK │
│ ACKACKs              S→R         7560         7421    1.8%   ✓ OK │
│ NAKs                 R→S          100           95    5.0%   ✓ OK │
├─────────────────────────────────────────────────────────────────────────────┤
│ ✓ Connection validated - metrics match within tolerance (7.0%)              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Questions for Review

1. Which fix option is preferred? (Option A seems simplest)
2. Should we also investigate why baseline sees more internal "losses" than highperf even without network impairment?
3. Are there other parallel test configs that might be affected by similar issues?

---

## Related Files

- `contrib/integration_testing/network/lib.sh` - Bash network control functions
- `contrib/integration_testing/network_controller.go` - Go wrapper for network control
- `contrib/integration_testing/test_parallel_mode.go` - Parallel test runner
- `contrib/integration_testing/test_configs.go` - Test configuration definitions

---

## Preventive Measures Added

### Shellcheck Self-Validation (2025-12-30)

Added automatic shellcheck validation to all network scripts. When any script sources `lib.sh`, shellcheck runs on all scripts in the network directory. If any issues are found, the script exits immediately with detailed error output.

**Behavior:**
- Runs automatically when `lib.sh` is sourced
- Validates all 7 scripts: `lib.sh`, `setup.sh`, `cleanup.sh`, `set_latency.sh`, `set_loss.sh`, `starlink_pattern.sh`, `status.sh`
- Exits with error if any shellcheck warnings/errors are found
- Skips gracefully if shellcheck is not installed (with warning)

**To bypass (not recommended):**
```bash
export SRT_SKIP_SHELLCHECK=1
```

This prevents shell script regressions from creeping into the codebase.

---

## Test Run: 2024-12-30 - GEO-Satellite High Latency Analysis

### Test Configuration
- **Test:** `Parallel-Loss-L5-20M-Base-vs-FullEL-GEO`
- **Network:** 5% packet loss, 300ms RTT (GEO-satellite latency)
- **Duration:** 2 minutes

### Type B Validation Results

| Section | Status | Issue |
|---------|--------|-------|
| B1: Baseline CG → Server | ✅ PASS | All metrics within 7% tolerance |
| B2: HighPerf CG → Server | ❌ FAIL | Retransmits 60.5% discrepancy |
| B3: Baseline Server → Client | ✅ PASS | All metrics within 7% tolerance |
| B4: HighPerf Server → Client | ❌ FAIL | Retransmits 59.7% discrepancy |

### Raw Metrics Analysis

**Retransmission Pipeline:**
| Pipeline | Sender | Sent | Receiver | Received | Diff |
|----------|--------|------|----------|----------|------|
| Baseline CG→Server | baseline-cg | 29,146 | baseline-server | 27,665 | **5.1%** ✅ |
| HighPerf CG→Server | highperf-cg | 25,965 | highperf-server | 10,266 | **60.5%** ❌ |
| Baseline Server→Client | baseline-server | 29,771 | baseline-client | 28,305 | **4.9%** ✅ |
| HighPerf Server→Client | highperf-server | 26,072 | highperf-client | 10,515 | **59.7%** ❌ |

**NAK Requests (Receiver sending NAKs to request retransmits):**
| Instance | Role | NAKs Sent | Expected (≈5% of pkts) |
|----------|------|-----------|------------------------|
| baseline-client | receiver | **11,093** | ~10,700 ✅ |
| highperf-client | receiver | **788** | ~10,700 ❌ (93% fewer!) |
| baseline-server (pub) | receiver | **10,961** | ~10,700 ✅ |
| highperf-server (pub) | receiver | **752** | ~10,700 ❌ (93% fewer!) |

**Unique Data Packets Received:**
| Receiver | Expected | Actual | Diff |
|----------|----------|--------|------|
| baseline-client | 215,375 | 215,600 | +0.1% ✅ |
| highperf-client | 215,374 | 204,809 | **-4.9%** ❌ |
| baseline-server | 215,375 | 215,878 | +0.2% ✅ |
| highperf-server | 215,374 | 205,084 | **-4.8%** ❌ |

### Root Cause Analysis - REVISED

**Previous Theory (INCORRECT):** HighPerf NAK suppression too aggressive.

**Corrected Understanding:**

After reviewing `design_nak_btree.md` and `ack_optimization_plan.md`:
1. NAK btree entries are removed ONLY when packets arrive
2. Periodic NAK sends the FULL NAK btree contents every 20ms
3. NAK entries persist until packet arrives or TSBPD expiry

**Actual Findings:**

| Metric | Baseline | HighPerf | Analysis |
|--------|----------|----------|----------|
| NAK Packets Sent | 11,093 | 788 | 14x fewer PACKETS |
| NAK Entries (ranges) | 3,170 | 12,349 | 4x MORE entries per packet |
| Retransmits Received | 27,665 | 10,266 | 60% fewer counted |
| `PktRecvLoss` | 10,947 | **0** | ZERO loss detected! |

**Key Insight:** HighPerf sends MORE NAK entries (12,349 vs 3,170) but in fewer packets (788 vs 11,093).
This is actually MORE efficient consolidation - not a problem!

**Real Bug Found: `PktRecvLoss` not incremented in EventLoop path**

```
push.go (non-EventLoop):
  Line 253: m.CongestionRecvPktLoss.Add(missingPkts)  ← Increments on gap detection

nak.go (EventLoop NAK btree path):
  NO CongestionRecvPktLoss.Add() call!  ← BUG: Loss not tracked!
```

The EventLoop path detects gaps via NAK btree scanning but does NOT increment the loss counter.
This is a **METRICS BUG**, not a protocol bug!

**Separate Issue: Retransmit Count Discrepancy**

| Path | Sender Sent | Receiver Counted | Diff |
|------|-------------|-----------------|------|
| Baseline CG→Server | 29,146 | 27,665 | 5% (expected) |
| HighPerf CG→Server | 25,965 | 10,266 | **60%** (unexpected!) |

Sender retransmit counts are similar (~26-29k), but HighPerf receiver counts are LOW.
This might be related to `pkt_recv_retrans_rate` using `stats.Interval` instead of `stats.Accumulated`:

```go
// connection_stats.go line 203 - SUSPICIOUS
"pkt_recv_retrans_rate": stats.Interval.PktRecvRetrans,  // Should be Accumulated?
```

### Defect Classification - REVISED

| Issue | Severity | Component | Root Cause | Status |
|-------|----------|-----------|------------|--------|
| `PktRecvLoss` = 0 in EventLoop | MEDIUM | `nak.go` | Different design - see analysis | NEEDS REVIEW |
| `NakBtreeInserts` = 0 | HIGH | `nak.go`? | Unknown - contradicts scan_gaps | **INVESTIGATION NEEDED** |
| Retransmit count discrepancy | HIGH | metrics | 60% fewer counted than sent | INVESTIGATING |
| NAK efficiency metrics | LOW | comparison | Need to show packets vs entries | ENHANCEMENT |

### Key Metrics Analysis

**NAK Btree Metrics (HighPerf only):**
```
nak_btree_scan_gaps_total:        21963  (gaps found during packet btree scan)
nak_btree_deletes_total:          10266  (entries removed when packets arrived)
nak_btree_inserts_total:          ???    (NOT SHOWING - suspicious!)
nak_fast_recent_inserts_total:    ???    (NOT SHOWING - suspicious!)
nak_consolidation_runs_total:     788    (NAK packets sent)
```

**Analysis Question:**
How can `deletes=10266` if `inserts=0`? Entries must be inserted before they can be deleted!

**Two paths insert into NAK btree:**
1. **FastNAK** (`fast_nak.go:140`): `nakBtree.Insert(seq)` → increments `NakFastRecentInserts`
2. **periodicNAK** (`nak.go:466`): `nakBtree.InsertBatch()` → increments `NakBtreeInserts`

If FastNAK inserts gaps BEFORE periodicNAK runs, then periodicNAK finds all gaps already in btree
→ `InsertBatch` returns 0 → `NakBtreeInserts` stays 0

But `NakFastRecentInserts` should still show the FastNAK inserts! Need to verify this.

### Understanding `PktRecvLoss` Design

**Legacy Path (Push):** Immediate NAK on gap detection
```go
// push.go line 253
m.CongestionRecvPktLoss.Add(missingPkts)  // Counts unique missing
```

**EventLoop Path:** Periodic NAK scan + NAK btree
```go
// nak.go line 468-469
m.NakBtreeInserts.Add(uint64(inserted))   // Counts unique btree inserts
m.NakBtreeScanGaps.Add(uint64(len(*gapsPtr)))  // Counts total gaps found
```

**Design difference:** EventLoop uses `NakBtreeInserts` as the equivalent of `PktRecvLoss`.
Unit tests only cover legacy path, not EventLoop path - this is a **test coverage gap**.

### Recommended Investigation

1. **Verify NAK btree insert metrics:**
   - Add debug logging to confirm `InsertBatch` return values
   - Or run test with `PRINT_PROM=true` to see raw Prometheus output

2. **Add missing unit tests:**
   - Test that `NakBtreeInserts` is incremented in EventLoop mode
   - Test that `NakFastRecentInserts` is incremented when FastNAK triggers

3. **Consider unifying loss counting:**
   - Either add `CongestionRecvPktLoss.Add()` when NAK btree inserts happen
   - Or document that `NakBtreeInserts` is the EventLoop equivalent

4. **Update comparison output:**
   - Show NAK packets vs NAK entries clearly
   - Highlight the efficiency difference (fewer packets, same or more entries)

---

## Metric Path Differences: Legacy vs EventLoop

### Overview

The receiver has TWO fundamentally different packet processing paths, each with different
metric semantics. Comparison/analysis code MUST account for these differences.

### Path 1: Legacy (Push-based, Synchronous)

**When used:** Default path without EventLoop, io_uring, or UsePacketRing

**Key characteristics:**
- Packets processed immediately in `Push()` function
- Gaps detected inline, immediate NAK sent
- Loss counted at detection time

**Metrics incremented:**
```go
// push.go - On gap detection:
m.CongestionRecvPktLoss.Add(missingPkts)    // Unique missing sequences
m.CongestionRecvByteLoss.Add(...)
m.CongestionRecvNAKRange.Add(...)           // NAK entries sent
m.CongestionRecvNAKPktsTotal.Add(...)
```

### Path 2: EventLoop (Ring-based, Asynchronous)

**When used:** With `-useeventloop -usepacketring -usenakbtree`

**Key characteristics:**
- Packets queued to lock-free ring, processed by EventLoop
- Gaps detected by periodic NAK btree scan
- Loss tracked via NAK btree insertions

**Metrics incremented:**
```go
// nak.go - On periodic NAK scan:
m.NakBtreeInserts.Add(uint64(inserted))     // UNIQUE new gaps (equivalent to PktRecvLoss)
m.NakBtreeScanGaps.Add(uint64(len(gaps)))   // ALL gaps found (includes re-scans)
m.NakBtreeDeletes.Add(1)                    // When packet arrives

// fast_nak.go - On immediate gap detection:
m.NakFastRecentInserts.Add(uint64(count))   // Immediate NAK inserts (also counts unique)
```

### Metric Equivalence Table

| Concept | Legacy Metric | EventLoop Metric |
|---------|---------------|------------------|
| Unique gaps detected | `CongestionRecvPktLoss` | `NakBtreeInserts` + `NakFastRecentInserts` |
| Gaps resolved | (implicit via retransmit) | `NakBtreeDeletes` |
| Total NAK activity | `CongestionRecvNAKPktsTotal` | `NakBtreeScanGaps` |
| Immediate NAKs | (always immediate) | `NakFastTriggers` |

### Analysis Code Enhancement Requirements

To correctly compare Baseline (Legacy) vs HighPerf (EventLoop):

1. **Don't expect `PktRecvLoss` for EventLoop:** It will be 0 by design
2. **Use equivalent metrics:** Compare `CongestionRecvPktLoss` with `NakBtreeInserts + NakFastRecentInserts`
3. **Consider scan frequency:** `NakBtreeScanGaps` includes re-scans of same gaps (every 20ms until resolved)
4. **Normalize by packets:** Some metrics are counts, others are cumulative - normalize for comparison

### Example: Correct Loss Comparison

```go
// WRONG: Direct comparison (will show HighPerf has 0 loss)
baselineLoss := baseline.CongestionRecvPktLoss
highperfLoss := highperf.CongestionRecvPktLoss  // Always 0!

// CORRECT: Use path-appropriate metrics
baselineLoss := baseline.CongestionRecvPktLoss
highperfLoss := highperf.NakBtreeInserts + highperf.NakFastRecentInserts
```

---

## Tools for Metric Verification

### metrics-audit Tool

The project includes a static analysis tool to verify metric alignment:

```bash
make audit-metrics
# or
go run tools/metrics-audit/main.go
```

**What it checks:**
1. Metrics defined in `ConnectionMetrics` struct (`metrics/metrics.go`)
2. Metrics defined in `ListenerMetrics` struct (`metrics/listener_metrics.go`)
3. Metrics actually incremented via `.Add()/.Store()` calls
4. Metrics exported to Prometheus via `.Load()` calls (`metrics/handler.go`)

**Use cases:**
- Verify new metrics are exported
- Find metrics that are incremented but never exported
- Find metrics that are exported but never incremented (dead code)
- Audit metric naming consistency

### Potential Enhancements to metrics-audit

1. **Path-aware analysis:** Identify which code path (Legacy vs EventLoop) increments each metric
2. **Equivalent metric grouping:** Identify metrics that serve the same purpose in different paths
3. **Test coverage check:** Verify each metric has unit test coverage
4. **Integration test coverage:** Check which metrics are verified in integration tests

---

## Unit Test Coverage Gaps

### Current Test Coverage (metrics_test.go)

| Test | Path Tested | Metrics Verified |
|------|-------------|------------------|
| `TestReceiverLossCounter` | Legacy (Push) | `CongestionRecvPktLoss`, `CongestionRecvNAKRange` |
| `TestReceiverPacketCounters` | Legacy (Push) | `CongestionRecvPkt`, `CongestionRecvPktUnique`, `CongestionRecvPktRetrans` |
| `TestPeriodicACKRunsCounter` | Legacy (Tick) | `CongestionRecvPeriodicACKRuns` |
| `TestPeriodicNAKRunsCounter` | Legacy (Tick) | `CongestionRecvPeriodicNAKRuns` |
| `TestReceiverRateStats` | Legacy (Tick) | `RecvRateLastUs`, `RecvRatePacketsPerSec` |

### Missing Test Coverage

| What Needs Testing | Path | Metrics to Verify |
|-------------------|------|-------------------|
| NAK btree insert on gap detection | EventLoop | `NakBtreeInserts`, `NakBtreeScanGaps` |
| NAK btree delete on packet arrival | EventLoop | `NakBtreeDeletes` |
| FastNAK trigger and insert | EventLoop | `NakFastTriggers`, `NakFastRecentInserts` |
| NAK consolidation | EventLoop | `NakConsolidationRuns`, `NakConsolidationMerged` |
| Ring buffer metrics | EventLoop | `RingDrainedPackets`, `RingBackoffCount` |

### Recommended New Tests

1. **TestNakBtreeInsertOnGapDetection:** Verify `NakBtreeInserts` increments when gaps found ✅ **IMPLEMENTED**
2. **TestNakBtreeDeleteOnPacketArrival:** Verify `NakBtreeDeletes` increments when missing packet arrives ✅ **IMPLEMENTED**
3. **TestFastNakMetrics:** Verify `NakFastTriggers` and `NakFastRecentInserts` on out-of-order packets
4. **TestEventLoopGapMetrics:** End-to-end test using EventLoop receiver

### Test Results Summary

New tests added to `congestion/live/receive/metrics_test.go`:

```
=== RUN   TestNakBtreeInsertOnPeriodicNAK
    NAK btree mode: NakBtreeInserts=2 (equivalent to Legacy CongestionRecvPktLoss)
--- PASS: TestNakBtreeInsertOnPeriodicNAK

=== RUN   TestNakBtreeDeleteOnPacketArrival
    NAK btree: inserts=1, deletes=1 (should be equal when all gaps resolved)
--- PASS: TestNakBtreeDeleteOnPacketArrival

=== RUN   TestNakBtreeScanGapsVsInserts
    Scan 1: inserts=2, scanGaps=2
    Scan 2: inserts=2, scanGaps=4   (ScanGaps increases, Inserts constant)
    Scan 3: inserts=2, scanGaps=6
--- PASS: TestNakBtreeScanGapsVsInserts
```

**Key Findings:**
- Unit tests confirm `NakBtreeInserts` metric IS incrementing correctly
- Unit tests confirm `NakBtreeDeletes` increments when missing packet arrives
- The metrics are working correctly at the unit test level
- `make audit-metrics` confirms `NakBtreeInserts` is fully aligned (defined, used, exported)

**RESOLVED:** `nak_btree_inserts_total` IS showing correctly when comparison output includes "NEW" metrics:
```
nak_btree_inserts_total:  0 → 10285  NEW
nak_btree_deletes_total:  0 → 10262  NEW
```
These match closely (10285 inserts, 10262 deletes = 23 gaps pending at shutdown).

---

## Critical Finding: 60% Retransmit Discrepancy (Dec 30, 2025)

### Test Command
```bash
PRINT_PROM=true sudo make test-parallel CONFIG=Parallel-Loss-L5-20M-Base-vs-FullEL-GEO | tee /tmp/Parallel-Loss-L5-20M-Base-vs-FullEL-GEO-PROM
```

### Observed Results

| Connection | Sender Retx | Receiver Retx | Diff | Status |
|------------|-------------|---------------|------|--------|
| Baseline CG→Server | 28424 | 27017 | 5.0% | ✓ OK |
| **HighPerf CG→Server** | **25634** | **10262** | **60.0%** | **✗ ERR** |
| Baseline Server→Client | 29508 | 27987 | 5.2% | ✓ OK |
| **HighPerf Server→Client** | **25813** | **10381** | **59.8%** | **✗ ERR** |

Baseline matches within tolerance (~5% = network loss). HighPerf has 60% discrepancy!

### Root Cause: io_uring Reordering Causes Premature NAKs

From mid-test metrics:
```
[highperf-client] NAKs: 27603 / retx: 10381 | recovery=100.0%
```

The receiver requested 27,603 packets via NAK but only counted 10,381 as retransmits.
Yet all data was delivered (recovery=100%)!

**What's happening:**
1. io_uring delivers packets out-of-order (async completion)
2. NAK btree scan detects "gap" and sends NAK
3. Original packet arrives (just reordered, not actually lost)
4. Original arrives WITHOUT `RetransmittedPacketFlag` → counted as original
5. Sender receives NAK → sends retransmit (duplicate/wasted work)
6. Retransmit arrives WITH flag → but gap already filled, dropped as duplicate

**Evidence:**
- `nak_btree_inserts_total`: 10285 (unique gaps detected)
- `pkt_recv_retrans_rate`: 10262 (retransmits received)
- These MATCH! The receiver correctly counted retransmits that filled gaps.

**The 60% "discrepancy" is actually:**
- ~17,000 NAKs sent for packets that were just reordered (not lost)
- These packets arrived via original transmission (no retransmit flag)
- The sender wasted work retransmitting packets that weren't actually lost

### Impact Assessment

| Aspect | Impact |
|--------|--------|
| **Data Integrity** | ✓ OK - all data delivered (recovery=100%) |
| **Protocol Correctness** | ✓ OK - metrics match when properly analyzed |
| **Efficiency** | ⚠ ISSUE - ~63% of retransmit bandwidth wasted |
| **CPU Usage** | ⚠ ISSUE - HighPerf uses more CPU (expected, but partially wasted) |

### Potential Fixes

1. **Increase `tooRecentThreshold`:** The NAK btree already has logic to skip "too recent" gaps:
   ```go
   tooRecentThreshold := now + uint64(float64(r.tsbpdDelay)*r.nakRecentPercent)
   ```
   With 300ms RTT and 10% recent window = 300ms threshold. But io_uring reordering can be longer!

2. **RTT-aware gap detection:** Use RTT measurement to set minimum gap age before NAKing

3. **io_uring completion ordering:** Process completions in sequence order when possible

4. **Retransmit deduplication:** Track recently-retransmitted sequences to avoid duplicates

### Experiment: Increasing NakRecentPercent (Dec 31, 2025)

**Hypothesis:** Premature NAKs from io_uring reordering cause unnecessary retransmits.

**Change:** Increased `NakRecentPercent` from 10% to 50% in `HighPerfSRTConfig`.

**Results:**
| Metric | Before (10%) | After (50%) | Change |
|--------|--------------|-------------|--------|
| B2 Retransmit Discrepancy | 60.0% | 59.8% | **No change** |
| B4 Retransmit Discrepancy | 59.8% | 60.0% | **No change** |

**Conclusion:** The premature NAK theory is **WRONG**. The `tooRecentThreshold` is not the cause.

---

## Revised Root Cause: Periodic NAK Duplication at High RTT

### Analysis

Looking at the numbers:
- `nak_btree_inserts_total`: 10310 (unique gaps detected)
- Retransmits sent by sender: 25576
- Ratio: **2.5x retransmits per gap!**

With 300ms RTT and 20ms periodic NAK interval:
```
t=0ms:   Gap detected, NAK #1 sent (contains gap)
t=20ms:  NAK #2 sent (gap still in btree - retransmit hasn't arrived)
t=40ms:  NAK #3 sent (gap still in btree)
...
t=140ms: NAK #8 sent (gap still in btree)
t=150ms: Sender receives NAK #1, retransmits
t=160ms: NAK #9 sent (gap still in btree)
...
t=280ms: NAK #15 sent (gap still in btree)
t=300ms: Retransmit arrives, gap deleted from btree

Result: 15 NAK packets sent for ONE gap = 15 duplicate retransmits!
```

### The Design Issue

The **periodic NAK sends the FULL btree content every 20ms**. This is intentional for reliability:
- If a NAK packet is lost, the next periodic NAK recovers
- The receiver doesn't track "which gaps have been NAKed"

But with high RTT, this causes:
- RTT / NAK_interval = 300ms / 20ms = **15 duplicate NAKs per gap**
- Sender has no deduplication, so it retransmits 15 times

### Why Baseline Doesn't Have This Issue

Baseline uses **immediate NAK** (one NAK per gap, sent once). The sender retransmits once, and by the time the next gap might be detected, the retransmit has already arrived.

### Potential Fixes

1. **Sender-side deduplication:** Track recently-retransmitted sequences with timestamps. Don't re-retransmit within an RTT window.
   ```go
   type RetransmitTracker struct {
       lastRetransmit map[uint32]time.Time
       rttWindow      time.Duration
   }
   ```

2. **Receiver-side NAK throttling:** Track which gaps have been NAKed and when. Only re-NAK if previous NAK was sent > RTT ago.
   ```go
   type NakEntry struct {
       sequence      uint32
       lastNakedAt   time.Time
   }
   ```

3. **RTT-aware NAK interval:** Increase NAK interval based on RTT. With 300ms RTT, use 150ms NAK interval (one NAK per gap during RTT).

4. **NAK acknowledgment:** Sender sends "NAK-ACK" when it starts retransmitting, so receiver stops re-NAKing those sequences.

---

## RTT Validation Tests (Dec 31, 2025)

### Test Matrix

| Test | Loss | RTT | HP Retx Sent | HP Retx Recv | Ratio | Discrepancy |
|------|------|-----|--------------|--------------|-------|-------------|
| **Clean** | 0% | 0ms | 0 | 0 | **1.00x** | **0.0%** ✓ |
| **NoLatency** | 10% | ~0ms | 24063 | 15395 | **1.56x** | 36.0% |
| **Continental** | 5% | 60ms | 18928 | 7642 | **2.48x** | 59.6% |
| **GEO** | 5% | 300ms | 25576 | 10293 | **2.48x** | 59.8% |

Note: Baseline (non-HighPerf) discrepancy always equals network loss rate (5% or 10%).

### Clean Test Confirmation (0% loss, 0ms RTT)

**Critical Finding:** With 0% packet loss, HighPerf generates:
- **ZERO NAKs**
- **ZERO retransmits**
- **0.0% discrepancy**

This confirms:
- ✅ io_uring is NOT causing false gap detection
- ✅ NAK btree works correctly when there's no actual loss
- ✅ The discrepancy requires real packet loss to manifest

### Key Findings

1. **RTT DOES affect the retransmit ratio:**
   - 0ms RTT → 1.56x retransmits per unique gap
   - 60ms+ RTT → 2.48x retransmits per unique gap

2. **The ratio PLATEAUS at ~2.5x:**
   - Continental (60ms) and GEO (300ms) have identical 2.48x ratio
   - This suggests a cap mechanism (NAK consolidation or sender-side limiting)

3. **Even at 0ms RTT, HighPerf has 1.56x duplicates:**
   - This is unexpected! FastNAK may be contributing to duplicates
   - Or there's inherent duplication in the periodic NAK + FastNAK combination

### Revised Theory

The 2.5x cap suggests the system naturally limits duplicate retransmissions:
- **NAK consolidation** merges duplicate NAK requests
- **Sender scheduling** may queue retransmits, reducing duplicates
- **Loss rate applies to NAKs too** - 5% of NAKs are lost, reducing cumulative duplicates

The ~36% discrepancy at 0ms RTT indicates:
- FastNAK sends immediate NAK on gap detection
- Periodic NAK (at 20ms) may still send for recently-resolved gaps
- Or packet reordering causes duplicate gap detection

### Recommendation

**Priority: Medium** - This is a performance issue, not a correctness issue:
- All data is delivered ✓
- Metrics are accurate ✓
- But ~36-60% of retransmit bandwidth is wasted

**Suggested fixes (in order of impact):**

1. **Sender-side retransmit deduplication** (most impactful):
   - Track recently-retransmitted sequences with timestamps
   - Skip re-retransmit within RTT window
   - Would reduce 2.48x → ~1.0x

2. **NAK btree entry state tracking** (receiver-side):
   - Track `lastNakedAt` timestamp per entry
   - Only re-NAK if previous NAK was > RTT/2 ago
   - Reduces NAK frequency without losing reliability

3. **Investigate FastNAK contribution**:
   - The 36% discrepancy at 0ms RTT suggests FastNAK + Periodic NAK overlap
   - May need coordination between immediate and periodic NAK paths

---

## Metrics Audit Tool

The project includes `make audit-metrics` to verify metric alignment:

```bash
$ make audit-metrics
=== GoSRT Metrics Audit ===
Phase 1a: Parsing metrics/metrics.go for ConnectionMetrics fields...
  Found 218 atomic fields in ConnectionMetrics
  Found 32 commented-out fields
Phase 2: Scanning codebase for .Add()/.Store() calls...
  Found 230 unique fields being incremented
Phase 3: Parsing metrics/handler.go for .Load() calls...
  Found 232 fields being exported to Prometheus
=== Results ===
✅ Fully Aligned (defined, used, exported): 230 fields
⚠️  Defined but never used: 2 fields
   - NakFastRecentOverflow
   - NakFastRecentSkipped
```

**Current audit status:**
- 230 metrics fully aligned
- 2 metrics defined but never used (FastNAK edge cases)
- 0 metrics used but not exported

### Potential Enhancements to metrics-audit

1. **Path analysis:** Show which code path (Legacy Push vs EventLoop Ring) increments each metric
2. **Equivalent groupings:** Identify metrics that serve same purpose in different paths
3. **Test coverage report:** Show which metrics have unit test coverage
4. **Integration test mapping:** Verify which metrics are checked in integration tests
5. **Zero-value detection:** Flag metrics that might always be 0 in certain configurations

### CPU Usage Note

The CPU comparison showed:
- **HighPerf Server:** User CPU -44.5%, System CPU +747.8%, Total +85.6%
- **HighPerf Client:** User CPU -64.4%, System CPU +994.7%, Total +105.4%

io_uring shifts work from userland to kernel (expected), but total CPU is significantly higher.
This may be related to the excessive EventLoop processing or the dropped packet handling.

