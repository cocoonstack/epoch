package server

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// newTestUploadSessions returns an uploadSessions whose tempfiles live under
// t.TempDir() so the test framework cleans them up automatically.
func newTestUploadSessions(t *testing.T) *uploadSessions {
	t.Helper()
	return newUploadSessions(t.TempDir())
}

// readFinalized rewinds and drains a *FinalizedUpload, returning its bytes.
// The caller still owns the FinalizedUpload and must Close() it.
func readFinalized(t *testing.T, fu *FinalizedUpload) []byte {
	t.Helper()
	rdr, err := fu.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	data, err := io.ReadAll(rdr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return data
}

func mustStart(t *testing.T, u *uploadSessions) string {
	t.Helper()
	id, err := u.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return id
}

func TestUploadSessionsLifecycle(t *testing.T) {
	u := newTestUploadSessions(t)

	id := mustStart(t, u)
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

	fu, err := u.Finalize(id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	defer func() { _ = fu.Close() }()
	if got := string(readFinalized(t, fu)); got != "hello world" {
		t.Errorf("finalized data = %q, want %q", got, "hello world")
	}
	if fu.Size() != 11 {
		t.Errorf("Size = %d, want 11", fu.Size())
	}
	if u.Len() != 0 {
		t.Errorf("Len after Finalize = %d, want 0", u.Len())
	}
}

func TestUploadSessionsAppendUnknown(t *testing.T) {
	u := newTestUploadSessions(t)
	if _, err := u.Append("nonexistent", strings.NewReader("x")); !errors.Is(err, errUploadNotFound) {
		t.Errorf("Append unknown: got %v, want errUploadNotFound", err)
	}
}

func TestUploadSessionsFinalizeUnknown(t *testing.T) {
	u := newTestUploadSessions(t)
	if _, err := u.Finalize("nonexistent"); !errors.Is(err, errUploadNotFound) {
		t.Errorf("Finalize unknown: got %v, want errUploadNotFound", err)
	}
}

func TestUploadSessionsSizeCap(t *testing.T) {
	u := newTestUploadSessions(t)
	u.maxBytes = 10

	id := mustStart(t, u)
	if _, err := u.Append(id, strings.NewReader("0123456789")); err != nil {
		t.Fatalf("first append (at cap): %v", err)
	}
	if _, err := u.Append(id, strings.NewReader("X")); !errors.Is(err, errUploadTooLarge) {
		t.Errorf("over-cap append: got %v, want errUploadTooLarge", err)
	}
	// The failed append must not retain any data.
	fu, err := u.Finalize(id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	defer func() { _ = fu.Close() }()
	if got := string(readFinalized(t, fu)); got != "0123456789" {
		t.Errorf("after over-cap append: got %q, want %q", got, "0123456789")
	}
}

// TestUploadSessionsAppendRollback asserts the all-or-nothing contract: a
// chunk that overflows the cap must be discarded entirely, not partially
// applied up to the cap.
func TestUploadSessionsAppendRollback(t *testing.T) {
	u := newTestUploadSessions(t)
	u.maxBytes = 10

	id := mustStart(t, u)
	if _, err := u.Append(id, strings.NewReader("01234")); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// "56789ABCDE" is 10 bytes — 5 fit, 5 do not. The whole append must
	// be rejected and the buffer must remain at the prior 5 bytes.
	if _, err := u.Append(id, strings.NewReader("56789ABCDE")); !errors.Is(err, errUploadTooLarge) {
		t.Errorf("partial overflow append: got %v, want errUploadTooLarge", err)
	}
	fu, err := u.Finalize(id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	defer func() { _ = fu.Close() }()
	if got := string(readFinalized(t, fu)); got != "01234" {
		t.Errorf("after rollback: got %q, want %q", got, "01234")
	}
}

func TestUploadSessionsSingleAppendOversized(t *testing.T) {
	u := newTestUploadSessions(t)
	u.maxBytes = 5

	id := mustStart(t, u)
	if _, err := u.Append(id, bytes.NewReader([]byte("0123456789"))); !errors.Is(err, errUploadTooLarge) {
		t.Errorf("oversized single append: got %v, want errUploadTooLarge", err)
	}
	// The failed first append must leave an empty buffer, not the
	// truncated leading prefix.
	fu, err := u.Finalize(id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	defer func() { _ = fu.Close() }()
	if data := readFinalized(t, fu); len(data) != 0 {
		t.Errorf("after oversized first append: len = %d, want 0", len(data))
	}
}

// TestUploadSessionsEmptyAppendAtCap asserts that an empty append at exactly
// the cap succeeds. OCI clients may legally send a final PUT with no body
// after a sequence of PATCH chunks that filled the buffer to capacity.
func TestUploadSessionsEmptyAppendAtCap(t *testing.T) {
	u := newTestUploadSessions(t)
	u.maxBytes = 10

	id := mustStart(t, u)
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
	u := newTestUploadSessions(t)
	u.maxBytes = 100

	id := mustStart(t, u)
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
	fu, err := u.Finalize(id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	defer func() { _ = fu.Close() }()
	if got := string(readFinalized(t, fu)); got != "seed-" {
		t.Errorf("after read-error rollback: got %q, want %q", got, "seed-")
	}
}

func TestUploadSessionsCancel(t *testing.T) {
	u := newTestUploadSessions(t)
	id := mustStart(t, u)
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
	u := newTestUploadSessions(t)
	u.ttl = time.Minute
	u.now = func() time.Time { return now }

	stale := mustStart(t, u)
	if u.Len() != 1 {
		t.Fatalf("Len after first Start = %d, want 1", u.Len())
	}

	// Advance past TTL.
	now = now.Add(2 * time.Minute)

	// Starting a new session triggers eviction of the stale one.
	fresh := mustStart(t, u)
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
	u := newTestUploadSessions(t)
	const n = 50
	type result struct {
		id  string
		err error
	}
	results := make(chan result, n)
	for range n {
		go func() {
			id, err := u.Start()
			results <- result{id: id, err: err}
		}()
	}
	seen := make(map[string]bool)
	for range n {
		r := <-results
		if r.err != nil {
			t.Errorf("Start: %v", r.err)
			continue
		}
		if seen[r.id] {
			t.Errorf("duplicate id %q", r.id)
		}
		seen[r.id] = true
	}
	if u.Len() != n {
		t.Errorf("Len = %d, want %d", u.Len(), n)
	}
}

// TestFinalizedUploadCloseRemovesFile verifies that the tempfile backing a
// finalized session is removed from disk on Close, so disk space is reclaimed
// after the registry has persisted the bytes. We assert by counting files in
// the spool dir rather than reaching into FinalizedUpload's unexported field.
func TestFinalizedUploadCloseRemovesFile(t *testing.T) {
	u := newTestUploadSessions(t)
	id := mustStart(t, u)
	if _, err := u.Append(id, strings.NewReader("payload")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := countSpoolFiles(t, u.dir); got != 1 {
		t.Fatalf("spool files after Append = %d, want 1", got)
	}

	fu, err := u.Finalize(id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := fu.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := u.Append(id, strings.NewReader("x")); !errors.Is(err, errUploadNotFound) {
		t.Errorf("Append after Finalize: got %v, want errUploadNotFound", err)
	}
	if got := countSpoolFiles(t, u.dir); got != 0 {
		t.Errorf("spool files after Close = %d, want 0", got)
	}
}

// TestUploadSessionsRollbackPoisonOnTruncateError exercises the poison path:
// if rollback can't restore the tempfile (here simulated by closing it before
// the rollback runs) the session is evicted and subsequent Appends return
// errUploadNotFound rather than corrupting the blob.
func TestUploadSessionsRollbackPoisonOnTruncateError(t *testing.T) {
	u := newTestUploadSessions(t)
	u.maxBytes = 5

	id := mustStart(t, u)
	// Close the underlying tempfile so the next Append's rollback will
	// fail (Truncate on a closed *os.File returns an error). The session
	// must then be evicted.
	u.mu.Lock()
	closed := u.sessions[id].file.Close()
	u.mu.Unlock()
	if closed != nil {
		t.Fatalf("close tempfile: %v", closed)
	}
	if _, err := u.Append(id, strings.NewReader("01234567")); err == nil {
		t.Fatal("expected error from poisoned append, got nil")
	}
	if u.Len() != 0 {
		t.Errorf("session still present after poison, Len = %d", u.Len())
	}
	if _, err := u.Append(id, strings.NewReader("x")); !errors.Is(err, errUploadNotFound) {
		t.Errorf("post-poison Append: got %v, want errUploadNotFound", err)
	}
}

// countSpoolFiles returns the number of regular files directly under dir.
func countSpoolFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if e.Type().IsRegular() {
			n++
		}
	}
	return n
}
