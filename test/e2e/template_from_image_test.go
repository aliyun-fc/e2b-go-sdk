package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"
)

const (
	templateE2EFlag         = "E2B_TEMPLATE_E2E"
	defaultTemplateE2EImage = "fc-e2b-registry.ap-southeast-1.cr.aliyuncs.com/runtime/base:v0.0.47"
)

// TestTemplateFromImageBuildQueryDeleteAndSpawn verifies that a failed rebuild
// cannot delete an existing template or stop a sandbox created from it.
func TestTemplateFromImageBuildQueryDeleteAndSpawn(t *testing.T) {
	if !enabled(templateE2EFlag) {
		t.Skip("set E2B_TEMPLATE_E2E=1 to run the real template from-image e2e test")
	}

	apiKey := env("E2B_API_KEY", env("E2B_OFFICIAL_API_KEY", ""))
	if apiKey == "" {
		t.Fatal("set E2B_API_KEY or E2B_OFFICIAL_API_KEY")
	}

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("E2B_TEMPLATE_E2E_TIMEOUT_SECONDS", 45*time.Minute))
	defer cancel()

	client, err := newSandboxE2EClient(
		apiKey,
		"e2b-go-sdk-template-e2e/1.0",
		envDuration("E2B_TEMPLATE_E2E_REQUEST_TIMEOUT_SECONDS", 180*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	testID := newTemplateE2ETestID(t)
	namePrefix := strings.TrimRight(env("E2B_TEMPLATE_E2E_NAME_PREFIX", env("E2B_TEMPLATE_E2E_NAME", "go-sdk-e2e-template")), "-")
	if namePrefix == "" {
		t.Fatal("E2B_TEMPLATE_E2E_NAME_PREFIX cannot be empty")
	}
	name := namePrefix + "-" + testID
	image := env("E2B_TEMPLATE_E2E_IMAGE", defaultTemplateE2EImage)
	tag := env("E2B_TEMPLATE_E2E_TAG", "from-image-e2e")

	var templateID string
	var buildID string
	var sandbox *e2b.Sandbox
	sandboxStopped := true
	templateDeleted := false
	var failedRebuildName string
	var failedRebuildBaselineTemplateIDs map[string]struct{}
	failedRebuildUnexpectedTemplateHandled := false
	failedRebuildObservationCompleted := false
	t.Cleanup(func() {
		if sandbox != nil && !sandboxStopped {
			if enabled("E2B_E2E_KEEP_SANDBOX") {
				t.Logf("keeping template sandbox by request: sandbox_id=%s template_id=%s", sandbox.SandboxID(), templateID)
				return
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			killed, err := stopTemplateE2ESandbox(cleanupCtx, client, sandbox)
			cleanupCancel()
			if err != nil {
				t.Errorf("sandbox cleanup failed; preserving template: sandbox_id=%s template_id=%s killed=%v err=%v", sandbox.SandboxID(), templateID, killed, err)
				return
			}
			sandboxStopped = true
			t.Logf("template sandbox cleanup: sandbox_id=%s killed=%v", sandbox.SandboxID(), killed)
		}

		if templateID == "" || templateDeleted || enabled("E2B_E2E_KEEP_TEMPLATE") {
			if templateID != "" && !templateDeleted && enabled("E2B_E2E_KEEP_TEMPLATE") {
				t.Logf("keeping template by request: template_id=%s build_id=%s name=%s", templateID, buildID, name)
			}
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := deleteOwnedTemplateE2E(cleanupCtx, client, templateID, buildID, name); err != nil {
			t.Errorf("template cleanup failed; preserving template: template_id=%s build_id=%s name=%s err=%v", templateID, buildID, name, err)
			return
		}
		t.Logf("template cleanup: template_id=%s deleted=true", templateID)
	})
	t.Cleanup(func() {
		if failedRebuildName == "" || failedRebuildBaselineTemplateIDs == nil || failedRebuildUnexpectedTemplateHandled {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cleanupCancel()
		if !failedRebuildObservationCompleted {
			candidateID, err := findAndDeleteUnexpectedTemplateE2E(cleanupCtx, client, failedRebuildName, failedRebuildBaselineTemplateIDs)
			if err != nil {
				t.Errorf("final unexpected-template observation or cleanup failed: template_id=%s name=%s err=%v", candidateID, failedRebuildName, err)
				return
			}
			if candidateID != "" {
				failedRebuildUnexpectedTemplateHandled = true
				t.Logf("final unexpected-template cleanup: template_id=%s name=%s deleted=true", candidateID, failedRebuildName)
			}
			return
		}
		candidateID, found, err := findTemplateE2ECleanupTargetExcluding(cleanupCtx, client, failedRebuildName, failedRebuildBaselineTemplateIDs)
		if err != nil {
			t.Errorf("final unexpected-template lookup failed: name=%s err=%v", failedRebuildName, err)
			return
		}
		if !found {
			return
		}
		if err := deleteOwnedTemplateE2E(cleanupCtx, client, candidateID, "", failedRebuildName); err != nil {
			t.Errorf("final unexpected-template cleanup failed: template_id=%s name=%s err=%v", candidateID, failedRebuildName, err)
			return
		}
		failedRebuildUnexpectedTemplateHandled = true
		t.Logf("final unexpected-template cleanup: template_id=%s name=%s deleted=true", candidateID, failedRebuildName)
	})
	t.Logf("template e2e resource name=%s image=%s", name, image)
	exists, err := client.TemplateExists(ctx, name)
	if err != nil {
		t.Fatalf("preflight TemplateExists(%q): %v", name, err)
	}
	if exists {
		t.Fatalf("refusing to use non-unique template name %q", name)
	}

	build, err := client.BuildTemplateInBackground(
		ctx,
		e2b.NewTemplate().FromImage(image),
		name,
		e2b.WithTemplateCPUCount(2),
		e2b.WithTemplateMemoryMB(2048),
		e2b.WithTemplateTags(tag),
	)
	if err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		recoveredTemplateID, recoveryErr := waitForTemplateE2ECleanupTarget(cleanupCtx, client, name)
		cleanupCancel()
		if recoveryErr != nil {
			t.Fatalf("BuildTemplateInBackground from image %q: %v; no uniquely owned cleanup target found for name %q: %v", image, err, name, recoveryErr)
		}
		templateID = recoveredTemplateID
		t.Fatalf("BuildTemplateInBackground from image %q: %v; recovered template_id=%s for cleanup", image, err, templateID)
	}
	templateID = build.TemplateID
	buildID = build.BuildID
	if build.TemplateID == "" || build.BuildID == "" {
		t.Fatalf("BuildTemplate returned incomplete build info: %+v", build)
	}
	t.Logf("template_id=%s build_id=%s name=%s image=%s", build.TemplateID, build.BuildID, name, image)
	if err := waitForTemplateE2EBuildReady(ctx, t, client, build.TemplateID, build.BuildID, 5*time.Second); err != nil {
		t.Fatalf("wait for template build: %v", err)
	}

	t.Run("query", func(t *testing.T) {
		status, err := client.GetBuildStatus(ctx, build.TemplateID, build.BuildID, 0)
		if err != nil {
			t.Fatalf("GetBuildStatus: %v", err)
		}
		if status.Status != e2b.TemplateBuildStatusReady {
			t.Fatalf("build status = %q, want %q", status.Status, e2b.TemplateBuildStatusReady)
		}

		if err := pollUntil(ctx, 90*time.Second, 2*time.Second, func() (bool, error) {
			return client.TemplateExists(ctx, name)
		}); err != nil {
			t.Fatalf("TemplateExists(%q) did not become true: %v", name, err)
		}

		if err := pollUntil(ctx, 90*time.Second, 2*time.Second, func() (bool, error) {
			templates, err := client.ListTemplates(ctx, "")
			if err != nil {
				return false, err
			}
			return containsTemplate(templates, build.TemplateID), nil
		}); err != nil {
			t.Fatalf("template %s did not appear in ListTemplates: %v", build.TemplateID, err)
		}

		if err := waitForTemplateE2EOwnership(ctx, client, build.TemplateID, build.BuildID, name); err != nil {
			t.Fatalf("GetTemplate did not confirm ownership: %v", err)
		}

		tags, err := client.GetTemplateTags(ctx, build.TemplateID)
		if err != nil {
			t.Fatalf("GetTemplateTags: %v", err)
		}
		if len(tags) == 0 {
			t.Fatalf("GetTemplateTags returned no tags")
		}
		if !containsTemplateTag(tags, tag) {
			t.Logf("requested template tag %q was normalized or ignored by this control plane; actual tags: %+v", tag, tags)
		}
	})

	t.Run("spawn_sandbox", func(t *testing.T) {
		var err error
		sandbox, err = client.CreateSandbox(
			ctx,
			e2b.WithTemplate(build.TemplateID),
			e2b.WithTimeout(600),
			e2b.WithMetadata(map[string]string{
				"go_sdk_e2e":             "true",
				"go_sdk_e2e_case":        "template_from_image",
				"go_sdk_e2e_template_id": build.TemplateID,
			}),
		)
		if err != nil {
			t.Fatalf("CreateSandbox from built template: %v", err)
		}
		sandboxStopped = false
		t.Logf("template sandbox created: sandbox_id=%s template_id=%s", sandbox.SandboxID(), build.TemplateID)

		result, err := sandbox.Commands.Run(
			ctx,
			"printf 'template-sandbox-ok\\n'; uname -s",
			e2b.WithCommandTimeout(60*time.Second),
			e2b.WithCommandRequestTimeout(120*time.Second),
		)
		if err != nil {
			t.Fatalf("run command in template sandbox: %v", err)
		}
		if !strings.Contains(result.Stdout, "template-sandbox-ok") {
			t.Fatalf("template sandbox stdout = %q", result.Stdout)
		}
	})

	t.Run("failed_rebuild_preserves_template_and_sandbox", func(t *testing.T) {
		if sandbox == nil {
			t.Fatal("sandbox was not created")
		}
		templatesBeforeRebuild, err := client.ListTemplates(ctx, "")
		if err != nil {
			t.Fatalf("ListTemplates before failed rebuild: %v", err)
		}
		baselineTemplateIDs := templateE2ETemplateIDs(templatesBeforeRebuild)
		if _, ok := baselineTemplateIDs[build.TemplateID]; !ok {
			t.Fatalf("original template %s missing from failed rebuild baseline", build.TemplateID)
		}
		failedRebuildBaselineTemplateIDs = baselineTemplateIDs

		attemptFailedRebuild := func(reference string) error {
			failedRebuildName = reference
			_, attemptErr := client.BuildTemplateInBackground(
				ctx,
				e2b.NewTemplate().FromImage(image).Copy("/absolute-copy-source-is-intentionally-invalid", "/tmp/e2b-invalid-copy"),
				reference,
				e2b.WithTemplateCPUCount(2),
				e2b.WithTemplateMemoryMB(2048),
				e2b.WithTemplateTags(tag),
			)
			return attemptErr
		}
		failedRebuildReference, buildErr, err := runFailedTemplateRebuildE2E(
			build.TemplateID,
			name,
			attemptFailedRebuild,
			func() error {
				return verifyTemplateE2EOwnership(ctx, client, build.TemplateID, build.BuildID, name)
			},
		)
		if err != nil {
			t.Fatalf("failed rebuild did not reach the intended existing-template failure path: %v", err)
		}
		if failedRebuildReference != build.TemplateID {
			t.Logf("control plane rejected TemplateID %q as a build name before template resolution; retried the existing-template failure path with unique alias %q", build.TemplateID, failedRebuildReference)
		}

		unexpectedTemplateID, err := findAndDeleteUnexpectedTemplateE2E(ctx, client, failedRebuildReference, baselineTemplateIDs)
		failedRebuildObservationCompleted = err == nil || unexpectedTemplateID != ""
		if unexpectedTemplateID != "" {
			if err == nil {
				failedRebuildUnexpectedTemplateHandled = true
			} else {
				t.Fatalf("failed rebuild created unexpected template %s for name %q; cleanup failed: %v", unexpectedTemplateID, failedRebuildReference, err)
			}
			t.Fatalf("failed rebuild created unexpected template %s for name %q; template was cleaned up", unexpectedTemplateID, failedRebuildReference)
		}
		if err != nil {
			t.Fatalf("check failed rebuild for an unexpected template: %v", err)
		}
		if !strings.Contains(buildErr.Error(), "absolute paths are not allowed") {
			t.Fatalf("failed rebuild did not reach the intended local COPY validation: %v", buildErr)
		}
		t.Logf("failed rebuild with existing template reference %q returned expected BuildError: %v", failedRebuildReference, buildErr)

		if err := verifyTemplateE2EOwnership(ctx, client, build.TemplateID, build.BuildID, name); err != nil {
			t.Fatalf("original template was not preserved after failed rebuild with TemplateID %q: %v", build.TemplateID, err)
		}
		status, err := client.GetBuildStatus(ctx, build.TemplateID, build.BuildID, 0)
		if err != nil {
			t.Fatalf("GetBuildStatus for original build after failed rebuild: %v", err)
		}
		if status.Status != e2b.TemplateBuildStatusReady {
			t.Fatalf("original build status after failed rebuild = %q, want %q", status.Status, e2b.TemplateBuildStatusReady)
		}
		running, err := sandbox.IsRunning(ctx)
		if err != nil {
			t.Fatalf("IsRunning after failed rebuild: %v", err)
		}
		if !running {
			t.Fatal("sandbox created from the original template stopped after failed rebuild")
		}

		result, err := sandbox.Commands.Run(
			ctx,
			"printf 'template-survived-failed-rebuild\\n'",
			e2b.WithCommandTimeout(60*time.Second),
			e2b.WithCommandRequestTimeout(120*time.Second),
		)
		if err != nil {
			t.Fatalf("run command after failed rebuild: %v", err)
		}
		if !strings.Contains(result.Stdout, "template-survived-failed-rebuild") {
			t.Fatalf("post-rebuild sandbox stdout = %q", result.Stdout)
		}
	})

	t.Run("stop_sandbox", func(t *testing.T) {
		if sandbox == nil || sandboxStopped {
			return
		}
		if enabled("E2B_E2E_KEEP_SANDBOX") {
			t.Logf("keeping template sandbox by request: sandbox_id=%s", sandbox.SandboxID())
			return
		}
		killed, err := stopTemplateE2ESandbox(ctx, client, sandbox)
		if err != nil {
			t.Fatalf("stop template sandbox %s: killed=%v err=%v", sandbox.SandboxID(), killed, err)
		}
		sandboxStopped = true
		t.Logf("template sandbox stopped: sandbox_id=%s killed=%v", sandbox.SandboxID(), killed)
	})

	t.Run("delete", func(t *testing.T) {
		if enabled("E2B_E2E_KEEP_SANDBOX") || enabled("E2B_E2E_KEEP_TEMPLATE") {
			t.Logf("skipping explicit template deletion by request: template_id=%s build_id=%s name=%s", build.TemplateID, build.BuildID, name)
			return
		}
		if !sandboxStopped {
			t.Fatalf("refusing to delete template %s while sandbox %s is not confirmed stopped", build.TemplateID, sandbox.SandboxID())
		}
		if err := deleteOwnedTemplateE2E(ctx, client, build.TemplateID, build.BuildID, name); err != nil {
			t.Fatalf("DeleteTemplate: %v", err)
		}
		templateDeleted = true
		if err := pollUntil(ctx, 90*time.Second, 2*time.Second, func() (bool, error) {
			exists, err := client.TemplateExists(ctx, name)
			if err != nil {
				return false, err
			}
			return !exists, nil
		}); err != nil {
			t.Fatalf("template alias %q did not disappear after deleting %s: %v", name, build.TemplateID, err)
		}
	})
}

func newTemplateE2ETestID(t *testing.T) string {
	t.Helper()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatalf("generate template e2e id: %v", err)
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(random[:])
}

func waitForTemplateE2EOwnership(ctx context.Context, client *e2b.Client, templateID, buildID, name string) error {
	return pollUntil(ctx, 90*time.Second, 2*time.Second, func() (bool, error) {
		if err := verifyTemplateE2EOwnership(ctx, client, templateID, buildID, name); err != nil {
			return false, err
		}
		return true, nil
	})
}

func deleteOwnedTemplateE2E(ctx context.Context, client *e2b.Client, templateID, buildID, name string) error {
	if err := waitForTemplateE2EOwnership(ctx, client, templateID, buildID, name); err != nil {
		return fmt.Errorf("confirm ownership of template %s: %w", templateID, err)
	}
	deleted, err := client.DeleteTemplate(ctx, templateID)
	if err != nil {
		return fmt.Errorf("DeleteTemplate(%s): %w", templateID, err)
	}
	if !deleted {
		return fmt.Errorf("DeleteTemplate(%s) returned false", templateID)
	}
	return nil
}

func waitForTemplateE2ECleanupTarget(ctx context.Context, client *e2b.Client, name string) (string, error) {
	var templateID string
	err := pollUntil(ctx, 90*time.Second, 2*time.Second, func() (bool, error) {
		candidateID, found, err := findTemplateE2ECleanupTarget(ctx, client, name)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		templateID = candidateID
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("recover template created for %q: %w", name, err)
	}
	return templateID, nil
}

func findTemplateE2ECleanupTarget(ctx context.Context, client *e2b.Client, name string) (string, bool, error) {
	return findTemplateE2ECleanupTargetExcluding(ctx, client, name, nil)
}

func findTemplateE2ECleanupTargetExcluding(ctx context.Context, client *e2b.Client, name string, excludedTemplateIDs map[string]struct{}) (string, bool, error) {
	templates, err := client.ListTemplates(ctx, "")
	if err != nil {
		return "", false, fmt.Errorf("ListTemplates while recovering %q: %w", name, err)
	}

	candidates := make([]string, 0, 1)
	seen := make(map[string]struct{})
	for _, template := range templates {
		if !containsString(template.Aliases, name) && !containsString(template.Names, name) {
			continue
		}
		if template.TemplateID == "" {
			return "", false, fmt.Errorf("template matching unique e2e name %q has an empty template ID", name)
		}
		if _, excluded := excludedTemplateIDs[template.TemplateID]; excluded {
			continue
		}
		if _, ok := seen[template.TemplateID]; ok {
			continue
		}
		seen[template.TemplateID] = struct{}{}
		candidates = append(candidates, template.TemplateID)
	}

	switch len(candidates) {
	case 0:
		return "", false, nil
	case 1:
		if err := verifyTemplateE2EOwnership(ctx, client, candidates[0], "", name); err != nil {
			return "", false, err
		}
		return candidates[0], true, nil
	default:
		return "", false, fmt.Errorf("multiple templates match unique e2e name %q: %v", name, candidates)
	}
}

func waitForUnexpectedTemplateE2ECleanupTarget(ctx context.Context, client *e2b.Client, name string, baselineTemplateIDs map[string]struct{}) (string, error) {
	const observationTimeout = 90 * time.Second
	const pollInterval = 2 * time.Second

	var templateID string
	lastLookupSucceeded := false
	err := pollUntil(ctx, observationTimeout, pollInterval, func() (bool, error) {
		candidateID, found, err := findTemplateE2ECleanupTargetExcluding(ctx, client, name, baselineTemplateIDs)
		lastLookupSucceeded = err == nil
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		templateID = candidateID
		return true, nil
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && lastLookupSucceeded {
			return "", nil
		}
		return "", fmt.Errorf("observe templates created for failed rebuild name %q: %w", name, err)
	}
	return templateID, nil
}

func findAndDeleteUnexpectedTemplateE2E(ctx context.Context, client *e2b.Client, name string, baselineTemplateIDs map[string]struct{}) (string, error) {
	templateID, err := waitForUnexpectedTemplateE2ECleanupTarget(ctx, client, name, baselineTemplateIDs)
	if err != nil || templateID == "" {
		return templateID, err
	}
	if err := deleteOwnedTemplateE2E(ctx, client, templateID, "", name); err != nil {
		return templateID, fmt.Errorf("delete unexpected template %s for name %q: %w", templateID, name, err)
	}
	return templateID, nil
}

func templateE2ETemplateIDs(templates []e2b.TemplateInfo) map[string]struct{} {
	ids := make(map[string]struct{}, len(templates))
	for _, template := range templates {
		if template.TemplateID != "" {
			ids[template.TemplateID] = struct{}{}
		}
	}
	return ids
}

func runFailedTemplateRebuildE2E(templateID, alias string, attempt func(string) error, confirmAliasOwner func() error) (string, *e2b.BuildError, error) {
	err := attempt(templateID)
	if err == nil {
		return "", nil, fmt.Errorf("failed rebuild with TemplateID %q unexpectedly succeeded", templateID)
	}

	var buildErr *e2b.BuildError
	if errors.As(err, &buildErr) {
		return templateID, buildErr, nil
	}

	var apiErr *e2b.APIError
	if !errors.As(err, &apiErr) {
		return "", nil, fmt.Errorf("failed rebuild with TemplateID error = %T %v, want local COPY BuildError or invalid-name APIError", err, err)
	}
	invalidNumericTemplateID := templateID != "" && templateID[0] >= '0' && templateID[0] <= '9'
	invalidNameForTemplateID := strings.Contains(strings.ToLower(apiErr.Message), "invalid template name") && strings.Contains(apiErr.Message, templateID)
	if apiErr.StatusCode != http.StatusBadRequest || !invalidNumericTemplateID || !invalidNameForTemplateID {
		return "", nil, fmt.Errorf("failed rebuild with TemplateID error = %T %v, want local COPY BuildError or invalid-name APIError", err, err)
	}
	if err := confirmAliasOwner(); err != nil {
		return "", nil, fmt.Errorf("confirm existing template before alias fallback: %w", err)
	}

	err = attempt(alias)
	if err == nil {
		return "", nil, fmt.Errorf("failed rebuild with existing template alias %q unexpectedly succeeded", alias)
	}
	buildErr = nil
	if !errors.As(err, &buildErr) {
		return "", nil, fmt.Errorf("failed rebuild with existing template alias error = %T %v, want *e2b.BuildError", err, err)
	}
	return alias, buildErr, nil
}

func waitForTemplateE2EBuildReady(ctx context.Context, t *testing.T, client *e2b.Client, templateID, buildID string, pollPeriod time.Duration) error {
	t.Helper()
	logsOffset := 0
	for {
		status, err := client.GetBuildStatus(ctx, templateID, buildID, logsOffset)
		if err != nil {
			return err
		}
		for _, entry := range status.LogEntries {
			t.Logf("template build log: level=%s message=%s", entry.Level, entry.Message)
		}
		logsOffset += len(status.LogEntries)
		switch status.Status {
		case e2b.TemplateBuildStatusReady:
			return nil
		case e2b.TemplateBuildStatusError:
			if status.Reason != nil && status.Reason.Message != "" {
				return &e2b.BuildError{Message: status.Reason.Message}
			}
			return &e2b.BuildError{Message: "build failed"}
		}

		timer := time.NewTimer(pollPeriod)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func verifyTemplateE2EOwnership(ctx context.Context, client *e2b.Client, templateID, buildID, name string) error {
	template, err := client.GetTemplate(ctx, templateID, 20, "")
	if err != nil {
		return fmt.Errorf("GetTemplate(%s): %w", templateID, err)
	}
	if template.TemplateID != templateID {
		return fmt.Errorf("GetTemplate templateID = %q, want %q", template.TemplateID, templateID)
	}
	if !containsString(template.Aliases, name) && !containsString(template.Names, name) {
		return fmt.Errorf("template %s does not contain unique e2e name %q in aliases=%v or names=%v", templateID, name, template.Aliases, template.Names)
	}
	if buildID != "" && !containsTemplateBuild(template.Builds, buildID) {
		return fmt.Errorf("template %s does not contain e2e build %s", templateID, buildID)
	}
	return nil
}

func stopTemplateE2ESandbox(ctx context.Context, client *e2b.Client, sandbox *e2b.Sandbox) (bool, error) {
	killed, err := sandbox.Kill(ctx)
	if err != nil {
		return killed, err
	}
	if err := waitForSandboxStopped(ctx, client, sandbox.SandboxID()); err != nil {
		return killed, fmt.Errorf("wait for sandbox %s to stop: %w", sandbox.SandboxID(), err)
	}
	return killed, nil
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func containsTemplateBuild(items []e2b.TemplateBuild, buildID string) bool {
	for _, item := range items {
		if item.BuildID == buildID {
			return true
		}
	}
	return false
}

func TestVerifyTemplateE2EOwnership(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		templateID string
		buildID    string
		aliases    []string
		names      []string
		builds     []map[string]string
		wantErr    string
	}{
		{
			name:       "matching alias and build",
			templateID: "template-id",
			buildID:    "build-id",
			aliases:    []string{"unique-name"},
			builds:     []map[string]string{{"buildID": "build-id"}},
		},
		{
			name:       "matching name and build",
			templateID: "template-id",
			buildID:    "build-id",
			names:      []string{"unique-name"},
			builds:     []map[string]string{{"buildID": "build-id"}},
		},
		{
			name:       "matching name without build id",
			templateID: "template-id",
			names:      []string{"unique-name"},
		},
		{
			name:       "wrong template id",
			templateID: "someone-elses-template",
			buildID:    "build-id",
			aliases:    []string{"unique-name"},
			builds:     []map[string]string{{"buildID": "build-id"}},
			wantErr:    "templateID",
		},
		{
			name:       "missing unique name",
			templateID: "template-id",
			buildID:    "build-id",
			aliases:    []string{"another-name"},
			builds:     []map[string]string{{"buildID": "build-id"}},
			wantErr:    "does not contain unique e2e name",
		},
		{
			name:       "missing build",
			templateID: "template-id",
			buildID:    "build-id",
			aliases:    []string{"unique-name"},
			builds:     []map[string]string{{"buildID": "another-build"}},
			wantErr:    "does not contain e2e build",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(map[string]any{
				"templateID": tt.templateID,
				"aliases":    tt.aliases,
				"names":      tt.names,
				"builds":     tt.builds,
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			client, err := e2b.NewClient(
				e2b.WithAPIKey("e2b_0123"),
				e2b.WithAPIURL("https://api.test"),
				e2b.WithHTTPClient(&http.Client{Transport: templateE2ERoundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.Method != http.MethodGet || req.URL.Path != "/templates/template-id" {
						return nil, fmt.Errorf("unexpected ownership request: %s %s", req.Method, req.URL.String())
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(string(payload))),
						Request:    req,
					}, nil
				})}),
			)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			err = verifyTemplateE2EOwnership(context.Background(), client, "template-id", tt.buildID, "unique-name")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("verifyTemplateE2EOwnership: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("verifyTemplateE2EOwnership error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestFindTemplateE2ECleanupTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		listPayload   string
		detailPayload string
		excludedIDs   []string
		wantID        string
		wantFound     bool
		wantErr       string
	}{
		{
			name:          "unique exact alias",
			listPayload:   `[{"templateID":"template-id","aliases":["unique-name"]}]`,
			detailPayload: `{"templateID":"template-id","aliases":["unique-name"],"builds":[]}`,
			wantID:        "template-id",
			wantFound:     true,
		},
		{
			name:          "new exact match excludes baseline template",
			listPayload:   `[{"templateID":"baseline-id","aliases":["unique-name"]},{"templateID":"template-id","names":["unique-name"]}]`,
			detailPayload: `{"templateID":"template-id","names":["unique-name"],"builds":[]}`,
			excludedIDs:   []string{"baseline-id"},
			wantID:        "template-id",
			wantFound:     true,
		},
		{
			name:        "baseline exact match is not a new template",
			listPayload: `[{"templateID":"template-id","aliases":["unique-name"]}]`,
			excludedIDs: []string{"template-id"},
		},
		{
			name:          "unique exact name",
			listPayload:   `[{"templateID":"template-id","names":["unique-name"]}]`,
			detailPayload: `{"templateID":"template-id","names":["unique-name"],"builds":[]}`,
			wantID:        "template-id",
			wantFound:     true,
		},
		{
			name:        "no exact match",
			listPayload: `[{"templateID":"template-id","aliases":["prefix-unique-name"],"names":["team/unique-name"]}]`,
		},
		{
			name:        "ambiguous exact match",
			listPayload: `[{"templateID":"template-a","aliases":["unique-name"]},{"templateID":"template-b","names":["unique-name"]}]`,
			wantErr:     "multiple templates",
		},
		{
			name:          "detail does not confirm name",
			listPayload:   `[{"templateID":"template-id","aliases":["unique-name"]}]`,
			detailPayload: `{"templateID":"template-id","aliases":["another-name"],"builds":[]}`,
			wantErr:       "does not contain unique e2e name",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := e2b.NewClient(
				e2b.WithAPIKey("e2b_0123"),
				e2b.WithAPIURL("https://api.test"),
				e2b.WithHTTPClient(&http.Client{Transport: templateE2ERoundTripFunc(func(req *http.Request) (*http.Response, error) {
					var payload string
					switch {
					case req.Method == http.MethodGet && req.URL.Path == "/templates":
						payload = tt.listPayload
					case req.Method == http.MethodGet && req.URL.Path == "/templates/template-id":
						payload = tt.detailPayload
					default:
						return nil, fmt.Errorf("unexpected cleanup target request: %s %s", req.Method, req.URL.String())
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(payload)),
						Request:    req,
					}, nil
				})}),
			)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			excluded := make(map[string]struct{}, len(tt.excludedIDs))
			for _, templateID := range tt.excludedIDs {
				excluded[templateID] = struct{}{}
			}
			gotID, found, err := findTemplateE2ECleanupTargetExcluding(context.Background(), client, "unique-name", excluded)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("findTemplateE2ECleanupTarget error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("findTemplateE2ECleanupTarget: %v", err)
			}
			if gotID != tt.wantID || found != tt.wantFound {
				t.Fatalf("findTemplateE2ECleanupTarget = (%q, %v), want (%q, %v)", gotID, found, tt.wantID, tt.wantFound)
			}
		})
	}
}

func TestDeleteOwnedTemplateE2E(t *testing.T) {
	t.Parallel()
	deleteCalls := 0
	client, err := e2b.NewClient(
		e2b.WithAPIKey("e2b_0123"),
		e2b.WithAPIURL("https://api.test"),
		e2b.WithHTTPClient(&http.Client{Transport: templateE2ERoundTripFunc(func(req *http.Request) (*http.Response, error) {
			var payload string
			switch {
			case req.Method == http.MethodGet && req.URL.Path == "/templates/template-id":
				payload = `{"templateID":"template-id","aliases":["unique-name"],"builds":[]}`
			case req.Method == http.MethodDelete && req.URL.Path == "/templates/template-id":
				deleteCalls++
			default:
				return nil, fmt.Errorf("unexpected owned-template cleanup request: %s %s", req.Method, req.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(payload)),
				Request:    req,
			}, nil
		})}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := deleteOwnedTemplateE2E(context.Background(), client, "template-id", "", "unique-name"); err != nil {
		t.Fatalf("deleteOwnedTemplateE2E: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("DeleteTemplate calls = %d, want 1", deleteCalls)
	}
}

func TestFindAndDeleteUnexpectedTemplateE2E(t *testing.T) {
	t.Parallel()
	deleteCalls := 0
	client, err := e2b.NewClient(
		e2b.WithAPIKey("e2b_0123"),
		e2b.WithAPIURL("https://api.test"),
		e2b.WithHTTPClient(&http.Client{Transport: templateE2ERoundTripFunc(func(req *http.Request) (*http.Response, error) {
			var payload string
			switch {
			case req.Method == http.MethodGet && req.URL.Path == "/templates":
				payload = `[{"templateID":"original-template-id","aliases":["existing-template-id"]},{"templateID":"unexpected-template-id","names":["existing-template-id"]}]`
			case req.Method == http.MethodGet && req.URL.Path == "/templates/unexpected-template-id":
				payload = `{"templateID":"unexpected-template-id","names":["existing-template-id"],"builds":[]}`
			case req.Method == http.MethodDelete && req.URL.Path == "/templates/unexpected-template-id":
				deleteCalls++
			default:
				return nil, fmt.Errorf("unexpected orphan-template request: %s %s", req.Method, req.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(payload)),
				Request:    req,
			}, nil
		})}),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	templateID, err := findAndDeleteUnexpectedTemplateE2E(
		context.Background(),
		client,
		"existing-template-id",
		map[string]struct{}{"original-template-id": {}},
	)
	if err != nil {
		t.Fatalf("findAndDeleteUnexpectedTemplateE2E: %v", err)
	}
	if templateID != "unexpected-template-id" {
		t.Fatalf("unexpected template ID = %q, want %q", templateID, "unexpected-template-id")
	}
	if deleteCalls != 1 {
		t.Fatalf("DeleteTemplate calls = %d, want 1", deleteCalls)
	}
}

func TestRunFailedTemplateRebuildE2E(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		templateID       string
		attemptErrors    []error
		confirmErr       error
		wantReference    string
		wantAttempts     []string
		wantConfirmCalls int
		wantErr          string
		wantBuildErr     string
	}{
		{
			name:          "template id reaches build failure",
			attemptErrors: []error{&e2b.BuildError{Message: "absolute paths are not allowed"}},
			wantReference: "template-id",
			wantAttempts:  []string{"template-id"},
			wantBuildErr:  "absolute paths are not allowed",
		},
		{
			name:       "invalid template id falls back to alias",
			templateID: "0template-id",
			attemptErrors: []error{
				&e2b.APIError{StatusCode: http.StatusBadRequest, Message: "Invalid template name '0template-id'"},
				&e2b.BuildError{Message: "absolute paths are not allowed"},
			},
			wantReference:    "unique-alias",
			wantAttempts:     []string{"0template-id", "unique-alias"},
			wantConfirmCalls: 1,
			wantBuildErr:     "absolute paths are not allowed",
		},
		{
			name:          "valid template id does not hide invalid name response",
			attemptErrors: []error{&e2b.APIError{StatusCode: http.StatusBadRequest, Message: "invalid template name 'template-id'"}},
			wantAttempts:  []string{"template-id"},
			wantErr:       "want local COPY BuildError or invalid-name APIError",
		},
		{
			name:          "unrelated bad request does not fall back",
			templateID:    "0template-id",
			attemptErrors: []error{&e2b.APIError{StatusCode: http.StatusBadRequest, Message: "invalid cpu count"}},
			wantAttempts:  []string{"0template-id"},
			wantErr:       "want local COPY BuildError or invalid-name APIError",
		},
		{
			name:          "authentication error does not fall back",
			templateID:    "0template-id",
			attemptErrors: []error{&e2b.APIError{StatusCode: http.StatusUnauthorized, Message: "unauthorized"}},
			wantAttempts:  []string{"0template-id"},
			wantErr:       "want local COPY BuildError or invalid-name APIError",
		},
		{
			name:       "alias ownership must be confirmed before fallback",
			templateID: "0template-id",
			attemptErrors: []error{
				&e2b.APIError{StatusCode: http.StatusBadRequest, Message: "invalid template name '0template-id'"},
			},
			confirmErr:       errors.New("ownership changed"),
			wantAttempts:     []string{"0template-id"},
			wantConfirmCalls: 1,
			wantErr:          "confirm existing template before alias fallback",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			templateID := tt.templateID
			if templateID == "" {
				templateID = "template-id"
			}
			attempts := make([]string, 0, len(tt.attemptErrors))
			confirmCalls := 0
			attempt := func(reference string) error {
				attempts = append(attempts, reference)
				if len(attempts) > len(tt.attemptErrors) {
					t.Fatalf("unexpected rebuild attempt %q", reference)
				}
				return tt.attemptErrors[len(attempts)-1]
			}
			confirm := func() error {
				confirmCalls++
				return tt.confirmErr
			}

			reference, buildErr, err := runFailedTemplateRebuildE2E(templateID, "unique-alias", attempt, confirm)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("runFailedTemplateRebuildE2E error = %v, want substring %q", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("runFailedTemplateRebuildE2E: %v", err)
				}
				if reference != tt.wantReference {
					t.Fatalf("failed rebuild reference = %q, want %q", reference, tt.wantReference)
				}
				if buildErr == nil || !strings.Contains(buildErr.Error(), tt.wantBuildErr) {
					t.Fatalf("failed rebuild BuildError = %v, want substring %q", buildErr, tt.wantBuildErr)
				}
			}
			if strings.Join(attempts, ",") != strings.Join(tt.wantAttempts, ",") {
				t.Fatalf("rebuild attempts = %v, want %v", attempts, tt.wantAttempts)
			}
			if confirmCalls != tt.wantConfirmCalls {
				t.Fatalf("ownership confirmation calls = %d, want %d", confirmCalls, tt.wantConfirmCalls)
			}
		})
	}
}

type templateE2ERoundTripFunc func(*http.Request) (*http.Response, error)

func (f templateE2ERoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
