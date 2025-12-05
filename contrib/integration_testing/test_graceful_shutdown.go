package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	// Test configuration
	serverAddr     = "127.0.0.1:6000"
	streamPath     = "/test-stream"
	shutdownDelay  = 5 * time.Second
	testDuration   = 10 * time.Second // How long to run before sending signal
	connectionWait = 2 * time.Second // Time to wait for connections to establish
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <test-name>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Available tests:\n")
		fmt.Fprintf(os.Stderr, "  graceful-shutdown-sigint - Test graceful shutdown on SIGINT\n")
		os.Exit(1)
	}

	testName := os.Args[1]

	switch testName {
	case "graceful-shutdown-sigint":
		testGracefulShutdownSIGINT()
	default:
		fmt.Fprintf(os.Stderr, "Unknown test: %s\n", testName)
		os.Exit(1)
	}
}

// testGracefulShutdownSIGINT tests graceful shutdown on SIGINT
// This is Test 1.1 from the testing plan
func testGracefulShutdownSIGINT() {
	fmt.Println("=== Test 1.1: Graceful Shutdown on SIGINT ===")
	fmt.Println()

	// Get the base directory (assuming we're in contrib/integration_testing)
	baseDir := getBaseDir()

	// Build paths to binaries
	serverBin := filepath.Join(baseDir, "contrib", "server", "server")
	clientGenBin := filepath.Join(baseDir, "contrib", "client-generator", "client-generator")
	clientBin := filepath.Join(baseDir, "contrib", "client", "client")

	// Check if binaries exist, if not, build them
	if err := ensureBinaries(baseDir, serverBin, clientGenBin, clientBin); err != nil {
		fmt.Fprintf(os.Stderr, "Error building binaries: %v\n", err)
		os.Exit(1)
	}

	// Create context for test orchestration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	fmt.Println("Starting server...")
	serverCmd := exec.CommandContext(ctx, serverBin, "-addr", serverAddr)
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr
	if err := serverCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if serverCmd.Process != nil {
			serverCmd.Process.Kill()
		}
	}()

	// Wait for server to start
	time.Sleep(500 * time.Millisecond)

	// Start client-generator (publisher)
	fmt.Println("Starting client-generator (publisher)...")
	publisherURL := fmt.Sprintf("srt://%s%s", serverAddr, streamPath)
	clientGenCmd := exec.CommandContext(ctx, clientGenBin, "-to", publisherURL, "-bitrate", "2000000")
	clientGenCmd.Stdout = os.Stdout
	clientGenCmd.Stderr = os.Stderr
	if err := clientGenCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting client-generator: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if clientGenCmd.Process != nil {
			clientGenCmd.Process.Kill()
		}
	}()

	// Wait for publisher to connect
	time.Sleep(500 * time.Millisecond)

	// Start client (subscriber)
	fmt.Println("Starting client (subscriber)...")
	// For subscribe, the stream ID should be "subscribe:/path" format
	// The client reads streamid from URL query parameter
	subscriberURL := fmt.Sprintf("srt://%s?streamid=subscribe:%s", serverAddr, streamPath)
	clientCmd := exec.CommandContext(ctx, clientBin, "-from", subscriberURL, "-to", "null")
	clientCmd.Stdout = os.Stdout
	clientCmd.Stderr = os.Stderr
	if err := clientCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting client: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if clientCmd.Process != nil {
			clientCmd.Process.Kill()
		}
	}()

	// Wait for connections to establish
	fmt.Printf("Waiting %v for connections to establish...\n", connectionWait)
	time.Sleep(connectionWait)

	// Verify processes are running
	if serverCmd.Process == nil || clientGenCmd.Process == nil || clientCmd.Process == nil {
		fmt.Fprintf(os.Stderr, "Error: One or more processes failed to start\n")
		os.Exit(1)
	}

	fmt.Printf("All processes started. Running for %v before sending SIGINT...\n", testDuration)
	time.Sleep(testDuration)

	// Send SIGINT to server
	fmt.Println("Sending SIGINT to server...")
	if err := serverCmd.Process.Signal(os.Interrupt); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending SIGINT: %v\n", err)
		os.Exit(1)
	}

	// Wait for server to shutdown gracefully
	fmt.Printf("Waiting up to %v for graceful shutdown...\n", shutdownDelay)
	shutdownComplete := make(chan struct{})
	var wg sync.WaitGroup

	// Monitor server process
	wg.Add(1)
	go func() {
		defer wg.Done()
		serverCmd.Wait()
		close(shutdownComplete)
	}()

	// Monitor client-generator (should exit when server closes connection)
	wg.Add(1)
	go func() {
		defer wg.Done()
		clientGenCmd.Wait()
	}()

	// Monitor client (should exit when server closes connection)
	wg.Add(1)
	go func() {
		defer wg.Done()
		clientCmd.Wait()
	}()

	// Wait for shutdown with timeout
	select {
	case <-shutdownComplete:
		fmt.Println("✓ Server shutdown completed")
	case <-time.After(shutdownDelay + 2*time.Second):
		fmt.Fprintf(os.Stderr, "✗ Server did not shutdown within %v\n", shutdownDelay+2*time.Second)
		os.Exit(1)
	}

	// Wait for all processes to exit
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		fmt.Println("✓ All processes exited")
	case <-time.After(5 * time.Second):
		fmt.Fprintf(os.Stderr, "✗ Some processes did not exit within 5 seconds\n")
		os.Exit(1)
	}

	// Check exit codes
	serverState := serverCmd.ProcessState
	if serverState != nil && !serverState.Success() {
		fmt.Fprintf(os.Stderr, "✗ Server exited with non-zero code\n")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("=== Test 1.1: PASSED ===")
	fmt.Println()
	fmt.Println("Verification:")
	fmt.Println("  ✓ Server received SIGINT")
	fmt.Println("  ✓ Server shutdown gracefully")
	fmt.Println("  ✓ All processes exited cleanly")
	fmt.Println("  ✓ No process leaks detected")
}

// getBaseDir returns the base directory of the gosrt project
func getBaseDir() string {
	_, filename, _, _ := runtime.Caller(0)
	// integration_testing/test_graceful_shutdown.go -> contrib/integration_testing -> contrib -> base
	dir := filepath.Dir(filename)
	return filepath.Join(dir, "..", "..")
}

// ensureBinaries ensures that all required binaries exist, building them if necessary
func ensureBinaries(baseDir string, serverBin, clientGenBin, clientBin string) error {
	binaries := []struct {
		path string
		pkg  string
	}{
		{serverBin, "./contrib/server"},
		{clientGenBin, "./contrib/client-generator"},
		{clientBin, "./contrib/client"},
	}

	for _, bin := range binaries {
		if _, err := os.Stat(bin.path); os.IsNotExist(err) {
			fmt.Printf("Building %s...\n", bin.path)
			cmd := exec.Command("go", "build", "-o", bin.path, bin.pkg)
			cmd.Dir = baseDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to build %s: %w", bin.path, err)
			}
		}
	}

	return nil
}

