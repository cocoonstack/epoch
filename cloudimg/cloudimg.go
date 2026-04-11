// Package cloudimg streams OCI-packaged cloud images out of an Epoch
// registry and into `cocoon image import`.
//
// A cloud image in epoch is an OCI 1.1 image manifest with artifactType
// `application/vnd.cocoonstack.os-image.v1+json`. Layers carry one or more
// disk parts (mediaType vnd.cocoonstack.disk.qcow2 or .raw, including the
// `.part` split variants the windows builder uses). Non-disk layers like
// `text/plain` SHA256SUMS are skipped on stream.
//
// Pushing cloud images to epoch is the upstream publisher's job —
// `oras push` / `crane copy` from cocoonstack/windows or cocoonstack/cocoon
// CI workflows. epoch only handles the read side: streaming the assembled
// disk to cocoon (or to stdout for `epoch get`).
package cloudimg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/utils"
)

// BlobReader is the minimal blob-fetch interface needed by [Stream]. Both
// the in-process registry and an HTTP registry client implement it via
// adapter types defined by their respective callers.
type BlobReader interface {
	// ReadBlob fetches a blob by its OCI digest (with sha256: prefix).
	ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error)
}

// Stream concatenates the disk layers of a cloud image manifest to w in the
// order required for reassembly: layers with disk mediaType are sorted by
// their `org.opencontainers.image.title` annotation lexicographically (which
// is what `split -d --additional-suffix` already produces). Layers with
// non-disk mediaTypes (e.g. text/plain SHA256SUMS) are skipped.
//
// Stream writes directly to w with no buffering, so a destination that
// supports splice (raw os.File, exec stdin pipe, http.ResponseWriter) gets
// the zero-copy fast path. Cloud image disks can be tens of GiB; the buffer
// avoidance is intentional.
//
// Stream returns an error if the manifest is not classified as a cloud image
// or contains zero disk layers.
func Stream(ctx context.Context, raw []byte, blobs BlobReader, w io.Writer) error {
	m, err := manifest.Parse(raw)
	if err != nil {
		return err
	}
	if kind := manifest.ClassifyParsed(m); kind != manifest.KindCloudImage {
		return fmt.Errorf("manifest is %s, not a cloud image", kind)
	}

	return StreamParsed(ctx, m, blobs, w)
}

// StreamParsed is the same as [Stream] but accepts an already-parsed manifest.
// Callers that have already classified and parsed (e.g. the /dl/ handler) use
// this to avoid a redundant JSON unmarshal on multi-GiB download hot paths.
func StreamParsed(ctx context.Context, m *manifest.OCIManifest, blobs BlobReader, w io.Writer) error {
	disks := diskLayers(m.Layers)
	if len(disks) == 0 {
		return errors.New("cloud image manifest has no disk layers")
	}

	for _, layer := range disks {
		if err := copyBlob(ctx, blobs, layer, w); err != nil {
			return err
		}
	}
	return nil
}

// diskLayers filters and sorts a manifest's layers, returning only the disk
// mediaTypes in title-lexicographic order.
func diskLayers(layers []manifest.Descriptor) []manifest.Descriptor {
	out := make([]manifest.Descriptor, 0, len(layers))
	for _, l := range layers {
		if manifest.IsDiskMediaType(l.MediaType) {
			out = append(out, l)
		}
	}
	slices.SortStableFunc(out, func(a, b manifest.Descriptor) int {
		return strings.Compare(a.Title(), b.Title())
	})
	return out
}

func copyBlob(ctx context.Context, blobs BlobReader, layer manifest.Descriptor, w io.Writer) error {
	body, err := blobs.ReadBlob(ctx, layer.Digest)
	if err != nil {
		return fmt.Errorf("get blob %s: %w", layer.Digest, err)
	}
	defer func() { _ = body.Close() }()
	return utils.CopyBlobExact(w, body, layer.Digest, layer.Size)
}
