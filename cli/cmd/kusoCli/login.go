package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/i582/cfmt/cmd/cfmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

type Access struct {
	AccessToken string `json:"access_token"`
}

var (
	loginInstance string // name to use in ~/.kuso/config (default: derived from API URL host)
	loginAPIURL   string // e.g. https://kuso.sislelabs.com
	loginUsername string
	loginPassword string
	loginToken    string // skip user/pass and store this token directly
)

var loginCmd = &cobra.Command{
	Use:     "login",
	Aliases: []string{"li"},
	Short:   "Login to a Kuso instance",
	Long: `Authenticate against a Kuso instance and store the session token
in ~/.kuso/credentials.yaml. Subsequent commands read it from there.

Interactive (default):
    kuso login

Non-interactive (CI / scripts):
    kuso login --api https://kuso.example.com \
               --username admin --password <pass>

    kuso login --api https://kuso.example.com --token <jwt-or-api-token>

The first non-interactive call also creates the instance entry if it
doesn't exist yet, so login can run on a fresh machine in one shot.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Non-interactive path: --api triggers it. We add the instance
		// entry if needed, set it as current, then login (or stash token).
		if loginAPIURL != "" {
			ensureInstanceFromFlags()
		} else {
			ensureInstanceOrCreate()
		}

		if loginToken != "" {
			setKusoCredentials(loginToken)
			cfmt.Print("  {{Token saved}}::green|bold\n")
			return nil
		}

		login(loginUsername, loginPassword)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringVar(&loginAPIURL, "api", "", "Kuso API URL (e.g. https://kuso.example.com). Triggers non-interactive mode.")
	loginCmd.Flags().StringVar(&loginInstance, "instance", "", "Instance name in ~/.kuso/config.yaml. Defaults to the API hostname.")
	loginCmd.Flags().StringVarP(&loginUsername, "username", "u", "", "Username (non-interactive)")
	loginCmd.Flags().StringVarP(&loginPassword, "password", "p", "", "Password (non-interactive)")
	loginCmd.Flags().StringVar(&loginToken, "token", "", "Pre-issued JWT or API token; skips user/password")
}

// ensureInstanceFromFlags adds (if missing) and selects the instance
// derived from --api / --instance flags. No prompts.
//
// Viper is package-level state in this CLI and uses "." as a key
// delimiter, which breaks instance names with dots
// (kuso.sislelabs.com -> nested map). Rather than rewire viper across
// the whole CLI, we write the instance entry and current-instance
// pointer with the API client directly: persist into the in-memory
// map, then call WriteConfig. The kuso.yaml ends up with a flat
// "instances: {name: {apiurl: ...}}" tree where viper's quoting
// preserves dotted names as actual keys.
func ensureInstanceFromFlags() {
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
		ConfigPath: viper.ConfigFileUsed(),
	}

	// viper splits keys on "." even when writing, so dotted instance
	// names like "kuso.sislelabs.com" become nested maps. Bypass viper
	// for this write — emit the YAML directly with yaml.v3 so the key
	// is preserved as a single string. We still rely on viper for reads
	// at startup; loadInstances also flattens dotted keys back together.
	cfgPath := viper.ConfigFileUsed()
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".kuso", "kuso.yaml")
		_ = os.MkdirAll(filepath.Dir(cfgPath), 0o700)
	}
	all := readKusoYAML(cfgPath)
	instances, _ := all["instances"].(map[string]any)
	if instances == nil {
		instances = map[string]any{}
	}
	instances[name] = map[string]any{
		"apiurl":     loginAPIURL,
		"iacBaseDir": ".kuso",
	}
	all["instances"] = instances
	all["currentInstance"] = name
	if api2, ok := all["api"].(map[string]any); !ok || api2 == nil {
		all["api"] = map[string]any{"url": loginAPIURL}
	}
	if err := writeKusoYAML(cfgPath, all); err != nil {
		fmt.Println("Failed to save configuration:", err)
		return
	}

	currentInstanceName = name
	currentInstance = instanceList[name]
	api.SetApiUrl(loginAPIURL, "")
}

func readKusoYAML(path string) map[string]any {
	out := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = yaml.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func writeKusoYAML(path string, data map[string]any) error {
	b, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func hostFromURL(u string) string {
	// Lightweight: strip scheme + path, keep host. Avoids importing
	// net/url for a one-liner.
	s := u
	for _, prefix := range []string{"https://", "http://"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			s = s[len(prefix):]
			break
		}
	}
	for i, c := range s {
		if c == '/' || c == ':' {
			return s[:i]
		}
	}
	return s
}

func ensureInstanceOrCreate() {

	instanceNameList = append(instanceNameList, "<create new>")

	instanceName := selectFromList("Select an instance", instanceNameList, currentInstanceName)
	if instanceName == "<create new>" {
		createInstanceForm()
	} else {
		setCurrentInstance(instanceName)
	}

}

func setKusoCredentials(token string) {

	if token == "" {
		token = promptLine("Token", "", "")
	}

	credentialsConfig.Set(currentInstanceName, token)
	writeConfigErr := credentialsConfig.WriteConfig()
	if writeConfigErr != nil {
		fmt.Println("Error writing config file: ", writeConfigErr)
		return
	}
}

func login(user string, pass string) {

	if user == "" {
		user = promptLine("Username", "", "")
	}

	if pass == "" {
		cfmt.Print("\n{{?}}::green|bold {{Password}}::bold   : ")
		bytepw, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			fmt.Println("Error reading password: ", err)
			return
		}
		pass = string(bytepw)
		fmt.Print("XXXXXXXXXXXXXXX\n\n")
	}

	res, err := api.Login(user, pass)
	if err != nil {
		fmt.Println("Error: ", err)
		return
	}

	if res.StatusCode() >= 200 && res.StatusCode() < 300 {

		var a Access
		json.Unmarshal(res.Body(), &a)

		cfmt.Print("  {{Login successful}}::green|bold\n\n")
		setKusoCredentials(a.AccessToken)
	} else {
		fmt.Println(res.StatusCode(), "Login failed")
	}

}
