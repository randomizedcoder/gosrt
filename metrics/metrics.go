package metrics

import (
	"math"
	"sync/atomic"
	"time"
)

const (
	// LockTimingSamples is the number of recent samples to keep for lock timing
	LockTimingSamples = 10
)

// ConnectionMetrics holds all metrics for a single connection
// All counters use atomic.Uint64 for lock-free, high-performance increments
type ConnectionMetrics struct {
	// Single counter for all successful receives (for peer idle timeout)
	PktRecvSuccess atomic.Uint64

	// Edge case tracking (should never increment, but defensive programming)
	PktRecvNil            atomic.Uint64 // Nil packet edge case
	PktRecvControlUnknown atomic.Uint64 // Unknown control packet types
	PktRecvSubTypeUnknown atomic.Uint64 // Unknown USER packet subtypes

	// Packet counters (atomic for performance)
	PktRecvDataSuccess atomic.Uint64
	PktRecvDataDropped atomic.Uint64
	PktRecvDataError   atomic.Uint64
	PktSentDataSuccess atomic.Uint64
	// PktSentDataDropped atomic.Uint64 // Not implemented - send drops tracked via CongestionSendPktDrop
	PktSentDataError atomic.Uint64

	// Control packet counters - ACK
	PktRecvACKSuccess atomic.Uint64
	// PktRecvACKDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktRecvACKError   atomic.Uint64 // Not implemented - control packets currently never error
	PktSentACKSuccess atomic.Uint64
	// PktSentACKDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktSentACKError   atomic.Uint64 // Not implemented - control packets currently never error

	// Control packet counters - ACK (Light vs Full breakdown) - Phase 5: ACK Optimization
	PktSentACKLiteSuccess atomic.Uint64 // Light ACKs sent (sequence only, no RTT)
	PktSentACKFullSuccess atomic.Uint64 // Full ACKs sent (includes RTT, triggers ACKACK)
	PktRecvACKLiteSuccess atomic.Uint64 // Light ACKs received
	PktRecvACKFullSuccess atomic.Uint64 // Full ACKs received

	// Control packet counters - ACKACK
	PktRecvACKACKSuccess atomic.Uint64
	// PktRecvACKACKDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktRecvACKACKError   atomic.Uint64 // Not implemented - control packets currently never error
	PktSentACKACKSuccess atomic.Uint64
	// PktSentACKACKDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktSentACKACKError   atomic.Uint64 // Not implemented - control packets currently never error

	// Control packet counters - NAK
	PktRecvNAKSuccess atomic.Uint64
	// PktRecvNAKDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktRecvNAKError   atomic.Uint64 // Not implemented - control packets currently never error
	PktSentNAKSuccess atomic.Uint64
	// PktSentNAKDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktSentNAKError   atomic.Uint64 // Not implemented - control packets currently never error

	// Control packet counters - KM
	PktRecvKMSuccess atomic.Uint64
	// PktRecvKMDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktRecvKMError   atomic.Uint64 // Not implemented - control packets currently never error
	PktSentKMSuccess atomic.Uint64
	// PktSentKMDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktSentKMError   atomic.Uint64 // Not implemented - control packets currently never error

	// Control packet counters - Keepalive
	PktRecvKeepaliveSuccess atomic.Uint64
	// PktRecvKeepaliveDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktRecvKeepaliveError   atomic.Uint64 // Not implemented - control packets currently never error
	PktSentKeepaliveSuccess atomic.Uint64
	// PktSentKeepaliveDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktSentKeepaliveError   atomic.Uint64 // Not implemented - control packets currently never error

	// Control packet counters - Shutdown
	PktRecvShutdownSuccess atomic.Uint64
	// PktRecvShutdownDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktRecvShutdownError   atomic.Uint64 // Not implemented - control packets currently never error
	PktSentShutdownSuccess atomic.Uint64
	// PktSentShutdownDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktSentShutdownError   atomic.Uint64 // Not implemented - control packets currently never error

	// Control packet counters - Handshake
	PktRecvHandshakeSuccess atomic.Uint64
	// PktRecvHandshakeDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktRecvHandshakeError   atomic.Uint64 // Not implemented - control packets currently never error
	PktSentHandshakeSuccess atomic.Uint64
	// PktSentHandshakeDropped atomic.Uint64 // Not implemented - control packets currently never dropped
	// PktSentHandshakeError   atomic.Uint64 // Not implemented - control packets currently never error

	// Path-specific counters
	PktRecvIoUring   atomic.Uint64
	PktRecvReadFrom  atomic.Uint64
	PktSentIoUring   atomic.Uint64
	PktSentWriteTo   atomic.Uint64
	PktSentSubmitted atomic.Uint64 // io_uring submissions (for detecting lost completions)

	// Error counters (detailed)
	PktRecvErrorIoUring atomic.Uint64
	PktRecvErrorParse   atomic.Uint64
	PktRecvErrorRoute   atomic.Uint64
	PktRecvErrorEmpty   atomic.Uint64
	PktRecvErrorUnknown atomic.Uint64 // Unknown/unexpected error (unrecognized drop reason)
	PktSentErrorIoUring atomic.Uint64
	PktSentErrorMarshal atomic.Uint64
	PktSentErrorSubmit  atomic.Uint64
	PktSentErrorUnknown atomic.Uint64 // Unknown/unexpected error (unrecognized drop reason)

	// Crypto operation error counters
	CryptoErrorEncrypt     atomic.Uint64 // Encryption/decryption payload errors
	CryptoErrorGenerateSEK atomic.Uint64 // SEK generation errors
	CryptoErrorMarshalKM   atomic.Uint64 // Key material marshaling errors

	// Routing failure counters
	PktRecvUnknownSocketId atomic.Uint64
	PktRecvNilConnection   atomic.Uint64
	PktRecvWrongPeer       atomic.Uint64
	PktRecvBacklogFull     atomic.Uint64

	// Resource exhaustion counters
	PktSentRingFull  atomic.Uint64
	PktRecvQueueFull atomic.Uint64

	// Lock timing (see Lock Timing Metrics section)
	HandlePacketLockTiming *LockTimingMetrics
	ReceiverLockTiming     *LockTimingMetrics
	SenderLockTiming       *LockTimingMetrics

	// Byte counters (for completeness)
	ByteRecvDataSuccess atomic.Uint64
	ByteRecvDataDropped atomic.Uint64
	ByteSentDataSuccess atomic.Uint64
	// ByteSentDataDropped atomic.Uint64 // Not implemented - send drops tracked via error counters

	// Special counters (from connStats migration)
	PktRecvUndecrypt  atomic.Uint64
	ByteRecvUndecrypt atomic.Uint64
	PktRecvInvalid    atomic.Uint64
	PktRetransFromNAK atomic.Uint64
	HeaderSize        atomic.Uint64
	MbpsLinkCapacity  atomic.Uint64 // Stored as uint64 (Mbps * 1000)

	// Congestion control - Receiver statistics
	CongestionRecvPkt              atomic.Uint64
	CongestionRecvByte             atomic.Uint64
	CongestionRecvPktUnique        atomic.Uint64
	CongestionRecvByteUnique       atomic.Uint64
	CongestionRecvPktLoss          atomic.Uint64
	CongestionRecvByteLoss         atomic.Uint64
	CongestionRecvPktRetrans       atomic.Uint64
	CongestionRecvByteRetrans      atomic.Uint64
	CongestionRecvPktBelated       atomic.Uint64
	CongestionRecvByteBelated      atomic.Uint64
	CongestionRecvPktDrop          atomic.Uint64
	CongestionRecvByteDrop         atomic.Uint64
	CongestionRecvPktBuf           atomic.Uint64
	CongestionRecvByteBuf          atomic.Uint64
	CongestionRecvMsBuf            atomic.Uint64
	CongestionRecvBytePayload      atomic.Uint64
	CongestionRecvMbpsBandwidth    atomic.Uint64 // Mbps * 1000
	CongestionRecvMbpsLinkCapacity atomic.Uint64 // Mbps * 1000
	CongestionRecvPktRetransRate   atomic.Uint64 // Retransmission rate: bytesRetrans/bytesRecv * 100 (NOT loss rate)

	// Duplicate packet tracking (defensive check in btree Insert)
	// Primary duplicate detection is in push.go; this catches edge cases
	CongestionRecvPktDuplicate  atomic.Uint64 // Duplicate data packets detected by btree
	CongestionRecvByteDuplicate atomic.Uint64 // Duplicate data bytes

	// NAK generation counters - Receiver sends NAKs to request retransmission
	// RFC SRT Appendix A defines two NAK encoding formats:
	// - Single packet (Figure 21): 4 bytes, bit 0 = 0
	// - Range of packets (Figure 22): 8 bytes, bit 0 of first = 1
	// These counters track PACKETS requested, not entries:
	//   NAKSingle + NAKRange = NAKPktsTotal = expected retransmissions
	CongestionRecvNAKSingle    atomic.Uint64 // Packets requested via single NAK entries (1 per entry)
	CongestionRecvNAKRange     atomic.Uint64 // Packets requested via range NAK entries (sum of range sizes)
	CongestionRecvNAKPktsTotal atomic.Uint64 // Total packets requested = NAKSingle + NAKRange

	// Congestion control - Sender statistics
	CongestionSendPkt                atomic.Uint64
	CongestionSendByte               atomic.Uint64
	CongestionSendPktUnique          atomic.Uint64
	CongestionSendByteUnique         atomic.Uint64
	CongestionSendPktLoss            atomic.Uint64
	CongestionSendByteLoss           atomic.Uint64
	CongestionSendPktRetrans         atomic.Uint64
	CongestionSendByteRetrans        atomic.Uint64
	CongestionSendUsSndDuration      atomic.Uint64
	CongestionSendPktDrop            atomic.Uint64
	CongestionSendByteDrop           atomic.Uint64
	CongestionSendPktBuf             atomic.Uint64
	CongestionSendByteBuf            atomic.Uint64
	CongestionSendMsBuf              atomic.Uint64
	CongestionSendPktFlightSize      atomic.Uint64
	CongestionSendUsPktSndPeriod     atomic.Uint64
	CongestionSendBytePayload        atomic.Uint64
	CongestionSendMbpsInputBandwidth atomic.Uint64 // Mbps * 1000
	CongestionSendMbpsSentBandwidth  atomic.Uint64 // Mbps * 1000
	CongestionSendPktRetransRate     atomic.Uint64 // Retransmission rate: bytesRetrans/bytesSent * 100 (NOT loss rate)

	// Additional error/drop counters for congestion control
	CongestionRecvPktNil               atomic.Uint64 // Nil packets received
	CongestionRecvPktStoreInsertFailed atomic.Uint64 // Packet store insertion failures
	// CongestionRecvDeliveryFailed       atomic.Uint64 // Not implemented - delivery callbacks don't fail
	// CongestionSendDeliveryFailed       atomic.Uint64 // Not implemented - delivery callbacks don't fail

	// Defensive counters for "should never happen" conditions (ISSUE-001)
	// These track programming errors or invalid state - should always be 0
	NakBtreeNilWhenEnabled    atomic.Uint64 // nakBtree nil when useNakBtree=true
	CongestionSendNAKNotFound atomic.Uint64 // NAK requests for packets not in lossList
	NakBeforeACKCount         atomic.Uint64 // NAK requests for already-ACK'd sequences (receiver bug indicator)

	// NAK receive counters - Sender receives NAKs and retransmits
	// RFC SRT Appendix A defines two NAK encoding formats:
	// - Single packet (Figure 21): 4 bytes, bit 0 = 0
	// - Range of packets (Figure 22): 8 bytes, bit 0 of first = 1
	// These counters track PACKETS requested, not entries:
	//   NAKSingleRecv + NAKRangeRecv = NAKPktsRecv = expected retransmissions
	CongestionSendNAKSingleRecv   atomic.Uint64 // Packets requested via single NAK entries (1 per entry)
	CongestionSendNAKRangeRecv    atomic.Uint64 // Packets requested via range NAK entries (sum of range sizes)
	CongestionSendNAKPktsRecv     atomic.Uint64 // Total packets requested = NAKSingleRecv + NAKRangeRecv
	CongestionSendNAKHonoredOrder atomic.Uint64 // NAK processing runs that honored receiver priority order

	// Granular drop counters - Congestion control (DATA packets only)
	CongestionRecvDataDropTooOld            atomic.Uint64 // Belated, past play time
	CongestionRecvDataDropAlreadyAcked      atomic.Uint64 // Already acknowledged
	CongestionRecvDataDropDuplicate         atomic.Uint64 // Duplicate (already in store)
	CongestionRecvDataDropStoreInsertFailed atomic.Uint64 // Store insert failed
	CongestionSendDataDropTooOld            atomic.Uint64 // Exceed drop threshold

	// TSBPD skip counters - Packets that NEVER arrived and were skipped when ACK advanced
	// These are distinct from "drops" which track packets that ARRIVED but were discarded
	CongestionRecvPktSkippedTSBPD        atomic.Uint64 // Packets skipped at TSBPD time (never arrived)
	CongestionRecvByteSkippedTSBPD       atomic.Uint64 // Bytes skipped (estimated from avgPayloadSize)
	ContiguousPointTSBPDAdvancements     atomic.Uint64 // Count of times contiguousPoint advanced due to TSBPD expiry
	ContiguousPointTSBPDSkippedPktsTotal atomic.Uint64 // Total packets skipped across all TSBPD advancements

	// Periodic timer tick counters - Track that timer routines are running
	// Used for health monitoring: should grow linearly with test duration
	// Expected: ACK ~100/sec (10ms interval), NAK ~50/sec (20ms interval)
	CongestionRecvPeriodicACKRuns atomic.Uint64 // Times periodicACK() actually ran
	CongestionRecvPeriodicNAKRuns atomic.Uint64 // Times periodicNAK() actually ran

	// NAK btree metrics - Core operations
	NakBtreeInserts     atomic.Uint64 // Sequences added to NAK btree
	NakBtreeDeletes     atomic.Uint64 // Sequences removed (packet arrived)
	NakBtreeExpired     atomic.Uint64 // Sequences removed (TSBPD expired)
	NakBtreeSize        atomic.Uint64 // Current size (gauge, updated each periodic NAK)
	NakBtreeScanPackets atomic.Uint64 // Packets scanned in periodicNakBtree()
	NakBtreeScanGaps    atomic.Uint64 // Gaps found during scan

	// NAK btree metrics - Periodic NAK execution
	NakPeriodicOriginalRuns atomic.Uint64 // Times periodicNakOriginal() executed
	NakPeriodicBtreeRuns    atomic.Uint64 // Times periodicNakBtree() executed
	NakPeriodicSkipped      atomic.Uint64 // Times skipped (interval not elapsed)

	// NAK btree metrics - Consolidation
	NakConsolidationRuns    atomic.Uint64 // Times consolidateNakBtree() ran
	NakConsolidationEntries atomic.Uint64 // Total entries produced by consolidation
	NakConsolidationMerged  atomic.Uint64 // Times adjacent sequences merged into ranges
	NakConsolidationTimeout atomic.Uint64 // Times consolidation hit time budget

	// Suppression metrics (future implementation - RTO-based)
	// These are placeholders for retransmission_and_nak_suppression_design.md
	NakSuppressedSeqs atomic.Uint64 // NAK entries skipped (already NAK'd recently, awaiting RTO)
	NakAllowedSeqs    atomic.Uint64 // NAK entries that passed RTO threshold
	RetransSuppressed atomic.Uint64 // Sender retransmissions skipped (already in flight)
	RetransAllowed    atomic.Uint64 // Sender retransmissions that passed threshold
	RetransFirstTime  atomic.Uint64 // First-time retransmissions (RetransmitCount was 0)

	// NAK btree metrics - FastNAK
	NakFastTriggers       atomic.Uint64 // Times FastNAK triggered after silence
	NakFastRecentInserts  atomic.Uint64 // Sequences added by FastNAKRecent jump detection
	NakFastRecentSkipped  atomic.Uint64 // FastNAKRecent skipped (gap too small)
	NakFastRecentOverflow atomic.Uint64 // FastNAKRecent gap too large (capped)

	// NAK packet splitting metrics (FR-11: MSS overflow handling)
	NakPacketsSplit atomic.Uint64 // Extra NAK packets needed due to MSS overflow

	// Granular error counters - Connection-level receive (DATA and Control packets)
	PktRecvDataErrorParse      atomic.Uint64 // DATA packet parse errors
	PktRecvControlErrorParse   atomic.Uint64 // Control packet parse errors
	PktRecvDataErrorIoUring    atomic.Uint64 // DATA packet io_uring errors
	PktRecvControlErrorIoUring atomic.Uint64 // Control packet io_uring errors
	PktRecvDataErrorEmpty      atomic.Uint64 // DATA packet empty datagrams
	PktRecvControlErrorEmpty   atomic.Uint64 // Control packet empty datagrams
	PktRecvDataErrorRoute      atomic.Uint64 // DATA packet routing failures
	PktRecvControlErrorRoute   atomic.Uint64 // Control packet routing failures

	// Granular error counters - Connection-level send (DATA and Control packets)
	PktSentDataErrorMarshal    atomic.Uint64 // DATA packet marshal errors
	PktSentControlErrorMarshal atomic.Uint64 // Control packet marshal errors
	PktSentDataRingFull        atomic.Uint64 // DATA packet ring full
	PktSentControlRingFull     atomic.Uint64 // Control packet ring full
	PktSentDataErrorSubmit     atomic.Uint64 // DATA packet submit errors
	PktSentControlErrorSubmit  atomic.Uint64 // Control packet submit errors
	PktSentDataErrorIoUring    atomic.Uint64 // DATA packet io_uring completion errors
	PktSentControlErrorIoUring atomic.Uint64 // Control packet io_uring completion errors

	// ========================================================================
	// Lock-Free Ring Buffer Metrics (Phase 3: Lockless Design)
	// ========================================================================
	// Tracks packet ring buffer operations and overflow conditions.

	RingDropsTotal       atomic.Uint64 // Packets dropped due to ring full (after backoff)
	RingDrainedPackets   atomic.Uint64 // Packets successfully drained from ring to btree
	RingPacketsProcessed atomic.Uint64 // Total packets consumed from ring (for delta calculation)

	// ========================================================================
	// Sender Tick Baseline Metrics (for burst detection comparison)
	// ========================================================================
	// Tracks baseline Tick() mode behavior for comparison with EventLoop.
	// These metrics enable Packets/Iteration ratio calculation to detect bursts.

	SendTickRuns             atomic.Uint64 // Number of Tick() invocations
	SendTickDeliveredPackets atomic.Uint64 // Packets delivered during Tick() calls

	// ========================================================================
	// Sender Lock-Free Ring Metrics (Phase 2: Lockless Sender)
	// ========================================================================
	// Tracks sender packet ring buffer operations.

	SendRingPushed       atomic.Uint64 // Packets successfully pushed to sender ring
	SendRingDropped      atomic.Uint64 // Packets dropped due to sender ring full
	SendRingDrained      atomic.Uint64 // Packets drained from sender ring to btree
	SendBtreeInserted    atomic.Uint64 // Packets inserted into sender btree
	SendBtreeDuplicates  atomic.Uint64 // Duplicate packets detected in sender btree
	SendRingDrainSeqGap  atomic.Uint64 // Sequence gaps detected during ring→btree drain (sender bug indicator)

	// ========================================================================
	// Sender Control Ring Metrics (Phase 3: Lockless Sender)
	// ========================================================================
	// Tracks control packet (ACK/NAK) ring buffer operations.

	SendControlRingPushedACK    atomic.Uint64 // ACKs successfully pushed to control ring
	SendControlRingPushedNAK    atomic.Uint64 // NAKs successfully pushed to control ring
	SendControlRingDroppedACK   atomic.Uint64 // ACKs dropped due to control ring full
	SendControlRingDroppedNAK   atomic.Uint64 // NAKs dropped due to control ring full
	SendControlRingDrained      atomic.Uint64 // Control packets drained by EventLoop
	SendControlRingProcessed    atomic.Uint64 // Control packets processed by EventLoop (total)
	SendControlRingProcessedACK atomic.Uint64 // ACKs processed by EventLoop
	SendControlRingProcessedNAK atomic.Uint64 // NAKs processed by EventLoop

	// ========================================================================
	// Sender EventLoop Metrics (Phase 4: Lockless Sender)
	// ========================================================================
	// Tracks sender EventLoop iterations, processing, and sleep behavior.

	// Startup diagnostics (debug intermittent failures)
	SendEventLoopStartAttempts   atomic.Uint64 // Times EventLoop() was called
	SendEventLoopSkippedDisabled atomic.Uint64 // Times EventLoop returned early (useEventLoop=false)
	SendEventLoopStarted         atomic.Uint64 // Times EventLoop entered main loop

	SendEventLoopIterations        atomic.Uint64 // Total EventLoop iterations
	SendEventLoopDefaultRuns       atomic.Uint64 // Default case runs (no timer fired)
	SendEventLoopDropFires         atomic.Uint64 // Drop ticker fires
	SendEventLoopDataDrained       atomic.Uint64 // Data packets drained from ring
	SendEventLoopControlDrained    atomic.Uint64 // Control packets drained from ring
	SendEventLoopACKsProcessed     atomic.Uint64 // ACKs processed by EventLoop
	SendEventLoopNAKsProcessed     atomic.Uint64 // NAKs processed by EventLoop
	SendEventLoopIdleBackoffs      atomic.Uint64 // Times EventLoop entered idle backoff
	// Diagnostic metrics for drain debugging
	SendEventLoopDrainAttempts     atomic.Uint64 // Times drain was called
	SendEventLoopDrainRingNil      atomic.Uint64 // Times packetRing was nil
	SendEventLoopDrainRingEmpty    atomic.Uint64 // Times TryPop returned empty (first try)
	SendEventLoopDrainRingHadData  atomic.Uint64 // Times ring.Len() > 0 before drain
	SendEventLoopTsbpdSleeps       atomic.Uint64 // Times EventLoop used TSBPD-aware sleep
	SendEventLoopEmptyBtreeSleeps  atomic.Uint64 // Times EventLoop slept due to empty btree
	SendEventLoopSleepClampedMin   atomic.Uint64 // Times sleep was clamped to minimum
	SendEventLoopSleepClampedMax   atomic.Uint64 // Times sleep was clamped to maximum
	SendEventLoopSleepTotalUs      atomic.Uint64 // Total sleep time in microseconds
	SendEventLoopNextDeliveryTotalUs atomic.Uint64 // Total next delivery time in microseconds
	SendDeliveryPackets            atomic.Uint64 // Packets delivered by EventLoop
	SendBtreeLen                   atomic.Uint64 // Current btree length (updated per iteration)
	// Delivery debugging metrics
	SendDeliveryAttempts       atomic.Uint64 // Times deliverReadyPacketsEventLoop was called
	SendDeliveryBtreeEmpty     atomic.Uint64 // Times btree was empty when trying to deliver
	SendDeliveryIterStarted    atomic.Uint64 // Times IterateFrom called callback (had packets)
	SendDeliveryTsbpdNotReady  atomic.Uint64 // Times first packet had tsbpdTime > nowUs
	SendDeliveryLastNowUs      atomic.Uint64 // Last nowUs value (for debugging)
	SendDeliveryLastTsbpd      atomic.Uint64 // Last first packet's tsbpdTime (for debugging)
	SendDeliveryStartSeq       atomic.Uint64 // Last deliveryStartPoint value (for debugging)
	SendDeliveryBtreeMinSeq    atomic.Uint64 // Btree min sequence (for debugging IterateFrom)
	SendDropAheadOfDelivery    atomic.Uint64 // Packets dropped that were ahead of deliveryStartPoint (head-of-line blocking)

	// ========================================================================
	// Zero-Copy Payload Pool Metrics (Phase 5: Lockless Sender)
	// ========================================================================
	// Tracks payload validation and buffer pool usage.
	SendPayloadSizeErrors atomic.Uint64 // Payloads rejected due to size validation

	// ========================================================================
	// Rate Calculation Fields (Phase 1: Lockless Design)
	// ========================================================================
	// These replace the embedded `rate struct` in congestion/live/receive.go and send.go
	// All values use atomic operations for lock-free access.
	// Float64 values are stored as uint64 using math.Float64bits/Float64frombits.

	// Receiver rate counters - accumulate between rate calculations
	RecvRatePeriodUs     atomic.Uint64 // Rate calculation period (microseconds), default 1s
	RecvRateLastUs       atomic.Uint64 // Last rate calculation time (microseconds since epoch)
	RecvRatePackets      atomic.Uint64 // Packets received in current period
	RecvRateBytes        atomic.Uint64 // Bytes received in current period
	RecvRateBytesRetrans atomic.Uint64 // Retransmitted bytes in current period

	// Receiver computed rates - updated atomically during rate calculation
	// Stored as uint64 using math.Float64bits(), read with math.Float64frombits()
	RecvRatePacketsPerSec  atomic.Uint64 // Packets per second (float64 bits)
	RecvRateBytesPerSec    atomic.Uint64 // Bytes per second (float64 bits)
	RecvRatePktRetransRate atomic.Uint64 // Retransmission rate percentage (float64 bits)

	// Sender rate counters - accumulate between rate calculations
	SendRatePeriodUs     atomic.Uint64 // Rate calculation period (microseconds), default 1s
	SendRateLastUs       atomic.Uint64 // Last rate calculation time (microseconds since epoch)
	SendRateBytes        atomic.Uint64 // Bytes input in current period
	SendRateBytesSent    atomic.Uint64 // Bytes actually sent in current period
	SendRateBytesRetrans atomic.Uint64 // Retransmitted bytes in current period

	// Sender computed rates - updated atomically during rate calculation
	// Stored as uint64 using math.Float64bits(), read with math.Float64frombits()
	SendRateEstInputBW     atomic.Uint64 // Estimated input bandwidth bytes/sec (float64 bits)
	SendRateEstSentBW      atomic.Uint64 // Estimated sent bandwidth bytes/sec (float64 bits)
	SendRatePktRetransRate atomic.Uint64 // Retransmission rate percentage (float64 bits)

	// Light ACK threshold counter - replaces nPackets in receiver
	// Used to determine when to send a "light" ACK vs full ACK
	RecvLightACKCounter atomic.Uint64 // Packets since last ACK (for light ACK threshold)

	// ========================================================================
	// EventLoop Metrics (Phase 4: ACK/ACKACK Redesign)
	// ========================================================================
	// Tracks EventLoop behavior for diagnosing ticker starvation issues.
	// Key diagnostic: DefaultRuns / FullACKFires ratio (expected ~1000)

	EventLoopIterations   atomic.Uint64 // Total loop iterations
	EventLoopFullACKFires atomic.Uint64 // Times fullACKTicker.C case executed
	EventLoopNAKFires     atomic.Uint64 // Times nakTicker.C case executed
	EventLoopRateFires    atomic.Uint64 // Times rateTicker.C case executed
	EventLoopDefaultRuns  atomic.Uint64 // Times default case executed
	EventLoopIdleBackoffs atomic.Uint64 // Times idle backoff sleep triggered

	// ========================================================================
	// ACK Btree Metrics (Phase 4: ACK/ACKACK Redesign)
	// ========================================================================
	// Tracks the ACK btree used for RTT calculation (stores sent Full ACKs awaiting ACKACK)

	AckBtreeSize           atomic.Uint64 // Current size of ack btree (gauge)
	AckBtreeEntriesExpired atomic.Uint64 // Entries expired by ExpireOlderThan
	AckBtreeUnknownACKACK  atomic.Uint64 // ACKACK received for unknown ackNum

	// ========================================================================
	// RTT Metrics (Phase 4: ACK/ACKACK Redesign)
	// ========================================================================
	// Current RTT values as gauges (stored as microseconds)

	RTTMicroseconds    atomic.Uint64 // Current RTT value in microseconds
	RTTVarMicroseconds atomic.Uint64 // Current RTT variance in microseconds

	// ========================================================================
	// io_uring Submission Metrics (Phase 5: WaitCQETimeout Implementation)
	// ========================================================================
	// Tracks each code path in the io_uring submission functions.
	// Key diagnostic: Success should match packet counts
	//                 RingFull/SubmitError should always be 0 (indicates ring sizing issue)

	// Send submission paths (connection_linux.go:sendIoUring)
	IoUringSendSubmitSuccess  atomic.Uint64 // Submit() succeeded
	IoUringSendSubmitRingFull atomic.Uint64 // GetSQE returned nil after retries (ring full)
	IoUringSendSubmitError    atomic.Uint64 // Submit() failed after retries
	IoUringSendGetSQERetries  atomic.Uint64 // GetSQE required retry (transient ring full)
	IoUringSendSubmitRetries  atomic.Uint64 // Submit() required retry (EINTR/EAGAIN)

	// Recv submission paths - Listener (listen_linux.go:submitRecvRequest)
	IoUringListenerRecvSubmitSuccess  atomic.Uint64 // Submit() succeeded
	IoUringListenerRecvSubmitRingFull atomic.Uint64 // GetSQE returned nil after retries
	IoUringListenerRecvSubmitError    atomic.Uint64 // Submit() failed after retries
	IoUringListenerRecvGetSQERetries  atomic.Uint64 // GetSQE required retry
	IoUringListenerRecvSubmitRetries  atomic.Uint64 // Submit() required retry

	// Recv submission paths - Dialer (dial_linux.go:submitRecvRequest)
	IoUringDialerRecvSubmitSuccess  atomic.Uint64 // Submit() succeeded
	IoUringDialerRecvSubmitRingFull atomic.Uint64 // GetSQE returned nil after retries
	IoUringDialerRecvSubmitError    atomic.Uint64 // Submit() failed after retries
	IoUringDialerRecvGetSQERetries  atomic.Uint64 // GetSQE required retry
	IoUringDialerRecvSubmitRetries  atomic.Uint64 // Submit() required retry

	// ========================================================================
	// io_uring Completion Handler Metrics (Phase 5: WaitCQETimeout Implementation)
	// ========================================================================
	// Tracks each code path in the io_uring completion handlers.
	// Key diagnostic: Success should match packet counts
	//                 Timeout indicates healthy timeout behavior
	//                 Error should always be 0

	// Send completion handler paths (connection_linux.go:sendCompletionHandler)
	IoUringSendCompletionSuccess      atomic.Uint64 // WaitCQETimeout returned a completion
	IoUringSendCompletionTimeout      atomic.Uint64 // ETIME: timeout expired (healthy)
	IoUringSendCompletionEBADF        atomic.Uint64 // Ring closed (normal shutdown)
	IoUringSendCompletionEINTR        atomic.Uint64 // Interrupted by signal
	IoUringSendCompletionError        atomic.Uint64 // Other unexpected errors
	IoUringSendCompletionCtxCancelled atomic.Uint64 // Context cancelled (shutdown)

	// Recv completion handler paths - Listener (listen_linux.go:getRecvCompletion)
	IoUringListenerRecvCompletionSuccess      atomic.Uint64 // WaitCQETimeout returned a completion
	IoUringListenerRecvCompletionTimeout      atomic.Uint64 // ETIME: timeout expired (healthy)
	IoUringListenerRecvCompletionEBADF        atomic.Uint64 // Ring closed (normal shutdown)
	IoUringListenerRecvCompletionEINTR        atomic.Uint64 // Interrupted by signal
	IoUringListenerRecvCompletionError        atomic.Uint64 // Other unexpected errors
	IoUringListenerRecvCompletionCtxCancelled atomic.Uint64 // Context cancelled (shutdown)

	// Recv completion handler paths - Dialer (dial_linux.go:getRecvCompletion)
	IoUringDialerRecvCompletionSuccess      atomic.Uint64 // WaitCQETimeout returned a completion
	IoUringDialerRecvCompletionTimeout      atomic.Uint64 // ETIME: timeout expired (healthy)
	IoUringDialerRecvCompletionEBADF        atomic.Uint64 // Ring closed (normal shutdown)
	IoUringDialerRecvCompletionEINTR        atomic.Uint64 // Interrupted by signal
	IoUringDialerRecvCompletionError        atomic.Uint64 // Other unexpected errors
	IoUringDialerRecvCompletionCtxCancelled atomic.Uint64 // Context cancelled (shutdown)
}

// ============================================================================
// Rate Metric Getter Helpers (Phase 1: Lockless Design)
// ============================================================================
// These encapsulate the math.Float64frombits() conversion for cleaner code.
// Use these in receiver.Stats(), sender.Stats(), and adaptive backoff.

// GetRecvRatePacketsPerSec returns packets per second as float64
func (m *ConnectionMetrics) GetRecvRatePacketsPerSec() float64 {
	return math.Float64frombits(m.RecvRatePacketsPerSec.Load())
}

// GetRecvRateBytesPerSec returns bytes per second as float64
func (m *ConnectionMetrics) GetRecvRateBytesPerSec() float64 {
	return math.Float64frombits(m.RecvRateBytesPerSec.Load())
}

// GetRecvRateMbps returns receive rate in megabits per second
func (m *ConnectionMetrics) GetRecvRateMbps() float64 {
	return m.GetRecvRateBytesPerSec() * 8 / 1024 / 1024
}

// GetRecvRateRetransPercent returns retransmission percentage
func (m *ConnectionMetrics) GetRecvRateRetransPercent() float64 {
	return math.Float64frombits(m.RecvRatePktRetransRate.Load())
}

// GetSendRateEstInputBW returns estimated input bandwidth in bytes/sec
func (m *ConnectionMetrics) GetSendRateEstInputBW() float64 {
	return math.Float64frombits(m.SendRateEstInputBW.Load())
}

// GetSendRateEstSentBW returns estimated sent bandwidth in bytes/sec
func (m *ConnectionMetrics) GetSendRateEstSentBW() float64 {
	return math.Float64frombits(m.SendRateEstSentBW.Load())
}

// GetSendRateMbps returns sent rate in megabits per second
func (m *ConnectionMetrics) GetSendRateMbps() float64 {
	return m.GetSendRateEstSentBW() * 8 / 1024 / 1024
}

// GetSendRateRetransPercent returns sender retransmission percentage
func (m *ConnectionMetrics) GetSendRateRetransPercent() float64 {
	return math.Float64frombits(m.SendRatePktRetransRate.Load())
}

// LockTimingMetrics tracks lock hold and wait times
// Uses lock-free ring buffer with atomic values for maximum performance
type LockTimingMetrics struct {
	// Recent hold time samples (nanoseconds) - each slot is atomic for lock-free reads
	holdTimeSamples [LockTimingSamples]atomic.Int64
	holdTimeIndex   atomic.Uint64 // Global write counter for circular buffer

	// Recent wait time samples (nanoseconds) - each slot is atomic for lock-free reads
	waitTimeSamples [LockTimingSamples]atomic.Int64
	waitTimeIndex   atomic.Uint64 // Global write counter for circular buffer

	// Max values (nanoseconds)
	maxHoldTime atomic.Int64
	maxWaitTime atomic.Int64
}

// RecordHoldTime records a lock hold time measurement
func (ltm *LockTimingMetrics) RecordHoldTime(duration time.Duration) {
	ns := duration.Nanoseconds()

	// Update max hold time (lock-free CAS loop)
	for {
		current := ltm.maxHoldTime.Load()
		if ns <= current {
			break
		}
		if ltm.maxHoldTime.CompareAndSwap(current, ns) {
			break
		}
	}

	// Store in circular buffer (lock-free)
	i := ltm.holdTimeIndex.Add(1) // Returns new value
	slot := i % LockTimingSamples
	ltm.holdTimeSamples[slot].Store(ns)
}

// RecordWaitTime records a lock wait time measurement
func (ltm *LockTimingMetrics) RecordWaitTime(duration time.Duration) {
	ns := duration.Nanoseconds()

	// Update max wait time (lock-free CAS loop)
	for {
		current := ltm.maxWaitTime.Load()
		if ns <= current {
			break
		}
		if ltm.maxWaitTime.CompareAndSwap(current, ns) {
			break
		}
	}

	// Store in circular buffer (lock-free)
	i := ltm.waitTimeIndex.Add(1) // Returns new value
	slot := i % LockTimingSamples
	ltm.waitTimeSamples[slot].Store(ns)
}

// GetTotalAcquisitions returns the total number of lock acquisitions
// Uses holdTimeIndex as a proxy (saves an atomic operation)
func (ltm *LockTimingMetrics) GetTotalAcquisitions() uint64 {
	// Use holdTimeIndex as it's incremented on every unlock
	// This is close enough for rate calculations
	return ltm.holdTimeIndex.Load()
}

// GetStats returns average and max hold/wait times
// All reads are lock-free (atomic operations only)
//
// Performance note: For 10 values, a simple loop is optimal.
// The Go compiler may auto-vectorize this, and the overhead of
// SIMD setup (if we used assembly/cgo) would likely exceed the benefit.
func (ltm *LockTimingMetrics) GetStats() (holdAvg, holdMax, waitAvg, waitMax float64) {
	// Snapshot hold time samples (lock-free atomic reads)
	var holdSum int64
	holdCount := 0
	for i := 0; i < LockTimingSamples; i++ {
		if sample := ltm.holdTimeSamples[i].Load(); sample > 0 {
			holdSum += sample
			holdCount++
		}
	}
	if holdCount > 0 {
		holdAvg = float64(holdSum) / float64(holdCount) / 1e9 // Convert to seconds
	}
	holdMax = float64(ltm.maxHoldTime.Load()) / 1e9 // Convert to seconds

	// Snapshot wait time samples (lock-free atomic reads)
	var waitSum int64
	waitCount := 0
	for i := 0; i < LockTimingSamples; i++ {
		if sample := ltm.waitTimeSamples[i].Load(); sample > 0 {
			waitSum += sample
			waitCount++
		}
	}
	if waitCount > 0 {
		waitAvg = float64(waitSum) / float64(waitCount) / 1e9 // Convert to seconds
	}
	waitMax = float64(ltm.maxWaitTime.Load()) / 1e9 // Convert to seconds

	return holdAvg, holdMax, waitAvg, waitMax
}

// SnapshotHoldTimes returns a snapshot of all hold time samples (for debugging)
func (ltm *LockTimingMetrics) SnapshotHoldTimes() [LockTimingSamples]int64 {
	var out [LockTimingSamples]int64
	for i := 0; i < LockTimingSamples; i++ {
		out[i] = ltm.holdTimeSamples[i].Load()
	}
	return out
}

// SnapshotWaitTimes returns a snapshot of all wait time samples (for debugging)
func (ltm *LockTimingMetrics) SnapshotWaitTimes() [LockTimingSamples]int64 {
	var out [LockTimingSamples]int64
	for i := 0; i < LockTimingSamples; i++ {
		out[i] = ltm.waitTimeSamples[i].Load()
	}
	return out
}

// NewConnectionMetrics creates a new ConnectionMetrics with LockTiming initialized.
// All atomic counters are zero-initialized automatically.
func NewConnectionMetrics() *ConnectionMetrics {
	return &ConnectionMetrics{
		HandlePacketLockTiming: &LockTimingMetrics{},
		ReceiverLockTiming:     &LockTimingMetrics{},
		SenderLockTiming:       &LockTimingMetrics{},
	}
}
