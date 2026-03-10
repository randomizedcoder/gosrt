package srt

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
)

func TestIsConnectionClosedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "io.EOF",
			err:  io.EOF,
			want: false,
		},
		{
			name: "connection refused",
			err:  fmt.Errorf("connection refused"),
			want: true,
		},
		{
			name: "use of closed network connection",
			err:  fmt.Errorf("use of closed network connection"),
			want: true,
		},
		{
			name: "broken pipe",
			err:  fmt.Errorf("broken pipe"),
			want: true,
		},
		{
			name: "wrapped connection refused",
			err: &net.OpError{
				Op:     "write",
				Net:    "udp",
				Source: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
				Addr:   &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5678},
				Err:    fmt.Errorf("connection refused"),
			},
			want: true,
		},
		{
			name: "wrapped broken pipe",
			err: &net.OpError{
				Op:     "write",
				Net:    "udp",
				Source: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
				Addr:   &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5678},
				Err:    fmt.Errorf("broken pipe"),
			},
			want: true,
		},
		{
			name: "net.OpError with unrelated inner error",
			err: &net.OpError{
				Op:     "read",
				Net:    "udp",
				Source: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
				Addr:   &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5678},
				Err:    fmt.Errorf("something else"),
			},
			want: false,
		},
		{
			name: "random error",
			err:  fmt.Errorf("some random error"),
			want: false,
		},
		{
			name: "context.Canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "os.ErrDeadlineExceeded",
			err:  os.ErrDeadlineExceeded,
			want: false,
		},
		{
			name: "net.OpError with non-matching inner error",
			err: &net.OpError{
				Op:     "read",
				Net:    "udp",
				Source: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
				Addr:   &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5678},
				Err:    fmt.Errorf("timeout"),
			},
			want: false,
		},
		{
			name: "wrapped error containing connection refused",
			err:  fmt.Errorf("dial failed: connection refused by server"),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsConnectionClosedError(tc.err)
			if got != tc.want {
				t.Errorf("IsConnectionClosedError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
