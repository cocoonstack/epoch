package server

import (
	"net/http"

	"github.com/cocoonstack/cocoon-common/auth"
)

const (
	cookieName   = "epoch_session"
	cookieMaxAge = 86400 // 24h
)

func (s *Server) getSession(r *http.Request) *auth.Session {
	if s.sso == nil {
		return nil
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	sess, ok := auth.VerifySession(c.Value, s.sso.CookieSecret)
	if !ok {
		return nil
	}
	return sess
}
