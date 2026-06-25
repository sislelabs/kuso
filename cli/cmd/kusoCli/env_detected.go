// env_detected.go adds `kuso env detected <project> <service>` — the
// read-only listing of env-var names kuso detected as referenced by a
// service (build-time source scan + runtime crash hints) but possibly
// unset. Mirrors the dashboard's "detected but unset" prompts.
//
// Registered on envCmd (defined in env.go) via this file's own init().
// envCmd already carries a persistent -o/--output flag, so we reuse it.

package kusoCli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var envDetectedCmd = &cobra.Command{
	Use:   "detected <project> <service>",
	Short: "List env-var names kuso detected as referenced by a service",
	Long: `Print the env-var names kuso detected as referenced by a service. Two
sources are merged: a build-time scan of .env.example and source, plus
runtime hints extracted from crash logs (missing-env errors). Cross-check
against 'kuso env list' to find names that are referenced but unset.`,
	Example: `  kuso env detected scubatony api
  kuso env detected scubatony api -o json`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetDetectedEnv(args[0], args[1])
		if err != nil {
			return fmt.Errorf("detected env: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}

		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}

		var out struct {
			Names      []string `json:"names"`
			DetectedAt string   `json:"detectedAt"`
			Hints      []struct {
				Name     string `json:"name"`
				LastLine string `json:"lastLine"`
				LastSeen string `json:"lastSeen"`
			} `json:"hints"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		if len(out.Names) == 0 && len(out.Hints) == 0 {
			fmt.Println("no detected env vars (no recent build scan or crash hints)")
			return nil
		}
		if len(out.Names) > 0 {
			fmt.Println("detected names:")
			for _, n := range out.Names {
				fmt.Printf("  %s\n", n)
			}
			if out.DetectedAt != "" {
				fmt.Printf("  (scanned %s)\n", out.DetectedAt)
			}
		}
		if len(out.Hints) > 0 {
			fmt.Println("runtime crash hints:")
			for _, h := range out.Hints {
				fmt.Printf("  %s (last seen %s)\n", h.Name, h.LastSeen)
				if h.LastLine != "" {
					fmt.Printf("    %s\n", h.LastLine)
				}
			}
		}
		return nil
	},
}

func init() {
	// envCmd is the package-level var defined in env.go; it already
	// carries a persistent -o/--output flag.
	envCmd.AddCommand(envDetectedCmd)
}
