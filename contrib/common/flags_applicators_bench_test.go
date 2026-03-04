package common

import (
	"testing"

	srt "github.com/randomizedcoder/gosrt"
)

// BenchmarkTableDriven_NoFlags benchmarks table-driven flag application with no flags set.
func BenchmarkTableDriven_NoFlags(b *testing.B) {
	ResetFlagSet()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		config := srt.DefaultConfig()
		applyFlagsToConfigTable(&config)
	}
}

// BenchmarkTableDriven_FewFlags benchmarks table-driven flag application with a few flags set.
func BenchmarkTableDriven_FewFlags(b *testing.B) {
	ResetFlagSet()
	*Latency = 200
	FlagSet["latency"] = true
	*FC = 102400
	FlagSet["fc"] = true
	*UseEventLoop = true
	FlagSet["useeventloop"] = true

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		config := srt.DefaultConfig()
		applyFlagsToConfigTable(&config)
	}
}

// BenchmarkTableDriven_ManyFlags benchmarks table-driven flag application with many flags set.
func BenchmarkTableDriven_ManyFlags(b *testing.B) {
	ResetFlagSet()

	// Set many flags
	*Latency = 200
	FlagSet["latency"] = true
	*FC = 102400
	FlagSet["fc"] = true
	*UseEventLoop = true
	FlagSet["useeventloop"] = true
	*UsePacketRing = true
	FlagSet["usepacketring"] = true
	*UseSendBtree = true
	FlagSet["usesendbtree"] = true
	*UseSendRing = true
	FlagSet["usesendring"] = true
	*UseSendControlRing = true
	FlagSet["usesendcontrolring"] = true
	*UseSendEventLoop = true
	FlagSet["usesendeventloop"] = true
	*IoUringEnabled = true
	FlagSet["iouringenabled"] = true
	*IoUringRecvEnabled = true
	FlagSet["iouringrecvenabled"] = true
	*UseNakBtree = true
	FlagSet["usenakbtree"] = true
	*TickIntervalMs = 5
	FlagSet["tickintervalms"] = true
	*PeriodicNakIntervalMs = 20
	FlagSet["periodicnakintervalms"] = true
	*PeriodicAckIntervalMs = 10
	FlagSet["periodicackintervalms"] = true
	*ReceiverDebug = true
	FlagSet["receiverdebug"] = true

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		config := srt.DefaultConfig()
		applyFlagsToConfigTable(&config)
	}
}

// BenchmarkTableDriven_AllFlags benchmarks table-driven flag application with all flags set.
func BenchmarkTableDriven_AllFlags(b *testing.B) {
	ResetFlagSet()

	// Mark all flags as set
	for _, fa := range flagApplicators {
		FlagSet[fa.Name] = true
	}
	for _, cfa := range conditionalFlagApplicators {
		// Set up conditions to pass
		switch cfa.Name {
		case "packetringmaxretries":
			*PacketRingMaxRetries = 10
		case "packetringmaxbackoffs":
			*PacketRingMaxBackoffs = 5
		case "backoffcoldstartpkts":
			*BackoffColdStartPkts = 1000
		case "lightackdifference":
			*LightACKDifference = 64
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		config := srt.DefaultConfig()
		applyFlagsToConfigTable(&config)
	}
}

// BenchmarkFlagTableIteration measures the overhead of iterating through the flag table.
func BenchmarkFlagTableIteration(b *testing.B) {
	ResetFlagSet()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		count := 0
		for _, fa := range flagApplicators {
			if FlagSet[fa.Name] {
				count++
			}
		}
		_ = count
	}
}

// BenchmarkFlagMapLookup measures map lookup performance for FlagSet.
func BenchmarkFlagMapLookup(b *testing.B) {
	ResetFlagSet()
	FlagSet["latency"] = true
	FlagSet["fc"] = true
	FlagSet["useeventloop"] = true

	flagNames := []string{"latency", "fc", "useeventloop", "nonexistent", "mss"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, name := range flagNames {
			_ = FlagSet[name]
		}
	}
}
