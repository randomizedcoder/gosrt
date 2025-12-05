package main

import "time"

// Default network configurations for each component
var (
	DefaultServerNetwork = NetworkConfig{
		IP:          "127.0.0.10",
		SRTPort:     6000,
		MetricsPort: 9090,
	}

	DefaultClientGeneratorNetwork = NetworkConfig{
		IP:          "127.0.0.20",
		SRTPort:     0, // Ephemeral port (client connects to server)
		MetricsPort: 9091,
	}

	DefaultClientNetwork = NetworkConfig{
		IP:          "127.0.0.30",
		SRTPort:     0, // Ephemeral port (client connects to server)
		MetricsPort: 9092,
	}
)

// Predefined SRT configuration presets for common scenarios
var (
	// DefaultSRTConfig - default settings, no special configuration
	DefaultSRTConfig = SRTConfig{}

	// SmallBuffersSRTConfig - minimal latency, small buffers
	SmallBuffersSRTConfig = SRTConfig{
		ConnectionTimeout: 1000 * time.Millisecond,
		PeerIdleTimeout:   2000 * time.Millisecond,
		Latency:           120 * time.Millisecond,
		RecvLatency:       120 * time.Millisecond,
		PeerLatency:       120 * time.Millisecond,
		TLPktDrop:         true,
	}

	// LargeBuffersSRTConfig - high latency tolerance, large buffers
	LargeBuffersSRTConfig = SRTConfig{
		ConnectionTimeout: 3000 * time.Millisecond,
		PeerIdleTimeout:   30000 * time.Millisecond,
		Latency:           3000 * time.Millisecond,
		RecvLatency:       3000 * time.Millisecond,
		PeerLatency:       3000 * time.Millisecond,
		TLPktDrop:         true,
	}

	// IoUringSRTConfig - io_uring enabled for high performance
	IoUringSRTConfig = SRTConfig{
		IoUringEnabled:     true,
		IoUringRecvEnabled: true,
	}

	// BTreeSRTConfig - B-tree packet reordering for large buffers
	BTreeSRTConfig = SRTConfig{
		PacketReorderAlgorithm: "btree",
		BTreeDegree:            32,
	}

	// ListSRTConfig - List-based packet reordering (default)
	ListSRTConfig = SRTConfig{
		PacketReorderAlgorithm: "list",
	}
)

