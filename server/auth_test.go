package server

import (
	"os"
	"testing"

	"github.com/cocoonstack/cocoon-common/auth"
)

func TestLoadSSOConfigGoogle(t *testing.T) {
	t.Setenv("SSO_PROVIDER", "google")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	t.Setenv("GOOGLE_OAUTH_REDIRECT_URI", "http://localhost:8080/login/callback")
	t.Setenv("GOOGLE_OAUTH_HOSTED_DOMAIN", "example.com")
	t.Setenv("SSO_COOKIE_SECRET", "")

	cfg := LoadSSOConfig(t.Context())
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
	if len(cfg.HostedDomains) != 1 || cfg.HostedDomains[0] != "example.com" {
		t.Fatalf("HostedDomains = %v, want [example.com]", cfg.HostedDomains)
	}
}

func TestLoadSSOConfigGoogleMultiDomain(t *testing.T) {
	t.Setenv("SSO_PROVIDER", "google")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	t.Setenv("GOOGLE_OAUTH_REDIRECT_URI", "http://localhost:8080/login/callback")
	t.Setenv("GOOGLE_OAUTH_HOSTED_DOMAIN", "cmgs.me, gitup.me ,, ")
	t.Setenv("SSO_COOKIE_SECRET", "")

	cfg := LoadSSOConfig(t.Context())
	if cfg == nil {
		t.Fatalf("LoadSSOConfig returned nil")
	}
	want := []string{"cmgs.me", "gitup.me"}
	if len(cfg.HostedDomains) != len(want) {
		t.Fatalf("HostedDomains = %v, want %v", cfg.HostedDomains, want)
	}
	for i, d := range want {
		if cfg.HostedDomains[i] != d {
			t.Fatalf("HostedDomains[%d] = %q, want %q", i, cfg.HostedDomains[i], d)
		}
	}
}

func TestUserMatchesHostedDomain(t *testing.T) {
	allowed := []string{"cmgs.me", "gitup.me"}
	cases := []struct {
		userHD, email string
		want          bool
	}{
		{"cmgs.me", "alice@cmgs.me", true},
		{"", "bob@gitup.me", true},
		{"gitup.me", "carol@gitup.me", true},
		{"", "eve@gmail.com", false},
		{"gmail.com", "eve@gmail.com", false},
		{"", "ALICE@CMGS.ME", true},
	}
	for _, tc := range cases {
		got := userMatchesHostedDomain(tc.userHD, tc.email, allowed)
		if got != tc.want {
			t.Errorf("userMatchesHostedDomain(%q,%q,%v) = %v, want %v", tc.userHD, tc.email, allowed, got, tc.want)
		}
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

	if cfg := LoadSSOConfig(t.Context()); cfg != nil {
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

func TestSignAndVerifySessionRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	want := &auth.Session{
		User:  "Alice",
		Email: "alice@example.com",
		Exp:   1234567890,
	}

	signed, err := auth.SignSession(*want, key)
	if err != nil {
		t.Fatalf("SignSession: %v", err)
	}
	got, ok := auth.VerifySession(signed, key)
	if !ok {
		t.Fatalf("VerifySession returned false")
	}
	if got.User != want.User || got.Email != want.Email || got.Exp != want.Exp {
		t.Fatalf("VerifySession() = %+v, want %+v", got, want)
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
