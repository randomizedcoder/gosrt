# Packet Loss Injection Implementation

## Overview

This document tracks the implementation progress of the packet loss injection framework for GoSRT
integration testing, based on the design in [packet_loss_injection_design.md](packet_loss_injection_design.md).

**Related Documents**:
- [Packet Loss Injection Design](packet_loss_injection_design.md) - The design document
- [Integration Testing Design](integration_testing_design.md) - Parent integration testing framework
- [Metrics Analysis Design](metrics_analysis_design.md) - Metrics validation for loss tests

---

## Implementation Status

### Phase 1: Network Scripts ✅ COMPLETE

Shell scripts for creating and managing the network namespace topology.

| Task | Status | File | Notes |
|------|--------|------|-------|
| Shared library (lib.sh) | ✅ Complete | `network/lib.sh` | Functions, variables, logging |
| Network setup script | ✅ Complete | `network/setup.sh` | Creates 5 namespaces |
| Cleanup script | ✅ Complete | `network/cleanup.sh` | Removes all namespaces |
| Latency switcher | ✅ Complete | `network/set_latency.sh` | Profiles 0-4 (0-300ms RTT) |
| Loss injection | ✅ Complete | `network/set_loss.sh` | 0-100% (blackhole + netem) |
| Starlink pattern | ✅ Complete | `network/starlink_pattern.sh` | 60ms 100% loss bursts |
| Network status display | ✅ Complete | `network/status.sh` | Human-friendly summary |
| Shellcheck validation | ✅ Complete | All scripts | All scripts pass shellcheck |

**Key Implementation Details**:
- All scripts use human-readable variable names
- 50,000 packet netem queue limit to prevent tail drops
- 100% loss uses blackhole routes (instant effect)
- Probabilistic loss uses netem loss parameter
- Latency switching via routing (no queue flush)

### Phase 2: Go Network Controller ✅ COMPLETE

Go wrapper for controlling network impairment from the test framework.

| Task | Status | File | Notes |
|------|--------|------|-------|
| NetworkController struct | ✅ Complete | `network_controller.go` | TestID, ScriptDir, namespace names, IPs |
| NewNetworkController | ✅ Complete | | Config-based constructor, auto-finds scripts |
| Setup method | ✅ Complete | | Calls setup.sh, sets isSetup flag |
| Cleanup method | ✅ Complete | | Stops patterns, calls cleanup.sh |
| SetLatencyProfile method | ✅ Complete | | Profiles 0-4, calls set_latency.sh |
| SetLoss method | ✅ Complete | | 0-100%, calls set_loss.sh |
| LossPattern type | ✅ Complete | | Events with offset, duration, percent |
| PatternStarlink | ✅ Complete | | Predefined Starlink reconvergence pattern |
| PatternHighLossBurst | ✅ Complete | | Predefined 85% loss burst pattern |
| StartPattern method | ✅ Complete | | Runs pattern in background goroutine |
| StopPattern method | ✅ Complete | | Cancels pattern, clears loss |
| RunInNamespace | ✅ Complete | | Executes command, returns output |
| StartProcessInNamespace | ✅ Complete | | Returns exec.Cmd for caller to manage |
| GetNamespace/GetIP | ✅ Complete | | Component name to namespace/IP lookup |
| Status method | ✅ Complete | | Calls status.sh, returns output |

**Key Features**:
- Thread-safe with mutex protection
- Context-aware for cancellation
- Automatic script directory detection
- Predefined loss patterns (Starlink, high-loss burst)
- Process spawning helpers for running binaries in namespaces

### Phase 3: Test Framework Integration ✅ COMPLETE

Integrate network impairment with the existing test framework.

| Task | Status | File | Notes |
|------|--------|------|-------|
| Add Mode field to TestConfig | ✅ Complete | `config.go` | `TestModeClean` / `TestModeNetwork` |
| Add Impairment field | ✅ Complete | `config.go` | `NetworkImpairment` struct |
| Network mode test runner | ✅ Complete | `test_network_mode.go` | `runNetworkModeTest()` |
| UDS metrics collection | ✅ Complete | `metrics_collector.go` | Already supports UDS |
| Process spawning in namespace | ✅ Complete | `test_network_mode.go` | `startProcessInNamespace()` |
| Cleanup on test failure | ✅ Complete | `test_network_mode.go` | `defer nc.Cleanup()` |
| Test mode dispatch | ✅ Complete | `test_graceful_shutdown.go` | Checks `config.Mode` |
| Latency profile support | ✅ Complete | `test_network_mode.go` | `getLatencyProfileIndex()` |
| Pattern support | ✅ Complete | `test_network_mode.go` | `getImpairmentPattern()` |
| CLI flag builders | ✅ Complete | `test_network_mode.go` | Uses namespace IPs |

**Key Features**:
- Automatic root privilege check
- Network namespace setup with defer cleanup
- UDS-based metrics collection (accessible from host)
- Impairment applied after connections established
- Graceful shutdown with SIGINT sequence
- Pattern cleanup before test end

### Phase 4: Network Impairment Test Configurations ✅ COMPLETE

Test configurations that use network impairment.

| Task | Status | File | Notes |
|------|--------|------|-------|
| 2% loss test | ✅ Complete | `test_configs.go` | `Network-Loss2pct-5Mbps` |
| 5% loss test | ✅ Complete | | `Network-Loss5pct-5Mbps` |
| 10% loss test | ✅ Complete | | `Network-Loss10pct-5Mbps` |
| Regional + 2% loss | ✅ Complete | | `Network-Regional-Loss2pct-5Mbps` (10ms RTT) |
| Continental + 2% loss | ✅ Complete | | `Network-Continental-Loss2pct-5Mbps` (60ms RTT) |
| Intercontinental + 5% loss | ✅ Complete | | `Network-Intercontinental-Loss5pct-5Mbps` (130ms RTT) |
| GEO Satellite + 2% loss | ✅ Complete | | `Network-GeoSatellite-Loss2pct-2Mbps` (300ms RTT) |
| Starlink pattern test | ✅ Complete | | `Network-Starlink-5Mbps` |
| High-loss burst test | ✅ Complete | | `Network-HighLossBurst-5Mbps` |
| Stress test | ✅ Complete | | `Network-Stress-HighLatencyHighLoss` |
| ExtraLargeBuffers config | ✅ Complete | | 5s latency, 4MB buffers |
| GetNetworkTestConfigByName | ✅ Complete | | Config lookup function |
| CLI: network-test | ✅ Complete | `test_graceful_shutdown.go` | Run single network test |
| CLI: network-test-all | ✅ Complete | | Run all network tests |
| CLI: list-network-configs | ✅ Complete | | List network configs |

**Network Test Configurations (10 total)**:
```
Network-Loss2pct-5Mbps               # Basic 2% loss
Network-Loss5pct-5Mbps               # Moderate 5% loss
Network-Loss10pct-5Mbps              # Heavy 10% loss
Network-Regional-Loss2pct-5Mbps      # 10ms RTT + 2% loss
Network-Continental-Loss2pct-5Mbps   # 60ms RTT + 2% loss
Network-Intercontinental-Loss5pct    # 130ms RTT + 5% loss
Network-GeoSatellite-Loss2pct-2Mbps  # 300ms RTT + 2% loss
Network-Starlink-5Mbps               # Starlink reconvergence pattern
Network-HighLossBurst-5Mbps          # 85% loss burst pattern
Network-Stress-HighLatencyHighLoss   # 130ms RTT + 10% loss @ 10Mbps
```

### Phase 5: Statistical Validation ✅ COMPLETE

Configurable tolerances for network impairment validation.

| Task | Status | File | Notes |
|------|--------|------|-------|
| `StatisticalThresholds` struct | ✅ Complete | `config.go` | Configurable per-test tolerances |
| `DefaultThresholds()` | ✅ Complete | | ±50% loss, 95% recovery |
| `HighLatencyThresholds()` | ✅ Complete | | ±60% loss, 90% recovery |
| `BurstLossThresholds()` | ✅ Complete | | ±100% loss, 85% recovery |
| `StressTestThresholds()` | ✅ Complete | | ±80% loss, 80% recovery |
| Threshold integration in analysis | ✅ Complete | `analysis.go` | Use config thresholds if set |
| Apply thresholds to network configs | ✅ Complete | `test_configs.go` | High-lat, burst, stress tests |

**Configurable Thresholds**:
```go
type StatisticalThresholds struct {
    LossRateTolerance float64 // ±X% of expected loss rate
    MinRetransRate    float64 // Minimum retransmission ratio
    MaxRetransRate    float64 // Maximum retransmission ratio
    MinNAKsPerLostPkt float64 // Minimum NAKs per lost packet
    MaxNAKsPerLostPkt float64 // Maximum NAKs per lost packet
    MinRecoveryRate   float64 // Minimum packet recovery rate
}
```

**Preset Thresholds Applied**:
- `Network-Intercontinental-*`: `HighLatencyThresholds()` (90% recovery)
- `Network-GeoSatellite-*`: `HighLatencyThresholds()` (90% recovery)
- `Network-Starlink-*`: `BurstLossThresholds()` (85% recovery)
- `Network-HighLossBurst-*`: `BurstLossThresholds()` (85% recovery)
- `Network-Stress-*`: `StressTestThresholds()` (80% recovery)

---

## File Structure

```
contrib/integration_testing/
├── network/
│   ├── lib.sh                 # ✅ Shared functions and variables
│   ├── setup.sh               # ✅ Create network namespaces
│   ├── cleanup.sh             # ✅ Remove network namespaces
│   ├── set_latency.sh         # ✅ Switch latency profile (0-4)
│   ├── set_loss.sh            # ✅ Set loss percentage (0-100)
│   ├── starlink_pattern.sh    # ✅ Starlink reconvergence pattern
│   └── status.sh              # ✅ Human-friendly network status display
├── network_controller.go      # ✅ Go wrapper for network control
├── test_network_mode.go       # ✅ Network mode test runner
├── test_graceful_shutdown.go  # ✅ Updated with mode dispatch
├── config.go                  # ✅ TestMode and NetworkImpairment types
├── analysis.go                # ✅ Statistical validation
└── test_configs.go            # 🔲 Network impairment test configs
```

---

## Network Topology

```
┌─────────────────┐                                    ┌─────────────────┐
│  ns_publisher   │                                    │  ns_subscriber  │
│   10.1.1.2      │──┐                              ┌──│   10.1.2.2      │
└─────────────────┘  │                              │  └─────────────────┘
                     │                              │
                     ▼                              ▼
              ┌─────────────────────────────────────────────┐
              │              ns_router_a                     │
              │         (Client-side Router)                 │
              │                                              │
              │  eth_pub: 10.1.1.1    eth_sub: 10.1.2.1     │
              │                                              │
              │  link0_a ─────────────── link0_b (0ms RTT)  │
              │  link1_a ─────────────── link1_b (10ms)     │
              │  link2_a ─────────────── link2_b (60ms)     │
              │  link3_a ─────────────── link3_b (130ms)    │
              │  link4_a ─────────────── link4_b (300ms)    │
              └──────────────────┬──────────────────────────┘
                                 │
                    5 parallel veth pairs
                    (fixed latency each)
                                 │
              ┌──────────────────▼──────────────────────────┐
              │              ns_router_b                     │
              │         (Server-side Router)                 │
              │                                              │
              │  eth_srv: 10.2.1.1                          │
              └──────────────────┬──────────────────────────┘
                                 │
                                 ▼
                      ┌─────────────────┐
                      │   ns_server     │
                      │   10.2.1.2      │
                      └─────────────────┘
```

---

## Loss Injection Methods

### 100% Loss (Blackhole Routes)

Used for Starlink events and complete outages. Instant effect.

```bash
# Apply
ip route add blackhole 10.2.1.0/24

# Remove
ip route del blackhole 10.2.1.0/24
```

### Probabilistic Loss (Netem)

Used for 1-99% loss. Applied to current latency link.

```bash
# Apply 5% loss to link2 (60ms RTT)
tc qdisc change dev link2_a root netem delay 30ms loss 5% limit 50000

# Remove loss (restore delay-only)
tc qdisc change dev link2_a root netem delay 30ms limit 50000
```

---

## Usage Examples

### Manual Testing

```bash
# Create network
cd contrib/integration_testing/network
sudo ./setup.sh

# View network status (interfaces, routes, connectivity)
sudo ./status.sh

# Run server in namespace
sudo ip netns exec ns_server_$$ \
    ../../../bin/server -addr 10.2.1.2:6000 -promuds /tmp/server.sock

# Run client-generator in publisher namespace
sudo ip netns exec ns_publisher_$$ \
    ../../../bin/client-generator -to srt://10.2.1.2:6000/stream \
    -promuds /tmp/client-gen.sock

# Run client in subscriber namespace
sudo ip netns exec ns_subscriber_$$ \
    ../../../bin/client -from srt://10.2.1.2:6000/stream \
    -promuds /tmp/client.sock

# Set 60ms RTT latency
sudo ./set_latency.sh 2

# Set 5% packet loss
sudo ./set_loss.sh 5

# Start Starlink pattern
sudo ./starlink_pattern.sh start

# Stop Starlink pattern
sudo ./starlink_pattern.sh stop

# Cleanup
sudo ./cleanup.sh
```

### From Go Test Framework

```go
ctx := context.Background()

// Create network controller
ctrl, err := NewNetworkController(NetworkControllerConfig{
    TestID: "mytest",
    // ScriptDir auto-detected if empty
})
if err != nil {
    return err
}

// Setup network (requires root)
if err := ctrl.Setup(ctx); err != nil {
    return err
}
defer ctrl.Cleanup(ctx)

// Configure impairment
ctrl.SetLatencyProfile(ctx, 2)  // 60ms RTT
ctrl.SetLoss(ctx, 5)            // 5% loss

// Get namespace and IP for server
serverNS, _ := ctrl.GetNamespace("server")
serverIP, _ := ctrl.GetIP("server")

// Start server in namespace
serverCmd, _ := ctrl.StartProcessInNamespace(ctx, serverNS,
    "./server", "-addr", serverIP+":6000", "-promuds", "/tmp/server.sock")
serverCmd.Start()
defer serverCmd.Process.Kill()

// Start Starlink pattern
ctrl.StartPattern(ctx, PatternStarlink)
defer ctrl.StopPattern(ctx)

// Run test...

// Get network status
status, _ := ctrl.Status(ctx)
fmt.Println(status)
```

---

## Next Steps

1. **Phase 2**: Implement Go NetworkController to wrap shell scripts
2. **Phase 3**: Integrate with test framework (process spawning in namespaces)
3. **Phase 4**: Create network impairment test configurations
4. **Phase 5**: Connect statistical validation to network mode tests

---

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-08 | Initial implementation document | - |
| 2024-12-08 | Phase 1 complete: All shell scripts created | - |
| 2024-12-08 | Updated design: null routes instead of nftables | - |
| 2024-12-08 | Phase 2 complete: NetworkController Go wrapper | - |
| 2024-12-08 | Phase 3 complete: Test framework integration | - |
| 2024-12-08 | Phase 4 complete: 10 network impairment test configs | - |
| 2024-12-08 | Phase 5 complete: Configurable statistical thresholds | - |

