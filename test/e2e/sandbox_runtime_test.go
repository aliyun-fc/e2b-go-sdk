package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"
)

const (
	sandboxRuntimeE2EFlag       = "E2B_SANDBOX_RUNTIME_E2E"
	legacySandboxRuntimeE2EFlag = "E2B_RUNTIME_E2E"
)

// TestSandboxRuntimeModules exercises the in-sandbox runtime modules
// (filesystem, commands, etc.) against a real control plane. It is skipped
// unless E2B_SANDBOX_RUNTIME_E2E (or the legacy E2B_RUNTIME_E2E) is set.
func TestSandboxRuntimeModules(t *testing.T) {
	if !enabledAny(sandboxRuntimeE2EFlag, legacySandboxRuntimeE2EFlag) {
		t.Skip("set E2B_SANDBOX_RUNTIME_E2E=1 to run real sandbox runtime module e2e tests")
	}

	apiKey := env("E2B_API_KEY", env("E2B_OFFICIAL_API_KEY", ""))
	if apiKey == "" {
		t.Fatal("set E2B_API_KEY or E2B_OFFICIAL_API_KEY")
	}

	ctx, cancel := context.WithTimeout(context.Background(), envDurationAny([]string{
		"E2B_SANDBOX_RUNTIME_E2E_TIMEOUT_SECONDS",
		"E2B_RUNTIME_E2E_TIMEOUT_SECONDS",
	}, 25*time.Minute))
	defer cancel()

	client, err := newSandboxE2EClient(
		apiKey,
		"e2b-go-sdk-runtime-e2e/1.0",
		envDurationAny([]string{
			"E2B_SANDBOX_RUNTIME_E2E_REQUEST_TIMEOUT_SECONDS",
			"E2B_RUNTIME_E2E_REQUEST_TIMEOUT_SECONDS",
		}, 120*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	testID := time.Now().UTC().Format("20060102150405")
	run := &e2eRun{
		ctx:        ctx,
		client:     client,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		testID:     testID,
		template:   env("E2B_E2E_TEMPLATE", e2b.DefaultTemplate),
		workdir:    "e2b-go-sdk-e2e-runtime-" + testID,
	}

	if err := run.createSandbox(nil); err != nil {
		t.Fatalf("create sandbox fixture: %v", err)
	}
	t.Cleanup(run.killSandbox)

	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{name: "commands", fn: run.verifyCommands},
		{name: "filesystem", fn: run.verifyFilesystem},
		{name: "filesystem_watch", fn: run.verifyWatch},
		{name: "pty", fn: run.verifyPTY},
		{name: "git", fn: run.verifyGit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertNoNewRuntimeIssues(t, run, tc.fn)
		})
	}
}
