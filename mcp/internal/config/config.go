// Package config holds runtime configuration for kuso-mcp.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the runtime configuration for kuso-mcp.
type Config struct {
	// URL is the base URL of the kuso server (e.g. https://kuso.example.com).
	URL string

	// Token is the API token used to authenticate against the kuso server.
	Token string

	// ReadOnly disables tools that mutate state when true.
	ReadOnly bool
}

// FromEnv resolves the kuso-mcp configuration. Environment variables win:
// when both KUSO_URL and KUSO_TOKEN are set they are used as-is. Anything
// missing falls back to the kuso CLI's config under ~/.kuso/ —
// kuso.yaml (instance list + currentInstance) for the URL and
// credentials.yaml (instance-name → bearer token, written by `kuso login`)
// for the token — so a logged-in CLI needs no extra env plumbing.
// ReadOnly defaults to false; callers set it from CLI flags.
func FromEnv() (*Config, error) {
	envURL := strings.TrimRight(strings.TrimSpace(os.Getenv("KUSO_URL")), "/")
	envToken := strings.TrimSpace(os.Getenv("KUSO_TOKEN"))
	if envURL != "" && envToken != "" {
		return &Config{URL: envURL, Token: envToken}, nil
	}

	cli := loadCLIConfig()

	// Pick the instance the CLI would talk to. An explicit KUSO_URL
	// selects by URL; otherwise the CLI's currentInstance wins, and a
	// sole configured instance is unambiguous enough to use directly.
	instName, instURL, err := cli.pickInstance(envURL)
	if err != nil {
		return nil, err
	}

	resolvedURL := envURL
	if resolvedURL == "" {
		resolvedURL = strings.TrimRight(strings.TrimSpace(instURL), "/")
	}
	if resolvedURL == "" {
		return nil, errors.New("KUSO_URL is not set and no kuso CLI config was found (~/.kuso/kuso.yaml); set KUSO_URL or run `kuso login`")
	}

	resolvedToken := envToken
	if resolvedToken == "" {
		resolvedToken = cli.tokenFor(instName, resolvedURL)
	}
	if resolvedToken == "" {
		return nil, fmt.Errorf("KUSO_TOKEN is not set and no credential for %s was found in the kuso CLI credentials file (~/.kuso/credentials.yaml); set KUSO_TOKEN or run `kuso login`", resolvedURL)
	}

	return &Config{URL: resolvedURL, Token: resolvedToken}, nil
}

// cliConfig is the read-only view of the kuso CLI's on-disk state:
// ~/.kuso/kuso.yaml (instances + currentInstance) and
// ~/.kuso/credentials.yaml (instance-name → token, 0600). Both files are
// best-effort: absent or malformed files just leave the maps empty and
// the env-var error paths report what to do.
type cliConfig struct {
	instances map[string]string // instance name → apiurl
	current   string            // currentInstance from kuso.yaml, may be ""
	tokens    map[string]string // instance name → bearer token (keys may be lowercased by the CLI's writer)
}

func loadCLIConfig() *cliConfig {
	c := &cliConfig{
		instances: map[string]string{},
		tokens:    map[string]string{},
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return c
	}

	// kuso.yaml: {instances: {<name>: {apiurl: ...}}, currentInstance: <name>}
	if raw, err := os.ReadFile(filepath.Join(home, ".kuso", "kuso.yaml")); err == nil {
		var doc struct {
			Instances map[string]struct {
				ApiUrl string `yaml:"apiurl"`
			} `yaml:"instances"`
			CurrentInstance string `yaml:"currentInstance"`
		}
		if yaml.Unmarshal(raw, &doc) == nil {
			for name, inst := range doc.Instances {
				c.instances[name] = inst.ApiUrl
			}
			c.current = doc.CurrentInstance
		}
	}

	// credentials.yaml: flat map of instance name → token. The CLI also
	// consults /etc/kuso/ as a fallback location; mirror that.
	for _, path := range []string{
		filepath.Join(home, ".kuso", "credentials.yaml"),
		filepath.Join("/etc/kuso", "credentials.yaml"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var creds map[string]string
		if yaml.Unmarshal(raw, &creds) != nil {
			continue
		}
		for name, tok := range creds {
			c.tokens[name] = tok
		}
		break
	}
	return c
}

// pickInstance chooses which CLI instance to use. With an explicit
// envURL the match is by apiurl (or by instance name == URL host); no
// match is fine — the caller may still have KUSO_TOKEN or a host-keyed
// credential. Without envURL: currentInstance if set, else a sole
// configured instance, else an error naming the choices.
func (c *cliConfig) pickInstance(envURL string) (name, apiURL string, err error) {
	if envURL != "" {
		want := strings.TrimRight(envURL, "/")
		for n, u := range c.instances {
			if strings.TrimRight(strings.TrimSpace(u), "/") == want {
				return n, u, nil
			}
		}
		if host := hostOf(envURL); host != "" {
			for n, u := range c.instances {
				if strings.EqualFold(n, host) {
					return n, u, nil
				}
			}
		}
		return "", "", nil
	}
	if c.current != "" {
		return c.current, c.instances[c.current], nil
	}
	if len(c.instances) == 1 {
		for n, u := range c.instances {
			return n, u, nil
		}
	}
	if len(c.instances) > 1 {
		names := make([]string, 0, len(c.instances))
		for n := range c.instances {
			names = append(names, n)
		}
		sort.Strings(names)
		return "", "", fmt.Errorf("kuso CLI config has multiple instances (%s) and no currentInstance; set KUSO_URL to pick one (or `kuso remote use <name>`)", strings.Join(names, ", "))
	}
	return "", "", nil
}

// tokenFor looks up the bearer token for an instance. The CLI's
// credentials writer (viper) lowercases keys, so the lookup is
// case-insensitive; when the instance name is unknown we also try the
// URL host, which is what instance names conventionally are.
func (c *cliConfig) tokenFor(instName, resolvedURL string) string {
	candidates := []string{}
	if instName != "" {
		candidates = append(candidates, instName)
	}
	if host := hostOf(resolvedURL); host != "" {
		candidates = append(candidates, host)
	}
	for _, want := range candidates {
		for name, tok := range c.tokens {
			if strings.EqualFold(name, want) && strings.TrimSpace(tok) != "" {
				return strings.TrimSpace(tok)
			}
		}
	}
	return ""
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
