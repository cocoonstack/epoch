package store

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Repository represents a repository row with aggregated tag stats.
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

// Tag represents a manifest tag row in the database.
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

// PlatformSize stores per-platform size and layer count for image indexes.
type PlatformSize struct {
	Digest     string `json:"digest"`
	Size       int64  `json:"size"`
	LayerCount int    `json:"layerCount"`
}

// PlatformSizes round-trips through a MySQL JSON column; empty persists as NULL.
type PlatformSizes []PlatformSize

// Value marshals PlatformSizes to JSON for MySQL storage.
func (p PlatformSizes) Value() (driver.Value, error) {
	if len(p) == 0 {
		return nil, nil
	}
	return json.Marshal(p)
}

// Scan unmarshals JSON from a MySQL column into PlatformSizes.
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
		return fmt.Errorf("scan platform_sizes: unsupported type %T", src)
	}
	if len(raw) == 0 {
		*p = nil
		return nil
	}
	return json.Unmarshal(raw, p)
}

// Blob represents a content-addressable blob row in the database.
type Blob struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	RefCount  int    `json:"refCount"`
}

// DashboardStats holds aggregate counts for the web UI dashboard.
type DashboardStats struct {
	RepositoryCount int   `json:"repositoryCount"`
	TagCount        int   `json:"tagCount"`
	BlobCount       int   `json:"blobCount"`
	TotalSize       int64 `json:"totalSize"`
}

// Token represents an API access token row in the database.
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
