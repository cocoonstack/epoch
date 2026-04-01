// Package manifest defines the Epoch registry data model.
//
// Epoch manages Cocoon VM snapshots as content-addressable artifacts:
//
//	epoch/
//	  manifests/{name}/{tag}.json    — snapshot manifest (metadata + blob refs)
//	  blobs/sha256/{digest}          — actual snapshot files (overlay, memory, config, ...)
//	  catalog.json                   — global index of all repositories
package manifest

import "time"

// Manifest describes a snapshot stored in Epoch.
// Analogous to an OCI image manifest — references content-addressable blobs.
type Manifest struct {
	// Schema version for forward compat.
	SchemaVersion int `json:"schemaVersion"`

	// Snapshot identity.
	Name string `json:"name"` // repository name (e.g. "sre-agent-bot")
	Tag  string `json:"tag"`  // tag (e.g. "v2", "latest")

	// Snapshot metadata (from Cocoon SnapshotConfig).
	SnapshotID   string            `json:"snapshotId"`
	Image        string            `json:"image,omitempty"`        // source image ref
	ImageBlobIDs map[string]string `json:"imageBlobIDs,omitempty"` // cocoon blob hex → object store key
	CPU          int               `json:"cpu,omitempty"`
	Memory       int64             `json:"memory,omitempty"`  // bytes
	Storage      int64             `json:"storage,omitempty"` // bytes
	NICs         int               `json:"nics,omitempty"`

	// Content-addressable layers.
	Layers []Layer `json:"layers"`

	// Base image blobs (cloudimg qcow2 etc.).
	BaseImages []Layer `json:"baseImages,omitempty"`

	// Total size of all layers + base images.
	TotalSize int64 `json:"totalSize"`

	// Timestamps.
	CreatedAt time.Time `json:"createdAt"`
	PushedAt  time.Time `json:"pushedAt"`
}

// Layer is a content-addressable blob reference.
type Layer struct {
	// Digest is the SHA-256 hex digest of the blob content.
	Digest string `json:"digest"`
	// Size in bytes.
	Size int64 `json:"size"`
	// Filename is the original filename (e.g. "overlay.qcow2", "memory-ranges").
	Filename string `json:"filename"`
	// MediaType hints at the content (e.g. "application/vnd.cocoon.disk.qcow2").
	MediaType string `json:"mediaType,omitempty"`
}

// Catalog is the global index of all repositories and their tags.
type Catalog struct {
	Repositories map[string]*Repository `json:"repositories"`
	UpdatedAt    time.Time              `json:"updatedAt"`
}

// Repository tracks tags for a snapshot name.
type Repository struct {
	Tags      map[string]string `json:"tags"` // tag → manifest digest
	UpdatedAt time.Time         `json:"updatedAt"`
}
