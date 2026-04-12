package server

import (
	"net/http"
	"slices"
	"strings"
)

var publicExactPaths = map[string]bool{
	"/healthz":        true,
	"/login":          true,
	"/login/callback": true,
	"/logout":         true,
	"/v2/token":       true,
}

var publicPathPrefixes = []string{"/dl/"}

func isPublicPath(path string) bool {
	if publicExactPaths[path] {
		return true
	}
	return slices.ContainsFunc(publicPathPrefixes, func(p string) bool {
		return strings.HasPrefix(path, p)
	})
}

func isV2WriteMethod(method string) bool {
	switch method {
	case http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

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
	w.Header().Set("WWW-Authenticate", wwwAuthenticateChallenge(r))
	v2Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "valid Bearer token required")
}

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
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
		writeError(w, http.StatusUnauthorized, "login required")
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) v2WritesRequireAuth() bool {
	return s.registryToken != "" || s.store != nil
}

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
