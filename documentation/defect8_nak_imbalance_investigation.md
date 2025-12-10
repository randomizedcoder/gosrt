# Defect 8: NAK Packet Imbalance and High Unfulfilled Rate

**Status**: ✅ Verified Fixed
**Priority**: High
**Discovered**: 2024-12-10
**Fixed**: 2024-12-10
**Verified**: 2024-12-10
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
- `listen.go`: Fixed connection lookup bug in send path (see Bug 3 below)
- `conn_request.go`: Use closure with direct metrics reference instead of broken lookup

### Bug 3: Listener Send Path - Wrong Lookup Key

**Discovery**: Found during non-io_uring verification testing on 2024-12-10.

#### SRT Socket ID Model

Per the SRT RFC, each end of an SRT connection has its **own unique socket ID**.
These are NOT shared - each peer generates its own ID independently during handshake.

```
   Client (Caller)                    Server (Listener)
   ================                   ==================
   socketId = 0xAABBCCDD              socketId = 0x11223344
   peerSocketId = 0x11223344          peerSocketId = 0xAABBCCDD
```

**Handshake Socket ID Exchange:**

1. **Client sends Induction request:**
   - Client generates its own socket ID (e.g., 0xAABBCCDD)
   - Sends `cif.SRTSocketId = 0xAABBCCDD` to server

2. **Server receives and stores peer ID:**
   - `peerSocketId = cif.SRTSocketId` (0xAABBCCDD)
   - Server generates its OWN socket ID via `generateSocketId()` (e.g., 0x11223344)
   - Stores in `req.socketId`

3. **Server sends Conclusion response:**
   - Sends `req.handshake.SRTSocketId = req.socketId` (0x11223344)
   - Client receives this as server's socket ID

4. **Connection established:**
   - Client stores: `socketId=0xAABBCCDD`, `peerSocketId=0x11223344`
   - Server stores: `socketId=0x11223344`, `peerSocketId=0xAABBCCDD`

**Key insight**: The socket IDs are DIFFERENT on each end. Each end identifies
itself with its own ID, and stores the peer's ID separately.

**SRT Control Packet Header** (RFC Section 3.2):
```
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                     Destination Socket ID                     |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

The `Destination Socket ID` field contains the **recipient's** socket ID - the ID
that the receiving end uses to identify itself. There is no "Source Socket ID"
field in the packet header.

#### How Socket IDs Are Used

**When SENDING a packet:**
- We set `DestinationSocketId = peerSocketId` (the remote end's ID)
- This tells the remote end "this packet is for your connection 0x11223344"

**When RECEIVING a packet:**
- The packet's `DestinationSocketId` contains **OUR** socket ID
- This is how we know which of our connections should receive this packet

#### How ln.conns is Keyed

In `conn_request.go:531`:
```go
req.ln.conns.Store(req.socketId, conn)  // Keyed by LOCAL socket ID
```

The listener's connection map is keyed by the **local socket ID** (the server's
own ID for that connection), NOT the peer's socket ID.

#### The Bug: SEND vs RECEIVE Path

**RECEIVE path (worked correctly):**
```go
// In listen.go receive loop:
val, ok := ln.conns.Load(p.Header().DestinationSocketId)
```
- Packet from peer has `DestinationSocketId = our local ID`
- Lookup by `DestinationSocketId` finds our connection ✓

**SEND path (was broken):**
```go
// In listen.go send():
val, ok := ln.conns.Load(h.DestinationSocketId)  // WRONG!
```
- When we SEND, `DestinationSocketId = peer's socket ID`
- But `ln.conns` is keyed by LOCAL socket ID
- Lookup by `DestinationSocketId` (peer's ID) always fails ✗

#### Example

```
Server sends NAK to Client:
  - Packet header: DestinationSocketId = 0xAABBCCDD (client's ID)
  - Server's ln.conns: { 0x11223344 → *srtConn }  (keyed by server's ID)
  - Lookup: ln.conns.Load(0xAABBCCDD) → NOT FOUND!
  - Result: IncrementSendMetrics never called, NAK not counted
```

#### Why the Receive Path Works

The receive path works because when the CLIENT sends a packet to the SERVER:
- Client sets `DestinationSocketId = 0x11223344` (server's ID)
- Server receives packet, does `ln.conns.Load(0x11223344)` → FOUND!

#### The Fix

Instead of trying to look up the connection by socket ID in the send path,
we create a closure that directly captures the connection's metrics:

```go
// conn_request.go - after connection is created:
conn.onSend = func(p packet.Packet) {
    req.ln.sendWithMetrics(p, conn.metrics)  // Direct reference, no lookup needed
}
```

**Note**: `dial.go` was not affected because the dialer only has one connection
and stores it directly in `dl.conn` (no map lookup required).

#### Why Existing Tests Didn't Catch This Bug

The existing test `TestConnectionMetricsNAKRetransmit` in `connection_metrics_test.go`:
```go
// Line 446-448: Only verifies CLIENT received NAKs
if writerMetrics, ok := connections[writerSocketId]; ok && writerMetrics != nil {
    recvNAK := writerMetrics.PktRecvNAKSuccess.Load()
    require.Greater(t, recvNAK, uint64(0), "Sender should receive NAKs")
}
```

The test drops packets on the CLIENT (dialer) side to trigger NAKs from the SERVER.
But it only checks that the CLIENT received NAKs - it never verifies that the SERVER
tracked **sending** NAKs. Since the dialer's receive path worked correctly, the bug
in the listener's send path went undetected.

#### New Tests Added

Added two new tests in `connection_metrics_test.go`:

1. **`TestListenerSendMetricsNAK`**: Verifies the SERVER (listener) correctly tracks
   `PktSentNAKSuccess` when sending NAKs. This directly tests the bug scenario.

2. **`TestListenerSendMetricsACK`**: Verifies the SERVER (listener) correctly tracks
   `PktSentACKSuccess` when sending ACKs. Tests the same send path for a different
   control packet type.

Both tests specifically target the listener's send path that had the wrong lookup key.

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

## Verification Results (2024-12-10)

### Test: `Network-Loss2pct-1Mbps-HighPerf` (io_uring + btree)

**Command**: `sudo make test-network CONFIG=Network-Loss2pct-1Mbps-HighPerf VERBOSE=1`

**Result**: ✅ NAK counting is now balanced

#### Sample Mid-Test Intervals (After Fix)

| Interval | Server NAKs Sent | CG NAKs Recv | Status |
|----------|------------------|--------------|--------|
| 1 | 11 | 9 | diff: 2 (network loss expected) |
| 2 | 7 | 7 | ✓ balanced |
| 3 | 9 | 9 | ✓ balanced |
| 4 | 13 | 13 | ✓ balanced |
| 5 | 11 | 9 | diff: 2 (network loss expected) |
| 6 | 14 | 14 | ✓ balanced |
| 7 | 10 | 10 | ✓ balanced |
| 8 | 6 | 6 | ✓ balanced |
| 9 | 8 | 8 | ✓ balanced |
| 10 | 17 | 17 | ✓ balanced |
| 11 | 13 | 13 | ✓ balanced |
| 12 | 13 | 12 | diff: 1 (network loss expected) |
| 13 | 17 | 15 | diff: 2 (network loss expected) |
| 14 | 12 | 12 | ✓ balanced |
| 15 | 13 | 13 | ✓ balanced |

**Key Observations**:
1. **2x ratio is eliminated** - Most intervals show exact balance
2. **Small differences (1-2) are expected** - These are NAK packets lost to 2% network impairment
3. **Retransmissions are also balanced** - Most intervals show `Retrans balanced: N sent = N recv`

#### Sample Verbose Output (After Fix)

```
[Connection1: CG→Server] Delta over 2.0s:
  Sender (CG):
    Packets: +257 total (+244 unique, +13 retrans)
    Control: +13 NAKs recv, +152 ACKs recv
    NAK detail: +13 singles, +0 ranges, requesting 13 pkts
  Receiver (Server):
    Packets: +253 total (+13 retrans, +26 gaps)
    Control: +13 NAKs sent, +305 ACKs sent
    NAK detail: +0 singles, +26 ranges, requesting 26 pkts
  Balance Check:
    ✓ NAK pkts balanced: 13 sent = 13 recv        <-- FIXED!
    ✓ Retrans balanced: 13 sent = 13 recv
```

Compare to **before fix** (2x ratio):
```
  Balance Check:
    ⚠ NAK pkt imbalance: Server sent 28, CG recv 56 (diff: -28)   <-- 2x bug
```

### Remaining Issues (Separate Defects)

1. **Defect 8a**: Client does not exit gracefully after SIGINT during quiesce phase
2. **"NAK request gap"**: Expected behavior - Range NAKs over-request due to no `LossMaxTTL` implementation

---

## Final Verification (2024-12-10)

A comprehensive verification was performed:

### 1. Added Error Counter
Added `SendConnLookupNotFound` counter to detect Bug 3 pattern in future.

### 2. Revert Test
Temporarily reverted fix to broken lookup code:
```
Error Analysis: ✗ FAILED
  ✗ server: gosrt_send_conn_lookup_not_found_total increased by 15144

NAK imbalance:
  Connection1: NAKs: sent=0, recv=87
```

### 3. Fix Applied
With closure-based fix:
```
Error Analysis: ✓ PASSED
NAK balance:
  ✓ NAK pkts balanced: 4 sent = 4 recv
```

### Conclusion
- **All three bugs (1, 2, 3) are fixed**
- **Error counters now detect Bug 3 pattern** (15,144 failures caught)
- **NAK balance is restored** for both io_uring and non-io_uring paths

---

## Related Documents

- [Integration Testing Defects](integration_testing_with_network_impairment_defects.md) - Parent defect tracker
- [Defect 7: Range NAK Amplification](integration_testing_with_network_impairment_defects.md#defect-7-range-naks-causing-2x-retransmission-amplification)
- [Metrics Analysis Design](metrics_analysis_design.md) - NAK detail counter design

