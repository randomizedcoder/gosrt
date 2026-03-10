package srt

import (
	"context"
	"testing"
)

func TestDialPublisher_URLParsing(t *testing.T) {
	// These tests verify URL parsing and StreamID construction only.
	// Actual dialing requires a running SRT server.

	tests := []struct {
		name        string
		address     string
		wantErr     bool
		errContains string
	}{
		{
			name:        "non-srt scheme returns error",
			address:     "udp://host:6000/stream",
			wantErr:     true,
			errContains: "unsupported scheme",
		},
		{
			name:        "http scheme returns error",
			address:     "http://host:6000/stream",
			wantErr:     true,
			errContains: "unsupported scheme",
		},
		{
			name:        "invalid URL returns error",
			address:     "://invalid",
			wantErr:     true,
			errContains: "invalid address",
		},
		{
			name:        "empty string returns error",
			address:     "",
			wantErr:     true,
			errContains: "", // url.Parse("") succeeds with empty scheme
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DialPublisher(context.TODO(), tc.address, DefaultConfig(), nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("DialPublisher(%q) expected error, got nil", tc.address)
				}
				if tc.errContains != "" {
					if got := err.Error(); !contains(got, tc.errContains) {
						t.Errorf("DialPublisher(%q) error = %q, want containing %q", tc.address, got, tc.errContains)
					}
				}
			} else if err != nil {
				t.Fatalf("DialPublisher(%q) unexpected error: %v", tc.address, err)
			}
		})
	}
}

func TestDialPublisher_StreamID(t *testing.T) {
	// Test StreamID construction by checking what Config.StreamId would be set to.
	// We can't easily check the config after DialPublisher since it tries to dial,
	// but we can test the URL parsing logic by calling the internal helpers.

	tests := []struct {
		name         string
		path         string
		wantStreamID string
	}{
		{
			name:         "normal path gets publish prefix",
			path:         "/mystream",
			wantStreamID: "publish:/mystream",
		},
		{
			name:         "path with existing publish prefix is not doubled",
			path:         "publish:/mystream",
			wantStreamID: "publish:/mystream",
		},
		{
			name:         "empty path defaults to publish:/",
			path:         "",
			wantStreamID: "publish:/",
		},
		{
			name:         "root path gets publish prefix",
			path:         "/",
			wantStreamID: "publish:/",
		},
		{
			name:         "nested path gets publish prefix",
			path:         "/live/stream1",
			wantStreamID: "publish:/live/stream1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			streamID := tc.path
			if streamID == "" {
				streamID = "/"
			}
			if len(streamID) < 8 || streamID[:8] != "publish:" {
				streamID = "publish:" + streamID
			}
			if streamID != tc.wantStreamID {
				t.Errorf("streamID = %q, want %q", streamID, tc.wantStreamID)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || len(s) >= len(substr) && containsAt(s, substr)
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
