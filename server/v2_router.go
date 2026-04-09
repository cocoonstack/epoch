package server

import (
	"net/http"
	"strings"
)

// OCI path action keywords used as route discriminators.
const (
	v2ActionManifests = "manifests"
	v2ActionBlobs     = "blobs"
	v2ActionTags      = "tags"
	v2ActionUploads   = "uploads"
)

// v2Route holds the parsed components of a /v2/ path with support for
// multi-segment OCI repository names (e.g. "library/nginx").
type v2Route struct {
	Name   string // repository name, may contain slashes
	Action string // v2Action* constant
	Param  string // reference, digest, uuid, or ""
}

// parseV2Path splits a path (everything after "/v2/") into a v2Route.
// It scans from the tail for known OCI action keywords so that
// multi-segment repository names are captured correctly.
func parseV2Path(raw string) (v2Route, bool) {
	trailingSlash := strings.HasSuffix(raw, "/")
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" {
		return v2Route{}, false
	}

	parts := strings.Split(raw, "/")
	n := len(parts)

	// .../blobs/uploads/{uuid}  (PATCH, PUT — complete chunked upload)
	if n >= 4 && parts[n-3] == v2ActionBlobs && parts[n-2] == v2ActionUploads {
		name := strings.Join(parts[:n-3], "/")
		return v2Route{Name: name, Action: v2ActionUploads, Param: parts[n-1]}, name != ""
	}

	// .../blobs/uploads/  (POST — init chunked upload; trailing slash required by OCI spec)
	if n >= 3 && parts[n-2] == v2ActionBlobs && parts[n-1] == v2ActionUploads {
		if !trailingSlash {
			return v2Route{}, false
		}
		name := strings.Join(parts[:n-2], "/")
		return v2Route{Name: name, Action: v2ActionUploads}, name != ""
	}

	// .../manifests/{reference}
	if n >= 3 && parts[n-2] == v2ActionManifests {
		name := strings.Join(parts[:n-2], "/")
		return v2Route{Name: name, Action: v2ActionManifests, Param: parts[n-1]}, name != ""
	}

	// .../blobs/{digest}
	if n >= 3 && parts[n-2] == v2ActionBlobs {
		name := strings.Join(parts[:n-2], "/")
		return v2Route{Name: name, Action: v2ActionBlobs, Param: parts[n-1]}, name != ""
	}

	// .../tags/list
	if n >= 3 && parts[n-2] == v2ActionTags && parts[n-1] == "list" {
		name := strings.Join(parts[:n-2], "/")
		return v2Route{Name: name, Action: v2ActionTags, Param: "list"}, name != ""
	}

	return v2Route{}, false
}

// setV2PathValues writes the parsed route fields into the request so
// downstream handlers can read them via r.PathValue.
func setV2PathValues(r *http.Request, route v2Route) {
	r.SetPathValue("name", route.Name)
	switch route.Action {
	case v2ActionManifests:
		r.SetPathValue("reference", route.Param)
	case v2ActionBlobs:
		r.SetPathValue("digest", route.Param)
	case v2ActionUploads:
		r.SetPathValue("uuid", route.Param)
	}
}

// v2Dispatch creates an http.HandlerFunc that parses the {path...} wildcard,
// looks up the action in the provided handler map, and dispatches accordingly.
//
// An empty `path` (request to bare `/v2/`) is the OCI Distribution "ping"
// endpoint and routes to v2Check regardless of method. The wildcard owns the
// ping registration so we can avoid a `/v2/{$}` rule that Go's ServeMux
// flags as conflicting with the wildcard pattern.
func (s *Server) v2Dispatch(handlers map[string]func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.PathValue("path")
		if raw == "" || raw == "/" {
			s.v2Check(w, r)
			return
		}
		route, ok := parseV2Path(raw)
		if !ok {
			v2Error(w, http.StatusNotFound, "NAME_UNKNOWN", "invalid repository path")
			return
		}
		setV2PathValues(r, route)
		h, found := handlers[route.Action]
		if !found {
			v2Error(w, http.StatusNotFound, "NAME_UNKNOWN", "unknown endpoint")
			return
		}
		h(w, r)
	}
}
