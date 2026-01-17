package srt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Buffer Pool Tests
// Reference: lockless_sender_design.md Section 6.2
// ═══════════════════════════════════════════════════════════════════════════════

func TestGetBuffer(t *testing.T) {
	buf := GetBuffer()
	require.NotNil(t, buf)
	require.NotNil(t, *buf)
	require.Equal(t, MaxPayloadBufferSize, len(*buf))

	// Return to pool
	PutBuffer(buf)
}

func TestGetBuffer_Multiple(t *testing.T) {
	// Get multiple buffers
	bufs := make([]*[]byte, 10)
	for i := 0; i < 10; i++ {
		bufs[i] = GetBuffer()
		require.NotNil(t, bufs[i])
		require.Equal(t, MaxPayloadBufferSize, len(*bufs[i]))
	}

	// Return all to pool
	for _, buf := range bufs {
		PutBuffer(buf)
	}
}

func TestPutBuffer_Nil(t *testing.T) {
	// Should not panic
	PutBuffer(nil)
}

func TestGetRecvBufferPool(t *testing.T) {
	pool := GetRecvBufferPool()
	require.NotNil(t, pool)

	// Get and put via pool directly
	buf := pool.Get().(*[]byte)
	require.NotNil(t, buf)
	require.Equal(t, MaxPayloadBufferSize, len(*buf))
	pool.Put(buf)
}

func TestValidateBufferSize(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		expected bool
	}{
		{"zero", 0, true},
		{"small", 100, true},
		{"typical_mpeg_ts", 1316, true},
		{"max_payload", MaxPayloadBufferSize, true},
		{"mtu_size", MaxPayloadBufferSize, true}, // Buffer can be full MTU
		{"one_over", MaxPayloadBufferSize + 1, false},
		{"negative", -1, false},
		{"large", 10000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateBufferSize(tt.size)
			require.Equal(t, tt.expected, result, "size=%d", tt.size)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Constants Tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestConstants(t *testing.T) {
	// Verify constants are sensible
	require.Equal(t, 1500, MaxPayloadBufferSize, "MTU should be 1500")
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark Tests
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkGetBuffer(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buf := GetBuffer()
		PutBuffer(buf)
	}
}

func BenchmarkGetBuffer_NoReturn(b *testing.B) {
	// Measure allocation cost when pool is empty
	bufs := make([]*[]byte, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bufs[i] = GetBuffer()
	}
	b.StopTimer()
	// Return all buffers
	for _, buf := range bufs {
		PutBuffer(buf)
	}
}
