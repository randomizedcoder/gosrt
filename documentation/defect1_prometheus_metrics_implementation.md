# Defect 1 Fix: Prometheus Metrics Export for Loss Detection

This document tracks the implementation progress for fixing Defect 1 (Statistical Validation Reports 0% Loss Despite Actual Loss Occurring).

**Related Documents**:
- [Defect Tracking](./integration_testing_with_network_impairment_defects.md)
- [Metrics Design](./metrics_and_statistics_design.md)
- [Metrics Implementation Progress](./metrics_implementation_progress.md)

---

## Overview

The congestion control metrics (`CongestionSendPkt`, `CongestionRecvPkt`, `CongestionRecvPktLoss`, etc.) exist in `ConnectionMetrics` but are not exported to Prometheus. This prevents the integration test framework from validating packet loss during network impairment tests.

## Implementation Phases

| Phase | Description | Status | Effort |
|-------|-------------|--------|--------|
| 1 | Export congestion control metrics to Prometheus | ✅ Complete | 30 min |
| 2 | Update analysis.go to use new Prometheus metrics | ✅ Complete | 30 min |
| 3 | Add cross-endpoint loss validation | ✅ Complete | 1 hour |
| 4 | Documentation updates | ✅ Complete | 30 min |

---

## Phase 1: Export Congestion Control Metrics to Prometheus

**Status**: ✅ Complete

**Objective**: Add exports for congestion control counters that already exist in `ConnectionMetrics` but are not exported to the `/metrics` endpoint.

### Metrics Exported

| Metric Name | Source Field | Description |
|-------------|--------------|-------------|
| `gosrt_connection_congestion_packets_total{direction="send"}` | `CongestionSendPkt` | Total packets sent by congestion control |
| `gosrt_connection_congestion_packets_total{direction="recv"}` | `CongestionRecvPkt` | Total packets received by congestion control |
| `gosrt_connection_congestion_packets_unique_total{direction="send"}` | `CongestionSendPktUnique` | Unique packets sent (excludes retransmissions) |
| `gosrt_connection_congestion_packets_unique_total{direction="recv"}` | `CongestionRecvPktUnique` | Unique packets received (excludes duplicates) |
| `gosrt_connection_congestion_packets_lost_total{direction="recv"}` | `CongestionRecvPktLoss` | Packets lost (detected via sequence gaps) |
| `gosrt_connection_congestion_packets_lost_total{direction="send"}` | `CongestionSendPktLoss` | Packets reported lost by peer |
| `gosrt_connection_congestion_retransmissions_total{direction="send"}` | `CongestionSendPktRetrans` | Packets retransmitted by sender |
| `gosrt_connection_congestion_retransmissions_total{direction="recv"}` | `CongestionRecvPktRetrans` | Retransmitted packets received |
| `gosrt_connection_congestion_bytes_total{direction="send"}` | `CongestionSendByte` | Bytes sent |
| `gosrt_connection_congestion_bytes_total{direction="recv"}` | `CongestionRecvByte` | Bytes received |

### Tasks

| Task | Status | Notes |
|------|--------|-------|
| Add `CongestionSendPkt` export | ✅ Complete | |
| Add `CongestionRecvPkt` export | ✅ Complete | |
| Add `CongestionSendPktUnique` export | ✅ Complete | Added for completeness |
| Add `CongestionRecvPktUnique` export | ✅ Complete | Added for completeness |
| Add `CongestionRecvPktLoss` export | ✅ Complete | |
| Add `CongestionSendPktLoss` export | ✅ Complete | Added for completeness |
| Add `CongestionSendPktRetrans` export | ✅ Complete | |
| Add `CongestionRecvPktRetrans` export | ✅ Complete | |
| Add `CongestionSendByte` export | ✅ Complete | For throughput calculation |
| Add `CongestionRecvByte` export | ✅ Complete | For throughput calculation |
| Build and verify no errors | ✅ Complete | `go build ./...` passes |
| Test with `curl` to verify output | 🔲 Pending | Requires running server |

### Files Modified

- `metrics/handler.go` - Added 10 new writeCounterValue calls for congestion control metrics

### Implementation Notes

Added more metrics than originally planned for completeness:
- `CongestionSendPktUnique` / `CongestionRecvPktUnique` - Useful for distinguishing unique vs total packets
- `CongestionSendPktLoss` - Peer-reported loss (complements receiver-detected loss)
- `CongestionSendByte` / `CongestionRecvByte` - Useful for throughput calculation

---

## Phase 2: Update Analysis to Use New Prometheus Metrics

**Status**: ✅ Complete

**Objective**: Update `ComputeDerivedMetrics()` in `analysis.go` to use the newly exported Prometheus counters for accurate loss calculation.

### Tasks

| Task | Status | Notes |
|------|--------|-------|
| Update `TotalPacketsSent` to use `gosrt_connection_congestion_packets_total{direction="send"}` | ✅ Complete | |
| Update `TotalPacketsRecv` to use `gosrt_connection_congestion_packets_total{direction="recv"}` | ✅ Complete | |
| Update `TotalPacketsLost` to use `gosrt_connection_congestion_packets_lost_total` | ✅ Complete | |
| Update `TotalRetransmissions` to use `gosrt_connection_congestion_retransmissions_total` | ✅ Complete | |
| Update `TotalBytesSent` to use `gosrt_connection_congestion_bytes_total{direction="send"}` | ✅ Complete | |
| Update `TotalBytesRecv` to use `gosrt_connection_congestion_bytes_total{direction="recv"}` | ✅ Complete | |
| Remove workaround code (byte estimation) | ✅ Complete | Now uses actual bytes |
| Build and verify no errors | ✅ Complete | `go build ./contrib/integration_testing/...` passes |

### Files Modified

- `contrib/integration_testing/analysis.go` - Updated `ComputeDerivedMetrics()`

### Implementation Notes

Changes made to `ComputeDerivedMetrics()`:
1. Replaced `gosrt_connection_packets_received_total` with `gosrt_connection_congestion_packets_total{direction="recv"}`
2. Replaced `gosrt_connection_send_submitted_total` with `gosrt_connection_congestion_packets_total{direction="send"}`
3. Now using `gosrt_connection_congestion_packets_lost_total{direction="recv"}` for accurate loss count
4. Now using `gosrt_connection_congestion_retransmissions_total{direction="send"}` for retransmissions
5. Now using actual bytes from `gosrt_connection_congestion_bytes_total` instead of estimating from packet count
6. Rate calculations now use actual byte counts, not estimates

---

## Phase 3: Add Cross-Endpoint Loss Validation

**Status**: ✅ Complete

**Objective**: Implement cross-endpoint loss calculation by comparing packets sent by sender vs packets received by receiver. This provides a second independent check that catches bugs in either single-endpoint detection.

### Design

Two independent loss calculation methods:

1. **Cross-Endpoint**: `(PacketsSent - PacketsRecv) / PacketsSent`
2. **Reported Loss**: `PacketsLost / PacketsSent` (from sequence gap detection)

The validation uses the **higher** of the two rates (more conservative) and warns if they disagree by > 50%.

### Tasks

| Task | Status | Notes |
|------|--------|-------|
| Add `CrossEndpointLossRate` field to `ObservedStatistics` | ✅ Complete | |
| Add `CrossEndpointLossAbs` field to `ObservedStatistics` | ✅ Complete | Absolute packet count |
| Add `ReportedLossRate` field to `ObservedStatistics` | ✅ Complete | |
| Add `ReportedLossAbs` field to `ObservedStatistics` | ✅ Complete | Absolute packet count |
| Add `LossMethodsAgree` field to `ObservedStatistics` | ✅ Complete | Cross-check flag |
| Add `LossDiscrepancy` field to `ObservedStatistics` | ✅ Complete | For debugging |
| Implement cross-endpoint calculation in `computeObservedStatistics()` | ✅ Complete | |
| Add validation that compares both methods | ✅ Complete | 50% tolerance |
| Add warning if methods disagree by > 50% | ✅ Complete | In ValidateStatistical() |
| Update error message to show both methods | ✅ Complete | Shows cross-endpoint and reported |
| Build and verify no errors | ✅ Complete | |
| Test with network impairment test | 🔲 Pending | Requires running test |

### Files Modified

- `contrib/integration_testing/analysis.go`:
  - Extended `ObservedStatistics` struct with 6 new fields
  - Rewrote `computeObservedStatistics()` to use dual-method approach
  - Updated `ValidateStatistical()` to show both methods in error messages
  - Added warning when methods disagree

### Implementation Notes

Key design decisions:
1. **Use higher of two rates**: More conservative - catches losses either method might miss
2. **50% tolerance for cross-check**: Accounts for timing differences, in-flight packets
3. **Both absolute and rate values**: Easier debugging when values don't match
4. **Warning, not error**: Method disagreement is informational, not a test failure

---

## Phase 4: Documentation Updates

**Status**: ✅ Complete

**Objective**: Update documentation to reflect the new Prometheus exports and validation approach.

### Tasks

| Task | Status | Notes |
|------|--------|-------|
| Update `metrics_implementation_progress.md` | ✅ Complete | Added Phase 8.1 section |
| Update defect document to mark as fixed | ✅ Complete | Status: Fixed (Pending Verification) |
| Add comments to code explaining dual validation approach | ✅ Complete | In computeObservedStatistics() |
| Update this implementation tracking document | ✅ Complete | |

### Files Modified

- `documentation/metrics_implementation_progress.md` - Added Phase 8.1 with new exports
- `documentation/integration_testing_with_network_impairment_defects.md` - Updated status to Fixed
- `documentation/defect1_prometheus_metrics_implementation.md` - This document

### Implementation Notes

The dual-validation approach is documented in the code comments in `computeObservedStatistics()`.

---

## Issues Discovered During Implementation

_(To be filled during implementation)_

| Issue | Phase | Description | Resolution |
|-------|-------|-------------|------------|
| | | | |

---

## Improvements Identified

_(To be filled during implementation)_

| Improvement | Priority | Description |
|-------------|----------|-------------|
| | | |

---

## Testing Checklist

| Test | Status | Notes |
|------|--------|-------|
| `curl /metrics` shows new counters | 🔲 Pending | |
| Clean network test passes | 🔲 Pending | |
| 2% loss test passes | 🔲 Pending | |
| 5% loss test passes | 🔲 Pending | |
| Cross-endpoint validation matches sequence gap detection | 🔲 Pending | |

---

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-08 | Initial implementation plan created | - |
| 2024-12-08 | Phase 1 complete: Added 10 new Prometheus metrics for congestion control | - |
| 2024-12-08 | Phase 2 complete: Updated analysis.go to use new metrics | - |
| 2024-12-08 | Phase 3 complete: Added dual-method loss calculation with cross-check | - |
| 2024-12-08 | Phase 4 complete: Documentation updated | - |

