package main

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/datarhei/gosrt/contrib/common"
	"github.com/datarhei/gosrt/metrics"
)

// MetricsSnapshot represents a single snapshot of Prometheus metrics
type MetricsSnapshot struct {
	Timestamp time.Time          // When the snapshot was taken
	Point     string             // Snapshot point identifier (e.g., "startup", "mid-test", "pre-shutdown")
	Metrics   map[string]float64 // Parsed metric values (metric name -> value)
	Raw       string             // Raw Prometheus format response
	Error     error              // Error if collection failed
}

// ComponentMetrics holds metrics for a single component
type ComponentMetrics struct {
	Component string             // Component identifier (server, client-generator, client)
	Endpoint  MetricsEndpoint    // Metrics endpoint configuration
	Snapshots []*MetricsSnapshot // Collected snapshots
}

// TestMetrics holds metrics for all components in a test
type TestMetrics struct {
	Server          ComponentMetrics
	ClientGenerator ComponentMetrics
	Client          ComponentMetrics
	client          *common.MetricsClient // Reusable metrics client
}

// NewTestMetrics creates a new TestMetrics instance with the given endpoints
func NewTestMetrics(serverEndpoint, clientGenEndpoint, clientEndpoint MetricsEndpoint) *TestMetrics {
	return &TestMetrics{
		Server: ComponentMetrics{
			Component: "server",
			Endpoint:  serverEndpoint,
			Snapshots: make([]*MetricsSnapshot, 0),
		},
		ClientGenerator: ComponentMetrics{
			Component: "client-generator",
			Endpoint:  clientGenEndpoint,
			Snapshots: make([]*MetricsSnapshot, 0),
		},
		Client: ComponentMetrics{
			Component: "client",
			Endpoint:  clientEndpoint,
			Snapshots: make([]*MetricsSnapshot, 0),
		},
		client: common.NewMetricsClient(),
	}
}

// NewTestMetricsFromURLs creates a new TestMetrics instance with HTTP URLs (backward compatibility)
func NewTestMetricsFromURLs(serverURL, clientGenURL, clientURL string) *TestMetrics {
	return NewTestMetrics(
		MetricsEndpoint{HTTPAddr: extractAddr(serverURL)},
		MetricsEndpoint{HTTPAddr: extractAddr(clientGenURL)},
		MetricsEndpoint{HTTPAddr: extractAddr(clientURL)},
	)
}

// extractAddr extracts the address from a URL (removes http:// prefix and /metrics suffix)
func extractAddr(url string) string {
	addr := strings.TrimPrefix(url, "http://")
	addr = strings.TrimSuffix(addr, "/metrics")
	return addr
}

// CollectMetricsFromEndpoint fetches metrics from an endpoint (HTTP or UDS)
func CollectMetricsFromEndpoint(client *common.MetricsClient, endpoint MetricsEndpoint, point string) *MetricsSnapshot {
	snapshot := &MetricsSnapshot{
		Timestamp: time.Now(),
		Point:     point,
		Metrics:   make(map[string]float64),
	}

	if !endpoint.IsConfigured() {
		snapshot.Error = fmt.Errorf("no metrics endpoint configured")
		return snapshot
	}

	var body []byte
	var err error

	// Prefer UDS if configured (works across network namespaces)
	if endpoint.UDSPath != "" {
		body, err = client.FetchUDS(endpoint.UDSPath)
	} else {
		body, err = client.FetchHTTP(endpoint.HTTPAddr)
	}

	if err != nil {
		snapshot.Error = fmt.Errorf("failed to fetch metrics: %w", err)
		return snapshot
	}

	snapshot.Raw = string(body)
	snapshot.Metrics = parsePrometheusMetrics(snapshot.Raw)

	return snapshot
}

// parsePrometheusMetrics parses Prometheus text format into a map of metric values
func parsePrometheusMetrics(raw string) map[string]float64 {
	metrics := make(map[string]float64)

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse metric line: metric_name{labels} value
		// or: metric_name value
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		name := parts[0]
		valueStr := parts[1]

		// Handle labels: extract metric name without labels for lookup
		if idx := strings.Index(name, "{"); idx != -1 {
			// Keep full name with labels as key
			// This allows distinguishing metrics with different labels
		}

		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue
		}

		metrics[name] = value
	}

	return metrics
}

// CollectAllMetrics collects metrics from all components in parallel
func (tm *TestMetrics) CollectAllMetrics(point string) {
	var wg sync.WaitGroup

	// Collect from server
	wg.Add(1)
	go func() {
		defer wg.Done()
		if tm.Server.Endpoint.IsConfigured() {
			snapshot := CollectMetricsFromEndpoint(tm.client, tm.Server.Endpoint, point)
			tm.Server.Snapshots = append(tm.Server.Snapshots, snapshot)
		}
	}()

	// Collect from client-generator
	wg.Add(1)
	go func() {
		defer wg.Done()
		if tm.ClientGenerator.Endpoint.IsConfigured() {
			snapshot := CollectMetricsFromEndpoint(tm.client, tm.ClientGenerator.Endpoint, point)
			tm.ClientGenerator.Snapshots = append(tm.ClientGenerator.Snapshots, snapshot)
		}
	}()

	// Collect from client
	wg.Add(1)
	go func() {
		defer wg.Done()
		if tm.Client.Endpoint.IsConfigured() {
			snapshot := CollectMetricsFromEndpoint(tm.client, tm.Client.Endpoint, point)
			tm.Client.Snapshots = append(tm.Client.Snapshots, snapshot)
		}
	}()

	wg.Wait()
}

// ErrorCounters is a list of error counter metric names to check
var ErrorCounters = []string{
	"gosrt_pkt_sent_error_total",
	"gosrt_pkt_recv_error_total",
	"gosrt_pkt_drop_total",
	"gosrt_crypto_error_encrypt_total",
	"gosrt_crypto_error_generate_sek_total",
	"gosrt_crypto_error_marshal_km_total",
}

// VerifyNoErrors checks that no error counters have incremented
func (tm *TestMetrics) VerifyNoErrors() error {
	var errors []string

	// Check each component
	for _, cm := range []*ComponentMetrics{&tm.Server, &tm.ClientGenerator, &tm.Client} {
		if len(cm.Snapshots) < 2 {
			continue
		}

		first := cm.Snapshots[0]
		last := cm.Snapshots[len(cm.Snapshots)-1]

		if first.Error != nil || last.Error != nil {
			continue
		}

		for _, counter := range ErrorCounters {
			firstVal := first.Metrics[counter]
			lastVal := last.Metrics[counter]

			if lastVal > firstVal {
				errors = append(errors, fmt.Sprintf(
					"%s: %s increased from %.0f to %.0f",
					cm.Component, counter, firstVal, lastVal,
				))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("error counters increased:\n  %s", strings.Join(errors, "\n  "))
	}

	return nil
}

// GetMetricDelta returns the change in a metric value between first and last snapshots
func (cm *ComponentMetrics) GetMetricDelta(metricName string) (float64, error) {
	if len(cm.Snapshots) < 2 {
		return 0, fmt.Errorf("not enough snapshots")
	}

	first := cm.Snapshots[0]
	last := cm.Snapshots[len(cm.Snapshots)-1]

	if first.Error != nil {
		return 0, first.Error
	}
	if last.Error != nil {
		return 0, last.Error
	}

	return last.Metrics[metricName] - first.Metrics[metricName], nil
}

// PrintSummary prints a summary of collected metrics
func (tm *TestMetrics) PrintSummary() {
	fmt.Println("\n=== Metrics Summary ===")

	for _, cm := range []*ComponentMetrics{&tm.Server, &tm.ClientGenerator, &tm.Client} {
		fmt.Printf("\n%s (%s):\n", cm.Component, cm.Endpoint.String())
		fmt.Printf("  Snapshots collected: %d\n", len(cm.Snapshots))

		if len(cm.Snapshots) == 0 {
			continue
		}

		// Count successful and failed snapshots
		successCount := 0
		failCount := 0
		var lastSuccessful *MetricsSnapshot
		for i := range cm.Snapshots {
			if cm.Snapshots[i].Error != nil {
				failCount++
			} else {
				successCount++
				lastSuccessful = cm.Snapshots[i]
			}
		}

		fmt.Printf("  Successful: %d, Failed: %d\n", successCount, failCount)

		// Show stats from the last successful snapshot
		if lastSuccessful != nil {
			fmt.Printf("  Metrics in last successful snapshot: %d\n", len(lastSuccessful.Metrics))

			// Print some key metrics
			keyMetrics := []string{
				"gosrt_pkt_sent_total",
				"gosrt_pkt_recv_total",
				"gosrt_pkt_retrans_total",
			}
			for _, m := range keyMetrics {
				if v, ok := lastSuccessful.Metrics[m]; ok {
					fmt.Printf("  %s: %.0f\n", m, v)
				}
			}
		}

		// Note if there were any errors
		if failCount > 0 {
			// Find the last error for context
			for i := len(cm.Snapshots) - 1; i >= 0; i-- {
				if cm.Snapshots[i].Error != nil {
					fmt.Printf("  Note: %d collection(s) failed (last error: %v)\n", failCount, cm.Snapshots[i].Error)
					break
				}
			}
		}
	}
}

// =============================================================================
// Stabilization Detection
// =============================================================================

// CreateStabilizationGetter creates a MetricsGetter for a component's /stabilize endpoint.
// Returns nil if the endpoint is not configured.
func CreateStabilizationGetter(endpoint MetricsEndpoint) metrics.MetricsGetter {
	if !endpoint.IsConfigured() {
		return nil
	}

	// Prefer UDS if configured (works across network namespaces)
	if endpoint.UDSPath != "" {
		return metrics.NewUDSGetter(endpoint.UDSPath)
	}

	// Fall back to HTTP
	url := fmt.Sprintf("http://%s/stabilize", endpoint.HTTPAddr)
	return metrics.NewHTTPGetter(url)
}

// GetAllStabilizationGetters returns MetricsGetters for all configured components.
func (tm *TestMetrics) GetAllStabilizationGetters() []metrics.MetricsGetter {
	var getters []metrics.MetricsGetter

	if getter := CreateStabilizationGetter(tm.Server.Endpoint); getter != nil {
		getters = append(getters, getter)
	}
	if getter := CreateStabilizationGetter(tm.ClientGenerator.Endpoint); getter != nil {
		getters = append(getters, getter)
	}
	if getter := CreateStabilizationGetter(tm.Client.Endpoint); getter != nil {
		getters = append(getters, getter)
	}

	return getters
}

// GetLastSnapshot returns the last snapshot for a component, or nil if none.
// component should be one of: "server", "client-generator", "client"
func (tm *TestMetrics) GetLastSnapshot(component string) *MetricsSnapshot {
	var cm *ComponentMetrics
	switch component {
	case "server":
		cm = &tm.Server
	case "client-generator", "clientgen":
		cm = &tm.ClientGenerator
	case "client":
		cm = &tm.Client
	default:
		return nil
	}

	if len(cm.Snapshots) == 0 {
		return nil
	}
	return cm.Snapshots[len(cm.Snapshots)-1]
}

// GetSnapshotByLabel returns a snapshot with the given point label for a component, or nil if not found.
// component should be one of: "server", "client-generator", "client"
// label should be one of: "startup", "mid-test", "pre-shutdown", "final"
func (tm *TestMetrics) GetSnapshotByLabel(component, label string) *MetricsSnapshot {
	var cm *ComponentMetrics
	switch component {
	case "server":
		cm = &tm.Server
	case "client-generator", "clientgen":
		cm = &tm.ClientGenerator
	case "client":
		cm = &tm.Client
	default:
		return nil
	}

	// Search backwards to find the most recent snapshot with this point
	for i := len(cm.Snapshots) - 1; i >= 0; i-- {
		if cm.Snapshots[i].Point == label {
			return cm.Snapshots[i]
		}
	}
	return nil
}

// WaitForStabilization waits for all components' metrics to stabilize.
// This should be called after pausing data generation (SIGUSR1 to client-generator)
// to detect when ACKs, NAKs, and retransmissions have completed.
func (tm *TestMetrics) WaitForStabilization(ctx context.Context) metrics.StabilizationResult {
	getters := tm.GetAllStabilizationGetters()

	if len(getters) == 0 {
		return metrics.StabilizationResult{
			Stable:  true,
			Elapsed: 0,
		}
	}

	cfg := metrics.DefaultStabilizationConfig()
	return metrics.WaitForStabilization(ctx, cfg, getters...)
}

// WaitForStabilizationWithConfig waits for all components' metrics to stabilize
// with a custom configuration.
func (tm *TestMetrics) WaitForStabilizationWithConfig(ctx context.Context, cfg metrics.StabilizationConfig) metrics.StabilizationResult {
	getters := tm.GetAllStabilizationGetters()

	if len(getters) == 0 {
		return metrics.StabilizationResult{
			Stable:  true,
			Elapsed: 0,
		}
	}

	return metrics.WaitForStabilization(ctx, cfg, getters...)
}

// =============================================================================
// Verbose Metrics Delta - Detailed per-connection analysis
// =============================================================================

// VerboseMetricsDelta holds detailed delta information for verbose output
type VerboseMetricsDelta struct {
	// Connection 1: ClientGenerator → Server
	Conn1 struct {
		// Sender (CG) deltas
		SenderPacketsSent   int64
		SenderPacketsUnique int64
		SenderRetransSent   int64
		SenderNAKsRecv      int64
		SenderACKsRecv      int64

		// Sender NAK detail (RFC SRT Appendix A)
		SenderNAKSingleRecv int64 // Single packet NAK entries received
		SenderNAKRangeRecv  int64 // Range NAK entries received
		SenderNAKPktsRecv   int64 // Total packets requested in received NAKs

		// Receiver (Server) deltas
		ReceiverPacketsRecv     int64
		ReceiverGapsDetected    int64
		ReceiverRetransRecv     int64
		ReceiverNAKsSent        int64
		ReceiverACKsSent        int64
		ReceiverDrops           int64
		ReceiverDropsTooLate    int64
		ReceiverDropsBufFull    int64
		ReceiverDropsDupes      int64 // Duplicate packets (already in buffer) - KEY METRIC for over-NAKing
		ReceiverDropsAlreadyAck int64 // Packets arrived after ACK advanced past them

		// Receiver NAK detail (RFC SRT Appendix A)
		ReceiverNAKSingleSent int64 // Single packet NAK entries sent
		ReceiverNAKRangeSent  int64 // Range NAK entries sent
		ReceiverNAKPktsSent   int64 // Total packets requested via sent NAKs

		// Balance checks
		NAKsSentVsRecv      int64 // Server.NAKsSent - CG.NAKsRecv (should be ~0)
		RetransSentVsRecv   int64 // CG.RetransSent - Server.RetransRecv (should be ~0)
		NAKPktsReqVsRetrans int64 // NAK packets requested - retransmissions sent
		DupesVsOverRequest  int64 // Duplicates should ≈ NAKPktsReq - GapsDetected (over-NAK confirmation)
	}
}

// PrintVerboseMetricsDelta prints detailed per-connection deltas between two snapshots
// Focus on Connection 1 (ClientGenerator → Server) to understand the NAK/retransmission flow
func (tm *TestMetrics) PrintVerboseMetricsDelta(prevIndex, currIndex int) {
	if len(tm.Server.Snapshots) <= currIndex || len(tm.ClientGenerator.Snapshots) <= currIndex {
		return
	}

	cgPrev := tm.ClientGenerator.Snapshots[prevIndex]
	cgCurr := tm.ClientGenerator.Snapshots[currIndex]
	serverPrev := tm.Server.Snapshots[prevIndex]
	serverCurr := tm.Server.Snapshots[currIndex]

	if cgPrev.Error != nil || cgCurr.Error != nil || serverPrev.Error != nil || serverCurr.Error != nil {
		fmt.Println("  [verbose] Skipped - collection errors")
		return
	}

	// Helper to get delta for a metric
	getDelta := func(curr, prev *MetricsSnapshot, prefix, contains string) int64 {
		var currSum, prevSum float64
		for name, val := range curr.Metrics {
			if strings.HasPrefix(name, prefix) && (contains == "" || strings.Contains(name, contains)) {
				currSum += val
			}
		}
		for name, val := range prev.Metrics {
			if strings.HasPrefix(name, prefix) && (contains == "" || strings.Contains(name, contains)) {
				prevSum += val
			}
		}
		return int64(currSum - prevSum)
	}

	// Calculate Connection 1 deltas (CG → Server)
	delta := VerboseMetricsDelta{}

	// CG as SENDER
	delta.Conn1.SenderPacketsSent = getDelta(cgCurr, cgPrev,
		"gosrt_connection_congestion_packets_total", "direction=\"send\"")
	delta.Conn1.SenderPacketsUnique = getDelta(cgCurr, cgPrev,
		"gosrt_connection_congestion_packets_unique_total", "direction=\"send\"")
	delta.Conn1.SenderRetransSent = getDelta(cgCurr, cgPrev,
		"gosrt_connection_congestion_retransmissions_total", "direction=\"send\"")
	delta.Conn1.SenderNAKsRecv = getDelta(cgCurr, cgPrev,
		"gosrt_connection_packets_received_total", "type=\"nak\"")
	delta.Conn1.SenderACKsRecv = getDelta(cgCurr, cgPrev,
		"gosrt_connection_packets_received_total", "type=\"ack\"")

	// CG NAK detail (RFC SRT Appendix A - received NAKs)
	delta.Conn1.SenderNAKSingleRecv = getDelta(cgCurr, cgPrev,
		"gosrt_connection_nak_entries_total", "direction=\"recv\",type=\"single\"")
	delta.Conn1.SenderNAKRangeRecv = getDelta(cgCurr, cgPrev,
		"gosrt_connection_nak_entries_total", "direction=\"recv\",type=\"range\"")
	delta.Conn1.SenderNAKPktsRecv = getDelta(cgCurr, cgPrev,
		"gosrt_connection_nak_packets_requested_total", "direction=\"recv\"")

	// Server as RECEIVER
	delta.Conn1.ReceiverPacketsRecv = getDelta(serverCurr, serverPrev,
		"gosrt_connection_congestion_packets_total", "direction=\"recv\"")
	delta.Conn1.ReceiverGapsDetected = getDelta(serverCurr, serverPrev,
		"gosrt_connection_congestion_packets_lost_total", "direction=\"recv\"")
	delta.Conn1.ReceiverRetransRecv = getDelta(serverCurr, serverPrev,
		"gosrt_connection_congestion_retransmissions_total", "direction=\"recv\"")
	delta.Conn1.ReceiverNAKsSent = getDelta(serverCurr, serverPrev,
		"gosrt_connection_packets_sent_total", "type=\"nak\"")
	delta.Conn1.ReceiverACKsSent = getDelta(serverCurr, serverPrev,
		"gosrt_connection_packets_sent_total", "type=\"ack\"")
	delta.Conn1.ReceiverDrops = getDelta(serverCurr, serverPrev,
		"gosrt_connection_congestion_recv_data_drop_total", "")
	delta.Conn1.ReceiverDropsTooLate = getDelta(serverCurr, serverPrev,
		"gosrt_connection_congestion_recv_data_drop_total", "reason=\"too_old\"") // Fixed: was "too_late"
	delta.Conn1.ReceiverDropsBufFull = getDelta(serverCurr, serverPrev,
		"gosrt_connection_congestion_recv_data_drop_total", "reason=\"store_insert_failed\"") // Fixed: was "buffer_full"
	delta.Conn1.ReceiverDropsDupes = getDelta(serverCurr, serverPrev,
		"gosrt_connection_congestion_recv_data_drop_total", "reason=\"duplicate\"")
	delta.Conn1.ReceiverDropsAlreadyAck = getDelta(serverCurr, serverPrev,
		"gosrt_connection_congestion_recv_data_drop_total", "reason=\"already_acked\"")

	// Server NAK detail (RFC SRT Appendix A - sent NAKs)
	delta.Conn1.ReceiverNAKSingleSent = getDelta(serverCurr, serverPrev,
		"gosrt_connection_nak_entries_total", "direction=\"sent\",type=\"single\"")
	delta.Conn1.ReceiverNAKRangeSent = getDelta(serverCurr, serverPrev,
		"gosrt_connection_nak_entries_total", "direction=\"sent\",type=\"range\"")
	delta.Conn1.ReceiverNAKPktsSent = getDelta(serverCurr, serverPrev,
		"gosrt_connection_nak_packets_requested_total", "direction=\"sent\"")

	// Balance checks
	delta.Conn1.NAKsSentVsRecv = delta.Conn1.ReceiverNAKsSent - delta.Conn1.SenderNAKsRecv
	delta.Conn1.RetransSentVsRecv = delta.Conn1.SenderRetransSent - delta.Conn1.ReceiverRetransRecv
	delta.Conn1.NAKPktsReqVsRetrans = delta.Conn1.ReceiverNAKPktsSent - delta.Conn1.SenderRetransSent
	// Over-NAK confirmation: duplicates should ≈ (NAK requests - actual gaps)
	delta.Conn1.DupesVsOverRequest = delta.Conn1.ReceiverDropsDupes - (delta.Conn1.ReceiverNAKPktsSent - delta.Conn1.ReceiverGapsDetected)

	// Print
	elapsed := cgCurr.Timestamp.Sub(cgPrev.Timestamp)

	fmt.Printf("\n  [Connection1: CG→Server] Delta over %.1fs:\n", elapsed.Seconds())
	fmt.Printf("    Sender (CG):\n")
	fmt.Printf("      Packets: +%d total (+%d unique, +%d retrans)\n",
		delta.Conn1.SenderPacketsSent, delta.Conn1.SenderPacketsUnique, delta.Conn1.SenderRetransSent)
	fmt.Printf("      Control: +%d NAKs recv, +%d ACKs recv\n",
		delta.Conn1.SenderNAKsRecv, delta.Conn1.SenderACKsRecv)
	if delta.Conn1.SenderNAKPktsRecv > 0 {
		fmt.Printf("      NAK detail: +%d singles, +%d ranges, requesting %d pkts\n",
			delta.Conn1.SenderNAKSingleRecv, delta.Conn1.SenderNAKRangeRecv, delta.Conn1.SenderNAKPktsRecv)
	}

	fmt.Printf("    Receiver (Server):\n")
	fmt.Printf("      Packets: +%d total (+%d retrans, +%d gaps)\n",
		delta.Conn1.ReceiverPacketsRecv, delta.Conn1.ReceiverRetransRecv, delta.Conn1.ReceiverGapsDetected)
	fmt.Printf("      Control: +%d NAKs sent, +%d ACKs sent\n",
		delta.Conn1.ReceiverNAKsSent, delta.Conn1.ReceiverACKsSent)
	if delta.Conn1.ReceiverNAKPktsSent > 0 {
		fmt.Printf("      NAK detail: +%d singles, +%d ranges, requesting %d pkts\n",
			delta.Conn1.ReceiverNAKSingleSent, delta.Conn1.ReceiverNAKRangeSent, delta.Conn1.ReceiverNAKPktsSent)
	}
	if delta.Conn1.ReceiverDrops > 0 || delta.Conn1.ReceiverDropsDupes > 0 || delta.Conn1.ReceiverDropsAlreadyAck > 0 || delta.Conn1.ReceiverDropsBufFull > 0 {
		fmt.Printf("      ⚠ DROPS: +%d (too_old: %d, already_ack: %d, dupes: %d, store_fail: %d)\n",
			delta.Conn1.ReceiverDrops, delta.Conn1.ReceiverDropsTooLate,
			delta.Conn1.ReceiverDropsAlreadyAck, delta.Conn1.ReceiverDropsDupes,
			delta.Conn1.ReceiverDropsBufFull)
	}

	fmt.Printf("    Balance Check:\n")
	if delta.Conn1.NAKsSentVsRecv != 0 {
		fmt.Printf("      ⚠ NAK pkt imbalance: Server sent %d, CG recv %d (diff: %d)\n",
			delta.Conn1.ReceiverNAKsSent, delta.Conn1.SenderNAKsRecv, delta.Conn1.NAKsSentVsRecv)
	} else {
		fmt.Printf("      ✓ NAK pkts balanced: %d sent = %d recv\n",
			delta.Conn1.ReceiverNAKsSent, delta.Conn1.SenderNAKsRecv)
	}
	if delta.Conn1.RetransSentVsRecv != 0 {
		fmt.Printf("      ⚠ Retrans imbalance: CG sent %d, Server recv %d (diff: %d)\n",
			delta.Conn1.SenderRetransSent, delta.Conn1.ReceiverRetransRecv, delta.Conn1.RetransSentVsRecv)
	} else {
		fmt.Printf("      ✓ Retrans balanced: %d sent = %d recv\n",
			delta.Conn1.SenderRetransSent, delta.Conn1.ReceiverRetransRecv)
	}

	// NAK request vs retransmission analysis
	if delta.Conn1.ReceiverNAKPktsSent > 0 {
		if delta.Conn1.NAKPktsReqVsRetrans > 0 {
			fmt.Printf("      ⚠ NAK request gap: requested %d pkts, sent %d retrans (unfulfilled: %d)\n",
				delta.Conn1.ReceiverNAKPktsSent, delta.Conn1.SenderRetransSent, delta.Conn1.NAKPktsReqVsRetrans)
		} else {
			fmt.Printf("      ✓ NAK requests fulfilled: %d requested, %d retransmitted\n",
				delta.Conn1.ReceiverNAKPktsSent, delta.Conn1.SenderRetransSent)
		}
	}

	// NAK efficiency
	if delta.Conn1.ReceiverGapsDetected > 0 {
		nakPerGap := float64(delta.Conn1.ReceiverNAKsSent) / float64(delta.Conn1.ReceiverGapsDetected)
		fmt.Printf("    NAK Efficiency: %.2f NAK pkts per gap\n", nakPerGap)
	}

	// Over-NAKing confirmation via duplicate detection
	// If duplicates ≈ (NAK requests - gaps), it confirms range NAKs are over-requesting
	if delta.Conn1.ReceiverDropsDupes > 0 {
		overNAKPkts := delta.Conn1.ReceiverNAKPktsSent - delta.Conn1.ReceiverGapsDetected
		if overNAKPkts > 0 {
			dupeMatchPct := float64(delta.Conn1.ReceiverDropsDupes) / float64(overNAKPkts) * 100
			fmt.Printf("    Over-NAK Confirmation: %d dupes vs %d over-requested pkts (%.0f%% match)\n",
				delta.Conn1.ReceiverDropsDupes, overNAKPkts, dupeMatchPct)
		}
	}
}

// PrintNakDebugMetrics prints detailed NAK-related metrics for debugging spurious NAKs
// This focuses on ring buffer state, NAK btree state, and NAK generation path
func (tm *TestMetrics) PrintNakDebugMetrics(snapshotIndex int) {
	if len(tm.Server.Snapshots) <= snapshotIndex {
		return
	}
	snap := tm.Server.Snapshots[snapshotIndex]
	if snap.Error != nil {
		fmt.Println("    [NAK Debug] Skipped - collection error")
		return
	}

	// Helper to get a specific metric value
	getMetric := func(prefix, contains string) float64 {
		for name, val := range snap.Metrics {
			if strings.HasPrefix(name, prefix) && (contains == "" || strings.Contains(name, contains)) {
				return val
			}
		}
		return 0
	}

	// Ring buffer state
	ringProcessed := getMetric("gosrt_ring_packets_processed_total", "")
	ringBacklog := getMetric("gosrt_ring_backlog_packets", "")
	ringDrops := getMetric("gosrt_ring_drops_total", "")
	ringDrained := getMetric("gosrt_ring_drained_packets_total", "")

	fmt.Printf("    Ring: processed=%.0f, backlog=%.0f, drops=%.0f, drained=%.0f\n",
		ringProcessed, ringBacklog, ringDrops, ringDrained)

	// NAK btree state
	nakInserts := getMetric("gosrt_nak_btree_inserts_total", "")
	nakDeletes := getMetric("gosrt_nak_btree_deletes_total", "")
	nakSize := getMetric("gosrt_nak_btree_size", "")
	nakExpired := getMetric("gosrt_nak_btree_expired_total", "")
	nakScanGaps := getMetric("gosrt_nak_btree_scan_gaps_total", "")

	fmt.Printf("    NAK btree: inserts=%.0f, deletes=%.0f, size=%.0f, expired=%.0f, scan_gaps=%.0f\n",
		nakInserts, nakDeletes, nakSize, nakExpired, nakScanGaps)

	// NAK periodic runs (which implementation)
	nakBtreeRuns := getMetric("gosrt_nak_periodic_runs_total", "impl=\"btree\"")
	nakOriginalRuns := getMetric("gosrt_nak_periodic_runs_total", "impl=\"original\"")
	nakSkipped := getMetric("gosrt_nak_periodic_skipped_total", "")

	fmt.Printf("    Periodic NAK: btree_runs=%.0f, original_runs=%.0f, skipped=%.0f\n",
		nakBtreeRuns, nakOriginalRuns, nakSkipped)

	// NAK output
	nakPktsSent := getMetric("gosrt_connection_packets_sent_total", "type=\"nak\"")
	nakSingleSent := getMetric("gosrt_connection_nak_entries_total", "direction=\"sent\",type=\"single\"")
	nakRangeSent := getMetric("gosrt_connection_nak_entries_total", "direction=\"sent\",type=\"range\"")
	nakPktsRequested := getMetric("gosrt_connection_nak_packets_requested_total", "direction=\"sent\"")

	fmt.Printf("    NAKs sent: pkts=%.0f (singles=%.0f, ranges=%.0f, requesting=%.0f pkts)\n",
		nakPktsSent, nakSingleSent, nakRangeSent, nakPktsRequested)

	// Gap detection
	gapsDetected := getMetric("gosrt_connection_congestion_packets_lost_total", "direction=\"recv\"")
	packetsRecv := getMetric("gosrt_connection_congestion_packets_total", "direction=\"recv\"")
	packetsUnique := getMetric("gosrt_connection_congestion_packets_unique_total", "direction=\"recv\"")

	fmt.Printf("    Packets: recv=%.0f, unique=%.0f, gaps=%.0f\n",
		packetsRecv, packetsUnique, gapsDetected)

	// Highlight anomalies
	if nakPktsSent > 0 && gapsDetected == 0 {
		fmt.Printf("    ⚠ SPURIOUS NAKs: %.0f NAKs sent but 0 gaps detected!\n", nakPktsSent)
	}
	if nakInserts > nakDeletes+nakSize+nakExpired {
		fmt.Printf("    ⚠ NAK btree imbalance: inserts=%.0f > (deletes+size+expired)=%.0f\n",
			nakInserts, nakDeletes+nakSize+nakExpired)
	}
	if nakOriginalRuns > 0 && nakBtreeRuns > 0 {
		fmt.Printf("    ⚠ BOTH NAK implementations ran: btree=%.0f, original=%.0f\n",
			nakBtreeRuns, nakOriginalRuns)
	}
}
