package e2b

import (
	"bytes"
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

func TestSandboxNetworkJSONMatchesControlPlane(t *testing.T) {
	rules := SandboxNetworkRules{
		"api.example.com": {
			{
				Transform: &SandboxNetworkTransform{
					Headers: map[string]string{
						"Authorization": "Bearer test-token",
						"X-Tenant":      "tenant-a",
						"fc.sandbox.network.header-value-replacements": `[{"placeholder":"sbx-key-0001","value":"real-secret-value"}]`,
					},
				},
			},
		},
	}

	createBody, err := json.Marshal(SandboxNetworkOpts{
		AllowOut: []string{"api.example.com"},
		DenyOut:  []string{AllTraffic},
		Rules:    rules,
	})
	if err != nil {
		t.Fatalf("marshal create network: %v", err)
	}
	wantCreate := `{"allowOut":["api.example.com"],"denyOut":["0.0.0.0/0"],"rules":{"api.example.com":[{"transform":{"headers":{"Authorization":"Bearer test-token","X-Tenant":"tenant-a","fc.sandbox.network.header-value-replacements":"[{\"placeholder\":\"sbx-key-0001\",\"value\":\"real-secret-value\"}]"}}}]}}`
	if string(createBody) != wantCreate {
		t.Fatalf("create network JSON = %s, want %s", createBody, wantCreate)
	}

	updateBody, err := json.Marshal(SandboxNetworkUpdate{
		AllowOut: []string{"api.example.com"},
		DenyOut:  []string{AllTraffic},
		Rules:    rules,
	})
	if err != nil {
		t.Fatalf("marshal update network: %v", err)
	}
	wantUpdate := `{"allowOut":["api.example.com"],"denyOut":["0.0.0.0/0"],"rules":{"api.example.com":[{"transform":{"headers":{"Authorization":"Bearer test-token","X-Tenant":"tenant-a","fc.sandbox.network.header-value-replacements":"[{\"placeholder\":\"sbx-key-0001\",\"value\":\"real-secret-value\"}]"}}}]}}`
	if string(updateBody) != wantUpdate {
		t.Fatalf("update network JSON = %s, want %s", updateBody, wantUpdate)
	}
}

func TestSandboxNetworkJSONUsesDefaultOmitEmptySemantics(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "create omitted rules", value: SandboxNetworkOpts{}, want: `{}`},
		{name: "create empty rules", value: SandboxNetworkOpts{Rules: SandboxNetworkRules{}}, want: `{}`},
		{name: "create empty allow out", value: SandboxNetworkOpts{AllowOut: []string{}}, want: `{}`},
		{name: "update omitted rules", value: SandboxNetworkUpdate{DenyOut: []string{"1.1.1.1"}}, want: `{"denyOut":["1.1.1.1"]}`},
		{name: "update empty rules", value: SandboxNetworkUpdate{Rules: SandboxNetworkRules{}}, want: `{}`},
		{name: "update empty egress", value: SandboxNetworkUpdate{AllowOut: []string{}, DenyOut: []string{}}, want: `{}`},
		{name: "empty update", value: SandboxNetworkUpdate{}, want: `{}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("JSON = %s, want %s", got, test.want)
			}
		})
	}
}

func TestSandboxNetworkInfoDecodesRules(t *testing.T) {
	var info SandboxNetworkInfo
	err := json.Unmarshal([]byte(`{
		"allowOut":["api.example.com"],
		"denyOut":["0.0.0.0/0"],
		"rules":{"api.example.com":[{"transform":{"headers":{"Authorization":"Bearer test-token","fc.sandbox.network.header-value-replacements":"[{\"placeholder\":\"sbx-key-0001\",\"value\":\"real-secret-value\"}]"}}}]}
	}`), &info)
	if err != nil {
		t.Fatalf("unmarshal sandbox network info: %v", err)
	}
	if len(info.AllowOut) != 1 || info.AllowOut[0] != "api.example.com" {
		t.Fatalf("allowOut = %#v", info.AllowOut)
	}
	if len(info.DenyOut) != 1 || info.DenyOut[0] != AllTraffic {
		t.Fatalf("denyOut = %#v", info.DenyOut)
	}
	rules := info.Rules["api.example.com"]
	if len(rules) != 1 || rules[0].Transform == nil || rules[0].Transform.Headers["Authorization"] != "Bearer test-token" {
		t.Fatalf("rules = %#v", info.Rules)
	}
	if got := rules[0].Transform.Headers["fc.sandbox.network.header-value-replacements"]; got != `[{"placeholder":"sbx-key-0001","value":"real-secret-value"}]` {
		t.Fatalf("header value replacements carrier = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCreateSandboxStartsMCPGatewayInBackground(t *testing.T) {
	var deleted bool
	var timeoutHeader string
	var gatewayCommand string
	var gatewayToken string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			return jsonResponse(http.StatusCreated, `{"clientID":"client","envdVersion":"0.6.4","sandboxID":"sbx_mcp","templateID":"mcp-gateway","domain":"example.com","envdAccessToken":"envd-token","trafficAccessToken":"traffic-token"}`, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/process.Process/Start":
			timeoutHeader = r.Header.Get("Connect-Timeout-Ms")
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read command request: %v", err)
			}
			if len(raw) < 5 {
				t.Fatalf("command request too short: %d", len(raw))
			}
			var request struct {
				Process struct {
					Args []string          `json:"args"`
					Envs map[string]string `json:"envs"`
				} `json:"process"`
			}
			if err := json.Unmarshal(raw[5:], &request); err != nil {
				t.Fatalf("decode command request: %v", err)
			}
			if len(request.Process.Args) != 3 {
				t.Fatalf("process args = %#v", request.Process.Args)
			}
			gatewayCommand = request.Process.Args[2]
			gatewayToken = request.Process.Envs["GATEWAY_ACCESS_TOKEN"]
			body := bytes.Join([][]byte{
				testConnectEnvelope(t, "{\"event\":{\"start\":{\"pid\":42}}}"),
				testConnectEnvelope(t, "{\"event\":{\"end\":{\"exitCode\":1,\"error\":\"gateway failed\"}}}"),
			}, nil)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/connect+json"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/sbx_mcp":
			deleted = true
			return jsonResponse(http.StatusNoContent, "", nil), nil
		default:
			t.Fatalf("unexpected request: %s %s host=%s", r.Method, r.URL.RequestURI(), r.URL.Host)
			return nil, nil
		}
	})
	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sandbox, err := client.CreateSandbox(context.Background(), WithMCP(map[string]any{"server": "stdio"}))
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if deleted {
		t.Fatal("gateway was treated as a foreground failure and deleted the sandbox")
	}
	if timeoutHeader != "" {
		t.Fatalf("Connect-Timeout-Ms = %q, want no command timeout", timeoutHeader)
	}
	if !strings.Contains(gatewayCommand, "mcp-gateway --config") {
		t.Fatalf("gateway command = %q", gatewayCommand)
	}
	if gatewayToken == "" || sandbox.MCPToken() != gatewayToken {
		t.Fatalf("gateway token env = %q sandbox token = %q", gatewayToken, sandbox.MCPToken())
	}
}
