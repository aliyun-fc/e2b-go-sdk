package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the entry point for E2B control-plane operations.
type Client struct {
	config Config
	http   *http.Client
}

// NewClient creates a client using Python SDK-compatible configuration defaults.
func NewClient(opts ...Option) (*Client, error) {
	cfg := NewConfig(opts...)
	if cfg.APIKey == "" {
		return nil, &AuthenticationError{Message: "API key is required, please visit the API Keys tab at https://e2b.dev/dashboard?tab=keys to get your API key. You can either set the environment variable E2B_API_KEY or pass it with WithAPIKey."}
	}
	if cfg.ValidateAPIKey {
		if err := ValidateAPIKey(cfg.APIKey); err != nil {
			return nil, err
		}
	}
	return &Client{config: cfg, http: cfg.HTTPClient}, nil
}

// Config returns a copy of the client's configuration. The Headers and
// SandboxHeaders maps are deep-copied so callers cannot mutate client state.
func (c *Client) Config() Config {
	cfg := c.config
	cfg.Headers = cloneStringMap(c.config.Headers)
	cfg.SandboxHeaders = cloneStringMap(c.config.SandboxHeaders)
	return cfg
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any, out any, expected ...int) error {
	return c.doJSONWithHeaders(ctx, method, path, query, body, out, nil, expected...)
}

func (c *Client) doJSONWithHeaders(ctx context.Context, method, path string, query url.Values, body any, out any, headers map[string]string, expected ...int) error {
	requestHeaders := cloneHeaders(c.config.Headers)
	for key, value := range headers {
		requestHeaders[key] = value
	}
	status, payload, err := c.do(ctx, method, c.config.apiURL(), path, query, body, requestHeaders, expected...)
	if err != nil {
		return err
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	_ = status
	return nil
}

func (c *Client) do(ctx context.Context, method, baseURL, path string, query url.Values, body any, headers map[string]string, expected ...int) (int, []byte, error) {
	status, payload, _, err := c.doFull(ctx, method, baseURL, path, query, body, headers, expected...)
	return status, payload, err
}

func (c *Client) doFull(ctx context.Context, method, baseURL, path string, query url.Values, body any, headers map[string]string, expected ...int) (int, []byte, http.Header, error) {
	var requestBody io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return 0, nil, nil, err
		}
		requestBody = buf
	}

	target, err := url.JoinPath(strings.TrimRight(baseURL, "/"), strings.TrimPrefix(path, "/"))
	if err != nil {
		return 0, nil, nil, err
	}
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	ctx, cancel := withTimeout(ctx, c.config.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, target, requestBody)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if baseURL == c.config.apiURL() {
		req.Header.Set("X-API-KEY", c.config.APIKey)
		if c.config.AccessToken != "" && req.Header.Get("Authorization") == "" {
			req.Header.Set("Authorization", "Bearer "+c.config.AccessToken)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, formatRequestTimeout()
		}
		return 0, nil, nil, err
	}
	defer res.Body.Close()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, payload, res.Header, err
	}

	if !statusExpected(res.StatusCode, expected) {
		return res.StatusCode, payload, res.Header, parseAPIError(res.StatusCode, payload, nil)
	}
	return res.StatusCode, payload, res.Header, nil
}

func statusExpected(status int, expected []int) bool {
	if len(expected) == 0 {
		return status >= 200 && status < 300
	}
	for _, code := range expected {
		if status == code {
			return true
		}
	}
	return false
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout == 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// optionalTimeout adapts a duration-valued timeout (where 0 means "unset, fall
// back to the configured default") to the *time.Duration form used by
// connectUnary. It preserves the legacy semantics of callers that cannot
// distinguish an explicit 0 from an absent value.
func optionalTimeout(timeout time.Duration) *time.Duration {
	if timeout == 0 {
		return nil
	}
	return &timeout
}

func nextTokenHeader(headers http.Header) string {
	if token := headers.Get("X-Next-Token"); token != "" {
		return token
	}
	// Fallback for headers supplied as non-canonical map literals (e.g. tests or
	// callers that bypass Header.Set); real net/http responses are canonicalized.
	if values := headers["x-next-token"]; len(values) > 0 { //nolint:staticcheck // intentional non-canonical lookup
		return values[0]
	}
	return ""
}
