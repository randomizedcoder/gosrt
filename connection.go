package srt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/congestion"
	"github.com/datarhei/gosrt/congestion/live"
	"github.com/datarhei/gosrt/crypto"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
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

// rtt implements lock-free RTT tracking using atomic operations.
// ACK-10: Replaced lock-based implementation with atomics for 8x better performance.
// See rtt_benchmark_test.go for benchmarks showing:
//   - Reads: 50-800x faster
//   - Mixed workload: 8x faster
type rtt struct {
	rttBits          atomic.Uint64 // float64 stored as bits
	rttVarBits       atomic.Uint64 // float64 stored as bits
	minNakIntervalUs atomic.Uint64 // minimum NAK interval in microseconds (from config)
}

// Recalculate updates RTT using EWMA smoothing (RFC 4.10).
// Uses atomic CAS to avoid locks.
func (r *rtt) Recalculate(rtt time.Duration) {
	lastRTT := float64(rtt.Microseconds())

	for {
		oldRTTBits := r.rttBits.Load()
		oldRTT := math.Float64frombits(oldRTTBits)
		oldRTTVar := math.Float64frombits(r.rttVarBits.Load())

		// RFC 4.10: EWMA smoothing
		newRTTVal := oldRTT*0.875 + lastRTT*0.125
		newRTTVarVal := oldRTTVar*0.75 + math.Abs(newRTTVal-lastRTT)*0.25

		// CAS the RTT value
		if r.rttBits.CompareAndSwap(oldRTTBits, math.Float64bits(newRTTVal)) {
			// RTT updated, now update RTTVar (slight race window acceptable for EWMA)
			r.rttVarBits.Store(math.Float64bits(newRTTVarVal))
			break
		}
		// CAS failed - another goroutine updated RTT, retry with new value
	}
}

func (r *rtt) RTT() float64 {
	return math.Float64frombits(r.rttBits.Load())
}

func (r *rtt) RTTVar() float64 {
	return math.Float64frombits(r.rttVarBits.Load())
}

func (r *rtt) NAKInterval() float64 {
	// 4.8.2.  Packet Retransmission (NAKs)
	rttVal := math.Float64frombits(r.rttBits.Load())
	rttVarVal := math.Float64frombits(r.rttVarBits.Load())
	// Use multiplication instead of division (faster: ~3-4 cycles vs ~15-20 cycles)
	nakInterval := (rttVal + 4*rttVarVal) * 0.5

	// Use configured minimum NAK interval (from Config.PeriodicNakIntervalMs)
	minNakInterval := float64(r.minNakIntervalUs.Load())
	if nakInterval < minNakInterval {
		nakInterval = minNakInterval
	}
	return nakInterval
}

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
	c.recv = live.NewReceiver(live.ReceiveConfig{
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

func (c *srtConn) ReadPacket() (packet.Packet, error) {
	var p packet.Packet
	select {
	case <-c.ctx.Done():
		return nil, io.EOF
	case p = <-c.readQueue:
	}

	if p.Header().PacketSequenceNumber.Gt(c.debug.expectedReadPacketSequenceNumber) {
		c.log("connection:error", func() string {
			return fmt.Sprintf("lost packets. got: %d, expected: %d (%d)", p.Header().PacketSequenceNumber.Val(), c.debug.expectedReadPacketSequenceNumber.Val(), c.debug.expectedReadPacketSequenceNumber.Distance(p.Header().PacketSequenceNumber))
		})
	} else if p.Header().PacketSequenceNumber.Lt(c.debug.expectedReadPacketSequenceNumber) {
		c.log("connection:error", func() string {
			return fmt.Sprintf("packet out of order. got: %d, expected: %d (%d)", p.Header().PacketSequenceNumber.Val(), c.debug.expectedReadPacketSequenceNumber.Val(), c.debug.expectedReadPacketSequenceNumber.Distance(p.Header().PacketSequenceNumber))
		})
		return nil, io.EOF
	}

	c.debug.expectedReadPacketSequenceNumber = p.Header().PacketSequenceNumber.Inc()

	return p, nil
}

func (c *srtConn) Read(b []byte) (int, error) {
	if c.readBuffer.Len() != 0 {
		return c.readBuffer.Read(b)
	}

	c.readBuffer.Reset()

	p, err := c.ReadPacket()
	if err != nil {
		return 0, err
	}

	c.readBuffer.Write(p.Data())

	// The packet is out of congestion control and written to the read buffer
	p.Decommission()

	return c.readBuffer.Read(b)
}

// WritePacket writes a packet to the write queue. Packets on the write queue
// will be sent to the peer of the connection. Only data packets will be sent.
func (c *srtConn) WritePacket(p packet.Packet) error {
	if p.Header().IsControlPacket {
		// Ignore control packets
		return nil
	}

	_, err := c.Write(p.Data())
	if err != nil {
		return err
	}

	return nil
}

func (c *srtConn) Write(b []byte) (int, error) {
	c.writeBuffer.Write(b)

	for {
		n, err := c.writeBuffer.Read(c.writeData)
		if err != nil {
			return 0, err
		}

		p := packet.NewPacket(nil)

		p.SetData(c.writeData[:n])

		p.Header().IsControlPacket = false
		// Give the packet a deliver timestamp
		p.Header().PktTsbpdTime = c.getTimestamp()

		// Non-blocking write to the write queue
		select {
		case <-c.ctx.Done():
			return 0, io.EOF
		case c.writeQueue <- p:
		default:
			return 0, io.EOF
		}

		if c.writeBuffer.Len() == 0 {
			break
		}
	}

	c.writeBuffer.Reset()

	return len(b), nil
}

// push puts a packet on the network queue. This is where packets go that came in from the network.
func (c *srtConn) push(p packet.Packet) {
	// Non-blocking write to the network queue
	select {
	case <-c.ctx.Done():
	case c.networkQueue <- p:
	default:
		c.log("connection:error", func() string { return "network queue is full" })
	}
}

// getTimestamp returns the elapsed time since the start of the connection in microseconds.
func (c *srtConn) getTimestamp() uint64 {
	return uint64(time.Since(c.start).Microseconds())
}

// getTimestampForPacket returns the elapsed time since the start of the connection in
// microseconds clamped a 32bit value.
func (c *srtConn) getTimestampForPacket() uint32 {
	return uint32(c.getTimestamp() & uint64(packet.MAX_TIMESTAMP))
}

// pop adds the destination address and socketid to the packet and sends it out to the network.
// The packet will be encrypted if required.
func (c *srtConn) pop(p packet.Packet) {
	p.Header().Addr = c.remoteAddr
	p.Header().DestinationSocketId = c.peerSocketId

	if !p.Header().IsControlPacket {
		c.cryptoLock.Lock()
		if c.crypto != nil {
			p.Header().KeyBaseEncryptionFlag = c.keyBaseEncryption
			if !p.Header().RetransmittedPacketFlag {
				if err := c.crypto.EncryptOrDecryptPayload(p.Data(), p.Header().KeyBaseEncryptionFlag, p.Header().PacketSequenceNumber.Val()); err != nil {
					c.log("connection:send:error", func() string {
						return fmt.Sprintf("encryption failed: %v", err)
					})
					// Track error in metrics if available
					if c.metrics != nil {
						c.metrics.CryptoErrorEncrypt.Add(1)
						c.metrics.PktSentDataError.Add(1)
					}
				}
			}

			c.kmPreAnnounceCountdown--
			c.kmRefreshCountdown--

			if c.kmPreAnnounceCountdown == 0 && !c.kmConfirmed {
				c.sendKMRequest(c.keyBaseEncryption.Opposite())

				// Resend the request until we get a response
				c.kmPreAnnounceCountdown = c.config.KMPreAnnounce/10 + 1
			}

			if c.kmRefreshCountdown == 0 {
				c.kmPreAnnounceCountdown = c.config.KMRefreshRate - c.config.KMPreAnnounce
				c.kmRefreshCountdown = c.config.KMRefreshRate

				// Switch the keys
				c.keyBaseEncryption = c.keyBaseEncryption.Opposite()

				c.kmConfirmed = false
			}

			if c.kmRefreshCountdown == c.config.KMRefreshRate-c.config.KMPreAnnounce {
				// Decommission the previous key, resp. create a new SEK that will
				// be used in the next switch.
				if err := c.crypto.GenerateSEK(c.keyBaseEncryption.Opposite()); err != nil {
					c.log("connection:crypto:error", func() string {
						return fmt.Sprintf("failed to generate SEK: %v", err)
					})
					// Track error in metrics if available
					if c.metrics != nil {
						c.metrics.CryptoErrorGenerateSEK.Add(1)
					}
				}
			}
		}
		c.cryptoLock.Unlock()

		c.log("data:send:dump", func() string { return p.Dump() })
	}

	// Check optional send filter (for testing packet drops)
	if c.sendFilter != nil && !c.sendFilter(p) {
		return // Filter returned false - drop packet
	}

	// Send the packet on the wire
	c.onSend(p)
}

// networkQueueReader reads the packets from the network queue in order to process them.
func (c *srtConn) networkQueueReader(ctx context.Context) {
	defer func() {
		c.log("connection:close", func() string { return "left network queue reader loop" })
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-c.networkQueue:
			c.handlePacket(p)
		}
	}
}

// writeQueueReader reads the packets from the write queue and puts them into congestion
// control for sending.
func (c *srtConn) writeQueueReader(ctx context.Context) {
	defer func() {
		c.log("connection:close", func() string { return "left write queue reader loop" })
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-c.writeQueue:
			// Put the packet into the send congestion control
			c.snd.Push(p)
		}
	}
}

// deliver writes the packets to the read queue in order to be consumed by the Read function.
func (c *srtConn) deliver(p packet.Packet) {
	// Non-blocking write to the read queue
	select {
	case <-c.ctx.Done():
	case c.readQueue <- p:
	default:
		c.log("connection:error", func() string { return "readQueue was blocking, dropping packet" })
	}
}

// handlePacket checks the packet header. If it is a control packet it will forwarded to the
// respective handler. If it is a data packet it will be put into congestion control for
// receiving. The packet will be decrypted if required.
// handlePacketDirect is called directly from io_uring completion handler
// It uses a per-connection mutex to ensure sequential processing (same guarantee as channel-based approach)
// The mutex is blocking to ensure no packets are dropped (never drop packets that successfully arrived from network)
func (c *srtConn) handlePacketDirect(p packet.Packet) {
	// Block until mutex available - never drop packets
	// Measure lock timing for debugging and performance monitoring
	if c.metrics != nil && c.metrics.HandlePacketLockTiming != nil {
		waitStart := time.Now()
		c.handlePacketMutex.Lock()
		waitDuration := time.Since(waitStart)

		if waitDuration > 0 {
			c.metrics.HandlePacketLockTiming.RecordWaitTime(waitDuration)
		}
		// Note: RecordHoldTime will increment holdTimeIndex, which serves as acquisition counter

		defer func() {
			holdDuration := time.Since(waitStart)                         // Total time from lock acquisition
			c.metrics.HandlePacketLockTiming.RecordHoldTime(holdDuration) // This increments holdTimeIndex
			c.handlePacketMutex.Unlock()
		}()

		c.handlePacket(p)
	} else {
		// Fallback if metrics not initialized (shouldn't happen in normal operation)
		c.handlePacketMutex.Lock()
		defer c.handlePacketMutex.Unlock()
		c.handlePacket(p)
	}
}

// initializeControlHandlers initializes the control packet dispatch tables.
// This is called once during connection initialization and the maps are never modified,
// so no locking is required for map access.
func (c *srtConn) initializeControlHandlers() {
	// Main control type handlers
	c.controlHandlers = map[packet.CtrlType]controlPacketHandler{
		packet.CTRLTYPE_KEEPALIVE: (*srtConn).handleKeepAlive,
		packet.CTRLTYPE_SHUTDOWN:  (*srtConn).handleShutdown,
		packet.CTRLTYPE_NAK:       (*srtConn).handleNAK,
		packet.CTRLTYPE_ACK:       (*srtConn).handleACK,
		packet.CTRLTYPE_ACKACK:    (*srtConn).handleACKACK,
		packet.CTRLTYPE_USER:      (*srtConn).handleUserPacket, // Special handler for SubType dispatch
	}

	// CTRLTYPE_USER SubType handlers
	c.userHandlers = map[packet.CtrlSubType]userPacketHandler{
		packet.EXTTYPE_HSREQ: (*srtConn).handleHSRequest,
		packet.EXTTYPE_HSRSP: (*srtConn).handleHSResponse,
		packet.EXTTYPE_KMREQ: (*srtConn).handleKMRequest,
		packet.EXTTYPE_KMRSP: (*srtConn).handleKMResponse,
	}
}

// handleUserPacket dispatches CTRLTYPE_USER packets based on SubType
func (c *srtConn) handleUserPacket(p packet.Packet) {
	header := p.Header()

	c.log("connection:recv:ctrl:user", func() string {
		return fmt.Sprintf("got CTRLTYPE_USER packet, subType: %s", header.SubType)
	})

	// Lookup SubType handler
	handler, ok := c.userHandlers[header.SubType]
	if !ok {
		// Unknown SubType - log and return gracefully
		c.log("connection:recv:ctrl:user:unknown", func() string {
			return fmt.Sprintf("unknown CTRLTYPE_USER SubType: %s", header.SubType)
		})
		return
	}

	// Call SubType handler
	handler(c, p)
}

// handlePacket receives and processes a packet. For control packets, it uses
// a dispatch table for O(1) lookup. The packet will be decrypted if required.
func (c *srtConn) handlePacket(p packet.Packet) {
	if p == nil {
		return
	}

	c.resetPeerIdleTimeout()

	header := p.Header()

	if header.IsControlPacket {
		// O(1) lookup in dispatch table (no locking needed - map is immutable)
		handler, ok := c.controlHandlers[header.ControlType]
		if !ok {
			// Unknown control type - log and return gracefully
			c.log("connection:recv:ctrl:unknown", func() string {
				return fmt.Sprintf("unknown control packet type: %s", header.ControlType)
			})
			// Track drop for unknown control type
			if c.metrics != nil {
				// Classify as generic error (unknown control type)
				c.metrics.PktRecvErrorParse.Add(1)
			}
			return
		}

		// Call handler
		handler(c, p)
		return
	}

	if header.PacketSequenceNumber.Gt(c.debug.expectedRcvPacketSequenceNumber) {
		c.log("connection:error", func() string {
			return fmt.Sprintf("recv lost packets. got: %d, expected: %d (%d)\n", header.PacketSequenceNumber.Val(), c.debug.expectedRcvPacketSequenceNumber.Val(), c.debug.expectedRcvPacketSequenceNumber.Distance(header.PacketSequenceNumber))
		})
	}

	c.debug.expectedRcvPacketSequenceNumber = header.PacketSequenceNumber.Inc()

	//fmt.Printf("%s\n", p.String())

	// Ignore FEC filter control packets
	// https://github.com/Haivision/srt/blob/master/docs/features/packet-filtering-and-fec.md
	// "An FEC control packet is distinguished from a regular data packet by having
	// its message number equal to 0. This value isn't normally used in SRT (message
	// numbers start from 1, increment to a maximum, and then roll back to 1)."
	if header.MessageNumber == 0 {
		c.log("connection:filter", func() string { return "dropped FEC filter control packet" })
		// Track drop for FEC filter packet
		if c.metrics != nil {
			c.metrics.PktRecvDataDropped.Add(1)
			c.metrics.ByteRecvDataDropped.Add(uint64(p.Len()))
		}
		return
	}

	// 4.5.1.1.  TSBPD Time Base Calculation
	if !c.tsbpdWrapPeriod {
		if header.Timestamp > packet.MAX_TIMESTAMP-(30*1000000) {
			c.tsbpdWrapPeriod = true
			c.log("connection:tsbpd", func() string { return "TSBPD wrapping period started" })
		}
	} else {
		if header.Timestamp >= (30*1000000) && header.Timestamp <= (60*1000000) {
			c.tsbpdWrapPeriod = false
			c.tsbpdTimeBaseOffset += uint64(packet.MAX_TIMESTAMP) + 1
			c.log("connection:tsbpd", func() string { return "TSBPD wrapping period finished" })
		}
	}

	tsbpdTimeBaseOffset := c.tsbpdTimeBaseOffset
	if c.tsbpdWrapPeriod {
		if header.Timestamp < (30 * 1000000) {
			tsbpdTimeBaseOffset += uint64(packet.MAX_TIMESTAMP) + 1
		}
	}

	header.PktTsbpdTime = c.tsbpdTimeBase + tsbpdTimeBaseOffset + uint64(header.Timestamp) + c.tsbpdDelay + c.tsbpdDrift

	c.log("data:recv:dump", func() string { return p.Dump() })

	c.cryptoLock.Lock()
	if c.crypto != nil {
		if header.KeyBaseEncryptionFlag != 0 {
			if err := c.crypto.EncryptOrDecryptPayload(p.Data(), header.KeyBaseEncryptionFlag, header.PacketSequenceNumber.Val()); err != nil {
				if c.metrics != nil {
					c.metrics.PktRecvUndecrypt.Add(1)
					c.metrics.ByteRecvUndecrypt.Add(uint64(p.Len()))
				}
			}
		} else {
			if c.metrics != nil {
				c.metrics.PktRecvUndecrypt.Add(1)
				c.metrics.ByteRecvUndecrypt.Add(uint64(p.Len()))
			}
		}
	}
	c.cryptoLock.Unlock()

	// Put the packet into receive congestion control
	c.recv.Push(p)
}

// handleKeepAlive resets the idle timeout and sends a keepalive to the peer.
func (c *srtConn) handleKeepAlive(p packet.Packet) {
	c.log("control:recv:keepalive:dump", func() string { return p.Dump() })

	// Note: Keepalive metrics are tracked via packet classifier in send/recv paths
	// No need to increment here - metrics already tracked

	c.resetPeerIdleTimeout()

	c.log("control:send:keepalive:dump", func() string { return p.Dump() })

	c.pop(p)
}

// sendProactiveKeepalive sends a keepalive packet to keep the connection alive.
// This is used when no data has been received for a while to prevent idle timeout.
func (c *srtConn) sendProactiveKeepalive() {
	p := packet.NewPacket(c.remoteAddr)
	p.Header().IsControlPacket = true
	p.Header().ControlType = packet.CTRLTYPE_KEEPALIVE
	p.Header().TypeSpecific = 0
	p.Header().Timestamp = c.getTimestampForPacket()
	p.Header().DestinationSocketId = c.peerSocketId

	c.log("control:send:keepalive:proactive", func() string {
		return "sending proactive keepalive to maintain connection"
	})

	// Note: Keepalive metrics are tracked via packet classifier in send path
	// No need to increment here - metrics already tracked

	c.pop(p)
}

// getKeepaliveInterval calculates the keepalive interval based on config.
// Returns 0 if proactive keepalives are disabled.
func (c *srtConn) getKeepaliveInterval() time.Duration {
	threshold := c.config.KeepaliveThreshold
	if threshold <= 0 || threshold >= 1.0 {
		return 0 // Disabled or invalid
	}
	return time.Duration(float64(c.config.PeerIdleTimeout) * threshold)
}

// handleShutdown closes the connection
func (c *srtConn) handleShutdown(p packet.Packet) {
	c.log("control:recv:shutdown:dump", func() string { return p.Dump() })

	// Note: Shutdown metrics are tracked via packet classifier in recv path
	// No need to increment here - metrics already tracked

	c.log("connection:close:reason", func() string {
		return "shutdown packet received from peer"
	})
	go c.close(metrics.CloseReasonGraceful)
}

// handleACK forwards the acknowledge sequence number to the congestion control and
// returns a ACKACK (on a full ACK). The RTT is also updated in case of a full ACK.
func (c *srtConn) handleACK(p packet.Packet) {
	c.log("control:recv:ACK:dump", func() string { return p.Dump() })

	// Note: ACK metrics are tracked via packet classifier in recv path
	// No need to increment here - metrics already tracked

	cif := &packet.CIFACK{}

	if err := p.UnmarshalCIF(cif); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:ACK:error", func() string { return fmt.Sprintf("invalid ACK: %s", err) })
		return
	}

	c.log("control:recv:ACK:cif", func() string { return cif.String() })

	// Phase 5: Track Light vs Full ACK received
	if c.metrics != nil {
		if cif.IsLite {
			c.metrics.PktRecvACKLiteSuccess.Add(1)
		} else if !cif.IsSmall {
			c.metrics.PktRecvACKFullSuccess.Add(1)
		}
		// Note: Small ACKs are not separately tracked
	}

	c.snd.ACK(cif.LastACKPacketSequenceNumber)

	if !cif.IsLite && !cif.IsSmall {
		// 4.10.  Round-Trip Time Estimation
		c.recalculateRTT(time.Duration(int64(cif.RTT)) * time.Microsecond)

		// Estimated Link Capacity (from packets/s to Mbps)
		// Store as uint64 (Mbps * 1000) for atomic operations
		if c.metrics != nil {
			mbps := float64(cif.EstimatedLinkCapacity) * MAX_PAYLOAD_SIZE * 8 / 1024 / 1024
			c.metrics.MbpsLinkCapacity.Store(uint64(mbps * 1000))
		}

		c.sendACKACK(p.Header().TypeSpecific)
	}
}

// handleNAK forwards the lost sequence number to the congestion control.
func (c *srtConn) handleNAK(p packet.Packet) {
	c.log("control:recv:NAK:dump", func() string { return p.Dump() })

	// Note: NAK recv metrics are tracked via packet classifier in IncrementRecvMetrics
	// The packet classifier is called by listen.go/dial.go when packets are received.
	// No need to increment here - already tracked in packet_classifier.go

	cif := &packet.CIFNAK{}

	if err := p.UnmarshalCIF(cif); err != nil {
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
		}
		c.log("control:recv:NAK:error", func() string { return fmt.Sprintf("invalid NAK: %s", err) })
		return
	}

	c.log("control:recv:NAK:cif", func() string { return cif.String() })

	// Inform congestion control about lost packets and track retransmissions
	retransCount := c.snd.NAK(cif.LostPacketSequenceNumber)
	if retransCount > 0 {
		if c.metrics != nil {
			c.metrics.PktRetransFromNAK.Add(uint64(retransCount))
		}
	}
}

// ExtendedStatistics contains statistics that are not part of the standard SRT Statistics struct.
// These are retrieved in a single call to minimize lock contention.
type ExtendedStatistics struct {
	PktSentACKACK     uint64 // Number of ACKACK packets sent
	PktRecvACKACK     uint64 // Number of ACKACK packets received
	PktRetransFromNAK uint64 // Number of packets retransmitted in response to NAKs
}

// GetExtendedStatistics returns all extended statistics in a single call with a single lock.
// This implements the Conn interface.
func (c *srtConn) GetExtendedStatistics() *ExtendedStatistics {
	if c.metrics == nil {
		return &ExtendedStatistics{}
	}
	return &ExtendedStatistics{
		PktSentACKACK:     c.metrics.PktSentACKACKSuccess.Load(),
		PktRecvACKACK:     c.metrics.PktRecvACKACKSuccess.Load(),
		PktRetransFromNAK: c.metrics.PktRetransFromNAK.Load(),
	}
}

// handleACKACK updates the RTT and NAK interval for the congestion control.
//
// RFC: https://datatracker.ietf.org/doc/html/draft-sharabayko-srt-01#section-3.2.5
//
// ACKACK is sent by the sender in response to a Full ACK (not Light ACK).
// It echoes the ACK Number (TypeSpecific field) from the original ACK.
//
// RTT Calculation (RFC Section 4.10):
//
//	RTT = time_now - time_when_ack_was_sent
//	Where time_when_ack_was_sent is stored in ackNumbers[ACK_Number]
//
// The receiver uses EWMA (Exponential Weighted Moving Average) smoothing:
//
//	RTT = RTT * 0.875 + lastRTT * 0.125
//	RTTVar = RTTVar * 0.75 + |RTT - lastRTT| * 0.25
//
// NAK interval is derived from RTT:
//
//	NAKInterval = (RTT + 4*RTTVar) / 2   (minimum 20ms)
//
// Cleanup: Entries in ackNumbers older than the current ACKACK are deleted
// to prevent unbounded map growth from lost ACKACKs.
func (c *srtConn) handleACKACK(p packet.Packet) {
	c.log("control:recv:ACKACK:dump", func() string { return p.Dump() })

	// Note: ACKACK metrics are tracked via packet classifier in recv path
	// No need to increment here - metrics already tracked

	ackNum := p.Header().TypeSpecific

	// ACK-5: Use btree for O(log n) lookup
	now := time.Now()
	c.ackLock.Lock()
	entry := c.ackNumbers.Get(ackNum)
	btreeLen := c.ackNumbers.Len()
	if entry != nil {
		// 4.10.  Round-Trip Time Estimation
		rttDuration := now.Sub(entry.timestamp)
		c.recalculateRTT(rttDuration)

		// DEBUG: Track ACKACK RTT calculation
		c.log("control:recv:ACKACK:rtt:debug", func() string {
			return fmt.Sprintf("ACKACK RTT: ackNum=%d, entryTimestamp=%s, now=%s, rtt=%v, btreeLen=%d",
				ackNum, entry.timestamp.Format("15:04:05.000000"), now.Format("15:04:05.000000"),
				rttDuration, btreeLen)
		})

		c.ackNumbers.Delete(ackNum)
		PutAckEntry(entry) // Return to pool
	} else {
		c.log("control:recv:ACKACK:error", func() string { return fmt.Sprintf("got unknown ACKACK (%d), btreeLen=%d", ackNum, btreeLen) })
		if c.metrics != nil {
			c.metrics.PktRecvInvalid.Add(1)
			c.metrics.AckBtreeUnknownACKACK.Add(1) // Phase 4: Track unknown ACKACK specifically
		}
	}

	// ACK-5: Use ExpireOlderThan for efficient bulk cleanup
	// Remove all entries with ACK number < current (they're stale)
	expiredCount, expired := c.ackNumbers.ExpireOlderThan(ackNum)
	btreeLenAfter := c.ackNumbers.Len()
	c.ackLock.Unlock()

	// Update ACK btree metrics (outside lock)
	if c.metrics != nil {
		c.metrics.AckBtreeEntriesExpired.Add(uint64(expiredCount))
		c.metrics.AckBtreeSize.Store(uint64(btreeLenAfter))
	}

	// Return expired entries to pool (outside lock)
	PutAckEntries(expired)

	c.recv.SetNAKInterval(uint64(c.rtt.NAKInterval()))
}

// recalculateRTT recalculates the RTT based on a full ACK exchange
func (c *srtConn) recalculateRTT(rtt time.Duration) {
	c.rtt.Recalculate(rtt)

	// Update RTT metrics (Phase 4: ACK/ACKACK Redesign)
	if c.metrics != nil {
		c.metrics.RTTMicroseconds.Store(uint64(c.rtt.RTT()))
		c.metrics.RTTVarMicroseconds.Store(uint64(c.rtt.RTTVar()))
	}

	c.log("connection:rtt", func() string {
		return fmt.Sprintf("RTT=%.0fus RTTVar=%.0fus NAKInterval=%.0fms", c.rtt.RTT(), c.rtt.RTTVar(), c.rtt.NAKInterval()/1000)
	})
}

// getNextACKNumber returns the next ACK number using atomic CAS.
// ACK numbers are monotonically increasing 32-bit counters, starting at 1.
// Value 0 is reserved for Light ACKs (which don't trigger ACKACK).
//
// This is lock-free and safe for concurrent use.
// Reference: ack_optimization_implementation.md → "### Improvement #2: Atomic nextACKNumber with CAS"
func (c *srtConn) getNextACKNumber() uint32 {
	for {
		current := c.nextACKNumber.Load()
		next := current + 1
		if next == 0 {
			next = 1 // Skip 0 (reserved for Light ACK)
		}
		if c.nextACKNumber.CompareAndSwap(current, next) {
			return current
		}
		// CAS failed, another goroutine incremented - retry
	}
}

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

// Close closes the connection.
func (c *srtConn) Close() error {
	c.log("connection:close:reason", func() string {
		return "application requested close"
	})
	c.close(metrics.CloseReasonGraceful)

	return nil
}

// GetPeerIdleTimeoutRemaining returns the remaining time until the peer idle timeout fires.
// Returns 0 if the timer is not active or has already fired.
// This implements the Conn interface.
func (c *srtConn) GetPeerIdleTimeoutRemaining() time.Duration {
	// Calculate remaining time based on when it was last reset (atomic read)
	lastResetNano := c.peerIdleTimeoutLastReset.Load()
	if lastResetNano == 0 {
		return 0
	}
	lastReset := time.Unix(0, lastResetNano)
	elapsed := time.Since(lastReset)
	remaining := c.config.PeerIdleTimeout - elapsed

	if remaining < 0 {
		return 0
	}
	return remaining
}

// resetPeerIdleTimeout resets the peer idle timeout timer (hot path - lock-free)
func (c *srtConn) resetPeerIdleTimeout() {
	// No lock needed - timer.Reset() and atomic store are thread-safe and lock-free
	c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
	c.peerIdleTimeoutLastReset.Store(time.Now().UnixNano())
}

// getTotalReceivedPackets returns total received packets (atomic read)
// This counts all packets that successfully reached the connection, indicating peer is alive
func (c *srtConn) getTotalReceivedPackets() uint64 {
	if c.metrics == nil {
		return 0
	}
	// Single atomic load - much faster than summing 8 counters
	return c.metrics.PktRecvSuccess.Load()
}

// watchPeerIdleTimeout watches for timeout using atomic counter checks
func (c *srtConn) watchPeerIdleTimeout() {

	// Get initial packet count
	initialCount := c.getTotalReceivedPackets()

	// Determine ticker interval based on timeout duration
	// For longer timeouts (>6s), check more frequently (1/4) for better responsiveness
	// For shorter timeouts (<=6s), check at 1/2 interval
	tickerInterval := c.config.PeerIdleTimeout / 2
	if c.config.PeerIdleTimeout > 6*time.Second {
		tickerInterval = c.config.PeerIdleTimeout / 4
	}
	ticker := time.NewTicker(tickerInterval)
	defer ticker.Stop()

	// Proactive keepalive ticker (if enabled)
	// Sends keepalive when connection is idle to prevent timeout
	keepaliveInterval := c.getKeepaliveInterval()
	var keepaliveTicker *time.Ticker
	var keepaliveChan <-chan time.Time
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		keepaliveChan = keepaliveTicker.C
		defer keepaliveTicker.Stop()
	}

	for {
		select {
		case <-c.peerIdleTimeout.C:
			// Timer expired - check if packets were received
			currentCount := c.getTotalReceivedPackets()
			if currentCount == initialCount {
				// No packets received - timeout occurred
				c.log("connection:close:reason", func() string {
					return fmt.Sprintf("peer idle timeout: no data received from peer for %s", c.config.PeerIdleTimeout)
				})
				c.log("connection:close", func() string {
					return fmt.Sprintf("no more data received from peer for %s. shutting down", c.config.PeerIdleTimeout)
				})
				go c.close(metrics.CloseReasonPeerIdle)
				return
			}
			// Packets were received - will reset timer after select

		case <-ticker.C:
			// Periodic check (1/2 timeout for <=6s, 1/4 timeout for >6s)
			// Will check counter and reset if needed after select

		case <-keepaliveChan:
			// Proactive keepalive: send if no recent activity to prevent timeout
			currentCount := c.getTotalReceivedPackets()
			if currentCount == initialCount {
				// No packets received since last check - send keepalive
				c.sendProactiveKeepalive()
			}
			// Note: We don't update initialCount here - that happens in the common logic below

		case <-c.ctx.Done():
			// Connection closing
			return
		}

		// Check if packets were received (common logic for both timer and ticker)
		// This is executed after the select, making the code more DRY and Go-idiomatic
		currentCount := c.getTotalReceivedPackets()
		if currentCount > initialCount {
			// Packets received - reset timer and update count
			initialCount = currentCount
			c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
			c.peerIdleTimeoutLastReset.Store(time.Now().UnixNano())
		}
	}
}

// close closes the connection with the specified reason.
// The reason is used for metrics tracking to identify why connections were closed.
func (c *srtConn) close(reason metrics.CloseReason) {

	c.shutdownOnce.Do(func() {
		// Unregister from metrics registry with close reason
		metrics.UnregisterConnection(c.socketId, reason)

		// Print statistics before closing (if logger is available)
		if c.logger != nil {
			c.printCloseStatistics()
		}

		c.log("connection:close", func() string { return "stopping peer idle timeout" })

		// Stop peer idle timeout timer
		if c.peerIdleTimeout != nil {
			c.peerIdleTimeout.Stop()
		}

		c.log("connection:close", func() string { return "sending shutdown message to peer" })

		c.sendShutdown()

		c.log("connection:close", func() string { return "stopping all routines and channels" })

		// Cancel connection context to signal all goroutines to exit
		c.cancelCtx()

		// Wait for all connection goroutines to finish (with timeout)
		c.log("connection:close", func() string { return "waiting for connection goroutines" })
		done := make(chan struct{})
		go func() {
			c.connWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// All connection goroutines finished
		case <-time.After(5 * time.Second):
			c.log("connection:close:warning", func() string {
				return "timeout waiting for connection goroutines"
			})
		}

		// Clean up io_uring resources if enabled (Linux-specific)
		// cleanupIoUring() handles: cancellation, QueueExit (to wake blocked WaitCQE),
		// waiting for completion handler, draining completions, buffer cleanup
		c.cleanupIoUring()

		c.log("connection:close", func() string { return "flushing congestion" })

		c.snd.Flush()
		c.recv.Flush()

		c.log("connection:close", func() string { return "shutdown" })

		go func() {
			c.onShutdown(c.socketId)
		}()

		// Notify parent waitgroup that this connection has shut down
		if c.shutdownWg != nil {
			c.shutdownWg.Done()
		}
	})
}

// drainCompletions and sendCompletionHandler are defined in connection_linux.go

func (c *srtConn) log(topic string, message func() string) {
	c.logger.Print(topic, c.socketId, 2, message)
}

// printCloseStatistics prints connection statistics in JSON format when the connection closes.
// This is called from close() before the connection is fully shut down.
func (c *srtConn) printCloseStatistics() {
	stats := &Statistics{}
	c.Stats(stats)

	remoteAddr := "unknown"
	if c.remoteAddr != nil {
		remoteAddr = c.remoteAddr.String()
	}

	// Get extended statistics
	extStats := c.GetExtendedStatistics()

	// Calculate retransmit percentage
	var retransPercent *float64
	if stats.Accumulated.PktSent > 0 {
		percent := (float64(stats.Accumulated.PktRetrans) / float64(stats.Accumulated.PktSent)) * 100.0
		retransPercent = &percent
	}

	// Get remaining peer idle timeout
	remainingTimeout := c.GetPeerIdleTimeoutRemaining()
	remainingSeconds := float64(remainingTimeout.Seconds())

	// Build JSON output
	output := map[string]interface{}{
		"timestamp":                           time.Now().Format(time.RFC3339Nano),
		"event":                               "connection_closed",
		"instance":                            c.config.InstanceName,
		"socket_id":                           fmt.Sprintf("0x%08x", c.socketId),
		"remote_addr":                         remoteAddr,
		"connection_duration":                 time.Since(c.start).String(),
		"peer_idle_timeout_remaining_seconds": remainingSeconds,
		"accumulated": map[string]interface{}{
			"pkt_sent_data":         stats.Accumulated.PktSent,
			"pkt_recv_data":         stats.Accumulated.PktRecv,
			"pkt_sent_ack":          stats.Accumulated.PktSentACK,
			"pkt_recv_ack":          stats.Accumulated.PktRecvACK,
			"pkt_sent_nak":          stats.Accumulated.PktSentNAK,
			"pkt_recv_nak":          stats.Accumulated.PktRecvNAK,
			"pkt_retrans_total":     stats.Accumulated.PktRetrans,
			"pkt_recv_loss":         stats.Accumulated.PktRecvLoss,
			"pkt_recv_retrans_rate": stats.Instantaneous.PktRecvRetransRate,
		},
		"instantaneous": map[string]interface{}{
			"mbps_sent_rate": stats.Instantaneous.MbpsSentRate,
			"mbps_recv_rate": stats.Instantaneous.MbpsRecvRate,
			"ms_rtt":         stats.Instantaneous.MsRTT,
		},
	}

	if extStats != nil {
		output["accumulated"].(map[string]interface{})["pkt_sent_ackack"] = extStats.PktSentACKACK
		output["accumulated"].(map[string]interface{})["pkt_recv_ackack"] = extStats.PktRecvACKACK
		output["accumulated"].(map[string]interface{})["pkt_retrans_from_nak"] = extStats.PktRetransFromNAK
	}

	if retransPercent != nil {
		output["accumulated"].(map[string]interface{})["pkt_retrans_percent"] = *retransPercent
	}

	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		c.log("connection:close:error", func() string {
			return fmt.Sprintf("failed to encode close statistics: %v", err)
		})
		return
	}

	fmt.Fprintf(os.Stderr, "\n%s\n", string(jsonData))
}

func (c *srtConn) SetDeadline(t time.Time) error      { return nil }
func (c *srtConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *srtConn) SetWriteDeadline(t time.Time) error { return nil }

func (c *srtConn) Stats(s *Statistics) {
	if s == nil {
		return
	}

	now := uint64(time.Since(c.start).Milliseconds())

	// Read from atomic counters directly (lock-free)
	// Still call Stats() to update instantaneous values (MsBuf, bandwidth, etc.)
	send := c.snd.Stats()
	recv := c.recv.Stats()

	previous := s.Accumulated
	interval := now - s.MsTimeStamp

	// Read from atomic counters (no lock needed)
	if c.metrics == nil {
		// Fallback if metrics not initialized (shouldn't happen)
		return
	}

	headerSize := c.metrics.HeaderSize.Load()

	// Accumulated - read directly from atomic counters (lock-free)
	s.Accumulated = StatisticsAccumulated{
		PktSent:        c.metrics.CongestionSendPkt.Load(),
		PktRecv:        c.metrics.CongestionRecvPkt.Load(),
		PktSentUnique:  c.metrics.CongestionSendPktUnique.Load(),
		PktRecvUnique:  c.metrics.CongestionRecvPktUnique.Load(),
		PktSendLoss:    c.metrics.CongestionSendPktLoss.Load(),
		PktRecvLoss:    c.metrics.CongestionRecvPktLoss.Load(),
		PktRetrans:     c.metrics.CongestionSendPktRetrans.Load(),
		PktRecvRetrans: c.metrics.CongestionRecvPktRetrans.Load(),
		PktSentACK:     c.metrics.PktSentACKSuccess.Load(),
		PktRecvACK:     c.metrics.PktRecvACKSuccess.Load(),
		PktSentNAK:     c.metrics.PktSentNAKSuccess.Load(),
		PktRecvNAK:     c.metrics.PktRecvNAKSuccess.Load(),
		PktSentKM:      c.metrics.PktSentKMSuccess.Load(),
		PktRecvKM:      c.metrics.PktRecvKMSuccess.Load(),
		UsSndDuration:  c.metrics.CongestionSendUsSndDuration.Load(),
		PktSendDrop: c.metrics.CongestionSendDataDropTooOld.Load() +
			c.metrics.PktSentDataErrorMarshal.Load() +
			c.metrics.PktSentDataRingFull.Load() +
			c.metrics.PktSentDataErrorSubmit.Load() +
			c.metrics.PktSentDataErrorIoUring.Load(),
		PktRecvDrop: c.metrics.CongestionRecvDataDropTooOld.Load() +
			c.metrics.CongestionRecvDataDropAlreadyAcked.Load() +
			c.metrics.CongestionRecvDataDropDuplicate.Load() +
			c.metrics.CongestionRecvDataDropStoreInsertFailed.Load(),
		PktRecvUndecrypt:  c.metrics.PktRecvUndecrypt.Load(),
		ByteSent:          c.metrics.CongestionSendByte.Load() + (c.metrics.CongestionSendPkt.Load() * headerSize),
		ByteRecv:          c.metrics.CongestionRecvByte.Load() + (c.metrics.CongestionRecvPkt.Load() * headerSize),
		ByteSentUnique:    c.metrics.CongestionSendByteUnique.Load() + (c.metrics.CongestionSendPktUnique.Load() * headerSize),
		ByteRecvUnique:    c.metrics.CongestionRecvByteUnique.Load() + (c.metrics.CongestionRecvPktUnique.Load() * headerSize),
		ByteRecvLoss:      c.metrics.CongestionRecvByteLoss.Load() + (c.metrics.CongestionRecvPktLoss.Load() * headerSize),
		ByteRetrans:       c.metrics.CongestionSendByteRetrans.Load() + (c.metrics.CongestionSendPktRetrans.Load() * headerSize),
		ByteRecvRetrans:   c.metrics.CongestionRecvByteRetrans.Load() + (c.metrics.CongestionRecvPktRetrans.Load() * headerSize),
		ByteSendDrop:      c.metrics.CongestionSendByteDrop.Load() + (s.Accumulated.PktSendDrop * headerSize),
		ByteRecvDrop:      c.metrics.CongestionRecvByteDrop.Load() + (s.Accumulated.PktRecvDrop * headerSize),
		ByteRecvUndecrypt: c.metrics.ByteRecvUndecrypt.Load() + (c.metrics.PktRecvUndecrypt.Load() * headerSize),
	}

	// Interval
	s.Interval = StatisticsInterval{
		MsInterval:         interval,
		PktSent:            s.Accumulated.PktSent - previous.PktSent,
		PktRecv:            s.Accumulated.PktRecv - previous.PktRecv,
		PktSentUnique:      s.Accumulated.PktSentUnique - previous.PktSentUnique,
		PktRecvUnique:      s.Accumulated.PktRecvUnique - previous.PktRecvUnique,
		PktSendLoss:        s.Accumulated.PktSendLoss - previous.PktSendLoss,
		PktRecvLoss:        s.Accumulated.PktRecvLoss - previous.PktRecvLoss,
		PktRetrans:         s.Accumulated.PktRetrans - previous.PktRetrans,
		PktRecvRetrans:     s.Accumulated.PktRecvRetrans - previous.PktRecvRetrans,
		PktSentACK:         s.Accumulated.PktSentACK - previous.PktSentACK,
		PktRecvACK:         s.Accumulated.PktRecvACK - previous.PktRecvACK,
		PktSentNAK:         s.Accumulated.PktSentNAK - previous.PktSentNAK,
		PktRecvNAK:         s.Accumulated.PktRecvNAK - previous.PktRecvNAK,
		MbpsSendRate:       float64(s.Accumulated.ByteSent-previous.ByteSent) * 8 / 1024 / 1024 / (float64(interval) / 1000),
		MbpsRecvRate:       float64(s.Accumulated.ByteRecv-previous.ByteRecv) * 8 / 1024 / 1024 / (float64(interval) / 1000),
		UsSndDuration:      s.Accumulated.UsSndDuration - previous.UsSndDuration,
		PktReorderDistance: 0,
		PktRecvBelated:     s.Accumulated.PktRecvBelated - previous.PktRecvBelated,
		PktSndDrop:         s.Accumulated.PktSendDrop - previous.PktSendDrop,
		PktRecvDrop:        s.Accumulated.PktRecvDrop - previous.PktRecvDrop,
		PktRecvUndecrypt:   s.Accumulated.PktRecvUndecrypt - previous.PktRecvUndecrypt,
		ByteSent:           s.Accumulated.ByteSent - previous.ByteSent,
		ByteRecv:           s.Accumulated.ByteRecv - previous.ByteRecv,
		ByteSentUnique:     s.Accumulated.ByteSentUnique - previous.ByteSentUnique,
		ByteRecvUnique:     s.Accumulated.ByteRecvUnique - previous.ByteRecvUnique,
		ByteRecvLoss:       s.Accumulated.ByteRecvLoss - previous.ByteRecvLoss,
		ByteRetrans:        s.Accumulated.ByteRetrans - previous.ByteRetrans,
		ByteRecvRetrans:    s.Accumulated.ByteRecvRetrans - previous.ByteRecvRetrans,
		ByteRecvBelated:    s.Accumulated.ByteRecvBelated - previous.ByteRecvBelated,
		ByteSendDrop:       s.Accumulated.ByteSendDrop - previous.ByteSendDrop,
		ByteRecvDrop:       s.Accumulated.ByteRecvDrop - previous.ByteRecvDrop,
		ByteRecvUndecrypt:  s.Accumulated.ByteRecvUndecrypt - previous.ByteRecvUndecrypt,
	}

	// Instantaneous
	s.Instantaneous = StatisticsInstantaneous{
		UsPktSendPeriod:       send.UsPktSndPeriod,
		PktFlowWindow:         uint64(c.config.FC),
		PktFlightSize:         send.PktFlightSize,
		MsRTT:                 c.rtt.RTT() / 1000,
		MbpsSentRate:          send.MbpsEstimatedSentBandwidth,
		MbpsRecvRate:          recv.MbpsEstimatedRecvBandwidth,
		MbpsLinkCapacity:      recv.MbpsEstimatedLinkCapacity,
		ByteAvailSendBuf:      0, // unlimited
		ByteAvailRecvBuf:      0, // unlimited
		MbpsMaxBW:             float64(c.config.MaxBW) / 1024 / 1024,
		ByteMSS:               uint64(c.config.MSS),
		PktSendBuf:            send.PktBuf,
		ByteSendBuf:           send.ByteBuf,
		MsSendBuf:             send.MsBuf,
		MsSendTsbPdDelay:      c.peerTsbpdDelay / 1000,
		PktRecvBuf:            recv.PktBuf,
		ByteRecvBuf:           recv.ByteBuf,
		MsRecvBuf:             recv.MsBuf,
		MsRecvTsbPdDelay:      c.tsbpdDelay / 1000,
		PktReorderTolerance:   uint64(c.config.LossMaxTTL),
		PktRecvAvgBelatedTime: 0,
		PktSendRetransRate:    send.PktRetransRate,
		PktRecvRetransRate:    recv.PktRetransRate,
	}

	// If we're only sending, the receiver congestion control value for the link capacity is zero,
	// use the value that we got from the receiver via the ACK packets.
	if s.Instantaneous.MbpsLinkCapacity == 0 {
		// Convert from uint64 (Mbps * 1000) back to float64 (Mbps)
		mbpsLinkCapacity := float64(c.metrics.MbpsLinkCapacity.Load()) / 1000.0
		s.Instantaneous.MbpsLinkCapacity = mbpsLinkCapacity
	}

	if c.config.MaxBW < 0 {
		s.Instantaneous.MbpsMaxBW = -1
	}

	s.MsTimeStamp = now
}
