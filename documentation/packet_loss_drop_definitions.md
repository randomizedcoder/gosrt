# Packet Loss vs. Packet Drop: Definitions and Implementation

## Overview

This document clarifies the distinction between **packet loss** and **packet drop** counters in the SRT protocol, based on the SRT RFC draft and the actual implementation requirements.

## Definitions

### Packet Loss (PktLoss)

**Loss** represents packets that are **detected as missing** by the receiver and reported to the sender via NAK (Negative Acknowledgement).

#### Receiver Side: `PktRecvLoss`
- **Definition**: "The total number of SRT DATA packets detected as presently missing (either reordered or lost) at the receiver side"
- **When incremented**:
  - When receiver detects gaps in sequence numbers (missing packets)
  - Before sending NAK to report the loss
  - For each missing packet in the gap
- **Location**: `congestion/live/receive.go:pushLocked()` when gaps are detected
- **Example**: Receiver receives seq 1, 2, 5, 6 → detects missing seq 3, 4 → increments `PktRecvLoss` by 2 → sends NAK with [3, 4]

#### Sender Side: `PktSendLoss`
- **Definition**: "The total number of data packets considered or reported as lost at the sender side. Does not correspond to the packets detected as lost at the receiver side."
- **When incremented**:
  - When sender receives a NAK from receiver
  - For each packet in the NAK list (each reported loss)
  - This represents packets that the receiver reported as lost
- **Location**: `congestion/live/send.go:nakLocked()` when processing NAK
- **Example**: Sender receives NAK with [3, 4] → increments `PktSendLoss` by 2 (one for each packet in NAK)

### Packet Drop (PktDrop)

**Drop** represents packets that are **intentionally discarded locally** in our receive/send pipeline for various reasons (too old, duplicate, errors, etc.).

#### Receiver Side: `PktRecvDrop`
- **Definition**: "The total number of dropped by the SRT receiver and, as a result, not delivered to the upstream application DATA packets"
- **When incremented**:
  - Packets too old (belated, past play time)
  - Packets already acknowledged (duplicate ACK)
  - Duplicate packets (already in packet store)
  - Packet store insert failures
  - Any other local discard in receive pipeline
- **Location**: `congestion/live/receive.go:pushLocked()` in various drop scenarios

#### Sender Side: `PktSendDrop`
- **Definition**: "The total number of dropped by the SRT sender DATA packets that have no chance to be delivered in time"
- **When incremented**:
  - Packets too old (exceed drop threshold, TLPKTDROP)
  - Serialization errors (marshal failures)
  - io_uring submission failures (ring full, submit errors)
  - Any other local discard in send pipeline
- **Location**:
  - `congestion/live/send.go:tickDropOldPackets()` for too-old packets
  - Send path error handlers for serialization/submission failures

## Key Distinctions

1. **Loss is remote detection**: Loss counters track packets that the **receiver** detected as missing and reported via NAK
2. **Drop is local discard**: Drop counters track packets that **we** (sender or receiver) intentionally discarded locally
3. **Loss triggers retransmission**: When sender receives NAK (loss report), it retransmits those packets
4. **Drop does not trigger retransmission**: Dropped packets are discarded and not retransmitted

## Current Implementation Issues

### Issue 1: Sender `PktSendLoss` Not Incremented on NAK

**Problem**: In `congestion/live/send.go:nakLocked()`, when processing NAK packets, we increment `PktRetrans` but **not** `PktSendLoss`.

**Fix Required**: Increment `PktSendLoss` for each packet in the NAK list (each reported loss).

### Issue 2: Incorrect Calculation of `PktLoss = PktDrop + PktRetrans`

**Problem**: In `congestion/live/send.go:Stats()`, we calculate `PktLoss = PktDrop + PktRetrans`, which is incorrect.

**Why it's wrong**:
- `PktDrop` = local drops (too old, errors) - these are NOT losses
- `PktRetrans` = retransmitted packets - these are responses to losses, not losses themselves
- `PktLoss` should be incremented when NAK is received (packets reported as lost)

**Fix Required**:
- Remove the calculation `PktLoss = PktDrop + PktRetrans`
- Increment `PktSendLoss` directly in `nakLocked()` for each packet in NAK list
- Read `PktSendLoss` directly from atomic counter in `Stats()`

## Correct Implementation Pattern

### Receiver Side

```go
// In pushLocked() when gaps detected:
if pkt.Header().PacketSequenceNumber.Gt(r.maxSeenSequenceNumber.Inc()) {
    // Gap detected - missing packets
    gapSize := uint64(pkt.Header().PacketSequenceNumber.Distance(r.maxSeenSequenceNumber))

    // Send NAK to report loss
    r.sendNAK([]circular.Number{
        r.maxSeenSequenceNumber.Inc(),
        pkt.Header().PacketSequenceNumber.Dec(),
    })

    // Increment loss counter (receiver detected loss)
    m.CongestionRecvPktLoss.Add(gapSize)
    m.CongestionRecvByteLoss.Add(gapSize * uint64(r.avgPayloadSize))
}

// In pushLocked() when dropping packets:
if pkt.Header().PacketSequenceNumber.Lte(r.lastDeliveredSequenceNumber) {
    // Too old - drop locally
    m.CongestionRecvPktDrop.Add(1)
    m.CongestionRecvByteDrop.Add(uint64(pktLen))
}
```

### Sender Side

```go
// In nakLocked() when processing NAK:
func (s *sender) nakLocked(sequenceNumbers []circular.Number) uint64 {
    for i := 0; i < len(sequenceNumbers); i += 2 {
        start := sequenceNumbers[i]
        end := sequenceNumbers[i+1]

        // Count packets in this NAK range
        lossCount := uint64(end.Distance(start)) + 1

        // Increment loss counter (packets reported as lost by receiver)
        m.CongestionSendPktLoss.Add(lossCount)
        m.CongestionSendByteLoss.Add(lossCount * avgPacketSize)

        // Retransmit packets...
        // Increment retrans counter...
    }
}

// In tickDropOldPackets() when dropping too-old packets:
if p.Header().PktTsbpdTime+s.dropThreshold <= now {
    // Too old - drop locally (NOT a loss, just a local drop)
    m.CongestionSendPktDrop.Add(1)
    m.CongestionSendByteDrop.Add(uint64(p.Len()))
}
```

## Summary Table

| Counter | Side | When Incremented | Example |
|---------|------|------------------|---------|
| `PktRecvLoss` | Receiver | Gap detected, before sending NAK | Receive seq 1,2,5,6 → detect missing 3,4 → increment by 2 |
| `PktSendLoss` | Sender | NAK received, for each packet in NAK | Receive NAK [3,4] → increment by 2 |
| `PktRecvDrop` | Receiver | Local drop (too old, duplicate, etc.) | Packet too old → increment by 1 |
| `PktSendDrop` | Sender | Local drop (too old, error, etc.) | Packet too old → increment by 1 |
| `PktRetrans` | Sender | Packet retransmitted (response to NAK) | Retransmit packet → increment by 1 |

## References

- SRT RFC Draft: `draft-sharabayko-srt-01.txt`
- SRT Statistics API: `statistics.go`
- Section 4.8.2: Packet Retransmission (NAKs)
- Section 4.6: Too-Late Packet Drop

