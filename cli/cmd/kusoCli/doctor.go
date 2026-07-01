package kusoCli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
- TLS certificate validity (chain + expiry),
- GitHub webhook health (App configured + recent delivery).

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

		// TLS certificate — dial the API host and inspect the leaf
		// cert's validity window + chain. Only meaningful for https;
		// a plain-http server (localhost dev) has no cert to check, so
		// we skip rather than FAIL. KUSO_INSECURE=1 mirrors the client
		// setting and downgrades a verify failure to WARN (fresh boxes
		// run LE *staging* certs the system roots reject).
		if u, perr := url.Parse(serverURL); perr == nil && u.Scheme == "https" {
			host := u.Hostname()
			port := u.Port()
			if port == "" {
				port = "443"
			}
			insecure := func() bool {
				v := strings.TrimSpace(os.Getenv("KUSO_INSECURE"))
				return v == "1" || strings.EqualFold(v, "true")
			}()
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			// First dial WITH verification so we can distinguish a
			// broken chain (untrusted root / hostname mismatch) from a
			// merely-expiring-soon cert.
			verifyErr := func() error {
				conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, port), &tls.Config{ServerName: host})
				if err != nil {
					return err
				}
				conn.Close()
				return nil
			}()
			// Re-dial skipping verification purely to READ the leaf's
			// expiry — even when the chain is untrusted we still want to
			// surface how long the presented cert is valid.
			conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, port), &tls.Config{ServerName: host, InsecureSkipVerify: true})
			switch {
			case err != nil:
				report("TLS", fmt.Sprintf("%s: could not read certificate: %v", host, err), "fail")
			default:
				chain := conn.ConnectionState().PeerCertificates
				conn.Close()
				if len(chain) == 0 {
					report("TLS", fmt.Sprintf("%s presented no certificate", host), "fail")
					break
				}
				leaf := chain[0]
				now := time.Now()
				switch {
				case now.Before(leaf.NotBefore):
					report("TLS", fmt.Sprintf("%s: certificate not valid until %s", host, leaf.NotBefore.Format(time.RFC3339)), "fail")
				case now.After(leaf.NotAfter):
					report("TLS", fmt.Sprintf("%s: certificate EXPIRED %s", host, leaf.NotAfter.Format(time.RFC3339)), "fail")
				case verifyErr != nil && !insecure:
					report("TLS", fmt.Sprintf("%s: chain invalid: %v — set KUSO_INSECURE=1 if this is a fresh LE-staging box", host, verifyErr), "fail")
				case time.Until(leaf.NotAfter) < 14*24*time.Hour:
					detail := fmt.Sprintf("%s: expires %s (in %d days) — renewal may be stuck", host, leaf.NotAfter.Format(time.RFC3339), int(time.Until(leaf.NotAfter).Hours()/24))
					if verifyErr != nil {
						detail += fmt.Sprintf("; chain not trusted (%v) — ignored via KUSO_INSECURE", verifyErr)
					}
					report("TLS", detail, "warn")
				case verifyErr != nil:
					report("TLS", fmt.Sprintf("%s: valid until %s but chain not trusted (%v) — ignored via KUSO_INSECURE", host, leaf.NotAfter.Format(time.RFC3339), verifyErr), "warn")
				default:
					report("TLS", fmt.Sprintf("%s: valid, expires %s", host, leaf.NotAfter.Format(time.RFC3339)), "pass")
				}
			}
		}

		// GitHub webhook round-trip — is the App configured, and are
		// pushes actually landing? Admin-gated; needs the typed client
		// (bearer + Host header) rather than a bare http request. We only
		// probe when we have a token, since it 401s otherwise.
		if token != "" && api != nil {
			resp, err := api.WebhookHealth()
			switch {
			case err != nil:
				report("github", "webhook-health probe failed: "+err.Error(), "warn")
			case resp.StatusCode() == 401 || resp.StatusCode() == 403:
				report("github", fmt.Sprintf("webhook-health returned %d — admin token required for this check", resp.StatusCode()), "warn")
			case resp.StatusCode() >= 300:
				report("github", fmt.Sprintf("webhook-health returned %d: %s", resp.StatusCode(), strings.TrimSpace(string(resp.Body()))), "warn")
			default:
				var wh struct {
					Configured        bool   `json:"configured"`
					LastDeliveryAt    string `json:"lastDeliveryAt"`
					LastDeliveryEvent string `json:"lastDeliveryEvent"`
				}
				if jerr := json.Unmarshal(resp.Body(), &wh); jerr != nil {
					report("github", "decode webhook-health: "+jerr.Error(), "warn")
				} else if !wh.Configured {
					report("github", "GitHub App not configured — connect it in the dashboard (Settings → GitHub, click 'Create GitHub App') or via the install wizard", "fail")
				} else if wh.LastDeliveryAt == "" {
					report("github", "App configured but no webhook delivery ever received — pushes may not be reaching kuso; check the App's Advanced → Recent Deliveries on GitHub", "warn")
				} else if at, perr := time.Parse(time.RFC3339, wh.LastDeliveryAt); perr == nil && time.Since(at) > 7*24*time.Hour {
					report("github", fmt.Sprintf("last webhook delivery was %s (%s) — no recent delivery; check the App's Recent Deliveries on GitHub", wh.LastDeliveryAt, dashIfEmpty(wh.LastDeliveryEvent)), "warn")
				} else {
					detail := "App configured; last delivery " + wh.LastDeliveryAt
					if wh.LastDeliveryEvent != "" {
						detail += " (" + wh.LastDeliveryEvent + ")"
					}
					report("github", detail, "pass")
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
