package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsPublicPath(t *testing.T) {
	public := []string{
		"/healthz",
		"/login",
		"/login/callback",
		"/logout",
		"/dl/win11",
		"/image/ubuntu",
		// Bare cloud-image short form: GET /<name> with no dot.
		// handleImageOrUI dispatches these to handleCloudImageDownload.
		"/win11",
		"/ubuntu",
		"/healthcheck", // single segment, no dot — handler answers 404 if missing
	}
	private := []string{
		"/",
		"/v2/",
		"/v2/foo/manifests/latest",
		"/api/repositories",
		// Multi-segment paths still require auth.
		"/foo/bar",
		// Single segment WITH a dot is treated as a UI asset, not a bare image.
		"/favicon.ico",
		"/style.css",
		"/ubuntu-22.04",
	}
	for _, p := range public {
		if !isPublicPath(p) {
			t.Errorf("isPublicPath(%q) = false, want true", p)
		}
	}
	for _, p := range private {
		if isPublicPath(p) {
			t.Errorf("isPublicPath(%q) = true, want false", p)
		}
	}
}

func TestIsV2WriteMethod(t *testing.T) {
	writes := []string{http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete}
	reads := []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	for _, m := range writes {
		if !isV2WriteMethod(m) {
			t.Errorf("isV2WriteMethod(%q) = false, want true", m)
		}
	}
	for _, m := range reads {
		if isV2WriteMethod(m) {
			t.Errorf("isV2WriteMethod(%q) = true, want false", m)
		}
	}
}

// TestWithAuthV2ReadIsPublic asserts the new policy: GET/HEAD on /v2/ pass
// through even when a registry token is configured.
func TestWithAuthV2ReadIsPublic(t *testing.T) {
	hit := false
	s := &Server{registryToken: "secret"}
	h := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			hit = false
			req := httptest.NewRequest(method, "/v2/foo/manifests/latest", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if !hit {
				t.Errorf("%s /v2/ was blocked: status %d", method, rec.Code)
			}
		})
	}
}

// TestWithAuthV2WriteRequiresToken asserts that PUT/POST/PATCH/DELETE on /v2/
// reject anonymous requests when a token is configured.
func TestWithAuthV2WriteRequiresToken(t *testing.T) {
	s := &Server{registryToken: "secret"}
	h := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been reached")
	}))

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/v2/foo/manifests/latest", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got == "" {
				t.Error("WWW-Authenticate header not set")
			}
		})
	}
}

// TestWithAuthV2WriteAcceptsValidToken confirms that a valid Bearer token
// passes the write check.
func TestWithAuthV2WriteAcceptsValidToken(t *testing.T) {
	hit := false
	s := &Server{registryToken: "secret"}
	h := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))

	req := httptest.NewRequest(http.MethodPut, "/v2/foo/manifests/latest", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !hit {
		t.Errorf("valid token was rejected: status %d", rec.Code)
	}
}

// TestWithAuthV2WriteRejectsInvalidToken makes sure a wrong Bearer token is
// not accepted just because the header is present.
func TestWithAuthV2WriteRejectsInvalidToken(t *testing.T) {
	s := &Server{registryToken: "secret"}
	h := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been reached with wrong token")
	}))

	req := httptest.NewRequest(http.MethodPut, "/v2/foo/manifests/latest", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestWithAuthV2WriteOpenWhenNoTokenConfigured asserts the dev fallback:
// when neither EPOCH_REGISTRY_TOKEN nor a token store is set, /v2/ writes
// are open.
func TestWithAuthV2WriteOpenWhenNoTokenConfigured(t *testing.T) {
	hit := false
	s := &Server{} // no token, no store
	h := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))

	req := httptest.NewRequest(http.MethodPut, "/v2/foo/manifests/latest", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !hit {
		t.Errorf("anonymous PUT was blocked when no token configured: status %d", rec.Code)
	}
}

// TestWithAuthDownloadIsPublic ensures /dl/ and /image/ bypass auth even
// when a token is configured.
func TestWithAuthDownloadIsPublic(t *testing.T) {
	hit := 0
	s := &Server{registryToken: "secret"}
	h := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
	}))

	for _, path := range []string{"/dl/win11", "/image/ubuntu"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
	if hit != 2 {
		t.Errorf("download routes blocked, hit = %d, want 2", hit)
	}
}
