package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// MetricsCollector scrapes Prometheus metrics from server and seeker.
type MetricsCollector struct {
	serverPromUDS string
	seekerPromUDS string
	httpClient    *http.Client
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector(serverUDS, seekerUDS string) *MetricsCollector {
	return &MetricsCollector{
		serverPromUDS: serverUDS,
		seekerPromUDS: seekerUDS,
		httpClient: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

// Sample implements MetricsSource interface.
func (mc *MetricsCollector) Sample(ctx context.Context) (StabilityMetrics, error) {
	return mc.Collect(ctx)
}

// SampleSeeker collects metrics from the seeker only.
func (mc *MetricsCollector) SampleSeeker(ctx context.Context) (StabilityMetrics, error) {
	return mc.scrapeAndParse(ctx, mc.seekerPromUDS)
}

// SampleServer collects metrics from the server only.
func (mc *MetricsCollector) SampleServer(ctx context.Context) (StabilityMetrics, error) {
	return mc.scrapeAndParse(ctx, mc.serverPromUDS)
}

// Collect scrapes both endpoints and aggregates metrics.
func (mc *MetricsCollector) Collect(ctx context.Context) (StabilityMetrics, error) {
	var m StabilityMetrics
	m.Timestamp = time.Now()

	// Scrape seeker metrics (primary source for throughput)
	seekerMetrics, err := mc.scrapeAndParse(ctx, mc.seekerPromUDS)
	if err != nil {
		return m, fmt.Errorf("seeker metrics: %w", err)
	}

	// Scrape server metrics (for NAKs, gaps, RTT)
	serverMetrics, err := mc.scrapeAndParse(ctx, mc.serverPromUDS)
	if err != nil {
		return m, fmt.Errorf("server metrics: %w", err)
	}

	// Aggregate metrics
	m = mc.aggregate(seekerMetrics, serverMetrics)
	m.Timestamp = time.Now()

	return m, nil
}

// scrapeAndParse scrapes a single Prometheus endpoint.
func (mc *MetricsCollector) scrapeAndParse(ctx context.Context, socketPath string) (StabilityMetrics, error) {
	var m StabilityMetrics

	// Create HTTP client with Unix socket transport
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(dialCtx, "unix", socketPath)
			},
		},
		Timeout: 2 * time.Second,
	}

	// Validate metrics URL (gosec G704: SSRF protection)
	const metricsURL = "http://localhost/metrics"
	parsedURL, parseErr := url.Parse(metricsURL)
	if parseErr != nil || parsedURL.Scheme != "http" || (parsedURL.Hostname() != "localhost" && parsedURL.Hostname() != "127.0.0.1") {
		return m, fmt.Errorf("invalid metrics URL: must be http://localhost")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", parsedURL.String(), nil)
	if err != nil {
		return m, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return m, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			// Log but don't override main error
			fmt.Fprintf(os.Stderr, "Warning: error closing response body: %v\n", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Parse Prometheus text format
	metrics := mc.parsePrometheus(resp.Body)

	// Extract relevant metrics
	m = mc.extractMetrics(metrics)

	return m, nil
}

// parsePrometheus parses Prometheus text format into a map.
func (mc *MetricsCollector) parsePrometheus(body interface{}) map[string]float64 {
	result := make(map[string]float64)

	var scanner *bufio.Scanner
	switch v := body.(type) {
	case *bufio.Scanner:
		scanner = v
	default:
		if reader, ok := body.(interface{ Read([]byte) (int, error) }); ok {
			scanner = bufio.NewScanner(reader)
		} else {
			return result
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Skip comments and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		// Parse "metric_name value" or "metric_name{labels} value"
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		name := parts[0]
		valueStr := parts[1]

		// Handle labels: metric_name{label="value"} -> metric_name
		if idx := strings.Index(name, "{"); idx > 0 {
			name = name[:idx]
		}

		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue
		}

		result[name] = value
	}

	return result
}

// extractMetrics extracts StabilityMetrics from parsed Prometheus data.
func (mc *MetricsCollector) extractMetrics(metrics map[string]float64) StabilityMetrics {
	var m StabilityMetrics

	// Client-seeker specific metrics
	m.TargetBitrate = int64(metrics["client_seeker_target_bitrate_bps"])
	m.ActualBitrate = int64(metrics["client_seeker_actual_bitrate_bps"])
	m.PacketsSent = uint64(metrics["client_seeker_packets_generated_total"])
	m.BytesSent = uint64(metrics["client_seeker_bytes_generated_total"])

	// Connection status
	if alive, ok := metrics["client_seeker_connection_alive"]; ok {
		m.ConnectionAlive = alive == 1
	}

	// SRT metrics (from server or seeker's SRT connection)
	m.RTTMs = metrics["srt_rtt_ms"]
	m.RTTVarianceMs = metrics["srt_rtt_variance_ms"]

	// Calculate rates (these would need to be deltas in real implementation)
	// For now, use raw values
	m.GapRate = metrics["srt_recv_gap_rate"]
	m.NAKRate = metrics["srt_send_nak_rate"]

	// Calculate throughput efficiency
	if m.TargetBitrate > 0 {
		m.ThroughputTE = float64(m.ActualBitrate) / float64(m.TargetBitrate)
	}

	// ═══════════════════════════════════════════════════════════════════
	// Bottleneck Detection Metrics (from client-seeker instrumentation)
	// See: client_seeker_instrumentation_design.md
	// ═══════════════════════════════════════════════════════════════════

	// Generator efficiency
	m.GeneratorEfficiency = metrics["client_seeker_generator_efficiency"]

	// TokenBucket metrics (tool overhead)
	m.TokenBucketWaitSec = metrics["client_seeker_tokenbucket_wait_seconds_total"]
	m.TokenBucketSpinSec = metrics["client_seeker_tokenbucket_spin_seconds_total"]
	m.TokenBucketBlocked = int64(metrics["client_seeker_tokenbucket_blocked_total"])
	m.TokenBucketMode = int(metrics["client_seeker_tokenbucket_mode"])

	// Publisher write metrics (library overhead)
	m.SRTWriteSec = metrics["client_seeker_srt_write_seconds_total"]
	m.SRTWriteBlocked = int64(metrics["client_seeker_srt_write_blocked_total"])
	m.SRTWriteErrors = int64(metrics["client_seeker_srt_write_errors_total"])

	// Perform bottleneck analysis
	m.BottleneckType, m.BottleneckReason = mc.analyzeBottleneck(m, metrics)

	return m
}

// analyzeBottleneck determines the bottleneck type from metrics.
// This mirrors the decision tree in client-seeker's bottleneck.go.
//
// IMPORTANT: The analysis distinguishes between:
// - RefillSleep mode: wait time is expected and efficient (sleeping is good!)
// - RefillHybrid/Spin mode: spin time indicates CPU overhead
func (mc *MetricsCollector) analyzeBottleneck(m StabilityMetrics, raw map[string]float64) (string, string) {
	// Thresholds
	const (
		efficiencyThreshold      = 0.95
		spinOverheadThreshold    = 0.10 // Spin time > 10% of uptime = tool bottleneck
		writeBlockedThreshold    = 0.10
		tokenStarvationThreshold = 0.10
	)

	// Step 1: Check if system is healthy
	if m.GeneratorEfficiency >= efficiencyThreshold {
		return "NONE", fmt.Sprintf("Efficiency %.1f%% >= %.1f%%", m.GeneratorEfficiency*100, efficiencyThreshold*100)
	}

	// Calculate derived metrics
	uptime := raw["client_seeker_uptime_seconds"]

	// For tool overhead, only count SPIN time, not wait time
	// In RefillSleep mode, wait time is expected (sleeping is efficient)
	// In RefillHybrid/Spin mode, spin time indicates CPU overhead
	var spinOverhead float64
	if uptime > 0 {
		spinOverhead = m.TokenBucketSpinSec / uptime
	}

	writeCount := raw["client_seeker_srt_write_total"]
	var writeBlockedRate float64
	if writeCount > 0 {
		writeBlockedRate = float64(m.SRTWriteBlocked) / writeCount
	}

	tokensAvailable := raw["client_seeker_tokenbucket_tokens"]
	tokensMax := raw["client_seeker_tokenbucket_tokens_max"]
	var tokenUtilization float64
	if tokensMax > 0 {
		tokenUtilization = tokensAvailable / tokensMax
	}

	// Get mode string
	modeStr := "sleep"
	switch m.TokenBucketMode {
	case 1:
		modeStr = "hybrid"
	case 2:
		modeStr = "spin"
	}

	// Step 2: Check for SPIN overhead (only in hybrid/spin modes)
	// Sleep mode wait time is NOT overhead - it's expected behavior
	if spinOverhead > spinOverheadThreshold {
		return "TOOL-LIMITED", fmt.Sprintf("Spin overhead %.1f%% > %.1f%% (mode: %s)", spinOverhead*100, spinOverheadThreshold*100, modeStr)
	}

	// Step 3: Check for library blocking (Write() taking too long)
	if writeBlockedRate > writeBlockedThreshold {
		return "LIBRARY-LIMITED", fmt.Sprintf("Write blocked rate %.1f%% > %.1f%%", writeBlockedRate*100, writeBlockedThreshold*100)
	}

	// Step 4: Check for token starvation
	if tokenUtilization < tokenStarvationThreshold {
		return "TOOL-LIMITED", fmt.Sprintf("Token utilization %.1f%% < %.1f%% (starving, mode: %s)", tokenUtilization*100, tokenStarvationThreshold*100, modeStr)
	}

	// Step 5: If efficiency is low but no clear tool bottleneck, it's likely library-limited
	// This is the key insight: if we're not spinning, not starving, but still slow,
	// the library (SRT) is probably the bottleneck
	if m.GeneratorEfficiency < efficiencyThreshold && spinOverhead < spinOverheadThreshold {
		return "LIBRARY-LIMITED", fmt.Sprintf("Efficiency %.1f%% without tool overhead (mode: %s)", m.GeneratorEfficiency*100, modeStr)
	}

	// Step 6: Unknown
	return "UNKNOWN", fmt.Sprintf("Efficiency %.1f%% but no clear indicator (mode: %s)", m.GeneratorEfficiency*100, modeStr)
}

// aggregate combines seeker and server metrics.
func (mc *MetricsCollector) aggregate(seeker, server StabilityMetrics) StabilityMetrics {
	m := seeker // Start with seeker metrics

	// Use server metrics for NAKs and gaps (server sees retransmissions)
	if server.NAKRate > 0 {
		m.NAKRate = server.NAKRate
	}
	if server.GapRate > 0 {
		m.GapRate = server.GapRate
	}

	// RTT can come from either side
	if server.RTTMs > 0 {
		m.RTTMs = server.RTTMs
		m.RTTVarianceMs = server.RTTVarianceMs
	}

	return m
}

// ParsePrometheusText parses a Prometheus text string (for testing).
func ParsePrometheusText(text string) map[string]float64 {
	mc := &MetricsCollector{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	return mc.parsePrometheus(scanner)
}
