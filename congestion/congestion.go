// Package congestions provides interfaces and types congestion control implementations for SRT
package congestion

import (
	"context"
	"sync"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
)

// RTTProvider provides access to RTT-related values for congestion control.
// Used for NAK suppression (receiver) and retransmit suppression (sender).
// Phase 6: RTO Suppression - implemented by srt.rtt
type RTTProvider interface {
	// RTOUs returns the pre-calculated RTO (Retransmission Timeout) in microseconds.
	// For NAK suppression (receiver): use full RTO (round-trip)
	// For retransmit suppression (sender): use RTOUs()/2 (one-way delay)
	RTOUs() uint64
}

// Sender is the sending part of the congestion control
type Sender interface {
	// Stats returns sender statistics.
	Stats() SendStats

	// Flush flushes all queued packages.
	Flush()

	// Push pushes a packet to be send on the sender queue.
	// Legacy path - may acquire locks in non-ring modes.
	Push(p packet.Packet)

	// PushDirect pushes a packet directly to the lock-free ring.
	// Returns true if successful, false if ring is full (caller should drop).
	// This bypasses the writeQueue channel for lower latency.
	// Only valid when UseRing() returns true.
	// Reference: sender_lockfree_architecture.md Section 7.8
	PushDirect(p packet.Packet) bool

	// UseRing returns whether the lock-free ring is enabled for Push operations.
	UseRing() bool

	// Tick gets called from a connection in order to proceed with the queued packets. The provided value for
	// now is corresponds to the timestamps in the queued packets. Those timestamps are the microseconds
	// since the start of the connection.
	Tick(now uint64)

	// ACK gets called when a sequence number has been confirmed from a receiver.
	ACK(sequenceNumber circular.Number)

	// NAK get called when packets with the listed sequence number should be resend.
	// Returns the number of packets retransmitted.
	NAK(sequenceNumbers []circular.Number) uint64

	// SetDropThreshold sets the threshold in microseconds for when to drop too late packages from the queue.
	SetDropThreshold(threshold uint64)

	// EventLoop runs the continuous event loop for sender packet processing (Phase 4: Lockless Sender).
	// This replaces the timer-driven Tick() for the sender side.
	// Only runs if UseEventLoop() returns true.
	// wg is decremented on exit for graceful shutdown coordination.
	EventLoop(ctx context.Context, wg *sync.WaitGroup)

	// UseEventLoop returns whether the sender event loop is enabled.
	// Used by connection code to decide between EventLoop and Tick loop.
	UseEventLoop() bool
}

// Receiver is the receiving part of the congestion control
type Receiver interface {
	// Stats returns receiver statistics.
	Stats() ReceiveStats

	// PacketRate returns the current packets and bytes per second, and the capacity of the link.
	PacketRate() (pps, bps, capacity float64)

	// Flush flushes all queued packages.
	Flush()

	// Push pushed a recieved packet to the receiver queue.
	Push(pkt packet.Packet)

	// Tick gets called from a connection in order to proceed with queued packets. The provided value for
	// now is corresponds to the timestamps in the queued packets. Those timestamps are the microseconds
	// since the start of the connection.
	Tick(now uint64)

	// SetNAKInterval sets the interval between two periodic NAK messages to the sender in microseconds.
	SetNAKInterval(nakInterval uint64)

	// SetRTTProvider sets the RTT provider for NAK suppression.
	// Phase 6: RTO Suppression - enables RTO-based NAK suppression in consolidateNakBtree().
	// Called during connection setup after the connection's RTT tracker is configured.
	SetRTTProvider(rtt RTTProvider)

	// EventLoop runs the continuous event loop for packet processing (Phase 4: Lockless Design).
	// This replaces the timer-driven Tick() for lower latency and smoother CPU usage.
	// Only runs if UseEventLoop() returns true.
	EventLoop(ctx context.Context, wg *sync.WaitGroup)

	// UseEventLoop returns whether the event loop is enabled.
	// Used by connection code to decide between EventLoop and Tick loop.
	UseEventLoop() bool

	// SetProcessConnectionControlPackets sets the callback for processing
	// connection-level control packets (ACKACK, KEEPALIVE) in EventLoop mode.
	// The callback is called at the start of each EventLoop iteration.
	// Set by connection.go to c.drainRecvControlRing.
	SetProcessConnectionControlPackets(func() int)
}

// SendStats are collected statistics from a sender
type SendStats struct {
	Pkt  uint64 // Sent packets in total
	Byte uint64 // Sent bytes in total

	PktUnique  uint64
	ByteUnique uint64

	PktLoss  uint64
	ByteLoss uint64

	PktRetrans  uint64
	ByteRetrans uint64

	UsSndDuration uint64 // microseconds

	PktDrop  uint64
	ByteDrop uint64

	// instantaneous
	PktBuf  uint64
	ByteBuf uint64
	MsBuf   uint64

	PktFlightSize uint64

	UsPktSndPeriod float64 // microseconds
	BytePayload    uint64

	MbpsEstimatedInputBandwidth float64
	MbpsEstimatedSentBandwidth  float64

	PktRetransRate float64 // Retransmission rate: bytesRetrans / bytesSent * 100 (NOT loss rate)
}

// ReceiveStats are collected statistics from a reciever
type ReceiveStats struct {
	Pkt  uint64
	Byte uint64

	PktUnique  uint64
	ByteUnique uint64

	PktLoss  uint64
	ByteLoss uint64

	PktRetrans  uint64
	ByteRetrans uint64

	PktBelated  uint64
	ByteBelated uint64

	PktDrop  uint64
	ByteDrop uint64

	// instantaneous
	PktBuf  uint64
	ByteBuf uint64
	MsBuf   uint64

	BytePayload uint64

	MbpsEstimatedRecvBandwidth float64
	MbpsEstimatedLinkCapacity  float64

	PktRetransRate float64 // Retransmission rate: bytesRetrans / bytesRecv * 100 (NOT loss rate)
}
