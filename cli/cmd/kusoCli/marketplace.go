package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso marketplace` browses the curated app catalog and deploys an
// app into a project via the same /apply flow `kuso apply` uses.
// `deploy` renders the app's kuso.yaml server-side (answers filled in
// via --set) and, unless --dry-run, ensures the target project exists
// (409-tolerant, mirrors import.go) then applies the rendered config.

var (
	mktProject string
	mktSets    []string
	mktDryRun  bool
)

var marketplaceCmd = &cobra.Command{
	Use:   "marketplace",
	Short: "Browse and deploy curated one-click apps",
}

var marketplaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available marketplace apps",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("run `kuso login` first")
		}
		resp, err := api.MarketplaceList()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list marketplace apps: %w", err)
		}
		var body struct {
			Apps []struct {
				Name, Title, Category, Description string
			} `json:"apps"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return err
		}
		for _, a := range body.Apps {
			fmt.Printf("%-16s %-12s %s\n", a.Name, a.Category, a.Title)
		}
		return nil
	},
}

var marketplaceInfoCmd = &cobra.Command{
	Use:   "info <app>",
	Short: "Show an app's details and required prompts",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("run `kuso login` first")
		}
		resp, err := api.MarketplaceGet(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("app %q not found", args[0])
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Println(string(resp.Body()))
		return nil
	},
}

var marketplaceDeployCmd = &cobra.Command{
	Use:   "deploy <app>",
	Short: "Deploy a marketplace app",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso marketplace deploy uptime-kuma --set host=status.example.com
  kuso marketplace deploy umami --project analytics --set host=stats.example.com --dry-run`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("run `kuso login` first")
		}
		app := args[0]
		answers := map[string]string{}
		for _, kv := range mktSets {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("--set expects key=value, got %q", kv)
			}
			answers[k] = v
		}
		project := mktProject
		if project == "" {
			project = app
		}
		resp, err := api.MarketplaceRender(app, project, answers)
		if err != nil {
			return err
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("app %q not found", app)
		}
		if resp.StatusCode() >= 400 {
			return fmt.Errorf("render failed (%d): %s", resp.StatusCode(), resp.String())
		}
		var rendered struct {
			YAML  string                          `json:"yaml"`
			Notes []struct{ Kind, Detail string } `json:"notes"`
		}
		if err := json.Unmarshal(resp.Body(), &rendered); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "## Plan")
		for _, n := range rendered.Notes {
			fmt.Fprintf(os.Stderr, "  - [%s] %s\n", n.Kind, n.Detail)
		}
		fmt.Fprintln(os.Stderr)

		if mktDryRun {
			fmt.Print(rendered.YAML)
			fmt.Fprintln(os.Stderr, "\n→ dry-run only — drop --dry-run to create resources")
			return nil
		}
		// Ensure the project exists (spec.Apply doesn't create it). 409 ok.
		pr, err := api.CreateProject(kusoApi.CreateProjectRequest{Name: project})
		if err != nil {
			return fmt.Errorf("create project: %w", err)
		}
		if pr.StatusCode() >= 300 && pr.StatusCode() != 409 {
			return fmt.Errorf("create project failed (%d): %s", pr.StatusCode(), pr.String())
		}
		ar, err := api.ApplyConfig(project, []byte(rendered.YAML), false, false)
		if err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		if ar.StatusCode() >= 400 {
			return fmt.Errorf("apply failed (%d): %s", ar.StatusCode(), ar.String())
		}
		fmt.Printf("→ deployed %s into project %s\n", app, project)
		return nil
	},
}

func init() {
	marketplaceDeployCmd.Flags().StringVar(&mktProject, "project", "", "target project (default: app name)")
	marketplaceDeployCmd.Flags().StringArrayVar(&mktSets, "set", nil, "prompt answer key=value (repeatable)")
	marketplaceDeployCmd.Flags().BoolVar(&mktDryRun, "dry-run", false, "render + print the kuso.yaml without creating anything")
	marketplaceCmd.AddCommand(marketplaceListCmd, marketplaceInfoCmd, marketplaceDeployCmd)
	rootCmd.AddCommand(marketplaceCmd)
}
