package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/utils"
)

func (s *Server) setupAuthRoutes() {
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("GET /login/callback", s.handleCallback)
	s.mux.HandleFunc("GET /logout", s.handleLogout)
	s.mux.HandleFunc("GET /auth/me", s.handleMe)
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
		"scope":         {utils.FirstNonEmpty(s.sso.Scopes, "openid profile email")},
		"state":         {state},
	}
	// Google's OAuth `hd` parameter takes a single domain. When the operator
	// configures multiple allowed domains, omit the hint entirely and rely on
	// the post-callback domain check to enforce membership; sending a joined
	// list would silently reject everyone.
	if s.sso.Provider == providerGoogle && len(s.sso.HostedDomains) == 1 {
		params.Set("hd", s.sso.HostedDomains[0])
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

	logger := log.WithFunc("server.handleCallback")

	// Exchange code for token
	tokenResp, err := http.PostForm(s.sso.TokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {s.sso.ClientID},
		"client_secret": {s.sso.ClientSecret},
		"redirect_uri":  {s.sso.RedirectURI},
		"code":          {code},
	})
	if err != nil {
		logger.Error(r.Context(), err, "token exchange failed")
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = tokenResp.Body.Close() }()
	body, _ := io.ReadAll(tokenResp.Body)
	var tok struct {
		AccessToken string `json:"access_token"` //nolint:gosec // OAuth response schema field name
		Error       string `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(body, &tok); unmarshalErr != nil {
		logger.Error(r.Context(), unmarshalErr, "token response parse failed")
		http.Error(w, "invalid token response", http.StatusBadGateway)
		return
	}
	if tok.AccessToken == "" {
		logger.Warnf(r.Context(), "no access_token: %s", body)
		http.Error(w, "SSO login failed: "+tok.Error, http.StatusBadGateway)
		return
	}

	// Get user info
	userReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, s.sso.UserInfoURL, nil)
	userReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	userResp, err := http.DefaultClient.Do(userReq) //nolint:gosec // user info endpoint comes from trusted SSO provider config
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
		logger.Error(r.Context(), err, "userinfo parse failed")
		http.Error(w, "invalid userinfo response", http.StatusBadGateway)
		return
	}
	if user.Name == "" {
		user.Name = user.Email
	}
	if len(s.sso.HostedDomains) > 0 && !userMatchesHostedDomain(user.HostedDomain, user.Email, s.sso.HostedDomains) {
		http.Error(w, "account is not in the allowed Google Workspace domain", http.StatusForbidden)
		return
	}

	// Set session cookie
	sess := session{User: user.Name, Email: user.Email, Exp: time.Now().Unix() + cookieMaxAge}
	signed := signSession(sess, s.sso.CookieSecret)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: signed,
		Path: "/", MaxAge: cookieMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	logger.Infof(r.Context(), "sso login: %s <%s>", user.Name, user.Email)
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

// userMatchesHostedDomain returns true if the OAuth-reported hosted domain or
// the email's domain part is in the configured allow-list. Comparisons are
// case-insensitive; allowed entries are assumed to already be normalized.
func userMatchesHostedDomain(userHD, email string, allowed []string) bool {
	hd := strings.ToLower(userHD)
	emailLower := strings.ToLower(email)
	for _, d := range allowed {
		if hd != "" && hd == d {
			return true
		}
		if strings.HasSuffix(emailLower, "@"+d) {
			return true
		}
	}
	return false
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(r)
	if sess == nil {
		writeJSON(w, 401, map[string]string{"error": "not logged in"})
		return
	}
	writeJSON(w, 200, map[string]string{"user": sess.User, "email": sess.Email})
}
