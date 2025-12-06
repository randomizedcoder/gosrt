package common

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/datarhei/gosrt/metrics"
)

// StartMetricsServers starts Prometheus metrics endpoint(s) based on the provided flags.
//
// This follows the exact pattern from context_and_cancellation_new_design.md:
// - Shutdown watcher goroutine: fire-and-forget (NO wg.Add), listens for ctx.Done()
// - Server goroutine: tracked with wg.Add(1) and defer wg.Done()
//
// Parameters:
//   - ctx: Parent context - when cancelled, triggers graceful shutdown of servers
//   - wg: WaitGroup to track server goroutines
//   - httpAddr: TCP address (e.g., ":9090") - empty string means no TCP listener
//   - udsPath: Unix socket path - empty string means no UDS listener
//
// If both httpAddr and udsPath are empty, this function does nothing (no listeners opened).
func StartMetricsServers(ctx context.Context, wg *sync.WaitGroup, httpAddr, udsPath string) error {
	handler := metrics.MetricsHandler()

	// Start TCP HTTP endpoint if configured
	if httpAddr != "" {
		if err := startHTTPMetricsServer(ctx, wg, httpAddr, handler); err != nil {
			return fmt.Errorf("failed to start HTTP metrics server: %w", err)
		}
	}

	// Start UDS endpoint if configured
	if udsPath != "" {
		if err := startUDSMetricsServer(ctx, wg, udsPath, handler); err != nil {
			return fmt.Errorf("failed to start UDS metrics server: %w", err)
		}
	}

	return nil
}

// startHTTPMetricsServer starts a TCP HTTP metrics server.
// Follows the pattern from context_and_cancellation_new_design.md section
// "Prometheus HTTP Server Shutdown Pattern".
func startHTTPMetricsServer(ctx context.Context, wg *sync.WaitGroup, addr string, handler http.Handler) error {
	promSrv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	// Shutdown watcher - triggers clean shutdown when context cancelled
	// NOTE: This is fire-and-forget (no wg.Add) per the design pattern
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := promSrv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "Prometheus HTTP server shutdown error: %v\n", err)
		}
	}()

	// Run Prometheus server in goroutine with waitgroup tracking
	wg.Add(1)
	go func() {
		defer wg.Done()
		fmt.Fprintf(os.Stderr, "Prometheus metrics HTTP server started on %s\n", addr)
		if err := promSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Prometheus HTTP server error: %v\n", err)
		}
	}()

	return nil
}

// startUDSMetricsServer starts a Unix Domain Socket metrics server.
// Same pattern as HTTP, but uses net.Listen("unix", path) and cleans up socket file.
func startUDSMetricsServer(ctx context.Context, wg *sync.WaitGroup, socketPath string, handler http.Handler) error {
	// Remove existing socket file if it exists (stale from previous run)
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing socket file: %w", err)
	}

	// Create Unix socket listener
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to create Unix socket listener: %w", err)
	}

	promSrv := &http.Server{
		Handler: handler,
	}

	// Shutdown watcher - triggers clean shutdown when context cancelled
	// Also cleans up the socket file
	// NOTE: This is fire-and-forget (no wg.Add) per the design pattern
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := promSrv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "Prometheus UDS server shutdown error: %v\n", err)
		}
		// Clean up socket file after server shutdown
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Failed to remove socket file %s: %v\n", socketPath, err)
		}
	}()

	// Run Prometheus server in goroutine with waitgroup tracking
	wg.Add(1)
	go func() {
		defer wg.Done()
		fmt.Fprintf(os.Stderr, "Prometheus metrics UDS server started on %s\n", socketPath)
		if err := promSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Prometheus UDS server error: %v\n", err)
		}
	}()

	return nil
}


