package incidents

import "testing"

func TestParseGitHubURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, owner, repo string
		wantErr         bool
	}{
		{"https://github.com/biznesguys/analiz", "biznesguys", "analiz", false},
		{"https://github.com/biznesguys/analiz.git", "biznesguys", "analiz", false},
		{"git@github.com:sislelabs/kuso.git", "sislelabs", "kuso", false},
		{"https://github.com/o/r/tree/main", "o", "r", false}, // extra segments ignored
		{"  https://github.com/a/b  ", "a", "b", false},       // trimmed
		{"https://github.com/onlyowner", "", "", true},
		{"not a url", "", "", true},
		{"", "", "", true},
	}
	for _, c := range cases {
		o, r, err := parseGitHubURL(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && (o != c.owner || r != c.repo) {
			t.Errorf("%q: got %s/%s want %s/%s", c.in, o, r, c.owner, c.repo)
		}
	}
}
