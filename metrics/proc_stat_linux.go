//go:build linux

package metrics

import (
	"os"
)

// ProcStat holds parsed CPU time values from /proc/self/stat.
// Times are in jiffies (typically 1/100th of a second on Linux).
type ProcStat struct {
	UserTime   uint64 // Field 14: utime - user mode jiffies
	SystemTime uint64 // Field 15: stime - kernel mode jiffies
}

// procStatPath is the path to the stat file. Variable for testing.
var procStatPath = "/proc/self/stat"

// scratchBuffer is a reusable buffer for reading /proc/self/stat.
// The file is typically ~300-400 bytes, 512 is plenty with room to spare.
var scratchBuffer = make([]byte, 512)

// ReadProcStat reads and parses /proc/self/stat to extract CPU time.
// Returns user and system time in jiffies (1/100th second on most Linux).
// Zero allocations in the parsing hot path.
func ReadProcStat() (ProcStat, error) {
	return readProcStatFromPath(procStatPath)
}

// readProcStatFromPath reads from a specific path (for testing).
func readProcStatFromPath(path string) (ProcStat, error) {
	var stat ProcStat

	f, err := os.Open(path)
	if err != nil {
		return stat, err
	}
	defer func() { _ = f.Close() }() // Error ignored: read-only file

	n, err := f.Read(scratchBuffer)
	if err != nil {
		return stat, err
	}

	return parseProcStatBytes(scratchBuffer[:n]), nil
}

// parseProcStat parses /proc/self/stat content from a string (for testing).
func parseProcStat(content string) (ProcStat, error) {
	return parseProcStatBytes([]byte(content)), nil
}

// parseProcStatBytes parses /proc/self/stat directly from bytes.
// Zero allocations - works directly on the byte slice.
//
// Format: pid (comm) state ppid ... utime(14) stime(15) ...
// Fields are space-separated, comm can contain spaces (in parentheses).
func parseProcStatBytes(data []byte) ProcStat {
	var stat ProcStat
	n := len(data)
	if n == 0 {
		return stat
	}

	// Find closing paren of comm field (search from end)
	closeParenIdx := -1
	for i := n - 1; i >= 0; i-- {
		if data[i] == ')' {
			closeParenIdx = i
			break
		}
	}
	if closeParenIdx == -1 || closeParenIdx+2 >= n {
		return stat
	}

	pos := closeParenIdx + 1

	// Skip space after ')'
	for pos < n && data[pos] == ' ' {
		pos++
	}

	// Skip 11 fields to reach utime (index 11 after comm)
	for skip := 0; skip < 11 && pos < n; skip++ {
		for pos < n && data[pos] != ' ' {
			pos++
		}
		for pos < n && data[pos] == ' ' {
			pos++
		}
	}

	if pos >= n {
		return stat
	}

	// Parse utime
	stat.UserTime = parseUint64(data, &pos)

	// Skip spaces
	for pos < n && data[pos] == ' ' {
		pos++
	}

	// Parse stime
	if pos < n {
		stat.SystemTime = parseUint64(data, &pos)
	}

	return stat
}

// parseUint64 parses a uint64 from bytes, zero allocations.
func parseUint64(data []byte, pos *int) uint64 {
	var result uint64
	n := len(data)
	p := *pos

	for p < n && data[p] >= '0' && data[p] <= '9' {
		result = result*10 + uint64(data[p]-'0')
		p++
	}

	*pos = p
	return result
}
