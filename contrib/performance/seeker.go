package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// SeekerControl implements the Seeker interface for controlling the client-seeker.
type SeekerControl struct {
	socketPath string
	mu         sync.Mutex
	conn       net.Conn
	reader     *bufio.Reader

	// Cached status
	lastStatus      atomic.Value // SeekerStatus
	lastStatusTime  atomic.Value // time.Time
	connectionAlive atomic.Bool
}

// NewSeekerControl creates a new seeker control client.
func NewSeekerControl(socketPath string) *SeekerControl {
	sc := &SeekerControl{
		socketPath: socketPath,
	}
	sc.lastStatusTime.Store(time.Time{})
	return sc
}

// Connect establishes connection to the control socket.
func (sc *SeekerControl) Connect() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn != nil {
		sc.conn.Close()
	}

	conn, err := net.DialTimeout("unix", sc.socketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", sc.socketPath, err)
	}

	sc.conn = conn
	sc.reader = bufio.NewReader(conn)
	return nil
}

// Close closes the connection.
func (sc *SeekerControl) Close() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn != nil {
		err := sc.conn.Close()
		sc.conn = nil
		sc.reader = nil
		return err
	}
	return nil
}

// SetBitrate implements Seeker interface.
func (sc *SeekerControl) SetBitrate(ctx context.Context, bps int64) error {
	req := map[string]interface{}{
		"command": "set_bitrate",
		"bitrate": bps,
	}

	resp, err := sc.sendCommand(ctx, req)
	if err != nil {
		return err
	}

	if resp.Status != "ok" {
		return fmt.Errorf("set_bitrate failed: %s", resp.Error)
	}

	return nil
}

// Status implements Seeker interface.
func (sc *SeekerControl) Status(ctx context.Context) (SeekerStatus, error) {
	req := map[string]interface{}{
		"command": "get_status",
	}

	resp, err := sc.sendCommand(ctx, req)
	if err != nil {
		return SeekerStatus{}, err
	}

	status := SeekerStatus{
		CurrentBitrate:  resp.CurrentBitrate,
		TargetBitrate:   resp.TargetBitrate,
		PacketsSent:     resp.PacketsSent,
		BytesSent:       resp.BytesSent,
		ConnectionAlive: resp.ConnectionAlive,
		UptimeSeconds:   resp.UptimeSeconds,
		WatchdogState:   resp.WatchdogState,
	}

	// Cache status
	sc.lastStatus.Store(status)
	sc.lastStatusTime.Store(time.Now())
	sc.connectionAlive.Store(status.ConnectionAlive)

	return status, nil
}

// Heartbeat implements Seeker interface.
func (sc *SeekerControl) Heartbeat(ctx context.Context) error {
	req := map[string]interface{}{
		"command": "heartbeat",
	}

	resp, err := sc.sendCommand(ctx, req)
	if err != nil {
		return err
	}

	if resp.Status != "ok" {
		return fmt.Errorf("heartbeat failed: %s", resp.Error)
	}

	return nil
}

// Stop implements Seeker interface.
func (sc *SeekerControl) Stop(ctx context.Context) error {
	req := map[string]interface{}{
		"command": "stop",
	}

	_, err := sc.sendCommand(ctx, req)
	return err
}

// IsAlive implements Seeker interface.
func (sc *SeekerControl) IsAlive() bool {
	return sc.connectionAlive.Load()
}

// sendCommand sends a command and reads the response.
func (sc *SeekerControl) sendCommand(ctx context.Context, req map[string]interface{}) (*ControlResponse, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Set deadline from context
	if deadline, ok := ctx.Deadline(); ok {
		sc.conn.SetDeadline(deadline)
	} else {
		sc.conn.SetDeadline(time.Now().Add(2 * time.Second))
	}

	// Send request
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	_, err = sc.conn.Write(append(data, '\n'))
	if err != nil {
		sc.connectionAlive.Store(false)
		return nil, fmt.Errorf("write: %w", err)
	}

	// Read response
	line, err := sc.reader.ReadString('\n')
	if err != nil {
		sc.connectionAlive.Store(false)
		return nil, fmt.Errorf("read: %w", err)
	}

	var resp ControlResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// ControlResponse matches the client-seeker's response format.
type ControlResponse struct {
	Status          string  `json:"status"`
	Error           string  `json:"error,omitempty"`
	CurrentBitrate  int64   `json:"current_bitrate,omitempty"`
	TargetBitrate   int64   `json:"target_bitrate,omitempty"`
	PacketsSent     uint64  `json:"packets_sent,omitempty"`
	BytesSent       uint64  `json:"bytes_sent,omitempty"`
	ConnectionAlive bool    `json:"connection_alive,omitempty"`
	UptimeSeconds   float64 `json:"uptime_seconds,omitempty"`
	WatchdogState   string  `json:"watchdog_state,omitempty"`
	IsHealthy       bool    `json:"is_healthy,omitempty"`
}

// GetStatusFast returns cached status if recent, otherwise fetches new.
func (sc *SeekerControl) GetStatusFast(ctx context.Context, maxAge time.Duration) (SeekerStatus, error) {
	lastTime := sc.lastStatusTime.Load().(time.Time)
	if time.Since(lastTime) < maxAge {
		if status, ok := sc.lastStatus.Load().(SeekerStatus); ok {
			return status, nil
		}
	}
	return sc.Status(ctx)
}

// Reconnect attempts to reconnect to the control socket.
func (sc *SeekerControl) Reconnect() error {
	sc.Close()
	return sc.Connect()
}
