package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/objectstore"
)

const (
	defaultManifestMediaType = manifest.MediaTypeOCIManifest
)

func urlVar(r *http.Request, name string) string {
	return mux.Vars(r)[name]
}

func stripSHA256Prefix(s string) string {
	return strings.TrimPrefix(s, "sha256:")
}

func isDigestRef(ref string) bool {
	return strings.HasPrefix(ref, "sha256:")
}

func isNotFound(err error) bool {
	return errors.Is(err, objectstore.ErrNotFound)
}
