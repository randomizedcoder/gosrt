package circular

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const max uint32 = 0b11111111_11111111_11111111_11111111

func ExampleNumber_Inc() {
	a := New(42, max)
	b := a.Inc()

	fmt.Println(b.Val())
	// Output: 43
}

func TestIncNoWrap(t *testing.T) {
	a := New(42, max)

	require.Equal(t, uint32(42), a.Val())

	a = a.Inc()

	require.Equal(t, uint32(43), a.Val())
}

func TestIncWrap(t *testing.T) {
	a := New(max-1, max)

	require.Equal(t, max-1, a.Val())

	a = a.Inc()

	require.Equal(t, max, a.Val())

	a = a.Inc()

	require.Equal(t, uint32(0), a.Val())
}

func TestDecNoWrap(t *testing.T) {
	a := New(42, max)

	require.Equal(t, uint32(42), a.Val())

	a = a.Dec()

	require.Equal(t, uint32(41), a.Val())
}

func TestDecWrap(t *testing.T) {
	a := New(0, max)

	require.Equal(t, uint32(0), a.Val())

	a = a.Dec()

	require.Equal(t, max, a.Val())

	a = a.Dec()

	require.Equal(t, max-1, a.Val())
}

func TestDistanceNoWrap(t *testing.T) {
	a := New(42, max)
	b := New(50, max)

	d := a.Distance(b)

	require.Equal(t, uint32(8), d)

	d = b.Distance(a)

	require.Equal(t, uint32(8), d)
}

func TestDistanceWrap(t *testing.T) {
	a := New(2, max)
	b := New(max-2, max)

	d := a.Distance(b)

	require.Equal(t, uint32(5), d)

	d = b.Distance(a)

	require.Equal(t, uint32(5), d)
}

func TestLt(t *testing.T) {
	a := New(42, max)
	b := New(50, max)
	c := New(max-10, max)

	x := a.Lt(b)

	require.Equal(t, true, x)

	x = b.Lt(a)

	require.Equal(t, false, x)

	x = a.Lt(c)

	require.Equal(t, false, x)

	x = c.Lt(a)

	require.Equal(t, true, x)
}

func TestGt(t *testing.T) {
	a := New(42, max)
	b := New(50, max)
	c := New(max-10, max)

	x := a.Gt(b)

	require.Equal(t, false, x)

	x = b.Gt(a)

	require.Equal(t, true, x)

	x = a.Gt(c)

	require.Equal(t, true, x)

	x = c.Gt(a)

	require.Equal(t, false, x)
}

func TestLtBranchless(t *testing.T) {
	// Test that LtBranchless produces the same results as Lt
	testCases := []struct {
		name string
		a    Number
		b    Number
	}{
		{"simple_less", New(42, max), New(50, max)},
		{"simple_greater", New(50, max), New(42, max)},
		{"wraparound_less", New(max-10, max), New(10, max)},
		{"wraparound_greater", New(10, max), New(max-10, max)},
		{"equal", New(42, max), New(42, max)},
		{"near_wraparound_less", New(max-5, max), New(5, max)},
		{"near_wraparound_greater", New(5, max), New(max-5, max)},
		{"threshold_boundary_less", New(max/2-1, max), New(max/2+1, max)},
		{"threshold_boundary_greater", New(max/2+1, max), New(max/2-1, max)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			expected := tc.a.Lt(tc.b)
			actual := tc.a.LtBranchless(tc.b)
			require.Equal(t, expected, actual, "LtBranchless should match Lt for a=%d, b=%d", tc.a.Val(), tc.b.Val())
		})
	}
}

func TestAdd(t *testing.T) {
	a := New(max-42, max)

	a = a.Add(42)

	require.Equal(t, max, a.Val())

	a = a.Add(1)

	require.Equal(t, uint32(0), a.Val())
}

func TestSub(t *testing.T) {
	a := New(42, max)

	a = a.Sub(42)

	require.Equal(t, uint32(0), a.Val())

	a = a.Sub(1)

	require.Equal(t, max, a.Val())
}
