package main

import (
	"testing"
)

func TestBitrateManager_New(t *testing.T) {
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)

	if bm.Current() != 100_000_000 {
		t.Errorf("Current() = %d, want 100000000", bm.Current())
	}
	if bm.Target() != 100_000_000 {
		t.Errorf("Target() = %d, want 100000000", bm.Target())
	}
	if bm.MinBitrate() != 1_000_000 {
		t.Errorf("MinBitrate() = %d, want 1000000", bm.MinBitrate())
	}
	if bm.MaxBitrate() != 1_000_000_000 {
		t.Errorf("MaxBitrate() = %d, want 1000000000", bm.MaxBitrate())
	}
}

func TestBitrateManager_New_ClampInitial(t *testing.T) {
	// Initial below min
	bm := NewBitrateManager(500_000, 1_000_000, 1_000_000_000)
	if bm.Current() != 1_000_000 {
		t.Errorf("Current() = %d, want 1000000 (clamped to min)", bm.Current())
	}

	// Initial above max
	bm = NewBitrateManager(2_000_000_000, 1_000_000, 1_000_000_000)
	if bm.Current() != 1_000_000_000 {
		t.Errorf("Current() = %d, want 1000000000 (clamped to max)", bm.Current())
	}
}

func TestBitrateManager_Set(t *testing.T) {
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)

	// Normal set
	err := bm.Set(200_000_000)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if bm.Current() != 200_000_000 {
		t.Errorf("Current() = %d, want 200000000", bm.Current())
	}

	// Verify token bucket was updated
	if bm.Bucket().Rate() != 200_000_000 {
		t.Errorf("Bucket.Rate() = %d, want 200000000", bm.Bucket().Rate())
	}
}

func TestBitrateManager_Set_Clamp(t *testing.T) {
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)

	// Set below min - should clamp
	err := bm.Set(500_000)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if bm.Current() != 1_000_000 {
		t.Errorf("Current() = %d, want 1000000 (clamped to min)", bm.Current())
	}

	// Set above max - should clamp
	err = bm.Set(2_000_000_000)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if bm.Current() != 1_000_000_000 {
		t.Errorf("Current() = %d, want 1000000000 (clamped to max)", bm.Current())
	}
}

func TestBitrateManager_Set_Invalid(t *testing.T) {
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)

	// Zero bitrate
	err := bm.Set(0)
	if err == nil {
		t.Error("Set(0) should return error")
	}

	// Negative bitrate
	err = bm.Set(-100)
	if err == nil {
		t.Error("Set(-100) should return error")
	}

	// Original bitrate should be unchanged
	if bm.Current() != 100_000_000 {
		t.Errorf("Current() = %d, want 100000000 (unchanged)", bm.Current())
	}
}

func TestParseBitrate(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		// Basic numbers
		{"1000000", 1_000_000, false},
		{"100", 100, false},

		// K suffix
		{"100K", 100_000, false},
		{"100k", 100_000, false},
		{"1.5K", 1_500, false},

		// M suffix
		{"100M", 100_000_000, false},
		{"100m", 100_000_000, false},
		{"1.5M", 1_500_000, false},
		{"500M", 500_000_000, false},

		// G suffix
		{"1G", 1_000_000_000, false},
		{"1g", 1_000_000_000, false},
		{"1.5G", 1_500_000_000, false},

		// Whitespace
		{"  100M  ", 100_000_000, false},

		// Errors
		{"", 0, true},
		{"abc", 0, true},
		{"-100M", 0, true},
		{"0M", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseBitrate(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseBitrate(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("ParseBitrate(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFormatBitrate(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1_500_000_000, "1.50 Gb/s"},
		{1_000_000_000, "1.00 Gb/s"},
		{500_000_000, "500.00 Mb/s"},
		{100_000_000, "100.00 Mb/s"},
		{1_500_000, "1.50 Mb/s"},
		{1_000_000, "1.00 Mb/s"},
		{500_000, "500.00 Kb/s"},
		{1_000, "1.00 Kb/s"},
		{500, "500 b/s"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := FormatBitrate(tt.input)
			if got != tt.expected {
				t.Errorf("FormatBitrate(%d) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestBitrateManager_Bucket(t *testing.T) {
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)

	bucket := bm.Bucket()
	if bucket == nil {
		t.Fatal("Bucket() returned nil")
	}

	// Bucket rate should match bitrate
	if bucket.Rate() != 100_000_000 {
		t.Errorf("Bucket.Rate() = %d, want 100000000", bucket.Rate())
	}

	// Changing bitrate should update bucket
	if err := bm.Set(200_000_000); err != nil {
		t.Fatalf("Set() failed: %v", err)
	}
	if bucket.Rate() != 200_000_000 {
		t.Errorf("Bucket.Rate() = %d after Set, want 200000000", bucket.Rate())
	}
}
