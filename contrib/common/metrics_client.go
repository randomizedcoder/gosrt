package common

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// MetricsClient provides methods to fetch Prometheus metrics from TCP or UDS endpoints
type MetricsClient struct {
	httpClient *http.Client
}

// NewMetricsClient creates a new metrics client
func NewMetricsClient() *MetricsClient {
	return &MetricsClient{
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// FetchHTTP fetches metrics from a TCP HTTP endpoint
func (mc *MetricsClient) FetchHTTP(ctx context.Context, addr string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/metrics", addr), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s: %w", addr, err)
	}

	resp, err := mc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics from %s: %w", addr, err)
	}
	defer func() { _ = resp.Body.Close() }() // Error ignored: body already read

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, addr)
	}

	return io.ReadAll(resp.Body)
}

// FetchUDS fetches metrics from a Unix Domain Socket endpoint
func (mc *MetricsClient) FetchUDS(ctx context.Context, socketPath string) ([]byte, error) {
	// Create a client with Unix socket transport
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(dialCtx, "unix", socketPath)
			},
		},
	}

	// The URL host doesn't matter for Unix sockets, but we need a valid URL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/metrics", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for socket %s: %w", socketPath, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics from socket %s: %w", socketPath, err)
	}
	defer func() { _ = resp.Body.Close() }() // Error ignored: body already read

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from socket %s", resp.StatusCode, socketPath)
	}

	return io.ReadAll(resp.Body)
}
