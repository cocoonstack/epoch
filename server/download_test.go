package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleImageOrUIDispatchToUI verifies that paths containing a dot are
// routed to the UI handler rather than the cloud image download path.
// We can only test the UI side here without spinning up a registry; the
// download branch is exercised by integration testing.
func TestHandleImageOrUIDispatchToUI(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "favicon", path: "favicon.ico"},
		{name: "stylesheet", path: "style.css"},
		{name: "image name with dot is unreachable via bare route", path: "ubuntu-22.04"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uiHit := false
			s := &Server{
				uiHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					uiHit = true
					w.WriteHeader(http.StatusOK)
				}),
			}
			req := httptest.NewRequest("GET", "/"+tt.path, nil)
			req.SetPathValue("name", tt.path)
			rec := httptest.NewRecorder()
			s.handleImageOrUI(rec, req)
			if !uiHit {
				t.Errorf("uiHandler was not called for %q", tt.path)
			}
		})
	}
}

// TestHandleImageOrUINoUIHandler ensures the no-UI fallback path returns 404
// for dot-containing paths (so a misconfigured server fails closed instead
// of falling through to the download path).
func TestHandleImageOrUINoUIHandler(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/foo.bar", nil)
	req.SetPathValue("name", "foo.bar")
	rec := httptest.NewRecorder()
	s.handleImageOrUI(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
