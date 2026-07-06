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

// pcovPtyStream wraps a body reader as an envelope-decoding connectStream.
func pcovPtyStream(body io.ReadCloser) *connectStream {
	return &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
}

// pcovPtyEnvelopes concatenates Connect-RPC envelopes for a streamed response.
func pcovPtyEnvelopes(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var buf []byte
	for _, p := range payloads {
		buf = append(buf, testConnectEnvelope(t, p)...)
	}
	return buf
}

// pcovPtyStreamResponse builds an OK server-stream response carrying payloads.
func pcovPtyStreamResponse(t *testing.T, payloads ...string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/connect+json"}},
		Body:       io.NopCloser(bytes.NewReader(pcovPtyEnvelopes(t, payloads...))),
	}
}

// pcovPtySandbox wires a data-plane Sandbox with the given envd version.
func pcovPtySandbox(client *Client, version string) *Sandbox {
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: version}
	sandbox.Commands = newCommands(sandbox)
	sandbox.Pty = newPty(sandbox)
	return sandbox
}

func TestPtyKillSuccess(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/SendSignal" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	ok, err := sandbox.Pty.Kill(context.Background(), 7)
	if err != nil || !ok {
		t.Fatalf("Kill = %v, %v", ok, err)
	}
}

func TestPtyKillNotFoundReturnsFalse(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, "", nil), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	ok, err := sandbox.Pty.Kill(context.Background(), 7)
	if err != nil {
		t.Fatalf("Kill err = %v", err)
	}
	if ok {
		t.Fatal("expected false for missing pty")
	}
}

func TestPtyKillPropagatesError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, "boom", nil), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	ok, err := sandbox.Pty.Kill(context.Background(), 7)
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Fatal("expected false on error")
	}
}

func TestPtySendStdinEncodesData(t *testing.T) {
	var body []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/SendInput" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ = io.ReadAll(r.Body)
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	if err := sandbox.Pty.SendStdin(context.Background(), 7, []byte("ls\n")); err != nil {
		t.Fatalf("SendStdin: %v", err)
	}
	var decoded struct {
		Input map[string]string `json:"input"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Input["pty"] != base64.StdEncoding.EncodeToString([]byte("ls\n")) {
		t.Fatalf("pty input = %q", decoded.Input["pty"])
	}
}

func TestPtyResizeSendsSize(t *testing.T) {
	var body []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/Update" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ = io.ReadAll(r.Body)
		return jsonResponse(http.StatusOK, "{}", nil), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	if err := sandbox.Pty.Resize(context.Background(), 7, PtySize{Rows: 40, Cols: 120}); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	pty := decoded["pty"].(map[string]any)
	size := pty["size"].(map[string]any)
	if size["rows"].(float64) != 40 || size["cols"].(float64) != 120 {
		t.Fatalf("size = %#v", size)
	}
}

func TestPtyCreateInjectsDefaultEnvs(t *testing.T) {
	var body []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/Start" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ = io.ReadAll(r.Body)
		return pcovPtyStreamResponse(t, `{"event":{"start":{"pid":88}}}`), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	handle, err := sandbox.Pty.Create(context.Background(), PtySize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = handle.Disconnect() }()
	if handle.PID() != 88 {
		t.Fatalf("PID = %d", handle.PID())
	}

	var decoded map[string]any
	if err := json.Unmarshal(body[5:], &decoded); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	process := decoded["process"].(map[string]any)
	envs := process["envs"].(map[string]any)
	if envs["TERM"] != "xterm-256color" || envs["LANG"] != "C.UTF-8" || envs["LC_ALL"] != "C.UTF-8" {
		t.Fatalf("default envs = %#v", envs)
	}
}

func TestPtyCreateWithOptions(t *testing.T) {
	var body []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ = io.ReadAll(r.Body)
		return pcovPtyStreamResponse(t, `{"event":{"start":{"pid":90}}}`), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	handle, err := sandbox.Pty.Create(
		context.Background(),
		PtySize{Rows: 10, Cols: 20},
		WithPtyCwd("/work"),
		WithPtyEnv("TERM", "screen"),
		WithPtyEnvs(map[string]string{"CUSTOM": "1"}),
		WithPtyUser("root"),
		WithPtyTimeout(30*time.Second),
		WithPtyRequestTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = handle.Disconnect() }()

	var decoded map[string]any
	if err := json.Unmarshal(body[5:], &decoded); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	process := decoded["process"].(map[string]any)
	if process["cwd"] != "/work" {
		t.Fatalf("cwd = %#v", process["cwd"])
	}
	envs := process["envs"].(map[string]any)
	// WithPtyEnvs replaces the env map, so the CUSTOM key must be present and
	// missing TERM should be filled with the default.
	if envs["CUSTOM"] != "1" {
		t.Fatalf("custom env = %#v", envs)
	}
	if envs["TERM"] != "xterm-256color" {
		t.Fatalf("TERM = %#v", envs["TERM"])
	}
}

func TestPtyCreateRejectsMissingStart(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return pcovPtyStreamResponse(t, `{"event":{"data":{"pty":"AA=="}}}`), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	_, err := sandbox.Pty.Create(context.Background(), PtySize{Rows: 24, Cols: 80})
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T %v, want SandboxError", err, err)
	}
}

func TestPtyCreateStreamError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, "boom", nil), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	_, err := sandbox.Pty.Create(context.Background(), PtySize{Rows: 24, Cols: 80})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPtyConnectReturnsHandle(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/Connect" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		return pcovPtyStreamResponse(t, `{"event":{"start":{"pid":91}}}`), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	handle, err := sandbox.Pty.Connect(context.Background(), 91, 2*time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = handle.Disconnect() }()
	if handle.PID() != 91 {
		t.Fatalf("PID = %d", handle.PID())
	}
}

func TestPtyConnectStreamError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, "boom", nil), nil
	})
	sandbox := pcovPtySandbox(mustTestClient(t, transport), "0.6.4")

	_, err := sandbox.Pty.Connect(context.Background(), 91, 0)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPtyWaitDeliversPtyOutput(t *testing.T) {
	ptyData := []byte("prompt$ ")
	body := io.NopCloser(bytes.NewReader(pcovPtyEnvelopes(t,
		`{"event":{"start":{"pid":5}}}`,
		`{"event":{"data":{"pty":"`+base64.StdEncoding.EncodeToString(ptyData)+`"}}}`,
		`{"event":{"end":{"exitCode":0}}}`,
	)))
	sandbox := pcovPtySandbox(mustTestClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	})), "0.6.4")
	handle, err := newCommandHandleFromStream(pcovPtyStream(body), nil, sandbox.Pty)
	if err != nil {
		t.Fatalf("newCommandHandleFromStream: %v", err)
	}

	var collected []byte
	result, err := handle.Wait(context.Background(), WithWaitPty(func(b []byte) {
		collected = append(collected, b...)
	}))
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	if string(collected) != "prompt$ " {
		t.Fatalf("pty output = %q", collected)
	}
}
