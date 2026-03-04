package srt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Error Path Table-Driven Tests
// Validates that the library correctly rejects invalid configurations and
// handles error conditions gracefully.
// ═══════════════════════════════════════════════════════════════════════════

// ConfigErrorTestCase tests config validation errors
type ConfigErrorTestCase struct {
	Name          string
	ModifyConfig  func(c *Config)
	ExpectError   bool
	ErrorContains string
}

// configErrorTests validates configuration validation
var configErrorTests = []ConfigErrorTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Timeout Validations
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:         "Valid_Default",
		ModifyConfig: func(c *Config) {}, // No changes - default is valid
		ExpectError:  false,
	},
	{
		Name:          "Invalid_ConnectionTimeout_Zero",
		ModifyConfig:  func(c *Config) { c.ConnectionTimeout = 0 },
		ExpectError:   true,
		ErrorContains: "ConnectionTimeout must be greater than 0",
	},
	{
		Name:          "Invalid_ConnectionTimeout_Negative",
		ModifyConfig:  func(c *Config) { c.ConnectionTimeout = -1 * time.Second },
		ExpectError:   true,
		ErrorContains: "ConnectionTimeout must be greater than 0",
	},
	{
		Name:          "Invalid_HandshakeTimeout_Zero",
		ModifyConfig:  func(c *Config) { c.HandshakeTimeout = 0 },
		ExpectError:   true,
		ErrorContains: "HandshakeTimeout must be greater than 0",
	},
	{
		Name: "Invalid_HandshakeTimeout_GreaterThanPeerIdle",
		ModifyConfig: func(c *Config) {
			c.HandshakeTimeout = 3 * time.Second
			c.PeerIdleTimeout = 2 * time.Second
		},
		ExpectError:   true,
		ErrorContains: "HandshakeTimeout",
	},
	{
		Name:          "Invalid_ShutdownDelay_Zero",
		ModifyConfig:  func(c *Config) { c.ShutdownDelay = 0 },
		ExpectError:   true,
		ErrorContains: "ShutdownDelay must be greater than 0",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// MSS and Payload Size
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Invalid_MSS_TooSmall",
		ModifyConfig:  func(c *Config) { c.MSS = 50 },
		ExpectError:   true,
		ErrorContains: "MSS must be between",
	},
	{
		Name:          "Invalid_MSS_TooLarge",
		ModifyConfig:  func(c *Config) { c.MSS = 100000 },
		ExpectError:   true,
		ErrorContains: "MSS must be between",
	},
	{
		Name:          "Invalid_PayloadSize_TooSmall",
		ModifyConfig:  func(c *Config) { c.PayloadSize = 10 },
		ExpectError:   true,
		ErrorContains: "PayloadSize must be between",
	},
	{
		Name:          "Invalid_PayloadSize_TooLarge",
		ModifyConfig:  func(c *Config) { c.PayloadSize = 100000 },
		ExpectError:   true,
		ErrorContains: "PayloadSize must be between",
	},
	{
		Name: "Invalid_PayloadSize_LargerThanMSS",
		ModifyConfig: func(c *Config) {
			c.MSS = 1500
			// MAX_PAYLOAD_SIZE is 1456, so we use a value just under that
			// but still larger than MSS-headers (1500-44=1456)
			// Need to set MSS smaller to trigger this specific error
			c.MSS = 500
			c.PayloadSize = 500 // larger than 500-44=456
		},
		ExpectError:   true,
		ErrorContains: "PayloadSize must not be larger than",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Passphrase / Encryption
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Invalid_Passphrase_TooShort",
		ModifyConfig:  func(c *Config) { c.Passphrase = "short" },
		ExpectError:   true,
		ErrorContains: "Passphrase must be between",
	},
	{
		Name: "Invalid_Passphrase_TooLong",
		ModifyConfig: func(c *Config) {
			c.Passphrase = "this-is-a-very-long-passphrase-that-exceeds-the-maximum-allowed-length-of-80-characters-by-quite-a-bit"
		},
		ExpectError:   true,
		ErrorContains: "Passphrase must be between",
	},
	{
		Name:         "Valid_Passphrase_MinLength",
		ModifyConfig: func(c *Config) { c.Passphrase = "1234567890" }, // Exactly MIN_PASSPHRASE_SIZE
		ExpectError:  false,
	},
	{
		Name:          "Invalid_PBKeylen_Invalid",
		ModifyConfig:  func(c *Config) { c.PBKeylen = 20 }, // Must be 16, 24, or 32
		ExpectError:   true,
		ErrorContains: "PBKeylen must be 16, 24, or 32",
	},
	{
		Name:         "Valid_PBKeylen_16",
		ModifyConfig: func(c *Config) { c.PBKeylen = 16 },
		ExpectError:  false,
	},
	{
		Name:         "Valid_PBKeylen_24",
		ModifyConfig: func(c *Config) { c.PBKeylen = 24 },
		ExpectError:  false,
	},
	{
		Name:         "Valid_PBKeylen_32",
		ModifyConfig: func(c *Config) { c.PBKeylen = 32 },
		ExpectError:  false,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// IP Settings
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Invalid_IPTOS_TooLarge",
		ModifyConfig:  func(c *Config) { c.IPTOS = 256 },
		ExpectError:   true,
		ErrorContains: "IPTOS must be lower than 255",
	},
	{
		Name:          "Invalid_IPTTL_TooLarge",
		ModifyConfig:  func(c *Config) { c.IPTTL = 256 },
		ExpectError:   true,
		ErrorContains: "IPTTL must be between 1 and 255",
	},
	{
		Name:          "Invalid_IPv6Only",
		ModifyConfig:  func(c *Config) { c.IPv6Only = 1 },
		ExpectError:   true,
		ErrorContains: "IPv6Only is not supported",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Required Features
	// NOTE: NAKReport, TooLatePacketDrop, TSBPDMode are forcibly set to true
	// in Validate(), so testing their "disabled" state would be testing dead code.
	// ═══════════════════════════════════════════════════════════════════════

	// ═══════════════════════════════════════════════════════════════════════
	// Overhead and Bandwidth
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Invalid_OverheadBW_TooLow",
		ModifyConfig:  func(c *Config) { c.OverheadBW = 5 },
		ExpectError:   true,
		ErrorContains: "OverheadBW must be between 10 and 100",
	},
	{
		Name:          "Invalid_OverheadBW_TooHigh",
		ModifyConfig:  func(c *Config) { c.OverheadBW = 150 },
		ExpectError:   true,
		ErrorContains: "OverheadBW must be between 10 and 100",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Unsupported Features
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Invalid_GroupConnect",
		ModifyConfig:  func(c *Config) { c.GroupConnect = true },
		ExpectError:   true,
		ErrorContains: "GroupConnect is not supported",
	},
	{
		Name:          "Invalid_PacketFilter",
		ModifyConfig:  func(c *Config) { c.PacketFilter = "fec" },
		ExpectError:   true,
		ErrorContains: "PacketFilter are not supported",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// StreamId
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name: "Invalid_StreamId_TooLong",
		ModifyConfig: func(c *Config) {
			// MAX_STREAMID_SIZE is 512
			longId := make([]byte, 600)
			for i := range longId {
				longId[i] = 'a'
			}
			c.StreamId = string(longId)
		},
		ExpectError:   true,
		ErrorContains: "StreamId must be shorter than",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Latency
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Invalid_PeerLatency_Negative",
		ModifyConfig:  func(c *Config) { c.PeerLatency = -1 * time.Second },
		ExpectError:   true,
		ErrorContains: "PeerLatency must be greater than 0",
	},
	{
		Name:          "Invalid_ReceiverLatency_Negative",
		ModifyConfig:  func(c *Config) { c.ReceiverLatency = -1 * time.Second },
		ExpectError:   true,
		ErrorContains: "ReceiverLatency must be greater than 0",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Tick/NAK/ACK Intervals
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Invalid_TickIntervalMs_Zero",
		ModifyConfig:  func(c *Config) { c.TickIntervalMs = 0 },
		ExpectError:   true,
		ErrorContains: "TickIntervalMs must be > 0",
	},
	{
		Name:          "Invalid_PeriodicNakIntervalMs_Zero",
		ModifyConfig:  func(c *Config) { c.PeriodicNakIntervalMs = 0 },
		ExpectError:   true,
		ErrorContains: "PeriodicNakIntervalMs must be > 0",
	},
	{
		Name:          "Invalid_PeriodicAckIntervalMs_Zero",
		ModifyConfig:  func(c *Config) { c.PeriodicAckIntervalMs = 0 },
		ExpectError:   true,
		ErrorContains: "PeriodicAckIntervalMs must be > 0",
	},
	{
		Name:          "Invalid_NakRecentPercent_Negative",
		ModifyConfig:  func(c *Config) { c.NakRecentPercent = -0.1 },
		ExpectError:   true,
		ErrorContains: "NakRecentPercent must be between 0.0 and 1.0",
	},
	{
		Name:          "Invalid_NakRecentPercent_TooHigh",
		ModifyConfig:  func(c *Config) { c.NakRecentPercent = 1.5 },
		ExpectError:   true,
		ErrorContains: "NakRecentPercent must be between 0.0 and 1.0",
	},

	// ═══════════════════════════════════════════════════════════════════════
	// LightACK
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:          "Invalid_LightACKDifference_TooHigh",
		ModifyConfig:  func(c *Config) { c.LightACKDifference = 6000 },
		ExpectError:   true,
		ErrorContains: "LightACKDifference must be <= 5000",
	},
}

// TestConfig_Validation_Table runs all config validation table tests
func TestConfig_Validation_Table(t *testing.T) {
	for _, tc := range configErrorTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			runConfigValidationTest(t, tc)
		})
	}
}

func runConfigValidationTest(t *testing.T, tc ConfigErrorTestCase) {
	t.Helper()

	config := DefaultConfig()
	tc.ModifyConfig(&config)
	err := config.Validate()

	if tc.ExpectError {
		require.Error(t, err, "Expected validation error for %s", tc.Name)
		if tc.ErrorContains != "" {
			require.Contains(t, err.Error(), tc.ErrorContains,
				"Error message should contain expected text")
		}
		t.Logf("✅ %s: validation failed as expected: %v", tc.Name, err)
	} else {
		require.NoError(t, err, "Expected no validation error for %s", tc.Name)
		t.Logf("✅ %s: validation passed", tc.Name)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Dial Error Tests
// ═══════════════════════════════════════════════════════════════════════════

type DialErrorTestCase struct {
	Name          string
	Network       string
	Address       string
	ModifyConfig  func(c *Config)
	ExpectError   bool
	ErrorContains string
}

var dialErrorTests = []DialErrorTestCase{
	{
		Name:          "Invalid_Network_Type",
		Network:       "tcp", // Must be "srt"
		Address:       "127.0.0.1:6000",
		ExpectError:   true,
		ErrorContains: "network must be 'srt'",
	},
	{
		Name:          "Invalid_Address_Empty",
		Network:       "srt",
		Address:       "",
		ExpectError:   true,
		ErrorContains: "missing", // "missing port in address"
	},
	{
		Name:          "Invalid_Address_NoPort",
		Network:       "srt",
		Address:       "127.0.0.1",
		ExpectError:   true,
		ErrorContains: "missing", // "missing port in address"
	},
	{
		Name:          "Invalid_Address_BadPort",
		Network:       "srt",
		Address:       "127.0.0.1:notaport",
		ExpectError:   true,
		ErrorContains: "unknown port", // Error from net.Dial
	},
	{
		Name:          "Unreachable_Address",
		Network:       "srt",
		Address:       "192.0.2.1:6000", // TEST-NET-1, non-routable
		ModifyConfig:  func(c *Config) { c.ConnectionTimeout = 100 * time.Millisecond },
		ExpectError:   true,
		ErrorContains: "", // Could be various errors
	},
}

func TestDial_Error_Table(t *testing.T) {
	for _, tc := range dialErrorTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			runDialErrorTest(t, tc)
		})
	}
}

func runDialErrorTest(t *testing.T, tc DialErrorTestCase) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	config := DefaultConfig()
	config.StreamId = "test-error"
	if tc.ModifyConfig != nil {
		tc.ModifyConfig(&config)
	}

	conn, err := Dial(ctx, tc.Network, tc.Address, config, &wg)

	if tc.ExpectError {
		require.Error(t, err, "Expected dial error for %s", tc.Name)
		if tc.ErrorContains != "" {
			require.Contains(t, err.Error(), tc.ErrorContains,
				"Error message should contain expected text")
		}
		t.Logf("✅ %s: dial failed as expected: %v", tc.Name, err)
	} else {
		require.NoError(t, err)
		if conn != nil {
			require.NoError(t, conn.Close())
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Listen Error Tests
// ═══════════════════════════════════════════════════════════════════════════

type ListenErrorTestCase struct {
	Name          string
	Address       string
	ModifyConfig  func(c *Config)
	ExpectError   bool
	ErrorContains string
}

var listenErrorTests = []ListenErrorTestCase{
	// Note: Empty address binds to all interfaces on a random port, which is valid
	{
		Name:    "Invalid_Config_ConnectionTimeout",
		Address: "127.0.0.1:6500",
		ModifyConfig: func(c *Config) {
			c.ConnectionTimeout = 0 // Invalid
		},
		ExpectError:   true,
		ErrorContains: "config",
	},
	{
		Name:    "Invalid_Config_MSS_TooSmall",
		Address: "127.0.0.1:6501",
		ModifyConfig: func(c *Config) {
			c.MSS = 10 // Too small
		},
		ExpectError:   true,
		ErrorContains: "MSS must be between",
	},
}

func TestListen_Error_Table(t *testing.T) {
	for _, tc := range listenErrorTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			runListenErrorTest(t, tc)
		})
	}
}

func runListenErrorTest(t *testing.T, tc ListenErrorTestCase) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	config := DefaultConfig()
	if tc.ModifyConfig != nil {
		tc.ModifyConfig(&config)
	}

	ln, err := Listen(ctx, "srt", tc.Address, config, &wg)

	if tc.ExpectError {
		require.Error(t, err, "Expected listen error for %s", tc.Name)
		if tc.ErrorContains != "" {
			require.Contains(t, err.Error(), tc.ErrorContains,
				"Error message should contain expected text")
		}
		t.Logf("✅ %s: listen failed as expected: %v", tc.Name, err)
	} else {
		require.NoError(t, err)
		if ln != nil {
			ln.Close()
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Connection Read/Write Error Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestConnection_WriteAfterClose(t *testing.T) {
	// Setup a simple server
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	serverConfig := DefaultConfig()

	server := Server{
		Addr:    "127.0.0.1:6600",
		Config:  &serverConfig,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			return PUBLISH
		},
		HandlePublish: func(conn Conn) {
			<-ctx.Done()
		},
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		_ = server.Serve()
	}()

	time.Sleep(50 * time.Millisecond)

	// Connect client
	clientConfig := DefaultConfig()
	clientConfig.StreamId = "test-write-after-close"

	conn, err := Dial(ctx, "srt", "127.0.0.1:6600", clientConfig, &wg)
	require.NoError(t, err)

	// Close connection
	err = conn.Close()
	require.NoError(t, err)

	// Try to write after close - should error
	_, err = conn.Write([]byte("test data"))
	require.Error(t, err, "Write after close should fail")
	t.Logf("✅ Write after close failed as expected: %v", err)
}

func TestConnection_ReadAfterClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	serverConfig := DefaultConfig()

	server := Server{
		Addr:    "127.0.0.1:6601",
		Config:  &serverConfig,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			return SUBSCRIBE
		},
		HandleSubscribe: func(conn Conn) {
			<-ctx.Done()
		},
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		_ = server.Serve()
	}()

	time.Sleep(50 * time.Millisecond)

	clientConfig := DefaultConfig()
	clientConfig.StreamId = "test-read-after-close"

	conn, err := Dial(ctx, "srt", "127.0.0.1:6601", clientConfig, &wg)
	require.NoError(t, err)

	err = conn.Close()
	require.NoError(t, err)

	// Try to read after close - should error
	buf := make([]byte, 1024)
	_, err = conn.Read(buf)
	require.Error(t, err, "Read after close should fail")
	t.Logf("✅ Read after close failed as expected: %v", err)
}
