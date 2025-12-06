# Test 1.1 Enhanced Implementation Plan

## Implementation Status

| Phase | Description | Status |
|-------|-------------|--------|
| Phase 1.1 | Add metrics server to client-generator | ✅ Complete |
| Phase 1.2 | Add metrics server to client | ✅ Complete |
| Phase 1.3 | Update contrib/common/flags.go | ✅ Complete (N/A - component-specific) |
| Phase 1.4 | Update test_flags.sh | ✅ Complete (N/A - component-specific) |
| Phase 2 | Network Configuration (LocalAddr) | ✅ Complete |
| Phase 3.1 | Create integration_testing/config.go | ✅ Complete |
| Phase 3.2 | Create integration_testing/defaults.go | ✅ Complete |
| Phase 3.3 | Create integration_testing/test_configs.go | ✅ Complete |
| Phase 3.4 | Create integration_testing/metrics_collector.go | ✅ Complete |
| Phase 3.5 | Update test_graceful_shutdown.go | ✅ Complete |
| Phase 4 | Build and test integration | ✅ Complete |
| Phase 5 | Documentation Updates | ⬜ Pending |

**Additional fixes during implementation:**
- Fixed WaitGroup panic in dial.go and listen.go (Add() now happens early in Dial()/Listen())
- Added `-metricsenabled` and `-metricslistenaddr` flags to server
- Fixed client/client-generator to not duplicate WaitGroup.Add() calls

---

## Overview

This document provides a detailed implementation plan for the enhanced integration testing features designed in `test_1.1_detailed_design.md`. The implementation is divided into phases to allow incremental development and testing.

**Design Changes to Implement**:
1. **TestConfig Structure**: Comprehensive test configuration with SRT settings, component-specific configs, and network configuration
2. **Client/Client-Generator Metrics**: Add Prometheus `/metrics` endpoints to client and client-generator
3. **Network Configuration**: Distinct loopback IP addresses for each component

---

## Phase 1: Client and Client-Generator Metrics Endpoints

**Priority**: High (Required for metrics comparison in integration tests)
**Estimated Effort**: 2-3 hours
**Dependencies**: None

### 1.1: Add Metrics Server to Client-Generator

**File**: `contrib/client-generator/main.go`

**Tasks**:

1. **Add CLI flags** (after existing flags):
   ```go
   var (
       metricsEnabled    = flag.Bool("metricsenabled", false, "Enable metrics endpoint")
       metricsListenAddr = flag.String("metricslistenaddr", "", "Address for metrics endpoint (e.g., :9091)")
   )
   ```

2. **Add metrics server function**:
   ```go
   func startMetricsServer(addr string) *http.Server {
       if addr == "" {
           return nil
       }

       mux := http.NewServeMux()
       mux.Handle("/metrics", metrics.MetricsHandler())

       server := &http.Server{
           Addr:    addr,
           Handler: mux,
       }

       go func() {
           if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
               fmt.Fprintf(os.Stderr, "Metrics server error: %v\n", err)
           }
       }()

       return server
   }
   ```

3. **Start metrics server in main()** (after SRT connection is established):
   ```go
   // Start metrics server if enabled
   var metricsServer *http.Server
   if *metricsEnabled && *metricsListenAddr != "" {
       metricsServer = startMetricsServer(*metricsListenAddr)
       if metricsServer != nil {
           defer metricsServer.Close()
       }
   }
   ```

4. **Add import**:
   ```go
   import (
       "net/http"
       "github.com/datarhei/gosrt/metrics"
   )
   ```

**Verification**:
- [ ] Build client-generator: `make client-generator`
- [ ] Run with metrics: `./client-generator -to srt://127.0.0.1:6000/test -bitrate 2000000 -metricsenabled -metricslistenaddr :9091`
- [ ] Verify metrics endpoint: `curl http://127.0.0.1:9091/metrics`

---

### 1.2: Add Metrics Server to Client

**File**: `contrib/client/main.go`

**Tasks**:

1. **Add CLI flags** (after existing flags):
   ```go
   var (
       metricsEnabled    = flag.Bool("metricsenabled", false, "Enable metrics endpoint")
       metricsListenAddr = flag.String("metricslistenaddr", "", "Address for metrics endpoint (e.g., :9092)")
   )
   ```

2. **Add metrics server function** (same as client-generator):
   ```go
   func startMetricsServer(addr string) *http.Server {
       if addr == "" {
           return nil
       }

       mux := http.NewServeMux()
       mux.Handle("/metrics", metrics.MetricsHandler())

       server := &http.Server{
           Addr:    addr,
           Handler: mux,
       }

       go func() {
           if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
               fmt.Fprintf(os.Stderr, "Metrics server error: %v\n", err)
           }
       }()

       return server
   }
   ```

3. **Start metrics server in main()** (after SRT connections are established):
   ```go
   // Start metrics server if enabled
   var metricsServer *http.Server
   if *metricsEnabled && *metricsListenAddr != "" {
       metricsServer = startMetricsServer(*metricsListenAddr)
       if metricsServer != nil {
           defer metricsServer.Close()
       }
   }
   ```

4. **Add import**:
   ```go
   import (
       "net/http"
       "github.com/datarhei/gosrt/metrics"
   )
   ```

**Verification**:
- [ ] Build client: `make client`
- [ ] Run with metrics: `./client -from srt://127.0.0.1:6000?streamid=subscribe:/test -to null -metricsenabled -metricslistenaddr :9092`
- [ ] Verify metrics endpoint: `curl http://127.0.0.1:9092/metrics`

---

### 1.3: Update contrib/common/flags.go

**File**: `contrib/common/flags.go`

**Tasks**:

1. **Check if flags already exist** in common flags (they may already be there for server)
2. **If not, add**:
   ```go
   var (
       MetricsEnabled    = flag.Bool("metricsenabled", false, "Enable metrics endpoint")
       MetricsListenAddr = flag.String("metricslistenaddr", "", "Address for metrics endpoint")
   )
   ```
3. **Update `ApplyFlagsToConfig()`** to apply these flags to config

**Alternative**: If metrics flags should NOT be shared (different ports per component), keep them component-specific.

---

### 1.4: Update test_flags.sh

**File**: `contrib/common/test_flags.sh`

**Tasks**:

1. **Add test cases** for new metrics flags:
   ```bash
   # Test: Metrics flags
   run_test "MetricsEnabled flag" "-metricsenabled" '"MetricsEnabled" *: *true' "$CLIENT_BIN"
   run_test "MetricsListenAddr flag" "-metricslistenaddr :9092" '"MetricsListenAddr" *: *":9092"' "$CLIENT_BIN"
   ```

---

## Phase 2: Network Configuration (Local Address Binding)

**Priority**: Medium (Enhances debugging, not required for basic tests)
**Estimated Effort**: 3-4 hours
**Dependencies**: None

### 2.1: Add Local Address Flag to Client-Generator

**File**: `contrib/client-generator/main.go`

**Tasks**:

1. **Add CLI flag**:
   ```go
   var (
       localAddr = flag.String("localaddr", "", "Local IP address to bind to (e.g., 127.0.0.20)")
   )
   ```

2. **Update openWriter() to support local address binding**:
   - This requires modifying the SRT dial to use a specific local address
   - May need to update `srt.Dial()` or use `srt.DialContext()` with options

3. **Research**: Check if `srt.Dial()` supports local address binding
   - If not, this may require changes to the gosrt library itself

**Implementation Notes**:
- For UDP sockets, local address binding uses `net.DialUDP()` with a non-nil local address
- The gosrt library may need to expose this capability
- Alternative: Use `net.ListenUDP()` with specific local address, then use the connection

---

### 2.2: Add Local Address Flag to Client

**File**: `contrib/client/main.go`

**Tasks**:

1. **Add CLI flag**:
   ```go
   var (
       localAddr = flag.String("localaddr", "", "Local IP address to bind to (e.g., 127.0.0.30)")
   )
   ```

2. **Update openReader()/openWriter() to support local address binding**

**Implementation Notes**: Same as client-generator (2.1)

---

### 2.3: Update gosrt Library (If Required)

**Files**: `dial.go`, `config.go`

**Tasks**:

1. **Add LocalAddr to Config**:
   ```go
   type Config struct {
       // ... existing fields ...

       // LocalAddr specifies the local address to bind to when dialing
       // If empty, the system chooses an ephemeral address
       LocalAddr string
   }
   ```

2. **Update dial.go to use LocalAddr**:
   ```go
   func Dial(network, address string, config Config, ctx context.Context, shutdownWg *sync.WaitGroup) (Conn, error) {
       // ...

       var laddr *net.UDPAddr
       if config.LocalAddr != "" {
           laddr, err = net.ResolveUDPAddr("udp", config.LocalAddr+":0")
           if err != nil {
               return nil, err
           }
       }

       pc, err := net.DialUDP("udp", laddr, raddr)
       // ...
   }
   ```

3. **Add CLI flag to contrib/common/flags.go**:
   ```go
   var (
       LocalAddr = flag.String("localaddr", "", "Local IP address to bind to")
   )
   ```

---

### 2.4: Verify Loopback Addresses Work

**Tasks**:

1. **Verify Linux supports multiple loopback addresses**:
   ```bash
   # These should all work on Linux
   ping -c 1 127.0.0.10
   ping -c 1 127.0.0.20
   ping -c 1 127.0.0.30
   ```

2. **Test binding to specific loopback addresses**:
   ```bash
   # Start server on 127.0.0.10
   ./server -addr 127.0.0.10:6000

   # Connect from 127.0.0.20
   ./client-generator -to srt://127.0.0.10:6000/test -localaddr 127.0.0.20

   # Verify with tcpdump
   sudo tcpdump -i lo -n 'port 6000'
   ```

---

## Phase 3: Integration Test Configuration Structure

**Priority**: High (Core of the enhanced testing)
**Estimated Effort**: 4-6 hours
**Dependencies**: Phase 1 (metrics endpoints), Phase 2 (local address binding - optional)

### 3.1: Create Test Configuration Package

**File**: `contrib/integration_testing/config.go`

**Tasks**:

1. **Create SRTConfig struct**:
   ```go
   type SRTConfig struct {
       ConnectionTimeout time.Duration
       PeerIdleTimeout   time.Duration
       HandshakeTimeout  time.Duration
       ShutdownDelay     time.Duration
       Latency           time.Duration
       RecvLatency       time.Duration
       PeerLatency       time.Duration
       // ... all other fields from design doc
   }

   func (c *SRTConfig) ToCliFlags() []string {
       // Convert to CLI flags
   }
   ```

2. **Create NetworkConfig struct**:
   ```go
   type NetworkConfig struct {
       IP          string
       SRTPort     int
       MetricsPort int
   }

   func (n *NetworkConfig) SRTAddr() string
   func (n *NetworkConfig) MetricsAddr() string
   func (n *NetworkConfig) MetricsURL() string
   ```

3. **Create ComponentConfig struct**:
   ```go
   type ComponentConfig struct {
       SRT        SRTConfig
       ExtraFlags []string
   }
   ```

4. **Create TestConfig struct**:
   ```go
   type TestConfig struct {
       Name        string
       Description string

       ServerNetwork          NetworkConfig
       ClientGeneratorNetwork NetworkConfig
       ClientNetwork          NetworkConfig

       Bitrate         int64
       TestDuration    time.Duration
       ConnectionWait  time.Duration

       Server          ComponentConfig
       ClientGenerator ComponentConfig
       Client          ComponentConfig

       SharedSRT       *SRTConfig

       MetricsEnabled  bool
       CollectInterval time.Duration

       ExpectedErrors     []string
       MaxExpectedDrops   int64
       MaxExpectedRetrans int64
   }

   func (c *TestConfig) GetEffectiveNetworkConfig() (server, clientGen, client NetworkConfig)
   func (c *TestConfig) GetServerFlags() []string
   func (c *TestConfig) GetClientGeneratorFlags() []string
   func (c *TestConfig) GetClientFlags() []string
   func (c *TestConfig) GetAllMetricsURLs() (server, clientGen, client string)
   ```

---

### 3.2: Create Default Configurations

**File**: `contrib/integration_testing/defaults.go`

**Tasks**:

1. **Define default network configurations**:
   ```go
   var (
       DefaultServerNetwork = NetworkConfig{
           IP: "127.0.0.10", SRTPort: 6000, MetricsPort: 9090,
       }
       DefaultClientGeneratorNetwork = NetworkConfig{
           IP: "127.0.0.20", SRTPort: 0, MetricsPort: 9091,
       }
       DefaultClientNetwork = NetworkConfig{
           IP: "127.0.0.30", SRTPort: 0, MetricsPort: 9092,
       }
   )
   ```

2. **Define SRT configuration presets**:
   ```go
   var (
       DefaultSRTConfig       = SRTConfig{}
       SmallBuffersSRTConfig  = SRTConfig{...}
       LargeBuffersSRTConfig  = SRTConfig{...}
       IoUringSRTConfig       = SRTConfig{...}
       BTreeSRTConfig         = SRTConfig{...}
   )
   ```

---

### 3.3: Create Test Configuration Table

**File**: `contrib/integration_testing/test_configs.go`

**Tasks**:

1. **Define test configurations** (from design doc):
   ```go
   var TestConfigs = []TestConfig{
       // Basic bandwidth tests
       {Name: "Default-1Mbps", ...},
       {Name: "Default-2Mbps", ...},
       {Name: "Default-5Mbps", ...},
       {Name: "Default-10Mbps", ...},

       // Buffer size tests
       {Name: "SmallBuffers-2Mbps", ...},
       {Name: "LargeBuffers-2Mbps", ...},

       // Algorithm tests
       {Name: "BTree-2Mbps", ...},
       {Name: "List-2Mbps", ...},

       // io_uring tests
       {Name: "IoUring-2Mbps", ...},
       {Name: "IoUring-10Mbps", ...},

       // Combined tests
       {Name: "IoUring-LargeBuffers-10Mbps", ...},

       // Component-specific tests
       {Name: "AsymmetricLatency-2Mbps", ...},
   }
   ```

---

### 3.4: Create Metrics Collection Functions

**File**: `contrib/integration_testing/metrics.go`

**Tasks**:

1. **Define metrics snapshot structure**:
   ```go
   type MetricsSnapshot struct {
       Timestamp time.Time
       Point     string
       Metrics   map[string]float64
       Raw       string
   }
   ```

2. **Create metrics collection function**:
   ```go
   func collectMetrics(metricsURL string) (*MetricsSnapshot, error) {
       // Fetch and parse Prometheus metrics
   }
   ```

3. **Create Prometheus parser**:
   ```go
   func parsePrometheusMetrics(raw string) map[string]float64 {
       // Parse Prometheus format
   }
   ```

4. **Create component metrics structure**:
   ```go
   type ComponentMetrics struct {
       Component string
       Addr      string
       Snapshots []*MetricsSnapshot
   }

   type TestMetrics struct {
       Server          ComponentMetrics
       ClientGenerator ComponentMetrics
       Client          ComponentMetrics
   }
   ```

5. **Create collection orchestration**:
   ```go
   func collectAllMetrics(testMetrics *TestMetrics, point string) error {
       // Collect from all three components in parallel
   }
   ```

6. **Create verification function**:
   ```go
   func verifyMetricsConsistency(testMetrics *TestMetrics) error {
       // Compare metrics across components
       // Verify error counters
   }
   ```

---

### 3.5: Update Test Orchestrator

**File**: `contrib/integration_testing/test_graceful_shutdown.go`

**Tasks**:

1. **Add new test function for multiple configurations**:
   ```go
   func testGracefulShutdownSIGINTWithConfigs() {
       // Iterate through TestConfigs
       // Run each test with its configuration
       // Collect and verify metrics
   }
   ```

2. **Update runTestWithConfig()**:
   ```go
   func runTestWithConfig(config TestConfig) error {
       // Use config.GetServerFlags(), etc.
       // Use network configuration
       // Collect metrics if enabled
       // Verify metrics at end
   }
   ```

3. **Add command-line option to run specific test or all tests**:
   ```go
   // Run single test by name
   // go run test_graceful_shutdown.go graceful-shutdown-sigint Default-2Mbps

   // Run all tests
   // go run test_graceful_shutdown.go graceful-shutdown-sigint-all
   ```

---

## Phase 4: Integration and Testing

**Priority**: High (Validation of all phases)
**Estimated Effort**: 2-3 hours
**Dependencies**: Phases 1-3

### 4.1: Build and Test Individual Components

**Tasks**:

1. **Build all binaries**:
   ```bash
   make server client client-generator
   ```

2. **Test metrics endpoints**:
   ```bash
   # Terminal 1: Start server
   ./contrib/server/server -addr 127.0.0.10:6000 -metricsenabled -metricslistenaddr 127.0.0.10:9090

   # Terminal 2: Start client-generator
   ./contrib/client-generator/client-generator -to srt://127.0.0.10:6000/test -bitrate 2000000 \
       -metricsenabled -metricslistenaddr 127.0.0.20:9091

   # Terminal 3: Start client
   ./contrib/client/client -from srt://127.0.0.10:6000?streamid=subscribe:/test -to null \
       -metricsenabled -metricslistenaddr 127.0.0.30:9092

   # Terminal 4: Verify metrics
   curl http://127.0.0.10:9090/metrics
   curl http://127.0.0.20:9091/metrics
   curl http://127.0.0.30:9092/metrics
   ```

3. **Test network configuration** (if Phase 2 implemented):
   ```bash
   # Verify traffic uses correct IP addresses
   sudo tcpdump -i lo -n 'port 6000'
   ```

---

### 4.2: Run Integration Tests

**Tasks**:

1. **Run single configuration**:
   ```bash
   cd contrib/integration_testing
   go run test_graceful_shutdown.go graceful-shutdown-sigint
   ```

2. **Run all configurations**:
   ```bash
   cd contrib/integration_testing
   go run test_graceful_shutdown.go graceful-shutdown-sigint-all
   ```

3. **Verify metrics collection**:
   - Check metrics snapshots are collected at each point
   - Verify metrics comparison works correctly
   - Check error counter verification

---

### 4.3: Update Makefile

**File**: `Makefile`

**Tasks**:

1. **Add new test targets**:
   ```makefile
   ## test-integration-all: Run all integration test configurations
   test-integration-all: client server client-generator
   	@cd contrib/integration_testing && go run test_graceful_shutdown.go graceful-shutdown-sigint-all

   ## test-integration-config: Run specific integration test configuration
   test-integration-config: client server client-generator
   	@cd contrib/integration_testing && go run test_graceful_shutdown.go graceful-shutdown-sigint $(CONFIG)
   ```

---

## Phase 5: Documentation Updates

**Priority**: Low (After implementation complete)
**Estimated Effort**: 1 hour
**Dependencies**: Phases 1-4

### 5.1: Update Implementation Progress

**File**: `documentation/test_1.1_implementation.md`

**Tasks**:

1. Update Phase 3 status to complete
2. Document all implemented features
3. Document any deviations from design
4. Update notes section

---

### 5.2: Update Main Design Document

**File**: `documentation/test_1.1_detailed_design.md`

**Tasks**:

1. Mark implemented sections
2. Update any design changes discovered during implementation
3. Add lessons learned

---

### 5.3: Update README or Contributing Guide

**Tasks**:

1. Document new CLI flags for client and client-generator
2. Document how to run integration tests with different configurations
3. Document metrics endpoints

---

## Implementation Order Summary

| Phase | Description | Priority | Effort | Dependencies |
|-------|-------------|----------|--------|--------------|
| 1 | Client/Client-Generator Metrics | High | 2-3 hrs | None |
| 2 | Network Configuration (LocalAddr) | Medium | 3-4 hrs | None |
| 3 | Test Configuration Structure | High | 4-6 hrs | Phase 1, (Phase 2 optional) |
| 4 | Integration and Testing | High | 2-3 hrs | Phases 1-3 |
| 5 | Documentation Updates | Low | 1 hr | Phases 1-4 |

**Total Estimated Effort**: 12-17 hours

---

## Recommended Implementation Sequence

1. **Start with Phase 1**: Add metrics endpoints to client and client-generator. This is straightforward and provides immediate value for testing.

2. **Then Phase 3.1-3.2**: Create the configuration structures. This can be tested independently of Phase 2.

3. **Optional Phase 2**: Add local address binding. This enhances debugging but is not strictly required. Can be deferred.

4. **Then Phase 3.3-3.5**: Complete the test configuration table and update the test orchestrator.

5. **Finally Phase 4-5**: Integration testing and documentation.

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Local address binding requires library changes | High | Phase 2 is optional; tests work without distinct IPs |
| Metrics endpoints increase memory usage | Low | Metrics server is lightweight; can be disabled |
| Test configurations may need tuning | Medium | Start with basic configurations; iterate based on results |
| Loopback addresses may not work on all systems | Low | Document Linux requirement; fallback to 127.0.0.1 |

---

## Success Criteria

1. **Phase 1 Complete**:
   - Client and client-generator expose `/metrics` endpoints
   - Endpoints return valid Prometheus format
   - Build succeeds with no lint errors

2. **Phase 2 Complete** (optional):
   - Components can bind to specific local addresses
   - Traffic uses correct source IP addresses (verified with tcpdump)

3. **Phase 3 Complete**:
   - All configuration structures implemented
   - Test configurations defined
   - Metrics collection and verification working

4. **Phase 4 Complete**:
   - All integration tests pass
   - Metrics collected and verified for each test
   - Multiple bandwidth configurations tested

5. **Phase 5 Complete**:
   - All documentation updated
   - Implementation progress tracked

