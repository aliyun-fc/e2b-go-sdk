package main

import (
	"context"
	"log"

	"github.com/e2b-dev/e2b-go-sdk/examples/internal/sample"
)

func main() {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Fatal(recovered)
		}
	}()

	sample.RunSandbox(context.Background())
}
