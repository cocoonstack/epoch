package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/store"
)

// snapshotConfigBlobLimit caps the snapshot config blob read to a sane upper
// bound. The actual payload is ~200 bytes (a fixed-shape SnapshotConfig
// struct); 1 MiB is many orders of magnitude above any plausible value but
// keeps a malicious or corrupted manifest from streaming forever.
const snapshotConfigBlobLimit = 1 << 20

// tagResponse builds the tag detail payload by passing the manifest bytes
// through as json.RawMessage — the manifest is already stored as valid JSON
// in MySQL, so re-unmarshaling it into a map[string]any only to re-marshal
// it on the way out wastes an allocation tree on every request (and grows
// linearly with multi-arch image index size).
//
// snapshotConfig is the decoded contents of the snapshot config blob (the
// 200-byte SnapshotConfig referenced by manifest.config.digest). It is only
// inlined when non-nil — the caller decides whether to fetch it (currently
// only the snapshot kind triggers a fetch).
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
	return resp
}

// fetchSnapshotConfig parses the cached manifest JSON to find the snapshot
// config descriptor, then streams the (tiny) config blob from object storage
// and decodes it as a [manifest.SnapshotConfig]. Returns (nil, nil) when the
// manifest's config descriptor is not a snapshot config — that is the normal
// non-snapshot path, not an error. Real failures (parse, stream, decode) are
// returned so the caller can decide whether to surface or log-and-skip.
func (s *Server) fetchSnapshotConfig(ctx context.Context, name, manifestJSON string) (*manifest.SnapshotConfig, error) {
	m, err := manifest.Parse([]byte(manifestJSON))
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Config.MediaType != manifest.MediaTypeSnapshotConfig {
		return nil, nil
	}
	body, _, err := s.reg.StreamBlob(ctx, stripSHA256Prefix(m.Config.Digest))
	if err != nil {
		return nil, fmt.Errorf("stream config blob %s for %s: %w", m.Config.Digest, name, err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, snapshotConfigBlobLimit))
	if err != nil {
		return nil, fmt.Errorf("read config blob %s: %w", m.Config.Digest, err)
	}
	var cfg manifest.SnapshotConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode snapshot config: %w", err)
	}
	return &cfg, nil
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
