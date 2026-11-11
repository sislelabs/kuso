package addons

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// Only addons kuso initialises itself start with the non-superuser app
// role; clones and HA keep the shape their seeding/operator expects.
func TestAdd_AppRoleOnlyOnNewNativeDB(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		req  CreateAddonRequest
		want bool
	}{
		{"postgres", CreateAddonRequest{Name: "pg", Kind: "postgres"}, true},
		{"mysql", CreateAddonRequest{Name: "my", Kind: "mysql"}, true},
		{"postgres ha", CreateAddonRequest{Name: "pg", Kind: "postgres", HA: true}, false},
		{"redis", CreateAddonRequest{Name: "rd", Kind: "redis"}, false},
		{"env clone", CreateAddonRequest{Name: "pg-staging", Kind: "postgres", ExtraLabels: map[string]string{kube.LabelEnv: "staging"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := fakeService(t, seedProj("alpha"))
			got, err := s.Add(context.Background(), "alpha", tc.req)
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			if got.Spec.AppRole != tc.want {
				t.Errorf("appRole = %v, want %v", got.Spec.AppRole, tc.want)
			}
		})
	}
	if newAddonGetsAppRole(CreateAddonRequest{Kind: "postgres"}, true) || newAddonGetsAppRole(CreateAddonRequest{Kind: "postgres", UseInstanceAddon: "shared"}, false) {
		t.Error("external / instance addons must not get appRole")
	}
}

func TestRotatedConnData(t *testing.T) {
	t.Parallel()
	pg := map[string][]byte{
		"POSTGRES_USER":     []byte("kuso"),
		"POSTGRES_PASSWORD": []byte("old"),
		"DATABASE_URL":      []byte("postgres://kuso:old@db:5432/app?sslmode=disable"),
		"DIRECT_URL":        []byte("postgres://kuso:old@db:5432/app?sslmode=disable"),
		"POOLER_URL":        []byte(""),
		"POSTGRES_HOST":     []byte("db"),
	}
	got, err := rotatedConnData("postgres", pg, "n3w")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"POSTGRES_PASSWORD": "n3w",
		"DATABASE_URL":      "postgres://kuso:n3w@db:5432/app?sslmode=disable",
		"DIRECT_URL":        "postgres://kuso:n3w@db:5432/app?sslmode=disable",
		"POOLER_URL":        "",
		"POSTGRES_HOST":     "db",
	} {
		if string(got[k]) != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if string(pg["POSTGRES_PASSWORD"]) != "old" {
		t.Error("input map was mutated")
	}

	my := map[string][]byte{
		"MYSQL_USER": []byte("kuso"), "MYSQL_PASSWORD": []byte("old"),
		"MYSQL_URL":    []byte("mysql://kuso:old@db:3306/app"),
		"DATABASE_URL": []byte("mysql://kuso:old@db:3306/app"),
	}
	if got, err := rotatedConnData("mysql", my, "n3w"); err != nil || string(got["MYSQL_URL"]) != "mysql://kuso:n3w@db:3306/app" || string(got["DATABASE_URL"]) != "mysql://kuso:n3w@db:3306/app" {
		t.Errorf("mysql: %v %q", err, got)
	}

	drift := map[string][]byte{"POSTGRES_USER": []byte("kuso"), "POSTGRES_PASSWORD": []byte("old"), "DATABASE_URL": []byte("postgres://kuso:other@db/app")}
	if _, err := rotatedConnData("postgres", drift, "n3w"); !errors.Is(err, ErrConflict) {
		t.Errorf("drifted URL: err = %v, want ErrConflict", err)
	}
	if _, err := rotatedConnData("redis", pg, "n3w"); !errors.Is(err, ErrInvalid) {
		t.Errorf("redis: err = %v, want ErrInvalid", err)
	}
}

func TestPodSuperuser(t *testing.T) {
	t.Parallel()
	pod := func(user string) *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Env: []corev1.EnvVar{{Name: "POSTGRES_USER", Value: user}}}}}}
	}
	if got := podSuperuser(pod("postgres"), "kuso"); got != "postgres" {
		t.Errorf("appRole pod: %q", got)
	}
	if got := podSuperuser(pod("kuso"), "kuso"); got != "kuso" {
		t.Errorf("legacy pod: %q", got)
	}
}

func TestRotateDBPassword_RefusesUnsupported(t *testing.T) {
	t.Parallel()
	ha := seedAddonCR("alpha-ha", map[string]string{kube.LabelProject: "alpha"}, nil, kube.KusoAddonSpec{Project: "alpha", Kind: "postgres", HA: true})
	bring := seedAddonCR("alpha-byo", map[string]string{kube.LabelProject: "alpha"}, nil, kube.KusoAddonSpec{Project: "alpha", Kind: "postgres", PasswordSecret: &kube.KusoSecretKeyRef{Name: "mine"}})
	s := fakeService(t, seedProj("alpha"), seedAddon("alpha", "cache", "redis"), ha, bring)
	for _, name := range []string{"cache", "ha", "byo"} {
		if _, err := s.RotateDBPassword(context.Background(), "alpha", name); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
