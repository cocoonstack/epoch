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
	"strconv"
	"time"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/utils"
)

// Pusher exports and uploads cocoon snapshots as OCI artifacts.
type Pusher struct {
	Uploader Uploader
	Cocoon   CocoonRunner
}

// PushOptions configures a snapshot push operation.
type PushOptions struct {
	Name      string
	Tag       string
	BaseImage string // optional cocoonstack.snapshot.baseimage annotation
	Source    string
	Revision  string
	Progress  func(string)
}

// PushResult contains the outcome of a successful push.
type PushResult struct {
	Name           string
	Tag            string
	ManifestDigest string // sha256:<hex>
	ManifestBytes  []byte
	TotalSize      int64
	LayerCount     int
}

// Push exports a snapshot via cocoon and uploads it as an OCI artifact.
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

	cfg, files, layers, readErr := p.readAndUploadEntries(ctx, opts.Name, stream, opts.Progress)
	// Close before wait so mid-tar failures unblock the subprocess.
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

	configDescriptor, err := p.uploadSnapshotConfig(ctx, opts.Name, cfg, files)
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

func (p *Pusher) readAndUploadEntries(ctx context.Context, name string, r io.Reader, progress func(string)) (*snapshotExportConfig, map[string]manifest.SnapshotFile, []manifest.Descriptor, error) {
	tr := tar.NewReader(r)
	var (
		cfg    *snapshotExportConfig
		files  = map[string]manifest.SnapshotFile{}
		layers []manifest.Descriptor
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read tar entry: %w", err)
		}

		if hdr.Name == snapshotJSONName {
			var envelope snapshotExportEnvelope
			if decErr := json.NewDecoder(tr).Decode(&envelope); decErr != nil {
				return nil, nil, nil, fmt.Errorf("parse snapshot.json: %w", decErr)
			}
			cfg = &envelope.Config
			continue
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size < 0 {
			return nil, nil, nil, fmt.Errorf("tar entry %s has negative size %d", hdr.Name, hdr.Size)
		}

		desc, fileMeta, uploadErr := p.uploadTarEntry(ctx, name, hdr, tr)
		if uploadErr != nil {
			return nil, nil, nil, fmt.Errorf("upload %s: %w", hdr.Name, uploadErr)
		}
		files[hdr.Name] = fileMeta
		layers = append(layers, desc)
		if progress != nil {
			progress(fmt.Sprintf("  %s -> %s (%d bytes)", hdr.Name, desc.Digest, desc.Size))
		}
	}

	return cfg, files, layers, nil
}

func (p *Pusher) uploadTarEntry(ctx context.Context, name string, hdr *tar.Header, body io.Reader) (manifest.Descriptor, manifest.SnapshotFile, error) {
	tmp, err := os.CreateTemp("", "epoch-snapshot-*")
	if err != nil {
		return manifest.Descriptor{}, manifest.SnapshotFile{}, fmt.Errorf("create temp: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, hdr.Size))
	if err != nil {
		return manifest.Descriptor{}, manifest.SnapshotFile{}, fmt.Errorf("buffer entry: %w", err)
	}

	digestHex := hex.EncodeToString(h.Sum(nil))
	digest := "sha256:" + digestHex

	exists, existsErr := p.Uploader.BlobExists(ctx, name, digest)
	if existsErr != nil {
		return manifest.Descriptor{}, manifest.SnapshotFile{}, fmt.Errorf("check blob %s: %w", digest, existsErr)
	}
	if !exists {
		if _, seekErr := tmp.Seek(0, io.SeekStart); seekErr != nil {
			return manifest.Descriptor{}, manifest.SnapshotFile{}, fmt.Errorf("seek temp: %w", seekErr)
		}
		if err := p.Uploader.PutBlob(ctx, name, digest, tmp, written); err != nil {
			return manifest.Descriptor{}, manifest.SnapshotFile{}, err
		}
	}

	fileMeta := manifest.SnapshotFile{Mode: hdr.Mode}
	sparseMap, ok := hdr.PAXRecords[sparsePAXMap]
	if ok {
		fileMeta.SparseMap = sparseMap
		rawSize, ok := hdr.PAXRecords[sparsePAXSize]
		if !ok {
			return manifest.Descriptor{}, manifest.SnapshotFile{}, fmt.Errorf("sparse entry %s missing %s", hdr.Name, sparsePAXSize)
		}
		sparseSize, parseErr := strconv.ParseInt(rawSize, 10, 64)
		if parseErr != nil {
			return manifest.Descriptor{}, manifest.SnapshotFile{}, fmt.Errorf("parse sparse size for %s: %w", hdr.Name, parseErr)
		}
		fileMeta.SparseSize = sparseSize
	}

	return manifest.Descriptor{
		MediaType:   manifest.MediaTypeForCocoonFile(hdr.Name),
		Digest:      digest,
		Size:        written,
		Annotations: map[string]string{manifest.AnnotationTitle: hdr.Name},
	}, fileMeta, nil
}

func (p *Pusher) uploadSnapshotConfig(ctx context.Context, name string, cfg *snapshotExportConfig, files map[string]manifest.SnapshotFile) (manifest.Descriptor, error) {
	cfgBlob := manifest.SnapshotConfig{
		SchemaVersion: "v1",
		SnapshotID:    cfg.ID,
		Description:   cfg.Description,
		Image:         cfg.Image,
		ImageDigest:   cfg.ImageDigest,
		ImageType:     cfg.ImageType,
		ImageBlobIDs:  cfg.ImageBlobIDs,
		Hypervisor:    cfg.Hypervisor,
		CPU:           cfg.CPU,
		Memory:        cfg.Memory,
		Storage:       cfg.Storage,
		NICs:          cfg.NICs,
		Network:       cfg.Network,
		Windows:       cfg.Windows,
		Files:         files,
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
