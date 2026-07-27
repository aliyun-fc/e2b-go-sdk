package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	e2b "github.com/e2b-dev/e2b-go-sdk"
)

func enabledAny(keys ...string) bool {
	for _, key := range keys {
		if enabled(key) {
			return true
		}
	}
	return false
}

func envDurationAny(keys []string, fallback time.Duration) time.Duration {
	for _, key := range keys {
		if env(key, "") != "" {
			return envDuration(key, fallback)
		}
	}
	return fallback
}

func newSandboxE2EClient(apiKey, integration string, requestTimeout time.Duration) (*e2b.Client, error) {
	return e2b.NewClient(
		e2b.WithAPIKey(apiKey),
		e2b.WithAPIURL(env("E2B_API_URL", "https://api.ap-southeast-1.e2b.fc.aliyuncs.com")),
		e2b.WithDomain(env("E2B_DOMAIN", "ap-southeast-1.e2b.fc.aliyuncs.com")),
		e2b.WithIntegration(integration),
		e2b.WithRequestTimeout(requestTimeout),
	)
}

func requireSandbox(t *testing.T, sandbox *e2b.Sandbox) {
	t.Helper()
	if sandbox == nil {
		t.Fatal("sandbox is nil; create subtest must run first")
	}
}

func assertSandboxInfo(t *testing.T, info e2b.SandboxInfo, sandboxID string, metadata map[string]string) {
	t.Helper()
	if info.SandboxID != "" && info.SandboxID != sandboxID {
		t.Fatalf("GetInfo sandboxID = %q, want %q", info.SandboxID, sandboxID)
	}
	if info.State != "" && info.State != e2b.SandboxStateRunning {
		t.Fatalf("GetInfo state = %q, want %q", info.State, e2b.SandboxStateRunning)
	}
	for key, want := range metadata {
		if got := info.Metadata[key]; got != want {
			t.Fatalf("GetInfo metadata[%q] = %q, want %q", key, got, want)
		}
	}
}

func waitForSandboxInList(ctx context.Context, client *e2b.Client, sandboxID, testID string) error {
	return pollUntil(ctx, 45*time.Second, 2*time.Second, func() (bool, error) {
		page, err := client.ListSandboxes(ctx, &e2b.SandboxQuery{
			Metadata: map[string]string{"go_sdk_e2e_id": testID},
		}, 20, "")
		if err != nil {
			return false, err
		}
		return containsSandbox(page.Items, sandboxID), nil
	})
}

func waitForSandboxTimeoutAtLeast(ctx context.Context, sandbox *e2b.Sandbox, minRemaining time.Duration) (e2b.SandboxInfo, error) {
	var last e2b.SandboxInfo
	err := pollUntil(ctx, 45*time.Second, 2*time.Second, func() (bool, error) {
		info, err := sandbox.GetInfo(ctx)
		if err != nil {
			return false, err
		}
		last = info
		return time.Until(info.EndAt) >= minRemaining, nil
	})
	return last, err
}

func waitForSandboxStopped(ctx context.Context, client *e2b.Client, sandboxID string) error {
	return pollUntil(ctx, 90*time.Second, 3*time.Second, func() (bool, error) {
		info, err := client.GetSandboxInfo(ctx, sandboxID)
		if err != nil {
			if isSandboxGone(err) {
				return true, nil
			}
			return false, err
		}
		return info.State == e2b.SandboxStateKilled, nil
	})
}

func isSandboxGone(err error) bool {
	var notFound *e2b.NotFoundError
	var sandboxNotFound *e2b.SandboxNotFoundError
	return errors.As(err, &notFound) || errors.As(err, &sandboxNotFound)
}

func pollUntil(ctx context.Context, timeout, interval time.Duration, fn func() (bool, error)) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		ok, err := fn()
		if err != nil {
			lastErr = err
		} else if ok {
			return nil
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-deadline.C:
			if lastErr != nil {
				return lastErr
			}
			return context.DeadlineExceeded
		case <-ticker.C:
		}
	}
}

func assertNoNewRuntimeIssues(t *testing.T, run *e2eRun, fn func() error) {
	t.Helper()
	issueStart := len(run.issues)
	if err := fn(); err != nil {
		t.Fatal(err)
	}
	if len(run.issues) > issueStart {
		t.Fatal(formatRuntimeIssues(run.issues[issueStart:]))
	}
}

func assertOptionalE2EFeature(t *testing.T, run *e2eRun, fn func() error) {
	t.Helper()
	issueStart := len(run.issues)
	if err := fn(); err != nil {
		if isOptionalFeatureUnavailable(err) {
			t.Skipf("current control plane does not support this optional feature reliably: %v", err)
		}
		t.Fatal(err)
	}
	if len(run.issues) > issueStart {
		t.Fatal(formatRuntimeIssues(run.issues[issueStart:]))
	}
}

func formatRuntimeIssues(issues []string) string {
	var builder strings.Builder
	builder.WriteString("runtime e2e completed with SDK issues:")
	for _, issue := range issues {
		builder.WriteString(fmt.Sprintf("\n- %s", issue))
	}
	return builder.String()
}

func isOptionalFeatureUnavailable(err error) bool {
	var apiErr *e2b.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 404, 500, 501, 503:
			return true
		}
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "not implemented") ||
		strings.Contains(text, "not supported") ||
		strings.Contains(text, "404 page not found") ||
		strings.Contains(text, "internalservererror") ||
		strings.Contains(text, "internal error has occurred")
}

func isVolumeServiceUnavailable(err error) bool {
	var volumeErr *e2b.VolumeError
	if errors.As(err, &volumeErr) {
		text := strings.ToLower(volumeErr.Message)
		return strings.Contains(text, "404") ||
			strings.Contains(text, "page not found") ||
			strings.Contains(text, "not implemented") ||
			strings.Contains(text, "not supported")
	}
	return isOptionalFeatureUnavailable(err)
}
