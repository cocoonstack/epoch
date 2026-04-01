package cmd

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/epoch/cocoon"
)

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls [name]",
		Aliases: []string{"list"},
		Short:   "List snapshots in the Epoch registry",
		Long:    `Without arguments, lists all repositories. With a name, lists all tags for that repository.`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 1 {
				return lsTags(ctx, args[0])
			}
			return lsRepos(ctx)
		},
	}
}

type repoSummary struct {
	Name      string `json:"name"`
	TagCount  int    `json:"tagCount"`
	TotalSize int64  `json:"totalSize"`
}

func lsRepos(ctx context.Context) error {
	var repos []repoSummary
	if err := apiGet(ctx, "/repositories", &repos); err != nil {
		return err
	}
	if len(repos) == 0 {
		fmt.Println("No snapshots in registry")
		return nil
	}
	slices.SortFunc(repos, func(a, b repoSummary) int { return cmp.Compare(a.Name, b.Name) })
	fmt.Printf("%-35s  %-6s  %s\n", "REPOSITORY", "TAGS", "SIZE")
	fmt.Println(strings.Repeat("-", 55))
	for _, r := range repos {
		fmt.Printf("%-35s  %-6d  %s\n", r.Name, r.TagCount, cocoon.HumanSize(r.TotalSize))
	}
	return nil
}

func lsTags(ctx context.Context, name string) error {
	var tags []struct {
		Name string `json:"name"`
	}
	if err := apiGet(ctx, "/repositories/"+name+"/tags", &tags); err != nil {
		return err
	}
	if len(tags) == 0 {
		fmt.Printf("No tags found for %s\n", name)
		return nil
	}
	fmt.Printf("Tags for %s:\n", name)
	for _, t := range tags {
		fmt.Printf("  %s:%s\n", name, t.Name)
	}
	return nil
}

// apiGet calls GET /api/{path} on the epoch server.
func apiGet(ctx context.Context, path string, out any) error {
	serverURL, token := resolveConfig()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/api"+path, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := newRegistryClient()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("API %s: %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
