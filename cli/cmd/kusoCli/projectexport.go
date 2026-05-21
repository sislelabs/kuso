package kusoCli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
		if err != nil {
			return fmt.Errorf("export: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		body := resp.Body()
		if exportSpecOutFile == "" || exportSpecOutFile == "-" {
			fmt.Print(string(body))
			return nil
		}
		if err := os.WriteFile(exportSpecOutFile, body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", exportSpecOutFile, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %d bytes to %s\n", len(body), exportSpecOutFile)
		return nil
	},
}

func init() {
	projectExportCmd.Flags().StringVarP(&exportSpecOutFile, "out", "o", "", "write to file instead of stdout")
	projectCmd.AddCommand(projectExportCmd)
}
