// Package manifest defines OCI manifest types and artifact classification.
package manifest

import (
	"encoding/json"
	"fmt"
	"time"
)

type Kind int

const (
	KindUnknown Kind = iota
	KindContainerImage
	KindCloudImage
	KindSnapshot
	KindImageIndex
)

func (k Kind) String() string {
	switch k {
	case KindContainerImage:
		return "container-image"
	case KindCloudImage:
		return "cloud-image"
	case KindSnapshot:
		return "snapshot"
	case KindImageIndex:
		return "image-index"
	default:
		return "unknown"
	}
}

type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
}

func (d Descriptor) Title() string {
	if d.Annotations == nil {
		return ""
	}
	return d.Annotations[AnnotationTitle]
}

// OCIManifest represents both OCI image manifests and image indexes.
type OCIManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Manifests     []IndexManifest   `json:"manifests,omitempty"`
	Subject       *Descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type IndexManifest struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Platform  *Platform `json:"platform,omitempty"`
}

type Platform struct {
	Architecture string `json:"architecture,omitempty"`
	OS           string `json:"os,omitempty"`
	OSVersion    string `json:"os.version,omitempty"`
	Variant      string `json:"variant,omitempty"`
}

func Parse(raw []byte) (*OCIManifest, error) {
	var m OCIManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse oci manifest: %w", err)
	}
	return &m, nil
}

// Classify returns the artifact kind from raw manifest JSON.
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

// ClassifyParsed classifies an already-parsed manifest.
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
		return KindImageIndex
	}

	return KindUnknown
}

type SnapshotFile struct {
	Mode       int64  `json:"mode,omitempty"`
	SparseMap  string `json:"sparseMap,omitempty"`
	SparseSize int64  `json:"sparseSize,omitempty"`
}

// SnapshotConfig is the OCI config blob for snapshot manifests.
type SnapshotConfig struct {
	SchemaVersion string                  `json:"schemaVersion"`
	SnapshotID    string                  `json:"snapshotId"`
	Description   string                  `json:"description,omitempty"`
	Image         string                  `json:"image,omitempty"`
	ImageBlobIDs  map[string]struct{}     `json:"imageBlobIds,omitempty"`
	Hypervisor    string                  `json:"hypervisor,omitempty"`
	CPU           int                     `json:"cpu,omitempty"`
	Memory        int64                   `json:"memory,omitempty"`
	Storage       int64                   `json:"storage,omitempty"`
	NICs          int                     `json:"nics,omitempty"`
	Network       string                  `json:"network,omitempty"`
	Windows       bool                    `json:"windows,omitempty"`
	Files         map[string]SnapshotFile `json:"files,omitempty"`
	CreatedAt     time.Time               `json:"createdAt"`
}

type Catalog struct {
	Repositories map[string]*Repository `json:"repositories"`
	UpdatedAt    time.Time              `json:"updatedAt"`
}

type Repository struct {
	Tags      map[string]string `json:"tags"`
	UpdatedAt time.Time         `json:"updatedAt"`
}
