package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/cloudimg"
	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/snapshot"
	"github.com/cocoonstack/epoch/utils"
)

func newPullCmd() *cobra.Command {
	var (
		overrideName string
		description  string
	)
	cmd := &cobra.Command{
		Use:   "pull <name>[:<tag>]",
		Short: "Pull a snapshot or cloud image from Epoch into cocoon",
		Long: `Fetch an OCI artifact from Epoch and pipe it into the cocoon CLI.

The artifact's classification (vnd.cocoonstack.snapshot.v1+json or
vnd.cocoonstack.os-image.v1+json) decides the import path:

  snapshot     → cocoon snapshot import --name <name>
  cloud image  → cocoon image import <name>

Container images are not consumable by cocoon and are rejected — pull them
with oras / crane / docker directly.

The cocoon binary must be available on $PATH (override with $EPOCH_COCOON_BINARY).
Requires EPOCH_SERVER and EPOCH_REGISTRY_TOKEN environment variables.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, tag := utils.ParseRef(args[0])

			cocoonBin, err := snapshot.ResolveCocoonBinary(os.Getenv(snapshot.CocoonBinaryEnv))
			if err != nil {
				return err
			}
			client := newRegistryClient()
			cocoon := &snapshot.ExecCocoon{Binary: cocoonBin, Stderr: os.Stderr}

			raw, _, err := client.GetManifest(ctx, name, tag)
			if err != nil {
				return fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
			}
			kind, err := manifest.Classify(raw)
			if err != nil {
				return fmt.Errorf("classify manifest %s:%s: %w", name, tag, err)
			}

			switch kind {
			case manifest.KindSnapshot:
				puller := &snapshot.Puller{Downloader: client, Cocoon: cocoon}
				return puller.Pull(ctx, snapshot.PullOptions{
					Name:        name,
					Tag:         tag,
					LocalName:   overrideName,
					Description: description,
					Progress: func(line string) {
						fmt.Fprintln(os.Stderr, line)
					},
				})

			case manifest.KindCloudImage:
				puller := &cloudimg.Puller{Downloader: client, Cocoon: cocoon}
				return puller.Pull(ctx, cloudimg.PullOptions{
					Name:      name,
					Tag:       tag,
					LocalName: overrideName,
				})

			case manifest.KindContainerImage:
				return fmt.Errorf("manifest %s:%s is a container image; pull with oras/crane/docker, then `cocoon image pull`", name, tag)

			default:
				return fmt.Errorf("manifest %s:%s has unknown artifact kind", name, tag)
			}
		},
	}
	cmd.Flags().StringVar(&overrideName, "name", "", "override the local cocoon name")
	cmd.Flags().StringVar(&description, "description", "", "snapshot description (snapshot pulls only)")
	return cmd
}
