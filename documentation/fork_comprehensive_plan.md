# Comprehensive Fork Plan: randomizedcoder/gosrt

**Date**: December 29, 2025
**Status**: 🔄 IN PROGRESS
**Fork**: `github.com/randomizedcoder/gosrt` (from `datarhei/gosrt`)

---

## 🎯 Vision

Build the most reliable, well-tested SRT implementation in Go using:
1. **AST-based static analysis** to prevent bug classes
2. **Table-driven tests** for comprehensive coverage with minimal code
3. **Automated verification** integrated into CI

---

## 📊 Current State Analysis

### Code Coverage Summary (as of 2025-12-29)

| Package | Coverage | Status |
|---------|----------|--------|
| `./circular` | 88.1% | ✅ Excellent |
| `./packet` | 85.1% | ✅ Excellent |
| `./net` | 83.8% | ✅ Excellent |
| `./crypto` | 79.6% | ✅ Good |
| `./congestion/live` | 73.6% | ⚡ Core logic |
| `./metrics` | 71.3% | ✅ Good |
| `./rand` | 71.9% | ✅ Good |
| `.` (root) | 45.3% | ⚠️ Needs work |
| **TOTAL** | **40.0%** | Target: 50%+ |

### What We Have ✅

| Component | Status | Details |
|-----------|--------|---------|
| **code-audit** | ✅ Complete | Unified AST tool (seq + metrics + test) |
| **seq-audit** | ✅ Complete | Type-aware AST analysis for 31-bit wraparound |
| **test-audit** | ✅ Complete | Field classification (CODE_PARAM/TEST_INFRA) |
| **metrics-audit** | ✅ Complete | Prometheus metrics verification |
| **Table-driven tests** | ✅ 13 files | 68% code reduction achieved |
| **Integration tests** | ✅ 170 configs | Clean, network, parallel, isolation |
| **Race detection** | ✅ ci-race | Full suite with CI integration |
| **Coverage targets** | ✅ Complete | `make coverage`, `coverage-by-package`, `coverage-check` |
| **Connection lifecycle tests** | 🔄 Started | Basic tests passing |

### What's Missing / In Progress

| Component | Priority | Status | Effort |
|-----------|----------|--------|--------|
| Connection lifecycle tests | HIGH | ✅ Done (14 tests) | - |
| Handshake protocol tests | HIGH | ✅ Done (13+ tests) | - |
| Error path tests | MEDIUM | ✅ Done (42 tests) | - |
| Server/crypto table tests | MEDIUM | ⏳ Next | 0.5 day |
| AST-based test generation | LOW | ⏳ Pending | 2 days |

---

## 🏗️ Architecture: AST-Based Quality System

```
┌─────────────────────────────────────────────────────────────────┐
│                    AST Analysis Pipeline                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐      │
│  │  seq-audit   │    │ test-audit   │    │metrics-audit │      │
│  │              │    │              │    │              │      │
│  │ • uint32 ops │    │ • CODE_PARAM │    │ • Prometheus │      │
│  │ • wraparound │    │ • corners    │    │ • handler    │      │
│  │ • int32 cast │    │ • coverage   │    │ • export     │      │
│  └──────┬───────┘    └──────┬───────┘    └──────┬───────┘      │
│         │                   │                   │               │
│         └───────────────────┼───────────────────┘               │
│                             │                                   │
│                             ▼                                   │
│                    ┌──────────────┐                             │
│                    │  Unified CI  │                             │
│                    │   Pipeline   │                             │
│                    └──────────────┘                             │
│                             │                                   │
│         ┌───────────────────┼───────────────────┐               │
│         │                   │                   │               │
│         ▼                   ▼                   ▼               │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐      │
│  │ make check   │    │  make test   │    │ make ci-race │      │
│  │              │    │              │    │              │      │
│  │ Block on:    │    │ Unit tests   │    │ Race detect  │      │
│  │ • HIGH sev   │    │ Integration  │    │ Full suite   │      │
│  │ • Missing    │    │ Table-driven │    │              │      │
│  └──────────────┘    └──────────────┘    └──────────────┘      │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

## 🔧 Phase 1: New AST Tool - `code-audit`

Create a unified tool that combines and extends our existing tools.

### Tool: `tools/code-audit/main.go`

```go
// Unified AST analysis tool for comprehensive code quality
//
// Modes:
//   - seq:      Detect 31-bit sequence arithmetic bugs
//   - test:     Analyze test struct field coverage
//   - metrics:  Verify Prometheus metrics
//   - coverage: Identify untested code paths
//   - generate: Generate table test cases from signatures
//   - all:      Run all analyses

package main

type AnalysisResult struct {
    Severity    string   // HIGH, MEDIUM, LOW
    Category    string   // seq, test, metrics, coverage
    File        string
    Line        int
    Function    string
    Message     string
    Suggestion  string
}

// Analysis modes
func analyzeSequenceArithmetic(pkgs []*packages.Package) []AnalysisResult
func analyzeTestCoverage(pkgs []*packages.Package) []AnalysisResult
func analyzeMetricsExport(pkgs []*packages.Package) []AnalysisResult
func analyzeCodeCoverage(pkgs []*packages.Package) []AnalysisResult
func generateTestCases(pkgs []*packages.Package) []TestCase
```

### Features

| Feature | Source | Enhancement |
|---------|--------|-------------|
| Sequence arithmetic | seq-audit | Add more patterns |
| Test field analysis | test-audit | Add derived parameter detection |
| Metrics verification | metrics-audit | Add handler coverage |
| Code coverage | NEW | Identify untested functions |
| Test generation | NEW | Generate table test cases |

---

## 🧪 Phase 2: Complete Table-Driven Test Coverage

### New Test Files Required

| File | Purpose | Test Cases | Lines |
|------|---------|------------|-------|
| `connection_lifecycle_table_test.go` | Close, timeout, cleanup | ~15 | ~150 |
| `handshake_table_test.go` | Protocol, flags, versions | ~20 | ~200 |
| `connection_error_table_test.go` | Error paths | ~15 | ~150 |
| `server_table_test.go` | Scalability, shutdown | ~12 | ~120 |
| `crypto_table_test.go` | Key lengths, concurrent | ~12 | ~100 |

### Unified Test Case Structure

```go
// BaseTestCase provides common fields for all table-driven tests
type BaseTestCase struct {
    Name        string
    Skip        string // Skip reason if non-empty
    Parallel    bool   // Can run in parallel
    Timeout     time.Duration
}

// ConnectionTestCase extends BaseTestCase for connection tests
type ConnectionTestCase struct {
    BaseTestCase

    // CODE_PARAMs
    CloseReason     metrics.CloseReason
    PeerIdleTimeout time.Duration
    Encryption      bool

    // TEST_INFRA
    ActiveTransfer   bool
    ConcurrentOps    int

    // EXPECTATIONS
    ExpectSuccess    bool
    ExpectError      string
    ExpectMetric     string
}
```

---

## 🔍 Phase 3: Coverage Enforcement

### New Makefile Target: `make coverage-check`

```makefile
## coverage-check: Ensure minimum code coverage (blocks on failure)
coverage-check:
	@echo "=== Code Coverage Analysis ==="
	go test -coverprofile=coverage.out ./...
	@COVERAGE=$$(go tool cover -func=coverage.out | grep total | awk '{print $$3}' | sed 's/%//'); \
	echo "Total coverage: $$COVERAGE%"; \
	if [ $$(echo "$$COVERAGE < 70" | bc) -eq 1 ]; then \
		echo "❌ Coverage below 70% threshold"; \
		exit 1; \
	fi
	@echo "✅ Coverage meets threshold"

## coverage-report: Generate detailed coverage report
coverage-report:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
```

### Coverage Targets by Package

| Package | Current | Target | Notes |
|---------|---------|--------|-------|
| `congestion/live` | ~85% | 90% | Most critical |
| `circular` | ~95% | 95% | Excellent |
| `metrics` | ~80% | 85% | Good |
| Root package | ~60% | 75% | Needs improvement |
| `crypto` | ~70% | 80% | Needs improvement |

---

## 🤖 Phase 4: AST-Based Test Generation

### Concept: Auto-Generate Test Cases from Function Signatures

```go
// tools/code-audit/generate.go

// Given a function like:
func (c *srtConn) Close() error

// Generate test case structure:
type CloseTestCase struct {
    // Derived from receiver type
    ConnectionState  ConnectionState  // From *srtConn fields

    // Derived from return type
    ExpectError      bool
    ExpectErrorType  string
}

// Given a function like:
func (r *receiver) Push(p packet.Packet)

// Generate test case structure:
type PushTestCase struct {
    // Derived from parameter type
    PacketType       packet.Type
    SequenceNumber   uint32
    TsbpdTime        uint64

    // Derived from receiver state
    ContiguousPoint  uint32
    NakBtreeSize     int
}
```

### Implementation Strategy

1. **Parse function signatures** with `go/ast`
2. **Extract parameter types** and their fields
3. **Identify CODE_PARAMs** from struct definitions
4. **Generate corner values** for each CODE_PARAM
5. **Output test case table** as Go code

---

## 📋 Phase 5: Unified CI Pipeline

### New Makefile Target: `make ci`

```makefile
## ci: Full CI pipeline (use in GitHub Actions)
ci: check test ci-race coverage-check
	@echo ""
	@echo "════════════════════════════════════════"
	@echo "✅ CI Pipeline Passed"
	@echo "════════════════════════════════════════"

## ci-full: Extended CI with integration tests (requires more time)
ci-full: ci test-integration
	@echo ""
	@echo "════════════════════════════════════════"
	@echo "✅ Full CI Pipeline Passed"
	@echo "════════════════════════════════════════"
```

### GitHub Actions Workflow (`.github/workflows/ci.yml`)

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'

      - name: Static Analysis
        run: make check

      - name: Unit Tests
        run: make test

      - name: Race Detection
        run: make ci-race

      - name: Coverage Check
        run: make coverage-check
```

---

## 📅 Implementation Timeline

### Week 1: Foundation

| Day | Task | Deliverable |
|-----|------|-------------|
| 1 | Unified `code-audit` tool | `tools/code-audit/main.go` |
| 2 | Connection lifecycle tests | `connection_lifecycle_table_test.go` |
| 3 | Handshake protocol tests | `handshake_table_test.go` |
| 4 | Error path tests | `connection_error_table_test.go` |
| 5 | Coverage enforcement | `make coverage-check` |

### Week 2: Polish

| Day | Task | Deliverable |
|-----|------|-------------|
| 1 | Server stress tests | `server_table_test.go` |
| 2 | Crypto integration tests | `crypto_table_test.go` |
| 3 | AST test generation (basic) | `code-audit generate` |
| 4 | GitHub Actions CI | `.github/workflows/ci.yml` |
| 5 | Documentation update | README, CONTRIBUTING |

---

## 📊 Success Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Code coverage | 40.0% | 50%+ | 🔄 Working |
| Test functions | 218 | 240+ | 🔄 Adding |
| Table-driven test files | 15 | 17 | 🔄 Adding |
| AST-detected bugs | 4 (fixed) | 0 (prevented) | ✅ Clean |
| CI pipeline time | N/A | < 5 min | ⏳ Pending |
| Race conditions | 0 | 0 | ✅ Clean |

---

## 🎯 Priority Order

```
1. [HIGH]   code-audit unified tool (consolidate 3 tools)     ✅ DONE
2. [HIGH]   Coverage enforcement (make coverage-check)        ✅ DONE
3. [HIGH]   Connection lifecycle table tests (14 tests)       ✅ DONE
4. [HIGH]   Handshake protocol table tests (13+ tests)        ✅ DONE
5. [MEDIUM] Error path table tests (42 tests)                 ✅ DONE
6. [MEDIUM] Server/crypto table tests                         ⏳ NEXT
7. [LOW]    AST test generation                               ⏳ PENDING
```

---

## 🚀 Next Steps

1. ✅ ~~Review and approve this plan~~
2. ✅ ~~Start with `code-audit` unified tool~~
3. ✅ ~~Implement connection lifecycle table tests~~
4. ✅ ~~Implement handshake protocol table tests~~
5. ✅ ~~Implement error path table tests~~
6. 🔄 Implement server/crypto table tests
7. ⏳ Increase coverage to 50%+

---

## 📜 Progress Log

### 2025-12-29 (Session 2)

**Completed:**
- ✅ Unified `code-audit` tool working (`make code-audit`)
- ✅ Coverage Makefile targets (exclude `./tools/`):
  - `make coverage` - Summary report
  - `make coverage-by-package` - Per-package breakdown
  - `make coverage-check` - Threshold enforcement (default 30%)
  - `make coverage-html` - Detailed HTML report
- ✅ Fixed `.PHONY` declarations in Makefile
- ✅ Documented NAK vs TSBPD race condition finding (not a bug - correct behavior)
- ✅ TDD investigation of `Corner_TotalPackets_Large` test
- ✅ **Connection Lifecycle Table Tests** (`connection_lifecycle_table_test.go`):
  - 14 test cases covering: graceful close, concurrent close, close under load
  - Close reasons: Graceful, ContextCancel, Error, PeerIdle
  - Corner cases: ZeroTimeout, ManyCloses, StressConcurrent
- ✅ **Handshake Protocol Table Tests** (`handshake_table_test.go`):
  - 13 connection tests based on SRT RFC draft-sharabayko-srt-01
  - Rejection scenarios: REJ_PEER, REJ_CLOSE, REJ_BADSECRET
  - Corner cases: EmptyStreamId, LongStreamId, SpecialChars, Min/Max latency
  - HandshakeType tests: String(), IsHandshake(), IsRejection()
  - Rejection reason validation (all 16 RFC codes)
- ✅ **Compact JSON output** for connection close events (single line)
- ✅ **Makefile documentation** for Go 1.25 experimental features:
  - `GOEXPERIMENT=jsonv2` for faster JSON (see https://go.dev/doc/go1.25)
  - `GOEXPERIMENT=greenteagc` for improved GC

**Coverage Status:**
- Total: 40.0% (target: 50%+)
- Best: `./circular` (88.1%), `./packet` (85.1%)
- Needs work: `.` root (45.3%), `./congestion/live` (73.6%)

### 2025-12-29 (Session 3)

**Completed:**
- ✅ **Error Path Table Tests** (`error_table_test.go`):
  - 35 config validation tests (timeouts, MSS, payload, passphrase, etc.)
  - 5 dial error tests (bad network, address, port, unreachable)
  - 2 listen error tests (invalid config)
  - 2 connection read/write after close tests
- ✅ **Enabled Go 1.25 experimental features by default**:
  - `GOEXPERIMENT=jsonv2,greenteagc` now set in Makefile
  - Can disable with `make build GOEXPERIMENT=`

**🐛 BUGS FOUND AND FIXED:**

| Bug ID | Severity | File | Description |
|--------|----------|------|-------------|
| BUG-001 | HIGH | `connection.go` | **Write after Close race condition** |
| BUG-002 | MEDIUM | `crypto/crypto.go` | **Keywrap errors not properly wrapped** |
| BUG-003 | MEDIUM | `crypto/crypto.go` | **GenerateSEK accepted invalid key types** |

**BUG-001 Details:**
- **Symptom**: `conn.Write()` after `conn.Close()` sometimes returned `nil` (success) instead of `io.EOF`
- **Root Cause**: Race condition in the `select` statement:
  ```go
  // BUGGY CODE:
  select {
  case <-c.ctx.Done():
      return 0, io.EOF
  case c.writeQueue <- p:  // Could win race against ctx.Done()!
  default:
      return 0, io.EOF
  }
  ```
- **Fix**: Check `ctx.Done()` BEFORE attempting the write:
  ```go
  // Check if connection is closed FIRST
  select {
  case <-c.ctx.Done():
      return 0, io.EOF
  default:
  }
  // Then proceed with write...
  ```
- **Verification**: Ran test 10 times, 10/10 passes (was 5/10 before fix)
- **Lesson**: Go's `select` is non-deterministic - when multiple channels are ready,
  one is chosen randomly. For close detection, always check ctx.Done() first!

**BUG-002 Details:**
- **Symptom**: Wrong passphrase returned raw `keywrap.ErrUnwrapFailed` error instead of semantic error
- **Root Cause**: `UnmarshalKM()` passed keywrap library errors through unchanged:
  ```go
  // BUGGY CODE:
  unwrap, err := keywrap.Unwrap(kek, km.Wrap)
  if err != nil {
      return err  // Raw keywrap error, callers can't check errors.Is()
  }
  ```
- **Problem**: Callers couldn't use `errors.Is(err, crypto.ErrDecryptionFailed)` to detect wrong passphrase
- **Fix**: Added `ErrDecryptionFailed` and wrap keywrap errors:
  ```go
  // FIXED CODE:
  unwrap, err := keywrap.Unwrap(kek, km.Wrap)
  if err != nil {
      if errors.Is(err, keywrap.ErrUnwrapFailed) {
          return fmt.Errorf("%w: %v", ErrDecryptionFailed, err)
      }
      return fmt.Errorf("crypto: key unwrap error: %w", err)
  }
  ```
- **Verification**: Added `TestCrypto_ErrorChain_*` tests to validate error semantics
- **Lesson**: Always wrap external library errors with semantic errors for proper `errors.Is()` handling

**BUG-003 Details:**
- **Symptom**: `GenerateSEK()` accepted `UnencryptedPacket` and `EvenAndOddKey` without error
- **Root Cause**: Validation used `IsValid()` which returns true for values 0-3:
  ```go
  // BUGGY CODE:
  func (c *crypto) GenerateSEK(key packet.PacketEncryption) error {
      if !key.IsValid() {  // IsValid() returns true for 0,1,2,3
          return fmt.Errorf("crypto: unknown key type")
      }
      // ... code silently ignores UnencryptedPacket and EvenAndOddKey ...
  }
  ```
- **Problem**: Calling `GenerateSEK(packet.UnencryptedPacket)` would succeed but not generate any key (neither evenSEK nor oddSEK updated)
- **Fix**: Explicitly check for valid SEK generation types:
  ```go
  // FIXED CODE:
  func (c *crypto) GenerateSEK(key packet.PacketEncryption) error {
      if key != packet.EvenKeyEncrypted && key != packet.OddKeyEncrypted {
          return fmt.Errorf("crypto: invalid key type for SEK generation, must be even or odd")
      }
      // ...
  }
  ```
- **Verification**: Added `TestCrypto_GenerateSEK_Table` with invalid key type tests
- **Lesson**: Be explicit about valid input values - don't rely on `IsValid()` methods that may be too permissive

**Completed:**
- ✅ Server table tests (`server_table_test.go`): 7 stream ID tests, 1 concurrent test, 2 shutdown tests, 1 config test
- ✅ Crypto table tests (`crypto/crypto_table_test.go`): 16 key length tests, 7 KM unmarshal tests, 5 KM marshal tests, 4 GenerateSEK tests, 10 encrypt/decrypt tests, 5 round-trip tests, 4 marshal errors tests, 3 error chain tests, 3 passphrase tests
- ✅ Config table tests (`config_table_test.go`): 5 auto-config tests, 10 URL unmarshal tests, 1 defaults test, 4 marshal round-trip tests

**Coverage Progress:**
- `crypto`: 89.1%
- `config.ApplyAutoConfiguration`: 0% → 100%
- `config.UnmarshalURL`: 66.7% → 100%
- `config.MarshalURL`: already 100%
- Main package: 46.5% → 47.3%
- **Total: 40.6%** (target: 50%+)

---

> **📌 See Also:** [Receive Testing Strategy](receive_testing_strategy.md) - Comprehensive plan for testing `receive.go`, the most critical file (2758 lines, 408 branches)

---

## 🎯 HIGH-RISK AREAS FOR TESTING

### Methodology
Areas ranked by: **Low Coverage × High Complexity × Config Dependency**

Files analyzed:
- `receive.go`: 2758 lines, **408 branches**, 161 config references
- `connection.go`: 2437 lines, **299 branches**
- `send.go`: 600+ lines, sender-side logic
- `dial.go`: 1043 lines, **136 branches**

### 🔴 CRITICAL PRIORITY (Coverage <50%, High Complexity)

| Function | Coverage | File | Risk Reason |
|----------|----------|------|-------------|
| `tickUpdateRateStats` | 23.8% | `send.go:315` | Rate calculation affects congestion control |
| `parseRetryStrategy` | 28.6% | `receive.go:48` | Config parsing, affects retry behavior |
| `push` | 33.3% | `connection.go:749` | Packet delivery path |
| `deliver` | 33.3% | `connection.go:876` | Data delivery to application |
| `handleKMRequest` | 38.3% | `connection.go:1555` | **Crypto key exchange** |
| `handleKMResponse` | 18.4% | `connection.go:1632` | **Crypto key exchange** |
| `handlePacket` | 44.4% | `connection.go:967` | Main packet dispatch |
| `NAK` (sender) | 50.0% | `send.go:399` | **Loss recovery** |
| `periodicNakOriginal` | 50.0% | `receive.go:1596` | Legacy NAK path |

### 🟠 HIGH PRIORITY (Coverage 50-70%, Config-Heavy)

| Function | Coverage | File | Risk Reason |
|----------|----------|------|-------------|
| `handleNAK` | 53.3% | `connection.go:1174` | NAK processing |
| `getSleepDuration` | 54.5% | `receive.go:171` | Timing-critical |
| `sendKMRequest` | 54.5% | `connection.go:1988` | Crypto setup |
| `deliverReadyPacketsLocked` | 55.6% | `receive.go:2705` | Packet ordering |
| `handleUserPacket` | 55.6% | `connection.go:944` | User data handling |
| `ACK` (sender) | 57.1% | `send.go:354` | ACK processing |
| `Push` (sender) | 57.1% | `send.go:158` | Send buffer |
| `pushWithLock` | 57.1% | `receive.go:577` | Receive path |
| `ReadPacket` | 58.3% | `connection.go:639` | Data read API |
| `Tick` (sender) | 58.8% | `send.go:216` | Periodic sender |
| `handleShutdown` | 60.0% | `connection.go:1114` | Clean shutdown |
| `sendNAK` | 60.9% | `connection.go:1775` | NAK transmission |
| `Validate` | 63.7% | `config.go:964` | Config validation |
| `checkFastNak` | 66.7% | `fast_nak.go:17` | FastNAK heuristic |
| `handleKeepAlive` | 66.7% | `connection.go:1070` | Connection liveness |
| `WritePacket` | 66.7% | `connection.go:685` | Data write API |
| `drainPacketRing` | 68.8% | `receive.go:2047` | Ring buffer drain |
| `handleACK` | 69.6% | `connection.go:1128` | ACK processing |

### 🟡 MEDIUM PRIORITY (Coverage 70-85%)

| Function | Coverage | File | Risk Reason |
|----------|----------|------|-------------|
| `pop` | 71.4% | `connection.go:772` | Packet retrieval |
| `watchPeerIdleTimeout` | 71.9% | `connection.go:2073` | Timeout handling |
| `handleACKACK` | 74.1% | `connection.go:1246` | RTT calculation |
| `getKeepaliveInterval` | 75.0% | `connection.go:1105` | Timing |
| `Close` | 75.0% | `connection.go:2027` | Resource cleanup |

### Bug-Prone Patterns Identified

1. **Key Material Exchange** (`handleKMRequest`/`handleKMResponse` at 18-38%)
   - Crypto negotiation with multiple paths
   - Low coverage = potential security/compatibility bugs

2. **Sender NAK/ACK Handling** (`NAK` 50%, `ACK` 57%)
   - Loss recovery is critical for reliability
   - Rate stats at 23.8% affects congestion control

3. **Packet Delivery Path** (`push`/`deliver` at 33%)
   - Core data path with minimal coverage
   - Affects all data transfer

4. **Config Validation** (`Validate` at 63.7%)
   - 1209 lines of config code
   - Many parameter combinations untested

### Recommended Test Focus

**Phase 1: Critical (Security + Reliability)**
1. `handleKMRequest`/`handleKMResponse` - crypto key exchange
2. `NAK`/`ACK` in send.go - loss recovery
3. `handlePacket` - packet dispatch

**Phase 2: Data Path**
1. `push`/`deliver` - packet delivery
2. `ReadPacket`/`WritePacket` - user API
3. `drainPacketRing`/`deliverReadyPacketsLocked` - ordering

**Phase 3: Timing + Config**
1. `getSleepDuration`/`watchPeerIdleTimeout` - timing
2. `Validate` - config edge cases
3. `tickUpdateRateStats` - congestion control

---

**Next Steps:**
- 📄 **[Receive Testing Strategy](receive_testing_strategy.md)** - Detailed plan for the most critical file
- Focus on Critical Priority functions first
- Add table-driven tests for sender NAK/ACK paths
- Add crypto key exchange tests

### Receive.go Priority (See [Receive Testing Strategy](receive_testing_strategy.md))

The `receive.go` file deserves special attention:
- **2758 lines**, **408 branches**, **30+ config parameters**
- Recommended to split into 7 smaller files before comprehensive testing
- Table-driven tests with AST/reflection-based generation
- Target: 73.6% → 90%+ coverage

---

## 📝 Notes

### Fork Advantages

- **No upstream approval needed** - can move fast
- **Breaking changes OK** - can refactor freely
- **Full control** - can add any tooling needed
- **Clean slate** - can set higher quality bar

### Design Principles

1. **AST over regex** - type-aware analysis catches more bugs
2. **Tables over individual tests** - 68% less code, easier maintenance
3. **Automate everything** - CI prevents regressions
4. **Fail fast** - block on HIGH severity issues

