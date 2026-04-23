package snapshot

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/utils"
)

// Puller downloads snapshot artifacts and pipes them into cocoon snapshot import.
type Puller struct {
	Downloader Downloader
	Cocoon     CocoonRunner
}

// PullOptions configures a snapshot pull operation.
type PullOptions struct {
	Name        string
	Tag         string
	LocalName   string // overrides cocoon-side snapshot name; empty = use Name
	Description string
	Progress    func(string)
}

// Pull downloads a snapshot artifact and feeds it to `cocoon snapshot import`.
func (p *Puller) Pull(ctx context.Context, opts PullOptions) error {
	if opts.Name == "" {
		return errors.New("snapshot pull: name is required")
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

	stdin, wait, err := p.Cocoon.Import(ctx, ImportOptions{
		Name:        localName,
		Description: opts.Description,
	})
	if err != nil {
		return fmt.Errorf("start cocoon snapshot import: %w", err)
	}

	streamErr := Stream(ctx, raw, p.Downloader, StreamOptions{
		Name:      opts.Name,
		LocalName: localName,
		Writer:    stdin,
		Progress:  opts.Progress,
	})
	closeErr := stdin.Close()
	waitErr := wait()

	switch {
	case streamErr != nil:
		return fmt.Errorf("stream snapshot: %w", streamErr)
	case closeErr != nil:
		return fmt.Errorf("close cocoon stdin: %w", closeErr)
	case waitErr != nil:
		return fmt.Errorf("cocoon snapshot import: %w", waitErr)
	}
	return nil
}

// StreamOptions configures snapshot tar stream assembly.
type StreamOptions struct {
	Name      string
	LocalName string // empty = use Name
	Writer    io.Writer
	Progress  func(string)
}

// Stream reassembles a snapshot manifest into a cocoon-import tar stream.
func Stream(ctx context.Context, raw []byte, dl Downloader, opts StreamOptions) error {
	if opts.Name == "" {
		return errors.New("snapshot stream: name is required")
	}
	if opts.Writer == nil {
		return errors.New("snapshot stream: writer is required")
	}

	m, err := manifest.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if kind := manifest.ClassifyParsed(m); kind != manifest.KindSnapshot {
		return fmt.Errorf("manifest is %s, not a snapshot", kind)
	}

	return StreamParsed(ctx, m, dl, opts)
}

// StreamParsed accepts an already-parsed manifest.
func StreamParsed(ctx context.Context, m *manifest.OCIManifest, dl Downloader, opts StreamOptions) error {
	if opts.Name == "" {
		return errors.New("snapshot stream: name is required")
	}
	if opts.Writer == nil {
		return errors.New("snapshot stream: writer is required")
	}
	localName := opts.LocalName
	if localName == "" {
		localName = opts.Name
	}

	cfg, err := FetchSnapshotConfig(ctx, dl, opts.Name, m.Config)
	if err != nil {
		return fmt.Errorf("fetch snapshot config: %w", err)
	}

	return writeImportTar(ctx, dl, opts.Name, localName, cfg, m.Layers, opts.Writer, opts.Progress)
}

// FetchSnapshotConfig downloads and parses the snapshot config blob.
func FetchSnapshotConfig(ctx context.Context, dl Downloader, name string, desc manifest.Descriptor) (*manifest.SnapshotConfig, error) {
	if desc.MediaType != manifest.MediaTypeSnapshotConfig {
		return nil, fmt.Errorf("unexpected config mediaType %q", desc.MediaType)
	}
	body, err := dl.GetBlob(ctx, name, desc.Digest)
	if err != nil {
		return nil, fmt.Errorf("get config blob %s: %w", desc.Digest, err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20)) // config blob is tiny
	if err != nil {
		return nil, fmt.Errorf("read config blob: %w", err)
	}
	var cfg manifest.SnapshotConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse snapshot config: %w", err)
	}
	return &cfg, nil
}

func writeImportTar(ctx context.Context, dl Downloader, name, localName string, cfg *manifest.SnapshotConfig, layers []manifest.Descriptor, w io.Writer, progress func(string)) error {
	bw := bufio.NewWriterSize(w, 256<<10)
	tw := tar.NewWriter(bw)

	now := nowFunc()
	envelope := snapshotExportEnvelope{
		Version: 1,
		Config: snapshotExportConfig{
			ID:           cfg.SnapshotID,
			Name:         localName,
			Description:  cfg.Description,
			Image:        cfg.Image,
			ImageDigest:  cfg.ImageDigest,
			ImageType:    cfg.ImageType,
			ImageBlobIDs: cfg.ImageBlobIDs,
			Hypervisor:   cfg.Hypervisor,
			CPU:          cfg.CPU,
			Memory:       cfg.Memory,
			Storage:      cfg.Storage,
			NICs:         cfg.NICs,
			Network:      cfg.Network,
			Windows:      cfg.Windows,
		},
	}
	envelopeJSON, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot envelope: %w", err)
	}
	envelopeJSON = append(envelopeJSON, '\n')

	if err := writeTarFile(tw, snapshotJSONName, envelopeJSON, 0o644, now); err != nil {
		return fmt.Errorf("write snapshot envelope: %w", err)
	}

	for _, layer := range layers {
		title := layer.Title()
		if title == "" {
			return fmt.Errorf("layer %s missing %s annotation", layer.Digest, manifest.AnnotationTitle)
		}
		if progress != nil {
			progress(fmt.Sprintf("  %s (%d bytes)", title, layer.Size))
		}
		var fileMeta manifest.SnapshotFile
		if cfg.Files != nil {
			fileMeta = cfg.Files[title]
		}
		if err := streamLayerToTar(ctx, dl, name, layer, fileMeta, tw, now); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	return bw.Flush()
}

func streamLayerToTar(ctx context.Context, dl Downloader, name string, layer manifest.Descriptor, fileMeta manifest.SnapshotFile, tw *tar.Writer, modTime time.Time) error {
	mode := fileMeta.Mode
	if mode == 0 {
		mode = 0o640
	}
	hdr := &tar.Header{
		Name:    layer.Title(),
		Size:    layer.Size,
		Mode:    mode,
		ModTime: modTime,
	}
	if fileMeta.SparseMap != "" {
		if fileMeta.SparseSize <= 0 {
			return fmt.Errorf("layer %s has sparse map without sparse size", layer.Digest)
		}
		hdr.PAXRecords = map[string]string{
			sparsePAXMap:  fileMeta.SparseMap,
			sparsePAXSize: strconv.FormatInt(fileMeta.SparseSize, 10),
		}
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	body, err := dl.GetBlob(ctx, name, layer.Digest)
	if err != nil {
		return fmt.Errorf("get blob %s: %w", layer.Digest, err)
	}
	defer func() { _ = body.Close() }()
	return utils.CopyBlobExact(tw, body, layer.Digest, layer.Size)
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mode int64, modTime time.Time) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:    name,
		Size:    int64(len(data)),
		Mode:    mode,
		ModTime: modTime,
	}); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write tar body %s: %w", name, err)
	}
	return nil
}
