package main

import (
	"fmt"
	"strconv"
	"time"
)

// TestMode indicates whether a test runs on clean network or with impairment
type TestMode string

const (
	// TestModeClean runs on default namespace with loopback - no network impairment
	TestModeClean TestMode = "clean"

	// TestModeNetwork runs in isolated namespaces with network impairment
	TestModeNetwork TestMode = "network"

	// TestModeParallel runs two pipelines in parallel with network impairment
	TestModeParallel TestMode = "parallel"

	// TestModeIsolation runs simplified CG→Server tests to isolate variables
	TestModeIsolation TestMode = "isolation"
)

// ============================================================================
// CONFIG VARIANT - Feature configuration presets
// ============================================================================

// ConfigVariant represents a feature configuration preset for test matrix generation.
// These presets enable consistent naming and programmatic test generation.
type ConfigVariant string

const (
	// ConfigBase is the baseline: list packet store, no io_uring, no NAK btree
	ConfigBase ConfigVariant = "Base"

	// ConfigBtree enables btree packet store only (no io_uring, no NAK btree)
	ConfigBtree ConfigVariant = "Btree"

	// ConfigIoUr enables io_uring only (list packet store, no NAK btree)
	ConfigIoUr ConfigVariant = "IoUr"

	// ConfigNakBtree enables NAK btree only (no FastNAK, no FastNAKRecent)
	ConfigNakBtree ConfigVariant = "NakBtree"

	// ConfigNakBtreeF enables NAK btree + FastNAK (no FastNAKRecent)
	ConfigNakBtreeF ConfigVariant = "NakBtreeF"

	// ConfigNakBtreeFr enables NAK btree + FastNAK + FastNAKRecent
	ConfigNakBtreeFr ConfigVariant = "NakBtreeFr"

	// ConfigFull enables everything: btree + io_uring + NAK btree + FastNAK + FastNAKRecent + HonorNakOrder
	ConfigFull ConfigVariant = "Full"

	// ConfigRing enables lock-free ring buffer only (on top of Base config)
	ConfigRing ConfigVariant = "Ring"

	// ConfigFullRing enables everything including lock-free ring buffer
	// This is the complete Phase 3 lockless design configuration
	ConfigFullRing ConfigVariant = "FullRing"

	// ConfigEventLoop enables lock-free ring + event loop (on top of Base config)
	// This is ring buffer with continuous event loop processing
	ConfigEventLoop ConfigVariant = "EventLoop"

	// ConfigFullEventLoop enables everything including lock-free ring + event loop
	// This is the complete Phase 4 lockless design configuration
	ConfigFullEventLoop ConfigVariant = "FullEventLoop"

	// ========================================================================
	// LOCKLESS SENDER VARIANTS (Phase 5+: Lockless Sender Design)
	// ========================================================================

	// ConfigSendBtree enables sender btree only (O(log n) NAK lookup)
	ConfigSendBtree ConfigVariant = "SendBtree"

	// ConfigSendRing enables sender btree + lock-free data ring
	ConfigSendRing ConfigVariant = "SendRing"

	// ConfigSendEL enables sender btree + data ring + control ring + EventLoop
	// This is the complete lockless sender configuration
	ConfigSendEL ConfigVariant = "SendEL"

	// ConfigFullSendEL enables everything: receiver lockless + sender lockless
	// This is the ultimate high-performance configuration
	ConfigFullSendEL ConfigVariant = "FullSendEL"

	// ========================================================================
	// COMPLETELY LOCK-FREE RECEIVER VARIANTS (Phase 6: Receiver Control Ring)
	// ========================================================================

	// ConfigRecvControlRing enables receiver control ring only (on top of FullEventLoop)
	ConfigRecvControlRing ConfigVariant = "RecvControlRing"

	// ConfigFullELLockFree enables everything: completely lock-free receiver + sender
	// This is the ultimate lock-free configuration with both sender and receiver control rings
	ConfigFullELLockFree ConfigVariant = "FullELLockFree"
)

// GetSRTConfig returns an SRTConfig for a given ConfigVariant.
// This centralizes configuration presets for test matrix generation.
func GetSRTConfig(variant ConfigVariant) SRTConfig {
	switch variant {
	case ConfigBase:
		return BaselineSRTConfig
	case ConfigBtree:
		return ControlSRTConfig.WithBtree(32)
	case ConfigIoUr:
		return ControlSRTConfig.WithIoUringSend().WithIoUringRecv()
	case ConfigNakBtree:
		return ControlSRTConfig.WithIoUringRecv().WithNakBtreeOnly()
	case ConfigNakBtreeF:
		return ControlSRTConfig.WithIoUringRecv().WithNakBtreeOnly().WithFastNak()
	case ConfigNakBtreeFr:
		return ControlSRTConfig.WithIoUringRecv().WithNakBtreeOnly().WithFastNak().WithFastNakRecent()
	case ConfigFull:
		return HighPerfSRTConfig
	case ConfigRing:
		return ControlSRTConfig.WithPacketRing()
	case ConfigFullRing:
		return HighPerfSRTConfig.WithPacketRing()
	case ConfigEventLoop:
		return ControlSRTConfig.WithPacketRing().WithEventLoop()
	case ConfigFullEventLoop:
		return HighPerfSRTConfig.WithPacketRing().WithEventLoop()
	// Lockless Sender variants
	case ConfigSendBtree:
		return ControlSRTConfig.WithSendBtree()
	case ConfigSendRing:
		return ControlSRTConfig.WithSendBtree().WithSendRing()
	case ConfigSendEL:
		return ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoop()
	case ConfigFullSendEL:
		return HighPerfSRTConfig.WithPacketRing().WithEventLoop().WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoop()
	// Completely Lock-Free Receiver variants
	case ConfigRecvControlRing:
		return HighPerfSRTConfig.WithPacketRing().WithEventLoop().WithRecvControlRing()
	case ConfigFullELLockFree:
		return HighPerfSRTConfig.WithPacketRing().WithEventLoop().WithRecvControlRing().WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoop()
	default:
		return BaselineSRTConfig
	}
}

// GetSRTConfigWithLatency returns an SRTConfig for a given variant with custom latency.
func GetSRTConfigWithLatency(variant ConfigVariant, latency time.Duration) SRTConfig {
	return GetSRTConfig(variant).WithLatency(latency)
}

// ============================================================================
// RTT PROFILE - Network round-trip time presets
// ============================================================================

// RTTProfile represents a network round-trip time profile.
// These match the RTT links defined in packet_loss_injection_design.md.
type RTTProfile string

const (
	// RTT0 is 0ms RTT - baseline/local testing
	RTT0 RTTProfile = "R0"

	// RTT10 is 10ms RTT - regional datacenter
	RTT10 RTTProfile = "R10"

	// RTT60 is 60ms RTT - cross-continental
	RTT60 RTTProfile = "R60"

	// RTT130 is 130ms RTT - intercontinental
	RTT130 RTTProfile = "R130"

	// RTT300 is 300ms RTT - GEO satellite
	RTT300 RTTProfile = "R300"
)

// GetRTTMs returns the RTT in milliseconds for a profile.
func GetRTTMs(profile RTTProfile) int {
	switch profile {
	case RTT0:
		return 0
	case RTT10:
		return 10
	case RTT60:
		return 60
	case RTT130:
		return 130
	case RTT300:
		return 300
	default:
		return 60 // Default to cross-continental
	}
}

// GetLatencyProfile returns the network latency profile string for netem configuration.
// This maps RTTProfile to the latency profile names used in network setup scripts.
func GetLatencyProfile(profile RTTProfile) string {
	switch profile {
	case RTT0:
		return "none"
	case RTT10:
		return "regional"
	case RTT60:
		return "continental"
	case RTT130:
		return "intercontinental"
	case RTT300:
		return "geo_satellite"
	default:
		return "continental"
	}
}

// ============================================================================
// TIMER PROFILE - Timer interval presets
// ============================================================================

// TimerProfile represents a timer interval preset for testing different
// tick/NAK/ACK frequencies.
type TimerProfile string

const (
	// TimerDefault uses default intervals: 10ms tick, 20ms NAK, 10ms ACK
	TimerDefault TimerProfile = "T-Default"

	// TimerFast uses aggressive intervals: 5ms tick, 10ms NAK, 5ms ACK
	TimerFast TimerProfile = "T-Fast"

	// TimerSlow uses conservative intervals: 20ms tick, 40ms NAK, 20ms ACK
	TimerSlow TimerProfile = "T-Slow"

	// TimerFastNak uses fast NAK only: 10ms tick, 5ms NAK, 10ms ACK
	TimerFastNak TimerProfile = "T-FastNak"

	// TimerSlowNak uses slow NAK (stress test): 10ms tick, 50ms NAK, 10ms ACK
	TimerSlowNak TimerProfile = "T-SlowNak"
)

// TimerIntervals holds timer interval values in milliseconds.
type TimerIntervals struct {
	TickMs uint64 // TSBPD delivery tick interval
	NakMs  uint64 // Periodic NAK timer interval
	AckMs  uint64 // Periodic ACK timer interval
}

// GetTimerIntervals returns the timer intervals for a profile.
func GetTimerIntervals(profile TimerProfile) TimerIntervals {
	switch profile {
	case TimerFast:
		return TimerIntervals{TickMs: 5, NakMs: 10, AckMs: 5}
	case TimerSlow:
		return TimerIntervals{TickMs: 20, NakMs: 40, AckMs: 20}
	case TimerFastNak:
		return TimerIntervals{TickMs: 10, NakMs: 5, AckMs: 10}
	case TimerSlowNak:
		return TimerIntervals{TickMs: 10, NakMs: 50, AckMs: 10}
	case TimerDefault:
		fallthrough
	default:
		return TimerIntervals{TickMs: 10, NakMs: 20, AckMs: 10}
	}
}

// NetworkImpairment defines network impairment parameters for network mode tests
type NetworkImpairment struct {
	// Loss configuration
	LossRate    float64 // Packet loss rate (0.0-1.0, e.g., 0.02 = 2%)
	BurstLength int     // Burst loss length (0 = random loss, >0 = burst model)

	// Latency configuration
	LatencyMs       int // Base RTT latency in milliseconds (netem delay = RTT/2)
	LatencyJitterMs int // Latency jitter in milliseconds

	// Pattern-based impairment (overrides above if set)
	Pattern string // "clean", "moderate", "heavy", "starlink", "geo-satellite"

	// Latency profile (predefined latency settings)
	LatencyProfile string // "local", "regional", "continental", "geo-satellite", "tier3-high"

	// Validation thresholds (nil = use defaults based on impairment type)
	Thresholds *StatisticalThresholds
}

// StatisticalThresholds defines configurable tolerances for statistical validation.
// All values are optional - zero values use defaults.
type StatisticalThresholds struct {
	// Loss rate tolerance as a fraction (0.5 = plus/minus 50% of expected loss rate)
	// Default: 0.5 for probabilistic loss, 1.0 for pattern-based
	LossRateTolerance float64

	// Minimum retransmission rate (fraction of lost packets that trigger retrans)
	// Default: 0.8 (80%)
	MinRetransRate float64

	// Maximum retransmission rate (no more than Nx retransmissions per lost packet)
	// Default: 3.0
	MaxRetransRate float64

	// Minimum NAKs per lost packet (lower bound for NAK activity)
	// Default: 0.5
	MinNAKsPerLostPkt float64

	// Maximum NAKs per lost packet (upper bound to detect NAK storms)
	// Default: 5.0
	MaxNAKsPerLostPkt float64

	// Minimum recovery rate (fraction of packets successfully received)
	// Default: 0.95 (95%)
	MinRecoveryRate float64
}

// DefaultThresholds returns the default thresholds for probabilistic loss
func DefaultThresholds() StatisticalThresholds {
	return StatisticalThresholds{
		LossRateTolerance: 0.5,  // plus/minus 50% tolerance
		MinRetransRate:    0.8,  // 80% of lost packets retransmitted
		MaxRetransRate:    3.0,  // No more than 3x retransmissions per loss
		MinNAKsPerLostPkt: 0.5,  // At least 0.5 NAKs per lost packet
		MaxNAKsPerLostPkt: 5.0,  // No more than 5 NAKs per lost packet
		MinRecoveryRate:   0.95, // 95% recovery rate
	}
}

// HighLatencyThresholds returns relaxed thresholds for high-latency scenarios
func HighLatencyThresholds() StatisticalThresholds {
	return StatisticalThresholds{
		LossRateTolerance: 0.6,  // plus/minus 60% tolerance
		MinRetransRate:    0.7,  // Lower retrans expectation
		MaxRetransRate:    4.0,  // Allow more retransmissions
		MinNAKsPerLostPkt: 0.3,  // Lower NAK expectation
		MaxNAKsPerLostPkt: 8.0,  // Higher NAK tolerance
		MinRecoveryRate:   0.90, // 90% recovery rate
	}
}

// BurstLossThresholds returns relaxed thresholds for burst loss patterns
func BurstLossThresholds() StatisticalThresholds {
	return StatisticalThresholds{
		LossRateTolerance: 1.0,  // plus/minus 100% tolerance (bursts are unpredictable)
		MinRetransRate:    0.5,  // Lower retrans expectation
		MaxRetransRate:    5.0,  // Allow more retransmissions
		MinNAKsPerLostPkt: 0.3,  // Lower NAK expectation
		MaxNAKsPerLostPkt: 10.0, // Higher NAK tolerance
		MinRecoveryRate:   0.85, // 85% recovery rate
	}
}

// StressTestThresholds returns very relaxed thresholds for stress testing
func StressTestThresholds() StatisticalThresholds {
	return StatisticalThresholds{
		LossRateTolerance: 0.8,  // plus/minus 80% tolerance
		MinRetransRate:    0.5,  // Lower retrans expectation
		MaxRetransRate:    5.0,  // Allow many retransmissions
		MinNAKsPerLostPkt: 0.2,  // Lower NAK expectation
		MaxNAKsPerLostPkt: 15.0, // Much higher NAK tolerance
		MinRecoveryRate:   0.80, // 80% recovery rate
	}
}

// MetricsEndpoint represents a metrics endpoint configuration
type MetricsEndpoint struct {
	HTTPAddr string // TCP HTTP address (e.g., "127.0.0.10:5101") - empty if not used
	UDSPath  string // Unix socket path (e.g., "/tmp/srt_metrics_server.sock") - empty if not used
}

// IsConfigured returns true if at least one endpoint is configured
func (e *MetricsEndpoint) IsConfigured() bool {
	return e.HTTPAddr != "" || e.UDSPath != ""
}

// String returns a string representation of the endpoint for logging
func (e *MetricsEndpoint) String() string {
	if e.UDSPath != "" {
		return "unix:" + e.UDSPath
	}
	if e.HTTPAddr != "" {
		return "http://" + e.HTTPAddr
	}
	return "(none)"
}

// SRTConfig represents SRT connection configuration parameters
// This mirrors the srt.Config struct and can be converted to CLI flags
type SRTConfig struct {
	// Connection timeouts
	ConnectionTimeout time.Duration // -conntimeo (milliseconds)
	PeerIdleTimeout   time.Duration // -peeridletimeo (milliseconds)
	HandshakeTimeout  time.Duration // -handshaketimeout
	ShutdownDelay     time.Duration // -shutdowndelay

	// Latency settings
	Latency     time.Duration // -latency (milliseconds)
	RecvLatency time.Duration // -rcvlatency (milliseconds)
	PeerLatency time.Duration // -peerlatency (milliseconds)

	// Buffer sizes
	FC      uint32 // -fc (flow control window, packets)
	RecvBuf uint32 // -rcvbuf (receive buffer, bytes)
	SendBuf uint32 // -sndbuf (send buffer, bytes)

	// Packet handling
	TLPktDrop              bool   // -tlpktdrop (too-late packet drop)
	PacketReorderAlgorithm string // -packetreorderalgorithm (list, btree)
	BTreeDegree            int    // -btreedegree (b-tree degree)

	// io_uring settings
	IoUringEnabled       bool // -iouringenabled
	IoUringRecvEnabled   bool // -iouringrecvenabled
	IoUringSendRingSize  int  // -iouringsendringsize
	IoUringRecvRingSize  int  // -iouringrecvringsize
	IoUringRecvBatchSize int  // -iouringrecvbatchsize
	IoUringRecvRingCount int  // -iouringrecvringcount (Phase 1: multi_iouring_design.md)
	IoUringSendRingCount int  // -iouringsendringcount (Phase 1: multi_iouring_design.md)

	// Congestion control
	Congestion string // -congestion (live, file)
	MaxBW      int64  // -maxbw (bytes/s, -1 for unlimited)
	InputBW    int64  // -inputbw (bytes/s)

	// Encryption
	Passphrase string // -passphrase
	PBKeyLen   int    // -pbkeylen (16, 24, 32)

	// Message mode
	MessageAPI bool // -messageapi

	// NAK reports
	NAKReport bool // -nakreport

	// NAK btree configuration (for io_uring reorder handling)
	UseNakBtree          bool    // -usenakbtree
	SuppressImmediateNak bool    // (auto-set internally, not a CLI flag)
	FastNakEnabled       bool    // -fastnakenabled
	FastNakRecentEnabled bool    // -fastnakrecentenabled
	HonorNakOrder        bool    // -honornakorder
	NakRecentPercent     float64 // -nakrecentpercent (default: 0.10, 10% of TSBPD delay)

	// Timer interval configuration
	TickIntervalMs        uint64 // -tickintervalms (TSBPD delivery tick, default: 10)
	PeriodicNakIntervalMs uint64 // -periodicnakintervalms (periodic NAK timer, default: 20)
	PeriodicAckIntervalMs uint64 // -periodicackintervalms (periodic ACK timer, default: 10)

	// Lock-free ring buffer configuration (Phase 3: Lockless Design)
	UsePacketRing             bool          // -usepacketring
	PacketRingSize            int           // -packetringsize (default: 1024)
	PacketRingShards          int           // -packetringshards (default: 4)
	PacketRingMaxRetries      int           // -packetringmaxretries (default: 10)
	PacketRingBackoffDuration time.Duration // -packetringbackoffduration (default: 100µs)
	PacketRingMaxBackoffs     int           // -packetringmaxbackoffs (default: 0)
	PacketRingRetryStrategy   string        // -packetringretrystrategy (sleep, next, random, adaptive, spin, hybrid)

	// Event loop configuration (Phase 4: Lockless Design)
	UseEventLoop          bool          // -useeventloop (requires -usepacketring)
	EventLoopRateInterval time.Duration // -eventlooprateinterval (default: 1s)
	BackoffColdStartPkts  int           // -backoffcoldstartpkts (default: 1000)
	BackoffMinSleep       time.Duration // -backoffminsleep (default: 10µs)
	BackoffMaxSleep       time.Duration // -backoffmaxsleep (default: 1ms)

	// ========================================================================
	// LOCKLESS SENDER CONFIGURATION (Phase 5+: Lockless Sender Design)
	// ========================================================================
	// See lockless_sender_design.md and lockless_sender_implementation_plan.md

	// Sender btree configuration (Phase 1: SendPacketBtree)
	UseSendBtree    bool // -usesendbtree (enable btree for sender packet storage)
	SendBtreeDegree int  // -sendbtreesize (default: 32)

	// Sender data ring (Phase 2: Lock-free Push())
	UseSendRing    bool // -usesendring (requires -usesendbtree)
	SendRingSize   int  // -sendringsize (default: 1024)
	SendRingShards int  // -sendringshards (default: 1 for ordering)

	// Sender control ring (Phase 3: Lock-free ACK/NAK routing)
	UseSendControlRing    bool // -usesendcontrolring (requires -usesendring)
	SendControlRingSize   int  // -sendcontrolringsize (default: 256)
	SendControlRingShards int  // -sendcontrolringshards (default: 2)

	// Sender EventLoop (Phase 4: Continuous event loop)
	UseSendEventLoop             bool          // -usesendeventloop (requires -usesendcontrolring)
	SendEventLoopBackoffMinSleep time.Duration // -sendeventloopbackoffminsleep (default: 100µs)
	SendEventLoopBackoffMaxSleep time.Duration // -sendeventloopbackoffmaxsleep (default: 1ms)
	SendTsbpdSleepFactor         float64       // -sendtsbpdsleepfactor (default: 0.9)
	SendDropThresholdUs          uint64        // -senddropthresholdus (default: 1000000)

	// EventLoop tight loop configuration (eventloop_batch_sizing_design.md)
	// -1 = library default (512), 0 = unbounded legacy mode, >0 = custom batch size
	EventLoopMaxDataPerIteration int // -eventloopmaxdata

	// ========================================================================
	// COMPLETELY LOCK-FREE RECEIVER (Phase 6: Receiver Control Ring)
	// ========================================================================
	// See completely_lockfree_receiver.md and completely_lockfree_receiver_implementation_plan.md

	// Receiver control ring (routes ACKACK/KEEPALIVE to EventLoop for lock-free processing)
	UseRecvControlRing    bool // -recvcontrolring (requires -useeventloop)
	RecvControlRingSize   int  // -recvcontrolringsize (default: 128)
	RecvControlRingShards int  // -recvcontrolringshards (default: 1)

	// Sender payload validation (Phase 5: Zero-copy)
	ValidateSendPayloadSize bool // -validatesendpayloadsize (validate payload size)

	// Debug configuration
	ReceiverDebug bool   // -receiverdebug (enable receiver debug logging)
	LogTopics     string // -logtopics (comma-separated log topics, e.g., "receiver" or "receiver:nak")
}

// ToCliFlags converts SRTConfig to CLI flag arguments
func (c *SRTConfig) ToCliFlags() []string {
	var flags []string

	// Connection timeouts
	if c.ConnectionTimeout > 0 {
		flags = append(flags, "-conntimeo", strconv.FormatInt(c.ConnectionTimeout.Milliseconds(), 10))
	}
	if c.PeerIdleTimeout > 0 {
		flags = append(flags, "-peeridletimeo", strconv.FormatInt(c.PeerIdleTimeout.Milliseconds(), 10))
	}
	if c.HandshakeTimeout > 0 {
		flags = append(flags, "-handshaketimeout", c.HandshakeTimeout.String())
	}
	if c.ShutdownDelay > 0 {
		flags = append(flags, "-shutdowndelay", c.ShutdownDelay.String())
	}

	// Latency settings
	if c.Latency > 0 {
		flags = append(flags, "-latency", strconv.FormatInt(c.Latency.Milliseconds(), 10))
	}
	if c.RecvLatency > 0 {
		flags = append(flags, "-rcvlatency", strconv.FormatInt(c.RecvLatency.Milliseconds(), 10))
	}
	if c.PeerLatency > 0 {
		flags = append(flags, "-peerlatency", strconv.FormatInt(c.PeerLatency.Milliseconds(), 10))
	}

	// Buffer sizes
	if c.FC > 0 {
		flags = append(flags, "-fc", strconv.FormatUint(uint64(c.FC), 10))
	}
	if c.RecvBuf > 0 {
		flags = append(flags, "-rcvbuf", strconv.FormatUint(uint64(c.RecvBuf), 10))
	}
	if c.SendBuf > 0 {
		flags = append(flags, "-sndbuf", strconv.FormatUint(uint64(c.SendBuf), 10))
	}

	// Packet handling
	if c.TLPktDrop {
		flags = append(flags, "-tlpktdrop")
	}
	if c.PacketReorderAlgorithm != "" {
		flags = append(flags, "-packetreorderalgorithm", c.PacketReorderAlgorithm)
	}
	if c.BTreeDegree > 0 {
		flags = append(flags, "-btreedegree", strconv.Itoa(c.BTreeDegree))
	}

	// io_uring settings
	if c.IoUringEnabled {
		flags = append(flags, "-iouringenabled")
	}
	if c.IoUringRecvEnabled {
		flags = append(flags, "-iouringrecvenabled")
	}
	if c.IoUringSendRingSize > 0 {
		flags = append(flags, "-iouringsendringsize", strconv.Itoa(c.IoUringSendRingSize))
	}
	if c.IoUringRecvRingSize > 0 {
		flags = append(flags, "-iouringrecvringsize", strconv.Itoa(c.IoUringRecvRingSize))
	}
	if c.IoUringRecvBatchSize > 0 {
		flags = append(flags, "-iouringrecvbatchsize", strconv.Itoa(c.IoUringRecvBatchSize))
	}
	if c.IoUringRecvRingCount > 1 {
		flags = append(flags, "-iouringrecvringcount", strconv.Itoa(c.IoUringRecvRingCount))
	}
	if c.IoUringSendRingCount > 1 {
		flags = append(flags, "-iouringsendringcount", strconv.Itoa(c.IoUringSendRingCount))
	}

	// Congestion control
	if c.Congestion != "" {
		flags = append(flags, "-congestion", c.Congestion)
	}
	if c.MaxBW != 0 {
		flags = append(flags, "-maxbw", strconv.FormatInt(c.MaxBW, 10))
	}
	if c.InputBW > 0 {
		flags = append(flags, "-inputbw", strconv.FormatInt(c.InputBW, 10))
	}

	// Encryption
	if c.Passphrase != "" {
		flags = append(flags, "-passphrase", c.Passphrase)
	}
	if c.PBKeyLen > 0 {
		flags = append(flags, "-pbkeylen", strconv.Itoa(c.PBKeyLen))
	}

	// Message mode
	if c.MessageAPI {
		flags = append(flags, "-messageapi")
	}

	// NAK reports
	if c.NAKReport {
		flags = append(flags, "-nakreport")
	}

	// NAK btree configuration
	// Note: SuppressImmediateNak is auto-set internally when UseNakBtree=true,
	// so we don't need to pass it as a CLI flag
	if c.UseNakBtree {
		flags = append(flags, "-usenakbtree")
	}
	if c.FastNakEnabled {
		flags = append(flags, "-fastnakenabled")
	}
	if c.FastNakRecentEnabled {
		flags = append(flags, "-fastnakrecentenabled")
	}
	if c.HonorNakOrder {
		flags = append(flags, "-honornakorder")
	}
	if c.NakRecentPercent > 0 {
		flags = append(flags, "-nakrecentpercent", strconv.FormatFloat(c.NakRecentPercent, 'f', 2, 64))
	}

	// Timer interval flags
	if c.TickIntervalMs > 0 {
		flags = append(flags, "-tickintervalms", strconv.FormatUint(c.TickIntervalMs, 10))
	}
	if c.PeriodicNakIntervalMs > 0 {
		flags = append(flags, "-periodicnakintervalms", strconv.FormatUint(c.PeriodicNakIntervalMs, 10))
	}
	if c.PeriodicAckIntervalMs > 0 {
		flags = append(flags, "-periodicackintervalms", strconv.FormatUint(c.PeriodicAckIntervalMs, 10))
	}

	// Lock-free ring buffer configuration (Phase 3: Lockless Design)
	if c.UsePacketRing {
		flags = append(flags, "-usepacketring")
	}
	if c.PacketRingSize > 0 {
		flags = append(flags, "-packetringsize", strconv.Itoa(c.PacketRingSize))
	}
	if c.PacketRingShards > 0 {
		flags = append(flags, "-packetringshards", strconv.Itoa(c.PacketRingShards))
	}
	if c.PacketRingMaxRetries > 0 {
		flags = append(flags, "-packetringmaxretries", strconv.Itoa(c.PacketRingMaxRetries))
	}
	if c.PacketRingBackoffDuration > 0 {
		flags = append(flags, "-packetringbackoffduration", c.PacketRingBackoffDuration.String())
	}
	if c.PacketRingMaxBackoffs > 0 {
		flags = append(flags, "-packetringmaxbackoffs", strconv.Itoa(c.PacketRingMaxBackoffs))
	}
	if c.PacketRingRetryStrategy != "" {
		flags = append(flags, "-packetringretrystrategy", c.PacketRingRetryStrategy)
	}

	// Event loop configuration (Phase 4: Lockless Design)
	if c.UseEventLoop {
		flags = append(flags, "-useeventloop")
	}
	if c.EventLoopRateInterval > 0 {
		flags = append(flags, "-eventlooprateinterval", c.EventLoopRateInterval.String())
	}
	if c.BackoffColdStartPkts > 0 {
		flags = append(flags, "-backoffcoldstartpkts", strconv.Itoa(c.BackoffColdStartPkts))
	}
	if c.BackoffMinSleep > 0 {
		flags = append(flags, "-backoffminsleep", c.BackoffMinSleep.String())
	}
	if c.BackoffMaxSleep > 0 {
		flags = append(flags, "-backoffmaxsleep", c.BackoffMaxSleep.String())
	}

	// Lockless Sender configuration (Phase 5+)
	if c.UseSendBtree {
		flags = append(flags, "-usesendbtree")
	}
	if c.SendBtreeDegree > 0 {
		flags = append(flags, "-sendbtreesize", strconv.Itoa(c.SendBtreeDegree))
	}
	if c.UseSendRing {
		flags = append(flags, "-usesendring")
	}
	if c.SendRingSize > 0 {
		flags = append(flags, "-sendringsize", strconv.Itoa(c.SendRingSize))
	}
	if c.SendRingShards > 0 {
		flags = append(flags, "-sendringshards", strconv.Itoa(c.SendRingShards))
	}
	if c.UseSendControlRing {
		flags = append(flags, "-usesendcontrolring")
	}
	if c.SendControlRingSize > 0 {
		flags = append(flags, "-sendcontrolringsize", strconv.Itoa(c.SendControlRingSize))
	}
	if c.SendControlRingShards > 0 {
		flags = append(flags, "-sendcontrolringshards", strconv.Itoa(c.SendControlRingShards))
	}
	if c.UseSendEventLoop {
		flags = append(flags, "-usesendeventloop")
	}
	if c.SendEventLoopBackoffMinSleep > 0 {
		flags = append(flags, "-sendeventloopbackoffminsleep", c.SendEventLoopBackoffMinSleep.String())
	}
	if c.SendEventLoopBackoffMaxSleep > 0 {
		flags = append(flags, "-sendeventloopbackoffmaxsleep", c.SendEventLoopBackoffMaxSleep.String())
	}
	if c.SendTsbpdSleepFactor > 0 {
		flags = append(flags, "-sendtsbpdsleepfactor", strconv.FormatFloat(c.SendTsbpdSleepFactor, 'f', 2, 64))
	}
	if c.SendDropThresholdUs > 0 {
		flags = append(flags, "-senddropthresholdus", strconv.FormatUint(c.SendDropThresholdUs, 10))
	}
	// EventLoop tight loop configuration
	// Only emit if explicitly set (non-zero means custom value)
	// -1 = library default, 0 = legacy unbounded, >0 = custom batch size
	if c.EventLoopMaxDataPerIteration != 0 {
		flags = append(flags, "-eventloopmaxdata", strconv.Itoa(c.EventLoopMaxDataPerIteration))
	}

	// Receiver control ring (Completely Lock-Free Receiver)
	if c.UseRecvControlRing {
		flags = append(flags, "-userecvcontrolring")
	}
	if c.RecvControlRingSize > 0 {
		flags = append(flags, "-recvcontrolringsize", strconv.Itoa(c.RecvControlRingSize))
	}
	if c.RecvControlRingShards > 0 {
		flags = append(flags, "-recvcontrolringshards", strconv.Itoa(c.RecvControlRingShards))
	}

	if c.ValidateSendPayloadSize {
		flags = append(flags, "-validatesendpayloadsize")
	}

	// Debug configuration
	if c.ReceiverDebug {
		flags = append(flags, "-receiverdebug")
	}
	if c.LogTopics != "" {
		flags = append(flags, "-logtopics", c.LogTopics)
	}

	return flags
}

// NetworkConfig represents network address configuration for a component
type NetworkConfig struct {
	IP          string // IP address for the component (e.g., "127.0.0.10")
	SRTPort     int    // SRT port (server only, clients use ephemeral)
	MetricsPort int    // Metrics TCP HTTP port (e.g., 5101, 5102, 5103) - 0 = disabled
	MetricsUDS  string // Metrics Unix socket path (e.g., "/tmp/srt_metrics_server.sock") - empty = disabled
}

// SRTAddr returns the SRT address string (e.g., "127.0.0.10:6000")
func (n *NetworkConfig) SRTAddr() string {
	if n.SRTPort > 0 {
		return fmt.Sprintf("%s:%d", n.IP, n.SRTPort)
	}
	return n.IP
}

// MetricsAddr returns the metrics address string (e.g., "127.0.0.10:9090")
func (n *NetworkConfig) MetricsAddr() string {
	if n.MetricsPort > 0 {
		return fmt.Sprintf("%s:%d", n.IP, n.MetricsPort)
	}
	return ""
}

// MetricsURL returns the full metrics URL (e.g., "http://127.0.0.10:9090/metrics")
func (n *NetworkConfig) MetricsURL() string {
	if n.MetricsPort > 0 {
		return fmt.Sprintf("http://%s:%d/metrics", n.IP, n.MetricsPort)
	}
	return ""
}

// ComponentConfig represents configuration specific to one component
type ComponentConfig struct {
	SRT        SRTConfig // SRT configuration (converted to CLI flags)
	ExtraFlags []string  // Additional CLI flags not covered by SRTConfig

	// Client-specific output options
	// These only apply to the client component (not server or client-generator)
	IoUringOutput bool // -iouringoutput (use io_uring for output writes, Linux only)
}

// ToCliFlags converts ComponentConfig to CLI flag arguments
func (c *ComponentConfig) ToCliFlags() []string {
	flags := c.SRT.ToCliFlags()
	flags = append(flags, c.ExtraFlags...)
	return flags
}

// TestConfig represents a complete test configuration
type TestConfig struct {
	// Test identification
	Name        string // Primary test name using standardized format
	LegacyName  string // Old test name for backward compatibility (optional)
	Description string // Detailed description of what this test validates

	// Test mode (clean network vs network impairment)
	Mode       TestMode          // TestModeClean (default) or TestModeNetwork
	Impairment NetworkImpairment // Network impairment settings (only used if Mode == TestModeNetwork)

	// Network configuration (IP addresses and ports for each component)
	ServerNetwork          NetworkConfig // Server network config (default: 127.0.0.10:6000)
	ClientGeneratorNetwork NetworkConfig // Client-generator network config (default: 127.0.0.20)
	ClientNetwork          NetworkConfig // Client network config (default: 127.0.0.30)

	// Test parameters
	Bitrate        int64         // Bitrate in bits per second for client-generator
	TestDuration   time.Duration // How long to run before shutdown
	ConnectionWait time.Duration // Time to wait for connections to establish

	// Component-specific configurations
	Server          ComponentConfig // Server configuration
	ClientGenerator ComponentConfig // Client-generator configuration
	Client          ComponentConfig // Client configuration

	// Shared SRT configuration (applied to all components)
	// If set, this is merged with component-specific configs (component takes precedence)
	SharedSRT *SRTConfig

	// Metrics collection settings
	MetricsEnabled  bool          // Whether to collect metrics during the test
	CollectInterval time.Duration // How often to collect metrics
	VerboseMetrics  bool          // Print detailed per-connection metrics deltas during test
	VerboseNetwork  bool          // Print detailed network controller logs (pattern events, route tables)

	// Expected results (for validation)
	ExpectedErrors     []string // List of expected error counters (e.g., "gosrt_pkt_drop_total")
	MaxExpectedDrops   int64    // Maximum expected packet drops (0 = none expected)
	MaxExpectedRetrans int64    // Maximum expected retransmissions
}

// GetEffectiveNetworkConfig returns the network config for each component,
// using defaults if not specified in the test config
func (c *TestConfig) GetEffectiveNetworkConfig() (server, clientGen, client NetworkConfig) {
	// Use test-specific config or fall back to defaults
	server = c.ServerNetwork
	if server.IP == "" {
		server = DefaultServerNetwork
	}

	clientGen = c.ClientGeneratorNetwork
	if clientGen.IP == "" {
		clientGen = DefaultClientGeneratorNetwork
	}

	client = c.ClientNetwork
	if client.IP == "" {
		client = DefaultClientNetwork
	}

	return server, clientGen, client
}

// GetServerFlags returns CLI flags for the server component
func (c *TestConfig) GetServerFlags() []string {
	serverNet, _, _ := c.GetEffectiveNetworkConfig()

	flags := []string{"-addr", serverNet.SRTAddr()}

	// Add metrics TCP endpoint if port is configured
	if serverNet.MetricsPort > 0 {
		flags = append(flags, "-promhttp", serverNet.MetricsAddr())
	}

	// Add metrics UDS endpoint if path is configured
	if serverNet.MetricsUDS != "" {
		flags = append(flags, "-promuds", serverNet.MetricsUDS)
	}

	// Apply shared config first (if any)
	if c.SharedSRT != nil {
		flags = append(flags, c.SharedSRT.ToCliFlags()...)
	}

	// Apply component-specific config (overrides shared)
	flags = append(flags, c.Server.ToCliFlags()...)

	return flags
}

// GetClientGeneratorFlags returns CLI flags for the client-generator component
func (c *TestConfig) GetClientGeneratorFlags() []string {
	serverNet, clientGenNet, _ := c.GetEffectiveNetworkConfig()

	// Build the publisher URL using the server's SRT address
	publisherURL := fmt.Sprintf("srt://%s/test-stream", serverNet.SRTAddr())

	flags := []string{
		"-to", publisherURL,
		"-bitrate", strconv.FormatInt(c.Bitrate, 10),
	}

	// Add local address binding (to use specific source IP)
	if clientGenNet.IP != "" {
		flags = append(flags, "-localaddr", clientGenNet.IP)
	}

	// Add metrics TCP endpoint if port is configured
	if clientGenNet.MetricsPort > 0 {
		flags = append(flags, "-promhttp", clientGenNet.MetricsAddr())
	}

	// Add metrics UDS endpoint if path is configured
	if clientGenNet.MetricsUDS != "" {
		flags = append(flags, "-promuds", clientGenNet.MetricsUDS)
	}

	// Apply shared config first (if any)
	if c.SharedSRT != nil {
		flags = append(flags, c.SharedSRT.ToCliFlags()...)
	}

	// Apply component-specific config (overrides shared)
	flags = append(flags, c.ClientGenerator.ToCliFlags()...)

	return flags
}

// GetClientFlags returns CLI flags for the client component
func (c *TestConfig) GetClientFlags() []string {
	serverNet, _, clientNet := c.GetEffectiveNetworkConfig()

	// Build the subscriber URL using the server's SRT address
	subscriberURL := fmt.Sprintf("srt://%s?streamid=subscribe:/test-stream&mode=caller", serverNet.SRTAddr())

	flags := []string{
		"-from", subscriberURL,
		"-to", "null",
	}

	// Add local address binding (to use specific source IP)
	if clientNet.IP != "" {
		flags = append(flags, "-localaddr", clientNet.IP)
	}

	// Add metrics TCP endpoint if port is configured
	if clientNet.MetricsPort > 0 {
		flags = append(flags, "-promhttp", clientNet.MetricsAddr())
	}

	// Add metrics UDS endpoint if path is configured
	if clientNet.MetricsUDS != "" {
		flags = append(flags, "-promuds", clientNet.MetricsUDS)
	}

	// Add io_uring output flag if enabled (client-specific, Linux only)
	if c.Client.IoUringOutput {
		flags = append(flags, "-iouringoutput")
	}

	// Apply shared config first (if any)
	if c.SharedSRT != nil {
		flags = append(flags, c.SharedSRT.ToCliFlags()...)
	}

	// Apply component-specific config (overrides shared)
	flags = append(flags, c.Client.ToCliFlags()...)

	return flags
}

// GetAllMetricsEndpoints returns the metrics endpoints for all components
func (c *TestConfig) GetAllMetricsEndpoints() (server, clientGen, client MetricsEndpoint) {
	serverNet, clientGenNet, clientNet := c.GetEffectiveNetworkConfig()
	return MetricsEndpoint{HTTPAddr: serverNet.MetricsAddr(), UDSPath: serverNet.MetricsUDS},
		MetricsEndpoint{HTTPAddr: clientGenNet.MetricsAddr(), UDSPath: clientGenNet.MetricsUDS},
		MetricsEndpoint{HTTPAddr: clientNet.MetricsAddr(), UDSPath: clientNet.MetricsUDS}
}

// ============================================================================
// PARALLEL COMPARISON TEST CONFIGURATION
// ============================================================================
// These types support running two pipelines (Baseline + HighPerf) in parallel
// for direct, side-by-side comparison under identical network conditions.

// PipelineConfig defines the configuration for one pipeline in a parallel test
type PipelineConfig struct {
	// Network addresses (uses .2 for Baseline, .3 for HighPerf)
	PublisherIP  string // e.g., "10.1.1.2" or "10.1.1.3"
	ServerIP     string // e.g., "10.2.1.2" or "10.2.1.3"
	SubscriberIP string // e.g., "10.1.2.2" or "10.1.2.3"
	ServerPort   int    // e.g., 6000 or 6001
	StreamID     string // e.g., "test-stream-baseline" or "test-stream-highperf"

	// SRT configuration for this pipeline
	SRT SRTConfig

	// Client-specific configuration
	ClientConfig ComponentConfig
}

// GetServerAddr returns the server address string
func (p *PipelineConfig) GetServerAddr() string {
	return fmt.Sprintf("%s:%d", p.ServerIP, p.ServerPort)
}

// ParallelTestConfig defines a parallel comparison test with two pipelines
type ParallelTestConfig struct {
	// Test identification
	Name        string // Primary test name using standardized format
	LegacyName  string // Old test name for backward compatibility (optional)
	Description string

	// Network impairment (shared by both pipelines)
	Impairment NetworkImpairment

	// Pipeline configurations
	Baseline PipelineConfig // Baseline pipeline (list + no io_uring)
	HighPerf PipelineConfig // High performance pipeline (btree + io_uring)

	// Test timing
	Bitrate         int64         // Bitrate in bits per second (same for both)
	TestDuration    time.Duration // How long to run the test
	ConnectionWait  time.Duration // Time to wait for all connections
	CollectInterval time.Duration // How often to collect metrics

	// Profiling settings
	ProfilingEnabled bool          // Enable profiling mode
	ProfileTypes     []string      // Profile types to collect: "cpu", "heap", "allocs", "block", "mutex"
	ProfileDuration  time.Duration // Duration for each profile run (default: 5 minutes)

	// Verbose output
	VerboseMetrics bool // Print detailed metrics deltas
	VerboseNetwork bool // Print network controller logs
}

// BaselineSRTConfig is the standard configuration: linked list, no io_uring
var BaselineSRTConfig = SRTConfig{
	ConnectionTimeout:      3000 * time.Millisecond,
	PeerIdleTimeout:        30000 * time.Millisecond,
	Latency:                3000 * time.Millisecond,
	RecvLatency:            3000 * time.Millisecond,
	PeerLatency:            3000 * time.Millisecond,
	IoUringEnabled:         false,  // NO io_uring
	IoUringRecvEnabled:     false,  // NO io_uring recv
	PacketReorderAlgorithm: "list", // Linked list packet store
	TLPktDrop:              true,
}

// HighPerfSRTConfig is the high-performance configuration: btree + io_uring + NAK btree
var HighPerfSRTConfig = SRTConfig{
	ConnectionTimeout:      3000 * time.Millisecond,
	PeerIdleTimeout:        30000 * time.Millisecond,
	Latency:                3000 * time.Millisecond,
	RecvLatency:            3000 * time.Millisecond,
	PeerLatency:            3000 * time.Millisecond,
	IoUringEnabled:         true,    // io_uring for SRT send
	IoUringRecvEnabled:     true,    // io_uring for SRT recv
	PacketReorderAlgorithm: "btree", // B-tree packet store
	BTreeDegree:            32,
	TLPktDrop:              true,
	// NAK btree for io_uring reorder handling
	UseNakBtree: true, // NAK btree for efficient gap detection
	// Note: SuppressImmediateNak is auto-set internally when UseNakBtree=true
	FastNakEnabled:       true, // FastNAK for outage recovery
	FastNakRecentEnabled: true, // FastNAKRecent for sequence jump detection
	HonorNakOrder:        true, // Sender honors receiver priority in NAK order
	NakRecentPercent:     0.10, // 10% of TSBPD delay for "too recent" window
}

// GetBaselineServerFlags returns CLI flags for the baseline server
func (c *ParallelTestConfig) GetBaselineServerFlags(testID string) []string {
	return c.getServerFlags(c.Baseline, "baseline", testID)
}

// GetHighPerfServerFlags returns CLI flags for the high-perf server
func (c *ParallelTestConfig) GetHighPerfServerFlags(testID string) []string {
	return c.getServerFlags(c.HighPerf, "highperf", testID)
}

// getPipelineColor returns the terminal color for a pipeline label
// Baseline = blue (cold/control), HighPerf = green (success/optimized)
func getPipelineColor(label string) string {
	switch label {
	case "baseline":
		return "blue"
	case "highperf":
		return "green"
	default:
		return ""
	}
}

func (c *ParallelTestConfig) getServerFlags(p PipelineConfig, label, testID string) []string {
	udsPath := fmt.Sprintf("/tmp/srt_server_%s_%s.sock", label, testID)
	flags := []string{
		"-addr", p.GetServerAddr(),
		"-promuds", udsPath,
		"-name", label + "-server",
	}
	if color := getPipelineColor(label); color != "" {
		flags = append(flags, "-color", color)
	}
	flags = append(flags, p.SRT.ToCliFlags()...)
	return flags
}

// GetBaselineClientGeneratorFlags returns CLI flags for the baseline client-generator
func (c *ParallelTestConfig) GetBaselineClientGeneratorFlags(testID string) []string {
	return c.getClientGeneratorFlags(c.Baseline, "baseline", testID)
}

// GetHighPerfClientGeneratorFlags returns CLI flags for the high-perf client-generator
func (c *ParallelTestConfig) GetHighPerfClientGeneratorFlags(testID string) []string {
	return c.getClientGeneratorFlags(c.HighPerf, "highperf", testID)
}

func (c *ParallelTestConfig) getClientGeneratorFlags(p PipelineConfig, label, testID string) []string {
	udsPath := fmt.Sprintf("/tmp/srt_clientgen_%s_%s.sock", label, testID)
	publisherURL := fmt.Sprintf("srt://%s/%s", p.GetServerAddr(), p.StreamID)
	flags := []string{
		"-to", publisherURL,
		"-bitrate", strconv.FormatInt(c.Bitrate, 10),
		"-localaddr", p.PublisherIP,
		"-promuds", udsPath,
		"-name", label + "-cg",
	}
	if color := getPipelineColor(label); color != "" {
		flags = append(flags, "-color", color)
	}
	flags = append(flags, p.SRT.ToCliFlags()...)
	return flags
}

// GetBaselineClientFlags returns CLI flags for the baseline client
func (c *ParallelTestConfig) GetBaselineClientFlags(testID string) []string {
	return c.getClientFlags(c.Baseline, "baseline", testID, false)
}

// GetHighPerfClientFlags returns CLI flags for the high-perf client
func (c *ParallelTestConfig) GetHighPerfClientFlags(testID string) []string {
	return c.getClientFlags(c.HighPerf, "highperf", testID, true)
}

func (c *ParallelTestConfig) getClientFlags(p PipelineConfig, label, testID string, ioUringOutput bool) []string {
	udsPath := fmt.Sprintf("/tmp/srt_client_%s_%s.sock", label, testID)
	subscriberURL := fmt.Sprintf("srt://%s?streamid=subscribe:/%s&mode=caller", p.GetServerAddr(), p.StreamID)
	flags := []string{
		"-from", subscriberURL,
		"-to", "null",
		"-localaddr", p.SubscriberIP,
		"-promuds", udsPath,
		"-name", label + "-client",
	}
	if color := getPipelineColor(label); color != "" {
		flags = append(flags, "-color", color)
	}
	if ioUringOutput {
		flags = append(flags, "-iouringoutput")
	}
	flags = append(flags, p.SRT.ToCliFlags()...)
	return flags
}

// GetAllUDSPaths returns all 6 UDS paths for metrics collection
func (c *ParallelTestConfig) GetAllUDSPaths(testID string) map[string]string {
	return map[string]string{
		"server_baseline":    fmt.Sprintf("/tmp/srt_server_baseline_%s.sock", testID),
		"server_highperf":    fmt.Sprintf("/tmp/srt_server_highperf_%s.sock", testID),
		"clientgen_baseline": fmt.Sprintf("/tmp/srt_clientgen_baseline_%s.sock", testID),
		"clientgen_highperf": fmt.Sprintf("/tmp/srt_clientgen_highperf_%s.sock", testID),
		"client_baseline":    fmt.Sprintf("/tmp/srt_client_baseline_%s.sock", testID),
		"client_highperf":    fmt.Sprintf("/tmp/srt_client_highperf_%s.sock", testID),
	}
}

// ============================================================================
// ISOLATION TEST CONFIGURATION
// ============================================================================
// These types support running simplified CG→Server tests to isolate
// which component/feature causes performance differences.
// No Client (subscriber), no network impairment, 30 second tests.

// IsolationTestConfig defines a simplified CG→Server test for variable isolation
type IsolationTestConfig struct {
	// Test identification
	Name        string // Primary test name using standardized format
	LegacyName  string // Old test name for backward compatibility (optional)
	Description string

	// Control pipeline (reference): list, no io_uring
	ControlCG     SRTConfig
	ControlServer SRTConfig

	// Test pipeline: one variable changed from control
	TestCG     SRTConfig
	TestServer SRTConfig

	// Test settings
	TestDuration   time.Duration // 30 seconds
	Bitrate        int64         // 5 Mb/s
	StatsPeriod    time.Duration // Stats display period (e.g., 10s to reduce output)
	LogTopics      string        // Comma-separated log topics for debugging (e.g., "listen:io_uring:completion:seq")
	VerboseMetrics bool          // Print detailed metrics at each stats period
}

// ControlSRTConfig is the base control configuration: list, no io_uring
// This is the reference point for all isolation tests.
var ControlSRTConfig = SRTConfig{
	ConnectionTimeout:      3000 * time.Millisecond,
	PeerIdleTimeout:        30000 * time.Millisecond,
	Latency:                3000 * time.Millisecond,
	RecvLatency:            3000 * time.Millisecond,
	PeerLatency:            3000 * time.Millisecond,
	IoUringEnabled:         false,  // NO io_uring send
	IoUringRecvEnabled:     false,  // NO io_uring recv
	PacketReorderAlgorithm: "list", // Linked list packet store
	TLPktDrop:              true,
}

// WithIoUringSend returns a copy of the config with io_uring send enabled
func (c SRTConfig) WithIoUringSend() SRTConfig {
	c.IoUringEnabled = true
	return c
}

// WithIoUringRecv returns a copy of the config with io_uring recv enabled
func (c SRTConfig) WithIoUringRecv() SRTConfig {
	c.IoUringRecvEnabled = true
	return c
}

// WithBtree returns a copy of the config with btree packet store
func (c SRTConfig) WithBtree(degree int) SRTConfig {
	c.PacketReorderAlgorithm = "btree"
	c.BTreeDegree = degree
	return c
}

// WithNakBtree returns a copy of the config with NAK btree enabled
// This enables NAK btree + FastNAK + HonorNakOrder.
// Note: SuppressImmediateNak is auto-set internally when UseNakBtree=true
func (c SRTConfig) WithNakBtree() SRTConfig {
	c.UseNakBtree = true
	// SuppressImmediateNak is auto-set by ApplyAutoConfiguration() when UseNakBtree=true
	c.FastNakEnabled = true
	c.FastNakRecentEnabled = true
	c.HonorNakOrder = true
	c.NakRecentPercent = 0.10 // Explicit 10% of TSBPD delay for "too recent" window
	return c
}

// WithHonorNakOrder returns a copy of the config with HonorNakOrder enabled
// This is a SENDER feature: retransmit packets in the order specified by the NAK packet
func (c SRTConfig) WithHonorNakOrder() SRTConfig {
	c.HonorNakOrder = true
	return c
}

// WithNakRecentPercent returns a copy of the config with a custom NakRecentPercent
// This controls the "too recent" window as a percentage of TSBPD delay.
// Default is 0.10 (10%), but for io_uring recv which can cause more reordering,
// a higher value like 0.50 (50%) may be needed.
func (c SRTConfig) WithNakRecentPercent(percent float64) SRTConfig {
	c.NakRecentPercent = percent
	return c
}

// ============================================================================
// NAK btree Permutation Helpers
// ============================================================================
// These helpers enable granular control over NAK btree features for permutation testing.
// See nak_btree_integration_testing.md for the permutation matrix.

// WithNakBtreeOnly enables NAK btree without FastNAK or HonorNakOrder.
// Use for baseline NAK btree testing (permutation #1).
// This is useful to isolate NAK btree behavior from FastNAK optimizations.
func (c SRTConfig) WithNakBtreeOnly() SRTConfig {
	c.UseNakBtree = true
	c.FastNakEnabled = false
	c.FastNakRecentEnabled = false
	c.HonorNakOrder = false
	c.NakRecentPercent = 0.10
	return c
}

// WithFastNak enables FastNAK optimization.
// FastNAK triggers immediate NAK after silence period (outage recovery).
// Requires NAK btree to be enabled.
func (c SRTConfig) WithFastNak() SRTConfig {
	c.FastNakEnabled = true
	return c
}

// WithFastNakRecent enables FastNAKRecent optimization.
// FastNAKRecent detects sequence jumps after network outages.
// Requires FastNAK to be enabled (no-op without it).
func (c SRTConfig) WithFastNakRecent() SRTConfig {
	c.FastNakRecentEnabled = true
	return c
}

// WithoutFastNak disables FastNAK while keeping NAK btree enabled.
// Also disables FastNAKRecent since it depends on FastNAK.
func (c SRTConfig) WithoutFastNak() SRTConfig {
	c.FastNakEnabled = false
	c.FastNakRecentEnabled = false
	return c
}

// WithoutHonorNakOrder disables HonorNakOrder.
// Use to test NAK btree without sender-side optimization.
func (c SRTConfig) WithoutHonorNakOrder() SRTConfig {
	c.HonorNakOrder = false
	return c
}

// WithoutFastNakRecent disables FastNAKRecent while keeping FastNAK enabled.
// Use for permutation testing to isolate FastNAKRecent effects.
func (c SRTConfig) WithoutFastNakRecent() SRTConfig {
	c.FastNakRecentEnabled = false
	return c
}

// WithLatency returns a copy with all latency settings adjusted.
// This sets Latency, RecvLatency, and PeerLatency to the same value.
func (c SRTConfig) WithLatency(d time.Duration) SRTConfig {
	c.Latency = d
	c.RecvLatency = d
	c.PeerLatency = d
	return c
}

// WithTimerProfile applies a timer interval preset to the config.
// Note: Timer intervals are applied via CLI flags, not SRTConfig fields.
// This helper is for documentation/consistency - actual application happens in ToCliFlags extensions.
func (c SRTConfig) WithTimerProfile(profile TimerProfile) SRTConfig {
	intervals := GetTimerIntervals(profile)
	c.TickIntervalMs = intervals.TickMs
	c.PeriodicNakIntervalMs = intervals.NakMs
	c.PeriodicAckIntervalMs = intervals.AckMs
	return c
}

// WithTimerIntervals applies custom timer intervals to the config.
func (c SRTConfig) WithTimerIntervals(tick, nak, ack uint64) SRTConfig {
	c.TickIntervalMs = tick
	c.PeriodicNakIntervalMs = nak
	c.PeriodicAckIntervalMs = ack
	return c
}

// ============================================================================
// LOCK-FREE RING BUFFER HELPERS (Phase 3: Lockless Design)
// ============================================================================

// WithPacketRing enables the lock-free ring buffer with default settings.
// Default: 1024 ring size, 4 shards, 10 retries, 100µs backoff, unlimited backoffs.
func (c SRTConfig) WithPacketRing() SRTConfig {
	c.UsePacketRing = true
	c.PacketRingSize = 1024
	c.PacketRingShards = 4
	c.PacketRingMaxRetries = 10
	c.PacketRingBackoffDuration = 100 * time.Microsecond
	c.PacketRingMaxBackoffs = 0 // Unlimited
	return c
}

// WithPacketRingCustom enables the lock-free ring buffer with custom settings.
func (c SRTConfig) WithPacketRingCustom(size, shards, maxRetries int, backoffDuration time.Duration, maxBackoffs int) SRTConfig {
	c.UsePacketRing = true
	c.PacketRingSize = size
	c.PacketRingShards = shards
	c.PacketRingMaxRetries = maxRetries
	c.PacketRingBackoffDuration = backoffDuration
	c.PacketRingMaxBackoffs = maxBackoffs
	return c
}

// WithoutPacketRing disables the lock-free ring buffer.
func (c SRTConfig) WithoutPacketRing() SRTConfig {
	c.UsePacketRing = false
	return c
}

// ============================================================================
// EVENT LOOP HELPERS (Phase 4: Lockless Design)
// ============================================================================

// WithEventLoop enables the continuous event loop with default settings.
// NOTE: Requires UsePacketRing=true (automatically enabled by this method).
// Default: 1s rate interval, 1000 cold start packets, 10µs-1ms backoff range.
func (c SRTConfig) WithEventLoop() SRTConfig {
	// Event loop requires ring buffer
	if !c.UsePacketRing {
		c = c.WithPacketRing()
	}
	c.UseEventLoop = true
	c.EventLoopRateInterval = 1 * time.Second
	c.BackoffColdStartPkts = 1000
	c.BackoffMinSleep = 10 * time.Microsecond
	c.BackoffMaxSleep = 1 * time.Millisecond
	return c
}

// WithEventLoopCustom enables the event loop with custom settings.
// NOTE: Requires UsePacketRing=true (automatically enabled by this method).
func (c SRTConfig) WithEventLoopCustom(rateInterval time.Duration, coldStartPkts int, minSleep, maxSleep time.Duration) SRTConfig {
	// Event loop requires ring buffer
	if !c.UsePacketRing {
		c = c.WithPacketRing()
	}
	c.UseEventLoop = true
	c.EventLoopRateInterval = rateInterval
	c.BackoffColdStartPkts = coldStartPkts
	c.BackoffMinSleep = minSleep
	c.BackoffMaxSleep = maxSleep
	return c
}

// WithoutEventLoop disables the event loop (uses timer-driven Tick).
func (c SRTConfig) WithoutEventLoop() SRTConfig {
	c.UseEventLoop = false
	return c
}

// ============================================================================
// LOCKLESS SENDER HELPERS (Phase 5+: Lockless Sender Design)
// ============================================================================

// WithSendBtree enables sender btree for O(log n) NAK lookup.
// Default: degree 32
func (c SRTConfig) WithSendBtree() SRTConfig {
	c.UseSendBtree = true
	c.SendBtreeDegree = 32
	return c
}

// WithSendRing enables the lock-free sender data ring.
// REQUIRES: UseSendBtree=true
// Default: 1024 ring size, 1 shard (for strict ordering)
func (c SRTConfig) WithSendRing() SRTConfig {
	if !c.UseSendBtree {
		c = c.WithSendBtree()
	}
	c.UseSendRing = true
	c.SendRingSize = 1024
	c.SendRingShards = 1 // Default: single shard for ordering
	return c
}

// WithSendRingCustom enables the lock-free sender data ring with custom settings.
func (c SRTConfig) WithSendRingCustom(size, shards int) SRTConfig {
	if !c.UseSendBtree {
		c = c.WithSendBtree()
	}
	c.UseSendRing = true
	c.SendRingSize = size
	c.SendRingShards = shards
	return c
}

// WithSendControlRing enables the lock-free sender control ring.
// REQUIRES: UseSendRing=true
// Default: 4096 size, 4 shards
// NOTE: Increased from 256/2 after ACK-drop fix caused failures at 300+ Mb/s with 4 recv rings.
// With EventLoop mode, dropped ACKs (when ring is full) can cause connection failures.
// See: performance_testing_implementation_log.md "Isolation Test Regression" section.
func (c SRTConfig) WithSendControlRing() SRTConfig {
	if !c.UseSendRing {
		c = c.WithSendRing()
	}
	c.UseSendControlRing = true
	c.SendControlRingSize = 4096 // Was 256 - increased to prevent ACK drops at high throughput
	c.SendControlRingShards = 4  // Was 2 - increased for better parallelism
	return c
}

// WithSendEventLoop enables the sender EventLoop.
// REQUIRES: UseSendControlRing=true
// Default: 100µs-1ms backoff, 0.9 TSBPD factor
func (c SRTConfig) WithSendEventLoop() SRTConfig {
	if !c.UseSendControlRing {
		c = c.WithSendControlRing()
	}
	c.UseSendEventLoop = true
	c.SendEventLoopBackoffMinSleep = 100 * time.Microsecond
	c.SendEventLoopBackoffMaxSleep = 1 * time.Millisecond
	c.SendTsbpdSleepFactor = 0.9
	// NOTE: SendDropThresholdUs = 0 means use the auto-calculated value from connection.go:
	// dropThreshold = 1.25 * peerTsbpdDelay + SendDropDelay (min 1 second)
	// This ensures drop threshold is always >= TSBPD latency for proper retransmission.
	c.SendDropThresholdUs = 0
	return c
}

// WithSendEventLoopCustom enables the sender EventLoop with custom settings.
func (c SRTConfig) WithSendEventLoopCustom(minSleep, maxSleep time.Duration, tsbpdFactor float64, dropThresholdUs uint64) SRTConfig {
	if !c.UseSendControlRing {
		c = c.WithSendControlRing()
	}
	c.UseSendEventLoop = true
	c.SendEventLoopBackoffMinSleep = minSleep
	c.SendEventLoopBackoffMaxSleep = maxSleep
	c.SendTsbpdSleepFactor = tsbpdFactor
	c.SendDropThresholdUs = dropThresholdUs
	return c
}

// WithoutSendEventLoop disables the sender EventLoop.
func (c SRTConfig) WithoutSendEventLoop() SRTConfig {
	c.UseSendEventLoop = false
	return c
}

// ============================================================================
// COMPLETELY LOCK-FREE RECEIVER HELPERS (Phase 6: Receiver Control Ring)
// ============================================================================

// WithRecvControlRing enables the receiver control ring for ACKACK/KEEPALIVE processing.
// This makes the receiver completely lock-free when combined with UseEventLoop.
// Reference: completely_lockfree_receiver.md
// NOTE: Increased sizes from 128/1 after ACK-drop fix caused failures at 300+ Mb/s.
func (c SRTConfig) WithRecvControlRing() SRTConfig {
	if !c.UseEventLoop {
		panic("WithRecvControlRing requires UseEventLoop=true")
	}
	c.UseRecvControlRing = true
	c.RecvControlRingSize = 2048 // Was 128 - increased to prevent control packet drops
	c.RecvControlRingShards = 2  // Was 1 - increased for better parallelism
	return c
}

// WithRecvControlRingCustom enables the receiver control ring with custom settings.
func (c SRTConfig) WithRecvControlRingCustom(size, shards int) SRTConfig {
	if !c.UseEventLoop {
		panic("WithRecvControlRingCustom requires UseEventLoop=true")
	}
	c.UseRecvControlRing = true
	c.RecvControlRingSize = size
	c.RecvControlRingShards = shards
	return c
}

// WithoutRecvControlRing disables the receiver control ring.
func (c SRTConfig) WithoutRecvControlRing() SRTConfig {
	c.UseRecvControlRing = false
	return c
}

// WithValidateSendPayloadSize enables payload size validation in sender Push().
func (c SRTConfig) WithValidateSendPayloadSize() SRTConfig {
	c.ValidateSendPayloadSize = true
	return c
}

// WithLegacyEventLoop disables the tight loop, using unbounded drain mode.
// This may help with stability at high parallelism (4+ recv rings) at the cost
// of higher control latency (~1-2ms vs ~500ns).
// Use this if experiencing issues with tight loop + high recv ring counts.
func (c SRTConfig) WithLegacyEventLoop() SRTConfig {
	c.EventLoopMaxDataPerIteration = 0 // 0 = unbounded legacy mode
	return c
}

// WithTightLoopEventLoop enables the tight loop with a custom batch size.
// Default is 512 when using library default (-1).
// Use 0 for unbounded legacy mode.
func (c SRTConfig) WithTightLoopEventLoop(batchSize int) SRTConfig {
	c.EventLoopMaxDataPerIteration = batchSize
	return c
}

// ============================================================================
// RING RETRY STRATEGY HELPERS
// ============================================================================

// WithRetryStrategy sets the ring retry strategy.
// Valid strategies: "sleep", "next", "random", "adaptive", "spin", "hybrid"
func (c SRTConfig) WithRetryStrategy(strategy string) SRTConfig {
	c.PacketRingRetryStrategy = strategy
	return c
}

// ============================================================================
// IO_URING TUNING HELPERS
// ============================================================================

// WithLargeIoUringRecvRing increases the io_uring receive ring size for high-throughput scenarios.
// Default ring size is 512; this increases it to 8192 to prevent CQ overflow at high packet rates.
// Use this when testing at 200+ Mb/s to avoid ring overflow crashes.
func (c SRTConfig) WithLargeIoUringRecvRing() SRTConfig {
	c.IoUringRecvRingSize = 8192
	c.IoUringRecvBatchSize = 512 // Larger batch for high throughput
	return c
}

// WithUltraHighThroughput configures for 500+ Mb/s throughput testing.
// This increases all buffer sizes and flow control windows to handle extreme packet rates.
// At 500 Mb/s with 1456 byte payloads: ~43,000 packets/sec
func (c SRTConfig) WithUltraHighThroughput() SRTConfig {
	// io_uring settings
	c.IoUringRecvRingSize = 16384  // 2x larger than large
	c.IoUringRecvBatchSize = 1024  // 2x larger batch

	// SRT buffer settings
	c.FC = 102400                  // 4x default flow control (25600 * 4)
	c.RecvBuf = 64 * 1024 * 1024   // 64 MB receive buffer
	c.SendBuf = 64 * 1024 * 1024   // 64 MB send buffer
	c.Latency = 5000 * time.Millisecond      // 5 second latency buffer
	c.RecvLatency = 5000 * time.Millisecond  // 5 second recv latency
	c.PeerLatency = 5000 * time.Millisecond  // 5 second peer latency

	// Packet ring - CRITICAL for high throughput!
	// Default: 1024 * 4 = 4096 slots fills in ~95ms at 500 Mb/s
	// Need: at least 1 second of buffer = 43000 packets
	c.UsePacketRing = true
	c.PacketRingSize = 16384       // 16x larger (16384 * 8 = 131k slots)
	c.PacketRingShards = 8         // More shards for parallelism
	c.PacketRingMaxRetries = 100   // More retries before giving up
	c.PacketRingBackoffDuration = 50 * time.Microsecond // Faster retry

	// Send ring - also needs to be larger
	c.UseSendRing = true
	c.SendRingSize = 8192          // 8x larger
	c.SendRingShards = 4           // More shards

	return c
}

// WithIoUringRecvRingCustom allows custom io_uring receive ring configuration.
func (c SRTConfig) WithIoUringRecvRingCustom(ringSize, batchSize int) SRTConfig {
	c.IoUringRecvRingSize = ringSize
	c.IoUringRecvBatchSize = batchSize
	return c
}

// ============================================================================
// MULTIPLE IO_URING RINGS HELPERS (Phase 1: multi_iouring_design.md)
// ============================================================================

// WithMultipleRecvRings enables multiple io_uring receive rings.
// REQUIRES: IoUringRecvEnabled=true
// This enables parallel completion processing for reduced latency.
// Valid values: 1-16 (power of 2 recommended)
func (c SRTConfig) WithMultipleRecvRings(count int) SRTConfig {
	if !c.IoUringRecvEnabled {
		c.IoUringRecvEnabled = true
	}
	if count < 1 || count > 16 {
		panic("IoUringRecvRingCount must be 1-16")
	}
	c.IoUringRecvRingCount = count
	return c
}

// WithMultipleSendRings enables multiple io_uring send rings per connection.
// REQUIRES: IoUringEnabled=true
// This enables parallel send completion processing.
// Valid values: 1-8 (power of 2 recommended)
func (c SRTConfig) WithMultipleSendRings(count int) SRTConfig {
	if !c.IoUringEnabled {
		c.IoUringEnabled = true
	}
	if count < 1 || count > 8 {
		panic("IoUringSendRingCount must be 1-8")
	}
	c.IoUringSendRingCount = count
	return c
}

// WithParallelIoUring enables multiple rings for both send and receive paths.
// This is the high-performance configuration for lowest latency.
func (c SRTConfig) WithParallelIoUring(recvCount, sendCount int) SRTConfig {
	return c.WithMultipleRecvRings(recvCount).WithMultipleSendRings(sendCount)
}

// ============================================================================
// PERFORMANCE MAXIMIZATION HELPERS (500 Mb/s Target)
// Reference: documentation/performance_maximization_500mbps.md
// ============================================================================

// WithAggressiveBuffers increases all buffer sizes 2x for 400+ Mb/s targets.
// Use this when standard WithUltraHighThroughput() isn't enough.
func (c SRTConfig) WithAggressiveBuffers() SRTConfig {
	// Flow control - 2x
	c.FC = 204800 // 8x default (25600 * 8)

	// SRT buffers - 2x
	c.RecvBuf = 128 * 1024 * 1024 // 128 MB
	c.SendBuf = 128 * 1024 * 1024 // 128 MB

	// Latency buffer - increased
	c.Latency = 8000 * time.Millisecond    // 8 seconds
	c.RecvLatency = 8000 * time.Millisecond
	c.PeerLatency = 8000 * time.Millisecond

	// Packet ring - 2x
	c.UsePacketRing = true
	c.PacketRingSize = 32768  // 2x (32k * 16 shards = 524k slots)
	c.PacketRingShards = 16   // 2x
	c.PacketRingMaxRetries = 200
	c.PacketRingBackoffDuration = 25 * time.Microsecond

	// Send ring - 2x
	c.UseSendRing = true
	c.SendRingSize = 16384 // 2x
	c.SendRingShards = 8   // 2x

	// io_uring recv - 2x
	c.IoUringRecvRingSize = 32768  // 2x
	c.IoUringRecvBatchSize = 2048  // 2x

	return c
}

// WithAggressiveTimers reduces timer intervals for faster response at high throughput.
// Lower intervals = more responsive but higher CPU usage.
func (c SRTConfig) WithAggressiveTimers() SRTConfig {
	// Timer intervals - halved
	c.TickIntervalMs = 5          // 5ms tick (was 10)
	c.PeriodicNakIntervalMs = 10  // 10ms NAK (was 20)
	c.PeriodicAckIntervalMs = 5   // 5ms ACK (was 10)

	// Receiver EventLoop backoff - reduced
	c.BackoffMinSleep = 1 * time.Microsecond   // 1µs (was 10µs)
	c.BackoffMaxSleep = 100 * time.Microsecond // 100µs (was 1ms)

	// Sender EventLoop backoff - reduced
	c.SendEventLoopBackoffMinSleep = 10 * time.Microsecond  // 10µs (was 100µs)
	c.SendEventLoopBackoffMaxSleep = 100 * time.Microsecond // 100µs (was 1ms)
	c.SendTsbpdSleepFactor = 0.95                           // Wake up closer to deadline

	return c
}

// WithExtremeBuffers increases all buffer sizes 4x for 500 Mb/s targets.
// WARNING: This uses significant memory (~1GB total buffers).
func (c SRTConfig) WithExtremeBuffers() SRTConfig {
	// Flow control - 4x
	c.FC = 409600 // 16x default

	// SRT buffers - 4x
	c.RecvBuf = 256 * 1024 * 1024 // 256 MB
	c.SendBuf = 256 * 1024 * 1024 // 256 MB

	// Latency buffer - maximum
	c.Latency = 10000 * time.Millisecond    // 10 seconds
	c.RecvLatency = 10000 * time.Millisecond
	c.PeerLatency = 10000 * time.Millisecond

	// Packet ring - 4x
	c.UsePacketRing = true
	c.PacketRingSize = 65536  // 4x (65k * 32 shards = 2M slots!)
	c.PacketRingShards = 32   // 4x
	c.PacketRingMaxRetries = 500
	c.PacketRingBackoffDuration = 10 * time.Microsecond

	// Send ring - 4x
	c.UseSendRing = true
	c.SendRingSize = 32768 // 4x
	c.SendRingShards = 16  // 4x

	// io_uring recv - 4x
	c.IoUringRecvRingSize = 65536 // 4x
	c.IoUringRecvBatchSize = 4096 // 4x

	return c
}

// WithExtremeTimers uses the most aggressive timer settings possible.
// WARNING: This will use significant CPU. Only use for benchmarking.
func (c SRTConfig) WithExtremeTimers() SRTConfig {
	// Timer intervals - minimum practical
	c.TickIntervalMs = 2         // 2ms tick
	c.PeriodicNakIntervalMs = 5  // 5ms NAK
	c.PeriodicAckIntervalMs = 2  // 2ms ACK

	// Receiver EventLoop backoff - near zero
	c.BackoffMinSleep = 100 * time.Nanosecond  // 100ns
	c.BackoffMaxSleep = 10 * time.Microsecond  // 10µs

	// Sender EventLoop backoff - near zero
	c.SendEventLoopBackoffMinSleep = 1 * time.Microsecond // 1µs
	c.SendEventLoopBackoffMaxSleep = 10 * time.Microsecond // 10µs
	c.SendTsbpdSleepFactor = 0.99 // Wake up very close to deadline

	return c
}

// With500MbpsOptimized applies all optimizations for 500 Mb/s throughput.
// This is the recommended starting point for 500 Mb/s experiments.
func (c SRTConfig) With500MbpsOptimized() SRTConfig {
	return c.WithUltraHighThroughput().
		WithAggressiveBuffers().
		WithAggressiveTimers().
		WithMultipleRecvRings(2) // 2 rings is the sweet spot
}

// ============================================================================
// DEBUG HELPERS
// ============================================================================

// WithReceiverDebug enables debug logging in the receiver.
func (c SRTConfig) WithReceiverDebug() SRTConfig {
	c.ReceiverDebug = true
	return c
}

// WithLogTopics sets the log topics for debug output.
// Topics use prefix matching: "receiver" matches "receiver:nak:debug", etc.
func (c SRTConfig) WithLogTopics(topics string) SRTConfig {
	c.LogTopics = topics
	return c
}

// GetControlCGFlags returns CLI flags for the control client-generator
func (c *IsolationTestConfig) GetControlCGFlags(testID string) []string {
	udsPath := fmt.Sprintf("/tmp/srt_cg_control_%s.sock", testID)
	// Control CG → Control Server on port 6000
	flags := []string{
		"-to", "srt://10.2.1.2:6000/test-stream-control",
		"-bitrate", strconv.FormatInt(c.Bitrate, 10),
		"-localaddr", "10.1.1.2",
		"-promuds", udsPath,
		"-name", "control-cg",
	}
	if c.StatsPeriod > 0 {
		flags = append(flags, "-statsperiod", c.StatsPeriod.String())
	}
	flags = append(flags, c.ControlCG.ToCliFlags()...)
	return flags
}

// GetTestCGFlags returns CLI flags for the test client-generator
func (c *IsolationTestConfig) GetTestCGFlags(testID string) []string {
	udsPath := fmt.Sprintf("/tmp/srt_cg_test_%s.sock", testID)
	// Test CG → Test Server on port 6001
	flags := []string{
		"-to", "srt://10.2.1.3:6001/test-stream-test",
		"-bitrate", strconv.FormatInt(c.Bitrate, 10),
		"-localaddr", "10.1.1.3",
		"-promuds", udsPath,
		"-name", "test-cg",
	}
	if c.StatsPeriod > 0 {
		flags = append(flags, "-statsperiod", c.StatsPeriod.String())
	}
	flags = append(flags, c.TestCG.ToCliFlags()...)
	return flags
}

// GetControlServerFlags returns CLI flags for the control server
func (c *IsolationTestConfig) GetControlServerFlags(testID string) []string {
	udsPath := fmt.Sprintf("/tmp/srt_server_control_%s.sock", testID)
	flags := []string{
		"-addr", "10.2.1.2:6000",
		"-promuds", udsPath,
		"-name", "control-server",
	}
	flags = append(flags, c.ControlServer.ToCliFlags()...)
	return flags
}

// GetTestServerFlags returns CLI flags for the test server
func (c *IsolationTestConfig) GetTestServerFlags(testID string) []string {
	udsPath := fmt.Sprintf("/tmp/srt_server_test_%s.sock", testID)
	flags := []string{
		"-addr", "10.2.1.3:6001",
		"-promuds", udsPath,
		"-name", "test-server",
	}
	if c.LogTopics != "" {
		flags = append(flags, "-logtopics", c.LogTopics)
	}
	flags = append(flags, c.TestServer.ToCliFlags()...)
	return flags
}

// GetAllUDSPaths returns UDS paths for the 4 processes
func (c *IsolationTestConfig) GetAllUDSPaths(testID string) map[string]string {
	return map[string]string{
		"cg_control":     fmt.Sprintf("/tmp/srt_cg_control_%s.sock", testID),
		"cg_test":        fmt.Sprintf("/tmp/srt_cg_test_%s.sock", testID),
		"server_control": fmt.Sprintf("/tmp/srt_server_control_%s.sock", testID),
		"server_test":    fmt.Sprintf("/tmp/srt_server_test_%s.sock", testID),
	}
}
