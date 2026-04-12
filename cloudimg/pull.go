package cloudimg

import (
	"context"
	"errors"
	"fmt"
	"io"
)

type Downloader interface {
	GetManifest(ctx context.Context, name, tag string) ([]byte, string, error)
	GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, error)
}

type CocoonRunner interface {
	ImageImport(ctx context.Context, name string) (io.WriteCloser, func() error, error)
}

type Puller struct {
	Downloader Downloader
	Cocoon     CocoonRunner
}

type PullOptions struct {
	Name      string // OCI repository name. Required.
	Tag       string // Defaults to "latest".
	LocalName string // Override the cocoon-side image name. Empty = use Name.
}

// Pull downloads a cloud-image artifact and pipes it into `cocoon image import`.
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

type blobReaderAdapter struct {
	name string
	dl   Downloader
}

func (a blobReaderAdapter) ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	return a.dl.GetBlob(ctx, a.name, digest)
}
