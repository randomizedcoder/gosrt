package metrics

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// StabilizationConfig configures the stabilization detection behavior.
type StabilizationConfig struct {
	// PollInterval is how often to check metrics (default: 100ms)
	PollInterval time.Duration

	// StableCount is how many consecutive unchanged polls required (default: 2)
	StableCount int

	// MaxWait is the maximum time to wait before giving up (default: 5s)
	MaxWait time.Duration
}

// DefaultStabilizationConfig returns sensible defaults for stabilization detection.
func DefaultStabilizationConfig() StabilizationConfig {
	return StabilizationConfig{
		PollInterval: 100 * time.Millisecond,
		StableCount:  2,
		MaxWait:      5 * time.Second,
	}
}

// StabilizationMetrics holds the 6 key counters we monitor for stabilization.
// These are the metrics that change during active data transfer and should
// stop changing once the pipeline has drained.
type StabilizationMetrics struct {
	DataSent uint64 // Packets sent (data)
	DataRecv uint64 // Packets received (data)
	AckSent  uint64 // ACKs sent
	AckRecv  uint64 // ACKs received
	NakSent  uint64 // NAKs sent (loss reports)
	NakRecv  uint64 // NAKs received (retransmit requests)
}

// GetStabilizationMetrics extracts stabilization-relevant metrics from a ConnectionMetrics.
// This aggregates across all connections in the registry.
func GetStabilizationMetricsFromRegistry() StabilizationMetrics {
	connections := GetConnections()

	var result StabilizationMetrics
	for _, info := range connections {
		if info == nil || info.Metrics == nil {
			continue
		}
		m := info.Metrics
		result.DataSent += m.PktSentDataSuccess.Load()
		result.DataRecv += m.PktRecvDataSuccess.Load()
		result.AckSent += m.PktSentACKSuccess.Load()
		result.AckRecv += m.PktRecvACKSuccess.Load()
		result.NakSent += m.PktSentNAKSuccess.Load()
		result.NakRecv += m.PktRecvNAKSuccess.Load()
	}
	return result
}

// Equal returns true if two StabilizationMetrics are identical.
// This is used to detect when metrics have stopped changing.
func (s StabilizationMetrics) Equal(other StabilizationMetrics) bool {
	return s.DataSent == other.DataSent &&
		s.DataRecv == other.DataRecv &&
		s.AckSent == other.AckSent &&
		s.AckRecv == other.AckRecv &&
		s.NakSent == other.NakSent &&
		s.NakRecv == other.NakRecv
}

// String returns a human-readable representation of the metrics.
func (s StabilizationMetrics) String() string {
	return fmt.Sprintf("data(s=%d,r=%d) ack(s=%d,r=%d) nak(s=%d,r=%d)",
		s.DataSent, s.DataRecv,
		s.AckSent, s.AckRecv,
		s.NakSent, s.NakRecv)
}

// StabilizationHandler returns an HTTP handler for the /stabilize endpoint.
// The output format is simple key=value lines, one per metric:
//
//	data_sent=12345
//	data_recv=12340
//	ack_sent=500
//	ack_recv=498
//	nak_sent=5
//	nak_recv=3
//
// This is optimized for fast generation (6 atomic loads) and fast parsing.
// Reuses metricsBuilderPool from handler.go to minimize memory overhead.
func StabilizationHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := GetStabilizationMetricsFromRegistry()

		// Reuse the same pool as /metrics handler
		b := metricsBuilderPool.Get().(*strings.Builder)
		defer func() {
			b.Reset()
			metricsBuilderPool.Put(b)
		}()

		// Build output using WriteString for efficiency
		b.WriteString("data_sent=")
		b.WriteString(strconv.FormatUint(m.DataSent, 10))
		b.WriteByte('\n')

		b.WriteString("data_recv=")
		b.WriteString(strconv.FormatUint(m.DataRecv, 10))
		b.WriteByte('\n')

		b.WriteString("ack_sent=")
		b.WriteString(strconv.FormatUint(m.AckSent, 10))
		b.WriteByte('\n')

		b.WriteString("ack_recv=")
		b.WriteString(strconv.FormatUint(m.AckRecv, 10))
		b.WriteByte('\n')

		b.WriteString("nak_sent=")
		b.WriteString(strconv.FormatUint(m.NakSent, 10))
		b.WriteByte('\n')

		b.WriteString("nak_recv=")
		b.WriteString(strconv.FormatUint(m.NakRecv, 10))
		b.WriteByte('\n')

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, b.String())
	})
}

// ParseStabilizationResponse parses the response from /stabilize endpoint.
// Returns error if any required metric is missing or malformed.
func ParseStabilizationResponse(response string) (StabilizationMetrics, error) {
	var result StabilizationMetrics
	found := make(map[string]bool)

	scanner := bufio.NewScanner(strings.NewReader(response))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return StabilizationMetrics{}, fmt.Errorf("invalid line: %q", line)
		}

		key := parts[0]
		value, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return StabilizationMetrics{}, fmt.Errorf("invalid value for %s: %v", key, err)
		}

		found[key] = true
		switch key {
		case "data_sent":
			result.DataSent = value
		case "data_recv":
			result.DataRecv = value
		case "ack_sent":
			result.AckSent = value
		case "ack_recv":
			result.AckRecv = value
		case "nak_sent":
			result.NakSent = value
		case "nak_recv":
			result.NakRecv = value
		default:
			// Unknown key - ignore for forward compatibility
		}
	}

	if err := scanner.Err(); err != nil {
		return StabilizationMetrics{}, fmt.Errorf("scanning response: %w", err)
	}

	// Verify all required keys are present
	required := []string{"data_sent", "data_recv", "ack_sent", "ack_recv", "nak_sent", "nak_recv"}
	for _, key := range required {
		if !found[key] {
			return StabilizationMetrics{}, fmt.Errorf("missing required metric: %s", key)
		}
	}

	return result, nil
}

// MetricsGetter is a function that returns current stabilization metrics.
// Used to abstract over different ways of getting metrics (HTTP/UDS fetch).
type MetricsGetter func() (StabilizationMetrics, error)

// StabilizationResult contains the result of waiting for stabilization.
type StabilizationResult struct {
	// Stable indicates whether stabilization was achieved
	Stable bool

	// Elapsed is the time spent waiting
	Elapsed time.Duration

	// Iterations is the number of poll cycles performed
	Iterations int

	// FinalMetrics contains the last metrics snapshot from each getter
	FinalMetrics []StabilizationMetrics

	// Error is set if stabilization failed due to an error (not timeout)
	Error error
}

// WaitForStabilization waits until metrics from all getters stop changing.
// It polls each getter at the configured interval and checks if all metrics
// are unchanged for the required number of consecutive polls.
//
// Returns a StabilizationResult indicating whether stabilization was achieved.
// The context can be used to cancel the wait early.
func WaitForStabilization(ctx context.Context, cfg StabilizationConfig, getters ...MetricsGetter) StabilizationResult {
	// Apply defaults if not set
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 100 * time.Millisecond
	}
	if cfg.StableCount <= 0 {
		cfg.StableCount = 2
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 5 * time.Second
	}

	if len(getters) == 0 {
		return StabilizationResult{
			Stable:  true,
			Elapsed: 0,
		}
	}

	start := time.Now()
	deadline := start.Add(cfg.MaxWait)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Get initial snapshot from all getters
	prevSnapshots := make([]StabilizationMetrics, len(getters))
	for i, getter := range getters {
		m, err := getter()
		if err != nil {
			return StabilizationResult{
				Stable:  false,
				Elapsed: time.Since(start),
				Error:   fmt.Errorf("initial fetch from getter %d: %w", i, err),
			}
		}
		prevSnapshots[i] = m
	}

	stableCount := 0
	iterations := 0

	for {
		select {
		case <-ctx.Done():
			return StabilizationResult{
				Stable:       false,
				Elapsed:      time.Since(start),
				Iterations:   iterations,
				FinalMetrics: prevSnapshots,
				Error:        ctx.Err(),
			}

		case <-ticker.C:
			iterations++

			if time.Now().After(deadline) {
				return StabilizationResult{
					Stable:       false,
					Elapsed:      time.Since(start),
					Iterations:   iterations,
					FinalMetrics: prevSnapshots,
					Error:        fmt.Errorf("stabilization timeout after %v", cfg.MaxWait),
				}
			}

			// Get current snapshots and check if all are unchanged
			allStable := true
			for i, getter := range getters {
				current, err := getter()
				if err != nil {
					return StabilizationResult{
						Stable:       false,
						Elapsed:      time.Since(start),
						Iterations:   iterations,
						FinalMetrics: prevSnapshots,
						Error:        fmt.Errorf("fetch from getter %d: %w", i, err),
					}
				}

				if !current.Equal(prevSnapshots[i]) {
					allStable = false
					prevSnapshots[i] = current
				}
			}

			if allStable {
				stableCount++
				if stableCount >= cfg.StableCount {
					// SUCCESS - all metrics stable for required consecutive polls
					return StabilizationResult{
						Stable:       true,
						Elapsed:      time.Since(start),
						Iterations:   iterations,
						FinalMetrics: prevSnapshots,
					}
				}
			} else {
				stableCount = 0 // Reset on any change
			}
		}
	}
}

// NewHTTPGetter creates a MetricsGetter that fetches from an HTTP URL.
// The URL should point to the /stabilize endpoint.
//
// Example:
//
//	getter := metrics.NewHTTPGetter("http://localhost:5101/stabilize")
func NewHTTPGetter(url string) MetricsGetter {
	client := &http.Client{Timeout: 2 * time.Second}
	return func() (StabilizationMetrics, error) {
		resp, err := client.Get(url)
		if err != nil {
			return StabilizationMetrics{}, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return StabilizationMetrics{}, fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		// Read response body
		buf := make([]byte, 256) // 6 lines * ~20 chars each = ~120 bytes max
		n, err := resp.Body.Read(buf)
		if err != nil && err.Error() != "EOF" {
			return StabilizationMetrics{}, err
		}

		return ParseStabilizationResponse(string(buf[:n]))
	}
}

// NewUDSGetter creates a MetricsGetter that fetches from a Unix Domain Socket.
// The socketPath should be the path to the UDS file.
//
// Example:
//
//	getter := metrics.NewUDSGetter("/tmp/server-metrics.sock")
func NewUDSGetter(socketPath string) MetricsGetter {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 2 * time.Second,
	}
	return func() (StabilizationMetrics, error) {
		// For UDS, the host in the URL doesn't matter, but we need a valid URL
		resp, err := client.Get("http://localhost/stabilize")
		if err != nil {
			return StabilizationMetrics{}, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return StabilizationMetrics{}, fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		// Read response body
		buf := make([]byte, 256) // 6 lines * ~20 chars each = ~120 bytes max
		n, err := resp.Body.Read(buf)
		if err != nil && err.Error() != "EOF" {
			return StabilizationMetrics{}, err
		}

		return ParseStabilizationResponse(string(buf[:n]))
	}
}

// AggregateStabilizationMetrics combines metrics from multiple sources into one.
// This is useful when you want to check if all components have stabilized together.
func AggregateStabilizationMetrics(all ...StabilizationMetrics) StabilizationMetrics {
	var result StabilizationMetrics
	for _, m := range all {
		result.DataSent += m.DataSent
		result.DataRecv += m.DataRecv
		result.AckSent += m.AckSent
		result.AckRecv += m.AckRecv
		result.NakSent += m.NakSent
		result.NakRecv += m.NakRecv
	}
	return result
}
