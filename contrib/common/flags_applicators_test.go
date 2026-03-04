package common

import (
	"testing"
	"time"

	srt "github.com/randomizedcoder/gosrt"
)

// TestApplyFlagsToConfig_TableDriven tests individual flag application.
// Each test case verifies that setting a specific flag results in the
// correct config field being updated.
func TestApplyFlagsToConfig_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		flagName string
		setup    func()
		check    func(*srt.Config) bool
	}{
		// Connection configuration flags
		{
			name:     "congestion",
			flagName: "congestion",
			setup:    func() { *Congestion = "live" },
			check:    func(c *srt.Config) bool { return c.Congestion == "live" },
		},
		{
			name:     "conntimeo",
			flagName: "conntimeo",
			setup:    func() { *Conntimeo = 5000 },
			check:    func(c *srt.Config) bool { return c.ConnectionTimeout == 5000*time.Millisecond },
		},
		{
			name:     "streamid",
			flagName: "streamid",
			setup:    func() { *Streamid = "test-stream" },
			check:    func(c *srt.Config) bool { return c.StreamId == "test-stream" },
		},
		{
			name:     "passphrase-flag",
			flagName: "passphrase-flag",
			setup:    func() { *PassphraseFlag = "secret123" },
			check:    func(c *srt.Config) bool { return c.Passphrase == "secret123" },
		},
		{
			name:     "pbkeylen",
			flagName: "pbkeylen",
			setup:    func() { *PBKeylen = 32 },
			check:    func(c *srt.Config) bool { return c.PBKeylen == 32 },
		},
		{
			name:     "latency",
			flagName: "latency",
			setup:    func() { *Latency = 200 },
			check:    func(c *srt.Config) bool { return c.Latency == 200*time.Millisecond },
		},
		{
			name:     "fc",
			flagName: "fc",
			setup:    func() { *FC = 102400 },
			check:    func(c *srt.Config) bool { return c.FC == 102400 },
		},
		{
			name:     "mss",
			flagName: "mss",
			setup:    func() { *MSS = 1500 },
			check:    func(c *srt.Config) bool { return c.MSS == 1500 },
		},

		// io_uring configuration flags
		{
			name:     "iouringenabled",
			flagName: "iouringenabled",
			setup:    func() { *IoUringEnabled = true },
			check:    func(c *srt.Config) bool { return c.IoUringEnabled },
		},
		{
			name:     "iouringrecvenabled",
			flagName: "iouringrecvenabled",
			setup:    func() { *IoUringRecvEnabled = true },
			check:    func(c *srt.Config) bool { return c.IoUringRecvEnabled },
		},
		{
			name:     "iouringrecvringsize",
			flagName: "iouringrecvringsize",
			setup:    func() { *IoUringRecvRingSize = 256 },
			check:    func(c *srt.Config) bool { return c.IoUringRecvRingSize == 256 },
		},

		// Timer interval flags
		{
			name:     "tickintervalms",
			flagName: "tickintervalms",
			setup:    func() { *TickIntervalMs = 5 },
			check:    func(c *srt.Config) bool { return c.TickIntervalMs == 5 },
		},
		{
			name:     "periodicnakintervalms",
			flagName: "periodicnakintervalms",
			setup:    func() { *PeriodicNakIntervalMs = 30 },
			check:    func(c *srt.Config) bool { return c.PeriodicNakIntervalMs == 30 },
		},

		// NAK btree flags
		{
			name:     "usenakbtree",
			flagName: "usenakbtree",
			setup:    func() { *UseNakBtree = true },
			check:    func(c *srt.Config) bool { return c.UseNakBtree },
		},
		{
			name:     "nakrecentpercent",
			flagName: "nakrecentpercent",
			setup:    func() { *NakRecentPercent = 0.15 },
			check:    func(c *srt.Config) bool { return c.NakRecentPercent == 0.15 },
		},

		// Sender lockless flags
		{
			name:     "usesendbtree",
			flagName: "usesendbtree",
			setup:    func() { *UseSendBtree = true },
			check:    func(c *srt.Config) bool { return c.UseSendBtree },
		},
		{
			name:     "usesendring",
			flagName: "usesendring",
			setup:    func() { *UseSendRing = true },
			check:    func(c *srt.Config) bool { return c.UseSendRing },
		},
		{
			name:     "usesendeventloop",
			flagName: "usesendeventloop",
			setup:    func() { *UseSendEventLoop = true },
			check:    func(c *srt.Config) bool { return c.UseSendEventLoop },
		},

		// Event loop flags
		{
			name:     "useeventloop",
			flagName: "useeventloop",
			setup:    func() { *UseEventLoop = true },
			check:    func(c *srt.Config) bool { return c.UseEventLoop },
		},
		{
			name:     "usepacketring",
			flagName: "usepacketring",
			setup:    func() { *UsePacketRing = true },
			check:    func(c *srt.Config) bool { return c.UsePacketRing },
		},

		// Debug flags
		{
			name:     "receiverdebug",
			flagName: "receiverdebug",
			setup:    func() { *ReceiverDebug = true },
			check:    func(c *srt.Config) bool { return c.ReceiverDebug },
		},

		// Timeout and shutdown flags
		{
			name:     "handshaketimeout",
			flagName: "handshaketimeout",
			setup:    func() { *HandshakeTimeout = 2 * time.Second },
			check:    func(c *srt.Config) bool { return c.HandshakeTimeout == 2*time.Second },
		},
		{
			name:     "shutdowndelay",
			flagName: "shutdowndelay",
			setup:    func() { *ShutdownDelay = 5 * time.Second },
			check:    func(c *srt.Config) bool { return c.ShutdownDelay == 5*time.Second },
		},

		// Instance name
		{
			name:     "name",
			flagName: "name",
			setup:    func() { *InstanceName = "TestInstance" },
			check:    func(c *srt.Config) bool { return c.InstanceName == "TestInstance" },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset state
			ResetFlagSet()
			config := srt.DefaultConfig()

			// Setup flag value and mark as set
			tc.setup()
			FlagSet[tc.flagName] = true

			// Apply flags
			ApplyFlagsToConfig(&config)

			// Check result
			if !tc.check(&config) {
				t.Errorf("flag %s not applied correctly", tc.flagName)
			}
		})
	}
}

// TestApplyFlagsToConfig_NoFlagsSet verifies that when no flags are set,
// the config remains at its default values.
func TestApplyFlagsToConfig_NoFlagsSet(t *testing.T) {
	ResetFlagSet()
	defaultConfig := srt.DefaultConfig()
	config := srt.DefaultConfig()

	ApplyFlagsToConfig(&config)

	// Key fields should remain at defaults
	if config.Latency != defaultConfig.Latency {
		t.Errorf("Latency changed unexpectedly: got %v, want %v", config.Latency, defaultConfig.Latency)
	}
	if config.FC != defaultConfig.FC {
		t.Errorf("FC changed unexpectedly: got %v, want %v", config.FC, defaultConfig.FC)
	}
	if config.MSS != defaultConfig.MSS {
		t.Errorf("MSS changed unexpectedly: got %v, want %v", config.MSS, defaultConfig.MSS)
	}
}

// TestApplyFlagsToConfig_ConditionalFlags tests flags with special conditions.
func TestApplyFlagsToConfig_ConditionalFlags(t *testing.T) {
	tests := []struct {
		name   string
		setup  func()
		check  func(*srt.Config) bool
		expect bool
	}{
		{
			name: "lightackdifference_positive",
			setup: func() {
				*LightACKDifference = 128
				FlagSet["lightackdifference"] = true
			},
			check:  func(c *srt.Config) bool { return c.LightACKDifference == 128 },
			expect: true,
		},
		{
			name: "lightackdifference_zero_not_applied",
			setup: func() {
				*LightACKDifference = 0
				FlagSet["lightackdifference"] = true
			},
			check:  func(c *srt.Config) bool { return c.LightACKDifference == 0 },
			expect: false, // Should NOT be applied when <= 0
		},
		{
			name: "packetringmaxretries_positive",
			setup: func() {
				*PacketRingMaxRetries = 5
				FlagSet["packetringmaxretries"] = true
			},
			check:  func(c *srt.Config) bool { return c.PacketRingMaxRetries == 5 },
			expect: true,
		},
		{
			name: "packetringmaxretries_negative_not_applied",
			setup: func() {
				*PacketRingMaxRetries = -1
				FlagSet["packetringmaxretries"] = true
			},
			check:  func(c *srt.Config) bool { return c.PacketRingMaxRetries == -1 },
			expect: false, // Should NOT be applied when < 0
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ResetFlagSet()
			config := srt.DefaultConfig()

			tc.setup()
			ApplyFlagsToConfig(&config)

			result := tc.check(&config)
			if result != tc.expect {
				t.Errorf("expected check to return %v, got %v", tc.expect, result)
			}
		})
	}
}

// TestFlagApplicatorCount verifies the table has expected number of entries.
// Update this if flags are added or removed.
func TestFlagApplicatorCount(t *testing.T) {
	count := GetFlagApplicatorCount()
	// This is a sanity check - update the expected count when adding flags
	if count < 80 {
		t.Errorf("expected at least 80 flag applicators, got %d", count)
	}
}

// TestConditionalFlagApplicatorCount verifies conditional applicators exist.
func TestConditionalFlagApplicatorCount(t *testing.T) {
	count := GetConditionalFlagApplicatorCount()
	if count < 5 {
		t.Errorf("expected at least 5 conditional flag applicators, got %d", count)
	}
}

// TestApplyFlagsToConfig_MultipleFlagsSet verifies multiple flags can be set together.
func TestApplyFlagsToConfig_MultipleFlagsSet(t *testing.T) {
	ResetFlagSet()
	config := srt.DefaultConfig()

	// Set multiple flags
	*Latency = 300
	FlagSet["latency"] = true

	*FC = 204800
	FlagSet["fc"] = true

	*UseEventLoop = true
	FlagSet["useeventloop"] = true

	*UsePacketRing = true
	FlagSet["usepacketring"] = true

	ApplyFlagsToConfig(&config)

	// Verify all flags were applied
	if config.Latency != 300*time.Millisecond {
		t.Errorf("Latency not set correctly: got %v, want %v", config.Latency, 300*time.Millisecond)
	}
	if config.FC != 204800 {
		t.Errorf("FC not set correctly: got %v, want %v", config.FC, 204800)
	}
	if !config.UseEventLoop {
		t.Error("UseEventLoop not set correctly")
	}
	if !config.UsePacketRing {
		t.Error("UsePacketRing not set correctly")
	}
}
