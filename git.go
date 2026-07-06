package e2b

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Git provides git operations implemented through sandbox commands.
type Git struct {
	commands *Commands
}

func newGit(commands *Commands) *Git {
	return &Git{commands: commands}
}

// Clone clones a repository.
func (g *Git) Clone(ctx context.Context, repoURL, path string, opts ...GitCloneOption) error {
	options := gitCloneOptionsFrom(opts...)
	if err := rejectGitRemoteExt(repoURL); err != nil {
		return err
	}
	target := repoURL
	if options.username != "" || options.password != "" {
		rewritten, err := urlWithCredentials(repoURL, options.username, options.password)
		if err != nil {
			return err
		}
		target = rewritten
	}
	args := []string{"git", "clone"}
	if options.branch != "" {
		if err := rejectGitOptionArg("branch", options.branch); err != nil {
			return err
		}
		args = append(args, "--branch", options.branch)
	}
	if options.depth > 0 {
		args = append(args, "--depth", strconv.Itoa(options.depth))
	}
	args = append(args, "--", target)
	if path != "" {
		args = append(args, path)
	}
	if err := g.runGit(ctx, args, options.gitCommandOptions); err != nil {
		return err
	}
	if options.username != "" || options.password != "" {
		clonePath := path
		if clonePath == "" {
			clonePath = cloneDestinationFromURL(repoURL)
		}
		if clonePath != "" {
			if err := g.runGit(ctx, []string{"git", "-C", clonePath, "remote", "set-url", "origin", repoURL}, options.gitCommandOptions); err != nil {
				return err
			}
		}
	}
	return nil
}

// Init initializes a git repository.
func (g *Git) Init(ctx context.Context, path string, opts ...GitInitOption) error {
	options := gitInitOptionsFrom(opts...)
	args := []string{"git", "init"}
	if options.bare {
		args = append(args, "--bare")
	}
	if options.initialBranch != "" {
		if err := rejectGitOptionArg("initial branch", options.initialBranch); err != nil {
			return err
		}
		args = append(args, "--initial-branch", options.initialBranch)
	}
	if path != "" {
		args = append(args, "--", path)
	}
	return g.runGit(ctx, args, options.gitCommandOptions)
}

// RemoteAdd adds a remote.
func (g *Git) RemoteAdd(ctx context.Context, path, name, remoteURL string, overwrite bool, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("remote name", name); err != nil {
		return err
	}
	if err := rejectGitOptionArg("remote URL", remoteURL); err != nil {
		return err
	}
	if err := rejectGitRemoteExt(remoteURL); err != nil {
		return err
	}
	if overwrite {
		_ = g.runGit(ctx, []string{"git", "-C", path, "remote", "remove", "--", name}, options)
	}
	args := []string{"git", "-C", path, "remote", "add", name, remoteURL}
	return g.runGit(ctx, args, options)
}

// RemoteGet returns a remote URL.
func (g *Git) RemoteGet(ctx context.Context, path, name string, opts ...GitOption) (string, error) {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("remote name", name); err != nil {
		return "", err
	}
	result, err := g.runGitResult(ctx, []string{"git", "-C", path, "remote", "get-url", name}, options)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

// Status returns parsed git status.
func (g *Git) Status(ctx context.Context, path string, opts ...GitOption) (GitStatus, error) {
	options := gitOptionsFrom(opts...)
	result, err := g.runGitResult(ctx, []string{"git", "-C", path, "status", "--porcelain=v1", "-b"}, options)
	if err != nil {
		return GitStatus{}, err
	}
	return parseGitStatus(result.Stdout), nil
}

// Branches lists branches.
func (g *Git) Branches(ctx context.Context, path string, opts ...GitOption) (GitBranches, error) {
	options := gitOptionsFrom(opts...)
	currentResult, err := g.runGitResult(ctx, []string{"git", "-C", path, "branch", "--show-current"}, options)
	if err != nil {
		return GitBranches{}, err
	}
	branchesResult, err := g.runGitResult(ctx, []string{"git", "-C", path, "branch", "--all", "--format=%(refname)"}, options)
	if err != nil {
		return GitBranches{}, err
	}
	return parseGitBranches(currentResult.Stdout, branchesResult.Stdout), nil
}

func parseGitBranches(current, raw string) GitBranches {
	branches := GitBranches{Current: strings.TrimSpace(current), Raw: raw}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "refs/remotes/"):
			branches.Remote = append(branches.Remote, strings.TrimPrefix(line, "refs/remotes/"))
		case strings.HasPrefix(line, "refs/heads/"):
			branches.Local = append(branches.Local, strings.TrimPrefix(line, "refs/heads/"))
		case strings.HasPrefix(line, "remotes/"):
			branches.Remote = append(branches.Remote, strings.TrimPrefix(line, "remotes/"))
		default:
			branches.Local = append(branches.Local, line)
		}
	}
	return branches
}

func (g *Git) CreateBranch(ctx context.Context, path, branch string, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("branch", branch); err != nil {
		return err
	}
	return g.runGit(ctx, []string{"git", "-C", path, "branch", branch}, options)
}

func (g *Git) CheckoutBranch(ctx context.Context, path, branch string, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("branch", branch); err != nil {
		return err
	}
	return g.runGit(ctx, []string{"git", "-C", path, "checkout", branch}, options)
}

func (g *Git) DeleteBranch(ctx context.Context, path, branch string, force bool, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("branch", branch); err != nil {
		return err
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	return g.runGit(ctx, []string{"git", "-C", path, "branch", flag, "--", branch}, options)
}

func (g *Git) Add(ctx context.Context, path string, files []string, all bool, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	args := []string{"git", "-C", path, "add"}
	if all {
		args = append(args, "--all")
	}
	if len(files) == 0 && !all {
		files = []string{"."}
	}
	args = appendGitPathspecs(args, files)
	return g.runGit(ctx, args, options)
}

func (g *Git) Commit(ctx context.Context, path, message, authorName, authorEmail string, allowEmpty bool, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	args := []string{"git", "-C", path, "commit", "-m", message}
	if authorName != "" || authorEmail != "" {
		args = append(args, "--author", fmt.Sprintf("%s <%s>", authorName, authorEmail))
	}
	if allowEmpty {
		args = append(args, "--allow-empty")
	}
	return g.runGit(ctx, args, options)
}

func (g *Git) Reset(ctx context.Context, path string, mode GitResetMode, target string, paths []string, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	args := []string{"git", "-C", path, "reset"}
	if mode != "" {
		args = append(args, "--"+string(mode))
	}
	if target != "" {
		if err := rejectGitOptionArg("reset target", target); err != nil {
			return err
		}
		args = append(args, target)
	}
	args = appendGitPathspecs(args, paths)
	return g.runGit(ctx, args, options)
}

func (g *Git) Restore(ctx context.Context, path string, paths []string, staged, worktree bool, source string, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	args := []string{"git", "-C", path, "restore"}
	if staged {
		args = append(args, "--staged")
	}
	if worktree {
		args = append(args, "--worktree")
	}
	if source != "" {
		if err := rejectGitOptionArg("restore source", source); err != nil {
			return err
		}
		args = append(args, "--source", source)
	}
	args = appendGitPathspecs(args, paths)
	return g.runGit(ctx, args, options)
}

func (g *Git) Push(ctx context.Context, path, remote, branch, username, password string, setUpstream bool, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("remote", remote); err != nil {
		return err
	}
	if err := rejectGitRemoteExt(remote); err != nil {
		return err
	}
	if err := rejectGitOptionArg("branch", branch); err != nil {
		return err
	}
	if username != "" || password != "" {
		return g.withTemporaryCredentialedRemote(ctx, path, remote, username, password, options, func(remoteArg string) error {
			args := []string{"git", "-C", path, "push"}
			if setUpstream {
				args = append(args, "--set-upstream")
			}
			if remoteArg != "" {
				args = append(args, remoteArg)
			}
			if branch != "" {
				args = append(args, branch)
			}
			return g.runGit(ctx, args, options)
		})
	}
	args := []string{"git", "-C", path, "push"}
	if setUpstream {
		args = append(args, "--set-upstream")
	}
	if remote != "" {
		args = append(args, remote)
	}
	if branch != "" {
		args = append(args, branch)
	}
	return g.runGit(ctx, args, options)
}

func (g *Git) Pull(ctx context.Context, path, remote, branch, username, password string, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("remote", remote); err != nil {
		return err
	}
	if err := rejectGitRemoteExt(remote); err != nil {
		return err
	}
	if err := rejectGitOptionArg("branch", branch); err != nil {
		return err
	}
	if username != "" || password != "" {
		return g.withTemporaryCredentialedRemote(ctx, path, remote, username, password, options, func(remoteArg string) error {
			args := []string{"git", "-C", path, "pull"}
			if remoteArg != "" {
				args = append(args, remoteArg)
			}
			if branch != "" {
				args = append(args, branch)
			}
			return g.runGit(ctx, args, options)
		})
	}
	args := []string{"git", "-C", path, "pull"}
	if remote != "" {
		args = append(args, remote)
	}
	if branch != "" {
		args = append(args, branch)
	}
	return g.runGit(ctx, args, options)
}

func (g *Git) SetConfig(ctx context.Context, key, value, scope, path string, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("config key", key); err != nil {
		return err
	}
	args := []string{"git"}
	if path != "" {
		args = append(args, "-C", path)
	}
	args = append(args, "config")
	if scope != "" {
		args = append(args, "--"+scope)
	}
	args = append(args, key, value)
	return g.runGit(ctx, args, options)
}

func (g *Git) GetConfig(ctx context.Context, key, scope, path string, opts ...GitOption) (string, error) {
	options := gitOptionsFrom(opts...)
	if err := rejectGitOptionArg("config key", key); err != nil {
		return "", err
	}
	args := []string{"git"}
	if path != "" {
		args = append(args, "-C", path)
	}
	args = append(args, "config")
	if scope != "" {
		args = append(args, "--"+scope)
	}
	args = append(args, "--get", key)
	result, err := g.runGitResult(ctx, args, options)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func (g *Git) DangerouslyAuthenticate(ctx context.Context, username, password, host, protocol string, opts ...GitOption) error {
	options := gitOptionsFrom(opts...)
	if protocol == "" {
		protocol = "https"
	}
	if host == "" {
		host = "github.com"
	}
	if err := rejectNetrcNewline("protocol", protocol); err != nil {
		return err
	}
	if err := rejectNetrcNewline("host", host); err != nil {
		return err
	}
	if err := rejectNetrcNewline("username", username); err != nil {
		return err
	}
	if err := rejectNetrcNewline("password", password); err != nil {
		return err
	}
	line := fmt.Sprintf("machine %s login %s password %s\n", host, username, password)
	cmd := []string{"/bin/bash", "-lc", fmt.Sprintf("umask 077; printf %%s %s >> ~/.netrc && chmod 600 ~/.netrc", shellQuote(line))}
	if protocol != "https" {
		cmd = []string{"/bin/bash", "-lc", fmt.Sprintf("git config --global url.%s://%s@%s/.insteadOf %s://%s/", shellQuote(protocol), shellQuote(username), shellQuote(host), shellQuote(protocol), shellQuote(host))}
	}
	return g.runGit(ctx, cmd, options)
}

func (g *Git) ConfigureUser(ctx context.Context, name, email, scope, path string, opts ...GitOption) error {
	if err := g.SetConfig(ctx, "user.name", name, scope, path, opts...); err != nil {
		return err
	}
	return g.SetConfig(ctx, "user.email", email, scope, path, opts...)
}

func (g *Git) runGit(ctx context.Context, args []string, options gitCommandOptions) error {
	_, err := g.runGitResult(ctx, args, options)
	return mapGitError(err)
}

func (g *Git) runGitResult(ctx context.Context, args []string, options gitCommandOptions) (CommandResult, error) {
	cmd := joinShellArgs(args)
	commandOptions := []CommandOption{
		WithCommandEnvs(options.envs),
		WithCommandTimeout(options.timeout),
	}
	if options.user != "" {
		commandOptions = append(commandOptions, WithCommandUser(options.user))
	}
	if options.cwd != "" {
		commandOptions = append(commandOptions, WithCommandCwd(options.cwd))
	}
	return g.commands.Run(ctx, cmd, commandOptions...)
}

func (g *Git) withTemporaryCredentialedRemote(ctx context.Context, path, remote, username, password string, options gitCommandOptions, fn func(remoteArg string) error) error {
	if remote == "" {
		remote = "origin"
	}
	if isHTTPURL(remote) {
		credentialed, err := urlWithCredentials(remote, username, password)
		if err != nil {
			return err
		}
		return fn(credentialed)
	}
	remoteResult, err := g.runGitResult(ctx, []string{"git", "-C", path, "remote", "get-url", remote}, options)
	if err != nil {
		return err
	}
	remoteURL := strings.TrimSpace(remoteResult.Stdout)
	if !isHTTPURL(remoteURL) {
		return &GitAuthError{Message: "git username/password auth requires an HTTP(S) remote"}
	}
	credentialed, err := urlWithCredentials(remoteURL, username, password)
	if err != nil {
		return err
	}
	if err := g.runGit(ctx, []string{"git", "-C", path, "remote", "set-url", remote, credentialed}, options); err != nil {
		return err
	}
	err = fn(remote)
	restoreErr := g.runGit(context.Background(), []string{"git", "-C", path, "remote", "set-url", remote, remoteURL}, options)
	if err != nil && restoreErr != nil {
		return errors.Join(err, fmt.Errorf("restore git remote URL: %w", restoreErr))
	}
	if err != nil {
		return err
	}
	if restoreErr != nil {
		return fmt.Errorf("restore git remote URL: %w", restoreErr)
	}
	return nil
}

func mapGitError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "authentication") || strings.Contains(strings.ToLower(err.Error()), "could not read username") {
		return &GitAuthError{Message: err.Error()}
	}
	if strings.Contains(strings.ToLower(err.Error()), "no upstream") {
		return &GitUpstreamError{Message: err.Error()}
	}
	return err
}

func parseGitStatus(raw string) GitStatus {
	status := GitStatus{Raw: raw, IsClean: true}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			parseStatusBranch(&status, strings.TrimPrefix(line, "## "))
			continue
		}
		if len(line) < 3 {
			continue
		}
		file := GitFileStatus{X: string(line[0]), Y: string(line[1]), Path: strings.TrimSpace(line[3:])}
		status.Files = append(status.Files, file)
		status.IsClean = false
		if isConflictStatus(file.X, file.Y) {
			status.Conflicts = append(status.Conflicts, file)
		}
	}
	return status
}

func parseStatusBranch(status *GitStatus, branchLine string) {
	parts := strings.SplitN(branchLine, "...", 2)
	status.Branch = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		upstream := parts[1]
		if idx := strings.Index(upstream, " ["); idx >= 0 {
			status.Upstream = upstream[:idx]
			meta := strings.TrimSuffix(strings.TrimPrefix(upstream[idx+1:], "["), "]")
			for _, item := range strings.Split(meta, ",") {
				item = strings.TrimSpace(item)
				fields := strings.Fields(item)
				if len(fields) == 2 {
					n, _ := strconv.Atoi(fields[1])
					switch fields[0] {
					case "ahead":
						status.Ahead = n
					case "behind":
						status.Behind = n
					}
				}
			}
		} else {
			status.Upstream = strings.TrimSpace(upstream)
		}
	}
}

func isConflictStatus(x, y string) bool {
	switch x + y {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
}

func joinShellArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func appendGitPathspecs(args []string, paths []string) []string {
	if len(paths) == 0 {
		return args
	}
	args = append(args, "--")
	return append(args, paths...)
}

func rejectGitOptionArg(name, value string) error {
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "-") {
		return &InvalidArgumentError{Message: fmt.Sprintf("git %s must not start with '-'", name)}
	}
	return nil
}

func rejectGitRemoteExt(raw string) error {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "ext::") {
		return &InvalidArgumentError{Message: "git clone does not allow ext:: remote URLs"}
	}
	return nil
}

func rejectNetrcNewline(name, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return &InvalidArgumentError{Message: fmt.Sprintf("git credential %s must not contain newlines", name)}
	}
	return nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func urlWithCredentials(raw, username, password string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if username != "" || password != "" {
		u.User = url.UserPassword(username, password)
	}
	return u.String(), nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func cloneDestinationFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && u.Path != "" {
		base := strings.TrimSuffix(pathBase(u.Path), ".git")
		if base != "." && base != "/" {
			return base
		}
	}
	if idx := strings.LastIndex(raw, ":"); idx >= 0 {
		raw = raw[idx+1:]
	}
	base := strings.TrimSuffix(pathBase(raw), ".git")
	if base == "." || base == "/" {
		return ""
	}
	return base
}

func pathBase(raw string) string {
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return ""
	}
	if idx := strings.LastIndex(raw, "/"); idx >= 0 {
		return raw[idx+1:]
	}
	return raw
}

type gitCommandOptions struct {
	envs    map[string]string
	user    string
	cwd     string
	timeout time.Duration
}

// GitOption configures git operations.
type GitOption func(*gitCommandOptions)

func WithGitEnv(key, value string) GitOption {
	return func(o *gitCommandOptions) { o.envs[key] = value }
}

func WithGitEnvs(envs map[string]string) GitOption {
	return func(o *gitCommandOptions) { o.envs = cloneStringMap(envs) }
}

func WithGitUser(user string) GitOption {
	return func(o *gitCommandOptions) { o.user = user }
}

func WithGitCwd(cwd string) GitOption {
	return func(o *gitCommandOptions) { o.cwd = cwd }
}

func WithGitTimeout(timeout time.Duration) GitOption {
	return func(o *gitCommandOptions) { o.timeout = timeout }
}

func gitOptionsFrom(opts ...GitOption) gitCommandOptions {
	options := gitCommandOptions{envs: map[string]string{}, timeout: 60 * time.Second}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

type gitCloneOptions struct {
	gitCommandOptions
	branch   string
	depth    int
	username string
	password string
}

type GitCloneOption func(*gitCloneOptions)

func WithGitCloneBranch(branch string) GitCloneOption {
	return func(o *gitCloneOptions) { o.branch = branch }
}

func WithGitCloneDepth(depth int) GitCloneOption {
	return func(o *gitCloneOptions) { o.depth = depth }
}

func WithGitCloneAuth(username, password string) GitCloneOption {
	return func(o *gitCloneOptions) {
		o.username = username
		o.password = password
	}
}

func WithGitCloneOption(opt GitOption) GitCloneOption {
	return func(o *gitCloneOptions) { opt(&o.gitCommandOptions) }
}

func gitCloneOptionsFrom(opts ...GitCloneOption) gitCloneOptions {
	options := gitCloneOptions{gitCommandOptions: gitOptionsFrom()}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

type gitInitOptions struct {
	gitCommandOptions
	bare          bool
	initialBranch string
}

type GitInitOption func(*gitInitOptions)

func WithGitInitBare(bare bool) GitInitOption {
	return func(o *gitInitOptions) { o.bare = bare }
}

func WithGitInitialBranch(branch string) GitInitOption {
	return func(o *gitInitOptions) { o.initialBranch = branch }
}

func WithGitInitOption(opt GitOption) GitInitOption {
	return func(o *gitInitOptions) { opt(&o.gitCommandOptions) }
}

func gitInitOptionsFrom(opts ...GitInitOption) gitInitOptions {
	options := gitInitOptions{gitCommandOptions: gitOptionsFrom()}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}
