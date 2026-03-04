package send

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Backoff Mode Benchmarks
//
// These benchmarks test the hypothesis that time.Sleep() is the bottleneck
// at high throughput by comparing different waiting strategies:
//
// 1. Sleep mode (current): time.Sleep() - lowest CPU, highest latency
// 2. Yield mode: runtime.Gosched() - medium CPU, medium latency
// 3. Spin mode: busy loop - highest CPU, lowest latency
// 4. NoWait mode: no waiting at all - maximum throughput baseline
//
// If throughput increases significantly with NoWait/Spin, the hypothesis is confirmed.
// ═══════════════════════════════════════════════════════════════════════════════

// WaitMode represents different waiting strategies
type WaitMode int

const (
	WaitModeSleep WaitMode = iota
	WaitModeYield
	WaitModeSpin
	WaitModeNone
)

func (m WaitMode) String() string {
	switch m {
	case WaitModeSleep:
		return "Sleep"
	case WaitModeYield:
		return "Yield"
	case WaitModeSpin:
		return "Spin"
	case WaitModeNone:
		return "NoWait"
	default:
		return "Unknown"
	}
}

// simulateWait performs the wait based on mode
func simulateWait(mode WaitMode, duration time.Duration) {
	switch mode {
	case WaitModeSleep:
		time.Sleep(duration)
	case WaitModeYield:
		// Yield approximately the same "time" as sleep would take
		iterations := int(duration.Microseconds() / 10)
		if iterations < 1 {
			iterations = 1
		}
		for i := 0; i < iterations; i++ {
			runtime.Gosched()
		}
	case WaitModeSpin:
		// Spin wait with periodic yields
		deadline := time.Now().Add(duration)
		i := 0
		for time.Now().Before(deadline) {
			i++
			if i%1000 == 0 {
				runtime.Gosched()
			}
		}
	case WaitModeNone:
		// No wait - immediate return
	}
}

// BenchmarkBackoffModes compares throughput with different wait modes
func BenchmarkBackoffModes(b *testing.B) {
	modes := []struct {
		mode     WaitMode
		duration time.Duration
	}{
		{WaitModeNone, 0},
		{WaitModeSpin, 10 * time.Microsecond},
		{WaitModeYield, 10 * time.Microsecond},
		{WaitModeSleep, 10 * time.Microsecond},
		{WaitModeSleep, 100 * time.Microsecond},
		{WaitModeSleep, 1 * time.Millisecond},
	}

	for _, tc := range modes {
		name := tc.mode.String()
		if tc.duration > 0 {
			name += "_" + tc.duration.String()
		}

		b.Run(name, func(b *testing.B) {
			var ops int64
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// Simulate EventLoop iteration
				simulateWait(tc.mode, tc.duration)
				atomic.AddInt64(&ops, 1)
			}

			b.ReportMetric(float64(ops)/b.Elapsed().Seconds(), "ops/sec")
		})
	}
}

// BenchmarkEventLoopThroughput measures actual EventLoop iteration rate
// with different backoff configurations
func BenchmarkEventLoopThroughput(b *testing.B) {
	configs := []struct {
		name     string
		minSleep time.Duration
		maxSleep time.Duration
	}{
		{"Current_10us_1ms", 10 * time.Microsecond, 1 * time.Millisecond},
		{"Aggressive_1us_100us", 1 * time.Microsecond, 100 * time.Microsecond},
		{"UltraAggressive_1us_10us", 1 * time.Microsecond, 10 * time.Microsecond},
		{"NoSleep_0_0", 0, 0},
	}

	for _, tc := range configs {
		b.Run(tc.name, func(b *testing.B) {
			// Create a minimal sender for benchmarking
			m := metrics.NewConnectionMetrics()

			s := &sender{
				metrics:          m,
				backoffMinSleep:  tc.minSleep,
				backoffMaxSleep:  tc.maxSleep,
				tsbpdSleepFactor: 0.9,
				useEventLoop:     true,
			}

			var iterations int64
			b.ResetTimer()

			// Simulate EventLoop iterations
			for i := 0; i < b.N; i++ {
				// Calculate sleep (simulating empty btree case)
				result := s.calculateTsbpdSleepDuration(
					0, // nextDeliveryIn (no packets)
					0, // deliveredCount
					0, // controlDrained
					tc.minSleep,
					tc.maxSleep,
				)

				// Wait using current implementation
				if result.Duration > 0 {
					time.Sleep(result.Duration)
				}

				atomic.AddInt64(&iterations, 1)
			}

			rate := float64(iterations) / b.Elapsed().Seconds()
			b.ReportMetric(rate, "iterations/sec")

			// At 500 Mb/s with 1456-byte packets, we need ~43K packets/sec
			// EventLoop should iterate faster than this
			b.Logf("Iteration rate: %.0f/sec (need >43K for 500 Mb/s)", rate)
		})
	}
}

// TestBackoffHypothesis tests if removing sleep increases iteration rate
func TestBackoffHypothesis(t *testing.T) {
	duration := 100 * time.Millisecond

	tests := []struct {
		name     string
		mode     WaitMode
		waitTime time.Duration
	}{
		{"NoWait", WaitModeNone, 0},
		{"Spin_10us", WaitModeSpin, 10 * time.Microsecond},
		{"Yield_10us", WaitModeYield, 10 * time.Microsecond},
		{"Sleep_10us", WaitModeSleep, 10 * time.Microsecond},
		{"Sleep_100us", WaitModeSleep, 100 * time.Microsecond},
		{"Sleep_1ms", WaitModeSleep, 1 * time.Millisecond},
	}

	results := make(map[string]float64)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var iterations int64

			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()

			start := time.Now()
			for {
				select {
				case <-ctx.Done():
					elapsed := time.Since(start)
					rate := float64(iterations) / elapsed.Seconds()
					results[tc.name] = rate
					t.Logf("%s: %.0f iterations/sec", tc.name, rate)
					return
				default:
					simulateWait(tc.mode, tc.waitTime)
					iterations++
				}
			}
		})
	}

	// Compare results
	t.Log("\n=== HYPOTHESIS TEST RESULTS ===")
	t.Log("If NoWait >> Sleep, then sleep is the bottleneck")
	t.Log("")

	baseline := results["Sleep_100us"]
	for name, rate := range results {
		improvement := (rate - baseline) / baseline * 100
		t.Logf("%-15s: %10.0f iterations/sec (%+.0f%% vs Sleep_100us)",
			name, rate, improvement)
	}

	// Verify hypothesis: NoWait should be MUCH faster than Sleep
	switch {
	case results["NoWait"] > results["Sleep_100us"]*10:
		t.Log("\n✅ HYPOTHESIS CONFIRMED: Removing sleep increases throughput 10x+")
		t.Log("   Recommendation: Implement spin/yield mode for high throughput")
	case results["NoWait"] > results["Sleep_100us"]*2:
		t.Log("\n🟡 HYPOTHESIS PARTIALLY CONFIRMED: Sleep adds significant overhead")
		t.Log("   Recommendation: Consider yield mode for high throughput")
	default:
		t.Log("\n❌ HYPOTHESIS NOT CONFIRMED: Sleep is not the primary bottleneck")
		t.Log("   Need to investigate other causes")
	}
}

// TestRealEventLoopIteration tests actual EventLoop iteration rate
// This creates a real sender and measures how fast it can iterate
func TestRealEventLoopIteration(t *testing.T) {
	// Create sender with different configs
	configs := []struct {
		name     string
		minSleep time.Duration
		maxSleep time.Duration
	}{
		{"Default", 10 * time.Microsecond, 1 * time.Millisecond},
		{"Aggressive", 1 * time.Microsecond, 10 * time.Microsecond},
	}

	for _, tc := range configs {
		t.Run(tc.name, func(t *testing.T) {
			m := metrics.NewConnectionMetrics()

			// Create minimal sender
			s := &sender{
				metrics:          m,
				packetBtree:      nil, // No btree - simulates empty
				useEventLoop:     true,
				backoffMinSleep:  tc.minSleep,
				backoffMaxSleep:  tc.maxSleep,
				tsbpdSleepFactor: 0.9,
			}

			// Initialize deliveryStartPoint
			s.deliveryStartPoint.Store(0)

			// Run for fixed duration
			duration := 200 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()

			var iterations int64

			// Simplified EventLoop - just measure iteration rate
			for {
				select {
				case <-ctx.Done():
					goto done
				default:
					// Simulate one EventLoop iteration
					result := s.calculateTsbpdSleepDuration(0, 0, 0, tc.minSleep, tc.maxSleep)
					if result.Duration > 0 {
						time.Sleep(result.Duration)
					}
					iterations++
					m.SendEventLoopIterations.Add(1)
				}
			}
		done:

			rate := float64(iterations) / duration.Seconds()

			t.Logf("%s: %d iterations in %v = %.0f/sec",
				tc.name, iterations, duration, rate)

			// At 500 Mb/s we need ~43K packets/sec
			// EventLoop must iterate faster than packet arrival rate
			if rate < 43000 {
				t.Logf("⚠️  WARNING: Iteration rate %.0f/sec < 43K (500 Mb/s requirement)", rate)
			}
		})
	}
}
