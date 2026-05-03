package kusoCli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	initProject string
	initRuntime string
	initPort    int
	initForce   bool
)

func init() {
	initCmd.Flags().StringVar(&initProject, "project", "", "project name (default: current dir name)")
	initCmd.Flags().StringVar(&initRuntime, "runtime", "dockerfile", "runtime: dockerfile|nixpacks|static|buildpacks")
	initCmd.Flags().IntVar(&initPort, "port", 8080, "container port")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing kuso.yml")
	rootCmd.AddCommand(initCmd)
}

// initCmd writes a starter kuso.yml in the current directory. It
// derives the project + service name from the directory and the repo
// URL from `git config --get remote.origin.url` if available.
//
// We deliberately keep the generated file minimal — just enough to
// build + deploy. Users can fill in domains, scale, env, volumes
// later. The schema reference at the top points at the docs.
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write a starter kuso.yml in the current directory.",
	Example: `  kuso init
  kuso init --runtime nixpacks --port 3000`,
	Run: func(cmd *cobra.Command, args []string) {
		path := "kuso.yml"
		if _, err := os.Stat(path); err == nil && !initForce {
			fmt.Fprintln(os.Stderr, "kuso.yml already exists; pass --force to overwrite")
			os.Exit(1)
		}

		project := initProject
		if project == "" {
			cwd, _ := os.Getwd()
			project = sanitizeName(filepath.Base(cwd))
		}
		repo := guessGitRemote()

		body := renderInitYAML(project, project, repo, initRuntime, initPort)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote kuso.yml (project=%s, runtime=%s, port=%d)\n", project, initRuntime, initPort)
		fmt.Println("next:")
		fmt.Println("  edit kuso.yml to taste")
		fmt.Println("  kuso apply           # plan + push")
	},
}

// renderInitYAML returns a small starter file. Pure string templating
// so we don't have to worry about yaml.Marshal field ordering — we
// want this file to be human-friendly first, machine-friendly second.
func renderInitYAML(project, service, repo, runtime string, port int) string {
	repoLine := ""
	if repo != "" {
		repoLine = "    repo: " + repo + "\n"
	} else {
		repoLine = "    # repo: https://github.com/owner/repo\n"
	}
	return fmt.Sprintf(`# kuso.yml — config-as-code for kuso (https://kuso.sislelabs.com)
# This file is the source of truth on every push: the UI is read-only
# for fields managed here.

project: %s
# baseDomain: %s.example.com   # optional — auto-generated from project name otherwise

services:
  - name: %s
%s    runtime: %s
    port: %d
    scale:
      min: 1                   # 0 = sleep when idle
      max: 5
      targetCPU: 70
    # domains:
    #   - %s.example.com
    # env:
    #   LOG_LEVEL: info
    # volumes:
    #   - name: data
    #     mountPath: /var/lib/app
    #     sizeGi: 5

# addons:
#   - { name: db,    kind: postgres }
#   - { name: cache, kind: redis }
`, project, project, service, repoLine, runtime, port, service)
}

// guessGitRemote returns the canonical https URL of origin if `git
// config` resolves it. We don't shell out to the git binary — go-git
// is already a CLI dep, so we use the `.git/config` file directly
// to keep the binary self-contained.
func guessGitRemote() string {
	// Cheap parse — no go-git import: read .git/config from cwd, find
	// `[remote "origin"] / url = ...`.
	for _, candidate := range []string{".git/config"} {
		b, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		inOrigin := false
		for _, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			if t == `[remote "origin"]` {
				inOrigin = true
				continue
			}
			if strings.HasPrefix(t, "[") {
				inOrigin = false
				continue
			}
			if inOrigin && strings.HasPrefix(t, "url") {
				if eq := strings.Index(t, "="); eq >= 0 {
					url := strings.TrimSpace(t[eq+1:])
					return normalizeGitURL(url)
				}
			}
		}
	}
	return ""
}

// normalizeGitURL turns ssh-style remotes into https so they're
// usable by the kuso server (which clones via x-access-token).
func normalizeGitURL(u string) string {
	u = strings.TrimSpace(u)
	// ssh form: git@github.com:owner/repo.git
	if strings.HasPrefix(u, "git@") && strings.Contains(u, ":") {
		u = strings.Replace(u, ":", "/", 1)
		u = strings.Replace(u, "git@", "https://", 1)
	}
	u = strings.TrimSuffix(u, ".git")
	return u
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	out := make([]byte, 0, len(s))
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, byte(c))
		} else if c == '-' || c == '_' || c == ' ' || c == '.' {
			out = append(out, '-')
		}
	}
	res := strings.Trim(string(out), "-")
	if res == "" {
		return "my-project"
	}
	return res
}
