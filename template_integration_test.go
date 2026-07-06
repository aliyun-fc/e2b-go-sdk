package e2b

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const defaultTemplateIntegrationImage = "fc-e2b-registry.cn-beijing.cr.aliyuncs.com/swebench/sweb.eval.x86_64.astropy_1776_astropy-12907:latest"

func TestTemplateIntegrationBuildCRUD(t *testing.T) {
	if !envEnabled("E2B_TEMPLATE_INTEGRATION") {
		t.Skip("set E2B_TEMPLATE_INTEGRATION=1 to run the real template build CRUD test")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		t.Fatal("set E2B_API_KEY")
	}

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("E2B_TEMPLATE_INTEGRATION_TIMEOUT", 30*time.Minute))
	defer cancel()

	client, err := NewClient(
		WithAPIKey(apiKey),
		WithAPIURL(envString("E2B_SAMPLE_API_URL", "https://api.cn-beijing.e2b.fc.aliyuncs.com")),
		WithDomain(envString("E2B_SAMPLE_DOMAIN", "cn-beijing.e2b.fc.aliyuncs.com")),
		WithIntegration("e2b-go-sdk-template-integration/1.0"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	name := envString("E2B_TEMPLATE_INTEGRATION_NAME", "go-sdk-template-test-"+time.Now().Format("20060102150405"))
	image := envString("E2B_TEMPLATE_INTEGRATION_IMAGE", defaultTemplateIntegrationImage)

	build, err := client.BuildTemplate(
		ctx,
		NewTemplate().FromImage(image),
		name,
		WithTemplateCPUCount(2),
		WithTemplateMemoryMB(2048),
		WithTemplatePollPeriod(5*time.Second),
	)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	t.Logf("template_id=%s build_id=%s name=%s", build.TemplateID, build.BuildID, name)

	defer func() {
		deleted, err := client.DeleteTemplate(context.Background(), build.TemplateID)
		t.Logf("delete template: deleted=%v err=%v", deleted, err)
	}()

	templates, err := client.ListTemplates(ctx, "")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	found := false
	for _, template := range templates {
		if template.TemplateID == build.TemplateID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("template %s not found in list", build.TemplateID)
	}

	template, err := client.GetTemplate(ctx, build.TemplateID, 100, "")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if template.TemplateID != build.TemplateID {
		t.Fatalf("GetTemplate templateID = %q", template.TemplateID)
	}

	exists, err := client.TemplateExists(ctx, name)
	if err != nil {
		t.Fatalf("TemplateExists: %v", err)
	}
	if !exists {
		t.Fatalf("template alias/name %q should exist", name)
	}

	if envEnabled("E2B_TEMPLATE_INTEGRATION_SANDBOX") {
		sandbox, err := client.CreateSandbox(ctx, WithTemplate(name), WithTimeout(900))
		if err != nil {
			t.Fatalf("CreateSandbox from built template: %v", err)
		}
		defer func() { _, _ = sandbox.Kill(context.Background()) }()

		result, err := sandbox.Commands.Run(ctx, "python3 -c 'print(\"helloworld\")'", WithCommandTimeout(60*time.Second), WithCommandRequestTimeout(120*time.Second))
		if err != nil {
			t.Fatalf("run python: %v", err)
		}
		if strings.TrimSpace(result.Stdout) != "helloworld" {
			t.Fatalf("stdout = %q", result.Stdout)
		}
	}
}

func TestTemplateIntegrationBuildCopy(t *testing.T) {
	if !envEnabled("E2B_TEMPLATE_COPY_INTEGRATION") {
		t.Skip("set E2B_TEMPLATE_COPY_INTEGRATION=1 to run the real template COPY build test")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		t.Fatal("set E2B_API_KEY")
	}

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte("hello copy integration\n"), 0o644); err != nil {
		t.Fatalf("write copy fixture: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("E2B_TEMPLATE_INTEGRATION_TIMEOUT", 30*time.Minute))
	defer cancel()

	client, err := NewClient(
		WithAPIKey(apiKey),
		WithAPIURL(envString("E2B_SAMPLE_API_URL", "https://api.cn-beijing.e2b.fc.aliyuncs.com")),
		WithDomain(envString("E2B_SAMPLE_DOMAIN", "cn-beijing.e2b.fc.aliyuncs.com")),
		WithIntegration("e2b-go-sdk-template-copy-integration/1.0"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	name := envString("E2B_TEMPLATE_COPY_INTEGRATION_NAME", "go-sdk-template-copy-test-"+time.Now().Format("20060102150405"))
	image := envString("E2B_TEMPLATE_INTEGRATION_IMAGE", defaultTemplateIntegrationImage)
	build, err := client.BuildTemplate(
		ctx,
		NewTemplate().FromImage(image).Copy("hello.txt", "/tmp/e2b-copy-hello.txt"),
		name,
		WithTemplateCPUCount(2),
		WithTemplateMemoryMB(2048),
		WithTemplatePollPeriod(5*time.Second),
	)
	if err != nil {
		if strings.Contains(err.Error(), "steps are not supported") {
			t.Skipf("control plane does not support template steps in this environment: %v", err)
		}
		t.Fatalf("BuildTemplate COPY: %v", err)
	}
	t.Logf("template_id=%s build_id=%s name=%s", build.TemplateID, build.BuildID, name)
	defer func() {
		deleted, err := client.DeleteTemplate(context.Background(), build.TemplateID)
		t.Logf("delete template: deleted=%v err=%v", deleted, err)
	}()

	sandbox, err := client.CreateSandbox(ctx, WithTemplate(name), WithTimeout(900))
	if err != nil {
		t.Fatalf("CreateSandbox from COPY template: %v", err)
	}
	defer func() { _, _ = sandbox.Kill(context.Background()) }()

	result, err := sandbox.Commands.Run(ctx, "cat /tmp/e2b-copy-hello.txt", WithCommandTimeout(60*time.Second), WithCommandRequestTimeout(120*time.Second))
	if err != nil {
		t.Fatalf("cat copied file: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "hello copy integration" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}

func envEnabled(key string) bool {
	value := strings.ToLower(os.Getenv(key))
	return value == "1" || value == "true" || value == "yes"
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
