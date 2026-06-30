// Package hostdriver defines the HostDriver interface — the only surface
// through which Orcastra interacts with managed servers.
//
// SSH is driver #1. Local Docker socket is driver #2.
// Every feature compiles against this interface, never against an SSH client.
package hostdriver

import (
	"context"
	"io"
)

// ContainerSpec describes a container to run.
type ContainerSpec struct {
	Name    string
	Image   string
	Env     map[string]string
	Ports   []PortBinding
	Volumes []VolumeMount
	Labels  map[string]string
}

// PortBinding maps a host port to a container port.
type PortBinding struct {
	HostPort      int
	ContainerPort int
	Protocol      string // "tcp" or "udp"
}

// VolumeMount maps a host path to a container path.
type VolumeMount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// ContainerInfo holds runtime state of a container.
type ContainerInfo struct {
	ID      string
	Name    string
	Image   string
	Status  string
	Running bool
}

// Metrics holds a point-in-time resource snapshot.
type Metrics struct {
	CPUPercent    float64
	MemUsedBytes  int64
	MemTotalBytes int64
	DiskUsedBytes int64
	DiskTotalBytes int64
}

// HostDriver is the single abstraction over any execution environment.
// SSH is one implementation. Local Docker socket is another.
// Future: Kubernetes, Nomad, Podman-over-systemd.
type HostDriver interface {
	// RunContainer starts a container from the given spec.
	// Returns the container ID.
	RunContainer(ctx context.Context, spec ContainerSpec) (id string, err error)

	// RemoveContainer stops and removes a container by ID.
	RemoveContainer(ctx context.Context, id string, force bool) error

	// StreamLogs streams stdout+stderr from a container.
	// The caller is responsible for closing the returned ReadCloser.
	StreamLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error)

	// Inspect returns the current state of a container.
	Inspect(ctx context.Context, containerID string) (*ContainerInfo, error)

	// ReadMetrics returns a point-in-time resource snapshot for the host.
	ReadMetrics(ctx context.Context) (*Metrics, error)

	// WriteFile writes bytes to a path on the host (e.g. proxy config).
	WriteFile(ctx context.Context, path string, data []byte, mode uint32) error

	// Ping checks connectivity to the host.
	Ping(ctx context.Context) error
}
