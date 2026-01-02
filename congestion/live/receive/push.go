package receive

// push.go - Packet push path functions
// Extracted from receiver.go for better organization

import (
	"math"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (r *receiver) Push(pkt packet.Packet) {
	// Phase 3: Lockless - Use function dispatch for ring vs locked path
	r.pushFn(pkt)
}

// pushWithLock is the legacy locked path (UsePacketRing=false)
// This wraps the existing pushLocked behavior with optional lock timing metrics.
func (r *receiver) pushWithLock(pkt packet.Packet) {
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

// pushToRing is the new lock-free path (UsePacketRing=true)
// Writes packet to lock-free ring buffer for later processing by Tick().
// This decouples packet arrival (io_uring completion) from processing (event loop).
func (r *receiver) pushToRing(pkt packet.Packet) {
	// Rate metrics (always atomic - Phase 1)
	m := r.metrics
	m.RecvLightACKCounter.Add(1) // Used for Light ACK triggering until Phase 5 complete
	m.RecvRatePackets.Add(1)
	m.RecvRateBytes.Add(pkt.Len())

	// Use packet sequence number for shard selection (distributes load)
	producerID := uint64(pkt.Header().PacketSequenceNumber.Val())

	if !r.packetRing.WriteWithBackoff(producerID, pkt, r.writeConfig) {
		// Ring write failed after all backoff retries - ring is persistently full
		m.RingDropsTotal.Add(1)
		r.releasePacketFully(pkt)
	}
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

	// Phase 1: Lockless - Use atomic counters instead of embedded rate struct
	m.RecvLightACKCounter.Add(1) // Used for Light ACK triggering until Phase 5 complete
	pktLen := pkt.Len()
	m.RecvRatePackets.Add(1)    // Replaces r.rate.packets++
	m.RecvRateBytes.Add(pktLen) // Replaces r.rate.bytes += pktLen

	m.CongestionRecvPkt.Add(1)
	m.CongestionRecvByte.Add(uint64(pktLen))

	if pkt.Header().RetransmittedPacketFlag {
		m.CongestionRecvPktRetrans.Add(1)
		m.CongestionRecvByteRetrans.Add(uint64(pktLen))
		m.RecvRateBytesRetrans.Add(pktLen) // Replaces r.rate.bytesRetrans += pktLen
	}

	// 5.1.2. SRT's Default LiveCC Algorithm - Exponential Moving Average
	// Using atomic load/store (no CAS loop needed - EMA tolerates rare lost updates)
	oldAvg := math.Float64frombits(r.avgPayloadSizeBits.Load())
	newAvg := 0.875*oldAvg + 0.125*float64(pktLen)
	r.avgPayloadSizeBits.Store(math.Float64bits(newAvg))

	// Check if too old (already past contiguousPoint)
	// Using contiguousPoint instead of lastDeliveredSequenceNumber per
	// contiguous_point_tsbpd_advancement_design.md - Phase 4
	if circular.SeqLessOrEqual(pkt.Header().PacketSequenceNumber.Val(), r.contiguousPoint.Load()) {
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
	// Use DeleteLocking() because this is called from legacy push path (not event loop)
	if r.nakBtree != nil {
		if r.nakBtree.DeleteLocking(seq) {
			m.NakBtreeDeletes.Add(1)
		}
	}

	// Insert into packet btree using consolidated helper
	r.insertAndUpdateMetrics(pkt, pktLen, false /* isRetransmit */, false /* updateDrainMetric */)

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
					// Here we're doing an average of the measurements (atomic EMA update)
					oldCap := math.Float64frombits(r.avgLinkCapacityBits.Load())
					newCap := 0.875*oldCap + 0.125*1_000_000/diff
					r.avgLinkCapacityBits.Store(math.Float64bits(newCap))
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

	// Phase 1: Lockless - Use atomic counters instead of embedded rate struct
	m.RecvLightACKCounter.Add(1) // Used for Light ACK triggering until Phase 5 complete

	pktLen := pkt.Len()

	m.RecvRatePackets.Add(1)    // Replaces r.rate.packets++
	m.RecvRateBytes.Add(pktLen) // Replaces r.rate.bytes += pktLen

	m.CongestionRecvPkt.Add(1)
	m.CongestionRecvByte.Add(uint64(pktLen))

	//pkt.PktTsbpdTime = pkt.Timestamp + r.delay
	if pkt.Header().RetransmittedPacketFlag {
		m.CongestionRecvPktRetrans.Add(1)
		m.CongestionRecvByteRetrans.Add(uint64(pktLen))

		m.RecvRateBytesRetrans.Add(pktLen) // Replaces r.rate.bytesRetrans += pktLen
	}

	// 5.1.2. SRT's Default LiveCC Algorithm - Exponential Moving Average
	// Using atomic load/store (no CAS loop needed - EMA tolerates rare lost updates)
	oldAvg := math.Float64frombits(r.avgPayloadSizeBits.Load())
	newAvg := 0.875*oldAvg + 0.125*float64(pktLen)
	r.avgPayloadSizeBits.Store(math.Float64bits(newAvg))

	// Check if too old (already past contiguousPoint) - Phase 4 change
	if circular.SeqLessOrEqual(pkt.Header().PacketSequenceNumber.Val(), r.contiguousPoint.Load()) {
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

		// Insert in correct position using consolidated helper (late arrival fills a gap)
		r.insertAndUpdateMetrics(pkt, pktLen, false /* isRetransmit */, false /* updateDrainMetric */)

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
		avgPayloadSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
		m.CongestionRecvByteLoss.Add(missingPkts * avgPayloadSize)

		r.maxSeenSequenceNumber = pkt.Header().PacketSequenceNumber
	}

	// Metrics already updated above in this function
	m.CongestionRecvPktBuf.Add(1)
	m.CongestionRecvPktUnique.Add(1)
	m.CongestionRecvByteBuf.Add(uint64(pktLen))
	m.CongestionRecvByteUnique.Add(uint64(pktLen))

	// Insert into packet store
	// Note: Duplicates should not occur here - in-order packets aren't in store yet,
	// and out-of-order packets check Has() before reaching this point
	inserted, dupPkt := r.packetStore.Insert(pkt)
	if !inserted && dupPkt != nil {
		// Defensive: If somehow a duplicate slipped through, release it
		m.CongestionRecvPktDuplicate.Add(1)
		m.CongestionRecvByteDuplicate.Add(uint64(pktLen))
		r.releasePacketFully(dupPkt)
	}
}
