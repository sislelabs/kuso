package kusoCli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sislelabs/kuso/compose"
	"kuso/pkg/kusoApi"
)

// `kuso import compose <file>` converts a local docker-compose.yml
// into kuso's config-as-code (kuso.yaml) and, with --apply, creates
// the resources on the connected kuso instance via the same /apply
// endpoint `kuso apply` uses. Default is dry-run: it prints the
// generated kuso.yaml plus a report of every mapping decision and
// every compose key with no kuso equivalent (flagged, never dropped).
// Touches nothing without --apply.

var (
	importProject          string
	importOut              string
	importApply            bool
	importDryRun           bool
	importAllowEmptyAddons bool
	importAllowMissingEnv  bool
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Import resources into kuso from other formats",
}

var importComposeCmd = &cobra.Command{
	Use:   "compose <docker-compose.yml>",
	Short: "Convert a local docker-compose.yml to kuso (dry-run by default)",
	Long: `Reads a local docker-compose file and converts it to kuso's
config-as-code shape. Datastore services (postgres, mysql, redis, …)
become managed kuso addons; app services become build (runtime=dockerfile)
or image (runtime=image) services. Compose keys kuso has no field for
are reported, never silently dropped.

Default is dry-run: prints the generated kuso.yaml + a mapping report,
touches nothing. -o writes the kuso.yaml to disk. --apply creates the
resources on the connected kuso instance.

--apply refuses to run when the conversion carries data-loss risk:
datastore conversions create EMPTY managed addons (source volumes,
data, users and init scripts are NOT migrated — override with
--allow-empty-addons), and env_file values are never imported
(override with --allow-missing-env-files).`,
	Example: `  # dry-run: print the generated kuso.yaml + report
  kuso import compose docker-compose.yml

  # write the kuso.yaml to disk for review / git
  kuso import compose docker-compose.yml -o kuso.yaml

  # convert and create resources on the connected kuso
  kuso import compose docker-compose.yml --apply

  # override the project slug (default: compose file's directory name)
  kuso import compose docker-compose.yml --project shop --apply`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		projectName := importProject
		if projectName == "" {
			projectName = projectNameFromPath(path)
		}

		proj, err := compose.Parse(cmd.Context(), raw, filepath.Dir(path))
		if err != nil {
			return err
		}
		doc, rep := compose.Convert(proj, projectName)
		yamlOut, err := doc.Marshal()
		if err != nil {
			return fmt.Errorf("render kuso.yaml: %w", err)
		}

		// Report goes to stderr so stdout stays a clean kuso.yaml that
		// can be piped (e.g. `kuso import compose c.yml > kuso.yaml`).
		fmt.Fprintln(os.Stderr, "## Conversion report")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, rep.Markdown())
		if rep.HasFlags() {
			fmt.Fprintln(os.Stderr, "⚠ Some services need attention before they'll deploy (see ⚠ rows above).")
			fmt.Fprintln(os.Stderr)
		}

		if importOut != "" {
			if err := os.WriteFile(importOut, yamlOut, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", importOut, err)
			}
			fmt.Fprintf(os.Stderr, "→ wrote %s\n", importOut)
		}

		if !importApply {
			// Dry-run: emit the generated kuso.yaml on stdout.
			fmt.Print(string(yamlOut))
			fmt.Fprintln(os.Stderr, "\n→ dry-run only — pass --apply to create resources, or -o to save the kuso.yaml")
			return nil
		}

		if api == nil {
			return fmt.Errorf("--apply needs a kuso login (run `kuso login` first)")
		}
		// Ensure the target project exists before applying — spec.Apply
		// creates services/addons/crons but not the project itself, and
		// compose import is usually a brand-new project. 409 (already
		// exists) is fine; anything else is fatal.
		if !importDryRun {
			if err := composeApplyGates(doc, rep); err != nil {
				return err
			}
			pr, err := api.CreateProject(kusoApi.CreateProjectRequest{Name: doc.Project})
			if err != nil {
				return fmt.Errorf("create project: %w", err)
			}
			if pr.StatusCode() >= 300 && pr.StatusCode() != 409 {
				return fmt.Errorf("create project failed (%d): %s", pr.StatusCode(), pr.String())
			}
		}
		resp, err := api.ApplyConfig(doc.Project, yamlOut, importDryRun, false)
		if err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		if resp.StatusCode() >= 400 {
			return fmt.Errorf("apply failed (%d): %s", resp.StatusCode(), resp.String())
		}
		printApplyResult(resp.Body(), importDryRun)
		return nil
	},
}

// composeApplyGates refuses a real (non-dry-run) --apply when the
// conversion carries data-loss or missing-config risk that needs an
// explicit acknowledgement:
//   - datastore conversions mint FRESH, EMPTY managed addons — source
//     volumes, data, users and init scripts are NOT migrated. Gate:
//     --allow-empty-addons.
//   - env_file values were never read, so services would deploy
//     without that configuration. Gate: --allow-missing-env-files.
func composeApplyGates(doc *compose.Doc, rep *compose.Report) error {
	if len(doc.Addons) > 0 && !importAllowEmptyAddons {
		names := make([]string, 0, len(doc.Addons))
		for _, a := range doc.Addons {
			names = append(names, fmt.Sprintf("%s (%s)", a.Name, a.Kind))
		}
		return fmt.Errorf("refusing --apply: %s would be created as EMPTY managed addon(s) — existing volumes, data, users and init scripts are NOT migrated. Plan a manual data migration (dump & restore), then re-run with --allow-empty-addons", strings.Join(names, ", "))
	}
	if len(rep.UnresolvedEnvFiles) > 0 && !importAllowMissingEnv {
		return fmt.Errorf("refusing --apply: env_file value(s) were not imported (%s) — services would deploy without that configuration. Copy the values into kuso env vars, then re-run with --allow-missing-env-files", strings.Join(rep.UnresolvedEnvFiles, ", "))
	}
	return nil
}

// projectNameFromPath derives a kuso project slug from the compose
// file's parent directory name (the conventional project name), or the
// file's base name when it sits at the repo root.
func projectNameFromPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	dir := filepath.Base(filepath.Dir(abs))
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		base := filepath.Base(abs)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return dir
}

func init() {
	importComposeCmd.Flags().StringVar(&importProject, "project", "", "kuso project slug (default: compose file's directory name)")
	importComposeCmd.Flags().StringVarP(&importOut, "out", "o", "", "write the generated kuso.yaml to this path")
	importComposeCmd.Flags().BoolVar(&importApply, "apply", false, "create resources on the connected kuso (default is dry-run)")
	importComposeCmd.Flags().BoolVar(&importDryRun, "server-dry-run", false, "with --apply, ask the server for the plan without writing")
	importComposeCmd.Flags().BoolVar(&importAllowEmptyAddons, "allow-empty-addons", false, "with --apply, permit datastore conversions — the managed addons start EMPTY; source volumes/data are NOT migrated")
	importComposeCmd.Flags().BoolVar(&importAllowMissingEnv, "allow-missing-env-files", false, "with --apply, permit services whose env_file values were not imported")
	importCmd.AddCommand(importComposeCmd)
	rootCmd.AddCommand(importCmd)
}
