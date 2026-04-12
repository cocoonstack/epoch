package manifest

import "strings"

const (
	// OCI / Docker manifest envelopes.
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerIndex    = "application/vnd.docker.distribution.manifest.list.v2+json"

	// OCI / Docker config blob types.
	MediaTypeOCIImageConfig = "application/vnd.oci.image.config.v1+json"
	MediaTypeDockerConfig   = "application/vnd.docker.container.image.v1+json"
	MediaTypeOCIEmpty       = "application/vnd.oci.empty.v1+json"

	// Cocoonstack artifactType discriminators.
	// WindowsImage is the legacy name; epoch recognizes both.
	ArtifactTypeOSImage      = "application/vnd.cocoonstack.os-image.v1+json"
	ArtifactTypeWindowsImage = "application/vnd.cocoonstack.windows-image.v1+json"
	ArtifactTypeSnapshot     = "application/vnd.cocoonstack.snapshot.v1+json"

	MediaTypeSnapshotConfig = "application/vnd.cocoonstack.snapshot.config.v1+json"

	// Disk layer media types. *Part variants are for split disks (GHCR per-layer limit).
	MediaTypeDiskQcow2     = "application/vnd.cocoonstack.disk.qcow2"
	MediaTypeDiskQcow2Part = "application/vnd.cocoonstack.disk.qcow2.part"
	MediaTypeDiskRaw       = "application/vnd.cocoonstack.disk.raw"
	MediaTypeDiskRawPart   = "application/vnd.cocoonstack.disk.raw.part"

	// Legacy windows-specific disk media types.
	MediaTypeWindowsDiskQcow2     = "application/vnd.cocoonstack.windows.disk.qcow2"
	MediaTypeWindowsDiskQcow2Part = "application/vnd.cocoonstack.windows.disk.qcow2.part"
	MediaTypeWindowsDiskRaw       = "application/vnd.cocoonstack.windows.disk.raw"
	MediaTypeWindowsDiskRawPart   = "application/vnd.cocoonstack.windows.disk.raw.part"

	// Snapshot-specific layer media types.
	MediaTypeVMConfig = "application/vnd.cocoonstack.vm.config+json"
	MediaTypeVMState  = "application/vnd.cocoonstack.vm.state+json"
	MediaTypeVMMemory = "application/vnd.cocoonstack.vm.memory"
	MediaTypeVMCidata = "application/vnd.cocoonstack.vm.cidata"

	MediaTypeGeneric = "application/octet-stream"
	MediaTypeTar     = "application/x-tar"

	// OCI standard annotation keys.
	AnnotationTitle       = "org.opencontainers.image.title"
	AnnotationCreated     = "org.opencontainers.image.created"
	AnnotationSource      = "org.opencontainers.image.source"
	AnnotationRevision    = "org.opencontainers.image.revision"
	AnnotationDescription = "org.opencontainers.image.description"

	// Cocoonstack annotation keys.
	AnnotationSnapshotID        = "cocoonstack.snapshot.id"
	AnnotationSnapshotBaseImage = "cocoonstack.snapshot.baseimage"
)

var snapshotFilenameMediaType = map[string]string{
	"config.json":   MediaTypeVMConfig,
	"state.json":    MediaTypeVMState,
	"memory-ranges": MediaTypeVMMemory,
	"cidata.img":    MediaTypeVMCidata,
	"overlay.qcow2": MediaTypeDiskQcow2,
}

// MediaTypeForCocoonFile returns the layer mediaType for a cocoon snapshot tar file.
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

// IsDiskMediaType reports whether mt is a disk layer mediaType.
func IsDiskMediaType(mt string) bool {
	switch mt {
	case MediaTypeDiskQcow2, MediaTypeDiskQcow2Part,
		MediaTypeDiskRaw, MediaTypeDiskRawPart,
		MediaTypeWindowsDiskQcow2, MediaTypeWindowsDiskQcow2Part,
		MediaTypeWindowsDiskRaw, MediaTypeWindowsDiskRawPart:
		return true
	}
	return false
}
