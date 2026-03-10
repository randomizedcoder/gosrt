package common

import (
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSetupPauseHandler(t *testing.T) {
	tests := []struct {
		name  string
		label string
	}{
		{
			name:  "SIGUSR1 sets paused to true",
			label: "stopping test",
		},
		{
			name:  "empty label does not panic",
			label: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var paused atomic.Bool

			SetupPauseHandler(&paused, tc.label)

			if paused.Load() {
				t.Fatal("paused should start as false")
			}

			// Send SIGUSR1 to self
			if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
				t.Fatalf("failed to send SIGUSR1: %v", err)
			}

			// Wait for signal to be processed
			deadline := time.After(2 * time.Second)
			for !paused.Load() {
				select {
				case <-deadline:
					t.Fatal("timed out waiting for paused to become true")
				default:
					time.Sleep(time.Millisecond)
				}
			}

			if !paused.Load() {
				t.Error("paused should be true after SIGUSR1")
			}
		})
	}
}
