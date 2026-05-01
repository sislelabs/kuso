package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// `kuso github` — inspect GitHub App state and connected repos.
//
//   kuso github status                    -> install URL + configured?
//   kuso github installations [-o json]   -> orgs/users with the App installed
//   kuso github repos <installation-id>   -> repos accessible via that install
//   kuso github refresh                   -> repull from GitHub

var githubCmd = &cobra.Command{
	Use:   "github",
	Short: "Inspect GitHub App state",
}

var githubStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show GitHub App install URL + configured state",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetInstallURL()
		if err != nil {
			return err
		}
		var data map[string]any
		_ = json.Unmarshal(resp.Body(), &data)
		fmt.Printf("configured: %v\n", data["configured"])
		if u, ok := data["url"].(string); ok && u != "" {
			fmt.Printf("install URL: %s\n", u)
		}
		return nil
	},
}

var githubInstallationsCmd = &cobra.Command{
	Use:     "installations",
	Aliases: []string{"installs"},
	Short:   "List orgs/users with the kuso GitHub App installed",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListInstallations()
		if err != nil {
			return err
		}
		var items []map[string]any
		_ = json.Unmarshal(resp.Body(), &items)
		sort.Slice(items, func(i, j int) bool {
			return asString(items[i]["accountLogin"]) < asString(items[j]["accountLogin"])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "ACCOUNT", "TYPE", "REPOS"})
			for _, i := range items {
				repos := 0
				if r, ok := i["repositories"].([]any); ok {
					repos = len(r)
				}
				idStr := ""
				if f, ok := i["id"].(float64); ok {
					idStr = fmt.Sprintf("%.0f", f)
				} else {
					idStr = asString(i["id"])
				}
				t.Append([]string{
					idStr,
					asString(i["accountLogin"]),
					asString(i["accountType"]),
					fmt.Sprintf("%d", repos),
				})
			}
			t.Render()
			return nil
		}
	},
}

var githubReposCmd = &cobra.Command{
	Use:   "repos <installation-id>",
	Short: "List repos accessible via an installation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		var id int64
		_, _ = fmt.Sscanf(args[0], "%d", &id)
		resp, err := api.ListInstallationRepos(id)
		if err != nil {
			return err
		}
		var items []map[string]any
		_ = json.Unmarshal(resp.Body(), &items)
		sort.Slice(items, func(i, j int) bool {
			return asString(items[i]["fullName"]) < asString(items[j]["fullName"])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"FULL NAME", "DEFAULT BRANCH", "PRIVATE"})
			for _, i := range items {
				t.Append([]string{
					asString(i["fullName"]),
					asString(i["defaultBranch"]),
					boolText(i["private"]),
				})
			}
			t.Render()
			return nil
		}
	},
}

var githubRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh the cached installation list from GitHub",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RefreshInstallations()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Println("installations refreshed")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(githubCmd)
	githubCmd.AddCommand(githubStatusCmd)
	githubCmd.AddCommand(githubInstallationsCmd)
	githubCmd.AddCommand(githubReposCmd)
	githubCmd.AddCommand(githubRefreshCmd)
	githubCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
}
