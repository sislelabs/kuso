package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// `kuso env example <project> <service>` reads the repo's .env.example
// (or .env.sample): the variables the app expects, for the dashboard's
// "Import from .env.example". Read-only: setting them stays an explicit
// `kuso env set`, since example values are rarely the real ones.

var envExampleCmd = &cobra.Command{
	Use:   "example <project> <service>",
	Short: "List the variables in the service repo's .env.example",
	Long: `Read .env.example (or .env.sample) from the service's repo, in its build
directory first and then the repo root, and list the variables it declares
with their example values. Variables already set on the service are
marked. Set the rest with 'kuso env set'.`,
	Example: `  kuso env example shop api`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RawGet(servicePath(args[0], args[1], "env-example"))
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("read .env.example: %w", err)
		}
		var ex struct {
			File    string   `json:"file"`
			Branch  string   `json:"branch"`
			Checked []string `json:"checked"`
			Entries []struct {
				Key     string `json:"key"`
				Value   string `json:"value"`
				Comment string `json:"comment"`
			} `json:"entries"`
			Truncated bool `json:"truncated"`
		}
		if err := json.Unmarshal(resp.Body(), &ex); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if outputFormat == "json" {
			return jsonOut(ex)
		}
		if ex.File == "" {
			fmt.Printf("no example env file in the repo (checked %s)\n", strings.Join(ex.Checked, ", "))
			return nil
		}
		set := map[string]bool{}
		if lr, err := api.GetEnv(args[0], args[1]); err == nil && lr.StatusCode() < 300 {
			var cur struct {
				EnvVars []struct {
					Name string `json:"name"`
				} `json:"envVars"`
			}
			if json.Unmarshal(lr.Body(), &cur) == nil {
				for _, v := range cur.EnvVars {
					set[v.Name] = true
				}
			}
		}
		fmt.Printf("%s on %s\n", ex.File, ex.Branch)
		t := tablewriter.NewWriter(os.Stdout)
		t.SetAutoWrapText(false)
		t.SetHeader([]string{"KEY", "EXAMPLE", "SET", "COMMENT"})
		for _, e := range ex.Entries {
			mark := ""
			if set[e.Key] {
				mark = "yes"
			}
			t.Append([]string{e.Key, truncate(e.Value, 40), mark, truncate(e.Comment, 50)})
		}
		t.Render()
		if ex.Truncated {
			fmt.Fprintln(os.Stderr, "note: the file is longer than the server reads; only the first entries are listed")
		}
		return nil
	},
}

func init() {
	envCmd.AddCommand(envExampleCmd)
}
