package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	e2b "github.com/e2b-dev/e2b-go-sdk"
)

const sandboxAdvancedE2EFlag = "E2B_SANDBOX_ADVANCED_E2E"

func TestSandboxAdvancedFeatures(t *testing.T) {
	if !enabled(sandboxAdvancedE2EFlag) {
		t.Skip("set E2B_SANDBOX_ADVANCED_E2E=1 to run real sandbox advanced e2e tests")
	}

	apiKey := env("E2B_API_KEY", env("E2B_OFFICIAL_API_KEY", ""))
	if apiKey == "" {
		t.Fatal("set E2B_API_KEY or E2B_OFFICIAL_API_KEY")
	}

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("E2B_SANDBOX_ADVANCED_E2E_TIMEOUT_SECONDS", 30*time.Minute))
	defer cancel()

	client, err := newSandboxE2EClient(
		apiKey,
		"e2b-go-sdk-sandbox-advanced-e2e/1.0",
		envDuration("E2B_SANDBOX_ADVANCED_E2E_REQUEST_TIMEOUT_SECONDS", 120*time.Second),
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
		workdir:    "e2b-go-sdk-e2e-advanced-" + testID,
	}

	if err := run.createSandbox(nil); err != nil {
		t.Fatalf("create sandbox fixture: %v", err)
	}
	t.Cleanup(run.killSandbox)

	for _, tc := range []struct {
		name     string
		fn       func() error
		optional bool
	}{
		{name: "network_and_metrics", fn: run.verifyNetworkAndMetrics},
		{name: "signed_file_urls", fn: run.verifySignedFileURLs},
		{name: "error_mapping", fn: run.verifyErrorMapping},
		{name: "pause_and_reconnect", fn: run.verifyPauseAndReconnect, optional: true},
		{name: "snapshot", fn: run.verifySnapshot, optional: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.optional {
				assertOptionalE2EFeature(t, run, tc.fn)
				return
			}
			assertNoNewRuntimeIssues(t, run, tc.fn)
		})
	}
}
