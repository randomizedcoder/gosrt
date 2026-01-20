package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/contrib/common"
)

// ProcessManager manages the server and client-seeker processes.
type ProcessManager struct {
	config Config

	serverCmd *exec.Cmd
	seekerCmd *exec.Cmd

	serverPromUDS    string
	seekerPromUDS    string
	seekerControlUDS string

	mu       sync.Mutex
	stopped  bool
	stopOnce sync.Once
}

// NewProcessManager creates a new process manager.
func NewProcessManager(config Config) *ProcessManager {
	// Generate unique socket paths
	pid := os.Getpid()
	timestamp := time.Now().UnixNano()

	return &ProcessManager{
		config:           config,
		serverPromUDS:    fmt.Sprintf("/tmp/perf_server_%d_%d.sock", pid, timestamp),
		seekerPromUDS:    fmt.Sprintf("/tmp/perf_seeker_metrics_%d_%d.sock", pid, timestamp),
		seekerControlUDS: fmt.Sprintf("/tmp/perf_seeker_control_%d_%d.sock", pid, timestamp),
	}
}

// StartServer starts the SRT server process.
func (pm *ProcessManager) StartServer(ctx context.Context) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.stopped {
		return fmt.Errorf("process manager stopped")
	}

	// Build server command
	args := pm.buildServerArgs()

	pm.serverCmd = exec.CommandContext(ctx, pm.config.ServerBinary, args...)
	pm.serverCmd.Stdout = os.Stdout
	pm.serverCmd.Stderr = os.Stderr

	if err := pm.serverCmd.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	return nil
}

// buildServerArgs builds command-line arguments for the server.
// Uses common.BuildFlagArgs() to pass through all SRT configuration flags
// that were explicitly set by the user.
func (pm *ProcessManager) buildServerArgs() []string {
	// Start with server-specific args
	args := []string{
		"-addr", pm.config.ServerAddr,
		"-promuds", pm.serverPromUDS,
		"-name", "perf-server",
	}

	// Always add essential baseline flags (connection timeouts, etc.)
	// These are required for reliable operation
	args = append(args, baselineArgs()...)

	// Add all explicitly-set SRT flags (filtered for server use)
	// Excludes: seeker-specific flags and baseline flags we already added
	// User-provided flags with same name will override baseline (flag parser is last-wins)
	srtArgs := common.BuildFlagArgsFiltered(
		// Seeker/performance-specific flags
		"target",
		"control-socket",
		"metrics-socket",
		"watchdog-timeout",
		"heartbeat-interval",
		"addr", // We set this explicitly above
		"promuds",
		"name",
		// Baseline flags are already added - filter them from user args
		// (but if user passes them explicitly, they'll be in SRTFlags and override baseline)
	)
	args = append(args, srtArgs...)

	return args
}

// StartSeeker starts the client-seeker process.
func (pm *ProcessManager) StartSeeker(ctx context.Context, initialBitrate int64) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.stopped {
		return fmt.Errorf("process manager stopped")
	}

	// Build seeker command
	args := pm.buildSeekerArgs(initialBitrate)

	pm.seekerCmd = exec.CommandContext(ctx, pm.config.SeekerBinary, args...)
	pm.seekerCmd.Stdout = os.Stdout
	pm.seekerCmd.Stderr = os.Stderr

	if err := pm.seekerCmd.Start(); err != nil {
		return fmt.Errorf("failed to start seeker: %w", err)
	}

	return nil
}

// buildSeekerArgs builds command-line arguments for the seeker.
// Uses common.BuildFlagArgs() to pass through all SRT configuration flags
// that were explicitly set by the user.
func (pm *ProcessManager) buildSeekerArgs(initialBitrate int64) []string {
	// Start with seeker-specific args
	args := []string{
		"-target", fmt.Sprintf("srt://%s/perf-test", pm.config.ServerAddr),
		"-initial", fmt.Sprintf("%d", initialBitrate),
		"-control-socket", pm.seekerControlUDS,
		"-metrics-socket", pm.seekerPromUDS,
		"-watchdog-timeout", pm.config.Timing.WatchdogTimeout.String(),
	}

	// Always add essential baseline flags (connection timeouts, etc.)
	args = append(args, baselineArgs()...)

	// Add all explicitly-set SRT flags (filtered for seeker use)
	// User-provided flags will override baseline (added after baseline, flag parser is last-wins)
	srtArgs := common.BuildFlagArgsFiltered(
		// Server/performance-specific flags to exclude
		"target",
		"control-socket",
		"metrics-socket",
		"watchdog-timeout",
		"heartbeat-interval",
		"addr", // Server-specific
		"promuds",
		"name",
		"initial", // We set this explicitly above
	)
	args = append(args, srtArgs...)

	return args
}

// baselineArgs returns essential flags that are always needed for reliable high-performance operation.
// These mirror the ConfigFullELLockFree from integration_testing/config.go and are always added.
// User flags will override these where applicable (flag parsing is last-wins).
func baselineArgs() []string {
	return []string{
		// Connection timeouts - required for handshake to complete
		"-conntimeo", "5000",
		"-peeridletimeo", "30000",
		// Handshake timeout - must be longer than default 1.5s for high-throughput setup
		"-handshaketimeout", "10s",

		// Latency settings - 3s default for high throughput
		"-latency", "3000",
		"-rcvlatency", "3000",
		"-peerlatency", "3000",

		// Drop too-late packets (required for live mode)
		"-tlpktdrop",

		// Packet reordering - btree is essential for io_uring
		"-packetreorderalgorithm", "btree",
		"-btreedegree", "32",

		// io_uring - enable by default for performance testing
		"-iouringenabled",
		"-iouringrecvenabled",

		// NAK btree - essential for io_uring reorder handling
		"-usenakbtree",
		"-fastnakenabled",
		"-fastnakrecentenabled",
		"-honornakorder",
		"-nakrecentpercent", "0.10",

		// Receiver lock-free path (Phase 3-4)
		"-usepacketring",
		"-useeventloop",
		"-eventlooprateinterval", "1s",
		"-backoffcoldstartpkts", "1000",
		"-backoffminsleep", "10µs",
		"-backoffmaxsleep", "1ms",

		// Sender lock-free path (Phase 5+)
		// UseSendControlRing requires UseSendRing, so always enable both
		"-usesendbtree",
		"-sendbtreesize", "32",
		"-usesendring",
		"-sendringsize", "1024",
		"-sendringshards", "1",
		"-usesendcontrolring",
		"-sendcontrolringsize", "256",
		"-sendcontrolringshards", "2",
		"-usesendeventloop",
		"-sendeventloopbackoffminsleep", "100µs",
		"-sendeventloopbackoffmaxsleep", "1ms",
		"-sendtsbpdsleepfactor", "0.90",

		// Receiver control ring
		"-userecvcontrolring",
		"-recvcontrolringsize", "128",
		"-recvcontrolringshards", "1",
	}
}

// defaultHighThroughputArgs returns default SRT configuration for high-throughput testing.
// These match ConfigFullELLockFree + WithUltraHighThroughput from isolation tests.
// Used when no SRT flags are explicitly set.
func defaultHighThroughputArgs() []string {
	return []string{
		// Note: baseline args (conntimeo, peeridletimeo, tlpktdrop) are added separately

		// Buffer configuration (WithUltraHighThroughput)
		"-fc", "102400",
		"-rcvbuf", "67108864",
		"-sndbuf", "67108864",
		"-latency", "5000",
		"-rcvlatency", "5000",
		"-peerlatency", "5000",

		// io_uring configuration (WithMultipleRecvRings)
		"-iouringenabled",
		"-iouringrecvenabled",
		"-iouringrecvringcount", "2",
		"-iouringrecvringsize", "16384",
		"-iouringrecvbatchsize", "1024",

		// Lock-free packet ring (WithPacketRing + WithUltraHighThroughput)
		"-usepacketring",
		"-packetringsize", "16384",
		"-packetringshards", "8",
		"-packetringmaxretries", "100",
		"-packetringbackoffduration", "50µs",

		// Receiver EventLoop (WithEventLoop)
		"-useeventloop",
		"-eventlooprateinterval", "1s",
		"-backoffcoldstartpkts", "1000",
		"-backoffminsleep", "10µs",
		"-backoffmaxsleep", "1ms",

		// Completely lock-free receiver (WithRecvControlRing)
		"-userecvcontrolring",
		"-recvcontrolringsize", "128",
		"-recvcontrolringshards", "1",

		// Sender btree (WithSendBtree)
		"-usesendbtree",
		"-sendbtreesize", "32",

		// Sender ring (WithSendRing + WithUltraHighThroughput)
		"-usesendring",
		"-sendringsize", "8192",
		"-sendringshards", "4",

		// Sender control ring + event loop (WithSendControlRing + WithSendEventLoop)
		"-usesendcontrolring",
		"-sendcontrolringsize", "256",
		"-sendcontrolringshards", "2",
		"-usesendeventloop",
		"-sendeventloopbackoffminsleep", "100µs",
		"-sendeventloopbackoffmaxsleep", "1ms",
		"-sendtsbpdsleepfactor", "0.90",

		// NAK and packet reordering (HighPerfSRTConfig base)
		"-packetreorderalgorithm", "btree",
		"-btreedegree", "32",
		"-usenakbtree",
		"-fastnakenabled",
		"-fastnakrecentenabled",
		"-honornakorder",
		"-nakrecentpercent", "0.10",
	}
}

// WaitReady waits for all processes to be ready.
func (pm *ProcessManager) WaitReady(ctx context.Context) error {
	timeout := 30 * time.Second
	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		criteria := pm.checkReadiness(ctx)
		if criteria.AllReady() {
			return nil
		}

		time.Sleep(pollInterval)
	}

	// Final check with detailed error
	criteria := pm.checkReadiness(ctx)
	if !criteria.AllReady() {
		return fmt.Errorf("readiness timeout after %v: %s", timeout, criteria.FirstFailure())
	}

	return nil
}

// GetPIDs returns the server and seeker process IDs for monitoring.
func (pm *ProcessManager) GetPIDs() (serverPID, seekerPID int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.serverCmd != nil && pm.serverCmd.Process != nil {
		serverPID = pm.serverCmd.Process.Pid
	}
	if pm.seekerCmd != nil && pm.seekerCmd.Process != nil {
		seekerPID = pm.seekerCmd.Process.Pid
	}
	return
}

// checkReadiness checks all readiness criteria.
func (pm *ProcessManager) checkReadiness(ctx context.Context) ReadinessCriteria {
	var criteria ReadinessCriteria

	pm.mu.Lock()
	criteria.ServerRunning = pm.serverCmd != nil && pm.serverCmd.Process != nil
	criteria.SeekerRunning = pm.seekerCmd != nil && pm.seekerCmd.Process != nil
	pm.mu.Unlock()

	// Check server metrics socket
	if criteria.ServerRunning {
		criteria.ServerMetricsReady = pm.probeSocket(ctx, pm.serverPromUDS)
	}

	// Check seeker metrics socket
	if criteria.SeekerRunning {
		criteria.SeekerMetricsReady = pm.probeSocket(ctx, pm.seekerPromUDS)
	}

	// Check seeker control socket
	if criteria.SeekerRunning {
		criteria.SeekerControlReady = pm.probeControlSocket(ctx, pm.seekerControlUDS)
	}

	// Check SRT connection
	if criteria.SeekerControlReady {
		criteria.ConnectionEstablished = pm.probeConnection(ctx)
	}

	return criteria
}

// probeSocket checks if a Unix socket is responding to HTTP requests.
func (pm *ProcessManager) probeSocket(ctx context.Context, socketPath string) bool {
	// Check if socket file exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return false
	}

	// Try to connect and get metrics
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
		Timeout: 1 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/metrics", nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// probeControlSocket checks if the control socket is responding.
func (pm *ProcessManager) probeControlSocket(ctx context.Context, socketPath string) bool {
	// Check if socket file exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return false
	}

	// Try to connect
	conn, err := net.DialTimeout("unix", socketPath, 1*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()

	// Send get_status command
	conn.SetDeadline(time.Now().Add(1 * time.Second))
	_, err = conn.Write([]byte(`{"command":"get_status"}` + "\n"))
	if err != nil {
		return false
	}

	// Read response
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return false
	}

	// Check for valid response
	return n > 0 && strings.Contains(string(buf[:n]), "status")
}

// probeConnection checks if the SRT connection is established.
func (pm *ProcessManager) probeConnection(ctx context.Context) bool {
	// Connect to control socket and check connection_alive
	conn, err := net.DialTimeout("unix", pm.seekerControlUDS, 1*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(1 * time.Second))
	_, err = conn.Write([]byte(`{"command":"get_status"}` + "\n"))
	if err != nil {
		return false
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return false
	}

	// Check for connection_alive: true
	response := string(buf[:n])
	return strings.Contains(response, `"connection_alive":true`)
}

// Stop stops all processes and cleans up.
func (pm *ProcessManager) Stop() {
	pm.stopOnce.Do(func() {
		pm.mu.Lock()
		pm.stopped = true
		pm.mu.Unlock()

		// Stop seeker first (it's the client)
		if pm.seekerCmd != nil && pm.seekerCmd.Process != nil {
			pm.seekerCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() {
				done <- pm.seekerCmd.Wait()
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				pm.seekerCmd.Process.Kill()
			}
		}

		// Stop server
		if pm.serverCmd != nil && pm.serverCmd.Process != nil {
			pm.serverCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() {
				done <- pm.serverCmd.Wait()
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				pm.serverCmd.Process.Kill()
			}
		}

		// Clean up socket files
		os.Remove(pm.serverPromUDS)
		os.Remove(pm.seekerPromUDS)
		os.Remove(pm.seekerControlUDS)
	})
}

// ServerMetricsPath returns the path to the server's Prometheus socket.
func (pm *ProcessManager) ServerMetricsPath() string {
	return pm.serverPromUDS
}

// SeekerMetricsPath returns the path to the seeker's Prometheus socket.
func (pm *ProcessManager) SeekerMetricsPath() string {
	return pm.seekerPromUDS
}

// SeekerControlPath returns the path to the seeker's control socket.
func (pm *ProcessManager) SeekerControlPath() string {
	return pm.seekerControlUDS
}

// FormatBitrateShort formats a bitrate for command-line use (e.g., "200M").
func FormatBitrateShort(bps int64) string {
	switch {
	case bps >= 1_000_000_000 && bps%1_000_000_000 == 0:
		return fmt.Sprintf("%dG", bps/1_000_000_000)
	case bps >= 1_000_000 && bps%1_000_000 == 0:
		return fmt.Sprintf("%dM", bps/1_000_000)
	case bps >= 1_000 && bps%1_000 == 0:
		return fmt.Sprintf("%dK", bps/1_000)
	default:
		return fmt.Sprintf("%d", bps)
	}
}

// ResolveBinaryPath resolves a binary path relative to the workspace.
func ResolveBinaryPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	// Try relative to current directory
	if _, err := os.Stat(path); err == nil {
		abs, _ := filepath.Abs(path)
		return abs
	}

	// Try relative to workspace root
	// Assume we're in contrib/performance
	workspaceRoot := filepath.Join("..", "..")
	fullPath := filepath.Join(workspaceRoot, path)
	if _, err := os.Stat(fullPath); err == nil {
		abs, _ := filepath.Abs(fullPath)
		return abs
	}

	return path
}
