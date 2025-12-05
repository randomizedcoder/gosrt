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
