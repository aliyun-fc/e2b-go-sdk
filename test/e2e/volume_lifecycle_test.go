package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	e2b "github.com/e2b-dev/e2b-go-sdk"
)

const volumeE2EFlag = "E2B_VOLUME_E2E"

// TestVolumeLifecycleContentAndMount exercises the volume lifecycle, content
// operations, and mounting into a sandbox against a real control plane. It is
// skipped unless E2B_VOLUME_E2E is set.
func TestVolumeLifecycleContentAndMount(t *testing.T) {
	if !enabled(volumeE2EFlag) {
		t.Skip("set E2B_VOLUME_E2E=1 to run the real volume lifecycle and mount e2e test")
	}

	apiKey := env("E2B_API_KEY", env("E2B_OFFICIAL_API_KEY", ""))
	if apiKey == "" {
		t.Fatal("set E2B_API_KEY or E2B_OFFICIAL_API_KEY")
	}

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("E2B_VOLUME_E2E_TIMEOUT_SECONDS", 25*time.Minute))
	defer cancel()

	client, err := newSandboxE2EClient(
		apiKey,
		"e2b-go-sdk-volume-e2e/1.0",
		envDuration("E2B_VOLUME_E2E_REQUEST_TIMEOUT_SECONDS", 120*time.Second),
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
		workdir:    "e2b-go-sdk-e2e-volume-" + testID,
	}

	volume, err := run.createVolumeFixture()
	if err != nil {
		if isVolumeServiceUnavailable(err) {
			t.Skipf("volume API is not available in this control plane: %v", err)
		}
		t.Fatalf("create volume fixture: %v", err)
	}
	t.Cleanup(func() {
		run.destroyVolume(volume)
	})

	if err := run.createSandbox(volume); err != nil {
		t.Fatalf("create sandbox with volume mount: %v", err)
	}
	t.Cleanup(run.killSandbox)

	assertNoNewRuntimeIssues(t, run, func() error {
		return run.verifyVolume(volume)
	})
}
