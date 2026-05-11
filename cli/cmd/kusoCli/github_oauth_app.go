package kusoCli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso github oauth-app` — register a GitHub OAuth App for a deployed
// service's "Sign in with GitHub" flow. This is NOT the kuso GitHub
// App (which is for repo access + webhooks); OAuth Apps are a
// separate GitHub primitive used for user-identity logins.
//
// GitHub's OAuth-App callback validation does prefix-matching on host
// (excluding sub-domains as a unit), so one OAuth App can't reasonably
// cover N kuso-deployed projects on different sub-domains. Cleanest
// pattern: one OAuth App per project, with this command pre-filling
// the GitHub-side form and stashing the resulting creds as kuso
// secrets on the service.
//
// Two subcommands:
//
//   kuso github oauth-app prepare <project> [<service>]
//       Resolve the service's public URL, print a pre-filled
//       github.com/settings/applications/new URL + the exact callback
//       URL to paste, then interactively read Client ID + Secret from
//       stdin and `kuso secret set` them on the service.
//
//   kuso github oauth-app set <project> <service> \
//       --client-id <id> --client-secret <secret> [--key-prefix GITHUB]
//       Non-interactive variant for CI / re-runs.
//
// Both write `<PREFIX>_CLIENT_ID` (plain env) and
// `<PREFIX>_CLIENT_SECRET` (secret) on the service. Default prefix is
// `GITHUB`, matching Better Auth's social-provider env convention.

var githubOauthAppCmd = &cobra.Command{
	Use:     "oauth-app",
	Aliases: []string{"oauth"},
	Short:   "Register a per-project GitHub OAuth App for 'Sign in with GitHub'",
}

var (
	gOauthPrepKeyPrefix string
	gOauthPrepCallback  string
	gOauthPrepAppName   string
	gOauthPrepNoInput   bool
)

var githubOauthAppPrepareCmd = &cobra.Command{
	Use:   "prepare <project> [<service>]",
	Short: "Walk through creating an OAuth App for one service",
	Long: `Resolve the service's public URL, print a pre-filled
github.com/settings/applications/new URL with the right Homepage and
Authorization callback set, then read the Client ID + Secret you get
from GitHub and store them as kuso env / secret on the service.

If <service> is omitted, the project's first service is used (works
for the common single-service-per-project case).

Examples:

  # Interactive (default):
  kuso github oauth-app prepare papelito web

  # Use a custom env-var prefix (Better Auth's default is GITHUB):
  kuso github oauth-app prepare papelito web --key-prefix GITHUB

  # Just print the URL + skip the interactive paste step:
  kuso github oauth-app prepare papelito web --no-input
`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runGithubOauthAppPrepare,
}

func runGithubOauthAppPrepare(cmd *cobra.Command, args []string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	project := args[0]
	service := ""
	if len(args) >= 2 {
		service = args[1]
	}

	svc, callbackURL, homepageURL, err := resolveServiceURLs(project, service, gOauthPrepCallback)
	if err != nil {
		return err
	}
	service = svc

	appName := gOauthPrepAppName
	if appName == "" {
		// Default to "<project>-<service>" — short, unique-per-project,
		// fits in GitHub's 34-char Application name limit for most names.
		appName = fmt.Sprintf("%s-%s", project, service)
	}

	// Build the prefill URL. Fields github accepts via query params:
	// name, url (Homepage URL), callback_url, description. The form
	// page reads them and prefills the inputs — user still has to
	// click "Register application."
	u := &url.URL{
		Scheme: "https",
		Host:   "github.com",
		Path:   "/settings/applications/new",
	}
	q := u.Query()
	q.Set("oauth_application[name]", appName)
	q.Set("oauth_application[url]", homepageURL)
	q.Set("oauth_application[callback_url]", callbackURL)
	q.Set("oauth_application[description]",
		fmt.Sprintf("Sign-in for %s/%s on kuso", project, service))
	u.RawQuery = q.Encode()

	fmt.Println()
	fmt.Println("OAuth App setup for", project+"/"+service)
	fmt.Println()
	fmt.Println("  Service URL:    ", homepageURL)
	fmt.Println("  Callback URL:   ", callbackURL)
	fmt.Println("  Application:    ", appName)
	fmt.Println()
	fmt.Println("Open this URL in a browser; fields are pre-filled:")
	fmt.Println()
	fmt.Println("  " + u.String())
	fmt.Println()
	fmt.Println("After you click 'Register application', GitHub shows you the")
	fmt.Println("Client ID and Client Secret. Paste them below.")
	fmt.Println()

	if gOauthPrepNoInput {
		fmt.Println("--no-input set; not reading creds. Finish with:")
		fmt.Println()
		fmt.Printf("  kuso github oauth-app set %s %s \\\n", project, service)
		fmt.Println("      --client-id <id> --client-secret <secret>")
		fmt.Println()
		return nil
	}

	scanner := bufio.NewScanner(os.Stdin)
	clientID := readLine(scanner, "Client ID")
	if clientID == "" {
		return fmt.Errorf("aborted: client ID is empty")
	}
	clientSecret := readLine(scanner, "Client Secret")
	if clientSecret == "" {
		return fmt.Errorf("aborted: client secret is empty")
	}

	return saveOauthCreds(project, service, gOauthPrepKeyPrefix, clientID, clientSecret)
}

var (
	gOauthSetClientID     string
	gOauthSetClientSecret string
	gOauthSetKeyPrefix    string
)

var githubOauthAppSetCmd = &cobra.Command{
	Use:   "set <project> <service>",
	Short: "Store an existing OAuth App's creds on a service (non-interactive)",
	Long: `Non-interactive variant of "prepare." Use when you already have
a Client ID and Client Secret in hand (CI scripts, re-running after
secret rotation, etc.).

Writes:
  <PREFIX>_CLIENT_ID     as a plain env var on the service
  <PREFIX>_CLIENT_SECRET as a kuso secret on the service

Default prefix is GITHUB, matching Better Auth's social-provider
env-var convention.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if gOauthSetClientID == "" || gOauthSetClientSecret == "" {
			return fmt.Errorf("--client-id and --client-secret are both required")
		}
		return saveOauthCreds(args[0], args[1], gOauthSetKeyPrefix, gOauthSetClientID, gOauthSetClientSecret)
	},
}

// resolveServiceURLs picks the right (or only) service and computes
// the callback URL. Order of precedence for the public hostname:
//
//  1. explicit --callback flag (user override).
//  2. service.spec.domains[0] (custom domain).
//  3. service.status.deployedRelease auto-domain ("<svc>.<basedomain>").
//
// Returns: resolved service name, callback URL, homepage URL.
func resolveServiceURLs(project, requestedService, explicitCallback string) (string, string, string, error) {
	resp, err := api.GetServices(project)
	if err != nil {
		return "", "", "", err
	}
	var services []map[string]any
	if err := json.Unmarshal(resp.Body(), &services); err != nil {
		return "", "", "", fmt.Errorf("parse services: %w", err)
	}
	if len(services) == 0 {
		return "", "", "", fmt.Errorf("project %q has no services", project)
	}

	var svc map[string]any
	if requestedService == "" {
		if len(services) > 1 {
			names := make([]string, 0, len(services))
			for _, s := range services {
				if name := serviceName(s); name != "" {
					names = append(names, name)
				}
			}
			return "", "", "", fmt.Errorf(
				"project %q has multiple services; pick one of: %s",
				project, strings.Join(names, ", "))
		}
		svc = services[0]
		requestedService = serviceName(svc)
	} else {
		want := fmt.Sprintf("%s-%s", project, requestedService)
		for _, s := range services {
			n := serviceName(s)
			if n == want || n == requestedService {
				svc = s
				break
			}
		}
		if svc == nil {
			return "", "", "", fmt.Errorf("service %q not found in project %q", requestedService, project)
		}
	}

	host := ""
	if explicitCallback != "" {
		host = explicitCallback
	} else {
		host = pickServiceURL(svc)
	}
	if host == "" {
		return "", "", "", fmt.Errorf(
			"could not determine public URL for %s/%s; pass --callback explicitly",
			project, requestedService)
	}

	// Ensure scheme. kuso always serves HTTPS on its auto-domains.
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "https://" + host
	}
	host = strings.TrimRight(host, "/")
	homepage := host
	// Better Auth's standard callback path. The provider id in the env
	// (GITHUB_*) maps to /api/auth/callback/github at runtime.
	callback := host + "/api/auth/callback/github"

	if explicitCallback != "" && !strings.HasPrefix(explicitCallback, "http") {
		// User passed a path-only or bare host; we already prefixed
		// scheme above. Leave callback as-is.
	}
	return requestedService, callback, homepage, nil
}

func serviceName(s map[string]any) string {
	if meta, ok := s["metadata"].(map[string]any); ok {
		if n, ok := meta["name"].(string); ok {
			return n
		}
	}
	if n, ok := s["name"].(string); ok {
		return n
	}
	return ""
}

// pickServiceURL pulls the public URL from a service CR. Custom domains
// take precedence; otherwise we fall back to the auto-generated host
// stamped into the deployed-release manifest.
func pickServiceURL(s map[string]any) string {
	spec, _ := s["spec"].(map[string]any)
	if spec != nil {
		if domains, ok := spec["domains"].([]any); ok && len(domains) > 0 {
			if d, ok := domains[0].(string); ok && d != "" {
				return d
			}
			if d, ok := domains[0].(map[string]any); ok {
				if h, ok := d["host"].(string); ok && h != "" {
					return h
				}
			}
		}
	}
	// Fall through to deployedRelease.manifest scrape (it's the
	// rendered ConfigMap, which we can string-grep for the auto
	// host).
	status, _ := s["status"].(map[string]any)
	if status != nil {
		if dr, ok := status["deployedRelease"].(map[string]any); ok {
			if m, ok := dr["manifest"].(string); ok {
				// Look for a line like `repoUrl: "..."` or `host: ...`.
				// Easier: kuso stamps an annotation with the auto URL,
				// but if not present, the cluster-default base domain
				// is project-wide. As a last resort, derive from the
				// service name + project's baseDomain — but that
				// requires another API call. Empty string is fine here
				// — caller asks the user for --callback.
				_ = m
			}
		}
	}
	return ""
}

func readLine(scanner *bufio.Scanner, label string) string {
	fmt.Printf("  %s: ", label)
	if !scanner.Scan() {
		return ""
	}
	return strings.TrimSpace(scanner.Text())
}

// saveOauthCreds writes <PREFIX>_CLIENT_ID as a plain env var and
// <PREFIX>_CLIENT_SECRET as a secret. /api/.../env is a complete-replace
// endpoint (see env set in env.go), so we read-modify-write to keep
// every other var the user has already set; otherwise this would wipe
// DATABASE_URL etc.
func saveOauthCreds(project, service, prefix, clientID, clientSecret string) error {
	if prefix == "" {
		prefix = "GITHUB"
	}
	prefix = strings.ToUpper(prefix)
	idKey := prefix + "_CLIENT_ID"
	secretKey := prefix + "_CLIENT_SECRET"

	// Read current env list.
	current, err := api.GetEnv(project, service)
	if err != nil {
		return fmt.Errorf("read current env: %w", err)
	}
	if current.StatusCode() >= 300 {
		return fmt.Errorf("read current env: server returned %d: %s",
			current.StatusCode(), string(current.Body()))
	}
	var existing struct {
		EnvVars []map[string]any `json:"envVars"`
	}
	if err := json.Unmarshal(current.Body(), &existing); err != nil {
		return fmt.Errorf("decode current env: %w", err)
	}

	// Merge: keep every existing entry verbatim (incl. valueFrom refs
	// to addon Secrets), upsert our idKey.
	byName := map[string]map[string]any{}
	for _, e := range existing.EnvVars {
		row := map[string]any{"name": e["name"]}
		if v, ok := e["value"]; ok && v != nil {
			row["value"] = v
		}
		if vf, ok := e["valueFrom"]; ok && vf != nil {
			row["valueFrom"] = vf
		}
		byName[asString(e["name"])] = row
	}
	byName[idKey] = map[string]any{"name": idKey, "value": clientID}

	out := make([]map[string]any, 0, len(byName))
	for _, v := range byName {
		out = append(out, v)
	}
	resp, err := api.SetEnv(project, service, kusoApi.SetEnvRequest{EnvVars: out})
	if err != nil {
		return fmt.Errorf("set %s: %w", idKey, err)
	}
	if resp.StatusCode() >= 300 {
		return fmt.Errorf("set %s: server returned %d: %s",
			idKey, resp.StatusCode(), string(resp.Body()))
	}

	sresp, err := api.SetSecret(project, service, kusoApi.SetSecretRequest{
		Key:   secretKey,
		Value: clientSecret,
	})
	if err != nil {
		return fmt.Errorf("set %s: %w", secretKey, err)
	}
	if sresp.StatusCode() >= 300 {
		return fmt.Errorf("set %s: server returned %d: %s",
			secretKey, sresp.StatusCode(), string(sresp.Body()))
	}

	fmt.Println()
	fmt.Printf("  ✓ set %s=%s on %s/%s\n", idKey, clientID, project, service)
	fmt.Printf("  ✓ set %s=<secret> on %s/%s\n", secretKey, project, service)
	fmt.Println()
	fmt.Println("Trigger a redeploy so the new env lands on the pod:")
	fmt.Printf("    kuso redeploy %s %s\n", project, service)
	fmt.Println()
	return nil
}

func init() {
	githubCmd.AddCommand(githubOauthAppCmd)
	githubOauthAppCmd.AddCommand(githubOauthAppPrepareCmd)
	githubOauthAppCmd.AddCommand(githubOauthAppSetCmd)

	githubOauthAppPrepareCmd.Flags().StringVar(&gOauthPrepKeyPrefix, "key-prefix", "GITHUB",
		"env-var name prefix (e.g. GITHUB → GITHUB_CLIENT_ID/_SECRET)")
	githubOauthAppPrepareCmd.Flags().StringVar(&gOauthPrepCallback, "callback", "",
		"override the service's auto-detected public URL (rare)")
	githubOauthAppPrepareCmd.Flags().StringVar(&gOauthPrepAppName, "app-name", "",
		"GitHub Application name (default: <project>-<service>)")
	githubOauthAppPrepareCmd.Flags().BoolVar(&gOauthPrepNoInput, "no-input", false,
		"just print the URL; don't ask for Client ID/Secret on stdin")

	githubOauthAppSetCmd.Flags().StringVar(&gOauthSetClientID, "client-id", "", "OAuth Client ID (required)")
	githubOauthAppSetCmd.Flags().StringVar(&gOauthSetClientSecret, "client-secret", "", "OAuth Client Secret (required)")
	githubOauthAppSetCmd.Flags().StringVar(&gOauthSetKeyPrefix, "key-prefix", "GITHUB",
		"env-var name prefix")
}
