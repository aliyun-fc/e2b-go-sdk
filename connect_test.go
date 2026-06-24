package e2b

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
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
	stream, err := sandbox.connectServerStream(context.Background(), "process.Process", "Start", map[string]any{"hello": "world"}, nil, 2*time.Second, nil)
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
