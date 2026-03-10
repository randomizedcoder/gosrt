package common

import (
	"testing"
)

func TestProfileOption(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		wantNil bool
	}{
		{name: "empty string", flag: "", wantNil: true},
		{name: "cpu", flag: "cpu", wantNil: false},
		{name: "mem", flag: "mem", wantNil: false},
		{name: "allocs", flag: "allocs", wantNil: false},
		{name: "heap", flag: "heap", wantNil: false},
		{name: "rate", flag: "rate", wantNil: false},
		{name: "mutex", flag: "mutex", wantNil: false},
		{name: "block", flag: "block", wantNil: false},
		{name: "thread", flag: "thread", wantNil: false},
		{name: "trace", flag: "trace", wantNil: false},
		{name: "unknown", flag: "unknown", wantNil: true},
		{name: "CPU uppercase", flag: "CPU", wantNil: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ProfileOption(tc.flag)
			if tc.wantNil && got != nil {
				t.Errorf("ProfileOption(%q) = non-nil, want nil", tc.flag)
			}
			if !tc.wantNil && got == nil {
				t.Errorf("ProfileOption(%q) = nil, want non-nil", tc.flag)
			}
		})
	}
}
