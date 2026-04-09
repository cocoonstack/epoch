package server

import "testing"

func TestDetectManifestMediaType(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "OCI image manifest",
			data: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`,
			want: "application/vnd.oci.image.manifest.v1+json",
		},
		{
			name: "OCI image index",
			data: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`,
			want: "application/vnd.oci.image.index.v1+json",
		},
		{
			name: "Docker manifest v2",
			data: `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","layers":[]}`,
			want: "application/vnd.docker.distribution.manifest.v2+json",
		},
		{
			name: "epoch legacy (no mediaType)",
			data: `{"schemaVersion":1,"name":"win11","tag":"latest","layers":[]}`,
			want: manifestMediaType,
		},
		{
			name: "garbage JSON falls back",
			data: `not json`,
			want: manifestMediaType,
		},
		{
			name: "empty mediaType field falls back",
			data: `{"mediaType":""}`,
			want: manifestMediaType,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectManifestMediaType([]byte(tt.data)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
