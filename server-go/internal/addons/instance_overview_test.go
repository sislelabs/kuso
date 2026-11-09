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
	)

	if out.SizeBytes != 458 || out.Conns != 3 {
		t.Fatalf("totals = %d bytes / %d conns, want 458 / 3", out.SizeBytes, out.Conns)
	}
	want := []InstanceDatabase{
		{Name: "blog_db", SizeBytes: 300, Connections: 1, Kind: "addon", Project: "blog", Addon: "db", AddonCR: "blog-db", Services: []string{"api"}},
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
