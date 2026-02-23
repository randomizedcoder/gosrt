//go:build go1.18

package receive

import (
	"time"

	"github.com/randomizedcoder/gosrt/congestion/live/common"
)

// ═══════════════════════════════════════════════════════════════════════════════
// RecvControlRing - Lock-free ring buffer for receiver control packets
//
// CRITICAL: This ring routes ACKACK and KEEPALIVE control packets to the
// EventLoop so that the EventLoop is the ONLY goroutine accessing the
// ackNumbers btree and other receiver state.
//
// Without this, ACKACK/KEEPALIVE handlers (called from io_uring completion
// handlers) would access shared state concurrently with the EventLoop,
// breaking the single-threaded access model.
//
// Reference: completely_lockfree_receiver.md Section 5.1.2
// Reference: completely_lockfree_receiver_implementation_plan.md Phase 2
// ═══════════════════════════════════════════════════════════════════════════════

// RecvControlPacketType identifies the type of receiver control packet
type RecvControlPacketType uint8

const (
	// RecvControlTypeACKACK is an ACKACK packet for RTT calculation
	RecvControlTypeACKACK RecvControlPacketType = iota

	// RecvControlTypeKEEPALIVE is a KEEPALIVE packet for connection maintenance
	RecvControlTypeKEEPALIVE
)

// String returns the string representation of the control packet type
func (t RecvControlPacketType) String() string {
	switch t {
	case RecvControlTypeACKACK:
		return "ACKACK"
	case RecvControlTypeKEEPALIVE:
		return "KEEPALIVE"
	default:
		return "UNKNOWN"
	}
}

// RecvControlPacket wraps an ACKACK or KEEPALIVE for ring transport.
// This is a value type (not pointer) to avoid allocations in the hot path.
type RecvControlPacket struct {
	// Type identifies the control packet type
	Type RecvControlPacketType

	// ACKNumber is the ACK number being acknowledged (for ACKACK only)
	ACKNumber uint32

	// Timestamp is the arrival time in nanoseconds (time.Now().UnixNano())
	// Captured at push time to ensure accurate RTT calculation
	Timestamp int64
}

// ArrivalTime returns the Timestamp as a time.Time
func (p RecvControlPacket) ArrivalTime() time.Time {
	return time.Unix(0, p.Timestamp)
}

// RecvControlRing wraps the generic control ring for receiver control packets.
// Push*() is called from io_uring completion handlers (multiple goroutines).
// TryPop() is called from EventLoop (single consumer).
type RecvControlRing struct {
	*common.ControlRing[RecvControlPacket]
}

// NewRecvControlRing creates a receiver control ring.
//
// Parameters:
//   - size: per-shard capacity (default: 128)
//   - shards: number of shards (default: 1)
//
// Reference: completely_lockfree_receiver.md Section 6.1.3
func NewRecvControlRing(size, shards int) (*RecvControlRing, error) {
	ring, err := common.NewControlRing[RecvControlPacket](size, shards)
	if err != nil {
		return nil, err
	}
	return &RecvControlRing{ControlRing: ring}, nil
}

// PushACKACK pushes an ACKACK to the control ring.
// Thread-safe: can be called from multiple goroutines (io_uring handlers).
// Returns true if successful, false if ring is full.
//
// The arrivalTime is captured at push time to ensure accurate RTT calculation.
// This is critical because the EventLoop may process the packet later, and
// using time.Now() at processing time would include queueing delay.
func (r *RecvControlRing) PushACKACK(ackNum uint32, arrivalTime time.Time) bool {
	return r.Push(uint64(RecvControlTypeACKACK), RecvControlPacket{
		Type:      RecvControlTypeACKACK,
		ACKNumber: ackNum,
		Timestamp: arrivalTime.UnixNano(),
	})
}

// PushKEEPALIVE pushes a KEEPALIVE to the control ring.
// Thread-safe: can be called from multiple goroutines (io_uring handlers).
// Returns true if successful, false if ring is full.
func (r *RecvControlRing) PushKEEPALIVE() bool {
	return r.Push(uint64(RecvControlTypeKEEPALIVE), RecvControlPacket{
		Type: RecvControlTypeKEEPALIVE,
	})
}
