package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"
	"github.com/pkg/profile"
)

type stats struct {
	bprev  uint64
	btotal uint64
	prev   uint64
	total  uint64

	lock sync.Mutex

	period time.Duration
	last   time.Time
}

func (s *stats) init(period time.Duration) {
	s.bprev = 0
	s.btotal = 0
	s.prev = 0
	s.total = 0

	s.period = period
	s.last = time.Now()

	go s.tick()
}

func (s *stats) tick() {
	ticker := time.NewTicker(s.period)
	defer ticker.Stop()

	for c := range ticker.C {
		s.lock.Lock()
		diff := c.Sub(s.last)

		bavg := float64(s.btotal-s.bprev) * 8 / (1000 * 1000 * diff.Seconds())
		avg := float64(s.total-s.prev) / diff.Seconds()

		s.bprev = s.btotal
		s.prev = s.total
		s.last = c

		s.lock.Unlock()

		fmt.Fprintf(os.Stderr, "\r%-54s: %8.3f kpackets (%8.3f packets/s), %8.3f mbytes (%8.3f Mbps)", c, float64(s.total)/1024, avg, float64(s.btotal)/1024/1024, bavg)
	}
}

func (s *stats) update(n uint64) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.btotal += n
	s.total++
}

var (
	// Client flags
	from        = flag.String("from", "", "Address to read from, sources: srt://, udp://, - (stdin)")
	to          = flag.String("to", "", "Address to write to, targets: srt://, udp://, file://, - (stdout)")
	logtopics   = flag.String("logtopics", "", "topics for the log output")
	profileMode = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
	bufferSize  = flag.Int("bufferSize", 2048, "buffer size for reading and writing (bytes)")

	// Config flags
	congestion            = flag.String("congestion", "", "type of congestion control. 'live' or 'file'")
	connectionTimeout     = flag.String("connectionTimeout", "", "connection timeout (e.g., '3s', '500ms')")
	driftTracer           = flag.Bool("driftTracer", true, "enable drift tracer")
	enforcedEncryption    = flag.Bool("enforcedEncryption", true, "reject connection if parties set different passphrase")
	fc                    = flag.Uint("fc", 0, "flow control window size (packets)")
	groupConnect          = flag.Bool("groupConnect", false, "accept group connections")
	groupStabilityTimeout = flag.String("groupStabilityTimeout", "", "group stability timeout (e.g., '1s', '500ms')")
	inputBW               = flag.Int64("inputBW", 0, "input bandwidth (bytes)")
	iptos                 = flag.Int("iptos", 0, "IP socket type of service")
	ipttl                 = flag.Int("ipttl", 0, "IP socket time to live option")
	ipv6Only              = flag.Int("ipv6Only", 0, "allow only IPv6")
	kmPreAnnounce         = flag.Uint64("kmPreAnnounce", 0, "duration of Stream Encryption key switchover (packets)")
	kmRefreshRate         = flag.Uint64("kmRefreshRate", 0, "stream encryption key refresh rate (packets)")
	latency               = flag.String("latency", "", "maximum accepted transmission latency (e.g., '120ms', '1s')")
	lossMaxTTL            = flag.Uint("lossMaxTTL", 0, "packet reorder tolerance")
	maxBW                 = flag.Int64("maxBW", 0, "bandwidth limit (bytes/s)")
	messageAPI            = flag.Bool("messageAPI", false, "enable SRT message mode")
	minInputBW            = flag.Int64("minInputBW", 0, "minimum input bandwidth")
	minVersion            = flag.Uint("minVersion", 0, "minimum SRT library version of a peer")
	mss                   = flag.Uint("mss", 0, "MTU size")
	nakReport             = flag.Bool("nakReport", true, "enable periodic NAK reports")
	overheadBW            = flag.Int64("overheadBW", 0, "limit bandwidth overhead (percents)")
	packetFilter          = flag.String("packetFilter", "", "set up the packet filter")
	passphrase            = flag.String("passphrase", "", "passphrase for de- and enrcypting the data")
	payloadSize           = flag.Uint("payloadSize", 0, "maximum payload size (bytes)")
	pbKeylen              = flag.Int("pbKeylen", 0, "crypto key length in bytes")
	peerIdleTimeout       = flag.String("peerIdleTimeout", "", "peer idle timeout (e.g., '2s', '500ms')")
	peerLatency           = flag.String("peerLatency", "", "minimum receiver latency to be requested by sender (e.g., '120ms', '1s')")
	receiverBufferSize    = flag.Uint("receiverBufferSize", 0, "receiver buffer size (bytes)")
	receiverLatency       = flag.String("receiverLatency", "", "receiver-side latency (e.g., '120ms', '1s')")
	sendBufferSize        = flag.Uint("sendBufferSize", 0, "sender buffer size (bytes)")
	sendDropDelay         = flag.String("sendDropDelay", "", "sender's delay before dropping packets (e.g., '1s', '500ms')")
	streamId              = flag.String("streamId", "", "stream ID (settable in caller mode only, visible on the listener peer)")
	tooLatePacketDrop     = flag.Bool("tooLatePacketDrop", true, "drop too late packets")
	transmissionType      = flag.String("transmissionType", "", "transmission type. 'live' or 'file'")
	tsbpdMode             = flag.Bool("tsbpdMode", true, "timestamp-based packet delivery mode")
	allowPeerIpChange     = flag.Bool("allowPeerIpChange", false, "if a new IP starts sending data on an existing socket id, allow it")
)

func parseDuration(s string, defaultVal time.Duration) time.Duration {
	if s == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultVal
	}
	return d
}

// applyConfigFlags applies command-line flag values to the config.
// It only applies flags that were explicitly set or have non-zero/non-empty values.
func applyConfigFlags(config *srt.Config, setFlags map[string]bool) {
	// Apply flag values to config
	if *congestion != "" {
		config.Congestion = *congestion
	}
	if *connectionTimeout != "" {
		config.ConnectionTimeout = parseDuration(*connectionTimeout, config.ConnectionTimeout)
	}
	if setFlags["driftTracer"] {
		config.DriftTracer = *driftTracer
	}
	if setFlags["enforcedEncryption"] {
		config.EnforcedEncryption = *enforcedEncryption
	}
	if *fc != 0 {
		config.FC = uint32(*fc)
	}
	if setFlags["groupConnect"] {
		config.GroupConnect = *groupConnect
	}
	if *groupStabilityTimeout != "" {
		config.GroupStabilityTimeout = parseDuration(*groupStabilityTimeout, config.GroupStabilityTimeout)
	}
	if *inputBW != 0 {
		config.InputBW = *inputBW
	}
	if *iptos != 0 {
		config.IPTOS = *iptos
	}
	if *ipttl != 0 {
		config.IPTTL = *ipttl
	}
	if *ipv6Only != 0 {
		config.IPv6Only = *ipv6Only
	}
	if *kmPreAnnounce != 0 {
		config.KMPreAnnounce = *kmPreAnnounce
	}
	if *kmRefreshRate != 0 {
		config.KMRefreshRate = *kmRefreshRate
	}
	if *latency != "" {
		config.Latency = parseDuration(*latency, config.Latency)
	}
	if *lossMaxTTL != 0 {
		config.LossMaxTTL = uint32(*lossMaxTTL)
	}
	if *maxBW != 0 {
		config.MaxBW = *maxBW
	}
	if setFlags["messageAPI"] {
		config.MessageAPI = *messageAPI
	}
	if *minInputBW != 0 {
		config.MinInputBW = *minInputBW
	}
	if *minVersion != 0 {
		config.MinVersion = uint32(*minVersion)
	}
	if *mss != 0 {
		config.MSS = uint32(*mss)
	}
	if setFlags["nakReport"] {
		config.NAKReport = *nakReport
	}
	if *overheadBW != 0 {
		config.OverheadBW = *overheadBW
	}
	if *packetFilter != "" {
		config.PacketFilter = *packetFilter
	}
	if *passphrase != "" {
		config.Passphrase = *passphrase
	}
	if *payloadSize != 0 {
		config.PayloadSize = uint32(*payloadSize)
	}
	if *pbKeylen != 0 {
		config.PBKeylen = *pbKeylen
	}
	if *peerIdleTimeout != "" {
		config.PeerIdleTimeout = parseDuration(*peerIdleTimeout, config.PeerIdleTimeout)
	}
	if *peerLatency != "" {
		config.PeerLatency = parseDuration(*peerLatency, config.PeerLatency)
	}
	if *receiverBufferSize != 0 {
		config.ReceiverBufferSize = uint32(*receiverBufferSize)
	}
	if *receiverLatency != "" {
		config.ReceiverLatency = parseDuration(*receiverLatency, config.ReceiverLatency)
	}
	if *sendBufferSize != 0 {
		config.SendBufferSize = uint32(*sendBufferSize)
	}
	if *sendDropDelay != "" {
		config.SendDropDelay = parseDuration(*sendDropDelay, config.SendDropDelay)
	}
	if *streamId != "" {
		config.StreamId = *streamId
	}
	if setFlags["tooLatePacketDrop"] {
		config.TooLatePacketDrop = *tooLatePacketDrop
	}
	if *transmissionType != "" {
		config.TransmissionType = *transmissionType
	}
	if setFlags["tsbpdMode"] {
		config.TSBPDMode = *tsbpdMode
	}
	if setFlags["allowPeerIpChange"] {
		config.AllowPeerIpChange = *allowPeerIpChange
	}

	// Log config if "config" topic is enabled
	if config.Logger != nil && config.Logger.HasTopic("config") {
		config.Logger.Print("config", 0, 1, func() string {
			return fmt.Sprintf("SRT Config:\n"+
				"  Congestion: %s\n"+
				"  ConnectionTimeout: %v\n"+
				"  DriftTracer: %v\n"+
				"  EnforcedEncryption: %v\n"+
				"  FC: %d\n"+
				"  GroupConnect: %v\n"+
				"  GroupStabilityTimeout: %v\n"+
				"  InputBW: %d\n"+
				"  IPTOS: %d\n"+
				"  IPTTL: %d\n"+
				"  IPv6Only: %d\n"+
				"  KMPreAnnounce: %d\n"+
				"  KMRefreshRate: %d\n"+
				"  Latency: %v\n"+
				"  LossMaxTTL: %d\n"+
				"  MaxBW: %d\n"+
				"  MessageAPI: %v\n"+
				"  MinInputBW: %d\n"+
				"  MinVersion: %#x\n"+
				"  MSS: %d\n"+
				"  NAKReport: %v\n"+
				"  OverheadBW: %d\n"+
				"  PacketFilter: %s\n"+
				"  Passphrase: %s\n"+
				"  PayloadSize: %d\n"+
				"  PBKeylen: %d\n"+
				"  PeerIdleTimeout: %v\n"+
				"  PeerLatency: %v\n"+
				"  ReceiverBufferSize: %d\n"+
				"  ReceiverLatency: %v\n"+
				"  SendBufferSize: %d\n"+
				"  SendDropDelay: %v\n"+
				"  StreamId: %s\n"+
				"  TooLatePacketDrop: %v\n"+
				"  TransmissionType: %s\n"+
				"  TSBPDMode: %v\n"+
				"  AllowPeerIpChange: %v",
				config.Congestion,
				config.ConnectionTimeout,
				config.DriftTracer,
				config.EnforcedEncryption,
				config.FC,
				config.GroupConnect,
				config.GroupStabilityTimeout,
				config.InputBW,
				config.IPTOS,
				config.IPTTL,
				config.IPv6Only,
				config.KMPreAnnounce,
				config.KMRefreshRate,
				config.Latency,
				config.LossMaxTTL,
				config.MaxBW,
				config.MessageAPI,
				config.MinInputBW,
				config.MinVersion,
				config.MSS,
				config.NAKReport,
				config.OverheadBW,
				config.PacketFilter,
				func() string {
					if config.Passphrase != "" {
						return "***"
					}
					return ""
				}(),
				config.PayloadSize,
				config.PBKeylen,
				config.PeerIdleTimeout,
				config.PeerLatency,
				config.ReceiverBufferSize,
				config.ReceiverLatency,
				config.SendBufferSize,
				config.SendDropDelay,
				config.StreamId,
				config.TooLatePacketDrop,
				config.TransmissionType,
				config.TSBPDMode,
				config.AllowPeerIpChange)
		})
	}
}

func main() {
	flag.Parse()

	// Track which flags were explicitly set
	setFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	var p func(*profile.Profile)
	switch *profileMode {
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

	if p != nil {
		defer profile.Start(profile.ProfilePath("."), profile.NoShutdownHook, p).Stop()
	}

	// Create default config and apply CLI flags
	defaultConfig := srt.DefaultConfig()

	// Set up logger first so it can be used for config logging
	var logger srt.Logger
	if len(*logtopics) != 0 {
		logger = srt.NewLogger(strings.Split(*logtopics, ","))
		defaultConfig.Logger = logger
	}

	// Apply flag values to default config
	applyConfigFlags(&defaultConfig, setFlags)

	go func() {
		if logger == nil {
			return
		}

		for m := range logger.Listen() {
			fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n", m.SocketId, m.Topic, m.File, m.Line, m.Message)
		}
	}()

	go func() {
		if logger == nil {
			return
		}

		for m := range logger.Listen() {
			fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n", m.SocketId, m.Topic, m.File, m.Line, m.Message)
		}
	}()

	r, err := openReader(*from, &defaultConfig, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: from: %v\n", err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	w, err := openWriter(*to, &defaultConfig, logger, *bufferSize)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: to: %v\n", err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	doneChan := make(chan error)

	go func() {
		buffer := make([]byte, *bufferSize)

		s := stats{}
		s.init(200 * time.Millisecond)

		for {
			n, err := r.Read(buffer)
			if err != nil {
				doneChan <- fmt.Errorf("read: %w", err)
				return
			}

			s.update(uint64(n))

			if _, err := w.Write(buffer[:n]); err != nil {
				doneChan <- fmt.Errorf("write: %w", err)
				return
			}
		}
	}()

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt)
		<-quit

		doneChan <- nil
	}()

	if err := <-doneChan; err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	} else {
		fmt.Fprint(os.Stderr, "\n")
	}

	w.Close()

	if srtconn, ok := w.(srt.Conn); ok {
		stats := &srt.Statistics{}
		srtconn.Stats(stats)

		data, err := json.MarshalIndent(stats, "", "   ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "writer: %+v\n", stats)
		} else {
			fmt.Fprintf(os.Stderr, "writer: %s\n", string(data))
		}
	}

	r.Close()

	if srtconn, ok := r.(srt.Conn); ok {
		stats := &srt.Statistics{}
		srtconn.Stats(stats)

		data, err := json.MarshalIndent(stats, "", "   ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "reader: %+v\n", stats)
		} else {
			fmt.Fprintf(os.Stderr, "reader: %s\n", string(data))
		}
	}

	if logger != nil {
		logger.Close()
	}
}

func openReader(addr string, defaultConfig *srt.Config, logger srt.Logger) (io.ReadCloser, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("the address must not be empty")
	}

	if addr == "-" {
		if os.Stdin == nil {
			return nil, fmt.Errorf("stdin is not defined")
		}

		return os.Stdin, nil
	}

	if strings.HasPrefix(addr, "debug://") {
		readerOptions := DebugReaderOptions{}
		parts := strings.SplitN(strings.TrimPrefix(addr, "debug://"), "?", 2)
		if len(parts) > 1 {
			options, err := url.ParseQuery(parts[1])
			if err != nil {
				return nil, err
			}

			if x, err := strconv.ParseUint(options.Get("bitrate"), 10, 64); err == nil {
				readerOptions.Bitrate = x
			}
		}

		r, err := NewDebugReader(readerOptions)

		return r, err
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	if u.Scheme == "srt" {
		// Start with default config (which includes CLI flags)
		config := *defaultConfig
		// Merge URL query parameters (they override CLI flags)
		if err := config.UnmarshalQuery(u.RawQuery); err != nil {
			return nil, err
		}
		config.Logger = logger

		mode := u.Query().Get("mode")

		if mode == "listener" {
			ln, err := srt.Listen("srt", u.Host, config)
			if err != nil {
				return nil, err
			}

			conn, _, err := ln.Accept(func(req srt.ConnRequest) srt.ConnType {
				if config.StreamId != req.StreamId() {
					return srt.REJECT
				}

				req.SetPassphrase(config.Passphrase)

				return srt.PUBLISH
			})
			if err != nil {
				return nil, err
			}

			if conn == nil {
				return nil, fmt.Errorf("incoming connection rejected")
			}

			return conn, nil
		} else if mode == "caller" {
			conn, err := srt.Dial("srt", u.Host, config)
			if err != nil {
				return nil, err
			}

			return conn, nil
		} else {
			return nil, fmt.Errorf("unsupported mode")
		}
	} else if u.Scheme == "udp" {
		laddr, err := net.ResolveUDPAddr("udp", u.Host)
		if err != nil {
			return nil, err
		}

		conn, err := net.ListenUDP("udp", laddr)
		if err != nil {
			return nil, err
		}

		return conn, nil
	}

	return nil, fmt.Errorf("unsupported reader")
}

func openWriter(addr string, defaultConfig *srt.Config, logger srt.Logger, bufferSize int) (io.WriteCloser, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("the address must not be empty")
	}

	if addr == "-" {
		if os.Stdout == nil {
			return nil, fmt.Errorf("stdout is not defined")
		}

		return NewNonblockingWriter(os.Stdout, bufferSize), nil
	}

	if strings.HasPrefix(addr, "file://") {
		path := strings.TrimPrefix(addr, "file://")
		file, err := os.Create(path)
		if err != nil {
			return nil, err
		}

		return NewNonblockingWriter(file, bufferSize), nil
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	if u.Scheme == "srt" {
		// Start with default config (which includes CLI flags)
		config := *defaultConfig
		// Merge URL query parameters (they override CLI flags)
		if err := config.UnmarshalQuery(u.RawQuery); err != nil {
			return nil, err
		}
		config.Logger = logger

		mode := u.Query().Get("mode")

		if mode == "listener" {
			ln, err := srt.Listen("srt", u.Host, config)
			if err != nil {
				return nil, err
			}

			conn, _, err := ln.Accept(func(req srt.ConnRequest) srt.ConnType {
				if config.StreamId != req.StreamId() {
					return srt.REJECT
				}

				req.SetPassphrase(config.Passphrase)

				return srt.SUBSCRIBE
			})
			if err != nil {
				return nil, err
			}

			if conn == nil {
				return nil, fmt.Errorf("incoming connection rejected")
			}

			return conn, nil
		} else if mode == "caller" {
			conn, err := srt.Dial("srt", u.Host, config)
			if err != nil {
				return nil, err
			}

			return conn, nil
		} else {
			return nil, fmt.Errorf("unsupported mode")
		}
	} else if u.Scheme == "udp" {
		raddr, err := net.ResolveUDPAddr("udp", u.Host)
		if err != nil {
			return nil, err
		}

		conn, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			return nil, err
		}

		return conn, nil
	}

	return nil, fmt.Errorf("unsupported writer")
}
