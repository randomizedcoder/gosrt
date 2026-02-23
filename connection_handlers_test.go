package srt

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/congestion"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockReceiverForHandlers implements congestion.Receiver for testing handlePacketDirect
type mockReceiverForHandlers struct {
	useEventLoop bool
}

func (m *mockReceiverForHandlers) Stats() congestion.ReceiveStats                    { return congestion.ReceiveStats{} }
func (m *mockReceiverForHandlers) PacketRate() (pps, bps, capacity float64)          { return 0, 0, 0 }
func (m *mockReceiverForHandlers) Flush()                                            {}
func (m *mockReceiverForHandlers) Push(p packet.Packet)                              {}
func (m *mockReceiverForHandlers) Tick(now uint64)                                   {}
func (m *mockReceiverForHandlers) SetNAKInterval(intervalUs uint64)                  {}
func (m *mockReceiverForHandlers) SetRTTProvider(rtt congestion.RTTProvider)         {}
func (m *mockReceiverForHandlers) EventLoop(ctx context.Context, wg *sync.WaitGroup) {}
func (m *mockReceiverForHandlers) UseEventLoop() bool                                { return m.useEventLoop }
func (m *mockReceiverForHandlers) SetProcessConnectionControlPackets(fn func() int)  {}

// Ensure mockReceiverForHandlers implements the interface
var _ congestion.Receiver = (*mockReceiverForHandlers)(nil)

// TestHandlePacketDirect_LockFreeMode verifies that when UseEventLoop() returns true,
// the handlePacketDirect function bypasses the mutex (Phase 0 implementation).
func TestHandlePacketDirect_LockFreeMode(t *testing.T) {
	testCases := []struct {
		name         string
		useEventLoop bool
		expectMutex  bool // Whether mutex should be acquired
		description  string
	}{
		{
			name:         "lock-free mode bypasses mutex",
			useEventLoop: true,
			expectMutex:  false,
			description:  "When UseEventLoop=true, mutex should NOT be acquired",
		},
		{
			name:         "legacy mode acquires mutex",
			useEventLoop: false,
			expectMutex:  true,
			description:  "When UseEventLoop=false, mutex SHOULD be acquired",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a minimal srtConn with the mock receiver
			c := &srtConn{
				recv: &mockReceiverForHandlers{useEventLoop: tc.useEventLoop},
				// handlePacket needs these to not crash
				controlHandlers: make(map[packet.CtrlType]controlPacketHandler),
				userHandlers:    make(map[packet.CtrlSubType]userPacketHandler),
			}

			// Override handlePacket to track it was called
			// We can't easily override methods, so we'll test mutex behavior instead

			// Create a data packet (won't crash handlePacket as badly)
			p := packet.NewPacket(nil)
			// Set it as a data packet header
			hdr := p.Header()
			hdr.IsControlPacket = false // Data packet

			// For lock-free mode, we can test that it doesn't block on mutex
			if tc.useEventLoop {
				// Lock the mutex from another goroutine
				c.handlePacketMutex.Lock()

				// handlePacketDirect should NOT block because it bypasses mutex
				done := make(chan bool, 1)
				go func() {
					defer func() {
						// Recover from any panic in handlePacket (expected due to minimal setup)
						recover()
						done <- true
					}()
					c.handlePacketDirect(p)
				}()

				// Should complete quickly (not blocked on mutex)
				select {
				case <-done:
					// Good - didn't block
				case <-time.After(100 * time.Millisecond):
					t.Fatal("handlePacketDirect blocked on mutex in lock-free mode - Phase 0 not working!")
				}

				c.handlePacketMutex.Unlock()
			} else {
				// For legacy mode, verify mutex IS acquired
				// Lock the mutex from another goroutine
				c.handlePacketMutex.Lock()

				// handlePacketDirect SHOULD block because it needs mutex
				done := make(chan bool, 1)
				go func() {
					defer func() {
						recover()
						done <- true
					}()
					c.handlePacketDirect(p)
				}()

				// Should NOT complete quickly (blocked on mutex)
				select {
				case <-done:
					t.Fatal("handlePacketDirect didn't block on mutex in legacy mode!")
				case <-time.After(50 * time.Millisecond):
					// Good - blocked as expected
				}

				// Now unlock and let it complete
				c.handlePacketMutex.Unlock()

				select {
				case <-done:
					// Good - completed after unlock
				case <-time.After(100 * time.Millisecond):
					t.Fatal("handlePacketDirect didn't complete after mutex unlock")
				}
			}

		})
	}
}

// TestHandlePacketDirect_NilReceiver verifies that when recv is nil,
// the legacy (mutex) path is used.
func TestHandlePacketDirect_NilReceiver(t *testing.T) {
	c := &srtConn{
		recv:            nil, // No receiver
		controlHandlers: make(map[packet.CtrlType]controlPacketHandler),
		userHandlers:    make(map[packet.CtrlSubType]userPacketHandler),
	}

	p := packet.NewPacket(nil)
	hdr := p.Header()
	hdr.IsControlPacket = false

	// Lock mutex
	c.handlePacketMutex.Lock()

	// Should block (legacy path)
	done := make(chan bool, 1)
	go func() {
		defer func() {
			recover()
			done <- true
		}()
		c.handlePacketDirect(p)
	}()

	select {
	case <-done:
		t.Fatal("handlePacketDirect didn't block when recv is nil - should use legacy path")
	case <-time.After(50 * time.Millisecond):
		// Good - blocked as expected
	}

	c.handlePacketMutex.Unlock()
}

// TestHandlePacketDirect_ConcurrentLockFree verifies that multiple concurrent calls
// in lock-free mode don't block each other.
func TestHandlePacketDirect_ConcurrentLockFree(t *testing.T) {
	c := &srtConn{
		recv:            &mockReceiverForHandlers{useEventLoop: true},
		controlHandlers: make(map[packet.CtrlType]controlPacketHandler),
		userHandlers:    make(map[packet.CtrlSubType]userPacketHandler),
	}

	const numGoroutines = 10
	var wg sync.WaitGroup
	var completed atomic.Int32

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				recover() // handlePacket will panic due to minimal setup
				completed.Add(1)
			}()

			p := packet.NewPacket(nil)
			p.Header().IsControlPacket = false
			c.handlePacketDirect(p)
		}()
	}

	// Wait for all to complete
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		assert.Equal(t, int32(numGoroutines), completed.Load(), "all goroutines should complete")
		// In lock-free mode, should complete quickly (not serialized)
		require.Less(t, elapsed, 500*time.Millisecond, "lock-free mode should not serialize calls")
		t.Logf("Completed %d concurrent calls in %v (lock-free)", numGoroutines, elapsed)
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent lock-free calls timed out")
	}
}
