package srt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Config.ApplyAutoConfiguration Tests - 0% coverage
// Tests automatic configuration propagation
// ═══════════════════════════════════════════════════════════════════════════

type AutoConfigTestCase struct {
	Name string

	// Input config
	IoUringRecvEnabled   bool
	UseNakBtree          bool
	SuppressImmediateNak bool
	FastNakEnabled       bool
	FastNakRecentEnabled bool

	// Expected output
	ExpectedUseNakBtree          bool
	ExpectedSuppressImmediateNak bool
	ExpectedFastNakEnabled       bool
	ExpectedFastNakRecentEnabled bool
}

var autoConfigTests = []AutoConfigTestCase{
	{
		Name:                         "IoUring_Enables_NakBtree",
		IoUringRecvEnabled:           true,
		UseNakBtree:                  false,
		SuppressImmediateNak:         false,
		FastNakEnabled:               false,
		FastNakRecentEnabled:         false,
		ExpectedUseNakBtree:          true,
		ExpectedSuppressImmediateNak: true,
		ExpectedFastNakEnabled:       true,
		ExpectedFastNakRecentEnabled: true,
	},
	{
		Name:                         "NakBtree_Enables_FastNak",
		IoUringRecvEnabled:           false,
		UseNakBtree:                  true,
		SuppressImmediateNak:         false,
		FastNakEnabled:               false,
		FastNakRecentEnabled:         false,
		ExpectedUseNakBtree:          true,
		ExpectedSuppressImmediateNak: false,
		ExpectedFastNakEnabled:       true,
		ExpectedFastNakRecentEnabled: true,
	},
	{
		Name:                         "NoIoUring_NoAutoConfig",
		IoUringRecvEnabled:           false,
		UseNakBtree:                  false,
		SuppressImmediateNak:         false,
		FastNakEnabled:               false,
		FastNakRecentEnabled:         false,
		ExpectedUseNakBtree:          false,
		ExpectedSuppressImmediateNak: false,
		ExpectedFastNakEnabled:       false,
		ExpectedFastNakRecentEnabled: false,
	},
	{
		Name:                         "PresetFastNak_Preserved",
		IoUringRecvEnabled:           false,
		UseNakBtree:                  true,
		SuppressImmediateNak:         false,
		FastNakEnabled:               true, // Already set
		FastNakRecentEnabled:         true, // Already set
		ExpectedUseNakBtree:          true,
		ExpectedSuppressImmediateNak: false,
		ExpectedFastNakEnabled:       true,
		ExpectedFastNakRecentEnabled: true,
	},
	{
		Name:                         "IoUring_WithPresets",
		IoUringRecvEnabled:           true,
		UseNakBtree:                  true, // Already set
		SuppressImmediateNak:         true, // Already set
		FastNakEnabled:               true, // Already set
		FastNakRecentEnabled:         true, // Already set
		ExpectedUseNakBtree:          true,
		ExpectedSuppressImmediateNak: true,
		ExpectedFastNakEnabled:       true,
		ExpectedFastNakRecentEnabled: true,
	},
}

func TestConfig_ApplyAutoConfiguration_Table(t *testing.T) {
	for _, tc := range autoConfigTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			cfg := DefaultConfig()
			cfg.IoUringRecvEnabled = tc.IoUringRecvEnabled
			cfg.UseNakBtree = tc.UseNakBtree
			cfg.SuppressImmediateNak = tc.SuppressImmediateNak
			cfg.FastNakEnabled = tc.FastNakEnabled
			cfg.FastNakRecentEnabled = tc.FastNakRecentEnabled

			cfg.ApplyAutoConfiguration()

			require.Equal(t, tc.ExpectedUseNakBtree, cfg.UseNakBtree,
				"UseNakBtree mismatch")
			require.Equal(t, tc.ExpectedSuppressImmediateNak, cfg.SuppressImmediateNak,
				"SuppressImmediateNak mismatch")
			require.Equal(t, tc.ExpectedFastNakEnabled, cfg.FastNakEnabled,
				"FastNakEnabled mismatch")
			require.Equal(t, tc.ExpectedFastNakRecentEnabled, cfg.FastNakRecentEnabled,
				"FastNakRecentEnabled mismatch")

			t.Logf("✅ %s: auto-configuration applied correctly", tc.Name)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Config.UnmarshalURL Tests - 66.7% coverage, add more cases
// ═══════════════════════════════════════════════════════════════════════════

type UnmarshalURLTestCase struct {
	Name         string
	URL          string
	ExpectError  bool
	ExpectedHost string
}

var unmarshalURLTests = []UnmarshalURLTestCase{
	{
		Name:         "Valid_Basic",
		URL:          "srt://127.0.0.1:6000",
		ExpectError:  false,
		ExpectedHost: "127.0.0.1:6000",
	},
	{
		Name:         "Valid_WithQuery",
		URL:          "srt://127.0.0.1:6000?latency=200",
		ExpectError:  false,
		ExpectedHost: "127.0.0.1:6000",
	},
	{
		Name:         "Valid_WithStreamId",
		URL:          "srt://127.0.0.1:6000?streamid=mystream",
		ExpectError:  false,
		ExpectedHost: "127.0.0.1:6000",
	},
	{
		Name:         "Valid_WithPassphrase",
		URL:          "srt://127.0.0.1:6000?passphrase=secret1234",
		ExpectError:  false,
		ExpectedHost: "127.0.0.1:6000",
	},
	{
		Name:         "Valid_MultipleParams",
		URL:          "srt://127.0.0.1:6000?latency=200&streamid=test&passphrase=secret1234",
		ExpectError:  false,
		ExpectedHost: "127.0.0.1:6000",
	},
	{
		Name:        "Invalid_WrongScheme",
		URL:         "http://127.0.0.1:6000",
		ExpectError: true,
	},
	{
		Name:        "Invalid_NoScheme",
		URL:         "127.0.0.1:6000",
		ExpectError: true,
	},
	{
		Name:        "Invalid_MalformedURL",
		URL:         "srt://[invalid",
		ExpectError: true,
	},
	// Corner cases
	{
		Name:         "Corner_IPv6",
		URL:          "srt://[::1]:6000",
		ExpectError:  false,
		ExpectedHost: "[::1]:6000",
	},
	{
		Name:         "Corner_Hostname",
		URL:          "srt://localhost:6000",
		ExpectError:  false,
		ExpectedHost: "localhost:6000",
	},
}

func TestConfig_UnmarshalURL_Table(t *testing.T) {
	for _, tc := range unmarshalURLTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			cfg := DefaultConfig()
			host, err := cfg.UnmarshalURL(tc.URL)

			if tc.ExpectError {
				require.Error(t, err, "Expected error for %s", tc.Name)
				t.Logf("✅ %s: rejected as expected", tc.Name)
				return
			}

			require.NoError(t, err, "Expected success for %s", tc.Name)
			require.Equal(t, tc.ExpectedHost, host, "Host mismatch")
			t.Logf("✅ %s: parsed host=%s", tc.Name, host)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Config Default Value Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestConfig_DefaultValues(t *testing.T) {
	cfg := DefaultConfig()

	// Check critical defaults
	// Note: Latency defaults to -1 (meaning use peer/receiver latency)
	// PeerLatency and ReceiverLatency default to 120ms
	require.Equal(t, time.Duration(-1), cfg.Latency, "Default latency should be -1 (use peer/receiver)")
	require.Equal(t, 120*time.Millisecond, cfg.PeerLatency, "Default peer latency")
	require.Equal(t, 120*time.Millisecond, cfg.ReceiverLatency, "Default receiver latency")
	require.Equal(t, uint32(MAX_PAYLOAD_SIZE), cfg.PayloadSize, "Default payload size")
	require.Equal(t, uint32(MAX_MSS_SIZE), cfg.MSS, "Default MSS")
	require.Equal(t, uint32(25600), cfg.FC, "Default flow control")
	require.True(t, cfg.NAKReport, "NAKReport should be true by default")
	require.Equal(t, 3*time.Second, cfg.ConnectionTimeout, "Default connection timeout")
	require.Equal(t, 2*time.Second, cfg.PeerIdleTimeout, "Default peer idle timeout")

	t.Log("✅ All default config values are correct")
}

// ═══════════════════════════════════════════════════════════════════════════
// Config.MarshalURL Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestConfig_MarshalURL_RoundTrip(t *testing.T) {
	testCases := []struct {
		Name       string
		ModifyFunc func(*Config)
		CheckFunc  func(*testing.T, *Config)
	}{
		{
			Name: "Default",
			ModifyFunc: func(c *Config) {
				// Use defaults
			},
			CheckFunc: func(t *testing.T, c *Config) {
				// Latency defaults to -1 (use peer/receiver latency)
				require.Equal(t, time.Duration(-1), c.Latency)
			},
		},
		{
			Name: "WithStreamId",
			ModifyFunc: func(c *Config) {
				c.StreamId = "test-stream"
			},
			CheckFunc: func(t *testing.T, c *Config) {
				require.Equal(t, "test-stream", c.StreamId)
			},
		},
		{
			Name: "WithLatency",
			ModifyFunc: func(c *Config) {
				c.Latency = 500 * time.Millisecond
			},
			CheckFunc: func(t *testing.T, c *Config) {
				require.Equal(t, 500*time.Millisecond, c.Latency)
			},
		},
		{
			Name: "WithPassphrase",
			ModifyFunc: func(c *Config) {
				c.Passphrase = "secret123456"
			},
			CheckFunc: func(t *testing.T, c *Config) {
				require.Equal(t, "secret123456", c.Passphrase)
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			// Create and modify config
			cfg1 := DefaultConfig()
			tc.ModifyFunc(&cfg1)

			// Marshal to URL
			url := cfg1.MarshalURL("127.0.0.1:6000")
			require.Contains(t, url, "srt://127.0.0.1:6000")

			// Unmarshal back
			cfg2 := DefaultConfig()
			host, err := cfg2.UnmarshalURL(url)
			require.NoError(t, err)
			require.Equal(t, "127.0.0.1:6000", host)

			// Check values preserved
			tc.CheckFunc(t, &cfg2)
			t.Logf("✅ %s: round-trip successful, URL=%s", tc.Name, url)
		})
	}
}
