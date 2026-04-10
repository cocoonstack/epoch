// Package server implements the Epoch HTTP server.
//
// It serves two APIs:
//   - /v2/ — OCI Distribution-shaped push/pull protocol (manifests + blob streaming via object storage)
//   - /api/ — Control plane API backed by MySQL
//
// Static frontend files are embedded and served at /.
//
// Routing uses gorilla/mux because Go 1.22+ stdlib net/http.ServeMux's
// pattern-conflict checks reject the combination of exact-match GET routes
// (`GET /v2/_catalog`) and method-specific wildcards (`HEAD /v2/{path...}`)
// that the OCI Distribution shape needs.
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

	"github.com/gorilla/mux"
	"github.com/projecteru2/core/log"

	"github.com/cocoonstack/epoch/registry"
	"github.com/cocoonstack/epoch/store"
	"github.com/cocoonstack/epoch/ui"
)

// defaultUploadSpoolDir is where in-progress chunked OCI uploads are spooled
// when EPOCH_UPLOAD_DIR is not set. It must be backed by real disk — see
// resolveUploadDir for the rationale.
const defaultUploadSpoolDir = "/var/cache/epoch/uploads"

// Server is the Epoch HTTP server.
type Server struct {
	reg           *registry.Registry
	store         *store.Store
	addr          string
	router        *mux.Router
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
		router:        mux.NewRouter(),
		sso:           sso,
		registryToken: regToken,
		uploads:       newUploadSessions(resolveUploadDir(ctx)),
	}
	s.setupRoutes(ctx)
	return s
}

// resolveUploadDir picks the directory used to spool in-progress chunked OCI
// uploads. Order of preference:
//
//  1. $EPOCH_UPLOAD_DIR if set and creatable.
//  2. defaultUploadSpoolDir (/var/cache/epoch/uploads) if creatable.
//  3. os.TempDir() with a loud warning — operators should fix the deploy.
//
// The directory MUST be backed by real disk. The default os.TempDir() on
// systemd hosts is often a tmpfs that lives in RAM, which would defeat the
// disk-backed upload session refactor and OOM the host on multi-GiB pushes.
// We log the chosen directory so operators can sanity-check it at boot.
func resolveUploadDir(ctx context.Context) string {
	logger := log.WithFunc("server.resolveUploadDir")
	candidates := []string{os.Getenv("EPOCH_UPLOAD_DIR"), defaultUploadSpoolDir}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			logger.Warnf(ctx, "upload spool dir %q not usable, trying next: %v", dir, err)
			continue
		}
		logger.Infof(ctx, "upload spool dir: %s", dir)
		return dir
	}
	fallback := os.TempDir()
	logger.Warnf(ctx, "no usable upload spool dir; falling back to %s — this is often tmpfs (RAM-backed) and will OOM on multi-GiB pushes. Set EPOCH_UPLOAD_DIR to a real-disk path.", fallback)
	return fallback
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

	handler := s.withLogging(s.withCORS(s.withAuth(s.router)))
	srv := newHTTPServer(ctx, s.addr, handler)
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	logger.Infof(ctx, "listening on %s", ln.Addr())
	return serveOnListener(ctx, srv, ln)
}

func (s *Server) setupRoutes(ctx context.Context) {
	// Registry V2 — OCI Distribution endpoints. gorilla/mux's regex segment
	// patterns make multi-segment repository names like `library/nginx` work
	// without a hand-rolled dispatcher: `{name:.+}` is greedy across slashes
	// and the literal segments after it (`/manifests/`, `/blobs/`, `/tags/`)
	// anchor the match. Routes are registered most-specific first.
	v2 := s.router.PathPrefix("/v2").Subrouter()
	v2.StrictSlash(true)

	// OCI Distribution discovery + special endpoints (must come before the
	// wildcards so the literal segment matches).
	v2.HandleFunc("/", s.v2Check).Methods(http.MethodGet, http.MethodHead)
	v2.HandleFunc("/_catalog", s.v2Catalog).Methods(http.MethodGet, http.MethodHead)
	v2.HandleFunc("/token", s.v2Token).Methods(http.MethodGet, http.MethodPost)

	// Manifests by tag or digest.
	v2.HandleFunc("/{name:.+}/manifests/{reference}", s.v2GetManifest).Methods(http.MethodGet)
	v2.HandleFunc("/{name:.+}/manifests/{reference}", s.v2HeadManifest).Methods(http.MethodHead)
	v2.HandleFunc("/{name:.+}/manifests/{reference}", s.v2PutManifest).Methods(http.MethodPut)
	v2.HandleFunc("/{name:.+}/manifests/{reference}", s.v2DeleteManifest).Methods(http.MethodDelete)

	// Tags list.
	v2.HandleFunc("/{name:.+}/tags/list", s.v2TagsList).Methods(http.MethodGet)

	// Blob uploads — POST init / PATCH chunks / PUT complete. The trailing
	// slash on the init path is required by the OCI spec; clients that omit
	// it will not match this route.
	v2.HandleFunc("/{name:.+}/blobs/uploads/", s.v2InitBlobUpload).Methods(http.MethodPost)
	v2.HandleFunc("/{name:.+}/blobs/uploads/{uuid}", s.v2PatchBlobUpload).Methods(http.MethodPatch)
	v2.HandleFunc("/{name:.+}/blobs/uploads/{uuid}", s.v2CompleteBlobUpload).Methods(http.MethodPut)

	// Blob get / head / put-by-digest. The {digest} segment includes the
	// `sha256:` prefix because that is the OCI spec form; handlers strip it
	// before keying into the object store.
	v2.HandleFunc("/{name:.+}/blobs/{digest}", s.v2GetBlob).Methods(http.MethodGet)
	v2.HandleFunc("/{name:.+}/blobs/{digest}", s.v2HeadBlob).Methods(http.MethodHead)
	v2.HandleFunc("/{name:.+}/blobs/{digest}", s.v2PutBlob).Methods(http.MethodPut)

	// Control plane API.
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/stats", s.apiStats).Methods(http.MethodGet)
	api.HandleFunc("/repositories", s.apiListRepositories).Methods(http.MethodGet)
	api.HandleFunc("/catalog/sync", s.apiSync).Methods(http.MethodPost)
	api.HandleFunc("/tokens", s.apiListTokens).Methods(http.MethodGet)
	api.HandleFunc("/tokens", s.apiCreateToken).Methods(http.MethodPost)
	api.HandleFunc("/tokens/{id:[0-9]+}", s.apiDeleteToken).Methods(http.MethodDelete)

	// Control plane API — repository routes (multi-segment names).
	// Most-specific first.
	api.HandleFunc("/repositories/{name:.+}/tags/{tag}", s.apiGetTag).Methods(http.MethodGet)
	api.HandleFunc("/repositories/{name:.+}/tags/{tag}", s.apiDeleteTag).Methods(http.MethodDelete)
	api.HandleFunc("/repositories/{name:.+}/tags", s.apiListTags).Methods(http.MethodGet)
	api.HandleFunc("/repositories/{name:.+}", s.apiGetRepository).Methods(http.MethodGet)

	// Public artifact download (auth-exempt). {name:.+} matches multi-segment
	// repository names like windows/win11 so /dl/windows/win11 routes correctly.
	s.router.HandleFunc("/dl/{name:.+}", s.handleArtifactDownload).Methods(http.MethodGet)

	// SSO login / logout routes must be registered BEFORE the UI catchall
	// below; gorilla/mux matches routes in registration order, so a route
	// added after PathPrefix("/") would be shadowed and 404 via the file
	// server.
	if s.sso != nil {
		s.setupAuthRoutes()
	}

	// Frontend — embedded UI catches everything else under `/`. Must be
	// registered last so specific routes win.
	uiFS, err := fs.Sub(ui.FS, ".")
	if err != nil {
		log.WithFunc("server.setupRoutes").Fatalf(ctx, err, "embed ui filesystem: %v", err)
	}
	s.uiHandler = http.FileServer(http.FS(uiFS))
	s.router.PathPrefix("/").Handler(s.uiHandler).Methods(http.MethodGet)
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
	// Build the logger once at wire-up time so the per-request hot path does
	// not allocate a new Fields struct on every call.
	logger := log.WithFunc("server.withLogging")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		logger.Infof(r.Context(), "%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, DELETE, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
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
		http.Error(w, `{"error":"marshal failed"}`, http.StatusInternalServerError)
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
