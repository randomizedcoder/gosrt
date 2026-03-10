package srt

import (
	"errors"
	"net"
	"strings"
)

// IsConnectionClosedError returns true if the error indicates a connection
// was closed, refused, or broken. These errors are expected during shutdown
// and typically should not be reported as failures.
func IsConnectionClosedError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	if strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "broken pipe") {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		opErrStr := opErr.Err.Error()
		if strings.Contains(opErrStr, "connection refused") ||
			strings.Contains(opErrStr, "broken pipe") {
			return true
		}
	}

	return false
}
