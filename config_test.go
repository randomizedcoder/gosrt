package srt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()
	err := config.Validate()

	if err != nil {
		require.NoError(t, err, "Failed to verify the default configuration: %s", err)
	}
}

// TestConfig_TimerIntervals_Validation verifies timer interval validation
func TestConfig_TimerIntervals_Validation(t *testing.T) {
	t.Run("TickIntervalMs_zero_rejected", func(t *testing.T) {
		config := DefaultConfig()
		config.TickIntervalMs = 0
		err := config.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "TickIntervalMs must be > 0")
	})

	t.Run("PeriodicNakIntervalMs_zero_rejected", func(t *testing.T) {
		config := DefaultConfig()
		config.PeriodicNakIntervalMs = 0
		err := config.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "PeriodicNakIntervalMs must be > 0")
	})

	t.Run("PeriodicAckIntervalMs_zero_rejected", func(t *testing.T) {
		config := DefaultConfig()
		config.PeriodicAckIntervalMs = 0
		err := config.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "PeriodicAckIntervalMs must be > 0")
	})

	t.Run("valid_timer_intervals_accepted", func(t *testing.T) {
		config := DefaultConfig()
		config.TickIntervalMs = 5
		config.PeriodicNakIntervalMs = 10
		config.PeriodicAckIntervalMs = 5
		err := config.Validate()
		require.NoError(t, err)
	})
}

// TestConfig_NakRecentPercent_Validation verifies NakRecentPercent range validation
func TestConfig_NakRecentPercent_Validation(t *testing.T) {
	t.Run("negative_rejected", func(t *testing.T) {
		config := DefaultConfig()
		config.NakRecentPercent = -0.1
		err := config.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "NakRecentPercent must be between 0.0 and 1.0")
	})

	t.Run("greater_than_one_rejected", func(t *testing.T) {
		config := DefaultConfig()
		config.NakRecentPercent = 1.1
		err := config.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "NakRecentPercent must be between 0.0 and 1.0")
	})

	t.Run("zero_accepted", func(t *testing.T) {
		config := DefaultConfig()
		config.NakRecentPercent = 0.0
		err := config.Validate()
		require.NoError(t, err)
	})

	t.Run("one_accepted", func(t *testing.T) {
		config := DefaultConfig()
		config.NakRecentPercent = 1.0
		err := config.Validate()
		require.NoError(t, err)
	})

	t.Run("valid_value_accepted", func(t *testing.T) {
		config := DefaultConfig()
		config.NakRecentPercent = 0.15
		err := config.Validate()
		require.NoError(t, err)
	})
}

// TestConfig_FastNakThreshold_Validation verifies FastNakThresholdMs validation when enabled
func TestConfig_FastNakThreshold_Validation(t *testing.T) {
	t.Run("zero_when_enabled_rejected", func(t *testing.T) {
		config := DefaultConfig()
		config.FastNakEnabled = true
		config.FastNakThresholdMs = 0
		err := config.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "FastNakThresholdMs must be > 0 when FastNakEnabled")
	})

	t.Run("zero_when_disabled_accepted", func(t *testing.T) {
		config := DefaultConfig()
		config.FastNakEnabled = false
		config.FastNakThresholdMs = 0
		err := config.Validate()
		require.NoError(t, err)
	})

	t.Run("valid_threshold_when_enabled_accepted", func(t *testing.T) {
		config := DefaultConfig()
		config.FastNakEnabled = true
		config.FastNakThresholdMs = 50
		err := config.Validate()
		require.NoError(t, err)
	})
}

// TestConfig_Defaults_TimerIntervals verifies default values for timer intervals
func TestConfig_Defaults_TimerIntervals(t *testing.T) {
	config := DefaultConfig()

	require.Equal(t, uint64(10), config.TickIntervalMs, "TickIntervalMs default should be 10")
	require.Equal(t, uint64(20), config.PeriodicNakIntervalMs, "PeriodicNakIntervalMs default should be 20")
	require.Equal(t, uint64(10), config.PeriodicAckIntervalMs, "PeriodicAckIntervalMs default should be 10")
}

// TestConfig_Defaults_NakBtreeParams verifies default values for NAK btree parameters
func TestConfig_Defaults_NakBtreeParams(t *testing.T) {
	config := DefaultConfig()

	require.Equal(t, 0.10, config.NakRecentPercent, "NakRecentPercent default should be 0.10")
	require.Equal(t, uint32(3), config.NakMergeGap, "NakMergeGap default should be 3")
	require.Equal(t, uint64(2000), config.NakConsolidationBudgetUs, "NakConsolidationBudgetUs default should be 2000 (2ms)")
	require.Equal(t, uint64(50), config.FastNakThresholdMs, "FastNakThresholdMs default should be 50")
}

func TestMarshalUnmarshal(t *testing.T) {
	wantConfig := Config{
		Congestion:            "xxx",
		ConnectionTimeout:     42 * time.Second,
		DriftTracer:           false,
		EnforcedEncryption:    false,
		FC:                    42,
		GroupConnect:          true,
		GroupStabilityTimeout: 42 * time.Second,
		InputBW:               42,
		IPTOS:                 42,
		IPTTL:                 42,
		IPv6Only:              42,
		KMPreAnnounce:         42,
		KMRefreshRate:         42,
		Latency:               42 * time.Second,
		LossMaxTTL:            42,
		MaxBW:                 42,
		MessageAPI:            true,
		MinInputBW:            42,
		MSS:                   42,
		NAKReport:             false,
		OverheadBW:            42,
		PacketFilter:          "FEC",
		Passphrase:            "foobar",
		PayloadSize:           42,
		PBKeylen:              42,
		PeerIdleTimeout:       42 * time.Second,
		PeerLatency:           42 * time.Second,
		ReceiverBufferSize:    42,
		ReceiverLatency:       42 * time.Second,
		SendBufferSize:        42,
		SendDropDelay:         42 * time.Second,
		StreamId:              "foobaz",
		TooLatePacketDrop:     false,
		TransmissionType:      "yyy",
		TSBPDMode:             false,
		Logger:                nil,
	}

	url := wantConfig.MarshalURL("localhost:6000")

	config := Config{}
	config.UnmarshalURL(url)

	require.Equal(t, wantConfig, config)
}
