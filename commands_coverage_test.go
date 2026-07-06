package e2b

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// pcovCmdStream wraps a body reader as an envelope-decoding connectStream.
func pcovCmdStream(body io.ReadCloser) *connectStream {
	return &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
}

// pcovCmdEnvelopes concatenates Connect-RPC envelopes for a streamed response.
func pcovCmdEnvelopes(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var buf []byte
	for _, p := range payloads {
		buf = append(buf, testConnectEnvelope(t, p)...)
	}
	return buf
}

// pcovCmdStreamResponse builds an OK server-stream response carrying payloads.
func pcovCmdStreamResponse(t *testing.T, payloads ...string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/connect+json"}},
		Body:       io.NopCloser(bytes.NewReader(pcovCmdEnvelopes(t, payloads...))),
	}
}

// pcovCmdSandbox wires a data-plane Sandbox with the given envd version.
func pcovCmdSandbox(client *Client, version string) *Sandbox {
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: version}
	sandbox.Commands = newCommands(sandbox)
	sandbox.Pty = newPty(sandbox)
	return sandbox
}

func TestCommandsListParsesProcesses(t *testing.T) {
	// Arrange
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/List" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body := `{"processes":[{"pid":11,"tag":"tag-1","config":{"cmd":"bash","args":["-c","ls"],"envs":{"A":"B"},"cwd":"/tmp"}}]}`
		return jsonResponse(http.StatusOK, body, nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	// Act
	infos, err := sandbox.Commands.List(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d", len(infos))
	}
	got := infos[0]
	if got.PID != 11 || got.Tag != "tag-1" || got.Cmd != "bash" || got.Cwd != "/tmp" {
		t.Fatalf("info = %#v", got)
	}
	if len(got.Args) != 2 || got.Args[1] != "ls" || got.Envs["A"] != "B" {
		t.Fatalf("info args/envs = %#v", got)
	}
}

func TestCommandsListReturnsError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, "boom", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	_, err := sandbox.Commands.List(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCommandsKillSuccess(t *testing.T) {
	var body []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/SendSignal" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ = io.ReadAll(r.Body)
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	ok, err := sandbox.Commands.Kill(context.Background(), 7)
	if err != nil || !ok {
		t.Fatalf("Kill = %v, %v", ok, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["signal"] != "SIGNAL_SIGKILL" {
		t.Fatalf("signal = %#v", decoded["signal"])
	}
}

func TestCommandsKillNotFoundReturnsFalse(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, "", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	ok, err := sandbox.Commands.Kill(context.Background(), 7)
	if err != nil {
		t.Fatalf("Kill err = %v", err)
	}
	if ok {
		t.Fatal("expected false for missing process")
	}
}

func TestCommandsKillPropagatesError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, "boom", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	ok, err := sandbox.Commands.Kill(context.Background(), 7)
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Fatal("expected false on error")
	}
}

func TestCommandsSendStdinEncodesData(t *testing.T) {
	var body []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/SendInput" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ = io.ReadAll(r.Body)
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	if err := sandbox.Commands.SendStdin(context.Background(), 7, []byte("hi")); err != nil {
		t.Fatalf("SendStdin: %v", err)
	}
	var decoded struct {
		Input map[string]string `json:"input"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Input["stdin"] != base64.StdEncoding.EncodeToString([]byte("hi")) {
		t.Fatalf("stdin = %q", decoded.Input["stdin"])
	}
}

func TestCommandsCloseStdinUnsupportedVersion(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("no request expected")
		return nil, nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.5.1")

	err := sandbox.Commands.CloseStdin(context.Background(), 7)
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T %v, want SandboxError", err, err)
	}
}

func TestCommandsCloseStdinSupportedVersion(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/CloseStdin" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	if err := sandbox.Commands.CloseStdin(context.Background(), 7); err != nil {
		t.Fatalf("CloseStdin: %v", err)
	}
}

func TestCommandsStartStdinUnsupportedVersion(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("no request expected")
		return nil, nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.2.0")

	_, err := sandbox.Commands.Start(context.Background(), "ls", WithCommandStdin(false))
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T %v, want SandboxError", err, err)
	}
}

func TestCommandsStartBuildsRequestBody(t *testing.T) {
	var body []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/Start" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ = io.ReadAll(r.Body)
		return pcovCmdStreamResponse(t, `{"event":{"start":{"pid":21}}}`), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	handle, err := sandbox.Commands.Start(
		context.Background(),
		"echo hi",
		WithCommandCwd("/work"),
		WithCommandEnv("K", "V"),
		WithCommandEnvs(map[string]string{"K2": "V2"}),
		WithCommandUser("root"),
		WithStdoutHandler(func(string) {}),
		WithStderrHandler(func(string) {}),
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = handle.Disconnect() }()
	if handle.PID() != 21 {
		t.Fatalf("PID = %d", handle.PID())
	}

	var decoded map[string]any
	if err := json.Unmarshal(body[5:], &decoded); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	process := decoded["process"].(map[string]any)
	if process["cwd"] != "/work" {
		t.Fatalf("cwd = %#v", process["cwd"])
	}
	envs := process["envs"].(map[string]any)
	if envs["K2"] != "V2" {
		t.Fatalf("envs = %#v", envs)
	}
}

func TestCommandsRunSuccess(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return pcovCmdStreamResponse(t,
			`{"event":{"start":{"pid":33}}}`,
			`{"event":{"data":{"stdout":"`+base64.StdEncoding.EncodeToString([]byte("out"))+`"}}}`,
			`{"event":{"data":{"stderr":"`+base64.StdEncoding.EncodeToString([]byte("err"))+`"}}}`,
			`{"event":{"end":{"exitCode":0}}}`,
		), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	var stdout, stderr string
	result, err := sandbox.Commands.Run(
		context.Background(),
		"echo hi",
		WithStdoutHandler(func(s string) { stdout += s }),
		WithStderrHandler(func(s string) { stderr += s }),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stdout != "out" || result.Stderr != "err" || result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
	if stdout != "out" || stderr != "err" {
		t.Fatalf("handlers = %q %q", stdout, stderr)
	}
}

func TestCommandsRunNonZeroExitReturnsError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return pcovCmdStreamResponse(t,
			`{"event":{"start":{"pid":33}}}`,
			`{"event":{"end":{"exitCode":2,"error":"failed"}}}`,
		), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	result, err := sandbox.Commands.Run(context.Background(), "false")
	var exitErr *CommandExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %T %v, want CommandExitError", err, err)
	}
	if result.ExitCode != 2 || exitErr.Result.ExitCode != 2 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
}

func TestCommandsRunStartError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, "boom", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	_, err := sandbox.Commands.Run(context.Background(), "ls")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCommandsConnectReturnsHandle(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/Connect" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		return pcovCmdStreamResponse(t, `{"event":{"start":{"pid":44}}}`), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	handle, err := sandbox.Commands.Connect(context.Background(), 44, 2*time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = handle.Disconnect() }()
	if handle.PID() != 44 {
		t.Fatalf("PID = %d", handle.PID())
	}
}

func TestCommandsConnectStreamError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, "boom", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")

	_, err := sandbox.Commands.Connect(context.Background(), 44, 0)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCommandHandleFromStreamRejectsMissingStart(t *testing.T) {
	body := io.NopCloser(bytes.NewReader(pcovCmdEnvelopes(t, `{"event":{"data":{"stdout":"AA=="}}}`)))
	stream := pcovCmdStream(body)

	_, err := newCommandHandleFromStream(stream, nil, nil)
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T %v, want SandboxError", err, err)
	}
}

func TestCommandHandleNextReturnsEvents(t *testing.T) {
	body := io.NopCloser(bytes.NewReader(pcovCmdEnvelopes(t,
		`{"event":{"start":{"pid":5}}}`,
		`{"event":{"data":{"stdout":"`+base64.StdEncoding.EncodeToString([]byte("x"))+`"}}}`,
		`{"event":{"end":{"exitCode":0}}}`,
	)))
	handle, err := newCommandHandleFromStream(pcovCmdStream(body), nil, nil)
	if err != nil {
		t.Fatalf("newCommandHandleFromStream: %v", err)
	}

	out, err := handle.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if out.Stdout != "x" {
		t.Fatalf("stdout = %q", out.Stdout)
	}
	// Drain until EOF is reported after the end event.
	for {
		_, err = handle.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next drain: %v", err)
		}
	}
}

func TestCommandWaitReturnsDecodeError(t *testing.T) {
	body := io.NopCloser(bytes.NewReader(pcovCmdEnvelopes(t,
		`{"event":{"start":{"pid":5}}}`,
		`{oops`,
	)))
	handle, err := newCommandHandleFromStream(pcovCmdStream(body), nil, nil)
	if err != nil {
		t.Fatalf("newCommandHandleFromStream: %v", err)
	}
	if _, err := handle.Wait(context.Background()); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestCommandWaitWithoutEndEvent(t *testing.T) {
	body := io.NopCloser(bytes.NewReader(pcovCmdEnvelopes(t, `{"event":{"start":{"pid":5}}}`)))
	handle, err := newCommandHandleFromStream(pcovCmdStream(body), nil, nil)
	if err != nil {
		t.Fatalf("newCommandHandleFromStream: %v", err)
	}
	_, err = handle.Wait(context.Background())
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T %v, want SandboxError", err, err)
	}
}

func TestCommandHandleKillRoutesToCommands(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/SendSignal" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")
	handle := &CommandHandle{pid: 7, commands: sandbox.Commands}

	ok, err := handle.Kill(context.Background())
	if err != nil || !ok {
		t.Fatalf("Kill = %v, %v", ok, err)
	}
}

func TestCommandHandleKillRoutesToPty(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/SendSignal" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")
	handle := &CommandHandle{pid: 7, pty: sandbox.Pty}

	ok, err := handle.Kill(context.Background())
	if err != nil || !ok {
		t.Fatalf("Kill = %v, %v", ok, err)
	}
}

func TestCommandHandleSendStdin(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")
	handle := &CommandHandle{pid: 7, commands: sandbox.Commands}

	if err := handle.SendStdin(context.Background(), []byte("data")); err != nil {
		t.Fatalf("SendStdin: %v", err)
	}
}

func TestCommandHandleSendStdinUnsupported(t *testing.T) {
	handle := &CommandHandle{pid: 7}
	err := handle.SendStdin(context.Background(), []byte("data"))
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T %v, want SandboxError", err, err)
	}
}

func TestCommandHandleCloseStdin(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovCmdSandbox(mustTestClient(t, transport), "0.6.4")
	handle := &CommandHandle{pid: 7, commands: sandbox.Commands}

	if err := handle.CloseStdin(context.Background()); err != nil {
		t.Fatalf("CloseStdin: %v", err)
	}
}

func TestCommandHandleCloseStdinUnsupported(t *testing.T) {
	handle := &CommandHandle{pid: 7}
	err := handle.CloseStdin(context.Background())
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T %v, want SandboxError", err, err)
	}
}

func TestCommandDecodeProtoBytesVariants(t *testing.T) {
	if got := decodeProtoBytes([]byte("raw")); string(got) != "raw" {
		t.Fatalf("[]byte case = %q", got)
	}
	if got := decodeProtoBytes(""); got != nil {
		t.Fatalf("empty string = %v", got)
	}
	if got := decodeProtoBytes("!!!notbase64!!!"); string(got) != "!!!notbase64!!!" {
		t.Fatalf("invalid base64 fallback = %q", got)
	}
	if got := decodeProtoBytes(42); got != nil {
		t.Fatalf("default case = %v", got)
	}
}
