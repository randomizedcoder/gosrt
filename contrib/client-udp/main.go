//go:build linux

// UDP-to-SRT Bridge using io_uring
//
// Receives UDP packets via io_uring and streams them to an SRT server.
// This bridges high-performance UDP input (e.g., from a video encoder)
// to an SRT output stream, using zero-copy buffer flow from io_uring
// completion to SRT write.
//
// Architecture:
//
//	UDP Source -> io_uring recv -> lock-free ring -> SRT Write() -> SRT Server
//
// The lock-free ring (go-lock-free-ring ShardedRing) decouples the io_uring
// completion handler from the SRT write path, keeping the completion handler
// fast and non-blocking.
//
// Usage:
//
//	./client-udp -from :5000 -to srt://host:6000/stream

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/pkg/profile"
	"github.com/randomizedcoder/giouring"
	ring "github.com/randomizedcoder/go-lock-free-ring"
	srt "github.com/randomizedcoder/gosrt"
	"github.com/randomizedcoder/gosrt/contrib/common"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

const (
	// Ring configuration defaults
	defaultIoRingSize  = 64   // io_uring ring size (power of 2)
	defaultBatchSize   = 32   // Batch size for io_uring resubmissions
	maxPacketSize      = 1500 // Maximum UDP packet size
	maxGetSQERetries   = 3    // Retries when GetSQE() returns nil
	maxSubmitRetries   = 3    // Retries for Submit() on transient errors
	retryBackoffUs     = 100  // Microseconds between retries
	waitTimeoutMs      = 10   // WaitCQETimeout timeout in milliseconds
	defaultPktRingSize = 1024 // Default lock-free packet ring capacity
	defaultPktShards   = 1    // Default lock-free ring shards
)

var (
	// Client-udp-specific flags
	from        = flag.String("from", "", "UDP listen address (e.g., :5000) (required)")
	to          = flag.String("to", "", "SRT destination URL (e.g., srt://host:6000/stream) (required)")
	ioRingSize  = flag.Uint("ioringsize", defaultIoRingSize, "io_uring ring size (power of 2)")
	batchSize   = flag.Int("batchsize", defaultBatchSize, "Batch size for io_uring resubmissions")
	pktRingSize = flag.Int("pktringsize", defaultPktRingSize, "Lock-free packet ring capacity")
	pktShards   = flag.Int("pktringshards", defaultPktShards, "Lock-free packet ring shards")
	logtopics   = flag.String("logtopics", "", "SRT log topics (comma-separated)")
	testflags   = flag.Bool("testflags", false, "Test mode: parse flags, apply to config, print config as JSON, and exit")
	profileFlag = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
	profilePath = flag.String("profilepath", ".", "directory to write profile files to")

	// Pause flag for graceful quiesce (set via SIGUSR1 signal)
	paused atomic.Bool
)

// udpPacket transfers received UDP data through the lock-free ring.
// Stored as interface{} in ring, type-asserted on TryRead().
type udpPacket struct {
	buffer *[]byte // Pointer to buffer from sync.Pool (avoids SA6002 allocation on Pool.Put)
	length int     // Valid bytes in buffer
}

// completionInfo tracks pending io_uring recv operations.
// Only recv operations exist (no send); send goes through SRT.
type completionInfo struct {
	buffer *[]byte                 // Pointer to buffer (for sync.Pool without SA6002 allocation)
	msg    *syscall.Msghdr         // Msghdr for recv
	iovec  *syscall.Iovec          // Iovec pointing to buffer
	rsa    *syscall.RawSockaddrAny // Source address (recv)
}

// udpToSrt bridges UDP input (io_uring) to SRT output via a lock-free ring.
type udpToSrt struct {
	// UDP socket
	fd   int
	conn *net.UDPConn

	// io_uring
	ioRing      *giouring.Ring
	ioRingSz    uint32
	batchSz     int
	completions map[uint64]*completionInfo // single-goroutine access, no lock needed
	nextID      uint64

	// Buffer pool
	bufferPool sync.Pool // *[]byte of maxPacketSize

	// Lock-free ring: io_uring recv -> SRT write
	packetRing  *ring.ShardedRing
	writeConfig ring.WriteConfig

	// Metrics
	clientMetrics *metrics.ConnectionMetrics
	ringDrops     atomic.Uint64
}

func main() {
	os.Exit(run())
}

func run() int {
	// Parse all flags (shared + client-udp-specific)
	common.ParseFlags()

	// Validate flag dependencies and auto-enable required flags
	if warnings := common.ValidateFlagDependencies(); len(warnings) > 0 {
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "⚠ %s\n", w)
		}
	}

	// Test mode: print config and exit (before profiler starts)
	if *testflags {
		config := srt.DefaultConfig()
		common.ApplyFlagsToConfig(&config)
		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling config: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	// Validate required flags
	if len(*from) == 0 {
		fmt.Fprintf(os.Stderr, "Error: -from is required\n")
		flag.PrintDefaults()
		return 1
	}
	if len(*to) == 0 {
		fmt.Fprintf(os.Stderr, "Error: -to is required\n")
		flag.PrintDefaults()
		return 1
	}

	// Setup profiling if requested
	var p func(*profile.Profile)
	switch *profileFlag {
	case "cpu":
		p = profile.CPUProfile
	case "mem":
		p = profile.MemProfile
	case "allocs":
		p = profile.MemProfileAllocs
	case "heap":
		p = profile.MemProfileHeap
	case "rate":
		p = profile.MemProfileRate(2048)
	case "mutex":
		p = profile.MutexProfile
	case "block":
		p = profile.BlockProfile
	case "thread":
		p = profile.ThreadcreationProfile
	case "trace":
		p = profile.TraceProfile
	default:
	}

	var prof interface{ Stop() }
	if p != nil {
		prof = profile.Start(profile.ProfilePath(*profilePath), profile.NoShutdownHook, p)
		defer prof.Stop()
	}
	_ = prof

	var logger srt.Logger
	if len(*logtopics) != 0 {
		logger = srt.NewLogger(strings.Split(*logtopics, ","))
	}

	// Get config for statistics interval and shutdown delay
	config := srt.DefaultConfig()
	common.ApplyFlagsToConfig(&config)

	// Create context that cancels on signal
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SIGUSR1 handler for graceful quiesce
	pauseChan := make(chan os.Signal, 1)
	signal.Notify(pauseChan, syscall.SIGUSR1)
	go func() {
		<-pauseChan
		fmt.Fprintf(os.Stderr, "\nPAUSE signal received - stopping UDP receive\n")
		paused.Store(true)
	}()

	// Single waitgroup for all goroutines
	var wg sync.WaitGroup

	// Start Prometheus Metrics Server(s) (if configured)
	if err := common.StartMetricsServers(ctx, &wg, *common.PromHTTPAddr, *common.PromUDSPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start metrics server: %v\n", err)
		return 1
	}

	// Start Logger Goroutine (if enabled)
	if logger != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range logger.Listen() {
				fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n",
					m.SocketId, m.Topic, m.File, m.Line, m.Message)
			}
		}()
	}

	// Open SRT connection to server
	conn, err := openWriter(ctx, *to, logger, &wg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: to: %v\n", err)
		flag.PrintDefaults()
		return 1
	}

	// Store connection socket ID for metrics lookup
	var connSocketId atomic.Uint32
	connSocketId.Store(conn.SocketId())

	// Start Statistics Ticker (if enabled)
	if config.StatisticsPrintInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(config.StatisticsPrintInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					connections := []srt.Conn{conn}
					labeler := func(index int, total int) string {
						return "udp-srt"
					}
					common.PrintConnectionStatistics(connections, config.StatisticsPrintInterval.String(), labeler)
				}
			}
		}()
	}

	// Create Client Metrics (lock-free atomic counters)
	clientMetrics := &metrics.ConnectionMetrics{}

	// Start throughput stats display loop
	instanceLabel := "UDP-SRT"
	if config.InstanceName != "" {
		instanceLabel = config.InstanceName
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		common.RunThroughputDisplayWithLabelAndColor(ctx, *common.StatsPeriod, instanceLabel, *common.OutputColor, func() (uint64, uint64, uint64, uint64, uint64, uint64) {
			var naksRecv, retrans uint64
			if socketId := connSocketId.Load(); socketId != 0 {
				conns := metrics.GetConnections()
				if connInfo, ok := conns[socketId]; ok && connInfo != nil && connInfo.Metrics != nil {
					naksRecv = connInfo.Metrics.CongestionSendNAKPktsRecv.Load()
					retrans = connInfo.Metrics.PktRetransFromNAK.Load()
				}
			}
			return clientMetrics.ByteSentDataSuccess.Load(),
				clientMetrics.PktSentDataSuccess.Load(),
				0, // gaps (N/A for sender)
				naksRecv,
				0, // skips (N/A for sender)
				retrans
		})
	}()

	// Create lock-free ring for io_uring recv -> SRT write handoff
	totalCapacity := uint64(*pktRingSize) * uint64(*pktShards)
	packetRing, ringErr := ring.NewShardedRing(totalCapacity, uint64(*pktShards))
	if ringErr != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create packet ring: %v\n", ringErr)
		return 1
	}

	writeConfig := ring.WriteConfig{
		Strategy:           ring.SpinThenYield,
		MaxRetries:         10,
		BackoffDuration:    100 * time.Microsecond,
		MaxBackoffs:        0,
		MaxBackoffDuration: 10 * time.Millisecond,
		BackoffMultiplier:  2.0,
	}

	// Create udpToSrt struct
	u := &udpToSrt{
		ioRingSz:    uint32(*ioRingSize),
		batchSz:     *batchSize,
		completions: make(map[uint64]*completionInfo),
		bufferPool: sync.Pool{
			New: func() interface{} {
				b := make([]byte, maxPacketSize)
				return &b
			},
		},
		packetRing:    packetRing,
		writeConfig:   writeConfig,
		clientMetrics: clientMetrics,
	}

	// Create UDP socket
	if socketErr := u.createSocket(*from); socketErr != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create UDP socket: %v\n", socketErr)
		return 1
	}

	fmt.Fprintf(os.Stderr, "UDP listening on %s\n", *from)

	// Initialize io_uring
	if ringInitErr := u.initIoUring(); ringInitErr != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to init io_uring: %v\n", ringInitErr)
		_ = io.Closer(u.conn).Close()
		return 1
	}

	fmt.Fprintf(os.Stderr, "io_uring initialized (ring_size=%d, batch_size=%d)\n", u.ioRingSz, u.batchSz)
	fmt.Fprintf(os.Stderr, "Packet ring: capacity=%d, shards=%d\n", totalCapacity, *pktShards)
	fmt.Fprintf(os.Stderr, "Streaming to %s\n", *to)

	// Pre-populate ring with receive requests
	u.submitRecvRequestBatch(int(u.ioRingSz / 2))

	// Start completion handler goroutine
	doneChan := make(chan error, 10)

	wg.Add(1)
	go func() {
		defer wg.Done()
		u.completionHandler(ctx)
	}()

	// Start SRT write loop goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		u.srtWriteLoop(ctx, conn, doneChan)
	}()

	// Wait for completion or context cancellation
	var shutdownStart time.Time
	select {
	case doneErr := <-doneChan:
		shutdownStart = time.Now()
		if doneErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", doneErr)
		} else {
			fmt.Fprint(os.Stderr, "\n")
		}
	case <-ctx.Done():
		shutdownStart = time.Now()
		fmt.Fprintf(os.Stderr, "\nShutdown signal received\n")
	}

	// Cleanup
	_ = io.Closer(conn).Close()
	_ = io.Closer(u.conn).Close()
	u.ioRing.QueueExit()

	if logger != nil {
		logger.Close()
	}

	// Wait for all goroutines with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Graceful shutdown complete after %dms\n", elapsedMs)
	case <-time.After(config.ShutdownDelay):
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Shutdown timed out after %s (elapsed: %dms)\n", config.ShutdownDelay, elapsedMs)
	}

	return 0
}

// createSocket creates and binds a UDP socket for io_uring.
func (u *udpToSrt) createSocket(addr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	u.conn = conn

	// Get raw file descriptor for io_uring
	file, err := conn.File()
	if err != nil {
		return err
	}
	u.fd = int(file.Fd())
	// Don't close file - fd must stay valid for io_uring

	return nil
}

// initIoUring initializes the io_uring ring.
func (u *udpToSrt) initIoUring() error {
	u.ioRing = giouring.NewRing()
	return u.ioRing.QueueInit(u.ioRingSz, 0)
}

// submitRecvRequestBatch submits multiple receive requests in a single batch.
// Phase 1: Prepare all SQEs (GetSQE + PrepareRecvMsg).
// Phase 2: Single ring.Submit() for all prepared SQEs.
func (u *udpToSrt) submitRecvRequestBatch(count int) {
	type pendingRequest struct {
		requestID uint64
		buffer    *[]byte
	}
	pending := make([]pendingRequest, 0, count)

	// Phase 1: Prepare all SQEs
	for i := 0; i < count; i++ {
		bufPtr, ok := u.bufferPool.Get().(*[]byte)
		if !ok {
			panic("bufferPool contained non-*[]byte value")
		}
		buf := *bufPtr

		rsa := new(syscall.RawSockaddrAny)
		iovec := new(syscall.Iovec)
		msg := new(syscall.Msghdr)

		iovec.Base = &buf[0]
		iovec.SetLen(len(buf))

		msg.Name = (*byte)(unsafe.Pointer(rsa))
		msg.Namelen = uint32(syscall.SizeofSockaddrAny)
		msg.Iov = iovec
		msg.Iovlen = 1

		requestID := u.nextID
		u.nextID++
		u.completions[requestID] = &completionInfo{
			buffer: bufPtr,
			msg:    msg,
			iovec:  iovec,
			rsa:    rsa,
		}

		// Get SQE with retry
		var sqe *giouring.SubmissionQueueEntry
		for retry := 0; retry < maxGetSQERetries; retry++ {
			sqe = u.ioRing.GetSQE()
			if sqe != nil {
				break
			}
			if retry < maxGetSQERetries-1 {
				time.Sleep(time.Duration(retryBackoffUs) * time.Microsecond)
			}
		}

		if sqe == nil {
			delete(u.completions, requestID)
			u.bufferPool.Put(bufPtr)
			fmt.Fprintf(os.Stderr, "Warning: ring full after %d retries, submitted %d/%d requests\n", maxGetSQERetries, i, count)
			break
		}

		sqe.PrepareRecvMsg(u.fd, msg, 0)
		sqe.SetData64(requestID)

		pending = append(pending, pendingRequest{
			requestID: requestID,
			buffer:    bufPtr,
		})
	}

	// Phase 2: Single Submit() for ALL prepared SQEs
	if len(pending) > 0 {
		var err error
		for retry := 0; retry < maxSubmitRetries; retry++ {
			_, err = u.ioRing.Submit()
			if err == nil {
				break
			}
			if err != syscall.EINTR && err != syscall.EAGAIN {
				break
			}
			if retry < maxSubmitRetries-1 {
				time.Sleep(time.Duration(retryBackoffUs) * time.Microsecond)
			}
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: batch submit failed: %v (cleaning up %d requests)\n", err, len(pending))
			for _, req := range pending {
				delete(u.completions, req.requestID)
				u.bufferPool.Put(req.buffer)
			}
		}
	}
}

// completionHandler processes io_uring completions with batched resubmission.
// Matches the gosrt library pattern from listen_linux.go.
func (u *udpToSrt) completionHandler(ctx context.Context) {
	timeout := syscall.NsecToTimespec(int64(waitTimeoutMs * time.Millisecond))
	pendingResubmits := 0

	for {
		// Check for shutdown
		select {
		case <-ctx.Done():
			if pendingResubmits > 0 {
				u.submitRecvRequestBatch(pendingResubmits)
			}
			return
		default:
		}

		// Block until completion or timeout
		cqe, err := u.ioRing.WaitCQETimeout(&timeout)
		if err != nil {
			if err == syscall.ETIME {
				// Flush pending resubmits on timeout to prevent ring from running dry
				if pendingResubmits > 0 {
					u.submitRecvRequestBatch(pendingResubmits)
					pendingResubmits = 0
				}
				continue
			}
			if err == syscall.EINTR {
				if pendingResubmits > 0 {
					u.submitRecvRequestBatch(pendingResubmits)
					pendingResubmits = 0
				}
				continue
			}
			if err == syscall.EBADF {
				return // Ring closed - normal shutdown
			}
			fmt.Fprintf(os.Stderr, "WaitCQETimeout error: %v\n", err)
			continue
		}

		// Look up completion info (no lock needed - single goroutine access)
		requestID := cqe.UserData
		compInfo, exists := u.completions[requestID]
		if !exists {
			u.ioRing.CQESeen(cqe)
			fmt.Fprintf(os.Stderr, "Warning: unknown request ID %d\n", requestID)
			continue
		}
		delete(u.completions, requestID)

		if u.handleRecvCompletion(cqe, compInfo) {
			pendingResubmits++
		}

		u.ioRing.CQESeen(cqe)

		// Batch resubmit when threshold reached
		if pendingResubmits >= u.batchSz {
			u.submitRecvRequestBatch(pendingResubmits)
			pendingResubmits = 0
		}
	}
}

// handleRecvCompletion processes a receive completion.
// Pushes data to the lock-free ring for the SRT write loop to consume.
// Returns true if caller should schedule a resubmit.
func (u *udpToSrt) handleRecvCompletion(cqe *giouring.CompletionQueueEvent, compInfo *completionInfo) bool {
	if cqe.Res < 0 {
		errno := syscall.Errno(-cqe.Res)
		fmt.Fprintf(os.Stderr, "Recv error: %v\n", errno)
		u.bufferPool.Put(compInfo.buffer)
		return true
	}

	bytesReceived := int(cqe.Res)
	if bytesReceived == 0 {
		u.bufferPool.Put(compInfo.buffer)
		return true
	}

	// Push to lock-free ring for SRT write loop
	pkt := udpPacket{buffer: compInfo.buffer, length: bytesReceived}
	if !u.packetRing.WriteWithBackoff(0, pkt, u.writeConfig) {
		// Ring full after all backoff retries - drop packet
		u.bufferPool.Put(compInfo.buffer)
		u.ringDrops.Add(1)
	}
	// Buffer ownership transferred to ring consumer (or pool on drop)

	return true
}

// srtWriteLoop drains the lock-free ring and writes packets to the SRT connection.
// Uses SetData() + WritePacket() to bypass Write()'s writeBuffer copy path.
// Follows the drainPacketRing pattern from congestion/live/receive/ring.go.
func (u *udpToSrt) srtWriteLoop(ctx context.Context, conn srt.Conn, doneChan chan<- error) {
	for {
		// Check context cancellation first
		select {
		case <-ctx.Done():
			doneChan <- nil
			return
		default:
		}

		// Check if paused
		if paused.Load() {
			select {
			case <-ctx.Done():
				doneChan <- nil
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		item, ok := u.packetRing.TryRead()
		if !ok {
			// Ring empty - yield to avoid busy-spinning
			runtime.Gosched()
			continue
		}

		udpPkt, ok := item.(udpPacket)
		if !ok {
			continue
		}

		// Create SRT packet and set payload (one copy into packet's bytes.Buffer)
		p := packet.NewPacket(nil)
		buf := *udpPkt.buffer
		p.SetData(buf[:udpPkt.length])
		u.bufferPool.Put(udpPkt.buffer) // Return buffer after SetData copies data

		// Push directly to sender via WritePacket (bypasses Write's writeBuffer)
		// WritePacket takes ownership of the packet on success
		writeErr := conn.WritePacket(p)
		if writeErr != nil {
			p.Decommission()
			// Check if context was canceled during write
			select {
			case <-ctx.Done():
				doneChan <- nil
				return
			default:
				errStr := writeErr.Error()
				if strings.Contains(errStr, "connection refused") ||
					strings.Contains(errStr, "use of closed network connection") ||
					strings.Contains(errStr, "broken pipe") {
					doneChan <- nil
					return
				}
				if opErr, isOpErr := writeErr.(*net.OpError); isOpErr {
					if opErr.Err != nil {
						opErrStr := opErr.Err.Error()
						if strings.Contains(opErrStr, "connection refused") ||
							strings.Contains(opErrStr, "broken pipe") {
							doneChan <- nil
							return
						}
					}
				}
				doneChan <- fmt.Errorf("write: %w", writeErr)
				return
			}
		}

		// Lock-free atomic increments for throughput tracking
		u.clientMetrics.ByteSentDataSuccess.Add(uint64(udpPkt.length))
		u.clientMetrics.PktSentDataSuccess.Add(1)
	}
}

// openWriter opens an SRT connection based on the URL scheme.
func openWriter(ctx context.Context, address string, logger srt.Logger, wg *sync.WaitGroup) (srt.Conn, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	switch u.Scheme {
	case "srt":
		config := srt.DefaultConfig()
		common.ApplyFlagsToConfig(&config)

		if logger != nil {
			config.Logger = logger
		}

		// Set stream ID in config before dialing
		streamID := u.Path
		if streamID == "" {
			streamID = "/"
		}
		if !strings.HasPrefix(streamID, "publish:") {
			streamID = "publish:" + streamID
		}
		config.StreamId = streamID

		conn, dialErr := srt.Dial(ctx, "srt", u.Host, config, wg)
		if dialErr != nil {
			return nil, fmt.Errorf("dial: %w", dialErr)
		}

		return conn, nil
	default:
		return nil, fmt.Errorf("unsupported scheme: %s (expected srt://)", u.Scheme)
	}
}
