package server

import (
	"fmt"
	"net/http"
)

// GET /v2/
//
// The Distribution Spec uses this endpoint as the registry-discovery probe
// (clients call it `Ping`). For registries that require authentication, the
// spec says it should return 401 with a Bearer challenge so the client knows
// where to fetch a token from. go-containerregistry, crane, docker, and
// containerd cache that challenge for the lifetime of the connection and
// reuse it on every subsequent request — including pushes.
//
// Returning 200 when auth is configured is what most clients interpret as
// "anonymous registry, no challenge needed", and they then never bother
// running the bearer flow on a later 401. So we serve 401-with-challenge
// when no valid credential was presented and 200 once the client authenticates.
//
// Other GET /v2/* paths (manifests, blobs, tags/list, _catalog) remain
// publicly readable per the cocoon design — only this discovery endpoint
// gates on credentials.
func (s *Server) v2Check(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if s.v2WritesRequireAuth() && !s.validateBearer(r) {
		w.Header().Set("WWW-Authenticate", wwwAuthenticateChallenge(r))
		v2Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	writeJSON(w, 200, map[string]any{})
}

// GET /v2/_catalog
func (s *Server) v2Catalog(w http.ResponseWriter, r *http.Request) {
	cat, err := s.reg.GetCatalog(r.Context())
	if err != nil {
		v2Error(w, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	names := make([]string, 0, len(cat.Repositories))
	for name := range cat.Repositories {
		names = append(names, name)
	}
	writeJSON(w, 200, map[string]any{"repositories": names})
}

// GET /v2/{name}/tags/list
func (s *Server) v2TagsList(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tags, err := s.reg.ListTags(r.Context(), name)
	if err != nil {
		// Fallback: read tags from catalog.
		cat, catErr := s.reg.GetCatalog(r.Context())
		if catErr != nil {
			v2Error(w, 404, "NAME_UNKNOWN", fmt.Sprintf("repository %q not found", name))
			return
		}
		repo, ok := cat.Repositories[name]
		if !ok {
			v2Error(w, 404, "NAME_UNKNOWN", fmt.Sprintf("repository %q not found", name))
			return
		}
		tags = make([]string, 0, len(repo.Tags))
		for t := range repo.Tags {
			tags = append(tags, t)
		}
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, 200, map[string]any{"name": name, "tags": tags})
}
