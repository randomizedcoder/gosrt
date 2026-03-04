package main

import (
	"context"
	"io"
	"time"
)

type Reader interface {
	io.ReadCloser
}

type debugReader struct {
	bytesPerSec uint64
	cancel      context.CancelFunc
	data        chan byte
}

type DebugReaderOptions struct {
	Bitrate uint64
}

func NewDebugReader(ctx context.Context, options DebugReaderOptions) (Reader, error) {
	r := &debugReader{
		bytesPerSec: options.Bitrate / 8,
	}

	if r.bytesPerSec == 0 {
		r.bytesPerSec = 262_144 // 2Mbit/s
	}

	r.data = make(chan byte, r.bytesPerSec)

	derivedCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	go r.generator(derivedCtx)

	return r, nil
}

func (r *debugReader) Read(p []byte) (int, error) {
	bufLen := len(p)

	if bufLen == 0 {
		return 0, nil
	}

	var i = 0

	for b := range r.data {
		p[i] = b

		i++
		if i == bufLen {
			break
		}
	}

	return i, nil
}

func (r *debugReader) Close() error {
	r.cancel()

	return nil
}

func (r *debugReader) generator(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()

	s := "abcdefghijklmnopqrstuvwxyz*"
	pivot := 0

	defer func() { close(r.data) }()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for i := uint64(0); i < r.bytesPerSec/10; i++ {
				r.data <- s[pivot]
				pivot++
				if pivot >= len(s) {
					pivot = 0
				}
			}
		}
	}
}
