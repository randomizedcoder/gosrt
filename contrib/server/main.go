package main

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"
	"github.com/pkg/profile"
)

var (
	// Server flags
	addr        = flag.String("addr", "", "address to listen on")
	app         = flag.String("app", "", "path prefix for streamid")
	token       = flag.String("token", "", "token query param for streamid")
	passphrase  = flag.String("passphrase", "", "passphrase for de- and enrcypting the data")
	logtopics   = flag.String("logtopics", "", "topics for the log output")
	profileMode = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")

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

// server is an implementation of the Server framework
type server struct {
	server *srt.Server

	// Map of publishing channels and a lock to serialize
	// access to the map.
	channels map[string]srt.PubSub
	lock     sync.RWMutex
}

func (s *server) ListenAndServe() error {
	return s.server.ListenAndServe()
}

func (s *server) Shutdown() {
	s.server.Shutdown()
}

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

	if len(*addr) == 0 {
		fmt.Fprintf(os.Stderr, "Provide a listen address with -addr\n")
		os.Exit(1)
	}

	s := server{
		channels: make(map[string]srt.PubSub),
	}

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

	config := srt.DefaultConfig()

	// Set up logger first so it can be used for config logging
	if len(*logtopics) != 0 {
		config.Logger = srt.NewLogger(strings.Split(*logtopics, ","))
	}

	// Apply flag values to config
	applyConfigFlags(&config, setFlags)

	s.server = &srt.Server{
		Addr:            *addr,
		HandleConnect:   s.handleConnect,
		HandlePublish:   s.handlePublish,
		HandleSubscribe: s.handleSubscribe,
		Config:          &config,
	}

	fmt.Fprintf(os.Stderr, "Listening on %s\n", *addr)

	go func() {
		if config.Logger == nil {
			return
		}

		for m := range config.Logger.Listen() {
			fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n", m.SocketId, m.Topic, m.File, m.Line, m.Message)
		}
	}()

	go func() {
		if err := s.ListenAndServe(); err != nil && err != srt.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "SRT Server: %s\n", err)
			os.Exit(2)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit

	s.Shutdown()

	if config.Logger != nil {
		config.Logger.Close()
	}
}

func (s *server) log(who, action, path, message string, client net.Addr) {
	fmt.Fprintf(os.Stderr, "%-10s %10s %s (%s) %s\n", who, action, path, client, message)
}

func (s *server) handleConnect(req srt.ConnRequest) srt.ConnType {
	var mode srt.ConnType = srt.SUBSCRIBE
	client := req.RemoteAddr()

	channel := ""

	if req.Version() == 4 {
		mode = srt.PUBLISH
		channel = "/" + client.String()

		req.SetPassphrase(*passphrase)
	} else if req.Version() == 5 {
		streamId := req.StreamId()
		path := streamId

		if strings.HasPrefix(streamId, "publish:") {
			mode = srt.PUBLISH
			path = strings.TrimPrefix(streamId, "publish:")
		} else if strings.HasPrefix(streamId, "subscribe:") {
			path = strings.TrimPrefix(streamId, "subscribe:")
		}

		u, err := url.Parse(path)
		if err != nil {
			return srt.REJECT
		}

		if req.IsEncrypted() {
			if err := req.SetPassphrase(*passphrase); err != nil {
				s.log("CONNECT", "FORBIDDEN", u.Path, err.Error(), client)
				return srt.REJECT
			}
		}

		// Check the token
		tokenValue := u.Query().Get("token")
		if len(*token) != 0 && *token != tokenValue {
			s.log("CONNECT", "FORBIDDEN", u.Path, "invalid token ("+tokenValue+")", client)
			return srt.REJECT
		}

		// Check the app patch
		appPath := *app
		if len(appPath) == 0 {
			appPath = "/"
		}
		if !strings.HasPrefix(u.Path, appPath) {
			s.log("CONNECT", "FORBIDDEN", u.Path, "invalid app ", client)
			return srt.REJECT
		}

		if len(strings.TrimPrefix(u.Path, appPath)) == 0 {
			s.log("CONNECT", "INVALID", u.Path, "stream name not provided", client)
			return srt.REJECT
		}

		channel = u.Path
	} else {
		return srt.REJECT
	}

	s.lock.RLock()
	pubsub := s.channels[channel]
	s.lock.RUnlock()

	if mode == srt.PUBLISH && pubsub != nil {
		s.log("CONNECT", "CONFLICT", channel, "already publishing", client)
		return srt.REJECT
	}

	if mode == srt.SUBSCRIBE && pubsub == nil {
		s.log("CONNECT", "NOTFOUND", channel, "not publishing", client)
		return srt.REJECT
	}

	return mode
}

func (s *server) handlePublish(conn srt.Conn) {
	channel := ""
	client := conn.RemoteAddr()
	if client == nil {
		conn.Close()
		return
	}

	if conn.Version() == 4 {
		channel = "/" + client.String()
	} else if conn.Version() == 5 {
		streamId := conn.StreamId()
		path := strings.TrimPrefix(streamId, "publish:")

		channel = path
	} else {
		s.log("PUBLISH", "INVALID", channel, "unknown connection version", client)
		conn.Close()
		return
	}

	// Look for the stream
	s.lock.Lock()
	pubsub := s.channels[channel]
	if pubsub == nil {
		pubsub = srt.NewPubSub(srt.PubSubConfig{
			Logger: s.server.Config.Logger,
		})
		s.channels[channel] = pubsub
	} else {
		pubsub = nil
	}
	s.lock.Unlock()

	if pubsub == nil {
		s.log("PUBLISH", "CONFLICT", channel, "already publishing", client)
		conn.Close()
		return
	}

	s.log("PUBLISH", "START", channel, "publishing", client)

	pubsub.Publish(conn)

	s.lock.Lock()
	delete(s.channels, channel)
	s.lock.Unlock()

	s.log("PUBLISH", "STOP", channel, "", client)

	stats := &srt.Statistics{}
	conn.Stats(stats)

	fmt.Fprintf(os.Stderr, "%+v\n", stats)

	conn.Close()
}

func (s *server) handleSubscribe(conn srt.Conn) {
	channel := ""
	client := conn.RemoteAddr()
	if client == nil {
		conn.Close()
		return
	}

	if conn.Version() == 4 {
		channel = client.String()
	} else if conn.Version() == 5 {
		streamId := conn.StreamId()
		path := strings.TrimPrefix(streamId, "subscribe:")

		channel = path
	} else {
		s.log("SUBSCRIBE", "INVALID", channel, "unknown connection version", client)
		conn.Close()
		return
	}

	s.log("SUBSCRIBE", "START", channel, "", client)

	// Look for the stream
	s.lock.RLock()
	pubsub := s.channels[channel]
	s.lock.RUnlock()

	if pubsub == nil {
		s.log("SUBSCRIBE", "NOTFOUND", channel, "not publishing", client)
		conn.Close()
		return
	}

	pubsub.Subscribe(conn)

	s.log("SUBSCRIBE", "STOP", channel, "", client)

	stats := &srt.Statistics{}
	conn.Stats(stats)

	fmt.Fprintf(os.Stderr, "%+v\n", stats)

	conn.Close()
}
