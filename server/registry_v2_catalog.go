package server

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
)

func (s *Server) v2Check(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) v2Catalog(w http.ResponseWriter, r *http.Request) {
	cat, err := s.reg.GetCatalog(r.Context())
	if err != nil {
		v2Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": slices.Sorted(maps.Keys(cat.Repositories))})
}

func (s *Server) v2TagsList(w http.ResponseWriter, r *http.Request) {
	name := urlVar(r, "name")
	tags, err := s.reg.ListTags(r.Context(), name)
	if err != nil {
		cat, catErr := s.reg.GetCatalog(r.Context())
		if catErr != nil {
			v2Error(w, http.StatusNotFound, "NAME_UNKNOWN", fmt.Sprintf("repository %q not found", name))
			return
		}
		repo, ok := cat.Repositories[name]
		if !ok {
			v2Error(w, http.StatusNotFound, "NAME_UNKNOWN", fmt.Sprintf("repository %q not found", name))
			return
		}
		tags = slices.Sorted(maps.Keys(repo.Tags))
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "tags": tags})
}
