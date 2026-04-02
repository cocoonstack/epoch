package server

import (
	"errors"
	"testing"

	"github.com/cocoonstack/epoch/objectstore"
)

func TestStripSHA256Prefix(t *testing.T) {
	if got := stripSHA256Prefix("sha256:abcdef"); got != "abcdef" {
		t.Fatalf("stripSHA256Prefix mismatch: got %q", got)
	}
	if got := stripSHA256Prefix("abcdef"); got != "abcdef" {
		t.Fatalf("stripSHA256Prefix should leave plain digest unchanged: got %q", got)
	}
}

func TestIsNotFound(t *testing.T) {
	if !isNotFound(objectstore.ErrNotFound) {
		t.Fatalf("expected objectstore.ErrNotFound to match")
	}
	if !isNotFound(errors.New("404 page missing")) {
		t.Fatalf("expected 404-style error to match")
	}
	if isNotFound(errors.New("boom")) {
		t.Fatalf("unexpected match for unrelated error")
	}
}
