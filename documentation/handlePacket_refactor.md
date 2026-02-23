# handlePacket Refactoring: Map-Based Dispatch

## Overview

This document describes the refactoring of the `handlePacket` function in `connection.go` to replace the large if-else chain with a map-based function dispatch table. This refactoring improves code maintainability, performance, and readability.

## Current Implementation

**Location**: `connection.go:700-809`

**Current Structure:**
```go
func (c *srtConn) handlePacket(p packet.Packet) {
    if p == nil {
        return
    }

    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)

    header := p.Header()

    if header.IsControlPacket {
        if header.ControlType == packet.CTRLTYPE_KEEPALIVE {
            c.handleKeepAlive(p)
        } else if header.ControlType == packet.CTRLTYPE_SHUTDOWN {
            c.handleShutdown(p)
        } else if header.ControlType == packet.CTRLTYPE_NAK {
            c.handleNAK(p)
        } else if header.ControlType == packet.CTRLTYPE_ACK {
            c.handleACK(p)
        } else if header.ControlType == packet.CTRLTYPE_ACKACK {
            c.handleACKACK(p)
        } else if header.ControlType == packet.CTRLTYPE_USER {
            c.log("connection:recv:ctrl:user", func() string {
                return fmt.Sprintf("got CTRLTYPE_USER packet, subType: %s", header.SubType)
            })

            // HSv4 Extension
            if header.SubType == packet.EXTTYPE_HSREQ {
                c.handleHSRequest(p)
            } else if header.SubType == packet.EXTTYPE_HSRSP {
                c.handleHSResponse(p)
            }

            // 3.2.2.  Key Material
            if header.SubType == packet.EXTTYPE_KMREQ {
                c.handleKMRequest(p)
            } else if header.SubType == packet.EXTTYPE_KMRSP {
                c.handleKMResponse(p)
            }
        }

        return
    }

    // ... data packet handling ...
}
```

**Issues with Current Implementation:**
1. **Large if-else chain**: Hard to maintain and extend
2. **O(n) lookup**: Linear search through conditions
3. **Nested conditionals**: CTRLTYPE_USER has its own if-else chain
4. **Code duplication**: Similar pattern for each control type
5. **Hard to test**: Difficult to mock or test individual handlers

## Proposed Refactoring

### Design: Map-Based Dispatch Table

Replace the if-else chain with a map that maps `ControlType` to handler functions. The map is initialized once during connection setup and never modified, so it requires no locking.

**Key Design Decisions:**
1. **Immutable map**: Initialized once, never modified → no locking needed
2. **Function type**: `func(*srtConn, packet.Packet)` for consistency
3. **Nested dispatch**: Separate map for CTRLTYPE_USER SubType handling
4. **Error handling**: Unknown types log and return gracefully

### Implementation

**1. Define Handler Function Type**

```go
// controlPacketHandler is the function signature for control packet handlers
type controlPacketHandler func(c *srtConn, p packet.Packet)

// userPacketHandler is the function signature for CTRLTYPE_USER SubType handlers
type userPacketHandler func(c *srtConn, p packet.Packet)
```

**2. Initialize Dispatch Maps in srtConn**

```go
// In srtConn struct (connection.go)
type srtConn struct {
    // ... existing fields ...

    // Control packet dispatch table (initialized once, never modified)
    controlHandlers map[packet.CtrlType]controlPacketHandler

    // CTRLTYPE_USER SubType dispatch table (initialized once, never modified)
    userHandlers map[packet.CtrlSubType]userPacketHandler
}

// initializeControlHandlers initializes the control packet dispatch tables
// Called once during connection initialization
func (c *srtConn) initializeControlHandlers() {
    // Main control type handlers
    c.controlHandlers = map[packet.CtrlType]controlPacketHandler{
        packet.CTRLTYPE_KEEPALIVE: (*srtConn).handleKeepAlive,
        packet.CTRLTYPE_SHUTDOWN:  (*srtConn).handleShutdown,
        packet.CTRLTYPE_NAK:       (*srtConn).handleNAK,
        packet.CTRLTYPE_ACK:       (*srtConn).handleACK,
        packet.CTRLTYPE_ACKACK:    (*srtConn).handleACKACK,
        packet.CTRLTYPE_USER:      (*srtConn).handleUserPacket, // Special handler
    }

    // CTRLTYPE_USER SubType handlers
    c.userHandlers = map[packet.CtrlSubType]userPacketHandler{
        packet.EXTTYPE_HSREQ: (*srtConn).handleHSRequest,
        packet.EXTTYPE_HSRSP: (*srtConn).handleHSResponse,
        packet.EXTTYPE_KMREQ: (*srtConn).handleKMRequest,
        packet.EXTTYPE_KMRSP: (*srtConn).handleKMResponse,
    }
}
```

**3. Refactored handlePacket Function**

```go
func (c *srtConn) handlePacket(p packet.Packet) {
    if p == nil {
        return
    }

    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)

    header := p.Header()

    if header.IsControlPacket {
        // Lookup handler in dispatch table
        handler, ok := c.controlHandlers[header.ControlType]
        if !ok {
            // Unknown control type - log and return
            c.log("connection:recv:ctrl:unknown", func() string {
                return fmt.Sprintf("unknown control packet type: %s", header.ControlType)
            })
            return
        }

        // Call handler
        handler(c, p)
        return
    }

    // ... data packet handling (unchanged) ...
}
```

**4. Special Handler for CTRLTYPE_USER**

```go
// handleUserPacket dispatches CTRLTYPE_USER packets based on SubType
func (c *srtConn) handleUserPacket(p packet.Packet) {
    header := p.Header()

    c.log("connection:recv:ctrl:user", func() string {
        return fmt.Sprintf("got CTRLTYPE_USER packet, subType: %s", header.SubType)
    })

    // Lookup SubType handler
    handler, ok := c.userHandlers[header.SubType]
    if !ok {
        // Unknown SubType - log and return
        c.log("connection:recv:ctrl:user:unknown", func() string {
            return fmt.Sprintf("unknown CTRLTYPE_USER SubType: %s", header.SubType)
        })
        return
    }

    // Call SubType handler
    handler(c, p)
}
```

**5. Update Connection Initialization**

```go
// In newSRTConn or similar initialization function
func newSRTConn(...) *srtConn {
    c := &srtConn{
        // ... other initialization ...
    }

    // Initialize dispatch tables
    c.initializeControlHandlers()

    return c
}
```

## Complete Refactored Code

**Full Implementation:**

```go
// In connection.go

// controlPacketHandler is the function signature for control packet handlers
type controlPacketHandler func(c *srtConn, p packet.Packet)

// userPacketHandler is the function signature for CTRLTYPE_USER SubType handlers
type userPacketHandler func(c *srtConn, p packet.Packet)

// In srtConn struct
type srtConn struct {
    // ... existing fields ...

    // Control packet dispatch table (initialized once, never modified, no locking needed)
    controlHandlers map[packet.CtrlType]controlPacketHandler

    // CTRLTYPE_USER SubType dispatch table (initialized once, never modified, no locking needed)
    userHandlers map[packet.CtrlSubType]userPacketHandler
}

// initializeControlHandlers initializes the control packet dispatch tables.
// This is called once during connection initialization and the maps are never modified,
// so no locking is required for map access.
func (c *srtConn) initializeControlHandlers() {
    // Main control type handlers
    c.controlHandlers = map[packet.CtrlType]controlPacketHandler{
        packet.CTRLTYPE_KEEPALIVE: (*srtConn).handleKeepAlive,
        packet.CTRLTYPE_SHUTDOWN:  (*srtConn).handleShutdown,
        packet.CTRLTYPE_NAK:       (*srtConn).handleNAK,
        packet.CTRLTYPE_ACK:       (*srtConn).handleACK,
        packet.CTRLTYPE_ACKACK:    (*srtConn).handleACKACK,
        packet.CTRLTYPE_USER:      (*srtConn).handleUserPacket, // Special handler for SubType dispatch
    }

    // CTRLTYPE_USER SubType handlers
    c.userHandlers = map[packet.CtrlSubType]userPacketHandler{
        packet.EXTTYPE_HSREQ: (*srtConn).handleHSRequest,
        packet.EXTTYPE_HSRSP: (*srtConn).handleHSResponse,
        packet.EXTTYPE_KMREQ: (*srtConn).handleKMRequest,
        packet.EXTTYPE_KMRSP: (*srtConn).handleKMResponse,
    }
}

// handleUserPacket dispatches CTRLTYPE_USER packets based on SubType
func (c *srtConn) handleUserPacket(p packet.Packet) {
    header := p.Header()

    c.log("connection:recv:ctrl:user", func() string {
        return fmt.Sprintf("got CTRLTYPE_USER packet, subType: %s", header.SubType)
    })

    // Lookup SubType handler
    handler, ok := c.userHandlers[header.SubType]
    if !ok {
        // Unknown SubType - log and return gracefully
        c.log("connection:recv:ctrl:user:unknown", func() string {
            return fmt.Sprintf("unknown CTRLTYPE_USER SubType: %s", header.SubType)
        })
        return
    }

    // Call SubType handler
    handler(c, p)
}

// handlePacket receives and processes a packet. For control packets, it uses
// a dispatch table for O(1) lookup. The packet will be decrypted if required.
func (c *srtConn) handlePacket(p packet.Packet) {
    if p == nil {
        return
    }

    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)

    header := p.Header()

    if header.IsControlPacket {
        // O(1) lookup in dispatch table (no locking needed - map is immutable)
        handler, ok := c.controlHandlers[header.ControlType]
        if !ok {
            // Unknown control type - log and return gracefully
            c.log("connection:recv:ctrl:unknown", func() string {
                return fmt.Sprintf("unknown control packet type: %s", header.ControlType)
            })
            return
        }

        // Call handler
        handler(c, p)
        return
    }

    // Data packet handling (unchanged)
    if header.PacketSequenceNumber.Gt(c.debug.expectedRcvPacketSequenceNumber) {
        c.log("connection:error", func() string {
            return fmt.Sprintf("recv lost packets. got: %d, expected: %d (%d)\n",
                header.PacketSequenceNumber.Val(),
                c.debug.expectedRcvPacketSequenceNumber.Val(),
                c.debug.expectedRcvPacketSequenceNumber.Distance(header.PacketSequenceNumber))
        })
    }

    c.debug.expectedRcvPacketSequenceNumber = header.PacketSequenceNumber.Inc()

    // Ignore FEC filter control packets
    if header.MessageNumber == 0 {
        c.log("connection:filter", func() string { return "dropped FEC filter control packet" })
        return
    }

    // ... rest of data packet handling (unchanged) ...
    // (TSBPD calculation, decryption, congestion control, etc.)
}
```

## Benefits

### 1. Performance
- **O(1) lookup**: Map lookup is constant time vs O(n) if-else chain
- **Better branch prediction**: Map lookup has more predictable behavior
- **No locking overhead**: Map is immutable after initialization

### 2. Maintainability
- **Easy to extend**: Adding new control types is just adding to the map
- **Clear separation**: Each handler is independent
- **Less code**: Eliminates repetitive if-else structure

### 3. Testability
- **Easy to mock**: Can replace handlers in tests
- **Isolated testing**: Each handler can be tested independently
- **Clear contracts**: Function signatures define the interface

### 4. Readability
- **Self-documenting**: Map shows all supported control types at a glance
- **Less nesting**: Flatter code structure
- **Consistent pattern**: All handlers follow the same pattern

## Error Handling

**Unknown Control Types:**
- Log the unknown type
- Return gracefully (don't crash)
- Allow connection to continue processing other packets

**Unknown SubTypes (CTRLTYPE_USER):**
- Log the unknown SubType
- Return gracefully
- Don't affect other packet processing

**Implementation:**
```go
// In handlePacket
if !ok {
    c.log("connection:recv:ctrl:unknown", func() string {
        return fmt.Sprintf("unknown control packet type: %s", header.ControlType)
    })
    return
}

// In handleUserPacket
if !ok {
    c.log("connection:recv:ctrl:user:unknown", func() string {
        return fmt.Sprintf("unknown CTRLTYPE_USER SubType: %s", header.SubType)
    })
    return
}
```

## Thread Safety

**No Locking Required:**
- Maps are initialized once during connection setup
- Maps are never modified after initialization
- Multiple goroutines can safely read from the maps concurrently
- Go's map read operations are safe for concurrent reads when the map is not being modified

**Initialization:**
- Maps are initialized in `initializeControlHandlers()` during connection creation
- This happens before the connection is used
- No race conditions possible

## Migration Strategy

**Phase 1: Add Dispatch Tables**
1. Add `controlHandlers` and `userHandlers` maps to `srtConn` struct
2. Add `initializeControlHandlers()` method
3. Call `initializeControlHandlers()` in connection initialization
4. Keep existing if-else chain (not used yet)

**Phase 2: Add handleUserPacket**
1. Create `handleUserPacket()` method
2. Move CTRLTYPE_USER logic to `handleUserPacket()`
3. Test CTRLTYPE_USER handling

**Phase 3: Refactor handlePacket**
1. Replace if-else chain with map lookup
2. Test all control packet types
3. Verify error handling for unknown types

**Phase 4: Cleanup**
1. Remove old if-else code
2. Update comments
3. Run full test suite

## Testing

**Unit Tests:**
```go
func TestControlPacketDispatch(t *testing.T) {
    // Test each control type is correctly dispatched
    for ctrlType, expectedHandler := range testCases {
        handler := conn.controlHandlers[ctrlType]
        require.NotNil(t, handler, "handler for %s should exist", ctrlType)
        // Verify handler is correct
    }
}

func TestUnknownControlType(t *testing.T) {
    // Test that unknown control types are handled gracefully
    p := createControlPacket(unknownCtrlType)
    conn.handlePacket(p)
    // Verify log was called, no panic occurred
}

func TestUserPacketDispatch(t *testing.T) {
    // Test each SubType is correctly dispatched
    for subType, expectedHandler := range testCases {
        handler := conn.userHandlers[subType]
        require.NotNil(t, handler, "handler for %s should exist", subType)
    }
}
```

**Integration Tests:**
- Test all existing control packet handling still works
- Test error cases (unknown types)
- Test performance improvement (benchmark)

## Performance Considerations

**Benchmark Comparison:**

```go
func BenchmarkHandlePacketIfElse(b *testing.B) {
    // Benchmark current if-else implementation
}

func BenchmarkHandlePacketMap(b *testing.B) {
    // Benchmark map-based dispatch
}
```

**Expected Improvements:**
- **Lookup time**: O(1) vs O(n) for if-else chain
- **Branch prediction**: Better CPU branch prediction
- **Code size**: Smaller code size (less branching)

## Future Enhancements

**Potential Extensions:**
1. **Dynamic handler registration**: Allow plugins to register handlers (would require locking)
2. **Handler middleware**: Add pre/post processing hooks
3. **Metrics**: Track handler call counts and timing
4. **Handler priorities**: Support priority-based dispatch

## Summary

This refactoring replaces the if-else chain in `handlePacket` with a map-based dispatch table, providing:
- **Better performance**: O(1) lookup vs O(n)
- **Better maintainability**: Easy to extend and modify
- **Better testability**: Isolated, mockable handlers
- **No locking overhead**: Immutable maps after initialization
- **Cleaner code**: Self-documenting, less nesting

The refactoring is backward-compatible and can be done incrementally with minimal risk.

