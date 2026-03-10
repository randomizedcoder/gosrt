package common

import (
	"log"
	"time"

	srt "github.com/randomizedcoder/gosrt"
)

// FlagApplicator defines how a single CLI flag is applied to srt.Config.
// The table-driven approach reduces cyclomatic complexity from 116 to ~5.
type FlagApplicator struct {
	Name  string                   // Flag name (must match flag definition)
	Apply func(config *srt.Config) // Function to apply the flag value to config
}

// flagApplicators is the table of all flag applicators.
// Each entry maps a flag name to its application function.
// Organized by category for maintainability.
var flagApplicators = []FlagApplicator{
	// ════════════════════════════════════════════════════════════════════════
	// Connection Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"congestion", func(c *srt.Config) { c.Congestion = *Congestion }},
	{"conntimeo", func(c *srt.Config) { c.ConnectionTimeout = time.Duration(*Conntimeo) * time.Millisecond }},
	{"streamid", func(c *srt.Config) { c.StreamId = *Streamid }},
	{"passphrase-flag", func(c *srt.Config) { c.Passphrase = *PassphraseFlag }},
	{"pbkeylen", func(c *srt.Config) { c.PBKeylen = *PBKeylen }},
	{"kmpreannounce", func(c *srt.Config) { c.KMPreAnnounce = *KMPreAnnounce }},
	{"kmrefreshrate", func(c *srt.Config) { c.KMRefreshRate = *KMRefreshRate }},
	{"enforcedencryption", func(c *srt.Config) { c.EnforcedEncryption = *EnforcedEncryption }},
	{"latency", func(c *srt.Config) { c.Latency = time.Duration(*Latency) * time.Millisecond }},
	{"peerlatency", func(c *srt.Config) { c.PeerLatency = time.Duration(*PeerLatency) * time.Millisecond }},
	{"rcvlatency", func(c *srt.Config) { c.ReceiverLatency = time.Duration(*RcvLatency) * time.Millisecond }},
	{"fc", func(c *srt.Config) { c.FC = uint32(*FC) }},
	{"sndbuf", func(c *srt.Config) { c.SendBufferSize = uint32(*SndBuf) }},
	{"rcvbuf", func(c *srt.Config) { c.ReceiverBufferSize = uint32(*RcvBuf) }},
	{"mss", func(c *srt.Config) { c.MSS = uint32(*MSS) }},
	{"payloadsize", func(c *srt.Config) { c.PayloadSize = uint32(*PayloadSize) }},
	{"maxbw", func(c *srt.Config) { c.MaxBW = *MaxBW }},
	{"inputbw", func(c *srt.Config) { c.InputBW = *InputBW }},
	{"mininputbw", func(c *srt.Config) { c.MinInputBW = *MinInputBW }},
	{"oheadbw", func(c *srt.Config) { c.OverheadBW = *OheadBW }},
	{"peeridletimeo", func(c *srt.Config) { c.PeerIdleTimeout = time.Duration(*PeerIdleTimeo) * time.Millisecond }},
	{"keepalivethreshold", func(c *srt.Config) { c.KeepaliveThreshold = *KeepaliveThreshold }},
	{"snddropdelay", func(c *srt.Config) { c.SendDropDelay = time.Duration(*SndDropDelay) * time.Millisecond }},
	{"iptos", func(c *srt.Config) { c.IPTOS = *IPTOS }},
	{"ipttl", func(c *srt.Config) { c.IPTTL = *IPTTL }},
	{"ipv6only", func(c *srt.Config) { c.IPv6Only = *IPv6Only }},
	{"drifttracer", func(c *srt.Config) { c.DriftTracer = *DriftTracer }},
	{"tlpktdrop", func(c *srt.Config) { c.TooLatePacketDrop = *TLPktDrop }},
	{"tsbpdmode", func(c *srt.Config) { c.TSBPDMode = *TSBPDMode }},
	{"messageapi", func(c *srt.Config) { c.MessageAPI = *MessageAPI }},
	{"nakreport", func(c *srt.Config) { c.NAKReport = *NAKReport }},
	{"lossmaxttl", func(c *srt.Config) { c.LossMaxTTL = uint32(*LossMaxTTL) }},
	{"packetfilter", func(c *srt.Config) { c.PacketFilter = *PacketFilter }},
	{"transtype", func(c *srt.Config) { c.TransmissionType = *Transtype }},
	{"groupconnect", func(c *srt.Config) { c.GroupConnect = *GroupConnect }},
	{"groupstabtimeo", func(c *srt.Config) { c.GroupStabilityTimeout = time.Duration(*GroupStabTimeo) * time.Millisecond }},
	{"allowpeeripchange", func(c *srt.Config) { c.AllowPeerIpChange = *AllowPeerIpChange }},

	// ════════════════════════════════════════════════════════════════════════
	// io_uring Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"iouringenabled", func(c *srt.Config) { c.IoUringEnabled = *IoUringEnabled }},
	{"iouringsendringsize", func(c *srt.Config) { c.IoUringSendRingSize = *IoUringSendRingSize }},
	{"iouringrecvenabled", func(c *srt.Config) { c.IoUringRecvEnabled = *IoUringRecvEnabled }},
	{"iouringrecvringsize", func(c *srt.Config) { c.IoUringRecvRingSize = *IoUringRecvRingSize }},
	{"iouringrecvinitialpending", func(c *srt.Config) { c.IoUringRecvInitialPending = *IoUringRecvInitialPending }},
	{"iouringrecvbatchsize", func(c *srt.Config) { c.IoUringRecvBatchSize = *IoUringRecvBatchSize }},
	{"iouringrecvringcount", func(c *srt.Config) { c.IoUringRecvRingCount = *IoUringRecvRingCount }},
	{"iouringsendringcount", func(c *srt.Config) { c.IoUringSendRingCount = *IoUringSendRingCount }},

	// ════════════════════════════════════════════════════════════════════════
	// Channel Buffer Size Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"networkqueuesize", func(c *srt.Config) { c.NetworkQueueSize = *NetworkQueueSize }},
	{"writequeuesize", func(c *srt.Config) { c.WriteQueueSize = *WriteQueueSize }},
	{"readqueuesize", func(c *srt.Config) { c.ReadQueueSize = *ReadQueueSize }},
	{"receivequeuesize", func(c *srt.Config) { c.ReceiveQueueSize = *ReceiveQueueSize }},

	// ════════════════════════════════════════════════════════════════════════
	// Packet Reordering Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"packetreorderalgorithm", func(c *srt.Config) { c.PacketReorderAlgorithm = *PacketReorderAlgorithm }},
	{"btreedegree", func(c *srt.Config) { c.BTreeDegree = *BTreeDegree }},

	// ════════════════════════════════════════════════════════════════════════
	// Timer Interval Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"tickintervalms", func(c *srt.Config) { c.TickIntervalMs = *TickIntervalMs }},
	{"periodicnakintervalms", func(c *srt.Config) { c.PeriodicNakIntervalMs = *PeriodicNakIntervalMs }},
	{"periodicackintervalms", func(c *srt.Config) { c.PeriodicAckIntervalMs = *PeriodicAckIntervalMs }},
	{"senddropintervalms", func(c *srt.Config) { c.SendDropIntervalMs = *SendDropIntervalMs }},
	{"eventlooprateintervalms", func(c *srt.Config) { c.EventLoopRateIntervalMs = *EventLoopRateIntervalMs }},

	// ════════════════════════════════════════════════════════════════════════
	// NAK Btree Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"usenakbtree", func(c *srt.Config) { c.UseNakBtree = *UseNakBtree }},
	{"nakrecentpercent", func(c *srt.Config) { c.NakRecentPercent = *NakRecentPercent }},
	{"nakmergegap", func(c *srt.Config) { c.NakMergeGap = uint32(*NakMergeGap) }},
	{"nakconsolidationbudgetms", func(c *srt.Config) { c.NakConsolidationBudgetUs = *NakConsolidationBudgetMs * 1000 }},

	// ════════════════════════════════════════════════════════════════════════
	// FastNAK Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"fastnakenabled", func(c *srt.Config) { c.FastNakEnabled = *FastNakEnabled }},
	{"fastnakthresholdms", func(c *srt.Config) { c.FastNakThresholdMs = *FastNakThresholdMs }},
	{"fastnakrecentenabled", func(c *srt.Config) { c.FastNakRecentEnabled = *FastNakRecentEnabled }},

	// ════════════════════════════════════════════════════════════════════════
	// Sender Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"honornakorder", func(c *srt.Config) { c.HonorNakOrder = *HonorNakOrder }},

	// ════════════════════════════════════════════════════════════════════════
	// Sender Lockless Configuration Flags (Phase 1: Lockless Sender)
	// ════════════════════════════════════════════════════════════════════════
	{"usesendbtree", func(c *srt.Config) { c.UseSendBtree = *UseSendBtree }},
	{"sendbtreesize", func(c *srt.Config) { c.SendBtreeDegree = *SendBtreeDegree }},

	// ════════════════════════════════════════════════════════════════════════
	// Sender Lock-Free Ring Configuration Flags (Phase 2: Lockless Sender)
	// ════════════════════════════════════════════════════════════════════════
	{"usesendring", func(c *srt.Config) { c.UseSendRing = *UseSendRing }},
	{"sendringsize", func(c *srt.Config) { c.SendRingSize = *SendRingSize }},
	{"sendringshards", func(c *srt.Config) { c.SendRingShards = *SendRingShards }},

	// ════════════════════════════════════════════════════════════════════════
	// Sender Control Ring Configuration Flags (Phase 3: Lockless Sender)
	// ════════════════════════════════════════════════════════════════════════
	{"usesendcontrolring", func(c *srt.Config) { c.UseSendControlRing = *UseSendControlRing }},
	{"sendcontrolringsize", func(c *srt.Config) { c.SendControlRingSize = *SendControlRingSize }},
	{"sendcontrolringshards", func(c *srt.Config) { c.SendControlRingShards = *SendControlRingShards }},

	// ════════════════════════════════════════════════════════════════════════
	// Receiver Control Ring Configuration Flags (Completely Lock-Free Receiver)
	// ════════════════════════════════════════════════════════════════════════
	{"userecvcontrolring", func(c *srt.Config) { c.UseRecvControlRing = *UseRecvControlRing }},
	{"recvcontrolringsize", func(c *srt.Config) { c.RecvControlRingSize = *RecvControlRingSize }},
	{"recvcontrolringshards", func(c *srt.Config) { c.RecvControlRingShards = *RecvControlRingShards }},

	// ════════════════════════════════════════════════════════════════════════
	// Sender EventLoop Configuration Flags (Phase 4: Lockless Sender)
	// ════════════════════════════════════════════════════════════════════════
	{"usesendeventloop", func(c *srt.Config) { c.UseSendEventLoop = *UseSendEventLoop }},
	{"sendeventloopbackoffminsleep", func(c *srt.Config) { c.SendEventLoopBackoffMinSleep = *SendEventLoopBackoffMinSleep }},
	{"sendeventloopbackoffmaxsleep", func(c *srt.Config) { c.SendEventLoopBackoffMaxSleep = *SendEventLoopBackoffMaxSleep }},
	{"sendtsbpdsleepfactor", func(c *srt.Config) { c.SendTsbpdSleepFactor = *SendTsbpdSleepFactor }},
	{"senddropthresholdus", func(c *srt.Config) { c.SendDropThresholdUs = *SendDropThresholdUs }},

	// ════════════════════════════════════════════════════════════════════════
	// Adaptive Backoff Flags (adaptive_eventloop_mode_design.md)
	// ════════════════════════════════════════════════════════════════════════
	{"useadaptivebackoff", func(c *srt.Config) { c.UseAdaptiveBackoff = *UseAdaptiveBackoff }},
	{"adaptivebackoffidlethreshold", func(c *srt.Config) { c.AdaptiveBackoffIdleThreshold = *AdaptiveBackoffIdleThreshold }},

	// ════════════════════════════════════════════════════════════════════════
	// EventLoop Tight Loop Flags (eventloop_batch_sizing_design.md)
	// ════════════════════════════════════════════════════════════════════════
	{"eventloopmaxdata", func(c *srt.Config) { c.EventLoopMaxDataPerIteration = *EventLoopMaxDataPerIteration }},

	// ════════════════════════════════════════════════════════════════════════
	// Lock-Free Ring Buffer Configuration Flags (Phase 3: Lockless Design)
	// ════════════════════════════════════════════════════════════════════════
	{"usepacketring", func(c *srt.Config) { c.UsePacketRing = *UsePacketRing }},
	{"packetringsize", func(c *srt.Config) { c.PacketRingSize = *PacketRingSize }},
	{"packetringshards", func(c *srt.Config) { c.PacketRingShards = *PacketRingShards }},
	{"packetringbackoffduration", func(c *srt.Config) { c.PacketRingBackoffDuration = *PacketRingBackoffDuration }},
	{"packetringretrystrategy", func(c *srt.Config) { c.PacketRingRetryStrategy = *PacketRingRetryStrategy }},

	// ════════════════════════════════════════════════════════════════════════
	// Event Loop Configuration Flags (Phase 4: Lockless Design)
	// ════════════════════════════════════════════════════════════════════════
	{"useeventloop", func(c *srt.Config) { c.UseEventLoop = *UseEventLoop }},
	{"eventlooprateinterval", func(c *srt.Config) { c.EventLoopRateInterval = *EventLoopRateInterval }},
	{"backoffminsleep", func(c *srt.Config) { c.BackoffMinSleep = *BackoffMinSleep }},
	{"backoffmaxsleep", func(c *srt.Config) { c.BackoffMaxSleep = *BackoffMaxSleep }},

	// ════════════════════════════════════════════════════════════════════════
	// Debug Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"receiverdebug", func(c *srt.Config) { c.ReceiverDebug = *ReceiverDebug }},

	// ════════════════════════════════════════════════════════════════════════
	// Statistics Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"statisticsinterval", func(c *srt.Config) { c.StatisticsPrintInterval = *StatisticsPrintInterval }},

	// ════════════════════════════════════════════════════════════════════════
	// Timeout and Shutdown Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"handshaketimeout", func(c *srt.Config) { c.HandshakeTimeout = *HandshakeTimeout }},
	{"shutdowndelay", func(c *srt.Config) { c.ShutdownDelay = *ShutdownDelay }},

	// ════════════════════════════════════════════════════════════════════════
	// Local Address Configuration Flags
	// ════════════════════════════════════════════════════════════════════════
	{"localaddr", func(c *srt.Config) { c.LocalAddr = *LocalAddr }},
	{"name", func(c *srt.Config) { c.InstanceName = *InstanceName }},
}

// conditionalFlagApplicators require special handling (conditionals, validation).
// These are applied separately from the main table.
type conditionalFlagApplicator struct {
	Name      string
	Condition func() bool
	Apply     func(config *srt.Config)
}

var conditionalFlagApplicators = []conditionalFlagApplicator{
	// ValidateSendPayloadSize uses value check instead of FlagSet
	{
		Name:      "validatesendpayloadsize",
		Condition: func() bool { return *ValidateSendPayloadSize },
		Apply:     func(c *srt.Config) { c.ValidateSendPayloadSize = true },
	},
	// PacketRingMaxRetries requires >= 0 check
	{
		Name:      "packetringmaxretries",
		Condition: func() bool { return FlagSet["packetringmaxretries"] && *PacketRingMaxRetries >= 0 },
		Apply:     func(c *srt.Config) { c.PacketRingMaxRetries = *PacketRingMaxRetries },
	},
	// PacketRingMaxBackoffs requires >= 0 check
	{
		Name:      "packetringmaxbackoffs",
		Condition: func() bool { return FlagSet["packetringmaxbackoffs"] && *PacketRingMaxBackoffs >= 0 },
		Apply:     func(c *srt.Config) { c.PacketRingMaxBackoffs = *PacketRingMaxBackoffs },
	},
	// BackoffColdStartPkts requires >= 0 check
	{
		Name:      "backoffcoldstartpkts",
		Condition: func() bool { return FlagSet["backoffcoldstartpkts"] && *BackoffColdStartPkts >= 0 },
		Apply:     func(c *srt.Config) { c.BackoffColdStartPkts = *BackoffColdStartPkts },
	},
	// LightACKDifference requires > 0 check
	{
		Name:      "lightackdifference",
		Condition: func() bool { return FlagSet["lightackdifference"] && *LightACKDifference > 0 },
		Apply:     func(c *srt.Config) { c.LightACKDifference = uint32(*LightACKDifference) },
	},
	// NakExpiryMargin requires validation
	{
		Name:      "nakexpirymargin",
		Condition: func() bool { return FlagSet["nakexpirymargin"] },
		Apply: func(c *srt.Config) {
			c.NakExpiryMargin = *NakExpiryMargin
			// Validate: values < -1.0 would create threshold in the past
			if c.NakExpiryMargin < -1.0 {
				log.Printf("WARNING: NakExpiryMargin %.2f invalid (< -1.0), resetting to 0.10", c.NakExpiryMargin)
				c.NakExpiryMargin = 0.10
			}
		},
	},
	// EWMAWarmupThreshold
	{
		Name:      "ewmawarmupthreshold",
		Condition: func() bool { return FlagSet["ewmawarmupthreshold"] },
		Apply:     func(c *srt.Config) { c.EWMAWarmupThreshold = uint32(*EWMAWarmupThreshold) },
	},
	// RTOMode requires enum conversion
	{
		Name:      "rtomode",
		Condition: func() bool { return FlagSet["rtomode"] },
		Apply: func(c *srt.Config) {
			switch *RTOMode {
			case "rtt_rttvar":
				c.RTOMode = srt.RTORttRttVar
			case "rtt_4rttvar":
				c.RTOMode = srt.RTORtt4RttVar
			case "rtt_rttvar_margin":
				c.RTOMode = srt.RTORttRttVarMargin
			}
		},
	},
	// ExtraRTTMargin
	{
		Name:      "extrarttmargin",
		Condition: func() bool { return FlagSet["extrarttmargin"] },
		Apply:     func(c *srt.Config) { c.ExtraRTTMargin = *ExtraRTTMargin },
	},
}

// applyFlagsToConfigTable applies CLI flag values using the table-driven approach.
// Only flags that were explicitly set (tracked in FlagSet map) override the default config.
// This reduces cyclomatic complexity from 116 to ~10.
func applyFlagsToConfigTable(config *srt.Config) {
	// Apply standard flags from table
	for _, fa := range flagApplicators {
		if FlagSet[fa.Name] {
			fa.Apply(config)
		}
	}

	// Apply conditional flags
	for _, cfa := range conditionalFlagApplicators {
		if cfa.Condition() {
			cfa.Apply(config)
		}
	}
}

// ResetFlagSet clears the FlagSet map for testing purposes.
func ResetFlagSet() {
	FlagSet = make(map[string]bool)
}

// GetFlagApplicatorCount returns the number of flag applicators for testing.
func GetFlagApplicatorCount() int {
	return len(flagApplicators)
}

// GetConditionalFlagApplicatorCount returns the number of conditional flag applicators for testing.
func GetConditionalFlagApplicatorCount() int {
	return len(conditionalFlagApplicators)
}
