package manifest

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Kind
	}{
		{
			name: "windows oci artifact (artifactType cloudimg)",
			raw: `{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"artifactType": "application/vnd.cocoonstack.os-image.v1+json",
				"config": {"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44","size":2},
				"layers": [
					{"mediaType":"application/vnd.cocoonstack.disk.qcow2.part","digest":"sha256:aa","size":1}
				]
			}`,
			want: KindCloudImage,
		},
		{
			name: "snapshot oci artifact",
			raw: `{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"artifactType": "application/vnd.cocoonstack.snapshot.v1+json",
				"config": {"mediaType":"application/vnd.cocoonstack.snapshot.config.v1+json","digest":"sha256:11","size":42},
				"layers": [
					{"mediaType":"application/vnd.cocoonstack.disk.qcow2","digest":"sha256:bb","size":1}
				]
			}`,
			want: KindSnapshot,
		},
		{
			name: "ubuntu docker buildx oci image (no artifactType, image config blob)",
			raw: `{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cc","size":1500},
				"layers": [
					{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:dd","size":987}
				]
			}`,
			want: KindContainerImage,
		},
		{
			name: "docker v2 manifest (no artifactType, docker config)",
			raw: `{
				"schemaVersion": 2,
				"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
				"config": {"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:ee","size":700},
				"layers": [
					{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"sha256:ff","size":42}
				]
			}`,
			want: KindContainerImage,
		},
		{
			name: "oci image index (multi-arch container, e.g. ghcr.io/cocoonstack/cocoon/ubuntu:24.04)",
			raw: `{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.index.v1+json",
				"manifests": [
					{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aa","size":675,"platform":{"architecture":"amd64","os":"linux"}},
					{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:bb","size":675,"platform":{"architecture":"arm64","os":"linux"}}
				]
			}`,
			want: KindContainerImage,
		},
		{
			name: "docker manifest list",
			raw: `{
				"schemaVersion": 2,
				"mediaType": "application/vnd.docker.distribution.manifest.list.v2+json",
				"manifests": []
			}`,
			want: KindContainerImage,
		},
		{
			name: "manifest with no discriminator",
			raw:  `{"schemaVersion":2,"layers":[]}`,
			want: KindUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Classify([]byte(tt.raw))
			if err != nil {
				t.Fatalf("Classify error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Classify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClassifyMalformedJSON(t *testing.T) {
	if _, err := Classify([]byte("not json")); err == nil {
		t.Fatalf("expected error for non-JSON input")
	}
}

func TestParse(t *testing.T) {
	raw := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"artifactType": "application/vnd.cocoonstack.snapshot.v1+json",
		"config": {
			"mediaType": "application/vnd.cocoonstack.snapshot.config.v1+json",
			"digest": "sha256:abc",
			"size": 42
		},
		"layers": [
			{
				"mediaType": "application/vnd.cocoonstack.vm.config+json",
				"digest": "sha256:11",
				"size": 100,
				"annotations": {"org.opencontainers.image.title": "config.json"}
			},
			{
				"mediaType": "application/vnd.cocoonstack.disk.qcow2",
				"digest": "sha256:22",
				"size": 200,
				"annotations": {"org.opencontainers.image.title": "overlay.qcow2"}
			}
		],
		"annotations": {
			"cocoonstack.snapshot.id": "sid-1",
			"cocoonstack.snapshot.baseimage": "ghcr.io/cocoonstack/cocoon/ubuntu:24.04"
		}
	}`

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.ArtifactType != ArtifactTypeSnapshot {
		t.Errorf("ArtifactType = %q, want %q", m.ArtifactType, ArtifactTypeSnapshot)
	}
	if m.Config.MediaType != MediaTypeSnapshotConfig {
		t.Errorf("Config.MediaType = %q, want %q", m.Config.MediaType, MediaTypeSnapshotConfig)
	}
	if len(m.Layers) != 2 {
		t.Fatalf("len(Layers) = %d, want 2", len(m.Layers))
	}
	if got := m.Layers[0].Title(); got != "config.json" {
		t.Errorf("Layers[0].Title() = %q, want config.json", got)
	}
	if got := m.Layers[1].Title(); got != "overlay.qcow2" {
		t.Errorf("Layers[1].Title() = %q, want overlay.qcow2", got)
	}
	if m.Annotations[AnnotationSnapshotBaseImage] != "ghcr.io/cocoonstack/cocoon/ubuntu:24.04" {
		t.Errorf("baseimage annotation mismatch: %q", m.Annotations[AnnotationSnapshotBaseImage])
	}
}

func TestMediaTypeForCocoonFile(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"config.json", MediaTypeVMConfig},
		{"state.json", MediaTypeVMState},
		{"memory-ranges", MediaTypeVMMemory},
		{"cidata.img", MediaTypeVMCidata},
		{"overlay.qcow2", MediaTypeDiskQcow2},
		{"some-other-disk.qcow2", MediaTypeDiskQcow2},
		{"raw-disk.raw", MediaTypeDiskRaw},
		{"unknown", MediaTypeGeneric},
	}
	for _, tt := range tests {
		if got := MediaTypeForCocoonFile(tt.filename); got != tt.want {
			t.Errorf("MediaTypeForCocoonFile(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

func TestIsDiskMediaType(t *testing.T) {
	yes := []string{
		MediaTypeDiskQcow2,
		MediaTypeDiskQcow2Part,
		MediaTypeDiskRaw,
		MediaTypeDiskRawPart,
	}
	no := []string{
		MediaTypeVMConfig,
		MediaTypeVMMemory,
		MediaTypeGeneric,
		"text/plain",
		"",
	}
	for _, mt := range yes {
		if !IsDiskMediaType(mt) {
			t.Errorf("IsDiskMediaType(%q) = false, want true", mt)
		}
	}
	for _, mt := range no {
		if IsDiskMediaType(mt) {
			t.Errorf("IsDiskMediaType(%q) = true, want false", mt)
		}
	}
}
