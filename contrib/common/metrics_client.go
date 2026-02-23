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
func (mc *MetricsClient) FetchHTTP(addr string) ([]byte, error) {
	resp, err := mc.httpClient.Get(fmt.Sprintf("http://%s/metrics", addr))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics from %s: %w", addr, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, addr)
	}

	return io.ReadAll(resp.Body)
}

// FetchUDS fetches metrics from a Unix Domain Socket endpoint
func (mc *MetricsClient) FetchUDS(socketPath string) ([]byte, error) {
	// Create a client with Unix socket transport
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}

	// The URL host doesn't matter for Unix sockets, but we need a valid URL
	resp, err := client.Get("http://localhost/metrics")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics from socket %s: %w", socketPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from socket %s", resp.StatusCode, socketPath)
	}

	return io.ReadAll(resp.Body)
}


