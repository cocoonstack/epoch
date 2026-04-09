package server

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Constants governing in-memory blob upload sessions used by the OCI push
// flow. The defaults are conservative; tests use shorter values via the
// configurable fields on uploadSessions.
//
// Note: maxBytes must stay below math.MaxInt64-1 because Append uses
// `remaining+1` as the LimitReader cap to detect overflow without a second
// pass. The 20 GiB default is many orders of magnitude below that ceiling.
const (
	defaultUploadMaxBytes = int64(20) << 30 // 20 GiB per session
	defaultUploadTTL      = time.Hour
)

var (
	// errUploadNotFound means the upload session was never created or has
	// been evicted/finalized.
	errUploadNotFound = errors.New("upload session not found")
	// errUploadTooLarge means an Append would exceed the per-session cap.
	errUploadTooLarge = errors.New("upload session exceeded size cap")
)

// uploadSessions tracks in-progress OCI blob uploads.
//
// The OCI Distribution spec lets clients push a blob in two ways:
//   - Monolithic: POST .../uploads/?digest=sha256:xxx with the full body
//   - Chunked: POST .../uploads/ → PATCH ... → ... → PUT ...
//
// Chunked uploads need server-side state (the partial body) between requests.
// We buffer that state in memory: chunked uploads are uncommon and OCI image
// layers are typically a few hundred MB, so a per-session cap plus TTL-based
// eviction is enough to bound memory without persistence.
//
// uploadSessions is safe for concurrent use.
type uploadSessions struct {
	mu       sync.Mutex
	sessions map[string]*uploadSession
	maxBytes int64
	ttl      time.Duration
	now      func() time.Time // injectable for tests
}

type uploadSession struct {
	buf       bytes.Buffer
	createdAt time.Time
}

func newUploadSessions() *uploadSessions {
	return &uploadSessions{
		sessions: make(map[string]*uploadSession),
		maxBytes: defaultUploadMaxBytes,
		ttl:      defaultUploadTTL,
		now:      time.Now,
	}
}

// Start creates a new upload session and returns its ID. Stale sessions
// (older than ttl) are swept first so abandoned uploads cannot pin memory.
func (u *uploadSessions) Start() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.evictExpiredLocked()
	id := uuid.NewString()
	u.sessions[id] = &uploadSession{createdAt: u.now()}
	return id
}

// Append streams data into the session's buffer and returns the new buffer
// size after the append. On any error (read failure or over-cap) the buffer
// is rolled back to its pre-Append length and the returned size reflects the
// pre-Append state — failed appends are all-or-nothing.
//
// The reader is consumed via a LimitReader with a cap of remaining+1 so an
// over-cap upload is detected without a second pass over the data. An empty
// reader at exactly the cap is a no-op success (OCI clients can legally send
// a final PUT with no body).
//
// Returns errUploadNotFound for unknown sessions and errUploadTooLarge when
// the append would exceed maxBytes.
func (u *uploadSessions) Append(id string, src io.Reader) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	sess, ok := u.sessions[id]
	if !ok {
		return 0, errUploadNotFound
	}
	startLen := sess.buf.Len()
	remaining := u.maxBytes - int64(startLen)
	n, err := sess.buf.ReadFrom(io.LimitReader(src, remaining+1))
	if err != nil {
		sess.buf.Truncate(startLen)
		return startLen, err
	}
	if n > remaining {
		sess.buf.Truncate(startLen)
		return startLen, errUploadTooLarge
	}
	return sess.buf.Len(), nil
}

// Finalize removes the session and returns its accumulated buffer. The
// caller is responsible for verifying the digest and persisting the bytes.
func (u *uploadSessions) Finalize(id string) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	sess, ok := u.sessions[id]
	if !ok {
		return nil, errUploadNotFound
	}
	delete(u.sessions, id)
	return sess.buf.Bytes(), nil
}

// Cancel removes the session without returning its data. Used when a client
// abandons an upload (and exposed for tests).
func (u *uploadSessions) Cancel(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.sessions, id)
}

// Len returns the number of live sessions. Test-only helper.
func (u *uploadSessions) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.sessions)
}

// evictExpiredLocked drops sessions whose age exceeds ttl. Caller must hold u.mu.
func (u *uploadSessions) evictExpiredLocked() {
	cutoff := u.now().Add(-u.ttl)
	for id, sess := range u.sessions {
		if sess.createdAt.Before(cutoff) {
			delete(u.sessions, id)
		}
	}
}
