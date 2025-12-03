package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"
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
		metrics.RecordHoldTime(holdDuration)   // This increments holdTimeIndex
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
		metrics.RecordHoldTime(holdDuration)   // This increments holdTimeIndex
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
		metrics.RecordHoldTime(holdDuration)   // This increments holdTimeIndex
		mutex.Unlock()
	}()

	fn()
}

