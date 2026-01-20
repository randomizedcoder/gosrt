# Client and Connection Statistics Design

**Created**: 2024-12-08
**Related**: [metrics_and_statistics_design.md](metrics_and_statistics_design.md)

## Overview

This document clarifies how statistics propagate from the GoSRT library to the client and client-generator applications, identifying the intended design vs. current implementation issues.

---

## Intended Architecture (Code Reuse)

The design goal was for **all components** (server, client, client-generator) to use the **same** statistics infrastructure:

```
┌─────────────────────────────────────────────────────────────────┐
│                     Shared Statistics Infrastructure              │
├─────────────────────────────────────────────────────────────────┤
│  metrics/metrics.go       - ConnectionMetrics struct (atomics)   │
│  metrics/packet_classifier.go - Increment functions             │
│  metrics/drop_reasons.go  - Drop reason enum                    │
│  metrics/handler.go       - Prometheus HTTP handler             │
│  metrics/registry.go      - Connection registration             │
└─────────────────────────────────────────────────────────────────┘
                              ↑
                    Used by all components:
                              │
         ┌────────────────────┼────────────────────┐
         │                    │                    │
    ┌────▼────┐         ┌────▼────┐         ┌────▼────┐
    │ Server  │         │ Client  │         │ Client- │
    │         │         │         │         │Generator│
    └─────────┘         └─────────┘         └─────────┘
```

### Key Design Principles

1. **Single Source of Truth**: Each SRT connection (`srtConn`) has ONE `c.metrics` object
2. **Automatic Registration**: When connection is created, metrics are registered
3. **Atomic Access**: All counters use `atomic.Uint64` for lock-free reads
4. **Prometheus Export**: Same handler exports metrics for all components

---

## How It Currently Works

### Connection Metrics Lifecycle

```go
// In connection.go, newSRTConn():

// 1. Create metrics object
c.metrics = &metrics.ConnectionMetrics{
    HandlePacketLockTiming: &metrics.LockTimingMetrics{},
    ReceiverLockTiming:     &metrics.LockTimingMetrics{},
    SenderLockTiming:       &metrics.LockTimingMetrics{},
}

// 2. Register with global registry
metrics.RegisterConnection(c.socketId, c.metrics)

// 3. Metrics are incremented during connection lifetime
// - packet_classifier.go increments on recv/send
// - congestion control increments on loss detection, retransmission
// - handleNAK/sendNAK increment NAK counters

// 4. Prometheus handler exports via metrics.GetConnections()
```

### For Throughput Display (200ms Updates)

The intended flow:
```
1. Application establishes SRT connection
2. Connection creates and registers c.metrics with socket ID
3. Application stores socket ID
4. Throughput display queries metrics.GetConnections()
5. Gets the SAME c.metrics object that the connection updates
6. Reads counters via atomic.Load()
```

---

## Current Issues Identified

### Issue 1: Two Separate Metrics Objects

The client and client-generator applications create their OWN `clientMetrics` object:

```go
// In contrib/client-generator/main.go (WRONG approach):
clientMetrics := &metrics.ConnectionMetrics{}  // NEW object!

// This is NOT the connection's metrics!
// The connection creates its own c.metrics in newSRTConn()
```

**Result**: Application reads from `clientMetrics` which is never updated by the connection.

### Issue 2: Manual Counter Updates

The application manually updates some counters:
```go
clientMetrics.ByteSentDataSuccess.Add(uint64(written))
clientMetrics.PktSentDataSuccess.Add(1)
```

But counters like `PktRetransFromNAK`, `CongestionRecvPktLoss` are only updated on the connection's `c.metrics`.

### Issue 3: Recent Fix (Partial)

We recently fixed the throughput display to query the connection's metrics:
```go
if socketId := connSocketId.Load(); socketId != 0 {
    conns, _ := metrics.GetConnections()
    if connMetrics, ok := conns[socketId]; ok && connMetrics != nil {
        retrans = connMetrics.PktRetransFromNAK.Load()
    }
}
```

This works, but is more complex than needed.

---

## Correct Design (Proposed)

### Option A: Use Connection's Metrics Directly (Recommended)

After establishing the connection, get a reference to its metrics object:

```go
// In client-generator:
conn, err := srt.Dial(ctx, "srt", host, config, wg)
if err != nil {
    return err
}

// Get the connection's socket ID
socketId := conn.SocketId()

// Query the actual metrics object
conns, _ := metrics.GetConnections()
connMetrics := conns[socketId]  // This IS the connection's c.metrics

// Use connMetrics for throughput display
common.RunThroughputDisplay(ctx, STATS_PERIOD, func() (uint64, uint64, uint64, uint64, uint64) {
    return connMetrics.ByteSentDataSuccess.Load(),
           connMetrics.PktSentDataSuccess.Load(),
           connMetrics.PktSentDataSuccess.Load(),
           connMetrics.PktSentDataDropped.Load(),
           connMetrics.PktRetransFromNAK.Load()
})
```

### Option B: Add Metrics Accessor to Conn Interface

Add a method to the `srt.Conn` interface:

```go
// In srt package:
type Conn interface {
    // ... existing methods ...
    Metrics() *metrics.ConnectionMetrics
}

// In connection.go:
func (c *srtConn) Metrics() *metrics.ConnectionMetrics {
    return c.metrics
}
```

Then in applications:
```go
conn, _ := srt.Dial(...)
connMetrics := conn.Metrics()

// Use connMetrics directly
```

---

## Which Counters Exist Where?

### Updated by Packet Classifier (receive/send paths)
- `PktRecvDataSuccess`, `PktSentDataSuccess`
- `PktRecvACKSuccess`, `PktSentACKSuccess`
- etc.

### Updated by Congestion Control
- `CongestionRecvPkt`, `CongestionSendPkt`
- `CongestionRecvPktLoss`, `CongestionSendPktLoss`
- `CongestionRecvPktRetrans`, `CongestionSendPktRetrans`

### Updated by Connection NAK Handling (Phase 6 fix)
- `PktSentNAKSuccess` - in `sendNAK()`
- `PktRecvNAKSuccess` - in `handleNAK()`
- `PktRetransFromNAK` - in `handleNAK()` when retransmissions occur

### NOT Updated (still 0)
Some `Congestion*` counters may not be populated because the congestion control layer doesn't call the increment functions.

---

## Prometheus vs Throughput Display

| Source | Data Path | Update Frequency |
|--------|-----------|------------------|
| Prometheus | `metrics.GetConnections()` → Handler | On HTTP request |
| Throughput Display | Should use same path | Every 200ms |
| JSON Stats | `conn.Stats()` / `GetExtendedStatistics()` | On request |

All three should be reading from the SAME `c.metrics` object!

---

## Investigation: Why NAKsPerLostPacket = 0?

### Root Cause: FOUND AND FIXED

The issue was in `analysis.go:computeObservedStatistics()`:

```go
// BUG: This was using the WRONG component for NAKs!
sender := ComputeDerivedMetrics(ts.ClientGenerator)
receiver := ComputeDerivedMetrics(ts.Client)

stats.NAKsPerLostPacket = float64(receiver.TotalNAKsSent) / float64(lossCount)
// ^^^ receiver = Client, which does NOT send NAKs!
```

### Understanding the Data Flow

```
Client-Generator (sender) → Server (relay) → Client (receiver)
                    ↑             ↓
                    └─── NAKs ────┘
```

1. **Client-Generator** sends data TO the **Server**
2. **Server** detects packet loss (via sequence gaps)
3. **Server** sends NAKs BACK to **Client-Generator**
4. **Client-Generator** receives NAKs and retransmits
5. **Server** relays data to **Client**

So the NAKs are sent by the **SERVER**, not the Client!

### The Fix

Updated `analysis.go` to use the server's metrics for NAK counting:

```go
sender := ComputeDerivedMetrics(ts.ClientGenerator)
receiver := ComputeDerivedMetrics(ts.Client)
server := ComputeDerivedMetrics(ts.Server)  // NEW: Server is the NAK sender!

// Fixed: Use server's NAK count, not client's
stats.NAKsPerLostPacket = float64(server.TotalNAKsSent) / float64(lossCount)
```

### Evidence That Prometheus Metrics ARE Working

The test output showed:
- JSON stats: `pkt_sent_nak: 394` (from server connection)
- Throughput display: `408 retx` (at the end)

This confirms the metrics ARE being tracked - the analysis was just looking at the wrong component!

---

## Throughput Display: Understanding the Output

### Two Components, Two Displays

Both client and client-generator print throughput stats to stderr. They now have labels:
- `[PUB]` = Client-Generator (publisher/sender)
- `[SUB]` = Client (subscriber/receiver)

### Why `[SUB]` Shows 0 retx

In the relay topology:
```
Client-Generator → Server → Client
         ↑           ↓
         └── NAKs ───┘
```

1. **Loss occurs** between Client-Generator and Server
2. **Server detects** loss and sends NAKs to Client-Generator
3. **Client-Generator retransmits** to Server
4. **Server relays** (already-recovered) data to Client

The Client (`[SUB]`) receives **relayed** data from the Server. It doesn't participate in the retransmission process, so it correctly shows:
- `loss = 0` (no loss on Server → Client path)
- `retx = 0` (no retransmissions received)

### What to Look For

- `[PUB]` should show increasing `retx` during loss testing
- `[SUB]` will show `0 retx` (correct behavior)

---

## Action Items

1. **Verify `sendNAK()` increment**: Ensure the increment is on the correct connection
2. **Consider Option B**: Add `Metrics()` accessor to `srt.Conn` interface
3. **Remove duplicate metrics objects**: Don't create `clientMetrics` in applications
4. **Audit all counter increments**: Ensure all `Congestion*` counters are populated

---

## References

- [metrics_and_statistics_design.md](metrics_and_statistics_design.md) - Original metrics design
- [defect2_prometheus_metrics_audit_implementation.md](defect2_prometheus_metrics_audit_implementation.md) - Implementation progress

