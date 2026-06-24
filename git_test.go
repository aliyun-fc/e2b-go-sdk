package e2b

import "testing"

func TestParseGitStatus(t *testing.T) {
	raw := "## main...origin/main [ahead 2, behind 1]\n M README.md\nA  new.go\nUU conflict.txt\n?? scratch.txt\n"
	status := parseGitStatus(raw)
	if status.Branch != "main" || status.Upstream != "origin/main" {
		t.Fatalf("branch/upstream = %q/%q", status.Branch, status.Upstream)
	}
	if status.Ahead != 2 || status.Behind != 1 {
		t.Fatalf("ahead/behind = %d/%d", status.Ahead, status.Behind)
	}
	if status.IsClean {
		t.Fatal("status should not be clean")
	}
	if len(status.Files) != 4 {
		t.Fatalf("files = %d", len(status.Files))
	}
	if len(status.Conflicts) != 1 || status.Conflicts[0].Path != "conflict.txt" {
		t.Fatalf("conflicts = %#v", status.Conflicts)
	}
}

func TestCloneDestinationFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/e2b-dev/sdk.git": "sdk",
		"git@github.com:e2b-dev/sdk.git":     "sdk",
		"https://github.com/e2b-dev/sdk":     "sdk",
	}
	for raw, want := range cases {
		if got := cloneDestinationFromURL(raw); got != want {
			t.Fatalf("cloneDestinationFromURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
