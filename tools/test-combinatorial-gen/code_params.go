// code_params.go - Extract actual code parameters from production files
// This differentiates "real code parameters" from "test infrastructure"

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// CodeParameter represents a real parameter in production code
type CodeParameter struct {
	Name        string   // e.g., "tsbpdDelay"
	Type        string   // e.g., "uint64"
	Source      string   // e.g., "receiver struct field"
	File        string   // e.g., "receive.go"
	UsedIn      []string // Functions where it's used
	IsCritical  bool     // Affects core behavior
	Description string
}

// ExtractCodeParameters analyzes production code to find real parameters
func ExtractCodeParameters(sourceDir string) ([]CodeParameter, error) {
	var params []CodeParameter

	// Key production files to analyze
	files := []string{
		"receive.go",
		"send.go",
		"connection.go",
	}

	for _, filename := range files {
		fullPath := filepath.Join(sourceDir, filename)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}

		fileParams, err := analyzeProductionFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("analyzing %s: %w", filename, err)
		}
		params = append(params, fileParams...)
	}

	return params, nil
}

func analyzeProductionFile(filename string) ([]CodeParameter, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var params []CodeParameter
	baseName := filepath.Base(filename)

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.TypeSpec:
			// Find struct definitions (receiver, sender, ReceiveConfig, etc.)
			if st, ok := x.Type.(*ast.StructType); ok {
				structName := x.Name.Name
				if isRelevantStruct(structName) {
					for _, field := range st.Fields.List {
						for _, name := range field.Names {
							param := CodeParameter{
								Name:       name.Name,
								Type:       typeToString(field.Type),
								Source:     fmt.Sprintf("%s struct field", structName),
								File:       baseName,
								IsCritical: isCriticalParam(name.Name),
							}
							params = append(params, param)
						}
					}
				}
			}

		case *ast.FuncDecl:
			// Find function parameters for key functions
			if isKeyFunction(x.Name.Name) {
				if x.Type.Params != nil {
					for _, field := range x.Type.Params.List {
						for _, name := range field.Names {
							param := CodeParameter{
								Name:       name.Name,
								Type:       typeToString(field.Type),
								Source:     fmt.Sprintf("%s() parameter", x.Name.Name),
								File:       baseName,
								IsCritical: isCriticalParam(name.Name),
							}
							params = append(params, param)
						}
					}
				}
			}
		}
		return true
	})

	return params, nil
}

func isRelevantStruct(name string) bool {
	relevant := []string{
		"receiver", "sender",
		"ReceiveConfig", "SendConfig",
		"Config", "StreamConfig",
	}
	for _, r := range relevant {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

func isKeyFunction(name string) bool {
	keyFuncs := []string{
		"contiguousScan", "contiguousScanWithTime",
		"gapScan", "periodicNakBtree", "periodicNAK",
		"periodicACK", "periodicACKLocked",
		"processOnePacket", "drainRingByDelta",
		"Tick", "EventLoop",
	}
	for _, f := range keyFuncs {
		if name == f {
			return true
		}
	}
	return false
}

func isCriticalParam(name string) bool {
	// Parameters that fundamentally affect behavior
	critical := []string{
		"tsbpdDelay", "tsbpd",
		"nakRecentPercent", "nakRecent",
		"nakMergeGap", "mergeGap",
		"initialSequenceNumber", "InitialSequence",
		"contiguousPoint",
		"lastACKSequenceNumber",
		"ringSize", "bufferSize",
	}
	nameLower := strings.ToLower(name)
	for _, c := range critical {
		if strings.Contains(nameLower, strings.ToLower(c)) {
			return true
		}
	}
	return false
}

func typeToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeToString(t.X)
	case *ast.SelectorExpr:
		return typeToString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + typeToString(t.Elt)
	case *ast.MapType:
		return "map[" + typeToString(t.Key) + "]" + typeToString(t.Value)
	default:
		return "unknown"
	}
}

// MatchTestFieldsToCode matches test struct fields to actual code parameters
func MatchTestFieldsToCode(testFields []StructField, codeParams []CodeParameter) map[string]FieldClassification {
	result := make(map[string]FieldClassification)

	for _, tf := range testFields {
		classification := classifyTestField(tf, codeParams)
		result[tf.Name] = classification
	}

	return result
}

type FieldClassification struct {
	Category    string // "code_param", "test_infra", "expectation"
	MatchedCode *CodeParameter
	Reason      string
}

func classifyTestField(tf StructField, codeParams []CodeParameter) FieldClassification {
	tfLower := strings.ToLower(tf.Name)

	// 1. Check if it matches a code parameter
	for _, cp := range codeParams {
		cpLower := strings.ToLower(cp.Name)
		// Direct match or partial match
		if tfLower == cpLower || strings.Contains(tfLower, cpLower) || strings.Contains(cpLower, tfLower) {
			return FieldClassification{
				Category:    "code_param",
				MatchedCode: &cp,
				Reason:      fmt.Sprintf("Matches %s in %s", cp.Source, cp.File),
			}
		}
	}

	// 2. Check if it's an expectation (starts with Expected, Min, Max, or ends with Pct/Count)
	if strings.HasPrefix(tf.Name, "Expected") ||
		strings.HasPrefix(tf.Name, "Min") ||
		strings.HasPrefix(tf.Name, "Max") ||
		strings.HasSuffix(tf.Name, "Count") && strings.HasPrefix(tf.Name, "Expected") {
		return FieldClassification{
			Category: "expectation",
			Reason:   "Name pattern indicates expected result",
		}
	}

	// 3. Check for test infrastructure patterns
	infraPatterns := []string{
		"cycles", "iterations", "spread", "pattern", "packets",
		"name", "description", "skip", "timeout",
	}
	for _, pattern := range infraPatterns {
		if strings.Contains(tfLower, pattern) {
			return FieldClassification{
				Category: "test_infra",
				Reason:   fmt.Sprintf("Name contains '%s' - test infrastructure", pattern),
			}
		}
	}

	// 4. Default to test_infra if we can't match
	return FieldClassification{
		Category: "test_infra",
		Reason:   "No match found in production code",
	}
}

// PrintCodeParameterAnalysis shows the analysis of test vs code params
func PrintCodeParameterAnalysis(testFile, sourceDir string) error {
	// Extract code parameters
	codeParams, err := ExtractCodeParameters(sourceDir)
	if err != nil {
		return fmt.Errorf("extracting code params: %w", err)
	}

	fmt.Printf("\n🔍 Production Code Parameters Found:\n")
	fmt.Printf("═══════════════════════════════════\n")
	for _, cp := range codeParams {
		critical := ""
		if cp.IsCritical {
			critical = " 🎯"
		}
		fmt.Printf("  %-25s %-10s from %s%s\n", cp.Name, cp.Type, cp.Source, critical)
	}

	// Parse test file for structs
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, testFile, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parsing test file: %w", err)
	}

	// Find test case structs
	ast.Inspect(node, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		if !strings.Contains(ts.Name.Name, "Test") && !strings.Contains(ts.Name.Name, "Case") {
			return true
		}

		fmt.Printf("\n📋 Test Struct: %s\n", ts.Name.Name)
		fmt.Printf("═══════════════════════════════════\n")

		var testFields []StructField
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				testFields = append(testFields, StructField{
					Name: name.Name,
					Type: typeToString(field.Type),
				})
			}
		}

		classifications := MatchTestFieldsToCode(testFields, codeParams)

		// Group by category
		codeParamFields := []string{}
		infraFields := []string{}
		expectFields := []string{}

		for fieldName, class := range classifications {
			switch class.Category {
			case "code_param":
				codeParamFields = append(codeParamFields, fieldName)
			case "test_infra":
				infraFields = append(infraFields, fieldName)
			case "expectation":
				expectFields = append(expectFields, fieldName)
			}
		}

		fmt.Printf("\n🎯 CODE PARAMETERS (need combinatorial coverage):\n")
		for _, f := range codeParamFields {
			class := classifications[f]
			fmt.Printf("   ✅ %-20s → %s\n", f, class.Reason)
		}

		fmt.Printf("\n🔧 TEST INFRASTRUCTURE (don't need combinations):\n")
		for _, f := range infraFields {
			class := classifications[f]
			fmt.Printf("   ⚙️  %-20s → %s\n", f, class.Reason)
		}

		fmt.Printf("\n📊 EXPECTATIONS (derived from params):\n")
		for _, f := range expectFields {
			class := classifications[f]
			fmt.Printf("   📈 %-20s → %s\n", f, class.Reason)
		}

		fmt.Printf("\n📌 SUMMARY:\n")
		fmt.Printf("   Code params:    %d (need full corner coverage)\n", len(codeParamFields))
		fmt.Printf("   Test infra:     %d (skip combinatorial)\n", len(infraFields))
		fmt.Printf("   Expectations:   %d (derived)\n", len(expectFields))

		return true
	})

	return nil
}

