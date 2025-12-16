package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProfileReport holds all data for the HTML report
type ProfileReport struct {
	TestName  string
	TestType  string // "isolation", "parallel", "clean"
	Timestamp time.Time
	OutputDir string
	Duration  time.Duration

	// For single pipeline tests
	Analyses []*ProfileAnalysis

	// For parallel comparison tests
	IsComparison     bool
	BaselineAnalyses []*ProfileAnalysis
	HighPerfAnalyses []*ProfileAnalysis
	Comparisons      []*ComparisonResult

	// Aggregated insights
	OverallSummary    *PerformanceSummary
	Recommendations   []string
	ZeroCopyReadiness *ZeroCopyAssessment
}

// PerformanceSummary aggregates key metrics across all profiles
type PerformanceSummary struct {
	// CPU metrics
	CPUTopFunc    string
	CPUTopPercent float64
	CPUImprovement float64 // For comparisons

	// Memory metrics
	TotalAllocations int64
	TotalBytes       int64
	HeapInUse        int64
	GCCycles         int
	MaxGCPause       time.Duration
	MemImprovement   float64 // For comparisons

	// Contention metrics
	TotalLockWait   time.Duration
	TopContention   string
	LockImprovement float64 // For comparisons

	// Blocking metrics
	TotalBlockTime   time.Duration
	TopBlocker       string
	BlockImprovement float64 // For comparisons
}

// ZeroCopyAssessment identifies opportunities for zero-copy optimizations
type ZeroCopyAssessment struct {
	Candidates       []ZeroCopyCandidate
	EstimatedSavings string
}

// ZeroCopyCandidate represents a potential zero-copy optimization target
type ZeroCopyCandidate struct {
	Location     string
	AllocsPerSec int64
	BytesPerSec  int64
	Poolable     bool
	Reusable     bool
	Notes        string
}

const reportTemplate = `<!DOCTYPE html>
<html>
<head>
    <title>Profile Report: {{.TestName}}</title>
    <style>
        :root {
            --bg-primary: #1a1a2e;
            --bg-secondary: #16213e;
            --bg-card: #0f3460;
            --text-primary: #eee;
            --text-secondary: #aaa;
            --accent: #e94560;
            --success: #00d4aa;
            --warning: #ffa500;
        }
        body {
            font-family: 'JetBrains Mono', 'Fira Code', monospace;
            background: var(--bg-primary);
            color: var(--text-primary);
            margin: 0;
            padding: 20px;
        }
        .container { max-width: 1400px; margin: 0 auto; }
        h1 { color: var(--accent); margin-bottom: 5px; }
        h2 { color: var(--text-primary); border-bottom: 2px solid var(--accent); padding-bottom: 8px; }
        h3 { color: var(--text-secondary); }

        .meta { color: var(--text-secondary); margin-bottom: 20px; }
        .meta code { background: var(--bg-secondary); padding: 2px 6px; border-radius: 3px; }

        .dashboard {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
            gap: 20px;
            margin-bottom: 30px;
        }
        .metric-card {
            background: var(--bg-card);
            padding: 20px;
            border-radius: 8px;
            border-left: 4px solid var(--accent);
        }
        .metric-value { font-size: 2em; color: var(--success); }
        .metric-label { color: var(--text-secondary); font-size: 0.9em; }
        .metric-delta { font-size: 0.8em; margin-top: 5px; }
        .delta-positive { color: var(--success); }
        .delta-negative { color: var(--accent); }

        .comparison-table {
            width: 100%;
            border-collapse: collapse;
            margin: 20px 0;
        }
        .comparison-table th, .comparison-table td {
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid var(--bg-secondary);
        }
        .comparison-table th {
            background: var(--bg-card);
            color: var(--accent);
        }
        .comparison-table tr:hover { background: var(--bg-secondary); }

        .analysis {
            background: var(--bg-secondary);
            margin-bottom: 30px;
            padding: 20px;
            border-radius: 8px;
        }
        .top-output {
            font-family: monospace;
            white-space: pre;
            font-size: 11px;
            overflow-x: auto;
            background: var(--bg-primary);
            padding: 15px;
            border-radius: 5px;
        }
        .flame-link {
            display: inline-block;
            margin: 10px 0;
            padding: 10px 20px;
            background: var(--accent);
            color: white;
            text-decoration: none;
            border-radius: 5px;
            font-weight: bold;
        }
        .flame-link:hover { opacity: 0.9; }

        .recommendations {
            background: linear-gradient(135deg, var(--bg-card), var(--bg-secondary));
            padding: 20px;
            border-radius: 8px;
            margin: 20px 0;
        }
        .recommendations li {
            margin: 10px 0;
            padding: 10px;
            background: var(--bg-primary);
            border-radius: 5px;
            border-left: 3px solid var(--warning);
        }

        .zero-copy {
            background: var(--bg-card);
            padding: 20px;
            border-radius: 8px;
            margin: 20px 0;
        }
        .poolable { color: var(--success); }
        .review { color: var(--warning); }

        .summary-box {
            background: linear-gradient(135deg, #134e5e, #71b280);
            padding: 20px;
            border-radius: 8px;
            margin: 20px 0;
        }

        details {
            margin-top: 10px;
        }
        summary {
            cursor: pointer;
            padding: 10px;
            background: var(--bg-card);
            border-radius: 5px;
            font-weight: bold;
        }
        summary:hover { opacity: 0.9; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔬 Profile Report: {{.TestName}}</h1>
        <div class="meta">
            <p>Type: <code>{{.TestType}}</code> | Generated: <code>{{.Timestamp.Format "2006-01-02 15:04:05"}}</code> | Duration: <code>{{.Duration}}</code></p>
            <p>Output: <code>{{.OutputDir}}</code></p>
        </div>

        {{if .IsComparison}}
        <!-- PARALLEL COMPARISON DASHBOARD -->
        <div class="summary-box">
            <h2>📊 Performance Comparison: Baseline vs HighPerf</h2>
        </div>

        {{if .OverallSummary}}
        <div class="dashboard">
            <div class="metric-card">
                <div class="metric-label">CPU Improvement</div>
                <div class="metric-value">{{printf "%.1f" .OverallSummary.CPUImprovement}}%</div>
                <div class="metric-delta delta-positive">⬇ Channel + syscall overhead reduced</div>
            </div>
            <div class="metric-card">
                <div class="metric-label">Memory Reduction</div>
                <div class="metric-value">{{printf "%.1f" .OverallSummary.MemImprovement}}%</div>
                <div class="metric-delta delta-positive">⬇ Fewer allocations</div>
            </div>
            <div class="metric-card">
                <div class="metric-label">Lock Wait Reduction</div>
                <div class="metric-value">{{printf "%.1f" .OverallSummary.LockImprovement}}%</div>
                <div class="metric-delta delta-positive">⬇ Less contention</div>
            </div>
            <div class="metric-card">
                <div class="metric-label">Block Time Reduction</div>
                <div class="metric-value">{{printf "%.1f" .OverallSummary.BlockImprovement}}%</div>
                <div class="metric-delta delta-positive">⬇ Less I/O waiting</div>
            </div>
        </div>
        {{end}}

        <!-- DETAILED COMPARISONS BY PROFILE TYPE -->
        {{range .Comparisons}}
        <div class="analysis">
            <h2>{{.Component | ToUpper}} - {{.ProfileType | ToUpper}} Comparison</h2>
            <table class="comparison-table">
                <tr>
                    <th>Function</th>
                    <th>Baseline</th>
                    <th>HighPerf</th>
                    <th>Delta</th>
                    <th>Status</th>
                </tr>
                {{range .FuncComparisons}}
                <tr>
                    <td>{{.FuncName}}</td>
                    <td>{{printf "%.1f%%" .BaselineValue}}</td>
                    <td>{{printf "%.1f%%" .HighPerfValue}}</td>
                    <td class="{{if lt .Delta 0.0}}delta-positive{{else}}delta-negative{{end}}">
                        {{printf "%+.1f%%" .Delta}}
                    </td>
                    <td>
                        {{if .IsNew}}🆕 New{{else if .IsRemoved}}✅ Removed{{else if lt .DeltaPercent -20.0}}🏆 Big Win{{else if gt .DeltaPercent 20.0}}⚠️ Regression{{end}}
                    </td>
                </tr>
                {{end}}
            </table>
            <p><strong>Summary:</strong> {{.Summary}}</p>
        </div>
        {{end}}

        {{end}}

        <!-- RECOMMENDATIONS -->
        {{if .Recommendations}}
        <div class="recommendations">
            <h2>💡 Optimization Recommendations</h2>
            <ul>
                {{range .Recommendations}}
                <li>{{.}}</li>
                {{end}}
            </ul>
        </div>
        {{end}}

        <!-- ZERO-COPY ASSESSMENT -->
        {{if .ZeroCopyReadiness}}
        <div class="zero-copy">
            <h2>🚀 Zero-Copy Readiness Assessment</h2>
            <p>Estimated savings with full zero-copy: <strong>{{.ZeroCopyReadiness.EstimatedSavings}}</strong></p>
            <table class="comparison-table">
                <tr>
                    <th>Location</th>
                    <th>Allocs/s</th>
                    <th>Bytes/s</th>
                    <th>Status</th>
                    <th>Notes</th>
                </tr>
                {{range .ZeroCopyReadiness.Candidates}}
                <tr>
                    <td>{{.Location}}</td>
                    <td>{{.AllocsPerSec}}</td>
                    <td>{{.BytesPerSec}}</td>
                    <td>
                        {{if .Poolable}}<span class="poolable">✅ Pool-able</span>
                        {{else if .Reusable}}<span class="poolable">✅ Reusable</span>
                        {{else}}<span class="review">⚠️ Review</span>{{end}}
                    </td>
                    <td>{{.Notes}}</td>
                </tr>
                {{end}}
            </table>
        </div>
        {{end}}

        <!-- RAW PROFILE DATA -->
        <h2>📁 Raw Profile Data</h2>
        {{range .Analyses}}
        <div class="analysis">
            <h3>{{if .Pipeline}}{{.Pipeline}} {{end}}{{.Component}} - {{.ProfileType}}</h3>
            <p>File: <code>{{.FilePath}}</code></p>
            {{if .FlameGraph}}
            <a class="flame-link" href="{{.FlameGraph}}" target="_blank">🔥 View Flame Graph</a>
            {{end}}
            {{if .TopFuncs}}
            <h4>Top Functions</h4>
            <table class="comparison-table">
                <tr><th>Function</th><th>Flat %</th><th>Cumulative %</th></tr>
                {{range .TopFuncs}}
                <tr>
                    <td>{{.Name}}</td>
                    <td>{{printf "%.1f%%" .Flat}}</td>
                    <td>{{printf "%.1f%%" .Cumulative}}</td>
                </tr>
                {{end}}
            </table>
            {{end}}
            {{if .TopOutput}}
            <details>
                <summary>Raw pprof Output (click to expand)</summary>
                <div class="top-output">{{.TopOutput}}</div>
            </details>
            {{end}}
        </div>
        {{end}}
    </div>
</body>
</html>`

// GenerateHTMLReport creates an HTML report from the profile analyses
func GenerateHTMLReport(report *ProfileReport) error {
	funcMap := template.FuncMap{
		"ToUpper": func(v interface{}) string {
			switch s := v.(type) {
			case string:
				return strings.ToUpper(s)
			case ProfileType:
				return strings.ToUpper(string(s))
			default:
				return strings.ToUpper(fmt.Sprintf("%v", v))
			}
		},
	}

	tmpl, err := template.New("report").Funcs(funcMap).Parse(reportTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	reportPath := filepath.Join(report.OutputDir, "report.html")
	f, err := os.Create(reportPath)
	if err != nil {
		return fmt.Errorf("failed to create report file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, report); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	// Also generate JSON for programmatic access
	jsonPath := filepath.Join(report.OutputDir, "report.json")
	jsonData, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to generate JSON report: %v\n", err)
	} else {
		if err := os.WriteFile(jsonPath, jsonData, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to write JSON report: %v\n", err)
		}
	}

	// Generate text summary
	textPath := filepath.Join(report.OutputDir, "summary.txt")
	generateTextSummary(report, textPath)

	fmt.Printf("\n=== Profile Report Generated ===\n")
	fmt.Printf("HTML Report:  %s\n", reportPath)
	fmt.Printf("JSON Data:    %s\n", jsonPath)
	fmt.Printf("Text Summary: %s\n", textPath)

	return nil
}

func generateTextSummary(report *ProfileReport, path string) {
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "PROFILE SUMMARY: %s\n", report.TestName)
	fmt.Fprintf(&buf, "Generated: %s\n", report.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&buf, "%s\n\n", strings.Repeat("=", 70))

	if report.IsComparison && report.OverallSummary != nil {
		fmt.Fprintf(&buf, "PERFORMANCE IMPROVEMENTS (HighPerf vs Baseline)\n")
		fmt.Fprintf(&buf, "%s\n", strings.Repeat("-", 50))
		fmt.Fprintf(&buf, "  CPU Overhead:     %.1f%% reduction\n", report.OverallSummary.CPUImprovement)
		fmt.Fprintf(&buf, "  Memory Usage:     %.1f%% reduction\n", report.OverallSummary.MemImprovement)
		fmt.Fprintf(&buf, "  Lock Contention:  %.1f%% reduction\n", report.OverallSummary.LockImprovement)
		fmt.Fprintf(&buf, "  Blocking Time:    %.1f%% reduction\n", report.OverallSummary.BlockImprovement)
		fmt.Fprintf(&buf, "\n")
	}

	// Show top functions from analyses
	if len(report.Analyses) > 0 {
		fmt.Fprintf(&buf, "TOP FUNCTIONS BY PROFILE\n")
		fmt.Fprintf(&buf, "%s\n", strings.Repeat("-", 50))
		for _, a := range report.Analyses {
			if len(a.TopFuncs) > 0 {
				label := string(a.ProfileType)
				if a.Component != "" {
					label = a.Component + " " + label
				}
				if a.Pipeline != "" {
					label = a.Pipeline + " " + label
				}
				fmt.Fprintf(&buf, "  %s:\n", label)
				for i, f := range a.TopFuncs {
					if i >= 3 {
						break
					}
					fmt.Fprintf(&buf, "    %d. %s (%.1f%%)\n", i+1, f.Name, f.Flat)
				}
			}
		}
		fmt.Fprintf(&buf, "\n")
	}

	// Show comparisons
	if len(report.Comparisons) > 0 {
		fmt.Fprintf(&buf, "COMPARISONS\n")
		fmt.Fprintf(&buf, "%s\n", strings.Repeat("-", 50))
		for _, c := range report.Comparisons {
			fmt.Fprintf(&buf, "  %s %s:\n", c.Component, c.ProfileType)
			fmt.Fprintf(&buf, "    %s\n", strings.TrimSpace(c.Summary))
		}
		fmt.Fprintf(&buf, "\n")
	}

	if len(report.Recommendations) > 0 {
		fmt.Fprintf(&buf, "RECOMMENDATIONS\n")
		fmt.Fprintf(&buf, "%s\n", strings.Repeat("-", 50))
		for i, rec := range report.Recommendations {
			fmt.Fprintf(&buf, "  %d. %s\n", i+1, rec)
		}
		fmt.Fprintf(&buf, "\n")
	}

	fmt.Fprintf(&buf, "FILES\n")
	fmt.Fprintf(&buf, "%s\n", strings.Repeat("-", 50))
	fmt.Fprintf(&buf, "  HTML Report:  %s\n", filepath.Join(report.OutputDir, "report.html"))
	fmt.Fprintf(&buf, "  JSON Data:    %s\n", filepath.Join(report.OutputDir, "report.json"))
	fmt.Fprintf(&buf, "  Profiles:     %s/*.pprof\n", report.OutputDir)

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write text summary: %v\n", err)
	}
}

// NewProfileReport creates a new ProfileReport with basic information
func NewProfileReport(testName, testType string, outputDir string, duration time.Duration) *ProfileReport {
	return &ProfileReport{
		TestName:  testName,
		TestType:  testType,
		Timestamp: time.Now(),
		OutputDir: outputDir,
		Duration:  duration,
	}
}

// AddAnalysis adds a profile analysis to the report
func (r *ProfileReport) AddAnalysis(analysis *ProfileAnalysis) {
	r.Analyses = append(r.Analyses, analysis)
}

// AddComparison adds a comparison result to the report
func (r *ProfileReport) AddComparison(comparison *ComparisonResult) {
	r.IsComparison = true
	r.Comparisons = append(r.Comparisons, comparison)

	// Collect recommendations from comparisons
	for _, rec := range comparison.Recommendations {
		// Avoid duplicates
		found := false
		for _, existing := range r.Recommendations {
			if existing == rec {
				found = true
				break
			}
		}
		if !found {
			r.Recommendations = append(r.Recommendations, rec)
		}
	}
}

// SetOverallSummary sets the overall performance summary
func (r *ProfileReport) SetOverallSummary(summary *PerformanceSummary) {
	r.OverallSummary = summary
}

// CalculateOverallSummary calculates the overall summary from comparisons
func (r *ProfileReport) CalculateOverallSummary() {
	if len(r.Comparisons) == 0 {
		return
	}

	summary := &PerformanceSummary{}

	for _, comp := range r.Comparisons {
		// Calculate improvement as reduction in total overhead
		totalImprovement := 0.0
		for _, fc := range comp.FuncComparisons {
			if fc.Delta < 0 {
				totalImprovement -= fc.Delta // Sum of reductions
			}
		}

		switch comp.ProfileType {
		case ProfileCPU:
			summary.CPUImprovement = totalImprovement
		case ProfileHeap, ProfileAllocs, ProfileMem:
			summary.MemImprovement = totalImprovement
		case ProfileMutex:
			summary.LockImprovement = totalImprovement
		case ProfileBlock:
			summary.BlockImprovement = totalImprovement
		}
	}

	r.OverallSummary = summary
}

// GenerateFromDirectory creates a report from all profiles in a directory
func GenerateReportFromDirectory(testName, testType, profileDir string, duration time.Duration) (*ProfileReport, error) {
	report := NewProfileReport(testName, testType, profileDir, duration)

	// Analyze all profiles
	analyses, err := AnalyzeAllProfiles(profileDir)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze profiles: %w", err)
	}

	for _, a := range analyses {
		report.AddAnalysis(a)
	}

	return report, nil
}

// GenerateComparisonReport creates a comparison report from baseline and highperf directories
func GenerateComparisonReport(testName, baselineDir, highperfDir string, duration time.Duration) (*ProfileReport, error) {
	report := NewProfileReport(testName, "parallel", baselineDir, duration)
	report.IsComparison = true

	// Analyze baseline profiles
	baselineAnalyses, err := AnalyzeAllProfiles(baselineDir)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze baseline profiles: %w", err)
	}
	report.BaselineAnalyses = baselineAnalyses

	// Analyze highperf profiles
	highperfAnalyses, err := AnalyzeAllProfiles(highperfDir)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze highperf profiles: %w", err)
	}
	report.HighPerfAnalyses = highperfAnalyses

	// Add all analyses to the report
	for _, a := range baselineAnalyses {
		a.Pipeline = "baseline"
		report.AddAnalysis(a)
	}
	for _, a := range highperfAnalyses {
		a.Pipeline = "highperf"
		report.AddAnalysis(a)
	}

	// Generate comparisons for matching profile types and components
	for _, baseAnalysis := range baselineAnalyses {
		for _, hpAnalysis := range highperfAnalyses {
			if baseAnalysis.ProfileType == hpAnalysis.ProfileType &&
				baseAnalysis.Component == hpAnalysis.Component {
				comparison := CompareProfiles(baseAnalysis, hpAnalysis)
				report.AddComparison(comparison)
			}
		}
	}

	// Calculate overall summary
	report.CalculateOverallSummary()

	return report, nil
}

