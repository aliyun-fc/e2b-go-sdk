package e2b

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestConnectServerStreamUsesEnvelopeAndTimeoutHeader(t *testing.T) {
	var requestBody []byte
	var contentType string
	var timeoutHeader string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/process.Process/Start" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		contentType = r.Header.Get("Content-Type")
		timeoutHeader = r.Header.Get("Connect-Timeout-Ms")
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/connect+json"}},
			Body:       io.NopCloser(bytes.NewReader(testConnectEnvelope(t, `{"event":{"start":{"pid":42}}}`))),
		}, nil
	})

	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: "0.5.2"}
	sandbox.Commands = newCommands(sandbox)
	stream, err := sandbox.connectServerStream(context.Background(), "process.Process", "Start", map[string]any{"hello": "world"}, nil, 2*time.Second, 0, nil)
	if err != nil {
		t.Fatalf("connectServerStream: %v", err)
	}
	defer stream.Close()

	if contentType != "application/connect+json" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if timeoutHeader != "2000" {
		t.Fatalf("Connect-Timeout-Ms = %q", timeoutHeader)
	}
	if len(requestBody) < 5 || requestBody[0] != 0 {
		t.Fatalf("request envelope header = %v", requestBody)
	}
	length := binary.BigEndian.Uint32(requestBody[1:5])
	if int(length) != len(requestBody)-5 {
		t.Fatalf("envelope length = %d body=%d", length, len(requestBody)-5)
	}
	var decoded map[string]any
	if err := json.Unmarshal(requestBody[5:], &decoded); err != nil {
		t.Fatalf("decode envelope payload: %v", err)
	}
	if decoded["hello"] != "world" {
		t.Fatalf("payload = %#v", decoded)
	}
}

func TestCommandWaitDecodesSplitUTF8(t *testing.T) {
	text := []byte("世界")
	body := io.NopCloser(bytes.NewReader(bytes.Join([][]byte{
		testConnectEnvelope(t, `{"event":{"start":{"pid":7}}}`),
		testConnectEnvelope(t, `{"event":{"data":{"stdout":"`+base64.StdEncoding.EncodeToString(text[:2])+`"}}}`),
		testConnectEnvelope(t, `{"event":{"data":{"stdout":"`+base64.StdEncoding.EncodeToString(text[2:])+`"}}}`),
		testConnectEnvelope(t, `{"event":{"end":{"exitCode":0}}}`),
	}, nil)))
	stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
	handle, err := newCommandHandleFromStream(stream, nil, nil)
	if err != nil {
		t.Fatalf("newCommandHandleFromStream: %v", err)
	}
	result, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.Stdout != "世界" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}

func TestCommandWaitRespectsCanceledContext(t *testing.T) {
	body := io.NopCloser(bytes.NewReader(nil))
	handle := &CommandHandle{
		stream: &connectStream{body: body, reader: bufio.NewReader(body), envelope: true},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := handle.Wait(ctx)
	if err != context.Canceled {
		t.Fatalf("Wait error = %v", err)
	}
}

func testConnectEnvelope(t *testing.T, payload string) []byte {
	t.Helper()
	out := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

func TestConnectServerStreamReturnsTransportError(t *testing.T) {
	want := errors.New("dial refused")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, want
	})
	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: "0.5.2"}
	sandbox.Commands = newCommands(sandbox)
	_, err = sandbox.connectServerStream(context.Background(), "process.Process", "Start", map[string]any{}, nil, 0, 0, nil)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	var timeoutErr *TimeoutError
	if errors.As(err, &timeoutErr) {
		t.Fatalf("transport error was reported as timeout: %v", err)
	}
}

func TestConnectStreamRejectsOversizedEnvelope(t *testing.T) {
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[1:5], maxConnectEnvelopeSize+1)
	body := io.NopCloser(bytes.NewReader(header))
	stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
	err := stream.Next(&map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("Next error = %v", err)
	}
}

func TestCommandRequestTimeoutControlsStreamSetup(t *testing.T) {
	var timeoutHeader string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		timeoutHeader = r.Header.Get("Connect-Timeout-Ms")
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: "0.5.2"}
	sandbox.Commands = newCommands(sandbox)
	start := time.Now()
	_, err = sandbox.Commands.Start(context.Background(), "sleep 1", WithCommandTimeout(2*time.Second), WithCommandRequestTimeout(10*time.Millisecond))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("error = %T %v, want TimeoutError", err, err)
	}
	if timeoutHeader != "2000" {
		t.Fatalf("Connect-Timeout-Ms = %q", timeoutHeader)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("request timeout took too long: %s", elapsed)
	}
}

func TestDoStreamRequestClosesLateResponseAfterTimeout(t *testing.T) {
	releaseResponse := make(chan struct{})
	bodyClosed := make(chan struct{})
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-releaseResponse
		body := &closeNotifyReadCloser{
			Reader: bytes.NewReader(testConnectEnvelope(t, `{"event":{"start":{"pid":42}}}`)),
			closed: bodyClosed,
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/connect+json"}},
			Body:       body,
		}, nil
	})
	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: "0.5.2"}
	sandbox.Commands = newCommands(sandbox)
	_, err = sandbox.connectServerStream(context.Background(), "process.Process", "Start", map[string]any{}, nil, 0, time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("error = %T %v, want TimeoutError", err, err)
	}
	close(releaseResponse)
	select {
	case <-bodyClosed:
	case <-time.After(time.Second):
		t.Fatal("late response body was not closed")
	}
}

type closeNotifyReadCloser struct {
	io.Reader
	closed chan struct{}
}

func (c *closeNotifyReadCloser) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}
