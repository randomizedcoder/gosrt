// client-seeker is a controllable SRT data generator for performance testing.
//
// The client-seeker is designed to be orchestrated by the performance test tool.
// It accepts bitrate commands via a Unix domain socket and sends data at the
// specified rate over an SRT connection.
//
// Usage:
//
//	./client-seeker -target srt://host:port/stream [SRT flags...]
//
// Control Protocol (JSON over Unix socket):
//
//	{"command": "set_bitrate", "bitrate": 100000000}  // Set to 100 Mb/s
//	{"command": "get_status"}                          // Get current status
//	{"command": "heartbeat"}                           // Keep-alive
//	{"command": "stop"}                                // Graceful shutdown
//
// This tool uses the same flag system as server and client-generator, allowing
// direct copy-paste of flags from isolation/parallel tests.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pkg/profile"
	"github.com/randomizedcoder/gosrt/contrib/common"
)

var (
	// Seeker-specific flags (not in common because they're seeker-only)
	flagMinBitrate = flag.Int64("min-bitrate-seeker", 1_000_000, "Minimum allowed bitrate (default: 1M)")
	flagMaxBitrate = flag.Int64("max-bitrate-seeker", 1_000_000_000, "Maximum allowed bitrate (default: 1G)")
	flagPacketSize = flag.Int("packet-size", 1456, "Packet size in bytes (default: 1456 for SRT)")

	// TokenBucket mode flag
	// IMPORTANT: RefillSleep (default) is recommended for high throughput.
	// RefillHybrid uses spin-wait which can consume excessive CPU and become the bottleneck.
	// See: client_seeker_instrumentation_design.md Section 9.3
	flagRefillMode = flag.String("refill-mode", "sleep", "TokenBucket refill mode: sleep (default, low CPU), hybrid (medium CPU), spin (high CPU)")

	// Watchdog flags (override common defaults for seeker-specific behavior)
	flagWatchdog     = flag.Bool("watchdog", true, "Enable watchdog")
	flagWatchdogSafe = flag.Int64("watchdog-safe", 10_000_000, "Safe bitrate on watchdog trigger (default: 10M)")
	flagWatchdogStop = flag.Duration("watchdog-stop", 30*time.Second, "Watchdog timeout before stopping (0 = never)")

	// Profiling flags (same as server/client-generator/client)
	flagProfile     = flag.String("profile", "", "Enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
	flagProfilePath = flag.String("profilepath", ".", "Directory to write profile files to")
)

func main() {
	// Parse flags (including common SRT flags)
	common.ParseFlags()

	// Validate flag dependencies and auto-enable required flags
	if warnings := common.ValidateFlagDependencies(); len(warnings) > 0 {
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "⚠ %s\n", w)
		}
	}

	// Get values from common flags (with defaults)
	targetURL := *common.SeekerTarget
	controlPath := *common.SeekerControlUDS
	metricsPath := *common.SeekerMetricsUDS
	watchdogTimeout := *common.SeekerWatchdogTimeout

	// Initial bitrate from common flags
	initialBitrate := *common.TestInitialBitrate
	if initialBitrate <= 0 {
		initialBitrate = 100_000_000 // Default 100 Mb/s
	}

	// Create context that cancels on signal
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Setup profiling if requested
	if p := common.ProfileOption(*flagProfile); p != nil {
		defer profile.Start(profile.ProfilePath(*flagProfilePath), profile.NoShutdownHook, p).Stop()
	}

	// Parse refill mode
	var refillMode RefillMode
	switch *flagRefillMode {
	case "sleep":
		refillMode = RefillSleep
	case "hybrid":
		refillMode = RefillHybrid
	case "spin":
		refillMode = RefillSpin
	default:
		fmt.Fprintf(os.Stderr, "Warning: unknown refill mode %q, using 'sleep'\n", *flagRefillMode)
		refillMode = RefillSleep
	}

	// Create BitrateManager with specified refill mode
	bm := NewBitrateManagerWithMode(initialBitrate, *flagMinBitrate, *flagMaxBitrate, refillMode)

	// Start token bucket refill loop
	go bm.Bucket().StartRefillLoop(ctx)

	// Create DataGenerator
	gen := NewDataGenerator(bm.Bucket(), *flagPacketSize)

	// Create ControlServer (with generator for resetting stats on bitrate change)
	cs := NewControlServer(controlPath, bm, gen)

	// Create Watchdog
	watchdogConfig := WatchdogConfig{
		Enabled:     *flagWatchdog,
		Timeout:     watchdogTimeout,
		SafeBitrate: *flagWatchdogSafe,
		StopTimeout: *flagWatchdogStop,
	}
	watchdog := NewWatchdog(watchdogConfig, cs, bm, stop)

	// Create Publisher (if -target specified)
	var pub *Publisher
	if targetURL != "" {
		pub = NewPublisher(targetURL)
	}

	// Create MetricsServer
	metricsServer := NewMetricsServer(metricsPath, bm, gen, pub, cs, watchdog)

	// Start control server
	go func() {
		if err := cs.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Control server error: %v\n", err)
			stop()
		}
	}()

	// Start metrics server
	go func() {
		if err := metricsServer.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Metrics server error: %v\n", err)
		}
	}()

	// Start watchdog
	go watchdog.Run(ctx)

	// Print startup info
	fmt.Fprintf(os.Stderr, "client-seeker started\n")
	fmt.Fprintf(os.Stderr, "  Control socket: %s\n", controlPath)
	fmt.Fprintf(os.Stderr, "  Metrics socket: %s\n", metricsPath)
	fmt.Fprintf(os.Stderr, "  Initial bitrate: %s\n", FormatBitrate(initialBitrate))
	fmt.Fprintf(os.Stderr, "  Bitrate range: %s - %s\n", FormatBitrate(*flagMinBitrate), FormatBitrate(*flagMaxBitrate))
	fmt.Fprintf(os.Stderr, "  Packet size: %d bytes\n", *flagPacketSize)
	fmt.Fprintf(os.Stderr, "  TokenBucket mode: %s\n", *flagRefillMode)
	if *flagWatchdog {
		fmt.Fprintf(os.Stderr, "  Watchdog: enabled (timeout=%v, safe=%s)\n",
			watchdogTimeout, FormatBitrate(*flagWatchdogSafe))
	} else {
		fmt.Fprintf(os.Stderr, "  Watchdog: disabled\n")
	}
	if *flagProfile != "" {
		fmt.Fprintf(os.Stderr, "  Profile: %s → %s/\n", *flagProfile, *flagProfilePath)
	}

	if targetURL == "" {
		// Control-only mode (no SRT connection)
		fmt.Fprintf(os.Stderr, "\nRunning in control-only mode (no -target specified)\n")
		fmt.Fprintf(os.Stderr, "\nTest with:\n")
		fmt.Fprintf(os.Stderr, "  echo '{\"command\":\"get_status\"}' | nc -U %s\n", controlPath)
		fmt.Fprintf(os.Stderr, "  curl --unix-socket %s http://localhost/metrics\n", metricsPath)

		// Wait for shutdown
		<-ctx.Done()
	} else {
		// Full mode with SRT connection
		fmt.Fprintf(os.Stderr, "  SRT target: %s\n", targetURL)

		// Connect to SRT server
		fmt.Fprintf(os.Stderr, "\nConnecting to SRT server...\n")
		if err := pub.Connect(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to connect: %v\n", err)
			stop()
		} else {
			fmt.Fprintf(os.Stderr, "Connected! Socket ID: %d\n", pub.SocketID())
			cs.SetConnectionAlive(true)

			// Start publisher (sends data from generator to SRT)
			go func() {
				if runErr := pub.Run(ctx, gen); runErr != nil {
					fmt.Fprintf(os.Stderr, "Publisher error: %v\n", runErr)
					cs.SetConnectionAlive(false)
				}
			}()

			fmt.Fprintf(os.Stderr, "\nSending data at %s...\n", FormatBitrate(initialBitrate))
			fmt.Fprintf(os.Stderr, "\nTest with:\n")
			fmt.Fprintf(os.Stderr, "  echo '{\"command\":\"get_status\"}' | nc -U %s\n", controlPath)
			fmt.Fprintf(os.Stderr, "  echo '{\"command\":\"set_bitrate\",\"bitrate\":200000000}' | nc -U %s\n", controlPath)
			fmt.Fprintf(os.Stderr, "  curl --unix-socket %s http://localhost/metrics\n", metricsPath)
		}

		// Wait for shutdown
		<-ctx.Done()

		// Close publisher
		if pub != nil {
			if closeErr := pub.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "publisher close error: %v\n", closeErr)
			}
			pub.WaitWithTimeout(5 * time.Second)
		}
	}

	// Cleanup
	cs.Stop(ctx)
	metricsServer.Stop(ctx)
	fmt.Fprintf(os.Stderr, "Shutdown complete\n")
}
