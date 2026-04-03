package manifest

import "testing"

func TestManifest_IsCloudImage(t *testing.T) {
	tests := []struct {
		name string
		m    *Manifest
		want bool
	}{
		{
			name: "snapshot with config files",
			m: &Manifest{
				Layers: []Layer{
					{Filename: "config.json"},
					{Filename: "overlay.qcow2"},
					{Filename: "memory-ranges"},
				},
			},
			want: false,
		},
		{
			name: "qcow2 parts",
			m: &Manifest{
				Layers: []Layer{
					{Filename: "win11.qcow2.part.001"},
					{Filename: "win11.qcow2.part.002"},
				},
			},
			want: true,
		},
		{
			name: "single qcow2",
			m: &Manifest{
				Layers: []Layer{{Filename: "ubuntu.qcow2"}},
			},
			want: true,
		},
		{
			name: "single raw disk",
			m: &Manifest{
				Layers: []Layer{{Filename: "disk.raw"}},
			},
			want: true,
		},
		{
			name: "with BaseImages",
			m: &Manifest{
				Layers:     []Layer{{Filename: "overlay.qcow2"}},
				BaseImages: []Layer{{Filename: "base.qcow2"}},
			},
			want: false,
		},
		{
			name: "with ImageBlobIDs",
			m: &Manifest{
				Layers:       []Layer{{Filename: "overlay.qcow2"}},
				ImageBlobIDs: map[string]string{"abc": "base.qcow2"},
			},
			want: false,
		},
		{
			name: "empty manifest",
			m:    &Manifest{},
			want: false,
		},
		{
			name: "nil manifest",
			m:    nil,
			want: false,
		},
		{
			name: "no disk layers",
			m: &Manifest{
				Layers: []Layer{{Filename: "random.bin"}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.IsCloudImage(); got != tt.want {
				t.Errorf("IsCloudImage() = %v, want %v", got, tt.want)
			}
		})
	}
}
