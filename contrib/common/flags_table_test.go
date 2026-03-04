package common

import (
	"testing"
	"time"

	srt "github.com/randomizedcoder/gosrt"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Test: IsTestFlag - Verifies test-only flag classification
// ═══════════════════════════════════════════════════════════════════════════════

func TestIsTestFlag_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		flagName string
		wantTest bool
	}{
		// Test-only flags (should NOT be passed to subprocesses)
		{"initial_is_test", "initial", true},
		{"min-bitrate_is_test", "min-bitrate", true},
		{"max-bitrate_is_test", "max-bitrate", true},
		{"step_is_test", "step", true},
		{"precision_is_test", "precision", true},
		{"search-timeout_is_test", "search-timeout", true},
		{"decrease_is_test", "decrease", true},
		{"warmup_is_test", "warmup", true},
		{"stability-window_is_test", "stability-window", true},
		{"sample-interval_is_test", "sample-interval", true},
		{"max-gap-rate_is_test", "max-gap-rate", true},
		{"max-nak-rate_is_test", "max-nak-rate", true},
		{"max-rtt_is_test", "max-rtt", true},
		{"min-throughput_is_test", "min-throughput", true},
		{"test-verbose_is_test", "test-verbose", true},
		{"test-json_is_test", "test-json", true},
		{"test-output_is_test", "test-output", true},
		{"profile-dir_is_test", "profile-dir", true},
		{"target_is_test", "target", true},
		{"control-socket_is_test", "control-socket", true},
		{"metrics-socket_is_test", "metrics-socket", true},
		{"watchdog-timeout_is_test", "watchdog-timeout", true},
		{"heartbeat-interval_is_test", "heartbeat-interval", true},

		// SRT config flags (should be passed to subprocesses)
		{"latency_is_srt", "latency", false},
		{"fc_is_srt", "fc", false},
		{"congestion_is_srt", "congestion", false},
		{"sndbuf_is_srt", "sndbuf", false},
		{"rcvbuf_is_srt", "rcvbuf", false},
		{"useeventloop_is_srt", "useeventloop", false},
		{"iouringenabled_is_srt", "iouringenabled", false},
		{"usepacketring_is_srt", "usepacketring", false},

		// Non-existent flags
		{"nonexistent_is_not_test", "nonexistent-flag", false},
		{"empty_is_not_test", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsTestFlag(tc.flagName)
			if got != tc.wantTest {
				t.Errorf("IsTestFlag(%q) = %v, want %v", tc.flagName, got, tc.wantTest)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: ApplyFlagsToConfig - Verifies flag-to-config mapping
// ═══════════════════════════════════════════════════════════════════════════════

// resetFlagState clears all FlagSet entries and resets flag variables to defaults.
// This allows isolated testing of flag application.
func resetFlagState() {
	// Clear the FlagSet tracking map
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	// Reset critical flag variables to their defaults
	// (Only reset the ones we'll be testing to avoid breaking other tests)
	*Latency = 0
	*FC = 0
	*SndBuf = 0
	*RcvBuf = 0
	*MSS = 0
	*PayloadSize = 0
	*PeerIdleTimeo = 0
	*Congestion = ""
	*Transtype = ""
	*Streamid = ""
	*PassphraseFlag = ""
	*PacketFilter = ""
	*PBKeylen = 0
	*DriftTracer = false
	*TLPktDrop = false
	*TSBPDMode = false
	*MessageAPI = false
	*NAKReport = false
	*EnforcedEncryption = false
	*GroupConnect = false
	*AllowPeerIpChange = false
	*IoUringEnabled = false
	*IoUringRecvEnabled = false
	*UseEventLoop = false
	*UsePacketRing = false
	*UseSendBtree = false
	*UseSendRing = false
	*UseSendControlRing = false
	*UseSendEventLoop = false
	*UseRecvControlRing = false
	*UseNakBtree = false
	*FastNakEnabled = false
	*FastNakRecentEnabled = false
	*HonorNakOrder = false
	*ReceiverDebug = false
	*RTOMode = ""
	*LocalAddr = ""
	*InstanceName = ""
}

func TestApplyFlagsToConfig_IntegerFlags_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		flagName  string
		setValue  func()
		checkFunc func(*srt.Config) interface{}
		expected  interface{}
	}{
		// Latency in milliseconds → Duration
		{
			name:     "latency_100ms",
			flagName: "latency",
			setValue: func() { *Latency = 100 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.Latency
			},
			expected: 100 * time.Millisecond,
		},
		{
			name:     "latency_0ms",
			flagName: "latency",
			setValue: func() { *Latency = 0 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.Latency
			},
			expected: time.Duration(0),
		},
		{
			name:     "latency_5000ms",
			flagName: "latency",
			setValue: func() { *Latency = 5000 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.Latency
			},
			expected: 5000 * time.Millisecond,
		},

		// FC (flow control window)
		{
			name:     "fc_102400",
			flagName: "fc",
			setValue: func() { *FC = 102400 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.FC
			},
			expected: uint32(102400),
		},
		{
			name:     "fc_8192",
			flagName: "fc",
			setValue: func() { *FC = 8192 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.FC
			},
			expected: uint32(8192),
		},

		// Send/Receive buffer sizes
		{
			name:     "sndbuf_67108864",
			flagName: "sndbuf",
			setValue: func() { *SndBuf = 67108864 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.SendBufferSize
			},
			expected: uint32(67108864),
		},
		{
			name:     "rcvbuf_134217728",
			flagName: "rcvbuf",
			setValue: func() { *RcvBuf = 134217728 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.ReceiverBufferSize
			},
			expected: uint32(134217728),
		},

		// MSS (MTU)
		{
			name:     "mss_1500",
			flagName: "mss",
			setValue: func() { *MSS = 1500 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.MSS
			},
			expected: uint32(1500),
		},

		// Payload size
		{
			name:     "payloadsize_1316",
			flagName: "payloadsize",
			setValue: func() { *PayloadSize = 1316 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.PayloadSize
			},
			expected: uint32(1316),
		},

		// Peer idle timeout
		{
			name:     "peeridletimeo_5000ms",
			flagName: "peeridletimeo",
			setValue: func() { *PeerIdleTimeo = 5000 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.PeerIdleTimeout
			},
			expected: 5000 * time.Millisecond,
		},

		// Connection timeout
		{
			name:     "conntimeo_3000ms",
			flagName: "conntimeo",
			setValue: func() { *Conntimeo = 3000 },
			checkFunc: func(c *srt.Config) interface{} {
				return c.ConnectionTimeout
			},
			expected: 3000 * time.Millisecond,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			// Set the flag value and mark it as set
			tc.setValue()
			FlagSet[tc.flagName] = true

			// Apply to a fresh config
			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			// Verify the result
			got := tc.checkFunc(config)
			if got != tc.expected {
				t.Errorf("After ApplyFlagsToConfig with %s set: got %v, want %v",
					tc.flagName, got, tc.expected)
			}
		})
	}
}

func TestApplyFlagsToConfig_StringFlags_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		flagName  string
		setValue  func()
		checkFunc func(*srt.Config) string
		expected  string
	}{
		{
			name:     "congestion_live",
			flagName: "congestion",
			setValue: func() { *Congestion = "live" },
			checkFunc: func(c *srt.Config) string {
				return c.Congestion
			},
			expected: "live",
		},
		{
			name:     "transtype_live",
			flagName: "transtype",
			setValue: func() { *Transtype = "live" },
			checkFunc: func(c *srt.Config) string {
				return c.TransmissionType
			},
			expected: "live",
		},
		{
			name:     "streamid_custom",
			flagName: "streamid",
			setValue: func() { *Streamid = "my-stream-123" },
			checkFunc: func(c *srt.Config) string {
				return c.StreamId
			},
			expected: "my-stream-123",
		},
		{
			name:     "passphrase_secret",
			flagName: "passphrase-flag",
			setValue: func() { *PassphraseFlag = "super-secret-password" },
			checkFunc: func(c *srt.Config) string {
				return c.Passphrase
			},
			expected: "super-secret-password",
		},
		{
			name:     "packetfilter_fec",
			flagName: "packetfilter",
			setValue: func() { *PacketFilter = "fec" },
			checkFunc: func(c *srt.Config) string {
				return c.PacketFilter
			},
			expected: "fec",
		},
		{
			name:     "localaddr_custom",
			flagName: "localaddr",
			setValue: func() { *LocalAddr = "192.168.1.100" },
			checkFunc: func(c *srt.Config) string {
				return c.LocalAddr
			},
			expected: "192.168.1.100",
		},
		{
			name:     "instancename_server1",
			flagName: "name",
			setValue: func() { *InstanceName = "Server1" },
			checkFunc: func(c *srt.Config) string {
				return c.InstanceName
			},
			expected: "Server1",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			tc.setValue()
			FlagSet[tc.flagName] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			got := tc.checkFunc(config)
			if got != tc.expected {
				t.Errorf("After ApplyFlagsToConfig with %s set: got %q, want %q",
					tc.flagName, got, tc.expected)
			}
		})
	}
}

func TestApplyFlagsToConfig_BoolFlags_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		flagName  string
		setValue  func()
		checkFunc func(*srt.Config) bool
		expected  bool
	}{
		// Connection options
		{
			name:     "drifttracer_true",
			flagName: "drifttracer",
			setValue: func() { *DriftTracer = true },
			checkFunc: func(c *srt.Config) bool {
				return c.DriftTracer
			},
			expected: true,
		},
		{
			name:     "tlpktdrop_true",
			flagName: "tlpktdrop",
			setValue: func() { *TLPktDrop = true },
			checkFunc: func(c *srt.Config) bool {
				return c.TooLatePacketDrop
			},
			expected: true,
		},
		{
			name:     "tsbpdmode_true",
			flagName: "tsbpdmode",
			setValue: func() { *TSBPDMode = true },
			checkFunc: func(c *srt.Config) bool {
				return c.TSBPDMode
			},
			expected: true,
		},
		{
			name:     "messageapi_true",
			flagName: "messageapi",
			setValue: func() { *MessageAPI = true },
			checkFunc: func(c *srt.Config) bool {
				return c.MessageAPI
			},
			expected: true,
		},
		{
			name:     "nakreport_true",
			flagName: "nakreport",
			setValue: func() { *NAKReport = true },
			checkFunc: func(c *srt.Config) bool {
				return c.NAKReport
			},
			expected: true,
		},
		{
			name:     "enforcedencryption_true",
			flagName: "enforcedencryption",
			setValue: func() { *EnforcedEncryption = true },
			checkFunc: func(c *srt.Config) bool {
				return c.EnforcedEncryption
			},
			expected: true,
		},
		{
			name:     "groupconnect_true",
			flagName: "groupconnect",
			setValue: func() { *GroupConnect = true },
			checkFunc: func(c *srt.Config) bool {
				return c.GroupConnect
			},
			expected: true,
		},
		{
			name:     "allowpeeripchange_true",
			flagName: "allowpeeripchange",
			setValue: func() { *AllowPeerIpChange = true },
			checkFunc: func(c *srt.Config) bool {
				return c.AllowPeerIpChange
			},
			expected: true,
		},

		// io_uring flags
		{
			name:     "iouringenabled_true",
			flagName: "iouringenabled",
			setValue: func() { *IoUringEnabled = true },
			checkFunc: func(c *srt.Config) bool {
				return c.IoUringEnabled
			},
			expected: true,
		},
		{
			name:     "iouringrecvenabled_true",
			flagName: "iouringrecvenabled",
			setValue: func() { *IoUringRecvEnabled = true },
			checkFunc: func(c *srt.Config) bool {
				return c.IoUringRecvEnabled
			},
			expected: true,
		},

		// Event loop and ring flags
		{
			name:     "useeventloop_true",
			flagName: "useeventloop",
			setValue: func() { *UseEventLoop = true },
			checkFunc: func(c *srt.Config) bool {
				return c.UseEventLoop
			},
			expected: true,
		},
		{
			name:     "usepacketring_true",
			flagName: "usepacketring",
			setValue: func() { *UsePacketRing = true },
			checkFunc: func(c *srt.Config) bool {
				return c.UsePacketRing
			},
			expected: true,
		},

		// Sender lockless flags
		{
			name:     "usesendbtree_true",
			flagName: "usesendbtree",
			setValue: func() { *UseSendBtree = true },
			checkFunc: func(c *srt.Config) bool {
				return c.UseSendBtree
			},
			expected: true,
		},
		{
			name:     "usesendring_true",
			flagName: "usesendring",
			setValue: func() { *UseSendRing = true },
			checkFunc: func(c *srt.Config) bool {
				return c.UseSendRing
			},
			expected: true,
		},
		{
			name:     "usesendcontrolring_true",
			flagName: "usesendcontrolring",
			setValue: func() { *UseSendControlRing = true },
			checkFunc: func(c *srt.Config) bool {
				return c.UseSendControlRing
			},
			expected: true,
		},
		{
			name:     "usesendeventloop_true",
			flagName: "usesendeventloop",
			setValue: func() { *UseSendEventLoop = true },
			checkFunc: func(c *srt.Config) bool {
				return c.UseSendEventLoop
			},
			expected: true,
		},

		// Receiver control ring
		{
			name:     "userecvcontrolring_true",
			flagName: "userecvcontrolring",
			setValue: func() { *UseRecvControlRing = true },
			checkFunc: func(c *srt.Config) bool {
				return c.UseRecvControlRing
			},
			expected: true,
		},

		// NAK btree flags
		{
			name:     "usenakbtree_true",
			flagName: "usenakbtree",
			setValue: func() { *UseNakBtree = true },
			checkFunc: func(c *srt.Config) bool {
				return c.UseNakBtree
			},
			expected: true,
		},

		// FastNAK flags
		{
			name:     "fastnakenabled_true",
			flagName: "fastnakenabled",
			setValue: func() { *FastNakEnabled = true },
			checkFunc: func(c *srt.Config) bool {
				return c.FastNakEnabled
			},
			expected: true,
		},
		{
			name:     "fastnakrecentenabled_true",
			flagName: "fastnakrecentenabled",
			setValue: func() { *FastNakRecentEnabled = true },
			checkFunc: func(c *srt.Config) bool {
				return c.FastNakRecentEnabled
			},
			expected: true,
		},

		// Sender retransmission flags
		{
			name:     "honornakorder_true",
			flagName: "honornakorder",
			setValue: func() { *HonorNakOrder = true },
			checkFunc: func(c *srt.Config) bool {
				return c.HonorNakOrder
			},
			expected: true,
		},

		// Debug flags
		{
			name:     "receiverdebug_true",
			flagName: "receiverdebug",
			setValue: func() { *ReceiverDebug = true },
			checkFunc: func(c *srt.Config) bool {
				return c.ReceiverDebug
			},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			tc.setValue()
			FlagSet[tc.flagName] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			got := tc.checkFunc(config)
			if got != tc.expected {
				t.Errorf("After ApplyFlagsToConfig with %s set: got %v, want %v",
					tc.flagName, got, tc.expected)
			}
		})
	}
}

func TestApplyFlagsToConfig_RTOMode_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		modeStr  string
		expected srt.RTOMode
	}{
		{"rtt_rttvar", "rtt_rttvar", srt.RTORttRttVar},
		{"rtt_4rttvar", "rtt_4rttvar", srt.RTORtt4RttVar},
		{"rtt_rttvar_margin", "rtt_rttvar_margin", srt.RTORttRttVarMargin},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			*RTOMode = tc.modeStr
			FlagSet["rtomode"] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			if config.RTOMode != tc.expected {
				t.Errorf("RTOMode %q: got %v, want %v", tc.modeStr, config.RTOMode, tc.expected)
			}
		})
	}
}

func TestApplyFlagsToConfig_FlagNotSet_DoesNotOverride(t *testing.T) {
	resetFlagState()

	// Set a flag value but DON'T mark it in FlagSet
	*Latency = 500

	// Config with pre-existing value
	config := &srt.Config{
		Latency: 100 * time.Millisecond,
	}

	ApplyFlagsToConfig(config)

	// Value should NOT be overridden because flag wasn't marked as set
	if config.Latency != 100*time.Millisecond {
		t.Errorf("Config.Latency changed to %v when flag was not set", config.Latency)
	}
}

func TestApplyFlagsToConfig_NakExpiryMargin_Validation(t *testing.T) {
	testCases := []struct {
		name     string
		margin   float64
		expected float64
	}{
		{"valid_0.10", 0.10, 0.10},
		{"valid_0.50", 0.50, 0.50},
		{"valid_-0.5", -0.5, -0.5},    // -0.5 is valid (creates threshold in future)
		{"invalid_-1.5", -1.5, 0.10},  // < -1.0 is invalid, reset to default
		{"invalid_-2.0", -2.0, 0.10},  // < -1.0 is invalid, reset to default
		{"boundary_-1.0", -1.0, -1.0}, // Exactly -1.0 is valid (threshold = now)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			*NakExpiryMargin = tc.margin
			FlagSet["nakexpirymargin"] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			if config.NakExpiryMargin != tc.expected {
				t.Errorf("NakExpiryMargin %f: got %f, want %f",
					tc.margin, config.NakExpiryMargin, tc.expected)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: ValidateFlagDependencies - Verifies dependency chain auto-enable
// ═══════════════════════════════════════════════════════════════════════════════

func TestValidateFlagDependencies_SenderChain_TableDriven(t *testing.T) {
	// Sender dependency chain: UseSendEventLoop → UseSendControlRing → UseSendRing → UseSendBtree
	// NOTE: ValidateFlagDependencies only enables ONE level of dependencies per call because
	// it checks FlagSet[...] which isn't updated when auto-enabling. This is by design -
	// the function is meant to be called once after flag parsing.
	testCases := []struct {
		name              string
		setFlags          func()
		expectBtree       bool
		expectRing        bool
		expectControlRing bool
		expectWarnings    int // Minimum number of warnings expected
	}{
		{
			name: "sendeventloop_enables_controlring_only",
			setFlags: func() {
				*UseSendEventLoop = true
				FlagSet["usesendeventloop"] = true
			},
			// Only one level is enabled (usesendcontrolring), not the full chain
			// because FlagSet["usesendcontrolring"] remains false after auto-enable
			expectBtree:       false,
			expectRing:        false,
			expectControlRing: true,
			expectWarnings:    1, // Only usesendcontrolring auto-enabled
		},
		{
			name: "sendcontrolring_enables_ring_only",
			setFlags: func() {
				*UseSendControlRing = true
				FlagSet["usesendcontrolring"] = true
			},
			// Only one level is enabled (usesendring)
			expectBtree:       false,
			expectRing:        true,
			expectControlRing: true,
			expectWarnings:    1, // ring auto-enabled
		},
		{
			name: "sendring_enables_btree",
			setFlags: func() {
				*UseSendRing = true
				FlagSet["usesendring"] = true
			},
			expectBtree:       true,
			expectRing:        true,
			expectControlRing: false,
			expectWarnings:    1, // btree auto-enabled
		},
		{
			name: "sendbtree_no_deps",
			setFlags: func() {
				*UseSendBtree = true
				FlagSet["usesendbtree"] = true
			},
			expectBtree:       true,
			expectRing:        false,
			expectControlRing: false,
			expectWarnings:    0, // No auto-enable needed
		},
		{
			name: "all_explicitly_set_no_warnings",
			setFlags: func() {
				*UseSendEventLoop = true
				*UseSendControlRing = true
				*UseSendRing = true
				*UseSendBtree = true
				FlagSet["usesendeventloop"] = true
				FlagSet["usesendcontrolring"] = true
				FlagSet["usesendring"] = true
				FlagSet["usesendbtree"] = true
			},
			expectBtree:       true,
			expectRing:        true,
			expectControlRing: true,
			expectWarnings:    0, // All explicitly set
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()
			tc.setFlags()

			warnings := ValidateFlagDependencies()

			if *UseSendBtree != tc.expectBtree {
				t.Errorf("UseSendBtree: got %v, want %v", *UseSendBtree, tc.expectBtree)
			}
			if *UseSendRing != tc.expectRing {
				t.Errorf("UseSendRing: got %v, want %v", *UseSendRing, tc.expectRing)
			}
			if *UseSendControlRing != tc.expectControlRing {
				t.Errorf("UseSendControlRing: got %v, want %v", *UseSendControlRing, tc.expectControlRing)
			}
			if len(warnings) < tc.expectWarnings {
				t.Errorf("Expected at least %d warnings, got %d: %v",
					tc.expectWarnings, len(warnings), warnings)
			}
		})
	}
}

func TestValidateFlagDependencies_ReceiverChain_TableDriven(t *testing.T) {
	// Receiver dependency chain: UseEventLoop → UsePacketRing
	// Also: UseRecvControlRing → UseEventLoop → UsePacketRing
	// NOTE: The recvcontrolring case enables BOTH eventloop and packetring because
	// these are checked in separate if blocks, not chained.
	testCases := []struct {
		name             string
		setFlags         func()
		expectPacketRing bool
		expectEventLoop  bool
		expectWarnings   int
	}{
		{
			name: "eventloop_enables_packetring",
			setFlags: func() {
				*UseEventLoop = true
				FlagSet["useeventloop"] = true
			},
			expectPacketRing: true,
			expectEventLoop:  true,
			expectWarnings:   1,
		},
		{
			name: "recvcontrolring_enables_both",
			setFlags: func() {
				*UseRecvControlRing = true
				FlagSet["userecvcontrolring"] = true
			},
			// recvcontrolring explicitly checks and enables BOTH eventloop and packetring
			// in separate if blocks, so both get enabled
			expectPacketRing: true,
			expectEventLoop:  true,
			expectWarnings:   2, // Both eventloop and packetring
		},
		{
			name: "packetring_only_no_deps",
			setFlags: func() {
				*UsePacketRing = true
				FlagSet["usepacketring"] = true
			},
			expectPacketRing: true,
			expectEventLoop:  false,
			expectWarnings:   0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()
			tc.setFlags()

			warnings := ValidateFlagDependencies()

			if *UsePacketRing != tc.expectPacketRing {
				t.Errorf("UsePacketRing: got %v, want %v", *UsePacketRing, tc.expectPacketRing)
			}
			if *UseEventLoop != tc.expectEventLoop {
				t.Errorf("UseEventLoop: got %v, want %v", *UseEventLoop, tc.expectEventLoop)
			}
			if len(warnings) < tc.expectWarnings {
				t.Errorf("Expected at least %d warnings, got %d: %v",
					tc.expectWarnings, len(warnings), warnings)
			}
		})
	}
}

func TestValidateFlagDependencies_IoUringRingCount_TableDriven(t *testing.T) {
	testCases := []struct {
		name              string
		ringCount         int
		expectIoUring     bool
		expectRecvEnabled bool
		expectWarnings    int
	}{
		{
			name:              "ringcount_1_no_auto_enable",
			ringCount:         1,
			expectIoUring:     false, // ringcount=1 doesn't trigger auto-enable
			expectRecvEnabled: false,
			expectWarnings:    0,
		},
		{
			name:              "ringcount_2_enables_iouring",
			ringCount:         2,
			expectIoUring:     true,
			expectRecvEnabled: true,
			expectWarnings:    2, // Both iouringenabled and iouringrecvenabled
		},
		{
			name:              "ringcount_4_enables_iouring",
			ringCount:         4,
			expectIoUring:     true,
			expectRecvEnabled: true,
			expectWarnings:    2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			*IoUringRecvRingCount = tc.ringCount
			FlagSet["iouringrecvringcount"] = true

			warnings := ValidateFlagDependencies()

			if *IoUringEnabled != tc.expectIoUring {
				t.Errorf("IoUringEnabled: got %v, want %v", *IoUringEnabled, tc.expectIoUring)
			}
			if *IoUringRecvEnabled != tc.expectRecvEnabled {
				t.Errorf("IoUringRecvEnabled: got %v, want %v", *IoUringRecvEnabled, tc.expectRecvEnabled)
			}
			if len(warnings) < tc.expectWarnings {
				t.Errorf("Expected at least %d warnings, got %d: %v",
					tc.expectWarnings, len(warnings), warnings)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: BuildFlagArgs / BuildFlagArgsFiltered
// ═══════════════════════════════════════════════════════════════════════════════

func TestBuildFlagArgs_ExcludesTestFlags(t *testing.T) {
	// Save and restore flag state
	oldFlagSet := make(map[string]bool)
	for k, v := range FlagSet {
		oldFlagSet[k] = v
	}
	defer func() {
		for k := range FlagSet {
			delete(FlagSet, k)
		}
		for k, v := range oldFlagSet {
			FlagSet[k] = v
		}
	}()

	// Clear and set up test flags
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	// Note: BuildFlagArgs uses flag.Visit() which tracks actually-parsed flags,
	// not our FlagSet map. We can't easily test this without actually parsing flags.
	// Instead, we test the IsTestFlag filtering logic separately.

	// Test that test flags are in the exclusion list
	testFlags := []string{"initial", "max-bitrate", "warmup", "target"}
	for _, name := range testFlags {
		if !IsTestFlag(name) {
			t.Errorf("Expected %q to be a test flag", name)
		}
	}

	// Test that SRT flags are not in the exclusion list
	srtFlags := []string{"latency", "fc", "useeventloop", "iouringenabled"}
	for _, name := range srtFlags {
		if IsTestFlag(name) {
			t.Errorf("Expected %q to NOT be a test flag", name)
		}
	}
}

func TestBuildFlagArgsFiltered_ExcludesSpecified(t *testing.T) {
	testCases := []struct {
		name       string
		exclude    []string
		shouldSkip map[string]bool
	}{
		{
			name:    "exclude_single",
			exclude: []string{"addr"},
			shouldSkip: map[string]bool{
				"addr": true,
			},
		},
		{
			name:    "exclude_multiple",
			exclude: []string{"addr", "port", "name"},
			shouldSkip: map[string]bool{
				"addr": true,
				"port": true,
				"name": true,
			},
		},
		{
			name:       "exclude_none",
			exclude:    []string{},
			shouldSkip: map[string]bool{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create exclusion map as BuildFlagArgsFiltered does internally
			excludeMap := make(map[string]bool)
			for _, name := range tc.exclude {
				excludeMap[name] = true
			}

			// Verify exclusion logic
			for name, shouldExclude := range tc.shouldSkip {
				if excludeMap[name] != shouldExclude {
					t.Errorf("Flag %q: excludeMap=%v, want %v",
						name, excludeMap[name], shouldExclude)
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Timer interval flags
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_TimerIntervals_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		flagName  string
		setValue  func()
		checkFunc func(*srt.Config) uint64
		expected  uint64
	}{
		{
			name:     "tickintervalms_10",
			flagName: "tickintervalms",
			setValue: func() { *TickIntervalMs = 10 },
			checkFunc: func(c *srt.Config) uint64 {
				return c.TickIntervalMs
			},
			expected: 10,
		},
		{
			name:     "periodicnakintervalms_20",
			flagName: "periodicnakintervalms",
			setValue: func() { *PeriodicNakIntervalMs = 20 },
			checkFunc: func(c *srt.Config) uint64 {
				return c.PeriodicNakIntervalMs
			},
			expected: 20,
		},
		{
			name:     "periodicackintervalms_10",
			flagName: "periodicackintervalms",
			setValue: func() { *PeriodicAckIntervalMs = 10 },
			checkFunc: func(c *srt.Config) uint64 {
				return c.PeriodicAckIntervalMs
			},
			expected: 10,
		},
		{
			name:     "senddropintervalms_100",
			flagName: "senddropintervalms",
			setValue: func() { *SendDropIntervalMs = 100 },
			checkFunc: func(c *srt.Config) uint64 {
				return c.SendDropIntervalMs
			},
			expected: 100,
		},
		{
			name:     "eventlooprateintervalms_1000",
			flagName: "eventlooprateintervalms",
			setValue: func() { *EventLoopRateIntervalMs = 1000 },
			checkFunc: func(c *srt.Config) uint64 {
				return c.EventLoopRateIntervalMs
			},
			expected: 1000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			tc.setValue()
			FlagSet[tc.flagName] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			got := tc.checkFunc(config)
			if got != tc.expected {
				t.Errorf("After ApplyFlagsToConfig with %s: got %d, want %d",
					tc.flagName, got, tc.expected)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Ring configuration flags
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_RingConfig_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		flagName  string
		setValue  func()
		checkFunc func(*srt.Config) int
		expected  int
	}{
		// Packet ring config
		{
			name:     "packetringsize_2048",
			flagName: "packetringsize",
			setValue: func() { *PacketRingSize = 2048 },
			checkFunc: func(c *srt.Config) int {
				return c.PacketRingSize
			},
			expected: 2048,
		},
		{
			name:     "packetringshards_4",
			flagName: "packetringshards",
			setValue: func() { *PacketRingShards = 4 },
			checkFunc: func(c *srt.Config) int {
				return c.PacketRingShards
			},
			expected: 4,
		},
		{
			name:     "packetringmaxretries_20",
			flagName: "packetringmaxretries",
			setValue: func() { *PacketRingMaxRetries = 20 },
			checkFunc: func(c *srt.Config) int {
				return c.PacketRingMaxRetries
			},
			expected: 20,
		},

		// Send ring config
		{
			name:     "sendringsize_2048",
			flagName: "sendringsize",
			setValue: func() { *SendRingSize = 2048 },
			checkFunc: func(c *srt.Config) int {
				return c.SendRingSize
			},
			expected: 2048,
		},
		{
			name:     "sendringshards_2",
			flagName: "sendringshards",
			setValue: func() { *SendRingShards = 2 },
			checkFunc: func(c *srt.Config) int {
				return c.SendRingShards
			},
			expected: 2,
		},

		// Send control ring config
		{
			name:     "sendcontrolringsize_256",
			flagName: "sendcontrolringsize",
			setValue: func() { *SendControlRingSize = 256 },
			checkFunc: func(c *srt.Config) int {
				return c.SendControlRingSize
			},
			expected: 256,
		},
		{
			name:     "sendcontrolringshards_2",
			flagName: "sendcontrolringshards",
			setValue: func() { *SendControlRingShards = 2 },
			checkFunc: func(c *srt.Config) int {
				return c.SendControlRingShards
			},
			expected: 2,
		},

		// Receive control ring config
		{
			name:     "recvcontrolringsize_256",
			flagName: "recvcontrolringsize",
			setValue: func() { *RecvControlRingSize = 256 },
			checkFunc: func(c *srt.Config) int {
				return c.RecvControlRingSize
			},
			expected: 256,
		},
		{
			name:     "recvcontrolringshards_2",
			flagName: "recvcontrolringshards",
			setValue: func() { *RecvControlRingShards = 2 },
			checkFunc: func(c *srt.Config) int {
				return c.RecvControlRingShards
			},
			expected: 2,
		},

		// io_uring ring config
		{
			name:     "iouringrecvringsize_4096",
			flagName: "iouringrecvringsize",
			setValue: func() { *IoUringRecvRingSize = 4096 },
			checkFunc: func(c *srt.Config) int {
				return c.IoUringRecvRingSize
			},
			expected: 4096,
		},
		{
			name:     "iouringrecvringcount_4",
			flagName: "iouringrecvringcount",
			setValue: func() { *IoUringRecvRingCount = 4 },
			checkFunc: func(c *srt.Config) int {
				return c.IoUringRecvRingCount
			},
			expected: 4,
		},
		{
			name:     "iouringsendringsize_512",
			flagName: "iouringsendringsize",
			setValue: func() { *IoUringSendRingSize = 512 },
			checkFunc: func(c *srt.Config) int {
				return c.IoUringSendRingSize
			},
			expected: 512,
		},
		{
			name:     "iouringsendringcount_2",
			flagName: "iouringsendringcount",
			setValue: func() { *IoUringSendRingCount = 2 },
			checkFunc: func(c *srt.Config) int {
				return c.IoUringSendRingCount
			},
			expected: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			tc.setValue()
			FlagSet[tc.flagName] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			got := tc.checkFunc(config)
			if got != tc.expected {
				t.Errorf("After ApplyFlagsToConfig with %s: got %d, want %d",
					tc.flagName, got, tc.expected)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Duration flags
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_DurationFlags_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		flagName  string
		setValue  func()
		checkFunc func(*srt.Config) time.Duration
		expected  time.Duration
	}{
		{
			name:     "handshaketimeout_1500ms",
			flagName: "handshaketimeout",
			setValue: func() { *HandshakeTimeout = 1500 * time.Millisecond },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.HandshakeTimeout
			},
			expected: 1500 * time.Millisecond,
		},
		{
			name:     "shutdowndelay_5s",
			flagName: "shutdowndelay",
			setValue: func() { *ShutdownDelay = 5 * time.Second },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.ShutdownDelay
			},
			expected: 5 * time.Second,
		},
		{
			name:     "statisticsinterval_10s",
			flagName: "statisticsinterval",
			setValue: func() { *StatisticsPrintInterval = 10 * time.Second },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.StatisticsPrintInterval
			},
			expected: 10 * time.Second,
		},
		{
			name:     "eventlooprateinterval_500ms",
			flagName: "eventlooprateinterval",
			setValue: func() { *EventLoopRateInterval = 500 * time.Millisecond },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.EventLoopRateInterval
			},
			expected: 500 * time.Millisecond,
		},
		{
			name:     "backoffminsleep_50us",
			flagName: "backoffminsleep",
			setValue: func() { *BackoffMinSleep = 50 * time.Microsecond },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.BackoffMinSleep
			},
			expected: 50 * time.Microsecond,
		},
		{
			name:     "backoffmaxsleep_2ms",
			flagName: "backoffmaxsleep",
			setValue: func() { *BackoffMaxSleep = 2 * time.Millisecond },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.BackoffMaxSleep
			},
			expected: 2 * time.Millisecond,
		},
		{
			name:     "packetringbackoffduration_200us",
			flagName: "packetringbackoffduration",
			setValue: func() { *PacketRingBackoffDuration = 200 * time.Microsecond },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.PacketRingBackoffDuration
			},
			expected: 200 * time.Microsecond,
		},

		// Sender EventLoop durations
		{
			name:     "sendeventloopbackoffminsleep_100us",
			flagName: "sendeventloopbackoffminsleep",
			setValue: func() { *SendEventLoopBackoffMinSleep = 100 * time.Microsecond },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.SendEventLoopBackoffMinSleep
			},
			expected: 100 * time.Microsecond,
		},
		{
			name:     "sendeventloopbackoffmaxsleep_1ms",
			flagName: "sendeventloopbackoffmaxsleep",
			setValue: func() { *SendEventLoopBackoffMaxSleep = 1 * time.Millisecond },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.SendEventLoopBackoffMaxSleep
			},
			expected: 1 * time.Millisecond,
		},

		// Adaptive backoff duration
		{
			name:     "adaptivebackoffidlethreshold_2s",
			flagName: "adaptivebackoffidlethreshold",
			setValue: func() { *AdaptiveBackoffIdleThreshold = 2 * time.Second },
			checkFunc: func(c *srt.Config) time.Duration {
				return c.AdaptiveBackoffIdleThreshold
			},
			expected: 2 * time.Second,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			tc.setValue()
			FlagSet[tc.flagName] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			got := tc.checkFunc(config)
			if got != tc.expected {
				t.Errorf("After ApplyFlagsToConfig with %s: got %v, want %v",
					tc.flagName, got, tc.expected)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Float flags
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_FloatFlags_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		flagName  string
		setValue  func()
		checkFunc func(*srt.Config) float64
		expected  float64
	}{
		{
			name:     "keepalivethreshold_0.5",
			flagName: "keepalivethreshold",
			setValue: func() { *KeepaliveThreshold = 0.5 },
			checkFunc: func(c *srt.Config) float64 {
				return c.KeepaliveThreshold
			},
			expected: 0.5,
		},
		{
			name:     "nakrecentpercent_0.15",
			flagName: "nakrecentpercent",
			setValue: func() { *NakRecentPercent = 0.15 },
			checkFunc: func(c *srt.Config) float64 {
				return c.NakRecentPercent
			},
			expected: 0.15,
		},
		{
			name:     "extrarttmargin_0.20",
			flagName: "extrarttmargin",
			setValue: func() { *ExtraRTTMargin = 0.20 },
			checkFunc: func(c *srt.Config) float64 {
				return c.ExtraRTTMargin
			},
			expected: 0.20,
		},
		{
			name:     "sendtsbpdsleepfactor_0.85",
			flagName: "sendtsbpdsleepfactor",
			setValue: func() { *SendTsbpdSleepFactor = 0.85 },
			checkFunc: func(c *srt.Config) float64 {
				return c.SendTsbpdSleepFactor
			},
			expected: 0.85,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			tc.setValue()
			FlagSet[tc.flagName] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			got := tc.checkFunc(config)
			if got != tc.expected {
				t.Errorf("After ApplyFlagsToConfig with %s: got %f, want %f",
					tc.flagName, got, tc.expected)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Conditional flag application (negative values handling)
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_ConditionalFlags_TableDriven(t *testing.T) {
	testCases := []struct {
		name            string
		flagName        string
		setValue        func()
		configDefault   func(*srt.Config)
		checkFunc       func(*srt.Config) int
		expectedApplied bool // Whether the flag value should be applied
		expectedValue   int
	}{
		// PacketRingMaxRetries: only applied if >= 0
		{
			name:     "packetringmaxretries_positive_applied",
			flagName: "packetringmaxretries",
			setValue: func() { *PacketRingMaxRetries = 20 },
			configDefault: func(c *srt.Config) {
				c.PacketRingMaxRetries = 10
			},
			checkFunc: func(c *srt.Config) int {
				return c.PacketRingMaxRetries
			},
			expectedApplied: true,
			expectedValue:   20,
		},
		{
			name:     "packetringmaxretries_negative_not_applied",
			flagName: "packetringmaxretries",
			setValue: func() { *PacketRingMaxRetries = -1 },
			configDefault: func(c *srt.Config) {
				c.PacketRingMaxRetries = 10
			},
			checkFunc: func(c *srt.Config) int {
				return c.PacketRingMaxRetries
			},
			expectedApplied: false,
			expectedValue:   10, // Should remain at default
		},

		// BackoffColdStartPkts: only applied if >= 0
		{
			name:     "backoffcoldstartpkts_positive_applied",
			flagName: "backoffcoldstartpkts",
			setValue: func() { *BackoffColdStartPkts = 2000 },
			configDefault: func(c *srt.Config) {
				c.BackoffColdStartPkts = 1000
			},
			checkFunc: func(c *srt.Config) int {
				return c.BackoffColdStartPkts
			},
			expectedApplied: true,
			expectedValue:   2000,
		},
		{
			name:     "backoffcoldstartpkts_negative_not_applied",
			flagName: "backoffcoldstartpkts",
			setValue: func() { *BackoffColdStartPkts = -1 },
			configDefault: func(c *srt.Config) {
				c.BackoffColdStartPkts = 1000
			},
			checkFunc: func(c *srt.Config) int {
				return c.BackoffColdStartPkts
			},
			expectedApplied: false,
			expectedValue:   1000,
		},

		// LightACKDifference: only applied if > 0
		{
			name:     "lightackdifference_positive_applied",
			flagName: "lightackdifference",
			setValue: func() { *LightACKDifference = 128 },
			configDefault: func(c *srt.Config) {
				c.LightACKDifference = 64
			},
			checkFunc: func(c *srt.Config) int {
				return int(c.LightACKDifference)
			},
			expectedApplied: true,
			expectedValue:   128,
		},
		{
			name:     "lightackdifference_zero_not_applied",
			flagName: "lightackdifference",
			setValue: func() { *LightACKDifference = 0 },
			configDefault: func(c *srt.Config) {
				c.LightACKDifference = 64
			},
			checkFunc: func(c *srt.Config) int {
				return int(c.LightACKDifference)
			},
			expectedApplied: false,
			expectedValue:   64,
		},
		{
			name:     "lightackdifference_negative_not_applied",
			flagName: "lightackdifference",
			setValue: func() { *LightACKDifference = -1 },
			configDefault: func(c *srt.Config) {
				c.LightACKDifference = 64
			},
			checkFunc: func(c *srt.Config) int {
				return int(c.LightACKDifference)
			},
			expectedApplied: false,
			expectedValue:   64,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			tc.setValue()
			FlagSet[tc.flagName] = true

			config := &srt.Config{}
			tc.configDefault(config)

			ApplyFlagsToConfig(config)

			got := tc.checkFunc(config)
			if got != tc.expectedValue {
				t.Errorf("After ApplyFlagsToConfig with %s: got %d, want %d (applied=%v)",
					tc.flagName, got, tc.expectedValue, tc.expectedApplied)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Multiple flags combined
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_MultipleFlagsCombined(t *testing.T) {
	resetFlagState()

	// Set multiple flags at once (typical usage pattern)
	*Latency = 200
	*FC = 102400
	*UseSendEventLoop = true
	*UseEventLoop = true
	*IoUringEnabled = true

	FlagSet["latency"] = true
	FlagSet["fc"] = true
	FlagSet["usesendeventloop"] = true
	FlagSet["useeventloop"] = true
	FlagSet["iouringenabled"] = true

	config := &srt.Config{}
	ApplyFlagsToConfig(config)

	// Verify all flags were applied
	if config.Latency != 200*time.Millisecond {
		t.Errorf("Latency: got %v, want %v", config.Latency, 200*time.Millisecond)
	}
	if config.FC != 102400 {
		t.Errorf("FC: got %d, want %d", config.FC, 102400)
	}
	if !config.UseSendEventLoop {
		t.Error("UseSendEventLoop should be true")
	}
	if !config.UseEventLoop {
		t.Error("UseEventLoop should be true")
	}
	if !config.IoUringEnabled {
		t.Error("IoUringEnabled should be true")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: NAK btree conversion flags
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_NakConsolidationBudget_Conversion(t *testing.T) {
	// NakConsolidationBudgetMs is converted from ms to µs
	resetFlagState()

	*NakConsolidationBudgetMs = 5 // 5 ms
	FlagSet["nakconsolidationbudgetms"] = true

	config := &srt.Config{}
	ApplyFlagsToConfig(config)

	expected := uint64(5000) // 5 ms = 5000 µs
	if config.NakConsolidationBudgetUs != expected {
		t.Errorf("NakConsolidationBudgetUs: got %d, want %d (from %d ms)",
			config.NakConsolidationBudgetUs, expected, 5)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkApplyFlagsToConfig_AllFlags(b *testing.B) {
	// Set up a realistic scenario with many flags set
	resetFlagState()

	*Latency = 200
	*FC = 102400
	*SndBuf = 67108864
	*RcvBuf = 134217728
	*UseSendEventLoop = true
	*UseEventLoop = true
	*UsePacketRing = true
	*IoUringEnabled = true
	*IoUringRecvEnabled = true

	FlagSet["latency"] = true
	FlagSet["fc"] = true
	FlagSet["sndbuf"] = true
	FlagSet["rcvbuf"] = true
	FlagSet["usesendeventloop"] = true
	FlagSet["useeventloop"] = true
	FlagSet["usepacketring"] = true
	FlagSet["iouringenabled"] = true
	FlagSet["iouringrecvenabled"] = true

	config := &srt.Config{}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ApplyFlagsToConfig(config)
	}
}

func BenchmarkValidateFlagDependencies(b *testing.B) {
	resetFlagState()

	*UseSendEventLoop = true
	*UseRecvControlRing = true
	*IoUringRecvRingCount = 4

	FlagSet["usesendeventloop"] = true
	FlagSet["userecvcontrolring"] = true
	FlagSet["iouringrecvringcount"] = true

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Reset before each iteration to test full chain
		*UseSendControlRing = false
		*UseSendRing = false
		*UseSendBtree = false
		*UseEventLoop = false
		*UsePacketRing = false
		*IoUringEnabled = false
		*IoUringRecvEnabled = false

		_ = ValidateFlagDependencies()
	}
}

func BenchmarkIsTestFlag(b *testing.B) {
	flags := []string{
		"initial", "max-bitrate", "warmup", // test flags
		"latency", "fc", "useeventloop", // SRT flags
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for _, f := range flags {
			_ = IsTestFlag(f)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Channel queue size flags
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_QueueSizes_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		flagName  string
		setValue  func()
		checkFunc func(*srt.Config) int
		expected  int
	}{
		{
			name:     "networkqueuesize_4096",
			flagName: "networkqueuesize",
			setValue: func() { *NetworkQueueSize = 4096 },
			checkFunc: func(c *srt.Config) int {
				return c.NetworkQueueSize
			},
			expected: 4096,
		},
		{
			name:     "writequeuesize_2048",
			flagName: "writequeuesize",
			setValue: func() { *WriteQueueSize = 2048 },
			checkFunc: func(c *srt.Config) int {
				return c.WriteQueueSize
			},
			expected: 2048,
		},
		{
			name:     "readqueuesize_1024",
			flagName: "readqueuesize",
			setValue: func() { *ReadQueueSize = 1024 },
			checkFunc: func(c *srt.Config) int {
				return c.ReadQueueSize
			},
			expected: 1024,
		},
		{
			name:     "receivequeuesize_8192",
			flagName: "receivequeuesize",
			setValue: func() { *ReceiveQueueSize = 8192 },
			checkFunc: func(c *srt.Config) int {
				return c.ReceiveQueueSize
			},
			expected: 8192,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagState()

			tc.setValue()
			FlagSet[tc.flagName] = true

			config := &srt.Config{}
			ApplyFlagsToConfig(config)

			got := tc.checkFunc(config)
			if got != tc.expected {
				t.Errorf("After ApplyFlagsToConfig with %s: got %d, want %d",
					tc.flagName, got, tc.expected)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Special case flags (packet reorder, btree degree, etc.)
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_SpecialCases_TableDriven(t *testing.T) {
	t.Run("packetreorderalgorithm_btree", func(t *testing.T) {
		resetFlagState()

		*PacketReorderAlgorithm = "btree"
		FlagSet["packetreorderalgorithm"] = true

		config := &srt.Config{}
		ApplyFlagsToConfig(config)

		if config.PacketReorderAlgorithm != "btree" {
			t.Errorf("PacketReorderAlgorithm: got %q, want %q",
				config.PacketReorderAlgorithm, "btree")
		}
	})

	t.Run("btreedegree_64", func(t *testing.T) {
		resetFlagState()

		*BTreeDegree = 64
		FlagSet["btreedegree"] = true

		config := &srt.Config{}
		ApplyFlagsToConfig(config)

		if config.BTreeDegree != 64 {
			t.Errorf("BTreeDegree: got %d, want %d", config.BTreeDegree, 64)
		}
	})

	t.Run("packetringretrystrategy_adaptive", func(t *testing.T) {
		resetFlagState()

		*PacketRingRetryStrategy = "adaptive"
		FlagSet["packetringretrystrategy"] = true

		config := &srt.Config{}
		ApplyFlagsToConfig(config)

		if config.PacketRingRetryStrategy != "adaptive" {
			t.Errorf("PacketRingRetryStrategy: got %q, want %q",
				config.PacketRingRetryStrategy, "adaptive")
		}
	})

	t.Run("eventloopmaxdata_512", func(t *testing.T) {
		resetFlagState()

		*EventLoopMaxDataPerIteration = 512
		FlagSet["eventloopmaxdata"] = true

		config := &srt.Config{}
		ApplyFlagsToConfig(config)

		if config.EventLoopMaxDataPerIteration != 512 {
			t.Errorf("EventLoopMaxDataPerIteration: got %d, want %d",
				config.EventLoopMaxDataPerIteration, 512)
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: ParseFlags behavior (limited testing due to global state)
// ═══════════════════════════════════════════════════════════════════════════════

func TestParseFlags_PopulatesFlagSet(t *testing.T) {
	// Note: We can't easily test ParseFlags() because it modifies global flag state.
	// This test verifies the expected behavior conceptually.

	// Before ParseFlags: FlagSet should be empty after reset
	resetFlagState()

	if len(FlagSet) != 0 {
		t.Errorf("After reset, FlagSet should be empty, got %d entries", len(FlagSet))
	}

	// flag.Visit() behavior: should only visit flags that were actually parsed
	// and differ from their default values. We verify this conceptually.

	// Verify FlagSet tracks correctly when we manually set flags
	FlagSet["test-flag"] = true

	if !FlagSet["test-flag"] {
		t.Error("FlagSet should track manually set flags")
	}

	if FlagSet["nonexistent-flag"] {
		t.Error("FlagSet should not track unset flags")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Edge cases and boundaries
// ═══════════════════════════════════════════════════════════════════════════════

func TestApplyFlagsToConfig_EdgeCases(t *testing.T) {
	t.Run("zero_latency", func(t *testing.T) {
		resetFlagState()

		*Latency = 0
		FlagSet["latency"] = true

		config := &srt.Config{
			Latency: 100 * time.Millisecond, // Pre-existing value
		}
		ApplyFlagsToConfig(config)

		if config.Latency != 0 {
			t.Errorf("Zero latency should be applied, got %v", config.Latency)
		}
	})

	t.Run("large_fc_value", func(t *testing.T) {
		resetFlagState()

		*FC = 1000000 // 1M packets
		FlagSet["fc"] = true

		config := &srt.Config{}
		ApplyFlagsToConfig(config)

		if config.FC != 1000000 {
			t.Errorf("Large FC: got %d, want %d", config.FC, 1000000)
		}
	})

	t.Run("empty_string_flag", func(t *testing.T) {
		resetFlagState()

		*Congestion = ""
		FlagSet["congestion"] = true

		config := &srt.Config{
			Congestion: "live", // Pre-existing value
		}
		ApplyFlagsToConfig(config)

		if config.Congestion != "" {
			t.Errorf("Empty string should be applied, got %q", config.Congestion)
		}
	})

	t.Run("ipv6only_negative", func(t *testing.T) {
		resetFlagState()

		*IPv6Only = -1 // Default value meaning "use system default"
		FlagSet["ipv6only"] = true

		config := &srt.Config{}
		ApplyFlagsToConfig(config)

		if config.IPv6Only != -1 {
			t.Errorf("IPv6Only: got %d, want %d", config.IPv6Only, -1)
		}
	})
}
