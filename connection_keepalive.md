# SRT Connection Keepalive Behavior in gosrt

This document describes how keepalive packets work in the gosrt package, including code references, configuration options, and debugging information.

## Overview

Keepalive packets in SRT are control packets used to maintain connection liveness and reset the peer idle timeout. In gosrt, keepalives follow an echo pattern: when a keepalive is received, it is immediately echoed back to the sender, and the peer idle timeout is reset.

**Important**: According to the [SRT RFC Section 3.2.3](https://haivision.github.io/srt-rfc/draft-sharabayko-srt.html#live-streaming-use-case), keepalives are typically sent when there's no data flow. In gosrt's implementation:

- **Keepalives are NOT automatically sent during data flow** - gosrt does not have a timer that proactively sends keepalives
- **ACKs serve the keepalive function** - During active data flow, ACKs are sent every 10ms (`PeriodicACKInterval: 10_000` microseconds), and these ACKs reset the peer idle timeout just like keepalives would
- **Keepalives are only echoed** - When a keepalive is received, it's immediately echoed back (Location: `connection.go:772`)
- **Keepalives are only needed** when there's a gap in data flow AND no ACKs are being sent

## Key Concepts

### Peer Idle Timeout

The **peer idle timeout** is a timer that tracks how long it has been since the last packet (data or control) was received from the peer. If no packets are received within this timeout period, the connection is automatically closed.

- **Default value**: 2 seconds (`2 * time.Second`)
- **Configuration**: `PeerIdleTimeout` in `Config` struct
- **Location**: `config.go:132` (field definition), `config.go:205` (default value)

### Keepalive Packet Type

Keepalive packets are SRT control packets with type `CTRLTYPE_KEEPALIVE` (value `0x0001`).

- **Definition**: `packet/packet.go:29`
- **String representation**: `packet/packet.go:44-45`

## Code Flow

### 1. Peer Idle Timeout Initialization

When a new SRT connection is created, a peer idle timeout timer is initialized.

**Location**: `connection.go:296-304`

```go
c.peerIdleTimeout = time.AfterFunc(c.config.PeerIdleTimeout, func() {
    c.log("connection:close", func() string {
        return fmt.Sprintf("no more data received from peer for %s. shutting down", c.config.PeerIdleTimeout)
    })
    c.log("connection:timeout:expired", func() string {
        return fmt.Sprintf("peer idle timeout of %s expired, closing connection", c.config.PeerIdleTimeout)
    })
    go c.close()
})
```

**Function**: `newSRTConn()` in `connection.go:241`

### 2. Packet Reception and Timeout Reset

When **any packet** (data or control) is received from the peer, the peer idle timeout is reset.

**Location**: `connection.go:639-644`

**Function**: `handlePacket()` in `connection.go:639`

```go
func (c *srtConn) handlePacket(p packet.Packet) {
    if p == nil {
        return
    }

    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
    // ... packet processing continues
}
```

**Key Point**: The timeout is reset for **all** received packets, not just keepalives. This includes:
- Data packets
- ACK packets
- ACKACK packets
- NAK packets
- Keepalive packets
- Other control packets

### 3. Keepalive Packet Handling

When a keepalive packet is received, it is processed by the `handleKeepAlive()` function.

**Location**: `connection.go:648-653` (detection), `connection.go:752-770` (handler)

**Function**: `handleKeepAlive()` in `connection.go:753`

```go
// handleKeepAlive resets the idle timeout and sends a keepalive to the peer.
func (c *srtConn) handleKeepAlive(p packet.Packet) {
    c.log("control:recv:keepalive:dump", func() string { return p.Dump() })

    c.statisticsLock.Lock()
    c.statistics.pktRecvKeepalive++
    c.statistics.pktSentKeepalive++
    c.statisticsLock.Unlock()

    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)

    c.log("connection:keepalive:timeout", func() string {
        return fmt.Sprintf("peer idle timeout reset to %s", c.config.PeerIdleTimeout)
    })

    c.log("control:send:keepalive:dump", func() string { return p.Dump() })

    c.pop(p)
}
```

**Behavior**:
1. Logs the received keepalive packet
2. Increments both `pktRecvKeepalive` and `pktSentKeepalive` statistics
3. Resets the peer idle timeout
4. Logs the timeout reset
5. Echoes the keepalive back to the peer via `c.pop(p)`

### 4. Keepalive Detection

Keepalive packets are detected in the `handlePacket()` function when processing control packets.

**Location**: `connection.go:648-653`

```go
if header.IsControlPacket {
    if header.ControlType == packet.CTRLTYPE_KEEPALIVE {
        c.log("connection:timeout:reset", func() string {
            return fmt.Sprintf("peer idle timeout reset by keepalive packet, timeout=%s", c.config.PeerIdleTimeout)
        })
        c.handleKeepAlive(p)
    }
    // ... other control packet types
}
```

## Statistics

Keepalive statistics are tracked in the `connStats` struct:

**Location**: `connection.go:130-131`

```go
type connStats struct {
    // ...
    pktSentKeepalive  uint64
    pktRecvKeepalive  uint64
    // ...
}
```

**Note**: When a keepalive is received, **both** counters are incremented because the keepalive is echoed back. This is done in `handleKeepAlive()` at `connection.go:757-758`.

## Configuration

### PeerIdleTimeout

The peer idle timeout can be configured when creating a listener or dialer.

**Field**: `Config.PeerIdleTimeout`
**Type**: `time.Duration`
**Default**: `2 * time.Second`
**Location**:
- Field definition: `config.go:132`
- Default value: `config.go:205`
- URL parameter: `peeridletimeo` (in milliseconds) - `config.go:412-416`

**Example**:
```go
config := srt.DefaultConfig()
config.PeerIdleTimeout = 90 * time.Second
```

**URL Parameter Example**:
```
srt://host:port?peeridletimeo=90000
```

### NAKReport

NAKReport enables periodic NAK (Negative Acknowledgement) reports, which are required for live streaming mode according to the [SRT RFC](https://haivision.github.io/srt-rfc/draft-sharabayko-srt.html#live-streaming-use-case).

**Field**: `Config.NAKReport`
**Type**: `bool`
**Default**: `true` (Location: `config.go:199`)
**Enforcement**:
- The `Validate()` function enforces `NAKReport = true` for live mode (Location: `config.go:641`)
- If set to `false`, validation returns an error: `"config: NAKReport must be enabled"` (Location: `config.go:688-689`)
- **Cannot be disabled** - NAKReport is always enabled in gosrt

**URL Parameter**: `nakreport` (Location: `config.go:377-383`)
- Note: Even if set to `false` in the URL, it will be forced to `true` during validation

**Client Configuration**: No configuration needed - NAKReport is enabled by default and enforced.

## Logging

The following log topics are available for debugging keepalive behavior. **All log messages now include timestamps** (format: `2006-01-02 15:04:05.000000000`) to help correlate events between client and server.

### Log Topics

1. **`control:recv:keepalive:dump`**
   - Logs when a keepalive packet is received
   - Location: `connection.go:754`
   - Shows the full packet dump

2. **`control:send:keepalive:dump`**
   - Logs when a keepalive packet is sent (echoed back)
   - Location: `connection.go:767`
   - Shows the full packet dump

3. **`connection:timeout:reset`**
   - Logs when a keepalive packet resets the peer idle timeout
   - Location: `connection.go:650-652`
   - Shows: `"peer idle timeout reset by keepalive packet, timeout=<duration>"`

4. **`connection:keepalive:timeout`**
   - Logs when the keepalive handler resets the timeout
   - Location: `connection.go:763-765`
   - Shows: `"peer idle timeout reset to <duration>"`

5. **`connection:timeout:expired`**
   - Logs when the peer idle timeout expires
   - Location: `connection.go:300-302`
   - Shows: `"peer idle timeout of <duration> expired, closing connection"`

6. **`connection:close`**
   - Logs when a connection is closed, including timeout reasons
   - Location: `connection.go:297-299`
   - Shows: `"no more data received from peer for <duration>. shutting down"`

7. **`connection:ack:send`** (Client-side)
   - Logs when an ACK is being sent by the client
   - Location: `connection.go:1243-1245`
   - Shows: `"sending ACK: seqno=<number>, lite=<true/false>"`

8. **`control:recv:ACK:seqno`** (Server-side)
   - Logs when an ACK is received from the client, including sequence number
   - Location: `connection.go:806-808`
   - Shows: `"received ACK from peer: seqno=<number>, lite=<true/false>, rtt=<microseconds>"`
   - **Useful for tracking ACK reception on the server side**

9. **`packet:send:error`**
   - Logs when socket writes fail (newly added for debugging)
   - Location: `dial.go:275`, `listen.go:444`
   - Shows: `"failed to write packet to network: <error>"`
   - **Critical for identifying when ACKs are generated but not transmitted**

### Example Usage

Enable keepalive and ACK logging when starting the server:

```bash
./server -addr :6001 -logtopics "connection:close,connection:timeout:reset,connection:keepalive:timeout,connection:timeout:expired,control:recv:keepalive,control:send:keepalive,control:recv:ACK:seqno,packet:send:error"
```

Enable ACK logging on the client:

```bash
./contrib/client/client -from "srt://..." -to - -logtopics "connection:ack:send,packet:send:error,connection:error"
```

**Note**: All log messages now include timestamps, making it easier to correlate events between client and server logs.

## Important Notes

### Timeout Reset Behavior

**Critical**: The peer idle timeout is reset by **any** received packet, not just keepalives. This includes:
- Data packets (most common during active streaming)
- ACK packets (sent every 10ms during active data flow)
- ACKACK packets
- NAK packets
- Keepalive packets
- Other control packets

**Location**: `connection.go:644`

This means that during active data transmission, the timeout is continuously reset by data and ACK packets, and keepalives are only necessary when there's no data flow.

**During Active Data Flow**:
- The receiver sends ACKs every 10ms (`PeriodicACKInterval: 10_000` microseconds - Location: `connection.go:312`)
- These ACKs reset the peer idle timeout (Location: `connection.go:644`)
- Keepalives are **not** sent automatically - they are only echoed when received
- This is compliant with the SRT RFC, which states keepalives are typically sent when there's no data flow

### Keepalive Echo Pattern

In gosrt, keepalives follow an **echo pattern**: when a keepalive is received, it is immediately echoed back to the sender. This is different from some implementations that send keepalives proactively on a timer.

**Location**: `connection.go:769` - the keepalive packet is sent back via `c.pop(p)`

### Network Queue Full Condition

If the network queue is full (1024 packets), incoming packets are dropped silently, which means the timeout is **not** reset. This could cause premature connection closure.

**Location**: `connection.go:517-526`

```go
func (c *srtConn) push(p packet.Packet) {
    // Non-blocking write to the network queue
    select {
    case <-c.ctx.Done():
    case c.networkQueue <- p:
    default:
        c.log("connection:error", func() string { return "network queue is full" })
        // Packet is dropped - timeout is NOT reset!
    }
}
```

**Log topic**: `connection:error` with message `"network queue is full"`

## ACK Generation and Potential Issues

### How ACKs Are Generated

ACKs are generated by the receiver congestion control's `periodicACK()` function, which is called every 10ms by the ticker. **ACKs serve as the ONLY mechanism for resetting the peer idle timeout during active data flow with no packet loss**, as:
- Keepalives are not automatically sent
- NAKs are only sent when there are lost packets (gaps in sequence)
- On perfect networks with zero loss, NAKs are never sent

**Location**: `congestion/live/receive.go:257-321` (`periodicACK`)

**Key Logic**:
1. If less than 10ms has passed since the last ACK AND less than 64 packets have been received, it returns early (no ACK sent) - **Line 262-267**
2. Otherwise, it calculates the sequence number to ACK and returns `ok = true`
3. The ticker then calls `sendACK()` if `ok = true` - **Location**: `congestion/live/receive.go:363-366`

**ACK Interval**: ACKs are sent every 10ms (`PeriodicACKInterval: 10_000` microseconds) during active data flow
- **Location**: `connection.go:312` (configuration)
- **Location**: `connection.go:404-419` (ticker that calls `recv.Tick()` every 10ms)

**Potential Issue**: If the client's `Read()` is blocked or slow (e.g., ffplay processing slowly), packets may accumulate in `readQueue`. When `readQueue` is full (1024 packets), new packets are dropped - **Location**: `connection.go:632`. However, packets are removed from `packetList` before being delivered, so `periodicACK()` might not have packets to process, potentially affecting ACK generation.

### NAK Generation (Periodic NAK Reports)

NAKs are generated by `periodicNAK()` which is called every 20ms by the ticker, but **only sent when there are lost packets**.

**Location**: `congestion/live/receive.go:323-361` (`periodicNAK`)

**Key Logic**:
1. Checks if 20ms has passed since last NAK (`PeriodicNAKInterval: 20_000` microseconds - Location: `connection.go:313`)
2. Scans `packetList` for gaps in sequence numbers (lost packets)
3. If gaps are found, returns a list of sequence numbers to NAK
4. If **no gaps** (all packets in sequence), returns an **empty list**
5. NAK is only sent if the list is not empty (Location: `congestion/live/receive.go:370-372`)

**Critical Finding**: On perfect networks with zero packet loss:
- NAKs are **never sent** (empty list returned)
- NAKs do **not** reset the peer idle timeout
- The connection relies **entirely** on ACKs (every 10ms) to stay alive
- If ACK generation stops, the connection will timeout after `PeerIdleTimeout` (default 2 seconds)

This explains why connections can close unexpectedly on perfect networks - if ACK generation fails for any reason, there's no fallback mechanism.

**Critical**: If ACKs stop being sent (e.g., due to a blocked Read(), ticker stopping, or network queue issues), the server will not receive any packets and the peer idle timeout will expire, causing the connection to close.

**Critical Bug - Statistics vs. Actual Transmission**: The `pktSentACK` statistic is incremented **before** the packet is actually sent to the network (Location: `connection.go:1289-1293`). The socket write error is **ignored** (Location: `dial.go:275` - `dl.pc.Write(buffer)` returns an error that is not checked). This means:
- Statistics may show ACKs as "sent" even when they never reach the network
- If socket writes fail (e.g., socket buffer full, network interface down, etc.), statistics will still increment
- The server won't receive the ACKs, causing the peer idle timeout to expire
- This explains why pcap shows no ACKs being sent, but client statistics show 89,146 ACKs "sent"

**Important Note on NAKs**: NAKs are **only sent when there are lost packets** (gaps in sequence numbers). If there's no packet loss, `periodicNAK()` returns an empty list and no NAK is sent (Location: `congestion/live/receive.go:323-361`, `congestion/live/receive.go:370-372`). This means:
- NAKs do **not** serve as a keepalive mechanism when there's no packet loss
- On perfect networks with no loss, the connection relies **entirely** on ACKs to stay alive
- If ACK generation stops, there's no fallback mechanism (no NAKs, no automatic keepalives)
- This is a design consideration for networks with zero packet loss

### ACK Sending

When `periodicACK()` returns `ok = true`, the ticker calls `sendACK()`.

**Location**: `connection.go:1242` (`sendACK`)

**Log Topic**: `connection:ack:send` - Logs when an ACK is being sent (newly added for debugging)

## Troubleshooting

### Connection Closes Unexpectedly

If connections are closing unexpectedly, check:

1. **Peer Idle Timeout Configuration**
   - Verify both server and client have compatible timeout values
   - Default is 2 seconds, which may be too short for some networks
   - Recommended: 30-90 seconds for production

2. **Network Queue Full**
   - Check for `"network queue is full"` errors in logs
   - This indicates packets are being dropped before processing
   - The timeout won't reset if packets are dropped

3. **Enable Logging**
   - Use the log topics listed above to see:
     - When keepalives are received/sent
     - When the timeout is reset
     - When the timeout expires

4. **Statistics**
   - Check `pktRecvKeepalive` and `pktSentKeepalive` in connection statistics
   - If both are 0, no keepalives are being exchanged (this is normal during active data flow)
   - Check `pktSentACK` - should show ACKs being sent every ~10ms during active data flow
   - If `pktSentACK` stops increasing, ACKs are not being sent, which will cause timeout

5. **ACK Generation**
   - Enable `connection:ack:send` logging to see when ACKs are being sent
   - If ACK logs stop appearing, investigate why ACK generation has stopped
   - Check for `"readQueue was blocking, dropping packet"` errors - this may affect ACK generation
   - **Critical**: On perfect networks with no packet loss, ACKs are the ONLY mechanism keeping the connection alive
   - If ACKs stop, there's no fallback (NAKs won't be sent without packet loss, keepalives aren't automatic)

6. **NAK Behavior on Perfect Networks**
   - Check `pktSentNAK` in statistics - should be 0 on perfect networks
   - This is expected behavior - NAKs are only sent when packets are lost
   - Don't rely on NAKs to keep connections alive on lossless networks

## Related Code References

- **Connection creation**: `connection.go:241` (`newSRTConn`)
- **Packet handling**: `connection.go:639` (`handlePacket`)
- **Keepalive handler**: `connection.go:753` (`handleKeepAlive`)
- **Timeout initialization**: `connection.go:296`
- **Timeout reset**: `connection.go:644`, `connection.go:761`
- **Config definition**: `config.go:132`
- **Config default**: `config.go:205`
- **Packet type definition**: `packet/packet.go:29`
- **Network queue**: `connection.go:282` (size: 1024)
- **Queue push**: `connection.go:518` (`push`)
- **ACK generation**: `congestion/live/receive.go:257` (`periodicACK`)
- **ACK sending**: `connection.go:1242` (`sendACK`)
- **Ticker**: `connection.go:404` (`ticker`) - calls `recv.Tick()` every 10ms
- **Periodic ACK interval**: `connection.go:312` (`PeriodicACKInterval: 10_000`)
- **NAK generation**: `congestion/live/receive.go:323` (`periodicNAK`)
- **NAK sending**: `connection.go:1217` (`sendNAK`)
- **Periodic NAK interval**: `connection.go:313` (`PeriodicNAKInterval: 20_000`)
- **NAKReport default**: `config.go:199` (`NAKReport: true`)
- **NAKReport enforcement**: `config.go:641`, `config.go:688-689`

## Real-World Example: Connection Closure Due to Missing ACKs

The following logs demonstrate a connection closure from both server and client perspectives, showing what happens when ACKs stop being received by the server.

### Server Log

```
SUBSCRIBE       START /live/stream (172.16.40.142:65026)
0xbdb43508 connection:close (in /home/das/Downloads/gosrt/connection.go:297)
no more data received from peer for 2s. shutting down
0xbdb43508 connection:close (in /home/das/Downloads/gosrt/connection.go:1398)
stopping peer idle timeout
0xbdb43508 connection:close (in /home/das/Downloads/gosrt/connection.go:1402)
sending shutdown message to peer
```

**Key Statistics from the Server:**
- **PktSent**: 3,942,630 (server sent 3.9M data packets)
- **PktRecv**: 0 (server received 0 data packets - client is receiving, not sending data)
- **PktSentACK**: 165,756 (server sent 165K ACKs)
- **PktRecvACK**: 147,739 (server received 147K ACKs from client)
- **Connection Duration**: ~1996 seconds (~33 minutes)
- **Close Reason**: "no more data received from peer for 2s. shutting down"

### Client Log

The client log shows ACKs being generated continuously up until the connection closes:

```
0xa9194f54 connection:ack:send (in /home/das/Downloads/gosrt/connection.go:1243)
sending ACK: seqno=614386185, lite=false
0xa9194f54 connection:ack:send (in /home/das/Downloads/gosrt/connection.go:1243)
sending ACK: seqno=614386222, lite=false
0xa9194f54 connection:ack:send (in /home/das/Downloads/gosrt/connection.go:1243)
sending ACK: seqno=614386281, lite=false
...
```

**Key Observations from Client:**
- ACKs are being generated continuously (logged every ~10ms)
- No `packet:send:error` messages, indicating socket writes are succeeding
- ACK sequence numbers are incrementing normally
- The client continues generating ACKs until the connection closes

### Analysis

**What Happened:**
1. The server was successfully receiving ACKs from the client (147,739 ACKs received over ~33 minutes)
2. The server's `PeerIdleTimeout` was 2 seconds (default value, not the 90 seconds configured)
3. At some point, ACKs stopped arriving at the server (either the client stopped sending them, or they were lost in transit)
4. After 2 seconds of no packets, the server's peer idle timeout expired
5. The server closed the connection with the message "no more data received from peer for 2s. shutting down"

**Important Observations:**
- The server received 147,739 ACKs over ~33 minutes, which is approximately 74 ACKs/second (expected ~100 ACKs/second for 10ms intervals, accounting for some variance)
- The discrepancy between server's `PktSentACK` (165,756) and `PktRecvACK` (147,739) suggests some ACKs may have been lost in transit, or there's a timing difference in when statistics are collected
- The connection lasted much longer than the 2-second timeout, confirming that ACKs were successfully resetting the timeout for most of the connection duration
- The client logs show ACKs being generated, but the server stopped receiving them, suggesting either:
  - Network packet loss between client and server
  - Client's socket writes started failing silently (though no `packet:send:error` was logged)
  - Client's ACK generation stopped (though logs show it continued)
- The final closure occurred when ACKs stopped arriving at the server, causing the 2-second timeout to expire

**Location**: `connection.go:297-299` - The peer idle timeout expiration message

## See Also

- [SRT Protocol Specification](https://datatracker.ietf.org/doc/html/draft-sharabayko-srt-00)
- SRT RFC Section 4.8.1: Packet Acknowledgement (ACKs, ACKACKs)
- `README.md` for general gosrt documentation
- `config.go` for all configuration options

