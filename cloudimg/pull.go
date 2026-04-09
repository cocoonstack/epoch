package cloudimg

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Downloader fetches manifests and blobs from an OCI registry. Defined
// locally (not borrowed from another package) so cloudimg has no
// sibling-package dependency.
type Downloader interface {
	GetManifest(ctx context.Context, name, tag string) ([]byte, string, error)
	GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, error)
}

// CocoonRunner abstracts how Puller invokes the cocoon CLI for image import.
// `*snapshot.ExecCocoon` satisfies this interface via its ImageImport method
// (via the ExecCocoonAdapter helper below).
type CocoonRunner interface {
	ImageImport(ctx context.Context, name string) (io.WriteCloser, func() error, error)
}

// Puller fetches an OCI cloud-image artifact and pipes the assembled disk
// into `cocoon image import`.
type Puller struct {
	Downloader Downloader
	Cocoon     CocoonRunner
}

// PullOptions configures a cloud-image pull.
type PullOptions struct {
	// Name is the OCI repository name. Required.
	Name string
	// Tag is the OCI tag. Defaults to "latest".
	Tag string
	// LocalName overrides the cocoon-side image name on import. Empty
	// means reuse Name.
	LocalName string
}

// Pull downloads a cloud-image artifact and pipes its concatenated disk
// layers into `cocoon image import`.
//
// Streaming errors close the cocoon stdin pipe so the import process exits
// cleanly; the original streaming error wins over the downstream wait error.
func (p *Puller) Pull(ctx context.Context, opts PullOptions) error {
	if opts.Name == "" {
		return errors.New("cloudimg pull: name is required")
	}
	if opts.Tag == "" {
		opts.Tag = "latest"
	}
	localName := opts.LocalName
	if localName == "" {
		localName = opts.Name
	}

	raw, _, err := p.Downloader.GetManifest(ctx, opts.Name, opts.Tag)
	if err != nil {
		return fmt.Errorf("get manifest %s:%s: %w", opts.Name, opts.Tag, err)
	}

	stdin, wait, err := p.Cocoon.ImageImport(ctx, localName)
	if err != nil {
		return fmt.Errorf("start cocoon image import: %w", err)
	}

	streamErr := Stream(ctx, raw, blobReaderAdapter{name: opts.Name, dl: p.Downloader}, stdin)
	closeErr := stdin.Close()
	waitErr := wait()

	switch {
	case streamErr != nil:
		return fmt.Errorf("stream cloudimg: %w", streamErr)
	case closeErr != nil:
		return fmt.Errorf("close cocoon stdin: %w", closeErr)
	case waitErr != nil:
		return fmt.Errorf("cocoon image import: %w", waitErr)
	}
	return nil
}

// blobReaderAdapter binds a snapshot.Downloader (which is repository-aware)
// to the [BlobReader] interface (which is not), so cloudimg.Stream can be
// shared between HTTP-side pull and the in-process /dl/ handler.
type blobReaderAdapter struct {
	name string
	dl   Downloader
}

func (a blobReaderAdapter) ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	return a.dl.GetBlob(ctx, a.name, digest)
}
