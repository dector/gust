// Package probe discovers running Gust instances for the current user.
package probe

import (
	"context"

	internalprobe "github.com/dector/gust/internal/probe"
)

// Instance describes a responsive Gust instance.
type Instance struct {
	// SocketPath is the path to the instance's Unix control socket.
	SocketPath string
	// Root is the instance's work directory. For older instances that do not
	// report a root, this is the socket path instead.
	Root         string
	State        string
	AppPort      int
	ProxyPort    int
	TailscaleURL string
}

// Scan returns instances sorted by root. Unreachable or stale sockets are
// ignored. An empty result means no responsive instances were found.
// Each socket is given a short timeout; ctx cancels the whole scan.
func Scan(ctx context.Context) ([]Instance, error) {
	found, err := internalprobe.Scan(ctx)
	if err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(found))
	for _, instance := range found {
		instances = append(instances, Instance{
			SocketPath:   instance.SocketPath,
			Root:         instance.Root,
			State:        instance.State,
			AppPort:      instance.AppPort,
			ProxyPort:    instance.ProxyPort,
			TailscaleURL: instance.TailscaleURL,
		})
	}
	return instances, nil
}
