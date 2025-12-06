package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
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
	Addr      string             // Metrics endpoint address
	Snapshots []*MetricsSnapshot // Collected snapshots
}

// TestMetrics holds metrics for all components in a test
type TestMetrics struct {
	Server          ComponentMetrics
	ClientGenerator ComponentMetrics
	Client          ComponentMetrics
}

// NewTestMetrics creates a new TestMetrics instance with the given URLs
func NewTestMetrics(serverURL, clientGenURL, clientURL string) *TestMetrics {
	return &TestMetrics{
		Server: ComponentMetrics{
			Component: "server",
			Addr:      serverURL,
			Snapshots: make([]*MetricsSnapshot, 0),
		},
		ClientGenerator: ComponentMetrics{
			Component: "client-generator",
			Addr:      clientGenURL,
			Snapshots: make([]*MetricsSnapshot, 0),
		},
		Client: ComponentMetrics{
			Component: "client",
			Addr:      clientURL,
			Snapshots: make([]*MetricsSnapshot, 0),
		},
	}
}

// CollectMetrics fetches metrics from a single URL
func CollectMetrics(metricsURL, point string) *MetricsSnapshot {
	snapshot := &MetricsSnapshot{
		Timestamp: time.Now(),
		Point:     point,
		Metrics:   make(map[string]float64),
	}

	if metricsURL == "" {
		snapshot.Error = fmt.Errorf("empty metrics URL")
		return snapshot
	}

	resp, err := http.Get(metricsURL)
	if err != nil {
		snapshot.Error = fmt.Errorf("failed to fetch metrics: %w", err)
		return snapshot
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snapshot.Error = fmt.Errorf("metrics endpoint returned status %d", resp.StatusCode)
		return snapshot
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		snapshot.Error = fmt.Errorf("failed to read metrics response: %w", err)
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
		if tm.Server.Addr != "" {
			snapshot := CollectMetrics(tm.Server.Addr, point)
			tm.Server.Snapshots = append(tm.Server.Snapshots, snapshot)
		}
	}()

	// Collect from client-generator
	wg.Add(1)
	go func() {
		defer wg.Done()
		if tm.ClientGenerator.Addr != "" {
			snapshot := CollectMetrics(tm.ClientGenerator.Addr, point)
			tm.ClientGenerator.Snapshots = append(tm.ClientGenerator.Snapshots, snapshot)
		}
	}()

	// Collect from client
	wg.Add(1)
	go func() {
		defer wg.Done()
		if tm.Client.Addr != "" {
			snapshot := CollectMetrics(tm.Client.Addr, point)
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
		fmt.Printf("\n%s (%s):\n", cm.Component, cm.Addr)
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
