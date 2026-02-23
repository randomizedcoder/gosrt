package main

import (
	"strings"
	"testing"
)

func TestParseTopOutput(t *testing.T) {
	// Sample pprof -top output
	sampleOutput := `File: test
Type: cpu
Time: Dec 16, 2025 at 2:30pm (PST)
Duration: 30s, Total samples = 15.2s (50.67%)
Showing nodes accounting for 14.8s, 97.37% of 15.2s total
Dropped 50 nodes (cum <= 0.08s)
      flat  flat%   sum%        cum   cum%
     3.50s 23.03% 23.03%      3.50s 23.03%  runtime.chanrecv
     2.80s 18.42% 41.45%      2.80s 18.42%  syscall.write
     1.50s  9.87% 51.32%      1.50s  9.87%  runtime.mallocgc
     1.20s  7.89% 59.21%      1.20s  7.89%  crypto/aes.gcmAesEnc
     0.90s  5.92% 65.13%      0.90s  5.92%  runtime.memmove
`

	funcs := parseTopOutput(sampleOutput, ProfileCPU)

	if len(funcs) != 5 {
		t.Errorf("Expected 5 functions, got %d", len(funcs))
	}

	// Check first function
	if funcs[0].Name != "runtime.chanrecv" {
		t.Errorf("Expected 'runtime.chanrecv', got '%s'", funcs[0].Name)
	}
	if funcs[0].Flat != 23.03 {
		t.Errorf("Expected flat 23.03, got %f", funcs[0].Flat)
	}

	// Check second function
	if funcs[1].Name != "syscall.write" {
		t.Errorf("Expected 'syscall.write', got '%s'", funcs[1].Name)
	}
	if funcs[1].Flat != 18.42 {
		t.Errorf("Expected flat 18.42, got %f", funcs[1].Flat)
	}
}

func TestParsePercentage(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"23.03%", 23.03},
		{"0%", 0},
		{"100%", 100},
		{"5.5%", 5.5},
		{"invalid", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parsePercentage(tt.input)
			if result != tt.expected {
				t.Errorf("parsePercentage(%q) = %f, want %f", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCompareProfiles(t *testing.T) {
	baseline := &ProfileAnalysis{
		Component:   "server",
		ProfileType: ProfileCPU,
		TopFuncs: []FuncStat{
			{Name: "runtime.chanrecv", Flat: 25.0},
			{Name: "syscall.write", Flat: 18.0},
			{Name: "runtime.mallocgc", Flat: 10.0},
		},
	}

	highperf := &ProfileAnalysis{
		Component:   "server",
		ProfileType: ProfileCPU,
		TopFuncs: []FuncStat{
			{Name: "runtime.chanrecv", Flat: 5.0},   // Improved
			{Name: "io_uring.Submit", Flat: 12.0},   // New
			{Name: "runtime.mallocgc", Flat: 8.0},   // Slightly improved
		},
	}

	result := CompareProfiles(baseline, highperf)

	if result.Component != "server" {
		t.Errorf("Expected component 'server', got '%s'", result.Component)
	}

	if result.ProfileType != ProfileCPU {
		t.Errorf("Expected ProfileCPU, got '%s'", result.ProfileType)
	}

	// Find the chanrecv comparison
	var chanrecvComp *FuncComparison
	for i := range result.FuncComparisons {
		if result.FuncComparisons[i].FuncName == "runtime.chanrecv" {
			chanrecvComp = &result.FuncComparisons[i]
			break
		}
	}

	if chanrecvComp == nil {
		t.Fatal("Expected to find runtime.chanrecv comparison")
	}

	if chanrecvComp.BaselineValue != 25.0 {
		t.Errorf("Expected baseline 25.0, got %f", chanrecvComp.BaselineValue)
	}
	if chanrecvComp.HighPerfValue != 5.0 {
		t.Errorf("Expected highperf 5.0, got %f", chanrecvComp.HighPerfValue)
	}
	if chanrecvComp.Delta != -20.0 {
		t.Errorf("Expected delta -20.0, got %f", chanrecvComp.Delta)
	}

	// Find the new io_uring function
	var ioUringComp *FuncComparison
	for i := range result.FuncComparisons {
		if result.FuncComparisons[i].FuncName == "io_uring.Submit" {
			ioUringComp = &result.FuncComparisons[i]
			break
		}
	}

	if ioUringComp == nil {
		t.Fatal("Expected to find io_uring.Submit comparison")
	}

	if !ioUringComp.IsNew {
		t.Error("Expected io_uring.Submit to be marked as new")
	}

	// Find the removed syscall.write function
	var syscallComp *FuncComparison
	for i := range result.FuncComparisons {
		if result.FuncComparisons[i].FuncName == "syscall.write" {
			syscallComp = &result.FuncComparisons[i]
			break
		}
	}

	if syscallComp == nil {
		t.Fatal("Expected to find syscall.write comparison")
	}

	if !syscallComp.IsRemoved {
		t.Error("Expected syscall.write to be marked as removed")
	}
}

func TestFormatComparison(t *testing.T) {
	result := &ComparisonResult{
		Component:   "server",
		ProfileType: ProfileCPU,
		FuncComparisons: []FuncComparison{
			{FuncName: "runtime.chanrecv", BaselineValue: 25.0, HighPerfValue: 5.0, Delta: -20.0, DeltaPercent: -80.0},
			{FuncName: "io_uring.Submit", HighPerfValue: 12.0, Delta: 12.0, IsNew: true},
		},
		Summary:         "2 improvements, 0 regressions\n",
		Recommendations: []string{"Test recommendation"},
	}

	output := result.FormatComparison()

	// Check that output contains expected elements
	if !strings.Contains(output, "SERVER CPU COMPARISON") {
		t.Error("Expected output to contain 'SERVER CPU COMPARISON'")
	}
	if !strings.Contains(output, "runtime.chanrecv") {
		t.Error("Expected output to contain 'runtime.chanrecv'")
	}
	if !strings.Contains(output, "io_uring.Submit") {
		t.Error("Expected output to contain 'io_uring.Submit'")
	}
	if !strings.Contains(output, "(new)") {
		t.Error("Expected output to contain '(new)'")
	}
	if !strings.Contains(output, "RECOMMENDATIONS") {
		t.Error("Expected output to contain 'RECOMMENDATIONS'")
	}
}

func TestGetTopArgs(t *testing.T) {
	tests := []struct {
		profileType ProfileType
		expected    []string
	}{
		{ProfileCPU, []string{"-top", "-nodecount=10"}},
		{ProfileHeap, []string{"-top", "-nodecount=10", "-inuse_space"}},
		{ProfileAllocs, []string{"-top", "-nodecount=10", "-alloc_objects"}},
		{ProfileMutex, []string{"-top", "-nodecount=10", "-contentions"}},
		{ProfileBlock, []string{"-top", "-nodecount=10", "-contentions"}},
	}

	for _, tt := range tests {
		t.Run(string(tt.profileType), func(t *testing.T) {
			result := getTopArgs(tt.profileType)
			if len(result) != len(tt.expected) {
				t.Errorf("getTopArgs(%v) = %v, want %v", tt.profileType, result, tt.expected)
				return
			}
			for i, arg := range result {
				if arg != tt.expected[i] {
					t.Errorf("getTopArgs(%v)[%d] = %s, want %s", tt.profileType, i, arg, tt.expected[i])
				}
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a very long string", 10, "this is..."},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateStr(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

func TestSortByAbsDelta(t *testing.T) {
	comps := []FuncComparison{
		{FuncName: "small", Delta: 2.0},
		{FuncName: "negative_large", Delta: -15.0},
		{FuncName: "large", Delta: 10.0},
		{FuncName: "tiny", Delta: 0.5},
	}

	sortByAbsDelta(comps)

	// Should be sorted by absolute delta descending
	expectedOrder := []string{"negative_large", "large", "small", "tiny"}
	for i, expected := range expectedOrder {
		if comps[i].FuncName != expected {
			t.Errorf("Position %d: expected %s, got %s", i, expected, comps[i].FuncName)
		}
	}
}

func TestGenerateRecommendations(t *testing.T) {
	result := &ComparisonResult{
		FuncComparisons: []FuncComparison{
			{FuncName: "runtime.chanrecv", HighPerfValue: 15.0},
			{FuncName: "makeSlice", HighPerfValue: 8.0},
			{FuncName: "sync.(*Mutex).Lock", HighPerfValue: 10.0},
			{FuncName: "runtime.mallocgc", HighPerfValue: 12.0},
			{FuncName: "syscall.write", HighPerfValue: 25.0},
		},
	}

	result.generateRecommendations()

	// Should have recommendations for all detected patterns
	if len(result.Recommendations) < 4 {
		t.Errorf("Expected at least 4 recommendations, got %d", len(result.Recommendations))
	}

	// Check for specific recommendation types
	hasChannel := false
	hasSlice := false
	hasLock := false
	hasSyscall := false
	hasGC := false

	for _, rec := range result.Recommendations {
		if strings.Contains(rec, "Channel") {
			hasChannel = true
		}
		if strings.Contains(rec, "Slice") {
			hasSlice = true
		}
		if strings.Contains(rec, "Lock") {
			hasLock = true
		}
		if strings.Contains(rec, "Syscall") {
			hasSyscall = true
		}
		if strings.Contains(rec, "GC") {
			hasGC = true
		}
	}

	if !hasChannel {
		t.Error("Expected channel recommendation")
	}
	if !hasSlice {
		t.Error("Expected slice recommendation")
	}
	if !hasLock {
		t.Error("Expected lock recommendation")
	}
	if !hasSyscall {
		t.Error("Expected syscall recommendation")
	}
	if !hasGC {
		t.Error("Expected GC recommendation")
	}
}

