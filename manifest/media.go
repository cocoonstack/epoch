package manifest

import "strings"

// Media types for Cocoon snapshot layers.
const (
	MediaTypeDiskQcow2 = "application/vnd.cocoon.disk.qcow2"
	MediaTypeDiskRaw   = "application/vnd.cocoon.disk.raw"
	MediaTypeMemory    = "application/vnd.cocoon.memory"
	MediaTypeCidata    = "application/vnd.cocoon.cidata"
	MediaTypeConfig    = "application/vnd.cocoon.config+json"
	MediaTypeState     = "application/vnd.cocoon.state+json"
	MediaTypeGeneric   = "application/octet-stream"
)

// MediaTypeForFile returns the media type based on filename.
func MediaTypeForFile(name string) string {
	switch {
	case name == "overlay.qcow2":
		return MediaTypeDiskQcow2
	case name == "cow.raw":
		return MediaTypeDiskRaw
	case name == "memory-ranges":
		return MediaTypeMemory
	case name == "cidata.img":
		return MediaTypeCidata
	case name == "config.json":
		return MediaTypeConfig
	case name == "state.json":
		return MediaTypeState
	case strings.HasSuffix(name, ".qcow2"):
		return MediaTypeDiskQcow2
	default:
		return MediaTypeGeneric
	}
}
