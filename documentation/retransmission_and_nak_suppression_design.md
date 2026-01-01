# Retransmisson and NAK Suppression Design

## Document Status
- **Created:** December 31, 2025
- **Status:** Draft - Implementation Planning
- **Related:** [parallel_tests_defects.md](./parallel_tests_defects.md), [design_nak_btree.md](./design_nak_btree.md), [metrics_and_statistics_design.md](./metrics_and_statistics_design.md)

---

## 1. Problem Statement

### 1.1 Observed Behavior

During parallel comparison testing (see `parallel_tests_defects.md`), we observed significant retransmission inefficiency in the HighPerf pipeline when RTT exceeds the Periodic NAK Interval:

| Test | Loss | RTT | HP Retx Sent | HP Retx Recv | Ratio | Discrepancy |
|------|------|-----|--------------|--------------|-------|-------------|
| **Clean** | 0% | 0ms | 0 | 0 | **1.00x** | **0.0%** ✓ |
| **NoLatency** | 10% | ~0ms | 24,063 | 15,395 | **1.56x** | 36.0% |
| **Continental** | 5% | 60ms | 18,928 | 7,642 | **2.48x** | 59.6% |
| **GEO** | 5% | 300ms | 25,576 | 10,293 | **2.48x** | 59.8% |

**Key Finding:** The sender transmits 1.56x-2.48x more retransmissions than the receiver actually needs.

### 1.2 Root Cause Analysis

The issue stems from the interaction between:
1. **Periodic NAK interval:** 20ms (configurable via `PeriodicNakIntervalMs`)
2. **Network RTT:** 60ms-300ms+ in realistic networks
3. **NAK btree design:** Sends the FULL btree content every periodic NAK

**Timeline Example (300ms RTT, 20ms NAK interval):**
```
t=0ms:   Gap detected for seq 1000, inserted into NAK btree
t=0ms:   Periodic NAK fires, sends NAK containing [1000]
t=20ms:  Periodic NAK fires, seq 1000 still in btree (retransmit not arrived yet)
         → Sends NAK containing [1000] again
t=40ms:  Periodic NAK fires → Sends NAK containing [1000] again
...
t=140ms: Sender receives first NAK (t=0ms NAK + 150ms one-way delay)
t=140ms: Sender retransmits seq 1000
t=160ms: Periodic NAK fires → Sends NAK containing [1000] (15th time)
t=290ms: Retransmit arrives at receiver, seq 1000 deleted from NAK btree

Result: 15 NAK packets sent for ONE gap = potential 15 duplicate retransmits!
```

### 1.3 Why Baseline Doesn't Have This Issue

The Baseline pipeline uses **immediate NAK** (one NAK per gap, sent once immediately). The sender retransmits once, and the retransmit typically arrives before any duplicate NAK could be sent.

The Baseline does have periodicNAK, but the full NAK being sent is for a single range.

---

## 2. SRT Protocol Background

### 2.1 ACK Control Packet (Full ACK)

```
    0                   1                   2                   3
    0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
   +-+-+-+-+-+-+-+-+-+-+-+-+- SRT Header +-+-+-+-+-+-+-+-+-+-+-+-+-+
   |1|        Control Type         |           Reserved            |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                    Acknowledgement Number                     |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                           Timestamp                           |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                     Destination Socket ID                     |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |            Last Acknowledged Packet Sequence Number           |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                              RTT                              |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                          RTT Variance                         |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                     Available Buffer Size                     |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                     Packets Receiving Rate                    |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                     Estimated Link Capacity                   |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                         Receiving Rate                        |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

**Key Fields:**
- **Last Acknowledged Packet Sequence Number:** The sequence number of the first unacknowledged packet (last ACK'd + 1)
- **RTT:** Round-trip time in microseconds, estimated by the receiver
- **RTT Variance:** Variance of the RTT estimate in microseconds

**ACK Types:**
- **Full ACK:** Sent every 10ms, contains all fields including RTT
- **Light ACK:** Contains only Last Acknowledged Packet Sequence Number (sent every 64 packets)
- **Small ACK:** Contains fields up to Available Buffer Size

### 2.2 NAK Control Packet

```
    0                   1                   2                   3
    0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
   +-+-+-+-+-+-+-+-+-+-+-+-+- SRT Header +-+-+-+-+-+-+-+-+-+-+-+-+-+
   |1|        Control Type         |           Reserved            |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                   Type-specific Information                   |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                           Timestamp                           |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |                     Destination Socket ID                     |
   +-+-+-+-+-+-+-+-+-+-+-+-+- CIF (Loss List) -+-+-+-+-+-+-+-+-+-+-+
   |0|                 Lost packet sequence number                 |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |1|         Range of lost packets from sequence number          |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |0|                    Up to sequence number                    |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

**Key Fields:**
- **Timestamp:** When the NAK was sent (microseconds since connection start)
- **Loss List:** Single sequence numbers or ranges of lost packets

### 2.3 ACKACK Control Packet

The sender returns an ACKACK immediately upon receiving a Full ACK. This enables:
1. **Receiver RTT calculation:** Receiver measures time from sending ACK to receiving ACKACK
2. **Sender ACK confirmation:** Sender knows the ACK was received

### 2.4 RTT Updates in gosrt

**Receiver updates RTT:**
- When ACKACK arrives: `connection_handlers.go:384` → `recalculateRTT()`

**Sender updates RTT:**
- When Full ACK arrives with RTT field: `connection_handlers.go:282` → `recalculateRTT()`

Both sides have accurate, frequently-updated RTT information (every 10ms for Full ACK cycles).

---

## 3. Control Packet Handling Architecture

Understanding how control packets are processed is critical for designing retransmission suppression.

### 3.1 Packet Routing Overview

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                      PACKET ROUTING ARCHITECTURE                             │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  UDP Socket                                                                  │
│       │                                                                      │
│       ▼                                                                      │
│  ┌─────────────────────────────────────────────────────────────────────────┐ │
│  │ io_uring recv / listen_*.go / dial_*.go                                 │ │
│  │                                                                         │ │
│  │  Packet arrives → handlePacketDirect() or conn.push()                   │ │
│  └─────────────────────────────────────────────────────────────────────────┘ │
│       │                                                                      │
│       ▼                                                                      │
│  ┌─────────────────────────────────────────────────────────────────────────┐ │
│  │ handlePacket() [connection_handlers.go:89]                              │ │
│  │                                                                         │ │
│  │  if header.IsControlPacket {                                            │ │
│  │      handler := c.controlHandlers[header.ControlType]  // O(1) lookup   │ │
│  │      handler(c, p)  // SYNCHRONOUS dispatch                             │ │
│  │  } else {                                                               │ │
│  │      c.recv.Push(p)  // Data packet → receiver path                     │ │
│  │  }                                                                      │ │
│  └─────────────────────────────────────────────────────────────────────────┘ │
│       │                           │                                          │
│       │ Control Packets           │ Data Packets                             │
│       ▼                           ▼                                          │
│  ┌──────────────────────┐   ┌──────────────────────────────────────────────┐ │
│  │ SYNCHRONOUS handlers │   │ receiver.Push() [push.go]                    │ │
│  │                      │   │                                              │ │
│  │ • handleACK()        │   │ if usePacketRing:                            │ │
│  │ • handleNAK()        │   │     pushToRing() → lock-free ring            │ │
│  │ • handleACKACK()     │   │ else:                                        │ │
│  │ • handleKeepAlive()  │   │     pushWithLock() → btree (with lock)       │ │
│  │ • handleShutdown()   │   │                                              │ │
│  └──────────────────────┘   └──────────────────────────────────────────────┘ │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Control Packet Dispatch Table

From `connection_handlers.go:45-54`:
```go
func (c *srtConn) initializeControlHandlers() {
    c.controlHandlers = map[packet.CtrlType]controlPacketHandler{
        packet.CTRLTYPE_KEEPALIVE: (*srtConn).handleKeepAlive,
        packet.CTRLTYPE_SHUTDOWN:  (*srtConn).handleShutdown,
        packet.CTRLTYPE_NAK:       (*srtConn).handleNAK,
        packet.CTRLTYPE_ACK:       (*srtConn).handleACK,
        packet.CTRLTYPE_ACKACK:    (*srtConn).handleACKACK,
        packet.CTRLTYPE_USER:      (*srtConn).handleUserPacket,
    }
}
```

**Key Point:** Control packets are dispatched **synchronously** via O(1) map lookup. The handler runs immediately on the packet arrival goroutine.

#### 3.2.1 Map vs Switch Performance Consideration

The current implementation uses a `map[packet.CtrlType]controlPacketHandler` for dispatch. Given the small number of control packet types (6 types), a `switch` statement may outperform the map due to:

1. **No hashing overhead** - Switch uses direct comparison
2. **Branch prediction** - CPU can predict common cases (ACK, NAK, ACKACK)
3. **No indirection** - Direct function call vs map lookup + function pointer

**Proposed Benchmark:**

```go
// connection_handlers_bench_test.go

// Package-level sink variables to prevent compiler optimization.
// Without these, the compiler may eliminate the code being benchmarked
// because the results are never used.
var (
    sinkHandler func(packet.Packet)
    sinkInt     int
)

// numTestPackets is the number of pre-generated packets.
// Must be large enough to defeat branch predictor (typically 4K-16K entries).
// Power of 2 allows fast modulo via bitmask.
const numTestPackets = 4096

// createMixedControlPackets generates a realistic mix of control packets.
// Distribution based on typical SRT traffic patterns:
//   - ACK: ~40% (sent every 10ms + light ACKs)
//   - ACKACK: ~35% (response to each ACK)
//   - Keepalive: ~15% (every 1s, but 2x for bidirectional)
//   - NAK: ~8% (only on packet loss)
//   - Shutdown: ~1% (rare)
//   - User: ~1% (application-specific)
func createMixedControlPackets() []packet.Packet {
    packets := make([]packet.Packet, numTestPackets)

    // Pre-computed distribution (cumulative)
    // 0-39: ACK, 40-74: ACKACK, 75-89: Keepalive, 90-97: NAK, 98: Shutdown, 99: User
    types := []packet.ControlType{
        packet.CTRLTYPE_ACK,       // 40%
        packet.CTRLTYPE_ACKACK,    // 35%
        packet.CTRLTYPE_KEEPALIVE, // 15%
        packet.CTRLTYPE_NAK,       // 8%
        packet.CTRLTYPE_SHUTDOWN,  // 1%
        packet.CTRLTYPE_USER,      // 1%
    }
    weights := []int{40, 35, 15, 8, 1, 1}  // Must sum to 100

    // Build weighted distribution
    var cumulative []int
    sum := 0
    for _, w := range weights {
        sum += w
        cumulative = append(cumulative, sum)
    }

    // Use deterministic seed for reproducible benchmarks
    rng := rand.New(rand.NewSource(42))

    for i := 0; i < numTestPackets; i++ {
        r := rng.Intn(100)
        var ctrlType packet.ControlType
        for j, c := range cumulative {
            if r < c {
                ctrlType = types[j]
                break
            }
        }
        packets[i] = createTestControlPacket(ctrlType)
    }

    return packets
}

func createTestControlPacket(ctrlType packet.ControlType) packet.Packet {
    p := packet.NewPacket(nil)
    p.Header().IsControlPacket = true
    p.Header().ControlType = ctrlType
    return p
}

// BenchmarkControlDispatchMap benchmarks map-based dispatch with realistic
// packet mix to defeat branch predictor.
func BenchmarkControlDispatchMap(b *testing.B) {
    c := &srtConn{}
    c.initializeControlHandlers()
    packets := createMixedControlPackets()
    mask := numTestPackets - 1  // Fast modulo for power of 2

    var handler func(packet.Packet)
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        p := packets[i&mask]  // Cycle through packets
        handler = c.controlHandlers[p.Header().ControlType]
    }
    sinkHandler = handler
}

// BenchmarkControlDispatchSwitch benchmarks switch-based dispatch with realistic
// packet mix to defeat branch predictor.
func BenchmarkControlDispatchSwitch(b *testing.B) {
    packets := createMixedControlPackets()
    mask := numTestPackets - 1

    var result int
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        p := packets[i&mask]
        switch p.Header().ControlType {
        case packet.CTRLTYPE_ACK:
            result = 1
        case packet.CTRLTYPE_NAK:
            result = 2
        case packet.CTRLTYPE_ACKACK:
            result = 3
        case packet.CTRLTYPE_KEEPALIVE:
            result = 4
        case packet.CTRLTYPE_SHUTDOWN:
            result = 5
        case packet.CTRLTYPE_USER:
            result = 6
        default:
            result = 0
        }
    }
    sinkInt = result
}

// BenchmarkControlDispatchMapWithCall measures full dispatch including handler call.
func BenchmarkControlDispatchMapWithCall(b *testing.B) {
    c := &srtConn{}
    c.initializeControlHandlers()
    c.metrics = metrics.NewConnectionMetrics()
    packets := createMixedControlPackets()
    mask := numTestPackets - 1

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        p := packets[i&mask]
        if handler, ok := c.controlHandlers[p.Header().ControlType]; ok {
            handler(p)
        }
    }
}

// BenchmarkControlDispatchSwitchWithCall for comparison.
func BenchmarkControlDispatchSwitchWithCall(b *testing.B) {
    c := &srtConn{}
    c.metrics = metrics.NewConnectionMetrics()
    packets := createMixedControlPackets()
    mask := numTestPackets - 1

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        p := packets[i&mask]
        switch p.Header().ControlType {
        case packet.CTRLTYPE_ACK:
            c.handleACK(p)
        case packet.CTRLTYPE_NAK:
            c.handleNAK(p)
        case packet.CTRLTYPE_ACKACK:
            c.handleACKACK(p)
        case packet.CTRLTYPE_KEEPALIVE:
            c.handleKeepAlive(p)
        case packet.CTRLTYPE_SHUTDOWN:
            c.handleShutdown(p)
        case packet.CTRLTYPE_USER:
            c.handleUserPacket(p)
        }
    }
}
```

**Key points for realistic benchmarks:**
1. **4096 packets** - Large enough to overflow typical branch predictor history (varies by CPU, usually 1K-4K entries)
2. **Realistic distribution** - Matches actual SRT traffic patterns
3. **Deterministic seed** - `rand.NewSource(42)` ensures reproducible results across runs
4. **Fast modulo** - `i & mask` instead of `i % numTestPackets` (power of 2)
5. **Both dispatch-only and with-call variants** - Measure overhead separately from handler cost

**Switch Implementation (if faster):**

```go
func (c *srtConn) dispatchControlPacket(p packet.Packet) {
    switch p.Header().ControlType {
    case packet.CTRLTYPE_ACK:
        c.handleACK(p)
    case packet.CTRLTYPE_NAK:
        c.handleNAK(p)
    case packet.CTRLTYPE_ACKACK:
        c.handleACKACK(p)
    case packet.CTRLTYPE_KEEPALIVE:
        c.handleKeepAlive(p)
    case packet.CTRLTYPE_SHUTDOWN:
        c.handleShutdown(p)
    case packet.CTRLTYPE_USER:
        c.handleUserPacket(p)
    default:
        c.metrics.PktRecvErrorParse.Add(1)
    }
}
```

**Expected Results:** For small type sets (< 10), switch typically outperforms map by 2-5x. Decision should be based on benchmark results.

**Action Item:** Add benchmark to `connection_handlers_bench_test.go`, run `make bench-control-dispatch`, select winner.

### 3.3 Concurrency Protection and Lockless Design

#### 3.3.1 Background: GoSRT Lockless Architecture

The data packet path has been redesigned to eliminate lock contention. See [`gosrt_lockless_design.md`](./gosrt_lockless_design.md) for full details.

**The Core Insight** (from `gosrt_lockless_design.md` Section 3):
> If we change the access pattern so that **only one goroutine ever accesses the btrees**, locks become unnecessary.

**Key Components:**

1. **Lock-Free Ring Buffer** (`congestion/live/receive/ring.go`)
   - io_uring completion handlers write packets to a lock-free MPSC ring
   - Multiple producers can write concurrently (atomic operations only)
   - Single consumer (EventLoop) reads packets

2. **EventLoop Architecture** (`congestion/live/receive/tick.go:135`)
   - Single goroutine that continuously processes packets
   - NO concurrent access to packet btree or NAK btree
   - All btree operations are single-threaded within EventLoop

3. **Rate Metrics via Atomics** (`metrics/metrics.go`)
   - All rate calculations use `atomic.Uint64` counters
   - No locks needed for metric updates
   - `SendRateBytes`, `RecvRatePackets`, etc. are all atomic

**Data Flow (Lock-Free Path):**
```
io_uring recv completion → pushToRing() → Lock-Free Ring → EventLoop → btree
        (multiple)             (atomic)        (MPSC)       (single)    (no lock)
```

#### 3.3.2 ACK Optimization: Continuous Scanning

See [`ack_optimization_plan.md`](./ack_optimization_plan.md) Section 3 for the complete design.

**Key Changes from ACK Optimization:**

1. **Unified `contiguousPoint`** - Single atomic variable tracks ACK progress
   - Replaces separate `ackScanHighWaterMark` and `nakScanStartPoint`
   - Both ACK and NAK scans start from same point (no redundant scanning)

2. **Continuous Scan Pattern** (EventLoop `default:` case):
   ```
   EventLoop iteration:
     1. deliverReadyPackets()  → deliver TSBPD-ready packets
     2. processOnePacket()     → insert packet into btree (from ring)
     3. contiguousScan()       → update contiguousPoint (ALWAYS runs)
     4. SEND DECISION:
        - If 64 packets received → Send Light ACK
        - If 10ms elapsed       → Send Full ACK (with RTT)
   ```

3. **Lock-Free ACK/NAK**:
   - `contiguousScan()` updates `r.contiguousPoint` (atomic)
   - NAK btree operations happen in EventLoop (single-threaded)
   - No lock acquisition during ACK/NAK generation

#### 3.3.3 Backwards Compatibility: Lock Wrappers

The lock-free path is **optional**. Legacy locked paths are still supported via function dispatch:

From `congestion/live/receive/receiver.go:234-237`:
```go
if recvConfig.UsePacketRing {
    r.pushFn = r.pushToRing      // Lock-free path
} else {
    r.pushFn = r.pushWithLock    // Legacy locked path
}
```

**Wrapper Pattern:**
- `pushToRing()` (`push.go:37`) - writes to lock-free ring, no mutex
- `pushWithLock()` (`push.go:22`) - acquires `r.lock`, calls `pushLocked()`

This pattern allows gradual migration and A/B testing between modes.

#### 3.3.4 Current Control Packet Handling

**io_uring Path** (high-performance):
- Uses `handlePacketDirect()` which acquires `c.handlePacketMutex`
- Ensures only ONE packet is processed at a time per connection

From `connection_handlers.go:14-38`:
```go
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    // io_uring path - acquire connection mutex first
    c.handlePacketMutex.Lock()
    defer c.handlePacketMutex.Unlock()
    c.handlePacket(p)
}
```

**Legacy Path** (standard UDP):
- Uses buffered channel + goroutine (serialized by design)

**Note:** Control packets currently use **synchronous dispatch with mutex protection**. This is a potential optimization opportunity - see Section 4.4 for future lock-free control packet design.

### 3.4 Sender Lock Architecture

The sender (`congestion/live/send.go`) uses a `sync.RWMutex`:

```go
// congestion/live/send.go:33-42
type sender struct {
    nextSequenceNumber circular.Number
    dropThreshold      uint64

    packetList *list.List    // Packets waiting to be sent (in flight, not yet transmitted)
    lossList   *list.List    // Packets sent but not yet ACK'd (available for retransmission)
    lock       sync.RWMutex
    lockTiming *metrics.LockTimingMetrics
    metrics    *metrics.ConnectionMetrics

    avgPayloadSize float64
    // ...
}
```

**Lock Usage:**
- **Write lock (`Lock()`):** When modifying lists (Push, ACK, NAK processing)
- **Read lock (`RLock()`):** When reading statistics

#### 3.4.1 Packet Lifecycle State Machine

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          SENDER PACKET LIFECYCLE                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  Application Data                                                           │
│       │                                                                     │
│       │ Push(p)                                                             │
│       ▼                                                                     │
│  ┌─────────────┐                                                            │
│  │ packetList  │  ← Packets waiting for their PktTsbpdTime                  │
│  │             │    (scheduled for transmission)                            │
│  └──────┬──────┘                                                            │
│         │                                                                   │
│         │ tickDeliverPackets() - when PktTsbpdTime <= now                   │
│         │ (sends packet on wire, moves to lossList)                         │
│         ▼                                                                   │
│  ┌─────────────┐                                                            │
│  │  lossList   │  ← Packets sent, waiting for ACK                           │
│  │             │    (available for retransmission via NAK)                  │
│  └──────┬──────┘                                                            │
│         │                                                                   │
│         ├───────────────────────────────────────┐                           │
│         │                                       │                           │
│         │ ACK received                          │ Drop threshold exceeded   │
│         │ ackLocked()                           │ tickDropOldPackets()      │
│         ▼                                       ▼                           │
│  ┌─────────────┐                         ┌─────────────┐                    │
│  │ Decommission│                         │ Decommission│                    │
│  │ (success)   │                         │ (too late)  │                    │
│  └─────────────┘                         └─────────────┘                    │
│                                                                             │
│  ═══════════════════════════════════════════════════════════════════════    │
│                                                                             │
│  NAK Processing (retransmit from lossList - packet stays in lossList):      │
│                                                                             │
│  lossList ──► nakLocked*() ──► deliver(p) ──► (packet still in lossList)    │
│               (find packet)    (retransmit)    (waits for ACK)              │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### 3.4.2 Adding Packets to packetList

**Function:** `pushLocked()` (lines 170-214)
**Trigger:** Application calls `sender.Push(p)` with new data

```go
// congestion/live/send.go:170-214 (simplified)
func (s *sender) pushLocked(p packet.Packet) {
    if p == nil {
        return
    }

    m := s.metrics

    // Assign sequence number to the packet
    p.Header().PacketSequenceNumber = s.nextSequenceNumber
    p.Header().PacketPositionFlag = packet.SinglePacket
    p.Header().OrderFlag = false
    p.Header().MessageNumber = 1

    s.nextSequenceNumber = s.nextSequenceNumber.Inc()

    pktLen := p.Len()

    m.CongestionSendPktBuf.Add(1)
    m.CongestionSendByteBuf.Add(uint64(pktLen))
    s.metrics.SendRateBytes.Add(pktLen)

    p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

    // Probe packet timing (for bandwidth estimation)
    probe := p.Header().PacketSequenceNumber.Val() & 0xF
    switch probe {
    case 0:
        s.probeTime = p.Header().PktTsbpdTime
    case 1:
        p.Header().PktTsbpdTime = s.probeTime
    }

    // ══════════════════════════════════════════════════════════════════
    // ADD TO PACKETLIST
    // Packet is now scheduled for transmission when PktTsbpdTime arrives
    // ══════════════════════════════════════════════════════════════════
    s.packetList.PushBack(p)  // <-- ADD

    flightSize := uint64(s.packetList.Len())
    m.CongestionSendPktFlightSize.Store(flightSize)
}
```

#### 3.4.3 The Ticker Goroutine: What Triggers Tick()

Before diving into `tickDeliverPackets()` and `tickDropOldPackets()`, it's important to understand **how and when `Tick()` is called**.

**Trigger Mechanism:**

When an SRT connection is established, the connection starts a **ticker goroutine** that periodically invokes both sender and receiver tick functions.

```go
// connection.go:509-513 - Starting the ticker goroutine
c.connWg.Add(1)
go func() {
    defer c.connWg.Done()
    c.ticker(c.ctx)  // <-- Ticker runs for lifetime of connection
}()
```

**The Ticker Function:**

```go
// connection.go:586-612
func (c *srtConn) ticker(ctx context.Context) {
    // Phase 4: Start event loop in separate goroutine if enabled
    if c.recv.UseEventLoop() {
        go c.recv.EventLoop(ctx)
    }

    // Create periodic timer (default: 10ms, configurable via TickIntervalMs)
    ticker := time.NewTicker(c.tick)  // c.tick = Config.TickIntervalMs (default 10ms)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return  // Connection closed

        case t := <-ticker.C:
            tickTime := uint64(t.Sub(c.start).Microseconds())

            // ══════════════════════════════════════════════════════════════
            // RECEIVER TICK (legacy mode only)
            // If EventLoop is enabled, receiver processes packets continuously
            // ══════════════════════════════════════════════════════════════
            if !c.recv.UseEventLoop() {
                c.recv.Tick(c.tsbpdTimeBase + tickTime)
            }

            // ══════════════════════════════════════════════════════════════
            // SENDER TICK (always runs)
            // Delivers packets from packetList, drops old packets from lossList
            // ══════════════════════════════════════════════════════════════
            c.snd.Tick(tickTime)
        }
    }
}
```

**Timing Configuration:**

```go
// connection.go:396 - Tick interval setup
c.tick = time.Duration(c.config.TickIntervalMs) * time.Millisecond
```

| Config Parameter | Default | Purpose |
|------------------|---------|---------|
| `TickIntervalMs` | 10ms | How often Tick() is called |

**What Happens on Each Tick:**

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         TICKER GOROUTINE (every 10ms)                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  time.Ticker fires every 10ms (default)                                     │
│       │                                                                     │
│       ▼                                                                     │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │ c.snd.Tick(tickTime)  [SENDER - always runs]                           ││
│  │                                                                         ││
│  │  1. tickDeliverPackets()                                                ││
│  │     - Find packets in packetList where PktTsbpdTime <= now              ││
│  │     - Send them on the wire via deliver()                               ││
│  │     - Move them to lossList                                             ││
│  │                                                                         ││
│  │  2. tickDropOldPackets()                                                ││
│  │     - Find packets in lossList where age > dropThreshold                ││
│  │     - Remove and Decommission() them                                    ││
│  │                                                                         ││
│  │  3. tickUpdateRateStats()                                               ││
│  │     - Calculate send rate, retransmit rate                              ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │ c.recv.Tick(tickTime)  [RECEIVER - only if EventLoop disabled]         ││
│  │                                                                         ││
│  │  1. periodicNAK()   - Detect gaps, send NAK packets                     ││
│  │  2. periodicACK()   - Send ACK/Light ACK packets                        ││
│  │  3. deliverReadyPacketsLocked() - Deliver to application                ││
│  │  4. updateRateStats() - Calculate receive rate                          ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│                                                                             │
│  Note: If UseEventLoop=true, receiver runs in separate EventLoop goroutine  │
│        instead of being driven by the ticker.                               │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Key Insight:** The ticker is created when the connection starts and runs for the **entire lifetime of the connection**. It's stopped when `ctx.Done()` signals (connection close).

#### 3.4.4 Moving Packets: packetList → lossList (on send)

**Function:** `tickDeliverPackets()` (lines 247-279)
**Trigger:** `Tick()` is called periodically; sends packets whose time has come

```go
// congestion/live/send.go:247-279
func (s *sender) tickDeliverPackets(now uint64) {
    m := s.metrics

    removeList := make([]*list.Element, 0, s.packetList.Len())

    // Find packets ready to send (PktTsbpdTime <= now)
    for e := s.packetList.Front(); e != nil; e = e.Next() {
        p := e.Value.(packet.Packet)
        if p.Header().PktTsbpdTime <= now {
            pktLen := p.Len()

            m.CongestionSendPkt.Add(1)
            m.CongestionSendPktUnique.Add(1)
            m.CongestionSendByte.Add(uint64(pktLen))
            m.CongestionSendByteUnique.Add(uint64(pktLen))
            m.CongestionSendUsSndDuration.Add(uint64(s.pktSndPeriod))

            s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
            s.metrics.SendRateBytesSent.Add(pktLen)

            // ══════════════════════════════════════════════════════════
            // SEND PACKET ON WIRE
            // ══════════════════════════════════════════════════════════
            s.deliver(p)  // <-- TRANSMIT

            removeList = append(removeList, e)
        } else {
            break  // packetList is ordered by time
        }
    }

    // ══════════════════════════════════════════════════════════════════
    // MOVE: packetList → lossList
    // Packet is now "in flight" - sent but not yet acknowledged
    // Available for retransmission if NAK received
    // ══════════════════════════════════════════════════════════════════
    for _, e := range removeList {
        s.lossList.PushBack(e.Value)  // <-- ADD to lossList
        s.packetList.Remove(e)         // <-- REMOVE from packetList
    }
}
```

#### 3.4.5 Removing Packets from lossList (ACK received)

**Function:** `ackLocked()` (lines 366-396)
**Trigger:** ACK control packet received from receiver

```go
// congestion/live/send.go:366-396
func (s *sender) ackLocked(sequenceNumber circular.Number) {
    m := s.metrics

    removeList := make([]*list.Element, 0, s.lossList.Len())

    // Find all packets with sequence number < ACK'd sequence
    // (cumulative ACK - all packets up to this point are acknowledged)
    for e := s.lossList.Front(); e != nil; e = e.Next() {
        p := e.Value.(packet.Packet)
        if p.Header().PacketSequenceNumber.Lt(sequenceNumber) {
            // ══════════════════════════════════════════════════════════
            // PACKET SUCCESSFULLY DELIVERED
            // Receiver has acknowledged receipt
            // ══════════════════════════════════════════════════════════
            removeList = append(removeList, e)
        } else {
            break  // lossList is ordered by sequence number
        }
    }

    // Remove ACK'd packets from lossList
    for _, e := range removeList {
        p := e.Value.(packet.Packet)

        m.CongestionSendPktBuf.Add(^uint64(0))                    // Decrement by 1
        m.CongestionSendByteBuf.Add(^uint64(uint64(p.Len()) - 1)) // Subtract pktLen

        // ══════════════════════════════════════════════════════════
        // REMOVE from lossList - packet no longer needed
        // ══════════════════════════════════════════════════════════
        s.lossList.Remove(e)  // <-- REMOVE

        // Return packet to pool for reuse
        p.Decommission()
    }

    s.pktSndPeriod = (s.avgPayloadSize + 16) * 1000000 / s.maxBW
}
```

#### 3.4.6 Removing Packets from lossList (drop threshold exceeded)

**Function:** `tickDropOldPackets()` (lines 281-313)
**Trigger:** `Tick()` is called periodically; drops packets that are too old

```go
// congestion/live/send.go:281-313
func (s *sender) tickDropOldPackets(now uint64) {
    m := s.metrics

    removeList := make([]*list.Element, 0, s.lossList.Len())

    for e := s.lossList.Front(); e != nil; e = e.Next() {
        p := e.Value.(packet.Packet)

        // ══════════════════════════════════════════════════════════
        // CHECK DROP THRESHOLD
        // If packet is too old, receiver can't use it anyway (TSBPD deadline passed)
        // dropThreshold is typically 1.25 * latency
        // ══════════════════════════════════════════════════════════
        if p.Header().PktTsbpdTime+s.dropThreshold <= now {
            pktLen := p.Len()
            metrics.IncrementSendDataDrop(m, metrics.DropReasonTooOldSend, uint64(pktLen))
            removeList = append(removeList, e)
        }
    }

    // Remove dropped packets
    for _, e := range removeList {
        p := e.Value.(packet.Packet)

        m.CongestionSendPktBuf.Add(^uint64(0))                    // Decrement by 1
        m.CongestionSendByteBuf.Add(^uint64(uint64(p.Len()) - 1)) // Subtract pktLen

        // ══════════════════════════════════════════════════════════
        // REMOVE from lossList - packet is too late to be useful
        // ══════════════════════════════════════════════════════════
        s.lossList.Remove(e)  // <-- REMOVE

        // Return packet to pool
        p.Decommission()
    }
}
```

#### 3.4.7 Reading lossList for Retransmission (NAK received)

**Function:** `nakLockedHonorOrder()` (lines 484-540)
**Trigger:** NAK control packet received from receiver

```go
// congestion/live/send.go:484-540 (simplified)
func (s *sender) nakLockedHonorOrder(sequenceNumbers []circular.Number) uint64 {
    m := s.metrics

    // Count packets requested
    totalLossCount := metrics.CountNAKEntries(m, sequenceNumbers, metrics.NAKCounterRecv)
    totalLossBytes := totalLossCount * uint64(s.avgPayloadSize)

    m.CongestionSendPktLoss.Add(totalLossCount)
    m.CongestionSendByteLoss.Add(totalLossBytes)

    retransCount := uint64(0)

    // Process each range/single in NAK order
    for i := 0; i < len(sequenceNumbers); i += 2 {
        startSeq := sequenceNumbers[i]
        endSeq := sequenceNumbers[i+1]

        // ══════════════════════════════════════════════════════════
        // ITERATE lossList to find packets for retransmission
        // Note: Packets STAY in lossList after retransmission
        //       (still need to wait for ACK)
        // ══════════════════════════════════════════════════════════
        for e := s.lossList.Front(); e != nil; e = e.Next() {  // <-- READ
            p := e.Value.(packet.Packet)
            pktSeq := p.Header().PacketSequenceNumber

            if pktSeq.Gte(startSeq) && pktSeq.Lte(endSeq) {
                pktLen := p.Len()
                m.CongestionSendPktRetrans.Add(1)
                m.CongestionSendPkt.Add(1)
                m.CongestionSendByteRetrans.Add(uint64(pktLen))
                m.CongestionSendByte.Add(uint64(pktLen))

                s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
                m.SendRateBytesSent.Add(pktLen)
                m.SendRateBytesRetrans.Add(pktLen)

                p.Header().RetransmittedPacketFlag = true

                // ══════════════════════════════════════════════════════
                // RETRANSMIT - packet stays in lossList
                // Will be removed when ACK covers this sequence number
                // ══════════════════════════════════════════════════════
                s.deliver(p)  // <-- RETRANSMIT (packet stays in lossList)

                retransCount++
            }
        }
    }

    if retransCount < totalLossCount {
        m.CongestionSendNAKNotFound.Add(totalLossCount - retransCount)
    }

    m.NakHonoredOrder.Add(1)
    return retransCount
}
```

#### 3.4.8 Summary Table: List Operations

| Operation | Function | Line | Action | Trigger |
|-----------|----------|------|--------|---------|
| **Add to packetList** | `pushLocked()` | 210 | `s.packetList.PushBack(p)` | App calls `Push(p)` |
| **Remove from packetList** | `tickDeliverPackets()` | 277 | `s.packetList.Remove(e)` | PktTsbpdTime reached |
| **Add to lossList** | `tickDeliverPackets()` | 276 | `s.lossList.PushBack(e.Value)` | Packet transmitted |
| **Remove from lossList** | `ackLocked()` | 389 | `s.lossList.Remove(e)` | ACK received |
| **Remove from lossList** | `tickDropOldPackets()` | 308 | `s.lossList.Remove(e)` | Drop threshold exceeded |
| **Read lossList** | `nakLockedOriginal()` | 444 | `for e := s.lossList.Back()...` | NAK received |
| **Read lossList** | `nakLockedHonorOrder()` | 504 | `for e := s.lossList.Front()...` | NAK received |

**Key Insight:** Packets in `lossList` are NOT removed on retransmission - they stay until ACK'd or dropped. This is why retransmit suppression is needed: the same packet can be retransmitted multiple times if multiple NAKs arrive before the ACK.

### 3.5 Data vs Control Path Comparison

| Aspect | Data Packets | Control Packets |
|--------|--------------|-----------------|
| **Path** | `Push()` → ring/btree | `handlePacket()` → handler |
| **Buffering** | Lock-free ring (optional) | None (synchronous) |
| **Concurrency** | EventLoop processes from ring | Mutex-protected handlers |
| **Latency** | Batch processing | Immediate |

### 3.6 Implications for Retransmit Suppression

Since control packets (NAK) are processed **synchronously** with mutex protection:

1. **NAK processing is serialized:** Only one NAK processed at a time per connection
2. **Sender lossList is mutex-protected:** Safe to read/modify during NAK processing
3. **No race between NAK handling and RTT updates:** Both acquire connection-level mutex

**This means:** We can safely add retransmit tracking directly to the sender's lossList packets without additional synchronization. The existing `sender.lock` provides the necessary protection.

---

## 4. Data Packet Path: Lock-Free Ring and EventLoop

While control packets are processed synchronously (Section 3), data packets follow a different path designed for high throughput.

### 4.1 Lock-Free Ring Architecture

When `usePacketRing=true` (HighPerf mode), data packets bypass locks entirely:

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                      DATA PACKET PATH (HighPerf Mode)                         │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  io_uring recv completion                                                    │
│       │                                                                      │
│       ▼                                                                      │
│  c.recv.Push(p) → pushToRing(pkt)                                           │
│       │                                                                      │
│       ▼                                                                      │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │ Lock-Free Ring Buffer (sharded MPSC)                                    ││
│  │                                                                         ││
│  │  • WriteWithBackoff() - non-blocking write                              ││
│  │  • TryRead() - non-blocking read                                        ││
│  │  • No locks - uses atomic operations                                    ││
│  │  • Sharded by sequence number for distribution                          ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│       │                                                                      │
│       │ EventLoop goroutine (single consumer)                               │
│       ▼                                                                      │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │ processOnePacket() [tick.go:331]                                        ││
│  │                                                                         ││
│  │  item, ok := r.packetRing.TryRead()                                     ││
│  │  if ok {                                                                ││
│  │      // Process: duplicate check, NAK btree delete, btree insert        ││
│  │      // NO LOCKS - single consumer owns the btree                       ││
│  │  }                                                                      ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 4.2 EventLoop Single-Consumer Model

The EventLoop is the **single consumer** of the lock-free ring. This means:
- No locks needed when accessing the packet btree
- All btree operations (insert, delete, iterate) are single-threaded within EventLoop
- Periodic operations (ACK, NAK) also run within EventLoop context

From `tick.go:150-328` (EventLoop main loop):
```go
for {
    select {
    case <-ctx.Done():
        return
    case <-fullACKTicker.C:
        r.drainRingByDelta()
        // ... ACK logic ...
    case <-nakTicker.C:
        // ... NAK logic ...
    default:
        r.processOnePacket()  // Drain one packet from ring into btree
        r.deliverReadyPackets()
        // ... ACK scan, backoff ...
    }
}
```

### 4.3 Control Packets Are NOT Queued

**Important:** Control packets (ACK, NAK, ACKACK) do NOT go through the lock-free ring. They are processed **synchronously** on the packet arrival goroutine.

This is intentional:
- Control packets need immediate processing (RTT measurement, retransmit triggers)
- Control packets are low-volume compared to data packets
- Buffering would add latency to critical operations

### 4.4 Future Enhancement: Lock-Free Control Packet Ring

If control packet processing becomes a bottleneck (e.g., high NAK rates causing mutex contention), we could extend the lock-free architecture to control packets.

#### 4.4.1 Design Goals

1. **Consistent with Data Packet Path** - Same patterns, similar function/variable names
2. **Lock-Free Hot Path** - No mutex in common cases
3. **Backwards Compatible** - Support both locked and lock-free modes
4. **Atomic Metrics** - Migrate lock-protected variables to atomics

#### 4.4.2 Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                    LOCK-FREE CONTROL PACKET PATH (Future)                     │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  io_uring recv completion                                                    │
│       │                                                                      │
│       ▼                                                                      │
│  handlePacket() [connection_handlers.go:89]                                  │
│       │                                                                      │
│       ├── if header.IsControlPacket                                          │
│       │       │                                                              │
│       │       ▼                                                              │
│       │   c.ctrl.Push(p) ──────────► Control Ring (lock-free)               │
│       │                                    │                                 │
│       │                                    ▼                                 │
│       │                              EventLoop consumes via                  │
│       │                              processControlPacketsDelta()            │
│       │                              (processes ALL accumulated packets)     │
│       │                                    │                                 │
│       │                              ┌─────┴─────┐                          │
│       │                              │ Dispatch  │                          │
│       │                              └─────┬─────┘                          │
│       │                   ┌────────────────┼────────────────┐               │
│       │                   ▼                ▼                ▼               │
│       │             handleACK()      handleNAK()      handleACKACK()        │
│       │                                                                      │
│       └── else (data packet)                                                 │
│               │                                                              │
│               ▼                                                              │
│           c.recv.Push(p) ──────────► Data Ring (existing)                   │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

**Note:** We use `processControlPacketsDelta()` (batch processing) instead of `processOneControlPacket()` (single packet). This reduces atomic operations from O(n) to O(1) and ensures control packets don't starve during high data rates. See Section 4.4.6 for details.

#### 4.4.3 Implementation Details

**a) Add Control Packet Ring**

New file: `control/ring.go` (or extend `connection.go`)

```go
// connection.go - new fields
type srtConn struct {
    // ... existing fields ...

    // Control packet processing (future lock-free path)
    ctrl              *controlProcessor
    useControlRing    bool
    controlRing       *lockfree.Ring[packet.Packet]
}
```

**b) Update handlePacket() to Push to Control Ring**

File: `connection_handlers.go:89-117`

```go
func (c *srtConn) handlePacket(p packet.Packet) {
    if p == nil {
        return
    }

    c.resetPeerIdleTimeout()
    header := p.Header()

    if header.IsControlPacket {
        // NEW: Use control processor (supports both modes)
        c.ctrl.Push(p)  // Dispatches to pushToControlRing or pushControlWithLock
        return
    }

    // Data packet path (unchanged)
    // ...
}
```

**c) Create Control Packet Push with Function Dispatch**

New file: `control_processor.go`

```go
type controlProcessor struct {
    conn           *srtConn
    pushFn         func(packet.Packet)  // Function dispatch (like receiver.pushFn)
    controlRing    *lockfree.Ring[packet.Packet]
    useControlRing bool
}

func (cp *controlProcessor) Push(p packet.Packet) {
    cp.pushFn(p)
}

// Lock-free path
func (cp *controlProcessor) pushToControlRing(p packet.Packet) {
    producerID := uint64(p.Header().TypeSpecific)  // Use ACK number or similar for sharding
    if !cp.controlRing.WriteWithBackoff(producerID, p, cp.writeConfig) {
        cp.conn.metrics.ControlRingDropsTotal.Add(1)
        // Fallback: process synchronously on drop
        cp.dispatchControl(p)
    }
}

// Legacy locked path
func (cp *controlProcessor) pushControlWithLock(p packet.Packet) {
    cp.conn.handlePacketMutex.Lock()
    defer cp.conn.handlePacketMutex.Unlock()
    cp.dispatchControl(p)
}

// Dispatch to appropriate handler (O(1) lookup)
func (cp *controlProcessor) dispatchControl(p packet.Packet) {
    handler, ok := cp.conn.controlHandlers[p.Header().ControlType]
    if !ok {
        cp.conn.metrics.PktRecvErrorParse.Add(1)
        return
    }
    handler(cp.conn, p)
}
```

**d) Initialize Control Processor with Mode Selection**

File: `connection.go` (in connection initialization)

```go
func (c *srtConn) initializeControlProcessor(config Config) {
    c.ctrl = &controlProcessor{
        conn:           c,
        useControlRing: config.UseControlRing,
    }

    if config.UseControlRing {
        c.ctrl.controlRing = lockfree.NewRing[packet.Packet](
            config.ControlRingSize,
            config.ControlRingShards,
        )
        c.ctrl.pushFn = c.ctrl.pushToControlRing
    } else {
        c.ctrl.pushFn = c.ctrl.pushControlWithLock
    }
}
```

**e) Add Control Packet Processing to EventLoop**

File: `congestion/live/receive/tick.go:135` - `EventLoop()`

```go
func (r *receiver) EventLoop(ctx context.Context) {
    // ... existing ticker setup ...

    for {
        select {
        case <-ctx.Done():
            return

        case <-fullACKTicker.C:
            // ... existing Full ACK logic ...

        case <-nakTicker.C:
            // ... existing NAK logic ...

        default:
            // Process data packets (existing)
            processed := r.processOnePacket()
            delivered := r.deliverReadyPackets()

            // ══════════════════════════════════════════════════════════════════
            // CONTROL PACKET PROCESSING
            //
            // IMPORTANT: We use processControlPacketsDelta() (batch processing)
            // instead of processOneControlPacket() (single packet).
            //
            // Reasons:
            // 1. Performance: Single atomic Add(count) vs Add(1) per packet
            // 2. Fairness: Clears backlog, prevents control packet starvation
            // 3. Consistency: Matches data packet's delta-based ring draining
            //
            // See Section 4.4.6 for detailed implementation.
            // ══════════════════════════════════════════════════════════════════
            controlProcessed := r.processControlPacketsDelta()

            // ... existing ACK scan and backoff ...

            if !processed && delivered == 0 && controlProcessed == 0 {
                r.metrics.EventLoopIdleBackoffs.Add(1)
                time.Sleep(backoff.getSleepDuration())
            } else {
                backoff.recordActivity()
            }
        }
    }
}

// ══════════════════════════════════════════════════════════════════════════════
// NOTE: We use processControlPacketsDelta() instead of processOneControlPacket()
//
// processControlPacketsDelta() is defined in Section 4.4.6 and processes
// ALL accumulated control packets in a single call, with a single atomic
// metrics update at the end (O(1) vs O(n) atomic operations).
//
// The old processOneControlPacket() approach (below) is NOT used:
// ══════════════════════════════════════════════════════════════════════════════

// DEPRECATED: processOneControlPacket - DO NOT USE
// This single-packet approach has been replaced by processControlPacketsDelta()
// which processes all accumulated packets with better performance.
//
// func (r *receiver) processOneControlPacket() bool {
//     if r.controlRing == nil {
//         return false
//     }
//     item, ok := r.controlRing.TryRead()
//     if !ok {
//         return false  // Ring empty
//     }
//     p := item.(packet.Packet)
//     r.metrics.ControlRingPacketsProcessed.Add(1)  // ← O(n) atomic ops!
//     r.conn.ctrl.dispatchControl(p)
//     return true
// }
```

**f) New Configuration Options**

File: `config.go`

The control ring configuration should mirror the data packet ring configuration for consistency, with more conservative defaults (fewer control packets than data packets).

```go
type Config struct {
    // ... existing fields ...

    // ==========================================================================
    // Data Packet Ring (existing)
    // ==========================================================================
    UsePacketRing              bool          // Enable lock-free data packet processing
    PacketRingSize             int           // Ring buffer size (default: 1024)
    PacketRingShards           int           // Number of shards (default: 4)
    PacketRingMaxRetries       int           // Max backoff retries (default: 10)
    PacketRingBackoffDuration  time.Duration // Initial backoff duration (default: 100µs)

    // ==========================================================================
    // Control Packet Ring (future lock-free path)
    // ==========================================================================
    UseControlRing             bool          // Enable lock-free control packet processing
    ControlRingSize            int           // Ring buffer size (default: 256)
    ControlRingShards          int           // Number of shards (default: 2)
    ControlRingMaxRetries      int           // Max backoff retries (default: 5)
    ControlRingBackoffDuration time.Duration // Initial backoff duration (default: 50µs)

    // ==========================================================================
    // Retransmit/NAK Suppression Configuration (RTO-based)
    // ==========================================================================
    RetransmitSuppressionEnabled bool    // Enable sender-side retransmit suppression
    NakSuppressionEnabled        bool    // Enable receiver-side NAK throttling
    RTOMode                      string  // "rtt_rttvar", "rtt_4rttvar", "rtt_rttvar_margin"
    ExtraRTTMarginPercent        float64 // Only for "rtt_rttvar_margin" mode (default: 10%)
}

// Defaults - Control ring is more conservative than data ring
const (
    // Data Packet Ring defaults
    DefaultPacketRingSize            = 1024
    DefaultPacketRingShards          = 4
    DefaultPacketRingMaxRetries      = 10
    DefaultPacketRingBackoffDuration = 100 * time.Microsecond

    // Control Packet Ring defaults (more conservative)
    DefaultControlRingSize            = 256   // 4x smaller than data ring
    DefaultControlRingShards          = 2     // 2x fewer shards
    DefaultControlRingMaxRetries      = 5     // Fewer retries (control is important)
    DefaultControlRingBackoffDuration = 50 * time.Microsecond // Faster backoff

    // Retransmit/NAK Suppression defaults (RTO-based)
    DefaultRTOMode             = "rtt_rttvar" // RTT + RTTVar (balanced)
    DefaultExtraRTTMarginPercent = 10.0       // 10% extra when using "rtt_rttvar_margin"
)
```

**Rationale for Control Ring Defaults:**

| Setting | Data Ring | Control Ring | Rationale |
|---------|-----------|--------------|-----------|
| Size | 1024 | 256 | Control packets are ~10-100x fewer |
| Shards | 4 | 2 | Less parallelism needed |
| MaxRetries | 10 | 5 | Control is time-sensitive, fail faster |
| BackoffDuration | 100µs | 50µs | Minimize latency for RTT measurement |

**g) CLI Flags and Test Scripts**

File: `contrib/common/flags.go`

```go
// Add new flags for control ring
flag.BoolVar(&config.UseControlRing, "usecontrolring", false, "Enable lock-free control packet ring")
flag.IntVar(&config.ControlRingSize, "controlringsize", 256, "Control ring buffer size")
flag.IntVar(&config.ControlRingShards, "controlringshards", 2, "Control ring shard count")
flag.IntVar(&config.ControlRingMaxRetries, "controlringmaxretries", 5, "Control ring max backoff retries")
flag.DurationVar(&config.ControlRingBackoffDuration, "controlringbackoffduration", 50*time.Microsecond, "Control ring backoff duration")

// Add new flags for retransmit/NAK suppression (RTO-based)
flag.BoolVar(&config.RetransmitSuppressionEnabled, "retransmitsuppression", true, "Enable sender retransmit suppression")
flag.BoolVar(&config.NakSuppressionEnabled, "naksuppression", false, "Enable receiver NAK throttling")
flag.StringVar(&config.RTOMode, "rtomode", "rtt_rttvar", "RTO calculation: rtt_rttvar, rtt_4rttvar, rtt_rttvar_margin")
flag.Float64Var(&config.ExtraRTTMarginPercent, "extrarttmarginpercent", 10.0, "Extra margin % for rtt_rttvar_margin mode")
```

File: `contrib/common/test_flags.sh`

```bash
# Control Ring flags
TEST_CONTROL_RING_FLAGS=(
    "-usecontrolring"
    "-controlringsize=256"
    "-controlringshards=2"
    "-controlringmaxretries=5"
    "-controlringbackoffduration=50µs"
)

# Retransmit/NAK Suppression flags (RTO-based)
TEST_SUPPRESSION_FLAGS=(
    "-retransmitsuppression"
    "-naksuppression"
    "-rtomode=rtt_rttvar"
    "-extrarttmarginpercent=10"
)

# Full EventLoop with control ring
TEST_FULL_EVENTLOOP_CONTROL_FLAGS=(
    "${TEST_EVENTLOOP_FLAGS[@]}"
    "${TEST_CONTROL_RING_FLAGS[@]}"
    "${TEST_SUPPRESSION_FLAGS[@]}"
)
```

**Verification:** Run `make test-flags` to ensure all new flags are properly registered and have valid defaults.

#### 4.4.4 Metrics Migration to Atomics

When implementing lock-free control packets, the following variables must be migrated from lock-protected to atomic:

**Sender-Side (congestion/live/send.go):**

| Variable | Current | Atomic Equivalent |
|----------|---------|-------------------|
| `s.avgPayloadSize` | `float64` under lock | `atomic.Uint64` (bits) |
| `s.pktSndPeriod` | `float64` under lock | `atomic.Uint64` (bits) |
| `s.maxBW` | `float64` under lock | `atomic.Uint64` (bits) |
| `s.probeTime` | `uint64` under lock | `atomic.Uint64` |

**Pattern (from `gosrt_lockless_design.md` Phase 1):**
```go
// Store float64 as uint64 bits
m.SendRateEstInputBW.Store(math.Float64bits(estimatedInputBW))

// Read float64 from uint64 bits
estimatedBW := math.Float64frombits(m.SendRateEstInputBW.Load())
```

**Connection-Level (connection.go):**

| Variable | Current | Atomic Equivalent |
|----------|---------|-------------------|
| `c.rtt.rttBits` | Already atomic | ✓ |
| `c.rtt.rttVarBits` | Already atomic | ✓ |
| `c.nextACKNumber` | Already atomic | ✓ |

**New Metrics for Control Ring:**

```go
// metrics/metrics.go - add these fields
ControlRingPacketsReceived   atomic.Uint64  // Packets written to control ring
ControlRingPacketsProcessed  atomic.Uint64  // Packets read from control ring
ControlRingDropsTotal        atomic.Uint64  // Packets dropped (ring full)
ControlRingDrainDelta        atomic.Uint64  // received - processed
```

#### 4.4.5 Latency Considerations

**Trade-off:** Lock-free control packet processing adds a small latency (~1 EventLoop iteration) to:
- RTT measurement (ACKACK processing)
- Retransmission triggering (NAK processing)

**Impact Analysis:**
- EventLoop iteration: ~10-100µs typical
- Processing delay: 1-5ms in worst case (busy EventLoop)
- RTT accuracy: Could be affected by processing delay

**Mitigation Strategies:**

##### Strategy 1: Arrival Time Tracking

Record packet arrival time at io_uring completion and pass through the processing chain. This allows RTT calculation to compensate for processing delays.

```go
// packet/packet.go - Add arrival time field to Header
type Header struct {
    // ... existing fields ...

    // Arrival time tracking (for RTT compensation)
    ArrivalTimeUs uint64  // Timestamp when packet was received (µs since connection start)
}
```

**io_uring recv completion path:**

```go
// listen_linux.go / dial_linux.go - Record arrival time
func (l *listener) handleRecvCompletion(cqe *uring.CQE) {
    // ... existing code ...

    // Record arrival time BEFORE any processing
    arrivalTime := uint64(time.Since(l.connectionStartTime).Microseconds())

    p := packet.NewFromBuffer(buf)
    p.Header().ArrivalTimeUs = arrivalTime  // Set arrival time

    conn.handlePacketDirect(p)
}
```

**RTT calculation with arrival time compensation:**

```go
// connection_handlers.go - handleACKACK with arrival compensation
func (c *srtConn) handleACKACK(p packet.Packet) {
    now := time.Now()

    // Use arrival time if available, otherwise use current time
    arrivalTime := p.Header().ArrivalTimeUs
    if arrivalTime == 0 {
        arrivalTime = uint64(time.Since(c.startTime).Microseconds())
    }

    // Calculate RTT using arrival time (more accurate)
    entry := c.ackNumbers.Get(ackNum)
    if entry != nil {
        // RTT = arrival_time - send_time (not processing_time - send_time)
        rttUs := arrivalTime - entry.timestampUs
        c.recalculateRTT(time.Duration(rttUs) * time.Microsecond)
    }
}
```

**Benefits:**
- RTT accuracy maintained even with EventLoop delays
- Low single-digit millisecond processing delays are compensated
- No impact on existing non-ring paths

##### Strategy 2: Interleaved Control Packet Processing

Call control packet processing between EVERY step in the EventLoop, not just at the end. This ensures control packets are serviced very regularly.

```go
// congestion/live/receive/tick.go - EventLoop with interleaved control processing
func (r *receiver) EventLoop(ctx context.Context) {
    // ... ticker setup ...

    for {
        select {
        case <-ctx.Done():
            return

        case <-fullACKTicker.C:
            r.processAllControlPackets()  // Process control FIRST
            r.drainRingByDelta()
            // ... Full ACK logic ...

        case <-nakTicker.C:
            r.processAllControlPackets()  // Process control FIRST
            // ... NAK logic ...

        default:
            // ══════════════════════════════════════════════════════════════════
            // DELTA-BASED CONTROL PACKET PROCESSING (CHOSEN APPROACH)
            //
            // We use processControlPacketsDelta() instead of multiple
            // processOneControlPacket() calls. This provides:
            // - Single atomic update (O(1) vs O(n))
            // - Clears control packet backlog efficiently
            // - Prevents starvation during data bursts
            //
            // See Section 4.4.6 for implementation details.
            // ══════════════════════════════════════════════════════════════════
            controlCount := r.processControlPacketsDelta()

            processed := r.processOnePacket()
            delivered := r.deliverReadyPackets()

            ok, newContiguous := r.contiguousScan()

            // ... ACK send decision, backoff ...
            if !processed && delivered == 0 && controlCount == 0 {
                r.metrics.EventLoopIdleBackoffs.Add(1)
                time.Sleep(backoff.getSleepDuration())
            } else {
                backoff.recordActivity()
            }
        }
    }
}

// ══════════════════════════════════════════════════════════════════════════════
// NOTE: processAllControlPackets() is NOT used in the chosen implementation.
// It has been superseded by processControlPacketsDelta() which is more efficient.
//
// The loop-based approach below was considered but rejected due to O(n) atomic ops:
// ══════════════════════════════════════════════════════════════════════════════

// DEPRECATED: processAllControlPackets - DO NOT USE
// Replaced by processControlPacketsDelta() in Section 4.4.6
//
// func (r *receiver) processAllControlPackets() int {
//     count := 0
//     for r.processOneControlPacket() {  // ← O(n) atomic ops!
//         count++
//         if count > 100 {
//             break
//         }
//     }
//     return count
// }
```

##### Strategy 3: Priority Processing Order

Process control packets BEFORE data packets in EventLoop:

```go
default:
    // ══════════════════════════════════════════════════════════════════
    // CONTROL PACKETS FIRST (time-critical for RTT)
    //
    // We use processControlPacketsDelta() which:
    // - Processes ALL accumulated control packets (up to cap of 50)
    // - Uses single atomic update (O(1) vs O(n))
    // - See Section 4.4.6 for implementation
    // ══════════════════════════════════════════════════════════════════
    controlCount := r.processControlPacketsDelta()

    // 2. Data packets
    processed := r.processOnePacket()
    delivered := r.deliverReadyPackets()

    // 3. ACK scan
    ok, newContiguous := r.contiguousScan()
    // ...
```

##### Strategy 4: Hybrid - ACKACK Immediate (Direct Handler Call)

For maximum RTT accuracy, ACKACK packets can be processed immediately by directly calling the handler, while queueing other control packets to the ring.

**Key Insight:** ACKACK handling is simple and fast (btree lookup + RTT calculation). It doesn't require the same protection as NAK/ACK because:
- It only reads from the ACK btree (which uses its own lock)
- RTT update uses atomic CAS (lock-free)
- No modification to packet btree or NAK btree

```go
// Hybrid: ACKACK direct handler call, others queued to ring
func (cp *controlProcessor) pushToControlRing(p packet.Packet) {
    if p.Header().ControlType == packet.CTRLTYPE_ACKACK {
        // ACKACK is time-critical for RTT measurement
        // Direct call is safe because:
        // 1. ACK btree has its own lock (c.ackLock)
        // 2. RTT update uses atomic CAS (no lock needed)
        // 3. No packet btree or NAK btree access
        cp.conn.handleACKACK(p)  // Direct call, no ring
        return
    }
    // NAK, ACK, KEEPALIVE go through ring for lock-free processing
    cp.controlRing.WriteWithBackoff(producerID, p, cp.writeConfig)
}
```

**Why this doesn't require additional locking:**

| Operation in handleACKACK() | Protection | Lock-Free? |
|----------------------------|------------|------------|
| `c.ackNumbers.Get(ackNum)` | `c.ackLock` (existing) | No, uses existing lock |
| `c.rtt.Recalculate()` | Atomic CAS | Yes |
| `c.metrics.RTT*.Store()` | Atomic | Yes |

The existing `c.ackLock` in `handleACKACK()` provides necessary protection. We don't add new locking - we leverage the existing fine-grained lock.

**Recommendation:** Combine Strategy 1 (arrival time tracking) + Strategy 4 (ACKACK direct) for best results:
- Accurate RTT even with processing delays (arrival time compensation)
- Minimal ACKACK latency (direct handler call)
- Lock-free path for NAK/ACK (ring + EventLoop)

#### 4.4.6 Delta-Based Control Packet Processing

Unlike data packets where we process one at a time, control packets should be processed in batches based on the **delta** (number accumulated since last processing). This ensures:
- Control packets don't starve during high data rates
- Timely processing of time-sensitive packets (NAK, ACK)
- Consistent with data packet's delta-based ring draining

##### Metrics Definition

**File: `metrics/metrics.go`**

```go
// Control Ring metrics (for delta calculation and monitoring)
ControlRingPacketsReceived  atomic.Uint64  // Incremented on ring write
ControlRingPacketsProcessed atomic.Uint64  // Incremented on ring read (batch)
ControlRingDropsTotal       atomic.Uint64  // Packets dropped (ring full)
```

**File: `metrics/handler.go`** - Add to Prometheus export:

```go
// Control Ring metrics
writeCounterIfNonZero(b, "gosrt_control_ring_packets_received_total",
    metrics.ControlRingPacketsReceived.Load(),
    "", connLabels)
writeCounterIfNonZero(b, "gosrt_control_ring_packets_processed_total",
    metrics.ControlRingPacketsProcessed.Load(),
    "", connLabels)
writeCounterIfNonZero(b, "gosrt_control_ring_drops_total",
    metrics.ControlRingDropsTotal.Load(),
    "", connLabels)

// Control Ring drain delta (received - processed = pending)
controlDelta := metrics.ControlRingPacketsReceived.Load() - metrics.ControlRingPacketsProcessed.Load()
writeGaugeIfNonZero(b, "gosrt_control_ring_pending_packets",
    controlDelta,
    "", connLabels)
```

**File: `metrics/handler_test.go`** - Add test coverage:

```go
func TestControlRingMetrics(t *testing.T) {
    m := metrics.NewConnectionMetrics()

    // Simulate control packet flow
    m.ControlRingPacketsReceived.Add(100)
    m.ControlRingPacketsProcessed.Add(95)
    m.ControlRingDropsTotal.Add(2)

    // Verify metrics are exported
    output := exportMetrics(m)
    assert.Contains(t, output, "gosrt_control_ring_packets_received_total 100")
    assert.Contains(t, output, "gosrt_control_ring_packets_processed_total 95")
    assert.Contains(t, output, "gosrt_control_ring_drops_total 2")
    assert.Contains(t, output, "gosrt_control_ring_pending_packets 5")
}
```

##### Implementation with Batch Atomic Update

**File: `congestion/live/receive/tick.go`**

```go
// processControlPacketsDelta processes all control packets that have accumulated
// since the last call. Similar to drainRingByDelta() for data packets.
//
// Performance optimization: Uses single atomic Add(count) at the end instead of
// Add(1) per packet. Reduces atomic operations from O(n) to O(1).
func (r *receiver) processControlPacketsDelta() int {
    if r.controlRing == nil {
        return 0
    }

    // Calculate delta: how many packets are waiting?
    received := r.metrics.ControlRingPacketsReceived.Load()
    processed := r.metrics.ControlRingPacketsProcessed.Load()
    delta := int(received - processed)

    if delta <= 0 {
        return 0  // No packets waiting
    }

    // Cap at reasonable maximum to prevent starvation of other work
    maxBatch := 50  // Process up to 50 control packets per call
    if delta > maxBatch {
        delta = maxBatch
    }

    // Process packets, accumulate count
    count := 0
    for i := 0; i < delta; i++ {
        item, ok := r.controlRing.TryRead()
        if !ok {
            break  // Ring empty (race condition - another consumer?)
        }

        p := item.(packet.Packet)
        r.conn.ctrl.dispatchControl(p)
        count++
    }

    // Single atomic update for all processed packets (performance optimization)
    // Reduces atomic operations from O(n) to O(1)
    if count > 0 {
        r.metrics.ControlRingPacketsProcessed.Add(uint64(count))
    }

    return count
}
```

**Performance Comparison:**

| Approach | Atomic Operations | Notes |
|----------|-------------------|-------|
| `Add(1)` per packet | O(n) | 50 packets = 50 atomic ops |
| `Add(count)` at end | O(1) | 50 packets = 1 atomic op |

For a batch of 50 control packets, this is a **50x reduction** in atomic operations.
```

**EventLoop Integration:**

```go
// EventLoop with delta-based control packet processing
func (r *receiver) EventLoop(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return

        case <-fullACKTicker.C:
            r.processControlPacketsDelta()  // Process control delta FIRST
            // ... Full ACK logic ...

        case <-nakTicker.C:
            r.processControlPacketsDelta()  // Process control delta FIRST
            // ... NAK logic ...

        default:
            // Delta-based processing for both control and data
            controlProcessed := r.processControlPacketsDelta()

            // Data packet processing (existing)
            processed := r.processOnePacket()
            delivered := r.deliverReadyPackets()

            // ACK scan
            ok, newContiguous := r.contiguousScan()
            // ...
        }
    }
}
```

**Comparison: One-at-a-time vs Delta-based:**

| Approach | Atomic Ops | Pros | Cons | Status |
|----------|------------|------|------|--------|
| `processOneControlPacket()` | O(n) | Simple, predictable latency | May starve during burst, inefficient | ❌ NOT USED |
| `processControlPacketsDelta()` | O(1) | Clears backlog, single atomic update | May delay data during control burst | ✅ CHOSEN |

**Decision:** We use `processControlPacketsDelta()` with a cap of 50 packets per call. This provides:
- **50x reduction** in atomic operations for batches of 50 packets
- Fair scheduling that prevents control packet starvation
- Configurable cap to balance responsiveness and fairness

See **Section 4.4.6** for the full implementation.

#### 4.4.7 New Packet Header Fields and Decommission Cleanup

When adding new fields to `packet.Header`, we MUST update `Decommission()` to clear them. Packets are returned to `sync.Pool` and reused - stale data causes bugs.

**New Fields Added in This Design:**

| Field | Location | Purpose | Clear Value |
|-------|----------|---------|-------------|
| `ArrivalTimeUs` | `packet.Header` | RTT compensation | `0` |
| `LastRetransmitTimeUs` | `packet.Header` | Retransmit suppression | `0` |
| `RetransmitCount` | `packet.Header` | Progressive tracking | `0` |

**Update to `packet/packet.go` - `Decommission()`:**

```go
func (p *pkt) Decommission() {
    // Return payload to pool (existing)
    if p.payload != nil {
        payloadPool.Put(p.payload)
        p.payload = nil
    }

    // Clear header fields that are set during packet lifetime
    // (not during initial parsing)
    h := p.Header()

    // Arrival time tracking (set by io_uring completion)
    h.ArrivalTimeUs = 0

    // Retransmit tracking (set by sender during NAK processing)
    h.LastRetransmitTimeUs = 0
    h.RetransmitCount = 0

    // Return packet to pool
    pktPool.Put(p)
}
```

**Why This Matters:**

```
Scenario without clearing:
  1. Packet A: ArrivalTimeUs = 1000, LastRetransmitTimeUs = 5000, RetransmitCount = 3
  2. Packet A decommissioned, returned to pool
  3. Packet B allocated from pool (same memory as A)
  4. Packet B: ArrivalTimeUs = 1000 (STALE!), now used for RTT calculation
  → Incorrect RTT, suppression decisions based on stale data
```

**Verification:** Add to `packet/packet_test.go`:

```go
func TestDecommissionClearsNewFields(t *testing.T) {
    p := NewPacket()
    h := p.Header()

    // Set new fields
    h.ArrivalTimeUs = 12345
    h.LastRetransmitTimeUs = 67890
    h.RetransmitCount = 5

    // Decommission
    p.Decommission()

    // Get same packet back from pool
    p2 := NewPacket()
    h2 := p2.Header()

    // Verify cleared
    assert.Equal(t, uint64(0), h2.ArrivalTimeUs)
    assert.Equal(t, uint64(0), h2.LastRetransmitTimeUs)
    assert.Equal(t, uint32(0), h2.RetransmitCount)
}
```

#### 4.4.8 Files to Modify (Summary)

| File | Changes |
|------|---------|
| `connection.go` | Add `ctrl *controlProcessor`, `controlRing`, `useControlRing` |
| `connection_handlers.go:89` | Update `handlePacket()` to call `c.ctrl.Push(p)` |
| `control_processor.go` (NEW) | Control packet push/dispatch logic |
| `congestion/live/receive/tick.go:135` | Add `processControlPacketsDelta()` to EventLoop |
| `config.go` | Add `UseControlRing`, `ControlRingSize`, `ControlRingShards` |
| `metrics/metrics.go` | Add control ring metrics |
| `metrics/handler.go` | Export control ring metrics to Prometheus |
| `packet/packet.go` | Add new header fields, update `Decommission()` |
| `packet/packet_test.go` | Add test for field cleanup |

#### 4.4.9 Decision: When to Implement

**Current Status:** NOT IMPLEMENTED - Control packets use synchronous dispatch with mutex.

**Implement When:**
- Control packet rate exceeds ~10,000/sec (unlikely in most scenarios)
- Profiling shows `handlePacketMutex` contention > 5%
- NAK storm scenarios cause measurable performance degradation

**For NAK Suppression Design:** The current synchronous control packet handling is sufficient. The sender's `s.lock` provides adequate protection for retransmit suppression logic.

---

## 5. Prerequisites: Bug Fixes and New Metrics

> **Status: COMPLETE ✅**
>
> All items in this section have been implemented and verified. See
> `documentation/duplicate_packet_metrics_implementation.md` for full details including:
> - Btree single-traversal optimization (48% faster for duplicates)
> - Memory leak fix in `receiver.go` (correct sync.Pool return)
> - New metrics added to `metrics/metrics.go` and `metrics/handler.go`
> - NAK-before-ACK defensive check in `send.go`
> - Memory stability tests and benchmarks
> - Makefile targets: `make test-memory-pool`, `make bench-memory-pool`

Before implementing retranmission and NAK suppression, we need to fix existing issues and add observability.

### 5.1 Bug Fix: Duplicate Packet Handling in btree Insert ✅ FIXED

**File:** `congestion/live/receive/packet_store_btree.go`

**Original Code (lines 58-64) - 2 traversals:**
```go
if replaced {
    s.tree.ReplaceOrInsert(old)  // BUG: Unnecessary 2nd traversal
    return false, pkt
}
```

**Fixed Code - 1 traversal:**
```go
if replaced {
    return false, old.packet  // Return OLD packet for decommissioning
}
```

**Key insight:** Both packets have identical data (same sequence number). Keep the new one (already in tree), return the old one for release. No second traversal needed.

**Also fixed:** Memory leak in `receiver.go` where wrong packet was released to sync.Pool.

**Verification:** Memory stability tests confirm sync.Pool working correctly (100K duplicate test shows negative heap growth due to GC reclaiming memory).

### 5.2 New Metrics Required ✅ ADDED

All metrics added to `metrics/metrics.go` and exported in `metrics/handler.go`:

| Metric | Prometheus Name | Status |
|--------|-----------------|--------|
| `CongestionRecvPktDuplicate` | `gosrt_recv_pkt_duplicate_total` | ✅ Added |
| `CongestionRecvByteDuplicate` | `gosrt_recv_byte_duplicate_total` | ✅ Added |
| `NakBeforeACKCount` | `gosrt_nak_before_ack_total` | ✅ Added |
| `NakSuppressedSeqs` | `gosrt_nak_suppressed_seqs_total` | ✅ Added (placeholder) |
| `NakAllowedSeqs` | `gosrt_nak_allowed_seqs_total` | ✅ Added (placeholder) |
| `RetransSuppressed` | `gosrt_retrans_suppressed_total` | ✅ Added (placeholder) |
| `RetransAllowed` | `gosrt_retrans_allowed_total` | ✅ Added (placeholder) |
| `RetransFirstTime` | `gosrt_retrans_first_time_total` | ✅ Added (placeholder) |

### 5.3 Defensive Check: NAK Sequence vs Last ACK ✅ IMPLEMENTED

**RFC Requirement:** The receiver should never send a NAK for a sequence number before the Last Acknowledged Packet Sequence Number (those packets are already confirmed received).

**Implementation in `congestion/live/send.go`:**
- Added `lastACKedSequence circular.Number` field to sender struct
- Updated `ackLocked()` to track highest ACK'd sequence
- Added defensive check in both `nakLockedOriginal()` and `nakLockedHonorOrder()`:

```go
// Defensive check: NAK should never request sequences before last ACK
if sequenceNumbers[i].Lt(s.lastACKedSequence) {
    m.NakBeforeACKCount.Add(1) // Metric tracks this condition
    continue // Skip this invalid request
}
```

Metric `NakBeforeACKCount` exported as `gosrt_nak_before_ack_total`.

---

## 6. Isolation Test Plan

### 6.1 New Test Configuration

Add to `contrib/integration_testing/test_configs.go`:

```go
// Isolation-1M-FullEventLoop-100ms-L5-Debug
// Designed to isolate and observe NAK suppression behavior
{
    Name:        "Isolation-1M-FullEventLoop-100ms-L5-Debug",
    Description: "DEBUG: 1 Mb/s with 100ms RTT (5x NAK interval) and 5% loss",
    ControlCG:     ControlSRTConfig,
    ControlServer: ControlSRTConfig,
    TestCG:        GetSRTConfig(ConfigFullEventLoop).WithReceiverDebug(),
    TestServer:    GetSRTConfig(ConfigFullEventLoop).WithReceiverDebug(),
    TestDuration:  60 * time.Second,
    Bitrate:       1_000_000,  // 1 Mb/s - low rate for easier analysis
    StatsPeriod:   5 * time.Second,
    LogTopics:     "receiver,control:send:ACK,control:recv:ACKACK,control:send:NAK",
}
```

### 6.2 Network Configuration

For isolation tests, add support for custom latency:
```go
Impairment: NetworkImpairment{
    LossRate:       0.05,      // 5% loss
    LatencyProfile: "custom",
    CustomRTTMs:    100,       // 100ms RTT (50ms one-way)
},
```

### 6.3 Expected Observations

With 100ms RTT and 20ms NAK interval:
- **NAKs per gap:** ~5 (100ms / 20ms)
- **Expected retransmit ratio:** ~2.5x-5x
- **Duplicate packets at receiver:** Should match sender's excess retransmissions

---

## 7. Design Options

### 7.1 Option A: Sender-Side Retransmit Suppression (Recommended)

**Concept:** Track when each packet was last retransmitted. Skip re-retransmission if the previous retransmit cannot have arrived yet.

#### 7.1.1 No Separate Map Needed

The sender already maintains a `lossList` (`container/list.List`) containing packets that may need retransmission. Each packet in the list is a `packet.Packet` with a `Header()`.

We can add tracking fields directly to the packet header (`packet/packet.go`):

```go
// In packet.Header struct (packet/packet.go)
type Header struct {
    // ... existing fields ...

    // Retransmit tracking (sender-side suppression)
    LastRetransmitTimeUs uint64  // Timestamp when last retransmitted (microseconds since connection start)
    RetransmitCount      uint32  // Number of times this packet has been retransmitted
}
```

#### 7.1.2 Packet Store Lookup

When a NAK arrives containing sequence numbers (singles or ranges), we iterate through the sender's `lossList` to find matching packets:

From `congestion/live/send.go:444-468` (current implementation):
```go
for e := s.lossList.Back(); e != nil; e = e.Prev() {
    p := e.Value.(packet.Packet)

    for i := 0; i < len(sequenceNumbers); i += 2 {
        if p.Header().PacketSequenceNumber.Gte(sequenceNumbers[i]) &&
           p.Header().PacketSequenceNumber.Lte(sequenceNumbers[i+1]) {
            // Found packet in NAK range - decide whether to retransmit
            // ... retransmit logic ...
        }
    }
}
```

**With suppression:**
```go
for e := s.lossList.Back(); e != nil; e = e.Prev() {
    p := e.Value.(packet.Packet)
    h := p.Header()

    for i := 0; i < len(sequenceNumbers); i += 2 {
        if h.PacketSequenceNumber.Gte(sequenceNumbers[i]) &&
           h.PacketSequenceNumber.Lte(sequenceNumbers[i+1]) {

            // NEW: Check if we should suppress this retransmit
            if s.shouldSuppressRetransmit(h, now) {
                m.RetransSuppressed.Add(1)
                continue // Skip - previous retransmit still in flight
            }

            // Update retransmit tracking
            h.LastRetransmitTimeUs = now
            h.RetransmitCount++

            // First-time retransmit metric
            if h.RetransmitCount == 1 {
                m.RetransFirstTime.Add(1)
            }

            // ... existing retransmit logic ...
        }
    }
}
```

#### 7.1.3 RTO-Based Suppression Logic with RTTVar

To make this design familiar to those with TCP experience, we use the term **RTO (Retransmission Timeout)** as defined in RFC 6298. The key insight is that we have **two different suppression contexts** with different timing requirements.

##### Two Suppression Contexts: Round-Trip vs One-Way

| Context | Location | Timing Basis | Purpose |
|---------|----------|--------------|---------|
| **NAK Suppression** | Receiver | Full RTO (round-trip) | "Don't re-NAK if NAK → Sender → Retransmit → Receiver hasn't completed" |
| **Retransmit Suppression** | Sender | Half RTO (one-way) | "Don't re-retransmit if Retransmit → Receiver hasn't completed" |

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    TIMING DIAGRAM: NAK vs RETRANSMIT                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  SENDER                                        RECEIVER                     │
│    │                                              │                         │
│    │◄───────────── NAK (seq 1000) ────────────────│ t=0: NAK sent           │
│    │                                              │                         │
│    │              (one-way delay)                 │                         │
│    │                                              │                         │
│    │─────────── Retransmit (seq 1000) ───────────►│ t=RTT/2: Retx sent      │
│    │                                              │                         │
│    │              (one-way delay)                 │                         │
│    │                                              │                         │
│    │                                              │ t=RTT: Retx arrives     │
│                                                                             │
│  ═══════════════════════════════════════════════════════════════════════    │
│                                                                             │
│  NAK SUPPRESSION (Receiver):                                                │
│    "Should I send another NAK for seq 1000?"                                │
│    → Wait FULL RTO before re-NAKing (NAK → Sender → Retx → back to me)      │
│                                                                             │
│  RETRANSMIT SUPPRESSION (Sender):                                           │
│    "Should I retransmit seq 1000 again?"                                    │
│    → Wait HALF RTO before re-retransmitting (Retx → Receiver one-way)       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

##### RTO Calculation Modes (RFC 6298 Inspired)

We calculate the **base RTO** (round-trip) using one of these modes, then apply `*0.5` for one-way suppression:

| Mode | RTO Formula | RFC Reference | Use Case |
|------|-------------|---------------|----------|
| `RTORttRttVar` | `RTT + RTTVar` | Balanced | Default, good for most networks |
| `RTORtt4RttVar` | `RTT + 4*RTTVar` | RFC 6298 | Conservative, TCP-compatible |
| `RTORttRttVarMargin` | `(RTT + RTTVar) * (1 + ExtraMargin)` | Configurable | Fine-tuned for specific deployments |

**RFC 6298 Section 2** defines TCP's RTO calculation:
> RTO = SRTT + max(G, K*RTTVAR) where K=4

We simplify by omitting the clock granularity (G) since SRT uses microsecond timestamps.

##### Performance-Optimized Configuration

**Problem:** `CalculateRTO()` and `OneWayDelay()` will be called frequently (every NAK entry, every retransmit candidate). We must avoid:
1. String comparisons (`mode == "rtt_rttvar"`)
2. Percentage conversions (`margin / 100.0`)
3. Function dispatch overhead

**Solution:** Use an **enum/function type** configured once per connection. Store the calculation function as a function pointer at connection setup.

```go
// config.go - RTO Mode Types

// RTOMode defines the RTO calculation strategy (enum for performance)
type RTOMode uint8

const (
    RTORttRttVar      RTOMode = iota // RTT + RTTVar (balanced default)
    RTORtt4RttVar                     // RTT + 4*RTTVar (RFC 6298 conservative)
    RTORttRttVarMargin                // (RTT + RTTVar) * (1 + margin)
)

// Config options
type Config struct {
    // RTO Calculation Mode (enum, not string)
    RTOMode               RTOMode  // RTORttRttVar, RTORtt4RttVar, RTORttRttVarMargin

    // Extra margin as a multiplier (0.1 = 10%, avoids division in hot path)
    // Only used with RTORttRttVarMargin mode
    ExtraRTTMargin        float64  // e.g., 0.1 for 10% extra margin
}

const (
    DefaultRTOMode       = RTORttRttVar
    DefaultExtraRTTMargin = 0.10  // 10% extra margin (stored as 0.1, not 10)
)
```

##### High-Performance RTO Calculation

**File:** `connection_rtt.go`

```go
// RTOCalculator is a function type for RTO calculation.
// Configured once per connection to avoid runtime mode checks.
type RTOCalculator func(rttBits, rttVarBits uint64) uint64

// rtt struct with pre-configured calculator (set once at connection setup)
type rtt struct {
    rttBits          atomic.Uint64 // float64 stored as bits
    rttVarBits       atomic.Uint64 // float64 stored as bits
    minNakIntervalUs atomic.Uint64 // minimum NAK interval in microseconds

    // Pre-configured RTO calculator (set at connection setup, never changes)
    // This avoids mode switch on every call
    rtoCalculator    RTOCalculator
    oneWayCalculator RTOCalculator  // Pre-computed: rtoCalculator * 0.5
}

// SetRTOMode configures the RTO calculator at connection setup.
// Called once during connection initialization - not in hot path.
func (r *rtt) SetRTOMode(mode RTOMode, extraMargin float64) {
    // Pre-compute the margin multiplier: (1 + extraMargin)
    marginMultiplier := 1.0 + extraMargin

    switch mode {
    case RTORttRttVar:
        r.rtoCalculator = func(rttBits, rttVarBits uint64) uint64 {
            rttVal := math.Float64frombits(rttBits)
            rttVarVal := math.Float64frombits(rttVarBits)
            return uint64(rttVal + rttVarVal)
        }
    case RTORtt4RttVar:
        r.rtoCalculator = func(rttBits, rttVarBits uint64) uint64 {
            rttVal := math.Float64frombits(rttBits)
            rttVarVal := math.Float64frombits(rttVarBits)
            return uint64(rttVal + 4.0*rttVarVal)
        }
    case RTORttRttVarMargin:
        // Capture marginMultiplier in closure (computed once, used many times)
        r.rtoCalculator = func(rttBits, rttVarBits uint64) uint64 {
            rttVal := math.Float64frombits(rttBits)
            rttVarVal := math.Float64frombits(rttVarBits)
            return uint64((rttVal + rttVarVal) * marginMultiplier)
        }
    default:
        // Default to balanced
        r.rtoCalculator = func(rttBits, rttVarBits uint64) uint64 {
            rttVal := math.Float64frombits(rttBits)
            rttVarVal := math.Float64frombits(rttVarBits)
            return uint64(rttVal + rttVarVal)
        }
    }

    // Pre-configure one-way calculator (*0.5 instead of /2 for performance)
    r.oneWayCalculator = func(rttBits, rttVarBits uint64) uint64 {
        return uint64(float64(r.rtoCalculator(rttBits, rttVarBits)) * 0.5)
    }
}

// CalculateRTO returns RTO in microseconds (hot path - no mode checks)
func (r *rtt) CalculateRTO() uint64 {
    return r.rtoCalculator(r.rttBits.Load(), r.rttVarBits.Load())
}

// OneWayDelay returns RTO*0.5 in microseconds (hot path - no mode checks)
// Uses *0.5 instead of /2 for guaranteed float multiplication (no integer division)
func (r *rtt) OneWayDelay() uint64 {
    return r.oneWayCalculator(r.rttBits.Load(), r.rttVarBits.Load())
}
```

**Performance comparison:**

| Approach | Operations per call | Notes |
|----------|---------------------|-------|
| String switch (`mode == "rtt_rttvar"`) | 3-6 string comparisons | Slow |
| Enum switch | 1 integer comparison + jump table | Medium |
| Function pointer (chosen) | 1 indirect call | Fastest, no branching |

##### Call Site 1: Sender Retransmit Suppression

**File:** `congestion/live/send.go`
**Function:** `nakLockedHonorOrder()` (lines 484-536)
**Current code reference:** Lines 509-527 (retransmit loop)

**Current implementation:**
```go
// congestion/live/send.go:509-527 (CURRENT)
if pktSeq.Gte(startSeq) && pktSeq.Lte(endSeq) {
    pktLen := p.Len()
    m.CongestionSendPktRetrans.Add(1)
    m.CongestionSendPkt.Add(1)
    // ... metrics and deliver ...
    p.Header().RetransmittedPacketFlag = true
    s.deliver(p)
    retransCount++
}
```

**Proposed implementation with suppression:**
```go
// congestion/live/send.go:509+ (PROPOSED)
if pktSeq.Gte(startSeq) && pktSeq.Lte(endSeq) {
    h := p.Header()

    // ──────────────────────────────────────────────────────────────────
    // RETRANSMIT SUPPRESSION CHECK
    // Only allow retransmit if enough time has passed for previous
    // retransmit to reach the receiver (one-way delay).
    // ──────────────────────────────────────────────────────────────────
    if h.LastRetransmitTimeUs > 0 {
        oneWayThreshold := s.rtt.OneWayDelay()  // Pre-configured, no args
        timeSinceRetransmit := now - h.LastRetransmitTimeUs

        if timeSinceRetransmit < oneWayThreshold {
            // Too soon - previous retransmit still in flight
            m.RetransSuppressed.Add(1)
            continue  // Skip this packet, check next in range
        }
    }

    // ──────────────────────────────────────────────────────────────────
    // PROCEED WITH RETRANSMIT
    // ──────────────────────────────────────────────────────────────────

    // Update retransmit tracking BEFORE sending
    h.LastRetransmitTimeUs = now
    h.RetransmitCount++

    // Metrics
    if h.RetransmitCount == 1 {
        m.RetransFirstTime.Add(1)
    }
    m.RetransAllowed.Add(1)

    pktLen := p.Len()
    m.CongestionSendPktRetrans.Add(1)
    m.CongestionSendPkt.Add(1)
    m.CongestionSendByteRetrans.Add(uint64(pktLen))
    m.CongestionSendByte.Add(uint64(pktLen))

    // Update running average payload size
    s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)

    // Phase 1: Lockless - use atomic counters
    m.SendRateBytesSent.Add(pktLen)
    m.SendRateBytesRetrans.Add(pktLen)

    h.RetransmittedPacketFlag = true
    s.deliver(p)

    retransCount++
}
```

**Note:** The same change applies to `nakLockedOriginal()` (lines 444-468).

##### Call Site 2: Receiver NAK Suppression (NAK Btree Consolidation)

**File:** `congestion/live/receive/nak_consolidate.go`
**Function:** `consolidateNakBtree()` (lines 43-123)
**Current code reference:** Lines 71-107 (btree iteration loop)

**Current implementation:**
```go
// nak_consolidate.go:71-107 (CURRENT)
r.nakBtree.Iterate(func(seq uint32) bool {
    // Time budget check every 100 iterations
    iterCount++
    if iterCount%100 == 0 {
        if time.Now().After(deadline) {
            r.metrics.NakConsolidationTimeout.Add(1)
            return false
        }
    }

    if currentEntry == nil {
        currentEntry = &NAKEntry{Start: seq, End: seq}
        return true
    }

    // Consolidation logic...
    gap := circular.SeqDiff(seq, currentEntry.End) - 1
    if gap >= 0 && uint32(gap) <= r.nakMergeGap {
        currentEntry.End = seq
        r.metrics.NakConsolidationMerged.Add(1)
    } else {
        entries = append(entries, *currentEntry)
        currentEntry = &NAKEntry{Start: seq, End: seq}
    }

    return true
})
```

**Issue:** The NAK btree currently stores only `uint32` sequence numbers with no timestamp tracking.

**Solution:** Add `NakEntry` struct with suppression tracking to the NAK btree.

**Step 1: Update NAKEntry struct**

**File:** `congestion/live/receive/nak_consolidate.go` (add new struct)

```go
// NakEntryWithTime stores a NAK entry with suppression tracking.
// Used for NAK btree to track when each sequence was last NAK'd.
type NakEntryWithTime struct {
    Seq           uint32  // Missing sequence number
    LastNakedAtUs uint64  // When we last sent NAK for this seq
    NakCount      uint32  // Number of times NAK'd
}
```

**Step 2: Update nakBtree to use NakEntryWithTime**

**File:** `congestion/live/receive/nak_btree.go`

```go
// nakBtree stores missing sequence numbers with suppression tracking.
type nakBtree struct {
    tree *btree.BTreeG[NakEntryWithTime]  // Changed from uint32
    mu   sync.RWMutex
    rtt  *rtt  // Reference to connection's RTT tracker
}

func newNakBtree(degree int, rtt *rtt) *nakBtree {
    return &nakBtree{
        tree: btree.NewG(degree, func(a, b NakEntryWithTime) bool {
            return circular.SeqLess(a.Seq, b.Seq)
        }),
        rtt: rtt,
    }
}
```

**Step 3: Update consolidateNakBtree with suppression**

**File:** `congestion/live/receive/nak_consolidate.go`

```go
// consolidateNakBtree converts NAK btree entries into ranges with suppression.
// Calculates RTO ONCE before traversal (performance optimization).
func (r *receiver) consolidateNakBtree(now uint64) []circular.Number {
    if r.nakBtree == nil || r.nakBtree.Len() == 0 {
        return nil
    }

    // ──────────────────────────────────────────────────────────────────
    // CALCULATE RTO ONCE BEFORE TRAVERSAL (performance optimization)
    // This avoids calling CalculateRTO() for every entry.
    // ──────────────────────────────────────────────────────────────────
    rtoThreshold := r.rtt.CalculateRTO()  // Pre-configured, no args

    deadline := time.Now().Add(r.nakConsolidationBudget)

    entriesPtr := nakEntryPool.Get().(*[]NAKEntry)
    entries := *entriesPtr
    defer func() {
        *entriesPtr = entries[:0]
        nakEntryPool.Put(entriesPtr)
    }()

    var currentEntry *NAKEntry
    iterCount := 0
    var suppressedCount uint64
    var allowedCount uint64

    r.nakBtree.Iterate(func(entry *NakEntryWithTime) bool {
        iterCount++
        if iterCount%100 == 0 && time.Now().After(deadline) {
            r.metrics.NakConsolidationTimeout.Add(1)
            return false
        }

        // ──────────────────────────────────────────────────────────────
        // NAK SUPPRESSION CHECK
        // Skip entries where the full round-trip (NAK → Sender → Retx → Us)
        // hasn't had time to complete.
        // ──────────────────────────────────────────────────────────────
        if entry.LastNakedAtUs > 0 {
            timeSinceNAK := now - entry.LastNakedAtUs
            if timeSinceNAK < rtoThreshold {
                // Too soon - round-trip hasn't completed
                suppressedCount++
                return true  // Continue to next entry
            }
        }

        // ──────────────────────────────────────────────────────────────
        // INCLUDE IN NAK - update tracking
        // ──────────────────────────────────────────────────────────────
        entry.LastNakedAtUs = now
        entry.NakCount++
        allowedCount++

        // Standard consolidation logic
        if currentEntry == nil {
            currentEntry = &NAKEntry{Start: entry.Seq, End: entry.Seq}
            return true
        }

        gap := circular.SeqDiff(entry.Seq, currentEntry.End) - 1
        if gap >= 0 && uint32(gap) <= r.nakMergeGap {
            currentEntry.End = entry.Seq
            r.metrics.NakConsolidationMerged.Add(1)
        } else {
            entries = append(entries, *currentEntry)
            currentEntry = &NAKEntry{Start: entry.Seq, End: entry.Seq}
        }

        return true
    })

    // Emit final entry
    if currentEntry != nil {
        entries = append(entries, *currentEntry)
    }

    // Update metrics (single atomic update per run)
    if r.metrics != nil {
        r.metrics.NakConsolidationRuns.Add(1)
        r.metrics.NakConsolidationEntries.Add(uint64(len(entries)))
        r.metrics.NakSuppressedSeqs.Add(suppressedCount)
        r.metrics.NakAllowedSeqs.Add(allowedCount)
    }

    return r.entriesToNakList(entries)
}
```

**Step 4: Update periodicNakBtree to pass `now`**

**File:** `congestion/live/receive/nak.go:186+`

```go
// periodicNakBtree scans the packet btree to find gaps and builds NAK list.
func (r *receiver) periodicNakBtree(now uint64) []circular.Number {
    // ... interval checks ...

    // Call consolidateNakBtree with current time for suppression
    return r.consolidateNakBtree(now)  // Pass now for suppression checks
}
```

##### Example Calculations

**Scenario:** RTT = 100ms, RTTVar = 10ms

| Mode | RTO (Round-Trip) | One-Way (*0.5) |
|------|------------------|-----------------|
| `RTORttRttVar` | 110ms | 55ms |
| `RTORtt4RttVar` | 140ms | 70ms |
| `RTORttRttVarMargin` (0.1) | 121ms | 60.5ms |

**GEO Satellite:** RTT = 300ms, RTTVar = 30ms

| Mode | RTO (Round-Trip) | One-Way (*0.5) |
|------|------------------|-----------------|
| `RTORttRttVar` | 330ms | 165ms |
| `RTORtt4RttVar` | 420ms | 210ms |
| `RTORttRttVarMargin` (0.1) | 363ms | 181.5ms |

##### TSBPD Consideration: Why We Don't Use Progressive Backoff

Unlike TCP, SRT has **Timestamp-Based Packet Delivery (TSBPD)** which imposes a hard deadline for packet arrival. Packets must arrive before their TSBPD time or they will be dropped.

**TCP approach (NOT recommended for SRT):**
- Progressive/exponential backoff: increase timeout with each retry
- Works for TCP because there's no hard delivery deadline

**Why this doesn't work for SRT:**
- TSBPD buffer has a fixed latency budget
- Delaying retransmits makes packets MORE likely to miss the deadline
- We want retransmits to happen ASAP within the suppression window

**Future Consideration:** If the current RTO-based suppression proves too aggressive (causing unnecessary duplicates), progressive backoff could be reconsidered. However, given TSBPD constraints, any backoff should be minimal (e.g., 5% increase per retry, capped at 2x base RTO).

##### Metrics for Suppression Analysis

```go
// metrics/metrics.go - Suppression metrics
RetransSuppressed      atomic.Uint64  // Retransmits suppressed (sender)
RetransAllowed         atomic.Uint64  // Retransmits that passed threshold (sender)
RetransFirstTime       atomic.Uint64  // First-time retransmits (sender)
NakSuppressedSeqs      atomic.Uint64  // NAK entries suppressed (receiver)
NakAllowedSeqs         atomic.Uint64  // NAK entries that passed threshold (receiver)
```

**Add to `metrics/handler.go`:**
```go
writeCounterIfNonZero(b, "gosrt_retrans_suppressed_total",
    metrics.RetransSuppressed.Load(), "", connLabels)
writeCounterIfNonZero(b, "gosrt_retrans_allowed_total",
    metrics.RetransAllowed.Load(), "", connLabels)
writeCounterIfNonZero(b, "gosrt_retrans_first_time_total",
    metrics.RetransFirstTime.Load(), "", connLabels)
writeCounterIfNonZero(b, "gosrt_nak_suppressed_seqs_total",
    metrics.NakSuppressedSeqs.Load(), "", connLabels)
writeCounterIfNonZero(b, "gosrt_nak_allowed_seqs_total",
    metrics.NakAllowedSeqs.Load(), "", connLabels)
```

**Add to `metrics/handler_test.go`:**
```go
func TestSuppressionMetrics(t *testing.T) {
    m := metrics.NewConnectionMetrics()

    m.RetransSuppressed.Add(100)
    m.RetransAllowed.Add(50)
    m.RetransFirstTime.Add(40)
    m.NakSuppressedSeqs.Add(75)
    m.NakAllowedSeqs.Add(25)

    output := exportMetrics(m)
    assert.Contains(t, output, "gosrt_retrans_suppressed_total 100")
    assert.Contains(t, output, "gosrt_retrans_allowed_total 50")
    assert.Contains(t, output, "gosrt_retrans_first_time_total 40")
    assert.Contains(t, output, "gosrt_nak_suppressed_seqs_total 75")
    assert.Contains(t, output, "gosrt_nak_allowed_seqs_total 25")
}
```

#### 7.1.4 Concurrency Considerations

The sender already uses `sync.RWMutex` to protect the `lossList`:

```go
// From send.go
func (s *sender) NAK(sequenceNumbers []circular.Number) uint64 {
    s.lock.Lock()
    defer s.lock.Unlock()
    return s.nakLocked(sequenceNumbers)
}
```

Since we modify packet headers during NAK processing (which holds the write lock), there's no race condition. The packet headers are only modified while holding `s.lock`.

**Concurrent access safety:**
| Operation | Lock Held | Safe? |
|-----------|-----------|-------|
| NAK processing (modify header) | `s.lock` (write) | ✓ |
| ACK processing (remove from lossList) | `s.lock` (write) | ✓ |
| Push new packet | `s.lock` (write) | ✓ |
| Read statistics | `s.lock` (read) | ✓ |

#### 7.1.5 Both List and BTree Support

The sender's `lossList` is used for both the list and btree packet reorder algorithms. The suppression logic operates at the sender level, not the packet store level, so it works with both modes.

**Note:** The list mode uses `container/list.List`, the btree mode for the sender would also use a similar approach with sequence-indexed lookup.

**Pros:**
- No protocol changes required
- Backward compatible
- Works regardless of receiver behavior
- Leverages existing RTT information
- **No separate tracking map needed** - uses existing packet header
- **No memory overhead** - fields added to existing header
- **No cleanup needed** - fields are on the packet, removed when packet is ACK'd

**Cons:**
- Modifies packet header structure (minor)
- Small increase in header size (~12 bytes)

### 7.2 Option B: Receiver-Side NAK Throttling

**Concept:** Track when each gap was last NAK'd. Only re-NAK if enough time has passed.

**Implementation:**

Extend NAK btree entry:
```go
type NakEntry struct {
    Sequence    uint32
    InsertedAt  uint64  // When gap was first detected
    LastNakedAt uint64  // When gap was last NAK'd
    NakCount    uint32  // How many times NAK'd
}
```

**Throttling Logic:**
```go
func (n *NakBtree) shouldIncludeInNAK(entry *NakEntry, now uint64) bool {
    if entry.LastNakedAt == 0 {
        return true // Never NAK'd, include
    }

    // Only re-NAK if previous NAK has had time to reach sender and return (full RTT)
    minNakInterval := uint64(r.rtt.NAKInterval())

    if now - entry.LastNakedAt < minNakInterval {
        r.metrics.NakSuppressedSeqs.Add(1)
        return false // Too soon
    }

    return true
}
```

**Pros:**
- Reduces NAK packet volume (bandwidth savings)
- Naturally adapts to RTT

**Cons:**
- Requires modifying NAK btree structure
- Risk of delayed recovery if suppression is too aggressive

### 7.3 Option C: RTT-Aware NAK Interval

**Concept:** Dynamically adjust the Periodic NAK interval based on RTT.

**Implementation:**
```go
func (r *receiver) calculateDynamicNAKInterval() time.Duration {
    rtt := r.rtt.RTT() // microseconds

    // NAK interval should be at least RTT/2 to avoid duplicates
    minInterval := time.Duration(rtt/2) * time.Microsecond

    // But not less than configured minimum
    if minInterval < r.config.PeriodicNakIntervalMs * time.Millisecond {
        return r.config.PeriodicNakIntervalMs * time.Millisecond
    }

    return minInterval
}
```

**Pros:**
- Simple conceptually
- Reduces all NAK-related overhead proportionally

**Cons:**
- Delays gap detection at high RTT
- May hurt recovery latency

### 7.4 Option D: Hybrid Approach (Best of A + B)

**Concept:** Implement both sender-side suppression AND receiver-side throttling for maximum efficiency.

1. **Receiver:** Track `lastNakedAt` per entry, only re-NAK if RTT/2 has passed
2. **Sender:** Track `lastRetransmitTime` per packet, skip if retransmit in flight

**Pros:**
- Maximum bandwidth savings
- Handles edge cases from both sides
- Graceful degradation (if one side doesn't implement, other still helps)

**Cons:**
- More complex implementation
- More metrics to track

---

## 8. Recommended Implementation Plan

### Phase 1: Observability (This PR)

1. **Fix duplicate handling bug** in `packet_store_btree.go`
2. **Add duplicate packet metrics:**
   - `CongestionRecvPktDuplicate`
   - `CongestionRecvByteDuplicate`
3. **Add NAK defensive check metric:**
   - `NakBeforeACKCount`
4. **Create isolation test:**
   - `Isolation-1M-FullEventLoop-100ms-L5-Debug`
5. **Run tests** to baseline current behavior

### Phase 2: Sender-Side Suppression (Option A)

1. **Add retransmit tracking** to sender
2. **Implement suppression logic** with RTT-aware threshold
3. **Add metrics:**
   - `RetransSuppressed`
   - `RetransFirstTime`
4. **Test with isolation config** to verify improvement
5. **Run full test matrix** to confirm no regression

### Phase 3: Receiver-Side Throttling (Option B) - Optional

1. **Extend NAK btree entry** with timing information
2. **Implement NAK throttling** logic
3. **Add metrics:**
   - `NakSuppressedSeqs`
4. **Test and validate**

### Phase 4: Optimization and Tuning

1. **Profile memory usage** of tracking structures
2. **Tune thresholds** based on test results
3. **Document final configuration recommendations**

---

## 9. Metrics Implementation Checklist

Per `metrics_and_statistics_design.md`, for each new metric:

- [ ] Add to `metrics/metrics.go` (atomic field in `ConnectionMetrics`)
- [ ] Add to `metrics/handler.go` (Prometheus export)
- [ ] Add to `metrics/handler_test.go` (test coverage)
- [ ] Run `make audit-metrics` to verify consistency
- [ ] Update documentation

### New Metrics Summary

| Metric | Type | Location | Purpose |
|--------|------|----------|---------|
| `gosrt_recv_pkt_duplicate_total` | Counter | Receiver | Track duplicate data packets |
| `gosrt_recv_byte_duplicate_total` | Counter | Receiver | Track duplicate data bytes |
| `gosrt_nak_before_ack_total` | Counter | Sender | Detect receiver bugs |
| `gosrt_retrans_suppressed_total` | Counter | Sender | Track suppression effectiveness |
| `gosrt_nak_suppressed_seqs_total` | Counter | Receiver | Track NAK throttling (Phase 3) |

---

## 10. Test Matrix

### Isolation Tests for Development

| Test Name | RTT | Loss | Bitrate | Purpose |
|-----------|-----|------|---------|---------|
| `Isolation-1M-FullEventLoop-100ms-L5-Debug` | 100ms | 5% | 1 Mb/s | Primary debug test |
| `Isolation-1M-FullEventLoop-300ms-L5-Debug` | 300ms | 5% | 1 Mb/s | GEO satellite simulation |
| `Isolation-1M-FullEventLoop-0ms-L5-Debug` | 0ms | 5% | 1 Mb/s | Baseline (no suppression needed) |

### Parallel Comparison Tests for Validation

| Test Name | RTT | Loss | Expected Improvement |
|-----------|-----|------|---------------------|
| `Parallel-Loss-L5-20M-Base-vs-FullEL-Continental` | 60ms | 5% | 2.48x → ~1.2x |
| `Parallel-Loss-L5-20M-Base-vs-FullEL-GEO` | 300ms | 5% | 2.48x → ~1.2x |

---

## 11. Success Criteria

1. **Retransmit ratio < 1.3x** at all RTT values (currently 1.56x-2.48x)
2. **Zero false suppressions** (all actual lost packets still recovered)
3. **Recovery latency unchanged** (no increase in time-to-recover)
4. **Memory overhead < 1KB per connection** for tracking structures
5. **CPU overhead negligible** (< 1% increase)

---

## 12. Open Questions

1. Should sender suppression be configurable (enable/disable)?
2. What's the optimal suppression threshold (RTT/2? RTT/4?)?
3. Should we add a "NAK generation rate" metric for debugging?
4. How do we handle RTT changes during suppression window?

---

## Appendix A: Code Locations

| Component | File | Function |
|-----------|------|----------|
| Sender NAK handling | `congestion/live/send.go` | `nakLocked*()` |
| Sender ACK handling | `congestion/live/send.go` | `ackLocked()` |
| Receiver NAK generation | `congestion/live/receive/nak.go` | `periodicNakBtree()` |
| Receiver ACK generation | `congestion/live/receive/ack.go` | `periodicACKLocked()` |
| Packet btree insert | `congestion/live/receive/packet_store_btree.go` | `Insert()` |
| RTT calculation | `connection_rtt.go` | `Recalculate()` |
| RTT update (receiver) | `connection_handlers.go:384` | `handleACKACK()` |
| RTT update (sender) | `connection_handlers.go:282` | `handleACK()` |
| Packet decommission | `packet/packet.go` | `Decommission()` |

## Appendix B: Related Documents

- [parallel_tests_defects.md](./parallel_tests_defects.md) - Test results showing the problem
- [design_nak_btree.md](./design_nak_btree.md) - NAK btree design
- [ack_optimization_plan.md](./ack_optimization_plan.md) - ACK/ACKACK optimization
- [metrics_and_statistics_design.md](./metrics_and_statistics_design.md) - Metrics implementation guide

