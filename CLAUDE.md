# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GoSRT is a pure-Go implementation of the SRT (Secure Reliable Transport) protocol for live video/audio streaming. The codebase is performance-focused, targeting 500+ Mb/s throughput with lock-free architecture.

**Not implemented by design**: Buffer mode, File Transfer Congestion Control (FileCC), Rendezvous Handshake, Connection Bonding.

## Build Commands

```bash
make build              # Build with Go 1.26+ (greenteagc GC default, jsonv2 experimental)
make client server      # Build production binaries
make client-debug server-debug  # Build with debug symbols (for profiling)
make build-performance  # Build performance testing tools
```

**Pure Go - No CGO Required**: All binaries are built with `CGO_ENABLED=0`. The io_uring integration (`giouring`) uses pure Go syscalls, not liburing. This enables fully static binaries with no C dependencies.

## Testing

```bash
# Unit tests
make test               # Full test suite with race detection and static checks
make test-quick         # Fast tests without static checks (development)

# Specific test suites
make test-circular      # Circular number/sequence arithmetic tests
make test-packet        # Packet marshaling tests
make test-flags         # CLI flag parsing and config application tests
make test-stream-tier1  # Core receiver stream tests (~50 tests, <3s)
make test-stream-tier2  # Extended coverage (~200 tests, <15s)
make test-stream-tier3  # Comprehensive (~1080 tests, <60s)

# Race detection
make test-race-eventloop  # EventLoop concurrency tests
make ci-race              # Full race detection for CI

# Debug builds (lock-free context verification)
make test-debug         # Run debug assertion tests
make build-debug        # Build binaries that panic on context violations

# Integration tests (require root for network namespaces)
sudo make test-isolation CONFIG=<config-name>
sudo make test-parallel CONFIG=<config-name>
sudo make test-network CONFIG=<config-name>

# Performance tests (no sudo required)
make test-performance   # AIMD search for max throughput
```

Run a single test:
```bash
go test -v ./congestion/live -run 'TestStream_Tier1'
go test -v -race ./congestion/live/send -run 'TestAdaptiveBackoff'
```

## Testing Philosophy & Quality Standards

GoSRT maintains a high quality bar through comprehensive testing at multiple levels. Tests are designed to catch subtle bugs—especially around sequence number wraparound and concurrent access—before they reach production.

### Table-Driven Tests

The preferred testing pattern. Table-driven tests provide:
- **Exhaustive coverage**: Easy to add new cases without code duplication
- **Self-documenting**: Test names describe the scenario being tested
- **Corner case focus**: Explicitly enumerate edge cases and boundaries

```go
// Example from circular/seq_math_31bit_wraparound_test.go
testCases := []struct {
    name string
    a    uint32
    b    uint32
    want bool
}{
    {"MAX < 0", MaxSeqNumber31, 0, true},          // Wraparound boundary
    {"MAX < 50", MaxSeqNumber31, 50, true},        // Near wraparound
    {"5 < 10 (normal)", 5, 10, true},              // Normal case
    {"equal", 100, 100, false},                    // Edge case
}
```

**Key packages with extensive table-driven tests:**
- `circular/`: Sequence arithmetic with wraparound (`seq_math_31bit_wraparound_test.go`, `seq_math_generic_test.go`)
- `packet/`: Packet marshaling (`packet_table_test.go`)
- `congestion/live/receive/`: Loss recovery, NAK generation (`loss_recovery_table_test.go`, `nak_consolidate_table_test.go`)
- `congestion/live/send/`: Sender delivery, ACK processing (`sender_ack_table_test.go`, `sender_tsbpd_table_test.go`)

### Regression Tests

When a bug is found, we preserve the broken implementation for documentation and add regression tests that:
1. **Document the bug**: Show what the broken behavior was
2. **Prove the fix works**: Verify the corrected implementation
3. **Prevent regression**: Fail if the bug is reintroduced

```go
// From circular/seq_math_31bit_wraparound_test.go

// SeqLessBroken is the OLD broken implementation preserved for documentation.
// DO NOT USE in production code. This is only for testing and documentation.
func SeqLessBroken(a, b uint32) bool { ... }

// TestRegression_SeqLessBroken_FailsAtWraparound documents the bug exists
func TestRegression_SeqLessBroken_FailsAtWraparound(t *testing.T) { ... }

// TestRegression_SeqLess_FixedAtWraparound proves the fix works
func TestRegression_SeqLess_FixedAtWraparound(t *testing.T) { ... }
```

### Boundary & Wraparound Testing

SRT's 31-bit sequence numbers require special attention at boundaries. Tests explicitly cover:
- **MAX→0 wraparound**: `SeqLess(MaxSeqNumber31, 0)` must return `true`
- **Near-boundary comparisons**: Values close to MAX compared with small values
- **All bit widths**: Generic functions tested at 16, 31, 32, and 64 bits

```bash
make test-circular        # Run all circular arithmetic tests
make bench-seqless        # Compare SeqLess implementations
```

### Benchmarks

Performance-critical code includes benchmarks to:
- **Compare implementations**: Measure before/after optimizations
- **Identify regressions**: Catch performance degradation
- **Guide optimization**: Find hotspots with `benchmem`

```bash
# Circular number comparisons
make bench-circular       # Lt vs LtBranchless benchmarks

# Receiver configurations
make bench-receiver       # Compare Original vs NakBtree vs NakBtreeFr
make bench-nak-btree      # NAK btree specific benchmarks

# Packet handling
make bench-packet         # Packet creation/marshaling
make bench-packet-pool    # sync.Pool performance
```

Benchmarks follow the pattern:
```go
func BenchmarkLt_Wraparound(b *testing.B) {
    a := New(max-100, max)
    vals := make([]Number, 1000)
    for i := range vals {
        vals[i] = New(uint32(i), max)
    }
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        for _, val := range vals {
            _ = a.Lt(val)
        }
    }
}
```

### Race Detection

Concurrent code is tested with Go's race detector. Race tests exercise:
- **EventLoop concurrency**: Real goroutines with tickers
- **Sequence wraparound under concurrency**: Multiple goroutines at MAX→0 boundary
- **Lock-free data structures**: Ring buffer operations

```bash
make test-race            # All receiver race tests
make test-race-eventloop  # EventLoop-specific race tests
make ci-race              # Full race detection (CI)
```

The `ci-race` target fails the build on any race detection, ensuring concurrent bugs are caught before merge.

### Fuzz Testing

Packet parsing uses Go's built-in fuzz testing to find edge cases in unmarshaling:

```bash
make fuzz                 # Run fuzz tests for 30s
```

```go
// From packet/packet_test.go
func FuzzPacket(f *testing.F) {
    f.Add("00000000c00000010000000000000000")  // Seed corpus
    f.Fuzz(func(t *testing.T, orig string) {
        // Unmarshal, re-marshal, verify round-trip
    })
}
```

### Test Audit Tools

Static analysis tools ensure test quality:

```bash
make audit-tests          # Analyze test files for table-driven conversion
make audit-coverage FILE=... # Check corner case coverage
make audit-corners-all    # Verify all table tests cover defined corners
```

### Tiered Test Execution

Tests are organized into tiers for efficient CI:

| Tier | Purpose | Tests | Time | When |
|------|---------|-------|------|------|
| Tier 1 | Core validation | ~50 | <3s | Every PR |
| Tier 2 | Extended coverage | ~200 | <15s | Daily CI |
| Tier 3 | Comprehensive | ~1080 | <60s | Nightly CI |

```bash
make test-stream-tier1    # Fast PR validation
make test-stream-tier2    # Daily coverage
make test-stream-tier3    # Nightly comprehensive
```

## Code Quality

```bash
make check              # Static analysis (sequence arithmetic safety)
make code-audit-seq     # Detect unsafe sequence number patterns
make audit-metrics      # Verify Prometheus metrics definitions
make lint               # staticcheck
make fmt                # gofmt
```

## CLI Flags System

GoSRT uses a centralized flag system in `contrib/common/flags.go` that provides consistent CLI configuration across all binaries (server, client, client-generator, performance tools).

### Architecture

**Key files:**
- `contrib/common/flags.go`: Central flag definitions, parsing, and config application
- `contrib/common/test_flags.sh`: Automated flag validation tests
- Each binary's `main.go`: Component-specific flags and `-testflags` mode

**How it works:**
1. `FlagSet` map tracks which flags were explicitly set by the user (via `flag.Visit()`)
2. `ParseFlags()` parses command-line arguments and populates `FlagSet`
3. `ApplyFlagsToConfig()` applies only explicitly-set flags to `srt.Config`
4. `ValidateFlagDependencies()` auto-enables required dependencies

```go
// In contrib/common/flags.go
var FlagSet = make(map[string]bool)

// Flag definition
Latency = flag.Int("latency", 0, "Maximum accepted transmission latency in milliseconds")

// In ApplyFlagsToConfig()
if FlagSet["latency"] {
    config.Latency = time.Duration(*Latency) * time.Millisecond
}
```

### Adding a New Flag

**Step 1: Add flag variable in `contrib/common/flags.go`**

```go
// In the var block with other flags
MyNewFlag = flag.Int("mynewflag", 0, "Description of what this flag does")
```

**Step 2: Add config application in `ApplyFlagsToConfig()`**

```go
// In the ApplyFlagsToConfig() function
if FlagSet["mynewflag"] {
    config.MyNewField = *MyNewFlag
}
```

**Step 3: Add test in `contrib/common/test_flags.sh`**

```bash
# Add a test case (pattern matches JSON output)
run_test "MyNewFlag flag" "-mynewflag 42" '"MyNewField" *: *42' "$SERVER_BIN"
```

**Step 4: Run flag tests**

```bash
make test-flags   # Validates all CLI flags are correctly parsed and applied
```

### Test Flags Mode

Each binary supports a `-testflags` mode that:
1. Parses all flags
2. Applies them to a default config
3. Prints the config as JSON
4. Exits (no actual server/client operation)

This enables automated testing without running full network operations:

```bash
# Example: test that -latency 200 sets Latency to 200ms (200000000 ns)
./contrib/client/client -testflags -latency 200
# Output: {"Latency": 200000000, ...}
```

### Known Limitations

**Boolean flags with false defaults**: Go's `flag.Visit()` only visits flags that changed from their default value. Setting `-drifttracer false` when the default is already `false` won't register as "set", so it won't override the config. This is a Go flag package limitation, not a bug.

### Flag Dependencies

Some flags have dependencies that are auto-enabled via `ValidateFlagDependencies()`:

```
-usesendeventloop → -usesendcontrolring → -usesendring → -usesendbtree
-useeventloop → -usepacketring
-userecvcontrolring → -useeventloop → -usepacketring
```

Warnings are printed when dependencies are auto-enabled.

## Architecture

### Package Structure

- **Root level**: Connection management (`connection.go`, `dial.go`, `listen.go`)
- **`circular/`**: 31-bit circular sequence number arithmetic with wraparound
- **`packet/`**: SRT packet marshaling/unmarshaling
- **`congestion/`**: Pluggable congestion control interfaces
- **`congestion/live/`**: LiveCC implementation (the main congestion control)
  - `send/`: Sender with lock-free EventLoop
  - `receive/`: Receiver with NAK btree, packet store
  - `common/`: Shared utilities including control rings
- **`crypto/`**: AES-CTR encryption
- **`metrics/`**: Prometheus metrics export
- **`contrib/`**: Example binaries (server, client, client-generator, performance tools)

### Two Execution Paths

1. **Tick-based** (traditional): Connection calls `Tick()` periodically via timer
2. **EventLoop-based** (lock-free): Continuous loop in dedicated goroutine
   - Enabled via `Config.UseEventLoop`
   - Lower latency, smoother CPU usage
   - Lock-free when possible, graceful fallback when ring full

### Lock-Free Architecture

Both sender and receiver leverage the `go-lock-free-ring` library (MPSC sharded ring) to achieve completely lock-free operation in the EventLoop hot path. The key insight is routing ALL data and control packets through lock-free rings so the EventLoop is the single consumer—eliminating all mutex contention.

**Sender Lock-Free Design** (`congestion/live/send/`):
- `SendPacketRing`: Data packets from `Push()` → ring → EventLoop drains to btree
- `SendControlRing`: ACK/NAK from io_uring handler → ring → EventLoop processes lock-free
- `SendPacketBtree`: O(log n) packet storage, accessed only by EventLoop (no locks needed)
- Eliminates bursty Tick() delivery—packets sent smoothly at TSBPD time

**Receiver Lock-Free Design** (`congestion/live/receive/`):
- `PacketRing`: Incoming data packets from io_uring → ring → EventLoop drains to btree
- `RecvControlRing`: ACKACK/KEEPALIVE from io_uring → ring → EventLoop processes lock-free
- All btree operations (`periodicACK`, `periodicNAK`, `deliverReadyPackets`) run lock-free in EventLoop

**Shared Infrastructure** (`congestion/live/common/`):
- `ControlRing[T]`: Generic lock-free ring used by both sender and receiver
- Thread-safety: `Push()` safe from multiple goroutines (io_uring handlers), `TryPop()` single consumer (EventLoop)
- Configurable shards for high-throughput scenarios

**Function Dispatch Pattern**: Both sender and receiver use function pointers configured at startup:
- EventLoop mode: calls lock-free versions (no locks, single-threaded btree access)
- Tick mode (fallback): calls locking wrapper versions for backward compatibility

See `documentation/lockless_sender_design.md` and `documentation/completely_lockfree_receiver.md` for full design details.

### Critical: Sequence Number Arithmetic

SRT sequence numbers are 31-bit values that wrap around. **Always use `circular.Number` for comparisons**.

```go
// CORRECT
if a.Lt(b) { ... }
if circular.Number(seq1).Lt(circular.Number(seq2)) { ... }

// WRONG - detected by code-audit-seq
if int32(a-b) < 0 { ... }  // Breaks at wraparound
if a < b { ... }           // Raw comparison fails at wraparound
```

The `make code-audit-seq` target detects unsafe patterns.

### Context Assertions (Debug Builds)

Functions that must run lock-free (EventLoop context) vs functions that may use locks (Tick context) are verified in debug builds:

```go
// In EventLoop path - no locks allowed
func (s *sender) processControlPacketsDelta() {
    AssertEventLoopContext()  // Panics in debug build if wrong context
    // ... lock-free code
}

// In Tick path - locks OK
func (s *sender) processControlPacketsLocked() {
    AssertTickContext()
    s.mu.Lock()
    // ...
}
```

Build with `make build-debug` to enable runtime context verification.

### Critical: Context and Cancellation Handling

This codebase uses a specific pattern for `context.Context` handling to enable graceful shutdown. See `documentation/context_and_cancellation_*.md` for full details.

**Key Rules:**

1. **Always inherit from parent context** - Never create contexts from `context.Background()` in library components. All contexts must inherit from the root context created in `main.go`.

```go
// CORRECT - inherit from parent
ctx, cancel := context.WithCancel(parentCtx)

// WRONG - breaks cancellation chain
ctx, cancel := context.WithCancel(context.Background())
```

2. **Context first argument** - Following Go convention, context should be the first parameter in functions.

```go
// CORRECT
func Dial(ctx context.Context, network, address string, ...) (Conn, error)

// WRONG
func Dial(network, address string, ctx context.Context, ...) (Conn, error)
```

3. **Check `ctx.Done()` FIRST** - Always check context cancellation in a separate select with default case BEFORE other channel operations. This prevents race conditions.

```go
// CORRECT - check context first
select {
case <-ctx.Done():
    return ctx.Err()
default:
}
// Then do other channel operations
select {
case data := <-dataChan:
    // process
case <-ctx.Done():
    return ctx.Err()
}

// WRONG - non-deterministic when both ready
select {
case <-ctx.Done():
    return ctx.Err()
case data := <-dataChan:  // May win even if ctx cancelled!
    // process
}
```

4. **Storage rules**:
   - **Long-lived structs** (Server, Listener, Dialer, Connection): Store context in struct field
   - **Short-lived operations**: Pass context as function parameter
   - **Goroutines**: Always pass context explicitly, even if stored in struct

5. **WaitGroup pattern**:
   - Call `wg.Add(1)` BEFORE starting goroutine (never inside)
   - Call `defer wg.Done()` as first line in goroutine
   - Wait for all children before calling parent's `Done()`

## Key Configuration Options

When testing or debugging, these `Config` fields affect behavior:

- `UseEventLoop`: Enable lock-free EventLoop instead of Tick-based processing
- `UseControlRing`: Enable lock-free control packet rings
- `UseIOURing`: Enable io_uring for network I/O
- `RecvRings`: Number of io_uring rings for receive path
- `NakBtreeF` / `NakBtreeFr`: NAK consolidation strategies

## Integration Testing

Integration tests use network namespaces to simulate real network conditions. Most require root.

### Quick Reference

```bash
# List available configurations
make test-isolation-list      # Single-component isolation tests
make test-network-list        # Network impairment tests
make test-parallel-list       # A/B comparison tests

# Performance tests (NO sudo required)
make test-performance         # AIMD search for max throughput
make test-performance-quick   # Quick sanity check
```

### Isolation Tests (require root)

Test individual features in isolation to identify performance bottlenecks.

```bash
# List configs
make test-isolation-list

# Run a single test
sudo make test-isolation CONFIG=Isolation-5M-CG-SendEventLoop

# With Prometheus metrics output
sudo make test-isolation CONFIG=Isolation-5M-CG-SendEventLoop PRINT_PROM=true

# With CPU profiling (uses debug builds automatically)
sudo PROFILES=cpu make test-isolation CONFIG=Isolation-5M-CG-SendEventLoop

# Run all isolation tests (~3.5 min)
sudo make test-isolation-all

# Sender-specific tests
sudo make test-isolation-sender-quick    # Quick sanity (~30s)
sudo make test-isolation-sender-phases   # Each feature in isolation (~6 min)
sudo make test-isolation-sender-all      # All sender tests (~15 min)
```

### Network Impairment Tests (require root)

Test behavior under packet loss, latency, and jitter.

```bash
# List configs
make test-network-list

# Run specific network condition
sudo make test-network CONFIG=Network-Loss2pct-5Mbps
sudo make test-network CONFIG=Network-Starlink-5Mbps VERBOSE=1

# Quick tests (2% and 5% loss)
sudo make test-network-quick

# All network tests
sudo make test-network-all
```

### Parallel Comparison Tests (require root)

Compare two configurations side-by-side (e.g., Baseline vs HighPerf).

```bash
# List configs
make test-parallel-list

# Run comparison
sudo make test-parallel CONFIG=Parallel-Starlink-5Mbps
sudo make test-parallel CONFIG=Parallel-Starlink-5Mbps VERBOSE=1

# With profiling
sudo PROFILES=cpu,mutex make test-parallel CONFIG=Parallel-Starlink-5Mbps

# With packet capture
sudo TCPDUMP_SERVER=/tmp/server.pcap make test-parallel CONFIG=...

# Lockless sender tests
sudo make test-parallel-sender           # Clean network, 20 Mb/s
sudo make test-parallel-sender-high      # 50 Mb/s
sudo make test-parallel-sender-starlink  # Starlink impairment
sudo make test-parallel-sender-all       # All 5 sender tests
```

### Performance Tests (NO sudo required)

Automated AIMD search to find maximum sustainable throughput.

```bash
# Default search (builds binaries automatically)
make test-performance

# Custom parameters
make test-performance INITIAL=100M MAX=400M STEP=10M FC=204800

# Quick sanity check
make test-performance-quick

# Full 500 Mb/s target test (10+ minutes)
make test-performance-500

# Dry run (validate config only)
make test-performance-dry-run
```

**Performance test options:**
- `INITIAL=200M` - Starting bitrate
- `MAX=600M` - Maximum bitrate to test
- `STEP=10M` - Additive increase step
- `PRECISION=5M` - Search precision
- `FC=102400` - Flow control window
- `RECV_RINGS=2` - Number of receive io_uring rings
- `VERBOSE=true` - Verbose output
- `JSON=true` - JSON output

### Matrix Tests

Comprehensive test matrices combining multiple configurations.

```bash
# With network impairment (require root)
sudo make test-matrix-tier1    # ~25 tests, ~40 min
sudo make test-matrix-tier2    # ~42 tests, ~70 min

# Clean network only (NO root required)
make test-clean-matrix-tier1   # ~14 tests, ~4 min
make test-clean-matrix-tier2   # ~24 tests, ~6 min
```

### Go Profiling & Automated Analysis

Integration tests support comprehensive Go profiling with automated analysis and comparison reporting.

**Enabling Profiling:**

```bash
# Single profile type
sudo PROFILES=cpu make test-isolation CONFIG=Isolation-5M-CG-SendEventLoop

# Multiple profile types
sudo PROFILES=cpu,mutex make test-parallel CONFIG=Parallel-Starlink-5Mbps

# All profile types (cpu, mutex, block, heap, allocs, thread, goroutine)
sudo PROFILES=all make test-parallel CONFIG=Parallel-Clean-20M-Base-vs-SendEL
```

**Available Profile Types:**

| Type | What it measures | Use for |
|------|------------------|---------|
| `cpu` | CPU time per function | Finding hot paths, optimization targets |
| `mutex` | Lock contention time | Identifying lock bottlenecks |
| `block` | Blocking operations (I/O, channels) | Channel/syscall overhead |
| `heap` | Memory in use | Memory leaks, large allocations |
| `allocs` | Allocation counts | GC pressure, object pooling candidates |
| `thread` | OS thread creation | Goroutine explosion detection |
| `goroutine` | Active goroutines | Leak detection, concurrency issues |

**Automated Analysis:**

When profiling is enabled, the integration test framework automatically:

1. **Runs `go tool pprof`** on each profile to extract top functions
2. **Generates flame graph SVGs** for visual analysis (requires graphviz)
3. **Parses results** to identify optimization opportunities
4. **Generates recommendations** based on common patterns:
   - Channel overhead (`chanrecv`/`chansend`) → suggest io_uring or buffered channels
   - Lock contention (`Mutex`/`Lock`) → suggest lock-free structures or sharding
   - GC pressure (`mallocgc`/`gcBgMarkWorker`) → suggest object pooling
   - Syscall overhead → suggest io_uring or batching

**Comparison Reports (Parallel Tests):**

For parallel tests (Baseline vs HighPerf), the analyzer compares profiles side-by-side:

```bash
sudo PROFILES=cpu,mutex make test-parallel CONFIG=Parallel-Clean-20M-Base-vs-SendEL
```

Generates:
- **HTML Report** (`/tmp/profile_<test>_<timestamp>/report.html`) - Interactive dashboard with:
  - Performance improvement metrics (CPU, memory, lock contention, block time)
  - Function-by-function comparison tables
  - Flame graph links
  - Optimization recommendations
- **JSON Data** (`report.json`) - Machine-readable for CI integration
- **Text Summary** (`summary.txt`) - Quick terminal-friendly overview

**Example Analysis Output:**

```
╔══════════════════════════════════════════════════════════════════════════════╗
║ SERVER CPU COMPARISON                                                         ║
╠══════════════════════════════════════════════════════════════════════════════╣
║ Function                                    Baseline  HighPerf     Delta      ║
║ ──────────────────────────────────────────────────────────────────────────── ║
║ runtime.chanrecv                              12.3%      2.1%    -82.9% ⬇    ║
║ sync.(*Mutex).Lock                             8.7%      0.3%    -96.6% ⬇    ║
║ syscall.Syscall                                6.2%      1.8%    -71.0% ⬇    ║
╠══════════════════════════════════════════════════════════════════════════════╣
║ RECOMMENDATIONS:                                                              ║
║ • Lock contention (0.3%): Consider lock-free structures or sharding          ║
╚══════════════════════════════════════════════════════════════════════════════╝
```

**Manual Profile Analysis:**

```bash
# Interactive web UI
go tool pprof -http=:8080 /tmp/profile_test/server/cpu.pprof

# Top functions (terminal)
go tool pprof -top /tmp/profile_test/server/cpu.pprof

# With debug binary for better symbols
go tool pprof -http=:8080 ./contrib/server/server-debug /tmp/profile_test/server/cpu.pprof
```

**Key Files:**
- `contrib/integration_testing/profiling.go`: Profile configuration and collection
- `contrib/integration_testing/profile_analyzer.go`: Automated analysis and comparison
- `contrib/integration_testing/profile_report.go`: HTML/JSON report generation

### io_uring Implementation

GoSRT uses io_uring for high-performance network I/O on Linux. See `documentation/IO_Uring*.md` for full design docs.

**Key Implementation Files:**
- `listen_linux.go`: Receive path (listener-level shared ring(s))
- `connection_linux.go`: Send path (per-connection ring(s))

**Read Path Architecture (Listener):**
- **Ring scope**: Shared ring(s) at listener level (one UDP socket for all connections)
- **Modes**: Single-ring (`recvRing`) or multi-ring (`recvRingStates[]`)
- **Goroutines per ring**:
  1. Completion handler (`recvCompletionHandler` / `recvCompletionHandlerIndependent`) - blocks in `WaitCQETimeout()` for completions
  2. Same goroutine handles resubmission after processing each completion
- **Batched submissions**: `submitRecvRequestBatch()` reduces syscall overhead by batching multiple `PrepareRecvMsg` operations
- **Pre-population**: At startup, ring is pre-populated with pending receives (default: full ring size)
- **Zero-copy**: Uses `UnmarshalZeroCopy()` to avoid buffer copying; buffer returned to pool after packet delivery

```go
// Read path flow:
// 1. Pre-populate: submitRecvRequestBatch(initialPending) fills ring with RecvMsg SQEs
// 2. Wait: WaitCQETimeout() blocks until completion or timeout
// 3. Process: processRecvCompletion() deserializes packet, routes to connection
// 4. Resubmit: After batch threshold, submitRecvRequestBatch() replenishes ring
```

**Write Path Architecture (Connection):**
- **Ring scope**: Per-connection ring(s) (each connection has dedicated ring(s))
- **Modes**: Single-ring (`sendRing`) or multi-ring (`sendRingStates[]` with round-robin)
- **Goroutines per ring**:
  1. Sender (caller's goroutine) - `send()` / `sendMultiRing()` submits packets
  2. Completion handler (`sendCompletionHandler` / `sendCompletionHandlerIndependent`) - blocks in `WaitCQETimeout()` for completions
- **Pre-computed sockaddr**: Remote address converted once at connection init, reused for all sends
- **Per-connection buffer pool**: `sync.Pool` of `*bytes.Buffer` eliminates lock contention

```go
// Write path flow:
// 1. Marshal: packet.Marshal(sendBuffer) serializes into pooled buffer
// 2. Submit: ring.GetSQE() + PrepareSendMsg() + ring.Submit()
// 3. Wait: Completion handler's WaitCQETimeout() receives completion
// 4. Cleanup: Return buffer to pool; data packets stay in btree for potential retransmit
```

**Configuration Options:**
- `IoUringRecvEnabled` / `IoUringEnabled`: Enable io_uring for receive/send paths
- `IoUringRecvRingSize` / `IoUringSendRingSize`: Ring size (must be power of 2)
- `IoUringRecvRingCount` / `IoUringSendRingCount`: Number of parallel rings (multi-ring mode)
- `IoUringRecvBatchSize`: Batch size for resubmitting read requests

### B-Tree Packet Storage

GoSRT uses `github.com/google/btree` (generic btree) extensively for packet buffering and loss recovery. All btrees use `circular.SeqLess()` comparators for correct 31-bit sequence number ordering with wraparound.

**Sender: SendPacketBtree** (`congestion/live/send/send_packet_btree.go`):
- Stores outgoing data packets keyed by sequence number
- Replaces the old `packetList` + `lossList` linked list design (O(n) → O(log n))
- **ACK processing**: `DeleteMin()` removes acknowledged packets up to ACK sequence
- **NAK processing**: `Get(seq)` for O(log n) retransmission lookup (vs O(n) linear scan)
- **Delivery**: `Ascend()` iterates packets ready for TSBPD delivery
- Single-traversal `ReplaceOrInsert` pattern avoids duplicate traversals

**Receiver: btreePacketStore** (`congestion/live/receive/packet_store_btree.go`):
- Stores incoming data packets, automatically ordered by sequence number
- Implements `packetStore` interface (switchable with list via `PacketReorderAlgorithm` config)
- **TSBPD delivery**: `RemoveAll()` delivers packets when their TSBPD time arrives
- **Gap detection**: `IterateFrom(startSeq)` provides O(log n) seek for NAK generation
- **Duplicate detection**: `Insert()` returns `(inserted, duplicatePacket)` in single traversal

**Receiver: NAK Btree** (`congestion/live/receive/nak_btree.go`):
- Stores **missing** sequence numbers (gaps in received stream) for NAK generation
- Separate from packet btree with its own lock (different access patterns)
- **Entry structure**: `NakEntryWithTime{Seq, TsbpdTimeUs, LastNakedAtUs, NakCount}`
- **RTO suppression**: Tracks `LastNakedAtUs` to avoid redundant NAKs within RTO window
- **TSBPD-aware expiry**: Entries expire based on `TsbpdTimeUs`, not just sequence
- **io_uring reordering tolerance**: Suppresses immediate NAK; periodic NAK handles gaps
- **Consolidation**: Converts singles to ranges for efficient NAK packets (e.g., 100 singles → 1 range)

**Why Btrees?**
- O(log n) vs O(n) for all operations (critical at 500+ Mb/s with thousands of packets in flight)
- Automatic ordering by sequence number (no manual sorting)
- Efficient range operations (`AscendRange`, `DeleteMin`)
- Memory-efficient for large packet counts

See `documentation/btree_*.md` and `documentation/design_nak_btree*.md` for full design details.

## Nix Flake Infrastructure

GoSRT includes a Nix flake for MicroVM-based integration testing. See `documentation/nix_microvm_design.md` and `documentation/nix_microvm_implementation_plan.md` for full details.

### Nix Commands

```bash
# Validate flake
nix flake check --no-build

# Show flake outputs
nix flake show

# Evaluate library exports
nix eval .#lib.serverIp      # "10.50.3.2"
nix eval .#lib.roleNames     # All 8 role names

# Build packages
nix build .#gosrt-debug      # Debug build with assertions
nix build .#gosrt-prod       # Production build
nix build .#gosrt-perf       # Performance build with pprof

# Enter development shell
nix develop
```

### MicroVM Management Commands

```bash
# Network setup (requires sudo)
sudo nix run .#srt-network-setup -- "$USER"    # Create namespaces, bridges, TAPs
sudo nix run .#srt-network-teardown            # Remove all network resources

# VM lifecycle
nix run .#srt-tmux-all                 # Start all VMs in tmux session
nix run .#srt-tmux-attach              # Attach to tmux session
nix run .#srt-tmux-clear               # Kill tmux session (without stopping VMs)
nix run .#srt-vm-stop                  # Stop all VMs (tmux session persists)
nix run .#srt-vm-stop-and-clear-tmux   # Stop all VMs AND kill tmux session (clean restart)
nix run .#srt-vm-check                 # Show VM status (running/stopped)
nix run .#srt-vm-wait                  # Wait for VMs to be ready (SSH accessible)

# Per-VM access
nix run .#srt-ssh-server               # SSH into server VM (password: srt)
nix run .#srt-ssh-publisher            # SSH into publisher VM
nix run .#srt-ssh-subscriber           # SSH into subscriber VM
nix run .#srt-console-server           # Serial console (Ctrl+C to disconnect)

# Integration tests
nix run .#srt-integration-smoke        # Quick health check
nix run .#srt-integration-basic        # VM and service verification
nix run .#srt-integration-full         # Complete test suite
```

**Typical workflow:**
```bash
# Fresh start
sudo nix run .#srt-network-setup -- "$USER"
nix run .#srt-tmux-all
nix run .#srt-vm-wait
nix run .#srt-integration-full

# Clean restart (after making changes)
nix run .#srt-vm-stop-and-clear-tmux   # Stops VMs and clears tmux
nix run .#srt-tmux-all                 # Start fresh
```

### CRITICAL: Never Use `--impure`

**NEVER use `--impure` or `builtins.getFlake` in Nix commands.** These break reproducibility and are not idiomatic Nix.

```bash
# WRONG - breaks reproducibility
nix eval --impure --expr '(builtins.getFlake "...").lib.serverIp'

# CORRECT - pure evaluation
nix eval .#lib.serverIp
```

If you find yourself needing `--impure`, the flake structure is wrong. Fix the flake instead:
- Library exports should be at flake top-level (not per-system)
- Use `specialArgs` to pass values to NixOS modules
- Use overlays for package customization

### Key Nix Files

| File | Purpose |
|------|---------|
| `flake.nix` | Main entry point, defines outputs |
| `nix/constants.nix` | Role definitions, base config |
| `nix/lib.nix` | Computed values (IPs, MACs, ports) |
| `nix/overlays/gosrt.nix` | Binary flavors (prod, debug, perf) |
| `nix/modules/srt-test.nix` | Impairment scenarios |
| `nix/modules/srt-network.nix` | Declarative nftables |

### Nix Idioms to Follow

1. **Data-driven**: Define facts in `constants.nix`, compute everything else in `lib.nix`
2. **No hardcoding**: IPs, MACs, ports derived from role index
3. **Declarative**: Use NixOS modules for config, not shell scripts
4. **Pure**: No `--impure`, no `builtins.getFlake`, no `import <nixpkgs>`
5. **Reproducible**: Pin inputs in `flake.lock`, use `vendorHash` for Go

### Shell Script Rules (writeShellApplication)

**NEVER use `# shellcheck disable=` directives.** Always fix the actual problem.

Common fixes:
- **SC2029** (variable expands client-side): Use `'single quotes'` or pass variables separately
- **SC2086** (word splitting): Quote variables `"$VAR"` or use arrays
- **SC2046** (word splitting in command substitution): Quote `"$(command)"`

```bash
# WRONG - disabling shellcheck
# shellcheck disable=SC2029
ssh host "command $VAR"

# CORRECT - pass variable properly
ssh host "command '$VAR'"
# or
ssh host 'command '"'$VAR'"
# or for complex cases, use heredoc or pass as argument
```

**Nix string escaping for bash**: In `writeShellApplication` text blocks, bash parameter expansion like `${var%pattern}` or `${var#pattern}` conflicts with Nix's `${...}` interpolation. Escape with `''${...}`:

```nix
# WRONG - Nix interprets ${gateway%.1} as interpolation, causing syntax error
host_ip="${gateway%.1}.254"

# CORRECT - ''$ escapes the $ for bash
host_ip="''${gateway%.1}.254"
```

## Key Dependencies

- `github.com/google/btree`: Packet storage, NAK tracking (generic btree with typed comparators)
- `github.com/randomizedcoder/giouring`: io_uring Go wrapper
- `github.com/randomizedcoder/go-lock-free-ring`: Lock-free queue for control packets

## Metrics System

GoSRT implements a custom high-performance metrics system using `atomic.Uint64`/`atomic.Int64` counters instead of the standard `prometheus/promauto` package.

**Why custom metrics (not promauto)?**
- **Lock-free**: Atomic counters have zero lock contention in hot paths
- **Readable values**: Unlike promauto, atomic counters can be read within the code (e.g., for rate calculations, conditional logic)
- **No double-collection**: Single source of truth—atomics are read directly by the Prometheus handler

**Key files:**
- `metrics/metrics.go`: `ConnectionMetrics` struct with all atomic counters
- `metrics/handler.go`: Custom `/metrics` HTTP handler (Prometheus text format)
- `metrics/helpers.go`: Increment helper functions

**Usage patterns:**
```go
// Single increment (lock-free)
c.metrics.PktRecvDataSuccess.Add(1)

// Read value (for internal use)
count := c.metrics.PktRecvDataSuccess.Load()

// Loop optimization: accumulate locally, then single atomic at end
var lossCount uint64
for _, entry := range entries {
    if entry.IsLoss() {
        lossCount++
    }
}
if lossCount > 0 {
    c.metrics.CongestionRecvPktLoss.Add(lossCount)  // One atomic op, not N
}
```

### Metrics Audit (CRITICAL)

**ALWAYS run `make audit-metrics` when modifying metrics.** The audit tool (`tools/metrics-audit/main.go`) uses Go AST parsing to ensure:

1. **No undefined increments**: Every `.Add()/.Store()` call references a field that exists in `ConnectionMetrics`
2. **No unused definitions**: Every field in `ConnectionMetrics` is actually incremented somewhere
3. **No unexported metrics**: Every incremented metric is exported via the Prometheus handler

```bash
make audit-metrics   # Run before committing any metrics changes
```

**Audit failure = CI failure.** If you add a new metric, you must:
1. Add the field to `ConnectionMetrics` in `metrics/metrics.go`
2. Add `.Add()` calls where the metric should be incremented
3. Add the metric export in `metrics/handler.go`

See `documentation/metrics_*.md` for full design details.
