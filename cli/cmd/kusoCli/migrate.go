package kusoCli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sislelabs/kuso/coolify"
	"kuso/pkg/kusoApi"
)

// `kuso migrate coolify` — port a remote Coolify v4 instance into
// the currently-logged-in kuso. The Coolify side is strictly read-
// only (the coolify package guards GET-only at the http.Request
// layer); every write happens against kuso. Default mode is dry-run:
// prints a markdown report grouping resources by what'll happen,
// touches nothing. --apply does the actual creates.

var (
	migrateCoolifyURL    string
	migrateCoolifyToken  string
	migrateApply         bool
	migrateProjectFilter string
	migrateOutDir        string
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate resources from another platform into kuso",
}

var migrateCoolifyCmd = &cobra.Command{
	Use:   "coolify",
	Short: "Migrate a Coolify v4 instance to kuso (dry-run by default)",
	Example: `  # dry-run report (touches nothing on either side)
  kuso migrate coolify --coolify-url https://ops.example.com --coolify-token 'sk_…'

  # actually create kuso projects / services / addons (still read-only on Coolify)
  kuso migrate coolify --coolify-url https://ops.example.com --coolify-token 'sk_…' --apply

  # write report + per-addon migrate-data.sh stubs into a directory
  kuso migrate coolify --coolify-url … --coolify-token … --out-dir ./migration`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if migrateCoolifyURL == "" || migrateCoolifyToken == "" {
			return fmt.Errorf("--coolify-url and --coolify-token are required")
		}
		if migrateApply && api == nil {
			return fmt.Errorf("--apply needs a kuso login (run `kuso login` first)")
		}
		c := coolify.New(migrateCoolifyURL, migrateCoolifyToken)
		fmt.Fprintln(os.Stderr, "→ snapshotting Coolify (read-only)…")
		inv, err := coolify.Snapshot(cmd.Context(), c)
		if err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		fmt.Fprintf(os.Stderr,
			"→ inventory: %d apps, %d services, %d databases · %d migrate, %d skip, %d flag\n",
			inv.NumApps, inv.NumServices, inv.NumDBs, inv.NumMigrate, inv.NumSkipped, inv.NumFlag,
		)

		items := inv.Items
		if migrateProjectFilter != "" {
			filtered := items[:0]
			for _, it := range items {
				if it.ProjectName == migrateProjectFilter {
					filtered = append(filtered, it)
				}
			}
			items = filtered
			fmt.Fprintf(os.Stderr, "→ filtered to project=%s: %d items\n", migrateProjectFilter, len(items))
		}

		report := renderMigrationReport(inv, items)
		if migrateOutDir != "" {
			if err := os.MkdirAll(migrateOutDir, 0o755); err != nil {
				return fmt.Errorf("mkdir out-dir: %w", err)
			}
			rp := filepath.Join(migrateOutDir, "REPORT.md")
			if err := os.WriteFile(rp, []byte(report), 0o644); err != nil {
				return fmt.Errorf("write report: %w", err)
			}
			fmt.Fprintf(os.Stderr, "→ wrote %s\n", rp)
		} else {
			fmt.Println(report)
		}

		if !migrateApply {
			fmt.Fprintln(os.Stderr, "→ dry-run only — pass --apply to actually create resources on kuso")
			return nil
		}

		fmt.Fprintln(os.Stderr, "→ applying to kuso…")
		applied := applyMigration(cmd.Context(), c, items)
		fmt.Fprintf(os.Stderr, "→ applied %d resources\n", applied)

		if migrateOutDir != "" {
			n := writeDataMigrationScripts(items, migrateOutDir)
			fmt.Fprintf(os.Stderr, "→ wrote %d migrate-data.sh stubs to %s\n", n, migrateOutDir)
		}
		return nil
	},
}

func renderMigrationReport(inv *coolify.Inventory, items []coolify.Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# kuso migrate coolify — dry-run report\n\n")
	fmt.Fprintf(&b, "**Source:** Coolify %s · **Items:** %d\n\n", inv.CoolifyVersion, len(items))
	fmt.Fprintf(&b, "| action | count |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| migrate | %d |\n| skip | %d |\n| flag | %d |\n\n",
		countAction(items, "migrate"), countAction(items, "skip"), countAction(items, "flag"))

	fmt.Fprintf(&b, "## Plan by Coolify project\n\n")
	// Collision-aware slug assignment: two Coolify projects that
	// slugify to the same kuso name each get their own bucket; the
	// second one becomes `<slug>-2` and so on. Same logic the apply
	// path uses, lifted out so report and apply agree.
	byCoolifyName := map[string][]coolify.Item{}
	order := []string{}
	for _, it := range items {
		key := it.ProjectName
		if key == "" {
			key = "(unmapped)"
		}
		if _, ok := byCoolifyName[key]; !ok {
			order = append(order, key)
		}
		byCoolifyName[key] = append(byCoolifyName[key], it)
	}
	slugFor := coolify.AssignKusoSlugs(order)
	for _, name := range order {
		kusoSlug := slugFor[name]
		header := name
		if kusoSlug != "" && kusoSlug != "(unmapped)" {
			header = fmt.Sprintf("%s · kuso project `%s`", name, kusoSlug)
		}
		fmt.Fprintf(&b, "### %s\n\n", header)
		fmt.Fprintf(&b, "| action | name | kind | reason |\n| --- | --- | --- | --- |\n")
		for _, it := range byCoolifyName[name] {
			tag := actionEmoji(it.Verdict.Action)
			detail := it.Verdict.Reason
			if it.App != nil {
				detail = fmt.Sprintf("%s · `%s` branch=%s", detail, it.App.GitRepository, it.App.GitBranch)
			} else if it.Database != nil {
				detail = fmt.Sprintf("%s · type=%s", detail, it.Database.DatabaseType)
			}
			fmt.Fprintf(&b, "| %s %s | `%s` | %s | %s |\n",
				tag, it.Verdict.Action, it.Name, it.Verdict.Kind, detail)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "## Notes\n\n")
	fmt.Fprintf(&b, "- Coolify is **read-only** by contract — this tool can't delete or modify anything on Coolify.\n")
	fmt.Fprintf(&b, "- docker-compose apps and Coolify service stacks are skipped per policy.\n")
	fmt.Fprintf(&b, "- Domains are NOT registered on kuso during migration; cut over manually after each service is healthy on its auto-generated `<svc>.<project>.<kuso-domain>` URL.\n")
	fmt.Fprintf(&b, "- Database migration is structural only — `migrate-data.sh` scripts are emitted to `--out-dir` so you run the data move when ready.\n")
	return b.String()
}

func countAction(items []coolify.Item, action string) int {
	n := 0
	for _, it := range items {
		if it.Verdict.Action == action {
			n++
		}
	}
	return n
}

func actionEmoji(a string) string {
	switch a {
	case "migrate":
		return "✓"
	case "skip":
		return "⊘"
	case "flag":
		return "⚠"
	}
	return "·"
}

func applyMigration(ctx context.Context, c *coolify.Client, items []coolify.Item) int {
	created := 0
	// Group by Coolify project NAME first (not slug) so two distinct
	// Coolify projects that happen to slugify to the same string each
	// get their own bucket. assignKusoSlugs adds the collision suffix.
	byCoolifyName := map[string][]coolify.Item{}
	coolifyOrder := []string{}
	for _, it := range items {
		if it.Verdict.Action != "migrate" {
			continue
		}
		if it.ProjectName == "" {
			continue
		}
		if _, ok := byCoolifyName[it.ProjectName]; !ok {
			coolifyOrder = append(coolifyOrder, it.ProjectName)
		}
		byCoolifyName[it.ProjectName] = append(byCoolifyName[it.ProjectName], it)
	}
	slugFor := coolify.AssignKusoSlugs(coolifyOrder)
	order := make([]string, 0, len(coolifyOrder))
	byProject := map[string][]coolify.Item{}
	for _, name := range coolifyOrder {
		slug := slugFor[name]
		order = append(order, slug)
		byProject[slug] = byCoolifyName[name]
	}

	for _, projectSlug := range order {
		fmt.Fprintf(os.Stderr, "  · project %s …\n", projectSlug)
		var defaultRepoURL, defaultBranch string
		for _, it := range byProject[projectSlug] {
			if it.App != nil && it.App.GitRepository != "" {
				defaultRepoURL = coolify.NormalizeRepoURL(it.App.GitRepository)
				defaultBranch = it.App.GitBranch
				break
			}
		}
		if defaultRepoURL == "" {
			fmt.Fprintf(os.Stderr, "    ⚠ no git-backed app — skipping project (kuso requires a defaultRepo)\n")
			continue
		}

		req := kusoApi.CreateProjectRequest{
			Name: projectSlug,
			DefaultRepo: &kusoApi.RepoRef{
				URL:           defaultRepoURL,
				DefaultBranch: defaultBranch,
			},
		}
		resp, err := api.CreateProject(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ✗ create project: %v\n", err)
			continue
		}
		switch {
		case resp.StatusCode() == 409:
			fmt.Fprintf(os.Stderr, "    · project exists (ok)\n")
		case resp.StatusCode() >= 300:
			fmt.Fprintf(os.Stderr, "    ✗ create project %d: %s\n", resp.StatusCode(), string(resp.Body()))
			continue
		default:
			fmt.Fprintf(os.Stderr, "    ✓ project created\n")
			created++
		}

		// Apps → services + envs.
		for _, it := range byProject[projectSlug] {
			if it.App == nil {
				continue
			}
			// Coolify app names are usually the full repo slug + branch
			// + uuid suffix (e.g. "biznesguys/foo:main-abc123def"). The
			// useful part for a kuso service name is the repo basename
			// — short, predictable, kube-DNS-safe after slugify.
			svcSlug := coolify.ServiceSlugFromApp(it.App)
			svcReq := kusoApi.CreateServiceRequest{
				Name:    svcSlug,
				Runtime: coolify.RuntimeForBuildPack(it.App.BuildPack),
				Port:    int32(coolify.ParseFirstPort(it.App.PortsExposes)),
				Repo: &kusoApi.ServiceRepoSpec{
					URL:  coolify.NormalizeRepoURL(it.App.GitRepository),
					Path: coolify.NormalizeBaseDir(it.App.BaseDirectory),
				},
			}
			sr, err := api.AddService(projectSlug, svcReq)
			if err != nil {
				fmt.Fprintf(os.Stderr, "      ✗ service %s: %v\n", svcSlug, err)
				continue
			}
			if sr.StatusCode() >= 300 && sr.StatusCode() != 409 {
				fmt.Fprintf(os.Stderr, "      ✗ service %s %d: %s\n", svcSlug, sr.StatusCode(), string(sr.Body()))
				continue
			}
			created++
			fmt.Fprintf(os.Stderr, "      ✓ service %s\n", svcSlug)

			envs, err := c.ListApplicationEnvs(ctx, it.App.UUID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "        ⚠ list envs: %v\n", err)
				continue
			}
			body := kusoApi.SetEnvRequest{EnvVars: []map[string]any{}}
			for _, e := range coolify.SelectEnvVars(envs) {
				body.EnvVars = append(body.EnvVars, map[string]any{
					"name":  e.Key,
					"value": e.Value,
				})
			}
			if len(body.EnvVars) == 0 {
				continue
			}
			er, err := api.SetEnv(projectSlug, svcSlug, body)
			if err != nil {
				fmt.Fprintf(os.Stderr, "        ⚠ set env: %v\n", err)
				continue
			}
			if er.StatusCode() >= 300 {
				fmt.Fprintf(os.Stderr, "        ⚠ set env %d: %s\n", er.StatusCode(), string(er.Body()))
				continue
			}
			fmt.Fprintf(os.Stderr, "        ✓ %d env vars\n", len(body.EnvVars))
		}

		// Databases → addons.
		for _, it := range byProject[projectSlug] {
			if it.Database == nil {
				continue
			}
			kind := coolify.AddonKindFromCoolify(it.Database.DatabaseType)
			if kind == "" {
				continue
			}
			addonName := coolify.SlugifyName(it.Database.Name)
			ar, err := api.AddAddon(projectSlug, kusoApi.CreateAddonRequest{Name: addonName, Kind: kind})
			if err != nil {
				fmt.Fprintf(os.Stderr, "      ✗ addon %s: %v\n", addonName, err)
				continue
			}
			if ar.StatusCode() >= 300 && ar.StatusCode() != 409 {
				fmt.Fprintf(os.Stderr, "      ✗ addon %s %d: %s\n", addonName, ar.StatusCode(), string(ar.Body()))
				continue
			}
			created++
			fmt.Fprintf(os.Stderr, "      ✓ addon %s (%s)\n", addonName, kind)
		}
	}
	return created
}

func writeDataMigrationScripts(items []coolify.Item, outDir string) int {
	n := 0
	for _, it := range items {
		if it.Database == nil || it.Verdict.Action != "migrate" {
			continue
		}
		name := coolify.SlugifyName(it.Database.Name)
		path := filepath.Join(outDir, "migrate-data-"+name+".sh")
		body := buildDataMigrationScript(it)
		// 0o700: data-migration scripts contain DATABASE_URL with
		// the source addon's password embedded inline. World-readable
		// would leak the password to any other user on the dev box;
		// world-executable serves no purpose since the user runs them
		// from their own shell. Owner-only rwx is the right call.
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "    ⚠ write %s: %v\n", path, err)
			continue
		}
		n++
	}
	return n
}

func buildDataMigrationScript(it coolify.Item) string {
	d := it.Database
	kind := coolify.AddonKindFromCoolify(d.DatabaseType)
	src := d.InternalDBURL
	if src == "" {
		src = "<paste source URL — internal_db_url was empty>"
	}
	header := fmt.Sprintf(`#!/usr/bin/env bash
# Data migration for Coolify database %q (%s).
#
# Coolify side is read-only — this script DUMPS from Coolify and
# loads into the matching kuso addon. Run only when the kuso addon
# is fully provisioned (kuso get addons %s -o json | jq …).
#
# Source URL (Coolify internal_db_url):
#   %s
#
# Destination URL: read from the kuso addon's connection secret —
#   kuso get addons <project> -o json
set -euo pipefail
SRC=%q
DST="${DST:-}"            # set to the kuso addon URL before running
if [[ -z "$DST" ]]; then echo "set DST=<kuso addon connection URL>" >&2; exit 1; fi
`, d.Name, kind, coolify.SlugifyName(it.ProjectName), src, src)

	switch kind {
	case "postgres":
		return header + `# postgres dump → restore. --no-owner avoids role mismatches between
# Coolify's user and kuso's "kuso" role.
pg_dump --no-owner --no-acl "$SRC" | psql "$DST"
`
	case "redis":
		return header + `# redis: scan keys and DUMP/RESTORE each. SAVE on the source
# first so the SNAPSHOT is consistent.
echo "Source: $SRC"
echo "Dest:   $DST"
echo "This is a SKETCH — review redis-cli flags before running."
echo "  redis-cli -u \$SRC --rdb /tmp/dump.rdb"
echo "  redis-cli -u \$DST DEBUG RELOAD … "
`
	default:
		return header + `# kuso doesn't ship a known dump+restore recipe for ` + kind + `.
# Use the engine's native tooling against $SRC and $DST.
echo "implement engine-specific migration here" >&2
exit 1
`
	}
}

// Mapping helpers (slugify, runtime classification, port parsing,
// service-slug derivation, repo-URL normalisation, slug assignment)
// live in github.com/sislelabs/kuso/coolify. The CLI and the
// server-side importer both use that single source of truth, so
// preview verdicts can no longer disagree with apply outcomes the
// way they did when this file kept its own copies (slugify clamped
// to 50 vs 63 chars, runtimeForBuildPack returned "" vs "dockerfile"
// on unknown, parseFirstPort handled "3000:3000" only server-side).

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.AddCommand(migrateCoolifyCmd)
	migrateCoolifyCmd.Flags().StringVar(&migrateCoolifyURL, "coolify-url", "", "Coolify base URL, e.g. https://ops.example.com")
	migrateCoolifyCmd.Flags().StringVar(&migrateCoolifyToken, "coolify-token", "", "Coolify API token (write-scope so env values are returned)")
	migrateCoolifyCmd.Flags().BoolVar(&migrateApply, "apply", false, "actually create kuso resources (default is dry-run)")
	migrateCoolifyCmd.Flags().StringVar(&migrateProjectFilter, "project", "", "only migrate one Coolify project by name")
	migrateCoolifyCmd.Flags().StringVar(&migrateOutDir, "out-dir", "", "write REPORT.md + per-addon migrate-data.sh into this directory")
}
