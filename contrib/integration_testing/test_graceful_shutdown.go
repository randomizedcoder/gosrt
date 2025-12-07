package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
		fmt.Println("Available test configurations:")
		fmt.Println()
		for _, c := range TestConfigs {
			fmt.Printf("  %-35s %s\n", c.Name, c.Description)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown test: %s\n", testName)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <test-name> [options]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nAvailable tests:\n")
	fmt.Fprintf(os.Stderr, "  graceful-shutdown-sigint              Run graceful shutdown test with default config\n")
	fmt.Fprintf(os.Stderr, "  graceful-shutdown-sigint-all          Run graceful shutdown test with all configurations\n")
	fmt.Fprintf(os.Stderr, "  graceful-shutdown-sigint-config NAME  Run graceful shutdown test with specific config\n")
	fmt.Fprintf(os.Stderr, "  list-configs                          List all available configurations\n")
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

// runTestWithConfig runs the graceful shutdown test with the given configuration
func runTestWithConfig(config TestConfig) error {
	testPassed, testMetrics, startTime, endTime := runTestWithMetrics(config)
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

	// Collect pre-shutdown metrics
	if config.MetricsEnabled {
		fmt.Println("\nCollecting pre-shutdown metrics...")
		testMetrics.CollectAllMetrics("pre-shutdown")
	}

	// Shutdown order: Client → Client-Generator → Server
	fmt.Println("\nInitiating shutdown sequence...")

	// Step 1: Send SIGINT to Client (subscriber)
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

	// Step 2: Send SIGINT to Client-Generator (publisher)
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

	time.Sleep(500 * time.Millisecond)

	// Step 3: Send SIGINT to Server (last)
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
	fmt.Println("  ✓ Client received SIGINT and exited gracefully")
	fmt.Println("  ✓ Client-generator received SIGINT and exited gracefully")
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

// ensureBinaries ensures that all required binaries exist, building them if necessary
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
