package kusoCli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/coolify"
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
		inv, err := coolify.Snapshot(c)
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
		applied := applyMigration(c, items)
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
	slugFor := assignKusoSlugs(order)
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

func applyMigration(c *coolify.Client, items []coolify.Item) int {
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
	slugFor := assignKusoSlugs(coolifyOrder)
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
				defaultRepoURL = "https://github.com/" + strings.TrimSuffix(it.App.GitRepository, ".git")
				defaultBranch = it.App.GitBranch
				break
			}
		}
		if defaultRepoURL == "" {
			fmt.Fprintf(os.Stderr, "    ⚠ no git-backed app — skipping project (kuso requires a defaultRepo)\n")
			continue
		}

		req := kusoApi.CreateProjectRequest{Name: projectSlug}
		req.DefaultRepo.URL = defaultRepoURL
		req.DefaultRepo.DefaultBranch = defaultBranch
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
			svcSlug := serviceSlugFromApp(it.App)
			svcReq := kusoApi.CreateServiceRequest{
				Name:    svcSlug,
				Runtime: runtimeForBuildPack(it.App.BuildPack),
				Port:    parseFirstPort(it.App.PortsExposes),
			}
			svcReq.Repo.URL = "https://github.com/" + strings.TrimSuffix(it.App.GitRepository, ".git")
			svcReq.Repo.Path = it.App.BaseDirectory
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

			envs, err := c.ListApplicationEnvs(it.App.UUID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "        ⚠ list envs: %v\n", err)
				continue
			}
			body := kusoApi.SetEnvRequest{EnvVars: []map[string]any{}}
			for _, e := range envs {
				if e.IsCoolify {
					continue
				}
				body.EnvVars = append(body.EnvVars, map[string]any{
					"name":  e.Key,
					"value": e.EffectiveValue(),
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
			addonName := slugifyName(it.Database.Name)
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
		name := slugifyName(it.Database.Name)
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
`, d.Name, kind, slugifyName(it.ProjectName), src, src)

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

// assignKusoSlugs takes a list of Coolify project names in source
// order and returns a map of Coolify name → kuso slug. When two
// projects slugify to the same base, the first wins the bare slug
// and subsequent ones get "-2", "-3", etc. Used by both the report
// renderer and the apply path so previews and writes agree.
func assignKusoSlugs(coolifyNames []string) map[string]string {
	out := map[string]string{}
	used := map[string]int{}
	for _, name := range coolifyNames {
		base := slugifyName(name)
		if base == "" {
			base = "x-unnamed"
		}
		used[base]++
		slug := base
		if used[base] > 1 {
			slug = fmt.Sprintf("%s-%d", base, used[base])
		}
		out[name] = slug
	}
	return out
}

// serviceSlugFromApp derives a short kuso service name from a
// Coolify Application. Order of preference:
//  1. Last path segment of GitRepository ("biznesguys/foo" → "foo")
//  2. Slugified Application.Name (the full ugly Coolify name)
// Either way we run through slugifyName for the kube-safety + length
// guarantees.
func serviceSlugFromApp(a *coolify.Application) string {
	if a.GitRepository != "" {
		// Take the last "/"-delimited segment, strip ":branch-uuid"
		// suffix Coolify sometimes appends.
		repo := a.GitRepository
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			repo = repo[i+1:]
		}
		if i := strings.Index(repo, ":"); i >= 0 {
			repo = repo[:i]
		}
		repo = strings.TrimSuffix(repo, ".git")
		if s := slugifyName(repo); s != "" {
			return s
		}
	}
	return slugifyName(a.Name)
}

func runtimeForBuildPack(bp string) string {
	switch strings.ToLower(bp) {
	case "nixpacks":
		return "nixpacks"
	case "dockerfile":
		return "dockerfile"
	case "static":
		return "static"
	}
	return ""
}

func parseFirstPort(s string) int {
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// slugifyName turns a Coolify name into a kube-safe slug. Lowercase,
// replace runs of non-[a-z0-9] with "-", trim leading/trailing dashes,
// truncate to 50 chars (kuso adds suffixes per environment, so leave
// headroom under the 63-byte DNS label limit).
func slugifyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var out strings.Builder
	prevDash := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				out.WriteRune('-')
				prevDash = true
			}
		}
	}
	res := strings.Trim(out.String(), "-")
	if len(res) > 50 {
		res = strings.Trim(res[:50], "-")
	}
	if res == "" {
		res = "x-unnamed"
	}
	return res
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.AddCommand(migrateCoolifyCmd)
	migrateCoolifyCmd.Flags().StringVar(&migrateCoolifyURL, "coolify-url", "", "Coolify base URL, e.g. https://ops.example.com")
	migrateCoolifyCmd.Flags().StringVar(&migrateCoolifyToken, "coolify-token", "", "Coolify API token (write-scope so env values are returned)")
	migrateCoolifyCmd.Flags().BoolVar(&migrateApply, "apply", false, "actually create kuso resources (default is dry-run)")
	migrateCoolifyCmd.Flags().StringVar(&migrateProjectFilter, "project", "", "only migrate one Coolify project by name")
	migrateCoolifyCmd.Flags().StringVar(&migrateOutDir, "out-dir", "", "write REPORT.md + per-addon migrate-data.sh into this directory")
}
