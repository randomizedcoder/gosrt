# CLI Arguments Documentation

This document describes how to update `contrib/client/main.go` and `contrib/server/main.go` to support CLI flags for all SRT connection configuration options.

## Overview

Both client and server programs currently use `srt.DefaultConfig()` with minimal CLI flag support. This document outlines the changes needed to:

1. Create a shared package (`contrib/common/flags.go`) containing all SRT connection configuration flags
2. Add CLI flags for all `Config` struct fields in the shared package
3. Keep application-specific flags (client/server) in their respective `main.go` files
4. Apply flag values to the configuration before creating connections
5. Maintain consistency between client and server implementations using the shared package

**Benefits of the Shared Package Approach:**
- **DRY (Don't Repeat Yourself)**: Flag declarations and logic are defined once
- **Consistency**: Both applications use the same flag names and behavior
- **Maintainability**: Changes only need to be made in one place
- **Type Safety**: Go's package system ensures proper access

## Configuration Fields

The `srt.Config` struct contains the following fields that should be configurable via CLI flags:

### Connection Settings
- `Congestion` (string): Type of congestion control ('live' or 'file')
- `ConnectionTimeout` (time.Duration): Connection timeout
- `StreamId` (string): Stream ID (settable in caller mode only)

### Encryption Settings
- `Passphrase` (string): Password for encrypted transmission
- `PBKeylen` (int): Crypto key length in bytes (16, 24, or 32)
- `KMPreAnnounce` (uint64): Duration of Stream Encryption key switchover (packets)
- `KMRefreshRate` (uint64): Stream encryption key refresh rate (packets)
- `EnforcedEncryption` (bool): Reject connection if parties set different passphrase

### Latency Settings
- `Latency` (time.Duration): Maximum accepted transmission latency (overrides PeerLatency and ReceiverLatency)
- `PeerLatency` (time.Duration): Minimum receiver latency to be requested by sender
- `ReceiverLatency` (time.Duration): Receiver-side latency

### Buffer Settings
- `FC` (uint32): Flow control window size (packets)
- `SendBufferSize` (uint32): Sender buffer size (bytes)
- `ReceiverBufferSize` (uint32): Receiver buffer size (bytes)
- `MSS` (uint32): MTU size
- `PayloadSize` (uint32): Maximum payload size (bytes)

### Bandwidth Settings
- `MaxBW` (int64): Bandwidth limit (bytes/s, -1 for unlimited)
- `InputBW` (int64): Input bandwidth (bytes)
- `MinInputBW` (int64): Minimum input bandwidth
- `OverheadBW` (int64): Limit bandwidth overhead (percents)

### Timeout Settings
- `PeerIdleTimeout` (time.Duration): Peer idle timeout
- `SendDropDelay` (time.Duration): Sender's delay before dropping packets

### Network Settings
- `IPTOS` (int): IP socket type of service
- `IPTTL` (int): IP socket "time to live" option
- `IPv6Only` (int): Allow only IPv6 (-1 for default)

### Feature Flags
- `DriftTracer` (bool): Enable drift tracer
- `TooLatePacketDrop` (bool): Drop too late packets
- `TSBPDMode` (bool): Timestamp-based packet delivery mode
- `MessageAPI` (bool): Enable SRT message mode
- `NAKReport` (bool): Enable periodic NAK reports

### Advanced Settings
- `LossMaxTTL` (uint32): Packet reorder tolerance
- `PacketFilter` (string): Set up the packet filter
- `TransmissionType` (string): Transmission type ('live' or 'file')
- `GroupConnect` (bool): Accept group connections
- `GroupStabilityTimeout` (time.Duration): Group stability timeout
- `AllowPeerIpChange` (bool): Allow new IP to send data on existing socket id

## Implementation Steps

To follow DRY (Don't Repeat Yourself) principles, we'll create a shared package that contains all the flag declarations and helper functions. Both `client/main.go` and `server/main.go` will import and use this shared package.

### Step 1: Create Shared Flags Package

Create a new file `contrib/common/flags.go` that will contain all flag declarations and helper functions shared between client and server:

```go
package common

import (
	"flag"
	"time"

	srt "github.com/datarhei/gosrt"
)

var (
	// Map to track which flags were explicitly set by the user
	FlagSet = make(map[string]bool)

	// Connection configuration flags (shared between client and server)
	Congestion         = flag.String("congestion", "", "Type of congestion control ('live' or 'file')")
	Conntimeo          = flag.Int("conntimeo", 0, "Connection timeout in milliseconds")
	Streamid           = flag.String("streamid", "", "Stream ID (settable in caller mode only)")
	PassphraseFlag     = flag.String("passphrase-flag", "", "Password for encrypted transmission (alternative to passphrase)")
	PBKeylen           = flag.Int("pbkeylen", 0, "Crypto key length in bytes (16, 24, or 32)")
	KMPreAnnounce      = flag.Uint64("kmpreannounce", 0, "Duration of Stream Encryption key switchover (packets)")
	KMRefreshRate      = flag.Uint64("kmrefreshrate", 0, "Stream encryption key refresh rate (packets)")
	EnforcedEncryption = flag.Bool("enforcedencryption", false, "Reject connection if parties set different passphrase")
	Latency            = flag.Int("latency", 0, "Maximum accepted transmission latency in milliseconds")
	PeerLatency        = flag.Int("peerlatency", 0, "Minimum receiver latency to be requested by sender in milliseconds")
	RcvLatency         = flag.Int("rcvlatency", 0, "Receiver-side latency in milliseconds")
	FC                 = flag.Uint64("fc", 0, "Flow control window size (packets)")
	SndBuf             = flag.Uint64("sndbuf", 0, "Sender buffer size in bytes")
	RcvBuf             = flag.Uint64("rcvbuf", 0, "Receiver buffer size in bytes")
	MSS                = flag.Uint64("mss", 0, "MTU size")
	PayloadSize        = flag.Uint64("payloadsize", 0, "Maximum payload size in bytes")
	MaxBW              = flag.Int64("maxbw", 0, "Bandwidth limit in bytes/s (-1 for unlimited)")
	InputBW            = flag.Int64("inputbw", 0, "Input bandwidth in bytes")
	MinInputBW         = flag.Int64("mininputbw", 0, "Minimum input bandwidth in bytes")
	OheadBW            = flag.Int64("oheadbw", 0, "Limit bandwidth overhead in percents")
	PeerIdleTimeo      = flag.Int("peeridletimeo", 0, "Peer idle timeout in milliseconds")
	SndDropDelay       = flag.Int("snddropdelay", 0, "Sender's delay before dropping packets in milliseconds")
	IPTOS              = flag.Int("iptos", 0, "IP socket type of service")
	IPTTL              = flag.Int("ipttl", 0, "IP socket 'time to live' option")
	IPv6Only           = flag.Int("ipv6only", -1, "Allow only IPv6 (-1 for default)")
	DriftTracer        = flag.Bool("drifttracer", false, "Enable drift tracer")
	TLPktDrop          = flag.Bool("tlpktdrop", false, "Drop too late packets")
	TSBPDMode          = flag.Bool("tsbpdmode", false, "Enable timestamp-based packet delivery mode")
	MessageAPI         = flag.Bool("messageapi", false, "Enable SRT message mode")
	NAKReport          = flag.Bool("nakreport", false, "Enable periodic NAK reports")
	LossMaxTTL         = flag.Uint64("lossmaxttl", 0, "Packet reorder tolerance")
	PacketFilter       = flag.String("packetfilter", "", "Set up the packet filter")
	Transtype          = flag.String("transtype", "", "Transmission type ('live' or 'file')")
	GroupConnect       = flag.Bool("groupconnect", false, "Accept group connections")
	GroupStabTimeo     = flag.Int("groupstabtimeo", 0, "Group stability timeout in milliseconds")
	AllowPeerIpChange  = flag.Bool("allowpeeripchange", false, "Allow new IP to send data on existing socket id")
)

// ParseFlags parses command-line flags and populates FlagSet map
// with flags that were explicitly set by the user.
func ParseFlags() {
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		FlagSet[f.Name] = true
	})
}

// ApplyFlagsToConfig applies CLI flag values to the provided config.
// Only flags that were explicitly set (tracked in FlagSet map) override the default config.
func ApplyFlagsToConfig(config *srt.Config) {
	if FlagSet["congestion"] {
		config.Congestion = *Congestion
	}
	if FlagSet["conntimeo"] {
		config.ConnectionTimeout = time.Duration(*Conntimeo) * time.Millisecond
	}
	if FlagSet["streamid"] {
		config.StreamId = *Streamid
	}
	if FlagSet["passphrase-flag"] {
		config.Passphrase = *PassphraseFlag
	}
	if FlagSet["pbkeylen"] {
		config.PBKeylen = *PBKeylen
	}
	if FlagSet["kmpreannounce"] {
		config.KMPreAnnounce = *KMPreAnnounce
	}
	if FlagSet["kmrefreshrate"] {
		config.KMRefreshRate = *KMRefreshRate
	}
	if FlagSet["enforcedencryption"] {
		config.EnforcedEncryption = *EnforcedEncryption
	}
	if FlagSet["latency"] {
		config.Latency = time.Duration(*Latency) * time.Millisecond
	}
	if FlagSet["peerlatency"] {
		config.PeerLatency = time.Duration(*PeerLatency) * time.Millisecond
	}
	if FlagSet["rcvlatency"] {
		config.ReceiverLatency = time.Duration(*RcvLatency) * time.Millisecond
	}
	if FlagSet["fc"] {
		config.FC = uint32(*FC)
	}
	if FlagSet["sndbuf"] {
		config.SendBufferSize = uint32(*SndBuf)
	}
	if FlagSet["rcvbuf"] {
		config.ReceiverBufferSize = uint32(*RcvBuf)
	}
	if FlagSet["mss"] {
		config.MSS = uint32(*MSS)
	}
	if FlagSet["payloadsize"] {
		config.PayloadSize = uint32(*PayloadSize)
	}
	if FlagSet["maxbw"] {
		config.MaxBW = *MaxBW
	}
	if FlagSet["inputbw"] {
		config.InputBW = *InputBW
	}
	if FlagSet["mininputbw"] {
		config.MinInputBW = *MinInputBW
	}
	if FlagSet["oheadbw"] {
		config.OverheadBW = *OheadBW
	}
	if FlagSet["peeridletimeo"] {
		config.PeerIdleTimeout = time.Duration(*PeerIdleTimeo) * time.Millisecond
	}
	if FlagSet["snddropdelay"] {
		config.SendDropDelay = time.Duration(*SndDropDelay) * time.Millisecond
	}
	if FlagSet["iptos"] {
		config.IPTOS = *IPTOS
	}
	if FlagSet["ipttl"] {
		config.IPTTL = *IPTTL
	}
	if FlagSet["ipv6only"] {
		config.IPv6Only = *IPv6Only
	}
	if FlagSet["drifttracer"] {
		config.DriftTracer = *DriftTracer
	}
	if FlagSet["tlpktdrop"] {
		config.TooLatePacketDrop = *TLPktDrop
	}
	if FlagSet["tsbpdmode"] {
		config.TSBPDMode = *TSBPDMode
	}
	if FlagSet["messageapi"] {
		config.MessageAPI = *MessageAPI
	}
	if FlagSet["nakreport"] {
		config.NAKReport = *NAKReport
	}
	if FlagSet["lossmaxttl"] {
		config.LossMaxTTL = uint32(*LossMaxTTL)
	}
	if FlagSet["packetfilter"] {
		config.PacketFilter = *PacketFilter
	}
	if FlagSet["transtype"] {
		config.TransmissionType = *Transtype
	}
	if FlagSet["groupconnect"] {
		config.GroupConnect = *GroupConnect
	}
	if FlagSet["groupstabtimeo"] {
		config.GroupStabilityTimeout = time.Duration(*GroupStabTimeo) * time.Millisecond
	}
	if FlagSet["allowpeeripchange"] {
		config.AllowPeerIpChange = *AllowPeerIpChange
	}
}
```

### Step 2: Add Application-Specific Flags

Each application (client and server) will have its own flags. Add these in their respective `main.go` files:

**In `contrib/client/main.go`:**

```go
package main

import (
	// ... other imports
	"github.com/datarhei/gosrt/contrib/common"
)

var (
	// Client-specific flags
	from      = flag.String("from", "", "Address to read from, sources: srt://, udp://, - (stdin)")
	to        = flag.String("to", "", "Address to write to, targets: srt://, udp://, file://, - (stdout)")
	logtopics = flag.String("logtopics", "", "topics for the log output")
)
```

**In `contrib/server/main.go`:**

```go
package main

import (
	// ... other imports
	"github.com/datarhei/gosrt/contrib/common"
)

var (
	// Server-specific flags
	addr       = flag.String("addr", "", "address to listen on")
	app        = flag.String("app", "", "path prefix for streamid")
	token      = flag.String("token", "", "token query param for streamid")
	passphrase = flag.String("passphrase", "", "passphrase for de- and encrypting the data")
	profile    = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
)
```

### Step 3: Update main() Functions

Update both `main()` functions to use the shared package:

```go
var (
	// Map to track which flags were explicitly set by the user
	flagSet = make(map[string]bool)

	// Variables to hold CLI arguments for existing functionality
	from      = flag.String("from", "", "Address to read from, sources: srt://, udp://, - (stdin)")
	to        = flag.String("to", "", "Address to write to, targets: srt://, udp://, file://, - (stdout)")
	logtopics = flag.String("logtopics", "", "topics for the log output")

	// Server-specific flags (for server/main.go only)
	addr       = flag.String("addr", "", "address to listen on")
	app        = flag.String("app", "", "path prefix for streamid")
	token      = flag.String("token", "", "token query param for streamid")
	passphrase = flag.String("passphrase", "", "passphrase for de- and encrypting the data")
	profile    = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")

	// Connection configuration flags
	congestion         = flag.String("congestion", "", "Type of congestion control ('live' or 'file')")
	conntimeo          = flag.Int("conntimeo", 0, "Connection timeout in milliseconds")
	streamid           = flag.String("streamid", "", "Stream ID (settable in caller mode only)")
	passphraseFlag     = flag.String("passphrase-flag", "", "Password for encrypted transmission (alternative to passphrase)")
	pbkeylen           = flag.Int("pbkeylen", 0, "Crypto key length in bytes (16, 24, or 32)")
	kmpreannounce      = flag.Uint64("kmpreannounce", 0, "Duration of Stream Encryption key switchover (packets)")
	kmrefreshrate      = flag.Uint64("kmrefreshrate", 0, "Stream encryption key refresh rate (packets)")
	enforcedencryption = flag.Bool("enforcedencryption", false, "Reject connection if parties set different passphrase")
	latency            = flag.Int("latency", 0, "Maximum accepted transmission latency in milliseconds")
	peerlatency        = flag.Int("peerlatency", 0, "Minimum receiver latency to be requested by sender in milliseconds")
	rcvlatency         = flag.Int("rcvlatency", 0, "Receiver-side latency in milliseconds")
	fc                 = flag.Uint64("fc", 0, "Flow control window size (packets)")
	sndbuf             = flag.Uint64("sndbuf", 0, "Sender buffer size in bytes")
	rcvbuf             = flag.Uint64("rcvbuf", 0, "Receiver buffer size in bytes")
	mss                = flag.Uint64("mss", 0, "MTU size")
	payloadsize        = flag.Uint64("payloadsize", 0, "Maximum payload size in bytes")
	maxbw              = flag.Int64("maxbw", 0, "Bandwidth limit in bytes/s (-1 for unlimited)")
	inputbw            = flag.Int64("inputbw", 0, "Input bandwidth in bytes")
	mininputbw         = flag.Int64("mininputbw", 0, "Minimum input bandwidth in bytes")
	oheadbw            = flag.Int64("oheadbw", 0, "Limit bandwidth overhead in percents")
	peeridletimeo      = flag.Int("peeridletimeo", 0, "Peer idle timeout in milliseconds")
	snddropdelay       = flag.Int("snddropdelay", 0, "Sender's delay before dropping packets in milliseconds")
	iptos              = flag.Int("iptos", 0, "IP socket type of service")
	ipttl              = flag.Int("ipttl", 0, "IP socket 'time to live' option")
	ipv6only           = flag.Int("ipv6only", -1, "Allow only IPv6 (-1 for default)")
	drifttracer        = flag.Bool("drifttracer", false, "Enable drift tracer")
	tlpktdrop          = flag.Bool("tlpktdrop", false, "Drop too late packets")
	tsbpdmode          = flag.Bool("tsbpdmode", false, "Enable timestamp-based packet delivery mode")
	messageapi         = flag.Bool("messageapi", false, "Enable SRT message mode")
	nakreport          = flag.Bool("nakreport", false, "Enable periodic NAK reports")
	lossmaxttl         = flag.Uint64("lossmaxttl", 0, "Packet reorder tolerance")
	packetfilter       = flag.String("packetfilter", "", "Set up the packet filter")
	transtype          = flag.String("transtype", "", "Transmission type ('live' or 'file')")
	groupconnect       = flag.Bool("groupconnect", false, "Accept group connections")
	groupstabtimeo     = flag.Int("groupstabtimeo", 0, "Group stability timeout in milliseconds")
	allowpeeripchange  = flag.Bool("allowpeeripchange", false, "Allow new IP to send data on existing socket id")
)
```

**In `contrib/client/main.go`:**

```go
func main() {
	// Parse all flags (both common and client-specific)
	common.ParseFlags()

	// Access flag values using *flagName (dereference the pointer)
	var logger srt.Logger
	if len(*logtopics) != 0 {
		logger = srt.NewLogger(strings.Split(*logtopics, ","))
	}

	r, err := openReader(*from, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: from: %v\n", err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	// ... rest of main()
}

func openReader(addr string, logger srt.Logger) (io.ReadCloser, error) {
	// ... existing code ...

	if u.Scheme == "srt" {
		config := srt.DefaultConfig()
		common.ApplyFlagsToConfig(&config)  // Apply CLI flags
		if err := config.UnmarshalQuery(u.RawQuery); err != nil {
			return nil, err
		}
		config.Logger = logger
		// ... rest of function
	}
}
```

**In `contrib/server/main.go`:**

```go
func main() {
	// Parse all flags (both common and server-specific)
	common.ParseFlags()

	if len(*addr) == 0 {
		fmt.Fprintf(os.Stderr, "Provide a listen address with -addr\n")
		os.Exit(1)
	}

	// ... profile setup code ...

	config := srt.DefaultConfig()
	common.ApplyFlagsToConfig(&config)  // Apply CLI flags

	// Handle server-specific passphrase flag
	if common.FlagSet["passphrase"] {
		config.Passphrase = *passphrase
	} else if common.FlagSet["passphrase-flag"] {
		config.Passphrase = *common.PassphraseFlag
	}

	if len(*logtopics) != 0 {
		config.Logger = srt.NewLogger(strings.Split(*logtopics, ","))
	}

	// Remove hardcoded values - they'll come from flags or defaults
	// config.KMPreAnnounce = 200
	// config.KMRefreshRate = 10000

	s.server = &srt.Server{
		Addr:            *addr,
		HandleConnect:   s.handleConnect,
		HandlePublish:   s.handlePublish,
		HandleSubscribe: s.handleSubscribe,
		Config:          &config,
	}

	// ... rest of main()
}
```

### Step 4: File Structure

After implementation, the file structure should look like:

```
contrib/
├── common/
│   ├── flags.go          # Shared flag declarations and ApplyFlagsToConfig()
│   └── flags_test.go     # Comprehensive tests for the flags package
├── client/
│   ├── main.go           # Client-specific flags and main()
│   ├── reader.go
│   └── writer.go
└── server/
    ├── main.go           # Server-specific flags and main()
    └── ...
```

**Key Points:**

1. **Shared Package (`contrib/common/flags.go`):**
   - Contains all SRT connection configuration flags
   - Exports `FlagSet` map to track set flags
   - Exports `ParseFlags()` function to parse and track flags
   - Exports `ApplyFlagsToConfig()` function to apply flags to config

2. **Test File (`contrib/common/flags_test.go`):**
   - Comprehensive test suite covering all flag types
   - Tests flag parsing and tracking
   - Tests config application with various scenarios
   - Tests edge cases (zero values, negative values, boolean flags)
   - Tests that unset flags don't override defaults
   - Tests that explicitly set zero values do override defaults

2. **Client (`contrib/client/main.go`):**
   - Declares client-specific flags (`from`, `to`, `logtopics`)
   - Calls `common.ParseFlags()` in `main()`
   - Calls `common.ApplyFlagsToConfig(&config)` before using config

3. **Server (`contrib/server/main.go`):**
   - Declares server-specific flags (`addr`, `app`, `token`, `passphrase`, `profile`)
   - Calls `common.ParseFlags()` in `main()`
   - Calls `common.ApplyFlagsToConfig(&config)` before using config
   - Handles server-specific passphrase flag logic

## Boolean Flag Handling

With the map-based approach, boolean flags are handled automatically. The `flagSet` map tracks which flags were explicitly set, allowing us to distinguish between:

- Flag not provided (use default) - `flagSet["flagName"]` is `false`
- Flag provided as `false` (override default to false) - `flagSet["flagName"]` is `true` and `*flagName` is `false`
- Flag provided as `true` (override default to true) - `flagSet["flagName"]` is `true` and `*flagName` is `true`

The `common.ApplyFlagsToConfig()` function checks `common.FlagSet["flagName"]` before applying the value, so boolean flags work correctly:

```go
if common.FlagSet["enforcedencryption"] {
    config.EnforcedEncryption = *common.EnforcedEncryption
}
```

This means:
- If the user doesn't provide `-enforcedencryption`, the default value is used
- If the user provides `-enforcedencryption=false`, the config is set to `false`
- If the user provides `-enforcedencryption=true`, the config is set to `true`

The same approach works for all flag types (strings, ints, bools, etc.), ensuring that only explicitly set flags override the default configuration.

## Flag Naming Convention

Use lowercase flag names that match the query parameter names used in `Config.UnmarshalQuery()`:
- `conntimeo` (not `connectiontimeout`)
- `rcvlatency` (not `receiverlatency`)
- `sndbuf` (not `sendbuffersize`)
- `peeridletimeo` (not `peeridletimeout`)

This maintains consistency with the existing URL query parameter parsing.

## Special Considerations

### Shared Package Benefits

Using a shared package (`contrib/common/flags.go`) provides several benefits:

- **DRY Principle**: Flag declarations and logic are defined once, not duplicated
- **Consistency**: Both client and server use the same flag names and behavior
- **Maintainability**: Changes to flag handling only need to be made in one place
- **Type Safety**: Go's package system ensures proper access to exported symbols

### Default Values

Flags should use `0`, `""`, or `false` as defaults. The `common.ApplyFlagsToConfig()` function uses the `common.FlagSet` map to determine which flags were explicitly set by the user. This ensures that:

- Zero values (0, "", false) don't accidentally override defaults when the flag wasn't provided
- Boolean flags can be explicitly set to `false` to override a default `true` value
- The map tracks all flag types consistently, not just booleans

### Flag Priority

The priority order should be:
1. CLI flags (highest priority)
2. URL query parameters (from `UnmarshalQuery`)
3. Default config values (lowest priority)

This means flags should be applied **before** calling `UnmarshalQuery()`.

### Server-Specific Flags

The server program has additional flags (`addr`, `app`, `token`, `passphrase`, `profile`) that should remain in the server's `main.go` only. These are not part of the shared package since they're specific to the server application.

### Passphrase Flag Handling

The server has a `passphrase` flag, while the shared package has a `passphrase-flag`. In the server's `main()`, handle both:

```go
if common.FlagSet["passphrase"] {
    config.Passphrase = *passphrase
} else if common.FlagSet["passphrase-flag"] {
    config.Passphrase = *common.PassphraseFlag
}
```

This allows the server to use either `-passphrase` (server-specific) or `-passphrase-flag` (shared) for consistency with the client.

### Backward Compatibility

Existing URL-based configuration (via query parameters) should continue to work. Flags provide an alternative way to set the same values.

## Example Usage

After implementation, users can configure connections via flags:

```bash
# Client example
./client -from srt://server:8000?mode=caller -to file://output.ts \
    -latency 200 -peerlatency 200 -rcvlatency 200 \
    -passphrase-flag "secret123" -fc 51200

# Server example
./server -addr :8000 -passphrase "secret123" \
    -latency 200 -peerlatency 200 -rcvlatency 200 \
    -kmpreannounce 200 -kmrefreshrate 10000
```

## Test Coverage

The `contrib/common/flags_test.go` file provides comprehensive test coverage for the flags package:

### Test Categories

1. **Flag Parsing Tests:**
   - Verifies `ParseFlags()` correctly populates `FlagSet` map
   - Tests behavior when no flags are provided
   - Ensures only explicitly set flags are tracked

2. **Config Application Tests by Type:**
   - **String flags**: Tests application of string values (congestion, streamid, etc.)
   - **Int flags**: Tests time.Duration conversions (latency, conntimeo, etc.)
   - **Uint64 flags**: Tests unsigned integer flags (fc, kmpreannounce, etc.)
   - **Int64 flags**: Tests signed integer flags including negative values (maxbw, etc.)
   - **Bool flags**: Tests boolean flag handling (enforcedencryption, drifttracer, etc.)

3. **Edge Case Tests:**
   - **Zero values not set**: Verifies that unset flags with zero defaults don't override config defaults
   - **Zero values explicitly set**: Verifies that explicitly set zero values do override config defaults
   - **Negative values**: Tests handling of negative values (e.g., `maxbw=-1` for unlimited)
   - **Boolean flags**: Tests all three states (not set, true, false)

4. **Comprehensive Tests:**
   - **All flags**: Tests applying all flags simultaneously
   - **Partial flags**: Tests applying only a subset of flags

### Running Tests

To run the tests:

```bash
# Run all tests in the common package
go test ./contrib/common/

# Run with verbose output
go test -v ./contrib/common/

# Run with coverage
go test -cover ./contrib/common/
```

### Test Isolation

The tests use a `resetFlags()` helper function to ensure test isolation. This function:
- Clears the `FlagSet` map
- Resets the global `flag.CommandLine` to a new FlagSet
- Re-declares flags for each test

This ensures that tests don't interfere with each other despite the flag package's global state.

## Testing Checklist

- [ ] `contrib/common/flags.go` is created with all shared flags
- [ ] All shared flags are exported (capitalized) from the common package
- [ ] `common.FlagSet` map is initialized
- [ ] `common.ParseFlags()` function is implemented and exported
- [ ] `common.ApplyFlagsToConfig()` function is implemented and exported
- [ ] Client-specific flags are declared in `contrib/client/main.go`
- [ ] Server-specific flags are declared in `contrib/server/main.go`
- [ ] Both `main.go` files import the `common` package
- [ ] Both `main.go` files call `common.ParseFlags()` in `main()`
- [ ] Both `main.go` files call `common.ApplyFlagsToConfig(&config)` before using config
- [ ] `common.ApplyFlagsToConfig()` checks `common.FlagSet` before applying values
- [ ] `common.ApplyFlagsToConfig()` handles all config fields
- [ ] Flags override defaults correctly
- [ ] URL query parameters still work
- [ ] Flags take precedence over URL query parameters
- [ ] Boolean flags handle all three states (not set, true, false)
- [ ] Both client and server implementations are consistent
- [ ] Zero values don't override defaults unintentionally (thanks to `common.FlagSet` map)
- [ ] Negative values (like `maxbw=-1`) are handled correctly
- [ ] Server handles both `-passphrase` and `-passphrase-flag` correctly
- [ ] Run `go test ./contrib/common/` to verify all tests pass
- [ ] All test cases in `flags_test.go` pass successfully

