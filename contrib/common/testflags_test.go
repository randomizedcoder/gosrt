package common

import (
	"testing"
	"time"

	srt "github.com/randomizedcoder/gosrt"
)

func TestHandleTestFlags(t *testing.T) {
	tests := []struct {
		name         string
		testflags    bool
		modifier     func(*srt.Config)
		wantHandled  bool
		wantExitCode int
	}{
		{
			name:         "testflags false returns not handled",
			testflags:    false,
			modifier:     nil,
			wantHandled:  false,
			wantExitCode: 0,
		},
		{
			name:         "testflags true with nil modifier",
			testflags:    true,
			modifier:     nil,
			wantHandled:  true,
			wantExitCode: 0,
		},
		{
			name:      "testflags true with modifier",
			testflags: true,
			modifier: func(c *srt.Config) {
				c.Latency = 200 * time.Millisecond
			},
			wantHandled:  true,
			wantExitCode: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exitCode, handled := HandleTestFlags(tc.testflags, tc.modifier)
			if handled != tc.wantHandled {
				t.Errorf("HandleTestFlags() handled = %v, want %v", handled, tc.wantHandled)
			}
			if exitCode != tc.wantExitCode {
				t.Errorf("HandleTestFlags() exitCode = %d, want %d", exitCode, tc.wantExitCode)
			}
		})
	}
}
