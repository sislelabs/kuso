package scaledown

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"kuso/server/internal/kube"
)

func refVars(pairs ...string) []any {
	var out []any
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]any{"name": pairs[i], "value": pairs[i+1]})
	}
	return out
}

func TestReferencedEnvs(t *testing.T) {
	t.Parallel()
	envs := map[string]bool{"alpha-api-staging": true, "alpha-auth-staging": true, "alpha-jobs-staging": true}
	cases := []struct {
		name  string
		vars  []kube.KusoEnvVar
		isEnv func(string) bool
	}{
		{
			name: "FQDN",
			vars: []kube.KusoEnvVar{
				{Name: "API_URL", Value: "http://alpha-api-staging.kuso.svc.cluster.local"},
				{Name: "AUTH_HOST", Value: "alpha-auth-staging.kuso.svc"},
				{Name: "AGAIN", Value: "http://alpha-api-staging.kuso.svc.cluster.local/v2"},
				{Name: "SELF", Value: "http://alpha-web-staging.kuso.svc.cluster.local"},
				{Name: "PUBLIC", Value: "https://api.example.com"},
				{Name: "DB", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "x-conn", "key": "URL"}}},
			},
		},
		{
			// RUN-9: same-namespace short Service names are the idiomatic in-namespace
			// form and must count as references, but only when they name a real env.
			name: "ShortNames",
			vars: []kube.KusoEnvVar{
				{Name: "API_URL", Value: "http://alpha-api-staging/v1"},
				{Name: "AUTH_ADDR", Value: "alpha-auth-staging:8080"},
				{Name: "CACHE", Value: "redis://not-an-env:6379"},
				{Name: "MODE", Value: "alpha-jobs-staging"}, // bare word: not a URL
				{Name: "PUBLIC", Value: "https://alpha-api-staging.example.com"},
			},
			isEnv: func(n string) bool { return envs[n] },
		},
	}
	want := []string{"kuso/alpha-api-staging", "kuso/alpha-auth-staging"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &kube.KusoEnvironment{}
			e.Name, e.Namespace = "alpha-web-staging", "kuso"
			e.Spec.EnvVars = tc.vars
			if got := referencedEnvs(e, tc.isEnv); !slices.Equal(got, want) {
				t.Fatalf("referencedEnvs = %v, want %v", got, want)
			}
		})
	}
}

// RUN-2: web calls api in-cluster through ${{ api.URL }}. Those calls
// bypass traefik and the activator, so api looks idle. It must stay up
// while web is awake and in use, and sleep together with web once both
// are idle.
func TestEvaluate_InClusterCallerKeepsTargetAwake(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	idle := map[string]string{LastActivityAnnotation: now.Add(-2 * time.Hour).Format(time.RFC3339)}
	busy := map[string]string{LastActivityAnnotation: now.Add(-time.Minute).Format(time.RFC3339)}
	staging := map[string]string{kube.LabelEnv: "staging"}
	qa := map[string]string{kube.LabelEnv: "qa"}

	f := &fixture{}
	f.service("alpha-web", nil, map[string]any{})
	f.service("alpha-api", nil, map[string]any{})
	f.service("alpha-auth", nil, map[string]any{})
	// staging: web is in use and calls api, which calls auth.
	f.env("alpha-web-staging", "alpha-web", staging, busy, map[string]any{
		"kind": "custom", "autoSleep": true,
		"envVars": refVars("API_URL", "http://alpha-api-staging.kuso.svc.cluster.local"),
	}, 1, "kuso-activator")
	f.env("alpha-api-staging", "alpha-api", staging, idle, map[string]any{
		"kind": "custom", "autoSleep": true,
		"envVars": refVars("AUTH_URL", "http://alpha-auth-staging.kuso.svc.cluster.local"),
	}, 1, "kuso-activator")
	f.env("alpha-auth-staging", "alpha-auth", staging, idle, map[string]any{
		"kind": "custom", "autoSleep": true,
	}, 1, "kuso-activator")
	// qa: web and api call each other and are both idle.
	f.env("alpha-web-qa", "alpha-web", qa, idle, map[string]any{
		"kind": "custom", "autoSleep": true,
		"envVars": refVars("API_URL", "http://alpha-api-qa.kuso.svc.cluster.local"),
	}, 1, "kuso-activator")
	f.env("alpha-api-qa", "alpha-api", qa, idle, map[string]any{
		"kind": "custom", "autoSleep": true,
		"envVars": refVars("WEB_URL", "http://alpha-web-qa.kuso.svc.cluster.local"),
	}, 1, "kuso-activator")

	kc := f.run(t, now)
	for env, want := range map[string]int32{
		"alpha-web-staging":  1,
		"alpha-api-staging":  1, // called by an awake, busy web
		"alpha-auth-staging": 1, // called by api, which stays up
		"alpha-web-qa":       0,
		"alpha-api-qa":       0, // mutual callers, both idle: sleep together
	} {
		if got := replicas(t, kc, env); got != want {
			t.Errorf("%s: replicas = %d, want %d", env, got, want)
		}
	}
}

// An env slept by an older kuso while an awake env still calls it is
// refusing those calls now. Wake it, a few per tick so an upgrade doesn't
// wake every such env at once.
func TestEvaluate_WakesSleptTargetOfAwakeCaller(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-2 * time.Hour).Format(time.RFC3339)
	busy := map[string]string{LastActivityAnnotation: now.Add(-time.Minute).Format(time.RFC3339)}
	slept := map[string]string{LastActivityAnnotation: stale, PreSleepReplicasAnnotation: "2"}
	staging := map[string]string{kube.LabelEnv: "staging"}

	f := &fixture{}
	f.service("alpha-web", nil, map[string]any{})
	targets := []string{"alpha-a-staging", "alpha-b-staging", "alpha-c-staging", "alpha-d-staging"}
	var vars []string
	for _, tgt := range targets {
		vars = append(vars, "URL_"+tgt, "http://"+tgt+".kuso.svc.cluster.local")
	}
	f.env("alpha-web-staging", "alpha-web", staging, busy, map[string]any{
		"kind": "custom", "autoSleep": true, "envVars": refVars(vars...),
	}, 1, "kuso-activator")
	for _, tgt := range targets {
		svc := tgt[:len(tgt)-len("-staging")]
		f.service(svc, nil, map[string]any{})
		f.env(tgt, svc, staging, slept, map[string]any{
			"kind": "custom", "autoSleep": true, "replicaCount": int64(0),
		}, 0, "kuso-activator")
	}
	// Slept and called, but nothing awake calls it: stays asleep.
	f.service("alpha-lone", nil, map[string]any{})
	f.env("alpha-lone-staging", "alpha-lone", staging, slept, map[string]any{
		"kind": "custom", "autoSleep": true, "replicaCount": int64(0),
	}, 0, "kuso-activator")

	kc := f.run(t, now)
	woken := 0
	for _, tgt := range targets {
		switch replicas(t, kc, tgt) {
		case 2:
			woken++
		case 0:
		default:
			t.Errorf("%s: woken to the wrong count", tgt)
		}
	}
	if woken != maxDependencyWakesPerTick {
		t.Errorf("woke %d slept targets in one tick, want %d", woken, maxDependencyWakesPerTick)
	}
	if got := replicas(t, kc, "alpha-lone-staging"); got != 0 {
		t.Errorf("alpha-lone-staging woken with no awake caller: replicas = %d", got)
	}
}

// RUN-8: a request stamped after the idle check must abort the sleep
// instead of being zeroed out from under.
func TestScaleToZero_AbortsWhenStampedAfterIdleCheck(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	f := &fixture{}
	f.service("alpha-web", nil, map[string]any{})
	f.env("alpha-web-staging", "alpha-web", map[string]string{kube.LabelEnv: "staging"},
		map[string]string{LastActivityAnnotation: now.Add(-5 * time.Second).Format(time.RFC3339)},
		map[string]any{"kind": "custom", "autoSleep": true}, 1, "kuso-activator")
	w := f.watcher(t, now)

	err := w.scaleToZero(context.Background(), testNS, "alpha-web-staging", 30)
	if !errors.Is(err, errRecentlyActive) {
		t.Fatalf("scaleToZero err = %v, want errRecentlyActive", err)
	}
	if got := replicas(t, w.Kube, "alpha-web-staging"); got != 1 {
		t.Errorf("replicas = %d, want 1", got)
	}
	if e := getEnv(t, w.Kube, "alpha-web-staging"); e.Annotations[PreSleepReplicasAnnotation] != "" {
		t.Error("pre-sleep annotation written for an env that wasn't slept")
	}
}

// Waking web through the activator wakes the slept envs it calls,
// transitively, but never a hard-stopped or awake one.
func TestWakeDependencies(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	slept := map[string]string{PreSleepReplicasAnnotation: "2"}
	f := &fixture{}
	f.env("alpha-web-staging", "alpha-web", nil, nil, map[string]any{
		"envVars": refVars(
			"API", "http://alpha-api-staging.kuso.svc.cluster.local",
			"STOPPED", "http://alpha-stopped-staging.kuso.svc.cluster.local",
			"AWAKE", "http://alpha-awake-staging.kuso.svc.cluster.local",
		),
	}, 1, "")
	f.env("alpha-api-staging", "alpha-api", nil, slept, map[string]any{
		"replicaCount": int64(0),
		"envVars":      refVars("AUTH", "alpha-auth-staging.kuso.svc.cluster.local"),
	}, 0, "")
	f.env("alpha-auth-staging", "alpha-auth", nil, slept, map[string]any{"replicaCount": int64(0)}, 0, "")
	f.env("alpha-stopped-staging", "alpha-stopped", nil, slept, map[string]any{"stopped": true, "replicaCount": int64(0)}, 0, "")
	f.env("alpha-awake-staging", "alpha-awake", nil, nil, map[string]any{}, 3, "")
	w := f.watcher(t, now)

	WakeDependencies(context.Background(), w.Kube, slog.Default(), testNS, "alpha-web-staging", now)
	for env, want := range map[string]int32{
		"alpha-api-staging":     2,
		"alpha-auth-staging":    2,
		"alpha-stopped-staging": 0,
		"alpha-awake-staging":   3,
	} {
		if got := replicas(t, w.Kube, env); got != want {
			t.Errorf("%s: replicas = %d, want %d", env, got, want)
		}
	}
}
