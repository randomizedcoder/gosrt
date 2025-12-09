# Integration Testing with Network Impairment - Defects and Improvements

This document tracks defects discovered during network impairment testing and plans for addressing them.

## Overview

The network impairment testing infrastructure uses Linux network namespaces with `tc netem` to simulate packet loss and latency. During initial testing, several issues have been identified.

---

## Defect 1: Statistical Validation Reports 0% Loss Despite Actual Loss Occurring

**Status**: 🟢 Fixed (Pending Verification)
**Priority**: High
**Discovered**: 2024-12-08
**Fixed**: 2024-12-08
**Implementation Tracking**: [defect1_prometheus_metrics_implementation.md](./defect1_prometheus_metrics_implementation.md)

### Symptoms

When running a 2% packet loss test:
```bash
sudo make test-network CONFIG=Network-Loss2pct-5Mbps
```

The test fails with:
```
Statistical Validation: ✗ FAILED
  ✗ LossRate: expected 1.0% - 3.0%, got 0.00
    Observed loss rate 0.00% outside expected range for 2.0% configured loss
```

### Evidence That Loss IS Occurring

The SRT connection statistics clearly show loss IS being detected and handled:

**Client-generator → Server connection (publisher to server):**
```json
{
  "pkt_recv_nak": 364,
  "pkt_retrans_from_nak": 378,
  "pkt_retrans_percent": 1.815213215520553
}
```

**Server → Client-generator connection (server receiving from publisher):**
```json
{
  "pkt_recv_loss": 685,
  "pkt_recv_loss_rate": 2.111216316503314
}
```

This shows:
- 685 packets were lost (detected via sequence number gaps)
- 2.11% loss rate (very close to configured 2%)
- 364 NAKs were sent to request retransmission
- 378 packets were retransmitted
- **SRT ARQ mechanism is working correctly!**

### Root Cause Analysis

After reviewing `metrics_and_statistics_design.md` and `metrics_implementation_progress.md`:

#### The Metrics EXIST But Are Not Exported to Prometheus

The `ConnectionMetrics` struct (`metrics/metrics.go`) has the relevant counters:

```go
// Congestion control - Receiver statistics (lines 140-159)
CongestionRecvPkt        atomic.Uint64  // Total packets received
CongestionRecvPktLoss    atomic.Uint64  // Packets lost (sequence gaps)
CongestionRecvPktRetrans atomic.Uint64  // Retransmitted packets received

// Congestion control - Sender statistics (lines 162-178)
CongestionSendPkt        atomic.Uint64  // Total packets sent
CongestionSendPktRetrans atomic.Uint64  // Packets retransmitted
```

**However**, these counters are **NOT exported** in `metrics/handler.go`!

The handler currently exports:
- ✅ `gosrt_connection_packets_received_total` (basic receive counts)
- ✅ `gosrt_connection_congestion_recv_data_drop_total` (drops after receipt)
- ✅ Various error counters
- ❌ `CongestionRecvPkt` - **NOT EXPORTED**
- ❌ `CongestionSendPkt` - **NOT EXPORTED**
- ❌ `CongestionRecvPktLoss` - **NOT EXPORTED**
- ❌ `CongestionRecvPktRetrans` - **NOT EXPORTED**
- ❌ `CongestionSendPktRetrans` - **NOT EXPORTED**

#### The Analysis Code Looks for Non-Existent Prometheus Metrics

The `analysis.go` code tries to find metrics that don't exist in Prometheus output:
- Looks for `status="retransmit"` in `gosrt_connection_packets_sent_total` - doesn't exist
- Uses `gosrt_connection_congestion_recv_data_drop_total` which is drops, not transit loss

### Plan to Fix

#### Option A: Export Congestion Control Metrics to Prometheus (Recommended)

**Scope**: Modify `metrics/handler.go` only - counters already exist!

The counters already exist in `ConnectionMetrics`. We just need to export them:

```go
// In metrics/handler.go - ADD these exports:

// Congestion control - packets sent/received (for loss calculation)
writeCounterValue(b, "gosrt_connection_congestion_packets_total",
    metrics.CongestionSendPkt.Load(),
    "socket_id", socketIdStr, "direction", "send")
writeCounterValue(b, "gosrt_connection_congestion_packets_total",
    metrics.CongestionRecvPkt.Load(),
    "socket_id", socketIdStr, "direction", "recv")

// Packets lost (detected via sequence number gaps)
writeCounterValue(b, "gosrt_connection_congestion_packets_lost_total",
    metrics.CongestionRecvPktLoss.Load(),
    "socket_id", socketIdStr, "direction", "recv")

// Retransmissions
writeCounterValue(b, "gosrt_connection_congestion_retransmissions_total",
    metrics.CongestionSendPktRetrans.Load(),
    "socket_id", socketIdStr, "direction", "send")
writeCounterValue(b, "gosrt_connection_congestion_retransmissions_total",
    metrics.CongestionRecvPktRetrans.Load(),
    "socket_id", socketIdStr, "direction", "recv")
```

**Pros**:
- Counters already exist and are being updated
- Only handler.go needs modification
- Adds valuable observability for all users

**Cons**:
- Slightly increases /metrics response size

#### Option B: Cross-Endpoint Loss Calculation (Complementary)

**Scope**: Modify `contrib/integration_testing/analysis.go`

Since the integration test has access to metrics from **both ends** of the connection, we can calculate loss by comparing:
- Packets sent by sender (client-generator's `CongestionSendPkt`)
- Packets received by receiver (server's `CongestionRecvPkt`)

```go
// In computeObservedStatistics():

// Cross-endpoint loss calculation (more reliable than single-endpoint detection)
packetsSentBySender := sender.TotalPacketsSent     // From client-generator
packetsReceivedByReceiver := receiver.TotalPacketsRecv  // From server

if packetsSentBySender > 0 {
    // Direct loss calculation: what was sent vs what arrived
    transitLoss := packetsSentBySender - packetsReceivedByReceiver
    if transitLoss > 0 {
        stats.CrossEndpointLossRate = float64(transitLoss) / float64(packetsSentBySender)
    }
}

// Also check the reported loss from receiver's sequence gap detection
reportedLoss := receiver.TotalPacketsLost  // From CongestionRecvPktLoss
if reportedLoss > 0 && packetsSentBySender > 0 {
    stats.ReportedLossRate = float64(reportedLoss) / float64(packetsSentBySender)
}

// Use the higher of the two for validation (defensive)
stats.LossRate = max(stats.CrossEndpointLossRate, stats.ReportedLossRate)
```

**Pros**:
- Double-checks loss from two independent sources
- Cross-endpoint comparison catches losses that sequence gap detection might miss
- More robust validation

**Cons**:
- Requires Option A to be implemented first (to get the metrics)

### Recommended Implementation Plan

**Phase 1: Export Missing Prometheus Metrics**
- File: `metrics/handler.go`
- Add exports for: `CongestionSendPkt`, `CongestionRecvPkt`, `CongestionRecvPktLoss`, `CongestionSendPktRetrans`, `CongestionRecvPktRetrans`
- Estimated effort: 30 minutes

**Phase 2: Update Analysis to Use New Metrics**
- File: `contrib/integration_testing/analysis.go`
- Update `ComputeDerivedMetrics()` to use new Prometheus counters
- Estimated effort: 30 minutes

**Phase 3: Add Cross-Endpoint Validation**
- File: `contrib/integration_testing/analysis.go`
- Implement cross-endpoint loss calculation
- Add validation that compares both methods
- Estimated effort: 1 hour

**Phase 4: Documentation**
- Update `metrics_and_statistics_design.md` to document new exports
- Update `metrics_implementation_progress.md` to track completion

### Files to Modify

| Phase | File | Changes |
|-------|------|---------|
| 1 | `metrics/handler.go` | Export CongestionSendPkt, CongestionRecvPkt, CongestionRecvPktLoss, CongestionSendPktRetrans, CongestionRecvPktRetrans |
| 2 | `contrib/integration_testing/analysis.go` | Use new Prometheus counters in ComputeDerivedMetrics() |
| 3 | `contrib/integration_testing/analysis.go` | Add cross-endpoint loss calculation for double-checking |
| 4 | `documentation/metrics_and_statistics_design.md` | Document new Prometheus exports |

### Validation Approach (Post-Fix)

After implementation, the statistical validation should:

1. **Report loss from sequence gap detection**: Using `gosrt_connection_congestion_packets_lost_total`
2. **Cross-check with endpoint comparison**: Packets sent (sender) vs packets received (receiver)
3. **Verify retransmission activity**: Using `gosrt_connection_congestion_retransmissions_total`
4. **Flag discrepancies**: If methods disagree significantly, report a warning

---

## Defect 2: NAK Counter Not Found in Prometheus Metrics

**Status**: ✅ Fixed (Pending Verification)
**Priority**: High
**Discovered**: 2024-12-08
**Related**: Defect 1
**Implementation**: [defect2_prometheus_metrics_audit_implementation.md](defect2_prometheus_metrics_audit_implementation.md)

### Symptoms

After Phase 2-4 implementation, the test STILL fails:
```
Statistical Validation: ✗ FAILED
  ✗ NAKsPerLostPacket: expected >= 0.50, got 0.00
```

And throughput display still shows `0 retx` despite retransmissions occurring.

### Evidence That NAKs ARE Working (JSON Stats)

```json
// Client-generator received NAKs and retransmitted:
{
  "pkt_recv_nak": 360,
  "pkt_retrans_from_nak": 368,
  "pkt_retrans_percent": 1.77%
}

// Server detected losses:
{
  "pkt_recv_loss": 675,
  "pkt_recv_loss_rate": 2.32%
}
```

**SRT ARQ is working correctly!** The JSON stats prove it.

### Root Cause Analysis (Updated)

**Issue 1: Throughput Display Uses Wrong Counter**

The client-generator throughput display uses:
```go
clientMetrics.CongestionSendPktRetrans.Load()  // Returns 0!
```

But the actual retransmissions are tracked in:
```go
clientMetrics.PktRetransFromNAK.Load()  // Returns 368!
```

**These are DIFFERENT counters!**

**Issue 2: NAK Counters Not Being Incremented**

The Prometheus exports were added (Phase 2), but the underlying counters are NOT being incremented:
- `PktSentNAKSuccess` - NOT incremented when NAKs are sent
- `PktRecvNAKSuccess` - NOT incremented when NAKs are received

The congestion control layer (which handles NAKs) does NOT call `IncrementSendMetrics()`/`IncrementRecvMetrics()` for NAK packets. It updates the SRT `Statistics` struct directly, bypassing `metrics.ConnectionMetrics`.

**Issue 3: CongestionSendPktRetrans vs PktRetransFromNAK**

Two different retransmission counters exist:
| Counter | Location | Populated? | Value |
|---------|----------|------------|-------|
| `CongestionSendPktRetrans` | metrics.ConnectionMetrics | ❌ No | 0 |
| `PktRetransFromNAK` | metrics.ConnectionMetrics | ✅ Yes | 368 |

### Updated Implementation Plan

**Phase 5**: Fix throughput display counters
- Use `PktRetransFromNAK` for sender (not `CongestionSendPktRetrans`)
- Verify receiver counter is correct

**Phase 6**: Increment NAK counters in congestion control
- Find where NAKs are sent in `congestion/live/recv.go`
- Find where NAKs are received in `congestion/live/send.go`
- Add explicit counter increments for `PktSentNAKSuccess` and `PktRecvNAKSuccess`

**Phase 7**: Verify all congestion control counters are populated
- Audit which `Congestion*` counters are actually being incremented

---

## Defect 3: 5% Loss Test Fails Recovery Rate Threshold

**Status**: 🟡 Under Investigation
**Priority**: Medium
**Discovered**: 2024-12-08
**Test**: `Network-Loss5pct-5Mbps`
**Last Updated**: 2024-12-09

### Latest Test Results (After Metrics Audit)

After completing the AST-based metrics audit (all 118 metrics now aligned), the test output shows:

```bash
sudo make test-network CONFIG=Network-Loss5pct-5Mbps
```

**Throughput Display (Now Working!):**
```
[PUB] 09:19:18.29 |   305.0 pkt/s |   19.61 MB |  4.997 Mb/s |   10.0k ok /     0 loss /   959 retx ~= 100.0%
[SUB] 09:19:18.79 |   610.0 pkt/s |   16.33 MB |  4.997 Mb/s |   16.7k ok /     0 loss /     0 retx ~= 100.0%
```

✅ **Improvement**: Retransmissions now visible in throughput display!

**JSON Stats (Connection Closed Events):**

| Component | Metric | Value |
|-----------|--------|-------|
| Client-generator | `pkt_sent_data` | 21,426 |
| Client-generator | `pkt_recv_nak` | 1,810 |
| Client-generator | `pkt_retrans_from_nak` | 980 |
| Client-generator | `pkt_retrans_percent` | 4.57% |
| Server (receiver) | `pkt_recv_data` | 20,509 |
| Server (receiver) | `pkt_recv_loss` | 1,702 |
| Server (receiver) | `pkt_recv_retrans` | 932 |
| Server (receiver) | `pkt_sent_nak` | 905 |
| Server (receiver) | `pkt_recv_drop` | 63 |
| Client (subscriber) | `pkt_recv_data` | 18,614 |

**Analysis Output:**
```
Statistical Validation: ✗ FAILED
  ✗ RecoveryRate: expected >= 95.0%, got 94.92
    Poor loss recovery - too many unrecoverable packets
  Observed Statistics:
    Configured loss: 5.0%, Retrans% of sent: 5.08%
    Packets sent: 19290, Retransmissions: 980, Lost: 0  ← STILL SHOWING "Lost: 0"!
```

### Issues Identified

#### Issue 3.1: Analysis Reports "Lost: 0" Despite JSON Showing Loss

The JSON clearly shows `pkt_recv_loss: 1702`, but the analysis reports "Lost: 0".

**Possible Causes:**
1. `ComputeDerivedMetrics()` is not correctly reading `CongestionRecvPktLoss` from Prometheus
2. The Prometheus metric key doesn't match what analysis expects
3. The loss counter is being read from the wrong component (client vs server)

#### Issue 3.2: Discrepancy Between "Packets sent" Values

| Source | Packets Sent |
|--------|--------------|
| JSON `pkt_sent_data` | 21,426 |
| Analysis output | 19,290 |
| JSON `PktSentUnique` | 18,614 |

The analysis may be using a different counter or component.

#### Issue 3.3: Recovery Rate Threshold Still Marginal

- **Current threshold**: 95%
- **Observed**: 94.92%
- **Margin**: Only 0.08% below threshold

With 63 unrecovered packets out of ~1700 lost:
- Actual recovery: (1702 - 63) / 1702 = **96.3%**

The analysis calculation may differ from this simple formula.

### What's Working ✅

1. **Throughput display now shows retransmissions**: `959 retx` visible
2. **NAK mechanism is working**: 905 NAKs sent, 980 retransmissions
3. **Retransmission percentage accurate**: 4.57% ≈ 5% configured loss
4. **All 118 metrics are now aligned** (AST audit passed)

### What's NOT Working ❌

1. **"Lost: 0"** in analysis - metric not being read correctly
2. **Recovery rate threshold** - marginal failure (94.92% vs 95.0%)
3. **Packet count discrepancy** - different values in JSON vs analysis

### Root Cause Hypothesis

The `CongestionRecvPktLoss` counter is exported to Prometheus as:
```
gosrt_connection_congestion_packets_lost_total{direction="recv"}
```

But `ComputeDerivedMetrics()` may be looking for a different metric name, or looking at the wrong component (client instead of server's receiver side).

### Proposed Investigation

1. **Debug metric extraction**: Add logging to see what Prometheus values `analysis.go` is actually reading
2. **Verify metric key**: Confirm `getSumByPrefix()` matches the actual Prometheus output
3. **Check component selection**: Ensure we're reading from the SERVER's metrics for loss counts (not client-generator or client)

### Previous Analysis (2024-12-08)

| Metric | Value (Previous) | Value (Latest) |
|--------|-----------------|----------------|
| Configured loss | 5% | 5% |
| Observed loss | 8.86% | Unknown (shows 0) |
| Packets sent | 21,591 | 21,426 |
| Retransmissions | 1,095 | 980 |
| Recovery rate | 94.36% | 94.92% |

### Possible Fixes (Unchanged)

**Option A: Fix "Lost: 0" metric reading issue** (PRIORITY)
- Debug and fix `ComputeDerivedMetrics()` to correctly read `CongestionRecvPktLoss`

**Option B: Adjust threshold for 5% loss tests**
- 5% loss: 93% minimum recovery rate instead of 95%

**Option C: Increase SRT buffers for high-loss scenarios**
- Larger flow control window
- Longer retransmission timeout

---

## Defect 4: Analysis Reports "Lost: 0" Despite Actual Loss

**Status**: 🔴 Open
**Priority**: High
**Discovered**: 2024-12-09
**Related**: Defect 3

### Symptoms

The analysis output reports "Lost: 0" even though JSON stats clearly show loss:

**Analysis Output:**
```
Observed Statistics:
    Configured loss: 5.0%, Retrans% of sent: 5.08%
    Packets sent: 19290, Retransmissions: 980, Lost: 0  ← WRONG!
```

**JSON Stats (same test):**
```json
{
  "pkt_recv_loss": 1702  ← ACTUAL LOSS
}
```

### Evidence

From the 5% loss test run (2024-12-09):

| Metric Source | Counter | Value |
|---------------|---------|-------|
| JSON `pkt_recv_loss` | Server detecting sequence gaps | 1,702 |
| Prometheus `gosrt_connection_congestion_packets_lost_total` | Should match | Unknown |
| Analysis `DerivedMetrics.TotalPacketsLost` | Should be 1,702 | **0** |

### Root Cause Analysis

The `analysis.go` file computes derived metrics in `ComputeDerivedMetrics()`. Let's trace the flow:

1. **Prometheus exports** `gosrt_connection_congestion_packets_lost_total{direction="recv"}`
2. **`ComputeDerivedMetrics()`** should read this with `getSumByPrefix()` or similar
3. **Problem**: Either the metric key doesn't match, or it's reading from wrong component

**Key Question**: Are we reading loss from `ts.Server` or from `ts.Client`?

The data flow is:
```
Client-Generator → SERVER (detects loss) → Client
       ↓                    ↓                ↓
  Sends packets      Records loss       Receives data
```

Loss detection happens on the SERVER (receiver side of client-generator connection). But the analysis might be reading from the Client component.

### Investigation Steps

1. Add debug logging to `ComputeDerivedMetrics()` to show raw Prometheus values
2. Verify `getSumByPrefix()` regex matches `gosrt_connection_congestion_packets_lost_total`
3. Confirm we're reading from `ts.Server` not `ts.Client` for loss counts
4. Check if zero-value filtering is hiding the metric

### Impact

- Statistical validation cannot work correctly without loss counts
- "Recovery rate" calculation is affected
- Cross-endpoint loss validation is affected

---

## Improvement Ideas

### I1: Show Retransmit Count in Throughput Display ✅ IMPLEMENTED

**Old output:**
```
11:11:55.16 |    10.22 kpkt/s |  305.09 pkt/s |    19.97 MB |  4.999 Mb/s |      10.22k ok /      0 loss ~= 100.000% success
```

**New output (implemented):**
```
HH:MM:SS.xx | kpkt/s | pkt/s | MB | Mb/s | 9999k ok / 999 loss / 999 retx ~= 100.0% success
```

**Changes made:**
- Updated `ThroughputGetter` to include retransmit count
- Updated `RunThroughputDisplay()` to display retransmits
- Updated client to show `CongestionRecvPktRetrans`
- Updated client-generator to show `CongestionSendPktRetrans`

### I2: Add Retransmission Counter to Prometheus

The Prometheus metrics don't export retransmission counts directly. This would be valuable for monitoring.

**Note**: This was partially addressed in Defect 1 Phase 1 with `gosrt_connection_congestion_retransmissions_total`.

### I3: Export Loss Rate as Gauge

Export `pkt_recv_loss_rate` as a Prometheus gauge for real-time loss monitoring.

### I4: Test Duration Configuration

Some tests may need longer durations to accumulate enough samples for statistical validation.

---

## Summary: Current State (2024-12-09)

### What's Fixed ✅

| Item | Status | Notes |
|------|--------|-------|
| **Prometheus metric exports** | ✅ Complete | All 118 metrics aligned (AST audit passed) |
| **Throughput display retransmissions** | ✅ Working | `[PUB]` shows `959 retx` |
| **NAK counter increments** | ✅ Fixed | `PktSentNAKSuccess`/`PktRecvNAKSuccess` now incremented |
| **CongestionSendNAKNotFound** | ✅ Fixed | Now tracks unretransmittable NAK requests |
| **PktRecvDataError aggregate** | ✅ Fixed | Now incremented with granular counters |

### What's Still Broken ❌

| Defect | Priority | Issue |
|--------|----------|-------|
| **Defect 3** | Medium | 5% loss test fails at 94.92% (threshold 95%) |
| **Defect 4** | High | Analysis reports "Lost: 0" despite actual 1,702 losses |

### Observations from Latest Test

The SRT ARQ mechanism is **working correctly**:
- 905 NAKs sent by server
- 980 retransmissions triggered
- 63 unrecoverable packets (3.7% of lost packets)
- ~96.3% actual recovery rate

The issues are in the **analysis/validation layer**, not SRT itself:
1. `TotalPacketsLost` not being populated from Prometheus
2. Threshold may be too aggressive for 5% loss scenarios

### Next Steps

1. **Debug Defect 4**: Add logging to `ComputeDerivedMetrics()` to trace why "Lost: 0"
2. **Verify metric keys**: Confirm Prometheus output matches what analysis expects
3. **Consider relaxing threshold**: 93% for 5% loss instead of 95%
4. **Run 2% loss test**: Verify it still passes to confirm regression didn't occur

---

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-08 | Initial document, Defect 1 documented | - |
| 2024-12-08 | Added Defect 2, 3 and implementation phases | - |
| 2024-12-09 | Updated after AST metrics audit (118 metrics aligned) | - |
| 2024-12-09 | Added Defect 4: "Lost: 0" analysis bug | - |
| 2024-12-09 | Updated Defect 3 with latest test results | - |

