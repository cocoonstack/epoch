package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/registry"
	"github.com/cocoonstack/epoch/server"
	"github.com/cocoonstack/epoch/store"
)

func newServeCmd() *cobra.Command {
	var (
		addr string
		dsn  string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Epoch HTTP server (registry V2 + control plane)",
		Long: `Start the Epoch HTTP server that provides:
  - /v2/ — OCI Distribution-shaped pull API (manifests + blob streaming)
  - /api/ — Control plane API backed by MySQL
  - /    — Web UI for browsing repositories and tags

Requires a MySQL database. Start one with:
  cd deploy && docker compose up -d`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			reg, err := registry.NewFromEnv()
			if err != nil {
				return fmt.Errorf("init registry: %w", err)
			}

			db, err := store.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("init mysql: %w", err)
			}
			defer func() { _ = db.Close() }()

			srv := server.New(ctx, reg, db, addr)
			return srv.ListenAndServe(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "Listen address (host:port)")
	cmd.Flags().StringVar(&dsn, "dsn", "epoch:epoch@tcp(127.0.0.1:3306)/epoch?parseTime=true", "MySQL DSN")
	return cmd
}
