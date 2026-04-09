package server

import "testing"

func TestParseV2Path(t *testing.T) {
	tests := []struct {
		raw    string
		want   v2Route
		wantOK bool
	}{
		// Single-segment name.
		{"nginx/manifests/latest", v2Route{"nginx", v2ActionManifests, "latest"}, true},
		{"nginx/blobs/sha256:abc", v2Route{"nginx", v2ActionBlobs, "sha256:abc"}, true},
		{"nginx/tags/list", v2Route{"nginx", v2ActionTags, "list"}, true},
		{"nginx/blobs/uploads/", v2Route{"nginx", v2ActionUploads, ""}, true},
		{"nginx/blobs/uploads/uuid-1", v2Route{"nginx", v2ActionUploads, "uuid-1"}, true},

		// Multi-segment name.
		{"library/nginx/manifests/latest", v2Route{"library/nginx", v2ActionManifests, "latest"}, true},
		{"org/team/app/blobs/sha256:def", v2Route{"org/team/app", v2ActionBlobs, "sha256:def"}, true},
		{"a/b/c/tags/list", v2Route{"a/b/c", v2ActionTags, "list"}, true},
		{"library/nginx/blobs/uploads/", v2Route{"library/nginx", v2ActionUploads, ""}, true},
		{"library/nginx/blobs/uploads/uuid-2", v2Route{"library/nginx", v2ActionUploads, "uuid-2"}, true},

		// Digest-by-reference with sha256 prefix.
		{"nginx/manifests/sha256:abc123", v2Route{"nginx", v2ActionManifests, "sha256:abc123"}, true},

		// Invalid: empty, no action, missing segments.
		{"", v2Route{}, false},
		{"nginx", v2Route{}, false},
		{"manifests/latest", v2Route{}, false},    // no name before action
		{"blobs/sha256:abc", v2Route{}, false},    // no name before action
		{"tags/list", v2Route{}, false},           // no name before action
		{"nginx/blobs/uploads", v2Route{}, false}, // missing trailing slash
		{"nginx/tags/latest", v2Route{}, false},   // tags/ must be followed by "list"
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, ok := parseV2Path(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("parseV2Path(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Errorf("parseV2Path(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}
