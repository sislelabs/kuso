package kusoCli

// `kuso login` — authenticate to a kuso server and persist the bearer
// token to ~/.kuso/credentials.yaml.
//
// Two paths:
//
//   Non-interactive (CI):
//     kuso login --api https://kuso.example.com --username admin --password '<pw>'
//     kuso login --api https://kuso.example.com --token <jwt>
//
//   Interactive:
//     kuso login          (picks an existing instance or creates one, then prompts)

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	loginAPIURL   string
	loginInstance string // override instance name; defaults to API host
	loginUsername string
	loginPassword string
	loginToken    string
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Login to a kuso instance",
	Long: `Authenticate against a kuso server. The resulting token is stored
in ~/.kuso/credentials.yaml and reused by subsequent commands.

Use --api to login non-interactively (creates the instance entry on
the fly). Use --token to install a pre-issued API token without
trading credentials.`,
	Example: `  kuso login
  kuso login --api https://kuso.example.com -u admin -p '<password>'
  kuso login --api https://kuso.example.com --token <jwt>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if loginAPIURL != "" {
			if err := upsertInstanceFromFlags(); err != nil {
				return err
			}
		} else {
			if err := pickOrCreateInstance(); err != nil {
				return err
			}
		}

		if currentInstance.ApiUrl == "" {
			return fmt.Errorf("no API URL configured for the current instance")
		}

		if loginToken != "" {
			return persistToken(loginToken)
		}
		return runUsernamePasswordLogin()
	},
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringVar(&loginAPIURL, "api", "", "Kuso API URL (e.g. https://kuso.example.com); triggers non-interactive mode")
	loginCmd.Flags().StringVar(&loginInstance, "instance", "", "Instance name in ~/.kuso/kuso.yaml (defaults to the API hostname)")
	loginCmd.Flags().StringVarP(&loginUsername, "username", "u", "", "Username (non-interactive)")
	loginCmd.Flags().StringVarP(&loginPassword, "password", "p", "", "Password (non-interactive)")
	loginCmd.Flags().StringVar(&loginToken, "token", "", "Pre-issued JWT or API token; skips username/password")
}

// upsertInstanceFromFlags adds (or updates) the instance for the
// --api / --instance flags. Used by the non-interactive path so a CI
// job can run `kuso login --api ... --token ...` on a fresh machine
// without an interactive setup step.
func upsertInstanceFromFlags() error {
	name := loginInstance
	if name == "" {
		name = hostFromURL(loginAPIURL)
	}
	if name == "" {
		name = "default"
	}

	if instanceList == nil {
		instanceList = map[string]Instance{}
	}
	instanceList[name] = Instance{
		Name:       name,
		ApiUrl:     loginAPIURL,
		IacBaseDir: ".kuso",
		ConfigPath: defaultConfigPath(),
	}
	if !contains(instanceNameList, name) {
		instanceNameList = append(instanceNameList, name)
	}
	currentInstanceName = name
	currentInstance = instanceList[name]
	api.SetApiUrl(loginAPIURL, "")
	return saveInstances()
}

// pickOrCreateInstance is the interactive selector. If the user has
// no instances yet we drop straight into the create wizard; otherwise
// they pick one (or create a new one).
func pickOrCreateInstance() error {
	if len(instanceNameList) == 0 {
		return createRemote()
	}
	options := append([]string{}, instanceNameList...)
	options = append(options, "<create new>")
	pick := selectFromList("Pick an instance", options, currentInstanceName)
	if pick == "<create new>" {
		return createRemote()
	}
	return selectRemote(pick)
}

// runUsernamePasswordLogin prompts for credentials (when not provided
// via flags), POSTs to /api/auth/login, and persists the returned
// access_token under the current instance.
func runUsernamePasswordLogin() error {
	user := loginUsername
	if user == "" {
		user = promptLine("Username", "", "")
	}
	pass := loginPassword
	if pass == "" {
		fmt.Print("? Password: ")
		bytePw, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		pass = string(bytePw)
	}

	resp, err := api.Login(user, pass)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return fmt.Errorf("login failed (%d): %s", resp.StatusCode(), strings.TrimSpace(string(resp.Body())))
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	if body.AccessToken == "" {
		return fmt.Errorf("server returned empty access_token")
	}
	return persistToken(body.AccessToken)
}

// persistToken writes the bearer to credentials.yaml and re-points
// the in-process API client so the rest of this command run is
// authenticated.
func persistToken(token string) error {
	if currentInstanceName == "" {
		return fmt.Errorf("no current instance to attach token to")
	}
	if err := saveToken(currentInstanceName, token); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	api.SetApiUrl(currentInstance.ApiUrl, token)
	fmt.Fprintln(os.Stderr, "Login successful.")
	return nil
}

// hostFromURL pulls the hostname out of a URL with a permissive parser
// — we want this to work on rough inputs like "kuso.example.com" too,
// not just RFC 3986 forms.
func hostFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
