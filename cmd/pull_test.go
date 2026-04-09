package cmd

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cocoonstack/epoch/manifest"
)

func TestBuildCocoonImportArgs(t *testing.T) {
	snapshotManifest := &manifest.Manifest{
		Name:       "myvm",
		Tag:        "v1",
		SnapshotID: "abc",
		Layers: []manifest.Layer{
			{Filename: "config.json"},
			{Filename: "memory-ranges"},
			{Filename: "overlay.qcow2"},
		},
	}
	cloudImageManifest := &manifest.Manifest{
		Name: "ubuntu-base",
		Tag:  "latest",
		Layers: []manifest.Layer{
			{Filename: "ubuntu.qcow2"},
		},
	}

	tests := []struct {
		name         string
		m            *manifest.Manifest
		registryName string
		overrideName string
		description  string
		want         []string
	}{
		{
			name:         "snapshot default name",
			m:            snapshotManifest,
			registryName: "myvm",
			want:         []string{"snapshot", "import", "--name", "myvm"},
		},
		{
			name:         "snapshot with override and description",
			m:            snapshotManifest,
			registryName: "myvm",
			overrideName: "myvm-restored",
			description:  "from epoch",
			want:         []string{"snapshot", "import", "--name", "myvm-restored", "--description", "from epoch"},
		},
		{
			name:         "cloud image default name",
			m:            cloudImageManifest,
			registryName: "ubuntu-base",
			want:         []string{"image", "import", "ubuntu-base"},
		},
		{
			name:         "cloud image with override (description ignored)",
			m:            cloudImageManifest,
			registryName: "ubuntu-base",
			overrideName: "ubuntu-22.04",
			description:  "ignored for images",
			want:         []string{"image", "import", "ubuntu-22.04"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCocoonImportArgs(tt.m, tt.registryName, tt.overrideName, tt.description)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCombineImportErrors(t *testing.T) {
	streamErr := errors.New("blob fetch failed")
	closeErr := errors.New("pipe closed twice")
	waitErr := errors.New("cocoon exit 1")

	tests := []struct {
		name             string
		bin              string
		stream, cl, wait error
		wantContains     string
	}{
		{name: "all nil", bin: "cocoon"},
		{name: "stream wins over close+wait", bin: "cocoon", stream: streamErr, cl: closeErr, wait: waitErr, wantContains: "stream artifact"},
		{name: "close wins over wait", bin: "cocoon", cl: closeErr, wait: waitErr, wantContains: "close cocoon stdin"},
		{name: "close alone", bin: "cocoon", cl: closeErr, wantContains: "close cocoon stdin"},
		{name: "wait alone", bin: "cocoon", wait: waitErr, wantContains: "cocoon import"},
		{name: "wait error uses custom binary path", bin: "/opt/cocoon-dev/cocoon", wait: waitErr, wantContains: "/opt/cocoon-dev/cocoon import"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := combineImportErrors(tt.bin, tt.stream, tt.cl, tt.wait)
			if tt.wantContains == "" {
				if got != nil {
					t.Errorf("want nil, got %v", got)
				}
				return
			}
			if got == nil || !strings.Contains(got.Error(), tt.wantContains) {
				t.Errorf("want error containing %q, got %v", tt.wantContains, got)
			}
		})
	}
}

func TestResolveCocoonBinary(t *testing.T) {
	// Use /bin/sh as a guaranteed-present binary on Linux test runners.
	t.Setenv(cocoonBinaryEnv, "  /bin/sh  ")
	got, err := resolveCocoonBinary()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "/bin/sh" {
		t.Errorf("got %q, want /bin/sh (whitespace should be trimmed)", got)
	}

	t.Setenv(cocoonBinaryEnv, "/nonexistent/path/cocoon-xyz-zzz")
	if _, err := resolveCocoonBinary(); err == nil {
		t.Error("expected error for nonexistent binary, got nil")
	}
}
