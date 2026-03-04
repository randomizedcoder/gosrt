package common

import (
	"flag"
	"fmt"
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

	// Multiple io_uring rings configuration flags (Phase 1: multi_iouring_design.md)
	IoUringRecvRingCount = flag.Int("iouringrecvringcount", 1, "Number of io_uring receive rings for parallel processing (1-16)")
	IoUringSendRingCount = flag.Int("iouringsendringcount", 1, "Number of io_uring send rings per connection (1-8)")

	// Timer interval configuration flags
	TickIntervalMs          = flag.Uint64("tickintervalms", 0, "TSBPD delivery tick interval in milliseconds (default: 10)")
	PeriodicNakIntervalMs   = flag.Uint64("periodicnakintervalms", 0, "Periodic NAK timer interval in milliseconds (default: 20)")
	PeriodicAckIntervalMs   = flag.Uint64("periodicackintervalms", 0, "Periodic ACK timer interval in milliseconds (default: 10)")
	SendDropIntervalMs      = flag.Uint64("senddropintervalms", 0, "Sender drop ticker interval in milliseconds (default: 100)")
	EventLoopRateIntervalMs = flag.Uint64("eventlooprateintervalms", 0, "Rate calculation interval in milliseconds (default: 1000)")

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
	SendControlRingSize   = flag.Int("sendcontrolringsize", 128, "Sender control ring size per shard (default: 128)")
	SendControlRingShards = flag.Int("sendcontrolringshards", 1, "Sender control ring shards (default: 1)")

	// Receiver control ring configuration flags (Completely Lock-Free Receiver)
	UseRecvControlRing    = flag.Bool("userecvcontrolring", false, "Enable lock-free ring for ACKACK/KEEPALIVE (requires -useeventloop)")
	RecvControlRingSize   = flag.Int("recvcontrolringsize", 128, "Receiver control ring size per shard (default: 128)")
	RecvControlRingShards = flag.Int("recvcontrolringshards", 1, "Receiver control ring shards (default: 1)")

	// Sender EventLoop configuration flags (Phase 4: Lockless Sender)
	UseSendEventLoop             = flag.Bool("usesendeventloop", false, "Enable sender EventLoop (requires -usesendcontrolring)")
	SendEventLoopBackoffMinSleep = flag.Duration("sendeventloopbackoffminsleep", 100*time.Microsecond, "Sender EventLoop minimum sleep (default: 100µs)")
	SendEventLoopBackoffMaxSleep = flag.Duration("sendeventloopbackoffmaxsleep", 1*time.Millisecond, "Sender EventLoop maximum sleep (default: 1ms)")
	SendTsbpdSleepFactor         = flag.Float64("sendtsbpdsleepfactor", 0.9, "Sender TSBPD sleep factor (default: 0.9)")
	SendDropThresholdUs          = flag.Uint64("senddropthresholdus", 0, "Sender drop threshold in microseconds (0 = auto-calculated from 1.25 * peerTsbpdDelay)")

	// Adaptive Backoff flags (adaptive_eventloop_mode_design.md)
	// Switches between Yield (~6M ops/sec) and Sleep (~1K ops/sec) based on activity
	UseAdaptiveBackoff           = flag.Bool("useadaptivebackoff", true, "Enable adaptive Yield/Sleep mode switching in EventLoop (Yield=high throughput, Sleep=CPU friendly when idle)")
	AdaptiveBackoffIdleThreshold = flag.Duration("adaptivebackoffidlethreshold", 1*time.Second, "Duration without activity before switching to Sleep mode (default: 1s)")

	// EventLoop Tight Loop flags (eventloop_batch_sizing_design.md)
	// Enables control-priority tight loop: control ring checked after EVERY data packet
	// -1 = use library default (512 for tight loop)
	// 0 = unbounded legacy mode (no tight loop)
	// >0 = tight loop with specified batch size
	EventLoopMaxDataPerIteration = flag.Int("eventloopmaxdata", -1, "Max data packets per EventLoop iteration (-1 = default 512, 0 = unbounded legacy, >0 = custom)")

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
			"'hybrid' (next + adaptive), "+
			"'autoadaptive' or 'auto' (⭐ RECOMMENDED for >300 Mb/s: Yield when active, Sleep when idle). "+
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

	// ════════════════════════════════════════════════════════════════════════════
	// Performance Test Flags
	// Used by: contrib/performance/, contrib/client-seeker/
	// Reference: performance_tools_flag_unification.md
	// ════════════════════════════════════════════════════════════════════════════

	// Search parameters (used by performance orchestrator)
	TestInitialBitrate = flag.Int64("initial", 200_000_000,
		"Starting bitrate for performance search in bps (default: 200M). "+
			"Supports suffixes: K, M, G (e.g., -initial 350M)")
	TestMinBitrate = flag.Int64("min-bitrate", 50_000_000,
		"Minimum bitrate floor in bps (default: 50M)")
	TestMaxBitrate = flag.Int64("max-bitrate", 600_000_000,
		"Maximum bitrate ceiling in bps (default: 600M)")
	TestStepSize = flag.Int64("step", 10_000_000,
		"Additive increase step in bps (default: 10M)")
	TestPrecision = flag.Int64("precision", 5_000_000,
		"Search stops when high-low < precision (default: 5M)")
	TestSearchTimeout = flag.Duration("search-timeout", 10*time.Minute,
		"Maximum search time (default: 10m)")
	TestDecreasePercent = flag.Float64("decrease", 0.25,
		"Multiplicative decrease on failure (default: 0.25 = 25%)")

	// Stability evaluation parameters
	TestWarmUpDuration = flag.Duration("warmup", 2*time.Second,
		"Warm-up duration after bitrate change (default: 2s)")
	TestStabilityWindow = flag.Duration("stability-window", 5*time.Second,
		"Stability evaluation window (default: 5s)")
	TestSampleInterval = flag.Duration("sample-interval", 500*time.Millisecond,
		"Prometheus scrape interval (default: 500ms)")

	// Stability thresholds
	TestMaxGapRate = flag.Float64("max-gap-rate", 0.01,
		"Max gaps per second for stability (default: 0.01)")
	TestMaxNAKRate = flag.Float64("max-nak-rate", 0.02,
		"Max NAKs per second for stability (default: 0.02)")
	TestMaxRTTMs = flag.Float64("max-rtt", 100,
		"Max RTT in milliseconds for stability (default: 100)")
	TestMinThroughput = flag.Float64("min-throughput", 0.95,
		"Min throughput ratio vs target (default: 0.95)")

	// Test output options
	TestVerbose = flag.Bool("test-verbose", false,
		"Enable verbose test output")
	TestJSONOutput = flag.Bool("test-json", false,
		"Output results as JSON")
	TestOutputFile = flag.String("test-output", "",
		"Path for test result output file")
	TestProfileDir = flag.String("profile-dir", "/tmp/srt_profiles",
		"Directory for CPU/heap profile captures")
	TestStatusInterval = flag.Duration("status-interval", 5*time.Second,
		"Interval for printing progress status during search (default: 5s, 0=disabled)")

	// Client-seeker specific flags
	SeekerTarget = flag.String("target", "",
		"SRT target URL for client-seeker (e.g., srt://127.0.0.1:6000/stream)")
	SeekerControlUDS = flag.String("control-socket", "/tmp/srt_seeker_control.sock",
		"Unix domain socket path for seeker control commands")
	SeekerMetricsUDS = flag.String("metrics-socket", "/tmp/srt_seeker_metrics.sock",
		"Unix domain socket path for seeker Prometheus metrics")
	SeekerWatchdogTimeout = flag.Duration("watchdog-timeout", 10*time.Second,
		"Watchdog timeout - stop if no heartbeat received (default: 10s)")
	SeekerHeartbeatInterval = flag.Duration("heartbeat-interval", 2*time.Second,
		"Expected heartbeat interval from orchestrator (default: 2s)")
)

// testOnlyFlags lists flags that should NOT be passed to subprocesses.
// These are orchestrator/test-specific parameters.
var testOnlyFlags = map[string]bool{
	"initial":            true,
	"min-bitrate":        true,
	"max-bitrate":        true,
	"step":               true,
	"precision":          true,
	"search-timeout":     true,
	"decrease":           true,
	"warmup":             true,
	"stability-window":   true,
	"sample-interval":    true,
	"max-gap-rate":       true,
	"max-nak-rate":       true,
	"max-rtt":            true,
	"min-throughput":     true,
	"test-verbose":       true,
	"test-json":          true,
	"test-output":        true,
	"profile-dir":        true,
	"target":             true,
	"control-socket":     true,
	"metrics-socket":     true,
	"watchdog-timeout":   true,
	"heartbeat-interval": true,
}

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
//
// This function delegates to the table-driven implementation in flags_applicators.go,
// reducing cyclomatic complexity from 116 to ~10.
func ApplyFlagsToConfig(config *srt.Config) {
	applyFlagsToConfigTable(config)
}

// BuildFlagArgs returns CLI arguments for all explicitly-set flags.
// Used by the performance orchestrator to spawn subprocesses (server, client-seeker)
// with the same SRT configuration.
//
// Excludes test-only flags (search params, stability thresholds, etc.) that are
// orchestrator-specific and shouldn't be passed to subprocesses.
//
// Example output: ["-fc=102400", "-rcvbuf=67108864", "-useeventloop", ...]
func BuildFlagArgs() []string {
	var args []string
	flag.Visit(func(f *flag.Flag) {
		// Skip test-only flags (not for subprocesses)
		if testOnlyFlags[f.Name] {
			return
		}
		// Format: -name=value
		args = append(args, fmt.Sprintf("-%s=%s", f.Name, f.Value.String()))
	})
	return args
}

// BuildFlagArgsFiltered returns CLI arguments for explicitly-set flags,
// excluding the specified flag names.
//
// Use this when you need to override certain flags for a subprocess.
// Example: exclude "-addr" when spawning server on a different port.
func BuildFlagArgsFiltered(exclude ...string) []string {
	excludeMap := make(map[string]bool)
	for _, name := range exclude {
		excludeMap[name] = true
	}

	var args []string
	flag.Visit(func(f *flag.Flag) {
		// Skip test-only flags
		if testOnlyFlags[f.Name] {
			return
		}
		// Skip excluded flags
		if excludeMap[f.Name] {
			return
		}
		args = append(args, fmt.Sprintf("-%s=%s", f.Name, f.Value.String()))
	})
	return args
}

// IsTestFlag returns true if the flag name is a test-only flag.
func IsTestFlag(name string) bool {
	return testOnlyFlags[name]
}

// PrintFlagSummary prints a summary of explicitly-set flags for debugging.
func PrintFlagSummary() {
	fmt.Println("=== Explicitly Set Flags ===")
	flag.Visit(func(f *flag.Flag) {
		category := "SRT"
		if testOnlyFlags[f.Name] {
			category = "TEST"
		}
		fmt.Printf("  [%s] -%s = %s\n", category, f.Name, f.Value.String())
	})
	fmt.Println("============================")
}

// ValidateFlagDependencies checks flag combinations and auto-enables dependencies.
// It returns a list of warnings for any auto-enabled flags.
// Call this after flag.Parse() to ensure consistent configuration.
func ValidateFlagDependencies() []string {
	var warnings []string

	// Sender dependency chain: UseSendEventLoop → UseSendControlRing → UseSendRing → UseSendBtree
	if FlagSet["usesendeventloop"] && *UseSendEventLoop {
		if !FlagSet["usesendcontrolring"] || !*UseSendControlRing {
			*UseSendControlRing = true
			if !FlagSet["usesendcontrolring"] {
				warnings = append(warnings, "Auto-enabled -usesendcontrolring (required by -usesendeventloop)")
			}
		}
	}
	if FlagSet["usesendcontrolring"] && *UseSendControlRing {
		if !FlagSet["usesendring"] || !*UseSendRing {
			*UseSendRing = true
			if !FlagSet["usesendring"] {
				warnings = append(warnings, "Auto-enabled -usesendring (required by -usesendcontrolring)")
			}
		}
	}
	if FlagSet["usesendring"] && *UseSendRing {
		if !FlagSet["usesendbtree"] || !*UseSendBtree {
			*UseSendBtree = true
			if !FlagSet["usesendbtree"] {
				warnings = append(warnings, "Auto-enabled -usesendbtree (required by -usesendring)")
			}
		}
	}

	// Receiver dependency chain: UseEventLoop → UsePacketRing
	if FlagSet["useeventloop"] && *UseEventLoop {
		if !FlagSet["usepacketring"] || !*UsePacketRing {
			*UsePacketRing = true
			if !FlagSet["usepacketring"] {
				warnings = append(warnings, "Auto-enabled -usepacketring (required by -useeventloop)")
			}
		}
	}

	// Receiver control ring dependency: UseRecvControlRing → UseEventLoop → UsePacketRing
	if FlagSet["userecvcontrolring"] && *UseRecvControlRing {
		if !FlagSet["useeventloop"] || !*UseEventLoop {
			*UseEventLoop = true
			if !FlagSet["useeventloop"] {
				warnings = append(warnings, "Auto-enabled -useeventloop (required by -userecvcontrolring)")
			}
		}
		if !FlagSet["usepacketring"] || !*UsePacketRing {
			*UsePacketRing = true
			if !FlagSet["usepacketring"] {
				warnings = append(warnings, "Auto-enabled -usepacketring (required by -userecvcontrolring)")
			}
		}
	}

	// io_uring receive rings require io_uring to be enabled
	if FlagSet["iouringrecvringcount"] && *IoUringRecvRingCount > 1 {
		if !FlagSet["iouringenabled"] || !*IoUringEnabled {
			*IoUringEnabled = true
			if !FlagSet["iouringenabled"] {
				warnings = append(warnings, "Auto-enabled -iouringenabled (required by -iouringrecvringcount)")
			}
		}
		if !FlagSet["iouringrecvenabled"] || !*IoUringRecvEnabled {
			*IoUringRecvEnabled = true
			if !FlagSet["iouringrecvenabled"] {
				warnings = append(warnings, "Auto-enabled -iouringrecvenabled (required by -iouringrecvringcount)")
			}
		}
	}

	return warnings
}
