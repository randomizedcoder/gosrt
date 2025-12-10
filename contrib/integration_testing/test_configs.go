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
