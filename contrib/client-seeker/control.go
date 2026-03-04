package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ControlServer handles the Unix domain socket control interface.
// It accepts JSON commands from the Orchestrator and updates the BitrateManager.
//
// The server is intentionally simple - it just executes commands.
// All complex logic (ramping, timing) lives in the Orchestrator.
type ControlServer struct {
	socketPath string
	listener   net.Listener
	bm         *BitrateManager
	gen        *DataGenerator // For resetting stats on bitrate change
	startTime  time.Time

	// Connection state tracking
	connectionAlive atomic.Bool

	// Heartbeat tracking (for watchdog)
	mu            sync.Mutex
	lastHeartbeat time.Time

	// Stats (atomic for thread safety)
	packetsSent atomic.Uint64
	bytesSent   atomic.Uint64

	// Shutdown coordination
	stopOnce sync.Once
	stopped  atomic.Bool
}

// NewControlServer creates a new control server.
//
// Parameters:
//   - socketPath: Path to the Unix domain socket
//   - bm: BitrateManager to control
//   - gen: DataGenerator (optional, for resetting stats on bitrate change)
func NewControlServer(socketPath string, bm *BitrateManager, gen *DataGenerator) *ControlServer {
	now := time.Now()
	cs := &ControlServer{
		socketPath:    socketPath,
		bm:            bm,
		gen:           gen,
		startTime:     now,
		lastHeartbeat: now, // Start with a heartbeat to allow initial setup
	}
	cs.connectionAlive.Store(false)
	return cs
}

// Start begins accepting connections on the control socket.
// This should be called as a goroutine.
//
// The server will:
// 1. Remove any existing socket file
// 2. Create a new Unix socket listener
// 3. Accept connections in a loop
// 4. Handle each connection in a separate goroutine
func (cs *ControlServer) Start(ctx context.Context) error {
	// Remove existing socket if present
	if err := os.Remove(cs.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing socket: %w", err)
	}

	// Create listener using context-aware ListenConfig
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "unix", cs.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", cs.socketPath, err)
	}
	cs.listener = listener

	// Ensure socket is cleaned up on exit
	go func() {
		<-ctx.Done()
		cs.Stop(ctx)
	}()

	// Accept loop
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			// Check if we're shutting down
			if cs.stopped.Load() {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			default:
				// Log error but continue accepting
				fmt.Fprintf(os.Stderr, "control: accept error: %v\n", acceptErr)
				continue
			}
		}

		// Handle connection in goroutine
		go cs.handleConnection(ctx, conn)
	}
}

// handleConnection processes a single client connection.
// Each connection can send multiple commands (one per line).
func (cs *ControlServer) handleConnection(ctx context.Context, conn net.Conn) {
	defer func() {
		if err := conn.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "control: close error: %v\n", err)
		}
	}()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Parse request
		req, parseErr := ParseRequest(line)
		if parseErr != nil {
			resp := NewErrorResponse(parseErr)
			if data, marshalErr := resp.Marshal(); marshalErr != nil {
				fmt.Fprintf(os.Stderr, "control: failed to marshal error response: %v\n", marshalErr)
			} else if _, writeErr := conn.Write(data); writeErr != nil {
				fmt.Fprintf(os.Stderr, "control: failed to write error response: %v\n", writeErr)
			}
			continue
		}

		// Handle command
		resp := cs.handleCommand(req)

		// Send response
		data, marshalErr := resp.Marshal()
		if marshalErr != nil {
			errResp := NewErrorResponse(fmt.Errorf("failed to marshal response: %w", marshalErr))
			if errData, errMarshalErr := errResp.Marshal(); errMarshalErr != nil {
				fmt.Fprintf(os.Stderr, "control: failed to marshal fallback error response: %v\n", errMarshalErr)
			} else {
				data = errData
			}
		}
		if _, writeErr := conn.Write(data); writeErr != nil {
			fmt.Fprintf(os.Stderr, "control: failed to write response: %v\n", writeErr)
		}

		// Check for stop command
		if req.Command == CmdStop {
			return
		}
	}
}

// handleCommand processes a single control command.
func (cs *ControlServer) handleCommand(req *ControlRequest) *ControlResponse {
	switch req.Command {
	case CmdSetBitrate:
		if err := cs.bm.Set(req.Bitrate); err != nil {
			return NewErrorResponse(err)
		}
		// NOTE: We no longer reset stats on bitrate change.
		// The generator now uses a sliding window for instantaneous rate calculation,
		// which naturally handles bitrate changes without needing resets.
		// See: performance_testing_implementation_log.md "Performance Degradation Investigation"
		return cs.buildStatusResponse()

	case CmdGetStatus:
		return cs.buildStatusResponse()

	case CmdHeartbeat:
		cs.recordHeartbeat()
		return NewOKResponse()

	case CmdStop:
		return NewOKResponse()

	default:
		return NewErrorResponse(fmt.Errorf("unknown command: %s", req.Command))
	}
}

// buildStatusResponse creates a status response with current metrics.
func (cs *ControlServer) buildStatusResponse() *ControlResponse {
	return NewStatusResponse(
		cs.bm.Current(),
		cs.bm.Target(),
		cs.packetsSent.Load(),
		cs.bytesSent.Load(),
		cs.connectionAlive.Load(),
		time.Since(cs.startTime).Seconds(),
		cs.WatchdogStateString(),
	)
}

// recordHeartbeat updates the last heartbeat time.
func (cs *ControlServer) recordHeartbeat() {
	cs.mu.Lock()
	cs.lastHeartbeat = time.Now()
	cs.mu.Unlock()
}

// LastHeartbeat returns the time of the last heartbeat.
func (cs *ControlServer) LastHeartbeat() time.Time {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.lastHeartbeat
}

// TimeSinceHeartbeat returns the duration since the last heartbeat.
func (cs *ControlServer) TimeSinceHeartbeat() time.Duration {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return time.Since(cs.lastHeartbeat)
}

// WatchdogStateString returns the current watchdog state as a string.
// This is determined by the Watchdog, but we provide a default here.
func (cs *ControlServer) WatchdogStateString() string {
	// Default to normal - the actual state is managed by Watchdog
	return WatchdogNormal
}

// SetConnectionAlive updates the connection status.
func (cs *ControlServer) SetConnectionAlive(alive bool) {
	cs.connectionAlive.Store(alive)
}

// IncrementStats atomically increments the packet and byte counters.
func (cs *ControlServer) IncrementStats(packets, bytes uint64) {
	cs.packetsSent.Add(packets)
	cs.bytesSent.Add(bytes)
}

// Stats returns the current packet and byte counts.
func (cs *ControlServer) Stats() (packets, bytes uint64) {
	return cs.packetsSent.Load(), cs.bytesSent.Load()
}

// Stop gracefully shuts down the control server.
// The context parameter is accepted for API consistency with other servers,
// though it is not currently used since the stop operation is immediate.
func (cs *ControlServer) Stop(_ context.Context) {
	cs.stopOnce.Do(func() {
		cs.stopped.Store(true)
		if cs.listener != nil {
			if closeErr := cs.listener.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "control listener close error: %v\n", closeErr)
			}
		}
		// Clean up socket file
		if removeErr := os.Remove(cs.socketPath); removeErr != nil && !os.IsNotExist(removeErr) {
			fmt.Fprintf(os.Stderr, "control socket cleanup error: %v\n", removeErr)
		}
	})
}

// SocketPath returns the path to the control socket.
func (cs *ControlServer) SocketPath() string {
	return cs.socketPath
}
