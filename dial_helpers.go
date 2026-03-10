package srt

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// DialPublisher dials an SRT server for publishing. It parses the URL
// (must be srt:// scheme), extracts the stream ID from the URL path
// (adding "publish:" prefix if not present), and dials the server.
//
// Example: srt.DialPublisher(ctx, "srt://host:6000/mystream", config, wg)
// Results in StreamId = "publish:/mystream"
func DialPublisher(ctx context.Context, address string, config Config, wg *sync.WaitGroup) (Conn, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	if u.Scheme != "srt" {
		return nil, fmt.Errorf("unsupported scheme %q, expected \"srt\"", u.Scheme)
	}

	streamID := u.Path
	if streamID == "" {
		streamID = "/"
	}
	if !strings.HasPrefix(streamID, "publish:") {
		streamID = "publish:" + streamID
	}
	config.StreamId = streamID

	return Dial(ctx, "srt", u.Host, config, wg)
}
