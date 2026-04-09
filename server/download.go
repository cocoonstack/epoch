package server

import (
	"fmt"
	"io"
	"net/http"

	"github.com/projecteru2/core/log"
)

// handleCloudImageDownload streams the concatenated layer blobs of `name:latest`
// as `application/octet-stream`. Used by vk-cocoon and other cloud-image
// consumers to fetch raw qcow2/raw images by name without OCI credentials.
//
// The handler reads the manifest as the legacy epoch format because that is
// what `epoch push` produces for cloud images today; OCI manifests are pushed
// via the OCI flow and resolved via the v2 manifest endpoint instead.
func (s *Server) handleCloudImageDownload(w http.ResponseWriter, r *http.Request) {
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

	// After WriteHeader we cannot turn an error into an HTTP status; the best
	// we can do is log and stop streaming so the client sees a truncated body.
	logger := log.WithFunc("server.handleCloudImageDownload")
	for _, layer := range m.Layers {
		body, _, blobErr := s.reg.StreamBlob(r.Context(), layer.Digest)
		if blobErr != nil {
			logger.Errorf(r.Context(), blobErr, "fetch blob %s for %s", layer.Digest, name)
			return
		}
		_, copyErr := io.Copy(w, body)
		_ = body.Close()
		if copyErr != nil {
			logger.Errorf(r.Context(), copyErr, "stream blob %s for %s", layer.Digest, name)
			return
		}
	}
}
