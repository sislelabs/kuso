package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso shared-secret` — project-level env vars auto-mounted into
// every service. Use case: cross-service integrations like Resend,
// Postmark, Stripe — set once, every service in the project picks
// it up via envFromSecrets.
//
//   kuso shared-secret list <project>
//   kuso shared-secret set <project> <KEY>=<VALUE>
//   kuso shared-secret unset <project> <KEY>

var sharedSecretForceFlag bool

// shadowedResp captures the server's 409 body for a shadow conflict:
//
//	{"error": "...", "code": "shadowed", "key": "FOO", "scope": "service",
//	 "services": ["api","bot"]}
//
// scope tells us which write was attempted ("shared" = user is writing a
// shared key that one or more services already shadow; "service" = user
// is writing a service key that shared already holds). services is only
// populated for scope=shared.
type shadowedResp struct {
	Code     string   `json:"code"`
	Key      string   `json:"key"`
	Scope    string   `json:"scope"`
	Services []string `json:"services"`
	Error    string   `json:"error"`
}

// parseShadowed returns a non-nil shadowedResp iff the response body
// contains code:"shadowed". Used by both `secret set` and `shared-secret
// set` to render a helpful "unset the override or pass --force" message
// instead of just dumping the raw 409 body at the user.
func parseShadowed(body []byte) *shadowedResp {
	var s shadowedResp
	if err := json.Unmarshal(body, &s); err != nil {
		return nil
	}
	if s.Code != "shadowed" {
		return nil
	}
	return &s
}

var sharedSecretCmd = &cobra.Command{
	Use:     "shared-secret",
	Aliases: []string{"shared-secrets", "ssec"},
	Short:   "Manage project-level shared secrets (env vars attached to every service)",
}

var sharedSecretListCmd = &cobra.Command{
	Use:     "list <project>",
	Aliases: []string{"ls"},
	Short:   "List shared secret keys (values are write-only and never returned)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListSharedSecrets(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var body struct {
			Keys []string `json:"keys"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(body.Keys)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"KEY"})
			for _, k := range body.Keys {
				t.Append([]string{k})
			}
			t.Render()
			return nil
		}
	},
}

var sharedSecretSetCmd = &cobra.Command{
	Use:   "set <project> <KEY=VALUE>",
	Short: "Upsert a shared secret",
	Args:  cobra.ExactArgs(2),
	Example: `  kuso shared-secret set myproj RESEND_API_KEY=re_abc123
  kuso shared-secret set myproj STRIPE_SECRET_KEY=sk_live_xxx`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Split on the FIRST = so values containing = work (e.g.
		// base64-encoded keys like "AKIA…====").
		kv := args[1]
		eq := -1
		for i, c := range kv {
			if c == '=' {
				eq = i
				break
			}
		}
		if eq <= 0 {
			return fmt.Errorf("argument must be KEY=VALUE")
		}
		req := kusoApi.SetSharedSecretRequest{Key: kv[:eq], Value: kv[eq+1:], Force: sharedSecretForceFlag}
		resp, err := api.SetSharedSecret(args[0], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() == 409 {
			if s := parseShadowed(resp.Body()); s != nil {
				// Shared writes are shadowed by service-scoped Secrets with
				// the same key — kube's envFrom evaluates sources in order
				// and the chart mounts service-scoped after shared, so the
				// service value wins. Without this warning the user sets
				// shared, rolls pods, and is baffled when the old value
				// is still in effect.
				return fmt.Errorf(
					"%s is already set as a service-scoped secret on: %s\n"+
						"\nservice-scoped values override shared at pod start, so the shared write\n"+
						"would have no effect. fix one of:\n"+
						"  • unset the override:  kuso secret unset %s <service> %s\n"+
						"  • or force the write:  kuso shared-secret set %s %s=… --force\n",
					s.Key, strings.Join(s.Services, ", "),
					args[0], s.Key,
					args[0], s.Key,
				)
			}
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		// Surface the rollout count so the user knows the change
		// actually reached the running pods. Previously this just
		// printed "set X on Y" — leaving the user to discover the
		// hard way that kube's envFrom is evaluated at pod-start,
		// not on Secret update, so existing pods were still holding
		// the old value.
		var body struct {
			Rolled int `json:"rolled"`
		}
		_ = json.Unmarshal(resp.Body(), &body)
		fmt.Printf("set %s on %s — %s\n", req.Key, args[0], rolloutMsg(body.Rolled))
		return nil
	},
}

var sharedSecretUnsetCmd = &cobra.Command{
	Use:     "unset <project> <KEY>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a shared secret",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.UnsetSharedSecret(args[0], args[1])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var body struct {
			Rolled int `json:"rolled"`
		}
		_ = json.Unmarshal(resp.Body(), &body)
		fmt.Printf("unset %s on %s — %s\n", args[1], args[0], rolloutMsg(body.Rolled))
		return nil
	},
}

// rolloutMsg formats the number of envs the server rolled into a
// short human phrase for CLI output. Plural rules in English are
// just plural enough that switching on the count reads cleanest.
//
// Shared between `secret set` and `shared-secret set` since both
// surface the rollout count the same way.
func rolloutMsg(rolled int) string {
	switch rolled {
	case 0:
		return "no running envs to roll"
	case 1:
		return "rolled 1 env"
	default:
		return fmt.Sprintf("rolled %d envs", rolled)
	}
}

func init() {
	rootCmd.AddCommand(sharedSecretCmd)
	sharedSecretCmd.AddCommand(sharedSecretListCmd)
	sharedSecretListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	sharedSecretCmd.AddCommand(sharedSecretSetCmd)
	sharedSecretSetCmd.Flags().BoolVar(&sharedSecretForceFlag, "force", false, "override the shadow check (set even if a service-scoped secret with the same key exists)")
	sharedSecretCmd.AddCommand(sharedSecretUnsetCmd)
}
