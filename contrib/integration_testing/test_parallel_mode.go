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

// ParallelProcessSet holds the 3 processes for one pipeline
type ParallelProcessSet struct {
	Label     string // "baseline" or "highperf"
	Server    *exec.Cmd
	ClientGen *exec.Cmd
	Client    *exec.Cmd
}

// ParallelTestResult holds the results from both pipelines
type ParallelTestResult struct {
	BaselineMetrics *TestMetrics
	HighPerfMetrics *TestMetrics
	StartTime       time.Time
	EndTime         time.Time
	Passed          bool
}

// runParallelModeTest runs a parallel comparison test with two pipelines.
// Both pipelines run simultaneously under identical network conditions.
// Supports profiling via PROFILES environment variable (e.g., PROFILES=cpu,mutex)
func runParallelModeTest(config ParallelTestConfig) ParallelTestResult {
	result := ParallelTestResult{
		StartTime: time.Now(),
		Passed:    false,
	}

	// Verify we're running as root (required for network namespaces)
	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "Error: Parallel tests require root privileges\n")
		fmt.Fprintf(os.Stderr, "Run with: sudo %s ...\n", os.Args[0])
		result.EndTime = time.Now()
		return result
	}

	// Check for profiling mode
	var profileConfig *ProfileConfig
	if ProfilingEnabled() {
		var err error
		profileConfig, err = NewProfileConfig(config.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating profile config: %v\n", err)
			result.EndTime = time.Now()
			return result
		}
		if profileConfig != nil {
			profileConfig.PrintProfilingInfo()
		}
	}

	// Create network controller
	nc, err := NewNetworkController(NetworkControllerConfig{
		TestID:  fmt.Sprintf("test_%d", os.Getpid()),
		Verbose: config.VerboseNetwork,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating network controller: %v\n", err)
		result.EndTime = time.Now()
		return result
	}

	// Create context for test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup network namespaces
	fmt.Println("Setting up network namespaces...")
	if err := nc.Setup(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up network: %v\n", err)
		result.EndTime = time.Now()
		return result
	}

	// Ensure cleanup happens even on failure
	defer func() {
		fmt.Println("\nCleaning up network namespaces...")
		if err := nc.Cleanup(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cleanup failed: %v\n", err)
		}
	}()

	// Setup parallel IPs (.3 addresses for HighPerf pipeline)
	fmt.Println("Setting up parallel IPs for dual-pipeline test...")
	if err := nc.SetupParallelIPs(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up parallel IPs: %v\n", err)
		result.EndTime = time.Now()
		return result
	}

	// Start packet captures if configured via environment variables
	tcpdumpConfig := GetTcpdumpConfigFromEnv()
	if tcpdumpConfig.HasAnyCapture() {
		fmt.Println("Starting packet captures (TCPDUMP_* enabled)...")
		if err := nc.StartTcpdumpFromConfig(ctx, tcpdumpConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: tcpdump start failed: %v\n", err)
		} else {
			if tcpdumpConfig.PublisherFile != "" {
				fmt.Printf("  Publisher (CG): %s\n", tcpdumpConfig.PublisherFile)
			}
			if tcpdumpConfig.ServerFile != "" {
				fmt.Printf("  Server: %s\n", tcpdumpConfig.ServerFile)
			}
			if tcpdumpConfig.SubscriberFile != "" {
				fmt.Printf("  Subscriber (Client): %s\n", tcpdumpConfig.SubscriberFile)
			}
		}
		fmt.Println()
	}

	// Print network status
	fmt.Println("\nNetwork Topology (Parallel Mode):")
	fmt.Printf("  Baseline Pipeline:\n")
	fmt.Printf("    Publisher:  %s → Server: %s:%d → Subscriber: %s\n",
		config.Baseline.PublisherIP, config.Baseline.ServerIP, config.Baseline.ServerPort, config.Baseline.SubscriberIP)
	fmt.Printf("    Stream ID:  %s\n", config.Baseline.StreamID)
	fmt.Printf("  HighPerf Pipeline:\n")
	fmt.Printf("    Publisher:  %s → Server: %s:%d → Subscriber: %s\n",
		config.HighPerf.PublisherIP, config.HighPerf.ServerIP, config.HighPerf.ServerPort, config.HighPerf.SubscriberIP)
	fmt.Printf("    Stream ID:  %s\n", config.HighPerf.StreamID)
	fmt.Println()

	// Apply initial latency profile if configured
	if config.Impairment.LatencyProfile != "" {
		profile := getLatencyProfileIndex(config.Impairment.LatencyProfile)
		if profile >= 0 {
			fmt.Printf("Setting latency profile: %s (profile %d)\n", config.Impairment.LatencyProfile, profile)
			if err := nc.SetLatencyProfile(ctx, profile); err != nil {
				fmt.Fprintf(os.Stderr, "Error setting latency: %v\n", err)
				result.EndTime = time.Now()
				return result
			}
		}
	}

	// Get the base directory and build paths to binaries
	// Use debug binaries when profiling is enabled (they have debug symbols for better profile output)
	baseDir := getBaseDir()
	var serverBin, clientGenBin, clientBin string
	if profileConfig != nil {
		serverBin = filepath.Join(baseDir, "contrib", "server", "server-debug")
		clientGenBin = filepath.Join(baseDir, "contrib", "client-generator", "client-generator-debug")
		clientBin = filepath.Join(baseDir, "contrib", "client", "client-debug")
	} else {
		serverBin = filepath.Join(baseDir, "contrib", "server", "server")
		clientGenBin = filepath.Join(baseDir, "contrib", "client-generator", "client-generator")
		clientBin = filepath.Join(baseDir, "contrib", "client", "client")
	}

	// Check if binaries exist
	if err := ensureBinaries(baseDir, serverBin, clientGenBin, clientBin); err != nil {
		fmt.Fprintf(os.Stderr, "Error building binaries: %v\n", err)
		result.EndTime = time.Now()
		return result
	}

	// Build CLI flags for both pipelines
	baselineServerFlags := config.GetBaselineServerFlags(nc.TestID)
	baselineClientGenFlags := config.GetBaselineClientGeneratorFlags(nc.TestID)
	baselineClientFlags := config.GetBaselineClientFlags(nc.TestID)

	highperfServerFlags := config.GetHighPerfServerFlags(nc.TestID)
	highperfClientGenFlags := config.GetHighPerfClientGeneratorFlags(nc.TestID)
	highperfClientFlags := config.GetHighPerfClientFlags(nc.TestID)

	// Add profiling flags if enabled
	if profileConfig != nil && len(profileConfig.Profiles) > 0 {
		profileType := profileConfig.Profiles[0]
		fmt.Printf("Enabling %s profiling for all 6 components\n\n", profileType)

		// Baseline pipeline profiling
		if args, err := profileConfig.GetProfileArgs("baseline_server", profileType); err == nil && args != nil {
			baselineServerFlags = append(baselineServerFlags, args...)
		}
		if args, err := profileConfig.GetProfileArgs("baseline_cg", profileType); err == nil && args != nil {
			baselineClientGenFlags = append(baselineClientGenFlags, args...)
		}
		if args, err := profileConfig.GetProfileArgs("baseline_client", profileType); err == nil && args != nil {
			baselineClientFlags = append(baselineClientFlags, args...)
		}

		// HighPerf pipeline profiling
		if args, err := profileConfig.GetProfileArgs("highperf_server", profileType); err == nil && args != nil {
			highperfServerFlags = append(highperfServerFlags, args...)
		}
		if args, err := profileConfig.GetProfileArgs("highperf_cg", profileType); err == nil && args != nil {
			highperfClientGenFlags = append(highperfClientGenFlags, args...)
		}
		if args, err := profileConfig.GetProfileArgs("highperf_client", profileType); err == nil && args != nil {
			highperfClientFlags = append(highperfClientFlags, args...)
		}
	}

	// Print CLI flags for debugging
	fmt.Println("CLI Flags (Baseline):")
	fmt.Printf("  Server: %s\n", strings.Join(baselineServerFlags, " "))
	fmt.Printf("  Client-Generator: %s\n", strings.Join(baselineClientGenFlags, " "))
	fmt.Printf("  Client: %s\n", strings.Join(baselineClientFlags, " "))
	fmt.Println()
	fmt.Println("CLI Flags (HighPerf):")
	fmt.Printf("  Server: %s\n", strings.Join(highperfServerFlags, " "))
	fmt.Printf("  Client-Generator: %s\n", strings.Join(highperfClientGenFlags, " "))
	fmt.Printf("  Client: %s\n", strings.Join(highperfClientFlags, " "))
	fmt.Println()

	// Initialize process sets
	baseline := &ParallelProcessSet{Label: "baseline"}
	highperf := &ParallelProcessSet{Label: "highperf"}

	// Start all servers first
	fmt.Println("Starting servers in namespace...")

	baseline.Server, err = startProcessInNamespace(ctx, nc.NamespaceServer, serverBin, baselineServerFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting baseline server: %v\n", err)
		result.EndTime = time.Now()
		return result
	}
	defer killProcess(baseline.Server)

	highperf.Server, err = startProcessInNamespace(ctx, nc.NamespaceServer, serverBin, highperfServerFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting highperf server: %v\n", err)
		result.EndTime = time.Now()
		return result
	}
	defer killProcess(highperf.Server)

	// Wait for servers to start
	time.Sleep(500 * time.Millisecond)

	// Start all client-generators
	fmt.Println("Starting client-generators in namespace...")

	baseline.ClientGen, err = startProcessInNamespace(ctx, nc.NamespacePublisher, clientGenBin, baselineClientGenFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting baseline client-generator: %v\n", err)
		result.EndTime = time.Now()
		return result
	}
	defer killProcess(baseline.ClientGen)

	highperf.ClientGen, err = startProcessInNamespace(ctx, nc.NamespacePublisher, clientGenBin, highperfClientGenFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting highperf client-generator: %v\n", err)
		result.EndTime = time.Now()
		return result
	}
	defer killProcess(highperf.ClientGen)

	// Wait for publishers to connect
	time.Sleep(500 * time.Millisecond)

	// Start all clients
	fmt.Println("Starting clients in namespace...")

	baseline.Client, err = startProcessInNamespace(ctx, nc.NamespaceSubscriber, clientBin, baselineClientFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting baseline client: %v\n", err)
		result.EndTime = time.Now()
		return result
	}
	defer killProcess(baseline.Client)

	highperf.Client, err = startProcessInNamespace(ctx, nc.NamespaceSubscriber, clientBin, highperfClientFlags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting highperf client: %v\n", err)
		result.EndTime = time.Now()
		return result
	}
	defer killProcess(highperf.Client)

	// Wait for all connections to establish
	fmt.Printf("Waiting %v for all connections to establish...\n", config.ConnectionWait)
	time.Sleep(config.ConnectionWait)

	// Apply impairment settings
	if config.Impairment.LossRate > 0 {
		lossPercent := int(config.Impairment.LossRate * 100)
		fmt.Printf("Applying %d%% packet loss (both pipelines)...\n", lossPercent)
		// Use parallel loss function to affect all 6 IPs
		if err := nc.SetLossParallel(ctx, lossPercent); err != nil {
			fmt.Fprintf(os.Stderr, "Error setting loss: %v\n", err)
			result.EndTime = time.Now()
			return result
		}
	}

	// Start impairment pattern if configured
	if config.Impairment.Pattern != "" && config.Impairment.Pattern != "clean" {
		pattern := getImpairmentPattern(config.Impairment.Pattern)
		if pattern != nil {
			fmt.Printf("Starting impairment pattern: %s (both pipelines)\n", config.Impairment.Pattern)
			// Use parallel pattern to affect all 6 IPs
			if err := nc.StartPatternParallel(ctx, *pattern); err != nil {
				fmt.Fprintf(os.Stderr, "Error starting pattern: %v\n", err)
				result.EndTime = time.Now()
				return result
			}
			defer func() {
				_ = nc.StopPatternParallel(ctx)
			}()
		}
	}

	// Initialize metrics collection for both pipelines
	udsPaths := config.GetAllUDSPaths(nc.TestID)

	baselineServerEndpoint := MetricsEndpoint{UDSPath: udsPaths["server_baseline"]}
	baselineClientGenEndpoint := MetricsEndpoint{UDSPath: udsPaths["clientgen_baseline"]}
	baselineClientEndpoint := MetricsEndpoint{UDSPath: udsPaths["client_baseline"]}
	result.BaselineMetrics = NewTestMetrics(baselineServerEndpoint, baselineClientGenEndpoint, baselineClientEndpoint)

	highperfServerEndpoint := MetricsEndpoint{UDSPath: udsPaths["server_highperf"]}
	highperfClientGenEndpoint := MetricsEndpoint{UDSPath: udsPaths["clientgen_highperf"]}
	highperfClientEndpoint := MetricsEndpoint{UDSPath: udsPaths["client_highperf"]}
	result.HighPerfMetrics = NewTestMetrics(highperfServerEndpoint, highperfClientGenEndpoint, highperfClientEndpoint)

	// Collect initial metrics
	fmt.Println("\nCollecting initial metrics (both pipelines)...")
	result.BaselineMetrics.CollectAllMetrics("startup")
	result.HighPerfMetrics.CollectAllMetrics("startup")

	// Run for test duration
	fmt.Printf("All 6 processes started. Running for %v...\n", config.TestDuration)

	// Periodically collect metrics during test
	if config.CollectInterval > 0 {
		collectTicker := time.NewTicker(config.CollectInterval)
		testTimer := time.NewTimer(config.TestDuration)

		snapshotCount := 1
	collectLoop:
		for {
			select {
			case <-collectTicker.C:
				fmt.Println("\nCollecting mid-test metrics (both pipelines)...")
				result.BaselineMetrics.CollectAllMetrics("mid-test")
				result.HighPerfMetrics.CollectAllMetrics("mid-test")
				snapshotCount++

				// Print verbose delta if enabled
				if config.VerboseMetrics && snapshotCount >= 2 {
					fmt.Println("  [Baseline]")
					result.BaselineMetrics.PrintVerboseMetricsDelta(snapshotCount-2, snapshotCount-1)
					fmt.Println("  [HighPerf]")
					result.HighPerfMetrics.PrintVerboseMetricsDelta(snapshotCount-2, snapshotCount-1)
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
	fmt.Println("\n--- Quiesce Phase (Both Pipelines) ---")

	// Send SIGUSR1 to both client-generators to pause data generation
	fmt.Println("Sending SIGUSR1 to both client-generators (pause data)...")
	_ = signalProcess(baseline.ClientGen, syscall.SIGUSR1)
	_ = signalProcess(highperf.ClientGen, syscall.SIGUSR1)

	// Wait for both pipelines to stabilize
	fmt.Println("Waiting for metrics to stabilize...")
	stabCtx, stabCancel := context.WithTimeout(ctx, 10*time.Second)

	// Try to stabilize both (use longer timeout)
	baselineStab := result.BaselineMetrics.WaitForStabilization(stabCtx)
	highperfStab := result.HighPerfMetrics.WaitForStabilization(stabCtx)
	stabCancel()

	if baselineStab.Stable {
		fmt.Printf("✓ Baseline stabilized in %v\n", baselineStab.Elapsed.Round(time.Millisecond))
	} else {
		fmt.Printf("⚠ Baseline stabilization timeout: %v\n", baselineStab.Error)
	}
	if highperfStab.Stable {
		fmt.Printf("✓ HighPerf stabilized in %v\n", highperfStab.Elapsed.Round(time.Millisecond))
	} else {
		fmt.Printf("⚠ HighPerf stabilization timeout: %v\n", highperfStab.Error)
	}

	// Collect pre-shutdown metrics
	fmt.Println("Collecting pre-shutdown metrics...")
	result.BaselineMetrics.CollectAllMetrics("pre-shutdown")
	result.HighPerfMetrics.CollectAllMetrics("pre-shutdown")

	// =================================================================
	// SHUTDOWN PHASE: Gracefully stop all 6 processes
	// =================================================================
	fmt.Println("\n--- Shutdown Phase ---")

	// Stop impairment pattern
	if config.Impairment.Pattern != "" && config.Impairment.Pattern != "clean" {
		fmt.Println("Stopping impairment pattern...")
		_ = nc.StopPatternParallel(ctx)
	}

	// Clear loss
	if config.Impairment.LossRate > 0 {
		fmt.Println("Clearing packet loss...")
		_ = nc.SetLossParallel(ctx, 0)
	}

	// Shutdown all processes
	fmt.Println("\nInitiating parallel shutdown sequence...")
	allPassed := shutdownParallelPipelines(baseline, highperf)

	// Collect final metrics
	fmt.Println("\nCollecting final metrics...")
	result.BaselineMetrics.CollectAllMetrics("final")
	result.HighPerfMetrics.CollectAllMetrics("final")

	// Generate profile comparison report if profiling was enabled
	if profileConfig != nil {
		generateParallelProfileReport(config.Name, profileConfig, config.TestDuration)
	}

	result.Passed = allPassed
	result.EndTime = time.Now()
	return result
}

// generateParallelProfileReport analyzes and compares profiles from baseline and highperf pipelines
func generateParallelProfileReport(testName string, profileConfig *ProfileConfig, testDuration time.Duration) {
	fmt.Println("\n=== Analyzing Parallel Test Profiles ===")

	// Analyze all collected profiles
	analyses, err := AnalyzeAllProfiles(profileConfig.OutputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to analyze profiles: %v\n", err)
		return
	}

	if len(analyses) == 0 {
		fmt.Println("No profile files found to analyze")
		return
	}

	// Print summary of all profiles
	PrintAnalysisSummary(analyses)

	// Separate analyses by pipeline using the Pipeline field
	var baselineAnalyses, highperfAnalyses []*ProfileAnalysis
	for _, a := range analyses {
		switch a.Pipeline {
		case "baseline":
			baselineAnalyses = append(baselineAnalyses, a)
		case "highperf":
			highperfAnalyses = append(highperfAnalyses, a)
		}
	}

	fmt.Printf("\nFound %d baseline profiles and %d highperf profiles\n",
		len(baselineAnalyses), len(highperfAnalyses))

	// Generate comparisons between matching components
	// Match by component name (server, cg, client) and profile type
	var comparisons []*ComparisonResult
	componentNames := []string{"server", "cg", "client"}

	for _, compName := range componentNames {
		// Find baseline and highperf profiles for this component
		for _, baseAnalysis := range baselineAnalyses {
			if baseAnalysis.Component != compName {
				continue
			}
			for _, hpAnalysis := range highperfAnalyses {
				// Match by component name and profile type
				if hpAnalysis.Component != compName {
					continue
				}
				if baseAnalysis.ProfileType != hpAnalysis.ProfileType {
					continue
				}

				// Generate and print comparison
				comparison := CompareProfiles(baseAnalysis, hpAnalysis)
				comparisons = append(comparisons, comparison)

				fmt.Printf("\n╔═══════════════════════════════════════════════════════════════╗\n")
				fmt.Printf("║  %s %s: Baseline vs HighPerf                    \n",
					strings.ToUpper(compName), strings.ToUpper(string(baseAnalysis.ProfileType)))
				fmt.Printf("╚═══════════════════════════════════════════════════════════════╝\n")
				fmt.Print(comparison.FormatComparison())
			}
		}
	}

	// Print overall summary
	if len(comparisons) > 0 {
		printParallelProfileSummary(comparisons)
	}

	// Generate HTML report
	report := NewProfileReport(testName+" (Parallel Comparison)", "parallel", profileConfig.OutputDir, testDuration)
	report.IsComparison = true
	report.BaselineAnalyses = baselineAnalyses
	report.HighPerfAnalyses = highperfAnalyses

	for _, a := range analyses {
		report.AddAnalysis(a)
	}
	for _, c := range comparisons {
		report.AddComparison(c)
	}
	report.CalculateOverallSummary()

	if err := GenerateHTMLReport(report); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to generate report: %v\n", err)
	}

	// Print summary of where profile files are located
	profileConfig.PrintProfileFileLocations()
}

// printParallelProfileSummary prints an overall summary of the parallel comparison
func printParallelProfileSummary(comparisons []*ComparisonResult) {
	fmt.Println("\n╔═══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  OVERALL PERFORMANCE COMPARISON: Baseline vs HighPerf            ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════════════╣")

	// Aggregate improvements across all comparisons
	totalImprovements := 0
	totalRegressions := 0
	var allRecommendations []string

	for _, comp := range comparisons {
		for _, fc := range comp.FuncComparisons {
			if fc.Delta < -1 { // More than 1% improvement
				totalImprovements++
			} else if fc.Delta > 1 { // More than 1% regression
				totalRegressions++
			}
		}
		allRecommendations = append(allRecommendations, comp.Recommendations...)
	}

	fmt.Printf("║  Total Improvements: %-46d ║\n", totalImprovements)
	fmt.Printf("║  Total Regressions:  %-46d ║\n", totalRegressions)
	fmt.Println("╠═══════════════════════════════════════════════════════════════════╣")

	if len(allRecommendations) > 0 {
		fmt.Println("║  TOP RECOMMENDATIONS:                                             ║")
		// Deduplicate and show top 5
		seen := make(map[string]bool)
		count := 0
		for _, rec := range allRecommendations {
			if seen[rec] || count >= 5 {
				continue
			}
			seen[rec] = true
			count++
			// Truncate to fit
			if len(rec) > 60 {
				rec = rec[:57] + "..."
			}
			fmt.Printf("║  • %-63s ║\n", rec)
		}
	}

	fmt.Println("╚═══════════════════════════════════════════════════════════════════╝")
}

// shutdownParallelPipelines shuts down both pipelines gracefully
func shutdownParallelPipelines(baseline, highperf *ParallelProcessSet) bool {
	allPassed := true

	// Shutdown clients first (subscribers)
	fmt.Println("Sending SIGINT to clients...")
	_ = signalProcess(baseline.Client, syscall.SIGINT)
	_ = signalProcess(highperf.Client, syscall.SIGINT)

	baselineClientExited := waitForProcessExit(baseline.Client, 5*time.Second)
	highperfClientExited := waitForProcessExit(highperf.Client, 5*time.Second)

	if baselineClientExited {
		fmt.Println("  ✓ Baseline client exited gracefully")
	} else {
		fmt.Println("  ✗ Baseline client did not exit gracefully")
		killProcess(baseline.Client)
		allPassed = false
	}
	if highperfClientExited {
		fmt.Println("  ✓ HighPerf client exited gracefully")
	} else {
		fmt.Println("  ✗ HighPerf client did not exit gracefully")
		killProcess(highperf.Client)
		allPassed = false
	}

	// Shutdown client-generators (publishers)
	fmt.Println("Sending SIGINT to client-generators...")
	_ = signalProcess(baseline.ClientGen, syscall.SIGINT)
	_ = signalProcess(highperf.ClientGen, syscall.SIGINT)

	baselineClientGenExited := waitForProcessExit(baseline.ClientGen, 8*time.Second)
	highperfClientGenExited := waitForProcessExit(highperf.ClientGen, 8*time.Second)

	if baselineClientGenExited {
		fmt.Println("  ✓ Baseline client-generator exited gracefully")
	} else {
		fmt.Println("  ✗ Baseline client-generator did not exit gracefully")
		killProcess(baseline.ClientGen)
		allPassed = false
	}
	if highperfClientGenExited {
		fmt.Println("  ✓ HighPerf client-generator exited gracefully")
	} else {
		fmt.Println("  ✗ HighPerf client-generator did not exit gracefully")
		killProcess(highperf.ClientGen)
		allPassed = false
	}

	// Shutdown servers
	fmt.Println("Sending SIGINT to servers...")
	_ = signalProcess(baseline.Server, syscall.SIGINT)
	_ = signalProcess(highperf.Server, syscall.SIGINT)

	baselineServerExited := waitForProcessExit(baseline.Server, 10*time.Second)
	highperfServerExited := waitForProcessExit(highperf.Server, 10*time.Second)

	if baselineServerExited {
		fmt.Println("  ✓ Baseline server exited gracefully")
	} else {
		fmt.Println("  ✗ Baseline server did not exit gracefully")
		killProcess(baseline.Server)
		allPassed = false
	}
	if highperfServerExited {
		fmt.Println("  ✓ HighPerf server exited gracefully")
	} else {
		fmt.Println("  ✗ HighPerf server did not exit gracefully")
		killProcess(highperf.Server)
		allPassed = false
	}

	return allPassed
}

// printParallelTestHeader prints the header for a parallel test
func printParallelTestHeader(config ParallelTestConfig) {
	fmt.Printf("=== Parallel Comparison Test: %s ===\n", config.Name)
	fmt.Printf("Description: %s\n", config.Description)
	fmt.Printf("Bitrate: %d bps (%.2f Mb/s)\n", config.Bitrate, float64(config.Bitrate)/1_000_000)
	if config.Impairment.Pattern != "" {
		fmt.Printf("Impairment Pattern: %s\n", config.Impairment.Pattern)
	} else if config.Impairment.LossRate > 0 {
		fmt.Printf("Packet Loss: %.1f%%\n", config.Impairment.LossRate*100)
	}
	if config.Impairment.LatencyProfile != "" {
		fmt.Printf("Latency Profile: %s\n", config.Impairment.LatencyProfile)
	}
	fmt.Println()
	fmt.Println("Pipelines:")
	fmt.Println("  Baseline: list + no io_uring")
	fmt.Println("  HighPerf: btree + io_uring")
	fmt.Println()
}
