package local

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sethiramicrosoft/orcastra/internal/hostdriver"
)

type Driver struct{}

func New() *Driver {
	return &Driver{}
}

func (d *Driver) RunContainer(ctx context.Context, spec hostdriver.ContainerSpec) (string, error) {
	if spec.Image == "" {
		return "", fmt.Errorf("container image is required")
	}

	args := []string{"run", "-d"}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	for _, p := range spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		args = append(args, "-p", fmt.Sprintf("%d:%d/%s", p.HostPort, p.ContainerPort, proto))
	}
	for _, v := range spec.Volumes {
		mode := "rw"
		if v.ReadOnly {
			mode = "ro"
		}
		args = append(args, "-v", fmt.Sprintf("%s:%s:%s", v.HostPath, v.ContainerPath, mode))
	}
	for k, v := range spec.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, spec.Image)

	out, err := runDocker(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (d *Driver) RemoveContainer(ctx context.Context, id string, force bool) error {
	if id == "" {
		return fmt.Errorf("container ID is required")
	}
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, id)
	_, err := runDocker(ctx, args...)
	return err
}

func (d *Driver) StreamLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container ID is required")
	}
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, containerID)

	cmd := exec.CommandContext(ctx, "docker", args...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open stderr pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start docker logs: %w", err)
	}
	return newCmdReadCloser(cmd, stdoutPipe, stderrPipe), nil
}

func (d *Driver) Inspect(ctx context.Context, containerID string) (*hostdriver.ContainerInfo, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container ID is required")
	}
	out, err := runDocker(ctx, "inspect", containerID)
	if err != nil {
		return nil, err
	}

	var inspect []struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		Image string `json:"Image"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
		} `json:"State"`
	}
	if err := json.Unmarshal([]byte(out), &inspect); err != nil {
		return nil, fmt.Errorf("parse docker inspect: %w", err)
	}
	if len(inspect) == 0 {
		return nil, fmt.Errorf("container not found")
	}
	item := inspect[0]
	return &hostdriver.ContainerInfo{
		ID:      item.ID,
		Name:    strings.TrimPrefix(item.Name, "/"),
		Image:   item.Image,
		Status:  item.State.Status,
		Running: item.State.Running,
	}, nil
}

func (d *Driver) ReadMetrics(ctx context.Context) (*hostdriver.Metrics, error) {
	// CPU usage from /proc/stat, memory+disk from standard Linux commands.
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
		CPUPercent:     0, // CPU sampling window is added in the metrics collector layer.
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
	_, err := runDocker(ctx, "version", "--format", "{{.Server.Version}}")
	return err
}

func runDocker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
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
