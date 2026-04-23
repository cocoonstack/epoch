package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/cocoonstack/epoch/utils"
)

const manifestBodyLimit = 50 << 20

func (s *Server) loadManifestRaw(r *http.Request, name, ref string) ([]byte, error) {
	if isDigestRef(ref) {
		return s.reg.ManifestJSONByDigest(r.Context(), name, ref)
	}
	return s.reg.ManifestJSON(r.Context(), name, ref)
}

func detectManifestMediaType(data []byte) string {
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && probe.MediaType != "" {
		return probe.MediaType
	}
	return defaultManifestMediaType
}

func (s *Server) v2GetManifest(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	ref := urlVar(r, "reference")

	data, err := s.loadManifestRaw(r, name, ref)
	if err != nil {
		if isNotFound(err) {
			v2Error(w, http.StatusNotFound, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %s:%s not found", name, ref))
			return
		}
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	digest := utils.SHA256Hex(data)
	w.Header().Set("Content-Type", detectManifestMediaType(data))
	w.Header().Set("Docker-Content-Digest", "sha256:"+digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data) //nolint:gosec // raw registry manifest bytes, not HTML rendering
}

func (s *Server) v2HeadManifest(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	ref := urlVar(r, "reference")

	data, err := s.loadManifestRaw(r, name, ref)
	if err != nil {
		if isNotFound(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	digest := utils.SHA256Hex(data)
	w.Header().Set("Content-Type", detectManifestMediaType(data))
	w.Header().Set("Docker-Content-Digest", "sha256:"+digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) v2DeleteManifest(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	ref := urlVar(r, "reference")

	var err error
	if isDigestRef(ref) {
		err = s.reg.DeleteManifestByDigest(r.Context(), name, ref)
	} else {
		err = s.reg.DeleteManifest(r.Context(), name, ref)
	}
	if err != nil {
		if isNotFound(err) {
			v2Error(w, http.StatusNotFound, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %s:%s not found", name, ref))
			return
		}
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) v2PutManifest(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	ref := urlVar(r, "reference")

	data, err := io.ReadAll(io.LimitReader(r.Body, manifestBodyLimit))
	if err != nil {
		v2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", "read body: "+err.Error())
		return
	}
	if !json.Valid(data) {
		v2Error(w, http.StatusBadRequest, "MANIFEST_INVALID", "invalid JSON")
		return
	}

	digest := "sha256:" + utils.SHA256Hex(data)

	if isDigestRef(ref) {
		if ref != digest {
			v2Error(w, http.StatusBadRequest, "DIGEST_INVALID",
				fmt.Sprintf("manifest digest %s does not match reference %s", digest, ref))
			return
		}
		if _, err := s.reg.PushManifestJSONByDigest(r.Context(), name, data); err != nil {
			v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
			return
		}
	} else {
		if err := s.reg.PushManifestJSON(r.Context(), name, ref, data); err != nil {
			v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
			return
		}
	}

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, ref))
	w.WriteHeader(http.StatusCreated)
}
