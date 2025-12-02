# Connection Close Analysis

## Overview

This document analyzes all code paths that lead to connection closure in the GoSRT library, identifies existing logging, and provides recommendations for enabling comprehensive connection close logging.

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
5. **Submit failure** - If `ring.Submit()` fails after retries, packet is dropped

**The Statistics Don't Prove Packets Were Sent**:
- `PktSentNAK: 614117` means `sendNAK()` was called 614,117 times
- **It does NOT mean 614,117 packets were actually sent to the network**
- If packets are being dropped in `sendIoUring()` or `send()`, statistics would still increment

**Verification Needed**:
- Check logs for `connection:send:error` or `packet:send:error` messages
- Check if io_uring ring is full (ring size vs. submission rate)
- Verify `c.sendRing != nil` when sending ACK/NAK packets
- Check if marshalling is failing for ACK/NAK packets

### Serialization/Deserialization Analysis

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

---

## Recommended Log Topics for Debugging

### For ACK/NAK Packet Debugging

**To track ACK/NAK packet sending:**
- `control:send:ACK:dump` - ACK packet dump when sending
- `control:send:ACK:cif` - ACK CIF details when sending
- `control:send:NAK:dump` - NAK packet dump when sending
- `control:send:NAK:cif` - NAK CIF details when sending
- `connection:send:error` - Errors when sending (ring full, submit failure, etc.)

**To track ACK/NAK packet receiving:**
- `control:recv:ACK:dump` - ACK packet dump when receiving
- `control:recv:ACK:cif` - ACK CIF details when receiving
- `control:recv:ACK:error` - ACK deserialization errors
- `control:recv:NAK:dump` - NAK packet dump when receiving
- `control:recv:NAK:cif` - NAK CIF details when receiving
- `control:recv:NAK:error` - NAK deserialization errors

**To track packet parsing errors:**
- `listen:recv:parse:error` - Packet parsing errors (malformed packets)
- `listen:recv:error` - Other receive errors (unknown socket ID, wrong peer, etc.)

**Example log topics string:**
```
-logtopics "listen:io_uring,listen:recv,listen:recv:parse:error,listen:recv:error,handshake:recv,connection:close,connection:close:reason,connection:send:error,control:send:ACK:dump,control:send:ACK:cif,control:send:NAK:dump,control:send:NAK:cif,control:recv:ACK:dump,control:recv:ACK:cif,control:recv:ACK:error,control:recv:NAK:dump,control:recv:NAK:cif,control:recv:NAK:error"
```

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

**Location**: `connection.go:1118-1204` (`handleHSResponse`)

**Multiple triggers** - All logged with `connection:close:reason`:
- Unsupported version
- Missing required flags (TSBPDRCV, TLPKTDROP, CRYPT, REXMITFLG)
- Invalid flags (STREAM, PACKET_FILTER for HSv4)

**Status**: ✅ **All logged with explicit reasons**

---

### 5. Encryption Errors 🔐

**Location**: `connection.go:1305-1357` (`handleKMResponse`)

**Multiple triggers** - All logged with `connection:close:reason`:
- KM_NOSECRET - Peer didn't enable encryption
- KM_BADSECRET - Peer has different passphrase
- Other encryption errors

**Status**: ✅ **All logged with explicit reasons**

---

### 6. Application-Initiated Close 🖱️

**Location**: `connection.go:1657-1665` (`Close()`)

**Trigger**: Application calls `conn.Close()`

**Current Logging**:
```go
c.log("connection:close:reason", func() string {
    return "application requested close"
})
```

**Log Topic**: `connection:close:reason`

**Status**: ✅ **Logged with explicit reason**

---

### 7. Dialer Connection Timeout ⏱️

**Location**: `dial.go:151-161`

**Trigger**: Server doesn't respond within `ConnectionTimeout`

**Current Logging**:
```go
dl.log("connection:close:reason", func() string {
    return fmt.Sprintf("connection timeout: server didn't respond within %s", dl.config.ConnectionTimeout)
})
```

**Log Topic**: `connection:close:reason`

**Status**: ✅ **Logged with explicit reason**

---

## Root Cause: Missing Proactive Keepalive Implementation

### Problem Statement

**The GoSRT library does not proactively send keepalive packets**, which causes connections to timeout when:
1. No data packets are being sent for 30 seconds
2. Network has high packet loss (keepalives get lost)
3. One side is receive-only (SUBSCRIBE) or send-only (PUBLISH)

### SRT Specification Requirements

According to the SRT specification (draft-sharabayko-srt-01.txt, section 3.2.3):
- **Keep-alive control packets are sent after a certain timeout from the last time any packet (Control or Data) was sent**
- **The default timeout for a keep-alive packet to be sent is 1 second**

### Current Implementation

**What exists:**
- `handleKeepAlive()` - Receives and **echoes** keepalive packets
- `peerIdleTimeout` - Tracks when to close connection if no packets received
- Timeout reset when packets are received

**What's missing:**
- **Proactive keepalive sending** - No code sends keepalives when no packets sent for 1 second
- **Keepalive timer** - No timer to trigger keepalive sending

### Expected Behavior

1. **Track last send time**: Record timestamp when any packet (data or control) is sent
2. **Send keepalive if idle**: If 1 second passes without sending any packet, send a keepalive
3. **Reset on any send**: Reset keepalive timer when any packet is sent (data, ACK, NAK, etc.)

### Impact on Observed Issues

**SUBSCRIBE connections (receive-only):**
- Server sends data packets → Client receives → Client sends ACK/NAK
- If server stops sending data, client stops sending ACK/NAK
- **Without proactive keepalives**, server doesn't receive anything for 30s → timeout

**PUBLISH connections (send-only):**
- Client sends data packets → Server receives → Server sends ACK/NAK
- If client stops sending data, server stops sending ACK/NAK
- **Without proactive keepalives**, client doesn't receive anything for 30s → timeout

**High packet loss networks:**
- Even if keepalives are sent, they may be lost
- Need more frequent keepalives or longer timeout
- Current 30s timeout may be too short for high-loss networks
