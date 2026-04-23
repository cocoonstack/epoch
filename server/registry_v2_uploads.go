package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

const (
	uploadBodyLimit = defaultUploadMaxBytes
)

func (s *Server) v2InitBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")

	if digest := r.URL.Query().Get("digest"); digest != "" {
		s.persistMonolithicUpload(w, r, name, digest)
		return
	}

	id, err := s.uploads.Start()
	if err != nil {
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

func (s *Server) persistMonolithicUpload(w http.ResponseWriter, r *http.Request, name, digest string) {
	id, err := s.uploads.Start()
	if err != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	body := io.LimitReader(r.Body, uploadBodyLimit)
	if _, appendErr := s.uploads.Append(id, body); appendErr != nil {
		drainBody(body)
		s.uploads.Cancel(id)
		writeUploadAppendError(w, appendErr)
		return
	}
	fu, err := s.uploads.Finalize(id)
	if err != nil {
		drainBody(body)
		writeUploadAppendError(w, err)
		return
	}
	defer func() { _ = fu.Close() }()

	s.persistVerifiedBlob(w, r, name, digest, fu)
}

// persistVerifiedBlob verifies the digest then streams to the object store.
func (s *Server) persistVerifiedBlob(w http.ResponseWriter, r *http.Request, name, digest string, fu *FinalizedUpload) {
	rdr, err := fu.Reader()
	if err != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	h := sha256.New()
	if _, hashErr := io.Copy(h, rdr); hashErr != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "hash upload: "+hashErr.Error())
		return
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		v2Error(w, http.StatusBadRequest, "DIGEST_INVALID",
			fmt.Sprintf("digest mismatch: got %s, expected %s", got, digest))
		return
	}

	rdr, err = fu.Reader()
	if err != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if err := s.reg.PushBlobFromStream(r.Context(), stripSHA256Prefix(digest), rdr, fu.Size()); err != nil {
		v2Error(w, http.StatusInternalServerError, "BLOB_UPLOAD_INVALID", err.Error())
		return
	}

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
