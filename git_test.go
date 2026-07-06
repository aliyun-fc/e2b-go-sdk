package e2b

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseGitStatus(t *testing.T) {
	raw := "## main...origin/main [ahead 2, behind 1]\n M README.md\nA  new.go\nUU conflict.txt\n?? scratch.txt\n"
	status := parseGitStatus(raw)
	if status.Branch != "main" || status.Upstream != "origin/main" {
		t.Fatalf("branch/upstream = %q/%q", status.Branch, status.Upstream)
	}
	if status.Ahead != 2 || status.Behind != 1 {
		t.Fatalf("ahead/behind = %d/%d", status.Ahead, status.Behind)
	}
	if status.IsClean {
		t.Fatal("status should not be clean")
	}
	if len(status.Files) != 4 {
		t.Fatalf("files = %d", len(status.Files))
	}
	if len(status.Conflicts) != 1 || status.Conflicts[0].Path != "conflict.txt" {
		t.Fatalf("conflicts = %#v", status.Conflicts)
	}
}

func TestCloneDestinationFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/e2b-dev/sdk.git": "sdk",
		"git@github.com:e2b-dev/sdk.git":     "sdk",
		"https://github.com/e2b-dev/sdk":     "sdk",
	}
	for raw, want := range cases {
		if got := cloneDestinationFromURL(raw); got != want {
			t.Fatalf("cloneDestinationFromURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseGitBranchesClassifiesRemoteRefs(t *testing.T) {
	raw := "refs/heads/main\nrefs/heads/feature/x\nrefs/remotes/origin/main\nrefs/remotes/upstream/dev\n"
	branches := parseGitBranches("main\n", raw)
	if branches.Current != "main" {
		t.Fatalf("current = %q", branches.Current)
	}
	if len(branches.Local) != 2 || branches.Local[0] != "main" || branches.Local[1] != "feature/x" {
		t.Fatalf("local = %#v", branches.Local)
	}
	if len(branches.Remote) != 2 || branches.Remote[0] != "origin/main" || branches.Remote[1] != "upstream/dev" {
		t.Fatalf("remote = %#v", branches.Remote)
	}
}

func TestGitCloneSeparatesRepoURLFromOptions(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
		return g.Clone(context.Background(), "--upload-pack=touch /tmp/pwned", "")
	})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %#v", commands)
	}
	want := "'git' 'clone' '--' '--upload-pack=touch /tmp/pwned'"
	if commands[0] != want {
		t.Fatalf("clone command = %q, want %q", commands[0], want)
	}
}

func TestGitPathspecsUseEndOfOptions(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}, {}, {}}, func(g *Git) error {
		if err := g.Add(context.Background(), "/repo", []string{"--intent-to-add"}, false); err != nil {
			return err
		}
		if err := g.Reset(context.Background(), "/repo", GitResetHard, "HEAD", []string{"--path"}); err != nil {
			return err
		}
		return g.Restore(context.Background(), "/repo", []string{"--source"}, false, true, "")
	})
	if err != nil {
		t.Fatalf("git operations: %v", err)
	}
	want := []string{
		"'git' '-C' '/repo' 'add' '--' '--intent-to-add'",
		"'git' '-C' '/repo' 'reset' '--hard' 'HEAD' '--' '--path'",
		"'git' '-C' '/repo' 'restore' '--worktree' '--' '--source'",
	}
	if len(commands) != len(want) {
		t.Fatalf("commands = %#v", commands)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("command[%d] = %q, want %q", i, commands[i], want[i])
		}
	}
}

func TestGitRejectsOptionLikeBranchAndExtRemote(t *testing.T) {
	g := &Git{}
	err := g.CheckoutBranch(context.Background(), "/repo", "--orphan")
	var invalid *InvalidArgumentError
	if !errors.As(err, &invalid) {
		t.Fatalf("CheckoutBranch error = %T %v, want InvalidArgumentError", err, err)
	}
	err = g.Clone(context.Background(), "ext::sh -c 'touch /tmp/pwned'", "")
	if !errors.As(err, &invalid) {
		t.Fatalf("Clone error = %T %v, want InvalidArgumentError", err, err)
	}
}

func TestGitRejectsExtRemoteAcrossSurface(t *testing.T) {
	const ext = "ext::sh -c 'touch /tmp/pwned'"
	g := &Git{}
	var invalid *InvalidArgumentError
	cases := map[string]error{
		"RemoteAdd": g.RemoteAdd(context.Background(), "/repo", "origin", ext, false),
		"Push":      g.Push(context.Background(), "/repo", ext, "main", "", "", false),
		"Pull":      g.Pull(context.Background(), "/repo", ext, "main", "", ""),
	}
	for name, err := range cases {
		if !errors.As(err, &invalid) {
			t.Fatalf("%s error = %T %v, want InvalidArgumentError", name, err, err)
		}
	}
}

func TestGitRejectsOptionLikeConfigKey(t *testing.T) {
	g := &Git{}
	var invalid *InvalidArgumentError
	if err := g.SetConfig(context.Background(), "--global", "value", "", "/repo"); !errors.As(err, &invalid) {
		t.Fatalf("SetConfig error = %T %v, want InvalidArgumentError", err, err)
	}
	if _, err := g.GetConfig(context.Background(), "--global", "", "/repo"); !errors.As(err, &invalid) {
		t.Fatalf("GetConfig error = %T %v, want InvalidArgumentError", err, err)
	}
}

func TestDangerouslyAuthenticateWritesPlainNetrcLine(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
		return g.DangerouslyAuthenticate(context.Background(), "me", "pw", "github.com", "")
	})
	if err != nil {
		t.Fatalf("DangerouslyAuthenticate: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %#v", commands)
	}
	if !strings.Contains(commands[0], "machine github.com login me password pw") {
		t.Fatalf("netrc command does not contain plain netrc line: %q", commands[0])
	}
	if strings.Contains(commands[0], "machine 'github.com'") || strings.Contains(commands[0], "login 'me'") || strings.Contains(commands[0], "password 'pw'") {
		t.Fatalf("netrc command still writes shell quotes into ~/.netrc: %q", commands[0])
	}
}

func TestDangerouslyAuthenticateRejectsNewlines(t *testing.T) {
	g := &Git{}
	err := g.DangerouslyAuthenticate(context.Background(), "me", "pw\nmachine evil login a password b", "github.com", "")
	var invalid *InvalidArgumentError
	if !errors.As(err, &invalid) {
		t.Fatalf("DangerouslyAuthenticate error = %T %v, want InvalidArgumentError", err, err)
	}
}

func TestGitCredentialedRemoteReportsRestoreFailure(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{
		{stdout: "https://example.com/repo.git\n"},
		{},
		{},
		{stderr: "restore failed", exitCode: 1},
	}, func(g *Git) error {
		return g.Push(context.Background(), "/repo", "origin", "main", "user", "token", false)
	})
	if err == nil || !strings.Contains(err.Error(), "restore git remote URL") {
		t.Fatalf("Push error = %v, want restore failure", err)
	}
	if len(commands) != 4 {
		t.Fatalf("commands = %#v", commands)
	}
	if !strings.Contains(commands[3], "'remote' 'set-url' 'origin' 'https://example.com/repo.git'") {
		t.Fatalf("restore command = %q", commands[3])
	}
}

type gitCommandResponse struct {
	stdout   string
	stderr   string
	exitCode int
}

func captureGitCommands(t *testing.T, responses []gitCommandResponse, fn func(*Git) error) ([]string, error) {
	t.Helper()
	var commands []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/process.Process/Start" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read command request: %v", err)
		}
		if len(raw) < 5 {
			t.Fatalf("command request too short: %d", len(raw))
		}
		var request struct {
			Process struct {
				Args []string `json:"args"`
			} `json:"process"`
		}
		if err := json.Unmarshal(raw[5:], &request); err != nil {
			t.Fatalf("decode command request: %v", err)
		}
		if len(request.Process.Args) != 3 {
			t.Fatalf("process args = %#v", request.Process.Args)
		}
		commands = append(commands, request.Process.Args[2])
		response := gitCommandResponse{}
		if len(responses) > 0 {
			response = responses[0]
			responses = responses[1:]
		}
		return gitCommandHTTPResponse(t, response), nil
	})
	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: "0.5.2"}
	sandbox.Commands = newCommands(sandbox)
	git := newGit(sandbox.Commands)
	err = fn(git)
	return commands, err
}

func gitCommandHTTPResponse(t *testing.T, response gitCommandResponse) *http.Response {
	t.Helper()
	parts := [][]byte{testConnectEnvelope(t, `{"event":{"start":{"pid":42}}}`)}
	if response.stdout != "" || response.stderr != "" {
		payload := map[string]any{
			"event": map[string]any{
				"data": map[string]string{
					"stdout": base64.StdEncoding.EncodeToString([]byte(response.stdout)),
					"stderr": base64.StdEncoding.EncodeToString([]byte(response.stderr)),
				},
			},
		}
		parts = append(parts, testJSONConnectEnvelope(t, payload))
	}
	payload := map[string]any{
		"event": map[string]any{
			"end": map[string]any{
				"exitCode": response.exitCode,
			},
		},
	}
	parts = append(parts, testJSONConnectEnvelope(t, payload))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/connect+json"}},
		Body:       io.NopCloser(bytes.NewReader(bytes.Join(parts, nil))),
	}
}

func testJSONConnectEnvelope(t *testing.T, payload any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return testConnectEnvelope(t, string(raw))
}
