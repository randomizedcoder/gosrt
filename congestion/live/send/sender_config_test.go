//go:build go1.18

package send

import (
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Table-driven tests for SendConfig validation and edge cases
//
// Tests configuration validation, defaults, and invalid combinations.
// Reference: send_eventloop_intermittent_failure_bug.md Section 7.6
// ============================================================================

// ConfigTestCase defines a test case for configuration validation
type ConfigTestCase struct {
	Name string

	// Configuration
	Config SendConfig

	// Expected behavior
	ShouldPanic    bool
	ExpectBtree    bool
	ExpectRing     bool
	ExpectControl  bool
	ExpectEventLoop bool
}

var configTestCases = []ConfigTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Valid Configurations
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name: "AllEnabled_Full",
		Config: SendConfig{
			InitialSequenceNumber:        circular.New(0, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:            &metrics.ConnectionMetrics{},
			OnDeliver:                    func(p packet.Packet) {},
			StartTime:                    time.Now(),
			UseBtree:                     true,
			BtreeDegree:                  32,
			UseSendRing:                  true,
			SendRingSize:                 1024,
			SendRingShards:               1,
			UseSendControlRing:           true,
			SendControlRingSize:          256,
			SendControlRingShards:        2,
			UseSendEventLoop:             true,
			SendEventLoopBackoffMinSleep: 100 * time.Microsecond,
			SendEventLoopBackoffMaxSleep: 1 * time.Millisecond,
			SendDropThresholdUs:          1_000_000,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: true,
	},
	{
		Name: "Legacy_AllDisabled",
		Config: SendConfig{
			InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:     &metrics.ConnectionMetrics{},
			OnDeliver:             func(p packet.Packet) {},
			StartTime:             time.Now(),
			UseBtree:              false,
			UseSendRing:           false,
			UseSendControlRing:    false,
			UseSendEventLoop:      false,
		},
		ShouldPanic:     false,
		ExpectBtree:     false,
		ExpectRing:      false,
		ExpectControl:   false,
		ExpectEventLoop: false,
	},
	{
		Name: "BtreeOnly_NoRing",
		Config: SendConfig{
			InitialSequenceNumber: circular.New(1000, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:     &metrics.ConnectionMetrics{},
			OnDeliver:             func(p packet.Packet) {},
			StartTime:             time.Now(),
			UseBtree:              true,
			BtreeDegree:           16,
			UseSendRing:           false,
			UseSendControlRing:    false,
			UseSendEventLoop:      false,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      false,
		ExpectControl:   false,
		ExpectEventLoop: false,
	},
	{
		Name: "BtreeAndRing_NoEventLoop",
		Config: SendConfig{
			InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:     &metrics.ConnectionMetrics{},
			OnDeliver:             func(p packet.Packet) {},
			StartTime:             time.Now(),
			UseBtree:              true,
			UseSendRing:           true,
			SendRingSize:          512,
			UseSendControlRing:    true,
			SendControlRingSize:   128,
			UseSendEventLoop:      false, // Tick mode
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: false,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// ISN Edge Cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name: "ISN_Zero",
		Config: SendConfig{
			InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:      &metrics.ConnectionMetrics{},
			OnDeliver:              func(p packet.Packet) {},
			StartTime:              time.Now(),
			UseBtree:               true,
			UseSendRing:            true,
			SendRingSize:           256,
			UseSendControlRing:     true, // Required for EventLoop
			SendControlRingSize:    64,
			UseSendEventLoop:       true,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: true,
	},
	{
		Name: "ISN_Max",
		Config: SendConfig{
			InitialSequenceNumber:  circular.New(2147483647, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:      &metrics.ConnectionMetrics{},
			OnDeliver:              func(p packet.Packet) {},
			StartTime:              time.Now(),
			UseBtree:               true,
			UseSendRing:            true,
			SendRingSize:           256,
			UseSendControlRing:     true,
			SendControlRingSize:    64,
			UseSendEventLoop:       true,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: true,
	},
	{
		Name: "ISN_Random_549M",
		Config: SendConfig{
			InitialSequenceNumber:  circular.New(549144712, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:      &metrics.ConnectionMetrics{},
			OnDeliver:              func(p packet.Packet) {},
			StartTime:              time.Now(),
			UseBtree:               true,
			UseSendRing:            true,
			SendRingSize:           256,
			UseSendControlRing:     true,
			SendControlRingSize:    64,
			UseSendEventLoop:       true,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: true,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Ring Size Configurations
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name: "SmallRing_32",
		Config: SendConfig{
			InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:      &metrics.ConnectionMetrics{},
			OnDeliver:              func(p packet.Packet) {},
			StartTime:              time.Now(),
			UseBtree:               true,
			UseSendRing:            true,
			SendRingSize:           32,
			UseSendControlRing:     true,
			SendControlRingSize:    64,
			UseSendEventLoop:       true,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: true,
	},
	{
		Name: "LargeRing_8192",
		Config: SendConfig{
			InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:      &metrics.ConnectionMetrics{},
			OnDeliver:              func(p packet.Packet) {},
			StartTime:              time.Now(),
			UseBtree:               true,
			UseSendRing:            true,
			SendRingSize:           8192,
			UseSendControlRing:     true,
			SendControlRingSize:    64,
			UseSendEventLoop:       true,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: true,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Drop Threshold Configurations
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name: "DropThreshold_1Second",
		Config: SendConfig{
			InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:      &metrics.ConnectionMetrics{},
			OnDeliver:              func(p packet.Packet) {},
			StartTime:              time.Now(),
			UseBtree:               true,
			UseSendRing:            true,
			SendRingSize:           256,
			UseSendControlRing:     true,
			SendControlRingSize:    64,
			UseSendEventLoop:       true,
			SendDropThresholdUs:    1_000_000,
			DropThreshold:          1_000_000,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: true,
	},
	{
		Name: "DropThreshold_10Seconds",
		Config: SendConfig{
			InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
			ConnectionMetrics:      &metrics.ConnectionMetrics{},
			OnDeliver:              func(p packet.Packet) {},
			StartTime:              time.Now(),
			UseBtree:               true,
			UseSendRing:            true,
			SendRingSize:           256,
			UseSendControlRing:     true,
			SendControlRingSize:    64,
			UseSendEventLoop:       true,
			SendDropThresholdUs:    10_000_000,
			DropThreshold:          10_000_000,
		},
		ShouldPanic:     false,
		ExpectBtree:     true,
		ExpectRing:      true,
		ExpectControl:   true,
		ExpectEventLoop: true,
	},
}

// TestConfig_Table tests various configuration combinations
func TestConfig_Table(t *testing.T) {
	for _, tc := range configTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.ShouldPanic {
				require.Panics(t, func() {
					NewSender(tc.Config)
				})
				return
			}

			s := NewSender(tc.Config).(*sender)

			// Verify btree
			if tc.ExpectBtree {
				require.True(t, s.useBtree)
				require.NotNil(t, s.packetBtree)
			} else {
				require.False(t, s.useBtree)
			}

			// Verify ring
			if tc.ExpectRing {
				require.True(t, s.useRing)
				require.NotNil(t, s.packetRing)
			} else {
				require.False(t, s.useRing)
			}

			// Verify control ring
			if tc.ExpectControl {
				require.True(t, s.useControlRing)
				require.NotNil(t, s.controlRing)
			} else {
				require.False(t, s.useControlRing)
			}

			// Verify EventLoop
			if tc.ExpectEventLoop {
				require.True(t, s.useEventLoop)
			} else {
				require.False(t, s.useEventLoop)
			}
		})
	}
}

// TestConfig_DeliveryStartPoint_InitializedToISN tests the critical bug fix
func TestConfig_DeliveryStartPoint_InitializedToISN(t *testing.T) {
	isnValues := []uint32{
		0,
		1000,
		549144712,  // THE FAILING CASE
		879502527,
		2147483640,
		2147483647,
	}

	for _, isn := range isnValues {
		t.Run(formatISN(isn), func(t *testing.T) {
			s := NewSender(SendConfig{
				InitialSequenceNumber:  circular.New(isn, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:      &metrics.ConnectionMetrics{},
				OnDeliver:              func(p packet.Packet) {},
				StartTime:              time.Now(),
				UseBtree:               true,
				UseSendRing:            true,
				SendRingSize:           256,
				UseSendControlRing:     true, // Required for EventLoop
				SendControlRingSize:    64,
				UseSendEventLoop:       true,
			}).(*sender)

			// CRITICAL: deliveryStartPoint must be initialized to ISN
			require.Equal(t, uint64(isn), s.deliveryStartPoint.Load(),
				"BUG: deliveryStartPoint must be initialized to ISN=%d, not 0", isn)

			// nextSequenceNumber must also be ISN
			require.Equal(t, isn, s.nextSequenceNumber.Val())
		})
	}
}

// TestConfig_NowFn_RelativeTime tests that nowFn returns relative time
func TestConfig_NowFn_RelativeTime(t *testing.T) {
	startTime := time.Now()

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      &metrics.ConnectionMetrics{},
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              startTime,
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           256,
		UseSendControlRing:     true,
		SendControlRingSize:    64,
		UseSendEventLoop:       true,
	}).(*sender)

	// Wait a bit
	time.Sleep(10 * time.Millisecond)

	nowUs := s.nowFn()

	// Should be approximately 10ms = 10,000 µs
	require.Greater(t, nowUs, uint64(5_000), "nowFn should return > 5ms")
	require.Less(t, nowUs, uint64(50_000), "nowFn should return < 50ms")

	// Must NOT be absolute time (which would be ~1.7e15 in 2026)
	require.Less(t, nowUs, uint64(1_000_000_000_000),
		"nowFn must return RELATIVE time, not absolute time")
}

// TestConfig_BtreeDegree tests btree degree configuration
func TestConfig_BtreeDegree(t *testing.T) {
	degrees := []int{2, 4, 8, 16, 32, 64}

	for _, degree := range degrees {
		t.Run("Degree_"+string(rune('0'+degree/10))+string(rune('0'+degree%10)), func(t *testing.T) {
			s := NewSender(SendConfig{
				InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:     &metrics.ConnectionMetrics{},
				OnDeliver:             func(p packet.Packet) {},
				StartTime:             time.Now(),
				UseBtree:              true,
				BtreeDegree:           degree,
			}).(*sender)

			require.NotNil(t, s.packetBtree)
		})
	}
}

// TestConfig_MultiShard tests multi-shard ring configuration
func TestConfig_MultiShard(t *testing.T) {
	shardCounts := []int{1, 2, 4, 8}

	for _, shards := range shardCounts {
		t.Run("Shards_"+string(rune('0'+shards)), func(t *testing.T) {
			s := NewSender(SendConfig{
				InitialSequenceNumber:    circular.New(0, packet.MAX_SEQUENCENUMBER),
				ConnectionMetrics:        &metrics.ConnectionMetrics{},
				OnDeliver:                func(p packet.Packet) {},
				StartTime:                time.Now(),
				UseBtree:                 true,
				UseSendRing:              true,
				SendRingSize:             1024,
				SendRingShards:           shards,
				UseSendControlRing:       true,
				SendControlRingSize:      256,
				SendControlRingShards:    shards,
				UseSendEventLoop:         true,
			}).(*sender)

			require.NotNil(t, s.packetRing)
			require.NotNil(t, s.controlRing)
		})
	}
}

// TestConfig_MetricsAttached tests that metrics are properly attached
func TestConfig_MetricsAttached(t *testing.T) {
	m := &metrics.ConnectionMetrics{}

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      m,
		OnDeliver:              func(p packet.Packet) {},
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           256,
		UseSendControlRing:     true,
		SendControlRingSize:    64,
		UseSendEventLoop:       true,
	}).(*sender)

	require.Equal(t, m, s.metrics)
}

// TestConfig_OnDeliver_Called tests that OnDeliver callback is invoked
func TestConfig_OnDeliver_Called(t *testing.T) {
	var deliverCalled bool

	s := NewSender(SendConfig{
		InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
		ConnectionMetrics:      &metrics.ConnectionMetrics{},
		OnDeliver:              func(p packet.Packet) { deliverCalled = true },
		StartTime:              time.Now(),
		UseBtree:               true,
		UseSendRing:            true,
		SendRingSize:           256,
		UseSendControlRing:     true,
		SendControlRingSize:    64,
		UseSendEventLoop:       true,
	}).(*sender)

	s.nowFn = func() uint64 { return 1_000_000 }

	// Push and deliver a packet
	pkt := createTestPacketWithTsbpd(0, 100)
	s.packetBtree.Insert(pkt)

	delivered, _ := s.deliverReadyPacketsEventLoop(1_000_000)

	require.Equal(t, 1, delivered)
	require.True(t, deliverCalled, "OnDeliver should have been called")
}

