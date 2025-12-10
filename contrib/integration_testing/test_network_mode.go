package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// runNetworkModeTest runs a test with network namespace isolation and impairment.
// This is used when config.Mode == TestModeNetwork.
func runNetworkModeTest(config TestConfig) (passed bool, metrics *TestMetrics, startTime, endTime time.Time) {
	startTime = time.Now()

	// Verify we're running as root (required for network namespaces)
	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "Error: Network mode tests require root privileges\n")
		fmt.Fprintf(os.Stderr, "Run with: sudo %s ...\n", os.Args[0])
		return false, nil, startTime, time.Now()
	}

	// Create network controller
	nc, err := NewNetworkController(NetworkControllerConfig{
		TestID: fmt.Sprintf("test_%d", os.Getpid()),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating network controller: %v\n", err)
		return false, nil, startTime, time.Now()
	}

	// Create context for test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup network namespaces
	fmt.Println("Setting up network namespaces...")
	if err := nc.Setup(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up network: %v\n", err)
		return false, nil, startTime, time.Now()
	}

	// Ensure cleanup happens even on failure
	defer func() {
		fmt.Println("\nCleaning up network namespaces...")
		if err := nc.Cleanup(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cleanup failed: %v\n", err)
		}
	}()

	// Print network status
	fmt.Println("\nNetwork Topology:")
	fmt.Printf("  Publisher:  %s (%s)\n", nc.NamespacePublisher, nc.IPPublisher)
	fmt.Printf("  Subscriber: %s (%s)\n", nc.NamespaceSubscriber, nc.IPSubscriber)
	fmt.Printf("  Server:     %s (%s)\n", nc.NamespaceServer, nc.IPServer)
	fmt.Println()

	// Apply initial latency profile if configured
	if config.Impairment.LatencyProfile != "" {
		profile := getLatencyProfileIndex(config.Impairment.LatencyProfile)
		if profile >= 0 {
			fmt.Printf("Setting latency profile: %s (profile %d)\n", config.Impairment.LatencyProfile, profile)
			if err := nc.SetLatencyProfile(ctx, profile); err != nil {
				fmt.Fprintf(os.Stderr, "Error setting latency: %v\n", err)
				return false, nil, startTime, time.Now()
			}
		}
	}

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

	// Build CLI flags - use namespace IPs instead of config network IPs
	serverFlags := buildNetworkModeServerFlags(config, nc)
	clientGenFlags := buildNetworkModeClientGenFlags(config, nc)
	clientFlags := buildNetworkModeClientFlags(config, nc)

	// Print CLI flags for debugging
	fmt.Println("CLI Flags:")
	fmt.Printf("  Server: %s\n", strings.Join(serverFlags, " "))
	fmt.Printf("  Client-Generator: %s\n", strings.Join(clientGenFlags, " "))
	fmt.Printf("  Client: %s\n", strings.Join(clientFlags, " "))
	fmt.Println()

	// Start server in server namespace
	fmt.Println("Starting server in namespace...")
	serverCmd, err := startProcessInNamespace(ctx, nc.NamespaceServer, serverBin, serverFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
		return false, nil, startTime, time.Now()
	}
	defer killProcess(serverCmd)

	// Wait for server to start
	time.Sleep(500 * time.Millisecond)

	// Start client-generator in publisher namespace
	fmt.Println("Starting client-generator in namespace...")
	clientGenCmd, err := startProcessInNamespace(ctx, nc.NamespacePublisher, clientGenBin, clientGenFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting client-generator: %v\n", err)
		return false, nil, startTime, time.Now()
	}
	defer killProcess(clientGenCmd)

	// Wait for publisher to connect
	time.Sleep(500 * time.Millisecond)

	// Start client in subscriber namespace
	fmt.Println("Starting client in namespace...")
	clientCmd, err := startProcessInNamespace(ctx, nc.NamespaceSubscriber, clientBin, clientFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting client: %v\n", err)
		return false, nil, startTime, time.Now()
	}
	defer killProcess(clientCmd)

	// Wait for connections to establish
	fmt.Printf("Waiting %v for connections to establish...\n", config.ConnectionWait)
	time.Sleep(config.ConnectionWait)

	// Apply impairment settings
	if config.Impairment.LossRate > 0 {
		lossPercent := int(config.Impairment.LossRate * 100)
		fmt.Printf("Applying %d%% packet loss...\n", lossPercent)
		if err := nc.SetLoss(ctx, lossPercent); err != nil {
			fmt.Fprintf(os.Stderr, "Error setting loss: %v\n", err)
			return false, nil, startTime, time.Now()
		}
	}

	// Start impairment pattern if configured
	if config.Impairment.Pattern != "" && config.Impairment.Pattern != "clean" {
		pattern := getImpairmentPattern(config.Impairment.Pattern)
		if pattern != nil {
			fmt.Printf("Starting impairment pattern: %s\n", config.Impairment.Pattern)
			if err := nc.StartPattern(ctx, *pattern); err != nil {
				fmt.Fprintf(os.Stderr, "Error starting pattern: %v\n", err)
				return false, nil, startTime, time.Now()
			}
			defer func() {
				_ = nc.StopPattern(ctx)
			}()
		}
	}

	// Initialize metrics collection if enabled
	// For network mode, we use UDS sockets for metrics collection
	var testMetrics *TestMetrics
	if config.MetricsEnabled {
		// Build UDS endpoints for each component
		serverEndpoint := MetricsEndpoint{UDSPath: fmt.Sprintf("/tmp/srt_server_%s.sock", nc.TestID)}
		clientGenEndpoint := MetricsEndpoint{UDSPath: fmt.Sprintf("/tmp/srt_clientgen_%s.sock", nc.TestID)}
		clientEndpoint := MetricsEndpoint{UDSPath: fmt.Sprintf("/tmp/srt_client_%s.sock", nc.TestID)}

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

		snapshotCount := 1 // We already collected "startup" as index 0
	collectLoop:
		for {
			select {
			case <-collectTicker.C:
				fmt.Println("\nCollecting mid-test metrics...")
				testMetrics.CollectAllMetrics("mid-test")
				snapshotCount++
				// Print verbose delta if enabled
				if config.VerboseMetrics && snapshotCount >= 2 {
					testMetrics.PrintVerboseMetricsDelta(snapshotCount-2, snapshotCount-1)
				}
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
	if err := signalProcess(clientGenCmd, syscall.SIGUSR1); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGUSR1 to client-generator: %v\n", err)
		// Non-fatal: continue with shutdown even if pause fails
	}

	// Step 2: Wait for metrics to stabilize (ACKs, NAKs stop incrementing)
	if testMetrics != nil {
		fmt.Println("Waiting for metrics to stabilize...")
		stabCtx, stabCancel := context.WithTimeout(ctx, 10*time.Second)
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
	if testMetrics != nil {
		fmt.Println("Collecting pre-shutdown metrics...")
		testMetrics.CollectAllMetrics("pre-shutdown")
	}

	// =================================================================
	// SHUTDOWN PHASE: Gracefully stop all processes
	// =================================================================
	fmt.Println("\n--- Shutdown Phase ---")

	// Stop any impairment pattern before shutdown
	if config.Impairment.Pattern != "" && config.Impairment.Pattern != "clean" {
		fmt.Println("Stopping impairment pattern...")
		_ = nc.StopPattern(ctx)
	}

	// Clear any loss before shutdown
	if config.Impairment.LossRate > 0 {
		fmt.Println("Clearing packet loss...")
		_ = nc.SetLoss(ctx, 0)
	}

	// Initiate graceful shutdown sequence
	fmt.Println("\nInitiating shutdown sequence...")

	// Send SIGINT to client (subscriber) first
	fmt.Println("Sending SIGINT to client (subscriber)...")
	if err := signalProcess(clientCmd, syscall.SIGINT); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGINT to client: %v\n", err)
	}

	// Wait for client to exit
	clientExited := waitForProcessExit(clientCmd, 5*time.Second)
	if clientExited {
		fmt.Println("✓ Client exited gracefully")
	} else {
		fmt.Fprintf(os.Stderr, "Error: client did not exit within 5 seconds\n")
		killProcess(clientCmd)
	}

	// Send SIGINT to client-generator
	fmt.Println("Sending SIGINT to client-generator (publisher)...")
	if err := signalProcess(clientGenCmd, syscall.SIGINT); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGINT to client-generator: %v\n", err)
	}

	// Wait for client-generator to exit
	clientGenExited := waitForProcessExit(clientGenCmd, 8*time.Second)
	if clientGenExited {
		fmt.Println("✓ Client-generator exited gracefully")
	} else {
		fmt.Fprintf(os.Stderr, "Error: client-generator did not exit within 8 seconds\n")
		killProcess(clientGenCmd)
	}

	// Send SIGINT to server
	fmt.Println("Sending SIGINT to server...")
	if err := signalProcess(serverCmd, syscall.SIGINT); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGINT to server: %v\n", err)
	}

	// Wait for server to exit
	serverExited := waitForProcessExit(serverCmd, 10*time.Second)
	if serverExited {
		fmt.Println("✓ Server shutdown completed")
	} else {
		fmt.Fprintf(os.Stderr, "Error: server did not exit within 10 seconds\n")
		killProcess(serverCmd)
	}

	// Collect final metrics
	if testMetrics != nil {
		fmt.Println("\nCollecting final metrics...")
		testMetrics.CollectAllMetrics("final")
	}

	// Verify all processes exited
	fmt.Println("\nVerification:")
	allPassed := true

	if clientExited {
		fmt.Println("  ✓ Client received SIGINT and exited gracefully")
	} else {
		fmt.Println("  ✗ Client did not exit gracefully")
		allPassed = false
	}

	if clientGenExited {
		fmt.Println("  ✓ Client-generator received SIGINT and exited gracefully")
	} else {
		fmt.Println("  ✗ Client-generator did not exit gracefully")
		allPassed = false
	}

	if serverExited {
		fmt.Println("  ✓ Server received SIGINT and shutdown gracefully")
	} else {
		fmt.Println("  ✗ Server did not shutdown gracefully")
		allPassed = false
	}

	endTime = time.Now()
	return allPassed, testMetrics, startTime, endTime
}

// startProcessInNamespace starts a process in a network namespace
func startProcessInNamespace(ctx context.Context, namespace string, binPath string, args []string) (*exec.Cmd, error) {
	cmdArgs := append([]string{"netns", "exec", namespace, binPath}, args...)
	cmd := exec.CommandContext(ctx, "ip", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start process in namespace %s: %w", namespace, err)
	}

	return cmd, nil
}

// signalProcess sends a signal to a process
func signalProcess(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process not running")
	}
	return cmd.Process.Signal(sig)
}

// waitForProcessExit waits for a process to exit within a timeout
func waitForProcessExit(cmd *exec.Cmd, timeout time.Duration) bool {
	if cmd == nil || cmd.Process == nil {
		return true
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// killProcess kills a process if it's running
func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

// buildNetworkModeServerFlags builds CLI flags for server in network mode
func buildNetworkModeServerFlags(config TestConfig, nc *NetworkController) []string {
	flags := []string{
		"-addr", fmt.Sprintf("%s:6000", nc.IPServer),
	}

	// Add UDS metrics endpoint (accessible from host for collection)
	flags = append(flags, "-promuds", fmt.Sprintf("/tmp/srt_server_%s.sock", nc.TestID))

	// Apply shared SRT config
	if config.SharedSRT != nil {
		flags = append(flags, config.SharedSRT.ToCliFlags()...)
	}

	// Apply component-specific config
	flags = append(flags, config.Server.ToCliFlags()...)

	return flags
}

// buildNetworkModeClientGenFlags builds CLI flags for client-generator in network mode
func buildNetworkModeClientGenFlags(config TestConfig, nc *NetworkController) []string {
	publisherURL := fmt.Sprintf("srt://%s:6000/test-stream", nc.IPServer)

	flags := []string{
		"-to", publisherURL,
		"-bitrate", fmt.Sprintf("%d", config.Bitrate),
	}

	// Add UDS metrics endpoint
	flags = append(flags, "-promuds", fmt.Sprintf("/tmp/srt_clientgen_%s.sock", nc.TestID))

	// Apply shared SRT config
	if config.SharedSRT != nil {
		flags = append(flags, config.SharedSRT.ToCliFlags()...)
	}

	// Apply component-specific config
	flags = append(flags, config.ClientGenerator.ToCliFlags()...)

	return flags
}

// buildNetworkModeClientFlags builds CLI flags for client in network mode
func buildNetworkModeClientFlags(config TestConfig, nc *NetworkController) []string {
	subscriberURL := fmt.Sprintf("srt://%s:6000?streamid=subscribe:/test-stream&mode=caller", nc.IPServer)

	flags := []string{
		"-from", subscriberURL,
		"-to", "null",
	}

	// Add UDS metrics endpoint
	flags = append(flags, "-promuds", fmt.Sprintf("/tmp/srt_client_%s.sock", nc.TestID))

	// Add io_uring output if configured
	if config.Client.IoUringOutput {
		flags = append(flags, "-iouringoutput")
	}

	// Apply shared SRT config
	if config.SharedSRT != nil {
		flags = append(flags, config.SharedSRT.ToCliFlags()...)
	}

	// Apply component-specific config
	flags = append(flags, config.Client.ToCliFlags()...)

	return flags
}

// getLatencyProfileIndex converts a latency profile name to an index
func getLatencyProfileIndex(profile string) int {
	switch strings.ToLower(profile) {
	case "none", "local", "":
		return 0
	case "regional", "tier1", "low":
		return 1
	case "continental", "tier2", "medium":
		return 2
	case "intercontinental", "tier3", "high":
		return 3
	case "geo-satellite", "geo", "satellite":
		return 4
	default:
		return -1
	}
}

// getImpairmentPattern returns a predefined impairment pattern by name
func getImpairmentPattern(name string) *LossPattern {
	switch strings.ToLower(name) {
	case "starlink":
		return &PatternStarlink
	case "high-loss", "high_loss", "highloss", "burst":
		return &PatternHighLossBurst
	default:
		return nil
	}
}
