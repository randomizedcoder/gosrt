package srt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Server Table-Driven Tests
// Tests server configuration, connection handling, and shutdown scenarios.
// ═══════════════════════════════════════════════════════════════════════════

// ServerTestCase defines a table-driven test case for server operations
type ServerTestCase struct {
	Name string

	// CODE_PARAMs - production parameters
	ServerAddr      string
	StreamIdHandler func(streamId string) ConnType

	// Client configuration
	ClientStreamId string

	// EXPECTATIONS
	ExpectConnect bool
	ExpectError   bool
}

var serverTests = []ServerTestCase{
	{
		Name:       "Basic_Publish",
		ServerAddr: "127.0.0.1:6300",
		StreamIdHandler: func(streamId string) ConnType {
			if streamId == "publish" {
				return PUBLISH
			}
			return REJECT
		},
		ClientStreamId: "publish",
		ExpectConnect:  true,
	},
	{
		Name:       "Basic_Subscribe",
		ServerAddr: "127.0.0.1:6301",
		StreamIdHandler: func(streamId string) ConnType {
			if streamId == "subscribe" {
				return SUBSCRIBE
			}
			return REJECT
		},
		ClientStreamId: "subscribe",
		ExpectConnect:  true,
	},
	{
		Name:       "Reject_Unknown_StreamId",
		ServerAddr: "127.0.0.1:6302",
		StreamIdHandler: func(streamId string) ConnType {
			if streamId == "allowed" {
				return PUBLISH
			}
			return REJECT
		},
		ClientStreamId: "unknown",
		ExpectConnect:  false,
		ExpectError:    true,
	},
	{
		Name:       "Empty_StreamId_Rejected",
		ServerAddr: "127.0.0.1:6303",
		StreamIdHandler: func(streamId string) ConnType {
			if streamId == "" {
				return REJECT
			}
			return PUBLISH
		},
		ClientStreamId: "",
		ExpectConnect:  false,
		ExpectError:    true,
	},
	{
		Name:       "StreamId_Based_Routing",
		ServerAddr: "127.0.0.1:6304",
		StreamIdHandler: func(streamId string) ConnType {
			switch streamId {
			case "pub":
				return PUBLISH
			case "sub":
				return SUBSCRIBE
			default:
				return REJECT
			}
		},
		ClientStreamId: "pub",
		ExpectConnect:  true,
	},
	{
		Name:       "Long_StreamId_Accepted",
		ServerAddr: "127.0.0.1:6305",
		StreamIdHandler: func(streamId string) ConnType {
			return PUBLISH
		},
		ClientStreamId: "this-is-a-very-long-stream-id-that-should-still-work-fine",
		ExpectConnect:  true,
	},
	{
		Name:       "SpecialChars_StreamId",
		ServerAddr: "127.0.0.1:6306",
		StreamIdHandler: func(streamId string) ConnType {
			return PUBLISH
		},
		ClientStreamId: "stream/with/slashes",
		ExpectConnect:  true,
	},
}

func TestServer_Table(t *testing.T) {
	for _, tc := range serverTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			runServerTest(t, tc)
		})
	}
}

func runServerTest(t *testing.T, tc ServerTestCase) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	server := Server{
		Addr: tc.ServerAddr,
		HandleConnect: func(req ConnRequest) ConnType {
			return tc.StreamIdHandler(req.StreamId())
		},
		HandlePublish: func(conn Conn) {
			<-ctx.Done()
		},
		HandleSubscribe: func(conn Conn) {
			<-ctx.Done()
		},
		Context:    ctx,
		ShutdownWg: &wg,
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		_ = server.Serve()
	}()

	// Small delay to ensure server is ready
	time.Sleep(10 * time.Millisecond)

	// Try to connect
	config := DefaultConfig()
	config.StreamId = tc.ClientStreamId

	conn, err := Dial(ctx, "srt", tc.ServerAddr, config, &wg)

	if tc.ExpectError {
		require.Error(t, err, "Expected connection error for %s", tc.Name)
		t.Logf("✅ %s: connection rejected as expected", tc.Name)
		return
	}

	if tc.ExpectConnect {
		require.NoError(t, err, "Expected successful connection for %s", tc.Name)
		require.NotNil(t, conn)
		err = conn.Close()
		require.NoError(t, err)
		t.Logf("✅ %s: connected and closed successfully", tc.Name)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Server Concurrent Connection Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestServer_ConcurrentConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	server := Server{
		Addr: "127.0.0.1:6310",
		HandleConnect: func(req ConnRequest) ConnType {
			return PUBLISH
		},
		HandlePublish: func(conn Conn) {
			<-ctx.Done()
		},
		Context:    ctx,
		ShutdownWg: &wg,
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		_ = server.Serve()
	}()

	time.Sleep(10 * time.Millisecond)

	// Connect multiple clients concurrently
	numClients := 5
	connChan := make(chan Conn, numClients)
	errChan := make(chan error, numClients)

	for i := 0; i < numClients; i++ {
		go func(idx int) {
			config := DefaultConfig()
			config.StreamId = "client"

			conn, err := Dial(ctx, "srt", "127.0.0.1:6310", config, &wg)
			if err != nil {
				errChan <- err
				return
			}
			connChan <- conn
		}(i)
	}

	// Collect results
	var connections []Conn
	for i := 0; i < numClients; i++ {
		select {
		case conn := <-connChan:
			connections = append(connections, conn)
		case err := <-errChan:
			t.Errorf("Connection %d failed: %v", i, err)
		case <-time.After(3 * time.Second):
			t.Fatal("Timeout waiting for connections")
		}
	}

	require.Equal(t, numClients, len(connections), "Expected %d concurrent connections", numClients)
	t.Logf("✅ Successfully established %d concurrent connections", numClients)

	// Close all connections
	for _, conn := range connections {
		conn.Close()
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Server Shutdown Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestServer_GracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	server := Server{
		Addr: "127.0.0.1:6311",
		HandleConnect: func(req ConnRequest) ConnType {
			return PUBLISH
		},
		HandlePublish: func(conn Conn) {
			<-ctx.Done()
		},
		Context:    ctx,
		ShutdownWg: &wg,
	}

	err := server.Listen()
	require.NoError(t, err)

	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve()
		close(serveDone)
	}()

	time.Sleep(10 * time.Millisecond)

	// Connect a client
	config := DefaultConfig()
	config.StreamId = "test"

	conn, err := Dial(ctx, "srt", "127.0.0.1:6311", config, &wg)
	require.NoError(t, err)

	// Trigger shutdown
	cancel()
	server.Shutdown()

	// Wait for server to stop
	select {
	case <-serveDone:
		t.Log("✅ Server shutdown gracefully")
	case <-time.After(5 * time.Second):
		t.Fatal("Server did not shutdown in time")
	}

	// Connection should be closed
	_, err = conn.Read(make([]byte, 1024))
	require.Error(t, err, "Read after shutdown should fail")
}

func TestServer_ShutdownBeforeServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	server := Server{
		Addr: "127.0.0.1:6312",
		HandleConnect: func(req ConnRequest) ConnType {
			return PUBLISH
		},
		Context:    ctx,
		ShutdownWg: &wg,
	}

	err := server.Listen()
	require.NoError(t, err)

	// Shutdown before Serve
	server.Shutdown()

	// Serve should return immediately
	err = server.Serve()
	require.Equal(t, ErrServerClosed, err, "Serve after shutdown should return ErrServerClosed")
	t.Log("✅ Server correctly returns ErrServerClosed when already shutdown")
}

// ═══════════════════════════════════════════════════════════════════════════
// Server Configuration Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestServer_CustomConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	config := DefaultConfig()
	config.Latency = 500 * time.Millisecond

	server := Server{
		Addr:   "127.0.0.1:6313",
		Config: &config,
		HandleConnect: func(req ConnRequest) ConnType {
			return PUBLISH
		},
		HandlePublish: func(conn Conn) {
			<-ctx.Done()
		},
		Context:    ctx,
		ShutdownWg: &wg,
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		_ = server.Serve()
	}()

	time.Sleep(10 * time.Millisecond)

	clientConfig := DefaultConfig()
	clientConfig.StreamId = "test"
	clientConfig.Latency = 500 * time.Millisecond

	conn, err := Dial(ctx, "srt", "127.0.0.1:6313", clientConfig, &wg)
	require.NoError(t, err)
	defer conn.Close()

	t.Log("✅ Server with custom config works correctly")
}
