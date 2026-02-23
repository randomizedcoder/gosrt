// Package main provides a controllable SRT data generator for performance testing.
//
// The client-seeker is a "dumb" data generator that can have its bitrate
// controlled externally via a Unix domain socket. It is designed to be
// orchestrated by the performance test tool.
package main

import (
	"encoding/json"
	"fmt"
)

// ControlRequest represents a command from the orchestrator.
type ControlRequest struct {
	Command string `json:"command"`           // "set_bitrate", "get_status", "heartbeat", "stop"
	Bitrate int64  `json:"bitrate,omitempty"` // Target bitrate in bits per second (for set_bitrate)
}

// ControlResponse represents a response to the orchestrator.
type ControlResponse struct {
	Status          string  `json:"status"`                     // "ok" or "error"
	Error           string  `json:"error,omitempty"`            // Error message if status="error"
	CurrentBitrate  int64   `json:"current_bitrate,omitempty"`  // Current sending rate (bps)
	TargetBitrate   int64   `json:"target_bitrate,omitempty"`   // Target rate from last set_bitrate
	PacketsSent     uint64  `json:"packets_sent,omitempty"`     // Total packets sent
	BytesSent       uint64  `json:"bytes_sent,omitempty"`       // Total bytes sent
	ConnectionAlive bool    `json:"connection_alive,omitempty"` // SRT connection status
	UptimeSeconds   float64 `json:"uptime_seconds,omitempty"`   // Time since start
	WatchdogState   string  `json:"watchdog_state,omitempty"`   // "normal", "warning", "critical"
}

// Command constants for ControlRequest.Command
const (
	CmdSetBitrate = "set_bitrate"
	CmdGetStatus  = "get_status"
	CmdHeartbeat  = "heartbeat"
	CmdStop       = "stop"
)

// Status constants for ControlResponse.Status
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// WatchdogState constants for ControlResponse.WatchdogState
const (
	WatchdogNormal   = "normal"
	WatchdogWarning  = "warning"
	WatchdogCritical = "critical"
)

// ParseRequest parses a JSON control request.
func ParseRequest(data []byte) (*ControlRequest, error) {
	var req ControlRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request JSON: %w", err)
	}

	// Validate command
	switch req.Command {
	case CmdSetBitrate:
		if req.Bitrate <= 0 {
			return nil, fmt.Errorf("set_bitrate requires positive bitrate, got %d", req.Bitrate)
		}
	case CmdGetStatus, CmdHeartbeat, CmdStop:
		// No additional validation needed
	default:
		return nil, fmt.Errorf("unknown command: %q", req.Command)
	}

	return &req, nil
}

// Marshal serializes a ControlResponse to JSON with newline terminator.
func (r *ControlResponse) Marshal() ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	// Append newline for line-based protocol
	return append(data, '\n'), nil
}

// NewOKResponse creates a success response.
func NewOKResponse() *ControlResponse {
	return &ControlResponse{Status: StatusOK}
}

// NewErrorResponse creates an error response.
func NewErrorResponse(err error) *ControlResponse {
	return &ControlResponse{
		Status: StatusError,
		Error:  err.Error(),
	}
}

// NewStatusResponse creates a status response with current metrics.
func NewStatusResponse(
	currentBitrate, targetBitrate int64,
	packetsSent, bytesSent uint64,
	connectionAlive bool,
	uptimeSeconds float64,
	watchdogState string,
) *ControlResponse {
	return &ControlResponse{
		Status:          StatusOK,
		CurrentBitrate:  currentBitrate,
		TargetBitrate:   targetBitrate,
		PacketsSent:     packetsSent,
		BytesSent:       bytesSent,
		ConnectionAlive: connectionAlive,
		UptimeSeconds:   uptimeSeconds,
		WatchdogState:   watchdogState,
	}
}
