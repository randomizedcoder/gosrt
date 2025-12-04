package metrics

import (
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
	PktSentDataDropped atomic.Uint64
	PktSentDataError   atomic.Uint64

	// Control packet counters - ACK
	PktRecvACKSuccess atomic.Uint64
	PktRecvACKDropped atomic.Uint64
	PktRecvACKError   atomic.Uint64
	PktSentACKSuccess atomic.Uint64
	PktSentACKDropped atomic.Uint64
	PktSentACKError   atomic.Uint64

	// Control packet counters - ACKACK
	PktRecvACKACKSuccess atomic.Uint64
	PktRecvACKACKDropped atomic.Uint64
	PktRecvACKACKError   atomic.Uint64
	PktSentACKACKSuccess atomic.Uint64
	PktSentACKACKDropped atomic.Uint64
	PktSentACKACKError   atomic.Uint64

	// Control packet counters - NAK
	PktRecvNAKSuccess atomic.Uint64
	PktRecvNAKDropped atomic.Uint64
	PktRecvNAKError   atomic.Uint64
	PktSentNAKSuccess atomic.Uint64
	PktSentNAKDropped atomic.Uint64
	PktSentNAKError   atomic.Uint64

	// Control packet counters - KM
	PktRecvKMSuccess atomic.Uint64
	PktRecvKMDropped atomic.Uint64
	PktRecvKMError   atomic.Uint64
	PktSentKMSuccess atomic.Uint64
	PktSentKMDropped atomic.Uint64
	PktSentKMError   atomic.Uint64

	// Control packet counters - Keepalive
	PktRecvKeepaliveSuccess atomic.Uint64
	PktRecvKeepaliveDropped atomic.Uint64
	PktRecvKeepaliveError   atomic.Uint64
	PktSentKeepaliveSuccess atomic.Uint64
	PktSentKeepaliveDropped atomic.Uint64
	PktSentKeepaliveError   atomic.Uint64

	// Control packet counters - Shutdown
	PktRecvShutdownSuccess atomic.Uint64
	PktRecvShutdownDropped atomic.Uint64
	PktRecvShutdownError   atomic.Uint64
	PktSentShutdownSuccess atomic.Uint64
	PktSentShutdownDropped atomic.Uint64
	PktSentShutdownError   atomic.Uint64

	// Control packet counters - Handshake
	PktRecvHandshakeSuccess atomic.Uint64
	PktRecvHandshakeDropped atomic.Uint64
	PktRecvHandshakeError   atomic.Uint64
	PktSentHandshakeSuccess atomic.Uint64
	PktSentHandshakeDropped atomic.Uint64
	PktSentHandshakeError   atomic.Uint64

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
	ByteSentDataDropped atomic.Uint64

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
	CongestionRecvPktLossRate      atomic.Uint64 // Percentage * 100

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
	CongestionSendPktLossRate        atomic.Uint64 // Percentage * 100

	// Additional error/drop counters for congestion control
	CongestionRecvPktNil               atomic.Uint64 // Nil packets received
	CongestionRecvPktStoreInsertFailed atomic.Uint64 // Packet store insertion failures
	CongestionRecvDeliveryFailed       atomic.Uint64 // Delivery callback failures
	CongestionSendDeliveryFailed       atomic.Uint64 // Delivery callback failures
	CongestionSendNAKNotFound          atomic.Uint64 // NAK requests for packets not in lossList

	// Granular drop counters - Congestion control (DATA packets only)
	CongestionRecvDataDropTooOld            atomic.Uint64 // Belated, past play time
	CongestionRecvDataDropAlreadyAcked      atomic.Uint64 // Already acknowledged
	CongestionRecvDataDropDuplicate         atomic.Uint64 // Duplicate (already in store)
	CongestionRecvDataDropStoreInsertFailed atomic.Uint64 // Store insert failed
	CongestionSendDataDropTooOld            atomic.Uint64 // Exceed drop threshold

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
