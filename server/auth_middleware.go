package server

import (
	"net/http"
	"strings"
)

// withAuth protects routes that require login.
// /v2/ requires Bearer token (machine clients). UI/API require SSO session.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Always allow: health, login/logout
		if path == "/healthz" || strings.HasPrefix(path, "/login") || strings.HasPrefix(path, "/logout") {
			next.ServeHTTP(w, r)
			return
		}

		// /v2/ — Bearer token auth for machine clients
		if strings.HasPrefix(path, "/v2/") { //nolint:nestif // auth middleware has inherent branching
			if s.registryToken != "" || s.store != nil {
				auth := r.Header.Get("Authorization")
				token := strings.TrimPrefix(auth, "Bearer ")
				valid := false
				// Check bootstrap token (env var)
				if s.registryToken != "" && token == s.registryToken {
					valid = true
				}
				// Check DB-managed tokens
				if !valid && s.store != nil && token != "" && token != auth {
					valid = s.store.ValidateToken(r.Context(), token)
				}
				if !valid {
					w.Header().Set("WWW-Authenticate", `Bearer realm="epoch"`)
					v2Error(w, 401, "UNAUTHORIZED", "valid Bearer token required")
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		// API/UI — accept Bearer token OR SSO session
		auth := r.Header.Get("Authorization")
		bearerToken := strings.TrimPrefix(auth, "Bearer ")
		if bearerToken != auth && bearerToken != "" {
			// Bearer token provided — validate (same logic as /v2/)
			valid := s.registryToken != "" && bearerToken == s.registryToken
			if !valid && s.store != nil {
				valid = s.store.ValidateToken(r.Context(), bearerToken)
			}
			if valid {
				next.ServeHTTP(w, r)
				return
			}
		}

		// SSO session (skip if SSO not configured)
		if s.sso == nil {
			next.ServeHTTP(w, r)
			return
		}
		sess := s.getSession(r)
		if sess == nil {
			if strings.HasPrefix(path, "/api/") {
				writeError(w, 401, "login required")
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}
