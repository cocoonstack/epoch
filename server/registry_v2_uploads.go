package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// uploadBodyLimit caps any single PATCH/PUT body. The actual cap on a full
// upload is enforced by uploadSessions.maxBytes; this is just a per-request
// safety net so a single PATCH cannot read forever. Kept aligned with
// defaultUploadMaxBytes so the per-request and per-session limits agree.
const uploadBodyLimit = defaultUploadMaxBytes

// v2InitBlobUpload handles `POST /v2/{name}/blobs/uploads/`.
//
// If the request includes `?digest=sha256:xxx`, the body is treated as a
// monolithic upload (POST contains the entire blob). Otherwise a new chunked
// upload session is started and the client is told where to PATCH.
func (s *Server) v2InitBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")

	if digest := r.URL.Query().Get("digest"); digest != "" {
		s.persistMonolithicUpload(w, r, name, digest)
		return
	}

	id := s.uploads.Start()
	w.Header().Set("Location", uploadLocation(name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	w.Header().Set("Range", "0-0")
	w.WriteHeader(http.StatusAccepted)
}

// v2PatchBlobUpload handles `PATCH /v2/{name}/blobs/uploads/{uuid}` — append
// a chunk to an in-progress upload session.
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
	w.Header().Set("Range", "0-"+strconv.Itoa(size-1))
	w.WriteHeader(http.StatusAccepted)
}

// v2CompleteBlobUpload handles `PUT /v2/{name}/blobs/uploads/{uuid}?digest=sha256:xxx`.
// Any trailing body is appended, the assembled buffer is digest-verified, and
// the blob is persisted. The session is removed on every error path so an
// abandoned or failed upload does not pin memory.
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

	data, err := s.uploads.Finalize(id)
	if err != nil {
		drainBody(body)
		writeUploadAppendError(w, err)
		return
	}

	s.persistVerifiedBlob(w, r, name, digest, data)
}

// persistMonolithicUpload handles the single-POST flow where the entire blob
// arrives in the body of `POST .../uploads/?digest=...`. No session state is
// involved.
func (s *Server) persistMonolithicUpload(w http.ResponseWriter, r *http.Request, name, digest string) {
	body := io.LimitReader(r.Body, uploadBodyLimit)
	data, err := io.ReadAll(body)
	if err != nil {
		drainBody(body)
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	s.persistVerifiedBlob(w, r, name, digest, data)
}

// persistVerifiedBlob is the shared finalize step for both push flows.
// Verifies the digest matches the bytes, then writes through to the registry.
func (s *Server) persistVerifiedBlob(w http.ResponseWriter, r *http.Request, name, digest string, data []byte) {
	sum := sha256.Sum256(data)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if got != digest {
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID",
			fmt.Sprintf("digest mismatch: got %s, expected %s", got, digest))
		return
	}

	if err := s.reg.PushBlobFromStream(r.Context(), stripSHA256Prefix(digest), bytes.NewReader(data), int64(len(data))); err != nil {
		v2Error(w, http.StatusInternalServerError, "BLOB_UPLOAD_INVALID", err.Error())
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

// uploadLocation builds the Location header value for an in-progress upload.
func uploadLocation(name, id string) string {
	return fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id)
}

// writeUploadAppendError translates upload-session errors to OCI v2 responses.
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

// drainBody discards up to uploadBodyLimit bytes from body so the underlying
// TCP connection can be reused (HTTP keep-alive). The cap is required: a
// malicious client could otherwise stream unbounded bytes on an error path
// and pin a handler goroutine. Callers pass the already-LimitReader-wrapped
// body so a misconfigured limit cannot accidentally bypass the cap.
func drainBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, uploadBodyLimit))
}
