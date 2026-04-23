package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/cocoon-common/auth"

	"github.com/cocoonstack/epoch/utils"
)

const tokenExchangeBodyLimit = 1 << 20 // 1 MiB

func (s *Server) setupAuthRoutes() {
	s.router.HandleFunc("/login", s.handleLogin).Methods(http.MethodGet)
	s.router.HandleFunc("/login/callback", s.handleCallback).Methods(http.MethodGet)
	s.router.HandleFunc("/logout", s.handleLogout).Methods(http.MethodGet)
	s.router.HandleFunc("/auth/me", s.handleMe).Methods(http.MethodGet)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.sso == nil {
		http.Error(w, "UI auth not configured", http.StatusNotImplemented)
		return
	}
	state := auth.RandomState()
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

	stateCookie, stateCookieErr := r.Cookie("sso_state")
	if stateCookieErr != nil || stateCookie.Value == "" {
		if sess := s.getSession(r); sess != nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		http.Error(w, "invalid state", http.StatusForbidden)
		return
	}
	if stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid state", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "sso_state", Path: "/", MaxAge: -1})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "no code", http.StatusBadRequest)
		return
	}

	logger := log.WithFunc("server.handleCallback")
	ctx := r.Context()

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {s.sso.ClientID},
		"client_secret": {s.sso.ClientSecret},
		"redirect_uri":  {s.sso.RedirectURI},
		"code":          {code},
	}
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.sso.TokenURL, strings.NewReader(tokenForm.Encode()))
	if err != nil {
		logger.Error(ctx, err, "build token request")
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResp, err := http.DefaultClient.Do(tokenReq) //nolint:gosec // token URL comes from trusted SSO provider config
	if err != nil {
		logger.Error(ctx, err, "token exchange failed")
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = tokenResp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(tokenResp.Body, tokenExchangeBodyLimit))
	if err != nil {
		logger.Error(ctx, err, "token response read")
		http.Error(w, "invalid token response", http.StatusBadGateway)
		return
	}
	var tok struct {
		AccessToken string `json:"access_token"` //nolint:gosec // OAuth response schema field name
		Error       string `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(body, &tok); unmarshalErr != nil {
		logger.Error(ctx, unmarshalErr, "token response parse failed")
		http.Error(w, "invalid token response", http.StatusBadGateway)
		return
	}
	if tok.AccessToken == "" {
		logger.Warnf(ctx, "no access_token: %s", body)
		http.Error(w, "SSO login failed: "+tok.Error, http.StatusBadGateway)
		return
	}

	userReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.sso.UserInfoURL, nil)
	if err != nil {
		logger.Error(ctx, err, "build userinfo request")
		http.Error(w, "userinfo failed", http.StatusBadGateway)
		return
	}
	userReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	userResp, err := http.DefaultClient.Do(userReq) //nolint:gosec // user info endpoint comes from trusted SSO provider config
	if err != nil {
		logger.Error(ctx, err, "userinfo fetch failed")
		http.Error(w, "userinfo failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = userResp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(userResp.Body, tokenExchangeBodyLimit))
	if err != nil {
		logger.Error(ctx, err, "userinfo read")
		http.Error(w, "invalid userinfo response", http.StatusBadGateway)
		return
	}
	var user struct {
		Name         string `json:"name"`
		Email        string `json:"email"`
		HostedDomain string `json:"hd"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		logger.Error(ctx, err, "userinfo parse failed")
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

	sess := auth.Session{User: user.Name, Email: user.Email, Exp: time.Now().Unix() + cookieMaxAge}
	signed, err := auth.SignSession(sess, s.sso.CookieSecret)
	if err != nil {
		logger.Error(ctx, err, "sign session")
		http.Error(w, "sign session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: signed,
		Path: "/", MaxAge: cookieMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	logger.Infof(ctx, "sso login: %s <%s>", user.Name, user.Email)
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

func userMatchesHostedDomain(userHD, email string, allowed []string) bool {
	hd := strings.ToLower(userHD)
	emailLower := strings.ToLower(email)
	return slices.ContainsFunc(allowed, func(d string) bool {
		return (hd != "" && hd == d) || strings.HasSuffix(emailLower, "@"+d)
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(r)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not logged in"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": sess.User, "email": sess.Email})
}
