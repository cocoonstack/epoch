package server

import (
	"net/http"

	"github.com/cocoonstack/epoch/store"
)

// GET /api/stats
func (s *Server) apiStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, stats)
}

// GET /api/repositories
func (s *Server) apiListRepositories(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepositories(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if repos == nil {
		repos = []store.Repository{}
	}
	writeJSON(w, 200, repos)
}

// GET /api/repositories/{name}
func (s *Server) apiGetRepository(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	repo, err := s.store.GetRepository(r.Context(), name)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if repo == nil {
		writeError(w, 404, "repository not found")
		return
	}
	writeJSON(w, 200, repo)
}

// GET /api/repositories/{name}/tags
func (s *Server) apiListTags(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tags, err := s.store.ListTags(r.Context(), name)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if tags == nil {
		tags = []store.Tag{}
	}
	writeJSON(w, 200, tags)
}

// GET /api/repositories/{name}/tags/{tag}
func (s *Server) apiGetTag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tag := r.PathValue("tag")

	t, err := s.store.GetTag(r.Context(), name, tag)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if t == nil {
		writeError(w, 404, "tag not found")
		return
	}

	resp, err := tagResponse(t)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, resp)
}

// DELETE /api/repositories/{name}/tags/{tag}
func (s *Server) apiDeleteTag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tag := r.PathValue("tag")

	// Delete from object storage (source of truth).
	if err := s.reg.DeleteManifest(r.Context(), name, tag); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Delete from MySQL (best-effort; object storage is source of truth).
	_ = s.store.DeleteTag(r.Context(), name, tag)

	w.WriteHeader(204)
}

// POST /api/sync
func (s *Server) apiSync(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SyncFromCatalog(r.Context(), s.reg); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "synced"})
}
