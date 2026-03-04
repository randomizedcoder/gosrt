package srt

import "fmt"

// ApplyAutoConfiguration sets internal configuration based on other settings.
// Call this after user configuration is applied but before connection creation.
// This ensures that when io_uring recv is enabled, the NAK btree and related
// features are automatically configured.
func (c *Config) ApplyAutoConfiguration() {
	// When io_uring recv is enabled, NAK btree and suppress immediate NAK are required
	// to handle the out-of-order packet delivery from io_uring
	if c.IoUringRecvEnabled {
		c.UseNakBtree = true
		c.SuppressImmediateNak = true
	}

	// When NAK btree is enabled, enable FastNAK by default
	if c.UseNakBtree {
		// Only set if not already explicitly set to true
		// (we can't tell if user explicitly set false, so we enable by default)
		if !c.FastNakEnabled {
			c.FastNakEnabled = true
		}
		if !c.FastNakRecentEnabled {
			c.FastNakRecentEnabled = true
		}
	}
}

// Validate validates a configuration, returns an error if a field
// has an invalid value.
//
// This function uses a table-driven approach for individual field validations,
// reducing cyclomatic complexity from 80 to ~15.
func (c *Config) Validate() error {
	// Apply required settings first (these must be set for validation to pass)
	c.Congestion = "live"
	c.NAKReport = true
	c.TooLatePacketDrop = true
	c.TSBPDMode = true

	// Apply latency inheritance
	if c.Latency >= 0 {
		c.PeerLatency = c.Latency
		c.ReceiverLatency = c.Latency
	}

	// Default LightACKDifference if not set
	if c.LightACKDifference == 0 {
		c.LightACKDifference = 64 // RFC recommendation
	}

	// Run table-driven validators
	if err := validateWithTable(c); err != nil {
		return err
	}

	// Validate timer intervals with comprehensive rules
	// See: configurable_timer_intervals_design.md Section 4.3
	if err := c.validateTimerIntervals(); err != nil {
		return err
	}

	return nil
}

// validateTimerIntervals performs comprehensive timer interval validation.
// Rules are documented in configurable_timer_intervals_design.md Section 4.3
func (c Config) validateTimerIntervals() error {
	// Get effective values (0 means use default, which is already set by DefaultConfig)
	ack := c.PeriodicAckIntervalMs
	nak := c.PeriodicNakIntervalMs
	tick := c.TickIntervalMs
	drop := c.SendDropIntervalMs
	rate := c.EventLoopRateIntervalMs

	var latencyMs uint64
	if latencyMsInt := c.Latency.Milliseconds(); latencyMsInt > 0 {
		latencyMs = uint64(latencyMsInt)
	} else {
		latencyMs = 3000 // Default 3s
	}

	// ═══════════════════════════════════════════════════════════════════════
	// ABSOLUTE BOUNDS - Prevent CPU spin and unresponsive behavior
	// ═══════════════════════════════════════════════════════════════════════

	// R1: ACK minimum (already checked for 0 above, but check explicit small values)
	if ack == 0 {
		return fmt.Errorf("config: PeriodicAckIntervalMs must be > 0 (default: 10)")
	}

	// R2: ACK maximum
	if ack > 1000 {
		return fmt.Errorf("config: PeriodicAckIntervalMs (%d) must be <= 1000ms - RTT accuracy degrades with larger intervals", ack)
	}

	// R3: NAK minimum
	if nak == 0 {
		return fmt.Errorf("config: PeriodicNakIntervalMs must be > 0 (default: 20)")
	}

	// R4: NAK maximum
	if nak > 2000 {
		return fmt.Errorf("config: PeriodicNakIntervalMs (%d) must be <= 2000ms - loss recovery too slow with larger intervals", nak)
	}

	// R6: Tick minimum
	if tick == 0 {
		return fmt.Errorf("config: TickIntervalMs must be > 0 (default: 10)")
	}

	// R7: Tick maximum
	if tick > 1000 {
		return fmt.Errorf("config: TickIntervalMs (%d) must be <= 1000ms - TSBPD delivery too slow with larger intervals", tick)
	}

	// R8: Drop minimum (if explicitly set)
	if drop > 0 && drop < 50 {
		return fmt.Errorf("config: SendDropIntervalMs (%d) must be >= 50ms - drop checks too frequent wastes CPU", drop)
	}

	// R9: Drop maximum (if explicitly set)
	if drop > 5000 {
		return fmt.Errorf("config: SendDropIntervalMs (%d) must be <= 5000ms - stale packets may accumulate", drop)
	}

	// R11: Rate minimum (if explicitly set)
	if rate > 0 && rate < 100 {
		return fmt.Errorf("config: EventLoopRateIntervalMs (%d) must be >= 100ms - rate calculation too frequent wastes CPU", rate)
	}

	// R12: Rate maximum (if explicitly set)
	if rate > 10000 {
		return fmt.Errorf("config: EventLoopRateIntervalMs (%d) must be <= 10000ms - rate updates too infrequent for monitoring", rate)
	}

	// ═══════════════════════════════════════════════════════════════════════
	// RELATIONSHIP CONSTRAINTS - Ensure protocol coherence
	// ═══════════════════════════════════════════════════════════════════════

	// R5: NAK >= ACK (NAK relies on RTT from ACK)
	if nak < ack {
		return fmt.Errorf("config: PeriodicNakIntervalMs (%d) must be >= PeriodicAckIntervalMs (%d) - NAK uses RTT from ACK",
			nak, ack)
	}

	// R10: Drop >= NAK (allow NAK/retransmit cycle before dropping)
	// Only validate if drop is explicitly set (non-zero)
	if drop > 0 && drop < nak {
		return fmt.Errorf("config: SendDropIntervalMs (%d) must be >= PeriodicNakIntervalMs (%d) - allow NAK/retransmit before drop",
			drop, nak)
	}

	// R13: NAK <= 10 × ACK (keep NAK reasonably coupled)
	if nak > ack*10 {
		return fmt.Errorf("config: PeriodicNakIntervalMs (%d) should be <= 10 × PeriodicAckIntervalMs (%d) - NAK too decoupled from ACK",
			nak, ack*10)
	}

	// R14: In Tick mode, Tick <= 2 × ACK (Tick must fire often enough for ACK)
	// Note: In EventLoop mode, ACK has its own ticker, so this doesn't apply
	if !c.UseEventLoop && tick > ack*2 {
		return fmt.Errorf("config: TickIntervalMs (%d) should be <= 2 × PeriodicAckIntervalMs (%d) in Tick mode - Tick drives ACK",
			tick, ack*2)
	}

	// ═══════════════════════════════════════════════════════════════════════
	// TSBPD-RELATED CONSTRAINTS - Ensure timers fit within latency window
	// ═══════════════════════════════════════════════════════════════════════

	// R15: ACK < Latency/2 (multiple ACKs before expiry)
	if ack > latencyMs/2 {
		return fmt.Errorf("config: PeriodicAckIntervalMs (%d) should be < Latency/2 (%d) - with Latency=%dms, ACK interval too large for RTT updates",
			ack, latencyMs/2, latencyMs)
	}

	// R16: NAK < Latency/2 (multiple NAK cycles before expiry)
	if nak > latencyMs/2 {
		return fmt.Errorf("config: PeriodicNakIntervalMs (%d) should be < Latency/2 (%d) - with Latency=%dms, NAK interval too large for recovery",
			nak, latencyMs/2, latencyMs)
	}

	// R17: Tick < Latency/4 (multiple delivery opportunities)
	if tick > latencyMs/4 {
		return fmt.Errorf("config: TickIntervalMs (%d) should be < Latency/4 (%d) - with Latency=%dms, Tick too slow for smooth delivery",
			tick, latencyMs/4, latencyMs)
	}

	// ═══════════════════════════════════════════════════════════════════════
	// TYPO DETECTION - Catch obvious mistakes
	// ═══════════════════════════════════════════════════════════════════════

	// Detect if user accidentally entered microseconds instead of milliseconds
	if ack >= 10000 {
		return fmt.Errorf("config: PeriodicAckIntervalMs=%d seems too large - did you mean %dms instead? (values are in milliseconds)",
			ack, ack/1000)
	}
	if nak >= 20000 {
		return fmt.Errorf("config: PeriodicNakIntervalMs=%d seems too large - did you mean %dms instead? (values are in milliseconds)",
			nak, nak/1000)
	}
	if tick >= 10000 {
		return fmt.Errorf("config: TickIntervalMs=%d seems too large - did you mean %dms instead? (values are in milliseconds)",
			tick, tick/1000)
	}

	return nil
}
