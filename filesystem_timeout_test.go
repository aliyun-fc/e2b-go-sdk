package e2b

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// streamHandler writes chunks with a delay before each chunk, flushing so the
// client observes progress at real wall-clock intervals.
func streamHandler(chunks int, delayBefore func(i int) time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for i := 0; i < chunks; i++ {
			if d := delayBefore(i); d > 0 {
				select {
				case <-time.After(d):
				case <-r.Context().Done():
					return
				}
			}
			if _, err := w.Write([]byte("chunk")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func newFilesystemForServer(t *testing.T, server *httptest.Server, opts ...Option) *Filesystem {
	t.Helper()
	clientOpts := append([]Option{
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(server.Client()),
	}, opts...)
	client, err := NewClient(clientOpts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: server.URL, envdVersion: "0.5.2"}
	return newFilesystem(sandbox)
}

func TestFileReadSurvivesSlowButProgressingStream(t *testing.T) {
	// Five chunks, 40ms apart — 200ms total, well past the 120ms request
	// timeout. The old total-deadline behavior would abort this; the idle
	// timeout must not, because every chunk arrives within the window.
	server := httptest.NewServer(streamHandler(5, func(int) time.Duration { return 40 * time.Millisecond }))
	defer server.Close()
	f := newFilesystemForServer(t, server)

	data, err := f.Read(context.Background(), "/f", WithFileRequestTimeout(120*time.Millisecond))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := "chunkchunkchunkchunkchunk"; data != want {
		t.Fatalf("data = %q, want %q", data, want)
	}
}

func TestFileReadTimesOutOnStalledStream(t *testing.T) {
	// First chunk immediately, then a long stall past the idle window.
	server := httptest.NewServer(streamHandler(2, func(i int) time.Duration {
		if i == 1 {
			return 800 * time.Millisecond
		}
		return 0
	}))
	defer server.Close()
	f := newFilesystemForServer(t, server)

	_, err := f.Read(context.Background(), "/f",
		WithFileRequestTimeout(2*time.Second),
		WithFileStreamIdleTimeout(120*time.Millisecond),
	)
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Read error = %T %v, want *TimeoutError", err, err)
	}
}

func TestFileReadDisabledIdleTimeoutSurvivesLongStall(t *testing.T) {
	// A stall longer than the handshake timeout must still succeed when the
	// body idle timeout is disabled with WithFileStreamIdleTimeout(0).
	server := httptest.NewServer(streamHandler(2, func(i int) time.Duration {
		if i == 1 {
			return 400 * time.Millisecond
		}
		return 0
	}))
	defer server.Close()
	f := newFilesystemForServer(t, server)

	data, err := f.Read(context.Background(), "/f",
		WithFileRequestTimeout(120*time.Millisecond),
		WithFileStreamIdleTimeout(0),
	)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := "chunkchunk"; data != want {
		t.Fatalf("data = %q, want %q", data, want)
	}
}

func TestFileStreamSlowConsumerDoesNotTripIdleTimeout(t *testing.T) {
	// The server delivers both chunks promptly; the consumer then pauses far
	// longer than the idle timeout between reads. The idle timeout counts only
	// while waiting on the wire, so a slow consumer must not trip it.
	server := httptest.NewServer(streamHandler(2, func(i int) time.Duration {
		if i == 1 {
			return 30 * time.Millisecond
		}
		return 0
	}))
	defer server.Close()
	f := newFilesystemForServer(t, server)

	reader, err := f.ReadStream(context.Background(), "/f", WithFileStreamIdleTimeout(60*time.Millisecond))
	if err != nil {
		t.Fatalf("ReadStream: %v", err)
	}
	defer reader.Close()

	var got []byte
	buf := make([]byte, 8)
	for {
		n, err := reader.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		time.Sleep(150 * time.Millisecond) // far longer than the 60ms idle window
	}
	if want := "chunkchunk"; string(got) != want {
		t.Fatalf("data = %q, want %q", got, want)
	}
}

func TestFileReadExplicitZeroRequestTimeoutDisables(t *testing.T) {
	// Global RequestTimeout is small; a per-call WithFileRequestTimeout(0) must
	// disable it entirely rather than fall back to the global value.
	server := httptest.NewServer(streamHandler(2, func(i int) time.Duration {
		if i == 1 {
			return 250 * time.Millisecond
		}
		return 0
	}))
	defer server.Close()
	f := newFilesystemForServer(t, server, WithRequestTimeout(60*time.Millisecond))

	data, err := f.Read(context.Background(), "/f", WithFileRequestTimeout(0))
	if err != nil {
		t.Fatalf("Read with disabled timeout: %v", err)
	}
	if want := "chunkchunk"; data != want {
		t.Fatalf("data = %q, want %q", data, want)
	}
}

func TestFileReadUnsetTimeoutFallsBackToGlobal(t *testing.T) {
	// With no per-call timeout, a stall past the small global RequestTimeout
	// must still trip (confirms the explicit-0 path above is what disables it).
	server := httptest.NewServer(streamHandler(2, func(i int) time.Duration {
		if i == 1 {
			return 400 * time.Millisecond
		}
		return 0
	}))
	defer server.Close()
	f := newFilesystemForServer(t, server, WithRequestTimeout(80*time.Millisecond))

	_, err := f.Read(context.Background(), "/f")
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Read error = %T %v, want *TimeoutError", err, err)
	}
}

func TestFileWriteStreamIdleZeroStillBoundedByRequestTimeout(t *testing.T) {
	// WithFileStreamIdleTimeout(0) must not disable the write body/response-header
	// timeout: writes are bounded by the request timeout regardless. The server
	// consumes the upload then stalls before sending response headers.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(400 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"f","path":"/f","type":"file"}]`))
	}))
	defer server.Close()
	f := newFilesystemForServer(t, server, WithRequestTimeout(100*time.Millisecond))

	_, err := f.WriteBytes(context.Background(), "/f", []byte("hello"), WithFileStreamIdleTimeout(0))
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("WriteBytes error = %T %v, want *TimeoutError", err, err)
	}
}

// stalledJSONHandler stalls for the given duration, then returns the body.
func stalledJSONHandler(stall time.Duration, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(stall):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestListExplicitZeroRequestTimeoutDisables(t *testing.T) {
	// Connect-RPC path: WithFileRequestTimeout(0) must disable the timeout too,
	// not fall back to the small global RequestTimeout.
	server := httptest.NewServer(stalledJSONHandler(200*time.Millisecond, `{"entries":[]}`))
	defer server.Close()
	f := newFilesystemForServer(t, server, WithRequestTimeout(60*time.Millisecond))

	if _, err := f.List(context.Background(), "/dir", WithFileRequestTimeout(0)); err != nil {
		t.Fatalf("List with disabled timeout: %v", err)
	}
}

func TestListUnsetRequestTimeoutFallsBackToGlobal(t *testing.T) {
	// With no per-call timeout, the small global RequestTimeout still applies.
	server := httptest.NewServer(stalledJSONHandler(400*time.Millisecond, `{"entries":[]}`))
	defer server.Close()
	f := newFilesystemForServer(t, server, WithRequestTimeout(80*time.Millisecond))

	_, err := f.List(context.Background(), "/dir")
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("List error = %T %v, want *TimeoutError", err, err)
	}
}

func TestFileWritePreservesContentLength(t *testing.T) {
	// Wrapping the request body for idle tracking must not defeat
	// http.NewRequestWithContext's Content-Length detection (which would force
	// chunked transfer-encoding).
	var contentLength int64
	var transferEncoding []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentLength = r.ContentLength
		transferEncoding = r.TransferEncoding
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"f","path":"/f","type":"file"}]`))
	}))
	defer server.Close()
	f := newFilesystemForServer(t, server)

	if _, err := f.WriteBytes(context.Background(), "/f", []byte("hello world")); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	if contentLength <= 0 {
		t.Fatalf("Content-Length = %d, want > 0 (upload became chunked)", contentLength)
	}
	if len(transferEncoding) != 0 {
		t.Fatalf("Transfer-Encoding = %v, want none", transferEncoding)
	}
}

func TestFileReadPropagatesCallerCancellation(t *testing.T) {
	server := httptest.NewServer(streamHandler(2, func(i int) time.Duration {
		if i == 1 {
			return time.Second
		}
		return 0
	}))
	defer server.Close()
	f := newFilesystemForServer(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	_, err := f.Read(ctx, "/f", WithFileRequestTimeout(2*time.Second))
	if err == nil {
		t.Fatal("expected error from caller cancellation")
	}
	// Caller cancellation must not be misreported as a request timeout.
	var timeout *TimeoutError
	if errors.As(err, &timeout) {
		t.Fatalf("caller cancellation reported as timeout: %v", err)
	}
}
