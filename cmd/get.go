package cmd

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
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

For snapshots: outputs a gzip-compressed tar archive compatible with
  cocoon snapshot import

For cloud images: outputs gzip-compressed qcow2 data compatible with
  cocoon image import

Progress information is written to stderr.

Examples:
  epoch get myvm:v1 | cocoon snapshot import --name myvm
  epoch get ubuntu-base:latest | cocoon image import ubuntu-base`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := utils.ParseRef(args[0])
			return streamArtifact(cmd.Context(), name, tag, os.Stdout)
		},
	}
}

func streamArtifact(ctx context.Context, name, tag string, w io.Writer) error {
	client := newRegistryClient()

	fmt.Fprintf(os.Stderr, "fetching manifest %s:%s from %s ...\n", name, tag, client.BaseURL())

	m, err := client.GetManifest(ctx, name, tag)
	if err != nil {
		return fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
	}

	if m.IsCloudImage() {
		fmt.Fprintf(os.Stderr, "type: cloud image (%d layers, %s)\n", len(m.Layers), utils.HumanSize(m.TotalSize))
		return streamCloudImage(ctx, client, name, m, w)
	}

	fmt.Fprintf(os.Stderr, "type: snapshot (%d layers, %s)\n", len(m.Layers), utils.HumanSize(m.TotalSize))
	return streamSnapshot(ctx, client, name, m, w)
}

// streamSnapshot writes a gzip-compressed tar archive to w,
// compatible with cocoon snapshot import.
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
	gw, _ := gzip.NewWriterLevel(bw, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

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
	if err := gw.Close(); err != nil {
		return fmt.Errorf("close gzip: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush output: %w", err)
	}

	fmt.Fprintf(os.Stderr, "done: %s:%s (%s)\n", name, m.Tag, utils.HumanSize(m.TotalSize))
	return nil
}

// streamCloudImage writes gzip-compressed qcow2 data to w,
// compatible with cocoon image import (auto-detects gzip + qcow2 magic).
func streamCloudImage(ctx context.Context, client *registryclient.Client, name string, m *manifest.Manifest, w io.Writer) error {
	bw := bufio.NewWriterSize(w, 256<<10)
	gw, _ := gzip.NewWriterLevel(bw, gzip.BestSpeed)

	for _, layer := range m.Layers {
		fmt.Fprintf(os.Stderr, "  streaming %s (%s)...\n", layer.Filename, utils.HumanSize(layer.Size))

		body, err := client.GetBlob(ctx, name, layer.Digest)
		if err != nil {
			return fmt.Errorf("get blob %s: %w", layer.Filename, err)
		}
		if _, copyErr := io.Copy(gw, body); copyErr != nil {
			body.Close() //nolint:errcheck,gosec
			return fmt.Errorf("stream %s: %w", layer.Filename, copyErr)
		}
		body.Close() //nolint:errcheck,gosec
	}

	if err := gw.Close(); err != nil {
		return fmt.Errorf("close gzip: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush output: %w", err)
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

	_, err = io.Copy(tw, body)
	return err
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
