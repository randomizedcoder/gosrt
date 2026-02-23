# Test Improvement Plan - Detailed Implementation

**Date**: December 29, 2025
**Status**: PLANNING
**Related**: `test_audit_comprehensive.md`, `table_driven_test_design.md`

---

## 📋 Overview

This document provides specific implementation details for the top 6 test improvement recommendations using **table-driven tests** to minimize code while maximizing coverage.

### Design Principle: Table-Driven Tests

Following `table_driven_test_design.md`, we use:
- **CODE_PARAMs**: Production parameters (need corner coverage)
- **TEST_INFRA**: Test mechanics (don't need combinatorial)
- **EXPECTATIONS**: Derived from inputs
- **Derived Parameters**: Auto-calculated from CODE_PARAMs

---

## 1. Connection Lifecycle Tests (Priority: HIGH)

### Current State

**Existing Tests** (`connection_test.go`):
- `TestEncryption` (line 15) - Encryption basics
- `TestEncryptionRetransmit` (line 154) - Retransmit with encryption
- `TestEncryptionKeySwap` (line 309) - Key swap mechanism
- `TestStats` (line 445) - Statistics

**Gap**: No tests for connection state transitions, timeouts, cleanup, or error recovery.

### Functions Requiring Tests

| Function | File | Line | Test Coverage |
|----------|------|------|---------------|
| `Close()` | `connection.go` | 2014 | ❌ None |
| `close(reason)` | `connection.go` | 2136 | ❌ None |
| `handleShutdown()` | `connection.go` | 1101 | ❌ None |
| `sendShutdown()` | `connection.go` | 1676 | ❌ None |
| `resetPeerIdleTimeout()` | `connection.go` | 2043 | ❌ None |
| `watchPeerIdleTimeout()` | `connection.go` | 2060 | ❌ None |
| `GetPeerIdleTimeoutRemaining()` | `connection.go` | 2026 | ❌ None |

### New Test File: `connection_lifecycle_table_test.go`

**Table-Driven Approach** (~150 lines vs ~400 individual tests)

```go
// connection_lifecycle_table_test.go

type LifecycleTestCase struct {
    // CODE_PARAMs
    Name              string
    CloseReason       metrics.CloseReason  // Graceful, Error, Timeout, PeerShutdown
    PeerIdleTimeout   time.Duration        // 0 = default

    // TEST_INFRA
    ActiveTransfer    bool   // Data transfer in progress
    ConcurrentCloses  int    // 0 = single, >0 = concurrent
    SendActivity      bool   // Reset timeout before close

    // EXPECTATIONS
    ExpectShutdownSent bool
    ExpectCleanup      bool
    ExpectMetricIncr   bool
}

var lifecycleTests = []LifecycleTestCase{
    // Core scenarios
    {Name: "GracefulClose", CloseReason: CloseReasonGraceful, ExpectShutdownSent: true, ExpectCleanup: true},
    {Name: "CloseUnderLoad", CloseReason: CloseReasonGraceful, ActiveTransfer: true, ExpectCleanup: true},
    {Name: "PeerIdleTimeout", CloseReason: CloseReasonTimeout, PeerIdleTimeout: 100*time.Millisecond},
    {Name: "TimeoutReset", SendActivity: true, PeerIdleTimeout: 100*time.Millisecond},
    {Name: "ConcurrentClose", ConcurrentCloses: 5, ExpectCleanup: true},

    // Corner cases
    {Name: "Corner_ZeroTimeout", PeerIdleTimeout: 0},
    {Name: "Corner_ErrorClose", CloseReason: CloseReasonError, ExpectShutdownSent: false},
    {Name: "Corner_PeerShutdown", CloseReason: CloseReasonPeerShutdown},
}

func TestConnection_Lifecycle_Table(t *testing.T) {
    for _, tc := range lifecycleTests {
        t.Run(tc.Name, func(t *testing.T) {
            t.Parallel()
            runLifecycleTest(t, tc)
        })
    }
}
```

### Implementation Steps

1. Create `connection_lifecycle_table_test.go` (~150 lines)
2. Single `runLifecycleTest()` helper function
3. Use `test-audit` tool to verify corner coverage
4. Run with `-race` flag automatically

---

## 2. Handshake Protocol Tests (Priority: HIGH)

### Current State

**Existing Tests**:
- `dial_test.go`:
  - `TestDialOK` (line 54) - Basic dial
  - `TestDialV4` (line 85) - HSv4
  - `TestDialV5` (line 224) - HSv5
  - `TestDialV5MissingExtension` (line 389)
- `listen_test.go`:
  - `TestListenHSV4` (line 123) - HSv4
  - `TestListenHSV5` (line 262) - HSv5
  - `TestListenDiscardRepeatedHandshakes` (line 622)

**Gap**: No tests for error paths, version negotiation failures, or malformed packets.

### Handshake Types (from `packet/packet.go`)

| Type | Value | Line | Test Coverage |
|------|-------|------|---------------|
| `HSTYPE_INDUCTION` | 0x00000001 | 83 | ✅ Partial |
| `HSTYPE_CONCLUSION` | 0xFFFFFFFF | 81 | ✅ Partial |
| `HSTYPE_WAVEHAND` | 0x00000000 | 82 | ❌ None |
| `HSTYPE_AGREEMENT` | 0xFFFFFFFE | 80 | ❌ None |
| `HSTYPE_DONE` | 0xFFFFFFFD | 79 | ❌ None |

### Key Functions (from `dial.go`)

| Function | Line | Purpose |
|----------|------|---------|
| `handleHandshake()` | 450 | Main handshake router |
| `sendInduction()` | 741 | Send induction packet |
| `sendShutdown()` | 776 | Send shutdown on error |

### New Test File: `handshake_table_test.go`

**Table-Driven Approach** (~200 lines vs ~500 individual tests)

```go
// handshake_table_test.go

type HandshakeTestCase struct {
    // CODE_PARAMs
    Name           string
    HSVersion      uint32  // 4 or 5
    HandshakeType  packet.HandshakeType

    // Flag combinations (CODE_PARAMs)
    HasTSBPDSND    bool
    HasTLPKTDROP   bool
    HasCRYPT       bool
    HasREXMITFLG   bool

    // TEST_INFRA
    Malformed      bool    // Send truncated/invalid
    RepeatCount    int     // Repeated packets
    TimeoutMs      int     // 0 = no timeout test

    // EXPECTATIONS
    ExpectAccept   bool
    ExpectReject   bool
    ExpectError    string  // Specific error message
}

var handshakeTests = []HandshakeTestCase{
    // Valid scenarios
    {Name: "V5_AllFlags", HSVersion: 5, HasTSBPDSND: true, HasTLPKTDROP: true, HasCRYPT: true, HasREXMITFLG: true, ExpectAccept: true},
    {Name: "V4_Valid", HSVersion: 4, ExpectAccept: true},

    // Missing flags (connection.go:1351-1381)
    {Name: "V5_MissingTSBPDSND", HSVersion: 5, HasTLPKTDROP: true, HasCRYPT: true, HasREXMITFLG: true, ExpectReject: true, ExpectError: "TSBPDSND"},
    {Name: "V5_MissingTLPKTDROP", HSVersion: 5, HasTSBPDSND: true, HasCRYPT: true, HasREXMITFLG: true, ExpectReject: true, ExpectError: "TLPKTDROP"},
    {Name: "V5_MissingCRYPT", HSVersion: 5, HasTSBPDSND: true, HasTLPKTDROP: true, HasREXMITFLG: true, ExpectReject: true, ExpectError: "CRYPT"},
    {Name: "V5_MissingREXMITFLG", HSVersion: 5, HasTSBPDSND: true, HasTLPKTDROP: true, HasCRYPT: true, ExpectReject: true, ExpectError: "REXMITFLG"},

    // Error cases
    {Name: "InvalidVersion", HSVersion: 99, ExpectReject: true},
    {Name: "Malformed", Malformed: true, ExpectReject: true},
    {Name: "Timeout", TimeoutMs: 100, ExpectReject: true},
    {Name: "RepeatedInduction", HandshakeType: packet.HSTYPE_INDUCTION, RepeatCount: 3, ExpectAccept: true},

    // Corner cases
    {Name: "Corner_V4_WithV5Flags", HSVersion: 4, HasTSBPDSND: true, ExpectReject: true, ExpectError: "HSv4 only"},
}

func TestHandshake_Table(t *testing.T) {
    for _, tc := range handshakeTests {
        t.Run(tc.Name, func(t *testing.T) {
            t.Parallel()
            runHandshakeTest(t, tc)
        })
    }
}
```

### Implementation Steps

1. Create `handshake_table_test.go` (~200 lines)
2. Single `runHandshakeTest()` helper
3. Covers all flag combinations via table
4. Auto-parallel execution

---

## 3. Race Detection CI Integration (Priority: HIGH)

### Current Makefile Targets

| Target | Line | Description |
|--------|------|-------------|
| `test` | 13 | `-race` flag ✅ |
| `test-quick` | 17 | `-race` flag ✅ |
| `test-race` | 332 | Dedicated race tests |
| `test-race-wraparound` | 337 | Wraparound race |
| `test-race-eventloop` | 343 | EventLoop race |
| `test-stream-race` | 326 | Stream race |

### Gap: No CI Integration

Currently race tests exist but aren't mandatory in CI.

### Existing Race Tests (`congestion/live/receive_race_test.go`)

| Test | Line | Coverage |
|------|------|----------|
| `TestRace_ConcurrentPacketPush` | ~50 | Ring buffer |
| `TestRace_EventLoop_*` | ~200 | EventLoop variants |
| `TestRace_SequenceWraparound` | ~400 | 31-bit wraparound |

### Proposed Changes

**1. Add CI target to Makefile:**
```makefile
# Line ~550 (new)
## ci-race: Run race detection for CI (fails build on race)
ci-race:
	@echo "Running race detection..."
	go test -race -timeout 5m ./... 2>&1 | tee race_results.txt
	@if grep -q "WARNING: DATA RACE" race_results.txt; then \
		echo "❌ DATA RACE DETECTED - failing build"; \
		exit 1; \
	fi
	@echo "✅ No races detected"
```

**2. Add pre-commit hook:**
```bash
# .githooks/pre-commit
#!/bin/bash
make ci-race || exit 1
```

**3. Document in README or CONTRIBUTING.md**

### Implementation Steps

1. Add `ci-race` target to Makefile
2. Create `.githooks/pre-commit`
3. Add GitHub Actions workflow (if applicable)
4. Document race test requirements

---

## 4. Crypto Integration Tests (Priority: MEDIUM)

### Current State

**Existing Tests** (`crypto/crypto_test.go`):

| Test | Line | Coverage |
|------|------|----------|
| `TestInvalidKeylength` | 22 | Key validation |
| `TestInvalidKM` | 27 | KM validation |
| `TestUnmarshal` | 50 | KM unmarshalling |
| `TestMarshal` | 114 | KM marshalling |
| `TestDecode` | 199 | Decryption |
| `TestEncode` | 268 | Encryption |

### Key Functions (`crypto/crypto.go`)

| Function | Line | Purpose |
|----------|------|---------|
| `New()` | 47 | Create crypto instance |
| `GenerateSEK()` | 82 | Generate SEK |
| `UnmarshalKM()` | 119 | Parse key material |
| `MarshalKM()` | 166 | Serialize key material |
| `EncryptOrDecryptPayload()` | 224 | Encrypt/decrypt |
| `calculateKEK()` | 277 | Derive KEK |

### New Test File: `crypto/crypto_table_test.go`

**Table-Driven Approach** (~100 lines vs ~200 individual tests)

```go
// crypto/crypto_table_test.go

type CryptoTestCase struct {
    // CODE_PARAMs
    Name           string
    KeyLength      int     // 128, 192, 256, or invalid
    Passphrase     string

    // TEST_INFRA
    PacketCount    int     // 0 = single packet
    Concurrent     int     // 0 = sequential
    WrongDecrypt   bool    // Use wrong passphrase for decrypt

    // EXPECTATIONS
    ExpectSuccess  bool
    ExpectError    string
}

var cryptoTests = []CryptoTestCase{
    // Valid key lengths
    {Name: "AES128", KeyLength: 128, Passphrase: "test", ExpectSuccess: true},
    {Name: "AES192", KeyLength: 192, Passphrase: "test", ExpectSuccess: true},
    {Name: "AES256", KeyLength: 256, Passphrase: "test", ExpectSuccess: true},

    // Invalid scenarios
    {Name: "InvalidKeyLength", KeyLength: 64, ExpectError: "invalid"},
    {Name: "WrongPassphrase", KeyLength: 256, WrongDecrypt: true, ExpectError: "decrypt"},
    {Name: "EmptyPassphrase", KeyLength: 256, Passphrase: "", ExpectError: "passphrase"},

    // Load/concurrency
    {Name: "Load_10k", KeyLength: 256, PacketCount: 10000, ExpectSuccess: true},
    {Name: "Concurrent_8", KeyLength: 256, Concurrent: 8, ExpectSuccess: true},

    // Corner cases
    {Name: "Corner_MinKey", KeyLength: 128, ExpectSuccess: true},
    {Name: "Corner_MaxKey", KeyLength: 256, ExpectSuccess: true},
}

func TestCrypto_Table(t *testing.T) {
    for _, tc := range cryptoTests {
        t.Run(tc.Name, func(t *testing.T) {
            t.Parallel()
            runCryptoTest(t, tc)
        })
    }
}
```

### Implementation Steps

1. Create `crypto/crypto_table_test.go` (~100 lines)
2. Single `runCryptoTest()` helper
3. Add benchmark in same file

---

## 5. Server Stress Tests (Priority: MEDIUM)

### Current State

**Existing Tests** (`server_test.go`):
- `TestServer` (line 11) - Basic server test only

### Server Functions (`server.go`)

| Function | Line | Purpose |
|----------|------|---------|
| `NewServer()` | 95 | Create server |
| `ListenAndServe()` | 123 | Start serving |
| `Listen()` | 134 | Start listening |
| `Serve()` | 170 | Accept connections |
| `Shutdown()` | 247 | Graceful shutdown |
| `GetConnections()` | 267 | List connections |

### New Test File: `server_table_test.go`

**Table-Driven Approach** (~120 lines vs ~300 individual tests)

```go
// server_table_test.go

type ServerTestCase struct {
    // CODE_PARAMs
    Name              string
    MaxConnections    int    // 0 = unlimited

    // TEST_INFRA
    ConcurrentClients int
    ConnectCycles     int    // Rapid connect/disconnect
    ShutdownDuring    bool   // Shutdown while active
    RestartAfter      bool   // Restart test

    // EXPECTATIONS
    ExpectAllConnected bool
    ExpectRejectOver   bool   // Reject over limit
    ExpectCleanup      bool
}

var serverTests = []ServerTestCase{
    {Name: "SingleClient", ConcurrentClients: 1, ExpectAllConnected: true},
    {Name: "TenClients", ConcurrentClients: 10, ExpectAllConnected: true},
    {Name: "ConnectionLimit_Under", MaxConnections: 10, ConcurrentClients: 5, ExpectAllConnected: true},
    {Name: "ConnectionLimit_At", MaxConnections: 10, ConcurrentClients: 10, ExpectAllConnected: true},
    {Name: "ConnectionLimit_Over", MaxConnections: 10, ConcurrentClients: 15, ExpectRejectOver: true},
    {Name: "RapidCycle", ConnectCycles: 50, ExpectCleanup: true},
    {Name: "ShutdownActive", ConcurrentClients: 5, ShutdownDuring: true, ExpectCleanup: true},
    {Name: "Restart", ConcurrentClients: 3, RestartAfter: true, ExpectCleanup: true},

    // Corner cases
    {Name: "Corner_ZeroMaxConn", MaxConnections: 0, ConcurrentClients: 100, ExpectAllConnected: true},
    {Name: "Corner_SingleMaxConn", MaxConnections: 1, ConcurrentClients: 5, ExpectRejectOver: true},
}

func TestServer_Table(t *testing.T) {
    for _, tc := range serverTests {
        t.Run(tc.Name, func(t *testing.T) {
            runServerTest(t, tc)  // Not parallel - server tests use shared port
        })
    }
}
```

### Implementation Steps

1. Create `server_table_test.go` (~120 lines)
2. Single `runServerTest()` helper
3. Sequential execution (port sharing)
4. Add benchmark separately

---

## 6. Error Handling Path Tests (Priority: MEDIUM)

### Error Return Points in `connection.go`

| Line | Error Type | Current Coverage |
|------|------------|------------------|
| 672 | Read error | ❌ None |
| 693 | Write error | ❌ None |
| 705 | Write error | ❌ None |
| 770 | Encryption error | ✅ Partial |
| 805 | SEK generation error | ❌ None |
| 1127 | Invalid ACK | ❌ None |
| 1174 | Invalid NAK | ❌ None |
| 1331 | Invalid HSReq | ❌ None |

### Close Reasons (`metrics/registry.go`)

```go
// Line 12
type CloseReason int

const (
    CloseReasonGraceful      CloseReason = iota // Normal shutdown
    CloseReasonError                            // Error occurred
    CloseReasonTimeout                          // Peer idle timeout
    CloseReasonPeerShutdown                     // Peer sent shutdown
    // ... more reasons
)
```

### New Test File: `connection_error_table_test.go`

**Table-Driven Approach** (~150 lines vs ~350 individual tests)

```go
// connection_error_table_test.go

type ErrorTestCase struct {
    // CODE_PARAMs
    Name           string
    ErrorType      string  // "ACK", "NAK", "Encryption", "SEK", "Network", "Buffer"

    // TEST_INFRA
    InjectError    func(*srtConn) // Error injection function
    MalformedData  []byte         // Bad packet data

    // EXPECTATIONS
    ExpectLog      string  // Expected log message
    ExpectClose    bool    // Connection should close
    ExpectRecover  bool    // Connection should recover
    ExpectMetric   string  // Metric to check
}

var errorTests = []ErrorTestCase{
    // Packet errors (connection.go:1127, 1174)
    {Name: "InvalidACK", ErrorType: "ACK", ExpectLog: "invalid ACK", ExpectRecover: true},
    {Name: "InvalidNAK", ErrorType: "NAK", ExpectLog: "invalid NAK", ExpectRecover: true},
    {Name: "MalformedACK", ErrorType: "ACK", MalformedData: []byte{0x00}, ExpectRecover: true},

    // Crypto errors (connection.go:770, 805)
    {Name: "EncryptionFail", ErrorType: "Encryption", ExpectClose: true, ExpectLog: "encryption failed"},
    {Name: "SEKGenFail", ErrorType: "SEK", ExpectClose: true, ExpectLog: "failed to generate SEK"},

    // Network errors
    {Name: "NetworkOutage", ErrorType: "Network", ExpectRecover: true},
    {Name: "NetworkTimeout", ErrorType: "Network", ExpectClose: true},

    // Buffer errors
    {Name: "BufferOverflow", ErrorType: "Buffer", ExpectRecover: true},

    // Corner cases
    {Name: "Corner_EmptyACK", ErrorType: "ACK", MalformedData: []byte{}, ExpectRecover: true},
    {Name: "Corner_TruncatedNAK", ErrorType: "NAK", MalformedData: []byte{0x01, 0x02}, ExpectRecover: true},
}

func TestConnection_Error_Table(t *testing.T) {
    for _, tc := range errorTests {
        t.Run(tc.Name, func(t *testing.T) {
            t.Parallel()
            runErrorTest(t, tc)
        })
    }
}
```

### Implementation Steps

1. Create `connection_error_table_test.go` (~150 lines)
2. Error injection helpers
3. Single `runErrorTest()` function
4. Covers all error paths via table

---

## 📊 Implementation Priority Matrix (Table-Driven)

| # | Recommendation | Priority | Effort | Lines | Dependencies |
|---|----------------|----------|--------|-------|--------------|
| 1 | Connection Lifecycle | HIGH | **1 day** | ~150 | None |
| 2 | Handshake Protocol | HIGH | **1 day** | ~200 | None |
| 3 | Race Detection CI | HIGH | 0.5 day | ~20 | Makefile |
| 4 | Crypto Integration | MEDIUM | **0.5 day** | ~100 | #1 |
| 5 | Server Stress | MEDIUM | **0.5 day** | ~120 | #1 |
| 6 | Error Handling | MEDIUM | **1 day** | ~150 | #1, #2 |

**Total Effort**: ~4.5 days (vs ~12.5 days individual = **64% reduction**)

---

## 📁 New Files Summary (Table-Driven)

| File | Purpose | Lines (Table) | Lines (Individual) | Savings |
|------|---------|---------------|-------------------|---------|
| `connection_lifecycle_table_test.go` | Connection state | ~150 | ~400 | **63%** |
| `handshake_table_test.go` | Protocol tests | ~200 | ~500 | **60%** |
| `server_table_test.go` | Scalability | ~120 | ~300 | **60%** |
| `connection_error_table_test.go` | Error paths | ~150 | ~350 | **57%** |
| `crypto/crypto_table_test.go` | Extended crypto | ~100 | ~200 | **50%** |

**Total New Test Code**: ~720 lines (vs ~1,750 individual = **59% reduction**)

---

## ✅ Success Criteria

1. **Connection Lifecycle**: All close scenarios tested, no goroutine leaks
2. **Handshake**: All handshake types tested, error paths covered
3. **Race Detection**: Zero races in CI runs
4. **Crypto**: All key lengths tested, concurrent access safe
5. **Server**: 100 concurrent clients supported, clean shutdown
6. **Error Handling**: All error paths tested, metrics accurate

---

## 🚀 Next Steps

1. Review this document
2. Approve priority order
3. **Day 1**: #3 (Race Detection CI) + #1 (Connection Lifecycle)
4. **Day 2**: #2 (Handshake Protocol) + #4 (Crypto)
5. **Day 3**: #5 (Server) + #6 (Error Handling)
6. Run `test-audit` on new files to verify corner coverage

### Table-Driven Benefits

- **59% less code** (720 vs 1,750 lines)
- **64% less effort** (4.5 vs 12.5 days)
- **Easier to add cases** (just add row to table)
- **Automatic parallel execution** (`t.Parallel()`)
- **Corner case verification** with `test-audit` tool

