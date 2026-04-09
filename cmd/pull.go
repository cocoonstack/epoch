package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/utils"
)

// cocoonBinaryEnv lets users override the cocoon binary used by `epoch pull`
// (defaults to looking up `cocoon` on PATH).
const cocoonBinaryEnv = "EPOCH_COCOON_BINARY"

func newPullCmd() *cobra.Command {
	var (
		overrideName string
		description  string
	)
	cmd := &cobra.Command{
		Use:   "pull <name>[:<tag>]",
		Short: "Pull an artifact from Epoch and import it into local cocoon",
		Long: `Stream an artifact from the Epoch registry directly into the cocoon CLI.

Equivalent to:
  epoch get <name>:<tag> | cocoon snapshot import --name <name>   (snapshots)
  epoch get <name>:<tag> | cocoon image import <name>             (cloud images)

The cocoon binary must be available on PATH (override with $EPOCH_COCOON_BINARY).

Requires EPOCH_SERVER (default http://127.0.0.1:4300) and
EPOCH_REGISTRY_TOKEN environment variables.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := utils.ParseRef(args[0])
			return pullViaCocoon(cmd.Context(), name, tag, overrideName, description)
		},
	}
	cmd.Flags().StringVar(&overrideName, "name", "", "override the local name (snapshot or cloud image)")
	cmd.Flags().StringVar(&description, "description", "", "snapshot description (snapshots only)")
	return cmd
}

func pullViaCocoon(ctx context.Context, name, tag, overrideName, description string) error {
	// Resolve and validate the cocoon binary up front so we fail fast with a
	// clear error before fetching anything from the registry.
	cocoonBin, err := resolveCocoonBinary()
	if err != nil {
		return err
	}

	client := newRegistryClient()
	m, err := fetchManifest(ctx, client, name, tag)
	if err != nil {
		return err
	}

	args := buildCocoonImportArgs(m, name, overrideName, description)
	fmt.Fprintf(os.Stderr, "running: %s %s\n", cocoonBin, strings.Join(args, " "))

	// Cocoon is the authoritative implementation of snapshot/cloud-image import
	// (qcow2 conversion, EROFS layer creation, snapshot DB writes). Importing
	// cocoon as a Go library would couple epoch to its full dependency tree,
	// so we shell out instead. The contract is the public CLI surface
	// `cocoon snapshot import` / `cocoon image import` reading from stdin.
	cocoonCmd := exec.CommandContext(ctx, cocoonBin, args...) //nolint:gosec // cocoonBin was just validated by exec.LookPath
	cocoonCmd.Stdout = os.Stderr
	cocoonCmd.Stderr = os.Stderr
	pipe, err := cocoonCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open cocoon stdin: %w", err)
	}
	if err := cocoonCmd.Start(); err != nil {
		_ = pipe.Close()
		return fmt.Errorf("start %s: %w", cocoonBin, err)
	}

	streamErr := streamArtifactBody(ctx, client, name, m, pipe)
	// Closing the pipe is what unblocks cocoon on the success path (it reads
	// stdin until EOF) and also on the error path (so cocoon's stdin loop
	// exits and Wait can return).
	closeErr := pipe.Close()
	waitErr := cocoonCmd.Wait()

	if err := combineImportErrors(cocoonBin, streamErr, closeErr, waitErr); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\n=== Pulled %s:%s (%s) ===\n", m.Name, m.Tag, utils.HumanSize(m.TotalSize))
	return nil
}

// resolveCocoonBinary picks the cocoon binary to invoke and validates that it
// is reachable. Honors $EPOCH_COCOON_BINARY (after trimming whitespace) and
// otherwise falls back to looking up `cocoon` on PATH.
func resolveCocoonBinary() (string, error) {
	bin := strings.TrimSpace(os.Getenv(cocoonBinaryEnv))
	if bin == "" {
		bin = "cocoon"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("locate cocoon binary %q: %w", bin, err)
	}
	return resolved, nil
}

// buildCocoonImportArgs builds the cocoon CLI arguments for importing the given manifest.
// The local name is `overrideName` when set, otherwise `registryName`. For cloud images
// cocoon takes the local name as a positional arg; for snapshots it takes --name (and
// optional --description) flags.
func buildCocoonImportArgs(m *manifest.Manifest, registryName, overrideName, description string) []string {
	localName := overrideName
	if localName == "" {
		localName = registryName
	}
	if m.IsCloudImage() {
		return []string{"image", "import", localName}
	}
	args := []string{"snapshot", "import", "--name", localName}
	if description != "" {
		args = append(args, "--description", description)
	}
	return args
}

// combineImportErrors prefers the original streaming error over downstream pipe/wait
// errors, since a broken stream is what causes cocoon to fail in the first place.
func combineImportErrors(cocoonBin string, streamErr, closeErr, waitErr error) error {
	switch {
	case streamErr != nil:
		return fmt.Errorf("stream artifact: %w", streamErr)
	case closeErr != nil:
		return fmt.Errorf("close cocoon stdin: %w", closeErr)
	case waitErr != nil:
		return fmt.Errorf("%s import: %w", cocoonBin, waitErr)
	}
	return nil
}
