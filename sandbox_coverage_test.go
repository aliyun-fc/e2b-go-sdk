package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// sbcovSandbox builds a Sandbox wired to a client using the given transport,
// without performing any network round-trips at construction time.
func sbcovSandbox(t *testing.T, transport http.RoundTripper) *Sandbox {
	t.Helper()
	client := mustTestClient(t, transport)
	return &Sandbox{
		client:             client,
		sandboxID:          "sbx_test",
		sandboxDomain:      "example.com",
		envdVersion:        "0.6.4",
		envdAccessToken:    "envd-token",
		trafficAccessToken: "traffic-token",
		envdAPIURL:         "https://envd.test",
		envdDirectURL:      "https://direct.test",
	}
}

// sbcovFailTransport fails every request; useful to assert a method issues no
// unexpected calls (e.g. debug short-circuits).
type sbcovFailTransport struct{ t *testing.T }

func (f sbcovFailTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
	return nil, nil
}

func TestSandboxCreateEmptyTemplateDefaultsAndOptions(t *testing.T) {
	var received newSandboxRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonResponse(http.StatusCreated, `{"clientID":"c","envdVersion":"0.6.4","sandboxID":"sbx","templateID":"base","domain":"example.com"}`, nil), nil
	})

	client := mustTestClient(t, transport)
	allowPublic := true
	_, err := client.CreateSandbox(
		context.Background(),
		WithTemplate(""), // empty template, no MCP -> DefaultTemplate
		WithTimeout(0),   // zero timeout -> DefaultSandboxTimeoutSeconds
		WithEnvs(map[string]string{"K": "V"}),
		WithNetwork(SandboxNetworkOpts{AllowOut: []string{"example.com"}, AllowPublicTraffic: &allowPublic}),
	)
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if received.TemplateID != DefaultTemplate {
		t.Fatalf("templateID = %q, want %q", received.TemplateID, DefaultTemplate)
	}
	if received.Timeout != DefaultSandboxTimeoutSeconds {
		t.Fatalf("timeout = %d", received.Timeout)
	}
	if received.EnvVars["K"] != "V" {
		t.Fatalf("envVars = %#v", received.EnvVars)
	}
	if received.Network == nil || len(received.Network.AllowOut) != 1 {
		t.Fatalf("network = %#v", received.Network)
	}
}

func TestSandboxCreateEmptyTemplateWithMCPUsesGateway(t *testing.T) {
	var templateID string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			var body newSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			templateID = body.TemplateID
			return jsonResponse(http.StatusCreated, `{"clientID":"c","envdVersion":"0.6.4","sandboxID":"sbx_mcp","templateID":"mcp-gateway","domain":"example.com","envdAccessToken":"tok"}`, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/process.Process/Start":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/connect+json"}},
				Body:       io.NopCloser(bytes.NewReader(testConnectEnvelope(t, `{"event":{"start":{"pid":42}}}`))),
			}, nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	sandbox, err := client.CreateSandbox(context.Background(), WithTemplate(""), WithMCP(map[string]any{"server": "stdio"}))
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if templateID != DefaultMCPTemplate {
		t.Fatalf("templateID = %q, want %q", templateID, DefaultMCPTemplate)
	}
	if sandbox.MCPToken() == "" {
		t.Fatal("expected MCP token")
	}
}

func TestSandboxCreateAutoResumeRequiresPause(t *testing.T) {
	transport := sbcovFailTransport{t: t}
	client := mustTestClient(t, transport)
	_, err := client.CreateSandbox(
		context.Background(),
		WithLifecycle(SandboxLifecycle{OnTimeout: "kill", AutoResume: true}),
	)
	var invalid *InvalidArgumentError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %T %v, want InvalidArgumentError", err, err)
	}
}

func TestSandboxCreateOldEnvdVersionCleansUp(t *testing.T) {
	var deleted bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			return jsonResponse(http.StatusCreated, `{"clientID":"c","envdVersion":"0.0.9","sandboxID":"sbx_old","templateID":"base","domain":"example.com"}`, nil), nil
		case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/sbx_old":
			deleted = true
			return jsonResponse(http.StatusNoContent, "", nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	_, err := client.CreateSandbox(context.Background())
	var tmplErr *TemplateError
	if !errors.As(err, &tmplErr) {
		t.Fatalf("error = %T %v, want TemplateError", err, err)
	}
	if !deleted {
		t.Fatal("expected cleanup DELETE for outdated sandbox")
	}
}

func TestSandboxCreateMCPGatewayStartFailureCleansUp(t *testing.T) {
	var deleted bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			return jsonResponse(http.StatusCreated, `{"clientID":"c","envdVersion":"0.6.4","sandboxID":"sbx_mcp","templateID":"mcp-gateway","domain":"example.com","envdAccessToken":"tok"}`, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/process.Process/Start":
			return nil, errors.New("dial refused")
		case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/sbx_mcp":
			deleted = true
			return jsonResponse(http.StatusNoContent, "", nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	_, err := client.CreateSandbox(context.Background(), WithMCP(map[string]any{"server": "stdio"}))
	var sbxErr *SandboxError
	if !errors.As(err, &sbxErr) {
		t.Fatalf("error = %T %v, want SandboxError", err, err)
	}
	if !strings.Contains(sbxErr.Message, "failed to start MCP gateway") {
		t.Fatalf("message = %q", sbxErr.Message)
	}
	if !deleted {
		t.Fatal("expected cleanup DELETE after gateway failure")
	}
}

func TestSandboxCreateErrorPropagatesAPIError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
	})
	client := mustTestClient(t, transport)
	_, err := client.CreateSandbox(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestSandboxTopLevelCreateSandboxRequiresAPIKey(t *testing.T) {
	t.Setenv("E2B_API_KEY", "")
	_, err := CreateSandbox(context.Background())
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %T %v, want AuthenticationError", err, err)
	}
}

func TestSandboxFormatMCPGatewayStartError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "plain error",
			err:  errors.New("boom"),
			want: "failed to start MCP gateway: boom",
		},
		{
			name: "exit error prefers stderr",
			err:  &CommandExitError{Result: CommandResult{Stderr: "  stderr detail  ", Error: "err", Stdout: "out"}},
			want: "failed to start MCP gateway: stderr detail",
		},
		{
			name: "exit error falls back to error",
			err:  &CommandExitError{Result: CommandResult{Error: " err detail ", Stdout: "out"}},
			want: "failed to start MCP gateway: err detail",
		},
		{
			name: "exit error falls back to stdout",
			err:  &CommandExitError{Result: CommandResult{Stdout: " out detail "}},
			want: "failed to start MCP gateway: out detail",
		},
		{
			name: "exit error falls back to error string",
			err:  &CommandExitError{Result: CommandResult{ExitCode: 3}},
			want: "failed to start MCP gateway: command exited with code 3 and error:\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatMCPGatewayStartError(tc.err)
			var sbxErr *SandboxError
			if !errors.As(got, &sbxErr) {
				t.Fatalf("error = %T, want SandboxError", got)
			}
			if sbxErr.Message != tc.want {
				t.Fatalf("message = %q, want %q", sbxErr.Message, tc.want)
			}
		})
	}
}

func TestSandboxConnectSandbox(t *testing.T) {
	tests := []struct {
		name        string
		timeout     []int
		wantTimeout float64
	}{
		{name: "default timeout", timeout: nil, wantTimeout: DefaultSandboxTimeoutSeconds},
		{name: "explicit timeout", timeout: []int{120}, wantTimeout: 120},
		{name: "non-positive timeout ignored", timeout: []int{-5}, wantTimeout: DefaultSandboxTimeoutSeconds},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx_c/connect" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				return jsonResponse(http.StatusOK, `{"clientID":"c","envdVersion":"0.6.4","sandboxID":"sbx_c","templateID":"base","domain":"example.com"}`, nil), nil
			})
			client := mustTestClient(t, transport)
			sandbox, err := client.ConnectSandbox(context.Background(), "sbx_c", tc.timeout...)
			if err != nil {
				t.Fatalf("ConnectSandbox: %v", err)
			}
			if sandbox.SandboxID() != "sbx_c" {
				t.Fatalf("sandbox id = %q", sandbox.SandboxID())
			}
			if body["timeout"].(float64) != tc.wantTimeout {
				t.Fatalf("timeout = %#v, want %v", body["timeout"], tc.wantTimeout)
			}
		})
	}
}

func TestSandboxConnectDebugShortCircuits(t *testing.T) {
	client, err := NewClient(WithAPIKey("e2b_0123"), WithDebug(true), WithHTTPClient(&http.Client{Transport: sbcovFailTransport{t: t}}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	s := &Sandbox{client: client, sandboxID: "sbx_test"}
	got, err := s.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got != s {
		t.Fatal("debug Connect should return the same sandbox")
	}
}

func TestSandboxConnectDelegatesToClient(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/sandboxes/sbx_test/connect" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		return jsonResponse(http.StatusOK, `{"clientID":"c","envdVersion":"0.6.4","sandboxID":"sbx_test","templateID":"base","domain":"example.com"}`, nil), nil
	})
	s := sbcovSandbox(t, transport)
	if _, err := s.Connect(context.Background(), 90); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

func TestSandboxKill(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantKilled bool
	}{
		{name: "ok", status: http.StatusNoContent, wantKilled: true},
		{name: "not found", status: http.StatusNotFound, wantKilled: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodDelete || r.URL.Path != "/sandboxes/sbx_test" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
				}
				return jsonResponse(tc.status, "", nil), nil
			})
			s := sbcovSandbox(t, transport)
			killed, err := s.Kill(context.Background())
			if err != nil {
				t.Fatalf("Kill: %v", err)
			}
			if killed != tc.wantKilled {
				t.Fatalf("killed = %v, want %v", killed, tc.wantKilled)
			}
		})
	}
}

func TestSandboxKillDebugAndError(t *testing.T) {
	debugClient, err := NewClient(WithAPIKey("e2b_0123"), WithDebug(true), WithHTTPClient(&http.Client{Transport: sbcovFailTransport{t: t}}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	killed, err := debugClient.KillSandbox(context.Background(), "sbx_test")
	if err != nil || !killed {
		t.Fatalf("debug KillSandbox = %v %v", killed, err)
	}

	errTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"code":401,"message":"nope"}`, nil), nil
	})
	client := mustTestClient(t, errTransport)
	if _, err := client.KillSandbox(context.Background(), "sbx_test"); err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestSandboxSetTimeout(t *testing.T) {
	var body map[string]int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx_test/timeout" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return jsonResponse(http.StatusNoContent, "", nil), nil
	})
	s := sbcovSandbox(t, transport)
	if err := s.SetTimeout(context.Background(), 45); err != nil {
		t.Fatalf("SetTimeout: %v", err)
	}
	if body["timeout"] != 45 {
		t.Fatalf("timeout = %d", body["timeout"])
	}
}

func TestSandboxSetTimeoutDebugShortCircuits(t *testing.T) {
	client, err := NewClient(WithAPIKey("e2b_0123"), WithDebug(true), WithHTTPClient(&http.Client{Transport: sbcovFailTransport{t: t}}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.SetSandboxTimeout(context.Background(), "sbx_test", 30); err != nil {
		t.Fatalf("SetSandboxTimeout: %v", err)
	}
}

func TestSandboxUpdateNetwork(t *testing.T) {
	var body map[string]json.RawMessage
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut || r.URL.Path != "/sandboxes/sbx_test/network" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return jsonResponse(http.StatusNoContent, "", nil), nil
	})
	s := sbcovSandbox(t, transport)
	allow := true
	if err := s.UpdateNetwork(context.Background(), SandboxNetworkUpdate{AllowInternetAccess: &allow, DenyOut: []string{"1.1.1.1"}}); err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	if string(body["allow_internet_access"]) != "true" {
		t.Fatalf("allow_internet_access = %s", body["allow_internet_access"])
	}
	if string(body["denyOut"]) != `["1.1.1.1"]` {
		t.Fatalf("denyOut = %s", body["denyOut"])
	}
	if _, exists := body["deny_out"]; exists {
		t.Fatalf("unexpected Python field name on the wire: %s", body["deny_out"])
	}
}

func TestSandboxGetInfo(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/sandboxes/sbx_test" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		return jsonResponse(http.StatusOK, `{"sandboxID":"sbx_test","templateID":"base","state":"running","cpuCount":2,"memoryMB":512}`, nil), nil
	})
	s := sbcovSandbox(t, transport)
	info, err := s.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.SandboxID != "sbx_test" || info.State != SandboxStateRunning {
		t.Fatalf("info = %#v", info)
	}
	if info.Metadata == nil {
		t.Fatal("metadata should be non-nil default")
	}
}

func TestSandboxGetInfoNotFound(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, `{"code":404,"message":"gone"}`, nil), nil
	})
	s := sbcovSandbox(t, transport)
	_, err := s.GetInfo(context.Background())
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %T %v, want NotFoundError", err, err)
	}
}

func TestSandboxGetMetrics(t *testing.T) {
	start := time.Unix(1000, 0)
	end := time.Unix(2000, 0)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/sandboxes/sbx_test/metrics" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.URL.Query().Get("start"); got != "1000" {
			t.Fatalf("start = %q", got)
		}
		if got := r.URL.Query().Get("end"); got != "2000" {
			t.Fatalf("end = %q", got)
		}
		return jsonResponse(http.StatusOK, `[{"cpuCount":2,"cpuUsedPct":50.0}]`, nil), nil
	})
	s := sbcovSandbox(t, transport)
	metrics, err := s.GetMetrics(context.Background(), &start, &end)
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
	if len(metrics) != 1 || metrics[0].CPUCount != 2 {
		t.Fatalf("metrics = %#v", metrics)
	}
}

func TestSandboxGetMetricsGating(t *testing.T) {
	// Debug -> nil, nil without any request.
	debugClient, err := NewClient(WithAPIKey("e2b_0123"), WithDebug(true), WithHTTPClient(&http.Client{Transport: sbcovFailTransport{t: t}}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	debugSbx := &Sandbox{client: debugClient, sandboxID: "sbx_test", envdVersion: "0.6.4"}
	metrics, err := debugSbx.GetMetrics(context.Background(), nil, nil)
	if err != nil || metrics != nil {
		t.Fatalf("debug GetMetrics = %#v %v", metrics, err)
	}

	// Old envd version -> TemplateError without any request.
	oldSbx := &Sandbox{client: mustTestClient(t, sbcovFailTransport{t: t}), sandboxID: "sbx_test", envdVersion: "0.1.0"}
	_, err = oldSbx.GetMetrics(context.Background(), nil, nil)
	var tmplErr *TemplateError
	if !errors.As(err, &tmplErr) {
		t.Fatalf("error = %T %v, want TemplateError", err, err)
	}
}

func TestSandboxPause(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantPause bool
	}{
		{name: "paused", status: http.StatusNoContent, wantPause: true},
		{name: "conflict", status: http.StatusConflict, wantPause: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx_test/pause" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
				}
				return jsonResponse(tc.status, "", nil), nil
			})
			s := sbcovSandbox(t, transport)
			paused, err := s.Pause(context.Background())
			if err != nil {
				t.Fatalf("Pause: %v", err)
			}
			if paused != tc.wantPause {
				t.Fatalf("paused = %v, want %v", paused, tc.wantPause)
			}
		})
	}
}

func TestSandboxPauseError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
	})
	s := sbcovSandbox(t, transport)
	if _, err := s.Pause(context.Background()); err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestSandboxCreateSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		snapName string
		wantName bool
	}{
		{name: "with name", snapName: "snap-1", wantName: true},
		{name: "without name", snapName: "", wantName: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]string
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/sbx_test/snapshots" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				return jsonResponse(http.StatusOK, `{"snapshotID":"snap-id","names":["snap-1"]}`, nil), nil
			})
			s := sbcovSandbox(t, transport)
			snapshot, err := s.CreateSnapshot(context.Background(), tc.snapName)
			if err != nil {
				t.Fatalf("CreateSnapshot: %v", err)
			}
			if snapshot.SnapshotID != "snap-id" {
				t.Fatalf("snapshot = %#v", snapshot)
			}
			_, hasName := body["name"]
			if hasName != tc.wantName {
				t.Fatalf("name present = %v, want %v", hasName, tc.wantName)
			}
		})
	}
}

func TestSandboxListSandboxes(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/sandboxes" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		q := r.URL.Query()
		if q.Get("metadata") == "" {
			t.Fatalf("metadata query missing: %s", r.URL.RawQuery)
		}
		if q.Get("state") != "running,paused" {
			t.Fatalf("state query = %q", q.Get("state"))
		}
		if q.Get("limit") != "10" {
			t.Fatalf("limit query = %q", q.Get("limit"))
		}
		if q.Get("nextToken") != "cursor-a" {
			t.Fatalf("nextToken query = %q", q.Get("nextToken"))
		}
		return jsonResponse(http.StatusOK, `[{"sandboxID":"sbx_1","templateID":"base","state":"running"}]`, http.Header{"x-next-token": []string{"cursor-b"}}), nil
	})
	client := mustTestClient(t, transport)
	query := &SandboxQuery{
		Metadata: map[string]string{"team": "core"},
		State:    []SandboxState{SandboxStateRunning, SandboxStatePaused},
	}
	page, err := client.ListSandboxes(context.Background(), query, 10, "cursor-a")
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SandboxID != "sbx_1" {
		t.Fatalf("items = %#v", page.Items)
	}
	if !page.HasNext || page.NextToken != "cursor-b" {
		t.Fatalf("pagination = hasNext=%v next=%q", page.HasNext, page.NextToken)
	}
}

func TestSandboxListSandboxesNilQueryAndNoToken(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.RawQuery != "" {
			t.Fatalf("expected no query, got %q", r.URL.RawQuery)
		}
		return jsonResponse(http.StatusOK, "", nil), nil
	})
	client := mustTestClient(t, transport)
	page, err := client.ListSandboxes(context.Background(), nil, 0, "")
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(page.Items) != 0 || page.HasNext {
		t.Fatalf("page = %#v", page)
	}
}

func TestSandboxListSandboxesErrors(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
		})
		client := mustTestClient(t, transport)
		if _, err := client.ListSandboxes(context.Background(), nil, 0, ""); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{not json}`, nil), nil
		})
		client := mustTestClient(t, transport)
		if _, err := client.ListSandboxes(context.Background(), nil, 0, ""); err == nil {
			t.Fatal("expected unmarshal error")
		}
	})
}

func TestSandboxListSnapshots(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/snapshots" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		q := r.URL.Query()
		if q.Get("sandboxID") != "sbx_test" || q.Get("limit") != "5" || q.Get("nextToken") != "cur" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		return jsonResponse(http.StatusOK, `[{"snapshotID":"snap-1"}]`, http.Header{"x-next-token": []string{"cur-2"}}), nil
	})
	s := sbcovSandbox(t, transport)
	page, err := s.ListSnapshots(context.Background(), 5, "cur")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SnapshotID != "snap-1" {
		t.Fatalf("items = %#v", page.Items)
	}
	if !page.HasNext || page.NextToken != "cur-2" {
		t.Fatalf("pagination = %v %q", page.HasNext, page.NextToken)
	}
}

func TestSandboxListSnapshotsErrors(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
		})
		client := mustTestClient(t, transport)
		if _, err := client.ListSnapshots(context.Background(), "", 0, ""); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{bad}`, nil), nil
		})
		client := mustTestClient(t, transport)
		if _, err := client.ListSnapshots(context.Background(), "", 0, ""); err == nil {
			t.Fatal("expected unmarshal error")
		}
	})
}

func TestSandboxDeleteSnapshot(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		wantDeleted bool
	}{
		{name: "deleted", status: http.StatusNoContent, wantDeleted: true},
		{name: "missing", status: http.StatusNotFound, wantDeleted: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodDelete || r.URL.Path != "/templates/snap-1" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
				}
				return jsonResponse(tc.status, "", nil), nil
			})
			client := mustTestClient(t, transport)
			deleted, err := client.DeleteSnapshot(context.Background(), "snap-1")
			if err != nil {
				t.Fatalf("DeleteSnapshot: %v", err)
			}
			if deleted != tc.wantDeleted {
				t.Fatalf("deleted = %v, want %v", deleted, tc.wantDeleted)
			}
		})
	}
}

func TestSandboxDeleteSnapshotError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"code":401,"message":"no"}`, nil), nil
	})
	client := mustTestClient(t, transport)
	if _, err := client.DeleteSnapshot(context.Background(), "snap-1"); err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestSandboxIsRunning(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantRun bool
	}{
		{name: "healthy", status: http.StatusOK, wantRun: true},
		{name: "bad gateway", status: http.StatusBadGateway, wantRun: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/health" {
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
				if got := r.Header.Get("E2b-Sandbox-Id"); got != "sbx_test" {
					t.Fatalf("sandbox header = %q", got)
				}
				return jsonResponse(tc.status, "", nil), nil
			})
			s := sbcovSandbox(t, transport)
			running, err := s.IsRunning(context.Background())
			if err != nil {
				t.Fatalf("IsRunning: %v", err)
			}
			if running != tc.wantRun {
				t.Fatalf("running = %v, want %v", running, tc.wantRun)
			}
		})
	}
}

func TestSandboxIsRunningError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
	})
	s := sbcovSandbox(t, transport)
	if _, err := s.IsRunning(context.Background()); err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestSandboxAccessors(t *testing.T) {
	s := sbcovSandbox(t, sbcovFailTransport{t: t})
	if s.SandboxDomain() != "example.com" {
		t.Fatalf("SandboxDomain = %q", s.SandboxDomain())
	}
	if s.EnvdVersion() != "0.6.4" {
		t.Fatalf("EnvdVersion = %q", s.EnvdVersion())
	}
	if s.EnvdDirectURL() != "https://direct.test" {
		t.Fatalf("EnvdDirectURL = %q", s.EnvdDirectURL())
	}
	if s.TrafficAccessToken() != "traffic-token" {
		t.Fatalf("TrafficAccessToken = %q", s.TrafficAccessToken())
	}
	if s.SandboxAccessToken() != "envd-token" {
		t.Fatalf("SandboxAccessToken = %q", s.SandboxAccessToken())
	}
	if got := s.GetHost(8080); got != "8080-sbx_test.example.com" {
		t.Fatalf("GetHost = %q", got)
	}
	if got := s.GetMCPURL(); got != "https://50005-sbx_test.example.com/mcp" {
		t.Fatalf("GetMCPURL = %q", got)
	}
}

func TestSandboxNewSandboxDefaultsDomain(t *testing.T) {
	client, err := NewClient(WithAPIKey("e2b_0123"), WithDomain("custom.dev"), WithHTTPClient(&http.Client{Transport: sbcovFailTransport{t: t}}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	s := client.newSandbox(sandboxCreateResponse{SandboxID: "sbx_x", EnvdVersion: "0.6.4"})
	if s.SandboxDomain() != "custom.dev" {
		t.Fatalf("domain = %q, want fallback to config domain", s.SandboxDomain())
	}
}

func TestSandboxFileURL(t *testing.T) {
	t.Run("expiration without secure", func(t *testing.T) {
		s := &Sandbox{envdVersion: "0.6.4", envdDirectURL: "https://direct.test"}
		exp := 60
		if _, err := s.DownloadURL("/f", nil, &exp); err == nil {
			t.Fatal("expected InvalidArgumentError")
		} else {
			var invalid *InvalidArgumentError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %T %v", err, err)
			}
		}
	})

	t.Run("no token unsecured plain", func(t *testing.T) {
		s := &Sandbox{envdVersion: "0.6.4", envdDirectURL: "https://direct.test"}
		got, err := s.DownloadURL("/f.txt", nil, nil)
		if err != nil {
			t.Fatalf("DownloadURL: %v", err)
		}
		if !strings.HasPrefix(got, "https://direct.test/files?") || !strings.Contains(got, "path=%2Ff.txt") {
			t.Fatalf("url = %q", got)
		}
		if strings.Contains(got, "signature") {
			t.Fatalf("unexpected signature in %q", got)
		}
	})

	t.Run("legacy version default user", func(t *testing.T) {
		s := &Sandbox{envdVersion: "0.3.0", envdAccessToken: "tok", envdDirectURL: "https://direct.test"}
		got, err := s.UploadURL("/f", nil, nil)
		if err != nil {
			t.Fatalf("UploadURL: %v", err)
		}
		if !strings.Contains(got, "username=user") {
			t.Fatalf("expected default username, got %q", got)
		}
		if !strings.Contains(got, "signature=v1_") {
			t.Fatalf("expected signature, got %q", got)
		}
	})

	t.Run("secure with user and expiration", func(t *testing.T) {
		s := &Sandbox{envdVersion: "0.6.4", envdAccessToken: "tok", envdDirectURL: "https://direct.test"}
		user := "root"
		exp := 120
		got, err := s.DownloadURL("/data", &user, &exp)
		if err != nil {
			t.Fatalf("DownloadURL: %v", err)
		}
		if !strings.Contains(got, "username=root") {
			t.Fatalf("expected username=root, got %q", got)
		}
		if !strings.Contains(got, "signature_expiration=") {
			t.Fatalf("expected signature_expiration, got %q", got)
		}
	})
}

func TestSandboxNonNilStringMapAndShellQuote(t *testing.T) {
	if got := nonNilStringMap(nil); got == nil || len(got) != 0 {
		t.Fatalf("nonNilStringMap(nil) = %#v", got)
	}
	// shellQuoteJSON error branch: channels cannot be JSON-marshalled.
	if got := shellQuoteJSON(make(chan int)); got != "''" {
		t.Fatalf("shellQuoteJSON(chan) = %q, want ''", got)
	}
	if got := shellQuoteJSON(map[string]string{"a": "b'c"}); !strings.Contains(got, `'"'"'`) {
		t.Fatalf("shellQuoteJSON quoting = %q", got)
	}
}
