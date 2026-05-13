package server

// /dl/{name} download tests are integration tests that require a real
// registry backend (S3 + manifests + blobs). The handleArtifactDownload
// handler is exercised by integration testing against a live epoch
// instance — see cocoon-specs/tests/vk-epoch-pull-tests.md.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// fakeManifestStreamer records which lookup method was called so the
// registryDownloader tag-routing logic can be asserted.
type fakeManifestStreamer struct {
	tagBody     []byte
	digestBody  []byte
	tagCalls    int
	digestCalls int
}

func (f *fakeManifestStreamer) ManifestJSON(_ context.Context, _, _ string) ([]byte, error) {
	f.tagCalls++
	if f.tagBody == nil {
		return nil, errors.New("not found")
	}
	return f.tagBody, nil
}

func (f *fakeManifestStreamer) ManifestJSONByDigest(_ context.Context, _, _ string) ([]byte, error) {
	f.digestCalls++
	if f.digestBody == nil {
		return nil, errors.New("not found")
	}
	return f.digestBody, nil
}

func (f *fakeManifestStreamer) StreamBlob(_ context.Context, _ string) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(nil)), 0, nil
}

// TestRegistryDownloaderHonorsTag asserts that the precomputed cache
// returns ONLY when (name, tag) match. With the old behavior, the cached
// parent manifest was returned for any GetManifest call on the same
// name, including image-index child fetches that pass a sha256 digest.
func TestRegistryDownloaderHonorsTag(t *testing.T) {
	cached := []byte(`{"cached":true}`)
	tagBody := []byte(`{"fromTag":true}`)
	digestBody := []byte(`{"fromDigest":true}`)
	streamer := &fakeManifestStreamer{tagBody: tagBody, digestBody: digestBody}

	dl := &registryDownloader{
		reg:          streamer,
		manifestRaw:  cached,
		manifestName: "repo",
		manifestTag:  "latest",
	}

	// Same (name, tag) → cache hit, no DB call.
	got, _, err := dl.GetManifest(t.Context(), "repo", "latest")
	if err != nil {
		t.Fatalf("cache hit GetManifest: %v", err)
	}
	if !bytes.Equal(got, cached) {
		t.Errorf("cache hit body = %q, want %q", got, cached)
	}
	if streamer.tagCalls != 0 || streamer.digestCalls != 0 {
		t.Errorf("cache hit triggered registry call: tag=%d digest=%d", streamer.tagCalls, streamer.digestCalls)
	}

	// Different tag → goes to registry by tag.
	got, _, err = dl.GetManifest(t.Context(), "repo", "v2")
	if err != nil {
		t.Fatalf("tag fetch GetManifest: %v", err)
	}
	if !bytes.Equal(got, tagBody) {
		t.Errorf("tag fetch body = %q, want %q", got, tagBody)
	}
	if streamer.tagCalls != 1 || streamer.digestCalls != 0 {
		t.Errorf("tag fetch should hit ManifestJSON once: tag=%d digest=%d", streamer.tagCalls, streamer.digestCalls)
	}

	// Digest-shaped ref → goes to registry by digest.
	got, _, err = dl.GetManifest(t.Context(), "repo", "sha256:deadbeef")
	if err != nil {
		t.Fatalf("digest fetch GetManifest: %v", err)
	}
	if !bytes.Equal(got, digestBody) {
		t.Errorf("digest fetch body = %q, want %q", got, digestBody)
	}
	if streamer.tagCalls != 1 || streamer.digestCalls != 1 {
		t.Errorf("digest fetch should hit ManifestJSONByDigest: tag=%d digest=%d", streamer.tagCalls, streamer.digestCalls)
	}
}
