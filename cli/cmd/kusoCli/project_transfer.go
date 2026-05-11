package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// `kuso project export` + `kuso project import` — move a project's
// spec between kuso instances.
//
// Scope: SPEC only (project + services + envs + addons + secrets).
// Live addon data (postgres rows, redis keys, S3 objects) is moved
// separately via `kuso addon-backup` — bundling pg_dump into the
// HTTP roundtrip would block the request for the duration of the
// dump and hold the archive in memory.

var (
	exportOutFile string
	importInFile  string
	importPolicy  string
)

var projectExportCmd = &cobra.Command{
	Use:   "export <project>",
	Short: "Download a project's full spec as a tar.gz",
	Long: `Streams project + services + envs + addons + per-env secret values
as a tar.gz to --out (or stdout when --out is omitted).

The export is portable across kuso instances of the same major
version. Live addon data is NOT included; copy that separately with
` + "`kuso addon-backup`" + ` per addon.`,
	Example: `  kuso project export shop --out shop.tar.gz
  kuso project export shop | gzip -d | tar tv     # inspect contents`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ExportProject(args[0])
		if err != nil {
			return fmt.Errorf("export: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		body := resp.Body()
		if exportOutFile == "" || exportOutFile == "-" {
			if _, err := os.Stdout.Write(body); err != nil {
				return fmt.Errorf("write stdout: %w", err)
			}
			return nil
		}
		if err := os.WriteFile(exportOutFile, body, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", exportOutFile, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %d bytes to %s\n", len(body), exportOutFile)
		return nil
	},
}

var projectImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Restore a project from a tar.gz produced by `kuso project export`",
	Long: `Reads the tarball from --in (or stdin when --in is omitted) and
recreates the project on this kuso instance.

Conflict handling (--policy):

  error      Refuse if the project name already exists (default).
  rename     Auto-suffix the imported project's name, e.g.
             "shop" → "shop-imported-20260512-1530".
  overwrite  Delete the existing project first. DESTRUCTIVE.

Domains in the spec are preserved as-is. If the destination cluster
already serves traffic for one of those hostnames, you'll have a clash
once the imported ingress goes live — the importer doesn't second-guess
you, the operator is expected to plan DNS cutover separately.`,
	Example: `  kuso project import --in shop.tar.gz
  kuso project import --in shop.tar.gz --policy rename
  cat shop.tar.gz | kuso project import --policy overwrite`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		var data []byte
		var err error
		if importInFile == "" || importInFile == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(importInFile)
		}
		if err != nil {
			return fmt.Errorf("read tarball: %w", err)
		}
		if len(data) == 0 {
			return fmt.Errorf("empty input — did you forget --in <file>?")
		}
		resp, err := api.ImportProject(data, importPolicy)
		if err != nil {
			return fmt.Errorf("import: %w", err)
		}
		if resp.StatusCode() == 409 {
			return fmt.Errorf("conflict: project already exists. Re-run with --policy rename or --policy overwrite")
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var result struct {
			Project      string   `json:"project"`
			Services     int      `json:"services"`
			Environments int      `json:"environments"`
			Addons       int      `json:"addons"`
			Secrets      int      `json:"secrets"`
			Warnings     []string `json:"warnings"`
		}
		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			fmt.Println(string(resp.Body()))
			return nil
		}
		fmt.Printf("imported project %q (services=%d envs=%d addons=%d secret-keys=%d)\n",
			result.Project, result.Services, result.Environments, result.Addons, result.Secrets)
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "  warn: %s\n", w)
		}
		if result.Addons > 0 {
			fmt.Fprintln(os.Stderr, "\nNote: addon spec was imported but DATA was not. To move live data,")
			fmt.Fprintln(os.Stderr, "use `kuso addon-backup restore` on the destination after pointing")
			fmt.Fprintln(os.Stderr, "at the source backup tarball.")
		}
		return nil
	},
}

func init() {
	projectExportCmd.Flags().StringVarP(&exportOutFile, "out", "o", "", "output file (default: stdout)")
	projectImportCmd.Flags().StringVarP(&importInFile, "in", "i", "", "input tarball (default: stdin)")
	projectImportCmd.Flags().StringVar(&importPolicy, "policy", "error",
		"conflict handling: error | rename | overwrite")

	projectCmd.AddCommand(projectExportCmd)
	projectCmd.AddCommand(projectImportCmd)
}
