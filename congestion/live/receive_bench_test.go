// Package live provides benchmark tests for the receiver.
//
// These benchmarks measure performance across different receiver configurations
// to detect regressions and compare implementation strategies.
//
// Run with: go test -bench "Benchmark" ./congestion/live/... -benchmem -benchtime=3s
//
// Key metrics:
//   - ns/op: Time per operation
//   - B/op: Bytes allocated per operation
//   - allocs/op: Number of allocations per operation
//
// See documentation/receiver_stream_tests_design.md Section 11 for details.
package live

import (
	"fmt"
	"net"
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
)

// ============================================================================
// BENCHMARK INFRASTRUCTURE
// ============================================================================

// BenchConfig defines a benchmark configuration.
type BenchConfig struct {
	Name           string
	ReceiverConfig ReceiverConfig
	PacketCount    int  // Number of packets for the benchmark
	WithLoss       bool // Simulate packet loss
	LossPercent    int  // Loss percentage (0-100)
	WithReorder    bool // Simulate packet reordering
}

// benchPresets defines key benchmark configurations to compare.
var benchPresets = []BenchConfig{
	// Baseline: Original mode
	{"Original", CfgOriginal, 10000, false, 0, false},

	// NAK btree variants
	{"NakBtree", CfgNakBtree, 10000, false, 0, false},
	{"NakBtreeF", CfgNakBtreeF, 10000, false, 0, false},
	{"NakBtreeFr", CfgNakBtreeFr, 10000, false, 0, false},

	// With packet loss (exercises NAK path)
	{"NakBtree-Loss10", CfgNakBtree, 10000, true, 10, false},
	{"NakBtreeF-Loss10", CfgNakBtreeF, 10000, true, 10, false},

	// With reordering
	{"NakBtree-Reorder", CfgNakBtree, 10000, false, 0, true},

	// Combined
	{"NakBtree-Loss10-Reorder", CfgNakBtree, 10000, true, 10, true},
}

// createBenchReceiver creates a receiver configured for benchmarking.
func createBenchReceiver(b *testing.B, cfg ReceiverConfig, startSeq uint32) *receiver {
	testMetrics := &metrics.ConnectionMetrics{
		HandlePacketLockTiming: &metrics.LockTimingMetrics{},
		ReceiverLockTiming:     &metrics.LockTimingMetrics{},
		SenderLockTiming:       &metrics.LockTimingMetrics{},
	}
	testMetrics.HeaderSize.Store(44)

	recvConfig := ReceiveConfig{
		InitialSequenceNumber:  circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
		PeriodicACKInterval:    10_000, // 10ms
		PeriodicNAKInterval:    20_000, // 20ms
		OnSendACK:              func(seq circular.Number, light bool) {},
		OnSendNAK:              func(list []circular.Number) {},
		OnDeliver:              func(p packet.Packet) {},
		ConnectionMetrics:      testMetrics,
		TsbpdDelay:             120_000, // 120ms
		NakConsolidationBudget: 20_000,  // 20ms
	}

	if cfg.UseNakBtree {
		recvConfig.PacketReorderAlgorithm = "btree"
		recvConfig.UseNakBtree = true
		recvConfig.NakRecentPercent = cfg.NakRecentPercent
		recvConfig.NakMergeGap = cfg.NakMergeGap
		recvConfig.NakConsolidationBudget = cfg.NakConsolidationBudget
		recvConfig.FastNakEnabled = cfg.FastNakEnabled
		recvConfig.FastNakRecentEnabled = cfg.FastNakRecentEnabled
		if cfg.FastNakEnabled {
			recvConfig.FastNakThresholdUs = 50_000
		}
	}

	recv := NewReceiver(recvConfig)
	return recv.(*receiver)
}

// generateBenchPackets creates packets for benchmarking.
func generateBenchPackets(count int, startSeq uint32, baseTimeUs uint64, addr net.Addr) []packet.Packet {
	packets := make([]packet.Packet, count)
	for i := 0; i < count; i++ {
		seq := circular.SeqAdd(startSeq, uint32(i))
		p := packet.NewPacket(addr)
		p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
		p.Header().PktTsbpdTime = baseTimeUs + uint64(i)*1000 // 1ms per packet
		p.Header().Timestamp = uint32(baseTimeUs + uint64(i)*1000)
		packets[i] = p
	}
	return packets
}

// applyBenchLoss removes packets to simulate loss.
func applyBenchLoss(packets []packet.Packet, lossPercent int) []packet.Packet {
	if lossPercent == 0 {
		return packets
	}
	result := make([]packet.Packet, 0, len(packets))
	for i, p := range packets {
		if i%100 >= lossPercent {
			result = append(result, p)
		}
	}
	return result
}

// applyBenchReorder applies simple reordering (swap adjacent pairs).
func applyBenchReorder(packets []packet.Packet) []packet.Packet {
	result := make([]packet.Packet, len(packets))
	copy(result, packets)
	for i := 0; i+1 < len(result); i += 2 {
		result[i], result[i+1] = result[i+1], result[i]
	}
	return result
}

// ============================================================================
// BENCHMARK: PUSH THROUGHPUT
// ============================================================================

// BenchmarkPush measures raw Push() throughput across configurations.
func BenchmarkPush(b *testing.B) {
	for _, preset := range benchPresets {
		b.Run(preset.Name, func(b *testing.B) {
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			// Generate packets
			packets := generateBenchPackets(preset.PacketCount, 1, baseTimeUs, addr)
			if preset.WithLoss {
				packets = applyBenchLoss(packets, preset.LossPercent)
			}
			if preset.WithReorder {
				packets = applyBenchReorder(packets)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				// Create fresh receiver for each iteration
				recv := createBenchReceiver(b, preset.ReceiverConfig, 1)

				// Push all packets
				for _, p := range packets {
					recv.Push(p)
				}
			}
		})
	}
}

// BenchmarkPush_SinglePacket measures single packet Push() latency.
func BenchmarkPush_SinglePacket(b *testing.B) {
	configs := []ReceiverConfig{CfgOriginal, CfgNakBtree, CfgNakBtreeF}

	for _, cfg := range configs {
		b.Run(cfg.Name, func(b *testing.B) {
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			recv := createBenchReceiver(b, cfg, 1)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				seq := uint32(i % 100000) // Cycle through sequences
				p := packet.NewPacket(addr)
				p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
				p.Header().PktTsbpdTime = baseTimeUs + uint64(seq)*1000
				p.Header().Timestamp = uint32(baseTimeUs + uint64(seq)*1000)
				recv.Push(p)
			}
		})
	}
}

// ============================================================================
// BENCHMARK: TICK LATENCY
// ============================================================================

// BenchmarkTick measures Tick() latency with pre-populated btree.
func BenchmarkTick(b *testing.B) {
	btreeSizes := []int{100, 1000, 5000, 10000}
	configs := []ReceiverConfig{CfgNakBtree, CfgNakBtreeF}

	for _, cfg := range configs {
		for _, size := range btreeSizes {
			name := fmt.Sprintf("%s/btree-%d", cfg.Name, size)
			b.Run(name, func(b *testing.B) {
				addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
				baseTimeUs := uint64(1_000_000)

				recv := createBenchReceiver(b, cfg, 1)

				// Pre-populate btree with packets
				packets := generateBenchPackets(size, 1, baseTimeUs, addr)
				for _, p := range packets {
					recv.Push(p)
				}

				// Tick time should be after TSBPD delay for some packets
				tickTime := baseTimeUs + 200_000 // 200ms after start

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					recv.Tick(tickTime + uint64(i)*10_000)
				}
			})
		}
	}
}

// BenchmarkTick_Empty measures Tick() with empty btree (best case).
func BenchmarkTick_Empty(b *testing.B) {
	configs := []ReceiverConfig{CfgOriginal, CfgNakBtree}

	for _, cfg := range configs {
		b.Run(cfg.Name, func(b *testing.B) {
			baseTimeUs := uint64(1_000_000)
			recv := createBenchReceiver(b, cfg, 1)
			tickTime := baseTimeUs + 200_000

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				recv.Tick(tickTime + uint64(i)*10_000)
			}
		})
	}
}

// ============================================================================
// BENCHMARK: NAK SCAN
// ============================================================================

// BenchmarkNakScan measures NAK scan time with varying btree sizes and gap patterns.
func BenchmarkNakScan(b *testing.B) {
	btreeSizes := []int{100, 1000, 5000, 10000}
	gapRatios := []int{0, 5, 10, 20} // Percent of packets missing

	for _, size := range btreeSizes {
		for _, gapRatio := range gapRatios {
			name := fmt.Sprintf("btree-%d/gaps-%dpct", size, gapRatio)
			b.Run(name, func(b *testing.B) {
				addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
				baseTimeUs := uint64(1_000_000)

				recv := createBenchReceiver(b, CfgNakBtree, 1)

				// Generate packets with gaps
				for i := 0; i < size; i++ {
					// Create gaps based on gapRatio
					if i%100 < (100 - gapRatio) {
						seq := uint32(i + 1)
						p := packet.NewPacket(addr)
						p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
						p.Header().PktTsbpdTime = baseTimeUs + uint64(i)*1000
						p.Header().Timestamp = uint32(baseTimeUs + uint64(i)*1000)
						recv.Push(p)
					}
				}

				tickTime := baseTimeUs + 200_000

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					recv.Tick(tickTime + uint64(i)*20_000)
				}
			})
		}
	}
}

// ============================================================================
// BENCHMARK: FULL PIPELINE
// ============================================================================

// BenchmarkFullPipeline measures end-to-end throughput (Push + Tick + Deliver).
func BenchmarkFullPipeline(b *testing.B) {
	streamSizes := []int{100, 1000, 5000}
	configs := []ReceiverConfig{CfgOriginal, CfgNakBtree, CfgNakBtreeF}

	for _, cfg := range configs {
		for _, size := range streamSizes {
			name := fmt.Sprintf("%s/stream-%d", cfg.Name, size)
			b.Run(name, func(b *testing.B) {
				addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
				baseTimeUs := uint64(1_000_000)

				packets := generateBenchPackets(size, 1, baseTimeUs, addr)

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					recv := createBenchReceiver(b, cfg, 1)

					// Push all packets
					for _, p := range packets {
						recv.Push(p)
					}

					// Tick to deliver (multiple ticks to ensure delivery)
					tickTime := baseTimeUs + 200_000
					for j := 0; j < 10; j++ {
						recv.Tick(tickTime + uint64(j)*20_000)
					}
				}
			})
		}
	}
}

// BenchmarkFullPipeline_WithLoss measures pipeline with packet loss.
func BenchmarkFullPipeline_WithLoss(b *testing.B) {
	lossRates := []int{5, 10, 20}
	configs := []ReceiverConfig{CfgNakBtree, CfgNakBtreeF}

	for _, cfg := range configs {
		for _, lossRate := range lossRates {
			name := fmt.Sprintf("%s/loss-%dpct", cfg.Name, lossRate)
			b.Run(name, func(b *testing.B) {
				addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
				baseTimeUs := uint64(1_000_000)

				allPackets := generateBenchPackets(1000, 1, baseTimeUs, addr)
				packets := applyBenchLoss(allPackets, lossRate)

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					recv := createBenchReceiver(b, cfg, 1)

					for _, p := range packets {
						recv.Push(p)
					}

					tickTime := baseTimeUs + 200_000
					for j := 0; j < 10; j++ {
						recv.Tick(tickTime + uint64(j)*20_000)
					}
				}
			})
		}
	}
}

// ============================================================================
// BENCHMARK: MEMORY ALLOCATION
// ============================================================================

// BenchmarkAllocs_Push focuses on allocation count per Push.
func BenchmarkAllocs_Push(b *testing.B) {
	configs := []ReceiverConfig{CfgOriginal, CfgNakBtree}

	for _, cfg := range configs {
		b.Run(cfg.Name, func(b *testing.B) {
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
			baseTimeUs := uint64(1_000_000)

			recv := createBenchReceiver(b, cfg, 1)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				seq := uint32(i)
				p := packet.NewPacket(addr)
				p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
				p.Header().PktTsbpdTime = baseTimeUs + uint64(seq)*1000
				p.Header().Timestamp = uint32(baseTimeUs + uint64(seq)*1000)
				recv.Push(p)
			}
		})
	}
}

// ============================================================================
// BENCHMARK: CONFIG COMPARISONS
// ============================================================================

// BenchmarkConfigComparison directly compares key configurations.
func BenchmarkConfigComparison(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseTimeUs := uint64(1_000_000)
	streamSize := 1000

	configs := map[string]ReceiverConfig{
		"1_Original":   CfgOriginal,
		"2_NakBtree":   CfgNakBtree,
		"3_NakBtreeF":  CfgNakBtreeF,
		"4_NakBtreeFr": CfgNakBtreeFr,
	}

	for name, cfg := range configs {
		b.Run(name, func(b *testing.B) {
			packets := generateBenchPackets(streamSize, 1, baseTimeUs, addr)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				recv := createBenchReceiver(b, cfg, 1)

				for _, p := range packets {
					recv.Push(p)
				}

				tickTime := baseTimeUs + 200_000
				for j := 0; j < 5; j++ {
					recv.Tick(tickTime + uint64(j)*20_000)
				}
			}
		})
	}
}

// BenchmarkNakMergeGap compares different NakMergeGap values.
func BenchmarkNakMergeGap(b *testing.B) {
	mergeGaps := []uint32{0, 1, 3, 5, 10}
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseTimeUs := uint64(1_000_000)

	for _, gap := range mergeGaps {
		name := fmt.Sprintf("mergeGap-%d", gap)
		b.Run(name, func(b *testing.B) {
			cfg := ReceiverConfig{
				Name:                   "NakBtree",
				UseNakBtree:            true,
				NakMergeGap:            gap,
				NakRecentPercent:       0.10,
				NakConsolidationBudget: 20_000,
			}

			// Generate stream with scattered gaps
			allPackets := generateBenchPackets(1000, 1, baseTimeUs, addr)
			packets := applyBenchLoss(allPackets, 10) // 10% loss

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				recv := createBenchReceiver(b, cfg, 1)

				for _, p := range packets {
					recv.Push(p)
				}

				tickTime := baseTimeUs + 200_000
				for j := 0; j < 5; j++ {
					recv.Tick(tickTime + uint64(j)*20_000)
				}
			}
		})
	}
}

// ============================================================================
// BENCHMARK: SCALABILITY
// ============================================================================

// BenchmarkScalability_StreamSize tests how performance scales with stream size.
func BenchmarkScalability_StreamSize(b *testing.B) {
	sizes := []int{100, 500, 1000, 2000, 5000, 10000}
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseTimeUs := uint64(1_000_000)

	for _, size := range sizes {
		name := fmt.Sprintf("NakBtree/size-%d", size)
		b.Run(name, func(b *testing.B) {
			packets := generateBenchPackets(size, 1, baseTimeUs, addr)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				recv := createBenchReceiver(b, CfgNakBtree, 1)

				for _, p := range packets {
					recv.Push(p)
				}

				tickTime := baseTimeUs + 200_000
				for j := 0; j < 5; j++ {
					recv.Tick(tickTime + uint64(j)*20_000)
				}
			}
		})
	}
}

// ============================================================================
// BENCHMARK: REALISTIC BUFFER SIZES
// ============================================================================

// RealisticStreamConfig defines a realistic streaming scenario.
type RealisticStreamConfig struct {
	Name         string
	BitrateMbps  int     // Bitrate in Mbps
	PayloadBytes int     // Payload size (1316 = 7 MPEG-TS packets)
	BufferSec    float64 // Buffer duration in seconds
}

// realisticConfigs defines real-world streaming scenarios.
var realisticConfigs = []RealisticStreamConfig{
	// 10 Mb/s scenarios (~950 packets/sec with 1316 byte packets)
	{"10Mbps-3s", 10, 1316, 3.0},   // ~2,850 packets
	{"10Mbps-5s", 10, 1316, 5.0},   // ~4,750 packets
	{"10Mbps-10s", 10, 1316, 10.0}, // ~9,500 packets
	{"10Mbps-30s", 10, 1316, 30.0}, // ~28,500 packets

	// 20 Mb/s scenarios (~1,900 packets/sec with 1316 byte packets)
	{"20Mbps-3s", 20, 1316, 3.0},   // ~5,700 packets
	{"20Mbps-5s", 20, 1316, 5.0},   // ~9,500 packets
	{"20Mbps-10s", 20, 1316, 10.0}, // ~19,000 packets
	{"20Mbps-30s", 20, 1316, 30.0}, // ~57,000 packets
}

// calculatePacketCount returns the number of packets for a realistic config.
func calculatePacketCount(cfg RealisticStreamConfig) int {
	bitsPerPacket := cfg.PayloadBytes * 8
	packetsPerSecond := float64(cfg.BitrateMbps*1_000_000) / float64(bitsPerPacket)
	return int(packetsPerSecond * cfg.BufferSec)
}

// BenchmarkRealistic_Push measures Push throughput for realistic buffer sizes.
func BenchmarkRealistic_Push(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseTimeUs := uint64(1_000_000)
	configs := []ReceiverConfig{CfgOriginal, CfgNakBtree, CfgNakBtreeF}

	for _, rcfg := range realisticConfigs {
		packetCount := calculatePacketCount(rcfg)
		for _, cfg := range configs {
			name := fmt.Sprintf("%s/%s/pkts-%d", cfg.Name, rcfg.Name, packetCount)
			b.Run(name, func(b *testing.B) {
				packets := generateBenchPackets(packetCount, 1, baseTimeUs, addr)

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					recv := createBenchReceiver(b, cfg, 1)
					for _, p := range packets {
						recv.Push(p)
					}
				}
			})
		}
	}
}

// BenchmarkRealistic_Tick measures Tick latency with realistic buffer sizes.
func BenchmarkRealistic_Tick(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseTimeUs := uint64(1_000_000)
	configs := []ReceiverConfig{CfgNakBtree, CfgNakBtreeF}

	for _, rcfg := range realisticConfigs {
		packetCount := calculatePacketCount(rcfg)
		for _, cfg := range configs {
			name := fmt.Sprintf("%s/%s/pkts-%d", cfg.Name, rcfg.Name, packetCount)
			b.Run(name, func(b *testing.B) {
				recv := createBenchReceiver(b, cfg, 1)

				// Pre-populate with packets
				packets := generateBenchPackets(packetCount, 1, baseTimeUs, addr)
				for _, p := range packets {
					recv.Push(p)
				}

				tickTime := baseTimeUs + 200_000

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					recv.Tick(tickTime + uint64(i)*20_000)
				}
			})
		}
	}
}

// BenchmarkRealistic_NakScan measures NAK scan with realistic buffer sizes and loss.
func BenchmarkRealistic_NakScan(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseTimeUs := uint64(1_000_000)
	lossRates := []int{0, 5, 10} // 0%, 5%, 10% loss

	for _, rcfg := range realisticConfigs {
		packetCount := calculatePacketCount(rcfg)
		for _, lossRate := range lossRates {
			name := fmt.Sprintf("NakBtree/%s/pkts-%d/loss-%dpct", rcfg.Name, packetCount, lossRate)
			b.Run(name, func(b *testing.B) {
				recv := createBenchReceiver(b, CfgNakBtree, 1)

				// Generate packets with gaps (simulating loss)
				for i := 0; i < packetCount; i++ {
					// Skip packets based on loss rate
					if lossRate == 0 || i%100 >= lossRate {
						seq := uint32(i + 1)
						p := packet.NewPacket(addr)
						p.Header().PacketSequenceNumber = circular.New(seq, packet.MAX_SEQUENCENUMBER)
						p.Header().PktTsbpdTime = baseTimeUs + uint64(i)*1000
						p.Header().Timestamp = uint32(baseTimeUs + uint64(i)*1000)
						recv.Push(p)
					}
				}

				tickTime := baseTimeUs + 200_000

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					recv.Tick(tickTime + uint64(i)*20_000)
				}
			})
		}
	}
}

// BenchmarkRealistic_FullPipeline measures full pipeline for realistic scenarios.
func BenchmarkRealistic_FullPipeline(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseTimeUs := uint64(1_000_000)
	configs := []ReceiverConfig{CfgOriginal, CfgNakBtree, CfgNakBtreeF}

	// Focus on key realistic scenarios
	keyConfigs := []RealisticStreamConfig{
		{"10Mbps-5s", 10, 1316, 5.0},   // ~4,750 packets (common TSBPD buffer)
		{"10Mbps-10s", 10, 1316, 10.0}, // ~9,500 packets
		{"20Mbps-5s", 20, 1316, 5.0},   // ~9,500 packets
		{"20Mbps-10s", 20, 1316, 10.0}, // ~19,000 packets
	}

	for _, rcfg := range keyConfigs {
		packetCount := calculatePacketCount(rcfg)
		for _, cfg := range configs {
			name := fmt.Sprintf("%s/%s/pkts-%d", cfg.Name, rcfg.Name, packetCount)
			b.Run(name, func(b *testing.B) {
				packets := generateBenchPackets(packetCount, 1, baseTimeUs, addr)

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					recv := createBenchReceiver(b, cfg, 1)

					// Push all packets
					for _, p := range packets {
						recv.Push(p)
					}

					// Multiple ticks to ensure delivery
					tickTime := baseTimeUs + 200_000
					for j := 0; j < 10; j++ {
						recv.Tick(tickTime + uint64(j)*20_000)
					}
				}
			})
		}
	}
}

// BenchmarkRealistic_MemoryUsage measures memory with large realistic buffers.
func BenchmarkRealistic_MemoryUsage(b *testing.B) {
	addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")
	baseTimeUs := uint64(1_000_000)

	// Large buffer scenarios for memory testing
	largeConfigs := []RealisticStreamConfig{
		{"10Mbps-30s", 10, 1316, 30.0}, // ~28,500 packets
		{"20Mbps-30s", 20, 1316, 30.0}, // ~57,000 packets
	}

	for _, rcfg := range largeConfigs {
		packetCount := calculatePacketCount(rcfg)
		name := fmt.Sprintf("NakBtree/%s/pkts-%d", rcfg.Name, packetCount)
		b.Run(name, func(b *testing.B) {
			packets := generateBenchPackets(packetCount, 1, baseTimeUs, addr)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				recv := createBenchReceiver(b, CfgNakBtree, 1)
				for _, p := range packets {
					recv.Push(p)
				}
			}
		})
	}
}
