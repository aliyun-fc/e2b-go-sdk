package e2b

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"time"
)

// Pty provides pseudo-terminal operations.
type Pty struct {
	sandbox *Sandbox
}

func newPty(s *Sandbox) *Pty {
	return &Pty{sandbox: s}
}

// Kill kills a PTY process.
func (p *Pty) Kill(ctx context.Context, pid int, requestTimeout ...time.Duration) (bool, error) {
	timeout := firstDuration(requestTimeout)
	req := map[string]any{
		"process": map[string]int{"pid": pid},
		"signal":  "SIGNAL_SIGKILL",
	}
	err := p.sandbox.connectUnary(ctx, "process.Process", "SendSignal", req, nil, nil, timeout, nil)
	if err == nil {
		return true, nil
	}
	var nf *NotFoundError
	if errors.As(err, &nf) {
		return false, nil
	}
	return false, err
}

// SendStdin sends input bytes to a PTY.
func (p *Pty) SendStdin(ctx context.Context, pid int, data []byte, requestTimeout ...time.Duration) error {
	timeout := firstDuration(requestTimeout)
	req := map[string]any{
		"process": map[string]int{"pid": pid},
		"input":   map[string]string{"pty": base64.StdEncoding.EncodeToString(data)},
	}
	return p.sandbox.connectUnary(ctx, "process.Process", "SendInput", req, nil, nil, timeout, nil)
}

// Create starts a PTY and returns a command handle.
func (p *Pty) Create(ctx context.Context, size PtySize, opts ...PtyOption) (*CommandHandle, error) {
	options := ptyOptionsFrom(opts...)
	envs := cloneStringMap(options.envs)
	if envs == nil {
		envs = map[string]string{}
	}
	if envs["TERM"] == "" {
		envs["TERM"] = "xterm-256color"
	}
	if envs["LANG"] == "" {
		envs["LANG"] = "C.UTF-8"
	}
	if envs["LC_ALL"] == "" {
		envs["LC_ALL"] = "C.UTF-8"
	}
	req := map[string]any{
		"process": map[string]any{
			"cmd":  "/bin/bash",
			"args": []string{"-i", "-l"},
			"envs": envs,
		},
		"pty": map[string]any{
			"size": map[string]int{"rows": size.Rows, "cols": size.Cols},
		},
	}
	if options.cwd != "" {
		req["process"].(map[string]any)["cwd"] = options.cwd
	}
	extra := map[string]string{keepalivePingHeader: strconv.Itoa(keepalivePingIntervalSec)}
	stream, err := p.sandbox.connectServerStream(ctx, "process.Process", "Start", req, options.user, options.timeout, options.requestTimeout, extra)
	if err != nil {
		return nil, err
	}
	handle, err := newCommandHandleFromStream(stream, nil, p)
	if err != nil {
		stream.Close()
		return nil, err
	}
	return handle, nil
}

// Connect attaches to an existing PTY.
func (p *Pty) Connect(ctx context.Context, pid int, timeout time.Duration, requestTimeout ...time.Duration) (*CommandHandle, error) {
	req := map[string]any{"process": map[string]int{"pid": pid}}
	extra := map[string]string{keepalivePingHeader: strconv.Itoa(keepalivePingIntervalSec)}
	stream, err := p.sandbox.connectServerStream(ctx, "process.Process", "Connect", req, nil, timeout, firstDuration(requestTimeout), extra)
	if err != nil {
		return nil, err
	}
	handle, err := newCommandHandleFromStream(stream, nil, p)
	if err != nil {
		stream.Close()
		return nil, err
	}
	return handle, nil
}

// Resize updates PTY dimensions.
func (p *Pty) Resize(ctx context.Context, pid int, size PtySize, requestTimeout ...time.Duration) error {
	timeout := firstDuration(requestTimeout)
	req := map[string]any{
		"process": map[string]int{"pid": pid},
		"pty": map[string]any{
			"size": map[string]int{"rows": size.Rows, "cols": size.Cols},
		},
	}
	return p.sandbox.connectUnary(ctx, "process.Process", "Update", req, nil, nil, timeout, nil)
}

type ptyOptions struct {
	envs           map[string]string
	user           *string
	cwd            string
	timeout        time.Duration
	requestTimeout time.Duration
}

// PtyOption configures PTY creation.
type PtyOption func(*ptyOptions)

func WithPtyEnv(key, value string) PtyOption {
	return func(o *ptyOptions) {
		if o.envs == nil {
			o.envs = map[string]string{}
		}
		o.envs[key] = value
	}
}

func WithPtyEnvs(envs map[string]string) PtyOption {
	return func(o *ptyOptions) { o.envs = cloneStringMap(envs) }
}

func WithPtyUser(user string) PtyOption {
	return func(o *ptyOptions) { o.user = &user }
}

func WithPtyCwd(cwd string) PtyOption {
	return func(o *ptyOptions) { o.cwd = cwd }
}

func WithPtyTimeout(timeout time.Duration) PtyOption {
	return func(o *ptyOptions) { o.timeout = timeout }
}

func WithPtyRequestTimeout(timeout time.Duration) PtyOption {
	return func(o *ptyOptions) { o.requestTimeout = timeout }
}

func ptyOptionsFrom(opts ...PtyOption) ptyOptions {
	options := ptyOptions{envs: map[string]string{}, timeout: 60 * time.Second}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}
