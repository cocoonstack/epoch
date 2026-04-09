package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/utils"
)

// Pusher uploads cocoon VM snapshots into an OCI registry.
type Pusher struct {
	Uploader Uploader
	Cocoon   CocoonRunner
}

// PushOptions configures a snapshot push.
type PushOptions struct {
	// Name is the OCI repository name. Required.
	Name string
	// Tag is the OCI tag. Defaults to "latest".
	Tag string
	// BaseImage is the optional cocoonstack.snapshot.baseimage annotation
	// value (an OCI ref like "ghcr.io/cocoonstack/cocoon/ubuntu:24.04").
	// When empty the annotation is omitted.
	BaseImage string
	// Source identifies the producer in the manifest annotations
	// (org.opencontainers.image.source). Optional.
	Source string
	// Revision is the org.opencontainers.image.revision annotation. Optional.
	Revision string
	// Progress receives one-line status updates. May be nil.
	Progress func(string)
}

// PushResult summarizes a successful snapshot push.
type PushResult struct {
	Name           string
	Tag            string
	ManifestDigest string // sha256:<hex>
	ManifestBytes  []byte
	TotalSize      int64
	LayerCount     int
}

// Push streams a snapshot from `cocoon snapshot export` and writes an OCI
// snapshot artifact to the registry.
//
// The data flow is:
//  1. start `cocoon snapshot export <name> -o -`
//  2. read tar entries from its stdout, hashing each one to a temp file and
//     uploading as a content-addressable blob
//  3. translate the snapshot.json envelope into a snapshot config blob
//     (mediaType vnd.cocoonstack.snapshot.config.v1+json) and upload it
//  4. assemble an OCI image manifest with artifactType
//     vnd.cocoonstack.snapshot.v1+json and PUT it under name:tag
func (p *Pusher) Push(ctx context.Context, opts PushOptions) (*PushResult, error) {
	if opts.Name == "" {
		return nil, errors.New("snapshot push: name is required")
	}
	if opts.Tag == "" {
		opts.Tag = "latest"
	}

	stream, wait, err := p.Cocoon.Export(ctx, opts.Name)
	if err != nil {
		return nil, err
	}

	cfg, layers, readErr := p.readAndUploadEntries(ctx, opts.Name, stream, opts.Progress)
	// Close the read side of the export pipe BEFORE waiting on the
	// subprocess. If readAndUploadEntries bailed out mid-tar, the cocoon
	// child is still trying to write to its stdout pipe; closing our read
	// side gives it a SIGPIPE-style EOF on next write so cmd.Wait() can
	// return instead of deadlocking. The same close is harmless on the
	// success path because the reader has already drained to EOF.
	_ = stream.Close()
	waitErr := wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	if cfg == nil {
		return nil, errMissingSnapshotJSON
	}

	configDescriptor, err := p.uploadSnapshotConfig(ctx, opts.Name, cfg)
	if err != nil {
		return nil, fmt.Errorf("upload snapshot config: %w", err)
	}

	manifestBytes, err := buildSnapshotManifest(configDescriptor, layers, opts)
	if err != nil {
		return nil, fmt.Errorf("build manifest: %w", err)
	}

	if err := p.Uploader.PutManifest(ctx, opts.Name, opts.Tag, manifestBytes, manifest.MediaTypeOCIManifest); err != nil {
		return nil, fmt.Errorf("put manifest %s:%s: %w", opts.Name, opts.Tag, err)
	}

	manifestDigest := "sha256:" + utils.SHA256Hex(manifestBytes)

	var totalSize int64
	for _, l := range layers {
		totalSize += l.Size
	}
	totalSize += configDescriptor.Size

	return &PushResult{
		Name:           opts.Name,
		Tag:            opts.Tag,
		ManifestDigest: manifestDigest,
		ManifestBytes:  manifestBytes,
		TotalSize:      totalSize,
		LayerCount:     len(layers),
	}, nil
}

// readAndUploadEntries walks the cocoon export tar, parsing snapshot.json
// into cfg and uploading every other entry as a blob descriptor.
func (p *Pusher) readAndUploadEntries(ctx context.Context, name string, r io.Reader, progress func(string)) (*snapshotExportConfig, []manifest.Descriptor, error) {
	tr := tar.NewReader(r)
	var (
		cfg    *snapshotExportConfig
		layers []manifest.Descriptor
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read tar entry: %w", err)
		}

		if hdr.Name == snapshotJSONName {
			var envelope snapshotExportEnvelope
			if decErr := json.NewDecoder(tr).Decode(&envelope); decErr != nil {
				return nil, nil, fmt.Errorf("parse snapshot.json: %w", decErr)
			}
			cfg = &envelope.Config
			continue
		}

		// Only regular files become OCI blob layers. cocoon snapshot
		// export shouldn't emit directories or symlinks today, but
		// guard against future changes that would otherwise produce
		// zero-byte phantom layers in the manifest. tar.TypeReg covers
		// both modern (TypeReg) and legacy GNU/POSIX-1988 (formerly
		// TypeRegA) regular file flags since Go 1.11 normalized them.
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size < 0 {
			return nil, nil, fmt.Errorf("tar entry %s has negative size %d", hdr.Name, hdr.Size)
		}

		desc, uploadErr := p.uploadTarEntry(ctx, name, hdr, tr)
		if uploadErr != nil {
			return nil, nil, fmt.Errorf("upload %s: %w", hdr.Name, uploadErr)
		}
		layers = append(layers, desc)
		if progress != nil {
			progress(fmt.Sprintf("  %s -> %s (%d bytes)", hdr.Name, desc.Digest, desc.Size))
		}
	}

	return cfg, layers, nil
}

// uploadTarEntry spools a single tar entry to a temp file while hashing,
// then uploads it via the registry's blob endpoint with dedup. The returned
// descriptor is what goes into the OCI manifest's `layers` array.
func (p *Pusher) uploadTarEntry(ctx context.Context, name string, hdr *tar.Header, body io.Reader) (manifest.Descriptor, error) {
	tmp, err := os.CreateTemp("", "epoch-snapshot-*")
	if err != nil {
		return manifest.Descriptor{}, fmt.Errorf("create temp: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, hdr.Size))
	if err != nil {
		return manifest.Descriptor{}, fmt.Errorf("buffer entry: %w", err)
	}

	digestHex := hex.EncodeToString(h.Sum(nil))
	digest := "sha256:" + digestHex

	exists, existsErr := p.Uploader.BlobExists(ctx, name, digest)
	if existsErr != nil {
		return manifest.Descriptor{}, fmt.Errorf("check blob %s: %w", digest, existsErr)
	}
	if !exists {
		if _, seekErr := tmp.Seek(0, io.SeekStart); seekErr != nil {
			return manifest.Descriptor{}, fmt.Errorf("seek temp: %w", seekErr)
		}
		if err := p.Uploader.PutBlob(ctx, name, digest, tmp, written); err != nil {
			return manifest.Descriptor{}, err
		}
	}

	return manifest.Descriptor{
		MediaType:   manifest.MediaTypeForCocoonFile(hdr.Name),
		Digest:      digest,
		Size:        written,
		Annotations: map[string]string{manifest.AnnotationTitle: hdr.Name},
	}, nil
}

// uploadSnapshotConfig builds the snapshot config blob from the cocoon
// envelope and uploads it. Returns a descriptor suitable for the manifest's
// `config` field.
func (p *Pusher) uploadSnapshotConfig(ctx context.Context, name string, cfg *snapshotExportConfig) (manifest.Descriptor, error) {
	cfgBlob := manifest.SnapshotConfig{
		SchemaVersion: "v1",
		SnapshotID:    cfg.ID,
		Image:         cfg.Image,
		CPU:           cfg.CPU,
		Memory:        cfg.Memory,
		Storage:       cfg.Storage,
		NICs:          cfg.NICs,
		CreatedAt:     nowFunc().UTC(),
	}
	data, err := json.Marshal(cfgBlob)
	if err != nil {
		return manifest.Descriptor{}, err
	}

	digest := "sha256:" + utils.SHA256Hex(data)
	exists, existsErr := p.Uploader.BlobExists(ctx, name, digest)
	if existsErr != nil {
		return manifest.Descriptor{}, fmt.Errorf("check config blob %s: %w", digest, existsErr)
	}
	if !exists {
		if err := p.Uploader.PutBlob(ctx, name, digest, bytes.NewReader(data), int64(len(data))); err != nil {
			return manifest.Descriptor{}, err
		}
	}
	return manifest.Descriptor{
		MediaType: manifest.MediaTypeSnapshotConfig,
		Digest:    digest,
		Size:      int64(len(data)),
	}, nil
}

// buildSnapshotManifest assembles the final OCI manifest JSON for a snapshot
// push. It populates the optional cocoonstack.snapshot.* annotations from
// opts and the snapshot config.
func buildSnapshotManifest(config manifest.Descriptor, layers []manifest.Descriptor, opts PushOptions) ([]byte, error) {
	annotations := map[string]string{
		manifest.AnnotationCreated: nowFunc().UTC().Format(time.RFC3339),
	}
	if opts.BaseImage != "" {
		annotations[manifest.AnnotationSnapshotBaseImage] = opts.BaseImage
	}
	if opts.Source != "" {
		annotations[manifest.AnnotationSource] = opts.Source
	}
	if opts.Revision != "" {
		annotations[manifest.AnnotationRevision] = opts.Revision
	}

	m := manifest.OCIManifest{
		SchemaVersion: 2,
		MediaType:     manifest.MediaTypeOCIManifest,
		ArtifactType:  manifest.ArtifactTypeSnapshot,
		Config:        config,
		Layers:        layers,
		Annotations:   annotations,
	}
	return json.MarshalIndent(m, "", "  ")
}
