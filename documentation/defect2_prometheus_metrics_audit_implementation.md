# Defect 2: NAK Counter Audit and Implementation

**Status**: ✅ Fixed (Pending Final Verification)
**Priority**: High
**Created**: 2024-12-08

## Summary

The statistical validation fails because NAK counters are not exported to Prometheus:
```
Statistical Validation: ✗ FAILED
  ✗ NAKsPerLostPacket: expected >= 0.50, got 0.00
```

Despite SRT clearly showing NAKs working (414 NAKs sent, 432 retransmissions).

---

## Phase 1: Full Prometheus Handler Audit

### Objective

Compare all fields in `metrics/metrics.go` against what's exported in `metrics/handler.go`.

### Audit Results

#### ✅ Metrics Currently Exported

| Metric Category | Counter Name | Prometheus Metric Name |
|----------------|--------------|------------------------|
| Success receive | `PktRecvSuccess` | `gosrt_connection_packets_received_total{type="all",status="success"}` |
| Edge cases | `PktRecvNil` | `gosrt_connection_packets_received_total{type="nil",status="error"}` |
| Edge cases | `PktRecvControlUnknown` | `gosrt_connection_packets_received_total{type="control_unknown",status="error"}` |
| Edge cases | `PktRecvSubTypeUnknown` | `gosrt_connection_packets_received_total{type="subtype_unknown",status="error"}` |
| Crypto errors | `CryptoErrorEncrypt` | `gosrt_connection_crypto_error_total{operation="encrypt"}` |
| Crypto errors | `CryptoErrorGenerateSEK` | `gosrt_connection_crypto_error_total{operation="generate_sek"}` |
| Crypto errors | `CryptoErrorMarshalKM` | `gosrt_connection_crypto_error_total{operation="marshal_km"}` |
| ACK receive | `PktRecvACKSuccess` | `gosrt_connection_packets_received_total{type="ack"}` |
| ACK drop | `PktRecvACKDropped` | `gosrt_connection_packets_dropped_total{type="ack"}` |
| Lock timing | Various | `gosrt_connection_lock_*` metrics |
| Congestion recv drops | Various | `gosrt_connection_congestion_recv_data_drop_total` |
| Congestion send drops | `CongestionSendDataDropTooOld` | `gosrt_connection_congestion_send_data_drop_total` |
| Data send errors | Various | `gosrt_connection_send_data_drop_total` |
| Control send errors | Various | `gosrt_connection_send_control_drop_total` |
| Data recv errors | Various | `gosrt_connection_recv_data_error_total` |
| Control recv errors | Various | `gosrt_connection_recv_control_error_total` |
| io_uring | `PktSentSubmitted` | `gosrt_connection_send_submitted_total` |
| Congestion CC | Various | `gosrt_connection_congestion_*` (Phase 1 additions) |

#### ❌ Metrics NOT Exported - Critical for Defect 2

| Category | Counter Name | Impact |
|----------|--------------|--------|
| **NAK Receive** | `PktRecvNAKSuccess` | **CRITICAL - Needed for validation** |
| **NAK Receive** | `PktRecvNAKDropped` | Error tracking |
| **NAK Receive** | `PktRecvNAKError` | Error tracking |
| **NAK Send** | `PktSentNAKSuccess` | **CRITICAL - Needed for validation** |
| **NAK Send** | `PktSentNAKDropped` | Error tracking |
| **NAK Send** | `PktSentNAKError` | Error tracking |
| **Retransmission** | `PktRetransFromNAK` | **CRITICAL - Direct retrans counter** |

#### ❌ Metrics NOT Exported - Data Packet Counters

| Category | Counter Name | Impact |
|----------|--------------|--------|
| Data packets | `PktRecvDataSuccess` | Useful for throughput |
| Data packets | `PktRecvDataDropped` | Error tracking |
| Data packets | `PktRecvDataError` | Error tracking |
| Data packets | `PktSentDataSuccess` | Useful for throughput |
| Data packets | `PktSentDataDropped` | Error tracking |
| Data packets | `PktSentDataError` | Error tracking |

#### ❌ Metrics NOT Exported - ACKACK Counters

| Category | Counter Name | Impact |
|----------|--------------|--------|
| ACKACK | `PktRecvACKACKSuccess` | ACK round-trip tracking |
| ACKACK | `PktRecvACKACKDropped` | Error tracking |
| ACKACK | `PktRecvACKACKError` | Error tracking |
| ACKACK | `PktSentACKACKSuccess` | ACK round-trip tracking |
| ACKACK | `PktSentACKACKDropped` | Error tracking |
| ACKACK | `PktSentACKACKError` | Error tracking |

#### ❌ Metrics NOT Exported - Other Control Packets

| Category | Counter Name |
|----------|--------------|
| KM | `PktRecvKMSuccess/Dropped/Error`, `PktSentKMSuccess/Dropped/Error` |
| Keepalive | `PktRecvKeepaliveSuccess/Dropped/Error`, `PktSentKeepaliveSuccess/Dropped/Error` |
| Shutdown | `PktRecvShutdownSuccess/Dropped/Error`, `PktSentShutdownSuccess/Dropped/Error` |
| Handshake | `PktRecvHandshakeSuccess/Dropped/Error`, `PktSentHandshakeSuccess/Dropped/Error` |

#### ❌ Metrics NOT Exported - Path Tracking

| Category | Counter Name |
|----------|--------------|
| Path | `PktRecvIoUring`, `PktRecvReadFrom` |
| Path | `PktSentIoUring`, `PktSentWriteTo` |

#### ❌ Metrics NOT Exported - Routing/Resource Errors

| Category | Counter Name |
|----------|--------------|
| Routing | `PktRecvUnknownSocketId`, `PktRecvNilConnection`, `PktRecvWrongPeer`, `PktRecvBacklogFull` |
| Resource | `PktSentRingFull`, `PktRecvQueueFull` |

#### ❌ Metrics NOT Exported - Byte Counters

| Category | Counter Name |
|----------|--------------|
| Bytes | `ByteRecvDataSuccess`, `ByteRecvDataDropped` |
| Bytes | `ByteSentDataSuccess`, `ByteSentDataDropped` |

#### ❌ Metrics NOT Exported - Special Counters

| Category | Counter Name |
|----------|--------------|
| Crypto | `PktRecvUndecrypt`, `ByteRecvUndecrypt` |
| Validation | `PktRecvInvalid` |
| Link | `HeaderSize`, `MbpsLinkCapacity` |

#### ❌ Metrics NOT Exported - Congestion Control Details

| Category | Counter Name |
|----------|--------------|
| Recv bytes | `CongestionRecvByteUnique`, `CongestionRecvByteLoss`, `CongestionRecvByteRetrans` |
| Recv state | `CongestionRecvPktBelated`, `CongestionRecvByteBelated` |
| Recv drops | `CongestionRecvPktDrop`, `CongestionRecvByteDrop` (aggregate) |
| Recv buffer | `CongestionRecvPktBuf`, `CongestionRecvByteBuf`, `CongestionRecvMsBuf` |
| Recv rates | `CongestionRecvBytePayload`, `CongestionRecvMbpsBandwidth`, `CongestionRecvMbpsLinkCapacity`, `CongestionRecvPktLossRate` |
| Send bytes | `CongestionSendByteUnique`, `CongestionSendByteLoss`, `CongestionSendByteRetrans` |
| Send state | `CongestionSendUsSndDuration`, `CongestionSendPktFlightSize`, `CongestionSendUsPktSndPeriod` |
| Send drops | `CongestionSendPktDrop`, `CongestionSendByteDrop` (aggregate) |
| Send buffer | `CongestionSendPktBuf`, `CongestionSendByteBuf`, `CongestionSendMsBuf` |
| Send rates | `CongestionSendBytePayload`, `CongestionSendMbpsInputBandwidth`, `CongestionSendMbpsSentBandwidth`, `CongestionSendPktLossRate` |
| Errors | `CongestionRecvPktNil`, `CongestionRecvPktStoreInsertFailed`, `CongestionRecvDeliveryFailed` |
| Errors | `CongestionSendDeliveryFailed`, `CongestionSendNAKNotFound` |

---

## Phase 2: Implementation Plan

### Priority 1: Fix Defect 2 (NAK Validation)

**Add to handler.go:**

```go
// NAK counters - success
writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvNAKSuccess.Load(),
    "socket_id", socketIdStr, "type", "nak", "status", "success")
writeCounterValue(b, "gosrt_connection_packets_sent_total",
    metrics.PktSentNAKSuccess.Load(),
    "socket_id", socketIdStr, "type", "nak", "status", "success")

// NAK counters - dropped/error
writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvNAKDropped.Load(),
    "socket_id", socketIdStr, "type", "nak", "status", "dropped")
writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvNAKError.Load(),
    "socket_id", socketIdStr, "type", "nak", "status", "error")
writeCounterValue(b, "gosrt_connection_packets_sent_total",
    metrics.PktSentNAKDropped.Load(),
    "socket_id", socketIdStr, "type", "nak", "status", "dropped")
writeCounterValue(b, "gosrt_connection_packets_sent_total",
    metrics.PktSentNAKError.Load(),
    "socket_id", socketIdStr, "type", "nak", "status", "error")

// Retransmission from NAK (direct counter)
writeCounterValue(b, "gosrt_connection_retransmissions_from_nak_total",
    metrics.PktRetransFromNAK.Load(),
    "socket_id", socketIdStr)
```

### Priority 2: Add ACKACK Counters

```go
// ACKACK counters
writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvACKACKSuccess.Load(),
    "socket_id", socketIdStr, "type", "ackack", "status", "success")
writeCounterValue(b, "gosrt_connection_packets_sent_total",
    metrics.PktSentACKACKSuccess.Load(),
    "socket_id", socketIdStr, "type", "ackack", "status", "success")
```

### Priority 3: Add Data Packet Counters

```go
// Data packet counters
writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvDataSuccess.Load(),
    "socket_id", socketIdStr, "type", "data", "status", "success")
writeCounterValue(b, "gosrt_connection_packets_sent_total",
    metrics.PktSentDataSuccess.Load(),
    "socket_id", socketIdStr, "type", "data", "status", "success")
```

### Priority 4: Add ACK Sent Counter

Note: Only `PktRecvACKSuccess` is exported, not `PktSentACKSuccess`!

```go
writeCounterValue(b, "gosrt_connection_packets_sent_total",
    metrics.PktSentACKSuccess.Load(),
    "socket_id", socketIdStr, "type", "ack", "status", "success")
```

---

## Phase 3: Update Analysis Code

After adding NAK exports, update `analysis.go` to use them:

```go
// In ComputeDerivedMetrics:
dm.TotalNAKsSent = getSumByPrefixContaining(last, "gosrt_connection_packets_sent_total", "type=\"nak\"", "status=\"success\"")
dm.TotalNAKsRecv = getSumByPrefixContaining(last, "gosrt_connection_packets_received_total", "type=\"nak\"", "status=\"success\"")
dm.TotalRetransFromNAK = getSumByPrefix(last, "gosrt_connection_retransmissions_from_nak_total")
```

---

## Phase 4: Update Throughput Display (Improvement I1)

After Defect 2 is fixed, update `contrib/common/statistics.go` to show retransmit count.

---

## Implementation Checklist

- [x] Phase 1: Audit complete (documented above)
- [x] Phase 2.1: Add NAK counters to handler.go ✅
- [x] Phase 2.2: Add ACKACK counters to handler.go ✅
- [x] Phase 2.3: Add Data packet counters to handler.go ✅
- [x] Phase 2.4: Add ACK sent counter to handler.go ✅
- [x] Phase 2.5: Add PktRetransFromNAK to handler.go ✅
- [x] Phase 3: Update analysis.go to use new metrics ✅
- [x] Phase 4: Update throughput display ✅
- [ ] ~~Verify tests pass~~ **STILL FAILING**
- [x] Phase 5: Fix throughput display ✅
- [x] Phase 6: Increment NAK counters in congestion control layer ✅
- [ ] Phase 7: Verify all congestion counters are populated

## Implementation Progress

### Phase 2: Prometheus Handler Updates (Completed 2024-12-08)

Added comprehensive control packet counters to `metrics/handler.go`:

**Control Packet Types Added:**
- ACK (sent/received with success/dropped/error status)
- ACKACK (sent/received with success/dropped/error status)
- NAK (sent/received with success/dropped/error status) ← **Fixes Defect 2**
- DATA (sent/received with success/dropped/error status)
- Keepalive (sent/received success)
- Shutdown (sent/received success)
- Handshake (sent/received success)
- KM (sent/received success)

**Additional Counters:**
- `gosrt_connection_retransmissions_from_nak_total` - Direct retransmission counter
- Byte counters for DATA packets (sent/received)

### Phase 3: Analysis Updates (Completed 2024-12-08)

Updated `contrib/integration_testing/analysis.go`:

- Added `TotalRetransFromNAK` and `TotalACKACKsSent/Recv` to `DerivedMetrics`
- Added computation for new metrics in `ComputeDerivedMetrics()`

### Phase 4: Throughput Display (Completed 2024-12-08)

Updated `contrib/common/statistics.go`:

**Old output:**
```
HH:MM:SS.xx | kpkt/s | pkt/s | MB | Mb/s | 9999k ok / 0 loss ~= 100.0% success
```

**New output:**
```
HH:MM:SS.xx | kpkt/s | pkt/s | MB | Mb/s | 9999k ok / 999 loss / 999 retx ~= 100.0% success
```

Updated callers:
- `contrib/client/main.go` - Shows `CongestionRecvPktRetrans`
- `contrib/client-generator/main.go` - Shows `CongestionSendPktRetrans`

⚠️ **ISSUE FOUND**: These counters are NOT being populated! See Phase 5-7.

---

## Phases 5-7: Fixing the Counter Population Issue

### Problem Summary

The counters we added to Prometheus exports exist in `metrics.ConnectionMetrics`, but they're NOT being incremented by the congestion control layer.

**Evidence from test run (2024-12-08):**
- JSON stats: `pkt_recv_nak: 360`, `pkt_retrans_from_nak: 368`
- Throughput display: `0 retx`
- Prometheus NAKs: `0`

### Phase 5: Fix Throughput Display (Completed 2024-12-08)

**Problems Fixed:**
1. `kpkt/s` label was misleading (showed total, not rate)
2. `0.00k ok` showed 0 because `PktRecvSuccess` wasn't populated
3. `0 retx` showed 0 because `CongestionSendPktRetrans` wasn't populated

**Changes Made:**

1. **Simplified display format** (`contrib/common/statistics.go`):
   - Removed redundant "kpkt/s" column
   - Use `currentPkts` for "ok" counter (instead of `successPkts`)
   - New format: `HH:MM:SS.xx | pkt/s | MB | Mb/s | ok / loss / retx ~= %`

2. **Fixed retransmit counter** (`contrib/client-generator/main.go`):
   ```go
   // OLD: clientMetrics.CongestionSendPktRetrans.Load()  // Returns 0
   // NEW: clientMetrics.PktRetransFromNAK.Load()         // Returns 368
   ```

### Phase 6: Increment NAK Counters in Congestion Control (Completed 2024-12-08)

**Problem**: The comments in `connection.go` said "NAK metrics are tracked via packet classifier" but this was **incorrect**. The `sendNAK()` and `handleNAK()` functions bypass the packet classifier entirely.

**Root Cause**: Control packets sent via `c.pop()` don't go through the classifier.

**Solution Applied** (`connection.go`):

1. In `sendNAK()` - Added counter increment when sending NAKs:
```go
if c.metrics != nil {
    c.metrics.PktSentNAKSuccess.Add(1)
}
```

2. In `handleNAK()` - Added counter increment when receiving NAKs:
```go
if c.metrics != nil {
    c.metrics.PktRecvNAKSuccess.Add(1)
}
```

**Note**: The congestion control layer (`recv.go`, `send.go`) doesn't have direct access to metrics - it uses callbacks to `connection.go` where the metrics live.

### Phase 7: Audit Congestion Control Counter Population

Check which `Congestion*` counters are actually being incremented:

| Counter | Expected Increment Location | Verified? |
|---------|---------------------------|-----------|
| `CongestionSendPkt` | send.go:Send() | ❓ |
| `CongestionRecvPkt` | recv.go:Push() | ❓ |
| `CongestionSendPktRetrans` | send.go:retransmit() | ❓ |
| `CongestionRecvPktRetrans` | recv.go:Push() | ❓ |
| `CongestionSendPktLoss` | ? | ❓ |
| `CongestionRecvPktLoss` | recv.go:lossDetection() | ❓ |

---

## Test Evidence

From the test run, we can see NAKs ARE working:

**Client-generator (sender) side:**
```json
{
  "pkt_recv_nak": 414,
  "pkt_retrans_from_nak": 432,
  "pkt_retrans_percent": 2.065206998757051
}
```

**Server (receiver) side:**
```json
{
  "pkt_recv_loss": 783,
  "pkt_recv_loss_rate": 1.7341188524590163
}
```

This proves:
- Loss detection is working (783 packets detected as lost via sequence gaps)
- NAKs are being sent (414 NAKs received by sender)
- Retransmission is working (432 packets retransmitted, ~2% rate)
- **The SRT ARQ mechanism is functioning correctly!**

The only issue is these counters aren't visible via Prometheus.

---

## Phase 7: Analysis Code Bug (FOUND AND FIXED)

### Issue

After implementing all Prometheus exports, the test STILL reported `NAKsPerLostPacket: 0.00`.

### Root Cause

The analysis code in `computeObservedStatistics()` was using the **wrong component** for NAK counting:

```go
// BUG: Was looking at CLIENT's NAKs sent
sender := ComputeDerivedMetrics(ts.ClientGenerator)
receiver := ComputeDerivedMetrics(ts.Client)

stats.NAKsPerLostPacket = float64(receiver.TotalNAKsSent) / float64(lossCount)
```

But in the relay topology:
```
Client-Generator → SERVER → Client
         ↑           ↓
         └── NAKs ───┘
```

NAKs are sent by the **SERVER** (when it detects loss from client-generator), NOT by the Client!

### Fix

Updated `analysis.go` to use the server's metrics:

```go
sender := ComputeDerivedMetrics(ts.ClientGenerator)
receiver := ComputeDerivedMetrics(ts.Client)
server := ComputeDerivedMetrics(ts.Server)  // Added

// Fixed: Use server's NAKs, not client's
stats.NAKsPerLostPacket = float64(server.TotalNAKsSent) / float64(lossCount)
```

### Status: ✅ FIXED

---

## Final Summary

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Prometheus Handler Audit | ✅ Complete |
| 2.1-2.5 | Comprehensive Metric Export | ✅ Complete |
| 3 | Analysis Updates | ✅ Complete |
| 4 | Throughput Display Update | ✅ Complete |
| 5 | Quick Fix for Throughput Display | ✅ Complete |
| 6 | NAK Counter Increments in Congestion Control | ✅ Complete |
| 7 | Analysis Code Bug (wrong component) | ✅ Complete |

