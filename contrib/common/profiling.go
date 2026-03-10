package common

import (
	"github.com/pkg/profile"
)

// ProfileOption converts a profile flag string to a profile.Profile option.
// Returns nil if the flag is empty or unrecognized.
// Supported: "cpu", "mem", "allocs", "heap", "rate", "mutex", "block", "thread", "trace"
func ProfileOption(flag string) func(*profile.Profile) {
	switch flag {
	case "cpu":
		return profile.CPUProfile
	case "mem":
		return profile.MemProfile
	case "allocs":
		return profile.MemProfileAllocs
	case "heap":
		return profile.MemProfileHeap
	case "rate":
		return profile.MemProfileRate(2048)
	case "mutex":
		return profile.MutexProfile
	case "block":
		return profile.BlockProfile
	case "thread":
		return profile.ThreadcreationProfile
	case "trace":
		return profile.TraceProfile
	default:
		return nil
	}
}
