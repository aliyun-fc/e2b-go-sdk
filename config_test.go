package e2b

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestValidateAPIKey(t *testing.T) {
	t.Run("accepts e2b hex key", func(t *testing.T) {
		if err := ValidateAPIKey("e2b_" + "0123456789abcdef"); err != nil {
			t.Fatalf("expected key to pass validation: %v", err)
		}
	})

	t.Run("rejects missing prefix", func(t *testing.T) {
		var auth *AuthenticationError
		err := ValidateAPIKey("sk_" + "0123456789abcdef")
		if !errors.As(err, &auth) {
			t.Fatalf("expected AuthenticationError, got %T: %v", err, err)
		}
	})

	t.Run("rejects empty body", func(t *testing.T) {
		if err := ValidateAPIKey("e2b_"); err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("rejects non hex body", func(t *testing.T) {
		if err := ValidateAPIKey("e2b_" + "z"); err == nil {
			t.Fatal("expected validation error")
		}
	})
}

func TestNewClientRequiresAndValidatesAPIKey(t *testing.T) {
	t.Setenv("E2B_API_KEY", "")
	_, err := NewClient()
	var auth *AuthenticationError
	if !errors.As(err, &auth) {
		t.Fatalf("expected missing key AuthenticationError, got %T: %v", err, err)
	}

	_, err = NewClient(WithAPIKey("not-a-valid-key"))
	if !errors.As(err, &auth) {
		t.Fatalf("expected invalid key AuthenticationError, got %T: %v", err, err)
	}

	client, err := NewClient(WithAPIKey("not-a-valid-key"), WithValidateAPIKey(false))
	if err != nil {
		t.Fatalf("expected disabled validation to pass: %v", err)
	}
	if client.config.APIKey != "not-a-valid-key" {
		t.Fatalf("unexpected api key: %q", client.config.APIKey)
	}
}

func TestConfigDefaultsAndURLs(t *testing.T) {
	t.Setenv("E2B_DOMAIN", "")
	t.Setenv("E2B_DEBUG", "")
	t.Setenv("E2B_API_URL", "")
	t.Setenv("E2B_SANDBOX_URL", "")

	cfg := NewConfig(WithAPIKey("e2b_0123"), WithIntegration("wrapper/1.0"))
	if cfg.Domain != "e2b.app" {
		t.Fatalf("domain = %q", cfg.Domain)
	}
	if cfg.APIURL != "https://api.e2b.app" {
		t.Fatalf("api url = %q", cfg.APIURL)
	}
	if cfg.sandboxURL("sbx", "e2b.app") != "https://sandbox.e2b.app" {
		t.Fatalf("sandbox url = %q", cfg.sandboxURL("sbx", "e2b.app"))
	}
	if cfg.sandboxDirectURL("sbx", "e2b.app") != "https://49983-sbx.e2b.app" {
		t.Fatalf("direct url = %q", cfg.sandboxDirectURL("sbx", "e2b.app"))
	}
	if got := cfg.host("sbx", "example.com", 3000); got != "3000-sbx.example.com" {
		t.Fatalf("host = %q", got)
	}
	if cfg.Headers["User-Agent"] != "e2b-go-sdk/"+Version+" wrapper/1.0" {
		t.Fatalf("user agent = %q", cfg.Headers["User-Agent"])
	}

	debug := NewConfig(WithDebug(true), WithRequestTimeout(0))
	if debug.APIURL != "http://localhost:3000" {
		t.Fatalf("debug api url = %q", debug.APIURL)
	}
	if debug.sandboxURL("sbx", "e2b.app") != "http://localhost:49983" {
		t.Fatalf("debug sandbox url = %q", debug.sandboxURL("sbx", "e2b.app"))
	}
	if debug.RequestTimeout != 0 {
		t.Fatalf("request timeout = %s", debug.RequestTimeout)
	}

	customTimeout := NewConfig(WithRequestTimeout(2 * time.Second))
	if customTimeout.RequestTimeout != 2*time.Second {
		t.Fatalf("request timeout = %s", customTimeout.RequestTimeout)
	}
}

// TestConfigCustomHeaderOverridesCaseInsensitiveDefault verifies that a custom
// header replaces a default header that differs only by casing, leaving a single
// entry for that name.
func TestConfigCustomHeaderOverridesCaseInsensitiveDefault(t *testing.T) {
	cfg := NewConfig(WithHeader("lang", "custom"))

	if got := cfg.Headers["lang"]; got != "custom" {
		t.Fatalf("lang header = %q, want custom", got)
	}
	count := 0
	for key := range cfg.Headers {
		if strings.EqualFold(key, "lang") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("case-insensitive Lang header count = %d, headers = %#v", count, cfg.Headers)
	}
}

// TestConfigHeadersPreservePublicKeyRepresentation verifies that custom header
// keys keep the exact casing the caller supplied (no duplicate default key is
// added) while the default headers remain unchanged.
func TestConfigHeadersPreservePublicKeyRepresentation(t *testing.T) {
	cfg := NewConfig(
		WithHeader("Lang", "custom"),
		WithHeader("x-foo", "control"),
		WithSandboxHeader("x-sandbox-foo", "sandbox"),
	)

	if got := cfg.Headers["Lang"]; got != "custom" {
		t.Fatalf("Lang header = %q, want custom", got)
	}
	if _, ok := cfg.Headers["lang"]; ok {
		t.Fatalf("default lang key was added alongside custom Lang: %#v", cfg.Headers)
	}
	if got := cfg.Headers["x-foo"]; got != "control" {
		t.Fatalf("x-foo header = %q, want control", got)
	}
	if got := cfg.SandboxHeaders["x-sandbox-foo"]; got != "sandbox" {
		t.Fatalf("x-sandbox-foo header = %q, want sandbox", got)
	}

	defaults := NewConfig()
	if got := defaults.Headers["lang"]; got != "go" {
		t.Fatalf("default lang header = %q, want go", got)
	}
}

// TestClientRequestCanonicalizesHeadersWithoutChangingPublicConfig verifies that
// outgoing requests send canonicalized header names while the public Config keeps
// the caller's original key representation.
func TestClientRequestCanonicalizesHeadersWithoutChangingPublicConfig(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if values := r.Header.Values("Lang"); len(values) != 1 || values[0] != "custom" {
			t.Fatalf("request Lang headers = %#v, want [custom]", values)
		}
		return jsonResponse(http.StatusOK, ``, nil), nil
	})
	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHeader("lang", "custom"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := client.doJSON(context.Background(), http.MethodGet, "/probe", nil, nil, nil); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	if got := client.Config().Headers["lang"]; got != "custom" {
		t.Fatalf("public config lang header = %q, want custom", got)
	}
}
