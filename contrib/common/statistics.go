package common

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// ThroughputGetter is a function that returns current bytes, packets, gaps, NAKs, skips, and retransmits
// This allows the display to work with any counter source (metrics.ConnectionMetrics, etc.)
// Returns: (bytes, pkts, gapsPkts, naksPkts, skipsPkts, retransPkts)
//   - bytes: total bytes transferred (for MB display)
//   - pkts: packets for rate calculation (for pkt/s display)
//   - gapsPkts: sequence gaps detected (triggers NAK/retransmission)
//   - naksPkts: NAK packets sent requesting retransmission
//   - skipsPkts: TSBPD skips = packets that NEVER arrived (TRUE losses)
//   - retransPkts: total retransmissions (ARQ recovery activity)
type ThroughputGetter func() (bytes uint64, pkts uint64, gapsPkts uint64, naksPkts uint64, skipsPkts uint64, retransPkts uint64)

// RunThroughputDisplay runs a throughput display loop that periodically prints stats
// The getter function is called to retrieve current byte/packet/success/loss/retrans totals
// The loop exits when ctx is canceled
//
// Output format (fixed-width columns):
//
//	[label           ] HH:MM:SS.xx | 999.9 pkt/s | 99.99 MB | 9.999 Mb/s | 9999k ok / 999 gaps / 999 NAKs / 999 retx | recovery=100.0%
//
// The label column is 16 chars wide for consistent vertical alignment.
//
// - "gaps" = sequence gaps detected (triggers NAK/retransmission)
// - "NAKs" = NAK packets sent requesting retransmission
// - "retx" = retransmissions sent/received (ARQ recovery activity)
// - "recovery" = (gaps - TSBPD_skips) / gaps = % of gaps successfully recovered
//   - 100% = all gaps recovered via retransmission (no true losses)
//   - <100% = some packets never arrived before TSBPD timeout (true losses)
func RunThroughputDisplay(ctx context.Context, period time.Duration, getter ThroughputGetter) {
	RunThroughputDisplayWithLabelAndColor(ctx, period, "", "", getter)
}

// RunThroughputDisplayWithLabel runs a throughput display loop with a component label
func RunThroughputDisplayWithLabel(ctx context.Context, period time.Duration, label string, getter ThroughputGetter) {
	RunThroughputDisplayWithLabelAndColor(ctx, period, label, "", getter)
}

// RunThroughputDisplayWithLabelAndColor runs a throughput display loop with a component label and color
// Color can be: red, green, yellow, blue, magenta, cyan, white (or empty for no color)
func RunThroughputDisplayWithLabelAndColor(ctx context.Context, period time.Duration, label, color string, getter ThroughputGetter) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	var prevBytes, prevPkts uint64
	last := time.Now()

	// Pre-compute color codes for efficiency
	colorCode := ColorCode(color)

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			currentBytes, currentPkts, gapsPkts, naksPkts, skipsPkts, retransPkts := getter()

			diff := now.Sub(last)
			if diff.Seconds() <= 0 {
				continue // Avoid division by zero
			}

			mbps := float64(currentBytes-prevBytes) * 8 / (1000 * 1000 * diff.Seconds())
			pps := float64(currentPkts-prevPkts) / diff.Seconds()

			// Calculate RECOVERY rate: (gaps - skips) / gaps
			// - gaps = sequence gaps detected (triggers NAK/retrans)
			// - skips = TSBPD skips (packets that NEVER arrived - true losses)
			// Recovery = 100% means all gaps were successfully retransmitted
			var recoveryPct = 100.0
			if gapsPkts > 0 {
				recoveryPct = (1.0 - float64(skipsPkts)/float64(gapsPkts)) * 100.0
				if recoveryPct < 0 {
					recoveryPct = 0
				}
			}

			// Format time with 2 decimal places: HH:MM:SS.xx
			timeStr := now.Format("15:04:05.00")

			// Simplified format: [label] time | rate | total MB | Mb/s | packets ok / gaps / NAKs / retx | recovery=%
			// Use currentPkts for "ok" since it's the actual received packet count
			// "gaps" = sequence gaps detected (triggers NAK/retrans)
			// "NAKs" = NAK packets sent requesting retransmission
			// "recovery" = % of gaps that were successfully retransmitted (100% = no true losses)
			labelStr := ""
			if label != "" {
				labelStr = fmt.Sprintf("[%-16s] ", label)
			}

			// Format the output line
			line := fmt.Sprintf("%s%s | %7.1f pkt/s | %7.2f MB | %6.3f Mb/s | %6.1fk ok / %5d gaps / %5d NAKs / %5d retx | recovery=%5.1f%%\n",
				labelStr,
				timeStr,
				pps,
				float64(currentBytes)/(1024*1024),
				mbps,
				float64(currentPkts)/1000,
				gapsPkts,
				naksPkts,
				retransPkts,
				recoveryPct)

			// Apply color if specified
			// Use newline instead of carriage return so both [PUB] and [SUB] lines are visible
			// when running multiple applications simultaneously
			fmt.Fprint(os.Stderr, ColorizeCode(line, colorCode))

			prevBytes, prevPkts = currentBytes, currentPkts
			last = now
		}
	}
}

// StartThroughputDisplay starts a goroutine that runs RunThroughputDisplayWithLabelAndColor.
// Uses instanceName if non-empty, otherwise falls back to defaultLabel.
func StartThroughputDisplay(ctx context.Context, wg *sync.WaitGroup, period time.Duration,
	defaultLabel string, instanceName string, color string, getter ThroughputGetter) {
	if getter == nil {
		return
	}
	label := defaultLabel
	if instanceName != "" {
		label = instanceName
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		RunThroughputDisplayWithLabelAndColor(ctx, period, label, color, getter)
	}()
}
