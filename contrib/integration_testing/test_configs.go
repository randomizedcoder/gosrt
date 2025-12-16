package main

import "time"

// TestConfigs defines all test configurations for integration testing
var TestConfigs = []TestConfig{
	// ========== Basic Bandwidth Tests ==========
	{
		Name:            "Default-1Mbps",
		Description:     "Default configuration at 1 Mb/s",
		Bitrate:         1_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
	},
	{
		Name:            "Default-2Mbps",
		Description:     "Default configuration at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
	},
	{
		Name:            "Default-5Mbps",
		Description:     "Default configuration at 5 Mb/s",
		Bitrate:         5_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
	},
	{
		Name:            "Default-10Mbps",
		Description:     "Default configuration at 10 Mb/s",
		Bitrate:         10_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
	},

	// ========== Buffer Size Tests ==========
	{
		Name:            "SmallBuffers-2Mbps",
		Description:     "Small buffers (120ms latency) at 2 Mb/s - tests minimal latency",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &SmallBuffersSRTConfig,
	},
	{
		Name:            "LargeBuffers-2Mbps",
		Description:     "Large buffers (3s latency) at 2 Mb/s - tests high-loss resilience",
		Bitrate:         2_000_000,
		TestDuration:    15 * time.Second, // Longer duration for larger buffers
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig,
	},

	// ========== Packet Reordering Algorithm Tests ==========
	{
		Name:            "BTree-2Mbps",
		Description:     "B-tree packet reordering at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &BTreeSRTConfig,
	},
	{
		Name:            "List-2Mbps",
		Description:     "List-based packet reordering at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &ListSRTConfig,
	},

	// ========== io_uring Tests ==========
	{
		Name:            "IoUring-2Mbps",
		Description:     "io_uring enabled at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &IoUringSRTConfig,
	},
	{
		Name:            "IoUring-10Mbps",
		Description:     "io_uring enabled at 10 Mb/s - tests high throughput",
		Bitrate:         10_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &IoUringSRTConfig,
	},

	// ========== Combined Configuration Tests ==========
	{
		Name:            "IoUring-LargeBuffers-BTree-10Mbps",
		Description:     "io_uring + large buffers + B-tree at 10 Mb/s - high performance config",
		Bitrate:         10_000_000,
		TestDuration:    15 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT: &SRTConfig{
			ConnectionTimeout:      3000 * time.Millisecond,
			PeerIdleTimeout:        30000 * time.Millisecond,
			RecvLatency:            3000 * time.Millisecond,
			PeerLatency:            3000 * time.Millisecond,
			IoUringEnabled:         true,
			IoUringRecvEnabled:     true,
			PacketReorderAlgorithm: "btree",
			TLPktDrop:              true,
		},
	},

	// ========== Component-Specific Configuration Tests ==========
	{
		Name:            "AsymmetricLatency-2Mbps",
		Description:     "Server and client with different latency settings",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		Server: ComponentConfig{
			SRT: SRTConfig{
				RecvLatency: 500 * time.Millisecond,
				PeerLatency: 500 * time.Millisecond,
				TLPktDrop:   true,
			},
		},
		ClientGenerator: ComponentConfig{
			SRT: SRTConfig{
				RecvLatency: 1000 * time.Millisecond,
				PeerLatency: 1000 * time.Millisecond,
				TLPktDrop:   true,
			},
		},
		Client: ComponentConfig{
			SRT: SRTConfig{
				RecvLatency: 1000 * time.Millisecond,
				PeerLatency: 1000 * time.Millisecond,
				TLPktDrop:   true,
			},
		},
	},

	// ========== io_uring Output Tests (Client-side) ==========
	// These tests validate the client's io_uring output writer
	// (uses unsafe package for zero-copy writes to stdout/file)
	{
		Name:            "IoUringOutput-2Mbps",
		Description:     "Client with io_uring output enabled at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		Client: ComponentConfig{
			IoUringOutput: true, // Enable io_uring for client output
		},
	},
	{
		Name:            "IoUringOutput-10Mbps",
		Description:     "Client with io_uring output enabled at 10 Mb/s - high throughput",
		Bitrate:         10_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		Client: ComponentConfig{
			IoUringOutput: true, // Enable io_uring for client output
		},
	},

	// ========== Full io_uring Path Tests ==========
	// These test io_uring for both SRT send/recv AND client output
	{
		Name:            "FullIoUring-2Mbps",
		Description:     "Full io_uring path: SRT send/recv + client output at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &IoUringSRTConfig, // io_uring for SRT
		Client: ComponentConfig{
			IoUringOutput: true, // io_uring for client output
		},
	},
	{
		Name:            "FullIoUring-10Mbps",
		Description:     "Full io_uring path: SRT send/recv + client output at 10 Mb/s",
		Bitrate:         10_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &IoUringSRTConfig, // io_uring for SRT
		Client: ComponentConfig{
			IoUringOutput: true, // io_uring for client output
		},
	},

	// ========== High Performance Config ==========
	{
		Name:            "HighPerf-10Mbps",
		Description:     "Maximum performance: io_uring everywhere + B-tree + large buffers at 10 Mb/s",
		Bitrate:         10_000_000,
		TestDuration:    15 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT: &SRTConfig{
			ConnectionTimeout:      3000 * time.Millisecond,
			PeerIdleTimeout:        30000 * time.Millisecond,
			RecvLatency:            3000 * time.Millisecond,
			PeerLatency:            3000 * time.Millisecond,
			IoUringEnabled:         true,
			IoUringRecvEnabled:     true,
			PacketReorderAlgorithm: "btree",
			TLPktDrop:              true,
		},
		Client: ComponentConfig{
			IoUringOutput: true, // io_uring for client output
		},
	},
}

// ============================================================================
// NETWORK IMPAIRMENT TEST CONFIGURATIONS
// ============================================================================
// These tests run in network namespace mode with controlled packet loss
// and latency. They require root privileges and are NOT included in the
// default TestConfigs slice.
//
// Run with: sudo ./integration_testing graceful-shutdown-sigint-config <name>

// NetworkTestConfigs contains test configurations that use network namespaces
// with controlled impairment (loss, latency, patterns).
var NetworkTestConfigs = []TestConfig{
	// ========== Basic Loss Tests (No Latency) ==========
	{
		Name:        "Network-Loss2pct-5Mbps",
		Description: "2% packet loss at 5 Mb/s - basic ARQ validation",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.02, // 2% loss
			LatencyProfile: "none",
		},
		Bitrate:         5_000_000,
		TestDuration:    30 * time.Second, // Longer duration for loss recovery
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig, // Large buffers for loss recovery
	},
	{
		Name:        "Network-Loss5pct-5Mbps",
		Description: "5% packet loss at 5 Mb/s - moderate loss recovery",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.05, // 5% loss
			LatencyProfile: "none",
		},
		Bitrate:         5_000_000,
		TestDuration:    30 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig,
	},
	{
		Name:        "Network-Loss10pct-5Mbps",
		Description: "10% packet loss at 5 Mb/s - heavy loss recovery",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.10, // 10% loss
			LatencyProfile: "none",
		},
		Bitrate:         5_000_000,
		TestDuration:    30 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig,
	},

	// ========== High Performance Loss Tests (io_uring + btree) ==========
	// These tests use maximum performance paths to investigate NAK handling issues
	{
		Name:        "Network-Loss2pct-1Mbps-HighPerf",
		Description: "2% loss with io_uring + btree - Defect 8 investigation",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.02, // 2% loss (same as baseline)
			LatencyProfile: "none",
		},
		Bitrate:         1_000_000,
		TestDuration:    30 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT: &SRTConfig{
			ConnectionTimeout:      3000 * time.Millisecond,
			PeerIdleTimeout:        30000 * time.Millisecond,
			Latency:                3000 * time.Millisecond,
			RecvLatency:            3000 * time.Millisecond,
			PeerLatency:            3000 * time.Millisecond,
			IoUringEnabled:         true,    // io_uring for SRT send
			IoUringRecvEnabled:     true,    // io_uring for SRT recv
			PacketReorderAlgorithm: "btree", // btree for faster packet lookup
			BTreeDegree:            32,
			TLPktDrop:              true,
		},
		Client: ComponentConfig{
			IoUringOutput: true, // io_uring for client output
		},
	},
	{
		Name:        "Network-Loss2pct-1Mbps-NoIoUring",
		Description: "2% loss WITHOUT io_uring - verify fix works for traditional path",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.02, // 2% loss
			LatencyProfile: "none",
		},
		Bitrate:         1_000_000,
		TestDuration:    30 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT: &SRTConfig{
			ConnectionTimeout:      3000 * time.Millisecond,
			PeerIdleTimeout:        30000 * time.Millisecond,
			Latency:                3000 * time.Millisecond,
			RecvLatency:            3000 * time.Millisecond,
			PeerLatency:            3000 * time.Millisecond,
			IoUringEnabled:         false,  // NO io_uring - use traditional WriteTo
			IoUringRecvEnabled:     false,  // NO io_uring - use traditional ReadFrom
			PacketReorderAlgorithm: "list", // list for baseline comparison
			TLPktDrop:              true,
		},
		Client: ComponentConfig{
			IoUringOutput: false, // NO io_uring for client output
		},
	},

	// ========== High Performance Loss Tests (io_uring + btree) ==========
	// These tests use maximum performance paths to investigate NAK handling issues
	{
		Name:        "Network-Loss2pct-5Mbps-HighPerf",
		Description: "2% loss with io_uring + btree - Defect 8 investigation",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.02, // 2% loss (same as baseline)
			LatencyProfile: "none",
		},
		Bitrate:         5_000_000,
		TestDuration:    30 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT: &SRTConfig{
			ConnectionTimeout:      3000 * time.Millisecond,
			PeerIdleTimeout:        30000 * time.Millisecond,
			Latency:                3000 * time.Millisecond,
			RecvLatency:            3000 * time.Millisecond,
			PeerLatency:            3000 * time.Millisecond,
			IoUringEnabled:         true,    // io_uring for SRT send
			IoUringRecvEnabled:     true,    // io_uring for SRT recv
			PacketReorderAlgorithm: "btree", // btree for faster packet lookup
			BTreeDegree:            32,
			TLPktDrop:              true,
		},
		Client: ComponentConfig{
			IoUringOutput: true, // io_uring for client output
		},
	},

	// ========== Latency + Loss Tests ==========
	{
		Name:        "Network-Regional-Loss2pct-5Mbps",
		Description: "10ms RTT + 2% loss at 5 Mb/s - regional network with light loss",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.02,
			LatencyProfile: "regional",
		},
		Bitrate:         5_000_000,
		TestDuration:    30 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig,
	},
	{
		Name:        "Network-Continental-Loss2pct-5Mbps",
		Description: "60ms RTT + 2% loss at 5 Mb/s - continental network with light loss",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.02,
			LatencyProfile: "continental",
		},
		Bitrate:         5_000_000,
		TestDuration:    30 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig,
	},
	{
		Name:        "Network-Intercontinental-Loss5pct-5Mbps",
		Description: "130ms RTT + 5% loss at 5 Mb/s - intercontinental with moderate loss",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "intercontinental",
			Thresholds:     ptrTo(HighLatencyThresholds()),
		},
		Bitrate:         5_000_000,
		TestDuration:    45 * time.Second, // Longer for high latency
		ConnectionWait:  5 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 3 * time.Second,
		SharedSRT:       &ExtraLargeBuffersSRTConfig, // Need extra large buffers
	},
	{
		Name:        "Network-GeoSatellite-Loss2pct-2Mbps",
		Description: "300ms RTT + 2% loss at 2 Mb/s - GEO satellite simulation",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.02,
			LatencyProfile: "geo-satellite",
			Thresholds:     ptrTo(HighLatencyThresholds()),
		},
		Bitrate:         2_000_000, // Lower bitrate for high latency
		TestDuration:    60 * time.Second,
		ConnectionWait:  10 * time.Second, // Long connection wait for 600ms RTT
		MetricsEnabled:  true,
		CollectInterval: 5 * time.Second,
		SharedSRT:       &ExtraLargeBuffersSRTConfig,
	},

	// ========== Pattern-Based Impairment Tests ==========
	// Starlink simulates LEO satellite reconvergence events: 60ms total outages
	// occurring 4 times per minute at 12s, 27s, 42s, 57s
	{
		Name:        "Network-Starlink-5Mbps",
		Description: "Starlink reconvergence pattern (60ms 100% loss at 12,27,42,57s) at 5 Mb/s",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional", // LEO satellite has low latency normally
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second, // Run for 1.5 minutes to see multiple events
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig,
	},
	{
		Name:        "Network-Starlink-20Mbps",
		Description: "Starlink reconvergence pattern at 20 Mb/s - higher throughput stress",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig,
	},
	{
		Name:        "Network-Starlink-5Mbps-HighPerf",
		Description: "Starlink pattern at 5 Mb/s with io_uring + btree optimizations",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT: &SRTConfig{
			ConnectionTimeout:      3000 * time.Millisecond,
			PeerIdleTimeout:        30000 * time.Millisecond,
			Latency:                3000 * time.Millisecond,
			RecvLatency:            3000 * time.Millisecond,
			PeerLatency:            3000 * time.Millisecond,
			IoUringEnabled:         true,
			IoUringRecvEnabled:     true,
			PacketReorderAlgorithm: "btree",
			BTreeDegree:            32,
			TLPktDrop:              true,
		},
		Client: ComponentConfig{
			IoUringOutput: true,
		},
	},
	{
		Name:        "Network-Starlink-20Mbps-HighPerf",
		Description: "Starlink pattern at 20 Mb/s with io_uring + btree - max stress",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT: &SRTConfig{
			ConnectionTimeout:      3000 * time.Millisecond,
			PeerIdleTimeout:        30000 * time.Millisecond,
			Latency:                3000 * time.Millisecond,
			RecvLatency:            3000 * time.Millisecond,
			PeerLatency:            3000 * time.Millisecond,
			IoUringEnabled:         true,
			IoUringRecvEnabled:     true,
			PacketReorderAlgorithm: "btree",
			BTreeDegree:            32,
			TLPktDrop:              true,
		},
		Client: ComponentConfig{
			IoUringOutput: true,
		},
	},
	{
		Name:        "Network-HighLossBurst-5Mbps",
		Description: "High loss burst pattern (85% loss for 1s every minute) at 5 Mb/s",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			Pattern:        "high-loss",
			LatencyProfile: "none",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &LargeBuffersSRTConfig,
	},

	// ========== NAK btree Network Tests ==========
	// These tests validate NAK btree loss recovery behavior
	// Key metrics to verify: FastNakTriggers, NakBtreeInserts, NakBtreeDeletes
	{
		Name:        "Network-Loss2pct-5Mbps-NakBtree",
		Description: "2% loss with NAK btree - verify loss recovery with all NAK btree features",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.02, // 2% loss
			LatencyProfile: "none",
		},
		Bitrate:         5_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &HighPerfSRTConfig, // Full NAK btree + io_uring + btree
	},
	{
		Name:        "Network-Starlink-5Mbps-NakBtree",
		Description: "Starlink pattern with NAK btree - tests FastNAK triggers during outages",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second, // 1.5 minutes for multiple Starlink events
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &HighPerfSRTConfig, // Full NAK btree + io_uring + btree
	},
	{
		Name:        "Network-Starlink-20Mbps-NakBtree",
		Description: "Starlink pattern at 20 Mb/s with NAK btree - high throughput stress test",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &HighPerfSRTConfig, // Full NAK btree + io_uring + btree
	},

	// ========== Stress Tests ==========
	{
		Name:        "Network-Stress-HighLatencyHighLoss",
		Description: "130ms RTT + 10% loss at 10 Mb/s - extreme stress test",
		Mode:        TestModeNetwork,
		Impairment: NetworkImpairment{
			LossRate:       0.10,
			LatencyProfile: "intercontinental",
			Thresholds:     ptrTo(StressTestThresholds()),
		},
		Bitrate:         10_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  5 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 3 * time.Second,
		SharedSRT:       &ExtraLargeBuffersSRTConfig,
	},
}

// ptrTo returns a pointer to the given StatisticalThresholds value
func ptrTo(t StatisticalThresholds) *StatisticalThresholds {
	return &t
}

// ExtraLargeBuffersSRTConfig provides 5-second buffers for high-latency scenarios
var ExtraLargeBuffersSRTConfig = SRTConfig{
	ConnectionTimeout: 10000 * time.Millisecond, // 10 second connection timeout
	PeerIdleTimeout:   60000 * time.Millisecond, // 60 second idle timeout
	RecvLatency:       5000 * time.Millisecond,  // 5 second receive latency
	PeerLatency:       5000 * time.Millisecond,  // 5 second peer latency
	FC:                51200,                    // 51200 packets flow control
	RecvBuf:           4 * 1024 * 1024,          // 4 MB receive buffer
	SendBuf:           4 * 1024 * 1024,          // 4 MB send buffer
	TLPktDrop:         true,
}

// GetTestConfigByName finds a test configuration by name
func GetTestConfigByName(name string) *TestConfig {
	for i := range TestConfigs {
		if TestConfigs[i].Name == name {
			return &TestConfigs[i]
		}
	}
	return nil
}

// GetNetworkTestConfigByName finds a network test configuration by name
func GetNetworkTestConfigByName(name string) *TestConfig {
	for i := range NetworkTestConfigs {
		if NetworkTestConfigs[i].Name == name {
			return &NetworkTestConfigs[i]
		}
	}
	return nil
}

// ============================================================================
// PARALLEL COMPARISON TEST CONFIGURATIONS
// ============================================================================
// These tests run two pipelines (Baseline + HighPerf) in parallel for
// direct comparison under identical network conditions.
//
// Run with: sudo make test-parallel CONFIG=<name>
// Profile with: sudo make test-parallel-profile CONFIG=<name> PROFILES=cpu,heap

// ParallelTestConfigs contains parallel comparison test configurations
var ParallelTestConfigs = []ParallelTestConfig{
	{
		Name:        "Parallel-Starlink-5Mbps",
		Description: "Parallel comparison: Starlink pattern at 5 Mb/s (Baseline vs HighPerf)",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          BaselineSRTConfig,
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          HighPerfSRTConfig,
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},
	{
		Name:        "Parallel-Starlink-20Mbps",
		Description: "Parallel comparison: Starlink pattern at 20 Mb/s (Baseline vs HighPerf)",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          BaselineSRTConfig,
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          HighPerfSRTConfig,
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},
	{
		Name:        "Parallel-Loss2pct-5Mbps",
		Description: "Parallel comparison: 2% probabilistic loss at 5 Mb/s",
		Impairment: NetworkImpairment{
			LossRate:       0.02,
			LatencyProfile: "regional",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          BaselineSRTConfig,
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          HighPerfSRTConfig,
		},
		Bitrate:         5_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ========================================================================
	// NAK BTREE PERMUTATION PARALLEL TESTS (Starlink Pattern)
	// ========================================================================
	// These tests compare feature permutations under Starlink outage pattern.
	// FastNAK features should show improvement in outage recovery.
	// See nak_btree_integration_testing.md for details.

	// Compare: NAK btree only vs NAK btree + FastNAK
	// Expected: FastNAK shows faster recovery after 60ms outages
	{
		Name:        "Parallel-Starlink-FastNak-Impact",
		Description: "Starlink: NAK btree only vs NAK btree + FastNAK (measure FastNAK impact)",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          ControlSRTConfig.WithNakBtreeOnly().WithIoUringRecv(), // NAK btree only
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          ControlSRTConfig.WithNakBtreeOnly().WithFastNak().WithIoUringRecv(), // + FastNAK
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Compare: NAK btree + FastNAK vs NAK btree + FastNAK + FastNAKRecent
	// Expected: FastNAKRecent detects sequence jumps after outages
	{
		Name:        "Parallel-Starlink-FastNakRecent-Impact",
		Description: "Starlink: FastNAK vs FastNAK + FastNAKRecent (measure sequence jump detection)",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          ControlSRTConfig.WithNakBtreeOnly().WithFastNak().WithIoUringRecv(), // FastNAK only
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          ControlSRTConfig.WithNakBtreeOnly().WithFastNak().WithFastNakRecent().WithIoUringRecv(), // + FastNAKRecent
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Compare: Baseline (list, no optimizations) vs Full NAK btree stack
	// Expected: Full NAK btree outperforms Baseline in outage recovery
	{
		Name:        "Parallel-Starlink-Full-NakBtree",
		Description: "Starlink: Baseline (list) vs Full NAK btree (all features)",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          BaselineSRTConfig, // Original: list, no io_uring
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          HighPerfSRTConfig, // Full: io_uring + btree + NAK btree + all features
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},
}

// GetParallelTestConfigByName finds a parallel test configuration by name
func GetParallelTestConfigByName(name string) *ParallelTestConfig {
	for i := range ParallelTestConfigs {
		if ParallelTestConfigs[i].Name == name {
			return &ParallelTestConfigs[i]
		}
	}
	return nil
}

// ============================================================================
// ISOLATION TEST CONFIGURATIONS
// ============================================================================
// These tests run simplified CG→Server pairs to isolate which component/feature
// causes performance differences. No Client, no network impairment, 30s tests.
//
// Control pipeline: list + no io_uring (baseline behavior)
// Test pipeline: exactly ONE variable changed from control
//
// Run with: sudo make test-isolation CONFIG=<name>
// Run all:  sudo make test-isolation-all

// IsolationTestConfigs contains isolation test configurations
var IsolationTestConfigs = []IsolationTestConfig{
	// Test 0: Control-Control (sanity check - both identical)
	{
		Name:          "Isolation-Control",
		Description:   "Sanity check: both pipelines identical (should show 0 difference)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig, // Same as control
		TestServer:    ControlSRTConfig, // Same as control
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second, // Reduce output frequency
	},

	// Test 1: CG io_uring send only
	{
		Name:          "Isolation-CG-IoUringSend",
		Description:   "Client-Generator: io_uring send path only",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithIoUringSend(), // Changed: io_uring send
		TestServer:    ControlSRTConfig,
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 2: CG io_uring recv only
	{
		Name:          "Isolation-CG-IoUringRecv",
		Description:   "Client-Generator: io_uring recv path only (for ACKs/NAKs)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithIoUringRecv(), // Changed: io_uring recv
		TestServer:    ControlSRTConfig,
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 3: CG btree packet store
	{
		Name:          "Isolation-CG-Btree",
		Description:   "Client-Generator: btree packet store (instead of list)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithBtree(32), // Changed: btree
		TestServer:    ControlSRTConfig,
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 4: Server io_uring send only
	{
		Name:          "Isolation-Server-IoUringSend",
		Description:   "Server: io_uring send path only",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithIoUringSend(), // Changed: io_uring send
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 5: Server io_uring recv only
	{
		Name:          "Isolation-Server-IoUringRecv",
		Description:   "Server: io_uring recv path only",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithIoUringRecv(), // Changed: io_uring recv
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
		// LogTopics:     "listen:io_uring:completion:seq", // Debug: uncomment to log sequence numbers
	},

	// Test 6: Server btree packet store
	{
		Name:          "Isolation-Server-Btree",
		Description:   "Server: btree packet store (instead of list)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithBtree(32), // Changed: btree
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========================================================================
	// NAK BTREE ISOLATION TESTS
	// ========================================================================
	// These tests isolate the new NAK btree mechanism vs the default.
	// NAK btree is primarily a RECEIVER feature (gap detection, NAK generation).
	// HonorNakOrder is a SENDER feature (processes NAKs in receiver's priority order).

	// Test 7: Server NAK btree (receiver side - core feature)
	{
		Name:          "Isolation-Server-NakBtree",
		Description:   "Server: NAK btree for gap detection (replaces lossList scan)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithNakBtree(), // Changed: NAK btree + all features
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 8: Server NAK btree + io_uring recv (realistic high-perf receiver)
	{
		Name:          "Isolation-Server-NakBtree-IoUringRecv",
		Description:   "Server: NAK btree + io_uring recv (combined receiver path)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithNakBtree().WithIoUringRecv(), // NAK btree + io_uring recv
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 8b: Server NAK btree + io_uring recv + 50% NakRecentPercent
	// This tests whether a larger "too recent" window fixes false gap detection
	// caused by io_uring's out-of-order packet delivery
	{
		Name:          "Isolation-Server-NakBtree-IoUringRecv-LargeWindow",
		Description:   "Server: NAK btree + io_uring recv + 50% recent window (debug test)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithNakBtree().WithIoUringRecv().WithNakRecentPercent(0.50), // 50% instead of 10%
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 9: CG HonorNakOrder (sender side - processes NAKs in order)
	{
		Name:          "Isolation-CG-HonorNakOrder",
		Description:   "Client-Generator: HonorNakOrder (retransmits in NAK packet order)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithHonorNakOrder(), // Changed: honor NAK order
		TestServer:    ControlSRTConfig,
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 10: Full NAK btree pipeline (Server NAK btree + CG HonorNakOrder)
	{
		Name:          "Isolation-FullNakBtree",
		Description:   "Full NAK btree: Server(NAK btree) + CG(HonorNakOrder)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithHonorNakOrder(), // Sender honors NAK order
		TestServer:    ControlSRTConfig.WithNakBtree(),      // Receiver uses NAK btree
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========================================================================
	// NAK BTREE PERMUTATION TESTS
	// ========================================================================
	// These tests isolate the impact of each NAK btree sub-feature.
	// Run on clean network to observe feature behavior without loss.
	// See nak_btree_integration_testing.md for the permutation matrix.

	// Permutation #1: NAK btree only (no FastNAK, no HonorNakOrder)
	{
		Name:          "Isolation-NakBtree-Only",
		Description:   "NAK btree only, no FastNAK, no HonorNakOrder (permutation #1)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithNakBtreeOnly().WithIoUringRecv(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Permutation #2: NAK btree + FastNAK only
	{
		Name:          "Isolation-NakBtree-FastNak",
		Description:   "NAK btree + FastNAK, no FastNAKRecent, no HonorNakOrder (permutation #2)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithNakBtreeOnly().WithFastNak().WithIoUringRecv(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Permutation #3: NAK btree + FastNAK + FastNAKRecent
	{
		Name:          "Isolation-NakBtree-FastNakRecent",
		Description:   "NAK btree + FastNAK + FastNAKRecent, no HonorNakOrder (permutation #3)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithNakBtreeOnly().WithFastNak().WithFastNakRecent().WithIoUringRecv(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Permutation #4: NAK btree + HonorNakOrder (no FastNAK)
	{
		Name:          "Isolation-NakBtree-HonorNakOrder",
		Description:   "NAK btree + HonorNakOrder, no FastNAK (permutation #4)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithHonorNakOrder(),                  // Sender honors NAK order
		TestServer:    ControlSRTConfig.WithNakBtreeOnly().WithIoUringRecv(), // Receiver: NAK btree only
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Permutation #5: NAK btree + FastNAK + HonorNakOrder (no FastNAKRecent)
	{
		Name:          "Isolation-NakBtree-FastNak-HonorNakOrder",
		Description:   "NAK btree + FastNAK + HonorNakOrder, no FastNAKRecent (permutation #5)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithHonorNakOrder(),                                // Sender honors NAK order
		TestServer:    ControlSRTConfig.WithNakBtreeOnly().WithFastNak().WithIoUringRecv(), // Receiver: NAK btree + FastNAK
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test 11: Full HighPerf with NAK btree (io_uring + btree + NAK btree + HonorNakOrder)
	{
		Name:          "Isolation-FullHighPerf-NakBtree",
		Description:   "Full HighPerf: io_uring send/recv + btree + NAK btree + HonorNakOrder",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithIoUringSend().WithIoUringRecv().WithBtree(32).WithHonorNakOrder(),
		TestServer:    ControlSRTConfig.WithIoUringSend().WithIoUringRecv().WithBtree(32).WithNakBtree(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},
}

// GetIsolationTestConfigByName finds an isolation test configuration by name
func GetIsolationTestConfigByName(name string) *IsolationTestConfig {
	for i := range IsolationTestConfigs {
		if IsolationTestConfigs[i].Name == name {
			return &IsolationTestConfigs[i]
		}
	}
	return nil
}
