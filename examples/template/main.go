package main

import (
	"context"
	"log"

	"github.com/aliyun-fc/e2b-go-sdk/examples/internal/sample"
)

func main() {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Fatal(recovered)
		}
	}()

	sample.RunTemplate(context.Background())
}
