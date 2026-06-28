package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/projecteru2/core/log"
)

const (
	uploadBodyLimit = defaultUploadMaxBytes

	// discardBlobTimeout bounds the detached corrupt-blob delete; the object
	// store layer imposes no request timeout of its own.
	discardBlobTimeout = 30 * time.Second
)

func (s *Server) v2InitBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")

	if digest := r.URL.Query().Get("digest"); digest != "" {
		s.persistMonolithicUpload(w, r, name, digest)
		return
	}

	id, err := s.uploads.Start()
	if err != nil {
		log.WithFunc("server.v2InitBlobUpload").Errorf(r.Context(), err, "start upload session failed")
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	w.Header().Set("Location", uploadLocation(name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	w.Header().Set("Range", "0-0")
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) v2PatchBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	id := urlVar(r, "uuid")

	body := io.LimitReader(r.Body, uploadBodyLimit)
	size, err := s.uploads.Append(id, body)
	if err != nil {
		drainBody(body)
		writeUploadAppendError(w, err)
		return
	}

	w.Header().Set("Location", uploadLocation(name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	w.Header().Set("Range", "0-"+strconv.FormatInt(size-1, 10))
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) v2CompleteBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	id := urlVar(r, "uuid")
	digest := r.URL.Query().Get("digest")

	body := io.LimitReader(r.Body, uploadBodyLimit)

	if digest == "" {
		drainBody(body)
		s.uploads.Cancel(id)
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "digest query parameter required")
		return
	}

	if _, err := s.uploads.Append(id, body); err != nil {
		drainBody(body)
		s.uploads.Cancel(id)
		writeUploadAppendError(w, err)
		return
	}

	fu, err := s.uploads.Finalize(id)
	if err != nil {
		writeUploadAppendError(w, err)
		return
	}
	defer func() { _ = fu.Close() }()

	s.persistVerifiedBlob(w, r, name, digest, fu)
}

// persistMonolithicUpload streams a single-PUT blob straight to the digest key
// while hashing inline (no disk spool), then verifies. A mismatch discards the
// object: epoch's read path does not re-hash, so the digest key must only ever
// hold verified bytes.
func (s *Server) persistMonolithicUpload(w http.ResponseWriter, r *http.Request, name, digest string) {
	dgst := stripSHA256Prefix(digest)

	exists, err := s.reg.BlobExists(r.Context(), dgst)
	if err != nil {
		// fail closed: streaming on could overwrite then delete an existing blob.
		log.WithFunc("server.persistMonolithicUpload").Errorf(r.Context(), err, "blob exists check for sha256:%s failed", dgst)
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if exists {
		drainBody(r.Body) // keep the connection reusable
		s.blobCreated(w, name, digest)
		return
	}

	hasher := sha256.New()
	body := io.TeeReader(io.LimitReader(r.Body, uploadBodyLimit), hasher)
	if err := s.reg.PushBlobStreaming(r.Context(), dgst, body, r.ContentLength); err != nil {
		log.WithFunc("server.persistMonolithicUpload").Errorf(r.Context(), err, "stream blob sha256:%s (content-length=%d) failed", dgst, r.ContentLength)
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	if got := "sha256:" + hex.EncodeToString(hasher.Sum(nil)); got != digest {
		s.discardCorruptBlob(r.Context(), dgst)
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID",
			fmt.Sprintf("digest mismatch: got %s, expected %s", got, digest))
		return
	}
	s.blobCreated(w, name, digest)
}

// discardCorruptBlob deletes a digest-mismatched blob on a detached, bounded
// context: a delete suppressed by request cancellation would leave unverified
// bytes the dedup path later trusts. A failure is logged for a backstop sweep.
func (s *Server) discardCorruptBlob(ctx context.Context, digest string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discardBlobTimeout)
	defer cancel()
	if err := s.reg.DeleteBlob(ctx, digest); err != nil {
		log.WithFunc("server.discardCorruptBlob").Errorf(ctx, err,
			"delete corrupt blob sha256:%s failed; digest key holds unverified bytes the dedup path will trust", digest)
	}
}

// persistVerifiedBlob verifies the digest then streams to the object store.
// Used by the chunked PATCH upload path, where the full blob is spooled to
// disk first so the digest can be checked before it reaches the object store.
func (s *Server) persistVerifiedBlob(w http.ResponseWriter, r *http.Request, name, digest string, fu *FinalizedUpload) {
	if got := fu.Digest(); got != digest {
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID",
			fmt.Sprintf("digest mismatch: got %s, expected %s", got, digest))
		return
	}

	rdr, err := fu.Reader()
	if err != nil {
		log.WithFunc("server.persistVerifiedBlob").Errorf(r.Context(), err, "open spooled blob %s failed", digest)
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if err := s.reg.PushBlobFromStream(r.Context(), stripSHA256Prefix(digest), rdr, fu.Size()); err != nil {
		log.WithFunc("server.persistVerifiedBlob").Errorf(r.Context(), err, "push spooled blob %s (size=%d) failed", digest, fu.Size())
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	s.blobCreated(w, name, digest)
}

func (s *Server) blobCreated(w http.ResponseWriter, name, digest string) {
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

func uploadLocation(name, id string) string {
	return fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id)
}

func writeUploadAppendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUploadNotFound):
		v2Error(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", err.Error())
	case errors.Is(err, errUploadTooLarge):
		v2Error(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", err.Error())
	default:
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

func drainBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, uploadBodyLimit))
}
