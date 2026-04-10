package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/snapshot"
	"github.com/cocoonstack/epoch/utils"
)

func newPushCmd() *cobra.Command {
	var (
		tag       string
		baseImage string
	)

	cmd := &cobra.Command{
		Use:   "push <snapshot-name>",
		Short: "Push a Cocoon snapshot to Epoch as an OCI artifact",
		Long: `Stream a snapshot out of cocoon via "cocoon snapshot export -o -" and upload
it to the Epoch registry as an OCI 1.1 artifact with artifactType
application/vnd.cocoonstack.snapshot.v1+json.

The cocoon binary must be available on $PATH (override with $EPOCH_COCOON_BINARY).

Requires EPOCH_SERVER (default http://127.0.0.1:8080) and EPOCH_REGISTRY_TOKEN
environment variables.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			cocoonBin, err := snapshot.ResolveCocoonBinary(os.Getenv(snapshot.CocoonBinaryEnv))
			if err != nil {
				return err
			}

			client := newRegistryClient()
			pusher := &snapshot.Pusher{
				Uploader: client,
				Cocoon:   &snapshot.ExecCocoon{Binary: cocoonBin, Stderr: os.Stderr},
			}

			fmt.Fprintf(os.Stderr, "pushing %s:%s to %s ...\n", name, tag, client.BaseURL())
			result, err := pusher.Push(ctx, snapshot.PushOptions{
				Name:      name,
				Tag:       tag,
				BaseImage: baseImage,
				Progress: func(line string) {
					fmt.Fprintln(os.Stderr, line)
				},
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "\n=== Pushed %s:%s ===\n", name, tag)
			fmt.Fprintf(os.Stderr, "  layers:     %d\n", result.LayerCount)
			fmt.Fprintf(os.Stderr, "  total-size: %s\n", utils.HumanSize(result.TotalSize))
			fmt.Fprintf(os.Stderr, "  digest:     %s\n", result.ManifestDigest)
			return nil
		},
	}

	cmd.Flags().StringVarP(&tag, "tag", "t", "latest", "OCI tag")
	cmd.Flags().StringVar(&baseImage, "base-image", "", "OCI ref of the base image this snapshot was created from (optional, sets cocoonstack.snapshot.baseimage annotation)")
	return cmd
}
