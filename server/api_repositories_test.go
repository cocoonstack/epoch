package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// TestAPIDeleteTagRejectsDigest: without the gate, a sha256: tag would
// reach DeleteManifest as a literal tag name and silently no-op.
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
