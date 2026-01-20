package main

import (
	"context"
	"time"
)

// FakeSeeker implements Seeker interface for testing.
type FakeSeeker struct {
	currentBitrate int64
	setBitrateCalls []int64
	heartbeatCalls  int
	stopCalled      bool
	alive           bool
	failOnBitrate   int64 // If non-zero, fail when this bitrate is set
}

func NewFakeSeeker(initialBitrate int64) *FakeSeeker {
	return &FakeSeeker{
		currentBitrate: initialBitrate,
		alive:          true,
	}
}

func (f *FakeSeeker) SetBitrate(ctx context.Context, bps int64) error {
	f.setBitrateCalls = append(f.setBitrateCalls, bps)
	f.currentBitrate = bps
	if f.failOnBitrate > 0 && bps >= f.failOnBitrate {
		f.alive = false
	}
	return nil
}

func (f *FakeSeeker) Status(ctx context.Context) (SeekerStatus, error) {
	return SeekerStatus{
		CurrentBitrate:  f.currentBitrate,
		TargetBitrate:   f.currentBitrate,
		ConnectionAlive: f.alive,
	}, nil
}

func (f *FakeSeeker) Heartbeat(ctx context.Context) error {
	f.heartbeatCalls++
	return nil
}

func (f *FakeSeeker) Stop(ctx context.Context) error {
	f.stopCalled = true
	return nil
}

func (f *FakeSeeker) IsAlive() bool {
	return f.alive
}

// FakeGate implements Gate interface for testing.
type FakeGate struct {
	// Configuration
	stableUpTo    int64 // Bitrates <= this are stable
	criticalAbove int64 // Bitrates > this cause critical failure

	// Tracking
	probeCalls []FakeProbeCall
}

type FakeProbeCall struct {
	Bitrate   int64
	ProbeStart time.Time
}

func NewFakeGate(stableUpTo, criticalAbove int64) *FakeGate {
	return &FakeGate{
		stableUpTo:    stableUpTo,
		criticalAbove: criticalAbove,
	}
}

func (f *FakeGate) Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult {
	f.probeCalls = append(f.probeCalls, FakeProbeCall{
		Bitrate:    bitrate,
		ProbeStart: probeStart,
	})

	if bitrate <= f.stableUpTo {
		return ProbeResult{
			Verdict:  VerdictStable,
			Duration: 5 * time.Second,
			Samples:  10,
			Reason:   "stable (fake)",
		}
	}

	if f.criticalAbove > 0 && bitrate > f.criticalAbove {
		return ProbeResult{
			Verdict:  VerdictCritical,
			Duration: 2 * time.Second,
			Samples:  4,
			Reason:   "critical threshold exceeded (fake)",
		}
	}

	return ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 5 * time.Second,
		Samples:  10,
		Reason:   "unstable (fake)",
	}
}

func (f *FakeGate) WithConfig(config StabilityConfig) Gate {
	return f // Return same gate for simplicity
}

// DeterministicGate provides deterministic responses for property testing.
type DeterministicGate struct {
	responses []ProbeVerdict
	index     int
}

func NewDeterministicGate(responses []ProbeVerdict) *DeterministicGate {
	return &DeterministicGate{responses: responses}
}

func (d *DeterministicGate) Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult {
	if d.index >= len(d.responses) {
		return ProbeResult{Verdict: VerdictStable}
	}
	verdict := d.responses[d.index]
	d.index++
	return ProbeResult{
		Verdict:  verdict,
		Duration: 5 * time.Second,
		Samples:  10,
	}
}

func (d *DeterministicGate) WithConfig(config StabilityConfig) Gate {
	return d
}

// ThresholdGate returns stable/unstable based on a simple threshold.
type ThresholdGate struct {
	threshold int64
}

func NewThresholdGate(threshold int64) *ThresholdGate {
	return &ThresholdGate{threshold: threshold}
}

func (t *ThresholdGate) Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult {
	if bitrate <= t.threshold {
		return ProbeResult{
			Verdict:  VerdictStable,
			Duration: 5 * time.Second,
			Samples:  10,
			Reason:   "stable",
		}
	}
	return ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 5 * time.Second,
		Samples:  10,
		Reason:   "unstable",
	}
}

func (t *ThresholdGate) WithConfig(config StabilityConfig) Gate {
	return t
}
