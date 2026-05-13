package server

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultUploadMaxBytes = int64(40) << 30 // 40 GiB per session
	defaultUploadTTL      = time.Hour
	uploadCopyBufSize     = 1 << 20 // 1 MiB
)

var (
	errUploadNotFound = errors.New("upload session not found")
	errUploadTooLarge = errors.New("upload session exceeded size cap")
)

// FinalizedUpload owns the tempfile of a finalized upload. Caller must Close it.
type FinalizedUpload struct {
	file   *os.File
	size   int64
	digest string // "sha256:..." computed inline during Append
}

// Size returns the total byte count of the upload.
func (f *FinalizedUpload) Size() int64 { return f.size }

// Digest returns the "sha256:..." digest computed while bytes streamed in.
// Saves a second pass over a potentially multi-GiB tempfile in the caller.
func (f *FinalizedUpload) Digest() string { return f.digest }

// Reader returns a reader positioned at the start of the upload data.
func (f *FinalizedUpload) Reader() (io.Reader, error) {
	if f.file == nil {
		return nil, errors.New("finalized upload already closed")
	}
	if _, err := f.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind upload tempfile: %w", err)
	}
	return f.file, nil
}

// Close removes the underlying tempfile and releases resources.
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

// uploadSessions tracks in-progress blob uploads using disk-backed tempfiles.
//
// Locking discipline:
//   - mu guards the sessions map. It is NEVER held across the long
//     io.CopyBuffer in Append — concurrent uploads to different sessions
//     would otherwise serialize on the global lock and stall the registry.
//   - Each *uploadSession has its own mu that serializes file writes
//     against rollback / close on that one session.
//   - scratchPool hands out 1 MiB copy buffers so concurrent Appends do
//     not contend on a shared scratch slice.
type uploadSessions struct {
	mu          sync.Mutex
	sessions    map[string]*uploadSession
	scratchPool sync.Pool // *[]byte buffers of uploadCopyBufSize
	dir         string
	maxBytes    int64
	ttl         time.Duration
	now         func() time.Time // injectable for tests
}

type uploadSession struct {
	mu        sync.Mutex // serializes file writes and rollback for this session
	file      *os.File
	hasher    hash.Hash // running sha256 of bytes written to file
	size      int64
	createdAt time.Time
	poisoned  bool // true after a failed rollback; subsequent appends rejected
}

func newUploadSessions(dir string) *uploadSessions {
	return &uploadSessions{
		sessions: make(map[string]*uploadSession),
		scratchPool: sync.Pool{New: func() any {
			buf := make([]byte, uploadCopyBufSize)
			return &buf
		}},
		dir:      dir,
		maxBytes: defaultUploadMaxBytes,
		ttl:      defaultUploadTTL,
		now:      time.Now,
	}
}

// Start creates a new upload session backed by a tempfile and returns its UUID.
func (u *uploadSessions) Start() (string, error) {
	u.evictExpired()

	f, err := os.CreateTemp(u.dir, "epoch-upload-*.bin")
	if err != nil {
		return "", fmt.Errorf("create upload tempfile: %w", err)
	}
	id := uuid.NewString()
	u.mu.Lock()
	u.sessions[id] = &uploadSession{file: f, hasher: sha256.New(), createdAt: u.now()}
	u.mu.Unlock()
	return id, nil
}

// Append streams data into the session. Failed appends roll back the
// running sha256 hasher too (see snapshotHash) so a retried PATCH does
// not double-hash the survivor bytes.
func (u *uploadSessions) Append(id string, src io.Reader) (int64, error) {
	u.evictExpired()

	u.mu.Lock()
	sess, ok := u.sessions[id]
	u.mu.Unlock()
	if !ok {
		return 0, errUploadNotFound
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.poisoned {
		return sess.size, errUploadNotFound
	}

	bufPtr, _ := u.scratchPool.Get().(*[]byte)
	defer u.scratchPool.Put(bufPtr)

	startSize := sess.size
	remaining := u.maxBytes - startSize
	hashState, hashErr := snapshotHash(sess.hasher)
	if hashErr != nil {
		return startSize, fmt.Errorf("snapshot hash state: %w", hashErr)
	}
	dst := io.MultiWriter(sess.file, sess.hasher)
	n, copyErr := io.CopyBuffer(dst, io.LimitReader(src, remaining+1), *bufPtr)
	if copyErr != nil {
		if rbErr := sess.rollback(startSize, hashState); rbErr != nil {
			u.poison(id, sess)
			return startSize, errors.Join(copyErr, rbErr)
		}
		return startSize, copyErr
	}
	if n > remaining {
		if rbErr := sess.rollback(startSize, hashState); rbErr != nil {
			u.poison(id, sess)
			return startSize, errors.Join(errUploadTooLarge, rbErr)
		}
		return startSize, errUploadTooLarge
	}
	sess.size = startSize + n
	return sess.size, nil
}

// Finalize completes an upload session and returns the finalized upload.
func (u *uploadSessions) Finalize(id string) (*FinalizedUpload, error) {
	u.mu.Lock()
	sess, ok := u.sessions[id]
	if ok {
		delete(u.sessions, id)
	}
	u.mu.Unlock()
	if !ok {
		return nil, errUploadNotFound
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return &FinalizedUpload{
		file:   sess.file,
		size:   sess.size,
		digest: "sha256:" + hex.EncodeToString(sess.hasher.Sum(nil)),
	}, nil
}

// Cancel aborts an upload session and removes its tempfile.
func (u *uploadSessions) Cancel(id string) {
	u.mu.Lock()
	sess, ok := u.sessions[id]
	if ok {
		delete(u.sessions, id)
	}
	u.mu.Unlock()
	if !ok {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	closeUploadFile(sess.file)
}

// Len returns the number of active upload sessions.
func (u *uploadSessions) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.sessions)
}

// poison evicts a session whose rollback failed. Caller must hold sess.mu;
// this function acquires u.mu to remove the map entry.
func (u *uploadSessions) poison(id string, sess *uploadSession) {
	sess.poisoned = true
	u.mu.Lock()
	delete(u.sessions, id)
	u.mu.Unlock()
	closeUploadFile(sess.file)
}

// evictExpired drops every session past its TTL. Each tempfile is closed
// OUTSIDE the global map lock and under the session's own mu so an
// in-flight Append on the evicted session unwinds first.
func (u *uploadSessions) evictExpired() {
	cutoff := u.now().Add(-u.ttl)
	var expired []*uploadSession
	u.mu.Lock()
	for id, sess := range u.sessions {
		if sess.createdAt.Before(cutoff) {
			delete(u.sessions, id)
			expired = append(expired, sess)
		}
	}
	u.mu.Unlock()
	for _, sess := range expired {
		sess.mu.Lock()
		closeUploadFile(sess.file)
		sess.mu.Unlock()
	}
}

func (s *uploadSession) rollback(startSize int64, hashState []byte) error {
	if err := s.file.Truncate(startSize); err != nil {
		return fmt.Errorf("truncate upload tempfile: %w", err)
	}
	if _, err := s.file.Seek(startSize, io.SeekStart); err != nil {
		return fmt.Errorf("seek upload tempfile: %w", err)
	}
	if err := restoreHash(s.hasher, hashState); err != nil {
		return fmt.Errorf("restore hash state: %w", err)
	}
	return nil
}

// snapshotHash captures the current hash.Hash state so a failed Append
// can be rolled back without rehashing the prefix.
func snapshotHash(h hash.Hash) ([]byte, error) {
	marshaler, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return nil, errors.New("hash does not implement encoding.BinaryMarshaler")
	}
	return marshaler.MarshalBinary()
}

// restoreHash returns the hasher to a snapshot captured by snapshotHash.
func restoreHash(h hash.Hash, state []byte) error {
	unmarshaler, ok := h.(encoding.BinaryUnmarshaler)
	if !ok {
		return errors.New("hash does not implement encoding.BinaryUnmarshaler")
	}
	return unmarshaler.UnmarshalBinary(state)
}

func closeUploadFile(f *os.File) {
	if f == nil {
		return
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}
