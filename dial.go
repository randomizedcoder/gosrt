package srt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/crypto"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/randomizedcoder/gosrt/rand"
)

// ErrClientClosed is returned when the client connection has
// been voluntarily closed.
var ErrClientClosed = errors.New("srt: client closed")

// dialer implements the Conn interface
type dialer struct {
	version uint32

	pc *net.UDPConn

	localAddr  net.Addr
	remoteAddr net.Addr

	config Config

	socketId                    uint32
	initialPacketSequenceNumber circular.Number

	crypto crypto.Crypto

	conn     *srtConn
	connLock sync.RWMutex
	connChan chan connResponse

	start time.Time

	rcvQueue chan packet.Packet // for packets that come from the wire

	// sndMutex is only used as fallback when io_uring is disabled or unavailable.
	// When io_uring is enabled, each connection has its own send path without mutex.
	sndMutex sync.Mutex
	sndData  bytes.Buffer // for packets that go to the wire

	shutdown     bool
	shutdownLock sync.RWMutex
	shutdownOnce sync.Once

	stopReader context.CancelFunc

	doneChan chan error

	// Context and waitgroup for graceful shutdown
	ctx        context.Context // Context for dialer (inherited from client)
	shutdownWg *sync.WaitGroup // Root waitgroup (from client)
	connWg     sync.WaitGroup  // Waitgroup for connection

	// io_uring receive path (Linux only)
	recvRing        interface{}                    // *giouring.Ring on Linux, nil on others
	recvRingFd      int                            // UDP socket file descriptor
	recvCompletions map[uint64]*recvCompletionInfo // Maps request ID to completion info
	recvCompLock    sync.Mutex                     // Protects recvCompletions map
	recvRequestID   atomic.Uint64                  // Atomic counter for generating unique request IDs
	recvCompCtx     context.Context                // Context for completion handler
	recvCompCancel  context.CancelFunc             // Cancel function for completion handler
	recvCompWg      sync.WaitGroup                 // WaitGroup for completion handler goroutine
}

type connResponse struct {
	conn *srtConn
	err  error
}

// Dial connects to the address using the SRT protocol with the given config
// and returns a Conn interface.
//
// The address is of the form "host:port".
//
// Example:
//
//	Dial(ctx, "srt", "127.0.0.1:3000", DefaultConfig(), shutdownWg)
//
// In case of an error the returned Conn is nil and the error is non-nil.
func Dial(ctx context.Context, network, address string, config Config, shutdownWg *sync.WaitGroup) (Conn, error) {
	if network != "srt" {
		return nil, fmt.Errorf("the network must be 'srt'")
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	if config.Logger == nil {
		config.Logger = NewLogger(nil)
	}

	// Increment waitgroup early, before any code that might call Close()
	// This ensures shutdownWg.Done() in Close() won't cause a negative counter
	if shutdownWg != nil {
		shutdownWg.Add(1)
	}

	dl := &dialer{
		config:     config,
		ctx:        ctx,
		shutdownWg: shutdownWg,
	}

	netdialer := net.Dialer{
		Control: DialControl(config),
	}

	// Set local address if specified (for binding to a specific interface/IP)
	if config.LocalAddr != "" {
		localAddr := config.LocalAddr
		// Add port :0 if not specified (use ephemeral port)
		if !strings.Contains(localAddr, ":") {
			localAddr = localAddr + ":0"
		}
		laddr, err := net.ResolveUDPAddr("udp", localAddr)
		if err != nil {
			if shutdownWg != nil {
				shutdownWg.Done() // Balance the Add(1) at start
			}
			return nil, fmt.Errorf("invalid local address %q: %w", config.LocalAddr, err)
		}
		netdialer.LocalAddr = laddr
	}

	conn, err := netdialer.Dial("udp", address)
	if err != nil {
		return nil, fmt.Errorf("failed dialing: %w", err)
	}

	pc, ok := conn.(*net.UDPConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("failed dialing: connection is not a UDP connection")
	}

	dl.pc = pc

	dl.localAddr = pc.LocalAddr()
	dl.remoteAddr = pc.RemoteAddr()

	dl.conn = nil
	dl.connChan = make(chan connResponse)

	// Determine receive queue buffer size (default: 2048 if not configured)
	rcvQueueSize := config.ReceiveQueueSize
	if rcvQueueSize <= 0 {
		rcvQueueSize = 2048
	}
	dl.rcvQueue = make(chan packet.Packet, rcvQueueSize)

	dl.doneChan = make(chan error)

	dl.start = time.Now()

	// Phase 2: Zero-copy uses the shared globalRecvBufferPool (see buffers.go)
	// This single pool is shared across ALL listeners and dialers for maximum reuse

	// Initialize io_uring receive ring (if enabled)
	// This is a no-op on non-Linux platforms
	ioUringInitialized := false
	if err := dl.initializeIoUringRecv(); err != nil {
		// Log error but don't fail - fall back to ReadFrom()
		// Error is already logged in initializeIoUringRecv() (if logging available)
	} else {
		// Check if io_uring was actually initialized (enabled and successful)
		ioUringInitialized = (dl.recvRing != nil)
	}

	// create a new socket ID
	dl.socketId, err = rand.Uint32()
	if err != nil {
		dl.Close()
		return nil, err
	}

	seqNum, err := rand.Uint32()
	if err != nil {
		dl.Close()
		return nil, err
	}
	dl.initialPacketSequenceNumber = circular.New(seqNum&packet.MAX_SEQUENCENUMBER, packet.MAX_SEQUENCENUMBER)

	// Only start ReadFrom() goroutine if io_uring is NOT enabled or failed to initialize
	// When io_uring is enabled and initialized, the completion handler processes packets
	if !ioUringInitialized {
		go func() {
			defer func() {
				dl.log("dial", func() string { return "ReadFrom goroutine exited" })
			}()

			for {
				// Check for context cancellation first
				select {
				case <-dl.ctx.Done():
					dl.doneChan <- ErrClientClosed
					return
				default:
				}

				if dl.isShutdown() {
					dl.doneChan <- ErrClientClosed
					return
				}

				// Phase 2: Zero-copy - get buffer from shared global pool
				bufferPtr := GetRecvBufferPool().Get().(*[]byte)

				pc.SetReadDeadline(time.Now().Add(3 * time.Second))
				n, _, err := pc.ReadFrom(*bufferPtr)
				if err != nil {
					// Return buffer to pool on read error
					GetRecvBufferPool().Put(bufferPtr)

					if errors.Is(err, os.ErrDeadlineExceeded) {
						continue
					}

					// Check context again after read error
					select {
					case <-dl.ctx.Done():
						dl.doneChan <- ErrClientClosed
						return
					default:
					}

					if dl.isShutdown() {
						dl.doneChan <- ErrClientClosed
						return
					}

					dl.doneChan <- err
					return
				}

				// Phase 2: Zero-copy - unmarshal directly referencing the pooled buffer
				p := packet.NewPacket(dl.remoteAddr)
				if err := p.UnmarshalZeroCopy(bufferPtr, n, dl.remoteAddr); err != nil {
					// Parse error - return buffer to pool via DecommissionWithBuffer
					p.DecommissionWithBuffer(GetRecvBufferPool())
					continue
				}

				// NOTE: Buffer is NOT returned to pool here! It's referenced by the packet.
				// Buffer will be returned by receiver.releasePacketFully() after delivery.

				// non-blocking
				select {
				case <-dl.ctx.Done():
					// Context cancelled - return buffer and decommission packet
					p.DecommissionWithBuffer(GetRecvBufferPool())
					dl.doneChan <- ErrClientClosed
					return
				case dl.rcvQueue <- p:
					// Success - packet queued (metrics tracked in reader())
				default:
					dl.log("dial", func() string { return "receive queue is full" })
					// Queue full - return buffer and decommission packet
					p.DecommissionWithBuffer(GetRecvBufferPool())
				}
			}
		}()
	}

	// Start reader goroutine with dialer context
	// Note: reader will exit when dl.ctx is cancelled (via client context cancellation)
	go dl.reader(dl.ctx)

	// Send the initial handshake request
	dl.sendInduction()

	dl.log("dial", func() string { return "waiting for response" })

	// Create handshake timeout context (wraps dialer context)
	handshakeCtx, handshakeCancel := context.WithTimeout(dl.ctx, dl.config.HandshakeTimeout)
	defer handshakeCancel()

	// Start goroutine to handle handshake timeout
	go func() {
		select {
		case <-handshakeCtx.Done():
			if handshakeCtx.Err() == context.DeadlineExceeded {
				dl.log("connection:close:reason", func() string {
					return fmt.Sprintf("handshake timeout: server didn't respond within %s", dl.config.HandshakeTimeout)
				})
				dl.connChan <- connResponse{
					conn: nil,
					err:  fmt.Errorf("handshake timeout: server didn't respond within %s", dl.config.HandshakeTimeout),
				}
			}
		case <-dl.ctx.Done():
			// Dialer context cancelled - don't send error, let main flow handle it
			return
		}
	}()

	// Wait for handshake to conclude or timeout
	select {
	case response := <-dl.connChan:
		if response.err != nil {
			handshakeCancel() // Cancel timeout context
			dl.Close()
			return nil, response.err
		}
		handshakeCancel() // Cancel timeout context (handshake completed)
		dl.connLock.Lock()
		dl.conn = response.conn
		dl.connLock.Unlock()
		return dl, nil
	case <-handshakeCtx.Done():
		// Timeout occurred - error already sent to connChan by goroutine above
		// Wait for the error response
		response := <-dl.connChan
		dl.Close()
		return nil, response.err
	case <-dl.ctx.Done():
		// Dialer context cancelled
		handshakeCancel()
		dl.Close()
		return nil, dl.ctx.Err()
	}
}

func (dl *dialer) checkConnection() error {
	select {
	case err := <-dl.doneChan:
		dl.Close()
		return err
	default:
	}

	return nil
}

// reader reads packets from the receive queue and pushes them into the connection
func (dl *dialer) reader(ctx context.Context) {
	defer func() {
		dl.log("dial", func() string { return "left reader loop" })
	}()

	dl.log("dial", func() string { return "reader loop started" })

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-dl.rcvQueue:
			if dl.isShutdown() {
				break
			}

			dl.log("packet:recv:dump", func() string { return p.Dump() })

			if p.Header().DestinationSocketId != dl.socketId {
				break
			}

			if p.Header().IsControlPacket && p.Header().ControlType == packet.CTRLTYPE_HANDSHAKE {
				dl.handleHandshake(p)
				break
			}

			dl.connLock.RLock()
			if dl.conn == nil {
				dl.connLock.RUnlock()
				// Note: Can't track metrics here - no connection yet
				break
			}
			conn := dl.conn
			dl.connLock.RUnlock()

			// Track successful receive (ReadFrom path)
			if conn.metrics != nil {
				metrics.IncrementRecvMetrics(conn.metrics, p, false, true, 0)
			}

			conn.push(p)
		}
	}
}

// Send a packet to the wire. This function must be synchronous in order to allow to safely call Packet.Decommission() afterward.
// NOTE: This is a fallback method used only when io_uring is disabled or unavailable.
// When io_uring is enabled, connections use their own per-connection send() method.
func (dl *dialer) send(p packet.Packet) {
	dl.sndMutex.Lock()
	defer dl.sndMutex.Unlock()

	dl.sndData.Reset()

	if err := p.Marshal(&dl.sndData); err != nil {
		p.Decommission()
		dl.log("packet:send:error", func() string { return "marshalling packet failed" })
		// Try to find connection for metrics tracking
		dl.connLock.RLock()
		conn := dl.conn
		dl.connLock.RUnlock()
		if conn != nil && conn.metrics != nil {
			metrics.IncrementSendMetrics(conn.metrics, p, false, false, metrics.DropReasonMarshal)
		}
		return
	}

	buffer := dl.sndData.Bytes()

	dl.log("packet:send:dump", func() string { return p.Dump() })

	// Write the packet's contents to the wire
	_, writeErr := dl.pc.Write(buffer)
	if writeErr != nil {
		dl.log("packet:send:error", func() string { return fmt.Sprintf("failed to write packet to network: %v", writeErr) })
		// Try to find connection for metrics tracking
		dl.connLock.RLock()
		conn := dl.conn
		dl.connLock.RUnlock()
		if conn != nil && conn.metrics != nil {
			metrics.IncrementSendMetrics(conn.metrics, p, false, false, metrics.DropReasonWrite)
		}
	} else {
		// Success - try to find connection for metrics tracking
		dl.connLock.RLock()
		conn := dl.conn
		dl.connLock.RUnlock()
		if conn != nil && conn.metrics != nil {
			metrics.IncrementSendMetrics(conn.metrics, p, false, true, 0)
		}
	}

	if p.Header().IsControlPacket {
		// Control packets can be decommissioned because they will not be sent again (data packets might be retransferred)
		p.Decommission()
	}
}

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
			metrics:                     connMetrics,        // Pre-created - no race!
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

func (dl *dialer) LocalAddr() net.Addr {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return nil
	}

	return dl.conn.LocalAddr()
}

func (dl *dialer) RemoteAddr() net.Addr {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return nil
	}

	return dl.conn.RemoteAddr()
}

func (dl *dialer) SocketId() uint32 {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0
	}

	return dl.conn.SocketId()
}

func (dl *dialer) PeerSocketId() uint32 {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0
	}

	return dl.conn.PeerSocketId()
}

func (dl *dialer) StreamId() string {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return ""
	}

	return dl.conn.StreamId()
}

func (dl *dialer) Version() uint32 {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0
	}

	return dl.conn.Version()
}

func (dl *dialer) isShutdown() bool {
	dl.shutdownLock.RLock()
	defer dl.shutdownLock.RUnlock()

	return dl.shutdown
}

func (dl *dialer) Close() error {
	dl.shutdownOnce.Do(func() {
		dl.shutdownLock.Lock()
		dl.shutdown = true
		dl.shutdownLock.Unlock()

		// Note: All goroutines will exit when dl.ctx is cancelled (via client context cancellation)
		// No need to call stopReader() - it was a no-op anyway since we use dl.ctx directly

		dl.connLock.RLock()
		if dl.conn != nil {
			dl.conn.Close() // Connection will call connWg.Done() when done (Phase 5)
		}
		dl.connLock.RUnlock()

		// Wait for connection to shutdown
		done := make(chan struct{})
		go func() {
			dl.connWg.Wait()
			close(done)
		}()

		// Use config shutdown delay as timeout, or default to 5 seconds
		timeout := 5 * time.Second
		if dl.config.ShutdownDelay > 0 {
			timeout = dl.config.ShutdownDelay
		}

		select {
		case <-done:
			// Connection closed
		case <-time.After(timeout):
			// Timeout - log warning but continue
			// Note: In production, we might want to log this
		}

		// Wait for receive completion handler (io_uring)
		done = make(chan struct{})
		go func() {
			dl.recvCompWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Receive handler exited
		case <-time.After(timeout):
			// Timeout - log warning but continue
		}

		// Cleanup io_uring receive ring (if initialized)
		dl.cleanupIoUringRecv()

		dl.log("dial", func() string { return "closing socket" })
		dl.pc.Close()

		select {
		case <-dl.doneChan:
		default:
		}

		// Notify root waitgroup
		if dl.shutdownWg != nil {
			dl.shutdownWg.Done()
		}
	})

	return nil
}

func (dl *dialer) Read(p []byte) (n int, err error) {
	if err := dl.checkConnection(); err != nil {
		return 0, err
	}

	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0, fmt.Errorf("no connection")
	}

	return dl.conn.Read(p)
}

func (dl *dialer) ReadPacket() (packet.Packet, error) {
	if err := dl.checkConnection(); err != nil {
		return nil, err
	}

	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return nil, fmt.Errorf("no connection")
	}

	return dl.conn.ReadPacket()
}

func (dl *dialer) Write(p []byte) (n int, err error) {
	if err := dl.checkConnection(); err != nil {
		return 0, err
	}

	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0, fmt.Errorf("no connection")
	}

	return dl.conn.Write(p)
}

func (dl *dialer) WritePacket(p packet.Packet) error {
	if err := dl.checkConnection(); err != nil {
		return err
	}

	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return fmt.Errorf("no connection")
	}

	return dl.conn.WritePacket(p)
}

func (dl *dialer) SetDeadline(t time.Time) error      { return dl.conn.SetDeadline(t) }
func (dl *dialer) SetReadDeadline(t time.Time) error  { return dl.conn.SetReadDeadline(t) }
func (dl *dialer) SetWriteDeadline(t time.Time) error { return dl.conn.SetWriteDeadline(t) }

func (dl *dialer) Stats(s *Statistics) {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return
	}

	dl.conn.Stats(s)
}

func (dl *dialer) GetExtendedStatistics() *ExtendedStatistics {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return nil
	}

	return dl.conn.GetExtendedStatistics()
}

func (dl *dialer) GetPeerIdleTimeoutRemaining() time.Duration {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0
	}

	return dl.conn.GetPeerIdleTimeoutRemaining()
}

func (dl *dialer) log(topic string, message func() string) {
	if dl.config.Logger == nil {
		return
	}

	dl.config.Logger.Print(topic, dl.socketId, 2, message)
}
