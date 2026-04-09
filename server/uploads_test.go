package server

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestUploadSessionsLifecycle(t *testing.T) {
	u := newUploadSessions()

	id := u.Start()
	if id == "" {
		t.Fatal("Start returned empty id")
	}
	if u.Len() != 1 {
		t.Fatalf("Len after Start = %d, want 1", u.Len())
	}

	size, err := u.Append(id, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if size != 5 {
		t.Fatalf("size after first append = %d, want 5", size)
	}

	size, err = u.Append(id, strings.NewReader(" world"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if size != 11 {
		t.Fatalf("size after second append = %d, want 11", size)
	}

	data, err := u.Finalize(id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("finalized data = %q, want %q", data, "hello world")
	}
	if u.Len() != 0 {
		t.Errorf("Len after Finalize = %d, want 0", u.Len())
	}
}

func TestUploadSessionsAppendUnknown(t *testing.T) {
	u := newUploadSessions()
	if _, err := u.Append("nonexistent", strings.NewReader("x")); !errors.Is(err, errUploadNotFound) {
		t.Errorf("Append unknown: got %v, want errUploadNotFound", err)
	}
}

func TestUploadSessionsFinalizeUnknown(t *testing.T) {
	u := newUploadSessions()
	if _, err := u.Finalize("nonexistent"); !errors.Is(err, errUploadNotFound) {
		t.Errorf("Finalize unknown: got %v, want errUploadNotFound", err)
	}
}

func TestUploadSessionsSizeCap(t *testing.T) {
	u := newUploadSessions()
	u.maxBytes = 10

	id := u.Start()
	if _, err := u.Append(id, strings.NewReader("0123456789")); err != nil {
		t.Fatalf("first append (at cap): %v", err)
	}
	if _, err := u.Append(id, strings.NewReader("X")); !errors.Is(err, errUploadTooLarge) {
		t.Errorf("over-cap append: got %v, want errUploadTooLarge", err)
	}
	// The failed append must not retain any data.
	data, _ := u.Finalize(id)
	if string(data) != "0123456789" {
		t.Errorf("after over-cap append: got %q, want %q", data, "0123456789")
	}
}

// TestUploadSessionsAppendRollback asserts the all-or-nothing contract: a
// chunk that overflows the cap must be discarded entirely, not partially
// applied up to the cap.
func TestUploadSessionsAppendRollback(t *testing.T) {
	u := newUploadSessions()
	u.maxBytes = 10

	id := u.Start()
	if _, err := u.Append(id, strings.NewReader("01234")); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// "56789ABCDE" is 10 bytes — 5 fit, 5 do not. The whole append must
	// be rejected and the buffer must remain at the prior 5 bytes.
	if _, err := u.Append(id, strings.NewReader("56789ABCDE")); !errors.Is(err, errUploadTooLarge) {
		t.Errorf("partial overflow append: got %v, want errUploadTooLarge", err)
	}
	data, _ := u.Finalize(id)
	if string(data) != "01234" {
		t.Errorf("after rollback: got %q, want %q", data, "01234")
	}
}

func TestUploadSessionsSingleAppendOversized(t *testing.T) {
	u := newUploadSessions()
	u.maxBytes = 5

	id := u.Start()
	if _, err := u.Append(id, bytes.NewReader([]byte("0123456789"))); !errors.Is(err, errUploadTooLarge) {
		t.Errorf("oversized single append: got %v, want errUploadTooLarge", err)
	}
	// The failed first append must leave an empty buffer, not the
	// truncated leading prefix.
	data, _ := u.Finalize(id)
	if len(data) != 0 {
		t.Errorf("after oversized first append: len = %d, want 0", len(data))
	}
}

// TestUploadSessionsEmptyAppendAtCap asserts that an empty append at exactly
// the cap succeeds. OCI clients may legally send a final PUT with no body
// after a sequence of PATCH chunks that filled the buffer to capacity.
func TestUploadSessionsEmptyAppendAtCap(t *testing.T) {
	u := newUploadSessions()
	u.maxBytes = 10

	id := u.Start()
	if _, err := u.Append(id, strings.NewReader("0123456789")); err != nil {
		t.Fatalf("fill to cap: %v", err)
	}
	size, err := u.Append(id, strings.NewReader(""))
	if err != nil {
		t.Errorf("empty append at cap: got %v, want nil", err)
	}
	if size != 10 {
		t.Errorf("size after empty append at cap = %d, want 10", size)
	}
}

// flakyReader returns some bytes then a hard error, simulating a mid-PATCH
// client disconnect.
type flakyReader struct {
	data    []byte
	err     error
	flushed bool
}

func (f *flakyReader) Read(p []byte) (int, error) {
	if !f.flushed {
		n := copy(p, f.data)
		f.flushed = true
		return n, nil
	}
	return 0, f.err
}

// TestUploadSessionsAppendReadErrorRollback asserts that an underlying read
// error mid-stream rolls the session back to its pre-Append state instead of
// leaving partial bytes.
func TestUploadSessionsAppendReadErrorRollback(t *testing.T) {
	u := newUploadSessions()
	u.maxBytes = 100

	id := u.Start()
	if _, err := u.Append(id, strings.NewReader("seed-")); err != nil {
		t.Fatalf("seed append: %v", err)
	}

	src := &flakyReader{data: []byte("partial-data"), err: io.ErrUnexpectedEOF}
	size, err := u.Append(id, src)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("flaky append: got %v, want io.ErrUnexpectedEOF", err)
	}
	if size != 5 {
		t.Errorf("rolled-back size = %d, want 5", size)
	}
	data, _ := u.Finalize(id)
	if string(data) != "seed-" {
		t.Errorf("after read-error rollback: got %q, want %q", data, "seed-")
	}
}

func TestUploadSessionsCancel(t *testing.T) {
	u := newUploadSessions()
	id := u.Start()
	u.Cancel(id)
	if u.Len() != 0 {
		t.Errorf("Len after Cancel = %d, want 0", u.Len())
	}
	if _, err := u.Append(id, strings.NewReader("x")); !errors.Is(err, errUploadNotFound) {
		t.Errorf("Append after Cancel: got %v, want errUploadNotFound", err)
	}
}

func TestUploadSessionsTTLEviction(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	u := newUploadSessions()
	u.ttl = time.Minute
	u.now = func() time.Time { return now }

	stale := u.Start()
	if u.Len() != 1 {
		t.Fatalf("Len after first Start = %d, want 1", u.Len())
	}

	// Advance past TTL.
	now = now.Add(2 * time.Minute)

	// Starting a new session triggers eviction of the stale one.
	fresh := u.Start()
	if u.Len() != 1 {
		t.Errorf("Len after eviction = %d, want 1 (only fresh)", u.Len())
	}
	if _, err := u.Append(stale, strings.NewReader("x")); !errors.Is(err, errUploadNotFound) {
		t.Errorf("stale Append: got %v, want errUploadNotFound", err)
	}
	if _, err := u.Append(fresh, strings.NewReader("x")); err != nil {
		t.Errorf("fresh Append: %v", err)
	}
}

func TestUploadSessionsConcurrentStart(t *testing.T) {
	u := newUploadSessions()
	const n = 50
	ids := make(chan string, n)
	for range n {
		go func() { ids <- u.Start() }()
	}
	seen := make(map[string]bool)
	for range n {
		id := <-ids
		if seen[id] {
			t.Errorf("duplicate id %q", id)
		}
		seen[id] = true
	}
	if u.Len() != n {
		t.Errorf("Len = %d, want %d", u.Len(), n)
	}
}
