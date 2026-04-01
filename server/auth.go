// OIDC authentication for the Epoch web UI and control plane API.
//
// Protected: / (UI), /api/* (control plane)
// Unprotected: /v2/* (OCI registry — machine clients), /healthz
//
// Flow:
//  1. Browser hits protected route → no session cookie → redirect /login
//  2. /login → redirect to the configured authorize endpoint
//  3. /login/callback → exchange code for token → set session cookie
//  4. /logout → clear cookie → redirect to provider logout when configured
//
// Config via environment variables:
//
//	SSO_PROVIDER=google|oidc
//	For generic OIDC:
//	  SSO_CLIENT_ID, SSO_CLIENT_SECRET, SSO_REDIRECT_URI
//	  SSO_AUTHORIZE_URL, SSO_TOKEN_URL, SSO_USERINFO_URL, SSO_LOGOUT_URL
//	For Google:
//	  GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET, GOOGLE_OAUTH_REDIRECT_URI
//	  GOOGLE_OAUTH_HOSTED_DOMAIN (optional)
//	SSO_COOKIE_SECRET (32-byte hex key for HMAC signing; auto-generated if empty)
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/internal/util"
)

const (
	defaultGoogleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultGoogleTokenURL     = "https://oauth2.googleapis.com/token" //nolint:gosec // not a credential, just a URL
	defaultGoogleUserInfoURL  = "https://openidconnect.googleapis.com/v1/userinfo"

	providerGoogle = "google"
)

// SSOConfig holds optional UI auth settings loaded from the environment.
type SSOConfig struct {
	Provider     string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	AuthorizeURL string
	TokenURL     string
	UserInfoURL  string
	LogoutURL    string
	Scopes       string
	HostedDomain string
	CookieSecret []byte
}

// LoadSSOConfig reads optional UI auth configuration from the environment.
func LoadSSOConfig() *SSOConfig {
	ctx := context.Background()
	provider := strings.ToLower(util.FirstNonEmpty(os.Getenv("SSO_PROVIDER"), detectProvider()))
	if provider == "" {
		return nil
	}

	cfg := loadProviderConfig(provider)
	if cfg == nil || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURI == "" || cfg.AuthorizeURL == "" || cfg.TokenURL == "" || cfg.UserInfoURL == "" {
		log.WithFunc("LoadSSOConfig").Infof(ctx, "[sso] disabled: incomplete %s configuration", provider)
		return nil
	}

	secret := os.Getenv("SSO_COOKIE_SECRET")
	var cookieKey []byte
	if secret != "" {
		cookieKey, _ = hex.DecodeString(secret)
	}
	if len(cookieKey) == 0 {
		cookieKey = make([]byte, 32)
		_, _ = rand.Read(cookieKey)
		log.WithFunc("LoadSSOConfig").Info(ctx, "[sso] no SSO_COOKIE_SECRET set, generated random key")
	}
	cfg.CookieSecret = cookieKey
	return cfg
}

// session is the data stored in the signed cookie.
type session struct {
	User  string `json:"u"`
	Email string `json:"e"`
	Exp   int64  `json:"x"` // unix timestamp
}

const (
	cookieName   = "epoch_session"
	cookieMaxAge = 86400 // 24h
)

// --- Routes ---

func (s *Server) setupAuthRoutes() {
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("GET /login/callback", s.handleCallback)
	s.mux.HandleFunc("GET /logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/me", s.handleMe)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.sso == nil {
		http.Error(w, "UI auth not configured", http.StatusNotImplemented)
		return
	}
	state := randomState()
	http.SetCookie(w, &http.Cookie{
		Name: "sso_state", Value: state,
		Path: "/", MaxAge: 300, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	params := url.Values{
		"client_id":     {s.sso.ClientID},
		"redirect_uri":  {s.sso.RedirectURI},
		"response_type": {"code"},
		"scope":         {util.FirstNonEmpty(s.sso.Scopes, "openid profile email")},
		"state":         {state},
	}
	if s.sso.Provider == providerGoogle && s.sso.HostedDomain != "" {
		params.Set("hd", s.sso.HostedDomain)
	}
	http.Redirect(w, r, s.sso.AuthorizeURL+"?"+params.Encode(), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if s.sso == nil {
		http.Error(w, "UI auth not configured", http.StatusNotImplemented)
		return
	}
	// Verify state
	stateCookie, err := r.Cookie("sso_state")
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid state", http.StatusForbidden)
		return
	}
	// Clear state cookie
	http.SetCookie(w, &http.Cookie{Name: "sso_state", Path: "/", MaxAge: -1})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "no code", 400)
		return
	}

	// Exchange code for token
	tokenResp, err := http.PostForm(s.sso.TokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {s.sso.ClientID},
		"client_secret": {s.sso.ClientSecret},
		"redirect_uri":  {s.sso.RedirectURI},
		"code":          {code},
	})
	if err != nil {
		log.WithFunc("Server.handleCallback").Errorf(r.Context(), err, "[sso] token exchange failed")
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = tokenResp.Body.Close() }()
	body, _ := io.ReadAll(tokenResp.Body)
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(body, &tok); unmarshalErr != nil {
		log.WithFunc("Server.handleCallback").Errorf(r.Context(), unmarshalErr, "[sso] token response parse failed")
		http.Error(w, "invalid token response", http.StatusBadGateway)
		return
	}
	if tok.AccessToken == "" {
		log.WithFunc("Server.handleCallback").Warnf(r.Context(), "[sso] no access_token: %s", body)
		http.Error(w, "SSO login failed: "+tok.Error, http.StatusBadGateway)
		return
	}

	// Get user info
	userReq, _ := http.NewRequest("GET", s.sso.UserInfoURL, nil)
	userReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	userResp, err := http.DefaultClient.Do(userReq)
	if err != nil {
		http.Error(w, "userinfo failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = userResp.Body.Close() }()
	body, _ = io.ReadAll(userResp.Body)
	var user struct {
		Name         string `json:"name"`
		Email        string `json:"email"`
		HostedDomain string `json:"hd"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		log.WithFunc("Server.handleCallback").Errorf(r.Context(), err, "[sso] userinfo parse failed")
		http.Error(w, "invalid userinfo response", http.StatusBadGateway)
		return
	}
	if user.Name == "" {
		user.Name = user.Email
	}
	if s.sso.HostedDomain != "" {
		if user.HostedDomain != s.sso.HostedDomain && !strings.HasSuffix(strings.ToLower(user.Email), "@"+strings.ToLower(s.sso.HostedDomain)) {
			http.Error(w, "account is not in the allowed Google Workspace domain", http.StatusForbidden)
			return
		}
	}

	// Set session cookie
	sess := session{User: user.Name, Email: user.Email, Exp: time.Now().Unix() + cookieMaxAge}
	signed := signSession(sess, s.sso.CookieSecret)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: signed,
		Path: "/", MaxAge: cookieMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	log.WithFunc("Server.handleCallback").Infof(r.Context(), "[sso] login: %s <%s>", user.Name, user.Email)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1})
	if s.sso != nil && s.sso.LogoutURL != "" {
		http.Redirect(w, r, s.sso.LogoutURL, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(r)
	if sess == nil {
		writeJSON(w, 401, map[string]string{"error": "not logged in"})
		return
	}
	writeJSON(w, 200, map[string]string{"user": sess.User, "email": sess.Email})
}

// --- Middleware ---

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
					valid = s.store.ValidateToken(token)
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
				valid = s.store.ValidateToken(bearerToken)
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

// --- Session helpers ---

func (s *Server) getSession(r *http.Request) *session {
	if s.sso == nil {
		return nil
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	sess, ok := verifySession(c.Value, s.sso.CookieSecret)
	if !ok || sess.Exp < time.Now().Unix() {
		return nil
	}
	return sess
}

func signSession(sess session, key []byte) string {
	data, _ := json.Marshal(sess)
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

func verifySession(cookie string, key []byte) (*session, bool) {
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return nil, false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	var sess session
	if json.Unmarshal(data, &sess) != nil {
		return nil, false
	}
	return &sess, true
}

func randomState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func detectProvider() string {
	switch {
	case os.Getenv("GOOGLE_OAUTH_CLIENT_ID") != "":
		return providerGoogle
	case os.Getenv("SSO_CLIENT_ID") != "":
		return "oidc"
	default:
		return ""
	}
}

func loadProviderConfig(provider string) *SSOConfig {
	switch provider {
	case providerGoogle:
		return &SSOConfig{
			Provider:     providerGoogle,
			ClientID:     os.Getenv("GOOGLE_OAUTH_CLIENT_ID"),
			ClientSecret: os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"),
			RedirectURI:  os.Getenv("GOOGLE_OAUTH_REDIRECT_URI"),
			AuthorizeURL: util.FirstNonEmpty(os.Getenv("SSO_AUTHORIZE_URL"), defaultGoogleAuthorizeURL),
			TokenURL:     util.FirstNonEmpty(os.Getenv("SSO_TOKEN_URL"), defaultGoogleTokenURL),
			UserInfoURL:  util.FirstNonEmpty(os.Getenv("SSO_USERINFO_URL"), defaultGoogleUserInfoURL),
			LogoutURL:    os.Getenv("SSO_LOGOUT_URL"),
			Scopes:       util.FirstNonEmpty(os.Getenv("SSO_SCOPES"), "openid profile email"),
			HostedDomain: os.Getenv("GOOGLE_OAUTH_HOSTED_DOMAIN"),
		}
	case "oidc":
		return &SSOConfig{
			Provider:     "oidc",
			ClientID:     os.Getenv("SSO_CLIENT_ID"),
			ClientSecret: os.Getenv("SSO_CLIENT_SECRET"),
			RedirectURI:  os.Getenv("SSO_REDIRECT_URI"),
			AuthorizeURL: os.Getenv("SSO_AUTHORIZE_URL"),
			TokenURL:     os.Getenv("SSO_TOKEN_URL"),
			UserInfoURL:  os.Getenv("SSO_USERINFO_URL"),
			LogoutURL:    os.Getenv("SSO_LOGOUT_URL"),
			Scopes:       util.FirstNonEmpty(os.Getenv("SSO_SCOPES"), "openid profile email"),
		}
	default:
		return nil
	}
}
