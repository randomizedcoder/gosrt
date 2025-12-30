package srt

import (
	"context"
	"fmt"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// handleHSRequest handles the HSv4 handshake extension request and sends the response
func (c *srtConn) handleHSRequest(p packet.Packet) {
	c.log("control:recv:HSReq:dump", func() string { return p.Dump() })

	cif := &packet.CIFHandshakeExtension{}

	if err := p.UnmarshalCIF(cif); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:HSReq:error", func() string { return fmt.Sprintf("invalid HSReq: %s", err) })
		return
	}

	c.log("control:recv:HSReq:cif", func() string { return cif.String() })

	// Check for version
	if cif.SRTVersion < 0x010200 || cif.SRTVersion >= 0x010300 {
		c.log("control:recv:HSReq:error", func() string { return fmt.Sprintf("unsupported version: %#08x", cif.SRTVersion) })
		c.log("connection:close:reason", func() string {
			return fmt.Sprintf("handshake error: unsupported SRT version %#08x", cif.SRTVersion)
		})
		c.close(metrics.CloseReasonError)
		return
	}

	// Check the required SRT flags
	if !cif.SRTFlags.TSBPDSND {
		c.log("control:recv:HSRes:error", func() string { return "TSBPDSND flag must be set" })
		c.log("connection:close:reason", func() string {
			return "handshake error: missing required flag TSBPDSND"
		})
		c.close(metrics.CloseReasonError)

		return
	}

	if !cif.SRTFlags.TLPKTDROP {
		c.log("control:recv:HSRes:error", func() string { return "TLPKTDROP flag must be set" })
		c.log("connection:close:reason", func() string {
			return "handshake error: missing required flag TLPKTDROP"
		})
		c.close(metrics.CloseReasonError)

		return
	}

	if !cif.SRTFlags.CRYPT {
		c.log("control:recv:HSRes:error", func() string { return "CRYPT flag must be set" })
		c.log("connection:close:reason", func() string {
			return "handshake error: missing required flag CRYPT"
		})
		c.close(metrics.CloseReasonError)

		return
	}

	if !cif.SRTFlags.REXMITFLG {
		c.log("control:recv:HSRes:error", func() string { return "REXMITFLG flag must be set" })
		c.log("connection:close:reason", func() string {
			return "handshake error: missing required flag REXMITFLG"
		})
		c.close(metrics.CloseReasonError)

		return
	}

	// we as receiver don't need this
	cif.SRTFlags.TSBPDSND = false

	// we as receiver are supporting these
	cif.SRTFlags.TSBPDRCV = true
	cif.SRTFlags.PERIODICNAK = true

	// These flag was introduced in HSv5 and should not be set in HSv4
	if cif.SRTFlags.STREAM {
		c.log("control:recv:HSReq:error", func() string { return "STREAM flag is set" })
		c.log("connection:close:reason", func() string {
			return "handshake error: invalid flag STREAM (HSv4 only, flag is HSv5 only)"
		})
		c.close(metrics.CloseReasonError)
		return
	}

	if cif.SRTFlags.PACKET_FILTER {
		c.log("control:recv:HSReq:error", func() string { return "PACKET_FILTER flag is set" })
		c.log("connection:close:reason", func() string {
			return "handshake error: invalid flag PACKET_FILTER (HSv4 only, flag is HSv5 only)"
		})
		c.close(metrics.CloseReasonError)
		return
	}

	recvTsbpdDelay := uint16(c.config.ReceiverLatency.Milliseconds())

	if cif.SendTSBPDDelay > recvTsbpdDelay {
		recvTsbpdDelay = cif.SendTSBPDDelay
	}

	c.tsbpdDelay = uint64(recvTsbpdDelay) * 1000

	cif.RecvTSBPDDelay = 0
	cif.SendTSBPDDelay = recvTsbpdDelay

	p.MarshalCIF(cif)

	// Send HS Response
	p.Header().SubType = packet.EXTTYPE_HSRSP

	c.pop(p)
}

// handleHSResponse handles the HSv4 handshake extension response
func (c *srtConn) handleHSResponse(p packet.Packet) {
	c.log("control:recv:HSRes:dump", func() string { return p.Dump() })

	cif := &packet.CIFHandshakeExtension{}

	if err := p.UnmarshalCIF(cif); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:HSRes:error", func() string { return fmt.Sprintf("invalid HSRes: %s", err) })
		return
	}

	c.log("control:recv:HSRes:cif", func() string { return cif.String() })

	if c.version == 4 {
		// Check for version
		if cif.SRTVersion < 0x010200 || cif.SRTVersion >= 0x010300 {
			c.log("control:recv:HSRes:error", func() string { return fmt.Sprintf("unsupported version: %#08x", cif.SRTVersion) })
			c.log("connection:close:reason", func() string {
				return fmt.Sprintf("handshake error: unsupported SRT version %#08x", cif.SRTVersion)
			})
			c.close(metrics.CloseReasonError)
			return
		}

		// TSBPDSND is not relevant from the receiver
		// PERIODICNAK is the sender's decision, we don't care, but will handle them

		// Check the required SRT flags
		if !cif.SRTFlags.TSBPDRCV {
			c.log("control:recv:HSRes:error", func() string { return "TSBPDRCV flag must be set" })
			c.log("connection:close:reason", func() string {
				return "handshake error: missing required flag TSBPDRCV"
			})
			c.close(metrics.CloseReasonError)

			return
		}

		if !cif.SRTFlags.TLPKTDROP {
			c.log("control:recv:HSRes:error", func() string { return "TLPKTDROP flag must be set" })
			c.log("connection:close:reason", func() string {
				return "handshake error: missing required flag TLPKTDROP"
			})
			c.close(metrics.CloseReasonError)

			return
		}

		if !cif.SRTFlags.CRYPT {
			c.log("control:recv:HSRes:error", func() string { return "CRYPT flag must be set" })
			c.log("connection:close:reason", func() string {
				return "handshake error: missing required flag CRYPT"
			})
			c.close(metrics.CloseReasonError)

			return
		}

		if !cif.SRTFlags.REXMITFLG {
			c.log("control:recv:HSRes:error", func() string { return "REXMITFLG flag must be set" })
			c.log("connection:close:reason", func() string {
				return "handshake error: missing required flag REXMITFLG"
			})
			c.close(metrics.CloseReasonError)

			return
		}

		// These flag was introduced in HSv5 and should not be set in HSv4
		if cif.SRTFlags.STREAM {
			c.log("control:recv:HSReq:error", func() string { return "STREAM flag is set" })
			c.log("connection:close:reason", func() string {
				return "handshake error: invalid flag STREAM (HSv4 only, flag is HSv5 only)"
			})
			c.close(metrics.CloseReasonError)
			return
		}

		if cif.SRTFlags.PACKET_FILTER {
			c.log("control:recv:HSReq:error", func() string { return "PACKET_FILTER flag is set" })
			c.log("connection:close:reason", func() string {
				return "handshake error: invalid flag PACKET_FILTER (HSv4 only, flag is HSv5 only)"
			})
			c.close(metrics.CloseReasonError)
			return
		}

		sendTsbpdDelay := uint16(c.config.PeerLatency.Milliseconds())

		if cif.SendTSBPDDelay > sendTsbpdDelay {
			sendTsbpdDelay = cif.SendTSBPDDelay
		}

		c.dropThreshold = uint64(float64(sendTsbpdDelay)*1.25) + uint64(c.config.SendDropDelay.Microseconds())
		if c.dropThreshold < uint64(time.Second.Microseconds()) {
			c.dropThreshold = uint64(time.Second.Microseconds())
		}
		c.dropThreshold += 20_000

		c.snd.SetDropThreshold(c.dropThreshold)

		c.stopHSRequests()
	}
}

func (c *srtConn) sendHSRequests(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	select {
	case <-ctx.Done():
		return
	case <-ticker.C:
		c.sendHSRequest()
	}
}

func (c *srtConn) sendHSRequest() {
	cif := &packet.CIFHandshakeExtension{
		SRTVersion: 0x00010203,
		SRTFlags: packet.CIFHandshakeExtensionFlags{
			TSBPDSND:      true,  // we send in TSBPD mode
			TSBPDRCV:      false, // not relevant for us as sender
			CRYPT:         true,  // must be always set
			TLPKTDROP:     true,  // must be set in live mode
			PERIODICNAK:   false, // not relevant for us as sender
			REXMITFLG:     true,  // must alwasy be set
			STREAM:        false, // has been introducet in HSv5
			PACKET_FILTER: false, // has been introducet in HSv5
		},
		RecvTSBPDDelay: 0,
		SendTSBPDDelay: uint16(c.config.ReceiverLatency.Milliseconds()),
	}

	p := packet.NewPacket(c.remoteAddr)

	p.Header().IsControlPacket = true

	p.Header().ControlType = packet.CTRLTYPE_USER
	p.Header().SubType = packet.EXTTYPE_HSREQ
	p.Header().Timestamp = c.getTimestampForPacket()

	p.MarshalCIF(cif)

	c.log("control:send:HSReq:dump", func() string { return p.Dump() })
	c.log("control:send:HSReq:cif", func() string { return cif.String() })

	c.pop(p)
}

