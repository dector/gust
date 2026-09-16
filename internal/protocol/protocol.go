// Package protocol defines the JSON message contract for Gust's local control
// socket. It intentionally has no internal dependencies so both the server and
// client packages can import it.
package protocol

// Action identifies a control request.
type Action string

const (
	ActionRerun  Action = "rerun"
	ActionStatus Action = "status"
)

// Error codes returned in Response.Error.
const (
	ErrInvalidRequest = "invalid_request"
	ErrShuttingDown   = "shutting_down"
)

// Request is a single control request sent over the socket.
type Request struct {
	Action Action `json:"action"`
}

// Response is a single control response sent over the socket.
type Response struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Status    string `json:"status,omitempty"`
	State     string `json:"state,omitempty"`
	PID       int    `json:"pid,omitempty"`
	AppPort   int    `json:"app_port,omitempty"`
	ProxyPort int    `json:"proxy_port,omitempty"`
	Version   int    `json:"version,omitempty"`
}
