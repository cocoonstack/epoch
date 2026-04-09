// Package snapshot pushes and pulls cocoon VM snapshots as OCI artifacts.
//
// A snapshot in epoch is an OCI 1.1 image manifest with artifactType
// `application/vnd.cocoonstack.snapshot.v1+json`. The manifest carries:
//
//   - A config blob (mediaType vnd.cocoonstack.snapshot.config.v1+json)
//     containing structured cocoon VM metadata (cpu, memory, image ref, ...).
//   - One layer blob per file inside `cocoon snapshot export -o -` tar
//     output, with the OCI standard `org.opencontainers.image.title`
//     annotation set to the original filename so the puller can reassemble
//     the tar and feed it to `cocoon snapshot import`.
//   - Optional `cocoonstack.snapshot.baseimage` annotation when the operator
//     supplies one with `epoch push --base-image <ref>`.
//
// epoch never reads cocoon's filesystem directly. Push streams the snapshot
// from `cocoon snapshot export <name> -o -` stdout; pull writes back via
// `cocoon snapshot import <name>` stdin. The cocoon binary is the only point
// of contact between epoch and cocoon's local storage layout.
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
	// CocoonBinaryEnv lets users override the cocoon binary used by epoch's
	// snapshot push/pull (defaults to looking up `cocoon` on $PATH).
	CocoonBinaryEnv = "EPOCH_COCOON_BINARY"

	// snapshotJSONName is the conventional first tar entry produced by
	// `cocoon snapshot export`. It carries the cocoon snapshot envelope
	// metadata that we translate into the OCI manifest config blob.
	snapshotJSONName = "snapshot.json"
)

// errMissingSnapshotJSON is returned by Pusher.Push when the cocoon export
// tar does not contain a snapshot.json envelope.
var errMissingSnapshotJSON = errors.New("snapshot.json not found in export stream")

// nowFunc is the time source for snapshot push. Tests override it.
var nowFunc = time.Now

// Uploader is the registry-side surface needed by [Pusher]. The HTTP
// `registryclient.Client` satisfies this interface; an in-process
// implementation could too.
type Uploader interface {
	BlobExists(ctx context.Context, name, digest string) (bool, error)
	PutBlob(ctx context.Context, name, digest string, body io.Reader, size int64) error
	PutManifest(ctx context.Context, name, tag string, data []byte, contentType string) error
}

// Downloader is the registry-side surface needed by [Puller].
type Downloader interface {
	GetManifest(ctx context.Context, name, tag string) ([]byte, string, error)
	GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, error)
}

// CocoonRunner abstracts how snapshot.Pusher and snapshot.Puller talk to
// the local `cocoon` CLI. The default implementation [ExecCocoon] runs
// `cocoon` as a subprocess; tests can substitute a fake.
type CocoonRunner interface {
	// Export runs `cocoon snapshot export <name> -o -` and returns a reader
	// for the resulting tar stream plus a wait function the caller invokes
	// once the stream is fully consumed.
	Export(ctx context.Context, name string) (io.ReadCloser, func() error, error)

	// Import runs `cocoon snapshot import <name>` (with optional --description)
	// and returns a writer for the tar stream plus a wait function. The caller
	// closes the writer to signal end-of-stream and then calls wait.
	Import(ctx context.Context, opts ImportOptions) (io.WriteCloser, func() error, error)
}

// ImportOptions configures `cocoon snapshot import`.
type ImportOptions struct {
	Name        string
	Description string
}

// ExecCocoon is the default [CocoonRunner] backed by an actual `cocoon`
// binary on $PATH (or the path in $EPOCH_COCOON_BINARY).
type ExecCocoon struct {
	// Binary is the resolved cocoon binary path. Use [ResolveCocoonBinary]
	// to populate it from $EPOCH_COCOON_BINARY or $PATH.
	Binary string
	// Stderr receives the cocoon subprocess stderr. Defaults to os.Stderr
	// when nil.
	Stderr io.Writer
}

// ResolveCocoonBinary picks the cocoon binary path from $EPOCH_COCOON_BINARY,
// falling back to looking up `cocoon` on $PATH. Returns an error if neither
// is reachable.
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

// Export runs `cocoon snapshot export <name> -o -` and returns its stdout
// stream and a wait function.
func (e *ExecCocoon) Export(ctx context.Context, name string) (io.ReadCloser, func() error, error) {
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

// Import runs `cocoon snapshot import <name>` and returns its stdin pipe and
// a wait function. The caller writes the tar stream, then closes the pipe,
// then calls wait.
func (e *ExecCocoon) Import(ctx context.Context, opts ImportOptions) (io.WriteCloser, func() error, error) {
	args := []string{"snapshot", "import", "--name", opts.Name}
	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}
	return e.startWithStdinPipe(ctx, args, "cocoon snapshot import")
}

// ImageImport runs `cocoon image import <name>` and returns its stdin pipe
// and a wait function. Used by the cloudimg package; lives here so the
// snapshot package owns the single `cocoon` exec helper.
func (e *ExecCocoon) ImageImport(ctx context.Context, name string) (io.WriteCloser, func() error, error) {
	return e.startWithStdinPipe(ctx, []string{"image", "import", name}, "cocoon image import")
}

func (e *ExecCocoon) startWithStdinPipe(ctx context.Context, args []string, label string) (io.WriteCloser, func() error, error) {
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

// snapshotExportEnvelope mirrors cocoon's `types.SnapshotExport` JSON shape.
// Only the fields epoch needs are present; the rest are dropped.
type snapshotExportEnvelope struct {
	Version int                  `json:"version"`
	Config  snapshotExportConfig `json:"config"`
}

// snapshotExportConfig mirrors cocoon's `types.SnapshotConfig`.
type snapshotExportConfig struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Image   string `json:"image,omitempty"`
	CPU     int    `json:"cpu,omitempty"`
	Memory  int64  `json:"memory,omitempty"`
	Storage int64  `json:"storage,omitempty"`
	NICs    int    `json:"nics,omitempty"`
}
