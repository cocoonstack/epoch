package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// handleDownload serves a cloud image by resolving its :latest manifest and
// streaming the concatenated layer blobs. It is used by vk-cocoon (and any
// cloud-image aware consumer) to fetch raw qcow2/raw images by name without
// needing OCI credentials.
//
// Routed at GET /dl/{name} and GET /image/{name} (both public, no auth).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	m, err := s.reg.PullManifest(r.Context(), name, "latest")
	if err != nil {
		if isNotFound(err) {
			http.Error(w, "image not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(m.Layers) == 0 {
		http.Error(w, "image has no layers", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	if m.TotalSize > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", m.TotalSize))
	}
	w.WriteHeader(http.StatusOK)

	// Stream all layers concatenated. Cloud images are typically a single
	// blob or a set of split parts; either way we emit them in order.
	// Errors after the 200 header cannot be surfaced to the client; log
	// via the access log and bail out.
	for _, layer := range m.Layers {
		body, _, err := s.reg.StreamBlob(r.Context(), layer.Digest)
		if err != nil {
			return
		}
		_, _ = io.Copy(w, body)
		_ = body.Close()
	}
}

// handleImageOrUI serves the top-level GET /{name} route. Cloud image
// consumers point at https://host/{image-name}; the embedded UI also owns
// the same namespace for asset files (e.g. /favicon.ico, /index.html).
// We disambiguate by filename extension: anything containing a "." is
// treated as a UI asset, everything else is a cloud image download.
func (s *Server) handleImageOrUI(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.Contains(name, ".") {
		if s.uiHandler != nil {
			s.uiHandler.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	s.handleDownload(w, r)
}
