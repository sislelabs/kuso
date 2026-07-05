package kusoCli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// `kuso github` — inspect GitHub App state and connected repos.
//
//   kuso github status                    -> install URL + configured?
//   kuso github installations [-o json]   -> orgs/users with the App installed
//   kuso github repos <installation-id>   -> repos accessible via that install
//   kuso github refresh                   -> repull from GitHub

var githubCmd = &cobra.Command{
	Use:   "github",
	Short: "Inspect GitHub App state",
}

var githubStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show GitHub App install URL + configured state",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetInstallURL()
		if err != nil {
			return err
		}
		var data map[string]any
		_ = json.Unmarshal(resp.Body(), &data)
		fmt.Printf("configured: %v\n", data["configured"])
		if u, ok := data["url"].(string); ok && u != "" {
			fmt.Printf("install URL: %s\n", u)
		}
		return nil
	},
}

var githubInstallationsCmd = &cobra.Command{
	Use:     "installations",
	Aliases: []string{"installs"},
	Short:   "List orgs/users with the kuso GitHub App installed",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListInstallations()
		if err != nil {
			return err
		}
		var items []map[string]any
		_ = json.Unmarshal(resp.Body(), &items)
		sort.Slice(items, func(i, j int) bool {
			return asString(items[i]["accountLogin"]) < asString(items[j]["accountLogin"])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "ACCOUNT", "TYPE", "REPOS"})
			for _, i := range items {
				repos := 0
				if r, ok := i["repositories"].([]any); ok {
					repos = len(r)
				}
				idStr := ""
				if f, ok := i["id"].(float64); ok {
					idStr = fmt.Sprintf("%.0f", f)
				} else {
					idStr = asString(i["id"])
				}
				t.Append([]string{
					idStr,
					asString(i["accountLogin"]),
					asString(i["accountType"]),
					fmt.Sprintf("%d", repos),
				})
			}
			t.Render()
			return nil
		}
	},
}

var githubReposCmd = &cobra.Command{
	Use:   "repos <installation-id>",
	Short: "List repos accessible via an installation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		id, perr := strconv.ParseInt(args[0], 10, 64)
		if perr != nil || id <= 0 {
			return fmt.Errorf("installation id must be a positive integer, got %q", args[0])
		}
		resp, err := api.ListInstallationRepos(id)
		if err != nil {
			return err
		}
		var items []map[string]any
		_ = json.Unmarshal(resp.Body(), &items)
		sort.Slice(items, func(i, j int) bool {
			return asString(items[i]["fullName"]) < asString(items[j]["fullName"])
		})
		switch outputFormat {
		case "json":
			return jsonOut(items)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"FULL NAME", "DEFAULT BRANCH", "PRIVATE"})
			for _, i := range items {
				t.Append([]string{
					asString(i["fullName"]),
					asString(i["defaultBranch"]),
					boolText(i["private"]),
				})
			}
			t.Render()
			return nil
		}
	},
}

var githubRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh the cached installation list from GitHub",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RefreshInstallations()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Println("installations refreshed")
		return nil
	},
}

// `kuso github configure` — admin-only command that POSTs the App
// credentials to /api/github/configure (same endpoint the dashboard
// wizard uses). Two input modes:
//
//   - --env-file path/to/github-app.env  (one KEY=VALUE per line:
//     APP_ID, APP_SLUG, CLIENT_ID, CLIENT_SECRET, WEBHOOK_SECRET, ORG)
//     plus --pem path/to/private-key.pem
//
//   - individual --app-id, --app-slug, --client-id, --client-secret,
//     --webhook-secret, --org flags + --pem
//
// The env-file form mirrors install.sh's KUSO_GITHUB_APP_ENV format
// so a single `github-app.env` works everywhere (install-time wizard,
// post-install reconfigure, CI scripts).

var (
	gcEnvFile        string
	gcPEMPath        string
	gcAppID          string
	gcAppSlug        string
	gcClientID       string
	gcClientSecret   string
	gcWebhookSecret  string
	gcOrg            string
	gcSkipWaitHealth bool
)

var githubConfigureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Set or update the GitHub App credentials on this kuso instance",
	Long: `Configure the GitHub App credentials.

Posts to /api/github/configure (admin-only) which writes the
kuso-github-app Secret and restarts deploy/kuso-server so the new
env loads. ~30s of downtime; the command polls /healthz until back.

Two input modes — env-file is the way if you already have one from
install.sh:

  # env-file (KEY=VALUE per line) + .pem
  kuso github configure \
    --env-file /etc/kuso/github-app.env \
    --pem /etc/kuso/github-app.pem

  # individual flags + .pem
  kuso github configure \
    --app-id 3567321 --app-slug kuso-sislelabs \
    --client-id Iv23li... --client-secret <secret> \
    --webhook-secret <secret> --org sislelabs \
    --pem ~/Downloads/kuso-sislelabs.pem`,
	Args: cobra.NoArgs,
	RunE: runGithubConfigure,
}

func runGithubConfigure(cmd *cobra.Command, args []string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}

	body := configurePayload{}

	// Apply env-file first (lowest priority), then per-flag overrides.
	if gcEnvFile != "" {
		envs, err := readEnvFile(gcEnvFile)
		if err != nil {
			return fmt.Errorf("read env-file: %w", err)
		}
		body.AppID = envs["APP_ID"]
		body.AppSlug = envs["APP_SLUG"]
		body.ClientID = envs["CLIENT_ID"]
		body.ClientSecret = envs["CLIENT_SECRET"]
		body.WebhookSecret = envs["WEBHOOK_SECRET"]
		body.Org = envs["ORG"]
	}
	if gcAppID != "" {
		body.AppID = gcAppID
	}
	if gcAppSlug != "" {
		body.AppSlug = gcAppSlug
	}
	if gcClientID != "" {
		body.ClientID = gcClientID
	}
	if gcClientSecret != "" {
		body.ClientSecret = gcClientSecret
	}
	if gcWebhookSecret != "" {
		body.WebhookSecret = gcWebhookSecret
	}
	if gcOrg != "" {
		body.Org = gcOrg
	}

	if gcPEMPath == "" {
		return errors.New("--pem is required (path to the GitHub App private key .pem)")
	}
	pemBytes, err := os.ReadFile(gcPEMPath)
	if err != nil {
		return fmt.Errorf("read pem: %w", err)
	}
	body.PrivateKey = string(pemBytes)

	// Lightweight client-side validation. The server validates again
	// (PEM parse, numeric ID), but failing here saves a round-trip
	// when a flag is just missing.
	for _, m := range []struct {
		name string
		val  string
	}{
		{"app-id", body.AppID},
		{"app-slug", body.AppSlug},
		{"client-id", body.ClientID},
		{"client-secret", body.ClientSecret},
		{"webhook-secret", body.WebhookSecret},
	} {
		if strings.TrimSpace(m.val) == "" {
			return fmt.Errorf("--%s required (or set in --env-file)", m.name)
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	resp, err := api.RawPost("/api/github/configure", payload, "application/json")
	if err != nil {
		return err
	}
	if resp.StatusCode() >= 300 {
		return fmt.Errorf("configure failed: %d %s", resp.StatusCode(), string(resp.Body()))
	}
	fmt.Println("GitHub App credentials saved.")

	if gcSkipWaitHealth {
		fmt.Println("kuso-server is restarting; skip-wait-health was set, exiting.")
		return nil
	}

	// Poll /healthz until the new pod's serving (~30s typically).
	// Status will fluctuate during the rollout (old pod 200 → 502 →
	// new pod 200) — we wait for two consecutive 200s to call it
	// done, otherwise we'd land on the old pod's last gasp.
	fmt.Println("Waiting for kuso-server to come back…")
	good := 0
	deadline := time.Now().Add(2 * time.Minute)
	healthURL := strings.TrimRight(api.BaseURL(), "/") + "/healthz"
	hc := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		hresp, err := hc.Get(healthURL)
		if err == nil && hresp.StatusCode == 200 {
			_ = hresp.Body.Close()
			good++
			if good >= 2 {
				fmt.Println("kuso-server is back online.")
				return nil
			}
			continue
		}
		if hresp != nil {
			_ = hresp.Body.Close()
		}
		good = 0
	}
	return fmt.Errorf("kuso-server didn't return to /healthz within 2m; check logs")
}

// configurePayload mirrors server-go's configureRequest. Tags must
// match exactly — the server decodes by JSON tag.
type configurePayload struct {
	AppID         string `json:"appId"`
	AppSlug       string `json:"appSlug"`
	ClientID      string `json:"clientId"`
	ClientSecret  string `json:"clientSecret"`
	WebhookSecret string `json:"webhookSecret"`
	PrivateKey    string `json:"privateKey"`
	Org           string `json:"org,omitempty"`
}

// readEnvFile parses a flat KEY=VALUE file. Permissive: ignores blank
// lines, ignores #-comments, tolerates surrounding whitespace, strips
// double-quoted or single-quoted values cleanly. Same shape install.sh
// reads from KUSO_GITHUB_APP_ENV.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	scn := bufio.NewScanner(f)
	for scn.Scan() {
		line := strings.TrimSpace(scn.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Strip surrounding quotes if present.
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[key] = val
	}
	return out, scn.Err()
}

// ---- Repo probes: check-repo / detect-runtime / scan-addons ----
//
// These POST a {installationId, owner, repo, [branch], [path]} body to
// the matching /api/github/* endpoint. They act on a single repo the
// caller names via --repo owner/name (non-admin server-side).
//
//   kuso github check-repo     --repo owner/name [--installation-id N]
//   kuso github detect-runtime --repo owner/name [--ref branch] [--path sub/dir] [--installation-id N]
//   kuso github scan-addons    --repo owner/name [--ref branch] [--path sub/dir] [--installation-id N]
//
// Server body shapes (verified against server-go handlers):
//   check-repo:     {installationId, owner, repo}   — installationId 0
//                   is auto-resolved server-side; always returns 200
//                   with {ok, ...} (even on access failure).
//   detect-runtime: {installationId, owner, repo, branch, path} — server
//                   REQUIRES installationId!=0 AND branch!="" (400 else).
//   scan-addons:    same required fields as detect-runtime.
//
// Because detect-runtime/scan-addons need a non-zero installationId and
// a branch, this CLI resolves both client-side when the user omits them:
// installationId by matching --repo's owner against the cached
// installation list, branch by that installation's repo default branch.

var (
	ghRepoFlag      string
	ghRefFlag       string
	ghPathFlag      string
	ghInstallIDFlag int64
)

// splitRepo turns "owner/name" into (owner, name). Rejects anything
// that isn't exactly two non-empty slash-separated parts.
func splitRepo(s string) (owner, repo string, err error) {
	owner, repo, ok := strings.Cut(s, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("--repo must be owner/name, got %q", s)
	}
	return owner, repo, nil
}

// resolveInstallation returns the installation id whose account matches
// owner. Used when the caller didn't pass --installation-id. Returns 0
// (no error) if nothing matches — the caller decides whether that's fatal.
func resolveInstallation(owner string) (int64, error) {
	resp, err := api.ListInstallations()
	if err := checkRespErr(resp, err); err != nil {
		return 0, err
	}
	var items []map[string]any
	if err := json.Unmarshal(resp.Body(), &items); err != nil {
		return 0, err
	}
	for _, it := range items {
		if strings.EqualFold(asString(it["accountLogin"]), owner) {
			if f, ok := it["id"].(float64); ok {
				return int64(f), nil
			}
		}
	}
	return 0, nil
}

// resolveDefaultBranch returns the default branch of owner/repo as seen
// through the given installation. Best-effort — "" if not found.
func resolveDefaultBranch(installID int64, owner, repo string) string {
	resp, err := api.ListInstallationRepos(installID)
	if err := checkRespErr(resp, err); err != nil {
		return ""
	}
	var items []map[string]any
	if err := json.Unmarshal(resp.Body(), &items); err != nil {
		return ""
	}
	want := owner + "/" + repo
	for _, it := range items {
		if strings.EqualFold(asString(it["fullName"]), want) {
			return asString(it["defaultBranch"])
		}
	}
	return ""
}

var githubCheckRepoCmd = &cobra.Command{
	Use:   "check-repo",
	Short: "Check whether the GitHub App can access a repo",
	Args:  cobra.NoArgs,
	Example: `  kuso github check-repo --repo sislelabs/kuso
  kuso github check-repo --repo owner/name --installation-id 12345678`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		owner, repo, err := splitRepo(ghRepoFlag)
		if err != nil {
			return err
		}
		// installationId 0 is fine here — the server auto-resolves it.
		body, _ := json.Marshal(map[string]any{
			"installationId": ghInstallIDFlag,
			"owner":          owner,
			"repo":           repo,
		})
		resp, err := api.RawPost("/api/github/check-repo", body, "application/json")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("check repo: %w", err)
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return jsonOut(data)
	},
}

var githubDetectRuntimeCmd = &cobra.Command{
	Use:   "detect-runtime",
	Short: "Auto-detect runtime + port from a repo",
	Args:  cobra.NoArgs,
	Example: `  kuso github detect-runtime --repo sislelabs/kuso
  kuso github detect-runtime --repo owner/name --ref main --path services/api`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGithubRepoProbe("/api/github/detect-runtime", "detect runtime")
	},
}

var githubScanAddonsCmd = &cobra.Command{
	Use:   "scan-addons",
	Short: "Suggest addon kinds from a repo's env/compose hints",
	Args:  cobra.NoArgs,
	Example: `  kuso github scan-addons --repo sislelabs/kuso
  kuso github scan-addons --repo owner/name --ref main`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGithubRepoProbe("/api/github/scan-addons", "scan addons")
	},
}

// runGithubRepoProbe is shared by detect-runtime + scan-addons: both
// take the same {installationId, owner, repo, branch, path} body and the
// server requires a non-zero installationId + a non-empty branch. We
// resolve both client-side when the caller omits them.
func runGithubRepoProbe(path, label string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	owner, repo, err := splitRepo(ghRepoFlag)
	if err != nil {
		return err
	}
	installID := ghInstallIDFlag
	if installID == 0 {
		installID, err = resolveInstallation(owner)
		if err != nil {
			return fmt.Errorf("resolve installation: %w", err)
		}
		if installID == 0 {
			return fmt.Errorf("no GitHub App installation found for owner %q — pass --installation-id or install the kuso App on that account", owner)
		}
	}
	branch := ghRefFlag
	if branch == "" {
		branch = resolveDefaultBranch(installID, owner, repo)
		if branch == "" {
			return fmt.Errorf("could not resolve default branch for %s/%s — pass --ref", owner, repo)
		}
	}
	body, _ := json.Marshal(map[string]any{
		"installationId": installID,
		"owner":          owner,
		"repo":           repo,
		"branch":         branch,
		"path":           ghPathFlag,
	})
	resp, err := api.RawPost(path, body, "application/json")
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	var data any
	if err := json.Unmarshal(resp.Body(), &data); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return jsonOut(data)
}

func init() {
	rootCmd.AddCommand(githubCmd)
	githubCmd.AddCommand(githubStatusCmd)
	githubCmd.AddCommand(githubInstallationsCmd)
	githubCmd.AddCommand(githubReposCmd)
	githubCmd.AddCommand(githubRefreshCmd)
	githubCmd.AddCommand(githubConfigureCmd)
	githubCmd.AddCommand(githubCheckRepoCmd)
	githubCmd.AddCommand(githubDetectRuntimeCmd)
	githubCmd.AddCommand(githubScanAddonsCmd)

	for _, c := range []*cobra.Command{githubCheckRepoCmd, githubDetectRuntimeCmd, githubScanAddonsCmd} {
		c.Flags().StringVar(&ghRepoFlag, "repo", "", "target repo as owner/name (required)")
		c.Flags().Int64Var(&ghInstallIDFlag, "installation-id", 0, "installation id (0 = auto-resolve from owner)")
	}
	for _, c := range []*cobra.Command{githubDetectRuntimeCmd, githubScanAddonsCmd} {
		c.Flags().StringVar(&ghRefFlag, "ref", "", "git branch (default: repo's default branch)")
		c.Flags().StringVar(&ghPathFlag, "path", "", "subdirectory within the repo")
	}
	githubCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	githubConfigureCmd.Flags().StringVar(&gcEnvFile, "env-file", "", "KEY=VALUE file with APP_ID/APP_SLUG/CLIENT_ID/CLIENT_SECRET/WEBHOOK_SECRET/ORG")
	githubConfigureCmd.Flags().StringVar(&gcPEMPath, "pem", "", "path to the GitHub App private key .pem (required)")
	githubConfigureCmd.Flags().StringVar(&gcAppID, "app-id", "", "GitHub App ID (numeric)")
	githubConfigureCmd.Flags().StringVar(&gcAppSlug, "app-slug", "", "GitHub App slug (URL fragment)")
	githubConfigureCmd.Flags().StringVar(&gcClientID, "client-id", "", "OAuth client ID")
	githubConfigureCmd.Flags().StringVar(&gcClientSecret, "client-secret", "", "OAuth client secret")
	githubConfigureCmd.Flags().StringVar(&gcWebhookSecret, "webhook-secret", "", "webhook HMAC secret (set the same value on the GitHub App)")
	githubConfigureCmd.Flags().StringVar(&gcOrg, "org", "", "GitHub org slug (informational)")
	githubConfigureCmd.Flags().BoolVar(&gcSkipWaitHealth, "skip-wait-health", false, "don't poll /healthz after — return immediately")
}
