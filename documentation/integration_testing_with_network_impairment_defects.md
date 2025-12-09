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

### Symptoms

When running the 5% packet loss test:
```bash
sudo make test-network CONFIG=Network-Loss5pct-5Mbps
```

The test fails with:
```
Statistical Validation: ✗ FAILED
  ✗ RecoveryRate: expected >= 95.0%, got 94.36
    Poor loss recovery - too many unrecoverable packets
```

### Observed Data

| Metric | Value |
|--------|-------|
| Configured loss | 5% |
| Observed loss rate | 8.86% (higher than configured) |
| Packets sent | 21,591 |
| Packets lost (detected) | 1,918 |
| Retransmissions | 1,095 |
| Unrecovered drops | 68 |
| Recovery rate | 94.36% |

### Deeper Analysis: Why 8.86% When Only 5% Configured?

**Network Topology:**
```
Publisher (10.1.1.2) ──┐
                       ├── ROUTER_CLIENT ══════ ROUTER_SERVER ─── Server (10.2.1.2)
Subscriber (10.1.2.2) ─┘        ↑                     ↑
                              link_a               link_b
                           (5% LOSS)            (NO LOSS)
```

**Loss is applied only on ROUTER_CLIENT → ROUTER_SERVER direction.**

**Expected vs Observed:**
| Metric | Expected (5% one-way) | Observed |
|--------|----------------------|----------|
| Loss events | ~1,080 | 1,918 |
| Loss rate | ~5.26% | 8.86% |

**The Math:**
- Original packets: 21,591 - 1,095 retrans = 20,496
- Expected losses (5%): 20,496 × 0.05 = 1,025
- Expected retrans losses (5% of 1,095): 55
- Total expected: ~1,080

**Actual: 1,918** ← Almost **2× expected**!

**Possible Causes:**
1. **Loss counter includes reordering events?** - If packets arrive out of order, they might be counted as "lost" then "recovered"
2. **Double-counting in sequence gap detection?**
3. **netem internal behavior with bursty patterns?**

**Observations:**
1. The recovery rate (94.36%) is marginal - only 68 packets truly unrecoverable
2. SRT ARQ IS working - 1,095 successful retransmissions
3. The high "loss" count might be a counting/detection issue, not actual network loss

### Possible Fixes

**Option A: Adjust threshold for 5% loss tests**

For higher loss scenarios, the 95% recovery rate may be too strict. Consider:
- 5% loss: 93% minimum recovery rate
- 10% loss: 90% minimum recovery rate

**Option B: Increase SRT buffers for high-loss scenarios**

The test config uses `latency=3000ms`. For 5% loss, we might need:
- Larger flow control window
- Longer retransmission timeout
- Deeper receiver buffer

**Option C: Adjust test duration**

A 30-second test may not be long enough to average out burst loss effects. Consider longer tests for statistical stability.

### Impact

- **2% loss tests**: PASSING ✅
- **5% loss tests**: FAILING (marginal) ❌
- **Higher loss tests**: Unknown

### Next Steps

1. Run the test multiple times to see if 94.36% is consistent or varies
2. Review threshold settings in `test_configs.go` for high-loss scenarios
3. Consider whether this is a threshold issue or an actual SRT performance concern

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

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-08 | Initial document, Defect 1 documented | - |

