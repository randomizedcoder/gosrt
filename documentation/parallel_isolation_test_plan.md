# Parallel Isolation Test Plan

**Purpose**: Systematically isolate which component/feature causes the excessive gap detection in the HighPerf pipeline.

**Methodology**:
- Control pipeline: No io_uring, linked list (baseline behavior)
- Test pipeline: Change exactly ONE variable from control
- Compare metrics between control and test
- **No network impairment** - use clean path (0 loss, 0 latency) to isolate code behavior
- **No Client (subscriber)** - just CG → Server to reduce variables
- **30 second tests** - differences are significant enough to show quickly

## Test Matrix

### Phase 1: io_uring and Packet Store Isolation (Tests 0-6)

| Test # | Name | Variable Changed | Control Config | Test Config |
|--------|------|------------------|----------------|-------------|
| 0 | Control-Control | None (baseline) | list, no io_uring | list, no io_uring |
| 1 | CG-IoUringSend | CG io_uring send | list, no io_uring | list, io_uring send only |
| 2 | CG-IoUringRecv | CG io_uring recv | list, no io_uring | list, io_uring recv only |
| 3 | CG-Btree | CG packet store | list, no io_uring | btree, no io_uring |
| 4 | Server-IoUringSend | Server io_uring send | list, no io_uring | list, io_uring send only |
| 5 | Server-IoUringRecv | Server io_uring recv | list, no io_uring | list, io_uring recv only |
| 6 | Server-Btree | Server packet store | list, no io_uring | btree, no io_uring |

### Phase 2: NAK btree Isolation (Tests 7-10)

The NAK btree is a new mechanism for loss detection and NAK generation. These tests isolate the NAK btree from other features:

| Test # | Name | Variable Changed | Description |
|--------|------|------------------|-------------|
| 7 | Server-NakBtree | Server NAK btree | NAK btree replaces lossList scan for gap detection |
| 8 | Server-NakBtree-IoUringRecv | Server NAK btree + io_uring recv | Combined receiver path - realistic high-perf scenario |
| 9 | CG-HonorNakOrder | CG HonorNakOrder | Sender retransmits in NAK packet order (receiver priority) |
| 10 | FullNakBtree | Both NAK btree + HonorNakOrder | Full NAK btree pipeline (server NAK btree + CG honor order) |

**NAK btree vs Packet Store btree** (IMPORTANT DISTINCTION):
- **Packet store btree** (Test 6): Stores received packets in sorted order for O(log n) insertion/lookup
- **NAK btree** (Tests 7-10): Tracks missing sequence numbers for the NAK mechanism (completely different feature)

The NAK btree enables:
- `UseNakBtree`: Use btree instead of lossList for tracking missing sequences
- `SuppressImmediateNak`: Let periodic NAK handle gaps (prevents false positives with io_uring reordering)
- `FastNakEnabled`: Trigger NAK immediately after detecting burst loss
- `FastNakRecentEnabled`: Detect sequence jumps for burst loss detection
- `HonorNakOrder`: Sender retransmits in the order specified by the NAK packet

## Test Architecture (Simplified)

```
ns_publisher                     ns_server
┌─────────────┐                 ┌─────────────┐
│ CG-Control  │────────────────▶│ Server-Ctrl │  Control Pipeline
│ (list, no   │     clean       │ (list, no   │
│  io_uring)  │     path        │  io_uring)  │
└─────────────┘                 └─────────────┘

┌─────────────┐                 ┌─────────────┐
│ CG-Test     │────────────────▶│ Server-Test │  Test Pipeline
│ (varied)    │     clean       │ (varied)    │
│             │     path        │             │
└─────────────┘                 └─────────────┘
```

No Client/Subscriber - we're only measuring CG→Server path.

## Configuration Details

### Test 0: Control-Control (Sanity Check)
Both pipelines identical - should show ~0% difference in all metrics.

```
Control: CG(list, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
Test:    CG(list, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
```

### Test 1: CG-IoUringSend
Only the client-generator uses io_uring for sending data packets.

```
Control: CG(list, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
Test:    CG(list, io_uring SEND only) → Server(list, no io_uring) → Client(list, no io_uring)
```

**CLI difference for Test pipeline CG:**
```
-iouringenabled (enables io_uring for send path)
# Note: -iouringrecvenabled is NOT set, so receive path is normal
```

### Test 2: CG-IoUringRecv
Only the client-generator uses io_uring for receiving control packets (ACKs, NAKs).

```
Control: CG(list, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
Test:    CG(list, io_uring RECV only) → Server(list, no io_uring) → Client(list, no io_uring)
```

**CLI difference for Test pipeline CG:**
```
-iouringrecvenabled (enables io_uring for recv path)
# Note: -iouringenabled is NOT set, so send path is normal
```

### Test 3: CG-Btree
Only the client-generator uses btree packet store (instead of linked list).

```
Control: CG(list, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
Test:    CG(btree, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
```

**CLI difference for Test pipeline CG:**
```
-packetreorderalgorithm btree -btreedegree 32
```

### Test 4: Server-IoUringSend
Only the server uses io_uring for sending data packets (to subscriber).

```
Control: CG(list, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
Test:    CG(list, no io_uring) → Server(list, io_uring SEND only) → Client(list, no io_uring)
```

**CLI difference for Test pipeline Server:**
```
-iouringenabled
```

### Test 5: Server-IoUringRecv
Only the server uses io_uring for receiving (from publisher).

```
Control: CG(list, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
Test:    CG(list, no io_uring) → Server(list, io_uring RECV only) → Client(list, no io_uring)
```

**CLI difference for Test pipeline Server:**
```
-iouringrecvenabled
```

### Test 6: Server-Btree
Only the server uses btree packet store.

```
Control: CG(list, no io_uring) → Server(list, no io_uring) → Client(list, no io_uring)
Test:    CG(list, no io_uring) → Server(btree, no io_uring) → Client(list, no io_uring)
```

**CLI difference for Test pipeline Server:**
```
-packetreorderalgorithm btree -btreedegree 32
```

### Test 7: Server-NakBtree
Only the server uses NAK btree for gap detection.

```
Control: CG(list, no io_uring, original NAK) → Server(list, no io_uring, original NAK)
Test:    CG(list, no io_uring, original NAK) → Server(list, no io_uring, NAK btree)
```

**CLI difference for Test pipeline Server:**
```
-usenakbtree -fastnakenabled -fastnakrecentenabled -honornakorder
```
Note: `SuppressImmediateNak` is auto-set internally when `-usenakbtree` is enabled.

### Test 8: Server-NakBtree-IoUringRecv
Server uses NAK btree + io_uring recv (realistic high-performance receiver).

```
Control: CG(list, no io_uring, original NAK) → Server(list, no io_uring, original NAK)
Test:    CG(list, no io_uring, original NAK) → Server(list, io_uring recv, NAK btree)
```

**CLI difference for Test pipeline Server:**
```
-iouringrecvenabled -usenakbtree -fastnakenabled -fastnakrecentenabled -honornakorder
```
Note: `SuppressImmediateNak` is auto-set internally when `-usenakbtree` or `-iouringrecvenabled` is enabled.

### Test 9: CG-HonorNakOrder
Only the client-generator uses HonorNakOrder (sender-side feature).

```
Control: CG(list, no io_uring, original NAK) → Server(list, no io_uring, original NAK)
Test:    CG(list, no io_uring, HonorNakOrder) → Server(list, no io_uring, original NAK)
```

**CLI difference for Test pipeline CG:**
```
-honornakorder
```

### Test 10: FullNakBtree
Full NAK btree pipeline: Server NAK btree + CG HonorNakOrder.

```
Control: CG(list, no io_uring, original NAK) → Server(list, no io_uring, original NAK)
Test:    CG(list, no io_uring, HonorNakOrder) → Server(list, no io_uring, NAK btree)
```

**CLI differences:**
- Test Server: `-usenakbtree -fastnakenabled -fastnakrecentenabled -honornakorder`
- Test CG: `-honornakorder`
Note: `SuppressImmediateNak` is auto-set internally when `-usenakbtree` is enabled.

## Implementation Plan

### Step 1: Add Granular SRT Config Options

Update `SRTConfig` struct to support individual io_uring path control:

```go
type SRTConfig struct {
    // ... existing fields ...

    // io_uring granular control
    IoUringSendEnabled bool // io_uring for send path only
    IoUringRecvEnabled bool // io_uring for recv path only

    // Packet reorder algorithm
    PacketReorderAlgorithm string // "list" or "btree"
    BTreeDegree            int
}
```

### Step 2: Update Flag Generation

Update `ToCliFlags()` to generate correct flags:

```go
func (c SRTConfig) ToCliFlags() []string {
    var flags []string

    // io_uring flags
    if c.IoUringSendEnabled {
        flags = append(flags, "-iouringenabled")
    }
    if c.IoUringRecvEnabled {
        flags = append(flags, "-iouringrecvenabled")
    }

    // Packet store algorithm
    if c.PacketReorderAlgorithm != "" {
        flags = append(flags, "-packetreorderalgorithm", c.PacketReorderAlgorithm)
        if c.PacketReorderAlgorithm == "btree" && c.BTreeDegree > 0 {
            flags = append(flags, "-btreedegree", strconv.Itoa(c.BTreeDegree))
        }
    }

    return flags
}
```

### Step 3: Create Helper Functions for Configs

```go
// BaseControlConfig returns the control config (list, no io_uring)
func BaseControlConfig() SRTConfig {
    return SRTConfig{
        ConnectionTimeout:      3000 * time.Millisecond,
        PeerIdleTimeout:        30000 * time.Millisecond,
        Latency:                3000 * time.Millisecond,
        TLPktDrop:              true,
        PacketReorderAlgorithm: "list",
        IoUringSendEnabled:     false,
        IoUringRecvEnabled:     false,
    }
}

// WithIoUringSend returns config with io_uring send enabled
func (c SRTConfig) WithIoUringSend() SRTConfig {
    c.IoUringSendEnabled = true
    return c
}

// WithIoUringRecv returns config with io_uring recv enabled
func (c SRTConfig) WithIoUringRecv() SRTConfig {
    c.IoUringRecvEnabled = true
    return c
}

// WithBtree returns config with btree packet store
func (c SRTConfig) WithBtree(degree int) SRTConfig {
    c.PacketReorderAlgorithm = "btree"
    c.BTreeDegree = degree
    return c
}
```

### Step 4: Create Isolation Test Configurations

```go
var IsolationTestConfigs = []ParallelTestConfig{
    // Test 0: Control-Control (sanity check)
    {
        Name:        "Isolation-Control",
        Description: "Both pipelines identical (sanity check)",
        Baseline:    PipelineConfig{SRT: BaseControlConfig()},
        HighPerf:    PipelineConfig{SRT: BaseControlConfig()},
        // ... common settings ...
    },

    // Test 1: CG io_uring send only
    {
        Name:        "Isolation-CG-IoUringSend",
        Description: "Client-Generator: io_uring send path only",
        Baseline:    PipelineConfig{SRT: BaseControlConfig()},
        HighPerf:    PipelineConfig{
            SRT: BaseControlConfig().WithIoUringSend(),
            // Only CG gets this config
        },
    },

    // ... Tests 2-6 ...
}
```

### Step 5: Add Component-Specific Config Override

Currently `ParallelTestConfig` has single SRT config per pipeline. We need component-level override:

```go
type PipelineConfig struct {
    // Base SRT config for all components
    SRT SRTConfig

    // Component-specific overrides (if different from base)
    ClientGeneratorSRT *SRTConfig // nil = use base SRT
    ServerSRT          *SRTConfig // nil = use base SRT
    ClientSRT          *SRTConfig // nil = use base SRT
}
```

### Step 6: Add Automated Batch Runner

Create a shell script wrapper that runs all 7 tests and captures output:

```bash
#!/bin/bash
# contrib/integration_testing/run_isolation_tests.sh

set -e

TESTS=(
    "Isolation-Control"
    "Isolation-CG-IoUringSend"
    "Isolation-CG-IoUringRecv"
    "Isolation-CG-Btree"
    "Isolation-Server-IoUringSend"
    "Isolation-Server-IoUringRecv"
    "Isolation-Server-Btree"
)

# Create output directory
OUTPUT_DIR=$(mktemp -d /tmp/isolation_tests_XXXXXX)
echo "Output directory: $OUTPUT_DIR"

# Run all tests sequentially
for i in "${!TESTS[@]}"; do
    TEST="${TESTS[$i]}"
    OUTPUT_FILE="$OUTPUT_DIR/test${i}_${TEST}.log"
    echo ""
    echo "=== Running Test $i: $TEST ==="
    echo "Output: $OUTPUT_FILE"

    sudo make test-isolation CONFIG="$TEST" 2>&1 | tee "$OUTPUT_FILE"

    echo "=== Test $i Complete ==="
done

echo ""
echo "============================================"
echo "All tests complete. Output in: $OUTPUT_DIR"
echo "============================================"
echo ""

# Generate summary
echo "=== SUMMARY ===" | tee "$OUTPUT_DIR/SUMMARY.txt"
for i in "${!TESTS[@]}"; do
    TEST="${TESTS[$i]}"
    OUTPUT_FILE="$OUTPUT_DIR/test${i}_${TEST}.log"

    # Extract key metrics from each test output
    echo "" | tee -a "$OUTPUT_DIR/SUMMARY.txt"
    echo "Test $i: $TEST" | tee -a "$OUTPUT_DIR/SUMMARY.txt"
    grep -E "(gaps|Gaps|GAPS|lost_total|retrans)" "$OUTPUT_FILE" | tail -10 | tee -a "$OUTPUT_DIR/SUMMARY.txt"
done
```

### Step 7: Add Makefile Targets

```makefile
## test-isolation: Run a single isolation test
test-isolation: server client-generator
	@cd contrib/integration_testing && go run . isolation-test $(CONFIG)

## test-isolation-all: Run all 7 isolation tests with output capture
test-isolation-all: server client-generator
	@echo "Running all isolation tests (7 tests × 30s = ~3.5 minutes)"
	@chmod +x contrib/integration_testing/run_isolation_tests.sh
	@contrib/integration_testing/run_isolation_tests.sh
```

## Expected Results (Clean Network)

With **no network impairment**, we expect:

### Phase 1: io_uring and Packet Store Isolation

| Test | Variable | Gaps (Control) | Gaps (Test) | Expected Diff |
|------|----------|----------------|-------------|---------------|
| 0 | None | 0 | 0 | 0 (sanity) |
| 1 | CG io_uring send | 0 | ? | 0 if send path is OK |
| 2 | CG io_uring recv | 0 | ? | 0 if recv path is OK |
| 3 | CG btree | 0 | ? | 0 if btree is OK |
| 4 | Server io_uring send | 0 | ? | 0 if send path is OK |
| 5 | Server io_uring recv | 0 | ? | 0 if recv path is OK |
| 6 | Server btree | 0 | ? | 0 if btree is OK |

### Phase 2: NAK btree Isolation

| Test | Variable | Gaps (Control) | Gaps (Test) | Expected Diff |
|------|----------|----------------|-------------|---------------|
| 7 | Server NAK btree | 0 | ? | 0 if NAK btree is OK |
| 8 | Server NAK btree + io_uring recv | 0 | ? | 0 if combined path is OK |
| 9 | CG HonorNakOrder | 0 | ? | 0 (HonorNakOrder is retransmit-order only) |
| 10 | Full NAK btree | 0 | ? | 0 if full pipeline is OK |

**Any non-zero gaps on clean network = code bug!**

### NAK btree Specific Metrics to Watch

In addition to gaps, the NAK btree tests should show these metrics in the Test pipeline:

| Metric | Test 7-10 Expected | Meaning |
|--------|-------------------|---------|
| `gosrt_nak_btree_inserts_total` | 0 on clean | NAK btree insertions (should be 0 with no loss) |
| `gosrt_nak_periodic_btree_runs_total` | > 0 | Periodic NAK scans running |
| `gosrt_nak_periodic_original_runs_total` | 0 | Original path disabled |
| `gosrt_nak_fast_triggers_total` | 0 on clean | FastNAK triggers (should be 0 with no loss) |
| `gosrt_nak_honored_order_total` | 0 on clean | NAKs processed in honor order (0 with no loss) |

## Phase 3: Client Isolation (if needed)

If CG and Server tests don't reveal the issue, add Client tests:

| Test # | Name | Variable Changed |
|--------|------|------------------|
| 11 | Client-IoUringRecv | Client io_uring recv |
| 12 | Client-IoUringOutput | Client io_uring output (to /dev/null) |
| 13 | Client-Btree | Client btree packet store |

## Decisions Made

1. **Test Duration**: 30 seconds per test
2. **Network Impairment**: None (clean path) - any gaps = code bug
3. **Architecture**: CG → Server only (no Client)
4. **Automation**: Shell script wrapper with output capture to temp files
5. **Total Runtime**: ~6.5 minutes for all 11 tests (original 7 + NAK btree 4)

## File Changes Required

1. `contrib/integration_testing/config.go` - Add granular SRT config options
2. `contrib/integration_testing/test_configs.go` - Add isolation test configs
3. `contrib/integration_testing/test_isolation_mode.go` - New file for simplified CG→Server tests
4. `contrib/integration_testing/test_graceful_shutdown.go` - Add `isolation-test` command dispatch
5. `contrib/integration_testing/run_isolation_tests.sh` - Batch runner script
6. `Makefile` - Add isolation test targets

## Simplified Test Mode

The isolation tests use a simpler architecture than full parallel tests:

```go
type IsolationTestConfig struct {
    Name        string
    Description string

    // Control pipeline (always list, no io_uring)
    ControlCG     SRTConfig
    ControlServer SRTConfig

    // Test pipeline (one variable changed)
    TestCG     SRTConfig
    TestServer SRTConfig

    // Test settings
    TestDuration time.Duration // 30s
    Bitrate      int64         // 5 Mb/s
}
```

No Client, no network impairment, 30 second tests.

