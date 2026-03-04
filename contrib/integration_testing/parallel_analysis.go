package main

import (
	"fmt"
	"sort"
	"strings"
)

// MetricCategory groups related metrics for organized display
type MetricCategory struct {
	Name    string
	Metrics []MetricComparison
}

// MetricComparison holds a comparison between baseline and highperf for one metric
type MetricComparison struct {
	Name        string
	BaselineVal float64
	HighPerfVal float64
	Diff        float64 // HighPerf - Baseline
	DiffPercent float64 // ((HighPerf - Baseline) / Baseline) * 100
	Significant bool    // True if diff is > 10%
}

// PipelineComparison holds the full comparison between two pipelines
type PipelineComparison struct {
	Component  string // "Server", "Client-Generator", "Client"
	Categories []MetricCategory
}

// CompareParallelPipelines performs a detailed comparison between baseline and highperf metrics
func CompareParallelPipelines(baseline, highperf *TestMetrics) []PipelineComparison {
	var comparisons []PipelineComparison

	// Compare each component
	components := []struct {
		name      string
		component string
	}{
		{"Client-Generator", "client-generator"},
		{"Server", "server"},
		{"Client", "client"},
	}

	for _, comp := range components {
		baseSnapshot := baseline.GetSnapshotByLabel(comp.component, "pre-shutdown")
		highSnapshot := highperf.GetSnapshotByLabel(comp.component, "pre-shutdown")

		if baseSnapshot == nil || highSnapshot == nil {
			continue
		}

		comparison := PipelineComparison{
			Component: comp.name,
		}

		// Group metrics by category
		comparison.Categories = categorizeAndCompareMetrics(baseSnapshot.Metrics, highSnapshot.Metrics)
		comparisons = append(comparisons, comparison)
	}

	return comparisons
}

// categorizeAndCompareMetrics groups metrics and computes differences
func categorizeAndCompareMetrics(baseline, highperf map[string]float64) []MetricCategory {
	categories := []MetricCategory{
		{Name: "📊 Packet Flow", Metrics: compareMetricGroup(baseline, highperf, []string{
			"gosrt_connection_congestion_packets_total",
			"gosrt_connection_congestion_packets_unique_total",
			"gosrt_connection_packets_received_total",
			"gosrt_connection_packets_sent_total",
		})},
		{Name: "⚠️ Gaps & Loss", Metrics: compareMetricGroup(baseline, highperf, []string{
			"gosrt_connection_congestion_packets_lost_total",
			"gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total",
		})},
		{Name: "🔄 Retransmissions", Metrics: compareMetricGroup(baseline, highperf, []string{
			"gosrt_connection_congestion_retransmissions_total",
			"gosrt_connection_retransmissions_from_nak_total",
		})},
		{Name: "🛡️ Suppression (RTO)", Metrics: compareSuppressionMetrics(baseline, highperf)},
		{Name: "📩 NAK Activity", Metrics: compareNAKMetrics(baseline, highperf)},
		{Name: "📥 Drops", Metrics: compareDropMetrics(baseline, highperf)},
		{Name: "⏱️ Timing", Metrics: compareMetricGroup(baseline, highperf, []string{
			"gosrt_connection_rtt_ms",
			"gosrt_connection_congestion_send_period_us",
			"gosrt_connection_periodic_ack_runs_total",
			"gosrt_connection_periodic_nak_runs_total",
		})},
		{Name: "📈 Bytes", Metrics: compareMetricGroup(baseline, highperf, []string{
			"gosrt_connection_bytes_received_total",
			"gosrt_connection_bytes_sent_total",
			"gosrt_connection_congestion_bytes_retrans_total",
		})},
		// Lockless Sender metrics (Phase 5+: Sender EventLoop)
		{Name: "🚀 Sender Ring", Metrics: compareMetricGroup(baseline, highperf, []string{
			"gosrt_send_ring_pushed_total",
			"gosrt_send_ring_dropped_total",
			"gosrt_send_ring_drained_total",
			"gosrt_send_btree_inserted_total",
			"gosrt_send_btree_duplicates_total",
		})},
		{Name: "🎛️ Sender Control Ring", Metrics: compareMetricGroup(baseline, highperf, []string{
			"gosrt_send_control_ring_pushed_ack_total",
			"gosrt_send_control_ring_pushed_nak_total",
			"gosrt_send_control_ring_dropped_ack_total",
			"gosrt_send_control_ring_dropped_nak_total",
			"gosrt_send_control_ring_drained_total",
			"gosrt_send_control_ring_processed_ack_total",
			"gosrt_send_control_ring_processed_nak_total",
		})},
		{Name: "⚡ Sender EventLoop", Metrics: compareSenderEventLoopMetrics(baseline, highperf)},
	}

	// Filter out empty categories
	var nonEmpty []MetricCategory
	for _, cat := range categories {
		if len(cat.Metrics) > 0 {
			nonEmpty = append(nonEmpty, cat)
		}
	}

	return nonEmpty
}

// normalizeMetricKey strips socket_id from metric key for comparison
// e.g., `foo{socket_id="0x123",type="bar"}` -> `foo{type="bar"}`
func normalizeMetricKey(name string) string {
	// Find the label section
	idx := strings.Index(name, "{")
	if idx < 0 {
		return name // No labels
	}

	basePart := name[:idx]
	labelPart := name[idx:]

	// Remove socket_id label using regex-like replacement
	// Handle both `socket_id="...",` and `,socket_id="..."`
	for {
		// Try to find socket_id="..."
		socketIdx := strings.Index(labelPart, `socket_id="`)
		if socketIdx < 0 {
			break
		}

		// Find the end of the socket_id value
		valueStart := socketIdx + len(`socket_id="`)
		valueEnd := strings.Index(labelPart[valueStart:], `"`)
		if valueEnd < 0 {
			break
		}
		valueEnd += valueStart + 1 // Include the closing quote

		// Determine if we need to remove a comma
		removeStart := socketIdx
		removeEnd := valueEnd

		// Check if there's a comma after
		if removeEnd < len(labelPart) && labelPart[removeEnd] == ',' {
			removeEnd++
		} else if removeStart > 1 && labelPart[removeStart-1] == ',' {
			// Comma before
			removeStart--
		}

		labelPart = labelPart[:removeStart] + labelPart[removeEnd:]
	}

	// Handle empty labels case: `{}`
	if labelPart == "{}" {
		return basePart
	}

	return basePart + labelPart
}

// compareMetricGroup compares a group of metrics by prefix
func compareMetricGroup(baseline, highperf map[string]float64, prefixes []string) []MetricComparison {
	var comparisons []MetricComparison

	// Build normalized value maps - SUM values across all socket_ids for each normalized key
	// This handles multiple connections (each with different socket_id) within a pipeline
	baselineSums := make(map[string]float64)
	highperfSums := make(map[string]float64)

	for name, value := range baseline {
		normKey := normalizeMetricKey(name)
		baselineSums[normKey] += value
	}
	for name, value := range highperf {
		normKey := normalizeMetricKey(name)
		highperfSums[normKey] += value
	}

	// Track which normalized keys we've processed
	processed := make(map[string]bool)

	for _, prefix := range prefixes {
		// Find all metrics matching this prefix in baseline
		for normKey, baseVal := range baselineSums {
			if !strings.HasPrefix(normKey, prefix) {
				continue
			}
			if processed[normKey] {
				continue
			}
			processed[normKey] = true

			highVal := highperfSums[normKey] // 0 if not present

			comp := createComparison(normKey, baseVal, highVal)
			if comp.BaselineVal != 0 || comp.HighPerfVal != 0 {
				comparisons = append(comparisons, comp)
			}
		}

		// Also check highperf for metrics not in baseline
		for normKey, highVal := range highperfSums {
			if !strings.HasPrefix(normKey, prefix) {
				continue
			}
			if processed[normKey] {
				continue // Already handled above
			}
			processed[normKey] = true

			comp := createComparison(normKey, 0, highVal)
			if comp.HighPerfVal != 0 {
				comparisons = append(comparisons, comp)
			}
		}
	}

	// Sort by metric name for consistent output
	sort.Slice(comparisons, func(i, j int) bool {
		return comparisons[i].Name < comparisons[j].Name
	})

	return comparisons
}

// compareNAKMetrics specifically handles NAK-related metrics
func compareNAKMetrics(baseline, highperf map[string]float64) []MetricComparison {
	nakPrefixes := []string{
		"gosrt_connection_nak_",
		"gosrt_connection_pkt_sent_nak",
		"gosrt_connection_pkt_recv_nak",
	}

	return compareMetricGroup(baseline, highperf, nakPrefixes)
}

// compareDropMetrics specifically handles drop-related metrics
func compareDropMetrics(baseline, highperf map[string]float64) []MetricComparison {
	dropPrefixes := []string{
		"gosrt_connection_congestion_recv_data_drop_total",
		"gosrt_connection_congestion_send_data_drop_total",
		"gosrt_connection_congestion_packets_drop_total",
	}

	return compareMetricGroup(baseline, highperf, dropPrefixes)
}

// compareSuppressionMetrics specifically handles RTO-based suppression metrics
// These metrics are critical for evaluating the effectiveness of suppression
func compareSuppressionMetrics(baseline, highperf map[string]float64) []MetricComparison {
	suppressionPrefixes := []string{
		// Sender-side retransmit suppression
		"gosrt_retrans_suppressed_total", // Retransmits blocked by suppression
		"gosrt_retrans_allowed_total",    // Retransmits that passed threshold
		"gosrt_retrans_first_time_total", // First-time retransmits (RetransmitCount was 0)
		// Receiver-side NAK suppression
		"gosrt_nak_suppressed_seqs_total", // NAK entries blocked by suppression
		"gosrt_nak_allowed_seqs_total",    // NAK entries that passed threshold
	}

	return compareMetricGroup(baseline, highperf, suppressionPrefixes)
}

// compareSenderEventLoopMetrics compares sender EventLoop metrics
// These are critical for evaluating the lockless sender implementation
// See lockless_sender_design.md Section 11 for expected metrics
func compareSenderEventLoopMetrics(baseline, highperf map[string]float64) []MetricComparison {
	eventLoopPrefixes := []string{
		// Core EventLoop counters
		"gosrt_send_event_loop_iterations_total",
		"gosrt_send_event_loop_default_runs_total",
		"gosrt_send_event_loop_drop_fires_total",
		// Packet processing
		"gosrt_send_event_loop_data_drained_total",
		"gosrt_send_event_loop_control_drained_total",
		"gosrt_send_event_loop_acks_processed_total",
		"gosrt_send_event_loop_naks_processed_total",
		"gosrt_send_delivery_packets_total",
		// Sleep behavior
		"gosrt_send_event_loop_idle_backoffs_total",
		"gosrt_send_event_loop_tsbpd_sleeps_total",
		"gosrt_send_event_loop_empty_btree_sleeps_total",
		"gosrt_send_event_loop_sleep_clamped_min_total",
		"gosrt_send_event_loop_sleep_clamped_max_total",
		// Current state
		"gosrt_send_btree_len",
	}

	comparisons := compareMetricGroup(baseline, highperf, eventLoopPrefixes)

	// Add burst detection metric (Packets/Iteration ratio)
	// Lower ratio = smoother delivery (more iterations per packet)
	// Baseline Tick() mode typically releases bursts in one iteration
	baseIterations := 0.0
	highIterations := 0.0
	baseDelivered := 0.0
	highDelivered := 0.0

	for name, val := range baseline {
		if strings.Contains(name, "send_event_loop_iterations_total") || strings.Contains(name, "send_tick_runs") {
			baseIterations += val
		}
		if strings.Contains(name, "send_delivery_packets_total") || strings.Contains(name, "send_tick_delivered_packets") {
			baseDelivered += val
		}
	}
	for name, val := range highperf {
		if strings.Contains(name, "send_event_loop_iterations_total") || strings.Contains(name, "send_tick_runs") {
			highIterations += val
		}
		if strings.Contains(name, "send_delivery_packets_total") || strings.Contains(name, "send_tick_delivered_packets") {
			highDelivered += val
		}
	}

	// Calculate Packets/Iteration ratio (lower = smoother)
	if baseIterations > 0 && highIterations > 0 {
		baseRatio := baseDelivered / baseIterations
		highRatio := highDelivered / highIterations
		comparisons = append(comparisons, createComparison(
			"[DERIVED] Packets/Iteration (lower=smoother)",
			baseRatio,
			highRatio,
		))
	}

	return comparisons
}

// createComparison builds a MetricComparison from two values
func createComparison(name string, baseVal, highVal float64) MetricComparison {
	diff := highVal - baseVal
	var diffPct float64
	if baseVal != 0 {
		diffPct = (diff / baseVal) * 100
	} else if highVal != 0 {
		diffPct = 100 // 100% increase from zero
	}

	return MetricComparison{
		Name:        simplifyMetricName(name),
		BaselineVal: baseVal,
		HighPerfVal: highVal,
		Diff:        diff,
		DiffPercent: diffPct,
		Significant: abs(diffPct) > 10, // >10% difference is significant
	}
}

// simplifyMetricName removes common prefixes for cleaner display
func simplifyMetricName(name string) string {
	// Remove common prefixes
	name = strings.TrimPrefix(name, "gosrt_connection_")
	name = strings.TrimPrefix(name, "congestion_")

	// Remove socket_id and other labels for cleaner display
	if idx := strings.Index(name, "{"); idx > 0 {
		// Keep the label info but simplify
		labelPart := name[idx:]
		basePart := name[:idx]

		// Extract direction if present
		if strings.Contains(labelPart, "direction=\"sent\"") {
			basePart += " (sent)"
		} else if strings.Contains(labelPart, "direction=\"recv\"") {
			basePart += " (recv)"
		}

		// Extract reason if present
		if strings.Contains(labelPart, "reason=\"") {
			start := strings.Index(labelPart, "reason=\"") + 8
			end := strings.Index(labelPart[start:], "\"")
			if end > 0 {
				reason := labelPart[start : start+end]
				basePart += " [" + reason + "]"
			}
		}

		// Extract type if present
		if strings.Contains(labelPart, "type=\"") {
			start := strings.Index(labelPart, "type=\"") + 6
			end := strings.Index(labelPart[start:], "\"")
			if end > 0 {
				typ := labelPart[start : start+end]
				basePart += " [" + typ + "]"
			}
		}

		name = basePart
	}

	return name
}

// abs returns the absolute value of a float64
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// PrintDetailedComparison prints a comprehensive comparison between pipelines
func PrintDetailedComparison(comparisons []PipelineComparison) {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                    PARALLEL PIPELINE COMPARISON                              ║")
	fmt.Println("║                    Baseline (list) vs HighPerf (btree+io_uring)              ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════════════════╝")

	for _, comp := range comparisons {
		fmt.Println()
		fmt.Printf("┌─────────────────────────────────────────────────────────────────────────────┐\n")
		fmt.Printf("│ %-75s │\n", comp.Component)
		fmt.Printf("├─────────────────────────────────────────────────────────────────────────────┤\n")

		for _, cat := range comp.Categories {
			if len(cat.Metrics) == 0 {
				continue
			}

			fmt.Printf("│ %-75s │\n", cat.Name)
			fmt.Printf("│ %-40s %12s %12s %10s │\n", "Metric", "Baseline", "HighPerf", "Diff")
			fmt.Printf("│ %-40s %12s %12s %10s │\n", strings.Repeat("─", 40), "────────────", "────────────", "──────────")

			for _, m := range cat.Metrics {
				// Format the difference
				var diffStr string
				switch {
				case m.Diff == 0:
					diffStr = "="
				case m.BaselineVal == 0:
					diffStr = "NEW"
				case m.Diff > 0:
					diffStr = fmt.Sprintf("+%.1f%%", m.DiffPercent)
				default:
					diffStr = fmt.Sprintf("%.1f%%", m.DiffPercent)
				}

				// Highlight significant differences
				var prefix string
				switch {
				case m.Significant && m.Diff > 0:
					prefix = "⚠️"
				case m.Significant && m.Diff < 0:
					prefix = "✓"
				default:
					prefix = "  "
				}

				name := m.Name
				if len(name) > 38 {
					name = name[:35] + "..."
				}

				fmt.Printf("│ %s%-38s %12.0f %12.0f %10s │\n",
					prefix, name, m.BaselineVal, m.HighPerfVal, diffStr)
			}
			fmt.Printf("│ %-75s │\n", "")
		}
		fmt.Printf("└─────────────────────────────────────────────────────────────────────────────┘\n")
	}

	// Print summary of significant differences
	fmt.Println()
	fmt.Println("=== SIGNIFICANT DIFFERENCES (>10%) ===")
	hasSignificant := false
	for _, comp := range comparisons {
		for _, cat := range comp.Categories {
			for _, m := range cat.Metrics {
				if m.Significant {
					hasSignificant = true
					direction := "↑"
					if m.Diff < 0 {
						direction = "↓"
					}
					fmt.Printf("  %s %s.%s: %.0f → %.0f (%s%.1f%%)\n",
						direction, comp.Component, m.Name,
						m.BaselineVal, m.HighPerfVal,
						func() string {
							if m.Diff > 0 {
								return "+"
							}
							return ""
						}(),
						m.DiffPercent)
				}
			}
		}
	}
	if !hasSignificant {
		fmt.Println("  (none - pipelines performed similarly)")
	}
}
