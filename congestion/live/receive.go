package live

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/congestion"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
)

// ReceiveConfig is the configuration for the liveRecv congestion control
type ReceiveConfig struct {
	InitialSequenceNumber  circular.Number
	PeriodicACKInterval    uint64 // microseconds
	PeriodicNAKInterval    uint64 // microseconds
	OnSendACK              func(seq circular.Number, light bool)
	OnSendNAK              func(list []circular.Number)
	OnDeliver              func(p packet.Packet)
	PacketReorderAlgorithm string                     // "list" (default) or "btree"
	BTreeDegree            int                        // B-tree degree (default: 32, only used if PacketReorderAlgorithm == "btree")
	LockTimingMetrics      *metrics.LockTimingMetrics // Optional lock timing metrics for performance monitoring
	ConnectionMetrics      *metrics.ConnectionMetrics // For atomic statistics updates

	// NAK btree configuration (Phase 4)
	UseNakBtree            bool    // Enable NAK btree for improved out-of-order handling
	SuppressImmediateNak   bool    // Suppress immediate NAK, let periodic NAK handle gaps
	TsbpdDelay             uint64  // Microseconds, for scan window calculation
	NakRecentPercent       float64 // Percentage of TSBPD delay for "recent" window (e.g., 0.10)
	NakMergeGap            uint32  // Maximum gap to merge into a single range
	NakConsolidationBudget uint64  // Microseconds, time budget for consolidation

	// FastNAK configuration
	FastNakEnabled       bool   // Enable FastNAK after silence
	FastNakThresholdUs   uint64 // Microseconds, silence threshold to trigger FastNAK
	FastNakRecentEnabled bool   // Enable FastNAKRecent to detect sequence jumps
}

// receiver implements the Receiver interface
type receiver struct {
	maxSeenSequenceNumber       circular.Number
	lastACKSequenceNumber       circular.Number
	lastDeliveredSequenceNumber circular.Number
	packetStore                 packetStore
	lock                        sync.RWMutex
	lockTiming                  *metrics.LockTimingMetrics // Optional lock timing metrics
	metrics                     *metrics.ConnectionMetrics // For atomic statistics updates

	nPackets uint

	periodicACKInterval uint64 // config
	periodicNAKInterval uint64 // config

	lastPeriodicACK uint64
	lastPeriodicNAK uint64

	avgPayloadSize  float64 // bytes
	avgLinkCapacity float64 // packets per second

	probeTime    time.Time
	probeNextSeq circular.Number

	rate struct {
		last   uint64 // microseconds
		period uint64

		packets      uint64
		bytes        uint64
		bytesRetrans uint64

		packetsPerSecond float64
		bytesPerSecond   float64

		pktRetransRate float64 // Retransmission rate (NOT loss rate)
	}

	sendACK func(seq circular.Number, light bool)
	sendNAK func(list []circular.Number)
	deliver func(p packet.Packet)

	// NAK btree fields (Phase 4)
	useNakBtree            bool
	suppressImmediateNak   bool
	nakBtree               *nakBtree
	tsbpdDelay             uint64 // Microseconds
	nakRecentPercent       float64
	nakMergeGap            uint32
	nakConsolidationBudget time.Duration

	// FastNAK fields
	fastNakEnabled       bool
	fastNakThreshold     time.Duration
	fastNakRecentEnabled bool

	// FastNAK tracking (atomic for lock-free access)
	lastPacketArrivalTime AtomicTime    // Time of last packet arrival
	lastNakTime           AtomicTime    // Time of last NAK sent
	lastDataPacketSeq     atomic.Uint32 // Last data packet sequence (for FastNAKRecent)
}

// NewReceiver takes a ReceiveConfig and returns a new Receiver
func NewReceiver(config ReceiveConfig) congestion.Receiver {
	// Choose packet store implementation based on config
	var store packetStore
	if config.PacketReorderAlgorithm == "btree" {
		degree := config.BTreeDegree
		if degree <= 0 {
			degree = 32 // Default btree degree
		}
		store = NewBTreePacketStore(degree)
	} else {
		// Default to list implementation
		store = NewListPacketStore()
	}

	r := &receiver{
		maxSeenSequenceNumber:       config.InitialSequenceNumber.Dec(),
		lastACKSequenceNumber:       config.InitialSequenceNumber.Dec(),
		lastDeliveredSequenceNumber: config.InitialSequenceNumber.Dec(),
		packetStore:                 store,
		lockTiming:                  config.LockTimingMetrics,
		metrics:                     config.ConnectionMetrics,

		periodicACKInterval: config.PeriodicACKInterval,
		periodicNAKInterval: config.PeriodicNAKInterval,

		avgPayloadSize: 1456, //  5.1.2. SRT's Default LiveCC Algorithm

		sendACK: config.OnSendACK,
		sendNAK: config.OnSendNAK,
		deliver: config.OnDeliver,

		// NAK btree configuration
		useNakBtree:            config.UseNakBtree,
		suppressImmediateNak:   config.SuppressImmediateNak,
		tsbpdDelay:             config.TsbpdDelay,
		nakRecentPercent:       config.NakRecentPercent,
		nakMergeGap:            config.NakMergeGap,
		nakConsolidationBudget: time.Duration(config.NakConsolidationBudget) * time.Microsecond,

		// FastNAK configuration
		fastNakEnabled:       config.FastNakEnabled,
		fastNakThreshold:     time.Duration(config.FastNakThresholdUs) * time.Microsecond,
		fastNakRecentEnabled: config.FastNakRecentEnabled,
	}

	// Create NAK btree if enabled
	if r.useNakBtree {
		degree := config.BTreeDegree
		if degree <= 0 {
			degree = 32 // Default btree degree
		}
		r.nakBtree = newNakBtree(degree)
	}

	if r.sendACK == nil {
		r.sendACK = func(seq circular.Number, light bool) {}
	}

	if r.sendNAK == nil {
		r.sendNAK = func(list []circular.Number) {}
	}

	if r.deliver == nil {
		r.deliver = func(p packet.Packet) {}
	}

	r.rate.last = 0
	r.rate.period = uint64(time.Second.Microseconds())

	return r
}

func (r *receiver) Stats() congestion.ReceiveStats {
	// Read lock only for rate calculations (not for statistics)
	r.lock.RLock()
	bytePayload := uint64(r.avgPayloadSize)
	mbpsBandwidth := r.rate.bytesPerSecond * 8 / 1024 / 1024
	mbpsLinkCapacity := r.avgLinkCapacity * packet.MAX_PAYLOAD_SIZE * 8 / 1024 / 1024
	pktRetransRate := r.rate.pktRetransRate
	r.lock.RUnlock()

	// Metrics are always available (initialized in connection.go before NewReceiver)
	m := r.metrics

	// Update atomic counters for instantaneous/calculated values
	m.CongestionRecvBytePayload.Store(bytePayload)
	m.CongestionRecvMbpsBandwidth.Store(uint64(mbpsBandwidth * 1000))
	m.CongestionRecvMbpsLinkCapacity.Store(uint64(mbpsLinkCapacity * 1000))
	m.CongestionRecvPktRetransRate.Store(uint64(pktRetransRate * 100))

	// Build return struct from atomic counters (lock-free reads)
	return congestion.ReceiveStats{
		Pkt:         m.CongestionRecvPkt.Load(),
		Byte:        m.CongestionRecvByte.Load(),
		PktUnique:   m.CongestionRecvPktUnique.Load(),
		ByteUnique:  m.CongestionRecvByteUnique.Load(),
		PktLoss:     m.CongestionRecvPktLoss.Load(),
		ByteLoss:    m.CongestionRecvByteLoss.Load(),
		PktRetrans:  m.CongestionRecvPktRetrans.Load(),
		ByteRetrans: m.CongestionRecvByteRetrans.Load(),
		PktBelated:  m.CongestionRecvPktBelated.Load(),
		ByteBelated: m.CongestionRecvByteBelated.Load(),
		PktDrop: m.CongestionRecvDataDropTooOld.Load() +
			m.CongestionRecvDataDropAlreadyAcked.Load() +
			m.CongestionRecvDataDropDuplicate.Load() +
			m.CongestionRecvDataDropStoreInsertFailed.Load(),
		ByteDrop:                   m.CongestionRecvByteDrop.Load(), // ByteDrop is maintained by helper functions
		PktBuf:                     m.CongestionRecvPktBuf.Load(),
		ByteBuf:                    m.CongestionRecvByteBuf.Load(),
		MsBuf:                      m.CongestionRecvMsBuf.Load(),
		BytePayload:                bytePayload,
		MbpsEstimatedRecvBandwidth: mbpsBandwidth,
		MbpsEstimatedLinkCapacity:  mbpsLinkCapacity,
		PktRetransRate:             pktRetransRate,
	}
}

func (r *receiver) PacketRate() (pps, bps, capacity float64) {
	r.lock.Lock()
	defer r.lock.Unlock()

	pps = r.rate.packetsPerSecond
	bps = r.rate.bytesPerSecond
	capacity = r.avgLinkCapacity

	return
}

func (r *receiver) Flush() {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.packetStore.Clear()
}

func (r *receiver) Push(pkt packet.Packet) {
	if r.lockTiming != nil {
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			r.pushLocked(pkt)
		})
		return
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	r.pushLocked(pkt)
}

func (r *receiver) pushLocked(pkt packet.Packet) {
	// Dispatch to appropriate implementation based on NAK btree mode
	if r.useNakBtree {
		r.pushLockedNakBtree(pkt)
		return
	}
	r.pushLockedOriginal(pkt)
}

// pushLockedNakBtree handles packet arrival when NAK btree is enabled (io_uring path).
// Key difference: NO gap detection or immediate NAK.
// With io_uring, packets arrive out of order, so gap detection would cause false positives.
// Instead, the btree sorts packets automatically, and periodicNakBtree scans for real gaps.
func (r *receiver) pushLockedNakBtree(pkt packet.Packet) {
	m := r.metrics

	if pkt == nil {
		m.CongestionRecvPktNil.Add(1)
		return
	}

	now := time.Now()
	seq := pkt.Header().PacketSequenceNumber.Val()

	// FastNAK tracking: detect outage recovery
	if r.fastNakEnabled && r.fastNakRecentEnabled {
		r.checkFastNakRecent(seq, now)
	}

	r.nPackets++
	pktLen := pkt.Len()
	r.rate.packets++
	r.rate.bytes += pktLen

	m.CongestionRecvPkt.Add(1)
	m.CongestionRecvByte.Add(uint64(pktLen))

	if pkt.Header().RetransmittedPacketFlag {
		m.CongestionRecvPktRetrans.Add(1)
		m.CongestionRecvByteRetrans.Add(uint64(pktLen))
		r.rate.bytesRetrans += pktLen
	}

	// 5.1.2. SRT's Default LiveCC Algorithm
	r.avgPayloadSize = 0.875*r.avgPayloadSize + 0.125*float64(pktLen)

	// Check if too old (already delivered)
	if pkt.Header().PacketSequenceNumber.Lte(r.lastDeliveredSequenceNumber) {
		m.CongestionRecvPktBelated.Add(1)
		m.CongestionRecvByteBelated.Add(uint64(pktLen))
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
		return
	}

	// Check if already acknowledged
	if pkt.Header().PacketSequenceNumber.Lt(r.lastACKSequenceNumber) {
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonAlreadyAcked, uint64(pktLen))
		return
	}

	// Check for duplicate (already in store)
	if r.packetStore.Has(pkt.Header().PacketSequenceNumber) {
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonDuplicate, uint64(pktLen))
		return
	}

	// Delete from NAK btree - this packet is no longer missing
	if r.nakBtree != nil {
		if r.nakBtree.Delete(seq) {
			m.NakBtreeDeletes.Add(1)
		}
	}

	// Insert into packet btree (btree handles ordering automatically)
	if r.packetStore.Insert(pkt) {
		m.CongestionRecvPktBuf.Add(1)
		m.CongestionRecvPktUnique.Add(1)
		m.CongestionRecvByteBuf.Add(uint64(pktLen))
		m.CongestionRecvByteUnique.Add(uint64(pktLen))
	} else {
		m.CongestionRecvPktStoreInsertFailed.Add(1)
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonStoreInsertFailed, uint64(pktLen))
	}

	// Update FastNAK tracking (after packet is accepted)
	r.lastPacketArrivalTime.Store(now)
	r.lastDataPacketSeq.Store(seq)

	// NOTE: No gap detection, no immediate NAK, no maxSeenSequenceNumber tracking
	// Gaps are detected by periodicNakBtree() which scans the packet btree
}

// pushLockedOriginal is the original implementation with gap detection and immediate NAK.
// Used when NAK btree is disabled (non-io_uring path).
func (r *receiver) pushLockedOriginal(pkt packet.Packet) {
	// Check metrics once at the beginning of the function
	m := r.metrics

	if pkt == nil {
		m.CongestionRecvPktNil.Add(1)
		return
	}

	// This is not really well (not at all) described in the specs. See core.cpp and window.h
	// and search for PUMASK_SEQNO_PROBE (0xF). Every 16th and 17th packet are
	// sent in pairs. This is used as a probe for the theoretical capacity of the link.
	if !pkt.Header().RetransmittedPacketFlag {
		probe := pkt.Header().PacketSequenceNumber.Val() & 0xF
		switch probe {
		case 0:
			r.probeTime = time.Now()
			r.probeNextSeq = pkt.Header().PacketSequenceNumber.Inc()
		case 1:
			if pkt.Header().PacketSequenceNumber.Equals(r.probeNextSeq) && !r.probeTime.IsZero() && pkt.Len() != 0 {
				// The time between packets scaled to a fully loaded packet
				diff := float64(time.Since(r.probeTime).Microseconds()) * (packet.MAX_PAYLOAD_SIZE / float64(pkt.Len()))
				if diff != 0 {
					// Here we're doing an average of the measurements.
					r.avgLinkCapacity = 0.875*r.avgLinkCapacity + 0.125*1_000_000/diff
				}
			} else {
				r.probeTime = time.Time{}
			}
		default:
			r.probeTime = time.Time{}
		}
	} else {
		r.probeTime = time.Time{}
	}

	r.nPackets++

	pktLen := pkt.Len()

	r.rate.packets++
	r.rate.bytes += pktLen

	m.CongestionRecvPkt.Add(1)
	m.CongestionRecvByte.Add(uint64(pktLen))

	//pkt.PktTsbpdTime = pkt.Timestamp + r.delay
	if pkt.Header().RetransmittedPacketFlag {
		m.CongestionRecvPktRetrans.Add(1)
		m.CongestionRecvByteRetrans.Add(uint64(pktLen))

		r.rate.bytesRetrans += pktLen
	}

	//  5.1.2. SRT's Default LiveCC Algorithm
	r.avgPayloadSize = 0.875*r.avgPayloadSize + 0.125*float64(pktLen)

	if pkt.Header().PacketSequenceNumber.Lte(r.lastDeliveredSequenceNumber) {
		// Too old, because up until r.lastDeliveredSequenceNumber, we already delivered
		m.CongestionRecvPktBelated.Add(1)
		m.CongestionRecvByteBelated.Add(uint64(pktLen))
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
		return
	}

	if pkt.Header().PacketSequenceNumber.Lt(r.lastACKSequenceNumber) {
		// Already acknowledged, ignoring
		metrics.IncrementRecvDataDrop(m, metrics.DropReasonAlreadyAcked, uint64(pktLen))
		return
	}

	if pkt.Header().PacketSequenceNumber.Equals(r.maxSeenSequenceNumber.Inc()) {
		// In order, the packet we expected
		r.maxSeenSequenceNumber = pkt.Header().PacketSequenceNumber
	} else if pkt.Header().PacketSequenceNumber.Lte(r.maxSeenSequenceNumber) {
		// Out of order, is it a missing piece? put it in the correct position
		if r.packetStore.Has(pkt.Header().PacketSequenceNumber) {
			// Already received (has been sent more than once), ignoring
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonDuplicate, uint64(pktLen))
			return
		}

		// Insert in correct position (packetStore handles ordering)
		if r.packetStore.Insert(pkt) {
			// Late arrival, this fills a gap
			m.CongestionRecvPktBuf.Add(1)
			m.CongestionRecvPktUnique.Add(1)
			m.CongestionRecvByteBuf.Add(uint64(pktLen))
			m.CongestionRecvByteUnique.Add(uint64(pktLen))
		} else {
			// Duplicate (shouldn't happen after Has check, but be safe)
			m.CongestionRecvPktStoreInsertFailed.Add(1)
			metrics.IncrementRecvDataDrop(m, metrics.DropReasonStoreInsertFailed, uint64(pktLen))
		}

		return
	} else {
		// Too far ahead, there are some missing sequence numbers, immediate NAK report.
		// TODO: Implement SRTO_LOSSMAXTTL to delay NAK for reordered packets.
		nakList := []circular.Number{
			r.maxSeenSequenceNumber.Inc(),
			pkt.Header().PacketSequenceNumber.Dec(),
		}
		r.sendNAK(nakList)

		// Count packets requested by this NAK using shared helper.
		// This ensures 100% consistency with how the sender counts received NAKs.
		// Note: The helper correctly handles both single (start==end) and range entries.
		missingPkts := metrics.CountNAKEntries(m, nakList, metrics.NAKCounterSend)

		// Update loss counters with the correct packet count
		m.CongestionRecvPktLoss.Add(missingPkts)
		m.CongestionRecvByteLoss.Add(missingPkts * uint64(r.avgPayloadSize))

		r.maxSeenSequenceNumber = pkt.Header().PacketSequenceNumber
	}

	m.CongestionRecvPktBuf.Add(1)
	m.CongestionRecvPktUnique.Add(1)
	m.CongestionRecvByteBuf.Add(uint64(pktLen))
	m.CongestionRecvByteUnique.Add(uint64(pktLen))

	r.packetStore.Insert(pkt)
}

func (r *receiver) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
	// Phase 1: Read-only work with read lock (allows concurrent Push() operations)
	r.lock.RLock()

	// Early return check (read-only)
	needLiteACK := false
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		if r.nPackets >= 64 {
			needLiteACK = true // Will send light ACK, but can't update nPackets yet
		} else {
			r.lock.RUnlock()
			return // Early return - no ACK needed
		}
	}

	// Read-only iteration (read lock allows concurrent Push() operations)
	minPktTsbpdTime, maxPktTsbpdTime := uint64(0), uint64(0)
	ackSequenceNumber := r.lastACKSequenceNumber

	// Get first packet for initial timestamps
	minPkt := r.packetStore.Min()
	if minPkt != nil {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		minH := minPkt.Header()
		minPktTsbpdTime = minH.PktTsbpdTime
		maxPktTsbpdTime = minH.PktTsbpdTime
	}

	// Find the sequence number up until we have all in a row.
	// Where the first gap is (or at the end of the list) is where we can ACK to.
	// Track packets skipped due to TSBPD timeout (never arrived, gap in sequence)
	m := r.metrics
	r.packetStore.Iterate(func(p packet.Packet) bool {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()

		// Skip packets that we already ACK'd.
		if h.PacketSequenceNumber.Lte(ackSequenceNumber) {
			return true // Continue
		}

		// If there are packets that should have been delivered by now, move forward.
		// This is where we "skip" packets that NEVER arrived - count them!
		if h.PktTsbpdTime <= now {
			// Count packets skipped: gap between current ACK and this packet
			// e.g., if ackSequenceNumber=10 and h.PacketSequenceNumber=15,
			// then packets 11,12,13,14 are being skipped (4 packets)
			if m != nil {
				skippedCount := uint64(h.PacketSequenceNumber.Distance(ackSequenceNumber))
				if skippedCount > 1 {
					// skippedCount-1 because Distance(10,15)=5, but we skip 11,12,13,14 (4 packets)
					actualSkipped := skippedCount - 1
					m.CongestionRecvPktSkippedTSBPD.Add(actualSkipped)
					m.CongestionRecvByteSkippedTSBPD.Add(actualSkipped * uint64(r.avgPayloadSize))
				}
			}
			ackSequenceNumber = h.PacketSequenceNumber
			return true // Continue
		}

		// Check if the packet is the next in the row.
		if h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
			ackSequenceNumber = h.PacketSequenceNumber
			maxPktTsbpdTime = h.PktTsbpdTime
			return true // Continue
		}

		return false // Stop iteration
	})

	// Release read lock before acquiring write lock (optimization: minimize lock contention)
	r.lock.RUnlock()

	// Phase 2: Write updates with write lock (brief - only for field updates)
	// Measure lock timing for the write lock (critical section)
	if r.lockTiming != nil {
		var okResult bool
		var seqResult circular.Number
		var liteResult bool
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			okResult, seqResult, liteResult = r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime)
		})
		return okResult, seqResult, liteResult
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber, minPktTsbpdTime, maxPktTsbpdTime)
}

func (r *receiver) periodicACKWriteLocked(now uint64, needLiteACK bool, ackSequenceNumber circular.Number, minPktTsbpdTime, maxPktTsbpdTime uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
	// Check metrics once at the beginning of the function
	m := r.metrics

	// Re-check conditions (may have changed between read and write lock)
	// If interval check still applies and we don't need lite ACK, return early
	if now-r.lastPeriodicACK < r.periodicACKInterval {
		if !needLiteACK {
			return // Early return - no update needed
		}
		// Lite ACK needed, continue to update fields
		lite = true
	}

	// Track that periodicACK actually ran (not just returned early)
	// Used for health monitoring: expected ~100/sec (10ms interval)
	if m != nil {
		m.CongestionRecvPeriodicACKRuns.Add(1)
	}

	// Update fields (write lock held - brief operation)
	ok = true
	sequenceNumber = ackSequenceNumber.Inc()

	// Keep track of the last ACK's sequence number. With this we can faster ignore
	// packets that come in late that have a lower sequence number.
	r.lastACKSequenceNumber = ackSequenceNumber

	r.lastPeriodicACK = now
	r.nPackets = 0

	msBuf := (maxPktTsbpdTime - minPktTsbpdTime) / 1_000
	m.CongestionRecvMsBuf.Store(msBuf)

	return
}

func (r *receiver) periodicNAK(now uint64) []circular.Number {
	// Dispatch to appropriate implementation
	if r.useNakBtree {
		return r.periodicNakBtree(now)
	}
	return r.periodicNakOriginal(now)
}

// periodicNakOriginal is the original implementation that iterates through the packet store.
func (r *receiver) periodicNakOriginal(now uint64) []circular.Number {
	if r.lockTiming != nil {
		var result []circular.Number
		metrics.WithRLockTiming(r.lockTiming, &r.lock, func() {
			result = r.periodicNakOriginalLocked(now)
		})
		return result
	}
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.periodicNakOriginalLocked(now)
}

// periodicNakOriginalLocked builds the NAK loss list by iterating through the packet store.
// RFC SRT Appendix A defines two NAK encoding formats:
// - Figure 21: Single sequence number (start == end) - 4 bytes on wire
// - Figure 22: Range of sequence numbers (start != end) - 8 bytes on wire
// The list contains pairs [start, end] for each gap found.
func (r *receiver) periodicNakOriginalLocked(now uint64) []circular.Number {
	if now-r.lastPeriodicNAK < r.periodicNAKInterval {
		return nil
	}

	// Track that periodicNAK actually ran (not just returned early)
	// Used for health monitoring: expected ~50/sec (20ms interval)
	m := r.metrics
	if m != nil {
		m.CongestionRecvPeriodicNAKRuns.Add(1)
		m.NakPeriodicOriginalRuns.Add(1)
	}

	list := []circular.Number{}

	// Send a periodic NAK

	ackSequenceNumber := r.lastACKSequenceNumber

	// Send a NAK for all gaps.
	// Not all gaps might get announced because the size of the NAK packet is limited.
	r.packetStore.Iterate(func(p packet.Packet) bool {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()

		// Skip packets that we already ACK'd.
		if h.PacketSequenceNumber.Lte(ackSequenceNumber) {
			return true // Continue
		}

		// If this packet is not in sequence, we stop here and report that gap.
		if !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
			nackSequenceNumber := ackSequenceNumber.Inc()

			list = append(list, nackSequenceNumber)
			list = append(list, h.PacketSequenceNumber.Dec())
		}

		ackSequenceNumber = h.PacketSequenceNumber
		return true // Continue
	})

	r.lastPeriodicNAK = now

	return list
}

// periodicNakBtree scans the packet btree to find gaps and builds NAK list.
// This is the new implementation for handling out-of-order packets with io_uring.
//
// Algorithm:
// 1. Scan packet btree from last ACK'd sequence
// 2. For each gap in the sequence, add missing seqs to NAK btree
// 3. Skip packets that are "too recent" (might still be in flight)
// 4. Consolidate NAK btree into ranges and return
func (r *receiver) periodicNakBtree(now uint64) []circular.Number {
	r.lock.RLock()
	defer r.lock.RUnlock()

	if now-r.lastPeriodicNAK < r.periodicNAKInterval {
		return nil
	}

	// Track that periodicNAK actually ran
	m := r.metrics
	if m != nil {
		m.CongestionRecvPeriodicNAKRuns.Add(1)
		m.NakPeriodicBtreeRuns.Add(1)
	}

	if r.nakBtree == nil {
		return nil
	}

	// Step 1: Calculate "too recent" threshold
	// Packets with TSBPD beyond this are too new to NAK (might be reordered, not lost)
	tooRecentThreshold := now
	if r.nakRecentPercent > 0 && r.tsbpdDelay > 0 {
		tooRecentThreshold = now + uint64(float64(r.tsbpdDelay)*r.nakRecentPercent)
	}

	// Step 2: Scan packet btree from last ACK'd sequence to find gaps
	expectedSeq := r.lastACKSequenceNumber.Inc()

	r.packetStore.Iterate(func(pkt packet.Packet) bool {
		h := pkt.Header()

		// Skip packets already ACK'd
		if h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) {
			return true // Continue
		}

		// Stop if this packet is "too recent" (might still be reordered)
		if h.PktTsbpdTime > tooRecentThreshold {
			return false // Stop iteration
		}

		actualSeq := h.PacketSequenceNumber

		// Detect gaps: expected vs actual
		if actualSeq.Gt(expectedSeq) {
			// There's a gap - add missing sequences to NAK btree
			seq := expectedSeq.Val()
			endSeq := actualSeq.Dec().Val()
			for circular.SeqLess(seq, endSeq) || seq == endSeq {
				r.nakBtree.Insert(seq)
				m.NakBtreeInserts.Add(1)
				m.NakBtreeScanGaps.Add(1)
				seq = circular.SeqAdd(seq, 1)
			}
		}

		m.NakBtreeScanPackets.Add(1)

		expectedSeq = actualSeq.Inc()
		return true // Continue
	})

	// Step 3: Consolidate NAK btree into ranges using optimized function
	// (uses sync.Pool and time budget)
	list := r.consolidateNakBtree()

	// Update NAK btree size gauge
	if m != nil {
		m.NakBtreeSize.Store(uint64(r.nakBtree.Len()))
	}

	r.lastPeriodicNAK = now

	return list
}

func (r *receiver) Tick(now uint64) {
	if ok, sequenceNumber, lite := r.periodicACK(now); ok {
		r.sendACK(sequenceNumber, lite)
	}

	if list := r.periodicNAK(now); len(list) != 0 {
		// Count NAK entries using shared helper before sending.
		// This ensures 100% consistency with how the sender counts received NAKs.
		// RFC SRT Appendix A:
		//   - Figure 21: Single (start == end) - 4 bytes on wire
		//   - Figure 22: Range (start != end) - 8 bytes on wire
		metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
		r.sendNAK(list)
	}

	// Deliver packets whose PktTsbpdTime is ripe
	// Capture metrics once to avoid repeated checks in closures
	m := r.metrics
	if r.lockTiming != nil {
		var removed int
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			removed = r.packetStore.RemoveAll(
				func(p packet.Packet) bool {
					// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
					h := p.Header()
					return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
				},
				func(p packet.Packet) {
					m.CongestionRecvPktBuf.Add(^uint64(0))                    // Decrement by 1
					m.CongestionRecvByteBuf.Add(^uint64(uint64(p.Len()) - 1)) // Subtract pktLen
					// PktBuf and ByteBuf are decremented in atomic counters above

					// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
					h := p.Header()
					r.lastDeliveredSequenceNumber = h.PacketSequenceNumber

					r.deliver(p)
				},
			)
		})
		_ = removed
	} else {
		r.lock.Lock()
		removed := r.packetStore.RemoveAll(
			func(p packet.Packet) bool {
				// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
				h := p.Header()
				return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
			},
			func(p packet.Packet) {
				m.CongestionRecvPktBuf.Add(^uint64(0))                    // Decrement by 1
				m.CongestionRecvByteBuf.Add(^uint64(uint64(p.Len()) - 1)) // Subtract pktLen

				// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
				h := p.Header()
				r.lastDeliveredSequenceNumber = h.PacketSequenceNumber

				r.deliver(p)
			},
		)
		r.lock.Unlock()
		_ = removed
	}

	// Update rate statistics
	if r.lockTiming != nil {
		metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
			r.updateRateStats(now)
		})
	} else {
		r.lock.Lock()
		r.updateRateStats(now)
		r.lock.Unlock()
	}
}

func (r *receiver) updateRateStats(now uint64) {
	tdiff := now - r.rate.last // microseconds

	if tdiff > r.rate.period {
		r.rate.packetsPerSecond = float64(r.rate.packets) / (float64(tdiff) / 1000 / 1000)
		r.rate.bytesPerSecond = float64(r.rate.bytes) / (float64(tdiff) / 1000 / 1000)
		if r.rate.bytes != 0 {
			r.rate.pktRetransRate = float64(r.rate.bytesRetrans) / float64(r.rate.bytes) * 100
		} else {
			r.rate.bytes = 0
		}

		r.rate.packets = 0
		r.rate.bytes = 0
		r.rate.bytesRetrans = 0

		r.rate.last = now
	}
}

func (r *receiver) SetNAKInterval(nakInterval uint64) {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.periodicNAKInterval = nakInterval
}

func (r *receiver) String(t uint64) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("maxSeen=%d lastACK=%d lastDelivered=%d\n", r.maxSeenSequenceNumber.Val(), r.lastACKSequenceNumber.Val(), r.lastDeliveredSequenceNumber.Val()))

	r.lock.RLock()
	r.packetStore.Iterate(func(p packet.Packet) bool {
		// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
		h := p.Header()
		b.WriteString(fmt.Sprintf("   %d @ %d (in %d)\n", h.PacketSequenceNumber.Val(), h.PktTsbpdTime, int64(h.PktTsbpdTime)-int64(t)))
		return true // Continue
	})
	r.lock.RUnlock()

	return b.String()
}
