package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// seedEnvSubscribed is seedEnv plus an explicit subscribedAddons list.
// A non-nil (even empty) list means "this env opted in to exactly these
// addons"; nil means legacy mount-all.
func seedEnvSubscribed(project, service, kind, name string, subscribed []string) seed {
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     kind,
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:          project,
			Service:          project + "-" + service,
			Kind:             kind,
			SubscribedAddons: subscribed,
		},
	})
}

// TestAdd_DoesNotLeakNewAddonIntoUnsubscribedEnvs is the regression for
// the tickero incident of 2026-07-25.
//
// Adding `storage-staging` to the tickero project injected
// `tickero-storage-staging-conn` into all FOUR production envs, none of
// which subscribed to it. Because an s3 conn secret exports the same key
// names as every other s3 conn (S3_BUCKET, S3_ENDPOINT, AWS_*) and kube
// envFrom is last-source-wins on duplicate keys, production pods
// resolved to STAGING's bucket and tickero-api-production crashlooped.
//
// The cause is a gap between the two roles extraConnSecrets plays.
// refreshEnvSecretsFiltered adds it to baseSecrets (so the new addon
// lands even though the informer cache hasn't caught up — the
// read-after-write hand-off), but projectAddonConns was built ONLY from
// the cached addon list. filterAddonConnsBySubscription passes through
// any secret it doesn't recognise as a project addon conn, on the theory
// that it must be a per-service or user-named secret. So the brand-new
// conn — present in baseSecrets, absent from projectAddonConns — took
// the "not an addon, leave it alone" branch and was mounted everywhere.
//
// The subscription VIEW was right the whole time: `kuso project addon
// list` showed storage-staging as unsubscribed while the pod had it
// mounted. That divergence is the tell.
func TestAdd_DoesNotLeakNewAddonIntoUnsubscribedEnvs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("tickero"),
		// Mirrors production: api subscribes to the real addons,
		// frontend subscribes to NOTHING (explicit empty list).
		seedEnvSubscribed("tickero", "api", "production", "tickero-api-production", []string{"db"}),
		seedEnvSubscribed("tickero", "frontend", "production", "tickero-frontend-production", []string{}),
	)

	// The pre-existing addon everyone is already wired to.
	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{Name: "db", Kind: "postgres"}); err != nil {
		t.Fatalf("Add db: %v", err)
	}

	// Now simulate adding `storage-staging` UNDER CACHE LAG, which is the
	// condition that actually bit us. Calling refreshEnvSecretsFiltered
	// directly with the new conn as an "extra" — and without seeding the
	// addon CR — reproduces the real state exactly: present in
	// extraConnSecrets, absent from the watch-cache-served addon List.
	//
	// Going through s.Add() here would NOT reproduce it: the fake dynamic
	// client's List is immediately consistent, so the new addon is always
	// visible and the buggy branch is never taken. That is precisely why
	// this class of bug survived — an end-to-end test against the fake
	// client is green either way.
	if err := s.refreshEnvSecretsFiltered(context.Background(), "tickero",
		[]string{"tickero-storage-staging-conn"}, nil); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	for _, tc := range []struct {
		env  string
		want []string // conns that MUST be present
		deny []string // conns that must NOT be present
	}{
		{
			env:  "tickero-api-production",
			want: []string{"tickero-db-conn"},
			deny: []string{"tickero-storage-staging-conn"},
		},
		{
			env:  "tickero-frontend-production",
			want: nil,
			deny: []string{"tickero-db-conn", "tickero-storage-staging-conn"},
		},
	} {
		envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", tc.env)
		if err != nil {
			t.Fatalf("get %s: %v", tc.env, err)
		}
		got := envCR.Spec.EnvFromSecrets
		for _, w := range tc.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: expected %q in envFromSecrets, got %v", tc.env, w, got)
			}
		}
		for _, d := range tc.deny {
			if slices.Contains(got, d) {
				t.Errorf("%s: %q leaked into envFromSecrets of an env that does not subscribe to it (got %v)",
					tc.env, d, got)
			}
		}
	}
}

// A nil SubscribedAddons is the documented legacy "mount everything"
// behaviour and must stay that way — projects created before the
// subscription model rely on it. This pins the nil/empty distinction so
// a fix for the leak above can't accidentally collapse the two.
func TestAdd_NilSubscriptionStillMountsAll(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("legacy"),
		seedEnv("legacy", "web", "production", "legacy-web-production"), // nil SubscribedAddons
	)
	if _, err := s.Add(context.Background(), "legacy", CreateAddonRequest{Name: "pg", Kind: "postgres"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "legacy-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if !slices.Contains(envCR.Spec.EnvFromSecrets, "legacy-pg-conn") {
		t.Errorf("nil subscription must mount all addon conns, got %v", envCR.Spec.EnvFromSecrets)
	}
}
