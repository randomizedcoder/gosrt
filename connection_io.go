package srt

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/packet"
)

// ReadPacket reads a packet from the queue of received packets. It blocks
// if the queue is empty. Only data packets are returned. Using ReadPacket
// and Read at the same time may lead to data loss.
func (c *srtConn) ReadPacket() (packet.Packet, error) {
	var p packet.Packet
	select {
	case <-c.ctx.Done():
		return nil, io.EOF
	case p = <-c.readQueue:
	}

	if p.Header().PacketSequenceNumber.Gt(c.debug.expectedReadPacketSequenceNumber) {
		c.log("connection:error", func() string {
			return fmt.Sprintf("lost packets. got: %d, expected: %d (%d)", p.Header().PacketSequenceNumber.Val(), c.debug.expectedReadPacketSequenceNumber.Val(), c.debug.expectedReadPacketSequenceNumber.Distance(p.Header().PacketSequenceNumber))
		})
	} else if p.Header().PacketSequenceNumber.Lt(c.debug.expectedReadPacketSequenceNumber) {
		c.log("connection:error", func() string {
			return fmt.Sprintf("packet out of order. got: %d, expected: %d (%d)", p.Header().PacketSequenceNumber.Val(), c.debug.expectedReadPacketSequenceNumber.Val(), c.debug.expectedReadPacketSequenceNumber.Distance(p.Header().PacketSequenceNumber))
		})
		return nil, io.EOF
	}

	c.debug.expectedReadPacketSequenceNumber = p.Header().PacketSequenceNumber.Inc()

	return p, nil
}

// Read reads data from the connection.
func (c *srtConn) Read(b []byte) (int, error) {
	if c.readBuffer.Len() != 0 {
		return c.readBuffer.Read(b)
	}

	c.readBuffer.Reset()

	p, err := c.ReadPacket()
	if err != nil {
		return 0, err
	}

	c.readBuffer.Write(p.Data())

	// The packet is out of congestion control and written to the read buffer
	p.Decommission()

	return c.readBuffer.Read(b)
}

// WritePacket writes a packet to the write queue. Packets on the write queue
// will be sent to the peer of the connection. Only data packets will be sent.
func (c *srtConn) WritePacket(p packet.Packet) error {
	if p.Header().IsControlPacket {
		// Ignore control packets
		return nil
	}

	_, err := c.Write(p.Data())
	if err != nil {
		return err
	}

	return nil
}

// Write writes data to the connection.
func (c *srtConn) Write(b []byte) (int, error) {
	// Check context cancellation FIRST, before any operations.
	// This follows the context_and_cancellation_new_design.md pattern where
	// context cancellation signals shutdown. Go's select is non-deterministic
	// when multiple channels are ready, so we must check ctx.Done() separately
	// to ensure Write() returns io.EOF after Close() is called.
	select {
	case <-c.ctx.Done():
		return 0, io.EOF
	default:
	}

	c.writeBuffer.Write(b)

	for {
		n, err := c.writeBuffer.Read(c.writeData)
		if err != nil {
			return 0, err
		}

		p := packet.NewPacket(nil)

		p.SetData(c.writeData[:n])

		p.Header().IsControlPacket = false
		// Give the packet a deliver timestamp
		p.Header().PktTsbpdTime = c.getTimestamp()

		// ═══════════════════════════════════════════════════════════════════════
		// WRITE PATH DISPATCH
		// When sender ring is enabled, use PushDirect for lower latency
		// (bypasses writeQueue channel and writeQueueReader goroutine).
		// Falls back to writeQueue channel for legacy (non-ring) mode.
		// Reference: sender_lockfree_architecture.md Section 7.8
		// ═══════════════════════════════════════════════════════════════════════
		select {
		case <-c.ctx.Done():
			return 0, io.EOF
		default:
		}

		if c.snd.UseRing() {
			// Direct push to lock-free ring (lower latency)
			if !c.snd.PushDirect(p) {
				// Ring full - drop packet
				p.Decommission()
				c.metrics.SendRingDropped.Add(1)
				return 0, io.EOF
			}
		} else {
			// Legacy path via writeQueue channel
			select {
			case c.writeQueue <- p:
			default:
				return 0, io.EOF
			}
		}

		if c.writeBuffer.Len() == 0 {
			break
		}
	}

	c.writeBuffer.Reset()

	return len(b), nil
}

// push puts a packet on the network queue. This is where packets go that came in from the network.
func (c *srtConn) push(p packet.Packet) {
	// Non-blocking write to the network queue
	select {
	case <-c.ctx.Done():
	case c.networkQueue <- p:
	default:
		c.log("connection:error", func() string { return "network queue is full" })
	}
}

// getTimestamp returns the elapsed time since the start of the connection in microseconds.
func (c *srtConn) getTimestamp() uint64 {
	return uint64(time.Since(c.start).Microseconds())
}

// getTimestampForPacket returns the elapsed time since the start of the connection in
// microseconds clamped a 32bit value.
func (c *srtConn) getTimestampForPacket() uint32 {
	return uint32(c.getTimestamp() & uint64(packet.MAX_TIMESTAMP))
}

// pop adds the destination address and socketid to the packet and sends it out to the network.
// The packet will be encrypted if required.
func (c *srtConn) pop(p packet.Packet) {
	p.Header().Addr = c.remoteAddr
	p.Header().DestinationSocketId = c.peerSocketId

	// OPTIMIZATION: Only acquire crypto lock if crypto is enabled AND this is a data packet.
	// Previously, the lock was acquired/released for EVERY data packet even
	// when crypto was nil, causing unnecessary overhead on non-encrypted connections.
	// See: send_eventloop_intermittent_failure_bug.md Section 22.8.2
	if !p.Header().IsControlPacket && c.crypto != nil {
		c.cryptoLock.Lock()
		p.Header().KeyBaseEncryptionFlag = c.keyBaseEncryption
		if !p.Header().RetransmittedPacketFlag {
			if err := c.crypto.EncryptOrDecryptPayload(p.Data(), p.Header().KeyBaseEncryptionFlag, p.Header().PacketSequenceNumber.Val()); err != nil {
				c.log("connection:send:error", func() string {
					return fmt.Sprintf("encryption failed: %v", err)
				})
				// Track error in metrics if available
				if c.metrics != nil {
					c.metrics.CryptoErrorEncrypt.Add(1)
					c.metrics.PktSentDataError.Add(1)
				}
			}
		}

		c.kmPreAnnounceCountdown--
		c.kmRefreshCountdown--

		if c.kmPreAnnounceCountdown == 0 && !c.kmConfirmed {
			c.sendKMRequest(c.keyBaseEncryption.Opposite())

			// Resend the request until we get a response
			c.kmPreAnnounceCountdown = c.config.KMPreAnnounce/10 + 1
		}

		if c.kmRefreshCountdown == 0 {
			c.kmPreAnnounceCountdown = c.config.KMRefreshRate - c.config.KMPreAnnounce
			c.kmRefreshCountdown = c.config.KMRefreshRate

			// Switch the keys
			c.keyBaseEncryption = c.keyBaseEncryption.Opposite()

			c.kmConfirmed = false
		}

		if c.kmRefreshCountdown == c.config.KMRefreshRate-c.config.KMPreAnnounce {
			// Decommission the previous key, resp. create a new SEK that will
			// be used in the next switch.
			if err := c.crypto.GenerateSEK(c.keyBaseEncryption.Opposite()); err != nil {
				c.log("connection:crypto:error", func() string {
					return fmt.Sprintf("failed to generate SEK: %v", err)
				})
				// Track error in metrics if available
				if c.metrics != nil {
					c.metrics.CryptoErrorGenerateSEK.Add(1)
				}
			}
		}
		c.cryptoLock.Unlock()
	}

	// Debug logging for data packets (runs regardless of crypto)
	if !p.Header().IsControlPacket {
		c.log("data:send:dump", func() string { return p.Dump() })
	}

	// Check optional send filter (for testing packet drops)
	if c.sendFilter != nil && !c.sendFilter(p) {
		return // Filter returned false - drop packet
	}

	// Send the packet on the wire
	c.onSend(p)
}

// networkQueueReader reads the packets from the network queue in order to process them.
func (c *srtConn) networkQueueReader(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	defer func() {
		c.log("connection:close", func() string { return "left network queue reader loop" })
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-c.networkQueue:
			c.handlePacket(p)
		}
	}
}

// writeQueueReader reads the packets from the write queue and puts them into congestion
// control for sending.
func (c *srtConn) writeQueueReader(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	defer func() {
		c.log("connection:close", func() string { return "left write queue reader loop" })
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-c.writeQueue:
			// Put the packet into the send congestion control
			c.snd.Push(p)
		}
	}
}

// deliver writes the packets to the read queue in order to be consumed by the Read function.
func (c *srtConn) deliver(p packet.Packet) {
	// Non-blocking write to the read queue
	select {
	case <-c.ctx.Done():
	case c.readQueue <- p:
	default:
		c.log("connection:error", func() string { return "readQueue was blocking, dropping packet" })
	}
}
