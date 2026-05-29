package releaserun

import (
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

// TestBuildJob_WaitForAddonsInitContainer is the regression test for Bug B:
// the release Job must include a wait-for-addons initContainer so a
// freshly-provisioned addon that is still bootstrapping (or an addon whose
// Service is briefly re-applied during a reconcile) does not permanently
// fail the release with `connection refused`. backoffLimit stays 0 — we
// never retry a genuinely-failed migration — so the readiness wait must
// live in an initContainer that gates the migration container.
func TestBuildJob_WaitForAddonsInitContainer(t *testing.T) {
	t.Parallel()

	r := New(nil)
	env := &kube.KusoEnvironment{
		Spec: kube.KusoEnvironmentSpec{
			Project:        "alpha",
			Service:        "alpha-api",
			EnvVars:        []kube.KusoEnvVar{{Name: "ENVIRONMENT", Value: "production"}},
			EnvFromSecrets: []string{"alpha-db-conn"},
			Release:        &kube.KusoReleaseSpec{Command: []string{"sh", "-c", "migrate up"}},
		},
	}
	img := &kube.KusoImage{Repository: "registry/alpha/api", Tag: "abc"}

	job := r.buildJob(env, img, "alpha-api-production-release-abc", 600)

	pod := job.Spec.Template.Spec

	// 1. The wait-for-addons initContainer must exist and run first.
	if len(pod.InitContainers) == 0 {
		t.Fatalf("release Job is missing the wait-for-addons initContainer; got none")
	}
	wait := pod.InitContainers[0]
	if wait.Name != "wait-for-addons" {
		t.Fatalf("expected first initContainer to be wait-for-addons, got %q (all: %v)", wait.Name, pod.InitContainers)
	}

	// 2. It must run a shell script that TCP-waits on the addon URLs.
	if len(wait.Command) < 3 || wait.Command[0] != "sh" || wait.Command[1] != "-c" {
		t.Fatalf("wait-for-addons command should be [sh -c <script>], got %v", wait.Command)
	}
	script := wait.Command[2]
	for _, want := range []string{"DATABASE_URL", "REDIS_URL", "NATS_URL", "nc -z"} {
		if !strings.Contains(script, want) {
			t.Errorf("wait script should reference %q; script:\n%s", want, script)
		}
	}

	// 3. It must see the same env (so $DATABASE_URL resolves) — both the
	//    inline EnvVars and the EnvFrom secret(s).
	if len(wait.EnvFrom) == 0 {
		t.Error("wait-for-addons initContainer should inherit envFrom (addon conn secrets)")
	}
	sawEnvVar := false
	for _, e := range wait.Env {
		if e.Name == "ENVIRONMENT" {
			sawEnvVar = true
		}
	}
	if !sawEnvVar {
		t.Error("wait-for-addons initContainer should inherit the env's inline envVars")
	}

	// 4. The migration container itself must still be present and unchanged
	//    in its one-shot semantics.
	if len(pod.Containers) != 1 || pod.Containers[0].Name != "release" {
		t.Fatalf("expected a single 'release' container, got %v", pod.Containers)
	}
	// backoffLimit MUST remain 0 — the readiness wait handles transient
	// not-ready; a genuine migration failure must not be retried.
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Errorf("release Job backoffLimit must stay 0 (never retry a failed migration), got %v", job.Spec.BackoffLimit)
	}
}
