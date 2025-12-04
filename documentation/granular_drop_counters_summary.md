# Granular Drop Counters - Summary and Recommendation

## Current Situation

**Aggregate Counters** (SRT RFC compliant):
- `CongestionRecvPktDrop` - All receiver drops (aggregate)
- `CongestionSendPktDrop` - Congestion control drops only (missing send path errors)

**Granular Counters** (Partial):
- `CongestionRecvPktNil` - Nil packets
- `CongestionRecvPktStoreInsertFailed` - Store insert failures
- Connection-level error counters (but don't distinguish DATA vs control)

## User Requirements

1. ✅ **Granular counters** for each drop reason (for debugging)
2. ✅ **Aggregate counter** that sums all granular (for SRT RFC compliance)
3. ✅ **DATA vs Control distinction** (control drops are more serious)
4. ⚠️ **Minimize scope** - careful consideration before changes

## Recommended Approach: Incremental Implementation

### Phase 1: Congestion Control Granular Counters (Low Risk, High Value)

**Scope**: Only congestion control layer (DATA packets only)

**New Counters** (5 counters):
```go
CongestionRecvDataDropTooOld        atomic.Uint64 // Belated, past play time
CongestionRecvDataDropAlreadyAcked  atomic.Uint64 // Already acknowledged
CongestionRecvDataDropDuplicate     atomic.Uint64 // Duplicate (already in store)
CongestionRecvDataDropStoreInsertFailed atomic.Uint64 // Store insert failed
CongestionSendDataDropTooOld        atomic.Uint64 // Exceed drop threshold
```

**Changes Required**:
- `metrics/metrics.go`: Add 5 new fields
- `congestion/live/receive.go`: Update 4 drop points (increment granular + aggregate)
- `congestion/live/send.go`: Update 1 drop point (increment granular + aggregate)
- `congestion/live/receive.go:Stats()`: Calculate aggregate from granular
- `congestion/live/send.go:Stats()`: Calculate aggregate from granular
- `connection.go:Stats()`: Use aggregate (no change needed)

**Estimated Effort**: 2-3 hours
**Risk**: Low (isolated to congestion control, no breaking changes)

### Phase 2: Connection-Level DATA vs Control Split (Medium Risk, Medium Value)

**Scope**: Connection-level error handlers

**New Counters** (6 counters):
```go
PktSentDataErrorMarshal    atomic.Uint64 // DATA packet marshal errors
PktSentControlErrorMarshal atomic.Uint64 // Control packet marshal errors
PktSentDataRingFull        atomic.Uint64 // DATA packet ring full
PktSentControlRingFull     atomic.Uint64 // Control packet ring full
PktSentDataErrorSubmit     atomic.Uint64 // DATA packet submit errors
PktSentControlErrorSubmit  atomic.Uint64 // Control packet submit errors
```

**Changes Required**:
- `metrics/metrics.go`: Add 6 new fields
- `metrics/packet_classifier.go`: Update `IncrementSendMetrics` to check packet type and increment appropriate counter
- `connection_linux.go`: Update error handlers to use new granular counters
- `connection.go:Stats()`: Aggregate DATA packet errors into `PktSendDrop`

**Estimated Effort**: 3-4 hours
**Risk**: Medium (touches connection-level code, need to ensure packet type detection is correct)

### Phase 3: Prometheus Metrics (Low Risk, Low Value)

**Scope**: Expose granular counters in Prometheus

**Changes Required**:
- `metrics/handler.go`: Add granular counter exports with labels

**Estimated Effort**: 1-2 hours
**Risk**: Low (additive only)

## Total Scope Estimate

**Option 1: Phase 1 Only** (Congestion Control Granular)
- Effort: 2-3 hours
- Risk: Low
- Value: High (covers most common drops)
- **Recommendation**: Start here

**Option 2: Phases 1 + 2** (Full Granularity)
- Effort: 5-7 hours
- Risk: Medium
- Value: Very High (complete visibility)
- **Recommendation**: If Phase 1 goes well, proceed to Phase 2

**Option 3: All Phases**
- Effort: 6-9 hours
- Risk: Medium
- Value: Very High + Prometheus visibility

## Implementation Strategy

### Helper Function Approach (Recommended)

Create helper functions to increment both granular + aggregate atomically:

```go
// In metrics/helpers.go
func IncrementRecvDataDrop(m *ConnectionMetrics, reason string, pktLen uint64) {
    if m == nil {
        return
    }

    // Increment granular counter
    switch reason {
    case "too_old":
        m.CongestionRecvDataDropTooOld.Add(1)
    case "already_acked":
        m.CongestionRecvDataDropAlreadyAcked.Add(1)
    case "duplicate":
        m.CongestionRecvDataDropDuplicate.Add(1)
    case "store_insert_failed":
        m.CongestionRecvDataDropStoreInsertFailed.Add(1)
    }

    // Always increment aggregate
    m.CongestionRecvPktDrop.Add(1)
    m.CongestionRecvByteDrop.Add(pktLen)
}

func IncrementSendDataDrop(m *ConnectionMetrics, reason string, pktLen uint64) {
    // Similar for sender
}
```

**Benefits**:
- Single point of change for aggregation logic
- Ensures consistency (granular + aggregate always in sync)
- Easy to add new drop reasons later
- Can be adopted incrementally

## Decision Matrix

| Option | Effort | Risk | Value | DATA/Control | Recommendation |
|--------|--------|------|-------|--------------|----------------|
| Phase 1 Only | 2-3h | Low | High | No (DATA only) | ✅ **Start Here** |
| Phase 1 + 2 | 5-7h | Medium | Very High | Yes | ✅ **If Phase 1 succeeds** |
| All Phases | 6-9h | Medium | Very High | Yes | Consider after Phase 2 |

## Questions for User

1. **Start with Phase 1 only?** (Congestion control granular counters)
   - Low risk, high value, covers most common drops
   - Can evaluate before proceeding

2. **Or proceed with Phases 1 + 2 together?** (Full granularity)
   - More complete, but larger scope
   - Need to ensure packet type detection is correct

3. **Helper function approach?** (Recommended)
   - Ensures granular + aggregate stay in sync
   - Easier to maintain

## Next Steps

1. **If approved**: Implement Phase 1 (congestion control granular counters)
2. **After Phase 1**: Evaluate and decide on Phase 2
3. **Documentation**: Update design docs with final approach

