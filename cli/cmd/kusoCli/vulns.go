// kuso vulns — image vulnerability scanning (Trivy). Reads per-image
// findings, forces rescans, sets the per-service deploy-gate policy and
// manages CVE ignores. docs/VULN_SCANNING.md is the contract.

package kusoCli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// Each command binds its own output variable; see output_format_test.go
// for why the shared outputFormat must not be reused here.
var (
	vulnsListOutput     string
	vulnsShowOutput     string
	vulnsScanOutput     string
	vulnsPolicyOutput   string
	vulnsPlatformOutput string
	vulnsScanAllOutput  string
	vulnsSettingsGetOut string
	vulnsSettingsSetOut string

	vulnsShowEnv, vulnsShowAddon, vulnsShowSeverity string
	vulnsShowFixable, vulnsShowHideIgnored          bool

	vulnsScanEnv, vulnsScanAddon string
	vulnsScanWait                bool

	vulnsPlatformDigest string

	vulnsSetMode, vulnsSetNotifyMin          string
	vulnsSetScanAddons, vulnsSetScanPlatform bool
	vulnsSetNightlyHour                      int
)

// Overridable in tests.
var (
	vulnsPollInterval = 5 * time.Second
	vulnsWaitTimeout  = 15 * time.Minute
)

var vulnsCmd = &cobra.Command{
	Use:     "vulns",
	Aliases: []string{"vuln"},
	Short:   "Image vulnerability scanning (CVE findings, deploy gate, ignores)",
	Long: `Every image kuso runs is scanned for known CVEs with Trivy: service images
after each build and nightly, addon and platform images nightly.

In enforce mode (the instance default) a build whose image has a fixable
CRITICAL finding that is not ignored is blocked from promotion; the previous
image keeps serving. To unblock: fix and rebuild, ignore the CVE with a reason
(` + "`kuso vulns ignore add`" + `) and rebuild, or set the service policy to report.`,
}

func requireAPI() error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	return nil
}

// vulnTarget resolves "<project> <service> [extra…]" or
// "<project> --addon a [extra…]" and returns the extra positionals.
func vulnTarget(args []string, addon string, extra int) (kusoApi.VulnTarget, []string, error) {
	if addon != "" {
		if len(args) != 1+extra {
			return kusoApi.VulnTarget{}, nil, fmt.Errorf("with --addon, pass only <project>")
		}
		return kusoApi.VulnTarget{Project: args[0], Addon: addon}, args[1:], nil
	}
	if len(args) != 2+extra {
		return kusoApi.VulnTarget{}, nil, fmt.Errorf("want <project> <service>, or <project> --addon <addon>")
	}
	return kusoApi.VulnTarget{Project: args[0], Service: args[1]}, args[2:], nil
}

func targetTitle(t kusoApi.VulnTarget, rep kusoApi.VulnReport, env string) string {
	if t.Addon != "" {
		return "addon " + t.Project + "/" + t.Addon
	}
	if rep.Image != nil && rep.Image.Env != "" {
		env = rep.Image.Env
	}
	if env == "" {
		env = "production"
	}
	return fmt.Sprintf("service %s/%s (env %s)", t.Project, t.Service, env)
}

func decodeVulnReport(body []byte) (kusoApi.VulnReport, error) {
	var rep kusoApi.VulnReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return rep, fmt.Errorf("decode vulnerability report: %w", err)
	}
	return rep, nil
}

var vulnsListCmd = &cobra.Command{
	Use:   "list <project>",
	Short: "Scan state of every service and addon image in a project",
	Long: `One row per live image. C/H/M/L counts critical/high/medium/low findings,
FIXABLE C/H those with a fixed version available. Ignored findings are not
counted.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsListOutput)
		if err != nil {
			return err
		}
		if err := requireAPI(); err != nil {
			return err
		}
		resp, err := api.GetProjectVulns(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("vulns list: %w", err)
		}
		if asJSON {
			return printRawJSON(resp.Body())
		}
		var rep kusoApi.VulnProjectReport
		if err := json.Unmarshal(resp.Body(), &rep); err != nil {
			return fmt.Errorf("decode project vulns: %w", err)
		}
		fmt.Printf("instance mode: %s\n", dash(rep.Mode))
		if len(rep.Images) == 0 {
			fmt.Println("no images")
			return nil
		}
		renderImageTable(os.Stdout, rep.Images)
		return nil
	},
}

var vulnsShowCmd = &cobra.Command{
	Use:   "show <project> <service|--addon addon>",
	Short: "Findings for a service's or addon's live image",
	Long: `Show the latest scan of a service's (or addon's) live image: gate mode,
counts and the findings table, CRITICAL first. Ignored findings are listed and
marked unless --hide-ignored.`,
	Example: `  kuso vulns show shop web
  kuso vulns show shop web --env staging --severity CRITICAL,HIGH --fixable
  kuso vulns show shop --addon db`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsShowOutput)
		if err != nil {
			return err
		}
		t, _, err := vulnTarget(args, vulnsShowAddon, 0)
		if err != nil {
			return err
		}
		if t.Addon != "" && vulnsShowEnv != "" {
			return fmt.Errorf("--env applies to services only")
		}
		sevs, err := parseSeverityFilter(vulnsShowSeverity)
		if err != nil {
			return err
		}
		if err := requireAPI(); err != nil {
			return err
		}
		q := kusoApi.VulnQuery{Env: vulnsShowEnv, Severities: sevs}
		if vulnsShowHideIgnored {
			q.IncludeIgnored = kusoApi.BoolPtr(false)
		}
		resp, err := api.GetVulns(t, q)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("vulns show: %w", err)
		}
		if asJSON {
			return printRawJSON(resp.Body())
		}
		rep, err := decodeVulnReport(resp.Body())
		if err != nil {
			return err
		}
		renderVulnReport(os.Stdout, targetTitle(t, rep, vulnsShowEnv), rep,
			findingFilter{severities: sevs, fixableOnly: vulnsShowFixable, hideIgnored: vulnsShowHideIgnored})
		return nil
	},
}

var vulnsScanCmd = &cobra.Command{
	Use:   "scan <project> <service|--addon addon>",
	Short: "Force a rescan of a service's or addon's live image",
	Long: `Queue a rescan of the live image against the latest vulnerability DB.
With --wait, poll until the scan finishes (up to 15m), then print the findings;
exits non-zero if the scan failed. Ctrl-C stops waiting; the scan itself keeps
running on the server.`,
	Example: `  kuso vulns scan shop web --wait
  kuso vulns scan shop --addon db`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsScanOutput)
		if err != nil {
			return err
		}
		t, _, err := vulnTarget(args, vulnsScanAddon, 0)
		if err != nil {
			return err
		}
		if t.Addon != "" && vulnsScanEnv != "" {
			return fmt.Errorf("--env applies to services only")
		}
		if err := requireAPI(); err != nil {
			return err
		}
		resp, err := api.ScanVulns(t, vulnsScanEnv)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("vulns scan: %w", err)
		}
		var queued kusoApi.VulnScanQueued
		if err := json.Unmarshal(resp.Body(), &queued); err != nil {
			return fmt.Errorf("decode scan response: %w", err)
		}
		if !vulnsScanWait {
			if asJSON {
				return printRawJSON(resp.Body())
			}
			fmt.Printf("scan #%d queued\n", queued.ScanID)
			return nil
		}
		fmt.Fprintf(os.Stderr, "scan #%d queued, waiting…\n", queued.ScanID)
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		body, rep, err := waitForVulnScan(ctx, t, vulnsScanEnv, queued.ScanID)
		if err != nil {
			return err
		}
		if asJSON {
			if err := printRawJSON(body); err != nil {
				return err
			}
		} else {
			renderVulnReport(os.Stdout, targetTitle(t, rep, vulnsScanEnv), rep, findingFilter{})
		}
		if s := rep.Image.Scan; s.Status == "failed" {
			return fmt.Errorf("scan #%d failed: %s", s.ID, dash(s.Error))
		}
		return nil
	},
}

// waitForVulnScan polls the findings endpoint (readable by project
// viewers, unlike the admin-only scans/{id}) until the image's latest
// scan is scanID or newer and terminal. Returns the final raw body too
// so -o json can print it unchanged.
func waitForVulnScan(ctx context.Context, t kusoApi.VulnTarget, env string, scanID int64) ([]byte, kusoApi.VulnReport, error) {
	deadline := time.NewTimer(vulnsWaitTimeout)
	defer deadline.Stop()
	last := ""
	for {
		resp, err := api.GetVulns(t, kusoApi.VulnQuery{Env: env})
		if err := checkRespErr(resp, err); err != nil {
			return nil, kusoApi.VulnReport{}, fmt.Errorf("poll scan: %w", err)
		}
		rep, err := decodeVulnReport(resp.Body())
		if err != nil {
			return nil, rep, err
		}
		if rep.Image != nil && rep.Image.Scan != nil && rep.Image.Scan.ID >= scanID {
			s := rep.Image.Scan
			if s.Status != last {
				fmt.Fprintf(os.Stderr, "  scan #%d: %s\n", s.ID, s.Status)
				last = s.Status
			}
			if s.Status == "succeeded" || s.Status == "failed" {
				return resp.Body(), rep, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, rep, fmt.Errorf("stopped waiting; scan #%d continues on the server", scanID)
		case <-deadline.C:
			return nil, rep, fmt.Errorf("scan #%d did not finish within %s; check later with `kuso vulns show`", scanID, vulnsWaitTimeout)
		case <-time.After(vulnsPollInterval):
		}
	}
}

var vulnPolicyModes = []string{"inherit", "enforce", "report", "off"}

var vulnsPolicyCmd = &cobra.Command{
	Use:   "policy <project> <service> [inherit|enforce|report|off]",
	Short: "Print or set a service's vulnerability gate policy",
	Long: `Without a mode, print the service's policy and the effective mode.

  inherit  use the instance mode (default)
  enforce  block promotion on a fixable CRITICAL finding that is not ignored
  report   scan and alert, never block
  off      do not scan this service at all`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsPolicyOutput)
		if err != nil {
			return err
		}
		if err := requireAPI(); err != nil {
			return err
		}
		var body []byte
		if len(args) == 3 {
			mode := strings.ToLower(args[2])
			if !containsStr(vulnPolicyModes, mode) {
				return fmt.Errorf("unknown policy %q (want one of %s)", args[2], strings.Join(vulnPolicyModes, ", "))
			}
			r, err := api.SetVulnPolicy(args[0], args[1], mode)
			if err := checkRespErr(r, err); err != nil {
				return fmt.Errorf("set policy: %w", err)
			}
			body = r.Body()
		} else {
			r, err := api.GetVulnPolicy(args[0], args[1])
			if err := checkRespErr(r, err); err != nil {
				return fmt.Errorf("get policy: %w", err)
			}
			body = r.Body()
		}
		if asJSON {
			return printRawJSON(body)
		}
		var p kusoApi.VulnPolicy
		if err := json.Unmarshal(body, &p); err != nil {
			return fmt.Errorf("decode policy: %w", err)
		}
		fmt.Printf("%s/%s policy: %s (effective: %s)\n", args[0], args[1], p.Mode, dash(p.EffectiveMode))
		return nil
	},
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var vulnsPlatformCmd = &cobra.Command{
	Use:   "platform",
	Short: "Scan state of kuso's own platform images (admin)",
	Long: `List the platform images (kuso-server, operator, registry, buildkitd,
traefik, …) and their latest scan. --digest shows one image's findings.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsPlatformOutput)
		if err != nil {
			return err
		}
		if err := requireAPI(); err != nil {
			return err
		}
		if vulnsPlatformDigest == "" {
			resp, err := api.GetPlatformVulns()
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("vulns platform: %w", err)
			}
			if asJSON {
				return printRawJSON(resp.Body())
			}
			var rep kusoApi.VulnPlatformReport
			if err := json.Unmarshal(resp.Body(), &rep); err != nil {
				return fmt.Errorf("decode platform vulns: %w", err)
			}
			if len(rep.Images) == 0 {
				fmt.Println("no platform images")
				return nil
			}
			renderImageTable(os.Stdout, rep.Images)
			return nil
		}
		resp, err := api.GetPlatformVulnFindings(vulnsPlatformDigest)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("vulns platform: %w", err)
		}
		if asJSON {
			return printRawJSON(resp.Body())
		}
		rep, err := decodeVulnReport(resp.Body())
		if err != nil {
			return err
		}
		title := "platform image"
		if rep.Image != nil {
			title += " " + imageOwner(*rep.Image)
		}
		renderVulnReport(os.Stdout, title, rep, findingFilter{})
		return nil
	},
}

var vulnsScanAllCmd = &cobra.Command{
	Use:   "scan-all",
	Short: "Run the nightly sweep now: rescan every live image (admin)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsScanAllOutput)
		if err != nil {
			return err
		}
		if err := requireAPI(); err != nil {
			return err
		}
		resp, err := api.ScanAllVulns()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("vulns scan-all: %w", err)
		}
		if asJSON {
			return printRawJSON(resp.Body())
		}
		var r kusoApi.VulnScanAllResult
		if err := json.Unmarshal(resp.Body(), &r); err != nil {
			return fmt.Errorf("decode scan-all response: %w", err)
		}
		fmt.Printf("queued %d scan(s)\n", r.Queued)
		return nil
	},
}

var vulnsSettingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Read/write instance vulnerability-scanning settings (admin)",
}

var vulnsSettingsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show instance vulnerability-scanning settings",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsSettingsGetOut)
		if err != nil {
			return err
		}
		if err := requireAPI(); err != nil {
			return err
		}
		resp, err := api.GetVulnSettings()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get settings: %w", err)
		}
		if asJSON {
			return printRawJSON(resp.Body())
		}
		var s kusoApi.VulnSettings
		if err := json.Unmarshal(resp.Body(), &s); err != nil {
			return fmt.Errorf("decode settings: %w", err)
		}
		printVulnSettings(s)
		return nil
	},
}

func printVulnSettings(s kusoApi.VulnSettings) {
	fmt.Printf("mode:                %s\n", s.Mode)
	fmt.Printf("scan addons:         %t\n", s.ScanAddons)
	fmt.Printf("scan platform:       %t\n", s.ScanPlatform)
	fmt.Printf("nightly hour (UTC):  %d\n", s.NightlyHourUTC)
	fmt.Printf("notify min severity: %s\n", s.NotifyMinSeverity)
}

var vulnsSettingsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Change instance vulnerability-scanning settings",
	Long: `Change only the flags you pass; the rest keep their current values.

  --mode enforce|report|off   off disables scanning, the gate and nightly sweeps
  --notify-min-severity critical|high|none`,
	Example: `  kuso vulns settings set --mode report
  kuso vulns settings set --scan-platform=false --nightly-hour 2`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsSettingsSetOut)
		if err != nil {
			return err
		}
		fl := cmd.Flags()
		if !fl.Changed("mode") && !fl.Changed("scan-addons") && !fl.Changed("scan-platform") &&
			!fl.Changed("nightly-hour") && !fl.Changed("notify-min-severity") {
			return errors.New("nothing to set; pass at least one of --mode, --scan-addons, --scan-platform, --nightly-hour, --notify-min-severity")
		}
		mode := strings.ToLower(vulnsSetMode)
		if fl.Changed("mode") && !containsStr([]string{"enforce", "report", "off"}, mode) {
			return fmt.Errorf("unknown --mode %q (want enforce, report or off)", vulnsSetMode)
		}
		if fl.Changed("nightly-hour") && (vulnsSetNightlyHour < 0 || vulnsSetNightlyHour > 23) {
			return fmt.Errorf("--nightly-hour must be 0-23 (got %d)", vulnsSetNightlyHour)
		}
		notifyMin := strings.ToLower(vulnsSetNotifyMin)
		if fl.Changed("notify-min-severity") && !containsStr([]string{"critical", "high", "none"}, notifyMin) {
			return fmt.Errorf("unknown --notify-min-severity %q (want critical, high or none)", vulnsSetNotifyMin)
		}
		if err := requireAPI(); err != nil {
			return err
		}
		// PUT replaces the whole object, so start from the current one.
		resp, err := api.GetVulnSettings()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("read current settings: %w", err)
		}
		var s kusoApi.VulnSettings
		if err := json.Unmarshal(resp.Body(), &s); err != nil {
			return fmt.Errorf("decode settings: %w", err)
		}
		if fl.Changed("mode") {
			s.Mode = mode
		}
		if fl.Changed("scan-addons") {
			s.ScanAddons = vulnsSetScanAddons
		}
		if fl.Changed("scan-platform") {
			s.ScanPlatform = vulnsSetScanPlatform
		}
		if fl.Changed("nightly-hour") {
			s.NightlyHourUTC = vulnsSetNightlyHour
		}
		if fl.Changed("notify-min-severity") {
			s.NotifyMinSeverity = notifyMin
		}
		put, err := api.PutVulnSettings(s)
		if err := checkRespErr(put, err); err != nil {
			return fmt.Errorf("update settings: %w", err)
		}
		if asJSON {
			return printRawJSON(put.Body())
		}
		if err := json.Unmarshal(put.Body(), &s); err != nil {
			return fmt.Errorf("decode settings: %w", err)
		}
		fmt.Println("vulnerability-scanning settings updated")
		printVulnSettings(s)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(vulnsCmd)
	vulnsCmd.AddCommand(vulnsListCmd, vulnsShowCmd, vulnsScanCmd, vulnsPolicyCmd,
		vulnsPlatformCmd, vulnsScanAllCmd, vulnsSettingsCmd)
	vulnsSettingsCmd.AddCommand(vulnsSettingsGetCmd, vulnsSettingsSetCmd)

	for cmd, v := range map[*cobra.Command]*string{
		vulnsListCmd:        &vulnsListOutput,
		vulnsShowCmd:        &vulnsShowOutput,
		vulnsScanCmd:        &vulnsScanOutput,
		vulnsPolicyCmd:      &vulnsPolicyOutput,
		vulnsPlatformCmd:    &vulnsPlatformOutput,
		vulnsScanAllCmd:     &vulnsScanAllOutput,
		vulnsSettingsGetCmd: &vulnsSettingsGetOut,
		vulnsSettingsSetCmd: &vulnsSettingsSetOut,
	} {
		cmd.Flags().StringVarP(v, "output", "o", "table", "output format [table, json]")
	}

	f := vulnsShowCmd.Flags()
	f.StringVar(&vulnsShowEnv, "env", "", "environment (default production)")
	f.StringVar(&vulnsShowAddon, "addon", "", "show an addon's image instead of a service's")
	f.StringVar(&vulnsShowSeverity, "severity", "", "only these severities, e.g. CRITICAL,HIGH")
	f.BoolVar(&vulnsShowFixable, "fixable", false, "only findings with a fixed version available")
	f.BoolVar(&vulnsShowHideIgnored, "hide-ignored", false, "omit ignored findings")

	f = vulnsScanCmd.Flags()
	f.StringVar(&vulnsScanEnv, "env", "", "environment (default production)")
	f.StringVar(&vulnsScanAddon, "addon", "", "scan an addon's image instead of a service's")
	f.BoolVar(&vulnsScanWait, "wait", false, "wait for the scan to finish, then print the findings")

	vulnsPlatformCmd.Flags().StringVar(&vulnsPlatformDigest, "digest", "", "show findings for one platform image digest (sha256:…)")

	f = vulnsSettingsSetCmd.Flags()
	f.StringVar(&vulnsSetMode, "mode", "", "instance mode: enforce, report or off")
	f.BoolVar(&vulnsSetScanAddons, "scan-addons", false, "scan addon images nightly (true/false)")
	f.BoolVar(&vulnsSetScanPlatform, "scan-platform", false, "scan platform images nightly (true/false)")
	f.IntVar(&vulnsSetNightlyHour, "nightly-hour", 0, "UTC hour (0-23) the nightly sweep starts")
	f.StringVar(&vulnsSetNotifyMin, "notify-min-severity", "", "lowest severity that notifies: critical, high or none")
}
