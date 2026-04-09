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
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/projecteru2/core/log"

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
	uploads       *uploadSessions
	uiHandler     http.Handler
}

// New creates a new server.
func New(ctx context.Context, reg *registry.Registry, st *store.Store, addr string) *Server {
	logger := log.WithFunc("server.New")
	sso := LoadSSOConfig(ctx)
	if sso != nil {
		logger.Infof(ctx, "UI auth enabled (provider=%s client_id=%s)", sso.Provider, sso.ClientID)
	} else {
		logger.Info(ctx, "UI auth disabled")
	}
	regToken := os.Getenv("EPOCH_REGISTRY_TOKEN")
	if regToken != "" {
		logger.Info(ctx, "registry token auth enabled")
	}
	s := &Server{
		reg:           reg,
		store:         st,
		addr:          addr,
		mux:           http.NewServeMux(),
		sso:           sso,
		registryToken: regToken,
		uploads:       newUploadSessions(),
	}
	s.setupRoutes(ctx)
	if sso != nil {
		s.setupAuthRoutes()
	}
	return s
}

// ListenAndServe starts the server with initial sync and background sync.
func (s *Server) ListenAndServe(ctx context.Context) error {
	logger := log.WithFunc("server.ListenAndServe")

	// Initial catalog sync.
	logger.Info(ctx, "syncing catalog to MySQL...")
	if err := s.store.SyncFromCatalog(ctx, s.reg); err != nil {
		logger.Warnf(ctx, "initial sync failed (continuing): %v", err)
	} else {
		logger.Info(ctx, "initial sync complete")
	}

	// Background sync every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.store.SyncFromCatalog(ctx, s.reg); err != nil {
					logger.Warnf(ctx, "background sync: %v", err)
				}
			}
		}
	}()

	handler := s.withLogging(s.withCORS(s.withAuth(s.mux)))
	srv := newHTTPServer(ctx, s.addr, handler)
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	logger.Infof(ctx, "listening on %s", ln.Addr())
	return serveOnListener(ctx, srv, ln)
}

func (s *Server) setupRoutes(ctx context.Context) {
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

	// OCI Distribution blob upload protocol (chunked + monolithic). Required
	// for go-containerregistry, docker, and buildah push clients that do not
	// know about epoch's PUT-by-digest shortcut.
	s.mux.HandleFunc("POST /v2/{name}/blobs/uploads/", s.v2InitBlobUpload)
	s.mux.HandleFunc("PATCH /v2/{name}/blobs/uploads/{uuid}", s.v2PatchBlobUpload)
	s.mux.HandleFunc("PUT /v2/{name}/blobs/uploads/{uuid}", s.v2CompleteBlobUpload)

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

	// Public cloud image download endpoints (auth-exempt).
	s.mux.HandleFunc("GET /dl/{name}", s.handleCloudImageDownload)
	s.mux.HandleFunc("GET /image/{name}", s.handleCloudImageDownload)

	// Frontend. The catch-all GET /{name} route serves cloud image downloads
	// at the top level (e.g. /win11) and falls through to the embedded UI
	// file server for paths that look like asset files (anything containing
	// a dot — see handleImageOrUI for the disambiguation rule).
	uiFS, err := fs.Sub(ui.FS, ".")
	if err != nil {
		log.WithFunc("server.setupRoutes").Fatalf(ctx, err, "embed ui filesystem: %v", err)
	}
	s.uiHandler = http.FileServer(http.FS(uiFS))
	s.mux.HandleFunc("GET /{name}", s.handleImageOrUI)
	s.mux.Handle("GET /", s.uiHandler)
}

func newHTTPServer(ctx context.Context, addr string, handler http.Handler) *http.Server {
	return &http.Server{ //nolint:gosec // timeouts are conservative for local and reverse-proxy deployments
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
}

func serveOnListener(ctx context.Context, srv *http.Server, ln net.Listener) error {
	defer func() { _ = ln.Close() }()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// --- Middleware ---

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		log.WithFunc("server.withLogging").Infof(r.Context(), "%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
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

var _ http.ResponseWriter = (*responseWriter)(nil)

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
	_, _ = w.Write(data) //nolint:gosec // marshaled JSON API response, not rendered as HTML
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
