package e2b

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCreateSandboxRequestAndResponse(t *testing.T) {
	var received map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-KEY"); got != "e2b_0123" {
			t.Fatalf("X-API-KEY = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
			"clientID":"client",
			"envdVersion":"0.6.4",
			"sandboxID":"sbx_123",
			"templateID":"tmpl",
			"domain":"example.com",
			"envdAccessToken":"envd-token",
			"trafficAccessToken":"traffic-token"
		}`)),
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
	lifecycle := SandboxLifecycle{OnTimeout: "pause", AutoResume: true}
	sandbox, err := client.CreateSandbox(
		context.Background(),
		WithTemplate("python"),
		WithTimeout(600),
		WithMetadata(map[string]string{"k": "v"}),
		WithEnv("A", "B"),
		WithSecure(false),
		WithInternetAccess(false),
		WithLifecycle(lifecycle),
		WithVolumeMount("/data", "vol"),
	)
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}

	if received["templateID"] != "python" {
		t.Fatalf("templateID = %#v", received["templateID"])
	}
	if received["timeout"].(float64) != 600 {
		t.Fatalf("timeout = %#v", received["timeout"])
	}
	if received["secure"].(bool) {
		t.Fatal("secure should be false")
	}
	if received["allow_internet_access"].(bool) {
		t.Fatal("allow_internet_access should be false")
	}
	if !received["autoPause"].(bool) {
		t.Fatal("autoPause should be true")
	}
	autoResume := received["autoResume"].(map[string]any)
	if !autoResume["enabled"].(bool) {
		t.Fatal("autoResume.enabled should be true")
	}
	envs := received["envVars"].(map[string]any)
	if envs["A"] != "B" {
		t.Fatalf("env var A = %#v", envs["A"])
	}

	if sandbox.SandboxID() != "sbx_123" {
		t.Fatalf("sandbox id = %q", sandbox.SandboxID())
	}
	if sandbox.EnvdAPIURL() != "https://49983-sbx_123.example.com" {
		t.Fatalf("envd api url = %q", sandbox.EnvdAPIURL())
	}
	if got := sandbox.sandboxHeaders(nil)["X-Access-Token"]; got != "envd-token" {
		t.Fatalf("X-Access-Token = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
