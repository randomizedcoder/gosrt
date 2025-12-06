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

	// io_uring configuration flags
	IoUringEnabled      = flag.Bool("iouringenabled", false, "Enable io_uring for per-connection send queues (requires Linux kernel 5.1+)")
	IoUringSendRingSize = flag.Int("iouringsendringsize", 0, "Size of the io_uring ring for per-connection send queues (must be power of 2, 16-1024)")

	// Channel buffer size configuration flags
	NetworkQueueSize = flag.Int("networkqueuesize", 0, "Size of the network queue channel buffer (packets from network)")
	WriteQueueSize   = flag.Int("writequeuesize", 0, "Size of the write queue channel buffer (packets from application writes)")
	ReadQueueSize    = flag.Int("readqueuesize", 0, "Size of the read queue channel buffer (packets ready for application reads)")
	ReceiveQueueSize = flag.Int("receivequeuesize", 0, "Size of the receive queue channel buffer (packets from network before routing to connections)")

	// Packet reordering configuration flags
	PacketReorderAlgorithm = flag.String("packetreorderalgorithm", "", "Packet reordering algorithm: 'list' (default, O(n) insertions) or 'btree' (O(log n) operations, better for large buffers)")
	BTreeDegree            = flag.Int("btreedegree", 0, "B-tree degree for packet reordering (only used if packetreorderalgorithm='btree', default: 32)")

	// io_uring receive configuration flags
	IoUringRecvEnabled        = flag.Bool("iouringrecvenabled", false, "Enable io_uring for receive operations (requires Linux kernel 5.1+)")
	IoUringRecvRingSize       = flag.Int("iouringrecvringsize", 0, "Size of the io_uring receive ring (must be power of 2, 64-32768)")
	IoUringRecvInitialPending = flag.Int("iouringrecvinitialpending", 0, "Initial number of pending receive requests at startup (default: ring size)")
	IoUringRecvBatchSize      = flag.Int("iouringrecvbatchsize", 0, "Batch size for resubmitting receive requests after completions (default: 256)")

	// Statistics configuration flags
	StatisticsPrintInterval = flag.Duration("statisticsinterval", 0, "Interval for printing connection statistics (e.g., 10s). 0 disables periodic statistics printing")

	// Timeout and shutdown configuration flags
	HandshakeTimeout = flag.Duration("handshaketimeout", 0, "Maximum time allowed for complete handshake exchange (e.g., 1.5s). Must be less than peeridletimeo")
	ShutdownDelay    = flag.Duration("shutdowndelay", 0, "Time to wait for graceful shutdown after signal (e.g., 5s)")

	// Local address binding for clients
	LocalAddr = flag.String("localaddr", "", "Local IP address to bind to when connecting (e.g., 127.0.0.20)")
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
	if FlagSet["iouringenabled"] {
		config.IoUringEnabled = *IoUringEnabled
	}
	if FlagSet["iouringsendringsize"] {
		config.IoUringSendRingSize = *IoUringSendRingSize
	}
	if FlagSet["networkqueuesize"] {
		config.NetworkQueueSize = *NetworkQueueSize
	}
	if FlagSet["writequeuesize"] {
		config.WriteQueueSize = *WriteQueueSize
	}
	if FlagSet["readqueuesize"] {
		config.ReadQueueSize = *ReadQueueSize
	}
	if FlagSet["receivequeuesize"] {
		config.ReceiveQueueSize = *ReceiveQueueSize
	}
	if FlagSet["packetreorderalgorithm"] {
		config.PacketReorderAlgorithm = *PacketReorderAlgorithm
	}
	if FlagSet["btreedegree"] {
		config.BTreeDegree = *BTreeDegree
	}
	if FlagSet["iouringrecvenabled"] {
		config.IoUringRecvEnabled = *IoUringRecvEnabled
	}
	if FlagSet["iouringrecvringsize"] {
		config.IoUringRecvRingSize = *IoUringRecvRingSize
	}
	if FlagSet["iouringrecvinitialpending"] {
		config.IoUringRecvInitialPending = *IoUringRecvInitialPending
	}
	if FlagSet["iouringrecvbatchsize"] {
		config.IoUringRecvBatchSize = *IoUringRecvBatchSize
	}
	if FlagSet["statisticsinterval"] {
		config.StatisticsPrintInterval = *StatisticsPrintInterval
	}
	if FlagSet["handshaketimeout"] {
		config.HandshakeTimeout = *HandshakeTimeout
	}
	if FlagSet["shutdowndelay"] {
		config.ShutdownDelay = *ShutdownDelay
	}
	if FlagSet["localaddr"] {
		config.LocalAddr = *LocalAddr
	}
}
