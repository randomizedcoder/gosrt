package main

import (
	"testing"
)

func TestParsePrometheusText(t *testing.T) {
	text := `# HELP client_seeker_current_bitrate_bps Current bitrate
# TYPE client_seeker_current_bitrate_bps gauge
client_seeker_current_bitrate_bps 100000000
client_seeker_target_bitrate_bps 100000000
client_seeker_packets_generated_total 12345
client_seeker_bytes_generated_total 17974320
client_seeker_connection_alive 1
srt_rtt_ms 15.5
srt_rtt_variance_ms 2.3
`

	metrics := ParsePrometheusText(text)

	tests := []struct {
		name     string
		expected float64
	}{
		{"client_seeker_current_bitrate_bps", 100000000},
		{"client_seeker_target_bitrate_bps", 100000000},
		{"client_seeker_packets_generated_total", 12345},
		{"client_seeker_bytes_generated_total", 17974320},
		{"client_seeker_connection_alive", 1},
		{"srt_rtt_ms", 15.5},
		{"srt_rtt_variance_ms", 2.3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := metrics[tt.name]
			if !ok {
				t.Errorf("metric %s not found", tt.name)
				return
			}
			if got != tt.expected {
				t.Errorf("metric %s = %f, want %f", tt.name, got, tt.expected)
			}
		})
	}
}

func TestParsePrometheusText_WithLabels(t *testing.T) {
	text := `srt_packets_sent_total{socket_id="123"} 5000
srt_packets_received_total{socket_id="123",stream="test"} 4900
`

	metrics := ParsePrometheusText(text)

	// Labels should be stripped
	if _, ok := metrics["srt_packets_sent_total"]; !ok {
		t.Error("srt_packets_sent_total not found (labels should be stripped)")
	}
	if _, ok := metrics["srt_packets_received_total"]; !ok {
		t.Error("srt_packets_received_total not found (labels should be stripped)")
	}
}

func TestParsePrometheusText_SkipsComments(t *testing.T) {
	text := `# This is a comment
# HELP metric_name Help text
# TYPE metric_name gauge
metric_name 42
`

	metrics := ParsePrometheusText(text)

	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}
	if metrics["metric_name"] != 42 {
		t.Errorf("metric_name = %f, want 42", metrics["metric_name"])
	}
}

func TestParsePrometheusText_Empty(t *testing.T) {
	metrics := ParsePrometheusText("")
	if len(metrics) != 0 {
		t.Errorf("expected 0 metrics for empty input, got %d", len(metrics))
	}
}

func TestParsePrometheusText_InvalidLines(t *testing.T) {
	text := `valid_metric 100
invalid line without value
another_valid 200
not_a_number abc
`

	metrics := ParsePrometheusText(text)

	if metrics["valid_metric"] != 100 {
		t.Errorf("valid_metric = %f, want 100", metrics["valid_metric"])
	}
	if metrics["another_valid"] != 200 {
		t.Errorf("another_valid = %f, want 200", metrics["another_valid"])
	}
	if _, ok := metrics["not_a_number"]; ok {
		t.Error("not_a_number should not be parsed (invalid value)")
	}
}

func TestMetricsCollector_ExtractMetrics(t *testing.T) {
	mc := &MetricsCollector{}

	rawMetrics := map[string]float64{
		"client_seeker_target_bitrate_bps":      200000000,
		"client_seeker_actual_bitrate_bps":      195000000,
		"client_seeker_packets_generated_total": 10000,
		"client_seeker_bytes_generated_total":   14560000,
		"client_seeker_connection_alive":        1,
		"srt_rtt_ms":                            12.5,
		"srt_rtt_variance_ms":                   1.8,
		"srt_recv_gap_rate":                     0.001,
		"srt_send_nak_rate":                     0.002,
	}

	m := mc.extractMetrics(rawMetrics)

	if m.TargetBitrate != 200000000 {
		t.Errorf("TargetBitrate = %d, want 200000000", m.TargetBitrate)
	}
	if m.ActualBitrate != 195000000 {
		t.Errorf("ActualBitrate = %d, want 195000000", m.ActualBitrate)
	}
	if !m.ConnectionAlive {
		t.Error("ConnectionAlive should be true")
	}
	if m.RTTMs != 12.5 {
		t.Errorf("RTTMs = %f, want 12.5", m.RTTMs)
	}

	// Check throughput efficiency
	expectedTE := float64(195000000) / float64(200000000)
	if m.ThroughputTE != expectedTE {
		t.Errorf("ThroughputTE = %f, want %f", m.ThroughputTE, expectedTE)
	}
}

func TestMetricsCollector_Aggregate(t *testing.T) {
	mc := &MetricsCollector{}

	seeker := StabilityMetrics{
		TargetBitrate:   200000000,
		ActualBitrate:   195000000,
		ConnectionAlive: true,
		ThroughputTE:    0.975,
	}

	server := StabilityMetrics{
		NAKRate:       0.015,
		GapRate:       0.005,
		RTTMs:         10.0,
		RTTVarianceMs: 1.5,
	}

	m := mc.aggregate(seeker, server)

	// Should use seeker's throughput metrics
	if m.TargetBitrate != 200000000 {
		t.Errorf("TargetBitrate = %d, want 200000000", m.TargetBitrate)
	}
	if m.ThroughputTE != 0.975 {
		t.Errorf("ThroughputTE = %f, want 0.975", m.ThroughputTE)
	}

	// Should use server's NAK/gap metrics
	if m.NAKRate != 0.015 {
		t.Errorf("NAKRate = %f, want 0.015", m.NAKRate)
	}
	if m.GapRate != 0.005 {
		t.Errorf("GapRate = %f, want 0.005", m.GapRate)
	}
	if m.RTTMs != 10.0 {
		t.Errorf("RTTMs = %f, want 10.0", m.RTTMs)
	}
}
