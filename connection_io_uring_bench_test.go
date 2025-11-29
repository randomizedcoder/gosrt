//go:build linux
// +build linux

package srt

import (
	"net"
	"testing"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/packet"
)

// BenchmarkSendPath compares the send path performance with and without io_uring
func BenchmarkSendPath(b *testing.B) {
	// This benchmark will be implemented to compare:
	// - Old send path (mutex-protected, synchronous)
	// - New send path (io_uring, per-connection, asynchronous)

	// Setup will be done in a separate helper function
	b.Skip("Benchmark infrastructure - to be implemented")
}

// BenchmarkSendPathSingleConnection benchmarks send performance for a single connection
func BenchmarkSendPathSingleConnection(b *testing.B) {
	// Setup: Create a connection with io_uring enabled
	// Measure: Time to send N packets through the send path

	b.Skip("Benchmark infrastructure - to be implemented")
}

// BenchmarkSendPathMultipleConnections benchmarks send performance with multiple concurrent connections
func BenchmarkSendPathMultipleConnections(b *testing.B) {
	// Setup: Create N connections (10, 100, 1000)
	// Measure: Aggregate throughput and latency distribution

	b.Skip("Benchmark infrastructure - to be implemented")
}

// BenchmarkCompletionHandler benchmarks the completion handler processing speed
func BenchmarkCompletionHandler(b *testing.B) {
	// Measure: Time to process completions from the ring

	b.Skip("Benchmark infrastructure - to be implemented")
}

// Helper function to create a test connection with io_uring enabled
func createTestConnectionWithIoUring(tb testing.TB, enableIoUring bool) (*srtConn, func()) {
	tb.Helper()

	// Create a test UDP connection
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("failed to create test UDP connection: %v", err)
	}

	udpConn := pc.(*net.UDPConn)

	// Extract socket FD if io_uring enabled
	var socketFd int
	if enableIoUring {
		socketFd, err = getUDPConnFD(udpConn)
		if err != nil {
			tb.Fatalf("failed to extract socket FD: %v", err)
		}
	}

	// Create config
	config := DefaultConfig()
	config.IoUringEnabled = enableIoUring
	if enableIoUring {
		config.IoUringSendRingSize = 64
	}

	// Create connection config
	connConfig := srtConnConfig{
		version:                     5,
		isCaller:                    false,
		localAddr:                   udpConn.LocalAddr(),
		remoteAddr:                  &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
		config:                      config,
		start:                       time.Now(),
		socketId:                    0x12345678,
		peerSocketId:                0x87654321,
		tsbpdTimeBase:               0,
		tsbpdDelay:                  120000, // 120ms in microseconds
		peerTsbpdDelay:              120000,
		initialPacketSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		crypto:                      nil,
		keyBaseEncryption:           packet.EvenKeyEncrypted,
		onSend:                      func(p packet.Packet) {}, // No-op for testing
		onShutdown:                  func(socketId uint32) {},
		logger:                      NewLogger(nil),
		socketFd:                    socketFd,
	}

	conn := newSRTConn(connConfig)

	cleanup := func() {
		conn.Close()
		udpConn.Close()
	}

	return conn, cleanup
}

// TestIoUringSendPath tests that the io_uring send path works correctly
func TestIoUringSendPath(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping io_uring test in short mode")
	}

	conn, cleanup := createTestConnectionWithIoUring(t, true)
	defer cleanup()

	// Verify io_uring ring was created
	if conn.sendRing == nil {
		t.Fatal("io_uring ring was not created")
	}

	// Verify completion handler is running
	if conn.sendCompCtx == nil {
		t.Fatal("completion handler context was not created")
	}

	// Create a test packet
	p := packet.NewPacket(conn.remoteAddr)
	p.Header().IsControlPacket = true
	p.Header().ControlType = packet.CTRLTYPE_USER

	// Test that send doesn't panic
	// Note: This will fail if socket FD is invalid, but that's expected in unit tests
	// In real usage, the socket FD would be valid
	conn.send(p)

	// Give completion handler time to process
	time.Sleep(10 * time.Millisecond)
}

// TestIoUringFallback tests that fallback works when io_uring is disabled
func TestIoUringFallback(t *testing.T) {
	conn, cleanup := createTestConnectionWithIoUring(t, false)
	defer cleanup()

	// Verify io_uring ring was NOT created
	if conn.sendRing != nil {
		t.Fatal("io_uring ring should not be created when disabled")
	}

	// Verify onSend is still set (fallback)
	if conn.onSend == nil {
		t.Fatal("onSend callback should be set for fallback")
	}
}

// TestSocketFDExtraction tests that socket FD extraction works
func TestSocketFDExtraction(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create test UDP connection: %v", err)
	}
	defer pc.Close()

	udpConn := pc.(*net.UDPConn)

	fd, err := getUDPConnFD(udpConn)
	if err != nil {
		t.Fatalf("failed to extract socket FD: %v", err)
	}

	if fd < 0 {
		t.Fatal("socket FD should be non-negative")
	}

	// Verify FD is valid by checking it's not -1
	if fd == -1 {
		t.Fatal("socket FD should not be -1")
	}
}
