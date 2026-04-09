package server

import (
	"errors"
	"strings"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/objectstore"
)

// defaultManifestMediaType is what HEAD/GET handlers fall back to when an
// uploaded manifest doesn't carry an explicit `mediaType` field. Standard
// OCI image manifest is the right default for everything cocoonstack
// publishes today.
const defaultManifestMediaType = manifest.MediaTypeOCIManifest

func stripSHA256Prefix(s string) string {
	return strings.TrimPrefix(s, "sha256:")
}

func isNotFound(err error) bool {
	return errors.Is(err, objectstore.ErrNotFound)
}
