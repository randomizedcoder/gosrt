package main

import (
	"testing"
	"time"
)

// TestConfigVariantGetSRTConfig verifies that GetSRTConfig returns correct configurations
func TestConfigVariantGetSRTConfig(t *testing.T) {
	tests := []struct {
		name              string
		variant           ConfigVariant
		wantBtree         bool
		wantIoUr          bool
		wantNakBtree      bool
		wantFastNak       bool
		wantFastNakRecent bool
		wantHonorNakOrder bool
	}{
		{
			name:         "Base - list, no io_uring",
			variant:      ConfigBase,
			wantBtree:    false,
			wantIoUr:     false,
			wantNakBtree: false,
		},
		{
			name:         "Btree - btree packet store only",
			variant:      ConfigBtree,
			wantBtree:    true,
			wantIoUr:     false,
			wantNakBtree: false,
		},
		{
			name:         "IoUr - io_uring only",
			variant:      ConfigIoUr,
			wantBtree:    false,
			wantIoUr:     true,
			wantNakBtree: false,
		},
		{
			name:              "NakBtree - NAK btree only (no FastNAK)",
			variant:           ConfigNakBtree,
			wantBtree:         false,
			wantIoUr:          true, // io_uring recv is enabled with NAK btree
			wantNakBtree:      true,
			wantFastNak:       false,
			wantFastNakRecent: false,
		},
		{
			name:              "NakBtreeF - NAK btree + FastNAK",
			variant:           ConfigNakBtreeF,
			wantBtree:         false,
			wantIoUr:          true,
			wantNakBtree:      true,
			wantFastNak:       true,
			wantFastNakRecent: false,
		},
		{
			name:              "NakBtreeFr - NAK btree + FastNAK + FastNAKRecent",
			variant:           ConfigNakBtreeFr,
			wantBtree:         false,
			wantIoUr:          true,
			wantNakBtree:      true,
			wantFastNak:       true,
			wantFastNakRecent: true,
		},
		{
			name:              "Full - everything enabled",
			variant:           ConfigFull,
			wantBtree:         true,
			wantIoUr:          true,
			wantNakBtree:      true,
			wantFastNak:       true,
			wantFastNakRecent: true,
			wantHonorNakOrder: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GetSRTConfig(tt.variant)

			// Check btree
			gotBtree := cfg.PacketReorderAlgorithm == "btree"
			if gotBtree != tt.wantBtree {
				t.Errorf("btree: got %v, want %v", gotBtree, tt.wantBtree)
			}

			// Check io_uring (either send or recv)
			gotIoUr := cfg.IoUringEnabled || cfg.IoUringRecvEnabled
			if gotIoUr != tt.wantIoUr {
				t.Errorf("io_uring: got %v, want %v", gotIoUr, tt.wantIoUr)
			}

			// Check NAK btree
			if cfg.UseNakBtree != tt.wantNakBtree {
				t.Errorf("UseNakBtree: got %v, want %v", cfg.UseNakBtree, tt.wantNakBtree)
			}

			// Check FastNAK
			if cfg.FastNakEnabled != tt.wantFastNak {
				t.Errorf("FastNakEnabled: got %v, want %v", cfg.FastNakEnabled, tt.wantFastNak)
			}

			// Check FastNAKRecent
			if cfg.FastNakRecentEnabled != tt.wantFastNakRecent {
				t.Errorf("FastNakRecentEnabled: got %v, want %v", cfg.FastNakRecentEnabled, tt.wantFastNakRecent)
			}

			// Check HonorNakOrder
			if cfg.HonorNakOrder != tt.wantHonorNakOrder {
				t.Errorf("HonorNakOrder: got %v, want %v", cfg.HonorNakOrder, tt.wantHonorNakOrder)
			}
		})
	}
}

// TestRTTProfile verifies RTT profile functions
func TestRTTProfile(t *testing.T) {
	tests := []struct {
		profile  RTTProfile
		wantMs   int
		wantName string
	}{
		{RTT0, 0, "none"},
		{RTT10, 10, "regional"},
		{RTT60, 60, "continental"},
		{RTT130, 130, "intercontinental"},
		{RTT300, 300, "geo_satellite"},
	}

	for _, tt := range tests {
		t.Run(string(tt.profile), func(t *testing.T) {
			if got := GetRTTMs(tt.profile); got != tt.wantMs {
				t.Errorf("GetRTTMs(%s) = %d, want %d", tt.profile, got, tt.wantMs)
			}
			if got := GetLatencyProfile(tt.profile); got != tt.wantName {
				t.Errorf("GetLatencyProfile(%s) = %s, want %s", tt.profile, got, tt.wantName)
			}
		})
	}
}

// TestTimerProfile verifies timer profile functions
func TestTimerProfile(t *testing.T) {
	tests := []struct {
		profile  TimerProfile
		wantTick uint64
		wantNak  uint64
		wantAck  uint64
	}{
		{TimerDefault, 10, 20, 10},
		{TimerFast, 5, 10, 5},
		{TimerSlow, 20, 40, 20},
		{TimerFastNak, 10, 5, 10},
		{TimerSlowNak, 10, 50, 10},
	}

	for _, tt := range tests {
		t.Run(string(tt.profile), func(t *testing.T) {
			intervals := GetTimerIntervals(tt.profile)
			if intervals.TickMs != tt.wantTick {
				t.Errorf("TickMs: got %d, want %d", intervals.TickMs, tt.wantTick)
			}
			if intervals.NakMs != tt.wantNak {
				t.Errorf("NakMs: got %d, want %d", intervals.NakMs, tt.wantNak)
			}
			if intervals.AckMs != tt.wantAck {
				t.Errorf("AckMs: got %d, want %d", intervals.AckMs, tt.wantAck)
			}
		})
	}
}

// TestWithLatency verifies WithLatency helper
func TestWithLatency(t *testing.T) {
	cfg := ControlSRTConfig

	// Apply 5 second latency
	cfg = cfg.WithLatency(5 * time.Second)

	if cfg.Latency != 5*time.Second {
		t.Errorf("Latency: got %v, want %v", cfg.Latency, 5*time.Second)
	}
	if cfg.RecvLatency != 5*time.Second {
		t.Errorf("RecvLatency: got %v, want %v", cfg.RecvLatency, 5*time.Second)
	}
	if cfg.PeerLatency != 5*time.Second {
		t.Errorf("PeerLatency: got %v, want %v", cfg.PeerLatency, 5*time.Second)
	}
}

// TestWithTimerProfile verifies WithTimerProfile helper
func TestWithTimerProfile(t *testing.T) {
	cfg := ControlSRTConfig

	// Apply FastNak timer profile
	cfg = cfg.WithTimerProfile(TimerFastNak)

	if cfg.TickIntervalMs != 10 {
		t.Errorf("TickIntervalMs: got %d, want %d", cfg.TickIntervalMs, 10)
	}
	if cfg.PeriodicNakIntervalMs != 5 {
		t.Errorf("PeriodicNakIntervalMs: got %d, want %d", cfg.PeriodicNakIntervalMs, 5)
	}
	if cfg.PeriodicAckIntervalMs != 10 {
		t.Errorf("PeriodicAckIntervalMs: got %d, want %d", cfg.PeriodicAckIntervalMs, 10)
	}
}

// TestWithoutFastNakRecent verifies WithoutFastNakRecent helper
func TestWithoutFastNakRecent(t *testing.T) {
	// Start with full NAK btree config that has FastNAKRecent enabled
	cfg := ControlSRTConfig.WithNakBtree()

	if !cfg.FastNakRecentEnabled {
		t.Fatal("WithNakBtree should enable FastNakRecentEnabled")
	}

	// Disable FastNAKRecent
	cfg = cfg.WithoutFastNakRecent()

	if cfg.FastNakRecentEnabled {
		t.Error("WithoutFastNakRecent should disable FastNakRecentEnabled")
	}

	// FastNAK should still be enabled
	if !cfg.FastNakEnabled {
		t.Error("WithoutFastNakRecent should not affect FastNakEnabled")
	}
}

// TestGetSRTConfigWithLatency verifies GetSRTConfigWithLatency helper
func TestGetSRTConfigWithLatency(t *testing.T) {
	cfg := GetSRTConfigWithLatency(ConfigFull, 10*time.Second)

	// Check it's Full config
	if !cfg.UseNakBtree {
		t.Error("Expected Full config to have NAK btree enabled")
	}

	// Check latency is set
	if cfg.Latency != 10*time.Second {
		t.Errorf("Latency: got %v, want %v", cfg.Latency, 10*time.Second)
	}
}

// TestToCliFlagsTimerIntervals verifies timer interval flags are emitted
func TestToCliFlagsTimerIntervals(t *testing.T) {
	cfg := ControlSRTConfig.WithTimerProfile(TimerFastNak)
	flags := cfg.ToCliFlags()

	// Check for timer interval flags
	found := map[string]bool{
		"-tickintervalms":        false,
		"-periodicnakintervalms": false,
		"-periodicackintervalms": false,
	}

	for i, flag := range flags {
		if _, ok := found[flag]; ok {
			found[flag] = true
			// Check the value follows
			if i+1 < len(flags) {
				val := flags[i+1]
				switch flag {
				case "-tickintervalms":
					if val != "10" {
						t.Errorf("%s value: got %s, want 10", flag, val)
					}
				case "-periodicnakintervalms":
					if val != "5" {
						t.Errorf("%s value: got %s, want 5", flag, val)
					}
				case "-periodicackintervalms":
					if val != "10" {
						t.Errorf("%s value: got %s, want 10", flag, val)
					}
				}
			}
		}
	}

	for flag, wasFound := range found {
		if !wasFound {
			t.Errorf("Expected flag %s to be present", flag)
		}
	}
}
