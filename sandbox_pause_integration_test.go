package e2b

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type manualPauseProbe struct {
	path string
	sha  string
}

type manualPauseState struct {
	Boot    string `json:"boot"`
	PID     int    `json:"pid"`
	Process bool   `json:"process"`
	Memory  string `json:"memory"`
	SHA     string `json:"sha"`
	Mode    string `json:"mode"`
	Link    string `json:"link"`
	SHM     bool   `json:"shm"`
}

func manualPauseCommand(t *testing.T, ctx context.Context, s *Sandbox, cmd string) string {
	t.Helper()
	r, err := s.Commands.Run(ctx, cmd, WithCommandUser("root"), WithCommandTimeout(30*time.Second), WithCommandRequestTimeout(40*time.Second))
	if err != nil {
		t.Fatalf("command: %v (stderr=%s)", err, r.Stderr)
	}
	if r.ExitCode != 0 {
		t.Fatalf("command exit=%d stderr=%s", r.ExitCode, r.Stderr)
	}
	return strings.TrimSpace(r.Stdout)
}

func setupManualPauseProbe(t *testing.T, ctx context.Context, s *Sandbox, run, label string) (manualPauseProbe, manualPauseState) {
	t.Helper()
	path := "/root/" + run + "-" + label
	payload := fmt.Sprintf("%s-%d\n%s", label, time.Now().UnixNano(), strings.Repeat("disk-marker\n", 100))
	source := `import http.server,json,os,uuid
token=uuid.uuid4().hex
class H(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.end_headers();self.wfile.write(json.dumps(dict(token=token,pid=os.getpid())).encode())
 def log_message(self,*a): pass
http.server.HTTPServer(('127.0.0.1',18080),H).serve_forever()
`
	manualPauseCommand(t, ctx, s, fmt.Sprintf("mkdir -p %s; printf %%s %s > %s/payload; chmod 640 %s/payload; ln -s payload %s/link; printf %%s %s > %s/probe.py; printf marker > /dev/shm/%s; nohup python3 -u %s/probe.py > %s/server.log 2>&1 < /dev/null & echo $! > %s/pid", path, shellQuote(payload), path, path, path, shellQuote(source), path, run, path, path, path))
	p := manualPauseProbe{path: path, sha: fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))}
	for i := 0; i < 15; i++ {
		state := readManualPauseProbe(t, ctx, s, p, run)
		if state.Memory != "" && state.Process && state.SHM && state.SHA == p.sha && state.Mode == "0o640" && state.Link == "payload" {
			return p, state
		}
		time.Sleep(time.Second)
	}
	t.Fatal("probe process did not become ready")
	return p, manualPauseState{}
}

func readManualPauseProbe(t *testing.T, ctx context.Context, s *Sandbox, p manualPauseProbe, run string) manualPauseState {
	t.Helper()
	code := fmt.Sprintf(`import os,json,hashlib,urllib.request
p=%q
pid=int(open(p+'/pid').read()) if os.path.exists(p+'/pid') else -1
try: cmd=open('/proc/%%s/cmdline'%%pid,'rb').read().decode(errors='replace')
except FileNotFoundError: cmd=''
try: memory=json.load(urllib.request.urlopen('http://127.0.0.1:18080',timeout=2))['token']
except Exception: memory=''
print(json.dumps(dict(boot=open('/proc/sys/kernel/random/boot_id').read().strip(),pid=pid,process=(p+'/probe.py') in cmd,memory=memory,sha=hashlib.sha256(open(p+'/payload','rb').read()).hexdigest() if os.path.exists(p+'/payload') else '',mode=oct(os.stat(p+'/payload').st_mode & 0o777) if os.path.exists(p+'/payload') else '',link=os.readlink(p+'/link') if os.path.islink(p+'/link') else '',shm=os.path.exists('/dev/shm/'+%q))))
`, p.path, run)
	var state manualPauseState
	if err := json.Unmarshal([]byte(manualPauseCommand(t, ctx, s, "python3 -c "+shellQuote(code))), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestFilesystemOnlyPauseConnect(t *testing.T) {
	if !envEnabled("E2B_FILESYSTEM_PAUSE_E2E") {
		t.Skip("set E2B_FILESYSTEM_PAUSE_E2E=1 to run live pause tests")
	}
	for _, name := range []string{"E2B_API_KEY", "E2B_API_URL", "E2B_DOMAIN", "E2B_E2E_TEMPLATE"} {
		if os.Getenv(name) == "" {
			t.Fatalf("missing %s", name)
		}
	}
	c, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
			defer cancel()
			opts := []SandboxCreateOption{WithTemplate(os.Getenv("E2B_E2E_TEMPLATE")), WithTimeout(600)}
			if mode == "auto" {
				opts = append(opts, WithLifecycle(SandboxLifecycle{OnTimeout: "pause"}), WithAutoPauseMemory(false))
			}
			s, err := c.CreateSandbox(ctx, opts...)
			if err != nil {
				t.Fatal(err)
			}
			id := s.SandboxID()
			t.Log("created", id)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				if _, err := c.KillSandbox(ctx, id); err != nil {
					t.Errorf("cleanup %s: %v", id, err)
				}
			})
			run := fmt.Sprintf("golive-%d", time.Now().UnixNano())
			p, before := setupManualPauseProbe(t, ctx, s, run, mode)
			t.Logf("before pid=%d live=%t sha=%s", before.PID, before.Process, before.SHA)
			if mode == "manual" {
				res, err := s.Pause(ctx, WithPauseMemory(false))
				if err != nil || !res {
					t.Fatalf("pause %v %v", res, err)
				}
			}
			for {
				info, err := s.GetInfo(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if info.State == SandboxStatePaused {
					break
				}
				time.Sleep(5 * time.Second)
			}
			t.Log("observed paused")
			s, err = c.ConnectSandbox(ctx, id, 600)
			if err != nil {
				t.Fatal(err)
			}
			if s.SandboxID() != id {
				t.Fatal("sandbox ID changed")
			}
			after := readManualPauseProbe(t, ctx, s, p, run)
			t.Logf("after live=%t memoryTokenPresent=%t sha=%s", after.Process, after.Memory != "", after.SHA)
			if before.SHA != after.SHA || after.Process || after.Memory != "" {
				t.Fatal("filesystem-only verification failed")
			}
		})
	}
}
