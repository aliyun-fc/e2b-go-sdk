package e2b

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// mcovClient builds a Client with the base test options plus any extras.
func mcovClient(t *testing.T, transport http.RoundTripper, opts ...Option) *Client {
	t.Helper()
	base := []Option{
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	}
	client, err := NewClient(append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// mcovSandbox builds a Sandbox wired to the given client for data-plane tests.
func mcovSandbox(t *testing.T, transport http.RoundTripper, opts ...Option) *Sandbox {
	t.Helper()
	client := mcovClient(t, transport, opts...)
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: "0.5.2"}
	sandbox.Commands = newCommands(sandbox)
	return sandbox
}

// mcovEnvelope builds a length-prefixed Connect envelope with explicit flags.
func mcovEnvelope(flags byte, payload string) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

func TestMcovConnectUnarySuccess(t *testing.T) {
	var gotPath, gotProto, gotExtra string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotProto = r.Header.Get("Connect-Protocol-Version")
		gotExtra = r.Header.Get("X-Extra")
		return jsonResponse(http.StatusOK, `{"value":"ok"}`, nil), nil
	})
	sandbox := mcovSandbox(t, transport)

	var out struct {
		Value string `json:"value"`
	}
	err := sandbox.connectUnary(context.Background(), "svc.Service", "Method", map[string]any{"a": 1}, &out, nil, nil, map[string]string{"X-Extra": "yes"})
	if err != nil {
		t.Fatalf("connectUnary: %v", err)
	}
	if out.Value != "ok" {
		t.Fatalf("value = %q", out.Value)
	}
	if gotPath != "/svc.Service/Method" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotProto != "1" {
		t.Fatalf("Connect-Protocol-Version = %q", gotProto)
	}
	if gotExtra != "yes" {
		t.Fatalf("X-Extra = %q", gotExtra)
	}
}

func TestMcovConnectUnaryNilResponseAndEmptyBody(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, ``, nil), nil
	})
	sandbox := mcovSandbox(t, transport)

	// response == nil is accepted even with an empty body.
	if err := sandbox.connectUnary(context.Background(), "svc.Service", "Method", map[string]any{}, nil, nil, nil, nil); err != nil {
		t.Fatalf("connectUnary nil response: %v", err)
	}
	// Non-nil response but empty body is also a no-op success.
	var out map[string]any
	if err := sandbox.connectUnary(context.Background(), "svc.Service", "Method", map[string]any{}, &out, nil, nil, nil); err != nil {
		t.Fatalf("connectUnary empty body: %v", err)
	}
}

func TestMcovConnectUnaryMarshalError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport should not be called on marshal error")
		return nil, nil
	})
	sandbox := mcovSandbox(t, transport)

	// A channel cannot be marshaled to JSON.
	err := sandbox.connectUnary(context.Background(), "svc.Service", "Method", make(chan int), nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestMcovConnectUnaryDecodeError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `not-json`, nil), nil
	})
	sandbox := mcovSandbox(t, transport)

	var out map[string]any
	err := sandbox.connectUnary(context.Background(), "svc.Service", "Method", map[string]any{}, &out, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "decode connect response") {
		t.Fatalf("error = %v", err)
	}
}

func TestMcovConnectUnaryHTTPError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, `{"code":"not_found","message":"gone"}`, nil), nil
	})
	sandbox := mcovSandbox(t, transport)

	err := sandbox.connectUnary(context.Background(), "svc.Service", "Method", map[string]any{}, nil, nil, nil, nil)
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %T %v, want *NotFoundError", err, err)
	}
}

func TestMcovConnectUnaryTransportError(t *testing.T) {
	t.Run("raw transport error", func(t *testing.T) {
		want := errors.New("dial fail")
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, want
		})
		sandbox := mcovSandbox(t, transport)
		err := sandbox.connectUnary(context.Background(), "svc.Service", "Method", map[string]any{}, nil, nil, nil, nil)
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})

	t.Run("context timeout maps to TimeoutError", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		sandbox := mcovSandbox(t, transport, WithRequestTimeout(10*time.Millisecond))
		err := sandbox.connectUnary(context.Background(), "svc.Service", "Method", map[string]any{}, nil, nil, nil, nil)
		var timeoutErr *TimeoutError
		if !errors.As(err, &timeoutErr) {
			t.Fatalf("error = %T %v, want *TimeoutError", err, err)
		}
	})
}

func TestMcovMapConnectHTTPError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		assert func(t *testing.T, err error)
	}{
		{
			name:   "coded error object",
			status: 500,
			body:   `{"error":{"code":"invalid_argument","message":"nope"}}`,
			assert: func(t *testing.T, err error) {
				var e *InvalidArgumentError
				if !errors.As(err, &e) {
					t.Fatalf("error = %T", err)
				}
			},
		},
		{
			name:   "top-level coded message",
			status: 500,
			body:   `{"code":"resource_exhausted","message":"too much"}`,
			assert: func(t *testing.T, err error) {
				var e *RateLimitError
				if !errors.As(err, &e) {
					t.Fatalf("error = %T", err)
				}
			},
		},
		{
			name:   "unauthorized plain body",
			status: http.StatusUnauthorized,
			body:   `denied`,
			assert: func(t *testing.T, err error) {
				var e *AuthenticationError
				if !errors.As(err, &e) {
					t.Fatalf("error = %T", err)
				}
			},
		},
		{
			name:   "not found plain body",
			status: http.StatusNotFound,
			body:   `absent`,
			assert: func(t *testing.T, err error) {
				var e *NotFoundError
				if !errors.As(err, &e) {
					t.Fatalf("error = %T", err)
				}
			},
		},
		{
			name:   "rate limited plain body",
			status: http.StatusTooManyRequests,
			body:   `slow`,
			assert: func(t *testing.T, err error) {
				var e *RateLimitError
				if !errors.As(err, &e) {
					t.Fatalf("error = %T", err)
				}
			},
		},
		{
			name:   "generic sandbox error",
			status: 500,
			body:   `boom`,
			assert: func(t *testing.T, err error) {
				var e *SandboxError
				if !errors.As(err, &e) {
					t.Fatalf("error = %T", err)
				}
				if !strings.Contains(e.Message, "500") {
					t.Fatalf("message = %q", e.Message)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := mapConnectHTTPError(tc.status, []byte(tc.body))
			tc.assert(t, err)
		})
	}
}

func TestMcovMapConnectCode(t *testing.T) {
	tests := []struct {
		code    string
		message string
		check   func(t *testing.T, err error)
	}{
		{"invalid_argument", "bad", func(t *testing.T, err error) {
			var e *InvalidArgumentError
			if !errors.As(err, &e) || e.Message != "bad" {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"unauthenticated", "no auth", func(t *testing.T, err error) {
			var e *AuthenticationError
			if !errors.As(err, &e) {
				t.Fatalf("err = %T", err)
			}
		}},
		{"not_found", "gone", func(t *testing.T, err error) {
			var e *NotFoundError
			if !errors.As(err, &e) {
				t.Fatalf("err = %T", err)
			}
		}},
		{"unavailable", "down", func(t *testing.T, err error) {
			var e *TimeoutError
			if !errors.As(err, &e) || !strings.Contains(e.Message, "sandbox timeout") {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"resource_exhausted", "quota", func(t *testing.T, err error) {
			var e *RateLimitError
			if !errors.As(err, &e) || !strings.Contains(e.Message, "Rate limit") {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"canceled", "stop", func(t *testing.T, err error) {
			var e *TimeoutError
			if !errors.As(err, &e) || !strings.Contains(e.Message, "request timeout") {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"deadline_exceeded", "late", func(t *testing.T, err error) {
			var e *TimeoutError
			if !errors.As(err, &e) || !strings.Contains(e.Message, "operation timeout") {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"unknown_code", "explained", func(t *testing.T, err error) {
			var e *SandboxError
			if !errors.As(err, &e) || e.Message != "explained" {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"only_code", "", func(t *testing.T, err error) {
			var e *SandboxError
			if !errors.As(err, &e) || e.Message != "only_code" {
				t.Fatalf("err = %#v", err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			tc.check(t, mapConnectCode(tc.code, tc.message))
		})
	}
}

func TestMcovConnectStreamNextNonEnvelope(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"n":1}` + "\n" + `{"n":2}`))
	stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: false}
	// Non-envelope streams decode via json.Decoder until EOF.
	stream.decoder = json.NewDecoder(stream.reader)

	var first map[string]int
	if err := stream.Next(&first); err != nil || first["n"] != 1 {
		t.Fatalf("first = %#v err = %v", first, err)
	}
	var second map[string]int
	if err := stream.Next(&second); err != nil || second["n"] != 2 {
		t.Fatalf("second = %#v err = %v", second, err)
	}
	var third map[string]int
	if err := stream.Next(&third); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestMcovConnectStreamNextEnvelopeVariants(t *testing.T) {
	t.Run("data then trailing error", func(t *testing.T) {
		body := io.NopCloser(bytes.NewReader(bytes.Join([][]byte{
			mcovEnvelope(0x00, `{"n":5}`),
			mcovEnvelope(0x02, `{"code":"invalid_argument","message":"bad tail"}`),
		}, nil)))
		stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}

		var out map[string]int
		if err := stream.Next(&out); err != nil || out["n"] != 5 {
			t.Fatalf("data frame out = %#v err = %v", out, err)
		}
		err := stream.Next(&out)
		var invalid *InvalidArgumentError
		if !errors.As(err, &invalid) {
			t.Fatalf("trailing error = %T %v", err, err)
		}
	})

	t.Run("empty trailer becomes EOF", func(t *testing.T) {
		body := io.NopCloser(bytes.NewReader(mcovEnvelope(0x02, `{}`)))
		stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
		if err := stream.Next(&map[string]any{}); !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF, got %v", err)
		}
	})

	t.Run("nil out on data frame", func(t *testing.T) {
		body := io.NopCloser(bytes.NewReader(mcovEnvelope(0x00, `{"n":1}`)))
		stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
		if err := stream.Next(nil); err != nil {
			t.Fatalf("nil out err = %v", err)
		}
	})

	t.Run("clean EOF on header", func(t *testing.T) {
		body := io.NopCloser(bytes.NewReader(nil))
		stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
		if err := stream.Next(&map[string]any{}); !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF, got %v", err)
		}
	})
}

func TestMcovConnectStreamCloseNil(t *testing.T) {
	var nilStream *connectStream
	if err := nilStream.Close(); err != nil {
		t.Fatalf("nil stream Close = %v", err)
	}
	empty := &connectStream{}
	if err := empty.Close(); err != nil {
		t.Fatalf("empty stream Close = %v", err)
	}
}

func TestMcovEncodeConnectJSONEnvelope(t *testing.T) {
	t.Run("marshal error", func(t *testing.T) {
		if _, err := encodeConnectJSONEnvelope(make(chan int)); err == nil {
			t.Fatal("expected marshal error")
		}
	})
	t.Run("length prefix", func(t *testing.T) {
		out, err := encodeConnectJSONEnvelope(map[string]int{"a": 1})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if got := binary.BigEndian.Uint32(out[1:5]); int(got) != len(out)-5 {
			t.Fatalf("length prefix = %d, body = %d", got, len(out)-5)
		}
	})
}

func TestMcovConnectServerStreamPlainJSONNoTimeout(t *testing.T) {
	// A plain application/json response with the request timeout disabled
	// exercises the non-envelope decoder branch and the timeout==0 fast path.
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"n":1}`, nil), nil
	})
	sandbox := mcovSandbox(t, transport, WithRequestTimeout(0))
	stream, err := sandbox.connectServerStream(context.Background(), "svc.Service", "Method", map[string]any{}, nil, 0, 0, nil)
	if err != nil {
		t.Fatalf("connectServerStream: %v", err)
	}
	defer stream.Close()
	if stream.envelope {
		t.Fatal("expected non-envelope stream for application/json")
	}
	if stream.decoder == nil {
		t.Fatal("expected decoder for non-envelope stream")
	}
}

func TestMcovConnectServerStreamEncodeError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport should not be reached on encode error")
		return nil, nil
	})
	sandbox := mcovSandbox(t, transport)
	if _, err := sandbox.connectServerStream(context.Background(), "svc.Service", "Method", make(chan int), nil, 0, 0, nil); err == nil {
		t.Fatal("expected encode error")
	}
}

func TestMcovConnectServerStreamHTTPError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"code":"unknown","message":"boom"}`, nil), nil
	})
	sandbox := mcovSandbox(t, transport)
	_, err := sandbox.connectServerStream(context.Background(), "svc.Service", "Method", map[string]any{}, nil, 0, 0, nil)
	var sbxErr *SandboxError
	if !errors.As(err, &sbxErr) {
		t.Fatalf("error = %T %v, want *SandboxError", err, err)
	}
}

func TestMcovDoStreamRequestNoTimeoutTransportError(t *testing.T) {
	// With the request timeout disabled, a raw transport error propagates
	// verbatim (not as a TimeoutError).
	want := errors.New("dial refused")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, want
	})
	sandbox := mcovSandbox(t, transport, WithRequestTimeout(0))
	_, err := sandbox.connectServerStream(context.Background(), "svc.Service", "Method", map[string]any{}, nil, 0, 0, nil)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestMcovConnectStreamNextNonEnvelopeDecodeError(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"n":`))
	stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: false}
	stream.decoder = json.NewDecoder(stream.reader)
	err := stream.Next(&map[string]any{})
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected non-EOF decode error, got %v", err)
	}
}

func TestMcovNextEnvelopeTruncatedPayload(t *testing.T) {
	// Header claims a 10-byte payload but only 3 bytes follow, so the payload
	// read fails with a non-EOF error.
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[1:5], 10)
	raw := append(header, []byte("abc")...)
	body := io.NopCloser(bytes.NewReader(raw))
	stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
	err := stream.Next(&map[string]any{})
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected truncated payload error, got %v", err)
	}
}

func TestMcovNextEnvelopeTrailingErrorObject(t *testing.T) {
	// A trailer frame carrying an error.error object maps to the coded error.
	body := io.NopCloser(bytes.NewReader(mcovEnvelope(0x02, `{"error":{"code":"unauthenticated","message":"nope"}}`)))
	stream := &connectStream{body: body, reader: bufio.NewReader(body), envelope: true}
	err := stream.Next(&map[string]any{})
	var auth *AuthenticationError
	if !errors.As(err, &auth) {
		t.Fatalf("error = %T %v, want *AuthenticationError", err, err)
	}
}

func TestMcovDoJSONNilOut(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"ignored":true}`, nil), nil
	})
	client := mustTestClient(t, transport)
	if err := client.doJSON(context.Background(), http.MethodGet, "/x", nil, nil, nil); err != nil {
		t.Fatalf("doJSON nil out: %v", err)
	}
}

func TestMcovDoFullBodyEncodeError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport should not be reached on body encode error")
		return nil, nil
	})
	client := mustTestClient(t, transport)
	// A channel body cannot be JSON-encoded.
	err := client.doJSON(context.Background(), http.MethodPost, "/x", nil, make(chan int), nil)
	if err == nil {
		t.Fatal("expected body encode error")
	}
}

// mcovErrReadCloser fails on Read to exercise response body read errors.
type mcovErrReadCloser struct{ err error }

func (r mcovErrReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (r mcovErrReadCloser) Close() error             { return nil }

func TestMcovDoFullBodyReadError(t *testing.T) {
	want := errors.New("read failure")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       mcovErrReadCloser{err: want},
		}, nil
	})
	client := mustTestClient(t, transport)
	_, _, err := client.do(context.Background(), http.MethodGet, client.config.apiURL(), "/x", nil, nil, nil)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestMcovClientConfigDeepCopy(t *testing.T) {
	client := mustTestClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, ``, nil), nil
	}))
	cfg := client.Config()
	cfg.Headers["mutated"] = "yes"
	cfg.SandboxHeaders["mutated"] = "yes"

	if _, ok := client.config.Headers["mutated"]; ok {
		t.Fatal("Config() did not deep-copy Headers")
	}
	if _, ok := client.config.SandboxHeaders["mutated"]; ok {
		t.Fatal("Config() did not deep-copy SandboxHeaders")
	}
}

func TestMcovDoFullHeaderInjection(t *testing.T) {
	t.Run("injects api key and bearer for api url", func(t *testing.T) {
		var apiKey, auth, contentType string
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			apiKey = r.Header.Get("X-API-KEY")
			auth = r.Header.Get("Authorization")
			contentType = r.Header.Get("Content-Type")
			return jsonResponse(http.StatusOK, `{"ok":true}`, nil), nil
		})
		client := mcovClient(t, transport, WithAccessToken("tok"))

		var out map[string]bool
		if err := client.doJSON(context.Background(), http.MethodPost, "/ping", nil, map[string]int{"a": 1}, &out); err != nil {
			t.Fatalf("doJSON: %v", err)
		}
		if apiKey != "e2b_0123" {
			t.Fatalf("X-API-KEY = %q", apiKey)
		}
		if auth != "Bearer tok" {
			t.Fatalf("Authorization = %q", auth)
		}
		if contentType != "application/json" {
			t.Fatalf("Content-Type = %q", contentType)
		}
		if !out["ok"] {
			t.Fatalf("out = %#v", out)
		}
	})

	t.Run("no injection for foreign base url", func(t *testing.T) {
		var apiKey string
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			apiKey = r.Header.Get("X-API-KEY")
			return jsonResponse(http.StatusOK, ``, nil), nil
		})
		client := mustTestClient(t, transport)
		_, _, err := client.do(context.Background(), http.MethodGet, "https://upload.other", "/x", nil, nil, nil)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		if apiKey != "" {
			t.Fatalf("X-API-KEY should be empty, got %q", apiKey)
		}
	})
}

func TestMcovDoFullQueryAndError(t *testing.T) {
	t.Run("attaches query and parses api error", func(t *testing.T) {
		var rawQuery string
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			rawQuery = r.URL.RawQuery
			return jsonResponse(http.StatusInternalServerError, `{"message":"boom"}`, nil), nil
		})
		client := mustTestClient(t, transport)
		q := url.Values{"a": []string{"b"}}
		_, _, err := client.do(context.Background(), http.MethodGet, client.config.apiURL(), "/x", q, nil, nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %T %v", err, err)
		}
		if rawQuery != "a=b" {
			t.Fatalf("query = %q", rawQuery)
		}
	})

	t.Run("context timeout maps to TimeoutError", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		client := mcovClient(t, transport, WithRequestTimeout(10*time.Millisecond))
		_, _, err := client.do(context.Background(), http.MethodGet, client.config.apiURL(), "/x", nil, nil, nil)
		var timeoutErr *TimeoutError
		if !errors.As(err, &timeoutErr) {
			t.Fatalf("error = %T %v", err, err)
		}
	})
}

func TestMcovDoJSONDecodeError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `not-json`, nil), nil
	})
	client := mustTestClient(t, transport)
	var out map[string]any
	err := client.doJSON(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("error = %v", err)
	}
}

func TestMcovStatusExpected(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		expected []int
		want     bool
	}{
		{"default 2xx pass", 204, nil, true},
		{"default 4xx fail", 404, nil, false},
		{"explicit match", 404, []int{404, 409}, true},
		{"explicit no match", 500, []int{404, 409}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusExpected(tc.status, tc.expected); got != tc.want {
				t.Fatalf("statusExpected = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMcovOptionalTimeout(t *testing.T) {
	if optionalTimeout(0) != nil {
		t.Fatal("expected nil for zero duration")
	}
	got := optionalTimeout(2 * time.Second)
	if got == nil || *got != 2*time.Second {
		t.Fatalf("optionalTimeout = %v", got)
	}
}

func TestMcovNextTokenHeader(t *testing.T) {
	t.Run("canonical header", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Next-Token", "abc")
		if got := nextTokenHeader(h); got != "abc" {
			t.Fatalf("token = %q", got)
		}
	})
	t.Run("non-canonical map literal", func(t *testing.T) {
		h := http.Header{"x-next-token": []string{"def"}}
		if got := nextTokenHeader(h); got != "def" {
			t.Fatalf("token = %q", got)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if got := nextTokenHeader(http.Header{}); got != "" {
			t.Fatalf("token = %q", got)
		}
	})
}

func TestMcovCompareVersion(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.2.0", "1.10.0", -1},
		{"2.0", "1.9.9", 1},
		{"1.0", "1.0.1", -1},
		{"1.0.1", "1.0", 1},
		{"0.4.0", "0.4", 0},
		{"invalid", "0.0.0", 0},
		{"1.x.3", "1.0.3", 0},
		{"1.2.3-beta+build", "1.2.3", 0},
		{" 1.2.3 ", "1.2.3", 0},
		{"", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			if got := compareVersion(tc.a, tc.b); got != tc.want {
				t.Fatalf("compareVersion(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestMcovParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want []int
	}{
		{"1.2.3", []int{1, 2, 3}},
		{"", []int{0}},
		{"1.beta.2", []int{1, 0, 2}},
		{"3.4-rc1", []int{3, 4}},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := parseVersion(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseVersion(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parseVersion(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestMcovGetSignatureVariants(t *testing.T) {
	t.Run("missing token errors", func(t *testing.T) {
		if _, err := GetSignature("/f", "read", nil, "", nil); err == nil {
			t.Fatal("expected error for empty access token")
		}
	})

	t.Run("nil user without expiration", func(t *testing.T) {
		sig, err := GetSignature("/f", "read", nil, "token", nil)
		if err != nil {
			t.Fatalf("GetSignature: %v", err)
		}
		if !strings.HasPrefix(sig.Signature, "v1_") {
			t.Fatalf("signature = %q", sig.Signature)
		}
		if sig.Expiration != nil {
			t.Fatalf("expiration = %v", *sig.Expiration)
		}
	})

	t.Run("with expiration", func(t *testing.T) {
		user := "root"
		exp := 60
		before := time.Now().Unix()
		sig, err := GetSignature("/f", "write", &user, "token", &exp)
		if err != nil {
			t.Fatalf("GetSignature: %v", err)
		}
		if sig.Expiration == nil {
			t.Fatal("expected expiration to be set")
		}
		if *sig.Expiration < before+int64(exp) {
			t.Fatalf("expiration = %d, want >= %d", *sig.Expiration, before+int64(exp))
		}
		if strings.HasSuffix(sig.Signature, "=") {
			t.Fatalf("signature has padding: %q", sig.Signature)
		}
	})
}
