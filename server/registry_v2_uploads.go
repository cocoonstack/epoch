package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// OCI blob upload session buffer.
//
// The OCI Distribution spec supports two push flows: monolithic (POST with
// digest+body) and chunked (POST → PATCH... → PUT). go-containerregistry and
// docker clients use the chunked flow. Chunks are buffered in memory and
// flushed to the object store in one Put on PUT completion.
//
// The buffer is bounded by the per-upload 20 GiB limit on PATCH/PUT bodies
// below. Sessions are identified by a UUID and evicted when the upload
// completes (or implicitly when the server restarts).
var (
	pendingUploads   = make(map[string][]byte)
	pendingUploadsMu sync.Mutex
)

// v2InitBlobUpload handles POST /v2/{name}/blobs/uploads/.
// If ?digest= is set, treats the body as a monolithic upload.
// Otherwise, starts a new chunked upload session.
func (s *Server) v2InitBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if digest := r.URL.Query().Get("digest"); digest != "" {
		s.completeBlobUpload(w, r, name, digest)
		return
	}

	uploadID := uuid.New().String()
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadID))
	w.Header().Set("Docker-Upload-UUID", uploadID)
	w.Header().Set("Range", "0-0")
	w.WriteHeader(http.StatusAccepted)
}

// v2PatchBlobUpload handles PATCH /v2/{name}/blobs/uploads/{uuid}.
// Appends chunk data to the in-memory buffer for the upload session.
func (s *Server) v2PatchBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	uploadID := r.PathValue("uuid")

	data, err := io.ReadAll(io.LimitReader(r.Body, 20<<30)) // 20 GiB per chunk
	if err != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	pendingUploadsMu.Lock()
	pendingUploads[uploadID] = append(pendingUploads[uploadID], data...)
	size := len(pendingUploads[uploadID])
	pendingUploadsMu.Unlock()

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadID))
	w.Header().Set("Docker-Upload-UUID", uploadID)
	w.Header().Set("Range", fmt.Sprintf("0-%d", size-1))
	w.WriteHeader(http.StatusAccepted)
}

// v2CompleteBlobUpload handles PUT /v2/{name}/blobs/uploads/{uuid}?digest=sha256:xxx.
// Concatenates any trailing body with the buffered chunks, verifies the digest,
// and persists the blob.
func (s *Server) v2CompleteBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	uploadID := r.PathValue("uuid")
	digest := r.URL.Query().Get("digest")

	if digest == "" {
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID", "digest query parameter required")
		return
	}

	finalData, err := io.ReadAll(io.LimitReader(r.Body, 20<<30))
	if err != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	pendingUploadsMu.Lock()
	data := append(pendingUploads[uploadID], finalData...)
	delete(pendingUploads, uploadID)
	pendingUploadsMu.Unlock()

	s.persistUploadedBlob(w, r, name, digest, data)
}

// completeBlobUpload handles the monolithic POST path (POST .../uploads/?digest=…)
// where the body is the entire blob.
func (s *Server) completeBlobUpload(w http.ResponseWriter, r *http.Request, name, digest string) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 20<<30))
	if err != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	s.persistUploadedBlob(w, r, name, digest, data)
}

// persistUploadedBlob verifies the digest against the body and stores the blob.
func (s *Server) persistUploadedBlob(w http.ResponseWriter, r *http.Request, name, digest string, data []byte) {
	sum := sha256.Sum256(data)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != digest {
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID",
			fmt.Sprintf("digest mismatch: got %s, expected %s", actual, digest))
		return
	}

	if err := s.reg.PushBlobFromReader(r.Context(), strings.TrimPrefix(digest, "sha256:"), data); err != nil {
		v2Error(w, http.StatusInternalServerError, "BLOB_UPLOAD_INVALID", err.Error())
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}
