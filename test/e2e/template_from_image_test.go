package main

import (
	"context"
	"strings"
	"testing"
	"time"

	e2b "github.com/e2b-dev/e2b-go-sdk"
)

const (
	templateE2EFlag         = "E2B_TEMPLATE_E2E"
	defaultTemplateE2EImage = "fc-e2b-registry.ap-southeast-1.cr.aliyuncs.com/runtime/base:v0.0.39"
)

// TestTemplateFromImageBuildQueryDeleteAndSpawn builds a template from an image,
// queries and deletes it, then spawns a sandbox from it, against a real control
// plane. It is skipped unless E2B_TEMPLATE_E2E is set.
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

	testID := time.Now().UTC().Format("20060102150405")
	name := env("E2B_TEMPLATE_E2E_NAME", "go-sdk-e2e-template-"+testID)
	image := env("E2B_TEMPLATE_E2E_IMAGE", defaultTemplateE2EImage)
	tag := env("E2B_TEMPLATE_E2E_TAG", "from-image-e2e")

	var templateID string
	templateDeleted := false
	t.Cleanup(func() {
		if templateID == "" || templateDeleted || enabled("E2B_E2E_KEEP_TEMPLATE") {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		deleted, err := client.DeleteTemplate(cleanupCtx, templateID)
		t.Logf("template cleanup: deleted=%v err=%v", deleted, err)
	})

	build, err := client.BuildTemplate(
		ctx,
		e2b.NewTemplate().FromImage(image),
		name,
		e2b.WithTemplateCPUCount(2),
		e2b.WithTemplateMemoryMB(2048),
		e2b.WithTemplateTags(tag),
		e2b.WithTemplatePollPeriod(5*time.Second),
	)
	if err != nil {
		t.Fatalf("BuildTemplate from image %q: %v", image, err)
	}
	templateID = build.TemplateID
	if build.TemplateID == "" || build.BuildID == "" {
		t.Fatalf("BuildTemplate returned incomplete build info: %+v", build)
	}
	t.Logf("template_id=%s build_id=%s name=%s image=%s", build.TemplateID, build.BuildID, name, image)

	t.Run("query", func(t *testing.T) {
		status, err := client.GetBuildStatus(ctx, build.TemplateID, build.BuildID, 0)
		if err != nil {
			t.Fatalf("GetBuildStatus: %v", err)
		}
		if status.Status != e2b.TemplateBuildStatusReady {
			t.Fatalf("build status = %q, want %q", status.Status, e2b.TemplateBuildStatusReady)
		}

		exists, err := client.TemplateExists(ctx, name)
		if err != nil {
			t.Fatalf("TemplateExists(%q): %v", name, err)
		}
		if !exists {
			t.Fatalf("TemplateExists(%q) returned false", name)
		}

		templates, err := client.ListTemplates(ctx, "")
		if err != nil {
			t.Fatalf("ListTemplates: %v", err)
		}
		if !containsTemplate(templates, build.TemplateID) {
			t.Fatalf("template %s not found in ListTemplates", build.TemplateID)
		}

		details, err := client.GetTemplate(ctx, build.TemplateID, 20, "")
		if err != nil {
			t.Fatalf("GetTemplate: %v", err)
		}
		if details.TemplateID != build.TemplateID {
			t.Fatalf("GetTemplate templateID = %q, want %q", details.TemplateID, build.TemplateID)
		}
		if !containsTemplateBuild(details.Builds, build.BuildID) {
			t.Fatalf("build %s not found in GetTemplate builds: %+v", build.BuildID, details.Builds)
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
		sandbox, err := client.CreateSandbox(
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
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cleanupCancel()
			killed, err := sandbox.Kill(cleanupCtx)
			t.Logf("template sandbox cleanup: killed=%v err=%v", killed, err)
		})

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

	t.Run("delete", func(t *testing.T) {
		deleted, err := client.DeleteTemplate(ctx, build.TemplateID)
		if err != nil {
			t.Fatalf("DeleteTemplate: %v", err)
		}
		if !deleted {
			t.Fatalf("DeleteTemplate(%s) returned false", build.TemplateID)
		}
		templateDeleted = true

		deletedAgain, err := client.DeleteTemplate(ctx, build.TemplateID)
		if err != nil {
			t.Fatalf("DeleteTemplate second call: %v", err)
		}
		if deletedAgain {
			t.Fatalf("DeleteTemplate(%s) returned true after deletion", build.TemplateID)
		}
	})
}

// containsTemplateBuild reports whether items contains a build with the given
// buildID.
func containsTemplateBuild(items []e2b.TemplateBuild, buildID string) bool {
	for _, item := range items {
		if item.BuildID == buildID {
			return true
		}
	}
	return false
}
