package srt

import (
	"net/url"
	"strconv"
	"time"
)

// ════════════════════════════════════════════════════════════════════════════
// Field Parser Types
// ════════════════════════════════════════════════════════════════════════════

// FieldParser defines how a single URL query parameter is parsed into Config.
type FieldParser struct {
	Key   string                      // Query parameter key (e.g., "latency")
	Parse func(*Config, string) error // Parse function
}

// FieldSerializer defines how a single Config field is serialized to URL query.
type FieldSerializer struct {
	Key       string                                // Query parameter key
	Serialize func(*Config, *Config) (string, bool) // Returns (value, shouldInclude)
}

// ════════════════════════════════════════════════════════════════════════════
// Parse Helpers
// ════════════════════════════════════════════════════════════════════════════

// parseBool parses a string as a boolean using multiple representations.
func parseBool(s string) (bool, bool) {
	switch s {
	case "yes", "on", "true", "1":
		return true, true
	case "no", "off", "false", "0":
		return false, true
	default:
		return false, false
	}
}

// parseInt parses a string as an integer.
func parseInt(s string) (int, bool) {
	v, err := strconv.Atoi(s)
	return v, err == nil
}

// parseUint32 parses a string as uint32.
func parseUint32(s string) (uint32, bool) {
	v, err := strconv.ParseUint(s, 10, 32)
	return uint32(v), err == nil
}

// parseInt64 parses a string as int64.
func parseInt64(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

// parseUint64 parses a string as uint64.
func parseUint64(s string) (uint64, bool) {
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// ════════════════════════════════════════════════════════════════════════════
// Field Parsers Table
// ════════════════════════════════════════════════════════════════════════════

var fieldParsers = []FieldParser{
	// String fields
	{"congestion", func(c *Config, s string) error { c.Congestion = s; return nil }},
	{"packetfilter", func(c *Config, s string) error { c.PacketFilter = s; return nil }},
	{"passphrase", func(c *Config, s string) error { c.Passphrase = s; return nil }},
	{"streamid", func(c *Config, s string) error { c.StreamId = s; return nil }},
	{"transtype", func(c *Config, s string) error { c.TransmissionType = s; return nil }},

	// Duration fields (milliseconds)
	{"conntimeo", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.ConnectionTimeout = time.Duration(v) * time.Millisecond
		}
		return nil
	}},
	{"groupstabtimeo", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.GroupStabilityTimeout = time.Duration(v) * time.Millisecond
		}
		return nil
	}},
	{"latency", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.Latency = time.Duration(v) * time.Millisecond
		}
		return nil
	}},
	{"peeridletimeo", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.PeerIdleTimeout = time.Duration(v) * time.Millisecond
		}
		return nil
	}},
	{"peerlatency", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.PeerLatency = time.Duration(v) * time.Millisecond
		}
		return nil
	}},
	{"rcvlatency", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.ReceiverLatency = time.Duration(v) * time.Millisecond
		}
		return nil
	}},
	{"snddropdelay", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.SendDropDelay = time.Duration(v) * time.Millisecond
		}
		return nil
	}},

	// Boolean fields
	{"drifttracer", func(c *Config, s string) error {
		if v, ok := parseBool(s); ok {
			c.DriftTracer = v
		}
		return nil
	}},
	{"enforcedencryption", func(c *Config, s string) error {
		if v, ok := parseBool(s); ok {
			c.EnforcedEncryption = v
		}
		return nil
	}},
	{"groupconnect", func(c *Config, s string) error {
		if v, ok := parseBool(s); ok {
			c.GroupConnect = v
		}
		return nil
	}},
	{"messageapi", func(c *Config, s string) error {
		if v, ok := parseBool(s); ok {
			c.MessageAPI = v
		}
		return nil
	}},
	{"nakreport", func(c *Config, s string) error {
		if v, ok := parseBool(s); ok {
			c.NAKReport = v
		}
		return nil
	}},
	{"tlpktdrop", func(c *Config, s string) error {
		if v, ok := parseBool(s); ok {
			c.TooLatePacketDrop = v
		}
		return nil
	}},
	{"tsbpdmode", func(c *Config, s string) error {
		if v, ok := parseBool(s); ok {
			c.TSBPDMode = v
		}
		return nil
	}},

	// Integer fields
	{"iptos", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.IPTOS = v
		}
		return nil
	}},
	{"ipttl", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.IPTTL = v
		}
		return nil
	}},
	{"ipv6only", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.IPv6Only = v
		}
		return nil
	}},
	{"pbkeylen", func(c *Config, s string) error {
		if v, ok := parseInt(s); ok {
			c.PBKeylen = v
		}
		return nil
	}},

	// Uint32 fields
	{"fc", func(c *Config, s string) error {
		if v, ok := parseUint32(s); ok {
			c.FC = v
		}
		return nil
	}},
	{"lossmaxttl", func(c *Config, s string) error {
		if v, ok := parseUint32(s); ok {
			c.LossMaxTTL = v
		}
		return nil
	}},
	{"mss", func(c *Config, s string) error {
		if v, ok := parseUint32(s); ok {
			c.MSS = v
		}
		return nil
	}},
	{"payloadsize", func(c *Config, s string) error {
		if v, ok := parseUint32(s); ok {
			c.PayloadSize = v
		}
		return nil
	}},
	{"rcvbuf", func(c *Config, s string) error {
		if v, ok := parseUint32(s); ok {
			c.ReceiverBufferSize = v
		}
		return nil
	}},
	{"sndbuf", func(c *Config, s string) error {
		if v, ok := parseUint32(s); ok {
			c.SendBufferSize = v
		}
		return nil
	}},

	// Int64 fields
	{"inputbw", func(c *Config, s string) error {
		if v, ok := parseInt64(s); ok {
			c.InputBW = v
		}
		return nil
	}},
	{"maxbw", func(c *Config, s string) error {
		if v, ok := parseInt64(s); ok {
			c.MaxBW = v
		}
		return nil
	}},
	{"mininputbw", func(c *Config, s string) error {
		if v, ok := parseInt64(s); ok {
			c.MinInputBW = v
		}
		return nil
	}},
	{"oheadbw", func(c *Config, s string) error {
		if v, ok := parseInt64(s); ok {
			c.OverheadBW = v
		}
		return nil
	}},

	// Uint64 fields
	{"kmpreannounce", func(c *Config, s string) error {
		if v, ok := parseUint64(s); ok {
			c.KMPreAnnounce = v
		}
		return nil
	}},
	{"kmrefreshrate", func(c *Config, s string) error {
		if v, ok := parseUint64(s); ok {
			c.KMRefreshRate = v
		}
		return nil
	}},
}

// fieldParserMap provides O(1) lookup by key.
var fieldParserMap map[string]func(*Config, string) error

func init() {
	fieldParserMap = make(map[string]func(*Config, string) error, len(fieldParsers))
	for _, fp := range fieldParsers {
		fieldParserMap[fp.Key] = fp.Parse
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Field Serializers Table
// ════════════════════════════════════════════════════════════════════════════

var fieldSerializers = []FieldSerializer{
	// String fields
	{"congestion", func(c, d *Config) (string, bool) {
		return c.Congestion, c.Congestion != d.Congestion
	}},
	{"packetfilter", func(c, d *Config) (string, bool) {
		return c.PacketFilter, c.PacketFilter != d.PacketFilter
	}},
	{"passphrase", func(c, d *Config) (string, bool) {
		return c.Passphrase, len(c.Passphrase) != 0
	}},
	{"streamid", func(c, d *Config) (string, bool) {
		return c.StreamId, len(c.StreamId) != 0
	}},
	{"transtype", func(c, d *Config) (string, bool) {
		return c.TransmissionType, c.TransmissionType != d.TransmissionType
	}},

	// Duration fields (milliseconds)
	{"conntimeo", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.ConnectionTimeout.Milliseconds(), 10), c.ConnectionTimeout != d.ConnectionTimeout
	}},
	{"groupstabtimeo", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.GroupStabilityTimeout.Milliseconds(), 10), c.GroupStabilityTimeout != d.GroupStabilityTimeout
	}},
	{"latency", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.Latency.Milliseconds(), 10), c.Latency != d.Latency
	}},
	{"peeridletimeo", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.PeerIdleTimeout.Milliseconds(), 10), c.PeerIdleTimeout != d.PeerIdleTimeout
	}},
	{"peerlatency", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.PeerLatency.Milliseconds(), 10), c.PeerLatency != d.PeerLatency
	}},
	{"rcvlatency", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.ReceiverLatency.Milliseconds(), 10), c.ReceiverLatency != d.ReceiverLatency
	}},
	{"snddropdelay", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.SendDropDelay.Milliseconds(), 10), c.SendDropDelay != d.SendDropDelay
	}},

	// Boolean fields
	{"drifttracer", func(c, d *Config) (string, bool) {
		return strconv.FormatBool(c.DriftTracer), c.DriftTracer != d.DriftTracer
	}},
	{"enforcedencryption", func(c, d *Config) (string, bool) {
		return strconv.FormatBool(c.EnforcedEncryption), c.EnforcedEncryption != d.EnforcedEncryption
	}},
	{"groupconnect", func(c, d *Config) (string, bool) {
		return strconv.FormatBool(c.GroupConnect), c.GroupConnect != d.GroupConnect
	}},
	{"messageapi", func(c, d *Config) (string, bool) {
		return strconv.FormatBool(c.MessageAPI), c.MessageAPI != d.MessageAPI
	}},
	{"nakreport", func(c, d *Config) (string, bool) {
		return strconv.FormatBool(c.NAKReport), c.NAKReport != d.NAKReport
	}},
	{"tlpktdrop", func(c, d *Config) (string, bool) {
		return strconv.FormatBool(c.TooLatePacketDrop), c.TooLatePacketDrop != d.TooLatePacketDrop
	}},
	{"tsbpdmode", func(c, d *Config) (string, bool) {
		return strconv.FormatBool(c.TSBPDMode), c.TSBPDMode != d.TSBPDMode
	}},

	// Integer fields
	{"iptos", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(int64(c.IPTOS), 10), c.IPTOS != d.IPTOS
	}},
	{"ipttl", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(int64(c.IPTTL), 10), c.IPTTL != d.IPTTL
	}},
	{"ipv6only", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(int64(c.IPv6Only), 10), c.IPv6Only != d.IPv6Only
	}},
	{"pbkeylen", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(int64(c.PBKeylen), 10), c.PBKeylen != d.PBKeylen
	}},

	// Uint32 fields
	{"fc", func(c, d *Config) (string, bool) {
		return strconv.FormatUint(uint64(c.FC), 10), c.FC != d.FC
	}},
	{"lossmaxttl", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(int64(c.LossMaxTTL), 10), c.LossMaxTTL != d.LossMaxTTL
	}},
	{"mss", func(c, d *Config) (string, bool) {
		return strconv.FormatUint(uint64(c.MSS), 10), c.MSS != d.MSS
	}},
	{"payloadsize", func(c, d *Config) (string, bool) {
		return strconv.FormatUint(uint64(c.PayloadSize), 10), c.PayloadSize != d.PayloadSize
	}},
	{"rcvbuf", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(int64(c.ReceiverBufferSize), 10), c.ReceiverBufferSize != d.ReceiverBufferSize
	}},
	{"sndbuf", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(int64(c.SendBufferSize), 10), c.SendBufferSize != d.SendBufferSize
	}},

	// Int64 fields
	{"inputbw", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.InputBW, 10), c.InputBW != d.InputBW
	}},
	{"maxbw", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.MaxBW, 10), c.MaxBW != d.MaxBW
	}},
	{"mininputbw", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.MinInputBW, 10), c.MinInputBW != d.InputBW // Note: uses d.InputBW
	}},
	{"oheadbw", func(c, d *Config) (string, bool) {
		return strconv.FormatInt(c.OverheadBW, 10), c.OverheadBW != d.OverheadBW
	}},

	// Uint64 fields (only when passphrase is set)
	{"kmpreannounce", func(c, d *Config) (string, bool) {
		return strconv.FormatUint(c.KMPreAnnounce, 10), len(c.Passphrase) != 0 && c.KMPreAnnounce != d.KMPreAnnounce
	}},
	{"kmrefreshrate", func(c, d *Config) (string, bool) {
		return strconv.FormatUint(c.KMRefreshRate, 10), len(c.Passphrase) != 0 && c.KMRefreshRate != d.KMRefreshRate
	}},
}

// ════════════════════════════════════════════════════════════════════════════
// Table-Driven Implementation Functions
// ════════════════════════════════════════════════════════════════════════════

// unmarshalQueryTable parses a query string using the table-driven approach.
func (c *Config) unmarshalQueryTable(query string) error {
	v, err := url.ParseQuery(query)
	if err != nil {
		return err
	}

	for key, values := range v {
		if len(values) == 0 {
			continue
		}
		if parser, ok := fieldParserMap[key]; ok {
			if parseErr := parser(c, values[0]); parseErr != nil {
				return parseErr
			}
		}
	}

	return nil
}

// marshalQueryTable serializes config to query string using the table-driven approach.
func (c *Config) marshalQueryTable() string {
	q := url.Values{}

	for _, fs := range fieldSerializers {
		if value, include := fs.Serialize(c, &defaultConfig); include {
			q.Set(fs.Key, value)
		}
	}

	return q.Encode()
}

// GetFieldParserCount returns the number of field parsers for testing.
func GetFieldParserCount() int {
	return len(fieldParsers)
}

// GetFieldSerializerCount returns the number of field serializers for testing.
func GetFieldSerializerCount() int {
	return len(fieldSerializers)
}
