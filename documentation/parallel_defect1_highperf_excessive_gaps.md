# Parallel Defect 1: HighPerf Pipeline Detects 11x More Gaps Than Baseline

**Status**: Under Investigation
**Date**: 2025-12-10
**Test**: `Parallel-Starlink-5Mbps`

## Investigation Progress

| Date | Action | Outcome |
|------|--------|---------|
| 2025-12-10 | Initial parallel test run | HighPerf shows 11x more gaps (7,332 vs 653) |
| 2025-12-10 | Created defect document | Documented observations and hypotheses |
| 2025-12-10 | Designed isolation test plan | 7 tests to isolate single variable changes |
| 2025-12-10 | Implemented isolation framework | `test_isolation_mode.go`, `run_isolation_tests.sh` |
| 2025-12-10 | Ready for isolation tests | `sudo make test-isolation-all` |

## Problem Statement

When running two SRT pipelines in parallel over the same network with identical impairment (Starlink pattern: 60ms 100% loss at 12s, 27s, 42s, 57s), the **HighPerf pipeline (btree + io_uring)** detects approximately **11x more gaps** than the **Baseline pipeline (list + no io_uring)**.

Despite this, both pipelines achieve **100% recovery** - all gaps are eventually filled via retransmission.

## Key Observations

### Summary Metrics from Test Run

| Metric | Baseline | HighPerf | Ratio |
|--------|----------|----------|-------|
| **Client gaps detected** | 653 | 7,332 | **11.2x** |
| **Client retransmissions** | 957 | 7,796 | **8.1x** |
| **CG→Server gaps** | 779 | 6,116 | **7.9x** |
| **CG→Server retrans** | 779 | 6,116 | **7.9x** |
| **Server NAKs sent** | 11 | 1,805 | **164x** |
| **Server drops (already_acked)** | 97+123 | 5,114+6,947 | **55x** |
| **Recovery rate** | 100% | 100% | Same |

### Real-time Output Patterns

During the test, the real-time stats showed a clear pattern:

```
[SUB] 20:44:32.80 | ... |    0.0k ok /     0 gaps /     0 retx | recovery=100.0%  # Baseline
[SUB] 20:44:32.82 | ... |    0.0k ok /    22 gaps /    22 retx | recovery=100.0%  # HighPerf - gaps appear immediately!
```

The HighPerf subscriber starts accumulating gaps almost immediately after data flow begins, while Baseline remains at 0 gaps for the same time period.

### Connection Close Statistics (JSON Output)

**Baseline Client:**
```json
{
  "pkt_recv_data": 57313,
  "pkt_recv_loss": 653,
  "pkt_sent_nak": 10
}
```

**HighPerf Client:**
```json
{
  "pkt_recv_data": 64380,
  "pkt_recv_loss": 7332,
  "pkt_sent_nak": 2504
}
```

### Key Differences

1. **HighPerf receives more total packets** (64,380 vs 57,313) - this includes retransmissions
2. **HighPerf sends 250x more NAKs** (2,504 vs 10)
3. **HighPerf has 55x more "already_acked" drops** - packets arriving after already acknowledged

## Hypotheses

### Hypothesis 1: io_uring Introduces Timing/Ordering Differences (HIGH CONFIDENCE)

**Theory**: The `io_uring` async I/O path may process packets with slightly different timing or ordering than the synchronous path, causing the receiver to detect more sequence gaps.

**Evidence**:
- The gaps appear immediately at test start, even before any network impairment
- Looking at `20:44:32.82` - HighPerf shows 22 gaps while Baseline shows 0 gaps
- This is BEFORE the Starlink pattern even starts applying loss

**Why this matters**: If packets arrive slightly out of order due to io_uring's async nature, the receiver will register a "gap" for each out-of-order packet, even if the missing packet arrives milliseconds later.

### Hypothesis 2: btree vs list Packet Store Sensitivity (MEDIUM CONFIDENCE)

**Theory**: The btree packet reorder buffer may have different sensitivity to sequence gaps compared to the linked list implementation.

**Evidence**:
- Both configurations use the same SRT protocol logic
- The btree degree is set to 32, which is quite high
- Need to verify if gap detection happens before or after packet store insertion

**Counter-evidence**: Gap detection (`CongestionRecvPktLoss`) happens in `congestion/live/receive.go` when a packet with a higher sequence number than expected arrives. This is BEFORE the packet store, so btree vs list shouldn't affect gap counting directly.

### Hypothesis 3: Async Receive Path Causes Out-of-Order Delivery (HIGH CONFIDENCE)

**Theory**: When `io_uring` is enabled for receiving (`-iouringrecvenabled`), the async completion of read operations may deliver packets to the application layer in a different order than they were received from the kernel.

**Evidence**:
- Baseline: 10 NAKs sent for 653 gaps = packets mostly arrive in order, few NAKs needed
- HighPerf: 2,504 NAKs sent for 7,332 gaps = constant out-of-order arrivals
- The ratio of NAKs-to-gaps is much higher for HighPerf

**Mechanism**:
1. `io_uring` submits multiple read requests
2. Completions may arrive out of order
3. Each "late" packet triggers a gap detection + NAK
4. Original packet arrives → "already_acked" drop

### Hypothesis 4: Publisher Timing Differences (LOW CONFIDENCE)

**Theory**: The HighPerf client-generator sends packets with slightly different timing due to io_uring async writes, causing more packets to be in-flight during the 60ms outages.

**Evidence**:
- HighPerf sends more total packets (63,124 vs 57,787)
- This is about 9% more packets for the same test duration

**Counter-evidence**: Both use the same 5 Mb/s bitrate target, and the real-time display shows similar pkt/s rates.

## Analysis of Comparison Output Issues

The detailed comparison output shows metrics alternating between `→ 0` and `0 →` patterns:

```
│ ✓packets_lost_total                              779            0    -100.0% │
│ ⚠️packets_lost_total                                0         6116        NEW │
```

This is a **display bug** in the comparison logic - metrics with different socket_ids are being treated as separate metrics rather than aggregated. The socket_id label should be stripped for comparison purposes.

## Proposed Next Steps

### Phase 1: Isolate the Variable (RECOMMENDED FIRST)

Run tests with single variable changes to isolate the cause:

1. **Test A**: Baseline config + io_uring receive only (no btree)
   - Goal: Determine if io_uring receive is the cause

2. **Test B**: HighPerf config - disable io_uring receive only
   - Goal: Confirm by removing the suspected cause

3. **Test C**: Baseline config + btree only (no io_uring)
   - Goal: Rule out btree as the cause

### Phase 2: Add Diagnostic Counters

Add counters to measure packet ordering:

1. `CongestionRecvPktOutOfOrder` - Packets received with seq < max_seen_seq (arriving late)
2. `CongestionRecvPktReorderDepth` - How far back the out-of-order packet was

### Phase 3: Investigate io_uring Receive Path

If Phase 1 confirms io_uring receive is the cause:

1. Review `io_uring` submission queue depth and completion ordering
2. Consider if `IOSQE_IO_LINK` could enforce ordering
3. Evaluate if the async receive path needs sequence-aware buffering

### Phase 4: Fix Comparison Output

The parallel comparison needs to:
1. Aggregate metrics across socket_ids
2. Compare Baseline connection 1 vs HighPerf connection 1 (not mix them)
3. Show a cleaner side-by-side comparison

## Questions for Further Investigation

1. Is the HighPerf pipeline using io_uring for the **receive** path on the client? (Yes - `-iouringrecvenabled`)

2. Are the 7,332 gaps detected all during the 60ms outages, or distributed throughout?

3. What is the average reorder depth? Are packets arriving 1-2 behind, or much more?

4. Does the issue persist without network impairment (clean network test)?

## Test Commands for Investigation

### Isolation Tests (Implemented)

A new isolation testing framework has been created to systematically test each variable:

```bash
# List all isolation tests
make test-isolation-list

# Run a single isolation test
sudo make test-isolation CONFIG=Isolation-CG-IoUringSend

# Run all 7 tests (~3.5 min, output captured to /tmp/isolation_tests_XXX/)
sudo make test-isolation-all
```

### Isolation Test Matrix

| Test | Variable Changed | Status |
|------|------------------|--------|
| `Isolation-Control` | None (sanity check) | Ready |
| `Isolation-CG-IoUringSend` | CG: io_uring send | Ready |
| `Isolation-CG-IoUringRecv` | CG: io_uring recv | Ready |
| `Isolation-CG-Btree` | CG: btree | Ready |
| `Isolation-Server-IoUringSend` | Server: io_uring send | Ready |
| `Isolation-Server-IoUringRecv` | Server: io_uring recv | Ready |
| `Isolation-Server-Btree` | Server: btree | Ready |

### Test Architecture

- **Duration**: 30 seconds per test
- **Network**: Clean path (no impairment) - any gaps = code bug
- **Architecture**: CG → Server only (no Client/Subscriber)
- **Control pipeline**: list + no io_uring (baseline)
- **Test pipeline**: exactly ONE variable changed from control

## Related Files

### Parallel Test Infrastructure
- `contrib/integration_testing/test_parallel_mode.go` - Parallel test orchestration
- `contrib/integration_testing/parallel_analysis.go` - Comparison logic

### Isolation Test Infrastructure (NEW)
- `contrib/integration_testing/test_isolation_mode.go` - Simplified CG→Server tests
- `contrib/integration_testing/run_isolation_tests.sh` - Batch runner with output capture
- `contrib/integration_testing/config.go` - `IsolationTestConfig`, helper methods
- `contrib/integration_testing/test_configs.go` - 7 isolation test configurations

### Core SRT Code (Under Investigation)
- `congestion/live/receive.go` - Gap detection logic
- `io_uring_reader.go` - io_uring receive path
- `io_uring_writer.go` - io_uring send path
- `packet/store_btree.go` - B-tree packet store
- `packet/store_list.go` - Linked list packet store

## Appendix: Full Comparison Data

### Client-Generator Comparison
| Metric | Baseline | HighPerf |
|--------|----------|----------|
| packets_sent_total [data] | 57,787 | 63,124 |
| packets_lost_total | 779 | 6,116 |
| retransmissions_total | 779 | 6,116 |
| nak_packets_requested_total (recv) | 779 | 6,116 |

### Server Comparison
| Metric | Baseline | HighPerf |
|--------|----------|----------|
| packets_received_total [data] | 57,227 | 62,452 |
| packets_lost_total (recv) | 569 | 5,404 |
| packets_drop_total (recv) | 219 | 5,444 |
| nak_entries_total (recv) [range] | 956 | 6,929 |

### Client Comparison
| Metric | Baseline | HighPerf |
|--------|----------|----------|
| packets_received_total [data] | 57,313 | 64,380 |
| packets_lost_total (recv) | 653 | 7,332 |
| retransmissions_total (recv) | 957 | 7,796 |
| packets_drop_total (recv) | 305 | 7,372 |
| recv_data_drop_total [already_acked] | 123 | 6,947 |

