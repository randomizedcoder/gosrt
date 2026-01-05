package srt

import (
	"time"

	"github.com/randomizedcoder/gosrt/packet"
)

const (
	UDP_HEADER_SIZE     = 28
	SRT_HEADER_SIZE     = 16
	MIN_MSS_SIZE        = 76
	MAX_MSS_SIZE        = 1500
	MIN_PAYLOAD_SIZE    = MIN_MSS_SIZE - UDP_HEADER_SIZE - SRT_HEADER_SIZE
	MAX_PAYLOAD_SIZE    = MAX_MSS_SIZE - UDP_HEADER_SIZE - SRT_HEADER_SIZE
	MIN_PASSPHRASE_SIZE = 10
	MAX_PASSPHRASE_SIZE = 80
	MAX_STREAMID_SIZE   = 512
	SRT_VERSION         = 0x010401
)

// RTOMode defines the RTO calculation strategy for NAK/retransmit suppression.
// Used for both NAK suppression (full RTO) and retransmit suppression (RTO/2).
type RTOMode uint8

const (
	RTORttRttVar       RTOMode = iota // RTT + RTTVar (balanced default)
	RTORtt4RttVar                     // RTT + 4*RTTVar (RFC 6298 conservative)
	RTORttRttVarMargin                // (RTT + RTTVar) * (1 + ExtraRTTMargin)
)

// String returns the string representation of RTOMode.
func (m RTOMode) String() string {
	switch m {
	case RTORtt4RttVar:
		return "rtt_4rttvar"
	case RTORttRttVarMargin:
		return "rtt_rttvar_margin"
	default:
		// RTORttRttVar (0) is the default, and any unknown value defaults to it
		return "rtt_rttvar"
	}
}

// Config is the configuration for a SRT connection
type Config struct {
	// Type of congestion control. 'live' or 'file'
	// SRTO_CONGESTION
	Congestion string

	// Connection timeout.
	// SRTO_CONNTIMEO
	ConnectionTimeout time.Duration

	// Enable drift tracer.
	// SRTO_DRIFTTRACER
	DriftTracer bool

	// Reject connection if parties set different passphrase.
	// SRTO_ENFORCEDENCRYPTION
	EnforcedEncryption bool

	// Flow control window size. Packets.
	// SRTO_FC
	FC uint32

	// Accept group connections.
	// SRTO_GROUPCONNECT
	GroupConnect bool

	// Group stability timeout.
	// SRTO_GROUPSTABTIMEO
	GroupStabilityTimeout time.Duration

	// Input bandwidth. Bytes.
	// SRTO_INPUTBW
	InputBW int64

	// IP socket type of service
	// SRTO_IPTOS
	IPTOS int

	// Defines IP socket "time to live" option.
	// SRTO_IPTTL
	IPTTL int

	// Allow only IPv6.
	// SRTO_IPV6ONLY
	IPv6Only int

	// Duration of Stream Encryption key switchover. Packets.
	// SRTO_KMPREANNOUNCE
	KMPreAnnounce uint64

	// Stream encryption key refresh rate. Packets.
	// SRTO_KMREFRESHRATE
	KMRefreshRate uint64

	// Defines the maximum accepted transmission latency.
	// SRTO_LATENCY
	Latency time.Duration

	// Packet reorder tolerance.
	// SRTO_LOSSMAXTTL
	LossMaxTTL uint32

	// Bandwidth limit in bytes/s.
	// SRTO_MAXBW
	MaxBW int64

	// Enable SRT message mode.
	// SRTO_MESSAGEAPI
	MessageAPI bool

	// Minimum input bandwidth
	// This option is effective only if both SRTO_MAXBW and SRTO_INPUTBW are set to 0. It controls the minimum allowed value of the input bitrate estimate.
	// SRTO_MININPUTBW
	MinInputBW int64

	// Minimum SRT library version of a peer.
	// SRTO_MINVERSION
	MinVersion uint32

	// MTU size
	// SRTO_MSS
	MSS uint32

	// Enable periodic NAK reports
	// SRTO_NAKREPORT
	NAKReport bool

	// Limit bandwidth overhead, percents
	// SRTO_OHEADBW
	OverheadBW int64

	// Set up the packet filter.
	// SRTO_PACKETFILTER
	PacketFilter string

	// Password for the encrypted transmission.
	// SRTO_PASSPHRASE
	Passphrase string

	// Maximum payload size. Bytes.
	// SRTO_PAYLOADSIZE
	PayloadSize uint32

	// Crypto key length in bytes.
	// SRTO_PBKEYLEN
	PBKeylen int

	// Peer idle timeout.
	// SRTO_PEERIDLETIMEO
	PeerIdleTimeout time.Duration

	// KeepaliveThreshold is the fraction of PeerIdleTimeout at which to send
	// proactive keepalive packets. This keeps connections alive during idle periods.
	// Default: 0.75 (75% of PeerIdleTimeout).
	// Set to 0 or negative to disable proactive keepalives.
	// Valid range: 0.0 to 1.0 (0 = disabled, values >= 1.0 are treated as disabled)
	KeepaliveThreshold float64

	// Minimum receiver latency to be requested by sender.
	// SRTO_PEERLATENCY
	PeerLatency time.Duration

	// Receiver buffer size. Bytes.
	// SRTO_RCVBUF
	ReceiverBufferSize uint32

	// Receiver-side latency.
	// SRTO_RCVLATENCY
	ReceiverLatency time.Duration

	// Sender buffer size. Bytes.
	// SRTO_SNDBUF
	SendBufferSize uint32

	// Sender's delay before dropping packets.
	// SRTO_SNDDROPDELAY
	SendDropDelay time.Duration

	// Stream ID (settable in caller mode only, visible on the listener peer)
	// SRTO_STREAMID
	StreamId string

	// InstanceName is a user-defined label for this connection/server instance
	// Used in logging, metrics labels, and JSON statistics output
	// Default: "" (empty = not set)
	InstanceName string

	// Drop too late packets.
	// SRTO_TLPKTDROP
	TooLatePacketDrop bool

	// Transmission type. 'live' or 'file'.
	// SRTO_TRANSTYPE
	TransmissionType string

	// Timestamp-based packet delivery mode.
	// SRTO_TSBPDMODE
	TSBPDMode bool

	// An implementation of the Logger interface
	Logger Logger

	// if a new IP starts sending data on an existing socket id, allow it
	AllowPeerIpChange bool

	// Enable io_uring for per-connection send queues (requires Linux kernel 5.1+)
	// When enabled, each connection uses its own io_uring ring for asynchronous sends
	IoUringEnabled bool

	// Size of the io_uring ring for per-connection send queues (must be power of 2, 16-1024)
	// Default: 64. Smaller rings use less memory but may limit throughput per connection
	IoUringSendRingSize int

	// Size of the network queue channel buffer (packets from network)
	// Default: 1024. Larger buffers reduce packet drops but use more memory
	NetworkQueueSize int

	// Size of the write queue channel buffer (packets from application writes)
	// Default: 1024. Larger buffers reduce write blocking but use more memory
	WriteQueueSize int

	// Size of the read queue channel buffer (packets ready for application reads)
	// Default: 1024. Larger buffers reduce read blocking but use more memory
	ReadQueueSize int

	// Size of the receive queue channel buffer (packets from network before routing to connections)
	// Used by listener and dialer. Default: 2048. Larger buffers reduce packet drops but use more memory
	ReceiveQueueSize int

	// Packet reordering algorithm for congestion control receiver
	// "list" (default) uses container/list.List - simpler, O(n) insertions
	// "btree" uses github.com/google/btree - better for large buffers/high reordering, O(log n) operations
	PacketReorderAlgorithm string

	// B-tree degree for packet reordering (only used if PacketReorderAlgorithm == "btree")
	// Default: 32. Higher values use more memory but may reduce tree height
	BTreeDegree int

	// Enable io_uring for receive operations (requires Linux kernel 5.1+)
	// When enabled, replaces blocking ReadFrom() with asynchronous io_uring RecvMsg
	IoUringRecvEnabled bool

	// Size of the io_uring receive ring (must be power of 2, 64-32768)
	// Default: 512. Larger rings allow more pending receives but use more memory
	IoUringRecvRingSize int

	// Initial number of pending receive requests at startup
	// Default: ring size (full ring). Must be <= IoUringRecvRingSize
	IoUringRecvInitialPending int

	// Batch size for resubmitting receive requests after completions
	// Default: 256. Larger batches reduce syscall overhead but increase latency
	IoUringRecvBatchSize int

	// Statistics print interval for server connections
	// If > 0, server will periodically print statistics for all active connections
	// Default: 0 (disabled). Set to e.g. 10s to print statistics every 10 seconds
	StatisticsPrintInterval time.Duration

	// Metrics configuration
	// Enable metrics collection and expose /metrics endpoint
	MetricsEnabled bool

	// HTTP address for /metrics endpoint (e.g., ":9090")
	// If empty, metrics server is not started
	// Default: "" (disabled)
	MetricsListenAddr string

	// Handshake timeout for complete handshake exchange (induction + conclusion)
	// Must be less than PeerIdleTimeout
	// Default: 1.5 seconds (SRT is designed for low loss/low RTT networks)
	HandshakeTimeout time.Duration

	// Shutdown delay - time to wait for graceful shutdown after signal before application exit
	// Default: 5 seconds
	ShutdownDelay time.Duration

	// Local address to bind to when dialing (client only)
	// Format: "IP" or "IP:port" (e.g., "127.0.0.20" or "127.0.0.20:0")
	// If empty, the system chooses an ephemeral address
	// Only used by Dial(), not Listen()
	LocalAddr string

	// --- NAK btree Configuration (for io_uring receive path) ---

	// Timer intervals (replaces hardcoded 10ms/20ms values)
	// TickIntervalMs is the TSBPD delivery tick interval in milliseconds
	// Default: 10. Lower = lower latency but higher CPU. Higher = higher latency but lower CPU.
	TickIntervalMs uint64

	// PeriodicNakIntervalMs is the periodic NAK timer interval in milliseconds
	// Default: 20. Lower = faster loss recovery but more NAK overhead.
	PeriodicNakIntervalMs uint64

	// PeriodicAckIntervalMs is the periodic ACK timer interval in milliseconds
	// Default: 10.
	PeriodicAckIntervalMs uint64

	// UseNakBtree enables the NAK btree for efficient gap detection
	// Auto-set to true when IoUringRecvEnabled=true
	UseNakBtree bool

	// SuppressImmediateNak prevents immediate NAK on gap detection
	// Auto-set to true when IoUringRecvEnabled=true (required to handle io_uring reordering)
	SuppressImmediateNak bool

	// NakRecentPercent is the percentage of TSBPD delay for "too recent" threshold
	// Packets within this threshold won't be added to NAK btree yet
	// Default: 0.10 (10% of TSBPD delay)
	NakRecentPercent float64

	// NakMergeGap is the maximum sequence gap to merge in NAK consolidation
	// Adjacent missing sequences within this gap are merged into ranges
	// Default: 3
	NakMergeGap uint32

	// NakConsolidationBudgetUs is the max time for NAK consolidation in microseconds
	// Default: 2000 (2ms)
	NakConsolidationBudgetUs uint64

	// FastNakEnabled enables the FastNAK optimization
	// When enabled, NAK is triggered immediately after silent period ends
	// Default: true when NAK btree is enabled
	FastNakEnabled bool

	// FastNakThresholdMs is the silent period to trigger FastNAK in milliseconds
	// Default: 50ms (typical Starlink outage is ~60ms)
	FastNakThresholdMs uint64

	// FastNakRecentEnabled adds recent gap immediately on FastNAK trigger
	// Detects sequence jump after outage and adds missing range to NAK btree
	// Default: true
	FastNakRecentEnabled bool

	// HonorNakOrder makes sender retransmit packets in NAK packet order
	// When enabled, oldest/most-urgent packets are retransmitted first
	// Default: false (existing behavior: newest-first)
	HonorNakOrder bool

	// --- RTO-based Suppression Configuration (Phase 6: RTO Suppression) ---

	// RTOMode controls how RTO is calculated for NAK/retransmit suppression.
	// Options:
	//   RTORttRttVar (0, default): RTT + RTTVar (balanced)
	//   RTORtt4RttVar (1): RTT + 4*RTTVar (RFC 6298 conservative)
	//   RTORttRttVarMargin (2): (RTT + RTTVar) * (1 + ExtraRTTMargin)
	// Default: RTORttRttVar
	RTOMode RTOMode

	// ExtraRTTMargin is the extra margin for RTORttRttVarMargin mode.
	// Specified as a multiplier (0.1 = 10% extra margin).
	// Only used when RTOMode = RTORttRttVarMargin.
	// Default: 0.10 (10%)
	ExtraRTTMargin float64

	// --- Testing Configuration ---

	// SendFilter is an optional function called before each packet is sent.
	// If set and returns false, the packet is dropped (not sent).
	// This is primarily for testing (e.g., simulating packet loss).
	// Must be set BEFORE Dial()/Accept() - cannot be modified after connection starts.
	// Default: nil (no filtering)
	SendFilter func(p packet.Packet) bool `json:"-"` // Not serializable

	// --- Lock-Free Ring Buffer (Phase 3: Lockless Design) ---

	// UsePacketRing enables lock-free ring buffer for packet handoff between
	// io_uring completion handlers and the receiver Tick() event loop.
	// When enabled, Push() writes to the ring (lock-free), and Tick() drains
	// the ring before processing (single-threaded, no locks needed).
	// Default: false (use legacy locked path)
	UsePacketRing bool

	// PacketRingSize is the capacity of the lock-free ring buffer (per shard).
	// Total ring capacity = PacketRingSize * PacketRingShards
	// Must be a power of 2. Default: 1024
	PacketRingSize int

	// PacketRingShards is the number of shards for the lock-free ring.
	// More shards reduce contention between concurrent producers.
	// Must be a power of 2. Default: 4
	PacketRingShards int

	// PacketRingMaxRetries is the maximum number of immediate retries
	// before starting backoff when the ring is full.
	// Default: 10
	PacketRingMaxRetries int

	// PacketRingBackoffDuration is the delay between backoff retries
	// when the ring is full.
	// Default: 100µs
	PacketRingBackoffDuration time.Duration

	// PacketRingMaxBackoffs is the maximum number of backoff iterations
	// before giving up and dropping the packet.
	// 0 = unlimited (keep retrying until success)
	// Default: 0
	PacketRingMaxBackoffs int

	// PacketRingRetryStrategy determines how ring writes handle full shards.
	// Options:
	//   "" or "sleep" - SleepBackoff: retry same shard, then sleep (default)
	//   "next"        - NextShard: try all shards before sleeping
	//   "random"      - RandomShard: try random shards (best load distribution)
	//   "adaptive"    - AdaptiveBackoff: exponential backoff with jitter
	//   "spin"        - SpinThenYield: yield CPU instead of sleep (lowest latency)
	//   "hybrid"      - Hybrid: NextShard + AdaptiveBackoff
	// Default: "" (uses SleepBackoff)
	PacketRingRetryStrategy string

	// --- Event Loop (Phase 4: Lockless Design) ---

	// UseEventLoop enables continuous event loop processing instead of
	// timer-driven Tick() for lower latency and smoother CPU utilization.
	// When enabled, packets are processed immediately as they arrive from
	// the ring buffer, and delivered as soon as TSBPD allows.
	// REQUIRES: UsePacketRing=true (event loop consumes from ring)
	// Default: false (use timer-driven Tick())
	UseEventLoop bool

	// EventLoopRateInterval is the interval for rate metric calculation
	// in the event loop. Uses a separate ticker from ACK/NAK.
	// Default: 1s
	EventLoopRateInterval time.Duration

	// BackoffColdStartPkts is the number of packets to receive before
	// the adaptive backoff engages. During cold start, minimum sleep
	// is used to ensure responsiveness during connection establishment.
	// Default: 1000
	BackoffColdStartPkts int

	// BackoffMinSleep is the minimum sleep duration during idle periods
	// in the event loop. Lower values = more responsive, higher CPU.
	// Default: 10µs
	BackoffMinSleep time.Duration

	// BackoffMaxSleep is the maximum sleep duration during idle periods
	// in the event loop. Higher values = lower CPU, less responsive.
	// Default: 1ms
	BackoffMaxSleep time.Duration

	// --- ACK Optimization Configuration (Phase 5: ACK Optimization) ---

	// LightACKDifference controls how often Light ACK packets are sent.
	// A Light ACK is sent when the contiguous sequence has advanced by
	// at least this many packets since the last Light ACK.
	// RFC recommends 64, but higher values reduce overhead at high bitrates.
	// Default: 64 (RFC recommendation)
	// Suggested for high bitrate (200Mb/s+): 256
	// Range: 1-5000
	LightACKDifference uint32

	// --- Debug Configuration ---

	// ReceiverDebug enables debug logging in the receiver for investigation.
	// When enabled, receiver logs NAK dispatch decisions, ring buffer operations,
	// and gap detection events. Uses the connection's logging function.
	// Default: false (no debug logging)
	ReceiverDebug bool
}

// DefaultConfig is the default configuration for a SRT connection
// if no individual configuration has been provided.
var defaultConfig Config = Config{
	Congestion:                "live",
	ConnectionTimeout:         3 * time.Second,
	DriftTracer:               true,
	EnforcedEncryption:        true,
	FC:                        25600,
	GroupConnect:              false,
	GroupStabilityTimeout:     0,
	InputBW:                   0,
	IPTOS:                     0,
	IPTTL:                     0,
	IPv6Only:                  -1,
	KMPreAnnounce:             1 << 12,
	KMRefreshRate:             1 << 24,
	Latency:                   -1,
	LossMaxTTL:                0,
	MaxBW:                     -1,
	MessageAPI:                false,
	MinVersion:                SRT_VERSION,
	MSS:                       MAX_MSS_SIZE,
	NAKReport:                 true,
	OverheadBW:                25,
	PacketFilter:              "",
	Passphrase:                "",
	PayloadSize:               MAX_PAYLOAD_SIZE,
	PBKeylen:                  16,
	PeerIdleTimeout:           2 * time.Second,
	KeepaliveThreshold:        0.75, // Send keepalive at 75% of PeerIdleTimeout
	PeerLatency:               120 * time.Millisecond,
	ReceiverBufferSize:        0,
	ReceiverLatency:           120 * time.Millisecond,
	SendBufferSize:            0,
	SendDropDelay:             1 * time.Second,
	StreamId:                  "",
	TooLatePacketDrop:         true,
	TransmissionType:          "live",
	TSBPDMode:                 true,
	AllowPeerIpChange:         false,
	IoUringEnabled:            false,
	IoUringSendRingSize:       64,
	NetworkQueueSize:          1024,
	WriteQueueSize:            1024,
	ReadQueueSize:             1024,
	ReceiveQueueSize:          2048,
	PacketReorderAlgorithm:    "list",
	BTreeDegree:               32,
	IoUringRecvEnabled:        false,
	IoUringRecvRingSize:       512,
	IoUringRecvInitialPending: 512,
	IoUringRecvBatchSize:      256,
	StatisticsPrintInterval:   0, // Disabled by default
	MetricsEnabled:            false,
	MetricsListenAddr:         "",                      // Disabled by default
	HandshakeTimeout:          1500 * time.Millisecond, // 1.5 seconds (must be < PeerIdleTimeout)
	ShutdownDelay:             5 * time.Second,         // 5 seconds

	// NAK btree defaults
	TickIntervalMs:           10,    // 10ms TSBPD tick
	PeriodicNakIntervalMs:    20,    // 20ms periodic NAK
	PeriodicAckIntervalMs:    10,    // 10ms periodic ACK
	UseNakBtree:              false, // Auto-set when IoUringRecvEnabled=true
	SuppressImmediateNak:     false, // Auto-set when IoUringRecvEnabled=true
	NakRecentPercent:         0.10,  // 10% of TSBPD delay
	NakMergeGap:              3,     // Merge gaps of 3 or less
	NakConsolidationBudgetUs: 2000,  // 2ms consolidation budget
	FastNakEnabled:           false, // Auto-set when UseNakBtree=true
	FastNakThresholdMs:       50,    // 50ms silent period triggers FastNAK
	FastNakRecentEnabled:     false, // Auto-set when FastNakEnabled=true
	HonorNakOrder:            false, // Existing behavior: newest-first

	// RTO-based suppression defaults (Phase 6)
	RTOMode:        RTORttRttVar, // RTT + RTTVar (balanced)
	ExtraRTTMargin: 0.10,         // 10% extra margin (only for RTORttRttVarMargin mode)

	// Lock-free ring buffer defaults (Phase 3)
	UsePacketRing:             false,                  // Legacy path by default
	PacketRingSize:            1024,                   // Per-shard capacity
	PacketRingShards:          4,                      // 4 shards = 4096 total capacity
	PacketRingMaxRetries:      10,                     // Immediate retries before backoff
	PacketRingBackoffDuration: 100 * time.Microsecond, // 100µs backoff delay
	PacketRingMaxBackoffs:     0,                      // 0 = unlimited backoffs
	PacketRingRetryStrategy:   "random",               // RandomShard - best RTT in benchmarks

	// Event loop defaults (Phase 4)
	UseEventLoop:          false,                 // Timer-driven Tick() by default
	EventLoopRateInterval: 1 * time.Second,       // Rate calculation every 1s
	BackoffColdStartPkts:  1000,                  // 1000 packets before backoff engages
	BackoffMinSleep:       10 * time.Microsecond, // 10µs minimum sleep
	BackoffMaxSleep:       1 * time.Millisecond,  // 1ms maximum sleep

	// ACK optimization defaults (Phase 5)
	LightACKDifference: 64, // RFC recommendation: send Light ACK every 64 packets
}

// DefaultConfig returns the default configuration for Dial and Listen.
func DefaultConfig() Config {
	return defaultConfig
}

// Note: ApplyAutoConfiguration() and Validate() are in config_validate.go
// Note: Marshal/Unmarshal functions are in config_marshal.go
