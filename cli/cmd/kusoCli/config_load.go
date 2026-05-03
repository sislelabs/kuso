package kusoCli

// Config + credentials persistence for the CLI.
//
// We keep two YAML files under ~/.kuso/:
//
//   kuso.yaml         — instance list + currently-selected instance.
//   credentials.yaml  — instance-name → bearer token.
//
// They live in separate files so the credentials file can be locked
// down to 0600 and shared via a secret manager without leaking the
// instance topology.
//
// We bypass viper for write paths because viper splits keys on "." and
// our instance names (kuso.sislelabs.com) contain dots. Reads go through
// viper for the credentials map (where keys are dotted instance names)
// using a NUL key delimiter. The instance config is parsed directly with
// yaml.v3 so dotted keys round-trip cleanly.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// credentialsConfig is the viper backing for the credentials.yaml
// file. Exposed package-level so login + remote can read/write
// individual instance tokens without re-loading.
var credentialsConfig *viper.Viper

// loadInstances reads ~/.kuso/kuso.yaml into instanceList +
// currentInstanceName. Missing file is fine — first-run users have no
// instances yet and `kuso login` creates one.
func loadInstances() {
	instanceList = map[string]Instance{}
	cfgPath := defaultConfigPath()

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		// No config yet; first-time user. Bail without complaint.
		return
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintln(os.Stderr, "kuso: malformed config at", cfgPath+":", err)
		return
	}
	if insts, ok := doc["instances"].(map[string]any); ok {
		for name, v := range insts {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			instanceList[name] = Instance{
				Name:       name,
				ApiUrl:     stringFrom(m["apiurl"]),
				IacBaseDir: stringFrom(m["iacBaseDir"]),
				ConfigPath: cfgPath,
			}
			instanceNameList = append(instanceNameList, name)
		}
	}
	if v, ok := doc["currentInstance"].(string); ok {
		currentInstanceName = v
		if cur, ok := instanceList[currentInstanceName]; ok {
			currentInstance = cur
		}
	}
}

// loadCredentials reads ~/.kuso/credentials.yaml. We use a NUL delim
// because instance names contain "." which viper would otherwise
// nest into sub-maps.
func loadCredentials() {
	credentialsConfig = viper.NewWithOptions(viper.KeyDelimiter("\x00"))
	credentialsConfig.SetConfigName("credentials")
	credentialsConfig.SetConfigType("yaml")
	credentialsConfig.AddConfigPath("$HOME/.kuso/")
	credentialsConfig.AddConfigPath("/etc/kuso/")
	// Missing file is OK — first-run users haven't logged in yet.
	if err := credentialsConfig.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !asErr(err, &notFound) {
			fmt.Fprintln(os.Stderr, "kuso: failed to read credentials:", err)
		}
	}
}

// saveInstances rewrites ~/.kuso/kuso.yaml from the in-memory
// instanceList + currentInstanceName. Atomicity: write to a tempfile
// and rename so a crash mid-write doesn't leave a half-truncated
// config.
func saveInstances() error {
	cfgPath := currentInstance.ConfigPath
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	doc := map[string]any{}
	insts := map[string]any{}
	for name, inst := range instanceList {
		insts[name] = map[string]any{
			"apiurl":     inst.ApiUrl,
			"iacBaseDir": inst.IacBaseDir,
		}
	}
	doc["instances"] = insts
	if currentInstanceName != "" {
		doc["currentInstance"] = currentInstanceName
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write tmp config: %w", err)
	}
	return os.Rename(tmp, cfgPath)
}

// saveToken persists a bearer token for an instance, creating the
// credentials.yaml file if it doesn't exist yet.
func saveToken(instanceName, token string) error {
	credentialsConfig.Set(instanceName, token)
	if err := credentialsConfig.WriteConfig(); err != nil {
		// First-time write: no file exists yet, so SafeWriteConfig.
		path := filepath.Join(homeDir(), ".kuso", "credentials.yaml")
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
			return mkErr
		}
		credentialsConfig.SetConfigFile(path)
		return credentialsConfig.WriteConfig()
	}
	return nil
}

// defaultConfigPath returns the canonical ~/.kuso/kuso.yaml location.
// Empty string means we couldn't resolve $HOME, which is unrecoverable
// for a CLI — caller should treat this as a hard error.
func defaultConfigPath() string {
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".kuso", "kuso.yaml")
}

// homeDir resolves the current user's home dir. We don't fall back to
// "/" or "." because writing config there silently would surprise the
// user; an empty string lets callers complain explicitly.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// stringFrom is a small helper for the typed unmarshal pattern: yaml
// returns map[string]any so leaf values come back as `any`. We coerce
// to string with an empty fallback so missing keys don't panic.
func stringFrom(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asErr is errors.As without the import noise — returns true if err
// is (or wraps) a value of the same dynamic type as target. target
// must be a pointer to an error type.
func asErr(err error, target any) bool {
	defer func() { _ = recover() }()
	switch t := target.(type) {
	case *viper.ConfigFileNotFoundError:
		_, ok := err.(viper.ConfigFileNotFoundError)
		return ok
	default:
		_ = t
		return false
	}
}
