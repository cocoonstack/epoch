package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Constants governing disk-backed blob upload sessions used by the OCI push
// flow. The defaults are conservative; tests use shorter values via the
// configurable fields on uploadSessions.
//
// Note: maxBytes must stay below math.MaxInt64-1 because Append uses
// `remaining+1` as the LimitReader cap to detect overflow without a second
// pass. The 40 GiB default is many orders of magnitude below that ceiling.
//
// uploadCopyBufSize is the io.CopyBuffer scratch size used for streaming
// chunk bodies into the session tempfile. The source is an io.LimitReader
// wrapping the HTTP request body, which does not forward WriterTo, so
// io.Copy would fall through to its 32 KiB default — millions of syscalls
// per multi-GiB layer. A 1 MiB buffer cuts that ~32x. The buffer lives on
// uploadSessions and is reused across Append calls under u.mu.
const (
	defaultUploadMaxBytes = int64(40) << 30 // 40 GiB per session
	defaultUploadTTL      = time.Hour
	uploadCopyBufSize     = 1 << 20 // 1 MiB
)

var (
	// errUploadNotFound means the upload session was never created or has
	// been evicted/finalized.
	errUploadNotFound = errors.New("upload session not found")
	// errUploadTooLarge means an Append would exceed the per-session cap.
	errUploadTooLarge = errors.New("upload session exceeded size cap")
)

// FinalizedUpload owns the tempfile holding a finalized upload session.
// Callers MUST Close it after digest verification + persisting; that is the
// only way the underlying tempfile gets removed from disk.
type FinalizedUpload struct {
	file *os.File
	size int64
}

// Size returns the total bytes accumulated by the session.
func (f *FinalizedUpload) Size() int64 { return f.size }

// Reader rewinds the underlying tempfile to offset 0 and returns it as an
// io.Reader. Safe to call multiple times — the digest-verify pass and the
// upload-to-S3 pass each call Reader once and stream the same file twice
// without copying its contents into memory.
func (f *FinalizedUpload) Reader() (io.Reader, error) {
	if f.file == nil {
		return nil, errors.New("finalized upload already closed")
	}
	if _, err := f.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind upload tempfile: %w", err)
	}
	return f.file, nil
}

// Close removes the underlying tempfile. Idempotent.
func (f *FinalizedUpload) Close() error {
	if f.file == nil {
		return nil
	}
	name := f.file.Name()
	closeErr := f.file.Close()
	f.file = nil
	rmErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return rmErr
}

// uploadSessions tracks in-progress OCI blob uploads.
//
// The OCI Distribution spec lets clients push a blob in two ways:
//   - Monolithic: POST .../uploads/?digest=sha256:xxx with the full body
//   - Chunked: POST .../uploads/ → PATCH ... → ... → PUT ...
//
// Chunked uploads need server-side state (the partial body) between requests.
// We persist that state to a per-session tempfile so multi-GiB OCI layers do
// not pin RAM. A per-session size cap and TTL-based eviction bound disk use.
//
// IMPORTANT: dir must point at a directory backed by real disk. The default
// os.TempDir() on systemd hosts is often a tmpfs, which would defeat the
// whole point of this refactor and OOM the host on multi-GiB pushes. The
// caller (server.New) is responsible for picking a sensible directory; tests
// pass t.TempDir().
//
// uploadSessions is safe for concurrent use.
type uploadSessions struct {
	mu       sync.Mutex
	sessions map[string]*uploadSession
	scratch  []byte // io.CopyBuffer reuse buffer; only touched under mu
	dir      string
	maxBytes int64
	ttl      time.Duration
	now      func() time.Time // injectable for tests
}

type uploadSession struct {
	file      *os.File
	size      int64
	createdAt time.Time
	poisoned  bool // true after a failed rollback; subsequent appends rejected
}

// newUploadSessions returns an upload session tracker that spools tempfiles
// into dir. dir must already exist and be writable.
func newUploadSessions(dir string) *uploadSessions {
	return &uploadSessions{
		sessions: make(map[string]*uploadSession),
		scratch:  make([]byte, uploadCopyBufSize),
		dir:      dir,
		maxBytes: defaultUploadMaxBytes,
		ttl:      defaultUploadTTL,
		now:      time.Now,
	}
}

// Start creates a new upload session backed by a tempfile and returns its ID.
// Stale sessions (older than ttl) are swept first so abandoned uploads cannot
// pin disk space.
func (u *uploadSessions) Start() (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.evictExpiredLocked()

	f, err := os.CreateTemp(u.dir, "epoch-upload-*.bin")
	if err != nil {
		return "", fmt.Errorf("create upload tempfile: %w", err)
	}
	id := uuid.NewString()
	u.sessions[id] = &uploadSession{file: f, createdAt: u.now()}
	return id, nil
}

// Append streams data into the session's tempfile and returns the new total
// size after the append. On any error (read failure or over-cap) the file is
// truncated back to its pre-Append length and the file pointer is rewound, so
// failed appends are all-or-nothing.
//
// If the rollback itself fails (truncate or seek error) the session is
// poisoned and removed from the active map — its tempfile would otherwise be
// in an indeterminate state and any subsequent append could corrupt the blob.
//
// The reader is consumed via a LimitReader with a cap of remaining+1 so an
// over-cap upload is detected without a second pass over the data. An empty
// reader at exactly the cap is a no-op success (OCI clients can legally send
// a final PUT with no body).
//
// Returns errUploadNotFound for unknown (or poisoned-and-evicted) sessions
// and errUploadTooLarge when the append would exceed maxBytes.
func (u *uploadSessions) Append(id string, src io.Reader) (int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.evictExpiredLocked()
	sess, ok := u.sessions[id]
	if !ok {
		return 0, errUploadNotFound
	}
	startSize := sess.size
	remaining := u.maxBytes - startSize
	// Reuse u.scratch across every Append — the whole method runs under u.mu
	// so the buffer is never touched concurrently, and a 1 MiB alloc per
	// PATCH on multi-GiB chunked uploads would otherwise add up fast.
	n, copyErr := io.CopyBuffer(sess.file, io.LimitReader(src, remaining+1), u.scratch)
	if copyErr != nil {
		if rbErr := sess.rollback(startSize); rbErr != nil {
			u.poisonLocked(id, sess)
			return startSize, errors.Join(copyErr, rbErr)
		}
		return startSize, copyErr
	}
	if n > remaining {
		if rbErr := sess.rollback(startSize); rbErr != nil {
			u.poisonLocked(id, sess)
			return startSize, errors.Join(errUploadTooLarge, rbErr)
		}
		return startSize, errUploadTooLarge
	}
	sess.size = startSize + n
	return sess.size, nil
}

// poisonLocked evicts a session whose tempfile is in an indeterminate state
// after a failed rollback. Caller must hold u.mu.
func (u *uploadSessions) poisonLocked(id string, sess *uploadSession) {
	sess.poisoned = true
	delete(u.sessions, id)
	closeUploadFile(sess.file)
}

// Finalize removes the session from the active map and returns its tempfile
// wrapped in a FinalizedUpload. The caller is responsible for digest
// verification, persisting the bytes, and Close()-ing.
func (u *uploadSessions) Finalize(id string) (*FinalizedUpload, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	sess, ok := u.sessions[id]
	if !ok {
		return nil, errUploadNotFound
	}
	delete(u.sessions, id)
	return &FinalizedUpload{file: sess.file, size: sess.size}, nil
}

// Cancel removes the session and deletes the underlying tempfile. Used when a
// client abandons an upload (and exposed for tests).
func (u *uploadSessions) Cancel(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if sess, ok := u.sessions[id]; ok {
		delete(u.sessions, id)
		closeUploadFile(sess.file)
	}
}

// Len returns the number of live sessions. Test-only helper.
func (u *uploadSessions) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.sessions)
}

// evictExpiredLocked drops sessions whose age exceeds ttl and removes their
// tempfiles. Caller must hold u.mu.
func (u *uploadSessions) evictExpiredLocked() {
	cutoff := u.now().Add(-u.ttl)
	for id, sess := range u.sessions {
		if sess.createdAt.Before(cutoff) {
			delete(u.sessions, id)
			closeUploadFile(sess.file)
		}
	}
}

// rollback truncates the session's tempfile back to startSize and rewinds
// the write offset, restoring the all-or-nothing append contract. Caller
// must hold u.mu (the only operation here is on s.file which is also under
// u.mu in practice).
func (s *uploadSession) rollback(startSize int64) error {
	if err := s.file.Truncate(startSize); err != nil {
		return fmt.Errorf("truncate upload tempfile: %w", err)
	}
	if _, err := s.file.Seek(startSize, io.SeekStart); err != nil {
		return fmt.Errorf("seek upload tempfile: %w", err)
	}
	return nil
}

// closeUploadFile releases a session-owned tempfile. Errors are swallowed
// because there is no useful caller — failures here only leak disk space, not
// correctness.
func closeUploadFile(f *os.File) {
	if f == nil {
		return
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}
