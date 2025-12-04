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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	srt "github.com/datarhei/gosrt"
	"github.com/datarhei/gosrt/contrib/common"
	"github.com/pkg/profile"
)

const (
	STATS_PERIOD = 200 * time.Millisecond
	CHANNEL_SIZE = 2048
)

type stats struct {
	bprev  uint64
	btotal uint64
	prev   uint64
	total  uint64

	lock sync.Mutex

	period time.Duration
	last   time.Time
}

func (s *stats) init(period time.Duration) {
	s.bprev = 0
	s.btotal = 0
	s.prev = 0
	s.total = 0

	s.period = period
	s.last = time.Now()

	go s.tick()
}

func (s *stats) tick() {
	ticker := time.NewTicker(s.period)
	defer ticker.Stop()

	for c := range ticker.C {
		s.lock.Lock()
		diff := c.Sub(s.last)

		bavg := float64(s.btotal-s.bprev) * 8 / (1000 * 1000 * diff.Seconds())
		avg := float64(s.total-s.prev) / diff.Seconds()

		s.bprev = s.btotal
		s.prev = s.total
		s.last = c

		s.lock.Unlock()

		fmt.Fprintf(os.Stderr, "\r%-54s: %8.3f kpackets (%8.3f packets/s), %8.3f mbytes (%8.3f Mbps)", c, float64(s.total)/1024, avg, float64(s.btotal)/1024/1024, bavg)
	}
}

func (s *stats) update(n uint64) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.btotal += n
	s.total++
}

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

	go func() {
		if logger == nil {
			return
		}

		for m := range logger.Listen() {
			fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n", m.SocketId, m.Topic, m.File, m.Line, m.Message)
		}
	}()

	// Create root context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create root waitgroup for tracking all shutdown operations
	var shutdownWg sync.WaitGroup
	shutdownWg.Add(1) // Increment for client shutdown

	// Setup signal handler that cancels context (Option 3: Context-Driven Shutdown)
	setupSignalHandler(ctx, cancel)

	r, err := openReader(*from, logger, ctx, &shutdownWg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: from: %v\n", err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	w, err := openWriter(*to, logger, ctx, &shutdownWg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: to: %v\n", err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Get config to check for statistics interval
	config := srt.DefaultConfig()
	common.ApplyFlagsToConfig(&config)

	// Start periodic statistics printing if enabled
	if config.StatisticsPrintInterval > 0 {
		go func() {
			ticker := time.NewTicker(config.StatisticsPrintInterval)
			defer ticker.Stop()

			for range ticker.C {
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
		}()
	}

	doneChan := make(chan error)

	go func() {
		buffer := make([]byte, CHANNEL_SIZE)

		s := stats{}
		s.init(STATS_PERIOD)

		for {
			select {
			case <-ctx.Done():
				// Context cancelled - exit gracefully
				return
			default:
				n, err := r.Read(buffer)
				if err != nil {
					doneChan <- fmt.Errorf("read: %w", err)
					return
				}

				s.update(uint64(n))

				if _, err := w.Write(buffer[:n]); err != nil {
					doneChan <- fmt.Errorf("write: %w", err)
					return
				}
			}
		}
	}()

	// Wait for either error or context cancellation
	select {
	case err := <-doneChan:
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Fprint(os.Stderr, "\n")
		}
	case <-ctx.Done():
		// Context cancelled - graceful shutdown
		fmt.Fprint(os.Stderr, "\n")
	}

	// Explicitly stop profiling before exiting to ensure profile is written
	if prof != nil {
		prof.Stop()
	}

	// Wait for graceful shutdown to complete (with timeout as safety net)
	done := make(chan struct{})
	go func() {
		shutdownWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All shutdown operations completed
	case <-time.After(config.ShutdownDelay):
		// Timeout - proceed with exit (safety net)
	}

	w.Close()

	// Only print writer stats if it's an SRT connection (not NullWriter)
	if srtconn, ok := w.(srt.Conn); ok {
		stats := &srt.Statistics{}
		srtconn.Stats(stats)

		data, err := json.MarshalIndent(stats, "", "   ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "writer: %+v\n", stats)
		} else {
			fmt.Fprintf(os.Stderr, "writer: %s\n", string(data))
		}
	} else if _, ok := w.(*NullWriter); ok {
		// NullWriter - no stats to print, but indicate output was discarded
		fmt.Fprintf(os.Stderr, "writer: output discarded (null mode)\n")
	}

	r.Close()

	if srtconn, ok := r.(srt.Conn); ok {
		stats := &srt.Statistics{}
		srtconn.Stats(stats)

		data, err := json.MarshalIndent(stats, "", "   ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "reader: %+v\n", stats)
		} else {
			fmt.Fprintf(os.Stderr, "reader: %s\n", string(data))
		}
	}

	if logger != nil {
		logger.Close()
	}
}

// setupSignalHandler sets up OS signal handling to cancel the root context
// Option 3: Context-Driven Shutdown - signal handler only cancels context
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-sigChan:
			// Signal received - cancel root context to initiate graceful shutdown
			// Components will detect context cancellation and shutdown automatically
			cancel()
		case <-ctx.Done():
			// Context already cancelled - exit
			return
		}
	}()
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

func openReader(addr string, logger srt.Logger, ctx context.Context, shutdownWg *sync.WaitGroup) (io.ReadCloser, error) {
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
			ln, err := srt.Listen("srt", u.Host, config, ctx, shutdownWg)
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
		} else if mode == "caller" {
			conn, err := srt.Dial("srt", u.Host, config)
			if err != nil {
				return nil, err
			}

			return conn, nil
		} else {
			return nil, fmt.Errorf("unsupported mode")
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

func openWriter(addr string, logger srt.Logger, ctx context.Context, shutdownWg *sync.WaitGroup) (io.WriteCloser, error) {
	// Handle no-output mode: empty string, "null", or "discard"
	if len(addr) == 0 || addr == "null" || addr == "discard" {
		return &NullWriter{}, nil
	}

	if addr == "-" {
		if os.Stdout == nil {
			return nil, fmt.Errorf("stdout is not defined")
		}

		return NewNonblockingWriter(os.Stdout, 2048), nil
	}

	if strings.HasPrefix(addr, "file://") {
		path := strings.TrimPrefix(addr, "file://")
		file, err := os.Create(path)
		if err != nil {
			return nil, err
		}

		return NewNonblockingWriter(file, 2048), nil
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
			ln, err := srt.Listen("srt", u.Host, config, ctx, shutdownWg)
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
		} else if mode == "caller" {
			conn, err := srt.Dial("srt", u.Host, config)
			if err != nil {
				return nil, err
			}

			return conn, nil
		} else {
			return nil, fmt.Errorf("unsupported mode")
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
