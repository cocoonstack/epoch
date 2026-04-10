package utils

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "  ", "first", "second"); got != "first" {
		t.Fatalf("FirstNonEmpty = %q, want %q", got, "first")
	}
	if got := FirstNonEmpty("", "  "); got != "" {
		t.Fatalf("FirstNonEmpty all blank = %q, want \"\"", got)
	}
}
