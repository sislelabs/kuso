package projects

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// nsTestService wires a Service with both a dynamic client (seeded with
// KusoProjects) and a typed clientset (seeded with Namespaces), so Create
// can see which namespaces already exist and who owns them.
func nsTestService(t *testing.T, namespaces []*corev1.Namespace, projects ...seed) (*Service, *k8sfake.Clientset) {
	t.Helper()
	objs := make([]runtime.Object, 0, len(namespaces))
	for _, n := range namespaces {
		objs = append(objs, n)
	}
	cs := k8sfake.NewSimpleClientset(objs...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRKuso: "KusoList", kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList", kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons: "KusoAddonList", kube.GVRBuilds: "KusoBuildList",
	})
	for _, sd := range projects {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}
	return New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso"), cs
}

func ns(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

var kusoManaged = map[string]string{kube.ManagedByLabel: kube.ManagedByValue}

// H2 (2026-09-23 review): an instance editor could point a new project at
// kube-system, the home namespace or another project's namespace, and kuso
// would adopt it (PSS label, exec/secret RBAC, backup S3 creds).
func TestCreate_RejectsUnsafeNamespace(t *testing.T) {
	t.Parallel()
	existing := []*corev1.Namespace{
		ns("kube-system", nil),
		ns("default", nil),
		ns("kuso", kusoManaged),
		ns("kuso-operator-system", nil),
		ns("cert-manager", nil),
		ns("traefik", nil),
		ns("kuso-koreni", kusoManaged),
		ns("payments", nil),
	}
	koreni := seedProject("koreni", kube.KusoProjectSpec{Namespace: "kuso-koreni"})

	cases := []struct {
		name, project, namespace string
		want                     error
	}{
		{"not dns-1123 (uppercase)", "evil", "Bad_NS", ErrInvalid},
		{"not dns-1123 (dot)", "evil", "a.b", ErrInvalid},
		{"not dns-1123 (too long)", "evil", strings.Repeat("a", 64), ErrInvalid},
		{"kube-system", "evil", "kube-system", ErrInvalid},
		{"kube-public (not yet present)", "evil", "kube-public", ErrInvalid},
		{"default", "evil", "default", ErrInvalid},
		{"home namespace", "evil", "kuso", ErrInvalid},
		{"operator namespace", "evil", "kuso-operator-system", ErrInvalid},
		{"cert-manager", "evil", "cert-manager", ErrInvalid},
		{"traefik", "evil", "traefik", ErrInvalid},
		{"any *-system", "evil", "longhorn-system", ErrInvalid},
		{"derived name hits reserved", "operator-system", "", ErrInvalid},
		{"another project's namespace", "evil", "kuso-koreni", ErrConflict},
		{"pre-existing namespace kuso doesn't manage", "evil", "payments", ErrConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, cs := nsTestService(t, existing, koreni)
			_, err := s.Create(context.Background(), CreateProjectRequest{Name: tc.project, Namespace: tc.namespace})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Create(%q, ns=%q) err = %v, want %v", tc.project, tc.namespace, err, tc.want)
			}
			// Rejection must happen before adoption: no RoleBinding stamped.
			target := tc.namespace
			if target == "" {
				target = derivedProjectNamespace(tc.project)
			}
			rbs, _ := cs.RbacV1().RoleBindings(target).List(context.Background(), metav1.ListOptions{})
			if len(rbs.Items) != 0 {
				t.Fatalf("namespace %q was adopted despite rejection: %d rolebindings", target, len(rbs.Items))
			}
		})
	}
}

func TestCreate_AllowsSafeNamespace(t *testing.T) {
	t.Parallel()
	existing := []*corev1.Namespace{
		// Left behind by a deleted project of the same name: kuso-managed,
		// unclaimed, so recreating the project reuses it.
		ns("kuso-shop", kusoManaged),
		ns("kuso-koreni", kusoManaged),
	}
	koreni := seedProject("koreni", kube.KusoProjectSpec{Namespace: "kuso-koreni"})
	cases := []struct{ name, project, namespace string }{
		{"derived, not yet present", "blog", ""},
		{"derived, kuso-managed leftover", "shop", ""},
		{"explicit, not yet present", "api", "team-api"},
		{"explicit kuso-managed leftover", "shop2", "kuso-shop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := nsTestService(t, existing, koreni)
			if _, err := s.Create(context.Background(), CreateProjectRequest{Name: tc.project, Namespace: tc.namespace}); err != nil {
				t.Fatalf("Create(%q, ns=%q): %v", tc.project, tc.namespace, err)
			}
		})
	}
}
