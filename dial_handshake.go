package srt

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/crypto"
	"github.com/randomizedcoder/gosrt/packet"
)

func (dl *dialer) handleHandshake(p packet.Packet) {
	cif := &packet.CIFHandshake{}

	err := p.UnmarshalCIF(cif)

	dl.log("handshake:recv:dump", func() string { return p.Dump() })
	dl.log("handshake:recv:cif", func() string { return cif.String() })

	if err != nil {
		dl.log("handshake:recv:error", func() string { return err.Error() })
		return
	}

	// assemble the response (4.3.1.  Caller-Listener Handshake)

	p.Header().ControlType = packet.CTRLTYPE_HANDSHAKE
	p.Header().SubType = 0
	p.Header().TypeSpecific = 0
	p.Header().Timestamp = uint32(time.Since(dl.start).Microseconds())
	p.Header().DestinationSocketId = 0 // must be 0 for handshake

	switch cif.HandshakeType {
	case packet.HSTYPE_INDUCTION:
		if cif.Version < 4 || cif.Version > 5 {
			dl.connChan <- connResponse{
				conn: nil,
				err:  fmt.Errorf("peer responded with unsupported handshake version (%d)", cif.Version),
			}

			return
		}

		cif.IsRequest = true
		cif.HandshakeType = packet.HSTYPE_CONCLUSION
		cif.InitialPacketSequenceNumber = dl.initialPacketSequenceNumber
		cif.MaxTransmissionUnitSize = dl.config.MSS // MTU size
		cif.MaxFlowWindowSize = dl.config.FC
		cif.SRTSocketId = dl.socketId
		cif.PeerIP.FromNetAddr(dl.localAddr)

		// Setup crypto context
		if len(dl.config.Passphrase) != 0 {
			keylen := dl.config.PBKeylen

			// If the server advertises a specific block cipher family and key size,
			// use this one, otherwise, use the configured one
			if cif.EncryptionField != 0 {
				switch cif.EncryptionField {
				case 2:
					keylen = 16
				case 3:
					keylen = 24
				case 4:
					keylen = 32
				}
			}

			cr, err := crypto.New(keylen)
			if err != nil {
				dl.connChan <- connResponse{
					conn: nil,
					err:  fmt.Errorf("failed creating crypto context: %w", err),
				}
			}

			dl.crypto = cr
		}

		// Verify version
		if cif.Version == 5 {
			dl.version = 5

			// Verify magic number
			if cif.ExtensionField != 0x4A17 {
				dl.connChan <- connResponse{
					conn: nil,
					err:  fmt.Errorf("peer sent the wrong magic number"),
				}

				return
			}

			cif.HasHS = true
			cif.SRTHS = &packet.CIFHandshakeExtension{
				SRTVersion: SRT_VERSION,
				SRTFlags: packet.CIFHandshakeExtensionFlags{
					TSBPDSND:      true,
					TSBPDRCV:      true,
					CRYPT:         true, // must always set to true
					TLPKTDROP:     true,
					PERIODICNAK:   true,
					REXMITFLG:     true,
					STREAM:        false,
					PACKET_FILTER: false,
				},
				RecvTSBPDDelay: uint16(dl.config.ReceiverLatency.Milliseconds()),
				SendTSBPDDelay: uint16(dl.config.PeerLatency.Milliseconds()),
			}

			cif.HasSID = true
			cif.StreamId = dl.config.StreamId

			if dl.crypto != nil {
				cif.HasKM = true
				cif.SRTKM = &packet.CIFKeyMaterialExtension{}

				if err := dl.crypto.MarshalKM(cif.SRTKM, dl.config.Passphrase, packet.EvenKeyEncrypted); err != nil {
					dl.connChan <- connResponse{
						conn: nil,
						err:  err,
					}

					return
				}
			}
		} else {
			dl.version = 4

			cif.EncryptionField = 0
			cif.ExtensionField = 2

			cif.HasHS = false
			cif.HasKM = false
			cif.HasSID = false
		}

		p.MarshalCIF(cif)

		dl.log("handshake:send:dump", func() string { return p.Dump() })
		dl.log("handshake:send:cif", func() string { return cif.String() })

		dl.send(p)
	case packet.HSTYPE_CONCLUSION:
		if cif.Version < 4 || cif.Version > 5 {
			dl.connChan <- connResponse{
				conn: nil,
				err:  fmt.Errorf("peer responded with unsupported handshake version (%d)", cif.Version),
			}

			return
		}

		recvTsbpdDelay := uint16(dl.config.ReceiverLatency.Milliseconds())
		sendTsbpdDelay := uint16(dl.config.PeerLatency.Milliseconds())

		if cif.Version == 5 {
			if cif.SRTHS == nil {
				dl.connChan <- connResponse{
					conn: nil,
					err:  fmt.Errorf("missing handshake extension"),
				}
				return
			}

			// Check if the peer version is sufficient
			if cif.SRTHS.SRTVersion < dl.config.MinVersion {
				dl.sendShutdown(cif.SRTSocketId)

				dl.connChan <- connResponse{
					conn: nil,
					err:  fmt.Errorf("peer SRT version is not sufficient"),
				}

				return
			}

			// Check the required SRT flags
			if !cif.SRTHS.SRTFlags.TSBPDSND || !cif.SRTHS.SRTFlags.TSBPDRCV ||
				!cif.SRTHS.SRTFlags.TLPKTDROP || !cif.SRTHS.SRTFlags.PERIODICNAK || !cif.SRTHS.SRTFlags.REXMITFLG {

				dl.sendShutdown(cif.SRTSocketId)

				dl.connChan <- connResponse{
					conn: nil,
					err:  fmt.Errorf("peer doesn't agree on SRT flags"),
				}

				return
			}

			// We only support live streaming
			if cif.SRTHS.SRTFlags.STREAM {
				dl.sendShutdown(cif.SRTSocketId)

				dl.connChan <- connResponse{
					conn: nil,
					err:  fmt.Errorf("peer doesn't support live streaming"),
				}

				return
			}

			// Select the largest TSBPD delay advertised by the listener, but at least 120ms
			if cif.SRTHS.SendTSBPDDelay > recvTsbpdDelay {
				recvTsbpdDelay = cif.SRTHS.SendTSBPDDelay
			}

			if cif.SRTHS.RecvTSBPDDelay > sendTsbpdDelay {
				sendTsbpdDelay = cif.SRTHS.RecvTSBPDDelay
			}
		}

		// If the peer has a smaller MTU size, adjust to it
		if cif.MaxTransmissionUnitSize < dl.config.MSS {
			dl.config.MSS = cif.MaxTransmissionUnitSize
			dl.config.PayloadSize = dl.config.MSS - SRT_HEADER_SIZE - UDP_HEADER_SIZE

			if dl.config.PayloadSize < MIN_PAYLOAD_SIZE {
				dl.sendShutdown(cif.SRTSocketId)

				dl.connChan <- connResponse{
					conn: nil,
					err: fmt.Errorf("effective MSS too small (%d bytes) to fit the minimal payload size (%d bytes)",
						dl.config.MSS, MIN_PAYLOAD_SIZE),
				}

				return
			}
		}

		// Extract socket FD for io_uring (if enabled)
		var socketFd int
		if dl.config.IoUringEnabled {
			var err error
			socketFd, err = getUDPConnFD(dl.pc)
			if err != nil {
				dl.log("connection:io_uring:error", func() string {
					return fmt.Sprintf("failed to extract socket FD: %v", err)
				})
				// Continue without io_uring - will fall back to regular sends
			}
		}

		// Create metrics FIRST - this allows building onSend closure before connection creation,
		// eliminating the initialization race condition.
		connMetrics := createConnectionMetrics(dl.localAddr, dl.socketId, dl.config.InstanceName)

		// Create a new connection with fully initialized onSend and metrics
		dl.connWg.Add(1) // Increment waitgroup before creating connection
		conn := newSRTConn(srtConnConfig{
			version:                     cif.Version,
			isCaller:                    true,
			localAddr:                   dl.localAddr,
			remoteAddr:                  dl.remoteAddr,
			config:                      dl.config,
			start:                       dl.start,
			socketId:                    dl.socketId,
			peerSocketId:                cif.SRTSocketId,
			tsbpdTimeBase:               uint64(time.Since(dl.start).Microseconds()),
			tsbpdDelay:                  uint64(recvTsbpdDelay) * 1000,
			peerTsbpdDelay:              uint64(sendTsbpdDelay) * 1000,
			initialPacketSequenceNumber: cif.InitialPacketSequenceNumber,
			crypto:                      dl.crypto,
			keyBaseEncryption:           packet.EvenKeyEncrypted,
			onSend:                      dl.send,              // Fallback if io_uring disabled
			sendFilter:                  dl.config.SendFilter, // Optional test filter
			onShutdown:                  func(socketId uint32) { dl.Close() },
			logger:                      dl.config.Logger,
			socketFd:                    socketFd,
			parentCtx:                   dl.ctx,
			parentWg:                    &dl.connWg,
			metrics:                     connMetrics,         // Pre-created - no race!
			recvBufferPool:              GetRecvBufferPool(), // Phase 2: shared global pool
		})

		dl.log("connection:new", func() string { return fmt.Sprintf("%#08x (%s)", conn.SocketId(), conn.StreamId()) })

		dl.connChan <- connResponse{
			conn: conn,
			err:  nil,
		}
	default:
		var err error
		var reason string

		if cif.HandshakeType.IsRejection() {
			reason = fmt.Sprintf("connection rejected: %s", cif.HandshakeType.String())
			err = fmt.Errorf("connection rejected: %s", cif.HandshakeType.String())
		} else {
			reason = fmt.Sprintf("unsupported handshake: %s", cif.HandshakeType.String())
			err = fmt.Errorf("unsupported handshake: %s", cif.HandshakeType.String())
		}

		dl.log("connection:close:reason", func() string { return reason })
		dl.connChan <- connResponse{
			conn: nil,
			err:  err,
		}
	}
}

func (dl *dialer) sendInduction() {
	p := packet.NewPacket(dl.remoteAddr)

	p.Header().IsControlPacket = true

	p.Header().ControlType = packet.CTRLTYPE_HANDSHAKE
	p.Header().SubType = 0
	p.Header().TypeSpecific = 0

	p.Header().Timestamp = uint32(time.Since(dl.start).Microseconds())
	p.Header().DestinationSocketId = 0

	cif := &packet.CIFHandshake{
		IsRequest:                   true,
		Version:                     4,
		EncryptionField:             0,
		ExtensionField:              2,
		InitialPacketSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
		MaxTransmissionUnitSize:     dl.config.MSS, // MTU size
		MaxFlowWindowSize:           dl.config.FC,
		HandshakeType:               packet.HSTYPE_INDUCTION,
		SRTSocketId:                 dl.socketId,
		SynCookie:                   0,
	}

	cif.PeerIP.FromNetAddr(dl.localAddr)

	p.MarshalCIF(cif)

	dl.log("handshake:send:dump", func() string { return p.Dump() })
	dl.log("handshake:send:cif", func() string { return cif.String() })

	dl.send(p)
}

func (dl *dialer) sendShutdown(peerSocketId uint32) {
	p := packet.NewPacket(dl.remoteAddr)

	data := [4]byte{}
	binary.BigEndian.PutUint32(data[0:], 0)

	p.SetData(data[0:4])

	p.Header().IsControlPacket = true

	p.Header().ControlType = packet.CTRLTYPE_SHUTDOWN
	p.Header().TypeSpecific = 0

	p.Header().Timestamp = uint32(time.Since(dl.start).Microseconds())
	p.Header().DestinationSocketId = peerSocketId

	dl.log("control:send:shutdown:dump", func() string { return p.Dump() })

	dl.send(p)
}

