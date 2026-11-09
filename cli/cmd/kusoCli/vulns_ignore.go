package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	vulnsIgnoreAddOutput  string
	vulnsIgnoreListOutput string

	vulnsIgnoreAddAddon, vulnsIgnoreAddReason, vulnsIgnoreAddExpires string
	vulnsIgnoreListAddon, vulnsIgnoreRmAddon                         string
	vulnsIgnoreRmYes                                                 bool
)

var vulnsIgnoreCmd = &cobra.Command{
	Use:   "ignore",
	Short: "Accept specific CVEs on a service or addon (excluded from the gate and alerts)",
}

func targetLabel(t kusoApi.VulnTarget) string {
	if t.Addon != "" {
		return "addon " + t.Project + "/" + t.Addon
	}
	return t.Project + "/" + t.Service
}

var vulnsIgnoreAddCmd = &cobra.Command{
	Use:   "add <project> <service|--addon addon> <VULN-ID>...",
	Short: "Ignore vulnerability IDs with a reason and optional expiry",
	Long: `Ignore one or more vulnerability IDs (CVE-…, GHSA-…) on a service or addon.
Ignored findings stay visible (marked) but no longer count toward the deploy
gate or alerts. A build that was blocked needs a rebuild (kuso build trigger)
after. An ignore covers the ID on every package it appears in.

--reason is required and applies to every ID given. --expires takes days, e.g.
30d or 30 (1-365); without it the ignore never expires. Prefer an expiry, so the
finding comes back for review. Every ID is attempted; the command fails if any
of them could not be ignored.`,
	Example: `  kuso vulns ignore add shop web CVE-2024-12345 --reason "not reachable: CLI-only code path" --expires 30d
  kuso vulns ignore add shop web CVE-2025-1 CVE-2025-2 --reason "pnpm's bundled deps, build-time only" --expires 30d
  kuso vulns ignore add shop --addon db CVE-2024-12345 --reason "upstream image, tracked in #42"`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsIgnoreAddOutput)
		if err != nil {
			return err
		}
		extra := len(args) - 2
		if vulnsIgnoreAddAddon != "" {
			extra = len(args) - 1
		}
		if extra < 1 {
			extra = 1
		}
		t, rest, err := vulnTargetIgnore(args, vulnsIgnoreAddAddon, "<VULN-ID>...", extra)
		if err != nil {
			return err
		}
		reason := strings.TrimSpace(vulnsIgnoreAddReason)
		if reason == "" {
			return fmt.Errorf("--reason is required")
		}
		days, err := parseExpiresDays(vulnsIgnoreAddExpires)
		if err != nil {
			return err
		}
		if err := requireAPI(); err != nil {
			return err
		}
		added := []json.RawMessage{}
		failed := 0
		for _, id := range rest {
			id = strings.TrimSpace(id)
			resp, err := api.AddVulnIgnore(t, kusoApi.VulnIgnoreRequest{VulnID: id, Reason: reason, ExpiresInDays: days})
			if err := checkRespErr(resp, err); err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "ignore %s: %v\n", id, err)
				continue
			}
			if asJSON {
				added = append(added, json.RawMessage(resp.Body()))
				continue
			}
			var ig kusoApi.VulnIgnore
			if err := json.Unmarshal(resp.Body(), &ig); err != nil {
				return fmt.Errorf("decode ignore: %w", err)
			}
			exp := "never expires"
			if ig.ExpiresAt != "" {
				exp = "expires " + ig.ExpiresAt
			}
			fmt.Printf("ignored %s on %s (#%d, %s)\n", ig.VulnID, targetLabel(t), ig.ID, exp)
		}
		if asJSON {
			body, err := json.Marshal(added)
			if err != nil {
				return err
			}
			if err := printRawJSON(body); err != nil {
				return err
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d ignores failed", failed, len(rest))
		}
		return nil
	},
}

// vulnTargetIgnore is vulnTarget for the ignore subcommands, whose
// trailing positional is named in the error.
func vulnTargetIgnore(args []string, addon, last string, extra int) (kusoApi.VulnTarget, []string, error) {
	t, rest, err := vulnTarget(args, addon, extra)
	if err != nil {
		if addon != "" {
			return t, nil, fmt.Errorf("with --addon, pass <project> %s", last)
		}
		return t, nil, fmt.Errorf("want <project> <service> %s, or <project> --addon <addon> %s", last, last)
	}
	return t, rest, nil
}

var vulnsIgnoreListCmd = &cobra.Command{
	Use:   "list <project> <service|--addon addon>",
	Short: "List ignores on a service or addon (expired ones included)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := vulnsOutput(vulnsIgnoreListOutput)
		if err != nil {
			return err
		}
		t, _, err := vulnTarget(args, vulnsIgnoreListAddon, 0)
		if err != nil {
			return err
		}
		if err := requireAPI(); err != nil {
			return err
		}
		resp, err := api.ListVulnIgnores(t)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list ignores: %w", err)
		}
		if asJSON {
			return printRawJSON(resp.Body())
		}
		var list []kusoApi.VulnIgnore
		if err := json.Unmarshal(resp.Body(), &list); err != nil {
			return fmt.Errorf("decode ignores: %w", err)
		}
		if len(list) == 0 {
			fmt.Printf("no ignores on %s\n", targetLabel(t))
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"ID", "VULN", "REASON", "EXPIRES", "CREATED BY", "CREATED"})
		for _, ig := range list {
			exp := dash(ig.ExpiresAt)
			if ig.Expired {
				exp += " (expired)"
			}
			tw.Append([]string{strconv.FormatInt(ig.ID, 10), ig.VulnID, ig.Reason, exp, dash(ig.CreatedBy), relativeAge(ig.CreatedAt)})
		}
		tw.Render()
		return nil
	},
}

var vulnsIgnoreRmCmd = &cobra.Command{
	Use:   "rm <project> <service|--addon addon> <id>",
	Short: "Remove an ignore by its ID",
	Long: `Remove an ignore (IDs from ` + "`kuso vulns ignore list`" + `). The finding counts toward
the deploy gate and alerts again, so in enforce mode the service's next build
can be blocked. Asks for confirmation; pass --yes in scripts.`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, rest, err := vulnTargetIgnore(args, vulnsIgnoreRmAddon, "<id>", 1)
		if err != nil {
			return err
		}
		id := rest[0]
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return fmt.Errorf("invalid ignore id %q (want a number from `kuso vulns ignore list`)", id)
		}
		if err := requireAPI(); err != nil {
			return err
		}
		if err := confirmDestructive(vulnsIgnoreRmYes, fmt.Sprintf("Remove ignore #%s on %s? The finding counts toward the deploy gate again.", id, targetLabel(t))); err != nil {
			return err
		}
		resp, err := api.DeleteVulnIgnore(t, id)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("remove ignore: %w", err)
		}
		fmt.Printf("removed ignore #%s on %s\n", id, targetLabel(t))
		return nil
	},
}

func init() {
	vulnsCmd.AddCommand(vulnsIgnoreCmd)
	vulnsIgnoreCmd.AddCommand(vulnsIgnoreAddCmd, vulnsIgnoreListCmd, vulnsIgnoreRmCmd)

	f := vulnsIgnoreAddCmd.Flags()
	f.StringVarP(&vulnsIgnoreAddOutput, "output", "o", "table", "output format [table, json]")
	f.StringVar(&vulnsIgnoreAddAddon, "addon", "", "ignore on an addon instead of a service")
	f.StringVar(&vulnsIgnoreAddReason, "reason", "", "why this is acceptable (required)")
	f.StringVar(&vulnsIgnoreAddExpires, "expires", "", "expire after N days, e.g. 30d (1-365; default never)")

	f = vulnsIgnoreListCmd.Flags()
	f.StringVarP(&vulnsIgnoreListOutput, "output", "o", "table", "output format [table, json]")
	f.StringVar(&vulnsIgnoreListAddon, "addon", "", "list an addon's ignores instead of a service's")

	f = vulnsIgnoreRmCmd.Flags()
	f.StringVar(&vulnsIgnoreRmAddon, "addon", "", "remove from an addon instead of a service")
	f.BoolVarP(&vulnsIgnoreRmYes, "yes", "y", false, "skip the confirmation prompt")
}
