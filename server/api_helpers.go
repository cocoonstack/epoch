package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/cocoonstack/epoch/store"
)

func tagResponse(t *store.Tag) (map[string]any, error) {
	var manifest any
	if err := json.Unmarshal([]byte(t.ManifestJSON), &manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
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
		"manifest":     manifest,
	}, nil
}

func parsePositivePathID(r *http.Request, key string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id: %w", err)
	}
	if id <= 0 {
		return 0, errors.New("invalid id: must be positive")
	}
	return id, nil
}
