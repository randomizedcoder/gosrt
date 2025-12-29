package main

import (
	"fmt"
)

// SmartTestStrategy defines different test coverage strategies
type SmartTestStrategy struct {
	// Corner cases - MUST test these
	CornerCases map[string][]interface{}

	// Critical combinations - Specific pairs/triples that MUST be tested together
	CriticalCombinations [][]string

	// Light coverage - Just one representative value
	LightCoverage map[string]interface{}
}

// FieldCategory categorizes fields by their testing importance
type FieldCategory int

const (
	CategoryCorner      FieldCategory = iota // Must test all corner values
	CategoryCritical                         // Important but fewer values needed
	CategoryLight                            // Single representative value is enough
	CategoryDerived                          // Calculated from other fields
	CategoryFixed                            // Fixed value, don't vary
)

// SmartFieldAnalysis holds the smart analysis of a field
type SmartFieldAnalysis struct {
	Name           string
	Category       FieldCategory
	CornerValues   []string // Values that MUST be tested
	TypicalValue   string   // A single representative value
	Reasoning      string   // Why this categorization
}

// AnalyzeFieldSmart determines the smart testing strategy for a field
func AnalyzeFieldSmart(name, typeName string) SmartFieldAnalysis {
	analysis := SmartFieldAnalysis{Name: name}

	// Sequence numbers - CRITICAL for wraparound
	if contains(name, "Seq", "Sequence") && typeName == "uint32" {
		analysis.Category = CategoryCorner
		analysis.CornerValues = []string{"0", "1_000_000", "MAX-100"}
		analysis.TypicalValue = "0"
		analysis.Reasoning = "31-bit wraparound is a critical edge case"
		return analysis
	}

	// Packet counts - Corner cases matter
	if contains(name, "Total", "Count", "Packets") && typeName == "int" {
		analysis.Category = CategoryCritical
		analysis.CornerValues = []string{"small(50)", "large(1000)"}
		analysis.TypicalValue = "100"
		analysis.Reasoning = "Size extremes catch buffer/scaling issues"
		return analysis
	}

	// TSBPD delay - Timing critical
	if contains(name, "Tsbpd", "Delay") && typeName == "uint64" {
		analysis.Category = CategoryCritical
		analysis.CornerValues = []string{"50_000 (aggressive)", "500_000 (high latency)"}
		analysis.TypicalValue = "120_000"
		analysis.Reasoning = "Timing edge cases affect NAK/delivery windows"
		return analysis
	}

	// Percentages - Just test boundaries
	if contains(name, "Percent", "Pct") && typeName == "float64" {
		analysis.Category = CategoryLight
		analysis.CornerValues = []string{"0.05 (aggressive)", "0.20 (conservative)"}
		analysis.TypicalValue = "0.10"
		analysis.Reasoning = "Middle values behave similarly"
		return analysis
	}

	// Cycle counts - Light coverage
	if contains(name, "Cycle") && typeName == "int" {
		analysis.Category = CategoryLight
		analysis.TypicalValue = "10"
		analysis.Reasoning = "Cycle count rarely affects correctness"
		return analysis
	}

	// Tick intervals - Light coverage
	if contains(name, "Tick", "Interval") {
		analysis.Category = CategoryLight
		analysis.TypicalValue = "10_000"
		analysis.Reasoning = "Tick timing is implementation detail"
		return analysis
	}

	// Spread/spacing - Light coverage
	if contains(name, "Spread", "Spacing") {
		analysis.Category = CategoryLight
		analysis.TypicalValue = "1_000"
		analysis.Reasoning = "Packet spacing rarely affects correctness"
		return analysis
	}

	// Booleans - Both values if name suggests importance
	if typeName == "bool" {
		if contains(name, "Retransmit", "Recovery", "Enable", "Use") {
			analysis.Category = CategoryCritical
			analysis.CornerValues = []string{"true", "false"}
			analysis.TypicalValue = "true"
			analysis.Reasoning = "Boolean toggles that change behavior significantly"
		} else {
			analysis.Category = CategoryLight
			analysis.TypicalValue = "true"
			analysis.Reasoning = "Boolean with minor impact"
		}
		return analysis
	}

	// Expected/Min/Max fields - Derived
	if hasPrefix(name, "Expected", "Min", "Max") {
		analysis.Category = CategoryDerived
		analysis.Reasoning = "Calculated from other fields"
		return analysis
	}

	// Pattern/Name fields - Fixed per test
	if contains(name, "Pattern", "Name") {
		analysis.Category = CategoryFixed
		analysis.Reasoning = "Configuration, not dimension"
		return analysis
	}

	// Default: Light coverage
	analysis.Category = CategoryLight
	analysis.TypicalValue = "typical"
	analysis.Reasoning = "Default: assume light coverage sufficient"
	return analysis
}

// GenerateSmartTestPlan creates a focused test plan
func GenerateSmartTestPlan(fields []FieldInfo) {
	fmt.Println("\n═══════════════════════════════════════════════════════════════════")
	fmt.Println("SMART TEST PLAN (Corner Cases + Light Coverage)")
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	var (
		cornerFields   []SmartFieldAnalysis
		criticalFields []SmartFieldAnalysis
		lightFields    []SmartFieldAnalysis
		derivedFields  []SmartFieldAnalysis
	)

	// Analyze each field
	for _, f := range fields {
		analysis := AnalyzeFieldSmart(f.Name, f.Type)
		switch analysis.Category {
		case CategoryCorner:
			cornerFields = append(cornerFields, analysis)
		case CategoryCritical:
			criticalFields = append(criticalFields, analysis)
		case CategoryLight:
			lightFields = append(lightFields, analysis)
		case CategoryDerived, CategoryFixed:
			derivedFields = append(derivedFields, analysis)
		}
	}

	// Print corner cases
	fmt.Println("\n🎯 CORNER CASES (MUST test all values):")
	fmt.Println("─────────────────────────────────────────")
	cornerCount := 1
	for _, f := range cornerFields {
		fmt.Printf("  %-20s: %v\n", f.Name, f.CornerValues)
		fmt.Printf("  └─ Reason: %s\n", f.Reasoning)
		cornerCount *= len(f.CornerValues)
	}

	// Print critical combinations
	fmt.Println("\n⚡ CRITICAL FIELDS (test boundary values):")
	fmt.Println("─────────────────────────────────────────")
	criticalCount := 1
	for _, f := range criticalFields {
		fmt.Printf("  %-20s: %v (typical: %s)\n", f.Name, f.CornerValues, f.TypicalValue)
		fmt.Printf("  └─ Reason: %s\n", f.Reasoning)
		if len(f.CornerValues) > 0 {
			criticalCount *= len(f.CornerValues)
		}
	}

	// Print light coverage
	fmt.Println("\n💡 LIGHT COVERAGE (single typical value):")
	fmt.Println("─────────────────────────────────────────")
	for _, f := range lightFields {
		fmt.Printf("  %-20s: %s\n", f.Name, f.TypicalValue)
		fmt.Printf("  └─ Reason: %s\n", f.Reasoning)
	}

	// Print derived fields
	fmt.Println("\n📊 DERIVED/FIXED (calculated or configuration):")
	fmt.Println("─────────────────────────────────────────")
	for _, f := range derivedFields {
		fmt.Printf("  %-20s: %s\n", f.Name, f.Reasoning)
	}

	// Calculate test count
	fmt.Println("\n═══════════════════════════════════════════════════════════════════")
	fmt.Println("TEST COUNT ESTIMATE")
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	// Strategy 1: Full corner coverage
	fullCorner := cornerCount * criticalCount
	fmt.Printf("\n📌 Strategy 1: Full corner × critical = %d tests\n", fullCorner)
	fmt.Println("   Tests ALL corner cases with ALL critical boundaries")

	// Strategy 2: Corner + selective critical
	selective := cornerCount * 2 // Just min/max of critical
	fmt.Printf("\n📌 Strategy 2: Corner × 2 (boundary only) = %d tests\n", selective)
	fmt.Println("   Tests ALL corner cases with only min/max of critical")

	// Strategy 3: Critical combinations only
	criticalCombos := defineCriticalCombinations(cornerFields, criticalFields)
	fmt.Printf("\n📌 Strategy 3: Critical combinations only = %d tests\n", len(criticalCombos))
	fmt.Println("   Tests SPECIFIC high-risk combinations:")
	for _, combo := range criticalCombos {
		fmt.Printf("     • %s\n", combo)
	}

	// Recommendation
	fmt.Println("\n✅ RECOMMENDATION:")
	fmt.Println("─────────────────────────────────────────")
	recommended := max(selective, len(criticalCombos))
	fmt.Printf("   Use Strategy 2 or 3: ~%d tests\n", recommended)
	fmt.Printf("   This is %.2f%% of full combinatorial (%d tests)\n",
		float64(recommended)/78732*100, 78732)
}

// defineCriticalCombinations returns specific high-risk combinations
func defineCriticalCombinations(corner, critical []SmartFieldAnalysis) []string {
	combos := []string{
		// Wraparound + large stream
		"StartSeq=MAX-100 + TotalPackets=1000 (wraparound stress)",
		// Wraparound + burst loss
		"StartSeq=MAX-100 + DropPattern=Burst (wraparound + loss)",
		// Small TSBPD + heavy loss
		"TsbpdDelay=50ms + DropPattern=Every5th (tight timing)",
		// Large TSBPD + large stream
		"TsbpdDelay=500ms + TotalPackets=1000 (buffer stress)",
		// Zero start + every pattern
		"StartSeq=0 + DropPattern=* (baseline for each pattern)",
		// No retransmit scenarios
		"DoRetransmit=false + DropPattern=Burst (permanent loss)",
	}
	return combos
}

func contains(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func hasPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

