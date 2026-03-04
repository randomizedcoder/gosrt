# GoSRT Lint Issues Resolution Plan

**Generated:** 2026-02-26
**Lint Config:** Tier 0 (Quick Lint)
**Total Issues:** 137

## Executive Summary

| Category | Count | Severity | Auto-fixable |
|----------|-------|----------|--------------|
| errcheck | 50 | Medium | No |
| unused | 33 | Low | No (requires analysis) |
| staticcheck | 28 | Medium-High | Partially |
| ineffassign | 22 | Low | No |
| gofmt | 4 | Low | Yes |

---

## 1. gofmt (4 issues) - PRIORITY: LOW, AUTO-FIXABLE

### Description
Files not properly formatted according to Go standards.

### Files Affected
- `congestion/live/receive/debug_context_stub.go:50`
- `congestion/live/receive/test_helpers_stub.go:21`
- `congestion/live/send/test_helpers_stub.go:21`
- `connection_debug_stub.go:38`

### Resolution
Run `gofmt -w` on affected files:
```bash
gofmt -w congestion/live/receive/debug_context_stub.go
gofmt -w congestion/live/receive/test_helpers_stub.go
gofmt -w congestion/live/send/test_helpers_stub.go
gofmt -w connection_debug_stub.go
```

Or use: `nix develop --command gofmt -w ./...`

---

## 2. errcheck (50 issues) - PRIORITY: HIGH

### Description
Error return values are not checked. This can hide bugs and make debugging difficult.

### Categories of errcheck Issues

#### 2.1 Unchecked `Close()` calls (14 instances)

**Pattern:** `defer x.Close()` or `x.Close()` without checking error

**Files:**
- `connection_io_uring_bench_test.go:120,121,182`
- `connection_metrics_test.go:42,46,96,128,205,209`
- `contrib/udp_echo/main.go:145`
- `metrics/proc_stat_linux.go:38`

**Resolution Strategy:**
For `defer` statements, use a helper pattern:
```go
// BEFORE (wrong)
defer f.Close()

// AFTER (correct) - Option A: Named return with deferred error check
func readFile(path string) (err error) {
    f, err := os.Open(path)
    if err != nil {
        return err
    }
    defer func() {
        if cerr := f.Close(); cerr != nil && err == nil {
            err = cerr
        }
    }()
    // ... use f
    return nil
}

// AFTER (correct) - Option B: Log close errors (acceptable for non-critical)
defer func() {
    if err := f.Close(); err != nil {
        log.Printf("failed to close file: %v", err)
    }
}()

// AFTER (correct) - Option C: For tests, use require
defer func() {
    require.NoError(t, f.Close())
}()
```

#### 2.2 Unchecked `recover()` calls (6 instances)

**Pattern:** `defer func() { recover() }()`

**Files:**
- `connection_concurrency_table_test.go:737,960`
- `connection_handlers_test.go:87,111,160,196`

**Resolution Strategy:**
The `recover()` function returns `interface{}` which should be checked:
```go
// BEFORE (wrong)
defer func() { recover() }()

// AFTER (correct) - If you need to suppress panics silently
defer func() { _ = recover() }()

// AFTER (better) - Log recovered panics
defer func() {
    if r := recover(); r != nil {
        t.Logf("recovered panic: %v", r)
    }
}()
```

#### 2.3 Unchecked `Marshal()` calls (15 instances)

**Pattern:** `p.Marshal(&buf)` without checking error

**Files:**
- `packet/packet_test.go:23,38,56,85,145,218,296,348,393,438,577,609,712,1128`

**Resolution Strategy:**
```go
// BEFORE (wrong)
p.Marshal(&buf)

// AFTER (correct) - In tests
err := p.Marshal(&buf)
require.NoError(t, err)

// AFTER (correct) - In production
if err := p.Marshal(&buf); err != nil {
    return fmt.Errorf("marshal packet: %w", err)
}
```

#### 2.4 Unchecked `Write()` calls (3 instances)

**Pattern:** `c.Write(data)` or `w.Write([]byte(...))` without checking error

**Files:**
- `connection_concurrency_table_test.go:892,919`
- `metrics/stabilization_test.go:331`

**Resolution Strategy:**
```go
// BEFORE (wrong)
c.Write(data)

// AFTER (correct)
n, err := c.Write(data)
if err != nil {
    return err
}
if n != len(data) {
    return fmt.Errorf("short write: %d/%d", n, len(data))
}
```

#### 2.5 Unchecked `UnmarshalZeroCopy()` / `UnmarshalURL()` calls (7 instances)

**Files:**
- `config_test.go:185`
- `packet/packet_test.go:926,950,1009,1022,1062,1074`

**Resolution Strategy:**
```go
// BEFORE (wrong)
p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)

// AFTER (correct)
err := p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
require.NoError(t, err)
```

#### 2.6 Unchecked `Publish()`/`Subscribe()` calls (4 instances)

**Files:**
- `connection_metrics_test.go:41,45,204,208`

**Resolution Strategy:**
```go
// BEFORE (wrong)
channel.Publish(conn)

// AFTER (correct)
err := channel.Publish(conn)
require.NoError(t, err)
```

#### 2.7 Unchecked `EncryptOrDecryptPayload()` calls (2 instances)

**Files:**
- `crypto/crypto_test.go:321,330`

**Resolution Strategy:**
```go
// BEFORE (wrong)
c.EncryptOrDecryptPayload(data, packet.EvenKeyEncrypted, packetSequenceNumber)

// AFTER (correct)
err := c.EncryptOrDecryptPayload(data, packet.EvenKeyEncrypted, packetSequenceNumber)
require.NoError(t, err)
```

#### 2.8 Unchecked `resp.Body.Close()` (2 instances)

**Files:**
- `metrics/stabilization.go:344,383`

**Resolution Strategy:**
```go
// BEFORE (wrong)
defer resp.Body.Close()

// AFTER (correct)
defer func() {
    if err := resp.Body.Close(); err != nil {
        // Log or handle - body close errors are usually non-critical
        log.Printf("failed to close response body: %v", err)
    }
}()
```

---

## 3. ineffassign (22 issues) - PRIORITY: MEDIUM

### Description
Variable is assigned but the assignment is never used. This indicates dead code or logic errors.

### Categories

#### 3.1 Unused error assignments in test setup (19 instances)

**Pattern:** `err = someOperation()` where `err` is never checked afterward

**Files:**
- `dial_test.go:128,162,169,197,204,267,303,310,360,367,390,401,426,430,431,433,451`
- `listen_test.go:182,326`

**Resolution Strategy:**
Either use the error or use blank identifier intentionally:
```go
// BEFORE (wrong) - Error assigned but never used
err = p.Marshal(&data)

// AFTER - Option A: Check the error
if err = p.Marshal(&data); err != nil {
    t.Fatalf("marshal failed: %v", err)
}

// AFTER - Option B: Explicitly ignore (if truly not needed)
_ = p.Marshal(&data)  // Error intentionally ignored: test doesn't care
```

#### 3.2 Loop variable reassignment issues (3 instances)

**Files:**
- `congestion/live/receive/tsbpd_advancement_test.go:398,417`
- `connection_lifecycle_table_test.go:274`

**Resolution Strategy:**
Review the logic - the variable is being assigned but the value is never used before being overwritten or the function returns.

---

## 4. staticcheck (28 issues) - PRIORITY: HIGH

### Categories

#### 4.1 SA4003: Impossible conditions (3 instances)

**Pattern:** Checking if unsigned value < 0

**Files:**
- `congestion/live/send/push.go:29,69` - `if p.Len() < 0`
- `congestion/live/send/sender_wraparound_table_test.go:184`

**Resolution Strategy:**
```go
// BEFORE (wrong) - uint64 can never be < 0
if p.Len() < 0 || p.Len() > maxPayloadSize {

// AFTER (correct) - Remove impossible condition
if p.Len() > maxPayloadSize {
```

#### 4.2 SA4006: Value never used (5 instances)

**Pattern:** `err = operation()` but `err` is never checked

**Files:**
- `connection_test.go:115,256,533`
- `pubsub_test.go:94,123,142,147`

**Resolution Strategy:**
Same as ineffassign - either check the error or explicitly ignore with `_`.

#### 4.3 SA4010: Append result never used (1 instance)

**File:** `tools/lock-requirements-analyzer/main.go:676`

**Resolution Strategy:**
```go
// BEFORE (wrong)
protected = append(protected, op)  // Result never used

// AFTER (correct) - Either use the result or remove the line
// If the slice should be modified:
protected = append(protected, op)
return protected  // Or use protected elsewhere

// If not needed:
// Remove the line entirely
```

#### 4.4 SA6002: sync.Pool argument should be pointer (8 instances)

**Pattern:** `pool.Put(buffer)` where buffer is `[]byte`

**Files:**
- `contrib/udp_echo/main.go:281,323,372,386,495,502,515,533`

**Resolution Strategy:**
sync.Pool expects pointer types to avoid allocations:
```go
// BEFORE (wrong) - causes allocation
s.bufferPool.Put(buffer)

// AFTER (correct) - wrap in pointer
type pooledBuffer struct {
    data []byte
}
// Then use:
s.bufferPool.Put(&pooledBuffer{data: buffer})

// Or simpler - use *[]byte in pool
buf := s.bufferPool.Get().(*[]byte)
// ... use *buf
s.bufferPool.Put(buf)  // Put back the pointer
```

#### 4.5 SA9003: Empty branch (5 instances)

**Pattern:** `if condition { }` with empty body

**Files:**
- `connection_concurrency_table_test.go:400`
- `contrib/integration_testing/metrics_collector.go:136`
- `rtt_benchmark_test.go:537`
- `server.go:240`
- `tools/metrics-audit/main.go:509`

**Resolution Strategy:**
```go
// BEFORE (wrong)
if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
    // Empty - nothing happens
}

// AFTER - Option A: Add proper error handling
if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
    log.Printf("metrics server error: %v", err)
}

// AFTER - Option B: If truly no action needed, add comment
if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
    // Intentionally empty: errors logged elsewhere
}

// AFTER - Option C: Remove the conditional if not needed
_ = s.metricsServer.ListenAndServe()
```

#### 4.6 S1009: Redundant nil check before len() (4 instances)

**Pattern:** `if x != nil && len(x) > 0`

**Files:**
- `congestion/live/receive/nak_consolidate_table_test.go:667`
- `contrib/integration_testing/parallel_comparison.go:969,974`
- `contrib/performance/reporter.go:148`

**Resolution Strategy:**
```go
// BEFORE (wrong) - nil check redundant
if list != nil && len(list) > 0 {

// AFTER (correct) - len(nil) == 0 in Go
if len(list) > 0 {
```

---

## 5. unused (33 issues) - PRIORITY: MEDIUM

### Description
Declared types, functions, fields, or variables that are never used.

### Categories

#### 5.1 Unused debug infrastructure (6 instances)

**Files:**
- `congestion/live/receive/debug_context_stub.go:16` - `type debugContext`
- `congestion/live/receive/receiver.go:206` - `field debugCtx`
- `congestion/live/send/debug_stub.go:16` - `type debugContext`
- `congestion/live/send/sender.go:240` - `field debug`
- `congestion/live/send/test_helpers_stub.go:18` - `func runInTickContext`
- `congestion/live/receive/test_helpers_stub.go` - similar

**Analysis:** These appear to be stub implementations for non-debug builds. The `debug` build tag likely provides real implementations.

**Resolution Strategy:**
Verify these are used in debug builds:
```bash
go build -tags debug ./...
```
If they ARE used in debug builds, add build constraint comments:
```go
//go:build !debug

// debugContext is a stub for non-debug builds.
// The real implementation is in debug_context.go with build tag 'debug'.
type debugContext struct{}
```

#### 5.2 Unused struct fields for future features (6 instances)

**Files:**
- `dial.go:58` - `stopReader`
- `dial.go:85,86` - `recvCompCtx`, `recvCompCancel`
- `listen.go:145` - `stopReader`
- `listen.go:174,175` - `recvCompCtx`, `recvCompCancel`

**Analysis:** These fields appear to be infrastructure for io_uring completion handling that may not be fully implemented yet.

**Resolution Strategy:**
1. If planned for future use: Add `// TODO: Used in upcoming io_uring multi-ring implementation` comment
2. If obsolete: Remove the fields
3. If used conditionally: Check if used in `_linux.go` files with build tags

#### 5.3 Unused metrics/helper functions (8 instances)

**Files:**
- `connection_linux.go:123,129,702` - increment/drain functions
- `dial_linux.go:123,129,777` - increment/drain functions
- `listen_linux.go:817,824,997` - increment/drain functions

**Analysis:** These appear to be instrumentation functions that may be called conditionally or were prepared for future metrics.

**Resolution Strategy:**
1. Search for usages: `grep -r "incrementSendPacketsProcessed" .`
2. If truly unused, decide: keep for future metrics or remove
3. If keeping, add `//nolint:unused // Prepared for metrics instrumentation`

#### 5.4 Unused test helpers (5 instances)

**Files:**
- `congestion/live/receive/hotpath_bench_test.go:98` - `generateHotPathPackets`
- `congestion/live/receive/nak_btree_scan_stream_test.go:427` - field `seed`
- `congestion/live/receive/receive_iouring_reorder_test.go:26` - `generatePackets`
- `congestion/live/receive/receive_race_test.go:34,44` - `raceTestConfig`, `defaultRaceConfig`
- `congestion/live/receive/stream_test_helpers_test.go:690` - `runNakCycles`

**Resolution Strategy:**
1. If these are for future tests: Keep with `// TODO` comment
2. If obsolete: Remove them
3. If used in skipped tests: Ensure the tests are enabled

#### 5.5 Unused contrib/tools code (5 instances)

**Files:**
- `contrib/client-seeker/generator.go:149` - `legacyActualBitrate`
- `contrib/integration_testing/network_controller.go:879` - `runScriptUnlocked`
- `contrib/integration_testing/parallel_comparison.go:1094,1182` - `sumMetricsByPrefix`, `formatDuration`
- `contrib/performance/process.go:232` - `defaultHighThroughputArgs`

**Resolution Strategy:**
Review each function:
1. If legacy/deprecated: Remove with git history preservation
2. If planned for use: Add `// TODO` comment
3. If exported for external use: Keep (though these are unexported)

#### 5.6 Unused tool field (1 instance)

**File:** `tools/metrics-lock-analyzer/main.go:686` - field `lockType`

**Resolution Strategy:**
Check if the field should be used or if it's dead code from refactoring.

#### 5.7 Unused listener function (1 instance)

**File:** `listen_io.go:129` - `sendBrokenLookup`

**Resolution Strategy:**
Search for callers; if none, determine if this is dead code or planned functionality.

---

## Recommended Resolution Order

### Phase 1: Quick Wins (< 1 hour)
1. ✅ Fix gofmt issues (4 files) - `gofmt -w`
2. ✅ Fix S1009 redundant nil checks (4 instances) - Simple removal
3. ✅ Fix SA4003 impossible conditions (3 instances) - Remove `< 0` checks on uint

### Phase 2: Test File Cleanup (2-3 hours)
1. Fix errcheck in test files - Add `require.NoError(t, err)` patterns
2. Fix ineffassign in test files - Either check errors or use `_`
3. Fix SA4006/SA9003 in test files

### Phase 3: Production Code (2-3 hours)
1. Fix errcheck in production code - Proper error handling
2. Fix SA6002 sync.Pool issues - Use pointer types
3. Review and fix unused code - Remove or document

### Phase 4: Debug Build Verification (1 hour)
1. Verify debug stubs are correct
2. Ensure debug builds compile and pass tests
3. Document the build tag system

### Phase 5: Full Lint Verification (1-2 hours)
1. Re-run Tier 0 (quick) lint - must pass with zero issues
2. Re-run Tier 1 (standard) lint - must pass with zero issues
3. Re-run Tier 2 (comprehensive) lint - must pass with zero issues
4. Run full test suite with race detection
5. Verify nix flake checks pass

**Verification Commands:**
```bash
# Step 1: Tier 0 - Quick lint (must be zero issues)
nix develop --command make lint-quick

# Step 2: Tier 1 - Standard lint (must be zero issues)
nix develop --command make lint

# Step 3: Tier 2 - Comprehensive lint (must be zero issues)
nix develop --command make lint-comprehensive

# Step 4: Full test suite with race detection
nix develop --command make test
nix develop --command make ci-race

# Step 5: Nix flake checks (CI simulation)
nix flake check --no-build
nix build .#checks.x86_64-linux.golangci-lint-quick
nix build .#checks.x86_64-linux.golangci-lint
nix build .#checks.x86_64-linux.golangci-lint-comprehensive

# Step 6: Debug build verification
nix develop --command go build -tags debug ./...
nix develop --command make test-debug
```

**Success Criteria for Phase 5:**
- [ ] `make lint-quick` exits with code 0
- [ ] `make lint` exits with code 0
- [ ] `make lint-comprehensive` exits with code 0
- [ ] `make test` passes all tests
- [ ] `make ci-race` detects no races
- [ ] All nix flake checks pass
- [ ] Debug builds compile and pass tests

---

---

## Notes

- **Do NOT disable linters** - Fix the actual issues
- **Do NOT use `//nolint` comments** unless absolutely necessary and documented
- **Preserve git history** - Use proper refactoring, not copy-paste
- **Run tests after each phase** to ensure fixes don't break functionality
