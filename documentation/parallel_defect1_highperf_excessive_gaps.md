# Parallel Defect 1: HighPerf Pipeline Detects 11x More Gaps Than Baseline

**Status**: ROOT CAUSE CONFIRMED ✅ — OUT-OF-ORDER DELIVERY PROVEN
**Date**: 2025-12-10 → 2025-12-11
**Test**: `Parallel-Starlink-5Mbps`
**Design Doc**: `design_io_uring_reorder_solutions.md`

## Investigation Progress

| Date | Action | Outcome |
|------|--------|---------|
| 2025-12-10 | Initial parallel test run | HighPerf shows 11x more gaps (7,332 vs 653) |
| 2025-12-10 | Created defect document | Documented observations and hypotheses |
| 2025-12-10 | Designed isolation test plan | 7 tests to isolate single variable changes |
| 2025-12-10 | Implemented isolation framework | `test_isolation_mode.go`, `run_isolation_tests.sh` |
| 2025-12-10 | **Ran all 7 isolation tests** | **ROOT CAUSE: Server io_uring recv** |
| 2025-12-10 | Documented io_uring architecture | See "io_uring Architecture Analysis" section |
| 2025-12-11 | Added sequence logging | `listen:io_uring:completion:seq` topic |
| 2025-12-11 | **Ran debug test** | **OUT-OF-ORDER DELIVERY CONFIRMED** ✅ |

## 🎯 ROOT CAUSE CONFIRMED: OUT-OF-ORDER DELIVERY

**The issue is specifically `io_uring receive` on the SERVER side.**

### Debug Logging Results (2025-12-11)

Sequence number logging was added to confirm out-of-order packet delivery. The results are **definitive**:

```
seq=194811147 reqID=77
seq=194811150 reqID=3776    ← GAP (148, 149 missing)
seq=194811152 reqID=3787    ← GAP (151 missing)
seq=194811146 reqID=4140    ← OUT OF ORDER! (went back 6 packets)
seq=194811148 reqID=4313    ← filling gap
seq=194811149 reqID=4258    ← filling gap
seq=194811151 reqID=4201    ← filling gap
seq=194811153 reqID=4161
...
seq=194811181 reqID=4206
seq=194811122 reqID=4224    ← MAJOR OUT OF ORDER! (went back 59 packets!)
seq=194811124 reqID=4135
seq=194811135 reqID=4257
seq=194811134 reqID=4160    ← backwards
seq=194811133 reqID=4177    ← backwards
seq=194811132 reqID=3989    ← backwards
seq=194811131 reqID=3906    ← backwards
seq=194811130 reqID=4270    ← backwards
```

**Key Observations**:

1. **Packets ARE delivered out of order** — This is now proven, not hypothesized
2. **Small gaps (1-3 packets)** — Common, filled shortly after
3. **Large gaps (50+ packets)** — Occasional, packets arrive very late
4. **Request IDs are NOT sequential** — Shows different io_uring requests completing in arbitrary order
5. **Sequences eventually arrive** — No actual packet loss, just reordering

### Impact Analysis

When packet `seq=194811150` arrives but `seq=194811148` and `seq=194811149` haven't yet:
1. SRT receiver sees a gap of 2 packets
2. NAK is sent requesting seq=148, 149
3. Packets 148, 149 arrive ~10 completions later (already in kernel!)
4. Original sender retransmits 148, 149 (unnecessary)
5. Both the late original AND retransmission arrive → `already_acked` drops

**This explains the 2,476 gaps on a CLEAN NETWORK with 0% packet loss.**

### Isolation Test Results (Clean Network - No Packet Loss!)

| Test | Variable Changed | Control Gaps | Test Gaps | Result |
|------|------------------|--------------|-----------|--------|
| 0: Control | None (sanity check) | 0 | 0 | ✓ |
| 1: CG-IoUringSend | CG: io_uring send | 0 | 0 | ✓ |
| 2: CG-IoUringRecv | CG: io_uring recv | 0 | 0 | ✓ |
| 3: CG-Btree | CG: btree | 0 | 0 | ✓ |
| 4: Server-IoUringSend | Server: io_uring send | 0 | 0 | ✓ |
| **5: Server-IoUringRecv** | **Server: io_uring recv** | **0** | **2,476** | ⚠️ **CAUSE** |
| 6: Server-Btree | Server: btree | 0 | 0 | ✓ |

### Key Findings from Test 5 (Server-IoUringRecv)

```
║ SERVER METRICS                      Control         Test       Diff ║
║ Packets Received                      19530        21970     +12.5% ║
║ Gaps Detected                             0         2476        NEW ║
║ Retrans Received                          0         2500        NEW ║
║ NAKs Sent                                 0          718        NEW ║
║ Drops                                     0         2500        NEW ║
```

**Critical Observation**: These gaps occur on a **CLEAN NETWORK** with:
- 0% packet loss
- 0ms latency
- Direct local network connection

This proves the `io_uring receive path` on the server is delivering packets out-of-order or with timing issues that cause false gap detection.

### What This Means

1. **Not io_uring send** - The async send path works correctly (Test 1, Test 4: 0 gaps)
2. **Not btree** - Both btree tests show 0 gaps (Test 3, Test 6)
3. **Not client-side io_uring recv** - Test 2 shows 0 gaps
4. **ONLY server-side io_uring recv** causes the issue

### Probable Mechanism

The `IoUringReader` on the server likely delivers packet completions in a different order than they were received from the kernel network stack. When packet N+1 is delivered to the application before packet N:

1. Receiver sees sequence gap (expected N, got N+1)
2. Gap counter increments
3. NAK sent requesting packet N
4. Packet N finally delivered → "already_acked" drop
5. Retransmission of N arrives → another "already_acked" drop

## Problem Statement

When running two SRT pipelines in parallel over the same network with identical impairment (Starlink pattern: 60ms 100% loss at 12s, 27s, 42s, 57s), the **HighPerf pipeline (btree + io_uring)** detects approximately **11x more gaps** than the **Baseline pipeline (list + no io_uring)**.

Despite this, both pipelines achieve **100% recovery** - all gaps are eventually filled via retransmission.

## Key Observations

### Summary Metrics from Test Run

| Metric | Baseline | HighPerf | Ratio |
|--------|----------|----------|-------|
| **Client gaps detected** | 653 | 7,332 | **11.2x** |
| **Client retransmissions** | 957 | 7,796 | **8.1x** |
| **CG→Server gaps** | 779 | 6,116 | **7.9x** |
| **CG→Server retrans** | 779 | 6,116 | **7.9x** |
| **Server NAKs sent** | 11 | 1,805 | **164x** |
| **Server drops (already_acked)** | 97+123 | 5,114+6,947 | **55x** |
| **Recovery rate** | 100% | 100% | Same |

### Real-time Output Patterns

During the test, the real-time stats showed a clear pattern:

```
[SUB] 20:44:32.80 | ... |    0.0k ok /     0 gaps /     0 retx | recovery=100.0%  # Baseline
[SUB] 20:44:32.82 | ... |    0.0k ok /    22 gaps /    22 retx | recovery=100.0%  # HighPerf - gaps appear immediately!
```

The HighPerf subscriber starts accumulating gaps almost immediately after data flow begins, while Baseline remains at 0 gaps for the same time period.

### Connection Close Statistics (JSON Output)

**Baseline Client:**
```json
{
  "pkt_recv_data": 57313,
  "pkt_recv_loss": 653,
  "pkt_sent_nak": 10
}
```

**HighPerf Client:**
```json
{
  "pkt_recv_data": 64380,
  "pkt_recv_loss": 7332,
  "pkt_sent_nak": 2504
}
```

### Key Differences

1. **HighPerf receives more total packets** (64,380 vs 57,313) - this includes retransmissions
2. **HighPerf sends 250x more NAKs** (2,504 vs 10)
3. **HighPerf has 55x more "already_acked" drops** - packets arriving after already acknowledged

## Hypotheses (Updated After Isolation Tests)

### Hypothesis 1: io_uring Introduces Timing/Ordering Differences ✅ CONFIRMED

**Theory**: The `io_uring` async I/O path may process packets with slightly different timing or ordering than the synchronous path, causing the receiver to detect more sequence gaps.

**CONFIRMED by isolation tests**: Only `Server-IoUringRecv` test shows gaps. All other io_uring tests (send paths) show 0 gaps.

### Hypothesis 2: btree vs list Packet Store Sensitivity ❌ REJECTED

**Theory**: The btree packet reorder buffer may have different sensitivity to sequence gaps compared to the linked list implementation.

**REJECTED by isolation tests**: Both `CG-Btree` and `Server-Btree` tests show 0 gaps. The btree implementation is NOT the cause.

### Hypothesis 3: Async Receive Path Causes Out-of-Order Delivery ✅ CONFIRMED (ROOT CAUSE)

**Theory**: When `io_uring` is enabled for receiving (`-iouringrecvenabled`), the async completion of read operations may deliver packets to the application layer in a different order than they were received from the kernel.

**CONFIRMED by isolation tests**: `Server-IoUringRecv` test shows 2,476 gaps on a clean network with 0% packet loss. This is the root cause.

**Mechanism** (confirmed):
1. `io_uring` submits multiple read requests
2. Completions are delivered out of order
3. Each "late" packet triggers a gap detection + NAK
4. Original packet arrives → "already_acked" drop

### Hypothesis 4: Publisher Timing Differences ❌ REJECTED

**Theory**: The HighPerf client-generator sends packets with slightly different timing due to io_uring async writes, causing more packets to be in-flight during the 60ms outages.

**REJECTED by isolation tests**: `CG-IoUringSend` test shows 0 gaps. The sender path is not the cause.

## Analysis of Comparison Output Issues

The detailed comparison output shows metrics alternating between `→ 0` and `0 →` patterns:

```
│ ✓packets_lost_total                              779            0    -100.0% │
│ ⚠️packets_lost_total                                0         6116        NEW │
```

This is a **display bug** in the comparison logic - metrics with different socket_ids are being treated as separate metrics rather than aggregated. The socket_id label should be stripped for comparison purposes.

## Proposed Next Steps

### ~~Phase 1: Isolate the Variable~~ ✅ COMPLETED

All 7 isolation tests have been run. Root cause identified: **Server io_uring receive path**.

### Phase 2: Investigate io_uring Receive Path (NEXT)

Now that we know the cause, we need to fix it:

1. **Review `io_uring_reader.go`** - Understand how completions are delivered
2. **Check completion ordering** - Are completions delivered FIFO or out-of-order?
3. **Possible fixes**:
   - Add sequence-aware buffering to reorder packets before delivering to SRT
   - Use `IOSQE_IO_LINK` to enforce ordering (if applicable)
   - Use a single outstanding read at a time (loses performance benefit)
   - Add a small reorder buffer to collect completions before dispatching

### Phase 3: Add Diagnostic Counters (Optional)

For deeper analysis, add counters to measure packet ordering:

1. `CongestionRecvPktOutOfOrder` - Packets received with seq < max_seen_seq (arriving late)
2. `CongestionRecvPktReorderDepth` - How far back the out-of-order packet was

### Phase 4: Fix and Verify

1. Implement fix in `io_uring_reader.go`
2. Re-run `sudo make test-isolation CONFIG=Isolation-Server-IoUringRecv`
3. Verify 0 gaps on clean network
4. Re-run full parallel test to verify fix under network impairment

---

## io_uring Architecture Analysis

### How io_uring Receive Works

The io_uring receive path is implemented in `listen_linux.go` (for server/listener).

#### Initialization (`initializeIoUringRecv`)

1. Creates a ring with 512 entries (configurable via `IoUringRecvRingSize`)
2. Pre-populates ring with `initialPending` read requests (default: 512)
3. Each read request has:
   - Unique `requestID` (atomic counter)
   - Buffer from pool
   - `recvCompletionInfo` stored in map

#### Submission (`submitRecvRequest`)

```go
// Simplified flow:
bufferPtr := recvBufferPool.Get()
requestID := recvRequestID.Add(1)
recvCompletions[requestID] = &recvCompletionInfo{buffer: bufferPtr, ...}
sqe.PrepareRecvMsg(recvRingFd, msg, 0)
sqe.SetData64(requestID)
ring.Submit()
```

**Key insight**: The kernel now has **512 outstanding reads** simultaneously!

#### Completion Handler (`recvCompletionHandler`)

```go
for {
    cqe, compInfo := getRecvCompletion(ctx, ring)  // Polls for completions
    processRecvCompletion(ring, cqe, compInfo)      // Deserialize + route
    pendingResubmits++
    if pendingResubmits >= batchSize {
        submitRecvRequestBatch(pendingResubmits)    // Batch resubmit
    }
}
```

**Critical**: Completions are processed in **CQE arrival order**, NOT packet sequence order!

### The Out-of-Order Problem

```
┌─────────────────────────────────────────────────────────────┐
│                    KERNEL NETWORK STACK                      │
│  Packets arrive in order: seq=100, seq=101, seq=102, seq=103 │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│              io_uring (512 pending recvmsg requests)         │
│                                                              │
│  Request 42 (buffer 42) ← receives seq=100                   │
│  Request 17 (buffer 17) ← receives seq=101                   │
│  Request 89 (buffer 89) ← receives seq=102                   │
│  Request 3  (buffer 3)  ← receives seq=103                   │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼  Completions may arrive out of order!
┌─────────────────────────────────────────────────────────────┐
│             COMPLETION QUEUE (CQE order)                     │
│                                                              │
│  CQE 1: Request 17, buffer 17 → seq=101 ← Delivered FIRST!   │
│  CQE 2: Request 42, buffer 42 → seq=100 ← "Gap" detected!    │
│  CQE 3: Request 3,  buffer 3  → seq=103                      │
│  CQE 4: Request 89, buffer 89 → seq=102 ← Another "gap"      │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                    SRT GAP DETECTION                         │
│                                                              │
│  Sees seq=101, expected seq=100 → GAP! Send NAK for seq=100  │
│  Sees seq=100 → Already sent NAK, packet arrives "late"      │
│  Sees seq=103, expected seq=102 → GAP! Send NAK for seq=102  │
│  Sees seq=102 → Already sent NAK, packet arrives "late"      │
└─────────────────────────────────────────────────────────────┘
```

### Why This Happens

The `recvmsg` operations complete in the order the **kernel's io_uring subsystem** processes them, not necessarily the order packets arrived on the wire. With 512 outstanding requests, there's significant opportunity for reordering:

1. **Kernel scheduling**: Different CPU cores may complete requests in different orders
2. **Memory allocation**: Buffer availability can affect completion timing
3. **io_uring internal batching**: Completions may be batched and reordered

### Evidence from Isolation Tests

On a **clean network** (0% loss, 0ms latency):
- Control pipeline (no io_uring): **0 gaps**
- Test pipeline (io_uring recv): **2,476 gaps**

This proves the reordering is happening in the io_uring path, not the network.

---

## Phase 2a: Debug Logging Plan

### A) Add Sequence Number Logging

**Goal**: Confirm packets are being delivered out-of-order by logging sequence numbers as they arrive.

**Implementation**:

Add logging to `listen_linux.go::processRecvCompletion` after successful packet deserialization:

```go
// After: p, err := packet.NewPacketFromData(addr, bufferSlice)
// Add:
h := p.Header()
if !h.IsControlPacket {
    ln.log("listen:io_uring:completion:seq", func() string {
        return fmt.Sprintf("DATA seq=%d requestID=%d",
            h.PacketSequenceNumber.Val(), cqe.UserData)
    })
}
```

**Log Topic**: `listen:io_uring:completion:seq`
- Hierarchical: subscribing to `listen:io_uring` will also capture these
- Can be filtered specifically for sequence analysis

### B) Test Plan

1. **Add logging topic to server flags** in isolation test:
   ```go
   // Add to GetControlServerFlags/GetTestServerFlags:
   "-logtopics", "listen:io_uring:completion:seq"
   ```

2. **Run short test** (reduce duration to 10s for less output):
   ```bash
   sudo make test-isolation CONFIG=Isolation-Server-IoUringRecv 2>&1 | tee /tmp/seq_debug.log
   ```

3. **Analyze output** - Look for patterns like:
   ```
   seq=100 requestID=1
   seq=102 requestID=3  ← GAP! Expected 101
   seq=101 requestID=2  ← Out of order delivery
   seq=103 requestID=4
   ```

### C) Expected Findings

If our hypothesis is correct, we should see:
- Sequence numbers arriving out of order
- `requestID` values NOT correlating with sequence order
- Gaps of 1-10+ packets (measuring reorder depth)

### D) Implementation Files

| File | Change |
|------|--------|
| `listen_linux.go` | Add sequence logging in `processRecvCompletion` |
| `dial_linux.go` | Add sequence logging in `processRecvCompletion` (if needed for client) |

---

---

## Phase 3: Fix Design Discussion

### The Core Problem

The current SRT NAK logic triggers immediately when a sequence gap is detected:

```
Receive seq=101 → expected seq=100 → GAP! → Send NAK for seq=100
```

This works fine for synchronous receive (packets arrive in order from kernel), but with io_uring's async completions, packets arrive out of order at the application layer.

### Potential Solution: Delayed NAK with Sliding Window

Inspired by [goTrackRTP](https://github.com/randomizedcoder/goTrackRTP), we can use a **sliding window** approach:

#### goTrackRTP Design Overview

```
                     ← Behind Window (bw) →         ← Ahead Window (aw) →
                     ┌───────────────────────────────────────────────────┐
   ...──────────────│    ACCEPTABLE WINDOW    │    Max()    │           │────...
                     └───────────────────────────────────────────────────┘
                     │                        │              │           │
              Packets here are          Max seen        Ahead packets
              "late but OK"             sequence        (shouldn't happen)
                                        number
```

Key concepts:
- **Max()** — Highest sequence number seen (current reference point)
- **Behind Window (bw)** — Packets arriving with seq < Max but within range are acceptable
- **Ahead Window (aw)** — Packets arriving with seq > Max+1 (gaps)
- **btree storage** — Efficient O(log n) insert/lookup for tracking seen sequences

#### Adaptation for goSRT with io_uring

**Proposal**: When io_uring receive is enabled, require btree and delay NAK generation.

```go
// Current behavior (immediate NAK):
if seq > maxSeen+1 {
    NAK(maxSeen+1, seq-1)  // Request all missing packets
    maxSeen = seq
}

// Proposed behavior (delayed NAK with window):
if seq > maxSeen+1 {
    // Don't NAK immediately - packets may be in-flight in io_uring
    maxSeen = seq
}
// Only NAK when packets fall off the "behind window"
if maxSeen - oldestMissing > behindWindow {
    NAK(oldestMissing)     // Only NAK truly late packets
}
```

#### Why This Works

1. **btree naturally sorts** — Even if packets arrive out of order, btree stores them in sequence order
2. **3-second buffer** — SRT already has a large receive buffer; we can afford to wait
3. **behindWindow sizing** — Based on observed reorder depth (50-100 packets), we'd set `bw` accordingly
4. **Only truly lost packets get NAK'd** — If a packet hasn't arrived after `bw` packets pass, it's probably lost

#### Window Sizing for goSRT

From the debug output, we observe:
- Most reordering is within 10 packets
- Occasional reordering up to 60 packets
- No actual packet loss (all packets eventually arrive)

Suggested initial parameters:
| Parameter | Value | Rationale |
|-----------|-------|-----------|
| Behind Window | 100 packets | Covers observed max reorder depth + margin |
| NAK Delay | 50 packets | Conservative - wait for most reordering to settle |

At 5 Mbps with 1316-byte packets:
- Packets per second: ~475
- 100 packets = ~210ms of buffer time
- This is well within the 3-second SRT latency buffer

### Alternative Approaches

| Approach | Pros | Cons |
|----------|------|------|
| **Sliding window NAK delay** | Preserves io_uring perf, clean fix | Requires careful tuning |
| **Reorder buffer before SRT** | Simple, transparent | Extra memory copy, latency |
| **Single outstanding io_uring read** | Simple, preserves ordering | Loses async benefit |
| **IOSQE_IO_LINK ordering** | Kernel-enforced order | May not work for UDP recvmsg |

### Implementation Considerations

1. **Make btree mandatory with io_uring recv** — btree handles sorting automatically
2. **Add `NAKDelayPackets` config option** — Allow tuning the behind window size
3. **Metrics for reorder depth** — Track max/avg reorder to inform tuning
4. **Fallback to immediate NAK** — If non-io_uring receive, keep current behavior

### Questions to Resolve Before Implementation

1. How does the current NAK logic interact with the receive buffer and TSBPD?
2. Should the window be packet-count based or time-based?
3. How do we handle the edge case of connection startup (initial sequence)?
4. Does the btree already provide any reordering benefit we can leverage?

---

## Questions Answered ✅

1. ~~Is the HighPerf pipeline using io_uring for the **receive** path on the client?~~
   **Answer**: Yes, via `-iouringrecvenabled`

2. ~~Are the 7,332 gaps detected all during the 60ms outages, or distributed throughout?~~
   **Answer**: Distributed throughout — gaps occur even on clean network

3. ~~What is the average reorder depth? Are packets arriving 1-2 behind, or much more?~~
   **Answer**: Mostly 1-10 packets, occasionally 50-60 packets behind

4. ~~Does the issue persist without network impairment (clean network test)?~~
   **Answer**: YES — 2,476 gaps on 0% loss network = purely software reordering

## Test Commands for Investigation

### Isolation Tests (Implemented)

A new isolation testing framework has been created to systematically test each variable:

```bash
# List all isolation tests
make test-isolation-list

# Run a single isolation test
sudo make test-isolation CONFIG=Isolation-CG-IoUringSend

# Run all 7 tests (~3.5 min, output captured to /tmp/isolation_tests_XXX/)
sudo make test-isolation-all
```

### Isolation Test Matrix

| Test | Variable Changed | Gaps | Status |
|------|------------------|------|--------|
| `Isolation-Control` | None (sanity check) | 0 | ✅ Pass |
| `Isolation-CG-IoUringSend` | CG: io_uring send | 0 | ✅ Pass |
| `Isolation-CG-IoUringRecv` | CG: io_uring recv | 0 | ✅ Pass |
| `Isolation-CG-Btree` | CG: btree | 0 | ✅ Pass |
| `Isolation-Server-IoUringSend` | Server: io_uring send | 0 | ✅ Pass |
| `Isolation-Server-IoUringRecv` | Server: io_uring recv | **2,476** | ⚠️ **ROOT CAUSE** |
| `Isolation-Server-Btree` | Server: btree | 0 | ✅ Pass |

### Test Architecture

- **Duration**: 30 seconds per test
- **Network**: Clean path (no impairment) - any gaps = code bug
- **Architecture**: CG → Server only (no Client/Subscriber)
- **Control pipeline**: list + no io_uring (baseline)
- **Test pipeline**: exactly ONE variable changed from control

## Related Files

### Parallel Test Infrastructure
- `contrib/integration_testing/test_parallel_mode.go` - Parallel test orchestration
- `contrib/integration_testing/parallel_analysis.go` - Comparison logic

### Isolation Test Infrastructure (NEW)
- `contrib/integration_testing/test_isolation_mode.go` - Simplified CG→Server tests
- `contrib/integration_testing/run_isolation_tests.sh` - Batch runner with output capture
- `contrib/integration_testing/config.go` - `IsolationTestConfig`, helper methods
- `contrib/integration_testing/test_configs.go` - 7 isolation test configurations

### Core SRT Code (Under Investigation)
- `listen_linux.go` - io_uring receive path for listener (SERVER) ← **ROOT CAUSE HERE**
- `dial_linux.go` - io_uring receive path for dialer (CLIENT)
- `congestion/live/receive.go` - Gap detection logic
- `packet/store_btree.go` - B-tree packet store
- `packet/store_list.go` - Linked list packet store

## Appendix: Full Comparison Data

### Client-Generator Comparison
| Metric | Baseline | HighPerf |
|--------|----------|----------|
| packets_sent_total [data] | 57,787 | 63,124 |
| packets_lost_total | 779 | 6,116 |
| retransmissions_total | 779 | 6,116 |
| nak_packets_requested_total (recv) | 779 | 6,116 |

### Server Comparison
| Metric | Baseline | HighPerf |
|--------|----------|----------|
| packets_received_total [data] | 57,227 | 62,452 |
| packets_lost_total (recv) | 569 | 5,404 |
| packets_drop_total (recv) | 219 | 5,444 |
| nak_entries_total (recv) [range] | 956 | 6,929 |

### Client Comparison
| Metric | Baseline | HighPerf |
|--------|----------|----------|
| packets_received_total [data] | 57,313 | 64,380 |
| packets_lost_total (recv) | 653 | 7,332 |
| retransmissions_total (recv) | 957 | 7,796 |
| packets_drop_total (recv) | 305 | 7,372 |
| recv_data_drop_total [already_acked] | 123 | 6,947 |

