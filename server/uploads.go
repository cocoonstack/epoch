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
	file *os.File
	size int64
}

func (f *FinalizedUpload) Size() int64 { return f.size }

func (f *FinalizedUpload) Reader() (io.Reader, error) {
	if f.file == nil {
		return nil, errors.New("finalized upload already closed")
	}
	if _, err := f.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind upload tempfile: %w", err)
	}
	return f.file, nil
}

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

// Append streams data into the session. Failed appends are rolled back.
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

func (u *uploadSessions) poisonLocked(id string, sess *uploadSession) {
	sess.poisoned = true
	delete(u.sessions, id)
	closeUploadFile(sess.file)
}

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

func (u *uploadSessions) Cancel(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if sess, ok := u.sessions[id]; ok {
		delete(u.sessions, id)
		closeUploadFile(sess.file)
	}
}

func (u *uploadSessions) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.sessions)
}

func (u *uploadSessions) evictExpiredLocked() {
	cutoff := u.now().Add(-u.ttl)
	for id, sess := range u.sessions {
		if sess.createdAt.Before(cutoff) {
			delete(u.sessions, id)
			closeUploadFile(sess.file)
		}
	}
}

func (s *uploadSession) rollback(startSize int64) error {
	if err := s.file.Truncate(startSize); err != nil {
		return fmt.Errorf("truncate upload tempfile: %w", err)
	}
	if _, err := s.file.Seek(startSize, io.SeekStart); err != nil {
		return fmt.Errorf("seek upload tempfile: %w", err)
	}
	return nil
}

func closeUploadFile(f *os.File) {
	if f == nil {
		return
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}
