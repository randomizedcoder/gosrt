package srt

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// UnmarshalURL takes a SRT URL and parses out the configuration. A SRT URL is
// srt://[host]:[port]?[key1]=[value1]&[key2]=[value2]... It returns the host:port
// of the URL.
func (c *Config) UnmarshalURL(srturl string) (string, error) {
	u, err := url.Parse(srturl)
	if err != nil {
		return "", err
	}

	if u.Scheme != "srt" {
		return "", fmt.Errorf("the URL doesn't seem to be an srt:// URL")
	}

	return u.Host, c.UnmarshalQuery(u.RawQuery)
}

// UnmarshalQuery parses a query string and interprets it as a configuration
// for a SRT connection. The key in each key/value pair corresponds to the
// respective field in the Config type, but with only lower case letters. Bool
// values can be represented as "true"/"false", "on"/"off", "yes"/"no", or "0"/"1".
func (c *Config) UnmarshalQuery(query string) error {
	v, err := url.ParseQuery(query)
	if err != nil {
		return err
	}

	// https://github.com/Haivision/srt/blob/master/docs/apps/srt-live-transmit.md

	if s := v.Get("congestion"); len(s) != 0 {
		c.Congestion = s
	}

	if s := v.Get("conntimeo"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.ConnectionTimeout = time.Duration(d) * time.Millisecond
		}
	}

	if s := v.Get("drifttracer"); len(s) != 0 {
		switch s {
		case "yes", "on", "true", "1":
			c.DriftTracer = true
		case "no", "off", "false", "0":
			c.DriftTracer = false
		}
	}

	if s := v.Get("enforcedencryption"); len(s) != 0 {
		switch s {
		case "yes", "on", "true", "1":
			c.EnforcedEncryption = true
		case "no", "off", "false", "0":
			c.EnforcedEncryption = false
		}
	}

	if s := v.Get("fc"); len(s) != 0 {
		if d, err := strconv.ParseUint(s, 10, 32); err == nil {
			c.FC = uint32(d)
		}
	}

	if s := v.Get("groupconnect"); len(s) != 0 {
		switch s {
		case "yes", "on", "true", "1":
			c.GroupConnect = true
		case "no", "off", "false", "0":
			c.GroupConnect = false
		}
	}

	if s := v.Get("groupstabtimeo"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.GroupStabilityTimeout = time.Duration(d) * time.Millisecond
		}
	}

	if s := v.Get("inputbw"); len(s) != 0 {
		if d, err := strconv.ParseInt(s, 10, 64); err == nil {
			c.InputBW = d
		}
	}

	if s := v.Get("iptos"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.IPTOS = d
		}
	}

	if s := v.Get("ipttl"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.IPTTL = d
		}
	}

	if s := v.Get("ipv6only"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.IPv6Only = d
		}
	}

	if s := v.Get("kmpreannounce"); len(s) != 0 {
		if d, err := strconv.ParseUint(s, 10, 64); err == nil {
			c.KMPreAnnounce = d
		}
	}

	if s := v.Get("kmrefreshrate"); len(s) != 0 {
		if d, err := strconv.ParseUint(s, 10, 64); err == nil {
			c.KMRefreshRate = d
		}
	}

	if s := v.Get("latency"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.Latency = time.Duration(d) * time.Millisecond
		}
	}

	if s := v.Get("lossmaxttl"); len(s) != 0 {
		if d, err := strconv.ParseUint(s, 10, 32); err == nil {
			c.LossMaxTTL = uint32(d)
		}
	}

	if s := v.Get("maxbw"); len(s) != 0 {
		if d, err := strconv.ParseInt(s, 10, 64); err == nil {
			c.MaxBW = d
		}
	}

	if s := v.Get("mininputbw"); len(s) != 0 {
		if d, err := strconv.ParseInt(s, 10, 64); err == nil {
			c.MinInputBW = d
		}
	}

	if s := v.Get("messageapi"); len(s) != 0 {
		switch s {
		case "yes", "on", "true", "1":
			c.MessageAPI = true
		case "no", "off", "false", "0":
			c.MessageAPI = false
		}
	}

	// minversion is ignored

	if s := v.Get("mss"); len(s) != 0 {
		if d, err := strconv.ParseUint(s, 10, 32); err == nil {
			c.MSS = uint32(d)
		}
	}

	if s := v.Get("nakreport"); len(s) != 0 {
		switch s {
		case "yes", "on", "true", "1":
			c.NAKReport = true
		case "no", "off", "false", "0":
			c.NAKReport = false
		}
	}

	if s := v.Get("oheadbw"); len(s) != 0 {
		if d, err := strconv.ParseInt(s, 10, 64); err == nil {
			c.OverheadBW = d
		}
	}

	if s := v.Get("packetfilter"); len(s) != 0 {
		c.PacketFilter = s
	}

	if s := v.Get("passphrase"); len(s) != 0 {
		c.Passphrase = s
	}

	if s := v.Get("payloadsize"); len(s) != 0 {
		if d, err := strconv.ParseUint(s, 10, 32); err == nil {
			c.PayloadSize = uint32(d)
		}
	}

	if s := v.Get("pbkeylen"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.PBKeylen = d
		}
	}

	if s := v.Get("peeridletimeo"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.PeerIdleTimeout = time.Duration(d) * time.Millisecond
		}
	}

	if s := v.Get("peerlatency"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.PeerLatency = time.Duration(d) * time.Millisecond
		}
	}

	if s := v.Get("rcvbuf"); len(s) != 0 {
		if d, err := strconv.ParseUint(s, 10, 32); err == nil {
			c.ReceiverBufferSize = uint32(d)
		}
	}

	if s := v.Get("rcvlatency"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.ReceiverLatency = time.Duration(d) * time.Millisecond
		}
	}

	// retransmitalgo not implemented (there's only one)

	if s := v.Get("sndbuf"); len(s) != 0 {
		if d, err := strconv.ParseUint(s, 10, 32); err == nil {
			c.SendBufferSize = uint32(d)
		}
	}

	if s := v.Get("snddropdelay"); len(s) != 0 {
		if d, err := strconv.Atoi(s); err == nil {
			c.SendDropDelay = time.Duration(d) * time.Millisecond
		}
	}

	if s := v.Get("streamid"); len(s) != 0 {
		c.StreamId = s
	}

	if s := v.Get("tlpktdrop"); len(s) != 0 {
		switch s {
		case "yes", "on", "true", "1":
			c.TooLatePacketDrop = true
		case "no", "off", "false", "0":
			c.TooLatePacketDrop = false
		}
	}

	if s := v.Get("transtype"); len(s) != 0 {
		c.TransmissionType = s
	}

	if s := v.Get("tsbpdmode"); len(s) != 0 {
		switch s {
		case "yes", "on", "true", "1":
			c.TSBPDMode = true
		case "no", "off", "false", "0":
			c.TSBPDMode = false
		}
	}

	return nil
}

// MarshalURL returns the SRT URL for this config and the given address (host:port).
func (c *Config) MarshalURL(address string) string {
	return "srt://" + address + "?" + c.MarshalQuery()
}

// MarshalQuery returns the corresponding query string for a configuration.
func (c *Config) MarshalQuery() string {
	q := url.Values{}

	if c.Congestion != defaultConfig.Congestion {
		q.Set("congestion", c.Congestion)
	}

	if c.ConnectionTimeout != defaultConfig.ConnectionTimeout {
		q.Set("conntimeo", strconv.FormatInt(c.ConnectionTimeout.Milliseconds(), 10))
	}

	if c.DriftTracer != defaultConfig.DriftTracer {
		q.Set("drifttracer", strconv.FormatBool(c.DriftTracer))
	}

	if c.EnforcedEncryption != defaultConfig.EnforcedEncryption {
		q.Set("enforcedencryption", strconv.FormatBool(c.EnforcedEncryption))
	}

	if c.FC != defaultConfig.FC {
		q.Set("fc", strconv.FormatUint(uint64(c.FC), 10))
	}

	if c.GroupConnect != defaultConfig.GroupConnect {
		q.Set("groupconnect", strconv.FormatBool(c.GroupConnect))
	}

	if c.GroupStabilityTimeout != defaultConfig.GroupStabilityTimeout {
		q.Set("groupstabtimeo", strconv.FormatInt(c.GroupStabilityTimeout.Milliseconds(), 10))
	}

	if c.InputBW != defaultConfig.InputBW {
		q.Set("inputbw", strconv.FormatInt(c.InputBW, 10))
	}

	if c.IPTOS != defaultConfig.IPTOS {
		q.Set("iptos", strconv.FormatInt(int64(c.IPTOS), 10))
	}

	if c.IPTTL != defaultConfig.IPTTL {
		q.Set("ipttl", strconv.FormatInt(int64(c.IPTTL), 10))
	}

	if c.IPv6Only != defaultConfig.IPv6Only {
		q.Set("ipv6only", strconv.FormatInt(int64(c.IPv6Only), 10))
	}

	if len(c.Passphrase) != 0 {
		if c.KMPreAnnounce != defaultConfig.KMPreAnnounce {
			q.Set("kmpreannounce", strconv.FormatUint(c.KMPreAnnounce, 10))
		}

		if c.KMRefreshRate != defaultConfig.KMRefreshRate {
			q.Set("kmrefreshrate", strconv.FormatUint(c.KMRefreshRate, 10))
		}
	}

	if c.Latency != defaultConfig.Latency {
		q.Set("latency", strconv.FormatInt(c.Latency.Milliseconds(), 10))
	}

	if c.LossMaxTTL != defaultConfig.LossMaxTTL {
		q.Set("lossmaxttl", strconv.FormatInt(int64(c.LossMaxTTL), 10))
	}

	if c.MaxBW != defaultConfig.MaxBW {
		q.Set("maxbw", strconv.FormatInt(c.MaxBW, 10))
	}

	if c.MinInputBW != defaultConfig.InputBW {
		q.Set("mininputbw", strconv.FormatInt(c.MinInputBW, 10))
	}

	if c.MessageAPI != defaultConfig.MessageAPI {
		q.Set("messageapi", strconv.FormatBool(c.MessageAPI))
	}

	if c.MSS != defaultConfig.MSS {
		q.Set("mss", strconv.FormatUint(uint64(c.MSS), 10))
	}

	if c.NAKReport != defaultConfig.NAKReport {
		q.Set("nakreport", strconv.FormatBool(c.NAKReport))
	}

	if c.OverheadBW != defaultConfig.OverheadBW {
		q.Set("oheadbw", strconv.FormatInt(c.OverheadBW, 10))
	}

	if c.PacketFilter != defaultConfig.PacketFilter {
		q.Set("packetfilter", c.PacketFilter)
	}

	if len(c.Passphrase) != 0 {
		q.Set("passphrase", c.Passphrase)
	}

	if c.PayloadSize != defaultConfig.PayloadSize {
		q.Set("payloadsize", strconv.FormatUint(uint64(c.PayloadSize), 10))
	}

	if c.PBKeylen != defaultConfig.PBKeylen {
		q.Set("pbkeylen", strconv.FormatInt(int64(c.PBKeylen), 10))
	}

	if c.PeerIdleTimeout != defaultConfig.PeerIdleTimeout {
		q.Set("peeridletimeo", strconv.FormatInt(c.PeerIdleTimeout.Milliseconds(), 10))
	}

	if c.PeerLatency != defaultConfig.PeerLatency {
		q.Set("peerlatency", strconv.FormatInt(c.PeerLatency.Milliseconds(), 10))
	}

	if c.ReceiverBufferSize != defaultConfig.ReceiverBufferSize {
		q.Set("rcvbuf", strconv.FormatInt(int64(c.ReceiverBufferSize), 10))
	}

	if c.ReceiverLatency != defaultConfig.ReceiverLatency {
		q.Set("rcvlatency", strconv.FormatInt(c.ReceiverLatency.Milliseconds(), 10))
	}

	if c.SendBufferSize != defaultConfig.SendBufferSize {
		q.Set("sndbuf", strconv.FormatInt(int64(c.SendBufferSize), 10))
	}

	if c.SendDropDelay != defaultConfig.SendDropDelay {
		q.Set("snddropdelay", strconv.FormatInt(c.SendDropDelay.Milliseconds(), 10))
	}

	if len(c.StreamId) != 0 {
		q.Set("streamid", c.StreamId)
	}

	if c.TooLatePacketDrop != defaultConfig.TooLatePacketDrop {
		q.Set("tlpktdrop", strconv.FormatBool(c.TooLatePacketDrop))
	}

	if c.TransmissionType != defaultConfig.TransmissionType {
		q.Set("transtype", c.TransmissionType)
	}

	if c.TSBPDMode != defaultConfig.TSBPDMode {
		q.Set("tsbpdmode", strconv.FormatBool(c.TSBPDMode))
	}

	return q.Encode()
}

