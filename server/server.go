// Package server implements the Epoch HTTP server. Uses gorilla/mux because
// stdlib ServeMux rejects OCI Distribution route patterns.
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

const (
	defaultUploadSpoolDir = "/var/cache/epoch/uploads"
)

var _ http.ResponseWriter = (*responseWriter)(nil)

// Server is the Epoch HTTP server providing OCI Distribution and control plane APIs.
type Server struct {
	addr          string     // config
	registryToken string     // config — Bearer token for /v2/ (empty = no token required)
	sso           *SSOConfig // config — nil = UI auth disabled

	reg   *registry.Registry // resources
	store *store.Store       // resources

	router    *mux.Router     // runtime
	uploads   *uploadSessions // runtime
	uiHandler http.Handler    // runtime
}

// New creates a Server with routes, auth, and upload sessions configured.
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
		addr:          addr,
		registryToken: regToken,
		sso:           sso,
		reg:           reg,
		store:         st,
		router:        mux.NewRouter(),
		uploads:       newUploadSessions(resolveUploadDir(ctx)),
	}
	s.setupRoutes(ctx)
	return s
}

// ListenAndServe starts the HTTP server and blocks until ctx is canceled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	logger := log.WithFunc("server.ListenAndServe")

	logger.Info(ctx, "syncing catalog to MySQL...")
	if err := s.store.SyncFromCatalog(ctx, s.reg); err != nil {
		logger.Warnf(ctx, "initial sync failed (continuing): %v", err)
	} else {
		logger.Info(ctx, "initial sync complete")
	}

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

func (s *Server) setupRoutes(ctx context.Context) {
	v2 := s.router.PathPrefix("/v2").Subrouter()
	v2.StrictSlash(true)

	v2.HandleFunc("/", s.v2Check).Methods(http.MethodGet, http.MethodHead)
	v2.HandleFunc("/_catalog", s.v2Catalog).Methods(http.MethodGet, http.MethodHead)
	v2.HandleFunc("/token", s.v2Token).Methods(http.MethodGet, http.MethodPost)

	v2.HandleFunc("/{name:.+}/manifests/{reference}", s.v2GetManifest).Methods(http.MethodGet)
	v2.HandleFunc("/{name:.+}/manifests/{reference}", s.v2HeadManifest).Methods(http.MethodHead)
	v2.HandleFunc("/{name:.+}/manifests/{reference}", s.v2PutManifest).Methods(http.MethodPut)
	v2.HandleFunc("/{name:.+}/manifests/{reference}", s.v2DeleteManifest).Methods(http.MethodDelete)

	v2.HandleFunc("/{name:.+}/tags/list", s.v2TagsList).Methods(http.MethodGet)

	v2.HandleFunc("/{name:.+}/blobs/uploads/", s.v2InitBlobUpload).Methods(http.MethodPost)
	v2.HandleFunc("/{name:.+}/blobs/uploads/{uuid}", s.v2PatchBlobUpload).Methods(http.MethodPatch)
	v2.HandleFunc("/{name:.+}/blobs/uploads/{uuid}", s.v2CompleteBlobUpload).Methods(http.MethodPut)

	v2.HandleFunc("/{name:.+}/blobs/{digest}", s.v2GetBlob).Methods(http.MethodGet)
	v2.HandleFunc("/{name:.+}/blobs/{digest}", s.v2HeadBlob).Methods(http.MethodHead)
	v2.HandleFunc("/{name:.+}/blobs/{digest}", s.v2PutBlob).Methods(http.MethodPut)

	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/stats", s.apiStats).Methods(http.MethodGet)
	api.HandleFunc("/repositories", s.apiListRepositories).Methods(http.MethodGet)
	api.HandleFunc("/catalog/sync", s.apiSync).Methods(http.MethodPost)
	api.HandleFunc("/tokens", s.apiListTokens).Methods(http.MethodGet)
	api.HandleFunc("/tokens", s.apiCreateToken).Methods(http.MethodPost)
	api.HandleFunc("/tokens/{id:[0-9]+}", s.apiDeleteToken).Methods(http.MethodDelete)

	api.HandleFunc("/repositories/{name:.+}/tags/{tag}", s.apiGetTag).Methods(http.MethodGet)
	api.HandleFunc("/repositories/{name:.+}/tags/{tag}", s.apiDeleteTag).Methods(http.MethodDelete)
	api.HandleFunc("/repositories/{name:.+}/tags", s.apiListTags).Methods(http.MethodGet)
	api.HandleFunc("/repositories/{name:.+}", s.apiGetRepository).Methods(http.MethodGet)

	s.router.HandleFunc("/dl/{name:.+}", s.handleArtifactDownload).Methods(http.MethodGet)

	// SSO routes must be registered before the UI catchall.
	if s.sso != nil {
		s.setupAuthRoutes()
	}

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
		// must outlive canceled parent ctx for graceful shutdown
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

func (s *Server) withLogging(next http.Handler) http.Handler {
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

type responseWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader captures the status code before delegating to the wrapped writer.
func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

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
