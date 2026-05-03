package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

// statusCmd prints a one-screen view of the project + every service +
// its production env. Pulls from the rollup endpoint /api/projects/{p}.
//
// If the working directory has a kuso.yml we use its `project:` value
// by default; otherwise the user passes the name explicitly.
var statusCmd = &cobra.Command{
	Use:     "status [project]",
	Short:   "Show the project rollup: services, URLs, replicas, builds.",
	Example: "  kuso status\n  kuso status my-product",
	Run: func(cmd *cobra.Command, args []string) {
		project := ""
		if len(args) > 0 {
			project = args[0]
		} else {
			if body, err := os.ReadFile("kuso.yml"); err == nil {
				project = readProjectFromYAML(body)
			}
		}
		if project == "" {
			fmt.Fprintln(os.Stderr, "error: pass <project> or run from a directory containing kuso.yml")
			os.Exit(1)
		}
		resp, err := api.GetProjectFull(project)
		if err != nil {
			fmt.Fprintln(os.Stderr, "status:", err)
			os.Exit(1)
		}
		if resp.StatusCode() == 404 {
			fmt.Fprintf(os.Stderr, "project %q not found\n", project)
			os.Exit(1)
		}
		if resp.StatusCode() >= 400 {
			fmt.Fprintf(os.Stderr, "status failed (%d): %s\n", resp.StatusCode(), resp.String())
			os.Exit(1)
		}

		var rollup struct {
			Project struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					BaseDomain string `json:"baseDomain"`
				} `json:"spec"`
			} `json:"project"`
			Services []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Runtime string `json:"runtime"`
					Port    int    `json:"port"`
				} `json:"spec"`
			} `json:"services"`
			Environments []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Service string `json:"service"`
					Kind    string `json:"kind"`
					Host    string `json:"host"`
					Branch  string `json:"branch"`
				} `json:"spec"`
				Status map[string]any `json:"status"`
			} `json:"environments"`
		}
		if err := json.Unmarshal(resp.Body(), &rollup); err != nil {
			fmt.Fprintln(os.Stderr, "decode:", err)
			os.Exit(1)
		}

		fmt.Printf("project %s\n", rollup.Project.Metadata.Name)
		if rollup.Project.Spec.BaseDomain != "" {
			fmt.Printf("  base   %s\n", rollup.Project.Spec.BaseDomain)
		}
		for _, s := range rollup.Services {
			fmt.Printf("\nservice %s   runtime=%s port=%d\n",
				short(s.Metadata.Name, rollup.Project.Metadata.Name), s.Spec.Runtime, s.Spec.Port)
			for _, e := range rollup.Environments {
				if e.Spec.Service != s.Metadata.Name {
					continue
				}
				phase, _ := e.Status["phase"].(string)
				if phase == "" {
					phase = "unknown"
				}
				url, _ := e.Status["url"].(string)
				replicaInfo := "-"
				if r, ok := e.Status["replicas"].(map[string]any); ok {
					ready, _ := r["ready"].(float64)
					desired, _ := r["desired"].(float64)
					replicaInfo = fmt.Sprintf("%d/%d", int(ready), int(desired))
				}
				fmt.Printf("  %s  %-10s replicas=%s\n", e.Spec.Kind, phase, replicaInfo)
				if url != "" {
					fmt.Printf("    %s\n", url)
				}
				if e.Spec.Branch != "" && e.Spec.Kind == "production" {
					fmt.Printf("    branch %s\n", e.Spec.Branch)
				}
			}
		}
	},
}

func short(full, project string) string {
	prefix := project + "-"
	if len(full) > len(prefix) && full[:len(prefix)] == prefix {
		return full[len(prefix):]
	}
	return full
}
