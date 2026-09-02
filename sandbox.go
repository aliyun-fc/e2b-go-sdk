package e2b

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSandboxTimeoutSeconds = 300
	DefaultTemplate              = "base"
	DefaultMCPTemplate           = "mcp-gateway"
	MCPPort                      = 50005
)

// Sandbox represents a running or paused E2B sandbox.
type Sandbox struct {
	client             *Client
	sandboxID          string
	sandboxDomain      string
	envdVersion        string
	envdAccessToken    string
	trafficAccessToken string
	envdAPIURL         string
	envdDirectURL      string
	mcpToken           string

	Files    *Filesystem
	Commands *Commands
	Pty      *Pty
	Git      *Git
}

type sandboxCreateResponse struct {
	ClientID           string `json:"clientID"`
	EnvdVersion        string `json:"envdVersion"`
	SandboxID          string `json:"sandboxID"`
	TemplateID         string `json:"templateID"`
	Alias              string `json:"alias,omitempty"`
	Domain             string `json:"domain,omitempty"`
	EnvdAccessToken    string `json:"envdAccessToken,omitempty"`
	TrafficAccessToken string `json:"trafficAccessToken,omitempty"`
}

type newSandboxRequest struct {
	TemplateID          string               `json:"templateID"`
	AllowInternetAccess bool                 `json:"allow_internet_access"`
	AutoPause           bool                 `json:"autoPause"`
	AutoResume          map[string]bool      `json:"autoResume,omitempty"`
	EnvVars             map[string]string    `json:"envVars"`
	MCP                 any                  `json:"mcp,omitempty"`
	Metadata            map[string]string    `json:"metadata"`
	Network             *SandboxNetworkOpts  `json:"network,omitempty"`
	Secure              bool                 `json:"secure"`
	Timeout             int                  `json:"timeout"`
	VolumeMounts        []SandboxVolumeMount `json:"volumeMounts,omitempty"`
}

type sandboxCreateOptions struct {
	template            string
	timeoutSeconds      int
	metadata            map[string]string
	envs                map[string]string
	secure              bool
	allowInternetAccess bool
	mcp                 any
	network             *SandboxNetworkOpts
	lifecycle           *SandboxLifecycle
	volumeMounts        []SandboxVolumeMount
}

// SandboxPage is one page of sandbox list results.
type SandboxPage struct {
	Items     []SandboxInfo
	NextToken string
	HasNext   bool
}

// SnapshotPage is one page of snapshot list results.
type SnapshotPage struct {
	Items     []SnapshotInfo
	NextToken string
	HasNext   bool
}

// SandboxCreateOption configures sandbox creation.
type SandboxCreateOption func(*sandboxCreateOptions)

// WithTemplate sets the sandbox template name, ID, or snapshot.
func WithTemplate(template string) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.template = template }
}

// WithTimeout sets the sandbox timeout in seconds.
func WithTimeout(seconds int) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.timeoutSeconds = seconds }
}

// WithMetadata replaces sandbox metadata.
func WithMetadata(metadata map[string]string) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.metadata = cloneStringMap(metadata) }
}

// WithEnv sets one environment variable for sandbox creation.
func WithEnv(key, value string) SandboxCreateOption {
	return func(o *sandboxCreateOptions) {
		if o.envs == nil {
			o.envs = map[string]string{}
		}
		o.envs[key] = value
	}
}

// WithEnvs replaces sandbox creation environment variables.
func WithEnvs(envs map[string]string) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.envs = cloneStringMap(envs) }
}

// WithSecure controls whether envd is protected by an access token.
func WithSecure(secure bool) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.secure = secure }
}

// WithInternetAccess controls sandbox internet access.
func WithInternetAccess(allow bool) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.allowInternetAccess = allow }
}

// WithMCP enables MCP configuration for the sandbox.
func WithMCP(mcp any) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.mcp = mcp }
}

// WithNetwork sets sandbox network configuration.
func WithNetwork(network SandboxNetworkOpts) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.network = &network }
}

// WithLifecycle sets sandbox timeout lifecycle behavior.
func WithLifecycle(lifecycle SandboxLifecycle) SandboxCreateOption {
	return func(o *sandboxCreateOptions) { o.lifecycle = &lifecycle }
}

// WithVolumeMount mounts a team volume name at a sandbox path.
func WithVolumeMount(path, name string) SandboxCreateOption {
	return func(o *sandboxCreateOptions) {
		o.volumeMounts = append(o.volumeMounts, SandboxVolumeMount{Name: name, Path: path})
	}
}

// CreateSandbox creates a sandbox using a client built from environment configuration.
func CreateSandbox(ctx context.Context, opts ...SandboxCreateOption) (*Sandbox, error) {
	client, err := NewClient()
	if err != nil {
		return nil, err
	}
	return client.CreateSandbox(ctx, opts...)
}

// CreateSandbox creates a new sandbox.
func (c *Client) CreateSandbox(ctx context.Context, opts ...SandboxCreateOption) (*Sandbox, error) {
	options := sandboxCreateOptions{
		template:            DefaultTemplate,
		timeoutSeconds:      DefaultSandboxTimeoutSeconds,
		metadata:            map[string]string{},
		envs:                map[string]string{},
		secure:              true,
		allowInternetAccess: true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	if options.template == "" && options.mcp != nil {
		options.template = DefaultMCPTemplate
	} else if options.template == "" {
		options.template = DefaultTemplate
	}
	if options.timeoutSeconds == 0 {
		options.timeoutSeconds = DefaultSandboxTimeoutSeconds
	}

	onTimeout := "kill"
	autoResume := false
	if options.lifecycle != nil {
		if options.lifecycle.OnTimeout != "" {
			onTimeout = options.lifecycle.OnTimeout
		}
		autoResume = options.lifecycle.AutoResume
	}
	if autoResume && onTimeout != "pause" {
		return nil, &InvalidArgumentError{Message: "auto_resume can only be true when on_timeout is pause"}
	}

	body := newSandboxRequest{
		TemplateID:          options.template,
		AllowInternetAccess: options.allowInternetAccess,
		AutoPause:           onTimeout == "pause",
		AutoResume:          map[string]bool{"enabled": autoResume},
		EnvVars:             nonNilStringMap(options.envs),
		MCP:                 options.mcp,
		Metadata:            nonNilStringMap(options.metadata),
		Network:             options.network,
		Secure:              options.secure,
		Timeout:             options.timeoutSeconds,
		VolumeMounts:        options.volumeMounts,
	}

	var response sandboxCreateResponse
	if err := c.doJSON(ctx, http.MethodPost, "/sandboxes", nil, body, &response, http.StatusCreated); err != nil {
		return nil, err
	}
	if compareVersion(response.EnvdVersion, "0.1.0") < 0 {
		c.cleanupCreatedSandbox(response.SandboxID)
		return nil, &TemplateError{Message: "you need to update the template to use the new SDK"}
	}
	sandbox := c.newSandbox(response)
	if options.mcp != nil {
		token, err := newRandomToken()
		if err != nil {
			c.cleanupCreatedSandbox(response.SandboxID)
			return nil, err
		}
		sandbox.mcpToken = token
		handle, err := sandbox.Commands.Start(ctx, fmt.Sprintf("mcp-gateway --config %s", shellQuoteJSON(options.mcp)), WithCommandUser("root"), WithCommandEnv("GATEWAY_ACCESS_TOKEN", token), WithCommandTimeout(0))
		if err != nil {
			c.cleanupCreatedSandbox(response.SandboxID)
			return nil, formatMCPGatewayStartError(err)
		}
		_ = handle.Disconnect()
	}
	return sandbox, nil
}

func (c *Client) cleanupCreatedSandbox(sandboxID string) {
	cleanupCtx, cancel := withTimeout(context.Background(), c.config.requestContextTimeout(0))
	defer cancel()
	_, _ = c.KillSandbox(cleanupCtx, sandboxID)
}

func formatMCPGatewayStartError(err error) error {
	var exitErr *CommandExitError
	if errors.As(err, &exitErr) {
		details := strings.TrimSpace(exitErr.Result.Stderr)
		if details == "" {
			details = strings.TrimSpace(exitErr.Result.Error)
		}
		if details == "" {
			details = strings.TrimSpace(exitErr.Result.Stdout)
		}
		if details == "" {
			details = err.Error()
		}
		return &SandboxError{Message: "failed to start MCP gateway: " + details}
	}
	return &SandboxError{Message: "failed to start MCP gateway: " + err.Error()}
}

// ConnectSandbox connects to a running or paused sandbox by ID.
func (c *Client) ConnectSandbox(ctx context.Context, sandboxID string, timeoutSeconds ...int) (*Sandbox, error) {
	timeout := DefaultSandboxTimeoutSeconds
	if len(timeoutSeconds) > 0 && timeoutSeconds[0] > 0 {
		timeout = timeoutSeconds[0]
	}
	var response sandboxCreateResponse
	body := map[string]int{"timeout": timeout}
	path := "/sandboxes/" + url.PathEscape(sandboxID) + "/connect"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, body, &response, http.StatusOK, http.StatusCreated); err != nil {
		return nil, err
	}
	return c.newSandbox(response), nil
}

// Connect reconnects this sandbox and extends its timeout if needed.
func (s *Sandbox) Connect(ctx context.Context, timeoutSeconds ...int) (*Sandbox, error) {
	if s.client.config.Debug {
		return s, nil
	}
	return s.client.ConnectSandbox(ctx, s.sandboxID, timeoutSeconds...)
}

// KillSandbox terminates a sandbox. It returns false when the sandbox is already gone.
func (c *Client) KillSandbox(ctx context.Context, sandboxID string) (bool, error) {
	if c.config.Debug {
		return true, nil
	}
	path := "/sandboxes/" + url.PathEscape(sandboxID)
	status, payload, err := c.do(ctx, http.MethodDelete, c.config.apiURL(), path, nil, nil, c.config.Headers, http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		_ = payload
		return false, nil
	}
	return true, nil
}

// Kill terminates the sandbox.
func (s *Sandbox) Kill(ctx context.Context) (bool, error) {
	return s.client.KillSandbox(ctx, s.sandboxID)
}

// SetSandboxTimeout updates sandbox timeout in seconds.
func (c *Client) SetSandboxTimeout(ctx context.Context, sandboxID string, timeoutSeconds int) error {
	if c.config.Debug {
		return nil
	}
	path := "/sandboxes/" + url.PathEscape(sandboxID) + "/timeout"
	return c.doJSON(ctx, http.MethodPost, path, nil, map[string]int{"timeout": timeoutSeconds}, nil)
}

// SetTimeout updates this sandbox timeout in seconds.
func (s *Sandbox) SetTimeout(ctx context.Context, timeoutSeconds int) error {
	return s.client.SetSandboxTimeout(ctx, s.sandboxID, timeoutSeconds)
}

// UpdateSandboxNetwork atomically replaces the mutable sandbox network
// configuration. Fields omitted from network are cleared by the control plane.
func (c *Client) UpdateSandboxNetwork(ctx context.Context, sandboxID string, network SandboxNetworkUpdate) error {
	path := "/sandboxes/" + url.PathEscape(sandboxID) + "/network"
	return c.doJSON(ctx, http.MethodPut, path, nil, network, nil)
}

// UpdateNetwork atomically replaces this sandbox's mutable network
// configuration. Fields omitted from network are cleared by the control plane.
func (s *Sandbox) UpdateNetwork(ctx context.Context, network SandboxNetworkUpdate) error {
	return s.client.UpdateSandboxNetwork(ctx, s.sandboxID, network)
}

// GetSandboxInfo returns sandbox information.
func (c *Client) GetSandboxInfo(ctx context.Context, sandboxID string) (SandboxInfo, error) {
	var info SandboxInfo
	path := "/sandboxes/" + url.PathEscape(sandboxID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &info); err != nil {
		return SandboxInfo{}, err
	}
	if info.Metadata == nil {
		info.Metadata = map[string]string{}
	}
	return info, nil
}

// GetInfo returns information for this sandbox.
func (s *Sandbox) GetInfo(ctx context.Context) (SandboxInfo, error) {
	return s.client.GetSandboxInfo(ctx, s.sandboxID)
}

// GetSandboxMetrics returns sandbox metrics.
func (c *Client) GetSandboxMetrics(ctx context.Context, sandboxID string, start, end *time.Time) ([]SandboxMetrics, error) {
	query := url.Values{}
	if start != nil {
		query.Set("start", strconv.FormatInt(start.Unix(), 10))
	}
	if end != nil {
		query.Set("end", strconv.FormatInt(end.Unix(), 10))
	}
	var metrics []SandboxMetrics
	path := "/sandboxes/" + url.PathEscape(sandboxID) + "/metrics"
	if err := c.doJSON(ctx, http.MethodGet, path, query, nil, &metrics); err != nil {
		return nil, err
	}
	return metrics, nil
}

// GetMetrics returns metrics for this sandbox.
func (s *Sandbox) GetMetrics(ctx context.Context, start, end *time.Time) ([]SandboxMetrics, error) {
	if s.client.config.Debug {
		return nil, nil
	}
	if compareVersion(s.envdVersion, "0.1.5") < 0 {
		return nil, &TemplateError{Message: "you need to update the template to use the new SDK"}
	}
	return s.client.GetSandboxMetrics(ctx, s.sandboxID, start, end)
}

// PauseSandbox pauses a sandbox. It returns false when the sandbox is already paused.
func (c *Client) PauseSandbox(ctx context.Context, sandboxID string) (bool, error) {
	path := "/sandboxes/" + url.PathEscape(sandboxID) + "/pause"
	status, _, err := c.do(ctx, http.MethodPost, c.config.apiURL(), path, nil, nil, c.config.Headers, http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusConflict)
	if err != nil {
		return false, err
	}
	return status != http.StatusConflict, nil
}

// Pause pauses this sandbox.
func (s *Sandbox) Pause(ctx context.Context) (bool, error) {
	return s.client.PauseSandbox(ctx, s.sandboxID)
}

// CreateSandboxSnapshot creates a snapshot from a sandbox.
func (c *Client) CreateSandboxSnapshot(ctx context.Context, sandboxID string, name string) (SnapshotInfo, error) {
	body := map[string]string{}
	if name != "" {
		body["name"] = name
	}
	var snapshot SnapshotInfo
	path := "/sandboxes/" + url.PathEscape(sandboxID) + "/snapshots"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, body, &snapshot); err != nil {
		return SnapshotInfo{}, err
	}
	return snapshot, nil
}

// CreateSnapshot creates a snapshot from this sandbox.
func (s *Sandbox) CreateSnapshot(ctx context.Context, name string) (SnapshotInfo, error) {
	return s.client.CreateSandboxSnapshot(ctx, s.sandboxID, name)
}

// ListSandboxes lists sandboxes with optional query, limit, and next token.
func (c *Client) ListSandboxes(ctx context.Context, query *SandboxQuery, limit int, nextToken string) (SandboxPage, error) {
	params := url.Values{}
	if query != nil {
		if len(query.Metadata) > 0 {
			metadata := url.Values{}
			for k, v := range query.Metadata {
				metadata.Set(k, v)
			}
			params.Set("metadata", metadata.Encode())
		}
		if len(query.State) > 0 {
			states := make([]string, 0, len(query.State))
			for _, state := range query.State {
				states = append(states, string(state))
			}
			params.Set("state", strings.Join(states, ","))
		}
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if nextToken != "" {
		params.Set("nextToken", nextToken)
	}
	status, payload, headers, err := c.doFull(ctx, http.MethodGet, c.config.apiURL(), "/v2/sandboxes", params, nil, c.config.Headers)
	if err != nil {
		_ = status
		return SandboxPage{}, err
	}
	var items []SandboxInfo
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &items); err != nil {
			return SandboxPage{}, err
		}
	}
	token := nextTokenHeader(headers)
	return SandboxPage{Items: items, NextToken: token, HasNext: token != ""}, nil
}

// ListSnapshots lists snapshots with optional source sandbox filter.
func (c *Client) ListSnapshots(ctx context.Context, sandboxID string, limit int, nextToken string) (SnapshotPage, error) {
	params := url.Values{}
	if sandboxID != "" {
		params.Set("sandboxID", sandboxID)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if nextToken != "" {
		params.Set("nextToken", nextToken)
	}
	_, payload, headers, err := c.doFull(ctx, http.MethodGet, c.config.apiURL(), "/snapshots", params, nil, c.config.Headers)
	if err != nil {
		return SnapshotPage{}, err
	}
	var items []SnapshotInfo
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &items); err != nil {
			return SnapshotPage{}, err
		}
	}
	token := nextTokenHeader(headers)
	return SnapshotPage{Items: items, NextToken: token, HasNext: token != ""}, nil
}

// ListSnapshots lists snapshots created from this sandbox.
func (s *Sandbox) ListSnapshots(ctx context.Context, limit int, nextToken string) (SnapshotPage, error) {
	return s.client.ListSnapshots(ctx, s.sandboxID, limit, nextToken)
}

// DeleteSnapshot deletes a snapshot by snapshot ID.
func (c *Client) DeleteSnapshot(ctx context.Context, snapshotID string) (bool, error) {
	path := "/templates/" + url.PathEscape(snapshotID)
	status, _, err := c.do(ctx, http.MethodDelete, c.config.apiURL(), path, nil, nil, c.config.Headers, http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound)
	if err != nil {
		return false, err
	}
	return status != http.StatusNotFound, nil
}

// IsRunning probes envd health.
func (s *Sandbox) IsRunning(ctx context.Context) (bool, error) {
	status, _, err := s.client.do(ctx, http.MethodGet, s.envdAPIURL, "/health", nil, nil, s.sandboxHeaders(nil), http.StatusOK, http.StatusNoContent, http.StatusBadGateway)
	if err != nil {
		return false, err
	}
	return status != http.StatusBadGateway, nil
}

// SandboxID returns the sandbox ID.
func (s *Sandbox) SandboxID() string { return s.sandboxID }

// SandboxDomain returns the sandbox domain.
func (s *Sandbox) SandboxDomain() string { return s.sandboxDomain }

// EnvdVersion returns the envd version.
func (s *Sandbox) EnvdVersion() string { return s.envdVersion }

// EnvdAPIURL returns the sandbox envd API URL.
func (s *Sandbox) EnvdAPIURL() string { return s.envdAPIURL }

// EnvdDirectURL returns the direct sandbox envd URL.
func (s *Sandbox) EnvdDirectURL() string { return s.envdDirectURL }

// TrafficAccessToken returns the traffic proxy token, when present.
func (s *Sandbox) TrafficAccessToken() string { return s.trafficAccessToken }

// SandboxAccessToken returns the access token required by secured sandbox
// services, when present.
//
// This is an FC-specific integration extension for trusted proxies such as
// CSM, not part of the upstream E2B SDK's public API. Treat the returned value
// as a secret: use it only as X-Access-Token for sandbox requests, and do not
// log, serialize, persist, or expose it to untrusted components.
func (s *Sandbox) SandboxAccessToken() string { return s.envdAccessToken }

// MCPToken returns the generated MCP gateway token, when MCP is enabled.
func (s *Sandbox) MCPToken() string { return s.mcpToken }

// GetHost returns the public host for a sandbox port.
func (s *Sandbox) GetHost(port int) string {
	return s.client.config.host(s.sandboxID, s.sandboxDomain, port)
}

// GetMCPURL returns the MCP URL for this sandbox.
func (s *Sandbox) GetMCPURL() string {
	return "https://" + s.GetHost(MCPPort) + "/mcp"
}

// DownloadURL returns a URL for downloading a sandbox file.
func (s *Sandbox) DownloadURL(path string, user *string, signatureExpirationSeconds *int) (string, error) {
	return s.fileURL(path, "read", user, signatureExpirationSeconds)
}

// UploadURL returns a URL for uploading a sandbox file.
func (s *Sandbox) UploadURL(path string, user *string, signatureExpirationSeconds *int) (string, error) {
	return s.fileURL(path, "write", user, signatureExpirationSeconds)
}

func (c *Client) newSandbox(response sandboxCreateResponse) *Sandbox {
	domain := response.Domain
	if domain == "" {
		domain = c.config.Domain
	}
	s := &Sandbox{
		client:             c,
		sandboxID:          response.SandboxID,
		sandboxDomain:      domain,
		envdVersion:        response.EnvdVersion,
		envdAccessToken:    response.EnvdAccessToken,
		trafficAccessToken: response.TrafficAccessToken,
	}
	s.envdAPIURL = c.config.sandboxURL(s.sandboxID, s.sandboxDomain)
	s.envdDirectURL = c.config.sandboxDirectURL(s.sandboxID, s.sandboxDomain)
	s.Files = newFilesystem(s)
	s.Commands = newCommands(s)
	s.Pty = newPty(s)
	s.Git = newGit(s.Commands)
	return s
}

func (s *Sandbox) fileURL(path, operation string, user *string, signatureExpirationSeconds *int) (string, error) {
	if s.envdAccessToken == "" && signatureExpirationSeconds != nil {
		return "", &InvalidArgumentError{Message: "signature expiration can be used only when sandbox is created as secured"}
	}
	username := user
	if username == nil && compareVersion(s.envdVersion, "0.4.0") < 0 {
		defaultUser := "user"
		username = &defaultUser
	}

	query := url.Values{}
	if path != "" {
		query.Set("path", path)
	}
	if username != nil && *username != "" {
		query.Set("username", *username)
	}
	if s.envdAccessToken != "" {
		signature, err := GetSignature(path, operation, username, s.envdAccessToken, signatureExpirationSeconds)
		if err != nil {
			return "", err
		}
		query.Set("signature", signature.Signature)
		if signature.Expiration != nil {
			query.Set("signature_expiration", strconv.FormatInt(*signature.Expiration, 10))
		}
	}
	u, err := url.JoinPath(s.envdDirectURL, "/files")
	if err != nil {
		return "", err
	}
	if encoded := query.Encode(); encoded != "" {
		u += "?" + encoded
	}
	return u, nil
}

func (s *Sandbox) sandboxHeaders(user *string) map[string]string {
	headers := mergeHeaders(s.client.config.SandboxHeaders, authenticationHeader(s.envdVersion, user))
	if s.envdAccessToken != "" {
		headers["X-Access-Token"] = s.envdAccessToken
	}
	headers["E2b-Sandbox-Id"] = s.sandboxID
	headers["E2b-Sandbox-Port"] = strconv.Itoa(EnvdPort)
	return headers
}

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func nonNilStringMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}

func newRandomToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate MCP token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

func shellQuoteJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "''"
	}
	return "'" + strings.ReplaceAll(string(b), "'", `'"'"'`) + "'"
}
