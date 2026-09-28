package kusoCli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// `kuso project export <project>` — reconstruct the project's current
// live state as a kuso.yaml config-as-code document.
//
// This is distinct from `kuso project export-archive`, which produces
// a portable tar.gz bundle (project + envs + secrets) for moving a
// project between kuso instances. This command emits a human-readable
// kuso.yaml you can commit to the repo and re-apply with `kuso apply`.

var exportSpecOutFile string

var projectExportCmd = &cobra.Command{
	Use:   "export <project>",
	Short: "Export a project's current state as kuso.yaml",
	Long: `Reconstructs a kuso.yaml document from the project's live state
(services, addons, crons) and writes it to --out (or stdout when
--out is omitted).

The result round-trips: re-applying it with ` + "`kuso apply`" + ` against
the same cluster is a no-op. Commit it to your repo root to enable
config-as-code on push.`,
	Example: `  kuso project export shop
  kuso project export shop -o kuso.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetProjectSpec(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("export: %w", err)
		}
		body := resp.Body()
		note := secretEnvNote(body)
		if exportSpecOutFile == "" || exportSpecOutFile == "-" {
			fmt.Print(string(body))
		} else {
			if err := os.WriteFile(exportSpecOutFile, body, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", exportSpecOutFile, err)
			}
			fmt.Fprintf(os.Stderr, "wrote %d bytes to %s\n", len(body), exportSpecOutFile)
		}
		if note != "" {
			fmt.Fprint(os.Stderr, note)
		}
		return nil
	},
}

func init() {
	projectExportCmd.Flags().StringVarP(&exportSpecOutFile, "out", "o", "", "write to file instead of stdout")
	projectCmd.AddCommand(projectExportCmd)
}

// secretEnvNote lists the env keys exported as `{secret: true}` — values
// held in kuso secrets that the YAML deliberately doesn't carry, so a
// project recreated from the file needs them set separately. Empty when
// there are none (or the body doesn't parse).
func secretEnvNote(body []byte) string {
	var doc struct {
		Services []struct {
			Name string               `yaml:"name"`
			Env  map[string]yaml.Node `yaml:"env"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return ""
	}
	var lines []string
	for _, s := range doc.Services {
		var keys []string
		for k, n := range s.Env {
			var m struct {
				Secret bool `yaml:"secret"`
			}
			if n.Kind == yaml.MappingNode && n.Decode(&m) == nil && m.Secret {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			lines = append(lines, "  "+s.Name+": "+strings.Join(keys, ", "))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "note: these env vars are held in kuso secrets and exported as {secret: true} without values.\n" +
		"apply leaves them untouched; a project recreated from this file needs them set separately (kuso env set):\n" +
		strings.Join(lines, "\n") + "\n"
}
