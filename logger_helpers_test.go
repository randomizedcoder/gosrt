package srt

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunLoggerOutput(t *testing.T) {
	tests := []struct {
		name   string
		logger Logger
		send   []string // messages to send before closing
	}{
		{
			name:   "nil logger does not panic",
			logger: nil,
			send:   nil,
		},
		{
			name:   "logger with messages exits after Close",
			logger: NewLogger([]string{"test"}),
			send:   []string{"hello", "world"},
		},
		{
			name:   "logger with no topics exits on Close",
			logger: NewLogger(nil),
			send:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var wg sync.WaitGroup

			RunLoggerOutput(tc.logger, &wg)

			// Send messages if any
			if tc.logger != nil {
				for _, msg := range tc.send {
					tc.logger.Print("test", 0, 1, func() string { return msg })
				}
				// Give goroutine time to process
				time.Sleep(10 * time.Millisecond)
				tc.logger.Close()
			}

			// Wait should complete
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				// Success
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for goroutine to exit")
			}
		})
	}
}

func TestRunLoggerOutput_ConcurrentClose(t *testing.T) {
	logger := NewLogger(strings.Split("test,debug", ","))
	var wg sync.WaitGroup

	RunLoggerOutput(logger, &wg)

	// Send some messages
	for i := 0; i < 10; i++ {
		logger.Print("test", 0, 1, func() string { return "msg" })
	}

	// Close from a different goroutine
	go logger.Close()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for goroutine to exit after concurrent close")
	}
}
