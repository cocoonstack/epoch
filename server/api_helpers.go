package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cocoonstack/epoch/store"
)

func tagResponse(t *store.Tag) (map[string]any, error) {
	var manifest any
	if err := json.Unmarshal([]byte(t.ManifestJSON), &manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return map[string]any{
		"repoName":   t.RepoName,
		"tag":        t.Name,
		"digest":     t.Digest,
		"totalSize":  t.TotalSize,
		"layerCount": t.LayerCount,
		"pushedAt":   t.PushedAt,
		"syncedAt":   t.SyncedAt,
		"manifest":   manifest,
	}, nil
}

func parsePositivePathID(r *http.Request, key string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(r.PathValue(key), "%d", &id); err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}
