//go:build linux

package metrics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Sample /proc/self/stat content for testing
const sampleProcStat = `818996 (cat) R 65005 818996 65005 34816 818996 4194304 295 0 0 0 0 0 0 0 20 0 1 0 918232 15704064 1018 18446744073709551615 94800145186816 94800146130609 140736276191024 0 0 0 0 0 0 0 0 0 17 11 0 0 0 0 0 94800146470184 94800146524676 94800940986368 140736276193520 140736276193540 140736276193540 140736276205529 0`

// Sample with spaces in comm field
const sampleProcStatWithSpaces = `12345 (my program name) S 1234 12345 12345 0 -1 4194304 100 0 0 0 150 75 0 0 20 0 1 0 12345 1234567 123 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0`

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		expectUser   uint64
		expectSystem uint64
	}{
		{"cat process", sampleProcStat, 0, 0},
		{"process with spaces", sampleProcStatWithSpaces, 150, 75},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stat, err := parseProcStat(tc.content)
			require.NoError(t, err)
			require.Equal(t, tc.expectUser, stat.UserTime)
			require.Equal(t, tc.expectSystem, stat.SystemTime)
		})
	}
}

func TestReadProcStatReal(t *testing.T) {
	stat, err := ReadProcStat()
	require.NoError(t, err)
	t.Logf("User: %d jiffies, System: %d jiffies", stat.UserTime, stat.SystemTime)
}

func TestReadProcStatFromFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "stat")
	content := `99999 (test) S 1 99999 99999 0 -1 4194304 1000 0 0 0 500 250 0 0 20 0 4 0 12345678 123456789 5000 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 2 0 0 0 0 0`
	require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))

	stat, err := readProcStatFromPath(tmpFile)
	require.NoError(t, err)
	require.Equal(t, uint64(500), stat.UserTime)
	require.Equal(t, uint64(250), stat.SystemTime)
}

func TestProcStatMalformed(t *testing.T) {
	tests := []string{"", "12345 test S 1", "12345 (test) S 1 2 3", "12345 (test"}
	for _, tc := range tests {
		stat, _ := parseProcStat(tc)
		require.Equal(t, uint64(0), stat.UserTime)
		require.Equal(t, uint64(0), stat.SystemTime)
	}
}

// BenchmarkReadProcStat benchmarks full I/O + parsing
func BenchmarkReadProcStat(b *testing.B) {
	ReadProcStat() // warm up
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ReadProcStat()
	}
}

// BenchmarkParseProcStatBytes benchmarks parsing only (no I/O)
func BenchmarkParseProcStatBytes(b *testing.B) {
	data := []byte(sampleProcStatWithSpaces)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = parseProcStatBytes(data)
	}
}
