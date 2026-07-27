package e2b

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateCRUD(t *testing.T) {
	var seen []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if got := r.Header.Get("X-API-KEY"); got != "e2b_0123" {
			t.Fatalf("X-API-KEY = %q", got)
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/templates":
			if got := r.URL.Query().Get("teamID"); got != "team-1" {
				t.Fatalf("teamID query = %q", got)
			}
			return jsonResponse(http.StatusOK, `[
				{
					"templateID":"tpl-1",
					"aliases":["alias-1"],
					"names":["team/alias-1"],
					"public":false,
					"buildID":"build-1",
					"buildStatus":"ready",
					"buildCount":1,
					"spawnCount":2,
					"cpuCount":2,
					"memoryMB":2048,
					"diskSizeMB":1024,
					"envdVersion":"0.5.2",
					"createdAt":"2026-06-24T00:00:00Z",
					"updatedAt":"2026-06-24T00:01:00Z",
					"lastSpawnedAt":null
				}
			]`, nil), nil
		case r.Method == http.MethodGet && r.URL.Path == "/templates/tpl-1":
			if got := r.URL.Query().Get("limit"); got != "50" {
				t.Fatalf("limit query = %q", got)
			}
			if got := r.URL.Query().Get("nextToken"); got != "cursor-1" {
				t.Fatalf("nextToken query = %q", got)
			}
			return jsonResponse(http.StatusOK, `{
				"templateID":"tpl-1",
				"aliases":["alias-1"],
				"names":["team/alias-1"],
				"public":false,
				"spawnCount":2,
				"createdAt":"2026-06-24T00:00:00Z",
				"updatedAt":"2026-06-24T00:01:00Z",
				"lastSpawnedAt":null,
				"builds":[
					{
						"buildID":"build-1",
						"status":"ready",
						"cpuCount":2,
						"memoryMB":2048,
						"diskSizeMB":1024,
						"envdVersion":"0.5.2",
						"createdAt":"2026-06-24T00:00:00Z",
						"updatedAt":"2026-06-24T00:01:00Z"
					}
				]
			}`, http.Header{"x-next-token": []string{"cursor-2"}}), nil
		case r.Method == http.MethodGet && r.URL.Path == "/templates/aliases/alias-1":
			return jsonResponse(http.StatusOK, `{"templateID":"tpl-1"}`, nil), nil
		case r.Method == http.MethodDelete && r.URL.Path == "/templates/tpl-1":
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	ctx := context.Background()

	templates, err := client.ListTemplates(ctx, "team-1")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(templates) != 1 || templates[0].TemplateID != "tpl-1" || templates[0].Aliases[0] != "alias-1" {
		t.Fatalf("templates = %#v", templates)
	}

	template, err := client.GetTemplate(ctx, "tpl-1", 50, "cursor-1")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if template.TemplateID != "tpl-1" || len(template.Builds) != 1 || template.Builds[0].BuildID != "build-1" {
		t.Fatalf("template = %#v", template)
	}
	if !template.HasNext || template.NextToken != "cursor-2" {
		t.Fatalf("pagination = hasNext=%v next=%q", template.HasNext, template.NextToken)
	}

	exists, err := client.TemplateExists(ctx, "alias-1")
	if err != nil {
		t.Fatalf("TemplateExists: %v", err)
	}
	if !exists {
		t.Fatal("expected template alias to exist")
	}

	deleted, err := client.DeleteTemplate(ctx, "tpl-1")
	if err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if !deleted {
		t.Fatal("expected delete to return true")
	}

	want := []string{
		"GET /templates?teamID=team-1",
		"GET /templates/tpl-1?limit=50&nextToken=cursor-1",
		"GET /templates/aliases/alias-1",
		"DELETE /templates/tpl-1",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(seen, "\n"))
	}
}

func TestBuildTemplateSimpleFromImage(t *testing.T) {
	const image = "fc-e2b-registry.ap-southeast-1.cr.aliyuncs.com/runtime/base:v0.0.39"
	var requestBuild map[string]any
	var triggerBuild map[string]any

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("X-API-KEY"); got != "e2b_0123" {
			t.Fatalf("X-API-KEY = %q", got)
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/templates":
			if err := json.NewDecoder(r.Body).Decode(&requestBuild); err != nil {
				t.Fatalf("decode request build: %v", err)
			}
			return jsonResponse(http.StatusAccepted, `{
				"templateID":"tpl-simple",
				"buildID":"build-simple",
				"aliases":["my-swe-eval"],
				"names":["team/my-swe-eval"],
				"public":false,
				"tags":["sample"]
			}`, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/v2/templates/tpl-simple/builds/build-simple":
			if err := json.NewDecoder(r.Body).Decode(&triggerBuild); err != nil {
				t.Fatalf("decode trigger build: %v", err)
			}
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		case r.Method == http.MethodGet && r.URL.Path == "/templates/tpl-simple/builds/build-simple/status":
			if got := r.URL.Query().Get("logsOffset"); got != "0" {
				t.Fatalf("logsOffset query = %q", got)
			}
			return jsonResponse(http.StatusOK, `{
				"templateID":"tpl-simple",
				"buildID":"build-simple",
				"status":"ready",
				"logEntries":[],
				"logs":[]
			}`, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	build, err := client.BuildTemplate(
		context.Background(),
		NewTemplate().FromImage(image),
		"my-swe-eval-astropy_1776_astropy-12907-0624-1",
		WithTemplateCPUCount(2),
		WithTemplateMemoryMB(2048),
		WithTemplateTags("sample"),
	)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	if build.TemplateID != "tpl-simple" || build.BuildID != "build-simple" {
		t.Fatalf("build = %#v", build)
	}

	if requestBuild["name"] != "my-swe-eval-astropy_1776_astropy-12907-0624-1" {
		t.Fatalf("name = %#v", requestBuild["name"])
	}
	if requestBuild["cpuCount"].(float64) != 2 || requestBuild["memoryMB"].(float64) != 2048 {
		t.Fatalf("resources = %#v", requestBuild)
	}

	if triggerBuild["fromImage"] != image {
		t.Fatalf("fromImage = %#v", triggerBuild["fromImage"])
	}
	steps, ok := triggerBuild["steps"].([]any)
	if !ok || len(steps) != 0 {
		t.Fatalf("steps = %#v", triggerBuild["steps"])
	}
}

func TestBuildTemplateUploadsCopySources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "copy.txt"), []byte("hello copy\n"), 0o644); err != nil {
		t.Fatalf("write copy fixture: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	var triggerBuild map[string]any
	var uploadSeen bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/templates":
			return jsonResponse(http.StatusAccepted, `{
				"templateID":"tpl-copy",
				"buildID":"build-copy",
				"aliases":["copy"],
				"names":["copy"],
				"public":false
			}`, nil), nil
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/templates/tpl-copy/files/"):
			hash := strings.TrimPrefix(r.URL.Path, "/templates/tpl-copy/files/")
			if hash == "" {
				t.Fatal("empty files hash")
			}
			return jsonResponse(http.StatusCreated, `{"present":false,"url":"https://upload.test/layer.tar.gz"}`, nil), nil
		case r.Method == http.MethodPut && r.URL.Host == "upload.test":
			uploadSeen = true
			assertTarContains(t, r.Body, "copy.txt", "hello copy\n")
			return jsonResponse(http.StatusOK, ``, nil), nil
		case r.Method == http.MethodPost && r.URL.Path == "/v2/templates/tpl-copy/builds/build-copy":
			if err := json.NewDecoder(r.Body).Decode(&triggerBuild); err != nil {
				t.Fatalf("decode trigger build: %v", err)
			}
			return jsonResponse(http.StatusNoContent, ``, nil), nil
		case r.Method == http.MethodGet && r.URL.Path == "/templates/tpl-copy/builds/build-copy/status":
			return jsonResponse(http.StatusOK, `{
				"templateID":"tpl-copy",
				"buildID":"build-copy",
				"status":"ready",
				"logEntries":[],
				"logs":[]
			}`, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s host=%s", r.Method, r.URL.RequestURI(), r.URL.Host)
			return nil, nil
		}
	})

	client := mustTestClient(t, transport)
	_, err = client.BuildTemplate(
		context.Background(),
		NewTemplate().FromImage("ubuntu:24.04").Copy("copy.txt", "/tmp/copy.txt"),
		"copy-template",
	)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	if !uploadSeen {
		t.Fatal("expected COPY upload")
	}
	steps := triggerBuild["steps"].([]any)
	first := steps[0].(map[string]any)
	if first["filesHash"] == "" {
		t.Fatalf("COPY step missing filesHash: %#v", first)
	}
}

func TestDeleteTemplateNotFound(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.URL.Path != "/templates/missing" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		return jsonResponse(http.StatusNotFound, `{"code":404,"message":"not found"}`, nil), nil
	})

	client := mustTestClient(t, transport)
	deleted, err := client.DeleteTemplate(context.Background(), "missing")
	if err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if deleted {
		t.Fatal("expected false for missing template")
	}
}

func mustTestClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func jsonResponse(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = http.Header{}
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func assertTarContains(t *testing.T, body io.Reader, name, content string) {
	t.Helper()
	gz, err := gzip.NewReader(body)
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if header.Name != name {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar file: %v", err)
		}
		if string(data) != content {
			t.Fatalf("tar content = %q", string(data))
		}
		return
	}
	t.Fatalf("tar missing %s", name)
}
