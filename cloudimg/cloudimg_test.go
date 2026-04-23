package cloudimg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cocoonstack/epoch/manifest"
)

// fakeBlobs is a tiny in-memory BlobReader for tests.
type fakeBlobs map[string][]byte

func (f fakeBlobs) ReadBlob(_ context.Context, digest string) (io.ReadCloser, error) {
	data, ok := f[digest]
	if !ok {
		return nil, errors.New("blob not found: " + digest)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

const (
	diskBlobA = "AAAA"
	diskBlobB = "BBBB"
	// Real sha256 digests of the byte contents above; required by CopyBlobExact's digest check.
	digestA = "sha256:63c1dd951ffedf6f7fd968ad4efa39b8ed584f162f46e715114ee184f8de9201"
	digestB = "sha256:4a8d8134f29b0b7b60c126f5532bc9f5d9bb73037373cf6fb872d81f1dcefdfd"
)

var winManifest = `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.cocoonstack.os-image.v1+json",
  "config": {"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:00","size":2},
  "layers": [
    {
      "mediaType": "application/vnd.cocoonstack.disk.qcow2.part",
      "digest": "` + digestB + `",
      "size": 4,
      "annotations": {"org.opencontainers.image.title": "win.qcow2.01.qcow2.part"}
    },
    {
      "mediaType": "text/plain",
      "digest": "sha256:cc",
      "size": 32,
      "annotations": {"org.opencontainers.image.title": "SHA256SUMS"}
    },
    {
      "mediaType": "application/vnd.cocoonstack.disk.qcow2.part",
      "digest": "` + digestA + `",
      "size": 4,
      "annotations": {"org.opencontainers.image.title": "win.qcow2.00.qcow2.part"}
    }
  ]
}`

func TestStreamConcatenatesDiskLayersInTitleOrder(t *testing.T) {
	blobs := fakeBlobs{
		digestA:     []byte(diskBlobA),
		digestB:     []byte(diskBlobB),
		"sha256:cc": []byte("ignored-sha256sums"),
	}

	var out bytes.Buffer
	if err := Stream(t.Context(), []byte(winManifest), blobs, &out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got, want := out.String(), "AAAABBBB"; got != want {
		t.Errorf("Stream output = %q, want %q", got, want)
	}
}

func TestStreamRejectsContainerImage(t *testing.T) {
	containerManifest := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:00","size":1},
		"layers": [{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:11","size":1}]
	}`
	err := Stream(t.Context(), []byte(containerManifest), fakeBlobs{}, io.Discard)
	if err == nil {
		t.Fatal("expected error streaming container image")
	}
	if !strings.Contains(err.Error(), "not a cloud image") {
		t.Errorf("error = %v, want %q substring", err, "not a cloud image")
	}
}

func TestStreamRejectsManifestWithNoDiskLayers(t *testing.T) {
	noDisk := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"artifactType": "application/vnd.cocoonstack.os-image.v1+json",
		"config": {"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:00","size":2},
		"layers": [{"mediaType":"text/plain","digest":"sha256:11","size":1}]
	}`
	err := Stream(t.Context(), []byte(noDisk), fakeBlobs{}, io.Discard)
	if err == nil {
		t.Fatal("expected error for manifest with no disk layers")
	}
}

func TestDiskLayersFiltersAndSorts(t *testing.T) {
	in := []manifest.Descriptor{
		{MediaType: "text/plain", Annotations: map[string]string{manifest.AnnotationTitle: "SHA256SUMS"}},
		{MediaType: manifest.MediaTypeDiskQcow2Part, Annotations: map[string]string{manifest.AnnotationTitle: "x.02.part"}},
		{MediaType: manifest.MediaTypeDiskQcow2Part, Annotations: map[string]string{manifest.AnnotationTitle: "x.00.part"}},
		{MediaType: manifest.MediaTypeDiskQcow2Part, Annotations: map[string]string{manifest.AnnotationTitle: "x.01.part"}},
	}
	got := diskLayers(in)
	want := []string{"x.00.part", "x.01.part", "x.02.part"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Title() != w {
			t.Errorf("got[%d].Title() = %q, want %q", i, got[i].Title(), w)
		}
	}
}

// fakeCocoon captures the cocoon image import stdin payload so puller tests
// can assert what cocoon would have received.
type fakeCocoon struct {
	importPayload bytes.Buffer
	importName    string
}

func (f *fakeCocoon) ImageImport(_ context.Context, name string) (io.WriteCloser, func() error, error) {
	f.importName = name
	return nopCloser{w: &f.importPayload}, func() error { return nil }, nil
}

type nopCloser struct{ w io.Writer }

func (n nopCloser) Write(p []byte) (int, error) { return n.w.Write(p) }
func (n nopCloser) Close() error                { return nil }

// fakeDownloader implements snapshot.Downloader from a static manifest +
// blob map.
type fakeDownloader struct {
	manifest    []byte
	contentType string
	blobs       map[string][]byte
}

func (f *fakeDownloader) GetManifest(_ context.Context, _, _ string) ([]byte, string, error) {
	return f.manifest, f.contentType, nil
}

func (f *fakeDownloader) GetBlob(_ context.Context, _, digest string) (io.ReadCloser, error) {
	data, ok := f.blobs[digest]
	if !ok {
		return nil, errors.New("blob not found: " + digest)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestPullerPipesAssembledDiskToCocoonImport(t *testing.T) {
	dl := &fakeDownloader{
		manifest:    []byte(winManifest),
		contentType: manifest.MediaTypeOCIManifest,
		blobs: map[string][]byte{
			digestA:     []byte(diskBlobA),
			digestB:     []byte(diskBlobB),
			"sha256:cc": []byte("ignored"),
		},
	}
	cocoon := &fakeCocoon{}
	puller := &Puller{Downloader: dl, Cocoon: cocoon}

	if err := puller.Pull(t.Context(), PullOptions{
		Name:      "windows/win11",
		Tag:       "25h2",
		LocalName: "win11",
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if cocoon.importName != "win11" {
		t.Errorf("import name = %q, want win11", cocoon.importName)
	}
	if got, want := cocoon.importPayload.String(), "AAAABBBB"; got != want {
		t.Errorf("import payload = %q, want %q", got, want)
	}
}
