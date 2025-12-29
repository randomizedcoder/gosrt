// Package main provides a reflection-based test case generator
// that automatically discovers struct fields and generates all combinations.
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

// FieldInfo holds metadata about a struct field discovered via AST
type FieldInfo struct {
	Name     string
	Type     string
	Tag      string
	Comments []string
}

// StructInfo holds metadata about a test case struct
type StructInfo struct {
	Name   string
	Fields []FieldInfo
}

// ValueRange defines possible values for a field
type ValueRange struct {
	FieldName string
	Values    []interface{}
	Generator func() []interface{} // Optional: dynamic generation
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: test-combinatorial-gen <file.go> [struct-name]")
		fmt.Println("       test-combinatorial-gen -coverage <file.go>")
		fmt.Println()
		fmt.Println("Analyzes test case structs and suggests combinatorial coverage.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -coverage    Check corner case coverage against defined corners")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  test-combinatorial-gen congestion/live/loss_recovery_table_test.go")
		fmt.Println("  test-combinatorial-gen congestion/live/loss_recovery_table_test.go LossRecoveryTestCase")
		fmt.Println("  test-combinatorial-gen -coverage congestion/live/loss_recovery_table_test.go")
		os.Exit(1)
	}

	// Check for coverage mode
	if os.Args[1] == "-coverage" && len(os.Args) > 2 {
		RunCoverageCheck(os.Args[2])
		return
	}

	filename := os.Args[1]
	targetStruct := ""
	if len(os.Args) > 2 {
		targetStruct = os.Args[2]
	}

	// Read file contents
	content, err := os.ReadFile(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}

	// Parse the file (allow test files by parsing as source)
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing file: %v\n", err)
		os.Exit(1)
	}

	// Find test case structs
	structs := findTestCaseStructs(node, targetStruct)

	if len(structs) == 0 {
		fmt.Println("No test case structs found.")
		fmt.Println("Looking for structs with 'TestCase' or 'Case' in the name.")
		os.Exit(0)
	}

	// Analyze each struct
	for _, s := range structs {
		analyzeStruct(s, filename)
	}
}

func findTestCaseStructs(node *ast.File, targetName string) []StructInfo {
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

		// Filter by target name or look for "TestCase" / "Case" suffix
		if targetName != "" {
			if name != targetName {
				return true
			}
		} else {
			if !strings.Contains(name, "TestCase") && !strings.HasSuffix(name, "Case") {
				return true
			}
		}

		info := StructInfo{Name: name}

		for _, field := range structType.Fields.List {
			if len(field.Names) == 0 {
				continue // Embedded field
			}

			fieldInfo := FieldInfo{
				Name: field.Names[0].Name,
				Type: typeToString(field.Type),
			}

			if field.Tag != nil {
				fieldInfo.Tag = field.Tag.Value
			}

			if field.Comment != nil {
				for _, c := range field.Comment.List {
					fieldInfo.Comments = append(fieldInfo.Comments, c.Text)
				}
			}

			info.Fields = append(info.Fields, fieldInfo)
		}

		structs = append(structs, info)
		return true
	})

	return structs
}

func typeToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeToString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeToString(t.X)
	case *ast.ArrayType:
		return "[]" + typeToString(t.Elt)
	case *ast.MapType:
		return "map[" + typeToString(t.Key) + "]" + typeToString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func analyzeStruct(s StructInfo, filename string) {
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Printf("STRUCT: %s (from %s)\n", s.Name, filepath.Base(filename))
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Println()

	// Categorize fields
	var (
		dimensions    []FieldInfo // Fields that define test dimensions
		expectations  []FieldInfo // Fields that define expected outcomes
		configuration []FieldInfo // Fields that configure behavior
	)

	for _, f := range s.Fields {
		switch {
		case strings.HasPrefix(f.Name, "Expected") || strings.HasPrefix(f.Name, "Min") || strings.HasPrefix(f.Name, "Max"):
			expectations = append(expectations, f)
		case f.Name == "Name" || strings.HasSuffix(f.Name, "Pattern") || strings.HasSuffix(f.Name, "Fn"):
			configuration = append(configuration, f)
		default:
			dimensions = append(dimensions, f)
		}
	}

	// Print dimension fields (these should be varied)
	fmt.Println("📊 DIMENSION FIELDS (vary these for combinations):")
	fmt.Println("─────────────────────────────────────────────────")
	for _, f := range dimensions {
		suggestedValues := suggestValues(f)
		fmt.Printf("  %-20s %-15s → %s\n", f.Name, f.Type, suggestedValues)
	}
	fmt.Println()

	// Print expectation fields
	fmt.Println("✓ EXPECTATION FIELDS (derived from dimensions):")
	fmt.Println("─────────────────────────────────────────────────")
	for _, f := range expectations {
		fmt.Printf("  %-20s %-15s\n", f.Name, f.Type)
	}
	fmt.Println()

	// Print configuration fields
	fmt.Println("⚙ CONFIGURATION FIELDS (usually fixed):")
	fmt.Println("─────────────────────────────────────────────────")
	for _, f := range configuration {
		fmt.Printf("  %-20s %-15s\n", f.Name, f.Type)
	}
	fmt.Println()

	// Generate combinatorial suggestions
	fmt.Println("📈 COMBINATORIAL ANALYSIS:")
	fmt.Println("─────────────────────────────────────────────────")

	totalCombinations := 1
	for _, f := range dimensions {
		count := estimateValueCount(f)
		totalCombinations *= count
		fmt.Printf("  %s: ~%d values\n", f.Name, count)
	}
	fmt.Printf("\n  TOTAL POTENTIAL COMBINATIONS: %d\n", totalCombinations)

	// Suggest a reasonable subset
	if totalCombinations > 100 {
		fmt.Println("\n⚠️  Too many combinations for exhaustive testing!")
		fmt.Println("   Suggestions:")
		fmt.Println("   1. Use pairwise testing (covers all pairs with fewer tests)")
		fmt.Println("   2. Use boundary value analysis (min, typical, max for each)")
		fmt.Println("   3. Use equivalence partitioning (group similar values)")
	}

	// Generate code template
	fmt.Println("\n📝 GENERATED CODE TEMPLATE:")
	fmt.Println("─────────────────────────────────────────────────")
	generateCodeTemplate(s, dimensions)

	// Generate smart test plan
	GenerateSmartTestPlan(s.Fields)
}

func suggestValues(f FieldInfo) string {
	switch f.Type {
	case "int":
		if strings.Contains(f.Name, "Packet") || strings.Contains(f.Name, "Total") {
			return "[50, 100, 500, 1000]"
		}
		if strings.Contains(f.Name, "Cycle") {
			return "[5, 10, 20]"
		}
		return "[small, medium, large]"

	case "uint32":
		if strings.Contains(f.Name, "Seq") {
			return "[0, mid, MAX-100] (wraparound testing)"
		}
		return "[0, typical, max]"

	case "uint64":
		if strings.Contains(f.Name, "Tsbpd") || strings.Contains(f.Name, "Delay") {
			return "[50_000, 120_000, 500_000] (µs)"
		}
		if strings.Contains(f.Name, "Spread") || strings.Contains(f.Name, "Interval") {
			return "[100, 1000, 10_000] (µs)"
		}
		return "[small, medium, large]"

	case "float64":
		if strings.Contains(f.Name, "Percent") || strings.Contains(f.Name, "Pct") {
			return "[0.05, 0.10, 0.20]"
		}
		if strings.Contains(f.Name, "Factor") {
			return "[1.0, 2.0, 3.0]"
		}
		return "[0.1, 0.5, 1.0]"

	case "bool":
		return "[true, false]"

	case "string":
		return "[depends on usage]"

	default:
		if strings.HasPrefix(f.Type, "*") || strings.Contains(f.Type, "interface") {
			return "[nil, instance1, instance2, ...]"
		}
		return "[type-specific values]"
	}
}

func estimateValueCount(f FieldInfo) int {
	switch f.Type {
	case "bool":
		return 2
	case "int", "uint32", "uint64":
		return 3 // boundary values: min, typical, max
	case "float64":
		return 3
	case "string":
		return 2
	default:
		return 3 // assume 3 variants for complex types
	}
}

func generateCodeTemplate(s StructInfo, dimensions []FieldInfo) {
	fmt.Println("```go")
	fmt.Printf("// %sGenerator generates all combinations for %s\n", s.Name, s.Name)
	fmt.Printf("type %sGenerator struct {\n", s.Name)

	for _, f := range dimensions {
		fmt.Printf("\t%sValues []%s\n", f.Name, f.Type)
	}
	fmt.Println("}")
	fmt.Println()

	fmt.Printf("func (g *%sGenerator) Generate() []%s {\n", s.Name, s.Name)
	fmt.Println("\tvar cases []" + s.Name)
	fmt.Println()

	// Generate nested loops
	indent := "\t"
	for _, f := range dimensions {
		fmt.Printf("%sfor _, %s := range g.%sValues {\n", indent, strings.ToLower(f.Name[:1])+f.Name[1:], f.Name)
		indent += "\t"
	}

	fmt.Printf("%scases = append(cases, %s{\n", indent, s.Name)
	for _, f := range dimensions {
		varName := strings.ToLower(f.Name[:1]) + f.Name[1:]
		fmt.Printf("%s\t%s: %s,\n", indent, f.Name, varName)
	}
	fmt.Printf("%s\t// TODO: Calculate expectations based on dimensions\n", indent)
	fmt.Printf("%s})\n", indent)

	// Close loops
	for i := len(dimensions) - 1; i >= 0; i-- {
		indent = indent[:len(indent)-1]
		fmt.Printf("%s}\n", indent)
	}

	fmt.Println("\treturn cases")
	fmt.Println("}")
	fmt.Println("```")
}

