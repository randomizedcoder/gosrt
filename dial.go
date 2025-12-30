package srt

import (
	"bytes"
	"context"
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

// Note: reader() and send() are in dial_io.go
// Note: handleHandshake(), sendInduction(), sendShutdown() are in dial_handshake.go
// Note: LocalAddr through log() are in dial_io.go
