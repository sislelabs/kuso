package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

// `kuso shell <project> <service>` — opens an interactive shell in
// one of the pods backing a service. The kuso server doesn't host an
// exec WebSocket yet (parity gap with the web UI), so this command
// resolves the pod via the API and shells out to the operator's
// kubectl. That's a reasonable assumption: anyone running the CLI
// against a self-hosted kuso almost certainly has cluster access.

var (
	shellEnv       string
	shellContainer string
	shellCmd       string
)

var shellCmdCobra = &cobra.Command{
	Use:   "shell <project> <service>",
	Short: "Open a shell in a service's pod (via local kubectl)",
	Long: `Open an interactive shell in one of the pods backing a service.

Resolves the pod name via the kuso API, then execs into it using your
local kubectl. Requires kubectl on PATH and an active kubeconfig
context for the same cluster the kuso server is talking to.`,
	Example: `  kuso shell hello web
  kuso shell hello web --env staging
  kuso shell hello web --command /bin/bash`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if _, err := exec.LookPath("kubectl"); err != nil {
			return fmt.Errorf("kubectl not on PATH — kuso shell delegates to it")
		}
		// We piggyback on the same /pods endpoint the web UI's
		// service detail panel uses. Returns {namespace, pods:[{name,…}]}.
		path := fmt.Sprintf("/api/projects/%s/services/%s/pods?env=%s",
			args[0], args[1], shellEnv)
		resp, err := api.RawGet(path)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("pod lookup: %d %s", resp.StatusCode(), string(resp.Body()))
		}
		var info struct {
			Namespace string `json:"namespace"`
			Pods      []struct {
				Name  string `json:"name"`
				Ready bool   `json:"ready"`
			} `json:"pods"`
		}
		if err := json.Unmarshal(resp.Body(), &info); err != nil {
			return fmt.Errorf("decode pods: %w", err)
		}
		if len(info.Pods) == 0 {
			return fmt.Errorf("no pods running for %s/%s in env %s", args[0], args[1], shellEnv)
		}
		// Prefer a Ready pod over a not-yet-ready one. If none are
		// ready (maybe the user is debugging a crashloop), fall back
		// to the first pod and let kubectl exec error meaningfully.
		target := info.Pods[0].Name
		for _, p := range info.Pods {
			if p.Ready {
				target = p.Name
				break
			}
		}

		kArgs := []string{"-n", info.Namespace, "exec", "-it", target}
		if shellContainer != "" {
			kArgs = append(kArgs, "-c", shellContainer)
		}
		kArgs = append(kArgs, "--", shellCmd)
		fmt.Fprintf(os.Stderr, "==> kubectl %v\n", kArgs)
		c := exec.Command("kubectl", kArgs...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(shellCmdCobra)
	shellCmdCobra.Flags().StringVar(&shellEnv, "env", "production", "environment (production|preview-pr-N|<custom>)")
	shellCmdCobra.Flags().StringVarP(&shellContainer, "container", "c", "", "container name (defaults to first)")
	shellCmdCobra.Flags().StringVar(&shellCmd, "command", "/bin/sh", "command to exec inside the pod")
}
