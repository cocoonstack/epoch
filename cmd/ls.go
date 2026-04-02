package cmd

import (
	"cmp"
	"context"
	"fmt"
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
	if err := newRegistryClient().GetJSON(ctx, "/repositories", &repos); err != nil {
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
	if err := newRegistryClient().GetJSON(ctx, "/repositories/"+name+"/tags", &tags); err != nil {
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
