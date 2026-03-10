package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/profile"
	srt "github.com/randomizedcoder/gosrt"
	"github.com/randomizedcoder/gosrt/contrib/common"
	"github.com/randomizedcoder/gosrt/metrics"
)

const (
	CHANNEL_SIZE = 2048
)

var (
	// Client-generator-specific flags
	to          = flag.String("to", "", "Address to write to, targets: srt:// (required)")
	logtopics   = flag.String("logtopics", "", "topics for the log output")
	testflags   = flag.Bool("testflags", false, "Test mode: parse flags, apply to config, print config as JSON, and exit")
	bitrate     = flag.Uint64("bitrate", 2_000_000, "Bitrate in bits per second (default: 2Mbps)")
	profileFlag = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
	profilePath = flag.String("profilepath", ".", "directory to write profile files to")

	// Pause flag for graceful quiesce (set via SIGUSR1 signal)
	paused atomic.Bool
)

func main() {
	os.Exit(run())
}

func run() int {
	// Parse all flags (shared + client-generator-specific)
	common.ParseFlags()

	// Validate flag dependencies and auto-enable required flags
	if warnings := common.ValidateFlagDependencies(); len(warnings) > 0 {
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "⚠ %s\n", w)
		}
	}

	// Test mode: print config and exit (before profiler starts)
	if exitCode, handled := common.HandleTestFlags(*testflags, nil); handled {
		return exitCode
	}

	// Validate required flags before starting profiler
	if len(*to) == 0 {
		fmt.Fprintf(os.Stderr, "Error: -to is required\n")
		flag.PrintDefaults()
		return 1
	}

	// Setup profiling if requested
	if p := common.ProfileOption(*profileFlag); p != nil {
		prof := profile.Start(profile.ProfilePath(*profilePath), profile.NoShutdownHook, p)
		defer prof.Stop()
	}

	var logger srt.Logger

	if len(*logtopics) != 0 {
		logger = srt.NewLogger(strings.Split(*logtopics, ","))
	}

	// Get config to check for statistics interval
	config := srt.DefaultConfig()
	common.ApplyFlagsToConfig(&config)

	// ============================================================
	// Create context that cancels on signal (replaces setupSignalHandler)
	// ============================================================
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ============================================================
	// SIGUSR1 handler for graceful quiesce (pause data generation)
	// ============================================================
	common.SetupPauseHandler(&paused, "stopping data generation")

	// Single waitgroup for all goroutines
	var wg sync.WaitGroup

	// ============================================================
	// Start Prometheus Metrics Server(s) (if configured)
	// ============================================================
	if err := common.StartMetricsServers(ctx, &wg, *common.PromHTTPAddr, *common.PromUDSPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start metrics server: %v\n", err)
		return 1
	}

	// ============================================================
	// Start Logger Goroutine (if enabled)
	// ============================================================
	srt.RunLoggerOutput(logger, &wg)

	// ============================================================
	// Open Writer (SRT connection to server)
	// ============================================================
	w, err := openWriter(*to, logger, ctx, &wg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: to: %v\n", err)
		flag.PrintDefaults()
		return 1
	}

	// Store connection socket ID for metrics lookup (if SRT connection)
	var connSocketId atomic.Uint32
	if srtconn, ok := w.(srt.Conn); ok {
		connSocketId.Store(srtconn.SocketId())
	}

	// ============================================================
	// Start Statistics Ticker (if enabled)
	// ============================================================
	srt.StartStatisticsTicker(ctx, &wg, config.StatisticsPrintInterval,
		func() []srt.Conn {
			if srtconn, ok := w.(srt.Conn); ok {
				return []srt.Conn{srtconn}
			}
			return nil
		},
		func(index int, total int) string { return "publisher" })

	// ============================================================
	// Create Client Metrics (lock-free atomic counters)
	// ============================================================
	// Application-level metrics for basic byte/packet counting
	clientMetrics := &metrics.ConnectionMetrics{}

	// Start throughput stats display loop
	common.StartThroughputDisplay(ctx, &wg, *common.StatsPeriod,
		"PUB", config.InstanceName, *common.OutputColor, func() (uint64, uint64, uint64, uint64, uint64, uint64) {
			var naksRecv, retrans uint64
			if socketId := connSocketId.Load(); socketId != 0 {
				conns := metrics.GetConnections()
				if connInfo, ok := conns[socketId]; ok && connInfo != nil && connInfo.Metrics != nil {
					naksRecv = connInfo.Metrics.CongestionSendNAKPktsRecv.Load()
					retrans = connInfo.Metrics.PktRetransFromNAK.Load()
				}
			}
			return clientMetrics.ByteSentDataSuccess.Load(),
				clientMetrics.PktSentDataSuccess.Load(),
				0, naksRecv, 0, retrans
		})

	// ============================================================
	// Main Write Loop
	// ============================================================
	// Buffered channel prevents goroutine blocking if main receives ctx.Done() first
	doneChan := make(chan error, 10)

	wg.Add(1)
	go func() {
		defer wg.Done()

		// Generate test data - pass context so generator stops on shutdown
		generator := newDataGenerator(ctx, *bitrate)
		buffer := make([]byte, CHANNEL_SIZE)

		for {
			// Check context cancellation first
			select {
			case <-ctx.Done():
				// Context canceled - exit gracefully
				doneChan <- nil
				return
			default:
			}

			// Check if paused (for graceful quiesce)
			if paused.Load() {
				// Paused - sleep briefly to reduce CPU, check for shutdown
				select {
				case <-ctx.Done():
					doneChan <- nil
					return
				case <-time.After(100 * time.Millisecond):
					continue
				}
			}

			n, readErr := generator.Read(buffer)
			if readErr != nil {
				if readErr == io.EOF {
					// Generator closed, exit gracefully
					doneChan <- nil
					return
				}
				// Check if context was canceled during read
				select {
				case <-ctx.Done():
					// Context canceled - exit gracefully (don't report error)
					doneChan <- nil
					return
				default:
					doneChan <- fmt.Errorf("generator read: %w", readErr)
					return
				}
			}

			// Check context cancellation before write
			select {
			case <-ctx.Done():
				// Context canceled - exit gracefully
				doneChan <- nil
				return
			default:
			}

			written, writeErr := w.Write(buffer[:n])
			if writeErr != nil {
				// Check if context was canceled or connection closed during shutdown
				select {
				case <-ctx.Done():
					// Context canceled - exit gracefully (don't report error)
					doneChan <- nil
					return
				default:
					if srt.IsConnectionClosedError(writeErr) {
						doneChan <- nil
						return
					}
					doneChan <- fmt.Errorf("write: %w", writeErr)
					return
				}
			}

			// Lock-free atomic increments for throughput tracking
			clientMetrics.ByteSentDataSuccess.Add(uint64(written))
			clientMetrics.PktSentDataSuccess.Add(1)
		}
	}()

	// ============================================================
	// Wait for Completion or Context Cancellation
	// ============================================================
	var shutdownStart time.Time
	select {
	case doneErr := <-doneChan:
		shutdownStart = time.Now()
		if doneErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", doneErr)
		} else {
			fmt.Fprint(os.Stderr, "\n")
		}
	case <-ctx.Done():
		shutdownStart = time.Now()
		// Context canceled - graceful shutdown
		fmt.Fprintf(os.Stderr, "\nShutdown signal received\n")
	}

	// ============================================================
	// Cleanup
	// ============================================================
	// Close connection
	_ = w.Close()

	// Close logger so its goroutine can exit (channel will close)
	if logger != nil {
		logger.Close()
	}

	// ============================================================
	// Wait for All Goroutines with Timeout
	// ============================================================
	common.WaitForShutdown(&wg, shutdownStart, config.ShutdownDelay)

	return 0
}

// openWriter opens a writer based on the URL scheme
func openWriter(address string, logger srt.Logger, ctx context.Context, wg *sync.WaitGroup) (io.WriteCloser, error) {
	config := srt.DefaultConfig()
	common.ApplyFlagsToConfig(&config)

	if logger != nil {
		config.Logger = logger
	}

	conn, err := srt.DialPublisher(ctx, address, config, wg)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// newDataGenerator creates a rate-limited data generator using the efficient
// common.DataGenerator implementation. This replaces the old byte-at-a-time
// channel-based approach that caused CPU saturation at high bitrates.
//
// The new implementation:
// - Uses packet-level rate limiting (1 limiter call per 1456 bytes vs per byte)
// - Pre-fills payload once (zero allocations in hot path)
// - Uses time-based pacing for high-rate traffic (>1000 pkt/s)
// - Achieves accurate rates up to 75+ Mb/s
func newDataGenerator(ctx context.Context, bitrate uint64) *common.DataGenerator {
	// Use PayloadSize from flags if set, otherwise use default
	payloadSize := uint32(*common.PayloadSize)
	if payloadSize == 0 {
		payloadSize = 1456 // Default SRT max payload size
	}

	return common.NewDataGenerator(ctx, bitrate, payloadSize)
}
