package kusoCli

import "testing"

func TestUnsetMsg_ReportsUnsubscribedServices(t *testing.T) {
	cases := []struct {
		rolled, unsubscribed int
		want                 string
	}{
		{0, 0, "nothing was using it"},
		{0, 3, "removed from 3 services (restarted)"},
		{0, 1, "removed from 1 service (restarted)"},
		{2, 0, "rolled 2 envs"},
		{1, 2, "removed from 2 services (restarted), rolled 1 env"},
	}
	for _, c := range cases {
		if got := unsetMsg(c.rolled, c.unsubscribed); got != c.want {
			t.Errorf("unsetMsg(%d, %d) = %q, want %q", c.rolled, c.unsubscribed, got, c.want)
		}
	}
}

// F2: with sharedEnvKeys=[] no service inherits a new key. The old message
// ("no running envs to roll") read as success; it must say nobody gets the
// value and how to subscribe.
func TestSetMsg_ReportsSubscribers(t *testing.T) {
	cases := []struct {
		name     string
		rolled   int
		resynced int
		subs     []string
		want     string
	}{
		{"none", 0, 0, []string{}, "set TOKEN on e2e — no service subscribes to TOKEN, so no pod receives it; subscribe one with: kuso env share e2e <service> TOKEN"},
		{"one", 1, 0, []string{"api"}, "set TOKEN on e2e — 1 subscribing service (api), rolled 1 env"},
		{"two", 3, 0, []string{"api", "worker"}, "set TOKEN on e2e — 2 subscribing services (api, worker), rolled 3 envs"},
		{"old server", 2, 0, nil, "set TOKEN on e2e — rolled 2 envs"},
		// Live: a by-name subscriber was re-propagated (and its pod
		// restarted) while no env mounted the whole Secret.
		{"resynced only", 0, 1, []string{"api"}, "set TOKEN on e2e — 1 subscribing service (api), restarted 1 subscribing service"},
	}
	for _, c := range cases {
		if got := setMsg("e2e", "TOKEN", c.rolled, c.resynced, c.subs); got != c.want {
			t.Errorf("%s: setMsg = %q, want %q", c.name, got, c.want)
		}
	}
}
