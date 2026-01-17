package srt

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/congestion/live/receive"
	"github.com/randomizedcoder/gosrt/congestion/live/send"
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

	// Receiver Control Ring (Phase 6: Completely Lock-Free Receiver)
	// Routes ACKACK and KEEPALIVE to EventLoop for lock-free processing.
	// nil means disabled, non-nil means enabled (no separate bool needed).
	// Reference: completely_lockfree_receiver.md Section 6.1.5
	recvControlRing *receive.RecvControlRing

	// context of all channels and routines
	ctx       context.Context
	cancelCtx context.CancelFunc

	// Debug context for lock-free path verification (debug builds only)
	// Tracks whether we're in EventLoop or Tick context for assert validation.
	// nil in release builds (zero overhead).
	debugCtx *connDebugContext

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

	// Control packet dispatch tables
	controlHandlers map[packet.CtrlType]controlPacketHandler
	userHandlers    map[packet.CtrlSubType]userPacketHandler

	// io_uring send queue (per-connection) - using giouring for high performance
	// Type is interface{} to allow conditional compilation (giouring.Ring on Linux, nil on others)
	// Multi-ring support: When IoUringSendRingCount > 1, we create multiple
	// independent io_uring rings, each with its own completion handler goroutine.
	// This enables parallel completion processing across CPU cores.
	// See multi_iouring_design.md Section 4.3 for design rationale.

	// Legacy single-ring fields (used when IoUringSendRingCount == 1)
	sendRing   interface{} // Direct ring access, no channels (type: *giouring.Ring on Linux)
	sendRingFd int         // Socket fd (shared by all rings)

	// Pre-computed sockaddr for UDP sends (computed once at connection init, reused for all sends)
	sendSockaddr    syscall.RawSockaddrAny // Pre-computed sockaddr structure
	sendSockaddrLen uint32                 // Length of sockaddr structure

	// Per-connection send buffer pool (eliminates lock contention)
	sendBufferPool sync.Pool // Isolated pool per connection

	// Reference to listener/dialer's receive buffer pool (Phase 2: zero-copy)
	// Used by receiver.releasePacketFully() to return buffers after packet delivery
	recvBufferPool *sync.Pool

	// Legacy single-ring completion tracking
	sendCompletions map[uint64]*sendCompletionInfo // Maps request ID to completion info
	sendCompLock    sync.RWMutex                   // Protects sendCompletions map

	// Atomic counter for generating unique request IDs (legacy single-ring)
	sendRequestID atomic.Uint64

	// Multi-ring fields (used when IoUringSendRingCount > 1)
	// Each sendRingState owns its ring, completion map, and ID counter
	// Note: Per-ring lock needed because sender and completer access concurrently
	sendRingStates     []*sendRingState // Slice of independent ring states
	sendRingNextIdx    atomic.Uint32    // Round-robin index for ring selection

	// Shared by both single-ring and multi-ring modes
	sendCompCtx    context.Context
	sendCompCancel context.CancelFunc
	sendCompWg     sync.WaitGroup // Wait for completion handler(s) to finish
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
// remoteAddr and streamId enable richer Prometheus labels for connection identification.
// startTime is the time when the connection was initiated (for age metrics).
// peerSocketId is the remote peer's socket ID (for cross-process connection correlation).
func createConnectionMetrics(localAddr net.Addr, socketId uint32, instanceName string,
	remoteAddr net.Addr, streamId string, peerSocketId uint32, startTime time.Time) *metrics.ConnectionMetrics {
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

	// Derive peer type from stream ID for Prometheus labeling
	peerType := derivePeerType(streamId)

	// Build remote address string (handle nil case for early registration)
	remoteAddrStr := ""
	if remoteAddr != nil {
		remoteAddrStr = remoteAddr.String()
	}

	// Register with metrics registry including connection metadata
	info := &metrics.ConnectionInfo{
		Metrics:      m,
		InstanceName: instanceName,
		RemoteAddr:   remoteAddrStr,
		StreamId:     streamId,
		PeerType:     peerType,
		PeerSocketID: peerSocketId,
		StartTime:    startTime,
	}
	metrics.RegisterConnection(socketId, info)

	return m
}

// derivePeerType determines the peer type from the stream ID.
// Returns "publisher" for publish streams, "subscriber" for subscribe streams,
// or "unknown" if the stream type cannot be determined.
func derivePeerType(streamId string) string {
	streamIdLower := strings.ToLower(streamId)
	if strings.HasPrefix(streamIdLower, "publish:") || strings.Contains(streamIdLower, "/publish") {
		return "publisher"
	} else if strings.HasPrefix(streamIdLower, "subscribe:") || strings.Contains(streamIdLower, "/subscribe") {
		return "subscriber"
	}
	return "unknown"
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

	// Debug context for lock-free path verification (nil in release builds)
	c.debugCtx = newConnDebugContext()

	c.nextACKNumber.Store(1)           // ACK numbers start at 1 (0 is reserved for Light ACK)
	c.ackNumbers = newAckEntryBtree(4) // ACK-5: btree with degree 4 (optimal for ~10 entries)

	c.kmPreAnnounceCountdown = c.config.KMRefreshRate - c.config.KMPreAnnounce
	c.kmRefreshCountdown = c.config.KMRefreshRate

	// 4.10.  Round-Trip Time Estimation (ACK-10: atomic initialization)
	c.rtt.rttBits.Store(math.Float64bits(float64((100 * time.Millisecond).Microseconds())))
	c.rtt.rttVarBits.Store(math.Float64bits(float64((50 * time.Millisecond).Microseconds())))
	// Set minimum NAK interval from config (convert ms to µs)
	c.rtt.minNakIntervalUs.Store(c.config.PeriodicNakIntervalMs * 1000)

	// Phase 6: RTO Suppression - configure RTO calculation mode
	// This must be set before receiver is created so RTOUs() returns valid values
	c.rtt.SetRTOMode(c.config.RTOMode, c.config.ExtraRTTMargin)
	// Trigger initial RTO calculation based on initial RTT values
	c.rtt.Recalculate(100 * time.Millisecond)

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

	// Initialize receiver (extracted for readability)
	c.recv = createReceiver(c)

	// Phase 6: RTO Suppression - wire up RTT provider to receiver for NAK suppression
	// This enables RTO-based NAK suppression in consolidateNakBtree()
	c.recv.SetRTTProvider(&c.rtt)

	// Phase 6: Receiver Control Ring - Initialize for ACKACK/KEEPALIVE routing
	// Note: No separate bool - recvControlRing != nil means enabled
	// Reference: completely_lockfree_receiver.md Section 6.1.5
	if c.config.UseRecvControlRing && c.config.UseEventLoop {
		ringSize := c.config.RecvControlRingSize
		if ringSize == 0 {
			ringSize = 128 // Default
		}
		ringShards := c.config.RecvControlRingShards
		if ringShards < 1 {
			ringShards = 1 // Default
		}
		ring, err := receive.NewRecvControlRing(ringSize, ringShards)
		if err != nil {
			// Log error but continue - will use locked path (ring stays nil)
			c.log("connection:init:error", func() string {
				return fmt.Sprintf("failed to create recv control ring: %v", err)
			})
		} else {
			c.recvControlRing = ring
			c.log("connection:init:recv_control_ring", func() string {
				return fmt.Sprintf("recv control ring enabled: size=%d, shards=%d", ringSize, ringShards)
			})

			// Set the callback on receiver so EventLoop can process control packets inline.
			// This eliminates the ~100µs polling latency from recvControlRingLoop.
			// The drainRecvControlRing function returns int for the number of packets processed.
			c.recv.SetProcessConnectionControlPackets(func() int {
				return c.drainRecvControlRingCount()
			})
		}
	}

	// 4.6.  Too-Late Packet Drop -> 125% of SRT latency, at least 1 second
	// https://github.com/Haivision/srt/blob/master/docs/API/API-socket-options.md#SRTO_SNDDROPDELAY
	c.dropThreshold = uint64(float64(c.peerTsbpdDelay)*1.25) + uint64(c.config.SendDropDelay.Microseconds())
	if c.dropThreshold < uint64(time.Second.Microseconds()) {
		c.dropThreshold = uint64(time.Second.Microseconds())
	}
	c.dropThreshold += 20_000

	c.snd = createSender(c)

	c.shutdownWg = config.parentWg

	c.ctx, c.cancelCtx = context.WithCancel(config.parentCtx)

	c.initializeControlHandlers()

	c.peerIdleTimeout = time.NewTimer(c.config.PeerIdleTimeout)
	c.peerIdleTimeoutLastReset.Store(time.Now().UnixNano())

	if c.config.IoUringRecvEnabled || c.config.IoUringEnabled {
		c.initializeIoUring(config)
	}

	if !c.config.IoUringRecvEnabled {
		c.connWg.Add(1)
		go c.networkQueueReader(c.ctx, &c.connWg)
	}

	// writeQueueReader takes packets from application Write() calls and pushes
	// them to the sender congestion control. This is ALWAYS needed regardless
	// of io_uring mode - io_uring is for network I/O (recv/send), not for the
	// internal write queue which feeds application data to the sender.
	c.connWg.Add(1)
	go c.writeQueueReader(c.ctx, &c.connWg)

	// Start processing goroutines based on receiver/sender mode
	//
	// CASE 1: Receiver EventLoop mode
	//   - recv.EventLoop() processes data packets AND control packets (via callback)
	//   - senderTickLoop() drives sender's Tick() (sender EventLoop not yet implemented)
	//   - NO recvControlRingLoop needed (control packets processed inline)
	//
	// CASE 2: Receiver Tick mode (legacy)
	//   - ticker() drives both recv.Tick() and snd.Tick()
	//   - recvControlRingLoop() handles control packets (if ring enabled)
	//
	// Reference: completely_lockfree_receiver_debugging.md Section 15-16
	c.log("connection:init:startup:debug", func() string {
		return fmt.Sprintf("recv.UseEventLoop=%v, snd.UseEventLoop=%v, config.UseEventLoop=%v, config.UsePacketRing=%v, recvControlRing=%v",
			c.recv.UseEventLoop(), c.snd.UseEventLoop(), c.config.UseEventLoop, c.config.UsePacketRing, c.recvControlRing != nil)
	})
	if c.recv.UseEventLoop() {
		// CASE 1: Receiver EventLoop mode
		c.log("connection:init:startup:eventloop", func() string {
			return "Starting receiver EventLoop mode"
		})
		c.connWg.Add(1)
		go c.recv.EventLoop(c.ctx, &c.connWg)

		// Check sender mode - either EventLoop or Tick
		if c.snd.UseEventLoop() {
			// CASE 1a: Both receiver and sender in EventLoop mode
			c.log("connection:init:startup:snd-eventloop", func() string {
				return "Starting sender EventLoop mode"
			})
			c.connWg.Add(1)
			go c.snd.EventLoop(c.ctx, &c.connWg)
		} else {
			// CASE 1b: Receiver EventLoop + Sender Tick
			// Use senderTickLoop to drive sender's Tick() in a separate goroutine
			c.connWg.Add(1)
			go c.senderTickLoop(c.ctx, &c.connWg)
		}
	} else {
		// CASE 2: Receiver Tick mode (legacy)
		// ticker() drives both recv.Tick() and snd.Tick()
		c.connWg.Add(1)
		go c.ticker(c.ctx, &c.connWg)
	}

	// Start peer idle timeout watcher (must be after context is created)
	c.connWg.Add(1)
	go c.watchPeerIdleTimeout(c.ctx, &c.connWg)

	if c.version == 4 && c.isCaller {
		// HSv4 caller contexts inherit from connection context
		var hsrequestsCtx context.Context
		hsrequestsCtx, c.stopHSRequests = context.WithCancel(c.ctx)
		c.connWg.Add(1)
		go c.sendHSRequests(hsrequestsCtx, &c.connWg)

		if c.crypto != nil {
			var kmrequestsCtx context.Context
			kmrequestsCtx, c.stopKMRequests = context.WithCancel(c.ctx)
			c.connWg.Add(1)
			go c.sendKMRequests(kmrequestsCtx, &c.connWg)
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

// createReceiver initializes the receiver with connection-specific configuration.
// Extracted from newSRTConn for readability.
// Reference: SRT spec 4.8.1 (ACKs), 4.8.2 (NAKs)
func createReceiver(c *srtConn) congestion.Receiver {
	return receive.New(receive.Config{
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

		// NAK btree expiry configuration (nak_btree_expiry_optimization.md)
		NakExpiryMargin:     c.config.NakExpiryMargin,
		EWMAWarmupThreshold: c.config.EWMAWarmupThreshold,

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
}

// createSender initializes the sender with connection-specific configuration.
// Extracted from newSRTConn for readability.
// Reference: SRT spec 4.6 (Too-Late Packet Drop)
func createSender(c *srtConn) congestion.Sender {
	return send.NewSender(send.SendConfig{
		InitialSequenceNumber: c.initialPacketSequenceNumber,
		DropThreshold:         c.dropThreshold,
		MaxBW:                 c.config.MaxBW,
		InputBW:               c.config.InputBW,
		MinInputBW:            c.config.MinInputBW,
		OverheadBW:            c.config.OverheadBW,
		OnDeliver:             c.pop,
		OnLog:                 c.log,
		LockTimingMetrics:     c.metrics.SenderLockTiming,
		ConnectionMetrics:     c.metrics,

		// CRITICAL: Connection start time for EventLoop time base.
		// PktTsbpdTime uses relative time (since connection start), so EventLoop
		// must also use relative time for TSBPD comparisons.
		StartTime:     c.start,
		HonorNakOrder: c.config.HonorNakOrder,
		RTOUs:         &c.rtt.rtoUs, // RTO suppression (Phase 6)

		// Phase 1: Sender Lockless Btree
		UseBtree:    c.config.UseSendBtree,
		BtreeDegree: c.config.SendBtreeDegree,

		// Phase 2: Sender Lock-Free Ring
		UseSendRing:    c.config.UseSendRing,
		SendRingSize:   c.config.SendRingSize,
		SendRingShards: c.config.SendRingShards,

		// Phase 3: Sender Control Packet Ring
		UseSendControlRing:    c.config.UseSendControlRing,
		SendControlRingSize:   c.config.SendControlRingSize,
		SendControlRingShards: c.config.SendControlRingShards,

		// Phase 4: Sender EventLoop
		UseSendEventLoop:             c.config.UseSendEventLoop,
		SendEventLoopBackoffMinSleep: c.config.SendEventLoopBackoffMinSleep,
		SendEventLoopBackoffMaxSleep: c.config.SendEventLoopBackoffMaxSleep,
		SendTsbpdSleepFactor:         c.config.SendTsbpdSleepFactor,
		SendDropThresholdUs:          c.config.SendDropThresholdUs,

		// Phase 5: Zero-copy payload pool
		ValidatePayloadSize: c.config.ValidateSendPayloadSize,
	})
}

// ticker invokes the congestion control in regular intervals with
// the current connection time.
//
// Phase 4: If the receiver uses event loop (UseEventLoop=true), the event loop
// runs in a separate goroutine and handles packet processing continuously.
// In this case, ticker() only drives the sender (unless sender EventLoop is also enabled).

// ═══════════════════════════════════════════════════════════════════════════
// Phase 7: Control Ring Processing (Completely Lock-Free Receiver)
// ═══════════════════════════════════════════════════════════════════════════

// drainRecvControlRingCount processes all pending control packets from the ring.
// Returns the number of packets processed.
// Called from receiver EventLoop via the ProcessConnectionControlPackets callback.
//
// This function processes ACKACK and KEEPALIVE packets that were pushed to
// the ring by dispatchACKACK() and dispatchKeepAlive().
//
// Reference: completely_lockfree_receiver.md Section 4.1
func (c *srtConn) drainRecvControlRingCount() int {
	if c.recvControlRing == nil {
		return 0
	}

	count := 0
	for {
		cp, ok := c.recvControlRing.TryPop()
		if !ok {
			break
		}

		count++
		if c.metrics != nil {
			c.metrics.RecvControlRingDrained.Add(1)
		}

		switch cp.Type {
		case receive.RecvControlTypeACKACK:
			arrivalTime := time.Unix(0, cp.Timestamp)
			c.handleACKACK(cp.ACKNumber, arrivalTime)
			if c.metrics != nil {
				c.metrics.RecvControlRingProcessedACKACK.Add(1)
				c.metrics.RecvControlRingProcessed.Add(1)
			}
		case receive.RecvControlTypeKEEPALIVE:
			c.handleKeepAliveEventLoop()
			// Note: handleKeepAliveEventLoop already increments RecvControlRingProcessedKEEPALIVE
			if c.metrics != nil {
				c.metrics.RecvControlRingProcessed.Add(1)
			}
		}
	}
	return count
}

// drainRecvControlRing processes all pending control packets from the ring.
// Called from recvControlRingLoop when receiver is in Tick mode.
//
// This is a wrapper around drainRecvControlRingCount that discards the count.
func (c *srtConn) drainRecvControlRing() {
	c.drainRecvControlRingCount()
}

// recvControlRingLoop runs the control ring drain loop.
// Called as a separate goroutine when control ring is enabled.
//
// This loop continuously drains control packets from the ring and processes them.
// The loop runs at high frequency (10kHz) to ensure timely RTT calculation.
//
// Reference: completely_lockfree_receiver.md Section 4.1
func (c *srtConn) recvControlRingLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	if c.recvControlRing == nil {
		return
	}

	// 10kHz check rate - ensures ACKACK is processed promptly for RTT
	ticker := time.NewTicker(100 * time.Microsecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Drain any remaining packets before exit
			c.drainRecvControlRing()
			return
		case <-ticker.C:
			c.drainRecvControlRing()
		}
	}
}

// senderTickLoop drives only the sender's Tick() in a loop.
// Used when receiver is in EventLoop mode but sender is in Tick mode.
// This separates sender timing from receiver timing.
//
// Reference: completely_lockfree_receiver_debugging.md Section 16.2
func (c *srtConn) senderTickLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(c.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log("connection:close", func() string { return "left senderTickLoop" })
			return
		case t := <-ticker.C:
			tickTime := uint64(t.Sub(c.start).Microseconds())
			c.snd.Tick(tickTime)
		}
	}
}

// ticker drives both receiver and sender Tick() in the legacy (non-EventLoop) mode.
// Also starts recvControlRingLoop if control ring is enabled.
//
// This function is only used when receiver is NOT in EventLoop mode.
// When receiver IS in EventLoop mode, recv.EventLoop() and senderTickLoop() are used instead.
func (c *srtConn) ticker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	var eventLoopWg sync.WaitGroup

	// Start recvControlRingLoop only when receiver is NOT in EventLoop mode.
	// When receiver IS in EventLoop mode, control packets are processed inline
	// via the ProcessConnectionControlPackets callback.
	if c.recvControlRing != nil {
		eventLoopWg.Add(1)
		go c.recvControlRingLoop(ctx, &eventLoopWg)
	}

	ticker := time.NewTicker(c.tick)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case t := <-ticker.C:
			tickTime := uint64(t.Sub(c.start).Microseconds())

			c.recv.Tick(c.tsbpdTimeBase + tickTime)

			c.snd.Tick(tickTime)
		}
	}

	c.log("connection:close", func() string { return "waiting for EventLoop goroutines" })
	eventLoopWg.Wait()
	c.log("connection:close", func() string { return "left ticker loop" })
}
