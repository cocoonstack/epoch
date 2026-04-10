package store

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Repository is a DB repository record. ArtifactType / Kind reflect the
// most recently pushed tag in the repo so the UI can show the artifact
// flavor (cloud-image / snapshot / container-image) without making a
// separate per-tag round-trip.
type Repository struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	TagCount     int       `json:"tagCount"`
	TotalSize    int64     `json:"totalSize"`
	ArtifactType string    `json:"artifactType,omitempty"`
	Kind         string    `json:"kind,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Tag is a DB tag record. ArtifactType captures the OCI 1.1 manifest
// artifactType field (e.g. cocoonstack.snapshot.v1+json) so the UI can
// show whether a tag is a snapshot, cloud image, or container image
// without re-parsing the manifest JSON on every list call.
//
// PlatformSizes is populated only for tags whose kind is image-index. Each
// entry is the standalone (config + sum(layers)) size of one child manifest,
// keyed by the child's content digest. Materialized at sync time so the tag
// detail API does not need to refetch every child manifest just to render
// per-platform sizes in the UI.
type Tag struct {
	ID            int64         `json:"id"`
	RepositoryID  int64         `json:"-"`
	RepoName      string        `json:"repoName,omitempty"`
	Name          string        `json:"name"`
	Digest        string        `json:"digest"`
	ArtifactType  string        `json:"artifactType,omitempty"`
	Kind          string        `json:"kind,omitempty"`
	ManifestJSON  string        `json:"-"`
	TotalSize     int64         `json:"totalSize"`
	LayerCount    int           `json:"layerCount"`
	PlatformSizes PlatformSizes `json:"platformSizes,omitempty"`
	PushedAt      time.Time     `json:"pushedAt"`
	SyncedAt      time.Time     `json:"syncedAt"`
}

// PlatformSize is the standalone content size of one child manifest inside
// an OCI image index — i.e. config blob plus the sum of layer blobs as
// declared by that child, with no cross-platform deduplication. The natural
// answer to "if I only pulled this platform, how many bytes would I download".
type PlatformSize struct {
	Digest     string `json:"digest"`
	Size       int64  `json:"size"`
	LayerCount int    `json:"layerCount"`
}

// PlatformSizes is a slice of [PlatformSize] that round-trips through a MySQL
// JSON column transparently. Empty/nil persists as SQL NULL so non-index tags
// do not store a placeholder `[]`.
type PlatformSizes []PlatformSize

// Value implements driver.Valuer for storing in a JSON column.
func (p PlatformSizes) Value() (driver.Value, error) {
	if len(p) == 0 {
		return nil, nil
	}
	return json.Marshal(p)
}

// Scan implements sql.Scanner for reading back from a JSON column. NULL maps
// to a nil slice; the column is allowed to be string or []byte depending on
// driver flags.
func (p *PlatformSizes) Scan(src any) error {
	if src == nil {
		*p = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("PlatformSizes.Scan: unsupported type %T", src)
	}
	if len(raw) == 0 {
		*p = nil
		return nil
	}
	return json.Unmarshal(raw, p)
}

// Blob is a DB blob record.
type Blob struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	RefCount  int    `json:"refCount"`
}

// DashboardStats holds aggregate stats for the UI dashboard.
type DashboardStats struct {
	RepositoryCount int   `json:"repositoryCount"`
	TagCount        int   `json:"tagCount"`
	BlobCount       int   `json:"blobCount"`
	TotalSize       int64 `json:"totalSize"`
}

// Token is a registry access token.
// The Token field is only populated on create (returned to caller); it is never read back from the DB.
type Token struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Token     string     `json:"token,omitempty"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

func (r *Repository) scanSummary(row rowScanner) error {
	return row.Scan(&r.ID, &r.Name, &r.CreatedAt, &r.UpdatedAt, &r.TagCount, &r.TotalSize, &r.ArtifactType, &r.Kind)
}

func (t *Tag) scanSummary(row rowScanner) error {
	return row.Scan(&t.ID, &t.RepositoryID, &t.Name, &t.Digest, &t.ArtifactType, &t.Kind, &t.TotalSize, &t.LayerCount, &t.PushedAt, &t.SyncedAt)
}

func (t *Tag) scanDetails(row rowScanner) error {
	return row.Scan(&t.ID, &t.RepositoryID, &t.Name, &t.Digest, &t.ArtifactType, &t.Kind, &t.ManifestJSON, &t.TotalSize, &t.LayerCount, &t.PlatformSizes, &t.PushedAt, &t.SyncedAt)
}

func (t *Token) scan(row rowScanner) error {
	return row.Scan(&t.ID, &t.Name, &t.CreatedBy, &t.CreatedAt, &t.LastUsed)
}
