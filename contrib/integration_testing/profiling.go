package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProfileType represents a type of Go profile
type ProfileType string

const (
	ProfileCPU          ProfileType = "cpu"
	ProfileMem          ProfileType = "mem"
	ProfileMutex        ProfileType = "mutex"
	ProfileBlock        ProfileType = "block"
	ProfileHeap         ProfileType = "heap"
	ProfileAllocs       ProfileType = "allocs"
	ProfileTrace        ProfileType = "trace"
	ProfileThread       ProfileType = "thread"       // OS thread creation (threadcreate)
	ProfileGoroutine    ProfileType = "goroutine"    // All current goroutines
)

// AllProfiles returns all profile types (excluding trace due to size)
func AllProfiles() []ProfileType {
	return []ProfileType{ProfileCPU, ProfileMutex, ProfileBlock, ProfileHeap, ProfileAllocs, ProfileThread, ProfileGoroutine}
}

// ProfileConfig holds configuration for a profiling run
type ProfileConfig struct {
	TestName  string
	Profiles  []ProfileType
	OutputDir string
	Duration  time.Duration // Duration for each profile iteration
}

// NewProfileConfig creates a ProfileConfig from environment variables
func NewProfileConfig(testName string) (*ProfileConfig, error) {
	envValue := os.Getenv("PROFILES")
	profiles := ParseProfiles(envValue)
	if len(profiles) == 0 {
		if envValue != "" {
			fmt.Printf("Warning: PROFILES=%q did not match any known profile types\n", envValue)
			fmt.Println("  Valid types: cpu, mem, mutex, block, heap, allocs, trace, thread, goroutine, all")
		}
		return nil, nil // Profiling not enabled
	}

	outputDir, err := CreateProfileDir(testName)
	if err != nil {
		return nil, err
	}

	return &ProfileConfig{
		TestName:  testName,
		Profiles:  profiles,
		OutputDir: outputDir,
		Duration:  60 * time.Second, // Default duration
	}, nil
}

// ParseProfiles parses the PROFILES environment variable
// Accepts: "all", "cpu", "cpu,mutex", "cpu,mutex,heap", etc.
func ParseProfiles(env string) []ProfileType {
	if env == "" {
		return nil
	}
	if env == "all" {
		return AllProfiles()
	}

	var profiles []ProfileType
	for _, p := range strings.Split(env, ",") {
		switch strings.TrimSpace(strings.ToLower(p)) {
		case "cpu":
			profiles = append(profiles, ProfileCPU)
		case "mem":
			profiles = append(profiles, ProfileMem)
		case "mutex":
			profiles = append(profiles, ProfileMutex)
		case "block":
			profiles = append(profiles, ProfileBlock)
		case "heap":
			profiles = append(profiles, ProfileHeap)
		case "allocs":
			profiles = append(profiles, ProfileAllocs)
		case "trace":
			profiles = append(profiles, ProfileTrace)
		case "thread", "threadcreate":
			profiles = append(profiles, ProfileThread)
		case "goroutine":
			profiles = append(profiles, ProfileGoroutine)
		}
	}
	return profiles
}

// CreateProfileDir creates a directory for profile output
func CreateProfileDir(testName string) (string, error) {
	timestamp := time.Now().Format("20060102_150405")
	safeName := strings.ReplaceAll(testName, "/", "_")
	safeName = strings.ReplaceAll(safeName, " ", "_")
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("profile_%s_%s", safeName, timestamp))

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create profile directory: %w", err)
	}
	return dir, nil
}

// CreateComponentDir creates a subdirectory for a component's profiles
func (c *ProfileConfig) CreateComponentDir(component string) (string, error) {
	dir := filepath.Join(c.OutputDir, component)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create component profile directory: %w", err)
	}
	return dir, nil
}

// ProfileFilePath returns the path for a profile file
func ProfileFilePath(dir, component string, profileType ProfileType) string {
	return filepath.Join(dir, fmt.Sprintf("%s_%s.pprof", component, profileType))
}

// ProfilingEnabled returns true if PROFILES env var is set
func ProfilingEnabled() bool {
	return os.Getenv("PROFILES") != ""
}

// GetProfileDuration returns the recommended duration for a profile type
func GetProfileDuration(p ProfileType) time.Duration {
	switch p {
	case ProfileCPU, ProfileMutex, ProfileBlock:
		return 120 * time.Second
	case ProfileHeap, ProfileAllocs, ProfileMem:
		return 60 * time.Second
	case ProfileTrace:
		return 30 * time.Second
	case ProfileThread, ProfileGoroutine:
		// Thread/goroutine profiles are point-in-time snapshots, not duration-based
		// but we still run for a duration to let the system stabilize
		return 120 * time.Second
	default:
		return 60 * time.Second
	}
}

// GetProfileArgs returns CLI arguments to enable profiling for a component
// Returns: ["-profile", "cpu", "-profilepath", "/tmp/profile_test/server"]
func (c *ProfileConfig) GetProfileArgs(component string, profileType ProfileType) ([]string, error) {
	if c == nil || len(c.Profiles) == 0 {
		return nil, nil
	}

	componentDir, err := c.CreateComponentDir(component)
	if err != nil {
		return nil, err
	}

	return []string{
		"-profile", string(profileType),
		"-profilepath", componentDir,
	}, nil
}

// GetAllProfileArgs returns CLI arguments for all configured profile types
// This is useful when you want to run multiple profile types sequentially
func (c *ProfileConfig) GetAllProfileArgs(component string) ([][]string, error) {
	if c == nil || len(c.Profiles) == 0 {
		return nil, nil
	}

	var allArgs [][]string
	for _, p := range c.Profiles {
		args, err := c.GetProfileArgs(component, p)
		if err != nil {
			return nil, err
		}
		if args != nil {
			allArgs = append(allArgs, args)
		}
	}
	return allArgs, nil
}

// GetFirstProfileArgs returns CLI arguments for the first configured profile type
// This is useful when you only want to collect one profile per test run
func (c *ProfileConfig) GetFirstProfileArgs(component string) ([]string, error) {
	if c == nil || len(c.Profiles) == 0 {
		return nil, nil
	}
	return c.GetProfileArgs(component, c.Profiles[0])
}

// PrintProfilingInfo prints information about the profiling configuration
func (c *ProfileConfig) PrintProfilingInfo() {
	if c == nil {
		return
	}

	fmt.Printf("\n╔═══════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  PROFILING ENABLED                                                    ║\n")
	fmt.Printf("╠═══════════════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Test:     %-60s ║\n", c.TestName)
	fmt.Printf("║  Profiles: %-60s ║\n", formatProfileTypes(c.Profiles))
	fmt.Printf("║  Output:   %-60s ║\n", truncatePath(c.OutputDir, 60))
	fmt.Printf("╚═══════════════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("\nProfile files will be written to: %s\n\n", c.OutputDir)
}

// formatProfileTypes formats profile types for display
func formatProfileTypes(profiles []ProfileType) string {
	if len(profiles) == 0 {
		return "(none)"
	}
	strs := make([]string, len(profiles))
	for i, p := range profiles {
		strs[i] = string(p)
	}
	return strings.Join(strs, ", ")
}

// truncatePath truncates a path for display
func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen+3:]
}

// ListProfileFiles returns all profile files in the output directory
func (c *ProfileConfig) ListProfileFiles() ([]string, error) {
	if c == nil {
		return nil, nil
	}

	var files []string
	err := filepath.Walk(c.OutputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".pprof") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// PrintProfileFileLocations prints a summary of all generated profile files
func (c *ProfileConfig) PrintProfileFileLocations() {
	if c == nil {
		return
	}

	files, err := c.ListProfileFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not list profile files: %v\n", err)
		return
	}

	fmt.Printf("\n╔═══════════════════════════════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  PROFILE FILES GENERATED                                                                      ║\n")
	fmt.Printf("╠═══════════════════════════════════════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Output Directory:                                                                            ║\n")
	fmt.Printf("║    %s\n", c.OutputDir)
	fmt.Printf("║                                                                                               ║\n")

	if len(files) == 0 {
		fmt.Printf("║  (No .pprof files found)                                                                      ║\n")
	} else {
		fmt.Printf("║  Files (%d total):                                                                             ║\n", len(files))
		for _, f := range files {
			// Show relative path from output dir for cleaner display
			relPath, err := filepath.Rel(c.OutputDir, f)
			if err != nil {
				relPath = f
			}
			fmt.Printf("║    %s\n", relPath)
		}
	}

	fmt.Printf("╠═══════════════════════════════════════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  To analyze profiles:                                                                         ║\n")
	fmt.Printf("║    go tool pprof -http=:8080 <file.pprof>                                                     ║\n")
	fmt.Printf("║    go tool pprof -top <file.pprof>                                                            ║\n")
	fmt.Printf("╚═══════════════════════════════════════════════════════════════════════════════════════════════╝\n\n")
}

// ProfileComponent represents a profiled component with its profile file paths
type ProfileComponent struct {
	Name         string                 // "server", "cg", "client"
	Pipeline     string                 // "baseline", "highperf" (for parallel tests)
	ProfileDir   string                 // Directory containing profile files
	ProfileFiles map[ProfileType]string // Map of profile type to file path
}

// GetProfileComponents returns information about all profiled components
func (c *ProfileConfig) GetProfileComponents() ([]ProfileComponent, error) {
	if c == nil {
		return nil, nil
	}

	entries, err := os.ReadDir(c.OutputDir)
	if err != nil {
		return nil, err
	}

	var components []ProfileComponent
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		compDir := filepath.Join(c.OutputDir, name)

		comp := ProfileComponent{
			Name:         name,
			ProfileDir:   compDir,
			ProfileFiles: make(map[ProfileType]string),
		}

		// Parse pipeline from name if present (e.g., "baseline_server" -> pipeline="baseline", name="server")
		if strings.Contains(name, "_") {
			parts := strings.SplitN(name, "_", 2)
			if parts[0] == "baseline" || parts[0] == "highperf" || parts[0] == "control" || parts[0] == "test" {
				comp.Pipeline = parts[0]
				comp.Name = parts[1]
			}
		}

		// Find profile files
		files, err := os.ReadDir(compDir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".pprof") {
				continue
			}

			// Parse profile type from filename
			basename := strings.TrimSuffix(f.Name(), ".pprof")
			for _, pt := range AllProfiles() {
				if strings.HasSuffix(basename, string(pt)) {
					comp.ProfileFiles[pt] = filepath.Join(compDir, f.Name())
					break
				}
			}
		}

		if len(comp.ProfileFiles) > 0 {
			components = append(components, comp)
		}
	}

	return components, nil
}
