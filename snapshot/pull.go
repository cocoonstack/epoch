package snapshot

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/cocoonstack/epoch/manifest"
)

// Puller fetches a snapshot OCI artifact and pipes it into
// `cocoon snapshot import`.
type Puller struct {
	Downloader Downloader
	Cocoon     CocoonRunner
}

// PullOptions configures a snapshot pull.
type PullOptions struct {
	// Name is the OCI repository name. Required.
	Name string
	// Tag is the OCI tag. Defaults to "latest".
	Tag string
	// LocalName overrides the cocoon-side snapshot name on import.
	// Empty means reuse Name.
	LocalName string
	// Description is forwarded to `cocoon snapshot import --description`.
	Description string
	// Progress receives one-line status updates. May be nil.
	Progress func(string)
}

// Pull downloads a snapshot artifact from the registry, reassembles its
// layers into a tar stream, and feeds it to `cocoon snapshot import`.
//
// Errors during streaming close the cocoon stdin pipe so the import process
// observes EOF and exits cleanly; the original streaming error wins over the
// downstream wait error.
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

// StreamOptions configures [Stream].
type StreamOptions struct {
	// Name is the OCI repository name used to fetch layer blobs. Required.
	Name string
	// LocalName overrides the snapshot name written into the rebuilt
	// snapshot.json envelope. Empty falls back to Name so the importer
	// reuses the registry repository name.
	LocalName string
	// Writer is the destination for the cocoon-import-shaped tar stream.
	// Required.
	Writer io.Writer
	// Progress receives one-line status updates per layer. May be nil.
	Progress func(string)
}

// Stream classifies the manifest as a snapshot, fetches its config blob, and
// writes the cocoon-import tar (snapshot.json envelope + every layer entry)
// to opts.Writer. It is the function shared between snapshot.Puller (which
// writes to cocoon's stdin) and epoch's server-side /dl/{name} handler
// (which writes to the HTTP response body).
//
// Stream does not invoke cocoon — it only reassembles the tar. The caller is
// responsible for piping the bytes to wherever they should land.
func Stream(ctx context.Context, raw []byte, dl Downloader, opts StreamOptions) error {
	if opts.Name == "" {
		return errors.New("snapshot stream: Name is required")
	}
	if opts.Writer == nil {
		return errors.New("snapshot stream: Writer is required")
	}

	kind, err := manifest.Classify(raw)
	if err != nil {
		return fmt.Errorf("classify manifest: %w", err)
	}
	if kind != manifest.KindSnapshot {
		return fmt.Errorf("manifest is %s, not a snapshot", kind)
	}

	m, err := manifest.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	return StreamParsed(ctx, m, dl, opts)
}

// StreamParsed is the same as [Stream] but accepts an already-parsed manifest.
// Callers that have already classified and parsed the manifest (e.g. the /dl/
// handler) use this to avoid a redundant JSON unmarshal.
func StreamParsed(ctx context.Context, m *manifest.OCIManifest, dl Downloader, opts StreamOptions) error {
	if opts.Name == "" {
		return errors.New("snapshot stream: Name is required")
	}
	if opts.Writer == nil {
		return errors.New("snapshot stream: Writer is required")
	}
	localName := opts.LocalName
	if localName == "" {
		localName = opts.Name
	}

	cfg, err := fetchSnapshotConfig(ctx, dl, opts.Name, m.Config)
	if err != nil {
		return fmt.Errorf("fetch snapshot config: %w", err)
	}

	return writeImportTar(ctx, dl, opts.Name, localName, cfg, m.Layers, opts.Writer, opts.Progress)
}

// writeImportTar serializes a snapshot to the cocoon-import tar layout:
// snapshot.json envelope first, then one tar entry per OCI layer in
// manifest order with the layer's title annotation as the entry name.
func writeImportTar(ctx context.Context, dl Downloader, name, localName string, cfg *manifest.SnapshotConfig, layers []manifest.Descriptor, w io.Writer, progress func(string)) error {
	bw := bufio.NewWriterSize(w, 256<<10)
	tw := tar.NewWriter(bw)

	now := nowFunc()
	envelope := snapshotExportEnvelope{
		Version: 1,
		Config: snapshotExportConfig{
			ID:      cfg.SnapshotID,
			Name:    localName,
			Image:   cfg.Image,
			CPU:     cfg.CPU,
			Memory:  cfg.Memory,
			Storage: cfg.Storage,
			NICs:    cfg.NICs,
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
		if err := streamLayerToTar(ctx, dl, name, layer, tw, now); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	return bw.Flush()
}

// fetchSnapshotConfig downloads and parses the snapshot config blob.
func fetchSnapshotConfig(ctx context.Context, dl Downloader, name string, desc manifest.Descriptor) (*manifest.SnapshotConfig, error) {
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

// streamLayerToTar fetches a layer blob and writes it as a tar entry whose
// name is the layer's `org.opencontainers.image.title` annotation. The tar
// header advertises layer.Size, so the body MUST match exactly: too few
// bytes corrupts the tar stream cocoon reads, too many means the registry
// served the wrong blob.
func streamLayerToTar(ctx context.Context, dl Downloader, name string, layer manifest.Descriptor, tw *tar.Writer, modTime time.Time) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:    layer.Title(),
		Size:    layer.Size,
		Mode:    0o640,
		ModTime: modTime,
	}); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	body, err := dl.GetBlob(ctx, name, layer.Digest)
	if err != nil {
		return fmt.Errorf("get blob %s: %w", layer.Digest, err)
	}
	defer func() { _ = body.Close() }()
	written, err := io.CopyN(tw, body, layer.Size)
	if err != nil {
		return fmt.Errorf("copy blob %s: %w", layer.Digest, err)
	}
	// Drain any extra bytes the registry sent so the connection can be
	// reused; if there are extras, fail loudly because the manifest size
	// is the source of truth.
	if extra, _ := io.Copy(io.Discard, body); extra > 0 {
		return fmt.Errorf("blob %s longer than manifest size %d (got %d extra)", layer.Digest, layer.Size, extra)
	}
	if written != layer.Size {
		return fmt.Errorf("blob %s shorter than manifest size %d (got %d)", layer.Digest, layer.Size, written)
	}
	return nil
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
