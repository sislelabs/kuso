package kusoCli

import "testing"

func TestServiceAddedSummary(t *testing.T) {
	cases := []struct {
		name string
		host string
		fb   *serviceFirstBuild
		want string
	}{
		{"no build block", "", nil, "service shop/web added\n"},
		{"started", "web.shop.example.com", &serviceFirstBuild{Triggered: true},
			"service shop/web added\nhost: web.shop.example.com\n" +
				"first build started. Find its id with: kuso build list shop web\n" +
				"then follow it with: kuso logs shop web --build <id>\n"},
		{"failed", "", &serviceFirstBuild{Error: "no github installation"},
			"service shop/web added\nfirst build did not start: no github installation\n" +
				"retry with: kuso build trigger shop web\n"},
	}
	for _, tc := range cases {
		if got := serviceAddedSummary("shop", "web", tc.host, tc.fb); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
