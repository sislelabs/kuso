package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/go-resty/resty/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// checkRespErr converts a (resty.Response, err) pair into a single
// actionable error, treating non-2xx status codes as failures the
// caller MUST surface. The original `get` commands fed every response
// — including 401 error envelopes — straight into json.Unmarshal,
// which silently produced empty result lists. CI scripts piping the
// output through jq saw "[]" and assumed the project was empty
// instead of "your token expired."
func checkRespErr(resp *resty.Response, err error) error {
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("nil response")
	}
	if resp.StatusCode() >= 300 {
		body := string(resp.Body())
		if body == "" {
			body = resp.Status()
		}
		// 401 deserves a more useful pointer than the raw "unauthorized".
		if resp.StatusCode() == 401 {
			return fmt.Errorf("server returned 401: %s — run `kuso login` to refresh the token", body)
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), body)
	}
	return nil
}

// getCmd is the agent-friendly read entrypoint. v0.2 surfaces:
//   kuso get projects [-o json]
//   kuso get services <project> [-o json]
//   kuso get envs <project> [-o json]
//   kuso get addons <project> [-o json]
//
// Output is deterministic (stable sort) so JSON diffs round-trip cleanly.
var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Read kuso resources non-interactively",
	Long: `Read kuso resources non-interactively. Supports -o json|table for
machine and human consumption respectively. Designed to be safe to call
from scripts, CI, and AI agents.`,
}

// ---------------- get projects ----------------

var getProjectsCmd = &cobra.Command{
	Use:     "projects",
	Aliases: []string{"project", "p"},
	Short:   "List projects the caller has access to",
	Example: `  kuso get projects
  kuso get projects -o json | jq '.[].metadata.name'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetProjects()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("fetch projects: %w", err)
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		sort.Slice(items, func(i, j int) bool {
			return projectName(items[i]) < projectName(items[j])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "REPO", "BRANCH", "PREVIEWS"})
			for _, p := range items {
				spec := mapAt(p, "spec")
				repo := mapAt(spec, "defaultRepo")
				previews := mapAt(spec, "previews")
				t.Append([]string{
					projectName(p),
					asString(repo["url"]),
					asString(repo["defaultBranch"]),
					boolText(previews["enabled"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

// ---------------- get services <project> ----------------

var getServicesCmd = &cobra.Command{
	Use:     "services <project>",
	Aliases: []string{"service", "s"},
	Short:   "List services in a project",
	Args:    cobra.ExactArgs(1),
	Example: `  kuso get services analiz
  kuso get services analiz -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetServices(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("fetch services: %w", err)
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		sort.Slice(items, func(i, j int) bool {
			return resourceName(items[i]) < resourceName(items[j])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "RUNTIME", "PORT", "PATH"})
			for _, s := range items {
				spec := mapAt(s, "spec")
				repo := mapAt(spec, "repo")
				short := stripPrefix(resourceName(s), args[0]+"-")
				t.Append([]string{
					short,
					asString(spec["runtime"]),
					asString(spec["port"]),
					asString(repo["path"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

// ---------------- get envs <project> ----------------

var getEnvsCmd = &cobra.Command{
	Use:     "envs <project>",
	Aliases: []string{"env", "environments", "e"},
	Short:   "List environments in a project",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetEnvironments(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("fetch environments: %w", err)
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		sort.Slice(items, func(i, j int) bool {
			return resourceName(items[i]) < resourceName(items[j])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "SERVICE", "KIND", "BRANCH", "HOST"})
			for _, e := range items {
				spec := mapAt(e, "spec")
				t.Append([]string{
					resourceName(e),
					stripPrefix(asString(spec["service"]), args[0]+"-"),
					asString(spec["kind"]),
					asString(spec["branch"]),
					asString(spec["host"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

// ---------------- get addons <project> ----------------

var getAddonsCmd = &cobra.Command{
	Use:     "addons <project>",
	Aliases: []string{"addon", "a"},
	Short:   "List addons in a project",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetAddonsForProject(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("fetch addons: %w", err)
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		sort.Slice(items, func(i, j int) bool {
			return resourceName(items[i]) < resourceName(items[j])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "KIND", "VERSION", "SIZE", "HA"})
			for _, a := range items {
				spec := mapAt(a, "spec")
				short := stripPrefix(resourceName(a), args[0]+"-")
				t.Append([]string{
					short,
					asString(spec["kind"]),
					asString(spec["version"]),
					asString(spec["size"]),
					boolText(spec["ha"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

// ---------------- helpers ----------------

func mapAt(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func projectName(p map[string]any) string {
	return resourceName(p)
}

func resourceName(o map[string]any) string {
	return asString(mapAt(o, "metadata")["name"])
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func boolText(v any) string {
	if b, ok := v.(bool); ok {
		if b {
			return "true"
		}
		return "false"
	}
	return ""
}

func stripPrefix(s, prefix string) string {
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

func jsonOut(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.AddCommand(getProjectsCmd)
	getCmd.AddCommand(getServicesCmd)
	getCmd.AddCommand(getEnvsCmd)
	getCmd.AddCommand(getAddonsCmd)

	getCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
}
