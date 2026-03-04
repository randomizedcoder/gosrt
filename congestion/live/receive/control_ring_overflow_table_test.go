//go:build go1.18

package receive

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// =============================================================================
// Control Ring Overflow Table-Driven Tests
//
// Phase 3.2: Comprehensive tests for receiver control ring overflow scenarios,
// edge cases, and error handling paths.
//
// Reference: completely_lockfree_receiver.md Section 5.1.2
// =============================================================================

// TestRecvControlRing_Creation_TableDriven tests ring creation with various configs.
func TestRecvControlRing_Creation_TableDriven(t *testing.T) {
	testCases := []struct {
		name         string
		size         int
		shards       int
		expectShards int
		expectError  bool
	}{
		// === Normal configurations ===
		{
			name:         "default_128_1",
			size:         128,
			shards:       1,
			expectShards: 1,
		},
		{
			name:         "large_ring",
			size:         4096,
			shards:       1,
			expectShards: 1,
		},
		{
			name:         "multi_shard",
			size:         128,
			shards:       2,
			expectShards: 2,
		},
		{
			name:         "small_ring",
			size:         16,
			shards:       1,
			expectShards: 1,
		},

		// === Default value cases ===
		{
			name:         "zero_size_defaults",
			size:         0,
			shards:       1,
			expectShards: 1,
		},
		{
			name:         "zero_shards_defaults",
			size:         128,
			shards:       0,
			expectShards: 1,
		},
		{
			name:         "negative_size_defaults",
			size:         -1,
			shards:       1,
			expectShards: 1,
		},
		{
			name:         "negative_shards_defaults",
			size:         128,
			shards:       -1,
			expectShards: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewRecvControlRing(tc.size, tc.shards)

			if tc.expectError {
				require.Error(t, err)
				require.Nil(t, ring)
			} else {
				require.NoError(t, err)
				require.NotNil(t, ring)
				require.Equal(t, tc.expectShards, ring.Shards())
			}
		})
	}
}

// TestRecvControlRing_Overflow_ACKACK_TableDriven tests ACKACK overflow scenarios.
func TestRecvControlRing_Overflow_ACKACK_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		ringSize      int
		pushCount     int
		expectDropped int // Minimum expected drops
		expectPushed  int // Minimum expected pushes
	}{
		{
			name:          "tiny_ring_overflow",
			ringSize:      4,
			pushCount:     10,
			expectDropped: 1,
			expectPushed:  4,
		},
		{
			name:          "small_ring_overflow",
			ringSize:      16,
			pushCount:     32,
			expectDropped: 1,
			expectPushed:  16,
		},
		{
			name:          "no_overflow",
			ringSize:      100,
			pushCount:     50,
			expectDropped: 0,
			expectPushed:  50,
		},
		{
			name:          "exactly_at_capacity",
			ringSize:      32,
			pushCount:     32,
			expectDropped: 0,
			expectPushed:  32,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewRecvControlRing(tc.ringSize, 1)
			require.NoError(t, err)

			now := time.Now()
			pushed := 0
			dropped := 0

			for i := 0; i < tc.pushCount; i++ {
				if ring.PushACKACK(uint32(i), now) {
					pushed++
				} else {
					dropped++
				}
			}

			t.Logf("%s: pushed=%d, dropped=%d", tc.name, pushed, dropped)

			require.GreaterOrEqual(t, dropped, tc.expectDropped)
			require.GreaterOrEqual(t, pushed, tc.expectPushed)
			require.Equal(t, tc.pushCount, pushed+dropped)
		})
	}
}

// TestRecvControlRing_Overflow_KEEPALIVE_TableDriven tests KEEPALIVE overflow scenarios.
func TestRecvControlRing_Overflow_KEEPALIVE_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		ringSize      int
		pushCount     int
		expectDropped int
	}{
		{
			name:          "tiny_ring_overflow",
			ringSize:      4,
			pushCount:     10,
			expectDropped: 1,
		},
		{
			name:          "no_overflow",
			ringSize:      64,
			pushCount:     32,
			expectDropped: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewRecvControlRing(tc.ringSize, 1)
			require.NoError(t, err)

			pushed := 0
			dropped := 0

			for i := 0; i < tc.pushCount; i++ {
				if ring.PushKEEPALIVE() {
					pushed++
				} else {
					dropped++
				}
			}

			require.GreaterOrEqual(t, dropped, tc.expectDropped)
		})
	}
}

// TestRecvControlRing_Mixed_Overflow_TableDriven tests mixed ACKACK/KEEPALIVE overflow.
func TestRecvControlRing_Mixed_Overflow_TableDriven(t *testing.T) {
	testCases := []struct {
		name           string
		ringSize       int
		ackackCount    int
		keepaliveCount int
		expectMinDrops int
	}{
		{
			name:           "small_ring_mixed",
			ringSize:       8,
			ackackCount:    10,
			keepaliveCount: 10,
			expectMinDrops: 1,
		},
		{
			name:           "alternating_overflow",
			ringSize:       4,
			ackackCount:    5,
			keepaliveCount: 5,
			expectMinDrops: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewRecvControlRing(tc.ringSize, 1)
			require.NoError(t, err)

			now := time.Now()
			totalPushed := 0
			totalDropped := 0

			// Alternate pushing
			for i := 0; i < tc.ackackCount+tc.keepaliveCount; i++ {
				var ok bool
				if i%2 == 0 && totalPushed/2 < tc.ackackCount {
					ok = ring.PushACKACK(uint32(i), now)
				} else {
					ok = ring.PushKEEPALIVE()
				}
				if ok {
					totalPushed++
				} else {
					totalDropped++
				}
			}

			require.GreaterOrEqual(t, totalDropped, tc.expectMinDrops)
		})
	}
}

// TestRecvControlRing_TryPop_States_TableDriven tests TryPop in various states.
func TestRecvControlRing_TryPop_States_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		setupRing  func(*RecvControlRing)
		expectOK   bool
		expectType RecvControlPacketType
	}{
		{
			name:      "empty_ring",
			setupRing: func(r *RecvControlRing) {},
			expectOK:  false,
		},
		{
			name: "single_ackack",
			setupRing: func(r *RecvControlRing) {
				r.PushACKACK(100, time.Now())
			},
			expectOK:   true,
			expectType: RecvControlTypeACKACK,
		},
		{
			name: "single_keepalive",
			setupRing: func(r *RecvControlRing) {
				r.PushKEEPALIVE()
			},
			expectOK:   true,
			expectType: RecvControlTypeKEEPALIVE,
		},
		{
			name: "after_drain",
			setupRing: func(r *RecvControlRing) {
				r.PushACKACK(100, time.Now())
				r.TryPop()
			},
			expectOK: false,
		},
		{
			name: "multiple_then_drain_partial",
			setupRing: func(r *RecvControlRing) {
				r.PushACKACK(1, time.Now())
				r.PushKEEPALIVE()
				r.TryPop() // Remove first
			},
			expectOK:   true,
			expectType: RecvControlTypeKEEPALIVE,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewRecvControlRing(32, 1)
			require.NoError(t, err)

			tc.setupRing(ring)

			pkt, ok := ring.TryPop()

			require.Equal(t, tc.expectOK, ok)
			if tc.expectOK {
				require.Equal(t, tc.expectType, pkt.Type)
			}
		})
	}
}

// TestRecvControlRing_Timestamp_Preservation_TableDriven tests timestamp accuracy.
func TestRecvControlRing_Timestamp_Preservation_TableDriven(t *testing.T) {
	testCases := []struct {
		name  string
		delay time.Duration
	}{
		{"no_delay", 0},
		{"1ms_delay", time.Millisecond},
		{"10ms_delay", 10 * time.Millisecond},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewRecvControlRing(32, 1)
			require.NoError(t, err)

			arrivalTime := time.Now()
			ring.PushACKACK(100, arrivalTime)

			// Simulate processing delay
			time.Sleep(tc.delay)
			processTime := time.Now()

			pkt, ok := ring.TryPop()
			require.True(t, ok)

			// Timestamp should match arrival time, not process time
			packetTime := pkt.ArrivalTime()
			require.Equal(t, arrivalTime.UnixNano(), packetTime.UnixNano())
			require.True(t, packetTime.Before(processTime) || packetTime.Equal(processTime))
		})
	}
}

// TestRecvControlRing_Concurrent_Overflow_TableDriven tests concurrent overflow.
func TestRecvControlRing_Concurrent_Overflow_TableDriven(t *testing.T) {
	testCases := []struct {
		name           string
		ringSize       int
		numProducers   int
		pushesPerProd  int
		consumerActive bool
	}{
		{
			name:           "overflow_no_consumer",
			ringSize:       32,
			numProducers:   4,
			pushesPerProd:  100,
			consumerActive: false,
		},
		{
			name:           "overflow_with_consumer",
			ringSize:       32,
			numProducers:   4,
			pushesPerProd:  100,
			consumerActive: true,
		},
		{
			name:           "high_contention",
			ringSize:       16,
			numProducers:   8,
			pushesPerProd:  50,
			consumerActive: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := NewRecvControlRing(tc.ringSize, 1)
			require.NoError(t, err)

			var pushed, dropped, consumed atomic.Int64
			var wg sync.WaitGroup
			stop := make(chan struct{})
			now := time.Now()

			// Consumer
			if tc.consumerActive {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							for {
								if _, ok := ring.TryPop(); ok {
									consumed.Add(1)
								} else {
									return
								}
							}
						default:
							if _, ok := ring.TryPop(); ok {
								consumed.Add(1)
							}
						}
					}
				}()
			}

			// Producers
			for p := 0; p < tc.numProducers; p++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					for i := 0; i < tc.pushesPerProd; i++ {
						if ring.PushACKACK(uint32(id*tc.pushesPerProd+i), now) {
							pushed.Add(1)
						} else {
							dropped.Add(1)
						}
					}
				}(p)
			}

			time.Sleep(50 * time.Millisecond)
			close(stop)
			wg.Wait()

			total := int64(tc.numProducers * tc.pushesPerProd)
			t.Logf("%s: pushed=%d, dropped=%d, consumed=%d",
				tc.name, pushed.Load(), dropped.Load(), consumed.Load())

			require.Equal(t, total, pushed.Load()+dropped.Load())
			if tc.consumerActive {
				require.Equal(t, pushed.Load(), consumed.Load())
			}
		})
	}
}

// TestRecvControlPacketType_String_TableDriven tests string representation.
func TestRecvControlPacketType_String_TableDriven(t *testing.T) {
	testCases := []struct {
		typ      RecvControlPacketType
		expected string
	}{
		{RecvControlTypeACKACK, "ACKACK"},
		{RecvControlTypeKEEPALIVE, "KEEPALIVE"},
		{RecvControlPacketType(99), "UNKNOWN"},
		{RecvControlPacketType(255), "UNKNOWN"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.typ.String())
		})
	}
}

// TestRecvControlRing_ArrivalTime_TableDriven tests ArrivalTime helper.
func TestRecvControlRing_ArrivalTime_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		timestamp int64
	}{
		{"zero", 0},
		{"small", 1000000},
		{"current", time.Now().UnixNano()},
		{"large", time.Now().Add(time.Hour).UnixNano()},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pkt := RecvControlPacket{
				Type:      RecvControlTypeACKACK,
				Timestamp: tc.timestamp,
			}

			arrival := pkt.ArrivalTime()
			require.Equal(t, tc.timestamp, arrival.UnixNano())
		})
	}
}

// TestRecvControlRing_Fallback_Simulation tests fallback behavior documentation.
func TestRecvControlRing_Fallback_Simulation(t *testing.T) {
	// Simulates what happens when ring is full
	ring, err := NewRecvControlRing(4, 1)
	require.NoError(t, err)

	now := time.Now()
	pushedCount := 0
	fallbackCount := 0

	for i := 0; i < 10; i++ {
		if ring.PushACKACK(uint32(i), now) {
			pushedCount++
		} else {
			// In production: would use locked fallback path
			fallbackCount++
		}
	}

	t.Logf("Ring full simulation: pushed=%d, fallback=%d", pushedCount, fallbackCount)
	require.Greater(t, fallbackCount, 0, "expected some fallback")
	require.Equal(t, 10, pushedCount+fallbackCount)
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkRecvControlRing_Overflow_Recovery(b *testing.B) {
	ring, _ := NewRecvControlRing(64, 1)
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Push until full
		for j := 0; j < 100; j++ {
			ring.PushACKACK(uint32(j), now)
		}
		// Drain
		for {
			if _, ok := ring.TryPop(); !ok {
				break
			}
		}
	}
}

func BenchmarkRecvControlRing_ArrivalTime(b *testing.B) {
	pkt := RecvControlPacket{
		Type:      RecvControlTypeACKACK,
		Timestamp: time.Now().UnixNano(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pkt.ArrivalTime()
	}
}
