package sample

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	defaultAPIURL            = "https://api.cn-beijing.e2b.fc.aliyuncs.com"
	defaultDomain            = "cn-beijing.e2b.fc.aliyuncs.com"
	defaultTemplate          = "code-interpreter-v1"
	defaultTemplateFromImage = "fc-e2b-registry.cn-beijing.cr.aliyuncs.com/swebench/sweb.eval.x86_64.astropy_1776_astropy-12907:latest"
)

func section(name string) {
	fmt.Printf("\n== %s ==\n", name)
}

func must(label string, err error) {
	if err != nil {
		panic(fmt.Errorf("%s: %w", label, err))
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func normalizeAPIURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" {
		return raw
	}
	if parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" {
		return raw
	}
	parsed.Scheme = "https"
	return parsed.String()
}

func enabled(key string) bool {
	value := strings.ToLower(os.Getenv(key))
	return value == "1" || value == "true" || value == "yes"
}

func boolPtr(value bool) *bool {
	return &value
}

func firstLines(s string, max int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > max {
		lines = lines[:max]
	}
	return strings.Join(lines, "\n")
}
