package common

import (
	"flag"
	"log"
	"time"

	srt "github.com/randomizedcoder/gosrt"
)

var (
	// Map to track which flags were explicitly set by the user
	FlagSet = make(map[string]bool)

	// Connection configuration flags (shared between client and server)
	Congestion         = flag.String("congestion", "", "Type of congestion control ('live' or 'file')")
	Conntimeo          = flag.Int("conntimeo", 0, "Connection timeout in milliseconds")
	Streamid           = flag.String("streamid", "", "Stream ID (settable in caller mode only)")
	PassphraseFlag     = flag.String("passphrase-flag", "", "Password for encrypted transmission (alternative to passphrase)")
	PBKeylen           = flag.Int("pbkeylen", 0, "Crypto key length in bytes (16, 24, or 32)")
	KMPreAnnounce      = flag.Uint64("kmpreannounce", 0, "Duration of Stream Encryption key switchover (packets)")
	KMRefreshRate      = flag.Uint64("kmrefreshrate", 0, "Stream encryption key refresh rate (packets)")
	EnforcedEncryption = flag.Bool("enforcedencryption", false, "Reject connection if parties set different passphrase")
	Latency            = flag.Int("latency", 0, "Maximum accepted transmission latency in milliseconds")
	PeerLatency        = flag.Int("peerlatency", 0, "Minimum receiver latency to be requested by sender in milliseconds")
	RcvLatency         = flag.Int("rcvlatency", 0, "Receiver-side latency in milliseconds")
	FC                 = flag.Uint64("fc", 0, "Flow control window size (packets)")
	SndBuf             = flag.Uint64("sndbuf", 0, "Sender buffer size in bytes")
	RcvBuf             = flag.Uint64("rcvbuf", 0, "Receiver buffer size in bytes")
	MSS                = flag.Uint64("mss", 0, "MTU size")
	PayloadSize        = flag.Uint64("payloadsize", 0, "Maximum payload size in bytes")
	MaxBW              = flag.Int64("maxbw", 0, "Bandwidth limit in bytes/s (-1 for unlimited)")
	InputBW            = flag.Int64("inputbw", 0, "Input bandwidth in bytes")
	MinInputBW         = flag.Int64("mininputbw", 0, "Minimum input bandwidth in bytes")
	OheadBW            = flag.Int64("oheadbw", 0, "Limit bandwidth overhead in percents")
	PeerIdleTimeo      = flag.Int("peeridletimeo", 0, "Peer idle timeout in milliseconds")
	KeepaliveThreshold = flag.Float64("keepalivethreshold", 0.75, "Fraction of peer idle timeout at which to send proactive keepalives (0 to disable, default 0.75)")
	SndDropDelay       = flag.Int("snddropdelay", 0, "Sender's delay before dropping packets in milliseconds")
	IPTOS              = flag.Int("iptos", 0, "IP socket type of service")
	IPTTL              = flag.Int("ipttl", 0, "IP socket 'time to live' option")
	IPv6Only           = flag.Int("ipv6only", -1, "Allow only IPv6 (-1 for default)")
	DriftTracer        = flag.Bool("drifttracer", false, "Enable drift tracer")
	TLPktDrop          = flag.Bool("tlpktdrop", false, "Drop too late packets")
	TSBPDMode          = flag.Bool("tsbpdmode", false, "Enable timestamp-based packet delivery mode")
	MessageAPI         = flag.Bool("messageapi", false, "Enable SRT message mode")
	NAKReport          = flag.Bool("nakreport", false, "Enable periodic NAK reports")
	LossMaxTTL         = flag.Uint64("lossmaxttl", 0, "Packet reorder tolerance")
	PacketFilter       = flag.String("packetfilter", "", "Set up the packet filter")
	Transtype          = flag.String("transtype", "", "Transmission type ('live' or 'file')")
	GroupConnect       = flag.Bool("groupconnect", false, "Accept group connections")
	GroupStabTimeo     = flag.Int("groupstabtimeo", 0, "Group stability timeout in milliseconds")
	AllowPeerIpChange  = flag.Bool("allowpeeripchange", false, "Allow new IP to send data on existing socket id")

	// io_uring configuration flags
	IoUringEnabled      = flag.Bool("iouringenabled", false, "Enable io_uring for per-connection send queues (requires Linux kernel 5.1+)")
	IoUringSendRingSize = flag.Int("iouringsendringsize", 0, "Size of the io_uring ring for per-connection send queues (must be power of 2, 16-1024)")

	// Channel buffer size configuration flags
	NetworkQueueSize = flag.Int("networkqueuesize", 0, "Size of the network queue channel buffer (packets from network)")
	WriteQueueSize   = flag.Int("writequeuesize", 0, "Size of the write queue channel buffer (packets from application writes)")
	ReadQueueSize    = flag.Int("readqueuesize", 0, "Size of the read queue channel buffer (packets ready for application reads)")
	ReceiveQueueSize = flag.Int("receivequeuesize", 0, "Size of the receive queue channel buffer (packets from network before routing to connections)")

	// Packet reordering configuration flags
	PacketReorderAlgorithm = flag.String("packetreorderalgorithm", "", "Packet reordering algorithm: 'list' (default, O(n) insertions) or 'btree' (O(log n) operations, better for large buffers)")
	BTreeDegree            = flag.Int("btreedegree", 0, "B-tree degree for packet reordering (only used if packetreorderalgorithm='btree', default: 32)")

	// io_uring receive configuration flags
	IoUringRecvEnabled        = flag.Bool("iouringrecvenabled", false, "Enable io_uring for receive operations (requires Linux kernel 5.1+)")
	IoUringRecvRingSize       = flag.Int("iouringrecvringsize", 0, "Size of the io_uring receive ring (must be power of 2, 64-32768)")
	IoUringRecvInitialPending = flag.Int("iouringrecvinitialpending", 0, "Initial number of pending receive requests at startup (default: ring size)")
	IoUringRecvBatchSize      = flag.Int("iouringrecvbatchsize", 0, "Batch size for resubmitting receive requests after completions (default: 256)")

	// Timer interval configuration flags
	TickIntervalMs        = flag.Uint64("tickintervalms", 0, "TSBPD delivery tick interval in milliseconds (default: 10)")
	PeriodicNakIntervalMs = flag.Uint64("periodicnakintervalms", 0, "Periodic NAK timer interval in milliseconds (default: 20)")
	PeriodicAckIntervalMs = flag.Uint64("periodicackintervalms", 0, "Periodic ACK timer interval in milliseconds (default: 10)")

	// NAK btree configuration flags
	UseNakBtree              = flag.Bool("usenakbtree", false, "Enable NAK btree for efficient gap detection (auto-enabled with -iouringrecvenabled)")
	NakRecentPercent         = flag.Float64("nakrecentpercent", 0, "Percentage of TSBPD delay for 'too recent' threshold (default: 0.10)")
	NakMergeGap              = flag.Uint64("nakmergegap", 0, "Max sequence gap to merge in NAK consolidation (default: 3)")
	NakConsolidationBudgetMs = flag.Uint64("nakconsolidationbudgetms", 0, "Max time for NAK consolidation in milliseconds (default: 2)")

	// FastNAK configuration flags
	FastNakEnabled       = flag.Bool("fastnakenabled", false, "Enable FastNAK optimization (default: true when NAK btree enabled)")
	FastNakThresholdMs   = flag.Uint64("fastnakthresholdms", 0, "Silent period to trigger FastNAK in milliseconds (default: 50)")
	FastNakRecentEnabled = flag.Bool("fastnakrecentenabled", false, "Add recent gap immediately on FastNAK trigger (default: true)")

	// Sender retransmission configuration flags
	HonorNakOrder = flag.Bool("honornakorder", false, "Retransmit packets in NAK packet order (oldest first)")

	// Sender lockless configuration flags (Phase 1: Lockless Sender)
	UseSendBtree    = flag.Bool("usesendbtree", false, "Enable btree for sender packet storage (O(log n) NAK lookup)")
	SendBtreeDegree = flag.Int("sendbtreesize", 32, "B-tree degree for sender (default: 32)")

	// Sender lock-free ring configuration flags (Phase 2: Lockless Sender)
	UseSendRing    = flag.Bool("usesendring", false, "Enable lock-free ring for sender Push() (requires -usesendbtree)")
	SendRingSize   = flag.Int("sendringsize", 1024, "Sender ring size per shard (default: 1024)")
	SendRingShards = flag.Int("sendringshards", 1, "Sender ring shards (1=strict ordering, >1=high throughput)")

	// Sender control ring configuration flags (Phase 3: Lockless Sender)
	UseSendControlRing    = flag.Bool("usesendcontrolring", false, "Enable lock-free ring for ACK/NAK (requires -usesendring)")
	SendControlRingSize   = flag.Int("sendcontrolringsize", 256, "Sender control ring size per shard (default: 256)")
	SendControlRingShards = flag.Int("sendcontrolringshards", 2, "Sender control ring shards (default: 2)")

	// Sender EventLoop configuration flags (Phase 4: Lockless Sender)
	UseSendEventLoop             = flag.Bool("usesendeventloop", false, "Enable sender EventLoop (requires -usesendcontrolring)")
	SendEventLoopBackoffMinSleep = flag.Duration("sendeventloopbackoffminsleep", 100*time.Microsecond, "Sender EventLoop minimum sleep (default: 100µs)")
	SendEventLoopBackoffMaxSleep = flag.Duration("sendeventloopbackoffmaxsleep", 1*time.Millisecond, "Sender EventLoop maximum sleep (default: 1ms)")
	SendTsbpdSleepFactor         = flag.Float64("sendtsbpdsleepfactor", 0.9, "Sender TSBPD sleep factor (default: 0.9)")
	SendDropThresholdUs          = flag.Uint64("senddropthresholdus", 0, "Sender drop threshold in microseconds (0 = auto-calculated from 1.25 * peerTsbpdDelay)")

	// Zero-copy payload pool flags (Phase 5: Lockless Sender)
	ValidateSendPayloadSize = flag.Bool("validatesendpayloadsize", false, "Validate payload size in Push() (rejects > 1316 bytes)")

	// RTO-based suppression configuration flags (Phase 6: RTO Suppression)
	RTOMode = flag.String("rtomode", "",
		"RTO calculation mode: 'rtt_rttvar' (RTT+RTTVar, default), "+
			"'rtt_4rttvar' (RTT+4*RTTVar, RFC 6298 conservative), "+
			"'rtt_rttvar_margin' (RTT+RTTVar with extra margin)")
	ExtraRTTMargin = flag.Float64("extrarttmargin", 0,
		"Extra RTT margin as decimal (0.1 = 10%, default: 0.1). Only used with rtomode=rtt_rttvar_margin")

	// NAK Btree Expiry Optimization flags (nak_btree_expiry_optimization.md)
	NakExpiryMargin = flag.Float64("nakexpirymargin", 0.10,
		"NAK btree expiry margin as percentage (0.1 = 10%). "+
			"Formula: expiryThreshold = now + (RTO * (1 + nakExpiryMargin)). "+
			"Higher values keep NAK entries longer, favoring recovery over phantom NAK reduction.")
	EWMAWarmupThreshold = flag.Uint("ewmawarmupthreshold", 32,
		"Minimum packets before inter-packet EWMA is considered warm (reliable). "+
			"Set to 0 to disable warm-up check. "+
			"Higher values improve accuracy but delay time-based expiry. "+
			"Default: 32 (balanced for most streams)")

	// Lock-free ring buffer configuration flags (Phase 3: Lockless Design)
	UsePacketRing             = flag.Bool("usepacketring", false, "Enable lock-free ring buffer for packet handoff (decouples io_uring completion from Tick processing)")
	PacketRingSize            = flag.Int("packetringsize", 0, "Capacity of the lock-free ring buffer per shard (must be power of 2, default: 1024)")
	PacketRingShards          = flag.Int("packetringshards", 0, "Number of shards for the lock-free ring (must be power of 2, default: 4)")
	PacketRingMaxRetries      = flag.Int("packetringmaxretries", -1, "Maximum immediate retries before backoff when ring is full (default: 10, -1 = not set)")
	PacketRingBackoffDuration = flag.Duration("packetringbackoffduration", 0, "Delay between backoff retries when ring is full (default: 100µs)")
	PacketRingMaxBackoffs     = flag.Int("packetringmaxbackoffs", -1, "Maximum backoff iterations before dropping packet (0 = unlimited, -1 = not set)")
	PacketRingRetryStrategy   = flag.String("packetringretrystrategy", "",
		"Ring write retry strategy when shard is full: "+
			"'sleep' (default, retry same shard then sleep 100µs), "+
			"'next' (try all shards before sleeping - avoids blocking), "+
			"'random' (try random shards - best load distribution), "+
			"'adaptive' (exponential backoff with jitter), "+
			"'spin' (yield CPU instead of sleep - lowest latency, highest CPU), "+
			"'hybrid' (next + adaptive). "+
			"Empty string uses default 'sleep' strategy.")

	// Event loop configuration flags (Phase 4: Lockless Design)
	UseEventLoop          = flag.Bool("useeventloop", false, "Enable continuous event loop (requires -usepacketring, replaces timer-driven Tick)")
	EventLoopRateInterval = flag.Duration("eventlooprateinterval", 0, "Rate metric calculation interval in event loop (default: 1s)")
	BackoffColdStartPkts  = flag.Int("backoffcoldstartpkts", -1, "Packets before adaptive backoff engages (default: 1000, -1 = not set)")
	BackoffMinSleep       = flag.Duration("backoffminsleep", 0, "Minimum sleep during idle periods (default: 10µs)")
	BackoffMaxSleep       = flag.Duration("backoffmaxsleep", 0, "Maximum sleep during idle periods (default: 1ms)")

	// ACK optimization configuration flags (Phase 5: ACK Optimization)
	LightACKDifference = flag.Int("lightackdifference", -1,
		"Send Light ACK after N contiguous packets progress (default: 64, max: 5000). "+
			"Higher values reduce ACK overhead at high bitrates.")

	// Debug configuration flags
	ReceiverDebug = flag.Bool("receiverdebug", false, "Enable debug logging in receiver (NAK dispatch, ring ops, gap detection)")

	// Statistics configuration flags
	StatisticsPrintInterval = flag.Duration("statisticsinterval", 0, "Interval for printing connection statistics (e.g., 10s). 0 disables periodic statistics printing")

	// Timeout and shutdown configuration flags
	HandshakeTimeout = flag.Duration("handshaketimeout", 0, "Maximum time allowed for complete handshake exchange (e.g., 1.5s). Must be less than peeridletimeo")
	ShutdownDelay    = flag.Duration("shutdowndelay", 0, "Time to wait for graceful shutdown after signal (e.g., 5s)")

	// Local address binding for clients
	LocalAddr = flag.String("localaddr", "", "Local IP address to bind to when connecting (e.g., 127.0.0.20)")

	// Instance name for labeling in metrics and logs
	InstanceName = flag.String("name", "", "Instance name for labeling in metrics, logs, and JSON output (e.g., Control, Test, Server1)")

	// io_uring output configuration flag (client-side)
	// WARNING: This feature uses the 'unsafe' package for direct memory access.
	IoUringOutput = flag.Bool("iouringoutput", false,
		"Enable io_uring for output writes (Linux only, ADVANCED). "+
			"Uses the 'unsafe' package for io_uring's zero-copy interface. "+
			"The unsafe code is isolated in contrib/common/writer_iouring_linux.go. "+
			"For most use cases, the default DirectWriter is recommended.")

	// Stats display period flag
	// This is application-level configuration, NOT part of srt.Config
	// Controls how often the [PUB]/[SUB] throughput display lines are printed
	StatsPeriod = flag.Duration("statsperiod", 1*time.Second,
		"Period for throughput display updates (e.g., 1s, 10s). "+
			"Controls how often the [PUB]/[SUB] lines are printed to stderr.")

	// Output color flag for terminal display
	// This is application-level configuration, NOT part of srt.Config
	OutputColor = flag.String("color", "",
		"ANSI color for terminal output (red, green, yellow, blue, magenta, cyan). "+
			"Useful for distinguishing between baseline and highperf pipelines in parallel tests.")

	// Prometheus metrics endpoint flags
	// These are application-level configuration, NOT part of srt.Config
	// By default (when not specified), NO metrics listeners are opened
	PromHTTPAddr = flag.String("promhttp", "",
		"TCP address for Prometheus metrics HTTP endpoint (e.g., :9090 or 127.0.0.1:9090). "+
			"If not specified, no TCP metrics listener is opened.")
	PromUDSPath = flag.String("promuds", "",
		"Unix Domain Socket path for Prometheus metrics endpoint "+
			"(e.g., /tmp/srt_metrics_server.sock). "+
			"If not specified, no UDS metrics listener is opened. "+
			"UDS allows metrics collection from processes in isolated network namespaces.")
)

// ParseFlags parses command-line flags and populates FlagSet map
// with flags that were explicitly set by the user.
func ParseFlags() {
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		FlagSet[f.Name] = true
	})
}

// ApplyFlagsToConfig applies CLI flag values to the provided config.
// Only flags that were explicitly set (tracked in FlagSet map) override the default config.
func ApplyFlagsToConfig(config *srt.Config) {
	if FlagSet["congestion"] {
		config.Congestion = *Congestion
	}
	if FlagSet["conntimeo"] {
		config.ConnectionTimeout = time.Duration(*Conntimeo) * time.Millisecond
	}
	if FlagSet["streamid"] {
		config.StreamId = *Streamid
	}
	if FlagSet["passphrase-flag"] {
		config.Passphrase = *PassphraseFlag
	}
	if FlagSet["pbkeylen"] {
		config.PBKeylen = *PBKeylen
	}
	if FlagSet["kmpreannounce"] {
		config.KMPreAnnounce = *KMPreAnnounce
	}
	if FlagSet["kmrefreshrate"] {
		config.KMRefreshRate = *KMRefreshRate
	}
	if FlagSet["enforcedencryption"] {
		config.EnforcedEncryption = *EnforcedEncryption
	}
	if FlagSet["latency"] {
		config.Latency = time.Duration(*Latency) * time.Millisecond
	}
	if FlagSet["peerlatency"] {
		config.PeerLatency = time.Duration(*PeerLatency) * time.Millisecond
	}
	if FlagSet["rcvlatency"] {
		config.ReceiverLatency = time.Duration(*RcvLatency) * time.Millisecond
	}
	if FlagSet["fc"] {
		config.FC = uint32(*FC)
	}
	if FlagSet["sndbuf"] {
		config.SendBufferSize = uint32(*SndBuf)
	}
	if FlagSet["rcvbuf"] {
		config.ReceiverBufferSize = uint32(*RcvBuf)
	}
	if FlagSet["mss"] {
		config.MSS = uint32(*MSS)
	}
	if FlagSet["payloadsize"] {
		config.PayloadSize = uint32(*PayloadSize)
	}
	if FlagSet["maxbw"] {
		config.MaxBW = *MaxBW
	}
	if FlagSet["inputbw"] {
		config.InputBW = *InputBW
	}
	if FlagSet["mininputbw"] {
		config.MinInputBW = *MinInputBW
	}
	if FlagSet["oheadbw"] {
		config.OverheadBW = *OheadBW
	}
	if FlagSet["peeridletimeo"] {
		config.PeerIdleTimeout = time.Duration(*PeerIdleTimeo) * time.Millisecond
	}
	if FlagSet["keepalivethreshold"] {
		config.KeepaliveThreshold = *KeepaliveThreshold
	}
	if FlagSet["snddropdelay"] {
		config.SendDropDelay = time.Duration(*SndDropDelay) * time.Millisecond
	}
	if FlagSet["iptos"] {
		config.IPTOS = *IPTOS
	}
	if FlagSet["ipttl"] {
		config.IPTTL = *IPTTL
	}
	if FlagSet["ipv6only"] {
		config.IPv6Only = *IPv6Only
	}
	if FlagSet["drifttracer"] {
		config.DriftTracer = *DriftTracer
	}
	if FlagSet["tlpktdrop"] {
		config.TooLatePacketDrop = *TLPktDrop
	}
	if FlagSet["tsbpdmode"] {
		config.TSBPDMode = *TSBPDMode
	}
	if FlagSet["messageapi"] {
		config.MessageAPI = *MessageAPI
	}
	if FlagSet["nakreport"] {
		config.NAKReport = *NAKReport
	}
	if FlagSet["lossmaxttl"] {
		config.LossMaxTTL = uint32(*LossMaxTTL)
	}
	if FlagSet["packetfilter"] {
		config.PacketFilter = *PacketFilter
	}
	if FlagSet["transtype"] {
		config.TransmissionType = *Transtype
	}
	if FlagSet["groupconnect"] {
		config.GroupConnect = *GroupConnect
	}
	if FlagSet["groupstabtimeo"] {
		config.GroupStabilityTimeout = time.Duration(*GroupStabTimeo) * time.Millisecond
	}
	if FlagSet["allowpeeripchange"] {
		config.AllowPeerIpChange = *AllowPeerIpChange
	}
	if FlagSet["iouringenabled"] {
		config.IoUringEnabled = *IoUringEnabled
	}
	if FlagSet["iouringsendringsize"] {
		config.IoUringSendRingSize = *IoUringSendRingSize
	}
	if FlagSet["networkqueuesize"] {
		config.NetworkQueueSize = *NetworkQueueSize
	}
	if FlagSet["writequeuesize"] {
		config.WriteQueueSize = *WriteQueueSize
	}
	if FlagSet["readqueuesize"] {
		config.ReadQueueSize = *ReadQueueSize
	}
	if FlagSet["receivequeuesize"] {
		config.ReceiveQueueSize = *ReceiveQueueSize
	}
	if FlagSet["packetreorderalgorithm"] {
		config.PacketReorderAlgorithm = *PacketReorderAlgorithm
	}
	if FlagSet["btreedegree"] {
		config.BTreeDegree = *BTreeDegree
	}
	if FlagSet["iouringrecvenabled"] {
		config.IoUringRecvEnabled = *IoUringRecvEnabled
	}
	if FlagSet["iouringrecvringsize"] {
		config.IoUringRecvRingSize = *IoUringRecvRingSize
	}
	if FlagSet["iouringrecvinitialpending"] {
		config.IoUringRecvInitialPending = *IoUringRecvInitialPending
	}
	if FlagSet["iouringrecvbatchsize"] {
		config.IoUringRecvBatchSize = *IoUringRecvBatchSize
	}

	// Timer interval flags
	if FlagSet["tickintervalms"] {
		config.TickIntervalMs = *TickIntervalMs
	}
	if FlagSet["periodicnakintervalms"] {
		config.PeriodicNakIntervalMs = *PeriodicNakIntervalMs
	}
	if FlagSet["periodicackintervalms"] {
		config.PeriodicAckIntervalMs = *PeriodicAckIntervalMs
	}

	// NAK btree flags
	if FlagSet["usenakbtree"] {
		config.UseNakBtree = *UseNakBtree
	}
	if FlagSet["nakrecentpercent"] {
		config.NakRecentPercent = *NakRecentPercent
	}
	if FlagSet["nakmergegap"] {
		config.NakMergeGap = uint32(*NakMergeGap)
	}
	if FlagSet["nakconsolidationbudgetms"] {
		config.NakConsolidationBudgetUs = *NakConsolidationBudgetMs * 1000 // Convert ms to µs
	}

	// FastNAK flags
	if FlagSet["fastnakenabled"] {
		config.FastNakEnabled = *FastNakEnabled
	}
	if FlagSet["fastnakthresholdms"] {
		config.FastNakThresholdMs = *FastNakThresholdMs
	}
	if FlagSet["fastnakrecentenabled"] {
		config.FastNakRecentEnabled = *FastNakRecentEnabled
	}

	// Sender flags
	if FlagSet["honornakorder"] {
		config.HonorNakOrder = *HonorNakOrder
	}

	// Sender lockless flags (Phase 1: Lockless Sender)
	if FlagSet["usesendbtree"] {
		config.UseSendBtree = *UseSendBtree
	}
	if FlagSet["sendbtreesize"] {
		config.SendBtreeDegree = *SendBtreeDegree
	}

	// Sender lock-free ring flags (Phase 2: Lockless Sender)
	if FlagSet["usesendring"] {
		config.UseSendRing = *UseSendRing
	}
	if FlagSet["sendringsize"] {
		config.SendRingSize = *SendRingSize
	}
	if FlagSet["sendringshards"] {
		config.SendRingShards = *SendRingShards
	}

	// Sender control ring flags (Phase 3: Lockless Sender)
	if FlagSet["usesendcontrolring"] {
		config.UseSendControlRing = *UseSendControlRing
	}
	if FlagSet["sendcontrolringsize"] {
		config.SendControlRingSize = *SendControlRingSize
	}
	if FlagSet["sendcontrolringshards"] {
		config.SendControlRingShards = *SendControlRingShards
	}

	// Sender EventLoop flags (Phase 4: Lockless Sender)
	if FlagSet["usesendeventloop"] {
		config.UseSendEventLoop = *UseSendEventLoop
	}
	if FlagSet["sendeventloopbackoffminsleep"] {
		config.SendEventLoopBackoffMinSleep = *SendEventLoopBackoffMinSleep
	}
	if FlagSet["sendeventloopbackoffmaxsleep"] {
		config.SendEventLoopBackoffMaxSleep = *SendEventLoopBackoffMaxSleep
	}
	if FlagSet["sendtsbpdsleepfactor"] {
		config.SendTsbpdSleepFactor = *SendTsbpdSleepFactor
	}
	if FlagSet["senddropthresholdus"] {
		config.SendDropThresholdUs = *SendDropThresholdUs
	}

	// Zero-copy payload pool flags (Phase 5: Lockless Sender)
	if *ValidateSendPayloadSize {
		config.ValidateSendPayloadSize = true
	}

	// RTO suppression flags (Phase 6: RTO Suppression)
	if FlagSet["rtomode"] {
		switch *RTOMode {
		case "rtt_rttvar":
			config.RTOMode = srt.RTORttRttVar
		case "rtt_4rttvar":
			config.RTOMode = srt.RTORtt4RttVar
		case "rtt_rttvar_margin":
			config.RTOMode = srt.RTORttRttVarMargin
		}
	}
	if FlagSet["extrarttmargin"] {
		config.ExtraRTTMargin = *ExtraRTTMargin
	}

	// NAK Btree Expiry Optimization flags (nak_btree_expiry_optimization.md)
	if FlagSet["nakexpirymargin"] {
		config.NakExpiryMargin = *NakExpiryMargin
		// Validate: values < -1.0 would create threshold in the past
		if config.NakExpiryMargin < -1.0 {
			log.Printf("WARNING: NakExpiryMargin %.2f invalid (< -1.0), resetting to 0.10", config.NakExpiryMargin)
			config.NakExpiryMargin = 0.10
		}
	}
	if FlagSet["ewmawarmupthreshold"] {
		config.EWMAWarmupThreshold = uint32(*EWMAWarmupThreshold)
	}

	// Lock-free ring buffer flags (Phase 3: Lockless Design)
	if FlagSet["usepacketring"] {
		config.UsePacketRing = *UsePacketRing
	}
	if FlagSet["packetringsize"] {
		config.PacketRingSize = *PacketRingSize
	}
	if FlagSet["packetringshards"] {
		config.PacketRingShards = *PacketRingShards
	}
	if FlagSet["packetringmaxretries"] && *PacketRingMaxRetries >= 0 {
		config.PacketRingMaxRetries = *PacketRingMaxRetries
	}
	if FlagSet["packetringbackoffduration"] {
		config.PacketRingBackoffDuration = *PacketRingBackoffDuration
	}
	if FlagSet["packetringmaxbackoffs"] && *PacketRingMaxBackoffs >= 0 {
		config.PacketRingMaxBackoffs = *PacketRingMaxBackoffs
	}
	if FlagSet["packetringretrystrategy"] {
		config.PacketRingRetryStrategy = *PacketRingRetryStrategy
	}

	// Event loop flags (Phase 4: Lockless Design)
	if FlagSet["useeventloop"] {
		config.UseEventLoop = *UseEventLoop
	}
	if FlagSet["eventlooprateinterval"] {
		config.EventLoopRateInterval = *EventLoopRateInterval
	}
	if FlagSet["backoffcoldstartpkts"] && *BackoffColdStartPkts >= 0 {
		config.BackoffColdStartPkts = *BackoffColdStartPkts
	}
	if FlagSet["backoffminsleep"] {
		config.BackoffMinSleep = *BackoffMinSleep
	}
	if FlagSet["backoffmaxsleep"] {
		config.BackoffMaxSleep = *BackoffMaxSleep
	}

	// ACK optimization flags (Phase 5: ACK Optimization)
	if FlagSet["lightackdifference"] && *LightACKDifference > 0 {
		config.LightACKDifference = uint32(*LightACKDifference)
	}

	// Debug flags
	if FlagSet["receiverdebug"] {
		config.ReceiverDebug = *ReceiverDebug
	}

	if FlagSet["statisticsinterval"] {
		config.StatisticsPrintInterval = *StatisticsPrintInterval
	}
	if FlagSet["handshaketimeout"] {
		config.HandshakeTimeout = *HandshakeTimeout
	}
	if FlagSet["shutdowndelay"] {
		config.ShutdownDelay = *ShutdownDelay
	}
	if FlagSet["localaddr"] {
		config.LocalAddr = *LocalAddr
	}
	if FlagSet["name"] {
		config.InstanceName = *InstanceName
	}
}
