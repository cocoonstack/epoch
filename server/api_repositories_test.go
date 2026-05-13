package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// TestAPIDeleteTagRejectsDigest verifies the control-plane DELETE endpoint
// refuses a digest-shaped {tag} value and tells the caller to use the v2
// delete-by-digest route. Without this gate the call falls through to
// DeleteManifest with the digest treated as a tag and silently no-ops.
func TestAPIDeleteTagRejectsDigest(t *testing.T) {
	s := &Server{}
	r := mux.NewRouter()
	r.HandleFunc("/api/repositories/{name:.+}/tags/{tag}", s.apiDeleteTag).Methods(http.MethodDelete)

	req := httptest.NewRequest(
		http.MethodDelete,
		"/api/repositories/cocoon/ubuntu/tags/sha256:0011223344",
		nil,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
