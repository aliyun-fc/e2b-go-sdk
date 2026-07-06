package e2b

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fcovFilesystem builds a data-plane Filesystem backed by a RoundTripper so both
// the envd HTTP path (fileRequest) and the Connect-RPC path (connectUnary) can be
// mocked without a live sandbox.
func fcovFilesystem(t *testing.T, transport http.RoundTripper, version string) *Filesystem {
	t.Helper()
	client := mustTestClient(t, transport)
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: version}
	sandbox.Files = newFilesystem(sandbox)
	return sandbox.Files
}

// fcovResponder returns a RoundTripper that always answers with the given status
// and body, recording the last request it saw.
func fcovResponder(status int, body string, seen *http.Request) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		if seen != nil {
			*seen = *r
		}
		return jsonResponse(status, body, nil), nil
	}
}

func TestFilesystemReadBytesMapsNotFound(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusNotFound, `{"message":"missing"}`, nil), "0.5.2")

	// Act
	_, err := f.ReadBytes(context.Background(), "/f")

	// Assert
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("ReadBytes error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestFilesystemReadStreamSuccessAndClose(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, "hello-stream", nil), "0.5.2")

	// Act
	reader, err := f.ReadStream(context.Background(), "/f")
	if err != nil {
		t.Fatalf("ReadStream: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Assert
	if string(data) != "hello-stream" {
		t.Fatalf("data = %q", data)
	}
}

func TestFilesystemReadStreamErrorClosesBody(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusUnauthorized, "denied", nil), "0.5.2")

	// Act
	_, err := f.ReadStream(context.Background(), "/f")

	// Assert
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("ReadStream error = %T %v, want *AuthenticationError", err, err)
	}
}

func TestFilesystemFileStreamReaderCloseNilSafe(t *testing.T) {
	// Arrange / Act / Assert: nil receiver and nil body must be no-ops.
	var nilReader *FileStreamReader
	if err := nilReader.Close(); err != nil {
		t.Fatalf("nil reader Close = %v", err)
	}
	empty := &FileStreamReader{}
	if err := empty.Close(); err != nil {
		t.Fatalf("empty reader Close = %v", err)
	}
}

func TestFilesystemWriteTextUsesDefaultUserOnOldVersion(t *testing.T) {
	// Arrange: version < 0.4.0 forces the implicit "user" default in fileRequest.
	var seen http.Request
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `[{"name":"f","path":"/f","type":"file"}]`, &seen), "0.3.0")

	// Act
	info, err := f.WriteText(context.Background(), "/f", "content")
	if err != nil {
		t.Fatalf("WriteText: %v", err)
	}

	// Assert
	if info.Name != "f" {
		t.Fatalf("info = %#v", info)
	}
	if got := seen.URL.Query().Get("username"); got != "user" {
		t.Fatalf("username = %q, want default \"user\"", got)
	}
}

func TestFilesystemWriteUsesExplicitUserQuery(t *testing.T) {
	// Arrange
	var seen http.Request
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `[{"name":"f","path":"/f","type":"file"}]`, &seen), "0.5.2")

	// Act
	if _, err := f.WriteBytes(context.Background(), "/f", []byte("x"), WithFileUser("alice")); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}

	// Assert
	if got := seen.URL.Query().Get("username"); got != "alice" {
		t.Fatalf("username = %q, want alice", got)
	}
}

func TestFilesystemWriteRejectsUnexpectedResultCount(t *testing.T) {
	// Arrange: a single write that returns two entries must fail.
	body := `[{"name":"a","path":"/a","type":"file"},{"name":"b","path":"/b","type":"file"}]`
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, body, nil), "0.5.2")

	// Act
	_, err := f.Write(context.Background(), "/f", strings.NewReader("x"))

	// Assert
	var sboxErr *SandboxError
	if !errors.As(err, &sboxErr) || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("Write error = %T %v", err, err)
	}
}

func TestFilesystemWriteFilesEmptyReturnsNil(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `[]`, nil), "0.5.2")

	// Act
	results, err := f.WriteFiles(context.Background(), nil)

	// Assert
	if err != nil || results != nil {
		t.Fatalf("WriteFiles = %v, %v", results, err)
	}
}

func TestFilesystemWriteFilesRejectsInvalidMetadata(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `[]`, nil), "0.6.4")

	// Act
	_, err := f.WriteFiles(context.Background(), []WriteEntry{{Path: "/f", Data: strings.NewReader("x")}},
		WithFileMetadata(map[string]string{"bad key": "v"}))

	// Assert
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("error = %T %v, want *InvalidArgumentError", err, err)
	}
}

func TestFilesystemWriteFilesMetadataRequiresNewerVersion(t *testing.T) {
	// Arrange: metadata needs envd >= 0.6.2.
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `[]`, nil), "0.6.1")

	// Act
	_, err := f.WriteFiles(context.Background(), []WriteEntry{{Path: "/f", Data: strings.NewReader("x")}},
		WithFileMetadata(map[string]string{"key": "value"}))

	// Assert
	var tmplErr *TemplateError
	if !errors.As(err, &tmplErr) {
		t.Fatalf("error = %T %v, want *TemplateError", err, err)
	}
}

func TestFilesystemWriteOctetStreamWithMetadata(t *testing.T) {
	// Arrange: octet-stream upload requires envd >= 0.5.7.
	var seen http.Request
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `[{"name":"f","path":"/f","type":"file"}]`, &seen), "0.6.4")

	// Act
	results, err := f.WriteBytes(context.Background(), "/f", []byte("payload"),
		WithOctetStreamUpload(true),
		WithFileMetadata(map[string]string{"env": "prod"}))
	if err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}

	// Assert
	if results.Name != "f" {
		t.Fatalf("results = %#v", results)
	}
	if ct := seen.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if md := seen.Header.Get("X-Metadata-env"); md != "prod" {
		t.Fatalf("X-Metadata-env = %q", md)
	}
}

func TestFilesystemWriteOctetStreamGzipEncodesBody(t *testing.T) {
	// Arrange: gzip on a single file forces the octet path and gzip encoding.
	var captured []byte
	var encoding string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		captured, _ = io.ReadAll(r.Body)
		encoding = r.Header.Get("Content-Encoding")
		return jsonResponse(http.StatusOK, `[{"name":"f","path":"/f","type":"file"}]`, nil), nil
	})
	f := fcovFilesystem(t, transport, "0.6.4")

	// Act
	if _, err := f.WriteBytes(context.Background(), "/f", []byte("hello gzip"), WithGzip(true)); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}

	// Assert
	if encoding != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", encoding)
	}
	zr, err := gzip.NewReader(bytes.NewReader(captured))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if string(decoded) != "hello gzip" {
		t.Fatalf("decoded body = %q", decoded)
	}
}

func TestFilesystemWriteOctetErrorMapsStatus(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusInsufficientStorage, `{"message":"full"}`, nil), "0.6.4")

	// Act
	_, err := f.WriteBytes(context.Background(), "/f", []byte("x"), WithOctetStreamUpload(true))

	// Assert
	var spaceErr *NotEnoughSpaceError
	if !errors.As(err, &spaceErr) {
		t.Fatalf("error = %T %v, want *NotEnoughSpaceError", err, err)
	}
}

func TestFilesystemWriteMultipartErrorMapsStatus(t *testing.T) {
	// Arrange: default version keeps the multipart path.
	f := fcovFilesystem(t, fcovResponder(http.StatusBadRequest, `{"message":"bad"}`, nil), "0.5.2")

	// Act
	_, err := f.WriteBytes(context.Background(), "/f", []byte("x"))

	// Assert
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("error = %T %v, want *InvalidArgumentError", err, err)
	}
}

func TestFilesystemWriteMultipartDecodeError(t *testing.T) {
	// Arrange: 2xx but non-JSON body triggers a decode error.
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `not-json`, nil), "0.5.2")

	// Act
	_, err := f.WriteBytes(context.Background(), "/f", []byte("x"))

	// Assert
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestFilesystemWriteFilesMultipleUsesMultipart(t *testing.T) {
	// Arrange
	var contentType string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		contentType = r.Header.Get("Content-Type")
		_, _ = io.Copy(io.Discard, r.Body)
		return jsonResponse(http.StatusOK, `[{"name":"a","path":"/a","type":"file"},{"name":"b","path":"/b","type":"file"}]`, nil), nil
	})
	f := fcovFilesystem(t, transport, "0.6.4")

	// Act
	results, err := f.WriteFiles(context.Background(), []WriteEntry{
		{Path: "/a", Data: strings.NewReader("a")},
		{Path: "/b", Data: strings.NewReader("b")},
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}

	// Assert
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart", contentType)
	}
}

func TestFilesystemListRejectsInvalidDepth(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `{"entries":[]}`, nil), "0.5.2")

	// Act
	_, err := f.List(context.Background(), "/dir", WithListDepth(0))

	// Assert
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("error = %T %v, want *InvalidArgumentError", err, err)
	}
}

func TestFilesystemListDecodesEntriesAndSkipsUnknownType(t *testing.T) {
	// Arrange: one valid entry and one with an unrecognized type (dropped).
	body := `{"entries":[{"name":"a","path":"/a","type":"FILE_TYPE_FILE","size":3},{"name":"b","path":"/b","type":"weird"}]}`
	var seen http.Request
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, body, &seen), "0.5.2")

	// Act
	entries, err := f.List(context.Background(), "/dir", WithListDepth(2))
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Assert
	if len(entries) != 1 || entries[0].Type != FileTypeFile || entries[0].Size != 3 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestFilesystemListMapsError(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusNotFound, `{}`, nil), "0.5.2")

	// Act
	_, err := f.List(context.Background(), "/dir")

	// Assert
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestFilesystemGetInfoSuccess(t *testing.T) {
	// Arrange
	body := `{"entry":{"name":"a","path":"/a","type":"dir","mode":493,"permissions":"drwxr-xr-x"}}`
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, body, nil), "0.5.2")

	// Act
	info, err := f.GetInfo(context.Background(), "/a")
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}

	// Assert
	if info.Type != FileTypeDir || info.Permissions != "drwxr-xr-x" {
		t.Fatalf("info = %#v", info)
	}
}

func TestFilesystemGetInfoUnknownTypeErrors(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `{"entry":{"name":"a","path":"/a","type":"mystery"}}`, nil), "0.5.2")

	// Act
	_, err := f.GetInfo(context.Background(), "/a")

	// Assert
	var sboxErr *SandboxError
	if !errors.As(err, &sboxErr) {
		t.Fatalf("error = %T %v, want *SandboxError", err, err)
	}
}

func TestFilesystemGetInfoMapsError(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusNotFound, `{}`, nil), "0.5.2")

	// Act
	_, err := f.GetInfo(context.Background(), "/a")

	// Assert
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestFilesystemExistsTrueFalseAndError(t *testing.T) {
	// Arrange / Act / Assert: exists.
	exists := fcovFilesystem(t, fcovResponder(http.StatusOK, `{"entry":{"name":"a","path":"/a","type":"file"}}`, nil), "0.5.2")
	if ok, err := exists.Exists(context.Background(), "/a"); err != nil || !ok {
		t.Fatalf("Exists = %v, %v, want true", ok, err)
	}

	// not found -> false, nil
	missing := fcovFilesystem(t, fcovResponder(http.StatusNotFound, `{}`, nil), "0.5.2")
	if ok, err := missing.Exists(context.Background(), "/a"); err != nil || ok {
		t.Fatalf("Exists = %v, %v, want false,nil", ok, err)
	}

	// other error -> propagated
	boom := fcovFilesystem(t, fcovResponder(http.StatusUnauthorized, `denied`, nil), "0.5.2")
	if _, err := boom.Exists(context.Background(), "/a"); err == nil {
		t.Fatal("expected propagated error")
	}
}

func TestFilesystemRemoveSuccessAndError(t *testing.T) {
	// Arrange / Act / Assert: success.
	ok := fcovFilesystem(t, fcovResponder(http.StatusOK, ``, nil), "0.5.2")
	if err := ok.Remove(context.Background(), "/a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// error
	bad := fcovFilesystem(t, fcovResponder(http.StatusNotFound, `{}`, nil), "0.5.2")
	err := bad.Remove(context.Background(), "/a")
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("Remove error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestFilesystemRenameSuccess(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `{"entry":{"name":"new","path":"/new","type":"file"}}`, nil), "0.5.2")

	// Act
	info, err := f.Rename(context.Background(), "/old", "/new")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Assert
	if info.Path != "/new" {
		t.Fatalf("info = %#v", info)
	}
}

func TestFilesystemRenameUnknownTypeErrors(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `{"entry":{"name":"x","path":"/x","type":"???"}}`, nil), "0.5.2")

	// Act
	_, err := f.Rename(context.Background(), "/old", "/new")

	// Assert
	var sboxErr *SandboxError
	if !errors.As(err, &sboxErr) {
		t.Fatalf("error = %T %v, want *SandboxError", err, err)
	}
}

func TestFilesystemRenameMapsError(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusNotFound, `{}`, nil), "0.5.2")

	// Act
	_, err := f.Rename(context.Background(), "/old", "/new")

	// Assert
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestFilesystemMakeDirCreatesAndDetectsExisting(t *testing.T) {
	// Arrange / Act / Assert: created.
	created := fcovFilesystem(t, fcovResponder(http.StatusOK, ``, nil), "0.5.2")
	if made, err := created.MakeDir(context.Background(), "/d"); err != nil || !made {
		t.Fatalf("MakeDir = %v, %v, want true", made, err)
	}

	// already exists -> false, nil (matches "already_exists" branch)
	existing := fcovFilesystem(t, fcovResponder(http.StatusBadRequest, `{"code":"failed_precondition","message":"path already_exists"}`, nil), "0.5.2")
	if made, err := existing.MakeDir(context.Background(), "/d"); err != nil || made {
		t.Fatalf("MakeDir = %v, %v, want false,nil", made, err)
	}

	// already exists (human phrasing) -> false, nil
	existing2 := fcovFilesystem(t, fcovResponder(http.StatusBadRequest, `{"code":"failed_precondition","message":"dir already exists"}`, nil), "0.5.2")
	if made, err := existing2.MakeDir(context.Background(), "/d"); err != nil || made {
		t.Fatalf("MakeDir(existing2) = %v, %v, want false,nil", made, err)
	}
}

func TestFilesystemMakeDirMapsError(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusNotFound, `{}`, nil), "0.5.2")

	// Act
	_, err := f.MakeDir(context.Background(), "/d")

	// Assert
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestFilesystemWatchDirVersionGates(t *testing.T) {
	// Arrange: each capability has a minimum envd version.
	tests := []struct {
		name    string
		version string
		opts    []WatchOption
	}{
		{"recursive", "0.1.3", []WatchOption{WithRecursiveWatch(true)}},
		{"includeEntry", "0.6.2", []WatchOption{WithWatchEntryInfo(true)}},
		{"networkMounts", "0.6.3", []WatchOption{WithWatchNetworkMounts(true)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := fcovFilesystem(t, fcovResponder(http.StatusOK, `{"watcherId":"w"}`, nil), tc.version)

			// Act
			_, err := f.WatchDir(context.Background(), "/dir", tc.opts...)

			// Assert
			var tmplErr *TemplateError
			if !errors.As(err, &tmplErr) {
				t.Fatalf("error = %T %v, want *TemplateError", err, err)
			}
		})
	}
}

func TestFilesystemWatchDirSuccess(t *testing.T) {
	// Arrange
	var seen http.Request
	f := fcovFilesystem(t, fcovResponder(http.StatusOK, `{"watcherId":"watch-123"}`, &seen), "0.6.4")

	// Act
	handle, err := f.WatchDir(context.Background(), "/dir",
		WithWatchUser("bob"),
		WithRecursiveWatch(true),
		WithWatchEntryInfo(true),
		WithWatchNetworkMounts(true),
		WithWatchRequestTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("WatchDir: %v", err)
	}

	// Assert
	if handle.WatcherID() != "watch-123" {
		t.Fatalf("WatcherID = %q", handle.WatcherID())
	}
	if got := seen.Header.Get(keepalivePingHeader); got == "" {
		t.Fatalf("missing keepalive ping header")
	}
}

func TestFilesystemWatchDirMapsError(t *testing.T) {
	// Arrange
	f := fcovFilesystem(t, fcovResponder(http.StatusNotFound, `{}`, nil), "0.6.4")

	// Act
	_, err := f.WatchDir(context.Background(), "/dir")

	// Assert
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestFilesystemHandleEnvdHTTPErrorMapping(t *testing.T) {
	// Arrange / Act / Assert: exhaustively map status codes to error types.
	// call closes the synthetic response body so the linter's bodyclose check
	// is satisfied while still exercising handleEnvdHTTPError.
	call := func(status int, body string, fileErrors bool) error {
		res := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
		defer func() { _ = res.Body.Close() }()
		return handleEnvdHTTPError(res, fileErrors)
	}

	if err := call(http.StatusOK, "", true); err != nil {
		t.Fatalf("2xx error = %v", err)
	}

	var argErr *InvalidArgumentError
	if err := call(http.StatusBadRequest, `{"message":"bad"}`, true); !errors.As(err, &argErr) || err.Error() != "bad" {
		t.Fatalf("400 = %T %v", err, err)
	}

	var authErr *AuthenticationError
	if err := call(http.StatusUnauthorized, "no", true); !errors.As(err, &authErr) {
		t.Fatalf("401 = %T %v", err, err)
	}

	var fnf *FileNotFoundError
	if err := call(http.StatusNotFound, "gone", true); !errors.As(err, &fnf) {
		t.Fatalf("404 fileErrors = %T %v", err, err)
	}

	var nf *NotFoundError
	if err := call(http.StatusNotFound, "gone", false); !errors.As(err, &nf) {
		t.Fatalf("404 = %T %v", err, err)
	}

	var rlErr *RateLimitError
	if err := call(http.StatusTooManyRequests, "slow", true); !errors.As(err, &rlErr) {
		t.Fatalf("429 = %T %v", err, err)
	}

	var toErr *TimeoutError
	if err := call(http.StatusBadGateway, "gateway", true); !errors.As(err, &toErr) {
		t.Fatalf("502 = %T %v", err, err)
	}

	var spaceErr *NotEnoughSpaceError
	if err := call(http.StatusInsufficientStorage, "full", true); !errors.As(err, &spaceErr) {
		t.Fatalf("507 = %T %v", err, err)
	}

	var sboxErr *SandboxError
	if err := call(http.StatusTeapot, "weird", true); !errors.As(err, &sboxErr) {
		t.Fatalf("default = %T %v", err, err)
	}
}

func TestFilesystemMapFilesystemError(t *testing.T) {
	// Arrange / Act / Assert
	if err := mapFilesystemError(nil); err != nil {
		t.Fatalf("nil = %v", err)
	}

	var fnf *FileNotFoundError
	if err := mapFilesystemError(&NotFoundError{Message: "x"}); !errors.As(err, &fnf) {
		t.Fatalf("NotFound = %T %v", err, err)
	}

	other := &InvalidArgumentError{Message: "y"}
	if err := mapFilesystemError(other); err != other {
		t.Fatalf("passthrough = %v", err)
	}
}

func TestFilesystemMapProtoFileType(t *testing.T) {
	// Arrange / Act / Assert
	cases := []struct {
		in   any
		want FileType
		ok   bool
	}{
		{"FILE_TYPE_FILE", FileTypeFile, true},
		{"file", FileTypeFile, true},
		{"FILE_TYPE_DIRECTORY", FileTypeDir, true},
		{"dir", FileTypeDir, true},
		{"nope", "", false},
		{float64(1), FileTypeFile, true},
		{float64(2), FileTypeDir, true},
		{float64(9), "", false},
		{nil, "", false},
	}
	for _, c := range cases {
		got, ok := mapProtoFileType(c.in)
		if got != c.want || ok != c.ok {
			t.Fatalf("mapProtoFileType(%v) = %v,%v want %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFilesystemValidateMetadata(t *testing.T) {
	// Arrange / Act / Assert
	if err := validateMetadata(map[string]string{"Good-Key": "printable value"}); err != nil {
		t.Fatalf("valid = %v", err)
	}

	var argErr *InvalidArgumentError
	if err := validateMetadata(map[string]string{"bad key": "v"}); !errors.As(err, &argErr) {
		t.Fatalf("bad key = %T %v", err, err)
	}
	if err := validateMetadata(map[string]string{"key": "bad\nvalue"}); !errors.As(err, &argErr) {
		t.Fatalf("bad value = %T %v", err, err)
	}
}

func TestFilesystemMetadataHeaders(t *testing.T) {
	// Arrange / Act
	headers := metadataHeaders(map[string]string{"env": "prod"})

	// Assert
	if headers["X-Metadata-env"] != "prod" {
		t.Fatalf("headers = %#v", headers)
	}
	if len(metadataHeaders(nil)) != 0 {
		t.Fatalf("empty metadata should yield no headers")
	}
}

func TestFilesystemWatchOptionsHandshakeTimeout(t *testing.T) {
	// Arrange / Act / Assert: unset -> nil, explicit 0 -> non-nil disabled.
	if got := watchOptionsFrom().handshakeTimeout(); got != nil {
		t.Fatalf("unset handshakeTimeout = %v, want nil", got)
	}
	explicit := watchOptionsFrom(WithWatchRequestTimeout(0)).handshakeTimeout()
	if explicit == nil || *explicit != 0 {
		t.Fatalf("explicit-0 handshakeTimeout = %v, want non-nil 0", explicit)
	}
}
