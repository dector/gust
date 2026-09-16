// Package protocol defines the JSON message contract for Gust's local control
// socket. It intentionally has no internal dependencies so both the server and
// client packages can import it.
package protocol

// Action identifies a control request.
type Action string

const (
	ActionRerun  Action = "rerun"
	ActionStatus Action = "status"
	ActionPause  Action = "pause"
	ActionResume Action = "resume"
	ActionLogs   Action = "logs"
)

// Auto-reload states returned in Response.AutoReload.
const (
	AutoReloadActive = "active"
	AutoReloadPaused = "paused"
)

// Error codes returned in Response.Error.
const (
	ErrInvalidRequest = "invalid_request"
	ErrShuttingDown   = "shutting_down"
	ErrNoFailureLogs  = "no_failure_logs"
)

// Request is a single control request sent over the socket.
type Request struct {
	Action Action `json:"action"`
}

// ExitSummary describes the most recent application process exit.
type ExitSummary struct {
	Code  int    `json:"code"`
	At    string `json:"at,omitempty"`
	Error bool   `json:"error"`
}

// Response is a single control response sent over the socket. Not every field
// applies to every action; unused fields are omitted.
type Response struct {
	OK         bool         `json:"ok"`
	Error      string       `json:"error,omitempty"`
	Status     string       `json:"status,omitempty"`
	State      string       `json:"state,omitempty"`
	PID        int          `json:"pid,omitempty"`
	AppPort    int          `json:"app_port,omitempty"`
	ProxyPort  int          `json:"proxy_port,omitempty"`
	Version    int          `json:"version,omitempty"`
	AutoReload string       `json:"auto_reload,omitempty"`
	LastExit   *ExitSummary `json:"last_exit,omitempty"`

	// Fields for the logs action.
	Code   int    `json:"code,omitempty"`
	At     string `json:"at,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}
