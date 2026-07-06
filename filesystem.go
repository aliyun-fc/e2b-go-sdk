package e2b

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const envdFilesRoute = "/files"

var (
	metadataKeyPattern   = regexp.MustCompile(`\A[A-Za-z0-9!#$%&'*+\-.^_` + "`" + `|~]+\z`)
	metadataValuePattern = regexp.MustCompile(`\A[\x20-\x7e]*\z`)
)

// Filesystem provides sandbox filesystem operations.
type Filesystem struct {
	sandbox *Sandbox
}

type WriteEntry struct {
	Path string
	Data io.Reader
}

type FileStreamReader struct {
	body io.ReadCloser
}

func newFilesystem(s *Sandbox) *Filesystem {
	return &Filesystem{sandbox: s}
}

// Read reads a file as text.
func (f *Filesystem) Read(ctx context.Context, path string, opts ...FileOption) (string, error) {
	data, err := f.ReadBytes(ctx, path, opts...)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReadBytes reads a file as bytes.
func (f *Filesystem) ReadBytes(ctx context.Context, path string, opts ...FileOption) ([]byte, error) {
	options := fileOptionsFrom(opts...)
	res, err := f.fileRequest(ctx, http.MethodGet, path, options.user, nil, nil, options.handshakeTimeout(), options.streamIdle, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if err := handleEnvdHTTPError(res, true); err != nil {
		return nil, err
	}
	return io.ReadAll(res.Body)
}

// ReadStream streams file content. Call Close when finished if not fully consumed.
func (f *Filesystem) ReadStream(ctx context.Context, path string, opts ...FileOption) (*FileStreamReader, error) {
	options := fileOptionsFrom(opts...)
	res, err := f.fileRequest(ctx, http.MethodGet, path, options.user, nil, nil, options.handshakeTimeout(), options.streamIdle, nil)
	if err != nil {
		return nil, err
	}
	if err := handleEnvdHTTPError(res, true); err != nil {
		res.Body.Close()
		return nil, err
	}
	return &FileStreamReader{body: res.Body}, nil
}

func (r *FileStreamReader) Read(p []byte) (int, error) {
	return r.body.Read(p)
}

func (r *FileStreamReader) Close() error {
	if r == nil || r.body == nil {
		return nil
	}
	return r.body.Close()
}

// Write writes one file. String and []byte helpers are provided by WriteText and WriteBytes.
func (f *Filesystem) Write(ctx context.Context, path string, data io.Reader, opts ...FileOption) (WriteInfo, error) {
	results, err := f.WriteFiles(ctx, []WriteEntry{{Path: path, Data: data}}, opts...)
	if err != nil {
		return WriteInfo{}, err
	}
	if len(results) != 1 {
		return WriteInfo{}, &SandboxError{Message: "received unexpected response from write operation"}
	}
	return results[0], nil
}

// WriteText writes text to a file.
func (f *Filesystem) WriteText(ctx context.Context, path, data string, opts ...FileOption) (WriteInfo, error) {
	return f.Write(ctx, path, strings.NewReader(data), opts...)
}

// WriteBytes writes bytes to a file.
func (f *Filesystem) WriteBytes(ctx context.Context, path string, data []byte, opts ...FileOption) (WriteInfo, error) {
	return f.Write(ctx, path, bytes.NewReader(data), opts...)
}

// WriteFiles writes multiple files using multipart/form-data.
func (f *Filesystem) WriteFiles(ctx context.Context, files []WriteEntry, opts ...FileOption) ([]WriteInfo, error) {
	options := fileOptionsFrom(opts...)
	if len(files) == 0 {
		return nil, nil
	}
	if err := validateMetadata(options.metadata); err != nil {
		return nil, err
	}
	if len(options.metadata) > 0 && compareVersion(f.sandbox.envdVersion, "0.6.2") < 0 {
		return nil, &TemplateError{Message: "file metadata requires envd 0.6.2 or later"}
	}

	supportsOctet := compareVersion(f.sandbox.envdVersion, "0.5.7") >= 0
	useOctet := supportsOctet
	if options.useOctetStreamSet {
		useOctet = options.useOctetStream && supportsOctet
	}
	if options.gzip {
		useOctet = supportsOctet
	}
	if useOctet {
		return f.writeOctetFiles(ctx, files, options)
	}
	return f.writeMultipart(ctx, files, options)
}

func (f *Filesystem) writeOctetFiles(ctx context.Context, files []WriteEntry, options fileOptions) ([]WriteInfo, error) {
	results := make([]WriteInfo, 0, len(files))
	for _, file := range files {
		written, err := f.writeOctet(ctx, file, options)
		if err != nil {
			return nil, err
		}
		results = append(results, written...)
	}
	return results, nil
}

func (f *Filesystem) writeOctet(ctx context.Context, file WriteEntry, options fileOptions) ([]WriteInfo, error) {
	var body io.Reader = file.Data
	if options.gzip {
		buf := &bytes.Buffer{}
		zw := gzip.NewWriter(buf)
		if _, err := io.Copy(zw, file.Data); err != nil {
			zw.Close()
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		body = buf
	}
	headers := map[string]string{"Content-Type": "application/octet-stream"}
	if options.gzip {
		headers["Content-Encoding"] = "gzip"
	}
	for k, v := range metadataHeaders(options.metadata) {
		headers[k] = v
	}
	res, err := f.fileRequest(ctx, http.MethodPost, file.Path, options.user, body, headers, options.handshakeTimeout(), nil, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if err := handleEnvdHTTPError(res, true); err != nil {
		return nil, err
	}
	var result []WriteInfo
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func (f *Filesystem) writeMultipart(ctx context.Context, files []WriteEntry, options fileOptions) ([]WriteInfo, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, file := range files {
		partHeader := textproto.MIMEHeader{}
		partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeQuotes(file.Path)))
		partHeader.Set("Content-Type", "application/octet-stream")
		part, err := writer.CreatePart(partHeader)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(part, file.Data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	headers := map[string]string{"Content-Type": writer.FormDataContentType()}
	for k, v := range metadataHeaders(options.metadata) {
		headers[k] = v
	}
	path := ""
	if len(files) == 1 {
		path = files[0].Path
	}
	res, err := f.fileRequest(ctx, http.MethodPost, path, options.user, body, headers, options.handshakeTimeout(), nil, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if err := handleEnvdHTTPError(res, true); err != nil {
		return nil, err
	}
	var result []WriteInfo
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

// List lists entries in a directory.
func (f *Filesystem) List(ctx context.Context, path string, opts ...FileOption) ([]EntryInfo, error) {
	options := fileOptionsFrom(opts...)
	if options.depth != nil && *options.depth < 1 {
		return nil, &InvalidArgumentError{Message: "depth should be at least 1"}
	}
	req := map[string]any{"path": path}
	if options.depth != nil {
		req["depth"] = *options.depth
	} else {
		req["depth"] = 1
	}
	var response struct {
		Entries []entryInfoJSON `json:"entries"`
	}
	err := f.sandbox.connectUnary(ctx, "filesystem.Filesystem", "ListDir", req, &response, options.user, options.handshakeTimeout(), nil)
	if err != nil {
		return nil, mapFilesystemError(err)
	}
	entries := make([]EntryInfo, 0, len(response.Entries))
	for _, entry := range response.Entries {
		if mapped, ok := entry.toEntryInfo(); ok {
			entries = append(entries, mapped)
		}
	}
	return entries, nil
}

// Exists reports whether a file or directory exists.
func (f *Filesystem) Exists(ctx context.Context, path string, opts ...FileOption) (bool, error) {
	_, err := f.GetInfo(ctx, path, opts...)
	if err == nil {
		return true, nil
	}
	var nf *FileNotFoundError
	var generic *NotFoundError
	if errors.As(err, &nf) || errors.As(err, &generic) {
		return false, nil
	}
	return false, err
}

// GetInfo returns metadata for a file or directory.
func (f *Filesystem) GetInfo(ctx context.Context, path string, opts ...FileOption) (EntryInfo, error) {
	options := fileOptionsFrom(opts...)
	var response struct {
		Entry entryInfoJSON `json:"entry"`
	}
	err := f.sandbox.connectUnary(ctx, "filesystem.Filesystem", "Stat", map[string]string{"path": path}, &response, options.user, options.handshakeTimeout(), nil)
	if err != nil {
		return EntryInfo{}, mapFilesystemError(err)
	}
	entry, ok := response.Entry.toEntryInfo()
	if !ok {
		return EntryInfo{}, &SandboxError{Message: "unknown filesystem entry type"}
	}
	return entry, nil
}

// Remove removes a file or directory.
func (f *Filesystem) Remove(ctx context.Context, path string, opts ...FileOption) error {
	options := fileOptionsFrom(opts...)
	err := f.sandbox.connectUnary(ctx, "filesystem.Filesystem", "Remove", map[string]string{"path": path}, nil, options.user, options.handshakeTimeout(), nil)
	return mapFilesystemError(err)
}

// Rename renames a file or directory.
func (f *Filesystem) Rename(ctx context.Context, oldPath, newPath string, opts ...FileOption) (EntryInfo, error) {
	options := fileOptionsFrom(opts...)
	var response struct {
		Entry entryInfoJSON `json:"entry"`
	}
	req := map[string]string{"source": oldPath, "destination": newPath}
	err := f.sandbox.connectUnary(ctx, "filesystem.Filesystem", "Move", req, &response, options.user, options.handshakeTimeout(), nil)
	if err != nil {
		return EntryInfo{}, mapFilesystemError(err)
	}
	entry, ok := response.Entry.toEntryInfo()
	if !ok {
		return EntryInfo{}, &SandboxError{Message: "unknown filesystem entry type"}
	}
	return entry, nil
}

// MakeDir creates a directory and parents as needed. It returns false if the directory already exists.
func (f *Filesystem) MakeDir(ctx context.Context, path string, opts ...FileOption) (bool, error) {
	options := fileOptionsFrom(opts...)
	err := f.sandbox.connectUnary(ctx, "filesystem.Filesystem", "MakeDir", map[string]string{"path": path}, nil, options.user, options.handshakeTimeout(), nil)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "already_exists") || strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return false, nil
	}
	return false, mapFilesystemError(err)
}

// WatchDir creates a persistent watcher that can be polled for new events.
func (f *Filesystem) WatchDir(ctx context.Context, path string, opts ...WatchOption) (*WatchHandle, error) {
	options := watchOptionsFrom(opts...)
	if options.recursive && compareVersion(f.sandbox.envdVersion, "0.1.4") < 0 {
		return nil, &TemplateError{Message: "you need to update the template to use recursive watching"}
	}
	if options.includeEntry && compareVersion(f.sandbox.envdVersion, "0.6.3") < 0 {
		return nil, &TemplateError{Message: "you need to update the template to include entry info in watch events"}
	}
	if options.allowNetworkMounts && compareVersion(f.sandbox.envdVersion, "0.6.4") < 0 {
		return nil, &TemplateError{Message: "you need to update the template to watch directories on network mounts"}
	}
	req := map[string]any{
		"path":               path,
		"recursive":          options.recursive,
		"includeEntry":       options.includeEntry,
		"allowNetworkMounts": options.allowNetworkMounts,
	}
	var response struct {
		WatcherID string `json:"watcherId"`
	}
	err := f.sandbox.connectUnary(ctx, "filesystem.Filesystem", "CreateWatcher", req, &response, options.user, options.handshakeTimeout(), map[string]string{
		keepalivePingHeader: fmt.Sprintf("%d", keepalivePingIntervalSec),
	})
	if err != nil {
		return nil, mapFilesystemError(err)
	}
	return &WatchHandle{filesystem: f, watcherID: response.WatcherID, user: options.user}, nil
}

func (f *Filesystem) fileRequest(ctx context.Context, method, path string, user *string, body io.Reader, headers map[string]string, handshakeTimeout, streamIdle *time.Duration, query url.Values) (*http.Response, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("path", path)
	if user == nil && compareVersion(f.sandbox.envdVersion, "0.4.0") < 0 {
		defaultUser := "user"
		user = &defaultUser
	}
	if user != nil && *user != "" {
		query.Set("username", *user)
	}

	target, err := url.JoinPath(f.sandbox.envdAPIURL, envdFilesRoute)
	if err != nil {
		return nil, err
	}
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}

	// Match the Python SDK: the request timeout bounds only the handshake, while
	// the transfer body is governed by a per-chunk idle timeout (defaults to the
	// request timeout; a caller can override or disable it with 0). The request
	// context has no total deadline — an idleTracker cancels it only when I/O
	// stalls, so large downloads/uploads are not aborted mid-transfer.
	//
	// handshakeTimeout is nil when unset (falls back to the configured
	// RequestTimeout) and non-nil when explicit — including an explicit 0, which
	// disables the timeout, mirroring Python's request_timeout=0 -> None.
	handshake := f.sandbox.client.config.RequestTimeout
	if handshakeTimeout != nil {
		handshake = *handshakeTimeout
	}
	bodyIdle := handshake
	if streamIdle != nil {
		bodyIdle = *streamIdle
	}
	reqCtx, cancel := context.WithCancel(ctx)
	tracker := newIdleTracker(cancel, bodyIdle)
	tracker.arm(handshake)

	req, err := http.NewRequestWithContext(reqCtx, method, target, body)
	if err != nil {
		tracker.stop()
		cancel()
		return nil, err
	}
	// Wrap after construction so http.NewRequestWithContext keeps its
	// Content-Length/GetBody detection on the original body reader.
	if req.Body != nil {
		req.Body = tracker.wrapRequestBody(req.Body)
	}
	for k, v := range f.sandbox.sandboxHeaders(user) {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := f.sandbox.client.http.Do(req)
	if err != nil {
		tracker.stop()
		cancel()
		if tracker.timedOut() {
			return nil, formatRequestTimeout()
		}
		return nil, err
	}
	// Headers have arrived, so the handshake window is done. Pause the timer and
	// let the body reads re-arm it per wire wait (a slow consumer between reads
	// must not trip the idle timeout).
	tracker.pause()
	res.Body = tracker.wrapResponseBody(res.Body)
	return res, nil
}

func handleEnvdHTTPError(res *http.Response, fileErrors bool) error {
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
	switch res.StatusCode {
	case http.StatusBadRequest:
		return &InvalidArgumentError{Message: message}
	case http.StatusUnauthorized:
		return &AuthenticationError{Message: message}
	case http.StatusNotFound:
		if fileErrors {
			return &FileNotFoundError{Message: message}
		}
		return &NotFoundError{Message: message}
	case http.StatusTooManyRequests:
		return &RateLimitError{Message: message + ": The requests are being rate limited."}
	case http.StatusBadGateway:
		return formatSandboxTimeout(message)
	case http.StatusInsufficientStorage:
		return &NotEnoughSpaceError{Message: message}
	default:
		return &SandboxError{Message: fmt.Sprintf("%d: %s", res.StatusCode, message)}
	}
}

func mapFilesystemError(err error) error {
	if err == nil {
		return nil
	}
	var nf *NotFoundError
	if errors.As(err, &nf) {
		return &FileNotFoundError{Message: nf.Message}
	}
	return err
}

type entryInfoJSON struct {
	Name          string            `json:"name"`
	Type          any               `json:"type"`
	Path          string            `json:"path"`
	Size          flexibleInt64     `json:"size"`
	Mode          uint32            `json:"mode"`
	Permissions   string            `json:"permissions"`
	Owner         string            `json:"owner"`
	Group         string            `json:"group"`
	ModifiedTime  time.Time         `json:"modifiedTime"`
	SymlinkTarget *string           `json:"symlinkTarget,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

func (e entryInfoJSON) toEntryInfo() (EntryInfo, bool) {
	ft, ok := mapProtoFileType(e.Type)
	if !ok {
		return EntryInfo{}, false
	}
	return EntryInfo{
		WriteInfo: WriteInfo{
			Name:     e.Name,
			Type:     ft,
			Path:     e.Path,
			Metadata: e.Metadata,
		},
		Size:          int64(e.Size),
		Mode:          e.Mode,
		Permissions:   e.Permissions,
		Owner:         e.Owner,
		Group:         e.Group,
		ModifiedTime:  e.ModifiedTime,
		SymlinkTarget: e.SymlinkTarget,
	}, true
}

func mapProtoFileType(value any) (FileType, bool) {
	switch v := value.(type) {
	case string:
		switch v {
		case "FILE_TYPE_FILE", "file":
			return FileTypeFile, true
		case "FILE_TYPE_DIRECTORY", "dir":
			return FileTypeDir, true
		default:
			return "", false
		}
	case float64:
		switch int(v) {
		case 1:
			return FileTypeFile, true
		case 2:
			return FileTypeDir, true
		default:
			return "", false
		}
	default:
		return "", false
	}
}

type flexibleInt64 int64

func (n *flexibleInt64) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*n = 0
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		if text == "" {
			*n = 0
			return nil
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int64 string %q: %w", text, err)
		}
		*n = flexibleInt64(value)
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*n = flexibleInt64(value)
	return nil
}

func validateMetadata(metadata map[string]string) error {
	for k, v := range metadata {
		if !metadataKeyPattern.MatchString(k) {
			return &InvalidArgumentError{Message: fmt.Sprintf("invalid metadata key %q: keys must be non-empty HTTP token characters", k)}
		}
		if !metadataValuePattern.MatchString(v) {
			return &InvalidArgumentError{Message: fmt.Sprintf("invalid metadata value for key %q: values must be printable US-ASCII", k)}
		}
	}
	return nil
}

func metadataHeaders(metadata map[string]string) map[string]string {
	headers := map[string]string{}
	for k, v := range metadata {
		headers["X-Metadata-"+k] = v
	}
	return headers
}

func escapeQuotes(s string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		`"`, `\"`,
		"\r", "",
		"\n", "",
	)
	return replacer.Replace(s)
}

type fileOptions struct {
	user              *string
	requestTimeout    time.Duration
	requestTimeoutSet bool
	streamIdle        *time.Duration
	depth             *int
	gzip              bool
	useOctetStream    bool
	useOctetStreamSet bool
	metadata          map[string]string
}

// handshakeTimeout returns the explicit request timeout when set (nil
// otherwise), so an explicit 0 is preserved as "disabled" rather than falling
// back to the configured default.
func (o fileOptions) handshakeTimeout() *time.Duration {
	if !o.requestTimeoutSet {
		return nil
	}
	d := o.requestTimeout
	return &d
}

// FileOption configures filesystem reads and writes.
type FileOption func(*fileOptions)

// WithFileUser runs a filesystem operation as a user.
func WithFileUser(user string) FileOption {
	return func(o *fileOptions) { o.user = &user }
}

// WithFileRequestTimeout sets a filesystem request timeout. For reads and
// writes it bounds the initial handshake (and, for reads, the default per-chunk
// idle window of the streamed body unless overridden by
// WithFileStreamIdleTimeout). For other operations it bounds the request.
// Passing 0 explicitly disables the timeout (matching the Python SDK); leaving
// it unset falls back to the client's configured RequestTimeout.
func WithFileRequestTimeout(timeout time.Duration) FileOption {
	return func(o *fileOptions) {
		o.requestTimeout = timeout
		o.requestTimeoutSet = true
	}
}

// WithFileStreamIdleTimeout sets the per-chunk idle timeout for the body of a
// streamed read (ReadStream/ReadBytes), independently of the handshake timeout.
// The transfer is aborted only when no data arrives on the wire within this
// window, so slow-but-progressing large downloads — and slow consumers between
// reads — are not aborted. It defaults to the request timeout; pass 0 to disable
// the body timeout entirely. It does not affect writes, whose body is bounded by
// the request timeout.
func WithFileStreamIdleTimeout(timeout time.Duration) FileOption {
	return func(o *fileOptions) { o.streamIdle = &timeout }
}

// WithListDepth sets List depth.
func WithListDepth(depth int) FileOption {
	return func(o *fileOptions) { o.depth = &depth }
}

// WithGzip requests gzip for downloads or uploads.
func WithGzip(enabled bool) FileOption {
	return func(o *fileOptions) { o.gzip = enabled }
}

// WithOctetStreamUpload controls application/octet-stream upload when supported.
func WithOctetStreamUpload(enabled bool) FileOption {
	return func(o *fileOptions) {
		o.useOctetStream = enabled
		o.useOctetStreamSet = true
	}
}

// WithFileMetadata attaches user metadata to uploaded files.
func WithFileMetadata(metadata map[string]string) FileOption {
	return func(o *fileOptions) { o.metadata = cloneStringMap(metadata) }
}

func fileOptionsFrom(opts ...FileOption) fileOptions {
	options := fileOptions{metadata: map[string]string{}}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

type watchOptions struct {
	user               *string
	requestTimeout     time.Duration
	requestTimeoutSet  bool
	recursive          bool
	includeEntry       bool
	allowNetworkMounts bool
}

// handshakeTimeout mirrors fileOptions.handshakeTimeout: an explicit 0 is
// preserved as "disabled" rather than falling back to the configured default.
func (o watchOptions) handshakeTimeout() *time.Duration {
	if !o.requestTimeoutSet {
		return nil
	}
	d := o.requestTimeout
	return &d
}

// WatchOption configures directory watching.
type WatchOption func(*watchOptions)

func WithWatchUser(user string) WatchOption {
	return func(o *watchOptions) { o.user = &user }
}

// WithWatchRequestTimeout sets the request timeout for creating the watcher.
// Passing 0 explicitly disables it (matching the Python SDK); leaving it unset
// falls back to the client's configured RequestTimeout.
func WithWatchRequestTimeout(timeout time.Duration) WatchOption {
	return func(o *watchOptions) {
		o.requestTimeout = timeout
		o.requestTimeoutSet = true
	}
}

func WithRecursiveWatch(enabled bool) WatchOption {
	return func(o *watchOptions) { o.recursive = enabled }
}

func WithWatchEntryInfo(enabled bool) WatchOption {
	return func(o *watchOptions) { o.includeEntry = enabled }
}

func WithWatchNetworkMounts(enabled bool) WatchOption {
	return func(o *watchOptions) { o.allowNetworkMounts = enabled }
}

func watchOptionsFrom(opts ...WatchOption) watchOptions {
	var options watchOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}
