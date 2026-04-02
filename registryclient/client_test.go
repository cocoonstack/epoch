package registryclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestClientUsesSharedHTTPConfigAndEndpoints(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		authHeaders []string
		handlerErr  error
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()

		fail := func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			if handlerErr == nil {
				handlerErr = fmt.Errorf(format, args...)
			}
			w.WriteHeader(http.StatusInternalServerError)
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/repositories":
			_, _ = w.Write([]byte(`[{"name":"demo","tagCount":1,"totalSize":42}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/repositories/demo/tags/latest":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/demo/manifests/latest":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"name":"demo","tag":"latest","snapshotId":"sid","layers":[],"totalSize":0,"createdAt":"2026-04-02T00:00:00Z"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v2/demo/manifests/v2":
			if ct := r.Header.Get("Content-Type"); ct != manifestContentType {
				fail("Content-Type = %q, want %q", ct, manifestContentType)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"name":"demo"`) {
				fail("unexpected manifest body: %s", string(body))
				return
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodHead && r.URL.Path == "/v2/demo/blobs/sha256:abc":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/demo/blobs/sha256:def":
			if got, want := r.ContentLength, int64(4); got != want {
				fail("ContentLength = %d, want %d", got, want)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "blob" {
				fail("blob body = %q, want blob", string(body))
				return
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/demo/blobs/sha256:def":
			_, _ = w.Write([]byte("blob"))
		default:
			fail("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "token-123")

	var repos []map[string]any
	if err := c.GetJSON(context.Background(), "/repositories", &repos); err != nil {
		t.Fatalf("GetJSON error: %v", err)
	}
	if len(repos) != 1 || repos[0]["name"] != "demo" {
		t.Fatalf("unexpected repos: %+v", repos)
	}

	if err := c.Delete(context.Background(), "/repositories/demo/tags/latest"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	if _, err := c.GetManifest(context.Background(), "demo", "latest"); err != nil {
		t.Fatalf("GetManifest error: %v", err)
	}

	if err := c.PutManifestJSON(context.Background(), "demo", "v2", []byte(`{"name":"demo","tag":"v2"}`)); err != nil {
		t.Fatalf("PutManifestJSON error: %v", err)
	}

	exists, err := c.BlobExists(context.Background(), "demo", "abc")
	if err != nil {
		t.Fatalf("BlobExists error: %v", err)
	}
	if exists {
		t.Fatalf("BlobExists = true, want false")
	}

	if err := c.PutBlob(context.Background(), "demo", "def", strings.NewReader("blob"), 4); err != nil {
		t.Fatalf("PutBlob error: %v", err)
	}

	body, err := c.GetBlob(context.Background(), "demo", "def")
	if err != nil {
		t.Fatalf("GetBlob error: %v", err)
	}
	defer func() { _ = body.Close() }()
	data, _ := io.ReadAll(body)
	if string(data) != "blob" {
		t.Fatalf("GetBlob body = %q, want blob", string(data))
	}

	mu.Lock()
	headers := append([]string(nil), authHeaders...)
	handlerErrCopy := handlerErr
	mu.Unlock()
	for _, auth := range headers {
		if auth != "Bearer token-123" {
			t.Fatalf("unexpected auth header: %q", auth)
		}
	}
	if handlerErrCopy != nil {
		t.Fatalf("handler error: %v", handlerErrCopy)
	}
}

func TestGetManifestJSONReturnsExactBody(t *testing.T) {
	t.Parallel()

	var (
		mu         sync.Mutex
		handlerErr error
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/demo/manifests/latest" {
			mu.Lock()
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"schemaVersion":1,"name":"demo"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	data, err := c.GetManifestJSON(context.Background(), "demo", "latest")
	if err != nil {
		t.Fatalf("GetManifestJSON error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got["name"] != "demo" {
		t.Fatalf("unexpected manifest payload: %s", string(data))
	}
	mu.Lock()
	handlerErrCopy := handlerErr
	mu.Unlock()
	if handlerErrCopy != nil {
		t.Fatalf("handler error: %v", handlerErrCopy)
	}
}
