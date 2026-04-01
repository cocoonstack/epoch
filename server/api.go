package server

import (
	"encoding/json"
	"fmt"
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

	// Parse manifest JSON for the response.
	var manifest any
	_ = json.Unmarshal([]byte(t.ManifestJSON), &manifest)

	writeJSON(w, 200, map[string]any{
		"repoName":   t.RepoName,
		"tag":        t.Name,
		"digest":     t.Digest,
		"totalSize":  t.TotalSize,
		"layerCount": t.LayerCount,
		"pushedAt":   t.PushedAt,
		"syncedAt":   t.SyncedAt,
		"manifest":   manifest,
	})
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

// --- Token Management ---

// GET /api/tokens
func (s *Server) apiListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListTokens(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, tokens)
}

// POST /api/tokens — body: {"name":"my-token"}
func (s *Server) apiCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, 400, "name is required")
		return
	}
	createdBy := ""
	if s.sso != nil {
		if sess := s.getSession(r); sess != nil {
			createdBy = sess.User
		}
	}
	plaintext, err := s.store.CreateToken(r.Context(), req.Name, createdBy)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"token": plaintext, "name": req.Name})
}

// DELETE /api/tokens/{id}
func (s *Server) apiDeleteToken(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id <= 0 {
		writeError(w, 400, "invalid id")
		return
	}
	if err := s.store.DeleteToken(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}
