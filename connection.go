package srt

import (
	"bytes"
	"context"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/congestion/live"
	"github.com/randomizedcoder/gosrt/congestion/live/receive"
	"github.com/randomizedcoder/gosrt/crypto"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// controlPacketHandler is the function signature for control packet handlers
type controlPacketHandler func(c *srtConn, p packet.Packet)

// userPacketHandler is the function signature for CTRLTYPE_USER SubType handlers
type userPacketHandler func(c *srtConn, p packet.Packet)

// Conn is a SRT network connection.
type Conn interface {
	// Read reads data from the connection.
	// Read can be made to time out and return an error after a fixed
	// time limit; see SetDeadline and SetReadDeadline.
	Read(p []byte) (int, error)

	// ReadPacket reads a packet from the queue of received packets. It blocks
	// if the queue is empty. Only data packets are returned. Using ReadPacket
	// and Read at the same time may lead to data loss.
	ReadPacket() (packet.Packet, error)

	// Write writes data to the connection.
	// Write can be made to time out and return an error after a fixed
	// time limit; see SetDeadline and SetWriteDeadline.
	Write(p []byte) (int, error)

	// WritePacket writes a packet to the write queue. Packets on the write queue
	// will be sent to the peer of the connection. Only data packets will be sent.
	WritePacket(p packet.Packet) error

	// Close closes the connection.
	// Any blocked Read or Write operations will be unblocked and return errors.
	Close() error

	// LocalAddr returns the local network address. The returned net.Addr is not shared by other invocations of LocalAddr.
	LocalAddr() net.Addr

	// RemoteAddr returns the remote network address. The returned net.Addr is not shared by other invocations of RemoteAddr.
	RemoteAddr() net.Addr

	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error

	// SocketId return the socketid of the connection.
	SocketId() uint32

	// PeerSocketId returns the socketid of the peer of the connection.
	PeerSocketId() uint32

	// StreamId returns the streamid use for the connection.
	StreamId() string

	// Stats returns accumulated and instantaneous statistics of the connection.
	Stats(s *Statistics)

	// Version returns the connection version, either 4 or 5. With version 4, the streamid is not available
	Version() uint32

	// GetExtendedStatistics returns extended statistics that are not part of the standard SRT Statistics struct.
	// This includes ACKACK packet counts and retransmissions triggered by NAKs.
	// Returns nil if extended statistics are not available.
	GetExtendedStatistics() *ExtendedStatistics

	// GetPeerIdleTimeoutRemaining returns the remaining time until the peer idle timeout fires.
	// Returns 0 if the timer is not active or has already fired.
	GetPeerIdleTimeoutRemaining() time.Duration
}

// rtt is defined in connection_rtt.go

// connStats struct removed - all statistics now use atomic counters in metrics.ConnectionMetrics

// Check if we implement the net.Conn interface
var _ net.Conn = &srtConn{}

type srtConn struct {
	version  uint32
	isCaller bool // Only relevant if version == 4

	localAddr  net.Addr
	remoteAddr net.Addr

	start time.Time

	shutdownOnce sync.Once

	socketId     uint32
	peerSocketId uint32

	config Config

	crypto                 crypto.Crypto
	keyBaseEncryption      packet.PacketEncryption
	kmPreAnnounceCountdown uint64
	kmRefreshCountdown     uint64
	kmConfirmed            bool
	cryptoLock             sync.Mutex

	peerIdleTimeout          *time.Timer  // Timer for peer idle timeout (lock-free reset)
	peerIdleTimeoutLastReset atomic.Int64 // Track when the peer idle timeout was last reset (Unix nano timestamp, atomic)

	rtt rtt // microseconds

	ackLock       sync.RWMutex
	ackNumbers    *ackEntryBtree // ACK-5: btree for O(log n) lookup + efficient cleanup
	nextACKNumber atomic.Uint32  // ACK number counter, incremented atomically (ACK-3)

	initialPacketSequenceNumber circular.Number

	tsbpdTimeBase       uint64 // microseconds
	tsbpdWrapPeriod     bool
	tsbpdTimeBaseOffset uint64 // microseconds
	tsbpdDelay          uint64 // microseconds
	tsbpdDrift          uint64 // microseconds
	peerTsbpdDelay      uint64 // microseconds
	dropThreshold       uint64 // microseconds

	// Queue for packets that are coming from the network
	networkQueue chan packet.Packet

	// Per-connection mutex for handlePacket() serialization (used by io_uring direct routing)
	// Ensures sequential processing per connection (same guarantee as channel-based approach)
	handlePacketMutex sync.Mutex

	// Queue for packets that are written with writePacket() and will be send to the network
	writeQueue  chan packet.Packet
	writeBuffer bytes.Buffer
	writeData   []byte

	// Queue for packets that will be read locally with ReadPacket()
	readQueue  chan packet.Packet
	readBuffer bytes.Buffer

	onSend     func(p packet.Packet)
	sendFilter func(p packet.Packet) bool // Optional filter for testing (returns false to drop)
	onShutdown func(socketId uint32)

	tick time.Duration

	// Congestion control
	recv congestion.Receiver
	snd  congestion.Sender

	// context of all channels and routines
	ctx       context.Context
	cancelCtx context.CancelFunc

	// Waitgroups for graceful shutdown
	shutdownWg *sync.WaitGroup // Parent waitgroup (from listener/dialer)
	connWg     sync.WaitGroup  // Waitgroup for all connection goroutines

	// statistics and statisticsLock removed - all statistics now use atomic counters in metrics.ConnectionMetrics

	// Metrics for Prometheus (atomic counters, lock-free)
	metrics *metrics.ConnectionMetrics

	logger Logger

	debug struct {
		expectedRcvPacketSequenceNumber  circular.Number
		expectedReadPacketSequenceNumber circular.Number
	}

	// HSv4
	stopHSRequests context.CancelFunc
	stopKMRequests context.CancelFunc

	// Control packet dispatch tables (initialized once, never modified, no locking needed)
	controlHandlers map[packet.CtrlType]controlPacketHandler // Main control type handlers
	userHandlers    map[packet.CtrlSubType]userPacketHandler // CTRLTYPE_USER SubType handlers

	// io_uring send queue (per-connection) - using giouring for high performance
	// Type is interface{} to allow conditional compilation (giouring.Ring on Linux, nil on others)
	sendRing   interface{} // Direct ring access, no channels (type: *giouring.Ring on Linux)
	sendRingFd int         // File descriptor for the socket (not the ring)

	// Pre-computed sockaddr for UDP sends (computed once at connection init, reused for all sends)
	sendSockaddr    syscall.RawSockaddrAny // Pre-computed sockaddr structure
	sendSockaddrLen uint32                 // Length of sockaddr structure

	// Per-connection send buffer pool (eliminates lock contention)
	sendBufferPool sync.Pool // Isolated pool per connection

	// Reference to listener/dialer's receive buffer pool (Phase 2: zero-copy)
	// Used by receiver.releasePacketFully() to return buffers after packet delivery
	recvBufferPool *sync.Pool

	// Completion tracking - minimal structure for performance
	sendCompletions map[uint64]*sendCompletionInfo // Maps request ID to completion info
	sendCompLock    sync.RWMutex                   // Protects sendCompletions map

	// Atomic counter for generating unique request IDs
	sendRequestID atomic.Uint64

	// Completion handler goroutine lifecycle (giouring uses direct CQE polling)
	sendCompCtx    context.Context
	sendCompCancel context.CancelFunc
	sendCompWg     sync.WaitGroup // Wait for completion handler to finish
}

// sendCompletionInfo stores minimal information needed for completion handling
type sendCompletionInfo struct {
	buffer    *bytes.Buffer // Buffer to return to per-connection pool
	packet    packet.Packet // Packet for metrics tracking (nil for control packets after decommission)
	isIoUring bool          // Track path for metrics
}

type srtConnConfig struct {
	version                     uint32
	isCaller                    bool
	localAddr                   net.Addr
	remoteAddr                  net.Addr
	config                      Config
	start                       time.Time
	socketId                    uint32
	peerSocketId                uint32
	tsbpdTimeBase               uint64 // microseconds
	tsbpdDelay                  uint64 // microseconds
	peerTsbpdDelay              uint64 // microseconds
	initialPacketSequenceNumber circular.Number
	crypto                      crypto.Crypto
	keyBaseEncryption           packet.PacketEncryption
	onSend                      func(p packet.Packet)
	sendFilter                  func(p packet.Packet) bool // Optional filter for testing
	onShutdown                  func(socketId uint32)
	logger                      Logger
	socketFd                    int                        // File descriptor for the UDP socket (for io_uring)
	parentCtx                   context.Context            // Parent context (from listener/dialer)
	parentWg                    *sync.WaitGroup            // Parent waitgroup (from listener/dialer)
	metrics                     *metrics.ConnectionMetrics // Pre-created metrics (required)
	recvBufferPool              *sync.Pool                 // Receive buffer pool (Phase 2: zero-copy)
}

// createConnectionMetrics creates a ConnectionMetrics instance for a connection.
// This should be called BEFORE newSRTConn() so that onSend closures can capture
// the metrics reference, avoiding initialization race conditions.
// The instanceName parameter is used for Prometheus metrics labeling.
func createConnectionMetrics(localAddr net.Addr, socketId uint32, instanceName string) *metrics.ConnectionMetrics {
	// Calculate header size (needed for metrics initialization)
	headerSize := uint64(8 + 16) // 8 bytes UDP + 16 bytes SRT
	if strings.Count(localAddr.String(), ":") < 2 {
		headerSize += 20 // 20 bytes IPv4 header
	} else {
		headerSize += 40 // 40 bytes IPv6 header
	}

	m := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	m.HeaderSize.Store(headerSize)

	// Register with metrics registry
	// Pass instance name for Prometheus labeling
	metrics.RegisterConnection(socketId, m, instanceName)

	return m
}

func newSRTConn(config srtConnConfig) *srtConn {
	// Validate required fields
	if config.metrics == nil {
		panic("newSRTConn: metrics must be pre-created via createConnectionMetrics()")
	}
	if config.onSend == nil {
		panic("newSRTConn: onSend must be provided (use createConnectionMetrics() first to build closure)")
	}

	c := &srtConn{
		version:                     config.version,
		isCaller:                    config.isCaller,
		localAddr:                   config.localAddr,
		remoteAddr:                  config.remoteAddr,
		config:                      config.config,
		start:                       config.start,
		socketId:                    config.socketId,
		peerSocketId:                config.peerSocketId,
		tsbpdTimeBase:               config.tsbpdTimeBase,
		tsbpdDelay:                  config.tsbpdDelay,
		peerTsbpdDelay:              config.peerTsbpdDelay,
		initialPacketSequenceNumber: config.initialPacketSequenceNumber,
		crypto:                      config.crypto,
		keyBaseEncryption:           config.keyBaseEncryption,
		onSend:                      config.onSend,     // Now fully initialized - no race!
		sendFilter:                  config.sendFilter, // Optional test filter
		onShutdown:                  config.onShutdown,
		logger:                      config.logger,
		metrics:                     config.metrics,        // Pre-created - no race!
		recvBufferPool:              config.recvBufferPool, // Phase 2: zero-copy
	}

	if c.onShutdown == nil {
		c.onShutdown = func(socketId uint32) {}
	}

	c.nextACKNumber.Store(1)           // ACK numbers start at 1 (0 is reserved for Light ACK)
	c.ackNumbers = newAckEntryBtree(4) // ACK-5: btree with degree 4 (optimal for ~10 entries)

	c.kmPreAnnounceCountdown = c.config.KMRefreshRate - c.config.KMPreAnnounce
	c.kmRefreshCountdown = c.config.KMRefreshRate

	// 4.10.  Round-Trip Time Estimation (ACK-10: atomic initialization)
	c.rtt.rttBits.Store(math.Float64bits(float64((100 * time.Millisecond).Microseconds())))
	c.rtt.rttVarBits.Store(math.Float64bits(float64((50 * time.Millisecond).Microseconds())))
	// Set minimum NAK interval from config (convert ms to µs)
	c.rtt.minNakIntervalUs.Store(c.config.PeriodicNakIntervalMs * 1000)

	// Determine channel buffer sizes (default: 1024 if not configured)
	networkQueueSize := c.config.NetworkQueueSize
	if networkQueueSize <= 0 {
		networkQueueSize = 1024
	}
	c.networkQueue = make(chan packet.Packet, networkQueueSize)

	writeQueueSize := c.config.WriteQueueSize
	if writeQueueSize <= 0 {
		writeQueueSize = 1024
	}
	c.writeQueue = make(chan packet.Packet, writeQueueSize)
	if c.version == 4 {
		// libsrt-1.2.3 receiver doesn't like it when the payload is larger than 7*188 bytes.
		// Here we just take a multiple of a mpegts chunk size.
		c.writeData = make([]byte, int(c.config.PayloadSize/188*188))
	} else {
		// For v5 we use the max. payload size: https://github.com/Haivision/srt/issues/876
		c.writeData = make([]byte, int(c.config.PayloadSize))
	}

	readQueueSize := c.config.ReadQueueSize
	if readQueueSize <= 0 {
		readQueueSize = 1024
	}
	c.readQueue = make(chan packet.Packet, readQueueSize)

	c.debug.expectedRcvPacketSequenceNumber = c.initialPacketSequenceNumber
	c.debug.expectedReadPacketSequenceNumber = c.initialPacketSequenceNumber

	// Metrics already created and registered via createConnectionMetrics()

	// TSBPD delivery tick interval - configurable via TickIntervalMs (default: 10ms)
	c.tick = time.Duration(c.config.TickIntervalMs) * time.Millisecond

	// 4.8.1.  Packet Acknowledgement (ACKs, ACKACKs) -> periodicACK = 10 milliseconds (default)
	// 4.8.2.  Packet Retransmission (NAKs) -> periodicNAK at least 20 milliseconds (default)
	// Note: Timer intervals now configurable via PeriodicAckIntervalMs/PeriodicNakIntervalMs
	c.recv = receive.New(receive.Config{
		InitialSequenceNumber:  c.initialPacketSequenceNumber,
		PeriodicACKInterval:    c.config.PeriodicAckIntervalMs * 1000, // Convert ms to µs
		PeriodicNAKInterval:    c.config.PeriodicNakIntervalMs * 1000, // Convert ms to µs
		OnSendACK:              c.sendACK,
		OnSendNAK:              c.sendNAK,
		OnDeliver:              c.deliver,
		PacketReorderAlgorithm: c.config.PacketReorderAlgorithm,
		BTreeDegree:            c.config.BTreeDegree,
		LockTimingMetrics:      c.metrics.ReceiverLockTiming,
		ConnectionMetrics:      c.metrics,

		// Buffer pool for zero-copy support (Phase 2: Lockless Design)
		BufferPool: c.recvBufferPool,

		// NAK btree configuration - enables TSBPD-based "too recent" protection for io_uring
		UseNakBtree:            c.config.UseNakBtree,
		SuppressImmediateNak:   c.config.SuppressImmediateNak,
		TsbpdDelay:             c.tsbpdDelay, // Note: Set after handshake, initially 0
		NakRecentPercent:       c.config.NakRecentPercent,
		NakMergeGap:            c.config.NakMergeGap,
		NakConsolidationBudget: c.config.NakConsolidationBudgetUs,

		// FastNAK configuration - quick NAK after silence period
		FastNakEnabled:       c.config.FastNakEnabled,
		FastNakThresholdUs:   c.config.FastNakThresholdMs * 1000, // Convert ms to µs
		FastNakRecentEnabled: c.config.FastNakRecentEnabled,

		// Lock-free ring buffer configuration (Phase 3: Lockless Design)
		UsePacketRing:             c.config.UsePacketRing,
		PacketRingSize:            c.config.PacketRingSize,
		PacketRingShards:          c.config.PacketRingShards,
		PacketRingMaxRetries:      c.config.PacketRingMaxRetries,
		PacketRingBackoffDuration: c.config.PacketRingBackoffDuration,
		PacketRingMaxBackoffs:     c.config.PacketRingMaxBackoffs,

		// Event loop configuration (Phase 4: Lockless Design)
		UseEventLoop:          c.config.UseEventLoop,
		EventLoopRateInterval: c.config.EventLoopRateInterval,
		BackoffColdStartPkts:  c.config.BackoffColdStartPkts,
		BackoffMinSleep:       c.config.BackoffMinSleep,
		BackoffMaxSleep:       c.config.BackoffMaxSleep,

		// Time base configuration (Phase 10: EventLoop Time Fix)
		// Pass connection's time base so receiver's nowFn matches PktTsbpdTime calculation.
		// Without this, EventLoop uses absolute Unix time while PktTsbpdTime is relative.
		TsbpdTimeBase: c.tsbpdTimeBase,
		StartTime:     c.start,

		// Light ACK configuration (Phase 5: ACK Optimization)
		LightACKDifference: c.config.LightACKDifference,

		// Debug logging - pass connection's log function
		Debug:   c.config.ReceiverDebug,
		LogFunc: c.log,
	})

	// 4.6.  Too-Late Packet Drop -> 125% of SRT latency, at least 1 second
	// https://github.com/Haivision/srt/blob/master/docs/API/API-socket-options.md#SRTO_SNDDROPDELAY
	c.dropThreshold = uint64(float64(c.peerTsbpdDelay)*1.25) + uint64(c.config.SendDropDelay.Microseconds())
	if c.dropThreshold < uint64(time.Second.Microseconds()) {
		c.dropThreshold = uint64(time.Second.Microseconds())
	}
	c.dropThreshold += 20_000

	c.snd = live.NewSender(live.SendConfig{
		InitialSequenceNumber: c.initialPacketSequenceNumber,
		DropThreshold:         c.dropThreshold,
		MaxBW:                 c.config.MaxBW,
		InputBW:               c.config.InputBW,
		MinInputBW:            c.config.MinInputBW,
		OverheadBW:            c.config.OverheadBW,
		OnDeliver:             c.pop,
		LockTimingMetrics:     c.metrics.SenderLockTiming,
		ConnectionMetrics:     c.metrics,
		HonorNakOrder:         c.config.HonorNakOrder,
	})

	// Store parent waitgroup
	c.shutdownWg = config.parentWg

	// Create connection context from parent context
	c.ctx, c.cancelCtx = context.WithCancel(config.parentCtx)

	// Initialize control packet dispatch tables (must be done before connection is used)
	c.initializeControlHandlers()

	// Initialize io_uring send ring if enabled (Linux-specific)
	c.initializeIoUring(config)

	// Initialize peer idle timeout (must be after context is created)
	c.peerIdleTimeout = time.NewTimer(c.config.PeerIdleTimeout)
	c.peerIdleTimeoutLastReset.Store(time.Now().UnixNano())

	// Start connection goroutines with waitgroup tracking.
	// Safe to start immediately because onSend and metrics are pre-initialized.
	c.connWg.Add(1)
	go func() {
		defer c.connWg.Done()
		c.networkQueueReader(c.ctx)
	}()

	c.connWg.Add(1)
	go func() {
		defer c.connWg.Done()
		c.writeQueueReader(c.ctx)
	}()

	c.connWg.Add(1)
	go func() {
		defer c.connWg.Done()
		c.ticker(c.ctx)
	}()

	// Start peer idle timeout watcher (must be after context is created)
	c.connWg.Add(1)
	go func() {
		defer c.connWg.Done()
		c.watchPeerIdleTimeout()
	}()

	if c.version == 4 && c.isCaller {
		// HSv4 caller contexts inherit from connection context
		var hsrequestsCtx context.Context
		hsrequestsCtx, c.stopHSRequests = context.WithCancel(c.ctx)
		c.connWg.Add(1)
		go func() {
			defer c.connWg.Done()
			c.sendHSRequests(hsrequestsCtx)
		}()

		if c.crypto != nil {
			var kmrequestsCtx context.Context
			kmrequestsCtx, c.stopKMRequests = context.WithCancel(c.ctx)
			c.connWg.Add(1)
			go func() {
				defer c.connWg.Done()
				c.sendKMRequests(kmrequestsCtx)
			}()
		}
	}

	return c
}

func (c *srtConn) LocalAddr() net.Addr {
	if c.localAddr == nil {
		return nil
	}

	addr, _ := net.ResolveUDPAddr("udp", c.localAddr.String())
	return addr
}

func (c *srtConn) RemoteAddr() net.Addr {
	if c.remoteAddr == nil {
		return nil
	}

	addr, _ := net.ResolveUDPAddr("udp", c.remoteAddr.String())
	return addr
}

func (c *srtConn) SocketId() uint32 {
	return c.socketId
}

func (c *srtConn) PeerSocketId() uint32 {
	return c.peerSocketId
}

func (c *srtConn) StreamId() string {
	return c.config.StreamId
}

func (c *srtConn) Version() uint32 {
	return c.version
}

// ticker invokes the congestion control in regular intervals with
// the current connection time.
//
// Phase 4: If the receiver uses event loop (UseEventLoop=true), the event loop
// runs in a separate goroutine and handles packet processing continuously.
// In this case, ticker() only drives the sender.
func (c *srtConn) ticker(ctx context.Context) {
	// Phase 4: Start event loop in separate goroutine if enabled
	if c.recv.UseEventLoop() {
		go c.recv.EventLoop(ctx)
	}

	ticker := time.NewTicker(c.tick)
	defer ticker.Stop()
	defer func() {
		c.log("connection:close", func() string { return "left ticker loop" })
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			tickTime := uint64(t.Sub(c.start).Microseconds())

			// Phase 4: Only call recv.Tick if event loop is not running
			if !c.recv.UseEventLoop() {
				c.recv.Tick(c.tsbpdTimeBase + tickTime)
			}
			c.snd.Tick(tickTime)
		}
	}
}

// ReadPacket, Read, WritePacket, Write, push, pop, getTimestamp, getTimestampForPacket,
// networkQueueReader, writeQueueReader, deliver - moved to connection_io.go

// handlePacketDirect, initializeControlHandlers, handleUserPacket, handlePacket,
// handleKeepAlive, sendProactiveKeepalive, getKeepaliveInterval, handleShutdown,
// handleACK, handleNAK, ExtendedStatistics, GetExtendedStatistics, handleACKACK,
// recalculateRTT, getNextACKNumber - moved to connection_handlers.go

// handleHSRequest, handleHSResponse, sendHSRequests, sendHSRequest - moved to connection_handshake.go



// handleKMRequest, handleKMResponse, sendKMRequests, sendKMRequest - moved to connection_keymgmt.go

// sendShutdown, splitNakList, sendNAK, sendACK, sendACKACK - moved to connection_send.go

// Close, GetPeerIdleTimeoutRemaining, resetPeerIdleTimeout, getTotalReceivedPackets,
// watchPeerIdleTimeout, close, log - moved to connection_lifecycle.go

// Statistics, Stats, printCloseStatistics, SetDeadline, SetReadDeadline, SetWriteDeadline - moved to connection_stats.go

