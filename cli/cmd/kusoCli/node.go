package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso node` — manage cluster nodes via the bootstrap-token flow.
// Pull-mode join: the new VM curls a one-liner from kuso, no SSH needed
// from the operator's side. The legacy SSH-driven flow is still
// available in the web UI as a fallback.
//
//   kuso node add-token --region eu --label tier=premium [-o json]
//   kuso node pending  [-o json]
//   kuso node revoke   <jti>
//
// `add-token` prints the curl one-liner. Paste it on the new VM as
// root (or with sudo) and watch the install scroll. The new node
// shows up in the kuso UI within ~30 seconds.

var nodeCmd = &cobra.Command{
	Use:     "node",
	Aliases: []string{"nodes"},
	Short:   "Manage cluster nodes (add-token / pending / revoke)",
}

var (
	nodeTokenLabels   []string
	nodeTokenRegion   string
	nodeTokenName     string
	nodeTokenTTL      string
)

var nodeAddTokenCmd = &cobra.Command{
	Use:   "add-token",
	Short: "Mint a single-use bootstrap token; print the curl one-liner.",
	Long: `Mint a single-use, time-limited token for adding a worker node.

The new VM runs:
    curl -fsSL <one-liner-url> | sudo sh

That command detects facts (arch / cloud / instance type), redeems the
token, and runs the standard k3s agent install. The token is consumed
on first use; replays return 410.`,
	Example: `  kuso node add-token --region eu
  kuso node add-token --label tier=premium --label gpu=true --ttl 30m
  kuso node add-token --name worker-2 --region eu -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		labels := map[string]string{}
		if nodeTokenRegion != "" {
			labels["region"] = nodeTokenRegion
		}
		for _, raw := range nodeTokenLabels {
			k, v, ok := splitKV(raw)
			if !ok {
				return fmt.Errorf("--label %q must be key=value", raw)
			}
			labels[k] = v
		}
		ttl := 0
		if nodeTokenTTL != "" {
			d, err := time.ParseDuration(nodeTokenTTL)
			if err != nil {
				return fmt.Errorf("--ttl: %w", err)
			}
			ttl = int(d.Seconds())
		}
		resp, err := api.MintNodeBootstrapToken(kusoApi.MintNodeBootstrapTokenRequest{
			Labels:     labels,
			NodeName:   nodeTokenName,
			TTLSeconds: ttl,
		})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var out struct {
			JTI       string            `json:"jti"`
			JTIPrefix string            `json:"jtiPrefix"`
			ExpiresAt time.Time         `json:"expiresAt"`
			OneLiner  string            `json:"oneLiner"`
			Labels    map[string]string `json:"labels"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode mint response: %w", err)
		}
		fmt.Printf("Token minted. Expires %s (%s from now).\n",
			out.ExpiresAt.Local().Format(time.RFC3339),
			time.Until(out.ExpiresAt).Round(time.Second))
		if len(out.Labels) > 0 {
			fmt.Printf("Labels: ")
			first := true
			for k, v := range out.Labels {
				if !first {
					fmt.Print(", ")
				}
				fmt.Printf("%s=%s", k, v)
				first = false
			}
			fmt.Println()
		}
		fmt.Println()
		fmt.Println("On the new VM, run as root:")
		fmt.Println()
		fmt.Println("  " + out.OneLiner)
		fmt.Println()
		fmt.Println("The node should appear in `kuso get nodes` within ~30 seconds.")
		// Use the hash prefix as the revoke handle — the cleartext is
		// only safe to surface once, here at mint time.
		fmt.Printf("To cancel before it's used:  kuso node revoke %s\n", out.JTIPrefix)
		fmt.Println()
		fmt.Println("Save the one-liner now — it's the only chance to capture it.")
		fmt.Println("`kuso node pending` will only show the prefix from now on.")
		return nil
	},
}

var nodePendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List bootstrap tokens that haven't been consumed yet.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListPendingNodeBootstrapTokens()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var body struct {
			Tokens []struct {
				JTIPrefix string            `json:"jtiPrefix"`
				JTIHash   string            `json:"jtiHash"`
				CreatedAt time.Time         `json:"createdAt"`
				ExpiresAt time.Time         `json:"expiresAt"`
				Labels    map[string]string `json:"labels"`
				NodeName  string            `json:"nodeName"`
				CreatedBy string            `json:"createdBy"`
			} `json:"tokens"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(body.Tokens) == 0 {
			fmt.Println("No pending bootstrap tokens.")
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		// Prefix is the safe-to-display 8-char head of the hash. The
		// Hash column is the revoke handle (full sha256). We never
		// re-display the cleartext token here — that's a one-shot
		// reveal at mint time only.
		tw.SetHeader([]string{"Prefix", "Hash", "Name", "Labels", "Expires in", "Created by"})
		for _, t := range body.Tokens {
			tw.Append([]string{
				t.JTIPrefix,
				t.JTIHash,
				t.NodeName,
				formatLabels(t.Labels),
				time.Until(t.ExpiresAt).Round(time.Second).String(),
				t.CreatedBy,
			})
		}
		tw.Render()
		fmt.Println()
		fmt.Println("Revoke a pending token: kuso node revoke <Hash>")
		return nil
	},
}

var nodeRevokeCmd = &cobra.Command{
	Use:   "revoke <jti>",
	Args:  cobra.ExactArgs(1),
	Short: "Revoke a pending bootstrap token by jti.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RevokeNodeBootstrapToken(args[0])
		if err != nil {
			return err
		}
		switch resp.StatusCode() {
		case 204:
			fmt.Printf("Revoked %s.\n", args[0])
			return nil
		case 404:
			return fmt.Errorf("token %s not found", args[0])
		default:
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
	},
}

func init() {
	nodeAddTokenCmd.Flags().StringSliceVar(&nodeTokenLabels, "label", nil,
		"Repeatable key=value label, baked onto the joined node (e.g. --label tier=premium)")
	nodeAddTokenCmd.Flags().StringVar(&nodeTokenRegion, "region", "",
		"Shorthand for --label region=<value>. (Tainting is operator-applied via kubectl after join.)")
	nodeAddTokenCmd.Flags().StringVar(&nodeTokenName, "name", "",
		"Override the joined node's name (default: VM hostname).")
	nodeAddTokenCmd.Flags().StringVar(&nodeTokenTTL, "ttl", "",
		"Token lifetime (e.g. 15m, 1h). Default 15m. Capped at 1h.")
	nodeAddTokenCmd.Flags().StringVarP(&outputFormat, "output", "o", "",
		"Output format: json | (default human)")

	nodePendingCmd.Flags().StringVarP(&outputFormat, "output", "o", "",
		"Output format: json | (default human)")

	nodeCmd.AddCommand(nodeAddTokenCmd)
	nodeCmd.AddCommand(nodePendingCmd)
	nodeCmd.AddCommand(nodeRevokeCmd)
	rootCmd.AddCommand(nodeCmd)
}

// splitKV parses k=v. Returns (k, v, true) on success; (_, _, false)
// on a malformed input. Whitespace around the boundary is trimmed so
// `--label "foo = bar"` works.
func splitKV(s string) (string, string, bool) {
	i := strings.Index(s, "=")
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
}

func formatLabels(m map[string]string) string {
	if len(m) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}
