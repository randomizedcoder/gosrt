# EventLoop Time Base Mismatch Fix Design

**Created**: 2025-12-28
**Status**: 📋 Planning
**Related**: `contiguous_point_tsbpd_advancement_design_implementation.md`, `iouring_waitcqetimeout_implementation.md`

---

## 1. Problem Statement

### 1.1 The Bug

The EventLoop mode has a **critical time base mismatch** that causes ALL packets to appear TSBPD-expired immediately, resulting in ~32% packet drops on a clean network.

**Time Base Mismatch**:

| Component | Time Base | Example Value |
|-----------|-----------|---------------|
| `PktTsbpdTime` | Relative (from connection start) | ~8,000,000 µs |
| `r.nowFn()` | Absolute (Unix epoch) | ~1,766,961,920,000,000 µs |

When receiver checks `now > PktTsbpdTime`:
```
1,766,961,920,000,000 > 8,000,000 = TRUE (always!)
```

### 1.2 Where The Times Are Set

**`PktTsbpdTime` calculation** (`connection.go:1024`):
```go
header.PktTsbpdTime = c.tsbpdTimeBase + tsbpdTimeBaseOffset + uint64(header.Timestamp) + c.tsbpdDelay + c.tsbpdDrift
```

Where `tsbpdTimeBase` is set from:
- `dial.go:698`: `uint64(time.Since(dl.start).Microseconds())` - relative
- `conn_request.go:497`: `uint64(req.timestamp)` - relative

**`r.nowFn()` default** (`receive.go:410`):
```go
r.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }  // Absolute!
```

### 1.3 Why Tick() Mode Works

`connection.go:626`:
```go
c.recv.Tick(c.tsbpdTimeBase + tickTime)
```

Where `tickTime = uint64(t.Sub(c.start).Microseconds())`.

Tick() explicitly passes the correct **relative** time. EventLoop uses `r.nowFn()` (absolute).

---

## 2. Why Unit Tests Didn't Catch This

### 2.1 Test Coverage Gap

**Finding**: There are **NO unit tests** that enable `UseEventLoop: true`.

```bash
grep -r "UseEventLoop:\s*true" congestion/live/  # No matches
```

The EventLoop is only exercised via integration tests.

### 2.2 Comprehensive Review of All `nowFn` Usage

```bash
grep -R nowFn ./ 2>&1 | grep -v documentation | grep .go
```

#### File: `congestion/live/receive.go` (Production Code)

| Location | Code | Purpose |
|----------|------|---------|
| Line ~195 | `nowFn func() uint64` | Field declaration |
| Line ~410 | `r.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }` | **DEFAULT: ABSOLUTE TIME** ❌ |
| Line ~875 | `r.contiguousScanWithTime(r.nowFn())` | Used by `contiguousScan()` |
| Line ~1044 | `now := r.nowFn()` | Used by `gapScan()` |
| Line ~2669 | `return r.deliverReadyPacketsWithTime(r.nowFn())` | Used by `deliverReadyPackets()` |

#### File: `congestion/live/receive_test.go`

| Test/Helper | `nowFn` Setting | `PktTsbpdTime` Setting | Consistent? |
|-------------|-----------------|------------------------|-------------|
| `mockNakBtreeRecvWithTsbpdAndTime` | `mockTime` starts at 0 | Relative (e.g., `baseTime + 200`) | ✅ Yes |
| `mockNakBtreeRecvWithTsbpd` | `time.Now().UnixMicro()` | **Must be > Unix time** | ⚠️ Fragile |
| Various NAK tests | Uses `Tick(baseTime)` explicitly | Relative to `baseTime` | ✅ Yes |

**Key Observations**:
- `mockNakBtreeRecvWithTsbpdAndTime` (line ~2143): Sets `mockTime := uint64(0)` and `r.nowFn = func() uint64 { return mockTime }` - **RELATIVE TIME** ✅
- `mockNakBtreeRecvWithTsbpd` (line ~2153): Sets `r.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }` - **ABSOLUTE TIME** ⚠️
- Tests using `mockNakBtreeRecvWithTsbpd` must set `PktTsbpdTime` to large values to work

#### File: `congestion/live/receive_stream_test.go`

| Test/Helper | `nowFn` Setting | `PktTsbpdTime` Setting | Consistent? |
|-------------|-----------------|------------------------|-------------|
| `createMatrixReceiver` | `mockTime` starts at 0 | `startTimeUs + i*interval + TsbpdDelayUs` | ✅ Yes |
| `generateMatrixStream` | N/A | `startTimeUs + uint64(i)*packetIntervalUs + profile.TsbpdDelayUs` | ✅ Yes |

**Key Observations**:
- `createMatrixReceiver` (line ~634-635): Sets `mockTime := uint64(0)` and `r.nowFn = func() uint64 { return mockTime }` - **RELATIVE TIME** ✅
- `generateMatrixStream` (line ~662): Sets `PktTsbpdTime = startTimeUs + uint64(i)*packetIntervalUs + profile.TsbpdDelayUs` - **RELATIVE** ✅

#### File: `congestion/live/core_scan_test.go`

| Test | `nowFn` Setting | `PktTsbpdTime` Setting | Consistent? |
|------|-----------------|------------------------|-------------|
| `TestContiguousScan_Gap` | `baseTime := uint64(1_000_000_000)` | `futureTime := baseTime + 1_000_000` | ✅ Yes |
| `TestContiguousScan_NoProgress` | `baseTime := uint64(1_000_000_000)` | `futureTime := baseTime + 1_000_000` | ✅ Yes |
| `TestContiguousScan_WraparoundWithGap` | `baseTime := uint64(1_000_000_000)` | `futureTime := baseTime + 1_000_000` | ✅ Yes |
| `TestContiguousScan_SmallGap_NoStaleHandling` | `baseTime := uint64(1_000_000_000)` | `futureTime := baseTime + 1_000_000` | ✅ Yes |
| `TestContiguousScan_SmallGap_Wraparound_NoStaleHandling` | `baseTime := uint64(1_000_000_000)` | `futureTime := baseTime + 1_000_000` | ✅ Yes |

**Key Observations**:
- All tests set `recv.nowFn = func() uint64 { return baseTime }` with a consistent `baseTime := uint64(1_000_000_000)`
- All tests set `PktTsbpdTime` relative to `baseTime` (e.g., `baseTime + 1_000_000`)
- **RELATIVE TIME** throughout ✅

### 2.3 Summary: Why Tests Pass But Production Fails

| Context | `nowFn` Time Base | `PktTsbpdTime` Time Base | Match? |
|---------|-------------------|--------------------------|--------|
| Unit tests (mock time) | Relative (~0 to ~1B) | Relative (~0 to ~1B) | ✅ |
| Unit tests (`Tick(baseNow)`) | N/A (passed explicitly) | Relative | ✅ |
| **Production EventLoop** | **Absolute (~1.7T)** | **Relative (~0 to ~1B)** | ❌ **MISMATCH** |

**Root Cause**: Tests either:
1. Override `nowFn` to return relative time (matching `PktTsbpdTime`)
2. Pass time explicitly to `Tick()` (bypassing `nowFn`)

No test exercises the default `nowFn` (absolute Unix time) with realistic relative `PktTsbpdTime`.

---

## 3. Design Requirements

### 3.1 Functional Requirements

1. **FR1**: EventLoop must use the same time base as `PktTsbpdTime` calculation
2. **FR2**: Existing `Tick()` mode must continue to work unchanged
3. **FR3**: Unit tests must be able to inject mock time for deterministic testing
4. **FR4**: No breaking changes to existing test helpers

### 3.2 Non-Functional Requirements

1. **NFR1**: Minimal performance impact (no extra allocations in hot path)
2. **NFR2**: Backward compatibility with existing code
3. **NFR3**: Clear, testable design

---

## 4. Proposed Solution

### 4.1 Add Time Base Configuration to ReceiveConfig

```go
// In congestion/live/receive.go

type ReceiveConfig struct {
    // ... existing fields ...

    // Time base configuration (Phase 10: EventLoop Time Fix)
    // When set, receiver uses this to compute relative time for TSBPD checks.
    // Required for EventLoop mode to match PktTsbpdTime calculation.
    TsbpdTimeBase uint64    // Same as connection's tsbpdTimeBase
    StartTime     time.Time // Connection start time for elapsed calculation
}
```

### 4.2 Update NewReceiver to Initialize nowFn Correctly

```go
// In NewReceiver():

// Initialize time provider for TSBPD delivery
// Default: absolute Unix time (backward compatible with tests that override nowFn)
// With TsbpdTimeBase+StartTime: relative time matching PktTsbpdTime calculation
if !recvConfig.StartTime.IsZero() {
    // Use relative time matching PktTsbpdTime calculation
    r.nowFn = func() uint64 {
        elapsed := uint64(time.Since(recvConfig.StartTime).Microseconds())
        return recvConfig.TsbpdTimeBase + elapsed
    }
} else {
    // Backward compatible: absolute time (tests will override this)
    r.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }
}
```

### 4.3 Update Connection to Pass Time Base

```go
// In connection.go, NewReceiver call:

c.recv = live.NewReceiver(live.ReceiveConfig{
    // ... existing fields ...

    // Time base for EventLoop (Phase 10: EventLoop Time Fix)
    TsbpdTimeBase: c.tsbpdTimeBase,
    StartTime:     c.start,
})
```

---

## 5. Test-Driven Development Plan

### Phase 0: Review Existing Tests Using `nowFn`

**Goal**: Audit all existing tests to ensure they use consistent time bases and identify any fragile tests.

#### Tests to Review

| File | Helper/Test | Risk Level | Action |
|------|-------------|------------|--------|
| `receive_test.go` | `mockNakBtreeRecvWithTsbpd` | ⚠️ **MEDIUM** | Uses absolute time - verify PktTsbpdTime values |
| `receive_test.go` | `mockNakBtreeRecvWithTsbpdAndTime` | ✅ LOW | Uses relative time correctly |
| `receive_stream_test.go` | `createMatrixReceiver` | ✅ LOW | Uses relative time correctly |
| `core_scan_test.go` | All tests | ✅ LOW | Set `nowFn` explicitly with relative time |

#### `mockNakBtreeRecvWithTsbpd` Risk Analysis

This helper sets `r.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }` (absolute time).

**Tests using this helper must**:
1. Set `PktTsbpdTime` to values much larger than typical relative times
2. OR the test logic doesn't depend on TSBPD timing

**Question**: After the fix, will tests using `mockNakBtreeRecvWithTsbpd` break?
- **Answer**: No, because they override `nowFn` AFTER `NewReceiver()` returns
- The fix changes the DEFAULT `nowFn` when `TsbpdTimeBase`/`StartTime` are provided
- Tests that override `nowFn` will continue to work

### Phase 1: Add Failing EventLoop Unit Test

**Goal**: Create a unit test that exercises EventLoop with realistic time bases and **fails** with current code.

**New Test File**: `congestion/live/eventloop_test.go`

#### Test 1: `TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery`

This test verifies that packets with future TSBPD times are NOT delivered prematurely.

```go
// TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery verifies EventLoop doesn't deliver packets
// before their TSBPD time when using production-like time bases.
//
// EXPECTED BEHAVIOR BEFORE FIX: FAIL
// - nowFn returns absolute Unix time (~1.7 trillion µs)
// - PktTsbpdTime is relative (~3 million µs)
// - All packets appear TSBPD-expired immediately
//
// EXPECTED BEHAVIOR AFTER FIX: PASS
// - nowFn returns relative time (matching PktTsbpdTime base)
// - Packets are held until TSBPD time passes
func TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery(t *testing.T) {
    connectionStart := time.Now()
    tsbpdTimeBase := uint64(0) // Relative to connection start
    tsbpdDelay := uint64(3_000_000) // 3 seconds

    var deliveredMu sync.Mutex
    var delivered []uint32

    recvConfig := ReceiveConfig{
        InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
        PeriodicACKInterval:    10_000,  // 10ms
        PeriodicNAKInterval:    20_000,  // 20ms
        OnSendACK:              func(seq circular.Number, light bool) {},
        OnSendNAK:              func(list []circular.Number) {},
        OnDeliver: func(p packet.Packet) {
            deliveredMu.Lock()
            delivered = append(delivered, p.Header().PacketSequenceNumber.Val())
            deliveredMu.Unlock()
        },
        ConnectionMetrics:      &metrics.ConnectionMetrics{HeaderSize: atomic.Uint64{}},
        TsbpdDelay:             tsbpdDelay,
        PacketReorderAlgorithm: "btree",
        UseNakBtree:            true,
        UsePacketRing:          true,
        PacketRingSize:         1024,
        PacketRingShards:       4,
        UseEventLoop:           true,
        // After fix: uncomment these lines
        // TsbpdTimeBase:        tsbpdTimeBase,
        // StartTime:            connectionStart,
    }

    recvConfig.ConnectionMetrics.HeaderSize.Store(44)
    recv := NewReceiver(recvConfig)

    // Push packets with RELATIVE PktTsbpdTime (like production)
    addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
    for i := 0; i < 10; i++ {
        p := packet.NewPacket(addr)
        p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
        // PktTsbpdTime = arrival_time + tsbpdDelay
        // First packet arrives at ~0, so TSBPD time = 0 + 3_000_000 = 3s
        p.Header().PktTsbpdTime = tsbpdTimeBase + uint64(i*10_000) + tsbpdDelay
        p.Header().Timestamp = uint32(i * 10_000)
        recv.Push(p)
    }

    // Start EventLoop in background
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    go recv.EventLoop(ctx)

    // Wait 50ms (packets have TSBPD time 3s in future, should NOT be delivered)
    time.Sleep(50 * time.Millisecond)

    deliveredMu.Lock()
    count := len(delivered)
    deliveredMu.Unlock()

    // BEFORE FIX: This will FAIL - all packets delivered immediately
    // AFTER FIX: This will PASS - no packets delivered (TSBPD not reached)
    if count > 0 {
        t.Errorf("BUG: %d packets delivered before TSBPD time (expected 0)", count)
        t.Logf("nowFn returns absolute Unix time but PktTsbpdTime is relative")
        t.Logf("Fix: Pass TsbpdTimeBase and StartTime to ReceiveConfig")
    }

    _ = connectionStart // Used after fix
}
```

#### Test 2: `TestEventLoop_TimeBase_TSBPD_DeliveryAfterExpiry`

This test verifies that packets ARE delivered once their TSBPD time passes.

```go
// TestEventLoop_TimeBase_TSBPD_DeliveryAfterExpiry verifies packets are delivered
// after their TSBPD time passes.
func TestEventLoop_TimeBase_TSBPD_DeliveryAfterExpiry(t *testing.T) {
    connectionStart := time.Now()
    tsbpdDelay := uint64(50_000) // 50ms (short for test)

    var deliveredMu sync.Mutex
    var delivered []uint32

    recvConfig := ReceiveConfig{
        // ... similar setup ...
        TsbpdDelay: tsbpdDelay,
        // After fix:
        // TsbpdTimeBase: 0,
        // StartTime:     connectionStart,
    }

    recv := NewReceiver(recvConfig)

    // Push packets
    for i := 0; i < 5; i++ {
        p := packet.NewPacket(addr)
        p.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
        p.Header().PktTsbpdTime = uint64(i*10_000) + tsbpdDelay // 50-90ms from start
    }

    // Start EventLoop
    ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
    defer cancel()
    go recv.EventLoop(ctx)

    // Wait for TSBPD to pass (100ms > 50-90ms)
    time.Sleep(100 * time.Millisecond)

    deliveredMu.Lock()
    count := len(delivered)
    deliveredMu.Unlock()

    // AFTER FIX: All 5 packets should be delivered
    require.Equal(t, 5, count, "All packets should be delivered after TSBPD")
}
```

#### Test 3: `TestEventLoop_TimeBase_NAK_TooRecent`

This test verifies that NAKs respect the "too recent" threshold using relative time.

```go
// TestEventLoop_TimeBase_NAK_TooRecent verifies NAK generation uses correct time base.
// Packets in the "too recent" window should NOT be NAKed.
func TestEventLoop_TimeBase_NAK_TooRecent(t *testing.T) {
    // Setup with gap at sequence 5
    // Verify: With correct time base, seq 5 is "too recent" (just arrived)
    //         With wrong time base, seq 5 appears NAKable (already expired)
}
```

### Phase 2: Add Additional EventLoop Tests

After the initial failing test, add more comprehensive tests:

```go
// TestEventLoop_TimeBase_DeliveryAfterTSBPD verifies packets ARE delivered after TSBPD.
func TestEventLoop_TimeBase_DeliveryAfterTSBPD(t *testing.T) {
    // Similar setup, but wait for TSBPD time to pass
    // Verify packets ARE delivered after their TSBPD time
}

// TestEventLoop_TimeBase_MixedDelivery verifies correct ordering.
func TestEventLoop_TimeBase_MixedDelivery(t *testing.T) {
    // Push packets with different TSBPD times
    // Verify they're delivered in TSBPD order, not arrival order
}

// TestEventLoop_TimeBase_NAKGeneration verifies NAKs use correct time.
func TestEventLoop_TimeBase_NAKGeneration(t *testing.T) {
    // Create gaps in sequence
    // Verify NAKs are generated based on relative time
    // (packets in "too recent" window should NOT be NAKed)
}
```

### Phase 3: Verify Existing Tests Still Pass

Run all existing tests to ensure backward compatibility:

```bash
go test ./congestion/live/ -v
```

All tests should pass because:
- Tests using `mockNakBtreeRecvWithTsbpdAndTime` override `nowFn` after creation
- Tests using `Tick()` pass time explicitly
- New tests will fail (expected - demonstrates the bug)

### Phase 4: Implement The Fix

1. Add `TsbpdTimeBase` and `StartTime` fields to `ReceiveConfig`
2. Update `NewReceiver()` to initialize `nowFn` correctly
3. Update `connection.go` to pass time base

### Phase 5: Verify Fix

1. Run new EventLoop tests - should now PASS
2. Run all existing tests - should still PASS
3. Run integration tests - drops should be eliminated

---

## 6. Implementation Checklist

### Phase 0: Review Existing Tests ✅ COMPLETE

- [x] Audit `receive_test.go` tests using `mockNakBtreeRecvWithTsbpd` (absolute time)
- [x] Verify `PktTsbpdTime` values are set appropriately for absolute time tests
- [x] Document any fragile tests that depend on time handling
- [x] Run `go test ./congestion/live/` - all existing tests PASS ✅

**Result**: All 46.9s of existing tests pass before changes.

### Phase 1: Create Failing Test ✅ COMPLETE

- [x] Create `congestion/live/eventloop_test.go`
- [x] Add `TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery`
- [x] Run test - **FAILED as expected** ✅

**Test Output (proves bug)**:
```
=== RUN   TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery
    eventloop_test.go:146: BUG DETECTED: 10 packets delivered before TSBPD time (expected 0)
    eventloop_test.go:147: This proves the time base mismatch:
    eventloop_test.go:148:   - nowFn returns absolute Unix time (~1766963731438520 µs)
    eventloop_test.go:149:   - PktTsbpdTime is relative (~3000000 µs)
    eventloop_test.go:150:   - Since 1766963731438530 >> 3000000, packets appear expired immediately
--- FAIL: TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery (0.05s)
```

### Phase 2: Add More Failing Tests ✅ COMPLETE

- [x] Add `TestEventLoop_TimeBase_ContiguousScan` - **FAILED** (contiguousPoint=7, expected <=2)
- [x] Add `TestEventLoop_TimeBase_GapScan` - **FAILED** (No NAKs generated)
- [x] Add `TestEventLoop_ContextCancellation` - **PASSED** (functionality test)
- [x] Add `TestEventLoop_MetricsIncrement` - **PASSED** (functionality test)

**All 3 time base tests FAIL as expected** ✅

### Phase 3: Verify Existing Tests Still Pass ✅ COMPLETE

- [x] Run `go test ./congestion/live/` - all EXISTING tests PASS ✅
- [x] This confirms backward compatibility before the fix

### Phase 4: Implement Fix ✅ COMPLETE

**Step 4.1**: Update `congestion/live/receive.go` ✅
- [x] Add `TsbpdTimeBase uint64` to `ReceiveConfig` (line ~118)
- [x] Add `StartTime time.Time` to `ReceiveConfig` (line ~119)
- [x] Update `NewReceiver()` to initialize `nowFn` (lines ~420-434)

**Step 4.2**: Update `connection.go` ✅
- [x] Pass `TsbpdTimeBase: c.tsbpdTimeBase` in `NewReceiver()` call (line ~471)
- [x] Pass `StartTime: c.start` in `NewReceiver()` call (line ~472)

**Step 4.3**: Update new EventLoop tests ✅
- [x] Add `TsbpdTimeBase: 0` and `StartTime: time.Now()` in test configs

### Phase 5: Verify Fix ✅ COMPLETE

- [x] Run new EventLoop tests - **ALL PASS** ✅
  ```
  === RUN   TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery
  --- PASS: TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery (0.05s)
  === RUN   TestEventLoop_TimeBase_ContiguousScan
  --- PASS: TestEventLoop_TimeBase_ContiguousScan (0.05s)
  === RUN   TestEventLoop_TimeBase_GapScan
  --- PASS: TestEventLoop_TimeBase_GapScan (0.15s)
  ```
- [x] Run all existing tests - **ALL PASS** (46.3s) ✅
- [x] Run `go test ./...` - full test suite **PASSES** ✅

### Phase 6: Integration Testing ✅ COMPLETE

**Test**: `sudo PRINT_PROM=true make test-isolation CONFIG=Isolation-5M-FullEventLoop`

**Results** (comparing before/after fix):

| Metric | Before Fix | After Fix | Status |
|--------|------------|-----------|--------|
| **Drops** | **4395** | **0** | **✅ FIXED** |
| RTT (Test Server) | 489 µs | 399 µs | -18% improved |
| Packets Received | 13751 | 13751 | ✅ |
| Gaps Detected | 0 | 0 | ✅ |
| NAKs Sent | 0 | 0 | ✅ |
| Recovery | 100% | 100% | ✅ |

**Analysis**:
- The time base fix **completely eliminated** the "too_old" drops
- `gosrt_connection_congestion_recv_data_drop_total{reason="too_old"}` no longer appears (was 4395)
- RTT improved from 489µs to 399µs (~18% reduction)
- All packets delivered successfully (13751/13751)
- Clean network with 0 gaps, 0 NAKs, 0 retransmissions

**Conclusion**: The EventLoop time base mismatch bug is **FIXED**.

### Phase 7: Add EventLoop Functionality Tests ✅ COMPLETE

- [x] Add `TestEventLoop_Basic_PacketDelivery` - Verifies packets delivered after TSBPD
- [x] Add `TestEventLoop_ACK_Periodic` - Verifies ACKs sent periodically
- [x] Add `TestEventLoop_NAK_GapDetection` - Verifies NAKs for gaps
- [x] Add `TestEventLoop_IdleBackoff` - Verifies idle backoff behavior
- [x] Add `TestEventLoop_ContextCancellation` - Already existed

### Phase 7: Add EventLoop + Ring Integration Tests ✅ COMPLETE

- [x] Add `TestEventLoop_Ring_BasicFlow` - Packets flow from ring → btree → delivery
- [x] Add `TestEventLoop_Ring_OutOfOrder` - Out-of-order packets reordered correctly
- [x] Add `TestEventLoop_Ring_HighThroughput` - 1000 packets processed at high rate

### Phase 8: Add io_uring Simulation Tests ✅ COMPLETE

- [x] Add `TestEventLoop_IoUring_SimulatedReorder` - Batch reordering as from CQE
- [x] Add `TestEventLoop_IoUring_LossRecovery` - NAK + retransmit flow
- [x] Add `TestEventLoop_IoUring_BurstLoss` - Burst loss NAK handling
- [x] Add `TestEventLoop_IoUring_TSBPD_Expiry` - Permanent loss TSBPD skip

### Phase 9: Integration Testing ✅ COMPLETE

Results documented above in Phase 6.

---

## 7. Risk Assessment

### 7.1 Low Risk

- **Backward Compatibility**: Tests that override `nowFn` after `NewReceiver()` will continue to work
- **Tick() Mode**: Unaffected (time passed explicitly)
- **`createMatrixReceiver`**: Already uses relative time - no change needed
- **`mockNakBtreeRecvWithTsbpdAndTime`**: Already uses relative time - no change needed

### 7.2 Medium Risk

| Test/Helper | Risk | Reason | Mitigation |
|-------------|------|--------|------------|
| `mockNakBtreeRecvWithTsbpd` | ⚠️ Medium | Uses absolute time | Verify `PktTsbpdTime` values are set correctly |
| Tests using `Tick()` with hardcoded `baseTime` | ⚠️ Medium | Assumes specific time ranges | Verify time values are consistent |

### 7.3 Detailed Analysis: `mockNakBtreeRecvWithTsbpd`

This helper (line ~2148-2155 in `receive_test.go`) does:
```go
r, _ := mockNakBtreeRecvWithTsbpdAndTime(...)
r.nowFn = func() uint64 { return uint64(time.Now().UnixMicro()) }  // Override to absolute
return r
```

**After the fix**:
- `NewReceiver()` will NOT set relative time (no `StartTime` provided)
- Then the helper overrides to absolute time
- **No change in behavior** - still uses absolute time

**Tests using this helper** set `PktTsbpdTime` to values like:
- `tickTime + 100` where `tickTime = baseTime + 2_000_000` and `baseTime = 1_000_000`
- These are ~3 million µs, which is << Unix time (~1.7 trillion µs)
- **These tests would break if `nowFn` returned relative time**
- **But since tests override `nowFn`, they continue to work**

### 7.4 Mitigation Strategy

1. **Phase 0**: Run all existing tests BEFORE any code changes
2. **Phase 3**: Run all existing tests AFTER adding new tests (before fix)
3. **Phase 5**: Run all tests AFTER fix implementation
4. **Document**: Any test that depends on absolute time handling

---

## 8. Appendix: Files to Modify

| File | Changes | Lines |
|------|---------|-------|
| `congestion/live/receive.go` | Add `TsbpdTimeBase`, `StartTime` to `ReceiveConfig` | ~100-120 |
| `congestion/live/receive.go` | Update `NewReceiver()` to init `nowFn` | ~410 |
| `connection.go` | Pass `TsbpdTimeBase` and `start` to `NewReceiver()` | ~426-475 |
| `congestion/live/eventloop_test.go` | **NEW**: EventLoop time base tests | ~200 lines |

---

## 9. Timeline

| Phase | Description | Estimated Time |
|-------|-------------|----------------|
| 0 | Review existing tests | 15 min |
| 1 | Create failing time base test | 20 min |
| 2 | Add more failing time base tests | 30 min |
| 3 | Verify existing tests pass | 5 min |
| 4 | Implement time base fix | 20 min |
| 5 | Verify fix (time base tests) | 10 min |
| 6 | Add EventLoop functionality tests | 45 min |
| 7 | Add EventLoop + Ring tests | 30 min |
| 8 | Add io_uring simulation tests | 45 min |
| 9 | Integration testing | 10 min |
| **Total** | | ~4 hours |

**Note**: Phases 6-8 (expanded EventLoop test coverage) are optional but recommended for comprehensive coverage. The critical path (Phases 0-5) fixes the bug in ~2 hours.

---

## 10. Comprehensive Test Coverage Plan

### 10.1 Current Test Coverage Gaps

| Component | Unit Tests | Integration Tests | Gap |
|-----------|------------|-------------------|-----|
| NAK btree (Tick path) | ✅ `receive_iouring_reorder_test.go` | ✅ Yes | None |
| Ring buffer | ✅ `receive_ring_test.go` | ✅ Yes | None |
| EventLoop | ❌ **NONE** | ✅ Yes | **CRITICAL** |
| EventLoop + Ring | ❌ **NONE** | ✅ Yes | **CRITICAL** |
| EventLoop + Ring + TSBPD | ❌ **NONE** | ✅ Yes | **CRITICAL** |
| EventLoop time base | ❌ **NONE** | ❌ (Bug) | **CRITICAL** |

### 10.2 New Test Suite: `eventloop_test.go`

#### Test Category 1: Time Base Tests (Bug Fix)

| Test | Purpose | Before Fix | After Fix |
|------|---------|------------|-----------|
| `TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery` | Verify no premature delivery | ❌ FAIL | ✅ PASS |
| `TestEventLoop_TimeBase_TSBPD_DeliveryAfterExpiry` | Verify delivery after TSBPD | ❌ FAIL | ✅ PASS |
| `TestEventLoop_TimeBase_NAK_TooRecent` | Verify NAK respects recent window | ❌ FAIL | ✅ PASS |
| `TestEventLoop_TimeBase_ContiguousScan` | Verify scan uses correct time | ❌ FAIL | ✅ PASS |
| `TestEventLoop_TimeBase_GapScan` | Verify gap scan uses correct time | ❌ FAIL | ✅ PASS |

#### Test Category 2: EventLoop Functionality (New Coverage)

| Test | Purpose | Expected |
|------|---------|----------|
| `TestEventLoop_Basic_PacketDelivery` | Basic EventLoop operation | ✅ PASS |
| `TestEventLoop_ACK_Periodic` | Periodic ACK generation | ✅ PASS |
| `TestEventLoop_NAK_GapDetection` | NAK generation in EventLoop | ✅ PASS |
| `TestEventLoop_IdleBackoff` | Adaptive backoff when idle | ✅ PASS |
| `TestEventLoop_ContextCancellation` | Clean shutdown | ✅ PASS |

#### Test Category 3: EventLoop + Ring Buffer Integration

| Test | Purpose | Expected |
|------|---------|----------|
| `TestEventLoop_Ring_BasicFlow` | Ring → EventLoop basic path | ✅ PASS |
| `TestEventLoop_Ring_OutOfOrder` | Ring delivers out-of-order | ✅ PASS |
| `TestEventLoop_Ring_HighThroughput` | Performance under load | ✅ PASS |
| `TestEventLoop_Ring_BackpressureFull` | Ring full handling | ✅ PASS |

#### Test Category 4: io_uring Simulation (End-to-End)

| Test | Purpose | Expected |
|------|---------|----------|
| `TestEventLoop_IoUring_SimulatedReorder` | Full io_uring → Ring → EventLoop | ✅ PASS |
| `TestEventLoop_IoUring_LossRecovery` | NAK → Retransmit → Delivery | ✅ PASS |
| `TestEventLoop_IoUring_BurstLoss` | Burst loss recovery | ✅ PASS |
| `TestEventLoop_IoUring_TSBPD_Expiry` | Packets expire correctly | ✅ PASS |

### 10.3 Test Configuration Matrix

Tests should cover these configuration combinations:

| Config | `UseEventLoop` | `UsePacketRing` | `UseNakBtree` | Current Coverage |
|--------|----------------|-----------------|---------------|------------------|
| Legacy | ❌ | ❌ | ❌ | ✅ Good |
| Btree only | ❌ | ❌ | ✅ | ✅ Good |
| Ring only | ❌ | ✅ | ❌ | ✅ `receive_ring_test.go` |
| Ring + Btree | ❌ | ✅ | ✅ | ⚠️ Limited |
| **EventLoop** | ✅ | ✅ | ✅ | ❌ **NONE** |

### 10.4 Test Helper Functions to Add

```go
// createEventLoopReceiver creates a receiver with EventLoop enabled
// and proper time base configuration.
func createEventLoopReceiver(t *testing.T, cfg EventLoopTestConfig) (*receiver, context.CancelFunc) {
    connectionStart := time.Now()

    recvConfig := ReceiveConfig{
        InitialSequenceNumber:  circular.New(cfg.StartSeq, packet.MAX_SEQUENCENUMBER),
        PeriodicACKInterval:    10_000,  // 10ms
        PeriodicNAKInterval:    20_000,  // 20ms
        TsbpdDelay:             cfg.TsbpdDelayUs,
        PacketReorderAlgorithm: "btree",
        UseNakBtree:            true,
        UsePacketRing:          true,
        PacketRingSize:         cfg.RingSize,
        PacketRingShards:       cfg.RingShards,
        UseEventLoop:           true,
        // Time base fix fields
        TsbpdTimeBase:          0,
        StartTime:              connectionStart,
        // Callbacks
        OnSendACK:              cfg.OnSendACK,
        OnSendNAK:              cfg.OnSendNAK,
        OnDeliver:              cfg.OnDeliver,
        ConnectionMetrics:      cfg.Metrics,
    }

    recv := NewReceiver(recvConfig).(*receiver)
    ctx, cancel := context.WithCancel(context.Background())

    return recv, cancel
}

// runEventLoopWithPackets starts EventLoop, pushes packets, and returns results
func runEventLoopWithPackets(t *testing.T, recv *receiver, packets []packet.Packet, duration time.Duration) EventLoopResults {
    ctx, cancel := context.WithTimeout(context.Background(), duration)
    defer cancel()

    // Start EventLoop in background
    var wg sync.WaitGroup
    wg.Add(1)
    go func() {
        defer wg.Done()
        recv.EventLoop(ctx)
    }()

    // Push packets (simulating io_uring delivery)
    for _, p := range packets {
        recv.Push(p)
    }

    // Wait for EventLoop to finish
    wg.Wait()

    return EventLoopResults{...}
}
```

---

## 11. Success Criteria

### Unit Tests

| Test | Before Fix | After Fix |
|------|------------|-----------|
| `TestEventLoop_TimeBase_TSBPD_NoEarlyDelivery` | ❌ FAIL | ✅ PASS |
| `TestEventLoop_TimeBase_TSBPD_DeliveryAfterExpiry` | ❌ FAIL | ✅ PASS |
| `TestEventLoop_TimeBase_NAK_TooRecent` | ❌ FAIL | ✅ PASS |
| All new EventLoop tests | N/A | ✅ PASS |
| All existing tests | ✅ PASS | ✅ PASS |

### Integration Test

| Metric | Before Fix | After Fix |
|--------|------------|-----------|
| Test Server Drops | 4395 | **0** |
| Test Server Packets Received | 9347 | **~13746** |
| Control vs Test diff | -32% | **~0%** |

---

## Phase 11: NAK Timer Time Base Fix (Bug #2)

### Problem Discovery

During integration testing with 10% packet loss (`Parallel-Loss-L10-20M-Base-vs-FullEL`),
the HighPerf pipeline showed **0 NAKs and 0 retransmissions** while Baseline showed ~173.

### Root Cause

In `EventLoop()` at line 2492, the NAK timer was using absolute time instead of `r.nowFn()`:

```go
case <-nakTicker.C:
    now := uint64(time.Now().UnixMicro())  // BUG! Absolute time ~1.7e12
    if list := r.periodicNAK(now); len(list) != 0 {
```

This caused `tooRecentThreshold = now + tsbpdDelay*0.90 ≈ 1.7e12`, meaning ALL packets
appeared "not too recent" (since their relative `PktTsbpdTime << 1.7e12`), resulting
in **over-NAKing** for some scenarios and **no NAKing** for others depending on TSBPD checks.

### TDD Fix

1. **Created failing test** `TestEventLoop_NAK_TimeBase_Consistency`:
   - Pushes packets where some should be "too recent" to NAK
   - With BUG: gaps are incorrectly NAKed (test FAILS)
   - With FIX: gaps are correctly skipped (test PASSES)

2. **Applied fix** - Changed to `now := r.nowFn()`:

```go
case <-nakTicker.C:
    // Use r.nowFn() for consistent time base with PktTsbpdTime
    now := r.nowFn()
    if list := r.periodicNAK(now); len(list) != 0 {
```

3. **Updated tests** with incorrect `PktTsbpdTime` values that assumed buggy time base:
   - `TestEventLoop_TimeBase_GapScan`
   - `TestEventLoop_IoUring_LossRecovery`

### Result

All 17 EventLoop tests pass with the fix.

