package cmd

import (
	"context"
	"fmt"
	"slices"

	"github.com/spf13/cobra"
)

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls [name]",
		Aliases: []string{"list"},
		Short:   "List repositories or tags in the Epoch registry",
		Long:    `Without arguments, lists all repositories via /v2/_catalog. With a name, lists all tags for that repository via /v2/<name>/tags/list.`,
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

func lsRepos(ctx context.Context) error {
	client, err := newRegistryClient()
	if err != nil {
		return err
	}
	repos, err := client.Catalog(ctx)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		fmt.Println("No repositories in registry")
		return nil
	}
	slices.Sort(repos)
	for _, r := range repos {
		fmt.Println(r)
	}
	return nil
}

func lsTags(ctx context.Context, name string) error {
	client, err := newRegistryClient()
	if err != nil {
		return err
	}
	tags, err := client.ListTags(ctx, name)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		fmt.Printf("No tags found for %s\n", name)
		return nil
	}
	slices.Sort(tags)
	for _, t := range tags {
		fmt.Printf("%s:%s\n", name, t)
	}
	return nil
}
