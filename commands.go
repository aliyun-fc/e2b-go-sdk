package e2b

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Commands runs sandbox commands through envd.
type Commands struct {
	sandbox *Sandbox
}

func newCommands(s *Sandbox) *Commands {
	return &Commands{sandbox: s}
}

// List lists running commands and PTY sessions.
func (c *Commands) List(ctx context.Context, requestTimeout ...time.Duration) ([]ProcessInfo, error) {
	timeout := firstDuration(requestTimeout)
	var response struct {
		Processes []struct {
			PID    int    `json:"pid"`
			Tag    string `json:"tag,omitempty"`
			Config struct {
				Cmd  string            `json:"cmd"`
				Args []string          `json:"args"`
				Envs map[string]string `json:"envs"`
				Cwd  string            `json:"cwd,omitempty"`
			} `json:"config"`
		} `json:"processes"`
	}
	err := c.sandbox.connectUnary(ctx, "process.Process", "List", map[string]any{}, &response, nil, timeout, nil)
	if err != nil {
		return nil, err
	}
	result := make([]ProcessInfo, 0, len(response.Processes))
	for _, p := range response.Processes {
		result = append(result, ProcessInfo{
			PID:  p.PID,
			Tag:  p.Tag,
			Cmd:  p.Config.Cmd,
			Args: p.Config.Args,
			Envs: p.Config.Envs,
			Cwd:  p.Config.Cwd,
		})
	}
	return result, nil
}

// Kill sends SIGKILL to a running command.
func (c *Commands) Kill(ctx context.Context, pid int, requestTimeout ...time.Duration) (bool, error) {
	timeout := firstDuration(requestTimeout)
	req := map[string]any{
		"process": map[string]int{"pid": pid},
		"signal":  "SIGNAL_SIGKILL",
	}
	err := c.sandbox.connectUnary(ctx, "process.Process", "SendSignal", req, nil, nil, timeout, nil)
	if err == nil {
		return true, nil
	}
	var nf *NotFoundError
	if errors.As(err, &nf) {
		return false, nil
	}
	return false, err
}

// SendStdin sends data to command stdin.
func (c *Commands) SendStdin(ctx context.Context, pid int, data []byte, requestTimeout ...time.Duration) error {
	timeout := firstDuration(requestTimeout)
	req := map[string]any{
		"process": map[string]int{"pid": pid},
		"input":   map[string]string{"stdin": base64.StdEncoding.EncodeToString(data)},
	}
	return c.sandbox.connectUnary(ctx, "process.Process", "SendInput", req, nil, nil, timeout, nil)
}

// CloseStdin closes command stdin.
func (c *Commands) CloseStdin(ctx context.Context, pid int, requestTimeout ...time.Duration) error {
	if compareVersion(c.sandbox.envdVersion, "0.5.2") < 0 {
		return &SandboxError{Message: fmt.Sprintf("sandbox envd version %s does not support closing stdin", c.sandbox.envdVersion)}
	}
	timeout := firstDuration(requestTimeout)
	req := map[string]any{"process": map[string]int{"pid": pid}}
	return c.sandbox.connectUnary(ctx, "process.Process", "CloseStdin", req, nil, nil, timeout, nil)
}

// Run starts a command and waits for it to finish.
func (c *Commands) Run(ctx context.Context, cmd string, opts ...CommandOption) (CommandResult, error) {
	handle, err := c.Start(ctx, cmd, opts...)
	if err != nil {
		return CommandResult{}, err
	}
	options := commandOptionsFrom(opts...)
	return handle.Wait(ctx, WithWaitStdout(options.onStdout), WithWaitStderr(options.onStderr))
}

// Start starts a command and returns a handle.
func (c *Commands) Start(ctx context.Context, cmd string, opts ...CommandOption) (*CommandHandle, error) {
	options := commandOptionsFrom(opts...)
	if options.stdinSet && !options.stdin && compareVersion(c.sandbox.envdVersion, "0.3.0") < 0 {
		return nil, &SandboxError{Message: fmt.Sprintf("sandbox envd version %s cannot specify stdin; rebuild your template", c.sandbox.envdVersion)}
	}
	req := map[string]any{
		"process": map[string]any{
			"cmd":  "/bin/bash",
			"args": []string{"-l", "-c", cmd},
			"envs": nonNilStringMap(options.envs),
		},
		"stdin": options.stdin,
	}
	if options.cwd != "" {
		req["process"].(map[string]any)["cwd"] = options.cwd
	}
	extra := map[string]string{keepalivePingHeader: strconv.Itoa(keepalivePingIntervalSec)}
	stream, err := c.sandbox.connectServerStream(ctx, "process.Process", "Start", req, options.user, options.timeout, options.requestTimeout, extra)
	if err != nil {
		return nil, err
	}
	handle, err := newCommandHandleFromStream(stream, c, nil)
	if err != nil {
		stream.Close()
		return nil, err
	}
	return handle, nil
}

// Connect attaches to an existing command by PID.
func (c *Commands) Connect(ctx context.Context, pid int, timeout time.Duration, requestTimeout ...time.Duration) (*CommandHandle, error) {
	req := map[string]any{"process": map[string]int{"pid": pid}}
	extra := map[string]string{keepalivePingHeader: strconv.Itoa(keepalivePingIntervalSec)}
	stream, err := c.sandbox.connectServerStream(ctx, "process.Process", "Connect", req, nil, timeout, firstDuration(requestTimeout), extra)
	if err != nil {
		return nil, err
	}
	handle, err := newCommandHandleFromStream(stream, c, nil)
	if err != nil {
		stream.Close()
		return nil, err
	}
	return handle, nil
}

// CommandHandle represents a running command or PTY stream.
type CommandHandle struct {
	pid           int
	stream        *connectStream
	commands      *Commands
	pty           *Pty
	stdout        strings.Builder
	stderr        strings.Builder
	stdoutPending []byte
	stderrPending []byte
	finished      bool
	result        CommandResult
	ptyMode       bool
	closed        bool
}

func newCommandHandleFromStream(stream *connectStream, commands *Commands, pty *Pty) (*CommandHandle, error) {
	var first processStreamResponse
	if err := stream.Next(&first); err != nil {
		return nil, err
	}
	if first.Event.Start == nil {
		return nil, &SandboxError{Message: "failed to start process: expected start event"}
	}
	return &CommandHandle{
		pid:      first.Event.Start.PID,
		stream:   stream,
		commands: commands,
		pty:      pty,
		ptyMode:  pty != nil,
	}, nil
}

// PID returns the process ID.
func (h *CommandHandle) PID() int { return h.pid }

// Disconnect stops receiving events without killing the command.
func (h *CommandHandle) Disconnect() error {
	h.closed = true
	return h.stream.Close()
}

// Wait blocks until the command exits.
func (h *CommandHandle) Wait(ctx context.Context, opts ...WaitOption) (CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = h.stream.Close()
		case <-done:
		}
	}()
	defer close(done)

	options := waitOptionsFrom(opts...)
	for {
		event, err := h.nextEvent()
		if err != nil {
			if ctx.Err() != nil {
				return CommandResult{}, ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return CommandResult{}, err
		}
		if event.Stdout != "" && options.onStdout != nil {
			options.onStdout(event.Stdout)
		}
		if event.Stderr != "" && options.onStderr != nil {
			options.onStderr(event.Stderr)
		}
		if len(event.Pty) > 0 && options.onPty != nil {
			options.onPty(event.Pty)
		}
		if h.finished {
			break
		}
	}
	if !h.finished {
		return CommandResult{}, &SandboxError{Message: "command ended without an end event"}
	}
	if h.result.ExitCode != 0 {
		return h.result, &CommandExitError{Result: h.result}
	}
	return h.result, nil
}

// Next returns the next output event.
func (h *CommandHandle) Next() (CommandOutput, error) {
	return h.nextEvent()
}

// Kill kills the command or PTY.
func (h *CommandHandle) Kill(ctx context.Context) (bool, error) {
	if h.pty != nil {
		return h.pty.Kill(ctx, h.pid)
	}
	return h.commands.Kill(ctx, h.pid)
}

// SendStdin sends data to command stdin.
func (h *CommandHandle) SendStdin(ctx context.Context, data []byte, requestTimeout ...time.Duration) error {
	if h.commands == nil {
		return &SandboxError{Message: "sending stdin is not supported for this command handle"}
	}
	return h.commands.SendStdin(ctx, h.pid, data, requestTimeout...)
}

// CloseStdin closes command stdin.
func (h *CommandHandle) CloseStdin(ctx context.Context, requestTimeout ...time.Duration) error {
	if h.commands == nil {
		return &SandboxError{Message: "closing stdin is not supported for this command handle"}
	}
	return h.commands.CloseStdin(ctx, h.pid, requestTimeout...)
}

func (h *CommandHandle) nextEvent() (CommandOutput, error) {
	if h.finished {
		return CommandOutput{}, io.EOF
	}
	var response processStreamResponse
	if err := h.stream.Next(&response); err != nil {
		return CommandOutput{}, err
	}
	event := response.Event
	if event.Data != nil {
		stdout := decodeProtoBytes(event.Data.Stdout)
		stderr := decodeProtoBytes(event.Data.Stderr)
		pty := decodeProtoBytes(event.Data.Pty)
		output := CommandOutput{}
		if len(stdout) > 0 {
			text := decodeUTF8Chunk(&h.stdoutPending, stdout, false)
			h.stdout.WriteString(text)
			output.Stdout = text
		}
		if len(stderr) > 0 {
			text := decodeUTF8Chunk(&h.stderrPending, stderr, false)
			h.stderr.WriteString(text)
			output.Stderr = text
		}
		if len(pty) > 0 {
			output.Pty = pty
		}
		if output.Stdout != "" || output.Stderr != "" || len(output.Pty) > 0 {
			return output, nil
		}
	}
	if event.End != nil {
		h.finished = true
		stdoutFlush := decodeUTF8Chunk(&h.stdoutPending, nil, true)
		stderrFlush := decodeUTF8Chunk(&h.stderrPending, nil, true)
		h.stdout.WriteString(stdoutFlush)
		h.stderr.WriteString(stderrFlush)
		h.result = CommandResult{
			Stdout:   h.stdout.String(),
			Stderr:   h.stderr.String(),
			ExitCode: event.End.ExitCode,
			Error:    event.End.Error,
		}
		_ = h.stream.Close()
		if stdoutFlush != "" || stderrFlush != "" {
			return CommandOutput{Stdout: stdoutFlush, Stderr: stderrFlush}, nil
		}
		return CommandOutput{}, nil
	}
	return CommandOutput{}, nil
}

func decodeUTF8Chunk(pending *[]byte, chunk []byte, final bool) string {
	data := make([]byte, 0, len(*pending)+len(chunk))
	data = append(data, (*pending)...)
	data = append(data, chunk...)
	*pending = (*pending)[:0]

	var out strings.Builder
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 && !final && !utf8.FullRune(data) {
			*pending = append(*pending, data...)
			break
		}
		out.WriteRune(r)
		data = data[size:]
	}
	return out.String()
}

// CommandOutput is one streamed command output event.
type CommandOutput struct {
	Stdout string
	Stderr string
	Pty    []byte
}

type processStreamResponse struct {
	Event processEvent `json:"event"`
}

type processEvent struct {
	Start *struct {
		PID int `json:"pid"`
	} `json:"start,omitempty"`
	Data *struct {
		Stdout any `json:"stdout,omitempty"`
		Stderr any `json:"stderr,omitempty"`
		Pty    any `json:"pty,omitempty"`
	} `json:"data,omitempty"`
	End *struct {
		ExitCode int    `json:"exitCode"`
		Error    string `json:"error,omitempty"`
	} `json:"end,omitempty"`
	Keepalive any `json:"keepalive,omitempty"`
}

func decodeProtoBytes(value any) []byte {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil
		}
		if decoded, err := base64.StdEncoding.DecodeString(v); err == nil {
			return decoded
		}
		return []byte(v)
	case []byte:
		return v
	default:
		return nil
	}
}

type commandOptions struct {
	envs           map[string]string
	user           *string
	cwd            string
	stdin          bool
	stdinSet       bool
	timeout        time.Duration
	requestTimeout time.Duration
	onStdout       func(string)
	onStderr       func(string)
}

// CommandOption configures command execution.
type CommandOption func(*commandOptions)

func WithCommandEnv(key, value string) CommandOption {
	return func(o *commandOptions) {
		if o.envs == nil {
			o.envs = map[string]string{}
		}
		o.envs[key] = value
	}
}

func WithCommandEnvs(envs map[string]string) CommandOption {
	return func(o *commandOptions) { o.envs = cloneStringMap(envs) }
}

func WithCommandUser(user string) CommandOption {
	return func(o *commandOptions) { o.user = &user }
}

func WithCommandCwd(cwd string) CommandOption {
	return func(o *commandOptions) { o.cwd = cwd }
}

func WithCommandStdin(enabled bool) CommandOption {
	return func(o *commandOptions) {
		o.stdin = enabled
		o.stdinSet = true
	}
}

func WithCommandTimeout(timeout time.Duration) CommandOption {
	return func(o *commandOptions) { o.timeout = timeout }
}

func WithCommandRequestTimeout(timeout time.Duration) CommandOption {
	return func(o *commandOptions) { o.requestTimeout = timeout }
}

func WithStdoutHandler(fn func(string)) CommandOption {
	return func(o *commandOptions) { o.onStdout = fn }
}

func WithStderrHandler(fn func(string)) CommandOption {
	return func(o *commandOptions) { o.onStderr = fn }
}

func commandOptionsFrom(opts ...CommandOption) commandOptions {
	options := commandOptions{envs: map[string]string{}, timeout: 60 * time.Second}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

type waitOptions struct {
	onStdout func(string)
	onStderr func(string)
	onPty    func([]byte)
}

// WaitOption configures CommandHandle.Wait.
type WaitOption func(*waitOptions)

func WithWaitStdout(fn func(string)) WaitOption {
	return func(o *waitOptions) { o.onStdout = fn }
}

func WithWaitStderr(fn func(string)) WaitOption {
	return func(o *waitOptions) { o.onStderr = fn }
}

func WithWaitPty(fn func([]byte)) WaitOption {
	return func(o *waitOptions) { o.onPty = fn }
}

func waitOptionsFrom(opts ...WaitOption) waitOptions {
	var options waitOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}
