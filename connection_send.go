package srt

import (
	"fmt"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// NAK CIF size constants for MSS overflow handling (FR-11)
const (
	// nakCIFMaxBytes is the maximum bytes available for NAK CIF payload
	// MSS (1500) - UDP header (28) - SRT header (16) = 1456 bytes
	nakCIFMaxBytes = MAX_MSS_SIZE - UDP_HEADER_SIZE - SRT_HEADER_SIZE

	// nakSingleEntryWireSize is the wire size of a single NAK entry (4 bytes)
	nakSingleEntryWireSize = 4

	// nakRangeEntryWireSize is the wire size of a range NAK entry (8 bytes: start + end)
	nakRangeEntryWireSize = 8
)

// splitNakList splits a NAK list into chunks that fit within MSS.
// Each chunk can be sent as a separate NAK packet.
// Returns a slice of NAK lists, each fitting within nakCIFMaxBytes.
func splitNakList(list []circular.Number, maxBytes int) [][]circular.Number {
	if len(list) == 0 {
		return nil
	}

	var chunks [][]circular.Number
	var currentChunk []circular.Number
	currentBytes := 0

	// Process pairs (start, end)
	for i := 0; i < len(list); i += 2 {
		if i+1 >= len(list) {
			break // Malformed list, ignore incomplete pair
		}

		start := list[i]
		end := list[i+1]

		// Calculate wire size for this entry
		var entrySize int
		if start.Val() == end.Val() {
			entrySize = nakSingleEntryWireSize // Single: 4 bytes
		} else {
			entrySize = nakRangeEntryWireSize // Range: 8 bytes
		}

		// Would this entry overflow the current chunk?
		if currentBytes+entrySize > maxBytes && len(currentChunk) > 0 {
			// Save current chunk and start a new one
			chunks = append(chunks, currentChunk)
			currentChunk = nil
			currentBytes = 0
		}

		// Add entry to current chunk
		currentChunk = append(currentChunk, start, end)
		currentBytes += entrySize
	}

	// Don't forget the last chunk
	if len(currentChunk) > 0 {
		chunks = append(chunks, currentChunk)
	}

	return chunks
}

// sendNAK sends NAK packet(s) to the peer with the given range of sequence numbers.
// If the list exceeds MSS, it will be split into multiple NAK packets (FR-11).
func (c *srtConn) sendNAK(list []circular.Number) {
	if len(list) == 0 {
		return
	}

	// Split the list into MSS-sized chunks
	chunks := splitNakList(list, nakCIFMaxBytes)

	// Track if we needed to split (for debugging/metrics)
	if len(chunks) > 1 {
		c.log("control:send:NAK:split", func() string {
			return fmt.Sprintf("NAK list split into %d packets (total %d entries)", len(chunks), len(list)/2)
		})
		// Increment split counter metric if available
		if c.metrics != nil {
			c.metrics.NakPacketsSplit.Add(uint64(len(chunks) - 1)) // Count extra packets needed
		}
	}

	// Send each chunk as a separate NAK packet
	for i, chunk := range chunks {
		p := packet.NewPacket(c.remoteAddr)

		p.Header().IsControlPacket = true
		p.Header().ControlType = packet.CTRLTYPE_NAK
		p.Header().Timestamp = c.getTimestampForPacket()

		cif := packet.CIFNAK{}
		cif.LostPacketSequenceNumber = append(cif.LostPacketSequenceNumber, chunk...)

		p.MarshalCIF(&cif)

		c.log("control:send:NAK:dump", func() string {
			if len(chunks) > 1 {
				return fmt.Sprintf("NAK packet %d/%d: %s", i+1, len(chunks), p.Dump())
			}
			return p.Dump()
		})
		c.log("control:send:NAK:cif", func() string { return cif.String() })

		// Note: NAK send metrics are tracked in the send path:
		// - io_uring path: connection_linux.go captures controlType before decommission
		// - non-io_uring path: listen.go/dial.go calls IncrementSendMetrics with valid packet

		c.pop(p)
	}
}

// sendACK sends an ACK to the peer with the given sequence number.
//
// RFC: https://datatracker.ietf.org/doc/html/draft-sharabayko-srt-01#section-3.2.4
//
// ACK Packet Types (Section 3.2.4):
//   - Full ACK: Sent every 10ms, includes RTT/RTTVar fields, triggers ACKACK for RTT calculation
//   - Light ACK: Sent more frequently at high data rates, includes only sequence number
//   - Small ACK: Includes fields up to Available Buffer Size (not commonly used)
//
// Last Acknowledged Packet Sequence Number (seq parameter): 32 bits.
// Contains the sequence number of the last data packet being acknowledged plus one.
// In other words, it is the sequence number of the first unacknowledged packet.
//
// TypeSpecific field:
//   - Full ACK: Contains ACK Number (monotonic counter), echoed in ACKACK for RTT calculation
//   - Light ACK: Set to 0, does NOT trigger ACKACK, does NOT contribute to RTT calculation
//
// The ACK Number (nextACKNumber) is a monotonically increasing 32-bit counter separate
// from packet sequence numbers. It's used solely for matching ACK→ACKACK pairs.
func (c *srtConn) sendACK(seq circular.Number, lite bool) {
	p := packet.NewPacket(c.remoteAddr)

	p.Header().IsControlPacket = true

	p.Header().ControlType = packet.CTRLTYPE_ACK
	p.Header().Timestamp = c.getTimestampForPacket()

	cif := packet.CIFACK{
		LastACKPacketSequenceNumber: seq,
	}

	if lite {
		// Light ACK: no lock needed (no btree access)
		cif.IsLite = true
		p.Header().TypeSpecific = 0
		c.metrics.PktSentACKLiteSuccess.Add(1) // Phase 5: Track Light ACK
	} else {
		// Full ACK: prepare data outside lock
		pps, bps, capacity := c.recv.PacketRate()

		cif.RTT = uint32(c.rtt.RTT())
		cif.RTTVar = uint32(c.rtt.RTTVar())
		cif.AvailableBufferSize = c.config.FC        // TODO: available buffer size (packets)
		cif.PacketsReceivingRate = uint32(pps)       // packets receiving rate (packets/s)
		cif.EstimatedLinkCapacity = uint32(capacity) // estimated link capacity (packets/s), not relevant for live mode
		cif.ReceivingRate = uint32(bps)              // receiving rate (bytes/s), not relevant for live mode

		// ACK-3: Use atomic CAS to get next ACK number (lock-free)
		ackNum := c.getNextACKNumber()
		p.Header().TypeSpecific = ackNum

		// ACK-5: Use btree + pool for efficient storage
		entry := GetAckEntry()
		entry.ackNum = ackNum
		entry.timestamp = time.Now()

		// ACK-8: Minimal lock scope - only for btree insert
		c.ackLock.Lock()
		if old := c.ackNumbers.Insert(entry); old != nil {
			PutAckEntry(old) // Return replaced entry to pool (shouldn't happen normally)
		}
		btreeLen := c.ackNumbers.Len()
		c.ackLock.Unlock()

		// Update ACK btree size metric (gauge)
		c.metrics.AckBtreeSize.Store(uint64(btreeLen))

		// DEBUG: Track Full ACK send timing
		c.log("control:send:ACK:fullack:debug", func() string {
			return fmt.Sprintf("Full ACK: ackNum=%d, timestamp=%s, btreeLen=%d, seq=%d",
				ackNum, entry.timestamp.Format("15:04:05.000000"), btreeLen, seq.Val())
		})

		c.metrics.PktSentACKFullSuccess.Add(1) // Phase 5: Track Full ACK
	}

	// ACK-8: MarshalCIF outside lock
	p.MarshalCIF(&cif)

	c.log("control:send:ACK:dump", func() string { return p.Dump() })
	c.log("control:send:ACK:cif", func() string { return cif.String() })

	// Note: ACK metrics are tracked via packet classifier in send path
	// No need to increment here - metrics already tracked

	c.pop(p)
}

// sendACKACK sends an ACKACK to the peer with the given ACK sequence.
func (c *srtConn) sendACKACK(ackSequence uint32) {
	p := packet.NewPacket(c.remoteAddr)

	p.Header().IsControlPacket = true

	p.Header().ControlType = packet.CTRLTYPE_ACKACK
	p.Header().Timestamp = c.getTimestampForPacket()

	p.Header().TypeSpecific = ackSequence

	c.log("control:send:ACKACK:dump", func() string { return p.Dump() })

	// Note: ACKACK metrics are tracked via packet classifier in send path
	// No need to increment here - metrics already tracked

	c.pop(p)
}

// sendShutdown sends a shutdown packet to the peer.
func (c *srtConn) sendShutdown() {
	p := packet.NewPacket(c.remoteAddr)

	p.Header().IsControlPacket = true

	p.Header().ControlType = packet.CTRLTYPE_SHUTDOWN
	p.Header().Timestamp = c.getTimestampForPacket()

	cif := packet.CIFShutdown{}

	p.MarshalCIF(&cif)

	c.log("control:send:shutdown:dump", func() string { return p.Dump() })
	c.log("control:send:shutdown:cif", func() string { return cif.String() })

	// Note: Shutdown metrics are tracked via packet classifier in send path
	// No need to increment here - metrics already tracked

	c.pop(p)
}

