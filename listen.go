package srt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	srtnet "github.com/randomizedcoder/gosrt/net"
	"github.com/randomizedcoder/gosrt/packet"
)

// ConnType represents the kind of connection as returned
// from the AcceptFunc. It is one of REJECT, PUBLISH, or SUBSCRIBE.
type ConnType int

// String returns a string representation of the ConnType.
func (c ConnType) String() string {
	switch c {
	case REJECT:
		return "REJECT"
	case PUBLISH:
		return "PUBLISH"
	case SUBSCRIBE:
		return "SUBSCRIBE"
	default:
		return ""
	}
}

const (
	REJECT    ConnType = ConnType(1 << iota) // Reject a connection
	PUBLISH                                  // This connection is meant to write data to the server
	SUBSCRIBE                                // This connection is meant to read data from a PUBLISHed stream
)

// RejectionReason are the rejection reasons that can be returned from the AcceptFunc in order to send
// another reason than the default one (REJ_PEER) to the client.
type RejectionReason uint32

// Table 7: Handshake Rejection Reason Codes
const (
	REJ_UNKNOWN    RejectionReason = 1000 // unknown reason
	REJ_SYSTEM     RejectionReason = 1001 // system function error
	REJ_PEER       RejectionReason = 1002 // rejected by peer
	REJ_RESOURCE   RejectionReason = 1003 // resource allocation problem
	REJ_ROGUE      RejectionReason = 1004 // incorrect data in handshake
	REJ_BACKLOG    RejectionReason = 1005 // listener's backlog exceeded
	REJ_IPE        RejectionReason = 1006 // internal program error
	REJ_CLOSE      RejectionReason = 1007 // socket is closing
	REJ_VERSION    RejectionReason = 1008 // peer is older version than agent's min
	REJ_RDVCOOKIE  RejectionReason = 1009 // rendezvous cookie collision
	REJ_BADSECRET  RejectionReason = 1010 // wrong password
	REJ_UNSECURE   RejectionReason = 1011 // password required or unexpected
	REJ_MESSAGEAPI RejectionReason = 1012 // stream flag collision
	REJ_CONGESTION RejectionReason = 1013 // incompatible congestion-controller type
	REJ_FILTER     RejectionReason = 1014 // incompatible packet filter
	REJ_GROUP      RejectionReason = 1015 // incompatible group
)

// These are the extended rejection reasons that may be less well supported
// Codes & their meanings taken from https://github.com/Haivision/srt/blob/f477af533562505abf5295f059cf2156b17be740/srtcore/access_control.h
const (
	REJX_BAD_REQUEST   RejectionReason = 1400 // General syntax error in the SocketID specification (also a fallback code for undefined cases)
	REJX_UNAUTHORIZED  RejectionReason = 1401 // Authentication failed, provided that the user was correctly identified and access to the required resource would be granted
	REJX_OVERLOAD      RejectionReason = 1402 // The server is too heavily loaded, or you have exceeded credits for accessing the service and the resource.
	REJX_FORBIDDEN     RejectionReason = 1403 // Access denied to the resource by any kind of reason.
	REJX_NOTFOUND      RejectionReason = 1404 // Resource not found at this time.
	REJX_BAD_MODE      RejectionReason = 1405 // The mode specified in `m` key in StreamID is not supported for this request.
	REJX_UNACCEPTABLE  RejectionReason = 1406 // The requested parameters specified in SocketID cannot be satisfied for the requested resource. Also when m=publish and the data format is not acceptable.
	REJX_CONFLICT      RejectionReason = 1407 // The resource being accessed is already locked for modification. This is in case of m=publish and the specified resource is currently read-only.
	REJX_NOTSUP_MEDIA  RejectionReason = 1415 // The media type is not supported by the application. This is the `t` key that specifies the media type as stream, file and auth, possibly extended by the application.
	REJX_LOCKED        RejectionReason = 1423 // The resource being accessed is locked for any access.
	REJX_FAILED_DEPEND RejectionReason = 1424 // The request failed because it specified a dependent session ID that has been disconnected.
	REJX_ISE           RejectionReason = 1500 // Unexpected internal server error
	REJX_UNIMPLEMENTED RejectionReason = 1501 // The request was recognized, but the current version doesn't support it.
	REJX_GW            RejectionReason = 1502 // The server acts as a gateway and the target endpoint rejected the connection.
	REJX_DOWN          RejectionReason = 1503 // The service has been temporarily taken over by a stub reporting this error. The real service can be down for maintenance or crashed.
	REJX_VERSION       RejectionReason = 1505 // SRT version not supported. This might be either unsupported backward compatibility, or an upper value of a version.
	REJX_NOROOM        RejectionReason = 1507 // The data stream cannot be archived due to lacking storage space. This is in case when the request type was to send a file or the live stream to be archived.
)

// ErrListenerClosed is returned when the listener is about to shutdown.
var ErrListenerClosed = errors.New("srt: listener closed")

// AcceptFunc receives a connection request and returns the type of connection
// and is required by the Listener for each Accept of a new connection.
type AcceptFunc func(req ConnRequest) ConnType

// Listener waits for new connections
type Listener interface {
	// Accept2 waits for new connections.
	// On closing the err will be ErrListenerClosed.
	Accept2() (ConnRequest, error)

	// Accept waits for new connections. For each new connection the AcceptFunc
	// gets called. Conn is a new connection if AcceptFunc is PUBLISH or SUBSCRIBE.
	// If AcceptFunc returns REJECT, Conn is nil. In case of failure error is not
	// nil, Conn is nil and ConnType is REJECT. On closing the listener err will
	// be ErrListenerClosed and ConnType is REJECT.
	//
	// Deprecated: replaced by Accept2().
	Accept(AcceptFunc) (Conn, ConnType, error)

	// Close closes the listener. It will stop accepting new connections and
	// close all currently established connections.
	Close()

	// Addr returns the address of the listener.
	Addr() net.Addr
}

// listener implements the Listener interface.
type listener struct {
	pc   *net.UDPConn
	addr net.Addr

	config Config

	backlog  chan packet.Packet
	connReqs map[uint32]*connRequest
	conns    sync.Map     // key: uint32 (socketId), value: *srtConn
	lock     sync.RWMutex // Used for doneErr and doneChan, not for conns

	start time.Time

	rcvQueue chan packet.Packet

	// sndMutex is only used as fallback when io_uring is disabled or unavailable.
	// When io_uring is enabled, each connection has its own send path without mutex.
	sndMutex sync.Mutex
	sndData  bytes.Buffer

	syncookie *srtnet.SYNCookie

	shutdown     bool
	shutdownLock sync.RWMutex
	shutdownOnce sync.Once

	stopReader context.CancelFunc

	doneChan chan struct{}
	doneErr  error
	doneOnce sync.Once

	// Context and waitgroup for graceful shutdown
	ctx        context.Context // Context for listener (inherited from server)
	shutdownWg *sync.WaitGroup // Server waitgroup (from Server)
	connWg     sync.WaitGroup  // Waitgroup for all connections

	// io_uring receive path (Linux only)
	// Multi-ring support: When IoUringRecvRingCount > 1, we create multiple
	// independent io_uring rings, each with its own completion handler goroutine.
	// This enables parallel completion processing across CPU cores.
	// See multi_iouring_design.md Section 4.1 for design rationale.

	// Legacy single-ring fields (used when IoUringRecvRingCount == 1)
	recvRing        interface{}                    // *giouring.Ring on Linux, nil on others
	recvRingFd      int                            // UDP socket file descriptor (shared)
	recvCompletions map[uint64]*recvCompletionInfo // Maps request ID to completion info
	recvCompLock    sync.Mutex                     // Protects recvCompletions map
	recvRequestID   atomic.Uint64                  // Atomic counter for generating unique request IDs

	// Multi-ring fields (used when IoUringRecvRingCount > 1)
	// Each recvRingState owns its ring, completion map, and ID counter (no cross-ring locking)
	recvRingStates []*recvRingState // Slice of independent ring states

	// Shared by both single-ring and multi-ring modes
	recvCompCtx    context.Context    // Context for completion handler(s)
	recvCompCancel context.CancelFunc // Cancel function for completion handler(s)
	recvCompWg     sync.WaitGroup     // WaitGroup for completion handler goroutine(s)
}

// Listen returns a new listener on the SRT protocol on the address with
// the provided config. The network parameter needs to be "srt".
//
// The address has the form "host:port".
//
// Examples:
//
//	Listen(ctx, "srt", "127.0.0.1:3000", DefaultConfig(), shutdownWg)
//
// In case of an error, the returned Listener is nil and the error is non-nil.
func Listen(ctx context.Context, network, address string, config Config, shutdownWg *sync.WaitGroup) (Listener, error) {
	if network != "srt" {
		return nil, fmt.Errorf("listen: the network must be 'srt'")
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("listen: invalid config: %w", err)
	}

	if config.Logger == nil {
		config.Logger = NewLogger(nil)
	}

	// Increment waitgroup early, before any code that might call Close()
	// This ensures shutdownWg.Done() in Close() won't cause a negative counter
	if shutdownWg != nil {
		shutdownWg.Add(1)
	}

	ln := &listener{
		config:     config,
		ctx:        ctx,
		shutdownWg: shutdownWg,
	}

	lc := net.ListenConfig{
		Control: ListenControl(config),
	}

	lp, err := lc.ListenPacket(context.Background(), "udp", address)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	pc := lp.(*net.UDPConn)

	ln.pc = pc
	ln.addr = pc.LocalAddr()
	if ln.addr == nil {
		return nil, fmt.Errorf("listen: no local address")
	}

	ln.connReqs = make(map[uint32]*connRequest)
	// ln.conns is sync.Map - zero value is ready to use, no initialization needed

	ln.backlog = make(chan packet.Packet, 128)

	// Determine receive queue buffer size (default: 2048 if not configured)
	rcvQueueSize := config.ReceiveQueueSize
	if rcvQueueSize <= 0 {
		rcvQueueSize = 2048
	}
	ln.rcvQueue = make(chan packet.Packet, rcvQueueSize)

	syncookie, err := srtnet.NewSYNCookie(ln.addr.String(), nil)
	if err != nil {
		ln.Close()
		return nil, err
	}
	ln.syncookie = syncookie

	ln.doneChan = make(chan struct{})

	ln.start = time.Now()

	// Phase 2: Zero-copy uses the shared globalRecvBufferPool (see buffers.go)
	// This single pool is shared across ALL listeners and dialers for maximum reuse

	// Initialize io_uring receive ring (if enabled)
	// This is a no-op on non-Linux platforms
	ioUringInitialized := false
	if err := ln.initializeIoUringRecv(); err != nil {
		// Log error but don't fail - fall back to ReadFrom()
		// Error is already logged in initializeIoUringRecv()
	} else {
		// Check if io_uring was actually initialized (enabled and successful)
		// Single-ring mode uses ln.recvRing, multi-ring mode uses ln.recvRingStates
		ioUringInitialized = (ln.recvRing != nil) || (len(ln.recvRingStates) > 0)
	}

	// Start reader goroutine with listener context
	// Note: reader will exit when ln.ctx is cancelled (via server context cancellation)
	go ln.reader(ln.ctx)

	// Only start ReadFrom() goroutine if io_uring is NOT enabled or failed to initialize
	// When io_uring is enabled and initialized, the completion handler processes packets
	if !ioUringInitialized {
		go func() {
			defer func() {
				ln.log("listen", func() string { return "ReadFrom goroutine exited" })
			}()

			for {
				// Check for context cancellation first
				select {
				case <-ln.ctx.Done():
					ln.markDone(ErrListenerClosed)
					return
				default:
				}

				if ln.isShutdown() {
					ln.markDone(ErrListenerClosed)
					return
				}

				// Phase 2: Zero-copy - get buffer from shared global pool
				bufferPtr := GetRecvBufferPool().Get().(*[]byte)

				ln.pc.SetReadDeadline(time.Now().Add(3 * time.Second))
				n, addr, err := ln.pc.ReadFrom(*bufferPtr)
				if err != nil {
					// Return buffer to pool on read error
					GetRecvBufferPool().Put(bufferPtr)

					if errors.Is(err, os.ErrDeadlineExceeded) {
						continue
					}

					// Check context again after read error
					select {
					case <-ln.ctx.Done():
						ln.markDone(ErrListenerClosed)
						return
					default:
					}

					if ln.isShutdown() {
						ln.markDone(ErrListenerClosed)
						return
					}

					ln.markDone(err)
					return
				}

				// Phase 2: Zero-copy - unmarshal directly referencing the pooled buffer
				p := packet.NewPacket(addr)
				if err := p.UnmarshalZeroCopy(bufferPtr, n, addr); err != nil {
					// Parse error - return buffer to pool via DecommissionWithBuffer
					p.DecommissionWithBuffer(GetRecvBufferPool())
					continue
				}

				// NOTE: Buffer is NOT returned to pool here! It's referenced by the packet.
				// Buffer will be returned by receiver.releasePacketFully() after delivery.

				// non-blocking
				select {
				case <-ln.ctx.Done():
					// Context cancelled - return buffer and decommission packet
					p.DecommissionWithBuffer(GetRecvBufferPool())
					ln.markDone(ErrListenerClosed)
					return
				case ln.rcvQueue <- p:
					// Success - packet queued (metrics tracked in reader())
				default:
					ln.log("listen", func() string { return "receive queue is full" })
					// Queue full - return buffer and decommission packet
					p.DecommissionWithBuffer(GetRecvBufferPool())
				}
			}
		}()
	}

	return ln, nil
}

// Note: Accept2() and Accept() are in listen_accept.go
// Note: markDone, error, handleShutdown, getConnections, isShutdown, Close, Addr are in listen_lifecycle.go
// Note: reader, send, sendWithMetrics, sendBrokenLookup, log are in listen_io.go
