package srt

import (
	"strings"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// Corner Case Tests for Config Validators
// These tests focus on boundary values and edge cases where bugs hide.
// ═══════════════════════════════════════════════════════════════════════════

// TestValidateMSS_Boundaries tests exact boundary values for MSS.
func TestValidateMSS_Boundaries(t *testing.T) {
	tests := []struct {
		name    string
		mss     uint32
		wantErr bool
	}{
		{"below_min", MIN_MSS_SIZE - 1, true},
		{"at_min", MIN_MSS_SIZE, false},
		{"at_min_plus_one", MIN_MSS_SIZE + 1, false},
		{"at_max_minus_one", MAX_MSS_SIZE - 1, false},
		{"at_max", MAX_MSS_SIZE, false},
		{"above_max", MAX_MSS_SIZE + 1, true},
		{"zero", 0, true},
		{"typical_ethernet", 1500, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{MSS: tc.mss}
			err := validateMSS(c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateMSS(%d) error = %v, wantErr %v", tc.mss, err, tc.wantErr)
			}
		})
	}
}

// TestValidateIPTOS_Boundaries tests IPTOS 8-bit value range.
// Note: Current validator only checks upper bound (> 255), not lower bound.
func TestValidateIPTOS_Boundaries(t *testing.T) {
	tests := []struct {
		name    string
		iptos   int
		wantErr bool
	}{
		{"zero", 0, false},
		{"one", 1, false},
		{"typical_dscp", 128, false},
		{"at_255", 255, false},
		{"at_256", 256, true},
		{"large_value", 1000, true},
		// Note: Validator does NOT currently check for negative values
		// This could be a potential improvement
		{"negative", -1, false}, // Current behavior - no lower bound check
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{IPTOS: tc.iptos}
			err := validateIPTOS(c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateIPTOS(%d) error = %v, wantErr %v", tc.iptos, err, tc.wantErr)
			}
		})
	}
}

// TestValidatePBKeylen_ExactValues tests only valid AES key lengths.
// Valid values are: 16 (AES-128), 24 (AES-192), 32 (AES-256).
// Zero is NOT valid - a key length must be specified.
func TestValidatePBKeylen_ExactValues(t *testing.T) {
	tests := []struct {
		name    string
		keylen  int
		wantErr bool
	}{
		{"zero_invalid", 0, true}, // Zero is not allowed
		{"aes128", 16, false},
		{"aes192", 24, false},
		{"aes256", 32, false},
		{"invalid_15", 15, true},
		{"invalid_17", 17, true},
		{"invalid_20", 20, true},
		{"invalid_23", 23, true},
		{"invalid_25", 25, true},
		{"invalid_31", 31, true},
		{"invalid_33", 33, true},
		{"invalid_64", 64, true},
		{"negative", -1, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{PBKeylen: tc.keylen}
			err := validatePBKeylen(c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validatePBKeylen(%d) error = %v, wantErr %v", tc.keylen, err, tc.wantErr)
			}
		})
	}
}

// TestValidateOverheadBW_Boundaries tests overhead bandwidth percentage.
func TestValidateOverheadBW_Boundaries(t *testing.T) {
	tests := []struct {
		name    string
		bw      int64
		wantErr bool
	}{
		{"below_min", 9, true},
		{"at_min", 10, false},
		{"at_min_plus_one", 11, false},
		{"typical", 25, false},
		{"at_max_minus_one", 99, false},
		{"at_max", 100, false},
		{"above_max", 101, true},
		{"zero", 0, true},
		{"negative", -1, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{OverheadBW: tc.bw}
			err := validateOverheadBW(c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateOverheadBW(%d) error = %v, wantErr %v", tc.bw, err, tc.wantErr)
			}
		})
	}
}

// TestValidateNakRecentPercent_FloatBoundaries tests floating point boundaries.
func TestValidateNakRecentPercent_FloatBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		percent float64
		wantErr bool
	}{
		{"zero", 0.0, false},
		{"tiny_positive", 0.0001, false},
		{"typical", 0.10, false},
		{"half", 0.50, false},
		{"at_max", 1.0, false},
		{"slightly_above_max", 1.0001, true},
		{"negative_tiny", -0.0001, true},
		{"negative", -0.1, true},
		{"large", 2.0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{NakRecentPercent: tc.percent}
			err := validateNakRecentPercent(c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateNakRecentPercent(%f) error = %v, wantErr %v", tc.percent, err, tc.wantErr)
			}
		})
	}
}

// TestValidateConnectionTimeout_DurationBoundaries tests duration edge cases.
func TestValidateConnectionTimeout_DurationBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		wantErr bool
	}{
		{"positive_1s", time.Second, false},
		{"positive_1ms", time.Millisecond, false},
		{"positive_1us", time.Microsecond, false},
		{"positive_1ns", time.Nanosecond, false},
		{"zero", 0, true},
		{"negative", -time.Second, true},
		{"large", 24 * time.Hour, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{ConnectionTimeout: tc.timeout}
			err := validateConnectionTimeout(c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateConnectionTimeout(%v) error = %v, wantErr %v", tc.timeout, err, tc.wantErr)
			}
		})
	}
}

// TestValidatePacketRingSize_PowerOf2 tests power of 2 requirement AND range [64, 65536].
func TestValidatePacketRingSize_PowerOf2(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		// Below minimum 64 - all fail even if power of 2
		{"1_below_min", 1, true},
		{"2_below_min", 2, true},
		{"4_below_min", 4, true},
		{"16_below_min", 16, true},
		{"32_below_min", 32, true},
		// At and above minimum 64
		{"64_at_min", 64, false},
		{"128", 128, false},
		{"256", 256, false},
		{"1024", 1024, false},
		{"65536_at_max", 65536, false},
		// Above max
		{"131072_above_max", 131072, true},
		// Not power of 2 (in valid range)
		{"100_not_pow2", 100, true},
		{"1000_not_pow2", 1000, true},
		{"1023_not_pow2", 1023, true},
		{"1025_not_pow2", 1025, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{
				UsePacketRing:    true,
				PacketRingSize:   tc.size,
				PacketRingShards: 1, // Valid power of 2
			}
			err := validatePacketRingConfig(c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validatePacketRingConfig(size=%d) error = %v, wantErr %v", tc.size, err, tc.wantErr)
			}
		})
	}
}

// TestValidateTimerIntervals_RelationshipConstraints tests timer relationships.
func TestValidateTimerIntervals_RelationshipConstraints(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errSub  string
	}{
		{
			name: "nak_less_than_ack",
			config: Config{
				PeriodicAckIntervalMs: 20,
				PeriodicNakIntervalMs: 10, // NAK < ACK is invalid
				TickIntervalMs:        10,
				Latency:               3 * time.Second,
			},
			wantErr: true,
			errSub:  "must be >= PeriodicAckIntervalMs",
		},
		{
			name: "nak_equal_to_ack",
			config: Config{
				PeriodicAckIntervalMs: 20,
				PeriodicNakIntervalMs: 20, // NAK == ACK is valid
				TickIntervalMs:        10,
				Latency:               3 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "nak_greater_than_10x_ack",
			config: Config{
				PeriodicAckIntervalMs: 10,
				PeriodicNakIntervalMs: 150, // NAK > 10*ACK is invalid
				TickIntervalMs:        10,
				Latency:               3 * time.Second,
			},
			wantErr: true,
			errSub:  "should be <= 10",
		},
		{
			name: "drop_below_minimum_50ms",
			config: Config{
				PeriodicAckIntervalMs: 10,
				PeriodicNakIntervalMs: 20,
				SendDropIntervalMs:    30, // Below min 50ms
				TickIntervalMs:        10,
				Latency:               3 * time.Second,
			},
			wantErr: true,
			errSub:  "must be >= 50ms",
		},
		{
			name: "drop_less_than_nak",
			config: Config{
				PeriodicAckIntervalMs: 10,
				PeriodicNakIntervalMs: 80,
				SendDropIntervalMs:    60, // >= 50ms but < NAK (80ms)
				TickIntervalMs:        10,
				Latency:               3 * time.Second,
			},
			wantErr: true,
			errSub:  "must be >= PeriodicNakIntervalMs",
		},
		{
			name: "valid_relationship",
			config: Config{
				PeriodicAckIntervalMs: 10,
				PeriodicNakIntervalMs: 20,
				SendDropIntervalMs:    100,
				TickIntervalMs:        10,
				Latency:               3 * time.Second,
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.validateTimerIntervals()
			if (err != nil) != tc.wantErr {
				t.Errorf("validateTimerIntervals() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && tc.errSub != "" && err != nil {
				if !strings.Contains(err.Error(), tc.errSub) {
					t.Errorf("error = %q, want substring %q", err.Error(), tc.errSub)
				}
			}
		})
	}
}

// TestValidatePassphrase_Length tests passphrase length constraints.
func TestValidatePassphrase_Length(t *testing.T) {
	tests := []struct {
		name       string
		passphrase string
		wantErr    bool
	}{
		{"empty_allowed", "", false},
		{"too_short_9", "123456789", true},
		{"min_10", "1234567890", false},
		{"typical_16", "1234567890123456", false},
		{"max_79", strings.Repeat("a", 79), false},
		{"at_max_80", strings.Repeat("a", 80), false}, // 80 is max
		// Note: Longer might be truncated or handled differently
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Passphrase: tc.passphrase}
			err := validatePassphrase(c)
			if (err != nil) != tc.wantErr {
				t.Errorf("validatePassphrase(len=%d) error = %v, wantErr %v",
					len(tc.passphrase), err, tc.wantErr)
			}
		})
	}
}

// TestValidateWithTable_OrderIndependence verifies validators run in order.
func TestValidateWithTable_OrderIndependence(t *testing.T) {
	// First validator to fail should report its error
	c := &Config{
		TransmissionType:  "file", // First failure
		ConnectionTimeout: 0,      // Second failure
		MSS:               0,      // Third failure
	}

	err := validateWithTable(c)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// First error should be about TransmissionType
	if !strings.Contains(err.Error(), "TransmissionType") {
		t.Errorf("expected TransmissionType error first, got: %v", err)
	}
}

// TestConfigValidatorCount_Minimum ensures minimum validator coverage.
func TestConfigValidatorCount_Minimum(t *testing.T) {
	count := GetConfigValidatorCount()

	// We expect at least 30 validators based on the plan
	minExpected := 30
	if count < minExpected {
		t.Errorf("GetConfigValidatorCount() = %d, want >= %d", count, minExpected)
	}
}

// BenchmarkValidateWithTable benchmarks the table-driven validation.
func BenchmarkValidateWithTable(b *testing.B) {
	c := DefaultConfig()

	b.Run("valid_config", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = validateWithTable(&c)
		}
	})

	b.Run("invalid_config_early_exit", func(b *testing.B) {
		invalid := Config{TransmissionType: "file"}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = validateWithTable(&invalid)
		}
	})
}
