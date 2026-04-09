package server

import (
	"net/http"
	"strings"
)

// publicPathPrefixes lists path prefixes that bypass auth entirely. Cloud
// image downloads are public so vk-cocoon and other consumers can pull
// without holding a registry token.
var publicPathPrefixes = []string{
	"/login",
	"/logout",
	"/dl/",
	"/image/",
}

// isPublicPath returns true for paths that bypass authentication entirely.
func isPublicPath(path string) bool {
	if path == "/healthz" {
		return true
	}
	for _, p := range publicPathPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// isV2WriteMethod returns true for HTTP methods that mutate /v2/ state.
// GET and HEAD are reads — by policy they are public on /v2/ so OCI clients
// can pull without credentials. PUT/POST/PATCH/DELETE still require a Bearer
// token.
func isV2WriteMethod(method string) bool {
	switch method {
	case http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// withAuth protects routes that require authentication.
//
// Policy:
//   - Public paths (healthz, login, logout, /dl/, /image/) bypass auth.
//   - /v2/ reads (GET/HEAD) are public; writes require a Bearer token.
//   - Everything else (UI, /api/) accepts a Bearer token OR an SSO session.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if isPublicPath(path) {
			next.ServeHTTP(w, r)
			return
		}

		if strings.HasPrefix(path, "/v2/") {
			s.serveV2(w, r, next)
			return
		}

		s.serveUIOrAPI(w, r, next)
	})
}

// serveV2 handles auth for the /v2/ subtree. Reads pass through; writes go
// through the Bearer token check.
func (s *Server) serveV2(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if !isV2WriteMethod(r.Method) {
		next.ServeHTTP(w, r)
		return
	}
	if !s.v2WritesRequireAuth() {
		next.ServeHTTP(w, r)
		return
	}
	if s.validateBearer(r) {
		next.ServeHTTP(w, r)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="epoch"`)
	v2Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "valid Bearer token required")
}

// serveUIOrAPI handles auth for non-v2 paths. Accepts a Bearer token first
// (machine clients hitting /api/) and falls back to SSO sessions for browsers.
func (s *Server) serveUIOrAPI(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if s.validateBearer(r) {
		next.ServeHTTP(w, r)
		return
	}
	if s.sso == nil {
		next.ServeHTTP(w, r)
		return
	}
	if s.getSession(r) != nil {
		next.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusUnauthorized, "login required")
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// v2WritesRequireAuth returns true if any token source is configured. With
// no bootstrap token and no DB-managed token store, /v2/ writes are open
// (matches the old behavior for unauthenticated dev setups).
func (s *Server) v2WritesRequireAuth() bool {
	return s.registryToken != "" || s.store != nil
}

// validateBearer extracts and validates a Bearer token from the request.
// Returns false when no token was supplied or the token is unknown.
func (s *Server) validateBearer(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" || token == auth {
		return false
	}
	if s.registryToken != "" && token == s.registryToken {
		return true
	}
	if s.store != nil && s.store.ValidateToken(r.Context(), token) {
		return true
	}
	return false
}
