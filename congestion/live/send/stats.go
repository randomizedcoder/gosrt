package send

import (
	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/packet"
)

func (s *sender) Stats() congestion.SendStats {
	// Read lock only for non-atomic fields (pktSndPeriod, avgPayloadSize, lossList/packetBtree)
	s.lock.RLock()
	usPktSndPeriod := s.pktSndPeriod
	bytePayload := uint64(s.avgPayloadSize)
	msBuf := uint64(0)

	// Calculate buffer time range
	if s.useBtree {
		// Phase 1: Btree mode - calculate from btree min/max
		if s.packetBtree != nil && s.packetBtree.Len() > 0 {
			minPkt := s.packetBtree.Min()
			maxPkt := s.packetBtree.Max()
			if minPkt != nil && maxPkt != nil {
				minTime := minPkt.Header().PktTsbpdTime
				maxTime := maxPkt.Header().PktTsbpdTime
				if maxTime > minTime {
					msBuf = (maxTime - minTime) / 1_000
				}
			}
		}
	} else {
		// Legacy: linked list mode
		if s.lossList != nil {
			maxElem := s.lossList.Back()
			minElem := s.lossList.Front()
			if maxElem != nil && minElem != nil {
				maxPkt, maxOK := maxElem.Value.(packet.Packet)
				minPkt, minOK := minElem.Value.(packet.Packet)
				if maxOK && minOK {
					msBuf = (maxPkt.Header().PktTsbpdTime - minPkt.Header().PktTsbpdTime) / 1_000
				}
			}
		}
	}
	s.lock.RUnlock()

	// Phase 1: Lockless - Get rates from ConnectionMetrics (lock-free)
	// Metrics are always available (initialized in connection.go before NewSender)
	m := s.metrics
	mbpsInputBW := m.GetSendRateEstInputBW() * 8 / 1024 / 1024 // Uses atomic load + conversion
	mbpsSentBW := m.GetSendRateEstSentBW() * 8 / 1024 / 1024   // Uses atomic load + conversion
	pktRetransRate := m.GetSendRateRetransPercent()            // Uses atomic load + conversion

	// Update atomic counters for instantaneous/calculated values
	m.CongestionSendUsPktSndPeriod.Store(uint64(usPktSndPeriod))
	m.CongestionSendBytePayload.Store(bytePayload)
	m.CongestionSendMsBuf.Store(msBuf)
	m.CongestionSendMbpsInputBandwidth.Store(uint64(mbpsInputBW * 1000))
	m.CongestionSendMbpsSentBandwidth.Store(uint64(mbpsSentBW * 1000))
	m.CongestionSendPktRetransRate.Store(uint64(pktRetransRate * 100))

	// Build return struct from atomic counters (lock-free reads)
	// PktLoss = packets reported as lost via NAK (incremented in nakLocked when NAK received)
	// PktDrop = packets dropped locally (too old, errors, etc.) - separate from loss
	return congestion.SendStats{
		Pkt:                         m.CongestionSendPkt.Load(),
		Byte:                        m.CongestionSendByte.Load(),
		PktUnique:                   m.CongestionSendPktUnique.Load(),
		ByteUnique:                  m.CongestionSendByteUnique.Load(),
		PktLoss:                     m.CongestionSendPktLoss.Load(),
		ByteLoss:                    m.CongestionSendByteLoss.Load(),
		PktRetrans:                  m.CongestionSendPktRetrans.Load(),
		ByteRetrans:                 m.CongestionSendByteRetrans.Load(),
		UsSndDuration:               m.CongestionSendUsSndDuration.Load(),
		PktDrop:                     m.CongestionSendDataDropTooOld.Load(), // Only congestion control drops
		ByteDrop:                    m.CongestionSendByteDrop.Load(),       // ByteDrop is maintained by helper functions
		PktBuf:                      m.CongestionSendPktBuf.Load(),
		ByteBuf:                     m.CongestionSendByteBuf.Load(),
		MsBuf:                       msBuf,
		PktFlightSize:               m.CongestionSendPktFlightSize.Load(),
		UsPktSndPeriod:              usPktSndPeriod,
		BytePayload:                 bytePayload,
		MbpsEstimatedInputBandwidth: mbpsInputBW,
		MbpsEstimatedSentBandwidth:  mbpsSentBW,
		PktRetransRate:              pktRetransRate,
	}
}
