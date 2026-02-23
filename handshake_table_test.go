package srt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Handshake Protocol Table-Driven Tests
// Based on SRT RFC draft-sharabayko-srt-01 Section 4.3
// ═══════════════════════════════════════════════════════════════════════════
//
// KEY CORNER CASES FROM RFC:
// 1. Caller-Listener handshake: INDUCTION → INDUCTION(cookie) → CONCLUSION → CONCLUSION
// 2. Rendezvous handshake: WAVEAHAND → CONCLUSION → AGREEMENT
// 3. Rejection codes (Table 7): REJ_UNKNOWN through REJ_GROUP
// 4. Version negotiation: Version 4 (caller) → Version 5 (listener)
// 5. Extension Field Magic: 0x4A17 (SRT identifier)
// 6. Encryption negotiation: AES-128, AES-192, AES-256
// 7. SYN Cookie validation (DoS prevention)
// ═══════════════════════════════════════════════════════════════════════════

// HandshakeTestCase defines a table-driven test case for handshake scenarios
type HandshakeTestCase struct {
	// Test identification
	Name string

	// CODE_PARAMs - production parameters affecting handshake
	Passphrase    string        // Encryption passphrase (empty = no encryption)
	StreamId      string        // Stream identifier
	Latency       time.Duration // Latency setting
	HandshakeTimeout time.Duration // Handshake timeout (0 = use default)

	// TEST_INFRA - test mechanics
	ServerRejects     bool            // Server should reject connection
	RejectionReason   RejectionReason // Reason for rejection
	InvalidCookie     bool            // Send invalid SYN cookie
	InvalidVersion    bool            // Send invalid protocol version
	MissingExtensions bool            // Missing required extensions
	CloseServerEarly  bool            // Close server during handshake

	// EXPECTATIONS
	ExpectSuccess       bool   // Expect connection to succeed
	ExpectError         bool   // Expect an error
	ExpectErrorContains string // Expected error message substring
}

// handshakeTests is the test table for handshake scenarios
var handshakeTests = []HandshakeTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Core Success Scenarios (per RFC Section 4.3.1)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "BasicConnection",
		StreamId:      "test-stream",
		ExpectSuccess: true,
	},
	{
		Name:          "ConnectionWithStreamId",
		StreamId:      "my-custom-stream-id",
		ExpectSuccess: true,
	},
	{
		Name:          "ConnectionWithLatency",
		StreamId:      "test-latency",
		Latency:       200 * time.Millisecond,
		ExpectSuccess: true,
	},
	{
		Name:          "ConnectionWithPassphrase_AES128",
		StreamId:      "test-encrypted",
		Passphrase:    "test-passphrase-16",
		ExpectSuccess: true,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Rejection Scenarios (per RFC Table 7)
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                "Rejection_REJ_PEER",
		StreamId:            "reject-peer",
		ServerRejects:       true,
		RejectionReason:     REJ_PEER,
		ExpectSuccess:       false,
		ExpectError:         true,
		ExpectErrorContains: "rejected",
	},
	{
		Name:                "Rejection_REJ_CLOSE",
		StreamId:            "reject-close",
		ServerRejects:       true,
		RejectionReason:     REJ_CLOSE,
		ExpectSuccess:       false,
		ExpectError:         true,
		ExpectErrorContains: "rejected",
	},
	{
		Name:                "Rejection_REJ_BADSECRET",
		StreamId:            "reject-secret",
		Passphrase:          "wrong-passphrase-xx",
		ServerRejects:       true,
		RejectionReason:     REJ_BADSECRET,
		ExpectSuccess:       false,
		ExpectError:         true,
		ExpectErrorContains: "rejected",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Timeout Scenarios
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                "HandshakeTimeout_ServerUnresponsive",
		StreamId:            "timeout-test",
		CloseServerEarly:    true,
		HandshakeTimeout:    500 * time.Millisecond,
		ExpectSuccess:       false,
		ExpectError:         true,
		ExpectErrorContains: "timeout",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Corner Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Corner_EmptyStreamId",
		StreamId:      "",
		ExpectSuccess: true,
	},
	{
		Name:          "Corner_LongStreamId",
		StreamId:      "very-long-stream-identifier-that-tests-the-maximum-length-handling-in-the-protocol-implementation",
		ExpectSuccess: true,
	},
	{
		Name:          "Corner_SpecialCharsStreamId",
		StreamId:      "stream/test:123?param=value",
		ExpectSuccess: true,
	},
	{
		Name:          "Corner_MinimumLatency",
		StreamId:      "min-latency",
		Latency:       50 * time.Millisecond, // Minimum practical latency (must be > 2 * PeriodicNakInterval)
		ExpectSuccess: true,
	},
	{
		Name:          "Corner_LargeLatency",
		StreamId:      "large-latency",
		Latency:       5 * time.Second,
		ExpectSuccess: true,
	},
}

// TestHandshake_Table runs all handshake table tests
func TestHandshake_Table(t *testing.T) {
	basePort := 6200

	for i, tc := range handshakeTests {
		port := basePort + i
		tc := tc // capture range variable
		t.Run(tc.Name, func(t *testing.T) {
			runHandshakeTest(t, tc, port)
		})
	}
}

// runHandshakeTest executes a single handshake test case
func runHandshakeTest(t *testing.T, tc HandshakeTestCase, port int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Create server config
	serverConfig := DefaultConfig()
	if tc.Latency > 0 {
		serverConfig.ReceiverLatency = tc.Latency
	}
	// Note: Server passphrase is set via req.SetPassphrase() in HandleConnect,
	// not in the server config (per SRT encryption handshake flow)

	addr := "127.0.0.1:" + itoa(port)
	connReceived := make(chan struct{})
	serverReady := make(chan struct{})

	server := Server{
		Addr:       addr,
		Config:     &serverConfig,
		Context:    ctx,
		ShutdownWg: &wg,
		HandleConnect: func(req ConnRequest) ConnType {
			if tc.ServerRejects {
				req.Reject(tc.RejectionReason)
				return REJECT
			}
			// For encrypted connections, server must verify passphrase
			if tc.Passphrase != "" && req.IsEncrypted() {
				if err := req.SetPassphrase(tc.Passphrase); err != nil {
					return REJECT
				}
			}
			return PUBLISH
		},
		HandlePublish: func(conn Conn) {
			select {
			case <-connReceived:
			default:
				close(connReceived)
			}
			<-ctx.Done()
		},
	}

	if tc.CloseServerEarly {
		// For timeout tests, don't actually start the server properly
		t.Logf("Testing timeout scenario - server won't respond")
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	// Start server in background
	go func() {
		close(serverReady)
		if !tc.CloseServerEarly {
			_ = server.Serve()
		}
	}()

	// Wait for server to be ready
	<-serverReady
	time.Sleep(50 * time.Millisecond)

	// Connect client
	clientConfig := DefaultConfig()
	clientConfig.StreamId = tc.StreamId
	if tc.Latency > 0 {
		clientConfig.Latency = tc.Latency
	}
	if tc.Passphrase != "" {
		clientConfig.Passphrase = tc.Passphrase
	}
	if tc.HandshakeTimeout > 0 {
		clientConfig.HandshakeTimeout = tc.HandshakeTimeout
	}

	conn, err := Dial(ctx, "srt", addr, clientConfig, &wg)

	if tc.ExpectSuccess {
		require.NoError(t, err, "Expected connection to succeed")
		require.NotNil(t, conn, "Connection should not be nil")
		conn.Close()
		t.Logf("✅ %s: handshake succeeded", tc.Name)
	} else if tc.ExpectError {
		require.Error(t, err, "Expected connection to fail")
		if tc.ExpectErrorContains != "" {
			require.Contains(t, err.Error(), tc.ExpectErrorContains,
				"Error should contain expected message")
		}
		t.Logf("✅ %s: handshake failed as expected: %v", tc.Name, err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Handshake Type Tests (per RFC Section 3.2.1)
// ═══════════════════════════════════════════════════════════════════════════

func TestHandshakeType_String(t *testing.T) {
	tests := []struct {
		name     string
		hsType   packet.HandshakeType
		expected string
	}{
		{"Done", packet.HSTYPE_DONE, "DONE"},
		{"Agreement", packet.HSTYPE_AGREEMENT, "AGREEMENT"},
		{"Conclusion", packet.HSTYPE_CONCLUSION, "CONCLUSION"},
		{"Wavehand", packet.HSTYPE_WAVEHAND, "WAVEHAND"},
		{"Induction", packet.HSTYPE_INDUCTION, "INDUCTION"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.hsType.String())
		})
	}
}

func TestHandshakeType_IsHandshake(t *testing.T) {
	tests := []struct {
		name        string
		hsType      packet.HandshakeType
		isHandshake bool
	}{
		{"Done", packet.HSTYPE_DONE, true},
		{"Agreement", packet.HSTYPE_AGREEMENT, true},
		{"Conclusion", packet.HSTYPE_CONCLUSION, true},
		{"Wavehand", packet.HSTYPE_WAVEHAND, true},
		{"Induction", packet.HSTYPE_INDUCTION, true},
		{"Rejection_1000", packet.HandshakeType(1000), false},
		{"Rejection_REJ_PEER", packet.HandshakeType(REJ_PEER), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.isHandshake, tc.hsType.IsHandshake())
		})
	}
}

func TestHandshakeType_IsRejection(t *testing.T) {
	tests := []struct {
		name        string
		hsType      packet.HandshakeType
		isRejection bool
	}{
		{"Done_not_rejection", packet.HSTYPE_DONE, false},
		{"Agreement_not_rejection", packet.HSTYPE_AGREEMENT, false},
		{"Conclusion_not_rejection", packet.HSTYPE_CONCLUSION, false},
		{"Rejection_REJ_UNKNOWN", packet.HandshakeType(REJ_UNKNOWN), true},
		{"Rejection_REJ_PEER", packet.HandshakeType(REJ_PEER), true},
		{"Rejection_REJ_ROGUE", packet.HandshakeType(REJ_ROGUE), true},
		{"Rejection_REJ_VERSION", packet.HandshakeType(REJ_VERSION), true},
		{"Rejection_REJ_BADSECRET", packet.HandshakeType(REJ_BADSECRET), true},
		{"Rejection_REJ_CONGESTION", packet.HandshakeType(REJ_CONGESTION), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.isRejection, tc.hsType.IsRejection())
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Rejection Reason Tests (per RFC Table 7)
// ═══════════════════════════════════════════════════════════════════════════

func TestRejectionReasons_Values(t *testing.T) {
	// Verify RFC-specified rejection codes
	tests := []struct {
		name   string
		reason RejectionReason
		code   uint32
	}{
		{"REJ_UNKNOWN", REJ_UNKNOWN, 1000},
		{"REJ_SYSTEM", REJ_SYSTEM, 1001},
		{"REJ_PEER", REJ_PEER, 1002},
		{"REJ_RESOURCE", REJ_RESOURCE, 1003},
		{"REJ_ROGUE", REJ_ROGUE, 1004},
		{"REJ_BACKLOG", REJ_BACKLOG, 1005},
		{"REJ_IPE", REJ_IPE, 1006},
		{"REJ_CLOSE", REJ_CLOSE, 1007},
		{"REJ_VERSION", REJ_VERSION, 1008},
		{"REJ_RDVCOOKIE", REJ_RDVCOOKIE, 1009},
		{"REJ_BADSECRET", REJ_BADSECRET, 1010},
		{"REJ_UNSECURE", REJ_UNSECURE, 1011},
		{"REJ_MESSAGEAPI", REJ_MESSAGEAPI, 1012},
		{"REJ_CONGESTION", REJ_CONGESTION, 1013},
		{"REJ_FILTER", REJ_FILTER, 1014},
		{"REJ_GROUP", REJ_GROUP, 1015},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.code, uint32(tc.reason),
				"Rejection code mismatch for %s", tc.name)
		})
	}
}

// Note: SYN Cookie tests are in net/syncookie_test.go
// They cover DoS prevention per RFC Section 4.3.1.1

