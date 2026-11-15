package kusoCli

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/spf13/cobra"
)

// `kuso upgrade` — drives the same self-update flow as the web UI's
// "Update available" banner. Hits /api/system/version to discover the
// target tag, POSTs /api/system/update to launch the kube Job, then
// polls /api/system/update/status until the rollout finishes.

var (
	upgradeCheck   bool
	upgradeRefresh bool
	upgradeForce   bool
	upgradeVersion string
	upgradeYes     bool
	// upgradeAllowUnverified accepts a release whose release.json has no
	// asset sha256s / updater digest (releases from before those existed).
	upgradeAllowUnverified bool
	upgradeAllowDowngrade  bool
	upgradeTimeout         time.Duration
)

// versionRe matches vX.Y.Z, optionally with a dash-suffix. Used to
// fail fast when --version is malformed instead of letting the server
// 404 from gh.
var versionRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([-A-Za-z0-9.]+)?$`)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade the kuso SERVER (control plane) to the latest release",
	Long: `Upgrade the kuso server this CLI is logged in to. This does NOT update
the kuso CLI binary itself; re-run the install-cli.sh one-liner for that.

The server rolls to the new image (a short API outage) and the release's
CRDs and platform manifests are applied. It asks for confirmation first;
pass --yes to skip the prompt (required when stdin is not a terminal).

Reads /api/system/version to find the latest GitHub release, then
launches a kube Job that swaps the server image. Without --force,
exits early when no update is available.

Pin to a specific release with --version vX.Y.Z. Useful for
re-running the same release, rolling back, or pulling a hotfix
that hasn't propagated to "latest" yet. Pinned upgrades skip the
"needsUpdate" gate (the user explicitly asked for that tag).`,
	Example: `  kuso upgrade --check               # just print version state
  kuso upgrade                       # update the server to latest (asks first)
  kuso upgrade --yes                 # same, without the prompt
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
		// Step 1: read current state. --refresh forces a synchronous
		// GitHub poll (POST /version/refresh) instead of returning
		// the cached value, which can be up to 6h stale on long-
		// running instances. Useful right after `make ship` when the
		// user wants the cluster to see the new release immediately.
		var resp *resty.Response
		var err error
		if upgradeRefresh {
			resp, err = api.RawPost("/api/system/version/refresh", nil, "application/json")
		} else {
			resp, err = api.RawGet("/api/system/version")
		}
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("version check: %w", err)
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

		target := upgradeVersion
		if target == "" {
			target = v.Latest
		}
		if err := confirmDestructive(upgradeYes, fmt.Sprintf(
			"Upgrade the kuso SERVER at %s from %s to %s? The control plane restarts.",
			api.BaseURL(), v.Current, target)); err != nil {
			return err
		}

		// Step 2: kick the Job. Empty version = "latest" path on the
		// server; non-empty pins the tag.
		fmt.Println("Starting update job…")
		var bodyBytes []byte
		if upgradeVersion != "" || upgradeAllowUnverified || upgradeAllowDowngrade {
			req := map[string]any{}
			if upgradeVersion != "" {
				req["version"] = upgradeVersion
			}
			if upgradeAllowUnverified {
				req["allowUnverified"] = true
			}
			if upgradeAllowDowngrade {
				req["allowDowngrade"] = true
			}
			bodyBytes, _ = json.Marshal(req)
		}
		startResp, err := api.RawPost("/api/system/update", bodyBytes, "application/json")
		if err := checkRespErr(startResp, err); err != nil {
			return fmt.Errorf("start update: %w", err)
		}
		var startBody struct {
			Job string `json:"job"`
		}
		// Reject HTML responses up front. A misconfigured ingress
		// (e.g. cert-manager not yet ready, traefik 503 page) returns
		// a styled HTML response which json.Unmarshal silently
		// accepts as `{Job:""}`. The poll loop then runs the full
		// 15 minutes against the same broken endpoint. Detecting
		// it here makes the failure mode "Upgrade refused" instead
		// of "kuso hung."
		if ct := startResp.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
			return fmt.Errorf("start update: server returned HTML — is the API URL correct? (got %s)", ct)
		}
		_ = json.Unmarshal(startResp.Body(), &startBody)
		if startBody.Job != "" {
			fmt.Printf("Job: %s\n", startBody.Job)
		}

		// Step 3: wait for the in-cluster updater to reach a terminal phase.
		return waitForUpgrade(upgradeTimeout)
	},
}

// waitForUpgrade polls /api/system/update/status, printing each phase
// change, until the updater reports a terminal phase. Shared by
// `kuso upgrade` and `kuso upgrade status --wait`.
func waitForUpgrade(timeout time.Duration) error {
	var lastPhase string
	p := newPoller(3*time.Second, timeout, maxUpgradePollFailures)
	err := p.run("update status", func() (*resty.Response, error) {
		return api.RawGet("/api/system/update/status")
	}, func(body []byte) (bool, error) {
		var s struct {
			Phase   string `json:"phase"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &s)
		if s.Phase != "" && s.Phase != lastPhase {
			if s.Message != "" {
				fmt.Printf("==> %s — %s\n", s.Phase, s.Message)
			} else {
				fmt.Printf("==> %s\n", s.Phase)
			}
			lastPhase = s.Phase
		}
		terminal, err := classifyUpgradePhase(s.Phase, s.Message)
		return terminal, err
	})
	if errors.Is(err, errPollTimeout) {
		return fmt.Errorf("%w; the upgrade may still be running, re-attach with 'kuso upgrade status --wait'", err)
	}
	if err != nil {
		return err
	}
	fmt.Println("Upgrade finished.")
	return nil
}

var upgradeStatusWait bool

var upgradeStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the server's update job state; --wait blocks until it finishes",
	Long: `Print the phase of the most recent server update (started by
'kuso upgrade', the web UI's Update button, or POST /api/system/update).

With --wait, block until that update reaches a terminal phase: exit 0 on
"done", non-zero on "failed" / "rolled-back" / "rollback-failed" or on
timeout. Brief API outages while the server pod is replaced are
tolerated.`,
	Example: `  kuso upgrade status           # one-shot
  kuso upgrade status --wait    # block until the running update finishes`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if upgradeStatusWait {
			return waitForUpgrade(upgradeTimeout)
		}
		resp, err := api.RawGet("/api/system/update/status")
		if _, verr := pollVerdict("update status", resp, err); verr != nil {
			return verr
		}
		var s struct {
			Phase   string `json:"phase"`
			Message string `json:"message"`
			Started string `json:"started"`
			Updated string `json:"updated"`
		}
		if err := json.Unmarshal(resp.Body(), &s); err != nil {
			return fmt.Errorf("decode update status: %w", err)
		}
		if s.Phase == "" {
			fmt.Println("No update has run on this server.")
			return nil
		}
		fmt.Printf("Phase:   %s\nMessage: %s\nStarted: %s\nUpdated: %s\n", s.Phase, s.Message, s.Started, s.Updated)
		return nil
	},
}

// maxUpgradePollFailures is how many consecutive transient status-poll
// failures (3s apart) are tolerated: about a minute, enough for the API
// pod to be replaced.
const maxUpgradePollFailures = 20

// upgradePollVerdict classifies one update-status poll; see pollVerdict.
func upgradePollVerdict(resp *resty.Response, err error) (retry bool, verdict error) {
	return pollVerdict("update status", resp, err)
}

// classifyUpgradePhase decides whether an update-status phase is terminal.
// It matches the phase vocabulary the in-cluster updater actually writes
// (updater.go UpdateStatus.Phase): pending | applying-crds | rolling-server
// | rolling-operator | done | failed, plus rolled-back / rollback-failed from
// the auto-rollback path. The capitalized aliases ("Done", "Succeeded",
// "Failed", "Error") are defensive only — the updater never emits them, and
// matching ONLY those is what made a successful upgrade ("done") fall through
// to a false "timed out after 15m". Returns terminal=false for non-terminal
// (or empty/unknown) phases so the caller keeps polling.
func classifyUpgradePhase(phase, message string) (terminal bool, err error) {
	switch phase {
	case "done", "Done", "Succeeded":
		return true, nil
	case "rolled-back":
		// Upgrade failed but the updater auto-rolled back to the prior
		// version — the instance is healthy on the OLD tag.
		return true, fmt.Errorf("upgrade failed and was rolled back to the previous version: %s", message)
	case "failed", "Failed", "Error", "rollback-failed":
		return true, fmt.Errorf("upgrade failed: %s", message)
	default:
		return false, nil
	}
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
	upgradeCmd.AddCommand(upgradeStatusCmd)
	upgradeStatusCmd.Flags().BoolVar(&upgradeStatusWait, "wait", false, "block until the update reaches a terminal phase; non-zero exit on failure")
	upgradeStatusCmd.Flags().DurationVar(&upgradeTimeout, "timeout", 20*time.Minute, "how long --wait waits")
	upgradeCmd.Flags().BoolVar(&upgradeCheck, "check", false, "only print version info, don't upgrade")
	upgradeCmd.Flags().BoolVar(&upgradeRefresh, "refresh", false, "force a synchronous GitHub poll instead of using the 6h-cached value")
	upgradeCmd.Flags().BoolVar(&upgradeForce, "force", false, "trigger update even if already current")
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "", "pin to a specific release tag (e.g. v0.7.13)")
	upgradeCmd.Flags().BoolVarP(&upgradeYes, "yes", "y", false, "skip the confirmation prompt")
	upgradeCmd.Flags().BoolVar(&upgradeAllowUnverified, "allow-unverified", false, "accept a release whose release.json can't pin its assets or updater image by hash (older releases); the server refuses them otherwise")
	upgradeCmd.Flags().DurationVar(&upgradeTimeout, "timeout", 20*time.Minute, "how long to wait for the update to finish")
	upgradeCmd.Flags().BoolVar(&upgradeAllowDowngrade, "allow-downgrade", false, "allow --version to roll back to a release older than the running one (admin only)")
}
