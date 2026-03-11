package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Clean Room Integration Test
//
// Validates the full streaming pipeline on loopback without root/namespaces:
//   ffmpeg (1 Mb/s) → UDP → client-udp → SRT → server → SRT → client (→ null)
//
// All processes run on 127.10.10.10 (Linux routes all 127.0.0.0/8 to lo).
// At 1 Mb/s on loopback, there should be zero packet loss.
//
// The test collects Prometheus metrics from all 3 SRT processes via UDS,
// correlates the 2 SRT connections (4 ends) by matching socket_id ↔ peer_socket_id,
// and compares every metric between corresponding ends.

const (
	cleanroomServerAddr      = "127.10.10.10:6100"
	cleanroomUDPPort         = 5100
	cleanroomStreamID        = "cleanroom-stream"
	cleanroomBitrate         = 1_000_000 // 1 Mb/s
	cleanroomDuration        = 15 * time.Second
	cleanroomConnWait        = 2 * time.Second
	cleanroomCollectInterval = 5 * time.Second
	cleanroomTolerancePct    = 2.0 // % tolerance for flow counters

	cleanroomUDSServer   = "/tmp/srt_cleanroom_server.sock"
	cleanroomUDSClientUDP = "/tmp/srt_cleanroom_clientudp.sock"
	cleanroomUDSClient   = "/tmp/srt_cleanroom_client.sock"
)

// connectionEndpoint represents one end of an SRT connection from metrics.
type connectionEndpoint struct {
	SocketID     string
	PeerSocketID string
	StreamID     string
	PeerType     string
	Component    string // "server", "client-udp", "client"
}

// connectionPair represents a correlated SRT connection across two processes.
type connectionPair struct {
	Label    string             // "publish" or "subscribe"
	Sender   connectionEndpoint // the sending end
	Receiver connectionEndpoint // the receiving end
}

// metricComparison holds a single metric comparison result.
type metricComparison struct {
	Name         string
	SenderValue  float64
	ReceiverValue float64
	Delta        float64
	DeltaPct     float64
	Significant  bool
	Category     string // "flow", "error", "control", "info"
}

func runCleanroomTest() {
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║  Clean Room Integration Test (no sudo required)            ║")
	fmt.Println("║  Pipeline: ffmpeg → client-udp → server → client → null   ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Check prerequisites
	if err := checkFFmpegAvailable(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	baseDir := getBaseDir()

	serverBin := filepath.Join(baseDir, "contrib", "server", "server")
	clientUDPBin := filepath.Join(baseDir, "contrib", "client-udp", "client-udp")
	clientBin := filepath.Join(baseDir, "contrib", "client", "client")

	if err := ensureBinaries(ctx, baseDir, serverBin, clientUDPBin, clientBin); err != nil {
		fmt.Fprintf(os.Stderr, "Error building binaries: %v\n", err)
		os.Exit(1)
	}

	// Clean up UDS files
	defer func() {
		os.Remove(cleanroomUDSServer)
		os.Remove(cleanroomUDSClientUDP)
		os.Remove(cleanroomUDSClient)
	}()
	// Remove stale sockets from prior runs
	os.Remove(cleanroomUDSServer)
	os.Remove(cleanroomUDSClientUDP)
	os.Remove(cleanroomUDSClient)

	// Build CLI flags
	srtConfig := GetSRTConfig(ConfigFullELLockFree)
	srtFlags := srtConfig.ToCliFlags()

	serverFlags := append([]string{
		"-addr", cleanroomServerAddr,
		"-promuds", cleanroomUDSServer,
		"-name", "cleanroom-server",
	}, srtFlags...)

	publishURL := fmt.Sprintf("srt://%s/%s", cleanroomServerAddr, cleanroomStreamID)
	clientUDPFlags := append([]string{
		"-from", fmt.Sprintf(":%d", cleanroomUDPPort),
		"-to", publishURL,
		"-promuds", cleanroomUDSClientUDP,
		"-name", "cleanroom-cudp",
	}, srtFlags...)

	subscribeURL := fmt.Sprintf("srt://%s?streamid=subscribe:/%s&mode=caller", cleanroomServerAddr, cleanroomStreamID)
	clientFlags := append([]string{
		"-from", subscribeURL,
		"-to", "null",
		"-promuds", cleanroomUDSClient,
		"-name", "cleanroom-client",
	}, srtFlags...)

	ffmpegArgs := []string{
		"-re",
		"-f", "lavfi",
		"-i", "testsrc=size=640x360:rate=25",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-b:v", strconv.Itoa(cleanroomBitrate),
		"-f", "rtp",
		fmt.Sprintf("rtp://127.10.10.10:%d?pkt_size=1316", cleanroomUDPPort),
	}

	// Print CLI flags
	fmt.Println("CLI Flags:")
	fmt.Printf("  Server:     %s %s\n", serverBin, strings.Join(serverFlags, " "))
	fmt.Printf("  Client-UDP: %s %s\n", clientUDPBin, strings.Join(clientUDPFlags, " "))
	fmt.Printf("  FFmpeg:     ffmpeg %s\n", strings.Join(ffmpegArgs, " "))
	fmt.Printf("  Client:     %s %s\n", clientBin, strings.Join(clientFlags, " "))
	fmt.Println()

	// Start processes
	fmt.Println("Starting server...")
	serverCmd := exec.CommandContext(ctx, serverBin, serverFlags...)
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr
	if err := serverCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
		os.Exit(1)
	}
	defer killProcess(serverCmd)

	time.Sleep(500 * time.Millisecond)

	fmt.Println("Starting client-udp...")
	clientUDPCmd := exec.CommandContext(ctx, clientUDPBin, clientUDPFlags...)
	clientUDPCmd.Stdout = os.Stdout
	clientUDPCmd.Stderr = os.Stderr
	if err := clientUDPCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting client-udp: %v\n", err)
		os.Exit(1)
	}
	defer killProcess(clientUDPCmd)

	time.Sleep(1 * time.Second)

	fmt.Println("Starting ffmpeg...")
	ffmpegCmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs...)
	ffmpegCmd.Stdout = os.Stdout
	ffmpegCmd.Stderr = os.Stderr
	if err := ffmpegCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting ffmpeg: %v\n", err)
		os.Exit(1)
	}
	defer killProcess(ffmpegCmd)

	time.Sleep(500 * time.Millisecond)

	fmt.Println("Starting client (subscriber)...")
	clientCmd := exec.CommandContext(ctx, clientBin, clientFlags...)
	clientCmd.Stdout = os.Stdout
	clientCmd.Stderr = os.Stderr
	if err := clientCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting client: %v\n", err)
		os.Exit(1)
	}
	defer killProcess(clientCmd)

	// Wait for connections
	fmt.Printf("Waiting %v for connections to establish...\n", cleanroomConnWait)
	time.Sleep(cleanroomConnWait)

	// Initialize metrics collection
	tm := NewTestMetrics(
		MetricsEndpoint{UDSPath: cleanroomUDSServer},
		MetricsEndpoint{UDSPath: cleanroomUDSClientUDP},
		MetricsEndpoint{UDSPath: cleanroomUDSClient},
	)

	// Collect initial metrics
	fmt.Println("\nCollecting initial metrics...")
	tm.CollectAllMetrics(ctx, "startup")

	// Run for test duration, collecting metrics periodically
	fmt.Printf("All 4 processes started. Running for %v...\n", cleanroomDuration)

	collectTicker := time.NewTicker(cleanroomCollectInterval)
	testTimer := time.NewTimer(cleanroomDuration)

collectLoop:
	for {
		select {
		case <-collectTicker.C:
			fmt.Println("Collecting mid-test metrics...")
			tm.CollectAllMetrics(ctx, "mid-test")
		case <-testTimer.C:
			collectTicker.Stop()
			break collectLoop
		}
	}

	// =================================================================
	// QUIESCE PHASE
	// =================================================================
	fmt.Println("\n--- Quiesce Phase ---")

	fmt.Println("Sending SIGUSR1 to client-udp (pause data)...")
	if err := signalProcess(clientUDPCmd, syscall.SIGUSR1); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to send SIGUSR1 to client-udp: %v\n", err)
	}

	fmt.Println("Sending SIGINT to ffmpeg...")
	if err := signalProcess(ffmpegCmd, syscall.SIGINT); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to signal ffmpeg: %v\n", err)
	}
	waitForProcessExit(ffmpegCmd, 3*time.Second)

	// Wait for stabilization
	fmt.Println("Waiting for metrics to stabilize...")
	stabCtx, stabCancel := context.WithTimeout(ctx, 10*time.Second)
	stabResult := tm.WaitForStabilization(stabCtx)
	stabCancel()

	if stabResult.Stable {
		fmt.Printf("Stabilized in %v\n", stabResult.Elapsed.Round(time.Millisecond))
	} else {
		fmt.Printf("Warning: Stabilization timeout: %v\n", stabResult.Error)
	}

	// Collect pre-shutdown metrics
	fmt.Println("Collecting pre-shutdown metrics...")
	tm.CollectAllMetrics(ctx, "pre-shutdown")

	// =================================================================
	// SHUTDOWN PHASE
	// =================================================================
	fmt.Println("\n--- Shutdown Phase ---")

	allPassed := true

	// Client first
	fmt.Println("Sending SIGINT to client...")
	if err := signalProcess(clientCmd, syscall.SIGINT); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to signal client: %v\n", err)
	}
	if !waitForProcessExit(clientCmd, 5*time.Second) {
		fmt.Println("  Client did not exit gracefully")
		killProcess(clientCmd)
		allPassed = false
	} else {
		fmt.Println("  Client exited gracefully")
	}

	// Client-UDP
	fmt.Println("Sending SIGINT to client-udp...")
	if err := signalProcess(clientUDPCmd, syscall.SIGINT); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to signal client-udp: %v\n", err)
	}
	if !waitForProcessExit(clientUDPCmd, 8*time.Second) {
		fmt.Println("  Client-udp did not exit gracefully")
		killProcess(clientUDPCmd)
		allPassed = false
	} else {
		fmt.Println("  Client-udp exited gracefully")
	}

	// Server
	fmt.Println("Sending SIGINT to server...")
	if err := signalProcess(serverCmd, syscall.SIGINT); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to signal server: %v\n", err)
	}
	if !waitForProcessExit(serverCmd, 10*time.Second) {
		fmt.Println("  Server did not exit gracefully")
		killProcess(serverCmd)
		allPassed = false
	} else {
		fmt.Println("  Server exited gracefully")
	}

	// Collect final metrics
	fmt.Println("\nCollecting final metrics...")
	tm.CollectAllMetrics(ctx, "final")

	// =================================================================
	// ANALYSIS PHASE
	// =================================================================
	fmt.Println("\n--- Analysis Phase ---")
	analysisPass := analyzeCleanroomMetrics(tm)

	if !allPassed {
		fmt.Println("\nWARNING: Not all processes exited gracefully")
	}

	if allPassed && analysisPass {
		fmt.Println("\n╔══════════════════════════════════════════════════════════════╗")
		fmt.Println("║  OVERALL: PASS                                             ║")
		fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	} else {
		fmt.Println("\n╔══════════════════════════════════════════════════════════════╗")
		fmt.Println("║  OVERALL: FAIL                                             ║")
		fmt.Println("╚══════════════════════════════════════════════════════════════╝")
		os.Exit(1)
	}
}

// analyzeCleanroomMetrics performs cross-connection analysis on metrics
// from all 3 processes. Returns true if all checks pass.
func analyzeCleanroomMetrics(tm *TestMetrics) bool {
	// Use pre-shutdown snapshot for analysis (most stable)
	serverSnap := tm.GetSnapshotByLabel("server", "pre-shutdown")
	cudpSnap := tm.GetSnapshotByLabel("clientgen", "pre-shutdown")
	clientSnap := tm.GetSnapshotByLabel("client", "pre-shutdown")

	if serverSnap == nil || serverSnap.Error != nil {
		fmt.Println("ERROR: No server metrics available for analysis")
		return false
	}
	if cudpSnap == nil || cudpSnap.Error != nil {
		fmt.Println("ERROR: No client-udp metrics available for analysis")
		return false
	}
	if clientSnap == nil || clientSnap.Error != nil {
		fmt.Println("ERROR: No client metrics available for analysis")
		return false
	}

	// Step 1: Parse connection identity from all 3 processes
	serverEndpoints := parseConnectionEndpoints(serverSnap.Raw, "server")
	cudpEndpoints := parseConnectionEndpoints(cudpSnap.Raw, "client-udp")
	clientEndpoints := parseConnectionEndpoints(clientSnap.Raw, "client")

	fmt.Printf("\nConnection endpoints found:\n")
	fmt.Printf("  Server:     %d connections\n", len(serverEndpoints))
	fmt.Printf("  Client-UDP: %d connections\n", len(cudpEndpoints))
	fmt.Printf("  Client:     %d connections\n", len(clientEndpoints))

	if len(serverEndpoints) < 2 {
		fmt.Println("ERROR: Server should have at least 2 connections (publish + subscribe)")
		return false
	}
	if len(cudpEndpoints) < 1 {
		fmt.Println("ERROR: Client-UDP should have at least 1 connection")
		return false
	}
	if len(clientEndpoints) < 1 {
		fmt.Println("ERROR: Client should have at least 1 connection")
		return false
	}

	// Step 2: Correlate connections across processes
	pairs := correlateConnections(serverEndpoints, cudpEndpoints, clientEndpoints)

	if len(pairs) == 0 {
		fmt.Println("ERROR: Could not correlate any connections across processes")
		fmt.Println("  Server endpoints:")
		for _, ep := range serverEndpoints {
			fmt.Printf("    socket=%s peer=%s stream=%s type=%s\n", ep.SocketID, ep.PeerSocketID, ep.StreamID, ep.PeerType)
		}
		fmt.Println("  Client-UDP endpoints:")
		for _, ep := range cudpEndpoints {
			fmt.Printf("    socket=%s peer=%s stream=%s type=%s\n", ep.SocketID, ep.PeerSocketID, ep.StreamID, ep.PeerType)
		}
		fmt.Println("  Client endpoints:")
		for _, ep := range clientEndpoints {
			fmt.Printf("    socket=%s peer=%s stream=%s type=%s\n", ep.SocketID, ep.PeerSocketID, ep.StreamID, ep.PeerType)
		}
		return false
	}

	fmt.Printf("  Correlated %d connection pair(s)\n", len(pairs))

	// Step 3-5: For each pair, extract metrics, compare, and report
	allRaw := map[string]string{
		"server":     serverSnap.Raw,
		"client-udp": cudpSnap.Raw,
		"client":     clientSnap.Raw,
	}

	totalSignificant := 0
	for _, pair := range pairs {
		significant := analyzeConnectionPair(pair, allRaw)
		totalSignificant += significant
	}

	// Overall summary
	fmt.Printf("\n═══════════════════════════════════════════════════════════════\n")
	if totalSignificant == 0 {
		fmt.Printf(" OVERALL: PASS — %d connections, 0 significant differences\n", len(pairs))
	} else {
		fmt.Printf(" OVERALL: FAIL — %d connections, %d significant differences\n", len(pairs), totalSignificant)
	}
	fmt.Printf("═══════════════════════════════════════════════════════════════\n")

	return totalSignificant == 0
}

// parseConnectionEndpoints extracts connection identity from raw Prometheus metrics.
// Looks for gosrt_connection_start_time_seconds lines with socket_id, peer_socket_id, etc.
func parseConnectionEndpoints(raw, component string) []connectionEndpoint {
	var endpoints []connectionEndpoint

	// Match: gosrt_connection_start_time_seconds{socket_id="...", ...} value
	re := regexp.MustCompile(`gosrt_connection_start_time_seconds\{([^}]+)\}`)

	for _, line := range strings.Split(raw, "\n") {
		matches := re.FindStringSubmatch(line)
		if len(matches) < 2 {
			continue
		}

		labels := parseLabels(matches[1])
		ep := connectionEndpoint{
			SocketID:     labels["socket_id"],
			PeerSocketID: labels["peer_socket_id"],
			StreamID:     labels["stream_id"],
			PeerType:     labels["peer_type"],
			Component:    component,
		}
		if ep.SocketID != "" {
			endpoints = append(endpoints, ep)
		}
	}

	return endpoints
}

// parseLabels parses Prometheus label string: key1="val1",key2="val2" → map
func parseLabels(labelStr string) map[string]string {
	labels := make(map[string]string)

	// Simple state machine to handle commas inside quoted values
	var key, value strings.Builder
	inValue := false
	inQuote := false
	parsingKey := true

	for i := 0; i < len(labelStr); i++ {
		ch := labelStr[i]

		switch {
		case ch == '=' && !inQuote && parsingKey:
			parsingKey = false
		case ch == '"' && !inQuote:
			inQuote = true
			inValue = true
		case ch == '"' && inQuote:
			inQuote = false
			labels[key.String()] = value.String()
			key.Reset()
			value.Reset()
			inValue = false
			parsingKey = true
		case ch == ',' && !inQuote:
			parsingKey = true
		case inValue:
			value.WriteByte(ch)
		case parsingKey:
			key.WriteByte(ch)
		}
	}

	return labels
}

// correlateConnections matches connection endpoints across processes.
// Returns pairs where one process's socket_id matches the other's peer_socket_id.
func correlateConnections(serverEps, cudpEps, clientEps []connectionEndpoint) []connectionPair {
	var pairs []connectionPair

	// Build lookup: socket_id → endpoint
	serverBySocket := make(map[string]connectionEndpoint)
	for _, ep := range serverEps {
		serverBySocket[ep.SocketID] = ep
	}

	// Conn 1 (publish path): client-udp → server
	// client-udp's peer_socket_id should match a server socket_id
	for _, cudpEp := range cudpEps {
		if serverEp, ok := serverBySocket[cudpEp.PeerSocketID]; ok {
			pairs = append(pairs, connectionPair{
				Label:    "publish",
				Sender:   cudpEp,
				Receiver: serverEp,
			})
			break
		}
	}

	// Conn 2 (subscribe path): server → client
	// client's peer_socket_id should match a server socket_id
	for _, clientEp := range clientEps {
		if serverEp, ok := serverBySocket[clientEp.PeerSocketID]; ok {
			pairs = append(pairs, connectionPair{
				Label:    "subscribe",
				Sender:   serverEp,
				Receiver: clientEp,
			})
			break
		}
	}

	return pairs
}

// analyzeConnectionPair compares metrics between the two ends of a connection.
// Returns the number of significant differences found.
func analyzeConnectionPair(pair connectionPair, allRaw map[string]string) int {
	fmt.Printf("\n═══════════════════════════════════════════════════════════════\n")
	fmt.Printf(" Connection: %s (%s) → %s (%s) [%s]\n",
		pair.Sender.Component, pair.Sender.SocketID,
		pair.Receiver.Component, pair.Receiver.SocketID,
		pair.Label)
	fmt.Printf("═══════════════════════════════════════════════════════════════\n")

	// Extract per-socket metrics
	senderMetrics := extractMetricsForSocket(allRaw[pair.Sender.Component], pair.Sender.SocketID)
	receiverMetrics := extractMetricsForSocket(allRaw[pair.Receiver.Component], pair.Receiver.SocketID)

	// Compare metrics
	comparisons := compareEndpointMetrics(senderMetrics, receiverMetrics)

	// Sort by category then name
	sort.Slice(comparisons, func(i, j int) bool {
		if comparisons[i].Category != comparisons[j].Category {
			return categoryOrder(comparisons[i].Category) < categoryOrder(comparisons[j].Category)
		}
		return comparisons[i].Name < comparisons[j].Name
	})

	// Print report
	fmt.Printf("\n %-45s %12s %12s %10s %s\n", "Metric", "Sender", "Receiver", "Delta", "Status")
	fmt.Printf(" %s\n", strings.Repeat("─", 95))

	significantCount := 0
	var significantDetails []metricComparison

	lastCategory := ""
	for _, cmp := range comparisons {
		if cmp.Category != lastCategory {
			fmt.Printf("\n [%s]\n", strings.ToUpper(cmp.Category))
			lastCategory = cmp.Category
		}

		status := "✓"
		if cmp.Significant {
			status = "✗"
			significantCount++
			significantDetails = append(significantDetails, cmp)
		}

		// Format metric name (strip common prefix)
		displayName := cleanMetricName(cmp.Name)

		fmt.Printf(" %-45s %12s %12s %10s %s\n",
			cleanroomTruncate(displayName, 45),
			formatMetricValue(cmp.SenderValue),
			formatMetricValue(cmp.ReceiverValue),
			cleanroomFormatDelta(cmp.Delta),
			status)
	}

	// Print significant differences
	if len(significantDetails) > 0 {
		fmt.Printf("\n ⚠ SIGNIFICANT DIFFERENCES (%d):\n", len(significantDetails))
		for _, cmp := range significantDetails {
			displayName := cleanMetricName(cmp.Name)
			if cmp.Category == "error" {
				fmt.Printf("   %s: sender=%.0f receiver=%.0f (any non-zero is a bug)\n",
					displayName, cmp.SenderValue, cmp.ReceiverValue)
			} else {
				fmt.Printf("   %s: sender=%.0f receiver=%.0f delta=%.0f (%.1f%%)\n",
					displayName, cmp.SenderValue, cmp.ReceiverValue, cmp.Delta, cmp.DeltaPct)
			}
		}
	} else {
		fmt.Printf("\n ✓ No significant differences — clean run\n")
	}

	return significantCount
}

// extractMetricsForSocket returns all metrics for a given socket_id from raw Prometheus output.
func extractMetricsForSocket(raw, socketID string) map[string]float64 {
	metrics := make(map[string]float64)
	if raw == "" || socketID == "" {
		return metrics
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check if this line contains the target socket_id
		socketLabel := fmt.Sprintf(`socket_id="%s"`, socketID)
		if !strings.Contains(line, socketLabel) {
			continue
		}

		// Parse metric name and value
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		value, err := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err != nil {
			continue
		}

		// Normalize the key: remove socket_id and instance labels
		// so metrics from different components can be paired
		key := cleanroomNormalizeKey(parts[0])
		metrics[key] = value
	}

	return metrics
}

// compareEndpointMetrics compares sender and receiver metric sets.
func compareEndpointMetrics(senderMetrics, receiverMetrics map[string]float64) []metricComparison {
	var comparisons []metricComparison

	// Collect all metric keys
	allKeys := make(map[string]bool)
	for k := range senderMetrics {
		allKeys[k] = true
	}
	for k := range receiverMetrics {
		allKeys[k] = true
	}

	// Build paired comparisons using metric matching rules
	paired := make(map[string]bool) // track which keys we've already paired

	for key := range allKeys {
		if paired[key] {
			continue
		}

		category := categorizeMetric(key)
		if category == "identity" {
			continue // skip identity metrics (start_time, etc.)
		}

		paired[key] = true

		// Try to find a cross-direction pair
		crossKey := crossDirectionKey(key)
		sVal := senderMetrics[key]
		rVal := receiverMetrics[key]

		if crossKey != "" && crossKey != key {
			// Cross-direction pairing: use crossKey for receiver lookup
			sVal = senderMetrics[key]
			rVal = receiverMetrics[crossKey]
			paired[crossKey] = true
		}

		// Detect one-sided metrics: key exists only on one side with no
		// matching key (or cross-key) on the other. These are endpoint-specific
		// operational metrics (e.g., send_delivery_*, recv_light_ack_*, ring_drained_*).
		_, senderHas := senderMetrics[key]
		_, receiverHas := receiverMetrics[key]
		if crossKey != "" && crossKey != key {
			_, receiverHasCross := receiverMetrics[crossKey]
			receiverHas = receiverHas || receiverHasCross
			_, senderHasCross := senderMetrics[crossKey]
			senderHas = senderHas || senderHasCross
		}
		if !senderHas || !receiverHas {
			category = "info" // one-sided metric, informational only
		}

		delta := sVal - rVal
		deltaPct := 0.0
		if sVal > 0 {
			deltaPct = math.Abs(delta) / sVal * 100
		} else if rVal > 0 {
			deltaPct = math.Abs(delta) / rVal * 100
		}

		significant := isSignificantDifference(key, category, sVal, rVal, deltaPct)

		comparisons = append(comparisons, metricComparison{
			Name:          key,
			SenderValue:   sVal,
			ReceiverValue: rVal,
			Delta:         delta,
			DeltaPct:      deltaPct,
			Significant:   significant,
			Category:      category,
		})
	}

	return comparisons
}

// crossDirectionKey returns the paired metric key by swapping direction labels.
func crossDirectionKey(key string) string {
	if strings.Contains(key, `direction="send"`) {
		return strings.Replace(key, `direction="send"`, `direction="recv"`, 1)
	}
	if strings.Contains(key, `direction="recv"`) {
		return strings.Replace(key, `direction="recv"`, `direction="send"`, 1)
	}
	// For sent/received control packets — but NOT summary type="all"
	// (there's no packets_sent_total{type="all"} counterpart)
	if strings.Contains(key, `type="all"`) {
		return ""
	}
	if strings.Contains(key, "packets_sent_total") {
		return strings.Replace(key, "packets_sent_total", "packets_received_total", 1)
	}
	if strings.Contains(key, "packets_received_total") {
		return strings.Replace(key, "packets_received_total", "packets_sent_total", 1)
	}
	return ""
}

// categorizeMetric returns the category for a metric key.
func categorizeMetric(key string) string {
	lower := strings.ToLower(key)

	// Identity metrics (skip)
	if strings.Contains(lower, "start_time") || strings.Contains(lower, "info") {
		return "identity"
	}

	// Informational metrics that look like errors but aren't:
	// - io_uring completion_timeout: normal yield mechanism
	// - drop_fires: TLPktDrop timer fires (operational, not an error)
	// - eventloop_idle_backoffs: normal backoff behavior
	// - periodic_nak_runs, nak_periodic_*: operational timer counters
	// - bytes_sent_total, bytes_received_total: raw I/O byte counters (not SRT-level)
	if strings.Contains(lower, "completion_timeout") ||
		strings.Contains(lower, "drop_fires") ||
		strings.Contains(lower, "idle_backoffs") ||
		strings.Contains(lower, "backoff") ||
		strings.Contains(lower, "periodic_nak_runs") ||
		strings.Contains(lower, "nak_periodic") ||
		strings.Contains(lower, "bytes_sent_total") ||
		strings.Contains(lower, "bytes_received_total") {
		return "info"
	}

	// Error/drop/loss metrics (zero tolerance)
	if strings.Contains(lower, "error") ||
		strings.Contains(lower, "drop") ||
		strings.Contains(lower, "lost") ||
		strings.Contains(lower, "fail") ||
		strings.Contains(lower, "timeout") {
		return "error"
	}

	// Control metrics (ACK, NAK, ACKACK, keepalive)
	if strings.Contains(lower, "ack") ||
		strings.Contains(lower, "nak") ||
		strings.Contains(lower, "keepalive") ||
		strings.Contains(lower, "handshake") {
		return "control"
	}

	// Flow metrics (packets, bytes)
	if strings.Contains(lower, "packet") ||
		strings.Contains(lower, "byte") ||
		strings.Contains(lower, "congestion") ||
		strings.Contains(lower, "retransmis") {
		return "flow"
	}

	// Everything else is informational
	return "info"
}

// isSignificantDifference determines if a metric difference is significant enough to flag.
func isSignificantDifference(_, category string, senderVal, receiverVal, deltaPct float64) bool {
	switch category {
	case "error":
		// Any non-zero error/drop/loss counter is significant
		return senderVal != 0 || receiverVal != 0
	case "flow":
		// Flow counters should match within tolerance.
		// Require both percentage threshold AND minimum absolute delta
		// to avoid flagging tiny timing-dependent gauge differences.
		if senderVal == 0 && receiverVal == 0 {
			return false
		}
		absDelta := math.Abs(senderVal - receiverVal)
		return deltaPct > cleanroomTolerancePct && absDelta > 5
	case "control":
		// Control counters have timing skew; use same tolerance + minimum
		if senderVal == 0 && receiverVal == 0 {
			return false
		}
		absDelta := math.Abs(senderVal - receiverVal)
		return deltaPct > cleanroomTolerancePct && absDelta > 5
	case "info":
		// Informational only — never flag
		return false
	}
	return false
}

// categoryOrder returns sort order for categories.
func categoryOrder(cat string) int {
	switch cat {
	case "flow":
		return 0
	case "error":
		return 1
	case "control":
		return 2
	case "info":
		return 3
	}
	return 4
}

// cleanMetricName strips common "gosrt_connection_" prefix for display.
func cleanMetricName(name string) string {
	name = strings.TrimPrefix(name, "gosrt_connection_")
	name = strings.TrimPrefix(name, "gosrt_")
	return name
}

// cleanroomNormalizeKey removes socket_id and instance labels from a metric key
// so that metrics from different components (server vs client) can be paired.
func cleanroomNormalizeKey(name string) string {
	// First strip socket_id using existing normalizer
	name = normalizeMetricKey(name)
	// Then strip instance label using the same approach
	return stripLabel(name, "instance")
}

// stripLabel removes a specific label from a Prometheus metric key.
func stripLabel(name, label string) string {
	idx := strings.Index(name, "{")
	if idx < 0 {
		return name
	}

	basePart := name[:idx]
	labelPart := name[idx:]

	needle := label + `="`
	for {
		labelIdx := strings.Index(labelPart, needle)
		if labelIdx < 0 {
			break
		}

		valueStart := labelIdx + len(needle)
		valueEnd := strings.Index(labelPart[valueStart:], `"`)
		if valueEnd < 0 {
			break
		}
		valueEnd += valueStart + 1 // past closing quote

		removeStart := labelIdx
		removeEnd := valueEnd

		// Remove trailing comma or leading comma
		if removeEnd < len(labelPart) && labelPart[removeEnd] == ',' {
			removeEnd++
		} else if removeStart > 1 && labelPart[removeStart-1] == ',' {
			removeStart--
		}

		labelPart = labelPart[:removeStart] + labelPart[removeEnd:]
	}

	if labelPart == "{}" {
		return basePart
	}

	return basePart + labelPart
}

// cleanroomFormatDelta formats a delta value for display.
func cleanroomFormatDelta(d float64) string {
	if d == 0 {
		return "0"
	}
	if d == float64(int64(d)) {
		return fmt.Sprintf("%+d", int64(d))
	}
	return fmt.Sprintf("%+.2f", d)
}

// cleanroomTruncate truncates a string to maxLen characters.
func cleanroomTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-2] + ".."
}
