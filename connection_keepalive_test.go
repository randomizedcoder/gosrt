package srt

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/congestion/live/receive"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Keepalive Echo Behavior Tests
// =============================================================================
// Tests for the keepalive echo/response mechanism that prevents infinite
// ping-pong loops between peers while preserving RTT measurement capability.
//
// SRT spec (Section 3.2.3) reserves TypeSpecific for future definition.
// GoSRT uses TypeSpecific to distinguish:
//   - KeepaliveTypeOriginal (0): Proactive keepalive, should be echoed
//   - KeepaliveTypeResponse (1): Echo response, must NOT be echoed
//
// Reference: documentation/draft-sharabayko-srt-01.txt Section 3.2.3
// =============================================================================

// sentPacket captures a packet sent via pop() for test verification
type sentPacket struct {
	controlType  packet.CtrlType
	typeSpecific uint32
}

// newTestConnForKeepalive creates a minimal srtConn suitable for keepalive tests.
// It sets up a sendFilter to capture sent packets and initializes the minimum
// fields required for dispatchKeepAlive/handleKeepAlive to function.
func newTestConnForKeepalive(t *testing.T) (*srtConn, *[]sentPacket) {
	t.Helper()

	var sent []sentPacket
	var mu sync.Mutex

	c := &srtConn{
		config: Config{
			PeerIdleTimeout: 5 * time.Second,
		},
		peerIdleTimeout: time.NewTimer(5 * time.Second),
		logger:          NewLogger(nil), // No-op logger (no topics enabled)
		onSend: func(p packet.Packet) {
			// no-op network send
		},
		sendFilter: func(p packet.Packet) bool {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, sentPacket{
				controlType:  p.Header().ControlType,
				typeSpecific: p.Header().TypeSpecific,
			})
			return false // Don't actually send on wire
		},
		controlHandlers: make(map[packet.CtrlType]controlPacketHandler),
		userHandlers:    make(map[packet.CtrlSubType]userPacketHandler),
		debugCtx:        newConnDebugContext(),
	}

	return c, &sent
}

// makeKeepalivePacket creates a KEEPALIVE packet with the given TypeSpecific value.
func makeKeepalivePacket(typeSpecific uint32) packet.Packet {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")
	p := packet.NewPacket(addr)
	p.Header().IsControlPacket = true
	p.Header().ControlType = packet.CTRLTYPE_KEEPALIVE
	p.Header().TypeSpecific = typeSpecific
	p.Header().Timestamp = 1000
	p.Header().DestinationSocketId = 42
	return p
}

// =============================================================================
// Table-Driven Tests: dispatchKeepAlive echo behavior
// =============================================================================

func TestDispatchKeepAlive_EchoBehavior_TableDriven(t *testing.T) {
	testCases := []struct {
		name             string
		typeSpecific     uint32
		expectEcho       bool
		expectEchoTypeTS uint32 // Expected TypeSpecific on echoed packet
		description      string
	}{
		{
			name:             "original keepalive is echoed as response",
			typeSpecific:     packet.KeepaliveTypeOriginal,
			expectEcho:       true,
			expectEchoTypeTS: packet.KeepaliveTypeResponse,
			description:      "Original keepalives (TypeSpecific=0) should be echoed with TypeSpecific=1 for RTT measurement",
		},
		{
			name:         "response keepalive is NOT echoed",
			typeSpecific: packet.KeepaliveTypeResponse,
			expectEcho:   false,
			description:  "Response keepalives (TypeSpecific=1) must NOT be echoed to prevent infinite ping-pong",
		},
		{
			name:         "unknown TypeSpecific value is NOT echoed",
			typeSpecific: 2,
			expectEcho:   false,
			description:  "Unknown TypeSpecific values should not be echoed (only explicit originals are echoed)",
		},
		{
			name:         "high TypeSpecific value is NOT echoed",
			typeSpecific: 0xFFFFFFFF,
			expectEcho:   false,
			description:  "Arbitrary high TypeSpecific values should not be echoed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, sent := newTestConnForKeepalive(t)

			p := makeKeepalivePacket(tc.typeSpecific)
			c.dispatchKeepAlive(p)

			if tc.expectEcho {
				require.Len(t, *sent, 1, "expected exactly one echoed packet")
				assert.Equal(t, packet.CTRLTYPE_KEEPALIVE, (*sent)[0].controlType, "echoed packet should be KEEPALIVE")
				assert.Equal(t, tc.expectEchoTypeTS, (*sent)[0].typeSpecific,
					"echoed packet TypeSpecific should be %d (response)", tc.expectEchoTypeTS)
			} else {
				require.Empty(t, *sent, "response keepalive must NOT be echoed (prevents infinite loop)")
			}
		})
	}
}

// =============================================================================
// Table-Driven Tests: sendProactiveKeepalive
// =============================================================================

func TestSendProactiveKeepalive_TypeSpecific(t *testing.T) {
	c, sent := newTestConnForKeepalive(t)

	c.sendProactiveKeepalive()

	require.Len(t, *sent, 1, "sendProactiveKeepalive should send exactly one packet")
	assert.Equal(t, packet.CTRLTYPE_KEEPALIVE, (*sent)[0].controlType)
	assert.Equal(t, packet.KeepaliveTypeOriginal, (*sent)[0].typeSpecific,
		"proactive keepalive must use TypeSpecific=0 (original) so peer echoes it for RTT")
}

// =============================================================================
// Table-Driven Tests: handleKeepAlive does NOT echo (Tick mode)
// =============================================================================

func TestHandleKeepAlive_NoEcho_TableDriven(t *testing.T) {
	testCases := []struct {
		name         string
		typeSpecific uint32
		description  string
	}{
		{
			name:         "original keepalive - no echo from handleKeepAlive",
			typeSpecific: packet.KeepaliveTypeOriginal,
			description:  "handleKeepAlive only resets idle timeout; echo is handled by dispatchKeepAlive",
		},
		{
			name:         "response keepalive - no echo from handleKeepAlive",
			typeSpecific: packet.KeepaliveTypeResponse,
			description:  "handleKeepAlive must never echo responses",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, sent := newTestConnForKeepalive(t)
			// Enter Tick context for AssertTickContext (no-op in release builds)
			c.EnterTick()
			defer c.ExitTick()

			p := makeKeepalivePacket(tc.typeSpecific)
			c.handleKeepAlive(p)

			require.Empty(t, *sent, "handleKeepAlive must NOT send any packets; echo is handled by dispatchKeepAlive")
		})
	}
}

// =============================================================================
// Regression Tests: Infinite ping-pong prevention
// =============================================================================

// TestRegression_KeepaliveNoPingPong simulates the full round-trip scenario
// that previously caused an infinite loop:
//
//	Peer A sends keepalive → Peer B echoes → Peer A echoes → infinite loop
//
// With the fix, Peer B echoes with TypeSpecific=1 (response), and when
// Peer A receives the response, it does NOT echo it back.
func TestRegression_KeepaliveNoPingPong(t *testing.T) {
	// Simulate two peers
	peerA, sentA := newTestConnForKeepalive(t)
	peerB, sentB := newTestConnForKeepalive(t)

	// Step 1: Peer A sends proactive keepalive (TypeSpecific=0)
	peerA.sendProactiveKeepalive()
	require.Len(t, *sentA, 1, "Peer A should send one proactive keepalive")
	assert.Equal(t, packet.KeepaliveTypeOriginal, (*sentA)[0].typeSpecific,
		"proactive keepalive should be original (TypeSpecific=0)")

	// Step 2: Peer B receives the original keepalive and dispatches it
	// Simulate the packet Peer B receives (original, TypeSpecific=0)
	keepaliveFromA := makeKeepalivePacket(packet.KeepaliveTypeOriginal)
	peerB.dispatchKeepAlive(keepaliveFromA)

	// Peer B should echo it back as a response (TypeSpecific=1)
	require.Len(t, *sentB, 1, "Peer B should echo the original keepalive")
	assert.Equal(t, packet.KeepaliveTypeResponse, (*sentB)[0].typeSpecific,
		"Peer B's echo should be a response (TypeSpecific=1)")

	// Step 3: Peer A receives the response (TypeSpecific=1)
	// This is the critical step - Peer A must NOT echo it back
	*sentA = (*sentA)[:0] // Clear Peer A's sent log
	responseFromB := makeKeepalivePacket(packet.KeepaliveTypeResponse)
	peerA.dispatchKeepAlive(responseFromB)

	// Peer A must NOT send anything - this breaks the loop
	require.Empty(t, *sentA,
		"REGRESSION: Peer A echoed a response keepalive, causing infinite ping-pong loop")
}

// TestRegression_KeepaliveNoPingPong_MultipleRounds extends the regression test
// to verify no echo accumulation over multiple keepalive cycles.
func TestRegression_KeepaliveNoPingPong_MultipleRounds(t *testing.T) {
	peerA, sentA := newTestConnForKeepalive(t)
	peerB, sentB := newTestConnForKeepalive(t)

	for round := range 10 {
		// Clear sent logs
		*sentA = (*sentA)[:0]
		*sentB = (*sentB)[:0]

		// Peer A sends proactive keepalive
		peerA.sendProactiveKeepalive()
		require.Len(t, *sentA, 1, "round %d: Peer A should send one keepalive", round)

		// Peer B receives and echoes
		keepaliveFromA := makeKeepalivePacket(packet.KeepaliveTypeOriginal)
		peerB.dispatchKeepAlive(keepaliveFromA)
		require.Len(t, *sentB, 1, "round %d: Peer B should echo once", round)

		// Peer A receives the response - must not echo
		*sentA = (*sentA)[:0]
		responseFromB := makeKeepalivePacket(packet.KeepaliveTypeResponse)
		peerA.dispatchKeepAlive(responseFromB)
		require.Empty(t, *sentA,
			"round %d: REGRESSION: response keepalive was echoed, causing infinite loop", round)
	}
}

// =============================================================================
// Idle Timeout Reset Tests
// =============================================================================

func TestDispatchKeepAlive_ResetsIdleTimeout_TableDriven(t *testing.T) {
	testCases := []struct {
		name         string
		typeSpecific uint32
		description  string
	}{
		{
			name:         "original keepalive resets idle timeout",
			typeSpecific: packet.KeepaliveTypeOriginal,
			description:  "Receiving any keepalive should reset peer idle timeout",
		},
		{
			name:         "response keepalive resets idle timeout",
			typeSpecific: packet.KeepaliveTypeResponse,
			description:  "Even response keepalives should reset peer idle timeout (peer is alive)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestConnForKeepalive(t)

			// Record initial timeout reset time
			initialReset := c.peerIdleTimeoutLastReset.Load()

			// Small delay to ensure timestamp changes
			time.Sleep(time.Millisecond)

			p := makeKeepalivePacket(tc.typeSpecific)
			c.dispatchKeepAlive(p)

			// Verify idle timeout was reset (timestamp advanced)
			newReset := c.peerIdleTimeoutLastReset.Load()
			assert.Greater(t, newReset, initialReset,
				"peer idle timeout should be reset after receiving %s keepalive", tc.name)
		})
	}
}

// =============================================================================
// Control Ring Path Tests
// =============================================================================

func TestDispatchKeepAlive_WithControlRing_EchoBehavior(t *testing.T) {
	testCases := []struct {
		name         string
		typeSpecific uint32
		expectEcho   bool
		description  string
	}{
		{
			name:         "original via control ring path - echoed",
			typeSpecific: packet.KeepaliveTypeOriginal,
			expectEcho:   true,
			description:  "Original keepalives should be echoed even when routed through control ring",
		},
		{
			name:         "response via control ring path - NOT echoed",
			typeSpecific: packet.KeepaliveTypeResponse,
			expectEcho:   false,
			description:  "Response keepalives must not be echoed even when routed through control ring",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, sent := newTestConnForKeepalive(t)

			// Enable control ring (EventLoop path)
			ring, err := receive.NewRecvControlRing(128, 1)
			require.NoError(t, err)
			c.recvControlRing = ring
			c.metrics = &metrics.ConnectionMetrics{}

			p := makeKeepalivePacket(tc.typeSpecific)
			c.dispatchKeepAlive(p)

			if tc.expectEcho {
				require.Len(t, *sent, 1, "expected echo via control ring path")
				assert.Equal(t, packet.KeepaliveTypeResponse, (*sent)[0].typeSpecific)
			} else {
				require.Empty(t, *sent, "response keepalive must NOT be echoed via control ring path")
			}

			// Verify the keepalive was pushed to the control ring for EventLoop processing
			cp, ok := ring.TryPop()
			require.True(t, ok, "keepalive should be pushed to control ring for idle timeout processing")
			assert.Equal(t, receive.RecvControlTypeKEEPALIVE, cp.Type)
		})
	}
}

// =============================================================================
// Packet Constants Tests
// =============================================================================

func TestKeepaliveTypeSpecificConstants(t *testing.T) {
	testCases := []struct {
		name     string
		value    uint32
		expected uint32
	}{
		{
			name:     "KeepaliveTypeOriginal is 0",
			value:    packet.KeepaliveTypeOriginal,
			expected: 0,
		},
		{
			name:     "KeepaliveTypeResponse is 1",
			value:    packet.KeepaliveTypeResponse,
			expected: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.value)
		})
	}

	// Verify they are distinct
	assert.NotEqual(t, packet.KeepaliveTypeOriginal, packet.KeepaliveTypeResponse,
		"Original and Response TypeSpecific values must be distinct")
}

// =============================================================================
// Wire Format Round-Trip Tests
// =============================================================================

// TestKeepaliveTypeSpecific_WireRoundTrip verifies that the TypeSpecific field
// survives marshal/unmarshal round-trip, which is essential for the echo
// mechanism to work across the network.
func TestKeepaliveTypeSpecific_WireRoundTrip(t *testing.T) {
	testCases := []struct {
		name         string
		typeSpecific uint32
	}{
		{"original (TypeSpecific=0)", packet.KeepaliveTypeOriginal},
		{"response (TypeSpecific=1)", packet.KeepaliveTypeResponse},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

			// Create and marshal
			original := packet.NewPacket(addr)
			original.Header().IsControlPacket = true
			original.Header().ControlType = packet.CTRLTYPE_KEEPALIVE
			original.Header().TypeSpecific = tc.typeSpecific
			original.Header().Timestamp = 12345
			original.Header().DestinationSocketId = 0xABCD

			var buf bytes.Buffer
			err := original.Marshal(&buf)
			require.NoError(t, err)

			// Unmarshal into new packet
			decoded := packet.NewPacket(addr)
			err = decoded.Unmarshal(buf.Bytes())
			require.NoError(t, err)

			// Verify TypeSpecific survived round-trip
			assert.Equal(t, tc.typeSpecific, decoded.Header().TypeSpecific,
				"TypeSpecific must survive marshal/unmarshal round-trip for keepalive echo to work across the network")
			assert.Equal(t, packet.CTRLTYPE_KEEPALIVE, decoded.Header().ControlType)
			assert.True(t, decoded.Header().IsControlPacket)
		})
	}
}
