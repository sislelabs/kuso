package kusoApi

import "testing"

func TestFeedFilterQuery(t *testing.T) {
	cases := []struct {
		f    FeedFilter
		want string
	}{
		{FeedFilter{}, ""},
		{FeedFilter{Limit: 20, Unread: true}, "?limit=20&unread=true"},
		{FeedFilter{Attention: true, Project: "shop co"}, "?attention=true&project=shop+co"},
	}
	for _, c := range cases {
		if got := c.f.query(); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.f, got, c.want)
		}
	}
}
