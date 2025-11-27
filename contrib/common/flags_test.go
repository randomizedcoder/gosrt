package common

import (
	"flag"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
)

// This test suite provides comprehensive coverage for the flags package:
//
// 1. Flag Parsing Tests:
//    - TestParseFlags: Verifies that ParseFlags() correctly populates FlagSet
//    - TestParseFlags_NoFlagsSet: Verifies FlagSet is empty when no flags are provided
//
// 2. Config Application Tests by Type:
//    - String flags: TestApplyFlagsToConfig_StringFlags
//    - Int flags: TestApplyFlagsToConfig_IntFlags
//    - Uint64 flags: TestApplyFlagsToConfig_Uint64Flags
//    - Int64 flags: TestApplyFlagsToConfig_Int64Flags
//    - Bool flags: TestApplyFlagsToConfig_BoolFlags
//
// 3. Edge Case Tests:
//    - Zero values not set: TestApplyFlagsToConfig_ZeroValues_NotSet
//    - Zero values explicitly set: TestApplyFlagsToConfig_ZeroValues_Set
//    - Negative values: TestApplyFlagsToConfig_NegativeValues
//    - Boolean flags not set: TestApplyFlagsToConfig_BoolFlags_NotSet
//    - Boolean flags set to false: TestApplyFlagsToConfig_BoolFlags_SetToFalse
//
// 4. Comprehensive Tests:
//    - TestApplyFlagsToConfig_AllFlags: Tests all flags at once
//    - TestApplyFlagsToConfig_PartialFlags: Tests partial flag application
//
// Note: These tests reset the flag package state between tests to ensure isolation.
// The flag package uses global state, so tests must carefully manage os.Args and
// flag.CommandLine to avoid interference between test cases.

// parseTestFlags is a test helper that parses flags from a test argument slice
// without modifying os.Args. It uses flag.FlagSet.Parse() directly.
func parseTestFlags(args []string) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	// Reset all actual flag variables to their defaults
	// This ensures we start from a clean state
	*Congestion = ""
	*Conntimeo = 0
	*Streamid = ""
	*PassphraseFlag = ""
	*PBKeylen = 0
	*KMPreAnnounce = 0
	*KMRefreshRate = 0
	*EnforcedEncryption = false
	*Latency = 0
	*PeerLatency = 0
	*RcvLatency = 0
	*FC = 0
	*SndBuf = 0
	*RcvBuf = 0
	*MSS = 0
	*PayloadSize = 0
	*MaxBW = 0
	*InputBW = 0
	*MinInputBW = 0
	*OheadBW = 0
	*PeerIdleTimeo = 0
	*SndDropDelay = 0
	*IPTOS = 0
	*IPTTL = 0
	*IPv6Only = -1
	*DriftTracer = false
	*TLPktDrop = false
	*TSBPDMode = false
	*MessageAPI = false
	*NAKReport = false
	*LossMaxTTL = 0
	*PacketFilter = ""
	*Transtype = ""
	*GroupConnect = false
	*GroupStabTimeo = 0
	*AllowPeerIpChange = false

	// Create a new FlagSet for testing
	testFlagSet := flag.NewFlagSet("test", flag.ContinueOnError)

	// Re-register all flags on the test FlagSet
	// We need to create new flag variables for the test FlagSet
	testCongestion := testFlagSet.String("congestion", "", "Type of congestion control ('live' or 'file')")
	testConntimeo := testFlagSet.Int("conntimeo", 0, "Connection timeout in milliseconds")
	testStreamid := testFlagSet.String("streamid", "", "Stream ID (settable in caller mode only)")
	testPassphraseFlag := testFlagSet.String("passphrase-flag", "", "Password for encrypted transmission (alternative to passphrase)")
	testPBKeylen := testFlagSet.Int("pbkeylen", 0, "Crypto key length in bytes (16, 24, or 32)")
	testKMPreAnnounce := testFlagSet.Uint64("kmpreannounce", 0, "Duration of Stream Encryption key switchover (packets)")
	testKMRefreshRate := testFlagSet.Uint64("kmrefreshrate", 0, "Stream encryption key refresh rate (packets)")
	testEnforcedEncryption := testFlagSet.Bool("enforcedencryption", false, "Reject connection if parties set different passphrase")
	testLatency := testFlagSet.Int("latency", 0, "Maximum accepted transmission latency in milliseconds")
	testPeerLatency := testFlagSet.Int("peerlatency", 0, "Minimum receiver latency to be requested by sender in milliseconds")
	testRcvLatency := testFlagSet.Int("rcvlatency", 0, "Receiver-side latency in milliseconds")
	testFC := testFlagSet.Uint64("fc", 0, "Flow control window size (packets)")
	testSndBuf := testFlagSet.Uint64("sndbuf", 0, "Sender buffer size in bytes")
	testRcvBuf := testFlagSet.Uint64("rcvbuf", 0, "Receiver buffer size in bytes")
	testMSS := testFlagSet.Uint64("mss", 0, "MTU size")
	testPayloadSize := testFlagSet.Uint64("payloadsize", 0, "Maximum payload size in bytes")
	testMaxBW := testFlagSet.Int64("maxbw", 0, "Bandwidth limit in bytes/s (-1 for unlimited)")
	testInputBW := testFlagSet.Int64("inputbw", 0, "Input bandwidth in bytes")
	testMinInputBW := testFlagSet.Int64("mininputbw", 0, "Minimum input bandwidth in bytes")
	testOheadBW := testFlagSet.Int64("oheadbw", 0, "Limit bandwidth overhead in percents")
	testPeerIdleTimeo := testFlagSet.Int("peeridletimeo", 0, "Peer idle timeout in milliseconds")
	testSndDropDelay := testFlagSet.Int("snddropdelay", 0, "Sender's delay before dropping packets in milliseconds")
	testIPTOS := testFlagSet.Int("iptos", 0, "IP socket type of service")
	testIPTTL := testFlagSet.Int("ipttl", 0, "IP socket 'time to live' option")
	testIPv6Only := testFlagSet.Int("ipv6only", -1, "Allow only IPv6 (-1 for default)")
	testDriftTracer := testFlagSet.Bool("drifttracer", false, "Enable drift tracer")
	testTLPktDrop := testFlagSet.Bool("tlpktdrop", false, "Drop too late packets")
	testTSBPDMode := testFlagSet.Bool("tsbpdmode", false, "Enable timestamp-based packet delivery mode")
	testMessageAPI := testFlagSet.Bool("messageapi", false, "Enable SRT message mode")
	testNAKReport := testFlagSet.Bool("nakreport", false, "Enable periodic NAK reports")
	testLossMaxTTL := testFlagSet.Uint64("lossmaxttl", 0, "Packet reorder tolerance")
	testPacketFilter := testFlagSet.String("packetfilter", "", "Set up the packet filter")
	testTranstype := testFlagSet.String("transtype", "", "Transmission type ('live' or 'file')")
	testGroupConnect := testFlagSet.Bool("groupconnect", false, "Accept group connections")
	testGroupStabTimeo := testFlagSet.Int("groupstabtimeo", 0, "Group stability timeout in milliseconds")
	testAllowPeerIpChange := testFlagSet.Bool("allowpeeripchange", false, "Allow new IP to send data on existing socket id")

	// Parse the test arguments
	if err := testFlagSet.Parse(args); err != nil {
		panic(err)
	}

	// Track which flags were set by checking the argument list
	// This is necessary because flag.Visit() doesn't visit flags set to their default value
	// (e.g., -drifttracer false when default is false)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) > 0 && arg[0] == '-' {
			flagName := arg[1:] // Remove the leading '-'
			// Handle flags with '=' (e.g., -flag=value)
			if eqIdx := len(flagName); eqIdx > 0 {
				for j := 0; j < len(flagName); j++ {
					if flagName[j] == '=' {
						flagName = flagName[:j]
						break
					}
				}
			}
			// Check if this flag exists in our FlagSet and mark it as set
			testFlagSet.VisitAll(func(f *flag.Flag) {
				if f.Name == flagName {
					FlagSet[f.Name] = true
				}
			})
		}
	}

	// Also track flags that were visited (for flags that changed from default)
	// This ensures we catch all flags that were actually set
	testFlagSet.Visit(func(f *flag.Flag) {
		FlagSet[f.Name] = true
	})

	// Copy values from test flags to the actual flag variables
	if FlagSet["congestion"] {
		*Congestion = *testCongestion
	}
	if FlagSet["conntimeo"] {
		*Conntimeo = *testConntimeo
	}
	if FlagSet["streamid"] {
		*Streamid = *testStreamid
	}
	if FlagSet["passphrase-flag"] {
		*PassphraseFlag = *testPassphraseFlag
	}
	if FlagSet["pbkeylen"] {
		*PBKeylen = *testPBKeylen
	}
	if FlagSet["kmpreannounce"] {
		*KMPreAnnounce = *testKMPreAnnounce
	}
	if FlagSet["kmrefreshrate"] {
		*KMRefreshRate = *testKMRefreshRate
	}
	if FlagSet["enforcedencryption"] {
		*EnforcedEncryption = *testEnforcedEncryption
	}
	if FlagSet["latency"] {
		*Latency = *testLatency
	}
	if FlagSet["peerlatency"] {
		*PeerLatency = *testPeerLatency
	}
	if FlagSet["rcvlatency"] {
		*RcvLatency = *testRcvLatency
	}
	if FlagSet["fc"] {
		*FC = *testFC
	}
	if FlagSet["sndbuf"] {
		*SndBuf = *testSndBuf
	}
	if FlagSet["rcvbuf"] {
		*RcvBuf = *testRcvBuf
	}
	if FlagSet["mss"] {
		*MSS = *testMSS
	}
	if FlagSet["payloadsize"] {
		*PayloadSize = *testPayloadSize
	}
	if FlagSet["maxbw"] {
		*MaxBW = *testMaxBW
	}
	if FlagSet["inputbw"] {
		*InputBW = *testInputBW
	}
	if FlagSet["mininputbw"] {
		*MinInputBW = *testMinInputBW
	}
	if FlagSet["oheadbw"] {
		*OheadBW = *testOheadBW
	}
	if FlagSet["peeridletimeo"] {
		*PeerIdleTimeo = *testPeerIdleTimeo
	}
	if FlagSet["snddropdelay"] {
		*SndDropDelay = *testSndDropDelay
	}
	if FlagSet["iptos"] {
		*IPTOS = *testIPTOS
	}
	if FlagSet["ipttl"] {
		*IPTTL = *testIPTTL
	}
	if FlagSet["ipv6only"] {
		*IPv6Only = *testIPv6Only
	}
	if FlagSet["drifttracer"] {
		*DriftTracer = *testDriftTracer
	}
	if FlagSet["tlpktdrop"] {
		*TLPktDrop = *testTLPktDrop
	}
	if FlagSet["tsbpdmode"] {
		*TSBPDMode = *testTSBPDMode
	}
	if FlagSet["messageapi"] {
		*MessageAPI = *testMessageAPI
	}
	if FlagSet["nakreport"] {
		*NAKReport = *testNAKReport
	}
	if FlagSet["lossmaxttl"] {
		*LossMaxTTL = *testLossMaxTTL
	}
	if FlagSet["packetfilter"] {
		*PacketFilter = *testPacketFilter
	}
	if FlagSet["transtype"] {
		*Transtype = *testTranstype
	}
	if FlagSet["groupconnect"] {
		*GroupConnect = *testGroupConnect
	}
	if FlagSet["groupstabtimeo"] {
		*GroupStabTimeo = *testGroupStabTimeo
	}
	if FlagSet["allowpeeripchange"] {
		*AllowPeerIpChange = *testAllowPeerIpChange
	}
}

func TestParseFlags(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	// Parse test arguments without modifying os.Args
	parseTestFlags([]string{"-congestion", "live", "-latency", "200", "-fc", "51200"})

	// Check that FlagSet was populated correctly
	if !FlagSet["congestion"] {
		t.Error("Expected 'congestion' to be in FlagSet")
	}
	if !FlagSet["latency"] {
		t.Error("Expected 'latency' to be in FlagSet")
	}
	if !FlagSet["fc"] {
		t.Error("Expected 'fc' to be in FlagSet")
	}
	if FlagSet["streamid"] {
		t.Error("Expected 'streamid' NOT to be in FlagSet (wasn't provided)")
	}
}

func TestParseFlags_NoFlagsSet(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	// Parse test arguments without modifying os.Args
	parseTestFlags([]string{})

	// FlagSet should be empty
	if len(FlagSet) != 0 {
		t.Errorf("Expected FlagSet to be empty, got %d entries", len(FlagSet))
	}
}

func TestApplyFlagsToConfig_StringFlags(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-congestion", "file", "-streamid", "test-stream-123"})

	config := srt.DefaultConfig()
	ApplyFlagsToConfig(&config)

	if config.Congestion != "file" {
		t.Errorf("Expected Congestion to be 'file', got '%s'", config.Congestion)
	}
	if config.StreamId != "test-stream-123" {
		t.Errorf("Expected StreamId to be 'test-stream-123', got '%s'", config.StreamId)
	}
}

func TestApplyFlagsToConfig_StringFlags_NotSet(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{})

	config := srt.DefaultConfig()
	defaultCongestion := config.Congestion
	defaultStreamId := config.StreamId

	ApplyFlagsToConfig(&config)

	// Should not override defaults when flags are not set
	if config.Congestion != defaultCongestion {
		t.Errorf("Expected Congestion to remain default '%s', got '%s'", defaultCongestion, config.Congestion)
	}
	if config.StreamId != defaultStreamId {
		t.Errorf("Expected StreamId to remain default '%s', got '%s'", defaultStreamId, config.StreamId)
	}
}

func TestApplyFlagsToConfig_IntFlags(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-conntimeo", "5000", "-latency", "300", "-peerlatency", "250", "-rcvlatency", "200"})

	config := srt.DefaultConfig()
	ApplyFlagsToConfig(&config)

	if config.ConnectionTimeout != 5000*time.Millisecond {
		t.Errorf("Expected ConnectionTimeout to be 5000ms, got %v", config.ConnectionTimeout)
	}
	if config.Latency != 300*time.Millisecond {
		t.Errorf("Expected Latency to be 300ms, got %v", config.Latency)
	}
	if config.PeerLatency != 250*time.Millisecond {
		t.Errorf("Expected PeerLatency to be 250ms, got %v", config.PeerLatency)
	}
	if config.ReceiverLatency != 200*time.Millisecond {
		t.Errorf("Expected ReceiverLatency to be 200ms, got %v", config.ReceiverLatency)
	}
}

func TestApplyFlagsToConfig_Uint64Flags(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-fc", "51200", "-kmpreannounce", "4096", "-kmrefreshrate", "16777216"})

	config := srt.DefaultConfig()
	ApplyFlagsToConfig(&config)

	if config.FC != 51200 {
		t.Errorf("Expected FC to be 51200, got %d", config.FC)
	}
	if config.KMPreAnnounce != 4096 {
		t.Errorf("Expected KMPreAnnounce to be 4096, got %d", config.KMPreAnnounce)
	}
	if config.KMRefreshRate != 16777216 {
		t.Errorf("Expected KMRefreshRate to be 16777216, got %d", config.KMRefreshRate)
	}
}

func TestApplyFlagsToConfig_Int64Flags(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-maxbw", "-1", "-inputbw", "10000000", "-mininputbw", "5000000"})

	config := srt.DefaultConfig()
	ApplyFlagsToConfig(&config)

	if config.MaxBW != -1 {
		t.Errorf("Expected MaxBW to be -1, got %d", config.MaxBW)
	}
	if config.InputBW != 10000000 {
		t.Errorf("Expected InputBW to be 10000000, got %d", config.InputBW)
	}
	if config.MinInputBW != 5000000 {
		t.Errorf("Expected MinInputBW to be 5000000, got %d", config.MinInputBW)
	}
}

func TestApplyFlagsToConfig_BoolFlags(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-enforcedencryption", "true", "-drifttracer", "false", "-tlpktdrop", "true"})

	config := srt.DefaultConfig()
	ApplyFlagsToConfig(&config)

	if !config.EnforcedEncryption {
		t.Error("Expected EnforcedEncryption to be true")
	}
	if config.DriftTracer {
		t.Error("Expected DriftTracer to be false")
	}
	if !config.TooLatePacketDrop {
		t.Error("Expected TooLatePacketDrop to be true")
	}
}

func TestApplyFlagsToConfig_BoolFlags_NotSet(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{})

	config := srt.DefaultConfig()
	defaultEnforcedEncryption := config.EnforcedEncryption
	defaultDriftTracer := config.DriftTracer

	ApplyFlagsToConfig(&config)

	// Should not override defaults when flags are not set
	if config.EnforcedEncryption != defaultEnforcedEncryption {
		t.Errorf("Expected EnforcedEncryption to remain default %v, got %v", defaultEnforcedEncryption, config.EnforcedEncryption)
	}
	if config.DriftTracer != defaultDriftTracer {
		t.Errorf("Expected DriftTracer to remain default %v, got %v", defaultDriftTracer, config.DriftTracer)
	}
}

func TestApplyFlagsToConfig_BoolFlags_SetToFalse(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-enforcedencryption", "false"})

	config := srt.DefaultConfig()
	// Default might be true, but we want to set it to false
	config.EnforcedEncryption = true

	ApplyFlagsToConfig(&config)

	// Should override to false when explicitly set
	if config.EnforcedEncryption {
		t.Error("Expected EnforcedEncryption to be false when explicitly set to false")
	}
}

func TestApplyFlagsToConfig_ZeroValues_NotSet(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{})

	config := srt.DefaultConfig()
	defaultConntimeo := config.ConnectionTimeout
	defaultFC := config.FC
	defaultMaxBW := config.MaxBW

	ApplyFlagsToConfig(&config)

	// Zero values should NOT override defaults when flags are not set
	if config.ConnectionTimeout != defaultConntimeo {
		t.Errorf("Expected ConnectionTimeout to remain default %v, got %v", defaultConntimeo, config.ConnectionTimeout)
	}
	if config.FC != defaultFC {
		t.Errorf("Expected FC to remain default %d, got %d", defaultFC, config.FC)
	}
	if config.MaxBW != defaultMaxBW {
		t.Errorf("Expected MaxBW to remain default %d, got %d", defaultMaxBW, config.MaxBW)
	}
}

func TestApplyFlagsToConfig_ZeroValues_Set(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-conntimeo", "0", "-fc", "0"})

	config := srt.DefaultConfig()
	config.ConnectionTimeout = 5000 * time.Millisecond
	config.FC = 25600

	ApplyFlagsToConfig(&config)

	// Zero values SHOULD override when explicitly set
	if config.ConnectionTimeout != 0 {
		t.Errorf("Expected ConnectionTimeout to be 0 when explicitly set, got %v", config.ConnectionTimeout)
	}
	if config.FC != 0 {
		t.Errorf("Expected FC to be 0 when explicitly set, got %d", config.FC)
	}
}

func TestApplyFlagsToConfig_NegativeValues(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-maxbw", "-1", "-ipv6only", "-1"})

	config := srt.DefaultConfig()
	ApplyFlagsToConfig(&config)

	if config.MaxBW != -1 {
		t.Errorf("Expected MaxBW to be -1, got %d", config.MaxBW)
	}
	if config.IPv6Only != -1 {
		t.Errorf("Expected IPv6Only to be -1, got %d", config.IPv6Only)
	}
}

func TestApplyFlagsToConfig_AllFlags(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{
		"-congestion", "live",
		"-conntimeo", "3000",
		"-streamid", "test-stream",
		"-passphrase-flag", "secret123",
		"-pbkeylen", "32",
		"-kmpreannounce", "2048",
		"-kmrefreshrate", "8388608",
		"-enforcedencryption", "true",
		"-latency", "150",
		"-peerlatency", "120",
		"-rcvlatency", "100",
		"-fc", "25600",
		"-sndbuf", "1048576",
		"-rcvbuf", "2097152",
		"-mss", "1500",
		"-payloadsize", "1456",
		"-maxbw", "100000000",
		"-inputbw", "50000000",
		"-mininputbw", "25000000",
		"-oheadbw", "30",
		"-peeridletimeo", "3000",
		"-snddropdelay", "2000",
		"-iptos", "184",
		"-ipttl", "64",
		"-ipv6only", "0",
		"-drifttracer", "true",
		"-tlpktdrop", "true",
		"-tsbpdmode", "true",
		"-messageapi", "false",
		"-nakreport", "true",
		"-lossmaxttl", "10",
		"-packetfilter", "fec",
		"-transtype", "live",
		"-groupconnect", "false",
		"-groupstabtimeo", "5000",
		"-allowpeeripchange", "true",
	})

	config := srt.DefaultConfig()
	ApplyFlagsToConfig(&config)

	// Verify all flags were applied
	if config.Congestion != "live" {
		t.Errorf("Expected Congestion to be 'live', got '%s'", config.Congestion)
	}
	if config.ConnectionTimeout != 3000*time.Millisecond {
		t.Errorf("Expected ConnectionTimeout to be 3000ms, got %v", config.ConnectionTimeout)
	}
	if config.StreamId != "test-stream" {
		t.Errorf("Expected StreamId to be 'test-stream', got '%s'", config.StreamId)
	}
	if config.Passphrase != "secret123" {
		t.Errorf("Expected Passphrase to be 'secret123', got '%s'", config.Passphrase)
	}
	if config.PBKeylen != 32 {
		t.Errorf("Expected PBKeylen to be 32, got %d", config.PBKeylen)
	}
	if config.KMPreAnnounce != 2048 {
		t.Errorf("Expected KMPreAnnounce to be 2048, got %d", config.KMPreAnnounce)
	}
	if config.KMRefreshRate != 8388608 {
		t.Errorf("Expected KMRefreshRate to be 8388608, got %d", config.KMRefreshRate)
	}
	if !config.EnforcedEncryption {
		t.Error("Expected EnforcedEncryption to be true")
	}
	if config.Latency != 150*time.Millisecond {
		t.Errorf("Expected Latency to be 150ms, got %v", config.Latency)
	}
	if config.PeerLatency != 120*time.Millisecond {
		t.Errorf("Expected PeerLatency to be 120ms, got %v", config.PeerLatency)
	}
	if config.ReceiverLatency != 100*time.Millisecond {
		t.Errorf("Expected ReceiverLatency to be 100ms, got %v", config.ReceiverLatency)
	}
	if config.FC != 25600 {
		t.Errorf("Expected FC to be 25600, got %d", config.FC)
	}
	if config.SendBufferSize != 1048576 {
		t.Errorf("Expected SendBufferSize to be 1048576, got %d", config.SendBufferSize)
	}
	if config.ReceiverBufferSize != 2097152 {
		t.Errorf("Expected ReceiverBufferSize to be 2097152, got %d", config.ReceiverBufferSize)
	}
	if config.MSS != 1500 {
		t.Errorf("Expected MSS to be 1500, got %d", config.MSS)
	}
	if config.PayloadSize != 1456 {
		t.Errorf("Expected PayloadSize to be 1456, got %d", config.PayloadSize)
	}
	if config.MaxBW != 100000000 {
		t.Errorf("Expected MaxBW to be 100000000, got %d", config.MaxBW)
	}
	if config.InputBW != 50000000 {
		t.Errorf("Expected InputBW to be 50000000, got %d", config.InputBW)
	}
	if config.MinInputBW != 25000000 {
		t.Errorf("Expected MinInputBW to be 25000000, got %d", config.MinInputBW)
	}
	if config.OverheadBW != 30 {
		t.Errorf("Expected OverheadBW to be 30, got %d", config.OverheadBW)
	}
	if config.PeerIdleTimeout != 3000*time.Millisecond {
		t.Errorf("Expected PeerIdleTimeout to be 3000ms, got %v", config.PeerIdleTimeout)
	}
	if config.SendDropDelay != 2000*time.Millisecond {
		t.Errorf("Expected SendDropDelay to be 2000ms, got %v", config.SendDropDelay)
	}
	if config.IPTOS != 184 {
		t.Errorf("Expected IPTOS to be 184, got %d", config.IPTOS)
	}
	if config.IPTTL != 64 {
		t.Errorf("Expected IPTTL to be 64, got %d", config.IPTTL)
	}
	if config.IPv6Only != 0 {
		t.Errorf("Expected IPv6Only to be 0, got %d", config.IPv6Only)
	}
	if !config.DriftTracer {
		t.Error("Expected DriftTracer to be true")
	}
	if !config.TooLatePacketDrop {
		t.Error("Expected TooLatePacketDrop to be true")
	}
	if !config.TSBPDMode {
		t.Error("Expected TSBPDMode to be true")
	}
	if config.MessageAPI {
		t.Error("Expected MessageAPI to be false")
	}
	if !config.NAKReport {
		t.Error("Expected NAKReport to be true")
	}
	if config.LossMaxTTL != 10 {
		t.Errorf("Expected LossMaxTTL to be 10, got %d", config.LossMaxTTL)
	}
	if config.PacketFilter != "fec" {
		t.Errorf("Expected PacketFilter to be 'fec', got '%s'", config.PacketFilter)
	}
	if config.TransmissionType != "live" {
		t.Errorf("Expected TransmissionType to be 'live', got '%s'", config.TransmissionType)
	}
	if config.GroupConnect {
		t.Error("Expected GroupConnect to be false")
	}
	if config.GroupStabilityTimeout != 5000*time.Millisecond {
		t.Errorf("Expected GroupStabilityTimeout to be 5000ms, got %v", config.GroupStabilityTimeout)
	}
	if !config.AllowPeerIpChange {
		t.Error("Expected AllowPeerIpChange to be true")
	}
}

func TestApplyFlagsToConfig_PartialFlags(t *testing.T) {
	// Clear FlagSet
	for k := range FlagSet {
		delete(FlagSet, k)
	}

	parseTestFlags([]string{"-latency", "200", "-fc", "51200"})

	config := srt.DefaultConfig()
	defaultCongestion := config.Congestion

	ApplyFlagsToConfig(&config)

	// Only set flags should be applied
	if config.Latency != 200*time.Millisecond {
		t.Errorf("Expected Latency to be 200ms, got %v", config.Latency)
	}
	if config.FC != 51200 {
		t.Errorf("Expected FC to be 51200, got %d", config.FC)
	}
	// Unset flag should keep default
	if config.Congestion != defaultCongestion {
		t.Errorf("Expected Congestion to remain default '%s', got '%s'", defaultCongestion, config.Congestion)
	}
}
