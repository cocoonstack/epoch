package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registryclient"
)

// snapshotExport JSON format tests

func TestSnapshotExportJSON(t *testing.T) {
	e := snapshotExport{
		Version: 1,
		Config: snapshotConfig{
			ID:           "abc123",
			Name:         "test-snap",
			Image:        "ubuntu:24.04",
			ImageBlobIDs: map[string]struct{}{"hex1": {}},
			CPU:          4,
			Memory:       1 << 30,
			Storage:      10 << 30,
			NICs:         2,
		},
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify it can be unmarshaled back.
	var got snapshotExport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("version: got %d, want 1", got.Version)
	}
	if got.Config.Name != "test-snap" {
		t.Errorf("name: got %q, want %q", got.Config.Name, "test-snap")
	}
	if got.Config.CPU != 4 {
		t.Errorf("cpu: got %d, want 4", got.Config.CPU)
	}
	if _, ok := got.Config.ImageBlobIDs["hex1"]; !ok {
		t.Error("ImageBlobIDs missing 'hex1'")
	}
}

func TestSnapshotExportJSON_ImageBlobIDsFormat(t *testing.T) {
	// Verify that ImageBlobIDs serializes as map[string]struct{} (cocoon format),
	// not map[string]string (epoch format).
	e := snapshotExport{
		Version: 1,
		Config: snapshotConfig{
			Name:         "test",
			ImageBlobIDs: map[string]struct{}{"deadbeef": {}},
		},
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}

	// Should contain "deadbeef":{} not "deadbeef":"..."
	s := string(data)
	if !strings.Contains(s, `"deadbeef":{}`) {
		t.Errorf("expected cocoon-style ImageBlobIDs, got: %s", s)
	}
}

// Streaming tests with mock HTTP server

func newMockRegistry(t *testing.T, m *manifest.Manifest, blobs map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Serve manifest.
	manifestData, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc(fmt.Sprintf("/v2/%s/manifests/%s", m.Name, m.Tag), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(manifestData) //nolint:errcheck
	})

	// Serve blobs.
	for digest, data := range blobs {
		mux.HandleFunc(fmt.Sprintf("/v2/%s/blobs/sha256:%s", m.Name, digest), func(w http.ResponseWriter, r *http.Request) {
			w.Write(data) //nolint:errcheck
		})
	}

	return httptest.NewServer(mux)
}

func TestStreamSnapshot(t *testing.T) {
	blobData := map[string][]byte{
		"aaa111": []byte(`{"cpu":4}`),
		"bbb222": []byte("disk overlay data here"),
		"ccc333": []byte("memory ranges data"),
	}

	m := &manifest.Manifest{
		SchemaVersion: 1,
		Name:          "test-snap",
		Tag:           "latest",
		SnapshotID:    "snap-id-001",
		Image:         "ubuntu:24.04",
		ImageBlobIDs:  map[string]string{"hex1": "base.qcow2"},
		CPU:           4,
		Memory:        1 << 30,
		Storage:       10 << 30,
		NICs:          2,
		Layers: []manifest.Layer{
			{Digest: "aaa111", Size: int64(len(blobData["aaa111"])), Filename: "config.json"},
			{Digest: "bbb222", Size: int64(len(blobData["bbb222"])), Filename: "overlay.qcow2"},
			{Digest: "ccc333", Size: int64(len(blobData["ccc333"])), Filename: "memory-ranges"},
		},
		TotalSize: 50,
	}

	srv := newMockRegistry(t, m, blobData)
	defer srv.Close()

	var buf bytes.Buffer
	client := newTestClient(srv.URL)
	if err := streamSnapshot(t.Context(), client, "test-snap", m, &buf); err != nil {
		t.Fatalf("streamSnapshot: %v", err)
	}

	output := buf.Bytes()
	gr, err := gzip.NewReader(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	files := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(tr)
		files[hdr.Name] = data
	}

	// Verify snapshot.json.
	sjData, ok := files["snapshot.json"]
	if !ok {
		t.Fatal("snapshot.json not found in archive")
	}
	var envelope snapshotExport
	if err := json.Unmarshal(sjData, &envelope); err != nil {
		t.Fatalf("parse snapshot.json: %v", err)
	}
	if envelope.Version != 1 {
		t.Errorf("version: got %d, want 1", envelope.Version)
	}
	if envelope.Config.ID != "snap-id-001" {
		t.Errorf("ID: got %q, want %q", envelope.Config.ID, "snap-id-001")
	}
	if envelope.Config.CPU != 4 {
		t.Errorf("CPU: got %d, want 4", envelope.Config.CPU)
	}
	if _, ok := envelope.Config.ImageBlobIDs["hex1"]; !ok {
		t.Error("ImageBlobIDs missing 'hex1'")
	}

	// Verify layer files.
	for _, layer := range m.Layers {
		got, ok := files[layer.Filename]
		if !ok {
			t.Errorf("layer %s not found in archive", layer.Filename)
			continue
		}
		want := blobData[layer.Digest]
		if !bytes.Equal(got, want) {
			t.Errorf("layer %s: got %q, want %q", layer.Filename, got, want)
		}
	}
}

func TestStreamCloudImage(t *testing.T) {
	// Simulate qcow2 data with QFI magic.
	qcow2Part1 := append([]byte{'Q', 'F', 'I', 0xfb}, make([]byte, 100)...)
	qcow2Part2 := make([]byte, 50)

	blobData := map[string][]byte{
		"ddd444": qcow2Part1,
		"eee555": qcow2Part2,
	}

	m := &manifest.Manifest{
		SchemaVersion: 1,
		Name:          "ubuntu-base",
		Tag:           "latest",
		Layers: []manifest.Layer{
			{Digest: "ddd444", Size: int64(len(qcow2Part1)), Filename: "ubuntu.qcow2.part.001"},
			{Digest: "eee555", Size: int64(len(qcow2Part2)), Filename: "ubuntu.qcow2.part.002"},
		},
		TotalSize: int64(len(qcow2Part1) + len(qcow2Part2)),
	}

	srv := newMockRegistry(t, m, blobData)
	defer srv.Close()

	var buf bytes.Buffer
	client := newTestClient(srv.URL)
	if err := streamCloudImage(t.Context(), client, "ubuntu-base", m, &buf); err != nil {
		t.Fatalf("streamCloudImage: %v", err)
	}

	output := buf.Bytes()
	gr, err := gzip.NewReader(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	got, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gunzipped: %v", err)
	}
	gr.Close()

	// Verify content is concat of parts.
	want := append(qcow2Part1, qcow2Part2...)
	if !bytes.Equal(got, want) {
		t.Errorf("content mismatch: got %d bytes, want %d", len(got), len(want))
	}

	// Verify starts with QFI magic.
	if got[0] != 'Q' || got[1] != 'F' || got[2] != 'I' || got[3] != 0xfb {
		t.Error("output should start with qcow2 magic")
	}
}

// newTestClient creates a registry client pointing at the test server.
func newTestClient(baseURL string) *registryclient.Client {
	return registryclient.New(baseURL, "")
}
