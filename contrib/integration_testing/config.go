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
)

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
	Name        string // Human-readable name (e.g., "SmallBuffers-1Mbps")
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
