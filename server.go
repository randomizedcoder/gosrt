package srt

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/datarhei/gosrt/metrics"
)

// Server is a framework for a SRT server
type Server struct {
	// The address the SRT server should listen on, e.g. ":6001".
	Addr string

	// Config is the configuration for a SRT listener.
	Config *Config

	// HandleConnect will be called for each incoming connection. This
	// allows you to implement your own interpretation of the streamid
	// and authorization. If this is nil, all connections will be
	// rejected.
	HandleConnect AcceptFunc

	// HandlePublish will be called for a publishing connection.
	HandlePublish func(conn Conn)

	// HandlePublish will be called for a subscribing connection.
	HandleSubscribe func(conn Conn)

	// Context is the root context for the server. When cancelled, the server will shutdown gracefully.
	Context context.Context

	// ShutdownWg is the root waitgroup for tracking all shutdown operations.
	// When this waitgroup reaches zero, all shutdown operations are complete.
	ShutdownWg *sync.WaitGroup

	ln           Listener
	listenerWg   sync.WaitGroup // Waitgroup for listener shutdown
	metricsServer *http.Server // Optional metrics HTTP server
	metricsServerOnce sync.Once // Ensure metrics server is started only once
}

// ErrServerClosed is returned when the server is about to shutdown.
var ErrServerClosed = errors.New("srt: server closed")

// ListenAndServe starts the SRT server. It blocks until an error happens.
// If the error is ErrServerClosed the server has shutdown normally.
func (s *Server) ListenAndServe() error {
	err := s.Listen()
	if err != nil {
		return err
	}

	return s.Serve()
}

// Listen opens the server listener.
// It returns immediately after the listener is ready.
func (s *Server) Listen() error {
	// Set some defaults if required.
	if s.HandlePublish == nil {
		s.HandlePublish = s.defaultHandler
	}

	if s.HandleSubscribe == nil {
		s.HandleSubscribe = s.defaultHandler
	}

	if s.Config == nil {
		config := DefaultConfig()
		s.Config = &config
	}

	// Increment listener waitgroup
	s.listenerWg.Add(1)

	// Start listening for incoming connections.
	// Pass context and waitgroup to listener
	ln, err := Listen("srt", s.Addr, *s.Config, s.Context, &s.listenerWg)
	if err != nil {
		s.listenerWg.Done() // Decrement on error
		return err
	}

	s.ln = ln

	// Start metrics server if enabled
	if s.Config != nil && s.Config.MetricsEnabled && s.Config.MetricsListenAddr != "" {
		s.startMetricsServer()
	}

	return err
}

// Serve starts accepting connections. It must be called after Listen().
// It blocks until an error happens.
// If the error is ErrServerClosed the server has shutdown normally.
// Option 3: Context-Driven Shutdown - Serve() watches context and automatically calls Shutdown() when cancelled.
func (s *Server) Serve() error {
	for {
		// Check for context cancellation first (Option 3: Context-Driven Shutdown)
		if s.Context != nil {
			select {
			case <-s.Context.Done():
				// Context cancelled - shutdown automatically
				s.Shutdown()
				return ErrServerClosed
			default:
			}
		}

		// Wait for connections.
		req, err := s.ln.Accept2()
		if err != nil {
			if err == ErrListenerClosed {
				return ErrServerClosed
			}

			return err
		}

		if s.HandleConnect == nil {
			req.Reject(REJ_PEER)
			continue
		}

		go func(req ConnRequest) {
			mode := s.HandleConnect(req)
			if mode == REJECT {
				req.Reject(REJ_PEER)
				return
			}

			conn, err := req.Accept()
			if err != nil {
				// rejected connection, ignore
				return
			}

			if mode == PUBLISH {
				s.HandlePublish(conn)
			} else {
				s.HandleSubscribe(conn)
			}
		}(req)
	}
}

// startMetricsServer starts an HTTP server for Prometheus metrics
func (s *Server) startMetricsServer() {
	s.metricsServerOnce.Do(func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.MetricsHandler())

		s.metricsServer = &http.Server{
			Addr:    s.Config.MetricsListenAddr,
			Handler: mux,
		}

		go func() {
			if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				// Log error (if logger available)
				// For now, silently ignore - metrics are optional
			}
		}()
	})
}

// Shutdown will shutdown the server. ListenAndServe will return a ErrServerClosed
func (s *Server) Shutdown() {
	if s.ln == nil {
		// No listener - just notify root waitgroup if set
		if s.ShutdownWg != nil {
			s.ShutdownWg.Done()
		}
		return
	}

	// Close the listener (this will trigger listener shutdown)
	s.ln.Close()

	// Wait for listener to shutdown
	done := make(chan struct{})
	go func() {
		s.listenerWg.Wait()
		close(done)
	}()

	// Use config shutdown delay as timeout, or default to 5 seconds
	timeout := 5 * time.Second
	if s.Config != nil && s.Config.ShutdownDelay > 0 {
		timeout = s.Config.ShutdownDelay
	}

	select {
	case <-done:
		// Listener shutdown complete
	case <-time.After(timeout):
		// Timeout - log warning but continue
		// Note: In production, we might want to log this, but logger might not be available
	}

	// Notify root waitgroup
	if s.ShutdownWg != nil {
		s.ShutdownWg.Done()
	}
}

func (s *Server) defaultHandler(conn Conn) {
	// Close the incoming connection
	conn.Close()
}

// GetConnections returns all active connections from the listener.
// This is safe to call concurrently and returns a snapshot of connections.
func (s *Server) GetConnections() []Conn {
	if s.ln == nil {
		return nil
	}

	// Use type assertion to access the internal listener
	// The listener interface doesn't expose conns, so we need to access it via type assertion
	// This is safe because we control the implementation
	type listenerWithConns interface {
		getConnections() []Conn
	}

	if ln, ok := s.ln.(listenerWithConns); ok {
		return ln.getConnections()
	}

	return nil
}
