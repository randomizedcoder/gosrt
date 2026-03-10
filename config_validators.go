package srt

import "fmt"

// ConfigValidator defines a single validation rule for Config.
// The table-driven approach reduces cyclomatic complexity and improves testability.
type ConfigValidator struct {
	Name     string              // Descriptive name for the validator
	Validate func(*Config) error // Validation function
}

// configValidators is the table of all config validators.
// Each entry performs a specific validation check.
// Organized by category for maintainability.
var configValidators = []ConfigValidator{
	// ════════════════════════════════════════════════════════════════════════
	// Core Protocol Requirements
	// ════════════════════════════════════════════════════════════════════════
	{"TransmissionType", validateTransmissionType},
	{"Congestion", validateCongestion},
	{"NAKReport", validateNAKReport},
	{"TooLatePacketDrop", validateTooLatePacketDrop},
	{"TSBPDMode", validateTSBPDMode},
	{"MinVersion", validateMinVersion},

	// ════════════════════════════════════════════════════════════════════════
	// Connection Parameters
	// ════════════════════════════════════════════════════════════════════════
	{"ConnectionTimeout", validateConnectionTimeout},
	{"HandshakeTimeout", validateHandshakeTimeout},
	{"HandshakeVsPeerIdleTimeout", validateHandshakeVsPeerIdleTimeout},
	{"ShutdownDelay", validateShutdownDelay},
	{"GroupConnect", validateGroupConnect},

	// ════════════════════════════════════════════════════════════════════════
	// Network Parameters
	// ════════════════════════════════════════════════════════════════════════
	{"IPTOS", validateIPTOS},
	{"IPTTL", validateIPTTL},
	{"IPv6Only", validateIPv6Only},

	// ════════════════════════════════════════════════════════════════════════
	// Packet Size Parameters
	// ════════════════════════════════════════════════════════════════════════
	{"MSS", validateMSS},
	{"PayloadSize", validatePayloadSize},
	{"PayloadSizeVsMSS", validatePayloadSizeVsMSS},

	// ════════════════════════════════════════════════════════════════════════
	// Bandwidth Parameters
	// ════════════════════════════════════════════════════════════════════════
	{"OverheadBW", validateOverheadBW},

	// ════════════════════════════════════════════════════════════════════════
	// Encryption Parameters
	// ════════════════════════════════════════════════════════════════════════
	{"KMPreAnnounce", validateKMPreAnnounce},
	{"Passphrase", validatePassphrase},
	{"PBKeylen", validatePBKeylen},

	// ════════════════════════════════════════════════════════════════════════
	// Latency Parameters
	// ════════════════════════════════════════════════════════════════════════
	{"PeerLatency", validatePeerLatency},
	{"ReceiverLatency", validateReceiverLatency},
	{"SendDropDelay", validateSendDropDelay},

	// ════════════════════════════════════════════════════════════════════════
	// Stream Parameters
	// ════════════════════════════════════════════════════════════════════════
	{"StreamId", validateStreamId},
	{"PacketFilter", validatePacketFilter},

	// ════════════════════════════════════════════════════════════════════════
	// io_uring Send Configuration
	// ════════════════════════════════════════════════════════════════════════
	{"IoUringSendConfig", validateIoUringSendConfig},

	// ════════════════════════════════════════════════════════════════════════
	// io_uring Receive Configuration
	// ════════════════════════════════════════════════════════════════════════
	{"IoUringRecvRingSize", validateIoUringRecvRingSize},
	{"IoUringRecvInitialPending", validateIoUringRecvInitialPending},
	{"IoUringRecvBatchSize", validateIoUringRecvBatchSize},

	// ════════════════════════════════════════════════════════════════════════
	// Lock-Free Ring Buffer Configuration (Phase 3)
	// ════════════════════════════════════════════════════════════════════════
	{"PacketRingConfig", validatePacketRingConfig},

	// ════════════════════════════════════════════════════════════════════════
	// EventLoop Configuration (Phase 4)
	// ════════════════════════════════════════════════════════════════════════
	{"EventLoopConfig", validateEventLoopConfig},

	// ════════════════════════════════════════════════════════════════════════
	// NAK Btree Parameters
	// ════════════════════════════════════════════════════════════════════════
	{"NakRecentPercent", validateNakRecentPercent},
	{"FastNakThreshold", validateFastNakThreshold},

	// ════════════════════════════════════════════════════════════════════════
	// ACK Optimization (Phase 5)
	// ════════════════════════════════════════════════════════════════════════
	{"LightACKDifference", validateLightACKDifference},
}

// ════════════════════════════════════════════════════════════════════════════
// Core Protocol Requirements
// ════════════════════════════════════════════════════════════════════════════

func validateTransmissionType(c *Config) error {
	if c.TransmissionType != "live" {
		return fmt.Errorf("config: TransmissionType must be 'live'")
	}
	return nil
}

func validateCongestion(c *Config) error {
	if c.Congestion != "live" {
		return fmt.Errorf("config: Congestion mode must be 'live'")
	}
	return nil
}

func validateNAKReport(c *Config) error {
	if !c.NAKReport {
		return fmt.Errorf("config: NAKReport must be enabled")
	}
	return nil
}

func validateTooLatePacketDrop(c *Config) error {
	if !c.TooLatePacketDrop {
		return fmt.Errorf("config: TooLatePacketDrop must be enabled")
	}
	return nil
}

func validateTSBPDMode(c *Config) error {
	if !c.TSBPDMode {
		return fmt.Errorf("config: TSBPDMode must be enabled")
	}
	return nil
}

func validateMinVersion(c *Config) error {
	if c.MinVersion != SRT_VERSION {
		return fmt.Errorf("config: MinVersion must be %#06x", SRT_VERSION)
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Connection Parameters
// ════════════════════════════════════════════════════════════════════════════

func validateConnectionTimeout(c *Config) error {
	if c.ConnectionTimeout <= 0 {
		return fmt.Errorf("config: ConnectionTimeout must be greater than 0")
	}
	return nil
}

func validateHandshakeTimeout(c *Config) error {
	if c.HandshakeTimeout <= 0 {
		return fmt.Errorf("config: HandshakeTimeout must be greater than 0")
	}
	return nil
}

func validateHandshakeVsPeerIdleTimeout(c *Config) error {
	if c.HandshakeTimeout >= c.PeerIdleTimeout {
		return fmt.Errorf("config: HandshakeTimeout (%v) must be less than PeerIdleTimeout (%v)",
			c.HandshakeTimeout, c.PeerIdleTimeout)
	}
	return nil
}

func validateShutdownDelay(c *Config) error {
	if c.ShutdownDelay <= 0 {
		return fmt.Errorf("config: ShutdownDelay must be greater than 0")
	}
	return nil
}

func validateGroupConnect(c *Config) error {
	if c.GroupConnect {
		return fmt.Errorf("config: GroupConnect is not supported")
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Network Parameters
// ════════════════════════════════════════════════════════════════════════════

func validateIPTOS(c *Config) error {
	if c.IPTOS > 0 && c.IPTOS > 255 {
		return fmt.Errorf("config: IPTOS must be lower than 255")
	}
	return nil
}

func validateIPTTL(c *Config) error {
	if c.IPTTL > 0 && c.IPTTL > 255 {
		return fmt.Errorf("config: IPTTL must be between 1 and 255")
	}
	return nil
}

func validateIPv6Only(c *Config) error {
	if c.IPv6Only > 0 {
		return fmt.Errorf("config: IPv6Only is not supported")
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Packet Size Parameters
// ════════════════════════════════════════════════════════════════════════════

func validateMSS(c *Config) error {
	if c.MSS < MIN_MSS_SIZE || c.MSS > MAX_MSS_SIZE {
		return fmt.Errorf("config: MSS must be between %d and %d (both inclusive)", MIN_MSS_SIZE, MAX_MSS_SIZE)
	}
	return nil
}

func validatePayloadSize(c *Config) error {
	if c.PayloadSize < MIN_PAYLOAD_SIZE || c.PayloadSize > MAX_PAYLOAD_SIZE {
		return fmt.Errorf("config: PayloadSize must be between %d and %d (both inclusive)", MIN_PAYLOAD_SIZE, MAX_PAYLOAD_SIZE)
	}
	return nil
}

func validatePayloadSizeVsMSS(c *Config) error {
	if c.PayloadSize > c.MSS-uint32(SRT_HEADER_SIZE+UDP_HEADER_SIZE) {
		return fmt.Errorf("config: PayloadSize must not be larger than %d (MSS - %d)", c.MSS-uint32(SRT_HEADER_SIZE+UDP_HEADER_SIZE), SRT_HEADER_SIZE-UDP_HEADER_SIZE)
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Bandwidth Parameters
// ════════════════════════════════════════════════════════════════════════════

func validateOverheadBW(c *Config) error {
	if c.OverheadBW < 10 || c.OverheadBW > 100 {
		return fmt.Errorf("config: OverheadBW must be between 10 and 100")
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Encryption Parameters
// ════════════════════════════════════════════════════════════════════════════

func validateKMPreAnnounce(c *Config) error {
	if c.KMRefreshRate != 0 {
		if c.KMPreAnnounce < 1 || c.KMPreAnnounce > c.KMRefreshRate/2 {
			return fmt.Errorf("config: KMPreAnnounce must be greater than 1 and smaller than KMRefreshRate/2")
		}
	}
	return nil
}

func validatePassphrase(c *Config) error {
	if len(c.Passphrase) != 0 {
		if len(c.Passphrase) < MIN_PASSPHRASE_SIZE || len(c.Passphrase) > MAX_PASSPHRASE_SIZE {
			return fmt.Errorf("config: Passphrase must be between %d and %d bytes long", MIN_PASSPHRASE_SIZE, MAX_PASSPHRASE_SIZE)
		}
	}
	return nil
}

func validatePBKeylen(c *Config) error {
	if c.PBKeylen != 16 && c.PBKeylen != 24 && c.PBKeylen != 32 {
		return fmt.Errorf("config: PBKeylen must be 16, 24, or 32 bytes")
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Latency Parameters
// ════════════════════════════════════════════════════════════════════════════

func validatePeerLatency(c *Config) error {
	if c.PeerLatency < 0 {
		return fmt.Errorf("config: PeerLatency must be greater than 0")
	}
	return nil
}

func validateReceiverLatency(c *Config) error {
	if c.ReceiverLatency < 0 {
		return fmt.Errorf("config: ReceiverLatency must be greater than 0")
	}
	return nil
}

func validateSendDropDelay(c *Config) error {
	if c.SendDropDelay < 0 {
		return fmt.Errorf("config: SendDropDelay must be greater than 0")
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Stream Parameters
// ════════════════════════════════════════════════════════════════════════════

func validateStreamId(c *Config) error {
	if len(c.StreamId) > MAX_STREAMID_SIZE {
		return fmt.Errorf("config: StreamId must be shorter than or equal to %d bytes", MAX_STREAMID_SIZE)
	}
	return nil
}

func validatePacketFilter(c *Config) error {
	if len(c.PacketFilter) != 0 {
		return fmt.Errorf("config: PacketFilter are not supported")
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// io_uring Send Configuration
// ════════════════════════════════════════════════════════════════════════════

func validateIoUringSendConfig(c *Config) error {
	if c.IoUringEnabled {
		if c.IoUringSendRingSize < 16 || c.IoUringSendRingSize > 1024 {
			return fmt.Errorf("config: IoUringSendRingSize must be between 16 and 1024")
		}
		// Check if ring size is a power of 2
		if c.IoUringSendRingSize&(c.IoUringSendRingSize-1) != 0 {
			return fmt.Errorf("config: IoUringSendRingSize must be a power of 2")
		}
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// io_uring Receive Configuration
// ════════════════════════════════════════════════════════════════════════════

func validateIoUringRecvRingSize(c *Config) error {
	if c.IoUringRecvRingSize > 0 {
		if c.IoUringRecvRingSize&(c.IoUringRecvRingSize-1) != 0 {
			return fmt.Errorf("config: IoUringRecvRingSize must be a power of 2")
		}
		if c.IoUringRecvRingSize < 64 || c.IoUringRecvRingSize > 32768 {
			return fmt.Errorf("config: IoUringRecvRingSize must be between 64 and 32768")
		}
	}
	return nil
}

func validateIoUringRecvInitialPending(c *Config) error {
	if c.IoUringRecvInitialPending > 0 {
		if c.IoUringRecvInitialPending < 16 || c.IoUringRecvInitialPending > 32768 {
			return fmt.Errorf("config: IoUringRecvInitialPending must be between 16 and 32768")
		}
		if c.IoUringRecvRingSize > 0 && c.IoUringRecvInitialPending > c.IoUringRecvRingSize {
			return fmt.Errorf("config: IoUringRecvInitialPending (%d) must not exceed IoUringRecvRingSize (%d)",
				c.IoUringRecvInitialPending, c.IoUringRecvRingSize)
		}
	}
	return nil
}

func validateIoUringRecvBatchSize(c *Config) error {
	if c.IoUringRecvBatchSize > 0 {
		if c.IoUringRecvBatchSize < 1 || c.IoUringRecvBatchSize > 32768 {
			return fmt.Errorf("config: IoUringRecvBatchSize must be between 1 and 32768")
		}
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Lock-Free Ring Buffer Configuration (Phase 3)
// ════════════════════════════════════════════════════════════════════════════

func validatePacketRingConfig(c *Config) error {
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
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// EventLoop Configuration (Phase 4)
// ════════════════════════════════════════════════════════════════════════════

func validateEventLoopConfig(c *Config) error {
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
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// NAK Btree Parameters
// ════════════════════════════════════════════════════════════════════════════

func validateNakRecentPercent(c *Config) error {
	if c.NakRecentPercent < 0 || c.NakRecentPercent > 1.0 {
		return fmt.Errorf("config: NakRecentPercent must be between 0.0 and 1.0 (default: 0.10)")
	}
	return nil
}

func validateFastNakThreshold(c *Config) error {
	if c.FastNakEnabled && c.FastNakThresholdMs == 0 {
		return fmt.Errorf("config: FastNakThresholdMs must be > 0 when FastNakEnabled (default: 50)")
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// ACK Optimization (Phase 5)
// ════════════════════════════════════════════════════════════════════════════

func validateLightACKDifference(c *Config) error {
	if c.LightACKDifference > 5000 {
		return fmt.Errorf("config: LightACKDifference must be <= 5000, got %d", c.LightACKDifference)
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Table-Driven Validation Orchestrator
// ════════════════════════════════════════════════════════════════════════════

// validateWithTable runs all validators from the table.
// Returns the first error encountered, or nil if all validations pass.
func validateWithTable(c *Config) error {
	for _, v := range configValidators {
		if err := v.Validate(c); err != nil {
			return err
		}
	}
	return nil
}

// GetConfigValidatorCount returns the number of validators for testing.
func GetConfigValidatorCount() int {
	return len(configValidators)
}
