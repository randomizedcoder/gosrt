//go:build !linux

package metrics

// ProcStat holds parsed CPU time values from /proc/self/stat.
// On non-Linux systems, this is a stub that returns zeros.
type ProcStat struct {
	UserTime   uint64
	SystemTime uint64
}

// ReadProcStat is a no-op on non-Linux systems.
func ReadProcStat() (ProcStat, error) {
	return ProcStat{}, nil
}
