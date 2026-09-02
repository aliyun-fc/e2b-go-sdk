package sample

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"
)

// RunSandbox runs the sandbox API sample.
func RunSandbox(ctx context.Context) {
	apiKey := env("E2B_API_KEY", "")
	if apiKey == "" {
		log.Fatal("set E2B_API_KEY first")
	}

	apiURL := normalizeAPIURL(env("E2B_API_URL", defaultAPIURL))
	domain := env("E2B_DOMAIN", defaultDomain)
	templateName := env("E2B_SAMPLE_TEMPLATE", defaultTemplate)
	fmt.Printf("using api_url=%s domain=%s template=%s\n", apiURL, domain, templateName)

	client, err := e2b.NewClient(
		e2b.WithAPIKey(apiKey),
		e2b.WithAPIURL(apiURL),
		e2b.WithDomain(domain),
		e2b.WithIntegration("e2b-go-sdk-sandbox-sample/1.0"),
	)
	must("create client", err)

	runSandboxSample(ctx, client, templateName)
}

func runSandboxSample(ctx context.Context, client *e2b.Client, templateName string) {
	section("sandbox lifecycle")
	sandbox, err := client.CreateSandbox(
		ctx,
		e2b.WithTemplate(templateName),
		e2b.WithTimeout(900),
		e2b.WithMetadata(map[string]string{"sample": "go-sdk-sandbox"}),
		e2b.WithEnv("E2B_SAMPLE", "true"),
		e2b.WithInternetAccess(true),
	)
	must("create sandbox", err)
	defer func() {
		killed, err := sandbox.Kill(context.Background())
		fmt.Printf("sandbox killed: %v err=%v\n", killed, err)
	}()

	fmt.Println("sandbox_id:", sandbox.SandboxID())
	fmt.Println("envd_api_url:", sandbox.EnvdAPIURL())
	fmt.Println("host_8000:", sandbox.GetHost(8000))

	running, err := sandbox.IsRunning(ctx)
	must("health check", err)
	fmt.Println("is_running:", running)

	info, err := sandbox.GetInfo(ctx)
	must("get sandbox info", err)
	fmt.Printf("template=%s state=%s envd=%s cpu=%d memMB=%d\n",
		info.TemplateID, info.State, info.EnvdVersion, info.CPUCount, info.MemoryMB)

	must("extend timeout", sandbox.SetTimeout(ctx, 900))

	runCommandsSample(ctx, sandbox)
	workdir := runFilesystemSample(ctx, sandbox)
	runGitSample(ctx, sandbox, workdir)
	runPtySample(ctx, sandbox)
	runNetworkSample(ctx, sandbox)
	runListAPISample(ctx, client)
	runStreamSample(ctx, sandbox, workdir)
}

func runCommandsSample(ctx context.Context, sandbox *e2b.Sandbox) {
	section("commands")
	envResult, err := sandbox.Commands.Run(ctx, "env | sort")
	must("run env", err)
	fmt.Println(firstLines(envResult.Stdout, 12))

	bg, err := sandbox.Commands.Start(ctx, "for i in 1 2 3; do echo tick-$i; sleep 1; done")
	must("start background command", err)
	bgResult, err := bg.Wait(ctx, e2b.WithWaitStdout(func(chunk string) {
		fmt.Print("stream stdout: ", chunk)
	}))
	must("wait background command", err)
	fmt.Printf("background exit=%d\n", bgResult.ExitCode)

	processes, err := sandbox.Commands.List(ctx)
	must("list processes", err)
	fmt.Printf("running process count: %d\n", len(processes))
}

func runFilesystemSample(ctx context.Context, sandbox *e2b.Sandbox) string {
	section("filesystem")
	workdir := "/tmp/e2b-go-sdk-sample"
	filePath := workdir + "/hello.txt"
	renamedPath := workdir + "/hello-renamed.txt"

	_, err := sandbox.Commands.Run(ctx, "rm -rf "+workdir)
	must("cleanup old sample directory", err)
	_, err = sandbox.Files.MakeDir(ctx, workdir)
	must("mkdir", err)

	writeInfo, err := sandbox.Files.WriteText(
		ctx,
		filePath,
		"hello from the E2B Go SDK\n",
	)
	must("write file", err)
	fmt.Printf("wrote file: %s type=%s\n", writeInfo.Path, writeInfo.Type)

	content, err := sandbox.Files.Read(ctx, filePath)
	must("read file", err)
	fmt.Printf("read file: %q\n", strings.TrimSpace(content))

	exists, err := sandbox.Files.Exists(ctx, filePath)
	must("exists", err)
	fmt.Println("file exists:", exists)

	entry, err := sandbox.Files.GetInfo(ctx, filePath)
	must("get file info", err)
	fmt.Printf("file info: name=%s size=%d mode=%o\n", entry.Name, entry.Size, entry.Mode)

	list, err := sandbox.Files.List(ctx, workdir, e2b.WithListDepth(1))
	must("list directory", err)
	fmt.Printf("directory entries: %d\n", len(list))

	watcher, err := sandbox.Files.WatchDir(ctx, workdir)
	must("watch directory", err)
	_, err = sandbox.Files.WriteText(ctx, workdir+"/watched.txt", "watch me\n")
	must("write watched file", err)
	time.Sleep(500 * time.Millisecond)
	events, err := watcher.GetNewEvents(ctx)
	must("get watcher events", err)
	fmt.Printf("watch events: %d\n", len(events))
	must("stop watcher", watcher.Stop(ctx))

	renamed, err := sandbox.Files.Rename(ctx, filePath, renamedPath)
	must("rename file", err)
	fmt.Println("renamed to:", renamed.Path)
	must("remove renamed file", sandbox.Files.Remove(ctx, renamedPath))

	return workdir
}

func runGitSample(ctx context.Context, sandbox *e2b.Sandbox, workdir string) {
	section("git")
	repoPath := workdir + "/repo"
	must("git init", sandbox.Git.Init(ctx, repoPath))
	must("git configure user", sandbox.Git.ConfigureUser(ctx, "E2B Go Sample", "sample@example.com", "local", repoPath))
	_, err := sandbox.Files.WriteText(ctx, repoPath+"/README.md", "# E2B Go SDK sample\n")
	must("write repo file", err)
	must("git add", sandbox.Git.Add(ctx, repoPath, []string{"README.md"}, false))
	must("git commit", sandbox.Git.Commit(ctx, repoPath, "initial sample commit", "", "", false))
	status, err := sandbox.Git.Status(ctx, repoPath)
	must("git status", err)
	fmt.Printf("git clean=%v files=%d\n", status.IsClean, len(status.Files))
	branches, err := sandbox.Git.Branches(ctx, repoPath)
	must("git branches", err)
	fmt.Printf("git current branch=%s locals=%v\n", branches.Current, branches.Local)
}

func runPtySample(ctx context.Context, sandbox *e2b.Sandbox) {
	section("pty")
	pty, err := sandbox.Pty.Create(ctx, e2b.PtySize{Rows: 24, Cols: 80})
	must("create pty", err)
	must("send pty input", sandbox.Pty.SendStdin(ctx, pty.PID(), []byte("echo pty-ok\nexit\n")))
	ptyResult, err := pty.Wait(ctx, e2b.WithWaitPty(func(chunk []byte) {
		fmt.Print(string(chunk))
	}))
	must("wait pty", err)
	fmt.Printf("pty exit=%d\n", ptyResult.ExitCode)
}

func runNetworkSample(ctx context.Context, sandbox *e2b.Sandbox) {
	section("network and metrics")
	must("update network", sandbox.UpdateNetwork(ctx, e2b.SandboxNetworkUpdate{
		AllowInternetAccess: boolPtr(true),
		AllowOut:            []string{e2b.AllTraffic},
	}))
	metrics, err := sandbox.GetMetrics(ctx, nil, nil)
	must("get metrics", err)
	fmt.Printf("metric points: %d\n", len(metrics))
}

func runListAPISample(ctx context.Context, client *e2b.Client) {
	section("list APIs")
	sandboxes, err := client.ListSandboxes(ctx, &e2b.SandboxQuery{
		Metadata: map[string]string{"sample": "go-sdk-sandbox"},
	}, 10, "")
	must("list sandboxes", err)
	fmt.Printf("listed sandboxes: %d has_next=%v\n", len(sandboxes.Items), sandboxes.HasNext)
}

func runStreamSample(ctx context.Context, sandbox *e2b.Sandbox, workdir string) {
	stream, err := sandbox.Files.ReadStream(ctx, workdir+"/watched.txt")
	must("stream file", err)
	defer stream.Close()
	streamed, err := io.ReadAll(stream)
	must("read stream", err)
	fmt.Printf("streamed file: %q\n", strings.TrimSpace(string(streamed)))
}
