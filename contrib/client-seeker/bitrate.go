package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// BitrateManager manages the current bitrate with atomic updates.
// It is intentionally simple - all ramping logic lives in the Orchestrator.
//
// The Orchestrator handles ramping by sending frequent set_bitrate commands.
// This keeps the Seeker "dumb" and stateless.
type BitrateManager struct {
	current    atomic.Int64 // Current bitrate in bps
	target     atomic.Int64 // Target bitrate (same as current for instant changes)
	minBitrate int64        // Floor (default: 1 Mb/s)
	maxBitrate int64        // Ceiling (default: 1 Gb/s)

	// Token bucket for rate limiting
	bucket *TokenBucket
}

// NewBitrateManager creates a new bitrate manager.
//
// Parameters:
//   - initialBitrate: Starting bitrate in bits per second
//   - minBitrate: Minimum allowed bitrate
//   - maxBitrate: Maximum allowed bitrate
func NewBitrateManager(initialBitrate, minBitrate, maxBitrate int64) *BitrateManager {
	return NewBitrateManagerWithMode(initialBitrate, minBitrate, maxBitrate, RefillSleep)
}

// NewBitrateManagerWithMode creates a new bitrate manager with a specific refill mode.
//
// IMPORTANT: RefillSleep is recommended for high throughput (>100 Mb/s).
// RefillHybrid uses spin-wait which can consume excessive CPU and become the bottleneck.
// See: client_seeker_instrumentation_design.md Section 9.3
func NewBitrateManagerWithMode(initialBitrate, minBitrate, maxBitrate int64, mode RefillMode) *BitrateManager {
	// Clamp initial to bounds
	if initialBitrate < minBitrate {
		initialBitrate = minBitrate
	}
	if initialBitrate > maxBitrate {
		initialBitrate = maxBitrate
	}

	bm := &BitrateManager{
		minBitrate: minBitrate,
		maxBitrate: maxBitrate,
		bucket:     NewTokenBucket(initialBitrate, mode),
	}
	bm.current.Store(initialBitrate)
	bm.target.Store(initialBitrate)

	return bm
}

// Set updates the bitrate instantly.
// The Orchestrator handles ramping by calling this frequently.
//
// Returns error if bitrate is invalid (though it will be clamped to bounds).
func (bm *BitrateManager) Set(bitrate int64) error {
	if bitrate <= 0 {
		return fmt.Errorf("bitrate must be positive, got %d", bitrate)
	}

	// Clamp to bounds
	if bitrate < bm.minBitrate {
		bitrate = bm.minBitrate
	}
	if bitrate > bm.maxBitrate {
		bitrate = bm.maxBitrate
	}

	// Update atomically
	bm.current.Store(bitrate)
	bm.target.Store(bitrate)

	// Update token bucket rate
	bm.bucket.SetRate(bitrate)

	return nil
}

// Current returns the current bitrate in bits per second.
func (bm *BitrateManager) Current() int64 {
	return bm.current.Load()
}

// Target returns the target bitrate (same as current for instant changes).
func (bm *BitrateManager) Target() int64 {
	return bm.target.Load()
}

// Bucket returns the token bucket for rate limiting.
func (bm *BitrateManager) Bucket() *TokenBucket {
	return bm.bucket
}

// MinBitrate returns the minimum allowed bitrate.
func (bm *BitrateManager) MinBitrate() int64 {
	return bm.minBitrate
}

// MaxBitrate returns the maximum allowed bitrate.
func (bm *BitrateManager) MaxBitrate() int64 {
	return bm.maxBitrate
}

// ParseBitrate parses a human-readable bitrate string.
// Supports suffixes: K, M, G (case-insensitive)
//
// Examples:
//   - "100M" -> 100_000_000
//   - "1.5G" -> 1_500_000_000
//   - "500K" -> 500_000
//   - "1000000" -> 1_000_000
func ParseBitrate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty bitrate string")
	}

	// Check for suffix
	multiplier := int64(1)
	suffix := strings.ToUpper(s[len(s)-1:])

	switch suffix {
	case "K":
		multiplier = 1_000
		s = s[:len(s)-1]
	case "M":
		multiplier = 1_000_000
		s = s[:len(s)-1]
	case "G":
		multiplier = 1_000_000_000
		s = s[:len(s)-1]
	}

	// Parse the numeric part
	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid bitrate number: %w", err)
	}

	if value <= 0 {
		return 0, fmt.Errorf("bitrate must be positive, got %f", value)
	}

	return int64(value * float64(multiplier)), nil
}

// FormatBitrate formats a bitrate as a human-readable string.
func FormatBitrate(bps int64) string {
	switch {
	case bps >= 1_000_000_000:
		return fmt.Sprintf("%.2f Gb/s", float64(bps)/1_000_000_000)
	case bps >= 1_000_000:
		return fmt.Sprintf("%.2f Mb/s", float64(bps)/1_000_000)
	case bps >= 1_000:
		return fmt.Sprintf("%.2f Kb/s", float64(bps)/1_000)
	default:
		return fmt.Sprintf("%d b/s", bps)
	}
}
