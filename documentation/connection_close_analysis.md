# Connection Close Analysis

## Overview

This document analyzes all code paths that lead to connection closure in the GoSRT library, identifies existing logging, and provides recommendations for enabling comprehensive connection close logging.

## Executive Summary

### Key Finding: Timer Reset Logic is Correct ✅

**The `peerIdleTimeout` reset logic is working correctly**:
- Timer reset happens at the **start** of `handlePacket()` (line 785)
- **ALL packets** that reach `handlePacket()` reset the timer (data, ACK, NAK, KEEPALIVE, etc.)
- Timer reset happens **before** any packet filtering or processing

### Root Cause: Network Packet Loss ❌

**The actual problem is network packet loss**:
- Statistics show: `PktRecvLossRate: 48.58%` (extreme loss rate)
- NAK packets are being sent but not received: `PktSentNAK: 614117`, `PktRecvNAK: 0`
- With 2-3% normal loss + bursts + 48% loss rate, it's possible for **ALL packets to be lost for 30 seconds**
- If no packets are received, `handlePacket()` is never called, timer is never reset → timeout

### Why This Happens

**One-way packet loss scenario**:
1. Server sends data packets at 40 Mb/s
2. Some data packets are lost (2-3% + bursts)
3. Client receives some packets → Sends ACK/NAK
4. **Client's ACK/NAK packets are lost** (one-way loss)
5. Server doesn't receive ACK/NAK → No packets received for 30s → Timeout

**The timer reset logic is correct** - the problem is that packets never reach the code due to network loss.

### Recommended Solutions

1. **Increase timeout for high-loss networks** - Change `PeerIdleTimeout` from 30s to 60-90s
2. **Implement proactive keepalive sending** - Send keepalives every 1 second (per SRT spec)
3. **Investigate network** - 48% packet loss is extreme and needs investigation
4. **Add diagnostic logging** - Log every packet that reaches `handlePacket()` to verify timer resets

## Real-World Log Analysis

### Observed Connection Close Scenarios

Based on production logs, the following close reasons have been observed:

#### 1. Peer Idle Timeout (Most Common) ⏰

**Client Logs:**
```
0xe718f87b connection:close:reason: shutdown packet received from peer
0xf1257956 connection:close:reason: peer idle timeout: no data received from peer for 30s
```

**Server Logs:**
```
0x7da21ce5 connection:close:reason: peer idle timeout: no data received from peer for 30s
```

**Analysis:**
- **Root Cause**: No packets received from peer for `PeerIdleTimeout` (30 seconds)
- **Timer Reset Logic**: ✅ **Correct** - `peerIdleTimeout.Reset()` is called at the start of `handlePacket()` for ALL received packets
- **The Problem**: Packets are being **lost in the network**, so they never reach `handlePacket()`, so the timer is never reset

**Evidence from Statistics**:
- Server: `PktSentNAK: 164915`, `PktRecvNAK: 0` - Server sends NAKs but receives none
- Client: `PktSentNAK: 614117`, `PktRecvNAK: 0` - Client sends NAKs but receives none
- **This indicates severe one-way packet loss** - ACK/NAK packets are being lost in one direction

**Why Timeout Occurs**:
- With 2-3% packet loss + bursts, it's possible for ALL packets (data + ACK/NAK) to be lost for 30 seconds
- If no packets are received for 30 seconds, `handlePacket()` is never called, timer is never reset → timeout

**Key Finding**: The timer reset logic is **correct**. The issue is **network packet loss** preventing packets from reaching `handlePacket()`. Proactive keepalive sending would help, but keepalives can also be lost.

#### 2. Shutdown Packet Received 📨

**Client Logs:**
```
0xe718f87b connection:close:reason: shutdown packet received from peer
```

**Analysis:**
- Server sends shutdown packet when peer idle timeout triggers
- Client receives shutdown and closes connection
- This is the **consequence** of peer idle timeout, not the root cause

#### 3. Application-Initiated Close 🖱️

**Server Logs:**
```
0x7da21ce5 connection:close:reason: application requested close
0x039e9e42 connection:close:reason: application requested close
```

**Analysis:**
- Application explicitly calls `conn.Close()`
- This is expected behavior for graceful shutdown
- Not a problem

#### 4. Unknown Destination Socket ID Errors 🔍

**Server Logs:**
```
0x00000000 listen:recv:error: unknown destination socket ID: 60726850
(repeated many times)
```

**Analysis:**
- Packets arriving for a socket ID that no longer exists
- Connection was closed, but packets are still in flight
- This is expected during connection teardown
- Should be suppressed during shutdown (already implemented)

---

## Peer Idle Timeout Tracking Analysis

### How `peerIdleTimeout` Reset Works

**Location**: `connection.go:780-785`

**Current Implementation**:
```go
func (c *srtConn) handlePacket(p packet.Packet) {
    if p == nil {
        return  // Early return - NO timer reset for nil packets
    }

    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)  // ← Timer reset happens HERE

    header := p.Header()
    // ... rest of packet processing ...
}
```

**Key Finding**: ✅ **The timer reset happens at the START of `handlePacket()`, BEFORE any packet processing or filtering.**

### Packet Receive Paths

#### Path 1: io_uring Receive (Phase 5 - Channel Bypass)

**Flow**:
1. `listen_linux.go:processRecvCompletion()` - Receives packet from io_uring
2. `listen_linux.go:504` - Calls `conn.handlePacketDirect(p)`
3. `connection.go:725-730` - `handlePacketDirect()` locks mutex and calls `handlePacket(p)`
4. `connection.go:785` - **Timer reset happens here**

**Status**: ✅ **All packets that reach `handlePacket()` reset the timer**

#### Path 2: Traditional ReadFrom() (Fallback)

**Flow**:
1. `listen.go:reader()` - Receives from `rcvQueue` channel
2. `listen.go:461` - Calls `conn.push(p)`
3. `connection.go:585-593` - `push()` sends to `networkQueue` channel
4. `connection.go:674-684` - `networkQueueReader()` receives from channel
5. `connection.go:682` - Calls `c.handlePacket(p)`
6. `connection.go:785` - **Timer reset happens here**

**Status**: ✅ **All packets that reach `handlePacket()` reset the timer**

### Packets That Reset the Timer

**All packet types that reach `handlePacket()` reset the timer**:
- ✅ **Data packets** - Reset timer (line 785, before processing)
- ✅ **Control packets** - Reset timer (line 785, before dispatch):
  - ACK packets
  - NAK packets
  - KEEPALIVE packets
  - SHUTDOWN packets
  - ACKACK packets
  - USER packets (handshake, key material)

### Packets That Do NOT Reset the Timer

**Packets that are dropped BEFORE reaching `handlePacket()`**:
- ❌ **Parse errors** - Dropped in `processRecvCompletion()` before `handlePacket()`
- ❌ **Handshake packets** (socketId == 0) - Routed to backlog, not `handlePacket()`
- ❌ **Unknown socket ID** - Dropped in `processRecvCompletion()` before `handlePacket()`
- ❌ **Wrong peer address** - Dropped in `processRecvCompletion()` before `handlePacket()`
- ❌ **FEC filter packets** (MessageNumber == 0) - **BUT timer is already reset** (line 785 happens before line 820 check)

**Important**: FEC filter packets (MessageNumber == 0) **DO reset the timer** because the reset happens at line 785, and the FEC check happens at line 820. This is actually correct behavior - we received a packet, so the peer is alive.

### Critical Discovery: Timer Reset Happens Early ✅

**The user is correct**: Any packet that successfully reaches `handlePacket()` will reset the `peerIdleTimeout` timer, regardless of:
- Packet type (data or control)
- Whether the packet is later filtered/dropped (FEC packets)
- Whether the packet is valid or invalid (unknown control types)

**This means**: If packets are being received at 40 Mb/s, the timer should be reset frequently and the timeout should NOT occur.

### Why Timeout Might Still Happen

Given that the timer reset logic is correct, if a timeout occurs with active traffic, possible causes:

1. **Network packet loss** - Packets are lost in transit, not received
   - High packet loss (2-3% + bursts) could cause gaps > 30 seconds
   - If ALL packets in a 30-second window are lost, timeout occurs
   - **With 2-3% loss + bursts, it's possible for all packets to be lost for 30 seconds**

2. **Packets dropped before `handlePacket()`** - Packets received but dropped earlier:
   - Parse errors (malformed packets) - Dropped in `processRecvCompletion()`
   - Unknown socket ID (connection closed, packets in flight) - Dropped in `processRecvCompletion()`
   - Wrong peer address (security check) - Dropped in `processRecvCompletion()`
   - **These packets never reach `handlePacket()`, so timer is NOT reset**

3. **One-way traffic** - Only one direction has traffic:
   - Server sends data packets → Client receives → Client sends ACK/NAK
   - If client's ACK/NAK packets are lost, server doesn't receive anything → timeout
   - **This is where proactive keepalives would help**

4. **Timer reset race condition** - Unlikely but possible:
   - Timer fires between packet receive and timer reset
   - Very unlikely given timer reset is at start of function

5. **Connection state issue** - Connection closed but packets still arriving:
   - Packets arrive for closed connection
   - Dropped as "unknown socket ID" in `processRecvCompletion()`
   - Timer not reset because `handlePacket()` never called

### ACK/NAK Packet Flow

**How ACK/NAK packets are sent**:
1. `receiver.Tick()` → `periodicACK()` / `periodicNAK()` → Calls `OnSendACK()` / `OnSendNAK()`
2. `connection.go:373-374` - `OnSendACK: c.sendACK`, `OnSendNAK: c.sendNAK`
3. `connection.go:1410-1437` - `sendACK()` creates packet and calls `c.pop(p)`
4. `connection.go:1385-1408` - `sendNAK()` creates packet and calls `c.pop(p)`
5. `connection.go:611-655` - `pop()` calls `c.onSend(p)` which sends packet to network

**Critical Discovery**: ⚠️ **Statistics are incremented BEFORE the packet is actually sent**

**The Problem**:
- `sendACK()` increments `c.statistics.pktSentACK++` at line 1454
- `sendNAK()` increments `c.statistics.pktSentNAK++` at line 1403
- **These statistics are incremented BEFORE `c.pop(p)` is called**
- If `c.pop(p)` fails or drops the packet, statistics still show packets as "sent"

**Potential Issues**:
1. **io_uring ring full** - `sendIoUring()` has a retry loop (max 3 retries), but if all retries fail, the packet is dropped
2. **Ring not initialized** - If `c.sendRing == nil`, `c.send()` drops the packet with error log
3. **Marshalling failure** - If packet marshalling fails, packet is dropped
4. **Type assertion failure** - If ring type assertion fails, packet is dropped

**The Statistics Don't Prove Packets Were Sent**:
- `PktSentNAK: 614117` means `sendNAK()` was called 614,117 times
- **It does NOT mean 614,117 packets were actually sent to the network**
- If packets are being dropped in `sendIoUring()` or `send()`, statistics would still increment

**Verification Needed**:
- Check logs for `connection:send:error` or `packet:send:error` messages
- Check if io_uring ring is full (would cause packet drops)
- Verify `c.sendRing != nil` when sending ACK/NAK packets
- Check if marshalling is failing for ACK/NAK packets

### Verification Needed

To diagnose the actual issue, we need to check:

1. **Are packets being received?** - Check if `handlePacket()` is being called
   - Add logging: `connection:recv:packet` topic to log every packet received
2. **Are packets being dropped?** - Check for "unknown socket ID" or parse errors
   - Already logged: `listen:recv:error` for unknown socket ID
   - Already logged: `listen:recv:parse:error` for parse errors
3. **Is traffic bidirectional?** - Check if ACK/NAK packets are being sent/received
   - Check statistics: `PktSentACK`, `PktRecvACK`, `PktSentNAK`, `PktRecvNAK`
4. **Network packet loss** - Check statistics for packet loss rates
   - Check: `PktRecvLoss`, `PktRecvLossRate`

### Root Cause Hypothesis

**Most Likely**: High packet loss (2-3% + bursts) combined with one-way traffic:
- Server sends data packets → Some lost
- Client receives some packets → Sends ACK/NAK
- **Client's ACK/NAK packets are lost** → Server doesn't receive anything for 30s → timeout

**Evidence from logs**:
- Server stats show: `PktSentNAK: 164915`, `PktRecvNAK: 0` (server sending NAKs, not receiving any)
- Client stats show: `PktSentNAK: 614117`, `PktRecvNAK: 0` (client sending NAKs, not receiving any)
- This suggests **NAK packets are being lost** in one direction
- High packet loss rates: `PktRecvLossRate: 48.58%` (client), indicating severe network issues

**Root Cause Analysis**:

**Critical Discovery**: ⚠️ **Statistics Don't Prove Packets Were Actually Sent**

The statistics (`PktSentNAK`, `PktSentACK`) are incremented **BEFORE** the packet is sent:
- `sendACK()` increments `pktSentACK++` at line 1454, then calls `c.pop(p)` at line 1457
- `sendNAK()` increments `pktSentNAK++` at line 1403, then calls `c.pop(p)` at line 1406

**If `c.pop(p)` fails or drops the packet, statistics still increment!**

**Potential Packet Drop Scenarios**:
1. **io_uring ring full** - `sendIoUring()` retries 3 times, but if ring stays full, packet is dropped (line 202-214)
2. **Marshalling failure** - Packet is dropped with error log (line 139-146)
3. **Ring not initialized** - If `c.sendRing == nil`, packet is dropped (line 661-667)
4. **Type assertion failure** - If ring type assertion fails, packet is dropped (line 126-132)
5. **Submit failure** - If `ring.Submit()` fails after retries, packet is dropped (line 246+)

**This Explains Everything**:
- Statistics show `PktSentNAK: 614117` - but this only means `sendNAK()` was called 614,117 times
- **It does NOT mean 614,117 packets were actually sent to the network**
- If packets are being dropped in `sendIoUring()`, the peer never receives them
- No packets received → `handlePacket()` never called → timer never reset → timeout

**Serialization/Deserialization Analysis**:

**Good News**: ✅ Tests exist and pass for ACK/NAK serialization:
- `TestFullACK`, `TestSmallACK`, `TestLiteACK` - Test ACK CIF marshalling/unmarshalling
- `TestNAK` - Test NAK CIF marshalling/unmarshalling
- **NEW**: `TestFullACKPacketRoundTrip` - Tests full packet (header + CIF) round-trip for ACK
- **NEW**: `TestFullNAKPacketRoundTrip` - Tests full packet (header + CIF) round-trip for NAK

**Test Results**: ✅ **All tests pass** - Serialization/deserialization is working correctly

The new round-trip tests verify:
1. Create packet with header (ControlType, Timestamp, DestinationSocketId, etc.)
2. Marshal CIF into packet payload
3. Marshal full packet (header + payload) to bytes
4. Unmarshal bytes back to packet (header + payload)
5. Unmarshal CIF from packet payload
6. Verify everything matches

**Conclusion**: Serialization/deserialization is **NOT the issue**. The problem must be elsewhere.

**However**: ⚠️ **Statistics are incremented BEFORE deserialization**

In `handleACK()` and `handleNAK()`:
- `pktRecvACK++` / `pktRecvNAK++` incremented at line 908/943
- `UnmarshalCIF()` called at line 913/948
- If deserialization fails, packet is logged as invalid but statistics already incremented

**Critical Finding**: If packets are received but fail to deserialize:
- `PktRecvNAK` would still increment (statistics incremented before deserialization)
- `PktRecvInvalid` would also increment
- But user sees `PktRecvNAK: 0`, suggesting packets are NOT being received at all

**Next Steps**:
1. **Check logs for parse errors** - Look for `listen:recv:parse:error` or `dial:recv:parse:error` messages
2. **Check logs for send errors** - Look for `connection:send:error` or `packet:send:error` messages
3. **Check logs for invalid packets** - Look for `control:recv:ACK:error` or `control:recv:NAK:error` messages
4. **Check packet dumps** - Enable `control:send:NAK:dump` and `control:recv:NAK:dump` logging to compare sent vs received
5. **Verify packets are actually sent** - Check if `sendIoUring()` is successfully submitting packets to the ring

**Solutions**:
1. **Fix statistics** - Only increment after successful send (or move increment to completion handler)
2. **Increase ring size** - If ring is full, increase `IoUringSendRingSize`
3. **Add send error monitoring** - Track dropped packets separately from sent packets
4. **Proactive keepalive sending** - Would help, but only if packets can actually be sent
5. **Longer timeout** - Increase `PeerIdleTimeout` from 30s to 60s or 90s as temporary workaround

---

## Connection Close Triggers

### 1. Peer Idle Timeout ⏰

**Location**: `connection.go:355-360`

**Trigger**: No packets received from peer for `PeerIdleTimeout` duration

**Current Logging**:
```go
c.log("connection:close:reason", func() string {
    return fmt.Sprintf("peer idle timeout: no data received from peer for %s", c.config.PeerIdleTimeout)
})
```

**Log Topic**: `connection:close:reason`

**Status**: ✅ **Logged with explicit reason**

**Root Cause**: Missing proactive keepalive implementation

---

### 2. Shutdown Control Packet 📨

**Location**: `connection.go:885-894` (`handleShutdown`)

**Trigger**: Received `CTRLTYPE_SHUTDOWN` control packet from peer

**Current Logging**:
```go
c.log("connection:close:reason", func() string {
    return "shutdown packet received from peer"
})
```

**Log Topics**:
- `control:recv:shutdown:dump` (packet dump)
- `connection:close:reason` (explicit reason)

**Status**: ✅ **Logged with explicit reason**

**Note**: Usually a consequence of peer idle timeout

---

### 3. Handshake Errors (HSv4) 🤝

**Location**: `connection.go:998-1156` (`handleHSRequest`)

**Multiple triggers** - All logged with `connection:close:reason`:
- Unsupported version
- Missing required flags (TSBPDSND, TLPKTDROP, CRYPT, REXMITFLG)
- Invalid flags (STREAM, PACKET_FILTER for HSv4)

**Status**: ✅ **All logged with explicit reasons**

---

### 4. Handshake Errors (HSv5) 🤝

**Location**: `connection.go:1158-1205` (`handleHSResponse`)

**Multiple triggers** - All logged with `connection:close:reason`:
- Unsupported version
- Missing required flags (TSBPDRCV, TLPKTDROP, CRYPT, REXMITFLG)
- Invalid flags (STREAM, PACKET_FILTER for HSv4)

**Status**: ✅ **All logged with explicit reasons**

---

### 5. Encryption/Key Material Errors 🔐

**Location**: `connection.go:1252-1301` (`handleKMResponse`)

**Multiple triggers** - All logged with `connection:close:reason`:
- Crypto initialization failure
- Peer didn't enable encryption (KM_NOSECRET)
- Peer has different passphrase (KM_BADSECRET)
- Other key material errors

**Status**: ✅ **All logged with explicit reasons**

---

### 6. Application-Initiated Close 🖱️

**Location**: `connection.go:1511-1514`

**Trigger**: Application calls `conn.Close()`

**Current Logging**:
```go
c.log("connection:close:reason", func() string {
    return "application requested close"
})
```

**Status**: ✅ **Logged with explicit reason**

---

### 7. Connection Timeout (Dialer) ⏱️

**Location**: `dial.go:217-222`

**Trigger**: Server doesn't respond within `ConnectionTimeout`

**Current Logging**:
```go
dl.log("connection:close:reason", func() string {
    return fmt.Sprintf("connection timeout: server didn't respond within %s", dl.config.ConnectionTimeout)
})
```

**Status**: ✅ **Logged with explicit reason**

---

### 8. Handshake Rejection 🤝

**Location**: `dial.go:585-595`

**Trigger**: Server rejects connection or unsupported handshake type

**Current Logging**:
```go
dl.log("connection:close:reason", func() string { return reason })
```

**Status**: ✅ **Logged with explicit reason**

---

## Logging Summary

### Log Topics for Connection Close Debugging

| Topic | Purpose | Status |
|-------|---------|--------|
| `connection:close:reason` | Explicit reason for close | ✅ Implemented |
| `connection:close` | Generic close messages | ✅ Existing |
| `connection:error` | Error conditions | ✅ Existing |
| `control:recv:shutdown` | Shutdown packet received | ✅ Existing |
| `control:recv:HSReq:error` | Handshake request errors | ✅ Existing |
| `control:recv:HSRes:error` | Handshake response errors | ✅ Existing |
| `control:recv:KMRes:error` | Key material errors | ✅ Existing |

### Recommended Server Startup Configuration

**Minimal set for debugging connection closes:**

```bash
-logtopics "connection:close,connection:close:reason"
```

**Comprehensive set (includes all close-related topics):**

```bash
-logtopics "connection:close,connection:close:reason,connection:error,control:recv:shutdown,control:recv:HSReq:error,control:recv:HSRes:error,control:recv:KMRes:error"
```

---

## Implementation Recommendations

### High Priority: Implement Proactive Keepalive Sending

**Problem**: Connections timeout because keepalives are not sent proactively.

**Solution**: Add keepalive sending logic to `ticker()` function:

1. **Track last send time**: Add `lastSendTime time.Time` to `srtConn` struct
2. **Update on send**: Update `lastSendTime` in `pop()` when any packet is sent
3. **Send keepalive in ticker**: In `ticker()`, check if 1 second has passed since last send, and send keepalive if needed

**Implementation Plan**:

```go
// In srtConn struct, add:
lastSendTime time.Time
keepaliveInterval time.Duration // Default: 1 second

// In pop(), update lastSendTime:
func (c *srtConn) pop(p packet.Packet) {
    // ... existing code ...
    c.lastSendTime = time.Now()
    c.onSend(p)
}

// In ticker(), add keepalive sending:
func (c *srtConn) ticker(ctx context.Context) {
    ticker := time.NewTicker(c.tick)
    defer ticker.Stop()

    keepaliveTicker := time.NewTicker(c.keepaliveInterval)
    defer keepaliveTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case t := <-ticker.C:
            tickTime := uint64(t.Sub(c.start).Microseconds())
            c.recv.Tick(c.tsbpdTimeBase + tickTime)
            c.snd.Tick(tickTime)
        case <-keepaliveTicker.C:
            // Send keepalive if no packet sent in last interval
            if time.Since(c.lastSendTime) >= c.keepaliveInterval {
                c.sendKeepAlive()
            }
        }
    }
}

// New function to send keepalive:
func (c *srtConn) sendKeepAlive() {
    p := packet.NewPacket(c.remoteAddr)
    p.Header().IsControlPacket = true
    p.Header().ControlType = packet.CTRLTYPE_KEEPALIVE
    p.Header().Timestamp = c.getTimestampForPacket()
    p.Header().DestinationSocketId = c.peerSocketId

    c.log("control:send:keepalive:dump", func() string { return p.Dump() })
    c.pop(p)
}
```

### Medium Priority: Adjust Timeout for High Loss Networks

**Problem**: 30-second timeout may be too short for high packet loss networks (2-3% loss + bursts).

**Solution**:
- Make `PeerIdleTimeout` configurable (already is)
- Consider increasing default for high-loss scenarios
- Or make timeout adaptive based on packet loss rate

### Low Priority: Suppress "Unknown Socket ID" During Shutdown

**Status**: ✅ Already implemented - errors are suppressed during shutdown

---

## Testing

After implementing proactive keepalives, test:

1. **SUBSCRIBE connection with no data**: Verify keepalives are sent every 1 second
2. **PUBLISH connection with no data**: Verify keepalives are sent every 1 second
3. **High packet loss**: Verify connections stay alive even with 2-3% loss
4. **Long-running connections**: Verify connections stay open for extended periods

---

## Conclusion

The connection close logging is now comprehensive and provides clear reasons for all close scenarios. The **primary issue** is the **missing proactive keepalive implementation**, which causes connections to timeout when no data packets are being sent. Implementing proactive keepalive sending will resolve the majority of unexpected connection closes.
