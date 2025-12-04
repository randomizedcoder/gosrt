# Drop and Loss Counters Implementation Review

## Summary

After reviewing the implementation against the corrected definitions in `packet_loss_drop_definitions.md`, here's the status:

## ✅ Correctly Implemented

### Loss Counters
- **`CongestionRecvPktLoss`**: ✅ Correctly incremented when receiver detects gaps (before sending NAK)
- **`CongestionSendPktLoss`**: ✅ Correctly incremented when sender receives NAK (for all packets in NAK list)

### Drop Counters - Congestion Control
- **`CongestionRecvPktDrop`**: ✅ Correctly incremented for:
  - Too old (belated, past play time)
  - Already acknowledged
  - Duplicate packets
  - Packet store insert failures
- **`CongestionSendPktDrop`**: ✅ Correctly incremented for:
  - Too old (exceed drop threshold)

## ⚠️ Gap Identified

### Send Path Errors Not in `PktSendDrop`

**Issue**: Serialization errors and io_uring failures are tracked in connection-level error counters but NOT included in `PktSendDrop` when reading SRT statistics.

**Current Implementation**:
- `connection.go:Stats()` reads `PktSendDrop` from `CongestionSendPktDrop` only
- Serialization errors tracked in `PktSentErrorMarshal` (connection-level)
- io_uring failures tracked in `PktSentRingFull`, `PktSentErrorSubmit` (connection-level)
- These are NOT aggregated into `PktSendDrop`

**SRT Specification**:
- `PktSendDrop`: "The total number of dropped by the SRT sender DATA packets that have no chance to be delivered in time"
- This definition suggests ALL drops should be included (too old, errors, etc.)

**Options**:

1. **Keep Separate** (Current Approach):
   - `CongestionSendPktDrop` = congestion control drops (too old)
   - Connection-level error counters = pre-congestion-control errors (marshal, io_uring)
   - **Pros**: Clear separation, matches implementation structure
   - **Cons**: `PktSendDrop` doesn't match SRT spec definition (should include all drops)

2. **Aggregate in Stats()**:
   - `PktSendDrop = CongestionSendPktDrop + PktSentErrorMarshal + PktSentRingFull + PktSentErrorSubmit` (for DATA packets only)
   - **Pros**: Matches SRT spec definition
   - **Cons**: Need to ensure we only count DATA packets, not control packets

3. **Also Increment CongestionSendPktDrop**:
   - Increment `CongestionSendPktDrop` for DATA packet serialization/io_uring errors
   - **Pros**: Single source of truth
   - **Cons**: Mixes congestion control with connection-level errors

**Recommendation**: Option 2 - Aggregate in `Stats()` but only for DATA packets. This matches the SRT spec while maintaining clear separation in the implementation.

## Current Counter Structure

### Aggregate Counters (SRT Spec Compliant)
- `CongestionRecvPktDrop` - All receiver drops (aggregate)
- `CongestionSendPktDrop` - Congestion control drops only (missing send path errors)
- `CongestionRecvPktLoss` - All receiver-detected losses (aggregate)
- `CongestionSendPktLoss` - All sender-reported losses (aggregate)

### Granular Error Counters (Additional Visibility)
- `CongestionRecvPktNil` - Nil packets
- `CongestionRecvPktStoreInsertFailed` - Store insert failures
- `PktSentErrorMarshal` - Serialization errors (connection-level)
- `PktSentRingFull` - io_uring ring full (connection-level)
- `PktSentErrorSubmit` - io_uring submit errors (connection-level)
- `PktSentErrorIoUring` - io_uring completion errors (connection-level)

## Action Items

1. ✅ **Verify loss counters** - DONE
   - Both receiver and sender loss counters correctly implemented

2. ✅ **Verify congestion control drop counters** - DONE
   - Receiver: All drop scenarios covered
   - Sender: Too-old drops covered

3. ⚠️ **Review send path error aggregation** - NEEDS DECISION
   - Should `PktSendDrop` include serialization/io_uring errors?
   - If yes, implement aggregation in `connection.go:Stats()`
   - If no, document the rationale

4. ✅ **Update comments** - DONE
   - Fixed outdated comments about PktLoss calculation

5. ✅ **Update progress document** - DONE
   - Documented corrected definitions
   - Noted implementation status

