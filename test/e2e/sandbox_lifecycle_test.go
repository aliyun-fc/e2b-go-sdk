package main

import (
	"context"
	"testing"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"
)

const (
	sandboxLifecycleE2EFlag       = "E2B_SANDBOX_LIFECYCLE_E2E"
	legacySandboxLifecycleE2EFlag = "E2B_SANDBOX_E2E"
)

// TestSandboxLifecycle exercises the full sandbox lifecycle (create, query,
// timeout, stop) against a real control plane. It is skipped unless
// E2B_SANDBOX_LIFECYCLE_E2E (or the legacy E2B_SANDBOX_E2E) is set.
func TestSandboxLifecycle(t *testing.T) {
	if !enabledAny(sandboxLifecycleE2EFlag, legacySandboxLifecycleE2EFlag) {
		t.Skip("set E2B_SANDBOX_LIFECYCLE_E2E=1 to run the real sandbox lifecycle e2e test")
	}

	apiKey := env("E2B_API_KEY", env("E2B_OFFICIAL_API_KEY", ""))
	if apiKey == "" {
		t.Fatal("set E2B_API_KEY or E2B_OFFICIAL_API_KEY")
	}

	ctx, cancel := context.WithTimeout(context.Background(), envDurationAny([]string{
		"E2B_SANDBOX_LIFECYCLE_E2E_TIMEOUT_SECONDS",
		"E2B_SANDBOX_E2E_TIMEOUT_SECONDS",
	}, 12*time.Minute))
	defer cancel()

	client, err := newSandboxE2EClient(
		apiKey,
		"e2b-go-sdk-sandbox-e2e/1.0",
		envDurationAny([]string{
			"E2B_SANDBOX_LIFECYCLE_E2E_REQUEST_TIMEOUT_SECONDS",
			"E2B_SANDBOX_E2E_REQUEST_TIMEOUT_SECONDS",
		}, 120*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	testID := time.Now().UTC().Format("20060102150405")
	templateName := env("E2B_E2E_TEMPLATE", e2b.DefaultTemplate)
	metadata := map[string]string{
		"go_sdk_e2e":      "true",
		"go_sdk_e2e_case": "sandbox_lifecycle",
		"go_sdk_e2e_id":   testID,
	}

	var sandbox *e2b.Sandbox
	killed := false
	t.Cleanup(func() {
		if sandbox == nil || killed || enabled("E2B_E2E_KEEP_SANDBOX") {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		cleanupKilled, err := sandbox.Kill(cleanupCtx)
		t.Logf("sandbox cleanup: killed=%v err=%v", cleanupKilled, err)
	})

	t.Run("create", func(t *testing.T) {
		created, err := client.CreateSandbox(
			ctx,
			e2b.WithTemplate(templateName),
			e2b.WithTimeout(300),
			e2b.WithMetadata(metadata),
			e2b.WithEnv("E2B_GO_SDK_E2E", testID),
			e2b.WithInternetAccess(true),
		)
		if err != nil {
			t.Fatalf("CreateSandbox: %v", err)
		}
		if created.SandboxID() == "" {
			t.Fatal("CreateSandbox returned an empty sandbox ID")
		}
		if created.EnvdAPIURL() == "" {
			t.Fatal("CreateSandbox returned an empty envd API URL")
		}
		sandbox = created
		t.Logf("sandbox_id=%s domain=%s envd=%s", sandbox.SandboxID(), sandbox.SandboxDomain(), sandbox.EnvdVersion())
	})

	t.Run("query_status", func(t *testing.T) {
		requireSandbox(t, sandbox)

		running, err := sandbox.IsRunning(ctx)
		if err != nil {
			t.Fatalf("IsRunning: %v", err)
		}
		if !running {
			t.Fatal("sandbox is not running after create")
		}

		info, err := sandbox.GetInfo(ctx)
		if err != nil {
			t.Fatalf("GetInfo: %v", err)
		}
		assertSandboxInfo(t, info, sandbox.SandboxID(), metadata)

		if err := waitForSandboxInList(ctx, client, sandbox.SandboxID(), testID); err != nil {
			t.Fatalf("ListSandboxes metadata query: %v", err)
		}
	})

	t.Run("connect", func(t *testing.T) {
		requireSandbox(t, sandbox)

		connected, err := client.ConnectSandbox(ctx, sandbox.SandboxID(), 600)
		if err != nil {
			t.Fatalf("ConnectSandbox: %v", err)
		}
		if connected.SandboxID() != sandbox.SandboxID() {
			t.Fatalf("connected sandboxID = %q, want %q", connected.SandboxID(), sandbox.SandboxID())
		}
		sandbox = connected

		result, err := sandbox.Commands.Run(ctx, "printf '%s' \"$E2B_GO_SDK_E2E\"", e2b.WithCommandTimeout(30*time.Second))
		if err != nil {
			t.Fatalf("run command on connected sandbox: %v", err)
		}
		if result.Stdout != testID {
			t.Fatalf("connected sandbox env stdout = %q, want %q", result.Stdout, testID)
		}
	})

	t.Run("extend_timeout", func(t *testing.T) {
		requireSandbox(t, sandbox)

		before, err := sandbox.GetInfo(ctx)
		if err != nil {
			t.Fatalf("GetInfo before SetTimeout: %v", err)
		}
		if err := sandbox.SetTimeout(ctx, 900); err != nil {
			t.Fatalf("SetTimeout: %v", err)
		}

		after, err := waitForSandboxTimeoutAtLeast(ctx, sandbox, 10*time.Minute)
		if err != nil {
			t.Fatalf("timeout extension not visible: %v", err)
		}
		if !before.EndAt.IsZero() && !after.EndAt.After(before.EndAt) {
			t.Fatalf("EndAt did not move forward after SetTimeout: before=%s after=%s", before.EndAt, after.EndAt)
		}
		t.Logf("timeout extended: before_end_at=%s after_end_at=%s", before.EndAt, after.EndAt)
	})

	t.Run("stop", func(t *testing.T) {
		requireSandbox(t, sandbox)

		stopped, err := sandbox.Kill(ctx)
		if err != nil {
			t.Fatalf("Kill: %v", err)
		}
		if !stopped {
			t.Fatal("Kill returned false for a running sandbox")
		}

		if err := waitForSandboxStopped(ctx, client, sandbox.SandboxID()); err != nil {
			t.Fatalf("sandbox did not stop: %v", err)
		}
		killed = true
	})
}
