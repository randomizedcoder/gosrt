// seq_math_31bit_wraparound_test.go - Comprehensive tests for 31-bit sequence wraparound
//
// This file documents the 31-bit wraparound bug and demonstrates:
// 1. SeqLessBroken preserves the OLD broken implementation for documentation
// 2. Tests show that SeqLessBroken FAILS at MAX→0 boundary (documented, not a test failure)
// 3. Tests show that SeqLess (fixed) PASSES at MAX→0 boundary
// 4. Performance benchmarks for each approach
//
// CONTEXT (SRT RFC Section 3.1):
//
//	Packet Sequence Number: 31 bits. The sequential number of the data packet.
//	Bit 0 is the data/control flag, leaving 31 bits for sequence numbers.
//	Range: 0 to 2,147,483,647 (0x7FFFFFFF)
//
// WHY THE BUG WASN'T CAUGHT:
//   - Tests for 16/32/64-bit include wraparound cases like "max-10 < 5"
//   - The 31-bit tests explicitly AVOIDED this case (see TestConsistencyAcrossBitWidths)
//   - No test existed for SeqLess(MaxSeqNumber31, 0) - the exact boundary case
//
// FIX HISTORY:
//   - Original: Used signed int32 arithmetic (broken for 31-bit)
//   - Fixed: Uses threshold-based comparison (works correctly)
//   - SeqLessBroken preserved for documentation and regression testing
package circular

import (
	"testing"
)

// =============================================================================
// PRESERVED BROKEN IMPLEMENTATION - For documentation and regression testing
// =============================================================================

// SeqLessBroken is the OLD broken implementation preserved for documentation.
// It uses signed int32 arithmetic which FAILS for 31-bit sequences because
// int32(MaxSeqNumber31 - 0) = 2147483647 (positive, doesn't overflow).
//
// DO NOT USE in production code. This is only for testing and documentation.
//
// Bug: For 31-bit sequences at the MAX→0 boundary:
//   - SeqLessBroken(MaxSeqNumber31, 0) = false (WRONG! should be true)
//   - SeqLessBroken(0, MaxSeqNumber31) = true (WRONG! should be false)
func SeqLessBroken(a, b uint32) bool {
	// Mask to 31 bits to ensure we're in SRT sequence space
	a = a & MaxSeqNumber31
	b = b & MaxSeqNumber31

	// Signed comparison - BROKEN for 31-bit because no overflow occurs
	diff := int32(a - b)
	return diff < 0
}

// =============================================================================
// PART 0: REGRESSION TEST - Prove SeqLessBroken is broken, SeqLess is fixed
// =============================================================================

// TestRegression_SeqLessBroken_FailsAtWraparound documents that SeqLessBroken
// has the bug. This test PASSES (because we expect the broken behavior).
// If this test ever fails, it means someone accidentally "fixed" SeqLessBroken.
func TestRegression_SeqLessBroken_FailsAtWraparound(t *testing.T) {
	t.Log("=== DOCUMENTING BROKEN BEHAVIOR IN SeqLessBroken ===")
	t.Log("This test PASSES because we EXPECT SeqLessBroken to return wrong values.")
	t.Log("")

	testCases := []struct {
		name       string
		a          uint32
		b          uint32
		correctVal bool // What the correct answer should be
		brokenVal  bool // What SeqLessBroken actually returns (wrong!)
	}{
		// Wraparound: MAX is "before" small numbers in circular order
		{"MAX < 0", MaxSeqNumber31, 0, true, false},         // Broken returns false!
		{"MAX < 50", MaxSeqNumber31, 50, true, false},       // Broken returns false!
		{"MAX-10 < 5", MaxSeqNumber31 - 10, 5, true, false}, // Broken returns false!

		// Wraparound: small numbers are "after" MAX in circular order
		{"0 < MAX", 0, MaxSeqNumber31, false, true},         // Broken returns true!
		{"50 < MAX", 50, MaxSeqNumber31, false, true},       // Broken returns true!
		{"5 < MAX-10", 5, MaxSeqNumber31 - 10, false, true}, // Broken returns true!
	}

	allBrokenAsExpected := true
	for _, tc := range testCases {
		got := SeqLessBroken(tc.a, tc.b)

		if got != tc.brokenVal {
			t.Errorf("❌ SeqLessBroken(%d, %d) = %v, expected broken value %v",
				tc.a, tc.b, got, tc.brokenVal)
			allBrokenAsExpected = false
		} else {
			// Document the bug
			if got == tc.correctVal {
				t.Logf("✓ %s: SeqLessBroken = %v (happens to be correct)", tc.name, got)
			} else {
				t.Logf("⚠ %s: SeqLessBroken = %v (WRONG! correct is %v) - BUG DOCUMENTED",
					tc.name, got, tc.correctVal)
			}
		}
	}

	if allBrokenAsExpected {
		t.Log("")
		t.Log("=== SUCCESS: SeqLessBroken exhibits expected broken behavior ===")
		t.Log("This confirms the bug exists in the old implementation.")
	}
}

// TestRegression_SeqLess_FixedAtWraparound proves SeqLess (the fixed version) works.
// This test PASSES because SeqLess correctly handles wraparound.
func TestRegression_SeqLess_FixedAtWraparound(t *testing.T) {
	t.Log("=== VERIFYING FIXED BEHAVIOR IN SeqLess ===")
	t.Log("This test PASSES because SeqLess correctly handles 31-bit wraparound.")
	t.Log("")

	testCases := []struct {
		name string
		a    uint32
		b    uint32
		want bool // The correct answer
	}{
		// Wraparound: MAX is "before" small numbers in circular order
		{"MAX < 0", MaxSeqNumber31, 0, true},
		{"MAX < 50", MaxSeqNumber31, 50, true},
		{"MAX-10 < 5", MaxSeqNumber31 - 10, 5, true},
		{"MAX-100 < 50", MaxSeqNumber31 - 100, 50, true},

		// Wraparound: small numbers are "after" MAX in circular order
		{"0 < MAX", 0, MaxSeqNumber31, false},
		{"50 < MAX", 50, MaxSeqNumber31, false},
		{"5 < MAX-10", 5, MaxSeqNumber31 - 10, false},
		{"50 < MAX-100", 50, MaxSeqNumber31 - 100, false},

		// Normal cases (should still work)
		{"5 < 10", 5, 10, true},
		{"10 < 5", 10, 5, false},
		{"equal", 100, 100, false},
	}

	allCorrect := true
	for _, tc := range testCases {
		got := SeqLess(tc.a, tc.b)

		if got != tc.want {
			t.Errorf("❌ FAIL: %s: SeqLess(%d, %d) = %v, want %v",
				tc.name, tc.a, tc.b, got, tc.want)
			allCorrect = false
		} else {
			t.Logf("✓ PASS: %s: SeqLess(%d, %d) = %v", tc.name, tc.a, tc.b, got)
		}
	}

	if allCorrect {
		t.Log("")
		t.Log("=== SUCCESS: SeqLess correctly handles all wraparound cases ===")
	}
}

// TestRegression_SideBySide_Comparison shows the difference between broken and fixed
func TestRegression_SideBySide_Comparison(t *testing.T) {
	t.Log("=== SIDE-BY-SIDE COMPARISON: SeqLessBroken vs SeqLess ===")
	t.Log("")
	t.Log("| Test Case           | Correct | Broken | Fixed | Broken Bug? |")
	t.Log("|---------------------|---------|--------|-------|-------------|")

	testCases := []struct {
		name    string
		a       uint32
		b       uint32
		correct bool
	}{
		{"MAX < 0", MaxSeqNumber31, 0, true},
		{"MAX < 50", MaxSeqNumber31, 50, true},
		{"MAX-10 < 5", MaxSeqNumber31 - 10, 5, true},
		{"0 < MAX", 0, MaxSeqNumber31, false},
		{"50 < MAX", 50, MaxSeqNumber31, false},
		{"5 < MAX-10", 5, MaxSeqNumber31 - 10, false},
		{"5 < 10 (normal)", 5, 10, true},
		{"10 < 5 (normal)", 10, 5, false},
	}

	bugsFound := 0
	for _, tc := range testCases {
		broken := SeqLessBroken(tc.a, tc.b)
		fixed := SeqLess(tc.a, tc.b)

		brokenHasBug := broken != tc.correct
		bugIndicator := ""
		if brokenHasBug {
			bugIndicator = "YES ⚠"
			bugsFound++
		} else {
			bugIndicator = "no"
		}

		t.Logf("| %-19s | %-7v | %-6v | %-5v | %-11s |",
			tc.name, tc.correct, broken, fixed, bugIndicator)

		// Verify fixed version is always correct
		if fixed != tc.correct {
			t.Errorf("UNEXPECTED: SeqLess (fixed) returned %v, want %v", fixed, tc.correct)
		}
	}

	t.Log("")
	t.Logf("=== SUMMARY: SeqLessBroken has %d bugs, SeqLess has 0 bugs ===", bugsFound)
}

// =============================================================================
// PART 1: DEMONSTRATE THE BUG IN CURRENT IMPLEMENTATION
// =============================================================================

// TestBug_31BitWraparound_SeqLess demonstrates that the current SeqLess
// implementation FAILS at the 31-bit MAX→0 boundary.
//
// This test SHOULD fail with the current implementation to prove the bug exists.
func TestBug_31BitWraparound_SeqLess(t *testing.T) {
	testCases := []struct {
		name     string
		a        uint32
		b        uint32
		want     bool
		buggyVal bool // What current broken implementation returns
	}{
		// These SHOULD be true (a < b in circular terms)
		{"MAX < 0", MaxSeqNumber31, 0, true, false},             // BUG: returns false!
		{"MAX < 1", MaxSeqNumber31, 1, true, false},             // BUG: returns false!
		{"MAX < 50", MaxSeqNumber31, 50, true, false},           // BUG: returns false!
		{"MAX < 98", MaxSeqNumber31, 98, true, false},           // BUG: returns false!
		{"MAX-10 < 5", MaxSeqNumber31 - 10, 5, true, false},     // BUG: returns false!
		{"MAX-100 < 50", MaxSeqNumber31 - 100, 50, true, false}, // BUG: returns false!

		// These SHOULD be false (a > b in circular terms)
		{"0 < MAX", 0, MaxSeqNumber31, false, true},             // BUG: returns true!
		{"1 < MAX", 1, MaxSeqNumber31, false, true},             // BUG: returns true!
		{"50 < MAX", 50, MaxSeqNumber31, false, true},           // BUG: returns true!
		{"98 < MAX", 98, MaxSeqNumber31, false, true},           // BUG: returns true!
		{"5 < MAX-10", 5, MaxSeqNumber31 - 10, false, true},     // BUG: returns true!
		{"50 < MAX-100", 50, MaxSeqNumber31 - 100, false, true}, // BUG: returns true!
	}

	t.Log("=== DEMONSTRATING 31-BIT WRAPAROUND BUG ===")
	t.Logf("MaxSeqNumber31 = %d (0x%X)", MaxSeqNumber31, MaxSeqNumber31)
	t.Log("")

	bugCount := 0
	for _, tc := range testCases {
		got := SeqLess(tc.a, tc.b)

		// Show the internal calculation
		diff := int32(tc.a&MaxSeqNumber31) - int32(tc.b&MaxSeqNumber31)

		if got != tc.want {
			bugCount++
			t.Logf("❌ BUG: %s: SeqLess(%d, %d) = %v, want %v",
				tc.name, tc.a, tc.b, got, tc.want)
			t.Logf("   Internal: int32(%d - %d) = int32(%d) = %d, %d < 0 = %v",
				tc.a, tc.b, tc.a-tc.b, diff, diff, diff < 0)
		} else {
			t.Logf("✓ OK: %s: SeqLess(%d, %d) = %v", tc.name, tc.a, tc.b, got)
		}
	}

	t.Logf("")
	if bugCount == 0 {
		t.Logf("=== ALL %d test cases PASS - implementation is FIXED ===", len(testCases))
	} else {
		t.Errorf("=== RESULT: %d/%d test cases have BUGS ===", bugCount, len(testCases))
	}
}

// TestBug_WhySignedArithmeticFails explains WHY the signed arithmetic trick doesn't work
func TestBug_WhySignedArithmeticFails(t *testing.T) {
	t.Log("=== WHY SIGNED ARITHMETIC FAILS FOR 31-BIT ===")
	t.Log("")

	// The trick works by relying on OVERFLOW
	t.Log("The signed arithmetic trick relies on OVERFLOW:")
	t.Log("")

	// 16-bit example (WORKS)
	t.Log("16-bit (WORKS):")
	max16 := uint16(0xFFFF)
	t.Logf("  max16 = %d (0x%X)", max16, max16)
	// Note: int16(0xFFFF) doesn't compile, must use subtraction directly
	diff16 := int16(max16 - 0)
	t.Logf("  int16(max16 - 0) = int16(%d) = %d (OVERFLOWS to negative!)", max16, diff16)
	t.Logf("  %d < 0 = %v ✓", diff16, diff16 < 0)
	t.Log("")

	// 32-bit example (WORKS)
	t.Log("32-bit (WORKS):")
	max32 := uint32(0xFFFFFFFF)
	t.Logf("  max32 = %d (0x%X)", max32, max32)
	diff32 := int32(max32 - 0)
	t.Logf("  int32(max32 - 0) = int32(%d) = %d (OVERFLOWS to negative!)", max32, diff32)
	t.Logf("  %d < 0 = %v ✓", diff32, diff32 < 0)
	t.Log("")

	// 31-bit example (BROKEN)
	t.Log("31-bit (BROKEN!):")
	t.Logf("  max31 = %d (0x%X)", MaxSeqNumber31, MaxSeqNumber31)
	diff31 := int32(MaxSeqNumber31) - int32(0)
	t.Logf("  int32(max31 - 0) = int32(%d) = %d (NO OVERFLOW - stays positive!)", MaxSeqNumber31, diff31)
	t.Logf("  %d < 0 = %v ✗ WRONG!", diff31, diff31 < 0)
	t.Log("")

	// Even int64 doesn't help
	t.Log("Using int64 doesn't help:")
	diff64 := int64(MaxSeqNumber31) - int64(0)
	t.Logf("  int64(max31 - 0) = %d (still positive!)", diff64)
	t.Logf("  %d < 0 = %v ✗ STILL WRONG!", diff64, diff64 < 0)
	t.Log("")

	t.Log("CONCLUSION: Signed arithmetic only works when the sequence space fills")
	t.Log("the entire range of the signed type (causing overflow at the boundary).")
	t.Log("For 31-bit sequences, we MUST use threshold-based comparison.")
}

// =============================================================================
// PART 2: IMPLEMENT AND TEST EACH FIX OPTION
// =============================================================================

// --- Option C: Threshold-based comparison (RECOMMENDED) ---

// SeqLessThreshold implements correct 31-bit comparison using threshold logic.
// This matches the semantics of circular.Number.Lt() but for raw uint32 values.
func SeqLessThreshold(a, b uint32) bool {
	a = a & MaxSeqNumber31
	b = b & MaxSeqNumber31

	if a == b {
		return false
	}

	// Calculate distance
	var d uint32
	aLessRaw := a < b
	if aLessRaw {
		d = b - a
	} else {
		d = a - b
	}

	// If distance < half sequence space: use raw comparison
	// If distance >= half: it's a wraparound, invert result
	const threshold = MaxSeqNumber31 / 2 // ~1.07 billion
	if d < threshold {
		return aLessRaw
	}
	return !aLessRaw // Wraparound: invert
}

// SeqGreaterThreshold is the complement of SeqLessThreshold
func SeqGreaterThreshold(a, b uint32) bool {
	a = a & MaxSeqNumber31
	b = b & MaxSeqNumber31

	if a == b {
		return false
	}

	var d uint32
	aGreaterRaw := a > b
	if aGreaterRaw {
		d = a - b
	} else {
		d = b - a
	}

	const threshold = MaxSeqNumber31 / 2
	if d < threshold {
		return aGreaterRaw
	}
	return !aGreaterRaw
}

// --- Option A: Using circular.Number.Lt() ---
// (Already implemented in circular.go, just wrapping for comparison)

func SeqLessUsingNumber(a, b uint32) bool {
	aNum := New(a&MaxSeqNumber31, MaxSeqNumber31)
	bNum := New(b&MaxSeqNumber31, MaxSeqNumber31)
	return aNum.Lt(bNum)
}

// TestFix_OptionC_ThresholdBased verifies the threshold-based fix works correctly
func TestFix_OptionC_ThresholdBased(t *testing.T) {
	testCases := []struct {
		name string
		a    uint32
		b    uint32
		want bool
	}{
		// Wraparound cases (the ones that fail with current implementation)
		{"MAX < 0", MaxSeqNumber31, 0, true},
		{"MAX < 1", MaxSeqNumber31, 1, true},
		{"MAX < 50", MaxSeqNumber31, 50, true},
		{"MAX < 98", MaxSeqNumber31, 98, true},
		{"MAX-10 < 5", MaxSeqNumber31 - 10, 5, true},
		{"MAX-100 < 50", MaxSeqNumber31 - 100, 50, true},

		{"0 < MAX", 0, MaxSeqNumber31, false},
		{"1 < MAX", 1, MaxSeqNumber31, false},
		{"50 < MAX", 50, MaxSeqNumber31, false},
		{"98 < MAX", 98, MaxSeqNumber31, false},
		{"5 < MAX-10", 5, MaxSeqNumber31 - 10, false},
		{"50 < MAX-100", 50, MaxSeqNumber31 - 100, false},

		// Normal cases (should still work)
		{"5 < 10", 5, 10, true},
		{"10 < 5", 10, 5, false},
		{"0 < 1", 0, 1, true},
		{"1 < 0", 1, 0, false},
		{"same value", 100, 100, false},
		{"MAX-1 < MAX", MaxSeqNumber31 - 1, MaxSeqNumber31, true},
		{"MAX < MAX-1", MaxSeqNumber31, MaxSeqNumber31 - 1, false},

		// Quarter range (within threshold)
		{"0 < quarter", 0, MaxSeqNumber31 / 4, true},
		{"quarter < 0", MaxSeqNumber31 / 4, 0, false},
	}

	t.Log("=== TESTING OPTION C: Threshold-based SeqLessThreshold ===")
	failCount := 0
	for _, tc := range testCases {
		got := SeqLessThreshold(tc.a, tc.b)
		if got != tc.want {
			failCount++
			t.Errorf("❌ FAIL: %s: SeqLessThreshold(%d, %d) = %v, want %v",
				tc.name, tc.a, tc.b, got, tc.want)
		} else {
			t.Logf("✓ PASS: %s", tc.name)
		}
	}

	if failCount == 0 {
		t.Log("=== ALL TESTS PASSED ===")
	} else {
		t.Errorf("=== %d TESTS FAILED ===", failCount)
	}
}

// TestFix_OptionA_UsingNumber verifies using circular.Number.Lt() works correctly
func TestFix_OptionA_UsingNumber(t *testing.T) {
	testCases := []struct {
		name string
		a    uint32
		b    uint32
		want bool
	}{
		// Wraparound cases
		{"MAX < 0", MaxSeqNumber31, 0, true},
		{"MAX < 1", MaxSeqNumber31, 1, true},
		{"MAX < 98", MaxSeqNumber31, 98, true},
		{"MAX-10 < 5", MaxSeqNumber31 - 10, 5, true},

		{"0 < MAX", 0, MaxSeqNumber31, false},
		{"1 < MAX", 1, MaxSeqNumber31, false},
		{"98 < MAX", 98, MaxSeqNumber31, false},
		{"5 < MAX-10", 5, MaxSeqNumber31 - 10, false},

		// Normal cases
		{"5 < 10", 5, 10, true},
		{"10 < 5", 10, 5, false},
	}

	t.Log("=== TESTING OPTION A: Using circular.Number.Lt() ===")
	failCount := 0
	for _, tc := range testCases {
		got := SeqLessUsingNumber(tc.a, tc.b)
		if got != tc.want {
			failCount++
			t.Errorf("❌ FAIL: %s: SeqLessUsingNumber(%d, %d) = %v, want %v",
				tc.name, tc.a, tc.b, got, tc.want)
		} else {
			t.Logf("✓ PASS: %s", tc.name)
		}
	}

	if failCount == 0 {
		t.Log("=== ALL TESTS PASSED ===")
	}
}

// TestCompare_AllImplementations runs the same tests against all implementations
func TestCompare_AllImplementations(t *testing.T) {
	testCases := []struct {
		a    uint32
		b    uint32
		want bool
	}{
		// Wraparound boundary cases
		{MaxSeqNumber31, 0, true},
		{MaxSeqNumber31, 1, true},
		{MaxSeqNumber31, 98, true},
		{MaxSeqNumber31 - 10, 5, true},
		{MaxSeqNumber31 - 100, 50, true},
		{0, MaxSeqNumber31, false},
		{1, MaxSeqNumber31, false},
		{98, MaxSeqNumber31, false},
		{5, MaxSeqNumber31 - 10, false},
		{50, MaxSeqNumber31 - 100, false},

		// Normal cases
		{5, 10, true},
		{10, 5, false},
		{0, 1, true},
		{1, 0, false},
		{100, 100, false},
		{MaxSeqNumber31 - 1, MaxSeqNumber31, true},
	}

	t.Log("=== COMPARISON OF ALL IMPLEMENTATIONS ===")
	t.Log("")
	t.Log("| Test Case | Expected | Current SeqLess | OptionC Threshold | OptionA Number.Lt |")
	t.Log("|-----------|----------|-----------------|-------------------|-------------------|")

	for _, tc := range testCases {
		current := SeqLess(tc.a, tc.b)
		optionC := SeqLessThreshold(tc.a, tc.b)
		optionA := SeqLessUsingNumber(tc.a, tc.b)

		currentMark := "✓"
		optionCMark := "✓"
		optionAMark := "✓"

		if current != tc.want {
			currentMark = "❌"
		}
		if optionC != tc.want {
			optionCMark = "❌"
		}
		if optionA != tc.want {
			optionAMark = "❌"
		}

		t.Logf("| %d < %d | %v | %s %v | %s %v | %s %v |",
			tc.a, tc.b, tc.want,
			currentMark, current,
			optionCMark, optionC,
			optionAMark, optionA)
	}
}

// =============================================================================
// PART 3: BENCHMARKS
// =============================================================================

// BenchmarkSeqLess_Current benchmarks current (broken) implementation
func BenchmarkSeqLess_Current(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLess(a, c)
	}
}

// BenchmarkSeqLess_Current_Wraparound benchmarks current impl at wraparound
func BenchmarkSeqLess_Current_Wraparound(b *testing.B) {
	a := uint32(MaxSeqNumber31)
	c := uint32(50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLess(a, c)
	}
}

// BenchmarkSeqLess_OptionC_Threshold benchmarks threshold-based fix
func BenchmarkSeqLess_OptionC_Threshold(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLessThreshold(a, c)
	}
}

// BenchmarkSeqLess_OptionC_Threshold_Wraparound benchmarks threshold at wraparound
func BenchmarkSeqLess_OptionC_Threshold_Wraparound(b *testing.B) {
	a := uint32(MaxSeqNumber31)
	c := uint32(50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLessThreshold(a, c)
	}
}

// BenchmarkSeqLess_OptionA_Number benchmarks using circular.Number.Lt()
func BenchmarkSeqLess_OptionA_Number(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLessUsingNumber(a, c)
	}
}

// BenchmarkSeqLess_OptionA_Number_Wraparound benchmarks Number at wraparound
func BenchmarkSeqLess_OptionA_Number_Wraparound(b *testing.B) {
	a := uint32(MaxSeqNumber31)
	c := uint32(50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLessUsingNumber(a, c)
	}
}

// BenchmarkComparison runs all implementations side by side
func BenchmarkComparison(b *testing.B) {
	normalA := uint32(1000000)
	normalB := uint32(1000050)
	wrapA := uint32(MaxSeqNumber31)
	wrapB := uint32(50)

	b.Run("Normal/Current", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SeqLess(normalA, normalB)
		}
	})

	b.Run("Normal/OptionC_Threshold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SeqLessThreshold(normalA, normalB)
		}
	})

	b.Run("Normal/OptionA_Number", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SeqLessUsingNumber(normalA, normalB)
		}
	})

	b.Run("Wraparound/Current", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SeqLess(wrapA, wrapB)
		}
	})

	b.Run("Wraparound/OptionC_Threshold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SeqLessThreshold(wrapA, wrapB)
		}
	})

	b.Run("Wraparound/OptionA_Number", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SeqLessUsingNumber(wrapA, wrapB)
		}
	})
}
