package srt

import (
	"context"
	"fmt"
	"time"

	"github.com/randomizedcoder/gosrt/crypto"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// handleKMRequest checks if the key material is valid and responds with a KM response.
func (c *srtConn) handleKMRequest(p packet.Packet) {
	c.log("control:recv:KMReq:dump", func() string { return p.Dump() })

	// Note: KM metrics are tracked via packet classifier in recv path
	// No need to increment here - metrics already tracked

	cif := &packet.CIFKeyMaterialExtension{}

	if err := p.UnmarshalCIF(cif); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:KMReq:error", func() string { return fmt.Sprintf("invalid KMReq: %s", err) })
		return
	}

	c.log("control:recv:KMReq:cif", func() string { return cif.String() })

	c.cryptoLock.Lock()

	if c.version == 4 && c.crypto == nil {
		cr, err := crypto.New(int(cif.KLen))
		if err != nil {
			c.log("control:recv:KMReq:error", func() string { return fmt.Sprintf("crypto: %s", err) })
			c.log("connection:close:reason", func() string {
				return fmt.Sprintf("encryption error: failed to initialize crypto: %s", err)
			})
			c.cryptoLock.Unlock()
			c.close(metrics.CloseReasonError)
			return
		}

		c.keyBaseEncryption = cif.KeyBasedEncryption.Opposite()
		c.crypto = cr
	}

	if c.crypto == nil {
		c.log("control:recv:KMReq:error", func() string { return "connection is not encrypted" })
		c.cryptoLock.Unlock()
		return
	}

	if cif.KeyBasedEncryption == c.keyBaseEncryption {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:KMReq:error", func() string {
			return "invalid KM request. wants to reset the key that is already in use"
		})
		c.cryptoLock.Unlock()
		return
	}

	if err := c.crypto.UnmarshalKM(cif, c.config.Passphrase); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:KMReq:error", func() string { return fmt.Sprintf("invalid KMReq: %s", err) })
		c.cryptoLock.Unlock()
		return
	}

	// Switch the keys
	c.keyBaseEncryption = c.keyBaseEncryption.Opposite()

	c.cryptoLock.Unlock()

	// Send KM Response
	p.Header().SubType = packet.EXTTYPE_KMRSP

	// Note: KM metrics are tracked via packet classifier in send path
	// No need to increment here - metrics already tracked

	c.pop(p)
}

// handleKMResponse confirms the change of encryption keys.
func (c *srtConn) handleKMResponse(p packet.Packet) {
	c.log("control:recv:KMRes:dump", func() string { return p.Dump() })

	// Note: KM metrics are tracked via packet classifier in recv path
	// No need to increment here - metrics already tracked

	cif := &packet.CIFKeyMaterialExtension{}

	if err := p.UnmarshalCIF(cif); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:KMRes:error", func() string { return fmt.Sprintf("invalid KMRes: %s", err) })
		return
	}

	c.cryptoLock.Lock()
	defer c.cryptoLock.Unlock()

	if c.crypto == nil {
		c.log("control:recv:KMRes:error", func() string { return "connection is not encrypted" })
		return
	}

	if c.version == 4 {
		c.stopKMRequests()

		if cif.Error != 0 {
			var reason string
			switch cif.Error {
			case packet.KM_NOSECRET:
				c.log("control:recv:KMRes:error", func() string { return "peer didn't enabled encryption" })
				reason = "encryption error: peer didn't enable encryption"
			case packet.KM_BADSECRET:
				c.log("control:recv:KMRes:error", func() string { return "peer has a different passphrase" })
				reason = "encryption error: peer has a different passphrase"
			default:
				reason = fmt.Sprintf("encryption error: key material error code %d", cif.Error)
			}
			c.log("connection:close:reason", func() string { return reason })
			c.close(metrics.CloseReasonError)
			return
		}
	}

	c.log("control:recv:KMRes:cif", func() string { return cif.String() })

	if c.kmPreAnnounceCountdown >= c.config.KMPreAnnounce {
		c.log("control:recv:KMRes:error", func() string { return "not in pre-announce period, ignored" })
		// Ignore the response, we're not in the pre-announce period
		return
	}

	c.kmConfirmed = true
}

func (c *srtConn) sendKMRequests(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	select {
	case <-ctx.Done():
		return
	case <-ticker.C:
		c.sendKMRequest(c.keyBaseEncryption)
	}
}

// sendKMRequest sends a KM request to the peer.
func (c *srtConn) sendKMRequest(key packet.PacketEncryption) {
	if c.crypto == nil {
		c.log("control:send:KMReq:error", func() string { return "connection is not encrypted" })
		return
	}

	cif := &packet.CIFKeyMaterialExtension{}

	if err := c.crypto.MarshalKM(cif, c.config.Passphrase, key); err != nil {
		c.log("control:send:KMReq:error", func() string {
			return fmt.Sprintf("failed to marshal key material: %v", err)
		})
		// Track error in metrics if available
		if c.metrics != nil {
			c.metrics.CryptoErrorMarshalKM.Add(1)
		}
		return
	}

	p := packet.NewPacket(c.remoteAddr)

	p.Header().IsControlPacket = true

	p.Header().ControlType = packet.CTRLTYPE_USER
	p.Header().SubType = packet.EXTTYPE_KMREQ
	p.Header().Timestamp = c.getTimestampForPacket()

	p.MarshalCIF(cif)

	c.log("control:send:KMReq:dump", func() string { return p.Dump() })
	c.log("control:send:KMReq:cif", func() string { return cif.String() })

	// Note: KM metrics are tracked via packet classifier in send path
	// No need to increment here - metrics already tracked

	c.pop(p)
}

