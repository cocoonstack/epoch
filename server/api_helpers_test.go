package server

import (
	"net/http/httptest"
	"testing"

	"github.com/cocoonstack/epoch/store"
)

func TestTagResponseParsesManifestJSON(t *testing.T) {
	got, err := tagResponse(&store.Tag{
		RepoName:     "demo",
		Name:         "latest",
		Digest:       "sha256:abc",
		TotalSize:    123,
		LayerCount:   2,
		ManifestJSON: `{"name":"demo","layers":[1,2]}`,
	})
	if err != nil {
		t.Fatalf("tagResponse error: %v", err)
	}
	if got["repoName"] != "demo" || got["tag"] != "latest" {
		t.Fatalf("unexpected tag response: %#v", got)
	}
	manifest, ok := got["manifest"].(map[string]any)
	if !ok || manifest["name"] != "demo" {
		t.Fatalf("manifest not decoded: %#v", got["manifest"])
	}
}

func TestParsePositivePathID(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/tokens/42", nil)
	req.SetPathValue("id", "42")
	if got, err := parsePositivePathID(req, "id"); err != nil || got != 42 {
		t.Fatalf("parsePositivePathID = (%d, %v), want (42, nil)", got, err)
	}

	req.SetPathValue("id", "0")
	if _, err := parsePositivePathID(req, "id"); err == nil {
		t.Fatalf("expected invalid id error")
	}

	req.SetPathValue("id", "42abc")
	if _, err := parsePositivePathID(req, "id"); err == nil {
		t.Fatalf("expected invalid id error for mixed input")
	}
}
