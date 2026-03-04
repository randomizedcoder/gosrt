package send

// calculateDropThreshold computes the threshold for dropping old packets.
//
// A packet is considered "too old" if its PktTsbpdTime <= threshold.
// threshold = nowUs - dropThreshold
//
// CRITICAL: This function must handle the case where nowUs < dropThreshold,
// which would cause uint64 underflow and wrap to a huge value (~18.4e18),
// incorrectly marking ALL packets as "too old".
//
// Returns (threshold, shouldDrop) where:
//   - threshold: the computed threshold value
//   - shouldDrop: false if it's too early to drop any packets (nowUs < dropThreshold)
func calculateDropThreshold(nowUs, dropThreshold uint64) (threshold uint64, shouldDrop bool) {
	// Guard against uint64 underflow at connection startup
	// When nowUs < dropThreshold, no packets can possibly be old enough to drop
	if nowUs < dropThreshold {
		return 0, false
	}
	return nowUs - dropThreshold, true
}

// shouldDropPacket determines if a packet should be dropped based on its TSBPD time.
// Returns true if the packet is old enough to be dropped.
func shouldDropPacket(pktTsbpdTime, threshold uint64) bool {
	return pktTsbpdTime <= threshold
}
