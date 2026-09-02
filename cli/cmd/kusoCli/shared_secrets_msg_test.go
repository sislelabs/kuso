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
