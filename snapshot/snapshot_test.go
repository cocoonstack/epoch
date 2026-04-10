package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/cocoonstack/epoch/manifest"
)

// fakeUploader records every blob and manifest write so tests can assert
// the wire format produced by Pusher.
type fakeUploader struct {
	mu        sync.Mutex
	blobs     map[string][]byte // digest -> bytes
	manifests map[string]fakeManifestUpload
}

type fakeManifestUpload struct {
	bytes       []byte
	contentType string
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{
		blobs:     map[string][]byte{},
		manifests: map[string]fakeManifestUpload{},
	}
}

func (f *fakeUploader) BlobExists(_ context.Context, _, digest string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.blobs[digest]
	return ok, nil
}

func (f *fakeUploader) PutBlob(_ context.Context, _, digest string, body io.Reader, _ int64) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[digest] = data
	return nil
}

func (f *fakeUploader) PutManifest(_ context.Context, name, tag string, data []byte, contentType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifests[name+":"+tag] = fakeManifestUpload{bytes: data, contentType: contentType}
	return nil
}

func (f *fakeUploader) GetManifest(_ context.Context, name, tag string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.manifests[name+":"+tag]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return m.bytes, m.contentType, nil
}

func (f *fakeUploader) GetBlob(_ context.Context, _, digest string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.blobs[digest]
	if !ok {
		return nil, errors.New("blob not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// fakeCocoon serves a deterministic snapshot tar from Export and captures
// the bytes Import receives so tests can assert pull-side reassembly.
type fakeCocoon struct {
	exportTar     []byte
	importPayload bytes.Buffer
	importOpts    ImportOptions
}

func (f *fakeCocoon) Export(_ context.Context, _ string) (io.ReadCloser, func() error, error) {
	return io.NopCloser(bytes.NewReader(f.exportTar)), func() error { return nil }, nil
}

func (f *fakeCocoon) Import(_ context.Context, opts ImportOptions) (io.WriteCloser, func() error, error) {
	f.importOpts = opts
	return &nopWriteCloser{w: &f.importPayload}, func() error { return nil }, nil
}

type nopWriteCloser struct{ w io.Writer }

func (n *nopWriteCloser) Write(p []byte) (int, error) { return n.w.Write(p) }
func (n *nopWriteCloser) Close() error                { return nil }

// buildExportTar produces a fake `cocoon snapshot export` tar containing a
// snapshot.json envelope plus the named files.
func buildExportTar(t *testing.T, cfg snapshotExportConfig, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	envelope := snapshotExportEnvelope{Version: 1, Config: cfg}
	envBytes, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: snapshotJSONName, Size: int64(len(envBytes)), Mode: 0o644}); err != nil {
		t.Fatalf("write envelope header: %v", err)
	}
	if _, err := tw.Write(envBytes); err != nil {
		t.Fatalf("write envelope: %v", err)
	}

	// Stable order so the layer order in the produced manifest is testable.
	for _, name := range []string{"config.json", "state.json", "memory-ranges", "overlay.qcow2"} {
		data, ok := files[name]
		if !ok {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0o640}); err != nil {
			t.Fatalf("write %s header: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func TestPushProducesOCISnapshotManifest(t *testing.T) {
	files := map[string][]byte{
		"config.json":   []byte(`{"cpu":4}`),
		"state.json":    []byte(`{"state":"running"}`),
		"memory-ranges": []byte("memory bytes"),
		"overlay.qcow2": []byte("qcow2 bytes"),
	}
	cfg := snapshotExportConfig{
		ID:      "snap-id-1",
		Name:    "myvm",
		Image:   "ghcr.io/cocoonstack/cocoon/ubuntu:24.04",
		CPU:     4,
		Memory:  1 << 30,
		Storage: 10 << 30,
		NICs:    1,
	}

	uploader := newFakeUploader()
	cocoon := &fakeCocoon{exportTar: buildExportTar(t, cfg, files)}
	pusher := &Pusher{Uploader: uploader, Cocoon: cocoon}

	result, err := pusher.Push(context.Background(), PushOptions{
		Name:      "myvm",
		Tag:       "v1",
		BaseImage: "ghcr.io/cocoonstack/cocoon/ubuntu:24.04",
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Manifest was uploaded with the right key + content type.
	upload, ok := uploader.manifests["myvm:v1"]
	if !ok {
		t.Fatalf("manifest myvm:v1 not uploaded")
	}
	if upload.contentType != manifest.MediaTypeOCIManifest {
		t.Errorf("manifest content-type = %q, want %q", upload.contentType, manifest.MediaTypeOCIManifest)
	}
	if !bytes.Equal(result.ManifestBytes, upload.bytes) {
		t.Errorf("PushResult.ManifestBytes does not match what was uploaded")
	}

	// Parse the manifest the pusher built and assert its OCI shape.
	parsed, err := manifest.Parse(upload.bytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if parsed.ArtifactType != manifest.ArtifactTypeSnapshot {
		t.Errorf("artifactType = %q, want %q", parsed.ArtifactType, manifest.ArtifactTypeSnapshot)
	}
	if parsed.Config.MediaType != manifest.MediaTypeSnapshotConfig {
		t.Errorf("config mediaType = %q, want %q", parsed.Config.MediaType, manifest.MediaTypeSnapshotConfig)
	}
	if !strings.HasPrefix(parsed.Config.Digest, "sha256:") {
		t.Errorf("config digest %q lacks sha256: prefix", parsed.Config.Digest)
	}
	if parsed.Annotations[manifest.AnnotationSnapshotBaseImage] != "ghcr.io/cocoonstack/cocoon/ubuntu:24.04" {
		t.Errorf("baseimage annotation missing: %v", parsed.Annotations)
	}

	// Layers must include all four files in tar order with the right
	// mediaType and title annotation.
	wantLayers := []struct {
		title     string
		mediaType string
	}{
		{"config.json", manifest.MediaTypeVMConfig},
		{"state.json", manifest.MediaTypeVMState},
		{"memory-ranges", manifest.MediaTypeVMMemory},
		{"overlay.qcow2", manifest.MediaTypeDiskQcow2},
	}
	if len(parsed.Layers) != len(wantLayers) {
		t.Fatalf("layers len = %d, want %d", len(parsed.Layers), len(wantLayers))
	}
	for i, want := range wantLayers {
		got := parsed.Layers[i]
		if got.MediaType != want.mediaType {
			t.Errorf("layers[%d].mediaType = %q, want %q", i, got.MediaType, want.mediaType)
		}
		if got.Title() != want.title {
			t.Errorf("layers[%d].title = %q, want %q", i, got.Title(), want.title)
		}
		if !strings.HasPrefix(got.Digest, "sha256:") {
			t.Errorf("layers[%d].digest %q lacks sha256: prefix", i, got.Digest)
		}
	}

	// The config blob the pusher uploaded must round-trip into SnapshotConfig.
	configBlob, ok := uploader.blobs[parsed.Config.Digest]
	if !ok {
		t.Fatalf("config blob %s not uploaded", parsed.Config.Digest)
	}
	var snapCfg manifest.SnapshotConfig
	if err := json.Unmarshal(configBlob, &snapCfg); err != nil {
		t.Fatalf("decode config blob: %v", err)
	}
	if snapCfg.SnapshotID != "snap-id-1" || snapCfg.CPU != 4 || snapCfg.Memory != 1<<30 {
		t.Errorf("config blob mismatch: %+v", snapCfg)
	}
}

func TestPushOmitsBaseImageAnnotationWhenEmpty(t *testing.T) {
	cocoon := &fakeCocoon{exportTar: buildExportTar(t, snapshotExportConfig{Name: "myvm"}, map[string][]byte{
		"config.json": []byte(`{}`),
	})}
	uploader := newFakeUploader()
	pusher := &Pusher{Uploader: uploader, Cocoon: cocoon}

	if _, err := pusher.Push(context.Background(), PushOptions{Name: "myvm"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	parsed, err := manifest.Parse(uploader.manifests["myvm:latest"].bytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if _, ok := parsed.Annotations[manifest.AnnotationSnapshotBaseImage]; ok {
		t.Errorf("baseimage annotation should be absent when --base-image not provided")
	}
}

func TestPullReassemblesTarFromOCISnapshot(t *testing.T) {
	files := map[string][]byte{
		"config.json":   []byte(`{"cpu":2}`),
		"state.json":    []byte(`{"state":"saved"}`),
		"memory-ranges": []byte("mem-bytes"),
		"overlay.qcow2": []byte("qcow-bytes"),
	}
	cfg := snapshotExportConfig{
		ID:     "snap-id-1",
		Name:   "myvm",
		Image:  "ghcr.io/cocoonstack/cocoon/ubuntu:24.04",
		CPU:    2,
		Memory: 1 << 30,
		NICs:   1,
	}

	uploader := newFakeUploader()
	cocoon := &fakeCocoon{exportTar: buildExportTar(t, cfg, files)}
	pusher := &Pusher{Uploader: uploader, Cocoon: cocoon}
	if _, err := pusher.Push(context.Background(), PushOptions{Name: "myvm", Tag: "v1"}); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	pullCocoon := &fakeCocoon{}
	puller := &Puller{Downloader: uploader, Cocoon: pullCocoon}

	if err := puller.Pull(context.Background(), PullOptions{Name: "myvm", Tag: "v1", LocalName: "myvm-restored"}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if pullCocoon.importOpts.Name != "myvm-restored" {
		t.Errorf("import name = %q, want myvm-restored", pullCocoon.importOpts.Name)
	}

	tr := tar.NewReader(&pullCocoon.importPayload)
	gotFiles := map[string][]byte{}
	var gotEnvelope *snapshotExportEnvelope
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar body: %v", err)
		}
		if hdr.Name == snapshotJSONName {
			var envelope snapshotExportEnvelope
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			gotEnvelope = &envelope
			continue
		}
		gotFiles[hdr.Name] = body
	}

	if gotEnvelope == nil {
		t.Fatal("snapshot.json not in import stream")
	}
	if gotEnvelope.Config.Name != "myvm-restored" {
		t.Errorf("envelope name = %q, want myvm-restored", gotEnvelope.Config.Name)
	}
	if gotEnvelope.Config.ID != "snap-id-1" || gotEnvelope.Config.CPU != 2 {
		t.Errorf("envelope config mismatch: %+v", gotEnvelope.Config)
	}

	for name, want := range files {
		if got, ok := gotFiles[name]; !ok {
			t.Errorf("import stream missing %s", name)
		} else if !bytes.Equal(got, want) {
			t.Errorf("import stream %s = %q, want %q", name, got, want)
		}
	}
}

func TestPullRejectsNonSnapshotManifest(t *testing.T) {
	uploader := newFakeUploader()
	containerManifest := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:00","size":1},
		"layers": []
	}`)
	uploader.manifests["foo:latest"] = fakeManifestUpload{
		bytes:       containerManifest,
		contentType: manifest.MediaTypeOCIManifest,
	}

	puller := &Puller{Downloader: uploader, Cocoon: &fakeCocoon{}}
	err := puller.Pull(context.Background(), PullOptions{Name: "foo", Tag: "latest"})
	if err == nil {
		t.Fatal("expected error pulling container manifest as snapshot")
	}
	if !strings.Contains(err.Error(), "not a snapshot") {
		t.Errorf("error = %v, want %q substring", err, "not a snapshot")
	}
}

func TestPushRequiresName(t *testing.T) {
	pusher := &Pusher{Uploader: newFakeUploader(), Cocoon: &fakeCocoon{}}
	_, err := pusher.Push(context.Background(), PushOptions{})
	if err == nil {
		t.Fatal("expected error when name is empty")
	}
}
