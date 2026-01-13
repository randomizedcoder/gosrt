package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
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
	// Parse all flags (shared + client-generator-specific)
	common.ParseFlags()

	// Setup profiling if requested
	var p func(*profile.Profile)
	switch *profileFlag {
	case "cpu":
		p = profile.CPUProfile
	case "mem":
		p = profile.MemProfile
	case "allocs":
		p = profile.MemProfileAllocs
	case "heap":
		p = profile.MemProfileHeap
	case "rate":
		p = profile.MemProfileRate(2048)
	case "mutex":
		p = profile.MutexProfile
	case "block":
		p = profile.BlockProfile
	case "thread":
		p = profile.ThreadcreationProfile
	case "trace":
		p = profile.TraceProfile
	default:
	}

	var prof interface{ Stop() }
	if p != nil {
		prof = profile.Start(profile.ProfilePath(*profilePath), profile.NoShutdownHook, p)
		defer prof.Stop()
	}
	// Silence unused variable warning when profiling is not enabled
	_ = prof

	// Test mode: print config and exit
	if *testflags {
		config := srt.DefaultConfig()
		common.ApplyFlagsToConfig(&config)
		// Print config as JSON
		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		os.Exit(0)
	}

	if len(*to) == 0 {
		fmt.Fprintf(os.Stderr, "Error: -to is required\n")
		flag.PrintDefaults()
		os.Exit(1)
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
	pauseChan := make(chan os.Signal, 1)
	signal.Notify(pauseChan, syscall.SIGUSR1)
	go func() {
		<-pauseChan
		fmt.Fprintf(os.Stderr, "\nPAUSE signal received - stopping data generation\n")
		paused.Store(true)
	}()

	// Single waitgroup for all goroutines
	var wg sync.WaitGroup

	// ============================================================
	// Start Prometheus Metrics Server(s) (if configured)
	// ============================================================
	if err := common.StartMetricsServers(ctx, &wg, *common.PromHTTPAddr, *common.PromUDSPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start metrics server: %v\n", err)
		os.Exit(1)
	}

	// ============================================================
	// Start Logger Goroutine (if enabled)
	// ============================================================
	if logger != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range logger.Listen() {
				fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n",
					m.SocketId, m.Topic, m.File, m.Line, m.Message)
			}
		}()
	}

	// ============================================================
	// Open Writer (SRT connection to server)
	// ============================================================
	w, err := openWriter(*to, logger, ctx, &wg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: to: %v\n", err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Store connection socket ID for metrics lookup (if SRT connection)
	var connSocketId atomic.Uint32
	if srtconn, ok := w.(srt.Conn); ok {
		connSocketId.Store(srtconn.SocketId())
	}

	// ============================================================
	// Start Statistics Ticker (if enabled)
	// ============================================================
	if config.StatisticsPrintInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(config.StatisticsPrintInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					var connections []srt.Conn

					// Check if writer is an SRT connection
					if srtconn, ok := w.(srt.Conn); ok {
						connections = append(connections, srtconn)
					}

					// Create labeler function for client-generator (publisher label)
					labeler := func(index int, total int) string {
						return "publisher"
					}

					common.PrintConnectionStatistics(connections, config.StatisticsPrintInterval.String(), labeler)
				}
			}
		}()
	}

	// ============================================================
	// Create Client Metrics (lock-free atomic counters)
	// ============================================================
	// Application-level metrics for basic byte/packet counting
	clientMetrics := &metrics.ConnectionMetrics{}

	// Start throughput stats display loop (uses shared common function)
	// Shows send stats: bytes, packets, gaps=0, NAKs received, skips=0, and retransmits
	// Note: For senders, gaps and skips are 0 (these are receiver-side metrics)
	// Use instance name from config if set, otherwise default to "PUB"
	instanceLabel := "PUB"
	if config.InstanceName != "" {
		instanceLabel = config.InstanceName
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		common.RunThroughputDisplayWithLabelAndColor(ctx, *common.StatsPeriod, instanceLabel, *common.OutputColor, func() (uint64, uint64, uint64, uint64, uint64, uint64) {
			// Get NAK received and retransmit count from the actual connection's metrics (if available)
			var naksRecv, retrans uint64
			if socketId := connSocketId.Load(); socketId != 0 {
				// Query the actual connection metrics
				conns := metrics.GetConnections()
				if connInfo, ok := conns[socketId]; ok && connInfo != nil && connInfo.Metrics != nil {
					naksRecv = connInfo.Metrics.CongestionSendNAKPktsRecv.Load() // NAKs received from receiver
					retrans = connInfo.Metrics.PktRetransFromNAK.Load()
				}
			}
			// Sender doesn't have gaps/skips - those are receiver metrics
			// Return 0 for gaps and skips, which gives recovery=100%
			// For NAKs, show NAKs received (from receiver side)
			return clientMetrics.ByteSentDataSuccess.Load(),
				clientMetrics.PktSentDataSuccess.Load(),
				0, // gaps (N/A for sender)
				naksRecv,
				0, // skips (N/A for sender)
				retrans
		})
	}()

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
				// Context cancelled - exit gracefully
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

			n, err := generator.Read(buffer)
			if err != nil {
				if err == io.EOF {
					// Generator closed, exit gracefully
					doneChan <- nil
					return
				}
				// Check if context was cancelled during read
				select {
				case <-ctx.Done():
					// Context cancelled - exit gracefully (don't report error)
					doneChan <- nil
					return
				default:
					doneChan <- fmt.Errorf("generator read: %w", err)
					return
				}
			}

			// Check context cancellation before write
			select {
			case <-ctx.Done():
				// Context cancelled - exit gracefully
				doneChan <- nil
				return
			default:
			}

			written, err := w.Write(buffer[:n])
			if err != nil {
				// Check if context was cancelled or connection closed during shutdown
				select {
				case <-ctx.Done():
					// Context cancelled - exit gracefully (don't report error)
					doneChan <- nil
					return
				default:
					// Check if error is due to connection being closed (expected during shutdown)
					errStr := err.Error()
					if strings.Contains(errStr, "connection refused") ||
						strings.Contains(errStr, "use of closed network connection") ||
						strings.Contains(errStr, "broken pipe") {
						// Connection closed during shutdown - exit gracefully (don't report error)
						doneChan <- nil
						return
					}
					// Check for net.OpError which indicates connection issues
					if opErr, ok := err.(*net.OpError); ok {
						if opErr.Err != nil {
							errStr := opErr.Err.Error()
							if strings.Contains(errStr, "connection refused") ||
								strings.Contains(errStr, "broken pipe") {
								// Connection closed during shutdown - exit gracefully (don't report error)
								doneChan <- nil
								return
							}
						}
					}
					doneChan <- fmt.Errorf("write: %w", err)
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
	case err := <-doneChan:
		shutdownStart = time.Now()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Fprint(os.Stderr, "\n")
		}
	case <-ctx.Done():
		shutdownStart = time.Now()
		// Context cancelled - graceful shutdown
		fmt.Fprintf(os.Stderr, "\nShutdown signal received\n")
	}

	// ============================================================
	// Cleanup
	// ============================================================
	// Close connection
	w.Close()

	// Close logger so its goroutine can exit (channel will close)
	if logger != nil {
		logger.Close()
	}

	// ============================================================
	// Wait for All Goroutines with Timeout
	// ============================================================
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Graceful shutdown complete after %dms\n", elapsedMs)
	case <-time.After(config.ShutdownDelay):
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Shutdown timed out after %s (elapsed: %dms)\n", config.ShutdownDelay, elapsedMs)
	}
}

// openWriter opens a writer based on the URL scheme
func openWriter(address string, logger srt.Logger, ctx context.Context, wg *sync.WaitGroup) (io.WriteCloser, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	switch u.Scheme {
	case "srt":
		config := srt.DefaultConfig()
		common.ApplyFlagsToConfig(&config)

		if logger != nil {
			config.Logger = logger
		}

		// Set stream ID in config before dialing
		// Server expects "publish:/path/to/stream" format
		streamID := u.Path
		if streamID == "" {
			streamID = "/"
		}
		// Ensure it starts with "publish:" prefix
		if !strings.HasPrefix(streamID, "publish:") {
			streamID = "publish:" + streamID
		}
		config.StreamId = streamID

		conn, err := srt.Dial(ctx, "srt", u.Host, config, wg)
		if err != nil {
			return nil, fmt.Errorf("dial: %w", err)
		}

		return conn, nil
	default:
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
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
