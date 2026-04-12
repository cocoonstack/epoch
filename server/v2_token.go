package server

import (
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	publicURLEnvVar      = "EPOCH_PUBLIC_URL"
	tokenServiceName     = "epoch"
	tokenLifetimeSeconds = 3600
)

func publicBaseURL(r *http.Request) string {
	if env := strings.TrimRight(strings.TrimSpace(os.Getenv(publicURLEnvVar)), "/"); env != "" {
		return env
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.ToLower(strings.TrimSpace(strings.SplitN(proto, ",", 2)[0]))
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.SplitN(h, ",", 2)[0]
		host = strings.TrimSpace(host)
	}
	return scheme + "://" + host
}

func wwwAuthenticateChallenge(r *http.Request) string {
	realm := publicBaseURL(r) + "/v2/token"
	return `Bearer realm="` + realm + `",service="` + tokenServiceName + `"`
}

type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"` //nolint:tagliatelle // OCI Distribution spec field name
	ExpiresIn   int    `json:"expires_in"`   //nolint:tagliatelle // OCI Distribution spec field name
	IssuedAt    string `json:"issued_at"`    //nolint:tagliatelle // OCI Distribution spec field name
}

// v2Token validates credentials and echoes the token back.
func (s *Server) v2Token(w http.ResponseWriter, r *http.Request) {
	if !s.v2WritesRequireAuth() {
		s.writeTokenResponse(w, "")
		return
	}

	candidate := extractTokenCandidate(r)
	if candidate == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="`+tokenServiceName+`"`)
		http.Error(w, "credentials required", http.StatusUnauthorized)
		return
	}

	if !s.tokenIsValid(r, candidate) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	s.writeTokenResponse(w, candidate)
}

func extractTokenCandidate(r *http.Request) string {
	if _, password, ok := r.BasicAuth(); ok && password != "" {
		return password
	}
	_ = r.ParseForm()
	if v := r.Form.Get("password"); v != "" {
		return v
	}
	if v := r.Form.Get("refresh_token"); v != "" {
		return v
	}
	return ""
}

func (s *Server) tokenIsValid(r *http.Request, candidate string) bool {
	if candidate == "" {
		return false
	}
	if s.registryToken != "" && candidate == s.registryToken {
		return true
	}
	if s.store != nil && s.store.ValidateToken(r.Context(), candidate) {
		return true
	}
	return false
}

func (s *Server) writeTokenResponse(w http.ResponseWriter, token string) {
	writeJSON(w, http.StatusOK, tokenResponse{
		Token:       token,
		AccessToken: token,
		ExpiresIn:   tokenLifetimeSeconds,
		IssuedAt:    time.Now().UTC().Format(time.RFC3339),
	})
}
