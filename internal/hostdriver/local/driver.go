package local

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"github.com/sethiramicrosoft/orcastra/internal/hostdriver"
)

type Driver struct {
	dc *client.Client
}

func New() (*Driver, error) {
	dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Driver{dc: dc}, nil
}

func (d *Driver) RunContainer(ctx context.Context, spec hostdriver.ContainerSpec) (string, error) {
	if spec.Image == "" {
		return "", fmt.Errorf("container image is required")
	}

	// Pull image first (ignore error if already present)
	rc, err := d.dc.ImagePull(ctx, spec.Image, image.PullOptions{})
	if err == nil {
		io.Copy(io.Discard, rc)
		rc.Close()
	}

	// Remove any existing container with the same name
	if spec.Name != "" {
		d.dc.ContainerRemove(ctx, spec.Name, container.RemoveOptions{Force: true}) //nolint:errcheck
	}

	// Build port bindings
	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}
	for _, p := range spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		cPort := nat.Port(fmt.Sprintf("%d/%s", p.ContainerPort, proto))
		exposedPorts[cPort] = struct{}{}
		portBindings[cPort] = []nat.PortBinding{{HostPort: fmt.Sprintf("%d", p.HostPort)}}
	}

	// Build env slice
	var envSlice []string
	for k, v := range spec.Env {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}

	// Build volume binds
	var binds []string
	for _, v := range spec.Volumes {
		mode := "rw"
		if v.ReadOnly {
			mode = "ro"
		}
		binds = append(binds, fmt.Sprintf("%s:%s:%s", v.HostPath, v.ContainerPath, mode))
	}

	resp, err := d.dc.ContainerCreate(ctx,
		&container.Config{
			Image:        spec.Image,
			Env:          envSlice,
			Labels:       spec.Labels,
			ExposedPorts: exposedPorts,
		},
		&container.HostConfig{
			PortBindings: portBindings,
			Binds:        binds,
			RestartPolicy: container.RestartPolicy{
				Name: "unless-stopped",
			},
		},
		nil, nil, spec.Name,
	)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}

	if err := d.dc.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}
	return resp.ID, nil
}

func (d *Driver) RemoveContainer(ctx context.Context, id string, force bool) error {
	if id == "" {
		return fmt.Errorf("container ID is required")
	}
	return d.dc.ContainerRemove(ctx, id, container.RemoveOptions{Force: force})
}

func (d *Driver) StreamLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container ID is required")
	}
	return d.dc.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Timestamps: false,
	})
}

func (d *Driver) Inspect(ctx context.Context, containerID string) (*hostdriver.ContainerInfo, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container ID is required")
	}
	info, err := d.dc.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}
	return &hostdriver.ContainerInfo{
		ID:      info.ID,
		Name:    strings.TrimPrefix(info.Name, "/"),
		Image:   info.Config.Image,
		Status:  info.State.Status,
		Running: info.State.Running,
	}, nil
}

func (d *Driver) ReadMetrics(ctx context.Context) (*hostdriver.Metrics, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", `awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END {print t, a}' /proc/meminfo; df -k / | awk 'NR==2 {print $2, $3}'`)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read host metrics: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("unexpected metrics output")
	}

	memParts := strings.Fields(lines[0])
	diskParts := strings.Fields(lines[1])
	if len(memParts) < 2 || len(diskParts) < 2 {
		return nil, fmt.Errorf("invalid metrics payload")
	}

	memTotalKB, _ := strconv.ParseInt(memParts[0], 10, 64)
	memAvailKB, _ := strconv.ParseInt(memParts[1], 10, 64)
	diskTotalKB, _ := strconv.ParseInt(diskParts[0], 10, 64)
	diskUsedKB, _ := strconv.ParseInt(diskParts[1], 10, 64)

	return &hostdriver.Metrics{
		CPUPercent:     0,
		MemTotalBytes:  memTotalKB * 1024,
		MemUsedBytes:   (memTotalKB - memAvailKB) * 1024,
		DiskTotalBytes: diskTotalKB * 1024,
		DiskUsedBytes:  diskUsedKB * 1024,
	}, nil
}

func (d *Driver) WriteFile(ctx context.Context, path string, data []byte, mode uint32) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	escapedPath := shellQuote(path)
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("cat > %s && chmod %o %s", escapedPath, mode, escapedPath))
	cmd.Stdin = bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write file: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *Driver) Ping(ctx context.Context) error {
	_, err := d.dc.Ping(ctx)
	return err
}

func (d *Driver) ListContainers(ctx context.Context, labelFilter map[string]string) ([]dockertypes.Container, error) {
	f := filters.NewArgs()
	for k, v := range labelFilter {
		f.Add("label", fmt.Sprintf("%s=%s", k, v))
	}
	return d.dc.ContainerList(ctx, container.ListOptions{Filters: f})
}

type cmdReadCloser struct {
	cmd    *exec.Cmd
	reader io.Reader
}

func newCmdReadCloser(cmd *exec.Cmd, stdout io.Reader, stderr io.Reader) io.ReadCloser {
	merged := io.MultiReader(stdout, stderr)
	return &cmdReadCloser{cmd: cmd, reader: bufio.NewReader(merged)}
}

func (c *cmdReadCloser) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *cmdReadCloser) Close() error {
	return c.cmd.Process.Kill()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
