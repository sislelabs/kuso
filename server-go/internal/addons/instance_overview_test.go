package addons

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestBuildInstanceOverview(t *testing.T) {
	addon := func(project, name, instance string) kube.KusoAddon {
		return kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: CRName(project, name), Labels: map[string]string{kube.LabelProject: project}},
			Spec:       kube.KusoAddonSpec{Project: project, UseInstanceAddon: instance},
		}
	}
	env := func(project, svc string, secrets ...string) kube.KusoEnvironment {
		return kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{Project: project, Service: project + "-" + svc, EnvFromSecrets: secrets}}
	}
	out := &InstanceOverview{Name: "pg"}
	buildInstanceOverview(out,
		[]instanceDBRow{
			{Name: "shop_db", SizeBytes: 100, Connections: 2},
			{Name: "blog_db", SizeBytes: 300, Connections: 1},
			{Name: "gone_db", SizeBytes: 50, Comment: instanceDBOwnerPrefix + "gone/db"},
			{Name: "postgres", SizeBytes: 8},
		},
		[]kube.KusoAddon{
			addon("shop", "db", "pg"),
			addon("blog", "db", "pg"),
			addon("new", "db", "pg"),    // database not created yet
			addon("other", "db", "pg2"), // a different instance server
		},
		[]kube.KusoEnvironment{
			env("shop", "web", "shop-db-conn", "shop-web-secrets"),
			env("shop", "worker", "shop-db-conn"),
			env("shop", "web-staging", "shop-db-conn"),
			env("blog", "api", "blog-db-conn"),
		},
		nil,
	)

	if out.SizeBytes != 458 || out.Conns != 3 {
		t.Fatalf("totals = %d bytes / %d conns, want 458 / 3", out.SizeBytes, out.Conns)
	}
	want := []InstanceDatabase{
		{Name: "blog_db", SizeBytes: 300, Connections: 1, ConnectionLimit: 0, Kind: "addon", Project: "blog", Addon: "db", AddonCR: "blog-db", Services: []string{"api"}},
		{Name: "shop_db", SizeBytes: 100, Connections: 2, Kind: "addon", Project: "shop", Addon: "db", AddonCR: "shop-db", Services: []string{"web", "web-staging", "worker"}},
		{Name: "gone_db", SizeBytes: 50, Kind: "orphan", Project: "gone", Addon: "db", Services: []string{}},
		{Name: "postgres", SizeBytes: 8, Kind: "other", Services: []string{}},
	}
	if !reflect.DeepEqual(out.Databases, want) {
		t.Fatalf("databases =\n%+v\nwant\n%+v", out.Databases, want)
	}
	if len(out.Missing) != 1 || out.Missing[0] != (InstanceAddonRef{Project: "new", Addon: "db", Database: "new_db"}) {
		t.Fatalf("missing = %+v, want new/db", out.Missing)
	}
}

// DATA-02 case: the conn Secret (and the app) use <p>_<short> while the
// label derivation gives <p>_<p>_<short>. The live database must map to the
// addon, never be listed as an orphan the page invites the admin to drop.
func TestBuildInstanceOverview_TrustsConnSecretDB(t *testing.T) {
	a := kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "tickero-db", Labels: map[string]string{
			kube.LabelProject: "tickero", addonLabel: "tickero-db",
		}},
		Spec: kube.KusoAddonSpec{Project: "tickero", UseInstanceAddon: "pg"},
	}
	if derived := pgIdentifier("tickero", instanceAddonName(&a, "tickero")); derived != "tickero_tickero_db" {
		t.Fatalf("fixture no longer exercises the mismatch: derived %q", derived)
	}
	envs := []kube.KusoEnvironment{{Spec: kube.KusoEnvironmentSpec{
		Project: "tickero", Service: "tickero-api",
		EnvVars: []kube.KusoEnvVar{{Name: "DATABASE_URL", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "tickero-db-conn", "key": "DATABASE_URL"},
		}}},
	}}}
	out := &InstanceOverview{Name: "pg"}
	buildInstanceOverview(out,
		[]instanceDBRow{{Name: "tickero_db", SizeBytes: 10, Comment: instanceDBOwnerPrefix + "tickero/db"}},
		[]kube.KusoAddon{a}, envs,
		map[string]string{"tickero-db": "tickero_db"},
	)
	got := out.Databases[0]
	if got.Kind != "addon" || got.AddonCR != "tickero-db" {
		t.Fatalf("live database classified %+v, want addon tickero-db", got)
	}
	if !reflect.DeepEqual(got.Services, []string{"api"}) {
		t.Fatalf("secretKeyRef consumer missing: services = %v", got.Services)
	}
	if len(out.Missing) != 0 {
		t.Fatalf("missing = %+v, want none", out.Missing)
	}
}

func TestMarkRetained(t *testing.T) {
	out := &InstanceOverview{Databases: []InstanceDatabase{
		{Name: "shop_db", Kind: "orphan", Project: "shop", Addon: "db"},
		{Name: "gone_db", Kind: "orphan", Project: "gone", Addon: "db"},
		{Name: "blog_db", Kind: "addon", Project: "blog", Addon: "db"},
	}}
	markRetained(out, func(project, addon string) string {
		if project == "shop" && addon == "db" {
			return "shop_db"
		}
		return ""
	})
	kinds := []string{out.Databases[0].Kind, out.Databases[1].Kind, out.Databases[2].Kind}
	if !reflect.DeepEqual(kinds, []string{"retained", "orphan", "addon"}) {
		t.Fatalf("kinds = %v, want [retained orphan addon]", kinds)
	}
}

func TestDeriveInstanceTenantState(t *testing.T) {
	a := &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-db", Labels: map[string]string{kube.LabelProject: "shop"}},
		Spec:       kube.KusoAddonSpec{Project: "shop", UseInstanceAddon: "pg"},
	}
	cases := []struct {
		name      string
		reachable bool
		dbs       map[string]bool
		want      string
	}{
		{"server down", false, nil, addonStateFailing},
		{"database missing", true, map[string]bool{"postgres": true}, addonStateFailing},
		{"healthy", true, map[string]bool{"shop_db": true}, addonStateLive},
	}
	for _, c := range cases {
		got, detail, ok := deriveInstanceTenantState(a, c.reachable, "boom", c.dbs)
		if !ok || got != c.want {
			t.Errorf("%s: state = %q (%s), want %q", c.name, got, detail, c.want)
		}
	}
}
