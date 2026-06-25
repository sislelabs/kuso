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

// --- node management parity with /settings/nodes -------------------
//
// Everything below rounds the `kuso node` subtree out to match the web
// node-management view: list nodes, edit kuso labels, the SSH-driven
// validate/join/remove lifecycle, host-package updates, per-node
// history, and the cluster cleanup sweep. All are admin-gated server-
// side; the CLI just surfaces the 403 cleanly.

var nodeListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List cluster nodes with status, roles, and live usage.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListNodes()
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
		var nodes []kusoApi.NodeSummary
		if err := json.Unmarshal(resp.Body(), &nodes); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(nodes) == 0 {
			fmt.Println("No nodes.")
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"Name", "Status", "Roles", "Region", "CPU", "Mem", "Pods", "Labels"})
		for _, n := range nodes {
			tw.Append([]string{
				n.Name,
				nodeStatus(n),
				strings.Join(n.Roles, ","),
				dashIfEmpty(n.Region),
				fmt.Sprintf("%s / %s", formatMilliCPU(n.CPUUsageMilli), formatMilliCPU(n.CPUCapacityMilli)),
				fmt.Sprintf("%s / %s", humanBytes(n.MemUsageBytes), humanBytes(n.MemCapacityBytes)),
				fmt.Sprintf("%d/%d", n.Pods, n.PodsCapacity),
				formatLabels(n.KusoLabels),
			})
		}
		tw.Render()
		return nil
	},
}

// nodeLabelCmd groups the label set/rm subcommands.
var nodeLabelCmd = &cobra.Command{
	Use:   "label",
	Short: "Set or remove kuso-managed labels on a node.",
	Long: `Edit the kuso.sislelabs.com/-namespaced labels on a node. Bare keys
only — the namespace prefix is applied server-side. Setting 'region'
also drops a matching NoSchedule taint so untolerated workloads steer
clear; removing it drops the taint.`,
}

var nodeLabelSetCmd = &cobra.Command{
	Use:   "set <name> <key=value> [key=value...]",
	Short: "Add or update kuso labels on a node (merges with existing).",
	Example: `  kuso node label set worker-2 tier=premium gpu=true
  kuso node label set worker-2 region=eu`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		name := args[0]
		// The label PUT is whole-set replacement: fetch the node's
		// current kuso labels first and merge the new pairs in so a
		// `set` of one key doesn't silently wipe the others.
		current, err := currentNodeLabels(name)
		if err != nil {
			return err
		}
		for _, raw := range args[1:] {
			k, v, ok := splitKV(raw)
			if !ok {
				return fmt.Errorf("%q must be key=value", raw)
			}
			current[k] = v
		}
		resp, err := api.SetNodeLabels(name, kusoApi.SetNodeLabelsRequest{Labels: current})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("Labels updated on %s: %s\n", name, formatLabels(current))
		return nil
	},
}

var nodeLabelRmCmd = &cobra.Command{
	Use:     "rm <name> <key> [key...]",
	Aliases: []string{"remove", "unset"},
	Short:   "Remove kuso labels from a node.",
	Example: `  kuso node label rm worker-2 gpu
  kuso node label rm worker-2 region`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		name := args[0]
		current, err := currentNodeLabels(name)
		if err != nil {
			return err
		}
		for _, k := range args[1:] {
			if _, ok := current[k]; !ok {
				return fmt.Errorf("node %s has no kuso label %q", name, k)
			}
			delete(current, k)
		}
		resp, err := api.SetNodeLabels(name, kusoApi.SetNodeLabelsRequest{Labels: current})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("Labels updated on %s: %s\n", name, formatLabels(current))
		return nil
	},
}

var (
	nodeSSHHost    string
	nodeSSHPort    int
	nodeSSHUser    string
	nodeSSHPass    string
	nodeSSHKeyFile string
	nodeSSHKeyID   string
	nodeJoinLabels []string
	nodeJoinName   string
	nodeRemoveForce bool
	nodeApplyReboot bool
)

// nodeCredsFromFlags assembles the shared SSH credential block from the
// connection flags. The private key is read from --ssh-key-file when
// given; --ssh-key-id defers to the server's SSH key library instead.
func nodeCredsFromFlags() (kusoApi.NodeCredentials, error) {
	creds := kusoApi.NodeCredentials{
		Host:     nodeSSHHost,
		Port:     nodeSSHPort,
		User:     nodeSSHUser,
		Password: nodeSSHPass,
	}
	if nodeSSHKeyFile != "" {
		b, err := os.ReadFile(nodeSSHKeyFile)
		if err != nil {
			return creds, fmt.Errorf("--ssh-key-file: %w", err)
		}
		creds.PrivateKey = string(b)
	}
	return creds, nil
}

var nodeValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Pre-flight check a remote VM over SSH before joining (Coolify-style).",
	Long: `Open an SSH session to a remote VM and run a series of probes — SSH
handshake, root/sudo, control-plane reachability, curl, existing-k3s —
WITHOUT installing anything. Fix any failing check, then run 'kuso node
join' with the same flags.`,
	Example: `  kuso node validate --host 10.0.0.5 --user root --ssh-key-file ~/.ssh/id_ed25519
  kuso node validate --host 10.0.0.5 --user ubuntu --ssh-key-id <id>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		creds, err := nodeCredsFromFlags()
		if err != nil {
			return err
		}
		resp, err := api.ValidateNode(kusoApi.ValidateNodeRequest{
			NodeCredentials: creds,
			SSHKeyID:        nodeSSHKeyID,
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
		var res kusoApi.ValidateNodeResult
		if err := json.Unmarshal(resp.Body(), &res); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		for _, c := range res.Checks {
			mark := "FAIL"
			if c.OK {
				mark = "ok"
			}
			fmt.Printf("  [%-4s] %-14s %s\n", mark, c.Label, c.Detail)
		}
		fmt.Println()
		if res.OK {
			fmt.Println("All checks passed — safe to `kuso node join`.")
			return nil
		}
		// Non-zero exit so scripts can gate the join on validate.
		return fmt.Errorf("one or more pre-flight checks failed")
	},
}

var nodeJoinCmd = &cobra.Command{
	Use:   "join",
	Short: "SSH into a remote VM and join it to the cluster as a k3s agent.",
	Long: `Run the k3s agent install on a remote VM over SSH and join it to this
cluster. Blocks for the duration of the install (typically 30-90s) and
prints the install log. Run 'kuso node validate' with the same flags
first to catch prereq problems early.`,
	Example: `  kuso node join --host 10.0.0.5 --user root --ssh-key-file ~/.ssh/id_ed25519
  kuso node join --host 10.0.0.5 --user root --ssh-key-file ~/.ssh/id_ed25519 \
    --label region=eu --label tier=premium --name worker-2`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		creds, err := nodeCredsFromFlags()
		if err != nil {
			return err
		}
		labels := map[string]string{}
		for _, raw := range nodeJoinLabels {
			k, v, ok := splitKV(raw)
			if !ok {
				return fmt.Errorf("--label %q must be key=value", raw)
			}
			labels[k] = v
		}
		resp, err := api.JoinNode(kusoApi.JoinNodeRequest{
			NodeCredentials: creds,
			SSHKeyID:        nodeSSHKeyID,
			Labels:          labels,
			Name:            nodeJoinName,
		})
		if err != nil {
			return err
		}
		// 502 carries {"error","output"} — surface the remote install
		// log so the operator can debug a failed join.
		if resp.StatusCode() >= 300 {
			var fail struct {
				Error  string `json:"error"`
				Output string `json:"output"`
			}
			if json.Unmarshal(resp.Body(), &fail) == nil && fail.Error != "" {
				if fail.Output != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), fail.Output)
				}
				return fmt.Errorf("join failed: %s", fail.Error)
			}
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var out struct {
			Output   string `json:"output"`
			NodeName string `json:"nodeName"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if out.Output != "" {
			fmt.Println(out.Output)
		}
		fmt.Printf("\nNode %s joined. It should report Ready within ~30 seconds (`kuso node list`).\n", dashIfEmpty(out.NodeName))
		return nil
	},
}

var nodeRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Cordon, drain, and remove a node from the cluster.",
	Long: `Cordon → drain → delete the node from the control plane. When SSH
connection flags are supplied, kuso also runs k3s-agent-uninstall on the
host so the VM is left clean; without them the node is only untracked
(use this when the VM is already gone). Refuses to remove the last
control-plane node.`,
	Example: `  kuso node remove worker-2
  kuso node remove worker-2 --host 10.0.0.5 --user root --ssh-key-file ~/.ssh/id_ed25519
  kuso node remove worker-2 --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		name := args[0]
		req := kusoApi.RemoveNodeRequest{Force: nodeRemoveForce}
		// Only attach credentials (triggering host uninstall) when a
		// host was actually provided.
		if nodeSSHHost != "" {
			creds, err := nodeCredsFromFlags()
			if err != nil {
				return err
			}
			req.Credentials = &creds
		}
		resp, err := api.RemoveNode(name, req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var out struct {
			Removed      string `json:"removed"`
			UninstallOut string `json:"uninstallOut"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if out.UninstallOut != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), out.UninstallOut)
		}
		fmt.Printf("Node %s removed.\n", out.Removed)
		return nil
	},
}

var nodeUpdatesCmd = &cobra.Command{
	Use:   "updates",
	Short: "Show pending host package updates per node.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.NodeUpdates()
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
			Data []struct {
				Node           string `json:"node"`
				Count          int    `json:"count"`
				RebootRequired bool   `json:"rebootRequired"`
				PkgMgr         string `json:"pkgMgr"`
				CheckedAt      string `json:"checkedAt"`
				Present        bool   `json:"present"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(body.Data) == 0 {
			fmt.Println("No node update advisories.")
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"Node", "Updates", "Reboot", "PkgMgr", "Checked"})
		for _, a := range body.Data {
			count := fmt.Sprintf("%d", a.Count)
			if !a.Present {
				count = "checking…"
			}
			reboot := "no"
			if a.RebootRequired {
				reboot = "YES"
			}
			tw.Append([]string{a.Node, count, reboot, dashIfEmpty(a.PkgMgr), dashIfEmpty(a.CheckedAt)})
		}
		tw.Render()
		return nil
	},
}

var nodeApplyUpdatesCmd = &cobra.Command{
	Use:   "apply-updates <name>",
	Short: "Apply pending host package updates on a node (privileged Job).",
	Long: `Launch a privileged Job that patches the node's host OS packages. By
default it's patch-only; --allow-reboot lets kuso run the cordon/drain/
reboot orchestration when a kernel or other update requires a restart.`,
	Example: `  kuso node apply-updates worker-2
  kuso node apply-updates worker-2 --allow-reboot`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		name := args[0]
		resp, err := api.ApplyNodeUpdates(name, kusoApi.ApplyNodeUpdatesRequest{AllowReboot: nodeApplyReboot})
		if err != nil {
			return err
		}
		if resp.StatusCode() == 409 {
			// Nothing-to-do or already-running both map to 409; pass the
			// server's message through so the user sees which.
			return fmt.Errorf("%s", strings.TrimSpace(string(resp.Body())))
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("Update Job started on %s", name)
		if nodeApplyReboot {
			fmt.Print(" (reboot allowed)")
		}
		fmt.Println(". Track progress with `kuso node updates`.")
		return nil
	},
}

var nodeHistoryCmd = &cobra.Command{
	Use:   "history <name>",
	Short: "Show up-to-7-days of CPU/RAM/disk samples for a node.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.NodeHistory(args[0])
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
			Node    string `json:"node"`
			Samples []struct {
				Ts                time.Time `json:"ts"`
				CPUUsedMilli      int64     `json:"cpuUsedMilli"`
				CPUCapacityMilli  int64     `json:"cpuCapacityMilli"`
				MemUsedBytes      int64     `json:"memUsedBytes"`
				MemCapacityBytes  int64     `json:"memCapacityBytes"`
				DiskAvailBytes    int64     `json:"diskAvailBytes"`
				DiskCapacityBytes int64     `json:"diskCapacityBytes"`
			} `json:"samples"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(body.Samples) == 0 {
			fmt.Printf("No samples yet for %s (the sampler runs every ~30 min).\n", body.Node)
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"Time", "CPU", "Mem", "Disk used"})
		for _, s := range body.Samples {
			tw.Append([]string{
				s.Ts.Local().Format(time.RFC3339),
				fmt.Sprintf("%s / %s", formatMilliCPU(s.CPUUsedMilli), formatMilliCPU(s.CPUCapacityMilli)),
				fmt.Sprintf("%s / %s", humanBytes(s.MemUsedBytes), humanBytes(s.MemCapacityBytes)),
				fmt.Sprintf("%s / %s", humanBytes(s.DiskCapacityBytes-s.DiskAvailBytes), humanBytes(s.DiskCapacityBytes)),
			})
		}
		tw.Render()
		return nil
	},
}

var nodeCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Delete completed pods + finished Jobs cluster-wide (admin only).",
	Long: `Sweep Succeeded/Failed pods and finished Jobs across all namespaces.
Running pods, active Jobs, KusoBuild-owned Jobs/pods, and the
kube-system family are left untouched. Use this when the host is
drowning in stale completion artifacts.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.CleanupCompleted()
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
			PodsDeleted int      `json:"podsDeleted"`
			JobsDeleted int      `json:"jobsDeleted"`
			Namespaces  []string `json:"namespaces"`
			Errors      []string `json:"errors"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		fmt.Printf("Deleted %d pod(s) and %d job(s)", out.PodsDeleted, out.JobsDeleted)
		if len(out.Namespaces) > 0 {
			fmt.Printf(" across: %s", strings.Join(out.Namespaces, ", "))
		}
		fmt.Println(".")
		for _, e := range out.Errors {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", e)
		}
		return nil
	},
}

// currentNodeLabels fetches a node's current kuso labels so the label
// set/rm commands can merge rather than clobber (the PUT is a full
// replace).
func currentNodeLabels(name string) (map[string]string, error) {
	resp, err := api.ListNodes()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
	}
	var nodes []kusoApi.NodeSummary
	if err := json.Unmarshal(resp.Body(), &nodes); err != nil {
		return nil, fmt.Errorf("decode nodes: %w", err)
	}
	for _, n := range nodes {
		if n.Name == name {
			out := map[string]string{}
			for k, v := range n.KusoLabels {
				out[k] = v
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("node %q not found", name)
}

// nodeStatus renders the human-facing status cell: Ready/NotReady plus
// a cordoned/unreachable qualifier.
func nodeStatus(n kusoApi.NodeSummary) string {
	s := "NotReady"
	if n.Ready {
		s = "Ready"
	}
	switch {
	case n.Unreachable:
		s += ",Unreachable"
	case !n.Schedulable:
		s += ",Cordoned"
	}
	return s
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// formatMilliCPU renders milli-CPU as cores with one decimal (e.g.
// 1500 → "1.5"). Keeps the nodes table compact vs. raw millicores.
func formatMilliCPU(m int64) string {
	if m == 0 {
		return "0"
	}
	return fmt.Sprintf("%.1f", float64(m)/1000)
}

// humanBytes renders a byte count with a binary (Ki/Mi/Gi) suffix —
// matches how kube quantities read and keeps the usage columns short.
func humanBytes(b int64) string {
	if b == 0 {
		return "0"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ci", float64(b)/float64(div), "KMGTPE"[exp])
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

	// --- node management parity commands ---

	nodeListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	nodeCmd.AddCommand(nodeListCmd)

	nodeCmd.AddCommand(nodeLabelCmd)
	nodeLabelCmd.AddCommand(nodeLabelSetCmd)
	nodeLabelCmd.AddCommand(nodeLabelRmCmd)

	// Shared SSH connection flags for validate/join/remove.
	for _, c := range []*cobra.Command{nodeValidateCmd, nodeJoinCmd, nodeRemoveCmd} {
		c.Flags().StringVar(&nodeSSHHost, "host", "", "SSH host (IP or hostname) of the target VM")
		c.Flags().IntVar(&nodeSSHPort, "port", 0, "SSH port (default 22 server-side)")
		c.Flags().StringVar(&nodeSSHUser, "user", "", "SSH user")
		c.Flags().StringVar(&nodeSSHPass, "password", "", "SSH password (prefer --ssh-key-file / --ssh-key-id)")
		c.Flags().StringVar(&nodeSSHKeyFile, "ssh-key-file", "", "path to a private key file for SSH auth")
		c.Flags().StringVar(&nodeSSHKeyID, "ssh-key-id", "", "id of a key in kuso's SSH key library (server-side lookup)")
	}
	nodeValidateCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	nodeCmd.AddCommand(nodeValidateCmd)

	nodeJoinCmd.Flags().StringSliceVar(&nodeJoinLabels, "label", nil, "repeatable key=value label baked onto the joined node")
	nodeJoinCmd.Flags().StringVar(&nodeJoinName, "name", "", "override the joined node's name (default: VM hostname)")
	nodeCmd.AddCommand(nodeJoinCmd)

	nodeRemoveCmd.Flags().BoolVar(&nodeRemoveForce, "force", false, "skip graceful pod eviction during drain")
	nodeCmd.AddCommand(nodeRemoveCmd)

	nodeUpdatesCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	nodeCmd.AddCommand(nodeUpdatesCmd)

	nodeApplyUpdatesCmd.Flags().BoolVar(&nodeApplyReboot, "allow-reboot", false, "permit cordon/drain/reboot when an update needs a restart")
	nodeCmd.AddCommand(nodeApplyUpdatesCmd)

	nodeHistoryCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	nodeCmd.AddCommand(nodeHistoryCmd)

	nodeCleanupCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	nodeCmd.AddCommand(nodeCleanupCmd)

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
