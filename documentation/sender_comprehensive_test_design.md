# Comprehensive Sender Test Design

**Created:** 2026-01-08
**Status:** IMPLEMENTATION READY
**Goal:** Achieve parity with receiver test coverage
**Related:** [Bug Document](send_eventloop_intermittent_failure_bug.md) - Contains detailed test specifications

---

## Overview

This document provides a high-level view of the sender test implementation plan.
For detailed test case specifications, see `send_eventloop_intermittent_failure_bug.md` Section 7.

---

## 1. Current State Analysis

### 1.1 Sender Tests (5,544 lines)
```
send_packet_btree_test.go          1512 lines  ← Good coverage
eventloop_test.go                   754 lines  ← Basic coverage
sender_test.go                      695 lines  ← Legacy tests
nak_table_test.go                   448 lines  ← NAK retransmit
control_ring_test.go                473 lines  ← Ring tests
data_ring_test.go                   422 lines  ← Ring tests
control_ring_v2_test.go             288 lines  ← Ring tests
drop_threshold_test.go              270 lines  ← Drop logic
```

### 1.2 Receiver Tests (19,973 lines) - Reference
```
nak_btree_scan_stream_test.go      1921 lines  ← Streaming scenarios
eventloop_test.go                  1976 lines  ← Comprehensive EL tests
nak_large_merge_ack_test.go        1275 lines  ← Complex NAK scenarios
loss_recovery_table_test.go        1064 lines  ← Table-driven recovery
receive_race_test.go               1006 lines  ← Concurrency tests
tsbpd_advancement_test.go           737 lines  ← TSBPD edge cases
ack_periodic_table_test.go          387 lines  ← Table-driven ACK
```

### 1.3 Critical Gap: Initialization Tests

**The bug we found:** `deliveryStartPoint` is never initialized, defaults to 0 while `nextSequenceNumber` is ~549M.

**Missing tests:**
- Sender initialization with various ISN values
- Tracking point initialization (deliveryStartPoint, contiguousPoint)
- Time base initialization (nowFn vs PktTsbpdTime)

---

## 2. Test Categories Needed

### Category 1: Initialization Tests (`sender_init_test.go`)

Test that all critical fields are properly initialized.

```go
type SenderInitTestCase struct {
    Name string

    // Config
    InitialSequenceNumber uint32
    UseBtree              bool
    UseRing               bool
    UseControlRing        bool
    UseEventLoop          bool
    StartTime             time.Time

    // Expected state after NewSender()
    ExpectedNextSeq          uint32
    ExpectedDeliveryStart    uint32  // CRITICAL: Must match ISN
    ExpectedContiguousPoint  uint32
    ExpectedNowFnRelative    bool    // nowFn uses relative time
}
```

**Test Cases:**
| Case | ISN | Expected deliveryStartPoint |
|------|-----|----------------------------|
| `ISN_Zero` | 0 | 0 |
| `ISN_Middle` | 1000 | 1000 |
| `ISN_Random_549M` | 549144712 | 549144712 |
| `ISN_NearMax` | 2147483640 | 2147483640 |
| `ISN_AtMax` | 2147483647 | 2147483647 |
| `ISN_Wraparound` | 0 (after wrap) | 0 |

### Category 2: Delivery Table Tests (`sender_delivery_table_test.go`)

Test `deliverReadyPacketsEventLoop` with various scenarios.

```go
type DeliveryTestCase struct {
    Name string

    // Setup
    InitialSequenceNumber uint32
    DeliveryStartPoint    uint32   // Where delivery scan starts
    PacketSequences       []uint32 // Packets in btree
    PacketTsbpdTimes      []uint64 // TSBPD times (relative µs)
    NowUs                 uint64   // Current time (relative µs)

    // Expected
    ExpectedDelivered      int
    ExpectedNextDeliveryIn time.Duration
    ExpectedIterStarted    bool     // Did IterateFrom find anything?
    ExpectedNewStartPoint  uint32
}
```

**Test Cases:**
| Case | ISN | StartPoint | Packets | Now | Delivered |
|------|-----|------------|---------|-----|-----------|
| `Empty_Btree` | 0 | 0 | [] | 1000 | 0 |
| `All_Ready` | 0 | 0 | [0,1,2] | 1000 | 3 |
| `None_Ready` | 0 | 0 | [0,1,2] | 0 | 0 |
| `Partial_Ready` | 0 | 0 | [0,1,2] | 500 | 1 |
| **`ISN_Mismatch`** | 549M | 0 | [549M] | 1000 | 0 (BUG!) |
| `ISN_Correct` | 549M | 549M | [549M] | 1000 | 1 |
| `Wraparound` | MAX-5 | MAX-5 | [MAX-5..MAX,0,1] | 1000 | 8 |

### Category 3: TSBPD Timing Tests (`sender_tsbpd_table_test.go`)

Test TSBPD time comparisons.

```go
type TsbpdTestCase struct {
    Name string

    // Setup
    ConnectionStartTime   time.Time
    PacketCreationOffset  time.Duration // When packet was created
    DeliveryCheckOffset   time.Duration // When delivery is checked

    // Expected
    ShouldBeReady bool
    Reason        string
}
```

**Test Cases:**
| Case | Create | Check | Ready? |
|------|--------|-------|--------|
| `Immediate_Ready` | 0ms | 1ms | Yes |
| `Not_Yet` | 0ms | 0ms | No (equal) |
| `Future_Packet` | 100ms | 50ms | No |
| `Past_Packet` | 0ms | 100ms | Yes |
| `Early_Connection` | 0ms | 0ms | No |

### Category 4: Drop Threshold Table Tests (`sender_drop_table_test.go`)

Already has `drop_threshold_test.go`, but add table-driven edge cases.

```go
type DropThresholdTestCase struct {
    Name string

    // Setup
    NowUs          uint64
    DropThreshold  uint64
    PacketTsbpdUs  uint64

    // Expected
    ShouldCalculate bool   // threshold calculation valid?
    ShouldDrop      bool   // packet should be dropped?
    Reason          string
}
```

**Test Cases:**
| Case | Now | Threshold | Pkt TSBPD | Should Drop? |
|------|-----|-----------|-----------|--------------|
| `Early_Connection` | 500ms | 1000ms | 0 | No (underflow guard) |
| `Normal_Old` | 2000ms | 1000ms | 500ms | Yes |
| `Normal_Recent` | 2000ms | 1000ms | 1500ms | No |
| `Exact_Boundary` | 1000ms | 1000ms | 0 | Yes (edge) |
| `Just_Before` | 999ms | 1000ms | 0 | No |

### Category 5: ACK Processing Table Tests (`sender_ack_table_test.go`)

Test `ackBtree` / `processACK` with various scenarios.

```go
type ACKProcessingTestCase struct {
    Name string

    // Setup
    InitialSequenceNumber uint32
    PacketsInBtree        []uint32
    ACKSequence           uint32

    // Expected
    ExpectedRemoved       int
    ExpectedBtreeLen      int
    ExpectedContiguous    uint32
}
```

**Test Cases:**
| Case | ISN | Packets | ACK | Removed |
|------|-----|---------|-----|---------|
| `ACK_All` | 0 | [0,1,2,3,4] | 5 | 5 |
| `ACK_Partial` | 0 | [0,1,2,3,4] | 3 | 3 |
| `ACK_None` | 0 | [5,6,7] | 3 | 0 |
| `ACK_Before_ISN` | 100 | [100,101,102] | 99 | 0 |
| `ACK_Wraparound` | MAX-2 | [MAX-2,MAX-1,MAX,0,1] | 1 | 4 |

### Category 6: NAK Processing Table Tests (`sender_nak_table_test.go`)

Already exists, but add wraparound cases.

### Category 7: Sequence Wraparound Tests (`sender_wraparound_table_test.go`)

Comprehensive 31-bit wraparound testing for all sender operations.

```go
type WraparoundTestCase struct {
    Name string

    // Setup
    InitialSequenceNumber uint32
    PacketCount           int
    OperationType         string // "deliver", "ack", "nak", "drop"

    // Expected
    ShouldWrap            bool
    PostWrapBehavior      string
}
```

**Critical Wraparound Scenarios:**
| Case | ISN | Operation | Packets Cross Wrap? |
|------|-----|-----------|---------------------|
| `Near_Max_Deliver` | MAX-100 | deliver | Yes |
| `Near_Max_ACK` | MAX-100 | ack | Yes |
| `Near_Max_NAK` | MAX-100 | nak | Yes |
| `Post_Wrap_Delivery` | 0 | deliver | After wrap |
| `Btree_Order_At_Wrap` | MAX-10 | iterate | Yes |

### Category 8: EventLoop Integration Tests (`sender_eventloop_integration_test.go`)

End-to-end tests for the complete EventLoop flow.

```go
type EventLoopIntegrationTestCase struct {
    Name string

    // Setup
    InitialSequenceNumber uint32
    PacketsToSend         int
    SimulatedDuration     time.Duration
    ACKsToSend            []uint32
    NAKsToSend            [][]uint32

    // Expected
    ExpectedDelivered     int
    ExpectedDropped       int
    ExpectedRetransmitted int
}
```

### Category 9: Ring Interaction Tests (`sender_ring_flow_test.go`)

Test the Push → Ring → Btree → Deliver flow.

```go
type RingFlowTestCase struct {
    Name string

    // Setup
    InitialSequenceNumber uint32
    PushCount             int
    DrainCount            int

    // Expected
    ExpectedInRing        int
    ExpectedInBtree       int
    ExpectedSequences     []uint32
}
```

### Category 10: Race Condition Tests (`sender_race_test.go`)

Concurrent access patterns (with -race flag).

---

## 3. Priority Order

### P0 - Critical (Must Fix Bug)
1. **`sender_init_test.go`** - Initialization tests including `deliveryStartPoint`
2. **`sender_delivery_table_test.go`** - ISN mismatch scenarios

### P1 - High Priority
3. **`sender_wraparound_table_test.go`** - 31-bit wraparound
4. **`sender_tsbpd_table_test.go`** - TSBPD timing
5. **`sender_ack_table_test.go`** - ACK processing

### P2 - Medium Priority
6. **`sender_eventloop_integration_test.go`** - Full flow
7. **`sender_ring_flow_test.go`** - Ring interactions
8. **`sender_race_test.go`** - Concurrency

---

## 4. Test Helper Patterns

### 4.1 Sender Factory with ISN

```go
// createSenderWithISN creates a sender with specific initial sequence number
func createSenderWithISN(t *testing.T, isn uint32) *sender {
    m := &metrics.ConnectionMetrics{}
    start := time.Now()

    s := NewSender(SendConfig{
        InitialSequenceNumber: circular.New(isn, packet.MAX_SEQUENCENUMBER),
        ConnectionMetrics:     m,
        StartTime:             start,
        OnDeliver:             func(p packet.Packet) {},
        UseBtree:              true,
        UseSendRing:           true,
        UseSendControlRing:    true,
        UseSendEventLoop:      true,
        // ... other config
    }).(*sender)

    return s
}
```

### 4.2 Packet Factory with Relative TSBPD

```go
// createPacketWithTsbpd creates a packet with relative TSBPD time
func createPacketWithTsbpd(seq uint32, tsbpdUs uint64) packet.Packet {
    p := packet.NewPacket(nil)
    p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
    p.Header().PktTsbpdTime = tsbpdUs
    return p
}
```

### 4.3 Time Provider Mock

```go
// mockNowFn creates a controllable time function
func mockNowFn(baseUs *uint64) func() uint64 {
    return func() uint64 {
        return atomic.LoadUint64(baseUs)
    }
}
```

---

## 5. Expected Test Count

| File | Test Cases | Lines (Est) |
|------|------------|-------------|
| `sender_init_test.go` | ~15 | 400 |
| `sender_init_table_test.go` | ~20 | 500 |
| `sender_delivery_table_test.go` | ~25 | 700 |
| `sender_tsbpd_table_test.go` | ~15 | 400 |
| `sender_drop_table_test.go` | ~10 | 300 |
| `sender_ack_table_test.go` | ~20 | 600 |
| `sender_nak_table_test.go` | (exists) | +200 |
| `sender_wraparound_table_test.go` | ~30 | 900 |
| `sender_eventloop_integration_test.go` | ~15 | 800 |
| `sender_ring_flow_test.go` | ~10 | 400 |
| `sender_race_test.go` | ~10 | 500 |
| **Total New** | **~170** | **~5,700** |

Combined with existing 5,544 lines → **~11,000+ lines** (closer to receiver parity)

---

## 6. Immediate Action Items

### Step 1: Create `sender_init_table_test.go`

Focus on the bug we found:
```go
func TestSender_Init_DeliveryStartPoint(t *testing.T) {
    testCases := []struct {
        Name string
        ISN  uint32
    }{
        {"ISN_Zero", 0},
        {"ISN_Middle", 1000},
        {"ISN_Random", 549144712},
        {"ISN_NearMax", 2147483640},
    }

    for _, tc := range testCases {
        t.Run(tc.Name, func(t *testing.T) {
            s := createSenderWithISN(t, tc.ISN)

            // CRITICAL: deliveryStartPoint must equal ISN
            got := s.deliveryStartPoint.Load()
            require.Equal(t, uint64(tc.ISN), got,
                "deliveryStartPoint must be initialized to ISN")
        })
    }
}
```

### Step 2: Fix the Bug

In `sender.go NewSender()`:
```go
// Initialize deliveryStartPoint to ISN
s.deliveryStartPoint.Store(uint64(sendConfig.InitialSequenceNumber.Val()))
```

### Step 3: Add More Tests

Expand to cover all categories.

---

## 7. Success Criteria

1. **Bug Fixed:** Test explicitly validates `deliveryStartPoint` initialization
2. **ISN Coverage:** Tests pass with ISN values: 0, middle, near-max, random
3. **Wraparound:** Tests cover 31-bit sequence wraparound
4. **Integration Pass:** `test-isolation-sender-20m-repeat` achieves 100% pass rate
5. **Line Coverage:** Sender tests exceed 10,000 lines (approaching receiver parity)

