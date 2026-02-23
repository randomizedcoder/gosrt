# Prometheus Metrics Unix Domain Socket Design

## Overview

This document describes the design for adding Unix Domain Socket (UDS) support to the Prometheus
metrics HTTP handler in the GoSRT server, client, and client-generator components. This enables
metrics collection from processes running in isolated network namespaces.

## Problem Statement

When GoSRT components run in isolated network namespaces (for packet loss injection testing),
the integration test orchestrator running in the default namespace cannot reach TCP-based
Prometheus metrics endpoints. Unix Domain Sockets solve this because they use the filesystem,
which is shared across namespaces.

## Requirements

### Functional Requirements

| Requirement | Description |
|-------------|-------------|
| **Dual Endpoint Support** | Support both TCP HTTP and Unix Domain Socket endpoints |
| **Independent Control** | Each endpoint type controlled by its own CLI flag |
| **Default Disabled** | By default, no Prometheus endpoint is opened |
| **Both Enabled** | User can enable both TCP and UDS simultaneously |
| **Socket Cleanup** | UDS socket file must be cleaned up on exit |
| **Component Parity** | All three components (server, client, client-generator) support both modes |

### CLI Flag Behavior

**Default: No listeners opened.** Users must explicitly enable metrics endpoints.

| Flags Provided | Result |
|----------------|--------|
| *(none)* | **No Prometheus endpoint** - no listener opened |
| `-promhttp :9090` | TCP HTTP listener on port 9090 |
| `-promuds /tmp/metrics.sock` | Unix Domain Socket listener at specified path |
| `-promhttp :9090 -promuds /tmp/metrics.sock` | Both TCP and UDS listeners active |

## Design

### New CLI Flags

Add two new flags to `contrib/common/flags.go`:

```go
// Prometheus metrics endpoint flags
// These are NOT part of srt.Config - they're application-level configuration
// By default (when not specified), NO metrics listeners are opened
var (
    // TCP HTTP endpoint for Prometheus metrics
    // If not specified, no TCP listener is opened
    // Example: -promhttp :9090
    // Example: -promhttp 127.0.0.1:9090
    PromHTTPAddr = flag.String("promhttp", "",
        "TCP address for Prometheus metrics HTTP endpoint (e.g., :9090 or 127.0.0.1:9090). "+
        "If not specified, no TCP metrics listener is opened.")

    // Unix Domain Socket endpoint for Prometheus metrics
    // If not specified, no UDS listener is opened
    // Example: -promuds /tmp/srt_metrics_server.sock
    PromUDSPath = flag.String("promuds", "",
        "Unix Domain Socket path for Prometheus metrics endpoint "+
        "(e.g., /tmp/srt_metrics_server.sock). "+
        "If not specified, no UDS metrics listener is opened. "+
        "UDS allows metrics collection from processes in isolated network namespaces.")
)
```

### Metrics Server Helper Functions

Create new file `contrib/common/metrics_server.go`.

**Note:** The implementation follows the exact pattern from `context_and_cancellation_new_design.md`
section "Prometheus HTTP Server Shutdown Pattern":
- Shutdown watcher: fire-and-forget goroutine (no `wg.Add(1)`)
- Server goroutine: tracked with `wg.Add(1)` and `defer wg.Done()`

```go
package common

import (
    "context"
    "fmt"
    "net"
    "net/http"
    "os"
    "sync"
    "time"

    "github.com/datarhei/gosrt/metrics"
)

// StartMetricsServers starts Prometheus metrics endpoint(s) based on the provided flags.
//
// This follows the exact pattern from context_and_cancellation_new_design.md:
// - Shutdown watcher goroutine: fire-and-forget (NO wg.Add), listens for ctx.Done()
// - Server goroutine: tracked with wg.Add(1) and defer wg.Done()
//
// Parameters:
//   - ctx: Parent context - when cancelled, triggers graceful shutdown of servers
//   - wg: WaitGroup to track server goroutines
//   - httpAddr: TCP address (e.g., ":9090") - empty string means no TCP listener
//   - udsPath: Unix socket path - empty string means no UDS listener
//
// If both httpAddr and udsPath are empty, this function does nothing (no listeners opened).
func StartMetricsServers(ctx context.Context, wg *sync.WaitGroup, httpAddr, udsPath string) error {
    handler := metrics.MetricsHandler()

    // Start TCP HTTP endpoint if configured
    if httpAddr != "" {
        if err := startHTTPMetricsServer(ctx, wg, httpAddr, handler); err != nil {
            return fmt.Errorf("failed to start HTTP metrics server: %w", err)
        }
    }

    // Start UDS endpoint if configured
    if udsPath != "" {
        if err := startUDSMetricsServer(ctx, wg, udsPath, handler); err != nil {
            return fmt.Errorf("failed to start UDS metrics server: %w", err)
        }
    }

    return nil
}

// startHTTPMetricsServer starts a TCP HTTP metrics server.
// Follows the pattern from context_and_cancellation_new_design.md section
// "Prometheus HTTP Server Shutdown Pattern".
func startHTTPMetricsServer(ctx context.Context, wg *sync.WaitGroup, addr string, handler http.Handler) error {
    promSrv := &http.Server{
        Addr:    addr,
        Handler: handler,
    }

    // Shutdown watcher - triggers clean shutdown when context cancelled
    // NOTE: This is fire-and-forget (no wg.Add) per the design pattern
    go func() {
        <-ctx.Done()
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := promSrv.Shutdown(shutdownCtx); err != nil {
            fmt.Fprintf(os.Stderr, "Prometheus HTTP server shutdown error: %v\n", err)
        }
    }()

    // Run Prometheus server in goroutine with waitgroup tracking
    wg.Add(1)
    go func() {
        defer wg.Done()
        fmt.Fprintf(os.Stderr, "Prometheus metrics HTTP server started on %s\n", addr)
        if err := promSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            fmt.Fprintf(os.Stderr, "Prometheus HTTP server error: %v\n", err)
        }
    }()

    return nil
}

// startUDSMetricsServer starts a Unix Domain Socket metrics server.
// Same pattern as HTTP, but uses net.Listen("unix", path) and cleans up socket file.
func startUDSMetricsServer(ctx context.Context, wg *sync.WaitGroup, socketPath string, handler http.Handler) error {
    // Remove existing socket file if it exists (stale from previous run)
    if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
        return fmt.Errorf("failed to remove existing socket file: %w", err)
    }

    // Create Unix socket listener
    listener, err := net.Listen("unix", socketPath)
    if err != nil {
        return fmt.Errorf("failed to create Unix socket listener: %w", err)
    }

    promSrv := &http.Server{
        Handler: handler,
    }

    // Shutdown watcher - triggers clean shutdown when context cancelled
    // Also cleans up the socket file
    // NOTE: This is fire-and-forget (no wg.Add) per the design pattern
    go func() {
        <-ctx.Done()
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := promSrv.Shutdown(shutdownCtx); err != nil {
            fmt.Fprintf(os.Stderr, "Prometheus UDS server shutdown error: %v\n", err)
        }
        // Clean up socket file after server shutdown
        if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
            fmt.Fprintf(os.Stderr, "Failed to remove socket file %s: %v\n", socketPath, err)
        }
    }()

    // Run Prometheus server in goroutine with waitgroup tracking
    wg.Add(1)
    go func() {
        defer wg.Done()
        fmt.Fprintf(os.Stderr, "Prometheus metrics UDS server started on %s\n", socketPath)
        if err := promSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
            fmt.Fprintf(os.Stderr, "Prometheus UDS server error: %v\n", err)
        }
    }()

    return nil
}
```

### Metrics Client Helper for Integration Tests

Add to `contrib/common/metrics_client.go`:

```go
package common

import (
    "context"
    "fmt"
    "io"
    "net"
    "net/http"
    "time"
)

// MetricsClient provides methods to fetch Prometheus metrics from TCP or UDS endpoints
type MetricsClient struct {
    httpClient *http.Client
}

// NewMetricsClient creates a new metrics client
func NewMetricsClient() *MetricsClient {
    return &MetricsClient{
        httpClient: &http.Client{
            Timeout: 5 * time.Second,
        },
    }
}

// FetchHTTP fetches metrics from a TCP HTTP endpoint
func (mc *MetricsClient) FetchHTTP(addr string) ([]byte, error) {
    resp, err := mc.httpClient.Get(fmt.Sprintf("http://%s/metrics", addr))
    if err != nil {
        return nil, fmt.Errorf("failed to fetch metrics from %s: %w", addr, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, addr)
    }

    return io.ReadAll(resp.Body)
}

// FetchUDS fetches metrics from a Unix Domain Socket endpoint
func (mc *MetricsClient) FetchUDS(socketPath string) ([]byte, error) {
    // Create a client with Unix socket transport
    client := &http.Client{
        Timeout: 5 * time.Second,
        Transport: &http.Transport{
            DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
                var d net.Dialer
                return d.DialContext(ctx, "unix", socketPath)
            },
        },
    }

    // The URL host doesn't matter for Unix sockets, but we need a valid URL
    resp, err := client.Get("http://localhost/metrics")
    if err != nil {
        return nil, fmt.Errorf("failed to fetch metrics from socket %s: %w", socketPath, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("unexpected status code %d from socket %s", resp.StatusCode, socketPath)
    }

    return io.ReadAll(resp.Body)
}
```

### Update Server main.go

Replace the existing metrics server setup with the new helper:

```go
// Before (current implementation):
if *metricsEnabled {
    promSrv := &http.Server{
        Addr:    *metricsListenAddr,
        Handler: metrics.MetricsHandler(),
    }

    // Shutdown watcher
    go func() {
        <-ctx.Done()
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := promSrv.Shutdown(shutdownCtx); err != nil {
            fmt.Fprintf(os.Stderr, "Prometheus server shutdown error: %v\n", err)
        }
    }()

    // Run Prometheus server
    wg.Add(1)
    go func() {
        defer wg.Done()
        fmt.Fprintf(os.Stderr, "Prometheus server started on %s\n", *metricsListenAddr)
        if err := promSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            fmt.Fprintf(os.Stderr, "Prometheus server error: %v\n", err)
        }
    }()
}

// After (new implementation - same pattern, just consolidated):
// Start Prometheus metrics server(s) if configured
// Note: If both flags are empty, this does nothing (no listeners opened)
if err := common.StartMetricsServers(ctx, &wg, *common.PromHTTPAddr, *common.PromUDSPath); err != nil {
    fmt.Fprintf(os.Stderr, "Failed to start metrics server: %v\n", err)
    os.Exit(1)
}
```

The `StartMetricsServers()` function internally uses the exact same pattern (shutdown watcher + server goroutine with wg tracking) but consolidates the logic and adds UDS support.

### Remove Deprecated Flags from Server

Remove these flags from `contrib/server/main.go` (they move to `common/flags.go`):

```go
// REMOVE these - replaced by common.PromHTTPAddr and common.PromUDSPath
metricsEnabled    = flag.Bool("metricsenabled", false, "Enable Prometheus metrics endpoint")
metricsListenAddr = flag.String("metricslistenaddr", ":9090", "Address for metrics endpoint")
```

### Client and Client-Generator Updates

Apply the same pattern to `contrib/client/main.go` and `contrib/client-generator/main.go`:

```go
// Start Prometheus metrics server(s) if configured
// Note: If both flags are empty (default), no listeners are opened
if err := common.StartMetricsServers(ctx, &wg, *common.PromHTTPAddr, *common.PromUDSPath); err != nil {
    fmt.Fprintf(os.Stderr, "Failed to start metrics server: %v\n", err)
    os.Exit(1)
}
```

## Integration Test Updates

### TestConfig Updates

Update `contrib/integration_testing/config.go`:

```go
type NetworkConfig struct {
    IP          string // IP address for this component
    SRTPort     int    // SRT port
    MetricsPort int    // TCP metrics port (0 = disabled)
    MetricsUDS  string // Unix socket path for metrics (empty = disabled)
}

// MetricsAddr returns the TCP metrics address
func (n NetworkConfig) MetricsAddr() string {
    if n.MetricsPort == 0 {
        return ""
    }
    return fmt.Sprintf("%s:%d", n.IP, n.MetricsPort)
}
```

### Defaults Update

Update `contrib/integration_testing/defaults.go`:

```go
// Default network configuration
// For namespace-isolated tests, use UDS paths instead of TCP ports
var DefaultNetworkConfig = NetworkConfig{
    Server: ComponentNetworkConfig{
        IP:          "127.0.0.10",
        SRTPort:     6000,
        MetricsPort: 0, // Disabled for namespace tests
        MetricsUDS:  "/tmp/srt_metrics_server.sock",
    },
    ClientGenerator: ComponentNetworkConfig{
        IP:          "127.0.0.20",
        SRTPort:     0, // Not needed - connects to server
        MetricsPort: 0, // Disabled for namespace tests
        MetricsUDS:  "/tmp/srt_metrics_clientgen.sock",
    },
    Client: ComponentNetworkConfig{
        IP:          "127.0.0.30",
        SRTPort:     0, // Not needed - connects to server
        MetricsPort: 0, // Disabled for namespace tests
        MetricsUDS:  "/tmp/srt_metrics_client.sock",
    },
}

// For non-namespace tests (current default), use TCP
var DefaultNetworkConfigTCP = NetworkConfig{
    Server: ComponentNetworkConfig{
        IP:          "127.0.0.10",
        SRTPort:     6000,
        MetricsPort: 5101,
        MetricsUDS:  "",
    },
    // ... etc
}
```

### Flag Generation Updates

Update the flag generation functions in `config.go`:

```go
func (c *TestConfig) GetServerFlags() []string {
    serverNet, _, _ := c.GetEffectiveNetworkConfig()

    flags := []string{
        "-addr", serverNet.SRTAddr(),
    }

    // Add TCP metrics if configured
    if serverNet.MetricsPort > 0 {
        flags = append(flags, "-promhttp", serverNet.MetricsAddr())
    }

    // Add UDS metrics if configured
    if serverNet.MetricsUDS != "" {
        flags = append(flags, "-promuds", serverNet.MetricsUDS)
    }

    // ... rest of flags
    return flags
}
```

### Metrics Collection Updates

Update `contrib/integration_testing/metrics_collector.go`:

```go
func (mc *MetricsCollector) CollectSnapshot(point string, config TestConfig) (*MetricsSnapshot, error) {
    serverNet, clientGenNet, clientNet := config.GetEffectiveNetworkConfig()

    client := common.NewMetricsClient()
    snapshot := &MetricsSnapshot{
        Timestamp: time.Now(),
        Point:     point,
    }

    // Collect server metrics
    var serverMetrics []byte
    var err error
    if serverNet.MetricsUDS != "" {
        serverMetrics, err = client.FetchUDS(serverNet.MetricsUDS)
    } else if serverNet.MetricsPort > 0 {
        serverMetrics, err = client.FetchHTTP(serverNet.MetricsAddr())
    }
    if err != nil {
        return nil, fmt.Errorf("failed to collect server metrics: %w", err)
    }
    snapshot.ServerMetrics = serverMetrics

    // ... similar for client and client-generator

    return snapshot, nil
}
```

## test_flags.sh Updates

Add new test cases:

```bash
# Test 29: PromHTTP flag
run_test "PromHTTP flag" "-promhttp :9090" "" "$SERVER_BIN"
# Note: This test just validates the flag is accepted, not that it works

# Test 30: PromUDS flag
run_test "PromUDS flag" "-promuds /tmp/test.sock" "" "$SERVER_BIN"

# Test 31: Both PromHTTP and PromUDS
run_test "Both PromHTTP and PromUDS" "-promhttp :9090 -promuds /tmp/test.sock" "" "$SERVER_BIN"
```

Note: These tests only validate flag parsing. The actual socket functionality should be tested
in integration tests.

## File Changes Summary

| File | Change |
|------|--------|
| `contrib/common/flags.go` | Add `PromHTTPAddr` and `PromUDSPath` flags |
| `contrib/common/metrics_server.go` | **NEW**: `StartMetricsServer()` helper |
| `contrib/common/metrics_client.go` | **NEW**: `FetchHTTP()` and `FetchUDS()` helpers |
| `contrib/server/main.go` | Replace inline metrics setup with `StartMetricsServer()` |
| `contrib/client/main.go` | Replace inline metrics setup with `StartMetricsServer()` |
| `contrib/client-generator/main.go` | Replace inline metrics setup with `StartMetricsServer()` |
| `contrib/integration_testing/config.go` | Add `MetricsUDS` to `NetworkConfig` |
| `contrib/integration_testing/defaults.go` | Add UDS defaults for namespace tests |
| `contrib/integration_testing/metrics_collector.go` | Support UDS collection |
| `contrib/common/test_flags.sh` | Add tests for new flags |

## Migration from Old Flags

The old flags (`-metricsenabled`, `-metricslistenaddr`) have been removed.

| Old Flag | New Flag | Notes |
|----------|----------|-------|
| `-metricsenabled -metricslistenaddr :9090` | `-promhttp :9090` | Simpler, single flag |
| `-metricsenabled -metricslistenaddr 127.0.0.1:9090` | `-promhttp 127.0.0.1:9090` | Same behavior |
| N/A | `-promuds /tmp/metrics.sock` | New UDS support |

## Usage Examples

### Server with TCP Metrics
```bash
./server -addr :6000 -promhttp :9090
```

### Server with UDS Metrics (for namespace tests)
```bash
ip netns exec ns_server ./server -addr 10.2.1.2:6000 -promuds /tmp/srt_metrics_server.sock
```

### Server with Both
```bash
./server -addr :6000 -promhttp :9090 -promuds /tmp/srt_metrics_server.sock
```

### Client with UDS Metrics
```bash
ip netns exec ns_subscriber ./client -from srt://10.2.1.2:6000 -to - -promuds /tmp/srt_metrics_client.sock
```

### Integration Test Fetching Metrics via UDS
```go
client := common.NewMetricsClient()
serverMetrics, err := client.FetchUDS("/tmp/srt_metrics_server.sock")
clientMetrics, err := client.FetchUDS("/tmp/srt_metrics_client.sock")
```

## Implementation Checklist

- [ ] Add `PromHTTPAddr` and `PromUDSPath` to `contrib/common/flags.go`
- [ ] Create `contrib/common/metrics_server.go` with `StartMetricsServer()`
- [ ] Create `contrib/common/metrics_client.go` with `FetchHTTP()` and `FetchUDS()`
- [ ] Update `contrib/server/main.go` to use `StartMetricsServer()`
- [ ] Update `contrib/client/main.go` to use `StartMetricsServer()`
- [ ] Update `contrib/client-generator/main.go` to use `StartMetricsServer()`
- [ ] Update `contrib/integration_testing/config.go` with `MetricsUDS`
- [ ] Update `contrib/integration_testing/defaults.go` with UDS defaults
- [ ] Update `contrib/integration_testing/metrics_collector.go` for UDS
- [ ] Update `contrib/common/test_flags.sh` with new tests
- [x] Remove deprecated `metricsEnabled` and `metricsListenAddr` flags
- [ ] Test TCP endpoint works
- [ ] Test UDS endpoint works
- [ ] Test both endpoints simultaneously
- [ ] Test UDS works across network namespaces
- [ ] Update documentation

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-06 | Initial design document | - |

