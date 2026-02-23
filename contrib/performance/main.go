// performance is an automated performance testing orchestrator for gosrt.
//
// It discovers the maximum sustainable throughput for a given SRT configuration
// by using AIMD (Additive Increase, Multiplicative Decrease) to find the stability
// ceiling.
//
// Usage:
//
//	./performance [flags]
//
// The tool uses the same flag system as server, client-generator, and client-seeker,
// allowing direct copy-paste of flags from isolation/parallel tests.
//
// Examples:
//
//	# Basic test with defaults (200M initial, 600M max)
//	./performance
//
//	# Start at 350M with custom SRT config (copy from isolation test)
//	./performance -initial 350000000 -fc 102400 -rcvbuf 67108864 \
//	  -iouringrecvringcount 2 -useeventloop -usepacketring
//
//	# With verbose output
//	./performance -test-verbose -initial 200000000
//
// See -help for all available flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/randomizedcoder/gosrt/contrib/common"
)

var (
	flagHelp    = flag.Bool("help", false, "Show help")
	flagVersion = flag.Bool("version", false, "Show version")
	flagDryRun  = flag.Bool("dry-run", false, "Parse config and validate contracts without running")
)

func main() {
	// Parse ALL flags (common SRT flags + test flags)
	common.ParseFlags()

	// Validate flag dependencies and auto-enable required flags
	if warnings := common.ValidateFlagDependencies(); len(warnings) > 0 {
		fmt.Println("=== Flag Dependencies ===")
		for _, w := range warnings {
			fmt.Printf("  ⚠ %s\n", w)
		}
		fmt.Println()
	}

	if *flagHelp {
		printUsage()
		os.Exit(0)
	}

	if *flagVersion {
		fmt.Println("performance v0.2.0 (unified flags)")
		os.Exit(0)
	}

	// Create configuration from parsed flags
	config := ConfigFromFlags()

	// Validate timing contracts
	if err := config.Timing.ValidateContracts(); err != nil {
		fmt.Fprintf(os.Stderr, "Contract violation:\n%v\n", err)
		os.Exit(1)
	}

	// Print configuration summary
	printConfig(config)

	// Print explicitly set flags (for debugging)
	if config.Verbose {
		common.PrintFlagSummary()
	}

	if *flagDryRun {
		fmt.Println("\n✓ Configuration valid (dry-run mode)")
		os.Exit(0)
	}

	// Create context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		fmt.Fprintf(os.Stderr, "\nReceived signal %v, stopping...\n", sig)
		cancel()
	}()

	// Resolve binary paths
	config.ServerBinary = ResolveBinaryPath(config.ServerBinary)
	config.SeekerBinary = ResolveBinaryPath(config.SeekerBinary)

	// Check binaries exist
	if _, err := os.Stat(config.ServerBinary); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Server binary not found: %s\n", config.ServerBinary)
		fmt.Fprintf(os.Stderr, "Build with: make build-performance\n")
		os.Exit(1)
	}
	if _, err := os.Stat(config.SeekerBinary); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Seeker binary not found: %s\n", config.SeekerBinary)
		fmt.Fprintf(os.Stderr, "Build with: make build-performance\n")
		os.Exit(1)
	}

	// Create process manager
	pm := NewProcessManager(*config)
	defer pm.Stop()

	// Start server
	fmt.Println("\n=== Starting Server ===")
	if err := pm.StartServer(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start server: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Server started on %s\n", config.ServerAddr)

	// Start seeker
	fmt.Println("\n=== Starting Client-Seeker ===")
	if err := pm.StartSeeker(ctx, config.Search.InitialBitrate); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start seeker: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Seeker started at %s\n", FormatBitrate(config.Search.InitialBitrate))

	// Wait for readiness
	fmt.Println("\n=== Waiting for Readiness ===")
	if err := pm.WaitReady(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Readiness check failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ All components ready")

	// Create components using ProcessManager's actual socket paths
	reportMode := ReportTerminal
	if config.JSONOutput {
		reportMode = ReportJSON
	}
	reporter := NewProgressReporter(reportMode)
	reporter.SetVerbose(config.Verbose)

	// Create metrics collector using ProcessManager's paths
	metricsCollector := NewMetricsCollector(
		pm.ServerMetricsPath(),
		pm.SeekerMetricsPath(),
	)

	// Create seeker control using ProcessManager's path
	seekerControl := NewSeekerControl(pm.SeekerControlPath())
	if err := seekerControl.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to seeker control: %v\n", err)
		os.Exit(1)
	}
	defer seekerControl.Close()

	// Create profiler
	profiler := NewDiagnosticProfiler(config.ProfileDir, nil)

	// Create stability gate
	gate := NewStabilityGate(config.Stability, config.Timing, metricsCollector, seekerControl, profiler)

	// Create search loop
	searchLoop := NewSearchLoop(config.Search, config.Timing, gate, seekerControl)
	searchLoop.SetVerbose(config.Verbose)
	searchLoop.SetStatusInterval(config.StatusInterval)

	// Create and start CPU monitor for visibility
	cpuMonitor := NewCPUMonitor()
	serverPID, seekerPID := pm.GetPIDs()
	cpuMonitor.SetProcesses(serverPID, seekerPID)
	cpuMonitor.Start()
	defer cpuMonitor.Stop()
	searchLoop.SetCPUMonitor(cpuMonitor)

	// Run the search
	fmt.Println("\n=== Starting Performance Search ===")
	fmt.Printf("Finding maximum sustainable throughput in range [%s, %s]\n",
		FormatBitrate(config.Search.MinBitrate), FormatBitrate(config.Search.MaxBitrate))

	result := searchLoop.Run(ctx)

	// Output results
	reporter.FinalReport(result)

	// Save probes if output path specified
	if config.OutputPath != "" {
		if err := reporter.SaveProbes(config.OutputPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save probes: %v\n", err)
		} else {
			fmt.Printf("\nProbes saved to: %s\n", config.OutputPath)
		}
	}

	// Exit with appropriate code
	if result.Status == StatusSuccess {
		os.Exit(0)
	} else {
		os.Exit(1)
	}
}

// printUsage prints usage information.
func printUsage() {
	fmt.Println(`Performance Test Orchestrator

Usage:
  ./performance [flags]

Uses the same flag system as server/client-generator, allowing direct
copy-paste of flags from isolation/parallel tests.

Performance Test Flags:
  -initial            Starting bitrate in bps (default: 200000000 = 200M)
  -min-bitrate        Minimum bitrate floor (default: 50000000 = 50M)
  -max-bitrate        Maximum bitrate ceiling (default: 600000000 = 600M)
  -step               Additive increase step (default: 10000000 = 10M)
  -precision          Search precision (default: 5000000 = 5M)
  -search-timeout     Maximum search time (default: 10m)

  -warmup             Warm-up duration after bitrate change (default: 2s)
  -stability-window   Stability evaluation window (default: 5s)

  -test-verbose       Enable verbose output
  -test-json          Output results as JSON
  -test-output        Path for result output file
  -profile-dir        Directory for profile captures (default: /tmp/srt_profiles)
  -status-interval    Interval for progress status updates (default: 5s, 0=disabled)

SRT Configuration Flags (passed to server and client-seeker):
  -fc                 Flow control window (packets)
  -rcvbuf             Receiver buffer size (bytes)
  -sndbuf             Sender buffer size (bytes)
  -latency            Latency in milliseconds

  -iouringenabled     Enable io_uring
  -iouringrecvenabled Enable io_uring receive
  -iouringrecvringcount  Number of receive rings (default: 1)
  -iouringrecvringsize   Size of each receive ring

  -useeventloop       Enable continuous event loop
  -usepacketring      Enable lock-free packet ring
  -packetringsize     Packet ring size per shard
  -packetringshards   Number of packet ring shards

  -usesendbtree       Enable btree for sender
  -usesendring        Enable lock-free sender ring
  -usesendcontrolring Enable lock-free control ring
  -sendcontrolringsize  Control ring size (increase for high throughput)

  ... and all other SRT flags (see -help for server)

Control Flags:
  -help               Show this help
  -version            Show version
  -dry-run            Validate config without running

Examples:
  # Basic test with defaults
  ./performance

  # Copy flags from isolation test
  ./performance -initial 350000000 -fc 102400 -rcvbuf 67108864 \
    -iouringrecvringcount 2 -useeventloop -usepacketring \
    -usesendbtree -usesendring -usesendcontrolring -sendcontrolringsize 1024

  # Validate configuration only
  ./performance -dry-run -initial 400000000 -fc 204800`)
}

// printConfig prints the configuration summary.
func printConfig(config *Config) {
	fmt.Println("=== Configuration ===")
	fmt.Printf("Search:\n")
	fmt.Printf("  Initial: %s\n", FormatBitrate(config.Search.InitialBitrate))
	fmt.Printf("  Range: %s - %s\n", FormatBitrate(config.Search.MinBitrate), FormatBitrate(config.Search.MaxBitrate))
	fmt.Printf("  Step: %s, Precision: %s\n", FormatBitrate(config.Search.StepSize), FormatBitrate(config.Search.Precision))
	fmt.Printf("  Timeout: %v\n", config.Search.Timeout)

	fmt.Printf("\nStability:\n")
	fmt.Printf("  WarmUp: %v, Window: %v\n", config.Stability.WarmUpDuration, config.Stability.StabilityWindow)
	fmt.Printf("  MaxGapRate: %.2f%%, MaxNAKRate: %.2f%%\n", config.Stability.MaxGapRate*100, config.Stability.MaxNAKRate*100)

	fmt.Printf("\nTiming:\n")
	fmt.Printf("  Heartbeat: %v, Watchdog: %v\n", config.Timing.HeartbeatInterval, config.Timing.WatchdogTimeout)
	fmt.Printf("  MinProbe: %v, RequiredSamples: %d\n", config.Timing.MinProbeDuration, config.Timing.RequiredSamples)

	// Print key SRT flags that are set
	fmt.Printf("\nSRT Flags (explicitly set):\n")
	srtFlags := common.BuildFlagArgs()
	if len(srtFlags) == 0 {
		fmt.Printf("  (using defaults)\n")
	} else {
		for _, f := range srtFlags {
			fmt.Printf("  %s\n", f)
		}
	}

	fmt.Printf("\nBinaries:\n")
	fmt.Printf("  Server: %s\n", config.ServerBinary)
	fmt.Printf("  Seeker: %s\n", config.SeekerBinary)
}
