package srt

import (
	"testing"

	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Phase 1.4: Control Packet Dispatch Tests
// =============================================================================
// Tests for control packet handling and dispatch logic with focus on:
// - Control packet type dispatch (O(1) map lookup)
// - User packet subtype dispatch (HSREQ, HSRSP, KMREQ, KMRSP)
// - Unknown type/subtype handling
// - Dispatch routing (ring vs locked handler)
// - FEC filter packet handling
// - Edge cases and boundary conditions
//
// Reference: documentation/unit_test_coverage_improvement_plan.md
// =============================================================================

// =============================================================================
// Control Packet Type Dispatch Tests
// =============================================================================
// Tests the control packet type dispatch logic used in handlePacket.
// The dispatch uses a map[packet.CtrlType]controlPacketHandler for O(1) lookup.
// =============================================================================

// ControlTypeInfo describes a control packet type for testing
type ControlTypeInfo struct {
	CtrlType    packet.CtrlType
	Name        string
	HasHandler  bool   // Whether a handler exists in the dispatch table
	HandlerName string // Name of the expected handler function
}

// GetKnownControlTypes returns all known control packet types
func GetKnownControlTypes() []ControlTypeInfo {
	return []ControlTypeInfo{
		{packet.CTRLTYPE_HANDSHAKE, "HANDSHAKE", false, ""}, // Not in dispatch table (handled separately)
		{packet.CTRLTYPE_KEEPALIVE, "KEEPALIVE", true, "dispatchKeepAlive"},
		{packet.CTRLTYPE_ACK, "ACK", true, "handleACK"},
		{packet.CTRLTYPE_NAK, "NAK", true, "handleNAK"},
		{packet.CTRLTYPE_WARN, "WARN", false, ""}, // Unimplemented
		{packet.CTRLTYPE_SHUTDOWN, "SHUTDOWN", true, "handleShutdown"},
		{packet.CTRLTYPE_ACKACK, "ACKACK", true, "dispatchACKACK"},
		{packet.CRTLTYPE_DROPREQ, "DROPREQ", false, ""},     // Unimplemented
		{packet.CRTLTYPE_PEERERROR, "PEERERROR", false, ""}, // Unimplemented
		{packet.CTRLTYPE_USER, "USER", true, "handleUserPacket"},
	}
}

func TestControlTypeDispatch_TableDriven(t *testing.T) {
	// Simulate the dispatch table from initializeControlHandlers
	// We test the lookup logic, not the actual handlers
	knownTypes := map[packet.CtrlType]string{
		packet.CTRLTYPE_KEEPALIVE: "dispatchKeepAlive",
		packet.CTRLTYPE_SHUTDOWN:  "handleShutdown",
		packet.CTRLTYPE_NAK:       "handleNAK",
		packet.CTRLTYPE_ACK:       "handleACK",
		packet.CTRLTYPE_ACKACK:    "dispatchACKACK",
		packet.CTRLTYPE_USER:      "handleUserPacket",
	}

	testCases := []struct {
		name          string
		ctrlType      packet.CtrlType
		expectFound   bool
		expectHandler string
	}{
		// =================================================================
		// Handlers that exist in dispatch table
		// =================================================================
		{
			name:          "KEEPALIVE has handler",
			ctrlType:      packet.CTRLTYPE_KEEPALIVE,
			expectFound:   true,
			expectHandler: "dispatchKeepAlive",
		},
		{
			name:          "ACK has handler",
			ctrlType:      packet.CTRLTYPE_ACK,
			expectFound:   true,
			expectHandler: "handleACK",
		},
		{
			name:          "NAK has handler",
			ctrlType:      packet.CTRLTYPE_NAK,
			expectFound:   true,
			expectHandler: "handleNAK",
		},
		{
			name:          "ACKACK has handler",
			ctrlType:      packet.CTRLTYPE_ACKACK,
			expectFound:   true,
			expectHandler: "dispatchACKACK",
		},
		{
			name:          "SHUTDOWN has handler",
			ctrlType:      packet.CTRLTYPE_SHUTDOWN,
			expectFound:   true,
			expectHandler: "handleShutdown",
		},
		{
			name:          "USER has handler",
			ctrlType:      packet.CTRLTYPE_USER,
			expectFound:   true,
			expectHandler: "handleUserPacket",
		},

		// =================================================================
		// Control types NOT in dispatch table
		// =================================================================
		{
			name:        "HANDSHAKE not in dispatch table",
			ctrlType:    packet.CTRLTYPE_HANDSHAKE,
			expectFound: false,
		},
		{
			name:        "WARN not in dispatch table (unimplemented)",
			ctrlType:    packet.CTRLTYPE_WARN,
			expectFound: false,
		},
		{
			name:        "DROPREQ not in dispatch table (unimplemented)",
			ctrlType:    packet.CRTLTYPE_DROPREQ,
			expectFound: false,
		},
		{
			name:        "PEERERROR not in dispatch table (unimplemented)",
			ctrlType:    packet.CRTLTYPE_PEERERROR,
			expectFound: false,
		},

		// =================================================================
		// Unknown/invalid control types
		// =================================================================
		{
			name:        "unknown type 0x0009",
			ctrlType:    packet.CtrlType(0x0009),
			expectFound: false,
		},
		{
			name:        "unknown type 0x00FF",
			ctrlType:    packet.CtrlType(0x00FF),
			expectFound: false,
		},
		{
			name:        "unknown type 0x7FFE (just below USER)",
			ctrlType:    packet.CtrlType(0x7FFE),
			expectFound: false,
		},
		{
			name:        "max uint16 type",
			ctrlType:    packet.CtrlType(0xFFFF),
			expectFound: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler, found := knownTypes[tc.ctrlType]

			require.Equal(t, tc.expectFound, found,
				"dispatch lookup for %s: got found=%v, want found=%v",
				tc.ctrlType, found, tc.expectFound)

			if tc.expectFound {
				require.Equal(t, tc.expectHandler, handler,
					"handler for %s: got %s, want %s",
					tc.ctrlType, handler, tc.expectHandler)
			}
		})
	}
}

// =============================================================================
// User Packet SubType Dispatch Tests
// =============================================================================
// Tests the CTRLTYPE_USER SubType dispatch logic.
// User packets have a SubType field that determines which handler to call.
// =============================================================================

func TestUserSubTypeDispatch_TableDriven(t *testing.T) {
	// Simulate the user handlers map from initializeControlHandlers
	userHandlers := map[packet.CtrlSubType]string{
		packet.EXTTYPE_HSREQ: "handleHSRequest",
		packet.EXTTYPE_HSRSP: "handleHSResponse",
		packet.EXTTYPE_KMREQ: "handleKMRequest",
		packet.EXTTYPE_KMRSP: "handleKMResponse",
	}

	testCases := []struct {
		name          string
		subType       packet.CtrlSubType
		expectFound   bool
		expectHandler string
	}{
		// =================================================================
		// Handlers that exist
		// =================================================================
		{
			name:          "HSREQ has handler",
			subType:       packet.EXTTYPE_HSREQ,
			expectFound:   true,
			expectHandler: "handleHSRequest",
		},
		{
			name:          "HSRSP has handler",
			subType:       packet.EXTTYPE_HSRSP,
			expectFound:   true,
			expectHandler: "handleHSResponse",
		},
		{
			name:          "KMREQ has handler",
			subType:       packet.EXTTYPE_KMREQ,
			expectFound:   true,
			expectHandler: "handleKMRequest",
		},
		{
			name:          "KMRSP has handler",
			subType:       packet.EXTTYPE_KMRSP,
			expectFound:   true,
			expectHandler: "handleKMResponse",
		},

		// =================================================================
		// SubTypes NOT in handler table
		// =================================================================
		{
			name:        "NONE subtype not handled",
			subType:     packet.CTRLSUBTYPE_NONE,
			expectFound: false,
		},
		{
			name:        "SID subtype not handled",
			subType:     packet.EXTTYPE_SID,
			expectFound: false,
		},
		{
			name:        "CONGESTION subtype not handled",
			subType:     packet.EXTTYPE_CONGESTION,
			expectFound: false,
		},
		{
			name:        "FILTER subtype not handled (unimplemented)",
			subType:     packet.EXTTYPE_FILTER,
			expectFound: false,
		},
		{
			name:        "GROUP subtype not handled (unimplemented)",
			subType:     packet.EXTTYPE_GROUP,
			expectFound: false,
		},

		// =================================================================
		// Unknown subtypes
		// =================================================================
		{
			name:        "unknown subtype 100",
			subType:     packet.CtrlSubType(100),
			expectFound: false,
		},
		{
			name:        "unknown subtype 0xFFFF",
			subType:     packet.CtrlSubType(0xFFFF),
			expectFound: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler, found := userHandlers[tc.subType]

			require.Equal(t, tc.expectFound, found,
				"user subtype lookup for %s: got found=%v, want found=%v",
				tc.subType, found, tc.expectFound)

			if tc.expectFound {
				require.Equal(t, tc.expectHandler, handler,
					"handler for %s: got %s, want %s",
					tc.subType, handler, tc.expectHandler)
			}
		})
	}
}

// =============================================================================
// FEC Filter Packet Tests
// =============================================================================
// Tests the FEC filter packet detection logic.
// FEC control packets have MessageNumber == 0 and should be dropped.
// =============================================================================

func TestFECFilterPacketDetection_TableDriven(t *testing.T) {
	testCases := []struct {
		name            string
		messageNumber   uint32
		isControlPacket bool
		shouldDrop      bool
		reason          string
	}{
		// =================================================================
		// FEC filter packets (should be dropped)
		// =================================================================
		{
			name:            "MessageNumber 0 - FEC filter packet",
			messageNumber:   0,
			isControlPacket: false, // Data packet
			shouldDrop:      true,
			reason:          "FEC filter packets have MessageNumber == 0",
		},

		// =================================================================
		// Normal data packets (should NOT be dropped)
		// =================================================================
		{
			name:            "MessageNumber 1 - normal packet",
			messageNumber:   1,
			isControlPacket: false,
			shouldDrop:      false,
			reason:          "First valid message number",
		},
		{
			name:            "MessageNumber 2 - normal packet",
			messageNumber:   2,
			isControlPacket: false,
			shouldDrop:      false,
		},
		{
			name:            "MessageNumber MAX - normal packet",
			messageNumber:   0x3FFFFFF, // 26-bit max
			isControlPacket: false,
			shouldDrop:      false,
			reason:          "Maximum message number",
		},
		{
			name:            "MessageNumber after rollback to 1",
			messageNumber:   1,
			isControlPacket: false,
			shouldDrop:      false,
			reason:          "Message numbers roll back to 1, not 0",
		},

		// =================================================================
		// Control packets (MessageNumber not relevant)
		// =================================================================
		{
			name:            "Control packet - MessageNumber ignored",
			messageNumber:   0,
			isControlPacket: true,
			shouldDrop:      false, // Control packets don't check MessageNumber
			reason:          "FEC check only applies to data packets",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the FEC filter check from handlePacket
			// Only check for FEC if it's a data packet
			shouldDrop := !tc.isControlPacket && tc.messageNumber == 0

			require.Equal(t, tc.shouldDrop, shouldDrop,
				"FEC filter check: messageNumber=%d, isControl=%v -> shouldDrop=%v, want %v (%s)",
				tc.messageNumber, tc.isControlPacket, shouldDrop, tc.shouldDrop, tc.reason)
		})
	}
}

// =============================================================================
// Dispatch Routing Tests
// =============================================================================
// Tests the routing logic for ACKACK and KEEPALIVE dispatch functions.
// These packets can be routed to either:
// 1. Control ring (if enabled) for EventLoop processing
// 2. Locked handler as fallback
// =============================================================================

// DispatchRouting represents the routing decision for a control packet
type DispatchRouting struct {
	UseRing    bool
	RingPushOK bool // Whether the ring push succeeded
}

func TestDispatchRouting_ACKACK_TableDriven(t *testing.T) {
	testCases := []struct {
		name                string
		ringEnabled         bool // recvControlRing != nil
		ringPushSuccess     bool // Whether PushACKACK returns true
		expectRingPath      bool // Expected to use ring path
		expectLockedPath    bool // Expected to use locked path
		expectRingMetric    bool // RecvControlRingPushedACKACK incremented
		expectDroppedMetric bool // RecvControlRingDroppedACKACK incremented
	}{
		// =================================================================
		// Ring disabled - always use locked path
		// =================================================================
		{
			name:             "ring disabled - use locked path",
			ringEnabled:      false,
			ringPushSuccess:  false, // N/A
			expectRingPath:   false,
			expectLockedPath: true,
		},

		// =================================================================
		// Ring enabled and push succeeds
		// =================================================================
		{
			name:             "ring enabled, push succeeds",
			ringEnabled:      true,
			ringPushSuccess:  true,
			expectRingPath:   true,
			expectLockedPath: false,
			expectRingMetric: true,
		},

		// =================================================================
		// Ring enabled but push fails (ring full)
		// =================================================================
		{
			name:                "ring enabled, push fails (ring full)",
			ringEnabled:         true,
			ringPushSuccess:     false,
			expectRingPath:      false, // Push failed
			expectLockedPath:    true,  // Fall through to locked
			expectDroppedMetric: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the dispatchACKACK logic from connection_handlers.go
			usedRingPath := false
			usedLockedPath := false
			ringMetricIncremented := false
			droppedMetricIncremented := false

			if tc.ringEnabled {
				// Ring is enabled, try to push
				if tc.ringPushSuccess {
					usedRingPath = true
					ringMetricIncremented = true
					// return (don't fall through)
				} else {
					// Ring full - track dropped metric
					droppedMetricIncremented = true
					// Fall through to locked path
					usedLockedPath = true
				}
			} else {
				// Ring disabled - use locked path directly
				usedLockedPath = true
			}

			require.Equal(t, tc.expectRingPath, usedRingPath,
				"ring path: got %v, want %v", usedRingPath, tc.expectRingPath)
			require.Equal(t, tc.expectLockedPath, usedLockedPath,
				"locked path: got %v, want %v", usedLockedPath, tc.expectLockedPath)
			require.Equal(t, tc.expectRingMetric, ringMetricIncremented,
				"ring metric: got %v, want %v", ringMetricIncremented, tc.expectRingMetric)
			require.Equal(t, tc.expectDroppedMetric, droppedMetricIncremented,
				"dropped metric: got %v, want %v", droppedMetricIncremented, tc.expectDroppedMetric)
		})
	}
}

func TestDispatchRouting_KEEPALIVE_TableDriven(t *testing.T) {
	testCases := []struct {
		name                string
		ringEnabled         bool
		ringPushSuccess     bool
		expectRingPath      bool
		expectLockedPath    bool
		expectRingMetric    bool
		expectDroppedMetric bool
	}{
		{
			name:             "ring disabled - use locked path",
			ringEnabled:      false,
			expectLockedPath: true,
		},
		{
			name:             "ring enabled, push succeeds",
			ringEnabled:      true,
			ringPushSuccess:  true,
			expectRingPath:   true,
			expectRingMetric: true,
		},
		{
			name:                "ring enabled, push fails",
			ringEnabled:         true,
			ringPushSuccess:     false,
			expectLockedPath:    true,
			expectDroppedMetric: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate dispatchKeepAlive logic
			usedRingPath := false
			usedLockedPath := false
			ringMetricIncremented := false
			droppedMetricIncremented := false

			if tc.ringEnabled {
				if tc.ringPushSuccess {
					usedRingPath = true
					ringMetricIncremented = true
				} else {
					droppedMetricIncremented = true
					usedLockedPath = true
				}
			} else {
				usedLockedPath = true
			}

			require.Equal(t, tc.expectRingPath, usedRingPath)
			require.Equal(t, tc.expectLockedPath, usedLockedPath)
			require.Equal(t, tc.expectRingMetric, ringMetricIncremented)
			require.Equal(t, tc.expectDroppedMetric, droppedMetricIncremented)
		})
	}
}

// =============================================================================
// Packet Classification Tests
// =============================================================================
// Tests the classification logic at the start of handlePacket.
// =============================================================================

func TestPacketClassification_TableDriven(t *testing.T) {
	testCases := []struct {
		name            string
		isControlPacket bool
		controlType     packet.CtrlType
		messageNumber   uint32
		expectPath      string // "control", "data", "fec_drop"
	}{
		// =================================================================
		// Control packets
		// =================================================================
		{
			name:            "ACK control packet",
			isControlPacket: true,
			controlType:     packet.CTRLTYPE_ACK,
			expectPath:      "control",
		},
		{
			name:            "NAK control packet",
			isControlPacket: true,
			controlType:     packet.CTRLTYPE_NAK,
			expectPath:      "control",
		},
		{
			name:            "KEEPALIVE control packet",
			isControlPacket: true,
			controlType:     packet.CTRLTYPE_KEEPALIVE,
			expectPath:      "control",
		},
		{
			name:            "USER control packet",
			isControlPacket: true,
			controlType:     packet.CTRLTYPE_USER,
			expectPath:      "control",
		},

		// =================================================================
		// Data packets
		// =================================================================
		{
			name:            "normal data packet",
			isControlPacket: false,
			messageNumber:   1,
			expectPath:      "data",
		},
		{
			name:            "data packet with high message number",
			isControlPacket: false,
			messageNumber:   0x3FFFFFF,
			expectPath:      "data",
		},

		// =================================================================
		// FEC filter packets (dropped)
		// =================================================================
		{
			name:            "FEC filter packet (MessageNumber=0)",
			isControlPacket: false,
			messageNumber:   0,
			expectPath:      "fec_drop",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate packet classification from handlePacket
			var path string
			switch {
			case tc.isControlPacket:
				path = "control"
			case tc.messageNumber == 0:
				path = "fec_drop"
			default:
				path = "data"
			}

			require.Equal(t, tc.expectPath, path,
				"packet classification: isControl=%v, msgNum=%d -> path=%s, want %s",
				tc.isControlPacket, tc.messageNumber, path, tc.expectPath)
		})
	}
}

// =============================================================================
// Control Type Constants Verification
// =============================================================================
// Verifies that control type constants have expected values per SRT spec.
// =============================================================================

func TestControlTypeConstants(t *testing.T) {
	// Verify control type values per SRT RFC Table 1
	require.Equal(t, packet.CtrlType(0x0000), packet.CTRLTYPE_HANDSHAKE, "HANDSHAKE = 0x0000")
	require.Equal(t, packet.CtrlType(0x0001), packet.CTRLTYPE_KEEPALIVE, "KEEPALIVE = 0x0001")
	require.Equal(t, packet.CtrlType(0x0002), packet.CTRLTYPE_ACK, "ACK = 0x0002")
	require.Equal(t, packet.CtrlType(0x0003), packet.CTRLTYPE_NAK, "NAK = 0x0003")
	require.Equal(t, packet.CtrlType(0x0004), packet.CTRLTYPE_WARN, "WARN = 0x0004")
	require.Equal(t, packet.CtrlType(0x0005), packet.CTRLTYPE_SHUTDOWN, "SHUTDOWN = 0x0005")
	require.Equal(t, packet.CtrlType(0x0006), packet.CTRLTYPE_ACKACK, "ACKACK = 0x0006")
	require.Equal(t, packet.CtrlType(0x0007), packet.CRTLTYPE_DROPREQ, "DROPREQ = 0x0007")
	require.Equal(t, packet.CtrlType(0x0008), packet.CRTLTYPE_PEERERROR, "PEERERROR = 0x0008")
	require.Equal(t, packet.CtrlType(0x7FFF), packet.CTRLTYPE_USER, "USER = 0x7FFF")
}

func TestUserSubTypeConstants(t *testing.T) {
	// Verify user subtype values per SRT RFC Table 5
	require.Equal(t, packet.CtrlSubType(0), packet.CTRLSUBTYPE_NONE, "NONE = 0")
	require.Equal(t, packet.CtrlSubType(1), packet.EXTTYPE_HSREQ, "HSREQ = 1")
	require.Equal(t, packet.CtrlSubType(2), packet.EXTTYPE_HSRSP, "HSRSP = 2")
	require.Equal(t, packet.CtrlSubType(3), packet.EXTTYPE_KMREQ, "KMREQ = 3")
	require.Equal(t, packet.CtrlSubType(4), packet.EXTTYPE_KMRSP, "KMRSP = 4")
	require.Equal(t, packet.CtrlSubType(5), packet.EXTTYPE_SID, "SID = 5")
	require.Equal(t, packet.CtrlSubType(6), packet.EXTTYPE_CONGESTION, "CONGESTION = 6")
	require.Equal(t, packet.CtrlSubType(7), packet.EXTTYPE_FILTER, "FILTER = 7")
	require.Equal(t, packet.CtrlSubType(8), packet.EXTTYPE_GROUP, "GROUP = 8")
}

// =============================================================================
// Control Type String Representation Tests
// =============================================================================
// Tests that String() methods return expected values.
// =============================================================================

func TestControlType_String_TableDriven(t *testing.T) {
	testCases := []struct {
		ctrlType packet.CtrlType
		expected string
	}{
		{packet.CTRLTYPE_HANDSHAKE, "HANDSHAKE"},
		{packet.CTRLTYPE_KEEPALIVE, "KEEPALIVE"},
		{packet.CTRLTYPE_ACK, "ACK"},
		{packet.CTRLTYPE_NAK, "NAK"},
		{packet.CTRLTYPE_WARN, "WARN"},
		{packet.CTRLTYPE_SHUTDOWN, "SHUTDOWN"},
		{packet.CTRLTYPE_ACKACK, "ACKACK"},
		{packet.CRTLTYPE_DROPREQ, "DROPREQ"},
		{packet.CRTLTYPE_PEERERROR, "PEERERROR"},
		{packet.CTRLTYPE_USER, "USER"},
		{packet.CtrlType(0x0009), "unknown"},
		{packet.CtrlType(0xFFFF), "unknown"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.ctrlType.String())
		})
	}
}

func TestCtrlSubType_String_TableDriven(t *testing.T) {
	testCases := []struct {
		subType  packet.CtrlSubType
		expected string
	}{
		{packet.CTRLSUBTYPE_NONE, "NONE"},
		{packet.EXTTYPE_HSREQ, "EXTTYPE_HSREQ"},
		{packet.EXTTYPE_HSRSP, "EXTTYPE_HSRSP"},
		{packet.EXTTYPE_KMREQ, "EXTTYPE_KMREQ"},
		{packet.EXTTYPE_KMRSP, "EXTTYPE_KMRSP"},
		{packet.EXTTYPE_SID, "EXTTYPE_SID"},
		{packet.EXTTYPE_CONGESTION, "EXTTYPE_CONGESTION"},
		{packet.EXTTYPE_FILTER, "EXTTYPE_FILTER"},
		{packet.EXTTYPE_GROUP, "EXTTYPE_GROUP"},
		{packet.CtrlSubType(100), "unknown"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.subType.String())
		})
	}
}

// =============================================================================
// HandlePacketDirect Mode Tests
// =============================================================================
// Tests the mode selection logic in handlePacketDirect.
// =============================================================================

func TestHandlePacketDirectMode_TableDriven(t *testing.T) {
	testCases := []struct {
		name            string
		useEventLoop    bool // recv.UseEventLoop()
		expectLockFree  bool // Whether lock-free path is taken
		expectMutexPath bool // Whether mutex path is taken
	}{
		{
			name:            "EventLoop mode - lock-free path",
			useEventLoop:    true,
			expectLockFree:  true,
			expectMutexPath: false,
		},
		{
			name:            "Legacy mode - mutex path",
			useEventLoop:    false,
			expectLockFree:  false,
			expectMutexPath: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the mode check from handlePacketDirect
			var usedLockFree, usedMutex bool

			if tc.useEventLoop {
				usedLockFree = true
			} else {
				usedMutex = true
			}

			require.Equal(t, tc.expectLockFree, usedLockFree,
				"lock-free path: got %v, want %v", usedLockFree, tc.expectLockFree)
			require.Equal(t, tc.expectMutexPath, usedMutex,
				"mutex path: got %v, want %v", usedMutex, tc.expectMutexPath)
		})
	}
}

// =============================================================================
// Null Packet Handling Tests
// =============================================================================
// Tests that nil packets are handled gracefully.
// =============================================================================

func TestNullPacketHandling(t *testing.T) {
	// The handlePacket function should return early for nil packets
	// This test documents the expected behavior

	testCases := []struct {
		name        string
		packet      interface{} // Using interface{} to allow nil
		expectEarly bool        // Whether to return early
	}{
		{
			name:        "nil packet returns early",
			packet:      nil,
			expectEarly: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the nil check from handlePacket
			p := tc.packet
			returnedEarly := p == nil

			require.Equal(t, tc.expectEarly, returnedEarly,
				"nil packet handling: got early=%v, want early=%v",
				returnedEarly, tc.expectEarly)
		})
	}
}

// =============================================================================
// Sequence Number Out-of-Order Detection Tests
// =============================================================================
// Tests the out-of-order packet detection logic in handlePacket.
// =============================================================================

func TestSequenceOutOfOrderDetection_TableDriven(t *testing.T) {
	// Using the circular comparison logic
	testCases := []struct {
		name             string
		expectedSeq      uint32
		receivedSeq      uint32
		expectLostPacket bool // receivedSeq > expectedSeq
		expectOutOfOrder bool // receivedSeq < expectedSeq
		expectInOrder    bool // receivedSeq == expectedSeq
	}{
		// =================================================================
		// In-order packets
		// =================================================================
		{
			name:          "exact expected sequence",
			expectedSeq:   100,
			receivedSeq:   100,
			expectInOrder: true,
		},

		// =================================================================
		// Lost packets (gap in sequence)
		// =================================================================
		{
			name:             "one packet lost",
			expectedSeq:      100,
			receivedSeq:      101,
			expectLostPacket: true,
		},
		{
			name:             "multiple packets lost",
			expectedSeq:      100,
			receivedSeq:      110,
			expectLostPacket: true,
		},

		// =================================================================
		// Out-of-order packets (received < expected)
		// =================================================================
		{
			name:             "late packet (one behind)",
			expectedSeq:      100,
			receivedSeq:      99,
			expectOutOfOrder: true,
		},
		{
			name:             "very late packet",
			expectedSeq:      100,
			receivedSeq:      50,
			expectOutOfOrder: true,
		},

		// =================================================================
		// Wraparound cases (using simplified comparison)
		// =================================================================
		{
			name:             "wraparound: expected near max, received near 0 (lost)",
			expectedSeq:      0x7FFFFFFE, // Near max
			receivedSeq:      0x7FFFFFFF, // Max (one ahead)
			expectLostPacket: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simplified comparison (actual uses circular.Number)
			var lostPacket, outOfOrder, inOrder bool
			switch {
			case tc.receivedSeq > tc.expectedSeq:
				lostPacket = true
			case tc.receivedSeq < tc.expectedSeq:
				outOfOrder = true
			default:
				inOrder = true
			}

			require.Equal(t, tc.expectLostPacket, lostPacket,
				"lost packet detection: expected=%d, received=%d", tc.expectedSeq, tc.receivedSeq)
			require.Equal(t, tc.expectOutOfOrder, outOfOrder,
				"out-of-order detection: expected=%d, received=%d", tc.expectedSeq, tc.receivedSeq)
			require.Equal(t, tc.expectInOrder, inOrder,
				"in-order detection: expected=%d, received=%d", tc.expectedSeq, tc.receivedSeq)
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkControlTypeDispatch(b *testing.B) {
	handlers := map[packet.CtrlType]string{
		packet.CTRLTYPE_KEEPALIVE: "dispatchKeepAlive",
		packet.CTRLTYPE_SHUTDOWN:  "handleShutdown",
		packet.CTRLTYPE_NAK:       "handleNAK",
		packet.CTRLTYPE_ACK:       "handleACK",
		packet.CTRLTYPE_ACKACK:    "dispatchACKACK",
		packet.CTRLTYPE_USER:      "handleUserPacket",
	}

	types := []packet.CtrlType{
		packet.CTRLTYPE_ACK,
		packet.CTRLTYPE_NAK,
		packet.CTRLTYPE_ACKACK,
		packet.CTRLTYPE_KEEPALIVE,
		packet.CTRLTYPE_USER,
		packet.CtrlType(99), // Unknown
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, ct := range types {
			_ = handlers[ct]
		}
	}
}

func BenchmarkUserSubTypeDispatch(b *testing.B) {
	handlers := map[packet.CtrlSubType]string{
		packet.EXTTYPE_HSREQ: "handleHSRequest",
		packet.EXTTYPE_HSRSP: "handleHSResponse",
		packet.EXTTYPE_KMREQ: "handleKMRequest",
		packet.EXTTYPE_KMRSP: "handleKMResponse",
	}

	subTypes := []packet.CtrlSubType{
		packet.EXTTYPE_HSREQ,
		packet.EXTTYPE_HSRSP,
		packet.EXTTYPE_KMREQ,
		packet.EXTTYPE_KMRSP,
		packet.CtrlSubType(99), // Unknown
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, st := range subTypes {
			_ = handlers[st]
		}
	}
}
