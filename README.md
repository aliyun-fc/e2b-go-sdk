# E2B Go SDK

Official Go SDK for E2B sandboxes.

This package mirrors the public surface and protocol behavior of the E2B Python SDK while using Go idioms: `context.Context`, typed errors, interfaces through concrete modules, and functional options.

```go
package main

import (
	"context"
	"fmt"
	"log"

	e2b "github.com/e2b-dev/e2b-go-sdk"
)

func main() {
	ctx := context.Background()

	client, err := e2b.NewClient(e2b.WithAPIKey("e2b_..."))
	if err != nil {
		log.Fatal(err)
	}

	sandbox, err := client.CreateSandbox(ctx, e2b.WithTemplate("base"))
	if err != nil {
		log.Fatal(err)
	}
	defer sandbox.Kill(ctx)

	result, err := sandbox.Commands.Run(ctx, "echo hello from e2b")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Stdout)
}
```

## Coverage

- Sandbox lifecycle: create, connect, kill, pause, timeout, info, metrics, snapshots, network updates
- Sandbox files: read, stream, write, multi-file upload, list, stat, remove, rename, mkdir, watch
- Commands and PTY: run/start/connect, stream output, stdin, close stdin, kill, resize
- Git: clone, init, remotes, status, branches, add, commit, reset, restore, push, pull, config, auth helpers
- Templates: fluent template builder, build/background build, build status, exists, tags
- Volumes: create, connect, list, destroy, metadata, directories, file read/write/stream/remove

## Examples

```sh
go run ./examples/sandbox
go run ./examples/template
```

Running the entry file directly is also supported:

```sh
go run ./examples/sandbox/main.go
go run ./examples/template/main.go
```

## Configuration

The SDK follows the Python SDK environment defaults:

- `E2B_API_KEY`
- `E2B_VALIDATE_API_KEY`
- `E2B_DOMAIN`
- `E2B_API_URL`
- `E2B_SANDBOX_URL`
- `E2B_VOLUME_API_URL`
- `E2B_DEBUG`

API keys are validated by default. To use a custom key shape:

```go
client, err := e2b.NewClient(
	e2b.WithAPIKey("custom-key"),
	e2b.WithValidateAPIKey(false),
)
```

## Development

```sh
GOCACHE=/private/tmp/e2b-go-sdk-gocache go test ./...
```
