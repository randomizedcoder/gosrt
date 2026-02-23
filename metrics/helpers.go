package metrics

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/randomizedcoder/gosrt/packet"
)

// metricsBuilderPool is a sync.Pool for strings.Builder objects to reduce allocations
var metricsBuilderPool = sync.Pool{
	New: func() interface{} {
		b := &strings.Builder{}
		b.Grow(64 * 1024) // Pre-allocate 64KB buffer
		return b
	},
}

// scratchPool provides reusable byte slices for number formatting
var scratchPool = sync.Pool{
	New: func() interface{} {
		// Pre-allocate enough for any formatted number
		buf := make([]byte, 0, 32)
		return &buf
	},
}

// writeCounterValue writes a Prometheus counter metric with minimal allocations
// Note: b is *strings.Builder from pool, will be reset after use
func writeCounterValue(b *strings.Builder, name string, value uint64, labels ...string) {
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			// Write label=value without fmt.Fprintf
			b.WriteString(labels[i])
			b.WriteString(`="`)
			b.WriteString(labels[i+1])
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')

	// Use scratch buffer from pool for number formatting
	scratchPtr := scratchPool.Get().(*[]byte)
	scratch := (*scratchPtr)[:0]
	scratch = strconv.AppendUint(scratch, value, 10)
	b.Write(scratch)
	*scratchPtr = scratch
	scratchPool.Put(scratchPtr)

	b.WriteByte('\n')
}

// writeCounterIfNonZero writes a Prometheus counter only if value > 0
// This reduces Prometheus storage for defensive/rare error counters that are usually zero.
// Use this for counters that track rare events, errors, or edge cases.
func writeCounterIfNonZero(b *strings.Builder, name string, value uint64, labels ...string) {
	if value == 0 {
		return // Skip zero values to reduce Prometheus storage
	}
	writeCounterValue(b, name, value, labels...)
}

// writeGauge writes a Prometheus gauge metric with minimal allocations
// Note: b is *strings.Builder from pool, will be reset after use
func writeGauge(b *strings.Builder, name string, value float64, labels ...string) {
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			// Write label=value without fmt.Fprintf
			b.WriteString(labels[i])
			b.WriteString(`="`)
			b.WriteString(labels[i+1])
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')

	// Use scratch buffer from pool for number formatting
	// Format cleanly for Prometheus:
	// - Whole numbers: 1873920 (not 1873920.000000000)
	// - Floats with decimals: 0.000233966 (minimal precision needed)
	scratchPtr := scratchPool.Get().(*[]byte)
	scratch := (*scratchPtr)[:0]

	// Check if value is a whole number (no fractional part)
	if value == float64(int64(value)) && value >= -9007199254740992 && value <= 9007199254740992 {
		// Format as integer for clean output
		scratch = strconv.AppendInt(scratch, int64(value), 10)
	} else {
		// Format as float with minimal precision (-1 = smallest representation)
		scratch = strconv.AppendFloat(scratch, value, 'f', -1, 64)
	}
	b.Write(scratch)
	*scratchPtr = scratch
	scratchPool.Put(scratchPtr)

	b.WriteByte('\n')
}

// writeGaugeIfNonZero writes a Prometheus gauge only if value != 0
// Use this for gauges that track rare/optional measurements.
func writeGaugeIfNonZero(b *strings.Builder, name string, value float64, labels ...string) {
	if value == 0.0 {
		return // Skip zero values to reduce Prometheus storage
	}
	writeGauge(b, name, value, labels...)
}

// WithLockTiming executes a function while measuring lock hold and wait times for a regular Mutex
func WithLockTiming(metrics *LockTimingMetrics, mutex interface {
	Lock()
	Unlock()
}, fn func()) {
	if metrics == nil {
		mutex.Lock()
		defer mutex.Unlock()
		fn()
		return
	}

	// Measure wait time
	waitStart := time.Now()
	mutex.Lock()
	waitDuration := time.Since(waitStart)

	if waitDuration > 0 {
		metrics.RecordWaitTime(waitDuration)
	}
	// Note: RecordHoldTime will increment holdTimeIndex, which serves as acquisition counter

	// Measure hold time
	defer func() {
		holdDuration := time.Since(waitStart) // Total time from lock acquisition
		metrics.RecordHoldTime(holdDuration)  // This increments holdTimeIndex
		mutex.Unlock()
	}()

	fn()
}

// WithRLockTiming executes a function while measuring read lock hold and wait times for an RWMutex
func WithRLockTiming(metrics *LockTimingMetrics, mutex interface {
	RLock()
	RUnlock()
}, fn func()) {
	if metrics == nil {
		mutex.RLock()
		defer mutex.RUnlock()
		fn()
		return
	}

	// Measure wait time
	waitStart := time.Now()
	mutex.RLock()
	waitDuration := time.Since(waitStart)

	if waitDuration > 0 {
		metrics.RecordWaitTime(waitDuration)
	}

	// Measure hold time
	defer func() {
		holdDuration := time.Since(waitStart) // Total time from lock acquisition
		metrics.RecordHoldTime(holdDuration)  // This increments holdTimeIndex
		mutex.RUnlock()
	}()

	fn()
}

// WithWLockTiming executes a function while measuring write lock hold and wait times for an RWMutex
func WithWLockTiming(metrics *LockTimingMetrics, mutex interface {
	Lock()
	Unlock()
}, fn func()) {
	if metrics == nil {
		mutex.Lock()
		defer mutex.Unlock()
		fn()
		return
	}

	// Measure wait time
	waitStart := time.Now()
	mutex.Lock()
	waitDuration := time.Since(waitStart)

	if waitDuration > 0 {
		metrics.RecordWaitTime(waitDuration)
	}
	// Note: RecordHoldTime will increment holdTimeIndex, which serves as acquisition counter

	// Measure hold time
	defer func() {
		holdDuration := time.Since(waitStart) // Total time from lock acquisition
		metrics.RecordHoldTime(holdDuration)  // This increments holdTimeIndex
		mutex.Unlock()
	}()

	fn()
}

// IncrementRecvDataDrop increments both granular and aggregate drop counters for receiver
// This ensures granular and aggregate counters stay in sync
// Metrics are guaranteed to be non-nil (initialized in connection.go before NewReceiver)
func IncrementRecvDataDrop(m *ConnectionMetrics, reason DropReason, pktLen uint64) {
	// Increment granular counter based on reason (enum comparison is fast)
	switch reason {
	case DropReasonTooOld:
		m.CongestionRecvDataDropTooOld.Add(1)
	case DropReasonAlreadyAcked:
		m.CongestionRecvDataDropAlreadyAcked.Add(1)
	case DropReasonDuplicate:
		m.CongestionRecvDataDropDuplicate.Add(1)
	case DropReasonStoreInsertFailed:
		m.CongestionRecvDataDropStoreInsertFailed.Add(1)
	}

	// Always increment aggregate
	m.CongestionRecvPktDrop.Add(1)
	m.CongestionRecvByteDrop.Add(pktLen)
}

// IncrementSendDataDrop increments both granular and aggregate drop counters for sender
// This ensures granular and aggregate counters stay in sync
// Metrics are guaranteed to be non-nil (initialized in connection.go before NewSender)
func IncrementSendDataDrop(m *ConnectionMetrics, reason DropReason, pktLen uint64) {
	// Increment granular counter based on reason (enum comparison is fast)
	switch reason {
	case DropReasonTooOldSend:
		m.CongestionSendDataDropTooOld.Add(1)
	}

	// Always increment aggregate
	m.CongestionSendPktDrop.Add(1)
	m.CongestionSendByteDrop.Add(pktLen)
}

// IncrementSendErrorDrop increments granular error counters and aggregate for DATA packets
// For control packets, only increments granular counter (not included in aggregate)
// Metrics are guaranteed to be non-nil (initialized in connection.go before NewSender)
func IncrementSendErrorDrop(m *ConnectionMetrics, p packet.Packet, reason DropReason, pktLen uint64) {
	// Determine if packet is DATA or control
	isData := p != nil && !p.Header().IsControlPacket

	// Increment granular counter based on packet type and reason (enum comparison is fast)
	switch reason {
	case DropReasonMarshal:
		if isData {
			m.PktSentDataErrorMarshal.Add(1)
			m.CongestionSendPktDrop.Add(1) // Aggregate for DATA only
			m.CongestionSendByteDrop.Add(pktLen)
		} else {
			m.PktSentControlErrorMarshal.Add(1)
		}
	case DropReasonRingFull:
		if isData {
			m.PktSentDataRingFull.Add(1)
			m.CongestionSendPktDrop.Add(1) // Aggregate for DATA only
			m.CongestionSendByteDrop.Add(pktLen)
		} else {
			m.PktSentControlRingFull.Add(1)
		}
	case DropReasonSubmit:
		if isData {
			m.PktSentDataErrorSubmit.Add(1)
			m.CongestionSendPktDrop.Add(1) // Aggregate for DATA only
			m.CongestionSendByteDrop.Add(pktLen)
		} else {
			m.PktSentControlErrorSubmit.Add(1)
		}
	case DropReasonIoUring:
		if isData {
			m.PktSentDataErrorIoUring.Add(1)
			m.CongestionSendPktDrop.Add(1) // Aggregate for DATA only
			m.CongestionSendByteDrop.Add(pktLen)
		} else {
			m.PktSentControlErrorIoUring.Add(1)
		}
	}
}

// IncrementRecvErrorDrop increments granular error counters for receive path
// For DATA packets, also increments the aggregate PktRecvDataError counter
// Note: For receive path, we may not have a packet object when errors occur
// Metrics are guaranteed to be non-nil (initialized in connection.go before NewReceiver)
func IncrementRecvErrorDrop(m *ConnectionMetrics, p packet.Packet, reason DropReason, isData bool) {
	// Increment granular counter based on packet type and reason (enum comparison is fast)
	switch reason {
	case DropReasonParse:
		if isData {
			m.PktRecvDataErrorParse.Add(1)
			m.PktRecvDataError.Add(1) // Aggregate
		} else {
			m.PktRecvControlErrorParse.Add(1)
		}
	case DropReasonIoUring:
		if isData {
			m.PktRecvDataErrorIoUring.Add(1)
			m.PktRecvDataError.Add(1) // Aggregate
		} else {
			m.PktRecvControlErrorIoUring.Add(1)
		}
	case DropReasonEmpty:
		if isData {
			m.PktRecvDataErrorEmpty.Add(1)
			m.PktRecvDataError.Add(1) // Aggregate
		} else {
			m.PktRecvControlErrorEmpty.Add(1)
		}
	case DropReasonRoute:
		if isData {
			m.PktRecvDataErrorRoute.Add(1)
			m.PktRecvDataError.Add(1) // Aggregate
		} else {
			m.PktRecvControlErrorRoute.Add(1)
		}
	}
}
