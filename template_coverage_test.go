package e2b

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// tcovChdir switches into dir for the duration of the test and restores the
// previous working directory on cleanup.
func tcovChdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

// tcovTarNames returns the sorted list of entry names inside a gzip+tar payload.
func tcovTarNames(t *testing.T, payload []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		names = append(names, header.Name)
	}
	return names
}

func TestTemplateBuilderMethods(t *testing.T) {
	// Arrange & Act
	tpl := NewTemplate().
		FromBaseTemplate("base").
		RunCmd("echo hi").
		SetEnv("KEY", "VALUE").
		Workdir("/app").
		User("root").
		Copy("src", "dest").
		WithStartCmd("start").
		WithReadyCmd("ready")

	// Assert
	if tpl.FromTemplate != "base" {
		t.Fatalf("FromTemplate = %q", tpl.FromTemplate)
	}
	if tpl.StartCmd != "start" || tpl.ReadyCmd != "ready" {
		t.Fatalf("start/ready = %q/%q", tpl.StartCmd, tpl.ReadyCmd)
	}
	want := []TemplateInstruction{
		{Type: InstructionRun, Args: []string{"echo hi"}},
		{Type: InstructionEnv, Args: []string{"KEY", "VALUE"}},
		{Type: InstructionWorkdir, Args: []string{"/app"}},
		{Type: InstructionUser, Args: []string{"root"}},
		{Type: InstructionCopy, Args: []string{"src", "dest"}},
	}
	if len(tpl.Steps) != len(want) {
		t.Fatalf("steps len = %d, want %d", len(tpl.Steps), len(want))
	}
	for i, step := range tpl.Steps {
		if step.Type != want[i].Type || strings.Join(step.Args, ",") != strings.Join(want[i].Args, ",") {
			t.Fatalf("step[%d] = %#v, want %#v", i, step, want[i])
		}
	}
}

func TestTemplateBuildOptionsFromDefaultsAndOverrides(t *testing.T) {
	// Arrange & Act: defaults with a nil option mixed in.
	defaults := templateBuildOptionsFrom(nil)
	// Assert defaults.
	if defaults.cpuCount != 2 || defaults.memoryMB != 1024 || defaults.pollPeriod != 200*time.Millisecond {
		t.Fatalf("defaults = %#v", defaults)
	}
	if defaults.skipCache || len(defaults.tags) != 0 {
		t.Fatalf("defaults skipCache/tags = %#v", defaults)
	}

	// Act: full overrides. Header options snapshot the input map immediately,
	// matching the upstream SDK's per-call connection configuration.
	headers := map[string]string{"x-build-mode": "micro"}
	headerOption := WithTemplateAPIHeaders(headers)
	headers["x-build-mode"] = "mutated"
	got := templateBuildOptionsFrom(
		WithTemplateTags("a", "b"),
		WithTemplateCPUCount(4),
		WithTemplateMemoryMB(4096),
		WithTemplateSkipCache(true),
		WithTemplatePollPeriod(time.Millisecond),
		headerOption,
	)
	// Assert overrides.
	if got.cpuCount != 4 || got.memoryMB != 4096 || !got.skipCache {
		t.Fatalf("overrides = %#v", got)
	}
	if got.pollPeriod != time.Millisecond {
		t.Fatalf("pollPeriod = %v", got.pollPeriod)
	}
	if strings.Join(got.tags, ",") != "a,b" {
		t.Fatalf("tags = %#v", got.tags)
	}
	if got.apiHeaders["x-build-mode"] != "micro" {
		t.Fatalf("headers = %#v", got.apiHeaders)
	}
	alias := templateBuildOptionsFrom(WithTemplateHeaders(map[string]string{"X-Alias": "ok"}))
	if alias.apiHeaders["X-Alias"] != "ok" {
		t.Fatalf("deprecated header alias = %#v", alias.apiHeaders)
	}
}

// TestTemplateBuildHeadersAreScopedToBuild verifies that headers passed via
// WithTemplateAPIHeaders are applied to every request of that build (create,
// trigger, poll) but do not leak into later unrelated calls, which keep the
// client's global header value.
func TestTemplateBuildHeadersAreScopedToBuild(t *testing.T) {
	const headerName = "X-E2B-Template-Build-Mode"
	var statusCalls int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/v3/templates":
			if got := r.Header.Get(headerName); got != "micro" {
				t.Fatalf("request build header = %q", got)
			}
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.URL.Path == "/v2/templates/tpl/builds/bld":
			if got := r.Header.Get(headerName); got != "micro" {
				t.Fatalf("trigger build header = %q", got)
			}
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		case strings.HasSuffix(r.URL.Path, "/status"):
			statusCalls++
			if statusCalls == 1 {
				if got := r.Header.Get(headerName); got != "micro" {
					t.Fatalf("build polling header = %q", got)
				}
			} else if got := r.Header.Get(headerName); got != "global" {
				t.Fatalf("build header leaked to unrelated request: %q", got)
			}
			return jsonResponse(http.StatusOK, `{"status":"ready","logEntries":[]}`, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHeader(strings.ToLower(headerName), "global"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	headers := map[string]string{headerName: "micro"}
	_, err = client.BuildTemplate(
		context.Background(),
		NewTemplate().FromImage("ubuntu:24.04"),
		"scoped",
		WithTemplateAPIHeaders(headers),
		WithTemplatePollPeriod(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	headers[headerName] = "mutated"
	if _, err := client.GetBuildStatus(context.Background(), "tpl", "bld", 0); err != nil {
		t.Fatalf("GetBuildStatus: %v", err)
	}
}

// TestGetBuildStatusWithOptionsUsesScopedHeaders verifies that
// GetBuildStatusWithOptions sends the headers from WithTemplateAPIHeaders and
// snapshots them when the option is created, so later mutation of the caller's
// map does not affect the request.
func TestGetBuildStatusWithOptionsUsesScopedHeaders(t *testing.T) {
	const headerName = "X-E2B-Template-Build-Mode"
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/templates/tpl/builds/bld/status" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get(headerName); got != "micro" {
			t.Fatalf("status build header = %q", got)
		}
		return jsonResponse(http.StatusOK, `{"status":"building","logEntries":[]}`, nil), nil
	})

	client := mustTestClient(t, transport)
	headers := map[string]string{headerName: "micro"}
	option := WithTemplateAPIHeaders(headers)
	headers[headerName] = "mutated"
	if _, err := client.GetBuildStatusWithOptions(context.Background(), "tpl", "bld", 0, option); err != nil {
		t.Fatalf("GetBuildStatusWithOptions: %v", err)
	}
}

// TestTemplateBuildHeadersCoverCopyControlPlaneOnly verifies that build-scoped
// headers are attached to the COPY control-plane requests (file-upload
// negotiation) but not to the presigned file upload itself.
func TestTemplateBuildHeadersCoverCopyControlPlaneOnly(t *testing.T) {
	const headerName = "X-E2B-Template-Build-Mode"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "copy.txt"), []byte("copy\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	tcovChdir(t, dir)

	var uploadSeen bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/templates":
			if got := r.Header.Get(headerName); got != "micro" {
				t.Fatalf("request build header = %q", got)
			}
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/templates/tpl/files/"):
			if got := r.Header.Get(headerName); got != "micro" {
				t.Fatalf("file lookup header = %q", got)
			}
			return jsonResponse(http.StatusOK, `{"present":false,"url":"https://upload.test/context"}`, nil), nil
		case r.Method == http.MethodPut && r.URL.Host == "upload.test":
			uploadSeen = true
			if got := r.Header.Get(headerName); got != "" {
				t.Fatalf("control-plane header leaked to upload URL: %q", got)
			}
			return jsonResponse(http.StatusOK, ``, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/v2/templates/tpl/builds/bld":
			if got := r.Header.Get(headerName); got != "micro" {
				t.Fatalf("trigger build header = %q", got)
			}
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	if _, err := client.BuildTemplateInBackground(
		context.Background(),
		NewTemplate().FromImage("ubuntu:24.04").Copy("copy.txt", "/tmp/copy.txt"),
		"copy",
		WithTemplateAPIHeaders(map[string]string{headerName: "micro"}),
	); err != nil {
		t.Fatalf("BuildTemplateInBackground: %v", err)
	}
	if !uploadSeen {
		t.Fatal("expected presigned upload")
	}
}

func TestBuildTemplatePollsUntilReady(t *testing.T) {
	var statusCalls int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/templates":
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/v2/templates/tpl/builds/bld":
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		case r.Method == http.MethodGet && r.URL.Path == "/templates/tpl/builds/bld/status":
			n := atomic.AddInt32(&statusCalls, 1)
			if n == 1 {
				if got := r.URL.Query().Get("logsOffset"); got != "0" {
					t.Fatalf("first logsOffset = %q", got)
				}
				return jsonResponse(http.StatusOK, `{"status":"building","logEntries":[{"level":"info","message":"x"}]}`, nil), nil
			}
			// Second poll should carry the accumulated offset (1 log entry).
			if got := r.URL.Query().Get("logsOffset"); got != "1" {
				t.Fatalf("second logsOffset = %q", got)
			}
			return jsonResponse(http.StatusOK, `{"status":"ready","logEntries":[]}`, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	build, err := client.BuildTemplate(
		context.Background(),
		NewTemplate().FromImage("ubuntu:24.04"),
		"poll",
		WithTemplatePollPeriod(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	if build.TemplateID != "tpl" || build.BuildID != "bld" {
		t.Fatalf("build = %#v", build)
	}
	if atomic.LoadInt32(&statusCalls) != 2 {
		t.Fatalf("status calls = %d", statusCalls)
	}
}

func TestBuildTemplateStatusErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusBody string
		wantMsg    string
	}{
		{
			name:       "reason message",
			statusBody: `{"status":"error","reason":{"message":"boom"}}`,
			wantMsg:    "boom",
		},
		{
			name:       "no reason falls back",
			statusBody: `{"status":"error"}`,
			wantMsg:    "build failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.URL.Path == "/v3/templates":
					return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
				case r.URL.Path == "/v2/templates/tpl/builds/bld":
					return jsonResponse(http.StatusNoContent, ``, nil), nil
				case strings.HasSuffix(r.URL.Path, "/status"):
					return jsonResponse(http.StatusOK, tt.statusBody, nil), nil
				default:
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
					return nil, nil
				}
			})

			client := mustTestClient(t, transport)
			_, err := client.BuildTemplate(
				context.Background(),
				NewTemplate().FromImage("ubuntu:24.04"),
				"err",
				WithTemplatePollPeriod(time.Millisecond),
			)
			var buildErr *BuildError
			if !errors.As(err, &buildErr) {
				t.Fatalf("error = %T %v", err, err)
			}
			if buildErr.Message != tt.wantMsg {
				t.Fatalf("message = %q, want %q", buildErr.Message, tt.wantMsg)
			}
		})
	}
}

func TestBuildTemplateGetStatusRequestError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/v3/templates":
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.URL.Path == "/v2/templates/tpl/builds/bld":
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		case strings.HasSuffix(r.URL.Path, "/status"):
			return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"down"}`, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	_, err := client.BuildTemplate(
		context.Background(),
		NewTemplate().FromImage("ubuntu:24.04"),
		"err",
		WithTemplatePollPeriod(time.Millisecond),
	)
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if !strings.Contains(buildErr.Message, "down") {
		t.Fatalf("message = %q", buildErr.Message)
	}
}

func TestBuildTemplateBackgroundRequestError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v3/templates" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		return jsonResponse(http.StatusBadRequest, `{"code":400,"message":"bad request"}`, nil), nil
	})

	client := mustTestClient(t, transport)
	_, err := client.BuildTemplate(context.Background(), NewTemplate().FromImage("ubuntu:24.04"), "x")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestBuildTemplateContextCancelledDuringPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/v3/templates":
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.URL.Path == "/v2/templates/tpl/builds/bld":
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		case strings.HasSuffix(r.URL.Path, "/status"):
			cancel() // cancel before the poll sleep so ctx.Done wins.
			return jsonResponse(http.StatusOK, `{"status":"building","logEntries":[]}`, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	_, err := client.BuildTemplate(
		ctx,
		NewTemplate().FromImage("ubuntu:24.04"),
		"cancel",
		WithTemplatePollPeriod(time.Hour),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestBuildTemplateInBackgroundNilTemplateSkipsFiles(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v3/templates" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld","tags":["t"]}`, nil), nil
	})

	client := mustTestClient(t, transport)
	info, err := client.BuildTemplateInBackground(context.Background(), nil, "no-template")
	if err != nil {
		t.Fatalf("BuildTemplateInBackground: %v", err)
	}
	if info.TemplateID != "tpl" || info.Name != "no-template" || info.Alias != "no-template" {
		t.Fatalf("info = %#v", info)
	}
	if len(info.Tags) != 1 || info.Tags[0] != "t" {
		t.Fatalf("tags = %#v", info.Tags)
	}
}

func TestBuildTemplateSkipCacheForcesBuild(t *testing.T) {
	var trigger map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/v3/templates":
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.URL.Path == "/v2/templates/tpl/builds/bld":
			if err := json.NewDecoder(r.Body).Decode(&trigger); err != nil {
				t.Fatalf("decode trigger: %v", err)
			}
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		case strings.HasSuffix(r.URL.Path, "/status"):
			return jsonResponse(http.StatusOK, `{"status":"ready","logEntries":[]}`, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	_, err := client.BuildTemplate(
		context.Background(),
		NewTemplate().FromImage("ubuntu:24.04"),
		"forced",
		WithTemplateSkipCache(true),
		WithTemplatePollPeriod(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	if force, _ := trigger["force"].(bool); !force {
		t.Fatalf("expected force=true, trigger = %#v", trigger)
	}
}

func TestBuildTemplatePrepareFilesErrorTriggersCleanup(t *testing.T) {
	tcovChdir(t, t.TempDir())
	var deleted bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/templates":
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.Method == http.MethodDelete && r.URL.Path == "/templates/tpl":
			deleted = true
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	// COPY of a non-existent file makes prepareTemplateFiles fail before upload.
	_, err := client.BuildTemplate(
		context.Background(),
		NewTemplate().FromImage("ubuntu:24.04").Copy("missing.txt", "/dest"),
		"cleanup",
	)
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if !strings.Contains(buildErr.Message, "no files found") {
		t.Fatalf("message = %q", buildErr.Message)
	}
	if !deleted {
		t.Fatal("expected cleanup DELETE")
	}
}

func TestBuildTemplateTriggerErrorTriggersCleanup(t *testing.T) {
	const headerName = "X-E2B-Template-Build-Mode"
	var deleted bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/templates":
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/v2/templates/tpl/builds/bld":
			return jsonResponse(http.StatusBadRequest, `{"code":400,"message":"steps are not supported"}`, nil), nil
		case r.Method == http.MethodDelete && r.URL.Path == "/templates/tpl":
			deleted = true
			if got := r.Header.Get(headerName); got != "micro" {
				t.Fatalf("cleanup build header = %q", got)
			}
			return jsonResponse(http.StatusOK, ``, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	_, err := client.BuildTemplate(
		context.Background(),
		NewTemplate().FromImage("ubuntu:24.04"),
		"trigger-fail",
		WithTemplateAPIHeaders(map[string]string{headerName: "micro"}),
	)
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if !strings.Contains(buildErr.Message, "steps are not supported") {
		t.Fatalf("message = %q", buildErr.Message)
	}
	if !deleted {
		t.Fatal("expected cleanup DELETE")
	}
}

func TestBuildTemplateCleanupFailureWrapsError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/templates":
			return jsonResponse(http.StatusAccepted, `{"templateID":"tpl","buildID":"bld"}`, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/v2/templates/tpl/builds/bld":
			return jsonResponse(http.StatusBadRequest, `{"code":400,"message":"boom"}`, nil), nil
		case r.Method == http.MethodDelete && r.URL.Path == "/templates/tpl":
			return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"delete failed"}`, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	_, err := client.BuildTemplate(context.Background(), NewTemplate().FromImage("ubuntu:24.04"), "wrap")
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if !strings.Contains(buildErr.Message, "failed to delete template") {
		t.Fatalf("message = %q", buildErr.Message)
	}
}

func TestDeleteTemplateAfterBuildStartFailureNotFound(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.URL.Path != "/templates/tpl" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		return jsonResponse(http.StatusNotFound, `{"code":404}`, nil), nil
	})

	client := mustTestClient(t, transport)
	// A 404 means the template is already gone; cleanup must treat that as success.
	if err := client.deleteTemplateAfterBuildStartFailure("tpl"); err != nil {
		t.Fatalf("deleteTemplateAfterBuildStartFailure: %v", err)
	}
}

func TestPrepareTemplateFilesUploadBranches(t *testing.T) {
	tests := []struct {
		name       string
		filesBody  string
		filesCode  int
		putCode    int
		wantErr    string
		wantUpload bool
	}{
		{name: "present skips upload", filesBody: `{"present":true}`, filesCode: http.StatusOK, wantUpload: false},
		{name: "empty url errors", filesBody: `{"present":false,"url":""}`, filesCode: http.StatusOK, wantErr: "upload URL is empty"},
		{name: "files lookup fails", filesBody: `{"code":500,"message":"down"}`, filesCode: http.StatusInternalServerError, wantErr: "500"},
		{name: "upload put fails", filesBody: `{"present":false,"url":"https://upload.test/x"}`, filesCode: http.StatusOK, putCode: http.StatusBadGateway, wantErr: "upload template files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "copy.txt"), []byte("hi\n"), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			tcovChdir(t, dir)

			var uploaded bool
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/templates/tpl/files/"):
					return jsonResponse(tt.filesCode, tt.filesBody, nil), nil
				case r.Method == http.MethodPut && r.URL.Host == "upload.test":
					uploaded = true
					return jsonResponse(tt.putCode, `boom`, nil), nil
				default:
					t.Fatalf("unexpected request: %s %s host=%s", r.Method, r.URL.RequestURI(), r.URL.Host)
					return nil, nil
				}
			})

			client := mustTestClient(t, transport)
			tpl := NewTemplate().FromImage("ubuntu:24.04").Copy("copy.txt", "/dest")
			body := *tpl
			body.Steps = append([]TemplateInstruction{}, tpl.Steps...)
			err := client.prepareTemplateFiles(context.Background(), "tpl", &body)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("prepareTemplateFiles: %v", err)
				}
				if uploaded != tt.wantUpload {
					t.Fatalf("uploaded = %v, want %v", uploaded, tt.wantUpload)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestPrepareTemplateFilesNilAndArgValidation(t *testing.T) {
	client := mustTestClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		return nil, nil
	}))

	// nil template is a no-op.
	if err := client.prepareTemplateFiles(context.Background(), "tpl", nil); err != nil {
		t.Fatalf("nil template: %v", err)
	}

	// COPY with a single argument must fail validation before any request.
	bad := &Template{Steps: []TemplateInstruction{{Type: InstructionCopy, Args: []string{"only"}}}}
	err := client.prepareTemplateFiles(context.Background(), "tpl", bad)
	if err == nil || !strings.Contains(err.Error(), "COPY requires source and destination") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateTemplateCopySource(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr bool
	}{
		{name: "ok relative", src: "dir/file.txt", wantErr: false},
		{name: "empty", src: "", wantErr: true},
		{name: "absolute", src: "/etc/passwd", wantErr: true},
		{name: "parent escape", src: "../outside", wantErr: true},
		{name: "bare parent", src: "..", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTemplateCopySource(tt.src)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnsureTemplateCopyWithinContext(t *testing.T) {
	ctxPath := t.TempDir()
	if err := ensureTemplateCopyWithinContext(filepath.Join(ctxPath, "a", "b.txt"), ctxPath); err != nil {
		t.Fatalf("within context: %v", err)
	}
	outside := filepath.Join(filepath.Dir(ctxPath), "escape.txt")
	if err := ensureTemplateCopyWithinContext(outside, ctxPath); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestTemplateCopyFilesVariants(t *testing.T) {
	ctxPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxPath, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ctxPath, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ctxPath, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ctxPath, "sub", "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatalf("write c.txt: %v", err)
	}

	// Single file.
	single, err := templateCopyFiles("a.txt", ctxPath)
	if err != nil || len(single) != 1 {
		t.Fatalf("single = %#v err=%v", single, err)
	}

	// Glob expansion.
	glob, err := templateCopyFiles("*.txt", ctxPath)
	if err != nil || len(glob) != 2 {
		t.Fatalf("glob = %#v err=%v", glob, err)
	}

	// Directory walk (includes the dir entry and its file).
	dir, err := templateCopyFiles("sub", ctxPath)
	if err != nil {
		t.Fatalf("dir: %v", err)
	}
	if len(dir) < 2 {
		t.Fatalf("dir walk = %#v", dir)
	}

	// No matches must error.
	if _, err := templateCopyFiles("does-not-exist.txt", ctxPath); err == nil {
		t.Fatal("expected no files error")
	}

	// Invalid source propagates validation error.
	if _, err := templateCopyFiles("../x", ctxPath); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestCalculateTemplateCopyHashAndTar(t *testing.T) {
	ctxPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxPath, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ctxPath, "dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ctxPath, "dir", "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	// Regular file + directory hashing is deterministic.
	h1, err := calculateTemplateCopyHash("file.txt", "/dest", ctxPath)
	if err != nil || h1 == "" {
		t.Fatalf("hash file: %v", err)
	}
	h1again, err := calculateTemplateCopyHash("file.txt", "/dest", ctxPath)
	if err != nil || h1again != h1 {
		t.Fatalf("hash not deterministic: %q vs %q err=%v", h1, h1again, err)
	}
	if _, err := calculateTemplateCopyHash("dir", "/dest", ctxPath); err != nil {
		t.Fatalf("hash dir: %v", err)
	}

	// Tar of the directory carries the nested file.
	tarball, err := createTemplateCopyTar("dir", ctxPath)
	if err != nil {
		t.Fatalf("tar dir: %v", err)
	}
	names := tcovTarNames(t, tarball)
	found := false
	for _, name := range names {
		if name == "dir/nested.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tar names = %#v", names)
	}

	// Validation errors surface from both helpers.
	if _, err := calculateTemplateCopyHash("", "/d", ctxPath); err == nil {
		t.Fatal("expected hash validation error")
	}
	if _, err := createTemplateCopyTar("", ctxPath); err == nil {
		t.Fatal("expected tar validation error")
	}
}

func TestCalculateTemplateCopyHashSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks not reliably supported on windows")
	}
	ctxPath := t.TempDir()
	target := filepath.Join(ctxPath, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(ctxPath, "link.txt")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if _, err := calculateTemplateCopyHash("link.txt", "/dest", ctxPath); err != nil {
		t.Fatalf("hash symlink: %v", err)
	}
	tarball, err := createTemplateCopyTar("link.txt", ctxPath)
	if err != nil {
		t.Fatalf("tar symlink: %v", err)
	}
	if names := tcovTarNames(t, tarball); len(names) == 0 {
		t.Fatal("expected symlink entry in tar")
	}
}

func TestGetBuildStatusRequestError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"nope"}`, nil), nil
	})
	client := mustTestClient(t, transport)
	_, err := client.GetBuildStatus(context.Background(), "tpl", "bld", 3)
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestListTemplatesError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.RawQuery != "" {
			t.Fatalf("expected no query, got %q", r.URL.RawQuery)
		}
		return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
	})
	client := mustTestClient(t, transport)
	_, err := client.ListTemplates(context.Background(), "")
	var tplErr *TemplateError
	if !errors.As(err, &tplErr) {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestGetTemplateErrors(t *testing.T) {
	t.Run("request error", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
		})
		client := mustTestClient(t, transport)
		_, err := client.GetTemplate(context.Background(), "tpl", 0, "")
		var tplErr *TemplateError
		if !errors.As(err, &tplErr) {
			t.Fatalf("error = %T %v", err, err)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{not json`, nil), nil
		})
		client := mustTestClient(t, transport)
		_, err := client.GetTemplate(context.Background(), "tpl", 0, "")
		if err == nil {
			t.Fatal("expected json decode error")
		}
	})
}

func TestDeleteTemplateRequestError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
	})
	client := mustTestClient(t, transport)
	_, err := client.DeleteTemplate(context.Background(), "tpl")
	var tplErr *TemplateError
	if !errors.As(err, &tplErr) {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestTemplateExistsStatuses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantExists bool
		wantErr    bool
	}{
		{name: "ok exists", statusCode: http.StatusOK, wantExists: true},
		{name: "forbidden exists", statusCode: http.StatusForbidden, wantExists: true},
		{name: "not found", statusCode: http.StatusNotFound, wantExists: false},
		{name: "server error", statusCode: http.StatusInternalServerError, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(tt.statusCode, `{}`, nil), nil
			})
			client := mustTestClient(t, transport)
			exists, err := client.TemplateExists(context.Background(), "alias")
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("TemplateExists: %v", err)
			}
			if exists != tt.wantExists {
				t.Fatalf("exists = %v, want %v", exists, tt.wantExists)
			}
		})
	}
}

func TestTemplateTagOperations(t *testing.T) {
	t.Run("assign success", func(t *testing.T) {
		var body map[string]any
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost || r.URL.Path != "/templates/tags" {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			return jsonResponse(http.StatusOK, `{"buildID":"bld","tags":["a"]}`, nil), nil
		})
		client := mustTestClient(t, transport)
		info, err := client.AssignTemplateTags(context.Background(), "target", []string{"a"})
		if err != nil {
			t.Fatalf("AssignTemplateTags: %v", err)
		}
		if info.BuildID != "bld" || len(info.Tags) != 1 || info.Tags[0] != "a" {
			t.Fatalf("info = %#v", info)
		}
		if body["target"] != "target" {
			t.Fatalf("body = %#v", body)
		}
	})

	t.Run("assign error", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
		})
		client := mustTestClient(t, transport)
		_, err := client.AssignTemplateTags(context.Background(), "target", []string{"a"})
		var tplErr *TemplateError
		if !errors.As(err, &tplErr) {
			t.Fatalf("error = %T %v", err, err)
		}
	})

	t.Run("remove success", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodDelete || r.URL.Path != "/templates/tags" {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			}
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		})
		client := mustTestClient(t, transport)
		if err := client.RemoveTemplateTags(context.Background(), "name", []string{"a"}); err != nil {
			t.Fatalf("RemoveTemplateTags: %v", err)
		}
	})

	t.Run("remove error", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
		})
		client := mustTestClient(t, transport)
		err := client.RemoveTemplateTags(context.Background(), "name", []string{"a"})
		var tplErr *TemplateError
		if !errors.As(err, &tplErr) {
			t.Fatalf("error = %T %v", err, err)
		}
	})

	t.Run("get success", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Path != "/templates/tpl/tags" {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			}
			return jsonResponse(http.StatusOK, `[{"tag":"a","buildID":"bld","createdAt":"2026-06-24T00:00:00Z"}]`, nil), nil
		})
		client := mustTestClient(t, transport)
		tags, err := client.GetTemplateTags(context.Background(), "tpl")
		if err != nil {
			t.Fatalf("GetTemplateTags: %v", err)
		}
		if len(tags) != 1 || tags[0].Tag != "a" || tags[0].BuildID != "bld" {
			t.Fatalf("tags = %#v", tags)
		}
	})

	t.Run("get error", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusInternalServerError, `{"code":500,"message":"boom"}`, nil), nil
		})
		client := mustTestClient(t, transport)
		_, err := client.GetTemplateTags(context.Background(), "tpl")
		var tplErr *TemplateError
		if !errors.As(err, &tplErr) {
			t.Fatalf("error = %T %v", err, err)
		}
	})
}

func TestUploadTemplateFilesSuccess(t *testing.T) {
	var contentType string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut || r.URL.Host != "upload.test" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		contentType = r.Header.Get("Content-Type")
		return jsonResponse(http.StatusOK, ``, nil), nil
	})
	client := mustTestClient(t, transport)
	if err := client.uploadTemplateFiles(context.Background(), "https://upload.test/x", []byte("payload")); err != nil {
		t.Fatalf("uploadTemplateFiles: %v", err)
	}
	if contentType != "application/gzip" {
		t.Fatalf("content-type = %q", contentType)
	}
}
