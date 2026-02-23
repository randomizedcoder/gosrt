# Client-Seeker Instrumentation Design

**Status**: Draft - Ready for Review
**Created**: 2026-01-17
**Parent**: [eventloop_profiling_analysis_design.md](eventloop_profiling_analysis_design.md)
**Goal**: Ensure we can always distinguish tool-limited vs library-limited scenarios

---

## 1. Problem Statement

### 1.1 What Happened

During performance testing at 350 Mb/s, we observed:
- "EventLoop Starvation" hypothesis flagged
- Throughput efficiency dropped to 74.5%
- No packet loss, no NAKs

We assumed the SRT library was the bottleneck. After extensive debugging of the library, we discovered **the client-seeker itself was the bottleneck** due to CPU overhead in the TokenBucket rate limiter.

### 1.2 Root Cause

**No metrics existed to distinguish:**
- Time spent in tool code (TokenBucket, Generator)
- Time spent in library code (SRT Write)
- Whether the tool was keeping up with the target rate

### 1.3 Cost

- ~6 hours debugging the wrong component
- Multiple design documents for timer optimization (not the issue)
- False conclusions about EventLoop starvation

---

## 2. Design Goals

### 2.1 Primary Goal

**Always be able to answer**: "Is the bottleneck in the tool or the library?"

### 2.2 Secondary Goals

1. **Automatic detection** - Tool should warn when it's self-limiting
2. **Zero runtime overhead** - Metrics must not impact performance being measured
3. **Prometheus-compatible** - Use existing metrics infrastructure
4. **TDD approach** - Tests before implementation

---

## 3. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           CLIENT-SEEKER                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐         │
│  │  BitrateManager │───▶│  TokenBucket    │───▶│  DataGenerator  │         │
│  │                 │    │                 │    │                 │         │
│  │  Metrics:       │    │  Metrics:       │    │  Metrics:       │         │
│  │  - target_bps   │    │  - wait_time    │    │  - packets_sent │         │
│  │  - changes      │    │  - spin_time    │    │  - bytes_sent   │         │
│  │                 │    │  - blocked_cnt  │    │  - efficiency   │         │
│  └─────────────────┘    │  - tokens_avail │    └────────┬────────┘         │
│                         └─────────────────┘             │                  │
│                                                         ▼                  │
│                                               ┌─────────────────┐         │
│                                               │   Publisher     │         │
│                                               │                 │         │
│                                               │  Metrics:       │         │
│                                               │  - write_time   │         │
│                                               │  - write_blocked│         │
│                                               │  - write_errors │         │
│                                               └────────┬────────┘         │
│                                                        │                  │
├────────────────────────────────────────────────────────┼──────────────────┤
│                                                        ▼                  │
│                                               ┌─────────────────┐         │
│                                               │   SRT Library   │         │
│                                               │   (external)    │         │
│                                               └─────────────────┘         │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 4. Metrics Specification

### 4.1 TokenBucket Metrics

| Metric Name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `seeker_tokenbucket_wait_seconds_total` | Counter | mode | Total time blocked waiting for tokens |
| `seeker_tokenbucket_spin_seconds_total` | Counter | - | Time spent in spin-wait loops |
| `seeker_tokenbucket_consume_total` | Counter | - | Total consume() calls |
| `seeker_tokenbucket_consume_blocked_total` | Counter | - | Times consume() had to wait |
| `seeker_tokenbucket_tokens` | Gauge | - | Current tokens available |
| `seeker_tokenbucket_tokens_max` | Gauge | - | Maximum token capacity |
| `seeker_tokenbucket_rate_bps` | Gauge | - | Current rate setting |

**Key Ratios for Detection:**
```
token_wait_ratio = seeker_tokenbucket_wait_seconds_total / elapsed_seconds
spin_ratio = seeker_tokenbucket_spin_seconds_total / elapsed_seconds
blocked_ratio = seeker_tokenbucket_consume_blocked_total / seeker_tokenbucket_consume_total
```

### 4.2 Generator Metrics

| Metric Name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `seeker_generator_packets_total` | Counter | - | Total packets generated |
| `seeker_generator_bytes_total` | Counter | - | Total bytes generated |
| `seeker_generator_target_bps` | Gauge | - | Target bitrate |
| `seeker_generator_actual_bps` | Gauge | - | Measured actual bitrate |
| `seeker_generator_efficiency` | Gauge | - | actual_bps / target_bps |
| `seeker_generator_latency_seconds` | Histogram | - | Time per Generate() call |

**Key Metric:**
```
efficiency = seeker_generator_actual_bps / seeker_generator_target_bps
```

### 4.3 Publisher (SRT Interface) Metrics

| Metric Name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `seeker_srt_write_seconds_total` | Counter | - | Total time in Write() calls |
| `seeker_srt_write_total` | Counter | - | Total Write() calls |
| `seeker_srt_write_blocked_total` | Counter | - | Times Write() blocked |
| `seeker_srt_write_errors_total` | Counter | reason | Write errors by type |
| `seeker_srt_write_latency_seconds` | Histogram | - | Write() call duration |

**Key Ratio:**
```
srt_write_ratio = seeker_srt_write_seconds_total / elapsed_seconds
```

### 4.4 Derived Metrics (Calculated)

| Metric Name | Formula | Interpretation |
|-------------|---------|----------------|
| `tool_overhead_ratio` | (wait_time + spin_time) / elapsed | % time in tool overhead |
| `srt_time_ratio` | srt_write_time / elapsed | % time in SRT library |
| `idle_ratio` | 1 - tool_overhead - srt_time | % time idle/other |

---

## 5. Bottleneck Detection Algorithm

### 5.1 Decision Tree

```go
// BottleneckType indicates where the performance limit is
type BottleneckType int

const (
    BottleneckUnknown BottleneckType = iota
    BottleneckHealthy              // Tool keeping up, no bottleneck detected
    BottleneckTool                 // Client-seeker is limiting
    BottleneckLibrary              // SRT library is limiting
    BottleneckNetwork              // Network/receiver is limiting
)

// DetectBottleneck analyzes metrics to determine bottleneck location
func DetectBottleneck(m *SeekerMetrics) (BottleneckType, string) {
    // Step 1: Check if generator is keeping up
    if m.Efficiency >= 0.95 {
        return BottleneckHealthy, "Tool healthy, achieving target rate"
    }

    // Step 2: Generator not keeping up - find out why

    // Check TokenBucket overhead
    tokenWaitRatio := m.TokenBucketWaitSeconds / m.ElapsedSeconds
    spinRatio := m.TokenBucketSpinSeconds / m.ElapsedSeconds
    toolOverhead := tokenWaitRatio + spinRatio

    if toolOverhead > 0.10 {
        return BottleneckTool, fmt.Sprintf(
            "TokenBucket overhead %.1f%% (wait=%.1f%%, spin=%.1f%%)",
            toolOverhead*100, tokenWaitRatio*100, spinRatio*100)
    }

    // Check SRT Write time
    srtWriteRatio := m.SRTWriteSeconds / m.ElapsedSeconds
    if srtWriteRatio > 0.50 {
        return BottleneckLibrary, fmt.Sprintf(
            "SRT Write() consuming %.1f%% of time", srtWriteRatio*100)
    }

    // Check if tokens are accumulating (library can't consume)
    tokenUtilization := float64(m.TokensAvailable) / float64(m.TokensMax)
    if tokenUtilization > 0.80 {
        return BottleneckLibrary, fmt.Sprintf(
            "Tokens accumulating (%.0f%% full) - library can't consume",
            tokenUtilization*100)
    }

    // Check for Write blocks
    if m.SRTWriteBlockedTotal > 0 {
        blockRatio := float64(m.SRTWriteBlockedTotal) / float64(m.SRTWriteTotal)
        if blockRatio > 0.10 {
            return BottleneckLibrary, fmt.Sprintf(
                "SRT Write blocking %.1f%% of calls", blockRatio*100)
        }
    }

    // Can't determine - need CPU profile
    return BottleneckUnknown, "Cannot determine bottleneck from metrics alone - run CPU profile"
}
```

### 5.2 Thresholds

| Threshold | Value | Rationale |
|-----------|-------|-----------|
| `EFFICIENCY_HEALTHY` | 0.95 | Allow 5% variance |
| `TOOL_OVERHEAD_CRITICAL` | 0.10 | >10% in tool code is too much |
| `SRT_WRITE_DOMINANT` | 0.50 | >50% in Write() means library-bound |
| `TOKEN_ACCUMULATION` | 0.80 | >80% full means library can't keep up |
| `WRITE_BLOCK_CRITICAL` | 0.10 | >10% blocked calls is significant |

---

## 6. Implementation Plan

### Phase 1: Add Metrics to TokenBucket (TDD)

**Files:**
- `contrib/client-seeker/tokenbucket.go` - Add metric fields
- `contrib/client-seeker/tokenbucket_test.go` - Add metric tests
- `contrib/client-seeker/metrics.go` (new) - Prometheus registration

**Tests first:**
```go
func TestTokenBucket_WaitTimeMetric(t *testing.T) {
    tb := NewTokenBucket(100_000_000, RefillHybrid)

    // Consume all tokens
    tb.Consume(tb.maxTokens)

    // This should block and record wait time
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    _ = tb.ConsumeOrWait(ctx, 1456)

    // Verify wait time was recorded
    stats := tb.Stats()
    assert.Greater(t, stats.WaitTimeNs, int64(0))
}

func TestTokenBucket_SpinTimeMetric(t *testing.T) {
    tb := NewTokenBucket(100_000_000, RefillSpin) // Force spin mode

    ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
    defer cancel()

    // This should spin and record spin time
    _ = tb.ConsumeOrWait(ctx, 1456)

    stats := tb.Stats()
    assert.Greater(t, stats.SpinTimeNs, int64(0))
}
```

### Phase 2: Add Metrics to Generator (TDD)

**Files:**
- `contrib/client-seeker/generator.go` - Add metric fields
- `contrib/client-seeker/generator_test.go` - Add metric tests

**Tests first:**
```go
func TestGenerator_EfficiencyMetric(t *testing.T) {
    // Create generator at 100 Mb/s
    bucket := NewTokenBucket(100_000_000, RefillSleep)
    gen := NewDataGenerator(bucket, 1456)

    ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
    defer cancel()

    // Generate packets for 1 second
    go bucket.StartRefillLoop(ctx)
    for i := 0; i < 1000; i++ {
        _, _ = gen.Generate(ctx)
    }

    // Efficiency should be close to 1.0
    efficiency := gen.Efficiency()
    assert.Greater(t, efficiency, 0.90)
}
```

### Phase 3: Add Metrics to Publisher (TDD)

**Files:**
- `contrib/client-seeker/publisher.go` - Add metric fields
- `contrib/client-seeker/publisher_test.go` - Add metric tests

### Phase 4: Add Prometheus Export

**Files:**
- `contrib/client-seeker/metrics.go` - Prometheus collectors
- `contrib/client-seeker/metrics_server.go` - Update to export new metrics

### Phase 5: Add Bottleneck Detection

**Files:**
- `contrib/client-seeker/bottleneck.go` (new) - Detection algorithm
- `contrib/client-seeker/bottleneck_test.go` (new) - Tests for detection

**Tests first:**
```go
func TestDetectBottleneck_ToolLimited(t *testing.T) {
    m := &SeekerMetrics{
        Efficiency:              0.75,
        TokenBucketWaitSeconds:  3.0,  // 30% of 10s
        TokenBucketSpinSeconds:  2.0,  // 20% of 10s
        SRTWriteSeconds:         1.0,  // 10% of 10s
        ElapsedSeconds:          10.0,
    }

    bottleneck, reason := DetectBottleneck(m)

    assert.Equal(t, BottleneckTool, bottleneck)
    assert.Contains(t, reason, "TokenBucket")
}

func TestDetectBottleneck_LibraryLimited(t *testing.T) {
    m := &SeekerMetrics{
        Efficiency:              0.75,
        TokenBucketWaitSeconds:  0.5,  // 5% of 10s
        TokenBucketSpinSeconds:  0.0,
        SRTWriteSeconds:         6.0,  // 60% of 10s
        ElapsedSeconds:          10.0,
    }

    bottleneck, reason := DetectBottleneck(m)

    assert.Equal(t, BottleneckLibrary, bottleneck)
    assert.Contains(t, reason, "SRT Write")
}

func TestDetectBottleneck_Healthy(t *testing.T) {
    m := &SeekerMetrics{
        Efficiency:              0.98,
        TokenBucketWaitSeconds:  0.1,
        TokenBucketSpinSeconds:  0.0,
        SRTWriteSeconds:         3.0,
        ElapsedSeconds:          10.0,
    }

    bottleneck, _ := DetectBottleneck(m)

    assert.Equal(t, BottleneckHealthy, bottleneck)
}
```

### Phase 6: Integrate with StabilityGate

**Files:**
- `contrib/performance/gate.go` - Add bottleneck detection call
- `contrib/performance/reporter.go` - Add tool health to report

### Phase 7: Fix TokenBucket Mode (The Original Issue)

**File:** `contrib/client-seeker/bitrate.go`

Change:
```go
bucket: NewTokenBucket(initialBitrate, RefillSleep),
```

Or add CLI flag for mode selection.

---

## 7. Verification Checklist

### 7.1 After Implementation

- [ ] All new metrics appear in Prometheus export
- [ ] Efficiency metric correctly reflects actual/target ratio
- [ ] Wait time metric increases when tokens blocked
- [ ] Spin time metric increases with RefillSpin mode
- [ ] SRT write time metric increases with slow receiver
- [ ] Bottleneck detection correctly identifies tool-limited
- [ ] Bottleneck detection correctly identifies library-limited
- [ ] Failure report includes tool health section

### 7.2 After Fix

- [ ] Re-run profile at 350 Mb/s - tool overhead should be <10%
- [ ] Efficiency should be >95% at 350 Mb/s
- [ ] Can now identify actual SRT library ceiling
- [ ] Failure reports correctly show library-limited when hitting ceiling

---

## 8. Success Criteria

The instrumentation is complete when:

1. **We can answer** "Is the tool or library the bottleneck?" from metrics alone
2. **Failure reports** include tool health status
3. **Automated detection** warns when tool is self-limiting
4. **Tests exist** for all detection scenarios
5. **After fixing TokenBucket**, we can find the true SRT library ceiling

---

## 9. Timeline Estimate

| Phase | Effort | Dependencies |
|-------|--------|--------------|
| Phase 1: TokenBucket metrics | 1 hour | None |
| Phase 2: Generator metrics | 30 min | Phase 1 |
| Phase 3: Publisher metrics | 30 min | None |
| Phase 4: Prometheus export | 30 min | Phases 1-3 |
| Phase 5: Bottleneck detection | 1 hour | Phases 1-3 |
| Phase 6: StabilityGate integration | 30 min | Phase 5 |
| Phase 7: TokenBucket fix | 15 min | Phases 1-6 |
| **Total** | **~4.5 hours** | |

---

## 10. Open Questions

1. Should we add a `-health-check` CLI mode for quick validation?
2. Should bottleneck detection run continuously or only on failure?
3. What's the right histogram bucket distribution for latency metrics?
4. Should we add a "tool overhead" metric to the server too?

---

## References

- [eventloop_profiling_analysis_design.md](eventloop_profiling_analysis_design.md) - Original analysis
- [performance_testing_implementation_log.md](performance_testing_implementation_log.md) - Implementation history
- [Prometheus Best Practices](https://prometheus.io/docs/practices/naming/)
