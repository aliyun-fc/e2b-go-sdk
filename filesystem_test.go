package e2b

import (
	"strings"
	"testing"
)

func TestEscapeQuotesStripsHeaderBreaks(t *testing.T) {
	got := escapeQuotes("a\"b\r\nc\\d")
	want := `a\"bc\\d`
	if got != want {
		t.Fatalf("escapeQuotes = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("escaped filename still contains header break: %q", got)
	}
}
