package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
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
			if len(args) == 1 {
				return lsTags(args[0])
			}
			return lsRepos()
		},
	}
}

func lsRepos() error {
	var repos []struct {
		Name      string `json:"name"`
		TagCount  int    `json:"tagCount"`
		TotalSize int64  `json:"totalSize"`
	}
	if err := apiGet("/repositories", &repos); err != nil {
		return err
	}
	if len(repos) == 0 {
		fmt.Println("No snapshots in registry")
		return nil
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	fmt.Printf("%-35s  %-6s  %s\n", "REPOSITORY", "TAGS", "SIZE")
	fmt.Println(strings.Repeat("-", 55))
	for _, r := range repos {
		fmt.Printf("%-35s  %-6d  %s\n", r.Name, r.TagCount, cocoon.HumanSize(r.TotalSize))
	}
	return nil
}

func lsTags(name string) error {
	var tags []struct {
		Name string `json:"name"`
	}
	if err := apiGet("/repositories/"+name+"/tags", &tags); err != nil {
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
func apiGet(path string, out interface{}) error {
	serverURL := os.Getenv("EPOCH_SERVER")
	if serverURL == "" {
		serverURL = "http://127.0.0.1:4300"
	}
	token := os.Getenv("EPOCH_REGISTRY_TOKEN")

	req, _ := http.NewRequest("GET", serverURL+"/api"+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("API %s: %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
