// Package proxy contains Gust proxy functionality.
//
// The reserved /__gust/comments browser API accepts mutations without an Origin
// header for non-browser clients. If Origin is present, it must be an HTTP(S)
// origin with the same host as the request (including remote Tailscale hosts).
package proxy
