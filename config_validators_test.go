package srt

import (
	"strings"
	"testing"
	"time"
)

// TestConfigValidators_Individual tests each validator in isolation.
func TestConfigValidators_Individual(t *testing.T) {
	tests := []struct {
		name      string
		validator func(*Config) error
		config    Config
		wantErr   bool
		errSubstr string
	}{
		// ════════════════════════════════════════════════════════════════════════
		// TransmissionType
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "TransmissionType_live_valid",
			validator: validateTransmissionType,
			config:    Config{TransmissionType: "live"},
			wantErr:   false,
		},
		{
			name:      "TransmissionType_file_invalid",
			validator: validateTransmissionType,
			config:    Config{TransmissionType: "file"},
			wantErr:   true,
			errSubstr: "TransmissionType must be 'live'",
		},

		// ════════════════════════════════════════════════════════════════════════
		// MSS
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "MSS_valid",
			validator: validateMSS,
			config:    Config{MSS: 1500},
			wantErr:   false,
		},
		{
			name:      "MSS_min_boundary",
			validator: validateMSS,
			config:    Config{MSS: MIN_MSS_SIZE},
			wantErr:   false,
		},
		{
			name:      "MSS_max_boundary",
			validator: validateMSS,
			config:    Config{MSS: MAX_MSS_SIZE},
			wantErr:   false,
		},
		{
			name:      "MSS_below_min",
			validator: validateMSS,
			config:    Config{MSS: MIN_MSS_SIZE - 1},
			wantErr:   true,
			errSubstr: "MSS must be between",
		},
		{
			name:      "MSS_above_max",
			validator: validateMSS,
			config:    Config{MSS: MAX_MSS_SIZE + 1},
			wantErr:   true,
			errSubstr: "MSS must be between",
		},

		// ════════════════════════════════════════════════════════════════════════
		// ConnectionTimeout
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "ConnectionTimeout_positive",
			validator: validateConnectionTimeout,
			config:    Config{ConnectionTimeout: time.Second},
			wantErr:   false,
		},
		{
			name:      "ConnectionTimeout_zero",
			validator: validateConnectionTimeout,
			config:    Config{ConnectionTimeout: 0},
			wantErr:   true,
			errSubstr: "ConnectionTimeout must be greater than 0",
		},
		{
			name:      "ConnectionTimeout_negative",
			validator: validateConnectionTimeout,
			config:    Config{ConnectionTimeout: -1},
			wantErr:   true,
			errSubstr: "ConnectionTimeout must be greater than 0",
		},

		// ════════════════════════════════════════════════════════════════════════
		// IPTOS
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "IPTOS_valid",
			validator: validateIPTOS,
			config:    Config{IPTOS: 128},
			wantErr:   false,
		},
		{
			name:      "IPTOS_zero",
			validator: validateIPTOS,
			config:    Config{IPTOS: 0},
			wantErr:   false, // 0 is allowed
		},
		{
			name:      "IPTOS_max_valid",
			validator: validateIPTOS,
			config:    Config{IPTOS: 255},
			wantErr:   false,
		},
		{
			name:      "IPTOS_above_255",
			validator: validateIPTOS,
			config:    Config{IPTOS: 256},
			wantErr:   true,
			errSubstr: "IPTOS must be lower than 255",
		},

		// ════════════════════════════════════════════════════════════════════════
		// PBKeylen
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "PBKeylen_16",
			validator: validatePBKeylen,
			config:    Config{PBKeylen: 16},
			wantErr:   false,
		},
		{
			name:      "PBKeylen_24",
			validator: validatePBKeylen,
			config:    Config{PBKeylen: 24},
			wantErr:   false,
		},
		{
			name:      "PBKeylen_32",
			validator: validatePBKeylen,
			config:    Config{PBKeylen: 32},
			wantErr:   false,
		},
		{
			name:      "PBKeylen_invalid",
			validator: validatePBKeylen,
			config:    Config{PBKeylen: 20},
			wantErr:   true,
			errSubstr: "PBKeylen must be 16, 24, or 32",
		},

		// ════════════════════════════════════════════════════════════════════════
		// OverheadBW
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "OverheadBW_valid",
			validator: validateOverheadBW,
			config:    Config{OverheadBW: 25},
			wantErr:   false,
		},
		{
			name:      "OverheadBW_min",
			validator: validateOverheadBW,
			config:    Config{OverheadBW: 10},
			wantErr:   false,
		},
		{
			name:      "OverheadBW_max",
			validator: validateOverheadBW,
			config:    Config{OverheadBW: 100},
			wantErr:   false,
		},
		{
			name:      "OverheadBW_below_min",
			validator: validateOverheadBW,
			config:    Config{OverheadBW: 9},
			wantErr:   true,
			errSubstr: "OverheadBW must be between 10 and 100",
		},
		{
			name:      "OverheadBW_above_max",
			validator: validateOverheadBW,
			config:    Config{OverheadBW: 101},
			wantErr:   true,
			errSubstr: "OverheadBW must be between 10 and 100",
		},

		// ════════════════════════════════════════════════════════════════════════
		// NakRecentPercent
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "NakRecentPercent_valid",
			validator: validateNakRecentPercent,
			config:    Config{NakRecentPercent: 0.10},
			wantErr:   false,
		},
		{
			name:      "NakRecentPercent_zero",
			validator: validateNakRecentPercent,
			config:    Config{NakRecentPercent: 0.0},
			wantErr:   false,
		},
		{
			name:      "NakRecentPercent_max",
			validator: validateNakRecentPercent,
			config:    Config{NakRecentPercent: 1.0},
			wantErr:   false,
		},
		{
			name:      "NakRecentPercent_negative",
			validator: validateNakRecentPercent,
			config:    Config{NakRecentPercent: -0.1},
			wantErr:   true,
			errSubstr: "NakRecentPercent must be between 0.0 and 1.0",
		},
		{
			name:      "NakRecentPercent_above_max",
			validator: validateNakRecentPercent,
			config:    Config{NakRecentPercent: 1.1},
			wantErr:   true,
			errSubstr: "NakRecentPercent must be between 0.0 and 1.0",
		},

		// ════════════════════════════════════════════════════════════════════════
		// GroupConnect
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "GroupConnect_false",
			validator: validateGroupConnect,
			config:    Config{GroupConnect: false},
			wantErr:   false,
		},
		{
			name:      "GroupConnect_true_unsupported",
			validator: validateGroupConnect,
			config:    Config{GroupConnect: true},
			wantErr:   true,
			errSubstr: "GroupConnect is not supported",
		},

		// ════════════════════════════════════════════════════════════════════════
		// PacketRing Configuration
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "PacketRing_disabled_no_validation",
			validator: validatePacketRingConfig,
			config:    Config{UsePacketRing: false},
			wantErr:   false,
		},
		{
			name:      "PacketRing_enabled_valid",
			validator: validatePacketRingConfig,
			config: Config{
				UsePacketRing:             true,
				PacketRingSize:            1024,
				PacketRingShards:          4,
				PacketRingMaxRetries:      10,
				PacketRingBackoffDuration: time.Millisecond,
				PacketRingMaxBackoffs:     5,
			},
			wantErr: false,
		},
		{
			name:      "PacketRing_size_not_power_of_2",
			validator: validatePacketRingConfig,
			config: Config{
				UsePacketRing:    true,
				PacketRingSize:   1000, // Not power of 2
				PacketRingShards: 4,
			},
			wantErr:   true,
			errSubstr: "PacketRingSize must be a power of 2",
		},
		{
			name:      "PacketRing_shards_not_power_of_2",
			validator: validatePacketRingConfig,
			config: Config{
				UsePacketRing:    true,
				PacketRingSize:   1024,
				PacketRingShards: 3, // Not power of 2
			},
			wantErr:   true,
			errSubstr: "PacketRingShards must be a power of 2",
		},

		// ════════════════════════════════════════════════════════════════════════
		// EventLoop Configuration
		// ════════════════════════════════════════════════════════════════════════
		{
			name:      "EventLoop_disabled_no_validation",
			validator: validateEventLoopConfig,
			config:    Config{UseEventLoop: false},
			wantErr:   false,
		},
		{
			name:      "EventLoop_without_PacketRing",
			validator: validateEventLoopConfig,
			config:    Config{UseEventLoop: true, UsePacketRing: false},
			wantErr:   true,
			errSubstr: "UseEventLoop requires UsePacketRing=true",
		},
		{
			name:      "EventLoop_valid",
			validator: validateEventLoopConfig,
			config: Config{
				UseEventLoop:          true,
				UsePacketRing:         true,
				EventLoopRateInterval: time.Second,
				BackoffMinSleep:       time.Microsecond,
				BackoffMaxSleep:       time.Millisecond,
			},
			wantErr: false,
		},
		{
			name:      "EventLoop_min_greater_than_max",
			validator: validateEventLoopConfig,
			config: Config{
				UseEventLoop:          true,
				UsePacketRing:         true,
				EventLoopRateInterval: time.Second,
				BackoffMinSleep:       time.Second,
				BackoffMaxSleep:       time.Millisecond,
			},
			wantErr:   true,
			errSubstr: "BackoffMinSleep",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.validator(&tc.config)
			if (err != nil) != tc.wantErr {
				t.Errorf("got error %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && tc.errSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("error message should contain %q, got %v", tc.errSubstr, err)
				}
			}
		})
	}
}

// TestConfigValidatorCount verifies we have a reasonable number of validators.
func TestConfigValidatorCount(t *testing.T) {
	count := GetConfigValidatorCount()
	if count < 30 {
		t.Errorf("expected at least 30 config validators, got %d", count)
	}
}

// TestValidate_DefaultConfig verifies DefaultConfig passes validation.
func TestValidate_DefaultConfig(t *testing.T) {
	config := DefaultConfig()
	err := config.Validate()
	if err != nil {
		t.Errorf("DefaultConfig should pass validation, got: %v", err)
	}
}

// TestValidate_RequiredSettings verifies required settings are applied.
func TestValidate_RequiredSettings(t *testing.T) {
	config := DefaultConfig()
	config.Congestion = ""   // Will be overwritten
	config.NAKReport = false // Will be overwritten
	config.TSBPDMode = false // Will be overwritten

	err := config.Validate()
	if err != nil {
		t.Errorf("Validate should apply required settings, got: %v", err)
	}

	// Verify settings were applied
	if config.Congestion != "live" {
		t.Error("Congestion should be set to 'live'")
	}
	if !config.NAKReport {
		t.Error("NAKReport should be enabled")
	}
	if !config.TSBPDMode {
		t.Error("TSBPDMode should be enabled")
	}
}

// TestValidate_LatencyInheritance verifies latency values are inherited.
func TestValidate_LatencyInheritance(t *testing.T) {
	config := DefaultConfig()
	config.Latency = 500 * time.Millisecond

	err := config.Validate()
	if err != nil {
		t.Errorf("Validate failed: %v", err)
	}

	if config.PeerLatency != config.Latency {
		t.Errorf("PeerLatency should inherit from Latency, got %v", config.PeerLatency)
	}
	if config.ReceiverLatency != config.Latency {
		t.Errorf("ReceiverLatency should inherit from Latency, got %v", config.ReceiverLatency)
	}
}

// BenchmarkValidate_DefaultConfig benchmarks validation of default config.
func BenchmarkValidate_DefaultConfig(b *testing.B) {
	config := DefaultConfig()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = config.Validate()
	}
}

// BenchmarkValidators_Individual benchmarks individual validators.
func BenchmarkValidators_Individual(b *testing.B) {
	config := DefaultConfig()

	b.Run("MSS", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = validateMSS(&config)
		}
	})

	b.Run("ConnectionTimeout", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = validateConnectionTimeout(&config)
		}
	})

	b.Run("PacketRingConfig", func(b *testing.B) {
		config.UsePacketRing = true
		config.PacketRingSize = 1024
		config.PacketRingShards = 4
		for i := 0; i < b.N; i++ {
			_ = validatePacketRingConfig(&config)
		}
	})
}
