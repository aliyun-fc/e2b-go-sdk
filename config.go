package e2b

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvdPort is the default envd port exposed by E2B sandboxes.
	EnvdPort = 49983

	defaultRequestTimeout = 60 * time.Second

	keepalivePingHeader      = "Keepalive-Ping-Interval"
	keepalivePingIntervalSec = 50
)

var apiKeyPattern = regexp.MustCompile(`\Ae2b_[0-9a-f]+\z`)

// Config contains all connection settings used by the SDK.
type Config struct {
	Domain             string
	Debug              bool
	APIKey             string
	ValidateAPIKey     bool
	APIURL             string
	SandboxURLOverride string
	AccessToken        string
	Integration        string
	Headers            map[string]string
	SandboxHeaders     map[string]string
	RequestTimeout     time.Duration
	HTTPClient         *http.Client
}

// Option configures a Client.
type Option func(*Config)

// NewConfig returns SDK configuration using Python SDK-compatible environment defaults.
func NewConfig(opts ...Option) Config {
	cfg := Config{
		Domain:         getenvDefault("E2B_DOMAIN", defaultDomain),
		Debug:          strings.EqualFold(os.Getenv("E2B_DEBUG"), "true"),
		APIKey:         os.Getenv("E2B_API_KEY"),
		ValidateAPIKey: !strings.EqualFold(os.Getenv("E2B_VALIDATE_API_KEY"), "false"),
		AccessToken:    os.Getenv("E2B_ACCESS_TOKEN"),
		Headers:        map[string]string{},
		SandboxHeaders: map[string]string{},
		RequestTimeout: defaultRequestTimeout,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if cfg.APIURL == "" {
		if env := os.Getenv("E2B_API_URL"); env != "" {
			cfg.APIURL = env
		} else if cfg.Debug {
			cfg.APIURL = "http://localhost:3000"
		} else {
			cfg.APIURL = "https://api." + cfg.Domain
		}
	}
	if cfg.SandboxURLOverride == "" {
		cfg.SandboxURLOverride = os.Getenv("E2B_SANDBOX_URL")
	}

	cfg.Headers = cloneHeaders(cfg.Headers)
	cfg.Headers["User-Agent"] = buildUserAgent(cfg.Integration)
	for k, v := range defaultAPIHeaders() {
		if _, ok := cfg.Headers[k]; !ok {
			cfg.Headers[k] = v
		}
	}
	cfg.SandboxHeaders = cloneHeaders(cfg.SandboxHeaders)
	cfg.SandboxHeaders["User-Agent"] = cfg.Headers["User-Agent"]

	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return cfg
}

// WithAPIKey sets the E2B API key.
func WithAPIKey(apiKey string) Option {
	return func(c *Config) { c.APIKey = apiKey }
}

// WithValidateAPIKey controls client-side API key format validation.
func WithValidateAPIKey(validate bool) Option {
	return func(c *Config) { c.ValidateAPIKey = validate }
}

// WithDomain sets the E2B domain, defaulting to e2b.app.
func WithDomain(domain string) Option {
	return func(c *Config) { c.Domain = domain }
}

// WithDebug enables local debug URLs, matching E2B_DEBUG=true in the Python SDK.
func WithDebug(debug bool) Option {
	return func(c *Config) { c.Debug = debug }
}

// WithAPIURL sets the control-plane API base URL.
func WithAPIURL(apiURL string) Option {
	return func(c *Config) { c.APIURL = strings.TrimRight(apiURL, "/") }
}

// WithSandboxURL overrides all sandbox data-plane URLs.
func WithSandboxURL(sandboxURL string) Option {
	return func(c *Config) { c.SandboxURLOverride = strings.TrimRight(sandboxURL, "/") }
}

// WithAccessToken sets the deprecated API Authorization bearer token.
func WithAccessToken(token string) Option {
	return func(c *Config) { c.AccessToken = token }
}

// WithIntegration appends an integration marker to the User-Agent.
func WithIntegration(integration string) Option {
	return func(c *Config) { c.Integration = integration }
}

// WithHeader sets a control-plane API header.
func WithHeader(key, value string) Option {
	return func(c *Config) {
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		c.Headers[key] = value
	}
}

// WithHeaders merges control-plane API headers.
func WithHeaders(headers map[string]string) Option {
	return func(c *Config) {
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		for k, v := range headers {
			c.Headers[k] = v
		}
	}
}

// WithSandboxHeader sets a data-plane envd header.
func WithSandboxHeader(key, value string) Option {
	return func(c *Config) {
		if c.SandboxHeaders == nil {
			c.SandboxHeaders = map[string]string{}
		}
		c.SandboxHeaders[key] = value
	}
}

// WithRequestTimeout sets the per-request timeout. Passing 0 disables it.
func WithRequestTimeout(timeout time.Duration) Option {
	return func(c *Config) { c.RequestTimeout = timeout }
}

// WithHTTPClient sets the HTTP client used for control-plane and data-plane requests.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Config) {
		if client != nil {
			c.HTTPClient = client
		}
	}
}

// ValidateAPIKey validates an E2B API key using the Python SDK-compatible format.
func ValidateAPIKey(apiKey string) error {
	if !apiKeyPattern.MatchString(apiKey) {
		return &AuthenticationError{Message: `Invalid API key format: expected "e2b_" followed by hex characters (e.g. "e2b_0000000000000000000000000000000000000000"). Visit the API Keys tab at https://e2b.dev/dashboard?tab=keys to get your API key.`}
	}
	return nil
}

func (c Config) apiURL() string {
	return strings.TrimRight(c.APIURL, "/")
}

func (c Config) sandboxURL(sandboxID, sandboxDomain string) string {
	if c.SandboxURLOverride != "" {
		return strings.TrimRight(c.SandboxURLOverride, "/")
	}
	if c.Debug {
		return "http://" + c.host(sandboxID, sandboxDomain, EnvdPort)
	}
	if sandboxDomain == "" {
		sandboxDomain = c.Domain
	}
	if isSupportedSandboxDomain(sandboxDomain) {
		return "https://sandbox." + sandboxDomain
	}
	return "https://" + c.host(sandboxID, sandboxDomain, EnvdPort)
}

func (c Config) sandboxDirectURL(sandboxID, sandboxDomain string) string {
	if c.SandboxURLOverride != "" {
		return strings.TrimRight(c.SandboxURLOverride, "/")
	}
	if c.Debug {
		return "http://" + c.host(sandboxID, sandboxDomain, EnvdPort)
	}
	return "https://" + c.host(sandboxID, sandboxDomain, EnvdPort)
}

func (c Config) host(sandboxID, sandboxDomain string, port int) string {
	if c.Debug {
		return "localhost:" + strconv.Itoa(port)
	}
	if sandboxDomain == "" {
		sandboxDomain = c.Domain
	}
	return fmt.Sprintf("%d-%s.%s", port, sandboxID, sandboxDomain)
}

func (c Config) requestContextTimeout(override time.Duration) time.Duration {
	if override != 0 {
		return override
	}
	return c.RequestTimeout
}

// resolveTimeout resolves an optional per-call timeout: nil falls back to the
// configured RequestTimeout, while a non-nil value is used verbatim — including
// an explicit 0, which disables the timeout (mirroring Python's
// request_timeout=0 -> None).
func (c Config) resolveTimeout(override *time.Duration) time.Duration {
	if override != nil {
		return *override
	}
	return c.RequestTimeout
}

func authenticationHeader(envdVersion string, user *string) map[string]string {
	username := ""
	if user != nil {
		username = *user
	} else if compareVersion(envdVersion, "0.4.0") < 0 {
		username = "user"
	}
	if username == "" {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":"))
	return map[string]string{"Authorization": "Basic " + encoded}
}

func buildUserAgent(integration string) string {
	parts := []string{"e2b-go-sdk/" + Version}
	if integration != "" {
		parts = append(parts, integration)
	}
	return strings.Join(parts, " ")
}

func defaultAPIHeaders() map[string]string {
	return map[string]string{
		"lang":            "go",
		"lang_version":    runtime.Version(),
		"package_version": Version,
		"publisher":       "e2b",
		"sdk_runtime":     "go",
		"system":          runtime.GOOS,
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	result := map[string]string{}
	for k, v := range headers {
		result[k] = v
	}
	return result
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func isSupportedSandboxDomain(domain string) bool {
	switch domain {
	case "e2b.app", "e2b.dev", "e2b.pro", "e2b-staging.dev":
		return true
	default:
		return false
	}
}
