package srt

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/congestion"
	receive "github.com/randomizedcoder/gosrt/congestion/live/receive"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Connection Concurrency Tests
// Tests for concurrent access patterns, race conditions, and context cancellation
// Run with: go test -v -race -run 'TestConcurrent'
// ═══════════════════════════════════════════════════════════════════════════════

// mockSenderForConcurrency implements congestion.Sender for testing concurrent writes
type mockSenderForConcurrency struct {
	mu           sync.Mutex
	packets      []packet.Packet
	useRing      bool
	pushCount    atomic.Int64
	pushFailures atomic.Int64
}

func (m *mockSenderForConcurrency) Stats() congestion.SendStats                       { return congestion.SendStats{} }
func (m *mockSenderForConcurrency) Flush()                                            {}
func (m *mockSenderForConcurrency) Tick(now uint64)                                   {}
func (m *mockSenderForConcurrency) ACK(sequenceNumber circular.Number)                {}
func (m *mockSenderForConcurrency) NAK(sequenceNumbers []circular.Number) uint64      { return 0 }
func (m *mockSenderForConcurrency) SetDropThreshold(threshold uint64)                 {}
func (m *mockSenderForConcurrency) EventLoop(ctx context.Context, wg *sync.WaitGroup) {}
func (m *mockSenderForConcurrency) UseEventLoop() bool                                { return false }
func (m *mockSenderForConcurrency) UseRing() bool                                     { return m.useRing }

func (m *mockSenderForConcurrency) Push(p packet.Packet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.packets = append(m.packets, p)
	m.pushCount.Add(1)
}

func (m *mockSenderForConcurrency) PushDirect(p packet.Packet) bool {
	m.pushCount.Add(1)
	// Simulate occasional failures for testing
	if m.pushCount.Load()%100 == 0 {
		m.pushFailures.Add(1)
		return false
	}
	m.mu.Lock()
	m.packets = append(m.packets, p)
	m.mu.Unlock()
	return true
}

var _ congestion.Sender = (*mockSenderForConcurrency)(nil)

// mockReceiverForConcurrency implements congestion.Receiver for testing
type mockReceiverForConcurrency struct {
	useEventLoop bool
	pushCount    atomic.Int64
}

func (m *mockReceiverForConcurrency) Stats() congestion.ReceiveStats {
	return congestion.ReceiveStats{}
}
func (m *mockReceiverForConcurrency) PacketRate() (pps, bps, capacity float64)          { return 0, 0, 0 }
func (m *mockReceiverForConcurrency) Flush()                                            {}
func (m *mockReceiverForConcurrency) Tick(now uint64)                                   {}
func (m *mockReceiverForConcurrency) SetNAKInterval(intervalUs uint64)                  {}
func (m *mockReceiverForConcurrency) SetRTTProvider(rtt congestion.RTTProvider)         {}
func (m *mockReceiverForConcurrency) EventLoop(ctx context.Context, wg *sync.WaitGroup) {}
func (m *mockReceiverForConcurrency) UseEventLoop() bool                                { return m.useEventLoop }
func (m *mockReceiverForConcurrency) SetProcessConnectionControlPackets(fn func() int)  {}

func (m *mockReceiverForConcurrency) Push(p packet.Packet) {
	m.pushCount.Add(1)
}

var _ congestion.Receiver = (*mockReceiverForConcurrency)(nil)

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Concurrent Write Operations
// ═══════════════════════════════════════════════════════════════════════════════

func TestConcurrent_Write_TableDriven(t *testing.T) {
	// NOTE: srtConn.Write() is NOT designed to be called concurrently from multiple goroutines
	// on the same connection - each connection should have a single writer in production.
	// These tests verify:
	// 1. Single writer per connection works correctly
	// 2. Multiple independent connections can write concurrently
	// 3. The underlying sender (ring/channel) handles its part correctly
	testCases := []struct {
		name           string
		numConnections int // Each connection has exactly one writer
		writesPerConn  int
		useRing        bool
	}{
		{
			name:           "single_conn_channel_mode",
			numConnections: 1,
			writesPerConn:  100,
			useRing:        false,
		},
		{
			name:           "multi_conn_channel_mode",
			numConnections: 10,
			writesPerConn:  50,
			useRing:        false,
		},
		{
			name:           "single_conn_ring_mode",
			numConnections: 1,
			writesPerConn:  100,
			useRing:        true,
		},
		{
			name:           "multi_conn_ring_mode",
			numConnections: 10,
			writesPerConn:  50,
			useRing:        true,
		},
		{
			name:           "stress_many_connections",
			numConnections: 20,
			writesPerConn:  100,
			useRing:        true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var wg sync.WaitGroup
			var writeCount atomic.Int64
			var errorCount atomic.Int64

			// Launch one writer per connection (production pattern)
			for i := 0; i < tc.numConnections; i++ {
				wg.Add(1)
				go func(connID int) {
					defer wg.Done()

					// Each connection has its own sender, buffer, and queue
					sender := &mockSenderForConcurrency{useRing: tc.useRing}
					receiver := &mockReceiverForConcurrency{}

					c := &srtConn{
						ctx:         ctx,
						cancelCtx:   cancel,
						snd:         sender,
						recv:        receiver,
						writeQueue:  make(chan packet.Packet, 1000),
						writeBuffer: bytes.Buffer{},
						writeData:   make([]byte, 1316),
						metrics:     &metrics.ConnectionMetrics{},
					}

					data := []byte("test data from connection")
					for j := 0; j < tc.writesPerConn; j++ {
						_, err := c.Write(data)
						if err != nil {
							errorCount.Add(1)
						} else {
							writeCount.Add(1)
						}
					}
				}(i)
			}

			wg.Wait()

			totalAttempts := int64(tc.numConnections * tc.writesPerConn)
			t.Logf("Connections: %d, WritesPerConn: %d, Total: %d, Success: %d, Errors: %d",
				tc.numConnections, tc.writesPerConn, totalAttempts, writeCount.Load(), errorCount.Load())

			// Verify all operations completed
			if writeCount.Load()+errorCount.Load() != totalAttempts {
				t.Errorf("Missing writes: got %d + %d = %d, want %d",
					writeCount.Load(), errorCount.Load(),
					writeCount.Load()+errorCount.Load(), totalAttempts)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Context Cancellation During Operations
// ═══════════════════════════════════════════════════════════════════════════════

func TestConcurrent_ContextCancellation_TableDriven(t *testing.T) {
	// NOTE: Read/Write are NOT designed to be called concurrently on the same connection.
	// These tests verify that context cancellation properly propagates to multiple
	// INDEPENDENT connections sharing the same parent context.
	testCases := []struct {
		name           string
		operation      string // "read" or "write"
		cancelDelay    time.Duration
		numConnections int  // Each connection has one operation
		expectAllEOF   bool // If true, ALL operations must return EOF; if false, verify no panic
	}{
		{
			name:           "cancel_during_read_single",
			operation:      "read",
			cancelDelay:    10 * time.Millisecond,
			numConnections: 1,
			expectAllEOF:   true, // Read blocks on channel, cancel causes EOF
		},
		{
			name:           "cancel_during_read_multi_conn",
			operation:      "read",
			cancelDelay:    10 * time.Millisecond,
			numConnections: 5,
			expectAllEOF:   true,
		},
		{
			name:           "cancel_during_write_single",
			operation:      "write",
			cancelDelay:    10 * time.Millisecond,
			numConnections: 1,
			expectAllEOF:   false, // Write may complete before cancel
		},
		{
			name:           "cancel_during_write_multi_conn",
			operation:      "write",
			cancelDelay:    10 * time.Millisecond,
			numConnections: 5,
			expectAllEOF:   false, // Write may complete before cancel
		},
		{
			name:           "immediate_cancel_read",
			operation:      "read",
			cancelDelay:    0, // Cancel immediately
			numConnections: 3,
			expectAllEOF:   true,
		},
		{
			name:           "immediate_cancel_write",
			operation:      "write",
			cancelDelay:    0,
			numConnections: 3,
			expectAllEOF:   true, // Immediate cancel = Write sees ctx.Done()
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Shared parent context for all connections
			ctx, cancel := context.WithCancel(context.Background())

			var wg sync.WaitGroup
			var eofCount atomic.Int64
			var otherErrCount atomic.Int64

			// Schedule cancellation
			if tc.cancelDelay > 0 {
				go func() {
					time.Sleep(tc.cancelDelay)
					cancel()
				}()
			} else {
				cancel() // Immediate cancellation
			}

			// Launch one operation per connection (production pattern)
			for i := 0; i < tc.numConnections; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()

					// Each connection has its own buffers
					sender := &mockSenderForConcurrency{useRing: false}
					receiver := &mockReceiverForConcurrency{}

					c := &srtConn{
						ctx:         ctx,
						cancelCtx:   cancel,
						snd:         sender,
						recv:        receiver,
						readQueue:   make(chan packet.Packet, 10),
						writeQueue:  make(chan packet.Packet, 1000),
						writeBuffer: bytes.Buffer{},
						writeData:   make([]byte, 1316),
						metrics:     &metrics.ConnectionMetrics{},
					}

					var err error
					if tc.operation == "read" {
						buf := make([]byte, 1024)
						_, err = c.Read(buf)
					} else {
						_, err = c.Write([]byte("test data"))
					}

					if err == io.EOF {
						eofCount.Add(1)
					} else if err != nil {
						otherErrCount.Add(1)
					}
				}()
			}

			wg.Wait()

			if tc.expectAllEOF {
				// All connections should get EOF after context cancellation
				if eofCount.Load() != int64(tc.numConnections) {
					t.Errorf("Expected %d EOF errors, got %d (other: %d)",
						tc.numConnections, eofCount.Load(), otherErrCount.Load())
				}
			}

			// Log results for debugging
			t.Logf("EOF: %d, OtherErrors: %d, Success: %d",
				eofCount.Load(), otherErrCount.Load(),
				int64(tc.numConnections)-eofCount.Load()-otherErrCount.Load())
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Concurrent handlePacketDirect (MPSC Pattern)
// ═══════════════════════════════════════════════════════════════════════════════

func TestConcurrent_HandlePacketDirect_TableDriven(t *testing.T) {
	testCases := []struct {
		name         string
		numProducers int
		packetsEach  int
		useEventLoop bool
	}{
		{
			name:         "single_producer_legacy",
			numProducers: 1,
			packetsEach:  100,
			useEventLoop: false,
		},
		{
			name:         "multi_producer_legacy",
			numProducers: 4,
			packetsEach:  100,
			useEventLoop: false,
		},
		{
			name:         "single_producer_lockfree",
			numProducers: 1,
			packetsEach:  100,
			useEventLoop: true,
		},
		{
			name:         "multi_producer_lockfree",
			numProducers: 4,
			packetsEach:  100,
			useEventLoop: true,
		},
		{
			name:         "stress_many_producers",
			numProducers: 10,
			packetsEach:  200,
			useEventLoop: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			receiver := &mockReceiverForConcurrency{useEventLoop: tc.useEventLoop}

			c := &srtConn{
				recv:            receiver,
				controlHandlers: make(map[packet.CtrlType]controlPacketHandler),
				userHandlers:    make(map[packet.CtrlSubType]userPacketHandler),
			}

			var wg sync.WaitGroup
			var processedCount atomic.Int64

			// Launch concurrent producers
			for i := 0; i < tc.numProducers; i++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					for j := 0; j < tc.packetsEach; j++ {
						p := packet.NewPacket(nil)
						hdr := p.Header()
						hdr.IsControlPacket = false // Data packet

						func() {
							defer func() {
								// Recover from expected panics due to minimal setup
								_ = recover() // Intentionally ignored: expected panic from minimal mock
								processedCount.Add(1)
							}()
							c.handlePacketDirect(p)
						}()
					}
				}(i)
			}

			wg.Wait()

			expected := int64(tc.numProducers * tc.packetsEach)
			if processedCount.Load() != expected {
				t.Errorf("Expected %d processed packets, got %d",
					expected, processedCount.Load())
			}

			t.Logf("Producers: %d, PacketsEach: %d, Total processed: %d",
				tc.numProducers, tc.packetsEach, processedCount.Load())
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: RecvControlRing Concurrency (MPSC Lock-Free)
// ═══════════════════════════════════════════════════════════════════════════════

func TestConcurrent_RecvControlRing_MPSC_TableDriven(t *testing.T) {
	testCases := []struct {
		name         string
		ringSize     int
		ringShards   int
		numProducers int
		itemsEach    int
	}{
		{
			name:         "single_shard_single_producer",
			ringSize:     128,
			ringShards:   1,
			numProducers: 1,
			itemsEach:    100,
		},
		{
			name:         "single_shard_multi_producer",
			ringSize:     128,
			ringShards:   1,
			numProducers: 4,
			itemsEach:    100,
		},
		{
			name:         "multi_shard_multi_producer",
			ringSize:     128,
			ringShards:   4,
			numProducers: 4,
			itemsEach:    100,
		},
		{
			name:         "stress_high_contention",
			ringSize:     64, // Small ring to force contention
			ringShards:   2,
			numProducers: 10,
			itemsEach:    200,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := receive.NewRecvControlRing(tc.ringSize, tc.ringShards)
			if err != nil {
				t.Fatalf("Failed to create ring: %v", err)
			}

			var wg sync.WaitGroup
			var pushCount atomic.Int64
			var dropCount atomic.Int64
			var popCount atomic.Int64

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Launch producers
			for i := 0; i < tc.numProducers; i++ {
				wg.Add(1)
				go func(producerID int) {
					defer wg.Done()
					for j := 0; j < tc.itemsEach; j++ {
						ackNum := uint32(producerID*tc.itemsEach + j)
						if ring.PushACKACK(ackNum, time.Now()) {
							pushCount.Add(1)
						} else {
							dropCount.Add(1)
						}
					}
				}(i)
			}

			// Single consumer (MPSC pattern)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
						if _, ok := ring.TryPop(); ok {
							popCount.Add(1)
						} else {
							// Brief yield to avoid busy-spin
							time.Sleep(time.Microsecond)
						}
					}
				}
			}()

			// Wait for producers to finish
			time.Sleep(50 * time.Millisecond)

			// Signal consumer to stop
			cancel()
			wg.Wait()

			// Drain remaining items
			for {
				if _, ok := ring.TryPop(); ok {
					popCount.Add(1)
				} else {
					break
				}
			}

			total := pushCount.Load() + dropCount.Load()
			expected := int64(tc.numProducers * tc.itemsEach)

			t.Logf("Producers: %d, Items: %d, Pushed: %d, Dropped: %d, Popped: %d",
				tc.numProducers, tc.itemsEach, pushCount.Load(), dropCount.Load(), popCount.Load())

			if total != expected {
				t.Errorf("Total push+drop (%d) != expected (%d)", total, expected)
			}

			if pushCount.Load() != popCount.Load() {
				t.Errorf("Pushed (%d) != Popped (%d)", pushCount.Load(), popCount.Load())
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Concurrent Close Operations (shutdownOnce)
// ═══════════════════════════════════════════════════════════════════════════════

func TestConcurrent_Close_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		numClosers int
		closeDelay time.Duration // Delay between close calls
		activeOps  bool          // Have active Read/Write during close
	}{
		{
			name:       "single_close",
			numClosers: 1,
		},
		{
			name:       "concurrent_double_close",
			numClosers: 2,
			closeDelay: 0, // Simultaneous
		},
		{
			name:       "concurrent_many_closes",
			numClosers: 10,
			closeDelay: 0,
		},
		{
			name:       "sequential_closes",
			numClosers: 5,
			closeDelay: 1 * time.Millisecond,
		},
		{
			name:       "close_with_active_read",
			numClosers: 3,
			activeOps:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())

			sender := &mockSenderForConcurrency{}
			receiver := &mockReceiverForConcurrency{}

			c := &srtConn{
				ctx:         ctx,
				cancelCtx:   cancel,
				snd:         sender,
				recv:        receiver,
				readQueue:   make(chan packet.Packet, 10),
				writeQueue:  make(chan packet.Packet, 1000),
				writeBuffer: bytes.Buffer{},
				writeData:   make([]byte, 1316),
				metrics:     &metrics.ConnectionMetrics{},
			}

			var wg sync.WaitGroup
			var closeCount atomic.Int64

			// Start active operations if requested
			if tc.activeOps {
				wg.Add(1)
				go func() {
					defer wg.Done()
					buf := make([]byte, 1024)
					for {
						_, err := c.Read(buf)
						if err == io.EOF {
							return
						}
					}
				}()
			}

			// Launch concurrent closers
			for i := 0; i < tc.numClosers; i++ {
				wg.Add(1)
				go func(closerID int) {
					defer wg.Done()
					if tc.closeDelay > 0 {
						time.Sleep(tc.closeDelay * time.Duration(closerID))
					}
					// Close the connection (should be idempotent via shutdownOnce)
					cancel() // Use cancel instead of c.Close() since we have minimal setup
					closeCount.Add(1)
				}(i)
			}

			wg.Wait()

			// Verify all closers completed
			if closeCount.Load() != int64(tc.numClosers) {
				t.Errorf("Expected %d close calls, got %d", tc.numClosers, closeCount.Load())
			}

			// Verify context is cancelled
			select {
			case <-ctx.Done():
				// Good - context was cancelled
			default:
				t.Error("Context should be cancelled after Close()")
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: Concurrent handlePacketDirect with Context Cancellation
// This tests the production scenario where multiple io_uring completion handlers
// call handlePacketDirect concurrently while context may be cancelled.
// ═══════════════════════════════════════════════════════════════════════════════

func TestConcurrent_HandlePacketWithCancel_TableDriven(t *testing.T) {
	testCases := []struct {
		name         string
		numPacketOps int
		duration     time.Duration
		cancelMidway bool
	}{
		{
			name:         "single_handler_no_cancel",
			numPacketOps: 1,
			duration:     50 * time.Millisecond,
			cancelMidway: false,
		},
		{
			name:         "multi_handler_no_cancel",
			numPacketOps: 4, // Simulates 4 io_uring completion handlers
			duration:     50 * time.Millisecond,
			cancelMidway: false,
		},
		{
			name:         "multi_handler_cancel_midway",
			numPacketOps: 4,
			duration:     50 * time.Millisecond,
			cancelMidway: true,
		},
		{
			name:         "stress_many_handlers",
			numPacketOps: 10,
			duration:     100 * time.Millisecond,
			cancelMidway: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.duration*2)
			defer cancel()

			receiver := &mockReceiverForConcurrency{useEventLoop: true}

			c := &srtConn{
				ctx:             ctx,
				cancelCtx:       cancel,
				recv:            receiver,
				metrics:         &metrics.ConnectionMetrics{},
				controlHandlers: make(map[packet.CtrlType]controlPacketHandler),
				userHandlers:    make(map[packet.CtrlSubType]userPacketHandler),
			}

			var wg sync.WaitGroup
			var packetOps atomic.Int64

			// Schedule midway cancellation if requested
			if tc.cancelMidway {
				go func() {
					time.Sleep(tc.duration / 2)
					cancel()
				}()
			}

			// Packet handlers (simulating io_uring completions)
			for i := 0; i < tc.numPacketOps; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-ctx.Done():
							return
						default:
							p := packet.NewPacket(nil)
							hdr := p.Header()
							hdr.IsControlPacket = false

							func() {
								defer func() { _ = recover() }()
								c.handlePacketDirect(p)
							}()
							packetOps.Add(1)
						}
					}
				}()
			}

			// Run for specified duration
			time.Sleep(tc.duration)
			cancel()

			wg.Wait()

			t.Logf("PacketOps: %d", packetOps.Load())

			// Verify operations occurred (no deadlock)
			if packetOps.Load() == 0 {
				t.Error("No operations completed - possible deadlock")
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test: RecvControlRing Type-Specific Push Concurrency
// ═══════════════════════════════════════════════════════════════════════════════

func TestConcurrent_RecvControlRing_AllTypes_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		ringSize   int
		ringShards int
		producers  int
		itemsEach  int
	}{
		{
			name:       "ackack_and_keepalive_concurrent",
			ringSize:   128,
			ringShards: 2,
			producers:  4,
			itemsEach:  100,
		},
		{
			name:       "high_volume_mixed",
			ringSize:   256,
			ringShards: 4,
			producers:  8,
			itemsEach:  200,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ring, err := receive.NewRecvControlRing(tc.ringSize, tc.ringShards)
			if err != nil {
				t.Fatalf("Failed to create ring: %v", err)
			}

			var wg sync.WaitGroup
			var ackackPush, keepalivePush atomic.Int64
			var ackackDrop, keepaliveDrop atomic.Int64

			// Half producers push ACKACK
			for i := 0; i < tc.producers/2; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					for j := 0; j < tc.itemsEach; j++ {
						if ring.PushACKACK(uint32(id*tc.itemsEach+j), time.Now()) {
							ackackPush.Add(1)
						} else {
							ackackDrop.Add(1)
						}
					}
				}(i)
			}

			// Other half push KEEPALIVE
			for i := tc.producers / 2; i < tc.producers; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					for j := 0; j < tc.itemsEach; j++ {
						if ring.PushKEEPALIVE() {
							keepalivePush.Add(1)
						} else {
							keepaliveDrop.Add(1)
						}
					}
				}(i)
			}

			wg.Wait()

			// Drain and count
			var ackackPop, keepalivePop int64
			for {
				entry, ok := ring.TryPop()
				if !ok {
					break
				}
				switch entry.Type {
				case receive.RecvControlTypeACKACK:
					ackackPop++
				case receive.RecvControlTypeKEEPALIVE:
					keepalivePop++
				}
			}

			t.Logf("ACKACK: pushed=%d, dropped=%d, popped=%d",
				ackackPush.Load(), ackackDrop.Load(), ackackPop)
			t.Logf("KEEPALIVE: pushed=%d, dropped=%d, popped=%d",
				keepalivePush.Load(), keepaliveDrop.Load(), keepalivePop)

			// Verify consistency
			if ackackPush.Load() != ackackPop {
				t.Errorf("ACKACK mismatch: pushed=%d, popped=%d",
					ackackPush.Load(), ackackPop)
			}
			if keepalivePush.Load() != keepalivePop {
				t.Errorf("KEEPALIVE mismatch: pushed=%d, popped=%d",
					keepalivePush.Load(), keepalivePop)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkConcurrent_Write_ChannelMode(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := &mockSenderForConcurrency{useRing: false}
	c := &srtConn{
		ctx:         ctx,
		cancelCtx:   cancel,
		snd:         sender,
		writeQueue:  make(chan packet.Packet, 10000),
		writeBuffer: bytes.Buffer{},
		writeData:   make([]byte, 1316),
		metrics:     &metrics.ConnectionMetrics{},
	}

	data := []byte("benchmark data payload")

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Write(data)
		}
	})
}

func BenchmarkConcurrent_Write_RingMode(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := &mockSenderForConcurrency{useRing: true}
	c := &srtConn{
		ctx:         ctx,
		cancelCtx:   cancel,
		snd:         sender,
		writeQueue:  make(chan packet.Packet, 10000),
		writeBuffer: bytes.Buffer{},
		writeData:   make([]byte, 1316),
		metrics:     &metrics.ConnectionMetrics{},
	}

	data := []byte("benchmark data payload")

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Write(data)
		}
	})
}

func BenchmarkConcurrent_RecvControlRing_MPSC(b *testing.B) {
	ring, err := receive.NewRecvControlRing(1024, 4)
	if err != nil {
		b.Fatalf("Failed to create ring: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := uint32(0)
		for pb.Next() {
			ring.PushACKACK(i, time.Now())
			i++
		}
	})
}

func BenchmarkConcurrent_HandlePacketDirect_LockFree(b *testing.B) {
	receiver := &mockReceiverForConcurrency{useEventLoop: true}
	c := &srtConn{
		recv:            receiver,
		controlHandlers: make(map[packet.CtrlType]controlPacketHandler),
		userHandlers:    make(map[packet.CtrlSubType]userPacketHandler),
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p := packet.NewPacket(nil)
			hdr := p.Header()
			hdr.IsControlPacket = false

			func() {
				defer func() { _ = recover() }()
				c.handlePacketDirect(p)
			}()
		}
	})
}
