package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewProfileReport(t *testing.T) {
	report := NewProfileReport("TestReport", "isolation", "/tmp/test", 60*time.Second)

	if report.TestName != "TestReport" {
		t.Errorf("Expected TestName 'TestReport', got '%s'", report.TestName)
	}
	if report.TestType != "isolation" {
		t.Errorf("Expected TestType 'isolation', got '%s'", report.TestType)
	}
	if report.OutputDir != "/tmp/test" {
		t.Errorf("Expected OutputDir '/tmp/test', got '%s'", report.OutputDir)
	}
	if report.Duration != 60*time.Second {
		t.Errorf("Expected Duration 60s, got %v", report.Duration)
	}
	if report.Timestamp.IsZero() {
		t.Error("Expected Timestamp to be set")
	}
}

func TestAddAnalysis(t *testing.T) {
	report := NewProfileReport("Test", "isolation", "/tmp", time.Minute)

	analysis := &ProfileAnalysis{
		Component:   "server",
		ProfileType: ProfileCPU,
		TopFuncs: []FuncStat{
			{Name: "main.run", Flat: 50.0},
		},
	}

	report.AddAnalysis(analysis)

	if len(report.Analyses) != 1 {
		t.Errorf("Expected 1 analysis, got %d", len(report.Analyses))
	}
	if report.Analyses[0].Component != "server" {
		t.Errorf("Expected component 'server', got '%s'", report.Analyses[0].Component)
	}
}

func TestAddComparison(t *testing.T) {
	report := NewProfileReport("Test", "parallel", "/tmp", time.Minute)

	comparison := &ComparisonResult{
		ProfileType: ProfileCPU,
		Component:   "server",
		Summary:     "3 improvements, 1 regression",
		Recommendations: []string{
			"Consider buffered channels",
			"Use sync.Pool for buffers",
		},
	}

	report.AddComparison(comparison)

	if !report.IsComparison {
		t.Error("Expected IsComparison to be true")
	}
	if len(report.Comparisons) != 1 {
		t.Errorf("Expected 1 comparison, got %d", len(report.Comparisons))
	}
	if len(report.Recommendations) != 2 {
		t.Errorf("Expected 2 recommendations, got %d", len(report.Recommendations))
	}
}

func TestAddComparisonDeduplicatesRecommendations(t *testing.T) {
	report := NewProfileReport("Test", "parallel", "/tmp", time.Minute)

	comp1 := &ComparisonResult{
		Recommendations: []string{"Recommendation A", "Recommendation B"},
	}
	comp2 := &ComparisonResult{
		Recommendations: []string{"Recommendation B", "Recommendation C"}, // B is duplicate
	}

	report.AddComparison(comp1)
	report.AddComparison(comp2)

	// Should have A, B, C (no duplicate B)
	if len(report.Recommendations) != 3 {
		t.Errorf("Expected 3 unique recommendations, got %d: %v", len(report.Recommendations), report.Recommendations)
	}
}

func TestCalculateOverallSummary(t *testing.T) {
	report := NewProfileReport("Test", "parallel", "/tmp", time.Minute)

	cpuComp := &ComparisonResult{
		ProfileType: ProfileCPU,
		Component:   "server",
		FuncComparisons: []FuncComparison{
			{FuncName: "runtime.chanrecv", Delta: -20.0}, // 20% improvement
			{FuncName: "syscall.write", Delta: -15.0},    // 15% improvement
			{FuncName: "io_uring.Submit", Delta: 10.0},   // 10% regression (new overhead)
		},
	}

	mutexComp := &ComparisonResult{
		ProfileType: ProfileMutex,
		Component:   "server",
		FuncComparisons: []FuncComparison{
			{FuncName: "sync.Mutex.Lock", Delta: -30.0}, // 30% improvement
		},
	}

	report.AddComparison(cpuComp)
	report.AddComparison(mutexComp)
	report.CalculateOverallSummary()

	if report.OverallSummary == nil {
		t.Fatal("Expected OverallSummary to be set")
	}

	// CPU improvement should be sum of reductions: 20 + 15 = 35
	if report.OverallSummary.CPUImprovement != 35.0 {
		t.Errorf("Expected CPU improvement 35.0, got %.1f", report.OverallSummary.CPUImprovement)
	}

	// Lock improvement should be 30
	if report.OverallSummary.LockImprovement != 30.0 {
		t.Errorf("Expected Lock improvement 30.0, got %.1f", report.OverallSummary.LockImprovement)
	}
}

func TestGenerateHTMLReport(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()

	report := NewProfileReport("TestHTMLReport", "isolation", tmpDir, 30*time.Second)
	report.AddAnalysis(&ProfileAnalysis{
		Component:   "server",
		ProfileType: ProfileCPU,
		TopOutput:   "Sample pprof output",
		TopFuncs: []FuncStat{
			{Name: "main.run", Flat: 50.0, FlatStr: "5s 50%"},
			{Name: "runtime.main", Flat: 30.0, FlatStr: "3s 30%"},
		},
	})
	report.Recommendations = []string{"Test recommendation"}

	err := GenerateHTMLReport(report)
	if err != nil {
		t.Fatalf("GenerateHTMLReport failed: %v", err)
	}

	// Check HTML file exists
	htmlPath := filepath.Join(tmpDir, "report.html")
	if _, err := os.Stat(htmlPath); os.IsNotExist(err) {
		t.Error("HTML report was not created")
	}

	// Check HTML content
	htmlContent, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("Failed to read HTML report: %v", err)
	}

	htmlStr := string(htmlContent)
	if !strings.Contains(htmlStr, "TestHTMLReport") {
		t.Error("HTML report should contain test name")
	}
	if !strings.Contains(htmlStr, "main.run") {
		t.Error("HTML report should contain function names")
	}
	if !strings.Contains(htmlStr, "Test recommendation") {
		t.Error("HTML report should contain recommendations")
	}

	// Check JSON file exists
	jsonPath := filepath.Join(tmpDir, "report.json")
	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		t.Error("JSON report was not created")
	}

	// Check summary file exists
	summaryPath := filepath.Join(tmpDir, "summary.txt")
	if _, err := os.Stat(summaryPath); os.IsNotExist(err) {
		t.Error("Summary file was not created")
	}
}

func TestGenerateHTMLReportWithComparison(t *testing.T) {
	tmpDir := t.TempDir()

	report := NewProfileReport("TestComparisonReport", "parallel", tmpDir, 60*time.Second)

	// Add baseline analysis
	baselineAnalysis := &ProfileAnalysis{
		Component:   "server",
		Pipeline:    "baseline",
		ProfileType: ProfileCPU,
		TopFuncs: []FuncStat{
			{Name: "runtime.chanrecv", Flat: 25.0},
		},
	}

	// Add highperf analysis
	highperfAnalysis := &ProfileAnalysis{
		Component:   "server",
		Pipeline:    "highperf",
		ProfileType: ProfileCPU,
		TopFuncs: []FuncStat{
			{Name: "runtime.chanrecv", Flat: 5.0},
		},
	}

	report.AddAnalysis(baselineAnalysis)
	report.AddAnalysis(highperfAnalysis)

	// Add comparison
	comparison := CompareProfiles(baselineAnalysis, highperfAnalysis)
	report.AddComparison(comparison)
	report.CalculateOverallSummary()

	err := GenerateHTMLReport(report)
	if err != nil {
		t.Fatalf("GenerateHTMLReport failed: %v", err)
	}

	// Check HTML content
	htmlPath := filepath.Join(tmpDir, "report.html")
	htmlContent, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("Failed to read HTML report: %v", err)
	}

	htmlStr := string(htmlContent)
	if !strings.Contains(htmlStr, "Comparison") {
		t.Error("HTML report should contain 'Comparison' for parallel tests")
	}
	if !strings.Contains(htmlStr, "baseline") || !strings.Contains(htmlStr, "highperf") {
		t.Error("HTML report should contain pipeline names")
	}
}

func TestGenerateTextSummary(t *testing.T) {
	tmpDir := t.TempDir()
	summaryPath := filepath.Join(tmpDir, "test_summary.txt")

	report := &ProfileReport{
		TestName:  "TestSummary",
		Timestamp: time.Now(),
		OutputDir: tmpDir,
		IsComparison: true,
		OverallSummary: &PerformanceSummary{
			CPUImprovement:  35.0,
			MemImprovement:  20.0,
			LockImprovement: 50.0,
			BlockImprovement: 25.0,
		},
		Recommendations: []string{
			"Reduce channel usage",
			"Use sync.Pool",
		},
		Analyses: []*ProfileAnalysis{
			{
				Component:   "server",
				ProfileType: ProfileCPU,
				TopFuncs: []FuncStat{
					{Name: "main.run", Flat: 50.0},
				},
			},
		},
	}

	generateTextSummary(report, summaryPath)

	content, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("Failed to read summary: %v", err)
	}

	summary := string(content)
	if !strings.Contains(summary, "TestSummary") {
		t.Error("Summary should contain test name")
	}
	if !strings.Contains(summary, "35.0% reduction") {
		t.Error("Summary should contain CPU improvement")
	}
	if !strings.Contains(summary, "Reduce channel usage") {
		t.Error("Summary should contain recommendations")
	}
}

func TestZeroCopyAssessment(t *testing.T) {
	report := NewProfileReport("ZeroCopyTest", "parallel", "/tmp", time.Minute)

	report.ZeroCopyReadiness = &ZeroCopyAssessment{
		EstimatedSavings: "60-70% memory reduction",
		Candidates: []ZeroCopyCandidate{
			{
				Location:     "bytes.makeSlice",
				AllocsPerSec: 45000,
				BytesPerSec:  380000000,
				Poolable:     true,
				Notes:        "Use sync.Pool for packet buffers",
			},
			{
				Location:     "bufio.NewReader",
				AllocsPerSec: 8000,
				BytesPerSec:  12000000,
				Reusable:     true,
				Notes:        "Reuse readers instead of allocating new",
			},
		},
	}

	if len(report.ZeroCopyReadiness.Candidates) != 2 {
		t.Errorf("Expected 2 candidates, got %d", len(report.ZeroCopyReadiness.Candidates))
	}
	if !report.ZeroCopyReadiness.Candidates[0].Poolable {
		t.Error("First candidate should be poolable")
	}
	if !report.ZeroCopyReadiness.Candidates[1].Reusable {
		t.Error("Second candidate should be reusable")
	}
}

