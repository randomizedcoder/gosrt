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
func (c *Config) Validate() error {
	if c.TransmissionType != "live" {
		return fmt.Errorf("config: TransmissionType must be 'live'")
	}

	c.Congestion = "live"
	c.NAKReport = true
	c.TooLatePacketDrop = true
	c.TSBPDMode = true

	if c.Congestion != "live" {
		return fmt.Errorf("config: Congestion mode must be 'live'")
	}

	if c.ConnectionTimeout <= 0 {
		return fmt.Errorf("config: ConnectionTimeout must be greater than 0")
	}

	if c.GroupConnect {
		return fmt.Errorf("config: GroupConnect is not supported")
	}

	if c.IPTOS > 0 && c.IPTOS > 255 {
		return fmt.Errorf("config: IPTOS must be lower than 255")
	}

	if c.IPTTL > 0 && c.IPTTL > 255 {
		return fmt.Errorf("config: IPTTL must be between 1 and 255")
	}

	if c.IPv6Only > 0 {
		return fmt.Errorf("config: IPv6Only is not supported")
	}

	if c.KMRefreshRate != 0 {
		if c.KMPreAnnounce < 1 || c.KMPreAnnounce > c.KMRefreshRate/2 {
			return fmt.Errorf("config: KMPreAnnounce must be greater than 1 and smaller than KMRefreshRate/2")
		}
	}

	if c.Latency >= 0 {
		c.PeerLatency = c.Latency
		c.ReceiverLatency = c.Latency
	}

	if c.MinVersion != SRT_VERSION {
		return fmt.Errorf("config: MinVersion must be %#06x", SRT_VERSION)
	}

	if c.MSS < MIN_MSS_SIZE || c.MSS > MAX_MSS_SIZE {
		return fmt.Errorf("config: MSS must be between %d and %d (both inclusive)", MIN_MSS_SIZE, MAX_MSS_SIZE)
	}

	if !c.NAKReport {
		return fmt.Errorf("config: NAKReport must be enabled")
	}

	if c.OverheadBW < 10 || c.OverheadBW > 100 {
		return fmt.Errorf("config: OverheadBW must be between 10 and 100")
	}

	if len(c.PacketFilter) != 0 {
		return fmt.Errorf("config: PacketFilter are not supported")
	}

	if len(c.Passphrase) != 0 {
		if len(c.Passphrase) < MIN_PASSPHRASE_SIZE || len(c.Passphrase) > MAX_PASSPHRASE_SIZE {
			return fmt.Errorf("config: Passphrase must be between %d and %d bytes long", MIN_PASSPHRASE_SIZE, MAX_PASSPHRASE_SIZE)
		}
	}

	if c.PayloadSize < MIN_PAYLOAD_SIZE || c.PayloadSize > MAX_PAYLOAD_SIZE {
		return fmt.Errorf("config: PayloadSize must be between %d and %d (both inclusive)", MIN_PAYLOAD_SIZE, MAX_PAYLOAD_SIZE)
	}

	if c.PayloadSize > c.MSS-uint32(SRT_HEADER_SIZE+UDP_HEADER_SIZE) {
		return fmt.Errorf("config: PayloadSize must not be larger than %d (MSS - %d)", c.MSS-uint32(SRT_HEADER_SIZE+UDP_HEADER_SIZE), SRT_HEADER_SIZE-UDP_HEADER_SIZE)
	}

	if c.PBKeylen != 16 && c.PBKeylen != 24 && c.PBKeylen != 32 {
		return fmt.Errorf("config: PBKeylen must be 16, 24, or 32 bytes")
	}

	if c.PeerLatency < 0 {
		return fmt.Errorf("config: PeerLatency must be greater than 0")
	}

	if c.ReceiverLatency < 0 {
		return fmt.Errorf("config: ReceiverLatency must be greater than 0")
	}

	if c.SendDropDelay < 0 {
		return fmt.Errorf("config: SendDropDelay must be greater than 0")
	}

	if len(c.StreamId) > MAX_STREAMID_SIZE {
		return fmt.Errorf("config: StreamId must be shorter than or equal to %d bytes", MAX_STREAMID_SIZE)
	}

	if !c.TooLatePacketDrop {
		return fmt.Errorf("config: TooLatePacketDrop must be enabled")
	}

	if c.TransmissionType != "live" {
		return fmt.Errorf("config: TransmissionType must be 'live'")
	}

	if !c.TSBPDMode {
		return fmt.Errorf("config: TSBPDMode must be enabled")
	}

	// Validate io_uring send configuration
	if c.IoUringEnabled {
		if c.IoUringSendRingSize < 16 || c.IoUringSendRingSize > 1024 {
			return fmt.Errorf("config: IoUringSendRingSize must be between 16 and 1024")
		}
		// Check if ring size is a power of 2
		if c.IoUringSendRingSize&(c.IoUringSendRingSize-1) != 0 {
			return fmt.Errorf("config: IoUringSendRingSize must be a power of 2")
		}
	}

	// Validate io_uring receive configuration
	if c.IoUringRecvRingSize > 0 {
		if c.IoUringRecvRingSize&(c.IoUringRecvRingSize-1) != 0 {
			return fmt.Errorf("config: IoUringRecvRingSize must be a power of 2")
		}
		if c.IoUringRecvRingSize < 64 || c.IoUringRecvRingSize > 32768 {
			return fmt.Errorf("config: IoUringRecvRingSize must be between 64 and 32768")
		}
	}

	if c.IoUringRecvInitialPending > 0 {
		if c.IoUringRecvInitialPending < 16 || c.IoUringRecvInitialPending > 32768 {
			return fmt.Errorf("config: IoUringRecvInitialPending must be between 16 and 32768")
		}
		if c.IoUringRecvRingSize > 0 && c.IoUringRecvInitialPending > c.IoUringRecvRingSize {
			return fmt.Errorf("config: IoUringRecvInitialPending (%d) must not exceed IoUringRecvRingSize (%d)",
				c.IoUringRecvInitialPending, c.IoUringRecvRingSize)
		}
	}

	if c.IoUringRecvBatchSize > 0 {
		if c.IoUringRecvBatchSize < 1 || c.IoUringRecvBatchSize > 32768 {
			return fmt.Errorf("config: IoUringRecvBatchSize must be between 1 and 32768")
		}
	}

	// Validate HandshakeTimeout
	if c.HandshakeTimeout <= 0 {
		return fmt.Errorf("config: HandshakeTimeout must be greater than 0")
	}

	// Validate HandshakeTimeout < PeerIdleTimeout
	if c.HandshakeTimeout >= c.PeerIdleTimeout {
		return fmt.Errorf("config: HandshakeTimeout (%v) must be less than PeerIdleTimeout (%v)",
			c.HandshakeTimeout, c.PeerIdleTimeout)
	}

	// Validate ShutdownDelay
	if c.ShutdownDelay <= 0 {
		return fmt.Errorf("config: ShutdownDelay must be greater than 0")
	}

	// Validate lock-free ring buffer configuration
	if c.UsePacketRing {
		if c.PacketRingSize < 64 || c.PacketRingSize > 65536 {
			return fmt.Errorf("config: PacketRingSize must be between 64 and 65536")
		}
		if c.PacketRingSize&(c.PacketRingSize-1) != 0 {
			return fmt.Errorf("config: PacketRingSize must be a power of 2")
		}
		if c.PacketRingShards < 1 || c.PacketRingShards > 64 {
			return fmt.Errorf("config: PacketRingShards must be between 1 and 64")
		}
		if c.PacketRingShards&(c.PacketRingShards-1) != 0 {
			return fmt.Errorf("config: PacketRingShards must be a power of 2")
		}
		if c.PacketRingMaxRetries < 0 {
			return fmt.Errorf("config: PacketRingMaxRetries must be >= 0")
		}
		if c.PacketRingBackoffDuration < 0 {
			return fmt.Errorf("config: PacketRingBackoffDuration must be >= 0")
		}
		if c.PacketRingMaxBackoffs < 0 {
			return fmt.Errorf("config: PacketRingMaxBackoffs must be >= 0")
		}
	}

	// Validate event loop configuration (Phase 4)
	if c.UseEventLoop {
		// Event loop requires lock-free ring buffer (Phase 3)
		if !c.UsePacketRing {
			return fmt.Errorf("config: UseEventLoop requires UsePacketRing=true")
		}
		if c.EventLoopRateInterval <= 0 {
			return fmt.Errorf("config: EventLoopRateInterval must be > 0")
		}
		if c.BackoffColdStartPkts < 0 {
			return fmt.Errorf("config: BackoffColdStartPkts must be >= 0")
		}
		if c.BackoffMinSleep < 0 {
			return fmt.Errorf("config: BackoffMinSleep must be >= 0")
		}
		if c.BackoffMaxSleep < 0 {
			return fmt.Errorf("config: BackoffMaxSleep must be >= 0")
		}
		if c.BackoffMinSleep > c.BackoffMaxSleep {
			return fmt.Errorf("config: BackoffMinSleep (%v) must be <= BackoffMaxSleep (%v)",
				c.BackoffMinSleep, c.BackoffMaxSleep)
		}
	}

	// Validate timer intervals - these control receiver processing frequency
	// Zero values would cause infinite loops or division by zero
	if c.TickIntervalMs == 0 {
		return fmt.Errorf("config: TickIntervalMs must be > 0 (default: 10)")
	}
	if c.PeriodicNakIntervalMs == 0 {
		return fmt.Errorf("config: PeriodicNakIntervalMs must be > 0 (default: 20)")
	}
	if c.PeriodicAckIntervalMs == 0 {
		return fmt.Errorf("config: PeriodicAckIntervalMs must be > 0 (default: 10)")
	}

	// Validate NAK btree parameters
	if c.NakRecentPercent < 0 || c.NakRecentPercent > 1.0 {
		return fmt.Errorf("config: NakRecentPercent must be between 0.0 and 1.0 (default: 0.10)")
	}

	// Validate FastNAK threshold when enabled
	if c.FastNakEnabled && c.FastNakThresholdMs == 0 {
		return fmt.Errorf("config: FastNakThresholdMs must be > 0 when FastNakEnabled (default: 50)")
	}

	// Validate LightACKDifference (Phase 5: ACK Optimization)
	// Default to 64 if not set, enforce maximum of 5000
	if c.LightACKDifference == 0 {
		c.LightACKDifference = 64 // RFC recommendation
	}
	if c.LightACKDifference > 5000 {
		return fmt.Errorf("config: LightACKDifference must be <= 5000, got %d", c.LightACKDifference)
	}

	return nil
}

