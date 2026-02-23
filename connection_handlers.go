package srt

import (
	"fmt"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// handlePacketDirect is called directly from io_uring completion handler
// In lock-free mode (UseEventLoop=true), packets are routed to lock-free rings:
// - Data packets → packetRing → EventLoop drains
// - Control packets → recvControlRing → EventLoop processes
// In legacy mode, uses a per-connection mutex for sequential processing.
// Reference: multi_iouring_design.md Phase 0
func (c *srtConn) handlePacketDirect(p packet.Packet) {
	// Phase 0: Check if we're in completely lock-free mode
	// When UseEventLoop is enabled, all downstream operations are lock-free:
	// - Data packets go to lock-free packetRing
	// - Control packets go to lock-free recvControlRing
	// No mutex needed - the rings provide thread-safe access
	if c.recv != nil && c.recv.UseEventLoop() {
		c.handlePacket(p)
		return
	}

	// Legacy path: acquire mutex for sequential processing
	// Block until mutex available - never drop packets
	// Measure lock timing for debugging and performance monitoring
	if c.metrics != nil && c.metrics.HandlePacketLockTiming != nil {
		waitStart := time.Now()
		c.handlePacketMutex.Lock()
		waitDuration := time.Since(waitStart)

		if waitDuration > 0 {
			c.metrics.HandlePacketLockTiming.RecordWaitTime(waitDuration)
		}
		// Note: RecordHoldTime will increment holdTimeIndex, which serves as acquisition counter

		defer func() {
			holdDuration := time.Since(waitStart)                         // Total time from lock acquisition
			c.metrics.HandlePacketLockTiming.RecordHoldTime(holdDuration) // This increments holdTimeIndex
			c.handlePacketMutex.Unlock()
		}()

		c.handlePacket(p)
	} else {
		// Fallback if metrics not initialized (shouldn't happen in normal operation)
		c.handlePacketMutex.Lock()
		defer c.handlePacketMutex.Unlock()
		c.handlePacket(p)
	}
}

// initializeControlHandlers initializes the control packet dispatch tables.
// This is called once during connection initialization and the maps are never modified,
// so no locking is required for map access.
func (c *srtConn) initializeControlHandlers() {
	// Main control type handlers
	// Note: KEEPALIVE and ACKACK use dispatch functions that route to:
	// - RecvControlRing (if enabled) for EventLoop processing
	// - Locked handlers as fallback
	// Reference: completely_lockfree_receiver.md Section 4.1
	c.controlHandlers = map[packet.CtrlType]controlPacketHandler{
		packet.CTRLTYPE_KEEPALIVE: (*srtConn).dispatchKeepAlive, // Routes to ring or locked handler
		packet.CTRLTYPE_SHUTDOWN:  (*srtConn).handleShutdown,
		packet.CTRLTYPE_NAK:       (*srtConn).handleNAK,
		packet.CTRLTYPE_ACK:       (*srtConn).handleACK,
		packet.CTRLTYPE_ACKACK:    (*srtConn).dispatchACKACK,   // Routes to ring or locked handler
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

	c.resetPeerIdleTimeout()

	header := p.Header()

	if header.IsControlPacket {
		// O(1) lookup in dispatch table (no locking needed - map is immutable)
		handler, ok := c.controlHandlers[header.ControlType]
		if !ok {
			// Unknown control type - log and return gracefully
			c.log("connection:recv:ctrl:unknown", func() string {
				return fmt.Sprintf("unknown control packet type: %s", header.ControlType)
			})
			// Track drop for unknown control type
			if c.metrics != nil {
				// Classify as generic error (unknown control type)
				c.metrics.PktRecvErrorParse.Add(1)
			}
			return
		}

		// Call handler
		handler(c, p)
		return
	}

	if header.PacketSequenceNumber.Gt(c.debug.expectedRcvPacketSequenceNumber) {
		c.log("connection:error", func() string {
			return fmt.Sprintf("recv lost packets. got: %d, expected: %d (%d)\n", header.PacketSequenceNumber.Val(), c.debug.expectedRcvPacketSequenceNumber.Val(), c.debug.expectedRcvPacketSequenceNumber.Distance(header.PacketSequenceNumber))
		})
	}

	c.debug.expectedRcvPacketSequenceNumber = header.PacketSequenceNumber.Inc()

	//fmt.Printf("%s\n", p.String())

	// Ignore FEC filter control packets
	// https://github.com/Haivision/srt/blob/master/docs/features/packet-filtering-and-fec.md
	// "An FEC control packet is distinguished from a regular data packet by having
	// its message number equal to 0. This value isn't normally used in SRT (message
	// numbers start from 1, increment to a maximum, and then roll back to 1)."
	if header.MessageNumber == 0 {
		c.log("connection:filter", func() string { return "dropped FEC filter control packet" })
		// Track drop for FEC filter packet
		if c.metrics != nil {
			c.metrics.PktRecvDataDropped.Add(1)
			c.metrics.ByteRecvDataDropped.Add(uint64(p.Len()))
		}
		return
	}

	// 4.5.1.1.  TSBPD Time Base Calculation
	if !c.tsbpdWrapPeriod {
		if header.Timestamp > packet.MAX_TIMESTAMP-(30*1000000) {
			c.tsbpdWrapPeriod = true
			c.log("connection:tsbpd", func() string { return "TSBPD wrapping period started" })
		}
	} else {
		if header.Timestamp >= (30*1000000) && header.Timestamp <= (60*1000000) {
			c.tsbpdWrapPeriod = false
			c.tsbpdTimeBaseOffset += uint64(packet.MAX_TIMESTAMP) + 1
			c.log("connection:tsbpd", func() string { return "TSBPD wrapping period finished" })
		}
	}

	tsbpdTimeBaseOffset := c.tsbpdTimeBaseOffset
	if c.tsbpdWrapPeriod {
		if header.Timestamp < (30 * 1000000) {
			tsbpdTimeBaseOffset += uint64(packet.MAX_TIMESTAMP) + 1
		}
	}

	header.PktTsbpdTime = c.tsbpdTimeBase + tsbpdTimeBaseOffset + uint64(header.Timestamp) + c.tsbpdDelay + c.tsbpdDrift

	c.log("data:recv:dump", func() string { return p.Dump() })

	c.cryptoLock.Lock()
	if c.crypto != nil {
		if header.KeyBaseEncryptionFlag != 0 {
			if err := c.crypto.EncryptOrDecryptPayload(p.Data(), header.KeyBaseEncryptionFlag, header.PacketSequenceNumber.Val()); err != nil {
				if c.metrics != nil {
					c.metrics.PktRecvUndecrypt.Add(1)
					c.metrics.ByteRecvUndecrypt.Add(uint64(p.Len()))
				}
			}
		} else {
			if c.metrics != nil {
				c.metrics.PktRecvUndecrypt.Add(1)
				c.metrics.ByteRecvUndecrypt.Add(uint64(p.Len()))
			}
		}
	}
	c.cryptoLock.Unlock()

	// Put the packet into receive congestion control
	c.recv.Push(p)
}

// ═══════════════════════════════════════════════════════════════════════════
// Control Packet Dispatch Functions (Phase 6: Completely Lock-Free Receiver)
// ═══════════════════════════════════════════════════════════════════════════

// dispatchACKACK routes ACKACK to control ring or locked handler.
// Simplified: just check recvControlRing != nil (no separate bool).
//
// Reference: completely_lockfree_receiver.md Section 4.1
func (c *srtConn) dispatchACKACK(p packet.Packet) {
	if c.recvControlRing != nil {
		// Push to control ring for EventLoop processing
		ackNum := p.Header().TypeSpecific
		arrivalTime := time.Now()

		if c.recvControlRing.PushACKACK(ackNum, arrivalTime) {
			if c.metrics != nil {
				c.metrics.RecvControlRingPushedACKACK.Add(1)
			}
			return
		}

		// Ring full - fall through to locked path
		if c.metrics != nil {
			c.metrics.RecvControlRingDroppedACKACK.Add(1)
		}
	}

	// Locked path (ring disabled or full)
	c.handleACKACKLocked(p)
}

// dispatchKeepAlive routes KEEPALIVE to control ring or locked handler.
// Simplified: just check recvControlRing != nil (no separate bool).
//
// Reference: completely_lockfree_receiver.md Section 4.1
func (c *srtConn) dispatchKeepAlive(p packet.Packet) {
	if c.recvControlRing != nil {
		if c.recvControlRing.PushKEEPALIVE() {
			if c.metrics != nil {
				c.metrics.RecvControlRingPushedKEEPALIVE.Add(1)
			}
			return
		}

		// Ring full - fall through to locked path
		if c.metrics != nil {
			c.metrics.RecvControlRingDroppedKEEPALIVE.Add(1)
		}
	}

	// Locked path (ring disabled or full)
	c.handleKeepAlive(p)
}

// handleKeepAliveEventLoop is the lock-free variant for EventLoop mode.
// Called when KEEPALIVE packets arrive via RecvControlRing.
//
// This function is completely lock-free - it only updates atomic state.
//
// Reference: completely_lockfree_receiver.md Section 5.1
func (c *srtConn) handleKeepAliveEventLoop() {
	// TDD: This assert will fail until we properly set EventLoop context
	// Reference: multi_iouring_design.md Phase 0.5
	c.AssertEventLoopContext()

	c.resetPeerIdleTimeout()

	if c.metrics != nil {
		c.metrics.RecvControlRingProcessedKEEPALIVE.Add(1)
	}

	c.log("control:recv:keepalive:eventloop", func() string {
		return "keepalive processed via EventLoop"
	})
}

// handleKeepAlive is the locking wrapper for Tick/legacy mode.
// Called from io_uring handlers when control ring is NOT enabled.
// Resets the idle timeout and sends a keepalive response to the peer.
func (c *srtConn) handleKeepAlive(p packet.Packet) {
	// TDD: This assert will fail until we properly set Tick context
	// Reference: multi_iouring_design.md Phase 0.5
	c.AssertTickContext()

	c.log("control:recv:keepalive:dump", func() string { return p.Dump() })

	// Note: Keepalive metrics are tracked via packet classifier in send/recv paths
	// No need to increment here - metrics already tracked

	c.resetPeerIdleTimeout()

	c.log("control:send:keepalive:dump", func() string { return p.Dump() })

	c.pop(p)
}

// sendProactiveKeepalive sends a keepalive packet to keep the connection alive.
// This is used when no data has been received for a while to prevent idle timeout.
func (c *srtConn) sendProactiveKeepalive() {
	p := packet.NewPacket(c.remoteAddr)
	p.Header().IsControlPacket = true
	p.Header().ControlType = packet.CTRLTYPE_KEEPALIVE
	p.Header().TypeSpecific = 0
	p.Header().Timestamp = c.getTimestampForPacket()
	p.Header().DestinationSocketId = c.peerSocketId

	c.log("control:send:keepalive:proactive", func() string {
		return "sending proactive keepalive to maintain connection"
	})

	// Note: Keepalive metrics are tracked via packet classifier in send path
	// No need to increment here - metrics already tracked

	c.pop(p)
}

// getKeepaliveInterval calculates the keepalive interval based on config.
// Returns 0 if proactive keepalives are disabled.
func (c *srtConn) getKeepaliveInterval() time.Duration {
	threshold := c.config.KeepaliveThreshold
	if threshold <= 0 || threshold >= 1.0 {
		return 0 // Disabled or invalid
	}
	return time.Duration(float64(c.config.PeerIdleTimeout) * threshold)
}

// handleShutdown closes the connection
func (c *srtConn) handleShutdown(p packet.Packet) {
	c.log("control:recv:shutdown:dump", func() string { return p.Dump() })

	// Note: Shutdown metrics are tracked via packet classifier in recv path
	// No need to increment here - metrics already tracked

	c.log("connection:close:reason", func() string {
		return "shutdown packet received from peer"
	})
	go c.close(metrics.CloseReasonGraceful)
}

// handleACK forwards the acknowledge sequence number to the congestion control and
// returns a ACKACK (on a full ACK). The RTT is also updated in case of a full ACK.
func (c *srtConn) handleACK(p packet.Packet) {
	c.log("control:recv:ACK:dump", func() string { return p.Dump() })

	// Note: ACK metrics are tracked via packet classifier in recv path
	// No need to increment here - metrics already tracked

	cif := &packet.CIFACK{}

	if err := p.UnmarshalCIF(cif); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:ACK:error", func() string { return fmt.Sprintf("invalid ACK: %s", err) })
		return
	}

	c.log("control:recv:ACK:cif", func() string { return cif.String() })

	// Phase 5: Track Light vs Full ACK received
	if c.metrics != nil {
		if cif.IsLite {
			c.metrics.PktRecvACKLiteSuccess.Add(1)
		} else if !cif.IsSmall {
			c.metrics.PktRecvACKFullSuccess.Add(1)
		}
		// Note: Small ACKs are not separately tracked
	}

	c.snd.ACK(cif.LastACKPacketSequenceNumber)

	if !cif.IsLite && !cif.IsSmall {
		// 4.10.  Round-Trip Time Estimation
		c.recalculateRTT(time.Duration(int64(cif.RTT)) * time.Microsecond)

		// Estimated Link Capacity (from packets/s to Mbps)
		// Store as uint64 (Mbps * 1000) for atomic operations
		if c.metrics != nil {
			mbps := float64(cif.EstimatedLinkCapacity) * MAX_PAYLOAD_SIZE * 8 / 1024 / 1024
			c.metrics.MbpsLinkCapacity.Store(uint64(mbps * 1000))
		}

		c.sendACKACK(p.Header().TypeSpecific)
	}
}

// handleNAK forwards the lost sequence number to the congestion control.
func (c *srtConn) handleNAK(p packet.Packet) {
	c.log("control:recv:NAK:dump", func() string { return p.Dump() })

	// Note: NAK recv metrics are tracked via packet classifier in IncrementRecvMetrics
	// The packet classifier is called by listen.go/dial.go when packets are received.
	// No need to increment here - already tracked in packet_classifier.go

	cif := &packet.CIFNAK{}

	if err := p.UnmarshalCIF(cif); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:NAK:error", func() string { return fmt.Sprintf("invalid NAK: %s", err) })
		return
	}

	c.log("control:recv:NAK:cif", func() string { return cif.String() })

	// Inform congestion control about lost packets and track retransmissions
	retransCount := c.snd.NAK(cif.LostPacketSequenceNumber)
	if retransCount > 0 {
		if c.metrics != nil {
			c.metrics.PktRetransFromNAK.Add(uint64(retransCount))
		}
	}
}

// ExtendedStatistics contains statistics that are not part of the standard SRT Statistics struct.
// These are retrieved in a single call to minimize lock contention.
type ExtendedStatistics struct {
	PktSentACKACK     uint64 // Number of ACKACK packets sent
	PktRecvACKACK     uint64 // Number of ACKACK packets received
	PktRetransFromNAK uint64 // Number of packets retransmitted in response to NAKs
}

// GetExtendedStatistics returns all extended statistics in a single call with a single lock.
// This implements the Conn interface.
func (c *srtConn) GetExtendedStatistics() *ExtendedStatistics {
	if c.metrics == nil {
		return &ExtendedStatistics{}
	}
	return &ExtendedStatistics{
		PktSentACKACK:     c.metrics.PktSentACKACKSuccess.Load(),
		PktRecvACKACK:     c.metrics.PktRecvACKACKSuccess.Load(),
		PktRetransFromNAK: c.metrics.PktRetransFromNAK.Load(),
	}
}

// handleACKACK updates the RTT and NAK interval for the congestion control.
//
// RFC: https://datatracker.ietf.org/doc/html/draft-sharabayko-srt-01#section-3.2.5
//
// ACKACK is sent by the sender in response to a Full ACK (not Light ACK).
// It echoes the ACK Number (TypeSpecific field) from the original ACK.
//
// RTT Calculation (RFC Section 4.10):
//
//	RTT = time_now - time_when_ack_was_sent
//	Where time_when_ack_was_sent is stored in ackNumbers[ACK_Number]
//
// The receiver uses EWMA (Exponential Weighted Moving Average) smoothing:
//
//	RTT = RTT * 0.875 + lastRTT * 0.125
//	RTTVar = RTTVar * 0.75 + |RTT - lastRTT| * 0.25
//
// NAK interval is derived from RTT:
//
//	NAKInterval = (RTT + 4*RTTVar) / 2   (minimum 20ms)
//
// Cleanup: Entries in ackNumbers older than the current ACKACK are deleted
// to prevent unbounded map growth from lost ACKACKs.

// handleACKACK is the lock-free variant for EventLoop mode.
// Called when control packets arrive via RecvControlRing.
//
// Parameters:
//   - ackNum: the ACK number from the ACKACK packet
//   - arrivalTime: when the ACKACK arrived (captured at ring push time)
//
// This function is completely lock-free - the EventLoop is the single-threaded
// consumer of the ackNumbers btree, so no synchronization is needed.
//
// Reference: completely_lockfree_receiver.md Section 5.1
func (c *srtConn) handleACKACK(ackNum uint32, arrivalTime time.Time) {
	// TDD: This assert will fail until we properly set EventLoop context
	// Reference: multi_iouring_design.md Phase 0.5
	c.AssertEventLoopContext()

	// Note: NO LOCK - EventLoop is single-threaded consumer of ackNumbers btree

	entry := c.ackNumbers.Get(ackNum)
	btreeLen := c.ackNumbers.Len()

	if entry != nil {
		// 4.10. Round-Trip Time Estimation
		rttDuration := arrivalTime.Sub(entry.timestamp)
		c.recalculateRTT(rttDuration)

		c.log("control:recv:ACKACK:rtt:debug", func() string {
			return fmt.Sprintf("ACKACK RTT (EventLoop): ackNum=%d, entryTimestamp=%s, arrivalTime=%s, rtt=%v, btreeLen=%d",
				ackNum, entry.timestamp.Format("15:04:05.000000"), arrivalTime.Format("15:04:05.000000"),
				rttDuration, btreeLen)
		})

		c.ackNumbers.Delete(ackNum)
		PutAckEntry(entry) // Return to pool
	} else {
		c.log("control:recv:ACKACK:error", func() string {
			return fmt.Sprintf("got unknown ACKACK (%d), btreeLen=%d", ackNum, btreeLen)
		})
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
			c.metrics.AckBtreeUnknownACKACK.Add(1)
		}
	}

	// Bulk cleanup of stale entries
	expiredCount, expired := c.ackNumbers.ExpireOlderThan(ackNum)
	btreeLenAfter := c.ackNumbers.Len()

	// Update metrics
	if c.metrics != nil {
		c.metrics.AckBtreeEntriesExpired.Add(uint64(expiredCount))
		c.metrics.AckBtreeSize.Store(uint64(btreeLenAfter))
		// Note: RecvControlRingProcessedACKACK is incremented by drainRecvControlRing()
		// after this function returns, not here (to avoid double-counting)
	}

	// Return expired entries to pool
	PutAckEntries(expired)

	c.recv.SetNAKInterval(uint64(c.rtt.NAKInterval()))
}

// handleACKACKLocked is the locking wrapper for Tick/legacy mode.
// Called from io_uring handlers when control ring is NOT enabled.
// Acquires c.ackLock to protect ackNumbers btree access.
func (c *srtConn) handleACKACKLocked(p packet.Packet) {
	// TDD: This assert will fail until we properly set Tick context
	// Reference: multi_iouring_design.md Phase 0.5
	c.AssertTickContext()

	c.log("control:recv:ACKACK:dump", func() string { return p.Dump() })

	ackNum := p.Header().TypeSpecific
	arrivalTime := time.Now()

	c.ackLock.Lock()
	entry := c.ackNumbers.Get(ackNum)
	btreeLen := c.ackNumbers.Len()

	if entry != nil {
		// 4.10. Round-Trip Time Estimation
		rttDuration := arrivalTime.Sub(entry.timestamp)
		c.recalculateRTT(rttDuration)

		c.log("control:recv:ACKACK:rtt:debug", func() string {
			return fmt.Sprintf("ACKACK RTT (Locked): ackNum=%d, rtt=%v, btreeLen=%d",
				ackNum, rttDuration, btreeLen)
		})

		c.ackNumbers.Delete(ackNum)
		PutAckEntry(entry) // Return to pool
	} else {
		c.log("control:recv:ACKACK:error", func() string {
			return fmt.Sprintf("got unknown ACKACK (%d), btreeLen=%d", ackNum, btreeLen)
		})
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
			c.metrics.AckBtreeUnknownACKACK.Add(1)
		}
	}

	// Bulk cleanup of stale entries
	expiredCount, expired := c.ackNumbers.ExpireOlderThan(ackNum)
	btreeLenAfter := c.ackNumbers.Len()
	c.ackLock.Unlock()

	// Update metrics (outside lock)
	if c.metrics != nil {
		c.metrics.AckBtreeEntriesExpired.Add(uint64(expiredCount))
		c.metrics.AckBtreeSize.Store(uint64(btreeLenAfter))
	}

	// Return expired entries to pool (outside lock)
	PutAckEntries(expired)

	c.recv.SetNAKInterval(uint64(c.rtt.NAKInterval()))
}

// recalculateRTT recalculates the RTT based on a full ACK exchange
func (c *srtConn) recalculateRTT(rtt time.Duration) {
	c.rtt.Recalculate(rtt)

	// Update RTT metrics (Phase 4: ACK/ACKACK Redesign)
	if c.metrics != nil {
		c.metrics.RTTMicroseconds.Store(uint64(c.rtt.RTT()))
		c.metrics.RTTVarMicroseconds.Store(uint64(c.rtt.RTTVar()))
		// Raw RTT: last sample without EWMA smoothing (for diagnostics)
		c.metrics.RTTLastSampleMicroseconds.Store(c.rtt.RTTLastSample())
	}

	c.log("connection:rtt", func() string {
		return fmt.Sprintf("RTT=%.0fus RTTVar=%.0fus RTTRaw=%dus NAKInterval=%.0fms",
			c.rtt.RTT(), c.rtt.RTTVar(), c.rtt.RTTLastSample(), c.rtt.NAKInterval()/1000)
	})
}

// getNextACKNumber returns the next ACK number using atomic CAS.
// ACK numbers are monotonically increasing 32-bit counters, starting at 1.
// Value 0 is reserved for Light ACKs (which don't trigger ACKACK).
//
// This is lock-free and safe for concurrent use.
// Reference: ack_optimization_implementation.md → "### Improvement #2: Atomic nextACKNumber with CAS"
func (c *srtConn) getNextACKNumber() uint32 {
	for {
		current := c.nextACKNumber.Load()
		next := current + 1
		if next == 0 {
			next = 1 // Skip 0 (reserved for Light ACK)
		}
		if c.nextACKNumber.CompareAndSwap(current, next) {
			return current
		}
		// CAS failed, another goroutine incremented - retry
	}
}
