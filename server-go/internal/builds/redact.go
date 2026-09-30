package builds

import (
	"context"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// minRedactLen is the shortest secret value scrubbed from log excerpts.
// Shorter values ("true", "5432", "prod") match ordinary log text far
// more often than they leak anything.
const minRedactLen = 6

const redactedMarker = "[redacted]"

// secretValuesForBuild returns the values of every Secret the build's
// service can read — its managed secret, the shared secrets, each env's
// envFromSecrets (addon conns included) and secretKeyRefs, and the
// build's own secret-ref build env — so log excerpts can be scrubbed
// before they are archived or sent to notification channels. A value
// spanning several lines also contributes each line, since logs split
// it. Best-effort: an unreadable Secret is skipped.
func (s *Service) secretValuesForBuild(ctx context.Context, ns string, b *kube.KusoBuild) []string {
	if s == nil || s.Kube == nil || s.Kube.Clientset == nil || b == nil {
		return nil
	}
	project := b.Spec.Project
	short := strings.TrimPrefix(b.Spec.Service, project+"-")
	names := map[string]bool{kube.ServiceSecretName(project, short): true}
	for _, n := range kube.SharedSecretNames(project) {
		names[n] = true
	}
	for _, v := range b.Spec.BuildEnv {
		if sec, _, ok := ParseBuildEnvSecretRef(v); ok {
			names[sec] = true
		}
	}
	if envs, err := s.serviceEnvs(ctx, ns, project, short); err == nil {
		for i := range envs {
			for _, n := range envs[i].Spec.EnvFromSecrets {
				names[n] = true
			}
			for _, ev := range envs[i].Spec.EnvVars {
				if n := secretKeyRefName(ev.ValueFrom); n != "" {
					names[n] = true
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if len(v) >= minRedactLen && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for n := range names {
		sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, n, metav1.GetOptions{})
		if err != nil {
			continue
		}
		for _, raw := range sec.Data {
			v := string(raw)
			add(v)
			if strings.Contains(v, "\n") {
				for _, line := range strings.Split(v, "\n") {
					add(line)
				}
			}
		}
	}
	return out
}

func secretKeyRefName(valueFrom map[string]any) string {
	ref, _ := valueFrom["secretKeyRef"].(map[string]any)
	name, _ := ref["name"].(string)
	return name
}

// redactSecrets replaces every occurrence of a known secret value in s.
// Longest values first, so a secret that contains another is replaced
// whole rather than leaving its remainder behind.
func redactSecrets(s string, values []string) string {
	if s == "" || len(values) == 0 {
		return s
	}
	sorted := append([]string(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, v := range sorted {
		if len(v) >= minRedactLen {
			s = strings.ReplaceAll(s, v, redactedMarker)
		}
	}
	return s
}

func redactLines(lines []string, values []string) []string {
	if len(values) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = redactSecrets(l, values)
	}
	return out
}
