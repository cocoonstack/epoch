package server

import "testing"

func TestParseAPIRepoPath(t *testing.T) {
	tests := []struct {
		raw    string
		want   apiRepoRoute
		wantOK bool
	}{
		// Single-segment name.
		{"nginx", apiRepoRoute{Name: "nginx"}, true},
		{"nginx/tags", apiRepoRoute{Name: "nginx", Sub: "tags"}, true},
		{"nginx/tags/latest", apiRepoRoute{Name: "nginx", Sub: "tags", Tag: "latest"}, true},

		// Multi-segment name.
		{"library/nginx", apiRepoRoute{Name: "library/nginx"}, true},
		{"library/nginx/tags", apiRepoRoute{Name: "library/nginx", Sub: "tags"}, true},
		{"library/nginx/tags/v1", apiRepoRoute{Name: "library/nginx", Sub: "tags", Tag: "v1"}, true},
		{"org/team/app/tags/prod", apiRepoRoute{Name: "org/team/app", Sub: "tags", Tag: "prod"}, true},

		// Edge cases.
		{"", apiRepoRoute{}, false},
		{"tags", apiRepoRoute{Name: "tags"}, true},               // bare name "tags" is a valid repo name
		{"tags/latest", apiRepoRoute{Name: "tags/latest"}, true}, // repo named "tags/latest" — no match because tail scan finds no keyword
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, ok := parseAPIRepoPath(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("parseAPIRepoPath(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Errorf("parseAPIRepoPath(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}
