# Defect 8: NAK Packet Imbalance and High Unfulfilled Rate

**Status**: 🟢 Fixed (Pending Verification)
**Priority**: High
**Discovered**: 2024-12-10
**Fixed**: 2024-12-10
**Related To**: [Defect 7](integration_testing_with_network_impairment_defects.md) (Range NAKs causing retransmission amplification)

---

## Summary

Running `Network-Loss2pct-5Mbps` with `--verbose` reveals concerning NAK handling anomalies:
1. **2x NAK packet imbalance** between sender and receiver counts
2. **~50% unfulfilled NAK requests**
3. **Low duplicate count** despite over-NAKing

---

## Observed Behavior

### 1. NAK Packet Imbalance (2x ratio)

```
⚠ NAK pkt imbalance: Server sent 28, CG recv 56 (diff: -28)
```

**CG is reporting receiving 2x the NAK packets that Server reports sending.**

This is mathematically impossible in a correct system:
- Server sends NAKs to CG (requesting retransmissions)
- CG should receive exactly as many NAKs as Server sends (minus ~2% network loss)
- But CG reports 2x the count

### 2. High Unfulfilled NAK Requests (~50%)

```
⚠ NAK request gap: requested 60 pkts, sent 32 retrans (unfulfilled: 28)
```

**Only ~50% of NAK-requested packets are being retransmitted.**

Possible causes:
- Packets dropped from sender buffer before retransmission
- NAK processing delay causing timeout
- Bug in NAK handling code path

### 3. Low Duplicate Count Despite Over-NAKing

```
Over-NAK Confirmation: 2 dupes vs 4 over-requested pkts (50% match)
```

**Expected more duplicates based on the over-NAK theory.** If range NAKs request packets that weren't lost, they should arrive as duplicates. But duplicates are low.

---

## Data Summary

| Metric | Expected | Observed | Issue |
|--------|----------|----------|-------|
| NAK imbalance (Server→CG) | ~0 (minus 2% loss) | -28 to -36 per interval | 2x ratio |
| NAK fulfillment rate | ~100% | ~50% | Half unfulfilled |
| Duplicate packets | High (if over-NAKing) | Low (1-7 per interval) | Missing duplicates |
| Recovery rate | ~98% | 95-96% | Slightly low |

### Sample Verbose Output (2-second interval)

```
[Connection1: CG→Server] Delta over 2.0s:
  Sender (CG):
    Packets: +1249 total (+1220 unique, +29 retrans)
    Control: +56 NAKs recv, +153 ACKs recv
    NAK detail: +29 singles, +0 ranges, requesting 29 pkts
  Receiver (Server):
    Packets: +1222 total (+29 retrans, +54 gaps)
    Control: +28 NAKs sent, +0 ACKs sent
    NAK detail: +2 singles, +27 ranges, requesting 56 pkts
    ⚠ DROPS: +2 (too_late: 0, buf_full: 0, dupes: 2)
  Balance Check:
    ⚠ NAK pkt imbalance: Server sent 28, CG recv 56 (diff: -28)
    ✓ Retrans balanced: 29 sent = 29 recv
    ⚠ NAK request gap: requested 56 pkts, sent 29 retrans (unfulfilled: 27)
  NAK Efficiency: 0.52 NAK pkts per gap
  Over-NAK Confirmation: 2 dupes vs 2 over-requested pkts (100% match)
```

---

## Hypotheses

### Hypothesis A: NAK Counting Bug (Most Likely)

The 2x NAK imbalance suggests a counting issue rather than network loss:
- Server might be counting NAK *packets* while CG counts NAK *entries* or *sequence ranges*
- Or vice versa - one side double-counting due to multiple code paths
- The Prometheus metric `packets_sent_total{type="nak"}` vs `packets_received_total{type="nak"}` may have different semantics

**Evidence for**: Consistent 2x ratio across all intervals (not random like network loss)
**Evidence against**: The metrics are clearly named "packets", not "entries"

### Hypothesis B: Processing Bottleneck

The standard Go channel-based path may be too slow:
- NAK packets arrive but can't be processed fast enough
- Some NAKs are silently dropped in channel buffers
- Retransmission requests expire before being fulfilled

**Evidence for**: 50% unfulfilled rate suggests processing delays
**Evidence against**: 5 Mb/s is not high throughput; should be easily handled

### Hypothesis C: Buffer Exhaustion

Sender's retransmission buffer may not hold packets long enough:
- NAK arrives for packet that's already been evicted from send buffer
- `CongestionSendNAKNotFound` should be incrementing but may not be checked

**Evidence for**: "unfulfilled" NAK requests
**Evidence against**: 3000ms latency buffer should be sufficient for 2% loss

### Hypothesis D: Race Condition in NAK Processing

Concurrent NAK handling may cause issues:
- Multiple NAKs for same packet processed simultaneously
- Retransmission sent for first NAK, second NAK finds packet already removed
- Could explain both low duplicates AND high unfulfilled

**Evidence for**: Would explain the strange 2x counting
**Evidence against**: Atomic counters should prevent double-counting

---

## Experimental Plan

### Experiment 1: Run with io_uring and btree

The GoSRT codebase has high-performance paths:
- **io_uring**: Bypasses Go channel overhead for send/receive
- **btree packet store**: Faster lookups than linked list

```bash
# Run same test with io_uring enabled
sudo make test-network CONFIG=Network-Loss2pct-5Mbps-HighPerf VERBOSE=1
or
sudo go run ./contrib/integration_testing network-test Network-Loss2pct-5Mbps-HighPerf --verbose
# Compare metrics between standard and io_uring modes
```

**Rationale**: If processing speed is the issue, io_uring/btree should:
- Reduce unfulfilled NAK rate
- Potentially fix the NAK counting imbalance if it's a race condition
- Improve duplicate detection if packets are being processed faster

**Decision tree**:
- If results **improve significantly** → Processing bottleneck confirmed (Hypothesis B)
- If results **stay the same** → Counting bug or logic issue (Hypothesis A or D)

### Experiment 2: Audit NAK Counting Code Paths

Review all locations where NAK packet/entry counting occurs:
- `connection.go`: `sendNAK()`, `handleNAK()`
- `congestion/live/receive.go`: NAK generation
- `congestion/live/send.go`: NAK reception and retransmission

Look for:
- Double-counting (incrementing in multiple places)
- Different counting semantics (packets vs entries vs sequence numbers)
- Race conditions in atomic counter updates

### Experiment 3: Add CongestionSendNAKNotFound Tracking

Verify the "unfulfilled" NAKs are actually hitting the "not found in buffer" path:
- Check if `CongestionSendNAKNotFound` is being incremented
- Export this metric to Prometheus if not already
- If not incrementing, find where NAK requests are being dropped

### Experiment 4: Packet-Level Trace

Enable detailed logging for one test run:
- Log every NAK packet sent (sequence numbers, entry count)
- Log every NAK packet received (sequence numbers, entry count)
- Log every retransmission attempt (sequence number, success/not-found)

This will definitively show:
- Whether packets are being counted correctly
- Where NAK requests are being lost
- Why duplicates are lower than expected

---

## Files to Investigate

| File | What to Check |
|------|---------------|
| `connection.go` | `sendNAK()` and `handleNAK()` counting logic |
| `congestion/live/send.go` | NAK reception, `CongestionSendNAKNotFound` usage |
| `congestion/live/receive.go` | NAK generation counting |
| `metrics/handler.go` | NAK metric export consistency |

---

## Next Steps (Prioritized)

1. **First**: Run Experiment 1 (io_uring/btree) to see if performance matters
2. **If results differ**: Focus on Hypothesis B (processing bottleneck)
3. **If results same**: Focus on Hypothesis A (counting bug) with code audit
4. **Then**: Add packet-level tracing to understand exact NAK flow

---

## Progress Log

| Date | Action | Result |
|------|--------|--------|
| 2024-12-10 | Initial observation during Network-Loss2pct-5Mbps test | Documented 2x imbalance, 50% unfulfilled |
| 2024-12-10 | Added duplicate tracking to verbose output | Confirmed low duplicates (1-7 per interval) |
| 2024-12-10 | Created focused investigation document | Ready for experiments |
| 2024-12-10 | **Experiment 1 completed** (io_uring + btree) | See results below |
| 2024-12-10 | **Code audit: metrics_and_statistics_design.md** | Confirmed packet classifier design |
| 2024-12-10 | **ROOT CAUSE FOUND** | Two bugs: recv double-count AND io_uring send not tracked |
| 2024-12-10 | **FIX IMPLEMENTED** | Fixed recv path + added IncrementSendControlMetric for io_uring |
| 2024-12-10 | **UNIT TESTS ADDED** | Created packet_classifier_test.go with 12 new tests |

---

## Experiment 1 Results: io_uring + btree (HighPerf)

### Test Configuration
```bash
sudo make test-network CONFIG=Network-Loss2pct-5Mbps-HighPerf VERBOSE=1
```

**Settings**: io_uring send/recv, io_uring client output, btree degree 32, 3000ms latency

### Key Findings

#### 1. NAK Imbalance PERSISTS (2x ratio) ❌

| Interval | Server Sent | CG Recv | Ratio |
|----------|-------------|---------|-------|
| 1 | 41 | 80 | 1.95x |
| 2 | 57 | 114 | 2.00x |
| 3 | 51 | 96 | 1.88x |
| 4 | 41 | 78 | 1.90x |
| 5 | 55 | 108 | 1.96x |
| ... | ... | ... | ~2x consistently |

**Conclusion**: The 2x imbalance is NOT caused by Go channel processing speed.
**Hypothesis B (Processing Bottleneck) is RULED OUT.**

#### 2. Duplicates NOW HIGH with btree ✓

| Test | Duplicates per interval |
|------|------------------------|
| Baseline (list) | 1-7 |
| HighPerf (btree) | **30-75** |

Sample output:
```
Over-NAK Confirmation: 38 dupes vs 1 over-requested pkts (3800% match)
Over-NAK Confirmation: 59 dupes vs 1 over-requested pkts (5900% match)
Over-NAK Confirmation: 69 dupes vs 3 over-requested pkts (2300% match)
```

**Key Insight**: The btree packet store is properly detecting and counting duplicates!
The list-based store was likely silently dropping duplicates without counting them.

#### 3. Unfulfilled Rate Still ~40-50% ❌

```
⚠ NAK request gap: requested 106 pkts, sent 63 retrans (unfulfilled: 43)
⚠ NAK request gap: requested 143 pkts, sent 87 retrans (unfulfilled: 56)
⚠ NAK request gap: requested 118 pkts, sent 68 retrans (unfulfilled: 50)
```

Pattern: `unfulfilled ≈ duplicates` - suggesting the "unfulfilled" packets
actually arrived as duplicates (the original packet wasn't lost, but was
requested by a range NAK anyway).

#### 4. Range NAKs Dominate (>95%)

Server consistently sends:
- 1-5 single NAK entries
- 90-165 range NAK entries

This confirms the immediate NAK behavior (no `LossMaxTTL` implementation)
causes aggressive range NAKs for any out-of-order packets.

### Sample Verbose Output (HighPerf)

```
[Connection1: CG→Server] Delta over 2.0s:
  Sender (CG):
    Packets: +1283 total (+1220 unique, +63 retrans)
    Control: +80 NAKs recv, +162 ACKs recv
    NAK detail: +33 singles, +30 ranges, requesting 63 pkts
  Receiver (Server):
    Packets: +1258 total (+62 retrans, +105 gaps)
    Control: +41 NAKs sent, +0 ACKs sent
    NAK detail: +1 singles, +105 ranges, requesting 106 pkts
    ⚠ DROPS: +38 (too_late: 0, buf_full: 0, dupes: 38)
  Balance Check:
    ⚠ NAK pkt imbalance: Server sent 41, CG recv 80 (diff: -39)
    ⚠ Retrans imbalance: CG sent 63, Server recv 62 (diff: 1)
    ⚠ NAK request gap: requested 106 pkts, sent 63 retrans (unfulfilled: 43)
  NAK Efficiency: 0.39 NAK pkts per gap
  Over-NAK Confirmation: 38 dupes vs 1 over-requested pkts (3800% match)
```

### Comparison: Baseline vs HighPerf

| Metric | Baseline (list) | HighPerf (io_uring+btree) | Conclusion |
|--------|-----------------|---------------------------|------------|
| NAK imbalance | 2x | **2x** | Same - NOT a perf issue |
| Unfulfilled rate | ~50% | **~40-50%** | Same |
| Duplicates | 1-7/interval | **30-75/interval** | btree detects dupes properly! |
| Retrans balance | ±1-2 | **±1-4** | Similar |
| Recovery rate | 95-96% | **94-95%** | Similar |

### Updated Hypothesis Analysis

| Hypothesis | Before Exp 1 | After Exp 1 | Status |
|------------|--------------|-------------|--------|
| A: Counting Bug | Likely | **MOST LIKELY** | Consistent 2x regardless of path |
| B: Processing Bottleneck | Possible | **RULED OUT** | io_uring made no difference |
| C: Buffer Exhaustion | Possible | Less likely | dupes ≈ unfulfilled suggests over-NAKing |
| D: Race Condition | Possible | Less likely | Atomic counters should prevent |

### Root Cause Theory (Updated)

The high duplicate count with btree provides a key clue:

1. Server detects "gap" when out-of-order packet arrives
2. Server sends range NAK requesting multiple packets
3. CG receives NAK, retransmits requested packets
4. Meanwhile, "missing" packets arrive (they were just reordered, not lost)
5. btree correctly detects them as duplicates

The **2x NAK imbalance** is likely a **counting semantic mismatch**:
- One side counts NAK *packets*
- Other side counts NAK *entries* or *sequence numbers in ranges*

### New Sub-Defect: Graceful Shutdown Failure

During this test, the client did not exit after SIGINT. The test hung until
manually interrupted with Ctrl+C:

```
Error: client did not exit within 5 seconds
[PUB] 19:44:35.92 |     0.0 pkt/s ...  (continues for 30+ seconds)
^C
signal: interrupt
```

**Root Cause**: When client-generator is paused (SIGUSR1) but server connection
remains open, the client's SRT connection blocks waiting for data that will
never arrive. The client's SIGINT handler may not be interrupting this blocking read.

**See**: [Defect 8a: Client Shutdown Hangs](#defect-8a-client-shutdown-hangs-during-quiesce)

---

## Defect 8a: Client Shutdown Hangs During Quiesce

**Status**: 🔴 Open
**Priority**: Medium
**Discovered**: 2024-12-10

### Observed Behavior

During the HighPerf test quiesce phase:
1. SIGUSR1 sent to client-generator (pauses data generation) ✓
2. Stabilization timeout occurs (expected when no data flowing) ✓
3. SIGINT sent to client (subscriber) ✓
4. **Client does not exit** - hangs indefinitely ❌

### Expected Behavior

Client should:
1. Receive SIGINT
2. Initiate graceful SRT connection close
3. Exit within 5 seconds

### Likely Cause

The client is blocked in an SRT read that doesn't respect the context
cancellation from the SIGINT handler. The subscriber connection is waiting
for data from the server, but the server-to-subscriber path has no data
(since client-generator is paused).

### Files to Investigate

- `contrib/client/main.go` - SIGINT handling
- `dial.go` or `connection.go` - SRT read blocking behavior
- Need to ensure read operations respect context.Done()

---

## Next Steps (Updated)

1. ~~Run Experiment 1 (io_uring/btree)~~ ✓ COMPLETE
2. ~~**Focus on Hypothesis A**: Audit NAK counting code paths~~ ✓ **ROOT CAUSE FOUND**
3. **Fix Defect 8a**: Ensure client exits on SIGINT during quiesce
4. **Implement Fix**: Remove duplicate NAK counter increments

---

## ROOT CAUSE IDENTIFIED (2024-12-10)

### Bug: Double-Counting NAK Packets

The NAK handlers in `connection.go` have **duplicate counter increments** that conflict
with the packet classifier. All other control packet types (ACK, ACKACK, Keepalive,
Shutdown, KM) correctly defer to the packet classifier, but NAK was incorrectly modified
to increment counters directly.

### Evidence: Comment Comparison

| Control Type | Comment in Handler | Increments Counter? |
|--------------|-------------------|---------------------|
| ACK | "metrics are tracked via packet classifier" | ❌ No (correct) |
| ACKACK | "metrics are tracked via packet classifier" | ❌ No (correct) |
| Keepalive | "metrics are tracked via packet classifier" | ❌ No (correct) |
| Shutdown | "metrics are tracked via packet classifier" | ❌ No (correct) |
| KM | "metrics are tracked via packet classifier" | ❌ No (correct) |
| **NAK** | "control packets **don't** go through packet classifier" | ✅ **Yes (BUG!)** |

### The Buggy Code

**handleNAK (connection.go:1051)** - WRONG:
```go
// Increment NAK received counter (control packets don't go through packet classifier)
if c.metrics != nil {
    c.metrics.PktRecvNAKSuccess.Add(1)  // <-- DUPLICATE INCREMENT!
}
```

**sendNAK (connection.go:1533)** - WRONG:
```go
// Increment NAK sent counter (control packets don't go through packet classifier)
if c.metrics != nil {
    c.metrics.PktSentNAKSuccess.Add(1)  // <-- DUPLICATE INCREMENT!
}
```

### The Packet Classifier Already Handles NAK

**IncrementRecvMetrics (metrics/packet_classifier.go:97-98)**:
```go
case packet.CTRLTYPE_NAK:
    m.PktRecvNAKSuccess.Add(1)  // <-- Already incremented here!
```

**IncrementSendMetrics path (metrics/packet_classifier.go:198-199)**:
```go
case packet.CTRLTYPE_NAK:
    m.PktSentNAKSuccess.Add(1)  // <-- Already incremented here!
```

### The Complete Fix

**Part 1: Receive Path (`handleNAK` in `connection.go`)**

The receive path correctly goes through `IncrementRecvMetrics` in `packet_classifier.go`,
so the duplicate increment in `handleNAK` was causing 2x counting. Fixed by removing it:

```go
// handleNAK - FIXED
// Note: NAK recv metrics are tracked via packet classifier in IncrementRecvMetrics
// The packet classifier is called by listen.go/dial.go when packets are received.
```

**Part 2: Send Path (io_uring) - The Real Bug!**

The io_uring send path in `connection_linux.go` was decommissioning control packets
BEFORE `IncrementSendMetrics` could classify them:

```go
// BEFORE (broken):
if p.Header().IsControlPacket {
    p.Decommission()
    packetForMetrics = nil  // IncrementSendMetrics sees nil, can't classify!
}
```

Fixed by capturing the control type BEFORE decommissioning, then using a new
`IncrementSendControlMetric` helper:

```go
// AFTER (fixed):
if p.Header().IsControlPacket {
    controlType = p.Header().ControlType  // Capture BEFORE decommission
    p.Decommission()
    packetForMetrics = nil
}
// ...
if isControlPacket {
    metrics.IncrementSendControlMetric(c.metrics, controlType)
} else {
    metrics.IncrementSendMetrics(c.metrics, packetForMetrics, ...)
}
```

**Files Changed**:
- `connection.go`: Removed duplicate increment in `handleNAK`, updated comments
- `connection_linux.go`: Capture controlType before decommission, use new helper
- `metrics/packet_classifier.go`: Added `IncrementSendControlMetric` helper function

### Unit Tests Added

Created `metrics/packet_classifier_test.go` with comprehensive tests:

| Test | Purpose |
|------|---------|
| `TestIncrementSendControlMetric` | Verify io_uring helper increments correct counter for each control type |
| `TestIncrementSendControlMetricNilMetrics` | Verify nil safety |
| `TestIncrementRecvMetricsNAK` | Verify receive path counts NAK exactly once |
| `TestIncrementRecvMetricsIoUringPath` | Verify io_uring receive path tracking |
| `TestIncrementSendMetricsNAK` | Verify non-io_uring send path counts NAK |
| `TestIncrementSendMetricsNilPacket` | Documents why `IncrementSendControlMetric` is needed |
| `TestAllControlTypesReceive` | Verify all 6 control types are counted on receive |
| `TestAllControlTypesSend` | Verify all 6 control types are counted on send |
| `TestDataPacketCounting` | Verify data packets are counted correctly |

Run with: `go test ./metrics/ -v -run "TestIncrement|TestAllControl|TestDataPacket"`

### Validation

After the fix, NAK counts should be balanced:
- Server sends N NAK packets → CG receives ~N NAK packets (minus ~2% network loss)
- No more 2x ratio on receive side
- No more 0 on send side (io_uring path now tracked)

---

## Related Documents

- [Integration Testing Defects](integration_testing_with_network_impairment_defects.md) - Parent defect tracker
- [Defect 7: Range NAK Amplification](integration_testing_with_network_impairment_defects.md#defect-7-range-naks-causing-2x-retransmission-amplification)
- [Metrics Analysis Design](metrics_analysis_design.md) - NAK detail counter design

