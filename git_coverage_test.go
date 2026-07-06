package e2b

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// gcovAssertCommands compares the joined shell commands captured from the envd
// process.Start stream against the expected sequence.
func gcovAssertCommands(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("command[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// gcovStartRequest captures the fully-decoded envd process.Start request so
// tests can assert env/cwd/user propagation instead of only the command text.
type gcovStartRequest struct {
	args          []string
	envs          map[string]string
	cwd           string
	authorization string
}

// gcovCaptureStart records every process.Start request issued while fn runs,
// always answering with a successful (exit 0) command result.
func gcovCaptureStart(t *testing.T, fn func(*Git) error) ([]gcovStartRequest, error) {
	t.Helper()
	var requests []gcovStartRequest
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
		var decoded struct {
			Process struct {
				Args []string          `json:"args"`
				Envs map[string]string `json:"envs"`
				Cwd  string            `json:"cwd"`
			} `json:"process"`
		}
		if err := json.Unmarshal(raw[5:], &decoded); err != nil {
			t.Fatalf("decode command request: %v", err)
		}
		requests = append(requests, gcovStartRequest{
			args:          decoded.Process.Args,
			envs:          decoded.Process.Envs,
			cwd:           decoded.Process.Cwd,
			authorization: r.Header.Get("Authorization"),
		})
		return gitCommandHTTPResponse(t, gitCommandResponse{}), nil
	})
	client, err := NewClient(
		WithAPIKey("e2b_0123"),
		WithAPIURL("https://api.test"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sandbox := &Sandbox{client: client, sandboxID: "sbx", envdAPIURL: "https://envd.test", envdVersion: "0.6.4"}
	sandbox.Commands = newCommands(sandbox)
	git := newGit(sandbox.Commands)
	return requests, fn(git)
}

func gcovAssertInvalidArgument(t *testing.T, name string, err error) {
	t.Helper()
	var invalid *InvalidArgumentError
	if !errors.As(err, &invalid) {
		t.Fatalf("%s error = %T %v, want InvalidArgumentError", name, err, err)
	}
}

func TestGitInitBuildsExpectedCommand(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Git) error
		want []string
	}{
		{
			name: "default path",
			run:  func(g *Git) error { return g.Init(context.Background(), "/repo") },
			want: []string{"'git' 'init' '--' '/repo'"},
		},
		{
			name: "bare with initial branch and no path",
			run: func(g *Git) error {
				return g.Init(context.Background(), "", WithGitInitBare(true), WithGitInitialBranch("main"))
			},
			want: []string{"'git' 'init' '--bare' '--initial-branch' 'main'"},
		},
		{
			name: "nil option is skipped",
			run:  func(g *Git) error { return g.Init(context.Background(), "/repo", nil) },
			want: []string{"'git' 'init' '--' '/repo'"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := captureGitCommands(t, []gitCommandResponse{{}}, tc.run)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			gcovAssertCommands(t, commands, tc.want)
		})
	}
}

func TestGitInitRejectsOptionLikeInitialBranch(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "Init", g.Init(context.Background(), "/repo", WithGitInitialBranch("--bare")))
}

func TestGitCloneRewritesCredentialsAndRestoresRemote(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}, {}}, func(g *Git) error {
		return g.Clone(context.Background(), "https://github.com/e2b-dev/sdk.git", "",
			WithGitCloneAuth("user", "token"),
			WithGitCloneBranch("main"),
			WithGitCloneDepth(1),
		)
	})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	want := []string{
		"'git' 'clone' '--branch' 'main' '--depth' '1' '--' 'https://user:token@github.com/e2b-dev/sdk.git'",
		"'git' '-C' 'sdk' 'remote' 'set-url' 'origin' 'https://github.com/e2b-dev/sdk.git'",
	}
	gcovAssertCommands(t, commands, want)
}

func TestGitCloneWithExplicitPathRestoresRemote(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}, {}}, func(g *Git) error {
		return g.Clone(context.Background(), "https://github.com/e2b-dev/sdk.git", "dest",
			WithGitCloneAuth("user", "token"),
			WithGitCloneOption(WithGitEnvs(map[string]string{"GIT_TERMINAL_PROMPT": "0"})),
		)
	})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	want := []string{
		"'git' 'clone' '--' 'https://user:token@github.com/e2b-dev/sdk.git' 'dest'",
		"'git' '-C' 'dest' 'remote' 'set-url' 'origin' 'https://github.com/e2b-dev/sdk.git'",
	}
	gcovAssertCommands(t, commands, want)
}

func TestGitCloneRejectsOptionLikeBranch(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "Clone", g.Clone(context.Background(), "https://github.com/e2b-dev/sdk.git", "", WithGitCloneBranch("--foo")))
}

func TestGitCloneCloneFailureSkipsRemoteRewrite(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{stderr: "boom", exitCode: 1}}, func(g *Git) error {
		return g.Clone(context.Background(), "https://github.com/e2b-dev/sdk.git", "", WithGitCloneAuth("user", "token"))
	})
	if err == nil {
		t.Fatal("Clone should fail when git clone exits non-zero")
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %#v, want only the clone attempt", commands)
	}
}

func TestGitCloneRemoteRewriteFailurePropagates(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}, {stderr: "denied", exitCode: 1}}, func(g *Git) error {
		return g.Clone(context.Background(), "https://github.com/e2b-dev/sdk.git", "", WithGitCloneAuth("user", "token"))
	})
	if err == nil {
		t.Fatal("Clone should fail when restoring origin URL fails")
	}
	if len(commands) != 2 {
		t.Fatalf("commands = %#v, want clone + failed set-url", commands)
	}
}

func TestGitRemoteAddOverwriteRemovesExisting(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}, {}}, func(g *Git) error {
		return g.RemoteAdd(context.Background(), "/repo", "origin", "https://example.com/x.git", true)
	})
	if err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	want := []string{
		"'git' '-C' '/repo' 'remote' 'remove' '--' 'origin'",
		"'git' '-C' '/repo' 'remote' 'add' 'origin' 'https://example.com/x.git'",
	}
	gcovAssertCommands(t, commands, want)
}

func TestGitRemoteAddRejectsOptionLikeArgs(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "RemoteAdd name", g.RemoteAdd(context.Background(), "/repo", "--evil", "https://example.com/x.git", false))
	gcovAssertInvalidArgument(t, "RemoteAdd url", g.RemoteAdd(context.Background(), "/repo", "origin", "--evil", false))
}

func TestGitRemoteGetReturnsTrimmedURL(t *testing.T) {
	var remoteURL string
	commands, err := captureGitCommands(t, []gitCommandResponse{{stdout: "https://example.com/x.git\n"}}, func(g *Git) error {
		got, e := g.RemoteGet(context.Background(), "/repo", "origin")
		remoteURL = got
		return e
	})
	if err != nil {
		t.Fatalf("RemoteGet: %v", err)
	}
	gcovAssertCommands(t, commands, []string{"'git' '-C' '/repo' 'remote' 'get-url' 'origin'"})
	if remoteURL != "https://example.com/x.git" {
		t.Fatalf("remote URL = %q", remoteURL)
	}
}

func TestGitRemoteGetRejectsOptionLikeNameAndPropagatesError(t *testing.T) {
	g := &Git{}
	_, err := g.RemoteGet(context.Background(), "/repo", "--evil")
	gcovAssertInvalidArgument(t, "RemoteGet", err)

	_, cmdErr := captureGitCommands(t, []gitCommandResponse{{stderr: "boom", exitCode: 1}}, func(g *Git) error {
		_, e := g.RemoteGet(context.Background(), "/repo", "origin")
		return e
	})
	if cmdErr == nil {
		t.Fatal("RemoteGet should surface command failure")
	}
}

func TestGitStatusParsesPorcelainOutput(t *testing.T) {
	var status GitStatus
	commands, err := captureGitCommands(t, []gitCommandResponse{{stdout: "## main...origin/main\n M file.go\n"}}, func(g *Git) error {
		s, e := g.Status(context.Background(), "/repo")
		status = s
		return e
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	gcovAssertCommands(t, commands, []string{"'git' '-C' '/repo' 'status' '--porcelain=v1' '-b'"})
	if status.Branch != "main" || status.Upstream != "origin/main" {
		t.Fatalf("branch/upstream = %q/%q", status.Branch, status.Upstream)
	}
	if status.IsClean || len(status.Files) != 1 {
		t.Fatalf("status = %#v", status)
	}
}

func TestGitStatusPropagatesError(t *testing.T) {
	_, err := captureGitCommands(t, []gitCommandResponse{{stderr: "boom", exitCode: 1}}, func(g *Git) error {
		_, e := g.Status(context.Background(), "/repo")
		return e
	})
	if err == nil {
		t.Fatal("Status should surface command failure")
	}
}

func TestGitBranchesRunsCurrentAndAll(t *testing.T) {
	var branches GitBranches
	commands, err := captureGitCommands(t, []gitCommandResponse{
		{stdout: "main\n"},
		{stdout: "refs/heads/main\nrefs/remotes/origin/main\n"},
	}, func(g *Git) error {
		b, e := g.Branches(context.Background(), "/repo")
		branches = b
		return e
	})
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	want := []string{
		"'git' '-C' '/repo' 'branch' '--show-current'",
		"'git' '-C' '/repo' 'branch' '--all' '--format=%(refname)'",
	}
	gcovAssertCommands(t, commands, want)
	if branches.Current != "main" {
		t.Fatalf("current = %q", branches.Current)
	}
	if len(branches.Local) != 1 || branches.Local[0] != "main" {
		t.Fatalf("local = %#v", branches.Local)
	}
	if len(branches.Remote) != 1 || branches.Remote[0] != "origin/main" {
		t.Fatalf("remote = %#v", branches.Remote)
	}
}

func TestGitBranchesPropagatesErrors(t *testing.T) {
	cases := []struct {
		name      string
		responses []gitCommandResponse
	}{
		{"first command fails", []gitCommandResponse{{stderr: "boom", exitCode: 1}}},
		{"second command fails", []gitCommandResponse{{stdout: "main\n"}, {stderr: "boom", exitCode: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := captureGitCommands(t, tc.responses, func(g *Git) error {
				_, e := g.Branches(context.Background(), "/repo")
				return e
			})
			if err == nil {
				t.Fatal("Branches should surface command failure")
			}
		})
	}
}

func TestGitCreateAndCheckoutBranch(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}, {}}, func(g *Git) error {
		if e := g.CreateBranch(context.Background(), "/repo", "feature"); e != nil {
			return e
		}
		return g.CheckoutBranch(context.Background(), "/repo", "main")
	})
	if err != nil {
		t.Fatalf("branch operations: %v", err)
	}
	want := []string{
		"'git' '-C' '/repo' 'branch' 'feature'",
		"'git' '-C' '/repo' 'checkout' 'main'",
	}
	gcovAssertCommands(t, commands, want)
}

func TestGitCreateBranchRejectsOptionLikeName(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "CreateBranch", g.CreateBranch(context.Background(), "/repo", "--orphan"))
}

func TestGitDeleteBranch(t *testing.T) {
	cases := []struct {
		name  string
		force bool
		want  string
	}{
		{"soft delete", false, "'git' '-C' '/repo' 'branch' '-d' '--' 'feature'"},
		{"force delete", true, "'git' '-C' '/repo' 'branch' '-D' '--' 'feature'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
				return g.DeleteBranch(context.Background(), "/repo", "feature", tc.force)
			})
			if err != nil {
				t.Fatalf("DeleteBranch: %v", err)
			}
			gcovAssertCommands(t, commands, []string{tc.want})
		})
	}
}

func TestGitDeleteBranchRejectsOptionLikeName(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "DeleteBranch", g.DeleteBranch(context.Background(), "/repo", "--force", false))
}

func TestGitAddVariants(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		all   bool
		want  string
	}{
		{"all", nil, true, "'git' '-C' '/repo' 'add' '--all'"},
		{"default to dot", nil, false, "'git' '-C' '/repo' 'add' '--' '.'"},
		{"explicit files", []string{"a.go", "b.go"}, false, "'git' '-C' '/repo' 'add' '--' 'a.go' 'b.go'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
				return g.Add(context.Background(), "/repo", tc.files, tc.all)
			})
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			gcovAssertCommands(t, commands, []string{tc.want})
		})
	}
}

func TestGitCommitVariants(t *testing.T) {
	cases := []struct {
		name        string
		message     string
		authorName  string
		authorEmail string
		allowEmpty  bool
		want        string
	}{
		{"simple message", "msg", "", "", false, "'git' '-C' '/repo' 'commit' '-m' 'msg'"},
		{"empty message", "", "", "", false, "'git' '-C' '/repo' 'commit' '-m' ''"},
		{"author and allow empty", "msg", "Alice", "a@x.com", true, "'git' '-C' '/repo' 'commit' '-m' 'msg' '--author' 'Alice <a@x.com>' '--allow-empty'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
				return g.Commit(context.Background(), "/repo", tc.message, tc.authorName, tc.authorEmail, tc.allowEmpty)
			})
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			gcovAssertCommands(t, commands, []string{tc.want})
		})
	}
}

func TestGitResetVariants(t *testing.T) {
	cases := []struct {
		name  string
		mode  GitResetMode
		reset string
		paths []string
		want  string
	}{
		{"no mode no target", "", "", nil, "'git' '-C' '/repo' 'reset'"},
		{"soft with target and paths", GitResetSoft, "HEAD~1", []string{"a"}, "'git' '-C' '/repo' 'reset' '--soft' 'HEAD~1' '--' 'a'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
				return g.Reset(context.Background(), "/repo", tc.mode, tc.reset, tc.paths)
			})
			if err != nil {
				t.Fatalf("Reset: %v", err)
			}
			gcovAssertCommands(t, commands, []string{tc.want})
		})
	}
}

func TestGitResetRejectsOptionLikeTarget(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "Reset", g.Reset(context.Background(), "/repo", "", "--hard", nil))
}

func TestGitRestoreStagedWithSource(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
		return g.Restore(context.Background(), "/repo", []string{"a"}, true, false, "HEAD")
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	gcovAssertCommands(t, commands, []string{"'git' '-C' '/repo' 'restore' '--staged' '--source' 'HEAD' '--' 'a'"})
}

func TestGitRestoreRejectsOptionLikeSource(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "Restore", g.Restore(context.Background(), "/repo", nil, false, false, "--evil"))
}

func TestGitPushWithoutCredentials(t *testing.T) {
	cases := []struct {
		name        string
		remote      string
		branch      string
		setUpstream bool
		want        string
	}{
		{"upstream with remote and branch", "origin", "main", true, "'git' '-C' '/repo' 'push' '--set-upstream' 'origin' 'main'"},
		{"empty remote and branch", "", "", false, "'git' '-C' '/repo' 'push'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
				return g.Push(context.Background(), "/repo", tc.remote, tc.branch, "", "", tc.setUpstream)
			})
			if err != nil {
				t.Fatalf("Push: %v", err)
			}
			gcovAssertCommands(t, commands, []string{tc.want})
		})
	}
}

func TestGitPushRejectsOptionLikeBranch(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "Push", g.Push(context.Background(), "/repo", "origin", "--bad", "", "", false))
}

func TestGitPushWithHTTPRemoteInlinesCredentials(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
		return g.Push(context.Background(), "/repo", "https://example.com/x.git", "main", "user", "token", false)
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	gcovAssertCommands(t, commands, []string{"'git' '-C' '/repo' 'push' 'https://user:token@example.com/x.git' 'main'"})
}

func TestGitPushWithNamedRemoteRewritesAndRestores(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{
		{stdout: "https://example.com/x.git\n"},
		{},
		{},
		{},
	}, func(g *Git) error {
		return g.Push(context.Background(), "/repo", "origin", "main", "user", "token", false)
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	want := []string{
		"'git' '-C' '/repo' 'remote' 'get-url' 'origin'",
		"'git' '-C' '/repo' 'remote' 'set-url' 'origin' 'https://user:token@example.com/x.git'",
		"'git' '-C' '/repo' 'push' 'origin' 'main'",
		"'git' '-C' '/repo' 'remote' 'set-url' 'origin' 'https://example.com/x.git'",
	}
	gcovAssertCommands(t, commands, want)
}

func TestGitPushWithNamedRemoteRejectsNonHTTPRemote(t *testing.T) {
	_, err := captureGitCommands(t, []gitCommandResponse{{stdout: "git@github.com:e2b-dev/sdk.git\n"}}, func(g *Git) error {
		return g.Push(context.Background(), "/repo", "origin", "main", "user", "token", false)
	})
	var authErr *GitAuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("Push error = %T %v, want GitAuthError", err, err)
	}
}

func TestGitPushWithNamedRemoteGetURLFailure(t *testing.T) {
	_, err := captureGitCommands(t, []gitCommandResponse{{stderr: "boom", exitCode: 1}}, func(g *Git) error {
		return g.Push(context.Background(), "/repo", "origin", "main", "user", "token", false)
	})
	if err == nil {
		t.Fatal("Push should fail when get-url fails")
	}
}

func TestGitPushWithNamedRemoteSetURLFailure(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{
		{stdout: "https://example.com/x.git\n"},
		{stderr: "denied", exitCode: 1},
	}, func(g *Git) error {
		return g.Push(context.Background(), "/repo", "origin", "main", "user", "token", false)
	})
	if err == nil {
		t.Fatal("Push should fail when set-url fails")
	}
	if len(commands) != 2 {
		t.Fatalf("commands = %#v, want get-url + failed set-url", commands)
	}
}

func TestGitPushMapsAuthenticationError(t *testing.T) {
	_, err := captureGitCommands(t, []gitCommandResponse{{stderr: "fatal: Authentication failed for 'https://example.com/x.git'", exitCode: 128}}, func(g *Git) error {
		return g.Push(context.Background(), "/repo", "origin", "main", "", "", false)
	})
	var authErr *GitAuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("Push error = %T %v, want GitAuthError", err, err)
	}
}

func TestGitPullVariants(t *testing.T) {
	cases := []struct {
		name     string
		remote   string
		branch   string
		username string
		password string
		want     string
	}{
		{"with remote and branch", "origin", "main", "", "", "'git' '-C' '/repo' 'pull' 'origin' 'main'"},
		{"empty remote and branch", "", "", "", "", "'git' '-C' '/repo' 'pull'"},
		{"http remote inlines credentials", "https://example.com/x.git", "main", "user", "token", "'git' '-C' '/repo' 'pull' 'https://user:token@example.com/x.git' 'main'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
				return g.Pull(context.Background(), "/repo", tc.remote, tc.branch, tc.username, tc.password)
			})
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}
			gcovAssertCommands(t, commands, []string{tc.want})
		})
	}
}

func TestGitPullRejectsOptionLikeBranch(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "Pull", g.Pull(context.Background(), "/repo", "origin", "--bad", "", ""))
}

func TestGitSetConfigVariants(t *testing.T) {
	cases := []struct {
		name  string
		scope string
		path  string
		want  string
	}{
		{"global scope without path", "global", "", "'git' 'config' '--global' 'user.name' 'Alice'"},
		{"path without scope", "", "/repo", "'git' '-C' '/repo' 'config' 'user.name' 'Alice'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
				return g.SetConfig(context.Background(), "user.name", "Alice", tc.scope, tc.path)
			})
			if err != nil {
				t.Fatalf("SetConfig: %v", err)
			}
			gcovAssertCommands(t, commands, []string{tc.want})
		})
	}
}

func TestGitGetConfigReturnsTrimmedValue(t *testing.T) {
	var value string
	commands, err := captureGitCommands(t, []gitCommandResponse{{stdout: "Alice\n"}}, func(g *Git) error {
		got, e := g.GetConfig(context.Background(), "user.name", "global", "/repo")
		value = got
		return e
	})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	gcovAssertCommands(t, commands, []string{"'git' '-C' '/repo' 'config' '--global' '--get' 'user.name'"})
	if value != "Alice" {
		t.Fatalf("value = %q", value)
	}
}

func TestGitGetConfigPropagatesError(t *testing.T) {
	_, err := captureGitCommands(t, []gitCommandResponse{{stderr: "boom", exitCode: 1}}, func(g *Git) error {
		_, e := g.GetConfig(context.Background(), "user.name", "", "")
		return e
	})
	if err == nil {
		t.Fatal("GetConfig should surface command failure")
	}
}

func TestGitDangerouslyAuthenticateNonHTTPSUsesInsteadOf(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}}, func(g *Git) error {
		return g.DangerouslyAuthenticate(context.Background(), "me", "pw", "", "ssh")
	})
	if err != nil {
		t.Fatalf("DangerouslyAuthenticate: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %#v", commands)
	}
	want := "'/bin/bash' '-lc' 'git config --global url.'\"'\"'ssh'\"'\"'://'\"'\"'me'\"'\"'@'\"'\"'github.com'\"'\"'/.insteadOf '\"'\"'ssh'\"'\"'://'\"'\"'github.com'\"'\"'/'"
	if commands[0] != want {
		t.Fatalf("insteadOf command = %q, want %q", commands[0], want)
	}
}

func TestGitDangerouslyAuthenticateRejectsNewlineFields(t *testing.T) {
	g := &Git{}
	gcovAssertInvalidArgument(t, "protocol", g.DangerouslyAuthenticate(context.Background(), "me", "pw", "github.com", "ht\ntps"))
	gcovAssertInvalidArgument(t, "host", g.DangerouslyAuthenticate(context.Background(), "me", "pw", "bad\nhost", "https"))
	gcovAssertInvalidArgument(t, "username", g.DangerouslyAuthenticate(context.Background(), "m\ne", "pw", "github.com", "https"))
}

func TestGitConfigureUser(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{}, {}}, func(g *Git) error {
		return g.ConfigureUser(context.Background(), "Alice", "a@x.com", "global", "")
	})
	if err != nil {
		t.Fatalf("ConfigureUser: %v", err)
	}
	want := []string{
		"'git' 'config' '--global' 'user.name' 'Alice'",
		"'git' 'config' '--global' 'user.email' 'a@x.com'",
	}
	gcovAssertCommands(t, commands, want)
}

func TestGitConfigureUserStopsOnFirstFailure(t *testing.T) {
	commands, err := captureGitCommands(t, []gitCommandResponse{{stderr: "boom", exitCode: 1}}, func(g *Git) error {
		return g.ConfigureUser(context.Background(), "Alice", "a@x.com", "global", "")
	})
	if err == nil {
		t.Fatal("ConfigureUser should fail when setting user.name fails")
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %#v, want only user.name attempt", commands)
	}
}

func TestGitMapGitErrorClassifies(t *testing.T) {
	if got := mapGitError(nil); got != nil {
		t.Fatalf("mapGitError(nil) = %v, want nil", got)
	}
	var authErr *GitAuthError
	if got := mapGitError(errors.New("fatal: Authentication failed")); !errors.As(got, &authErr) {
		t.Fatalf("mapGitError authentication = %T %v, want GitAuthError", got, got)
	}
	if got := mapGitError(errors.New("fatal: could not read Username for 'https://x'")); !errors.As(got, &authErr) {
		t.Fatalf("mapGitError could not read username = %T %v, want GitAuthError", got, got)
	}
	var upstreamErr *GitUpstreamError
	if got := mapGitError(errors.New("fatal: no upstream configured for branch")); !errors.As(got, &upstreamErr) {
		t.Fatalf("mapGitError no upstream = %T %v, want GitUpstreamError", got, got)
	}
	plain := errors.New("some other failure")
	if got := mapGitError(plain); got != plain {
		t.Fatalf("mapGitError plain = %v, want passthrough", got)
	}
}

func TestGitCommandOptionsPropagateEnvUserCwd(t *testing.T) {
	requests, err := gcovCaptureStart(t, func(g *Git) error {
		return g.Init(context.Background(), "/repo",
			WithGitInitOption(WithGitEnv("GIT_TERMINAL_PROMPT", "0")),
			WithGitInitOption(WithGitUser("bob")),
			WithGitInitOption(WithGitCwd("/work")),
			WithGitInitOption(WithGitTimeout(30*time.Second)),
		)
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	req := requests[0]
	if req.envs["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("envs = %#v", req.envs)
	}
	if req.cwd != "/work" {
		t.Fatalf("cwd = %q", req.cwd)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("bob:"))
	if req.authorization != wantAuth {
		t.Fatalf("authorization = %q, want %q", req.authorization, wantAuth)
	}
}
