// Package common provides shared infrastructure for sender and receiver
// congestion control implementations.
//
// Key Components:
//   - ControlRing[T]: Generic lock-free ring for control packets (ACK, NAK, ACKACK, KEEPALIVE)
//
// This package enables code reuse between the sender and receiver control rings,
// reducing duplication and improving testability.
//
// Reference: documentation/completely_lockfree_receiver.md Section 3.4.9
package common
