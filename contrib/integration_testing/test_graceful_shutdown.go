package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	testName := os.Args[1]

	switch testName {
	case "graceful-shutdown-sigint":
		// Run with default config
		config := GetTestConfigByName("Default-2Mbps")
		if config == nil {
			config = &TestConfigs[0] // Fallback to first config
		}
		testGracefulShutdownSIGINTWithConfig(*config)

	case "graceful-shutdown-sigint-all":
		// Run all configurations
		testGracefulShutdownSIGINTAllConfigs()

	case "graceful-shutdown-sigint-config":
		// Run with specific configuration
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Error: config name required\n")
			fmt.Fprintf(os.Stderr, "Usage: %s graceful-shutdown-sigint-config <config-name>\n", os.Args[0])
			fmt.Fprintf(os.Stderr, "\nAvailable configurations:\n")
			for _, c := range TestConfigs {
				fmt.Fprintf(os.Stderr, "  %s - %s\n", c.Name, c.Description)
			}
			os.Exit(1)
		}
		configName := os.Args[2]
		config := GetTestConfigByName(configName)
		if config == nil {
			fmt.Fprintf(os.Stderr, "Error: unknown configuration: %s\n", configName)
			fmt.Fprintf(os.Stderr, "\nAvailable configurations:\n")
			for _, c := range TestConfigs {
				fmt.Fprintf(os.Stderr, "  %s - %s\n", c.Name, c.Description)
			}
			os.Exit(1)
		}
		testGracefulShutdownSIGINTWithConfig(*config)

	case "list-configs":
		// List all configurations
		fmt.Println("Available test configurations (clean network):")
		fmt.Println()
		for _, c := range TestConfigs {
			fmt.Printf("  %-35s %s\n", c.Name, c.Description)
		}

	case "list-network-configs":
		// List network impairment configurations
		fmt.Println("Available network impairment configurations (require root):")
		fmt.Println()
		for _, c := range NetworkTestConfigs {
			fmt.Printf("  %-45s %s\n", c.Name, c.Description)
		}

	case "network-test":
		// Run a specific network impairment test
		// Usage: network-test <config-name> [-v|--verbose]
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Error: config name required\n")
			fmt.Fprintf(os.Stderr, "Usage: sudo %s network-test <config-name> [-v|--verbose]\n", os.Args[0])
			fmt.Fprintf(os.Stderr, "\nAvailable configurations:\n")
			for _, c := range NetworkTestConfigs {
				fmt.Fprintf(os.Stderr, "  %-45s %s\n", c.Name, c.Description)
			}
			os.Exit(1)
		}
		configName := os.Args[2]
		config := GetNetworkTestConfigByName(configName)
		if config == nil {
			fmt.Fprintf(os.Stderr, "Error: unknown network configuration: %s\n", configName)
			fmt.Fprintf(os.Stderr, "\nAvailable configurations:\n")
			for _, c := range NetworkTestConfigs {
				fmt.Fprintf(os.Stderr, "  %-45s %s\n", c.Name, c.Description)
			}
			os.Exit(1)
		}
		// Check for verbose flag
		for _, arg := range os.Args[3:] {
			if arg == "-v" || arg == "--verbose" {
				config.VerboseMetrics = true
				config.VerboseNetwork = true
				fmt.Println("Verbose mode enabled (metrics + network)")
			}
		}
		testNetworkModeWithConfig(*config)

	case "network-test-all":
		// Run all network impairment tests
		testNetworkModeAllConfigs()

	case "list-parallel-configs":
		// List parallel comparison test configurations
		fmt.Println("Available parallel comparison configurations (require root):")
		fmt.Println()
		for _, c := range ParallelTestConfigs {
			fmt.Printf("  %-35s %s\n", c.Name, c.Description)
		}

	case "parallel-test":
		// Run a specific parallel comparison test
		// Usage: parallel-test <config-name> [-v|--verbose]
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Error: config name required\n")
			fmt.Fprintf(os.Stderr, "Usage: sudo %s parallel-test <config-name> [-v|--verbose]\n", os.Args[0])
			fmt.Fprintf(os.Stderr, "\nAvailable configurations:\n")
			for _, c := range ParallelTestConfigs {
				fmt.Fprintf(os.Stderr, "  %-35s %s\n", c.Name, c.Description)
			}
			os.Exit(1)
		}
		configName := os.Args[2]
		config := GetParallelTestConfigByName(configName)
		if config == nil {
			fmt.Fprintf(os.Stderr, "Error: unknown parallel configuration: %s\n", configName)
			fmt.Fprintf(os.Stderr, "\nAvailable configurations:\n")
			for _, c := range ParallelTestConfigs {
				fmt.Fprintf(os.Stderr, "  %-35s %s\n", c.Name, c.Description)
			}
			os.Exit(1)
		}
		// Check for verbose flag
		for _, arg := range os.Args[3:] {
			if arg == "-v" || arg == "--verbose" {
				config.VerboseMetrics = true
				config.VerboseNetwork = true
				fmt.Println("Verbose mode enabled (metrics + network)")
			}
		}
		testParallelModeWithConfig(*config)

	case "parallel-test-all":
		// Run all parallel comparison tests
		testParallelModeAllConfigs()

	case "list-isolation-configs":
		// List isolation test configurations
		fmt.Println("Available isolation test configurations (require root):")
		fmt.Println()
		for _, c := range IsolationTestConfigs {
			fmt.Printf("  %-35s %s\n", c.Name, c.Description)
		}

	case "isolation-test":
		// Run a specific isolation test
		// Usage: isolation-test <config-name>
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Error: config name required\n")
			fmt.Fprintf(os.Stderr, "Usage: sudo %s isolation-test <config-name>\n", os.Args[0])
			fmt.Fprintf(os.Stderr, "\nAvailable configurations:\n")
			for _, c := range IsolationTestConfigs {
				fmt.Fprintf(os.Stderr, "  %-35s %s\n", c.Name, c.Description)
			}
			os.Exit(1)
		}
		configName := os.Args[2]
		config := GetIsolationTestConfigByName(configName)
		if config == nil {
			fmt.Fprintf(os.Stderr, "Error: unknown isolation configuration: %s\n", configName)
			fmt.Fprintf(os.Stderr, "\nAvailable configurations:\n")
			for _, c := range IsolationTestConfigs {
				fmt.Fprintf(os.Stderr, "  %-35s %s\n", c.Name, c.Description)
			}
			os.Exit(1)
		}
		testIsolationModeWithConfig(*config)

	case "isolation-test-all":
		// Run all isolation tests
		testIsolationModeAllConfigs()

	// Matrix-generated test commands
	case "matrix-list":
		// List all matrix-generated tests
		listMatrixTests()

	case "matrix-summary":
		// Show summary of matrix tests by tier
		showMatrixSummary()

	case "matrix-list-tier1":
		// List Tier 1 (Core) tests only
		listMatrixTestsByTier(TierCore)

	case "matrix-list-tier2":
		// List Tier 1+2 (Daily) tests
		listMatrixTestsByTier(TierDaily)

	case "matrix-run-tier1":
		// Run all Tier 1 tests (requires root)
		runMatrixTestsByTier(TierCore)

	case "matrix-run-tier2":
		// Run Tier 1+2 tests (requires root)
		runMatrixTestsByTier(TierDaily)

	case "matrix-run-all":
		// Run all matrix tests (requires root)
		runMatrixTestsByTier(TierNightly)

	// Clean network matrix commands
	case "clean-matrix-list":
		// List all clean network matrix tests
		listCleanMatrixTests()

	case "clean-matrix-summary":
		// Show summary of clean network tests
		showCleanMatrixSummary()

	case "clean-matrix-tier1-list":
		// List Tier 1 clean network tests
		listCleanMatrixTestsByTier(TierCore)

	case "clean-matrix-tier2-list":
		// List Tier 1+2 clean network tests
		listCleanMatrixTestsByTier(TierDaily)

	case "clean-matrix-run-tier1":
		// Run Tier 1 clean network tests (no root needed)
		runCleanMatrixTestsByTier(TierCore)

	case "clean-matrix-run-tier2":
		// Run Tier 1+2 clean network tests (no root needed)
		runCleanMatrixTestsByTier(TierDaily)

	case "clean-matrix-run-all":
		// Run all clean network tests (no root needed)
		runCleanMatrixTestsByTier(TierNightly)

	default:
		fmt.Fprintf(os.Stderr, "Unknown test: %s\n", testName)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <test-name> [options]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nClean Network Tests (no root required):\n")
	fmt.Fprintf(os.Stderr, "  graceful-shutdown-sigint              Run graceful shutdown test with default config\n")
	fmt.Fprintf(os.Stderr, "  graceful-shutdown-sigint-all          Run graceful shutdown test with all configurations\n")
	fmt.Fprintf(os.Stderr, "  graceful-shutdown-sigint-config NAME  Run graceful shutdown test with specific config\n")
	fmt.Fprintf(os.Stderr, "  list-configs                          List all clean network configurations\n")
	fmt.Fprintf(os.Stderr, "\nNetwork Impairment Tests (require root):\n")
	fmt.Fprintf(os.Stderr, "  network-test NAME                     Run network impairment test with specific config\n")
	fmt.Fprintf(os.Stderr, "  network-test-all                      Run all network impairment tests\n")
	fmt.Fprintf(os.Stderr, "  list-network-configs                  List all network impairment configurations\n")
	fmt.Fprintf(os.Stderr, "\nParallel Comparison Tests (require root):\n")
	fmt.Fprintf(os.Stderr, "  parallel-test NAME                    Run parallel comparison test (Baseline vs HighPerf)\n")
	fmt.Fprintf(os.Stderr, "  parallel-test-all                     Run all parallel comparison tests\n")
	fmt.Fprintf(os.Stderr, "  list-parallel-configs                 List all parallel comparison configurations\n")
	fmt.Fprintf(os.Stderr, "\nIsolation Tests (require root):\n")
	fmt.Fprintf(os.Stderr, "  isolation-test NAME                   Run CG→Server isolation test (single variable change)\n")
	fmt.Fprintf(os.Stderr, "  isolation-test-all                    Run all isolation tests (~3.5 min total)\n")
	fmt.Fprintf(os.Stderr, "  list-isolation-configs                List all isolation test configurations\n")
	fmt.Fprintf(os.Stderr, "\nMatrix-Generated Parallel Tests (require root):\n")
	fmt.Fprintf(os.Stderr, "  matrix-list                           List all matrix-generated tests (64 tests)\n")
	fmt.Fprintf(os.Stderr, "  matrix-summary                        Show test summary by tier and category\n")
	fmt.Fprintf(os.Stderr, "  matrix-list-tier1                     List Tier 1 (Core) tests (~25 tests)\n")
	fmt.Fprintf(os.Stderr, "  matrix-list-tier2                     List Tier 1+2 (Daily) tests (~42 tests)\n")
	fmt.Fprintf(os.Stderr, "  matrix-run-tier1                      Run Tier 1 tests (require root, ~40 min)\n")
	fmt.Fprintf(os.Stderr, "  matrix-run-tier2                      Run Tier 1+2 tests (require root, ~70 min)\n")
	fmt.Fprintf(os.Stderr, "  matrix-run-all                        Run all matrix tests (require root, ~100 min)\n")
	fmt.Fprintf(os.Stderr, "\nMatrix-Generated Clean Network Tests (no root needed):\n")
	fmt.Fprintf(os.Stderr, "  clean-matrix-list                     List all clean network tests (~42 tests)\n")
	fmt.Fprintf(os.Stderr, "  clean-matrix-summary                  Show clean test summary by tier\n")
	fmt.Fprintf(os.Stderr, "  clean-matrix-tier1-list               List Tier 1 clean tests (~14 tests)\n")
	fmt.Fprintf(os.Stderr, "  clean-matrix-tier2-list               List Tier 1+2 clean tests (~24 tests)\n")
	fmt.Fprintf(os.Stderr, "  clean-matrix-run-tier1                Run Tier 1 clean tests (~4 min)\n")
	fmt.Fprintf(os.Stderr, "  clean-matrix-run-tier2                Run Tier 1+2 clean tests (~6 min)\n")
	fmt.Fprintf(os.Stderr, "  clean-matrix-run-all                  Run all clean tests (~10 min)\n")
}

// testGracefulShutdownSIGINTAllConfigs runs the graceful shutdown test with all configurations
func testGracefulShutdownSIGINTAllConfigs() {
	fmt.Println("=== Test 1.1: Graceful Shutdown on SIGINT (All Configurations) ===")
	fmt.Println()
	fmt.Printf("Total configurations to test: %d\n", len(TestConfigs))
	fmt.Println()

	passed := 0
	failed := 0
	var failedConfigs []string

	for i, config := range TestConfigs {
		fmt.Printf("\n--- Configuration %d/%d: %s ---\n", i+1, len(TestConfigs), config.Name)
		fmt.Printf("Description: %s\n", config.Description)
		fmt.Printf("Bitrate: %d bps (%.2f Mb/s)\n", config.Bitrate, float64(config.Bitrate)/1_000_000)
		fmt.Println()

		err := runTestWithConfig(config)
		if err != nil {
			fmt.Printf("✗ Configuration %s FAILED: %v\n", config.Name, err)
			failed++
			failedConfigs = append(failedConfigs, config.Name)
		} else {
			fmt.Printf("✓ Configuration %s PASSED\n", config.Name)
			passed++
		}

		// Wait between tests
		if i < len(TestConfigs)-1 {
			time.Sleep(2 * time.Second)
		}
	}

	fmt.Println()
	fmt.Printf("=== Results: %d/%d passed, %d failed ===\n", passed, len(TestConfigs), failed)

	if failed > 0 {
		fmt.Println("\nFailed configurations:")
		for _, name := range failedConfigs {
			fmt.Printf("  - %s\n", name)
		}
		os.Exit(1)
	}
}

// testGracefulShutdownSIGINTWithConfig runs the graceful shutdown test with a specific configuration
func testGracefulShutdownSIGINTWithConfig(config TestConfig) {
	fmt.Printf("=== Test 1.1: Graceful Shutdown on SIGINT (%s) ===\n", config.Name)
	fmt.Println()
	fmt.Printf("Description: %s\n", config.Description)
	fmt.Printf("Bitrate: %d bps (%.2f Mb/s)\n", config.Bitrate, float64(config.Bitrate)/1_000_000)
	fmt.Println()

	if err := runTestWithConfig(config); err != nil {
		fmt.Fprintf(os.Stderr, "Test FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("=== Test 1.1 (%s): PASSED ===\n", config.Name)
}

// testNetworkModeWithConfig runs a network impairment test with a specific configuration
func testNetworkModeWithConfig(config TestConfig) {
	fmt.Printf("=== Network Impairment Test: %s ===\n", config.Name)
	fmt.Println()
	fmt.Printf("Description: %s\n", config.Description)
	fmt.Printf("Mode: %s\n", config.Mode)
	fmt.Printf("Bitrate: %d bps (%.2f Mb/s)\n", config.Bitrate, float64(config.Bitrate)/1_000_000)
	if config.Impairment.LossRate > 0 {
		fmt.Printf("Loss Rate: %.1f%%\n", config.Impairment.LossRate*100)
	}
	if config.Impairment.LatencyProfile != "" {
		fmt.Printf("Latency Profile: %s\n", config.Impairment.LatencyProfile)
	}
	if config.Impairment.Pattern != "" {
		fmt.Printf("Impairment Pattern: %s\n", config.Impairment.Pattern)
	}
	fmt.Println()

	if err := runTestWithConfig(config); err != nil {
		fmt.Fprintf(os.Stderr, "Test FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("=== Network Test (%s): PASSED ===\n", config.Name)
}

// testNetworkModeAllConfigs runs all network impairment tests
func testNetworkModeAllConfigs() {
	fmt.Println("=== Network Impairment Tests (All Configurations) ===")
	fmt.Println()
	fmt.Printf("Total configurations to test: %d\n", len(NetworkTestConfigs))
	fmt.Println()
	fmt.Println("NOTE: These tests require root privileges for network namespace creation.")
	fmt.Println()

	passed := 0
	failed := 0
	var failedConfigs []string

	for i, config := range NetworkTestConfigs {
		fmt.Printf("\n--- Configuration %d/%d: %s ---\n", i+1, len(NetworkTestConfigs), config.Name)
		fmt.Printf("Description: %s\n", config.Description)
		fmt.Printf("Bitrate: %d bps (%.2f Mb/s)\n", config.Bitrate, float64(config.Bitrate)/1_000_000)
		if config.Impairment.LossRate > 0 {
			fmt.Printf("Loss Rate: %.1f%%\n", config.Impairment.LossRate*100)
		}
		if config.Impairment.LatencyProfile != "" {
			fmt.Printf("Latency Profile: %s\n", config.Impairment.LatencyProfile)
		}
		if config.Impairment.Pattern != "" {
			fmt.Printf("Impairment Pattern: %s\n", config.Impairment.Pattern)
		}
		fmt.Println()

		err := runTestWithConfig(config)
		if err != nil {
			fmt.Printf("✗ Configuration %s FAILED: %v\n", config.Name, err)
			failed++
			failedConfigs = append(failedConfigs, config.Name)
		} else {
			fmt.Printf("✓ Configuration %s PASSED\n", config.Name)
			passed++
		}

		// Wait between tests for cleanup
		if i < len(NetworkTestConfigs)-1 {
			time.Sleep(5 * time.Second)
		}
	}

	fmt.Println()
	fmt.Printf("=== Results: %d/%d passed, %d failed ===\n", passed, len(NetworkTestConfigs), failed)

	if failed > 0 {
		fmt.Println("\nFailed configurations:")
		for _, name := range failedConfigs {
			fmt.Printf("  - %s\n", name)
		}
		os.Exit(1)
	}
}

// testParallelModeWithConfig runs a parallel comparison test with a specific configuration
func testParallelModeWithConfig(config ParallelTestConfig) {
	printParallelTestHeader(config)

	result := runParallelModeTest(config)

	if !result.Passed {
		fmt.Fprintf(os.Stderr, "Test FAILED: parallel execution failed\n")
		os.Exit(1)
	}

	// Perform comparison analysis
	if result.BaselineMetrics != nil && result.HighPerfMetrics != nil {
		// Detailed comparison between pipelines
		comparisons := CompareParallelPipelines(result.BaselineMetrics, result.HighPerfMetrics)
		PrintDetailedComparison(comparisons)
	}

	fmt.Println()
	fmt.Printf("=== Parallel Test (%s): PASSED ===\n", config.Name)
}

// testParallelModeAllConfigs runs all parallel comparison tests
func testParallelModeAllConfigs() {
	fmt.Println("=== Parallel Comparison Tests (All Configurations) ===")
	fmt.Println()
	fmt.Printf("Total configurations to test: %d\n", len(ParallelTestConfigs))
	fmt.Println()
	fmt.Println("NOTE: These tests require root privileges for network namespace creation.")
	fmt.Println()

	passed := 0
	failed := 0
	var failedConfigs []string

	for i, config := range ParallelTestConfigs {
		fmt.Printf("\n--- Configuration %d/%d: %s ---\n", i+1, len(ParallelTestConfigs), config.Name)
		printParallelTestHeader(config)

		result := runParallelModeTest(config)
		if !result.Passed {
			fmt.Printf("✗ Configuration %s FAILED\n", config.Name)
			failed++
			failedConfigs = append(failedConfigs, config.Name)
		} else {
			// Print detailed comparison
			if result.BaselineMetrics != nil && result.HighPerfMetrics != nil {
				comparisons := CompareParallelPipelines(result.BaselineMetrics, result.HighPerfMetrics)
				PrintDetailedComparison(comparisons)
			}
			fmt.Printf("✓ Configuration %s PASSED\n", config.Name)
			passed++
		}

		// Wait between tests for cleanup
		if i < len(ParallelTestConfigs)-1 {
			time.Sleep(5 * time.Second)
		}
	}

	fmt.Println()
	fmt.Printf("=== Results: %d/%d passed, %d failed ===\n", passed, len(ParallelTestConfigs), failed)

	if failed > 0 {
		fmt.Println("\nFailed configurations:")
		for _, name := range failedConfigs {
			fmt.Printf("  - %s\n", name)
		}
		os.Exit(1)
	}
}

// Note: printParallelComparisonSummary was replaced by PrintDetailedComparison in parallel_analysis.go

// runTestWithConfig runs the graceful shutdown test with the given configuration
func runTestWithConfig(config TestConfig) error {
	var testPassed bool
	var testMetrics *TestMetrics
	var startTime, endTime time.Time

	// Dispatch based on test mode
	if config.Mode == TestModeNetwork {
		// Run in network namespace mode (requires root)
		testPassed, testMetrics, startTime, endTime = runNetworkModeTest(config)
	} else {
		// Run in clean network mode (default - uses loopback)
		testPassed, testMetrics, startTime, endTime = runTestWithMetrics(config)
	}

	if !testPassed {
		return fmt.Errorf("test execution failed")
	}

	// Perform metrics analysis if metrics were collected
	if testMetrics != nil && config.MetricsEnabled {
		analysisResult := AnalyzeTestResults(testMetrics, &config, startTime, endTime)
		PrintAnalysisResult(analysisResult)

		if !analysisResult.Passed {
			return fmt.Errorf("metrics analysis failed: %s", analysisResult.Summary)
		}
	}

	return nil
}

// runTestWithMetrics runs the test and returns metrics for analysis
func runTestWithMetrics(config TestConfig) (passed bool, metrics *TestMetrics, startTime, endTime time.Time) {
	startTime = time.Now()

	// Get network configuration
	serverNet, clientGenNet, clientNet := config.GetEffectiveNetworkConfig()

	// Print network configuration
	fmt.Println("Network Configuration:")
	fmt.Printf("  Server:           %s (metrics: %s)\n", serverNet.SRTAddr(), serverNet.MetricsAddr())
	fmt.Printf("  Client-Generator: %s (metrics: %s)\n", clientGenNet.IP, clientGenNet.MetricsAddr())
	fmt.Printf("  Client:           %s (metrics: %s)\n", clientNet.IP, clientNet.MetricsAddr())
	fmt.Println()

	// Get the base directory
	baseDir := getBaseDir()

	// Build paths to binaries
	serverBin := filepath.Join(baseDir, "contrib", "server", "server")
	clientGenBin := filepath.Join(baseDir, "contrib", "client-generator", "client-generator")
	clientBin := filepath.Join(baseDir, "contrib", "client", "client")

	// Check if binaries exist
	if err := ensureBinaries(baseDir, serverBin, clientGenBin, clientBin); err != nil {
		fmt.Fprintf(os.Stderr, "Error building binaries: %v\n", err)
		return false, nil, startTime, time.Now()
	}

	// Create context for test orchestration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get CLI flags for each component
	serverFlags := config.GetServerFlags()
	clientGenFlags := config.GetClientGeneratorFlags()
	clientFlags := config.GetClientFlags()

	// Print CLI flags for debugging
	fmt.Println("CLI Flags:")
	fmt.Printf("  Server: %s\n", strings.Join(serverFlags, " "))
	fmt.Printf("  Client-Generator: %s\n", strings.Join(clientGenFlags, " "))
	fmt.Printf("  Client: %s\n", strings.Join(clientFlags, " "))
	fmt.Println()

	// Start server
	fmt.Println("Starting server...")
	serverCmd := exec.CommandContext(ctx, serverBin, serverFlags...)
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr
	if err := serverCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
		return false, nil, startTime, time.Now()
	}
	defer func() {
		if serverCmd.Process != nil {
			serverCmd.Process.Kill()
		}
	}()

	// Wait for server to start
	time.Sleep(500 * time.Millisecond)

	// Start client-generator (publisher)
	fmt.Println("Starting client-generator (publisher)...")
	clientGenCmd := exec.CommandContext(ctx, clientGenBin, clientGenFlags...)
	clientGenCmd.Stdout = os.Stdout
	clientGenCmd.Stderr = os.Stderr
	if err := clientGenCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting client-generator: %v\n", err)
		return false, nil, startTime, time.Now()
	}
	defer func() {
		if clientGenCmd.Process != nil {
			clientGenCmd.Process.Kill()
		}
	}()

	// Wait for publisher to connect
	time.Sleep(500 * time.Millisecond)

	// Start client (subscriber)
	fmt.Println("Starting client (subscriber)...")
	clientCmd := exec.CommandContext(ctx, clientBin, clientFlags...)
	clientCmd.Stdout = os.Stdout
	clientCmd.Stderr = os.Stderr
	if err := clientCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting client: %v\n", err)
		return false, nil, startTime, time.Now()
	}
	defer func() {
		if clientCmd.Process != nil {
			clientCmd.Process.Kill()
		}
	}()

	// Wait for connections to establish
	fmt.Printf("Waiting %v for connections to establish...\n", config.ConnectionWait)
	time.Sleep(config.ConnectionWait)

	// Verify processes are running
	if serverCmd.Process == nil || clientGenCmd.Process == nil || clientCmd.Process == nil {
		fmt.Fprintf(os.Stderr, "Error: one or more processes failed to start\n")
		return false, nil, startTime, time.Now()
	}

	// Initialize metrics collection if enabled
	var testMetrics *TestMetrics
	if config.MetricsEnabled {
		serverEndpoint, clientGenEndpoint, clientEndpoint := config.GetAllMetricsEndpoints()
		testMetrics = NewTestMetrics(serverEndpoint, clientGenEndpoint, clientEndpoint)

		// Collect initial metrics
		fmt.Println("\nCollecting initial metrics...")
		testMetrics.CollectAllMetrics("startup")
	}

	// Run for test duration
	fmt.Printf("All processes started. Running for %v...\n", config.TestDuration)

	// Periodically collect metrics during test
	if config.MetricsEnabled && config.CollectInterval > 0 {
		collectTicker := time.NewTicker(config.CollectInterval)
		testTimer := time.NewTimer(config.TestDuration)

	collectLoop:
		for {
			select {
			case <-collectTicker.C:
				fmt.Println("\nCollecting mid-test metrics...")
				testMetrics.CollectAllMetrics("mid-test")
			case <-testTimer.C:
				collectTicker.Stop()
				break collectLoop
			}
		}
	} else {
		time.Sleep(config.TestDuration)
	}

	// =================================================================
	// QUIESCE PHASE: Pause data flow and wait for metrics to stabilize
	// =================================================================
	fmt.Println("\n--- Quiesce Phase ---")

	// Step 1: Send SIGUSR1 to client-generator to pause data generation
	fmt.Println("Sending SIGUSR1 to client-generator (pause data)...")
	if err := clientGenCmd.Process.Signal(syscall.SIGUSR1); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGUSR1 to client-generator: %v\n", err)
		// Non-fatal: continue with shutdown even if pause fails
	}

	// Step 2: Wait for metrics to stabilize (ACKs, NAKs stop incrementing)
	if config.MetricsEnabled {
		fmt.Println("Waiting for metrics to stabilize...")
		stabCtx, stabCancel := context.WithTimeout(context.Background(), 10*time.Second)
		result := testMetrics.WaitForStabilization(stabCtx)
		stabCancel()

		if result.Stable {
			fmt.Printf("✓ Metrics stabilized in %v (%d iterations)\n", result.Elapsed.Round(time.Millisecond), result.Iterations)
		} else {
			fmt.Printf("⚠ Stabilization timeout after %v: %v\n", result.Elapsed.Round(time.Millisecond), result.Error)
			// Non-fatal: continue with metrics collection
		}
	} else {
		// No metrics - just wait a brief period for any in-flight packets
		time.Sleep(500 * time.Millisecond)
	}

	// Step 3: Collect pre-shutdown metrics (now accurate after stabilization)
	if config.MetricsEnabled {
		fmt.Println("Collecting pre-shutdown metrics...")
		testMetrics.CollectAllMetrics("pre-shutdown")
	}

	// =================================================================
	// SHUTDOWN PHASE: Gracefully stop all processes
	// =================================================================
	fmt.Println("\n--- Shutdown Phase ---")
	// Shutdown order: Client-Generator → (drain) → Client → Server
	// This allows us to verify pipeline balance after draining

	// Step 4: Send SIGINT to Client-Generator (publisher) - full shutdown
	fmt.Println("Sending SIGINT to client-generator (publisher)...")
	if err := clientGenCmd.Process.Signal(os.Interrupt); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGINT to client-generator: %v\n", err)
		return false, testMetrics, startTime, time.Now()
	}

	clientGenDone := make(chan struct{})
	go func() {
		clientGenCmd.Wait()
		close(clientGenDone)
	}()

	// Client-generator has internal 5-second timeouts for waitgroups, allow 8 seconds total
	select {
	case <-clientGenDone:
		fmt.Println("✓ Client-generator exited gracefully")
	case <-time.After(8 * time.Second):
		fmt.Fprintf(os.Stderr, "Error: client-generator did not exit within 8 seconds\n")
		return false, testMetrics, startTime, time.Now()
	}

	if clientGenCmd.ProcessState != nil && !clientGenCmd.ProcessState.Success() {
		fmt.Fprintf(os.Stderr, "Error: client-generator exited with non-zero code\n")
		return false, testMetrics, startTime, time.Now()
	}

	// Step 2: Brief wait before stopping client
	// Note: Pipeline balance verification uses pre-shutdown metrics (collected above)
	// since stopping client-generator causes subscriber connections to close
	time.Sleep(500 * time.Millisecond)

	// Step 3: Send SIGINT to Client (subscriber)
	fmt.Println("Sending SIGINT to client (subscriber)...")
	if err := clientCmd.Process.Signal(os.Interrupt); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGINT to client: %v\n", err)
		return false, testMetrics, startTime, time.Now()
	}

	clientDone := make(chan struct{})
	go func() {
		clientCmd.Wait()
		close(clientDone)
	}()

	// Client has internal 5-second timeouts for waitgroups, allow 8 seconds total
	select {
	case <-clientDone:
		fmt.Println("✓ Client exited gracefully")
	case <-time.After(8 * time.Second):
		fmt.Fprintf(os.Stderr, "Error: client did not exit within 8 seconds\n")
		return false, testMetrics, startTime, time.Now()
	}

	if clientCmd.ProcessState != nil && !clientCmd.ProcessState.Success() {
		fmt.Fprintf(os.Stderr, "Error: client exited with non-zero code\n")
		return false, testMetrics, startTime, time.Now()
	}

	time.Sleep(500 * time.Millisecond)

	// Step 4: Send SIGINT to Server (last)
	fmt.Println("Sending SIGINT to server...")
	if err := serverCmd.Process.Signal(os.Interrupt); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGINT to server: %v\n", err)
		return false, testMetrics, startTime, time.Now()
	}

	serverDone := make(chan struct{})
	go func() {
		serverCmd.Wait()
		close(serverDone)
	}()

	// Get shutdown delay from config or use default
	shutdownDelay := 5 * time.Second
	if config.SharedSRT != nil && config.SharedSRT.ShutdownDelay > 0 {
		shutdownDelay = config.SharedSRT.ShutdownDelay
	}

	select {
	case <-serverDone:
		fmt.Println("✓ Server shutdown completed")
	case <-time.After(shutdownDelay + 2*time.Second):
		fmt.Fprintf(os.Stderr, "Error: server did not shutdown within %v\n", shutdownDelay+2*time.Second)
		return false, testMetrics, startTime, time.Now()
	}

	if serverCmd.ProcessState != nil && !serverCmd.ProcessState.Success() {
		fmt.Fprintf(os.Stderr, "Error: server exited with non-zero code\n")
		return false, testMetrics, startTime, time.Now()
	}

	// Print basic verification
	fmt.Println()
	fmt.Println("Verification:")
	fmt.Println("  ✓ Client-generator received SIGINT and exited gracefully")
	fmt.Println("  ✓ Client received SIGINT and exited gracefully")
	fmt.Println("  ✓ Server received SIGINT and shutdown gracefully")
	fmt.Println("  ✓ All processes exited with code 0")
	fmt.Println("  ✓ All processes exited within expected timeframes")

	endTime = time.Now()
	return true, testMetrics, startTime, endTime
}

// getBaseDir returns the base directory of the gosrt project
func getBaseDir() string {
	_, filename, _, _ := runtime.Caller(0)
	dir := filepath.Dir(filename)
	return filepath.Join(dir, "..", "..")
}

// ensureBinaries ensures that all required binaries exist, building them if necessary.
// Note: If binaries are stale after source changes, run 'make clean' to force rebuild.
func ensureBinaries(baseDir string, serverBin, clientGenBin, clientBin string) error {
	binaries := []struct {
		path string
		pkg  string
	}{
		{serverBin, "./contrib/server"},
		{clientGenBin, "./contrib/client-generator"},
		{clientBin, "./contrib/client"},
	}

	for _, bin := range binaries {
		if _, err := os.Stat(bin.path); os.IsNotExist(err) {
			fmt.Printf("Building %s...\n", bin.path)
			cmd := exec.Command("go", "build", "-o", bin.path, bin.pkg)
			cmd.Dir = baseDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to build %s: %w", bin.path, err)
			}
		}
	}

	return nil
}

// ============================================================================
// MATRIX-GENERATED TEST COMMANDS
// ============================================================================

// listMatrixTests lists all matrix-generated tests with their tier and description.
func listMatrixTests() {
	cfg := DefaultParallelMatrixConfig()
	tests := GenerateParallelTests(cfg)

	tierNames := map[TestTier]string{
		TierCore:    "Core",
		TierDaily:   "Daily",
		TierNightly: "Nightly",
	}

	fmt.Printf("Matrix-Generated Parallel Tests (%d total):\n\n", len(tests))
	for i, t := range tests {
		fmt.Printf("  %3d. [%-8s] %-55s %s\n", i+1, tierNames[t.Tier], t.Name, t.Duration)
	}
	fmt.Println()
	fmt.Println("Use 'matrix-summary' to see counts by tier and category.")
	fmt.Println("Use 'matrix-run-tier1' to run Tier 1 (Core) tests only.")
}

// showMatrixSummary shows a summary of matrix tests by tier and category.
func showMatrixSummary() {
	cfg := DefaultParallelMatrixConfig()
	tests := GenerateParallelTests(cfg)

	PrintTestSummary(tests)
}

// listMatrixTestsByTier lists tests up to and including the specified tier.
func listMatrixTestsByTier(maxTier TestTier) {
	cfg := DefaultParallelMatrixConfig()
	allTests := GenerateParallelTests(cfg)
	tests := FilterTestsByTier(allTests, maxTier)

	tierNames := map[TestTier]string{
		TierCore:    "Tier 1 (Core)",
		TierDaily:   "Tier 1+2 (Daily)",
		TierNightly: "All Tiers (Nightly)",
	}

	fmt.Printf("%s Matrix Tests (%d tests):\n\n", tierNames[maxTier], len(tests))
	for i, t := range tests {
		fmt.Printf("  %3d. %-55s %s\n", i+1, t.Name, t.Duration)
	}

	// Calculate total estimated time
	var totalDuration time.Duration
	for _, t := range tests {
		totalDuration += t.Duration
	}
	fmt.Printf("\nEstimated total runtime: %s\n", totalDuration.Round(time.Minute))
}

// runMatrixTestsByTier runs all tests up to and including the specified tier.
func runMatrixTestsByTier(maxTier TestTier) {
	cfg := DefaultParallelMatrixConfig()
	allTests := GenerateParallelTests(cfg)
	tests := FilterTestsByTier(allTests, maxTier)

	tierNames := map[TestTier]string{
		TierCore:    "Tier 1 (Core)",
		TierDaily:   "Tier 1+2 (Daily)",
		TierNightly: "All Tiers (Nightly)",
	}

	fmt.Printf("╔═══════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  Matrix Test Runner: %-20s                           ║\n", tierNames[maxTier])
	fmt.Printf("╚═══════════════════════════════════════════════════════════════════════╝\n\n")

	fmt.Printf("Running %d parallel tests...\n\n", len(tests))

	// Calculate total estimated time
	var totalDuration time.Duration
	for _, t := range tests {
		totalDuration += t.Duration
	}
	fmt.Printf("Estimated total runtime: %s\n\n", totalDuration.Round(time.Minute))

	passed := 0
	failed := 0
	var failedTests []string

	startTime := time.Now()

	for i, t := range tests {
		fmt.Printf("━━━ Test %d/%d: %s ━━━\n", i+1, len(tests), t.Name)
		fmt.Printf("    %s\n", t.Description)
		fmt.Printf("    Duration: %s\n\n", t.Duration)

		// Run the parallel test using the generated config
		printParallelTestHeader(t.Config)
		result := runParallelModeTest(t.Config)

		if result.Passed {
			// Print detailed comparison
			if result.BaselineMetrics != nil && result.HighPerfMetrics != nil {
				comparisons := CompareParallelPipelines(result.BaselineMetrics, result.HighPerfMetrics)
				PrintDetailedComparison(comparisons)
			}
			passed++
			fmt.Printf("✓ PASSED: %s\n\n", t.Name)
		} else {
			failed++
			failedTests = append(failedTests, t.Name)
			fmt.Printf("✗ FAILED: %s\n\n", t.Name)
		}

		// Wait between tests for cleanup
		if i < len(tests)-1 {
			time.Sleep(5 * time.Second)
		}
	}

	elapsed := time.Since(startTime)

	// Print summary
	fmt.Printf("\n╔═══════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  MATRIX TEST SUMMARY                                                  ║\n")
	fmt.Printf("╠═══════════════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Tests Run:  %-5d                                                    ║\n", len(tests))
	fmt.Printf("║  Passed:     %-5d                                                    ║\n", passed)
	fmt.Printf("║  Failed:     %-5d                                                    ║\n", failed)
	fmt.Printf("║  Duration:   %-10s                                               ║\n", elapsed.Round(time.Second))
	fmt.Printf("╚═══════════════════════════════════════════════════════════════════════╝\n")

	if failed > 0 {
		fmt.Println("\nFailed tests:")
		for _, name := range failedTests {
			fmt.Printf("  - %s\n", name)
		}
		os.Exit(1)
	}
}

// ============================================================================
// CLEAN NETWORK MATRIX TEST COMMANDS
// ============================================================================

// listCleanMatrixTests lists all clean network matrix tests.
func listCleanMatrixTests() {
	tests := GenerateCleanNetworkTests()
	PrintCleanTestMatrix(tests)
	fmt.Println()
	fmt.Println("Use 'clean-matrix-summary' to see counts by tier.")
	fmt.Println("Use 'clean-matrix-run-tier1' to run Tier 1 tests (no root needed).")
}

// showCleanMatrixSummary shows a summary of clean network tests.
func showCleanMatrixSummary() {
	tests := GenerateCleanNetworkTests()
	PrintCleanTestSummary(tests)
}

// listCleanMatrixTestsByTier lists clean tests up to and including the specified tier.
func listCleanMatrixTestsByTier(maxTier TestTier) {
	allTests := GenerateCleanNetworkTests()
	tests := FilterCleanTestsByTier(allTests, maxTier)

	tierNames := map[TestTier]string{
		TierCore:    "Tier 1 (Core)",
		TierDaily:   "Tier 1+2 (Daily)",
		TierNightly: "All Tiers (Nightly)",
	}

	fmt.Printf("%s Clean Network Tests (%d tests):\n\n", tierNames[maxTier], len(tests))
	for i, t := range tests {
		fmt.Printf("  %3d. %-45s %s\n", i+1, t.Name, t.Duration)
	}

	// Calculate total estimated time
	var totalDuration time.Duration
	for _, t := range tests {
		totalDuration += t.Duration
	}
	fmt.Printf("\nEstimated total runtime: %s\n", totalDuration.Round(time.Minute))
}

// runCleanMatrixTestsByTier runs all clean network tests up to and including the specified tier.
func runCleanMatrixTestsByTier(maxTier TestTier) {
	allTests := GenerateCleanNetworkTests()
	tests := FilterCleanTestsByTier(allTests, maxTier)

	tierNames := map[TestTier]string{
		TierCore:    "Tier 1 (Core)",
		TierDaily:   "Tier 1+2 (Daily)",
		TierNightly: "All Tiers (Nightly)",
	}

	fmt.Printf("╔═══════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  Clean Network Matrix Test Runner: %-20s             ║\n", tierNames[maxTier])
	fmt.Printf("╚═══════════════════════════════════════════════════════════════════════╝\n\n")

	fmt.Printf("Running %d clean network tests...\n\n", len(tests))

	// Calculate total estimated time
	var totalDuration time.Duration
	for _, t := range tests {
		totalDuration += t.Duration
	}
	fmt.Printf("Estimated total runtime: %s\n\n", totalDuration.Round(time.Minute))

	passed := 0
	failed := 0
	var failedTests []string

	startTime := time.Now()

	for i, t := range tests {
		fmt.Printf("━━━ Test %d/%d: %s ━━━\n", i+1, len(tests), t.Name)
		fmt.Printf("    %s\n", t.Description)
		fmt.Printf("    Duration: %s\n\n", t.Duration)

		// Run the clean network test
		err := runTestWithConfig(t.Config)

		if err == nil {
			passed++
			fmt.Printf("✓ PASSED: %s\n\n", t.Name)
		} else {
			failed++
			failedTests = append(failedTests, t.Name)
			fmt.Printf("✗ FAILED: %s - %v\n\n", t.Name, err)
		}

		// Brief pause between tests
		if i < len(tests)-1 {
			time.Sleep(1 * time.Second)
		}
	}

	elapsed := time.Since(startTime)

	// Print summary
	fmt.Printf("\n╔═══════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  CLEAN NETWORK TEST SUMMARY                                           ║\n")
	fmt.Printf("╠═══════════════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Tests Run:  %-5d                                                    ║\n", len(tests))
	fmt.Printf("║  Passed:     %-5d                                                    ║\n", passed)
	fmt.Printf("║  Failed:     %-5d                                                    ║\n", failed)
	fmt.Printf("║  Duration:   %-10s                                               ║\n", elapsed.Round(time.Second))
	fmt.Printf("╚═══════════════════════════════════════════════════════════════════════╝\n")

	if failed > 0 {
		fmt.Println("\nFailed tests:")
		for _, name := range failedTests {
			fmt.Printf("  - %s\n", name)
		}
		os.Exit(1)
	}
}
