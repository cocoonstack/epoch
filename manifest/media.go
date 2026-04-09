package manifest

import "strings"

// Cocoonstack and OCI media type / artifactType / annotation constants used
// across epoch. They are grouped by purpose, not by occurrence.
const (
	// --- OCI / Docker standard manifest envelopes ---

	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerIndex    = "application/vnd.docker.distribution.manifest.list.v2+json"

	// --- OCI / Docker config blob types (used to recognize container images) ---

	MediaTypeOCIImageConfig = "application/vnd.oci.image.config.v1+json"
	MediaTypeDockerConfig   = "application/vnd.docker.container.image.v1+json"
	MediaTypeOCIEmpty       = "application/vnd.oci.empty.v1+json"

	// --- Cocoonstack artifactType discriminators ---
	//
	// These appear in the top-level `artifactType` field of an OCI 1.1 image
	// manifest and are how epoch decides whether an artifact is a cloud image
	// or a VM snapshot. A manifest without one of these is treated as a
	// regular container image (or unknown).

	ArtifactTypeOSImage  = "application/vnd.cocoonstack.os-image.v1+json"
	ArtifactTypeSnapshot = "application/vnd.cocoonstack.snapshot.v1+json"

	// --- Snapshot config blob (the OCI manifest's config field, not a layer) ---

	MediaTypeSnapshotConfig = "application/vnd.cocoonstack.snapshot.config.v1+json"

	// --- Disk layer media types (shared between cloudimg and snapshot) ---
	//
	// `*Part` variants are used by `oras push` for split disks (the windows
	// builder publishes ~1.9 GiB chunks to stay under GHCR's per-layer limit).

	MediaTypeDiskQcow2     = "application/vnd.cocoonstack.disk.qcow2"
	MediaTypeDiskQcow2Part = "application/vnd.cocoonstack.disk.qcow2.part"
	MediaTypeDiskRaw       = "application/vnd.cocoonstack.disk.raw"
	MediaTypeDiskRawPart   = "application/vnd.cocoonstack.disk.raw.part"

	// --- Snapshot-specific layer media types ---
	//
	// One mediaType per file inside `cocoon snapshot export -o -` tar output.
	// `vm.*` (not `snapshot.*`) so the cocoonstack snapshot.config.v1+json
	// stays unambiguous as the manifest config blob name.

	MediaTypeVMConfig = "application/vnd.cocoonstack.vm.config+json"
	MediaTypeVMState  = "application/vnd.cocoonstack.vm.state+json"
	MediaTypeVMMemory = "application/vnd.cocoonstack.vm.memory"
	MediaTypeVMCidata = "application/vnd.cocoonstack.vm.cidata"

	MediaTypeGeneric = "application/octet-stream"

	// --- OCI standard annotation keys ---

	AnnotationTitle       = "org.opencontainers.image.title"
	AnnotationCreated     = "org.opencontainers.image.created"
	AnnotationSource      = "org.opencontainers.image.source"
	AnnotationRevision    = "org.opencontainers.image.revision"
	AnnotationDescription = "org.opencontainers.image.description"

	// --- Cocoonstack annotation keys ---

	AnnotationSnapshotID        = "cocoonstack.snapshot.id"
	AnnotationSnapshotBaseImage = "cocoonstack.snapshot.baseimage"
)

// snapshotFilenameMediaType maps a cocoon snapshot tar entry filename to its
// canonical layer mediaType. Used by snapshot.Pusher when writing the OCI
// manifest, and by snapshot.Puller as a sanity check on incoming layers.
var snapshotFilenameMediaType = map[string]string{
	"config.json":   MediaTypeVMConfig,
	"state.json":    MediaTypeVMState,
	"memory-ranges": MediaTypeVMMemory,
	"cidata.img":    MediaTypeVMCidata,
	"overlay.qcow2": MediaTypeDiskQcow2,
}

// MediaTypeForCocoonFile returns the OCI layer mediaType for a file inside
// a cocoon snapshot tar. Unknown filenames fall back to a qcow2/raw suffix
// match, then to MediaTypeGeneric.
func MediaTypeForCocoonFile(name string) string {
	if mt, ok := snapshotFilenameMediaType[name]; ok {
		return mt
	}
	switch {
	case strings.HasSuffix(name, ".qcow2"):
		return MediaTypeDiskQcow2
	case strings.HasSuffix(name, ".raw"):
		return MediaTypeDiskRaw
	}
	return MediaTypeGeneric
}

// IsDiskMediaType reports whether mt is a cocoonstack disk layer mediaType
// (qcow2/raw, whole or split). Used by cloudimg.Stream to decide which layers
// to concatenate.
func IsDiskMediaType(mt string) bool {
	switch mt {
	case MediaTypeDiskQcow2, MediaTypeDiskQcow2Part,
		MediaTypeDiskRaw, MediaTypeDiskRawPart:
		return true
	}
	return false
}
