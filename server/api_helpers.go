package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/store"
)

func tagResponse(t *store.Tag, snapshotConfig *manifest.SnapshotConfig) map[string]any {
	resp := map[string]any{
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
	if snapshotConfig != nil {
		resp["snapshotConfig"] = snapshotConfig
	}
	if len(t.PlatformSizes) > 0 {
		resp["platformSizes"] = t.PlatformSizes
	}
	return resp
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
