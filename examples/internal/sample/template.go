package sample

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	e2b "github.com/e2b-dev/e2b-go-sdk"
)

// RunTemplate runs the template build sample.
func RunTemplate(ctx context.Context) {
	apiKey := env("E2B_API_KEY", "")
	if apiKey == "" {
		log.Fatal("set E2B_API_KEY first")
	}

	apiURL := normalizeAPIURL(env("E2B_API_URL", defaultAPIURL))
	domain := env("E2B_DOMAIN", defaultDomain)
	fmt.Printf("using api_url=%s domain=%s\n", apiURL, domain)

	client, err := e2b.NewClient(
		e2b.WithAPIKey(apiKey),
		e2b.WithAPIURL(apiURL),
		e2b.WithDomain(domain),
		e2b.WithIntegration("e2b-go-sdk-template-sample/1.0"),
	)
	must("create client", err)

	runTemplateSample(ctx, client)
}

func runTemplateSample(ctx context.Context, client *e2b.Client) {
	section("template build")

	fromImage := env("E2B_SAMPLE_TEMPLATE_FROM_IMAGE", defaultTemplateFromImage)
	templateName := env("E2B_SAMPLE_TEMPLATE_BUILD_NAME", "go-sdk-template-sample-"+time.Now().Format("20060102150405"))
	fmt.Printf("template_name=%s\n", templateName)
	fmt.Printf("from_image=%s\n", fromImage)

	build, err := client.BuildTemplate(
		ctx,
		e2b.NewTemplate().FromImage(fromImage),
		templateName,
		e2b.WithTemplateCPUCount(2),
		e2b.WithTemplateMemoryMB(2048),
		e2b.WithTemplateSkipCache(enabled("E2B_SAMPLE_TEMPLATE_SKIP_CACHE")),
		e2b.WithTemplatePollPeriod(5*time.Second),
	)
	must("build template", err)
	fmt.Printf("template_id=%s build_id=%s\n", build.TemplateID, build.BuildID)

	if !enabled("E2B_SAMPLE_KEEP_TEMPLATE") {
		defer func() {
			deleted, err := client.DeleteTemplate(context.Background(), build.TemplateID)
			fmt.Printf("template deleted: %v err=%v\n", deleted, err)
		}()
	}

	templates, err := client.ListTemplates(ctx, "")
	must("list templates", err)
	found := false
	for _, template := range templates {
		if template.TemplateID == build.TemplateID {
			found = true
			fmt.Printf("listed template: %s aliases=%v names=%v status=%s\n",
				template.TemplateID, template.Aliases, template.Names, template.BuildStatus)
			break
		}
	}
	fmt.Printf("templates listed=%d found_built=%v\n", len(templates), found)

	details, err := client.GetTemplate(ctx, build.TemplateID, 100, "")
	must("get template", err)
	fmt.Printf("template builds=%d has_next=%v\n", len(details.Builds), details.HasNext)

	exists, err := client.TemplateExists(ctx, templateName)
	must("check template alias", err)
	fmt.Printf("template exists: %v\n", exists)

	templateSandbox, err := client.CreateSandbox(
		ctx,
		e2b.WithTemplate(templateName),
		e2b.WithTimeout(900),
	)
	must("create sandbox from built template", err)
	defer func() {
		killed, err := templateSandbox.Kill(context.Background())
		fmt.Printf("template sandbox killed: %v err=%v\n", killed, err)
	}()

	fmt.Println("template sandbox_id:", templateSandbox.SandboxID())
	execution, err := templateSandbox.Commands.Run(
		ctx,
		"python3 -c 'print(\"helloworld\")'",
		e2b.WithCommandTimeout(60*time.Second),
		e2b.WithCommandRequestTimeout(120*time.Second),
	)
	must("run template sandbox helloworld", err)
	fmt.Printf("template sandbox stdout: %s\n", strings.TrimSpace(execution.Stdout))
}
