package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// TestDLRouteResolution verifies /dl/ URLs split into (name, ref) the way
// handleArtifactDownload expects. The handler-side legacy fallback is
// integration-tested separately.
func TestDLRouteResolution(t *testing.T) {
	tests := []struct {
		path        string
		wantName    string
		wantRef     string
		wantMatched bool
	}{
		{"/dl/win11", "win11", "", true},
		{"/dl/win11/latest", "win11", "latest", true},
		{"/dl/win11/sha256:abc", "win11", "sha256:abc", true},
		{"/dl/simular/win11", "simular", "win11", true},
		{"/dl/simular/win11/22h2-20260510", "simular/win11", "22h2-20260510", true},
		{"/dl/simular/win11/sha256:adafd938", "simular/win11", "sha256:adafd938", true},
		{"/dl/", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			gotName, gotRef, matched := resolveDLRoute(t, tt.path)
			if matched != tt.wantMatched {
				t.Fatalf("matched = %v, want %v", matched, tt.wantMatched)
			}
			if !matched {
				return
			}
			if gotName != tt.wantName {
				t.Errorf("name = %q, want %q", gotName, tt.wantName)
			}
			if gotRef != tt.wantRef {
				t.Errorf("ref = %q, want %q", gotRef, tt.wantRef)
			}
		})
	}
}

func resolveDLRoute(t *testing.T, path string) (name, ref string, matched bool) {
	t.Helper()
	r := mux.NewRouter()
	capture := func(w http.ResponseWriter, req *http.Request) {
		name = mux.Vars(req)["name"]
		ref = mux.Vars(req)["ref"]
		matched = true
	}
	r.HandleFunc("/dl/{name:.+}/{ref}", capture).Methods(http.MethodGet)
	r.HandleFunc("/dl/{name:.+}", capture).Methods(http.MethodGet)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(httptest.NewRecorder(), req)
	return name, ref, matched
}

// TestDLPublicPathPostMigration ensures /dl/{name}/{ref} bypasses auth.
func TestDLPublicPathPostMigration(t *testing.T) {
	paths := []string{
		"/dl/win11/latest",
		"/dl/simular/win11/22h2-20260510",
		"/dl/simular/win11/sha256:adafd938",
	}
	for _, p := range paths {
		if !isPublicPath(p) {
			t.Errorf("isPublicPath(%q) = false, want true", p)
		}
	}
}
