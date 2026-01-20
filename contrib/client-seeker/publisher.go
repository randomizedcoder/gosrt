package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	srt "github.com/randomizedcoder/gosrt"
	"github.com/randomizedcoder/gosrt/contrib/common"
)

// Publisher manages the SRT connection and sends data from the generator.
type Publisher struct {
	targetURL string
	conn      srt.Conn
	config    srt.Config
	wg        *sync.WaitGroup

	// Connection state
	connectionAlive atomic.Bool
	lastError       atomic.Value // stores error

	// Stats
	packetsSent atomic.Uint64
	bytesSent   atomic.Uint64
	nakCount    atomic.Uint64

	// Instrumentation for bottleneck detection
	// See: client_seeker_instrumentation_design.md
	writeTimeNs      atomic.Int64  // Total time spent in Write() calls
	writeCount       atomic.Uint64 // Number of Write() calls
	writeBlockedCount atomic.Uint64 // Times Write() blocked (took > threshold)
	writeErrorCount  atomic.Uint64 // Write errors
}

// PublisherDetailedStats holds detailed statistics for bottleneck detection.
// See: client_seeker_instrumentation_design.md
type PublisherDetailedStats struct {
	// Packet counts
	PacketsSent uint64
	BytesSent   uint64

	// Write timing - key metrics for bottleneck detection
	WriteTimeNs       int64  // Total time in Write() calls
	WriteCount        uint64 // Number of Write() calls
	WriteBlockedCount uint64 // Times Write() blocked
	WriteErrorCount   uint64 // Write errors

	// Connection state
	ConnectionAlive bool
}

// NewPublisher creates a new SRT publisher.
//
// Parameters:
//   - targetURL: SRT URL (srt://host:port/stream)
func NewPublisher(targetURL string) *Publisher {
	p := &Publisher{
		targetURL: targetURL,
		wg:        &sync.WaitGroup{},
	}
	p.connectionAlive.Store(false)
	return p
}

// Connect establishes the SRT connection.
func (p *Publisher) Connect(ctx context.Context) error {
	u, err := url.Parse(p.targetURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "srt" {
		return fmt.Errorf("unsupported scheme: %s (expected srt)", u.Scheme)
	}

	// Build config from flags
	config := srt.DefaultConfig()
	common.ApplyFlagsToConfig(&config)

	// Set stream ID from URL path
	streamID := u.Path
	if streamID == "" {
		streamID = "/"
	}
	// Ensure it starts with "publish:" prefix for server
	if !strings.HasPrefix(streamID, "publish:") {
		streamID = "publish:" + streamID
	}
	config.StreamId = streamID

	p.config = config

	// Dial the server
	conn, err := srt.Dial(ctx, "srt", u.Host, config, p.wg)
	if err != nil {
		p.lastError.Store(err)
		return fmt.Errorf("dial failed: %w", err)
	}

	p.conn = conn
	p.connectionAlive.Store(true)
	return nil
}

// Run starts the main send loop, reading from the generator and writing to SRT.
// This should be called as a goroutine.
func (p *Publisher) Run(ctx context.Context, gen *DataGenerator) error {
	if p.conn == nil {
		return fmt.Errorf("not connected")
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Generate packet (rate-limited by TokenBucket)
		packet, err := gen.Generate(ctx)
		if err != nil {
			// Context cancelled - normal shutdown
			if ctx.Err() != nil {
				return nil
			}
			p.lastError.Store(err)
			return fmt.Errorf("generate: %w", err)
		}

		// Write to SRT connection (instrumented for bottleneck detection)
		writeStart := time.Now()
		n, err := p.conn.Write(packet)
		writeElapsed := time.Since(writeStart)

		// Record write timing
		p.writeTimeNs.Add(writeElapsed.Nanoseconds())
		p.writeCount.Add(1)

		// Track blocked writes (> 1ms is considered "blocked")
		if writeElapsed > time.Millisecond {
			p.writeBlockedCount.Add(1)
		}

		if err != nil {
			p.connectionAlive.Store(false)
			p.lastError.Store(err)
			p.writeErrorCount.Add(1)

			// Check if it's a normal shutdown
			if ctx.Err() != nil {
				return nil
			}

			// Check for expected shutdown errors
			errStr := err.Error()
			if strings.Contains(errStr, "use of closed network connection") ||
				strings.Contains(errStr, "broken pipe") ||
				err == io.EOF {
				return nil
			}

			return fmt.Errorf("write: %w", err)
		}

		// Update stats
		p.packetsSent.Add(1)
		p.bytesSent.Add(uint64(n))
	}
}

// Close closes the SRT connection.
func (p *Publisher) Close() error {
	p.connectionAlive.Store(false)
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// IsAlive returns true if the connection is alive.
func (p *Publisher) IsAlive() bool {
	return p.connectionAlive.Load()
}

// LastError returns the last error that occurred.
func (p *Publisher) LastError() error {
	if v := p.lastError.Load(); v != nil {
		return v.(error)
	}
	return nil
}

// Stats returns current statistics.
func (p *Publisher) Stats() (packets, bytes, naks uint64) {
	return p.packetsSent.Load(), p.bytesSent.Load(), p.nakCount.Load()
}

// Wait waits for all goroutines to finish.
func (p *Publisher) Wait() {
	p.wg.Wait()
}

// WaitWithTimeout waits for goroutines with a timeout.
func (p *Publisher) WaitWithTimeout(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// SocketID returns the SRT socket ID (for metrics lookup).
func (p *Publisher) SocketID() uint32 {
	if p.conn != nil {
		return p.conn.SocketId()
	}
	return 0
}

// PrintStats prints current statistics to stderr.
func (p *Publisher) PrintStats() {
	packets, bytes, _ := p.Stats()
	fmt.Fprintf(os.Stderr, "Publisher stats: packets=%d, bytes=%d, alive=%v\n",
		packets, bytes, p.IsAlive())
}

// DetailedStats returns detailed statistics for bottleneck detection.
// See: client_seeker_instrumentation_design.md
func (p *Publisher) DetailedStats() PublisherDetailedStats {
	return PublisherDetailedStats{
		PacketsSent:       p.packetsSent.Load(),
		BytesSent:         p.bytesSent.Load(),
		WriteTimeNs:       p.writeTimeNs.Load(),
		WriteCount:        p.writeCount.Load(),
		WriteBlockedCount: p.writeBlockedCount.Load(),
		WriteErrorCount:   p.writeErrorCount.Load(),
		ConnectionAlive:   p.connectionAlive.Load(),
	}
}

// ResetStats resets all statistics counters.
// Useful for getting clean measurements during testing.
func (p *Publisher) ResetStats() {
	p.packetsSent.Store(0)
	p.bytesSent.Store(0)
	p.nakCount.Store(0)
	p.writeTimeNs.Store(0)
	p.writeCount.Store(0)
	p.writeBlockedCount.Store(0)
	p.writeErrorCount.Store(0)
}
