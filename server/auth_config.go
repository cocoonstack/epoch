package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"

	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/utils"
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
	ClientSecret string //nolint:gosec // OAuth configuration schema field name
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
func LoadSSOConfig(ctx context.Context) *SSOConfig {
	logger := log.WithFunc("server.LoadSSOConfig")
	provider := strings.ToLower(utils.FirstNonEmpty(os.Getenv("SSO_PROVIDER"), detectProvider()))
	if provider == "" {
		return nil
	}

	cfg := loadProviderConfig(provider)
	if cfg == nil || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURI == "" || cfg.AuthorizeURL == "" || cfg.TokenURL == "" || cfg.UserInfoURL == "" {
		logger.Infof(ctx, "sso disabled: incomplete %s configuration", provider)
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
		logger.Info(ctx, "no SSO_COOKIE_SECRET set, generated random key")
	}
	cfg.CookieSecret = cookieKey
	return cfg
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
			AuthorizeURL: utils.FirstNonEmpty(os.Getenv("SSO_AUTHORIZE_URL"), defaultGoogleAuthorizeURL),
			TokenURL:     utils.FirstNonEmpty(os.Getenv("SSO_TOKEN_URL"), defaultGoogleTokenURL),
			UserInfoURL:  utils.FirstNonEmpty(os.Getenv("SSO_USERINFO_URL"), defaultGoogleUserInfoURL),
			LogoutURL:    os.Getenv("SSO_LOGOUT_URL"),
			Scopes:       utils.FirstNonEmpty(os.Getenv("SSO_SCOPES"), "openid profile email"),
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
			Scopes:       utils.FirstNonEmpty(os.Getenv("SSO_SCOPES"), "openid profile email"),
		}
	default:
		return nil
	}
}
