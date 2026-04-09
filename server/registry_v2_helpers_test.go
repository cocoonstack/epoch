package server

import (
	"errors"
	"fmt"
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
	if !isNotFound(fmt.Errorf("get manifest: %w", objectstore.ErrNotFound)) {
		t.Fatalf("expected wrapped objectstore.ErrNotFound to match via errors.Is")
	}
	if isNotFound(errors.New("boom")) {
		t.Fatalf("unexpected match for unrelated error")
	}
}
