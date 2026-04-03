package utils

import "testing"

func TestTruncate(t *testing.T) {
	if got := Truncate("abcdef", 3); got != "abc" {
		t.Fatalf("Truncate = %q, want %q", got, "abc")
	}
	if got := Truncate("ab", 3); got != "ab" {
		t.Fatalf("Truncate short string = %q, want %q", got, "ab")
	}
	if got := Truncate("abc", 0); got != "abc" {
		t.Fatalf("Truncate zero = %q, want %q", got, "abc")
	}
}
