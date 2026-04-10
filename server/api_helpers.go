package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/cocoonstack/epoch/store"
)

// tagResponse builds the tag detail payload by passing the manifest bytes
// through as json.RawMessage — the manifest is already stored as valid JSON
// in MySQL, so re-unmarshaling it into a map[string]any only to re-marshal
// it on the way out wastes an allocation tree on every request (and grows
// linearly with multi-arch image index size).
func tagResponse(t *store.Tag) map[string]any {
	return map[string]any{
		"repoName":     t.RepoName,
		"tag":          t.Name,
		"digest":       t.Digest,
		"artifactType": t.ArtifactType,
		"kind":         t.Kind,
		"totalSize":    t.TotalSize,
		"layerCount":   t.LayerCount,
		"pushedAt":     t.PushedAt,
		"syncedAt":     t.SyncedAt,
		"manifest":     json.RawMessage(t.ManifestJSON),
	}
}

func parsePositivePathID(r *http.Request, key string) (int64, error) {
	id, err := strconv.ParseInt(urlVar(r, key), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id: %w", err)
	}
	if id <= 0 {
		return 0, errors.New("invalid id: must be positive")
	}
	return id, nil
}
