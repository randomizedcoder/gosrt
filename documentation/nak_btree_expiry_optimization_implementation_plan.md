# NAK Btree Expiry Optimization - Implementation Plan

> **Document Purpose:** Step-by-step implementation guide with precise Go file/function/line references.
> **Design Document:** `nak_btree_expiry_optimization.md` (parent design)
> **Status:** IMPLEMENTATION PLAN
> **Date:** 2026-01-11

---

## Table of Contents

1. [Overview](#1-overview)
   - [1.1 Goals](#11-goals)
   - [1.2 Key Files](#12-key-files)
   - [1.3 Implementation Phases](#13-implementation-phases)
2. [Phase 1: Configuration Infrastructure](#phase-1-configuration-infrastructure)
   - [Step 1.1: Add NakExpiryMargin to Config](#step-11-add-nakexpirymargin-to-config)
   - [Step 1.2: Add CLI Flag](#step-12-add-cli-flag)
   - [Step 1.3: Verification](#step-13-verification)
3. [Phase 2: Data Structure Changes](#phase-2-data-structure-changes)
   - [Step 2.1: Update NakEntryWithTime Struct](#step-21-update-nakentrywithtyme-struct)
   - [Step 2.2: Add InsertWithTsbpd Methods](#step-22-add-insertwithtsbpd-methods)
   - [Step 2.3: Add DeleteBeforeTsbpd Methods](#step-23-add-deletebeforetsbpd-methods)
   - [Step 2.4: Verification](#step-24-verification)
4. [Phase 3: Receiver Infrastructure](#phase-3-receiver-infrastructure)
   - [Step 3.1: Add Fields to Receiver Struct](#step-31-add-fields-to-receiver-struct)
   - [Step 3.2: Wire Up Configuration](#step-32-wire-up-configuration)
   - [Step 3.3: Add Function Dispatch](#step-33-add-function-dispatch)
   - [Step 3.4: Verification](#step-34-verification)
5. [Phase 4: TSBPD Estimation](#phase-4-tsbpd-estimation)
   - [Step 4.1: Add estimateTsbpdForSeq Function](#step-41-add-estimatetsbpdforseq-function)
   - [Step 4.2: Add Inter-Packet Interval Tracking](#step-42-add-inter-packet-interval-tracking)
   - [Step 4.3: Add Fallback Estimation](#step-43-add-fallback-estimation)
   - [Step 4.4: Verification](#step-44-verification)
6. [Phase 5: Gap Scan Integration](#phase-5-gap-scan-integration)
   - [Step 5.1: Update gapScan to Calculate TSBPD](#step-51-update-gapscan-to-calculate-tsbpd)
   - [Step 5.2: Add Pre-filter for Expired Entries](#step-52-add-pre-filter-for-expired-entries)
   - [Step 5.3: Verification](#step-53-verification)
7. [Phase 6: Expiry Logic](#phase-6-expiry-logic)
   - [Step 6.1: Add calculateExpiryThreshold](#step-61-add-calculateexpirythreshold)
   - [Step 6.2: Modify expireNakEntries](#step-62-modify-expirenakentries)
   - [Step 6.3: Verification](#step-63-verification)
8. [Phase 7: Metrics](#phase-7-metrics)
   - [Step 7.1: Add Metric Fields](#step-71-add-metric-fields)
   - [Step 7.2: Export Metrics](#step-72-export-metrics)
   - [Step 7.3: Verification](#step-73-verification)
9. [Phase 8: Unit Tests](#phase-8-unit-tests)
   - [Step 8.1: NakEntryWithTime Tests](#step-81-nakentrywithtyme-tests)
   - [Step 8.2: DeleteBeforeTsbpd Tests](#step-82-deletebeforetsbpd-tests)
   - [Step 8.3: TSBPD Estimation Tests](#step-83-tsbpd-estimation-tests)
   - [Step 8.4: Corner Case Tests](#step-84-corner-case-tests)
10. [Phase 9: Benchmark Tests](#phase-9-benchmark-tests)
11. [Phase 10: Integration Testing](#phase-10-integration-testing)
12. [Conclusion](#conclusion)

---

## 1. Overview

### 1.1 Goals

Implement RTT-aware early expiry of NAK btree entries to eliminate "phantom NAKs" (`nak_not_found`)
while preserving recovery opportunity for urgent packets.

**Key formula:**
```
expiryThreshold = now + (RTO * (1 + nakExpiryMargin))
```

### 1.2 Key Files

| File | Lines | Changes |
|------|-------|---------|
| `config.go` | 358-359 | Add `NakExpiryMargin` and `EWMAWarmupThreshold` fields |
| `config.go` | 624 | Add default values |
| `contrib/common/flags.go` | ~140 | Add `-nakexpirymargin` and `-ewmawarmupthreshold` CLI flags |
| `congestion/live/receive/nak_btree.go` | 15-19 | Add `TsbpdTimeUs` field |
| `congestion/live/receive/nak_btree.go` | 83+ | Add new methods |
| `congestion/live/receive/nak.go` | 509-544 | Modify `expireNakEntries()` |
| `congestion/live/receive/nak.go` | NEW | Add TSBPD estimation functions |
| `congestion/live/receive/receiver.go` | 77+ | Add new fields |
| `congestion/live/receive/push.go` | TOP | Add `const` block with named constants |
| `congestion/live/receive/push.go` | 138 | Add `updateInterPacketInterval()` function |
| `congestion/live/receive/push_test.go` | NEW | Unit tests for inter-packet tracking |
| `metrics/metrics.go` | 251 | Add expiry + estimation counters |
| `metrics/handler.go` | ~800 | Export new metrics |
| `metrics/handler_test.go` | NEW | Test new metrics export |

### 1.3 Implementation Phases

| Phase | Description | Estimated Time |
|-------|-------------|----------------|
| Phase 1 | Configuration Infrastructure | 30 min |
| Phase 2 | Data Structure Changes | 1 hour |
| Phase 3 | Receiver Infrastructure | 45 min |
| Phase 4 | TSBPD Estimation | 1 hour |
| Phase 5 | Gap Scan Integration | 1.5 hours |
| Phase 6 | Expiry Logic | 1 hour |
| Phase 7 | Metrics | 30 min |
| Phase 8 | Unit Tests | 2 hours |
| Phase 9 | Benchmark Tests | 1 hour |
| Phase 10 | Integration Testing | 2 hours |
| **Total** | | **~11 hours** |

---

## Phase 1: Configuration Infrastructure

### Step 1.1: Add NakExpiryMargin to Config

**File:** `config.go`

#### Step 1.1a: Add to Config Struct

**Location:** After `ExtraRTTMargin` (line 358)

```go
	ExtraRTTMargin float64

	// --- NAK Btree Expiry Configuration ---

	// NakExpiryMargin adds extra margin when expiring NAK btree entries.
	// Specified as a percentage (0.1 = 10% extra margin).
	//
	// Formula: expiryThreshold = now + (RTO * (1 + nakExpiryMargin))
	//
	// Higher values = more conservative (keep NAK entries longer, favor recovery).
	// Lower values = more aggressive (expire entries earlier, reduce phantom NAKs).
	//
	// Values:
	//   0.0:  Baseline - expire at exactly now + RTO
	//   0.05: 5% margin - slightly conservative
	//   0.10: 10% margin (default) - moderately conservative
	//   0.25: 25% margin - more conservative
	//   0.50: 50% margin - very conservative (high-jitter networks)
	//
	// Default: 0.10 (10% - prefer potential repair over phantom NAK reduction)
	NakExpiryMargin float64
```

#### Step 1.1b: Add Default Value

**Location:** In `DefaultConfig()` (after line 624)

```go
	ExtraRTTMargin: 0.10,         // 10% extra margin (only for RTORttRttVarMargin mode)

	// NAK btree expiry defaults
	NakExpiryMargin: 0.10, // 10% margin - slightly conservative, favors recovery
```

**Checkpoint:**
```bash
go build ./...
```

### Step 1.2: Add CLI Flag

**File:** `contrib/common/flags.go`

#### Step 1.2a: Add Flag Variable

**Location:** After `ExtraRTTMargin` flag definition (line 121-122)

```go
var ExtraRTTMargin = flag.Float64("extrarttmargin", 0,
	"Extra RTT margin as decimal (0.1 = 10%, default: 0.1). Only used with rtomode=rtt_rttvar_margin")

// NAK Btree Expiry Optimization (nak_btree_expiry_optimization.md)
var NakExpiryMargin = flag.Float64("nakexpirymargin", 0.10,
	"NAK btree expiry margin as percentage (0.1 = 10%). "+
		"Formula: expiryThreshold = now + (RTO * (1 + nakExpiryMargin)). "+
		"Higher values keep NAK entries longer, favoring recovery over phantom NAK reduction.")
```

#### Step 1.2b: Add to ApplyFlagsToConfig with Validation

**Location:** In `ApplyFlagsToConfig()` function (after line 468)

```go
	if FlagSet["extrarttmargin"] {
		config.ExtraRTTMargin = *ExtraRTTMargin
	}
	if FlagSet["nakexpirymargin"] {
		config.NakExpiryMargin = *NakExpiryMargin
	}

	// Validate NakExpiryMargin bounds
	// A value < -1.0 would result in an expiry threshold in the past,
	// effectively disabling all NAKs and causing 100% packet loss.
	// We allow -1.0 as a potential "disable time-based expiry" signal,
	// but anything more negative is nonsensical.
	if config.NakExpiryMargin < -1.0 {
		log.Printf("WARNING: NakExpiryMargin %.2f is invalid (< -1.0), resetting to default 0.10",
			config.NakExpiryMargin)
		config.NakExpiryMargin = 0.10
	}
```

**Why this validation matters:**
- `nakExpiryMargin = 0.0`: threshold = now + RTO (baseline)
- `nakExpiryMargin = -0.5`: threshold = now + RTO*0.5 (aggressive)
- `nakExpiryMargin = -1.0`: threshold = now + RTO*0 = now (max aggressive, expire everything)
- `nakExpiryMargin < -1.0`: threshold = now - something = **past** (broken!)

A negative threshold would expire *every* NAK entry immediately, preventing any
loss recovery. The `-1.0` limit allows maximum aggressiveness while preventing
catastrophic misconfiguration.

**Checkpoint:**
```bash
go build ./contrib/...
```

### Step 1.3: Add EWMAWarmupThreshold Config

**File:** `config.go`

#### Step 1.3a: Add to Config Struct

**Location:** After `NakExpiryMargin` in Config struct

```go
	NakExpiryMargin float64

	// EWMAWarmupThreshold is the minimum number of packets needed before
	// inter-packet interval EWMA is considered "warm" (reliable).
	//
	// Rationale:
	// - EWMA with α=0.125 reaches ~95% of true value after ~24 samples
	// - Default of 32 provides safety margin for variance
	// - At 1000 pps, this is only 32ms of data
	// - At 100 pps (low bitrate), this is 320ms
	//
	// Values:
	//   0:  Disable warm-up check (always use EWMA, even if cold)
	//   16: Fast warm-up (high-rate streams, less accuracy)
	//   32: Default (balanced)
	//   64: Slow warm-up (low-rate streams, more accuracy)
	//
	// During warm-up (sampleCount < threshold), we use conservative
	// fallback estimation (tsbpdDelay as worst-case estimate).
	//
	// Default: 32
	EWMAWarmupThreshold uint32
```

#### Step 1.3b: Add Default Value

**Location:** In `DefaultConfig()` (after NakExpiryMargin)

```go
	// NAK btree expiry defaults
	NakExpiryMargin:     0.10, // 10% margin - slightly conservative, favors recovery
	EWMAWarmupThreshold: 32,   // 32 samples before EWMA considered warm
```

### Step 1.4: Add EWMAWarmupThreshold CLI Flag

**File:** `contrib/common/flags.go`

#### Step 1.4a: Add Flag Variable

**Location:** After NakExpiryMargin flag definition

```go
var NakExpiryMargin = flag.Float64("nakexpirymargin", 0.10,
	"NAK btree expiry margin as percentage (0.1 = 10%). "+
		"Formula: expiryThreshold = now + (RTO * (1 + nakExpiryMargin)). "+
		"Higher values keep NAK entries longer, favoring recovery over phantom NAK reduction.")

var EWMAWarmupThreshold = flag.Uint("ewmawarmupthreshold", 32,
	"Minimum packets before inter-packet EWMA is considered warm (reliable). "+
		"Set to 0 to disable warm-up check. "+
		"Higher values improve accuracy but delay time-based expiry. "+
		"Default: 32 (balanced for most streams)")
```

#### Step 1.4b: Add to ApplyFlagsToConfig

**Location:** In `ApplyFlagsToConfig()` function (after NakExpiryMargin handling)

```go
	if FlagSet["nakexpirymargin"] {
		config.NakExpiryMargin = *NakExpiryMargin
	}
	if FlagSet["ewmawarmupthreshold"] {
		config.EWMAWarmupThreshold = uint32(*EWMAWarmupThreshold)
	}
```

**Note:** No validation needed for EWMAWarmupThreshold - any value >= 0 is valid:
- 0 = disable warm-up (always use EWMA)
- Any positive value = require that many samples before trusting EWMA

**Checkpoint:**
```bash
go build ./contrib/...
```

### Step 1.5: Verification

#### Step 1.5a: Build Test

```bash
cd /home/das/Downloads/srt/gosrt
go build ./...
```

#### Step 1.5b: Flag Test

**File:** `contrib/common/test_flags.sh`

Add test cases (after ExtraRTTMargin tests):

```bash
# Test: NAK expiry margin flag
run_test "NakExpiryMargin flag default" "" '"NakExpiryMargin" *: *0\.1' "$SERVER_BIN"
run_test "NakExpiryMargin flag custom" "-nakexpirymargin 0.25" '"NakExpiryMargin" *: *0\.25' "$SERVER_BIN"

# Test: EWMA warm-up threshold flag
run_test "EWMAWarmupThreshold flag default" "" '"EWMAWarmupThreshold" *: *32' "$SERVER_BIN"
run_test "EWMAWarmupThreshold flag custom" "-ewmawarmupthreshold 64" '"EWMAWarmupThreshold" *: *64' "$SERVER_BIN"
run_test "EWMAWarmupThreshold flag disabled" "-ewmawarmupthreshold 0" '"EWMAWarmupThreshold" *: *0' "$SERVER_BIN"
```

Run:
```bash
make test-flags
```

---

## Phase 2: Data Structure Changes

### Step 2.1: Update NakEntryWithTime Struct

**File:** `congestion/live/receive/nak_btree.go`

**Location:** Lines 15-19

**Current:**
```go
type NakEntryWithTime struct {
	Seq           uint32 // Missing sequence number
	LastNakedAtUs uint64 // When we last sent NAK for this seq (microseconds)
	NakCount      uint32 // Number of times NAK'd
}
```

**Change to:**
```go
// NakEntryWithTime stores a missing sequence number with timing information.
// Used in NAK btree to track:
// - When the packet should be delivered (TsbpdTimeUs) - for RTT-aware expiry
// - When we last sent NAK (LastNakedAtUs) - for NAK suppression
// - How many times NAK'd (NakCount) - for metrics
type NakEntryWithTime struct {
	Seq           uint32 // Missing sequence number
	TsbpdTimeUs   uint64 // TSBPD release time for this sequence (microseconds)
	LastNakedAtUs uint64 // When we last sent NAK for this seq (microseconds)
	NakCount      uint32 // Number of times NAK'd
}
```

**Checkpoint:**
```bash
go build ./congestion/...
```

### Step 2.2: Add InsertWithTsbpd Methods

**File:** `congestion/live/receive/nak_btree.go`

**Location:** After `InsertBatchLocking()` (after line ~83)

```go
// InsertWithTsbpd adds a missing sequence number with its TSBPD time.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use InsertWithTsbpdLocking().
func (nb *nakBtree) InsertWithTsbpd(seq uint32, tsbpdTimeUs uint64) {
	entry := NakEntryWithTime{
		Seq:           seq,
		TsbpdTimeUs:   tsbpdTimeUs,
		LastNakedAtUs: 0,
		NakCount:      0,
	}
	nb.tree.ReplaceOrInsert(entry)
}

// InsertWithTsbpdLocking adds a missing sequence with TSBPD time, with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) InsertWithTsbpdLocking(seq uint32, tsbpdTimeUs uint64) {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	nb.InsertWithTsbpd(seq, tsbpdTimeUs)
}

// InsertBatchWithTsbpd adds multiple missing sequences with their TSBPD times.
// seqs and tsbpdTimes must have equal length.
// Returns the count of newly inserted sequences (excludes duplicates).
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use InsertBatchWithTsbpdLocking().
func (nb *nakBtree) InsertBatchWithTsbpd(seqs []uint32, tsbpdTimes []uint64) int {
	if len(seqs) == 0 || len(seqs) != len(tsbpdTimes) {
		return 0
	}

	count := 0
	for i, seq := range seqs {
		entry := NakEntryWithTime{
			Seq:           seq,
			TsbpdTimeUs:   tsbpdTimes[i],
			LastNakedAtUs: 0,
			NakCount:      0,
		}
		if _, replaced := nb.tree.ReplaceOrInsert(entry); !replaced {
			count++
		}
	}
	return count
}

// InsertBatchWithTsbpdLocking adds multiple missing sequences with TSBPD times, with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) InsertBatchWithTsbpdLocking(seqs []uint32, tsbpdTimes []uint64) int {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return nb.InsertBatchWithTsbpd(seqs, tsbpdTimes)
}
```

**Checkpoint:**
```bash
go build ./congestion/...
```

### Step 2.3: Add DeleteBeforeTsbpd Methods

**File:** `congestion/live/receive/nak_btree.go`

**Location:** After `DeleteBeforeLocking()` (after line ~128)

```go
// DeleteBeforeTsbpd removes all entries whose TsbpdTimeUs is before the threshold.
// Uses DeleteMin() for O(log n) per delete (no lookup needed, zero allocation).
// This is the optimized implementation - see DeleteBeforeTsbpdSlow for benchmarking.
//
// An entry is expired if: entry.TsbpdTimeUs < expiryThresholdUs
// This means retransmit can't arrive before TSBPD time.
//
// Key invariant: TSBPD times are monotonically increasing with sequence numbers.
// This allows us to stop at the first non-expired entry (sorted order).
//
// Performance: For n entries to expire from btree of size N:
//   - DeleteBeforeTsbpd (optimized):  O(n * log N) - DeleteMin is O(log N), zero allocs
//   - DeleteBeforeTsbpdSlow:          O(n) alloc + O(n * log N) collect + O(n * log N) delete
//
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use DeleteBeforeTsbpdLocking().
func (nb *nakBtree) DeleteBeforeTsbpd(expiryThresholdUs uint64) int {
	deleted := 0

	for {
		// Get the minimum element (oldest sequence = earliest TSBPD)
		minItem, found := nb.tree.Min()
		if !found {
			break // Tree is empty
		}

		// Check if it should be expired (TSBPD < threshold)
		// Due to TSBPD monotonicity invariant, once we find a non-expired entry,
		// all subsequent entries (higher sequences) are also non-expired.
		if minItem.TsbpdTimeUs >= expiryThresholdUs {
			break // Stop at first non-expired
		}

		// Delete the minimum (O(log n), no lookup needed)
		nb.tree.DeleteMin()
		deleted++
	}

	return deleted
}

// DeleteBeforeTsbpdLocking removes entries before threshold with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) DeleteBeforeTsbpdLocking(expiryThresholdUs uint64) int {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return nb.DeleteBeforeTsbpd(expiryThresholdUs)
}

// DeleteBeforeTsbpdSlow is the unoptimized implementation for benchmarking comparison.
// It collects items to remove in a slice, then deletes them in a second pass.
// This requires allocation of the toDelete slice and two traversals.
func (nb *nakBtree) DeleteBeforeTsbpdSlow(expiryThresholdUs uint64) int {
	var toDelete []NakEntryWithTime

	// First pass: collect items to delete
	nb.tree.Ascend(func(entry NakEntryWithTime) bool {
		if entry.TsbpdTimeUs < expiryThresholdUs {
			toDelete = append(toDelete, entry)
			return true
		}
		return false // Stop at first non-expired
	})

	// Second pass: delete collected items
	for _, entry := range toDelete {
		nb.tree.Delete(entry)
	}
	return len(toDelete)
}
```

### Step 2.4: Verification

```bash
go build ./congestion/...
go test ./congestion/live/receive/... -run TestNakBtree -v
```

---

## Phase 3: Receiver Infrastructure

### Step 3.1: Add Fields to Receiver Struct

**File:** `congestion/live/receive/receiver.go`

**Location:** After `avgLinkCapacityBits` (around line 77)

```go
	avgPayloadSizeBits  atomic.Uint64 // float64 via math.Float64bits/Float64frombits
	avgLinkCapacityBits atomic.Uint64 // float64 via math.Float64bits/Float64frombits

	// Inter-packet interval tracking (for TSBPD estimation fallback)
	// See nak_btree_expiry_optimization.md Section 4.6
	avgInterPacketIntervalUs atomic.Uint64 // EWMA of inter-packet arrival interval (µs)
	lastPacketArrivalUs      atomic.Uint64 // Last packet arrival time (µs) for interval calc
	interPacketSampleCount   atomic.Uint32 // Count of valid samples for warm-up tracking
```

**Location:** After `nakConsolidationBudget` (around line 93)

```go
	nakConsolidationBudget time.Duration

	// NAK btree expiry configuration
	// See nak_btree_expiry_optimization.md Section 5
	nakExpiryMargin       float64 // Margin for expiry threshold calculation
	ewmaWarmupThreshold   uint32  // Min samples before EWMA is trusted (0 = disabled)
```

**Location:** In function dispatch section (around line 131)

```go
	nakLen              func() int
	nakIterateAndUpdate func(fn func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool))

	// NAK btree TSBPD-aware function dispatch (Phase: NAK Btree Expiry Optimization)
	nakInsertWithTsbpd      func(seq uint32, tsbpdTimeUs uint64)
	nakInsertBatchWithTsbpd func(seqs []uint32, tsbpdTimes []uint64) int
	nakDeleteBeforeTsbpd    func(expiryThresholdUs uint64) int
```

### Step 3.2: Wire Up Configuration

**File:** `congestion/live/receive/receiver.go`

**Location:** In `New()` function, where NAK btree config is set (around line 200)

```go
		nakMergeGap:            recvConfig.NakMergeGap,
		nakConsolidationBudget: recvConfig.NakConsolidationBudget,
		nakExpiryMargin:        recvConfig.NakExpiryMargin,
		ewmaWarmupThreshold:    recvConfig.EWMAWarmupThreshold,
```

**File:** `congestion/live/receive/ring.go`

**Location:** In `RecvConfig` struct, add fields:

```go
	NakConsolidationBudget time.Duration

	// NAK btree expiry configuration
	NakExpiryMargin       float64
	EWMAWarmupThreshold   uint32
```

### Step 3.3: Add Function Dispatch

**File:** `congestion/live/receive/receiver.go`

**Location:** In `setupNakDispatch()` function (lines 497-519)

Modify the existing function to include the new TSBPD-aware dispatch:

**Current (lines 502-518):**
```go
	if usePacketRing {
		// Event loop mode: lock-free (single-threaded after ring drain)
		r.nakInsert = r.nakBtree.Insert
		r.nakInsertBatch = r.nakBtree.InsertBatch
		r.nakDelete = r.nakBtree.Delete
		r.nakDeleteBefore = r.nakBtree.DeleteBefore
		r.nakLen = r.nakBtree.Len
		r.nakIterateAndUpdate = r.nakBtree.IterateAndUpdate
	} else {
		// Tick mode: locking (concurrent Push/Tick safety)
		r.nakInsert = r.nakBtree.InsertLocking
		r.nakInsertBatch = r.nakBtree.InsertBatchLocking
		r.nakDelete = r.nakBtree.DeleteLocking
		r.nakDeleteBefore = r.nakBtree.DeleteBeforeLocking
		r.nakLen = r.nakBtree.LenLocking
		r.nakIterateAndUpdate = r.nakBtree.IterateAndUpdateLocking
	}
```

**Replace with:**
```go
	if usePacketRing {
		// Event loop mode: lock-free (single-threaded after ring drain)
		r.nakInsert = r.nakBtree.Insert
		r.nakInsertBatch = r.nakBtree.InsertBatch
		r.nakDelete = r.nakBtree.Delete
		r.nakDeleteBefore = r.nakBtree.DeleteBefore
		r.nakLen = r.nakBtree.Len
		r.nakIterateAndUpdate = r.nakBtree.IterateAndUpdate
		// TSBPD-aware methods (NAK Btree Expiry Optimization)
		r.nakInsertWithTsbpd = r.nakBtree.InsertWithTsbpd
		r.nakInsertBatchWithTsbpd = r.nakBtree.InsertBatchWithTsbpd
		r.nakDeleteBeforeTsbpd = r.nakBtree.DeleteBeforeTsbpd
	} else {
		// Tick mode: locking (concurrent Push/Tick safety)
		r.nakInsert = r.nakBtree.InsertLocking
		r.nakInsertBatch = r.nakBtree.InsertBatchLocking
		r.nakDelete = r.nakBtree.DeleteLocking
		r.nakDeleteBefore = r.nakBtree.DeleteBeforeLocking
		r.nakLen = r.nakBtree.LenLocking
		r.nakIterateAndUpdate = r.nakBtree.IterateAndUpdateLocking
		// TSBPD-aware methods (NAK Btree Expiry Optimization)
		r.nakInsertWithTsbpd = r.nakBtree.InsertWithTsbpdLocking
		r.nakInsertBatchWithTsbpd = r.nakBtree.InsertBatchWithTsbpdLocking
		r.nakDeleteBeforeTsbpd = r.nakBtree.DeleteBeforeTsbpdLocking
	}
```

### Step 3.4: Verification

```bash
go build ./congestion/...
```

---

## Phase 4: TSBPD Estimation

### Step 4.1: Add estimateTsbpdForSeq Function

**File:** `congestion/live/receive/nak.go`

**Location:** After imports, before `periodicNakBtreeLocked()` (around line 14)

```go
// estimateTsbpdForSeq calculates TSBPD for a missing sequence using linear interpolation.
// This is critical for accurate expiry timing, especially during large gap scenarios
// (e.g., Starlink 60ms outages causing 100+ packet gaps).
//
// IMPORTANT: TSBPD Monotonicity Guard
// The design relies on TSBPD times increasing monotonically with sequence numbers.
// However, sender-side clock jumps or upstream TSBPD calculation bugs could violate this.
// We guard against this by ensuring estimated TSBPD is never less than lowerTsbpd.
// If a calculation error results in TSBPD in the past, the packet would be immediately
// expired and never NAK'd, losing a recovery opportunity.
//
// See nak_btree_expiry_optimization.md Section 4.5.6 for design rationale.
//
// Parameters:
//   - missingSeq: The missing sequence number needing TSBPD estimation
//   - lowerSeq: Sequence of packet before the gap (has known TSBPD)
//   - lowerTsbpd: PktTsbpdTime of the lower boundary packet
//   - upperSeq: Sequence of packet after the gap (has known TSBPD)
//   - upperTsbpd: PktTsbpdTime of the upper boundary packet
//
// Returns: Estimated TSBPD for missingSeq (microseconds), guaranteed >= lowerTsbpd
func estimateTsbpdForSeq(missingSeq, lowerSeq uint32, lowerTsbpd uint64, upperSeq uint32, upperTsbpd uint64) uint64 {
	// Handle edge cases: same sequence or invalid TSBPD ordering
	if upperSeq == lowerSeq {
		return lowerTsbpd
	}

	// TSBPD Monotonicity Guard: If upper TSBPD is not greater than lower,
	// something is wrong (clock jump, bug). Return lower as safe fallback.
	if upperTsbpd <= lowerTsbpd {
		return lowerTsbpd
	}

	// Linear interpolation using circular sequence arithmetic:
	// TSBPD_missing = lowerTsbpd + (missingSeq - lowerSeq) * (upperTsbpd - lowerTsbpd) / (upperSeq - lowerSeq)
	seqRange := uint64(circular.SeqSub(upperSeq, lowerSeq))
	tsbpdRange := upperTsbpd - lowerTsbpd
	seqOffset := uint64(circular.SeqSub(missingSeq, lowerSeq))

	// Avoid division by zero (should not happen given edge case check above)
	if seqRange == 0 {
		return lowerTsbpd
	}

	estimated := lowerTsbpd + (seqOffset * tsbpdRange / seqRange)

	// Final monotonicity guard: ensure we never return less than lowerTsbpd
	// This protects against any arithmetic edge cases (overflow, etc.)
	if estimated < lowerTsbpd {
		return lowerTsbpd
	}

	return estimated
}
```

### Step 4.2: Add Inter-Packet Interval Tracking

**File:** `congestion/live/receive/push.go`

#### Step 4.2a: Add Named Constants

**Location:** At the top of the file, after imports, add a const block:

```go
const (
	// InterPacketIntervalMinUs is the minimum valid inter-packet interval (10µs).
	// Intervals shorter than this are likely measurement errors or high-speed bursts.
	// 10µs corresponds to theoretical max of 100,000 packets/second.
	InterPacketIntervalMinUs = 10

	// InterPacketIntervalMaxUs is the maximum valid inter-packet interval (100ms).
	// Intervals longer than this indicate pauses (network outage, scheduling, etc.)
	// that should not pollute the EWMA calculation.
	InterPacketIntervalMaxUs = 100_000

	// InterPacketEWMAOld is the weight for the old value in EWMA calculation (87.5%).
	// Using same formula as avgPayloadSize for consistency across codebase.
	InterPacketEWMAOld = 0.875

	// InterPacketEWMANew is the weight for the new value in EWMA calculation (12.5%).
	InterPacketEWMANew = 0.125

	// InterPacketIntervalDefaultUs is the default interval when no measurement available (1ms).
	// Corresponds to ~1000 packets/second, conservative for typical 5Mbps+ streams.
	InterPacketIntervalDefaultUs = 1000
)
```

#### Step 4.2b: Add updateInterPacketInterval Function

**Location:** In `push.go`, after the const block:

```go
// updateInterPacketInterval tracks the inter-packet arrival interval using EWMA.
// This is used as a fallback for TSBPD estimation when linear interpolation
// is not possible (e.g., gap at start of buffer, single packet).
//
// The function is extracted for testability - allows unit testing of the EWMA
// logic without needing full receiver setup.
//
// See nak_btree_expiry_optimization.md Section 5.2.9 for design rationale.
//
// Parameters:
//   - nowUs: Current time in microseconds
//   - lastArrivalUs: Previous packet arrival time (0 if first packet)
//   - oldInterval: Current EWMA value (0 if uninitialized)
//
// Returns:
//   - newInterval: Updated EWMA value (0 if measurement invalid)
//   - valid: Whether the measurement was valid and interval was updated
func updateInterPacketInterval(nowUs, lastArrivalUs, oldInterval uint64) (newInterval uint64, valid bool) {
	// Need a previous arrival time to calculate interval
	if lastArrivalUs == 0 || nowUs <= lastArrivalUs {
		return 0, false
	}

	intervalUs := nowUs - lastArrivalUs

	// Clamp to valid range to filter outliers
	if intervalUs < InterPacketIntervalMinUs || intervalUs > InterPacketIntervalMaxUs {
		return 0, false
	}

	// First measurement: use directly
	if oldInterval == 0 {
		return intervalUs, true
	}

	// EWMA update: 87.5% old + 12.5% new
	newInterval = uint64(float64(oldInterval)*InterPacketEWMAOld + float64(intervalUs)*InterPacketEWMANew)
	return newInterval, true
}
```

#### Step 4.2c: Update pushLocked to Use New Function

**Location:** In `pushLocked()` function, after FastNAK tracking (lines 137-139)

**Current:**
```go
	// Update FastNAK tracking (after packet is accepted)
	r.lastPacketArrivalTime.Store(now)
	r.lastDataPacketSeq.Store(seq)

	// NOTE: No gap detection, no immediate NAK, no maxSeenSequenceNumber tracking
```

**Replace with:**
```go
	// Update FastNAK tracking (after packet is accepted)
	r.lastPacketArrivalTime.Store(now)
	r.lastDataPacketSeq.Store(seq)

	// Track inter-packet interval for TSBPD estimation fallback
	// Also tracks sample count for EWMA warm-up (Section 4.6)
	nowUs := uint64(now.UnixMicro())
	lastArrivalUs := r.lastPacketArrivalUs.Swap(nowUs)
	if newInterval, valid := updateInterPacketInterval(nowUs, lastArrivalUs, r.avgInterPacketIntervalUs.Load()); valid {
		r.avgInterPacketIntervalUs.Store(newInterval)
		// Increment sample count for warm-up tracking (saturate to avoid overflow)
		count := r.interPacketSampleCount.Load()
		if count < math.MaxUint32 {
			r.interPacketSampleCount.Add(1)
		}
	}

	// NOTE: No gap detection, no immediate NAK, no maxSeenSequenceNumber tracking
```

**Also update** `pushLockedBtree()` if it exists, or the ring drain path if that's
where packets are actually processed in event loop mode.

#### Step 4.2d: Unit Test for updateInterPacketInterval

**File:** `congestion/live/receive/push_test.go` (NEW or add to existing)

```go
func TestUpdateInterPacketInterval(t *testing.T) {
	tests := []struct {
		name          string
		nowUs         uint64
		lastArrivalUs uint64
		oldInterval   uint64
		wantInterval  uint64
		wantValid     bool
	}{
		{
			name:          "first_measurement",
			nowUs:         1_001_000,
			lastArrivalUs: 1_000_000,
			oldInterval:   0,
			wantInterval:  1000, // 1ms
			wantValid:     true,
		},
		{
			name:          "ewma_update",
			nowUs:         1_002_000,
			lastArrivalUs: 1_001_000,
			oldInterval:   1000,
			wantInterval:  1000, // (1000*0.875 + 1000*0.125) = 1000
			wantValid:     true,
		},
		{
			name:          "interval_too_short",
			nowUs:         1_000_005,
			lastArrivalUs: 1_000_000,
			oldInterval:   1000,
			wantInterval:  0, // 5µs < 10µs minimum
			wantValid:     false,
		},
		{
			name:          "interval_too_long",
			nowUs:         1_200_000,
			lastArrivalUs: 1_000_000,
			oldInterval:   1000,
			wantInterval:  0, // 200ms > 100ms maximum
			wantValid:     false,
		},
		{
			name:          "no_previous_arrival",
			nowUs:         1_000_000,
			lastArrivalUs: 0,
			oldInterval:   1000,
			wantInterval:  0,
			wantValid:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			newInterval, valid := updateInterPacketInterval(tc.nowUs, tc.lastArrivalUs, tc.oldInterval)
			require.Equal(t, tc.wantValid, valid)
			if valid {
				require.Equal(t, tc.wantInterval, newInterval)
			}
		})
	}
}
```

### Step 4.3: Add EWMA Warm-up Check

**File:** `congestion/live/receive/nak.go`

**Location:** After `estimateTsbpdForSeq()` (around line 45)

```go
// isEWMAWarm returns true if enough inter-packet samples have been collected
// for the EWMA to be considered reliable.
//
// See nak_btree_expiry_optimization.md Section 4.6 for warm-up strategy.
func (r *receiver) isEWMAWarm() bool {
	// Threshold of 0 means warm-up check is disabled (always warm)
	if r.ewmaWarmupThreshold == 0 {
		return true
	}
	return r.interPacketSampleCount.Load() >= r.ewmaWarmupThreshold
}
```

### Step 4.4: Add Fallback Estimation with Warm-up

**File:** `congestion/live/receive/nak.go`

**Location:** After `isEWMAWarm()`

```go
// estimateTsbpdFallback uses inter-packet interval when linear interpolation not possible.
// This handles edge cases where we don't have both boundary packets:
//   - Gap at start of packet buffer (no lower boundary)
//   - Single packet in buffer
//
// During warm-up (EWMA not yet reliable), uses conservative tsbpdDelay estimate.
// See nak_btree_expiry_optimization.md Section 4.6 for warm-up strategy.
//
// Returns estimated TSBPD for missingSeq based on reference packet.
// Includes TSBPD monotonicity guard: result is guaranteed >= refTsbpd for forward gaps.
func (r *receiver) estimateTsbpdFallback(missingSeq uint32, refSeq uint32, refTsbpd uint64) uint64 {
	// During warm-up, use conservative estimate (tsbpdDelay as worst-case)
	// This may slightly over-NAK but won't miss recovery opportunities
	if !r.isEWMAWarm() {
		// Track cold fallback for metrics/debugging
		if r.metrics != nil {
			r.metrics.NakTsbpdEstColdFallback.Add(1)
		}
		// Conservative: assume refTsbpd + full tsbpdDelay per packet
		// This over-estimates TSBPD, meaning we'll expire NAKs later (safer)
		return refTsbpd + r.tsbpdDelay
	}

	// EWMA is warm - use it
	intervalUs := r.avgInterPacketIntervalUs.Load()
	if intervalUs == 0 {
		// Edge case: warm but no interval (shouldn't happen, but handle it)
		intervalUs = InterPacketIntervalDefaultUs
	}

	// Calculate signed sequence difference for forward/backward estimation
	seqDiff := int64(circular.SeqSub(missingSeq, refSeq))

	// Estimate TSBPD: ref + (seqDiff * interval)
	estimated := uint64(int64(refTsbpd) + seqDiff*int64(intervalUs))

	// TSBPD Monotonicity Guard for forward gaps
	if seqDiff > 0 && estimated < refTsbpd {
		return refTsbpd
	}

	return estimated
}
```

### Step 4.5: Verification

```bash
go build ./congestion/...
go test ./congestion/live/receive/... -run TestEstimate -v
go test ./congestion/live/receive/... -run TestEWMAWarm -v
```

---

## Phase 5: Gap Scan Integration

### Step 5.1: Update gapScan to Calculate TSBPD

**File:** `congestion/live/receive/nak.go`

This phase requires modifications to track boundary packet TSBPD times during gap detection,
then use linear interpolation to estimate TSBPD for missing sequences.

#### Step 5.1a: Add Boundary Tracking Variables

**Location:** In `periodicNakBtree()`, after `gapsPtr := &gaps` (around line 242)

Add new tracking variables:

```go
	gapsPtr := &gaps

	// TSBPD tracking for gap estimation (NAK Btree Expiry Optimization)
	// Track boundary packets' TSBPD for linear interpolation of missing sequences
	var gapBoundaries []struct {
		gapStart       uint32   // First missing seq in this gap
		gapEnd         uint32   // Last missing seq in this gap
		lowerSeq       uint32   // Packet before gap
		lowerTsbpd     uint64   // TSBPD of packet before gap
		upperSeq       uint32   // Packet after gap
		upperTsbpd     uint64   // TSBPD of packet after gap
	}
```

#### Step 5.1b: Track Previous Packet in scanPacket Closure

**Location:** In `scanPacket` closure (around line 329), add tracking for previous packet:

Add before the closure definition:
```go
	var prevSeq uint32
	var prevTsbpd uint64
	var havePrevPacket bool
```

Inside the closure, after gap detection (around line 372), add boundary tracking:

```go
			for circular.SeqLess(seq, endSeq) || seq == endSeq {
				*gapsPtr = append(*gapsPtr, seq)
				seq = circular.SeqAdd(seq, 1)
			}

			// Track boundary for this gap (NAK Btree Expiry Optimization)
			if havePrevPacket {
				gapBoundaries = append(gapBoundaries, struct {
					gapStart, gapEnd       uint32
					lowerSeq               uint32
					lowerTsbpd             uint64
					upperSeq               uint32
					upperTsbpd             uint64
				}{
					gapStart:   gapStart,
					gapEnd:     endSeq,
					lowerSeq:   prevSeq,
					lowerTsbpd: prevTsbpd,
					upperSeq:   actualSeqNum.Val(),
					upperTsbpd: h.PktTsbpdTime,
				})
			}
		}

		// Track this packet as previous for next iteration
		havePrevPacket = true
		prevSeq = actualSeqNum.Val()
		prevTsbpd = h.PktTsbpdTime
```

#### Step 5.1c: Calculate TSBPD for Gap Entries (Optimized)

##### Performance Analysis

The TSBPD calculation for gap entries needs to be efficient because:
- Large gaps (100+ packets in Starlink outages) are common
- This runs on every periodic NAK scan (every 20ms by default)
- The calculation is on the hot path

**Original Approach (Per-Sequence Interpolation):**
```
For each gap entry:
  1. Find boundary (O(1) amortized with boundaryIdx tracking)
  2. Call estimateTsbpdForSeq() - involves:
     - 3 uint64 subtractions
     - 1 multiplication
     - 1 division
     - Circular arithmetic helper calls
  Total: ~10-15 operations per entry
```

**Optimized Approach (Batch Increment):**
```
For each gap boundary:
  1. Calculate interval once: (upperTsbpd - lowerTsbpd) / (upperSeq - lowerSeq)
  2. Set baseTsbpd = lowerTsbpd
  3. For each seq in gap: tsbpd = baseTsbpd; baseTsbpd += interval
  Total: 1 division + N additions (N = gap size)
```

**Performance Comparison (100-packet gap):**
| Approach | Operations | Estimated CPU Cycles |
|----------|------------|---------------------|
| Per-Sequence | 1000-1500 | ~3000-4500 |
| Batch Increment | ~105 | ~300-400 |
| **Speedup** | | **~10x** |

##### Call Flow Diagram

```
periodicNakBtree()
    │
    ├─► gapScan() [with lock]
    │       │
    │       ├─► IterateFrom(startSeq)
    │       │       │
    │       │       └─► scanPacket(pkt) [for each packet]
    │       │               │
    │       │               ├─► Detect gap (expected vs actual)
    │       │               ├─► Append to gapsPtr
    │       │               └─► Track boundary (prevSeq, prevTsbpd, actualSeq, actualTsbpd)
    │       │
    │       └─► Return gaps[], boundaries[]
    │
    ├─► calculateBatchTsbpd() [NEW - no lock needed]
    │       │
    │       ├─► For each boundary:
    │       │       ├─► interval = (upper - lower) / seqRange  [1 division]
    │       │       └─► For each seq: tsbpd += interval        [N additions]
    │       │
    │       └─► Update metrics: BoundaryEstimation vs EWMAEstimation
    │
    ├─► preFilterExpired() [if expiryThreshold > 0]
    │       │
    │       └─► Remove entries where tsbpd < threshold
    │
    └─► nakInsertBatchWithTsbpd() [with btree lock]
```

##### Implementation

**Location:** Replace gap insertion code (lines 464-471)

**Current:**
```go
	// Batch insert all gaps with single lock acquisition
	if len(*gapsPtr) > 0 {
		inserted := r.nakInsertBatch(*gapsPtr)
		if m != nil {
			m.NakBtreeInserts.Add(uint64(inserted))
			m.NakBtreeScanGaps.Add(uint64(len(*gapsPtr)))
		}
	}
```

**Replace with optimized batch calculation:**
```go
	// Batch insert all gaps with TSBPD estimation (NAK Btree Expiry Optimization)
	// Uses optimized batch increment approach - calculate interval once per boundary,
	// then increment for each sequence. ~10x faster than per-sequence interpolation.
	if len(*gapsPtr) > 0 {
		tsbpdTimes := make([]uint64, len(*gapsPtr))
		gapIdx := 0 // Current position in gapsPtr

		// Track estimation method usage for metrics
		boundaryEstCount := uint64(0)
		ewmaEstCount := uint64(0)

		// Process each boundary's gaps with batch increment
		for _, b := range gapBoundaries {
			// Calculate interval once for this boundary
			seqRange := uint64(circular.SeqSub(b.upperSeq, b.lowerSeq))
			var intervalUs uint64
			if seqRange > 0 && b.upperTsbpd > b.lowerTsbpd {
				intervalUs = (b.upperTsbpd - b.lowerTsbpd) / seqRange
			} else {
				// Fallback: use EWMA interval
				intervalUs = r.avgInterPacketIntervalUs.Load()
				if intervalUs == 0 {
					intervalUs = InterPacketIntervalDefaultUs
				}
			}

			// Calculate TSBPD for each sequence in this gap using increment
			// Start from lower boundary and add interval for each step
			baseTsbpd := b.lowerTsbpd + intervalUs // First missing seq is lowerSeq + 1

			for gapIdx < len(*gapsPtr) {
				seq := (*gapsPtr)[gapIdx]

				// Check if this seq belongs to current boundary
				if circular.SeqGreater(seq, b.gapEnd) {
					break // Move to next boundary
				}

				// Assign TSBPD using batch increment
				tsbpdTimes[gapIdx] = baseTsbpd
				baseTsbpd += intervalUs
				gapIdx++
				boundaryEstCount++
			}
		}

		// Handle any remaining gaps without boundaries (edge case)
		// Use EWMA fallback estimation
		if gapIdx < len(*gapsPtr) {
			intervalUs := r.avgInterPacketIntervalUs.Load()
			if intervalUs == 0 {
				intervalUs = InterPacketIntervalDefaultUs
			}
			// Use current time + TSBPD delay as base
			baseTsbpd := r.nowFn() + r.tsbpdDelay
			for ; gapIdx < len(*gapsPtr); gapIdx++ {
				tsbpdTimes[gapIdx] = baseTsbpd
				baseTsbpd += intervalUs
				ewmaEstCount++
			}
		}

		// Update estimation method metrics
		if m != nil {
			m.NakTsbpdEstBoundary.Add(boundaryEstCount)
			m.NakTsbpdEstEWMA.Add(ewmaEstCount)
		}

		inserted := r.nakInsertBatchWithTsbpd(*gapsPtr, tsbpdTimes)
		if m != nil {
			m.NakBtreeInserts.Add(uint64(inserted))
			m.NakBtreeScanGaps.Add(uint64(len(*gapsPtr)))
		}
	}
```

##### Why This Optimization Works

1. **Division is expensive**: CPU division takes 20-100+ cycles vs 1-3 for addition
2. **Boundary boundaries are few**: Even with 100+ missing packets, typically 1-3 gaps
3. **TSBPD is linear**: Within a gap, packets were sent at regular intervals
4. **Incremental calculation preserves accuracy**: Same result as interpolation

### Step 5.2: Add Pre-filter for Expired Entries

**File:** `congestion/live/receive/nak.go`

**Location:** In the gap insertion code (step 5.1c), after TSBPD calculation loop,
before `nakInsertBatchWithTsbpd()` call.

This optimization is critical for large outages (Starlink scenarios) where many
packets are already unrecoverable at detection time. Without this filter, we would:
1. Insert thousands of NAK entries
2. Immediately expire them in `expireNakEntries()`
3. Waste memory and CPU

**Insert this code after TSBPD calculation, before insertion:**

```go
		// Pre-filter expired entries (optimization for large outages)
		// See nak_btree_expiry_optimization.md Section 4.5.4.1
		// If TSBPD < threshold, the packet is already unrecoverable - don't insert
		nowUs := r.nowFn()
		expiryThreshold := r.calculateExpiryThreshold(nowUs)

		if expiryThreshold > 0 && len(*gapsPtr) > 0 {
			// Filter out already-expired entries before insertion
			validCount := 0
			skippedCount := uint64(0)
			for i := range *gapsPtr {
				if tsbpdTimes[i] >= expiryThreshold {
					// Keep this entry
					if validCount != i {
						(*gapsPtr)[validCount] = (*gapsPtr)[i]
						tsbpdTimes[validCount] = tsbpdTimes[i]
					}
					validCount++
				} else {
					// Already expired - don't insert
					skippedCount++
				}
			}
			*gapsPtr = (*gapsPtr)[:validCount]
			tsbpdTimes = tsbpdTimes[:validCount]

			// Update metric for skipped entries
			if m != nil && skippedCount > 0 {
				m.NakBtreeSkippedExpired.Add(skippedCount)
			}
		}

		// Now insert the filtered gaps
		if len(*gapsPtr) > 0 {
			inserted := r.nakInsertBatchWithTsbpd(*gapsPtr, tsbpdTimes)
			if m != nil {
				m.NakBtreeInserts.Add(uint64(inserted))
				m.NakBtreeScanGaps.Add(uint64(len(*gapsPtr)))
			}
		}
```

**Note:** The pre-filter is in-place to avoid allocation. The loop maintains
`validCount` as the write index, copying valid entries forward.

### Step 5.3: Handle Edge Cases

**File:** `congestion/live/receive/nak.go`

#### Step 5.3a: Gap at Start (No Lower Boundary)

When the first packet scanned already shows a gap from `contiguousPoint`,
we don't have a lower boundary packet. Use EWMA fallback:

```go
			// No lower boundary: use EWMA fallback from upper packet
			if !havePrevPacket && len(gapBoundaries) == 0 {
				// This is the first gap, starting from contiguousPoint
				// Use fallback estimation from the upper boundary (first packet found)
				for j, seq := range *gapsPtr {
					if circular.SeqLess(seq, actualSeqNum.Val()) {
						tsbpdTimes[j] = r.estimateTsbpdFallback(seq, actualSeqNum.Val(), h.PktTsbpdTime)
					}
				}
			}
```

#### Step 5.3b: Single Packet in Buffer

Handle the rare case where there's only one packet in the buffer:

```go
			// Single packet case: all gaps before or after use fallback
			if r.packetStore.Len() == 1 {
				refPkt := r.packetStore.Min()
				if refPkt != nil {
					refSeq := refPkt.Header().PacketSequenceNumber.Val()
					refTsbpd := refPkt.Header().PktTsbpdTime
					for j, seq := range *gapsPtr {
						tsbpdTimes[j] = r.estimateTsbpdFallback(seq, refSeq, refTsbpd)
					}
				}
			}
```

### Step 5.4: Verification

```bash
# Build
go build ./congestion/...

# Run unit tests
go test ./congestion/live/receive/... -run TestGapScan -v
go test ./congestion/live/receive/... -run TestTsbpdEstimation -v

# Run with race detector
go test ./congestion/live/receive/... -race -run TestGapScan
```

---

## Phase 6: Expiry Logic

### Step 6.1: Add calculateExpiryThreshold

**File:** `congestion/live/receive/nak.go`

**Location:** Before `expireNakEntries()` (around line 505)

```go
// calculateExpiryThreshold computes the TSBPD threshold for NAK entry expiry.
// Entries with TSBPD < threshold are expired (no time for retransmit to arrive).
//
// Formula: threshold = now + (RTO * (1 + nakExpiryMargin))
//
// This follows the same percentage-based pattern as ExtraRTTMargin in
// rto_suppression_implementation.md for consistency.
//
// See nak_btree_expiry_optimization.md Section 5.2.5 for design.
//
// Examples at RTO=15ms:
//   nakExpiryMargin=0.00: threshold = now + 15.0ms (baseline)
//   nakExpiryMargin=0.05: threshold = now + 15.75ms (5% conservative)
//   nakExpiryMargin=0.10: threshold = now + 16.5ms (10% conservative, default)
//
// Returns 0 if RTT not yet available (caller should fall back to sequence-based expiry).
func (r *receiver) calculateExpiryThreshold(nowUs uint64) uint64 {
	rtoUs := r.getRTOUs()
	if rtoUs == 0 {
		return 0 // RTT not yet available - use fallback
	}

	// Apply percentage-based nakExpiryMargin: RTO * (1 + nakExpiryMargin)
	adjustedRtoUs := uint64(float64(rtoUs) * (1.0 + r.nakExpiryMargin))

	return nowUs + adjustedRtoUs
}
```

### Step 6.2: Modify expireNakEntries

**File:** `congestion/live/receive/nak.go`

**Location:** Lines 509-544

**Replace the entire function:**

```go
// expireNakEntries removes entries from the NAK btree that are too old to be useful.
// Uses time-based expiry (FR-19 optimization) when RTT is available:
//   - An entry is expired if: now + RTT > entry.TsbpdTimeUs
//   - This means even if we send NAK now, retransmit can't arrive before TSBPD
//
// Falls back to sequence-based expiry if RTT not available.
//
// See nak_btree_expiry_optimization.md Section 6.2 for design.
//
// This is called in Tick() AFTER sendNAK to keep it out of the hot path.
func (r *receiver) expireNakEntries() int {
	if r.nakBtree == nil {
		if r.metrics != nil {
			r.metrics.NakBtreeNilWhenEnabled.Add(1)
		}
		return 0
	}

	nowUs := r.nowFn()
	expiryThreshold := r.calculateExpiryThreshold(nowUs)

	// Try time-based expiry first (preferred)
	if expiryThreshold > 0 {
		expired := r.nakDeleteBeforeTsbpd(expiryThreshold)
		if expired > 0 && r.metrics != nil {
			r.metrics.NakBtreeExpiredEarly.Add(uint64(expired))
		}
		return expired
	}

	// Fallback: sequence-based expiry (RTT not yet available)
	// Find the oldest packet in the packet btree (brief lock)
	r.lock.RLock()
	minPkt := r.packetStore.Min()
	var cutoff uint32
	if minPkt != nil {
		cutoff = minPkt.Header().PacketSequenceNumber.Val()
	}
	r.lock.RUnlock()

	if minPkt == nil {
		return 0 // Empty packet store, nothing to expire
	}

	// Any NAK entry older than the oldest packet's sequence is expired
	expired := r.nakDeleteBefore(cutoff)
	if expired > 0 && r.metrics != nil {
		r.metrics.NakBtreeExpired.Add(uint64(expired))
	}

	return expired
}
```

### Step 6.3: Verification

```bash
go build ./congestion/...
go test ./congestion/live/receive/... -run TestExpire -v
```

---

## Phase 7: Metrics

### Step 7.1: Add Metric Fields

**File:** `metrics/metrics.go`

**Location:** After `NakBtreeExpired` (line 251), in "NAK btree metrics - Core operations" section

**Current:**
```go
	// NAK btree metrics - Core operations
	NakBtreeInserts     atomic.Uint64 // Sequences added to NAK btree
	NakBtreeDeletes     atomic.Uint64 // Sequences removed (packet arrived)
	NakBtreeExpired     atomic.Uint64 // Sequences removed (TSBPD expired)
```

**Replace with:**
```go
	// NAK btree metrics - Core operations
	NakBtreeInserts        atomic.Uint64 // Sequences added to NAK btree
	NakBtreeDeletes        atomic.Uint64 // Sequences removed (packet arrived)
	NakBtreeExpired        atomic.Uint64 // Sequences removed (sequence-based expiry, fallback)
	NakBtreeExpiredEarly   atomic.Uint64 // Sequences removed RTT before TSBPD (time-based)
	NakBtreeSkippedExpired atomic.Uint64 // Sequences not inserted (already expired at gap detection)

	// NAK TSBPD estimation metrics - Tracks which estimation method was used
	// High BoundaryEst + low EWMA = good (accurate linear interpolation)
	// High EWMA = many edge cases (gaps at start, single packet buffer)
	NakTsbpdEstBoundary    atomic.Uint64 // Entries estimated using boundary packets (linear interpolation)
	NakTsbpdEstEWMA        atomic.Uint64 // Entries estimated using inter-packet EWMA (warm fallback)
	NakTsbpdEstColdFallback atomic.Uint64 // Entries estimated during EWMA warm-up (conservative)
```

### Step 7.2: Export Metrics

**File:** `metrics/handler.go`

**Location:** Find existing `NakBtreeExpired` export and add new metrics after it.

```bash
grep -n "NakBtreeExpired" metrics/handler.go
```

Add after the existing `NakBtreeExpired` export:

```go
writeCounterIfNonZero(b, "gosrt_nak_btree_expired_total",
	metrics.NakBtreeExpired.Load(),
	"socket_id", socketIdStr, "instance", instanceName)
writeCounterIfNonZero(b, "gosrt_nak_btree_expired_early_total",
	metrics.NakBtreeExpiredEarly.Load(),
	"socket_id", socketIdStr, "instance", instanceName)
writeCounterIfNonZero(b, "gosrt_nak_btree_skipped_expired_total",
	metrics.NakBtreeSkippedExpired.Load(),
	"socket_id", socketIdStr, "instance", instanceName)

// TSBPD estimation method metrics
writeCounterIfNonZero(b, "gosrt_nak_tsbpd_est_boundary_total",
	metrics.NakTsbpdEstBoundary.Load(),
	"socket_id", socketIdStr, "instance", instanceName)
writeCounterIfNonZero(b, "gosrt_nak_tsbpd_est_ewma_total",
	metrics.NakTsbpdEstEWMA.Load(),
	"socket_id", socketIdStr, "instance", instanceName)
writeCounterIfNonZero(b, "gosrt_nak_tsbpd_est_cold_fallback_total",
	metrics.NakTsbpdEstColdFallback.Load(),
	"socket_id", socketIdStr, "instance", instanceName)
```

### Step 7.3: Add Handler Test

**File:** `metrics/handler_test.go`

Add test case for new metrics:

```go
func TestHandlerNewExpiryMetrics(t *testing.T) {
	metrics := &Metrics{}
	metrics.NakBtreeExpiredEarly.Store(100)
	metrics.NakBtreeSkippedExpired.Store(50)
	metrics.NakTsbpdEstBoundary.Store(1000)
	metrics.NakTsbpdEstEWMA.Store(10)
	metrics.NakTsbpdEstColdFallback.Store(5)

	handler := NewHandler()
	handler.Register(metrics, "test-socket", "test-instance")

	// Exercise handler
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	require.Contains(t, body, "gosrt_nak_btree_expired_early_total")
	require.Contains(t, body, "gosrt_nak_btree_skipped_expired_total")
	require.Contains(t, body, "gosrt_nak_tsbpd_est_boundary_total")
	require.Contains(t, body, "gosrt_nak_tsbpd_est_ewma_total")
	require.Contains(t, body, "gosrt_nak_tsbpd_est_cold_fallback_total")
}
```

### Step 7.4: Metrics Summary Table

| Metric | Description | Expected Values |
|--------|-------------|-----------------|
| `gosrt_nak_btree_expired_total` | Sequence-based expiry (fallback) | Low after RTT established |
| `gosrt_nak_btree_expired_early_total` | Time-based RTT-aware expiry | Primary counter after RTT |
| `gosrt_nak_btree_skipped_expired_total` | Pre-filtered at detection | Spikes during outages |
| `gosrt_nak_tsbpd_est_boundary_total` | Linear interpolation used | Should be ~99% of estimates |
| `gosrt_nak_tsbpd_est_ewma_total` | EWMA fallback used (warm) | Low in normal operation |
| `gosrt_nak_tsbpd_est_cold_fallback_total` | Conservative fallback (cold) | Only during startup |
| `gosrt_nak_btree_expired_total` | Sequence-based expiry (fallback) | Low after RTT established |
| `gosrt_nak_btree_expired_early_total` | Time-based RTT-aware expiry | Primary counter after RTT |
| `gosrt_nak_btree_skipped_expired_total` | Pre-filtered at detection | Spikes during outages |
| `gosrt_nak_tsbpd_est_boundary_total` | Linear interpolation used | Should be ~99% of estimates |
| `gosrt_nak_tsbpd_est_ewma_total` | EWMA fallback used | Low in normal operation |

**Operational Insights:**
- `boundary >> ewma`: Normal operation, accurate estimation
- `ewma >> boundary`: Many edge cases, investigate gap patterns
- `skipped_expired` high: Large outages detected, pre-filter working
- `expired_early` increasing: Time-based expiry reducing phantom NAKs

### Step 7.5: Verification

```bash
go build ./metrics/...
go test ./metrics/... -v -run TestHandler
make audit-metrics
```

---

## Phase 8: Unit Tests

### Step 8.1: NakEntryWithTime Tests

**File:** `congestion/live/receive/nak_btree_test.go`

Add test for new TsbpdTimeUs field:

```go
func TestNakEntryWithTime_TsbpdField(t *testing.T) {
	entry := NakEntryWithTime{
		Seq:           100,
		TsbpdTimeUs:   5_000_000,
		LastNakedAtUs: 1_000_000,
		NakCount:      2,
	}

	require.Equal(t, uint32(100), entry.Seq)
	require.Equal(t, uint64(5_000_000), entry.TsbpdTimeUs)
	require.Equal(t, uint64(1_000_000), entry.LastNakedAtUs)
	require.Equal(t, uint32(2), entry.NakCount)
}
```

### Step 8.2: DeleteBeforeTsbpd Tests

**File:** `congestion/live/receive/nak_btree_test.go`

Add comprehensive table-driven tests (from design document Section 7.4):

```go
func TestDeleteBeforeTsbpd(t *testing.T) {
	tests := []struct {
		name              string
		entries           []NakEntryWithTime
		nowUs             uint64
		rtoUs             uint64
		nakExpiryMargin   float64
		wantExpired       int
		wantRemaining     []uint32
	}{
		{
			name: "no_entries_expired_when_all_recent",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 1_020_000},
				{Seq: 101, TsbpdTimeUs: 1_021_000},
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     0,
			wantRemaining:   []uint32{100, 101},
		},
		{
			name: "oldest_entries_expired",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 1_010_000},
				{Seq: 101, TsbpdTimeUs: 1_011_000},
				{Seq: 102, TsbpdTimeUs: 1_020_000},
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     2,
			wantRemaining:   []uint32{102},
		},
		{
			name: "nakExpiryMargin_protects_borderline_entry",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 1_017_000},
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10, // threshold = 1s + 16.5ms = 1.0165s
			wantExpired:     0,    // 1.017s > 1.0165s → kept
			wantRemaining:   []uint32{100},
		},
		{
			name: "empty_tree",
			entries:         []NakEntryWithTime{},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     0,
			wantRemaining:   []uint32{},
		},
		{
			name: "zero_rto_fallback",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 1_010_000},
			},
			nowUs:           1_000_000,
			rtoUs:           0, // RTT not yet measured
			nakExpiryMargin: 0.10,
			wantExpired:     0, // Should not expire with time-based (falls back)
			wantRemaining:   []uint32{100},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nb := newNakBtree(32)
			for _, e := range tc.entries {
				nb.InsertWithTsbpd(e.Seq, e.TsbpdTimeUs)
			}

			expiryThreshold := tc.nowUs + uint64(float64(tc.rtoUs)*(1.0+tc.nakExpiryMargin))
			expired := nb.DeleteBeforeTsbpd(expiryThreshold)

			require.Equal(t, tc.wantExpired, expired)
			require.Equal(t, len(tc.wantRemaining), nb.Len())

			// Verify remaining entries
			remaining := make([]uint32, 0)
			nb.Iterate(func(e NakEntryWithTime) bool {
				remaining = append(remaining, e.Seq)
				return true
			})
			require.Equal(t, tc.wantRemaining, remaining)
		})
	}
}
```

### Step 8.3: TSBPD Estimation Tests

**File:** `congestion/live/receive/nak_tsbpd_estimation_test.go` (NEW FILE)

```go
package receive

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateTsbpdForSeq(t *testing.T) {
	tests := []struct {
		name       string
		missingSeq uint32
		lowerSeq   uint32
		lowerTsbpd uint64
		upperSeq   uint32
		upperTsbpd uint64
		wantTsbpd  uint64
	}{
		{
			name:       "mid_point_interpolation",
			missingSeq: 105,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   110,
			upperTsbpd: 1_010_000,
			wantTsbpd:  1_005_000, // (105-100)/(110-100) * 10ms = 5ms
		},
		{
			name:       "single_packet_gap",
			missingSeq: 101,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   102,
			upperTsbpd: 1_002_000,
			wantTsbpd:  1_001_000,
		},
		{
			name:       "same_boundary_returns_lower",
			missingSeq: 100,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   100,
			upperTsbpd: 1_000_000,
			wantTsbpd:  1_000_000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := estimateTsbpdForSeq(tc.missingSeq, tc.lowerSeq, tc.lowerTsbpd, tc.upperSeq, tc.upperTsbpd)
			require.Equal(t, tc.wantTsbpd, result)
		})
	}
}
```

### Step 8.4: Corner Case Tests

**File:** `congestion/live/receive/nak_expiry_test.go` (NEW FILE)

Corner case tests from design document Section 7.3:

```go
func TestExpiry_CornerCases(t *testing.T) {
	tests := []struct {
		name         string
		description  string
		setupFn      func(r *receiver)
		expectExpiry int
	}{
		{
			name:        "rtt_spike_during_gap",
			description: "RTT doubles during gap detection",
			setupFn: func(r *receiver) {
				// Simulate RTT spike by setting high RTO
			},
			expectExpiry: 0, // nakExpiryMargin should protect
		},
		{
			name:        "large_gap_starlink",
			description: "60ms outage causing 100+ packet gap",
			setupFn: func(r *receiver) {
				// Insert 100 entries with TSBPD spread across 60ms
			},
			expectExpiry: 0, // Most should be pre-filtered
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Test implementation
		})
	}
}
```

---

## Phase 9: Benchmark Tests

**File:** `congestion/live/receive/nak_btree_benchmark_test.go` (NEW FILE)

### Step 9.1: DeleteBeforeTsbpd Benchmarks

```go
package receive

import (
	"testing"
)

// BenchmarkDeleteBeforeTsbpd_Optimized tests the DeleteMin-based implementation
func BenchmarkDeleteBeforeTsbpd_Optimized(b *testing.B) {
	sizes := []int{10, 100, 1000, 10000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				nb := newNakBtree(32)
				baseTime := uint64(1_000_000)
				for j := 0; j < size; j++ {
					nb.InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
				}
				// Expire half the entries
				threshold := baseTime + uint64(size/2)*1000
				b.StartTimer()

				nb.DeleteBeforeTsbpd(threshold)
			}
		})
	}
}

// BenchmarkDeleteBeforeTsbpd_Slow tests the collect-then-delete implementation
func BenchmarkDeleteBeforeTsbpd_Slow(b *testing.B) {
	sizes := []int{10, 100, 1000, 10000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				nb := newNakBtree(32)
				baseTime := uint64(1_000_000)
				for j := 0; j < size; j++ {
					nb.InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
				}
				threshold := baseTime + uint64(size/2)*1000
				b.StartTimer()

				nb.DeleteBeforeTsbpdSlow(threshold)
			}
		})
	}
}

// BenchmarkInsertWithTsbpd measures insert performance with TSBPD
func BenchmarkInsertWithTsbpd(b *testing.B) {
	nb := newNakBtree(32)
	baseTime := uint64(1_000_000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nb.InsertWithTsbpd(uint32(i), baseTime+uint64(i)*1000)
	}
}

// BenchmarkInsertBatchWithTsbpd measures batch insert performance
func BenchmarkInsertBatchWithTsbpd(b *testing.B) {
	batchSizes := []int{10, 50, 100, 500}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch=%d", batchSize), func(b *testing.B) {
			seqs := make([]uint32, batchSize)
			tsbpds := make([]uint64, batchSize)
			baseTime := uint64(1_000_000)
			for j := 0; j < batchSize; j++ {
				seqs[j] = uint32(j)
				tsbpds[j] = baseTime + uint64(j)*1000
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				nb := newNakBtree(32)
				nb.InsertBatchWithTsbpd(seqs, tsbpds)
			}
		})
	}
}
```

### Step 9.2: Run Benchmarks

```bash
# Compare optimized vs slow implementation
go test ./congestion/live/receive/... -bench=DeleteBeforeTsbpd -benchmem -count=5

# Full benchmark suite
go test ./congestion/live/receive/... -bench=. -benchmem
```

**Expected results:**
- `DeleteBeforeTsbpd` (optimized) should be 2-3x faster than `DeleteBeforeTsbpdSlow`
- `InsertWithTsbpd` should have minimal overhead vs `Insert`
- Zero allocations for `DeleteBeforeTsbpd` (optimized uses DeleteMin)

---

## Phase 10: Integration Testing

### Step 10.1: Run Unit Tests

```bash
# All receiver tests with race detector
go test ./congestion/live/receive/... -race -v

# Specific expiry tests
go test ./congestion/live/receive/... -run TestExpiry -v
go test ./congestion/live/receive/... -run TestDeleteBeforeTsbpd -v
go test ./congestion/live/receive/... -run TestEstimate -v
```

### Step 10.2: Run Parallel Integration Tests

```bash
# Main test that showed elevated NAKs
make test-parallel CONFIG=Parallel-Clean-20M-FullEL-vs-FullSendEL

# Additional configurations
make test-parallel CONFIG=Parallel-Clean-50M-Ring-vs-NoRing
```

### Step 10.3: Verify Metrics

After running tests, check Prometheus metrics:

**Expected changes:**
| Metric | Before | After | Change |
|--------|--------|-------|--------|
| `nak_not_found` (sender) | ~1,794 | <100 | Significant reduction |
| `NakBtreeExpiredEarly` | 0 | >0 | New metric shows time-based expiry |
| `NakBtreeExpired` | >0 | ~0 | Falls back less often |
| `NakBtreeSkippedExpired` | 0 | ≥0 | May show pre-filter working |

**Metrics collection:**
```bash
# Collect metrics from test server
curl -s http://localhost:9091/metrics | grep -E "nak_btree|nak_not_found"
```

### Step 10.4: Network Impairment Tests

```bash
# Test with Starlink-like conditions (per integration_testing_with_network_impairment_defects.md)
make test-impaired CONFIG=Parallel-Clean-20M-FullEL-vs-FullSendEL IMPAIRMENT=starlink

# Test with high latency
make test-impaired CONFIG=Parallel-Clean-20M-FullEL-vs-FullSendEL IMPAIRMENT=high-latency
```

### Step 10.5: Configuration Sensitivity Testing

Test different `nakExpiryMargin` values:

```bash
# Conservative (less aggressive expiry)
./bin/server -nakexpirymargin 0.25 ...

# Aggressive (more expiry)
./bin/server -nakexpirymargin 0.0 ...

# Default
./bin/server -nakexpirymargin 0.10 ...
```

Monitor `nak_not_found` and `CongestionRecvPktLoss` to find optimal balance.

---

## Conclusion

### Summary of Changes

| Component | Files Modified | Key Changes |
|-----------|---------------|-------------|
| Configuration | `config.go`, `flags.go` | `NakExpiryMargin` option |
| Data Structure | `nak_btree.go` | `TsbpdTimeUs` field, new methods |
| Receiver | `receiver.go` | New fields, function dispatch |
| TSBPD Estimation | `nak.go` | `estimateTsbpdForSeq()`, fallback |
| Gap Scan | `nak.go` | Boundary tracking, pre-filter |
| Expiry Logic | `nak.go` | Time-based `expireNakEntries()` |
| Metrics | `metrics.go`, `handler.go` | 2 new counters |

### Verification Checklist

```bash
# 1. Build verification
go build ./...

# 2. Unit tests
go test ./congestion/live/receive/... -race -v

# 3. Benchmarks
go test ./congestion/live/receive/... -bench=DeleteBeforeTsbpd -benchmem

# 4. Integration tests
make test-parallel CONFIG=Parallel-Clean-20M-FullEL-vs-FullSendEL

# 5. Metrics audit
make audit-metrics
```

### Success Criteria

1. **All tests pass** - No regressions
2. **`nak_not_found` reduced** - Target: <100 from ~1,794
3. **`NakBtreeExpiredEarly` > 0** - Time-based expiry working
4. **No performance regression** - Benchmarks stable or improved
5. **Memory impact minimal** - +16 bytes/receiver, +8 bytes/NAK entry

### Rollback Plan

If issues arise after deployment:

1. Set `nakExpiryMargin = -1` to disable time-based expiry (use sequence-based)
2. Or revert the `expireNakEntries()` changes
3. The new metrics will help diagnose issues

---

## Appendix: File Modification Summary

```
config.go                                    +20 lines
contrib/common/flags.go                       +6 lines
congestion/live/receive/nak_btree.go         +80 lines
congestion/live/receive/nak.go               +60 lines (modify 40)
congestion/live/receive/receiver.go          +10 lines
congestion/live/receive/push.go              +15 lines
metrics/metrics.go                            +2 lines
metrics/handler.go                           +10 lines
congestion/live/receive/nak_btree_test.go    +100 lines
congestion/live/receive/nak_expiry_test.go   +150 lines (NEW)
congestion/live/receive/nak_benchmark_test.go +80 lines (NEW)
---------------------------------------------------------
Total:                                       ~530 lines added/modified
```

