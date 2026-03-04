package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControlServer_SetBitrate(t *testing.T) {
	// Create temp socket path
	socketPath := filepath.Join(t.TempDir(), "test.sock")

	// Create components
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil) // nil generator for tests

	// Start server
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = cs.Start(ctx) }()
	time.Sleep(50 * time.Millisecond) // Wait for server to start

	// Connect and send command using context-aware dialer
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("conn.Close error: %v", closeErr)
		}
	}()

	// Send set_bitrate command
	req := ControlRequest{Command: CmdSetBitrate, Bitrate: 200_000_000}
	data, _ := json.Marshal(req)
	_, _ = conn.Write(append(data, '\n'))

	// Read response
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, readErr := conn.Read(buf)
	if readErr != nil {
		t.Fatalf("failed to read response: %v", readErr)
	}

	var resp ControlResponse
	if unmarshalErr := json.Unmarshal(buf[:n], &resp); unmarshalErr != nil {
		t.Fatalf("failed to parse response: %v", unmarshalErr)
	}

	// Verify
	if resp.Status != StatusOK {
		t.Errorf("Status = %q, want %q", resp.Status, StatusOK)
	}
	if resp.CurrentBitrate != 200_000_000 {
		t.Errorf("CurrentBitrate = %d, want 200000000", resp.CurrentBitrate)
	}
	if bm.Current() != 200_000_000 {
		t.Errorf("BitrateManager.Current() = %d, want 200000000", bm.Current())
	}
}

func TestControlServer_GetStatus(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = cs.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("conn.Close error: %v", closeErr)
		}
	}()

	// Send get_status command
	req := ControlRequest{Command: CmdGetStatus}
	data, _ := json.Marshal(req)
	_, _ = conn.Write(append(data, '\n'))

	// Read response
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, readErr := conn.Read(buf)
	if readErr != nil {
		t.Fatalf("failed to read response: %v", readErr)
	}

	var resp ControlResponse
	if unmarshalErr := json.Unmarshal(buf[:n], &resp); unmarshalErr != nil {
		t.Fatalf("failed to parse response: %v", unmarshalErr)
	}

	// Verify
	if resp.Status != StatusOK {
		t.Errorf("Status = %q, want %q", resp.Status, StatusOK)
	}
	if resp.CurrentBitrate != 100_000_000 {
		t.Errorf("CurrentBitrate = %d, want 100000000", resp.CurrentBitrate)
	}
	if resp.UptimeSeconds <= 0 {
		t.Errorf("UptimeSeconds = %f, want > 0", resp.UptimeSeconds)
	}
}

func TestControlServer_Heartbeat(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = cs.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Record time before heartbeat
	beforeHeartbeat := cs.LastHeartbeat()

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("conn.Close error: %v", closeErr)
		}
	}()

	// Send heartbeat command
	req := ControlRequest{Command: CmdHeartbeat}
	data, _ := json.Marshal(req)
	_, _ = conn.Write(append(data, '\n'))

	// Read response
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, readErr := conn.Read(buf)
	if readErr != nil {
		t.Fatalf("failed to read response: %v", readErr)
	}

	var resp ControlResponse
	if unmarshalErr := json.Unmarshal(buf[:n], &resp); unmarshalErr != nil {
		t.Fatalf("failed to parse response: %v", unmarshalErr)
	}

	// Verify
	if resp.Status != StatusOK {
		t.Errorf("Status = %q, want %q", resp.Status, StatusOK)
	}

	// Heartbeat time should have been updated
	afterHeartbeat := cs.LastHeartbeat()
	if !afterHeartbeat.After(beforeHeartbeat) {
		t.Errorf("LastHeartbeat not updated: before=%v, after=%v", beforeHeartbeat, afterHeartbeat)
	}
}

func TestControlServer_InvalidCommand(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = cs.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("conn.Close error: %v", closeErr)
		}
	}()

	// Send invalid command
	if _, writeErr := conn.Write([]byte(`{"command":"invalid_command"}` + "\n")); writeErr != nil {
		t.Fatalf("failed to write command: %v", writeErr)
	}

	// Read response
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, readErr := conn.Read(buf)
	if readErr != nil {
		t.Fatalf("failed to read response: %v", readErr)
	}

	var resp ControlResponse
	if unmarshalErr := json.Unmarshal(buf[:n], &resp); unmarshalErr != nil {
		t.Fatalf("failed to parse response: %v", unmarshalErr)
	}

	// Verify error response
	if resp.Status != StatusError {
		t.Errorf("Status = %q, want %q", resp.Status, StatusError)
	}
	if resp.Error == "" {
		t.Error("Error message should not be empty")
	}
}

func TestControlServer_InvalidJSON(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = cs.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("conn.Close error: %v", closeErr)
		}
	}()

	// Send invalid JSON
	if _, writeErr := conn.Write([]byte("not valid json\n")); writeErr != nil {
		t.Fatalf("failed to write command: %v", writeErr)
	}

	// Read response
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	n, readErr := conn.Read(buf)
	if readErr != nil {
		t.Fatalf("failed to read response: %v", readErr)
	}

	var resp ControlResponse
	if unmarshalErr := json.Unmarshal(buf[:n], &resp); unmarshalErr != nil {
		t.Fatalf("failed to parse response: %v", unmarshalErr)
	}

	if resp.Status != StatusError {
		t.Errorf("Status = %q, want %q", resp.Status, StatusError)
	}
}

func TestControlServer_MultipleCommands(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = cs.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("conn.Close error: %v", closeErr)
		}
	}()

	// Send multiple commands on same connection
	commands := []ControlRequest{
		{Command: CmdSetBitrate, Bitrate: 150_000_000},
		{Command: CmdGetStatus},
		{Command: CmdHeartbeat},
		{Command: CmdSetBitrate, Bitrate: 200_000_000},
	}

	for i, req := range commands {
		data, _ := json.Marshal(req)
		_, _ = conn.Write(append(data, '\n'))

		buf := make([]byte, 4096)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, readErr := conn.Read(buf)
		if readErr != nil {
			t.Fatalf("command %d: failed to read response: %v", i, readErr)
		}

		var resp ControlResponse
		if unmarshalErr := json.Unmarshal(buf[:n], &resp); unmarshalErr != nil {
			t.Fatalf("command %d: failed to parse response: %v", i, unmarshalErr)
		}

		if resp.Status != StatusOK {
			t.Errorf("command %d: Status = %q, want %q", i, resp.Status, StatusOK)
		}
	}

	// Final bitrate should be 200M
	if bm.Current() != 200_000_000 {
		t.Errorf("Final bitrate = %d, want 200000000", bm.Current())
	}
}

func TestControlServer_SocketCleanup(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	ctx, cancel := context.WithCancel(context.Background())

	go func() { _ = cs.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Verify socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		t.Fatal("socket file should exist")
	}

	// Stop server
	cancel()
	time.Sleep(100 * time.Millisecond)

	// Socket should be cleaned up
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after stop")
	}
}
