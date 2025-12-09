package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	srt "github.com/datarhei/gosrt"
	"github.com/datarhei/gosrt/contrib/common"
	"github.com/datarhei/gosrt/metrics"
	"github.com/pkg/profile"
)

const (
	STATS_PERIOD = 1 * time.Second // Throughput display update interval
	CHANNEL_SIZE = 2048
)

var (
	// Client-specific flags
	from        = flag.String("from", "", "Address to read from, sources: srt://, udp://, - (stdin)")
	to          = flag.String("to", "", "Address to write to, targets: srt://, udp://, file://, - (stdout), null (discard output, useful for profiling)")
	logtopics   = flag.String("logtopics", "", "topics for the log output")
	profileFlag = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
	testflags   = flag.Bool("testflags", false, "Test mode: parse flags, apply to config, print config as JSON, and exit")
)

func main() {
	// Parse all flags (shared + client-specific)
	common.ParseFlags()

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

	// Store profile so we can stop it explicitly on signal
	var prof interface{ Stop() }
	if p != nil {
		prof = profile.Start(profile.ProfilePath("."), profile.NoShutdownHook, p)
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
	// Open Reader and Writer
	// ============================================================
	r, err := openReader(*from, logger, ctx, &wg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: from: %v\n", err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Store connection socket ID for metrics lookup (if SRT connection)
	var connSocketId atomic.Uint32
	if srtconn, ok := r.(srt.Conn); ok {
		connSocketId.Store(srtconn.SocketId())
	}

	w, err := openWriter(*to, logger, ctx, &wg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: to: %v\n", err)
		flag.PrintDefaults()
		os.Exit(1)
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

					// Check if reader is an SRT connection
					if srtconn, ok := r.(srt.Conn); ok {
						connections = append(connections, srtconn)
					}

					// Check if writer is an SRT connection (and not NullWriter)
					if srtconn, ok := w.(srt.Conn); ok {
						connections = append(connections, srtconn)
					}

					// Create labeler function for client (reader/writer labels)
					labeler := func(index int, total int) string {
						if index == 0 && total == 2 {
							return "reader"
						} else if index == 1 && total == 2 {
							return "writer"
						} else if total == 1 {
							// Single connection - determine if reader or writer
							if _, ok := r.(srt.Conn); ok {
								return "reader"
							}
							return "writer"
						}
						return ""
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
	// Shows receive stats: bytes, packets, success count, loss count, and retransmits
	wg.Add(1)
	go func() {
		defer wg.Done()
		common.RunThroughputDisplayWithLabel(ctx, STATS_PERIOD, "SUB", func() (uint64, uint64, uint64, uint64, uint64) {
			// Get loss and retransmit counts from the actual connection's metrics (if available)
			var loss, retrans uint64
			if socketId := connSocketId.Load(); socketId != 0 {
				// Query the actual connection metrics
				conns, _ := metrics.GetConnections()
				if connMetrics, ok := conns[socketId]; ok && connMetrics != nil {
					loss = connMetrics.CongestionRecvPktLoss.Load()
					retrans = connMetrics.CongestionRecvPktRetrans.Load()
				}
			}
			return clientMetrics.ByteRecvDataSuccess.Load(),
				clientMetrics.PktRecvDataSuccess.Load(),
				clientMetrics.PktRecvDataSuccess.Load(), // Use data packets for success count
				loss,
				retrans
		})
	}()

	// ============================================================
	// Main Read/Write Loop
	// ============================================================
	// Buffered channel prevents goroutine blocking if main receives ctx.Done() first
	doneChan := make(chan error, 10)

	wg.Add(1)
	go func() {
		defer wg.Done()

		buffer := make([]byte, CHANNEL_SIZE)

		// Check if reader is an SRT connection (supports SetReadDeadline)
		var srtConn srt.Conn
		if conn, ok := r.(srt.Conn); ok {
			srtConn = conn
		}

		for {
			// Check context cancellation first
			select {
			case <-ctx.Done():
				// Context cancelled - exit gracefully
				doneChan <- nil
				return
			default:
			}

			// Set read deadline to allow periodic context checks
			// This prevents Read() from blocking indefinitely
			if srtConn != nil {
				srtConn.SetReadDeadline(time.Now().Add(2 * time.Second))
			}

			// Perform the read operation
			n, err := r.Read(buffer)

			// Handle read result
			if err != nil {
				// Check if context was cancelled
				select {
				case <-ctx.Done():
					// Context cancelled - exit gracefully (don't report error)
					doneChan <- nil
					return
				default:
				}

				// Check if error is a timeout (expected - allows context check)
				if errors.Is(err, os.ErrDeadlineExceeded) {
					// Timeout occurred - continue loop to check context again
					continue
				}

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
						// Check if it's a timeout error
						if errors.Is(opErr.Err, os.ErrDeadlineExceeded) {
							// Timeout occurred - continue loop to check context again
							continue
						}
					}
				}

				// Other error - report it
				doneChan <- fmt.Errorf("read: %w", err)
				return
			}

			// Lock-free atomic increments for throughput tracking
			clientMetrics.ByteRecvDataSuccess.Add(uint64(n))
			clientMetrics.PktRecvDataSuccess.Add(1)

			// Check context cancellation before write
			select {
			case <-ctx.Done():
				// Context cancelled - exit gracefully
				doneChan <- nil
				return
			default:
			}

			if _, err := w.Write(buffer[:n]); err != nil {
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
		}
	}()

	// ============================================================
	// Wait for Completion or Context Cancellation
	// ============================================================
	select {
	case err := <-doneChan:
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Fprint(os.Stderr, "\n")
		}
	case <-ctx.Done():
		// Context cancelled - graceful shutdown
		fmt.Fprintf(os.Stderr, "\nShutdown signal received\n")
	}

	// ============================================================
	// Cleanup
	// ============================================================
	// Close connections
	w.Close()
	r.Close()

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
		fmt.Fprintf(os.Stderr, "Graceful shutdown complete\n")
	case <-time.After(config.ShutdownDelay):
		fmt.Fprintf(os.Stderr, "Shutdown timed out after %s\n", config.ShutdownDelay)
	}
}

// NullWriter is an io.WriteCloser that discards all data.
// Useful for profiling and testing SRT connections without output overhead.
type NullWriter struct{}

func (n *NullWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func (n *NullWriter) Close() error {
	return nil
}

func openReader(addr string, logger srt.Logger, ctx context.Context, wg *sync.WaitGroup) (io.ReadCloser, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("the address must not be empty")
	}

	if addr == "-" {
		if os.Stdin == nil {
			return nil, fmt.Errorf("stdin is not defined")
		}

		return os.Stdin, nil
	}

	if strings.HasPrefix(addr, "debug://") {
		readerOptions := DebugReaderOptions{}
		parts := strings.SplitN(strings.TrimPrefix(addr, "debug://"), "?", 2)
		if len(parts) > 1 {
			options, err := url.ParseQuery(parts[1])
			if err != nil {
				return nil, err
			}

			if x, err := strconv.ParseUint(options.Get("bitrate"), 10, 64); err == nil {
				readerOptions.Bitrate = x
			}
		}

		r, err := NewDebugReader(readerOptions)

		return r, err
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	if u.Scheme == "srt" {
		config := srt.DefaultConfig()
		if err := config.UnmarshalQuery(u.RawQuery); err != nil {
			return nil, err
		}
		// Apply CLI flags (they override URL query parameters)
		common.ApplyFlagsToConfig(&config)
		config.Logger = logger

		mode := u.Query().Get("mode")

		if mode == "listener" {
			ln, err := srt.Listen(ctx, "srt", u.Host, config, wg)
			if err != nil {
				return nil, err
			}

			for {
				req, err := ln.Accept2()
				if err != nil {
					return nil, err
				}

				if config.StreamId != req.StreamId() {
					req.Reject(srt.REJ_PEER)
					continue
				}

				if err := req.SetPassphrase(config.Passphrase); err != nil {
					req.Reject(srt.REJ_BADSECRET)
					continue
				}

				conn, err := req.Accept()
				if err != nil {
					continue
				}

				return conn, nil
			}
		} else if mode == "caller" || mode == "" {
			// Default to caller mode if mode not specified
			// Stream ID is set in config via UnmarshalQuery (from URL query parameter)
			conn, err := srt.Dial(ctx, "srt", u.Host, config, wg)
			if err != nil {
				return nil, err
			}

			return conn, nil
		} else {
			return nil, fmt.Errorf("unsupported mode: %s", mode)
		}
	} else if u.Scheme == "udp" {
		laddr, err := net.ResolveUDPAddr("udp", u.Host)
		if err != nil {
			return nil, err
		}

		conn, err := net.ListenUDP("udp", laddr)
		if err != nil {
			return nil, err
		}

		return conn, nil
	}

	return nil, fmt.Errorf("unsupported reader")
}

func openWriter(addr string, logger srt.Logger, ctx context.Context, wg *sync.WaitGroup) (io.WriteCloser, error) {
	// Handle no-output mode: empty string, "null", or "discard"
	if len(addr) == 0 || addr == "null" || addr == "discard" {
		return &common.NullWriter{}, nil
	}

	if addr == "-" {
		if os.Stdout == nil {
			return nil, fmt.Errorf("stdout is not defined")
		}

		// Check if io_uring output is requested (Linux only, uses unsafe)
		if *common.IoUringOutput {
			if !common.IoUringOutputAvailable() {
				return nil, fmt.Errorf("io_uring output is only available on Linux")
			}
			return common.NewIoUringStdoutWriter()
		}

		// Use DirectWriter for stdout - zero locks, direct syscall
		return common.NewStdoutWriter(), nil
	}

	if strings.HasPrefix(addr, "file://") {
		path := strings.TrimPrefix(addr, "file://")

		// Check if io_uring output is requested (Linux only, uses unsafe)
		if *common.IoUringOutput {
			if !common.IoUringOutputAvailable() {
				return nil, fmt.Errorf("io_uring output is only available on Linux")
			}
			// Create file first, then wrap with io_uring writer
			f, err := os.Create(path)
			if err != nil {
				return nil, err
			}
			return common.NewIoUringFileWriter(int(f.Fd()))
		}

		// Use DirectWriter for file output - zero locks, direct syscall
		return common.NewFileWriter(path)
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	if u.Scheme == "srt" {
		config := srt.DefaultConfig()
		if err := config.UnmarshalQuery(u.RawQuery); err != nil {
			return nil, err
		}
		// Apply CLI flags (they override URL query parameters)
		common.ApplyFlagsToConfig(&config)
		config.Logger = logger

		mode := u.Query().Get("mode")

		if mode == "listener" {
			ln, err := srt.Listen(ctx, "srt", u.Host, config, wg)
			if err != nil {
				return nil, err
			}

			for {
				req, err := ln.Accept2()
				if err != nil {
					return nil, err
				}

				if config.StreamId != req.StreamId() {
					req.Reject(srt.REJ_PEER)
					continue
				}

				if err := req.SetPassphrase(config.Passphrase); err != nil {
					req.Reject(srt.REJ_BADSECRET)
					continue
				}

				conn, err := req.Accept()
				if err != nil {
					continue
				}

				return conn, nil
			}
		} else if mode == "caller" || mode == "" {
			// Default to caller mode if mode not specified
			// Stream ID is set in config via UnmarshalQuery (from URL query parameter)
			conn, err := srt.Dial(ctx, "srt", u.Host, config, wg)
			if err != nil {
				return nil, err
			}

			return conn, nil
		} else {
			return nil, fmt.Errorf("unsupported mode: %s", mode)
		}
	} else if u.Scheme == "udp" {
		raddr, err := net.ResolveUDPAddr("udp", u.Host)
		if err != nil {
			return nil, err
		}

		conn, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			return nil, err
		}

		return conn, nil
	}

	return nil, fmt.Errorf("unsupported writer")
}
