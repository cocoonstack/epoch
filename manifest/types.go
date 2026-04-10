// Package manifest defines the OCI manifest types and cocoonstack
// artifact-classification helpers used by epoch.
//
// Epoch stores three kinds of artifacts in the same OCI registry:
//
//   - Container images — standard OCI / Docker images. Epoch is a transparent
//     mirror; no cocoonstack-specific shape.
//   - Cloud images — disk-only OCI artifacts published via `oras push`, with
//     artifactType [ArtifactTypeOSImage]. The windows builder is the canonical
//     producer (split qcow2 parts).
//   - VM snapshots — full cocoon VM state captured by `cocoon vm save` and
//     uploaded by `epoch push` as an OCI artifact with artifactType
//     [ArtifactTypeSnapshot].
//
// All three are stored as OCI 1.1 image manifests. The classification function
// [Classify] looks at top-level artifactType first, then falls back to the
// config blob mediaType for plain container images.
package manifest

import (
	"encoding/json"
	"fmt"
	"time"
)

// Kind classifies an OCI manifest by what it stores. The zero value is
// KindUnknown, which means the bytes parsed as JSON but no recognized
// discriminator was present.
type Kind int

const (
	KindUnknown Kind = iota
	KindContainerImage
	KindCloudImage
	KindSnapshot
)

// String returns a short human-readable name for the kind.
func (k Kind) String() string {
	switch k {
	case KindContainerImage:
		return "container-image"
	case KindCloudImage:
		return "cloud-image"
	case KindSnapshot:
		return "snapshot"
	default:
		return "unknown"
	}
}

// Descriptor is the OCI Content Descriptor used by manifests to reference
// blobs (config or layers). Only the fields epoch needs are present.
type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
}

// Title returns the layer's `org.opencontainers.image.title` annotation, or
// the empty string. Used to recover filenames for snapshot tar reassembly and
// to order cloudimg split parts.
func (d Descriptor) Title() string {
	if d.Annotations == nil {
		return ""
	}
	return d.Annotations[AnnotationTitle]
}

// OCIManifest is a parsed OCI 1.1 image manifest. It is intentionally a
// strict subset of the spec — epoch never produces or consumes fields outside
// this surface.
type OCIManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Subject       *Descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Parse decodes raw manifest bytes into an [OCIManifest]. Wraps the JSON
// error with a stable prefix so callers can match cleanly.
func Parse(raw []byte) (*OCIManifest, error) {
	var m OCIManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse oci manifest: %w", err)
	}
	return &m, nil
}

// Classify returns the artifact kind by inspecting the manifest in the
// following order of authority:
//
//  1. Top-level `artifactType` (OCI 1.1) for cocoonstack-specific values.
//  2. `config.mediaType` for plain OCI / Docker container image manifests.
//  3. Top-level `mediaType` for OCI image indexes / Docker manifest lists,
//     which are always container images when no cocoonstack artifactType
//     is set (multi-arch images like ghcr.io/cocoonstack/cocoon/ubuntu:24.04).
//
// The function does not validate the manifest against the OCI spec —
// malformed JSON returns KindUnknown plus the parse error.
func Classify(raw []byte) (Kind, error) {
	var probe struct {
		MediaType    string `json:"mediaType,omitempty"`
		ArtifactType string `json:"artifactType,omitempty"`
		Config       struct {
			MediaType string `json:"mediaType"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return KindUnknown, fmt.Errorf("classify manifest: %w", err)
	}
	return classifyFields(probe.ArtifactType, probe.Config.MediaType, probe.MediaType), nil
}

// ClassifyParsed returns the artifact kind of an already-parsed manifest.
// Callers that have already called [Parse] use this to avoid a second JSON
// unmarshal of the same bytes. The ordering of authority matches [Classify].
func ClassifyParsed(m *OCIManifest) Kind {
	return classifyFields(m.ArtifactType, m.Config.MediaType, m.MediaType)
}

func classifyFields(artifactType, configMediaType, topMediaType string) Kind {
	switch artifactType {
	case ArtifactTypeOSImage, ArtifactTypeWindowsImage:
		return KindCloudImage
	case ArtifactTypeSnapshot:
		return KindSnapshot
	}

	switch configMediaType {
	case MediaTypeOCIImageConfig, MediaTypeDockerConfig:
		return KindContainerImage
	}

	switch topMediaType {
	case MediaTypeOCIIndex, MediaTypeDockerIndex:
		return KindContainerImage
	}

	return KindUnknown
}

// SnapshotConfig is the JSON shape of the config blob referenced by a snapshot
// OCI manifest's `config` descriptor. Wire mediaType: [MediaTypeSnapshotConfig].
//
// The config blob captures the cocoon VM metadata that is too structured for
// annotations (numeric resource values, base image hex IDs). It is written as
// a single content-addressable blob and referenced from the manifest like any
// other OCI config.
type SnapshotConfig struct {
	SchemaVersion string    `json:"schemaVersion"`
	SnapshotID    string    `json:"snapshotId"`
	Image         string    `json:"image,omitempty"`
	CPU           int       `json:"cpu,omitempty"`
	Memory        int64     `json:"memory,omitempty"`
	Storage       int64     `json:"storage,omitempty"`
	NICs          int       `json:"nics,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Catalog is epoch's global index of repositories under `epoch/catalog.json`.
// It is internal to epoch's storage layer; OCI clients use `/v2/_catalog`
// instead.
type Catalog struct {
	Repositories map[string]*Repository `json:"repositories"`
	UpdatedAt    time.Time              `json:"updatedAt"`
}

// Repository tracks the tags currently in use for a single repository name.
// Tag values point at the manifest's storage key under `epoch/manifests/...`.
type Repository struct {
	Tags      map[string]string `json:"tags"`
	UpdatedAt time.Time         `json:"updatedAt"`
}
