package cmd

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registryclient"
	"github.com/cocoonstack/epoch/utils"
)

func newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>[:<tag>]",
		Short: "Stream a snapshot or cloud image to stdout",
		Long: `Stream an artifact from the Epoch registry to stdout.

For snapshots: outputs a raw tar archive compatible with
  cocoon snapshot import (auto-detects gzip/raw)

For cloud images: outputs raw qcow2 data compatible with
  cocoon image import (auto-detects format)

Progress information is written to stderr.

Examples:
  epoch get myvm:v1 | cocoon snapshot import --name myvm
  epoch get ubuntu-base:latest | cocoon image import ubuntu-base`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, tag := utils.ParseRef(args[0])
			client := newRegistryClient()
			m, err := fetchManifest(ctx, client, name, tag)
			if err != nil {
				return err
			}
			return streamArtifactBody(ctx, client, name, m, os.Stdout)
		},
	}
}

// fetchManifest downloads the manifest for name:tag and logs a one-line header to stderr.
func fetchManifest(ctx context.Context, client *registryclient.Client, name, tag string) (*manifest.Manifest, error) {
	fmt.Fprintf(os.Stderr, "fetching manifest %s:%s from %s ...\n", name, tag, client.BaseURL())
	m, err := client.GetManifest(ctx, name, tag)
	if err != nil {
		return nil, fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
	}
	return m, nil
}

// streamArtifactBody writes the manifest body to w, picking the format based on the
// manifest type: a tar archive for snapshots, raw qcow2 bytes for cloud images.
func streamArtifactBody(ctx context.Context, client *registryclient.Client, name string, m *manifest.Manifest, w io.Writer) error {
	if m.IsCloudImage() {
		fmt.Fprintf(os.Stderr, "type: cloud image (%d layers, %s)\n", len(m.Layers), utils.HumanSize(m.TotalSize))
		return streamCloudImage(ctx, client, name, m, w)
	}
	fmt.Fprintf(os.Stderr, "type: snapshot (%d layers, %s)\n", len(m.Layers), utils.HumanSize(m.TotalSize))
	return streamSnapshot(ctx, client, name, m, w)
}

// streamSnapshot writes a raw tar archive to w,
// compatible with cocoon snapshot import (auto-detects gzip/raw).
func streamSnapshot(ctx context.Context, client *registryclient.Client, name string, m *manifest.Manifest, w io.Writer) error {
	blobIDs := make(map[string]struct{}, len(m.ImageBlobIDs))
	for k := range m.ImageBlobIDs {
		blobIDs[k] = struct{}{}
	}

	envelope := snapshotExport{
		Version: 1,
		Config: snapshotConfig{
			ID:           m.SnapshotID,
			Name:         name,
			Image:        m.Image,
			ImageBlobIDs: blobIDs,
			CPU:          m.CPU,
			Memory:       m.Memory,
			Storage:      m.Storage,
			NICs:         m.NICs,
		},
	}
	jsonData, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot metadata: %w", err)
	}
	jsonData = append(jsonData, '\n')

	now := time.Now()
	bw := bufio.NewWriterSize(w, 256<<10)
	tw := tar.NewWriter(bw)

	if err := tw.WriteHeader(&tar.Header{
		Name:    "snapshot.json",
		Size:    int64(len(jsonData)),
		Mode:    0o644,
		ModTime: now,
	}); err != nil {
		return fmt.Errorf("write snapshot.json header: %w", err)
	}
	if _, err := tw.Write(jsonData); err != nil {
		return fmt.Errorf("write snapshot.json: %w", err)
	}

	for _, layer := range m.Layers {
		fmt.Fprintf(os.Stderr, "  streaming %s (%s)...\n", layer.Filename, utils.HumanSize(layer.Size))

		if err := streamBlobToTar(ctx, client, name, layer, tw, now); err != nil {
			return fmt.Errorf("stream %s: %w", layer.Filename, err)
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush output: %w", err)
	}

	fmt.Fprintf(os.Stderr, "done: %s:%s (%s)\n", name, m.Tag, utils.HumanSize(m.TotalSize))
	return nil
}

// streamCloudImage writes raw qcow2 data to w,
// compatible with cocoon image import (auto-detects format).
//
// No bufio wrapper here on purpose: cloud image layers can be tens of GB, and
// io.Copy from an HTTP body to an *os.File destination (e.g. an exec stdin
// pipe) takes the splice fast-path on Linux only when the destination is the
// raw fd, not a wrapped buffered writer.
func streamCloudImage(ctx context.Context, client *registryclient.Client, name string, m *manifest.Manifest, w io.Writer) error {
	for _, layer := range m.Layers {
		fmt.Fprintf(os.Stderr, "  streaming %s (%s)...\n", layer.Filename, utils.HumanSize(layer.Size))

		body, err := client.GetBlob(ctx, name, layer.Digest)
		if err != nil {
			return fmt.Errorf("get blob %s: %w", layer.Filename, err)
		}
		if _, copyErr := io.Copy(w, body); copyErr != nil {
			body.Close() //nolint:errcheck,gosec
			return fmt.Errorf("stream %s: %w", layer.Filename, copyErr)
		}
		body.Close() //nolint:errcheck,gosec
	}

	fmt.Fprintf(os.Stderr, "done: %s:%s (%s)\n", name, m.Tag, utils.HumanSize(m.TotalSize))
	return nil
}

// streamBlobToTar downloads a blob from the registry and writes it as a tar entry.
func streamBlobToTar(ctx context.Context, client *registryclient.Client, name string, layer manifest.Layer, tw *tar.Writer, modTime time.Time) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:    layer.Filename,
		Size:    layer.Size,
		Mode:    0o640,
		ModTime: modTime,
	}); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}

	body, err := client.GetBlob(ctx, name, layer.Digest)
	if err != nil {
		return fmt.Errorf("get blob: %w", err)
	}
	defer body.Close() //nolint:errcheck

	if _, err = io.Copy(tw, body); err != nil {
		return fmt.Errorf("copy blob data: %w", err)
	}
	return nil
}

// snapshotExport matches cocoon's types.SnapshotExport for the snapshot.json envelope.
type snapshotExport struct {
	Version int            `json:"version"`
	Config  snapshotConfig `json:"config"`
}

// snapshotConfig matches cocoon's types.SnapshotConfig.
// Keep in sync with cocoon/types/snapshot.go.
type snapshotConfig struct {
	ID           string              `json:"id,omitempty"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Image        string              `json:"image,omitempty"`
	ImageBlobIDs map[string]struct{} `json:"image_blob_ids,omitempty"`
	CPU          int                 `json:"cpu,omitempty"`
	Memory       int64               `json:"memory,omitempty"`
	Storage      int64               `json:"storage,omitempty"`
	NICs         int                 `json:"nics,omitempty"`
	Network      string              `json:"network,omitempty"`
	Windows      bool                `json:"windows,omitempty"`
}
