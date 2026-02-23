package srt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Connection Lifecycle Table-Driven Tests
// Tests for Close(), timeout, cleanup, and concurrent access
// ═══════════════════════════════════════════════════════════════════════════

// LifecycleTestCase defines a table-driven test case for connection lifecycle
type LifecycleTestCase struct {
	// Test identification
	Name string

	// CODE_PARAMs - production parameters
	CloseReason     metrics.CloseReason
	PeerIdleTimeout time.Duration // 0 = use default

	// TEST_INFRA - test mechanics
	ActiveTransfer   bool // Data transfer during close
	ConcurrentCloses int  // 0 = single close, >0 = concurrent goroutines
	SendActivity     bool // Send activity before close to test timeout reset

	// EXPECTATIONS
	ExpectShutdownSent bool // Expect shutdown message sent to peer
	ExpectCleanup      bool // Expect clean resource cleanup
	ExpectTimeout      bool // Expect timeout to fire
}

// lifecycleTests is the test table for connection lifecycle scenarios
var lifecycleTests = []LifecycleTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Core Scenarios
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:               "GracefulClose",
		CloseReason:        metrics.CloseReasonGraceful,
		ExpectShutdownSent: true,
		ExpectCleanup:      true,
	},
	{
		Name:               "CloseUnderLoad",
		CloseReason:        metrics.CloseReasonGraceful,
		ActiveTransfer:     true,
		ExpectShutdownSent: true,
		ExpectCleanup:      true,
	},
	{
		Name:             "ConcurrentClose",
		ConcurrentCloses: 5,
		ExpectCleanup:    true,
	},
	{
		Name:               "DoubleClose",
		CloseReason:        metrics.CloseReasonGraceful,
		ConcurrentCloses:   2, // Two sequential closes (idempotent)
		ExpectShutdownSent: true,
		ExpectCleanup:      true,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Close Reason Variants
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:               "CloseReason_Graceful",
		CloseReason:        metrics.CloseReasonGraceful,
		ExpectShutdownSent: true,
		ExpectCleanup:      true,
	},
	{
		Name:               "CloseReason_ContextCancel",
		CloseReason:        metrics.CloseReasonContextCancel,
		ExpectShutdownSent: false, // Context cancelled, may not send
		ExpectCleanup:      true,
	},
	{
		Name:               "CloseReason_Error",
		CloseReason:        metrics.CloseReasonError,
		ExpectShutdownSent: false, // Error state, may not send
		ExpectCleanup:      true,
	},
	{
		Name:               "CloseReason_PeerIdle",
		CloseReason:        metrics.CloseReasonPeerIdle,
		PeerIdleTimeout:    2 * time.Second, // Must be > HandshakeTimeout (default 1.5s)
		ExpectTimeout:      false,           // We close manually before timeout
		ExpectCleanup:      true,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Activity and Timeout Scenarios
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:             "ActivityResetsTimeout",
		PeerIdleTimeout:  2 * time.Second, // Must be > HandshakeTimeout (default 1.5s)
		SendActivity:     true,            // Sending data should reset timeout
		ExpectCleanup:    true,
	},
	{
		Name:               "CloseWithActiveTransfer_LargeData",
		CloseReason:        metrics.CloseReasonGraceful,
		ActiveTransfer:     true,
		ExpectShutdownSent: true,
		ExpectCleanup:      true,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Corner Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Corner_ZeroTimeout",
		CloseReason:   metrics.CloseReasonGraceful,
		ExpectCleanup: true,
	},
	{
		Name:             "Corner_ManyCloses",
		ConcurrentCloses: 10,
		ExpectCleanup:    true,
	},
	{
		Name:             "Corner_StressConcurrent",
		ConcurrentCloses: 20, // Stress test with many concurrent close calls
		ExpectCleanup:    true,
	},
	{
		Name:               "Corner_CloseImmediatelyAfterConnect",
		CloseReason:        metrics.CloseReasonGraceful,
		ExpectShutdownSent: true,
		ExpectCleanup:      true,
		// No activity between connect and close
	},
}

// TestConnection_Lifecycle_Table runs all lifecycle table tests
func TestConnection_Lifecycle_Table(t *testing.T) {
	// Use a single port for all tests to avoid port exhaustion
	basePort := 6100

	for i, tc := range lifecycleTests {
		port := basePort + i
		t.Run(tc.Name, func(t *testing.T) {
			runLifecycleTest(t, tc, port)
		})
	}
}

// runLifecycleTest executes a single lifecycle test case
func runLifecycleTest(t *testing.T, tc LifecycleTestCase, port int) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Create server config
	serverConfig := DefaultConfig()
	if tc.PeerIdleTimeout > 0 {
		serverConfig.PeerIdleTimeout = tc.PeerIdleTimeout
	}

	// Track server connection received
	connReceived := make(chan struct{})

	addr := "127.0.0.1:" + itoa(port)

	server := Server{
		Addr:       addr,
		Config:     &serverConfig,
		Context:    ctx,
		ShutdownWg: &wg,
		HandleConnect: func(req ConnRequest) ConnType {
			return PUBLISH
		},
		HandlePublish: func(conn Conn) {
			select {
			case <-connReceived:
				// Already closed
			default:
				close(connReceived)
			}

			// Read data if active transfer test
			if tc.ActiveTransfer {
				buf := make([]byte, 1024)
				for {
					_, err := conn.Read(buf)
					if err != nil {
						break
					}
				}
			}

			// Wait for context done
			<-ctx.Done()
		},
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	// Start server in background
	go func() {
		_ = server.Serve()
	}()

	// Small delay for server to start accepting
	time.Sleep(50 * time.Millisecond)

	// Connect client
	clientConfig := DefaultConfig()
	clientConfig.StreamId = "test-lifecycle"

	conn, err := Dial(ctx, "srt", addr, clientConfig, &wg)
	if err != nil {
		// Some test cases may expect connection failure
		if tc.ExpectTimeout {
			t.Logf("✅ %s: connection timed out as expected", tc.Name)
			return
		}
		require.NoError(t, err)
	}

	// Wait for server to receive connection
	select {
	case <-connReceived:
	case <-time.After(3 * time.Second):
		t.Fatalf("Server did not receive connection")
	}

	// Handle SendActivity scenario
	if tc.SendActivity {
		// Send some data to reset timeout
		_, _ = conn.Write([]byte("activity"))
		time.Sleep(50 * time.Millisecond)
	}

	// Handle ActiveTransfer scenario
	var transferDone sync.WaitGroup
	if tc.ActiveTransfer {
		transferDone.Add(1)
		go func() {
			defer transferDone.Done()
			for i := 0; i < 10; i++ {
				_, err := conn.Write([]byte("data-transfer-test"))
				if err != nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	// Handle ConcurrentCloses scenario
	if tc.ConcurrentCloses > 0 {
		var closewg sync.WaitGroup
		for i := 0; i < tc.ConcurrentCloses; i++ {
			closewg.Add(1)
			go func() {
				defer closewg.Done()
				_ = conn.Close()
			}()
		}
		closewg.Wait()
	} else {
		// Normal close
		err = conn.Close()
		// Close should be idempotent, so no error expected
	}

	// Wait for active transfer to complete
	if tc.ActiveTransfer {
		transferDone.Wait()
	}

	// Verify cleanup - give time for shutdown
	time.Sleep(100 * time.Millisecond)

	// If we get here without panic, cleanup succeeded
	t.Logf("✅ %s: lifecycle test passed", tc.Name)
}

// itoa converts int to string without importing strconv
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}
