package server

import (
	"errors"
	"strings"

	"github.com/cocoonstack/epoch/objectstore"
)

const manifestMediaType = "application/vnd.epoch.manifest.v1+json"

func stripSHA256Prefix(s string) string {
	return strings.TrimPrefix(s, "sha256:")
}

func isNotFound(err error) bool {
	return errors.Is(err, objectstore.ErrNotFound) || strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404")
}
