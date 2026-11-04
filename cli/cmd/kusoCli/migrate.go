package kusoCli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	// Flag-driven, no positionals. Without this cobra defaults to
	// ArbitraryArgs and RunE never looks at args, so
	// `kuso migrate coolify myproject --apply` silently migrated the
	// ENTIRE instance while the user believed they had scoped it.
	Args: cobra.NoArgs,
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
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(migrateCoolifyURL)), "http://") {
			fmt.Fprintln(os.Stderr, "⚠ --coolify-url is plain http:// — the Coolify API token (root-scoped) is sent in cleartext. Use https:// or an SSH tunnel unless this network is trusted.")
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
			if err := os.WriteFile(rp, []byte(report), 0o600); err != nil {
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
		stats := applyMigration(cmd.Context(), c, items)
		fmt.Fprintf(os.Stderr, "→ applied: %d created, %d skipped (already exist), %d failed\n",
			stats.created, stats.skipped, stats.failed)

		if migrateOutDir != "" {
			n := writeDataMigrationScripts(items, migrateOutDir)
			fmt.Fprintf(os.Stderr, "→ wrote %d migrate-data.sh stubs to %s\n", n, migrateOutDir)
		}
		// Partial failure must not exit 0 — scripts chaining on this
		// command would treat a half-migrated instance as done.
		if stats.failed > 0 {
			return fmt.Errorf("%d resource(s) failed to migrate — see ✗/⚠ lines above", stats.failed)
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
	// Slugs come from migrationProjectSlugs, the same function apply
	// uses, so the report names exactly the projects apply creates.
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
	slugFor := migrationProjectSlugs(items)
	for _, name := range order {
		header := name
		if kusoSlug, ok := slugFor[name]; ok {
			header = fmt.Sprintf("%s · kuso project `%s`", name, kusoSlug)
		}
		fmt.Fprintf(&b, "### %s\n\n", header)
		fmt.Fprintf(&b, "| action | name | kind | reason |\n| --- | --- | --- | --- |\n")
		for _, it := range byCoolifyName[name] {
			tag := actionEmoji(it.Verdict.Action)
			detail := it.Verdict.Reason
			if it.ProjectName == "" && it.Verdict.Action == "migrate" {
				tag = "✗"
				detail = "no Coolify project/environment resolved — will FAIL on --apply · " + detail
			}
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

// migrationStats is the per-resource outcome tally for --apply.
// created = newly created on kuso; skipped = 409 conflicts (already
// exist, fine on re-runs); failed = anything that errored and was NOT
// applied. The command exits non-zero when failed > 0.
type migrationStats struct {
	created int
	skipped int
	failed  int
}

// migrationProjectSlugs assigns kuso project slugs to the Coolify
// projects apply will actually create — those with at least one migrate
// item — in first-seen order. The report and apply both call this so
// the promised names can't drift from the created ones.
func migrationProjectSlugs(items []coolify.Item) map[string]string {
	var names []string
	seen := map[string]bool{}
	for _, it := range items {
		if it.Verdict.Action != "migrate" || it.ProjectName == "" || seen[it.ProjectName] {
			continue
		}
		seen[it.ProjectName] = true
		names = append(names, it.ProjectName)
	}
	return coolify.AssignKusoSlugs(names)
}

func applyMigration(ctx context.Context, c *coolify.Client, items []coolify.Item) migrationStats {
	var stats migrationStats
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
			// The inventory couldn't resolve this item's project (GetProject
			// failed or its environment is gone). It was meant to migrate and
			// won't, so it's a failure — not a silent skip with exit 0.
			fmt.Fprintf(os.Stderr, "  ✗ %s %s: no Coolify project resolved — not migrated\n", coolify.ItemKind(it), it.Name)
			stats.failed++
			continue
		}
		if _, ok := byCoolifyName[it.ProjectName]; !ok {
			coolifyOrder = append(coolifyOrder, it.ProjectName)
		}
		byCoolifyName[it.ProjectName] = append(byCoolifyName[it.ProjectName], it)
	}
	slugFor := migrationProjectSlugs(items)
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
			stats.failed += len(byProject[projectSlug])
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
			stats.failed++
			continue
		}
		switch {
		case resp.StatusCode() == 409:
			fmt.Fprintf(os.Stderr, "    · project exists (ok)\n")
			stats.skipped++
		case resp.StatusCode() >= 300:
			fmt.Fprintf(os.Stderr, "    ✗ create project: %s\n", apiErrorMessage(resp.StatusCode(), string(resp.Body())))
			stats.failed++
			continue
		default:
			fmt.Fprintf(os.Stderr, "    ✓ project created\n")
			stats.created++
		}

		// Apps → services + envs.
		svcSlugFor := assignServiceSlugs(byProject[projectSlug])
		for _, it := range byProject[projectSlug] {
			if it.App == nil {
				continue
			}
			svcSlug := svcSlugFor[it.App]
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
				stats.failed++
				continue
			}
			if sr.StatusCode() >= 300 && sr.StatusCode() != 409 {
				fmt.Fprintf(os.Stderr, "      ✗ service %s %d: %s\n", svcSlug, sr.StatusCode(), string(sr.Body()))
				stats.failed++
				continue
			}
			if sr.StatusCode() == 409 {
				// Conflict = already exists. Fine on a re-run of the same
				// app, but a service built from another repo/path is someone
				// else's: writing this app's env into it would silently
				// repoint it (e.g. prod picking up staging's DATABASE_URL).
				if err := checkExistingServiceRepo(projectSlug, svcSlug, svcReq.Repo); err != nil {
					fmt.Fprintf(os.Stderr, "      ✗ service %s: %v — not writing env\n", svcSlug, err)
					stats.failed++
					continue
				}
				stats.skipped++
				fmt.Fprintf(os.Stderr, "      · service %s exists (ok)\n", svcSlug)
			} else {
				stats.created++
				fmt.Fprintf(os.Stderr, "      ✓ service %s\n", svcSlug)
			}

			envs, err := c.ListApplicationEnvs(ctx, it.App.UUID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "        ⚠ list envs: %v\n", err)
				stats.failed++
				continue
			}
			vars := coolify.SelectEnvVars(envs)
			if len(vars) == 0 {
				continue
			}
			n, err := importCoolifyEnvVars(projectSlug, svcSlug, vars)
			if err != nil {
				fmt.Fprintf(os.Stderr, "        ⚠ set env (%d/%d written): %v\n", n, len(vars), err)
				stats.failed++
				continue
			}
			fmt.Fprintf(os.Stderr, "        ✓ %d env vars\n", n)
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
				stats.failed++
				continue
			}
			if ar.StatusCode() >= 300 && ar.StatusCode() != 409 {
				fmt.Fprintf(os.Stderr, "      ✗ addon %s %d: %s\n", addonName, ar.StatusCode(), string(ar.Body()))
				stats.failed++
				continue
			}
			if ar.StatusCode() == 409 {
				stats.skipped++
				fmt.Fprintf(os.Stderr, "      · addon %s exists (ok)\n", addonName)
			} else {
				stats.created++
				fmt.Fprintf(os.Stderr, "      ✓ addon %s (%s)\n", addonName, kind)
			}
		}
	}
	return stats
}

// assignServiceSlugs gives every app in one kuso project its own service
// name. ServiceSlugFromApp only looks at the repo basename, so a monorepo's
// apps, or a prod and a staging app from one repo, all derive the same
// slug; later ones get -2, -3, …. Apps are numbered in Coolify UUID order
// so a re-run maps each app back onto the same service.
func assignServiceSlugs(items []coolify.Item) map[*coolify.Application]string {
	var apps []*coolify.Application
	for _, it := range items {
		if it.App != nil {
			apps = append(apps, it.App)
		}
	}
	sort.SliceStable(apps, func(i, j int) bool { return apps[i].UUID < apps[j].UUID })
	out := make(map[*coolify.Application]string, len(apps))
	taken := map[string]bool{}
	for _, a := range apps {
		out[a] = coolify.UniqueSlug(coolify.ServiceSlugFromApp(a), taken)
	}
	return out
}

// checkExistingServiceRepo confirms a service that already exists (409 on
// create) was built from the same repo URL + path this app wants. Anything
// else — a different repo, a different path, or a spec we can't read — is
// an error, so the caller never writes env into an unrelated service.
func checkExistingServiceRepo(project, service string, want *kusoApi.ServiceRepoSpec) error {
	resp, err := api.GetService(project, service)
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("exists, and its repo can't be read to confirm it's this app: %w", err)
	}
	var got struct {
		Spec struct {
			Repo struct {
				URL  string `json:"url"`
				Path string `json:"path"`
			} `json:"repo"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(resp.Body(), &got); err != nil {
		return fmt.Errorf("exists, and its spec can't be decoded: %w", err)
	}
	if repoKey(got.Spec.Repo.URL) != repoKey(want.URL) || strings.Trim(got.Spec.Repo.Path, "/") != strings.Trim(want.Path, "/") {
		return fmt.Errorf("already exists for a different repo (%s path=%q, this app is %s path=%q)",
			got.Spec.Repo.URL, got.Spec.Repo.Path, want.URL, want.Path)
	}
	return nil
}

// repoKey reduces a repo URL to host+path for comparison, ignoring scheme,
// credentials (the server redacts them), case of the host and ".git".
func repoKey(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".git")
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return strings.ToLower(u.Host) + strings.TrimSuffix(u.Path, "/")
	}
	return s
}

// importCoolifyEnvVars writes each var through the same per-key
// {value, auto:true} path `kuso env set` uses, so the server routes
// credentials into the managed <service>-secrets Secret instead of
// leaving them as plaintext CR literals (the legacy whole-list POST /env
// did). Build-time vars need no special case: service env reaches the
// build as build env, with secret-backed values forwarded as buildkit
// secret mounts rather than baked buildArgs.
func importCoolifyEnvVars(project, service string, vars []struct{ Key, Value string }) (int, error) {
	n := 0
	for _, v := range vars {
		resp, err := api.SetEnvVar(project, service, v.Key, kusoApi.SetEnvVarRequest{Value: v.Value, Auto: true})
		if err := checkRespErr(resp, err); err != nil {
			return n, fmt.Errorf("%s: %w", v.Key, err)
		}
		n++
	}
	return n, nil
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
	// external_db_url is reachable from the operator's machine;
	// internal_db_url is a docker-internal hostname that only resolves
	// on the Coolify host, so it's the fallback, not the default.
	src, srcField := d.ExternalDBURL, "external_db_url"
	if src == "" {
		src, srcField = d.InternalDBURL, "internal_db_url (docker-internal — run this on the Coolify host or swap in a reachable URL)"
	}
	if src == "" {
		src, srcField = "<paste source URL — Coolify returned none>", "none"
	}
	// The URL carries the source password: single-quote it so bash runs
	// no $(…)/backtick substitution on it, and keep it out of comments
	// where a newline in it would end the comment.
	header := fmt.Sprintf(`#!/usr/bin/env bash
# Data migration for Coolify database %s (%s).
#
# Coolify side is read-only — this script DUMPS from Coolify and
# loads into the matching kuso addon. Run only when the kuso addon
# is fully provisioned (kuso get addons %s -o json | jq …).
#
# Source URL: SRC below, from Coolify %s.
#
# Destination URL: read from the kuso addon's connection secret —
#   kuso get addons <project> -o json
set -euo pipefail
SRC=%s
DST="${DST:-}"            # set to the kuso addon URL before running
if [[ -z "$DST" ]]; then echo "set DST=<kuso addon connection URL>" >&2; exit 1; fi
`, coolify.SlugifyName(d.Name), kind, coolify.SlugifyName(it.ProjectName), srcField, shellQuote(src))

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
