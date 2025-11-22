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
	"syscall"
	"time"
	"unsafe"

	srtnet "github.com/datarhei/gosrt/net"
	"github.com/datarhei/gosrt/packet"
	"github.com/pawelgaczynski/giouring"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/sys/unix"
)

var (
	lC = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "listen",
			Name:      "counts",
			Help:      "gosrt listener counts",
		},
		[]string{"function", "variable", "type"},
	)

	lH = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Subsystem: "listen",
			Name:      "durations_seconds",
			Help:      "gosrt listener function durations in seconds",
			Objectives: map[float64]float64{
				0.1:  quantileError,
				0.5:  quantileError,
				0.99: quantileError,
			},
			MaxAge: summaryVecMaxAge,
		},
		[]string{"function", "variable", "type"},
	)

	// Listener channel blocking metrics
	listenerChannelBlockedDuration = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Subsystem: "listen",
			Name:      "channel_blocked_duration_seconds",
			Help:      "Duration that listener channels were blocked (when send was not immediate)",
			Objectives: map[float64]float64{
				0.1:  quantileError,
				0.5:  quantileError,
				0.99: quantileError,
			},
			MaxAge: summaryVecMaxAge,
		},
		[]string{"channel"},
	)

	listenerChannelBlockedCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "listen",
			Name:      "channel_blocked_total",
			Help:      "Total number of times listener channels were blocked",
		},
		[]string{"channel"},
	)
)

const (
	CHANNEL_SIZE_BACKLOG  = 128
	CHANNEL_SIZE_RCVQUEUE = 2048

	IOURING_RING_QUEUE_SIZE = 1024
)

// sendBufferPool is a sync.Pool for bytes.Buffer objects used in io_uring send operations
var sendBufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

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

// sendContext tracks resources for async send operations
type sendContext struct {
	packet packet.Packet // Only set for control packets (nil for data packets)
	buffer *bytes.Buffer // Buffer from payloadPool to return
}

// listener implements the Listener interface.
type listener struct {
	pc   *net.UDPConn
	fd   int // File descriptor for io_uring operations
	addr net.Addr

	config Config

	backlog  chan packet.Packet
	connReqs map[uint32]*connRequest
	// Map of socket IDs to connection objects, which is read heavy because all
	// packets are routed to the correct connection
	conns sync.Map     // key: uint32 (socketId), value: *srtConn
	lock  sync.RWMutex // protects connReqs and doneErr

	start time.Time

	rcvQueue chan packet.Packet

	// io_uring for async I/O (send path)
	ring            *giouring.Ring
	sendContexts    map[uint64]*sendContext
	sendContextLock sync.Mutex
	nextSendID      uint64         // atomic counter
	completionWg    sync.WaitGroup // WaitGroup to wait for completion handler to exit
	submitLock      sync.Mutex     // Protects GetSQE() and Submit() calls (GetSQE is not thread-safe)

	syncookie *srtnet.SYNCookie

	shutdown     bool
	shutdownLock sync.RWMutex
	shutdownOnce sync.Once

	stopReader context.CancelFunc

	doneChan chan struct{}
	doneErr  error
	doneOnce sync.Once
}

// Listen returns a new listener on the SRT protocol on the address with
// the provided config. The network parameter needs to be "srt".
//
// The address has the form "host:port".
//
// Examples:
//
//	Listen("srt", "127.0.0.1:3000", DefaultConfig())
//
// In case of an error, the returned Listener is nil and the error is non-nil.
func Listen(network, address string, config Config) (Listener, error) {

	startTime := time.Now()
	defer func() {
		lH.WithLabelValues("Listen", "duration", "complete").Observe(time.Since(startTime).Seconds())
	}()
	lC.WithLabelValues("Listen", "count", "start").Inc()

	if network != "srt" {
		return nil, fmt.Errorf("listen: the network must be 'srt'")
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("listen: invalid config: %w", err)
	}

	if config.Logger == nil {
		config.Logger = NewLogger(nil)
	}

	ln := &listener{
		config: config,
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

	// Get file descriptor for io_uring operations
	rawConn, err := pc.SyscallConn()
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("listen: failed to get syscall conn: %w", err)
	}
	var fdErr error
	err = rawConn.Control(func(fd uintptr) {
		ln.fd = int(fd)
	})
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("listen: failed to get file descriptor: %w", err)
	}
	if fdErr != nil {
		ln.Close()
		return nil, fmt.Errorf("listen: failed to get file descriptor: %w", fdErr)
	}

	ln.connReqs = make(map[uint32]*connRequest)
	// conns sync.Map zero value is ready to use, no initialization needed

	ln.backlog = make(chan packet.Packet, CHANNEL_SIZE_BACKLOG)

	ln.rcvQueue = make(chan packet.Packet, CHANNEL_SIZE_RCVQUEUE)

	// Initialize doneChan early so it's available for goroutines and error handling
	ln.doneChan = make(chan struct{})

	// Initialize io_uring ring for async send operations
	// Use larger ring size to handle high send rates without filling up
	// Ring size must be power of 2, and larger rings allow more in-flight operations
	ln.ring = giouring.NewRing()
	ringSize := uint32(4096) // Increased from 1024 to handle high send rates
	err = ln.ring.QueueInit(ringSize, 0)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("listen: failed to initialize io_uring: %w", err)
	}

	// Initialize send context tracking
	ln.sendContexts = make(map[uint64]*sendContext)
	ln.nextSendID = 0

	// Start send completion handler goroutine (doneChan must be initialized before this)
	ln.completionWg.Add(1)
	go func() {
		defer ln.completionWg.Done()
		ln.sendCompletionHandler()
	}()

	syncookie, err := srtnet.NewSYNCookie(ln.addr.String(), nil)
	if err != nil {
		ln.Close()
		return nil, err
	}
	ln.syncookie = syncookie

	ln.start = time.Now()

	var readerCtx context.Context
	readerCtx, ln.stopReader = context.WithCancel(context.Background())
	go ln.reader(readerCtx)

	go func() {
		buffer := make([]byte, config.MSS) // MTU size

		for {
			if ln.isShutdown() {
				ln.markDone(ErrListenerClosed)
				return
			}

			ln.pc.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, addr, err := ln.pc.ReadFrom(buffer)
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					continue
				}

				if ln.isShutdown() {
					ln.markDone(ErrListenerClosed)
					return
				}

				ln.markDone(err)
				return
			}

			p, err := packet.NewPacketFromData(addr, buffer[:n])
			if err != nil {
				lC.WithLabelValues("Listen", "packet_parse_error", "count").Inc()
				continue
			}

			// non-blocking
			select {
			case ln.rcvQueue <- p:
				lC.WithLabelValues("Listen", "packets_received", "count").Inc()
			default:
				// Blocked - measure blocking duration
				lC.WithLabelValues("Listen", "receive_queue_full", "count").Inc()
				ln.log("listen", func() string { return "receive queue is full" })
			}
		}
	}()

	lC.WithLabelValues("Listen", "listener_created", "count").Inc()
	return ln, nil
}

func (ln *listener) Accept2() (ConnRequest, error) {

	startTime := time.Now()
	defer func() {
		lH.WithLabelValues("Accept2", "duration", "complete").Observe(time.Since(startTime).Seconds())
	}()
	lC.WithLabelValues("Accept2", "count", "start").Inc()

	if ln.isShutdown() {
		return nil, ErrListenerClosed
	}

	for {
		select {
		case <-ln.doneChan:
			return nil, ln.error()

		case p := <-ln.backlog:
			req := newConnRequest(ln, p)
			if req == nil {
				lC.WithLabelValues("Accept2", "connection_request_failed", "count").Inc()
				break
			}

			lC.WithLabelValues("Accept2", "connection_request_accepted", "count").Inc()
			return req, nil
		}
	}
}

func (ln *listener) Accept(acceptFn AcceptFunc) (Conn, ConnType, error) {
	for {
		req, err := ln.Accept2()
		if err != nil {
			return nil, REJECT, err
		}

		if acceptFn == nil {
			req.Reject(REJ_PEER)
			continue
		}

		mode := acceptFn(req)
		if mode != PUBLISH && mode != SUBSCRIBE {
			// Figure out the reason
			reason := REJ_PEER
			if req.(*connRequest).rejectionReason > 0 {
				reason = req.(*connRequest).rejectionReason
			}
			req.Reject(reason)
			continue
		}

		conn, err := req.Accept()
		if err != nil {
			continue
		}

		return conn, mode, nil
	}
}

// markDone marks the listener as done by closing
// the done channel & sets the error
func (ln *listener) markDone(err error) {
	ln.doneOnce.Do(func() {
		ln.lock.Lock()
		defer ln.lock.Unlock()
		ln.doneErr = err
		// doneChan may be nil if markDone is called during initialization before doneChan is created
		if ln.doneChan != nil {
			close(ln.doneChan)
		}
	})
}

// error returns the error that caused the listener to be done
// if it's nil then the listener is not done
func (ln *listener) error() error {
	ln.lock.Lock()
	defer ln.lock.Unlock()
	return ln.doneErr
}

func (ln *listener) handleShutdown(socketId uint32) {
	ln.conns.Delete(socketId)
}

func (ln *listener) isShutdown() bool {
	ln.shutdownLock.RLock()
	defer ln.shutdownLock.RUnlock()

	return ln.shutdown
}

func (ln *listener) Close() {
	ln.shutdownOnce.Do(func() {
		ln.shutdownLock.Lock()
		ln.shutdown = true
		ln.shutdownLock.Unlock()

		ln.conns.Range(func(key, value interface{}) bool {
			conn := value.(*srtConn)
			if conn == nil {
				return true // continue iteration
			}
			conn.close()
			return true // continue iteration
		})

		// Stop reader if it was started (may be nil if Close() is called during initialization)
		if ln.stopReader != nil {
			ln.stopReader()
		}

		// Signal completion handler to exit by closing doneChan
		// The completion handler will drain pending completions before exiting
		// doneChan is now initialized early, but check for nil for safety
		if ln.doneChan != nil {
			ln.markDone(ErrListenerClosed)
		}

		// Wait for completion handler to exit before calling QueueExit()
		// This prevents race conditions where QueueExit() is called while
		// the completion handler is still accessing the ring
		ln.completionWg.Wait()

		// Only call QueueExit() if ring was successfully initialized
		// (ring might be nil if QueueInit failed during initialization)
		if ln.ring != nil {
			ln.ring.QueueExit()
		}

		ln.log("listen", func() string { return "closing socket" })

		ln.pc.Close()
	})
}

func (ln *listener) Addr() net.Addr {
	addrString := "0.0.0.0:0"
	if ln.addr != nil {
		addrString = ln.addr.String()
	}

	addr, _ := net.ResolveUDPAddr("udp", addrString)
	return addr
}

func (ln *listener) reader(ctx context.Context) {
	defer func() {
		ln.log("listen", func() string { return "left reader loop" })
	}()

	ln.log("listen", func() string { return "reader loop started" })

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-ln.rcvQueue:
			if ln.isShutdown() {
				break
			}

			ln.log("packet:recv:dump", func() string { return p.Dump() })

			if p.Header().DestinationSocketId == 0 {
				if p.Header().IsControlPacket && p.Header().ControlType == packet.CTRLTYPE_HANDSHAKE {
					// non-blocking
					select {
					case ln.backlog <- p:
						lC.WithLabelValues("reader", "handshake_packets", "count").Inc()
					default:
						// Blocked - measure blocking duration
						lC.WithLabelValues("reader", "backlog_full", "count").Inc()
						ln.log("handshake:recv:error", func() string { return "backlog is full" })
					}
				}
				break
			}

			val, ok := ln.conns.Load(p.Header().DestinationSocketId)
			var conn *srtConn
			if ok {
				conn = val.(*srtConn)
			}

			if !ok || conn == nil {
				// ignore the packet, we don't know the destination
				lC.WithLabelValues("reader", "packets_unknown_destination", "count").Inc()
				break
			}

			if !ln.config.AllowPeerIpChange {
				if p.Header().Addr.String() != conn.RemoteAddr().String() {
					// ignore the packet, it's not from the expected peer
					// https://haivision.github.io/srt-rfc/draft-sharabayko-srt.html#name-security-considerations
					lC.WithLabelValues("reader", "packets_peer_ip_mismatch", "count").Inc()
					break
				}
			}

			conn.push(p)
			lC.WithLabelValues("reader", "packets_routed", "count").Inc()
		}
	}
}

// Send a packet to the wire using io_uring async I/O.
// This function returns immediately after submitting to io_uring.
// Packet decommissioning happens in the completion handler.
func (ln *listener) send(p packet.Packet) {
	startTime := time.Now()
	defer func() {
		lH.WithLabelValues("send", "duration", "complete").Observe(time.Since(startTime).Seconds())
	}()

	// Get buffer from sendBufferPool (no mutex needed - each send gets its own buffer)
	sendBuffer := sendBufferPool.Get().(*bytes.Buffer)
	sendBuffer.Reset()

	// Marshal packet into buffer
	if err := p.Marshal(sendBuffer); err != nil {
		sendBufferPool.Put(sendBuffer)
		p.Decommission()
		ln.log("packet:send:error", func() string { return "marshalling packet failed" })
		lC.WithLabelValues("send", "marshal_error", "count").Inc()
		return
	}

	// Get the underlying slice - valid as long as buffer isn't modified
	bufferSlice := sendBuffer.Bytes()

	ln.log("packet:send:dump", func() string { return p.Dump() })

	// Prepare sendmsg structures
	var iovec syscall.Iovec
	iovec.Base = &bufferSlice[0]
	iovec.SetLen(len(bufferSlice))

	var msg syscall.Msghdr
	addrPtr, addrLen, err := sockaddrToPtr(p.Header().Addr)
	if err != nil {
		sendBufferPool.Put(sendBuffer)
		ln.log("packet:send:error", func() string { return fmt.Sprintf("address conversion failed: %v", err) })
		lC.WithLabelValues("send", "addr_error", "count").Inc()
		return
	}
	msg.Name = (*byte)(addrPtr)
	msg.Namelen = addrLen
	msg.Iov = &iovec
	msg.Iovlen = 1

	// Get unique ID for this send operation
	sendID := atomic.AddUint64(&ln.nextSendID, 1)

	// Create context - only store packet if it's a control packet
	// Data packets don't need decommissioning here (handled by congestion control)
	// This allows GC to free data packets sooner
	ctx := &sendContext{
		buffer: sendBuffer, // Always need buffer to return to pool
		packet: nil,        // Will be set only for control packets
	}

	// Only store packet pointer if it's a control packet (needs decommissioning)
	if p.Header().IsControlPacket {
		ctx.packet = p
	}
	// For data packets, ctx.packet remains nil, allowing GC to free the packet

	// Add context to map (with lock)
	ln.sendContextLock.Lock()
	ln.sendContexts[sendID] = ctx
	ln.sendContextLock.Unlock()

	// Get SQE from ring
	// Check space first - if GetSQE() returns nil but SQSpaceLeft() shows space,
	// we may need to submit/flush pending entries first
	if ln.ring.SQSpaceLeft() == 0 {
		// No space available - try to wait for completions
		spaceAvailable := false
		for i := 0; i < 10; i++ {
			time.Sleep(100 * time.Microsecond) // Wait 100us
			if ln.ring.SQSpaceLeft() > 0 {
				spaceAvailable = true
				break
			}
		}

		if !spaceAvailable {
			// Still full after waiting - remove context and return buffer
			ln.sendContextLock.Lock()
			delete(ln.sendContexts, sendID)
			ln.sendContextLock.Unlock()
			sendBufferPool.Put(sendBuffer)
			ln.log("packet:send:error", func() string {
				return fmt.Sprintf("io_uring ring full (space left: %d, ready: %d)",
					ln.ring.SQSpaceLeft(), ln.ring.SQReady())
			})
			lC.WithLabelValues("send", "ring_full", "count").Inc()
			return
		}
	}

	// Get SQE and submit - must be protected by mutex because GetSQE() is not thread-safe
	// GetSQE() modifies sq.sqeTail without atomic operations, so concurrent calls cause corruption
	ln.submitLock.Lock()
	sqe := ln.ring.GetSQE()
	if sqe == nil {
		// GetSQE returned nil but we have space - try submitting to flush pending entries
		// This can happen if we've prepared SQEs but haven't submitted them yet
		_, _ = ln.ring.Submit() // Submit any pending entries
		sqe = ln.ring.GetSQE()  // Try again

		if sqe == nil {
			// Still nil - remove context and return buffer
			ln.submitLock.Unlock()
			ln.sendContextLock.Lock()
			delete(ln.sendContexts, sendID)
			ln.sendContextLock.Unlock()
			sendBufferPool.Put(sendBuffer)
			ln.log("packet:send:error", func() string {
				return fmt.Sprintf("io_uring GetSQE failed (space left: %d, ready: %d)",
					ln.ring.SQSpaceLeft(), ln.ring.SQReady())
			})
			lC.WithLabelValues("send", "ring_full", "count").Inc()
			return
		}
	}

	// Prepare sendmsg operation
	sqe.PrepareSendMsg(ln.fd, &msg, 0)
	sqe.SetData64(sendID)

	// Submit to io_uring (non-blocking)
	// Note: We keep the lock during Submit() to ensure atomicity of GetSQE+Submit
	_, err = ln.ring.Submit()
	ln.submitLock.Unlock() // Always unlock, even on error

	if err != nil {
		// Submission failed - remove context and return buffer
		ln.sendContextLock.Lock()
		delete(ln.sendContexts, sendID)
		ln.sendContextLock.Unlock()
		sendBufferPool.Put(sendBuffer)
		ln.log("packet:send:error", func() string { return fmt.Sprintf("io_uring submit failed: %v", err) })
		lC.WithLabelValues("send", "submit_error", "count").Inc()
		return
	}

	// Function returns immediately - completion will be handled asynchronously
	packetType := "data"
	if p.Header().IsControlPacket {
		packetType = "control"
	}
	lC.WithLabelValues("send", "packets_sent", packetType).Inc()
}

func (ln *listener) log(topic string, message func() string) {
	if ln.config.Logger == nil {
		return
	}

	ln.config.Logger.Print(topic, 0, 2, message)
}

// sockaddrToPtr converts net.Addr to syscall sockaddr pointer
// Constructs raw sockaddr structures manually since sockaddr() method is unexported
func sockaddrToPtr(addr net.Addr) (unsafe.Pointer, uint32, error) {
	switch a := addr.(type) {
	case *net.UDPAddr:
		if a.IP.To4() != nil {
			// IPv4
			var sa unix.RawSockaddrInet4
			sa.Family = unix.AF_INET
			copy(sa.Addr[:], a.IP.To4())
			// Port in network byte order (big-endian)
			p := (*[2]byte)(unsafe.Pointer(&sa.Port))
			p[0] = byte(a.Port >> 8)
			p[1] = byte(a.Port)
			return unsafe.Pointer(&sa), unix.SizeofSockaddrInet4, nil
		} else {
			// IPv6
			var sa unix.RawSockaddrInet6
			sa.Family = unix.AF_INET6
			copy(sa.Addr[:], a.IP)
			// Port in network byte order (big-endian)
			p := (*[2]byte)(unsafe.Pointer(&sa.Port))
			p[0] = byte(a.Port >> 8)
			p[1] = byte(a.Port)
			// Note: ZoneId handling for IPv6 link-local addresses
			// If needed, can be extracted from a.Zone using net.InterfaceByName
			return unsafe.Pointer(&sa), unix.SizeofSockaddrInet6, nil
		}
	default:
		return nil, 0, fmt.Errorf("unsupported address type: %T", addr)
	}
}

// sendCompletionHandler processes io_uring send completions
// This is a named function (not anonymous) for clarity
func (ln *listener) sendCompletionHandler() {
	defer func() {
		ln.log("listen", func() string { return "left send completion handler loop" })
	}()

	for {
		// Check for shutdown before blocking on WaitCQE
		// Non-blocking select allows graceful shutdown
		select {
		case <-ln.doneChan:
			// Shutdown requested - drain any pending completions and exit
			ln.drainPendingCompletions()
			return
		default:
			// Continue to wait for completions
		}

		// Check if ring is still valid (might be nil if initialization failed)
		if ln.ring == nil {
			return
		}

		cqe, err := ln.ring.WaitCQE()
		if err != nil {
			// Check if we're shutting down (WaitCQE might return error on shutdown)
			if ln.isShutdown() {
				return
			}
			continue
		}

		// Get the unique ID from completion
		sendID := cqe.UserData

		// Look up context in map (with lock)
		ln.sendContextLock.Lock()
		ctx, exists := ln.sendContexts[sendID]
		if !exists {
			// Context not found (shouldn't happen, but handle gracefully)
			ln.sendContextLock.Unlock()
			if ln.ring != nil {
				ln.ring.CQESeen(cqe)
			}
			continue
		}

		// Remove from map immediately (we have the context now)
		delete(ln.sendContexts, sendID)
		ln.sendContextLock.Unlock()

		// Now we can safely use the context
		buffer := ctx.buffer
		bufferLen := len(buffer.Bytes()) // Get buffer length for size check

		// Check send result first (before returning buffer)
		// cqe.Res contains the number of bytes sent on success, or negative errno on error
		if cqe.Res < 0 {
			// Send error - convert negative errno to error
			errno := syscall.Errno(-cqe.Res)
			ln.log("packet:send:error", func() string {
				return fmt.Sprintf("io_uring send error for ID %d: %v (errno: %d)", sendID, errno, cqe.Res)
			})
			lC.WithLabelValues("send", "io_uring_error", "count").Inc()

			// For critical errors, log more details
			if errno == syscall.EBADF {
				ln.log("packet:send:error", func() string {
					return fmt.Sprintf("io_uring send EBADF - file descriptor %d may be invalid", ln.fd)
				})
			} else if errno == syscall.EMSGSIZE {
				// Message too long - packet exceeds MTU or socket buffer size
				ln.log("packet:send:error", func() string {
					return fmt.Sprintf("io_uring send EMSGSIZE - packet size %d bytes exceeds MTU (MSS: %d)",
						bufferLen, ln.config.MSS)
				})
			}
		} else if int(cqe.Res) != bufferLen {
			// Partial send - this shouldn't happen with UDP, but log it
			ln.log("packet:send:warning", func() string {
				return fmt.Sprintf("io_uring partial send: sent %d of %d bytes", cqe.Res, bufferLen)
			})
		}

		// Return buffer to sendBufferPool
		// Safe to do now - kernel has finished reading the data (confirmed by completion)
		sendBufferPool.Put(buffer)

		// Decommission control packets if present
		// Data packets have ctx.packet == nil, so nothing to do
		if ctx.packet != nil {
			ctx.packet.Decommission()
		}

		// Mark completion as seen (ring might have been closed, so check)
		if ln.ring != nil {
			ln.ring.CQESeen(cqe)
		}
	}
}

// drainPendingCompletions processes any remaining completions during shutdown
func (ln *listener) drainPendingCompletions() {
	// Check if ring is still valid (might be nil if initialization failed)
	if ln.ring == nil {
		return
	}

	// Process any remaining completions in the queue
	// Use PeekCQE to check if there are completions (non-blocking),
	// then WaitCQE to actually consume them
	for {
		// Peek to check if there are completions (non-blocking)
		peekCQE, err := ln.ring.PeekCQE()
		if err != nil || peekCQE == nil {
			// No more completions in queue
			break
		}

		// Actually wait for and consume the completion
		// This will return immediately since we know there's one available
		cqe, err := ln.ring.WaitCQE()
		if err != nil {
			// Error or no more completions (ring might have been closed)
			break
		}

		// Process the completion
		sendID := cqe.UserData

		ln.sendContextLock.Lock()
		ctx, exists := ln.sendContexts[sendID]
		if exists {
			delete(ln.sendContexts, sendID)
		}
		ln.sendContextLock.Unlock()

		if exists {
			// Clean up context
			sendBufferPool.Put(ctx.buffer)
			if ctx.packet != nil {
				ctx.packet.Decommission()
			}
		}

		// Mark completion as seen (ring might have been closed, so check)
		if ln.ring != nil {
			ln.ring.CQESeen(cqe)
		}
	}
}
