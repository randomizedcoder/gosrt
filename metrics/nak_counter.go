package metrics

import "github.com/randomizedcoder/gosrt/circular"

// NAKCounterType specifies which counter set to increment.
type NAKCounterType int

const (
	// NAKCounterSend is used when the RECEIVER generates and sends NAKs.
	// Increments: CongestionRecvNAKSingle, CongestionRecvNAKRange, CongestionRecvNAKPktsTotal
	NAKCounterSend NAKCounterType = iota

	// NAKCounterRecv is used when the SENDER receives NAKs.
	// Increments: CongestionSendNAKSingleRecv, CongestionSendNAKRangeRecv, CongestionSendNAKPktsRecv
	NAKCounterRecv
)

// CountNAKEntries iterates through a NAK loss list and increments the appropriate
// single/range/total counters. This function is used by BOTH:
//   - Receiver when generating NAKs (NAKCounterSend)
//   - Sender when receiving NAKs (NAKCounterRecv)
//
// Using the same function for both paths ensures counters are 100% consistent
// between endpoints, which is critical for NAK delivery rate calculations.
//
// The loss list format is pairs of [start, end] circular.Number values:
//   - If start == end: Single packet NAK entry (1 packet)
//   - If start != end: Range NAK entry (end.Distance(start) + 1 packets)
//
// RFC SRT Appendix A:
//   - Figure 21: Single packet entry (4 bytes on wire)
//   - Figure 22: Range entry (8 bytes on wire)
//
// Returns the total number of packets requested by the NAK list.
func CountNAKEntries(m *ConnectionMetrics, list []circular.Number, counterType NAKCounterType) uint64 {
	if m == nil || len(list) == 0 {
		return 0
	}

	var totalPkts uint64

	for i := 0; i+1 < len(list); i += 2 {
		start := list[i]
		end := list[i+1]

		if start.Equals(end) {
			// Single packet NAK entry: exactly 1 packet
			// RFC SRT Appendix A, Figure 21: bit 0 = 0
			if counterType == NAKCounterSend {
				m.CongestionRecvNAKSingle.Add(1)
			} else {
				m.CongestionSendNAKSingleRecv.Add(1)
			}
			totalPkts++
		} else {
			// Range NAK entry: multiple packets
			// RFC SRT Appendix A, Figure 22: bit 0 of first = 1
			// Packet count = end.Distance(start) + 1 (inclusive range)
			rangeSize := uint64(end.Distance(start)) + 1
			if counterType == NAKCounterSend {
				m.CongestionRecvNAKRange.Add(rangeSize)
			} else {
				m.CongestionSendNAKRangeRecv.Add(rangeSize)
			}
			totalPkts += rangeSize
		}
	}

	// Increment total packets counter
	if counterType == NAKCounterSend {
		m.CongestionRecvNAKPktsTotal.Add(totalPkts)
	} else {
		m.CongestionSendNAKPktsRecv.Add(totalPkts)
	}

	return totalPkts
}
