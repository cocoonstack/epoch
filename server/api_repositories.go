package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/snapshot"
)

func (s *Server) apiStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) apiListRepositories(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepositories(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

func (s *Server) apiGetRepository(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	repo, err := s.store.GetRepository(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if repo == nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (s *Server) apiListTags(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	tags, err := s.store.ListTags(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

func (s *Server) apiGetTag(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	tag := urlVar(r, "tag")
	logger := log.WithFunc("server.apiGetTag")

	t, err := s.store.GetTag(r.Context(), name, tag)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	// Inline snapshot config for the UI.
	var snapshotConfig *manifest.SnapshotConfig
	if t.Kind == manifest.KindSnapshot.String() {
		cfg, fetchErr := s.loadSnapshotConfig(r.Context(), name, t.ManifestJSON)
		if fetchErr != nil {
			logger.Warnf(r.Context(), "fetch snapshot config for %s:%s: %v", name, tag, fetchErr)
		} else {
			snapshotConfig = cfg
		}
	}

	writeJSON(w, http.StatusOK, tagResponse(t, snapshotConfig))
}

func (s *Server) loadSnapshotConfig(ctx context.Context, name, manifestJSON string) (*manifest.SnapshotConfig, error) {
	m, err := manifest.Parse([]byte(manifestJSON))
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	dl := &registryDownloader{reg: s.reg}
	return snapshot.FetchSnapshotConfig(ctx, dl, name, m.Config)
}

func (s *Server) apiDeleteTag(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	tag := urlVar(r, "tag")

	if err := s.reg.DeleteManifest(r.Context(), name, tag); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.DeleteTag(r.Context(), name, tag)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiSync(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SyncFromCatalog(r.Context(), s.reg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "synced"})
}
