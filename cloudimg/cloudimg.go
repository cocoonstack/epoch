// Package cloudimg streams OCI-packaged cloud images out of an Epoch registry.
package cloudimg

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/utils"
)

// BlobReader abstracts reading a blob by digest.
type BlobReader interface {
	ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error)
}

// Stream concatenates disk layers sorted by title annotation.
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

// StreamParsed streams disk layers from an already-parsed manifest.
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

func diskLayers(layers []manifest.Descriptor) []manifest.Descriptor {
	out := make([]manifest.Descriptor, 0, len(layers))
	for _, l := range layers {
		if manifest.IsDiskMediaType(l.MediaType) {
			out = append(out, l)
		}
	}
	slices.SortStableFunc(out, func(a, b manifest.Descriptor) int {
		return cmp.Compare(a.Title(), b.Title())
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
