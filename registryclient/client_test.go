package registryclient

import (
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

	c := New(srv.URL, "token-123")
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
