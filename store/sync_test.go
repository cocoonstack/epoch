package store

import (
	"testing"

	"github.com/cocoonstack/epoch/manifest"
)

func TestAggregateBlobsDedupesWithinTagAndCountsAcrossTags(t *testing.T) {
	pending := []pendingTag{
		{
			repoName: "ubuntu",
			descriptors: []manifest.Descriptor{
				{Digest: "sha256:aa", Size: 10, MediaType: "application/vnd.oci.image.config.v1+json"},
				{Digest: "sha256:bb", Size: 20, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"},
				{Digest: "sha256:bb", Size: 20, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"}, // dup within tag
			},
		},
		{
			repoName: "ubuntu",
			descriptors: []manifest.Descriptor{
				{Digest: "sha256:bb", Size: 20, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"}, // shared across tags
				{Digest: "sha256:cc", Size: 30, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"},
			},
		},
		{
			repoName: "ubuntu",
			descriptors: []manifest.Descriptor{
				{Digest: "", Size: 0, MediaType: ""}, // skipped
			},
		},
	}

	got := aggregateBlobs(pending)

	if len(got) != 3 {
		t.Fatalf("got %d aggregates, want 3", len(got))
	}
	if r := got["aa"].refCount; r != 1 {
		t.Errorf("aa refCount = %d, want 1", r)
	}
	if r := got["bb"].refCount; r != 2 {
		t.Errorf("bb refCount = %d, want 2", r)
	}
	if r := got["cc"].refCount; r != 1 {
		t.Errorf("cc refCount = %d, want 1", r)
	}
	if got["bb"].size != 20 {
		t.Errorf("bb size = %d, want 20", got["bb"].size)
	}
}
