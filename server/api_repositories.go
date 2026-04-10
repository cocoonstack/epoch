package server

import (
	"net/http"

	"github.com/cocoonstack/epoch/store"
)

// GET /api/stats
func (s *Server) apiStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// GET /api/repositories
func (s *Server) apiListRepositories(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepositories(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if repos == nil {
		repos = []store.Repository{}
	}
	writeJSON(w, http.StatusOK, repos)
}

// GET /api/repositories/{name}
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

// GET /api/repositories/{name}/tags
func (s *Server) apiListTags(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	tags, err := s.store.ListTags(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tags == nil {
		tags = []store.Tag{}
	}
	writeJSON(w, http.StatusOK, tags)
}

// GET /api/repositories/{name}/tags/{tag}
func (s *Server) apiGetTag(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	tag := urlVar(r, "tag")

	t, err := s.store.GetTag(r.Context(), name, tag)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	resp, err := tagResponse(t)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// DELETE /api/repositories/{name}/tags/{tag}
func (s *Server) apiDeleteTag(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	tag := urlVar(r, "tag")

	// Delete from object storage (source of truth).
	if err := s.reg.DeleteManifest(r.Context(), name, tag); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Delete from MySQL (best-effort; object storage is source of truth).
	_ = s.store.DeleteTag(r.Context(), name, tag)

	w.WriteHeader(http.StatusNoContent)
}

// POST /api/catalog/sync
func (s *Server) apiSync(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SyncFromCatalog(r.Context(), s.reg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "synced"})
}
