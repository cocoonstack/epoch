// Package server implements the Epoch HTTP server.
//
// It serves two APIs:
//   - /v2/ — OCI Distribution-shaped push/pull protocol (manifests + blob streaming via object storage)
//   - /api/ — Control plane API backed by MySQL
//
// Static frontend files are embedded and served at /.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/cocoonstack/epoch/registry"
	"github.com/cocoonstack/epoch/store"
	"github.com/cocoonstack/epoch/ui"
)

// Server is the Epoch HTTP server.
type Server struct {
	reg           *registry.Registry
	store         *store.Store
	addr          string
	mux           *http.ServeMux
	sso           *SSOConfig // nil = UI auth disabled
	registryToken string     // Bearer token for /v2/ (empty = no token required)
}

// New creates a new server.
func New(reg *registry.Registry, st *store.Store, addr string) *Server {
	sso := LoadSSOConfig()
	if sso != nil {
		log.Printf("[epoch] UI auth enabled (provider=%s client_id=%s)", sso.Provider, sso.ClientID)
	} else {
		log.Printf("[epoch] UI auth disabled")
	}
	regToken := os.Getenv("EPOCH_REGISTRY_TOKEN")
	if regToken != "" {
		log.Printf("[epoch] registry token auth enabled")
	}
	s := &Server{
		reg:           reg,
		store:         st,
		addr:          addr,
		mux:           http.NewServeMux(),
		sso:           sso,
		registryToken: regToken,
	}
	s.setupRoutes()
	if sso != nil {
		s.setupAuthRoutes()
	}
	return s
}

func (s *Server) setupRoutes() {
	// Registry V2 pull API.
	s.mux.HandleFunc("GET /v2/", s.v2Check)
	s.mux.HandleFunc("GET /v2/_catalog", s.v2Catalog)
	s.mux.HandleFunc("GET /v2/{name}/tags/list", s.v2TagsList)
	s.mux.HandleFunc("GET /v2/{name}/manifests/{reference}", s.v2GetManifest)
	s.mux.HandleFunc("HEAD /v2/{name}/manifests/{reference}", s.v2HeadManifest)
	s.mux.HandleFunc("GET /v2/{name}/blobs/{digest}", s.v2GetBlob)
	s.mux.HandleFunc("HEAD /v2/{name}/blobs/{digest}", s.v2HeadBlob)

	// Registry V2 push API.
	s.mux.HandleFunc("PUT /v2/{name}/blobs/{digest}", s.v2PutBlob)
	s.mux.HandleFunc("PUT /v2/{name}/manifests/{reference}", s.v2PutManifest)

	// Control plane API.
	s.mux.HandleFunc("GET /api/stats", s.apiStats)
	s.mux.HandleFunc("GET /api/repositories", s.apiListRepositories)
	s.mux.HandleFunc("GET /api/repositories/{name}", s.apiGetRepository)
	s.mux.HandleFunc("GET /api/repositories/{name}/tags", s.apiListTags)
	s.mux.HandleFunc("GET /api/repositories/{name}/tags/{tag}", s.apiGetTag)
	s.mux.HandleFunc("DELETE /api/repositories/{name}/tags/{tag}", s.apiDeleteTag)
	s.mux.HandleFunc("POST /api/sync", s.apiSync)

	// Token management (SSO-protected via withAuth middleware).
	s.mux.HandleFunc("GET /api/tokens", s.apiListTokens)
	s.mux.HandleFunc("POST /api/tokens", s.apiCreateToken)
	s.mux.HandleFunc("DELETE /api/tokens/{id}", s.apiDeleteToken)

	// Frontend.
	uiFS, err := fs.Sub(ui.FS, ".")
	if err != nil {
		log.Fatalf("ui embed: %v", err)
	}
	s.mux.Handle("GET /", http.FileServer(http.FS(uiFS)))
}

// ListenAndServe starts the server with initial sync and background sync.
func (s *Server) ListenAndServe() error {
	// Initial catalog sync.
	ctx := context.Background()
	log.Println("[epoch] syncing catalog to MySQL...")
	if err := s.store.SyncFromCatalog(ctx, s.reg); err != nil {
		log.Printf("[epoch] initial sync failed (continuing): %v", err)
	} else {
		log.Println("[epoch] initial sync complete")
	}

	// Background sync every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if err := s.store.SyncFromCatalog(ctx, s.reg); err != nil {
				log.Printf("[epoch] background sync: %v", err)
			}
		}
	}()

	handler := s.withLogging(s.withCORS(s.withAuth(s.mux)))
	log.Printf("[epoch] listening on %s", s.addr)
	srv := &http.Server{ //nolint:gosec // timeouts are handled by the reverse proxy in production
		Addr:    s.addr,
		Handler: handler,
	}
	return srv.ListenAndServe()
}

// --- Middleware ---

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, DELETE, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"marshal failed"}`, 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func v2Error(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"errors": []map[string]string{
			{"code": code, "message": msg},
		},
	})
}
