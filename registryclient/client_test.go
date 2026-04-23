package registryclient

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestClientV2RoundTrip exercises the OCI Distribution endpoints the client
// uses for push and pull. The handler asserts that requests carry the right
// auth header and content type, and the test verifies the responses are
// decoded as expected.
func TestClientV2RoundTrip(t *testing.T) {
	t.Parallel()

	const (
		repoName     = "demo"
		blobDigest   = "sha256:def"
		manifestType = "application/vnd.oci.image.manifest.v1+json"
		manifestBody = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`
	)

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
		case r.Method == http.MethodGet && r.URL.Path == "/v2/"+repoName+"/manifests/latest":
			w.Header().Set("Content-Type", manifestType)
			_, _ = io.WriteString(w, manifestBody)

		case r.Method == http.MethodPut && r.URL.Path == "/v2/"+repoName+"/manifests/v2":
			if ct := r.Header.Get("Content-Type"); ct != manifestType {
				fail("Content-Type = %q, want %q", ct, manifestType)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"schemaVersion":2`) {
				fail("unexpected manifest body: %s", string(body))
				return
			}
			w.WriteHeader(http.StatusCreated)

		case r.Method == http.MethodHead && r.URL.Path == "/v2/"+repoName+"/blobs/"+blobDigest:
			w.WriteHeader(http.StatusNotFound)

		case r.Method == http.MethodPut && r.URL.Path == "/v2/"+repoName+"/blobs/"+blobDigest:
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

		case r.Method == http.MethodGet && r.URL.Path == "/v2/"+repoName+"/blobs/"+blobDigest:
			_, _ = io.WriteString(w, "blob")

		default:
			fail("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, "token-123")
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	ctx := t.Context()

	data, ct, err := c.GetManifest(ctx, repoName, "latest")
	if err != nil {
		t.Fatalf("GetManifest error: %v", err)
	}
	if ct != manifestType {
		t.Errorf("Content-Type = %q, want %q", ct, manifestType)
	}
	if string(data) != manifestBody {
		t.Errorf("body = %q, want %q", data, manifestBody)
	}

	if err = c.PutManifest(ctx, repoName, "v2", []byte(manifestBody), manifestType); err != nil {
		t.Fatalf("PutManifest error: %v", err)
	}

	exists, err := c.BlobExists(ctx, repoName, blobDigest)
	if err != nil {
		t.Fatalf("BlobExists error: %v", err)
	}
	if exists {
		t.Fatalf("BlobExists = true, want false")
	}

	if err = c.PutBlob(ctx, repoName, blobDigest, strings.NewReader("blob"), 4); err != nil {
		t.Fatalf("PutBlob error: %v", err)
	}

	body, err := c.GetBlob(ctx, repoName, blobDigest)
	if err != nil {
		t.Fatalf("GetBlob error: %v", err)
	}
	defer func() { _ = body.Close() }()
	got, _ := io.ReadAll(body)
	if string(got) != "blob" {
		t.Fatalf("GetBlob body = %q, want blob", got)
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

// TestGetManifestNotFoundReturnsSentinel locks in the contract the
// cocoon-operator hibernation reconciler depends on: a 404 from the
// registry surfaces as ErrManifestNotFound so callers can tell
// "snapshot not pushed yet" apart from real transport or server
// failures, while other non-200 statuses continue to flow through
// statusError as descriptive errors.
func TestGetManifestNotFoundReturnsSentinel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	_, _, err = c.GetManifest(t.Context(), "demo", "missing")
	if err == nil {
		t.Fatalf("GetManifest on 404 must return an error")
	}
	if !errors.Is(err, ErrManifestNotFound) {
		t.Errorf("GetManifest 404 err = %v, want errors.Is(..., ErrManifestNotFound)", err)
	}
}

func TestDeleteManifestNotFoundIsSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if err := c.DeleteManifest(t.Context(), "demo", "gone"); err != nil {
		t.Errorf("DeleteManifest on 404 should succeed, got %v", err)
	}
}

func TestGetManifestServerErrorNotConfusedWithNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	_, _, err = c.GetManifest(t.Context(), "demo", "boom")
	if err == nil {
		t.Fatalf("GetManifest on 500 must return an error")
	}
	if errors.Is(err, ErrManifestNotFound) {
		t.Errorf("GetManifest 500 must NOT satisfy errors.Is(..., ErrManifestNotFound), got %v", err)
	}
}
