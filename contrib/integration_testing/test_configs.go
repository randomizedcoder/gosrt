package main

import "time"

// TestConfigs defines all test configurations for integration testing
var TestConfigs = []TestConfig{
	// ========== Basic Bandwidth Tests ==========
	{
		Name:            "Int-Clean-1M-5s-Base",
		LegacyName:      "Default-1Mbps",
		Description:     "Default configuration at 1 Mb/s",
		Bitrate:         1_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
	},
	{
		Name:            "Int-Clean-2M-5s-Base",
		LegacyName:      "Default-2Mbps",
		Description:     "Default configuration at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
	},
	{
		Name:            "Int-Clean-5M-5s-Base",
		LegacyName:      "Default-5Mbps",
		Description:     "Default configuration at 5 Mb/s",
		Bitrate:         5_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
	},
	{
		Name:            "Int-Clean-10M-5s-Base",
		LegacyName:      "Default-10Mbps",
		Description:     "Default configuration at 10 Mb/s",
		Bitrate:         10_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
	},

	// ========== Buffer Size Tests ==========
	{
		Name:            "Int-Clean-2M-120ms-Base",
		LegacyName:      "SmallBuffers-2Mbps",
		Description:     "Small buffers (120ms latency) at 2 Mb/s - tests minimal latency",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &SmallBuffersSRTConfig,
	},
	{
		Name:            "Int-Clean-2M-3s-Base",
		LegacyName:      "LargeBuffers-2Mbps",
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
		Name:            "Int-Clean-2M-5s-Btree",
		LegacyName:      "BTree-2Mbps",
		Description:     "B-tree packet reordering at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &BTreeSRTConfig,
	},
	{
		Name:            "Int-Clean-2M-5s-List",
		LegacyName:      "List-2Mbps",
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
		Name:            "Int-Clean-2M-5s-IoUr",
		LegacyName:      "IoUring-2Mbps",
		Description:     "io_uring enabled at 2 Mb/s",
		Bitrate:         2_000_000,
		TestDuration:    10 * time.Second,
		ConnectionWait:  2 * time.Second,
		MetricsEnabled:  true,
		CollectInterval: 2 * time.Second,
		SharedSRT:       &IoUringSRTConfig,
	},
	{
		Name:            "Int-Clean-10M-5s-IoUr",
		LegacyName:      "IoUring-10Mbps",
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
		Name:            "Int-Clean-10M-3s-IoUrBtree",
		LegacyName:      "IoUring-LargeBuffers-BTree-10Mbps",
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
		Name:            "Int-Clean-2M-Asymmetric",
		LegacyName:      "AsymmetricLatency-2Mbps",
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
		Name:            "Int-Clean-2M-5s-IoUrOut",
		LegacyName:      "IoUringOutput-2Mbps",
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
		Name:            "Int-Clean-10M-5s-IoUrOut",
		LegacyName:      "IoUringOutput-10Mbps",
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
		Name:            "Int-Clean-2M-5s-FullIoUr",
		LegacyName:      "FullIoUring-2Mbps",
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
		Name:            "Int-Clean-10M-5s-FullIoUr",
		LegacyName:      "FullIoUring-10Mbps",
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
		Name:            "Int-Clean-10M-3s-Full",
		LegacyName:      "HighPerf-10Mbps",
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
		Name:        "Network-Loss-2pct-5M-Base",
		LegacyName:  "Network-Loss2pct-5Mbps",
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
		Name:        "Network-Loss-5pct-5M-Base",
		LegacyName:  "Network-Loss5pct-5Mbps",
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
		Name:        "Network-Loss-10pct-5M-Base",
		LegacyName:  "Network-Loss10pct-5Mbps",
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
		Name:        "Network-Loss-2pct-1M-Full",
		LegacyName:  "Network-Loss2pct-1Mbps-HighPerf",
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
		Name:        "Network-Loss-2pct-1M-NoIoUr",
		LegacyName:  "Network-Loss2pct-1Mbps-NoIoUring",
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
		Name:        "Network-Loss-2pct-5M-Full",
		LegacyName:  "Network-Loss2pct-5Mbps-HighPerf",
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
		Name:        "Network-Regional-2pct-5M-R10-Base",
		LegacyName:  "Network-Regional-Loss2pct-5Mbps",
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
		Name:        "Network-Continental-2pct-5M-R60-Base",
		LegacyName:  "Network-Continental-Loss2pct-5Mbps",
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
		Name:        "Network-Intercont-5pct-5M-R130-Base",
		LegacyName:  "Network-Intercontinental-Loss5pct-5Mbps",
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
		Name:        "Network-GeoSat-2pct-2M-R300-Base",
		LegacyName:  "Network-GeoSatellite-Loss2pct-2Mbps",
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
		Name:        "Network-Starlink-5M-Base",
		LegacyName:  "Network-Starlink-5Mbps",
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
		Name:        "Network-Starlink-20M-Base",
		LegacyName:  "Network-Starlink-20Mbps",
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
		Name:        "Network-Starlink-5M-Full",
		LegacyName:  "Network-Starlink-5Mbps-HighPerf",
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
		Name:        "Network-Starlink-20M-Full",
		LegacyName:  "Network-Starlink-20Mbps-HighPerf",
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
		Name:        "Network-HighLoss-5M-Base",
		LegacyName:  "Network-HighLossBurst-5Mbps",
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
		Name:        "Network-Loss-2pct-5M-NakBtree",
		LegacyName:  "Network-Loss2pct-5Mbps-NakBtree",
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
		Name:        "Network-Starlink-5M-NakBtree",
		LegacyName:  "Network-Starlink-5Mbps-NakBtree",
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
		Name:        "Network-Starlink-20M-NakBtree",
		LegacyName:  "Network-Starlink-20Mbps-NakBtree",
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
		Name:        "Network-Stress-10pct-10M-R130",
		LegacyName:  "Network-Stress-HighLatencyHighLoss",
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
	// First try to match by Name (new standardized name)
	for i := range TestConfigs {
		if TestConfigs[i].Name == name {
			return &TestConfigs[i]
		}
	}
	// Fall back to LegacyName for backward compatibility
	for i := range TestConfigs {
		if TestConfigs[i].LegacyName == name {
			return &TestConfigs[i]
		}
	}
	return nil
}

// GetNetworkTestConfigByName finds a network test configuration by name
func GetNetworkTestConfigByName(name string) *TestConfig {
	// First try to match by Name (new standardized name)
	for i := range NetworkTestConfigs {
		if NetworkTestConfigs[i].Name == name {
			return &NetworkTestConfigs[i]
		}
	}
	// Fall back to LegacyName for backward compatibility
	for i := range NetworkTestConfigs {
		if NetworkTestConfigs[i].LegacyName == name {
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
		Name:        "Parallel-Starlink-5M-Base-vs-Full",
		LegacyName:  "Parallel-Starlink-5Mbps",
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
		Name:        "Parallel-Starlink-20M-Base-vs-Full",
		LegacyName:  "Parallel-Starlink-20Mbps",
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
		Name:        "Parallel-Loss-2pct-5M-Base-vs-Full",
		LegacyName:  "Parallel-Loss2pct-5Mbps",
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
		Name:        "Parallel-Starlink-5M-NakBtree-vs-NakBtreeF",
		LegacyName:  "Parallel-Starlink-FastNak-Impact",
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
		Name:        "Parallel-Starlink-5M-NakBtreeF-vs-NakBtreeFr",
		LegacyName:  "Parallel-Starlink-FastNakRecent-Impact",
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
		Name:        "Parallel-Starlink-5M-Base-vs-NakBtreeFr",
		LegacyName:  "Parallel-Starlink-Full-NakBtree",
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

	// ========================================================================
	// PHASE 3: LOCK-FREE RING BUFFER PARALLEL TESTS
	// ========================================================================
	// These tests compare lock-free ring buffer configurations.
	// The ring buffer enables lockless packet handoff from io_uring to receiver.
	// See gosrt_lockless_design.md Phase 3 for details.

	// Compare: Baseline (list, no optimizations) vs Ring only
	// Expected: Ring shows reduced lock contention, minimal overhead
	{
		Name:        "Parallel-Starlink-5M-Base-vs-Ring",
		Description: "Phase 3: Baseline vs Lock-free Ring (isolate ring impact)",
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
			SRT:          BaselineSRTConfig, // Original: list, no io_uring, no ring
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigRing), // Ring only (list + ring)
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Compare: Full (io_uring + btree + NAK btree) vs FullRing (Full + Ring)
	// Expected: FullRing shows improved lock-free performance
	{
		Name:        "Parallel-Starlink-5M-Full-vs-FullRing",
		Description: "Phase 3: Full stack vs Full + Lock-free Ring (measure ring benefit)",
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
			SRT:          HighPerfSRTConfig, // Full: io_uring + btree + NAK btree + all features
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullRing), // Full + Ring buffer
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Compare: Baseline vs FullRing (complete Phase 3 lockless stack)
	// Expected: FullRing shows maximum improvement over baseline
	{
		Name:        "Parallel-Starlink-5M-Base-vs-FullRing",
		Description: "Phase 3: Baseline vs Full Lockless Stack (maximum improvement)",
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
			SRT:          BaselineSRTConfig, // Original: list, no optimizations
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullRing), // Full lockless: io_uring + btree + NAK btree + Ring
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// High-throughput test: 20 Mb/s with Full Lockless stack
	// Expected: Ring shows more benefit at higher packet rates
	{
		Name:        "Parallel-Starlink-20M-Base-vs-FullRing",
		Description: "Phase 3: 20 Mb/s - Baseline vs Full Lockless Stack (high-rate stress test)",
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
			SRT:          GetSRTConfig(ConfigFullRing),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ============================================================================
	// Phase 4: Event Loop Parallel Comparisons
	// ============================================================================

	// Compare Ring (Tick) vs Ring+EventLoop
	// Expected: EventLoop shows lower latency, smoother CPU
	{
		Name:        "Parallel-Starlink-5M-Ring-vs-EventLoop",
		Description: "Phase 4: Ring+Tick vs Ring+EventLoop (measure event loop benefit)",
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
			SRT:          GetSRTConfig(ConfigRing), // Ring with Tick()
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigEventLoop), // Ring with EventLoop
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Compare FullRing (Tick) vs FullEventLoop
	// Expected: FullEventLoop shows maximum Phase 4 improvement
	{
		Name:        "Parallel-Starlink-5M-FullRing-vs-FullEventLoop",
		Description: "Phase 4: Full+Ring+Tick vs Full+Ring+EventLoop (measure event loop benefit)",
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
			SRT:          GetSRTConfig(ConfigFullRing), // Full stack with Tick()
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop), // Full stack with EventLoop
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Ultimate comparison: Baseline vs Full Lockless + EventLoop
	// Expected: Maximum improvement across all phases
	{
		Name:        "Parallel-Starlink-5M-Base-vs-FullEventLoop",
		Description: "Phase 4: Baseline vs Full Lockless Pipeline (Phases 1-4 combined)",
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
			SRT:          BaselineSRTConfig, // Original: list, no optimizations
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop), // Full lockless: all optimizations
		},
		Bitrate:         5_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// High-rate event loop stress test (Full Ring vs Full EventLoop)
	{
		Name:        "Parallel-Starlink-20M-FullRing-vs-FullEventLoop",
		Description: "Phase 4: 20 Mb/s - Full+Ring vs Full+EventLoop (high-rate event loop test)",
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
			SRT:          GetSRTConfig(ConfigFullRing),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Ultimate comparison at high rate: Baseline vs Full Lockless + EventLoop
	// Expected: Tests whether the lockless pipeline scales to 20 Mb/s
	{
		Name:        "Parallel-Starlink-20M-Base-vs-FullEventLoop",
		Description: "Phase 4: 20 Mb/s - Baseline vs Full Lockless Pipeline (high-rate stress test)",
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
			SRT:          BaselineSRTConfig, // Original: list, no optimizations
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop), // Full lockless: all optimizations
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ========== High Throughput Parallel Tests (Phase 5: Find Performance Limits) ==========

	// 50 Mb/s comparison: Baseline vs Full Lockless + EventLoop
	{
		Name:        "Parallel-Starlink-50M-Base-vs-FullEventLoop",
		Description: "Phase 5: 50 Mb/s - Baseline vs Full Lockless Pipeline (high-throughput test)",
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
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         50_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// 100 Mb/s comparison: Baseline vs Full Lockless + EventLoop
	{
		Name:        "Parallel-Starlink-100M-Base-vs-FullEventLoop",
		Description: "Phase 5: 100 Mb/s - Baseline vs Full Lockless Pipeline (extreme throughput test)",
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
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         100_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// 50 Mb/s clean (no impairment): Find raw throughput limit
	{
		Name:        "Parallel-Clean-50M-Base-vs-FullEventLoop",
		Description: "Phase 5: 50 Mb/s Clean - No impairment, raw throughput comparison",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
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
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         50_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// 100 Mb/s clean (no impairment): Find raw throughput limit
	{
		Name:        "Parallel-Clean-100M-Base-vs-FullEventLoop",
		Description: "Phase 5: 100 Mb/s Clean - No impairment, raw throughput comparison",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
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
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         100_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// 400 Mb/s clean (no impairment): Push beyond design target
	{
		Name:        "Parallel-Clean-400M-Base-vs-FullEventLoop",
		Description: "Phase 5: 400 Mb/s Clean - No impairment, extreme throughput test",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
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
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         400_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ========== EventLoop with RTT Variation Tests (Phase 4: Network Latency) ==========

	// EventLoop with 60ms RTT (cross-continental)
	{
		Name:        "Parallel-Starlink-20M-R60-Base-vs-FullEL",
		Description: "Phase 4: EventLoop with 60ms RTT (cross-continental)",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "continental",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// EventLoop with 130ms RTT (intercontinental)
	{
		Name:        "Parallel-Starlink-20M-R130-Base-vs-FullEL",
		Description: "Phase 4: EventLoop with 130ms RTT (intercontinental)",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "intercontinental",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    120 * time.Second, // Longer for high RTT
		ConnectionWait:  5 * time.Second,   // Longer connection wait
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// EventLoop with 300ms RTT (GEO satellite)
	{
		Name:        "Parallel-Starlink-20M-R300-Base-vs-FullEL",
		Description: "Phase 4: EventLoop with 300ms RTT (GEO satellite)",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LatencyProfile: "geo_satellite",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    150 * time.Second, // Longer for GEO RTT
		ConnectionWait:  10 * time.Second,  // Much longer connection wait
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ========== EventLoop with Loss Impairment Tests (Phase 4: Loss Recovery) ==========

	// EventLoop with 5% packet loss at 5 Mb/s (basic test)
	{
		Name:        "Parallel-Loss-L5-5M-Base-vs-FullEL",
		Description: "Phase 4: EventLoop with 5% packet loss at 5 Mb/s (basic test)",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "regional",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         5_000_000, // 5 Mb/s - lower throughput basic test
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// EventLoop with 5% packet loss at 20 Mb/s
	{
		Name:        "Parallel-Loss-L5-20M-Base-vs-FullEL",
		Description: "Phase 4: EventLoop with 5% packet loss at 20 Mb/s",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "regional",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// EventLoop with 10% packet loss (stress test)
	{
		Name:        "Parallel-Loss-L10-20M-Base-vs-FullEL",
		Description: "Phase 4: EventLoop with 10% packet loss (stress test)",
		Impairment: NetworkImpairment{
			LossRate:       0.10,
			LatencyProfile: "regional",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// EventLoop: FullRing (Phase 3) vs FullEL (Phase 4) under Starlink + 5% loss
	{
		Name:        "Parallel-Starlink-L5-20M-FullRing-vs-FullEL",
		Description: "Phase 4: Compare Ring (Tick) vs EventLoop under Starlink + 5% loss",
		Impairment: NetworkImpairment{
			Pattern:        "starlink",
			LossRate:       0.05,
			LatencyProfile: "regional",
			Thresholds:     ptrTo(BurstLossThresholds()),
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigFullRing), // Phase 3: Full + Ring (Tick mode)
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop), // Phase 4: Full + EventLoop
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ============================================================================
	// LATENCY PROFILE VARIATIONS
	// ============================================================================
	// Test loss recovery across different latency profiles (continental, intercontinental, GEO)

	// Continental latency (60ms RTT) + 5% loss
	{
		Name:        "Parallel-Loss-L5-20M-Base-vs-FullEL-Continental",
		Description: "EventLoop with 5% loss at continental latency (60ms RTT)",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "continental",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Continental latency (60ms RTT) + 10% loss
	{
		Name:        "Parallel-Loss-L10-20M-Base-vs-FullEL-Continental",
		Description: "EventLoop with 10% loss at continental latency (60ms RTT)",
		Impairment: NetworkImpairment{
			LossRate:       0.10,
			LatencyProfile: "continental",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Intercontinental latency (130ms RTT) + 5% loss
	{
		Name:        "Parallel-Loss-L5-20M-Base-vs-FullEL-Intercontinental",
		Description: "EventLoop with 5% loss at intercontinental latency (130ms RTT)",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "intercontinental",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Intercontinental latency (130ms RTT) + 10% loss (stress test)
	{
		Name:        "Parallel-Loss-L10-20M-Base-vs-FullEL-Intercontinental",
		Description: "EventLoop with 10% loss at intercontinental latency (130ms RTT) - stress test",
		Impairment: NetworkImpairment{
			LossRate:       0.10,
			LatencyProfile: "intercontinental",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// GEO satellite latency (300ms RTT) + 2% loss (realistic satellite conditions)
	{
		Name:        "Parallel-Loss-L2-20M-Base-vs-FullEL-GEO",
		Description: "EventLoop with 2% loss at GEO satellite latency (300ms RTT)",
		Impairment: NetworkImpairment{
			LossRate:       0.02,
			LatencyProfile: "geo-satellite",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    120 * time.Second, // Longer for GEO due to high RTT
		ConnectionWait:  5 * time.Second,   // Longer wait for GEO connections
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// GEO satellite latency (300ms RTT) + 5% loss (challenging satellite conditions)
	{
		Name:        "Parallel-Loss-L5-20M-Base-vs-FullEL-GEO",
		Description: "EventLoop with 5% loss at GEO satellite latency (300ms RTT) - challenging",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "geo-satellite",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    120 * time.Second, // Longer for GEO due to high RTT
		ConnectionWait:  5 * time.Second,   // Longer wait for GEO connections
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// No latency (0ms) + 10% loss - baseline comparison without latency effects
	{
		Name:        "Parallel-Loss-L10-20M-Base-vs-FullEL-NoLatency",
		Description: "EventLoop with 10% loss at zero latency - isolates loss handling",
		Impairment: NetworkImpairment{
			LossRate:       0.10,
			LatencyProfile: "none",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ============================================================================
	// NAK SUPPRESSION DEBUG TESTS (Section 6 of retransmission_and_nak_suppression_design.md)
	// ============================================================================
	// These tests are designed to isolate and observe NAK suppression behavior.
	// Low bitrate (1 Mb/s) for easier packet-level analysis.
	// High RTT (130ms) causes multiple NAKs per gap (130ms / 20ms NAK interval = 6.5 NAKs/gap).

	// Debug: 1 Mb/s, 130ms RTT, 5% loss - observe excessive retransmissions
	{
		Name:        "Parallel-Debug-L5-1M-R130-Base-vs-FullEL",
		Description: "DEBUG: 1 Mb/s with 130ms RTT and 5% loss - observe NAK/retrans ratio",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "intercontinental", // 130ms RTT (closest to design's 100ms)
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         1_000_000, // 1 Mb/s - low rate for easier analysis
		TestDuration:    60 * time.Second,
		ConnectionWait:  5 * time.Second,
		CollectInterval: 5 * time.Second, // 5s stats period for cleaner output
		ProfileDuration: 5 * time.Minute,
	},

	// Debug: 1 Mb/s, 300ms RTT, 5% loss - extreme case (300ms / 20ms = 15 NAKs/gap)
	{
		Name:        "Parallel-Debug-L5-1M-R300-Base-vs-FullEL",
		Description: "DEBUG: 1 Mb/s with 300ms RTT and 5% loss - extreme NAK/retrans ratio",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "geo-satellite", // 300ms RTT
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         1_000_000,
		TestDuration:    90 * time.Second, // Longer for GEO latency
		ConnectionWait:  10 * time.Second,
		CollectInterval: 5 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Debug: 1 Mb/s, no latency, 5% loss - isolate loss from RTT effects
	{
		Name:        "Parallel-Debug-L5-1M-NoRTT-Base-vs-FullEL",
		Description: "DEBUG: 1 Mb/s with 0ms RTT and 5% loss - isolate loss handling",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "none", // 0ms RTT
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		Bitrate:         1_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 5 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ============================================================================
	// LOCKLESS SENDER PARALLEL TESTS (Phase 5+)
	// ============================================================================
	// These tests compare the new lockless sender implementation against
	// the baseline sender. Key metrics to observe:
	// - SendRingPushed/Dropped: Ring buffer utilization
	// - SendBtreeInserted/Duplicates: Btree operations
	// - SendEventLoopIterations: EventLoop activity
	// - SendDeliveryPackets: Packets delivered
	// - Packets/Iteration ratio: Burst detection (lower = smoother)
	// See lockless_sender_design.md Section 11 for expected metrics.

	// Compare: Baseline (locking sender) vs Sender Btree only
	// Expected: Btree shows O(log n) NAK lookup improvement
	{
		Name:        "Parallel-Clean-20M-Base-vs-SendBtree",
		Description: "Lockless Sender Phase 1: Baseline vs SendPacketBtree (O(log n) NAK lookup)",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigSendBtree),
		},
		Bitrate:         20_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Compare: Baseline vs Sender Btree + Data Ring
	// Expected: Lock-free Push() reduces contention
	{
		Name:        "Parallel-Clean-20M-Base-vs-SendRing",
		Description: "Lockless Sender Phase 2: Baseline vs SendPacketBtree + SendRing",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigSendRing),
		},
		Bitrate:         20_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Compare: Baseline vs Full Sender EventLoop
	// Expected: Smoother delivery (lower Packets/Iteration ratio)
	{
		Name:        "Parallel-Clean-20M-Base-vs-SendEL",
		Description: "Lockless Sender Phase 4: Baseline vs Full Sender EventLoop",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigSendEL),
		},
		Bitrate:         20_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Compare: Full Receiver EventLoop vs Full Receiver + Full Sender EventLoop
	// Expected: Maximum lockless performance
	{
		Name:        "Parallel-Clean-20M-FullEL-vs-FullSendEL",
		Description: "Ultimate: Receiver EventLoop vs Full Lockless (Receiver + Sender EventLoop)",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigFullEventLoop),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullSendEL),
		},
		Bitrate:         20_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// High throughput test: 50 Mb/s Baseline vs Full Sender EventLoop
	{
		Name:        "Parallel-Clean-50M-Base-vs-SendEL",
		Description: "Lockless Sender: 50 Mb/s Baseline vs Full Sender EventLoop",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigSendEL),
		},
		Bitrate:         50_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Extreme throughput test: 100 Mb/s Baseline vs Full Lockless (Receiver + Sender)
	{
		Name:        "Parallel-Clean-100M-Base-vs-FullSendEL",
		Description: "Ultimate: 100 Mb/s Baseline vs Full Lockless (Receiver + Sender EventLoop)",
		Impairment: NetworkImpairment{
			Pattern:        "none",
			LatencyProfile: "none",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullSendEL),
		},
		Bitrate:         100_000_000,
		TestDuration:    60 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// Starlink test with Sender EventLoop: Test loss recovery
	{
		Name:        "Parallel-Starlink-20M-Base-vs-SendEL",
		Description: "Lockless Sender: Starlink pattern at 20 Mb/s with Sender EventLoop",
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
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigSendEL),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// 5% loss test with Sender EventLoop
	{
		Name:        "Parallel-Loss-L5-20M-Base-vs-SendEL",
		Description: "Lockless Sender: 5% loss at 20 Mb/s with Sender EventLoop",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "regional",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigSendEL),
		},
		Bitrate:         20_000_000,
		TestDuration:    90 * time.Second,
		ConnectionWait:  3 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// GEO satellite with Full Lockless (Receiver + Sender) - 5% loss
	{
		Name:        "Parallel-Loss-L5-20M-Base-vs-FullSendEL-GEO",
		Description: "Ultimate: 5% loss at GEO (300ms RTT) with Full Lockless (Receiver + Sender)",
		Impairment: NetworkImpairment{
			LossRate:       0.05,
			LatencyProfile: "geo-satellite",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullSendEL),
		},
		Bitrate:         20_000_000,
		TestDuration:    120 * time.Second,
		ConnectionWait:  5 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// GEO satellite with Full Lockless (Receiver + Sender) - 10% loss
	{
		Name:        "Parallel-Loss-L10-20M-Base-vs-FullSendEL-GEO",
		Description: "Extreme: 10% loss at GEO (300ms RTT) with Full Lockless (Receiver + Sender)",
		Impairment: NetworkImpairment{
			LossRate:       0.10,
			LatencyProfile: "geo-satellite",
		},
		Baseline: PipelineConfig{
			PublisherIP:  "10.1.1.2",
			ServerIP:     "10.2.1.2",
			SubscriberIP: "10.1.2.2",
			ServerPort:   6000,
			StreamID:     "test-stream-baseline",
			SRT:          GetSRTConfig(ConfigBase),
		},
		HighPerf: PipelineConfig{
			PublisherIP:  "10.1.1.3",
			ServerIP:     "10.2.1.3",
			SubscriberIP: "10.1.2.3",
			ServerPort:   6001,
			StreamID:     "test-stream-highperf",
			SRT:          GetSRTConfig(ConfigFullSendEL),
		},
		Bitrate:         20_000_000,
		TestDuration:    120 * time.Second,
		ConnectionWait:  5 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},

	// ========================================================================
	// FFMPEG PUBLISHER PARALLEL TESTS
	// ========================================================================
	// These tests exercise the real-world ingest path:
	// ffmpeg (testsrc MPEG-TS) -> UDP -> client-udp -> SRT -> server -> SRT -> client
	// 4 processes per pipeline x 2 pipelines = 8 processes total.

	{
		Name:          "Parallel-FFmpeg-Clean-5M-Base-vs-Full",
		Description:   "FFmpeg->client-udp: Baseline vs HighPerf at 5 Mb/s (clean network)",
		PublisherType: PublisherTypeFFmpegUDP,
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
		ConnectionWait:  5 * time.Second,
		CollectInterval: 2 * time.Second,
		ProfileDuration: 5 * time.Minute,
	},
}

// GetParallelTestConfigByName finds a parallel test configuration by name
func GetParallelTestConfigByName(name string) *ParallelTestConfig {
	// First try to match by Name (new standardized name)
	for i := range ParallelTestConfigs {
		if ParallelTestConfigs[i].Name == name {
			return &ParallelTestConfigs[i]
		}
	}
	// Fall back to LegacyName for backward compatibility
	for i := range ParallelTestConfigs {
		if ParallelTestConfigs[i].LegacyName == name {
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
		Name:          "Isolation-5M-Control",
		LegacyName:    "Isolation-Control",
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
		Name:          "Isolation-5M-CG-IoUrSend",
		LegacyName:    "Isolation-CG-IoUringSend",
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
		Name:          "Isolation-5M-CG-IoUrRecv",
		LegacyName:    "Isolation-CG-IoUringRecv",
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
		Name:          "Isolation-5M-CG-Btree",
		LegacyName:    "Isolation-CG-Btree",
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
		Name:          "Isolation-5M-Server-IoUrSend",
		LegacyName:    "Isolation-Server-IoUringSend",
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
		Name:          "Isolation-5M-Server-IoUrRecv",
		LegacyName:    "Isolation-Server-IoUringRecv",
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
		Name:          "Isolation-5M-Server-Btree",
		LegacyName:    "Isolation-Server-Btree",
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
		Name:          "Isolation-5M-Server-NakBtree",
		LegacyName:    "Isolation-Server-NakBtree",
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
		Name:          "Isolation-5M-Server-NakBtree-IoUr",
		LegacyName:    "Isolation-Server-NakBtree-IoUringRecv",
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
		Name:          "Isolation-5M-Server-NakBtree-IoUr-50pct",
		LegacyName:    "Isolation-Server-NakBtree-IoUringRecv-LargeWindow",
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
		Name:          "Isolation-5M-CG-HonorNakOrder",
		LegacyName:    "Isolation-CG-HonorNakOrder",
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
		Name:          "Isolation-5M-FullNakBtree",
		LegacyName:    "Isolation-FullNakBtree",
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
		Name:          "Isolation-5M-NakBtree-Only",
		LegacyName:    "Isolation-NakBtree-Only",
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
		Name:          "Isolation-5M-NakBtreeF",
		LegacyName:    "Isolation-NakBtree-FastNak",
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
		Name:          "Isolation-5M-NakBtreeFr",
		LegacyName:    "Isolation-NakBtree-FastNakRecent",
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
		Name:          "Isolation-5M-NakBtree-HonorNak",
		LegacyName:    "Isolation-NakBtree-HonorNakOrder",
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
		Name:          "Isolation-5M-NakBtreeF-HonorNak",
		LegacyName:    "Isolation-NakBtree-FastNak-HonorNakOrder",
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
		Name:          "Isolation-5M-Full",
		LegacyName:    "Isolation-FullHighPerf-NakBtree",
		Description:   "Full HighPerf: io_uring send/recv + btree + NAK btree + HonorNakOrder",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithIoUringSend().WithIoUringRecv().WithBtree(32).WithHonorNakOrder(),
		TestServer:    ControlSRTConfig.WithIoUringSend().WithIoUringRecv().WithBtree(32).WithNakBtree(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========================================================================
	// HIGH BITRATE DEBUG TESTS (50 Mb/s)
	// ========================================================================
	// These tests are for debugging the 50 Mb/s performance issue documented
	// in integration_testing_50mbps_defect.md
	{
		Name:          "Isolation-50M-Full",
		Description:   "50 Mb/s Full HighPerf: io_uring send/recv + btree + NAK btree + HonorNakOrder (DEBUGGING)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithIoUringSend().WithIoUringRecv().WithBtree(32).WithHonorNakOrder(),
		TestServer:    ControlSRTConfig.WithIoUringSend().WithIoUringRecv().WithBtree(32).WithNakBtree(),
		TestDuration:  60 * time.Second, // Longer for profiling
		Bitrate:       50_000_000,       // 50 Mb/s
		StatsPeriod:   10 * time.Second,
	},
	{
		Name:          "Isolation-50M-NakBtree",
		Description:   "50 Mb/s NAK btree only (no io_uring send) for comparison (DEBUGGING)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,                                  // No io_uring on CG
		TestServer:    ControlSRTConfig.WithNakBtree().WithIoUringRecv(), // NAK btree + io_uring recv
		TestDuration:  60 * time.Second,
		Bitrate:       50_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========================================================================
	// PHASE 3: LOCK-FREE RING BUFFER ISOLATION TESTS
	// ========================================================================
	// These tests isolate the lock-free ring buffer feature.
	// The ring enables lockless packet handoff from io_uring to receiver.

	// Test: Server Ring buffer only (baseline server with ring)
	{
		Name:          "Isolation-5M-Server-Ring",
		Description:   "Server: Lock-free ring buffer only (isolate ring overhead)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithPacketRing(), // Changed: ring buffer only
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Server Ring + io_uring recv (combines lockless recv with lockless handoff)
	{
		Name:          "Isolation-5M-Server-Ring-IoUr",
		Description:   "Server: Ring + io_uring recv (lockless receive + lockless handoff)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithPacketRing().WithIoUringRecv(), // Ring + io_uring recv
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Server Ring + NAK btree + io_uring recv (high-perf receiver stack)
	{
		Name:          "Isolation-5M-Server-Ring-NakBtree-IoUr",
		Description:   "Server: Ring + NAK btree + io_uring (full lockless receiver)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithPacketRing().WithNakBtree().WithIoUringRecv(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Full lockless pipeline (io_uring + btree + NAK btree + Ring + HonorNakOrder)
	{
		Name:          "Isolation-5M-FullRing",
		Description:   "Full Phase 3 Lockless: io_uring + btree + NAK btree + Ring + HonorNakOrder",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullRing).WithHonorNakOrder(), // Full + ring + honor NAK order
		TestServer:    GetSRTConfig(ConfigFullRing),                     // Full + ring
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: High throughput 20 Mb/s with full lockless stack
	{
		Name:          "Isolation-20M-FullRing",
		Description:   "20 Mb/s Full Lockless: stress test lock-free ring at higher rate",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullRing).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullRing),
		TestDuration:  30 * time.Second,
		Bitrate:       20_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ============================================================================
	// Phase 4: Event Loop Tests
	// ============================================================================

	// Test: Event loop only (ring + event loop on base config)
	{
		Name:          "Isolation-5M-EventLoop",
		Description:   "Phase 4 Event Loop: Ring + continuous event loop (default settings)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigEventLoop),
		TestServer:    GetSRTConfig(ConfigEventLoop),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Full lockless pipeline with event loop
	{
		Name:          "Isolation-5M-FullEventLoop",
		Description:   "Full Phase 4 Lockless: io_uring + btree + NAK btree + Ring + EventLoop + HonorNakOrder",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Debug: Full lockless pipeline with verbose metrics (short duration)
	{
		Name:           "Isolation-5M-FullEventLoop-Debug",
		Description:    "DEBUG: Full EventLoop with verbose metrics, ACK/ACKACK debug logging, short duration",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithReceiverDebug().WithLogTopics("receiver,control:send:ACK,control:recv:ACKACK"),
		TestServer:     GetSRTConfig(ConfigFullEventLoop).WithReceiverDebug().WithLogTopics("receiver,control:send:ACK,control:recv:ACKACK"),
		TestDuration:   10 * time.Second,
		Bitrate:        5_000_000,
		StatsPeriod:    2 * time.Second,
		VerboseMetrics: true,
	},

	// Test: Server-only EventLoop to diagnose RTT issue
	// Hypothesis: CG's EventLoop is delaying ACK processing, causing high RTT
	{
		Name:           "Isolation-5M-ServerEventLoop-Only",
		Description:    "DEBUG: EventLoop ONLY on server (not CG) to diagnose RTT issue",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         ControlSRTConfig.WithHonorNakOrder().WithLogTopics("control:send:ACK,control:recv:ACKACK"), // Standard config, NO EventLoop
		TestServer:     GetSRTConfig(ConfigFullEventLoop).WithReceiverDebug().WithLogTopics("receiver,control:send:ACK,control:recv:ACKACK"),
		TestDuration:   10 * time.Second,
		Bitrate:        5_000_000,
		StatsPeriod:    2 * time.Second,
		VerboseMetrics: true,
	},

	// Test: CG-only EventLoop to diagnose RTT issue
	// If Server-only has good RTT but CG-only has bad RTT → Problem is on CG side
	{
		Name:           "Isolation-5M-CGEventLoop-Only",
		Description:    "DEBUG: EventLoop ONLY on CG (not server) to diagnose RTT issue",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithLogTopics("control:send:ACK,control:recv:ACKACK"), // EventLoop enabled
		TestServer:     ControlSRTConfig.WithReceiverDebug().WithLogTopics("receiver,control:send:ACK,control:recv:ACKACK"),         // Standard config, NO EventLoop
		TestDuration:   10 * time.Second,
		Bitrate:        5_000_000,
		StatsPeriod:    2 * time.Second,
		VerboseMetrics: true,
	},

	// Test: EventLoop WITHOUT io_uring - isolate io_uring as RTT cause
	// Hypothesis: io_uring send batching may be causing RTT inflation
	{
		Name:           "Isolation-5M-EventLoop-NoIOUring",
		Description:    "DEBUG: EventLoop enabled but io_uring disabled - test if io_uring causes RTT",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         ControlSRTConfig.WithHonorNakOrder(),                                           // No EventLoop, no io_uring
		TestServer:     ControlSRTConfig.WithBtree(32).WithNakBtree().WithPacketRing().WithEventLoop(), // EventLoop but NO io_uring
		TestDuration:   10 * time.Second,
		Bitrate:        5_000_000,
		StatsPeriod:    2 * time.Second,
		VerboseMetrics: true,
	},

	// Test: High throughput 20 Mb/s with full event loop stack
	{
		Name:          "Isolation-20M-FullEventLoop",
		Description:   "20 Mb/s Full Phase 4 Lockless: stress test event loop at higher rate",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop),
		TestDuration:  30 * time.Second,
		Bitrate:       20_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Event loop with aggressive backoff (for low-traffic scenarios)
	{
		Name:          "Isolation-5M-EventLoop-LowBackoff",
		Description:   "Event loop with shorter backoff (5µs-500µs) for lower latency",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithPacketRing().WithEventLoopCustom(1*time.Second, 500, 5*time.Microsecond, 500*time.Microsecond),
		TestServer:    ControlSRTConfig.WithPacketRing().WithEventLoopCustom(1*time.Second, 500, 5*time.Microsecond, 500*time.Microsecond),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Event loop with relaxed backoff (for CPU efficiency)
	{
		Name:          "Isolation-5M-EventLoop-HighBackoff",
		Description:   "Event loop with longer backoff (50µs-5ms) for CPU efficiency",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithPacketRing().WithEventLoopCustom(1*time.Second, 2000, 50*time.Microsecond, 5*time.Millisecond),
		TestServer:    ControlSRTConfig.WithPacketRing().WithEventLoopCustom(1*time.Second, 2000, 50*time.Microsecond, 5*time.Millisecond),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========== High Throughput Tests (Phase 5: Find Performance Limits) ==========

	// Test: 50 Mb/s with full event loop stack
	{
		Name:          "Isolation-50M-FullEventLoop",
		Description:   "50 Mb/s Full Phase 4 Lockless: high-throughput stress test",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop),
		TestDuration:  60 * time.Second, // Longer duration for stability
		Bitrate:       50_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: 100 Mb/s with full event loop stack
	{
		Name:          "Isolation-100M-FullEventLoop",
		Description:   "100 Mb/s Full Phase 4 Lockless: extreme throughput test",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop),
		TestDuration:  60 * time.Second,
		Bitrate:       100_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: 150 Mb/s with full event loop stack (push the limits)
	{
		Name:          "Isolation-150M-FullEventLoop",
		Description:   "150 Mb/s Full Phase 4 Lockless: find throughput ceiling",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop),
		TestDuration:  60 * time.Second,
		Bitrate:       150_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: 200 Mb/s with full event loop stack (target from design doc)
	{
		Name:          "Isolation-200M-FullEventLoop",
		Description:   "200 Mb/s Full Phase 4 Lockless: design document target",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop),
		TestDuration:  60 * time.Second,
		Bitrate:       200_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: 400 Mb/s with full event loop stack (beyond design target)
	{
		Name:          "Isolation-400M-FullEventLoop",
		Description:   "400 Mb/s Full Phase 4 Lockless: push beyond design target",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop),
		TestDuration:  60 * time.Second,
		Bitrate:       400_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: 400 Mb/s with LARGE io_uring ring (debug: test ring overflow hypothesis)
	{
		Name:          "Isolation-400M-FullEventLoop-LargeRing",
		Description:   "400 Mb/s with 8192-entry io_uring ring (debug: test ring overflow hypothesis)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       400_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: 300 Mb/s to find exact throughput ceiling
	{
		Name:          "Isolation-300M-FullEventLoop",
		Description:   "300 Mb/s Full Phase 4 Lockless: find exact throughput ceiling",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullEventLoop),
		TestDuration:  60 * time.Second,
		Bitrate:       300_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ============================================================================
	// Phase 5: Ring Retry Strategy Tests
	// Compare different retry strategies to find optimal for io_uring throughput
	// ============================================================================

	// Baseline: SleepBackoff (current default)
	{
		Name:          "Isolation-5M-Strategy-Sleep",
		Description:   "Ring Strategy: SleepBackoff (baseline, current default)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithRetryStrategy("sleep"),
		TestServer:    GetSRTConfig(ConfigFullEventLoop).WithRetryStrategy("sleep"),
		TestDuration:  12 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// NextShard: Try all shards before sleeping
	{
		Name:          "Isolation-5M-Strategy-Next",
		Description:   "Ring Strategy: NextShard (try all shards before sleeping)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithRetryStrategy("next"),
		TestServer:    GetSRTConfig(ConfigFullEventLoop).WithRetryStrategy("next"),
		TestDuration:  12 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// RandomShard: Random shard selection (fastest in ring benchmarks!)
	{
		Name:          "Isolation-5M-Strategy-Random",
		Description:   "Ring Strategy: RandomShard (fastest in ring benchmarks)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithRetryStrategy("random"),
		TestServer:    GetSRTConfig(ConfigFullEventLoop).WithRetryStrategy("random"),
		TestDuration:  12 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// AdaptiveBackoff: Exponential backoff with jitter
	{
		Name:          "Isolation-5M-Strategy-Adaptive",
		Description:   "Ring Strategy: AdaptiveBackoff (exponential backoff with jitter)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithRetryStrategy("adaptive"),
		TestServer:    GetSRTConfig(ConfigFullEventLoop).WithRetryStrategy("adaptive"),
		TestDuration:  12 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// SpinThenYield: Yield CPU instead of sleep (lowest latency, highest CPU)
	{
		Name:          "Isolation-5M-Strategy-Spin",
		Description:   "Ring Strategy: SpinThenYield (lowest latency, highest CPU)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithRetryStrategy("spin"),
		TestServer:    GetSRTConfig(ConfigFullEventLoop).WithRetryStrategy("spin"),
		TestDuration:  12 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Hybrid: NextShard + AdaptiveBackoff
	{
		Name:          "Isolation-5M-Strategy-Hybrid",
		Description:   "Ring Strategy: Hybrid (NextShard + AdaptiveBackoff)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullEventLoop).WithHonorNakOrder().WithRetryStrategy("hybrid"),
		TestServer:    GetSRTConfig(ConfigFullEventLoop).WithRetryStrategy("hybrid"),
		TestDuration:  12 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// ============================================================================
	// Lockless Sender Isolation Tests (Phase 5: Sender-Specific Features)
	// These tests isolate each sender feature independently to identify issues.
	//
	// IMPORTANT: Sender features are tested on BOTH CG (publisher) and Server
	// (forwarder) to identify where issues occur in the data flow:
	//   CG → Server (publish path)
	//   Server → Client (subscribe/forward path)
	// ============================================================================

	// --------------------------------------------------------------------------
	// Phase 1: SendPacketBtree - O(log n) sender packet storage
	// Tests btree alone without rings or EventLoop
	// --------------------------------------------------------------------------

	// Test: CG uses sender btree only
	{
		Name:          "Isolation-5M-CG-SendBtree",
		Description:   "Client-Generator: Sender btree only (O(log n) NAK lookup)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithSendBtree(), // ONLY sender btree
		TestServer:    ControlSRTConfig,
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Server uses sender btree only
	{
		Name:          "Isolation-5M-Server-SendBtree",
		Description:   "Server: Sender btree only (O(log n) NAK lookup)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithSendBtree(), // ONLY sender btree
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// --------------------------------------------------------------------------
	// Phase 2: SendPacketRing - Lock-free data ring for Push()
	// Tests btree + data ring without control ring or EventLoop
	// --------------------------------------------------------------------------

	// Test: CG uses sender btree + data ring
	{
		Name:          "Isolation-5M-CG-SendRing",
		Description:   "Client-Generator: Sender btree + data ring (lock-free Push())",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithSendBtree().WithSendRing(), // btree + data ring
		TestServer:    ControlSRTConfig,
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Server uses sender btree + data ring
	{
		Name:          "Isolation-5M-Server-SendRing",
		Description:   "Server: Sender btree + data ring (lock-free Push())",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithSendBtree().WithSendRing(), // btree + data ring
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// --------------------------------------------------------------------------
	// Phase 3: SendControlRing - Lock-free ACK/NAK routing
	// Tests btree + data ring + control ring WITHOUT EventLoop (Tick() mode)
	// --------------------------------------------------------------------------

	// Test: CG uses sender btree + data ring + control ring (NO EventLoop)
	{
		Name:          "Isolation-5M-CG-SendControlRing",
		Description:   "Client-Generator: Sender btree + data ring + control ring (NO EventLoop)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithoutSendEventLoop(),
		TestServer:    ControlSRTConfig,
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Server uses sender btree + data ring + control ring (NO EventLoop)
	{
		Name:          "Isolation-5M-Server-SendControlRing",
		Description:   "Server: Sender btree + data ring + control ring (NO EventLoop)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithoutSendEventLoop(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// --------------------------------------------------------------------------
	// Phase 4: SendEventLoop - Full sender lockless path
	// Tests complete sender stack with EventLoop
	// --------------------------------------------------------------------------

	// Test: CG uses full sender EventLoop
	{
		Name:          "Isolation-5M-CG-SendEventLoop",
		Description:   "Client-Generator: Full sender EventLoop (btree + rings + EventLoop)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigSendEL), // Full sender EventLoop
		TestServer:    ControlSRTConfig,
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Server uses full sender EventLoop
	{
		Name:          "Isolation-5M-Server-SendEventLoop",
		Description:   "Server: Full sender EventLoop (btree + rings + EventLoop)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,
		TestServer:    GetSRTConfig(ConfigSendEL), // Full sender EventLoop
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Both CG and Server use full sender EventLoop
	{
		Name:          "Isolation-5M-Full-SendEventLoop",
		Description:   "Full sender EventLoop on both CG and Server",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigSendEL),
		TestServer:    GetSRTConfig(ConfigSendEL),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// --------------------------------------------------------------------------
	// Combined: Sender EventLoop + Receiver features
	// Tests interaction between sender and receiver lockless paths
	// --------------------------------------------------------------------------

	// Test: Sender EventLoop + Receiver io_uring recv
	{
		Name:          "Isolation-5M-SendEL-IoUrRecv",
		Description:   "Sender EventLoop + io_uring recv (test interaction)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigSendEL).WithIoUringRecv(),
		TestServer:    GetSRTConfig(ConfigSendEL).WithIoUringRecv(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Sender EventLoop + Receiver Ring
	{
		Name:          "Isolation-5M-SendEL-RecvRing",
		Description:   "Sender EventLoop + Receiver ring (test interaction)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigSendEL).WithPacketRing(),
		TestServer:    GetSRTConfig(ConfigSendEL).WithPacketRing(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Sender EventLoop + Full Receiver EventLoop
	{
		Name:          "Isolation-5M-SendEL-RecvEL",
		Description:   "Sender EventLoop + Receiver EventLoop (both lockless)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigSendEL).WithPacketRing().WithEventLoop(),
		TestServer:    GetSRTConfig(ConfigSendEL).WithPacketRing().WithEventLoop(),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// --------------------------------------------------------------------------
	// Sender EventLoop backoff tuning tests
	// --------------------------------------------------------------------------

	// Test: Sender EventLoop with low backoff (lower latency)
	{
		Name:          "Isolation-5M-SendEL-LowBackoff",
		Description:   "Sender EventLoop: Low backoff (5µs-500µs) for lower latency",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(5*time.Microsecond, 500*time.Microsecond, 0.9, 1000000),
		TestServer:    ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(5*time.Microsecond, 500*time.Microsecond, 0.9, 1000000),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Sender EventLoop with high backoff (CPU efficiency)
	{
		Name:          "Isolation-5M-SendEL-HighBackoff",
		Description:   "Sender EventLoop: High backoff (50µs-5ms) for CPU efficiency",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(50*time.Microsecond, 5*time.Millisecond, 0.9, 1000000),
		TestServer:    ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(50*time.Microsecond, 5*time.Millisecond, 0.9, 1000000),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// --------------------------------------------------------------------------
	// Higher bitrate sender tests
	// --------------------------------------------------------------------------

	// Test: Sender EventLoop at 20 Mb/s
	{
		Name:          "Isolation-20M-SendEventLoop",
		Description:   "20 Mb/s Sender EventLoop stress test",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigSendEL),
		TestServer:    GetSRTConfig(ConfigSendEL),
		TestDuration:  30 * time.Second,
		Bitrate:       20_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Full lockless (sender + receiver) at 20 Mb/s
	{
		Name:          "Isolation-20M-FullSendEL",
		Description:   "20 Mb/s Full Lockless (Sender + Receiver EventLoop)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullSendEL).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullSendEL),
		TestDuration:  30 * time.Second,
		Bitrate:       20_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Sender EventLoop at 50 Mb/s
	{
		Name:          "Isolation-50M-SendEventLoop",
		Description:   "50 Mb/s Sender EventLoop stress test",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigSendEL),
		TestServer:    GetSRTConfig(ConfigSendEL),
		TestDuration:  60 * time.Second,
		Bitrate:       50_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Full lockless (sender + receiver) at 50 Mb/s
	{
		Name:          "Isolation-50M-FullSendEL",
		Description:   "50 Mb/s Full Lockless (Sender + Receiver EventLoop)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullSendEL).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullSendEL),
		TestDuration:  60 * time.Second,
		Bitrate:       50_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// --------------------------------------------------------------------------
	// DEBUG: Isolate Server subscriber (forward) path
	// These tests are designed to identify the clean network bug
	// --------------------------------------------------------------------------

	// Test: CG-only sender EventLoop (Server uses Tick, verify CG->Server works)
	{
		Name:          "Isolation-5M-CGOnly-SendEL",
		Description:   "DEBUG: Sender EventLoop ONLY on CG (Server uses Tick)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigSendEL), // CG has sender EventLoop
		TestServer:    ControlSRTConfig,           // Server uses baseline (Tick)
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Server-only sender EventLoop (CG uses Tick, isolate Server forwarding)
	{
		Name:          "Isolation-5M-ServerOnly-SendEL",
		Description:   "DEBUG: Sender EventLoop ONLY on Server (CG uses Tick)",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        ControlSRTConfig,           // CG uses baseline (Tick)
		TestServer:    GetSRTConfig(ConfigSendEL), // Server has sender EventLoop
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// --------------------------------------------------------------------------
	// DEBUG: 20M SendEventLoop intermittent failure debugging
	// This test has been observed to fail intermittently with 97% packet drops
	// The failure appears to be a startup race condition where the EventLoop
	// never enters delivery mode and all packets are dropped as "too_old"
	// --------------------------------------------------------------------------

	// Test: 20M SendEventLoop with verbose output and frequent metrics
	{
		Name:           "Isolation-20M-SendEventLoop-Debug",
		Description:    "DEBUG: 20M SendEventLoop with verbose metrics, short duration for rapid iteration",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         GetSRTConfig(ConfigSendEL).WithLogTopics("sender,sender:eventloop,sender:delivery,sender:ring"),
		TestServer:     GetSRTConfig(ConfigSendEL).WithLogTopics("sender,sender:eventloop,sender:delivery,sender:ring"),
		TestDuration:   15 * time.Second, // Short duration for rapid iteration
		Bitrate:        20_000_000,
		StatsPeriod:    2 * time.Second, // Frequent stats to catch failures early
		VerboseMetrics: true,
	},

	// Test: 20M SendEventLoop with slower backoff (hypothesis: aggressive backoff causes race)
	{
		Name:           "Isolation-20M-SendEventLoop-SlowBackoff",
		Description:    "DEBUG: 20M SendEventLoop with 1ms-10ms backoff (test if aggressive backoff causes race)",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(1*time.Millisecond, 10*time.Millisecond, 0.90, 1000000),
		TestServer:     ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(1*time.Millisecond, 10*time.Millisecond, 0.90, 1000000),
		TestDuration:   15 * time.Second,
		Bitrate:        20_000_000,
		StatsPeriod:    2 * time.Second,
		VerboseMetrics: true,
	},

	// Test: 20M SendEventLoop with faster backoff (hypothesis: too-slow backoff causes race)
	{
		Name:           "Isolation-20M-SendEventLoop-FastBackoff",
		Description:    "DEBUG: 20M SendEventLoop with 10µs-100µs backoff (test if too-slow backoff causes race)",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(10*time.Microsecond, 100*time.Microsecond, 0.90, 1000000),
		TestServer:     ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(10*time.Microsecond, 100*time.Microsecond, 0.90, 1000000),
		TestDuration:   15 * time.Second,
		Bitrate:        20_000_000,
		StatsPeriod:    2 * time.Second,
		VerboseMetrics: true,
	},

	// Test: 20M SendEventLoop with no TSBPD sleep (hypothesis: TSBPD sleep causes race)
	{
		Name:           "Isolation-20M-SendEventLoop-NoTSBPD",
		Description:    "DEBUG: 20M SendEventLoop with TSBPD factor 0 (deliver immediately, no TSBPD sleep)",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(100*time.Microsecond, 1*time.Millisecond, 0.0, 1000000),
		TestServer:     ControlSRTConfig.WithSendBtree().WithSendRing().WithSendControlRing().WithSendEventLoopCustom(100*time.Microsecond, 1*time.Millisecond, 0.0, 1000000),
		TestDuration:   15 * time.Second,
		Bitrate:        20_000_000,
		StatsPeriod:    2 * time.Second,
		VerboseMetrics: true,
	},

	// ========================================================================
	// FULL SEND EVENTLOOP DEBUG TEST
	// ========================================================================
	// This test enables both receiver EventLoop and sender EventLoop with
	// verbose logging for debugging the intermittent NAK/drop issue.
	//
	// Log topics enabled:
	// - sender:eventloop:drain      - packet drain from ring to btree
	// - sender:eventloop:drain:gap  - sequence gap detection
	// - sender:eventloop:delivery   - delivery attempt start/packet/notready
	// - sender:eventloop:drop       - packet drops (too old)
	// ========================================================================

	{
		Name:           "Isolation-20M-FullSendEL-Debug",
		Description:    "DEBUG: 20M FullSendEL with verbose sender EventLoop logging for drop/NAK investigation",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         GetSRTConfig(ConfigFullSendEL).WithLogTopics("sender:eventloop:drain,sender:eventloop:delivery,sender:eventloop:drop"),
		TestServer:     GetSRTConfig(ConfigFullSendEL).WithLogTopics("sender:eventloop:drain,sender:eventloop:delivery,sender:eventloop:drop"),
		TestDuration:   15 * time.Second, // Short duration for rapid iteration
		Bitrate:        20_000_000,
		StatsPeriod:    2 * time.Second, // Frequent stats to catch failures early
		VerboseMetrics: true,
	},

	// ========================================================================
	// COMPLETELY LOCK-FREE RECEIVER TESTS (Phase 6: Receiver Control Ring)
	// ========================================================================
	// Reference: completely_lockfree_receiver.md

	// Test: Receiver control ring only at 5 Mb/s
	{
		Name:          "Isolation-5M-RecvControlRing",
		Description:   "Receiver control ring (ACKACK/KEEPALIVE lock-free) at 5 Mb/s",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigRecvControlRing).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigRecvControlRing),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Completely lock-free at 5 Mb/s
	{
		Name:          "Isolation-5M-FullELLockFree",
		Description:   "Completely lock-free (sender + receiver control rings) at 5 Mb/s",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Receiver control ring at 20 Mb/s
	{
		Name:          "Isolation-20M-RecvControlRing",
		Description:   "Receiver control ring at 20 Mb/s stress test",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigRecvControlRing).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigRecvControlRing),
		TestDuration:  30 * time.Second,
		Bitrate:       20_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Completely lock-free at 20 Mb/s
	{
		Name:          "Isolation-20M-FullELLockFree",
		Description:   "Completely lock-free at 20 Mb/s stress test",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree),
		TestDuration:  30 * time.Second,
		Bitrate:       20_000_000,
		StatsPeriod:   5 * time.Second,
	},

	// Test: Completely lock-free at 50 Mb/s
	{
		Name:          "Isolation-50M-FullELLockFree",
		Description:   "Completely lock-free at 50 Mb/s high-throughput test",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       50_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: Completely lock-free debug at 20 Mb/s
	{
		Name:           "Isolation-20M-FullELLockFree-Debug",
		Description:    "DEBUG: 20M FullELLockFree with verbose logging",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithLogTopics("sender:eventloop,receiver,control:recv:ACKACK"),
		TestServer:     GetSRTConfig(ConfigFullELLockFree).WithLogTopics("sender:eventloop,receiver,control:recv:ACKACK"),
		TestDuration:   15 * time.Second,
		Bitrate:        20_000_000,
		StatsPeriod:    2 * time.Second,
		VerboseMetrics: true,
	},

	// ========== Multi-Ring io_uring Tests (Phase 5: multi_iouring_design.md) ==========

	// Test: 2 receive rings at 5 Mb/s
	{
		Name:          "Isolation-5M-MultiRing2",
		Description:   "Multi-ring io_uring (2 recv rings) at 5 Mb/s",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: 4 receive rings at 5 Mb/s
	{
		Name:          "Isolation-5M-MultiRing4",
		Description:   "Multi-ring io_uring (4 recv rings) at 5 Mb/s",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// Test: 4 receive rings + 2 send rings at 5 Mb/s
	{
		Name:          "Isolation-5M-MultiRing4-Send2",
		Description:   "Multi-ring io_uring (4 recv + 2 send rings) at 5 Mb/s",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithParallelIoUring(4, 2),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4),
		TestDuration:  30 * time.Second,
		Bitrate:       5_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// DEBUG: Multi-ring send with verbose logging to diagnose 2-send-ring failure
	{
		Name:           "Isolation-5M-MultiRing4-Send2-Debug",
		Description:    "DEBUG: Multi-ring io_uring (4 recv + 2 send) with verbose io_uring logging",
		ControlCG:      ControlSRTConfig,
		ControlServer:  ControlSRTConfig,
		TestCG:         GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithParallelIoUring(4, 2).WithLogTopics("connection:io_uring,connection:send,connection:new"),
		TestServer:     GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLogTopics("connection:io_uring,listen:recv"),
		TestDuration:   10 * time.Second, // Shorter for debugging
		Bitrate:        5_000_000,
		StatsPeriod:    5 * time.Second,
		VerboseMetrics: true,
	},

	// Test: 4 receive rings at 50 Mb/s high-throughput
	{
		Name:          "Isolation-50M-MultiRing4",
		Description:   "Multi-ring io_uring (4 recv rings) at 50 Mb/s high-throughput",
		ControlCG:     ControlSRTConfig,
		ControlServer: ControlSRTConfig,
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       50_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========== Multi-Ring Comparison Tests ==========
	// These tests compare SINGLE-RING io_uring vs MULTI-RING io_uring
	// to measure the actual benefit of multiple rings at high throughput.

	// 100 Mb/s: Single-ring vs 2 recv rings
	{
		Name:          "Isolation-100M-Ring1-vs-Ring2",
		Description:   "100 Mb/s: Single recv ring vs 2 recv rings (measure multi-ring benefit)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithLargeIoUringRecvRing(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithLargeIoUringRecvRing(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       100_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 100 Mb/s: Single-ring vs 4 recv rings
	{
		Name:          "Isolation-100M-Ring1-vs-Ring4",
		Description:   "100 Mb/s: Single recv ring vs 4 recv rings (measure multi-ring benefit)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithLargeIoUringRecvRing(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithLargeIoUringRecvRing(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       100_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 100 Mb/s: 2 recv rings vs 4 recv rings
	{
		Name:          "Isolation-100M-Ring2-vs-Ring4",
		Description:   "100 Mb/s: 2 recv rings vs 4 recv rings (diminishing returns?)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       100_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 200 Mb/s: Single-ring vs 2 recv rings
	{
		Name:          "Isolation-200M-Ring1-vs-Ring2",
		Description:   "200 Mb/s: Single recv ring vs 2 recv rings (high throughput stress)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithLargeIoUringRecvRing(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithLargeIoUringRecvRing(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       200_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 200 Mb/s: Single-ring vs 4 recv rings
	{
		Name:          "Isolation-200M-Ring1-vs-Ring4",
		Description:   "200 Mb/s: Single recv ring vs 4 recv rings (high throughput stress)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithLargeIoUringRecvRing(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithLargeIoUringRecvRing(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       200_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 200 Mb/s: 2 recv rings vs 4 recv rings
	{
		Name:          "Isolation-200M-Ring2-vs-Ring4",
		Description:   "200 Mb/s: 2 recv rings vs 4 recv rings (find optimal ring count)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       200_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 100 Mb/s: With multi-ring SEND (1 send vs 2 send rings)
	{
		Name:          "Isolation-100M-Send1-vs-Send2",
		Description:   "100 Mb/s: Single send ring vs 2 send rings (measure send parallelism)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithParallelIoUring(4, 2).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       100_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 200 Mb/s: Full multi-ring (4 recv + 2 send) vs single-ring
	{
		Name:          "Isolation-200M-Single-vs-Multi",
		Description:   "200 Mb/s: Single-ring vs Full multi-ring (4 recv + 2 send) - ultimate comparison",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithLargeIoUringRecvRing(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithLargeIoUringRecvRing(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithParallelIoUring(4, 2).WithLargeIoUringRecvRing(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
		TestDuration:  60 * time.Second,
		Bitrate:       200_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========== 300 Mb/s High Throughput Tests ==========

	// 300 Mb/s: 1 recv ring vs 2 recv rings (both using full lock-free config)
	// This is the recommended test for verifying 300 Mb/s stability since 4-ring doesn't work
	{
		Name:          "Isolation-300M-Ring1-vs-Ring2",
		Description:   "300 Mb/s: 1 recv ring vs 2 recv rings (full lock-free, stable config)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithUltraHighThroughput(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestDuration:  60 * time.Second,
		Bitrate:       300_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 300 Mb/s: 2 recv rings vs 4 recv rings
	{
		Name:          "Isolation-300M-Ring2-vs-Ring4",
		Description:   "300 Mb/s: 2 recv rings vs 4 recv rings (high throughput test)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithUltraHighThroughput(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithUltraHighThroughput(),
		TestDuration:  60 * time.Second,
		Bitrate:       300_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 300 Mb/s: 2 recv rings vs 4 recv rings with LEGACY EventLoop (no tight loop)
	// Use this if the tight loop version fails - helps diagnose tight loop issues
	{
		Name:          "Isolation-300M-Ring2-vs-Ring4-LegacyEL",
		Description:   "300 Mb/s: 2 vs 4 recv rings with legacy unbounded EventLoop",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithUltraHighThroughput().WithLegacyEventLoop(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithUltraHighThroughput().WithLegacyEventLoop(),
		TestDuration:  60 * time.Second,
		Bitrate:       300_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========== 400 Mb/s Very High Throughput Tests ==========

	// 400 Mb/s: 2 recv rings vs 4 recv rings
	{
		Name:          "Isolation-400M-Ring2-vs-Ring4",
		Description:   "400 Mb/s: 2 recv rings vs 4 recv rings (very high throughput test)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithUltraHighThroughput(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithUltraHighThroughput(),
		TestDuration:  60 * time.Second,
		Bitrate:       400_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ========== 500 Mb/s Ultra-High Throughput Tests ==========
	// These tests require larger buffers and flow control windows.

	// 500 Mb/s: 2 recv rings vs 4 recv rings (does 4 rings help at extreme throughput?)
	{
		Name:          "Isolation-500M-Ring2-vs-Ring4",
		Description:   "500 Mb/s: 2 recv rings vs 4 recv rings (ultra-high throughput stress test)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithUltraHighThroughput(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithUltraHighThroughput(),
		TestDuration:  60 * time.Second,
		Bitrate:       500_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// 500 Mb/s: 4 recv rings vs 8 recv rings (pushing parallelism limits)
	{
		Name:          "Isolation-500M-Ring4-vs-Ring8",
		Description:   "500 Mb/s: 4 recv rings vs 8 recv rings (maximum parallelism test)",
		ControlCG:     GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(4).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(4).WithUltraHighThroughput(),
		TestCG:        GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().WithMultipleRecvRings(8).WithUltraHighThroughput(),
		TestServer:    GetSRTConfig(ConfigFullELLockFree).WithMultipleRecvRings(8).WithUltraHighThroughput(),
		TestDuration:  60 * time.Second,
		Bitrate:       500_000_000,
		StatsPeriod:   10 * time.Second,
	},

	// ============================================================================
	// PERFORMANCE MAXIMIZATION TESTS (500 Mb/s Target)
	// Reference: documentation/performance_maximization_500mbps.md
	// These tests use aggressive buffer and timer tuning to push throughput limits.
	// ============================================================================

	// ========== 400 Mb/s with Aggressive Tuning ==========

	// 400 Mb/s: Standard vs Aggressive Buffers (test if buffers help)
	{
		Name:        "Isolation-400M-Standard-vs-AggressiveBuffers",
		Description: "400 Mb/s: standard buffers vs aggressive 2x buffers",
		ControlCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput().WithAggressiveBuffers(),
		TestServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput().WithAggressiveBuffers(),
		TestDuration: 60 * time.Second,
		Bitrate:      400_000_000,
		StatsPeriod:  10 * time.Second,
	},

	// 400 Mb/s: Standard vs Aggressive Timers (test if timers help)
	{
		Name:        "Isolation-400M-Standard-vs-AggressiveTimers",
		Description: "400 Mb/s: standard timers vs aggressive fast timers",
		ControlCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput().WithAggressiveTimers(),
		TestServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput().WithAggressiveTimers(),
		TestDuration: 60 * time.Second,
		Bitrate:      400_000_000,
		StatsPeriod:  10 * time.Second,
	},

	// 400 Mb/s: Full Aggressive (buffers + timers combined)
	{
		Name:        "Isolation-400M-Aggressive",
		Description: "400 Mb/s: full aggressive tuning (buffers + timers)",
		ControlCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput().
			WithAggressiveBuffers().WithAggressiveTimers(),
		TestServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput().
			WithAggressiveBuffers().WithAggressiveTimers(),
		TestDuration: 60 * time.Second,
		Bitrate:      400_000_000,
		StatsPeriod:  10 * time.Second,
	},

	// ========== 500 Mb/s with Aggressive and Extreme Tuning ==========

	// 500 Mb/s: Aggressive Tuning (buffers + timers)
	{
		Name:        "Isolation-500M-Aggressive",
		Description: "500 Mb/s: aggressive tuning (2x buffers + fast timers)",
		ControlCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput().
			WithAggressiveBuffers().WithAggressiveTimers(),
		TestServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput().
			WithAggressiveBuffers().WithAggressiveTimers(),
		TestDuration: 60 * time.Second,
		Bitrate:      500_000_000,
		StatsPeriod:  10 * time.Second,
	},

	// 500 Mb/s: Extreme Tuning (4x buffers + extreme timers)
	{
		Name:        "Isolation-500M-Extreme",
		Description: "500 Mb/s: extreme tuning (4x buffers + extreme timers)",
		ControlCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).
			WithExtremeBuffers().WithExtremeTimers(),
		TestServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).
			WithExtremeBuffers().WithExtremeTimers(),
		TestDuration: 60 * time.Second,
		Bitrate:      500_000_000,
		StatsPeriod:  10 * time.Second,
	},

	// 500 Mb/s: Optimized preset (combines best practices)
	{
		Name:        "Isolation-500M-Optimized",
		Description: "500 Mb/s: optimized preset with With500MbpsOptimized()",
		ControlCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		ControlServer: GetSRTConfig(ConfigFullELLockFree).
			WithMultipleRecvRings(2).WithUltraHighThroughput(),
		TestCG: GetSRTConfig(ConfigFullELLockFree).WithHonorNakOrder().
			With500MbpsOptimized(),
		TestServer: GetSRTConfig(ConfigFullELLockFree).
			With500MbpsOptimized(),
		TestDuration: 60 * time.Second,
		Bitrate:      500_000_000,
		StatsPeriod:  10 * time.Second,
	},
}

// GetIsolationTestConfigByName finds an isolation test configuration by name
func GetIsolationTestConfigByName(name string) *IsolationTestConfig {
	// First try to match by Name (new standardized name)
	for i := range IsolationTestConfigs {
		if IsolationTestConfigs[i].Name == name {
			return &IsolationTestConfigs[i]
		}
	}
	// Fall back to LegacyName for backward compatibility
	for i := range IsolationTestConfigs {
		if IsolationTestConfigs[i].LegacyName == name {
			return &IsolationTestConfigs[i]
		}
	}
	return nil
}
