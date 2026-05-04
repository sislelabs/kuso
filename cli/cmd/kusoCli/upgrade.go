package kusoCli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/spf13/cobra"
)

// `kuso upgrade` — drives the same self-update flow as the web UI's
// "Update available" banner. Hits /api/system/version to discover the
// target tag, POSTs /api/system/update to launch the kube Job, then
// polls /api/system/update/status until the rollout finishes.

var (
	upgradeCheck   bool
	upgradeForce   bool
	upgradeVersion string
)

// versionRe matches vX.Y.Z, optionally with a dash-suffix. Used to
// fail fast when --version is malformed instead of letting the server
// 404 from gh.
var versionRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([-A-Za-z0-9.]+)?$`)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Self-update the kuso server to the latest release",
	Long: `Self-update the kuso server.

Reads /api/system/version to find the latest GitHub release, then
launches a kube Job that swaps the server image. Without --force,
exits early when no update is available.

Pin to a specific release with --version vX.Y.Z. Useful for
re-running the same release, rolling back, or pulling a hotfix
that hasn't propagated to "latest" yet. Pinned upgrades skip the
"needsUpdate" gate (the user explicitly asked for that tag).`,
	Example: `  kuso upgrade --check               # just print version state
  kuso upgrade                       # update to latest
  kuso upgrade --version v0.7.13     # pin to a specific tag
  kuso upgrade --force               # re-run even if already current`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if upgradeVersion != "" && !versionRe.MatchString(upgradeVersion) {
			return fmt.Errorf("--version must look like vX.Y.Z (got %q)", upgradeVersion)
		}
		// Step 1: read current state.
		resp, err := api.RawGet("/api/system/version")
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("version check: %d %s", resp.StatusCode(), string(resp.Body()))
		}
		var v struct {
			Current     string `json:"current"`
			Latest      string `json:"latest"`
			NeedsUpdate bool   `json:"needsUpdate"`
			Breaking    bool   `json:"breaking"`
		}
		if err := json.Unmarshal(resp.Body(), &v); err != nil {
			return fmt.Errorf("decode version: %w", err)
		}
		fmt.Printf("Current: %s\n", v.Current)
		if upgradeVersion != "" {
			fmt.Printf("Target:  %s (pinned)\n", upgradeVersion)
		} else if v.Latest != "" {
			fmt.Printf("Latest:  %s\n", v.Latest)
		}
		if v.Breaking && upgradeVersion == "" {
			fmt.Println("WARNING: latest release is marked breaking — review the changelog before upgrading.")
		}
		if upgradeCheck {
			return nil
		}
		// Pinned upgrades short-circuit the "already up to date" check
		// — the whole point of --version is to override that.
		if upgradeVersion == "" && !v.NeedsUpdate && !upgradeForce {
			fmt.Println("Already up to date.")
			return nil
		}
		if upgradeVersion != "" && upgradeVersion == v.Current && !upgradeForce {
			fmt.Printf("Already on %s.\n", upgradeVersion)
			return nil
		}

		// Step 2: kick the Job. Empty version = "latest" path on the
		// server; non-empty pins the tag.
		fmt.Println("Starting update job…")
		var bodyBytes []byte
		if upgradeVersion != "" {
			bodyBytes, _ = json.Marshal(map[string]string{"version": upgradeVersion})
		}
		startResp, err := api.RawPost("/api/system/update", bodyBytes, "application/json")
		if err != nil {
			return err
		}
		if startResp.StatusCode() >= 300 {
			return fmt.Errorf("start update: %d %s", startResp.StatusCode(), string(startResp.Body()))
		}
		var startBody struct {
			Job string `json:"job"`
		}
		_ = json.Unmarshal(startResp.Body(), &startBody)
		if startBody.Job != "" {
			fmt.Printf("Job: %s\n", startBody.Job)
		}

		// Step 3: poll. The status ConfigMap is updated by the
		// in-cluster updater; we just print phase transitions and
		// quit when we hit a terminal state.
		var lastPhase string
		// Bound the poll loop generously — image pulls + rollouts
		// regularly take a couple minutes on slow links.
		deadline := time.Now().Add(15 * time.Minute)
		for time.Now().Before(deadline) {
			time.Sleep(3 * time.Second)
			sResp, err := api.RawGet("/api/system/update/status")
			if err != nil {
				return err
			}
			var s struct {
				Phase   string `json:"phase"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(sResp.Body(), &s)
			if s.Phase != "" && s.Phase != lastPhase {
				if s.Message != "" {
					fmt.Printf("==> %s — %s\n", s.Phase, s.Message)
				} else {
					fmt.Printf("==> %s\n", s.Phase)
				}
				lastPhase = s.Phase
			}
			switch s.Phase {
			case "Succeeded", "Done":
				fmt.Println("Upgrade finished.")
				return nil
			case "Failed", "Error":
				return fmt.Errorf("upgrade failed: %s", s.Message)
			}
		}
		return fmt.Errorf("upgrade timed out after 15m; check 'kuso status' or the Update page")
	},
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
	upgradeCmd.Flags().BoolVar(&upgradeCheck, "check", false, "only print version info, don't upgrade")
	upgradeCmd.Flags().BoolVar(&upgradeForce, "force", false, "trigger update even if already current")
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "", "pin to a specific release tag (e.g. v0.7.13)")
}
