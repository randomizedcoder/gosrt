# NAK btree Debugging

**Status**: MAIN BUG FIXED - MINOR ISSUE REMAINS
**Date**: 2024-12-15
**Test**: `Isolation-Server-NakBtree-IoUringRecv`
**Root Cause**: NAK btree config NOT PASSED from `connection.go` to receiver (line 403-414)
**Fix**: Added NAK btree config fields to `NewReceiver()` call in `connection.go`
**Result**: Gaps reduced from **2939 → 0** ✅ | NAKs reduced from **863 → 126** ⚠️

---

## 1. Issue Summary

**Problem**: On a **clean network** (no packet loss, no latency), the test pipeline with NAK btree + io_uring recv shows significant false positive gap detection while the control pipeline shows zero gaps.

| Metric | Control | Test (NAK btree + io_uring recv) | Difference |
|--------|---------|----------------------------------|------------|
| Packets Received | 19530 | 22434 | +14.9% (extra = retrans) |
| **Gaps Detected** | **0** | **2939** | **NEW (should be 0!)** |
| Retrans Received | 0 | 2964 | NEW |
| NAKs Sent | 0 | 863 | NEW |
| Drops | 0 | 2964 | NEW |
| RTT | 0.089ms | 4.36ms | **49x higher** |

**Expected**: Both pipelines should show 0 gaps on a clean network.

**Actual**: Test pipeline detects ~15% false positive gaps, triggering unnecessary NAKs and retransmissions that then arrive too late and are dropped.

---

## 🔬 KEY FINDINGS (2024-12-15)

### Finding 1: NAK btree alone works perfectly

**`Isolation-Server-NakBtree` test PASSED with 0 gaps!**

| Metric | Control | Test (NAK btree only) | Result |
|--------|---------|----------------------|--------|
| Packets Received | 19530 | 19530 | ✅ IDENTICAL |
| Gaps Detected | 0 | 0 | ✅ IDENTICAL |
| Retrans Received | 0 | 0 | ✅ IDENTICAL |
| NAKs Sent | 0 | 0 | ✅ IDENTICAL |
| Drops | 0 | 0 | ✅ IDENTICAL |
| RTT | ~0.07ms | ~0.08ms | ✅ IDENTICAL |

### Finding 2: 50% NakRecentPercent does NOT fix the issue ❌

**`Isolation-Server-NakBtree-IoUringRecv-LargeWindow` (50% window) STILL FAILS!**

| Metric | 10% Window | 50% Window | Difference |
|--------|------------|------------|------------|
| Gaps Detected | 2939 | 2786 | -5% (no real improvement) |
| NAKs Sent | 863 | 815 | -5.5% |
| Retrans Received | 2964 | 2804 | -5.4% |
| Drops | 2964 | 2804 | -5.4% |

**Conclusion**: The "too recent" window size is NOT the problem. The issue is more fundamental.

### Finding 3: Unit tests pass but integration tests fail

The unit tests in `receive_iouring_reorder_test.go` simulate out-of-order delivery and pass. But the real io_uring recv path in integration tests shows massive false gap detection. This suggests the unit tests don't accurately model the real io_uring behavior.

---

## 2. Test Configuration

```
Control Server: -addr 10.2.1.2:6000 -packetreorderalgorithm list
Test Server:    -addr 10.2.1.3:6001 -packetreorderalgorithm list -iouringrecvenabled -usenakbtree -fastnakenabled -fastnakrecentenabled -honornakorder

Control CG: -to srt://10.2.1.2:6000/test-stream-control -packetreorderalgorithm list
Test CG:    -to srt://10.2.1.3:6001/test-stream-test -packetreorderalgorithm list
```

**Key difference**: Test Server has:
- `-iouringrecvenabled` (io_uring for receiving packets)
- `-usenakbtree` (NAK btree for gap detection)
- `-fastnakenabled` (FastNAK optimization)
- `-fastnakrecentenabled` (FastNAK sequence jump detection)
- `-honornakorder` (sender honors NAK order)

---

## 3. Observed Behavior Analysis

### 3.1 The Cascade

1. io_uring delivers packets (possibly out of order due to batching)
2. NAK btree scans for gaps
3. Gaps are "detected" (false positives)
4. NAKs are sent (863 NAK packets)
5. Sender retransmits (2964 packets)
6. Retransmissions arrive but are **dropped** (2964 drops = 100% of retrans!)
7. RTT increases significantly (0.089ms → 4.36ms)

### 3.2 Key Observations

| Observation | Value | Significance |
|-------------|-------|--------------|
| NAKs sent / Gaps detected | 863 / 2939 = 0.29 | Each NAK contained ~3.4 packets on average |
| Retrans / NAKs | 2964 / 863 = 3.43 | Confirms ~3.4 packets per NAK |
| Drops / Retrans | 2964 / 2964 = 100% | **All retransmissions were dropped!** |
| RTT increase | 49x | Significant latency added by retransmission cycles |

### 3.3 Why Are All Retransmissions Dropped?

If retransmissions are dropped, it means they arrived **after TSBPD expiry** for their sequence numbers. This suggests:
- The gaps were detected for packets that actually DID arrive (just reordered)
- By the time retransmissions arrive, the original packets have already been delivered
- The "too late" logic drops the retransmissions

This strongly suggests **false positive gap detection**.

---

## 4. Hypotheses (Updated After All Test Results)

### ~~Hypothesis 1: NAK btree Logic Bug~~ ❌ ELIMINATED

**Result**: `Isolation-Server-NakBtree` passed with 0 gaps. NAK btree is working correctly with traditional UDP socket receive.

---

### ~~Hypothesis 2: NakRecentPercent "Too Recent" Window Too Small~~ ❌ ELIMINATED

**Result**: Testing with 50% window (1500ms instead of 300ms) made almost no difference:
- 10% window: 2939 gaps
- 50% window: 2786 gaps (only -5% improvement)

The "too recent" window is NOT the root cause.

---

### Hypothesis 3: NAK btree NOT being cleared when packets arrive (HIGH PROBABILITY) ⭐ NEW FOCUS

**Theory**: When a packet arrives via io_uring, it should be removed from the NAK btree (if it was previously marked as a gap). If this removal is NOT happening correctly in the io_uring path, the NAK btree retains "phantom" entries for packets that actually arrived.

**Evidence**:
- Unit tests pass but they don't use actual io_uring recv path
- Unit tests manually call `Push()` which may handle NAK btree differently
- Real io_uring path might skip or race the NAK btree removal step

**Key Question**: When `periodicNakBtree()` runs, does the NAK btree contain sequences that are ACTUALLY in the packetStore?

**Test**: Add debug logging to compare NAK btree contents vs packetStore contents.

---

### Hypothesis 4: io_uring recv path skips NAK btree deletion step (HIGH PROBABILITY) ⭐

**Theory**: There may be two different code paths for packet reception:
1. Traditional UDP socket → calls full `Push()` including NAK btree deletion
2. io_uring recv → calls a different/simplified path that misses NAK btree deletion

**Evidence**:
- Problem ONLY occurs with io_uring recv
- Unit tests simulate reordering but may not test the actual io_uring code path

**Test**: Trace the code path from io_uring completion handler to see if NAK btree deletion is being called.

---

### Hypothesis 5: Race between io_uring completion and periodic NAK timer (MEDIUM PROBABILITY)

**Theory**: io_uring delivers packets in batches. The periodic NAK timer might fire DURING batch processing, seeing a partially-filled packetStore and detecting "gaps" that are just unprocessed packets in the batch.

**Evidence**:
- ~2800 false gaps is approximately the same across test runs (deterministic)
- This could be explained by consistent timing patterns

**Test**: Add debug logging to see timing of NAK scans vs packet arrivals.

---

### Hypothesis 6: FastNAKRecent sequence jump detection (MEDIUM PROBABILITY)

**Theory**: FastNAKRecent detects "sequence jumps" and triggers NAKs. With io_uring reordering, these "jumps" might be false positives.

**Test**: Create a test config with FastNAK disabled to isolate the impact.

---

## 5. Investigation Timeline

### ✅ Phase 1: Isolation Testing — COMPLETED

| Test | Command | Result | Conclusion |
|------|---------|--------|------------|
| A1 | `Isolation-Server-NakBtree` | ✅ 0 gaps | NAK btree alone is CORRECT |
| A2 | `Isolation-Server-NakBtree-IoUringRecv` | ❌ 2939 gaps | io_uring interaction is problem |

---

### ✅ Phase 2: Parameter Tuning — COMPLETED (DID NOT FIX)

| Test | NakRecentPercent | Result | Conclusion |
|------|------------------|--------|------------|
| 10% (default) | 0.10 | 2939 gaps | ❌ |
| 50% (large window) | 0.50 | 2786 gaps | ❌ (-5% only) |

**Observation**: The "too recent" window changes made no difference. This was confusing until we found the real bug.

---

### ✅ Phase 3: Code Analysis — ROOT CAUSE FOUND!

**Why unit tests pass but integration fails**:

| Aspect | Unit Tests | Integration Tests |
|--------|------------|-------------------|
| Receiver creation | Direct `NewReceiver()` call with full config | Via `newSRTConn()` which doesn't pass config |
| `UseNakBtree` | Explicitly set to `true` | Defaults to `false` (not passed!) |
| `TsbpdDelay` | Explicitly set | Defaults to `0` (not passed!) |
| Code path | `periodicNakBtree()` ✅ | `periodicNakOriginal()` ❌ |
| "Too recent" protection | Active | **NOT ACTIVE** |

**The unit tests pass because they directly create the receiver with the correct config. Integration tests go through `connection.go` which doesn't pass the config!**

---

## 6. Why This Bug Was Hard to Find

### The Misleading Evidence

1. **Unit tests passed** → Suggested NAK btree logic was correct (it IS correct)
2. **Problem only with io_uring** → Suggested io_uring-specific bug (red herring)
3. **50% window didn't help** → Suggested TSBPD threshold was wrong (red herring)

### The Real Reason

The NAK btree **was never being used at all** in integration tests! The `UseNakBtree` flag wasn't passed, so `r.useNakBtree = false`, and the old `periodicNakOriginal()` was used instead.

The old NAK logic has NO "too recent" protection:

```go
// periodicNakOriginal() - NO "too recent" check!
r.packetStore.Iterate(func(p packet.Packet) bool {
    if !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
        // ANY gap is NAK'd immediately, even if it's just io_uring reordering
        list = append(list, nackSequenceNumber)
        list = append(list, h.PacketSequenceNumber.Dec())
    }
    return true
})
```

### Why `Isolation-Server-NakBtree` (without io_uring) Passed

Without io_uring, packets arrive in-order via traditional UDP socket. Even though `periodicNakOriginal()` was used (no "too recent" protection), there were no reordering-induced gaps to detect. So the test passed by accident!

With io_uring, packets arrive out-of-order, and `periodicNakOriginal()` NAKs for every temporary gap.

---

## 7. ROOT CAUSE IDENTIFIED ⭐⭐⭐

### ACTUAL BUG: NAK btree Configuration NOT PASSED to Receiver!

**The design is CORRECT. The problem is a simple configuration wiring bug.**

The NAK btree configuration (`UseNakBtree`, `TsbpdDelay`, `NakRecentPercent`, etc.) is **NOT being passed** from the connection config to the receiver! This means:

1. CLI passes `-usenakbtree` → stored in `c.config.UseNakBtree`
2. But `NewReceiver()` is **NOT given** `UseNakBtree: c.config.UseNakBtree`
3. So `r.useNakBtree` defaults to `false`
4. **`periodicNakOriginal()` is used** (line 607) — the OLD NAK logic WITHOUT "too recent" protection!
5. The old logic NAKs for ANY gap, including temporary io_uring reordering gaps

### The Bug Location: `connection.go` lines 403-414

```go
// CURRENT CODE (BUGGY):
c.recv = live.NewReceiver(live.ReceiveConfig{
    InitialSequenceNumber:  c.initialPacketSequenceNumber,
    PeriodicACKInterval:    10_000,
    PeriodicNAKInterval:    20_000,
    OnSendACK:              c.sendACK,
    OnSendNAK:              c.sendNAK,
    OnDeliver:              c.deliver,
    PacketReorderAlgorithm: c.config.PacketReorderAlgorithm,
    BTreeDegree:            c.config.BTreeDegree,
    LockTimingMetrics:      c.metrics.ReceiverLockTiming,
    ConnectionMetrics:      c.metrics,
    // ⚠️ MISSING: All NAK btree configuration!
    // UseNakBtree:          c.config.UseNakBtree,
    // SuppressImmediateNak: c.config.SuppressImmediateNak,
    // TsbpdDelay:           c.tsbpdDelay,
    // NakRecentPercent:     c.config.NakRecentPercent,
    // etc.
})
```

### The Receiver Dispatch Logic: `receive.go` line 602-608

```go
func (r *receiver) periodicNAK(now uint64) []circular.Number {
    // Dispatch to appropriate implementation
    if r.useNakBtree {
        return r.periodicNakBtree(now)  // ✅ Has "too recent" protection
    }
    return r.periodicNakOriginal(now)   // ❌ NO "too recent" protection!
}
```

Since `r.useNakBtree` is `false` (default, because config wasn't passed), the code ALWAYS uses `periodicNakOriginal()` which:
- Iterates through the packet store
- NAKs for ANY sequence gap
- Has NO "too recent" protection for io_uring reordering

### The ReceiveConfig Struct Has All The Fields (receive.go lines 17-41)

```go
type ReceiveConfig struct {
    InitialSequenceNumber  circular.Number
    PeriodicACKInterval    uint64
    PeriodicNAKInterval    uint64
    OnSendACK              func(seq circular.Number, light bool)
    OnSendNAK              func(list []circular.Number)
    OnDeliver              func(p packet.Packet)
    PacketReorderAlgorithm string
    BTreeDegree            int
    LockTimingMetrics      *metrics.LockTimingMetrics
    ConnectionMetrics      *metrics.ConnectionMetrics

    // NAK btree configuration (Phase 4) ← ALL PRESENT!
    UseNakBtree            bool    // Enable NAK btree for improved out-of-order handling
    SuppressImmediateNak   bool    // Suppress immediate NAK, let periodic NAK handle gaps
    TsbpdDelay             uint64  // Microseconds, for scan window calculation
    NakRecentPercent       float64 // Percentage of TSBPD delay for "recent" window
    NakMergeGap            uint32  // Maximum gap to merge into a single range
    NakConsolidationBudget uint64  // Microseconds, time budget for consolidation

    // FastNAK configuration
    FastNakEnabled       bool
    FastNakThresholdUs   uint64
    FastNakRecentEnabled bool
}
```

The struct is complete, but **none of these fields are being populated** when `NewReceiver()` is called!

### Why Unit Tests Pass

Unit tests in `receive_iouring_reorder_test.go` **DO pass the config correctly**:

```go
// From receive_iouring_reorder_test.go line 176-188:
recv := NewReceiver(ReceiveConfig{
    InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
    PeriodicACKInterval:    10,
    PeriodicNAKInterval:    20,
    // ... etc ...
    PacketReorderAlgorithm: "btree",
    UseNakBtree:            true,                    // ✅ PASSED!
    SuppressImmediateNak:   true,                    // ✅ PASSED!
    TsbpdDelay:             tsbpdDelayUs,            // ✅ PASSED!
    NakRecentPercent:       0.10,                    // ✅ PASSED!
    // ... etc ...
})
```

Unit tests directly create the receiver with the correct config, so `r.useNakBtree = true` and `periodicNakBtree()` is used. The design works correctly in unit tests!

But integration tests go through `connection.go` → `NewReceiver()` which DOESN'T pass the config, so `r.useNakBtree = false` and the buggy `periodicNakOriginal()` is used.

### Evidence Summary (Reinterpreted)

| Finding | What We Thought | What Actually Happened |
|---------|-----------------|------------------------|
| NAK btree alone: 0 gaps | NAK btree works correctly | NAK btree config wasn't passed, but no io_uring so original logic works |
| NAK btree + io_uring: 2939 gaps | io_uring breaks NAK btree | NAK btree config wasn't passed, original logic can't handle io_uring reorder |
| 50% window made no difference | "Too recent" window ineffective | "Too recent" window never used because wrong code path! |

### The Design IS Correct

The TSBPD-based "too recent" threshold in `periodicNakBtree()` (lines 716-719) SHOULD work:

```go
// Step 1: Calculate "too recent" threshold
// Packets with TSBPD beyond this are too new to NAK (might be reordered, not lost)
tooRecentThreshold := now
if r.nakRecentPercent > 0 && r.tsbpdDelay > 0 {
    tooRecentThreshold = now + uint64(float64(r.tsbpdDelay)*r.nakRecentPercent)
}
```

But this code is **never executed** because `r.useNakBtree` is false!

---

## 8. The Fix

### The Fix is Simple: Wire the Configuration

The fix is to pass the NAK btree configuration from `c.config` to `live.ReceiveConfig` in `connection.go`:

```go
// FIXED CODE (connection.go):
c.recv = live.NewReceiver(live.ReceiveConfig{
    InitialSequenceNumber:  c.initialPacketSequenceNumber,
    PeriodicACKInterval:    10_000,
    PeriodicNAKInterval:    20_000,
    OnSendACK:              c.sendACK,
    OnSendNAK:              c.sendNAK,
    OnDeliver:              c.deliver,
    PacketReorderAlgorithm: c.config.PacketReorderAlgorithm,
    BTreeDegree:            c.config.BTreeDegree,
    LockTimingMetrics:      c.metrics.ReceiverLockTiming,
    ConnectionMetrics:      c.metrics,

    // ✅ ADD THESE: NAK btree configuration
    UseNakBtree:            c.config.UseNakBtree,
    SuppressImmediateNak:   c.config.SuppressImmediateNak,
    TsbpdDelay:             c.tsbpdDelay,  // Note: c.tsbpdDelay is in microseconds
    NakRecentPercent:       c.config.NakRecentPercent,
    NakMergeGap:            c.config.NakMergeGap,
    NakConsolidationBudget: uint64(c.config.NakConsolidationBudget.Microseconds()),

    // ✅ ADD THESE: FastNAK configuration
    FastNakEnabled:       c.config.FastNakEnabled,
    FastNakThresholdUs:   uint64(c.config.FastNakThreshold.Microseconds()),
    FastNakRecentEnabled: c.config.FastNakRecentEnabled,
})
```

### Note on TsbpdDelay Timing

The `c.tsbpdDelay` field is set AFTER the handshake completes (in `handleHandshake()` at line 1261):

```go
c.tsbpdDelay = uint64(recvTsbpdDelay) * 1000  // Convert ms to µs
```

But `NewReceiver()` is called in `newSRTConn()` BEFORE the handshake. This means at receiver creation time, `c.tsbpdDelay` is 0!

**Potential issue**: The receiver's `tsbpdDelay` might need to be set later, after the handshake. We need to verify:
1. Does `periodicNakBtree()` get called before the handshake completes?
2. If so, we may need a mechanism to update the receiver's `tsbpdDelay` after handshake.

This needs investigation, but even without `tsbpdDelay` being set correctly, fixing `UseNakBtree` will at least route to the correct code path.

---

## 9. Test-Driven Development Plan ⭐

### Principle: Failing Test First, Then Fix

We need to create Go tests that:
1. **FAIL** before the fix is implemented (proving the bug exists)
2. **PASS** after the fix is implemented (proving the fix works)

### Test 1: Verify `UseNakBtree` is Passed to Receiver

**File**: `connection_test.go` (or new file `connection_config_test.go`)

**Purpose**: Verify that when a connection is created with `UseNakBtree=true` in the config, the receiver actually gets `useNakBtree=true`.

```go
// Test that verifies NAK btree config is passed to receiver
// This test should FAIL before the fix!
func TestConnectionPassesNakBtreeConfigToReceiver(t *testing.T) {
    // Create a config with UseNakBtree=true
    config := Config{
        UseNakBtree:      true,
        NakRecentPercent: 0.10,
        // ... other required fields
    }

    // Create a mock connection or use internal test helpers
    // We need to inspect the receiver's useNakBtree field

    // Get the receiver and check if useNakBtree is true
    // This requires either:
    // a) A test helper that exposes receiver internals
    // b) Using reflection to inspect private fields
    // c) Creating a test-specific method on receiver

    // Assert that receiver.useNakBtree == true
    // EXPECTED BEFORE FIX: FAIL (receiver.useNakBtree == false)
    // EXPECTED AFTER FIX:  PASS (receiver.useNakBtree == true)
}
```

### Test 2: Verify `periodicNakBtree` is Called (Not `periodicNakOriginal`)

**File**: `congestion/live/receive_test.go` or new file

**Purpose**: Verify that when `UseNakBtree=true`, the `periodicNakBtree()` function is called during `periodicNAK()`.

```go
// Test that periodicNakBtree() is used when UseNakBtree=true
func TestPeriodicNakDispatchToNakBtree(t *testing.T) {
    // Create receiver WITH UseNakBtree=true
    recv := NewReceiver(ReceiveConfig{
        // ... required fields ...
        UseNakBtree:      true,
        TsbpdDelay:       3_000_000,  // 3 seconds in µs
        NakRecentPercent: 0.10,
    })

    // Get underlying receiver struct
    r := recv.(*receiver)

    // Verify the flag is set
    if !r.useNakBtree {
        t.Error("Expected useNakBtree=true but got false")
    }

    // Verify NAK btree was created
    if r.nakBtree == nil {
        t.Error("Expected nakBtree to be initialized but got nil")
    }
}
```

### Test 3: Integration Test via Metrics

**File**: `congestion/live/receive_integration_test.go` (new)

**Purpose**: Create a receiver through the full connection path and verify metrics show `periodicNakBtree` is being used.

```go
// Test that verifies the FULL path from connection config to receiver behavior
// This test will FAIL before the fix because the config isn't passed!
func TestNakBtreeMetricsInFullConnectionPath(t *testing.T) {
    // This test should create a real connection (or mock the minimum needed)
    // and verify that NakPeriodicBtreeRuns counter increments
    // (not NakPeriodicOriginalRuns)

    // Before fix: NakPeriodicOriginalRuns > 0, NakPeriodicBtreeRuns == 0
    // After fix:  NakPeriodicBtreeRuns > 0, NakPeriodicOriginalRuns == 0
}
```

### Test 4: Receiver Config Validation Test

**File**: `connection_test.go`

**Purpose**: Test that all expected config fields are passed from `srt.Config` to `live.ReceiveConfig`.

```go
// Test that validates ALL NAK btree config fields are passed
func TestAllNakBtreeConfigFieldsPassedToReceiver(t *testing.T) {
    testCases := []struct {
        name     string
        config   Config
        expected struct {
            UseNakBtree          bool
            SuppressImmediateNak bool
            NakRecentPercent     float64
            FastNakEnabled       bool
            FastNakRecentEnabled bool
        }
    }{
        {
            name: "NAK btree disabled",
            config: Config{
                UseNakBtree: false,
            },
            expected: struct {
                UseNakBtree          bool
                SuppressImmediateNak bool
                NakRecentPercent     float64
                FastNakEnabled       bool
                FastNakRecentEnabled bool
            }{
                UseNakBtree: false,
            },
        },
        {
            name: "NAK btree enabled",
            config: Config{
                UseNakBtree:          true,
                SuppressImmediateNak: true,
                NakRecentPercent:     0.15,
                FastNakEnabled:       true,
                FastNakRecentEnabled: true,
            },
            expected: struct {
                UseNakBtree          bool
                SuppressImmediateNak bool
                NakRecentPercent     float64
                FastNakEnabled       bool
                FastNakRecentEnabled bool
            }{
                UseNakBtree:          true,
                SuppressImmediateNak: true,
                NakRecentPercent:     0.15,
                FastNakEnabled:       true,
                FastNakRecentEnabled: true,
            },
        },
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Create connection with config
            // Inspect receiver's internal fields
            // Assert they match expected values

            // EXPECTED BEFORE FIX: FAIL (fields are default values, not config values)
            // EXPECTED AFTER FIX:  PASS (fields match config values)
        })
    }
}
```

### Test Implementation Challenges

To write these tests, we need to:

1. **Access receiver internals**: The `receiver` struct fields are private. Options:
   - Add a test-only method: `func (r *receiver) TestGetConfig() ReceiveConfig`
   - Use build tag `// +build testing` to expose internals
   - Use reflection (less desirable)
   - Create a new `testing.go` file in the package with test helpers

2. **Create a connection in test context**: The `newSRTConn()` function requires many dependencies. Options:
   - Create a `TestNewSRTConn()` helper that creates a minimal connection
   - Mock the required interfaces
   - Extract the receiver creation logic for easier testing

3. **Verify code path via metrics**: Use the `NakPeriodicBtreeRuns` vs `NakPeriodicOriginalRuns` metrics to verify which code path is taken.

---

## 10. Recommended Test Implementation Approach

### Step 1: Add Test Helper to Receiver Package

Create `congestion/live/testing.go`:

```go
//go:build testing
// +build testing

package live

// TestReceiverInternals exposes receiver internals for testing
type TestReceiverInternals struct {
    UseNakBtree          bool
    SuppressImmediateNak bool
    TsbpdDelay           uint64
    NakRecentPercent     float64
    NakBtreeCreated      bool
    FastNakEnabled       bool
    FastNakRecentEnabled bool
}

// GetTestInternals returns internal state for testing
// Only available with -tags=testing
func (r *receiver) GetTestInternals() TestReceiverInternals {
    return TestReceiverInternals{
        UseNakBtree:          r.useNakBtree,
        SuppressImmediateNak: r.suppressImmediateNak,
        TsbpdDelay:           r.tsbpdDelay,
        NakRecentPercent:     r.nakRecentPercent,
        NakBtreeCreated:      r.nakBtree != nil,
        FastNakEnabled:       r.fastNakEnabled,
        FastNakRecentEnabled: r.fastNakRecentEnabled,
    }
}
```

### Step 2: Create Connection-Level Test

Create `connection_nakbtree_test.go`:

```go
//go:build testing
// +build testing

package srt

import (
    "testing"
    "github.com/datarhei/gosrt/congestion/live"
)

// TestConnectionPassesNakBtreeConfig verifies that NAK btree config
// is properly passed from srt.Config to the receiver.
//
// EXPECTED BEFORE FIX: FAIL
// EXPECTED AFTER FIX:  PASS
func TestConnectionPassesNakBtreeConfig(t *testing.T) {
    // Create config with NAK btree enabled
    config := Config{
        UseNakBtree:          true,
        SuppressImmediateNak: true,
        NakRecentPercent:     0.25,
        FastNakEnabled:       true,
        FastNakRecentEnabled: true,
    }

    // Apply auto configuration
    config.ApplyAutoConfiguration()

    // Create a minimal connection for testing
    // (This will require a test helper or refactoring)
    conn := createTestConnection(t, config)
    defer conn.Close()

    // Get receiver internals
    recv := conn.recv.(*live.receiver)
    internals := recv.GetTestInternals()

    // Verify all fields are passed correctly
    if !internals.UseNakBtree {
        t.Error("UseNakBtree not passed: expected true, got false")
    }
    if !internals.SuppressImmediateNak {
        t.Error("SuppressImmediateNak not passed: expected true, got false")
    }
    if internals.NakRecentPercent != 0.25 {
        t.Errorf("NakRecentPercent not passed: expected 0.25, got %f", internals.NakRecentPercent)
    }
    if !internals.FastNakEnabled {
        t.Error("FastNakEnabled not passed: expected true, got false")
    }
    if !internals.FastNakRecentEnabled {
        t.Error("FastNakRecentEnabled not passed: expected true, got false")
    }
    if !internals.NakBtreeCreated {
        t.Error("NAK btree not created despite UseNakBtree=true")
    }
}
```

### Step 3: Verify with Metrics Test

```go
// TestNakBtreeCodePathUsed verifies that periodicNakBtree() is called
// when UseNakBtree=true, not periodicNakOriginal().
//
// EXPECTED BEFORE FIX: FAIL (NakPeriodicOriginalRuns > 0)
// EXPECTED AFTER FIX:  PASS (NakPeriodicBtreeRuns > 0)
func TestNakBtreeCodePathUsed(t *testing.T) {
    testMetrics := metrics.NewTestConnectionMetrics()

    recv := live.NewReceiver(live.ReceiveConfig{
        InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
        PeriodicACKInterval:   10_000,
        PeriodicNAKInterval:   20_000,
        ConnectionMetrics:     testMetrics,
        UseNakBtree:           true,  // Enable NAK btree
        TsbpdDelay:            3_000_000,
        NakRecentPercent:      0.10,
    })

    // Trigger periodic NAK by calling Tick()
    // This should call periodicNakBtree() not periodicNakOriginal()
    r := recv.(*live.receiver)
    r.Tick(100_000_000) // 100ms

    // Check metrics
    btreeRuns := testMetrics.NakPeriodicBtreeRuns.Load()
    originalRuns := testMetrics.NakPeriodicOriginalRuns.Load()

    if btreeRuns == 0 {
        t.Error("Expected NakPeriodicBtreeRuns > 0, got 0")
    }
    if originalRuns > 0 {
        t.Errorf("Expected NakPeriodicOriginalRuns == 0, got %d", originalRuns)
    }
}
```

---

## 11. Implementation Order

### Phase 1: Write Failing Tests

1. **Create test helper** (`congestion/live/testing.go`)
   - Add `GetTestInternals()` method to expose receiver config
   - Use build tag `testing` to avoid production impact

2. **Create receiver-level test** (`congestion/live/receive_config_test.go`)
   - Test that `NewReceiver()` with `UseNakBtree=true` creates a receiver with `useNakBtree=true`
   - This test should PASS (receiver creation is correct)

3. **Create connection-level test** (`connection_nakbtree_test.go`)
   - Test that creating a connection with `UseNakBtree=true` results in a receiver with `useNakBtree=true`
   - This test should **FAIL** before the fix!

4. **Run tests to confirm failure**
   ```bash
   go test -tags=testing -run TestConnectionPassesNakBtreeConfig -v
   # Expected: FAIL (config not passed)
   ```

### Phase 2: Implement the Fix

1. **Modify `connection.go`** line 403-414
   - Add all NAK btree config fields to `NewReceiver()` call
   - Handle `TsbpdDelay` timing (may need deferred setting)

2. **Run tests again**
   ```bash
   go test -tags=testing -run TestConnectionPassesNakBtreeConfig -v
   # Expected: PASS
   ```

### Phase 3: Integration Testing

1. **Run isolation test**
   ```bash
   sudo make test-isolation CONFIG=Isolation-Server-NakBtree-IoUringRecv
   # Expected: ~0 gaps (same as control)
   ```

2. **Run all NAK btree isolation tests**
   ```bash
   sudo make test-isolation CONFIG=Isolation-Server-NakBtree
   sudo make test-isolation CONFIG=Isolation-Server-NakBtree-IoUringRecv
   sudo make test-isolation CONFIG=Isolation-FullNakBtree
   # All should pass with ~0 gaps
   ```

---

## 12. Files to Modify

| File | Change Required |
|------|-----------------|
| `connection.go` | Add NAK btree config fields to `NewReceiver()` call (lines 403-414) |
| `congestion/live/testing.go` | NEW: Add test helper to expose receiver internals |
| `connection_nakbtree_test.go` | NEW: Test that config is passed correctly |
| `congestion/live/receive_config_test.go` | NEW: Test receiver creation with NAK btree config |

### The Actual Code Change (connection.go)

```go
// BEFORE (buggy):
c.recv = live.NewReceiver(live.ReceiveConfig{
    InitialSequenceNumber:  c.initialPacketSequenceNumber,
    PeriodicACKInterval:    10_000,
    PeriodicNAKInterval:    20_000,
    OnSendACK:              c.sendACK,
    OnSendNAK:              c.sendNAK,
    OnDeliver:              c.deliver,
    PacketReorderAlgorithm: c.config.PacketReorderAlgorithm,
    BTreeDegree:            c.config.BTreeDegree,
    LockTimingMetrics:      c.metrics.ReceiverLockTiming,
    ConnectionMetrics:      c.metrics,
})

// AFTER (fixed):
c.recv = live.NewReceiver(live.ReceiveConfig{
    InitialSequenceNumber:  c.initialPacketSequenceNumber,
    PeriodicACKInterval:    10_000,
    PeriodicNAKInterval:    20_000,
    OnSendACK:              c.sendACK,
    OnSendNAK:              c.sendNAK,
    OnDeliver:              c.deliver,
    PacketReorderAlgorithm: c.config.PacketReorderAlgorithm,
    BTreeDegree:            c.config.BTreeDegree,
    LockTimingMetrics:      c.metrics.ReceiverLockTiming,
    ConnectionMetrics:      c.metrics,

    // NAK btree configuration
    UseNakBtree:            c.config.UseNakBtree,
    SuppressImmediateNak:   c.config.SuppressImmediateNak,
    TsbpdDelay:             c.tsbpdDelay,
    NakRecentPercent:       c.config.NakRecentPercent,
    NakMergeGap:            c.config.NakMergeGap,
    NakConsolidationBudget: uint64(c.config.NakConsolidationBudget.Microseconds()),

    // FastNAK configuration
    FastNakEnabled:       c.config.FastNakEnabled,
    FastNakThresholdUs:   uint64(c.config.FastNakThreshold.Microseconds()),
    FastNakRecentEnabled: c.config.FastNakRecentEnabled,
})
```

---

## 13. TsbpdDelay Timing Issue

### Potential Problem

The `c.tsbpdDelay` field is set in `handleHandshake()` (line 1261) AFTER the connection is created:

```go
// In handleHandshake(), line 1261:
c.tsbpdDelay = uint64(recvTsbpdDelay) * 1000  // Convert ms to µs
```

But `NewReceiver()` is called in `newSRTConn()` BEFORE the handshake completes.

### Possible Solutions

**Option A**: Pass `tsbpdDelay` separately after handshake
```go
// Add method to receiver:
func (r *receiver) SetTsbpdDelay(delay uint64) {
    r.tsbpdDelay = delay
}

// Call after handshake:
c.recv.SetTsbpdDelay(c.tsbpdDelay)
```

**Option B**: Use a pointer/reference
```go
// Pass pointer to connection's tsbpdDelay
TsbpdDelayPtr: &c.tsbpdDelay,  // Receiver reads via pointer
```

**Option C**: Use default until set
- The default `nakRecentPercent = 0.10` with `tsbpdDelay = 0` means `tooRecentThreshold = now`
- This is still safe: PktTsbpdTime for fresh packets is `now + tsbpdDelay > now`
- Packets will still be marked as "too recent" even with default values
- After handshake sets `tsbpdDelay`, the correct threshold is used

**Recommendation**: Investigate Option A or C. The critical fix is ensuring `UseNakBtree=true` is passed - the `tsbpdDelay` timing may be acceptable with defaults.

---

## 14. Implementation Progress

### ✅ Phase 1: Create Failing Tests — COMPLETED

**Files created:**
- `congestion/live/testing.go` - Test helper to expose receiver internals
- `congestion/live/receive_config_test.go` - Receiver-level tests
- `connection_nakbtree_test.go` - Connection-level tests (the critical ones)

**Test Results (Before Fix):**

| Test | Result | Significance |
|------|--------|--------------|
| `TestReceiverCreationWithNakBtreeConfig` | ✅ PASS | Receiver handles config correctly when passed directly |
| `TestPeriodicNakDispatchesToCorrectImplementation` | ✅ PASS | Dispatch logic works correctly |
| `TestConnectionPassesNakBtreeConfigToReceiver` | ❌ FAIL | **BUG CONFIRMED** - Config NOT passed |
| `TestConnectionWithIoUringAutoEnablesNakBtree` | ❌ FAIL | **BUG CONFIRMED** - io_uring protection NOT active |

**Key test output:**
```
connection_nakbtree_test.go:51: UseNakBtree not passed: expected true, got false
connection_nakbtree_test.go:52: BUG CONFIRMED: NAK btree config is NOT being passed from connection to receiver!
```

**Run tests with:**
```bash
go test -tags=testing -run TestConnectionPassesNakBtreeConfigToReceiver -v
```

---

### ✅ Phase 2: Implement the Fix — COMPLETED

**Change made in `connection.go` lines 403-426:**

```go
c.recv = live.NewReceiver(live.ReceiveConfig{
    InitialSequenceNumber:  c.initialPacketSequenceNumber,
    PeriodicACKInterval:    10_000,
    PeriodicNAKInterval:    20_000,
    OnSendACK:              c.sendACK,
    OnSendNAK:              c.sendNAK,
    OnDeliver:              c.deliver,
    PacketReorderAlgorithm: c.config.PacketReorderAlgorithm,
    BTreeDegree:            c.config.BTreeDegree,
    LockTimingMetrics:      c.metrics.ReceiverLockTiming,
    ConnectionMetrics:      c.metrics,

    // NAK btree configuration - enables TSBPD-based "too recent" protection for io_uring
    UseNakBtree:            c.config.UseNakBtree,
    SuppressImmediateNak:   c.config.SuppressImmediateNak,
    TsbpdDelay:             c.tsbpdDelay,
    NakRecentPercent:       c.config.NakRecentPercent,
    NakMergeGap:            c.config.NakMergeGap,
    NakConsolidationBudget: c.config.NakConsolidationBudgetUs,

    // FastNAK configuration - quick NAK after silence period
    FastNakEnabled:       c.config.FastNakEnabled,
    FastNakThresholdUs:   c.config.FastNakThresholdMs * 1000,
    FastNakRecentEnabled: c.config.FastNakRecentEnabled,
})
```

**Test Results (After Fix):**

| Test | Result |
|------|--------|
| `TestConnectionPassesNakBtreeConfigToReceiver` | ✅ PASS |
| `TestConnectionPassesDisabledNakBtreeConfigToReceiver` | ✅ PASS |
| `TestConnectionWithIoUringAutoEnablesNakBtree` | ✅ PASS |
| All existing tests (`go test ./... -short`) | ✅ PASS |

---

### ✅ Phase 3: Integration Test — COMPLETED (Partial Success)

**Test Run:** `sudo make test-isolation CONFIG=Isolation-Server-NakBtree-IoUringRecv`

**Results:**
```
╔════════════════════════════════════════════════════════════════════╗
║ ISOLATION TEST RESULTS: Isolation-Server-NakBtree-IoUringRecv      ║
╠════════════════════════════════════════════════════════════════════╣
║ SERVER METRICS                      Control         Test       Diff ║
║ ────────────────────────────── ──────────── ──────────── ────────── ║
║ Packets Received                      19530        19470      -0.3% ║
║ Gaps Detected                             0            0          = ║
║ Retrans Received                          0            0          = ║
║ NAKs Sent                                 0          126        NEW ║
║ Drops                                     0            0          = ║
║                                                                    ║
║ CLIENT-GENERATOR METRICS            Control         Test       Diff ║
║ ────────────────────────────── ──────────── ──────────── ────────── ║
║ Packets Sent                          19530        19470      -0.3% ║
║ Retrans Sent                              0            0          = ║
║ NAKs Received                             0          126        NEW ║
╚════════════════════════════════════════════════════════════════════╝
```

**What's Working ✅:**
- **Gaps Detected = 0** — The main bug is fixed! No false gap detection.
- **Retrans Received = 0** — No unnecessary retransmissions.
- **Drops = 0** — No packet drops.

**What's Unexpected ⚠️:**
1. **126 NAKs sent** (vs 0 for Control) — NAKs are being generated but not causing retransmissions
2. **60 fewer packets received** (19470 vs 19530 = -0.3%)
3. **RTT is 75x higher** (6.0ms vs 0.08ms for Control)

---

## 15. Post-Fix Observations (2024-12-15)

### 15.1 Observation Summary

| Metric | Before Fix | After Fix | Change |
|--------|------------|-----------|--------|
| Gaps Detected | 2939 | **0** | ✅ FIXED |
| Retrans Received | 2964 | **0** | ✅ FIXED |
| Drops | 2964 | **0** | ✅ FIXED |
| NAKs Sent | 863 | **126** | ⚠️ Reduced but not zero |
| RTT | 4.36ms | 6.0ms | ⚠️ Still elevated |
| Packets Received | 22434 (inflated) | 19470 (-0.3%) | ⚠️ Slightly lower than control |

### 15.2 Why 126 NAKs with 0 Retransmissions?

**Observation**: The server sent 126 NAKs, but the client didn't retransmit any packets. Why?

**Possible Explanations:**

1. **NAKs for "phantom" gaps that filled before retransmission**
   - The NAK btree detected gaps (correctly or incorrectly)
   - NAKs were sent to the client
   - But before the client could retransmit, the "missing" packets arrived from io_uring
   - Client's retransmit logic saw the packets were already ACK'd, so didn't retransmit

2. **FastNAK triggering during connection setup/teardown**
   - FastNAK detects "silence" periods and triggers NAKs
   - During connection establishment or shutdown, there may be brief gaps
   - These trigger FastNAK but don't represent real packet loss

3. **`tsbpdDelay = 0` at receiver creation time**
   - The receiver is created BEFORE the handshake completes
   - At creation time, `c.tsbpdDelay = 0`
   - The "too recent" threshold calculation: `tooRecentThreshold = now + (tsbpdDelay × nakRecentPercent)`
   - With `tsbpdDelay = 0`, `tooRecentThreshold = now`
   - This means ALL packets with `PktTsbpdTime > now` are "too recent" (should be safe)
   - But what about initial packets before TSBPD is calibrated?

4. **Race between io_uring completion and NAK scan**
   - Even with the "too recent" protection, there may be edge cases
   - The periodic NAK scan runs every 20ms
   - If io_uring has a batch in flight during the scan, temporary gaps may be detected

### 15.3 Why 60 Fewer Packets?

**Observation**: Test received 19470 packets, Control received 19530 (60 fewer = 0.3%)

**Possible Explanations:**

1. **Timing differences in test setup**
   - The test and control pipelines start at slightly different times
   - 60 packets at 5Mbps ≈ 0.1 seconds of data
   - Could be connection establishment timing

2. **NAK overhead affecting throughput**
   - 126 NAK packets consume some bandwidth/CPU
   - This might slightly reduce the effective data rate
   - 60 packets = 0.3% reduction matches the ~0.6% NAK overhead (126 NAKs / 19470 packets)

3. **RTT increase affecting flow**
   - Higher RTT (6ms vs 0.08ms) could affect ACK flow
   - Slightly slower ACK responses might reduce sender rate

### 15.4 Why RTT is 75x Higher?

**Observation**: Test RTT = 6.0ms, Control RTT = 0.08ms

**Possible Explanations:**

1. **io_uring processing overhead**
   - io_uring batch completion adds latency
   - Packets sit in completion queue before being processed
   - This adds to measured RTT

2. **NAK processing overhead**
   - Processing 126 NAKs adds CPU work
   - This might delay ACK responses

3. **Different code paths**
   - NAK btree path has additional processing
   - TSBPD checks, btree operations, etc.

### 15.5 Hypotheses for Remaining NAKs

**Hypothesis A: FastNAK is Overly Aggressive**

FastNAK triggers after a "silence" period (default 50ms). During normal operation, there shouldn't be 50ms gaps. But:
- Connection setup/teardown might have gaps
- io_uring batch scheduling might create micro-gaps

**Test**: Run with FastNAK disabled:
```bash
# Modify test config to set FastNakEnabled: false
```

**Hypothesis B: Initial Packets Before TSBPD Calibration**

When the connection starts, `tsbpdDelay` is 0. The first few packets might have incorrect `PktTsbpdTime` values, causing false gap detection.

**Test**: Add logging to see when NAKs are generated (early in connection vs steady state)

**Hypothesis C: NAK btree Scan Timing**

The periodic NAK scan (every 20ms) might catch io_uring mid-batch. Even with "too recent" protection, edge cases might slip through.

**Test**: Increase NAK interval or add debug logging to see scan timing vs packet arrival

### 15.6 Proposed Next Steps

**Option A: Accept Current State (Pragmatic)**

The main issue (2939 false gaps → 0 gaps) is fixed. 126 NAKs with 0 retransmissions is a minor inefficiency but doesn't affect data integrity. The 0.3% throughput difference is within acceptable variance.

**Pros**: No further changes needed
**Cons**: Small inefficiency remains

**Option B: Investigate FastNAK (Recommended)**

Create a test config with `FastNakEnabled: false` and re-run to isolate FastNAK's contribution.

```go
// In test_configs.go, add:
{
    Name:          "Isolation-Server-NakBtree-IoUringRecv-NoFastNak",
    Description:   "Server: NAK btree + io_uring recv (FastNAK disabled)",
    TestServer:    ControlSRTConfig.WithNakBtree().WithIoUringRecv(),
    // Override to disable FastNAK
}
```

**Pros**: Quick to test
**Cons**: FastNAK is useful for real outages

**Option C: Add Debug Logging**

Add temporary debug logging to understand WHEN the 126 NAKs are generated:
- At connection start?
- During steady state?
- At connection end?

**Pros**: Provides insight
**Cons**: Requires code changes and another test run

**Option D: Fix TsbpdDelay Timing Issue**

The receiver is created with `tsbpdDelay = 0` before the handshake. Add a mechanism to update the receiver's `tsbpdDelay` after handshake completes.

```go
// Add method to receiver:
func (r *receiver) SetTsbpdDelay(delay uint64)

// Call after handshake in connection.go:
c.recv.SetTsbpdDelay(c.tsbpdDelay)
```

**Pros**: Fixes a potential source of early NAKs
**Cons**: More complex change

### 15.7 Observability Improvements Required

Before making further code changes, we need better visibility into what's happening. The current output makes it hard to:
1. Distinguish which server/client is which in the `[PUB]` output
2. See NAK counts in the periodic output
3. Identify which JSON output belongs to which instance
4. Access detailed Prometheus metrics during tests

---

## 16. Observability Improvement Plan

### 16.1 Problem: Can't Distinguish Instances

Current output:
```
[PUB] 10:28:15.71 |   302.1 pkt/s | ... | 3.0k ok /     0 gaps /     0 retx | recovery=100.0%
[PUB] 10:28:15.72 |   302.1 pkt/s | ... | 3.0k ok /     0 gaps /     0 retx | recovery=100.0%
```

Both show `[PUB]` - we can't tell which is Control vs Test.

### 16.2 Proposed Changes

#### Change 1: Add `-name` Flag to Identify Instances

**File**: `contrib/common/flags.go`

```go
// Instance identification flag
InstanceName = flag.String("name", "",
    "Name/description for this instance (shown in logs and metrics output)")
```

**Usage**:
```bash
# Control server
./server -addr 10.2.1.2:6000 -name "Control" ...
# Test server
./server -addr 10.2.1.3:6001 -name "Test" ...
```

**Output becomes**:
```
[Control] 10:28:15.71 | 302.1 pkt/s | ... | 3.0k ok / 0 gaps / 0 NAKs / 0 retx | recovery=100.0%
[Test]    10:28:15.72 | 302.1 pkt/s | ... | 3.0k ok / 0 gaps / 126 NAKs / 0 retx | recovery=100.0%
```

---

#### Change 2: Add NAKs to Periodic Output

**File**: `contrib/common/statistics.go`

Current `ThroughputGetter` signature:
```go
type ThroughputGetter func() (bytes, pkts, gapsPkts, skipsPkts, retransPkts uint64)
```

Proposed change:
```go
type ThroughputGetter func() (bytes, pkts, gapsPkts, naksPkts, skipsPkts, retransPkts uint64)
```

**Output format change**:
```
[name] HH:MM:SS.xx | 999.9 pkt/s | 99.99 MB | 9.999 Mb/s | 9999k ok / 999 gaps / 999 NAKs / 999 retx | recovery=100.0%
                                                                        ^^^^^^^^ NEW
```

This requires updating:
- `contrib/common/statistics.go` - Add NAK to getter and output
- `contrib/server/main.go` - Update getter to return NAK count
- `contrib/client-generator/main.go` - Update getter to return NAK count

---

#### Change 3: Add Instance Name to connection_closed JSON

**File**: `connection.go` (line ~2050)

The connection_closed JSON currently has no way to identify which instance it came from.

**Option A**: Add `name` field from srt.Config
```go
output := map[string]interface{}{
    "timestamp":     time.Now().Format(time.RFC3339Nano),
    "event":         "connection_closed",
    "instance_name": c.config.InstanceName,  // NEW
    "socket_id":     fmt.Sprintf("0x%08x", c.socketId),
    ...
}
```

This requires:
- Add `InstanceName string` to `srt.Config`
- Add `-name` flag handling in `ApplyFlagsToConfig`
- Include in connection_closed output

---

#### Change 4: Add PRINT_PROM Option to Isolation Tests

**File**: `contrib/integration_testing/test_isolation_mode.go`

Add environment variable check:
```go
// At end of test, before cleanup:
if os.Getenv("PRINT_PROM") == "true" {
    fmt.Println("\n=== PROMETHEUS METRICS (Control Server) ===")
    printPrometheusMetrics(controlServerPromPath)
    fmt.Println("\n=== PROMETHEUS METRICS (Test Server) ===")
    printPrometheusMetrics(testServerPromPath)
    fmt.Println("\n=== PROMETHEUS METRICS (Control CG) ===")
    printPrometheusMetrics(controlCGPromPath)
    fmt.Println("\n=== PROMETHEUS METRICS (Test CG) ===")
    printPrometheusMetrics(testCGPromPath)
}
```

**Usage**:
```bash
sudo PRINT_PROM=true make test-isolation CONFIG=Isolation-Server-NakBtree-IoUringRecv
```

---

### 16.3 Files to Modify

| File | Change |
|------|--------|
| `contrib/common/flags.go` | Add `InstanceName` flag |
| `contrib/common/statistics.go` | Add NAKs to `ThroughputGetter` and output format |
| `contrib/server/main.go` | Update getter, pass name to display |
| `contrib/client-generator/main.go` | Update getter, pass name to display |
| `config.go` | Add `InstanceName` field to `Config` |
| `connection.go` | Include `instance_name` in connection_closed JSON |
| `contrib/integration_testing/test_isolation_mode.go` | Add PRINT_PROM support |
| `contrib/integration_testing/config.go` | Add `-name` to CLI flags generation |

---

### 16.4 Implementation Order

1. **Phase 1**: Add `-name` flag and update periodic output
   - Modify `flags.go`, `statistics.go`, `server/main.go`, `client-generator/main.go`
   - Test manually to verify output

2. **Phase 2**: Add instance name to connection_closed JSON
   - Modify `config.go`, `connection.go`
   - Test to verify JSON output includes name

3. **Phase 3**: Add PRINT_PROM support
   - Modify `test_isolation_mode.go`
   - Test with `PRINT_PROM=true`

4. **Phase 4**: Update integration test CLI flags
   - Modify `contrib/integration_testing/config.go` to pass `-name Control` and `-name Test`
   - Run isolation test to verify improved output

---

### 16.5 Expected Output After Changes

**Periodic output**:
```
[Control] 10:28:15.71 | 302.1 pkt/s | 5.90 MB | 4.950 Mb/s | 3.0k ok /     0 gaps /     0 NAKs /     0 retx | recovery=100.0%
[Test]    10:28:15.72 | 302.1 pkt/s | 5.90 MB | 4.950 Mb/s | 3.0k ok /     0 gaps /   126 NAKs /     0 retx | recovery=100.0%
```

**connection_closed JSON**:
```json
{
  "timestamp": "2025-12-15T10:28:37.736041389-08:00",
  "event": "connection_closed",
  "instance_name": "Control",
  "socket_id": "0x2b78e33f",
  "remote_addr": "10.2.1.2:6000",
  ...
}
```

**PRINT_PROM output**:
```
=== PROMETHEUS METRICS (Test Server) ===
gosrt_connection_gaps_detected_total{socket_id="0x89dac4ad"} 0
gosrt_connection_naks_sent_total{socket_id="0x89dac4ad"} 126
gosrt_connection_nak_btree_scan_runs_total{socket_id="0x89dac4ad"} 1500
...
```

---

### 16.6 Decision Required

**Question**: Should we proceed with these observability improvements before investigating the 126 NAKs further?

**Recommendation**: Yes - better observability will help us understand:
1. WHEN the NAKs are generated (connection start, steady state, or shutdown)
2. Which specific metrics are involved
3. Any timing patterns between Control and Test

**Do you want to proceed with implementation?**

---

## Change Log

| Date | Change |
|------|--------|
| 2024-12-15 | Initial debugging document created |
| 2024-12-15 | `Isolation-Server-NakBtree` passed - NAK btree alone is correct |
| 2024-12-15 | `Isolation-Server-NakBtree-IoUringRecv-LargeWindow` FAILED - 50% window didn't help |
| 2024-12-15 | ~~ROOT CAUSE: "Too recent" threshold uses TSBPD time~~ (superseded) |
| 2024-12-15 | **ACTUAL ROOT CAUSE FOUND**: NAK btree config NOT PASSED to receiver! |
| 2024-12-15 | The design is CORRECT. Bug is in `connection.go` line 403-414 wiring. |
| 2024-12-15 | Created TDD plan: failing tests first, then fix, then integration tests |
| 2024-12-15 | **Phase 1 COMPLETED**: Created failing tests (confirmed bug exists) |
| 2024-12-15 | **Phase 2 COMPLETED**: Implemented fix in `connection.go` - all tests pass |
| 2024-12-15 | **Phase 3 COMPLETED**: Integration test - **MAIN BUG FIXED** (0 gaps vs 2939) |
| 2024-12-15 | Post-fix: 126 NAKs still generated (vs 863 before), needs investigation |
| 2024-12-15 | Proposed observability improvements to help debug remaining 126 NAKs |

