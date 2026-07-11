package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	e2b "github.com/e2b-dev/e2b-go-sdk"
)

type e2eRun struct {
	ctx        context.Context
	client     *e2b.Client
	sandbox    *e2b.Sandbox
	httpClient *http.Client
	testID     string
	template   string
	workdir    string
	issues     []string
}

func main() {
	os.Exit(run())
}

func run() int {
	full := enabled("E2B_E2E_FULL")
	timeout := envDuration("E2B_E2E_TIMEOUT_SECONDS", 25*time.Minute)
	if full {
		timeout = envDuration("E2B_E2E_TIMEOUT_SECONDS", 45*time.Minute)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	apiKey := env("E2B_API_KEY", env("E2B_OFFICIAL_API_KEY", ""))
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "set E2B_API_KEY or E2B_OFFICIAL_API_KEY")
		return 1
	}

	templateName := env("E2B_E2E_TEMPLATE", e2b.DefaultTemplate)
	testID := time.Now().UTC().Format("20060102150405")
	run := &e2eRun{
		ctx:        ctx,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		testID:     testID,
		template:   templateName,
		workdir:    "e2b-go-sdk-e2e-" + testID,
	}

	if !runStep("client", func() error {
		client, err := newClient(apiKey)
		if err != nil {
			return err
		}
		run.client = client
		cfg := client.Config()
		fmt.Printf("api_url=%s domain=%s template=%s timeout=%s\n", cfg.APIURL, cfg.Domain, templateName, timeout)
		return nil
	}) {
		return 1
	}

	var mountedVolume *e2b.Volume
	if full || enabled("E2B_E2E_VOLUME") {
		if !runStep("volume setup", func() error {
			volume, err := run.createVolumeFixture()
			if err != nil {
				return err
			}
			mountedVolume = volume
			return nil
		}) {
			return 1
		}
		defer run.destroyVolume(mountedVolume)
	}

	if !runStep("sandbox lifecycle", func() error {
		return run.createSandbox(mountedVolume)
	}) {
		run.killSandbox()
		return 1
	}
	defer run.killSandbox()

	for _, item := range []struct {
		name string
		fn   func() error
	}{
		{name: "commands", fn: run.verifyCommands},
		{name: "filesystem", fn: run.verifyFilesystem},
		{name: "watch", fn: run.verifyWatch},
		{name: "pty", fn: run.verifyPTY},
		{name: "git", fn: run.verifyGit},
		{name: "network and metrics", fn: run.verifyNetworkAndMetrics},
		{name: "signed file urls", fn: run.verifySignedFileURLs},
		{name: "error mapping", fn: run.verifyErrorMapping},
	} {
		if !runStep(item.name, item.fn) {
			return 1
		}
	}

	if mountedVolume != nil {
		if !runStep("volume content and mount", func() error {
			return run.verifyVolume(mountedVolume)
		}) {
			return 1
		}
	}
	if full || enabled("E2B_E2E_PAUSE") {
		if !runStep("pause and reconnect", run.verifyPauseAndReconnect) {
			return 1
		}
	}
	if full || enabled("E2B_E2E_SNAPSHOT") {
		if !runStep("snapshot", run.verifySnapshot) {
			return 1
		}
	}
	if full || enabled("E2B_E2E_TEMPLATE_BUILD") {
		if !runStep("template build", run.verifyTemplateBuild) {
			return 1
		}
	}

	if len(run.issues) > 0 {
		fmt.Println("\nE2E completed with SDK issues:")
		for _, issue := range run.issues {
			fmt.Println("- " + issue)
		}
		return 1
	}

	fmt.Println("\nE2E completed successfully")
	return 0
}

func newClient(apiKey string) (*e2b.Client, error) {
	domain := env("E2B_DOMAIN", "e2b.app")
	apiURL := env("E2B_API_URL", "https://api."+domain)
	opts := []e2b.Option{
		e2b.WithAPIKey(apiKey),
		e2b.WithDomain(domain),
		e2b.WithAPIURL(apiURL),
		e2b.WithIntegration("e2b-go-sdk-e2e/1.0"),
		e2b.WithRequestTimeout(envDuration("E2B_E2E_REQUEST_TIMEOUT_SECONDS", 120*time.Second)),
	}
	return e2b.NewClient(opts...)
}

func (r *e2eRun) createSandbox(volume *e2b.Volume) error {
	opts := []e2b.SandboxCreateOption{
		e2b.WithTemplate(r.template),
		e2b.WithTimeout(900),
		e2b.WithMetadata(map[string]string{
			"go_sdk_e2e":    "true",
			"go_sdk_e2e_id": r.testID,
		}),
		e2b.WithEnv("E2B_GO_SDK_E2E", r.testID),
		e2b.WithInternetAccess(true),
	}
	if volume != nil {
		opts = append(opts, e2b.WithVolumeMount("/mnt/e2b-go-sdk-e2e", volume.Name()))
	}

	sandbox, err := r.client.CreateSandbox(r.ctx, opts...)
	if err != nil {
		return err
	}
	r.sandbox = sandbox
	fmt.Printf("sandbox_id=%s envd=%s envd_api_url=%s\n", sandbox.SandboxID(), sandbox.EnvdVersion(), sandbox.EnvdAPIURL())
	fmt.Printf("host_8000=%s mcp_url=%s\n", sandbox.GetHost(8000), sandbox.GetMCPURL())

	running, err := sandbox.IsRunning(r.ctx)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("sandbox is not running after create")
	}

	info, err := sandbox.GetInfo(r.ctx)
	if err != nil {
		return err
	}
	if info.SandboxID != "" && info.SandboxID != sandbox.SandboxID() {
		return fmt.Errorf("GetInfo sandboxID=%q, want %q", info.SandboxID, sandbox.SandboxID())
	}
	fmt.Printf("state=%s cpu=%d memory_mb=%d disk_mb=%d\n", info.State, info.CPUCount, info.MemoryMB, info.DiskSizeMB)

	if err := sandbox.SetTimeout(r.ctx, 900); err != nil {
		return err
	}
	connected, err := r.client.ConnectSandbox(r.ctx, sandbox.SandboxID(), 900)
	if err != nil {
		return err
	}
	if connected.SandboxID() != sandbox.SandboxID() {
		return fmt.Errorf("connected sandboxID=%q, want %q", connected.SandboxID(), sandbox.SandboxID())
	}
	r.sandbox = connected

	return r.waitForSandboxInMetadataList(sandbox.SandboxID())
}

func (r *e2eRun) waitForSandboxInMetadataList(sandboxID string) error {
	timeout := 30 * time.Second
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		page, err := r.client.ListSandboxes(r.ctx, &e2b.SandboxQuery{
			Metadata: map[string]string{"go_sdk_e2e_id": r.testID},
		}, 20, "")
		if err != nil {
			return err
		}
		if containsSandbox(page.Items, sandboxID) {
			return nil
		}

		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("created sandbox %s not found in ListSandboxes metadata query after %s", sandboxID, timeout)
		case <-ticker.C:
		}
	}
}

func (r *e2eRun) verifyCommands() error {
	result, err := r.sandbox.Commands.Run(
		r.ctx,
		"printf '%s' \"$E2B_GO_SDK_E2E\"",
		e2b.WithCommandEnv("COMMAND_E2E", "ok"),
		e2b.WithCommandTimeout(30*time.Second),
	)
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.Stdout) != r.testID {
		return fmt.Errorf("command env stdout=%q, want %q", result.Stdout, r.testID)
	}

	var streamedStdout strings.Builder
	var streamedStderr strings.Builder
	handle, err := r.sandbox.Commands.Start(r.ctx, "for i in 1 2 3; do echo out-$i; echo err-$i >&2; sleep 0.1; done")
	if err != nil {
		return err
	}
	result, err = handle.Wait(r.ctx, e2b.WithWaitStdout(func(chunk string) {
		streamedStdout.WriteString(chunk)
	}), e2b.WithWaitStderr(func(chunk string) {
		streamedStderr.WriteString(chunk)
	}))
	if err != nil {
		return err
	}
	if result.ExitCode != 0 || !strings.Contains(streamedStdout.String(), "out-3") || !strings.Contains(streamedStderr.String(), "err-3") {
		return fmt.Errorf("stream result=%+v stdout=%q stderr=%q", result, streamedStdout.String(), streamedStderr.String())
	}

	stdin, err := r.sandbox.Commands.Start(r.ctx, "cat", e2b.WithCommandStdin(true), e2b.WithCommandTimeout(20*time.Second))
	if err != nil {
		return err
	}
	if err := stdin.SendStdin(r.ctx, []byte("stdin-ok\n")); err != nil {
		_, _ = stdin.Kill(context.Background())
		return err
	}
	if err := stdin.CloseStdin(r.ctx); err != nil {
		_, _ = stdin.Kill(context.Background())
		return err
	}
	result, err = stdin.Wait(r.ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.Stdout) != "stdin-ok" {
		return fmt.Errorf("stdin stdout=%q", result.Stdout)
	}

	reconnect, err := r.sandbox.Commands.Start(r.ctx, "sleep 5; echo reconnect-ok", e2b.WithCommandTimeout(20*time.Second))
	if err != nil {
		return err
	}
	pid := reconnect.PID()
	if err := reconnect.Disconnect(); err != nil {
		return err
	}
	reattached, err := r.sandbox.Commands.Connect(r.ctx, pid, 20*time.Second)
	if err != nil {
		return err
	}
	result, err = reattached.Wait(r.ctx)
	if err != nil {
		return err
	}
	if !strings.Contains(result.Stdout, "reconnect-ok") {
		return fmt.Errorf("reconnected stdout=%q", result.Stdout)
	}

	sleep, err := r.sandbox.Commands.Start(r.ctx, "sleep 30", e2b.WithCommandTimeout(0))
	if err != nil {
		return err
	}
	processes, err := r.sandbox.Commands.List(r.ctx)
	if err != nil {
		_, _ = sleep.Kill(context.Background())
		return err
	}
	if !containsProcess(processes, sleep.PID()) {
		_, _ = sleep.Kill(context.Background())
		return fmt.Errorf("sleep pid %d not found in process list", sleep.PID())
	}
	killed, err := sleep.Kill(r.ctx)
	if err != nil {
		return err
	}
	if !killed {
		return fmt.Errorf("sleep pid %d was not killed", sleep.PID())
	}

	_, err = r.sandbox.Commands.Run(r.ctx, "echo expected-failure >&2; exit 7")
	var exitErr *e2b.CommandExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("non-zero command error=%T %v, want CommandExitError", err, err)
	}
	if exitErr.Result.ExitCode != 7 || !strings.Contains(exitErr.Result.Stderr, "expected-failure") {
		return fmt.Errorf("exit error result=%+v", exitErr.Result)
	}
	return nil
}

func (r *e2eRun) verifyFilesystem() error {
	if _, err := r.sandbox.Commands.Run(r.ctx, "rm -rf "+shellQuote(r.workdir)); err != nil {
		return fmt.Errorf("cleanup old workdir %s: %w", r.workdir, err)
	}
	created, err := r.sandbox.Files.MakeDir(r.ctx, r.workdir)
	if err != nil {
		return fmt.Errorf("mkdir %s: %w", r.workdir, err)
	}
	if !created {
		return fmt.Errorf("MakeDir(%s) returned false for a new directory", r.workdir)
	}
	created, err = r.sandbox.Files.MakeDir(r.ctx, r.workdir)
	if err != nil {
		return fmt.Errorf("mkdir existing %s: %w", r.workdir, err)
	}
	if created {
		if envdAtLeast(r.sandbox.EnvdVersion(), "0.6.0") {
			return fmt.Errorf("MakeDir(%s) returned true for an existing directory", r.workdir)
		}
		fmt.Printf("skip MakeDir existing false assertion: envd %s returned created=true\n", r.sandbox.EnvdVersion())
	}

	absolutePath := "/tmp/e2b-go-sdk-e2e-" + r.testID + ".txt"
	if _, err := r.writeText(absolutePath, "absolute-path\n"); err != nil {
		r.recordIssue("Files.WriteText absolute /tmp path failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
	} else if text, err := r.sandbox.Files.Read(r.ctx, absolutePath); err != nil {
		return fmt.Errorf("read absolute path file: %w", err)
	} else if strings.TrimSpace(text) != "absolute-path" {
		return fmt.Errorf("absolute path readback=%q", text)
	}

	defaultPath := r.workdir + "/default-multipart.txt"
	if _, err := r.sandbox.Files.WriteText(r.ctx, defaultPath, "default-multipart\n"); err != nil {
		r.recordIssue("Files.WriteText default multipart upload failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
	} else {
		text, err := r.sandbox.Files.Read(r.ctx, defaultPath)
		if err != nil {
			return fmt.Errorf("read default multipart file: %w", err)
		}
		if strings.TrimSpace(text) != "default-multipart" {
			return fmt.Errorf("default multipart readback=%q", text)
		}
	}

	textPath := r.workdir + "/hello.txt"
	writeInfo, err := r.writeText(textPath, "hello filesystem\n")
	if err != nil {
		return fmt.Errorf("write text via octet %s: %w", textPath, err)
	}
	if writeInfo.Path == "" || writeInfo.Type != e2b.FileTypeFile {
		return fmt.Errorf("WriteText info=%+v", writeInfo)
	}
	text, err := r.sandbox.Files.Read(r.ctx, textPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) != "hello filesystem" {
		return fmt.Errorf("Read text=%q", text)
	}

	bytesPath := r.workdir + "/bytes.bin"
	if _, err := r.writeBytes(bytesPath, []byte{0, 1, 2, 3}); err != nil {
		return fmt.Errorf("write bytes via octet %s: %w", bytesPath, err)
	}
	data, err := r.sandbox.Files.ReadBytes(r.ctx, bytesPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, []byte{0, 1, 2, 3}) {
		return fmt.Errorf("ReadBytes=%v", data)
	}

	stream, err := r.sandbox.Files.ReadStream(r.ctx, textPath, e2b.WithFileStreamIdleTimeout(30*time.Second))
	if err != nil {
		return err
	}
	streamed, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if string(streamed) != text {
		return fmt.Errorf("ReadStream=%q, want %q", string(streamed), text)
	}

	files := []e2b.WriteEntry{
		{Path: r.workdir + "/multi-a.txt", Data: strings.NewReader("a")},
		{Path: r.workdir + "/multi-b.txt", Data: strings.NewReader("b")},
	}
	written, err := r.sandbox.Files.WriteFiles(r.ctx, files)
	if err != nil {
		r.recordIssue("Files.WriteFiles multipart upload failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
		if _, err := r.writeText(r.workdir+"/multi-a.txt", "a"); err != nil {
			return fmt.Errorf("write fallback multi-a: %w", err)
		}
		if _, err := r.writeText(r.workdir+"/multi-b.txt", "b"); err != nil {
			return fmt.Errorf("write fallback multi-b: %w", err)
		}
	} else if len(written) != 2 {
		return fmt.Errorf("WriteFiles returned %d entries", len(written))
	}

	if envdAtLeast(r.sandbox.EnvdVersion(), "0.5.7") {
		gzipPath := r.workdir + "/gzip.txt"
		if _, err := r.writeText(gzipPath, "gzip-ok\n", e2b.WithGzip(true)); err != nil {
			return err
		}
		gzipText, err := r.sandbox.Files.Read(r.ctx, gzipPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(gzipText) != "gzip-ok" {
			return fmt.Errorf("gzip upload readback=%q", gzipText)
		}
	} else {
		fmt.Printf("skip octet/gzip upload: envd %s < 0.5.7\n", r.sandbox.EnvdVersion())
	}

	if envdAtLeast(r.sandbox.EnvdVersion(), "0.6.2") {
		metaPath := r.workdir + "/metadata.txt"
		info, err := r.writeText(metaPath, "metadata-ok\n", e2b.WithFileMetadata(map[string]string{"purpose": "e2e"}))
		if err != nil {
			return err
		}
		if info.Metadata["purpose"] != "e2e" {
			return fmt.Errorf("file metadata=%+v", info.Metadata)
		}
	} else {
		fmt.Printf("skip file metadata: envd %s < 0.6.2\n", r.sandbox.EnvdVersion())
	}

	entries, err := r.sandbox.Files.List(r.ctx, r.workdir, e2b.WithListDepth(1))
	if err != nil {
		r.recordIssue("Files.List failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
	} else if len(entries) < 4 {
		return fmt.Errorf("List returned %d entries", len(entries))
	}
	entry, err := r.sandbox.Files.GetInfo(r.ctx, textPath)
	if err != nil {
		r.recordIssue("Files.GetInfo failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
	} else if entry.Size == 0 || entry.Type != e2b.FileTypeFile {
		return fmt.Errorf("GetInfo=%+v", entry)
	}
	exists, err := r.sandbox.Files.Exists(r.ctx, textPath)
	if err != nil {
		r.recordIssue("Files.Exists failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
	} else if !exists {
		return fmt.Errorf("Exists(%s) returned false", textPath)
	}

	renamedPath := r.workdir + "/renamed.txt"
	renamed, err := r.sandbox.Files.Rename(r.ctx, textPath, renamedPath)
	if err != nil {
		r.recordIssue("Files.Rename failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
		probe, probeErr := r.sandbox.Commands.Run(
			r.ctx,
			"if test -e "+shellQuote(renamedPath)+"; then echo target; elif test -e "+shellQuote(textPath)+"; then echo source; else echo missing; fi",
		)
		if probeErr != nil {
			return fmt.Errorf("rename probe: %w", probeErr)
		}
		switch strings.TrimSpace(probe.Stdout) {
		case "target":
		case "source":
			if _, err := r.sandbox.Commands.Run(r.ctx, "mv "+shellQuote(textPath)+" "+shellQuote(renamedPath)); err != nil {
				return fmt.Errorf("rename fallback: %w", err)
			}
		default:
			return fmt.Errorf("rename left neither source nor target: source=%s target=%s", textPath, renamedPath)
		}
	} else if !strings.HasSuffix(renamed.Path, "/renamed.txt") {
		return fmt.Errorf("Rename path=%q", renamed.Path)
	} else {
		renamedPath = renamed.Path
	}
	if err := r.sandbox.Files.Remove(r.ctx, renamedPath); err != nil {
		r.recordIssue("Files.Remove failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
		if _, err := r.sandbox.Commands.Run(r.ctx, "rm -f "+shellQuote(renamedPath)); err != nil {
			return fmt.Errorf("remove fallback: %w", err)
		}
	}
	exists, err = r.sandbox.Files.Exists(r.ctx, renamedPath)
	if err != nil {
		r.recordIssue("Files.Exists after remove failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
	} else if exists {
		return fmt.Errorf("removed file still exists: %s", renamedPath)
	}
	return nil
}

func (r *e2eRun) verifyWatch() error {
	includeEntry := envdAtLeast(r.sandbox.EnvdVersion(), "0.6.3")
	if includeEntry {
		if err := r.verifyWatchOnce(true); err != nil {
			r.recordIssue("Files.WatchDir with entry info failed on envd %s: %v", r.sandbox.EnvdVersion(), err)
			return r.verifyWatchOnce(false)
		}
		return nil
	}
	return r.verifyWatchOnce(false)
}

func (r *e2eRun) verifyWatchOnce(includeEntry bool) error {
	opts := []e2b.WatchOption{e2b.WithRecursiveWatch(true)}
	if includeEntry {
		opts = append(opts, e2b.WithWatchEntryInfo(true))
	}
	watcher, err := r.sandbox.Files.WatchDir(r.ctx, r.workdir, opts...)
	if err != nil {
		return err
	}
	stopped := false
	defer func() {
		if stopped {
			return
		}
		if err := watcher.Stop(context.Background()); err != nil {
			fmt.Printf("watcher cleanup err=%v\n", err)
		}
	}()

	watchedPath := r.workdir + "/watched.txt"
	if _, err := r.writeText(watchedPath, "watch-ok\n"); err != nil {
		return err
	}
	events, err := pollWatcher(r.ctx, watcher, 5*time.Second)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("watcher returned no events")
	}
	if includeEntry {
		hasEntry := false
		for _, event := range events {
			if event.Entry != nil {
				hasEntry = true
				break
			}
		}
		if !hasEntry {
			return fmt.Errorf("watcher events did not include entry info: %+v", events)
		}
	}
	if err := watcher.Stop(r.ctx); err != nil {
		return err
	}
	stopped = true
	return nil
}

func (r *e2eRun) verifyPTY() error {
	pty, err := r.sandbox.Pty.Create(r.ctx, e2b.PtySize{Rows: 24, Cols: 80}, e2b.WithPtyTimeout(30*time.Second))
	if err != nil {
		return err
	}
	if err := r.sandbox.Pty.Resize(r.ctx, pty.PID(), e2b.PtySize{Rows: 30, Cols: 100}); err != nil {
		_, _ = pty.Kill(context.Background())
		return err
	}
	if err := pty.Disconnect(); err != nil {
		_, _ = pty.Kill(context.Background())
		return err
	}
	connected, err := r.sandbox.Pty.Connect(r.ctx, pty.PID(), 30*time.Second)
	if err != nil {
		return err
	}
	if err := r.sandbox.Pty.SendStdin(r.ctx, connected.PID(), []byte("echo pty-ok\nexit\n")); err != nil {
		_, _ = connected.Kill(context.Background())
		return err
	}
	var output strings.Builder
	result, err := connected.Wait(r.ctx, e2b.WithWaitPty(func(chunk []byte) {
		output.Write(chunk)
	}))
	if err != nil {
		return err
	}
	if result.ExitCode != 0 || !strings.Contains(output.String(), "pty-ok") {
		return fmt.Errorf("pty result=%+v output=%q", result, output.String())
	}
	return nil
}

func (r *e2eRun) verifyGit() error {
	repo := r.workdir + "/repo"
	remote := "/home/user/" + r.workdir + "/remote.git"
	clone := r.workdir + "/clone"

	if err := r.sandbox.Git.Init(r.ctx, remote, e2b.WithGitInitBare(true)); err != nil {
		return err
	}
	if err := r.sandbox.Git.Init(r.ctx, repo, e2b.WithGitInitialBranch("main")); err != nil {
		return err
	}
	if err := r.sandbox.Git.ConfigureUser(r.ctx, "E2B Go E2E", "go-sdk-e2e@example.com", "local", repo); err != nil {
		return err
	}
	if _, err := r.writeText(repo+"/README.md", "# e2e\n"); err != nil {
		return err
	}
	if err := r.sandbox.Git.Add(r.ctx, repo, []string{"README.md"}, false); err != nil {
		return err
	}
	if err := r.sandbox.Git.Commit(r.ctx, repo, "initial commit", "", "", false); err != nil {
		return err
	}
	status, err := r.sandbox.Git.Status(r.ctx, repo)
	if err != nil {
		return err
	}
	if !status.IsClean {
		return fmt.Errorf("git status not clean: %+v", status)
	}
	if err := r.sandbox.Git.RemoteAdd(r.ctx, repo, "origin", remote, false); err != nil {
		return err
	}
	remoteURL, err := r.sandbox.Git.RemoteGet(r.ctx, repo, "origin")
	if err != nil {
		return err
	}
	if remoteURL != remote {
		return fmt.Errorf("remote URL=%q, want %q", remoteURL, remote)
	}
	if err := r.sandbox.Git.Push(r.ctx, repo, "origin", "main", "", "", true); err != nil {
		return err
	}
	if err := r.sandbox.Git.Clone(r.ctx, remote, clone, e2b.WithGitCloneBranch("main")); err != nil {
		return err
	}
	if err := r.sandbox.Git.ConfigureUser(r.ctx, "E2B Go E2E", "go-sdk-e2e@example.com", "local", clone); err != nil {
		return err
	}
	branches, err := r.sandbox.Git.Branches(r.ctx, clone)
	if err != nil {
		return err
	}
	if branches.Current != "main" {
		return fmt.Errorf("current branch=%q", branches.Current)
	}
	if err := r.sandbox.Git.CreateBranch(r.ctx, clone, "feature/e2e"); err != nil {
		return err
	}
	if err := r.sandbox.Git.CheckoutBranch(r.ctx, clone, "feature/e2e"); err != nil {
		return err
	}
	if _, err := r.writeText(clone+"/README.md", "# changed\n"); err != nil {
		return err
	}
	if err := r.sandbox.Git.Add(r.ctx, clone, []string{"README.md"}, false); err != nil {
		return err
	}
	status, err = r.sandbox.Git.Status(r.ctx, clone)
	if err != nil {
		return err
	}
	if status.IsClean {
		return fmt.Errorf("git status unexpectedly clean after adding feature file")
	}
	if err := r.sandbox.Git.Reset(r.ctx, clone, e2b.GitResetMixed, "HEAD", nil); err != nil {
		return err
	}
	if err := r.sandbox.Git.Restore(r.ctx, clone, []string{"README.md"}, false, true, "HEAD"); err != nil {
		return err
	}
	if err := r.sandbox.Git.CheckoutBranch(r.ctx, clone, "main"); err != nil {
		return err
	}
	if err := r.sandbox.Git.DeleteBranch(r.ctx, clone, "feature/e2e", true); err != nil {
		return err
	}
	if err := r.sandbox.Git.SetConfig(r.ctx, "e2e.marker", "ok", "local", clone); err != nil {
		return err
	}
	marker, err := r.sandbox.Git.GetConfig(r.ctx, "e2e.marker", "local", clone)
	if err != nil {
		return err
	}
	if marker != "ok" {
		return fmt.Errorf("git config e2e.marker=%q", marker)
	}
	if err := r.sandbox.Git.Pull(r.ctx, clone, "origin", "main", "", ""); err != nil {
		return err
	}
	return nil
}

func (r *e2eRun) verifyNetworkAndMetrics() error {
	allow := true
	if err := r.sandbox.UpdateNetwork(r.ctx, e2b.SandboxNetworkUpdate{
		AllowInternetAccess: &allow,
		AllowOut:            []string{e2b.AllTraffic},
	}); err != nil {
		return err
	}
	if envdAtLeast(r.sandbox.EnvdVersion(), "0.1.5") {
		metrics, err := r.sandbox.GetMetrics(r.ctx, nil, nil)
		if err != nil {
			return err
		}
		fmt.Printf("metrics_points=%d\n", len(metrics))
	} else {
		fmt.Printf("skip metrics: envd %s < 0.1.5\n", r.sandbox.EnvdVersion())
	}

	result, err := r.sandbox.Commands.Run(r.ctx, `
if command -v curl >/dev/null 2>&1; then
  curl -fsS --max-time 10 https://example.com >/dev/null && echo curl-ok
elif command -v wget >/dev/null 2>&1; then
  wget -q -T 10 -O /dev/null https://example.com && echo wget-ok
else
  echo network-tool-missing
fi
`, e2b.WithCommandTimeout(30*time.Second))
	if err != nil {
		return err
	}
	fmt.Printf("egress_probe=%s\n", strings.TrimSpace(result.Stdout))
	return nil
}

func (r *e2eRun) verifySignedFileURLs() error {
	path := r.workdir + "/signed-url.txt"
	if _, err := r.writeText(path, "signed-url-ok\n"); err != nil {
		return err
	}
	expiration := 120
	downloadURL, err := r.sandbox.DownloadURL(path, nil, &expiration)
	if err != nil {
		return err
	}
	res, err := r.httpClient.Get(downloadURL)
	if err != nil {
		return err
	}
	body, err := readAndClose(res)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusForbidden && strings.Contains(string(body), "X-Access-Token header is required") {
			fmt.Printf("skip signed file urls: envd %s requires X-Access-Token header for direct file URLs\n", r.sandbox.EnvdVersion())
			return nil
		}
		return fmt.Errorf("download URL status=%d body=%s", res.StatusCode, string(body))
	}
	if strings.TrimSpace(string(body)) != "signed-url-ok" {
		return fmt.Errorf("download URL body=%q", string(body))
	}

	uploadPath := r.workdir + "/signed-upload.txt"
	uploadURL, err := r.sandbox.UploadURL(uploadPath, nil, &expiration)
	if err != nil {
		return err
	}
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", filepath.Base(uploadPath))
	if err != nil {
		return err
	}
	if _, err := io.WriteString(part, "signed-upload-ok\n"); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, uploadURL, &payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err = r.httpClient.Do(req)
	if err != nil {
		return err
	}
	body, err = readAndClose(res)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("upload URL status=%d body=%s", res.StatusCode, string(body))
	}
	text, err := r.sandbox.Files.Read(r.ctx, uploadPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) != "signed-upload-ok" {
		return fmt.Errorf("uploaded text=%q", text)
	}
	return nil
}

func (r *e2eRun) verifyErrorMapping() error {
	_, err := r.sandbox.Files.Read(r.ctx, r.workdir+"/missing.txt")
	var fileNotFound *e2b.FileNotFoundError
	if !errors.As(err, &fileNotFound) {
		return fmt.Errorf("missing file error=%T %v, want FileNotFoundError", err, err)
	}

	_, err = r.client.ConnectSandbox(r.ctx, "i000000000000000000000", 1)
	var notFound *e2b.NotFoundError
	var sandboxNotFound *e2b.SandboxNotFoundError
	if !errors.As(err, &notFound) && !errors.As(err, &sandboxNotFound) {
		var apiErr *e2b.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
			fmt.Printf("skip missing sandbox NotFound mapping: control plane rejected synthetic ID: %v\n", err)
		} else {
			r.recordIssue("ConnectSandbox missing sandbox returned %T %v, want NotFoundError/SandboxNotFoundError", err, err)
		}
	}

	err = r.sandbox.Git.Clone(r.ctx, "ext::sh -c 'echo bad'", "")
	var invalid *e2b.InvalidArgumentError
	if !errors.As(err, &invalid) {
		return fmt.Errorf("invalid git remote error=%T %v, want InvalidArgumentError", err, err)
	}
	return nil
}

func (r *e2eRun) createVolumeFixture() (*e2b.Volume, error) {
	name := "go-sdk-e2e-" + r.testID
	volume, err := r.client.CreateVolume(r.ctx, name)
	if err != nil {
		return nil, err
	}
	fmt.Printf("volume_id=%s volume_name=%s\n", volume.VolumeID(), volume.Name())
	if _, err := volume.MakeDir(r.ctx, "shared", e2b.WithVolumeMode(0o755)); err != nil {
		return nil, err
	}
	if _, err := volume.WriteFileText(r.ctx, "shared/from-volume.txt", "volume-mount-ok\n", e2b.WithVolumeMode(0o644)); err != nil {
		return nil, err
	}
	return volume, nil
}

func (r *e2eRun) verifyVolume(volume *e2b.Volume) error {
	info, err := r.client.GetVolumeInfo(r.ctx, volume.VolumeID())
	if err != nil {
		return err
	}
	if info.VolumeID != volume.VolumeID() || info.Token == "" {
		return fmt.Errorf("GetVolumeInfo=%+v", info)
	}
	volumes, err := r.client.ListVolumes(r.ctx)
	if err != nil {
		return err
	}
	if !containsVolume(volumes, volume.VolumeID()) {
		return fmt.Errorf("volume %s not found in ListVolumes", volume.VolumeID())
	}
	connected, err := r.client.ConnectVolume(r.ctx, volume.VolumeID())
	if err != nil {
		return err
	}
	if connected.VolumeID() != volume.VolumeID() {
		return fmt.Errorf("ConnectVolume id=%s, want %s", connected.VolumeID(), volume.VolumeID())
	}

	text, err := connected.ReadFile(r.ctx, "shared/from-volume.txt")
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) != "volume-mount-ok" {
		return fmt.Errorf("volume read=%q", text)
	}
	stream, err := connected.ReadFileStream(r.ctx, "shared/from-volume.txt")
	if err != nil {
		return err
	}
	streamed, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if strings.TrimSpace(string(streamed)) != "volume-mount-ok" {
		return fmt.Errorf("volume stream=%q", string(streamed))
	}
	entries, err := connected.List(r.ctx, "shared", e2b.WithVolumeDepth(1))
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("volume shared directory is empty")
	}
	stat, err := connected.UpdateMetadata(r.ctx, "shared/from-volume.txt", e2b.WithVolumeMode(0o600))
	if err != nil {
		return err
	}
	if stat.Mode == 0 {
		return fmt.Errorf("volume metadata update returned empty mode: %+v", stat)
	}
	exists, err := connected.Exists(r.ctx, "shared/from-volume.txt")
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("volume file does not exist")
	}

	result, err := r.sandbox.Commands.Run(r.ctx, "cat /mnt/e2b-go-sdk-e2e/shared/from-volume.txt", e2b.WithCommandTimeout(30*time.Second))
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.Stdout) != "volume-mount-ok" {
		return fmt.Errorf("mounted volume stdout=%q", result.Stdout)
	}

	if err := connected.Remove(r.ctx, "shared/from-volume.txt"); err != nil {
		return err
	}
	exists, err = connected.Exists(r.ctx, "shared/from-volume.txt")
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("removed volume file still exists")
	}
	return nil
}

func (r *e2eRun) verifyPauseAndReconnect() error {
	marker := "go-sdk-e2e-pause.txt"
	if _, err := r.writeText(marker, "pause-ok\n"); err != nil {
		return err
	}
	paused, err := r.sandbox.Pause(r.ctx)
	if err != nil {
		return err
	}
	if !paused {
		return fmt.Errorf("Pause returned false")
	}
	running, err := r.sandbox.IsRunning(r.ctx)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("sandbox still running after pause")
	}
	resumed, err := r.sandbox.Connect(r.ctx, 900)
	if err != nil {
		return err
	}
	r.sandbox = resumed
	running, err = r.sandbox.IsRunning(r.ctx)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("sandbox not running after reconnect")
	}
	text, err := r.sandbox.Files.Read(r.ctx, marker)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) != "pause-ok" {
		return fmt.Errorf("pause marker=%q", text)
	}
	return nil
}

func (r *e2eRun) verifySnapshot() error {
	marker := "go-sdk-e2e-snapshot.txt"
	if _, err := r.writeText(marker, "snapshot-ok\n"); err != nil {
		return err
	}
	name := "go-sdk-e2e-snapshot-" + r.testID
	snapshot, err := r.sandbox.CreateSnapshot(r.ctx, name)
	if err != nil {
		return err
	}
	fmt.Printf("snapshot_id=%s names=%v\n", snapshot.SnapshotID, snapshot.Names)
	snapshotDeleted := false
	defer func() {
		if !snapshotDeleted {
			deleted, err := r.client.DeleteSnapshot(context.Background(), snapshot.SnapshotID)
			fmt.Printf("snapshot cleanup deleted=%v err=%v\n", deleted, err)
		}
	}()

	page, err := r.client.ListSnapshots(r.ctx, r.sandbox.SandboxID(), 20, "")
	if err != nil {
		return err
	}
	if !containsSnapshot(page.Items, snapshot.SnapshotID) {
		return fmt.Errorf("snapshot %s not found in ListSnapshots", snapshot.SnapshotID)
	}

	clone, err := r.client.CreateSandbox(r.ctx, e2b.WithTemplate(snapshot.SnapshotID), e2b.WithTimeout(600))
	if err != nil {
		return err
	}
	cloneKilled := false
	defer func() {
		if !cloneKilled {
			killed, err := clone.Kill(context.Background())
			fmt.Printf("snapshot sandbox cleanup killed=%v err=%v\n", killed, err)
		}
	}()

	text, err := clone.Files.Read(r.ctx, marker)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) != "snapshot-ok" {
		return fmt.Errorf("snapshot marker=%q", text)
	}
	killed, err := clone.Kill(r.ctx)
	if err != nil {
		return err
	}
	cloneKilled = true
	if !killed {
		return fmt.Errorf("snapshot sandbox Kill returned false")
	}
	deleted, err := r.client.DeleteSnapshot(r.ctx, snapshot.SnapshotID)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("DeleteSnapshot returned false")
	}
	snapshotDeleted = true
	deletedAgain, err := r.client.DeleteSnapshot(r.ctx, snapshot.SnapshotID)
	if err != nil {
		return err
	}
	if deletedAgain {
		return fmt.Errorf("DeleteSnapshot returned true for already deleted snapshot")
	}
	return nil
}

func (r *e2eRun) verifyTemplateBuild() error {
	tmp, err := os.MkdirTemp("", "e2b-go-sdk-e2e-template-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "fixture.txt"), []byte("copy-ok\n"), 0o644); err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(tmp); err != nil {
		return err
	}
	defer func() {
		_ = os.Chdir(wd)
	}()

	name := "go-sdk-e2e-template-" + r.testID
	initialTag := "go-sdk-e2e"
	copyExpected := true
	build, err := r.client.BuildTemplate(
		r.ctx,
		e2b.NewTemplate().
			FromBaseTemplate(r.template).
			RunCmd("mkdir -p /tmp/e2b-go-sdk-e2e && echo run-ok > /tmp/e2b-go-sdk-e2e/run.txt").
			Copy("fixture.txt", "/tmp/e2b-go-sdk-e2e/copy.txt").
			SetEnv("E2B_GO_SDK_TEMPLATE_E2E", "env-ok"),
		name,
		e2b.WithTemplateCPUCount(1),
		e2b.WithTemplateMemoryMB(1024),
		e2b.WithTemplateTags(initialTag),
		e2b.WithTemplatePollPeriod(5*time.Second),
	)
	if err != nil {
		r.recordIssue("BuildTemplate with COPY failed: %v", err)
		copyExpected = false
		name += "-nocopy"
		build, err = r.client.BuildTemplate(
			r.ctx,
			e2b.NewTemplate().
				FromBaseTemplate(r.template).
				RunCmd("mkdir -p /tmp/e2b-go-sdk-e2e && echo run-ok > /tmp/e2b-go-sdk-e2e/run.txt").
				SetEnv("E2B_GO_SDK_TEMPLATE_E2E", "env-ok"),
			name,
			e2b.WithTemplateCPUCount(1),
			e2b.WithTemplateMemoryMB(1024),
			e2b.WithTemplateTags(initialTag),
			e2b.WithTemplatePollPeriod(5*time.Second),
		)
		if err != nil {
			return err
		}
	}
	fmt.Printf("template_id=%s build_id=%s name=%s\n", build.TemplateID, build.BuildID, build.Name)
	defer func() {
		deleted, err := r.client.DeleteTemplate(context.Background(), build.TemplateID)
		fmt.Printf("template cleanup deleted=%v err=%v\n", deleted, err)
	}()

	if exists, err := r.client.TemplateExists(r.ctx, name); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("TemplateExists(%s) returned false", name)
	}
	templates, err := r.client.ListTemplates(r.ctx, "")
	if err != nil {
		return err
	}
	if !containsTemplate(templates, build.TemplateID) {
		return fmt.Errorf("template %s not found in ListTemplates", build.TemplateID)
	}
	details, err := r.client.GetTemplate(r.ctx, build.TemplateID, 20, "")
	if err != nil {
		return err
	}
	if details.TemplateID != build.TemplateID || len(details.Builds) == 0 {
		return fmt.Errorf("GetTemplate details=%+v", details)
	}
	taggedName := name + ":" + initialTag
	if _, err := r.client.AssignTemplateTags(r.ctx, taggedName, []string{"go-sdk-e2e-assigned"}); err != nil {
		r.recordIssue("AssignTemplateTags(%s) failed: %v", taggedName, err)
	} else {
		tags, err := r.client.GetTemplateTags(r.ctx, build.TemplateID)
		if err != nil {
			return err
		}
		if !containsTemplateTag(tags, "go-sdk-e2e-assigned") {
			return fmt.Errorf("assigned template tag not found: %+v", tags)
		}
		if err := r.client.RemoveTemplateTags(r.ctx, name, []string{"go-sdk-e2e-assigned"}); err != nil {
			return err
		}
	}

	templateRef := build.TemplateID + ":" + initialTag
	sandbox, err := r.client.CreateSandbox(r.ctx, e2b.WithTemplate(templateRef), e2b.WithTimeout(600))
	if err != nil {
		return err
	}
	defer func() {
		killed, err := sandbox.Kill(context.Background())
		fmt.Printf("template sandbox cleanup killed=%v err=%v\n", killed, err)
	}()
	result, err := sandbox.Commands.Run(
		r.ctx,
		"cat /tmp/e2b-go-sdk-e2e/run.txt; if test -f /tmp/e2b-go-sdk-e2e/copy.txt; then cat /tmp/e2b-go-sdk-e2e/copy.txt; fi; printf '%s\\n' \"$E2B_GO_SDK_TEMPLATE_E2E\"",
		e2b.WithCommandTimeout(60*time.Second),
		e2b.WithCommandRequestTimeout(120*time.Second),
	)
	if err != nil {
		return err
	}
	stdout := result.Stdout
	wants := []string{"run-ok", "env-ok"}
	if copyExpected {
		wants = append(wants, "copy-ok")
	}
	for _, want := range wants {
		if !strings.Contains(stdout, want) {
			r.recordIssue("template sandbox stdout=%q missing %q", stdout, want)
		}
	}
	return nil
}

func (r *e2eRun) recordIssue(format string, args ...any) {
	issue := fmt.Sprintf(format, args...)
	r.issues = append(r.issues, issue)
	fmt.Println("issue:", issue)
}

func (r *e2eRun) writeText(path, data string, opts ...e2b.FileOption) (e2b.WriteInfo, error) {
	return r.sandbox.Files.WriteText(r.ctx, path, data, octetFileOptions(opts...)...)
}

func (r *e2eRun) writeBytes(path string, data []byte, opts ...e2b.FileOption) (e2b.WriteInfo, error) {
	return r.sandbox.Files.WriteBytes(r.ctx, path, data, octetFileOptions(opts...)...)
}

func octetFileOptions(opts ...e2b.FileOption) []e2b.FileOption {
	result := make([]e2b.FileOption, 0, len(opts)+1)
	result = append(result, e2b.WithOctetStreamUpload(true))
	result = append(result, opts...)
	return result
}

func runStep(name string, fn func() error) bool {
	return step(name, fn) == nil
}

func step(name string, fn func() error) error {
	fmt.Printf("\n== %s ==\n", name)
	start := time.Now()
	if err := fn(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %s after %s: %v\n", name, time.Since(start).Round(time.Millisecond), err)
		return err
	}
	fmt.Printf("ok %s (%s)\n", name, time.Since(start).Round(time.Millisecond))
	return nil
}

func (r *e2eRun) killSandbox() {
	if r.sandbox == nil || enabled("E2B_E2E_KEEP_SANDBOX") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	killed, err := r.sandbox.Kill(ctx)
	fmt.Printf("sandbox cleanup killed=%v err=%v\n", killed, err)
}

func (r *e2eRun) destroyVolume(volume *e2b.Volume) {
	if volume == nil || enabled("E2B_E2E_KEEP_VOLUME") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	destroyed, err := r.client.DestroyVolume(ctx, volume.VolumeID())
	fmt.Printf("volume cleanup destroyed=%v err=%v\n", destroyed, err)
}

func pollWatcher(ctx context.Context, watcher *e2b.WatchHandle, timeout time.Duration) ([]e2b.FilesystemEvent, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := watcher.GetNewEvents(ctx)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			return events, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, nil
		case <-ticker.C:
		}
	}
}

func readAndClose(res *http.Response) ([]byte, error) {
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

func containsSandbox(items []e2b.SandboxInfo, sandboxID string) bool {
	for _, item := range items {
		if item.SandboxID == sandboxID {
			return true
		}
	}
	return false
}

func containsProcess(items []e2b.ProcessInfo, pid int) bool {
	for _, item := range items {
		if item.PID == pid {
			return true
		}
	}
	return false
}

func containsVolume(items []e2b.VolumeInfo, volumeID string) bool {
	for _, item := range items {
		if item.VolumeID == volumeID {
			return true
		}
	}
	return false
}

func containsSnapshot(items []e2b.SnapshotInfo, snapshotID string) bool {
	for _, item := range items {
		if item.SnapshotID == snapshotID {
			return true
		}
	}
	return false
}

func containsTemplate(items []e2b.TemplateInfo, templateID string) bool {
	for _, item := range items {
		if item.TemplateID == templateID {
			return true
		}
	}
	return false
}

func containsTemplateTag(items []e2b.TemplateTag, tag string) bool {
	for _, item := range items {
		if item.Tag == tag {
			return true
		}
	}
	return false
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func enabled(key string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func envdAtLeast(got, want string) bool {
	return compareVersion(got, want) >= 0
}

func compareVersion(a, b string) int {
	as := parseVersion(a)
	bs := parseVersion(b)
	for i := 0; i < 3; i++ {
		if as[i] < bs[i] {
			return -1
		}
		if as[i] > bs[i] {
			return 1
		}
	}
	return 0
}

func parseVersion(raw string) [3]int {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	var out [3]int
	parts := strings.Split(raw, ".")
	for i := 0; i < len(parts) && i < len(out); i++ {
		part := parts[i]
		if idx := strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }); idx >= 0 {
			part = part[:idx]
		}
		n, _ := strconv.Atoi(part)
		out[i] = n
	}
	return out
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
