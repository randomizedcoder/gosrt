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
}

// NetworkControllerConfig holds configuration for creating a NetworkController
type NetworkControllerConfig struct {
	// TestID is the unique identifier for this test run
	// If empty, the current process PID will be used
	TestID string

	// ScriptDir is the directory containing the network scripts
	// If empty, it will be auto-detected relative to the current executable
	ScriptDir string
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
	if nc.isSetup {
		if err := nc.runScriptUnlocked(ctx, "set_loss.sh", "0"); err != nil {
			return fmt.Errorf("failed to clear loss: %w", err)
		}
		nc.CurrentLossPercent = 0
	}

	return nil
}

// runPattern executes a loss pattern in a loop
func (nc *NetworkController) runPattern(ctx context.Context, pattern LossPattern) {
	defer nc.patternWg.Done()

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
			return
		}

		// Wait for next event
		select {
		case <-ctx.Done():
			return
		case <-time.After(waitDuration):
		}

		// Apply the loss
		nc.mu.Lock()
		if nc.isSetup {
			_ = nc.runScriptUnlocked(ctx, "set_loss.sh", strconv.Itoa(nextEvent.LossPercent))
		}
		nc.mu.Unlock()

		// Wait for event duration
		select {
		case <-ctx.Done():
			// Clear loss before exiting
			nc.mu.Lock()
			if nc.isSetup {
				_ = nc.runScriptUnlocked(ctx, "set_loss.sh", "0")
			}
			nc.mu.Unlock()
			return
		case <-time.After(nextEvent.Duration):
		}

		// Clear the loss
		nc.mu.Lock()
		if nc.isSetup {
			_ = nc.runScriptUnlocked(ctx, "set_loss.sh", "0")
		}
		nc.mu.Unlock()
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

// runScript executes a script with the TEST_ID environment variable
func (nc *NetworkController) runScript(ctx context.Context, script string, args ...string) error {
	scriptPath := filepath.Join(nc.ScriptDir, script)
	cmd := exec.CommandContext(ctx, scriptPath, args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("TEST_ID=%s", nc.TestID))

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
	cmd.Env = append(os.Environ(), fmt.Sprintf("TEST_ID=%s", nc.TestID))

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
	cmd.Env = append(os.Environ(), fmt.Sprintf("TEST_ID=%s", nc.TestID))

	return cmd.CombinedOutput()
}
