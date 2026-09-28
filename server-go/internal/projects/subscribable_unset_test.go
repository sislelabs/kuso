package projects

import (
	"context"
	"reflect"
	"testing"

	"kuso/server/internal/kube"
)

// A service whose subscribedAddons is unset mounts every addon, so the
// list endpoint must report every addon as subscribed. It reported [],
// and `kuso project addon unsubscribe <p> <s> one` (current minus one)
// then unsubscribed the service from ALL addons (live, e2e3).
func TestListSubscribableAddons_UnsetMeansAll(t *testing.T) {
	s := fakeService(t,
		seedProject("p", kube.KusoProjectSpec{}),
		seedService("p", "api", kube.KusoServiceSpec{}),
		seedAddon("p", "db", "postgres"),
		seedAddon("p", "cache", "redis"),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"p-cache-conn", "p-db-conn"}, nil
	}
	got, err := s.ListSubscribableAddons(context.Background(), "p", "api")
	if err != nil {
		t.Fatalf("ListSubscribableAddons: %v", err)
	}
	if !reflect.DeepEqual(got.Subscribed, got.Available) || len(got.Available) != 2 {
		t.Errorf("unset subscription reported %v of %v; want every available addon", got.Subscribed, got.Available)
	}
}
