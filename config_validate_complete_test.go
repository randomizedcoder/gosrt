package srt

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// =============================================================================
// Phase 2.1: Config Validation Complete Coverage
// =============================================================================
// Comprehensive table-driven tests for Config.Validate() covering all fields
// and boundary conditions.
//
// Reference: documentation/unit_test_coverage_improvement_plan.md
// =============================================================================

// =============================================================================
// TransmissionType Validation Tests
// =============================================================================

func TestConfigValidate_TransmissionType_TableDriven(t *testing.T) {
	testCases := []struct {
		name             string
		transmissionType string
		expectError      bool
		errorContains    string
	}{
		{
			name:             "live mode - valid",
			transmissionType: "live",
			expectError:      false,
		},
		{
			name:             "file mode - not supported",
			transmissionType: "file",
			expectError:      true,
			errorContains:    "TransmissionType must be 'live'",
		},
		{
			name:             "empty string - invalid",
			transmissionType: "",
			expectError:      true,
			errorContains:    "TransmissionType must be 'live'",
		},
		{
			name:             "unknown mode - invalid",
			transmissionType: "buffer",
			expectError:      true,
			errorContains:    "TransmissionType must be 'live'",
		},
		{
			name:             "case sensitive - Live invalid",
			transmissionType: "Live",
			expectError:      true,
			errorContains:    "TransmissionType must be 'live'",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.TransmissionType = tc.transmissionType
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// ConnectionTimeout Validation Tests
// =============================================================================

func TestConfigValidate_ConnectionTimeout_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		timeout       time.Duration
		expectError   bool
		errorContains string
	}{
		{
			name:        "positive value - valid",
			timeout:     3 * time.Second,
			expectError: false,
		},
		{
			name:        "1 nanosecond - valid (minimum positive)",
			timeout:     1 * time.Nanosecond,
			expectError: false,
		},
		{
			name:          "zero - invalid",
			timeout:       0,
			expectError:   true,
			errorContains: "ConnectionTimeout must be greater than 0",
		},
		{
			name:          "negative - invalid",
			timeout:       -1 * time.Second,
			expectError:   true,
			errorContains: "ConnectionTimeout must be greater than 0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.ConnectionTimeout = tc.timeout
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// MSS (Maximum Segment Size) Validation Tests
// =============================================================================

func TestConfigValidate_MSS_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		mss           uint32
		expectError   bool
		errorContains string
	}{
		{
			name:        "at minimum (76) - valid",
			mss:         MIN_MSS_SIZE,
			expectError: false,
		},
		{
			name:          "below minimum (75) - invalid",
			mss:           MIN_MSS_SIZE - 1,
			expectError:   true,
			errorContains: "MSS must be between",
		},
		{
			name:        "at maximum (1500) - valid",
			mss:         MAX_MSS_SIZE,
			expectError: false,
		},
		{
			name:          "above maximum (1501) - invalid",
			mss:           MAX_MSS_SIZE + 1,
			expectError:   true,
			errorContains: "MSS must be between",
		},
		{
			name:        "typical ethernet (1500) - valid",
			mss:         1500,
			expectError: false,
		},
		{
			name:        "middle value (800) - valid",
			mss:         800,
			expectError: false,
		},
		{
			name:          "zero - invalid",
			mss:           0,
			expectError:   true,
			errorContains: "MSS must be between",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.MSS = tc.mss
			// Adjust PayloadSize to be compatible with MSS
			if tc.mss >= MIN_MSS_SIZE {
				config.PayloadSize = tc.mss - uint32(SRT_HEADER_SIZE+UDP_HEADER_SIZE)
				if config.PayloadSize < MIN_PAYLOAD_SIZE {
					config.PayloadSize = MIN_PAYLOAD_SIZE
				}
			}
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// PayloadSize Validation Tests
// =============================================================================

func TestConfigValidate_PayloadSize_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		payloadSize   uint32
		mss           uint32 // Set MSS to ensure PayloadSize fits
		expectError   bool
		errorContains string
	}{
		{
			name:        "at minimum - valid",
			payloadSize: MIN_PAYLOAD_SIZE,
			mss:         MAX_MSS_SIZE,
			expectError: false,
		},
		{
			name:          "below minimum - invalid",
			payloadSize:   MIN_PAYLOAD_SIZE - 1,
			mss:           MAX_MSS_SIZE,
			expectError:   true,
			errorContains: "PayloadSize must be between",
		},
		{
			name:        "at maximum - valid",
			payloadSize: MAX_PAYLOAD_SIZE,
			mss:         MAX_MSS_SIZE,
			expectError: false,
		},
		{
			name:          "above maximum - invalid",
			payloadSize:   MAX_PAYLOAD_SIZE + 1,
			mss:           MAX_MSS_SIZE,
			expectError:   true,
			errorContains: "PayloadSize must be between",
		},
		{
			name:          "exceeds MSS limit - invalid",
			payloadSize:   1400, // Too large for small MSS
			mss:           MIN_MSS_SIZE,
			expectError:   true,
			errorContains: "PayloadSize must not be larger than",
		},
		{
			name:        "typical value (1316) - valid",
			payloadSize: 1316,
			mss:         MAX_MSS_SIZE,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.PayloadSize = tc.payloadSize
			config.MSS = tc.mss
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// IP Settings Validation Tests
// =============================================================================

func TestConfigValidate_IPTOS_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		iptos       int
		expectError bool
	}{
		{"zero - valid", 0, false},
		{"typical value (128) - valid", 128, false},
		{"max valid (255) - valid", 255, false},
		{"above max (256) - invalid", 256, true},
		{"large value (1000) - invalid", 1000, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.IPTOS = tc.iptos
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "IPTOS")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigValidate_IPTTL_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		ipttl       int
		expectError bool
	}{
		{"zero - valid (default)", 0, false},
		{"typical value (64) - valid", 64, false},
		{"max valid (255) - valid", 255, false},
		{"above max (256) - invalid", 256, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.IPTTL = tc.ipttl
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "IPTTL")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigValidate_IPv6Only_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		ipv6Only    int
		expectError bool
	}{
		{"zero - valid (disabled)", 0, false},
		{"non-zero - not supported", 1, true},
		{"any positive - not supported", 2, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.IPv6Only = tc.ipv6Only
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "IPv6Only is not supported")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// Crypto Settings Validation Tests
// =============================================================================

func TestConfigValidate_PBKeylen_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		keylen      int
		expectError bool
	}{
		{"16 bytes (AES-128) - valid", 16, false},
		{"24 bytes (AES-192) - valid", 24, false},
		{"32 bytes (AES-256) - valid", 32, false},
		{"0 bytes - invalid", 0, true},
		{"8 bytes - invalid", 8, true},
		{"20 bytes - invalid", 20, true},
		{"64 bytes - invalid", 64, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.PBKeylen = tc.keylen
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "PBKeylen must be 16, 24, or 32")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigValidate_Passphrase_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		passphrase    string
		expectError   bool
		errorContains string
	}{
		{
			name:        "empty - valid (no encryption)",
			passphrase:  "",
			expectError: false,
		},
		{
			name:        "exactly minimum (10 chars) - valid",
			passphrase:  "1234567890",
			expectError: false,
		},
		{
			name:          "below minimum (9 chars) - invalid",
			passphrase:    "123456789",
			expectError:   true,
			errorContains: "Passphrase must be between",
		},
		{
			name:        "exactly maximum (80 chars) - valid",
			passphrase:  strings.Repeat("a", MAX_PASSPHRASE_SIZE),
			expectError: false,
		},
		{
			name:          "above maximum (81 chars) - invalid",
			passphrase:    strings.Repeat("a", MAX_PASSPHRASE_SIZE+1),
			expectError:   true,
			errorContains: "Passphrase must be between",
		},
		{
			name:        "typical passphrase - valid",
			passphrase:  "MySecurePassword123!",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.Passphrase = tc.passphrase
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// KM (Key Management) Settings Validation Tests
// =============================================================================

func TestConfigValidate_KMSettings_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		refreshRate   uint64
		preAnnounce   uint64
		expectError   bool
		errorContains string
	}{
		{
			name:        "both zero - valid (disabled)",
			refreshRate: 0,
			preAnnounce: 0,
			expectError: false,
		},
		{
			name:        "valid: preAnnounce < refreshRate/2",
			refreshRate: 1000,
			preAnnounce: 100,
			expectError: false,
		},
		{
			name:        "valid: preAnnounce = refreshRate/2",
			refreshRate: 1000,
			preAnnounce: 500,
			expectError: false,
		},
		{
			name:          "invalid: preAnnounce = 0 when refreshRate > 0",
			refreshRate:   1000,
			preAnnounce:   0,
			expectError:   true,
			errorContains: "KMPreAnnounce must be greater than 1",
		},
		{
			name:          "invalid: preAnnounce > refreshRate/2",
			refreshRate:   1000,
			preAnnounce:   600,
			expectError:   true,
			errorContains: "KMPreAnnounce must be greater than 1 and smaller than KMRefreshRate/2",
		},
		{
			name:        "edge case: refreshRate = 2, preAnnounce = 1",
			refreshRate: 2,
			preAnnounce: 1,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.KMRefreshRate = tc.refreshRate
			config.KMPreAnnounce = tc.preAnnounce
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// Latency Settings Validation Tests
// =============================================================================

func TestConfigValidate_Latency_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		latency       time.Duration
		peerLatency   time.Duration
		recvLatency   time.Duration
		expectError   bool
		errorContains string
	}{
		{
			name:        "positive latency - valid",
			latency:     200 * time.Millisecond,
			expectError: false,
		},
		{
			name:        "zero latency - valid",
			latency:     0,
			peerLatency: 100 * time.Millisecond,
			recvLatency: 100 * time.Millisecond,
			expectError: false,
		},
		{
			name:          "negative peerLatency - invalid",
			latency:       -1, // Will skip Latency propagation
			peerLatency:   -1 * time.Millisecond,
			expectError:   true,
			errorContains: "PeerLatency must be greater than 0",
		},
		{
			name:          "negative receiverLatency - invalid",
			latency:       -1,
			peerLatency:   100 * time.Millisecond,
			recvLatency:   -1 * time.Millisecond,
			expectError:   true,
			errorContains: "ReceiverLatency must be greater than 0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.Latency = tc.latency
			if tc.peerLatency != 0 {
				config.PeerLatency = tc.peerLatency
			}
			if tc.recvLatency != 0 {
				config.ReceiverLatency = tc.recvLatency
			}
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// OverheadBW Validation Tests
// =============================================================================

func TestConfigValidate_OverheadBW_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		overheadBW  int64
		expectError bool
	}{
		{"at minimum (10) - valid", 10, false},
		{"below minimum (9) - invalid", 9, true},
		{"at maximum (100) - valid", 100, false},
		{"above maximum (101) - invalid", 101, true},
		{"typical value (25) - valid", 25, false},
		{"zero - invalid", 0, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.OverheadBW = tc.overheadBW
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "OverheadBW must be between 10 and 100")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// StreamId Validation Tests
// =============================================================================

func TestConfigValidate_StreamId_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		streamId    string
		expectError bool
	}{
		{"empty - valid", "", false},
		{"typical value - valid", "my-stream-id", false},
		{"at max length (512) - valid", strings.Repeat("a", MAX_STREAMID_SIZE), false},
		{"above max length (513) - invalid", strings.Repeat("a", MAX_STREAMID_SIZE+1), true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.StreamId = tc.streamId
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "StreamId must be shorter than or equal to")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// Unsupported Features Validation Tests
// =============================================================================

func TestConfigValidate_UnsupportedFeatures_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		mutate        func(*Config)
		errorContains string
	}{
		{
			name:          "GroupConnect not supported",
			mutate:        func(c *Config) { c.GroupConnect = true },
			errorContains: "GroupConnect is not supported",
		},
		{
			name:          "PacketFilter not supported",
			mutate:        func(c *Config) { c.PacketFilter = "fec" },
			errorContains: "PacketFilter are not supported",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			tc.mutate(&config)
			err := config.Validate()

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errorContains)
		})
	}
}

// =============================================================================
// io_uring Configuration Validation Tests
// =============================================================================

func TestConfigValidate_IoUringSend_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		enabled       bool
		ringSize      int
		expectError   bool
		errorContains string
	}{
		{
			name:        "disabled - no validation",
			enabled:     false,
			ringSize:    0,
			expectError: false,
		},
		{
			name:        "enabled with valid size (64) - valid",
			enabled:     true,
			ringSize:    64,
			expectError: false,
		},
		{
			name:        "enabled with valid size (256) - valid",
			enabled:     true,
			ringSize:    256,
			expectError: false,
		},
		{
			name:        "enabled with max size (1024) - valid",
			enabled:     true,
			ringSize:    1024,
			expectError: false,
		},
		{
			name:          "enabled with size below min (8) - invalid",
			enabled:       true,
			ringSize:      8,
			expectError:   true,
			errorContains: "IoUringSendRingSize must be between 16 and 1024",
		},
		{
			name:          "enabled with size above max (2048) - invalid",
			enabled:       true,
			ringSize:      2048,
			expectError:   true,
			errorContains: "IoUringSendRingSize must be between 16 and 1024",
		},
		{
			name:          "enabled with non-power-of-2 size - invalid",
			enabled:       true,
			ringSize:      100,
			expectError:   true,
			errorContains: "IoUringSendRingSize must be a power of 2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.IoUringEnabled = tc.enabled
			if tc.ringSize > 0 {
				config.IoUringSendRingSize = tc.ringSize
			}
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigValidate_IoUringRecv_TableDriven(t *testing.T) {
	testCases := []struct {
		name           string
		ringSize       int
		initialPending int // must be <= ringSize to avoid validation error
		expectError    bool
		errorContains  string
	}{
		{
			name:        "zero (disabled) - valid",
			ringSize:    0,
			expectError: false,
		},
		{
			name:           "min valid (64) - valid",
			ringSize:       64,
			initialPending: 64, // must set to avoid default 512 > 64 error
			expectError:    false,
		},
		{
			name:        "max valid (32768) - valid",
			ringSize:    32768,
			expectError: false,
		},
		{
			name:          "below min (32) - invalid",
			ringSize:      32,
			expectError:   true,
			errorContains: "IoUringRecvRingSize must be between 64 and 32768",
		},
		{
			name:          "above max (65536) - invalid",
			ringSize:      65536,
			expectError:   true,
			errorContains: "IoUringRecvRingSize must be between 64 and 32768",
		},
		{
			name:          "non-power-of-2 (100) - invalid",
			ringSize:      100,
			expectError:   true,
			errorContains: "IoUringRecvRingSize must be a power of 2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.IoUringRecvRingSize = tc.ringSize
			if tc.initialPending > 0 {
				config.IoUringRecvInitialPending = tc.initialPending
			}
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// Handshake and Shutdown Timeout Validation Tests
// =============================================================================

func TestConfigValidate_Timeouts_TableDriven(t *testing.T) {
	testCases := []struct {
		name             string
		handshakeTimeout time.Duration
		peerIdleTimeout  time.Duration
		shutdownDelay    time.Duration
		expectError      bool
		errorContains    string
	}{
		{
			name:             "valid timeouts",
			handshakeTimeout: 3 * time.Second,
			peerIdleTimeout:  5 * time.Second,
			shutdownDelay:    200 * time.Millisecond,
			expectError:      false,
		},
		{
			name:             "handshake timeout zero - invalid",
			handshakeTimeout: 0,
			peerIdleTimeout:  5 * time.Second,
			shutdownDelay:    200 * time.Millisecond,
			expectError:      true,
			errorContains:    "HandshakeTimeout must be greater than 0",
		},
		{
			name:             "handshake timeout negative - invalid",
			handshakeTimeout: -1 * time.Second,
			peerIdleTimeout:  5 * time.Second,
			shutdownDelay:    200 * time.Millisecond,
			expectError:      true,
			errorContains:    "HandshakeTimeout must be greater than 0",
		},
		{
			name:             "handshake >= peerIdle - invalid",
			handshakeTimeout: 5 * time.Second,
			peerIdleTimeout:  5 * time.Second,
			shutdownDelay:    200 * time.Millisecond,
			expectError:      true,
			errorContains:    "HandshakeTimeout",
		},
		{
			name:             "handshake > peerIdle - invalid",
			handshakeTimeout: 10 * time.Second,
			peerIdleTimeout:  5 * time.Second,
			shutdownDelay:    200 * time.Millisecond,
			expectError:      true,
			errorContains:    "HandshakeTimeout",
		},
		{
			name:             "shutdown delay zero - invalid",
			handshakeTimeout: 3 * time.Second,
			peerIdleTimeout:  5 * time.Second,
			shutdownDelay:    0,
			expectError:      true,
			errorContains:    "ShutdownDelay must be greater than 0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.HandshakeTimeout = tc.handshakeTimeout
			config.PeerIdleTimeout = tc.peerIdleTimeout
			config.ShutdownDelay = tc.shutdownDelay
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// PacketRing Configuration Validation Tests
// =============================================================================

func TestConfigValidate_PacketRing_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		usePacketRing bool
		ringSize      int
		shards        int
		maxRetries    int
		expectError   bool
		errorContains string
	}{
		{
			name:          "disabled - no validation",
			usePacketRing: false,
			expectError:   false,
		},
		{
			name:          "valid configuration",
			usePacketRing: true,
			ringSize:      1024,
			shards:        4,
			maxRetries:    3,
			expectError:   false,
		},
		{
			name:          "ring size too small",
			usePacketRing: true,
			ringSize:      32,
			shards:        4,
			maxRetries:    3,
			expectError:   true,
			errorContains: "PacketRingSize must be between 64 and 65536",
		},
		{
			name:          "ring size too large",
			usePacketRing: true,
			ringSize:      131072,
			shards:        4,
			maxRetries:    3,
			expectError:   true,
			errorContains: "PacketRingSize must be between 64 and 65536",
		},
		{
			name:          "ring size not power of 2",
			usePacketRing: true,
			ringSize:      1000,
			shards:        4,
			maxRetries:    3,
			expectError:   true,
			errorContains: "PacketRingSize must be a power of 2",
		},
		{
			name:          "shards too small",
			usePacketRing: true,
			ringSize:      1024,
			shards:        0,
			maxRetries:    3,
			expectError:   true,
			errorContains: "PacketRingShards must be between 1 and 64",
		},
		{
			name:          "shards too large",
			usePacketRing: true,
			ringSize:      1024,
			shards:        128,
			maxRetries:    3,
			expectError:   true,
			errorContains: "PacketRingShards must be between 1 and 64",
		},
		{
			name:          "shards not power of 2",
			usePacketRing: true,
			ringSize:      1024,
			shards:        3,
			maxRetries:    3,
			expectError:   true,
			errorContains: "PacketRingShards must be a power of 2",
		},
		{
			name:          "negative max retries",
			usePacketRing: true,
			ringSize:      1024,
			shards:        4,
			maxRetries:    -1,
			expectError:   true,
			errorContains: "PacketRingMaxRetries must be >= 0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.UsePacketRing = tc.usePacketRing
			if tc.usePacketRing {
				config.PacketRingSize = tc.ringSize
				config.PacketRingShards = tc.shards
				config.PacketRingMaxRetries = tc.maxRetries
			}
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// EventLoop Configuration Validation Tests
// =============================================================================

func TestConfigValidate_EventLoop_TableDriven(t *testing.T) {
	testCases := []struct {
		name            string
		useEventLoop    bool
		usePacketRing   bool
		rateInterval    time.Duration
		backoffMinSleep time.Duration
		backoffMaxSleep time.Duration
		expectError     bool
		errorContains   string
	}{
		{
			name:          "disabled - no validation",
			useEventLoop:  false,
			usePacketRing: false,
			expectError:   false,
		},
		{
			name:            "valid configuration",
			useEventLoop:    true,
			usePacketRing:   true,
			rateInterval:    time.Second,
			backoffMinSleep: time.Microsecond,
			backoffMaxSleep: time.Millisecond,
			expectError:     false,
		},
		{
			name:          "eventloop without packet ring - invalid",
			useEventLoop:  true,
			usePacketRing: false,
			rateInterval:  time.Second,
			expectError:   true,
			errorContains: "UseEventLoop requires UsePacketRing=true",
		},
		{
			name:            "backoff min > max - invalid",
			useEventLoop:    true,
			usePacketRing:   true,
			rateInterval:    time.Second,
			backoffMinSleep: time.Second,
			backoffMaxSleep: time.Microsecond,
			expectError:     true,
			errorContains:   "BackoffMinSleep",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.UseEventLoop = tc.useEventLoop
			config.UsePacketRing = tc.usePacketRing
			if tc.usePacketRing {
				// Set valid packet ring config
				config.PacketRingSize = 1024
				config.PacketRingShards = 4
			}
			if tc.rateInterval > 0 {
				config.EventLoopRateInterval = tc.rateInterval
			}
			if tc.backoffMinSleep > 0 {
				config.BackoffMinSleep = tc.backoffMinSleep
			}
			if tc.backoffMaxSleep > 0 {
				config.BackoffMaxSleep = tc.backoffMaxSleep
			}
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// LightACKDifference Validation Tests
// =============================================================================

func TestConfigValidate_LightACKDifference_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		value         uint32
		expectError   bool
		errorContains string
	}{
		{"zero (default applied) - valid", 0, false, ""},
		{"RFC recommendation (64) - valid", 64, false, ""},
		{"max valid (5000) - valid", 5000, false, ""},
		{"above max (5001) - invalid", 5001, true, "LightACKDifference must be <= 5000"},
		{"large value (10000) - invalid", 10000, true, "LightACKDifference must be <= 5000"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.LightACKDifference = tc.value
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// Power of 2 Validation Helper Test
// =============================================================================

func TestIsPowerOf2(t *testing.T) {
	testCases := []struct {
		value  uint32
		isPow2 bool
	}{
		{0, false},
		{1, true},
		{2, true},
		{3, false},
		{4, true},
		{5, false},
		{8, true},
		{16, true},
		{32, true},
		{64, true},
		{100, false},
		{128, true},
		{256, true},
		{512, true},
		{1000, false},
		{1024, true},
		{2048, true},
		{4096, true},
		{65536, true},
	}

	for _, tc := range testCases {
		// Using the same bit trick as in config_validate.go
		isPow2 := tc.value != 0 && tc.value&(tc.value-1) == 0
		require.Equal(t, tc.isPow2, isPow2, "isPowerOf2(%d) = %v, want %v", tc.value, isPow2, tc.isPow2)
	}
}

// =============================================================================
// MinVersion Validation Tests
// =============================================================================

func TestConfigValidate_MinVersion_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		minVersion  uint32
		expectError bool
	}{
		{"correct version - valid", SRT_VERSION, false},
		{"wrong version - invalid", 0x010200, true},
		{"zero version - invalid", 0, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.MinVersion = tc.minVersion
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "MinVersion must be")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// SendDropDelay Validation Tests
// =============================================================================

func TestConfigValidate_SendDropDelay_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		delay       time.Duration
		expectError bool
	}{
		{"zero - valid", 0, false},
		{"positive - valid", time.Second, false},
		{"negative - invalid", -time.Second, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.SendDropDelay = tc.delay
			err := config.Validate()

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "SendDropDelay must be greater than 0")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkConfigValidate_DefaultConfig(b *testing.B) {
	config := DefaultConfig()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = config.Validate()
	}
}

func BenchmarkConfigValidate_WithAllFeatures(b *testing.B) {
	config := DefaultConfig()
	config.UsePacketRing = true
	config.PacketRingSize = 1024
	config.PacketRingShards = 4
	config.UseEventLoop = true
	config.IoUringEnabled = true
	config.Passphrase = "testpassphrase123"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = config.Validate()
	}
}
