package store

import "time"

// Repository is a DB repository record.
type Repository struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	TagCount  int       `json:"tagCount"`
	TotalSize int64     `json:"totalSize"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Tag is a DB tag record.
type Tag struct {
	ID           int64     `json:"id"`
	RepositoryID int64     `json:"-"`
	RepoName     string    `json:"repoName,omitempty"`
	Name         string    `json:"name"`
	Digest       string    `json:"digest"`
	ManifestJSON string    `json:"-"`
	TotalSize    int64     `json:"totalSize"`
	LayerCount   int       `json:"layerCount"`
	PushedAt     time.Time `json:"pushedAt"`
	SyncedAt     time.Time `json:"syncedAt"`
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
type Token struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Token     string     `json:"token"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}
