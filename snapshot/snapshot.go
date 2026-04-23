// Package snapshot pushes and pulls cocoon VM snapshots as OCI artifacts.
// Push streams from `cocoon snapshot export`; pull writes to `cocoon snapshot import`.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	// CocoonBinaryEnv is the env var that overrides the cocoon binary path.
	CocoonBinaryEnv  = "EPOCH_COCOON_BINARY"
	snapshotJSONName = "snapshot.json"

	// cocoon uses custom PAX keys for sparse files; epoch preserves them.
	sparsePAXMap  = "COCOON.sparse.map"
	sparsePAXSize = "COCOON.sparse.size"
)

var (
	errMissingSnapshotJSON = errors.New("snapshot.json not found in export stream")

	nowFunc = time.Now // tests override
)

// Uploader abstracts OCI blob and manifest uploads.
type Uploader interface {
	BlobExists(ctx context.Context, name, digest string) (bool, error)
	PutBlob(ctx context.Context, name, digest string, body io.Reader, size int64) error
	PutManifest(ctx context.Context, name, tag string, data []byte, contentType string) error
}

// Downloader abstracts OCI manifest and blob downloads.
type Downloader interface {
	GetManifest(ctx context.Context, name, tag string) ([]byte, string, error)
	GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, error)
}

// CocoonRunner abstracts the local `cocoon` CLI. Default is ExecCocoon; tests substitute a fake.
type CocoonRunner interface {
	Export(ctx context.Context, name string) (io.ReadCloser, func() error, error)
	Import(ctx context.Context, opts ImportOptions) (io.WriteCloser, func() error, error)
}

// ImportOptions configures a cocoon snapshot import invocation.
type ImportOptions struct {
	Name        string
	Description string
}

// ExecCocoon runs the cocoon binary as a subprocess.
type ExecCocoon struct {
	Binary string
	Stderr io.Writer
}

// ResolveCocoonBinary finds the cocoon binary on PATH.
func ResolveCocoonBinary(envValue string) (string, error) {
	bin := strings.TrimSpace(envValue)
	if bin == "" {
		bin = "cocoon"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("locate cocoon binary %q: %w", bin, err)
	}
	return resolved, nil
}

// Export streams a snapshot out of cocoon via `cocoon snapshot export`.
func (e *ExecCocoon) Export(ctx context.Context, name string) (io.ReadCloser, func() error, error) {
	// cocoon CLI is the authoritative implementation for snapshot export; no Go library equivalent exists.
	cmd := exec.CommandContext(ctx, e.Binary, "snapshot", "export", name, "-o", "-") //nolint:gosec // Binary was validated by ResolveCocoonBinary
	cmd.Stderr = e.stderr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start cocoon snapshot export: %w", err)
	}
	return stdout, func() error {
		if waitErr := cmd.Wait(); waitErr != nil {
			return fmt.Errorf("cocoon snapshot export: %w", waitErr)
		}
		return nil
	}, nil
}

// Import starts a `cocoon snapshot import` subprocess accepting tar on stdin.
func (e *ExecCocoon) Import(ctx context.Context, opts ImportOptions) (io.WriteCloser, func() error, error) {
	args := []string{"snapshot", "import", "--name", opts.Name}
	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}
	return e.startWithStdinPipe(ctx, args, "cocoon snapshot import")
}

// ImageImport starts a `cocoon image import` subprocess accepting data on stdin.
func (e *ExecCocoon) ImageImport(ctx context.Context, name string) (io.WriteCloser, func() error, error) {
	return e.startWithStdinPipe(ctx, []string{"image", "import", name}, "cocoon image import")
}

func (e *ExecCocoon) startWithStdinPipe(ctx context.Context, args []string, label string) (io.WriteCloser, func() error, error) {
	// cocoon CLI is the authoritative implementation for snapshot/image import; no Go library equivalent exists.
	cmd := exec.CommandContext(ctx, e.Binary, args...) //nolint:gosec // Binary was validated by ResolveCocoonBinary
	cmd.Stdout = e.stderr()
	cmd.Stderr = e.stderr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, nil, fmt.Errorf("start %s: %w", label, err)
	}
	return stdin, func() error {
		if waitErr := cmd.Wait(); waitErr != nil {
			return fmt.Errorf("%s: %w", label, waitErr)
		}
		return nil
	}, nil
}

func (e *ExecCocoon) stderr() io.Writer {
	if e.Stderr == nil {
		return io.Discard
	}
	return e.Stderr
}

type snapshotExportEnvelope struct {
	Version int                  `json:"version"`
	Config  snapshotExportConfig `json:"config"`
}

type snapshotExportConfig struct {
	ID           string              `json:"id,omitempty"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Image        string              `json:"image,omitempty"`
	ImageDigest  string              `json:"image_digest,omitempty"`
	ImageType    string              `json:"image_type,omitempty"`
	ImageBlobIDs map[string]struct{} `json:"image_blob_ids,omitempty"`
	Hypervisor   string              `json:"hypervisor,omitempty"`
	CPU          int                 `json:"cpu,omitempty"`
	Memory       int64               `json:"memory,omitempty"`
	Storage      int64               `json:"storage,omitempty"`
	NICs         int                 `json:"nics,omitempty"`
	Network      string              `json:"network,omitempty"`
	Windows      bool                `json:"windows,omitempty"`
}
