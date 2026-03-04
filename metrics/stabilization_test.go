package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestConnectionInfoForStab creates a ConnectionInfo for stabilization tests
func newTestConnectionInfoForStab(m *ConnectionMetrics, instanceName string) *ConnectionInfo {
	return &ConnectionInfo{
		Metrics:      m,
		InstanceName: instanceName,
		RemoteAddr:   "127.0.0.1:1234",
		StreamId:     "test-stream",
		PeerType:     "unknown",
		PeerSocketID: 0x87654321,
		StartTime:    time.Now(),
	}
}

func TestStabilizationMetricsEqual(t *testing.T) {
	m1 := StabilizationMetrics{
		DataSent: 100,
		DataRecv: 90,
		AckSent:  50,
		AckRecv:  45,
		NakSent:  5,
		NakRecv:  3,
	}

	m2 := StabilizationMetrics{
		DataSent: 100,
		DataRecv: 90,
		AckSent:  50,
		AckRecv:  45,
		NakSent:  5,
		NakRecv:  3,
	}

	m3 := StabilizationMetrics{
		DataSent: 101, // Different
		DataRecv: 90,
		AckSent:  50,
		AckRecv:  45,
		NakSent:  5,
		NakRecv:  3,
	}

	require.True(t, m1.Equal(m2), "identical metrics should be equal")
	require.False(t, m1.Equal(m3), "different metrics should not be equal")
}

func TestParseStabilizationResponse(t *testing.T) {
	response := `data_sent=12345
data_recv=12340
ack_sent=500
ack_recv=498
nak_sent=5
nak_recv=3
`
	m, err := ParseStabilizationResponse(response)
	require.NoError(t, err)
	require.Equal(t, uint64(12345), m.DataSent)
	require.Equal(t, uint64(12340), m.DataRecv)
	require.Equal(t, uint64(500), m.AckSent)
	require.Equal(t, uint64(498), m.AckRecv)
	require.Equal(t, uint64(5), m.NakSent)
	require.Equal(t, uint64(3), m.NakRecv)
}

func TestParseStabilizationResponseMissingField(t *testing.T) {
	response := `data_sent=12345
data_recv=12340
ack_sent=500
ack_recv=498
nak_sent=5
`
	// Missing nak_recv
	_, err := ParseStabilizationResponse(response)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required metric")
}

func TestParseStabilizationResponseInvalidValue(t *testing.T) {
	response := `data_sent=not_a_number
data_recv=12340
ack_sent=500
ack_recv=498
nak_sent=5
nak_recv=3
`
	_, err := ParseStabilizationResponse(response)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid value")
}

func TestStabilizationHandler(t *testing.T) {
	// Register a test connection
	m := &ConnectionMetrics{}
	m.PktSentDataSuccess.Store(100)
	m.PktRecvDataSuccess.Store(90)
	m.PktSentACKSuccess.Store(50)
	m.PktRecvACKSuccess.Store(45)
	m.PktSentNAKSuccess.Store(5)
	m.PktRecvNAKSuccess.Store(3)

	socketId := uint32(12345)
	RegisterConnection(socketId, newTestConnectionInfoForStab(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Create test server
	handler := StabilizationHandler()
	req := httptest.NewRequest("GET", "/stabilize", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/plain")

	// Parse the response
	parsed, err := ParseStabilizationResponse(rec.Body.String())
	require.NoError(t, err)
	require.Equal(t, uint64(100), parsed.DataSent)
	require.Equal(t, uint64(90), parsed.DataRecv)
	require.Equal(t, uint64(50), parsed.AckSent)
	require.Equal(t, uint64(45), parsed.AckRecv)
	require.Equal(t, uint64(5), parsed.NakSent)
	require.Equal(t, uint64(3), parsed.NakRecv)
}

func TestWaitForStabilizationImmediate(t *testing.T) {
	// Getter that always returns the same value (already stable)
	stableMetrics := StabilizationMetrics{
		DataSent: 100,
		DataRecv: 100,
	}
	getter := func(_ context.Context) (StabilizationMetrics, error) {
		return stableMetrics, nil
	}

	cfg := StabilizationConfig{
		PollInterval: 10 * time.Millisecond,
		StableCount:  2,
		MaxWait:      1 * time.Second,
	}

	ctx := context.Background()
	result := WaitForStabilization(ctx, cfg, getter)

	require.True(t, result.Stable, "should stabilize immediately when metrics don't change")
	require.NoError(t, result.Error)
	require.GreaterOrEqual(t, result.Iterations, 2, "should take at least 2 iterations")
	require.Less(t, result.Elapsed, 500*time.Millisecond, "should complete quickly")
}

func TestWaitForStabilizationChangingThenStable(t *testing.T) {
	// Getter that changes for the first few calls then stabilizes
	var callCount atomic.Int32
	getter := func(_ context.Context) (StabilizationMetrics, error) {
		count := callCount.Add(1)
		if count < 5 {
			// Changing
			return StabilizationMetrics{
				DataSent: uint64(count * 10),
			}, nil
		}
		// Stable
		return StabilizationMetrics{
			DataSent: 100,
		}, nil
	}

	cfg := StabilizationConfig{
		PollInterval: 10 * time.Millisecond,
		StableCount:  2,
		MaxWait:      2 * time.Second,
	}

	ctx := context.Background()
	result := WaitForStabilization(ctx, cfg, getter)

	require.True(t, result.Stable, "should eventually stabilize")
	require.NoError(t, result.Error)
	require.GreaterOrEqual(t, callCount.Load(), int32(6), "should have called getter multiple times")
}

func TestWaitForStabilizationTimeout(t *testing.T) {
	// Getter that never stabilizes
	var counter atomic.Uint64
	getter := func(_ context.Context) (StabilizationMetrics, error) {
		return StabilizationMetrics{
			DataSent: counter.Add(1),
		}, nil
	}

	cfg := StabilizationConfig{
		PollInterval: 10 * time.Millisecond,
		StableCount:  2,
		MaxWait:      100 * time.Millisecond,
	}

	ctx := context.Background()
	result := WaitForStabilization(ctx, cfg, getter)

	require.False(t, result.Stable, "should not stabilize")
	require.Error(t, result.Error)
	require.Contains(t, result.Error.Error(), "timeout")
}

func TestWaitForStabilizationContextCancelled(t *testing.T) {
	// Getter that never stabilizes
	var counter atomic.Uint64
	getter := func(_ context.Context) (StabilizationMetrics, error) {
		return StabilizationMetrics{
			DataSent: counter.Add(1),
		}, nil
	}

	cfg := StabilizationConfig{
		PollInterval: 10 * time.Millisecond,
		StableCount:  2,
		MaxWait:      5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after 50ms
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result := WaitForStabilization(ctx, cfg, getter)

	require.False(t, result.Stable, "should not stabilize due to cancellation")
	require.ErrorIs(t, result.Error, context.Canceled)
}

func TestWaitForStabilizationMultipleGetters(t *testing.T) {
	// Two getters that stabilize at different times
	var counter1, counter2 atomic.Int32

	getter1 := func(_ context.Context) (StabilizationMetrics, error) {
		count := counter1.Add(1)
		if count < 3 {
			return StabilizationMetrics{DataSent: uint64(count)}, nil
		}
		return StabilizationMetrics{DataSent: 100}, nil
	}

	getter2 := func(_ context.Context) (StabilizationMetrics, error) {
		count := counter2.Add(1)
		if count < 5 {
			return StabilizationMetrics{DataRecv: uint64(count)}, nil
		}
		return StabilizationMetrics{DataRecv: 200}, nil
	}

	cfg := StabilizationConfig{
		PollInterval: 10 * time.Millisecond,
		StableCount:  2,
		MaxWait:      2 * time.Second,
	}

	ctx := context.Background()
	result := WaitForStabilization(ctx, cfg, getter1, getter2)

	require.True(t, result.Stable, "should stabilize when both getters stabilize")
	require.NoError(t, result.Error)
	require.Len(t, result.FinalMetrics, 2, "should have final metrics from both getters")
	require.Equal(t, uint64(100), result.FinalMetrics[0].DataSent)
	require.Equal(t, uint64(200), result.FinalMetrics[1].DataRecv)
}

func TestWaitForStabilizationNoGetters(t *testing.T) {
	cfg := DefaultStabilizationConfig()
	ctx := context.Background()

	result := WaitForStabilization(ctx, cfg)

	require.True(t, result.Stable, "should immediately return stable with no getters")
	require.NoError(t, result.Error)
}

func TestDefaultStabilizationConfig(t *testing.T) {
	cfg := DefaultStabilizationConfig()

	require.Equal(t, 100*time.Millisecond, cfg.PollInterval)
	require.Equal(t, 2, cfg.StableCount)
	require.Equal(t, 5*time.Second, cfg.MaxWait)
}

func TestStabilizationMetricsString(t *testing.T) {
	m := StabilizationMetrics{
		DataSent: 100,
		DataRecv: 90,
		AckSent:  50,
		AckRecv:  45,
		NakSent:  5,
		NakRecv:  3,
	}

	s := m.String()
	require.Contains(t, s, "data(s=100,r=90)")
	require.Contains(t, s, "ack(s=50,r=45)")
	require.Contains(t, s, "nak(s=5,r=3)")
}

func TestAggregateStabilizationMetrics(t *testing.T) {
	m1 := StabilizationMetrics{DataSent: 100, DataRecv: 90}
	m2 := StabilizationMetrics{DataSent: 50, DataRecv: 40}
	m3 := StabilizationMetrics{DataSent: 25, DataRecv: 20}

	result := AggregateStabilizationMetrics(m1, m2, m3)

	require.Equal(t, uint64(175), result.DataSent)
	require.Equal(t, uint64(150), result.DataRecv)
}

func TestNewHTTPGetter(t *testing.T) {
	// Create a test server that returns stabilization metrics
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if _, err := w.Write([]byte(`data_sent=1000
data_recv=950
ack_sent=100
ack_recv=95
nak_sent=10
nak_recv=8
`)); err != nil {
			t.Logf("w.Write error: %v", err)
		}
	}))
	defer server.Close()

	getter := NewHTTPGetter(server.URL + "/stabilize")
	m, err := getter(context.Background())

	require.NoError(t, err)
	require.Equal(t, uint64(1000), m.DataSent)
	require.Equal(t, uint64(950), m.DataRecv)
	require.Equal(t, uint64(100), m.AckSent)
	require.Equal(t, uint64(95), m.AckRecv)
	require.Equal(t, uint64(10), m.NakSent)
	require.Equal(t, uint64(8), m.NakRecv)
}

// =============================================================================
// Benchmarks
// =============================================================================

// BenchmarkStabilizationHandlerNoConnections benchmarks handler with no connections
func BenchmarkStabilizationHandlerNoConnections(b *testing.B) {
	handler := StabilizationHandler()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/stabilize", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkStabilizationHandlerSingleConnection benchmarks handler with one active connection
func BenchmarkStabilizationHandlerSingleConnection(b *testing.B) {
	socketId := uint32(0x12345678)
	m := &ConnectionMetrics{}
	RegisterConnection(socketId, newTestConnectionInfoForStab(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set realistic counter values
	m.PktSentDataSuccess.Store(100000)
	m.PktRecvDataSuccess.Store(100000)
	m.PktSentACKSuccess.Store(50000)
	m.PktRecvACKSuccess.Store(50000)
	m.PktSentNAKSuccess.Store(1000)
	m.PktRecvNAKSuccess.Store(1000)

	handler := StabilizationHandler()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/stabilize", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkStabilizationHandler10Connections benchmarks handler with 10 connections
func BenchmarkStabilizationHandler10Connections(b *testing.B) {
	for i := 0; i < 10; i++ {
		socketId := uint32(0x10000000 + i)
		m := &ConnectionMetrics{}
		RegisterConnection(socketId, newTestConnectionInfoForStab(m, ""))
		defer UnregisterConnection(socketId, CloseReasonGraceful)

		// Set realistic values
		m.PktSentDataSuccess.Store(uint64(10000 * (i + 1)))
		m.PktRecvDataSuccess.Store(uint64(10000 * (i + 1)))
		m.PktSentACKSuccess.Store(uint64(5000 * (i + 1)))
		m.PktRecvACKSuccess.Store(uint64(5000 * (i + 1)))
		m.PktSentNAKSuccess.Store(uint64(100 * (i + 1)))
		m.PktRecvNAKSuccess.Store(uint64(100 * (i + 1)))
	}

	handler := StabilizationHandler()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/stabilize", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkParseStabilizationResponse benchmarks the parsing of /stabilize response
func BenchmarkParseStabilizationResponse(b *testing.B) {
	response := `data_sent=123456789
data_recv=123456780
ack_sent=61728394
ack_recv=61728390
nak_sent=12345
nak_recv=12340
`

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = ParseStabilizationResponse(response)
	}
}

// BenchmarkStabilizationVsMetrics compares /stabilize vs /metrics endpoint performance
func BenchmarkStabilizationVsMetrics(b *testing.B) {
	// Setup a connection with realistic values
	socketId := uint32(0xABCDEF00)
	m := &ConnectionMetrics{}
	RegisterConnection(socketId, newTestConnectionInfoForStab(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	m.PktSentDataSuccess.Store(100000)
	m.PktRecvDataSuccess.Store(100000)
	m.PktSentACKSuccess.Store(50000)
	m.PktRecvACKSuccess.Store(50000)
	m.PktSentNAKSuccess.Store(1000)
	m.PktRecvNAKSuccess.Store(1000)
	m.ByteSentDataSuccess.Store(140000000)
	m.ByteRecvDataSuccess.Store(140000000)
	m.CongestionRecvPkt.Store(100000)
	m.CongestionSendPkt.Store(100000)

	stabilizeHandler := StabilizationHandler()
	metricsHandler := MetricsHandler()

	b.Run("Stabilize", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest(http.MethodGet, "/stabilize", nil)
			rec := httptest.NewRecorder()
			stabilizeHandler.ServeHTTP(rec, req)
		}
	})

	b.Run("Metrics", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			rec := httptest.NewRecorder()
			metricsHandler.ServeHTTP(rec, req)
		}
	})
}
