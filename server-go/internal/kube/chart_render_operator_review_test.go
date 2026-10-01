package kube

import (
	"strings"
	"testing"
)

// Chart regressions from the 2026-10-01 operator review. These pin chart
// behaviour the server relies on; helm must be on PATH or they skip.

// Two pods surging onto one ReadWriteOnce disk risks SQLite corruption or
// a rollout that hangs on a lock, so RWO volumes deploy with Recreate.
func TestKusoEnvironmentChart_RWOVolumeUsesRecreate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sets []string
		want string
		not  string
	}{
		{"no volume", nil, "type: RollingUpdate", "type: Recreate"},
		{"rwo default", []string{"volumes[0].name=data", "volumes[0].mountPath=/data"}, "type: Recreate", "type: RollingUpdate"},
		{"rwo explicit", []string{"volumes[0].name=data", "volumes[0].mountPath=/data", "volumes[0].accessMode=RWO"}, "type: Recreate", "type: RollingUpdate"},
		{"rwx", []string{"volumes[0].name=data", "volumes[0].mountPath=/data", "volumes[0].accessMode=RWX"}, "type: RollingUpdate", "type: Recreate"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := helmTemplateEnvNS(t, "kuso", tc.sets...)
			if !strings.Contains(out, tc.want) || strings.Contains(out, tc.not) {
				t.Fatalf("want %q and no %q in Deployment strategy:\n%s", tc.want, tc.not, out)
			}
		})
	}
}

// Cron pods used to get envFromSecrets only, so env-level aliases such as
// DATABASE_URL: ${{ db.X }} never reached them. spec.env must render on
// the job container and on the wait-for-addons init.
func TestKusoCronChart_RendersEnv(t *testing.T) {
	t.Parallel()
	out := helmTemplateCron(t,
		"image.repository=registry/app", "image.tag=v1",
		"env[0].name=NODE_ENV", "env[0].value=production",
		"env[1].name=DATABASE_URL",
		"env[1].valueFrom.secretKeyRef.name=alpha-db-conn",
		"env[1].valueFrom.secretKeyRef.key=DATABASE_URL",
	)
	if n := strings.Count(out, `- name: "NODE_ENV"`); n != 2 {
		t.Errorf("NODE_ENV rendered %d times, want 2 (container + init):\n%s", n, out)
	}
	if n := strings.Count(out, "name: alpha-db-conn"); n < 2 {
		t.Errorf("secretKeyRef alias rendered %d times, want 2:\n%s", n, out)
	}
}

func TestKusoAddonChart_PublicTCPTargets(t *testing.T) {
	t.Parallel()
	pub := []string{"publicTCP.enabled=true", "publicTCP.port=30001"}
	t.Run("ha postgres targets -rw", func(t *testing.T) {
		t.Parallel()
		out := helmTemplateAddon(t, "postgres", append([]string{"ha=true"}, pub...)...)
		if !strings.Contains(out, "kind: IngressRouteTCP") || !strings.Contains(routeSection(out), "name: test-addon-rw") {
			t.Fatalf("HA postgres public TCP must route to test-addon-rw (release fullname + -rw):\n%s", routeSection(out))
		}
	})
	for _, kind := range []string{"mailpit", "memcached"} {
		kind := kind
		t.Run("unsupported "+kind+" renders no route", func(t *testing.T) {
			t.Parallel()
			out := helmTemplateAddon(t, kind, pub...)
			if strings.Contains(out, "kind: IngressRouteTCP") {
				t.Fatalf("%s must not render an IngressRouteTCP:\n%s", kind, routeSection(out))
			}
		})
	}
	t.Run("valkey routes 6379", func(t *testing.T) {
		t.Parallel()
		out := helmTemplateAddon(t, "valkey", pub...)
		if !strings.Contains(routeSection(out), "port: 6379") {
			t.Fatalf("valkey public TCP must route to 6379:\n%s", routeSection(out))
		}
	})
}

func routeSection(out string) string {
	if i := strings.Index(out, "kind: IngressRouteTCP"); i >= 0 {
		return out[i:]
	}
	return "(no IngressRouteTCP)"
}
