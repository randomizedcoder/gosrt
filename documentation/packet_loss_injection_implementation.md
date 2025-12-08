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

### Phase 2: Go Network Controller 🔲 PENDING

Go wrapper for controlling network impairment from the test framework.

| Task | Status | File | Notes |
|------|--------|------|-------|
| NetworkController struct | 🔲 Pending | `network_controller.go` | Manages namespaces and impairment |
| Setup/Cleanup methods | 🔲 Pending | | Call shell scripts or use netlink |
| SetLatency method | 🔲 Pending | | Switch latency profile |
| SetLoss method | 🔲 Pending | | Set loss percentage |
| StartPattern method | 🔲 Pending | | Start Starlink/burst patterns |
| StopPattern method | 🔲 Pending | | Stop patterns |
| Namespace command runner | 🔲 Pending | | `ip netns exec` wrapper |

### Phase 3: Test Framework Integration 🔲 PENDING

Integrate network impairment with the existing test framework.

| Task | Status | File | Notes |
|------|--------|------|-------|
| Add Mode field to TestConfig | ✅ Complete | `config.go` | `TestModeClean` / `TestModeNetwork` |
| Add Impairment field | ✅ Complete | `config.go` | `NetworkImpairment` struct |
| Network mode test runner | 🔲 Pending | | Setup namespace before test |
| UDS metrics collection | ✅ Complete | `metrics_collector.go` | Already supports UDS |
| Process spawning in namespace | 🔲 Pending | | `ip netns exec` for processes |
| Cleanup on test failure | 🔲 Pending | | Ensure namespaces are removed |

### Phase 4: Network Impairment Test Configurations 🔲 PENDING

Test configurations that use network impairment.

| Task | Status | File | Notes |
|------|--------|------|-------|
| 2% loss test | 🔲 Pending | `test_configs.go` | Basic loss recovery |
| 5% loss test | 🔲 Pending | | Moderate loss |
| 10% loss test | 🔲 Pending | | Heavy loss |
| Latency + loss tests | 🔲 Pending | | Combined impairment |
| Starlink pattern test | 🔲 Pending | | Burst loss recovery |
| High-loss burst test | 🔲 Pending | | 85% loss for 1 second |

### Phase 5: Statistical Validation 🔲 PENDING

Validate that observed metrics match expected impairment.

| Task | Status | File | Notes |
|------|--------|------|-------|
| Statistical validation function | ✅ Complete | `analysis.go` | `ValidateStatistical()` |
| Loss rate tolerance | ✅ Complete | | ±50% tolerance |
| Retransmission validation | ✅ Complete | | Min/max retrans rate |
| NAK validation | ✅ Complete | | NAKs per lost packet |
| Recovery rate validation | ✅ Complete | | Min recovery rate |
| Integration with test runner | 🔲 Pending | | Call for network mode tests |

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
├── network_controller.go      # 🔲 Go wrapper for network control
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

### From Go Test Framework (Future)

```go
// Create network controller
ctrl := NewNetworkController(testID)
defer ctrl.Cleanup()

// Setup network
if err := ctrl.Setup(); err != nil {
    return err
}

// Configure impairment
ctrl.SetLatencyProfile(2)  // 60ms RTT
ctrl.SetLoss(5)            // 5% loss

// Start processes in namespaces
ctrl.RunInNamespace("server", serverCmd)
ctrl.RunInNamespace("publisher", publisherCmd)
ctrl.RunInNamespace("subscriber", subscriberCmd)

// Run test...

// Collect metrics via UDS
serverMetrics := ctrl.CollectMetrics("server", "/tmp/server.sock")
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

