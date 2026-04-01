package server

import (
	"os"
	"testing"
)

func TestLoadSSOConfigGoogle(t *testing.T) {
	t.Setenv("SSO_PROVIDER", "google")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	t.Setenv("GOOGLE_OAUTH_REDIRECT_URI", "http://localhost:8080/login/callback")
	t.Setenv("GOOGLE_OAUTH_HOSTED_DOMAIN", "example.com")
	t.Setenv("SSO_COOKIE_SECRET", "")

	cfg := LoadSSOConfig()
	if cfg == nil {
		t.Fatalf("LoadSSOConfig returned nil")
	}
	if cfg.Provider != "google" {
		t.Fatalf("Provider = %q, want google", cfg.Provider)
	}
	if cfg.AuthorizeURL != defaultGoogleAuthorizeURL {
		t.Fatalf("AuthorizeURL = %q, want %q", cfg.AuthorizeURL, defaultGoogleAuthorizeURL)
	}
	if len(cfg.CookieSecret) != 32 {
		t.Fatalf("CookieSecret length = %d, want 32", len(cfg.CookieSecret))
	}
	if cfg.HostedDomain != "example.com" {
		t.Fatalf("HostedDomain = %q, want example.com", cfg.HostedDomain)
	}
}

func TestLoadSSOConfigOIDCDisabledWhenIncomplete(t *testing.T) {
	t.Setenv("SSO_PROVIDER", "oidc")
	t.Setenv("SSO_CLIENT_ID", "client-id")
	t.Setenv("SSO_CLIENT_SECRET", "")
	t.Setenv("SSO_REDIRECT_URI", "http://localhost:8080/login/callback")
	t.Setenv("SSO_AUTHORIZE_URL", "https://issuer.example/auth")
	t.Setenv("SSO_TOKEN_URL", "https://issuer.example/token")
	t.Setenv("SSO_USERINFO_URL", "https://issuer.example/userinfo")

	if cfg := LoadSSOConfig(); cfg != nil {
		t.Fatalf("LoadSSOConfig returned %+v, want nil", cfg)
	}
}

func TestDetectProvider(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	if got := detectProvider(); got != "google" {
		t.Fatalf("detectProvider() = %q, want google", got)
	}

	clearAuthEnv(t)
	t.Setenv("SSO_CLIENT_ID", "client-id")
	if got := detectProvider(); got != "oidc" {
		t.Fatalf("detectProvider() = %q, want oidc", got)
	}
}

func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SSO_PROVIDER",
		"GOOGLE_OAUTH_CLIENT_ID",
		"GOOGLE_OAUTH_CLIENT_SECRET",
		"GOOGLE_OAUTH_REDIRECT_URI",
		"GOOGLE_OAUTH_HOSTED_DOMAIN",
		"SSO_CLIENT_ID",
		"SSO_CLIENT_SECRET",
		"SSO_REDIRECT_URI",
		"SSO_AUTHORIZE_URL",
		"SSO_TOKEN_URL",
		"SSO_USERINFO_URL",
		"SSO_LOGOUT_URL",
		"SSO_COOKIE_SECRET",
	} {
		_ = os.Unsetenv(key)
	}
}
