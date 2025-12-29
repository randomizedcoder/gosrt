package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
)

// FieldInfo and StructInfo are defined in main.go but we need them here too
// They're in the same package so this works

// CornerValue represents a corner case value for a field
type CornerValue struct {
	Field       string
	Value       string      // String representation
	NumericVal  interface{} // Parsed numeric value if applicable
	IsCritical  bool        // Is this a critical corner case?
	Description string      // Why this is a corner case
}

// TestCoverage tracks which corner cases are covered
type TestCoverage struct {
	Field         string
	CornerValues  []CornerValue
	CoveredValues map[string][]string // value -> list of test names covering it
	MissingValues []CornerValue       // Corner values not covered by any test
}

// GenerateCornerCasesForField auto-generates corner cases based on field name and type
func GenerateCornerCasesForField(fieldName, fieldType string) []CornerValue {
	var corners []CornerValue
	maxSeq := uint32(0x7FFFFFFF)

	// Sequence number fields - CRITICAL for 31-bit wraparound
	if containsAny(fieldName, "Seq", "Sequence", "Point") && fieldType == "uint32" {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "0", IsCritical: true, Description: "Zero - baseline"},
			CornerValue{Field: fieldName, Value: fmt.Sprintf("%d", maxSeq-100), IsCritical: true, Description: "Near MAX - wraparound zone"},
			CornerValue{Field: fieldName, Value: fmt.Sprintf("%d", maxSeq), IsCritical: true, Description: "AT MAX - immediate wrap"},
		)
		return corners
	}

	// Packet count fields
	if containsAny(fieldName, "Total", "Count", "Packets", "Size") && (fieldType == "int" || fieldType == "uint32") {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "1", IsCritical: true, Description: "Minimum - single item"},
			CornerValue{Field: fieldName, Value: "100", IsCritical: false, Description: "Typical"},
			CornerValue{Field: fieldName, Value: "1000", IsCritical: true, Description: "Large - stress test"},
		)
		return corners
	}

	// Time/delay fields in microseconds
	if containsAny(fieldName, "Tsbpd", "Delay", "Timeout", "Interval") && fieldType == "uint64" {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "10000", IsCritical: true, Description: "10ms - aggressive"},
			CornerValue{Field: fieldName, Value: "120000", IsCritical: false, Description: "120ms - standard"},
			CornerValue{Field: fieldName, Value: "500000", IsCritical: true, Description: "500ms - high latency"},
		)
		return corners
	}

	// Time fields (generic)
	if containsAny(fieldName, "Time", "Mock") && fieldType == "uint64" {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "past", IsCritical: true, Description: "Time in past"},
			CornerValue{Field: fieldName, Value: "present", IsCritical: false, Description: "Current time"},
			CornerValue{Field: fieldName, Value: "future", IsCritical: true, Description: "Time in future"},
		)
		return corners
	}

	// Percentage fields
	if containsAny(fieldName, "Percent", "Pct", "Ratio") && fieldType == "float64" {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "0.01", IsCritical: true, Description: "1% - minimum"},
			CornerValue{Field: fieldName, Value: "0.10", IsCritical: false, Description: "10% - typical"},
			CornerValue{Field: fieldName, Value: "0.50", IsCritical: true, Description: "50% - extreme"},
		)
		return corners
	}

	// Gap/threshold fields
	if containsAny(fieldName, "Gap", "Threshold", "Merge") && (fieldType == "uint32" || fieldType == "int") {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "0", IsCritical: true, Description: "Zero gap"},
			CornerValue{Field: fieldName, Value: "1", IsCritical: true, Description: "Minimum gap"},
			CornerValue{Field: fieldName, Value: "10", IsCritical: false, Description: "Typical gap"},
			CornerValue{Field: fieldName, Value: "100", IsCritical: true, Description: "Large gap"},
		)
		return corners
	}

	// Boolean fields
	if fieldType == "bool" {
		critical := containsAny(fieldName, "Enable", "Use", "Do", "Allow", "Retransmit", "Recovery")
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "true", IsCritical: critical, Description: "Enabled"},
			CornerValue{Field: fieldName, Value: "false", IsCritical: critical, Description: "Disabled"},
		)
		return corners
	}

	// Slice/array fields (check for empty, single, multiple)
	if strings.HasPrefix(fieldType, "[]") {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "empty", IsCritical: true, Description: "Empty slice"},
			CornerValue{Field: fieldName, Value: "single", IsCritical: true, Description: "Single element"},
			CornerValue{Field: fieldName, Value: "multiple", IsCritical: false, Description: "Multiple elements"},
		)
		return corners
	}

	// Interface/pointer fields (patterns, functions)
	if strings.HasPrefix(fieldType, "*") || fieldType == "interface{}" || containsAny(fieldName, "Pattern", "Fn", "Func") {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "nil", IsCritical: true, Description: "Nil/unset"},
			CornerValue{Field: fieldName, Value: "set", IsCritical: true, Description: "Valid instance"},
		)
		return corners
	}

	// Cycle/iteration counts
	if containsAny(fieldName, "Cycle", "Iteration", "Repeat") && fieldType == "int" {
		corners = append(corners,
			CornerValue{Field: fieldName, Value: "1", IsCritical: true, Description: "Single cycle"},
			CornerValue{Field: fieldName, Value: "10", IsCritical: false, Description: "Typical cycles"},
		)
		return corners
	}

	// Default: no specific corners for this field
	return corners
}

// GenerateCornerCasesForStruct generates corner cases for all fields in a struct
func GenerateCornerCasesForStruct(fields []FieldInfo) []CornerValue {
	var allCorners []CornerValue

	for _, field := range fields {
		// Skip derived/expectation fields
		if strings.HasPrefix(field.Name, "Expected") ||
			strings.HasPrefix(field.Name, "Min") ||
			strings.HasPrefix(field.Name, "Max") ||
			field.Name == "Name" {
			continue
		}

		corners := GenerateCornerCasesForField(field.Name, field.Type)
		allCorners = append(allCorners, corners...)
	}

	return allCorners
}

// containsAny checks if s contains any of the substrings
func containsAny(s string, substrs ...string) bool {
	sLower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(sLower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// DefineCornerCases is kept for backward compatibility but now generates dynamically
func DefineCornerCases() []CornerValue {
	// This is the legacy function - now we use GenerateCornerCasesForStruct
	// which auto-discovers fields
	return nil
}

// ExtractTestValues parses a test file and extracts actual values used
func ExtractTestValues(filename string) (map[string]map[string][]string, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// Map: field -> value -> test names
	values := make(map[string]map[string][]string)

	// Find the test cases slice
	ast.Inspect(node, func(n ast.Node) bool {
		// Look for composite literals that look like test cases
		compLit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}

		// Check if this is inside a slice of test cases
		currentTestName := ""

		for _, elt := range compLit.Elts {
			kvExpr, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}

			key, ok := kvExpr.Key.(*ast.Ident)
			if !ok {
				continue
			}

			fieldName := key.Name
			valueStr := extractValue(kvExpr.Value)

			// Track test name
			if fieldName == "Name" {
				currentTestName = strings.Trim(valueStr, "\"")
				continue
			}

			// Initialize map if needed
			if values[fieldName] == nil {
				values[fieldName] = make(map[string][]string)
			}

			// Record this value
			if currentTestName != "" {
				values[fieldName][valueStr] = append(values[fieldName][valueStr], currentTestName)
			}
		}

		return true
	})

	return values, nil
}

// extractValue converts an AST expression to a string representation
func extractValue(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		if v.Name == "true" || v.Name == "false" {
			return v.Name
		}
		return v.Name
	case *ast.SelectorExpr:
		// e.g., packet.MAX_SEQUENCENUMBER
		return extractValue(v.X) + "." + v.Sel.Name
	case *ast.BinaryExpr:
		// e.g., MAX - 50
		return extractValue(v.X) + " " + v.Op.String() + " " + extractValue(v.Y)
	case *ast.UnaryExpr:
		return v.Op.String() + extractValue(v.X)
	case *ast.CallExpr:
		// e.g., &DropBurst{...}
		return extractValue(v.Fun)
	case *ast.CompositeLit:
		if v.Type != nil {
			return extractValue(v.Type)
		}
		return "composite"
	case *ast.StarExpr:
		return "*" + extractValue(v.X)
	default:
		return fmt.Sprintf("%T", expr)
	}
}

// CheckCoverage compares defined corners against actual test values
func CheckCoverage(corners []CornerValue, testValues map[string]map[string][]string) []TestCoverage {
	// Group corners by field
	cornersByField := make(map[string][]CornerValue)
	for _, c := range corners {
		cornersByField[c.Field] = append(cornersByField[c.Field], c)
	}

	var coverage []TestCoverage

	for field, fieldCorners := range cornersByField {
		tc := TestCoverage{
			Field:         field,
			CornerValues:  fieldCorners,
			CoveredValues: make(map[string][]string),
		}

		actualValues := testValues[field]

		for _, corner := range fieldCorners {
			covered := false

			// Check for exact match or equivalent
			for actualVal, testNames := range actualValues {
				if valuesMatch(corner, actualVal) {
					tc.CoveredValues[corner.Value] = testNames
					covered = true
					break
				}
			}

			if !covered {
				tc.MissingValues = append(tc.MissingValues, corner)
			}
		}

		coverage = append(coverage, tc)
	}

	return coverage
}

// valuesMatch checks if a corner value matches an actual test value
func valuesMatch(corner CornerValue, actualStr string) bool {
	// Direct string match
	if corner.Value == actualStr {
		return true
	}

	// Try numeric comparison
	switch v := corner.NumericVal.(type) {
	case uint32:
		if actual, err := strconv.ParseUint(actualStr, 10, 32); err == nil {
			return uint32(actual) == v
		}
		// Check for expressions like "MAX - 50"
		if strings.Contains(actualStr, "MAX_SEQUENCENUMBER") {
			if strings.Contains(actualStr, "- 50") && v == 0x7FFFFFFF-50 {
				return true
			}
			if strings.Contains(actualStr, "- 100") && v == 0x7FFFFFFF-100 {
				return true
			}
		}
	case uint64:
		if actual, err := strconv.ParseUint(actualStr, 10, 64); err == nil {
			return actual == v
		}
		// Handle underscore notation like 120_000
		cleanStr := strings.ReplaceAll(actualStr, "_", "")
		if actual, err := strconv.ParseUint(cleanStr, 10, 64); err == nil {
			return actual == v
		}
	case int:
		if actual, err := strconv.Atoi(actualStr); err == nil {
			return actual == v
		}
	case float64:
		if actual, err := strconv.ParseFloat(actualStr, 64); err == nil {
			return actual == v
		}
	case bool:
		return actualStr == fmt.Sprintf("%v", v)
	}

	// Check for pattern type matches
	if corner.Field == "DropPattern" {
		return strings.Contains(actualStr, corner.Value)
	}

	return false
}

// PrintCoverageReport generates a detailed coverage report
func PrintCoverageReport(coverage []TestCoverage) {
	fmt.Println("\n═══════════════════════════════════════════════════════════════════")
	fmt.Println("CORNER CASE COVERAGE REPORT")
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	totalCorners := 0
	coveredCorners := 0
	criticalMissing := 0

	for _, tc := range coverage {
		totalCorners += len(tc.CornerValues)
		coveredCorners += len(tc.CoveredValues)

		fmt.Printf("\n📊 %s:\n", tc.Field)
		fmt.Println("─────────────────────────────────────────")

		// Show covered
		for _, corner := range tc.CornerValues {
			if tests, ok := tc.CoveredValues[corner.Value]; ok {
				status := "✅"
				if corner.IsCritical {
					status = "✅🎯"
				}
				fmt.Printf("  %s %-15s covered by: %v\n", status, corner.Value, tests)
			}
		}

		// Show missing
		for _, missing := range tc.MissingValues {
			status := "❌"
			if missing.IsCritical {
				status = "❌🚨"
				criticalMissing++
			}
			fmt.Printf("  %s %-15s MISSING - %s\n", status, missing.Value, missing.Description)
		}
	}

	// Summary
	fmt.Println("\n═══════════════════════════════════════════════════════════════════")
	fmt.Println("SUMMARY")
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf("Total corner cases:    %d\n", totalCorners)
	fmt.Printf("Covered:               %d (%.1f%%)\n", coveredCorners, float64(coveredCorners)/float64(totalCorners)*100)
	fmt.Printf("Missing:               %d\n", totalCorners-coveredCorners)
	fmt.Printf("Critical missing:      %d 🚨\n", criticalMissing)

	if criticalMissing > 0 {
		fmt.Println("\n⚠️  CRITICAL CORNER CASES NOT COVERED!")
		fmt.Println("   These should be added to the test suite.")
	} else if totalCorners == coveredCorners {
		fmt.Println("\n✅ ALL CORNER CASES COVERED!")
	}
}

// RunCoverageCheck is the main entry point for coverage checking
func RunCoverageCheck(filename string) {
	// Read and parse the file to discover structs
	content, err := os.ReadFile(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		return
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing file: %v\n", err)
		return
	}

	// Find test case structs
	structs := findTestCaseStructsForCoverage(node)
	if len(structs) == 0 {
		fmt.Println("No test case structs found in", filename)
		return
	}

	// Extract test values from the file
	testValues, err := ExtractTestValues(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error extracting test values: %v\n", err)
		return
	}

	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf("CORNER CASE COVERAGE: %s\n", filename)
	fmt.Println("═══════════════════════════════════════════════════════════════════")

	// Process each struct
	for _, s := range structs {
		fmt.Printf("\n📋 Struct: %s (%d fields)\n", s.Name, len(s.Fields))
		fmt.Println("───────────────────────────────────────────────────────────────────")

		// Generate struct-specific corner cases
		corners := GenerateCornerCasesForStruct(s.Fields)

		if len(corners) == 0 {
			fmt.Println("  (no corner cases generated for this struct)")
			continue
		}

		// Check coverage
		coverage := CheckCoverage(corners, testValues)
		PrintStructCoverageReport(s.Name, coverage)
	}
}

// findTestCaseStructsForCoverage finds structs that look like test cases
func findTestCaseStructsForCoverage(node *ast.File) []StructInfo {
	var structs []StructInfo

	ast.Inspect(node, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}

		structType, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			return true
		}

		name := typeSpec.Name.Name

		// Look for test case structs
		if !strings.Contains(name, "Test") && !strings.Contains(name, "Case") {
			return true
		}

		info := StructInfo{Name: name}

		for _, field := range structType.Fields.List {
			if len(field.Names) == 0 {
				continue
			}

			info.Fields = append(info.Fields, FieldInfo{
				Name: field.Names[0].Name,
				Type: typeToStringForCoverage(field.Type),
			})
		}

		if len(info.Fields) > 0 {
			structs = append(structs, info)
		}
		return true
	})

	return structs
}

// typeToStringForCoverage converts AST type to string
func typeToStringForCoverage(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeToStringForCoverage(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeToStringForCoverage(t.X)
	case *ast.ArrayType:
		return "[]" + typeToStringForCoverage(t.Elt)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "unknown"
	}
}

// PrintStructCoverageReport prints coverage for a specific struct
func PrintStructCoverageReport(structName string, coverage []TestCoverage) {
	totalCorners := 0
	coveredCorners := 0
	criticalMissing := 0

	for _, tc := range coverage {
		totalCorners += len(tc.CornerValues)
		coveredCorners += len(tc.CoveredValues)

		// Count covered
		for _, corner := range tc.CornerValues {
			if tests, ok := tc.CoveredValues[corner.Value]; ok {
				status := "✅"
				if corner.IsCritical {
					status = "✅🎯"
				}
				fmt.Printf("  %s %-20s: %s [%v]\n", status, tc.Field+"/"+corner.Value, corner.Description, tests)
			}
		}

		// Count and show missing
		for _, missing := range tc.MissingValues {
			status := "❌"
			if missing.IsCritical {
				status = "❌🚨"
				criticalMissing++
			}
			fmt.Printf("  %s %-20s: %s\n", status, tc.Field+"/"+missing.Value, missing.Description)
		}
	}

	// Summary for this struct
	fmt.Println()
	fmt.Printf("  Summary: %d/%d corners covered (%.1f%%)\n", coveredCorners, totalCorners,
		float64(coveredCorners)/float64(totalCorners)*100)
	if criticalMissing > 0 {
		fmt.Printf("  ⚠️  %d CRITICAL corners missing\n", criticalMissing)
	} else if coveredCorners == totalCorners {
		fmt.Printf("  ✅ All corners covered!\n")
	}
}

