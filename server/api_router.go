package server

import (
	"net/http"
	"strings"
)

const apiSubTags = "tags"

// apiRepoRoute holds parsed components of an /api/repositories/ path.
type apiRepoRoute struct {
	Name string // repository name, may contain slashes
	Sub  string // apiSubTags or ""
	Tag  string // tag name when Sub == apiSubTags and a tag is specified
}

// parseAPIRepoPath splits a path (everything after "/api/repositories/") into
// an apiRepoRoute. Like parseV2Path it scans from the tail so multi-segment
// repository names work correctly.
func parseAPIRepoPath(raw string) (apiRepoRoute, bool) {
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" {
		return apiRepoRoute{}, false
	}

	parts := strings.Split(raw, "/")
	n := len(parts)

	// .../tags/{tag}
	if n >= 3 && parts[n-2] == apiSubTags {
		name := strings.Join(parts[:n-2], "/")
		return apiRepoRoute{Name: name, Sub: apiSubTags, Tag: parts[n-1]}, name != ""
	}

	// .../tags  (list tags)
	if n >= 2 && parts[n-1] == apiSubTags {
		name := strings.Join(parts[:n-1], "/")
		return apiRepoRoute{Name: name, Sub: apiSubTags}, name != ""
	}

	// bare name (get repository)
	return apiRepoRoute{Name: raw}, true
}

func (s *Server) apiRepoDispatchGET(w http.ResponseWriter, r *http.Request) {
	route, ok := parseAPIRepoPath(r.PathValue("path"))
	if !ok {
		writeError(w, http.StatusNotFound, "invalid repository path")
		return
	}
	r.SetPathValue("name", route.Name)
	switch {
	case route.Sub == apiSubTags && route.Tag != "":
		r.SetPathValue("tag", route.Tag)
		s.apiGetTag(w, r)
	case route.Sub == apiSubTags:
		s.apiListTags(w, r)
	default:
		s.apiGetRepository(w, r)
	}
}

func (s *Server) apiRepoDispatchDELETE(w http.ResponseWriter, r *http.Request) {
	route, ok := parseAPIRepoPath(r.PathValue("path"))
	if !ok {
		writeError(w, http.StatusNotFound, "invalid repository path")
		return
	}
	if route.Sub != apiSubTags || route.Tag == "" {
		writeError(w, http.StatusMethodNotAllowed, "DELETE only supported for tags")
		return
	}
	r.SetPathValue("name", route.Name)
	r.SetPathValue("tag", route.Tag)
	s.apiDeleteTag(w, r)
}
