package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// runIsolationModeTest runs a simplified CG→Server isolation test
// No Client (subscriber), no network impairment, 30 second tests.
func runIsolationModeTest(config IsolationTestConfig) (passed bool) {
	startTime := time.Now()

	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "Error: Isolation tests require root privileges\n")
		fmt.Fprintf(os.Stderr, "Run with: sudo %s isolation-test <config-name>\n", os.Args[0])
		return false
	}

	fmt.Printf("\n=== Isolation Test: %s ===\n", config.Name)
	fmt.Printf("Description: %s\n", config.Description)
	fmt.Printf("Duration: %v\n", config.TestDuration)
	fmt.Printf("Bitrate: %d bps (%.2f Mb/s)\n", config.Bitrate, float64(config.Bitrate)/1_000_000)
	fmt.Println()

	// Create network controller
	nc, err := NewNetworkController(NetworkControllerConfig{
		TestID:  fmt.Sprintf("iso_%d", os.Getpid()),
		Verbose: false, // Quiet for isolation tests
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating network controller: %v\n", err)
		return false
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up network namespaces
	fmt.Println("Setting up network namespaces...")
	if err := nc.Setup(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up network: %v\n", err)
		return false
	}

	// Set up parallel IPs (.3 addresses for test pipeline)
	if err := nc.SetupParallelIPs(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up parallel IPs: %v\n", err)
		return false
	}

	defer func() {
		fmt.Println("\nCleaning up network namespaces...")
		_ = nc.CleanupParallelIPs(ctx)
		if err := nc.Cleanup(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cleanup failed: %v\n", err)
		}
	}()

	// Use clean path (no latency, no loss)
	fmt.Println("Using clean network path (no impairment)")

	// Print topology
	fmt.Println("\nTopology:")
	fmt.Println("  Control: CG(10.1.1.2) → Server(10.2.1.2:6000)")
	fmt.Println("  Test:    CG(10.1.1.3) → Server(10.2.1.3:6001)")
	fmt.Println()

	// Build CLI flags
	testID := nc.TestID
	controlServerFlags := config.GetControlServerFlags(testID)
	testServerFlags := config.GetTestServerFlags(testID)
	controlCGFlags := config.GetControlCGFlags(testID)
	testCGFlags := config.GetTestCGFlags(testID)

	// Print CLI differences
	fmt.Println("CLI Flags:")
	fmt.Printf("  Control Server: %s\n", strings.Join(controlServerFlags, " "))
	fmt.Printf("  Test Server:    %s\n", strings.Join(testServerFlags, " "))
	fmt.Printf("  Control CG:     %s\n", strings.Join(controlCGFlags, " "))
	fmt.Printf("  Test CG:        %s\n", strings.Join(testCGFlags, " "))
	fmt.Println()

	// Get base directory and binaries
	baseDir := getBaseDir()
	serverBin := filepath.Join(baseDir, "contrib", "server", "server")
	clientGenBin := filepath.Join(baseDir, "contrib", "client-generator", "client-generator")

	// Build binaries if needed
	if err := ensureBinaries(baseDir, serverBin, clientGenBin, ""); err != nil {
		fmt.Fprintf(os.Stderr, "Error building binaries: %v\n", err)
		return false
	}

	// Start servers
	fmt.Println("Starting servers...")
	controlServer, err := startProcessInNamespace(ctx, nc.NamespaceServer, serverBin, controlServerFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting control server: %v\n", err)
		return false
	}
	testServer, err := startProcessInNamespace(ctx, nc.NamespaceServer, serverBin, testServerFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting test server: %v\n", err)
		return false
	}
	time.Sleep(500 * time.Millisecond)

	// Start client-generators
	fmt.Println("Starting client-generators...")
	controlCG, err := startProcessInNamespace(ctx, nc.NamespacePublisher, clientGenBin, controlCGFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting control CG: %v\n", err)
		return false
	}
	testCG, err := startProcessInNamespace(ctx, nc.NamespacePublisher, clientGenBin, testCGFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting test CG: %v\n", err)
		return false
	}

	// Wait for connections
	fmt.Println("Waiting 2s for connections to establish...")
	time.Sleep(2 * time.Second)

	// Create metrics collectors (one per pipeline)
	udsPaths := config.GetAllUDSPaths(testID)
	controlMetrics := NewTestMetrics(
		MetricsEndpoint{UDSPath: udsPaths["server_control"]},
		MetricsEndpoint{UDSPath: udsPaths["cg_control"]},
		MetricsEndpoint{}, // No client
	)
	testMetrics := NewTestMetrics(
		MetricsEndpoint{UDSPath: udsPaths["server_test"]},
		MetricsEndpoint{UDSPath: udsPaths["cg_test"]},
		MetricsEndpoint{}, // No client
	)

	// Collect initial metrics
	fmt.Println("Collecting initial metrics...")
	controlMetrics.CollectAllMetrics("startup")
	testMetrics.CollectAllMetrics("startup")

	// Run for test duration
	fmt.Printf("Running for %v...\n", config.TestDuration)
	time.Sleep(config.TestDuration)

	// Collect final metrics
	fmt.Println("\nCollecting final metrics...")
	controlMetrics.CollectAllMetrics("final")
	testMetrics.CollectAllMetrics("final")

	// Print raw Prometheus metrics if PRINT_PROM=true (BEFORE shutdown!)
	// This must be done before processes exit and remove their UDS sockets
	printAllPrometheusMetrics(udsPaths)

	// Shutdown
	fmt.Println("\nShutting down...")
	_ = signalProcess(controlCG, syscall.SIGINT)
	_ = signalProcess(testCG, syscall.SIGINT)
	time.Sleep(500 * time.Millisecond)
	_ = signalProcess(controlServer, syscall.SIGINT)
	_ = signalProcess(testServer, syscall.SIGINT)

	// Wait for processes to exit
	waitForProcessExit(controlCG, 3*time.Second)
	waitForProcessExit(testCG, 3*time.Second)
	waitForProcessExit(controlServer, 3*time.Second)
	waitForProcessExit(testServer, 3*time.Second)

	// Print comparison
	printIsolationComparison(config.Name,
		controlMetrics.GetSnapshotByLabel("server", "final"),
		testMetrics.GetSnapshotByLabel("server", "final"),
		controlMetrics.GetSnapshotByLabel("client-generator", "final"),
		testMetrics.GetSnapshotByLabel("client-generator", "final"),
	)

	elapsed := time.Since(startTime)
	fmt.Printf("\n=== Isolation Test Complete (%v) ===\n", elapsed.Round(time.Second))

	return true
}

// printIsolationComparison prints a simple comparison table
func printIsolationComparison(testName string, controlServer, testServer, controlCG, testCG *MetricsSnapshot) {
	fmt.Println()
	fmt.Println("╔═════════════════════════════════════════════════════════════════════╗")
	fmt.Printf("║ %-67s ║\n", "ISOLATION TEST RESULTS: "+testName)
	fmt.Println("╠═════════════════════════════════════════════════════════════════════╣")

	// Extract key metrics
	type MetricRow struct {
		Name    string
		Control float64
		Test    float64
	}

	// Server metrics
	serverRows := []MetricRow{
		{"Packets Received", getMetricSum(controlServer, "gosrt_connection_congestion_packets_total", "direction=\"recv\""),
			getMetricSum(testServer, "gosrt_connection_congestion_packets_total", "direction=\"recv\"")},
		{"Gaps Detected", getMetricSum(controlServer, "gosrt_connection_congestion_packets_lost_total", "direction=\"recv\""),
			getMetricSum(testServer, "gosrt_connection_congestion_packets_lost_total", "direction=\"recv\"")},
		{"Retrans Received", getMetricSum(controlServer, "gosrt_connection_congestion_retransmissions_total", "direction=\"recv\""),
			getMetricSum(testServer, "gosrt_connection_congestion_retransmissions_total", "direction=\"recv\"")},
		{"NAKs Sent", getMetricSum(controlServer, "gosrt_connection_packets_sent_total", "type=\"nak\""),
			getMetricSum(testServer, "gosrt_connection_packets_sent_total", "type=\"nak\"")},
		{"Drops", getMetricSum(controlServer, "gosrt_connection_congestion_recv_data_drop_total", ""),
			getMetricSum(testServer, "gosrt_connection_congestion_recv_data_drop_total", "")},
	}

	// CG metrics
	cgRows := []MetricRow{
		{"Packets Sent", getMetricSum(controlCG, "gosrt_connection_congestion_packets_total", "direction=\"send\""),
			getMetricSum(testCG, "gosrt_connection_congestion_packets_total", "direction=\"send\"")},
		{"Retrans Sent", getMetricSum(controlCG, "gosrt_connection_congestion_retransmissions_total", "direction=\"send\""),
			getMetricSum(testCG, "gosrt_connection_congestion_retransmissions_total", "direction=\"send\"")},
		{"NAKs Received", getMetricSum(controlCG, "gosrt_connection_packets_received_total", "type=\"nak\""),
			getMetricSum(testCG, "gosrt_connection_packets_received_total", "type=\"nak\"")},
	}

	fmt.Printf("║ %-28s %12s %12s %12s ║\n", "SERVER METRICS", "Control", "Test", "Diff")
	fmt.Printf("║ %-28s %12s %12s %12s ║\n", "────────────────────────────", "────────────", "────────────", "────────────")
	for _, row := range serverRows {
		diff := formatDiff(row.Control, row.Test)
		fmt.Printf("║ %-28s %12.0f %12.0f %12s ║\n", row.Name, row.Control, row.Test, diff)
	}

	fmt.Printf("║ %-67s ║\n", "")
	fmt.Printf("║ %-28s %12s %12s %12s ║\n", "CLIENT-GENERATOR METRICS", "Control", "Test", "Diff")
	fmt.Printf("║ %-28s %12s %12s %12s ║\n", "────────────────────────────", "────────────", "────────────", "────────────")
	for _, row := range cgRows {
		diff := formatDiff(row.Control, row.Test)
		fmt.Printf("║ %-28s %12.0f %12.0f %12s ║\n", row.Name, row.Control, row.Test, diff)
	}

	fmt.Println("╚═════════════════════════════════════════════════════════════════════╝")

	// Highlight if there's a significant gap difference
	controlGaps := getMetricSum(controlServer, "gosrt_connection_congestion_packets_lost_total", "direction=\"recv\"")
	testGaps := getMetricSum(testServer, "gosrt_connection_congestion_packets_lost_total", "direction=\"recv\"")

	if controlGaps == 0 && testGaps == 0 {
		fmt.Println("\n✓ GOOD: Both pipelines show 0 gaps (clean network)")
	} else if testGaps > controlGaps*1.1 { // Test has >10% more gaps
		fmt.Printf("\n⚠ FINDING: Test pipeline has more gaps (%.0f vs %.0f) - this variable may be the cause!\n",
			testGaps, controlGaps)
	} else if controlGaps > testGaps*1.1 { // Control has >10% more gaps
		fmt.Printf("\n✓ Test pipeline has FEWER gaps (%.0f vs %.0f)\n", testGaps, controlGaps)
	} else {
		fmt.Println("\n= Both pipelines show similar gap counts")
	}
}

// getMetricSum sums metrics matching a prefix and optional label filter
func getMetricSum(snapshot *MetricsSnapshot, prefix, labelFilter string) float64 {
	if snapshot == nil {
		return 0
	}
	var sum float64
	for name, value := range snapshot.Metrics {
		if strings.HasPrefix(name, prefix) {
			if labelFilter == "" || strings.Contains(name, labelFilter) {
				sum += value
			}
		}
	}
	return sum
}

// formatDiff formats the difference between control and test
func formatDiff(control, test float64) string {
	if control == 0 && test == 0 {
		return "="
	}
	if control == 0 {
		return "NEW"
	}
	diff := ((test - control) / control) * 100
	if diff > 0 {
		return fmt.Sprintf("+%.1f%%", diff)
	} else if diff < 0 {
		return fmt.Sprintf("%.1f%%", diff)
	}
	return "="
}

// testIsolationModeWithConfig runs a single isolation test
func testIsolationModeWithConfig(config IsolationTestConfig) {
	passed := runIsolationModeTest(config)
	if passed {
		fmt.Printf("\n=== Isolation Test (%s): COMPLETED ===\n", config.Name)
	} else {
		fmt.Printf("\n=== Isolation Test (%s): FAILED ===\n", config.Name)
		os.Exit(1)
	}
}

// testIsolationModeAllConfigs runs all isolation tests
func testIsolationModeAllConfigs() {
	fmt.Println("Running all isolation tests...")
	fmt.Printf("Total: %d tests × ~30s = ~%d seconds\n\n", len(IsolationTestConfigs), len(IsolationTestConfigs)*35)

	passed := 0
	failed := 0
	var failedConfigs []string

	for i, config := range IsolationTestConfigs {
		fmt.Printf("\n[%d/%d] Running: %s\n", i+1, len(IsolationTestConfigs), config.Name)
		fmt.Println(strings.Repeat("=", 60))

		result := runIsolationModeTest(config)
		if result {
			fmt.Printf("✓ Test %d (%s) COMPLETED\n", i, config.Name)
			passed++
		} else {
			fmt.Printf("✗ Test %d (%s) FAILED\n", i, config.Name)
			failed++
			failedConfigs = append(failedConfigs, config.Name)
		}
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("=== Results: %d/%d completed, %d failed ===\n", passed, len(IsolationTestConfigs), failed)
	if len(failedConfigs) > 0 {
		fmt.Println("Failed tests:")
		for _, name := range failedConfigs {
			fmt.Printf("  - %s\n", name)
		}
	}
}

// Note: ensureBinaries is defined in test_graceful_shutdown.go

// printPrometheusMetrics fetches and prints metrics from a UDS path
func printPrometheusMetrics(label string, udsPath string) {
	if udsPath == "" {
		fmt.Printf("\n=== PROMETHEUS METRICS (%s) ===\n", label)
		fmt.Println("(no UDS path configured)")
		return
	}

	// Check if socket exists
	if _, err := os.Stat(udsPath); os.IsNotExist(err) {
		fmt.Printf("\n=== PROMETHEUS METRICS (%s) ===\n", label)
		fmt.Printf("(socket not found: %s)\n", udsPath)
		return
	}

	// Create HTTP client using Unix socket
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", udsPath)
			},
		},
		Timeout: 5 * time.Second,
	}

	// Fetch metrics
	resp, err := client.Get("http://localhost/metrics")
	if err != nil {
		fmt.Printf("\n=== PROMETHEUS METRICS (%s) ===\n", label)
		fmt.Printf("(error fetching metrics: %v)\n", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("\n=== PROMETHEUS METRICS (%s) ===\n", label)
		fmt.Printf("(error reading response: %v)\n", err)
		return
	}

	// Parse and filter metrics
	lines := strings.Split(string(body), "\n")
	var filtered []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Filter: show gosrt_* and go_* (runtime) metrics, skip comments
		if strings.HasPrefix(line, "#") {
			continue // Skip Prometheus comment/HELP/TYPE lines
		}
		if strings.HasPrefix(line, "gosrt_") || strings.HasPrefix(line, "go_") {
			filtered = append(filtered, line)
		}
	}

	// Sort for consistent output
	sort.Strings(filtered)

	fmt.Printf("\n=== PROMETHEUS METRICS (%s) ===\n", label)
	if len(filtered) == 0 {
		fmt.Println("(no connection metrics found)")
	} else {
		for _, line := range filtered {
			fmt.Println(line)
		}
	}
}

// printAllPrometheusMetrics prints metrics from all UDS paths if PRINT_PROM=true
func printAllPrometheusMetrics(udsPaths map[string]string) {
	if os.Getenv("PRINT_PROM") != "true" {
		return
	}

	fmt.Println("\n" + strings.Repeat("=", 70))
	fmt.Println("PROMETHEUS METRICS DUMP (PRINT_PROM=true)")
	fmt.Println(strings.Repeat("=", 70))

	// Print in consistent order
	orderedKeys := []string{"server_control", "server_test", "cg_control", "cg_test"}
	labels := map[string]string{
		"server_control": "Control Server",
		"server_test":    "Test Server",
		"cg_control":     "Control CG",
		"cg_test":        "Test CG",
	}

	for _, key := range orderedKeys {
		if path, ok := udsPaths[key]; ok {
			printPrometheusMetrics(labels[key], path)
		}
	}
}
