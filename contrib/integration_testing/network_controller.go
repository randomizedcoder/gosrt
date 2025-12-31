package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NetworkController manages network namespaces and impairment for integration testing.
// It wraps the shell scripts in contrib/integration_testing/network/ to provide
// a Go-native interface for controlling network conditions during tests.
type NetworkController struct {
	// TestID is the unique identifier for this test run (used in namespace names)
	TestID string

	// ScriptDir is the directory containing the network scripts
	ScriptDir string

	// Namespace names (populated after Setup)
	NamespacePublisher    string
	NamespaceSubscriber   string
	NamespaceServer       string
	NamespaceRouterClient string
	NamespaceRouterServer string

	// IP addresses (populated after Setup)
	IPPublisher  string
	IPSubscriber string
	IPServer     string

	// Current state
	CurrentLatencyProfile int
	CurrentLossPercent    int

	// Pattern control
	patternCancel context.CancelFunc
	patternWg     sync.WaitGroup

	// Mutex for state changes
	mu sync.Mutex

	// isSetup tracks whether the network has been created
	isSetup bool

	// Verbose enables detailed logging of pattern events and script execution
	Verbose bool
}

// NetworkControllerConfig holds configuration for creating a NetworkController
type NetworkControllerConfig struct {
	// TestID is the unique identifier for this test run
	// If empty, the current process PID will be used
	TestID string

	// ScriptDir is the directory containing the network scripts
	// If empty, it will be auto-detected relative to the current executable
	ScriptDir string

	// Verbose enables detailed logging of pattern events and script execution.
	// When true, the controller logs each pattern event and passes SRT_NETWORK_DEBUG=1
	// to shell scripts for additional debug output.
	Verbose bool
}

// NewNetworkController creates a new NetworkController with the given configuration
func NewNetworkController(cfg NetworkControllerConfig) (*NetworkController, error) {
	testID := cfg.TestID
	if testID == "" {
		testID = strconv.Itoa(os.Getpid())
	}

	scriptDir := cfg.ScriptDir
	if scriptDir == "" {
		// Try multiple locations to find the network scripts directory
		candidates := []string{}

		// 1. Try relative to the executable
		if execPath, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(execPath), "network"))
		}

		// 2. Try 'network' subdirectory of current working directory
		if cwd, err := os.Getwd(); err == nil {
			candidates = append(candidates, filepath.Join(cwd, "network"))
			// 3. Try full path from project root
			candidates = append(candidates, filepath.Join(cwd, "contrib", "integration_testing", "network"))
		}

		// Find the first candidate that exists
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				scriptDir = candidate
				break
			}
		}
	}

	// Verify script directory exists
	if _, err := os.Stat(scriptDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("script directory not found: %s", scriptDir)
	}

	// Verify required scripts exist
	requiredScripts := []string{"lib.sh", "setup.sh", "cleanup.sh", "set_latency.sh", "set_loss.sh"}
	for _, script := range requiredScripts {
		scriptPath := filepath.Join(scriptDir, script)
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("required script not found: %s", scriptPath)
		}
	}

	nc := &NetworkController{
		TestID:    testID,
		ScriptDir: scriptDir,
		Verbose:   cfg.Verbose,

		// Default IP addresses (from lib.sh)
		IPPublisher:  "10.1.1.2",
		IPSubscriber: "10.1.2.2",
		IPServer:     "10.2.1.2",

		// Namespace names
		NamespacePublisher:    fmt.Sprintf("ns_publisher_%s", testID),
		NamespaceSubscriber:   fmt.Sprintf("ns_subscriber_%s", testID),
		NamespaceServer:       fmt.Sprintf("ns_server_%s", testID),
		NamespaceRouterClient: fmt.Sprintf("ns_router_a_%s", testID),
		NamespaceRouterServer: fmt.Sprintf("ns_router_b_%s", testID),
	}

	return nc, nil
}

// Setup creates the network namespaces and configures the topology.
// This must be called before any other operations.
// Requires root privileges.
func (nc *NetworkController) Setup(ctx context.Context) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if nc.isSetup {
		return fmt.Errorf("network already setup")
	}

	// Run setup.sh with TEST_ID environment variable
	if err := nc.runScript(ctx, "setup.sh"); err != nil {
		return fmt.Errorf("setup failed: %w", err)
	}

	nc.isSetup = true
	nc.CurrentLatencyProfile = 0
	nc.CurrentLossPercent = 0

	return nil
}

// Cleanup removes all network namespaces and resources.
// This should be called when the test is complete, typically via defer.
func (nc *NetworkController) Cleanup(ctx context.Context) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	// Stop any running patterns
	if nc.patternCancel != nil {
		nc.patternCancel()
		nc.patternWg.Wait()
		nc.patternCancel = nil
	}

	// Run cleanup.sh
	if err := nc.runScript(ctx, "cleanup.sh"); err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}

	nc.isSetup = false
	return nil
}

// SetLatencyProfile switches to a different latency profile by changing routing.
// Profile values:
//   - 0: No delay (0ms RTT)
//   - 1: Regional datacenter (10ms RTT)
//   - 2: Cross-continental (60ms RTT)
//   - 3: Intercontinental (130ms RTT)
//   - 4: GEO satellite (300ms RTT)
func (nc *NetworkController) SetLatencyProfile(ctx context.Context, profile int) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if !nc.isSetup {
		return fmt.Errorf("network not setup")
	}

	if profile < 0 || profile > 4 {
		return fmt.Errorf("invalid latency profile: %d (must be 0-4)", profile)
	}

	if err := nc.runScript(ctx, "set_latency.sh", strconv.Itoa(profile)); err != nil {
		return fmt.Errorf("set latency failed: %w", err)
	}

	nc.CurrentLatencyProfile = profile
	return nil
}

// SetLoss sets the packet loss percentage.
// Loss values:
//   - 0: No loss
//   - 1-99: Probabilistic loss via netem
//   - 100: Complete outage via blackhole routes
func (nc *NetworkController) SetLoss(ctx context.Context, percent int) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if !nc.isSetup {
		return fmt.Errorf("network not setup")
	}

	if percent < 0 || percent > 100 {
		return fmt.Errorf("invalid loss percent: %d (must be 0-100)", percent)
	}

	if err := nc.runScript(ctx, "set_loss.sh", strconv.Itoa(percent)); err != nil {
		return fmt.Errorf("set loss failed: %w", err)
	}

	nc.CurrentLossPercent = percent
	return nil
}

// LossPattern defines a pattern for dynamic loss injection
type LossPattern struct {
	// Name is a human-readable name for the pattern
	Name string

	// Events defines the loss events in the pattern
	Events []LossEvent

	// RepeatInterval is how often the pattern repeats (0 = no repeat)
	RepeatInterval time.Duration
}

// LossEvent defines a single loss event in a pattern
type LossEvent struct {
	// Offset is the time offset from the start of the pattern (or minute for Starlink)
	Offset time.Duration

	// Duration is how long the loss lasts
	Duration time.Duration

	// LossPercent is the loss percentage during this event
	LossPercent int
}

// Predefined patterns
var (
	// PatternStarlink simulates LEO satellite reconvergence events
	// 100% loss for 60ms at seconds 12, 27, 42, 57 of each minute
	PatternStarlink = LossPattern{
		Name: "starlink",
		Events: []LossEvent{
			{Offset: 12 * time.Second, Duration: 60 * time.Millisecond, LossPercent: 100},
			{Offset: 27 * time.Second, Duration: 60 * time.Millisecond, LossPercent: 100},
			{Offset: 42 * time.Second, Duration: 60 * time.Millisecond, LossPercent: 100},
			{Offset: 57 * time.Second, Duration: 60 * time.Millisecond, LossPercent: 100},
		},
		RepeatInterval: time.Minute,
	}

	// PatternHighLossBurst simulates severe network degradation
	// 85% loss for 1 second at 1.5 seconds into each minute
	PatternHighLossBurst = LossPattern{
		Name: "high_loss_burst",
		Events: []LossEvent{
			{Offset: 1500 * time.Millisecond, Duration: time.Second, LossPercent: 85},
		},
		RepeatInterval: time.Minute,
	}
)

// StartPattern starts a dynamic loss pattern in the background.
// Only one pattern can be active at a time.
func (nc *NetworkController) StartPattern(ctx context.Context, pattern LossPattern) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if !nc.isSetup {
		return fmt.Errorf("network not setup")
	}

	// Stop any existing pattern
	if nc.patternCancel != nil {
		nc.patternCancel()
		nc.patternWg.Wait()
	}

	// Create cancellable context for the pattern
	patternCtx, cancel := context.WithCancel(ctx)
	nc.patternCancel = cancel

	nc.patternWg.Add(1)
	go nc.runPattern(patternCtx, pattern)

	return nil
}

// StopPattern stops any running loss pattern
func (nc *NetworkController) StopPattern(ctx context.Context) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if nc.patternCancel != nil {
		nc.patternCancel()
		nc.patternWg.Wait()
		nc.patternCancel = nil
	}

	// Clear any loss that was set by the pattern
	// IMPORTANT: Pass CURRENT_LATENCY_PROFILE explicitly
	if nc.isSetup {
		script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent 0",
			nc.ScriptDir, nc.CurrentLatencyProfile)
		cmd := exec.CommandContext(ctx, "bash", "-c", script)
		cmd.Env = nc.buildScriptEnv()
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to clear loss: %w\nOutput: %s", err, string(output))
		}
		nc.CurrentLossPercent = 0
	}

	return nil
}

// runPattern executes a loss pattern in a loop
func (nc *NetworkController) runPattern(ctx context.Context, pattern LossPattern) {
	defer nc.patternWg.Done()

	if nc.Verbose {
		fmt.Printf("[PATTERN] Starting pattern %q with %d events, repeat=%v\n",
			pattern.Name, len(pattern.Events), pattern.RepeatInterval)
	}

	eventCount := 0
	for {
		// Calculate time until next event
		now := time.Now()
		var nextEvent *LossEvent
		var waitDuration time.Duration

		if pattern.RepeatInterval > 0 {
			// For repeating patterns, find next event based on current time within the interval
			offsetInInterval := now.UnixNano() % int64(pattern.RepeatInterval)
			currentOffset := time.Duration(offsetInInterval)

			for i := range pattern.Events {
				if pattern.Events[i].Offset > currentOffset {
					nextEvent = &pattern.Events[i]
					waitDuration = pattern.Events[i].Offset - currentOffset
					break
				}
			}

			// If no event found in current interval, use first event of next interval
			if nextEvent == nil && len(pattern.Events) > 0 {
				nextEvent = &pattern.Events[0]
				waitDuration = pattern.RepeatInterval - currentOffset + pattern.Events[0].Offset
			}
		}

		if nextEvent == nil {
			// No events to run
			if nc.Verbose {
				fmt.Printf("[PATTERN] No more events, exiting pattern loop\n")
			}
			return
		}

		if nc.Verbose {
			fmt.Printf("[PATTERN] Next event: %d%% loss for %v, waiting %v\n",
				nextEvent.LossPercent, nextEvent.Duration, waitDuration)
		}

		// Wait for next event
		select {
		case <-ctx.Done():
			if nc.Verbose {
				fmt.Printf("[PATTERN] Context cancelled while waiting for event\n")
			}
			return
		case <-time.After(waitDuration):
		}

		eventCount++

		// Apply the loss
		nc.mu.Lock()
		if nc.isSetup {
			if nc.Verbose {
				fmt.Printf("[PATTERN] Event #%d: Applying %d%% loss at %v (latency profile %d)\n",
					eventCount, nextEvent.LossPercent, time.Now().Format("15:04:05.000"), nc.CurrentLatencyProfile)
			}
			// Use inline bash with CURRENT_LATENCY_PROFILE set explicitly
			script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent %d",
				nc.ScriptDir, nc.CurrentLatencyProfile, nextEvent.LossPercent)
			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Env = nc.buildScriptEnv()
			if output, err := cmd.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "[PATTERN] ERROR applying %d%% loss: %v\nOutput: %s\n", nextEvent.LossPercent, err, string(output))
			}
			nc.dumpRouteTables(ctx)
		}
		nc.mu.Unlock()

		// Wait for event duration
		select {
		case <-ctx.Done():
			// Clear loss before exiting
			if nc.Verbose {
				fmt.Printf("[PATTERN] Context cancelled during event, clearing loss\n")
			}
			nc.mu.Lock()
			if nc.isSetup {
				script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent 0",
					nc.ScriptDir, nc.CurrentLatencyProfile)
				cmd := exec.CommandContext(ctx, "bash", "-c", script)
				cmd.Env = nc.buildScriptEnv()
				if output, err := cmd.CombinedOutput(); err != nil {
					fmt.Fprintf(os.Stderr, "[PATTERN] ERROR clearing loss on exit: %v\nOutput: %s\n", err, string(output))
				}
				nc.dumpRouteTables(ctx)
			}
			nc.mu.Unlock()
			return
		case <-time.After(nextEvent.Duration):
		}

		// Clear the loss
		nc.mu.Lock()
		if nc.isSetup {
			if nc.Verbose {
				fmt.Printf("[PATTERN] Event #%d: Clearing loss at %v (after %v)\n",
					eventCount, time.Now().Format("15:04:05.000"), nextEvent.Duration)
			}
			script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent 0",
				nc.ScriptDir, nc.CurrentLatencyProfile)
			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Env = nc.buildScriptEnv()
			if output, err := cmd.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "[PATTERN] ERROR clearing loss: %v\nOutput: %s\n", err, string(output))
			}
			nc.dumpRouteTables(ctx)
		}
		nc.mu.Unlock()
	}
}

// dumpRouteTables prints the route tables from both routers when verbose mode is enabled.
// This is useful for debugging blackhole route application/removal.
func (nc *NetworkController) dumpRouteTables(ctx context.Context) {
	if !nc.Verbose || !nc.isSetup {
		return
	}

	fmt.Printf("[ROUTES] Route tables at %v:\n", time.Now().Format("15:04:05.000"))

	// Router A (client-side router)
	routerACmd := exec.CommandContext(ctx, "ip", "netns", "exec", nc.NamespaceRouterClient, "ip", "route", "show")
	if output, err := routerACmd.CombinedOutput(); err == nil {
		fmt.Printf("[ROUTES] Router A (%s):\n", nc.NamespaceRouterClient)
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" {
				// Highlight blackhole routes
				if strings.Contains(line, "blackhole") {
					fmt.Printf("[ROUTES]   >>> %s <<<\n", line)
				} else {
					fmt.Printf("[ROUTES]   %s\n", line)
				}
			}
		}
	} else {
		fmt.Printf("[ROUTES] Router A: ERROR: %v\n", err)
	}

	// Router B (server-side router)
	routerBCmd := exec.CommandContext(ctx, "ip", "netns", "exec", nc.NamespaceRouterServer, "ip", "route", "show")
	if output, err := routerBCmd.CombinedOutput(); err == nil {
		fmt.Printf("[ROUTES] Router B (%s):\n", nc.NamespaceRouterServer)
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" {
				// Highlight blackhole routes
				if strings.Contains(line, "blackhole") {
					fmt.Printf("[ROUTES]   >>> %s <<<\n", line)
				} else {
					fmt.Printf("[ROUTES]   %s\n", line)
				}
			}
		}
	} else {
		fmt.Printf("[ROUTES] Router B: ERROR: %v\n", err)
	}
}

// RunInNamespace executes a command in a specific namespace.
// Returns the command's stdout, stderr combined output, and any error.
func (nc *NetworkController) RunInNamespace(ctx context.Context, namespace string, name string, args ...string) ([]byte, error) {
	if !nc.isSetup {
		return nil, fmt.Errorf("network not setup")
	}

	cmdArgs := append([]string{"netns", "exec", namespace, name}, args...)
	cmd := exec.CommandContext(ctx, "ip", cmdArgs...)
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("command failed in namespace %s: %w\nOutput: %s", namespace, err, string(output))
	}

	return output, nil
}

// StartProcessInNamespace starts a process in a namespace and returns the exec.Cmd.
// The caller is responsible for managing the process lifecycle.
func (nc *NetworkController) StartProcessInNamespace(ctx context.Context, namespace string, name string, args ...string) (*exec.Cmd, error) {
	if !nc.isSetup {
		return nil, fmt.Errorf("network not setup")
	}

	cmdArgs := append([]string{"netns", "exec", namespace, name}, args...)
	cmd := exec.CommandContext(ctx, "ip", cmdArgs...)
	cmd.Env = os.Environ()

	return cmd, nil
}

// GetNamespace returns the namespace name for a component
func (nc *NetworkController) GetNamespace(component string) (string, error) {
	switch strings.ToLower(component) {
	case "publisher", "client-generator", "clientgenerator":
		return nc.NamespacePublisher, nil
	case "subscriber", "client":
		return nc.NamespaceSubscriber, nil
	case "server":
		return nc.NamespaceServer, nil
	default:
		return "", fmt.Errorf("unknown component: %s", component)
	}
}

// GetIP returns the IP address for a component
func (nc *NetworkController) GetIP(component string) (string, error) {
	switch strings.ToLower(component) {
	case "publisher", "client-generator", "clientgenerator":
		return nc.IPPublisher, nil
	case "subscriber", "client":
		return nc.IPSubscriber, nil
	case "server":
		return nc.IPServer, nil
	default:
		return "", fmt.Errorf("unknown component: %s", component)
	}
}

// Status returns the current network status as a string
func (nc *NetworkController) Status(ctx context.Context) (string, error) {
	if !nc.isSetup {
		return "", fmt.Errorf("network not setup")
	}

	output, err := nc.runScriptWithOutput(ctx, "status.sh")
	if err != nil {
		return "", fmt.Errorf("status failed: %w", err)
	}

	return string(output), nil
}

// IsSetup returns whether the network has been created
func (nc *NetworkController) IsSetup() bool {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	return nc.isSetup
}

// ============================================================================
// PARALLEL TEST METHODS
// ============================================================================
// These methods support running two pipelines in parallel by managing
// the .3 IP addresses and applying impairments to all 6 participant IPs.

// SetupParallelIPs adds the .3 IP addresses for the HighPerf pipeline
func (nc *NetworkController) SetupParallelIPs(ctx context.Context) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if !nc.isSetup {
		return fmt.Errorf("network not setup - call Setup() first")
	}

	// Source lib.sh and call setup_parallel_ips
	script := fmt.Sprintf("source %s/lib.sh && setup_parallel_ips", nc.ScriptDir)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = nc.buildScriptEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("setup_parallel_ips failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// CleanupParallelIPs removes the .3 IP addresses for the HighPerf pipeline
func (nc *NetworkController) CleanupParallelIPs(ctx context.Context) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	// Source lib.sh and call cleanup_parallel_ips
	script := fmt.Sprintf("source %s/lib.sh && cleanup_parallel_ips", nc.ScriptDir)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = nc.buildScriptEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cleanup_parallel_ips failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// SetLossParallel sets packet loss for parallel tests (affects all 6 IPs)
func (nc *NetworkController) SetLossParallel(ctx context.Context, lossPercent int) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if !nc.isSetup {
		return fmt.Errorf("network not setup")
	}

	// Source lib.sh and call set_loss_percent_parallel
	// IMPORTANT: Pass CURRENT_LATENCY_PROFILE explicitly because bash variable
	// doesn't persist across invocations (each bash -c creates fresh environment)
	script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent_parallel %d",
		nc.ScriptDir, nc.CurrentLatencyProfile, lossPercent)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = nc.buildScriptEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("set_loss_percent_parallel failed: %w\nOutput: %s", err, string(output))
	}

	nc.CurrentLossPercent = lossPercent
	return nil
}

// StartPatternParallel starts an impairment pattern for parallel tests (affects all 6 IPs)
func (nc *NetworkController) StartPatternParallel(ctx context.Context, pattern LossPattern) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if !nc.isSetup {
		return fmt.Errorf("network not setup")
	}

	// Cancel any existing pattern
	if nc.patternCancel != nil {
		nc.patternCancel()
		nc.patternWg.Wait()
	}

	// Create new context for pattern
	patternCtx, cancel := context.WithCancel(ctx)
	nc.patternCancel = cancel

	// Start pattern goroutine
	nc.patternWg.Add(1)
	go nc.runPatternParallel(patternCtx, pattern)

	return nil
}

// StopPatternParallel stops any running parallel impairment pattern
func (nc *NetworkController) StopPatternParallel(ctx context.Context) error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if nc.patternCancel != nil {
		nc.patternCancel()
		nc.patternWg.Wait()
		nc.patternCancel = nil
	}

	// Ensure loss is cleared for all 6 IPs
	// IMPORTANT: Pass CURRENT_LATENCY_PROFILE explicitly for clear_netem_loss
	script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && clear_blackhole_loss_parallel && clear_netem_loss",
		nc.ScriptDir, nc.CurrentLatencyProfile)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = nc.buildScriptEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clear parallel loss failed: %w\nOutput: %s", err, string(output))
	}

	nc.CurrentLossPercent = 0
	return nil
}

// runPatternParallel executes the impairment pattern for parallel tests
func (nc *NetworkController) runPatternParallel(ctx context.Context, pattern LossPattern) {
	defer nc.patternWg.Done()

	if nc.Verbose {
		fmt.Printf("[PATTERN] Starting parallel pattern \"%s\" with %d events, repeat=%v\n",
			pattern.Name, len(pattern.Events), pattern.RepeatInterval)
	}

	eventIndex := 0
	for {
		if eventIndex >= len(pattern.Events) {
			if pattern.RepeatInterval <= 0 {
				// Pattern complete, no repeat
				if nc.Verbose {
					fmt.Printf("[PATTERN] Parallel pattern \"%s\" complete (no repeat)\n", pattern.Name)
				}
				return
			}
			// Reset for next cycle
			eventIndex = 0
		}

		event := pattern.Events[eventIndex]

		// Wait for event time
		waitDuration := event.Offset
		if eventIndex > 0 {
			waitDuration = event.Offset - pattern.Events[eventIndex-1].Offset - pattern.Events[eventIndex-1].Duration
		}

		if nc.Verbose {
			fmt.Printf("[PATTERN] Next event: %d%% loss for %v, waiting %v\n",
				event.LossPercent, event.Duration, waitDuration)
		}

		select {
		case <-ctx.Done():
			if nc.Verbose {
				fmt.Printf("[PATTERN] Context cancelled while waiting for event\n")
			}
			return
		case <-time.After(waitDuration):
		}

		// Apply loss (using parallel function for all 6 IPs)
		if nc.Verbose {
			fmt.Printf("[PATTERN] Event #%d: Applying %d%% loss at %v\n",
				eventIndex+1, event.LossPercent, time.Now().Format("15:04:05.000"))
		}

		nc.mu.Lock()
		// IMPORTANT: Pass CURRENT_LATENCY_PROFILE explicitly
		script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent_parallel %d",
			nc.ScriptDir, nc.CurrentLatencyProfile, event.LossPercent)
		cmd := exec.CommandContext(ctx, "bash", "-c", script)
		cmd.Env = nc.buildScriptEnv()
		if err := cmd.Run(); err != nil {
			if nc.Verbose {
				fmt.Fprintf(os.Stderr, "[PATTERN] ERROR applying parallel loss: %v\n", err)
			}
		}
		nc.CurrentLossPercent = event.LossPercent

		// Dump route tables if verbose
		if nc.Verbose {
			nc.dumpRouteTables(ctx)
		}
		nc.mu.Unlock()

		// Wait for event duration
		select {
		case <-ctx.Done():
			// Clear loss before exiting
			nc.mu.Lock()
			script := fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent_parallel 0",
				nc.ScriptDir, nc.CurrentLatencyProfile)
			cmd := exec.CommandContext(context.Background(), "bash", "-c", script)
			cmd.Env = nc.buildScriptEnv()
			_ = cmd.Run()
			nc.CurrentLossPercent = 0
			nc.mu.Unlock()
			return
		case <-time.After(event.Duration):
		}

		// Clear loss after event
		if nc.Verbose {
			fmt.Printf("[PATTERN] Event #%d: Clearing loss at %v (after %v)\n",
				eventIndex+1, time.Now().Format("15:04:05.000"), event.Duration)
		}

		nc.mu.Lock()
		// IMPORTANT: Pass CURRENT_LATENCY_PROFILE explicitly
		script = fmt.Sprintf("source %s/lib.sh && CURRENT_LATENCY_PROFILE=%d && set_loss_percent_parallel 0",
			nc.ScriptDir, nc.CurrentLatencyProfile)
		cmd = exec.CommandContext(ctx, "bash", "-c", script)
		cmd.Env = nc.buildScriptEnv()
		if err := cmd.Run(); err != nil {
			if nc.Verbose {
				fmt.Fprintf(os.Stderr, "[PATTERN] ERROR clearing parallel loss: %v\n", err)
			}
		}
		nc.CurrentLossPercent = 0

		// Dump route tables if verbose
		if nc.Verbose {
			nc.dumpRouteTables(ctx)
		}
		nc.mu.Unlock()

		eventIndex++
	}
}

// buildScriptEnv returns the environment variables for script execution.
// Includes TEST_ID and SRT_NETWORK_DEBUG=1 when verbose mode is enabled.
func (nc *NetworkController) buildScriptEnv() []string {
	env := append(os.Environ(), fmt.Sprintf("TEST_ID=%s", nc.TestID))
	if nc.Verbose {
		env = append(env, "SRT_NETWORK_DEBUG=1")
	}
	return env
}

// runScript executes a script with the TEST_ID environment variable
func (nc *NetworkController) runScript(ctx context.Context, script string, args ...string) error {
	scriptPath := filepath.Join(nc.ScriptDir, script)
	cmd := exec.CommandContext(ctx, scriptPath, args...)
	cmd.Env = nc.buildScriptEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w\nOutput: %s", script, err, string(output))
	}

	return nil
}

// runScriptUnlocked is like runScript but assumes the mutex is already held
func (nc *NetworkController) runScriptUnlocked(ctx context.Context, script string, args ...string) error {
	scriptPath := filepath.Join(nc.ScriptDir, script)
	cmd := exec.CommandContext(ctx, scriptPath, args...)
	cmd.Env = nc.buildScriptEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w\nOutput: %s", script, err, string(output))
	}

	return nil
}

// runScriptWithOutput executes a script and returns its output
func (nc *NetworkController) runScriptWithOutput(ctx context.Context, script string, args ...string) ([]byte, error) {
	scriptPath := filepath.Join(nc.ScriptDir, script)
	cmd := exec.CommandContext(ctx, scriptPath, args...)
	cmd.Env = nc.buildScriptEnv()

	return cmd.CombinedOutput()
}
