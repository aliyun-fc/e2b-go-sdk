package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"
)

// enabledAny reports whether any of the given environment variable keys is set
// to a truthy value.
func enabledAny(keys ...string) bool {
	for _, key := range keys {
		if enabled(key) {
			return true
		}
	}
	return false
}

// envDurationAny returns the duration from the first set key among keys, falling
// back to fallback when none is set.
func envDurationAny(keys []string, fallback time.Duration) time.Duration {
	for _, key := range keys {
		if env(key, "") != "" {
			return envDuration(key, fallback)
		}
	}
	return fallback
}

// newSandboxE2EClient builds an e2b.Client for the E2E suite, taking the API URL
// and domain from the environment (defaulting to the Singapore region).
func newSandboxE2EClient(apiKey, integration string, requestTimeout time.Duration) (*e2b.Client, error) {
	return e2b.NewClient(
		e2b.WithAPIKey(apiKey),
		e2b.WithAPIURL(env("E2B_API_URL", "https://api.ap-southeast-1.e2b.fc.aliyuncs.com")),
		e2b.WithDomain(env("E2B_DOMAIN", "ap-southeast-1.e2b.fc.aliyuncs.com")),
		e2b.WithIntegration(integration),
		e2b.WithRequestTimeout(requestTimeout),
	)
}

// requireSandbox fails the test when the shared sandbox has not been created yet,
// signalling that the create subtest must run before the current one.
func requireSandbox(t *testing.T, sandbox *e2b.Sandbox) {
	t.Helper()
	if sandbox == nil {
		t.Fatal("sandbox is nil; create subtest must run first")
	}
}

// assertSandboxInfo fails the test unless info reports the expected sandbox ID,
// the running state, and every expected metadata entry (empty fields are treated
// as "not returned" and skipped).
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

// waitForSandboxInList polls ListSandboxes (filtered by the test's metadata ID)
// until sandboxID appears or the timeout elapses.
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

// waitForSandboxTimeoutAtLeast polls GetInfo until the sandbox's remaining
// lifetime is at least minRemaining, returning the most recent info seen.
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

// waitForSandboxStopped polls until the sandbox reaches the killed state or has
// disappeared entirely (treated as stopped).
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

// isSandboxGone reports whether err indicates the sandbox no longer exists
// (a not-found or sandbox-not-found error).
func isSandboxGone(err error) bool {
	var notFound *e2b.NotFoundError
	var sandboxNotFound *e2b.SandboxNotFoundError
	return errors.As(err, &notFound) || errors.As(err, &sandboxNotFound)
}

// pollUntil calls fn every interval until it returns true, the timeout fires, or
// ctx is cancelled. Timeout and cancellation remain detectable with errors.Is;
// the most recent callback error is included only as diagnostic context.
func pollUntil(ctx context.Context, timeout, interval time.Duration, fn func() (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := pollCtx.Err(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		if err := pollCtx.Err(); err != nil {
			return pollUntilTerminalError(err, lastErr)
		}
		ok, err := fn()
		if err != nil {
			lastErr = err
		} else if ok {
			return nil
		} else {
			lastErr = nil
		}

		select {
		case <-pollCtx.Done():
			return pollUntilTerminalError(pollCtx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func pollUntilTerminalError(cause, lastErr error) error {
	if lastErr == nil {
		return cause
	}
	return fmt.Errorf("%w (last poll error: %v)", cause, lastErr)
}

func TestPollUntilDoesNotPollCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	err := pollUntil(ctx, time.Second, time.Hour, func() (bool, error) {
		calls++
		return false, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pollUntil error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("poll calls = %d, want 0 for an already canceled context", calls)
	}
}

func TestPollUntilDeadlinePreservesCauseAndLastError(t *testing.T) {
	transientErr := errors.New("temporary lookup failure")
	err := pollUntil(context.Background(), 10*time.Millisecond, time.Hour, func() (bool, error) {
		return false, transientErr
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pollUntil error = %v, want context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), transientErr.Error()) {
		t.Fatalf("pollUntil error = %q, want last poll error %q", err, transientErr)
	}
}

func TestPollUntilCancellationPreservesCauseAndLastError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transientErr := errors.New("temporary lookup failure")
	err := pollUntil(ctx, time.Second, time.Hour, func() (bool, error) {
		cancel()
		return false, transientErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pollUntil error = %v, want context.Canceled", err)
	}
	if !strings.Contains(err.Error(), transientErr.Error()) {
		t.Fatalf("pollUntil error = %q, want last poll error %q", err, transientErr)
	}
}

// assertNoNewRuntimeIssues runs fn and fails the test if it errors or if it
// appends any new entries to the run's recorded SDK issues.
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

// assertOptionalE2EFeature runs fn for a feature that may be unavailable on the
// current control plane: it skips the test when the error looks like an
// unsupported feature, fails on any other error, and fails on new runtime issues.
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

// formatRuntimeIssues renders the collected SDK issues as a single bulleted
// message for use in test failure output.
func formatRuntimeIssues(issues []string) string {
	var builder strings.Builder
	builder.WriteString("runtime e2e completed with SDK issues:")
	for _, issue := range issues {
		builder.WriteString(fmt.Sprintf("\n- %s", issue))
	}
	return builder.String()
}

// isOptionalFeatureUnavailable reports whether err looks like the control plane
// not supporting a feature, based on the API status code (404/500/501/503) or
// well-known "not implemented"/"not supported" phrasings in the message.
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

// isVolumeServiceUnavailable reports whether err indicates the volume service is
// not available, checking VolumeError messages and otherwise deferring to
// isOptionalFeatureUnavailable.
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
