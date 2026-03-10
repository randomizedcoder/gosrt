package common

import (
	"sync"
	"testing"
	"time"
)

func TestWaitForShutdown(t *testing.T) {
	tests := []struct {
		name       string
		delay      time.Duration
		goroutines int
		finishIn   time.Duration // how long goroutines take to finish
	}{
		{
			name:       "no goroutines completes immediately",
			delay:      time.Second,
			goroutines: 0,
			finishIn:   0,
		},
		{
			name:       "goroutines finish before timeout",
			delay:      2 * time.Second,
			goroutines: 2,
			finishIn:   10 * time.Millisecond,
		},
		{
			name:       "zero delay times out immediately",
			delay:      0,
			goroutines: 0,
			finishIn:   0,
		},
		{
			name:       "negative delay times out immediately",
			delay:      -1 * time.Second,
			goroutines: 0,
			finishIn:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var wg sync.WaitGroup

			for i := 0; i < tc.goroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					time.Sleep(tc.finishIn)
				}()
			}

			start := time.Now()
			// Should not panic or hang
			WaitForShutdown(&wg, start, tc.delay)
		})
	}
}
