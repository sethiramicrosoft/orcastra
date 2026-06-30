package ssh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/sethiramicrosoft/orcastra/internal/hostdriver"
)

type Config struct {
	Host        string
	Port        int
	User        string
	PrivateKey  []byte
	Timeout     time.Duration
	Fingerprint string // optional SHA256 fingerprint pin
}

type Driver struct {
	cfg Config
}

func New(cfg Config) (*Driver, error) {
	if cfg.Host == "" || cfg.User == "" || len(cfg.PrivateKey) == 0 {
		return nil, fmt.Errorf("host, user and private key are required")
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Driver{cfg: cfg}, nil
}

func (d *Driver) RunContainer(ctx context.Context, spec hostdriver.ContainerSpec) (string, error) {
	if spec.Image == "" {
		return "", fmt.Errorf("container image is required")
	}

	args := []string{"docker", "run", "-d"}
	if spec.Name != "" {
		args = append(args, "--name", shellQuote(spec.Name))
	}
	for k, v := range spec.Env {
		args = append(args, "-e", shellQuote(fmt.Sprintf("%s=%s", k, v)))
	}
	for _, p := range spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		args = append(args, "-p", shellQuote(fmt.Sprintf("%d:%d/%s", p.HostPort, p.ContainerPort, proto)))
	}
	for _, v := range spec.Volumes {
		mode := "rw"
		if v.ReadOnly {
			mode = "ro"
		}
		args = append(args, "-v", shellQuote(fmt.Sprintf("%s:%s:%s", v.HostPath, v.ContainerPath, mode)))
	}
	for k, v := range spec.Labels {
		args = append(args, "--label", shellQuote(fmt.Sprintf("%s=%s", k, v)))
	}
	args = append(args, shellQuote(spec.Image))

	out, err := d.run(ctx, strings.Join(args, " "))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (d *Driver) RemoveContainer(ctx context.Context, id string, force bool) error {
	if id == "" {
		return fmt.Errorf("container ID is required")
	}
	cmd := "docker rm "
	if force {
		cmd += "-f "
	}
	cmd += shellQuote(id)
	_, err := d.run(ctx, cmd)
	return err
}

func (d *Driver) StreamLogs(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container ID is required")
	}
	client, err := d.dial(ctx)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("create ssh session: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("open stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("open stderr pipe: %w", err)
	}

	cmd := "docker logs "
	if follow {
		cmd += "-f "
	}
	cmd += shellQuote(containerID)
	if err := session.Start(cmd); err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("start docker logs over ssh: %w", err)
	}

	return &sshReadCloser{
		reader:  io.MultiReader(stdout, stderr),
		session: session,
		client:  client,
	}, nil
}

func (d *Driver) Inspect(ctx context.Context, containerID string) (*hostdriver.ContainerInfo, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container ID is required")
	}
	out, err := d.run(ctx, "docker inspect "+shellQuote(containerID))
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
	out, err := d.run(ctx, `awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} END {print t, a}' /proc/meminfo; df -k / | awk 'NR==2 {print $2, $3}'`)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
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
	client, err := d.dial(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()

	session.Stdin = bytes.NewReader(data)
	cmd := fmt.Sprintf("cat > %s && chmod %o %s", shellQuote(path), mode, shellQuote(path))
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Run(cmd); err != nil {
		return fmt.Errorf("write remote file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (d *Driver) Ping(ctx context.Context) error {
	_, err := d.run(ctx, "docker version --format '{{.Server.Version}}'")
	return err
}

func (d *Driver) run(ctx context.Context, command string) (string, error) {
	client, err := d.dial(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case <-ctx.Done():
		_ = session.Close()
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("remote command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), nil
	}
}

func (d *Driver) dial(_ context.Context) (*gossh.Client, error) {
	signer, err := gossh.ParsePrivateKey(d.cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	clientCfg := &gossh.ClientConfig{
		User:            d.cfg.User,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: d.hostKeyCallback(),
		Timeout:         d.cfg.Timeout,
	}
	addr := fmt.Sprintf("%s:%d", d.cfg.Host, d.cfg.Port)
	client, err := gossh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return client, nil
}

func (d *Driver) hostKeyCallback() gossh.HostKeyCallback {
	if strings.TrimSpace(d.cfg.Fingerprint) == "" {
		return gossh.InsecureIgnoreHostKey()
	}
	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		fp := gossh.FingerprintSHA256(key)
		if fp != strings.TrimSpace(d.cfg.Fingerprint) {
			return fmt.Errorf("host fingerprint mismatch: expected %s got %s", d.cfg.Fingerprint, fp)
		}
		return nil
	}
}

type sshReadCloser struct {
	reader  io.Reader
	session *gossh.Session
	client  *gossh.Client
}

func (s *sshReadCloser) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *sshReadCloser) Close() error {
	_ = s.session.Close()
	return s.client.Close()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
