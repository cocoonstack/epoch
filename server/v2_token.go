package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// publicURLEnvVar is the optional override for the absolute URL clients use to
// reach the server. When set, the WWW-Authenticate realm and the token issuer
// route are anchored at this URL. When unset, both fall back to constructing
// the URL from the incoming request, which works as long as the operator has
// not put a CDN/proxy that rewrites paths in front of the registry.
const publicURLEnvVar = "EPOCH_PUBLIC_URL"

// tokenServiceName is the static `service` value advertised in the
// WWW-Authenticate header. OCI clients echo this back as a `service=` query
// parameter on their token request, but the issuer does not currently use it
// for anything beyond logging.
const tokenServiceName = "epoch"

// tokenLifetimeSeconds is the apparent lifetime returned in the token issuer
// response. The token itself is the same static value `EPOCH_REGISTRY_TOKEN`
// the operator configured, so this is purely advisory: clients refetch on
// expiry but always get the same token back.
const tokenLifetimeSeconds = 3600

// publicBaseURL returns the absolute base URL clients should use for token
// fetches. Preference order:
//  1. EPOCH_PUBLIC_URL env var (operator-configured, e.g. https://epoch.simular.cloud)
//  2. https://<request Host> when X-Forwarded-Proto says https or the request itself is TLS
//  3. http://<request Host> as a last resort
//
// The result has no trailing slash.
func publicBaseURL(r *http.Request) string {
	if env := strings.TrimRight(strings.TrimSpace(os.Getenv(publicURLEnvVar)), "/"); env != "" {
		return env
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		// Trust the first hop's proto when present (nginx in front of epoch
		// terminates TLS and forwards plaintext on the loopback).
		scheme = strings.ToLower(strings.TrimSpace(strings.SplitN(proto, ",", 2)[0]))
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.SplitN(h, ",", 2)[0]
		host = strings.TrimSpace(host)
	}
	return scheme + "://" + host
}

// wwwAuthenticateChallenge builds the Bearer challenge header value pointing
// at this server's own /v2/token endpoint. The realm MUST be an absolute URL
// (oras and docker fail with `unsupported protocol scheme ""` otherwise).
func wwwAuthenticateChallenge(r *http.Request) string {
	realm := publicBaseURL(r) + "/v2/token"
	return `Bearer realm="` + realm + `",service="` + tokenServiceName + `"`
}

// tokenResponse is the JSON shape OCI Distribution clients (oras, docker,
// containerd, go-containerregistry, buildah) expect from a token issuer.
// Both `token` and `access_token` are populated for compatibility — docker
// historically reads `token`, while OCI Distribution Spec §6 specifies
// `access_token`.
type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"` //nolint:tagliatelle // OCI Distribution spec field name
	ExpiresIn   int    `json:"expires_in"`   //nolint:tagliatelle // OCI Distribution spec field name
	IssuedAt    string `json:"issued_at"`    //nolint:tagliatelle // OCI Distribution spec field name
}

// v2Token is the token issuer endpoint advertised in WWW-Authenticate. It
// supports the three credential delivery methods OCI Distribution clients use
// in the wild and validates the credential against the same rules withAuth
// uses for direct Bearer requests:
//
//  1. HTTP Basic auth header (`Authorization: Basic ...`). docker/distribution
//     and crane use this for both GET and POST on the token endpoint.
//  2. Form-encoded `password` (or `refresh_token`) in a POST body. OCI
//     Distribution Spec §6.4 / RFC 6749 §4.3 password grant. oras uses this
//     for POST.
//  3. Query parameter `password` on GET. Some older docker clients only.
//
// Whichever delivery the client uses, the credential value is then matched
// against `EPOCH_REGISTRY_TOKEN` (the static env-configured token) OR a
// per-user token in the database-backed store. On success, the same token is
// echoed back inside the OCI token JSON response — there is no token exchange,
// the issuer's job here is purely to satisfy the challenge handshake.
//
// When no auth backend is configured at all (`v2WritesRequireAuth() == false`),
// the endpoint returns an empty token immediately so anonymous push clients
// can still complete the handshake and then succeed because the middleware
// lets them through unchecked.
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

// extractTokenCandidate pulls the credential value out of whichever delivery
// method the client used. Returns "" if no recognized credential was supplied.
// Order of precedence is Basic header > form body > query param so a client
// that supplies more than one consistent way still works.
func extractTokenCandidate(r *http.Request) string {
	if _, password, ok := r.BasicAuth(); ok && password != "" {
		return password
	}
	// ParseForm is safe to call even on GET; it merges query params into Form.
	// For POST with application/x-www-form-urlencoded it also reads the body.
	_ = r.ParseForm()
	if v := r.Form.Get("password"); v != "" {
		return v
	}
	if v := r.Form.Get("refresh_token"); v != "" {
		return v
	}
	return ""
}

// tokenIsValid mirrors validateBearer's allow rules but takes the candidate
// token directly so the basic-auth path can reuse the logic.
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

// writeTokenResponse emits the OCI token JSON. Issued at is in RFC 3339 with
// no fractional seconds — what the spec example uses.
func (s *Server) writeTokenResponse(w http.ResponseWriter, token string) {
	resp := tokenResponse{
		Token:       token,
		AccessToken: token,
		ExpiresIn:   tokenLifetimeSeconds,
		IssuedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp) //nolint:errcheck,gosec // best-effort write to client
}
