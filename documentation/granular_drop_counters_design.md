# Granular Drop Counters Design Proposal

## Overview

This document proposes adding granular drop counters for each specific drop reason, while maintaining aggregate counters for SRT RFC compliance. This will enable better debugging by identifying which specific drop types are occurring.

## Design Principles

1. **Granular Counters**: One counter per drop reason (for debugging)
2. **Aggregate Counter**: Sum of all granular counters (for SRT RFC compliance)
3. **DATA vs Control**: Separate counters for DATA and control packets (control drops are more serious)
4. **Minimal Changes**: Leverage existing infrastructure where possible

## Current State Analysis

### Receiver Drop Points (Congestion Control - DATA packets only)

1. **Too Old (Belated)** - `receive.go:278-292`
   - Current: `CongestionRecvPktDrop.Add(1)`
   - Also increments: `CongestionRecvPktBelated.Add(1)`

2. **Already Acknowledged** - `receive.go:295-304`
   - Current: `CongestionRecvPktDrop.Add(1)`

3. **Duplicate (Already in Store)** - `receive.go:312-320`
   - Current: `CongestionRecvPktDrop.Add(1)`

4. **Store Insert Failed** - `receive.go:338-346`
   - Current: `CongestionRecvPktDrop.Add(1)`
   - Also increments: `CongestionRecvPktStoreInsertFailed.Add(1)`

5. **Nil Packet** - `receive.go:209-213`
   - Current: `CongestionRecvPktNil.Add(1)` (separate counter, not in PktDrop)

### Sender Drop Points

#### Congestion Control (DATA packets only)

1. **Too Old (Exceed Drop Threshold)** - `send.go:323-335`
   - Current: `CongestionSendPktDrop.Add(1)`

#### Connection Level (DATA and Control packets)

2. **Serialization Error (Marshal Failure)** - `connection_linux.go:144-155`
   - Current: `PktSentErrorMarshal.Add(1)` (connection-level, not in CongestionSendPktDrop)
   - Affects: Both DATA and control packets

3. **io_uring Ring Full** - `connection_linux.go:217-235`
   - Current: `PktSentRingFull.Add(1)` (connection-level, not in CongestionSendPktDrop)
   - Also: `PktSentDataDropped.Add(1)` for DATA packets
   - Affects: Both DATA and control packets

4. **io_uring Submit Error** - `connection_linux.go:277+`
   - Current: `PktSentErrorSubmit.Add(1)` (connection-level, not in CongestionSendPktDrop)
   - Affects: Both DATA and control packets

5. **io_uring Completion Error** - `connection_linux.go:sendCompletionHandler()`
   - Current: Tracked via `IncrementSendMetrics` with error reason
   - Affects: Both DATA and control packets

## Proposed Design

### Option A: Minimal Change (Recommended)

Add granular counters for congestion control drops only, keep connection-level errors separate.

**New Counters** (DATA packets only, congestion control):
```go
// Receiver - Granular drop counters (DATA packets)
CongestionRecvDataDropTooOld        atomic.Uint64 // Belated, past play time
CongestionRecvDataDropAlreadyAcked  atomic.Uint64 // Already acknowledged
CongestionRecvDataDropDuplicate     atomic.Uint64 // Duplicate (already in store)
CongestionRecvDataDropStoreInsertFailed atomic.Uint64 // Store insert failed

// Sender - Granular drop counters (DATA packets)
CongestionSendDataDropTooOld        atomic.Uint64 // Exceed drop threshold
```

**Aggregate Counter Calculation**:
```go
// In Stats() methods:
PktRecvDrop = CongestionRecvDataDropTooOld +
              CongestionRecvDataDropAlreadyAcked +
              CongestionRecvDataDropDuplicate +
              CongestionRecvDataDropStoreInsertFailed

PktSendDrop = CongestionSendDataDropTooOld +
              PktSentErrorMarshal (DATA only) +
              PktSentRingFull (DATA only) +
              PktSentErrorSubmit (DATA only)
```

**Pros**:
- Minimal changes (only congestion control layer)
- Clear separation: congestion control vs connection-level
- DATA vs control distinction maintained (connection-level counters already distinguish)

**Cons**:
- Connection-level errors not granular (but they're already tracked separately)
- Need to track DATA vs control in connection-level error handlers

### Option B: Full Granularity

Add granular counters for ALL drop points, including connection-level.

**New Counters** (All drop points):
```go
// Receiver - Granular drop counters
CongestionRecvDataDropTooOld        atomic.Uint64
CongestionRecvDataDropAlreadyAcked  atomic.Uint64
CongestionRecvDataDropDuplicate     atomic.Uint64
CongestionRecvDataDropStoreInsertFailed atomic.Uint64

// Connection-level receive errors (already exist, but need DATA/control split)
PktRecvDataErrorParse      atomic.Uint64 // DATA packet parse errors
PktRecvControlErrorParse   atomic.Uint64 // Control packet parse errors
// ... etc for all error types

// Sender - Granular drop counters
CongestionSendDataDropTooOld        atomic.Uint64
ConnectionSendDataDropMarshal       atomic.Uint64 // DATA packet marshal errors
ConnectionSendControlDropMarshal    atomic.Uint64 // Control packet marshal errors
ConnectionSendDataDropRingFull      atomic.Uint64 // DATA packet ring full
ConnectionSendControlDropRingFull   atomic.Uint64 // Control packet ring full
// ... etc
```

**Pros**:
- Complete visibility into all drop reasons
- DATA vs control distinction everywhere

**Cons**:
- Large number of new counters (~20+)
- Significant changes to connection-level error handlers
- More complex aggregation logic

### Option C: Hybrid (Balanced)

Add granular counters for congestion control, enhance connection-level to distinguish DATA vs control.

**New Counters**:
```go
// Congestion control - Granular (same as Option A)
CongestionRecvDataDropTooOld        atomic.Uint64
CongestionRecvDataDropAlreadyAcked  atomic.Uint64
CongestionRecvDataDropDuplicate     atomic.Uint64
CongestionRecvDataDropStoreInsertFailed atomic.Uint64
CongestionSendDataDropTooOld        atomic.Uint64

// Connection-level - Split DATA vs control (enhance existing)
PktSentDataErrorMarshal    atomic.Uint64 // DATA packet marshal errors (new)
PktSentControlErrorMarshal atomic.Uint64 // Control packet marshal errors (new)
PktSentDataRingFull        atomic.Uint64 // DATA packet ring full (new)
PktSentControlRingFull     atomic.Uint64 // Control packet ring full (new)
// Keep existing aggregate counters for backward compatibility
```

**Aggregate Counter Calculation**:
```go
PktRecvDrop = CongestionRecvDataDropTooOld +
              CongestionRecvDataDropAlreadyAcked +
              CongestionRecvDataDropDuplicate +
              CongestionRecvDataDropStoreInsertFailed

PktSendDrop = CongestionSendDataDropTooOld +
              PktSentDataErrorMarshal +
              PktSentDataRingFull +
              PktSentDataErrorSubmit
```

**Pros**:
- Good balance of granularity and complexity
- DATA vs control distinction where it matters most
- Moderate changes (congestion control + connection-level enhancements)

**Cons**:
- Still need to track packet type in connection-level error handlers
- Some duplication (granular + aggregate counters)

## Recommendation: Option C (Hybrid)

**Rationale**:
1. **Congestion control drops** are the most common and need granular visibility
2. **Connection-level errors** should distinguish DATA vs control (control drops are more serious)
3. **Moderate scope** - not as extensive as Option B, but more useful than Option A
4. **Maintains backward compatibility** - aggregate counters still work

## Implementation Plan

### Phase 1: Add Granular Counters to ConnectionMetrics

**File**: `metrics/metrics.go`

Add new fields:
```go
// Congestion control - Granular drop counters (DATA packets only)
CongestionRecvDataDropTooOld        atomic.Uint64
CongestionRecvDataDropAlreadyAcked  atomic.Uint64
CongestionRecvDataDropDuplicate     atomic.Uint64
CongestionRecvDataDropStoreInsertFailed atomic.Uint64
CongestionSendDataDropTooOld        atomic.Uint64

// Connection-level - Split DATA vs control for send errors
PktSentDataErrorMarshal    atomic.Uint64 // DATA packet marshal errors
PktSentControlErrorMarshal atomic.Uint64 // Control packet marshal errors
PktSentDataRingFull        atomic.Uint64 // DATA packet ring full
PktSentControlRingFull     atomic.Uint64 // Control packet ring full
PktSentDataErrorSubmit     atomic.Uint64 // DATA packet submit errors
PktSentControlErrorSubmit  atomic.Uint64 // Control packet submit errors
```

### Phase 2: Update Congestion Control Drop Points

**Files**: `congestion/live/receive.go`, `congestion/live/send.go`

Replace `CongestionRecvPktDrop.Add(1)` with specific granular counter:
- Too old → `CongestionRecvDataDropTooOld.Add(1)` + `CongestionRecvPktDrop.Add(1)`
- Already ACK'd → `CongestionRecvDataDropAlreadyAcked.Add(1)` + `CongestionRecvPktDrop.Add(1)`
- Duplicate → `CongestionRecvDataDropDuplicate.Add(1)` + `CongestionRecvPktDrop.Add(1)`
- Store insert failed → `CongestionRecvDataDropStoreInsertFailed.Add(1)` + `CongestionRecvPktDrop.Add(1)`

Same for sender: `CongestionSendDataDropTooOld.Add(1)` + `CongestionSendPktDrop.Add(1)`

### Phase 3: Update Connection-Level Error Handlers

**Files**: `connection_linux.go`, `connection.go`, `listen.go`, `dial.go`

In `IncrementSendMetrics` and error handlers:
- Check if packet is DATA or control
- Increment appropriate granular counter (`PktSentDataErrorMarshal` vs `PktSentControlErrorMarshal`)
- Also increment aggregate `CongestionSendPktDrop` for DATA packets only

### Phase 4: Update Stats() Methods

**Files**: `congestion/live/receive.go`, `congestion/live/send.go`, `connection.go`

Calculate aggregate from granular counters:
```go
PktRecvDrop = CongestionRecvDataDropTooOld +
              CongestionRecvDataDropAlreadyAcked +
              CongestionRecvDataDropDuplicate +
              CongestionRecvDataDropStoreInsertFailed

PktSendDrop = CongestionSendDataDropTooOld +
              PktSentDataErrorMarshal +
              PktSentDataRingFull +
              PktSentDataErrorSubmit
```

### Phase 5: Update Prometheus Metrics

**File**: `metrics/handler.go`

Expose granular counters with labels:
```
gosrt_connection_congestion_recv_data_drop_total{reason="too_old",socket_id="..."} 10
gosrt_connection_congestion_recv_data_drop_total{reason="already_acked",socket_id="..."} 5
gosrt_connection_congestion_recv_data_drop_total{reason="duplicate",socket_id="..."} 2
gosrt_connection_congestion_recv_data_drop_total{reason="store_insert_failed",socket_id="..."} 1
gosrt_connection_congestion_send_data_drop_total{reason="too_old",socket_id="..."} 3
gosrt_connection_send_data_drop_total{reason="marshal",socket_id="..."} 0
gosrt_connection_send_data_drop_total{reason="ring_full",socket_id="..."} 0
gosrt_connection_send_control_drop_total{reason="marshal",socket_id="..."} 0
gosrt_connection_send_control_drop_total{reason="ring_full",socket_id="..."} 0
```

## Impact Analysis

### Code Changes Required

1. **New Fields**: ~11 new atomic counters in `ConnectionMetrics`
2. **Congestion Control**: ~5 locations to update (receive.go: 4, send.go: 1)
3. **Connection-Level**: ~3-5 locations to update (error handlers)
4. **Stats() Methods**: ~3 locations to update (aggregation logic)
5. **Prometheus Handler**: ~1 location to update (expose granular counters)

### Breaking Changes

**None** - Aggregate counters still work, granular counters are additive.

### Performance Impact

**Minimal** - Additional atomic increments (very fast), no new locks.

### Testing Requirements

1. Verify granular counters increment correctly for each drop reason
2. Verify aggregate counter = sum of granular counters
3. Verify DATA vs control distinction works correctly
4. Verify Prometheus metrics expose granular counters correctly

## Alternative: Keep Current + Add Helper Function

If the scope is too large, we could:
1. Keep current aggregate counters
2. Add a helper function that increments both granular + aggregate
3. Gradually migrate drop points to use the helper
4. This allows incremental adoption without breaking changes

## Decision Needed

**Questions to Answer**:
1. Do we want granular counters for ALL drop reasons, or just congestion control?
2. How important is DATA vs control distinction for connection-level errors?
3. Should we implement all at once, or incrementally?

**Recommendation**: Start with **Option C (Hybrid)** - implement granular counters for congestion control first (most common drops), then enhance connection-level to distinguish DATA vs control. This provides immediate debugging value with manageable scope.

