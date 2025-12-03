package metrics

import (
	"runtime"
	"strings"
	"time"
)

// Track program start time for GC timestamp conversion
var programStartTime = time.Now()

// writeRuntimeMetrics writes Go runtime metrics to the strings.Builder
// These metrics are compatible with prometheus/client_golang's promauto metrics
func writeRuntimeMetrics(b *strings.Builder) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Memory metrics
	writeGauge(b, "go_memstats_alloc_bytes", float64(m.Alloc))
	writeGauge(b, "go_memstats_alloc_bytes_total", float64(m.TotalAlloc))
	writeGauge(b, "go_memstats_sys_bytes", float64(m.Sys))
	writeGauge(b, "go_memstats_lookups_total", float64(m.Lookups))
	writeGauge(b, "go_memstats_mallocs_total", float64(m.Mallocs))
	writeGauge(b, "go_memstats_frees_total", float64(m.Frees))

	// Heap metrics
	writeGauge(b, "go_memstats_heap_alloc_bytes", float64(m.HeapAlloc))
	writeGauge(b, "go_memstats_heap_sys_bytes", float64(m.HeapSys))
	writeGauge(b, "go_memstats_heap_idle_bytes", float64(m.HeapIdle))
	writeGauge(b, "go_memstats_heap_inuse_bytes", float64(m.HeapInuse))
	writeGauge(b, "go_memstats_heap_released_bytes", float64(m.HeapReleased))
	writeGauge(b, "go_memstats_heap_objects", float64(m.HeapObjects))

	// Stack metrics
	writeGauge(b, "go_memstats_stack_inuse_bytes", float64(m.StackInuse))
	writeGauge(b, "go_memstats_stack_sys_bytes", float64(m.StackSys))

	// MSpan metrics
	writeGauge(b, "go_memstats_mspan_inuse_bytes", float64(m.MSpanInuse))
	writeGauge(b, "go_memstats_mspan_sys_bytes", float64(m.MSpanSys))

	// MCache metrics
	writeGauge(b, "go_memstats_mcache_inuse_bytes", float64(m.MCacheInuse))
	writeGauge(b, "go_memstats_mcache_sys_bytes", float64(m.MCacheSys))

	// Other memory metrics
	writeGauge(b, "go_memstats_buck_hash_sys_bytes", float64(m.BuckHashSys))
	writeGauge(b, "go_memstats_gc_sys_bytes", float64(m.GCSys))
	writeGauge(b, "go_memstats_other_sys_bytes", float64(m.OtherSys))
	writeGauge(b, "go_memstats_next_gc_bytes", float64(m.NextGC))

	// GC metrics
	if m.LastGC != 0 {
		// LastGC is in nanoseconds since program start
		// Convert to seconds since program start (relative time)
		lastGCRelative := float64(m.LastGC) / 1e9
		writeGauge(b, "go_memstats_last_gc_time_seconds", lastGCRelative)
	}
	writeGauge(b, "go_memstats_gc_cpu_fraction", m.GCCPUFraction)
	writeGauge(b, "go_memstats_gc_count", float64(m.NumGC))

	// GC pause duration (total)
	writeGauge(b, "go_memstats_gc_duration_seconds", float64(m.PauseTotalNs)/1e9)

	// Goroutines
	writeGauge(b, "go_goroutines", float64(runtime.NumGoroutine()))

	// CPU count
	writeGauge(b, "go_cpu_count", float64(runtime.NumCPU()))
}

