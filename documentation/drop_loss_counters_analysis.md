# Drop and Loss Counters Analysis

## Current Implementation Status

### Summary

We have **aggregate counters** for drops and losses, not separate counters for each drop reason. This is acceptable for the current implementation, but we should verify that all drop/loss scenarios are correctly categorized.

### Current Counters

#### Loss Counters (Correctly Implemented)
- ✅ `CongestionRecvPktLoss` - Incremented when receiver detects gaps (before sending NAK)
- ✅ `CongestionSendPktLoss` - Incremented when sender receives NAK (for all packets in NAK list)

#### Drop Counters (Aggregate)
- ✅ `CongestionRecvPktDrop` - Aggregates all receiver drop reasons:
  - Too old (belated, past play time)
  - Already acknowledged
  - Duplicate packets
  - Packet store insert failures
- ✅ `CongestionSendPktDrop` - Aggregates all sender drop reasons:
  - Too old (exceed drop threshold)
  - Serialization errors (marshal failures) - **NOT YET IMPLEMENTED**
  - io_uring submission failures (ring full, submit errors) - **NOT YET IMPLEMENTED**

#### Additional Specific Error Counters
- ✅ `CongestionRecvPktNil` - Nil packets received
- ✅ `CongestionRecvPktStoreInsertFailed` - Packet store insertion failures (also counted in PktDrop)
- ✅ `CongestionRecvDeliveryFailed` - Delivery callback failures (not yet used)
- ✅ `CongestionSendDeliveryFailed` - Delivery callback failures (not yet used)
- ✅ `CongestionSendNAKNotFound` - NAK requests for packets not in lossList (not yet used)

### Implementation Verification

#### Receiver Side (`congestion/live/receive.go`)

**Loss Increments** (✅ Correct):
- Line 359: `CongestionRecvPktLoss.Add(len)` - When gaps detected (before sending NAK)

**Drop Increments** (✅ Correct):
- Line 283: `CongestionRecvPktDrop.Add(1)` - Too old (belated)
- Line 298: `CongestionRecvPktDrop.Add(1)` - Already acknowledged
- Line 315: `CongestionRecvPktDrop.Add(1)` - Duplicate (already in packet store)
- Line 341: `CongestionRecvPktDrop.Add(1)` - Store insert failed (duplicate after Has check)

**Additional Counters**:
- Line 210: `CongestionRecvPktNil.Add(1)` - Nil packet
- Line 340: `CongestionRecvPktStoreInsertFailed.Add(1)` - Store insert failed

#### Sender Side (`congestion/live/send.go`)

**Loss Increments** (✅ Correct):
- Line 462: `CongestionSendPktLoss.Add(totalLossCount)` - When NAK received (for all packets in NAK list)

**Drop Increments** (✅ Correct):
- Line 330: `CongestionSendPktDrop.Add(1)` - Too old (exceed drop threshold)

**Missing Drop Increments** (⚠️ Partially Implemented):
- Serialization errors (marshal failures) - Tracked in `PktSentErrorMarshal` (connection-level), but NOT in `CongestionSendPktDrop`
- io_uring submission failures (ring full, submit errors) - Tracked in `PktSentRingFull` and `PktSentErrorSubmit` (connection-level), but NOT in `CongestionSendPktDrop`
- **Note**: According to SRT spec, `PktSendDrop` should include ALL drops by sender (too old, errors, etc.). Currently, `PktSendDrop` in `connection.go:Stats()` only reads from `CongestionSendPktDrop` (congestion control drops), missing serialization/io_uring errors.
- **Recommendation**: Either aggregate connection-level error counters with congestion control drops in `Stats()`, or also increment `CongestionSendPktDrop` for DATA packet serialization/io_uring errors (but be careful to only count DATA packets, not control packets).

### Recommendations

#### Option 1: Keep Aggregate Counters (Current Approach)
**Pros**:
- Simple, low overhead
- Matches SRT specification (single `PktSendDrop` and `PktRecvDrop` counters)
- Sufficient for most use cases

**Cons**:
- Less granular visibility into drop reasons
- Need to check logs or additional counters to understand why packets were dropped

#### Option 2: Add Granular Drop Reason Counters
**Pros**:
- Better visibility into specific drop reasons
- Can identify specific issues (e.g., too many "too old" drops vs "duplicate" drops)

**Cons**:
- More counters to maintain
- More complex Prometheus metrics (would need labels)
- Doesn't match SRT specification exactly (spec has single drop counter)

**Recommendation**: **Keep aggregate counters** for now, but ensure all drop scenarios are correctly incrementing `CongestionSendPktDrop` and `CongestionRecvPktDrop`. The additional specific error counters (`CongestionRecvPktNil`, `CongestionRecvPktStoreInsertFailed`, etc.) provide some granularity without breaking the SRT spec.

### Action Items

1. ✅ **Verify loss counters are correct** - DONE
   - `CongestionRecvPktLoss` incremented when gaps detected (before NAK)
   - `CongestionSendPktLoss` incremented when NAK received (all packets in NAK)

2. ✅ **Verify drop counters are correct** - DONE
   - `CongestionRecvPktDrop` incremented for all receiver drop scenarios
   - `CongestionSendPktDrop` incremented for too-old packets

3. ⚠️ **Verify drop counter aggregation** - NEEDS REVIEW
   - Current: `PktSendDrop` in `connection.go:Stats()` only reads from `CongestionSendPktDrop` (congestion control drops)
   - Issue: Serialization errors and io_uring failures are tracked in connection-level error counters (`PktSentErrorMarshal`, `PktSentRingFull`, `PktSentErrorSubmit`) but NOT included in `PktSendDrop`
   - Question: Should `PktSendDrop` aggregate all sender drops (congestion control + errors), or keep them separate?
   - SRT Spec: "The total number of dropped by the SRT sender DATA packets that have no chance to be delivered in time" - suggests ALL drops should be included
   - **Action**: Review if we should aggregate connection-level error counters with congestion control drops in `Stats()`, or add separate counters for pre-congestion-control drops

4. ✅ **Update comments** - DONE
   - Fixed outdated comment in `tickDropOldPackets()` about PktLoss calculation

5. ✅ **Update progress document** - TODO
   - Document the corrected definitions
   - Note any missing implementations

