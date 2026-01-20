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

	// Create listener
	listener, err := net.Listen("unix", cs.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", cs.socketPath, err)
	}
	cs.listener = listener

	// Ensure socket is cleaned up on exit
	go func() {
		<-ctx.Done()
		cs.Stop()
	}()

	// Accept loop
	for {
		conn, err := listener.Accept()
		if err != nil {
			// Check if we're shutting down
			if cs.stopped.Load() {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			default:
				// Log error but continue accepting
				fmt.Fprintf(os.Stderr, "control: accept error: %v\n", err)
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
	defer conn.Close()

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
		req, err := ParseRequest(line)
		if err != nil {
			resp := NewErrorResponse(err)
			data, _ := resp.Marshal()
			conn.Write(data)
			continue
		}

		// Handle command
		resp := cs.handleCommand(req)

		// Send response
		data, err := resp.Marshal()
		if err != nil {
			resp := NewErrorResponse(fmt.Errorf("failed to marshal response: %w", err))
			data, _ = resp.Marshal()
		}
		conn.Write(data)

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
func (cs *ControlServer) Stop() {
	cs.stopOnce.Do(func() {
		cs.stopped.Store(true)
		if cs.listener != nil {
			cs.listener.Close()
		}
		// Clean up socket file
		os.Remove(cs.socketPath)
	})
}

// SocketPath returns the path to the control socket.
func (cs *ControlServer) SocketPath() string {
	return cs.socketPath
}
