package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/objectstore"
)

// defaultManifestMediaType is what HEAD/GET handlers fall back to when an
// uploaded manifest doesn't carry an explicit `mediaType` field. Standard
// OCI image manifest is the right default for everything cocoonstack
// publishes today.
const defaultManifestMediaType = manifest.MediaTypeOCIManifest

// urlVar returns the value of a route variable extracted by gorilla/mux,
// or the empty string if the variable is not set.
func urlVar(r *http.Request, name string) string {
	if v := mux.Vars(r); v != nil {
		return v[name]
	}
	return ""
}

func stripSHA256Prefix(s string) string {
	return strings.TrimPrefix(s, "sha256:")
}

// isDigestRef reports whether an OCI manifest reference is a content digest
// (e.g. `sha256:abc...`) rather than a tag. The single source of truth for
// every site that needs to branch on "is this a tag or a digest" — keeps
// the definition consistent if we ever add sha512: support.
func isDigestRef(ref string) bool {
	return strings.HasPrefix(ref, "sha256:")
}

func isNotFound(err error) bool {
	return errors.Is(err, objectstore.ErrNotFound)
}
