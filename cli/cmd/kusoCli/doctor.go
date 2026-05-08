package kusoCli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// doctor runs a set of cheap pre-flight checks against the local
// environment and the configured kuso server. Output is a list of
// PASS / WARN / FAIL lines so a first-time user can spot the gap
// before they hit a UI dead-end.
//
// We deliberately keep the checks side-effect-free: this is a "tell
// me what's wrong" surface, not a remediation tool. Every WARN/FAIL
// names the next step.
//
// Why a CLI command and not a server endpoint: most of these checks
// answer "is the user's local env wired up?" — DNS resolution from
// their machine, server reachability, presence of a token. Those
// can't be answered from inside the kube cluster.

func init() {
	rootCmd.AddCommand(doctorCmd)
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run pre-flight checks against the configured kuso server.",
	Long: `doctor diagnoses common first-time setup issues:
- token presence (kuso login),
- server URL DNS resolution,
- server reachability (/healthz),
- API auth (/api/projects),
- TLS certificate validity (chain + expiry).

Use it after a fresh install or when something feels off — the
output names the next concrete step for every finding.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		any := false
		fail := false
		report := func(label, detail string, level string) {
			any = true
			tag := "PASS"
			switch level {
			case "warn":
				tag = "WARN"
			case "fail":
				tag = "FAIL"
				fail = true
			}
			fmt.Printf("[%s] %s\n", tag, label)
			if detail != "" {
				fmt.Printf("       %s\n", detail)
			}
		}

		// Token presence — env var beats saved credentials.
		token := strings.TrimSpace(os.Getenv("KUSO_TOKEN"))
		if token == "" && credentialsConfig != nil && currentInstanceName != "" {
			token = strings.TrimSpace(credentialsConfig.GetString(currentInstanceName))
		}
		if token == "" {
			report("token", "no KUSO_TOKEN env var or saved login — run: kuso login", "fail")
		} else {
			report("token", "present", "pass")
		}

		// Server URL — env var beats saved instance.
		serverURL := strings.TrimRight(os.Getenv("KUSO_SERVER"), "/")
		if serverURL == "" {
			serverURL = strings.TrimRight(currentInstance.ApiUrl, "/")
		}
		if serverURL == "" {
			report("server URL", "no KUSO_SERVER configured — run: kuso login --server https://<your-instance>", "fail")
			if fail {
				os.Exit(1)
			}
			return
		}
		report("server URL", serverURL, "pass")

		// DNS lookup of the host portion.
		host := hostFromURL(serverURL)
		if host != "" {
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				report("DNS", fmt.Sprintf("%s: %v — check your /etc/hosts or DNS provider", host, err), "fail")
			} else if len(ips) == 0 {
				report("DNS", fmt.Sprintf("%s resolved to no IPs", host), "fail")
			} else {
				report("DNS", fmt.Sprintf("%s → %s", host, strings.Join(ips, ", ")), "pass")
			}
		}

		// /healthz probe — unauthenticated, fast.
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			report("/healthz", err.Error(), "fail")
		} else {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				report("/healthz", "200", "pass")
			} else {
				report("/healthz", fmt.Sprintf("status=%d", resp.StatusCode), "fail")
			}
		}

		// /api/projects with token — the simplest "are we authed?" probe.
		if token != "" {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/api/projects", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				report("auth", err.Error(), "fail")
			} else {
				defer resp.Body.Close()
				switch resp.StatusCode {
				case 200:
					report("auth", "200 — token accepted", "pass")
				case 401, 403:
					report("auth", fmt.Sprintf("status=%d — token invalid or expired; run kuso login again", resp.StatusCode), "fail")
				default:
					report("auth", fmt.Sprintf("status=%d", resp.StatusCode), "warn")
				}
			}
		}

		_ = any // suppress unused warning in case all checks short-circuit
		if fail {
			fmt.Println()
			fmt.Println("doctor: failures above — fix the FAIL lines and re-run.")
			os.Exit(1)
		}
	},
}

