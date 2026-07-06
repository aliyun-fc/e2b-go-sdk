package e2b

import (
	"strings"
	"testing"
)

func TestMcovOptionSetters(t *testing.T) {
	cfg := NewConfig(
		WithAPIKey("e2b_dead"),
		WithDomain("example.dev"),
		WithSandboxURL("https://sbx.override/"),
		WithAccessToken("tok-123"),
		WithHeader("X-Custom", "one"),
		WithHeaders(map[string]string{"X-Extra": "two", "X-Custom": "override"}),
		WithSandboxHeader("X-Sbx", "three"),
	)

	if cfg.Domain != "example.dev" {
		t.Fatalf("Domain = %q", cfg.Domain)
	}
	// WithSandboxURL trims a trailing slash.
	if cfg.SandboxURLOverride != "https://sbx.override" {
		t.Fatalf("SandboxURLOverride = %q", cfg.SandboxURLOverride)
	}
	if cfg.AccessToken != "tok-123" {
		t.Fatalf("AccessToken = %q", cfg.AccessToken)
	}
	if cfg.Headers["X-Custom"] != "override" {
		t.Fatalf("X-Custom header = %q", cfg.Headers["X-Custom"])
	}
	if cfg.Headers["X-Extra"] != "two" {
		t.Fatalf("X-Extra header = %q", cfg.Headers["X-Extra"])
	}
	if cfg.SandboxHeaders["X-Sbx"] != "three" {
		t.Fatalf("X-Sbx header = %q", cfg.SandboxHeaders["X-Sbx"])
	}
}

func TestMcovOptionSettersOnNilMaps(t *testing.T) {
	// The WithHeader/WithHeaders/WithSandboxHeader options must lazily allocate
	// their target maps when the config starts with nil maps.
	cfg := Config{}
	WithHeader("A", "1")(&cfg)
	WithHeaders(map[string]string{"B": "2"})(&cfg)
	WithSandboxHeader("C", "3")(&cfg)

	if cfg.Headers["A"] != "1" || cfg.Headers["B"] != "2" {
		t.Fatalf("headers = %#v", cfg.Headers)
	}
	if cfg.SandboxHeaders["C"] != "3" {
		t.Fatalf("sandbox headers = %#v", cfg.SandboxHeaders)
	}
}

func TestMcovNewConfigEnvBranches(t *testing.T) {
	t.Run("api url and sandbox url from env", func(t *testing.T) {
		t.Setenv("E2B_API_URL", "https://api.env/")
		t.Setenv("E2B_SANDBOX_URL", "https://sbx.env")
		t.Setenv("E2B_DOMAIN", "domain.env")

		cfg := NewConfig(WithAPIKey("e2b_abc"))
		if cfg.APIURL != "https://api.env/" {
			t.Fatalf("APIURL = %q", cfg.APIURL)
		}
		// apiURL() trims the trailing slash even though APIURL keeps it.
		if cfg.apiURL() != "https://api.env" {
			t.Fatalf("apiURL() = %q", cfg.apiURL())
		}
		if cfg.SandboxURLOverride != "https://sbx.env" {
			t.Fatalf("SandboxURLOverride = %q", cfg.SandboxURLOverride)
		}
		if cfg.Domain != "domain.env" {
			t.Fatalf("Domain = %q", cfg.Domain)
		}
	})

	t.Run("production api url derives from domain", func(t *testing.T) {
		t.Setenv("E2B_API_URL", "")
		t.Setenv("E2B_SANDBOX_URL", "")
		t.Setenv("E2B_DEBUG", "")
		t.Setenv("E2B_DOMAIN", "")

		cfg := NewConfig(WithAPIKey("e2b_abc"), WithDomain("custom.io"))
		if cfg.APIURL != "https://api.custom.io" {
			t.Fatalf("APIURL = %q", cfg.APIURL)
		}
	})
}

func TestMcovSandboxURLVariants(t *testing.T) {
	t.Run("override wins", func(t *testing.T) {
		cfg := Config{SandboxURLOverride: "https://custom.sbx/"}
		if got := cfg.sandboxURL("sbx", "e2b.app"); got != "https://custom.sbx" {
			t.Fatalf("sandboxURL = %q", got)
		}
		if got := cfg.sandboxDirectURL("sbx", "e2b.app"); got != "https://custom.sbx" {
			t.Fatalf("sandboxDirectURL = %q", got)
		}
	})

	t.Run("unsupported domain falls back to host", func(t *testing.T) {
		cfg := Config{Domain: "e2b.app"}
		got := cfg.sandboxURL("sbx", "unknown.example")
		if got != "https://49983-sbx.unknown.example" {
			t.Fatalf("sandboxURL = %q", got)
		}
	})

	t.Run("empty domain falls back to config domain", func(t *testing.T) {
		cfg := Config{Domain: "e2b.dev"}
		if got := cfg.sandboxURL("sbx", ""); got != "https://sandbox.e2b.dev" {
			t.Fatalf("sandboxURL = %q", got)
		}
	})

	t.Run("debug uses localhost", func(t *testing.T) {
		cfg := Config{Debug: true}
		if got := cfg.sandboxURL("sbx", "e2b.app"); got != "http://localhost:49983" {
			t.Fatalf("sandboxURL = %q", got)
		}
		if got := cfg.sandboxDirectURL("sbx", "e2b.app"); got != "http://localhost:49983" {
			t.Fatalf("sandboxDirectURL = %q", got)
		}
		if got := cfg.host("sbx", "e2b.app", 3000); got != "localhost:3000" {
			t.Fatalf("host = %q", got)
		}
	})

	t.Run("host with empty domain uses config domain", func(t *testing.T) {
		cfg := Config{Domain: "fallback.io"}
		if got := cfg.host("sbx", "", 1234); got != "1234-sbx.fallback.io" {
			t.Fatalf("host = %q", got)
		}
	})
}

func TestMcovGetenvDefault(t *testing.T) {
	t.Run("uses env when present", func(t *testing.T) {
		t.Setenv("MCOV_ENV_KEY", "present")
		if got := getenvDefault("MCOV_ENV_KEY", "fallback"); got != "present" {
			t.Fatalf("getenvDefault = %q", got)
		}
	})

	t.Run("uses fallback when empty", func(t *testing.T) {
		t.Setenv("MCOV_ENV_KEY", "")
		if got := getenvDefault("MCOV_ENV_KEY", "fallback"); got != "fallback" {
			t.Fatalf("getenvDefault = %q", got)
		}
	})
}

func TestMcovAuthenticationHeader(t *testing.T) {
	t.Run("explicit user", func(t *testing.T) {
		user := "root"
		got := authenticationHeader("0.5.0", &user)
		if !strings.HasPrefix(got["Authorization"], "Basic ") {
			t.Fatalf("Authorization = %q", got["Authorization"])
		}
	})

	t.Run("legacy version defaults to user", func(t *testing.T) {
		got := authenticationHeader("0.3.0", nil)
		if !strings.HasPrefix(got["Authorization"], "Basic ") {
			t.Fatalf("Authorization = %q", got["Authorization"])
		}
	})

	t.Run("modern version with nil user yields no header", func(t *testing.T) {
		if got := authenticationHeader("0.5.0", nil); got != nil {
			t.Fatalf("expected nil header, got %#v", got)
		}
	})

	t.Run("empty user yields no header", func(t *testing.T) {
		empty := ""
		if got := authenticationHeader("0.3.0", &empty); got != nil {
			t.Fatalf("expected nil header, got %#v", got)
		}
	})
}
