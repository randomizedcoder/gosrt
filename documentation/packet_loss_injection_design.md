# Packet Loss Injection Design

## Overview

This document describes the design for controlled packet loss injection in the GoSRT integration testing framework. Packet loss injection is essential for validating SRT's core ARQ-based loss recovery mechanism.

**Related Documents**:
- [Integration Testing Design](integration_testing_design.md) - Parent integration testing framework
- [Packet Loss Injection Implementation](packet_loss_injection_implementation.md) - Implementation progress tracker
- [amt.sh](amt.sh) - Linux kernel network namespace example (reference for implementation)

---

## Requirements

### 1. Functional Requirements

#### 1.1 Packet Loss Control

| Requirement | Description | Priority |
|-------------|-------------|----------|
| **Uniform Loss** | Drop packets at a fixed percentage (0.1%, 1%, 2%, 5%, 10%) | High |
| **Burst Loss** | Drop N consecutive packets (e.g., 5, 10, 20 packets) | High |
| **Periodic Burst** | Burst loss at regular intervals (e.g., every 10 seconds) | High |
| **Random Burst** | Burst loss with random timing and duration | Medium |
| **Correlated Loss** | Loss probability depends on previous packet state | Medium |
| **Asymmetric Loss** | Different loss rates for send vs receive paths | Medium |

#### 1.2 Latency Control

| Requirement | Description | Priority |
|-------------|-------------|----------|
| **No Latency** | Baseline testing with zero added delay | High |
| **Tri-Modal Latency** | Three latency tiers: 10ms, 60ms, 130ms | High |
| **GEO Satellite** | Geostationary satellite latency (~600ms RTT) | High |
| **Jitter** | Variable delay with configurable distribution | High |
| **Asymmetric Latency** | Different latency for each direction | Medium |
| **Long Queue** | 50,000 packet queue to prevent netem tail drop | High |

##### Latency Profiles (RTT-based)

Latency profiles are defined by their **Round-Trip Time (RTT)**. Since netem applies one-way delay, the configured delay is RTT/2.

| Profile | RTT | Netem Delay (RTT/2) | Use Case |
|---------|-----|---------------------|----------|
| **None** | 0ms | 0ms | Baseline, local network |
| **Tier 1 (Low)** | 10ms | 5ms | Regional datacenter |
| **Tier 2 (Medium)** | 60ms | 30ms | Cross-continental |
| **Tier 3 (High)** | 130ms | 65ms | Intercontinental |
| **GEO Satellite** | 300ms | 150ms | Geostationary orbit |

##### Netem Queue Configuration

To prevent tail-drop when injecting latency, netem must be configured with a large queue:

```bash
# Default netem queue is 1000 packets - too small for high latency
# Configure 50,000 packet queue limit to prevent drops
tc qdisc add dev rtr_sub root netem delay 130ms limit 50000
```

**Rationale**: At 10 Mb/s with 1500-byte packets, 130ms latency requires buffering:
- Bandwidth-delay product: 10 Mb/s × 0.130s = 1.3 Mb ≈ 108 packets minimum
- 50,000 packets provides large headroom for bursts and higher bitrates

#### 1.3 Network Outage Simulation

| Requirement | Description | Priority |
|-------------|-------------|----------|
| **Complete Outage** | Total packet drop for specified duration | High |
| **Periodic Outage** | Outages at regular intervals | High |
| **Starlink Pattern** | Specific LEO satellite reconvergence simulation | High |

#### 1.4 Starlink Reconvergence Pattern

LEO satellite networks like Starlink experience periodic reconvergence events:

```
Timeline (one minute):
├── 0s  ────────────────┤ Normal operation
├── 12s ─── 100% loss 50-70ms ─── Normal
├── 27s ─── 100% loss 50-70ms ─── Normal
├── 42s ─── 100% loss 50-70ms ─── Normal
├── 57s ─── 100% loss 50-70ms ─── Normal
└── 60s ────────────────┤ Repeat
```

**Characteristics**:
- Occurs at seconds 12, 27, 42, 57 of each minute
- Duration: 50-70ms per event
- Impact: **100% packet loss** (complete outage) during event
- Pattern repeats every minute

#### 1.5 High Loss Burst Pattern

Simulates severe network degradation:

```
Timeline (one minute):
├── 0s  ────────────────┤ Normal operation
├── 1.5s ─── 80-90% loss for 1 second ─── Normal
└── 60s ────────────────┤ Repeat
```

**Characteristics**:
- Occurs at 1.5 seconds into each minute
- Duration: 1 second
- Impact: 80-90% packet loss (severe degradation, not complete outage)
- Tests SRT recovery under extreme but not total loss

### 2. Non-Functional Requirements

| Requirement | Description |
|-------------|-------------|
| **Performance** | Must handle 100 Mb/s+ throughput without being the bottleneck |
| **Precision** | Timing accuracy within 1ms for latency injection |
| **Reproducibility** | Deterministic patterns for debugging (seed-based randomness) |
| **Observability** | Log/metric injection events for correlation with SRT metrics |
| **Ease of Use** | Simple configuration via CLI or test config files |
| **Cross-Platform** | Linux required (netem, iproute2), macOS nice-to-have |
| **No Tail-Drop** | Netem queue configured with 50k packet limit to prevent drops |

### 3. Integration Requirements

| Requirement | Description |
|-------------|-------------|
| **Test Framework** | Integrate with existing `TestConfig` structure |
| **Metrics Correlation** | Correlate injection events with Prometheus metrics |
| **Automation** | Fully automated (no manual network configuration) |
| **Cleanup** | Automatic cleanup on test completion or failure |
| **Parallel Tests** | Support concurrent test runs with isolation |

---

## Implementation Options

### Option 1: Linux Network Namespaces + TC/Netem

Use Linux kernel features to create isolated network environments with traffic control.

#### Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Host System                                        │
│                                                                             │
│  ┌─────────────────┐      ┌─────────────────┐      ┌─────────────────┐     │
│  │   Namespace A   │      │   Namespace B   │      │   Namespace C   │     │
│  │                 │      │                 │      │                 │     │
│  │ Client-Generator│      │     Server      │      │     Client      │     │
│  │   (Publisher)   │      │                 │      │  (Subscriber)   │     │
│  │                 │      │                 │      │                 │     │
│  │   10.0.1.1      │      │   10.0.1.2      │      │   10.0.2.2      │     │
│  └────────┬────────┘      └────────┬────────┘      └────────┬────────┘     │
│           │                        │                        │               │
│           │    veth pair          │    veth pair           │               │
│           │                        │                        │               │
│  ┌────────▼────────────────────────▼────────────────────────▼────────┐     │
│  │                        Bridge (br0)                                │     │
│  │                                                                    │     │
│  │   TC/Netem rules applied here:                                    │     │
│  │   - Packet loss (uniform, burst, correlated)                      │     │
│  │   - Latency and jitter                                            │     │
│  │   - Bandwidth limiting                                            │     │
│  │   - Packet reordering                                             │     │
│  └────────────────────────────────────────────────────────────────────┘     │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### TC/Netem Commands

```bash
# Add 2% uniform packet loss
tc qdisc add dev veth0 root netem loss 2%

# Add 100ms latency with 20ms jitter (normal distribution)
tc qdisc add dev veth0 root netem delay 100ms 20ms distribution normal

# Add burst loss (5% loss with 25% correlation - creates bursts)
tc qdisc add dev veth0 root netem loss 5% 25%

# Combined: 50ms latency + 10ms jitter + 1% loss
tc qdisc add dev veth0 root netem delay 50ms 10ms loss 1%

# Complete outage (100% loss)
tc qdisc add dev veth0 root netem loss 100%

# Starlink pattern (requires scripted changes at specific times)
# At seconds 12, 27, 42, 57: apply 100% loss for 60ms
```

#### Reference Implementation

The file `documentation/amt.sh` (downloaded from Linux kernel selftests) provides a reference for:
- Creating network namespaces
- Setting up veth pairs
- Configuring bridges
- Applying tc/netem rules
- Cleanup on exit

**Note**: Detailed review of `amt.sh` deferred to implementation phase.

#### Pros

| Advantage | Description |
|-----------|-------------|
| **Kernel-Level** | Uses battle-tested kernel features (netem) |
| **High Performance** | No userspace packet copying, handles 100+ Gb/s |
| **Feature Complete** | Supports all required loss/latency patterns |
| **Precision** | Kernel timers provide microsecond precision |
| **Low Development** | Mostly shell scripts, minimal Go code |
| **Well Documented** | Extensive documentation and examples available |

#### Cons

| Disadvantage | Description |
|--------------|-------------|
| **Linux Only** | Network namespaces are Linux-specific |
| **Root Required** | Creating namespaces requires root/sudo |
| **Complexity** | Setup/teardown logic can be error-prone |
| **Dynamic Changes** | Changing loss patterns requires tc commands |
| **Debugging** | Harder to debug than in-process solution |
| **CI/CD Compatibility** | May require privileged containers |

#### Effort Estimate

| Task | Effort |
|------|--------|
| Namespace setup/teardown scripts | 2-4 hours |
| TC/netem configuration library | 4-8 hours |
| Integration with TestConfig | 4-8 hours |
| Starlink pattern implementation | 2-4 hours |
| Testing and debugging | 4-8 hours |
| **Total** | **16-32 hours** |

---

### Option 2: Custom UDP Proxy Tool

Build a Go-based UDP proxy that intercepts and manipulates packets.

#### Architecture

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│ Client-Generator│────▶│    UDP Proxy    │────▶│     Server      │
│   (Publisher)   │     │                 │     │                 │
│  127.0.0.20:*   │     │  Intercepts &   │     │ 127.0.0.10:6000 │
└─────────────────┘     │  manipulates    │     └─────────────────┘
                        │  packets        │              ▲
                        │                 │              │
                        │  - Drop         │     ┌────────┴────────┐
                        │  - Delay        │     │     Client      │
                        │  - Duplicate    │◀────│  (Subscriber)   │
                        │  - Reorder      │     │  127.0.0.30:*   │
                        └─────────────────┘     └─────────────────┘
```

#### High-Level Design

```go
// ProxyConfig defines packet manipulation rules
type ProxyConfig struct {
    ListenAddr  string        // Address to listen on
    ForwardAddr string        // Address to forward to

    // Loss configuration
    LossRate       float64    // 0.0-1.0 (e.g., 0.02 = 2%)
    BurstLossProb  float64    // Probability of burst after loss
    BurstLossSize  int        // Number of packets in burst

    // Latency configuration
    BaseLatency    time.Duration
    Jitter         time.Duration
    JitterDistrib  string     // "uniform", "normal", "pareto"

    // Outage patterns
    OutageSchedule []OutageEvent
}

type OutageEvent struct {
    StartOffset time.Duration  // Offset from minute start
    Duration    time.Duration  // How long to drop
    Repeat      bool           // Repeat every minute
}

// Proxy handles packet interception
type Proxy struct {
    config     ProxyConfig
    listenConn *net.UDPConn
    forwardConn *net.UDPConn
    delayQueue *DelayQueue    // SPSC queue for latency injection
}
```

#### Pros

| Advantage | Description |
|-----------|-------------|
| **Cross-Platform** | Works on Linux, macOS, Windows |
| **No Root** | Runs as normal user |
| **Tight Integration** | Can be embedded in test framework |
| **Fine Control** | Complete control over packet handling |
| **Debugging** | Easy to add logging and debugging |
| **Programmable** | Complex patterns easy to implement |

#### Cons

| Disadvantage | Description |
|--------------|-------------|
| **Development Effort** | Significant code to write |
| **Performance** | Userspace packet copying overhead |
| **Latency Precision** | Go scheduler may affect timing |
| **Testing Required** | Needs its own test suite |
| **Maintenance** | Another component to maintain |
| **Bugs** | Risk of subtle bugs in timing/queuing |

#### Effort Estimate

| Task | Effort |
|------|--------|
| Basic UDP proxy | 4-8 hours |
| Loss injection logic | 4-8 hours |
| Delay queue implementation | 8-16 hours |
| Outage pattern scheduling | 4-8 hours |
| Integration with TestConfig | 4-8 hours |
| Testing and debugging | 16-24 hours |
| **Total** | **40-72 hours** |

---

### Option 3: Existing Tools

Evaluate existing network impairment tools.

#### 3.1 Toxiproxy (Shopify)

**Description**: TCP/UDP proxy for simulating network conditions.

```bash
# Example usage
toxiproxy-cli create srt_proxy -l localhost:16000 -u localhost:6000

# Add latency
toxiproxy-cli toxic add srt_proxy -t latency -a latency=100 -a jitter=20

# Add packet loss (requires UDP support)
toxiproxy-cli toxic add srt_proxy -t slow_close -a delay=1000
```

| Aspect | Assessment |
|--------|------------|
| UDP Support | Limited (primarily TCP-focused) |
| Latency | ✅ Supported |
| Packet Loss | ⚠️ Limited UDP support |
| Integration | ✅ CLI and API |
| Maintenance | ✅ Active project |

#### 3.2 Comcast (tylertreat/comcast)

**Description**: Simulates network problems using tc/netem wrapper.

```bash
# Add network impairment
comcast --device=eth0 --latency=250 --packet-loss=10%

# Remove impairment
comcast --stop
```

| Aspect | Assessment |
|--------|------------|
| UDP Support | ✅ Full (uses netem) |
| Latency | ✅ Supported |
| Packet Loss | ✅ Supported |
| Integration | ⚠️ CLI only |
| Maintenance | ⚠️ Less active |

#### 3.3 tc-netem directly

**Description**: Use tc/netem commands directly without wrapper.

| Aspect | Assessment |
|--------|------------|
| UDP Support | ✅ Full |
| Latency | ✅ Full control |
| Packet Loss | ✅ All patterns |
| Integration | ⚠️ Shell commands |
| Maintenance | ✅ Kernel maintained |

#### 3.4 pumba (for containers)

**Description**: Chaos testing tool for Docker containers.

| Aspect | Assessment |
|--------|------------|
| UDP Support | ✅ Uses tc/netem |
| Container Required | ⚠️ Docker only |
| Integration | ✅ CLI and API |
| Maintenance | ✅ Active |

---

## Comparison Matrix

| Criteria | Namespaces + Netem | Custom Proxy | Toxiproxy | Comcast |
|----------|-------------------|--------------|-----------|---------|
| **Packet Loss** | ✅ Full | ✅ Full | ⚠️ Limited | ✅ Full |
| **Latency/Jitter** | ✅ Full | ✅ Full | ✅ Full | ✅ Full |
| **Burst Loss** | ✅ Correlated | ✅ Custom | ❌ No | ✅ Correlated |
| **Starlink Pattern** | ⚠️ Scripted | ✅ Native | ❌ No | ⚠️ Scripted |
| **Performance** | ✅ Kernel | ⚠️ Userspace | ⚠️ Userspace | ✅ Kernel |
| **Precision** | ✅ <1ms | ⚠️ ~1-5ms | ⚠️ ~1-5ms | ✅ <1ms |
| **Cross-Platform** | ❌ Linux only | ✅ All | ✅ All | ❌ Linux only |
| **Root Required** | ❌ Yes | ✅ No | ✅ No | ❌ Yes |
| **Dev Effort** | ⭐⭐⭐⭐ Low | ⭐⭐ High | ⭐⭐⭐ Medium | ⭐⭐⭐⭐ Low |
| **Integration** | ⭐⭐⭐ Medium | ⭐⭐⭐⭐⭐ Tight | ⭐⭐⭐ Medium | ⭐⭐ Low |
| **Debugging** | ⭐⭐ Hard | ⭐⭐⭐⭐ Easy | ⭐⭐⭐ Medium | ⭐⭐ Hard |

---

## Recommendation

**To Be Decided** - This section will be updated after reviewing the options.

### Preliminary Assessment

| Option | Recommendation | Rationale |
|--------|----------------|-----------|
| **Namespaces + Netem** | ⭐ Likely Best | Lowest effort, highest precision, proven solution |
| **Custom Proxy** | Consider Later | Only if cross-platform is critical requirement |
| **Toxiproxy** | Not Recommended | UDP support too limited |
| **Comcast** | Backup Option | Simple wrapper, less flexible |

### Decision

**Selected Approach**: Linux Network Namespaces + TC/Netem + Go Controller

Rationale:
- Lowest development effort
- Highest timing precision (kernel-level)
- Battle-tested kernel features
- Go controller enables dynamic pattern changes (Starlink, high-loss bursts)

---

## Detailed Design

### Key Learnings from `amt.sh` Review

The Linux kernel selftest `amt.sh` demonstrates:

1. **Namespace Creation**: `ip netns add <name>` with unique names using `mktemp -u`
2. **Veth Pairs**: `ip link add <name> type veth peer name <peer>` for namespace interconnection
3. **Namespace Assignment**: `ip link set <dev> netns <ns>` moves interfaces into namespaces
4. **Bridge Setup**: Optional bridge for multi-namespace connectivity
5. **Cleanup Pattern**: Trap handler with `exit_cleanup()` for reliable teardown
6. **Command Execution**: `ip netns exec <ns> <command>` runs commands in namespace context

### Network Architecture for GoSRT Testing

#### Design Rationale

**Problem with Single Router + Dynamic Netem**:
When netem rules are changed dynamically (e.g., switching from 60ms to 130ms latency), the queue
is flushed. This causes unrealistic packet loss during transitions and doesn't accurately simulate
real network behavior during Starlink reconvergence events.

**Solution: Dual Router with Fixed Latency Links**:
- Two router namespaces connected by **multiple parallel veth pairs**, each with fixed latency
- Switch between latency profiles by changing **routing**, not netem rules (no queue flush)
- Use **null/blackhole routes** for 100% loss injection (Starlink events) - instant, no dependencies
- Use **netem loss parameter** for probabilistic loss (2%, 5%, etc.) - already on inter-router links

#### Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                           Host System                                                │
│                                                                                                      │
│  ┌────────────────────────┐                                      ┌────────────────────────┐         │
│  │     ns_publisher       │                                      │     ns_subscriber      │         │
│  │                        │                                      │                        │         │
│  │   Client-Generator     │                                      │        Client          │         │
│  │      or FFmpeg         │                                      │                        │         │
│  │                        │                                      │                        │         │
│  │   eth0: 10.1.1.2/24    │                                      │   eth0: 10.1.2.2/24    │         │
│  │   gw: 10.1.1.1         │                                      │   gw: 10.1.2.1         │         │
│  └───────────┬────────────┘                                      └───────────┬────────────┘         │
│              │                                                               │                       │
│              │ veth pair                                                     │ veth pair             │
│              │                                                               │                       │
│  ┌───────────▼───────────────────────────────────────────────────────────────▼───────────────┐      │
│  │                                      ns_router_a                                           │      │
│  │                                    (Client-side Router)                                    │      │
│  │                                                                                            │      │
│  │   ┌──────────────┐                                              ┌──────────────┐          │      │
│  │   │ eth_pub      │                                              │ eth_sub      │          │      │
│  │   │ 10.1.1.1/24  │                                              │ 10.1.2.1/24  │          │      │
│  │   └──────────────┘                                              └──────────────┘          │      │
│  │                                                                                            │      │
│  │   ┌─────────────────────────────────────────────────────────────────────────────────┐     │      │
│  │   │                    Parallel Links to ns_router_b                                 │     │      │
│  │   │                    (Fixed Latency - Never Changed)                               │     │      │
│  │   │                                                                                  │     │      │
│  │   │   link0: 10.100.0.1/30 ──────────── 0ms RTT (no delay) ────────────────────────│     │      │
│  │   │   link1: 10.100.1.1/30 ──────────── 10ms RTT (5ms each way) ───────────────────│     │      │
│  │   │   link2: 10.100.2.1/30 ──────────── 60ms RTT (30ms each way) ──────────────────│     │      │
│  │   │   link3: 10.100.3.1/30 ──────────── 130ms RTT (65ms each way) ─────────────────│     │      │
│  │   │   link4: 10.100.4.1/30 ──────────── 300ms RTT (150ms each way) ────────────────│     │      │
│  │   │                                                                                  │     │      │
│  │   └─────────────────────────────────────────────────────────────────────────────────┘     │      │
│  │                                                                                            │      │
│  │   Blackhole routes: 100% drop for Starlink/outage events (instant effect)               │      │
│  │   Netem loss: Probabilistic loss (2%, 5%, etc.) on inter-router links                   │      │
│  │   Routing: Switch between links to change latency profile                                │      │
│  │                                                                                            │      │
│  └───────────────────────────────────────────────────────────────────────────────────────────┘      │
│                                            │ │ │ │ │                                                 │
│                                            │ │ │ │ │  5 parallel veth pairs                         │
│                                            │ │ │ │ │  (one per latency tier)                        │
│                                            ▼ ▼ ▼ ▼ ▼                                                 │
│  ┌───────────────────────────────────────────────────────────────────────────────────────────┐      │
│  │                                      ns_router_b                                           │      │
│  │                                    (Server-side Router)                                    │      │
│  │                                                                                            │      │
│  │   ┌─────────────────────────────────────────────────────────────────────────────────┐     │      │
│  │   │                    Parallel Links to ns_router_a                                 │     │      │
│  │   │                    (Fixed Latency - Never Changed)                               │     │      │
│  │   │                                                                                  │     │      │
│  │   │   link0: 10.100.0.2/30 ──────────── 0ms RTT (no delay) ────────────────────────│     │      │
│  │   │   link1: 10.100.1.2/30 ──────────── 10ms RTT (5ms each way) ───────────────────│     │      │
│  │   │   link2: 10.100.2.2/30 ──────────── 60ms RTT (30ms each way) ──────────────────│     │      │
│  │   │   link3: 10.100.3.2/30 ──────────── 130ms RTT (65ms each way) ─────────────────│     │      │
│  │   │   link4: 10.100.4.2/30 ──────────── 300ms RTT (150ms each way) ────────────────│     │      │
│  │   │                                                                                  │     │      │
│  │   └─────────────────────────────────────────────────────────────────────────────────┘     │      │
│  │                                                                                            │      │
│  │   ┌──────────────┐                                                                        │      │
│  │   │ eth_srv      │                                                                        │      │
│  │   │ 10.2.1.1/24  │                                                                        │      │
│  │   └──────┬───────┘                                                                        │      │
│  │          │                                                                                 │      │
│  └──────────┼────────────────────────────────────────────────────────────────────────────────┘      │
│             │                                                                                        │
│             │ veth pair                                                                              │
│             │                                                                                        │
│  ┌──────────▼─────────────────────────────────────────────────────────────────────────────┐         │
│  │                                      ns_server                                          │         │
│  │                                                                                         │         │
│  │                                    GoSRT Server                                         │         │
│  │                                    10.2.1.2:6000                                        │         │
│  │                                                                                         │         │
│  │   eth0: 10.2.1.2/24                                                                     │         │
│  │   gw: 10.2.1.1                                                                          │         │
│  └─────────────────────────────────────────────────────────────────────────────────────────┘         │
│                                                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────────────────────────┐       │
│  │                              Go Controller (netem-controller)                             │       │
│  │                                                                                           │       │
│  │  • Latency switching: Change routing to use different inter-router link                  │       │
│  │  • 100% loss (Starlink/outage): Blackhole routes (instant effect)                        │       │
│  │  • Probabilistic loss: Netem loss parameter on inter-router links                        │       │
│  │  • Starlink pattern: Blackhole route at seconds 12, 27, 42, 57 for 50-70ms              │       │
│  │                                                                                           │       │
│  └──────────────────────────────────────────────────────────────────────────────────────────┘       │
│                                                                                                      │
└──────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

#### Network Subnets

| Subnet | Purpose | Endpoints |
|--------|---------|-----------|
| **10.1.1.0/24** | Publisher ↔ Router A | Publisher (10.1.1.2), Router A (10.1.1.1) |
| **10.1.2.0/24** | Subscriber ↔ Router A | Subscriber (10.1.2.2), Router A (10.1.2.1) |
| **10.2.1.0/24** | Server ↔ Router B | Server (10.2.1.2), Router B (10.2.1.1) |
| **10.100.0.0/30** | Inter-router Link 0 | Router A (10.100.0.1), Router B (10.100.0.2) - 0ms |
| **10.100.1.0/30** | Inter-router Link 1 | Router A (10.100.1.1), Router B (10.100.1.2) - 10ms RTT |
| **10.100.2.0/30** | Inter-router Link 2 | Router A (10.100.2.1), Router B (10.100.2.2) - 60ms RTT |
| **10.100.3.0/30** | Inter-router Link 3 | Router A (10.100.3.1), Router B (10.100.3.2) - 130ms RTT |
| **10.100.4.0/30** | Inter-router Link 4 | Router A (10.100.4.1), Router B (10.100.4.2) - 300ms RTT |

#### Inter-Router Links with Fixed Latency

| Link | Router A IP | Router B IP | RTT | Netem Delay | Use Case |
|------|-------------|-------------|-----|-------------|----------|
| link0 | 10.100.0.1/30 | 10.100.0.2/30 | 0ms | none | Baseline, local |
| link1 | 10.100.1.1/30 | 10.100.1.2/30 | 10ms | 5ms each side | Regional DC |
| link2 | 10.100.2.1/30 | 10.100.2.2/30 | 60ms | 30ms each side | Cross-continental |
| link3 | 10.100.3.1/30 | 10.100.3.2/30 | 130ms | 65ms each side | Intercontinental |
| link4 | 10.100.4.1/30 | 10.100.4.2/30 | 300ms | 150ms each side | GEO Satellite |

#### Latency Switching via Routing

To switch between latency profiles, change the routing table (no queue flush):

```bash
# Switch to 60ms RTT path (link2)
ip netns exec ns_router_a ip route replace 10.2.1.0/24 via 10.100.2.2
ip netns exec ns_router_b ip route replace 10.1.1.0/24 via 10.100.2.1
ip netns exec ns_router_b ip route replace 10.1.2.0/24 via 10.100.2.1

# Switch to 130ms RTT path (link3)
ip netns exec ns_router_a ip route replace 10.2.1.0/24 via 10.100.3.2
ip netns exec ns_router_b ip route replace 10.1.1.0/24 via 10.100.3.1
ip netns exec ns_router_b ip route replace 10.1.2.0/24 via 10.100.3.1
```

#### Loss Injection via Null Routes and Netem

**100% Loss Events** (Starlink, complete outage) use blackhole routes - instant and simple:

```bash
# Starlink event: 100% drop for 60ms using blackhole route
# Add blackhole route to drop all traffic to the server subnet
ip netns exec ns_router_a ip route add blackhole 10.2.1.0/24
sleep 0.06
# Remove blackhole route to restore traffic
ip netns exec ns_router_a ip route del blackhole 10.2.1.0/24
```

**Probabilistic Loss** (2%, 5%, etc.) uses netem's loss parameter on inter-router links:

```bash
# Add 5% loss to the inter-router link (combined with existing latency)
# IMPORTANT: Always include "limit 50000" to prevent netem tail drops
ip netns exec ns_router_a tc qdisc change dev link2_a root netem delay 30ms loss 5% limit 50000

# Remove probabilistic loss (restore delay-only, keep large queue)
ip netns exec ns_router_a tc qdisc change dev link2_a root netem delay 30ms limit 50000
```

#### Advantages of Dual Router Architecture

| Advantage | Description |
|-----------|-------------|
| **No queue flush** | Latency changes via routing, not netem reconfiguration |
| **Realistic transitions** | Packets in-flight continue through their path |
| **Fixed latency links** | Set once at startup, never modified |
| **Simple loss injection** | Blackhole routes for 100% loss, netem for probabilistic loss |
| **No nftables dependency** | Uses only iproute2 and tc (already required for namespaces) |
| **Instant loss events** | Blackhole route change is immediate (kernel routing table) |
| **Independent paths** | Can test different latencies simultaneously if needed |
| **50k queue preserved** | Latency queues maintain their contents during transitions |

### Shell Script: Namespace Setup

```bash
#!/bin/bash
# setup_network.sh - Create isolated network namespaces for SRT testing
# Dual-router architecture with fixed latency links
#
# shellcheck disable=SC2086  # We intentionally use unquoted variables for arrays

set -euo pipefail

#=============================================================================
# CONFIGURATION - Human-readable names at the top for easy modification
#=============================================================================

# Unique suffix for this test run (allows parallel test runs)
readonly TEST_ID="${TEST_ID:-$$}"

# Namespace names
readonly NS_PUBLISHER="ns_publisher_${TEST_ID}"
readonly NS_SUBSCRIBER="ns_subscriber_${TEST_ID}"
readonly NS_SERVER="ns_server_${TEST_ID}"
readonly NS_ROUTER_A="ns_router_a_${TEST_ID}"  # Client-side router
readonly NS_ROUTER_B="ns_router_b_${TEST_ID}"  # Server-side router

# IP Subnets
readonly SUBNET_PUBLISHER="10.1.1"      # Publisher <-> Router A
readonly SUBNET_SUBSCRIBER="10.1.2"     # Subscriber <-> Router A
readonly SUBNET_SERVER="10.2.1"         # Server <-> Router B
readonly SUBNET_INTERLINK="10.100"      # Inter-router links

# Netem queue limit (prevent tail-drop during high latency)
readonly NETEM_QUEUE_LIMIT=50000

# Latency profiles (RTT in milliseconds, netem delay = RTT/2)
# Format: "link_index:rtt_ms:description"
readonly LATENCY_LINK_0="0:0:no_delay"
readonly LATENCY_LINK_1="1:10:regional_dc"
readonly LATENCY_LINK_2="2:60:cross_continental"
readonly LATENCY_LINK_3="3:130:intercontinental"
readonly LATENCY_LINK_4="4:300:geo_satellite"

# State file for cleanup
readonly STATE_FILE="/tmp/srt_network_state_${TEST_ID}"

#=============================================================================
# HELPER FUNCTIONS
#=============================================================================

log_info() {
    echo "[INFO] $*"
}

log_error() {
    echo "[ERROR] $*" >&2
}

# Run command in a namespace
ns_exec() {
    local namespace="$1"
    shift
    ip netns exec "${namespace}" "$@"
}

# Create a veth pair between a namespace and a router
create_veth_to_router() {
    local ns_name="$1"
    local router_ns="$2"
    local ns_iface="$3"
    local router_iface="$4"
    local ns_ip="$5"
    local router_ip="$6"
    local subnet_prefix="$7"

    log_info "Creating veth: ${ns_name}/${ns_iface} <-> ${router_ns}/${router_iface}"

    # Create veth pair
    ip link add "${ns_iface}" type veth peer name "${router_iface}"

    # Move interfaces to their namespaces
    ip link set "${ns_iface}" netns "${ns_name}"
    ip link set "${router_iface}" netns "${router_ns}"

    # Configure namespace side
    ns_exec "${ns_name}" ip addr add "${ns_ip}/24" dev "${ns_iface}"
    ns_exec "${ns_name}" ip link set "${ns_iface}" up
    ns_exec "${ns_name}" ip link set lo up
    ns_exec "${ns_name}" ip route add default via "${router_ip}"

    # Configure router side
    ns_exec "${router_ns}" ip addr add "${router_ip}/24" dev "${router_iface}"
    ns_exec "${router_ns}" ip link set "${router_iface}" up
}

# Create an inter-router link with fixed latency
create_interrouter_link() {
    local link_index="$1"
    local rtt_ms="$2"
    local description="$3"

    local iface_a="link${link_index}_a"
    local iface_b="link${link_index}_b"
    local ip_a="${SUBNET_INTERLINK}.${link_index}.1"
    local ip_b="${SUBNET_INTERLINK}.${link_index}.2"
    local delay_ms=$((rtt_ms / 2))

    log_info "Creating inter-router link ${link_index}: ${rtt_ms}ms RTT (${description})"

    # Create veth pair
    ip link add "${iface_a}" type veth peer name "${iface_b}"

    # Move to router namespaces
    ip link set "${iface_a}" netns "${NS_ROUTER_A}"
    ip link set "${iface_b}" netns "${NS_ROUTER_B}"

    # Configure Router A side
    ns_exec "${NS_ROUTER_A}" ip addr add "${ip_a}/30" dev "${iface_a}"
    ns_exec "${NS_ROUTER_A}" ip link set "${iface_a}" up

    # Configure Router B side
    ns_exec "${NS_ROUTER_B}" ip addr add "${ip_b}/30" dev "${iface_b}"
    ns_exec "${NS_ROUTER_B}" ip link set "${iface_b}" up

    # Apply netem latency (only if RTT > 0)
    if [[ "${delay_ms}" -gt 0 ]]; then
        ns_exec "${NS_ROUTER_A}" tc qdisc add dev "${iface_a}" root netem \
            delay "${delay_ms}ms" limit "${NETEM_QUEUE_LIMIT}"
        ns_exec "${NS_ROUTER_B}" tc qdisc add dev "${iface_b}" root netem \
            delay "${delay_ms}ms" limit "${NETEM_QUEUE_LIMIT}"
    fi
}

# Set the active latency profile by changing routing
set_latency_profile() {
    local link_index="$1"
    local next_hop_b="${SUBNET_INTERLINK}.${link_index}.2"
    local next_hop_a="${SUBNET_INTERLINK}.${link_index}.1"

    log_info "Switching to latency profile: link${link_index}"

    # Router A: Route to server subnet via Router B
    ns_exec "${NS_ROUTER_A}" ip route replace "${SUBNET_SERVER}.0/24" via "${next_hop_b}"

    # Router B: Route to publisher/subscriber subnets via Router A
    ns_exec "${NS_ROUTER_B}" ip route replace "${SUBNET_PUBLISHER}.0/24" via "${next_hop_a}"
    ns_exec "${NS_ROUTER_B}" ip route replace "${SUBNET_SUBSCRIBER}.0/24" via "${next_hop_a}"
}

#=============================================================================
# CLEANUP
#=============================================================================

cleanup() {
    log_info "Cleaning up network namespaces..."

    # Delete namespaces (this also removes their interfaces)
    ip netns del "${NS_PUBLISHER}" 2>/dev/null || true
    ip netns del "${NS_SUBSCRIBER}" 2>/dev/null || true
    ip netns del "${NS_SERVER}" 2>/dev/null || true
    ip netns del "${NS_ROUTER_A}" 2>/dev/null || true
    ip netns del "${NS_ROUTER_B}" 2>/dev/null || true

    # Remove state file
    rm -f "${STATE_FILE}"

    log_info "Cleanup complete"
}

# Register cleanup on exit
trap cleanup EXIT

#=============================================================================
# MAIN SETUP
#=============================================================================

main() {
    log_info "Creating SRT test network (ID: ${TEST_ID})"

    # Create all namespaces
    log_info "Creating namespaces..."
    ip netns add "${NS_PUBLISHER}"
    ip netns add "${NS_SUBSCRIBER}"
    ip netns add "${NS_SERVER}"
    ip netns add "${NS_ROUTER_A}"
    ip netns add "${NS_ROUTER_B}"

    # Enable IP forwarding on routers
    ns_exec "${NS_ROUTER_A}" sysctl -qw net.ipv4.ip_forward=1
    ns_exec "${NS_ROUTER_B}" sysctl -qw net.ipv4.ip_forward=1

    # Create endpoint connections to Router A
    create_veth_to_router "${NS_PUBLISHER}" "${NS_ROUTER_A}" \
        "eth0" "eth_pub" \
        "${SUBNET_PUBLISHER}.2" "${SUBNET_PUBLISHER}.1" "${SUBNET_PUBLISHER}"

    create_veth_to_router "${NS_SUBSCRIBER}" "${NS_ROUTER_A}" \
        "eth0" "eth_sub" \
        "${SUBNET_SUBSCRIBER}.2" "${SUBNET_SUBSCRIBER}.1" "${SUBNET_SUBSCRIBER}"

    # Create server connection to Router B
    create_veth_to_router "${NS_SERVER}" "${NS_ROUTER_B}" \
        "eth0" "eth_srv" \
        "${SUBNET_SERVER}.2" "${SUBNET_SERVER}.1" "${SUBNET_SERVER}"

    # Create inter-router links with fixed latency
    log_info "Creating inter-router links with fixed latency..."

    # Parse and create each latency link
    for link_def in "${LATENCY_LINK_0}" "${LATENCY_LINK_1}" "${LATENCY_LINK_2}" \
                    "${LATENCY_LINK_3}" "${LATENCY_LINK_4}"; do
        IFS=':' read -r idx rtt desc <<< "${link_def}"
        create_interrouter_link "${idx}" "${rtt}" "${desc}"
    done

    # Set initial routing to use link0 (no latency)
    set_latency_profile 0

    # No nftables setup needed - we use:
    # - Blackhole routes for 100% loss (Starlink events)
    # - Netem loss parameter for probabilistic loss

    # Save state for external tools
    cat > "${STATE_FILE}" << EOF
TEST_ID=${TEST_ID}
NS_PUBLISHER=${NS_PUBLISHER}
NS_SUBSCRIBER=${NS_SUBSCRIBER}
NS_SERVER=${NS_SERVER}
NS_ROUTER_A=${NS_ROUTER_A}
NS_ROUTER_B=${NS_ROUTER_B}
PUBLISHER_IP=${SUBNET_PUBLISHER}.2
SUBSCRIBER_IP=${SUBNET_SUBSCRIBER}.2
SERVER_IP=${SUBNET_SERVER}.2
EOF

    # Print summary
    log_info "=========================================="
    log_info "Network setup complete!"
    log_info "=========================================="
    log_info ""
    log_info "Namespaces:"
    log_info "  Publisher:   ${NS_PUBLISHER}  (${SUBNET_PUBLISHER}.2)"
    log_info "  Subscriber:  ${NS_SUBSCRIBER} (${SUBNET_SUBSCRIBER}.2)"
    log_info "  Server:      ${NS_SERVER}     (${SUBNET_SERVER}.2:6000)"
    log_info "  Router A:    ${NS_ROUTER_A}   (client-side)"
    log_info "  Router B:    ${NS_ROUTER_B}   (server-side)"
    log_info ""
    log_info "Inter-router links (fixed latency):"
    log_info "  Link 0: 0ms RTT    (no delay)"
    log_info "  Link 1: 10ms RTT   (regional DC)"
    log_info "  Link 2: 60ms RTT   (cross-continental)"
    log_info "  Link 3: 130ms RTT  (intercontinental)"
    log_info "  Link 4: 300ms RTT  (GEO satellite)"
    log_info ""
    log_info "Current latency profile: Link 0 (0ms)"
    log_info ""
    log_info "To run commands in namespaces:"
    log_info "  ip netns exec ${NS_SERVER} ./server -addr ${SUBNET_SERVER}.2:6000"
    log_info "  ip netns exec ${NS_PUBLISHER} ./client-generator -to srt://${SUBNET_SERVER}.2:6000"
    log_info "  ip netns exec ${NS_SUBSCRIBER} ./client -from srt://${SUBNET_SERVER}.2:6000"
    log_info ""
    log_info "To switch latency profile:"
    log_info "  source ${STATE_FILE} && set_latency_profile 2  # 60ms RTT"
    log_info ""
    log_info "State file: ${STATE_FILE}"
}

# Run main if executed directly (not sourced)
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
```

### Go Controller: Dynamic Pattern Management

```go
// netem_controller.go - Dynamic network impairment controller
// Uses blackhole routes for 100% loss (Starlink events)
// Uses netem loss parameter for probabilistic loss
// Uses routing changes for latency switching (no queue flush)

package netemcontroller

import (
    "context"
    "fmt"
    "os/exec"
    "sync"
    "time"
)

// NetworkController manages network impairment via blackhole routes, netem, and routing
type NetworkController struct {
    testID       string        // Test run ID for namespace names
    nsRouterA    string        // Client-side router namespace
    nsRouterB    string        // Server-side router namespace
    mu           sync.Mutex
    ctx          context.Context
    cancel       context.CancelFunc
    wg           sync.WaitGroup
    currentLink  int           // Currently active latency link (0-4)
}

// LatencyProfile represents a pre-configured latency path
type LatencyProfile struct {
    Name       string
    RTTMs      int    // Round-trip time in milliseconds
    LinkIndex  int    // Which inter-router link to use (0-4)
}

// Predefined latency profiles (RTT values)
var (
    LatencyNone   = LatencyProfile{"none", 0, 0}
    LatencyTier1  = LatencyProfile{"tier1-10ms", 10, 1}     // Regional DC
    LatencyTier2  = LatencyProfile{"tier2-60ms", 60, 2}     // Cross-continental
    LatencyTier3  = LatencyProfile{"tier3-130ms", 130, 3}   // Intercontinental
    LatencyGEO    = LatencyProfile{"geo-300ms", 300, 4}     // GEO Satellite
)

// LossPattern represents a time-based loss pattern
type LossPattern struct {
    Name   string
    Events []LossEvent
}

// LossEvent represents a single loss injection event
type LossEvent struct {
    OffsetInMinute time.Duration  // When in the minute to trigger
    LossPercent    int            // 0-100 (100 = total drop)
    DurationMs     int            // How long the loss lasts
}

// Predefined loss patterns
var (
    // Starlink LEO reconvergence: 100% drop at seconds 12, 27, 42, 57
    PatternStarlink = LossPattern{
        Name: "starlink",
        Events: []LossEvent{
            {OffsetInMinute: 12 * time.Second, LossPercent: 100, DurationMs: 60},
            {OffsetInMinute: 27 * time.Second, LossPercent: 100, DurationMs: 60},
            {OffsetInMinute: 42 * time.Second, LossPercent: 100, DurationMs: 60},
            {OffsetInMinute: 57 * time.Second, LossPercent: 100, DurationMs: 60},
        },
    }

    // High loss burst: 85% drop at 1.5s into each minute
    PatternHighLoss = LossPattern{
        Name: "high-loss",
        Events: []LossEvent{
            {OffsetInMinute: 1500 * time.Millisecond, LossPercent: 85, DurationMs: 1000},
        },
    }

    // Combined: Starlink + High Loss
    PatternStarlinkWithHighLoss = LossPattern{
        Name: "starlink-high-loss",
        Events: []LossEvent{
            {OffsetInMinute: 1500 * time.Millisecond, LossPercent: 85, DurationMs: 1000},
            {OffsetInMinute: 12 * time.Second, LossPercent: 100, DurationMs: 60},
            {OffsetInMinute: 27 * time.Second, LossPercent: 100, DurationMs: 60},
            {OffsetInMinute: 42 * time.Second, LossPercent: 100, DurationMs: 60},
            {OffsetInMinute: 57 * time.Second, LossPercent: 100, DurationMs: 60},
        },
    }
)

// Network subnets (must match setup_network.sh)
const (
    subnetPublisher  = "10.1.1"
    subnetSubscriber = "10.1.2"
    subnetServer     = "10.2.1"
    subnetInterlink  = "10.100"
)

// NewNetworkController creates a controller for the given test ID
func NewNetworkController(testID string) *NetworkController {
    ctx, cancel := context.WithCancel(context.Background())
    return &NetworkController{
        testID:      testID,
        nsRouterA:   fmt.Sprintf("ns_router_a_%s", testID),
        nsRouterB:   fmt.Sprintf("ns_router_b_%s", testID),
        ctx:         ctx,
        cancel:      cancel,
        currentLink: 0,
    }
}

// SetLatencyProfile switches to a different latency path via routing
// This does NOT flush any queues - packets in flight continue on their path
func (c *NetworkController) SetLatencyProfile(profile LatencyProfile) error {
    c.mu.Lock()
    defer c.mu.Unlock()

    if profile.LinkIndex == c.currentLink {
        return nil // Already on this link
    }

    nextHopB := fmt.Sprintf("%s.%d.2", subnetInterlink, profile.LinkIndex)
    nextHopA := fmt.Sprintf("%s.%d.1", subnetInterlink, profile.LinkIndex)

    // Update Router A: route to server via selected link
    if err := c.nsExec(c.nsRouterA, "ip", "route", "replace",
        subnetServer+".0/24", "via", nextHopB); err != nil {
        return fmt.Errorf("failed to update router_a route: %w", err)
    }

    // Update Router B: route to publisher/subscriber via selected link
    if err := c.nsExec(c.nsRouterB, "ip", "route", "replace",
        subnetPublisher+".0/24", "via", nextHopA); err != nil {
        return fmt.Errorf("failed to update router_b pub route: %w", err)
    }
    if err := c.nsExec(c.nsRouterB, "ip", "route", "replace",
        subnetSubscriber+".0/24", "via", nextHopA); err != nil {
        return fmt.Errorf("failed to update router_b sub route: %w", err)
    }

    c.currentLink = profile.LinkIndex
    return nil
}

// SetLoss applies a loss rate:
// - 100% loss uses blackhole routes (instant, for Starlink events)
// - Probabilistic loss uses netem loss parameter on inter-router links
func (c *NetworkController) SetLoss(percent int) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.applyNftLoss(percent)
}

// ClearLoss removes loss injection
func (c *NetworkController) ClearLoss() error {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.clearBlackhole()
    return c.clearNetemLoss()
}

// StartPattern starts a dynamic loss pattern
func (c *NetworkController) StartPattern(pattern LossPattern) {
    c.wg.Add(1)
    go c.runPattern(pattern)
}

// Stop stops all patterns and clears rules
func (c *NetworkController) Stop() {
    c.cancel()
    c.wg.Wait()
    c.ClearLoss()
}

// applyLoss applies loss using the appropriate method:
// - 100% loss: blackhole route (instant)
// - Probabilistic loss: netem loss parameter
func (c *NetworkController) applyLoss(percent int) error {
    // Clear any existing loss configuration
    c.clearBlackhole()
    _ = c.clearNetemLoss()

    if percent == 0 {
        return nil
    }

    if percent == 100 {
        // Total drop using blackhole route - instant effect
        // Add blackhole routes for both server and subscriber subnets
        if err := c.nsExec(c.nsRouterA, "ip", "route", "add", "blackhole", c.serverSubnet); err != nil {
            return err
        }
        return c.nsExec(c.nsRouterA, "ip", "route", "add", "blackhole", c.subscriberSubnet)
    }

    // Probabilistic loss using netem on current latency link
    // Modify the existing netem qdisc to add loss parameter
    // IMPORTANT: Always include "limit 50000" to prevent netem tail drops
    currentLink := fmt.Sprintf("link%d_a", c.currentLatency)
    delay := c.getDelayForLatency(c.currentLatency)
    return c.nsExec(c.nsRouterA, "tc", "qdisc", "change", "dev", currentLink,
        "root", "netem", "delay", delay, "loss", fmt.Sprintf("%d%%", percent), "limit", "50000")
}

// clearBlackhole removes blackhole routes
func (c *NetworkController) clearBlackhole() {
    // Ignore errors - routes may not exist
    _ = c.nsExec(c.nsRouterA, "ip", "route", "del", "blackhole", c.serverSubnet)
    _ = c.nsExec(c.nsRouterA, "ip", "route", "del", "blackhole", c.subscriberSubnet)
}

// clearNetemLoss removes loss parameter from netem (restores delay-only, keeps large queue)
func (c *NetworkController) clearNetemLoss() error {
    currentLink := fmt.Sprintf("link%d_a", c.currentLatency)
    delay := c.getDelayForLatency(c.currentLatency)
    // Keep limit 50000 to prevent tail drops
    return c.nsExec(c.nsRouterA, "tc", "qdisc", "change", "dev", currentLink,
        "root", "netem", "delay", delay, "limit", "50000")
}

// getDelayForLatency returns the netem delay string for a latency profile
func (c *NetworkController) getDelayForLatency(profile int) string {
    delays := []string{"0ms", "5ms", "30ms", "65ms", "150ms"}
    if profile >= 0 && profile < len(delays) {
        return delays[profile]
    }
    return "0ms"
}

// nsExec runs a command in a network namespace
func (c *NetworkController) nsExec(ns string, args ...string) error {
    cmdArgs := append([]string{"netns", "exec", ns}, args...)
    cmd := exec.Command("ip", cmdArgs...)
    if output, err := cmd.CombinedOutput(); err != nil {
        return fmt.Errorf("%s: %s", err, string(output))
    }
    return nil
}

func (c *NetworkController) runPattern(pattern LossPattern) {
    defer c.wg.Done()

    for {
        // Calculate time to next minute boundary
        now := time.Now()
        minuteStart := now.Truncate(time.Minute)

        // Process events for this minute
        for _, event := range pattern.Events {
            eventTime := minuteStart.Add(event.OffsetInMinute)

            // Skip if event already passed in this minute
            if eventTime.Before(now) {
                continue
            }

            // Wait until event time
            select {
            case <-c.ctx.Done():
                return
            case <-time.After(time.Until(eventTime)):
            }

            // Apply loss (blackhole for 100%, netem for probabilistic)
            c.mu.Lock()
            if err := c.applyLoss(event.LossPercent); err != nil {
                fmt.Printf("Error applying loss: %v\n", err)
            }
            c.mu.Unlock()

            // Wait for duration
            select {
            case <-c.ctx.Done():
                return
            case <-time.After(time.Duration(event.DurationMs) * time.Millisecond):
            }

            // Clear loss
            c.mu.Lock()
            if err := c.clearNftLoss(); err != nil {
                fmt.Printf("Error clearing loss: %v\n", err)
            }
            c.mu.Unlock()
        }

        // Wait for next minute
        nextMinute := minuteStart.Add(time.Minute)
        select {
        case <-c.ctx.Done():
            return
        case <-time.After(time.Until(nextMinute)):
        }
    }
}
```

### Command Reference

With the dual-router architecture:
- **Latency** is controlled via **routing** (switching between fixed-latency links)
- **100% Loss** (Starlink, outages) is controlled via **blackhole routes** (instant effect)
- **Probabilistic Loss** is controlled via **netem loss parameter** on current latency link
- **Netem delay** is applied once at setup to inter-router links (fixed latency values)

#### Latency Switching via Routing

```bash
# ============================================================================
# Latency is switched by changing routes - NO queue flush occurs
# The netem delays on inter-router links are fixed at setup time
# ============================================================================

# Variables (from state file)
NS_ROUTER_A="ns_router_a_$$"
NS_ROUTER_B="ns_router_b_$$"
SUBNET_SERVER="10.2.1"
SUBNET_PUBLISHER="10.1.1"
SUBNET_SUBSCRIBER="10.1.2"

# Switch to 0ms RTT (link0)
ip netns exec "${NS_ROUTER_A}" ip route replace "${SUBNET_SERVER}.0/24" via 10.100.0.2
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_PUBLISHER}.0/24" via 10.100.0.1
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_SUBSCRIBER}.0/24" via 10.100.0.1

# Switch to 10ms RTT (link1)
ip netns exec "${NS_ROUTER_A}" ip route replace "${SUBNET_SERVER}.0/24" via 10.100.1.2
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_PUBLISHER}.0/24" via 10.100.1.1
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_SUBSCRIBER}.0/24" via 10.100.1.1

# Switch to 60ms RTT (link2)
ip netns exec "${NS_ROUTER_A}" ip route replace "${SUBNET_SERVER}.0/24" via 10.100.2.2
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_PUBLISHER}.0/24" via 10.100.2.1
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_SUBSCRIBER}.0/24" via 10.100.2.1

# Switch to 130ms RTT (link3)
ip netns exec "${NS_ROUTER_A}" ip route replace "${SUBNET_SERVER}.0/24" via 10.100.3.2
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_PUBLISHER}.0/24" via 10.100.3.1
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_SUBSCRIBER}.0/24" via 10.100.3.1

# Switch to 300ms RTT (link4) - GEO satellite
ip netns exec "${NS_ROUTER_A}" ip route replace "${SUBNET_SERVER}.0/24" via 10.100.4.2
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_PUBLISHER}.0/24" via 10.100.4.1
ip netns exec "${NS_ROUTER_B}" ip route replace "${SUBNET_SUBSCRIBER}.0/24" via 10.100.4.1

# View current routes
ip netns exec "${NS_ROUTER_A}" ip route
ip netns exec "${NS_ROUTER_B}" ip route
```

#### Loss Injection via Null Routes and Netem

```bash
# ============================================================================
# Loss is injected using two mechanisms:
# - Blackhole routes: 100% loss (Starlink events, complete outages) - instant
# - Netem loss parameter: Probabilistic loss (2%, 5%, etc.)
# No nftables dependency - uses only iproute2 and tc
# ============================================================================

NS_ROUTER_A="ns_router_a_$$"
SUBNET_SERVER="10.2.1.0/24"
SUBNET_SUBSCRIBER="10.1.2.0/24"

# === Starlink Event: 100% drop for 60ms using blackhole routes ===
# Add blackhole routes - instant effect, kernel routing table update
ip netns exec "${NS_ROUTER_A}" ip route add blackhole "${SUBNET_SERVER}"
ip netns exec "${NS_ROUTER_A}" ip route add blackhole "${SUBNET_SUBSCRIBER}"
sleep 0.06
# Remove blackhole routes - restore normal routing
ip netns exec "${NS_ROUTER_A}" ip route del blackhole "${SUBNET_SERVER}"
ip netns exec "${NS_ROUTER_A}" ip route del blackhole "${SUBNET_SUBSCRIBER}"

# === Probabilistic Loss using netem ===
# Add loss parameter to existing netem qdisc on current latency link
# IMPORTANT: Always include "limit 50000" to prevent netem tail drops
# Example: link2 has 30ms delay, add 5% loss
ip netns exec "${NS_ROUTER_A}" tc qdisc change dev link2_a root netem delay 30ms loss 5% limit 50000

# High loss event: 85% drop for 1 second
ip netns exec "${NS_ROUTER_A}" tc qdisc change dev link2_a root netem delay 30ms loss 85% limit 50000
sleep 1
# Remove loss (restore delay-only, keep large queue)
ip netns exec "${NS_ROUTER_A}" tc qdisc change dev link2_a root netem delay 30ms limit 50000

# === Static probability-based loss examples ===
# 2% loss on tier2 link (30ms delay)
ip netns exec "${NS_ROUTER_A}" tc qdisc change dev link2_a root netem delay 30ms loss 2% limit 50000

# 5% loss on tier3 link (65ms delay)
ip netns exec "${NS_ROUTER_A}" tc qdisc change dev link3_a root netem delay 65ms loss 5% limit 50000

# === Clear all loss (restore delay-only, keep large queue) ===
ip netns exec "${NS_ROUTER_A}" tc qdisc change dev link2_a root netem delay 30ms limit 50000

# === View current configuration ===
ip netns exec "${NS_ROUTER_A}" ip route show
ip netns exec "${NS_ROUTER_A}" tc qdisc show
```

#### Netem Setup (Applied Once at Startup)

```bash
# ============================================================================
# Netem delays are fixed at setup time - NEVER changed during tests
# This prevents queue flush issues
# ============================================================================

NS_ROUTER_A="ns_router_a_$$"
NS_ROUTER_B="ns_router_b_$$"
NETEM_QUEUE_LIMIT=50000

# Link 0: No delay (0ms RTT)
# (no netem needed)

# Link 1: 5ms each way = 10ms RTT
ip netns exec "${NS_ROUTER_A}" tc qdisc add dev link1_a root netem delay 5ms limit "${NETEM_QUEUE_LIMIT}"
ip netns exec "${NS_ROUTER_B}" tc qdisc add dev link1_b root netem delay 5ms limit "${NETEM_QUEUE_LIMIT}"

# Link 2: 30ms each way = 60ms RTT
ip netns exec "${NS_ROUTER_A}" tc qdisc add dev link2_a root netem delay 30ms limit "${NETEM_QUEUE_LIMIT}"
ip netns exec "${NS_ROUTER_B}" tc qdisc add dev link2_b root netem delay 30ms limit "${NETEM_QUEUE_LIMIT}"

# Link 3: 65ms each way = 130ms RTT
ip netns exec "${NS_ROUTER_A}" tc qdisc add dev link3_a root netem delay 65ms limit "${NETEM_QUEUE_LIMIT}"
ip netns exec "${NS_ROUTER_B}" tc qdisc add dev link3_b root netem delay 65ms limit "${NETEM_QUEUE_LIMIT}"

# Link 4: 150ms each way = 300ms RTT (GEO satellite)
ip netns exec "${NS_ROUTER_A}" tc qdisc add dev link4_a root netem delay 150ms limit "${NETEM_QUEUE_LIMIT}"
ip netns exec "${NS_ROUTER_B}" tc qdisc add dev link4_b root netem delay 150ms limit "${NETEM_QUEUE_LIMIT}"

# View netem status
ip netns exec "${NS_ROUTER_A}" tc qdisc show
ip netns exec "${NS_ROUTER_B}" tc qdisc show
```

### Integration Test Flow

```
1. Setup Phase
   ├── Create namespaces (setup_network.sh)
   ├── Start netem controller
   ├── Start server in ns_srv with -promuds /tmp/srt_server.sock
   ├── Start client-generator in ns_pub with -promuds /tmp/srt_clientgen.sock
   └── Start client in ns_sub with -promuds /tmp/srt_client.sock

2. Test Phase (per configuration)
   ├── Apply impairment pattern (e.g., "starlink")
   ├── Run for test duration
   ├── Collect metrics via UDS (curl --unix-socket)
   └── Verify SRT recovery metrics

3. Teardown Phase
   ├── Stop netem controller
   ├── Stop all processes
   ├── Cleanup namespaces
   └── Generate report
```

### Metrics Collection via Unix Domain Sockets

Since each GoSRT process runs in an isolated network namespace, TCP-based Prometheus
endpoints are not accessible from the host or integration test orchestrator. **Unix
Domain Sockets (UDS)** solve this problem because socket files are accessible via
the shared filesystem regardless of network namespace isolation.

#### Starting Processes with UDS Metrics

```bash
# Server in ns_srv namespace
ip netns exec ns_srv ./server -addr 10.0.1.1:6000 \
    -promuds /tmp/srt_metrics_server.sock

# Client-generator in ns_pub namespace
ip netns exec ns_pub ./client-generator -to srt://10.0.1.1:6000/stream \
    -promuds /tmp/srt_metrics_clientgen.sock

# Client in ns_sub namespace
ip netns exec ns_sub ./client -from srt://10.0.1.1:6000?streamid=subscribe:/stream \
    -promuds /tmp/srt_metrics_client.sock
```

#### Collecting Metrics from Host

```bash
# Query server metrics (from host or orchestrator)
curl --unix-socket /tmp/srt_metrics_server.sock http://localhost/metrics

# Query client-generator metrics
curl --unix-socket /tmp/srt_metrics_clientgen.sock http://localhost/metrics

# Query client metrics
curl --unix-socket /tmp/srt_metrics_client.sock http://localhost/metrics

# Filter for SRT-specific counters
curl -s --unix-socket /tmp/srt_metrics_server.sock http://localhost/metrics | grep gosrt_
```

#### Integration with Go Test Orchestrator

```go
// Use common.MetricsClient to fetch metrics via UDS
client := common.NewMetricsClient()

// Fetch from each component via socket path
serverMetrics, _ := client.FetchUDS("/tmp/srt_metrics_server.sock")
clientGenMetrics, _ := client.FetchUDS("/tmp/srt_metrics_clientgen.sock")
clientMetrics, _ := client.FetchUDS("/tmp/srt_metrics_client.sock")
```

**See**: `prometheus_uds_design.md` for full UDS implementation details.

### File Structure

```
contrib/integration_testing/
├── netem/
│   ├── setup_network.sh      # Namespace setup script
│   ├── teardown_network.sh   # Cleanup script
│   ├── controller.go         # Go netem controller
│   └── patterns.go           # Predefined impairment patterns
├── loss_test_configs.go      # Test configs with impairment
└── loss_tests.go             # Loss injection test orchestration
```

---

## Test Configuration Integration

Integrate impairment settings with `TestConfig`:

```go
type NetworkImpairment struct {
    Enabled bool

    // Target interface (which link to impair)
    Target string  // "subscriber", "publisher", "server" (default: "subscriber")

    // Static loss configuration
    LossRate        float64  // 0.0-1.0 (e.g., 0.02 = 2%)
    LossCorrelation float64  // 0.0-1.0 for burst loss (e.g., 0.25 = 25%)

    // Latency configuration
    LatencyProfile string        // "none", "tier1-low", "tier2-medium", "tier3-high", "geo-satellite"
    Latency        time.Duration // Custom latency (overrides profile if non-zero)
    Jitter         time.Duration // Custom jitter (overrides profile if non-zero)
    Distribution   string        // "normal", "pareto", "uniform" (default: "normal")

    // Dynamic patterns (overrides static settings)
    Pattern string  // "clean", "starlink", "high-loss", "starlink-high-loss"

    // Queue limit (defaults to 50000 if zero)
    QueueLimit int
}

// Predefined impairment patterns
var ImpairmentPatterns = map[string]NetworkImpairment{
    // ===== No impairment =====
    "clean": {},

    // ===== Latency Profiles (no loss) =====
    // Tier 1: Low latency (10ms, regional datacenter)
    "latency-tier1": {Enabled: true, LatencyProfile: "tier1-low"},

    // Tier 2: Medium latency (60ms, cross-continental)
    "latency-tier2": {Enabled: true, LatencyProfile: "tier2-medium"},

    // Tier 3: High latency (130ms, intercontinental)
    "latency-tier3": {Enabled: true, LatencyProfile: "tier3-high"},

    // GEO Satellite (300ms one-way, 600ms RTT)
    "latency-geo": {Enabled: true, LatencyProfile: "geo-satellite"},

    // ===== Static loss rates (no latency) =====
    "lossy-1pct":  {Enabled: true, LossRate: 0.01},
    "lossy-2pct":  {Enabled: true, LossRate: 0.02},
    "lossy-5pct":  {Enabled: true, LossRate: 0.05},
    "lossy-10pct": {Enabled: true, LossRate: 0.10},

    // Burst loss (correlated)
    "burst-loss": {Enabled: true, LossRate: 0.05, LossCorrelation: 0.25},

    // ===== Latency + Loss combinations =====
    // GEO satellite with typical loss
    "geo-with-loss": {
        Enabled:        true,
        LatencyProfile: "geo-satellite",
        LossRate:       0.005, // 0.5% loss typical for satellite
    },

    // Intercontinental with loss
    "tier3-with-loss": {
        Enabled:        true,
        LatencyProfile: "tier3-high",
        LossRate:       0.02, // 2% loss
    },

    // Cross-continental with loss
    "tier2-with-loss": {
        Enabled:        true,
        LatencyProfile: "tier2-medium",
        LossRate:       0.01, // 1% loss
    },

    // ===== Dynamic patterns =====
    // Starlink reconvergence pattern
    // 100% loss for 50-70ms at seconds 12, 27, 42, 57 of each minute
    "starlink": {Enabled: true, Pattern: "starlink"},

    // High loss burst pattern
    // 80-90% loss for 1 second at 1.5s into each minute
    "high-loss": {Enabled: true, Pattern: "high-loss"},

    // Combined: Starlink + High Loss
    "starlink-high-loss": {Enabled: true, Pattern: "starlink-high-loss"},
}

// Example test configurations with impairment
var LossTestConfigs = []TestConfig{
    {
        Name:        "Starlink-2Mbps",
        Description: "2 Mb/s with Starlink reconvergence pattern",
        Bitrate:     2_000_000,
        TestDuration: 2 * time.Minute, // Run at least 2 minutes to see pattern
        Impairment:  ImpairmentPatterns["starlink"],
    },
    {
        Name:        "HighLoss-2Mbps",
        Description: "2 Mb/s with periodic 85% loss bursts",
        Bitrate:     2_000_000,
        TestDuration: 2 * time.Minute,
        Impairment:  ImpairmentPatterns["high-loss"],
    },
    {
        Name:        "Starlink-HighLoss-2Mbps",
        Description: "2 Mb/s with Starlink + high loss patterns",
        Bitrate:     2_000_000,
        TestDuration: 2 * time.Minute,
        Impairment:  ImpairmentPatterns["starlink-high-loss"],
    },

    // ===== Latency Profile Tests =====
    {
        Name:        "Tier1-Latency-10Mbps",
        Description: "10 Mb/s with Tier 1 latency (10ms, regional DC)",
        Bitrate:     10_000_000,
        TestDuration: 30 * time.Second,
        Impairment:  ImpairmentPatterns["latency-tier1"],
    },
    {
        Name:        "Tier2-Latency-10Mbps",
        Description: "10 Mb/s with Tier 2 latency (60ms, cross-continental)",
        Bitrate:     10_000_000,
        TestDuration: 30 * time.Second,
        Impairment:  ImpairmentPatterns["latency-tier2"],
    },
    {
        Name:        "Tier3-Latency-10Mbps",
        Description: "10 Mb/s with Tier 3 latency (130ms, intercontinental)",
        Bitrate:     10_000_000,
        TestDuration: 30 * time.Second,
        Impairment:  ImpairmentPatterns["latency-tier3"],
    },
    {
        Name:        "GEO-Satellite-10Mbps",
        Description: "10 Mb/s with GEO satellite latency (300ms one-way, 600ms RTT)",
        Bitrate:     10_000_000,
        TestDuration: 30 * time.Second,
        Impairment:  ImpairmentPatterns["latency-geo"],
    },

    // ===== Latency + Loss Combination Tests =====
    {
        Name:        "GEO-WithLoss-5Mbps",
        Description: "5 Mb/s with GEO latency + 0.5% loss (realistic satellite)",
        Bitrate:     5_000_000,
        TestDuration: 1 * time.Minute,
        Impairment:  ImpairmentPatterns["geo-with-loss"],
    },
    {
        Name:        "Tier3-WithLoss-5Mbps",
        Description: "5 Mb/s with intercontinental latency + 2% loss",
        Bitrate:     5_000_000,
        TestDuration: 1 * time.Minute,
        Impairment:  ImpairmentPatterns["tier3-with-loss"],
    },
}
```

---

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-06 | Initial design document | - |
| 2024-12-06 | Corrected Starlink pattern to 100% loss | - |
| 2024-12-06 | Added high-loss burst pattern (80-90% at 1.5s) | - |
| 2024-12-06 | Reviewed amt.sh, added detailed design | - |
| 2024-12-06 | Added Go controller for dynamic patterns | - |
| 2024-12-06 | Added shell script for namespace setup | - |
| 2024-12-06 | Changed bridge to router architecture (4 namespaces) | - |
| 2024-12-06 | Added latency profiles: none, tier1 (10ms), tier2 (60ms), tier3 (130ms), GEO (300ms) | - |
| 2024-12-06 | Added 50k packet queue limit to prevent netem tail-drop | - |
| 2024-12-06 | Changed iptables references to nftables (nft) | - |
| 2024-12-06 | Corrected latency to RTT (netem delay = RTT/2) | - |
| 2024-12-06 | Dual router architecture: ns_router_a + ns_router_b with 5 parallel fixed-latency links | - |
| 2024-12-06 | Latency switching via routing (no queue flush) | - |
| 2024-12-06 | Loss injection via nftables DROP (no queue impact) | - |
| 2024-12-06 | Updated shell script to be shellcheck-compliant with readable variable names | - |
| 2024-12-06 | Added metrics collection via Unix Domain Sockets (UDS) for namespace isolation | - |
| 2024-12-08 | Replaced nftables with null/blackhole routes for 100% loss events | - |
| 2024-12-08 | Use netem loss parameter for probabilistic loss (simpler, no nftables dependency) | - |

