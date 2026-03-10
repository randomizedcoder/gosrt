package srt

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPrintConnectionStatistics(t *testing.T) {
	tests := []struct {
		name        string
		connections []Conn
		interval    string
		labeler     ConnectionTypeLabeler
	}{
		{
			name:        "nil connections does not panic",
			connections: nil,
			interval:    "1s",
			labeler:     nil,
		},
		{
			name:        "empty slice does not panic",
			connections: []Conn{},
			interval:    "1s",
			labeler:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			PrintConnectionStatistics(tc.connections, tc.interval, tc.labeler)
		})
	}
}

func TestStartStatisticsTicker(t *testing.T) {
	tests := []struct {
		name      string
		interval  time.Duration
		connsFunc func() []Conn
	}{
		{
			name:      "zero interval does not start goroutine",
			interval:  0,
			connsFunc: func() []Conn { return nil },
		},
		{
			name:      "negative interval does not start goroutine",
			interval:  -1 * time.Second,
			connsFunc: func() []Conn { return nil },
		},
		{
			name:      "nil connsFunc does not start goroutine",
			interval:  time.Second,
			connsFunc: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var wg sync.WaitGroup
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Should not panic
			StartStatisticsTicker(ctx, &wg, tc.interval, tc.connsFunc, nil)

			// With zero/negative interval or nil connsFunc, no goroutine should be started
			// wg.Wait() should return immediately
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				// Success
			case <-time.After(time.Second):
				t.Fatal("timed out - goroutine may have been started when it shouldn't have been")
			}
		})
	}
}

func TestStartStatisticsTicker_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	StartStatisticsTicker(ctx, &wg, 100*time.Millisecond, func() []Conn {
		return nil
	}, nil)

	// Cancel immediately
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for goroutine to exit after context cancel")
	}
}
