package main

import (
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// INSTRUMENTATION TESTS - For bottleneck detection
// See: client_seeker_instrumentation_design.md
// ═══════════════════════════════════════════════════════════════════════════

func TestPublisher_DetailedStats_Structure(t *testing.T) {
	// Test that DetailedStats returns all required fields
	pub := NewPublisher("srt://127.0.0.1:6000/test")

	stats := pub.DetailedStats()

	// All fields should be initialized (even if zero)
	// These are the fields needed for bottleneck detection
	t.Logf("Publisher DetailedStats fields:")
	t.Logf("  PacketsSent: %d", stats.PacketsSent)
	t.Logf("  BytesSent: %d", stats.BytesSent)
	t.Logf("  WriteTimeNs: %d", stats.WriteTimeNs)
	t.Logf("  WriteCount: %d", stats.WriteCount)
	t.Logf("  WriteBlockedCount: %d", stats.WriteBlockedCount)
	t.Logf("  WriteErrorCount: %d", stats.WriteErrorCount)
	t.Logf("  ConnectionAlive: %v", stats.ConnectionAlive)

	// Verify types are correct (compilation test)
	_ = stats.PacketsSent
	_ = stats.BytesSent
	_ = stats.WriteTimeNs
	_ = stats.WriteCount
	_ = stats.WriteBlockedCount
	_ = stats.WriteErrorCount
	_ = stats.ConnectionAlive
}

func TestPublisher_WriteTimeMetric_NotConnected(t *testing.T) {
	// Test that write time is tracked even when not connected
	// (should be 0 since no writes happen)
	pub := NewPublisher("srt://127.0.0.1:6000/test")

	stats := pub.DetailedStats()

	if stats.WriteTimeNs != 0 {
		t.Errorf("WriteTimeNs = %d, want 0 when not connected", stats.WriteTimeNs)
	}
	if stats.WriteCount != 0 {
		t.Errorf("WriteCount = %d, want 0 when not connected", stats.WriteCount)
	}
}

func TestPublisher_ResetStats(t *testing.T) {
	pub := NewPublisher("srt://127.0.0.1:6000/test")

	// Manually set some stats (simulating writes)
	pub.packetsSent.Store(100)
	pub.bytesSent.Store(145600)
	pub.writeTimeNs.Store(1000000)
	pub.writeCount.Store(100)
	pub.writeBlockedCount.Store(5)
	pub.writeErrorCount.Store(1)

	// Verify stats are set
	stats := pub.DetailedStats()
	if stats.PacketsSent != 100 {
		t.Errorf("PacketsSent = %d, want 100", stats.PacketsSent)
	}

	// Reset
	pub.ResetStats()

	// Verify stats are cleared
	stats = pub.DetailedStats()
	if stats.PacketsSent != 0 {
		t.Errorf("PacketsSent after reset = %d, want 0", stats.PacketsSent)
	}
	if stats.WriteTimeNs != 0 {
		t.Errorf("WriteTimeNs after reset = %d, want 0", stats.WriteTimeNs)
	}
	if stats.WriteCount != 0 {
		t.Errorf("WriteCount after reset = %d, want 0", stats.WriteCount)
	}
}

func TestPublisher_WriteRatio_Calculation(t *testing.T) {
	// Test that we can calculate write time ratio for bottleneck detection
	pub := NewPublisher("srt://127.0.0.1:6000/test")

	// Simulate 100ms elapsed, 50ms in writes
	elapsedNs := int64(100 * time.Millisecond)
	writeTimeNs := int64(50 * time.Millisecond)
	pub.writeTimeNs.Store(writeTimeNs)

	stats := pub.DetailedStats()

	// Calculate write ratio (this would be done by bottleneck detector)
	writeRatio := float64(stats.WriteTimeNs) / float64(elapsedNs)

	t.Logf("Write ratio test:")
	t.Logf("  Elapsed: %v", time.Duration(elapsedNs))
	t.Logf("  Write time: %v", time.Duration(stats.WriteTimeNs))
	t.Logf("  Write ratio: %.2f%%", writeRatio*100)

	// Ratio should be 0.5 (50%)
	if writeRatio < 0.49 || writeRatio > 0.51 {
		t.Errorf("Write ratio = %.2f, want ~0.50", writeRatio)
	}
}
