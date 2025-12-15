//go:build testing
// +build testing

package srt

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/congestion/live"
	"github.com/datarhei/gosrt/packet"
)

// TestConnectionPassesNakBtreeConfigToReceiver verifies that NAK btree configuration
// from srt.Config is properly passed to the receiver.
//
// **EXPECTED BEFORE FIX: FAIL** - Config is not being passed in connection.go
// **EXPECTED AFTER FIX:  PASS** - Config is passed correctly
func TestConnectionPassesNakBtreeConfigToReceiver(t *testing.T) {
	// Create a config with NAK btree enabled
	config := Config{
		UseNakBtree:          true,
		SuppressImmediateNak: true,
		NakRecentPercent:     0.25,
		FastNakEnabled:       true,
		FastNakRecentEnabled: true,
		NakMergeGap:          7,
		PeerIdleTimeout:      5 * time.Second, // Required to avoid panic
	}

	// Apply auto configuration (this is normally done automatically)
	config.ApplyAutoConfiguration()

	// Create a minimal connection for testing
	conn := createMinimalTestConnection(t, config)
	defer conn.Close()

	// Get receiver internals using the test helper
	internals, ok := live.GetReceiverTestInternals(conn.recv)
	if !ok {
		t.Fatal("Failed to get receiver internals - receiver is not a *live.receiver")
	}

	// Verify UseNakBtree is passed
	// This is the CRITICAL assertion - it should FAIL before the fix
	if !internals.UseNakBtree {
		t.Error("UseNakBtree not passed: expected true, got false")
		t.Log("BUG CONFIRMED: NAK btree config is NOT being passed from connection to receiver!")
	}

	// Verify SuppressImmediateNak is passed
	if !internals.SuppressImmediateNak {
		t.Error("SuppressImmediateNak not passed: expected true, got false")
	}

	// Verify NakRecentPercent is passed
	if internals.NakRecentPercent != 0.25 {
		t.Errorf("NakRecentPercent not passed: expected 0.25, got %f", internals.NakRecentPercent)
	}

	// Verify FastNakEnabled is passed
	if !internals.FastNakEnabled {
		t.Error("FastNakEnabled not passed: expected true, got false")
	}

	// Verify FastNakRecentEnabled is passed
	if !internals.FastNakRecentEnabled {
		t.Error("FastNakRecentEnabled not passed: expected true, got false")
	}

	// Verify NakMergeGap is passed
	if internals.NakMergeGap != 7 {
		t.Errorf("NakMergeGap not passed: expected 7, got %d", internals.NakMergeGap)
	}

	// Verify NAK btree was created (should be created when UseNakBtree=true)
	if !internals.NakBtreeCreated {
		t.Error("NAK btree not created despite UseNakBtree=true")
	}
}

// TestConnectionPassesDisabledNakBtreeConfigToReceiver verifies that when
// NAK btree is disabled, the receiver correctly reflects this.
func TestConnectionPassesDisabledNakBtreeConfigToReceiver(t *testing.T) {
	// Create a config with NAK btree disabled (default)
	config := Config{
		UseNakBtree:     false,
		PeerIdleTimeout: 5 * time.Second, // Required to avoid panic
	}

	// Create a minimal connection for testing
	conn := createMinimalTestConnection(t, config)
	defer conn.Close()

	// Get receiver internals
	internals, ok := live.GetReceiverTestInternals(conn.recv)
	if !ok {
		t.Fatal("Failed to get receiver internals")
	}

	// Verify UseNakBtree is false
	if internals.UseNakBtree {
		t.Error("UseNakBtree should be false, got true")
	}

	// NAK btree should NOT be created
	if internals.NakBtreeCreated {
		t.Error("NAK btree should not be created when UseNakBtree=false")
	}
}

// TestConnectionWithIoUringAutoEnablesNakBtree verifies that when io_uring recv
// is enabled, NAK btree is auto-enabled via ApplyAutoConfiguration.
func TestConnectionWithIoUringAutoEnablesNakBtree(t *testing.T) {
	// Create a config with io_uring recv enabled
	// Note: ApplyAutoConfiguration should set UseNakBtree=true
	config := Config{
		IoUringRecvEnabled: true,
		PeerIdleTimeout:    5 * time.Second, // Required to avoid panic
	}

	// Apply auto configuration
	config.ApplyAutoConfiguration()

	// Verify auto-configuration set UseNakBtree
	if !config.UseNakBtree {
		t.Fatal("ApplyAutoConfiguration should set UseNakBtree=true when IoUringRecvEnabled=true")
	}

	// Create a minimal connection for testing
	conn := createMinimalTestConnection(t, config)
	defer conn.Close()

	// Get receiver internals
	internals, ok := live.GetReceiverTestInternals(conn.recv)
	if !ok {
		t.Fatal("Failed to get receiver internals")
	}

	// Verify UseNakBtree was passed (this tests the fix)
	if !internals.UseNakBtree {
		t.Error("UseNakBtree not passed to receiver despite IoUringRecvEnabled=true and ApplyAutoConfiguration")
		t.Log("BUG: io_uring recv is enabled but NAK btree protection is NOT active!")
	}
}

// createMinimalTestConnection creates a minimal srtConn for testing purposes.
// This bypasses the full Listen/Dial flow and directly creates a connection.
func createMinimalTestConnection(t *testing.T, config Config) *srtConn {
	t.Helper()

	// Create a dummy UDP connection
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("Failed to create UDP connection: %v", err)
	}

	// Get socket file descriptor
	rawConn, err := udpConn.SyscallConn()
	if err != nil {
		udpConn.Close()
		t.Fatalf("Failed to get raw connection: %v", err)
	}
	var socketFd int
	err = rawConn.Control(func(fd uintptr) {
		socketFd = int(fd)
	})
	if err != nil {
		udpConn.Close()
		t.Fatalf("Failed to get socket fd: %v", err)
	}

	// Create test context and waitgroup
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// Pre-create metrics
	localAddr := udpConn.LocalAddr()
	socketId := uint32(0x12345678)
	connMetrics := createConnectionMetrics(localAddr, socketId)

	// Create connection config
	connConfig := srtConnConfig{
		version:                     5,
		isCaller:                    false,
		localAddr:                   localAddr,
		remoteAddr:                  &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
		config:                      config,
		start:                       time.Now(),
		socketId:                    socketId,
		peerSocketId:                0x87654321,
		tsbpdTimeBase:               0,
		tsbpdDelay:                  3_000_000, // 3 seconds in microseconds
		peerTsbpdDelay:              3_000_000,
		initialPacketSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		crypto:                      nil,
		keyBaseEncryption:           packet.EvenKeyEncrypted,
		onSend:                      func(p packet.Packet) {}, // No-op for testing
		onShutdown:                  func(socketId uint32) {},
		logger:                      NewLogger(nil),
		socketFd:                    socketFd,
		parentCtx:                   ctx,
		parentWg:                    &wg,
		metrics:                     connMetrics,
	}

	wg.Add(1) // Increment waitgroup before creating connection
	conn := newSRTConn(connConfig)

	// Register cleanup
	t.Cleanup(func() {
		cancel()
		conn.Close()
		udpConn.Close()
	})

	return conn
}
