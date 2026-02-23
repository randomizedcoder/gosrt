//go:build testing
// +build testing

package live

import (
	"github.com/randomizedcoder/gosrt/congestion"
)

// TestReceiverInternals exposes receiver internals for testing.
// This struct is only available when building with -tags=testing.
type TestReceiverInternals struct {
	UseNakBtree          bool
	SuppressImmediateNak bool
	TsbpdDelay           uint64
	NakRecentPercent     float64
	NakBtreeCreated      bool
	FastNakEnabled       bool
	FastNakRecentEnabled bool
	NakMergeGap          uint32
}

// GetTestInternals returns internal state for testing.
// Only available with -tags=testing build tag.
func (r *receiver) GetTestInternals() TestReceiverInternals {
	return TestReceiverInternals{
		UseNakBtree:          r.useNakBtree,
		SuppressImmediateNak: r.suppressImmediateNak,
		TsbpdDelay:           r.tsbpdDelay,
		NakRecentPercent:     r.nakRecentPercent,
		NakBtreeCreated:      r.nakBtree != nil,
		FastNakEnabled:       r.fastNakEnabled,
		FastNakRecentEnabled: r.fastNakRecentEnabled,
		NakMergeGap:          r.nakMergeGap,
	}
}

// GetReceiverTestInternals extracts test internals from a congestion.Receiver.
// This allows inspection from outside the live package (e.g., from srt package tests).
// Returns the internals and true if the receiver is a *live.receiver, false otherwise.
func GetReceiverTestInternals(r congestion.Receiver) (TestReceiverInternals, bool) {
	recv, ok := r.(*receiver)
	if !ok {
		return TestReceiverInternals{}, false
	}
	return recv.GetTestInternals(), true
}
