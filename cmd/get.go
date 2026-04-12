package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/cloudimg"
	"github.com/cocoonstack/epoch/manifest"
	"github.com/cocoonstack/epoch/registryclient"
	"github.com/cocoonstack/epoch/utils"
)

func newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>[:<tag>]",
		Short: "Stream a cloud image's raw bytes to stdout",
		Long: `Stream the assembled disk bytes of a cocoonstack cloud image to stdout.

Snapshots cannot be streamed via "epoch get" because they are not a single
contiguous artifact — use "epoch pull" instead, which pipes them through
"cocoon snapshot import".

Container images are not consumable by cocoon and are rejected — use
oras / crane / docker directly.

Progress is written to stderr; the data goes to stdout for piping.

Examples:
  epoch get windows/win11:25h2 | cocoon image import win11
  ssh registry-host epoch get cocoon/ubuntu:24.04 | cocoon image import ubuntu`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, tag := utils.ParseRef(args[0])

			client := newRegistryClient()
			raw, _, err := client.GetManifest(ctx, name, tag)
			if err != nil {
				return fmt.Errorf("get manifest %s:%s: %w", name, tag, err)
			}
			kind, err := manifest.Classify(raw)
			if err != nil {
				return fmt.Errorf("classify manifest %s:%s: %w", name, tag, err)
			}
			if kind != manifest.KindCloudImage {
				return fmt.Errorf("manifest %s:%s is %s; only cloud images can be streamed via `epoch get`", name, tag, kind)
			}
			fmt.Fprintf(os.Stderr, "streaming cloud image %s:%s ...\n", name, tag)
			return cloudimg.Stream(ctx, raw, &httpBlobReader{client: client, name: name}, os.Stdout)
		},
	}
}

type httpBlobReader struct {
	client *registryclient.Client
	name   string
}

// ReadBlob downloads a blob by digest from the remote registry.
func (h *httpBlobReader) ReadBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	return h.client.GetBlob(ctx, h.name, digest)
}
