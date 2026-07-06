package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultVolumeFileTimeout = time.Hour

// VolumeInfo is basic volume metadata.
type VolumeInfo struct {
	VolumeID string `json:"volumeID"`
	Name     string `json:"name"`
}

// VolumeAndToken includes the volume content API token.
type VolumeAndToken struct {
	VolumeInfo
	Token string `json:"token"`
}

// VolumeEntryStat is returned by volume content metadata operations.
type VolumeEntryStat struct {
	Name   string    `json:"name"`
	Type   FileType  `json:"type"`
	Path   string    `json:"path"`
	Size   int64     `json:"size"`
	Mode   int       `json:"mode"`
	UID    int       `json:"uid"`
	GID    int       `json:"gid"`
	ATime  time.Time `json:"atime"`
	MTime  time.Time `json:"mtime"`
	CTime  time.Time `json:"ctime"`
	Target string    `json:"target,omitempty"`
}

// Volume is an E2B persistent volume.
type Volume struct {
	client   *Client
	volumeID string
	name     string
	token    string
	domain   string
	debug    bool
	apiURL   string
}

// CreateVolume creates a new team volume.
func (c *Client) CreateVolume(ctx context.Context, name string) (*Volume, error) {
	var response VolumeAndToken
	if err := c.doJSON(ctx, http.MethodPost, "/volumes", nil, map[string]string{"name": name}, &response, http.StatusCreated); err != nil {
		return nil, &VolumeError{Message: err.Error()}
	}
	return c.newVolume(response), nil
}

// ConnectVolume connects to an existing volume by ID.
func (c *Client) ConnectVolume(ctx context.Context, volumeID string) (*Volume, error) {
	info, err := c.GetVolumeInfo(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	return c.newVolume(info), nil
}

// GetVolumeInfo returns volume metadata and content token.
func (c *Client) GetVolumeInfo(ctx context.Context, volumeID string) (VolumeAndToken, error) {
	var response VolumeAndToken
	path := "/volumes/" + url.PathEscape(volumeID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &response); err != nil {
		var nf *NotFoundError
		if errors.As(err, &nf) {
			return VolumeAndToken{}, &NotFoundError{Message: "volume " + volumeID + " not found"}
		}
		return VolumeAndToken{}, &VolumeError{Message: err.Error()}
	}
	return response, nil
}

// ListVolumes lists all team volumes.
func (c *Client) ListVolumes(ctx context.Context) ([]VolumeInfo, error) {
	var response []VolumeInfo
	if err := c.doJSON(ctx, http.MethodGet, "/volumes", nil, nil, &response); err != nil {
		return nil, &VolumeError{Message: err.Error()}
	}
	return response, nil
}

// DestroyVolume deletes a volume. It returns false when not found.
func (c *Client) DestroyVolume(ctx context.Context, volumeID string) (bool, error) {
	path := "/volumes/" + url.PathEscape(volumeID)
	status, _, err := c.do(ctx, http.MethodDelete, c.config.apiURL(), path, nil, nil, c.config.Headers, http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound)
	if err != nil {
		return false, &VolumeError{Message: err.Error()}
	}
	return status != http.StatusNotFound, nil
}

func (c *Client) newVolume(info VolumeAndToken) *Volume {
	return &Volume{
		client:   c,
		volumeID: info.VolumeID,
		name:     info.Name,
		token:    info.Token,
		domain:   c.config.Domain,
		debug:    c.config.Debug,
		apiURL:   volumeAPIURL(c.config),
	}
}

func (v *Volume) VolumeID() string { return v.volumeID }
func (v *Volume) Name() string     { return v.name }
func (v *Volume) Token() string    { return v.token }

// List lists directory contents.
func (v *Volume) List(ctx context.Context, path string, opts ...VolumeOption) ([]VolumeEntryStat, error) {
	options := volumeOptionsFrom(opts...)
	query := url.Values{"path": []string{path}}
	if options.depth != nil {
		query.Set("depth", strconv.Itoa(*options.depth))
	}
	var response []VolumeEntryStat
	if err := v.volumeJSON(ctx, http.MethodGet, "/volumecontent/"+url.PathEscape(v.volumeID)+"/dir", query, nil, &response, options); err != nil {
		return nil, err
	}
	return response, nil
}

// MakeDir creates a directory.
func (v *Volume) MakeDir(ctx context.Context, path string, opts ...VolumeOption) (VolumeEntryStat, error) {
	options := volumeOptionsFrom(opts...)
	query := volumePathQuery(path, options)
	var response VolumeEntryStat
	err := v.volumeJSON(ctx, http.MethodPost, "/volumecontent/"+url.PathEscape(v.volumeID)+"/dir", query, nil, &response, options)
	return response, err
}

// Exists reports whether a file or directory exists.
func (v *Volume) Exists(ctx context.Context, path string, opts ...VolumeOption) (bool, error) {
	_, err := v.GetInfo(ctx, path, opts...)
	if err == nil {
		return true, nil
	}
	var nf *NotFoundError
	if errors.As(err, &nf) {
		return false, nil
	}
	return false, err
}

// GetInfo returns file or directory metadata.
func (v *Volume) GetInfo(ctx context.Context, path string, opts ...VolumeOption) (VolumeEntryStat, error) {
	options := volumeOptionsFrom(opts...)
	query := url.Values{"path": []string{path}}
	var response VolumeEntryStat
	err := v.volumeJSON(ctx, http.MethodGet, "/volumecontent/"+url.PathEscape(v.volumeID)+"/path", query, nil, &response, options)
	return response, err
}

// UpdateMetadata updates uid, gid, or mode.
func (v *Volume) UpdateMetadata(ctx context.Context, path string, opts ...VolumeOption) (VolumeEntryStat, error) {
	options := volumeOptionsFrom(opts...)
	query := url.Values{"path": []string{path}}
	body := map[string]int{}
	if options.uid != nil {
		body["uid"] = *options.uid
	}
	if options.gid != nil {
		body["gid"] = *options.gid
	}
	if options.mode != nil {
		body["mode"] = *options.mode
	}
	var response VolumeEntryStat
	err := v.volumeJSON(ctx, http.MethodPatch, "/volumecontent/"+url.PathEscape(v.volumeID)+"/path", query, body, &response, options)
	return response, err
}

// ReadFile reads a file as text.
func (v *Volume) ReadFile(ctx context.Context, path string, opts ...VolumeOption) (string, error) {
	data, err := v.ReadFileBytes(ctx, path, opts...)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReadFileBytes reads a file as bytes.
func (v *Volume) ReadFileBytes(ctx context.Context, path string, opts ...VolumeOption) ([]byte, error) {
	options := volumeOptionsFrom(opts...)
	res, err := v.volumeRequest(ctx, http.MethodGet, "/volumecontent/"+url.PathEscape(v.volumeID)+"/file", url.Values{"path": []string{path}}, nil, nil, options)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if err := handleVolumeHTTPError(res); err != nil {
		return nil, err
	}
	return io.ReadAll(res.Body)
}

// ReadFileStream streams a file.
func (v *Volume) ReadFileStream(ctx context.Context, path string, opts ...VolumeOption) (io.ReadCloser, error) {
	options := volumeOptionsFrom(opts...)
	res, err := v.volumeRequest(ctx, http.MethodGet, "/volumecontent/"+url.PathEscape(v.volumeID)+"/file", url.Values{"path": []string{path}}, nil, nil, options)
	if err != nil {
		return nil, err
	}
	if err := handleVolumeHTTPError(res); err != nil {
		res.Body.Close()
		return nil, err
	}
	return res.Body, nil
}

// WriteFile writes a file from a reader.
func (v *Volume) WriteFile(ctx context.Context, path string, data io.Reader, opts ...VolumeOption) (VolumeEntryStat, error) {
	options := volumeOptionsFrom(opts...)
	query := volumePathQuery(path, options)
	var response VolumeEntryStat
	err := v.volumeJSONReader(ctx, http.MethodPut, "/volumecontent/"+url.PathEscape(v.volumeID)+"/file", query, data, &response, options)
	return response, err
}

func (v *Volume) WriteFileText(ctx context.Context, path, data string, opts ...VolumeOption) (VolumeEntryStat, error) {
	return v.WriteFile(ctx, path, strings.NewReader(data), opts...)
}

func (v *Volume) WriteFileBytes(ctx context.Context, path string, data []byte, opts ...VolumeOption) (VolumeEntryStat, error) {
	return v.WriteFile(ctx, path, bytes.NewReader(data), opts...)
}

// Remove removes a file or directory.
func (v *Volume) Remove(ctx context.Context, path string, opts ...VolumeOption) error {
	options := volumeOptionsFrom(opts...)
	return v.volumeJSON(ctx, http.MethodDelete, "/volumecontent/"+url.PathEscape(v.volumeID)+"/path", url.Values{"path": []string{path}}, nil, nil, options)
}

func (v *Volume) volumeJSON(ctx context.Context, method, path string, query url.Values, body any, out any, options volumeOptions) error {
	var reader io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return err
		}
		reader = buf
	}
	headers := map[string]string{}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	res, err := v.volumeRequest(ctx, method, path, query, reader, headers, options)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := handleVolumeHTTPError(res); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (v *Volume) volumeJSONReader(ctx context.Context, method, path string, query url.Values, body io.Reader, out any, options volumeOptions) error {
	headers := map[string]string{"Content-Type": "application/octet-stream"}
	res, err := v.volumeRequest(ctx, method, path, query, body, headers, options)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := handleVolumeHTTPError(res); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (v *Volume) volumeRequest(ctx context.Context, method, path string, query url.Values, body io.Reader, headers map[string]string, options volumeOptions) (*http.Response, error) {
	target, err := url.JoinPath(v.apiURL, strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	timeout := options.requestTimeout
	if timeout == 0 {
		timeout = defaultVolumeFileTimeout
	}
	requestCtx, cancel := withTimeout(ctx, timeout)
	// The response body outlives this function (callers stream/close it), so the
	// timeout must govern the whole request; cancel is invoked only on the error
	// paths below, and otherwise the deadline fires on its own. Keep the binding
	// to satisfy go vet's lostcancel check.
	_ = cancel
	req, err := http.NewRequestWithContext(requestCtx, method, target, body)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("User-Agent", buildUserAgent(""))
	if v.token != "" {
		req.Header.Set("Authorization", "Bearer "+v.token)
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	res, err := v.client.http.Do(req)
	if err != nil {
		callerErr := ctx.Err()
		requestErr := requestCtx.Err()
		cancel()
		if callerErr != nil {
			return nil, callerErr
		}
		if requestErr != nil {
			return nil, formatRequestTimeout()
		}
		return nil, err
	}
	res.Body = cancelReadCloser{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	if c.cancel != nil {
		c.cancel()
	}
	return err
}

func handleVolumeHTTPError(res *http.Response) error {
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(res.Body)
	message := string(body)
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		message = parsed.Message
	}
	if res.StatusCode == http.StatusNotFound {
		return &NotFoundError{Message: message}
	}
	return &VolumeError{Message: strconv.Itoa(res.StatusCode) + ": " + message}
}

func volumeAPIURL(config Config) string {
	if env := os.Getenv("E2B_VOLUME_API_URL"); env != "" {
		return strings.TrimRight(env, "/")
	}
	if config.Debug {
		return "http://localhost:8080"
	}
	return config.apiURL()
}

func volumePathQuery(path string, options volumeOptions) url.Values {
	query := url.Values{"path": []string{path}}
	if options.uid != nil {
		query.Set("uid", strconv.Itoa(*options.uid))
	}
	if options.gid != nil {
		query.Set("gid", strconv.Itoa(*options.gid))
	}
	if options.mode != nil {
		query.Set("mode", strconv.Itoa(*options.mode))
	}
	if options.force != nil {
		query.Set("force", strconv.FormatBool(*options.force))
	}
	return query
}

type volumeOptions struct {
	uid            *int
	gid            *int
	mode           *int
	force          *bool
	depth          *int
	requestTimeout time.Duration
}

// VolumeOption configures volume content operations.
type VolumeOption func(*volumeOptions)

func WithVolumeUID(uid int) VolumeOption {
	return func(o *volumeOptions) { o.uid = &uid }
}

func WithVolumeGID(gid int) VolumeOption {
	return func(o *volumeOptions) { o.gid = &gid }
}

func WithVolumeMode(mode int) VolumeOption {
	return func(o *volumeOptions) { o.mode = &mode }
}

func WithVolumeForce(force bool) VolumeOption {
	return func(o *volumeOptions) { o.force = &force }
}

func WithVolumeDepth(depth int) VolumeOption {
	return func(o *volumeOptions) { o.depth = &depth }
}

func WithVolumeRequestTimeout(timeout time.Duration) VolumeOption {
	return func(o *volumeOptions) { o.requestTimeout = timeout }
}

func volumeOptionsFrom(opts ...VolumeOption) volumeOptions {
	var options volumeOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}
