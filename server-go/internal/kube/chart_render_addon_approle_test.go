package kube

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type renderedObj struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name string `yaml:"name"`
					Env  []struct {
						Name      string `yaml:"name"`
						Value     string `yaml:"value"`
						ValueFrom struct {
							SecretKeyRef struct {
								Name string `yaml:"name"`
								Key  string `yaml:"key"`
							} `yaml:"secretKeyRef"`
						} `yaml:"valueFrom"`
					} `yaml:"env"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func renderedObjs(t *testing.T, out string) map[string]renderedObj {
	t.Helper()
	objs := map[string]renderedObj{}
	for _, doc := range strings.Split(out, "\n---\n") {
		var o renderedObj
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("decode: %v\n%s", err, doc)
		}
		if o.Kind != "" {
			objs[o.Kind+"/"+o.Metadata.Name] = o
		}
	}
	return objs
}

// envOf returns "value" or "secret/key" for a container env var.
func envOf(o renderedObj, container, name string) string {
	for _, c := range o.Spec.Template.Spec.Containers {
		if c.Name != container {
			continue
		}
		for _, e := range c.Env {
			if e.Name == name {
				if e.ValueFrom.SecretKeyRef.Name != "" {
					return e.ValueFrom.SecretKeyRef.Name + "/" + e.ValueFrom.SecretKeyRef.Key
				}
				return e.Value
			}
		}
	}
	return ""
}

// DB-10: an appRole addon boots its superuser from <name>-admin and hands
// apps a plain role; without the flag the render is the legacy one (the
// conn user is the superuser and no admin Secret exists).
func TestAddonChart_AppRoleSeparatesSuperuser(t *testing.T) {
	for _, tc := range []struct {
		kind, container, rootEnv string
		appRole                  bool
		wantRoot                 string
	}{
		{"postgres", "postgres", "POSTGRES_PASSWORD", false, "test-addon-conn/POSTGRES_PASSWORD"},
		{"postgres", "postgres", "POSTGRES_PASSWORD", true, "test-addon-admin/ADMIN_PASSWORD"},
		{"mysql", "mysql", "MYSQL_ROOT_PASSWORD", false, "test-addon-conn/MYSQL_PASSWORD"},
		{"mysql", "mysql", "MYSQL_ROOT_PASSWORD", true, "test-addon-admin/ADMIN_PASSWORD"},
	} {
		sets := []string{}
		if tc.appRole {
			sets = append(sets, "appRole=true")
		}
		objs := renderedObjs(t, helmTemplateAddon(t, tc.kind, sets...))
		sts, ok := objs["StatefulSet/test-addon"]
		if !ok {
			t.Fatalf("%s: no StatefulSet", tc.kind)
		}
		if got := envOf(sts, tc.container, tc.rootEnv); got != tc.wantRoot {
			t.Errorf("%s appRole=%v: %s from %q, want %q", tc.kind, tc.appRole, tc.rootEnv, got, tc.wantRoot)
		}
		admin, hasAdmin := objs["Secret/test-addon-admin"]
		if hasAdmin != tc.appRole {
			t.Errorf("%s appRole=%v: admin Secret rendered = %v", tc.kind, tc.appRole, hasAdmin)
		}
		if tc.appRole && admin.Data["ADMIN_PASSWORD"] == "" {
			t.Errorf("%s: admin Secret has no password", tc.kind)
		}
		if tc.kind != "postgres" {
			continue
		}
		wantUser := map[bool]string{false: "kuso", true: "postgres"}[tc.appRole]
		if got := envOf(sts, "postgres", "POSTGRES_USER"); got != wantUser {
			t.Errorf("postgres appRole=%v: POSTGRES_USER = %q, want %q", tc.appRole, got, wantUser)
		}
		cm, hasInit := objs["ConfigMap/test-addon-initdb"]
		if hasInit != tc.appRole {
			t.Errorf("postgres appRole=%v: initdb ConfigMap rendered = %v", tc.appRole, hasInit)
		}
		if tc.appRole {
			script := cm.Data["10-kuso-app-role.sh"]
			for _, want := range []string{"CREATE ROLE kuso LOGIN NOSUPERUSER", "OWNER TO kuso", ":'app_pw'"} {
				if !strings.Contains(script, want) {
					t.Errorf("init script lacks %q:\n%s", want, script)
				}
			}
			if got := envOf(sts, "postgres", "KUSO_APP_PASSWORD"); got != "test-addon-conn/POSTGRES_PASSWORD" {
				t.Errorf("KUSO_APP_PASSWORD from %q", got)
			}
		}
	}
}
