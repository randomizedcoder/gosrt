package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
)

// MetricsServer serves Prometheus metrics over a Unix domain socket.
// This allows the Orchestrator to scrape metrics from the client-seeker
// even when running in isolated network namespaces.
type MetricsServer struct {
	socketPath string
	bm         *BitrateManager
	gen        *DataGenerator
	pub        *Publisher
	cs         *ControlServer
	watchdog   *Watchdog
	startTime  time.Time

	listener net.Listener
	server   *http.Server
	stopOnce sync.Once
}

// NewMetricsServer creates a new metrics server.
func NewMetricsServer(
	socketPath string,
	bm *BitrateManager,
	gen *DataGenerator,
	pub *Publisher,
	cs *ControlServer,
	watchdog *Watchdog,
) *MetricsServer {
	return &MetricsServer{
		socketPath: socketPath,
		bm:         bm,
		gen:        gen,
		pub:        pub,
		cs:         cs,
		watchdog:   watchdog,
		startTime:  time.Now(),
	}
}

// Start begins serving metrics on the Unix socket.
func (ms *MetricsServer) Start(ctx context.Context) error {
	// Remove existing socket if present
	if err := os.Remove(ms.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing socket: %w", err)
	}

	// Create listener using context-aware ListenConfig
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "unix", ms.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", ms.socketPath, err)
	}
	ms.listener = listener

	// Create HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", ms.metricsHandler)
	mux.HandleFunc("/health", ms.healthHandler)

	ms.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		if serveErr := ms.server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "metrics server error: %v\n", serveErr)
		}
	}()

	// Handle shutdown
	go func() {
		<-ctx.Done()
		ms.Stop(ctx)
	}()

	return nil
}

// metricsHandler serves Prometheus-format metrics.
func (ms *MetricsServer) metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	// Use a helper that stops on first error (client disconnect)
	var writeErr error
	write := func(format string, a ...interface{}) {
		if writeErr != nil {
			return
		}
		_, writeErr = fmt.Fprintf(w, format, a...)
	}

	// Client-seeker specific metrics
	write("# HELP client_seeker_current_bitrate_bps Current bitrate in bits per second\n")
	write("# TYPE client_seeker_current_bitrate_bps gauge\n")
	write("client_seeker_current_bitrate_bps %d\n", ms.bm.Current())

	write("# HELP client_seeker_target_bitrate_bps Target bitrate in bits per second\n")
	write("# TYPE client_seeker_target_bitrate_bps gauge\n")
	write("client_seeker_target_bitrate_bps %d\n", ms.bm.Target())

	if ms.gen != nil {
		packets, bytes := ms.gen.Stats()
		write("# HELP client_seeker_packets_generated_total Total packets generated\n")
		write("# TYPE client_seeker_packets_generated_total counter\n")
		write("client_seeker_packets_generated_total %d\n", packets)

		write("# HELP client_seeker_bytes_generated_total Total bytes generated\n")
		write("# TYPE client_seeker_bytes_generated_total counter\n")
		write("client_seeker_bytes_generated_total %d\n", bytes)

		write("# HELP client_seeker_actual_bitrate_bps Measured bitrate in bits per second\n")
		write("# TYPE client_seeker_actual_bitrate_bps gauge\n")
		write("client_seeker_actual_bitrate_bps %.0f\n", ms.gen.ActualBitrate())

		// Generator efficiency metric (key for bottleneck detection)
		genStats := ms.gen.DetailedStats()
		write("# HELP client_seeker_generator_efficiency Ratio of actual/target bitrate (< 0.95 indicates bottleneck)\n")
		write("# TYPE client_seeker_generator_efficiency gauge\n")
		write("client_seeker_generator_efficiency %.4f\n", genStats.Efficiency)
	}

	// TokenBucket metrics (for bottleneck detection)
	if ms.bm != nil {
		tbStats := ms.bm.Bucket().DetailedStats()

		write("# HELP client_seeker_tokenbucket_wait_seconds_total Total time waiting for tokens\n")
		write("# TYPE client_seeker_tokenbucket_wait_seconds_total counter\n")
		write("client_seeker_tokenbucket_wait_seconds_total %.6f\n", float64(tbStats.TotalWaitNs)/1e9)

		write("# HELP client_seeker_tokenbucket_spin_seconds_total Time spent in spin-wait loops\n")
		write("# TYPE client_seeker_tokenbucket_spin_seconds_total counter\n")
		write("client_seeker_tokenbucket_spin_seconds_total %.6f\n", float64(tbStats.SpinTimeNs)/1e9)

		write("# HELP client_seeker_tokenbucket_consume_total Total consume calls\n")
		write("# TYPE client_seeker_tokenbucket_consume_total counter\n")
		write("client_seeker_tokenbucket_consume_total %d\n", tbStats.WaitCount)

		write("# HELP client_seeker_tokenbucket_blocked_total Times consume had to wait\n")
		write("# TYPE client_seeker_tokenbucket_blocked_total counter\n")
		write("client_seeker_tokenbucket_blocked_total %d\n", tbStats.BlockedCount)

		write("# HELP client_seeker_tokenbucket_tokens Current tokens available\n")
		write("# TYPE client_seeker_tokenbucket_tokens gauge\n")
		write("client_seeker_tokenbucket_tokens %d\n", tbStats.TokensAvailable)

		write("# HELP client_seeker_tokenbucket_tokens_max Maximum token capacity\n")
		write("# TYPE client_seeker_tokenbucket_tokens_max gauge\n")
		write("client_seeker_tokenbucket_tokens_max %d\n", tbStats.TokensMax)

		write("# HELP client_seeker_tokenbucket_mode Token bucket mode (sleep=0, hybrid=1, spin=2)\n")
		write("# TYPE client_seeker_tokenbucket_mode gauge\n")
		modeVal := 0
		switch tbStats.Mode {
		case "hybrid":
			modeVal = 1
		case "spin":
			modeVal = 2
		}
		write("client_seeker_tokenbucket_mode %d\n", modeVal)
	}

	if ms.pub != nil {
		packets, bytes, naks := ms.pub.Stats()
		write("# HELP client_seeker_packets_sent_total Total packets sent over SRT\n")
		write("# TYPE client_seeker_packets_sent_total counter\n")
		write("client_seeker_packets_sent_total %d\n", packets)

		write("# HELP client_seeker_bytes_sent_total Total bytes sent over SRT\n")
		write("# TYPE client_seeker_bytes_sent_total counter\n")
		write("client_seeker_bytes_sent_total %d\n", bytes)

		write("# HELP client_seeker_naks_received_total Total NAKs received\n")
		write("# TYPE client_seeker_naks_received_total counter\n")
		write("client_seeker_naks_received_total %d\n", naks)

		write("# HELP client_seeker_connection_alive Connection status (1=alive, 0=dead)\n")
		write("# TYPE client_seeker_connection_alive gauge\n")
		if ms.pub.IsAlive() {
			write("client_seeker_connection_alive 1\n")
		} else {
			write("client_seeker_connection_alive 0\n")
		}

		// Publisher write metrics (for bottleneck detection)
		pubStats := ms.pub.DetailedStats()
		write("# HELP client_seeker_srt_write_seconds_total Total time in SRT Write() calls\n")
		write("# TYPE client_seeker_srt_write_seconds_total counter\n")
		write("client_seeker_srt_write_seconds_total %.6f\n", float64(pubStats.WriteTimeNs)/1e9)

		write("# HELP client_seeker_srt_write_total Total Write() calls\n")
		write("# TYPE client_seeker_srt_write_total counter\n")
		write("client_seeker_srt_write_total %d\n", pubStats.WriteCount)

		write("# HELP client_seeker_srt_write_blocked_total Times Write() blocked (> 1ms)\n")
		write("# TYPE client_seeker_srt_write_blocked_total counter\n")
		write("client_seeker_srt_write_blocked_total %d\n", pubStats.WriteBlockedCount)

		write("# HELP client_seeker_srt_write_errors_total Write errors\n")
		write("# TYPE client_seeker_srt_write_errors_total counter\n")
		write("client_seeker_srt_write_errors_total %d\n", pubStats.WriteErrorCount)
	}

	if ms.cs != nil {
		write("# HELP client_seeker_heartbeat_age_seconds Seconds since last heartbeat\n")
		write("# TYPE client_seeker_heartbeat_age_seconds gauge\n")
		write("client_seeker_heartbeat_age_seconds %.3f\n", ms.cs.TimeSinceHeartbeat().Seconds())
	}

	if ms.watchdog != nil {
		write("# HELP client_seeker_watchdog_state Watchdog state (0=normal, 1=warning, 2=critical)\n")
		write("# TYPE client_seeker_watchdog_state gauge\n")
		write("client_seeker_watchdog_state %d\n", ms.watchdog.State())
	}

	write("# HELP client_seeker_uptime_seconds Uptime in seconds\n")
	write("# TYPE client_seeker_uptime_seconds gauge\n")
	write("client_seeker_uptime_seconds %.3f\n", time.Since(ms.startTime).Seconds())

	// Stop if client disconnected
	if writeErr != nil {
		return
	}

	// Also include standard gosrt metrics via the handler
	write("\n# Standard gosrt metrics\n")
	handler := metrics.MetricsHandler()
	// Create a response writer wrapper that writes to our writer
	handler.ServeHTTP(&metricsResponseWriter{w: w}, r)
}

// metricsResponseWriter wraps an http.ResponseWriter to capture output
type metricsResponseWriter struct {
	w http.ResponseWriter
}

func (m *metricsResponseWriter) Header() http.Header {
	return m.w.Header()
}

func (m *metricsResponseWriter) Write(b []byte) (int, error) {
	return m.w.Write(b)
}

func (m *metricsResponseWriter) WriteHeader(statusCode int) {
	// Don't write header again - we already wrote it
}

// healthHandler returns health status.
func (ms *MetricsServer) healthHandler(w http.ResponseWriter, _ *http.Request) {
	healthy := true
	status := "healthy"

	if ms.pub != nil && !ms.pub.IsAlive() {
		healthy = false
		status = "unhealthy: connection dead"
	}

	if ms.watchdog != nil && !ms.watchdog.IsHealthy() {
		healthy = false
		status = fmt.Sprintf("unhealthy: watchdog %s", ms.watchdog.StateString())
	}

	if healthy {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	// Write error is typically client disconnect - log for visibility
	if _, err := fmt.Fprintf(w, "%s\n", status); err != nil {
		fmt.Fprintf(os.Stderr, "health check write error (client disconnect?): %v\n", err)
	}
}

// Stop gracefully shuts down the metrics server.
// The provided context is used as the base for the shutdown timeout.
// If ctx is already canceled (e.g., during signal handling), a fresh
// background context is used for the shutdown timeout.
func (ms *MetricsServer) Stop(ctx context.Context) {
	ms.stopOnce.Do(func() {
		if ms.server != nil {
			// If the context is already canceled, use a fresh one for shutdown
			baseCtx := ctx
			if ctx.Err() != nil {
				baseCtx = context.Background()
			}
			shutdownCtx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
			defer cancel()
			if err := ms.server.Shutdown(shutdownCtx); err != nil {
				fmt.Fprintf(os.Stderr, "metrics server shutdown error: %v\n", err)
			}
		}
		if ms.listener != nil {
			if err := ms.listener.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "metrics listener close error: %v\n", err)
			}
		}
		if err := os.Remove(ms.socketPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "metrics socket cleanup error: %v\n", err)
		}
	})
}

// SocketPath returns the path to the metrics socket.
func (ms *MetricsServer) SocketPath() string {
	return ms.socketPath
}
