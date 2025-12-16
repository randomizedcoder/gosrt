package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseProfiles(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []ProfileType
	}{
		{
			name:     "empty",
			input:    "",
			expected: nil,
		},
		{
			name:     "all",
			input:    "all",
			expected: AllProfiles(),
		},
		{
			name:     "single cpu",
			input:    "cpu",
			expected: []ProfileType{ProfileCPU},
		},
		{
			name:     "multiple",
			input:    "cpu,mutex,heap",
			expected: []ProfileType{ProfileCPU, ProfileMutex, ProfileHeap},
		},
		{
			name:     "with spaces",
			input:    "cpu, mutex, heap",
			expected: []ProfileType{ProfileCPU, ProfileMutex, ProfileHeap},
		},
		{
			name:     "case insensitive",
			input:    "CPU,MUTEX",
			expected: []ProfileType{ProfileCPU, ProfileMutex},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseProfiles(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("ParseProfiles(%q) = %v, want %v", tt.input, result, tt.expected)
				return
			}
			for i, p := range result {
				if p != tt.expected[i] {
					t.Errorf("ParseProfiles(%q)[%d] = %v, want %v", tt.input, i, p, tt.expected[i])
				}
			}
		})
	}
}

func TestCreateProfileDir(t *testing.T) {
	dir, err := CreateProfileDir("Test-Name/With-Slash")
	if err != nil {
		t.Fatalf("CreateProfileDir failed: %v", err)
	}
	defer os.RemoveAll(dir)

	// Verify directory was created
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("Expected directory, got file")
	}

	// Verify name doesn't contain slash
	if strings.Contains(filepath.Base(dir), "/") {
		t.Errorf("Directory name should not contain slash: %s", dir)
	}
}

func TestProfileConfig_GetProfileArgs(t *testing.T) {
	// Create a temporary config
	config := &ProfileConfig{
		TestName:  "TestConfig",
		Profiles:  []ProfileType{ProfileCPU, ProfileMutex},
		OutputDir: t.TempDir(),
		Duration:  60 * time.Second,
	}

	args, err := config.GetProfileArgs("server", ProfileCPU)
	if err != nil {
		t.Fatalf("GetProfileArgs failed: %v", err)
	}

	if len(args) != 4 {
		t.Errorf("Expected 4 args, got %d: %v", len(args), args)
	}

	if args[0] != "-profile" {
		t.Errorf("Expected '-profile', got %s", args[0])
	}
	if args[1] != "cpu" {
		t.Errorf("Expected 'cpu', got %s", args[1])
	}
	if args[2] != "-profilepath" {
		t.Errorf("Expected '-profilepath', got %s", args[2])
	}

	// Verify component directory was created
	expectedDir := filepath.Join(config.OutputDir, "server")
	if args[3] != expectedDir {
		t.Errorf("Expected '%s', got %s", expectedDir, args[3])
	}

	info, err := os.Stat(expectedDir)
	if err != nil {
		t.Fatalf("Component directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("Expected directory, got file")
	}
}

func TestGetProfileDuration(t *testing.T) {
	tests := []struct {
		profile  ProfileType
		expected time.Duration
	}{
		{ProfileCPU, 120 * time.Second},
		{ProfileMutex, 120 * time.Second},
		{ProfileBlock, 120 * time.Second},
		{ProfileHeap, 60 * time.Second},
		{ProfileAllocs, 60 * time.Second},
		{ProfileTrace, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(string(tt.profile), func(t *testing.T) {
			result := GetProfileDuration(tt.profile)
			if result != tt.expected {
				t.Errorf("GetProfileDuration(%v) = %v, want %v", tt.profile, result, tt.expected)
			}
		})
	}
}

func TestProfilingEnabled(t *testing.T) {
	// Save original value
	orig := os.Getenv("PROFILES")
	defer os.Setenv("PROFILES", orig)

	// Test disabled
	os.Unsetenv("PROFILES")
	if ProfilingEnabled() {
		t.Error("ProfilingEnabled() should be false when PROFILES is unset")
	}

	// Test enabled
	os.Setenv("PROFILES", "cpu")
	if !ProfilingEnabled() {
		t.Error("ProfilingEnabled() should be true when PROFILES is set")
	}
}

func TestNewProfileConfig(t *testing.T) {
	// Save original value
	orig := os.Getenv("PROFILES")
	defer os.Setenv("PROFILES", orig)

	// Test disabled
	os.Unsetenv("PROFILES")
	config, err := NewProfileConfig("Test")
	if err != nil {
		t.Fatalf("NewProfileConfig failed: %v", err)
	}
	if config != nil {
		t.Error("Expected nil config when PROFILES is unset")
	}

	// Test enabled
	os.Setenv("PROFILES", "cpu,mutex")
	config, err = NewProfileConfig("Test")
	if err != nil {
		t.Fatalf("NewProfileConfig failed: %v", err)
	}
	if config == nil {
		t.Fatal("Expected non-nil config when PROFILES is set")
	}
	defer os.RemoveAll(config.OutputDir)

	if len(config.Profiles) != 2 {
		t.Errorf("Expected 2 profiles, got %d", len(config.Profiles))
	}
	if config.Profiles[0] != ProfileCPU {
		t.Errorf("Expected CPU profile, got %v", config.Profiles[0])
	}
	if config.Profiles[1] != ProfileMutex {
		t.Errorf("Expected Mutex profile, got %v", config.Profiles[1])
	}
}

func TestProfileFilePath(t *testing.T) {
	path := ProfileFilePath("/tmp/profiles", "server", ProfileCPU)
	expected := "/tmp/profiles/server_cpu.pprof"
	if path != expected {
		t.Errorf("ProfileFilePath() = %s, want %s", path, expected)
	}
}
