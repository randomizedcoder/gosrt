package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/datarhei/gosrt/packet"
)

// metricsBuilderPool is a sync.Pool for strings.Builder objects to reduce allocations
var metricsBuilderPool = sync.Pool{
	New: func() interface{} {
		b := &strings.Builder{}
		b.Grow(64 * 1024) // Pre-allocate 64KB buffer
		return b
	},
}

// writeCounterValue writes a Prometheus counter metric
// Note: b is *strings.Builder from pool, will be reset after use
func writeCounterValue(b *strings.Builder, name string, value uint64, labels ...string) {
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(b, "%s=\"%s\"", labels[i], labels[i+1])
		}
		b.WriteByte('}')
	}
	fmt.Fprintf(b, " %d\n", value)
}

// writeGauge writes a Prometheus gauge metric
// Note: b is *strings.Builder from pool, will be reset after use
func writeGauge(b *strings.Builder, name string, value float64, labels ...string) {
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(b, "%s=\"%s\"", labels[i], labels[i+1])
		}
		b.WriteByte('}')
	}
	fmt.Fprintf(b, " %.9f\n", value)
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
// For DATA packets, also increments aggregate (if applicable)
// Note: For receive path, we may not have a packet object when errors occur
// Metrics are guaranteed to be non-nil (initialized in connection.go before NewReceiver)
func IncrementRecvErrorDrop(m *ConnectionMetrics, p packet.Packet, reason DropReason, isData bool) {
	// Increment granular counter based on packet type and reason (enum comparison is fast)
	switch reason {
	case DropReasonParse:
		if isData {
			m.PktRecvDataErrorParse.Add(1)
		} else {
			m.PktRecvControlErrorParse.Add(1)
		}
	case DropReasonIoUring:
		if isData {
			m.PktRecvDataErrorIoUring.Add(1)
		} else {
			m.PktRecvControlErrorIoUring.Add(1)
		}
	case DropReasonEmpty:
		if isData {
			m.PktRecvDataErrorEmpty.Add(1)
		} else {
			m.PktRecvControlErrorEmpty.Add(1)
		}
	case DropReasonRoute:
		if isData {
			m.PktRecvDataErrorRoute.Add(1)
		} else {
			m.PktRecvControlErrorRoute.Add(1)
		}
	}
}
