package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	cookieName   = "epoch_session"
	cookieMaxAge = 86400 // 24h
)

type session struct {
	User  string `json:"u"`
	Email string `json:"e"`
	Exp   int64  `json:"x"` // unix timestamp
}

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
